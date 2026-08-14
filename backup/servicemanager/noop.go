package servicemanager

import (
	"context"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// Noop is a service manager that performs no operations
// Used for file-only backups where no services need to be stopped/started
type Noop struct {
	env    []string
	logger zerolog.Logger
}

// NewNoop creates a new Noop service manager
func NewNoop(env []string, logger ...zerolog.Logger) *Noop {
	l := log.Logger
	if len(logger) > 0 {
		l = logger[0]
	}
	return &Noop{
		env:    env,
		logger: l,
	}
}

// GetLockFileName returns "noop" as the lock file identifier
func (n *Noop) GetLockFileName() string {
	return TypeNoop
}

// GetServices returns an empty slice since noop manager doesn't manage any services
func (n *Noop) GetServices(ctx context.Context, services []string) ([]string, error) {
	n.logger.Debug().Msg("Noop service manager: no services to manage")
	return []string{}, nil
}

// Stop is a no-op that always succeeds
func (n *Noop) Stop(ctx context.Context, services []string, timeout time.Duration) error {
	n.logger.Debug().Strs("services", services).Dur("timeout", timeout).Msg("Noop service manager: stop is a no-op")
	return nil
}

// Start is a no-op that always succeeds
func (n *Noop) Start(ctx context.Context, services []string, timeout time.Duration) error {
	n.logger.Debug().Strs("services", services).Dur("timeout", timeout).Msg("Noop service manager: start is a no-op")
	return nil
}

// Ps returns an empty string since noop manager doesn't have any services to report
func (n *Noop) Ps(ctx context.Context, services []string) (string, error) {
	n.logger.Debug().Strs("services", services).Msg("Noop service manager: ps returns empty")
	return "", nil
}
