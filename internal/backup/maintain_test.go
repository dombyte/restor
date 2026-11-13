package backup

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestManager_Maintain(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mock.InOrder(
		f.restic.EXPECT().Prune(mock.Anything).Return(nil).Once(),
		f.restic.EXPECT().Unlock(mock.Anything).Return(nil).Once(),
		f.restic.EXPECT().Check(mock.Anything).Return(nil).Once(),
	)
	f.hooks.EXPECT().RunEnv(mock.Anything, []string{
		"RESTOR_HOOK=post", "RESTOR_COMMAND=maintain", "RESTOR_STATUS=success",
		"RESTOR_JOBS=", "RESTOR_SUCCEEDED_JOBS=", "RESTOR_INCOMPLETE_JOBS=",
		"RESTOR_FAILED_JOBS=", "RESTOR_SNAPSHOT_IDS=",
	}, "/bin/post").Return(nil, nil).Once()
	m := f.manager(t, Settings{Post: "/bin/post"}, fileJob(f, t, "a", "--keep-last=1"))

	require.NoError(t, m.Maintain(context.Background(), Maintenance{Prune: true, Check: true}),
		"no backup and no forget: the mocks fail the test on any")
}

func TestManager_MaintainFails(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.restic.EXPECT().Check(mock.Anything).Return(assert.AnError).Once()
	f.hooks.EXPECT().RunEnv(mock.Anything, mock.Anything, "/bin/failed").Return(nil, nil).Once()
	m := f.manager(t, Settings{OnFailure: "/bin/failed"})

	err := m.Maintain(context.Background(), Maintenance{Check: true})

	require.ErrorIs(t, err, ErrMaintenanceFailed)
	require.ErrorIs(t, m.Maintain(context.Background(), Maintenance{}), ErrNothingToMaintain)
}
