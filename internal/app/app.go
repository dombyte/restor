// Package app is the composition root: it loads the configuration, builds every
// component with its concrete dependencies and runs one backup cycle. It is the only
// package that imports config and names concrete types.
package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"slices"

	"github.com/rs/zerolog"

	"github.com/dombyte/restor/internal/backup"
	"github.com/dombyte/restor/internal/command"
	"github.com/dombyte/restor/internal/config"
	"github.com/dombyte/restor/internal/lock"
	"github.com/dombyte/restor/internal/restic"
	"github.com/dombyte/restor/internal/servicemanager"
	"github.com/dombyte/restor/internal/util"
)

// ErrUnknownType is returned by CreateServiceManager for an unknown service manager.
var ErrUnknownType = errors.New("app: unknown service manager")

// Options are the inputs of New.
type Options struct {
	// ConfigPath is the configuration file.
	ConfigPath string
	// Getenv resolves ${VAR} references in the configuration.
	Getenv func(string) string
	// Environ is the process environment the external programs inherit.
	Environ []string
	// Log is the root logger.
	Log zerolog.Logger
}

// App is one wired backup run.
type App struct {
	manager *backup.Manager
	empty   bool
	log     zerolog.Logger
}

// New loads the configuration and wires all components; it starts nothing.
func New(o Options) (*App, error) {
	cfg, err := config.Load(o.ConfigPath, o.Getenv)
	if err != nil {
		return nil, err
	}
	projects := cfg.ResolvedProjects()
	logProjects(o.Log, projects)
	warnMissingBinaries(o.Log, projects, exec.LookPath)

	env := append(slices.Clone(o.Environ), cfg.EnvPairs()...)
	runner := command.New(env, component(o.Log, "command"))
	clock := util.NewRealClock()
	manager, err := createManager(cfg, projects, managerDeps{
		runner: runner, resticRunner: resticRunner(env, cfg.Global.ResticRepo, o.Log),
		clock: clock, lockDir: lockDir(o.Getenv), log: o.Log,
	})
	if err != nil {
		return nil, err
	}
	return &App{manager: manager, empty: len(projects) == 0, log: o.Log}, nil
}

// Run runs one backup cycle.
func (a *App) Run(ctx context.Context) error {
	if a.empty {
		a.log.Warn().Msg("no projects configured")
		return nil
	}
	return a.manager.Run(ctx)
}

type managerDeps struct {
	runner, resticRunner *command.Runner
	clock                util.Clock
	lockDir              string
	log                  zerolog.Logger
}

// lockDir picks the lock directory of the user running restor.
func lockDir(getenv func(string) string) string {
	return lock.DefaultDir(lock.User{
		UID: os.Getuid(), GOOS: runtime.GOOS,
		RuntimeDir: getenv("XDG_RUNTIME_DIR"), TempDir: os.TempDir(),
	})
}

func createManager(cfg *config.Config, projects []config.Project, d managerDeps) (
	*backup.Manager, error,
) {
	resticClient, err := restic.New(restic.Deps{Runner: d.resticRunner})
	if err != nil {
		return nil, err
	}
	locker, err := lock.New(lock.Settings{Dir: d.lockDir}, lock.Deps{
		Clock: d.clock, Processes: lock.OSProcesses{}, Log: component(d.log, "lock"),
	})
	if err != nil {
		return nil, err
	}
	smDeps := servicemanager.Deps{Runner: d.runner, Clock: d.clock}
	bps := make([]backup.Project, 0, len(projects))
	for _, p := range projects {
		services, err := CreateServiceManager(p, smDeps)
		if err != nil {
			return nil, fmt.Errorf("app: project %s: %w", p.Name, err)
		}
		bps = append(bps, backup.Project{Settings: projectSettings(p), Services: services})
	}
	return backup.NewManager(backup.Settings{
		Parallel:     cfg.RunMode() == config.ModeParallel,
		AutoPrune:    cfg.Global.AutoPrune,
		PruneOptions: cfg.Global.PruneOptions,
	}, bps, backup.Deps{
		Restic: resticClient, Locker: locker, Hooks: d.runner, Log: component(d.log, "backup"),
	})
}

// CreateServiceManager builds the service manager a project is configured with.
func CreateServiceManager(p config.Project, d servicemanager.Deps) (backup.Services, error) {
	switch p.ServiceManager {
	case config.ManagerDockerCompose:
		return servicemanager.NewCompose(
			servicemanager.ComposeSettings{Binary: "docker", File: p.ComposeFile}, d)
	case config.ManagerPodmanCompose:
		return servicemanager.NewCompose(
			servicemanager.ComposeSettings{Binary: "podman", File: p.ComposeFile}, d)
	case config.ManagerSystemd:
		return servicemanager.NewSystemd(servicemanager.SystemdSettings{
			Units: p.SystemdUnits, User: p.SystemdScope == config.ScopeUser,
		}, d)
	case config.ManagerNoop:
		// Keyed by project: noop projects share no services and may run in parallel.
		return servicemanager.NewNoop(servicemanager.NoopSettings{LockKey: "noop-" + p.Name}),
			nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownType, p.ServiceManager)
	}
}

func projectSettings(p config.Project) backup.ProjectSettings {
	return backup.ProjectSettings{
		Name: p.Name, Services: p.Services, Sources: p.Sources, StopServices: p.StopServices,
		StopTimeout: p.StopTimeout, StartTimeout: p.StartTimeout,
		RetentionPolicy: p.RetentionPolicy,
		BackupOptions:   p.BackupOptions, ForgetOptions: p.ForgetOptions,
		PreBackupCmd: p.PreBackupCmd, PostBackupCmd: p.PostBackupCmd,
	}
}

// resticRunner runs restic with the repository in RESTIC_REPOSITORY, so it never shows up
// in arguments or debug logs. An empty repo leaves a RESTIC_REPOSITORY from the env alone.
func resticRunner(env []string, repo string, log zerolog.Logger) *command.Runner {
	if repo != "" {
		env = append(slices.Clone(env), "RESTIC_REPOSITORY="+repo)
	}
	return command.New(env, component(log, "restic"))
}

func component(log zerolog.Logger, name string) zerolog.Logger {
	return log.With().Str("component", name).Logger()
}

func logProjects(log zerolog.Logger, projects []config.Project) {
	for _, p := range projects {
		log.Info().Str("project", p.Name).Str("service_manager", p.ServiceManager).
			Strs("sources", p.Sources).Bool("stop_services", p.StopServices).
			Msg("project configured")
	}
}

// warnMissingBinaries warns about programs the run will need but PATH does not have.
func warnMissingBinaries(log zerolog.Logger, projects []config.Project,
	lookPath func(string) (string, error),
) {
	binaries := []string{"restic"}
	for _, p := range projects {
		switch p.ServiceManager {
		case config.ManagerDockerCompose:
			binaries = append(binaries, "docker")
		case config.ManagerPodmanCompose:
			binaries = append(binaries, "podman")
		case config.ManagerSystemd:
			binaries = append(binaries, "systemctl")
		}
	}
	slices.Sort(binaries)
	for _, b := range slices.Compact(binaries) {
		if _, err := lookPath(b); err != nil {
			log.Warn().Str("binary", b).Msg("binary not found in PATH; commands using it will fail")
		}
	}
}
