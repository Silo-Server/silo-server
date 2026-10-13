package workerwire

import (
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// LocalAuthority limits each core snapshot to a deadline measured from the
// local request-send time. Its caller verifies the core signature before Apply.
// Expiry and replacement callbacks receive the revoked generation, so callers
// can fence cancellation against a later generation of the same work.
type LocalAuthority struct {
	mu        sync.Mutex
	nodeID    int
	bootID    string
	nonce     string
	sequence  uint64
	deadline  time.Time
	active    map[string]uint64
	pending   map[string]uint64
	outputs   map[string]bool
	timer     *time.Timer
	closed    bool
	onRevoked func(string, uint64)
}
type revokedWork struct {
	workID     string
	generation uint64
}

func NewLocalAuthority(nodeID int, bootID, nonce string, onRevoked func(string, uint64)) (*LocalAuthority, error) {
	if nodeID <= 0 || bootID == "" || nonce == "" {
		return nil, errors.New("invalid local authority identity")
	}
	return &LocalAuthority{nodeID: nodeID, bootID: bootID, nonce: nonce, active: map[string]uint64{}, onRevoked: onRevoked}, nil
}

func (a *LocalAuthority) Apply(snapshot Snapshot, requestSent time.Time) error {
	now := time.Now()
	if requestSent.IsZero() || requestSent.After(now) || snapshot.ValidForMillis <= 0 {
		return errors.New("invalid authority request timing")
	}
	validMillis := snapshot.ValidForMillis
	if validMillis > 2000 {
		validMillis = 2000
	}
	deadline := requestSent.Add(time.Duration(validMillis) * time.Millisecond)
	if !now.Before(deadline) {
		return errors.New("authority response expired in transit")
	}
	active := make(map[string]uint64, len(snapshot.Active))
	for work, generation := range snapshot.Active {
		if work == "" || generation == 0 {
			return errors.New("invalid authority work scope")
		}
		active[work] = generation
	}
	pending := make(map[string]uint64, len(snapshot.Pending))
	for work, generation := range snapshot.Pending {
		if work == "" || generation == 0 {
			return errors.New("invalid pending work scope")
		}
		pending[work] = generation
	}
	if len(snapshot.Outputs) > 4096 {
		return errors.New("output authority snapshot exceeds bound")
	}
	outputs := make(map[string]bool, len(snapshot.Outputs))
	for id, allowed := range snapshot.Outputs {
		decoded, err := hex.DecodeString(id)
		if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != id {
			return errors.New("invalid output authority identity")
		}
		if allowed {
			outputs[id] = true
		}
	}
	a.mu.Lock()
	if a.closed || snapshot.Version != 1 || snapshot.NodeID != a.nodeID || snapshot.BootID != a.bootID || snapshot.Nonce != a.nonce || snapshot.Sequence <= a.sequence {
		a.mu.Unlock()
		return errors.New("authority snapshot identity or sequence mismatch")
	}
	revoked := a.expireLocked(now)
	for work, generation := range a.active {
		if active[work] != generation && pending[work] != generation {
			revoked = append(revoked, revokedWork{work, generation})
		}
	}
	for work, generation := range a.pending {
		if active[work] != generation && pending[work] != generation && a.active[work] != generation {
			revoked = append(revoked, revokedWork{work, generation})
		}
	}
	a.pending = pending
	a.sequence = snapshot.Sequence
	a.deadline = deadline
	a.active = active
	a.outputs = outputs
	if a.timer != nil {
		a.timer.Stop()
	}
	sequence := a.sequence
	a.timer = time.AfterFunc(time.Until(deadline), func() { a.expire(sequence) })
	a.mu.Unlock()
	a.notify(revoked)
	return nil
}

func (a *LocalAuthority) Allowed(workID string, generation uint64) bool {
	a.mu.Lock()
	revoked := a.expireLocked(time.Now())
	allowed := !a.closed && generation != 0 && (a.active[workID] == generation || a.pending[workID] == generation)
	a.mu.Unlock()
	a.notify(revoked)
	return allowed
}

// Generation returns the currently published generation, excluding candidates.
func (a *LocalAuthority) Generation(workID string) uint64 {
	a.mu.Lock()
	revoked := a.expireLocked(time.Now())
	generation := a.active[workID]
	if a.closed {
		generation = 0
	}
	a.mu.Unlock()
	a.notify(revoked)
	return generation
}

// ObservedGeneration includes admitted candidates as well as published work.
// Freshness is checked locally; callers must refresh outside lifecycle locks
// when a restarted worker has no local artifact ownership information.
func (a *LocalAuthority) ObservedGeneration(workID string) (uint64, bool) {
	a.mu.Lock()
	now := time.Now()
	revoked := a.expireLocked(now)
	generation := max(a.active[workID], a.pending[workID])
	fresh := !a.closed && a.deadline.After(now)
	a.mu.Unlock()
	a.notify(revoked)
	return generation, fresh
}

// OutputAllowed checks a viewer's prepared-artifact grant independently from
// the shared artifact's execution lifetime. It never performs a core RPC.
func (a *LocalAuthority) OutputAllowed(id string) bool {
	a.mu.Lock()
	revoked := a.expireLocked(time.Now())
	allowed := !a.closed && id != "" && a.outputs[id]
	a.mu.Unlock()
	a.notify(revoked)
	return allowed
}

// SetNonce marks the next reconcile request. Replies for earlier requests can
// no longer renew authority; the existing snapshot retains its current deadline.
func (a *LocalAuthority) SetNonce(nonce string) error {
	if nonce == "" {
		return errors.New("empty authority nonce")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return errors.New("local authority closed")
	}
	a.nonce = nonce
	return nil
}
func (a *LocalAuthority) Close() {
	a.mu.Lock()
	a.closed = true
	if a.timer != nil {
		a.timer.Stop()
	}
	revoked := a.clearLocked()
	a.mu.Unlock()
	a.notify(revoked)
}
func (a *LocalAuthority) expire(sequence uint64) {
	a.mu.Lock()
	var revoked []revokedWork
	if a.sequence == sequence {
		revoked = a.expireLocked(time.Now())
	}
	a.mu.Unlock()
	a.notify(revoked)
}
func (a *LocalAuthority) expireLocked(now time.Time) []revokedWork {
	if a.deadline.IsZero() || now.Before(a.deadline) {
		return nil
	}
	return a.clearLocked()
}
func (a *LocalAuthority) clearLocked() []revokedWork {
	revoked := make([]revokedWork, 0, len(a.active))
	for work, generation := range a.active {
		revoked = append(revoked, revokedWork{work, generation})
	}
	for work, generation := range a.pending {
		if a.active[work] != generation {
			revoked = append(revoked, revokedWork{work, generation})
		}
	}
	a.pending = map[string]uint64{}
	a.active = map[string]uint64{}
	a.outputs = map[string]bool{}
	a.deadline = time.Time{}
	return revoked
}
func (a *LocalAuthority) notify(revoked []revokedWork) {
	if a.onRevoked != nil {
		for _, work := range revoked {
			a.onRevoked(work.workID, work.generation)
		}
	}
}
