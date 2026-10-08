package servicemanager

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/dombyte/restor/internal/servicemanager/mocks"
)

func newSystemd(t *testing.T, user bool) (*Systemd, *mocks.MockRunner) {
	t.Helper()
	d, r, _ := newDeps(t)
	s, err := NewSystemd(SystemdSettings{Units: []string{"a.service", "b.service"}, User: user}, d)
	require.NoError(t, err)
	return s, r
}

func expectSystemctl(r *mocks.MockRunner, args ...any) *mock.Call {
	return r.On("Run", append([]any{mock.Anything, "systemctl"}, args...)...)
}

func TestSystemd_LockKey(t *testing.T) {
	t.Parallel()
	s, _ := newSystemd(t, false)
	assert.Equal(t, "a.service,b.service", s.LockKey())
}

func TestSystemd_Running(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		requested []string
		states    string
		want      []string
		wantErr   error
	}{
		{name: "all by default", states: "active\ninactive\n", want: []string{"a.service"}},
		{
			name: "requested", requested: []string{"b.service"}, states: "activating\n",
			want: []string{"b.service"},
		},
		{name: "none up", states: "failed\ninactive\n"},
		{name: "unknown", requested: []string{"x.service"}, wantErr: errUnknownUnit},
		{name: "status fails", states: "active\n", wantErr: assert.AnError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, r := newSystemd(t, false)
			units := tt.requested
			if len(units) == 0 {
				units = []string{"a.service", "b.service"}
			}
			args := append([]any{"is-active"}, toAny(units)...)
			expectSystemctl(r, args...).Return([]byte(tt.states), assert.AnError).Maybe()

			got, err := s.Running(context.Background(), tt.requested)

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func toAny(list []string) []any {
	out := make([]any, len(list))
	for i, s := range list {
		out[i] = s
	}
	return out
}

func TestSystemd_StopUserScope(t *testing.T) {
	t.Parallel()
	s, r := newSystemd(t, true)
	expectSystemctl(r, "--user", "stop", "a.service").Return(nil, nil).Once()
	expectSystemctl(r, "--user", "is-active", "a.service").
		Return([]byte("deactivating\n"), assert.AnError).Once()
	expectSystemctl(r, "--user", "is-active", "a.service").
		Return([]byte("inactive\n"), assert.AnError).Once()

	ok, err := s.Stop(context.Background(), []string{"a.service"}, 5*time.Second)

	require.NoError(t, err)
	assert.True(t, ok)
}

func TestSystemd_StopNotLoaded(t *testing.T) {
	t.Parallel()
	s, r := newSystemd(t, false)
	expectSystemctl(r, "stop", "a.service").
		Return([]byte("Unit a.service not loaded."), assert.AnError).Once()
	expectSystemctl(r, "is-active", "a.service").Return([]byte("inactive"), nil).Once()

	ok, err := s.Stop(context.Background(), []string{"a.service"}, time.Second)

	require.NoError(t, err)
	assert.True(t, ok)
}

func TestSystemd_StopFails(t *testing.T) {
	t.Parallel()
	s, r := newSystemd(t, false)
	expectSystemctl(r, "stop", "a.service").Return([]byte("denied"), assert.AnError).Once()

	_, err := s.Stop(context.Background(), []string{"a.service"}, time.Second)

	require.ErrorIs(t, err, assert.AnError)
}

func TestSystemd_Start(t *testing.T) {
	t.Parallel()
	s, r := newSystemd(t, false)
	expectSystemctl(r, "start", "a.service", "b.service").Return(nil, nil).Once()
	expectSystemctl(r, "is-active", "a.service", "b.service").
		Return([]byte("active\nactivating\n"), assert.AnError).Once()
	expectSystemctl(r, "is-active", "a.service", "b.service").
		Return([]byte("active\nactive\n"), nil).Once()

	ok, err := s.Start(context.Background(), []string{"a.service", "b.service"}, time.Second)

	require.NoError(t, err)
	assert.True(t, ok)
}

func TestSystemd_StartTimeout(t *testing.T) {
	t.Parallel()
	s, r := newSystemd(t, false)
	expectSystemctl(r, "start", "a.service").Return(nil, nil).Once()
	expectSystemctl(r, "is-active", "a.service").Return([]byte("failed"), assert.AnError)

	ok, err := s.Start(context.Background(), []string{"a.service"}, time.Second)

	require.NoError(t, err)
	assert.False(t, ok)
}

func TestSystemd_StartFails(t *testing.T) {
	t.Parallel()
	s, r := newSystemd(t, false)
	expectSystemctl(r, "start", "a.service").Return(nil, assert.AnError).Once()

	_, err := s.Start(context.Background(), []string{"a.service"}, time.Second)

	require.ErrorIs(t, err, assert.AnError)
}

func TestSystemd_StatusUnexpectedOutput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want error
	}{
		{name: "command failed", err: assert.AnError, want: assert.AnError},
		{name: "wrong line count", want: errUnexpectedOutput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, r := newSystemd(t, false)
			expectSystemctl(r, "start", "a.service").Return(nil, nil).Once()
			expectSystemctl(r, "is-active", "a.service").Return(nil, tt.err)

			_, err := s.Start(context.Background(), []string{"a.service"}, time.Second)

			require.ErrorIs(t, err, tt.want)
		})
	}
}

func TestSystemd_StartAndStopNothing(t *testing.T) {
	t.Parallel()
	s, _ := newSystemd(t, false)
	ok, err := s.Stop(context.Background(), nil, time.Second)
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = s.Start(context.Background(), nil, time.Second)
	require.NoError(t, err)
	assert.True(t, ok)
}
