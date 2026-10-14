package workercontrolcore

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/workerwire"
)

var workPattern = regexp.MustCompile(`^[a-zA-Z0-9._:-]{1,160}$`)

func validWork(work string) bool { return workPattern.MatchString(work) && work != "." && work != ".." }
func validAssignment(a workerwire.Assignment) bool {
	return a.Version == 1 && a.NodeID > 0 && validWork(a.WorkID) && (a.JobID == "" || workerwire.ValidExecutionID(a.JobID)) && len(a.Body) <= 512<<10 && (len(a.Body) == 0 || json.Valid(a.Body))
}
func copyAssignment(a workerwire.Assignment) workerwire.Assignment {
	a.Body = append(json.RawMessage(nil), a.Body...)
	if a.Sources != nil {
		m := make(map[string]workerwire.Source, len(a.Sources))
		for k, v := range a.Sources {
			m[k] = v
		}
		a.Sources = m
	}
	return a
}

type record struct {
	assignment workerwire.Assignment
	ready      bool
	bindings   map[string]string
	lease      time.Time
}
type workKey struct {
	node int
	work string
}
type MemoryStore struct {
	mu               sync.Mutex
	live, candidates map[workKey]record
	versions         map[workKey]uint64
	nonces           map[string]time.Time
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{live: map[workKey]record{}, candidates: map[workKey]record{}, versions: map[workKey]uint64{}, nonces: map[string]time.Time{}}
}
func (s *MemoryStore) Begin(ctx context.Context, a workerwire.Assignment) (workerwire.Assignment, error) {
	if err := ctx.Err(); err != nil {
		return workerwire.Assignment{}, err
	}
	if !validAssignment(a) {
		return workerwire.Assignment{}, ErrDenied
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.beginLocked(a)
}
func (s *MemoryStore) beginLocked(a workerwire.Assignment) (workerwire.Assignment, error) {
	k := workKey{a.NodeID, a.WorkID}
	if previous, ok := s.live[k]; ok {
		a.JobID = previous.assignment.JobID
	} else if previous, ok := s.candidates[k]; ok {
		a.JobID = previous.assignment.JobID
	}
	if s.versions[k] >= 1<<53-1 {
		return workerwire.Assignment{}, ErrUnavailable
	}
	s.versions[k]++
	a.Generation = s.versions[k]
	a = copyAssignment(a)
	s.candidates[k] = record{assignment: a}
	return copyAssignment(a), nil
}
func (s *MemoryStore) Complete(ctx context.Context, a workerwire.Assignment) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validAssignment(a) {
		return ErrDenied
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := workKey{a.NodeID, a.WorkID}
	r, ok := s.candidates[k]
	if !ok || r.assignment.Generation != a.Generation || s.versions[k] != a.Generation {
		return ErrFenced
	}
	a.JobID = r.assignment.JobID
	s.candidates[k] = record{assignment: copyAssignment(a), ready: true, bindings: r.bindings, lease: r.lease}
	return nil
}
func (s *MemoryStore) Activate(ctx context.Context, node int, work string, generation uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := workKey{node, work}
	r, ok := s.candidates[k]
	if !ok || !r.ready || r.assignment.Generation != generation || s.versions[k] != generation {
		return ErrFenced
	}
	s.live[k] = r
	delete(s.candidates, k)
	return nil
}
func (s *MemoryStore) Get(ctx context.Context, node int, work string) (workerwire.Assignment, bool, error) {
	if err := ctx.Err(); err != nil {
		return workerwire.Assignment{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := workKey{node, work}
	r, ok := s.live[k]
	if !ok {
		r, ok = s.candidates[k]
	}
	if !ok || !r.ready {
		return workerwire.Assignment{}, false, nil
	}
	return copyAssignment(r.assignment), true, nil
}
func (s *MemoryStore) Candidate(ctx context.Context, node int, work string) (workerwire.Assignment, bool, error) {
	if err := ctx.Err(); err != nil {
		return workerwire.Assignment{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.candidates[workKey{node, work}]
	if !ok {
		return workerwire.Assignment{}, false, nil
	}
	return copyAssignment(r.assignment), true, nil
}
func (s *MemoryStore) generations(ctx context.Context, node int, pending bool) (map[string]uint64, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	records := s.live
	if pending {
		records = s.candidates
	}
	m := map[string]uint64{}
	for k, r := range records {
		if k.node == node && (pending || r.ready) {
			m[k.work] = r.assignment.Generation
		}
	}
	return m, nil
}
func (s *MemoryStore) Active(ctx context.Context, node int) (map[string]uint64, error) {
	return s.generations(ctx, node, false)
}
func (s *MemoryStore) Pending(ctx context.Context, node int) (map[string]uint64, error) {
	return s.generations(ctx, node, true)
}
func (s *MemoryStore) IsActive(ctx context.Context, node int, work string, generation uint64) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := workKey{node, work}
	for _, records := range []map[workKey]record{s.live, s.candidates} {
		if r, ok := records[k]; ok && generation > 0 && r.assignment.Generation == generation {
			return true, nil
		}
	}
	return false, nil
}
func (s *MemoryStore) Stop(ctx context.Context, node int, work string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if node <= 0 || !validWork(work) {
		return ErrDenied
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := workKey{node, work}
	if s.versions[k] >= 1<<53-1 {
		return ErrUnavailable
	}
	s.versions[k]++
	delete(s.live, k)
	delete(s.candidates, k)
	return nil
}

// Cancel never removes the healthy live generation or a newer candidate.
func (s *MemoryStore) Cancel(ctx context.Context, node int, work string, generation uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := workKey{node, work}
	r, ok := s.candidates[k]
	if !ok || r.assignment.Generation != generation {
		return ErrFenced
	}
	delete(s.candidates, k)
	return nil
}
func (s *MemoryStore) ClaimNonce(ctx context.Context, node int, nonce string, expires time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if node <= 0 || len(nonce) != 32 || !expires.After(time.Now()) || expires.After(time.Now().Add(time.Minute+time.Second)) {
		return ErrDenied
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for k, v := range s.nonces {
		if !now.Before(v) {
			delete(s.nonces, k)
		}
	}
	k := strconv.Itoa(node) + ":" + nonce
	if _, ok := s.nonces[k]; ok {
		return ErrDenied
	}
	if len(s.nonces) >= 65536 {
		return ErrUnavailable
	}
	s.nonces[k] = expires
	return nil
}
