package backup

import (
	"context"
	"fmt"
	"sync"

	"github.com/dombyte/dbk/backup/restic"
	"github.com/dombyte/dbk/config"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// Manager manages the backup process for multiple projects
type Manager struct {
	config   *config.Config
	projects []config.Project
	locker   *Locker
	logger   zerolog.Logger
	restic   *restic.Restic
}

// NewManager creates a new Manager instance
func NewManager(cfg *config.Config, projects []config.Project) *Manager {
	// Create a restic instance for global operations (prune)
	// Convert global environments to array
	envVars := config.GetEnvArray(cfg.Environments)

	return &Manager{
		config:   cfg,
		projects: projects,
		locker:   NewLocker(""), // Use default lock directory
		logger:   log.Logger,
		restic:   restic.NewRestic(cfg.Global.ResticRepo, envVars, cfg.Global.PruneOptions),
	}
}

// Run executes the backup process based on the configuration mode
func (m *Manager) Run(ctx context.Context) error {
	logger := m.logger.With().
		Str("mode", m.config.GetMode()).
		Int("project_count", len(m.projects)).
		Logger()

	logger.Info().Msg("Starting backup manager")

	// Clean up stale locks before starting
	if err := m.locker.CleanupStaleLocks(); err != nil {
		logger.Warn().Err(err).Msg("Failed to clean up stale locks")
	}

	// Check if global auto_prune is enabled and any project has retention_policy
	hasAutoPrune := m.config.Global.AutoPrune
	if hasAutoPrune {
		// Check if at least one project has a retention policy
		hasAutoPrune = false
		for _, project := range m.projects {
			if project.RetentionPolicy != "" {
				hasAutoPrune = true
				break
			}
		}
	}

	err := m.runByMode(ctx)
	if err != nil {
		return err
	}

	// Run forget operations sequentially (even in parallel mode) to avoid repository lock conflicts
	// This must be done AFTER all backups complete but BEFORE prune
	if hasAutoPrune {
		for _, project := range m.projects {
			if project.RetentionPolicy != "" {
				projectLogger := logger.With().Str("project", project.Name).Logger()
				projectRestic := restic.NewRestic(project.ResticRepo, config.GetEnvArray(project.Environment), []string{}, projectLogger)
				projectLogger.Info().Str("tag", project.Name).Str("retention_policy", project.RetentionPolicy).Msg("Running forget")
				if forgetErr := projectRestic.Forget(ctx, project.Name, project.RetentionPolicy, project.ForgetOptions); forgetErr != nil {
					logger.Error().Err(forgetErr).Str("project", project.Name).Msg("Forget failed (continuing)")
				} else {
					projectLogger.Info().Msg("Forget completed")
				}
			}
		}
	}

	// Run repository-wide prune if any project has auto_prune enabled
	// This must be done AFTER all projects have run forget
	if hasAutoPrune {
		logger.Info().Msg("Running repository-wide prune (auto_prune was enabled for one or more projects)")
		if pruneErr := m.restic.Prune(ctx); pruneErr != nil {
			logger.Error().Err(pruneErr).Msg("Repository prune failed")
			// Don't fail the entire backup, just log the error
			// The forget operations already succeeded per project
		}

		// Run unlock after prune to remove any stale locks
		if unlockErr := m.restic.Unlock(ctx); unlockErr != nil {
			logger.Error().Err(unlockErr).Msg("Repository unlock failed")
			// Don't fail the entire backup, just log the error
		}
	}

	return nil
}

// runByMode runs projects in the configured mode
func (m *Manager) runByMode(ctx context.Context) error {
	switch m.config.GetMode() {
	case "parallel":
		return m.runParallel(ctx)
	case "sequential":
		return m.runSequential(ctx)
	default:
		return fmt.Errorf("unknown execution mode: %s", m.config.Mode)
	}
}

// runParallel runs all project backups in parallel
func (m *Manager) runParallel(ctx context.Context) error {
	logger := m.logger.With().Str("mode", "parallel").Logger()

	logger.Info().Msg("Running backups in parallel mode")

	var wg sync.WaitGroup
	errors := make(chan error, len(m.projects))

	for _, project := range m.projects {
		wg.Add(1)
		go func(p config.Project) {
			defer wg.Done()

			pb := NewProjectBackup(&p, m.locker)
			// Create project backup with a separate locker instance for each goroutine
			// But we want to share the same lock directory, so we'll use the same locker

			if err := pb.Run(ctx); err != nil {
				errors <- fmt.Errorf("project %s: %w", p.Name, err)
			} else {
				logger.Info().Str("project", p.Name).Msg("Project backup completed")
			}
		}(project)
	}

	// Wait for all backups to complete
	wg.Wait()
	close(errors)

	// Collect and return errors
	var errs []error
	for err := range errors {
		errs = append(errs, err)
		logger.Error().Err(err).Msg("Project backup failed")
	}

	if len(errs) > 0 {
		return fmt.Errorf("%d projects failed: %v", len(errs), errs)
	}

	logger.Info().Msg("All parallel backups completed successfully")
	return nil
}

// runSequential runs all project backups one after another
func (m *Manager) runSequential(ctx context.Context) error {
	logger := m.logger.With().Str("mode", "sequential").Logger()

	logger.Info().Msg("Running backups in sequential mode")

	var errs []error

	for _, project := range m.projects {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			// Continue with next project
		}
		pb := NewProjectBackup(&project, m.locker)

		logger.Info().Str("project", project.Name).Msg("Starting project backup")

		if err := pb.Run(ctx); err != nil {
			errs = append(errs, fmt.Errorf("project %s: %w", project.Name, err))
			logger.Error().Err(err).Str("project", project.Name).Msg("Project backup failed")
		} else {
			logger.Info().Str("project", project.Name).Msg("Project backup completed")
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("%d projects failed: %v", len(errs), errs)
	}

	logger.Info().Msg("All sequential backups completed successfully")
	return nil
}
