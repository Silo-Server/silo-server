// Package workerdrain owns the private worker's retirement admission fence.
// Redis observations are deliberately not used as proof that work is absent.
package workerdrain

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
)

var ErrUnavailable = errors.New("worker drain authority unavailable")
var ErrFenced = errors.New("worker admission fenced")

// Source resolves the worker's stable registered identity and current durable
// fence. Calls are serialized with admission and status inside this process.
type Source func(context.Context) (nodeID int, fenceID, nativeServerID string, err error)

// Identity correlates public health with an authenticated drain receipt from
// the same process. The public identity alone is never authorization.
type Identity struct {
	NativeServerID   string `json:"native_server_id,omitempty"`
	WorkerInstanceID string `json:"worker_instance_id,omitempty"`
}

type Status struct {
	NodeID             int       `json:"node_id"`
	FenceID            string    `json:"fence_id"`
	WorkerInstanceID   string    `json:"worker_instance_id"`
	NativeServerID     string    `json:"native_server_id"`
	Fenced             bool      `json:"fenced"`
	Drained            bool      `json:"drained"`
	ActiveJobs         int       `json:"active_jobs"`
	ActiveRequests     int       `json:"active_requests"`
	ActiveReservations int       `json:"active_reservations"`
	ObservedAt         time.Time `json:"observed_at"`
}

type Gate struct {
	mu         sync.Mutex
	source     Source
	jobs       func() int
	revoked    func(context.Context, string) bool
	now        func() time.Time
	instanceID string
	serverID   string
	nodeID     int
	fenceID    string
	sealed     bool
	active     int
	permits    map[string]time.Time
	lastPruned time.Time
}

func New(source Source, jobs func() int, revoked func(context.Context, string) bool) *Gate {
	return &Gate{source: source, jobs: jobs, revoked: revoked, now: time.Now, instanceID: uuid.NewString(), permits: make(map[string]time.Time)}
}

func (g *Gate) syncLocked(ctx context.Context) error {
	if g.source == nil {
		return ErrUnavailable
	}
	id, fence, serverID, err := g.source(ctx)
	if err != nil {
		return err
	}
	if id <= 0 || serverID == "" {
		return ErrUnavailable
	}
	if g.nodeID != 0 && (g.nodeID != id || g.serverID != serverID) {
		return ErrUnavailable
	}
	g.nodeID = id
	g.serverID = serverID
	if fence != g.fenceID {
		// Only an explicit durable cancellation can reopen a sealed worker.
		g.fenceID = fence
		g.sealed = false
	}
	return nil
}

// PublicIdentity returns the last verified process/realm binding. Its first
// read resolves durable authority without minting an identity. Later reads stay
// cheap liveness observations; authenticated drain always verifies it freshly.
func (g *Gate) PublicIdentity(ctx context.Context) Identity {
	if g == nil {
		return Identity{}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.serverID == "" {
		if err := g.syncLocked(ctx); err != nil {
			return Identity{}
		}
	}
	return Identity{NativeServerID: g.serverID, WorkerInstanceID: g.instanceID}
}

func (g *Gate) pruneLocked(ctx context.Context, force bool) {
	now := g.now()
	if !force && now.Sub(g.lastPruned) < time.Minute {
		return
	}
	g.lastPruned = now
	for key, expiry := range g.permits {
		if !expiry.After(now) || (force && g.revoked != nil && g.revoked(ctx, key)) {
			delete(g.permits, key)
		}
	}
}

// Begin reserves a request before any execution or egress can start. Existing
// identities retain their bounded media permit while retiring; an unknown
// identity cannot start, reconstruct, or resume after the worker sees a fence.
// Empty identities are one-shot background/extraction work and never retained.
func (g *Gate) Begin(ctx context.Context, key string, expiry time.Time) (func(), error) {
	if g == nil {
		return func() {}, nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	authorityConfirmed := true
	if err := g.syncLocked(ctx); err != nil {
		// Existing playback keeps its original bounded permit through a
		// temporary authority outage. No new identity, renewal, background work
		// or retirement receipt is allowed without current durable authority.
		expires, known := g.permits[key]
		if g.sealed || key == "" || !known || !expires.After(g.now()) {
			return nil, err
		}
		authorityConfirmed = false
	}
	// Expiry is checked for this identity on every admission. Full expiry
	// sweeps are bounded, and potentially remote deny lookups belong to the
	// explicit retirement observation rather than every media segment.
	if prior, ok := g.permits[key]; ok && !prior.After(g.now()) {
		delete(g.permits, key)
	}
	g.pruneLocked(ctx, false)
	_, known := g.permits[key]
	if g.fenceID != "" && (g.sealed || key == "" || !known) {
		return nil, ErrFenced
	}
	if authorityConfirmed && key != "" && expiry.After(g.now()) && expiry.After(g.permits[key]) {
		g.permits[key] = expiry
	}
	g.active++
	var once sync.Once
	return func() { once.Do(func() { g.mu.Lock(); g.active--; g.mu.Unlock() }) }, nil
}

// Observe samples actual worker execution plus request and permit reservations.
// A zero receipt seals admission under the same mutex, so a delayed dispatch
// cannot turn that receipt into a false zero. Process restart keeps the DB fence
// and starts with no permits, refusing resurrection from old client tokens.
func (g *Gate) Observe(ctx context.Context) (Status, error) {
	if g == nil {
		return Status{}, ErrUnavailable
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.syncLocked(ctx); err != nil {
		return Status{}, err
	}
	g.pruneLocked(ctx, true)
	jobs := 0
	if g.jobs != nil {
		jobs = g.jobs()
	}
	if jobs < 0 {
		return Status{}, ErrUnavailable
	}
	if g.fenceID != "" && jobs == 0 && g.active == 0 && len(g.permits) == 0 {
		g.sealed = true
	}
	return Status{NodeID: g.nodeID, FenceID: g.fenceID, WorkerInstanceID: g.instanceID, NativeServerID: g.serverID, Fenced: g.fenceID != "", Drained: g.fenceID != "" && g.sealed && jobs == 0 && g.active == 0 && len(g.permits) == 0, ActiveJobs: jobs, ActiveRequests: g.active, ActiveReservations: len(g.permits), ObservedAt: g.now().UTC()}, nil
}

// Revoke forgets a positively completed transport. In-flight requests remain
// reserved until their release; a fenced worker cannot admit the identity again.
func (g *Gate) Revoke(key string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	delete(g.permits, key)
	g.mu.Unlock()
}
