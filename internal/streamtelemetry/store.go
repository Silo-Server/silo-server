package streamtelemetry

import (
	"context"
	"sync"
	"time"
)

const (
	publisherMetaField              = "meta"
	publisherReasonDecode           = "decode"
	publisherReasonOversized        = "oversized"
	publisherReasonMetaMissing      = "meta_missing"
	publisherReasonIdentityMismatch = "identity_mismatch"
	publisherReasonCountMismatch    = "count_mismatch"
	identityFieldSubject            = "subject"
	identityFieldProfileID          = "profile_id"
	identityFieldMediaFileID        = "media_file_id"
)

type SnapshotStore interface {
	Publish(context.Context, Snapshot) error
	Load(context.Context) (Snapshot, error)
}

type Member struct {
	PublisherID   string
	LastHeartbeat time.Time
}

type PublisherError struct {
	PublisherID  string
	DecodeErrors int
	Reason       string
}

type PublisherSet struct {
	Members   []Member
	Snapshots []Snapshot
	Errors    []PublisherError
	Truncated bool
}

type GlobalSnapshotStore interface {
	SnapshotStore
	LoadAll(context.Context) (PublisherSet, error)
	Leave(context.Context) error
}

type LocalStore struct {
	mu       sync.RWMutex
	snapshot Snapshot
	departed bool
}

func NewLocalStore() *LocalStore { return &LocalStore{} }

func (s *LocalStore) Publish(_ context.Context, snapshot Snapshot) error {
	s.mu.Lock()
	s.snapshot = cloneSnapshot(snapshot)
	s.departed = false
	s.mu.Unlock()
	return nil
}

func (s *LocalStore) LoadAll(_ context.Context) (PublisherSet, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.departed || s.snapshot.PublisherID == "" {
		return PublisherSet{}, nil
	}
	snapshot := cloneSnapshot(s.snapshot)
	return PublisherSet{Members: []Member{{PublisherID: snapshot.PublisherID, LastHeartbeat: snapshot.CapturedAt}}, Snapshots: []Snapshot{snapshot}}, nil
}

func (s *LocalStore) Leave(_ context.Context) error {
	s.mu.Lock()
	s.snapshot = Snapshot{}
	s.departed = true
	s.mu.Unlock()
	return nil
}

func (s *LocalStore) Load(_ context.Context) (Snapshot, error) {
	s.mu.RLock()
	snapshot := cloneSnapshot(s.snapshot)
	s.mu.RUnlock()
	return snapshot, nil
}
