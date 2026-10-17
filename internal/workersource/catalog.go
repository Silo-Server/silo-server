package workersource

import (
	"context"
	"time"
)

const catalogCheckInterval = 250 * time.Millisecond
const catalogCacheLimit = 10000

type pathKind struct{ path, kind string }
type approvalCheck struct {
	done chan struct{}
	err  error
}

// catalogAllowed caches only the exact catalog path's continued existence and
// enabled-folder membership. Viewer and work authorization remain independent.
// Its non-sliding lifetime starts before the query, including time spent blocked.
func (a *Authority) catalogAllowed(ctx context.Context, path, kind string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	k := pathKind{path, kind}
	a.approvalMu.Lock()
	started := a.options.Now()
	if until, ok := a.approvals[k]; ok && started.Before(until) {
		a.approvalMu.Unlock()
		return ctx.Err()
	}
	delete(a.approvals, k)
	if pending := a.approvalChecks[k]; pending != nil {
		a.approvalMu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-pending.done:
			if err := ctx.Err(); err != nil {
				return err
			}
			if pending.err != nil {
				return pending.err
			}
			return a.catalogAllowed(ctx, path, kind)
		}
	}
	if len(a.approvalChecks) >= catalogCacheLimit {
		a.approvalMu.Unlock()
		return ErrUnavailable
	}
	pending := &approvalCheck{done: make(chan struct{})}
	a.approvalChecks[k] = pending
	a.approvalMu.Unlock()
	err := a.options.PathApprover(ctx, path, kind)
	if err == nil {
		err = ctx.Err()
	}
	a.approvalMu.Lock()
	until := started.Add(catalogCheckInterval)
	if err == nil && a.options.Now().Before(until) {
		if len(a.approvals) >= catalogCacheLimit {
			clear(a.approvals)
		}
		a.approvals[k] = until
	}
	pending.err = err
	delete(a.approvalChecks, k)
	close(pending.done)
	a.approvalMu.Unlock()
	return err
}
