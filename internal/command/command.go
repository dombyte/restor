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
	"time"

	"github.com/rs/zerolog"
)

// waitDelay bounds how long Run waits for a cancelled program's output pipes to close
// after the context killed it.
const waitDelay = 5 * time.Second

// maxOutputLines is how much of a failed program's output an error carries.
const maxOutputLines = 20

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

// Run executes name with args and returns its combined stdout and stderr. A non-zero
// exit returns the output too, with an error that carries the last lines of it.
func (r *Runner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name == "" {
		return nil, ErrEmptyCommand
	}
	r.log.Debug().Str("program", name).Strs("args", args).Msg("running command")

	// Running configured programs (restic, compose, systemctl, hooks) is the purpose here;
	// they run directly, without a shell.
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // see above
	cmd.Env = r.env
	cmd.WaitDelay = waitDelay
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, &Error{Program: name, Output: tail(out, maxOutputLines), Err: err}
	}
	return out, nil
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
