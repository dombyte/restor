package config

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// ErrUnknownJob is returned by Select for a name that is not a job.
var ErrUnknownJob = errors.New("config: unknown job")

// Plan is the validated configuration with the defaults applied; it is read-only.
type Plan struct {
	// Concurrency is how many jobs run at once; 0 is unlimited.
	Concurrency int
	// Repository holds the repository settings.
	Repository Repository
	// OnFailure is the hook that runs once after a failed run.
	OnFailure string
	// Jobs are sorted by name.
	Jobs []Job
}

// Repository is the restic repository and the settings of every restic call.
type Repository struct {
	URL string
	// EnvFile is the absolute path of the env file, "" when there is none.
	EnvFile string
	// Env are the variables of EnvFile as sorted KEY=value pairs; only restic gets them.
	Env            []string
	BackendOptions map[string]string
	LimitUpload    int
	LimitDownload  int
	RetryLock      time.Duration
	Prune          bool
	MaxUnused      string
}

// Job is one job with the defaults applied.
type Job struct {
	// Name is the lowercase job name and the restic tag.
	Name string
	// Type is TypeFiles, TypeDockerCompose, TypePodmanCompose or TypeSystemd.
	Type        string
	ComposeFile string
	Units       []string
	// Scope is ScopeSystem or ScopeUser (systemd only).
	Scope    string
	Services []string
	Sources  []string
	// After are the jobs this job waits for (its after plus the before of the others),
	// sorted.
	After             []string
	Stop              bool
	StopTimeout       time.Duration
	StartTimeout      time.Duration
	BackupTimeout     time.Duration
	Keep              Keep
	Exclude           []string
	IExclude          []string
	ExcludeFile       []string
	ExcludeIfPresent  []string
	ExcludeCaches     bool
	ExcludeLargerThan string
	OneFileSystem     bool
	SkipIfUnchanged   bool
	Tags              []string
	Pre               string
	Post              string
	OnFailure         string
}

// StopsServices reports whether the job stops services during its backup.
func (j Job) StopsServices() bool { return j.Type != TypeFiles && j.Stop }

// Select returns the plan with only the named jobs (all for no names). Their order
// among each other is kept; ordering against jobs that are not selected is dropped.
func (p *Plan) Select(names []string) (*Plan, error) {
	if len(names) == 0 {
		return p, nil
	}
	want := map[string]bool{}
	for _, n := range names {
		want[strings.ToLower(n)] = true
	}
	out := *p
	out.Jobs = nil
	for _, j := range p.Jobs {
		if want[j.Name] {
			out.Jobs = append(out.Jobs, j)
			delete(want, j.Name)
		}
	}
	if len(want) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrUnknownJob,
			strings.Join(slices.Sorted(maps.Keys(want)), ", "))
	}
	return &out, nil
}

// plan validates f and builds the Plan; the env file is read here.
func (f *File) plan() (*Plan, []error) {
	errs := f.lowerJobNames()
	errs = append(errs, f.validate()...)
	env, err := envPairs(f.Repository.EnvFile)
	if err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return nil, errs
	}
	after := f.afterLists()
	jobs := make([]Job, 0, len(f.Jobs))
	for _, name := range sortedKeys(f.Jobs) {
		jobs = append(jobs, f.job(name, f.Jobs[name], after[name]))
	}
	r := f.Repository
	return &Plan{
		Concurrency: valueOr(f.Concurrency, DefaultConcurrency),
		Repository: Repository{
			URL: r.URL, EnvFile: r.EnvFile, Env: env, BackendOptions: r.BackendOptions,
			LimitUpload: r.LimitUpload, LimitDownload: r.LimitDownload,
			RetryLock: time.Duration(r.RetryLock), Prune: r.Prune, MaxUnused: r.MaxUnused,
		},
		OnFailure: f.OnFailure,
		Jobs:      jobs,
	}, nil
}

// lowerJobNames makes job names and their before/after references lowercase: names are
// case-insensitive and the lowercase name is the restic tag.
func (f *File) lowerJobNames() []error {
	var errs []error
	lower := make(map[string]JobConfig, len(f.Jobs))
	for _, name := range sortedKeys(f.Jobs) {
		key := strings.ToLower(name)
		if _, dup := lower[key]; dup {
			errs = append(errs, &FieldError{
				Path: "jobs." + key, Problem: "duplicate job name (names are case-insensitive)",
			})
		}
		j := f.Jobs[name]
		j.Before, j.After = lowerAll(j.Before), lowerAll(j.After)
		lower[key] = j
	}
	f.Jobs = lower
	return errs
}

// afterLists returns for each job the jobs it waits for: its own after, plus every job
// that names it in before.
func (f *File) afterLists() map[string][]string {
	after := map[string][]string{}
	for name, j := range f.Jobs {
		after[name] = append(after[name], j.After...)
		for _, b := range j.Before {
			after[b] = append(after[b], name)
		}
	}
	for name, list := range after {
		slices.Sort(list)
		after[name] = slices.Compact(list)
	}
	return after
}

// job applies the defaults to j.
func (f *File) job(name string, j JobConfig, after []string) Job {
	d := f.Defaults
	duration := func(job, def *Duration, fallback time.Duration) time.Duration {
		return time.Duration(pick(job, def, Duration(fallback)))
	}
	return Job{
		Name: name, Type: j.Type, ComposeFile: j.ComposeFile, Units: j.Units,
		Scope: or(j.Scope, ScopeSystem), Services: j.Services, Sources: j.Sources,
		After: after, Stop: pick(j.Stop, d.Stop, true),
		StopTimeout:   duration(j.StopTimeout, d.StopTimeout, DefaultStopTimeout),
		StartTimeout:  duration(j.StartTimeout, d.StartTimeout, DefaultStartTimeout),
		BackupTimeout: duration(j.BackupTimeout, d.BackupTimeout, 0),
		Keep:          pick(j.Keep, d.Keep, Keep{}),
		Exclude:       list(j.Exclude, d.Exclude), IExclude: list(j.IExclude, d.IExclude),
		ExcludeFile:       list(j.ExcludeFile, d.ExcludeFile),
		ExcludeIfPresent:  list(j.ExcludeIfPresent, d.ExcludeIfPresent),
		ExcludeCaches:     pick(j.ExcludeCaches, d.ExcludeCaches, false),
		ExcludeLargerThan: pick(j.ExcludeLargerThan, d.ExcludeLargerThan, ""),
		OneFileSystem:     pick(j.OneFileSystem, d.OneFileSystem, false),
		SkipIfUnchanged:   pick(j.SkipIfUnchanged, d.SkipIfUnchanged, false),
		Tags:              list(j.Tags, d.Tags),
		Pre:               pick(j.Pre, d.Pre, ""), Post: pick(j.Post, d.Post, ""),
		OnFailure: pick(j.OnFailure, d.OnFailure, ""),
	}
}

// envPairs reads the env file as sorted KEY=value pairs; none without a file.
func envPairs(path string) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	env, err := loadEnvFile(path)
	if err != nil {
		return nil, &FieldError{Path: "repository.env_file", Problem: err.Error()}
	}
	var errs []error
	pairs := make([]string, 0, len(env))
	for _, k := range sortedKeys(env) {
		// exec rejects names with "=" and NUL bytes anywhere, which would fail every command.
		if strings.ContainsRune(k+env[k], 0) {
			errs = append(errs, &FieldError{
				Path: "repository.env_file", Problem: k + ": contains a NUL byte",
			})
		}
		pairs = append(pairs, k+"="+env[k])
	}
	return pairs, errors.Join(errs...)
}

// pick returns the job's value, else the default's, else fallback.
func pick[T any](job, def *T, fallback T) T {
	if job != nil {
		return *job
	}
	return valueOr(def, fallback)
}

func valueOr[T any](v *T, fallback T) T {
	if v != nil {
		return *v
	}
	return fallback
}

// list returns the job's list when it is set (also when empty), else the default's.
func list(job, def []string) []string {
	if job != nil {
		return job
	}
	return def
}

func or(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}

func lowerAll(list []string) []string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = strings.ToLower(s)
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}
