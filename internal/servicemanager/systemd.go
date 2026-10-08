package servicemanager

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dombyte/restor/internal/util"
)

// systemctl is the systemd CLI, looked up in PATH.
const systemctl = "systemctl"

// SystemdSettings configure a Systemd manager.
type SystemdSettings struct {
	// Units are the managed unit names.
	Units []string
	// User selects the user manager (`systemctl --user`).
	User bool
}

// Systemd manages systemd units with systemctl.
type Systemd struct {
	s      SystemdSettings
	runner Runner
	clock  util.Clock
}

// NewSystemd returns a Systemd manager.
func NewSystemd(s SystemdSettings, d Deps) (*Systemd, error) {
	if err := d.validate(); err != nil {
		return nil, err
	}
	if err := util.RequireAll(ErrMissingDependency,
		util.Requirement{Name: "Units", OK: len(s.Units) > 0},
	); err != nil {
		return nil, err
	}
	return &Systemd{s: s, runner: d.Runner, clock: d.Clock}, nil
}

// LockKey returns the unit names joined with commas.
func (s *Systemd) LockKey() string { return strings.Join(s.s.Units, ",") }

// Services returns the requested names that are configured units, or all units when
// requested is empty or names none of them.
func (s *Systemd) Services(_ context.Context, requested []string) ([]string, error) {
	var filtered []string
	for _, r := range requested {
		if slices.Contains(s.s.Units, r) {
			filtered = append(filtered, r)
		}
	}
	if len(filtered) == 0 {
		return s.s.Units, nil
	}
	return filtered, nil
}

// Stop runs `systemctl stop <units>` and waits until none of them is active. It returns
// false when some are still active after timeout.
func (s *Systemd) Stop(ctx context.Context, units []string, timeout time.Duration) (bool, error) {
	if len(units) == 0 {
		return true, nil
	}
	cmdCtx, cancel := commandContext(ctx, timeout)
	defer cancel()
	if out, err := s.run(cmdCtx, append([]string{"stop"}, units...)...); err != nil &&
		!strings.Contains(string(out), "not running") &&
		!strings.Contains(string(out), "not loaded") {
		return false, fmt.Errorf("servicemanager: stop %s: %w", s.LockKey(), err)
	}
	return waitUntil(ctx, s.clock, timeout, func(ctx context.Context) (bool, error) {
		states, err := s.states(ctx, units)
		return err == nil && !slices.ContainsFunc(states, isUp), err
	})
}

// Start runs `systemctl start <units>` and waits until all of them are active. It
// returns false when some are not active after timeout.
func (s *Systemd) Start(ctx context.Context, units []string, timeout time.Duration) (bool, error) {
	if len(units) == 0 {
		return true, nil
	}
	cmdCtx, cancel := commandContext(ctx, timeout)
	defer cancel()
	if _, err := s.run(cmdCtx, append([]string{"start"}, units...)...); err != nil {
		return false, fmt.Errorf("servicemanager: start %s: %w", s.LockKey(), err)
	}
	return waitUntil(ctx, s.clock, timeout, func(ctx context.Context) (bool, error) {
		states, err := s.states(ctx, units)
		return err == nil && !slices.ContainsFunc(states, func(st string) bool {
			return st != "active"
		}), err
	})
}

// states runs `systemctl is-active <units>`, which prints one state per unit in order
// (active, inactive, failed, activating, …) and exits non-zero when any unit is not
// active.
func (s *Systemd) states(ctx context.Context, units []string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	out, err := s.run(ctx, append([]string{"is-active"}, units...)...)
	states := strings.Fields(string(out))
	if len(states) != len(units) {
		if err == nil {
			err = fmt.Errorf("%w: %q", errUnexpectedOutput, out)
		}
		return nil, fmt.Errorf("servicemanager: status of %s: %w", s.LockKey(), err)
	}
	return states, nil
}

// isUp reports whether a unit state means the unit is still (or already) running.
func isUp(state string) bool {
	switch state {
	case "active", "activating", "deactivating", "reloading", "refreshing":
		return true
	default:
		return false
	}
}

func (s *Systemd) run(ctx context.Context, args ...string) ([]byte, error) {
	if s.s.User {
		args = append([]string{"--user"}, args...)
	}
	return s.runner.Run(ctx, systemctl, args...)
}
