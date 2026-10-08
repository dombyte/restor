package clocktest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestClock_AfterAdvances(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	c := New(start)
	assert.Equal(t, start, c.Now())

	got := <-c.After(time.Second)
	assert.Equal(t, start.Add(time.Second), got)
	assert.Equal(t, start.Add(time.Second), c.Now())
}
