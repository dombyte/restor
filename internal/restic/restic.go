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

	"github.com/rs/zerolog"

	"github.com/dombyte/restor/internal/util"
)

const (
	// program is the restic executable, looked up in PATH.
	program = "restic"
	// exitIncomplete is restic's exit code for a snapshot that was saved, but without
	// some source files that could not be read.
	exitIncomplete = 3
)

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

// IncompleteError is returned by Backup when restic saved the snapshot, but could not
// read some source files (exit code 3). Backup returns the snapshot ID with it.
type IncompleteError struct {
	// Tag is the project tag of the snapshot.
	Tag string
	// Err is the runner's error, with restic's last output lines.
	Err error
}

func (e *IncompleteError) Error() string {
	return fmt.Sprintf("restic: backup %s: snapshot incomplete, some source files could "+
		"not be read: %v", e.Tag, e.Err)
}

// Unwrap returns the runner's error.
func (e *IncompleteError) Unwrap() error { return e.Err }

// Incomplete reports true; callers match it with errors.As without importing restic.
func (e *IncompleteError) Incomplete() bool { return true }

// Deps are the dependencies of a Client.
type Deps struct {
	// Runner executes restic with the repository environment.
	Runner Runner
	// Log reports the summary of each backup.
	Log zerolog.Logger
}

// Client runs restic commands against one repository.
type Client struct {
	runner Runner
	log    zerolog.Logger
}

// New returns a Client.
func New(d Deps) (*Client, error) {
	if err := util.RequireAll(ErrMissingDependency,
		util.Requirement{Name: "Runner", OK: d.Runner != nil},
	); err != nil {
		return nil, err
	}
	return &Client{runner: d.Runner, log: d.Log}, nil
}

// Backup runs `restic backup [opts] --tag <tag> <sources>` and returns the ID of the new
// snapshot, or "" when the output does not name one. An incomplete snapshot returns its
// ID together with an *IncompleteError.
func (c *Client) Backup(ctx context.Context, tag string, sources, opts []string) (string, error) {
	if len(sources) == 0 {
		return "", ErrNoSources
	}
	args := append([]string{"backup"}, opts...)
	args = append(args, "--tag", tag)
	args = append(args, sources...)
	out, err := c.runner.Run(ctx, program, args...)
	if err != nil && !isExit(err, exitIncomplete) {
		return "", fmt.Errorf("restic: backup %s: %w", tag, err)
	}
	c.logSummary(tag, string(out))
	if err != nil {
		return parseSnapshotID(string(out)), &IncompleteError{Tag: tag, Err: err}
	}
	return parseSnapshotID(string(out)), nil
}

// logSummary logs restic's summary lines (files, dirs, added data, totals).
func (c *Client) logSummary(tag, output string) {
	fields := map[string]any{}
	prefixes := []struct{ prefix, name string }{
		{"Files:", "files"},
		{"Dirs:", "dirs"},
		{"Added to the repository:", "added"},
		{"processed ", "processed"},
	}
	for line := range strings.SplitSeq(output, "\n") {
		for _, p := range prefixes {
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), p.prefix); ok {
				fields[p.name] = strings.Join(strings.Fields(v), " ")
			}
		}
	}
	if len(fields) > 0 {
		c.log.Info().Str("tag", tag).Fields(fields).Msg("backup summary")
	}
}

// isExit reports whether err carries the process exit code code.
func isExit(err error, code int) bool {
	var exit interface{ ExitCode() int }
	return errors.As(err, &exit) && exit.ExitCode() == code
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
