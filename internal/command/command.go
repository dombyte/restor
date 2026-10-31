// Package command runs external programs (restic, docker/podman compose, systemctl and
// hooks) with a fixed environment, bound to a context.
package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog"
)

const (
	// waitDelay is how long a cancelled program gets to exit after SIGTERM (restic removes
	// its repository lock) before it is killed.
	waitDelay = 10 * time.Second
	// maxOutputLines is how much of a program's output an error or debug log carries.
	maxOutputLines = 20
	// maxOutput bounds the stdout Run keeps in memory; a long restic run keeps its last
	// part, which holds the summary and the snapshot ID.
	maxOutput = 1 << 20
	// maxStderr bounds the stderr Run keeps for errors and debug logs.
	maxStderr = 64 << 10
)

// ErrEmptyCommand is returned when Run is called without a program name.
var ErrEmptyCommand = errors.New("command: empty command")

// Runner executes programs directly (no shell) with Env as their complete environment.
type Runner struct {
	env       []string
	log       zerolog.Logger
	waitDelay time.Duration
}

// New returns a Runner whose programs get env (KEY=value pairs, later entries win).
func New(env []string, log zerolog.Logger) *Runner {
	return &Runner{env: env, log: log, waitDelay: waitDelay}
}

// Run executes name with args and returns its stdout (the last maxOutput bytes). Stderr
// is kept apart, so warnings there (compose, podman's provider banner) never reach the
// callers that parse the output. A non-zero exit returns the output too, with an error
// that carries the last lines of stderr, or of stdout when stderr is empty. When ctx
// ends, the program's process group gets SIGTERM, the program SIGKILL after waitDelay,
// and what is left of the group SIGKILL when it ended. A program that exits successfully
// while a process it started still holds its output succeeds after waitDelay, with a
// warning.
func (r *Runner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return r.RunEnv(ctx, nil, name, args...)
}

// RunEnv is Run with env (KEY=value pairs) added to the Runner's environment for this
// program only; they win over the Runner's entries.
func (r *Runner) RunEnv(ctx context.Context, env []string, name string, args ...string) (
	[]byte, error,
) {
	if name == "" {
		return nil, ErrEmptyCommand
	}
	r.log.Debug().Str("program", name).Strs("args", args).Msg("running command")

	// Running configured programs (restic, compose, systemctl, hooks) is the purpose here;
	// they run directly, without a shell.
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // see above
	cmd.Env = slices.Concat(r.env, env)
	// Its own process group, so that a cancel also reaches the processes it started (a
	// hook script's children), not only the program itself.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return signalGroup(cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = r.waitDelay
	stdout, stderr := &tailBuffer{max: maxOutput}, &tailBuffer{max: maxStderr}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := r.finish(ctx, cmd, name, cmd.Run())
	out, errOut := stdout.Bytes(), stderr.Bytes()
	if err != nil {
		if len(bytes.TrimSpace(errOut)) == 0 {
			errOut = out // programs that report their failure on stdout (scripts)
		}
		return out, &Error{Program: name, Output: tail(errOut, maxOutputLines), Err: err}
	}
	r.log.Debug().Str("program", name).Str("output", tail(out, maxOutputLines)).
		Str("stderr", tail(errOut, maxOutputLines)).Msg("command finished")
	return out, nil
}

// finish cleans up after cmd ended with err and returns the error of the run.
func (r *Runner) finish(ctx context.Context, cmd *exec.Cmd, name string, err error) error {
	if ctx.Err() != nil && cmd.Process != nil {
		// Kill what is left of a cancelled group: after waitDelay exec only kills the
		// program itself.
		if kerr := signalGroup(cmd.Process.Pid, syscall.SIGKILL); kerr != nil {
			r.log.Warn().Err(kerr).Str("program", name).Msg("cannot kill process group")
		}
	}
	if ctx.Err() == nil && errors.Is(err, exec.ErrWaitDelay) {
		// The program succeeded, but something it started in the background (a hook's
		// "daemon &") still holds its output. That process keeps running, without output.
		r.log.Warn().Str("program", name).Msg("a process the program started still holds " +
			"its output; redirect the output of background processes")
		return nil
	}
	return err
}

// Stdio are the standard streams of an attached program.
type Stdio struct {
	In       io.Reader
	Out, Err io.Writer
}

// Attach runs name with args connected to stdio, for interactive use (restor restic …),
// and returns its error; the exit code is available through errors.As with an
// interface{ ExitCode() int }. It stays in the caller's process group, so a terminal's
// Ctrl-C reaches it directly; a cancelled ctx sends SIGTERM, SIGKILL after waitDelay.
func (r *Runner) Attach(ctx context.Context, stdio Stdio, name string, args ...string) error {
	if name == "" {
		return ErrEmptyCommand
	}
	r.log.Debug().Str("program", name).Strs("args", args).Msg("running attached command")
	// The program is restic with the user's own arguments; no shell is involved.
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // see above
	cmd.Env = r.env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdio.In, stdio.Out, stdio.Err
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = waitDelay
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("command: %s: %w", name, err)
	}
	return nil
}

// signalGroup sends sig to the process group led by pid; a group that has already exited
// is not an error.
func signalGroup(pid int, sig syscall.Signal) error {
	if err := syscall.Kill(-pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("command: signal process group %d: %w", pid, err)
	}
	return nil
}

// tailBuffer is an io.Writer that keeps only the last max bytes written to it.
type tailBuffer struct {
	max int
	buf []byte
}

// Write appends p. The buffer is compacted only when it reaches twice max, so long
// output costs linear time instead of one copy of max bytes per write.
func (b *tailBuffer) Write(p []byte) (int, error) {
	b.buf = append(b.buf, p...)
	if len(b.buf) >= 2*b.max {
		b.buf = append(b.buf[:0], b.buf[len(b.buf)-b.max:]...)
	}
	return len(p), nil
}

// Bytes returns the last max bytes written.
func (b *tailBuffer) Bytes() []byte {
	if over := len(b.buf) - b.max; over > 0 {
		return b.buf[over:]
	}
	return b.buf
}

// Error is a failed program run. The arguments are left out on purpose: they may carry
// repository URLs or other secrets.
type Error struct {
	// Program is the executable that failed.
	Program string
	// Output holds the last lines of the program's stderr, or of its stdout when it wrote
	// nothing to stderr.
	Output string
	// Err is the exec error (exit status, not found, killed by the context).
	Err error
}

func (e *Error) Error() string {
	if e.Output == "" {
		return fmt.Sprintf("command: %s: %v", e.Program, e.Err)
	}
	return fmt.Sprintf("command: %s: %v: %s", e.Program, e.Err, e.Output)
}

// Unwrap returns the exec error.
func (e *Error) Unwrap() error { return e.Err }

// tail returns the last n non-empty lines of out, joined with "; ".
func tail(out []byte, n int) string {
	lines := strings.Split(string(bytes.TrimSpace(out)), "\n")
	kept := make([]string, 0, len(lines))
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" {
			kept = append(kept, l)
		}
	}
	if len(kept) > n {
		kept = kept[len(kept)-n:]
	}
	return strings.Join(kept, "; ")
}
