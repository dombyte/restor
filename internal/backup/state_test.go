package backup

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/dombyte/restor/internal/backup/mocks"
)

func TestRunJob_RecordsStoppedServices(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	svc := f.services(t, "key", "web", "db")
	f.expectLock("key")
	svc.EXPECT().Stop(mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(context.Context, []string, time.Duration) (bool, error) {
			assert.Equal(t, []string{"web", "db"}, f.state.get("web"),
				"recorded before the stop")
			return true, nil
		}).Once()
	f.expectBackup("web", nil)
	f.expectStart(svc, true, nil)
	j := job("web", svc)
	j.Settings.Pre, j.Settings.Post = "", ""

	require.NoError(t, errOf(f.manager(t, Settings{}).runJob(context.Background(), j)))
	assert.Nil(t, f.state.get("web"), "removed once the services run again")
}

func TestRunJob_KeepsRecordWhenStartFails(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	svc := f.services(t, "key", "web")
	f.expectLock("key")
	f.expectStop(svc, true, nil)
	f.expectBackup("web", nil)
	f.expectStart(svc, false, nil) // not running within the start timeout
	j := job("web", svc)
	j.Settings.Pre, j.Settings.Post = "", ""

	r := f.manager(t, Settings{}).runJob(context.Background(), j)

	require.ErrorIs(t, r.err, ErrNotStarted)
	assert.Equal(t, outcomeFailed, r.outcome)
	assert.Equal(t, outcomeComplete, r.backup, "the snapshot itself is fine")
	assert.Equal(t, "abc", r.snapshotID)
	assert.Equal(t, []string{"web"}, f.state.get("web"), "the next run tries again")
}

func TestManager_RecoversStoppedServices(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.state.records["web"] = []string{"app"}
	svc := f.services(t, "key")
	f.locker.EXPECT().Lock("key").RunAndReturn(func(string) (func() error, error) {
		f.rec.add("lock")
		return func() error { f.rec.add("release"); return nil }, nil
	}).Twice()
	svc.EXPECT().Start(mock.Anything, []string{"app"}, mock.Anything).
		RunAndReturn(func(context.Context, []string, time.Duration) (bool, error) {
			f.rec.add("recover app")
			return true, nil
		}).Once()
	f.expectBackup("web", nil)
	var runEnv []string
	f.hooks.EXPECT().RunEnv(mock.Anything, mock.Anything, "/bin/run-post").
		RunAndReturn(func(_ context.Context, env []string, _ string, _ ...string) (
			[]byte, error,
		) {
			runEnv = env
			return nil, nil
		}).Once()
	j := job("web", svc)
	j.Settings.Pre, j.Settings.Post = "", ""

	require.NoError(t, f.manager(t, Settings{Post: "/bin/run-post"}, j).Run(
		context.Background()))
	assert.Equal(t, []string{
		"lock", "recover app", "release", "lock", "backup web", "release",
	}, f.rec.list(), "recovered under the job's lock, before the jobs")
	assert.Nil(t, f.state.get("web"))
	assert.Contains(t, runEnv, "RESTOR_STATUS=success", "a recovery is a warning")
	assert.True(t, slices.ContainsFunc(runEnv, func(kv string) bool {
		return strings.HasPrefix(kv, "RESTOR_WARNINGS=web: an earlier run stopped services")
	}), runEnv)
}

func TestManager_RecoveryLeavesALockedJobAlone(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.state.records["web"] = []string{"app"}
	f.locker.EXPECT().Lock("key").Return(nil, assert.AnError).Twice()
	j := job("web", f.services(t, "key"))

	err := f.manager(t, Settings{}, j).Run(context.Background())

	require.ErrorIs(t, err, ErrJobsFailed, "the job itself cannot lock either")
	assert.Equal(t, []string{"app"}, f.state.get("web"), "the record belongs to the other run")
}

func TestManager_RecoveryFails(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.state.records["web"] = []string{"app"}
	svc := f.services(t, "key")
	f.expectLock("key")
	f.expectLock("key")
	svc.EXPECT().Start(mock.Anything, []string{"app"}, mock.Anything).
		Return(false, assert.AnError).Once()
	f.expectBackup("web", nil)
	var runEnv []string
	f.hooks.EXPECT().RunEnv(mock.Anything, mock.Anything, "/bin/run-failed").
		RunAndReturn(func(_ context.Context, env []string, _ string, _ ...string) (
			[]byte, error,
		) {
			runEnv = env
			return nil, nil
		}).Once()
	j := job("web", svc)
	j.Settings.Pre, j.Settings.Post = "", ""

	require.NoError(t, f.manager(t, Settings{OnFailure: "/bin/run-failed"}, j).Run(
		context.Background()), "the backup still ran")
	assert.Contains(t, runEnv, "RESTOR_STATUS=failed")
	assert.Equal(t, []string{"app"}, f.state.get("web"), "kept for the next run")
}

func TestManager_RecoveryCannotReadRecord(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	st := mocks.NewMockState(t)
	st.EXPECT().Load("web").Return(nil, assert.AnError).Once()
	f.expectLock("web")
	f.expectBackup("web", nil)
	d := f.deps()
	d.State = st
	m, err := NewManager(Settings{}, []Job{fileJob(f, t, "web", "")}, d)
	require.NoError(t, err)

	require.NoError(t, m.Run(context.Background()), "the jobs still run")
}
