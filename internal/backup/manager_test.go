package backup

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// fileJob is a project without services or hooks.
func fileJob(f *fixture, t *testing.T, name, policy string) Job {
	t.Helper()
	p := job(name, f.services(t, name))
	p.Settings.Pre, p.Settings.Post = "", ""
	p.Settings.ForgetArgs = strings.Fields(policy)
	return p
}

func TestManager_RunSequential(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for _, name := range []string{"b", "a", "c"} {
		f.expectLock(name)
		f.expectBackup(name, nil)
	}
	m := f.manager(t, Settings{Concurrency: 1},
		fileJob(f, t, "b", ""), fileJob(f, t, "a", ""), fileJob(f, t, "c", ""))

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
	f.restic.EXPECT().Forget(mock.Anything, "a", []string{"--keep-last", "1"}).
		Return(nil).Once()
	m := f.manager(t, Settings{Prune: true},
		fileJob(f, t, "a", "--keep-last 1"), fileJob(f, t, "b", "--keep-last 1"),
		fileJob(f, t, "c", ""))

	err := m.Run(context.Background())

	require.ErrorIs(t, err, ErrJobsFailed)
	assert.Contains(t, err.Error(), "1 of 3")
	assert.Len(t, f.rec.list(), 9)
	// Only the backed-up project a is forgotten: b failed, and prune/unlock are skipped
	// (the restic mock fails the test on any unexpected call).
}

func TestManager_Maintenance(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for _, name := range []string{"a", "b", "c"} {
		f.expectLock(name)
		f.expectBackup(name, nil)
	}
	f.restic.EXPECT().Forget(mock.Anything, "a", []string{"--keep-last", "1"}).
		Return(assert.AnError).Once()
	f.restic.EXPECT().Forget(mock.Anything, "c", []string{"--keep-daily", "7"}).
		Return(nil).Once()
	f.restic.EXPECT().Prune(mock.Anything).Return(assert.AnError).Once()
	f.restic.EXPECT().Unlock(mock.Anything).Return(assert.AnError).Once()
	m := f.manager(t, Settings{Concurrency: 1, Prune: true},
		fileJob(f, t, "a", "--keep-last 1"), fileJob(f, t, "b", ""),
		fileJob(f, t, "c", "--keep-daily 7"))

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
		{name: "prune off: forget only", settings: Settings{}, policy: "--keep-last 1"},
		{name: "no retention policy", settings: Settings{Concurrency: 1, Prune: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.expectLock("a")
			f.expectBackup("a", nil)
			if tt.policy != "" {
				f.restic.EXPECT().Forget(mock.Anything, "a", []string{"--keep-last", "1"}).
					Return(nil).Once()
			}
			m := f.manager(t, tt.settings, fileJob(f, t, "a", tt.policy))

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
	m := f.manager(t, Settings{Concurrency: 1, Prune: true},
		fileJob(f, t, "a", "--keep-last 1"), fileJob(f, t, "b", "--keep-last 1"))

	err := m.Run(ctx)

	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, []string{"lock a", "release a"}, f.rec.list(),
		"b is not started after the cancel, no maintenance")
}

func TestManager_IncompleteSnapshotKeepsHistory(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		id   string
		err  error
	}{
		{name: "unreadable files", id: "abc", err: fmt.Errorf("w: %w", incompleteError{})},
		{name: "no snapshot ID", id: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.expectLock("a")
			f.expectLock("b")
			f.expectBackup("a", nil)
			f.restic.EXPECT().Backup(mock.Anything, "b", mock.Anything, mock.Anything).
				Return(tt.id, tt.err).Once()
			f.restic.EXPECT().Forget(mock.Anything, "a", []string{"--keep-last", "1"}).
				Return(nil).Once()
			m := f.manager(t, Settings{Concurrency: 1, Prune: true},
				fileJob(f, t, "a", "--keep-last 1"), fileJob(f, t, "b", "--keep-last 1"))

			require.NoError(t, m.Run(context.Background()), "an incomplete project succeeds")
			// b is not forgotten and prune/unlock are skipped (the restic mock fails the
			// test on any unexpected call).
		})
	}
}

func TestRunJob_ReportsCompleteness(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.expectLock("a")
	f.expectBackup("a", nil)
	r := f.manager(t, Settings{}).runJob(context.Background(), fileJob(f, t, "a", ""))
	require.NoError(t, r.err)
	assert.Equal(t, outcomeComplete, r.outcome)
	assert.Equal(t, "abc", r.snapshotID)
}

func TestManager_ShutdownTimeout(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	assert.Equal(t, shutdownGrace, f.manager(t, Settings{}).ShutdownTimeout(),
		"no services to restart")

	slow := fileJob(f, t, "slow", "")
	slow.Settings.StartTimeout = 5 * time.Minute
	unstopped := fileJob(f, t, "unstopped", "")
	unstopped.Settings.StartTimeout, unstopped.Settings.StopServices = time.Hour, false
	jobs := []Job{slow, unstopped}
	for i := range 10 {
		jobs = append(jobs, fileJob(f, t, strconv.Itoa(i), "")) // 20 s each
	}
	m := f.manager(t, Settings{}, jobs...)

	assert.Equal(t, shutdownGrace+5*time.Minute+restartGrace, m.ShutdownTimeout(),
		"the largest start timeout counts, not the sum")
}

func TestManager_RunOrder(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	jobs := map[string]Job{}
	for _, name := range []string{"a", "b", "c", "d"} {
		f.expectLock(name)
		f.expectBackup(name, nil)
		jobs[name] = fileJob(f, t, name, "")
	}
	jobs["a"] = after(jobs["a"], "c")      // a waits for c
	jobs["b"] = after(jobs["b"], "a", "x") // b waits for a; x is not in this run
	m := f.manager(t, Settings{Concurrency: 1}, jobs["a"], jobs["b"], jobs["c"], jobs["d"])

	require.NoError(t, m.Run(context.Background()))
	assert.Equal(t, []string{"backup c", "backup a", "backup b", "backup d"},
		backups(f.rec.list()), "the first job whose predecessors finished starts first")
}

func TestManager_RunAfterFailedJob(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.expectLock("a")
	f.expectLock("b")
	f.expectBackup("a", assert.AnError)
	f.expectBackup("b", nil)
	m := f.manager(t, Settings{}, fileJob(f, t, "a", ""), after(fileJob(f, t, "b", ""), "a"))

	require.ErrorIs(t, m.Run(context.Background()), ErrJobsFailed)
	assert.Equal(t, []string{"backup a", "backup b"}, backups(f.rec.list()),
		"after only orders: b runs although a failed")
}

func TestManager_RunFailureHooks(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.expectLock("a")
	f.expectLock("b")
	f.expectBackup("a", assert.AnError)
	f.expectBackup("b", nil)
	var jobEnv, runEnv []string
	f.hooks.EXPECT().RunEnv(mock.Anything, mock.Anything, "/bin/job-failed", "x").
		RunAndReturn(func(_ context.Context, env []string, _ string, _ ...string) (
			[]byte, error,
		) {
			jobEnv = env
			return nil, assert.AnError // only logged
		}).Once()
	f.hooks.EXPECT().RunEnv(mock.Anything, mock.Anything, "/bin/run-failed").
		RunAndReturn(func(_ context.Context, env []string, _ string, _ ...string) (
			[]byte, error,
		) {
			runEnv = env
			return nil, assert.AnError // only logged
		}).Once()
	a := fileJob(f, t, "a", "")
	a.Settings.OnFailure = "/bin/job-failed x"
	b := fileJob(f, t, "b", "")
	b.Settings.OnFailure = "/bin/job-failed x" // b succeeds: not called
	m := f.manager(t, Settings{OnFailure: "/bin/run-failed"}, a, b)

	require.ErrorIs(t, m.Run(context.Background()), ErrJobsFailed)
	assert.Equal(t, []string{
		"RESTOR_JOB=a", "RESTOR_HOOK=on_failure", "RESTOR_STATUS=failed",
		"RESTOR_SNAPSHOT_ID=", "RESTOR_ERROR=backup: a: " + assert.AnError.Error(),
	}, jobEnv)
	assert.Equal(t, []string{
		"RESTOR_HOOK=on_failure", "RESTOR_STATUS=failed", "RESTOR_JOBS=a,b",
		"RESTOR_SUCCEEDED_JOBS=b", "RESTOR_INCOMPLETE_JOBS=", "RESTOR_FAILED_JOBS=a",
		"RESTOR_SNAPSHOT_IDS=b=abc", "RESTOR_ERROR=backup: a: " + assert.AnError.Error(),
	}, runEnv)
}

func TestManager_RunFailureHookAfterPruneFailure(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.expectLock("a")
	f.expectBackup("a", nil)
	f.restic.EXPECT().Forget(mock.Anything, "a", mock.Anything).Return(nil).Once()
	f.restic.EXPECT().Prune(mock.Anything).Return(assert.AnError).Once()
	f.restic.EXPECT().Unlock(mock.Anything).Return(nil).Once()
	f.hooks.EXPECT().RunEnv(mock.Anything, []string{
		"RESTOR_HOOK=on_failure", "RESTOR_STATUS=failed", "RESTOR_JOBS=a",
		"RESTOR_SUCCEEDED_JOBS=a", "RESTOR_INCOMPLETE_JOBS=", "RESTOR_FAILED_JOBS=",
		"RESTOR_SNAPSHOT_IDS=a=abc", "RESTOR_ERROR=" + assert.AnError.Error(),
	}, "/bin/run-failed").Return(nil, nil).Once()
	m := f.manager(t, Settings{Prune: true, OnFailure: "/bin/run-failed"},
		fileJob(f, t, "a", "--keep-last 1"))

	require.NoError(t, m.Run(context.Background()), "a failed prune does not fail the run")
}

func TestManager_RunFailureHookSkippedAfterCancel(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	f.expectLock("a")
	f.restic.EXPECT().Backup(mock.Anything, "a", mock.Anything, mock.Anything).
		RunAndReturn(func(context.Context, string, []string, []string) (string, error) {
			cancel()
			return "", context.Canceled
		})
	a := fileJob(f, t, "a", "")
	a.Settings.OnFailure = "/bin/job-failed"
	m := f.manager(t, Settings{Post: "/bin/run-post", OnFailure: "/bin/run-failed"}, a)

	require.ErrorIs(t, m.Run(ctx), ErrJobsFailed)
	// The hooks mock fails the test on any call.
}

func TestManager_RunPostHook(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		errB error
		want []string
	}{
		{name: "success", want: []string{
			"RESTOR_HOOK=post", "RESTOR_STATUS=success", "RESTOR_JOBS=a,b",
			"RESTOR_SUCCEEDED_JOBS=a,b", "RESTOR_INCOMPLETE_JOBS=", "RESTOR_FAILED_JOBS=",
			"RESTOR_SNAPSHOT_IDS=a=abc,b=abc",
		}},
		{name: "incomplete", errB: incompleteError{}, want: []string{
			"RESTOR_HOOK=post", "RESTOR_STATUS=incomplete", "RESTOR_JOBS=a,b",
			"RESTOR_SUCCEEDED_JOBS=a", "RESTOR_INCOMPLETE_JOBS=b", "RESTOR_FAILED_JOBS=",
			"RESTOR_SNAPSHOT_IDS=a=abc,b=abc",
		}},
		{name: "unchanged has no snapshot", errB: unchangedError{}, want: []string{
			"RESTOR_HOOK=post", "RESTOR_STATUS=success", "RESTOR_JOBS=a,b",
			"RESTOR_SUCCEEDED_JOBS=a,b", "RESTOR_INCOMPLETE_JOBS=", "RESTOR_FAILED_JOBS=",
			"RESTOR_SNAPSHOT_IDS=a=abc",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.expectLock("a")
			f.expectLock("b")
			f.expectBackup("a", nil)
			f.expectBackup("b", tt.errB)
			f.hooks.EXPECT().RunEnv(mock.Anything, tt.want, "/bin/run-post", "x").
				Return(nil, assert.AnError).Once() // only logged
			m := f.manager(t, Settings{Post: "/bin/run-post x", OnFailure: "/bin/run-failed"},
				fileJob(f, t, "a", ""), fileJob(f, t, "b", ""))

			require.NoError(t, m.Run(context.Background()))
			// on_failure does not run: the hooks mock fails the test on any other call.
		})
	}
}

func TestManager_RunPostHookBeforeOnFailure(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.expectLock("a")
	f.expectBackup("a", assert.AnError)
	f.hooks.EXPECT().RunEnv(mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, env []string, name string, _ ...string) (
			[]byte, error,
		) {
			f.rec.add(name + " " + env[0] + " " + env[1])
			return nil, nil
		}).Twice()
	m := f.manager(t, Settings{Post: "/bin/run-post", OnFailure: "/bin/run-failed"},
		fileJob(f, t, "a", ""))

	require.ErrorIs(t, m.Run(context.Background()), ErrJobsFailed)
	assert.Equal(t, []string{
		"lock a", "backup a", "release a",
		"/bin/run-post RESTOR_HOOK=post RESTOR_STATUS=failed",
		"/bin/run-failed RESTOR_HOOK=on_failure RESTOR_STATUS=failed",
	}, f.rec.list(), "post runs first, as in a job")
}

// after returns j with after set.
func after(j Job, names ...string) Job {
	j.Settings.After = names
	return j
}

// backups returns the backup calls of calls.
func backups(calls []string) []string {
	var out []string
	for _, c := range calls {
		if strings.HasPrefix(c, "backup ") {
			out = append(out, c)
		}
	}
	return out
}
