package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dombyte/restor/internal/config"
	"github.com/dombyte/restor/internal/restic"
	"github.com/dombyte/restor/internal/servicemanager"
	"github.com/dombyte/restor/internal/util/clocktest"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func options(path string) Options {
	return Options{
		ConfigPath: path, Getenv: func(string) string { return "" },
		Environ: []string{"PATH=/usr/bin"}, Log: zerolog.Nop(),
	}
}

func TestNew_WiresAllJobTypes(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, `
version: 2
repository: {url: /repo, prune: true}
jobs:
  docker: {type: docker-compose, compose_file: /d.yml, sources: [/d]}
  podman: {type: podman-compose, compose_file: /p.yml, sources: [/p]}
  units: {type: systemd, units: [a.service], sources: [/u]}
  files: {sources: [/f]}
`)
	a, err := New(options(path))
	require.NoError(t, err)
	assert.NotNil(t, a.manager)
}

func TestNew_Errors(t *testing.T) {
	t.Parallel()
	_, err := New(options(filepath.Join(t.TempDir(), "missing.yaml")))
	require.Error(t, err)

	_, err = New(options(writeConfig(t, "version: 2\njobs:\n  x: {type: nope}\n")))
	require.ErrorIs(t, err, config.ErrInvalid)

	o := options(writeConfig(t, "version: 2\njobs:\n  x: {sources: [/x]}\n"))
	o.Jobs = []string{"y"}
	_, err = New(o)
	require.ErrorIs(t, err, config.ErrUnknownJob)
}

func TestLockDir_UsesRuntimeDir(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("root always uses /run/restor")
	}
	getenv := func(k string) string {
		if k == "XDG_RUNTIME_DIR" {
			return "/run/user/1000"
		}
		return ""
	}
	assert.Equal(t, "/run/user/1000/restor", lockDir(getenv))
}

func TestCreateServiceManager(t *testing.T) {
	t.Parallel()
	d := servicemanager.Deps{
		Runner: nopRunner{}, Clock: clocktest.New(time.Unix(0, 0)),
	}
	tests := []struct {
		job      config.Job
		lockKeys []string
		wantErr  error
	}{
		{
			job:      config.Job{Type: config.TypeDockerCompose, ComposeFile: "/d"},
			lockKeys: []string{"/d"},
		},
		{
			job:      config.Job{Type: config.TypePodmanCompose, ComposeFile: "/p"},
			lockKeys: []string{"/p"},
		},
		{job: config.Job{
			Type: config.TypeSystemd, Units: []string{"b", "a"}, Scope: config.ScopeUser,
		}, lockKeys: []string{"a", "b"}},
		{
			job:      config.Job{Name: "etc", Type: config.TypeFiles},
			lockKeys: []string{"files-etc"},
		},
		{job: config.Job{Type: "nope"}, wantErr: ErrUnknownType},
		{
			job:     config.Job{Type: config.TypeDockerCompose},
			wantErr: servicemanager.ErrMissingDependency,
		},
	}
	for _, tt := range tests {
		t.Run(tt.job.Type, func(t *testing.T) {
			t.Parallel()
			s, err := CreateServiceManager(tt.job, d)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.lockKeys, s.LockKeys())
		})
	}
}

type nopRunner struct{}

func (nopRunner) Run(context.Context, string, ...string) ([]byte, error) { return nil, nil }

func TestJobSettings(t *testing.T) {
	t.Parallel()
	j := config.Job{
		Name: "web", Type: config.TypeSystemd, Services: []string{"s"}, Sources: []string{"/x"},
		Stop: true, StopTimeout: time.Second, StartTimeout: 2 * time.Second,
		Keep: config.Keep{Last: 1, Tags: []string{"k"}}, Exclude: []string{"*.tmp"},
		OneFileSystem: true, Tags: []string{"prod"}, Pre: "pre", Post: "post",
		OnFailure: "fail", After: []string{"db"}, BackupTimeout: time.Hour,
	}
	s := jobSettings(j)
	assert.Equal(t, "web", s.Name)
	assert.Equal(t, []string{"s"}, s.Services)
	assert.Equal(t, []string{"/x"}, s.Sources)
	assert.True(t, s.StopServices)
	assert.Equal(t, time.Second, s.StopTimeout)
	assert.Equal(t, 2*time.Second, s.StartTimeout)
	assert.Equal(t, []string{"--exclude=*.tmp", "--one-file-system", "--tag=prod"},
		s.BackupArgs)
	assert.Equal(t, []string{"--keep-last=1", "--keep-tag=k"}, s.ForgetArgs)
	assert.Equal(t, "pre", s.Pre)
	assert.Equal(t, "post", s.Post)
	assert.Equal(t, "fail", s.OnFailure)
	assert.Equal(t, []string{"db"}, s.After)
	assert.Equal(t, time.Hour, s.BackupTimeout)

	j.Type = config.TypeFiles
	assert.False(t, jobSettings(j).StopServices, "file jobs stop nothing")
}

func TestResticSettingsAndEnv(t *testing.T) {
	t.Parallel()
	r := config.Repository{
		URL: "/repo", Env: []string{"RESTIC_PASSWORD=x"}, LimitUpload: 5,
		BackendOptions: map[string]string{"s3.connections": "2"}, MaxUnused: "1%",
	}
	s := resticSettings(r)
	assert.Equal(t, []string{"--option=s3.connections=2", "--limit-upload=5"}, s.GlobalArgs)
	assert.Equal(t, []string{"--max-unused=1%"}, s.PruneArgs)
	assert.Equal(t, []string{"PATH=/bin", "RESTIC_PASSWORD=x", "RESTIC_REPOSITORY=/repo"},
		resticEnv([]string{"PATH=/bin"}, r))
	assert.Equal(t, []string{"PATH=/bin"}, resticEnv([]string{"PATH=/bin"}, config.Repository{}))
}

func TestWarnMissingBinaries(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	var looked []string
	lookPath := func(b string) (string, error) {
		looked = append(looked, b)
		if b == "podman" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + b, nil
	}
	warnMissingBinaries(zerolog.New(&buf), []config.Job{
		{Type: config.TypePodmanCompose},
		{Type: config.TypePodmanCompose},
		{Type: config.TypeDockerCompose},
		{Type: config.TypeSystemd},
		{Type: config.TypeFiles},
	}, lookPath)

	assert.Equal(t, []string{"docker", "podman", "restic", "systemctl"}, looked)
	assert.Contains(t, buf.String(), `"binary":"podman"`)
	assert.NotContains(t, buf.String(), `"binary":"docker"`)
}

func TestWarnConfig(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := zerolog.New(&buf)

	warnConfig(log, &config.Plan{}, []string{"RESTIC_REPOSITORY=/r", "RESTIC_REPOSITORY="})
	assert.Contains(t, buf.String(), "no restic repository", "the later, empty value wins")

	buf.Reset()
	warnConfig(log, &config.Plan{}, []string{"RESTIC_REPOSITORY_FILE=/etc/repo"})
	assert.Empty(t, buf.String())
}

func TestWarnRelativeHooks(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	plan := &config.Plan{OnFailure: "notify/x", Jobs: []config.Job{
		{Name: "rel", Pre: "./hooks/pre.sh --flag", Post: "bin/post"},
		{Name: "ok", Pre: "/usr/local/bin/pre.sh ./arg", Post: "pg_dump", OnFailure: "/x"},
	}}

	warnRelativeHooks(zerolog.New(&buf), plan)

	out := buf.String()
	assert.Contains(t, out, `"job":"","hook":"notify/x"`)
	assert.Contains(t, out, `"job":"rel","hook":"./hooks/pre.sh"`)
	assert.Contains(t, out, `"job":"rel","hook":"bin/post"`)
	assert.NotContains(t, out, `"job":"ok"`, "absolute paths and PATH lookups are fine")
}

func TestWarnEnvFileMode(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, tt := range []struct {
		mode os.FileMode
		warn bool
	}{{0o600, false}, {0o400, false}, {0o640, true}, {0o604, true}} {
		path := filepath.Join(dir, tt.mode.String())
		require.NoError(t, os.WriteFile(path, nil, 0o600))
		require.NoError(t, os.Chmod(path, tt.mode))
		var buf bytes.Buffer

		warnEnvFileMode(zerolog.New(&buf), path)

		assert.Equal(t, tt.warn, strings.Contains(buf.String(), "accessible by other users"),
			tt.mode)
	}
	var buf bytes.Buffer
	warnEnvFileMode(zerolog.New(&buf), "")
	warnEnvFileMode(zerolog.New(&buf), filepath.Join(dir, "missing"))
	assert.Empty(t, buf.String())
}

// fakeRestic puts a restic script that prints out first on PATH (the test must not be
// parallel) and returns the options for the config at path.
func fakeRestic(t *testing.T, path, out string) Options {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\necho '" + out + "'\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "restic"), []byte(script), 0o700))
	t.Setenv("PATH", dir)
	return options(path)
}

// TestApp_RunChecksResticVersion is not parallel: it sets PATH.
func TestApp_RunChecksResticVersion(t *testing.T) {
	path := writeConfig(t, "version: 2\nrepository: {url: /r, retry_lock: 1m}\n"+
		"jobs: {a: {sources: [/a]}}\n")

	a, err := New(fakeRestic(t, path, "restic 0.15.2 compiled with go1.20"))
	require.NoError(t, err)
	err = a.Run(context.Background())
	require.ErrorIs(t, err, ErrResticVersion)
	assert.EqualError(t, err,
		"app: restic is too old: 0.15.2, repository.retry_lock needs 0.16.0")

	a, err = New(fakeRestic(t, path, "not restic"))
	require.NoError(t, err)
	require.ErrorIs(t, a.Run(context.Background()), restic.ErrVersion)
}

func TestRequiredResticVersion(t *testing.T) {
	t.Parallel()
	v, reason := requiredResticVersion(&config.Plan{Jobs: []config.Job{{Name: "a"}}})
	assert.Equal(t, restic.Version{}, v)
	assert.Empty(t, reason)

	v, reason = requiredResticVersion(&config.Plan{
		Repository: config.Repository{RetryLock: time.Minute},
		Jobs:       []config.Job{{Name: "a"}, {Name: "b", SkipIfUnchanged: true}},
	})
	assert.Equal(t, "0.17.0", v.String())
	assert.Equal(t, "jobs.b.skip_if_unchanged", reason)
}
