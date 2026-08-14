package backup

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/dombyte/restor/backup/restic"
	"github.com/dombyte/restor/backup/servicemanager"
	"github.com/dombyte/restor/config"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// ProjectBackup handles the backup process for a single project
type ProjectBackup struct {
	project    *config.Project
	serviceMgr servicemanager.ServiceManager
	restic     *restic.Restic
	locker     *Locker
	logger     zerolog.Logger
}

// NewProjectBackup creates a new ProjectBackup instance
func NewProjectBackup(project *config.Project, locker *Locker) (*ProjectBackup, error) {
	// Convert environment map to array
	envArray := config.GetEnvArray(project.Environment)

	// Create service manager based on project configuration
	serviceConfig := &servicemanager.ServiceConfig{
		ServiceManagerType: project.ServiceManager,
		ComposeFile:        project.ComposeFile,
		SystemdUnits:       project.SystemdUnits,
		SystemdScope:       project.SystemdScope,
	}

	factory := servicemanager.NewFactory()
	serviceMgr, err := factory.CreateServiceManager(serviceConfig, envArray)
	if err != nil {
		return nil, fmt.Errorf("failed to create service manager: %w", err)
	}

	return &ProjectBackup{
		project:    project,
		serviceMgr: serviceMgr,
		restic:     restic.NewRestic(project.ResticRepo, envArray, []string{}),
		locker:     locker,
		logger:     log.Logger,
	}, nil
}

// Run executes the backup process for the project
func (pb *ProjectBackup) Run(ctx context.Context) error {
	logger := pb.logger.With().
		Str("project", pb.project.Name).
		Str("service_manager", pb.project.ServiceManager).
		Logger()

	// Recreate service manager with project-specific logger for better log context
	serviceConfig := &servicemanager.ServiceConfig{
		ServiceManagerType: pb.project.ServiceManager,
		ComposeFile:        pb.project.ComposeFile,
		SystemdUnits:       pb.project.SystemdUnits,
		SystemdScope:       pb.project.SystemdScope,
	}
	
	factory := servicemanager.NewFactory()
	var err error
	pb.serviceMgr, err = factory.CreateServiceManager(serviceConfig, config.GetEnvArray(pb.project.Environment), logger)
	if err != nil {
		return fmt.Errorf("failed to create service manager: %w", err)
	}

	// Recreate restic with project-specific logger for better log context
	pb.restic = restic.NewRestic(pb.project.ResticRepo, config.GetEnvArray(pb.project.Environment), []string{}, logger)

	// Acquire lock using the service manager's lock file name
	lockFileName := pb.serviceMgr.GetLockFileName()
	if lockFileName == "" {
		lockFileName = pb.project.Name // Fallback to project name if no lock file name
	}
	if acquired, err := pb.locker.Lock(lockFileName); err != nil {
		return fmt.Errorf("failed to acquire lock: %w", err)
	} else if !acquired {
		return fmt.Errorf("project %s is already locked by another backup process", pb.project.Name)
	}
	defer pb.locker.Unlock(lockFileName)

	// Execute pre-backup command if specified
	if pb.project.PreBackupCmd != "" {
		if err := pb.runCommand(ctx, "Pre-backup command", pb.project.PreBackupCmd); err != nil {
			logger.Error().Err(err).Msg("Pre-backup command failed")
			return fmt.Errorf("pre-backup command failed: %w", err)
		}
	}

	// Get services to stop (either from config or all services from service manager)
	// Only needed if we're stopping containers
	var services []string
	var getServicesErr error
	if pb.project.StopServices {
		services, getServicesErr = pb.serviceMgr.GetServices(ctx, pb.project.Services)
		if getServicesErr != nil {
			logger.Error().Err(getServicesErr).Msg("Failed to get services")
			return fmt.Errorf("failed to get services: %w", getServicesErr)
		}

		logger.Debug().Strs("services", services).Msg("Services to manage")

		// Stop services
		if len(services) > 0 {
			if err := pb.serviceMgr.Stop(ctx, services, pb.project.StopTimeout); err != nil {
				logger.Error().Err(err).Msg("Failed to stop services")
				// Continue anyway, maybe we can still backup
			}
		}

		// Ensure services are stopped by checking status
		if len(services) > 0 {
			if err := pb.waitForStopped(ctx, services, pb.project.StopTimeout); err != nil {
				logger.Warn().Err(err).Msg("Services may not have stopped completely")
			}
		}
	} else {
		logger.Debug().Msg("Service stop/start disabled for this project")
	}

	// Run the actual backup
	snapshotID, err := pb.restic.Backup(ctx, pb.project.Sources, pb.project.Name, pb.project.BackupOptions)
	if err != nil {
		logger.Error().Err(err).Msg("Backup failed")

		// Try to restart services even if backup failed (only if we stopped them)
		if pb.project.StopServices && len(services) > 0 {
			if restartErr := pb.serviceMgr.Start(ctx, services, pb.project.StartTimeout); restartErr != nil {
				logger.Error().Err(restartErr).Msg("Failed to restart services after backup failure")
			} else {
				logger.Info().Msg("Services restarted after backup failure")
			}
		}

		return fmt.Errorf("backup failed: %w", err)
	}

	logger.Info().Str("snapshot_id", snapshotID).Msg("Backup completed")

	// Start services back up (only if we stopped them)
	if pb.project.StopServices && len(services) > 0 {
		if err := pb.serviceMgr.Start(ctx, services, pb.project.StartTimeout); err != nil {
			logger.Error().Err(err).Msg("Failed to restart services")
			return fmt.Errorf("failed to restart services: %w", err)
		}
	}

	// Execute post-backup command if specified
	if pb.project.PostBackupCmd != "" {
		if err := pb.runCommand(ctx, "Post-backup command", pb.project.PostBackupCmd); err != nil {
			logger.Error().Err(err).Msg("Post-backup command failed")
			// Don't fail the backup, just log the error
		} else {
			logger.Info().Msg("Post-backup command completed")
		}
	}

	return nil
}

// runCommand executes a shell command
func (pb *ProjectBackup) runCommand(ctx context.Context, name, command string) error {
	pb.logger.Info().Str("command", name).Str("cmd", command).Msg("Executing command")

	// Parse command and arguments
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return nil
	}

	cmd := exec.CommandContext(ctx, parts[0], parts[1:]...)
	cmd.Env = config.GetEnvArray(pb.project.Environment)

	output, err := cmd.CombinedOutput()
	if err != nil {
		pb.logger.Error().Err(err).Bytes("output", output).Msg(name + " failed")
		return fmt.Errorf("%s failed: %w", name, err)
	}

	pb.logger.Debug().Str("output", string(output)).Msg(name + " output")
	return nil
}

// waitForStopped waits for services to be stopped
func (pb *ProjectBackup) waitForStopped(ctx context.Context, services []string, timeout time.Duration) error {
	startTime := time.Now()

	for {
		// Check if all services are stopped
		allStopped := true
		for _, svc := range services {
			if pb.isServiceRunning(ctx, svc) {
				allStopped = false
				break
			}
		}

		if allStopped {
			return nil
		}

		// Check timeout
		if time.Since(startTime) >= timeout {
			return fmt.Errorf("timeout waiting for services to stop")
		}

		// Wait a bit before checking again
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
			// Continue loop
		}
	}
}

// isServiceRunning checks if a service is running using the service manager
func (pb *ProjectBackup) isServiceRunning(ctx context.Context, service string) bool {
	// Use the service manager's Ps method to check service status
	output, err := pb.serviceMgr.Ps(ctx, []string{service})
	if err != nil {
		return false
	}

	// Check if the service appears in the output and is running
	// Different service managers have different output formats, but we can check for common patterns
	return strings.Contains(output, service) &&
		(strings.Contains(output, "Up") || 
		 strings.Contains(output, "Running") ||
		 strings.Contains(output, "active (running)"))
}
