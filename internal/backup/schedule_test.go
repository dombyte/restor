package backup

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/dombyte/restor/internal/backup/mocks"
)

// scheduledJob returns a job with lock keys and the jobs it runs after.
func scheduledJob(t *testing.T, name string, keys []string, after ...string) Job {
	t.Helper()
	s := mocks.NewMockServices(t)
	s.EXPECT().LockKeys().Return(keys).Maybe()
	s.EXPECT().Running(mock.Anything, mock.Anything).Return(nil, nil).Maybe()
	return Job{Settings: JobSettings{Name: name, After: after}, Services: s}
}

func TestSchedule(t *testing.T) {
	t.Parallel()
	jobs := []Job{
		scheduledJob(t, "a", []string{"compose"}),
		scheduledJob(t, "b", []string{"compose"}), // shares a's compose file
		scheduledJob(t, "c", []string{"c"}, "a"),  // runs after a
		scheduledJob(t, "d", []string{"d"}),
		scheduledJob(t, "e", []string{"e"}, "missing"), // missing: not in this run
	}
	s := newSchedule(jobs, 0)

	assert.Equal(t, []int{0, 3, 4}, s.next(), "b waits for the lock, c for a")
	assert.Empty(t, s.next(), "nothing new while they run")
	s.finish(3)
	assert.Empty(t, s.next())
	s.finish(0)
	assert.Equal(t, []int{1, 2}, s.next(), "a finished: its lock and c are free")
	s.finish(1)
	s.finish(2)
	s.finish(4)
	assert.Empty(t, s.next())
	assert.Zero(t, s.running)
}

func TestSchedule_Limit(t *testing.T) {
	t.Parallel()
	jobs := []Job{
		scheduledJob(t, "a", nil), scheduledJob(t, "b", nil), scheduledJob(t, "c", nil),
	}
	s := newSchedule(jobs, 2)

	assert.Equal(t, []int{0, 1}, s.next())
	assert.Empty(t, s.next(), "the limit is reached")
	s.finish(1)
	assert.Equal(t, []int{2}, s.next())
}
