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
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/dombyte/restor/internal/backup"
	"github.com/dombyte/restor/internal/command"
	"github.com/dombyte/restor/internal/config"
	"github.com/dombyte/restor/internal/lock"
	"github.com/dombyte/restor/internal/restic"
	"github.com/dombyte/restor/internal/servicemanager"
	"github.com/dombyte/restor/internal/util"
)

// envFileOthersMask are the permission bits that let group or others access the env file.
const envFileOthersMask os.FileMode = 0o077

// ErrUnknownType is returned by CreateServiceManager for an unknown job type.
var ErrUnknownType = errors.New("app: unknown job type")

// Options are the inputs of New.
type Options struct {
	// ConfigPath is the configuration file.
	ConfigPath string
	// Jobs selects the jobs to run; empty runs all.
	Jobs []string
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
	log     zerolog.Logger
}

// New loads the configuration and wires all components; it starts nothing.
func New(o Options) (*App, error) {
	plan, err := config.Load(o.ConfigPath, o.Getenv)
	if err != nil {
		return nil, err
	}
	if plan, err = plan.Select(o.Jobs); err != nil {
		return nil, err
	}
	logJobs(o.Log, plan.Jobs)
	resticEnv := resticEnv(o.Environ, plan.Repository)
	warnConfig(o.Log, plan, resticEnv)
	warnMissingBinaries(o.Log, plan.Jobs, exec.LookPath)

	manager, err := createManager(plan, managerDeps{
		runner:       command.New(o.Environ, component(o.Log, "command")),
		resticRunner: command.New(resticEnv, component(o.Log, "restic")),
		clock:        util.NewRealClock(), lockDir: lockDir(o.Getenv), log: o.Log,
	})
	if err != nil {
		return nil, err
	}
	return &App{manager: manager, log: o.Log}, nil
}

// Run runs one backup cycle.
func (a *App) Run(ctx context.Context) error {
	return a.manager.Run(ctx)
}

// ShutdownTimeout returns how long Run may take after its context was cancelled, so that
// every job can restart the services it stopped.
func (a *App) ShutdownTimeout() time.Duration { return a.manager.ShutdownTimeout() }

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

func createManager(plan *config.Plan, d managerDeps) (*backup.Manager, error) {
	resticClient, err := restic.New(resticSettings(plan.Repository),
		restic.Deps{Runner: d.resticRunner, Log: component(d.log, "restic")})
	if err != nil {
		return nil, err
	}
	locker, err := lock.New(lock.Settings{Dir: d.lockDir})
	if err != nil {
		return nil, err
	}
	smDeps := servicemanager.Deps{Runner: d.runner, Clock: d.clock}
	bps := make([]backup.Project, 0, len(plan.Jobs))
	for _, j := range plan.Jobs {
		services, err := CreateServiceManager(j, smDeps)
		if err != nil {
			return nil, fmt.Errorf("app: job %s: %w", j.Name, err)
		}
		bps = append(bps, backup.Project{Settings: jobSettings(j), Services: services})
	}
	parallel := plan.Concurrency != 1
	if parallel {
		warnSharedLocks(d.log, bps)
	}
	return backup.NewManager(backup.Settings{
		Parallel: parallel, AutoPrune: plan.Repository.Prune,
	}, bps, backup.Deps{
		Restic: resticClient, Locker: locker, Hooks: d.runner, Log: component(d.log, "backup"),
	})
}

// CreateServiceManager builds the service manager of a job's type.
func CreateServiceManager(j config.Job, d servicemanager.Deps) (backup.Services, error) {
	switch j.Type {
	case config.TypeDockerCompose:
		return servicemanager.NewCompose(
			servicemanager.ComposeSettings{Binary: "docker", File: j.ComposeFile}, d)
	case config.TypePodmanCompose:
		return servicemanager.NewCompose(
			servicemanager.ComposeSettings{Binary: "podman", File: j.ComposeFile}, d)
	case config.TypeSystemd:
		return servicemanager.NewSystemd(servicemanager.SystemdSettings{
			Units: j.Units, User: j.Scope == config.ScopeUser,
		}, d)
	case config.TypeFiles:
		// Keyed by job: file jobs share no services and may run at the same time.
		return servicemanager.NewNoop(servicemanager.NoopSettings{LockKey: "files-" + j.Name}),
			nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownType, j.Type)
	}
}

func jobSettings(j config.Job) backup.ProjectSettings {
	return backup.ProjectSettings{
		Name: j.Name, Services: j.Services, Sources: j.Sources, StopServices: j.StopsServices(),
		StopTimeout: j.StopTimeout, StartTimeout: j.StartTimeout,
		BackupArgs: backupFlags(j).Args(), ForgetArgs: keep(j.Keep).Args(),
		PreBackupCmd: j.Pre, PostBackupCmd: j.Post,
	}
}

func backupFlags(j config.Job) restic.BackupFlags {
	return restic.BackupFlags{
		Exclude: j.Exclude, IExclude: j.IExclude, ExcludeFile: j.ExcludeFile,
		ExcludeIfPresent: j.ExcludeIfPresent, ExcludeCaches: j.ExcludeCaches,
		ExcludeLargerThan: j.ExcludeLargerThan, OneFileSystem: j.OneFileSystem,
		SkipIfUnchanged: j.SkipIfUnchanged, Tags: j.Tags,
	}
}

func keep(k config.Keep) restic.Keep {
	return restic.Keep{
		Last: k.Last, Hourly: k.Hourly, Daily: k.Daily, Weekly: k.Weekly, Monthly: k.Monthly,
		Yearly: k.Yearly, Within: k.Within, WithinHourly: k.WithinHourly,
		WithinDaily: k.WithinDaily, WithinWeekly: k.WithinWeekly,
		WithinMonthly: k.WithinMonthly, WithinYearly: k.WithinYearly, Tags: k.Tags,
	}
}

func resticSettings(r config.Repository) restic.Settings {
	return restic.Settings{
		GlobalArgs: restic.Repository{
			BackendOptions: r.BackendOptions, LimitUpload: r.LimitUpload,
			LimitDownload: r.LimitDownload, RetryLock: r.RetryLock,
		}.Args(),
		PruneArgs: restic.PruneFlags{MaxUnused: r.MaxUnused}.Args(),
	}
}

// resticEnv is the environment of restic: the process environment, the env file and the
// repository in RESTIC_REPOSITORY, so it never shows up in arguments or debug logs. An
// empty URL leaves a RESTIC_REPOSITORY from the environment alone.
func resticEnv(environ []string, r config.Repository) []string {
	env := slices.Concat(environ, r.Env)
	if r.URL != "" {
		env = append(env, "RESTIC_REPOSITORY="+r.URL)
	}
	return env
}

func component(log zerolog.Logger, name string) zerolog.Logger {
	return log.With().Str("component", name).Logger()
}

func logJobs(log zerolog.Logger, jobs []config.Job) {
	for _, j := range jobs {
		log.Info().Str("job", j.Name).Str("type", or(j.Type, "files")).
			Strs("sources", j.Sources).Strs("after", j.After).
			Bool("stop", j.StopsServices()).Msg("job configured")
	}
}

func or(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// warnConfig warns about settings that load fine but are probably not what was meant.
func warnConfig(log zerolog.Logger, plan *config.Plan, resticEnv []string) {
	if !hasRepository(resticEnv) {
		log.Warn().Msg("no restic repository: set repository.url, or RESTIC_REPOSITORY " +
			"in the env file")
	}
	warnEnvFileMode(log, plan.Repository.EnvFile)
	warnRelativeHooks(log, plan)
}

// warnRelativeHooks warns about hooks whose program is a relative path such as
// ./backup.sh: it resolves against the working directory, which is / (system) or the home
// directory (user) under systemd, not against the config directory.
func warnRelativeHooks(log zerolog.Logger, plan *config.Plan) {
	hooks := [][2]string{{"", plan.OnFailure}}
	for _, j := range plan.Jobs {
		hooks = append(hooks, [2]string{j.Name, j.Pre}, [2]string{j.Name, j.Post},
			[2]string{j.Name, j.OnFailure})
	}
	for _, h := range hooks {
		fields := strings.Fields(h[1])
		if len(fields) > 0 && strings.Contains(fields[0], "/") && !filepath.IsAbs(fields[0]) {
			log.Warn().Str("job", h[0]).Str("hook", fields[0]).
				Msg("hook path is relative to the working directory, not to the config " +
					"file; use an absolute path")
		}
	}
}

// warnEnvFileMode warns when the env file, which usually holds the restic password, can
// be read or changed by other users. It is only a warning: the file still works.
func warnEnvFileMode(log zerolog.Logger, path string) {
	if path == "" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		return // config.Load already read it; a race here is not worth a warning
	}
	if info.Mode().Perm()&envFileOthersMask != 0 {
		log.Warn().Str("env_file", path).Str("mode", info.Mode().Perm().String()).
			Msg("env_file is accessible by other users; restrict it with chmod 600")
	}
}

// hasRepository reports whether env (KEY=value, later entries win) names a repository.
func hasRepository(env []string) bool {
	vars := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		vars[k] = v
	}
	return vars["RESTIC_REPOSITORY"] != "" || vars["RESTIC_REPOSITORY_FILE"] != ""
}

// warnSharedLocks warns about jobs that share a lock key (a compose file or a systemd
// unit): in parallel mode they start at the same time, so all but one fail as locked.
func warnSharedLocks(log zerolog.Logger, projects []backup.Project) {
	owner := map[string]string{}
	for _, p := range projects {
		for _, key := range p.Services.LockKeys() {
			if first, ok := owner[key]; ok {
				log.Warn().Str("job", p.Settings.Name).Str("other_job", first).
					Str("lock", key).Msg("jobs share services and cannot run in " +
					"parallel: one of them will fail as locked; use concurrency: 1")
				continue
			}
			owner[key] = p.Settings.Name
		}
	}
}

// warnMissingBinaries warns about programs the run will need but PATH does not have.
func warnMissingBinaries(log zerolog.Logger, jobs []config.Job,
	lookPath func(string) (string, error),
) {
	binaries := []string{"restic"}
	for _, j := range jobs {
		switch j.Type {
		case config.TypeDockerCompose:
			binaries = append(binaries, "docker")
		case config.TypePodmanCompose:
			binaries = append(binaries, "podman")
		case config.TypeSystemd:
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
