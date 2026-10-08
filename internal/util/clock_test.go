package util

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRealClock(t *testing.T) {
	t.Parallel()
	c := NewRealClock()
	before := time.Now()
	assert.False(t, c.Now().Before(before))
	assert.NotNil(t, c.After(0))
}
