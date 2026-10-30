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

// LockKeys returns the unit names, sorted: one lock per unit, so projects that share
// only some units still exclude each other.
func (s *Systemd) LockKeys() []string {
	return slices.Compact(slices.Sorted(slices.Values(s.s.Units)))
}

// Running returns the units among requested (all units when it is empty) that are up
// (active, activating, reloading, …). A requested name that is not a configured unit is
// an error: falling back to all units would stop more than asked.
func (s *Systemd) Running(ctx context.Context, requested []string) ([]string, error) {
	units, err := s.All(ctx, requested)
	if err != nil {
		return nil, err
	}
	states, err := s.states(ctx, units)
	if err != nil {
		return nil, err
	}
	var up []string
	for i, u := range units {
		if isUp(states[i]) {
			up = append(up, u)
		}
	}
	return up, nil
}

// All returns a copy of requested, or all units when it is empty, whether up or not. A
// requested name that is not a configured unit is an error.
func (s *Systemd) All(_ context.Context, requested []string) ([]string, error) {
	units := s.s.Units
	if len(requested) > 0 {
		units = requested
	}
	for _, u := range units {
		if !slices.Contains(s.s.Units, u) {
			return nil, fmt.Errorf("servicemanager: %w: %q", errUnknownUnit, u)
		}
	}
	return slices.Clone(units), nil
}

// Stop runs `systemctl stop <units>` and waits until none of them is active. It returns
// false when some are still active after timeout.
func (s *Systemd) Stop(ctx context.Context, units []string, timeout time.Duration) (bool, error) {
	if len(units) == 0 {
		return true, nil
	}
	deadline := s.clock.Now().Add(timeout)
	cmdCtx, cancel := commandContext(ctx, timeout)
	defer cancel()
	// systemctl reports these on stderr, which the runner's error carries.
	if _, err := s.run(cmdCtx, append([]string{"stop"}, units...)...); err != nil &&
		!strings.Contains(err.Error(), "not running") &&
		!strings.Contains(err.Error(), "not loaded") {
		return false, fmt.Errorf("servicemanager: stop %s: %w", unitList(units), err)
	}
	return waitUntil(ctx, s.clock, deadline, func(ctx context.Context) (bool, error) {
		states, err := s.states(ctx, units)
		return err == nil && !slices.ContainsFunc(states, isUp), err
	})
}

// Start runs `systemctl start <units>` and waits until all of them are active, or, for a
// Type=oneshot unit, inactive: `systemctl start` waits until a oneshot unit has run and
// fails when it failed, so inactive means it ran (unless it sets RemainAfterExit=yes,
// then it is active). It returns false when some are not started after timeout.
func (s *Systemd) Start(ctx context.Context, units []string, timeout time.Duration) (bool, error) {
	if len(units) == 0 {
		return true, nil
	}
	deadline := s.clock.Now().Add(timeout)
	cmdCtx, cancel := commandContext(ctx, timeout)
	defer cancel()
	if _, err := s.run(cmdCtx, append([]string{"start"}, units...)...); err != nil {
		return false, fmt.Errorf("servicemanager: start %s: %w", unitList(units), err)
	}
	var oneshot []bool // looked up once, when a unit is inactive
	return waitUntil(ctx, s.clock, deadline, func(ctx context.Context) (bool, error) {
		states, err := s.states(ctx, units)
		if err != nil {
			return false, err
		}
		if oneshot == nil && slices.Contains(states, "inactive") {
			oneshot = s.oneshots(ctx, units)
		}
		return started(states, oneshot), nil
	})
}

// started reports whether every unit is active, or inactive and a oneshot unit.
func started(states []string, oneshot []bool) bool {
	for i, st := range states {
		if st != "active" && (st != "inactive" || i >= len(oneshot) || !oneshot[i]) {
			return false
		}
	}
	return true
}

// oneshots reports for each unit whether it is a Type=oneshot unit, from
// `systemctl show --property=Type <units>` (one "Type=…" block per unit, in order). When
// the query fails, no unit counts as oneshot: Start then waits for active, as before,
// and reports a timeout instead of a started unit.
func (s *Systemd) oneshots(ctx context.Context, units []string) []bool {
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	oneshot := make([]bool, len(units))
	out, err := s.run(ctx, append([]string{"show", "--property=Type"}, units...)...)
	blocks := strings.Split(strings.TrimSpace(string(out)), "\n\n")
	if err != nil || len(blocks) != len(units) {
		return oneshot
	}
	for i, b := range blocks {
		oneshot[i] = strings.TrimSpace(b) == "Type=oneshot"
	}
	return oneshot
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
		return nil, fmt.Errorf("servicemanager: status of %s: %w", unitList(units), err)
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

// unitList names units in errors.
func unitList(units []string) string { return strings.Join(units, ",") }

func (s *Systemd) run(ctx context.Context, args ...string) ([]byte, error) {
	if s.s.User {
		args = append([]string{"--user"}, args...)
	}
	return s.runner.Run(ctx, systemctl, args...)
}
