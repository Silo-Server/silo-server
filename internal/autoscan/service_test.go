package autoscan

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scantrigger"
)

type fakeStore struct {
	settings       Settings
	sources        []Source
	connection     Connection
	advanced       map[string]string // source ID -> marker
	recorded       map[string]string // source ID -> error message
	createdEvents  []EventCreate
	events         []EventFinish
	createEventErr error
}

func (f *fakeStore) GetSettings(context.Context) (Settings, error) { return f.settings, nil }
func (f *fakeStore) ListEnabledSources(context.Context) ([]Source, error) {
	return f.sources, nil
}
func (f *fakeStore) GetSource(_ context.Context, id string) (Source, error) {
	for _, s := range f.sources {
		if s.ID == id {
			return s, nil
		}
	}
	return Source{}, ErrNotFound
}
func (f *fakeStore) GetConnection(context.Context, string) (Connection, error) {
	return f.connection, nil
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
func (f *fakeStore) CreateEvent(_ context.Context, event EventCreate) (int64, error) {
	if f.createEventErr != nil {
		return 0, f.createEventErr
	}
	f.createdEvents = append(f.createdEvents, event)
	return int64(len(f.createdEvents)), nil
}
func (f *fakeStore) FinishEvent(_ context.Context, event EventFinish) error {
	f.events = append(f.events, event)
	return nil
}

// fakeProvider implements ScanSourceProvider, keyed by capability ID.
type fakeProvider struct {
	paths      map[string][]string // key: capabilityID, legacy source_paths behavior
	changes    map[string][]Change // key: capabilityID, structured changes
	nextMarker string
	err        error
	errByCap   map[string]error
	lastConfig map[string]string
	calls      int
}

func (f *fakeProvider) PollChanges(_ context.Context, _ string, capabilityID, _ string, _ ResolvedConnection, sourceConfig map[string]string) ([]Change, string, error) {
	f.calls++
	f.lastConfig = sourceConfig
	if f.err != nil {
		return nil, "", f.err
	}
	if err := f.errByCap[capabilityID]; err != nil {
		return nil, "", err
	}
	if changes, ok := f.changes[capabilityID]; ok {
		return changes, f.nextMarker, nil
	}
	changes := make([]Change, 0, len(f.paths[capabilityID]))
	for _, path := range f.paths[capabilityID] {
		changes = append(changes, Change{SourcePath: path, Scope: ChangeScopeAuto})
	}
	return changes, f.nextMarker, nil
}

func TestPollOncePassesSourceConfigToProvider(t *testing.T) {
	store := &fakeStore{
		settings: Settings{Enabled: true, DefaultPollIntervalSeconds: 600, DebounceSeconds: 60},
		sources: []Source{{
			ID: "s1", PluginID: "silo.autoscan.cephfs", CapabilityID: "cephfs", Enabled: true,
			SourceConfig: map[string]string{"exclusions": ".downloads\n.recyclebin"},
		}},
	}
	prov := &fakeProvider{nextMarker: "m1"}
	svc := newService(store, prov, &recordingQueuer{}, allowSuppressor{})
	if err := svc.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if got := prov.lastConfig["exclusions"]; got != ".downloads\n.recyclebin" {
		t.Fatalf("source config = %#v", prov.lastConfig)
	}
}

func TestPollOnceSkipsSourceWhenPollAlreadyRunning(t *testing.T) {
	store := &fakeStore{
		settings:       Settings{Enabled: true, DefaultPollIntervalSeconds: 600, DebounceSeconds: 60},
		createEventErr: ErrPollAlreadyRunning,
		sources: []Source{{
			ID: "s1", PluginID: "silo.autoscan.cephfs", CapabilityID: "cephfs", Enabled: true,
		}},
	}
	prov := &fakeProvider{paths: map[string][]string{"cephfs": {"/mnt/media/Movie/movie.mkv"}}, nextMarker: "m1"}
	q := &recordingQueuer{}
	svc := newService(store, prov, q, allowSuppressor{})

	if err := svc.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if prov.calls != 0 {
		t.Fatalf("provider calls = %d, want 0", prov.calls)
	}
	if len(q.enqueued) != 0 {
		t.Fatalf("enqueued scans = %+v, want none", q.enqueued)
	}
	if len(store.events) != 0 {
		t.Fatalf("finished events = %+v, want none", store.events)
	}
	if len(store.recorded) != 0 {
		t.Fatalf("recorded source errors = %+v, want none", store.recorded)
	}
	if len(store.advanced) != 0 {
		t.Fatalf("advanced markers = %+v, want none", store.advanced)
	}
}

// passthroughConnRes resolves to empty credentials; the engine doesn't inspect
// them in tests (the fake provider ignores conn).
type passthroughConnRes struct{}

func (passthroughConnRes) Resolve(context.Context, Connection) (ResolvedConnection, error) {
	return ResolvedConnection{}, nil
}

type fakeResolver struct{}

func (fakeResolver) Resolve(_ context.Context, req scantrigger.Request) (*scantrigger.Target, error) {
	if strings.Contains(req.Path, "vanished") {
		// Mimic the real resolver's stat failure for deleted paths.
		return nil, &scantrigger.RequestError{Status: 400, Code: "bad_request", Message: "Path does not exist"}
	}
	if strings.HasPrefix(req.Path, "/mnt/media/") {
		mode := scantrigger.ModeSubtree
		if filepath.Ext(req.Path) == ".mkv" {
			mode = scantrigger.ModeFile
		}
		return &scantrigger.Target{Folder: &models.MediaFolder{ID: 7}, Mode: mode, Path: req.Path, Trigger: req.Trigger}, nil
	}
	return nil, nil
}

func (fakeResolver) ResolveMissingSubtree(_ context.Context, subtreePath, trigger string) (*scantrigger.Target, error) {
	if strings.HasPrefix(subtreePath, "/mnt/media/") {
		return &scantrigger.Target{Folder: &models.MediaFolder{ID: 7}, Mode: scantrigger.ModeSubtree, Path: subtreePath, Trigger: trigger}, nil
	}
	return nil, &scantrigger.RequestError{Status: 400, Code: "bad_request", Message: "outside media folders"}
}

func (fakeResolver) ResolveVanishedPath(_ context.Context, path, trigger string) (*scantrigger.Target, error) {
	if !strings.HasPrefix(path, "/mnt/media/") {
		return nil, &scantrigger.RequestError{Status: 400, Code: "bad_request", Message: "outside media folders"}
	}
	// Mimic the real resolver: vanished video files reconcile via their parent
	// directory; other vanished paths reconcile as themselves.
	scope := path
	if filepath.Ext(path) == ".mkv" {
		scope = filepath.Dir(path)
	}
	return &scantrigger.Target{Folder: &models.MediaFolder{ID: 7}, Mode: scantrigger.ModeSubtree, Path: scope, Trigger: trigger}, nil
}

type distinctFolderResolver struct{}

func (distinctFolderResolver) Resolve(_ context.Context, req scantrigger.Request) (*scantrigger.Target, error) {
	if !strings.HasPrefix(req.Path, "/mnt/media/library-") {
		return nil, &scantrigger.RequestError{Status: 400, Code: "bad_request", Message: "outside media folders"}
	}
	rest := strings.TrimPrefix(req.Path, "/mnt/media/library-")
	idPart := rest
	if slash := strings.IndexByte(rest, '/'); slash >= 0 {
		idPart = rest[:slash]
	}
	id, err := strconv.Atoi(idPart)
	if err != nil {
		return nil, &scantrigger.RequestError{Status: 400, Code: "bad_request", Message: "outside media folders"}
	}
	return &scantrigger.Target{
		Folder:  &models.MediaFolder{ID: id},
		Mode:    scantrigger.ModeFile,
		Path:    req.Path,
		Trigger: req.Trigger,
	}, nil
}

func (distinctFolderResolver) ResolveMissingSubtree(_ context.Context, subtreePath, trigger string) (*scantrigger.Target, error) {
	return &scantrigger.Target{
		Folder:  &models.MediaFolder{ID: 1},
		Mode:    scantrigger.ModeSubtree,
		Path:    subtreePath,
		Trigger: trigger,
	}, nil
}

func (distinctFolderResolver) ResolveVanishedPath(_ context.Context, path, trigger string) (*scantrigger.Target, error) {
	return nil, &scantrigger.RequestError{Status: 400, Code: "bad_request", Message: "outside media folders"}
}

// unresolvableResolver treats every path as outside Silo's media folders,
// returning a RequestError — the "none resolved → misconfiguration" signal.
type unresolvableResolver struct{}

func (unresolvableResolver) Resolve(_ context.Context, req scantrigger.Request) (*scantrigger.Target, error) {
	return nil, &scantrigger.RequestError{Status: 400, Code: "bad_request", Message: "outside media folders"}
}
func (unresolvableResolver) ResolveMissingSubtree(context.Context, string, string) (*scantrigger.Target, error) {
	return nil, &scantrigger.RequestError{Status: 400, Code: "bad_request", Message: "outside media folders"}
}
func (unresolvableResolver) ResolveVanishedPath(context.Context, string, string) (*scantrigger.Target, error) {
	return nil, &scantrigger.RequestError{Status: 400, Code: "bad_request", Message: "outside media folders"}
}

// mixedTransientResolver resolves /mnt/media/ paths normally and fails every
// other path with a plain (non-RequestError) internal error — the mixed
// "some resolved, some failed transiently" poll window.
type mixedTransientResolver struct{}

func (mixedTransientResolver) Resolve(_ context.Context, req scantrigger.Request) (*scantrigger.Target, error) {
	if strings.HasPrefix(req.Path, "/mnt/media/") {
		return &scantrigger.Target{Folder: &models.MediaFolder{ID: 7}, Mode: scantrigger.ModeSubtree, Path: req.Path, Trigger: req.Trigger}, nil
	}
	return nil, errors.New("listing folders: connection timeout")
}
func (mixedTransientResolver) ResolveMissingSubtree(context.Context, string, string) (*scantrigger.Target, error) {
	return nil, errors.New("listing folders: connection timeout")
}
func (mixedTransientResolver) ResolveVanishedPath(context.Context, string, string) (*scantrigger.Target, error) {
	return nil, errors.New("listing folders: connection timeout")
}

// transientFailureResolver fails every resolve with a plain (non-RequestError)
// error, simulating an internal fault such as a database timeout.
type transientFailureResolver struct{}

func (transientFailureResolver) Resolve(context.Context, scantrigger.Request) (*scantrigger.Target, error) {
	return nil, errors.New("listing folders: connection timeout")
}
func (transientFailureResolver) ResolveMissingSubtree(context.Context, string, string) (*scantrigger.Target, error) {
	return nil, errors.New("listing folders: connection timeout")
}
func (transientFailureResolver) ResolveVanishedPath(context.Context, string, string) (*scantrigger.Target, error) {
	return nil, errors.New("listing folders: connection timeout")
}

// denySuppressor resolves paths normally but denies every claim, simulating a
// recently-scanned / debounced target (resolved but suppressed).
type denySuppressor struct{}

func (denySuppressor) ShouldScan(context.Context, string, time.Duration) (bool, error) {
	return false, nil
}
func (denySuppressor) Release(context.Context, string) error { return nil }

type recordingQueuer struct {
	enqueued       []scantrigger.Target
	batches        [][]scantrigger.Target
	autoscanEvents []int64
	createdCount   *int
	reusedCount    int
}

func (q *recordingQueuer) EnqueueScans(_ context.Context, targets []scantrigger.Target) error {
	q.batches = append(q.batches, append([]scantrigger.Target(nil), targets...))
	q.enqueued = append(q.enqueued, targets...)
	return nil
}
func (q *recordingQueuer) EnqueueAutoscanScans(_ context.Context, targets []scantrigger.Target, eventID int64) (int, int, error) {
	q.batches = append(q.batches, append([]scantrigger.Target(nil), targets...))
	q.enqueued = append(q.enqueued, targets...)
	q.autoscanEvents = append(q.autoscanEvents, eventID)
	if q.createdCount != nil {
		return *q.createdCount, q.reusedCount, nil
	}
	return len(targets), q.reusedCount, nil
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
func (failingQueuer) EnqueueAutoscanScans(context.Context, []scantrigger.Target, int64) (int, int, error) {
	return 0, 0, context.DeadlineExceeded
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
			ID: "s1", PluginID: "silo.autoscan.arr", CapabilityID: "arr", ConnectionID: strptr("c1"), Enabled: true,
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

func TestPollOnceRecordsSuccessfulEvent(t *testing.T) {
	marker := "m0"
	store := &fakeStore{
		settings: Settings{Enabled: true, DefaultPollIntervalSeconds: 600, DebounceSeconds: 60},
		sources: []Source{{
			ID: "s1", PluginID: "silo.autoscan.arr", CapabilityID: "arr", ConnectionID: strptr("c1"), Enabled: true, Marker: &marker,
		}},
	}
	prov := &fakeProvider{paths: map[string][]string{
		"arr": {"/mnt/media/Show/S01/E01.mkv"},
	}, nextMarker: "m1"}
	q := &recordingQueuer{}
	svc := newService(store, prov, q, allowSuppressor{})
	if err := svc.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(store.createdEvents) != 1 {
		t.Fatalf("expected one event created, got %d", len(store.createdEvents))
	}
	if got := store.createdEvents[0].MarkerBefore; got != marker {
		t.Fatalf("marker_before = %q, want %q", got, marker)
	}
	if len(q.autoscanEvents) != 1 || q.autoscanEvents[0] != 1 {
		t.Fatalf("autoscan enqueue event ids = %v, want [1]", q.autoscanEvents)
	}
	if len(store.events) != 1 {
		t.Fatalf("expected one finished event, got %d", len(store.events))
	}
	event := store.events[0]
	if event.ID != 1 || event.Status != EventStatusSuccess {
		t.Fatalf("event identity/status = %+v", event)
	}
	if event.ChangesReturned != 1 || event.ChangesResolved != 1 || event.TargetsClaimed != 1 {
		t.Fatalf("event change counts = %+v", event)
	}
	if event.ScansCreated != 1 || event.ScansReused != 0 || event.ScansSuppressed != 0 {
		t.Fatalf("event scan counts = %+v", event)
	}
	if event.MarkerAfter != "m1" || event.ErrorMessage != "" {
		t.Fatalf("event completion fields = %+v", event)
	}
}

func TestPollOnceRecordsReusedScanCounts(t *testing.T) {
	store := &fakeStore{
		settings: Settings{Enabled: true, DefaultPollIntervalSeconds: 600, DebounceSeconds: 60},
		sources: []Source{{
			ID: "s1", PluginID: "silo.autoscan.arr", CapabilityID: "arr", ConnectionID: strptr("c1"), Enabled: true,
		}},
	}
	prov := &fakeProvider{paths: map[string][]string{
		"arr": {
			"/mnt/media/ShowA/S01/E01.mkv",
			"/mnt/media/ShowB/S01/E01.mkv",
		},
	}, nextMarker: "m1"}
	created := 1
	q := &recordingQueuer{createdCount: &created, reusedCount: 1}
	svc := newService(store, prov, q, allowSuppressor{})
	if err := svc.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(store.events) != 1 {
		t.Fatalf("expected one finished event, got %d", len(store.events))
	}
	event := store.events[0]
	if event.ScansCreated != 1 || event.ScansReused != 1 {
		t.Fatalf("event scan counts = %+v", event)
	}
	if event.TargetsClaimed != 2 {
		t.Fatalf("targets_claimed = %d, want 2", event.TargetsClaimed)
	}
}

func TestPollOnceAppliesSourceRewritesBeforeEnqueue(t *testing.T) {
	// The provider returns RAW source-namespace paths (/data/tv/...). The source's
	// rewrite /data/tv -> /mnt/media/tv must be applied host-side so the resolved
	// target uses the rewritten, Silo-native path. fakeResolver only resolves
	// paths under /mnt/media/, so a missing rewrite would resolve to nothing.
	store := &fakeStore{
		settings: Settings{Enabled: true, DefaultPollIntervalSeconds: 600, DebounceSeconds: 60},
		sources: []Source{{
			ID: "s1", PluginID: "silo.autoscan.arr", CapabilityID: "arr", ConnectionID: strptr("c1"), Enabled: true,
			PathRewrites: []PathRewrite{{From: "/data/tv", To: "/mnt/media/tv"}},
		}},
	}
	prov := &fakeProvider{paths: map[string][]string{
		"arr": {"/data/tv/Show/S01/E01.mkv"},
	}, nextMarker: "m1"}
	q := &recordingQueuer{}
	svc := newService(store, prov, q, allowSuppressor{})
	if err := svc.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(q.enqueued) != 1 {
		t.Fatalf("expected 1 enqueued target, got %d: %+v", len(q.enqueued), q.enqueued)
	}
	// uniqueParentDirs collapses E01.mkv to its parent dir; the rewritten target
	// must be the Silo-native /mnt/media/tv/Show/S01.
	if got := q.enqueued[0].Path; got != "/mnt/media/tv/Show/S01" {
		t.Fatalf("expected rewritten target path /mnt/media/tv/Show/S01, got %q", got)
	}
	if _, ok := store.advanced["s1"]; !ok {
		t.Fatalf("expected marker advanced for s1")
	}
}

func TestPollOnceStructuredFileChangeEnqueuesExactFile(t *testing.T) {
	store := &fakeStore{
		settings: Settings{Enabled: true, DefaultPollIntervalSeconds: 600, DebounceSeconds: 60},
		sources: []Source{{
			ID: "s1", PluginID: "silo.autoscan.cephfs", CapabilityID: "cephfs", Enabled: true,
			PathRewrites: []PathRewrite{{From: "/ceph/tv", To: "/mnt/media/tv"}},
		}},
	}
	prov := &fakeProvider{changes: map[string][]Change{
		"cephfs": {{
			SourcePath: "/ceph/tv/Show/S01/E01.mkv",
			Scope:      ChangeScopeFile,
		}},
	}, nextMarker: "m1"}
	q := &recordingQueuer{}
	svc := newService(store, prov, q, allowSuppressor{})
	if err := svc.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(q.enqueued) != 1 {
		t.Fatalf("expected 1 file scan, got %d: %+v", len(q.enqueued), q.enqueued)
	}
	if got := q.enqueued[0].Mode; got != scantrigger.ModeFile {
		t.Fatalf("mode = %q, want %q", got, scantrigger.ModeFile)
	}
	if got := q.enqueued[0].Path; got != "/mnt/media/tv/Show/S01/E01.mkv" {
		t.Fatalf("file path = %q", got)
	}
	if _, ok := store.advanced["s1"]; !ok {
		t.Fatalf("expected marker advanced for s1")
	}
}

func TestPollOnceStructuredSubtreeChangeEnqueuesExactSubtree(t *testing.T) {
	store := &fakeStore{
		settings: Settings{Enabled: true, DefaultPollIntervalSeconds: 600, DebounceSeconds: 60},
		sources: []Source{{
			ID: "s1", PluginID: "silo.autoscan.cephfs", CapabilityID: "cephfs", Enabled: true,
			PathRewrites: []PathRewrite{{From: "/ceph/movies", To: "/mnt/media/movies"}},
		}},
	}
	prov := &fakeProvider{changes: map[string][]Change{
		"cephfs": {{
			SourcePath: "/ceph/movies/Movie (2026)",
			Scope:      ChangeScopeSubtree,
		}},
	}, nextMarker: "m1"}
	q := &recordingQueuer{}
	svc := newService(store, prov, q, allowSuppressor{})
	if err := svc.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(q.enqueued) != 1 {
		t.Fatalf("expected 1 subtree scan, got %d: %+v", len(q.enqueued), q.enqueued)
	}
	if got := q.enqueued[0].Mode; got != scantrigger.ModeSubtree {
		t.Fatalf("mode = %q, want %q", got, scantrigger.ModeSubtree)
	}
	if got := q.enqueued[0].Path; got != "/mnt/media/movies/Movie (2026)" {
		t.Fatalf("subtree path = %q", got)
	}
	if _, ok := store.advanced["s1"]; !ok {
		t.Fatalf("expected marker advanced for s1")
	}
}

func TestPollOnceFileChangeForDeletedPathFallsBackToSubtreeScan(t *testing.T) {
	store := &fakeStore{
		settings: Settings{Enabled: true, DefaultPollIntervalSeconds: 600, DebounceSeconds: 60},
		sources: []Source{{
			ID: "s1", PluginID: "silo.autoscan.cephfs", CapabilityID: "cephfs", Enabled: true,
			PathRewrites: []PathRewrite{{From: "/ceph/movies", To: "/mnt/media/movies"}},
		}},
	}
	prov := &fakeProvider{changes: map[string][]Change{
		"cephfs": {{
			SourcePath: "/ceph/movies/Movie-vanished (2026)/Movie-vanished (2026).mkv",
			Scope:      ChangeScopeFile,
		}},
	}, nextMarker: "m1"}
	q := &recordingQueuer{}
	svc := newService(store, prov, q, allowSuppressor{})
	if err := svc.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(q.enqueued) != 1 {
		t.Fatalf("expected 1 fallback subtree scan, got %d: %+v", len(q.enqueued), q.enqueued)
	}
	if got := q.enqueued[0].Mode; got != scantrigger.ModeSubtree {
		t.Fatalf("mode = %q, want %q", got, scantrigger.ModeSubtree)
	}
	if got := q.enqueued[0].Path; got != "/mnt/media/movies/Movie-vanished (2026)" {
		t.Fatalf("subtree path = %q", got)
	}
	if _, ok := store.advanced["s1"]; !ok {
		t.Fatalf("expected marker advanced for s1")
	}
}

func TestPollOnceLegacyChangeForDeletedDirFallsBackToSubtreeScan(t *testing.T) {
	store := &fakeStore{
		settings: Settings{Enabled: true, DefaultPollIntervalSeconds: 600, DebounceSeconds: 60},
		sources: []Source{{
			ID: "s1", PluginID: "silo.autoscan.cephfs", CapabilityID: "cephfs", Enabled: true,
			PathRewrites: []PathRewrite{{From: "/ceph/movies", To: "/mnt/media/movies"}},
		}},
	}
	// Legacy/auto changes collapse to the parent dir before resolving; a
	// removed movie folder makes that dir vanish too.
	prov := &fakeProvider{changes: map[string][]Change{
		"cephfs": {{
			SourcePath: "/ceph/movies/Movie-vanished (2026)/Movie-vanished (2026).mkv",
			Scope:      ChangeScopeAuto,
		}},
	}, nextMarker: "m1"}
	q := &recordingQueuer{}
	svc := newService(store, prov, q, allowSuppressor{})
	if err := svc.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(q.enqueued) != 1 {
		t.Fatalf("expected 1 fallback subtree scan, got %d: %+v", len(q.enqueued), q.enqueued)
	}
	if got := q.enqueued[0].Mode; got != scantrigger.ModeSubtree {
		t.Fatalf("mode = %q, want %q", got, scantrigger.ModeSubtree)
	}
	if got := q.enqueued[0].Path; got != "/mnt/media/movies/Movie-vanished (2026)" {
		t.Fatalf("subtree path = %q", got)
	}
}

func TestPollOnceCollapsesLargeTargetBatchToLibraryScans(t *testing.T) {
	store := &fakeStore{
		settings: Settings{Enabled: true, DefaultPollIntervalSeconds: 600, DebounceSeconds: 60},
		sources: []Source{{
			ID: "s1", PluginID: "silo.autoscan.cephfs", CapabilityID: "cephfs", Enabled: true,
		}},
	}
	changes := make([]Change, 0, maxAutoscanTargetsPerPoll+1)
	for i := 0; i <= maxAutoscanTargetsPerPoll; i++ {
		changes = append(changes, Change{
			SourcePath: "/mnt/media/Show/S01/Episode" + strconv.Itoa(i) + ".mkv",
			Scope:      ChangeScopeFile,
		})
	}
	prov := &fakeProvider{changes: map[string][]Change{"cephfs": changes}, nextMarker: "m1"}
	q := &recordingQueuer{}
	svc := newService(store, prov, q, allowSuppressor{})

	if err := svc.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(q.enqueued) != 1 {
		t.Fatalf("expected one collapsed library scan, got %d: %+v", len(q.enqueued), q.enqueued)
	}
	target := q.enqueued[0]
	if target.Folder == nil || target.Folder.ID != 7 || target.Mode != scantrigger.ModeLibrary || target.Path != "" {
		t.Fatalf("unexpected collapsed target: %+v", target)
	}
	if got, ok := store.advanced["s1"]; !ok || got != "m1" {
		t.Fatalf("marker must advance after collapsed enqueue, got %q ok=%v", got, ok)
	}
	if len(store.events) != 1 {
		t.Fatalf("expected one finished event, got %d", len(store.events))
	}
	event := store.events[0]
	if event.ChangesReturned != maxAutoscanTargetsPerPoll+1 || event.TargetsClaimed != maxAutoscanTargetsPerPoll+1 {
		t.Fatalf("event should preserve original burst counts, got %+v", event)
	}
	if event.ScansCreated != 1 || event.ScansReused != 0 {
		t.Fatalf("event should record collapsed scan counts, got %+v", event)
	}
}

func TestPollOnceChunksCollapsedLibraryScansAtTargetCap(t *testing.T) {
	store := &fakeStore{
		settings: Settings{Enabled: true, DefaultPollIntervalSeconds: 600, DebounceSeconds: 60},
		sources: []Source{{
			ID: "s1", PluginID: "silo.autoscan.cephfs", CapabilityID: "cephfs", Enabled: true,
		}},
	}
	changes := make([]Change, 0, maxAutoscanTargetsPerPoll+1)
	for i := 0; i <= maxAutoscanTargetsPerPoll; i++ {
		changes = append(changes, Change{
			SourcePath: "/mnt/media/library-" + strconv.Itoa(i+1) + "/Episode.mkv",
			Scope:      ChangeScopeFile,
		})
	}
	prov := &fakeProvider{changes: map[string][]Change{"cephfs": changes}, nextMarker: "m1"}
	q := &recordingQueuer{}
	svc := NewService(store, prov, passthroughConnRes{}, distinctFolderResolver{}, q, allowSuppressor{}, nil)

	if err := svc.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(q.enqueued) != maxAutoscanTargetsPerPoll+1 {
		t.Fatalf("enqueued scans = %d, want %d", len(q.enqueued), maxAutoscanTargetsPerPoll+1)
	}
	if len(q.batches) != 2 {
		t.Fatalf("queue batches = %d, want 2", len(q.batches))
	}
	if len(q.batches[0]) != maxAutoscanTargetsPerPoll {
		t.Fatalf("first batch size = %d, want %d", len(q.batches[0]), maxAutoscanTargetsPerPoll)
	}
	if len(q.batches[1]) != 1 {
		t.Fatalf("second batch size = %d, want 1", len(q.batches[1]))
	}
	if got, ok := store.advanced["s1"]; !ok || got != "m1" {
		t.Fatalf("marker must advance after all chunks enqueue, got %q ok=%v", got, ok)
	}
}

func TestPollOnceScansDistinctPathsUnderSameFolder(t *testing.T) {
	// Two imported subtrees resolve to the SAME folder ID (7) but DIFFERENT
	// target paths. Keying suppression on folder ID alone would drop the second;
	// keying on (folder, path) must scan both.
	store := &fakeStore{
		settings: Settings{Enabled: true, DefaultPollIntervalSeconds: 600, DebounceSeconds: 60},
		sources: []Source{{
			ID: "s1", PluginID: "silo.autoscan.arr", CapabilityID: "arr", ConnectionID: strptr("c1"), Enabled: true,
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
			ID: "s1", PluginID: "silo.autoscan.arr", CapabilityID: "arr", ConnectionID: strptr("c1"), Enabled: true,
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

func TestPollOnceAdvancesMarkerWhenPathsReturnedButNoneResolve(t *testing.T) {
	// A filesystem watcher (e.g. CephFS) may return paths from folders that are
	// not registered as Silo libraries. The marker must ADVANCE so autoscan does
	// not stall permanently; no source error is recorded, but the event finishes
	// as "unresolved" so the condition stays visible in poll history.
	store := &fakeStore{
		settings: Settings{Enabled: true, DefaultPollIntervalSeconds: 600, DebounceSeconds: 60},
		sources: []Source{{
			ID: "s1", PluginID: "silo.autoscan.arr", CapabilityID: "arr", ConnectionID: strptr("c1"), Enabled: true,
		}},
	}
	prov := &fakeProvider{paths: map[string][]string{
		"arr": {
			"/data/tv/Show/S01/E01.mkv",
			"/data/tv/Show/S01/E02.mkv",
		},
	}, nextMarker: "m1"}
	q := &recordingQueuer{}
	// unresolvableResolver rejects every path; allowSuppressor would happily claim
	// anything, so the only reason nothing resolves is the resolver.
	svc := NewService(store, prov, passthroughConnRes{}, unresolvableResolver{}, q, allowSuppressor{}, nil)
	if err := svc.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(q.enqueued) != 0 {
		t.Fatalf("nothing should enqueue when no path resolves, got %d", len(q.enqueued))
	}
	if got := store.advanced["s1"]; got != "m1" {
		t.Fatalf("marker must advance when paths returned but none resolved, got %q", got)
	}
	if msg, ok := store.recorded["s1"]; ok {
		t.Fatalf("no error should be recorded when advancing past unresolvable paths, got %q", msg)
	}
	if len(store.events) != 1 {
		t.Fatalf("expected one finished event, got %d", len(store.events))
	}
	event := store.events[0]
	if event.Status != EventStatusUnresolved {
		t.Fatalf("event status = %q, want %q", event.Status, EventStatusUnresolved)
	}
	if event.ChangesReturned != 2 || event.ChangesResolved != 0 || event.TargetsClaimed != 0 {
		t.Fatalf("event counts = %+v", event)
	}
	if !strings.Contains(event.ErrorMessage, "none matched a Silo library folder") {
		t.Fatalf("event message = %q", event.ErrorMessage)
	}
	if event.MarkerAfter != "m1" {
		t.Fatalf("event marker after = %q, want %q", event.MarkerAfter, "m1")
	}
}

func TestPollOnceHoldsMarkerOnTransientResolveFailure(t *testing.T) {
	// Nothing resolved because the resolver failed INTERNALLY (e.g. a database
	// fault), not because the paths are outside Silo's libraries. Advancing
	// would silently skip real imports, so the marker must HOLD and an error be
	// recorded; the window is retried once the fault clears.
	store := &fakeStore{
		settings: Settings{Enabled: true, DefaultPollIntervalSeconds: 600, DebounceSeconds: 60},
		sources: []Source{{
			ID: "s1", PluginID: "silo.autoscan.arr", CapabilityID: "arr", ConnectionID: strptr("c1"), Enabled: true,
		}},
	}
	prov := &fakeProvider{paths: map[string][]string{
		"arr": {
			"/mnt/media/Show/S01/E01.mkv",
			"/mnt/media/Show/S01/E02.mkv",
		},
	}, nextMarker: "m1"}
	q := &recordingQueuer{}
	svc := NewService(store, prov, passthroughConnRes{}, transientFailureResolver{}, q, allowSuppressor{}, nil)
	if err := svc.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(q.enqueued) != 0 {
		t.Fatalf("nothing should enqueue when no path resolves, got %d", len(q.enqueued))
	}
	if got, ok := store.advanced["s1"]; ok {
		t.Fatalf("marker must NOT advance on transient resolve failure, advanced to %q", got)
	}
	msg, ok := store.recorded["s1"]
	if !ok || !strings.Contains(msg, "failed internally") {
		t.Fatalf("expected recorded transient-failure error, got %q ok=%v", msg, ok)
	}
	if len(store.events) != 1 {
		t.Fatalf("expected one finished event, got %d", len(store.events))
	}
	event := store.events[0]
	if event.Status != EventStatusError {
		t.Fatalf("event status = %q, want %q", event.Status, EventStatusError)
	}
	if event.ChangesReturned != 2 || event.ChangesResolved != 0 {
		t.Fatalf("event counts = %+v", event)
	}
}

func TestPollOnceHoldsMarkerWhenSomeResolveAndOthersFailTransiently(t *testing.T) {
	// One path resolves and enqueues, another fails with an internal
	// (non-RequestError) resolver fault. The resolved target's scan must still
	// be enqueued, but the marker must HOLD and an error be recorded so the
	// failed path is retried next poll instead of being skipped.
	store := &fakeStore{
		settings: Settings{Enabled: true, DefaultPollIntervalSeconds: 600, DebounceSeconds: 60},
		sources: []Source{{
			ID: "s1", PluginID: "silo.autoscan.arr", CapabilityID: "arr", ConnectionID: strptr("c1"), Enabled: true,
		}},
	}
	prov := &fakeProvider{paths: map[string][]string{
		"arr": {
			"/mnt/media/Show/S01/E01.mkv",
			"/data/other/Movie/movie.mkv",
		},
	}, nextMarker: "m1"}
	q := &recordingQueuer{}
	svc := NewService(store, prov, passthroughConnRes{}, mixedTransientResolver{}, q, allowSuppressor{}, nil)
	if err := svc.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(q.enqueued) != 1 {
		t.Fatalf("the resolved target should still enqueue, got %d", len(q.enqueued))
	}
	if got, ok := store.advanced["s1"]; ok {
		t.Fatalf("marker must NOT advance when any resolve attempt failed transiently, advanced to %q", got)
	}
	msg, ok := store.recorded["s1"]
	if !ok || !strings.Contains(msg, "failed internally") {
		t.Fatalf("expected recorded transient-failure error, got %q ok=%v", msg, ok)
	}
	if len(store.events) != 1 {
		t.Fatalf("expected one finished event, got %d", len(store.events))
	}
	event := store.events[0]
	if event.Status != EventStatusError {
		t.Fatalf("event status = %q, want %q", event.Status, EventStatusError)
	}
	if event.ChangesReturned != 2 || event.ChangesResolved != 1 {
		t.Fatalf("event counts = %+v", event)
	}
}

func TestPollOnceAdvancesMarkerWhenResolvedButAllSuppressed(t *testing.T) {
	// Paths DO resolve to a Silo library folder, but every claim is denied by the
	// suppressor (recently scanned / debounced). This is NOT a misconfiguration:
	// the work is effectively done, so the marker must ADVANCE and no error is
	// recorded. Regression guard for treating "resolved-but-suppressed" as
	// "nothing resolved."
	store := &fakeStore{
		settings: Settings{Enabled: true, DefaultPollIntervalSeconds: 600, DebounceSeconds: 60},
		sources: []Source{{
			ID: "s1", PluginID: "silo.autoscan.arr", CapabilityID: "arr", ConnectionID: strptr("c1"), Enabled: true,
			// rewrite /data/tv -> /mnt/media/tv so fakeResolver resolves the paths.
			PathRewrites: []PathRewrite{{From: "/data/tv", To: "/mnt/media/tv"}},
		}},
	}
	prov := &fakeProvider{paths: map[string][]string{
		"arr": {
			"/data/tv/Show/S01/E01.mkv",
			"/data/tv/Show/S01/E02.mkv",
		},
	}, nextMarker: "m1"}
	q := &recordingQueuer{}
	svc := newService(store, prov, q, denySuppressor{})
	if err := svc.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(q.enqueued) != 0 {
		t.Fatalf("nothing should enqueue when every target is suppressed, got %d", len(q.enqueued))
	}
	if got, ok := store.advanced["s1"]; !ok || got != "m1" {
		t.Fatalf("marker must advance to %q when paths resolve but are all suppressed, got %q ok=%v", "m1", got, ok)
	}
	if _, ok := store.recorded["s1"]; ok {
		t.Fatalf("no error should be recorded when targets merely suppressed (work is debounced, not misconfigured)")
	}
}

func TestPollOnceAdvancesMarkerWhenZeroPathsReturned(t *testing.T) {
	// The "nothing to do" case: provider returns no paths at all. The marker must
	// advance normally and no error is recorded.
	store := &fakeStore{
		settings: Settings{Enabled: true, DefaultPollIntervalSeconds: 600, DebounceSeconds: 60},
		sources: []Source{{
			ID: "s1", PluginID: "silo.autoscan.arr", CapabilityID: "arr", ConnectionID: strptr("c1"), Enabled: true,
		}},
	}
	prov := &fakeProvider{paths: map[string][]string{"arr": {}}, nextMarker: "m1"}
	q := &recordingQueuer{}
	svc := newService(store, prov, q, allowSuppressor{})
	if err := svc.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if got, ok := store.advanced["s1"]; !ok || got != "m1" {
		t.Fatalf("marker must advance to %q when zero paths returned, got %q ok=%v", "m1", got, ok)
	}
	if _, ok := store.recorded["s1"]; ok {
		t.Fatalf("no error should be recorded when there's simply nothing to do")
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
		sources:  []Source{{ID: "s1", PluginID: "silo.autoscan.arr", CapabilityID: "arr", ConnectionID: strptr("c1"), Enabled: true}},
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
		sources:  []Source{{ID: "s1", PluginID: "silo.autoscan.arr", CapabilityID: "arr", ConnectionID: strptr("c1"), Enabled: true}},
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
	if len(store.events) != 1 {
		t.Fatalf("expected one finished event, got %d", len(store.events))
	}
	event := store.events[0]
	if event.Status != EventStatusError || !strings.Contains(event.ErrorMessage, context.DeadlineExceeded.Error()) {
		t.Fatalf("event should record enqueue error, got %+v", event)
	}
	if event.ChangesReturned != 1 || event.ChangesResolved != 1 || event.TargetsClaimed != 1 {
		t.Fatalf("event counts = %+v", event)
	}
}

func TestPollOncePollsConnectionlessSource(t *testing.T) {
	// A connection is OPTIONAL: a source with no bound connection is still polled
	// (the provider gets an empty connection it may ignore — e.g. a filesystem
	// watcher). Whether the plugin needs credentials is the plugin's concern.
	store := &fakeStore{
		settings: Settings{Enabled: true, DefaultPollIntervalSeconds: 600, DebounceSeconds: 60},
		sources: []Source{{
			ID: "s1", PluginID: "silo.autoscan.arr", CapabilityID: "arr", ConnectionID: nil, Enabled: true,
		}},
	}
	prov := &fakeProvider{paths: map[string][]string{"arr": {"/mnt/media/Show/S01/E01.mkv"}}, nextMarker: "m1"}
	q := &recordingQueuer{}
	svc := newService(store, prov, q, allowSuppressor{})
	if err := svc.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(q.enqueued) != 1 {
		t.Fatalf("a connection-less source must still poll + enqueue, got %d enqueued", len(q.enqueued))
	}
	if _, ok := store.recorded["s1"]; ok {
		t.Fatalf("a connection-less source must not record a 'no connection' error")
	}
	if _, ok := store.advanced["s1"]; !ok {
		t.Fatalf("marker must advance after a successful connection-less poll")
	}
}

func TestPollOnceSkipsSourcePolledTooRecently(t *testing.T) {
	recent := time.Now().Add(-30 * time.Second)
	interval := 600
	store := &fakeStore{
		settings: Settings{Enabled: true, DefaultPollIntervalSeconds: 600, DebounceSeconds: 60},
		sources: []Source{{
			ID: "s1", PluginID: "silo.autoscan.arr", CapabilityID: "arr", ConnectionID: strptr("c1"), Enabled: true,
			PollIntervalSeconds: &interval, LastRunAt: &recent,
		}},
	}
	prov := &fakeProvider{paths: map[string][]string{"arr": {"/mnt/media/Show/S01/E01.mkv"}}, nextMarker: "m1"}
	q := &recordingQueuer{}
	svc := newService(store, prov, q, allowSuppressor{})
	if err := svc.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(q.enqueued) != 0 {
		t.Fatalf("a source within its poll interval must be skipped, got %d enqueued", len(q.enqueued))
	}
	if _, ok := store.advanced["s1"]; ok {
		t.Fatalf("marker must NOT advance for a skipped (too-recent) source")
	}
}

func TestPollOnceRunsSourcePastItsInterval(t *testing.T) {
	old := time.Now().Add(-20 * time.Minute)
	interval := 600 // 10 min; last run was 20 min ago => eligible
	store := &fakeStore{
		settings: Settings{Enabled: true, DefaultPollIntervalSeconds: 600, DebounceSeconds: 60},
		sources: []Source{{
			ID: "s1", PluginID: "silo.autoscan.arr", CapabilityID: "arr", ConnectionID: strptr("c1"), Enabled: true,
			PollIntervalSeconds: &interval, LastRunAt: &old,
		}},
	}
	prov := &fakeProvider{paths: map[string][]string{"arr": {"/mnt/media/Show/S01/E01.mkv"}}, nextMarker: "m1"}
	q := &recordingQueuer{}
	svc := newService(store, prov, q, allowSuppressor{})
	if err := svc.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(q.enqueued) != 1 {
		t.Fatalf("a source past its interval must poll, got %d enqueued", len(q.enqueued))
	}
	if _, ok := store.advanced["s1"]; !ok {
		t.Fatalf("expected marker advanced for s1")
	}
}

func TestPollOnceRecordsScanSourceResolutionFailure(t *testing.T) {
	// Two enabled sources: one polls normally, while the other simulates the
	// production resolver failing because the plugin cannot be resolved.
	store := &fakeStore{
		settings: Settings{Enabled: true, DefaultPollIntervalSeconds: 600, DebounceSeconds: 60},
		sources: []Source{
			{ID: "live", PluginID: "silo.autoscan.live", CapabilityID: "arr-live", ConnectionID: strptr("c1"), Enabled: true},
			{ID: "gone", PluginID: "silo.autoscan.gone", CapabilityID: "arr-gone", ConnectionID: strptr("c1"), Enabled: true},
		},
	}
	prov := &fakeProvider{paths: map[string][]string{
		"arr-live": {"/mnt/media/Show/S01/E01.mkv"},
	}, errByCap: map[string]error{
		"arr-gone": errors.New("scan source plugin \"silo.autoscan.gone\" is not installed"),
	}, nextMarker: "m1"}
	q := &recordingQueuer{}
	svc := newService(store, prov, q, allowSuppressor{})

	if err := svc.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(q.enqueued) != 1 {
		t.Fatalf("only the live source should enqueue, got %d: %+v", len(q.enqueued), q.enqueued)
	}
	if _, ok := store.advanced["live"]; !ok {
		t.Fatalf("expected live source marker advanced")
	}
	if _, ok := store.advanced["gone"]; ok {
		t.Fatalf("failed source must NOT advance")
	}
	if got := store.recorded["gone"]; !strings.Contains(got, "not installed") {
		t.Fatalf("failed source error = %q", got)
	}
	if len(store.events) != 2 {
		t.Fatalf("expected one event per source, got %+v", store.events)
	}
	if store.events[1].Status != EventStatusError || !strings.Contains(store.events[1].ErrorMessage, "not installed") {
		t.Fatalf("failed source event = %+v", store.events[1])
	}
}
