package backup

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/dombyte/restor/internal/backup/mocks"
)

// recorder collects the calls of all fakes in order.
type recorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *recorder) add(call string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, call)
}

func (r *recorder) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

type fixture struct {
	rec    *recorder
	restic *mocks.MockRestic
	locker *mocks.MockLocker
	hooks  *mocks.MockRunner
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{
		rec:    &recorder{},
		restic: mocks.NewMockRestic(t),
		locker: mocks.NewMockLocker(t),
		hooks:  mocks.NewMockRunner(t),
	}
	return f
}

func (f *fixture) manager(t *testing.T, s Settings, jobs ...Job) *Manager {
	t.Helper()
	m, err := NewManager(s, jobs, Deps{
		Restic: f.restic, Locker: f.locker, Hooks: f.hooks, Log: zerolog.Nop(),
	})
	require.NoError(t, err)
	return m
}

// expectLock expects the lock for key and records lock and release.
func (f *fixture) expectLock(key string) {
	f.locker.EXPECT().Lock(key).RunAndReturn(func(string) (func() error, error) {
		f.rec.add("lock " + key)
		return func() error { f.rec.add("release " + key); return nil }, nil
	}).Once()
}

func (f *fixture) expectBackup(tag string, err error) {
	f.restic.EXPECT().Backup(mock.Anything, tag, mock.Anything, mock.Anything).
		RunAndReturn(func(context.Context, string, []string, []string) (string, error) {
			f.rec.add("backup " + tag)
			return "abc", err
		}).Once()
}

func (f *fixture) expectHook(name string, err error) {
	f.hooks.EXPECT().RunEnv(mock.Anything, mock.Anything, name, "arg").
		RunAndReturn(func(context.Context, []string, string, ...string) ([]byte, error) {
			f.rec.add("hook " + name)
			return nil, err
		}).Once()
}

// services builds a Services mock that records stop/start.
func (f *fixture) services(t *testing.T, key string, names ...string) *mocks.MockServices {
	t.Helper()
	s := mocks.NewMockServices(t)
	s.EXPECT().LockKeys().Return([]string{key}).Maybe()
	s.EXPECT().Running(mock.Anything, mock.Anything).Return(names, nil).Maybe()
	return s
}

func (f *fixture) expectStop(s *mocks.MockServices, ok bool, err error) {
	s.EXPECT().Stop(mock.Anything, mock.Anything, 10*time.Second).
		RunAndReturn(func(context.Context, []string, time.Duration) (bool, error) {
			f.rec.add("stop")
			return ok, err
		}).Once()
}

func (f *fixture) expectStart(s *mocks.MockServices, ok bool, err error) {
	s.EXPECT().Start(mock.Anything, mock.Anything, 20*time.Second).
		RunAndReturn(func(ctx context.Context, _ []string, _ time.Duration) (bool, error) {
			f.rec.add("start")
			if _, has := ctx.Deadline(); !has {
				return false, assert.AnError // restart must be bounded
			}
			return ok, err
		}).Once()
}

// errOf returns the error of a job result.
func errOf(r result) error { return r.err }

// incompleteError is what restic returns for a snapshot without unreadable files.
type incompleteError struct{}

func (incompleteError) Error() string    { return "snapshot incomplete" }
func (incompleteError) Incomplete() bool { return true }

// unchangedError is what restic returns when --skip-if-unchanged skipped the snapshot.
type unchangedError struct{}

func (unchangedError) Error() string   { return "nothing changed" }
func (unchangedError) Unchanged() bool { return true }

func job(name string, s Services) Job {
	return Job{Settings: JobSettings{
		Name: name, Sources: []string{"/data/" + name}, StopServices: true,
		StopTimeout: 10 * time.Second, StartTimeout: 20 * time.Second,
		Pre: "pre arg", Post: "post arg",
	}, Services: s}
}

func TestNewManager_MissingDependencies(t *testing.T) {
	t.Parallel()
	_, err := NewManager(Settings{}, nil, Deps{})
	require.ErrorIs(t, err, ErrMissingDependency)

	f := newFixture(t)
	_, err = NewManager(Settings{}, []Job{{Settings: JobSettings{Name: "x"}}},
		Deps{Restic: f.restic, Locker: f.locker, Hooks: f.hooks})
	require.ErrorIs(t, err, ErrMissingDependency)
	assert.Contains(t, err.Error(), "Jobs[x].Services")
}

func TestRunJob_Sequence(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	svc := f.services(t, "key", "web")
	f.expectLock("key")
	f.expectHook("pre", nil)
	f.expectStop(svc, true, nil)
	f.expectBackup("web", nil)
	f.expectStart(svc, true, nil)
	f.expectHook("post", nil)

	require.NoError(t, errOf(f.manager(t, Settings{}).runJob(context.Background(),
		job("web", svc))))

	assert.Equal(t, []string{
		"lock key", "hook pre", "stop", "backup web", "start", "hook post", "release key",
	}, f.rec.list())
}

func TestRunJob_FailureHandling(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		setup   func(f *fixture, s *mocks.MockServices)
		wantErr bool
		want    []string
	}{
		{
			name: "pre-backup hook fails: services untouched",
			setup: func(f *fixture, _ *mocks.MockServices) {
				f.expectHook("pre", assert.AnError)
			},
			wantErr: true,
			want:    []string{"lock key", "hook pre", "release key"},
		},
		{
			name: "stop fails: backup runs anyway",
			setup: func(f *fixture, s *mocks.MockServices) {
				f.expectHook("pre", nil)
				f.expectStop(s, false, assert.AnError)
				f.expectBackup("web", nil)
				f.expectStart(s, true, nil)
				f.expectHook("post", nil)
			},
			want: []string{
				"lock key", "hook pre", "stop", "backup web", "start", "hook post",
				"release key",
			},
		},
		{
			name: "stop and start time out: warnings only",
			setup: func(f *fixture, s *mocks.MockServices) {
				f.expectHook("pre", nil)
				f.expectStop(s, false, nil)
				f.expectBackup("web", nil)
				f.expectStart(s, false, nil)
				f.expectHook("post", nil)
			},
			want: []string{
				"lock key", "hook pre", "stop", "backup web", "start", "hook post",
				"release key",
			},
		},
		{
			name: "backup fails: services restarted, post hook runs",
			setup: func(f *fixture, s *mocks.MockServices) {
				f.expectHook("pre", nil)
				f.expectStop(s, true, nil)
				f.expectBackup("web", assert.AnError)
				f.expectStart(s, true, nil)
				f.expectHook("post", nil)
			},
			wantErr: true,
			want: []string{
				"lock key", "hook pre", "stop", "backup web", "start", "hook post",
				"release key",
			},
		},
		{
			name: "incomplete snapshot: warning only, post hook runs",
			setup: func(f *fixture, s *mocks.MockServices) {
				f.expectHook("pre", nil)
				f.expectStop(s, true, nil)
				f.expectBackup("web", fmt.Errorf("wrapped: %w", incompleteError{}))
				f.expectStart(s, true, nil)
				f.expectHook("post", nil)
			},
			want: []string{
				"lock key", "hook pre", "stop", "backup web", "start", "hook post",
				"release key",
			},
		},
		{
			name: "restart fails: project fails, post hook runs",
			setup: func(f *fixture, s *mocks.MockServices) {
				f.expectHook("pre", nil)
				f.expectStop(s, true, nil)
				f.expectBackup("web", nil)
				f.expectStart(s, false, assert.AnError)
				f.expectHook("post", nil)
			},
			wantErr: true,
			want: []string{
				"lock key", "hook pre", "stop", "backup web", "start", "hook post",
				"release key",
			},
		},
		{
			name: "post-backup hook fails: project succeeds",
			setup: func(f *fixture, s *mocks.MockServices) {
				f.expectHook("pre", nil)
				f.expectStop(s, true, nil)
				f.expectBackup("web", nil)
				f.expectStart(s, true, nil)
				f.expectHook("post", assert.AnError)
			},
			want: []string{
				"lock key", "hook pre", "stop", "backup web", "start", "hook post",
				"release key",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			svc := f.services(t, "key", "web")
			f.expectLock("key")
			tt.setup(f, svc)

			err := errOf(f.manager(t, Settings{}).runJob(context.Background(), job("web", svc)))

			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.want, f.rec.list())
		})
	}
}

func TestRunJob_StopServicesDisabled(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	svc := mocks.NewMockServices(t)
	svc.EXPECT().LockKeys().Return([]string{"key"})
	f.expectLock("key")
	f.expectBackup("web", nil)
	p := job("web", svc)
	p.Settings.StopServices = false
	p.Settings.Pre, p.Settings.Post = "", " "

	require.NoError(t, errOf(f.manager(t, Settings{}).runJob(context.Background(), p)))
	assert.Equal(t, []string{"lock key", "backup web", "release key"}, f.rec.list())
}

func TestRunJob_NoServices(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	svc := f.services(t, "noop") // resolves to no services: nothing to stop or start
	f.expectLock("noop")
	f.expectBackup("files", nil)
	p := job("files", svc)
	p.Settings.Pre, p.Settings.Post = "", ""

	require.NoError(t, errOf(f.manager(t, Settings{}).runJob(context.Background(), p)))
	assert.Equal(t, []string{"lock noop", "backup files", "release noop"}, f.rec.list())
}

func TestRunJob_RestartsOnlyRunningServices(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	svc := mocks.NewMockServices(t)
	svc.EXPECT().LockKeys().Return([]string{"key"})
	svc.EXPECT().Running(mock.Anything, []string{"web", "db", "job"}).
		Return([]string{"web"}, nil).Once()
	svc.EXPECT().Stop(mock.Anything, []string{"web"}, 10*time.Second).Return(true, nil).Once()
	svc.EXPECT().Start(mock.Anything, []string{"web"}, 20*time.Second).Return(true, nil).Once()
	f.expectLock("key")
	f.expectBackup("web", nil)
	p := job("web", svc)
	p.Settings.Services = []string{"web", "db", "job"}
	p.Settings.Pre, p.Settings.Post = "", ""

	require.NoError(t, errOf(f.manager(t, Settings{}).runJob(context.Background(), p)))
}

func TestRunJob_StartAll(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		stop     bool
		running  []string
		startErr error
		started  bool
	}{
		{
			name: "starts the services that were down", stop: true, running: []string{"web"},
			started: true,
		},
		{name: "without stop", running: nil, started: true},
		{
			name: "failed start is a warning", stop: true, running: []string{"web"},
			startErr: assert.AnError,
		},
		{name: "start timeout is a warning", stop: true, running: []string{"web"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			svc := mocks.NewMockServices(t)
			svc.EXPECT().LockKeys().Return([]string{"key"})
			if tt.stop {
				svc.EXPECT().Running(mock.Anything, []string{"web", "db"}).
					Return(tt.running, nil).Once()
				svc.EXPECT().Stop(mock.Anything, tt.running, 10*time.Second).
					Return(true, nil).Once()
				svc.EXPECT().Start(mock.Anything, tt.running, 20*time.Second).
					Return(true, nil).Once()
			}
			svc.EXPECT().All(mock.Anything, []string{"web", "db"}).
				Return([]string{"web", "db"}, nil).Once()
			others := []string{"web", "db"}
			if tt.stop {
				others = []string{"db"}
			}
			svc.EXPECT().Start(mock.Anything, others, 20*time.Second).
				Return(tt.started, tt.startErr).Once()
			f.expectLock("key")
			f.expectBackup("web", nil)
			p := job("web", svc)
			p.Settings.Services = []string{"web", "db"}
			p.Settings.StopServices, p.Settings.StartAll = tt.stop, true
			p.Settings.Pre, p.Settings.Post = "", ""

			require.NoError(t, errOf(f.manager(t, Settings{}).runJob(context.Background(), p)))
		})
	}
}

func TestRunJob_StartAllListFails(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	svc := mocks.NewMockServices(t)
	svc.EXPECT().LockKeys().Return([]string{"key"})
	svc.EXPECT().All(mock.Anything, mock.Anything).Return(nil, assert.AnError).Once()
	f.expectLock("key")
	f.expectBackup("web", nil)
	p := job("web", svc)
	p.Settings.StopServices, p.Settings.StartAll = false, true
	p.Settings.Pre, p.Settings.Post = "", ""

	require.NoError(t, errOf(f.manager(t, Settings{}).runJob(context.Background(), p)),
		"a service that was down must not fail the backup")
}

func TestRunJob_StartAllSkippedAfterCancel(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	svc := f.services(t, "key", "web")
	ctx, cancel := context.WithCancel(context.Background())
	f.expectLock("key")
	f.expectStop(svc, true, nil)
	f.restic.EXPECT().Backup(mock.Anything, "web", mock.Anything, mock.Anything).
		RunAndReturn(func(context.Context, string, []string, []string) (string, error) {
			cancel()
			return "", context.Canceled
		})
	f.expectStart(svc, true, nil) // only the stopped service; All is never called
	p := job("web", svc)
	p.Settings.StartAll = true
	p.Settings.Pre, p.Settings.Post = "", ""

	err := errOf(f.manager(t, Settings{}).runJob(ctx, p))

	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, []string{"lock key", "stop", "start", "release key"}, f.rec.list())
}

func TestRunJob_ServicesFail(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	svc := mocks.NewMockServices(t)
	svc.EXPECT().LockKeys().Return([]string{"key"})
	svc.EXPECT().Running(mock.Anything, mock.Anything).Return(nil, assert.AnError)
	f.expectLock("key")
	f.expectHook("post", nil)
	p := job("web", svc)
	p.Settings.Pre = ""

	err := errOf(f.manager(t, Settings{}).runJob(context.Background(), p))

	require.ErrorIs(t, err, assert.AnError)
	assert.Equal(t, []string{"lock key", "hook post", "release key"}, f.rec.list())
}

func TestRunJob_Locked(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	svc := mocks.NewMockServices(t)
	svc.EXPECT().LockKeys().Return([]string{"key"})
	f.locker.EXPECT().Lock("key").Return(nil, assert.AnError)

	err := errOf(f.manager(t, Settings{}).runJob(context.Background(), job("web", svc)))

	require.ErrorIs(t, err, assert.AnError)
}

func TestRunJob_ReleaseFails(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	svc := f.services(t, "key")
	f.locker.EXPECT().Lock("key").Return(func() error { return assert.AnError }, nil)
	f.restic.EXPECT().Backup(mock.Anything, "web", mock.Anything, mock.Anything).
		Return("", nil)
	p := job("web", svc)
	p.Settings.Pre, p.Settings.Post = "", ""

	require.NoError(t, errOf(f.manager(t, Settings{}).runJob(context.Background(), p)),
		"a failed release is only logged")
}

func TestRunJob_RestartAfterCancel(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	svc := f.services(t, "key", "web")
	ctx, cancel := context.WithCancel(context.Background())
	f.expectLock("key")
	f.expectStop(svc, true, nil)
	f.restic.EXPECT().Backup(mock.Anything, "web", mock.Anything, mock.Anything).
		RunAndReturn(func(context.Context, string, []string, []string) (string, error) {
			cancel() // SIGTERM during the backup
			return "", context.Canceled
		})
	svc.EXPECT().Start(mock.Anything, []string{"web"}, 20*time.Second).
		RunAndReturn(func(ctx context.Context, _ []string, _ time.Duration) (bool, error) {
			return ctx.Err() == nil, ctx.Err()
		})
	f.hooks.EXPECT().RunEnv(mock.Anything, mock.Anything, "post", "arg").
		RunAndReturn(func(ctx context.Context, _ []string, _ string, _ ...string) ([]byte, error) {
			f.rec.add("hook post")
			return nil, ctx.Err()
		}).Once()
	p := job("web", svc)
	p.Settings.Pre, p.Settings.OnFailure = "", "/bin/job-failed"

	err := errOf(f.manager(t, Settings{}).runJob(ctx, p))

	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, []string{"lock key", "stop", "hook post", "release key"}, f.rec.list(),
		"the post hook undoes the pre hook, also after a cancel; on_failure is skipped")
}

func TestRunJob_LocksEveryKey(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	svc := mocks.NewMockServices(t)
	svc.EXPECT().LockKeys().Return([]string{"a", "b"})
	svc.EXPECT().Running(mock.Anything, mock.Anything).Return(nil, nil)
	f.expectLock("a")
	f.expectLock("b")
	f.expectBackup("web", nil)
	p := job("web", svc)
	p.Settings.Pre, p.Settings.Post = "", ""

	require.NoError(t, errOf(f.manager(t, Settings{}).runJob(context.Background(), p)))
	assert.Equal(t, []string{"lock a", "lock b", "backup web", "release b", "release a"},
		f.rec.list())
}

func TestRunJob_PartlyLocked(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	svc := mocks.NewMockServices(t)
	svc.EXPECT().LockKeys().Return([]string{"a", "b"})
	f.expectLock("a")
	f.locker.EXPECT().Lock("b").Return(nil, assert.AnError)

	err := errOf(f.manager(t, Settings{}).runJob(context.Background(), job("web", svc)))

	require.ErrorIs(t, err, assert.AnError)
	assert.Equal(t, []string{"lock a", "release a"}, f.rec.list(),
		"the locks taken before the failure are released")
}

func TestRunJob_HookEnvironment(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	svc := f.services(t, "key")
	f.expectLock("key")
	f.expectBackup("web", nil)
	envs := map[string][]string{}
	f.hooks.EXPECT().RunEnv(mock.Anything, mock.Anything, mock.Anything, "arg").
		RunAndReturn(func(_ context.Context, env []string, name string, _ ...string) (
			[]byte, error,
		) {
			envs[name] = env
			return nil, nil
		}).Twice()

	r := f.manager(t, Settings{}).runJob(context.Background(), job("web", svc))

	require.NoError(t, r.err)
	assert.Equal(t, []string{"RESTOR_JOB=web", "RESTOR_HOOK=pre"}, envs["pre"])
	assert.Equal(t, []string{
		"RESTOR_JOB=web", "RESTOR_HOOK=post", "RESTOR_STATUS=success",
		"RESTOR_SNAPSHOT_ID=abc",
	}, envs["post"])
}

func TestRunJob_FailedHookEnvironment(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.expectLock("key")
	f.expectBackup("web", assert.AnError)
	envs := map[string][]string{}
	f.hooks.EXPECT().RunEnv(mock.Anything, mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, env []string, name string, _ ...string) (
			[]byte, error,
		) {
			envs[name] = env
			return nil, nil
		}).Twice()
	j := job("web", f.services(t, "key"))
	j.Settings.Pre, j.Settings.Post, j.Settings.OnFailure = "", "post", "failed"

	r := f.manager(t, Settings{}).runJob(context.Background(), j)

	require.ErrorIs(t, r.err, assert.AnError)
	want := "RESTOR_ERROR=backup: web: " + assert.AnError.Error()
	assert.Contains(t, envs["post"], want, "post and on_failure get the same error")
	assert.Contains(t, envs["failed"], want)
}

func TestHookEnv_Incomplete(t *testing.T) {
	t.Parallel()
	assert.Equal(t, []string{
		"RESTOR_JOB=web", "RESTOR_HOOK=post", "RESTOR_STATUS=incomplete",
		"RESTOR_SNAPSHOT_ID=abc",
	}, hookEnv("post", "web", result{outcome: outcomeIncomplete, snapshotID: "abc"}))
}

func TestRunJob_BackupTimeout(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	svc := f.services(t, "key", "web")
	f.expectLock("key")
	f.expectStop(svc, true, nil)
	f.restic.EXPECT().Backup(mock.Anything, "web", mock.Anything, mock.Anything).
		RunAndReturn(func(ctx context.Context, _ string, _, _ []string) (string, error) {
			<-ctx.Done() // restic is killed when the timeout passes
			return "", ctx.Err()
		}).Once()
	f.expectStart(svc, true, nil)
	j := job("web", svc)
	j.Settings.Pre, j.Settings.Post = "", ""
	j.Settings.BackupTimeout = time.Nanosecond

	r := f.manager(t, Settings{}).runJob(context.Background(), j)

	require.ErrorIs(t, r.err, context.DeadlineExceeded)
	assert.Contains(t, r.err.Error(), "backup: web: backup_timeout of 1ns exceeded")
	assert.Equal(t, []string{"lock key", "stop", "start", "release key"}, f.rec.list(),
		"the services are started again")
}

func TestRunJob_UnchangedIsComplete(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.expectLock("key")
	f.restic.EXPECT().Backup(mock.Anything, "web", mock.Anything, mock.Anything).
		Return("", fmt.Errorf("w: %w", unchangedError{})).Once()
	j := job("web", f.services(t, "key"))
	j.Settings.Pre, j.Settings.Post = "", ""

	r := f.manager(t, Settings{}).runJob(context.Background(), j)

	require.NoError(t, r.err)
	assert.Equal(t, outcomeComplete, r.outcome, "forget and prune may run")
	assert.Empty(t, r.snapshotID)
}
