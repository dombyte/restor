package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dombyte/restor/internal/config"
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

func TestNew_WiresAllServiceManagers(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, `
global: {restic_repo: /repo, auto_prune: true}
projects:
  docker: {service_manager: docker-compose, compose_file: /d.yml, sources: [/d]}
  podman: {service_manager: podman-compose, compose_file: /p.yml, sources: [/p]}
  units: {service_manager: systemd, systemd_units: [a.service], sources: [/u]}
  files: {service_manager: noop, sources: [/f]}
`)
	a, err := New(options(path))
	require.NoError(t, err)
	assert.False(t, a.empty)
}

func TestNew_Errors(t *testing.T) {
	t.Parallel()
	_, err := New(options(filepath.Join(t.TempDir(), "missing.yaml")))
	require.Error(t, err)

	_, err = New(options(writeConfig(t, "projects:\n  x: {service_manager: nope}\n")))
	require.ErrorIs(t, err, config.ErrInvalid)
}

func TestApp_RunWithoutProjects(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	o := options(writeConfig(t, "global: {restic_repo: /repo}\n"))
	o.Log = zerolog.New(&buf)
	a, err := New(o)
	require.NoError(t, err)

	require.NoError(t, a.Run(context.Background()))
	assert.Contains(t, buf.String(), "no projects configured")
}

func TestCreateServiceManager(t *testing.T) {
	t.Parallel()
	d := servicemanager.Deps{
		Runner: nopRunner{}, Clock: clocktest.New(time.Unix(0, 0)),
	}
	tests := []struct {
		project config.Project
		lockKey string
		wantErr error
	}{
		{
			project: config.Project{ServiceManager: config.ManagerDockerCompose, ComposeFile: "/d"},
			lockKey: "/d",
		},
		{
			project: config.Project{ServiceManager: config.ManagerPodmanCompose, ComposeFile: "/p"},
			lockKey: "/p",
		},
		{project: config.Project{
			ServiceManager: config.ManagerSystemd, SystemdUnits: []string{"a", "b"},
			SystemdScope: config.ScopeUser,
		}, lockKey: "a,b"},
		{
			project: config.Project{Name: "files", ServiceManager: config.ManagerNoop},
			lockKey: "noop-files",
		},
		{project: config.Project{ServiceManager: "nope"}, wantErr: ErrUnknownType},
		{
			project: config.Project{ServiceManager: config.ManagerDockerCompose},
			wantErr: servicemanager.ErrMissingDependency,
		},
	}
	for _, tt := range tests {
		t.Run(tt.project.ServiceManager, func(t *testing.T) {
			t.Parallel()
			s, err := CreateServiceManager(tt.project, d)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.lockKey, s.LockKey())
		})
	}
}

type nopRunner struct{}

func (nopRunner) Run(context.Context, string, ...string) ([]byte, error) { return nil, nil }

func TestProjectSettings(t *testing.T) {
	t.Parallel()
	p := config.Project{
		Name: "web", Services: []string{"s"}, Sources: []string{"/x"}, StopServices: true,
		StopTimeout: time.Second, StartTimeout: 2 * time.Second, RetentionPolicy: "--keep-last 1",
		BackupOptions: []string{"-b"}, ForgetOptions: []string{"-f"},
		PreBackupCmd: "pre", PostBackupCmd: "post",
	}
	s := projectSettings(p)
	assert.Equal(t, "web", s.Name)
	assert.Equal(t, []string{"s"}, s.Services)
	assert.Equal(t, []string{"/x"}, s.Sources)
	assert.True(t, s.StopServices)
	assert.Equal(t, time.Second, s.StopTimeout)
	assert.Equal(t, 2*time.Second, s.StartTimeout)
	assert.Equal(t, "--keep-last 1", s.RetentionPolicy)
	assert.Equal(t, []string{"-b"}, s.BackupOptions)
	assert.Equal(t, []string{"-f"}, s.ForgetOptions)
	assert.Equal(t, "pre", s.PreBackupCmd)
	assert.Equal(t, "post", s.PostBackupCmd)
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
	warnMissingBinaries(zerolog.New(&buf), []config.Project{
		{ServiceManager: config.ManagerPodmanCompose},
		{ServiceManager: config.ManagerPodmanCompose},
		{ServiceManager: config.ManagerDockerCompose},
		{ServiceManager: config.ManagerSystemd},
		{ServiceManager: config.ManagerNoop},
	}, lookPath)

	assert.Equal(t, []string{"docker", "podman", "restic", "systemctl"}, looked)
	assert.Contains(t, buf.String(), `"binary":"podman"`)
	assert.NotContains(t, buf.String(), `"binary":"docker"`)
}
