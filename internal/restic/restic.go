// Package restic wraps the restic CLI: backup, forget, prune, unlock and version. The
// repository and its credentials come from the runner's environment (RESTIC_REPOSITORY,
// RESTIC_PASSWORD_FILE, …), so they never appear in arguments or logs. The restic flags
// are built here from typed settings (Repository, BackupFlags, Keep, PruneFlags); no
// caller passes raw restic arguments.
package restic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
		"(set repository.retry_lock to wait for it)")
	errWrongPassword = errors.New("wrong password or no key found")
)

// Runner executes a program and returns its stdout; its error carries the program's
// stderr.
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

// UnchangedError is returned by Backup when restic skipped the snapshot because nothing
// changed since the last one (--skip-if-unchanged): the latest snapshot is still current.
type UnchangedError struct {
	// Tag is the job tag of the skipped snapshot.
	Tag string
}

func (e *UnchangedError) Error() string {
	return "restic: backup " + e.Tag + ": nothing changed, no snapshot created"
}

// Unchanged reports true; callers match it with errors.As without importing restic.
func (e *UnchangedError) Unchanged() bool { return true }

// Deps are the dependencies of a Client.
type Deps struct {
	// Runner executes restic with the repository environment.
	Runner Runner
	// Log reports the summary of each backup.
	Log zerolog.Logger
}

// Settings configure a Client.
type Settings struct {
	// GlobalArgs come before the command of every restic call (Repository.Args).
	GlobalArgs []string
	// PruneArgs are the options of prune (PruneFlags.Args).
	PruneArgs []string
}

// Client runs restic commands against one repository.
type Client struct {
	s      Settings
	runner Runner
	log    zerolog.Logger
}

// New returns a Client.
func New(s Settings, d Deps) (*Client, error) {
	if err := util.RequireAll(ErrMissingDependency,
		util.Requirement{Name: "Runner", OK: d.Runner != nil},
	); err != nil {
		return nil, err
	}
	return &Client{s: s, runner: d.Runner, log: d.Log}, nil
}

// run runs restic with the global arguments, then command and args.
func (c *Client) run(ctx context.Context, command string, args ...string) ([]byte, error) {
	return c.runner.Run(ctx, program, slices.Concat(c.s.GlobalArgs, []string{command}, args)...)
}

// Backup runs `restic backup --json --group-by=host,tags [args] --tag=<tag> -- <sources>`
// and returns the short ID of the new snapshot, or "" when restic reports none. args come
// from BackupFlags.Args. An incomplete snapshot returns its ID together with an
// *IncompleteError, a snapshot that --skip-if-unchanged skipped an *UnchangedError.
func (c *Client) Backup(ctx context.Context, tag string, sources, args []string) (
	string, error,
) {
	if len(sources) == 0 {
		return "", ErrNoSources
	}
	// The parent snapshot comes from the job's own snapshots (its tags include the job
	// name): by restic's default (host,paths) a job with the same sources as another would
	// compare against that job's snapshot, and --skip-if-unchanged would skip its own.
	out, err := c.run(ctx, "backup", slices.Concat([]string{"--json", "--group-by=host,tags"},
		args, []string{"--tag=" + tag, "--"}, sources)...)
	if err != nil && !isExit(err, exitIncomplete) {
		return "", failed("backup "+tag, err)
	}
	sum, found := parseSummary(out)
	if found {
		c.logSummary(tag, sum)
	}
	return backupResult(tag, sum, found, err)
}

// backupResult returns the snapshot ID of a backup that ended with err (nil or exit code
// 3) and the summary sum, if found.
func backupResult(tag string, sum summary, found bool, err error) (string, error) {
	switch {
	case err != nil:
		return sum.snapshotID(), &IncompleteError{Tag: tag, Err: readable(err)}
	case found && !sum.DryRun && sum.SnapshotID == "":
		// Only a skipped snapshot leaves the ID out of a real summary.
		return "", &UnchangedError{Tag: tag}
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
// (status messages, plain text) are skipped.
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
		Stringer("duration", seconds(s.DurationSeconds)).
		Msg("backup summary")
}

// seconds converts restic's float seconds to a duration that logs as e.g. "1m2.345s".
func seconds(s float64) time.Duration {
	return time.Duration(s * float64(time.Second)).Round(time.Millisecond)
}

// failed wraps the error of a restic run, naming the cause of the exit codes restic uses
// for setup problems, which its JSON output would otherwise bury, and with restic's JSON
// messages replaced by their text.
func failed(op string, err error) error {
	err = readable(err)
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

// Forget runs `restic forget --group-by=host --tag=<tag> [args]`; args come from
// Keep.Args. The tag selects the job's snapshots and grouping by host alone keeps them in
// one group per host: restic's default (host,paths) would leave the snapshots of
// changed sources in a group of their own that is never forgotten.
func (c *Client) Forget(ctx context.Context, tag string, args []string) error {
	all := slices.Concat([]string{"--group-by=host", "--tag=" + tag}, args)
	if _, err := c.run(ctx, "forget", all...); err != nil {
		return failed("forget "+tag, err)
	}
	return nil
}

// Prune runs `restic prune [prune args]` for the whole repository.
func (c *Client) Prune(ctx context.Context) error {
	if _, err := c.run(ctx, "prune", c.s.PruneArgs...); err != nil {
		return failed("prune", err)
	}
	return nil
}

// Unlock runs `restic unlock` to remove stale repository locks.
func (c *Client) Unlock(ctx context.Context) error {
	if _, err := c.run(ctx, "unlock"); err != nil {
		return failed("unlock", err)
	}
	return nil
}

// Version runs `restic version` and returns the version it reports.
func (c *Client) Version(ctx context.Context) (Version, error) {
	out, err := c.runner.Run(ctx, program, "version")
	if err != nil {
		return Version{}, failed("version", err)
	}
	return ParseVersion(string(out))
}
