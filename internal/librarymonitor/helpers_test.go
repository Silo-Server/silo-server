package librarymonitor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scantrigger"
)

const waitTimeout = 10 * time.Second

func quietLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// fakeFolders is a FolderLister and a scantrigger.FolderRepository.
type fakeFolders struct {
	mu      sync.Mutex
	folders []*models.MediaFolder
}

func (f *fakeFolders) set(folders ...*models.MediaFolder) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.folders = folders
}

func (f *fakeFolders) List(context.Context) ([]*models.MediaFolder, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*models.MediaFolder, 0, len(f.folders))
	for _, folder := range f.folders {
		copied := *folder
		copied.Paths = append([]string(nil), folder.Paths...)
		out = append(out, &copied)
	}
	return out, nil
}

func (f *fakeFolders) GetByID(ctx context.Context, id int) (*models.MediaFolder, error) {
	folders, _ := f.List(ctx)
	for _, folder := range folders {
		if folder.ID == id {
			return folder, nil
		}
	}
	return nil, catalog.ErrFolderNotFound
}

func library(id int, paths ...string) *models.MediaFolder {
	return &models.MediaFolder{
		ID: id, Name: fmt.Sprintf("Library %d", id), Type: "movies", Paths: paths,
		Enabled: true, RealtimeMonitoring: true, SortOrder: id,
	}
}

// fakeQueue records every EnqueueScans batch and hands it to receipts.
type fakeQueue struct {
	receipts chan []scantrigger.Target
	mu       sync.Mutex
	failures int
}

func newFakeQueue() *fakeQueue {
	return &fakeQueue{receipts: make(chan []scantrigger.Target, 1024)}
}

func (q *fakeQueue) EnqueueScans(_ context.Context, targets []scantrigger.Target) error {
	q.mu.Lock()
	fail := q.failures > 0
	if fail {
		q.failures--
	}
	q.mu.Unlock()
	batch := append([]scantrigger.Target(nil), targets...)
	q.receipts <- batch
	if fail {
		return errors.New("queue unavailable")
	}
	return nil
}

// targetKey renders a target for assertions.
func targetKey(t scantrigger.Target) string {
	id := 0
	if t.Folder != nil {
		id = t.Folder.ID
	}
	return fmt.Sprintf("%d %s %s %s", id, t.Mode, t.Path, t.Trigger)
}

func batchKeys(batch []scantrigger.Target) []string {
	keys := make([]string, 0, len(batch))
	for _, t := range batch {
		keys = append(keys, targetKey(t))
	}
	sort.Strings(keys)
	return keys
}

// fakeStatus records node reports.
type fakeStatus struct {
	reports chan []LibraryStatus
	mu      sync.Mutex
	removed []string
}

func newFakeStatus() *fakeStatus {
	return &fakeStatus{reports: make(chan []LibraryStatus, 1024)}
}

func (s *fakeStatus) Report(_ context.Context, _ string, statuses []LibraryStatus) error {
	s.reports <- append([]LibraryStatus(nil), statuses...)
	return nil
}

func (s *fakeStatus) RemoveNode(_ context.Context, nodeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removed = append(s.removed, nodeID)
	return nil
}

// waitStatus waits for a report that satisfies ok and returns it.
func waitStatus(t *testing.T, s *fakeStatus, what string, ok func([]LibraryStatus) bool) []LibraryStatus {
	t.Helper()
	deadline := time.After(waitTimeout)
	var last []LibraryStatus
	for {
		select {
		case rows := <-s.reports:
			last = rows
			if ok(rows) {
				return rows
			}
		case <-deadline:
			t.Fatalf("timed out waiting for status %s; last report %+v", what, last)
		}
	}
}

func statusOf(rows []LibraryStatus, libraryID int) (LibraryStatus, bool) {
	for _, row := range rows {
		if row.LibraryID == libraryID {
			return row, true
		}
	}
	return LibraryStatus{}, false
}

func hasState(libraryID int, state State) func([]LibraryStatus) bool {
	return func(rows []LibraryStatus) bool {
		row, ok := statusOf(rows, libraryID)
		return ok && row.State == state
	}
}

// fakeBackend is a Backend driven by the test.
type fakeBackend struct {
	name   string
	events chan Event
	calls  chan string

	mu     sync.Mutex
	roots  map[string]int
	addErr map[string]error
	block  map[string]chan struct{}
	// removeBlock holds RemoveRoot for a root, as a hung mount would, after
	// it reported "removing <root>".
	removeBlock map[string]chan struct{}
	adds        map[string]int
	removes     map[string]int
	closed      bool
}

func newFakeBackend(name string) *fakeBackend {
	return &fakeBackend{
		name:        name,
		events:      make(chan Event, 64),
		calls:       make(chan string, 1024),
		roots:       make(map[string]int),
		addErr:      make(map[string]error),
		block:       make(map[string]chan struct{}),
		removeBlock: make(map[string]chan struct{}),
		adds:        make(map[string]int),
		removes:     make(map[string]int),
	}
}

func (b *fakeBackend) Name() string { return b.name }

func (b *fakeBackend) AddRoot(ctx context.Context, root string) error {
	b.mu.Lock()
	b.adds[root]++
	gate := b.block[root]
	err := b.addErr[root]
	b.mu.Unlock()
	b.calls <- "add " + root
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.roots[root] = 3
	b.mu.Unlock()
	return nil
}

func (b *fakeBackend) RemoveRoot(root string) {
	b.mu.Lock()
	b.removes[root]++
	gate := b.removeBlock[root]
	b.mu.Unlock()
	if gate != nil {
		b.calls <- "removing " + root
		<-gate
	}
	b.mu.Lock()
	delete(b.roots, root)
	b.mu.Unlock()
	b.calls <- "remove " + root
}

func (b *fakeBackend) Directories(root string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.roots[root]
}

func (b *fakeBackend) Events() <-chan Event { return b.events }

func (b *fakeBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed {
		b.closed = true
		close(b.events)
	}
	return nil
}

func (b *fakeBackend) addCount(root string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.adds[root]
}

// waitCall waits for backend calls ("add <root>" or "remove <root>") in any
// order; other calls are skipped.
func waitCall(t *testing.T, b *fakeBackend, want ...string) {
	t.Helper()
	missing := make(map[string]bool, len(want))
	for _, call := range want {
		missing[call] = true
	}
	deadline := time.After(waitTimeout)
	var seen []string
	for len(missing) > 0 {
		select {
		case call := <-b.calls:
			delete(missing, call)
			seen = append(seen, call)
		case <-deadline:
			t.Fatalf("timed out waiting for %v on %s; saw %s", want, b.name, strings.Join(seen, ", "))
		}
	}
}

// fakeResolver resolves by a per-test function and counts calls.
type fakeResolver struct {
	mu    sync.Mutex
	calls map[string]int
	fn    func(kind changeKind, path string) (*scantrigger.Target, error)
}

func (r *fakeResolver) record(kind changeKind, path string) (*scantrigger.Target, error) {
	r.mu.Lock()
	if r.calls == nil {
		r.calls = make(map[string]int)
	}
	r.calls[path]++
	r.mu.Unlock()
	return r.fn(kind, path)
}

func (r *fakeResolver) Resolve(_ context.Context, req scantrigger.Request) (*scantrigger.Target, error) {
	return r.record(changeFile, req.Path)
}

func (r *fakeResolver) ResolveVanishedPath(_ context.Context, path, _ string) (*scantrigger.Target, error) {
	return r.record(changeVanishedFile, path)
}

func (r *fakeResolver) ResolveMissingSubtree(_ context.Context, path, _ string) (*scantrigger.Target, error) {
	return r.record(changeVanishedDir, path)
}

func (r *fakeResolver) count(path string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[path]
}

// testConfig returns a config with millisecond timings and fakes.
func testConfig(folders *fakeFolders, queue *fakeQueue, status *fakeStatus) Config {
	return Config{
		NodeID:            "node-test",
		Folders:           folders,
		Queue:             queue,
		Status:            status,
		Logger:            quietLogger(),
		ServerEnabled:     true,
		QuietWindow:       40 * time.Millisecond,
		FlushInterval:     10 * time.Millisecond,
		CreatedFallback:   time.Hour,
		StablePoll:        time.Hour,
		ReconcileInterval: time.Hour,
		StatusRefresh:     time.Hour,
		WalkStall:         time.Hour,
	}
}

func startMonitor(t *testing.T, cfg Config) *Monitor {
	t.Helper()
	m, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	m.Start(context.Background())
	t.Cleanup(m.Stop)
	return m
}
