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

	"github.com/dombyte/restor/internal/app"
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
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "no command", want: `no command given: use "restor run"`},
		{name: "version 1 call", args: []string{"--config", "x"}, want: "no command given"},
		{name: "unknown flag", args: []string{"run", "--nope"}, want: "unknown flag"},
		{name: "unknown command", args: []string{"backup"}, want: "unknown command"},
		{name: "missing config file", args: []string{"run", "-c", missing}, want: "startup failed"},
		{name: "validate", args: []string{"validate", "-c", missing}, want: "startup failed"},
		{name: "restic", args: []string{"restic", "-c", missing}, want: "startup failed"},
		{name: "snapshots", args: []string{"snapshots", "-c", missing}, want: "startup failed"},
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

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestRun_InvalidConfigLogsEachProblem(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, "version: 2\nconcurrency: -1\njobs:\n  a: {type: systemd}\n")
	var stdout, stderr bytes.Buffer

	assert.Equal(t, 1, run([]string{"run", "--config", path}, &stdout, &stderr))
	out := stderr.String()
	assert.NotContains(t, out, `\n`, "no escaped line breaks")
	assert.Equal(t, 3, strings.Count(out, "startup failed"), out)
	for _, want := range []string{
		"concurrency: must be", "jobs.a.units: required", "jobs.a.sources: required",
	} {
		assert.Contains(t, out, want)
	}
}

func TestRun_UnknownJob(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, "version: 2\njobs: {a: {sources: [/a]}}\n")
	var stdout, stderr bytes.Buffer

	assert.Equal(t, 1, run([]string{"run", "-c", path, "a", "b"}, &stdout, &stderr))
	assert.Contains(t, stderr.String(), "unknown job: b")
}

func TestRun_Validate(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, `version: 2
repository: {url: /repo, prune: true}
defaults:
  keep: {daily: 7, weekly: 4}
jobs:
  web: {type: docker-compose, compose_file: /c.yml, sources: [/w], keep: {last: 2}}
  db: {type: docker-compose, compose_file: /c.yml, sources: [/d], before: [web]}
  etc: {sources: [/etc]}
  tmp: {sources: [/tmp/x], keep: {}}
`)
	var stdout, stderr bytes.Buffer

	assert.Equal(t, 0, run([]string{"validate", "-c", path}, &stdout, &stderr), stderr.String())
	out := stdout.String()
	assert.Contains(t, out, path+" is valid")
	assert.Contains(t, out, "prune:        true")
	assert.Regexp(t, `(?s)JOB.*\n`+
		`db +docker-compose +- +true +false +A +/d\n`+
		`etc +files +- +false +false +A +/etc\n`+
		`tmp +files +- +false +false +- +/tmp/x\n`+
		`web +docker-compose +db +true +false +B +/w\n`, out)
	assert.Regexp(t, `(?s)\nKEEP\n`+
		`  A +daily=7 weekly=4 +\(db, etc\)\n`+
		`  - +never forgotten +\(tmp\)\n`+
		`  B +last=2 +\(web\)\n$`, out)
	assert.NotContains(t, stderr.String(), "job configured", "validate logs warnings only")
}

// TestRun_Restic is not parallel: it sets PATH.
func TestRun_Restic(t *testing.T) {
	dir := t.TempDir()
	// A fake restic on PATH that prints its arguments and the repository.
	script := "#!/bin/sh\necho \"$@\" \"$RESTIC_REPOSITORY\"\nexit 3\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "restic"), []byte(script), 0o700))
	t.Setenv("PATH", dir)
	path := writeConfig(t, "version: 2\nrepository: {url: /repo, limit_upload: 5}\n"+
		"jobs: {a: {sources: [/a]}}\n")
	var stdout, stderr bytes.Buffer

	code := run([]string{"restic", "-c", path, "snapshots", "--tag", "a"}, &stdout, &stderr)

	assert.Equal(t, 3, code, "restic's exit code")
	assert.Equal(t, "--limit-upload=5 snapshots --tag a /repo\n", stdout.String())
	assert.Empty(t, stderr.String())
}

// TestRun_Snapshots is not parallel: it sets PATH.
func TestRun_Snapshots(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\necho \"$@\" \"$RESTIC_REPOSITORY\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "restic"), []byte(script), 0o700))
	t.Setenv("PATH", dir)
	path := writeConfig(t, "version: 2\nrepository: {url: /repo, limit_upload: 5}\n"+
		"jobs: {a: {sources: [/a]}, b: {sources: [/b]}, c: {sources: [/c]}}\n")
	tests := []struct {
		name string
		jobs []string
		want string
	}{
		{name: "all jobs", want: "--tag=a --tag=b --tag=c"},
		{name: "named jobs", jobs: []string{"C", "a"}, want: "--tag=a --tag=c"},
	}
	for _, tt := range tests {
		var stdout, stderr bytes.Buffer
		args := append([]string{"snapshots", "-c", path}, tt.jobs...)

		assert.Equal(t, 0, run(args, &stdout, &stderr), tt.name)
		assert.Equal(t, "--limit-upload=5 snapshots "+tt.want+" /repo\n", stdout.String(),
			tt.name)
		assert.Empty(t, stderr.String(), tt.name)
	}

	var stdout, stderr bytes.Buffer
	assert.Equal(t, 1, run([]string{"snapshots", "-c", path, "x"}, &stdout, &stderr))
	assert.Contains(t, stderr.String(), "unknown job: x")
	assert.Empty(t, stdout.String(), "restic does not run")

	stderr.Reset()
	assert.Equal(t, 1, run([]string{"snapshots", "-c", path, "--all", "a"}, &stdout, &stderr))
	assert.Contains(t, stderr.String(), "name no jobs")
	assert.Empty(t, stdout.String(), "restic does not run")

	assert.Equal(t, 0, run([]string{"snapshots", "-c", path, "--all"}, &stdout, &stderr))
	assert.Equal(t, "--limit-upload=5 snapshots /repo\n", stdout.String(), "no tag filter")

	stdout.Reset()
	repoOnly := writeConfig(t, "version: 2\nrepository: {url: /repo}\n")
	assert.Equal(t, 0, run([]string{"snapshots", "-c", repoOnly}, &stdout, &stderr))
	assert.Equal(t, "snapshots /repo\n", stdout.String(), "without jobs: every snapshot")
}

// TestRun_Restore is not parallel: it sets PATH.
func TestRun_Restore(t *testing.T) {
	dir := t.TempDir()
	// A fake restic: a supported version, then the arguments and the repository; exit 3
	// for a snapshot named ffffffff.
	script := "#!/bin/sh\nif [ \"$1\" = version ]; then echo restic 0.17.1; exit 0; fi\n" +
		"echo \"$@\" \"$RESTIC_REPOSITORY\"\n" +
		"case \"$*\" in *ffffffff*) exit 3;; esac\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "restic"), []byte(script), 0o700))
	t.Setenv("PATH", dir)
	path := writeConfig(t, "version: 2\nrepository: {url: /repo, limit_upload: 5}\n")
	tests := []struct {
		name   string
		args   []string
		code   int
		stdout string
		stderr string
	}{
		{
			name: "dry run by default", args: []string{"1a2b3c4d", "--target", "/tmp/r"},
			stdout: "--limit-upload=5 restore --target=/tmp/r --dry-run --verbose=2 -- " +
				"1a2b3c4d /repo\n",
			stderr: "nothing was written; pass --apply to restore",
		},
		{
			name: "in place", args: []string{
				"1a2b3c4d", "--in-place", "--apply",
				"--include", "/srv/db", "--overwrite", "if-newer", "--verify",
			},
			stdout: "--limit-upload=5 restore --target=/ --include=/srv/db " +
				"--overwrite=if-newer --verify -- 1a2b3c4d /repo\n",
			stderr: "restore completed",
		},
		{
			name: "no target", args: []string{"1a2b3c4d"}, code: 1,
			stderr: "either --target <dir> or --in-place",
		},
		{
			name: "both targets", args: []string{"1a2b3c4d", "--target", "/t", "--in-place"},
			code: 1, stderr: "either --target <dir> or --in-place",
		},
		{
			name: "latest", args: []string{"latest", "--in-place"}, code: 1,
			stderr: "is not a snapshot ID",
		},
		{
			name: "restic's exit code", args: []string{"ffffffff", "--in-place"}, code: 3,
			stdout: "--limit-upload=5 restore --target=/ --dry-run --verbose=2 -- " +
				"ffffffff /repo\n",
			stderr: "restore failed",
		},
	}
	for _, tt := range tests {
		var stdout, stderr bytes.Buffer
		args := append([]string{"restore", "-c", path}, tt.args...)

		assert.Equal(t, tt.code, run(args, &stdout, &stderr), tt.name)
		assert.Equal(t, tt.stdout, stdout.String(), tt.name)
		assert.Contains(t, stderr.String(), tt.stderr, tt.name)
	}
}

// TestRun_UserDefaultConfig is not parallel: it sets XDG_CONFIG_HOME.
func TestRun_UserDefaultConfig(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads " + app.SystemConfig)
	}
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, "restor", "config.yaml")
	var stdout, stderr bytes.Buffer

	assert.Equal(t, 1, run([]string{"validate"}, &stdout, &stderr))
	assert.Contains(t, stderr.String(), path)
	assert.Contains(t, stderr.String(), "users other than root read this file by default")

	stderr.Reset()
	other := filepath.Join(dir, "other.yaml")
	assert.Equal(t, 1, run([]string{"validate", "-c", other}, &stdout, &stderr))
	assert.NotContains(t, stderr.String(), "by default", "no hint for --config")

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte("version: 2\njobs: {a: {sources: [/a]}}\n"),
		0o600))
	assert.Equal(t, 0, run([]string{"validate"}, &stdout, &stderr))
	assert.Contains(t, stdout.String(), path+" is valid")
}

// TestRun_NoHome is not parallel: it sets HOME.
func TestRun_NoHome(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads " + app.SystemConfig)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	var stdout, stderr bytes.Buffer

	assert.Equal(t, 1, run([]string{"validate"}, &stdout, &stderr))
	assert.Contains(t, stderr.String(), "no default configuration file")
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
	log := newLogger(&buf, zerolog.InfoLevel)
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
