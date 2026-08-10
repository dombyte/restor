package docker

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// Compose wraps the docker compose CLI
type Compose struct {
	composeFile string
	env         []string
	logger      zerolog.Logger
}

// NewCompose creates a new Compose wrapper
func NewCompose(composeFile string, env []string, logger ...zerolog.Logger) *Compose {
	l := log.Logger
	if len(logger) > 0 {
		l = logger[0]
	}
	return &Compose{
		composeFile: composeFile,
		env:         env,
		logger:      l,
	}
}

// buildCommand builds the base docker compose command
func (c *Compose) buildCommand(args ...string) *exec.Cmd {
	cmdArgs := []string{"compose", "-f", c.composeFile}
	cmdArgs = append(cmdArgs, args...)

	cmd := exec.Command("docker", cmdArgs...)
	cmd.Env = c.env

	return cmd
}

// GetServices returns the list of services defined in the compose file
// If services parameter is provided, returns only those services
// If services parameter is empty, returns all services
func (c *Compose) GetServices(ctx context.Context, services []string) ([]string, error) {
	if len(services) > 0 {
		// Use the provided services list
		return services, nil
	}

	// Get all services from docker compose config
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

	log.Debug().Strs("services", result).Msg("Found services in compose file")
	return result, nil
}

// Stop stops the specified services
func (c *Compose) Stop(ctx context.Context, services []string, timeout time.Duration) error {
	if len(services) == 0 {
		c.logger.Info().Msg("No services to stop")
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
func (c *Compose) Start(ctx context.Context, services []string, timeout time.Duration) error {
	if len(services) == 0 {
		c.logger.Info().Msg("No services to start")
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
func (c *Compose) waitForHealthy(ctx context.Context, services []string, timeout time.Duration) error {
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
func (c *Compose) Ps(ctx context.Context, services []string) (string, error) {
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
