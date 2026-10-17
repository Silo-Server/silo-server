package workercontrol

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

const authorityRefreshInterval = 500 * time.Millisecond
const authorityRefreshTimeout = 500 * time.Millisecond
const authorityRefreshRetryDelay = 25 * time.Millisecond
const authorityRefreshAttempts = 2

type authorityRefresh struct {
	done chan struct{}
	err  error
	sent time.Time
}

// refresh coalesces callers without assigning any caller ownership of the RPC.
// A canceled waiter leaves the runtime's bounded refresh available to others.
func (r *Runtime) refresh(ctx context.Context) error {
	_, err := r.waitRefresh(ctx)
	return err
}

// refreshFor permits a captured snapshot to serve a later caller only when it
// includes that caller's exact authority. Otherwise one newer flight must cover
// the scope; a missing scope in a fresh response remains a definitive denial.
func (r *Runtime) refreshFor(ctx context.Context, allowed func() bool) error {
	minimum := time.Now()
	for attempt := range 2 {
		flight, err := r.waitRefresh(ctx)
		if err != nil {
			return err
		}
		if attempt != 0 && flight.sent.Before(minimum) {
			break
		}
		if allowed() {
			return nil
		}
		if !flight.sent.Before(minimum) {
			break
		}
	}
	return errors.New("worker snapshot does not include required authority")
}

// refreshCurrent also supports operations for which a fresh empty snapshot is
// meaningful, such as configuration reload or retained-artifact deletion.
func (r *Runtime) refreshCurrent(ctx context.Context) error {
	minimum := time.Now()
	for range 2 {
		flight, err := r.waitRefresh(ctx)
		if err != nil {
			return err
		}
		if !flight.sent.Before(minimum) {
			return nil
		}
	}
	return errors.New("worker snapshot predates refresh request")
}

func (r *Runtime) waitRefresh(ctx context.Context) (*authorityRefresh, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := r.ctx.Err(); err != nil {
		return nil, err
	}
	r.refreshMu.Lock()
	flight := r.refreshFlight
	if flight == nil {
		flight = &authorityRefresh{done: make(chan struct{})}
		r.refreshFlight = flight
		go r.runRefresh(flight)
	}
	r.refreshMu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-r.ctx.Done():
		return nil, r.ctx.Err()
	case <-flight.done:
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return flight, flight.err
	}
}

func (r *Runtime) runRefresh(flight *authorityRefresh) {
	var sent time.Time
	var notify func()
	var err error
	for attempt := range authorityRefreshAttempts {
		ctx, cancel := context.WithTimeout(r.ctx, authorityRefreshTimeout)
		sent = time.Now()
		notify, err = r.refreshAttempt(ctx, sent)
		cancel()
		if err == nil || !errors.Is(err, errControlUnavailable) || attempt+1 == authorityRefreshAttempts || r.ctx.Err() != nil {
			break
		}
		timer := time.NewTimer(authorityRefreshRetryDelay)
		select {
		case <-r.ctx.Done():
			err = r.ctx.Err()
		case <-timer.C:
		}
		timer.Stop()
		if r.ctx.Err() != nil {
			break
		}
	}
	r.refreshMu.Lock()
	// Anchor the next refresh to request send, just like the authority lease.
	// Two fast failures still wait a regular interval before another flight.
	r.refreshNext = sent.Add(authorityRefreshInterval)
	r.refreshFlight = nil
	if r.refreshWake != nil {
		select {
		case r.refreshWake <- struct{}{}:
		default:
		}
	}
	r.refreshMu.Unlock()
	// Drain callbacks may synchronously request another signed configuration.
	// Release flight ownership first, and finish this caller after the callback.
	if notify != nil {
		notify()
	}
	flight.sent = sent
	flight.err = err
	close(flight.done)
}

func (r *Runtime) refreshLoop() {
	r.refreshMu.Lock()
	if r.refreshWake == nil {
		r.refreshWake = make(chan struct{}, 1)
	}
	wake := r.refreshWake
	r.refreshMu.Unlock()
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		r.refreshMu.Lock()
		next := r.refreshNext
		r.refreshMu.Unlock()
		timer.Reset(max(0, time.Until(next)))
		select {
		case <-r.ctx.Done():
			return
		case <-wake:
			continue
		case <-timer.C:
			_ = r.refresh(r.ctx)
		}
	}
}

func (r *Runtime) refreshAttempt(ctx context.Context, sent time.Time) (func(), error) {
	n := nonce()
	if err := r.authority.SetNonce(n); err != nil {
		return nil, err
	}
	r.mu.Lock()
	sequence := r.sequence
	r.mu.Unlock()
	var snapshot workerwire.Snapshot
	if err := r.callGrant(ctx, http.MethodPost, "/snapshot", map[string]any{"boot_id": r.bootID, "nonce": n, "sequence": sequence}, workeridentity.DomainSnapshotGrant, &snapshot); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if snapshot.ExecutionConfig != nil {
		if err := r.config.check(*snapshot.ExecutionConfig); err != nil {
			return nil, err
		}
	}
	if err := r.authority.Apply(snapshot, sent); err != nil {
		return nil, err
	}
	r.renewals.reconcile(snapshot)
	var notify func()
	if snapshot.ExecutionConfig != nil {
		var err error
		notify, err = r.config.install(*snapshot.ExecutionConfig)
		if err != nil {
			return nil, err
		}
	}
	r.mu.Lock()
	r.sequence = snapshot.Sequence
	r.mu.Unlock()
	return notify, nil
}
