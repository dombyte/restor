package servicemanager

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dombyte/restor/internal/util"
)

// composeServiceLabel is the container label naming the compose service (podman-compose
// reports services only through it).
const composeServiceLabel = "com.docker.compose.service"

// stateRunning is the container state of a running service.
const stateRunning = "running"

// ComposeSettings configure a Compose manager.
type ComposeSettings struct {
	// Binary is "docker" or "podman"; the manager runs `<binary> compose -f <file> …`.
	Binary string
	// Files are the compose file paths, each passed with -f; the first one names the
	// job's services in lock keys and errors, the others override it.
	Files []string
	// ProjectName is passed with -p; empty leaves it to Compose (the file's directory).
	ProjectName string
	// Profiles are enabled with --profile, so that their services are found and stopped.
	Profiles []string
}

// Compose manages the services of one compose file with docker or podman compose. It
// belongs to one job of one run, which uses it from one goroutine at a time.
type Compose struct {
	s      ComposeSettings
	runner Runner
	clock  util.Clock
	// services caches `config --services`: the compose files do not change during a job,
	// and the command is slow (Compose parses and interpolates every file).
	services []string
	// seen are the services `ps` reported a container for: a listed service among them
	// needs no `config --services` to tell it from a typo.
	seen map[string]bool
}

// NewCompose returns a Compose manager.
func NewCompose(s ComposeSettings, d Deps) (*Compose, error) {
	if err := d.validate(); err != nil {
		return nil, err
	}
	if err := util.RequireAll(ErrMissingDependency,
		util.Requirement{Name: "Binary", OK: s.Binary != ""},
		util.Requirement{Name: "Files", OK: len(s.Files) > 0 && s.Files[0] != ""},
	); err != nil {
		return nil, err
	}
	return &Compose{s: s, runner: d.Runner, clock: d.Clock}, nil
}

// LockKeys returns "compose:<compose file path>", for docker and podman alike: both
// back up the same data.
func (c *Compose) LockKeys() []string { return []string{"compose:" + c.file()} }

// file returns the compose file that names the job's services.
func (c *Compose) file() string { return c.s.Files[0] }

// Running returns the services among requested (every service of the compose file when
// it is empty) that have a running container. A requested name that is not a service of
// the compose file is an error: a typo would otherwise back up a running service.
func (c *Compose) Running(ctx context.Context, requested []string) ([]string, error) {
	running, err := c.running(ctx)
	if err != nil {
		return nil, err
	}
	services, err := c.listServices(ctx, requested)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(services, func(s string) bool { return !running[s] }), nil
}

// All returns requested, or every service of the compose file when it is empty, whether
// running or not. A requested name that is not a service of the compose file is an error.
func (c *Compose) All(ctx context.Context, requested []string) ([]string, error) {
	return c.listServices(ctx, requested)
}

// listServices returns a copy of requested, or of every service of the compose file.
// Requested services that `ps` reported exist: only another name needs the slow
// `config --services` to be checked.
func (c *Compose) listServices(ctx context.Context, requested []string) ([]string, error) {
	if len(requested) > 0 && !slices.ContainsFunc(requested, func(s string) bool {
		return !c.seen[s]
	}) {
		return slices.Clone(requested), nil
	}
	all, err := c.allServices(ctx)
	if err != nil {
		return nil, err
	}
	if len(requested) == 0 {
		return slices.Clone(all), nil
	}
	for _, s := range requested {
		if !slices.Contains(all, s) {
			return nil, fmt.Errorf("servicemanager: %w: %q in %s", errUnknownService, s, c.file())
		}
	}
	return slices.Clone(requested), nil
}

// allServices returns every service of the compose file, listed once.
func (c *Compose) allServices(ctx context.Context) ([]string, error) {
	if c.services != nil {
		return c.services, nil
	}
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	out, err := c.run(ctx, "config", "--services")
	if err != nil {
		return nil, fmt.Errorf("servicemanager: list services of %s: %w", c.file(), err)
	}
	c.services = strings.Fields(string(out))
	return c.services, nil
}

// Stop runs `stop -t <timeout in seconds, rounded up> <services>` and waits until none of
// them is running. It returns false when they still run after timeout.
func (c *Compose) Stop(ctx context.Context, services []string, timeout time.Duration) (
	bool, error,
) {
	if len(services) == 0 {
		return true, nil
	}
	deadline := c.clock.Now().Add(timeout)
	cmdCtx, cancel := commandContext(ctx, timeout)
	defer cancel()
	// Compose takes whole seconds; truncating 500ms to 0 would kill the containers at once.
	seconds := strconv.Itoa(int(math.Ceil(timeout.Seconds())))
	args := append([]string{"stop", "-t", seconds}, services...)
	// Compose reports this on stderr, which the runner's error carries.
	if _, err := c.run(cmdCtx, args...); err != nil &&
		!strings.Contains(strings.ToLower(err.Error()), "no containers to stop") {
		return false, fmt.Errorf("servicemanager: stop %s: %w", c.file(), err)
	}
	return waitUntil(ctx, c.clock, deadline, func(ctx context.Context) (bool, error) {
		running, err := c.running(ctx)
		return err == nil && !anyOf(services, running), err
	})
}

// Start runs `start <services>` and waits until all of them are running at two polls in a
// row (see confirmed). It returns false when some are not running after timeout, and an
// error wrapping errStartFailed as soon as one has exited (or is dead) without a running
// container: waiting would not bring it back. `start` (unlike `up`) restarts the stopped
// containers as they were and never recreates them from a changed compose file.
func (c *Compose) Start(ctx context.Context, services []string, timeout time.Duration) (
	bool, error,
) {
	if len(services) == 0 {
		return true, nil
	}
	deadline := c.clock.Now().Add(timeout)
	cmdCtx, cancel := commandContext(ctx, timeout)
	defer cancel()
	if _, err := c.run(cmdCtx, append([]string{"start"}, services...)...); err != nil {
		return false, fmt.Errorf("servicemanager: start %s: %w", c.file(), err)
	}
	stable := confirmed()
	return waitUntil(ctx, c.clock, deadline, func(ctx context.Context) (bool, error) {
		states, err := c.states(ctx)
		if err != nil {
			return false, err
		}
		if failed := exited(services, states); len(failed) > 0 {
			return false, fmt.Errorf("servicemanager: start %s: %w: %s", c.file(),
				errStartFailed, strings.Join(failed, ","))
		}
		return stable(allOf(services, running(states))), nil
	})
}

// running returns the services of the compose project that have a running container
// (not one that is restarting: it is not up yet, and may never be).
func (c *Compose) running(ctx context.Context) (map[string]bool, error) {
	states, err := c.states(ctx)
	if err != nil {
		return nil, err
	}
	return running(states), nil
}

// states returns the state of every service that has a container: "running" when one of
// its containers runs, else the (lowercase) state of the last one listed. Docker lists
// only running containers without --all; podman-compose always lists all, and may not
// know the flag.
func (c *Compose) states(ctx context.Context) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	args := []string{"ps", "--format", "json"}
	if c.s.Binary == "docker" {
		args = append(args, "--all")
	}
	out, err := c.run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("servicemanager: status of %s: %w", c.file(), err)
	}
	entries, err := parseComposePs(out)
	if err != nil {
		return nil, fmt.Errorf("servicemanager: status of %s: %w", c.file(), err)
	}
	states := map[string]string{}
	for _, e := range entries {
		if states[e.service()] != stateRunning {
			states[e.service()] = strings.ToLower(e.State)
		}
	}
	c.see(states)
	return states, nil
}

// see remembers the services that have a container.
func (c *Compose) see(states map[string]string) {
	if c.seen == nil {
		c.seen = map[string]bool{}
	}
	for s := range states {
		c.seen[s] = true
	}
}

// running returns the services whose state is running.
func running(states map[string]string) map[string]bool {
	up := map[string]bool{}
	for s, st := range states {
		up[s] = st == stateRunning
	}
	return up
}

// exited returns the services among names whose container exited or died.
func exited(names []string, states map[string]string) []string {
	return slices.DeleteFunc(slices.Clone(names), func(s string) bool {
		return states[s] != "exited" && states[s] != "dead"
	})
}

func (c *Compose) run(ctx context.Context, args ...string) ([]byte, error) {
	all := []string{"compose"}
	for _, f := range c.s.Files {
		all = append(all, "-f", f)
	}
	if c.s.ProjectName != "" {
		all = append(all, "-p", c.s.ProjectName)
	}
	for _, p := range c.s.Profiles {
		all = append(all, "--profile", p)
	}
	return c.runner.Run(ctx, c.s.Binary, append(all, args...)...)
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

// allOf reports whether every one of names is in set.
func allOf(names []string, set map[string]bool) bool {
	for _, n := range names {
		if !set[n] {
			return false
		}
	}
	return true
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
