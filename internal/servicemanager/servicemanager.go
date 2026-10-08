// Package servicemanager stops and starts the services of a project around its backup:
// Docker/Podman Compose projects, systemd units, or nothing (noop). Each manager runs its
// CLI through an injected Runner and polls with an injected Clock.
package servicemanager

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dombyte/restor/internal/util"
)

const (
	// pollInterval is the pause between two status checks while waiting for services.
	pollInterval = 500 * time.Millisecond
	// statusTimeout bounds one status query (compose ps/config, systemctl is-active).
	statusTimeout = 30 * time.Second
	// commandGrace is added to the configured stop/start timeout to bound the stop/start
	// command itself (image pulls, slow unit dependencies).
	commandGrace = 2 * time.Minute
)

var (
	// ErrMissingDependency is returned by the constructors for a missing dependency or
	// setting.
	ErrMissingDependency = errors.New("servicemanager: missing dependency")

	errUnexpectedOutput = errors.New("unexpected output")
	errUnknownUnit      = errors.New("service is not one of the systemd units")
)

// Runner executes a program and returns its combined output, also on failure.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Deps are the dependencies of the compose and systemd managers.
type Deps struct {
	// Runner executes docker, podman or systemctl.
	Runner Runner
	// Clock drives the status polling.
	Clock util.Clock
}

func (d Deps) validate() error {
	return util.RequireAll(ErrMissingDependency,
		util.Requirement{Name: "Runner", OK: d.Runner != nil},
		util.Requirement{Name: "Clock", OK: d.Clock != nil},
	)
}

// waitUntil polls done every pollInterval until it reports true (true), ctx ends, or
// timeout has passed (false). A failed poll (e.g. a status query that timed out) is
// retried; its error is returned only when the last poll before the timeout failed.
func waitUntil(ctx context.Context, clock util.Clock, timeout time.Duration,
	done func(ctx context.Context) (bool, error),
) (bool, error) {
	deadline := clock.Now().Add(timeout)
	for {
		ok, err := done(ctx)
		if err == nil && ok {
			return true, nil
		}
		if !clock.Now().Before(deadline) {
			return false, err
		}
		select {
		case <-ctx.Done():
			return false, fmt.Errorf("servicemanager: wait: %w", ctx.Err())
		case <-clock.After(pollInterval):
		}
	}
}

// commandContext bounds a stop/start command by the configured timeout plus commandGrace.
func commandContext(ctx context.Context, timeout time.Duration) (context.Context,
	context.CancelFunc,
) {
	return context.WithTimeout(ctx, timeout+commandGrace)
}
