// Package backup orchestrates one backup run: per project lock, pre-backup hook, stop
// services, restic backup, start services, post-backup hook; then forget per project and
// one repository-wide prune and unlock. It talks to restic, the service managers, the
// locker and the hook runner only through the interfaces declared here.
package backup

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"

	"github.com/dombyte/restor/internal/util"
)

var (
	// ErrMissingDependency is returned by NewManager for a missing dependency.
	ErrMissingDependency = errors.New("backup: missing dependency")
	// ErrProjectsFailed is returned by Run when at least one project failed.
	ErrProjectsFailed = errors.New("backup: projects failed")
)

// Restic runs restic commands against the repository.
type Restic interface {
	Backup(ctx context.Context, tag string, sources, opts []string) (string, error)
	Forget(ctx context.Context, tag, policy string, opts []string) error
	Prune(ctx context.Context, opts []string) error
	Unlock(ctx context.Context, opts []string) error
}

// Services stops and starts the services of one project. Running returns the services
// among requested (all when empty) that run now: only those are stopped and started
// again. Stop and Start report false when the services did not reach the expected state
// within the timeout.
type Services interface {
	LockKey() string
	Running(ctx context.Context, requested []string) ([]string, error)
	Stop(ctx context.Context, services []string, timeout time.Duration) (bool, error)
	Start(ctx context.Context, services []string, timeout time.Duration) (bool, error)
}

// Locker keeps two runs from working on the same services at the same time.
type Locker interface {
	Lock(key string) (release func() error, err error)
}

// Runner executes the hook commands.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Settings configure a Manager.
type Settings struct {
	// Parallel runs all projects at once instead of one after another.
	Parallel bool
	// AutoPrune runs forget per project with a retention policy, then prune and unlock.
	AutoPrune bool
	// PruneOptions are global restic options for prune and unlock.
	PruneOptions []string
}

// Project is one project and its service manager.
type Project struct {
	Settings ProjectSettings
	Services Services
}

// Deps are the dependencies of a Manager.
type Deps struct {
	Restic Restic
	Locker Locker
	Hooks  Runner
	Log    zerolog.Logger
}

// Manager runs the backup of all projects.
type Manager struct {
	s        Settings
	projects []Project
	d        Deps
}

// NewManager returns a Manager for projects, run in the given order in sequential mode.
func NewManager(s Settings, projects []Project, d Deps) (*Manager, error) {
	reqs := []util.Requirement{
		{Name: "Restic", OK: d.Restic != nil},
		{Name: "Locker", OK: d.Locker != nil},
		{Name: "Hooks", OK: d.Hooks != nil},
	}
	for _, p := range projects {
		reqs = append(reqs, util.Requirement{
			Name: "Projects[" + p.Settings.Name + "].Services", OK: p.Services != nil,
		})
	}
	if err := util.RequireAll(ErrMissingDependency, reqs...); err != nil {
		return nil, err
	}
	return &Manager{s: s, projects: projects, d: d}, nil
}

// Run backs up every project. With AutoPrune it then runs forget for the projects that
// were backed up, and prune and unlock only when every project succeeded. A cancelled
// run does no maintenance. Project failures are logged here and summarized in the
// returned ErrProjectsFailed; forget, prune and unlock failures are only logged.
func (m *Manager) Run(ctx context.Context) error {
	m.d.Log.Info().Bool("parallel", m.s.Parallel).Int("projects", len(m.projects)).
		Msg("starting backup run")

	succeeded, failed := m.runProjects(ctx)
	if ctx.Err() == nil {
		m.maintain(ctx, succeeded, failed == 0)
	}
	if failed > 0 {
		return fmt.Errorf("%w: %d of %d", ErrProjectsFailed, failed, len(m.projects))
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	return nil
}

// forget runs forget for every one of projects with a retention policy and reports
// whether there was one.
func (m *Manager) forget(ctx context.Context, projects []Project) bool {
	forgot := false
	for _, p := range projects {
		s := p.Settings
		if s.RetentionPolicy == "" {
			continue
		}
		forgot = true
		log := m.d.Log.With().Str("project", s.Name).Logger()
		if err := m.d.Restic.Forget(ctx, s.Name, s.RetentionPolicy, s.ForgetOptions); err != nil {
			log.Error().Err(err).Msg("forget failed")
			continue
		}
		log.Info().Str("retention_policy", s.RetentionPolicy).Msg("forget completed")
	}
	return forgot
}

// runProjects runs the projects in the configured mode and returns those that succeeded
// (in their configured order) and how many failed; projects skipped after a cancel are
// neither.
func (m *Manager) runProjects(ctx context.Context) ([]Project, int) {
	ok := make([]bool, len(m.projects)) // each run writes only its own index
	var failed atomic.Int32
	run := func(i int) {
		p := m.projects[i]
		if err := m.runProject(ctx, p); err != nil {
			failed.Add(1)
			m.d.Log.Error().Err(err).Str("project", p.Settings.Name).Msg("project backup failed")
			return
		}
		ok[i] = true
	}
	if m.s.Parallel {
		var wg sync.WaitGroup
		for i := range m.projects {
			wg.Go(func() { run(i) })
		}
		wg.Wait()
	} else {
		for i := range m.projects {
			if ctx.Err() != nil {
				break
			}
			run(i)
		}
	}
	return m.selected(ok), int(failed.Load())
}

// selected returns the projects whose index is true in ok, in their configured order.
func (m *Manager) selected(ok []bool) []Project {
	var out []Project
	for i, p := range m.projects {
		if ok[i] {
			out = append(out, p)
		}
	}
	return out
}

// maintain runs forget for the projects with a retention policy among succeeded, then
// one prune and unlock when allSucceeded. They run after all backups because they need
// exclusive repository locks. Forget only touches projects with a new snapshot; prune is
// skipped after a failure, so a broken run never removes data.
func (m *Manager) maintain(ctx context.Context, succeeded []Project, allSucceeded bool) {
	if !m.s.AutoPrune || !m.forget(ctx, succeeded) {
		return
	}
	if !allSucceeded {
		m.d.Log.Warn().Msg("prune skipped because a project failed")
		return
	}
	if err := m.d.Restic.Prune(ctx, m.s.PruneOptions); err != nil {
		m.d.Log.Error().Err(err).Msg("prune failed")
	} else {
		m.d.Log.Info().Msg("prune completed")
	}
	if err := m.d.Restic.Unlock(ctx, m.s.PruneOptions); err != nil {
		m.d.Log.Error().Err(err).Msg("unlock failed")
	}
}
