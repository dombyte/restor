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
// query of at most 30 s), so a slow start ends with a warning, not a cancelled restart.
const restartGrace = 3 * time.Minute

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

// JobSettings configure the backup of one job.
type JobSettings struct {
	// Name is the job name and the restic tag.
	Name string
	// After are the jobs that must finish before this one starts.
	After []string
	// Services limits the stopped services; empty means all of the service manager's.
	Services []string
	// Sources are the backed up paths.
	Sources []string
	// StopServices stops the services during the backup.
	StopServices bool
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

// result is how a job ended, with its snapshot ID and, when it failed, why.
type result struct {
	outcome    outcome
	snapshotID string
	err        error
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

// runJob backs up one job and logs a failure. Services that were stopped are always
// restarted, also when the backup failed or the run was cancelled. The on_failure hook
// runs after a failure.
func (m *Manager) runJob(ctx context.Context, j Job) result {
	s := j.Settings
	log := m.d.Log.With().Str("job", s.Name).Logger()
	releases, err := m.lockAll(j.Services.LockKeys())
	r := result{outcome: outcomeFailed, err: err}
	if err == nil {
		log.Info().Msg("starting job")
		r = m.backupJob(ctx, j, log)
	}
	m.releaseAll(releases, log)
	if r.outcome == outcomeFailed {
		r.err = fmt.Errorf("backup: %s: %w", s.Name, r.err)
		log.Error().Err(r.err).Msg("job failed")
		m.jobHook(ctx, s, hookOnFailure, r, log)
	}
	return r
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
// it can undo what the pre hook did (e.g. a maintenance mode).
func (m *Manager) backupJob(ctx context.Context, j Job, log zerolog.Logger) result {
	s := j.Settings
	if err := m.hook(ctx, s.Pre, hookEnv(hookPre, s.Name, result{})); err != nil {
		return result{outcome: outcomeFailed, err: fmt.Errorf("pre hook: %w", err)}
	}
	r := m.backupWithServices(ctx, j, log)
	m.jobHook(ctx, s, hookPost, r, log)
	if r.outcome != outcomeFailed {
		log.Info().Msg("job completed")
	}
	return r
}

// backupWithServices stops the services, runs the backup and starts the services again.
func (m *Manager) backupWithServices(ctx context.Context, j Job, log zerolog.Logger) result {
	stopped, err := m.stopServices(ctx, j, log)
	if err != nil {
		return result{outcome: outcomeFailed, err: err}
	}
	r := m.backup(ctx, j.Settings, log)
	if err := m.startServices(ctx, j, stopped, log); err != nil {
		return result{
			outcome: outcomeFailed, snapshotID: r.snapshotID, err: errors.Join(r.err, err),
		}
	}
	return r
}

// jobHook runs the post or on_failure hook of s with the job's result in its
// environment; a failure is logged and does not change the result. After a cancel it is
// skipped: like every hook, it is bound to the run context.
func (m *Manager) jobHook(ctx context.Context, s JobSettings, kind string, r result,
	log zerolog.Logger,
) {
	command := s.Post
	if kind == hookOnFailure {
		command = s.OnFailure
	}
	if strings.TrimSpace(command) == "" {
		return
	}
	if ctx.Err() != nil {
		log.Warn().Str("hook", kind).Msg("hook skipped because the run was cancelled")
		return
	}
	if err := m.hook(ctx, command, hookEnv(kind, s.Name, r)); err != nil {
		log.Error().Err(err).Str("hook", kind).Msg("hook failed")
	}
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
	return env
}

// stopServices stops the job's running services and returns them, so that only those
// are started again. A failed or slow stop is logged and the backup continues.
func (m *Manager) stopServices(ctx context.Context, j Job, log zerolog.Logger) (
	[]string, error,
) {
	if !j.Settings.StopServices {
		log.Debug().Msg("service stop/start disabled")
		return nil, nil
	}
	services, err := j.Services.Running(ctx, j.Settings.Services)
	if err != nil {
		return nil, err
	}
	if len(services) == 0 {
		log.Debug().Msg("no services running, nothing to stop")
		return nil, nil
	}
	log.Info().Strs("services", services).Msg("stopping services")
	stopped, err := j.Services.Stop(ctx, services, j.Settings.StopTimeout)
	switch {
	case err != nil:
		log.Warn().Err(err).Msg("cannot stop services, backing up anyway")
	case !stopped:
		log.Warn().Stringer("timeout", j.Settings.StopTimeout).
			Msg("services did not stop within the timeout, backing up anyway")
	}
	return services, nil
}

// startServices starts the services stopServices stopped. It uses its own bounded context
// so that services come back up after the run was cancelled.
func (m *Manager) startServices(ctx context.Context, j Job, services []string,
	log zerolog.Logger,
) error {
	if len(services) == 0 {
		return nil
	}
	timeout := j.Settings.StartTimeout
	startCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout+restartGrace)
	defer cancel()
	log.Info().Strs("services", services).Msg("starting services")
	running, err := j.Services.Start(startCtx, services, timeout)
	if err != nil {
		return fmt.Errorf("start services: %w", err)
	}
	if !running {
		log.Warn().Stringer("timeout", timeout).Msg("services did not start within the timeout")
	}
	return nil
}

// backup runs restic. A snapshot without some unreadable source files, or a run that
// names no snapshot, is logged as a warning: the job counts as backed up, but is not
// complete, so forget and prune leave the older snapshots alone.
func (m *Manager) backup(ctx context.Context, s JobSettings, log zerolog.Logger) result {
	id, err := m.resticBackup(ctx, s)
	var incomplete interface{ Incomplete() bool }
	var unchanged interface{ Unchanged() bool }
	switch {
	case errors.As(err, &unchanged) && unchanged.Unchanged():
		// The latest snapshot is still current and complete: forget and prune may run.
		log.Info().Msg("backup completed, nothing changed since the last snapshot")
		return result{outcome: outcomeComplete}
	case errors.As(err, &incomplete) && incomplete.Incomplete():
		log.Warn().Err(err).Str("snapshot_id", id).
			Msg("backup completed, but some source files could not be read")
		return result{outcome: outcomeIncomplete, snapshotID: id}
	case err != nil:
		return result{outcome: outcomeFailed, err: err}
	case id == "":
		log.Warn().Msg("backup completed, but restic reported no snapshot ID")
		return result{outcome: outcomeIncomplete}
	}
	log.Info().Str("snapshot_id", id).Msg("backup completed")
	return result{outcome: outcomeComplete, snapshotID: id}
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

// hook runs command, split on whitespace, with env added; an empty command does nothing.
func (m *Manager) hook(ctx context.Context, command string, env []string) error {
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return nil
	}
	if _, err := m.d.Hooks.RunEnv(ctx, env, parts[0], parts[1:]...); err != nil {
		return fmt.Errorf("%s: %w", parts[0], err)
	}
	return nil
}
