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

// PodmanCompose wraps the podman compose CLI
type PodmanCompose struct {
	composeFile string
	env         []string
	logger      zerolog.Logger
}

// NewPodmanCompose creates a new PodmanCompose wrapper
func NewPodmanCompose(composeFile string, env []string, logger ...zerolog.Logger) *PodmanCompose {
	l := log.Logger
	if len(logger) > 0 {
		l = logger[0]
	}
	return &PodmanCompose{
		composeFile: composeFile,
		env:         env,
		logger:      l,
	}
}

// GetLockFileName returns the compose file path for locking
func (c *PodmanCompose) GetLockFileName() string {
	return c.composeFile
}

// buildCommand builds the base podman compose command
func (c *PodmanCompose) buildCommand(args ...string) *exec.Cmd {
	cmdArgs := []string{"compose", "-f", c.composeFile}
	cmdArgs = append(cmdArgs, args...)

	cmd := exec.Command("podman", cmdArgs...)
	cmd.Env = c.env

	return cmd
}

// GetServices returns the list of services defined in the compose file
// If services parameter is provided, returns only those services
// If services parameter is empty, returns all services
func (c *PodmanCompose) GetServices(ctx context.Context, services []string) ([]string, error) {
	if len(services) > 0 {
		// Use the provided services list
		return services, nil
	}

	// Get all services from podman compose config
	cmd := c.buildCommand("config", "--services")

	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get compose services: %w", err)
	}

	// Parse the output (one service per line)
	serviceList := strings.Split(strings.TrimSpace(string(output)), "\n")
	var result []string

	for _, svc := range serviceList {
		svc = strings.TrimSpace(svc)
		if svc != "" {
			result = append(result, svc)
		}
	}

	c.logger.Debug().Strs("services", result).Msg("Found services in compose file")
	return result, nil
}

// Stop stops the specified services
func (c *PodmanCompose) Stop(ctx context.Context, services []string, timeout time.Duration) error {
	if len(services) == 0 {
		c.logger.Debug().Msg("No services to stop")
		return nil
	}

	cmd := c.buildCommand("stop", "-t", fmt.Sprintf("%d", int(timeout.Seconds())))
	cmd.Args = append(cmd.Args, services...)

	c.logger.Info().Strs("services", services).Dur("timeout", timeout).Msg("Stopping services")

	output, err := cmd.CombinedOutput()
	if err != nil {
		c.logger.Warn().Err(err).Bytes("output", output).Msg("Failed to stop some services")
		// Check if the error is because containers weren't running
		// This is not necessarily a critical error
		if !strings.Contains(string(output), "No containers to stop") &&
			!strings.Contains(string(output), "no containers to stop") {
			return fmt.Errorf("failed to stop services: %w", err)
		}
	}

	c.logger.Info().Strs("services", services).Msg("Services stopped successfully")
	return nil
}

// Start starts the specified services
func (c *PodmanCompose) Start(ctx context.Context, services []string, timeout time.Duration) error {
	if len(services) == 0 {
		c.logger.Debug().Msg("No services to start")
		return nil
	}

	cmd := c.buildCommand("up", "-d")
	cmd.Args = append(cmd.Args, services...)

	c.logger.Info().Strs("services", services).Msg("Starting services")

	output, err := cmd.CombinedOutput()
	if err != nil {
		c.logger.Error().Err(err).Bytes("output", output).Msg("Failed to start services")
		return fmt.Errorf("failed to start services: %w", err)
	}

	// Wait for containers to be healthy
	if err := c.waitForHealthy(ctx, services, timeout); err != nil {
		c.logger.Warn().Err(err).Msg("Services started but not all healthy within timeout")
	}

	c.logger.Info().Strs("services", services).Msg("Services started successfully")
	return nil
}

// waitForHealthy waits for the specified services to become healthy
func (c *PodmanCompose) waitForHealthy(ctx context.Context, services []string, timeout time.Duration) error {
	// For simplicity, we'll just wait for the timeout duration
	// A more robust implementation would check container health status
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Ps returns information about running containers
func (c *PodmanCompose) Ps(ctx context.Context, services []string) (string, error) {
	cmd := c.buildCommand("ps")
	if len(services) > 0 {
		cmd.Args = append(cmd.Args, services...)
	}

	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get container status: %w", err)
	}

	return string(output), nil
}

// IsContainerRunning checks if a container is running
func (c *PodmanCompose) IsContainerRunning(ctx context.Context, service string) bool {
	output, err := c.Ps(ctx, []string{service})
	if err != nil {
		return false
	}

	// Check if the service appears in the output and is running
	// Podman compose ps output includes status information
	return strings.Contains(output, service) &&
		(strings.Contains(output, "Up") || strings.Contains(output, "Running"))
}
