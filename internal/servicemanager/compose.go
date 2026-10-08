package servicemanager

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dombyte/restor/internal/util"
)

// composeServiceLabel is the container label naming the compose service (podman-compose
// reports services only through it).
const composeServiceLabel = "com.docker.compose.service"

// ComposeSettings configure a Compose manager.
type ComposeSettings struct {
	// Binary is "docker" or "podman"; the manager runs `<binary> compose -f <file> …`.
	Binary string
	// File is the compose file path.
	File string
}

// Compose manages the services of one compose file with docker or podman compose.
type Compose struct {
	s      ComposeSettings
	runner Runner
	clock  util.Clock
}

// NewCompose returns a Compose manager.
func NewCompose(s ComposeSettings, d Deps) (*Compose, error) {
	if err := d.validate(); err != nil {
		return nil, err
	}
	if err := util.RequireAll(ErrMissingDependency,
		util.Requirement{Name: "Binary", OK: s.Binary != ""},
		util.Requirement{Name: "File", OK: s.File != ""},
	); err != nil {
		return nil, err
	}
	return &Compose{s: s, runner: d.Runner, clock: d.Clock}, nil
}

// LockKey returns the compose file path.
func (c *Compose) LockKey() string { return c.s.File }

// Services returns requested, or every service of the compose file when it is empty.
func (c *Compose) Services(ctx context.Context, requested []string) ([]string, error) {
	if len(requested) > 0 {
		return requested, nil
	}
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	out, err := c.run(ctx, "config", "--services")
	if err != nil {
		return nil, fmt.Errorf("servicemanager: list services of %s: %w", c.s.File, err)
	}
	return strings.Fields(string(out)), nil
}

// Stop runs `stop -t <timeout> <services>` and waits until none of them is running. It
// returns false when they still run after timeout.
func (c *Compose) Stop(ctx context.Context, services []string, timeout time.Duration) (
	bool, error,
) {
	if len(services) == 0 {
		return true, nil
	}
	cmdCtx, cancel := commandContext(ctx, timeout)
	defer cancel()
	args := append([]string{"stop", "-t", strconv.Itoa(int(timeout.Seconds()))}, services...)
	if out, err := c.run(cmdCtx, args...); err != nil &&
		!strings.Contains(strings.ToLower(string(out)), "no containers to stop") {
		return false, fmt.Errorf("servicemanager: stop %s: %w", c.s.File, err)
	}
	return waitUntil(ctx, c.clock, timeout, func(ctx context.Context) (bool, error) {
		running, err := c.running(ctx)
		return err == nil && !anyOf(services, running), err
	})
}

// Start runs `up -d <services>` and waits for timeout before it returns true.
func (c *Compose) Start(ctx context.Context, services []string, timeout time.Duration) (
	bool, error,
) {
	if len(services) == 0 {
		return true, nil
	}
	cmdCtx, cancel := commandContext(ctx, timeout)
	defer cancel()
	if _, err := c.run(cmdCtx, append([]string{"up", "-d"}, services...)...); err != nil {
		return false, fmt.Errorf("servicemanager: start %s: %w", c.s.File, err)
	}
	select {
	case <-ctx.Done():
		return false, fmt.Errorf("servicemanager: wait: %w", ctx.Err())
	case <-c.clock.After(timeout):
		return true, nil
	}
}

// running returns the services of the compose project that have a running container.
func (c *Compose) running(ctx context.Context) (map[string]bool, error) {
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	out, err := c.run(ctx, "ps", "--format", "json")
	if err != nil {
		return nil, fmt.Errorf("servicemanager: status of %s: %w", c.s.File, err)
	}
	entries, err := parseComposePs(out)
	if err != nil {
		return nil, fmt.Errorf("servicemanager: status of %s: %w", c.s.File, err)
	}
	running := map[string]bool{}
	for _, e := range entries {
		if strings.EqualFold(e.State, "running") {
			running[e.service()] = true
		}
	}
	return running, nil
}

func (c *Compose) run(ctx context.Context, args ...string) ([]byte, error) {
	return c.runner.Run(ctx, c.s.Binary, append([]string{"compose", "-f", c.s.File}, args...)...)
}

// psEntry is one container of `compose ps --format json`. Docker Compose sets Service;
// podman-compose passes through `podman ps`, which only has the label.
type psEntry struct {
	Service string          `json:"Service"`
	State   string          `json:"State"`
	Labels  json.RawMessage `json:"Labels"`
}

func (e psEntry) service() string {
	if e.Service != "" {
		return e.Service
	}
	var labels map[string]string
	if json.Unmarshal(e.Labels, &labels) != nil {
		return "" // Docker reports labels as one string; Service is set there
	}
	return labels[composeServiceLabel]
}

// parseComposePs accepts a JSON array (podman, older Docker Compose) or one JSON object
// per line (Docker Compose ≥ 2.21).
func parseComposePs(out []byte) ([]psEntry, error) {
	out = bytes.TrimSpace(out)
	if len(out) == 0 {
		return nil, nil
	}
	var entries []psEntry
	if out[0] == '[' {
		if err := json.Unmarshal(out, &entries); err != nil {
			return nil, fmt.Errorf("parse ps output: %w", err)
		}
		return entries, nil
	}
	for line := range bytes.SplitSeq(out, []byte("\n")) {
		if line = bytes.TrimSpace(line); len(line) == 0 {
			continue
		}
		var e psEntry
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("parse ps output: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// anyOf reports whether one of names is in set.
func anyOf(names []string, set map[string]bool) bool {
	for _, n := range names {
		if set[n] {
			return true
		}
	}
	return false
}
