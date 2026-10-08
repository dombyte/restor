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

func TestSystemd_Services(t *testing.T) {
	t.Parallel()
	s, _ := newSystemd(t, false)
	tests := []struct {
		name      string
		requested []string
		want      []string
		wantErr   bool
	}{
		{name: "all by default", want: []string{"a.service", "b.service"}},
		{name: "filtered", requested: []string{"b.service"}, want: []string{"b.service"}},
		{name: "unknown", requested: []string{"b.service", "x.service"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := s.Services(context.Background(), tt.requested)
			if tt.wantErr {
				require.ErrorIs(t, err, errUnknownUnit)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
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
			expectSystemctl(r, "is-active", "a.service").Return(nil, tt.err).Once()

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
