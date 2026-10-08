package servicemanager

import (
	"os/exec"
	"strings"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// Factory creates ServiceManager instances based on configuration
// It also performs binary validation and logs warnings for missing binaries
type Factory struct {
	// No state needed for this factory
}

// NewFactory creates a new factory instance
func NewFactory() *Factory {
	return &Factory{}
}

// CreateServiceManager creates a ServiceManager based on the configuration
// It validates binaries and logs warnings if they are not found
func (f *Factory) CreateServiceManager(config *ServiceConfig, env []string, logger ...zerolog.Logger) (ServiceManager, error) {
	// Validate the configuration first
	if err := config.Validate(); err != nil {
		return nil, err
	}

	// Validate and warn about missing binaries (skip for noop)
	if config.ServiceManagerType != TypeNoop {
		f.validateBinaries(config)
	}

	// Use provided logger or default
	l := log.Logger
	if len(logger) > 0 {
		l = logger[0]
	}

	// Add service_manager to the logger context for consistent logging
	// Note: Project name may already be in the logger passed from project.go
	l = l.With().Str("service_manager", config.ServiceManagerType).Logger()

	switch config.ServiceManagerType {
	case TypeDockerCompose:
		l.Debug().Str("service_manager", TypeDockerCompose).Str("compose_file", config.ComposeFile).Msg("Creating Docker Compose service manager")
		return NewDockerCompose(config.ComposeFile, env, l), nil

	case TypePodmanCompose:
		l.Debug().Str("service_manager", TypePodmanCompose).Str("compose_file", config.ComposeFile).Msg("Creating Podman Compose service manager")
		return NewPodmanCompose(config.ComposeFile, env, l), nil

	case TypeSystemd:
		scope := config.SystemdScope
		if scope == "" {
			scope = DefaultSystemdScope
		}
		l.Debug().Str("service_manager", TypeSystemd).Strs("units", config.SystemdUnits).Str("scope", scope).Msg("Creating Systemd service manager")
		return NewSystemd(config.SystemdUnits, scope, env, l), nil

	case TypeNoop:
		l.Debug().Str("service_manager", TypeNoop).Msg("Creating Noop service manager")
		return NewNoop(env, l), nil

	default:
		return nil, &ConfigValidationError{
			Field:   "service_manager",
			Message: "unsupported service manager type: " + config.ServiceManagerType,
		}
	}
}

// validateBinaries checks if the required binaries exist and logs warnings
func (f *Factory) validateBinaries(config *ServiceConfig) {
	binary := ""

	switch config.ServiceManagerType {
	case TypeDockerCompose:
		binary = BinaryDocker
	case TypePodmanCompose:
		binary = BinaryPodman
	case TypeSystemd:
		binary = BinarySystemctl
	default:
		return // No binary to validate for unknown types
	}

	// Check if binary exists in PATH
	if !binaryExists(binary) {
		log.Warn().Str("binary", binary).Str("service_manager", config.ServiceManagerType).Msg("Binary not found in PATH - operations will fail if the binary is not available at runtime")
	} else {
		log.Debug().Str("binary", binary).Str("service_manager", config.ServiceManagerType).Msg("Binary found in PATH")
	}
}

// binaryExists checks if a binary exists in the system PATH
func binaryExists(binaryName string) bool {
	// Try to find the binary using 'which' or 'command -v'
	cmd := exec.Command("command", "-v", binaryName)
	output, err := cmd.Output()

	// If command succeeds, the binary exists
	if err == nil && len(output) > 0 {
		return true
	}

	// Try with 'which' as a fallback
	cmd = exec.Command("which", binaryName)
	output, err = cmd.Output()
	if err == nil && len(output) > 0 {
		return true
	}

	return false
}

// GetBinaryPath returns the full path to a binary if it exists, empty string otherwise
func GetBinaryPath(binaryName string) string {
	cmd := exec.Command("command", "-v", binaryName)
	output, err := cmd.Output()
	if err == nil {
		return strings.TrimSpace(string(output))
	}
	return ""
}

// CheckRequiredBinary checks if a specific binary is available and returns its path or error
func CheckRequiredBinary(binaryName string) (string, error) {
	path := GetBinaryPath(binaryName)
	if path == "" {
		return "", exec.ErrNotFound
	}
	return path, nil
}
