package streamtelemetry

import (
	"context"
	"fmt"
	"sort"
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
	// SessionsTruncated and TransfersTruncated are separate because only one of
	// them is a completeness claim. A reader that could not take every session —
	// or every publisher — is blind to sessions, which BuildGlobalView must turn
	// into an incomplete view. A reader that could not take every transfer is not:
	// transfers feed no LiveByteFacts, and a transfer identity is minted partly
	// from client-supplied input, so one client could otherwise fill the aggregate
	// transfer budget and switch ghost detection off for the whole fleet. That is
	// the same reasoning Registry.dropTransfer applies on the publisher side; the
	// reader side has to apply it too or the fix is only half done.
	SessionsTruncated  bool
	TransfersTruncated bool
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

// LocalHub is an in-process stand-in for Redis: several publishers in one
// process publish into it, and any of them can read the merged set back.
//
// LocalStore deliberately holds exactly ONE snapshot, which was right while a
// process was a single publisher. It stopped being right when each API process
// gained a reporting publisher alongside its measuring one: two LocalStores mean
// the reported sessions are written somewhere nothing reads, so a non-distributed
// deployment would serve a "complete" view with every paused and pre-delivery
// viewer missing from it.
//
// Handles are bound to a publisher id so Leave can remove one publisher without
// disturbing the other.
type LocalHub struct {
	mu        sync.RWMutex
	snapshots map[string]Snapshot
}

func NewLocalHub() *LocalHub {
	return &LocalHub{snapshots: make(map[string]Snapshot, 2)}
}

// Store returns this publisher's handle onto the hub.
func (h *LocalHub) Store(publisherID string) GlobalSnapshotStore {
	return &localHubStore{hub: h, publisherID: publisherID}
}

type localHubStore struct {
	hub         *LocalHub
	mu          sync.Mutex
	publisherID string
}

func (s *localHubStore) Publish(_ context.Context, snapshot Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if snapshot.PublisherID == "" {
		snapshot.PublisherID = s.publisherID
	} else if s.publisherID == "" {
		// NewRegistry generates an id when Config.PublisherID is empty, after
		// startup has already constructed this handle. Bind on the first real
		// snapshot, just as RedisStore does.
		s.publisherID = snapshot.PublisherID
	} else if snapshot.PublisherID != s.publisherID {
		return fmt.Errorf("stream telemetry publisher id changed from %q to %q", s.publisherID, snapshot.PublisherID)
	}
	s.hub.mu.Lock()
	s.hub.snapshots[s.publisherID] = cloneSnapshot(snapshot)
	s.hub.mu.Unlock()
	return nil
}

// LoadAll returns every publisher in the process, which is what makes the merge
// see measured and reported state together without Redis.
func (s *localHubStore) LoadAll(_ context.Context) (PublisherSet, error) {
	s.hub.mu.RLock()
	defer s.hub.mu.RUnlock()
	set := PublisherSet{}
	ids := make([]string, 0, len(s.hub.snapshots))
	for id := range s.hub.snapshots {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		snapshot := cloneSnapshot(s.hub.snapshots[id])
		if snapshot.PublisherID == "" {
			continue
		}
		set.Members = append(set.Members, Member{PublisherID: snapshot.PublisherID, LastHeartbeat: snapshot.CapturedAt})
		set.Snapshots = append(set.Snapshots, snapshot)
	}
	return set, nil
}

// Leave removes only this publisher, so one component shutting down does not
// blank the other's contribution.
func (s *localHubStore) Leave(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hub.mu.Lock()
	delete(s.hub.snapshots, s.publisherID)
	s.hub.mu.Unlock()
	return nil
}

func (s *localHubStore) Load(_ context.Context) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hub.mu.RLock()
	defer s.hub.mu.RUnlock()
	return cloneSnapshot(s.hub.snapshots[s.publisherID]), nil
}
