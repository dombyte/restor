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

const composeFile = "/srv/app/compose.yml"

func newCompose(t *testing.T) (*Compose, *mocks.MockRunner) {
	t.Helper()
	d, r, _ := newDeps(t)
	c, err := NewCompose(ComposeSettings{Binary: "podman", File: composeFile}, d)
	require.NoError(t, err)
	return c, r
}

// expectCompose expects `podman compose -f <file> <args>`.
func expectCompose(r *mocks.MockRunner, args ...any) *mock.Call {
	all := append([]any{mock.Anything, "podman", "compose", "-f", composeFile}, args...)
	return r.On("Run", all...)
}

func TestCompose_LockKey(t *testing.T) {
	t.Parallel()
	c, _ := newCompose(t)
	assert.Equal(t, composeFile, c.LockKey())
}

func TestCompose_Running(t *testing.T) {
	t.Parallel()
	ps := []byte(`{"Service":"web","State":"running"}` + "\n" +
		`{"Service":"db","State":"exited"}`)
	c, r := newCompose(t)
	expectCompose(r, "ps", "--format", "json").Return(ps, nil).Once()
	requested := []string{"web", "db"}
	got, err := c.Running(context.Background(), requested)
	require.NoError(t, err)
	assert.Equal(t, []string{"web"}, got)
	assert.Equal(t, []string{"web", "db"}, requested, "requested is not modified")

	expectCompose(r, "config", "--services").Return([]byte("web\ndb\njob\n\n"), nil).Once()
	expectCompose(r, "ps", "--format", "json").Return(ps, nil).Once()
	got, err = c.Running(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"web"}, got)

	expectCompose(r, "config", "--services").Return(nil, assert.AnError).Once()
	_, err = c.Running(context.Background(), nil)
	require.ErrorIs(t, err, assert.AnError)

	expectCompose(r, "ps", "--format", "json").Return(nil, assert.AnError).Once()
	_, err = c.Running(context.Background(), []string{"web"})
	require.ErrorIs(t, err, assert.AnError)
}

func TestCompose_Stop(t *testing.T) {
	t.Parallel()
	c, r := newCompose(t)
	expectCompose(r, "stop", "-t", "10", "web", "db").Return(nil, nil).Once()
	expectCompose(r, "ps", "--format", "json").
		Return([]byte(`{"Service":"web","State":"running"}`), nil).Once()
	expectCompose(r, "ps", "--format", "json").
		Return([]byte(`{"Service":"other","State":"running"}`), nil).Once()

	ok, err := c.Stop(context.Background(), []string{"web", "db"}, 10*time.Second)

	require.NoError(t, err)
	assert.True(t, ok)
}

func TestCompose_StopTimeout(t *testing.T) {
	t.Parallel()
	c, r := newCompose(t)
	expectCompose(r, "stop", "-t", "1", "web").Return(nil, nil).Once()
	expectCompose(r, "ps", "--format", "json").
		Return([]byte(`[{"Service":"web","State":"running"}]`), nil)

	ok, err := c.Stop(context.Background(), []string{"web"}, time.Second)

	require.NoError(t, err)
	assert.False(t, ok)
}

func TestCompose_StopNoContainers(t *testing.T) {
	t.Parallel()
	c, r := newCompose(t)
	expectCompose(r, "stop", "-t", "1", "web").
		Return([]byte("No containers to stop"), assert.AnError).Once()
	expectCompose(r, "ps", "--format", "json").Return(nil, nil).Once()

	ok, err := c.Stop(context.Background(), []string{"web"}, time.Second)

	require.NoError(t, err)
	assert.True(t, ok)
}

func TestCompose_StopFails(t *testing.T) {
	t.Parallel()
	c, r := newCompose(t)
	expectCompose(r, "stop", "-t", "1", "web").Return([]byte("boom"), assert.AnError).Once()

	_, err := c.Stop(context.Background(), []string{"web"}, time.Second)

	require.ErrorIs(t, err, assert.AnError)
}

func TestCompose_StopStatusFails(t *testing.T) {
	t.Parallel()
	c, r := newCompose(t)
	expectCompose(r, "stop", "-t", "1", "web").Return(nil, nil).Once()
	expectCompose(r, "ps", "--format", "json").Return([]byte("not json"), nil).Once()

	_, err := c.Stop(context.Background(), []string{"web"}, time.Second)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "status of "+composeFile)
}

func TestCompose_StartAndStopNothing(t *testing.T) {
	t.Parallel()
	c, _ := newCompose(t)
	ok, err := c.Stop(context.Background(), nil, time.Second)
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = c.Start(context.Background(), nil, time.Second)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestCompose_StartFails(t *testing.T) {
	t.Parallel()
	c, r := newCompose(t)
	expectCompose(r, "start", "web").Return(nil, assert.AnError).Once()

	_, err := c.Start(context.Background(), []string{"web"}, time.Second)

	require.ErrorIs(t, err, assert.AnError)
}

func TestParseComposePs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		out     string
		want    map[string]string // service → state
		wantErr bool
	}{
		{name: "empty", out: " \n", want: map[string]string{}},
		{
			name: "docker json lines",
			out: `{"Service":"web","State":"running","Labels":"a=b,c=d"}` + "\n\n" +
				`{"Service":"db","State":"exited"}`,
			want: map[string]string{"web": "running", "db": "exited"},
		},
		{
			name: "podman array with labels",
			out:  `[{"State":"running","Labels":{"com.docker.compose.service":"web"}}]`,
			want: map[string]string{"web": "running"},
		},
		{name: "broken array", out: `[{"State":`, wantErr: true},
		{name: "broken line", out: `{"State":"running"}` + "\nnope", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			entries, err := parseComposePs([]byte(tt.out))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			got := map[string]string{}
			for _, e := range entries {
				got[e.service()] = e.State
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCompose_Start(t *testing.T) {
	t.Parallel()
	c, r := newCompose(t)
	expectCompose(r, "start", "web", "db").Return(nil, nil).Once()
	expectCompose(r, "ps", "--format", "json").
		Return([]byte(`{"Service":"web","State":"running"}`), nil).Once()
	expectCompose(r, "ps", "--format", "json").
		Return([]byte(`{"Service":"web","State":"running"}`+"\n"+
			`{"Service":"db","State":"running"}`), nil).Once()

	ok, err := c.Start(context.Background(), []string{"web", "db"}, time.Minute)

	require.NoError(t, err)
	assert.True(t, ok, "returns as soon as all services run")
}

func TestCompose_StartTimeout(t *testing.T) {
	t.Parallel()
	c, r := newCompose(t)
	expectCompose(r, "start", "web").Return(nil, nil).Once()
	expectCompose(r, "ps", "--format", "json").
		Return([]byte(`{"Service":"web","State":"restarting"}`), nil)

	ok, err := c.Start(context.Background(), []string{"web"}, time.Second)

	require.NoError(t, err)
	assert.False(t, ok)
}
