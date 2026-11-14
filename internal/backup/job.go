package backup

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

// restartGrace is added to the start timeout to bound restarting the services after the
// run was cancelled (the run context can no longer be used for that). It is above what a
// service manager adds to the timeout itself (2 min for the start command, plus one status
// query of at most 30 s), so a slow start ends with a timeout error, not a cancelled
// restart.
const restartGrace = 3 * time.Minute

// postHookGrace bounds the post hook of a job after the run was cancelled: it still runs,
// to undo what the pre hook did (e.g. a maintenance mode), but must not hold up the
// shutdown for long.
const postHookGrace = time.Minute

// Values of RESTOR_HOOK in the hook environment.
const (
	hookPre       = "pre"
	hookPost      = "post"
	hookOnFailure = "on_failure"
)

// Values of RESTOR_STATUS in the hook environment.
const (
	statusSuccess    = "success"
	statusIncomplete = "incomplete"
	statusFailed     = "failed"
)

const (
	// warningSeparator joins warnings and errors in the hook environment.
	warningSeparator = "; "
	// maxEnvVar bounds one variable of a hook's environment, well below the 128 KiB that
	// Linux accepts.
	maxEnvVar = 32 << 10
	// truncated ends a variable that was cut to maxEnvVar.
	truncated = " … (truncated)"
)

// ErrNotStarted is the error of a job whose services were not running within the start
// timeout after the backup.
var ErrNotStarted = errors.New("services did not start")

// JobSettings configure the backup of one job.
type JobSettings struct {
	// Name is the job name and the restic tag.
	Name string
	// After are the jobs that must finish before this one starts.
	After []string
	// Requires are jobs (also in After) whose failure skips this one: it fails without
	// running.
	Requires []string
	// Services limits the stopped services; empty means all of the service manager's.
	Services []string
	// Sources are the positional arguments of the backup: the backed up paths, or the
	// command whose output restic backs up (BackupArgs say which).
	Sources []string
	// StopServices stops the services during the backup.
	StopServices bool
	// StartAll also starts the services that were not running when the backup began.
	StartAll bool
	// StopTimeout and StartTimeout bound waiting for the services.
	StopTimeout  time.Duration
	StartTimeout time.Duration
	// BackupTimeout cancels restic when the backup takes longer; 0 is no limit.
	BackupTimeout time.Duration
	// BackupArgs are the restic backup flags.
	BackupArgs []string
	// ForgetArgs are the restic forget flags (the retention policy); empty skips forget.
	ForgetArgs []string
	// Pre, Post and OnFailure are hooks, split on whitespace (no shell).
	Pre, Post, OnFailure string
}

// outcome is how the backup of one job ended.
type outcome int

const (
	// The zero value: not started because the run was cancelled.
	_ outcome = iota
	// outcomeFailed: no snapshot, the job failed.
	outcomeFailed
	// outcomeIncomplete: backed up, but the snapshot misses files or was not reported.
	outcomeIncomplete
	// outcomeComplete: backed up with a complete snapshot.
	outcomeComplete
)

// result is how a job ended, with its snapshot ID, why it failed and what it warned
// about. backup is how the backup alone ended: a job whose services did not come back
// fails, but its snapshot still counts for forget and prune.
type result struct {
	outcome    outcome
	backup     outcome
	snapshotID string
	err        error
	warnings   []string
}

// failure is the result of a job that saved no snapshot.
func failure(err error) result {
	return result{outcome: outcomeFailed, backup: outcomeFailed, err: err}
}

// backedUp is the result of a job that saved a snapshot (or found nothing changed).
func backedUp(o outcome, snapshotID string) result {
	return result{outcome: o, backup: o, snapshotID: snapshotID}
}

// status is the RESTOR_STATUS of r.
func (r result) status() string {
	switch r.outcome {
	case outcomeComplete:
		return statusSuccess
	case outcomeIncomplete:
		return statusIncomplete
	default:
		return statusFailed
	}
}

// jobRun is one job while it runs: its logger and the warnings for its hooks.
type jobRun struct {
	Job
	log      zerolog.Logger
	warnings []string
}

func (m *Manager) newRun(j Job) *jobRun {
	return &jobRun{Job: j, log: m.d.Log.With().Str("job", j.Settings.Name).Logger()}
}

// warn logs msg (with err) as a warning and keeps it for the hooks (RESTOR_WARNINGS).
func (r *jobRun) warn(err error, msg string) {
	e, text := r.log.Warn(), msg
	if err != nil {
		e, text = e.Err(err), msg+": "+err.Error()
	}
	e.Msg(msg)
	r.warnings = append(r.warnings, text)
}

// runJob backs up one job and logs a failure. Services that were stopped are always
// restarted, also when the backup failed or the run was cancelled. The on_failure hook
// runs after a failure.
func (m *Manager) runJob(ctx context.Context, j Job) result {
	r := m.newRun(j)
	res := m.lockedJob(ctx, r)
	res.warnings = r.warnings
	if res.outcome == outcomeFailed {
		r.log.Error().Err(res.err).Msg("job failed")
		m.jobHook(ctx, r, hookOnFailure, res)
	}
	return res
}

// lockedJob takes the job's locks, backs it up and releases them.
func (m *Manager) lockedJob(ctx context.Context, r *jobRun) result {
	releases, err := m.lockAll(r.Services.LockKeys())
	defer m.releaseAll(releases, r.log)
	if err != nil {
		return failure(fmt.Errorf("backup: %s: %w", r.Settings.Name, err))
	}
	r.log.Info().Msg("starting job")
	return m.backupJob(ctx, r)
}

// skipJob fails a job without running it, because the job it requires failed; only its
// on_failure hook runs.
func (m *Manager) skipJob(ctx context.Context, j Job, required string) result {
	r := m.newRun(j)
	res := failure(fmt.Errorf("backup: %s: %w: %s", j.Settings.Name, ErrRequiredJobFailed,
		required))
	r.log.Error().Err(res.err).Msg("job skipped")
	if !m.s.DryRun {
		m.jobHook(ctx, r, hookOnFailure, res)
	}
	return res
}

// lockAll takes the locks for keys and returns the release functions of those it took,
// also when a later one fails. The locks do not block, so the order cannot deadlock.
func (m *Manager) lockAll(keys []string) ([]func() error, error) {
	releases := make([]func() error, 0, len(keys))
	for _, key := range keys {
		release, err := m.d.Locker.Lock(key)
		if err != nil {
			return releases, err
		}
		releases = append(releases, release)
	}
	return releases, nil
}

// releaseAll releases the locks in reverse order; a failed release is only logged, the
// kernel drops the lock when the process exits anyway.
func (m *Manager) releaseAll(releases []func() error, log zerolog.Logger) {
	for _, release := range slices.Backward(releases) {
		if err := release(); err != nil {
			log.Warn().Err(err).Msg("cannot release lock")
		}
	}
}

// backupJob runs the hooks around the service stop/start and the backup of a locked job.
// Once the pre hook succeeded, the post hook always runs, also when the backup failed, so
// it can undo what the pre hook did (e.g. a maintenance mode). An error names the job.
func (m *Manager) backupJob(ctx context.Context, r *jobRun) result {
	s := r.Settings
	if err := m.hook(ctx, s.Pre, hookEnv(hookPre, s.Name, result{})); err != nil {
		return failure(fmt.Errorf("backup: %s: pre hook: %w", s.Name, err))
	}
	res := m.backupWithServices(ctx, r)
	if res.err != nil {
		res.err = fmt.Errorf("backup: %s: %w", s.Name, res.err)
	}
	m.jobHook(ctx, r, hookPost, res)
	if res.outcome != outcomeFailed {
		r.log.Info().Msg("job completed")
	}
	return res
}

// backupWithServices stops the services, runs the backup and starts the services again.
// Services that do not come back fail the job, but leave its backup outcome alone.
func (m *Manager) backupWithServices(ctx context.Context, r *jobRun) result {
	stopped, err := m.stopServices(ctx, r)
	if err != nil {
		return failure(err)
	}
	res := m.backup(ctx, r)
	if err := m.startServices(ctx, r, stopped); err != nil {
		res.outcome, res.err = outcomeFailed, errors.Join(res.err, err)
		return res
	}
	m.startOthers(ctx, r, stopped)
	return res
}

// jobHook runs the post or on_failure hook of the job with its result in the
// environment; a failure is logged and does not change the result. The post hook undoes
// the pre hook, so it also runs after a cancel, for at most postHookGrace; the
// on_failure hook is skipped then, like the run-level hooks.
func (m *Manager) jobHook(ctx context.Context, r *jobRun, kind string, res result) {
	command := r.Settings.OnFailure
	if kind == hookPost {
		command = r.Settings.Post
		var cancel context.CancelFunc
		ctx, cancel = graceContext(ctx, postHookGrace)
		defer cancel()
	}
	if strings.TrimSpace(command) == "" {
		return
	}
	if ctx.Err() != nil {
		r.log.Warn().Str("hook", kind).Msg("hook skipped because the run was cancelled")
		return
	}
	res.warnings = r.warnings
	if err := m.hook(ctx, command, hookEnv(kind, r.Settings.Name, res)); err != nil {
		r.log.Error().Err(err).Str("hook", kind).Msg("hook failed")
	}
}

// graceContext returns a context that ends grace after ctx ends instead of with it, for
// work that undoes what a job did and must still run after a cancel.
func graceContext(ctx context.Context, grace time.Duration) (context.Context,
	context.CancelFunc,
) {
	graced, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(ctx, func() { time.AfterFunc(grace, cancel) })
	return graced, func() { stop(); cancel() }
}

// hookEnv returns the RESTOR_* variables of a job hook. The pre hook gets only the job
// and the hook name.
func hookEnv(kind, job string, r result) []string {
	env := []string{"RESTOR_JOB=" + job, "RESTOR_HOOK=" + kind}
	if kind == hookPre {
		return env
	}
	env = append(env, "RESTOR_STATUS="+r.status(), "RESTOR_SNAPSHOT_ID="+r.snapshotID)
	if r.err != nil {
		env = append(env, "RESTOR_ERROR="+r.err.Error())
	}
	if len(r.warnings) > 0 {
		env = append(env, "RESTOR_WARNINGS="+strings.Join(r.warnings, warningSeparator))
	}
	return env
}

// stopServices stops the job's running services and returns them, so that only those
// are started again. A failed or slow stop is a warning and the backup continues.
func (m *Manager) stopServices(ctx context.Context, r *jobRun) ([]string, error) {
	s := r.Settings
	if !s.StopServices {
		r.log.Debug().Msg("service stop/start disabled")
		return nil, nil
	}
	services, err := r.Services.Running(ctx, s.Services)
	if err != nil {
		return nil, err
	}
	if len(services) == 0 {
		// Usually a service that is down, but also what a wrong compose project (started
		// with -p, or from other compose files) looks like: the backup runs on live data.
		r.warn(nil, "no services running, nothing to stop; backing up anyway")
		return nil, nil
	}
	m.saveState(r, services)
	r.log.Info().Strs("services", services).Msg("stopping services")
	stopped, err := r.Services.Stop(ctx, services, s.StopTimeout)
	switch {
	case err != nil:
		r.warn(err, "cannot stop services, backing up anyway")
	case !stopped:
		r.warn(nil, fmt.Sprintf("services did not stop within %s, backing up anyway",
			s.StopTimeout))
	}
	return services, nil
}

// saveState records the services the job is about to stop, so that the next run starts
// them when this one ends before it does. Without the record the backup still runs.
func (m *Manager) saveState(r *jobRun, services []string) {
	if err := m.d.State.Save(r.Settings.Name, services); err != nil {
		r.warn(err, "cannot record the stopped services: if restor is killed before it "+
			"starts them again, the next run will not")
	}
}

// startServices starts the services stopServices stopped, and fails when they do not run
// within the start timeout: a service that does not come back needs attention. It uses
// its own bounded context so that services come back up after the run was cancelled.
// Once they run, the record of the stopped services is removed.
func (m *Manager) startServices(ctx context.Context, r *jobRun, services []string) error {
	if len(services) == 0 {
		return nil
	}
	timeout := r.Settings.StartTimeout
	startCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout+restartGrace)
	defer cancel()
	r.log.Info().Strs("services", services).Msg("starting services")
	running, err := r.Services.Start(startCtx, services, timeout)
	switch {
	case err != nil:
		return fmt.Errorf("start services: %w", err)
	case !running:
		return fmt.Errorf("%w within %s: %s", ErrNotStarted, timeout,
			strings.Join(services, ", "))
	}
	if err := m.d.State.Remove(r.Settings.Name); err != nil {
		// The next run starts services that already run: harmless.
		r.log.Warn().Err(err).Msg("cannot remove the record of the stopped services")
	}
	return nil
}

// startOthers starts the job's services that it did not stop (StartAll); those already
// running are left as they are by the service manager. A failure is a warning only: a
// service that was down before the backup must not fail it, or one broken service would
// block forget and prune on every run. After a cancel it is skipped.
func (m *Manager) startOthers(ctx context.Context, r *jobRun, stopped []string) {
	s := r.Settings
	if !s.StartAll || ctx.Err() != nil {
		return
	}
	all, err := r.Services.All(ctx, s.Services)
	if err != nil {
		r.warn(err, "cannot list the services to start")
		return
	}
	others := slices.DeleteFunc(all, func(s string) bool { return slices.Contains(stopped, s) })
	if len(others) == 0 {
		return
	}
	r.log.Info().Strs("services", others).Msg("starting services that were not stopped")
	running, err := r.Services.Start(ctx, others, s.StartTimeout)
	switch {
	case err != nil:
		r.warn(err, "cannot start services that were not stopped")
	case !running:
		r.warn(nil, fmt.Sprintf("services that were not stopped did not start within %s",
			s.StartTimeout))
	}
}

// backup runs restic. A snapshot without some unreadable source files, or a run that
// names no snapshot, is a warning: the job counts as backed up, but is not complete, so
// forget and prune leave the older snapshots alone.
func (m *Manager) backup(ctx context.Context, r *jobRun) result {
	id, err := m.resticBackup(ctx, r.Settings)
	var incomplete interface{ Incomplete() bool }
	var unchanged interface{ Unchanged() bool }
	switch {
	case errors.As(err, &unchanged) && unchanged.Unchanged():
		// The latest snapshot is still current and complete: forget and prune may run.
		r.log.Info().Msg("backup completed, nothing changed since the last snapshot")
		return backedUp(outcomeComplete, "")
	case errors.As(err, &incomplete) && incomplete.Incomplete():
		r.warn(err, "backup completed, but some source files could not be read")
		return backedUp(outcomeIncomplete, id)
	case err != nil:
		return failure(err)
	case id == "":
		r.warn(nil, "backup completed, but restic reported no snapshot ID")
		return backedUp(outcomeIncomplete, "")
	}
	r.log.Info().Str("snapshot_id", id).Msg("backup completed")
	return backedUp(outcomeComplete, id)
}

// resticBackup runs restic backup, cancelled when it takes longer than the backup
// timeout.
func (m *Manager) resticBackup(ctx context.Context, s JobSettings) (string, error) {
	if s.BackupTimeout <= 0 {
		return m.d.Restic.Backup(ctx, s.Name, s.Sources, s.BackupArgs)
	}
	backupCtx, cancel := context.WithTimeout(ctx, s.BackupTimeout)
	defer cancel()
	id, err := m.d.Restic.Backup(backupCtx, s.Name, s.Sources, s.BackupArgs)
	if err != nil && ctx.Err() == nil && backupCtx.Err() != nil {
		return id, fmt.Errorf("backup_timeout of %s exceeded: %w", s.BackupTimeout, err)
	}
	return id, err
}

// dryRunJob logs the services the job would stop and runs its backup, whose BackupArgs
// carry restic's --dry-run: no locks, hooks or service changes, and no snapshot.
func (m *Manager) dryRunJob(ctx context.Context, j Job) result {
	r := m.newRun(j)
	if j.Settings.StopServices {
		services, err := j.Services.Running(ctx, j.Settings.Services)
		if err != nil {
			return m.dryRunFailed(r, err)
		}
		r.log.Info().Strs("services", services).Msg("dry run: would stop these services")
	}
	_, err := m.resticBackup(ctx, j.Settings)
	var incomplete interface{ Incomplete() bool }
	switch {
	case errors.As(err, &incomplete) && incomplete.Incomplete():
		r.warn(err, "dry run: some source files could not be read")
		return backedUp(outcomeIncomplete, "")
	case err != nil:
		return m.dryRunFailed(r, err)
	}
	r.log.Info().Msg("dry run completed")
	return backedUp(outcomeComplete, "")
}

func (m *Manager) dryRunFailed(r *jobRun, err error) result {
	res := failure(fmt.Errorf("backup: %s: %w", r.Settings.Name, err))
	r.log.Error().Err(res.err).Msg("dry run failed")
	return res
}

// hook runs command, split on whitespace, with env added; an empty command does nothing.
func (m *Manager) hook(ctx context.Context, command string, env []string) error {
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return nil
	}
	if _, err := m.d.Hooks.RunEnv(ctx, execSafe(env), parts[0], parts[1:]...); err != nil {
		return fmt.Errorf("%s: %w", parts[0], err)
	}
	return nil
}

// execSafe returns env as exec accepts it: without NUL bytes (a program's output in
// RESTOR_ERROR may hold them, and exec refuses the whole environment then) and each
// variable at most maxEnvVar bytes (Linux refuses a longer one, and RESTOR_ERROR of the
// run joins the errors of every job). A hook must not fail exactly when it reports one.
func execSafe(env []string) []string {
	out := make([]string, len(env))
	for i, kv := range env {
		kv = strings.ReplaceAll(kv, "\x00", "")
		if len(kv) > maxEnvVar {
			kv = strings.ToValidUTF8(kv[:maxEnvVar-len(truncated)], "") + truncated
		}
		out[i] = kv
	}
	return out
}
