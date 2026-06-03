package autoscan

import (
	"context"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scantrigger"
)

type fakeStore struct {
	settings   Settings
	sources    []Source
	connection Connection
	advanced   map[string]string // source ID -> marker
	recorded   map[string]string // source ID -> error message
	ensured    []DiscoveredSource
}

func (f *fakeStore) GetSettings(context.Context) (Settings, error) { return f.settings, nil }
func (f *fakeStore) ListEnabledSources(context.Context) ([]Source, error) {
	return f.sources, nil
}
func (f *fakeStore) GetConnection(context.Context, string) (Connection, error) {
	return f.connection, nil
}
func (f *fakeStore) EnsureSource(_ context.Context, installationID int, capabilityID string) error {
	f.ensured = append(f.ensured, DiscoveredSource{InstallationID: installationID, CapabilityID: capabilityID})
	return nil
}
func (f *fakeStore) AdvanceMarker(_ context.Context, sourceID, marker string) error {
	if f.advanced == nil {
		f.advanced = map[string]string{}
	}
	f.advanced[sourceID] = marker
	return nil
}
func (f *fakeStore) RecordError(_ context.Context, sourceID, msg string) error {
	if f.recorded == nil {
		f.recorded = map[string]string{}
	}
	f.recorded[sourceID] = msg
	return nil
}

// fakeProvider implements ScanSourceProvider, keyed by capability ID.
type fakeProvider struct {
	paths      map[string][]string // key: capabilityID
	nextMarker string
	err        error
}

func (f *fakeProvider) PollChanges(_ context.Context, _ int, capabilityID, _ string, _ ResolvedConnection) ([]string, string, error) {
	if f.err != nil {
		return nil, "", f.err
	}
	return f.paths[capabilityID], f.nextMarker, nil
}

// passthroughConnRes resolves to empty credentials; the engine doesn't inspect
// them in tests (the fake provider ignores conn).
type passthroughConnRes struct{}

func (passthroughConnRes) Resolve(context.Context, Connection) (ResolvedConnection, error) {
	return ResolvedConnection{}, nil
}

type fakeResolver struct{}

func (fakeResolver) Resolve(_ context.Context, req scantrigger.Request) (*scantrigger.Target, error) {
	if len(req.Path) >= 11 && req.Path[:11] == "/mnt/media/" {
		return &scantrigger.Target{Folder: &models.MediaFolder{ID: 7}, Mode: scantrigger.ModeSubtree, Path: req.Path, Trigger: req.Trigger}, nil
	}
	return nil, nil
}

type recordingQueuer struct{ enqueued []scantrigger.Target }

func (q *recordingQueuer) EnqueueScans(_ context.Context, targets []scantrigger.Target) error {
	q.enqueued = append(q.enqueued, targets...)
	return nil
}

type allowSuppressor struct{}

func (allowSuppressor) ShouldScan(context.Context, string, time.Duration) (bool, error) {
	return true, nil
}
func (allowSuppressor) Release(context.Context, string) error { return nil }

type recordingSuppressor struct {
	claimed  []string
	released []string
}

func (s *recordingSuppressor) ShouldScan(_ context.Context, key string, _ time.Duration) (bool, error) {
	s.claimed = append(s.claimed, key)
	return true, nil
}
func (s *recordingSuppressor) Release(_ context.Context, key string) error {
	s.released = append(s.released, key)
	return nil
}

type failingQueuer struct{}

func (failingQueuer) EnqueueScans(context.Context, []scantrigger.Target) error {
	return context.DeadlineExceeded
}

func newService(store Store, provider ScanSourceProvider, queue Queuer, suppress Suppressor) *Service {
	return NewService(store, provider, passthroughConnRes{}, fakeResolver{}, queue, suppress, nil)
}

// strptr is a tiny helper for the *string ConnectionID field in tests.
func strptr(s string) *string { return &s }

func TestPollOnceEnqueuesDedupedFolders(t *testing.T) {
	store := &fakeStore{
		settings: Settings{Enabled: true, DefaultPollIntervalSeconds: 600, DebounceSeconds: 60},
		sources: []Source{{
			ID: "s1", InstallationID: 1, CapabilityID: "arr", ConnectionID: strptr("c1"), Enabled: true,
		}},
	}
	prov := &fakeProvider{paths: map[string][]string{
		"arr": {
			"/mnt/media/Show/S01/E01.mkv",
			"/mnt/media/Show/S01/E02.mkv",
			"/outside/lib/x.mkv",
		},
	}, nextMarker: "m1"}
	q := &recordingQueuer{}
	svc := newService(store, prov, q, allowSuppressor{})
	if err := svc.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(q.enqueued) != 1 {
		t.Fatalf("expected 1 deduped folder scan, got %d: %+v", len(q.enqueued), q.enqueued)
	}
	if q.enqueued[0].Trigger != "autoscan" || q.enqueued[0].Folder.ID != 7 {
		t.Fatalf("unexpected target: %+v", q.enqueued[0])
	}
	if _, ok := store.advanced["s1"]; !ok {
		t.Fatalf("expected marker advanced for s1")
	}
}

func TestPollOnceScansDistinctPathsUnderSameFolder(t *testing.T) {
	// Two imported subtrees resolve to the SAME folder ID (7) but DIFFERENT
	// target paths. Keying suppression on folder ID alone would drop the second;
	// keying on (folder, path) must scan both.
	store := &fakeStore{
		settings: Settings{Enabled: true, DefaultPollIntervalSeconds: 600, DebounceSeconds: 60},
		sources: []Source{{
			ID: "s1", InstallationID: 1, CapabilityID: "arr", ConnectionID: strptr("c1"), Enabled: true,
		}},
	}
	prov := &fakeProvider{paths: map[string][]string{
		"arr": {
			"/mnt/media/ShowA/S01/E01.mkv",
			"/mnt/media/ShowB/S01/E01.mkv",
		},
	}, nextMarker: "m1"}
	q := &recordingQueuer{}
	sup := &recordingSuppressor{}
	svc := newService(store, prov, q, sup)
	if err := svc.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(q.enqueued) != 2 {
		t.Fatalf("expected 2 enqueued targets (distinct paths), got %d: %+v", len(q.enqueued), q.enqueued)
	}
	if len(sup.claimed) != 2 {
		t.Fatalf("expected 2 claimed keys, got %d: %v", len(sup.claimed), sup.claimed)
	}
	if sup.claimed[0] == sup.claimed[1] {
		t.Fatalf("expected distinct claimed keys, both were %q", sup.claimed[0])
	}
}

func TestPollOnceStoresOpaqueMarkerVerbatim(t *testing.T) {
	store := &fakeStore{
		settings: Settings{Enabled: true, DefaultPollIntervalSeconds: 600, DebounceSeconds: 60},
		sources: []Source{{
			ID: "s1", InstallationID: 1, CapabilityID: "arr", ConnectionID: strptr("c1"), Enabled: true,
		}},
	}
	const opaque = "eyJjdXJzb3IiOiJhYmMxMjMifQ==|2026-06-02T14:10:00Z"
	prov := &fakeProvider{paths: map[string][]string{
		"arr": {"/mnt/media/Show/S01/E01.mkv"},
	}, nextMarker: opaque}
	svc := newService(store, prov, &recordingQueuer{}, allowSuppressor{})
	if err := svc.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if got := store.advanced["s1"]; got != opaque {
		t.Fatalf("marker not stored verbatim: got %q want %q", got, opaque)
	}
}

func TestPollOnceDisabledNoop(t *testing.T) {
	store := &fakeStore{settings: Settings{Enabled: false}}
	q := &recordingQueuer{}
	svc := newService(store, &fakeProvider{}, q, allowSuppressor{})
	if err := svc.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(q.enqueued) != 0 {
		t.Fatalf("disabled autoscan should enqueue nothing, got %d", len(q.enqueued))
	}
}

func TestPollOnceProviderErrorKeepsMarker(t *testing.T) {
	store := &fakeStore{
		settings: Settings{Enabled: true, DefaultPollIntervalSeconds: 600, DebounceSeconds: 60},
		sources:  []Source{{ID: "s1", InstallationID: 1, CapabilityID: "arr", ConnectionID: strptr("c1"), Enabled: true}},
	}
	prov := &fakeProvider{err: context.DeadlineExceeded}
	q := &recordingQueuer{}
	svc := newService(store, prov, q, allowSuppressor{})
	if err := svc.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce should not propagate per-source error: %v", err)
	}
	if _, ok := store.advanced["s1"]; ok {
		t.Fatalf("marker must NOT advance on provider failure")
	}
	if msg, ok := store.recorded["s1"]; !ok || msg == "" {
		t.Fatalf("expected provider error recorded for s1, got %q ok=%v", msg, ok)
	}
}

func TestPollOnceReleasesClaimOnEnqueueFailure(t *testing.T) {
	store := &fakeStore{
		settings: Settings{Enabled: true, DefaultPollIntervalSeconds: 600, DebounceSeconds: 60},
		sources:  []Source{{ID: "s1", InstallationID: 1, CapabilityID: "arr", ConnectionID: strptr("c1"), Enabled: true}},
	}
	prov := &fakeProvider{paths: map[string][]string{"arr": {"/mnt/media/Show/S01/E01.mkv"}}, nextMarker: "m1"}
	sup := &recordingSuppressor{}
	svc := newService(store, prov, failingQueuer{}, sup)
	if err := svc.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(sup.claimed) != 1 || len(sup.released) != 1 || sup.released[0] != sup.claimed[0] {
		t.Fatalf("expected claimed folder to be released, claimed=%v released=%v", sup.claimed, sup.released)
	}
	if _, ok := store.advanced["s1"]; ok {
		t.Fatalf("marker must NOT advance when enqueue fails")
	}
}

func TestPollOnceSkipsEnabledSourceWithoutConnection(t *testing.T) {
	store := &fakeStore{
		settings: Settings{Enabled: true, DefaultPollIntervalSeconds: 600, DebounceSeconds: 60},
		sources: []Source{{
			ID: "s1", InstallationID: 1, CapabilityID: "arr", ConnectionID: nil, Enabled: true,
		}},
	}
	prov := &fakeProvider{paths: map[string][]string{"arr": {"/mnt/media/Show/S01/E01.mkv"}}, nextMarker: "m1"}
	q := &recordingQueuer{}
	svc := newService(store, prov, q, allowSuppressor{})
	if err := svc.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(q.enqueued) != 0 {
		t.Fatalf("a connection-less source must not poll, got %d enqueued", len(q.enqueued))
	}
	if msg, ok := store.recorded["s1"]; !ok || msg != "no connection bound" {
		t.Fatalf("expected 'no connection bound' recorded for s1, got %q ok=%v", msg, ok)
	}
	if _, ok := store.advanced["s1"]; ok {
		t.Fatalf("marker must NOT advance for a connection-less source")
	}
}
