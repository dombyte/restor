package servicemanager

import (
	"context"
	"time"
)

// NoopSettings configure a Noop manager.
type NoopSettings struct {
	// LockKey identifies the project for the locker.
	LockKey string
}

// Noop manages no services; it is used for file-only backups.
type Noop struct {
	lockKey string
}

// NewNoop returns a Noop manager.
func NewNoop(s NoopSettings) *Noop {
	return &Noop{lockKey: s.LockKey}
}

// LockKey returns the configured key.
func (n *Noop) LockKey() string { return n.lockKey }

// Running returns no services.
func (n *Noop) Running(context.Context, []string) ([]string, error) { return nil, nil }

// Stop does nothing.
func (n *Noop) Stop(context.Context, []string, time.Duration) (bool, error) { return true, nil }

// Start does nothing.
func (n *Noop) Start(context.Context, []string, time.Duration) (bool, error) { return true, nil }
