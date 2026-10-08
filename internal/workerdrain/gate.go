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

// ErrIdentityUnavailable distinguishes positive registration/realm loss from
// a transient source I/O failure; known permits cannot use outage continuity.
var ErrIdentityUnavailable = errors.Join(ErrUnavailable, errors.New("worker identity missing or changed"))

var errAuthorityStale = errors.Join(ErrUnavailable, errors.New("worker authority changed during lookup"))

// Source resolves the worker's stable registered identity and current durable
// fence. It may be called concurrently; each read must resolve fresh authority.
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
	mu                sync.Mutex
	source            Source
	jobs              func() int
	revoked           func(context.Context, string) bool
	now               func() time.Time
	instanceID        string
	serverID          string
	nodeID            int
	fenceID           string
	sealed            bool
	active            int
	permits           map[string]time.Time
	lastPruned        time.Time
	authorityEpoch    uint64
	identityLost      bool
	permitVersions    map[string]uint64
	nextPermitVersion uint64
}

func New(source Source, jobs func() int, revoked func(context.Context, string) bool) *Gate {
	return &Gate{source: source, jobs: jobs, revoked: revoked, now: time.Now, instanceID: uuid.NewString(), permits: make(map[string]time.Time), permitVersions: make(map[string]uint64)}
}

// syncLocked enters and returns with mu held, releasing it around source I/O.
// An epoch change invalidates both successes and errors from an older read.
func (g *Gate) syncLocked(ctx context.Context) error {
	if g.source == nil {
		return ErrUnavailable
	}
	for attempt := 0; attempt < 3; attempt++ {
		epoch := g.authorityEpoch
		g.mu.Unlock()
		id, fence, serverID, err := g.source(ctx)
		g.mu.Lock()
		if epoch != g.authorityEpoch {
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			if errors.Is(err, ErrIdentityUnavailable) {
				g.loseIdentityLocked()
			}
			return err
		}
		if id <= 0 || serverID == "" || g.nodeID != 0 && (g.nodeID != id || g.serverID != serverID) {
			g.loseIdentityLocked()
			return ErrIdentityUnavailable
		}
		if g.nodeID == 0 || g.identityLost || fence != g.fenceID {
			g.authorityEpoch++
		}
		g.nodeID, g.serverID, g.identityLost = id, serverID, false
		if fence != g.fenceID {
			// Only a fresh explicit durable cancellation can reopen sealing.
			g.fenceID, g.sealed = fence, false
		}
		return nil
	}
	return errAuthorityStale
}

func (g *Gate) loseIdentityLocked() {
	// Every positive loss invalidates reads already in flight, including reads
	// that began after an earlier loss but before another foreign observation.
	g.authorityEpoch++
	g.identityLost = true
}

func (g *Gate) retainLocked(key string, expiry time.Time) {
	if key == "" || !expiry.After(g.now()) {
		return
	}
	if expiry.After(g.permits[key]) {
		g.permits[key] = expiry
	}
	g.nextPermitVersion++
	g.permitVersions[key] = g.nextPermitVersion
}

func (g *Gate) revokeLocked(key string) {
	delete(g.permits, key)
	delete(g.permitVersions, key)
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

// pruneLocked keeps expiry checks local. Explicit deny observations run without
// mu and apply only to the exact permit generation that was observed.
func (g *Gate) pruneLocked(ctx context.Context, force bool) bool {
	now := g.now()
	if !force && now.Sub(g.lastPruned) < time.Minute {
		return true
	}
	g.lastPruned = now
	for key, expiry := range g.permits {
		if !expiry.After(now) {
			g.revokeLocked(key)
		}
	}
	if !force || g.revoked == nil || len(g.permits) == 0 {
		return true
	}
	versions := make(map[string]uint64, len(g.permits))
	for key := range g.permits {
		versions[key] = g.permitVersions[key]
	}
	epoch := g.authorityEpoch
	g.mu.Unlock()
	denied := make([]string, 0, len(versions))
	for key := range versions {
		if g.revoked(ctx, key) {
			denied = append(denied, key)
		}
	}
	g.mu.Lock()
	if epoch != g.authorityEpoch || ctx.Err() != nil {
		return false
	}
	for _, key := range denied {
		if current, exists := g.permitVersions[key]; exists && current == versions[key] {
			g.revokeLocked(key)
		}
	}
	return true
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
		if g.identityLost || errors.Is(err, ErrIdentityUnavailable) || errors.Is(err, errAuthorityStale) || ctx.Err() != nil || g.sealed || key == "" || !known || !expires.After(g.now()) {
			return nil, err
		}
		authorityConfirmed = false
	}
	// Expiry is checked for this identity on every admission. Full expiry
	// sweeps are bounded, and potentially remote deny lookups belong to the
	// explicit retirement observation rather than every media segment.
	if prior, ok := g.permits[key]; ok && !prior.After(g.now()) {
		g.revokeLocked(key)
	}
	g.pruneLocked(ctx, false)
	_, known := g.permits[key]
	if g.fenceID != "" && (g.sealed || key == "" || !known) {
		return nil, ErrFenced
	}
	if authorityConfirmed && key != "" && expiry.After(g.now()) && expiry.After(g.permits[key]) {
		g.retainLocked(key, expiry)
	}
	g.active++
	var once sync.Once
	return func() { once.Do(func() { g.mu.Lock(); g.active--; g.mu.Unlock() }) }, nil
}

// BeginPreparation admits new one-shot preparation, never a known-ID retry
// after fencing. Successful publication transfers its reservation to a retained
// artifact permit before request release. Failed preparation retains nothing.
func (g *Gate) BeginPreparation(ctx context.Context, key string, expiry time.Time) (func(bool), error) {
	end, err := g.Begin(ctx, "", time.Time{})
	if err != nil {
		return nil, err
	}
	var once sync.Once
	return func(published bool) {
		once.Do(func() {
			if g != nil && published {
				g.mu.Lock()
				g.retainLocked(key, expiry)
				g.mu.Unlock()
			}
			end()
		})
	}, nil
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
	if !g.pruneLocked(ctx, true) {
		return Status{}, errAuthorityStale
	}
	jobs := 0
	if g.jobs != nil {
		jobs = g.jobs()
	}
	if jobs < 0 {
		return Status{}, ErrUnavailable
	}
	if g.fenceID != "" && jobs == 0 && g.active == 0 && len(g.permits) == 0 && !g.sealed {
		g.sealed = true
		g.authorityEpoch++
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
	g.revokeLocked(key)
	g.mu.Unlock()
}
