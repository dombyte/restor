package servicemanager

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// Systemd wraps the systemctl CLI for managing systemd services
type Systemd struct {
	units  []string
	scope  string
	env    []string
	logger zerolog.Logger
}

// NewSystemd creates a new Systemd wrapper
func NewSystemd(units []string, scope string, env []string, logger ...zerolog.Logger) *Systemd {
	if scope == "" {
		scope = DefaultSystemdScope
	}

	l := log.Logger
	if len(logger) > 0 {
		l = logger[0]
	}

	return &Systemd{
		units:  units,
		scope:  scope,
		env:    env,
		logger: l,
	}
}

// GetLockFileName returns a unique identifier for locking based on units
func (s *Systemd) GetLockFileName() string {
	// Create a unique identifier from the sorted units list
	if len(s.units) == 0 {
		return "systemd"
	}
	return strings.Join(s.units, ",")
}

// buildCommand builds the base systemctl command with appropriate scope
func (s *Systemd) buildCommand(args ...string) *exec.Cmd {
	var cmdArgs []string

	// Add scope flag if user-level
	if s.scope == ScopeUser {
		cmdArgs = append(cmdArgs, "--user")
	}

	cmdArgs = append(cmdArgs, args...)

	cmd := exec.Command("systemctl", cmdArgs...)
	cmd.Env = s.env

	return cmd
}

// GetServices returns the configured systemd units
// For systemd, we use the provided services list (systemd_units from config)
// The services parameter passed in is ignored, as systemd services are explicitly configured
func (s *Systemd) GetServices(ctx context.Context, services []string) ([]string, error) {
	// For systemd, we always use the configured units
	// The services parameter is ignored
	if len(s.units) == 0 {
		return nil, fmt.Errorf("no systemd units configured")
	}

	// If specific services are requested, filter to those that are in our configured units
	if len(services) > 0 {
		var filtered []string
		for _, svc := range services {
			for _, unit := range s.units {
				if svc == unit {
					filtered = append(filtered, svc)
					break
				}
			}
		}
		if len(filtered) == 0 {
			// If none of the requested services match, return all configured units
			filtered = s.units
		}
		return filtered, nil
	}

	return s.units, nil
}

// Stop stops the specified systemd services
func (s *Systemd) Stop(ctx context.Context, services []string, timeout time.Duration) error {
	if len(services) == 0 {
		s.logger.Debug().Msg("No services to stop")
		return nil
	}

	// Build the stop command
	cmd := s.buildCommand("stop")
	cmd.Args = append(cmd.Args, services...)

	s.logger.Info().Strs("services", services).Dur("timeout", timeout).Msg("Stopping systemd services")

	// Systemctl stop doesn't have a built-in timeout, so we'll use context timeout
	output, err := cmd.CombinedOutput()
	if err != nil {
		s.logger.Warn().Err(err).Bytes("output", output).Msg("Failed to stop some systemd services")
		// Check if the error is because services weren't running
		if !strings.Contains(string(output), "not running") &&
			!strings.Contains(string(output), "not loaded") {
			return fmt.Errorf("failed to stop systemd services: %w", err)
		}
	}

	// Wait for services to actually be stopped using status checks
	if err := s.waitForStopped(ctx, services, timeout); err != nil {
		s.logger.Warn().Err(err).Msg("Systemd services may not have stopped completely within timeout")
		// Don't return error here as the stop command was issued
	}

	s.logger.Info().Strs("services", services).Msg("Systemd services stopped successfully")
	return nil
}

// Start starts the specified systemd services
func (s *Systemd) Start(ctx context.Context, services []string, timeout time.Duration) error {
	if len(services) == 0 {
		s.logger.Debug().Msg("No services to start")
		return nil
	}

	// Build the start command
	cmd := s.buildCommand("start")
	cmd.Args = append(cmd.Args, services...)

	s.logger.Info().Strs("services", services).Msg("Starting systemd services")

	output, err := cmd.CombinedOutput()
	if err != nil {
		s.logger.Error().Err(err).Bytes("output", output).Msg("Failed to start systemd services")
		return fmt.Errorf("failed to start systemd services: %w", err)
	}

	// Wait for services to be running
	if err := s.waitForRunning(ctx, services, timeout); err != nil {
		s.logger.Warn().Err(err).Msg("Systemd services started but not all running within timeout")
		// Don't return error here as the start command was issued
	}

	s.logger.Info().Strs("services", services).Msg("Systemd services started successfully")
	return nil
}

// waitForStopped waits for the specified services to be stopped
func (s *Systemd) waitForStopped(ctx context.Context, services []string, timeout time.Duration) error {
	startTime := time.Now()

	for {
		// Check if all services are stopped
		allStopped := true
		for _, svc := range services {
			if s.IsServiceRunning(ctx, svc) {
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

// waitForRunning waits for the specified services to be running
func (s *Systemd) waitForRunning(ctx context.Context, services []string, timeout time.Duration) error {
	startTime := time.Now()

	for {
		// Check if all services are running
		allRunning := true
		for _, svc := range services {
			if !s.IsServiceRunning(ctx, svc) {
				allRunning = false
				break
			}
		}

		if allRunning {
			return nil
		}

		// Check timeout
		if time.Since(startTime) >= timeout {
			return fmt.Errorf("timeout waiting for services to start")
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

// Ps returns information about running systemd services
func (s *Systemd) Ps(ctx context.Context, services []string) (string, error) {
	cmd := s.buildCommand("status")
	if len(services) > 0 {
		cmd.Args = append(cmd.Args, services...)
	} else {
		// If no specific services, use --all to show all units
		// But this might be too broad, so we'll just show the configured units
		cmd.Args = append(cmd.Args, s.units...)
	}

	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get systemd service status: %w", err)
	}

	return string(output), nil
}

// IsServiceRunning checks if a systemd service is running
func (s *Systemd) IsServiceRunning(ctx context.Context, service string) bool {
	output, err := s.Ps(ctx, []string{service})
	if err != nil {
		return false
	}

	// Check if the service is active (running)
	// systemctl status output includes "Active: active (running)" for running services
	return strings.Contains(output, "Active: active (running)") ||
		strings.Contains(output, "active (running)")
}
