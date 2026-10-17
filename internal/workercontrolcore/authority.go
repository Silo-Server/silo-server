package workercontrolcore

import (
	"context"
	"crypto/ed25519"
	"errors"
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
	if err != nil {
		return errors.Join(ErrUnavailable, err)
	}
	if !ok || !node.Enabled {
		return ErrDenied
	}
	verifier := workeridentity.Verifier{Origin: s.options.Origin, Domain: domain, AllowHTTP: s.options.AllowHTTP, ResolveKey: func(id int) (ed25519.PublicKey, error) {
		if id != node.ID {
			return nil, ErrDenied
		}
		return node.PublicKey, nil
	}}
	if err = verifier.Verify(r, nodeID); err != nil {
		return errors.Join(ErrDenied, err)
	}
	receipts, ok := s.options.Store.(NonceStore)
	if !ok {
		return ErrUnavailable
	}
	// Proof timestamps can be up to 30s in the future. Keep the receipt for the
	// full remaining 60s acceptance envelope rather than only 30s from arrival.
	err = receipts.ClaimNonce(r.Context(), nodeID, r.Header.Get(workeridentity.HeaderNonce), time.Now().Add(time.Minute))
	if err != nil && !errors.Is(err, ErrDenied) {
		return errors.Join(ErrUnavailable, err)
	}
	return err
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

func (s *Service) invalidateThrough(node int, work string, generation uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := workKey{node, work}
	for key := range s.authority {
		if key.workKey == k && key.generation <= generation {
			delete(s.authority, key)
		}
	}
	for key, check := range s.authorityChecks {
		if key.workKey == k && key.generation <= generation {
			check.revision++
		}
	}
	if v, ok := s.admissions[k]; ok && v.generation <= generation {
		if v.stop != nil {
			v.stop()
		}
		delete(s.admissions, k)
	}
}
func (s *Service) invalidateCached(node int, work string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireAuthorityLocked(workKey{node, work})
}
func (s *Service) expireAuthorityLocked(work workKey) {
	for key := range s.authority {
		if key.workKey == work {
			delete(s.authority, key)
		}
	}
	for key, check := range s.authorityChecks {
		if key.workKey == work {
			check.revision++
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

func (s *Service) executionStatus(ctx context.Context, node Node, a workerwire.Assignment) (bool, error) {
	if s.options.ExecutionStatus != nil {
		return s.options.ExecutionStatus(ctx, node, a)
	}
	if s.options.ExecutionStatuses != nil {
		allowed, err := s.options.ExecutionStatuses(ctx, node, []workerwire.Assignment{a})
		if err != nil {
			return false, err
		}
		if len(allowed) != 1 {
			return false, ErrUnavailable
		}
		return allowed[0], ctx.Err()
	}
	return s.executionActive(ctx, node, a), ctx.Err()
}

// WorkActive caches shared-store/registry checks for 250ms per authority.
// Begin/Stop invalidate this core's cache immediately; other core changes are
// observed within 250ms, below the two-second worker authority horizon.
func (s *Service) WorkActive(ctx context.Context, nodeID int, work string, generation uint64) bool {
	active, _ := s.workActive(ctx, nodeID, work, generation, false)
	return active
}

// WorkActiveStatus distinguishes dependency failure from a confirmed inactive
// scope. Its entries cannot reuse a compatibility bool reader's false result.
func (s *Service) WorkActiveStatus(ctx context.Context, nodeID int, work string, generation uint64) (bool, error) {
	return s.workActive(ctx, nodeID, work, generation, true)
}

func (s *Service) workActive(ctx context.Context, nodeID int, work string, generation uint64, typed bool) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if generation == 0 {
		return false, nil
	}
	k := authorityKey{workKey: workKey{nodeID, work}, generation: generation, typed: typed}
	for range authorityCheckAttempts {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		s.mu.Lock()
		if cached, ok := s.authority[k]; ok && time.Now().Before(cached.until) {
			s.mu.Unlock()
			return cached.active && ctx.Err() == nil, ctx.Err()
		}
		check, ok := s.authorityChecks[k]
		if !ok {
			if len(s.authorityChecks) >= authorityCacheLimit {
				s.mu.Unlock()
				return false, ErrUnavailable
			}
			check = &authorityCheck{}
			s.authorityChecks[k] = check
		}
		check.users++
		revision := check.revision
		s.mu.Unlock()

		var active bool
		var lookupErr error
		if typed {
			active, lookupErr = s.lookupWorkActiveStatus(ctx, nodeID, work, generation)
		} else {
			active = s.lookupWorkActive(ctx, nodeID, work, generation)
		}

		s.mu.Lock()
		check.users--
		if check.users == 0 {
			delete(s.authorityChecks, k)
		}
		if ctx.Err() != nil {
			s.mu.Unlock()
			return false, ctx.Err()
		}
		if revision != check.revision {
			// Only a change to this exact authority invalidates its lookup.
			// Recheck current state: admission completion need not revoke it.
			s.mu.Unlock()
			continue
		}
		if lookupErr != nil {
			s.mu.Unlock()
			return false, lookupErr
		}
		if len(s.authority) >= authorityCacheLimit {
			clear(s.authority)
		}
		s.authority[k] = authorityResult{active: active, generation: generation, until: time.Now().Add(250 * time.Millisecond)}
		s.mu.Unlock()
		return active, nil
	}
	return false, ErrUnavailable
}

const authorityCacheLimit = 10000
const authorityCheckAttempts = 3

func (s *Service) lookupWorkActive(ctx context.Context, nodeID int, work string, generation uint64) bool {
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
	return active
}

func (s *Service) lookupWorkActiveStatus(ctx context.Context, nodeID int, work string, generation uint64) (bool, error) {
	node, found, err := s.options.Nodes.ByID(ctx, nodeID)
	if err != nil || !found || !node.Enabled {
		return false, err
	}
	active, err := s.options.Store.IsActive(ctx, nodeID, work, generation)
	if err != nil || !active {
		return false, err
	}
	candidate, pending, err := s.options.Store.Candidate(ctx, nodeID, work)
	if err != nil {
		return false, err
	}
	if pending && candidate.Generation == generation {
		return s.pendingSnapshotStatus(ctx, nodeID, work, generation)
	}
	a, ready, err := s.options.Store.Get(ctx, nodeID, work)
	if err != nil || !ready || a.Generation != generation {
		return false, err
	}
	return s.executionStatus(ctx, node, a)
}
