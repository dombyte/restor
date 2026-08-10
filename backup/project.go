package backup

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/dombyte/dbk/backup/docker"
	"github.com/dombyte/dbk/backup/restic"
	"github.com/dombyte/dbk/config"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// ProjectBackup handles the backup process for a single project
type ProjectBackup struct {
	project *config.Project
	compose *docker.Compose
	restic  *restic.Restic
	locker  *Locker
	logger  zerolog.Logger
}

// NewProjectBackup creates a new ProjectBackup instance
func NewProjectBackup(project *config.Project, locker *Locker) *ProjectBackup {
	// Convert environment map to array
	envArray := config.GetEnvArray(project.Environment)

	return &ProjectBackup{
		project: project,
		compose: docker.NewCompose(project.ComposeFile, envArray),
		restic:  restic.NewRestic(project.ResticRepo, envArray, []string{}),
		locker:  locker,
		logger:  log.Logger,
	}
}

// Run executes the backup process for the project
func (pb *ProjectBackup) Run(ctx context.Context) error {
	logger := pb.logger.With().
		Str("project", pb.project.Name).
		Str("compose_file", pb.project.ComposeFile).
		Logger()

	// Recreate compose with project-specific logger for better log context
	pb.compose = docker.NewCompose(pb.project.ComposeFile, config.GetEnvArray(pb.project.Environment), logger)

	// Recreate restic with project-specific logger for better log context
	pb.restic = restic.NewRestic(pb.project.ResticRepo, config.GetEnvArray(pb.project.Environment), []string{}, logger)

	// Acquire lock
	if acquired, err := pb.locker.Lock(pb.project.ComposeFile); err != nil {
		return fmt.Errorf("failed to acquire lock: %w", err)
	} else if !acquired {
		return fmt.Errorf("compose file %s is already locked by another backup process", pb.project.ComposeFile)
	}
	defer pb.locker.Unlock(pb.project.ComposeFile)

	// Execute pre-backup command if specified
	if pb.project.PreBackupCmd != "" {
		if err := pb.runCommand(ctx, "Pre-backup command", pb.project.PreBackupCmd); err != nil {
			logger.Error().Err(err).Msg("Pre-backup command failed")
			return fmt.Errorf("pre-backup command failed: %w", err)
		}
	}

	// Get services to stop (either from config or all services from compose file)
	// Only needed if we're stopping containers
	var services []string
	var getServicesErr error
	if pb.project.StopContainers {
		services, getServicesErr = pb.compose.GetServices(ctx, pb.project.Services)
		if getServicesErr != nil {
			logger.Error().Err(getServicesErr).Msg("Failed to get services")
			return fmt.Errorf("failed to get services: %w", getServicesErr)
		}

		logger.Info().Strs("services", services).Msg("Services to manage")

		// Stop containers
		if len(services) > 0 {
			if err := pb.compose.Stop(ctx, services, pb.project.StopTimeout); err != nil {
				logger.Error().Err(err).Msg("Failed to stop containers")
				// Continue anyway, maybe we can still backup
			}
		}

		// Ensure containers are stopped by checking status
		if len(services) > 0 {
			if err := pb.waitForStopped(ctx, services, pb.project.StopTimeout); err != nil {
				logger.Warn().Err(err).Msg("Containers may not have stopped completely")
			}
		}
	} else {
		logger.Info().Msg("Container stop/start disabled for this project")
	}

	// Run the actual backup
	snapshotID, err := pb.restic.Backup(ctx, pb.project.Sources, pb.project.Name, pb.project.BackupOptions)
	if err != nil {
		logger.Error().Err(err).Msg("Backup failed")

		// Try to restart containers even if backup failed (only if we stopped them)
		if pb.project.StopContainers && len(services) > 0 {
			if restartErr := pb.compose.Start(ctx, services, pb.project.StartTimeout); restartErr != nil {
				logger.Error().Err(restartErr).Msg("Failed to restart containers after backup failure")
			} else {
				logger.Info().Msg("Containers restarted after backup failure")
			}
		}

		return fmt.Errorf("backup failed: %w", err)
	}

	logger.Info().Str("snapshot_id", snapshotID).Msg("Backup completed")

	// Start containers back up (only if we stopped them)
	if pb.project.StopContainers && len(services) > 0 {
		if err := pb.compose.Start(ctx, services, pb.project.StartTimeout); err != nil {
			logger.Error().Err(err).Msg("Failed to restart containers")
			return fmt.Errorf("failed to restart containers: %w", err)
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

// waitForStopped waits for containers to be stopped
func (pb *ProjectBackup) waitForStopped(ctx context.Context, services []string, timeout time.Duration) error {
	startTime := time.Now()

	for {
		// Check if all containers are stopped
		allStopped := true
		for _, svc := range services {
			if pb.isContainerRunning(ctx, svc) {
				allStopped = false
				break
			}
		}

		if allStopped {
			return nil
		}

		// Check timeout
		if time.Since(startTime) >= timeout {
			return fmt.Errorf("timeout waiting for containers to stop")
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

// isContainerRunning checks if a container is running
func (pb *ProjectBackup) isContainerRunning(ctx context.Context, service string) bool {
	// Use docker compose ps to check container status
	output, err := pb.compose.Ps(ctx, []string{service})
	if err != nil {
		return false
	}

	// Check if the service appears in the output and is running
	// Docker compose ps output includes status information
	return strings.Contains(output, service) &&
		(strings.Contains(output, "Up") || strings.Contains(output, "Running"))
}
