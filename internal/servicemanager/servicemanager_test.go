package servicemanager

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dombyte/restor/internal/servicemanager/mocks"
	"github.com/dombyte/restor/internal/util/clocktest"
)

func newDeps(t *testing.T) (Deps, *mocks.MockRunner, *clocktest.Clock) {
	t.Helper()
	r := mocks.NewMockRunner(t)
	c := clocktest.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	return Deps{Runner: r, Clock: c}, r, c
}

func TestConstructors_MissingDependencies(t *testing.T) {
	t.Parallel()
	d, _, _ := newDeps(t)
	tests := []struct {
		name string
		new  func() error
	}{
		{name: "compose without deps", new: func() error {
			_, err := NewCompose(ComposeSettings{Binary: "docker", File: "f"}, Deps{})
			return err
		}},
		{name: "compose without file", new: func() error {
			_, err := NewCompose(ComposeSettings{Binary: "docker"}, d)
			return err
		}},
		{name: "systemd without clock", new: func() error {
			_, err := NewSystemd(SystemdSettings{Units: []string{"a"}}, Deps{Runner: d.Runner})
			return err
		}},
		{name: "systemd without units", new: func() error {
			_, err := NewSystemd(SystemdSettings{}, d)
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.ErrorIs(t, tt.new(), ErrMissingDependency)
		})
	}
}

func TestWaitUntil(t *testing.T) {
	t.Parallel()
	clock := clocktest.New(time.Unix(0, 0))
	calls := 0
	ok, err := waitUntil(context.Background(), clock, 2*time.Second,
		func(context.Context) (bool, error) {
			calls++
			return calls == 3, nil
		})
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, 3, calls)
	assert.Equal(t, time.Unix(0, 0).Add(2*pollInterval), clock.Now())
}

func TestWaitUntil_Timeout(t *testing.T) {
	t.Parallel()
	clock := clocktest.New(time.Unix(0, 0))
	ok, err := waitUntil(context.Background(), clock, 2*time.Second,
		func(context.Context) (bool, error) { return false, nil })
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, time.Unix(2, 0), clock.Now())
}

func TestWaitUntil_Error(t *testing.T) {
	t.Parallel()
	_, err := waitUntil(context.Background(), clocktest.New(time.Unix(0, 0)), time.Second,
		func(context.Context) (bool, error) { return false, assert.AnError })
	require.ErrorIs(t, err, assert.AnError)
}

func TestWaitUntil_Cancelled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// A closed context and a ready timer race in select; either way the loop must stop
	// with the context error before the timeout.
	_, err := waitUntil(ctx, blockingClock{}, time.Hour,
		func(context.Context) (bool, error) { return false, nil })
	require.ErrorIs(t, err, context.Canceled)
}

// blockingClock never fires, so only the context can end a wait.
type blockingClock struct{}

func (blockingClock) Now() time.Time                       { return time.Unix(0, 0) }
func (blockingClock) After(time.Duration) <-chan time.Time { return nil }

func TestNoop(t *testing.T) {
	t.Parallel()
	n := NewNoop(NoopSettings{LockKey: "noop_files"})
	ctx := context.Background()

	assert.Equal(t, "noop_files", n.LockKey())
	services, err := n.Services(ctx, []string{"a"})
	require.NoError(t, err)
	assert.Empty(t, services)
	ok, err := n.Stop(ctx, []string{"a"}, time.Second)
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = n.Start(ctx, []string{"a"}, time.Second)
	require.NoError(t, err)
	assert.True(t, ok)
}
