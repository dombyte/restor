// Package restic wraps the restic CLI: backup, forget, prune and unlock. The repository
// and its credentials come from the runner's environment (RESTIC_REPOSITORY,
// RESTIC_PASSWORD_FILE, …), so they never appear in arguments or logs.
package restic

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/dombyte/restor/internal/util"
)

// program is the restic executable, looked up in PATH.
const program = "restic"

var (
	// ErrMissingDependency is returned by New for a nil dependency.
	ErrMissingDependency = errors.New("restic: missing dependency")
	// ErrNoSources is returned by Backup when there is nothing to back up.
	ErrNoSources = errors.New("restic: no sources")
)

// Runner executes a program and returns its combined output.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Deps are the dependencies of a Client.
type Deps struct {
	// Runner executes restic with the repository environment.
	Runner Runner
}

// Client runs restic commands against one repository.
type Client struct {
	runner Runner
}

// New returns a Client.
func New(d Deps) (*Client, error) {
	if err := util.RequireAll(ErrMissingDependency,
		util.Requirement{Name: "Runner", OK: d.Runner != nil},
	); err != nil {
		return nil, err
	}
	return &Client{runner: d.Runner}, nil
}

// Backup runs `restic backup [opts] --tag <tag> <sources>` and returns the ID of the new
// snapshot, or "" when the output does not name one.
func (c *Client) Backup(ctx context.Context, tag string, sources, opts []string) (string, error) {
	if len(sources) == 0 {
		return "", ErrNoSources
	}
	args := append([]string{"backup"}, opts...)
	args = append(args, "--tag", tag)
	args = append(args, sources...)
	out, err := c.runner.Run(ctx, program, args...)
	if err != nil {
		return "", fmt.Errorf("restic: backup %s: %w", tag, err)
	}
	return parseSnapshotID(string(out)), nil
}

// Forget runs `restic forget [opts] --tag <tag> <policy>`; policy is split on whitespace
// (e.g. "--keep-daily 7 --keep-weekly 4").
func (c *Client) Forget(ctx context.Context, tag, policy string, opts []string) error {
	args := append([]string{"forget"}, opts...)
	args = append(args, "--tag", tag)
	args = append(args, strings.Fields(policy)...)
	if _, err := c.runner.Run(ctx, program, args...); err != nil {
		return fmt.Errorf("restic: forget %s: %w", tag, err)
	}
	return nil
}

// Prune runs `restic [opts] prune` for the whole repository.
func (c *Client) Prune(ctx context.Context, opts []string) error {
	args := slices.Concat(opts, []string{"prune"})
	if _, err := c.runner.Run(ctx, program, args...); err != nil {
		return fmt.Errorf("restic: prune: %w", err)
	}
	return nil
}

// Unlock runs `restic [opts] unlock` to remove stale repository locks.
func (c *Client) Unlock(ctx context.Context, opts []string) error {
	args := slices.Concat(opts, []string{"unlock"})
	if _, err := c.runner.Run(ctx, program, args...); err != nil {
		return fmt.Errorf("restic: unlock: %w", err)
	}
	return nil
}

// parseSnapshotID returns the ID from restic's "snapshot <id> saved" line, or "".
func parseSnapshotID(output string) string {
	for line := range strings.SplitSeq(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[0] == "snapshot" && fields[2] == "saved" {
			return fields[1]
		}
	}
	return ""
}
