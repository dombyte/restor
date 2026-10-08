package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

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

func TestRun_EmptyConfig(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("global: {restic_repo: /repo}\n"), 0o600))
	var stdout, stderr bytes.Buffer

	assert.Equal(t, 0, run([]string{"--config", path, "--debug"}, &stdout, &stderr))
	assert.Contains(t, stderr.String(), "no projects configured")
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
	require.ErrorIs(t, waitForRun(context.Background(), done, zerolog.Nop()), assert.AnError)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done = make(chan error, 1)
	done <- context.Canceled // the run ends after restarting services
	require.ErrorIs(t, waitForRun(ctx, done, zerolog.Nop()), context.Canceled)
}
