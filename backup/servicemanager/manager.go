package servicemanager

import (
	"context"
	"time"
)

// ServiceManager defines the interface for service managers
// that can start, stop, and query container/service status
type ServiceManager interface {
	// GetServices returns the list of services to manage
	// For docker/podman: reads from compose file if services not provided
	// For systemd: returns systemd_units from config if services not provided
	GetServices(ctx context.Context, services []string) ([]string, error)

	// Stop stops the specified services
	Stop(ctx context.Context, services []string, timeout time.Duration) error

	// Start starts the specified services
	Start(ctx context.Context, services []string, timeout time.Duration) error

	// Ps returns information about running services
	Ps(ctx context.Context, services []string) (string, error)

	// GetLockFileName returns a unique identifier for locking
	// This is used by the locker to prevent concurrent operations on the same project
	GetLockFileName() string
}

// ServiceConfig holds the configuration for a service manager
type ServiceConfig struct {
	ServiceManagerType string
	ComposeFile        string
	SystemdUnits       []string
	SystemdScope       string
}

// Validate validates the service configuration
func (sc *ServiceConfig) Validate() error {
	switch sc.ServiceManagerType {
	case TypeDockerCompose, TypePodmanCompose:
		if sc.ComposeFile == "" {
			return &ConfigValidationError{Field: "compose_file", Message: "compose_file is required for docker-compose and podman-compose"}
		}
	case TypeSystemd:
		if len(sc.SystemdUnits) == 0 {
			return &ConfigValidationError{Field: "systemd_units", Message: "systemd_units is required for systemd"}
		}
		if sc.SystemdScope != "" && sc.SystemdScope != ScopeSystem && sc.SystemdScope != ScopeUser {
			return &ConfigValidationError{
				Field:    "systemd_scope",
				Message:  "systemd_scope must be 'system' or 'user'",
			}
		}
	default:
		return &ConfigValidationError{
			Field:    "service_manager",
			Message:  "unsupported service manager type: " + sc.ServiceManagerType,
		}
	}
	return nil
}

// ConfigValidationError represents a configuration validation error
type ConfigValidationError struct {
	Field   string
	Message string
}

func (e *ConfigValidationError) Error() string {
	return e.Field + ": " + e.Message
}