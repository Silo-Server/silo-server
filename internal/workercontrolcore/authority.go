package workercontrolcore

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

// NonceStore shares replay receipts between core instances. Receipts are claimed
// only after cryptographic proof validation and cannot evict unexpired entries.
type NonceStore interface {
	ClaimNonce(context.Context, int, string, time.Time) error
}

func (s *Service) VerifyRequest(r *http.Request, nodeID int, domain string) error {
	node, ok, err := s.options.Nodes.ByID(r.Context(), nodeID)
	if err != nil || !ok || !node.Enabled {
		return ErrDenied
	}
	verifier := workeridentity.Verifier{Origin: s.options.Origin, Domain: domain, AllowHTTP: s.options.AllowHTTP, ResolveKey: func(id int) (ed25519.PublicKey, error) {
		if id != node.ID {
			return nil, ErrDenied
		}
		return node.PublicKey, nil
	}}
	if err = verifier.Verify(r, nodeID); err != nil {
		return err
	}
	receipts, ok := s.options.Store.(NonceStore)
	if !ok {
		return ErrUnavailable
	}
	// Proof timestamps can be up to 30s in the future. Keep the receipt for the
	// full remaining 60s acceptance envelope rather than only 30s from arrival.
	return receipts.ClaimNonce(r.Context(), nodeID, r.Header.Get(workeridentity.HeaderNonce), time.Now().Add(time.Minute))
}

func (s *Service) startAdmission(ctx context.Context, a workerwire.Assignment) error {
	leases, ok := s.options.Store.(AdmissionLeases)
	if !ok {
		return ErrUnavailable
	}
	if err := leases.TouchLease(ctx, a.NodeID, a.WorkID, a.Generation, admissionLeaseTTL); err != nil {
		return err
	}
	owner, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	k := workKey{a.NodeID, a.WorkID}
	if old, ok := s.admissions[k]; ok && old.stop != nil {
		old.stop()
	}
	s.expireAuthorityLocked(k)
	s.admissions[k] = admission{ctx: owner, stop: cancel, generation: a.Generation, count: 1}
	s.mu.Unlock()
	go s.renewAdmission(owner, a)
	return nil
}
func (s *Service) finishAdmission(a workerwire.Assignment) {
	leases, ok := s.options.Store.(AdmissionLeases)
	if ok {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = leases.TouchLease(ctx, a.NodeID, a.WorkID, a.Generation, admissionLeaseTTL)
		cancel()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := workKey{a.NodeID, a.WorkID}
	v, ok := s.admissions[k]
	if ok && v.generation == a.Generation {
		if v.stop != nil {
			v.stop()
			v.stop = nil
		}
		v.count = 0
		v.until = time.Now().Add(2 * time.Second)
		s.admissions[k] = v
	}
	s.expireAuthorityLocked(k)
}
func (s *Service) invalidate(node int, work string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := workKey{node, work}
	s.expireAuthorityLocked(k)
	if v, ok := s.admissions[k]; ok && v.stop != nil {
		v.stop()
	}
	delete(s.admissions, k)
}
func (s *Service) invalidateCached(node int, work string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireAuthorityLocked(workKey{node, work})
}
func (s *Service) expireAuthorityLocked(work workKey) {
	s.epoch++
	for key := range s.authority {
		if key.workKey == work {
			delete(s.authority, key)
		}
	}
}
func (s *Service) clearAdmission(a workerwire.Assignment) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := workKey{a.NodeID, a.WorkID}
	if v, ok := s.admissions[k]; ok && v.generation == a.Generation {
		if v.stop != nil {
			v.stop()
		}
		delete(s.admissions, k)
	}
}

// HasInFlight covers only core-originated admission/dispatch and its short
// publication grace. A durable recipe must take over after the request ends.
func (s *Service) HasInFlight(nodeID int, workID string) bool {
	s.mu.Lock()
	k := workKey{nodeID, workID}
	v, ok := s.admissions[k]
	local := ok && (v.count > 0 && v.ctx.Err() == nil || time.Now().Before(v.until))
	if ok && !local {
		delete(s.admissions, k)
	}
	s.mu.Unlock()
	if local {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	return s.sharedLease(ctx, nodeID, workID, 0)
}
func (s *Service) pendingOwned(ctx context.Context, nodeID int, work string, generation uint64) bool {
	s.mu.Lock()
	v, ok := s.admissions[workKey{nodeID, work}]
	local := ok && v.generation == generation && v.count > 0 && v.ctx.Err() == nil
	s.mu.Unlock()
	return local || s.sharedLease(ctx, nodeID, work, generation)
}
func (s *Service) executionActive(ctx context.Context, node Node, a workerwire.Assignment) bool {
	return s.options.ExecutionActive == nil || s.options.ExecutionActive(ctx, node, a)
}

// WorkActive bounds shared-store/registry checks to four per second per active
// work. Begin/Stop invalidate this core's cache immediately; other core changes
// are observed within 250ms, below the two-second worker authority horizon.
func (s *Service) WorkActive(ctx context.Context, nodeID int, work string, generation uint64) bool {
	if ctx.Err() != nil || generation == 0 {
		return false
	}
	k := authorityKey{workKey: workKey{nodeID, work}, generation: generation}
	s.mu.Lock()
	cached, ok := s.authority[k]
	epoch := s.epoch
	s.mu.Unlock()
	if ok && cached.generation == generation && time.Now().Before(cached.until) {
		return cached.active
	}
	node, found, err := s.options.Nodes.ByID(ctx, nodeID)
	active := err == nil && found && node.Enabled
	if active {
		var permitted bool
		permitted, err = s.options.Store.IsActive(ctx, nodeID, work, generation)
		active = err == nil && permitted
	}
	if active {
		candidate, pending, e := s.options.Store.Candidate(ctx, nodeID, work)
		active = e == nil
		if active && pending && candidate.Generation == generation {
			active = s.pendingOwned(ctx, nodeID, work, generation)
		} else if active {
			a, ready, e := s.options.Store.Get(ctx, nodeID, work)
			active = e == nil && ready && a.Generation == generation && s.executionActive(ctx, node, a)
		}
	}
	s.mu.Lock()
	if len(s.authority) >= 10000 {
		clear(s.authority)
	}
	if epoch == s.epoch {
		s.authority[k] = authorityResult{active: active, generation: generation, until: time.Now().Add(250 * time.Millisecond)}
	} else {
		active = false
	}
	s.mu.Unlock()
	return active
}
