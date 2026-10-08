package backup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

// restartGrace is added to the start timeout to bound restarting the services after the
// run was cancelled (the run context can no longer be used for that).
const restartGrace = 3 * time.Minute

// ProjectSettings configure the backup of one project.
type ProjectSettings struct {
	// Name is the project name and the restic tag.
	Name string
	// Services limits the stopped services; empty means all of the service manager's.
	Services []string
	// Sources are the backed up paths.
	Sources []string
	// StopServices stops the services during the backup.
	StopServices bool
	// StopTimeout and StartTimeout bound waiting for the services.
	StopTimeout  time.Duration
	StartTimeout time.Duration
	// RetentionPolicy is passed to forget, split on whitespace; empty skips forget.
	RetentionPolicy string
	// BackupOptions and ForgetOptions are extra restic options.
	BackupOptions []string
	ForgetOptions []string
	// PreBackupCmd and PostBackupCmd are hooks, split on whitespace (no shell).
	PreBackupCmd  string
	PostBackupCmd string
}

// runProject backs up one project. Services that were stopped are always restarted, also
// when the backup failed or the run was cancelled.
func (m *Manager) runProject(ctx context.Context, p Project) error {
	log := m.d.Log.With().Str("project", p.Settings.Name).Logger()
	release, err := m.d.Locker.Lock(p.Services.LockKey())
	if err != nil {
		return fmt.Errorf("backup: %s: %w", p.Settings.Name, err)
	}
	defer func() {
		if err := release(); err != nil {
			log.Warn().Err(err).Msg("cannot release lock")
		}
	}()
	log.Info().Msg("starting project backup")
	return m.backupProject(ctx, p, log)
}

// backupProject runs the hooks, the service stop/start and the backup of a locked project.
func (m *Manager) backupProject(ctx context.Context, p Project, log zerolog.Logger) error {
	s := p.Settings
	if err := m.hook(ctx, s.PreBackupCmd); err != nil {
		return fmt.Errorf("backup: %s: pre-backup hook: %w", s.Name, err)
	}
	stopped, err := m.stopServices(ctx, p, log)
	if err != nil {
		return fmt.Errorf("backup: %s: %w", s.Name, err)
	}
	backupErr := m.backup(ctx, s, log)
	if err := m.startServices(ctx, p, stopped, log); err != nil {
		return errors.Join(backupErr, fmt.Errorf("backup: %s: %w", s.Name, err))
	}
	if backupErr != nil {
		return backupErr
	}
	if err := m.hook(ctx, s.PostBackupCmd); err != nil {
		log.Error().Err(err).Msg("post-backup hook failed")
	}
	log.Info().Msg("project backup completed")
	return nil
}

// stopServices stops the project's running services and returns them, so that only
// those are started again. A failed or slow stop is logged and the backup continues.
func (m *Manager) stopServices(ctx context.Context, p Project, log zerolog.Logger) (
	[]string, error,
) {
	if !p.Settings.StopServices {
		log.Debug().Msg("service stop/start disabled")
		return nil, nil
	}
	services, err := p.Services.Running(ctx, p.Settings.Services)
	if err != nil {
		return nil, err
	}
	if len(services) == 0 {
		log.Info().Msg("no services running, nothing to stop")
		return nil, nil
	}
	log.Info().Strs("services", services).Msg("stopping services")
	stopped, err := p.Services.Stop(ctx, services, p.Settings.StopTimeout)
	switch {
	case err != nil:
		log.Warn().Err(err).Msg("cannot stop services, backing up anyway")
	case !stopped:
		log.Warn().Dur("timeout", p.Settings.StopTimeout).
			Msg("services did not stop within the timeout, backing up anyway")
	}
	return services, nil
}

// startServices starts the services stopServices stopped. It uses its own bounded context
// so that services come back up after the run was cancelled.
func (m *Manager) startServices(ctx context.Context, p Project, services []string,
	log zerolog.Logger,
) error {
	if len(services) == 0 {
		return nil
	}
	timeout := p.Settings.StartTimeout
	startCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout+restartGrace)
	defer cancel()
	log.Info().Strs("services", services).Msg("starting services")
	running, err := p.Services.Start(startCtx, services, timeout)
	if err != nil {
		return fmt.Errorf("start services: %w", err)
	}
	if !running {
		log.Warn().Dur("timeout", timeout).Msg("services did not start within the timeout")
	}
	return nil
}

// backup runs restic. An incomplete snapshot (some source files unreadable) is saved, so
// it is logged as a warning and the project counts as backed up.
func (m *Manager) backup(ctx context.Context, s ProjectSettings, log zerolog.Logger) error {
	id, err := m.d.Restic.Backup(ctx, s.Name, s.Sources, s.BackupOptions)
	var incomplete interface{ Incomplete() bool }
	if errors.As(err, &incomplete) && incomplete.Incomplete() {
		log.Warn().Err(err).Str("snapshot_id", id).
			Msg("backup completed, but some source files could not be read")
		return nil
	}
	if err != nil {
		return fmt.Errorf("backup: %s: %w", s.Name, err)
	}
	if id == "" {
		log.Warn().Msg("backup completed, but restic reported no snapshot ID")
		return nil
	}
	log.Info().Str("snapshot_id", id).Msg("backup completed")
	return nil
}

// hook runs command, split on whitespace; an empty command does nothing.
func (m *Manager) hook(ctx context.Context, command string) error {
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return nil
	}
	if _, err := m.d.Hooks.Run(ctx, parts[0], parts[1:]...); err != nil {
		return fmt.Errorf("%s: %w", parts[0], err)
	}
	return nil
}
