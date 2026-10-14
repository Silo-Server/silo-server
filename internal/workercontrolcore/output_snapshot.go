package workercontrolcore

import (
	"context"
	"errors"
	"time"

	"github.com/Silo-Server/silo-server/internal/workeridentity"
)

// MaxOutputVerificationTokens matches the bounded private authority registry.
const MaxOutputVerificationTokens = maxOutputAuthorities

type outputSnapshot struct {
	records         []OutputAuthorityRecord
	assignments     AuthoritySnapshot
	active, pending map[string]uint64
}

// Capture viewer records first. A later assignment publication can otherwise
// look definitively missing against an assignment snapshot taken too early.
func (s *Service) readOutputSnapshot(ctx context.Context, node Node) (outputSnapshot, error) {
	state := outputSnapshot{active: map[string]uint64{}, pending: map[string]uint64{}}
	var err error
	if s.options.OutputAuthorities != nil {
		state.records, err = s.options.OutputAuthorities.List(ctx, node.ID)
		if err != nil {
			return outputSnapshot{}, err
		}
		if len(state.records) > maxOutputAuthorities {
			return outputSnapshot{}, ErrUnavailable
		}
	}
	state.assignments, err = s.options.Store.Snapshot(ctx, node.ID)
	if err != nil {
		return outputSnapshot{}, err
	}
	for work, a := range state.assignments.Active {
		if a.NodeID != node.ID || a.WorkID != work || a.Generation == 0 {
			return outputSnapshot{}, ErrUnavailable
		}
		if err = ctx.Err(); err != nil {
			return outputSnapshot{}, err
		}
		var allowed bool
		if s.options.ExecutionStatus != nil {
			allowed, err = s.options.ExecutionStatus(ctx, node, a)
		} else {
			allowed = s.executionActive(ctx, node, a)
		}
		if err != nil {
			return outputSnapshot{}, err
		}
		if allowed {
			state.active[work] = a.Generation
		}
	}
	for work, a := range state.assignments.Pending {
		if a.NodeID != node.ID || a.WorkID != work || a.Generation == 0 {
			return outputSnapshot{}, ErrUnavailable
		}
		owned, err := s.pendingSnapshotStatus(ctx, node.ID, work, a.Generation)
		if err != nil {
			return outputSnapshot{}, err
		}
		if owned {
			state.pending[work] = a.Generation
		}
	}
	return state, ctx.Err()
}

func (s *Service) pendingSnapshotStatus(ctx context.Context, node int, work string, generation uint64) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	v, ok := s.admissions[workKey{node, work}]
	local := ok && v.generation == generation && v.count > 0 && v.ctx.Err() == nil
	s.mu.Unlock()
	if local {
		return true, nil
	}
	leases, ok := s.options.Store.(AdmissionLeases)
	if !ok {
		return false, nil
	}
	return leases.HasLease(ctx, node, work, generation)
}

// HasInFlightStatus preserves admission grace while exposing failed shared
// lease reads to snapshot callers instead of turning them into a new denial.
func (s *Service) HasInFlightStatus(ctx context.Context, node int, work string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	v, ok := s.admissions[workKey{node, work}]
	local := ok && (v.count > 0 && v.ctx.Err() == nil || time.Now().Before(v.until))
	s.mu.Unlock()
	if local {
		return true, nil
	}
	leases, ok := s.options.Store.(AdmissionLeases)
	if !ok {
		return false, nil
	}
	return leases.HasLease(ctx, node, work, 0)
}

func (s *Service) verifyOutputTokens(ctx context.Context, tokens []string) ([]TokenVerification, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return nil, nil
	}
	if s.options.VerifyTokens != nil {
		results, err := s.options.VerifyTokens(ctx, tokens)
		if err != nil {
			return nil, err
		}
		if len(results) != len(tokens) {
			return nil, ErrUnavailable
		}
		return results, ctx.Err()
	}
	if s.options.VerifyToken == nil {
		return nil, ErrUnavailable
	}
	results := make([]TokenVerification, len(tokens))
	for i, token := range tokens {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		results[i].Claims, results[i].Err = s.options.VerifyToken(ctx, token)
	}
	return results, ctx.Err()
}

func (s *Service) outputsForSnapshot(ctx context.Context, node Node, state outputSnapshot) (map[string]bool, error) {
	out := make(map[string]bool)
	var eligible, retired []OutputAuthorityRecord
	var tokens []string
	for _, record := range state.records {
		// Match Get's active-first, ready-pending fallback semantics. A newer
		// candidate does not replace a still-live generation for viewer output.
		a, found := state.assignments.Active[record.WorkID]
		if !found {
			a, found = state.assignments.Pending[record.WorkID]
		}
		if !found || a.Generation != record.Generation {
			retired = append(retired, record)
			continue
		}
		eligible = append(eligible, record)
		tokens = append(tokens, record.Token)
	}
	verified, err := s.verifyOutputTokens(ctx, tokens)
	if err != nil {
		return nil, err
	}
	for i, record := range eligible {
		result := verified[i]
		if errors.Is(result.Err, ErrOutputRevoked) {
			retired = append(retired, record)
			continue
		}
		if result.Err != nil {
			if errors.Is(result.Err, ErrDenied) {
				continue
			}
			return nil, result.Err
		}
		claims := result.Claims
		allowed := state.active[record.WorkID] == record.Generation
		if _, live := state.assignments.Active[record.WorkID]; !live {
			allowed = state.pending[record.WorkID] == record.Generation
		}
		if claims == nil || !claims.Bound() || !workeridentity.SameOrigin(claims.TranscodeNode, node.URL, node.AllowHTTP) || claims.RoutingExecutionNodeID != 0 && claims.RoutingExecutionNodeID != node.ID || ExtractOutputWork(claims) != record.WorkID || claims.ExpiresAt == nil || claims.ExpiresAt.Unix() != record.ExpiresAt || !time.Now().Before(claims.ExpiresAt.Time) || !allowed {
			continue
		}
		out[record.ID] = true
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	for _, record := range retired {
		if err = s.options.OutputAuthorities.Delete(ctx, record); err != nil {
			return nil, err
		}
	}
	return out, nil
}
