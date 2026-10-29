// Package app is the composition root: it loads the configuration, builds every
// component with its concrete dependencies and runs one backup cycle. It is the only
// package that imports config and names concrete types.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/rs/zerolog"

	"github.com/dombyte/restor/internal/backup"
	"github.com/dombyte/restor/internal/command"
	"github.com/dombyte/restor/internal/config"
	"github.com/dombyte/restor/internal/lock"
	"github.com/dombyte/restor/internal/restic"
	"github.com/dombyte/restor/internal/servicemanager"
	"github.com/dombyte/restor/internal/util"
)

const (
	// columnPadding separates the columns of Describe.
	columnPadding = 2
)

// envFileOthersMask are the permission bits that let group or others access the env file.
const envFileOthersMask os.FileMode = 0o077

var (
	// ErrUnknownType is returned by CreateServiceManager for an unknown job type.
	ErrUnknownType = errors.New("app: unknown job type")
	// ErrResticVersion is returned by Run when restic is older than the oldest release
	// restor supports.
	ErrResticVersion = errors.New("app: restic is too old")
	// ErrNoJobs is returned by Run for a configuration without jobs: it is valid for the
	// commands that only need the repository, but has nothing to back up.
	ErrNoJobs = errors.New("app: no jobs configured")
)

// Options are the inputs of New.
type Options struct {
	// ConfigPath is the configuration file.
	ConfigPath string
	// Jobs selects the jobs to run; empty runs all.
	Jobs []string
	// Getenv resolves ${VAR} references in the configuration.
	Getenv func(string) string
	// Environ is the process environment the external programs inherit.
	Environ []string
	// Log is the root logger.
	Log zerolog.Logger
}

// App is one wired backup run.
type App struct {
	plan         *config.Plan
	manager      *backup.Manager
	restic       *restic.Client
	resticRunner *command.Runner
	log          zerolog.Logger
}

// New loads the configuration and wires all components; it starts nothing.
func New(o Options) (*App, error) {
	plan, err := config.Load(o.ConfigPath, o.Getenv)
	if err != nil {
		return nil, err
	}
	if plan, err = plan.Select(o.Jobs); err != nil {
		return nil, err
	}
	logJobs(o.Log, plan.Jobs)
	resticEnv := resticEnv(o.Environ, plan.Repository)
	warnConfig(o.Log, plan, resticEnv)
	warnMissingBinaries(o.Log, plan.Jobs, exec.LookPath)

	resticRunner := command.New(resticEnv, component(o.Log, "restic"))
	resticClient, err := restic.New(resticSettings(plan.Repository),
		restic.Deps{Runner: resticRunner, Log: component(o.Log, "restic")})
	if err != nil {
		return nil, err
	}
	manager, err := createManager(plan, managerDeps{
		runner: command.New(o.Environ, component(o.Log, "command")), restic: resticClient,
		clock: util.NewRealClock(), lockDir: lockDir(o.Getenv), log: o.Log,
	})
	if err != nil {
		return nil, err
	}
	return &App{
		plan: plan, manager: manager, restic: resticClient, resticRunner: resticRunner,
		log: o.Log,
	}, nil
}

// Run checks the restic version, then runs one backup cycle.
func (a *App) Run(ctx context.Context) error {
	if len(a.plan.Jobs) == 0 {
		return ErrNoJobs
	}
	if err := a.checkRestic(ctx); err != nil {
		return err
	}
	return a.manager.Run(ctx)
}

// checkRestic fails when restic is missing or older than restor supports, before any
// service is stopped, and warns when it is newer than restor was tested with.
func (a *App) checkRestic(ctx context.Context) error {
	v, err := a.restic.Version(ctx)
	if err != nil {
		return err
	}
	oldest, newest := restic.Supported()
	if !v.AtLeast(oldest) {
		return fmt.Errorf("%w: %s, restor needs %s or newer", ErrResticVersion, v, oldest)
	}
	log := a.log.Info()
	if !newest.AtLeast(v) {
		log = a.log.Warn().Stringer("newest_tested", newest)
	}
	log.Stringer("restic_version", v).Msg(resticVersionMessage(newest.AtLeast(v)))
	return nil
}

func resticVersionMessage(tested bool) string {
	if tested {
		return "restic found"
	}
	return "restic is newer than the releases restor was tested with; please report problems"
}

// Stdio are the standard streams of `restor restic`.
type Stdio = command.Stdio

// Restic runs restic with args, the repository settings and env file, connected to
// stdio: `restor restic snapshots` and the like.
func (a *App) Restic(ctx context.Context, stdio Stdio, args []string) error {
	global := resticSettings(a.plan.Repository).GlobalArgs
	return a.resticRunner.Attach(ctx, stdio, "restic", slices.Concat(global, args)...)
}

// Snapshots lists the snapshots of the selected jobs (all jobs when none were named),
// connected to stdio: `restor snapshots web` is `restor restic snapshots --tag=web` with
// the job name checked against the configuration.
func (a *App) Snapshots(ctx context.Context, stdio Stdio) error {
	tags := make([]string, 0, len(a.plan.Jobs))
	for _, j := range a.plan.Jobs {
		tags = append(tags, j.Name)
	}
	args := slices.Concat(resticSettings(a.plan.Repository).GlobalArgs, []string{"snapshots"},
		restic.SnapshotsFlags{Tags: tags}.Args())
	return a.resticRunner.Attach(ctx, stdio, "restic", args...)
}

// Describe writes the validated plan to w: the jobs in the order they start with
// concurrency 1, with their type, what they wait for and what they back up, then each
// distinct retention policy once, labelled in the table. The repository URL is left out,
// it may hold credentials.
func (a *App) Describe(w io.Writer) error {
	jobs := a.orderedJobs()
	labels, groups := keepGroups(jobs)
	tw := tabwriter.NewWriter(w, 0, 0, columnPadding, ' ', 0)
	_, _ = fmt.Fprintf(tw, "concurrency:\t%d (0 = all at once)\n", a.plan.Concurrency)
	_, _ = fmt.Fprintf(tw, "prune:\t%t\n", a.plan.Repository.Prune)
	if len(jobs) == 0 {
		_, _ = fmt.Fprintln(tw, "\nno jobs configured: restor run has nothing to back up")
		return tw.Flush()
	}
	_, _ = fmt.Fprintln(tw, "\nJOB\tTYPE\tAFTER\tSTOP\tSTART ALL\tKEEP\tSOURCES")
	for _, j := range jobs {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%t\t%t\t%s\t%s\n", j.Name, or(j.Type, "files"),
			or(strings.Join(j.After, ","), "-"), j.StopsServices(), j.StartsAll(),
			labels[j.Name], strings.Join(j.Sources, " "))
	}
	_, _ = fmt.Fprintln(tw, "\nKEEP")
	for _, g := range groups {
		_, _ = fmt.Fprintf(tw, "  %s\t%s\t(%s)\n", g.label, g.policy, strings.Join(g.jobs, ", "))
	}
	return tw.Flush()
}

// orderedJobs returns the jobs in the order they start with concurrency 1.
func (a *App) orderedJobs() []config.Job {
	byName := map[string]config.Job{}
	for _, j := range a.plan.Jobs {
		byName[j.Name] = j
	}
	order := a.manager.Order()
	jobs := make([]config.Job, 0, len(order))
	for _, name := range order {
		jobs = append(jobs, byName[name])
	}
	return jobs
}

// keepGroup is one retention policy and the jobs that use it.
type keepGroup struct {
	label, policy string
	jobs          []string
}

// keepGroups labels each distinct retention policy of jobs A, B, … in order of first use
// ("-" for jobs that are never forgotten) and returns the label of every job and the
// groups in label order.
func keepGroups(jobs []config.Job) (map[string]string, []keepGroup) {
	labels := map[string]string{}
	index := map[string]int{} // policy → position in groups
	var groups []keepGroup
	next := 0 // the next letter label
	for _, j := range jobs {
		policy := strings.ReplaceAll(strings.Join(keep(j.Keep).Args(), " "), "--keep-", "")
		i, seen := index[policy]
		if !seen {
			g := keepGroup{label: "-", policy: "never forgotten"}
			if policy != "" {
				g = keepGroup{label: keepLabel(next), policy: policy}
				next++
			}
			i = len(groups)
			index[policy] = i
			groups = append(groups, g)
		}
		groups[i].jobs = append(groups[i].jobs, j.Name)
		labels[j.Name] = groups[i].label
	}
	return labels, groups
}

// keepLabel returns the n-th label: A … Z, then AA, AB, …
func keepLabel(n int) string {
	const letters = 26
	if n < letters {
		return string(rune('A' + n))
	}
	return keepLabel(n/letters-1) + keepLabel(n%letters)
}

// ShutdownTimeout returns how long Run may take after its context was cancelled, so that
// every job can restart the services it stopped.
func (a *App) ShutdownTimeout() time.Duration { return a.manager.ShutdownTimeout() }

type managerDeps struct {
	runner  *command.Runner
	restic  *restic.Client
	clock   util.Clock
	lockDir string
	log     zerolog.Logger
}

// SystemConfig is the configuration file of root, a system installation.
const SystemConfig = "/etc/restor/config.yaml"

// DefaultConfig returns the configuration file of the user uid: SystemConfig for root,
// otherwise restor/config.yaml in $XDG_CONFIG_HOME or ~/.config. It is a fixed rule, not a
// search path: falling back to another file could forget snapshots in another repository
// than intended. Empty when the user has no absolute config or home directory.
func DefaultConfig(uid int, getenv func(string) string) string {
	if uid == 0 {
		return SystemConfig
	}
	// The XDG spec ignores relative paths; so does restor, for HOME too.
	dir := getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(dir) {
		home := getenv("HOME")
		if !filepath.IsAbs(home) {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "restor", "config.yaml")
}

// lockDir picks the lock directory of the user running restor.
func lockDir(getenv func(string) string) string {
	return lock.DefaultDir(lock.User{
		UID: os.Getuid(), GOOS: runtime.GOOS,
		RuntimeDir: getenv("XDG_RUNTIME_DIR"), TempDir: os.TempDir(),
	})
}

func createManager(plan *config.Plan, d managerDeps) (*backup.Manager, error) {
	locker, err := lock.New(lock.Settings{Dir: d.lockDir})
	if err != nil {
		return nil, err
	}
	smDeps := servicemanager.Deps{Runner: d.runner, Clock: d.clock}
	jobs := make([]backup.Job, 0, len(plan.Jobs))
	for _, j := range plan.Jobs {
		services, err := CreateServiceManager(j, smDeps)
		if err != nil {
			return nil, fmt.Errorf("app: job %s: %w", j.Name, err)
		}
		jobs = append(jobs, backup.Job{Settings: jobSettings(j), Services: services})
	}
	return backup.NewManager(backup.Settings{
		Concurrency: plan.Concurrency, Prune: plan.Repository.Prune,
		Post: plan.Post, OnFailure: plan.OnFailure,
	}, jobs, backup.Deps{
		Restic: d.restic, Locker: locker, Hooks: d.runner, Log: component(d.log, "backup"),
	})
}

// CreateServiceManager builds the service manager of a job's type.
func CreateServiceManager(j config.Job, d servicemanager.Deps) (backup.Services, error) {
	switch j.Type {
	case config.TypeDockerCompose:
		return servicemanager.NewCompose(
			servicemanager.ComposeSettings{Binary: "docker", File: j.ComposeFile}, d)
	case config.TypePodmanCompose:
		return servicemanager.NewCompose(
			servicemanager.ComposeSettings{Binary: "podman", File: j.ComposeFile}, d)
	case config.TypeSystemd:
		return servicemanager.NewSystemd(servicemanager.SystemdSettings{
			Units: j.Units, User: j.Scope == config.ScopeUser,
		}, d)
	case config.TypeFiles:
		// Keyed by job: file jobs share no services and may run at the same time.
		return servicemanager.NewNoop(servicemanager.NoopSettings{LockKey: "files-" + j.Name}),
			nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownType, j.Type)
	}
}

func jobSettings(j config.Job) backup.JobSettings {
	return backup.JobSettings{
		Name: j.Name, After: j.After, Services: j.Services, Sources: j.Sources,
		StopServices: j.StopsServices(), StartAll: j.StartsAll(), StopTimeout: j.StopTimeout,
		StartTimeout: j.StartTimeout, BackupTimeout: j.BackupTimeout,
		BackupArgs: backupFlags(j).Args(), ForgetArgs: keep(j.Keep).Args(),
		Pre: j.Pre, Post: j.Post, OnFailure: j.OnFailure,
	}
}

func backupFlags(j config.Job) restic.BackupFlags {
	return restic.BackupFlags{
		Exclude: j.Exclude, IExclude: j.IExclude, ExcludeFile: j.ExcludeFile,
		ExcludeIfPresent: j.ExcludeIfPresent, ExcludeCaches: j.ExcludeCaches,
		ExcludeLargerThan: j.ExcludeLargerThan, OneFileSystem: j.OneFileSystem,
		SkipIfUnchanged: j.SkipIfUnchanged, Tags: j.Tags,
	}
}

func keep(k config.Keep) restic.Keep {
	return restic.Keep{
		Last: k.Last, Hourly: k.Hourly, Daily: k.Daily, Weekly: k.Weekly, Monthly: k.Monthly,
		Yearly: k.Yearly, Within: k.Within, WithinHourly: k.WithinHourly,
		WithinDaily: k.WithinDaily, WithinWeekly: k.WithinWeekly,
		WithinMonthly: k.WithinMonthly, WithinYearly: k.WithinYearly, Tags: k.Tags,
	}
}

func resticSettings(r config.Repository) restic.Settings {
	return restic.Settings{
		GlobalArgs: restic.Repository{
			BackendOptions: r.BackendOptions, LimitUpload: r.LimitUpload,
			LimitDownload: r.LimitDownload, RetryLock: r.RetryLock,
		}.Args(),
		PruneArgs: restic.PruneFlags{MaxUnused: r.MaxUnused}.Args(),
	}
}

// resticEnv is the environment of restic: the process environment, the env file and the
// repository in RESTIC_REPOSITORY, so it never shows up in arguments or debug logs. An
// empty URL leaves a RESTIC_REPOSITORY from the environment alone.
func resticEnv(environ []string, r config.Repository) []string {
	env := slices.Concat(environ, r.Env)
	if r.URL != "" {
		env = append(env, "RESTIC_REPOSITORY="+r.URL)
	}
	return env
}

func component(log zerolog.Logger, name string) zerolog.Logger {
	return log.With().Str("component", name).Logger()
}

func logJobs(log zerolog.Logger, jobs []config.Job) {
	for _, j := range jobs {
		log.Info().Str("job", j.Name).Str("type", or(j.Type, "files")).
			Strs("sources", j.Sources).Strs("after", j.After).
			Bool("stop", j.StopsServices()).Bool("start_all", j.StartsAll()).
			Msg("job configured")
	}
}

func or(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// warnConfig warns about settings that load fine but are probably not what was meant.
func warnConfig(log zerolog.Logger, plan *config.Plan, resticEnv []string) {
	if !hasRepository(resticEnv) {
		log.Warn().Msg("no restic repository: set repository.url, or RESTIC_REPOSITORY " +
			"in the env file")
	}
	warnEnvFileMode(log, plan.Repository.EnvFile)
	warnRelativeHooks(log, plan)
}

// warnRelativeHooks warns about hooks whose program is a relative path such as
// ./backup.sh: it resolves against the working directory, which is / (system) or the home
// directory (user) under systemd, not against the config directory.
func warnRelativeHooks(log zerolog.Logger, plan *config.Plan) {
	// The setting as a config path, so the run-level hooks need no job.
	hooks := [][2]string{{"post", plan.Post}, {"on_failure", plan.OnFailure}}
	for _, j := range plan.Jobs {
		path := "jobs." + j.Name + "."
		hooks = append(hooks, [2]string{path + "pre", j.Pre},
			[2]string{path + "post", j.Post}, [2]string{path + "on_failure", j.OnFailure})
	}
	for _, h := range hooks {
		fields := strings.Fields(h[1])
		if len(fields) > 0 && strings.Contains(fields[0], "/") && !filepath.IsAbs(fields[0]) {
			log.Warn().Str("field", h[0]).Str("hook", fields[0]).
				Msg("hook path is relative to the working directory, not to the config " +
					"file; use an absolute path")
		}
	}
}

// warnEnvFileMode warns when the env file, which usually holds the restic password, can
// be read or changed by other users. It is only a warning: the file still works.
func warnEnvFileMode(log zerolog.Logger, path string) {
	if path == "" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		return // config.Load already read it; a race here is not worth a warning
	}
	if info.Mode().Perm()&envFileOthersMask != 0 {
		log.Warn().Str("env_file", path).Str("mode", info.Mode().Perm().String()).
			Msg("env_file is accessible by other users; restrict it with chmod 600")
	}
}

// hasRepository reports whether env (KEY=value, later entries win) names a repository.
func hasRepository(env []string) bool {
	vars := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		vars[k] = v
	}
	return vars["RESTIC_REPOSITORY"] != "" || vars["RESTIC_REPOSITORY_FILE"] != ""
}

// warnMissingBinaries warns about programs the run will need but PATH does not have.
func warnMissingBinaries(log zerolog.Logger, jobs []config.Job,
	lookPath func(string) (string, error),
) {
	binaries := []string{"restic"}
	for _, j := range jobs {
		switch j.Type {
		case config.TypeDockerCompose:
			binaries = append(binaries, "docker")
		case config.TypePodmanCompose:
			binaries = append(binaries, "podman")
		case config.TypeSystemd:
			binaries = append(binaries, "systemctl")
		}
	}
	slices.Sort(binaries)
	for _, b := range slices.Compact(binaries) {
		if _, err := lookPath(b); err != nil {
			log.Warn().Str("binary", b).Msg("binary not found in PATH; commands using it will fail")
		}
	}
}
