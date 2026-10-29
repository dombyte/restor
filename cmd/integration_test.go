//go:build integration

package main

// TestIntegration_CLI drives the restor commands end to end, as a user would, against
// the restic on PATH and a local repository: run, validate, snapshots (with jobs, --all,
// without jobs), restore (dry run, --target, --in-place, --target /) and the restic
// passthrough. Files-only jobs, so it needs no service manager and no root:
//
//	go test -tags integration -race ./cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cli runs restor with args and returns its exit code, stdout and stderr.
func cli(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// e2e is a local repository with an env file and the configurations that use it.
type e2e struct {
	dir, src string
}

func newE2E(t *testing.T) *e2e {
	t.Helper()
	dir := t.TempDir()
	e := &e2e{dir: dir, src: filepath.Join(dir, "src")}
	e.write(t, "restic.env", "RESTIC_PASSWORD='pa$word #1'\n"+
		"RESTIC_CACHE_DIR="+filepath.Join(dir, "cache")+"\n")
	e.write(t, "src/a.txt", "a")
	e.write(t, "src/sub/b.txt", "b")
	init := exec.Command("restic", "init")
	init.Env = append(os.Environ(), "RESTIC_REPOSITORY="+e.repo(),
		"RESTIC_PASSWORD=pa$word #1", "RESTIC_CACHE_DIR="+filepath.Join(dir, "cache"))
	out, err := init.CombinedOutput()
	require.NoError(t, err, string(out))
	return e
}

func (e *e2e) repo() string { return filepath.Join(e.dir, "repo") }

func (e *e2e) write(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(e.dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

// config writes a configuration with the repository and jobs (YAML below jobs:, empty
// for none) and returns its path.
func (e *e2e) config(t *testing.T, name, jobs string) string {
	t.Helper()
	content := "version: 2\nrepository:\n  url: " + e.repo() + "\n  env_file: restic.env\n"
	if jobs != "" {
		content += "jobs:\n" + jobs
	}
	return e.write(t, name, content)
}

// snapshotIDs returns the short IDs of the snapshots tagged tag.
func (e *e2e) snapshotIDs(t *testing.T, config, tag string) []string {
	t.Helper()
	code, out, stderr := cli("restic", "-c", config, "snapshots", "--json", "--tag", tag)
	require.Equal(t, 0, code, stderr)
	var list []struct {
		ShortID string `json:"short_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &list))
	ids := make([]string, 0, len(list))
	for _, s := range list {
		ids = append(ids, s.ShortID)
	}
	return ids
}

func (e *e2e) read(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(content)
}

// TestIntegration_CLI is not parallel: run installs a signal handler per call.
func TestIntegration_CLI(t *testing.T) {
	e := newE2E(t)
	cfg := e.config(t, "config.yaml", "  app: {sources: ["+e.src+"], keep: {last: 3}}\n")
	old := e.config(t, "old.yaml", "  old: {sources: ["+e.src+"]}\n")
	repoOnly := e.config(t, "repo.yaml", "")

	code, out, stderr := cli("validate", "-c", cfg)
	require.Equal(t, 0, code, stderr)
	assert.Contains(t, out, "is valid")

	// Backups: app is configured, old stands for a job that was renamed or removed.
	code, _, stderr = cli("run", "-c", cfg)
	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stderr, "backup run completed")
	code, _, stderr = cli("run", "-c", old)
	require.Equal(t, 0, code, stderr)
	appIDs, oldIDs := e.snapshotIDs(t, cfg, "app"), e.snapshotIDs(t, cfg, "old")
	require.Len(t, appIDs, 1)
	require.Len(t, oldIDs, 1)
	app, removed := appIDs[0], oldIDs[0]

	t.Run("snapshots", func(t *testing.T) {
		code, out, stderr := cli("snapshots", "-c", cfg)
		require.Equal(t, 0, code, stderr)
		assert.Contains(t, out, app)
		assert.NotContains(t, out, removed, "only the snapshots of the configured jobs")

		code, out, stderr = cli("snapshots", "-c", cfg, "--all")
		require.Equal(t, 0, code, stderr)
		assert.Contains(t, out, app)
		assert.Contains(t, out, removed, "--all: also those of removed jobs")

		code, out, stderr = cli("snapshots", "-c", repoOnly)
		require.Equal(t, 0, code, stderr)
		assert.Contains(t, out, removed, "without jobs: every snapshot")

		code, _, stderr = cli("run", "-c", repoOnly)
		assert.Equal(t, 1, code)
		assert.Contains(t, stderr, "no jobs configured")
	})

	t.Run("restore", func(t *testing.T) {
		target := filepath.Join(e.dir, "target")
		a, b := filepath.Join(e.src, "a.txt"), filepath.Join(e.src, "sub", "b.txt")

		code, out, stderr := cli("restore", "-c", repoOnly, app, "--target", target)
		require.Equal(t, 0, code, stderr)
		assert.Contains(t, out+stderr, a, "the dry run lists the files")
		assert.Contains(t, stderr, "nothing was written")
		assert.NoDirExists(t, target, "a dry run writes nothing")

		code, _, stderr = cli("restore", "-c", repoOnly, app, "--target", target,
			"--include", filepath.Join(e.src, "sub"), "--apply")
		require.Equal(t, 0, code, stderr)
		assert.Equal(t, "b", e.read(t, filepath.Join(target, b)))
		assert.NoFileExists(t, filepath.Join(target, a), "not included")

		require.NoError(t, os.WriteFile(a, []byte("changed"), 0o600))
		require.NoError(t, os.Remove(b))
		code, _, stderr = cli("restore", "-c", cfg, app, "--in-place", "--include", e.src,
			"--verify", "--apply")
		require.Equal(t, 0, code, stderr)
		assert.Contains(t, stderr, "restore completed")
		assert.Equal(t, "a", e.read(t, a), "restored in place")
		assert.Equal(t, "b", e.read(t, b), "deleted file restored")

		require.NoError(t, os.WriteFile(a, []byte("changed"), 0o600))
		code, _, stderr = cli("restore", "-c", cfg, removed, "--target", "/",
			"--include", a, "--apply")
		require.Equal(t, 0, code, stderr)
		assert.Equal(t, "a", e.read(t, a), "--target / is --in-place")

		code, _, stderr = cli("restore", "-c", cfg, "latest", "--in-place", "--apply")
		assert.Equal(t, 1, code)
		assert.Contains(t, stderr, "is not a snapshot ID")
	})
}
