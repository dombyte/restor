package config

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// Value formats restic accepts.
const (
	sizePattern      = `^[0-9]+[kKmMgGtT]?$`
	maxUnusedPattern = `^(unlimited|[0-9]+(\.[0-9]+)?%|[0-9]+[kKmMgGtT]?)$`
	withinPattern    = `^([0-9]+[ymdh])+$`
	backendPattern   = `^[a-z0-9]+\.[a-z0-9_.-]+$`
)

// validate checks every field of f (after lowerJobNames) and returns all problems.
func (f *File) validate() []error {
	v := &validator{}
	v.check(f.Version == Version, "version",
		fmt.Sprintf("must be %d (see the README for the changes)", Version))
	v.check(valueOr(f.Concurrency, DefaultConcurrency) >= 0, "concurrency", "must be >= 0")
	v.check(len(f.Jobs) > 0, "jobs", "at least one job is required")
	v.repository(f.Repository)
	v.at("defaults").common(f, f.Defaults)
	for _, name := range sortedKeys(f.Jobs) {
		jv := v.at("jobs." + name)
		jv.check(validName(name), "",
			`name must not be empty, start with "-" or contain "," or whitespace`)
		jv.job(f, name, f.Jobs[name])
	}
	v.cycle(f)
	return v.errs
}

// validName reports whether a job name or tag works as a restic tag: restic splits --tag
// values on commas (so "web,db" would also match the snapshots of "web"), and a leading
// "-" or whitespace breaks the command line.
func validName(name string) bool {
	return name != "" && !strings.HasPrefix(name, "-") &&
		!strings.ContainsFunc(name, func(r rune) bool { return r == ',' || unicode.IsSpace(r) })
}

// validator collects FieldErrors below one path. Sub-validators made by at share the
// list of problems.
type validator struct {
	path string
	errs []error
	root *validator
}

// at returns a validator for the fields below path.
func (v *validator) at(path string) *validator {
	return &validator{path: joinPath(v.path, path), root: v.top()}
}

func (v *validator) top() *validator {
	if v.root != nil {
		return v.root
	}
	return v
}

// check records problem for field unless ok.
func (v *validator) check(ok bool, field, problem string) {
	if !ok {
		top := v.top()
		top.errs = append(top.errs, &FieldError{Path: joinPath(v.path, field), Problem: problem})
	}
}

func (v *validator) matches(pattern, value, field, problem string) {
	v.check(regexp.MustCompile(pattern).MatchString(value), field,
		fmt.Sprintf("%q is not %s", value, problem))
}

func (v *validator) repository(r RepositoryConfig) {
	rv := v.at("repository")
	for _, key := range sortedKeys(r.BackendOptions) {
		rv.matches(backendPattern, key, "backend_options", "a backend option such as "+
			"s3.connections")
		rv.check(r.BackendOptions[key] != "", "backend_options."+key, "must not be empty")
	}
	rv.check(r.LimitUpload >= 0, "limit_upload", "must be >= 0")
	rv.check(r.LimitDownload >= 0, "limit_download", "must be >= 0")
	rv.check(r.RetryLock >= 0, "retry_lock", "must be >= 0")
	if r.MaxUnused != "" {
		rv.matches(maxUnusedPattern, r.MaxUnused, "max_unused",
			`a limit such as "5%", "1G" or "unlimited"`)
	}
}

// common checks the fields a job shares with the defaults section; f gives the job
// names.
func (v *validator) common(f *File, d Defaults) {
	durations := map[string]*Duration{
		"stop_timeout": d.StopTimeout, "start_timeout": d.StartTimeout,
		"backup_timeout": d.BackupTimeout,
	}
	for _, field := range sortedKeys(durations) {
		v.check(durations[field] == nil || *durations[field] >= 0, field, "must be >= 0")
	}
	lists := map[string][]string{
		"exclude": d.Exclude, "iexclude": d.IExclude, "exclude_file": d.ExcludeFile,
		"exclude_if_present": d.ExcludeIfPresent,
	}
	for _, field := range sortedKeys(lists) {
		v.check(!slices.Contains(lists[field], ""), field, "must not contain empty entries")
	}
	if d.ExcludeLargerThan != nil {
		v.matches(sizePattern, *d.ExcludeLargerThan, "exclude_larger_than",
			`a size such as "500M" or "2G"`)
	}
	v.tags("tags", d.Tags)
	for _, t := range d.Tags {
		_, isJob := f.Jobs[strings.ToLower(t)]
		v.check(!isJob, "tags", fmt.Sprintf("%q is the name of a job: forgetting that job "+
			"would also select these snapshots", t))
	}
	if d.Keep != nil {
		v.at("keep").keep(*d.Keep)
	}
}

func (v *validator) tags(field string, tags []string) {
	for _, t := range tags {
		v.check(validName(t), field, fmt.Sprintf(
			`%q: a tag must not be empty, start with "-" or contain "," or whitespace`, t))
	}
}

func (v *validator) keep(k Keep) {
	counts := map[string]int{
		"last": k.Last, "hourly": k.Hourly, "daily": k.Daily, "weekly": k.Weekly,
		"monthly": k.Monthly, "yearly": k.Yearly,
	}
	for _, field := range sortedKeys(counts) {
		v.check(counts[field] >= 0, field, "must be >= 0")
	}
	within := map[string]string{
		"within": k.Within, "within_hourly": k.WithinHourly, "within_daily": k.WithinDaily,
		"within_weekly": k.WithinWeekly, "within_monthly": k.WithinMonthly,
		"within_yearly": k.WithinYearly,
	}
	rule := false
	for _, field := range sortedKeys(within) {
		if within[field] != "" {
			rule = true
			v.matches(withinPattern, within[field], field, `a duration such as "30d" or "1y6m"`)
		}
	}
	for _, n := range counts {
		rule = rule || n > 0
	}
	v.tags("tags", k.Tags)
	v.check(rule || len(k.Tags) == 0, "tags", "needs another rule (last, daily, within, …): "+
		"with tags alone restic forgets every other snapshot, also the newest")
}

// job checks one job; f gives the defaults and the other job names.
func (v *validator) job(f *File, name string, j JobConfig) {
	v.common(f, j.Defaults)
	v.check(len(j.Sources) > 0, "sources", "required")
	v.check(!slices.Contains(j.Sources, ""), "sources", "must not contain empty entries")
	v.references(f, name, "before", j.Before)
	v.references(f, name, "after", j.After)
	switch j.Type {
	case TypeDockerCompose, TypePodmanCompose:
		v.compose(j)
	case TypeSystemd:
		v.systemd(j)
	case TypeFiles:
		v.files(j)
	default:
		v.check(false, "type", fmt.Sprintf("unsupported value %q (use %s, %s or %s, or "+
			"leave it out for files only)", j.Type, TypeDockerCompose, TypePodmanCompose,
			TypeSystemd))
	}
	if j.Type != TypeFiles && pick(j.Stop, f.Defaults.Stop, true) {
		v.timeouts(f.Defaults, j)
	}
	v.keepTags(f.Defaults, j)
}

// keepTags checks that the job's snapshots do not all carry a tag that keep.tags keeps:
// forget would then keep every snapshot of the job.
func (v *validator) keepTags(d Defaults, j JobConfig) {
	tags := list(j.Tags, d.Tags)
	for _, t := range pick(j.Keep, d.Keep, Keep{}).Tags {
		v.check(!slices.Contains(tags, t), "keep.tags", fmt.Sprintf("%q is in tags: every "+
			"snapshot of the job has it, so forget would never remove one", t))
	}
}

// references checks that before/after name other jobs.
func (v *validator) references(f *File, self, field string, names []string) {
	for _, n := range names {
		_, ok := f.Jobs[n]
		v.check(ok && n != self, field, fmt.Sprintf("%q is not another job", n))
	}
}

func (v *validator) compose(j JobConfig) {
	v.check(j.ComposeFile != "", "compose_file", "required for type "+j.Type)
	v.onlyFor(j.Units == nil, "units", TypeSystemd)
	v.onlyFor(j.Scope == "", "scope", TypeSystemd)
}

func (v *validator) systemd(j JobConfig) {
	v.check(len(j.Units) > 0, "units", "required for type systemd")
	v.check(slices.Contains([]string{"", ScopeSystem, ScopeUser}, j.Scope), "scope",
		fmt.Sprintf("must be %q or %q", ScopeSystem, ScopeUser))
	for _, s := range j.Services {
		v.check(slices.Contains(j.Units, s), "services", fmt.Sprintf("%q is not in units", s))
	}
	v.onlyFor(j.ComposeFile == "", "compose_file", "docker-compose or podman-compose")
}

// files checks that a job without a type sets nothing that only services use.
func (v *validator) files(j JobConfig) {
	unset := map[string]bool{
		"compose_file": j.ComposeFile == "", "units": j.Units == nil, "scope": j.Scope == "",
		"services": j.Services == nil, "stop": j.Stop == nil,
		"stop_timeout": j.StopTimeout == nil, "start_timeout": j.StartTimeout == nil,
	}
	for _, field := range sortedKeys(unset) {
		v.check(unset[field], field, "only for jobs with a type (docker-compose, "+
			"podman-compose, systemd)")
	}
}

func (v *validator) onlyFor(unset bool, field, types string) {
	v.check(unset, field, "only for type "+types)
}

// timeouts checks that a job that stops services waits for them.
func (v *validator) timeouts(d Defaults, j JobConfig) {
	stop := pick(j.StopTimeout, d.StopTimeout, Duration(DefaultStopTimeout))
	start := pick(j.StartTimeout, d.StartTimeout, Duration(DefaultStartTimeout))
	v.check(stop != 0, "stop_timeout", "must be > 0 when services are stopped")
	v.check(start != 0, "start_timeout", "must be > 0 when services are stopped")
}

// cycle reports the first chain of jobs that wait for themselves through before/after.
func (v *validator) cycle(f *File) {
	after := f.afterLists()
	done := map[string]bool{}
	for _, name := range sortedKeys(f.Jobs) {
		if c := findCycle(name, after, done, nil); c != nil {
			v.check(false, "jobs."+c[0], "before/after form a cycle: "+strings.Join(c, " → "))
			return
		}
	}
}

// findCycle walks the after lists depth-first from name and returns the first cycle, as
// the names from its start back to it. path holds the names being visited.
func findCycle(name string, after map[string][]string, done map[string]bool,
	path []string,
) []string {
	if i := slices.Index(path, name); i >= 0 {
		return append(slices.Clone(path[i:]), name)
	}
	if done[name] {
		return nil
	}
	for _, a := range after[name] {
		if c := findCycle(a, after, done, append(path, name)); c != nil {
			return c
		}
	}
	done[name] = true
	return nil
}
