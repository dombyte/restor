package backup

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestManager_MaintenanceWarnings(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.expectLock("a")
	f.expectLock("b")
	f.expectBackup("a", nil)
	f.expectBackup("b", assert.AnError)
	f.restic.EXPECT().Forget(mock.Anything, "a", mock.Anything).Return(nil).Once()
	var env []string
	f.hooks.EXPECT().RunEnv(mock.Anything, mock.Anything, "/bin/post").
		RunAndReturn(func(_ context.Context, e []string, _ string, _ ...string) (
			[]byte, error,
		) {
			env = e
			return nil, nil
		}).Once()
	m := f.manager(t, Settings{Prune: true, Post: "/bin/post"},
		fileJob(f, t, "a", "--keep-last=1"), fileJob(f, t, "b", "--keep-last=1"))

	require.ErrorIs(t, m.Run(context.Background()), ErrJobsFailed)
	assert.Contains(t, env, "RESTOR_WARNINGS=b: forget skipped because the backup failed; "+
		"prune skipped because a backup failed or is incomplete")
}

func TestExecSafe(t *testing.T) {
	t.Parallel()
	long := "RESTOR_ERROR=" + strings.Repeat("é", maxEnvVar)
	got := execSafe([]string{"RESTOR_ERROR=a\x00b", long, "RESTOR_JOB=web"})

	assert.Equal(t, "RESTOR_ERROR=ab", got[0], "exec refuses NUL bytes")
	assert.LessOrEqual(t, len(got[1]), maxEnvVar)
	assert.True(t, strings.HasSuffix(got[1], truncated))
	assert.True(t, utf8.ValidString(got[1]), "not cut inside a character")
	assert.Equal(t, "RESTOR_JOB=web", got[2])
}
