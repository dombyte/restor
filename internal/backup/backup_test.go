package backup

import (
	"context"
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

func (f *fixture) manager(t *testing.T, s Settings, projects ...Project) *Manager {
	t.Helper()
	m, err := NewManager(s, projects, Deps{
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
	f.hooks.EXPECT().Run(mock.Anything, name, "arg").
		RunAndReturn(func(context.Context, string, ...string) ([]byte, error) {
			f.rec.add("hook " + name)
			return nil, err
		}).Once()
}

// services builds a Services mock that records stop/start.
func (f *fixture) services(t *testing.T, key string, names ...string) *mocks.MockServices {
	t.Helper()
	s := mocks.NewMockServices(t)
	s.EXPECT().LockKey().Return(key).Maybe()
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

func project(name string, s Services) Project {
	return Project{Settings: ProjectSettings{
		Name: name, Sources: []string{"/data/" + name}, StopServices: true,
		StopTimeout: 10 * time.Second, StartTimeout: 20 * time.Second,
		PreBackupCmd: "pre arg", PostBackupCmd: "post arg",
	}, Services: s}
}

func TestNewManager_MissingDependencies(t *testing.T) {
	t.Parallel()
	_, err := NewManager(Settings{}, nil, Deps{})
	require.ErrorIs(t, err, ErrMissingDependency)

	f := newFixture(t)
	_, err = NewManager(Settings{}, []Project{{Settings: ProjectSettings{Name: "x"}}},
		Deps{Restic: f.restic, Locker: f.locker, Hooks: f.hooks})
	require.ErrorIs(t, err, ErrMissingDependency)
	assert.Contains(t, err.Error(), "Projects[x].Services")
}

func TestRunProject_Sequence(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	svc := f.services(t, "key", "web")
	f.expectLock("key")
	f.expectHook("pre", nil)
	f.expectStop(svc, true, nil)
	f.expectBackup("web", nil)
	f.expectStart(svc, true, nil)
	f.expectHook("post", nil)

	require.NoError(t, f.manager(t, Settings{}).runProject(context.Background(),
		project("web", svc)))

	assert.Equal(t, []string{
		"lock key", "hook pre", "stop", "backup web", "start", "hook post", "release key",
	}, f.rec.list())
}

func TestRunProject_FailureHandling(t *testing.T) {
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
			name: "backup fails: services restarted, no post hook",
			setup: func(f *fixture, s *mocks.MockServices) {
				f.expectHook("pre", nil)
				f.expectStop(s, true, nil)
				f.expectBackup("web", assert.AnError)
				f.expectStart(s, true, nil)
			},
			wantErr: true,
			want:    []string{"lock key", "hook pre", "stop", "backup web", "start", "release key"},
		},
		{
			name: "restart fails: project fails",
			setup: func(f *fixture, s *mocks.MockServices) {
				f.expectHook("pre", nil)
				f.expectStop(s, true, nil)
				f.expectBackup("web", nil)
				f.expectStart(s, false, assert.AnError)
			},
			wantErr: true,
			want:    []string{"lock key", "hook pre", "stop", "backup web", "start", "release key"},
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

			err := f.manager(t, Settings{}).runProject(context.Background(), project("web", svc))

			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.want, f.rec.list())
		})
	}
}

func TestRunProject_StopServicesDisabled(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	svc := mocks.NewMockServices(t)
	svc.EXPECT().LockKey().Return("key")
	f.expectLock("key")
	f.expectBackup("web", nil)
	p := project("web", svc)
	p.Settings.StopServices = false
	p.Settings.PreBackupCmd, p.Settings.PostBackupCmd = "", " "

	require.NoError(t, f.manager(t, Settings{}).runProject(context.Background(), p))
	assert.Equal(t, []string{"lock key", "backup web", "release key"}, f.rec.list())
}

func TestRunProject_NoServices(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	svc := f.services(t, "noop") // resolves to no services: nothing to stop or start
	f.expectLock("noop")
	f.expectBackup("files", nil)
	p := project("files", svc)
	p.Settings.PreBackupCmd, p.Settings.PostBackupCmd = "", ""

	require.NoError(t, f.manager(t, Settings{}).runProject(context.Background(), p))
	assert.Equal(t, []string{"lock noop", "backup files", "release noop"}, f.rec.list())
}

func TestRunProject_RestartsOnlyRunningServices(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	svc := mocks.NewMockServices(t)
	svc.EXPECT().LockKey().Return("key")
	svc.EXPECT().Running(mock.Anything, []string{"web", "db", "job"}).
		Return([]string{"web"}, nil).Once()
	svc.EXPECT().Stop(mock.Anything, []string{"web"}, 10*time.Second).Return(true, nil).Once()
	svc.EXPECT().Start(mock.Anything, []string{"web"}, 20*time.Second).Return(true, nil).Once()
	f.expectLock("key")
	f.expectBackup("web", nil)
	p := project("web", svc)
	p.Settings.Services = []string{"web", "db", "job"}
	p.Settings.PreBackupCmd, p.Settings.PostBackupCmd = "", ""

	require.NoError(t, f.manager(t, Settings{}).runProject(context.Background(), p))
}

func TestRunProject_ServicesFail(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	svc := mocks.NewMockServices(t)
	svc.EXPECT().LockKey().Return("key")
	svc.EXPECT().Running(mock.Anything, mock.Anything).Return(nil, assert.AnError)
	f.expectLock("key")
	p := project("web", svc)
	p.Settings.PreBackupCmd = ""

	err := f.manager(t, Settings{}).runProject(context.Background(), p)

	require.ErrorIs(t, err, assert.AnError)
	assert.Equal(t, []string{"lock key", "release key"}, f.rec.list())
}

func TestRunProject_Locked(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	svc := mocks.NewMockServices(t)
	svc.EXPECT().LockKey().Return("key")
	f.locker.EXPECT().Lock("key").Return(nil, assert.AnError)

	err := f.manager(t, Settings{}).runProject(context.Background(), project("web", svc))

	require.ErrorIs(t, err, assert.AnError)
}

func TestRunProject_ReleaseFails(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	svc := f.services(t, "key")
	f.locker.EXPECT().Lock("key").Return(func() error { return assert.AnError }, nil)
	f.restic.EXPECT().Backup(mock.Anything, "web", mock.Anything, mock.Anything).
		Return("", nil)
	p := project("web", svc)
	p.Settings.PreBackupCmd, p.Settings.PostBackupCmd = "", ""

	require.NoError(t, f.manager(t, Settings{}).runProject(context.Background(), p),
		"a failed release is only logged")
}

func TestRunProject_RestartAfterCancel(t *testing.T) {
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
	p := project("web", svc)
	p.Settings.PreBackupCmd = ""

	err := f.manager(t, Settings{}).runProject(ctx, p)

	require.ErrorIs(t, err, context.Canceled)
}
