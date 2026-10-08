// Package clocktest provides a fake util.Clock for tests of polling loops: time only moves
// when a loop waits, so tests never sleep.
package clocktest

import (
	"sync"
	"time"
)

// Clock is a fake util.Clock. After advances the clock by d and fires immediately, so a
// polling loop runs through its timeout without real waiting.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// New returns a fake clock starting at start.
func New(start time.Time) *Clock {
	return &Clock{now: start}
}

// Now returns the fake current time.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// After advances the clock by d and returns a channel that already holds the new time.
func (c *Clock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	ch := make(chan time.Time, 1)
	ch <- c.now
	return ch
}
