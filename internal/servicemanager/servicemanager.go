// Package servicemanager stops and starts the services of a job around its backup:
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
	errUnknownService   = errors.New("service is not in the compose file")
	// errStartFailed ends the wait for a start: a service failed or exited.
	errStartFailed = errors.New("service failed to start")
)

// Runner executes a program and returns its stdout, also on failure. Its error carries
// the program's stderr.
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
// deadline has passed (false); it polls at least once. A failed poll (e.g. a status query
// that timed out) is retried; its error is returned only when the last poll before the
// deadline failed, or at once when it wraps errStartFailed.
func waitUntil(ctx context.Context, clock util.Clock, deadline time.Time,
	done func(ctx context.Context) (bool, error),
) (bool, error) {
	for {
		ok, err := done(ctx)
		if err == nil && ok {
			return true, nil
		}
		if gaveUp(err, clock, deadline) {
			return false, err
		}
		select {
		case <-ctx.Done():
			return false, fmt.Errorf("servicemanager: wait: %w", ctx.Err())
		case <-clock.After(pollInterval):
		}
	}
}

// gaveUp reports whether waitUntil stops after a poll that returned err: at the deadline,
// or at once for a service that failed to start.
func gaveUp(err error, clock util.Clock, deadline time.Time) bool {
	return errors.Is(err, errStartFailed) || !clock.Now().Before(deadline)
}

// confirmed returns a poll result that reports true only when up was true at this poll
// and the one before: a service that crashes at once is still reported as running right
// after its start command ("Up Less than a second", or active for a Type=simple unit), and
// the second look, pollInterval later, sees it exited or failed.
func confirmed() func(up bool) bool {
	before := false
	return func(up bool) bool {
		done := up && before
		before = up
		return done
	}
}

// commandContext bounds a stop/start command by the configured timeout plus commandGrace.
// The timeout counts from before the command, so a slow command shortens the wait that
// follows: Stop and Start take at most timeout + commandGrace + one status query.
func commandContext(ctx context.Context, timeout time.Duration) (context.Context,
	context.CancelFunc,
) {
	return context.WithTimeout(ctx, timeout+commandGrace)
}
