package backup

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// fileProject is a project without services or hooks.
func fileProject(f *fixture, t *testing.T, name, policy string) Project {
	t.Helper()
	p := project(name, f.services(t, name))
	p.Settings.PreBackupCmd, p.Settings.PostBackupCmd = "", ""
	p.Settings.RetentionPolicy = policy
	return p
}

func TestManager_RunSequential(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for _, name := range []string{"b", "a", "c"} {
		f.expectLock(name)
		f.expectBackup(name, nil)
	}
	m := f.manager(t, Settings{},
		fileProject(f, t, "b", ""), fileProject(f, t, "a", ""), fileProject(f, t, "c", ""))

	require.NoError(t, m.Run(context.Background()))
	assert.Equal(t, []string{
		"lock b", "backup b", "release b", "lock a", "backup a", "release a",
		"lock c", "backup c", "release c",
	}, f.rec.list(), "projects run in the given order")
}

func TestManager_RunParallel(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for _, name := range []string{"a", "b", "c"} {
		f.expectLock(name)
	}
	f.expectBackup("a", nil)
	f.expectBackup("b", assert.AnError)
	f.expectBackup("c", nil)
	f.restic.EXPECT().Forget(mock.Anything, "a", "--keep-last 1", []string(nil)).
		Return(nil).Once()
	m := f.manager(t, Settings{Parallel: true, AutoPrune: true},
		fileProject(f, t, "a", "--keep-last 1"), fileProject(f, t, "b", "--keep-last 1"),
		fileProject(f, t, "c", ""))

	err := m.Run(context.Background())

	require.ErrorIs(t, err, ErrProjectsFailed)
	assert.Contains(t, err.Error(), "1 of 3")
	assert.Len(t, f.rec.list(), 9)
	// Only the backed-up project a is forgotten: b failed, and prune/unlock are skipped
	// (the restic mock fails the test on any unexpected call).
}

func TestManager_Maintenance(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	opts := []string{"--cache-dir=/c"}
	for _, name := range []string{"a", "b", "c"} {
		f.expectLock(name)
		f.expectBackup(name, nil)
	}
	f.restic.EXPECT().Forget(mock.Anything, "a", "--keep-last 1", []string(nil)).
		Return(assert.AnError).Once()
	f.restic.EXPECT().Forget(mock.Anything, "c", "--keep-daily 7", []string(nil)).
		Return(nil).Once()
	f.restic.EXPECT().Prune(mock.Anything, opts).Return(assert.AnError).Once()
	f.restic.EXPECT().Unlock(mock.Anything, opts).Return(assert.AnError).Once()
	m := f.manager(t, Settings{AutoPrune: true, PruneOptions: opts},
		fileProject(f, t, "a", "--keep-last 1"), fileProject(f, t, "b", ""),
		fileProject(f, t, "c", "--keep-daily 7"))

	require.NoError(t, m.Run(context.Background()),
		"forget, prune and unlock failures do not fail the run")
}

func TestManager_MaintenanceSkipped(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		settings Settings
		policy   string
	}{
		{name: "auto_prune off", settings: Settings{}, policy: "--keep-last 1"},
		{name: "no retention policy", settings: Settings{AutoPrune: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.expectLock("a")
			f.expectBackup("a", nil)
			m := f.manager(t, tt.settings, fileProject(f, t, "a", tt.policy))

			require.NoError(t, m.Run(context.Background()))
			// The restic mock fails the test on any unexpected Forget/Prune/Unlock.
		})
	}
}

func TestManager_RunCancelled(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	f.expectLock("a")
	f.restic.EXPECT().Backup(mock.Anything, "a", mock.Anything, mock.Anything).
		RunAndReturn(func(context.Context, string, []string, []string) (string, error) {
			cancel()
			return "id", nil
		})
	m := f.manager(t, Settings{AutoPrune: true},
		fileProject(f, t, "a", "--keep-last 1"), fileProject(f, t, "b", "--keep-last 1"))

	err := m.Run(ctx)

	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, []string{"lock a", "release a"}, f.rec.list(),
		"b is not started after the cancel, no maintenance")
}
