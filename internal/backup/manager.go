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
	"time"

	"github.com/rs/zerolog"

	"github.com/dombyte/restor/internal/util"
)

// shutdownGrace is what a cancelled run needs besides restarting services: the running
// command gets SIGTERM and is killed 10 s later (package command), then the run unwinds.
const shutdownGrace = time.Minute

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

// Services stops and starts the services of one project. LockKeys name what it manages
// (a compose file, each systemd unit); a project runs only while it holds all of them.
// Running returns the services
// among requested (all when empty) that run now: only those are stopped and started
// again. Stop and Start report false when the services did not reach the expected state
// within the timeout.
type Services interface {
	LockKeys() []string
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

// Run backs up every project. With AutoPrune it then runs forget for the projects with a
// complete snapshot, and prune and unlock only when every snapshot is complete. A
// cancelled run does no maintenance. Project failures are logged here and summarized in
// the returned ErrProjectsFailed; forget, prune and unlock failures are only logged.
func (m *Manager) Run(ctx context.Context) error {
	m.d.Log.Info().Bool("parallel", m.s.Parallel).Int("projects", len(m.projects)).
		Stringer("shutdown_timeout", m.ShutdownTimeout()).Msg("starting backup run")

	outcomes := m.runProjects(ctx)
	if ctx.Err() == nil {
		m.maintain(ctx, outcomes)
	}
	if failed := m.count(outcomes, outcomeFailed); failed > 0 {
		return fmt.Errorf("%w: %d of %d", ErrProjectsFailed, failed, len(m.projects))
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	return nil
}

// ShutdownTimeout returns how long the run may take after its context was cancelled: the
// running commands end, then each running project restarts the services it stopped. Only
// the projects running at the cancel restart (sequential mode starts no further one), and
// in parallel mode they restart at the same time, so the largest start timeout counts,
// not the sum.
func (m *Manager) ShutdownTimeout() time.Duration {
	var restart time.Duration
	for _, p := range m.projects {
		if p.Settings.StopServices {
			restart = max(restart, p.Settings.StartTimeout+restartGrace)
		}
	}
	return shutdownGrace + restart
}

// outcome is how the backup of one project ended.
type outcome int

const (
	// The zero value: not started because the run was cancelled.
	_ outcome = iota
	// outcomeFailed: no snapshot, the project failed.
	outcomeFailed
	// outcomeIncomplete: backed up, but the snapshot misses files or was not reported.
	outcomeIncomplete
	// outcomeComplete: backed up with a complete snapshot.
	outcomeComplete
)

// runProjects runs the projects in the configured mode and returns the outcome of each,
// by index.
func (m *Manager) runProjects(ctx context.Context) []outcome {
	outcomes := make([]outcome, len(m.projects)) // each run writes only its own index
	run := func(i int) {
		p := m.projects[i]
		complete, err := m.runProject(ctx, p)
		switch {
		case err != nil:
			outcomes[i] = outcomeFailed
			m.d.Log.Error().Err(err).Str("project", p.Settings.Name).Msg("project backup failed")
		case complete:
			outcomes[i] = outcomeComplete
		default:
			outcomes[i] = outcomeIncomplete
		}
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
	return outcomes
}

// count returns how many of outcomes are want.
func (m *Manager) count(outcomes []outcome, want outcome) int {
	n := 0
	for _, o := range outcomes {
		if o == want {
			n++
		}
	}
	return n
}

// maintain runs forget for the projects with a retention policy and a complete snapshot,
// then one prune and unlock when every project has one. They run after all backups
// because they need exclusive repository locks. A failed or incomplete backup keeps its
// project's history and skips prune, so a broken run never removes data.
func (m *Manager) maintain(ctx context.Context, outcomes []outcome) {
	if !m.s.AutoPrune || !m.forgetAll(ctx, outcomes) {
		return
	}
	if m.count(outcomes, outcomeComplete) < len(m.projects) {
		m.d.Log.Warn().Msg("prune skipped because a project failed or is incomplete")
		return
	}
	m.prune(ctx)
}

// forgetAll runs forget for the projects with a retention policy and a complete snapshot
// and reports whether there was one.
func (m *Manager) forgetAll(ctx context.Context, outcomes []outcome) bool {
	forgot := false
	for i, p := range m.projects {
		if p.Settings.RetentionPolicy == "" {
			continue
		}
		switch outcomes[i] {
		case outcomeComplete:
			forgot = true
			m.forget(ctx, p.Settings)
		case outcomeIncomplete:
			m.d.Log.Warn().Str("project", p.Settings.Name).
				Msg("forget skipped because the snapshot is incomplete")
		}
	}
	return forgot
}

// forget applies the retention policy of one project; a failure is only logged.
func (m *Manager) forget(ctx context.Context, s ProjectSettings) {
	log := m.d.Log.With().Str("project", s.Name).Logger()
	if err := m.d.Restic.Forget(ctx, s.Name, s.RetentionPolicy, s.ForgetOptions); err != nil {
		log.Error().Err(err).Msg("forget failed")
		return
	}
	log.Info().Str("retention_policy", s.RetentionPolicy).Msg("forget completed")
}

// prune runs prune and unlock for the whole repository; failures are only logged.
func (m *Manager) prune(ctx context.Context) {
	if err := m.d.Restic.Prune(ctx, m.s.PruneOptions); err != nil {
		m.d.Log.Error().Err(err).Msg("prune failed")
	} else {
		m.d.Log.Info().Msg("prune completed")
	}
	if err := m.d.Restic.Unlock(ctx, m.s.PruneOptions); err != nil {
		m.d.Log.Error().Err(err).Msg("unlock failed")
	}
}
