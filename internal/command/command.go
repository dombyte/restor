// Package command runs external programs (restic, docker/podman compose, systemctl and
// hooks) with a fixed environment, bound to a context.
package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
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
	// maxOutput bounds the output Run keeps in memory; a long restic run keeps its last
	// part, which holds the summary and the snapshot ID.
	maxOutput = 1 << 20
)

// ErrEmptyCommand is returned when Run is called without a program name.
var ErrEmptyCommand = errors.New("command: empty command")

// Runner executes programs directly (no shell) with Env as their complete environment.
type Runner struct {
	env []string
	log zerolog.Logger
}

// New returns a Runner whose programs get env (KEY=value pairs, later entries win).
func New(env []string, log zerolog.Logger) *Runner {
	return &Runner{env: env, log: log}
}

// Run executes name with args and returns its combined stdout and stderr (the last
// maxOutput bytes). A non-zero exit returns the output too, with an error that carries
// the last lines of it. When ctx ends, the program gets SIGTERM, then SIGKILL after
// waitDelay.
func (r *Runner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name == "" {
		return nil, ErrEmptyCommand
	}
	r.log.Debug().Str("program", name).Strs("args", args).Msg("running command")

	// Running configured programs (restic, compose, systemctl, hooks) is the purpose here;
	// they run directly, without a shell.
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // see above
	cmd.Env = r.env
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = waitDelay
	buf := &tailBuffer{max: maxOutput}
	cmd.Stdout, cmd.Stderr = buf, buf
	err := cmd.Run()
	out := buf.Bytes()
	if err != nil {
		return out, &Error{Program: name, Output: tail(out, maxOutputLines), Err: err}
	}
	r.log.Debug().Str("program", name).Str("output", tail(out, maxOutputLines)).
		Msg("command finished")
	return out, nil
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
	// Output holds the last lines of the program's combined output.
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
