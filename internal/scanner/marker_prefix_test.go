package scanner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/blobstore/blobstoretest"
)

// countingMarkerStore counts every Get and List, including misses, which the
// in-memory store does not record.
type countingMarkerStore struct {
	*blobstoretest.Memory
	gets    atomic.Int64
	lists   atomic.Int64
	listErr error
	// listHook, when set, runs before each List and can block or fail it.
	listHook func(ctx context.Context) error
}

func newCountingMarkerStore() *countingMarkerStore {
	return &countingMarkerStore{Memory: blobstoretest.New()}
}

func (s *countingMarkerStore) Get(ctx context.Context, key string) (io.ReadCloser, blobstore.ObjectInfo, error) {
	s.gets.Add(1)
	return s.Memory.Get(ctx, key)
}

func (s *countingMarkerStore) List(ctx context.Context, prefix, cursor string, limit int) ([]blobstore.ObjectInfo, string, error) {
	s.lists.Add(1)
	if s.listErr != nil {
		return nil, "", s.listErr
	}
	if s.listHook != nil {
		if err := s.listHook(ctx); err != nil {
			return nil, "", err
		}
	}
	return s.Memory.List(ctx, prefix, cursor, limit)
}

const markerFetchFiles = 1000

func markerTestHash(i int) string { return fmt.Sprintf("%016x", i+1) }

// fetchMarkersForFiles runs the per-file marker lookup the way a scan does,
// once for each new or changed file, and returns how many found markers.
func fetchMarkersForFiles(t *testing.T, s *Scanner, n int) int {
	t.Helper()
	found := 0
	for i := range n {
		if s.fetchMarkers(context.Background(), markerTestHash(i)) != nil {
			found++
		}
	}
	return found
}

func TestFetchMarkersSkipsGetsWhileMarkerPrefixEmpty(t *testing.T) {
	store := newCountingMarkerStore()
	// Other asset namespaces are populated; only markers/ decides.
	if err := store.Put(context.Background(), "artwork/poster.jpg", []byte("x")); err != nil {
		t.Fatal(err)
	}
	s := &Scanner{artworkStore: store}

	if found := fetchMarkersForFiles(t, s, markerFetchFiles); found != 0 {
		t.Fatalf("found markers for %d files, want 0", found)
	}
	gets, lists := store.gets.Load(), store.lists.Load()
	t.Logf("%d files, empty markers/ prefix: %d GETs, %d LISTs", markerFetchFiles, gets, lists)
	if gets != 0 || lists != 1 {
		t.Fatalf("got %d GETs and %d LISTs, want 0 GETs and 1 LIST", gets, lists)
	}
}

func TestFetchMarkersReadsEveryFileWhenMarkersExist(t *testing.T) {
	store := newCountingMarkerStore()
	key := "markers/" + markerTestHash(7) + ".json"
	if err := store.Put(context.Background(), key, []byte(`{"IntroStart":1.5,"IntroEnd":62}`)); err != nil {
		t.Fatal(err)
	}
	s := &Scanner{artworkStore: store}

	if found := fetchMarkersForFiles(t, s, markerFetchFiles); found != 1 {
		t.Fatalf("found markers for %d files, want 1", found)
	}
	gets, lists := store.gets.Load(), store.lists.Load()
	t.Logf("%d files, 1 marker object: %d GETs, %d LISTs", markerFetchFiles, gets, lists)
	if gets != markerFetchFiles || lists != 1 {
		t.Fatalf("got %d GETs and %d LISTs, want %d GETs and 1 LIST", gets, lists, markerFetchFiles)
	}
	markers := s.fetchMarkers(context.Background(), markerTestHash(7))
	if markers == nil || markers.IntroStart == nil || *markers.IntroStart != 1.5 || markers.IntroEnd == nil || *markers.IntroEnd != 62 {
		t.Fatalf("markers = %+v, want intro 1.5-62", markers)
	}
}

func TestFetchMarkersRechecksEmptyPrefixAfterTTL(t *testing.T) {
	store := newCountingMarkerStore()
	s := &Scanner{artworkStore: store}
	hash := markerTestHash(0)

	if s.fetchMarkers(context.Background(), hash) != nil {
		t.Fatal("found markers in an empty store")
	}
	// An external producer writes its first marker after this node checked.
	if err := store.Put(context.Background(), "markers/"+hash+".json", []byte(`{"CreditsStart":1200}`)); err != nil {
		t.Fatal(err)
	}
	if s.fetchMarkers(context.Background(), hash) != nil {
		t.Fatal("read markers before the empty-prefix check expired")
	}
	s.markerPrefix.checkedAt = s.markerPrefix.checkedAt.Add(-markerPrefixCheckTTL)
	markers := s.fetchMarkers(context.Background(), hash)
	if markers == nil || markers.CreditsStart == nil || *markers.CreditsStart != 1200 {
		t.Fatalf("markers after the check expired = %+v, want credits at 1200", markers)
	}
	if lists := store.lists.Load(); lists != 2 {
		t.Fatalf("got %d LISTs, want 2", lists)
	}
}

func TestMarkerPrefixEmptyOnLocalStore(t *testing.T) {
	ctx := context.Background()
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, "artwork/poster.jpg", []byte("x")); err != nil {
		t.Fatal(err)
	}
	s := &Scanner{artworkStore: store}
	if !s.markerPrefixEmpty(ctx) {
		t.Fatal("local store without markers/ reported markers")
	}
	if err := store.Put(ctx, "markers/"+markerTestHash(0)+".json", []byte(`{"IntroEnd":30}`)); err != nil {
		t.Fatal(err)
	}
	s.markerPrefix.checkedAt = time.Time{}
	if s.markerPrefixEmpty(ctx) {
		t.Fatal("local store with a marker reported an empty prefix")
	}
}

func TestFetchMarkersFallsBackToGetsWhenListFails(t *testing.T) {
	store := newCountingMarkerStore()
	store.listErr = errors.New("list denied")
	key := "markers/" + markerTestHash(3) + ".json"
	if err := store.Put(context.Background(), key, []byte(`{"IntroEnd":30}`)); err != nil {
		t.Fatal(err)
	}
	s := &Scanner{artworkStore: store}

	if found := fetchMarkersForFiles(t, s, markerFetchFiles); found != 1 {
		t.Fatalf("found markers for %d files, want 1", found)
	}
	// A failed LIST falls back to today's per-file GET and is not retried for
	// every file.
	if gets, lists := store.gets.Load(), store.lists.Load(); gets != markerFetchFiles || lists != 1 {
		t.Fatalf("got %d GETs and %d LISTs, want %d GETs and 1 LIST", gets, lists, markerFetchFiles)
	}
}

// blockingListStore returns a store whose List blocks until its context ends,
// like a request to a server that accepted the connection and never answers.
// The returned channel receives once each List starts.
func blockingListStore() (*countingMarkerStore, <-chan struct{}) {
	store := newCountingMarkerStore()
	entered := make(chan struct{}, 16)
	store.listHook = func(ctx context.Context) error {
		entered <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}
	return store, entered
}

// awaitResult fails the test if a marker prefix check does not return within
// limit, which is far shorter than the blocked LIST it must not wait on.
func awaitResult(t *testing.T, res <-chan bool, limit time.Duration, what string) bool {
	t.Helper()
	select {
	case v := <-res:
		return v
	case <-time.After(limit):
		t.Fatalf("%s did not return within %s", what, limit)
		return false
	}
}

func TestMarkerPrefixCheckWaitersHonorTheirContext(t *testing.T) {
	store, entered := blockingListStore()
	s := &Scanner{artworkStore: store}
	// Only the leader's own context ends this LIST.
	s.markerPrefix.listTimeout = time.Hour

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	defer cancelLeader()
	leader := make(chan bool, 1)
	go func() { leader <- s.markerPrefixEmpty(leaderCtx) }()
	<-entered

	// A worker from a canceled scan leaves at once instead of waiting on
	// another scan's LIST.
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	waiter := make(chan bool, 1)
	go func() { waiter <- s.markerPrefixEmpty(canceled) }()
	if awaitResult(t, waiter, 2*time.Second, "canceled waiter") {
		t.Fatal("canceled waiter reported an empty prefix")
	}

	cancelLeader()
	if awaitResult(t, leader, 2*time.Second, "canceled leader") {
		t.Fatal("canceled leader reported an empty prefix")
	}
	// A check its caller abandoned records nothing, so the next caller lists
	// again.
	if !s.markerPrefix.checkedAt.IsZero() || s.markerPrefix.checking != nil {
		t.Fatalf("canceled check left state: checkedAt=%v checking=%v", s.markerPrefix.checkedAt, s.markerPrefix.checking)
	}
	store.listHook = nil
	if !s.markerPrefixEmpty(context.Background()) {
		t.Fatal("empty store reported markers after a canceled check")
	}
	if lists := store.lists.Load(); lists != 2 {
		t.Fatalf("got %d LISTs, want 2", lists)
	}
}

func TestMarkerPrefixCheckTimesOutToPerFileReads(t *testing.T) {
	store, entered := blockingListStore()
	s := &Scanner{artworkStore: store}
	const timeout = 50 * time.Millisecond
	s.markerPrefix.listTimeout = timeout
	hash := markerTestHash(0)
	if err := store.Put(context.Background(), "markers/"+hash+".json", []byte(`{"IntroEnd":30}`)); err != nil {
		t.Fatal(err)
	}

	leader := make(chan bool, 1)
	go func() { leader <- s.fetchMarkers(context.Background(), hash) != nil }()
	<-entered
	// A second worker waits on the hung LIST only until its timeout, then
	// reads its file directly, as it does today.
	waiter := make(chan bool, 1)
	go func() { waiter <- s.fetchMarkers(context.Background(), hash) != nil }()

	limit := timeout + 5*time.Second
	if !awaitResult(t, leader, limit, "worker running the hung LIST") {
		t.Fatal("worker running the hung LIST did not read the marker")
	}
	if !awaitResult(t, waiter, limit, "worker waiting on the hung LIST") {
		t.Fatal("worker waiting on the hung LIST did not read the marker")
	}
	if !s.markerPrefix.listFailed {
		t.Fatal("timed-out LIST was not recorded as failed")
	}

	// The timeout is cached like any failed LIST: later files GET without
	// listing again.
	fetchMarkersForFiles(t, s, 10)
	if gets, lists := store.gets.Load(), store.lists.Load(); gets != 12 || lists != 1 {
		t.Fatalf("got %d GETs and %d LISTs, want 12 GETs and 1 LIST", gets, lists)
	}
}
