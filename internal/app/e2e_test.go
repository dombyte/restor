//go:build e2e

package app

// The end-to-end tests run the service managers against real Docker Compose, Podman
// Compose and systemd, and a whole run that starts services an earlier run left stopped.
// They start containers and units, so they refuse to run outside a container or CI:
//
//	scripts/e2e.sh [image]     # in a throwaway privileged container
//
// Tests whose tools are missing (podman, systemd as PID 1, restic) are skipped.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dombyte/restor/internal/command"
	"github.com/dombyte/restor/internal/config"
	"github.com/dombyte/restor/internal/servicemanager"
	"github.com/dombyte/restor/internal/state"
	"github.com/dombyte/restor/internal/util"
)

const e2eImage = "busybox:1.37.0" // renovate: datasource=docker

// e2eCompose has a service that runs, one of a profile and one that exits at once. No
// networks: podman's netavark fails to set them up inside the test container.
const e2eCompose = `services:
  web:
    image: ` + e2eImage + `
    command: [sleep, "3600"]
    init: true
    network_mode: none
  tools:
    image: ` + e2eImage + `
    command: [sleep, "3600"]
    init: true
    network_mode: none
    profiles: [tools]
  broken:
    image: ` + e2eImage + `
    command: [sh, -c, "exit 1"]
    network_mode: none
`

// requireE2E skips unless RESTOR_E2E=1 is set inside a container or on CI: the tests
// start containers and systemd units.
func requireE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("RESTOR_E2E") != "1" {
		t.Skip("set RESTOR_E2E=1, see scripts/e2e.sh")
	}
	_, docker := os.Stat("/.dockerenv")
	_, podman := os.Stat("/run/.containerenv")
	if docker != nil && podman != nil && os.Getenv("CI") != "true" {
		t.Fatal("refusing to start containers and units outside a container or CI")
	}
}

func e2eRunner() *command.Runner { return command.New(os.Environ(), zerolog.Nop()) }

func e2eDeps() servicemanager.Deps {
	return servicemanager.Deps{Runner: e2eRunner(), Clock: util.NewRealClock()}
}

// e2eProject writes the compose file and starts web, tools and broken with binary.
func e2eProject(t *testing.T, binary, project string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "compose.yaml")
	require.NoError(t, os.WriteFile(file, []byte(e2eCompose), 0o600))
	compose := func(args ...string) error {
		_, err := e2eRunner().Run(context.Background(), binary, append([]string{
			"compose", "-f", file, "-p", project, "--profile", "tools",
		}, args...)...)
		return err
	}
	require.NoError(t, compose("up", "-d", "web", "tools", "broken"))
	t.Cleanup(func() { _ = compose("down", "-v", "-t", "0") })
	require.Eventually(t, func() bool { // right after up, broken is still "Up"
		out, err := e2eRunner().Run(context.Background(), binary, "compose", "-f", file,
			"-p", project, "ps", "--services", "--status", "running")
		return err == nil && !slices.Contains(strings.Fields(string(out)), "broken")
	}, 30*time.Second, 200*time.Millisecond)
	return file
}

func TestE2E(t *testing.T) {
	requireE2E(t)
	t.Run("docker compose", func(t *testing.T) {
		testE2ECompose(t, config.TypeDockerCompose, "docker")
	})
	t.Run("podman compose", func(t *testing.T) {
		// ls needs the provider and its connection to podman, version only the CLI.
		if exec.Command("podman", "compose", "ls").Run() != nil {
			t.Skip("no working podman compose")
		}
		testE2ECompose(t, config.TypePodmanCompose, "podman")
	})
	t.Run("systemd", testE2ESystemd)
	t.Run("recovery", testE2ERecovery)
}

func testE2ECompose(t *testing.T, typ, binary string) {
	ctx := context.Background()
	project := "restor-e2e-" + binary
	file := e2eProject(t, binary, project)
	job := config.Job{
		Type: typ, ComposeFiles: []string{file}, ProjectName: project,
		Profiles: []string{"tools"},
	}
	svc, err := CreateServiceManager(job, e2eDeps())
	require.NoError(t, err)

	running, err := svc.Running(ctx, nil)
	require.NoError(t, err)
	slices.Sort(running)
	assert.Equal(t, []string{"tools", "web"}, running, "broken has exited")

	job.Profiles = nil
	withoutProfile, err := CreateServiceManager(job, e2eDeps())
	require.NoError(t, err)
	got, err := withoutProfile.Running(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"web"}, got, "without profiles the tools service is not found")

	ok, err := svc.Stop(ctx, running, 5*time.Second)
	require.NoError(t, err)
	assert.True(t, ok)
	got, err = svc.Running(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, got)

	ok, err = svc.Start(ctx, running, time.Minute)
	require.NoError(t, err)
	assert.True(t, ok)

	start := time.Now()
	ok, err = svc.Start(ctx, []string{"broken"}, 10*time.Second)
	assert.False(t, ok, "a container that exits at once is not started")
	if binary == "docker" {
		// podman lists exited containers only with podman-compose (no --all): it waits.
		require.ErrorContains(t, err, "service failed to start")
		assert.Less(t, time.Since(start), 5*time.Second, "no wait for the start timeout")
	}
}

// e2eUnit writes a runtime unit and removes it after the test.
func e2eUnit(t *testing.T, name, execStart string) {
	t.Helper()
	path := filepath.Join("/run/systemd/system", name)
	unit := "[Service]\nExecStart=" + execStart + "\n"
	require.NoError(t, os.WriteFile(path, []byte(unit), 0o644))
	systemctl := func(args ...string) error {
		_, err := e2eRunner().Run(context.Background(), "systemctl", args...)
		return err
	}
	require.NoError(t, systemctl("daemon-reload"))
	t.Cleanup(func() {
		_ = systemctl("stop", name)
		_ = os.Remove(path)
		_ = systemctl("daemon-reload")
		_ = systemctl("reset-failed", name)
	})
}

func testE2ESystemd(t *testing.T) {
	if _, err := os.Stat("/run/systemd/system"); err != nil || os.Getuid() != 0 {
		t.Skip("needs systemd as PID 1 and root")
	}
	ctx := context.Background()
	e2eUnit(t, "restor-e2e-a.service", "/bin/sleep 3600")
	e2eUnit(t, "restor-e2e-b.service", "/bin/sleep 3600")
	e2eUnit(t, "restor-e2e-fail.service", "/bin/false")
	units := []string{"restor-e2e-a.service", "restor-e2e-b.service"}
	_, err := e2eRunner().Run(ctx, "systemctl", append([]string{"start"}, units...)...)
	require.NoError(t, err)
	svc, err := CreateServiceManager(config.Job{
		Type: config.TypeSystemd, Units: append(units, "restor-e2e-fail.service"),
	}, e2eDeps())
	require.NoError(t, err)

	running, err := svc.Running(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, units, running, "the failing unit is not up")
	ok, err := svc.Stop(ctx, units, 10*time.Second)
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = svc.Start(ctx, units, time.Minute)
	require.NoError(t, err)
	assert.True(t, ok)

	start := time.Now()
	_, err = svc.Start(ctx, []string{"restor-e2e-fail.service"}, time.Minute)
	require.ErrorContains(t, err, "service failed to start")
	assert.Less(t, time.Since(start), 30*time.Second, "no wait for the start timeout")
}

// testE2ERecovery stops a service as a killed run would have left it, with its record,
// then runs restor: the service runs again afterwards and the record is gone.
func testE2ERecovery(t *testing.T) {
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("no restic")
	}
	ctx := context.Background()
	project := "restor-e2e-recovery"
	file := e2eProject(t, "docker", project)
	_, err := e2eRunner().Run(ctx, "docker", "compose", "-f", file, "-p", project, "stop",
		"web")
	require.NoError(t, err)

	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	env := filepath.Join(dir, "restic.env")
	require.NoError(t, os.WriteFile(env, []byte("RESTIC_PASSWORD=e2e\n"), 0o600))
	_, err = command.New(append(os.Environ(), "RESTIC_PASSWORD=e2e",
		"RESTIC_REPOSITORY="+repo), zerolog.Nop()).Run(ctx, "restic", "init")
	require.NoError(t, err)
	cfg := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfg, []byte(`version: 2
repository: {url: `+repo+`, env_file: restic.env}
jobs:
  e2e-web:
    type: docker-compose
    compose_file: `+file+`
    project_name: `+project+`
    services: [web]
    stop_timeout: 5s
    sources: [`+dir+`/restic.env]
`), 0o600))

	records, err := state.New(state.Settings{Dir: stateDir(os.Getenv, lockDir(os.Getenv))})
	require.NoError(t, err)
	require.NoError(t, records.Save("e2e-web", []string{"web"}))
	t.Cleanup(func() { _ = records.Remove("e2e-web") })

	a, err := New(Options{
		ConfigPath: cfg, Getenv: os.Getenv, Environ: os.Environ(), Log: zerolog.Nop(),
		Hostname: os.Hostname,
	})
	require.NoError(t, err)
	require.NoError(t, a.Run(ctx))

	out, err := e2eRunner().Run(ctx, "docker", "compose", "-f", file, "-p", project, "ps",
		"--services", "--status", "running")
	require.NoError(t, err)
	assert.Contains(t, strings.Fields(string(out)), "web", "started again")
	left, err := records.Load("e2e-web")
	require.NoError(t, err)
	assert.Nil(t, left, "the record is removed once the service runs")
}
