package workersource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"
)

type MemoryStore struct {
	mu      sync.Mutex
	records map[string]Record
	revoked map[string]time.Time
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{records: make(map[string]Record), revoked: make(map[string]time.Time)}
}
func workKey(node int, work string, generation uint64) string {
	b, _ := json.Marshal(struct {
		Node       int
		Work       string
		Generation uint64
	}{node, work, generation})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func (s *MemoryStore) purge(now time.Time) {
	for key, r := range s.records {
		if !now.Before(r.ExpiresAt) {
			delete(s.records, key)
		}
	}
	for key, until := range s.revoked {
		if !now.Before(until) {
			delete(s.revoked, key)
		}
	}
}
func (s *MemoryStore) Create(ctx context.Context, r Record, l Limits) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purge(time.Now())
	if _, ok := s.revoked[workKey(r.Source.NodeID, r.Source.WorkID, r.Source.Generation)]; ok {
		return ErrInactive
	}
	if _, ok := s.records[r.Source.Grant]; ok {
		return ErrDenied
	}
	var bytes int64
	for _, existing := range s.records {
		bytes += existing.Source.Size
	}
	if len(s.records) >= l.MaxGrants || r.Source.Size > l.MaxSourceBytes-bytes {
		return ErrQuota
	}
	s.records[r.Source.Grant] = r
	return nil
}
func (s *MemoryStore) Get(ctx context.Context, grant string) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[grant]
	if !ok {
		return Record{}, ErrNotFound
	}
	return r, nil
}
func (s *MemoryStore) Extend(ctx context.Context, grant string, expires time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[grant]
	if !ok {
		return ErrNotFound
	}
	if !time.Now().Before(r.ExpiresAt) {
		return ErrExpired
	}
	if _, ok := s.revoked[workKey(r.Source.NodeID, r.Source.WorkID, r.Source.Generation)]; ok {
		return ErrInactive
	}
	r.ExpiresAt = expires
	s.records[grant] = r
	return nil
}
func (s *MemoryStore) Delete(ctx context.Context, grant string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.records, grant)
	return nil
}
func (s *MemoryStore) RevokeWork(ctx context.Context, node int, work string, generation uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := workKey(node, work, generation)
	s.revoked[key] = time.Now().Add(DefaultTTL)
	for id, r := range s.records {
		if workKey(r.Source.NodeID, r.Source.WorkID, r.Source.Generation) == key {
			delete(s.records, id)
		}
	}
	return nil
}
