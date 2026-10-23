package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_Version(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"version"}, {"--version"}, {"-v"}} {
		var stdout, stderr bytes.Buffer
		assert.Equal(t, 0, run(args, &stdout, &stderr), args)
		assert.Equal(t, buildInfo()+"\n", stdout.String(), args)
	}
}

func TestRun_Errors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing config flag", want: `required flag "config" not set`},
		{name: "unknown flag", args: []string{"--nope"}, want: "unknown flag"},
		{
			name: "unexpected argument", args: []string{"--config", "x", "extra"},
			want: "unknown command",
		},
		{
			name: "missing config file",
			args: []string{"--config", filepath.Join(t.TempDir(), "missing.yaml")},
			want: "startup failed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			assert.Equal(t, 1, run(tt.args, &stdout, &stderr))
			assert.Contains(t, stderr.String(), tt.want)
		})
	}
}

func TestRun_InvalidConfigLogsEachProblem(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path,
		[]byte("version: 2\nconcurrency: -1\njobs:\n  a: {type: systemd}\n"), 0o600))
	var stdout, stderr bytes.Buffer

	assert.Equal(t, 1, run([]string{"--config", path}, &stdout, &stderr))
	out := stderr.String()
	assert.NotContains(t, out, `\n`, "no escaped line breaks")
	assert.Equal(t, 3, strings.Count(out, "startup failed"), out)
	for _, want := range []string{
		"concurrency: must be", "jobs.a.units: required", "jobs.a.sources: required",
	} {
		assert.Contains(t, out, want)
	}
}

func TestJoined(t *testing.T) {
	t.Parallel()
	a, b, c := errors.New("a"), errors.New("b"), errors.New("c")
	nested := fmt.Errorf("config: %w", errors.Join(a, errors.Join(b, c)))

	assert.Equal(t, []error{a, b, c}, joined(nested))
	assert.Equal(t, []error{a}, joined(a))
}

func TestNewLogger_NoColorWithoutTerminal(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := newLogger(&buf, false)
	log.Info().Str("k", "v").Msg("hello")
	assert.NotContains(t, buf.String(), "\x1b[")
	assert.Contains(t, buf.String(), "hello k=v")

	f, err := os.Create(filepath.Join(t.TempDir(), "log"))
	require.NoError(t, err)
	defer f.Close()
	assert.False(t, isTerminal(f))
}

func TestWaitForRun(t *testing.T) {
	t.Parallel()
	done := make(chan error, 1)
	done <- assert.AnError
	require.ErrorIs(t, waitForRun(context.Background(), done, time.Hour, zerolog.Nop()),
		assert.AnError)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done = make(chan error, 1)
	done <- context.Canceled // the run ends after restarting services
	require.ErrorIs(t, waitForRun(ctx, done, time.Hour, zerolog.Nop()), context.Canceled)

	// The run does not end after the cancel: give up after the timeout (0: at once).
	require.ErrorIs(t, waitForRun(ctx, make(chan error), 0, zerolog.Nop()),
		errShutdownDeadline)
}
