// Package restic wraps the restic CLI: backup, forget, prune and unlock. The repository
// and its credentials come from the runner's environment (RESTIC_REPOSITORY,
// RESTIC_PASSWORD_FILE, …), so they never appear in arguments or logs.
package restic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/dombyte/restor/internal/util"
)

const (
	// program is the restic executable, looked up in PATH.
	program = "restic"
	// exitIncomplete is restic's exit code for a snapshot that was saved, but without
	// some source files that could not be read.
	exitIncomplete = 3
	// shortIDLength is the length of the snapshot IDs restic prints.
	shortIDLength = 8
	// Exit codes of restic ≥ 0.17 for common setup problems.
	exitNoRepository  = 10
	exitLocked        = 11
	exitWrongPassword = 12
)

var (
	// ErrMissingDependency is returned by New for a nil dependency.
	ErrMissingDependency = errors.New("restic: missing dependency")
	// ErrNoSources is returned by Backup when there is nothing to back up.
	ErrNoSources = errors.New("restic: no sources")

	errNoRepository = errors.New("repository does not exist (create it with restic init)")
	errLocked       = errors.New("repository is locked by another process " +
		"(add --retry-lock=<duration> to the options to wait for it)")
	errWrongPassword = errors.New("wrong password or no key found")
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

// Backup runs `restic backup --json [opts] --tag <tag> <sources>` and returns the short ID
// of the new snapshot, or "" when restic reports none (also for --dry-run). An incomplete
// snapshot returns its ID together with an *IncompleteError.
func (c *Client) Backup(ctx context.Context, tag string, sources, opts []string) (string, error) {
	if len(sources) == 0 {
		return "", ErrNoSources
	}
	args := slices.Concat([]string{"backup", "--json"}, opts, []string{"--tag", tag}, sources)
	out, err := c.runner.Run(ctx, program, args...)
	if err != nil && !isExit(err, exitIncomplete) {
		return "", failed("backup "+tag, err)
	}
	sum, found := parseSummary(out)
	if found {
		c.logSummary(tag, sum)
	}
	if err != nil {
		return sum.snapshotID(), &IncompleteError{Tag: tag, Err: err}
	}
	return sum.snapshotID(), nil
}

// summary is the "summary" message of `restic backup --json`.
type summary struct {
	MessageType     string  `json:"message_type"`
	FilesNew        int64   `json:"files_new"`
	FilesChanged    int64   `json:"files_changed"`
	FilesUnmodified int64   `json:"files_unmodified"`
	DirsNew         int64   `json:"dirs_new"`
	DirsChanged     int64   `json:"dirs_changed"`
	DirsUnmodified  int64   `json:"dirs_unmodified"`
	DataAdded       int64   `json:"data_added"`
	FilesProcessed  int64   `json:"total_files_processed"`
	BytesProcessed  int64   `json:"total_bytes_processed"`
	DurationSeconds float64 `json:"total_duration"`
	SnapshotID      string  `json:"snapshot_id"`
	DryRun          bool    `json:"dry_run"`
}

// snapshotID returns the short snapshot ID (as restic prints it), or "" when no snapshot
// was saved: a dry run reports the ID it would have used.
func (s summary) snapshotID() string {
	if s.DryRun {
		return ""
	}
	return s.SnapshotID[:min(len(s.SnapshotID), shortIDLength)]
}

// parseSummary returns the last summary message of restic's JSON lines. Other lines
// (status, errors, plain text from stderr) are skipped.
func parseSummary(out []byte) (summary, bool) {
	var last summary
	found := false
	for line := range bytes.SplitSeq(out, []byte("\n")) {
		var s summary
		if json.Unmarshal(bytes.TrimSpace(line), &s) == nil && s.MessageType == "summary" {
			last, found = s, true
		}
	}
	return last, found
}

// logSummary logs what the backup processed and added.
func (c *Client) logSummary(tag string, s summary) {
	c.log.Info().Str("tag", tag).
		Int64("files_new", s.FilesNew).Int64("files_changed", s.FilesChanged).
		Int64("files_unmodified", s.FilesUnmodified).Int64("dirs_new", s.DirsNew).
		Int64("dirs_changed", s.DirsChanged).Int64("dirs_unmodified", s.DirsUnmodified).
		Int64("bytes_added", s.DataAdded).Int64("files_processed", s.FilesProcessed).
		Int64("bytes_processed", s.BytesProcessed).
		Dur("duration", time.Duration(s.DurationSeconds*float64(time.Second))).
		Msg("backup summary")
}

// failed wraps the error of a restic run, naming the cause of the exit codes restic uses
// for setup problems, which its JSON output would otherwise bury.
func failed(op string, err error) error {
	var exit interface{ ExitCode() int }
	if !errors.As(err, &exit) {
		return fmt.Errorf("restic: %s: %w", op, err)
	}
	var cause error
	switch exit.ExitCode() {
	case exitNoRepository:
		cause = errNoRepository
	case exitLocked:
		cause = errLocked
	case exitWrongPassword:
		cause = errWrongPassword
	default:
		return fmt.Errorf("restic: %s: %w", op, err)
	}
	return fmt.Errorf("restic: %s: %w: %w", op, cause, err)
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
		return failed("forget "+tag, err)
	}
	return nil
}

// Prune runs `restic [opts] prune` for the whole repository.
func (c *Client) Prune(ctx context.Context, opts []string) error {
	args := slices.Concat(opts, []string{"prune"})
	if _, err := c.runner.Run(ctx, program, args...); err != nil {
		return failed("prune", err)
	}
	return nil
}

// Unlock runs `restic [opts] unlock` to remove stale repository locks.
func (c *Client) Unlock(ctx context.Context, opts []string) error {
	args := slices.Concat(opts, []string{"unlock"})
	if _, err := c.runner.Run(ctx, program, args...); err != nil {
		return failed("unlock", err)
	}
	return nil
}
