// Package backup orchestrates one backup run: per job lock, pre hook, stop services,
// restic backup, start services, post and on_failure hooks; the jobs run in the order
// and with the concurrency the schedule allows. Then forget per job, one repository-wide
// prune and unlock, check, and the run-level post and on_failure hooks. It talks to
// restic, the service managers, the locker and the hook runner only through the
// interfaces declared here.
package backup

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/dombyte/restor/internal/util"
)

// shutdownGrace is what a cancelled run needs besides restarting services: the running
// command gets SIGTERM and is killed 10 s later (package command), then the run unwinds.
const shutdownGrace = time.Minute

var (
	// ErrMissingDependency is returned by NewManager for a missing dependency.
	ErrMissingDependency = errors.New("backup: missing dependency")
	// ErrJobsFailed is returned by Run when at least one job failed.
	ErrJobsFailed = errors.New("backup: jobs failed")
	// ErrRequiredJobFailed is the error of a job that was skipped because a job it
	// requires failed.
	ErrRequiredJobFailed = errors.New("skipped, required job failed")
	// ErrRequiresNotAfter is returned by NewManager for a job that requires a job it does
	// not run after: the schedule would not wait for the result it checks.
	ErrRequiresNotAfter = errors.New("backup: a required job is not in after")
)

// Restic runs restic commands against the repository.
type Restic interface {
	Backup(ctx context.Context, tag string, sources, args []string) (string, error)
	Forget(ctx context.Context, tag string, args []string) error
	Prune(ctx context.Context) error
	Check(ctx context.Context) error
	Unlock(ctx context.Context) error
}

// Services stops and starts the services of one job. LockKeys name what it manages
// (a compose file, each systemd unit); a job runs only while it holds all of them, and
// jobs that share a key never run at the same time. Running returns the services among
// requested (all when empty) that run now: only those are stopped and started again.
// All returns requested (all when empty) whether running or not, for JobSettings.StartAll.
// Stop and Start report false when the services did not reach the expected state within
// the timeout.
type Services interface {
	LockKeys() []string
	Running(ctx context.Context, requested []string) ([]string, error)
	All(ctx context.Context, requested []string) ([]string, error)
	Stop(ctx context.Context, services []string, timeout time.Duration) (bool, error)
	Start(ctx context.Context, services []string, timeout time.Duration) (bool, error)
}

// Locker keeps two runs from working on the same services at the same time.
type Locker interface {
	Lock(key string) (release func() error, err error)
}

// Runner executes the hook commands with extra environment variables.
type Runner interface {
	RunEnv(ctx context.Context, env []string, name string, args ...string) ([]byte, error)
}

// Settings configure a Manager.
type Settings struct {
	// Concurrency is how many jobs run at once; 0 is unlimited.
	Concurrency int
	// Prune runs prune and unlock after the run when every job has a complete snapshot.
	Prune bool
	// Check runs check after forget and prune, whatever the jobs' results.
	Check bool
	// Post is the hook that runs once after every run, with a summary of it.
	Post string
	// OnFailure is the hook that runs once after a run in which something failed.
	OnFailure string
}

// Job is one job and its service manager.
type Job struct {
	Settings JobSettings
	Services Services
}

// Deps are the dependencies of a Manager.
type Deps struct {
	Restic Restic
	Locker Locker
	Hooks  Runner
	Log    zerolog.Logger
}

// Manager runs the backup of all jobs.
type Manager struct {
	s    Settings
	jobs []Job
	d    Deps
}

// NewManager returns a Manager for jobs; among the jobs that may start, the earlier one
// in jobs starts first.
func NewManager(s Settings, jobs []Job, d Deps) (*Manager, error) {
	reqs := []util.Requirement{
		{Name: "Restic", OK: d.Restic != nil},
		{Name: "Locker", OK: d.Locker != nil},
		{Name: "Hooks", OK: d.Hooks != nil},
	}
	for _, j := range jobs {
		reqs = append(reqs, util.Requirement{
			Name: "Jobs[" + j.Settings.Name + "].Services", OK: j.Services != nil,
		})
	}
	if err := util.RequireAll(ErrMissingDependency, reqs...); err != nil {
		return nil, err
	}
	for _, j := range jobs {
		for _, r := range j.Settings.Requires {
			if !slices.Contains(j.Settings.After, r) {
				return nil, fmt.Errorf("%w: %s requires %s", ErrRequiresNotAfter,
					j.Settings.Name, r)
			}
		}
	}
	return &Manager{s: s, jobs: jobs, d: d}, nil
}

// Run backs up every job; a job whose required job failed is skipped and fails. Then it
// runs forget for the jobs with a retention policy and a complete snapshot, with Prune,
// prune and unlock when every snapshot is complete, and with Check, check. A cancelled
// run does no maintenance. Job failures are logged here and summarized in the returned
// ErrJobsFailed; forget, prune, unlock and check failures are only logged. Then the Post
// hook runs, and when anything failed, the OnFailure hook.
func (m *Manager) Run(ctx context.Context) error {
	m.d.Log.Info().Int("concurrency", m.s.Concurrency).Int("jobs", len(m.jobs)).
		Stringer("shutdown_timeout", m.ShutdownTimeout()).Msg("starting backup run")

	results := m.runJobs(ctx)
	var n notes
	if ctx.Err() == nil {
		n = m.maintain(ctx, results)
	}
	s := m.summarize(results, n)
	m.runHooks(ctx, s)
	if len(s.failed) > 0 {
		return fmt.Errorf("%w: %d of %d", ErrJobsFailed, len(s.failed), len(m.jobs))
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	return nil
}

// ShutdownTimeout returns how long the run may take after its context was cancelled: the
// running commands end, then each running job restarts the services it stopped and runs
// its post hook. Only the jobs running at the cancel do that (no further job starts), and
// they do it at the same time, so the slowest job counts, not the sum.
func (m *Manager) ShutdownTimeout() time.Duration {
	var slowest time.Duration
	for _, j := range m.jobs {
		var d time.Duration
		if j.Settings.StopServices {
			d += j.Settings.StartTimeout + restartGrace
		}
		if strings.TrimSpace(j.Settings.Post) != "" {
			d += postHookGrace
		}
		slowest = max(slowest, d)
	}
	return shutdownGrace + slowest
}

// Order returns the job names in the order they start with concurrency 1.
func (m *Manager) Order() []string {
	s := newSchedule(m.jobs, 1)
	var names []string
	for next := s.next(); len(next) > 0; next = s.next() {
		names = append(names, m.jobs[next[0]].Settings.Name)
		s.finish(next[0])
	}
	return names
}

// runJobs runs the jobs as the schedule allows and returns the result of each, by index.
// It starts one goroutine per job and waits for all of them before it returns.
func (m *Manager) runJobs(ctx context.Context) []result {
	results := make([]result, len(m.jobs)) // each goroutine writes only its own index
	s := newSchedule(m.jobs, m.s.Concurrency)
	done := make(chan int)
	for {
		if ctx.Err() == nil {
			for _, i := range s.next() {
				// The required jobs have finished: they are in After.
				required := m.failedRequirement(s, results, i)
				go func() {
					results[i] = m.startJob(ctx, m.jobs[i], required)
					done <- i
				}()
			}
		}
		if s.running == 0 {
			return results
		}
		s.finish(<-done)
	}
}

// startJob runs one job: skipped when the job it requires failed, else a backup.
func (m *Manager) startJob(ctx context.Context, j Job, failedRequirement string) result {
	switch {
	case failedRequirement != "":
		return m.skipJob(ctx, j, failedRequirement)
	default:
		return m.runJob(ctx, j)
	}
}

// failedRequirement returns the first job that job i requires and that failed, or "".
// Required jobs that are not part of the run are ignored, like their ordering.
func (m *Manager) failedRequirement(s *schedule, results []result, i int) string {
	for _, name := range m.jobs[i].Settings.Requires {
		if j, ok := s.index[name]; ok && results[j].outcome == outcomeFailed {
			return name
		}
	}
	return ""
}

// maintain runs forget for the jobs with a retention policy and a complete snapshot,
// then one prune and unlock when every job has one, then check. They run after all
// backups because they need exclusive repository locks. A failed or incomplete backup
// keeps its job's history and skips prune, so a broken run never removes data; services
// that did not come back fail their job, but not its backup.
func (m *Manager) maintain(ctx context.Context, results []result) notes {
	forgot, n := m.forgetAll(ctx, results)
	if m.s.Prune && forgot {
		n.add(m.pruneIfComplete(ctx, results))
	}
	if m.s.Check {
		n.add(m.check(ctx))
	}
	return n
}

// pruneIfComplete runs prune and unlock when every job has a complete snapshot.
func (m *Manager) pruneIfComplete(ctx context.Context, results []result) notes {
	for _, r := range results {
		if r.backup != outcomeComplete {
			msg := "prune skipped because a backup failed or is incomplete"
			m.d.Log.Warn().Msg(msg)
			return notes{warnings: []string{msg}}
		}
	}
	return m.prune(ctx)
}

// check verifies the repository; a failure is logged and returned. It runs also after
// failed jobs: what it checks is the data that is already there.
func (m *Manager) check(ctx context.Context) notes {
	if err := m.d.Restic.Check(ctx); err != nil {
		m.d.Log.Error().Err(err).Msg("check failed")
		return notes{failures: []string{err.Error()}}
	}
	m.d.Log.Info().Msg("check completed")
	return notes{}
}

// forgetAll runs forget for the jobs with a retention policy and a complete snapshot. It
// reports whether there was one, and what failed or was skipped.
func (m *Manager) forgetAll(ctx context.Context, results []result) (bool, notes) {
	forgot := false
	var n notes
	for i, j := range m.jobs {
		if len(j.Settings.ForgetArgs) == 0 {
			continue
		}
		if results[i].backup == outcomeComplete {
			forgot = true
			if err := m.forget(ctx, j.Settings); err != nil {
				n.failures = append(n.failures, err.Error())
			}
			continue
		}
		skipped := "forget skipped because the backup failed"
		if results[i].backup == outcomeIncomplete {
			skipped = "forget skipped because the snapshot is incomplete"
		}
		m.d.Log.Warn().Str("job", j.Settings.Name).Msg(skipped)
		n.warnings = append(n.warnings, j.Settings.Name+": "+skipped)
	}
	return forgot, n
}

// forget applies the retention policy of one job; a failure is logged and returned.
func (m *Manager) forget(ctx context.Context, s JobSettings) error {
	log := m.d.Log.With().Str("job", s.Name).Logger()
	if err := m.d.Restic.Forget(ctx, s.Name, s.ForgetArgs); err != nil {
		log.Error().Err(err).Msg("forget failed")
		return err
	}
	log.Info().Strs("keep", s.ForgetArgs).Msg("forget completed")
	return nil
}

// prune runs prune and unlock for the whole repository; failures are logged and returned.
func (m *Manager) prune(ctx context.Context) notes {
	var n notes
	if err := m.d.Restic.Prune(ctx); err != nil {
		m.d.Log.Error().Err(err).Msg("prune failed")
		n.failures = append(n.failures, err.Error())
	} else {
		m.d.Log.Info().Msg("prune completed")
	}
	if err := m.d.Restic.Unlock(ctx); err != nil {
		m.d.Log.Error().Err(err).Msg("unlock failed")
		n.failures = append(n.failures, err.Error())
	}
	return n
}

// runHooks runs the run-level post hook, then the on_failure hook when anything failed.
func (m *Manager) runHooks(ctx context.Context, s summary) {
	m.runHook(ctx, hookPost, m.s.Post, s)
	if len(s.failures) > 0 {
		m.runHook(ctx, hookOnFailure, m.s.OnFailure, s)
	}
}

// runHook runs a run-level hook with the summary of the run in its environment; its own
// failure is only logged. After a cancel it is skipped, like every hook.
func (m *Manager) runHook(ctx context.Context, kind, command string, s summary) {
	if strings.TrimSpace(command) == "" {
		return
	}
	if ctx.Err() != nil {
		m.d.Log.Warn().Str("hook", kind).Msg("hook skipped because the run was cancelled")
		return
	}
	if err := m.hook(ctx, command, s.env(kind)); err != nil {
		m.d.Log.Error().Err(err).Str("hook", kind).Msg("hook failed")
	}
}
