package util

import "time"

// Clock abstracts time so loops that wait (service stop/start polling) can be driven by a
// fake clock in tests.
type Clock interface {
	// Now returns the current time.
	Now() time.Time
	// After returns a channel that receives the time once d has elapsed.
	After(d time.Duration) <-chan time.Time
}

// RealClock is the production Clock backed by the time package.
type RealClock struct{}

// NewRealClock returns the wall clock.
func NewRealClock() RealClock { return RealClock{} }

// Now returns time.Now().
func (RealClock) Now() time.Time { return time.Now() }

// After wraps time.After.
func (RealClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
