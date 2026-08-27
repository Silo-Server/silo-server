package claimcontext

import (
	"context"
	"sync"
)

type ownershipGuardKey struct{}

type guardedCancelContext struct {
	context.Context
	guard    func() error
	cancel   context.CancelFunc
	done     chan struct{}
	doneOnce sync.Once
}

// WithOwnershipGuard installs a synchronous ownership check for claimed work.
func WithOwnershipGuard(ctx context.Context, guard func() error) context.Context {
	if guard == nil {
		return ctx
	}
	return context.WithValue(ctx, ownershipGuardKey{}, guard)
}

// WithGuardedCancel derives a cancelable context without losing an installed
// ownership guard.
func WithGuardedCancel(ctx context.Context) (context.Context, context.CancelFunc) {
	base, cancel := context.WithCancel(ctx)
	guard, _ := ctx.Value(ownershipGuardKey{}).(func() error)
	if guard == nil {
		return base, cancel
	}
	guarded := &guardedCancelContext{
		Context: base,
		guard:   guard,
		cancel:  cancel,
		done:    make(chan struct{}),
	}
	context.AfterFunc(base, guarded.closeDone)
	return guarded, func() {
		cancel()
		guarded.closeDone()
	}
}

func (c *guardedCancelContext) Done() <-chan struct{} {
	c.check()
	return c.done
}

func (c *guardedCancelContext) Err() error {
	c.check()
	if err := c.Context.Err(); err != nil {
		return err
	}
	select {
	case <-c.done:
		return context.Canceled
	default:
		return nil
	}
}

func (c *guardedCancelContext) check() {
	if c.guard() != nil {
		c.cancel()
		c.closeDone()
		return
	}
	if c.Context.Err() != nil {
		c.closeDone()
	}
}

func (c *guardedCancelContext) closeDone() {
	c.doneOnce.Do(func() { close(c.done) })
}
