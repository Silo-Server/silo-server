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
	advances       []MarkerAdvance   // every advance attempt, stored or not
	staleAdvance   bool              // reject advances as if an admin reset the marker
	recorded       map[string]string // source ID -> error message
	failures       []PollFailure     // every error write, with the rows the poll read
	getConnErr     error
	createdEvents  []EventCreate
	events         []EventFinish
	createEventErr error
	deliveries     []WebhookDelivery
	retried        []WebhookDelivery
	retryDelays    []time.Duration // delay of each RetryWebhookDelivery call
	retryMessages  []string        // last_error of each RetryWebhookDelivery call
	completed      []int64
	webhookErrors  map[string]string
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
	if f.getConnErr != nil {
		return Connection{}, f.getConnErr
	}
	return f.connection, nil
}
func (f *fakeStore) AdvanceMarker(_ context.Context, adv MarkerAdvance) (bool, error) {
	f.advances = append(f.advances, adv)
	if f.staleAdvance {
		return false, nil
	}
	if f.advanced == nil {
		f.advanced = map[string]string{}
	}
	f.advanced[adv.Source.ID] = adv.NextMarker
	return true, nil
}
func (f *fakeStore) RecordError(_ context.Context, failure PollFailure) (bool, error) {
	f.failures = append(f.failures, failure)
	if f.recorded == nil {
		f.recorded = map[string]string{}
	}
	f.recorded[failure.Source.ID] = failure.Message
	return true, nil
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
func (f *fakeStore) CreateWebhookDelivery(_ context.Context, in ChangeIngest) (WebhookDelivery, error) {
	delivery := WebhookDelivery{
		ID:                int64(len(f.deliveries) + 1),
		SourceID:          in.SourceID,
		ProviderEventType: in.ProviderEventType,
		Changes:           append([]Change(nil), in.Changes...),
		ReceivedAt:        in.ReceivedAt,
		AttemptCount:      1,
		LockedBy:          "immediate",
	}
	f.deliveries = append(f.deliveries, delivery)
	return delivery, nil
}
func (f *fakeStore) ClaimWebhookDeliveries(_ context.Context, workerID string, limit int) ([]WebhookDelivery, error) {
	if limit > len(f.retried) {
		limit = len(f.retried)
	}
	out := append([]WebhookDelivery(nil), f.retried[:limit]...)
	f.retried = f.retried[limit:]
	for i := range out {
		out[i].AttemptCount++
		out[i].LockedBy = workerID
	}
	return out, nil
}
func (f *fakeStore) CompleteWebhookDelivery(_ context.Context, id int64, _ string) error {
	f.completed = append(f.completed, id)
	return nil
}
func (f *fakeStore) RetryWebhookDelivery(_ context.Context, id int64, lockedBy string, delay time.Duration, msg string) error {
	f.retryDelays = append(f.retryDelays, delay)
	f.retryMessages = append(f.retryMessages, msg)
	for _, delivery := range f.deliveries {
		if delivery.ID == id {
			delivery.LockedBy = lockedBy
			f.retried = append(f.retried, delivery)
			break
		}
	}
	return nil
}
func (f *fakeStore) RecordWebhookError(_ context.Context, sourceID, msg string) error {
	if f.webhookErrors == nil {
		f.webhookErrors = map[string]string{}
	}
	f.webhookErrors[sourceID] = msg
	return nil
}
func (f *fakeStore) ClearWebhookError(_ context.Context, sourceID string) error {
	if f.webhookErrors != nil {
		delete(f.webhookErrors, sourceID)
	}
	return nil
}

// fakeProvider implements ScanSourceProvider, keyed by capability ID.
type fakeProvider struct {
	paths      map[string][]string // key: capabilityID, legacy source_paths behavior
	changes    map[string][]Change // key: capabilityID, structured changes
	nextMarker string
	err        error
	errByCap   map[string]error
	lastMarker string
	calls      int
}

func (f *fakeProvider) PollChanges(_ context.Context, _ string, capabilityID, marker string, _ ResolvedConnection, _ map[string]string) ([]Change, string, error) {
	f.calls++
	f.lastMarker = marker
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

func (r fakeResolver) ResolveVanishedFile(ctx context.Context, path, trigger string) (*scantrigger.Target, error) {
	return r.ResolveVanishedPath(ctx, path, trigger)
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

func (r distinctFolderResolver) ResolveVanishedFile(ctx context.Context, path, trigger string) (*scantrigger.Target, error) {
	return r.ResolveVanishedPath(ctx, path, trigger)
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

func (r mixedTransientResolver) ResolveVanishedFile(ctx context.Context, path, trigger string) (*scantrigger.Target, error) {
	return r.ResolveVanishedPath(ctx, path, trigger)
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

func (r transientFailureResolver) ResolveVanishedFile(ctx context.Context, path, trigger string) (*scantrigger.Target, error) {
	return r.ResolveVanishedPath(ctx, path, trigger)
}

// denySuppressor resolves paths normally but denies every claim, simulating a
// recently-scanned / debounced target (resolved but suppressed).
type denySuppressor struct{}

func (denySuppressor) ShouldScan(context.Context, string, string, time.Duration) (bool, error) {
	return false, nil
}
func (denySuppressor) Release(context.Context, string, string) error { return nil }

type recordingQueuer struct {
	enqueued       []scantrigger.Target
	batches        [][]scantrigger.Target
	autoscanEvents []int64
	createdCount   *int
}

func (q *recordingQueuer) EnqueueScans(_ context.Context, targets []scantrigger.Target) error {
	q.batches = append(q.batches, append([]scantrigger.Target(nil), targets...))
	q.enqueued = append(q.enqueued, targets...)
	return nil
}

// EnqueueAutoscanScans reports the first createdCount targets (all, when
// unset) as created runs and the rest as reused, naming each run after the
// target's position across every call ("run-0", "run-1", ...).
func (q *recordingQueuer) EnqueueAutoscanScans(_ context.Context, targets []scantrigger.Target, eventID int64) ([]scantrigger.EnqueueOutcome, error) {
	offset := len(q.enqueued)
	q.batches = append(q.batches, append([]scantrigger.Target(nil), targets...))
	q.enqueued = append(q.enqueued, targets...)
	q.autoscanEvents = append(q.autoscanEvents, eventID)
	created := len(targets)
	if q.createdCount != nil {
		created = *q.createdCount
	}
	outcomes := make([]scantrigger.EnqueueOutcome, len(targets))
	for i := range targets {
		outcomes[i] = scantrigger.EnqueueOutcome{RunID: "run-" + strconv.Itoa(offset+i), Created: i < created}
	}
	return outcomes, nil
}

type allowSuppressor struct{}

func (allowSuppressor) ShouldScan(context.Context, string, string, time.Duration) (bool, error) {
	return true, nil
}
func (allowSuppressor) Release(context.Context, string, string) error { return nil }

type recordingSuppressor struct {
	claimed  []string
	released []string
}

func (s *recordingSuppressor) ShouldScan(_ context.Context, key, _ string, _ time.Duration) (bool, error) {
	s.claimed = append(s.claimed, key)
	return true, nil
}
func (s *recordingSuppressor) Release(_ context.Context, key, _ string) error {
	s.released = append(s.released, key)
	return nil
}

type failingQueuer struct{}

func (failingQueuer) EnqueueScans(context.Context, []scantrigger.Target) error {
	return context.DeadlineExceeded
}
func (failingQueuer) EnqueueAutoscanScans(context.Context, []scantrigger.Target, int64) ([]scantrigger.EnqueueOutcome, error) {
	return nil, context.DeadlineExceeded
}

func newService(store Store, provider ScanSourceProvider, queue Queuer, suppress Suppressor) *Service {
	svc := NewService(store, provider, passthroughConnRes{}, fakeResolver{}, queue, suppress, nil)
	svc.observe = observeUnchangedFile
	return svc
}

// observeUnchangedFile reports every path as the same regular file, so poll
// tests claim deterministically without reading the host filesystem.
func observeUnchangedFile(string) (string, bool) { return "file:1:1", true }

// strptr is a tiny helper for the *string ConnectionID field in tests.
func strptr(s string) *string { return &s }

// A poll whose marker advance lost to an admin reset still consumed its
// window: the event succeeds and no error is recorded on the source.
func TestPollOnceStaleMarkerAdvanceIsNotAnError(t *testing.T) {
	store := &fakeStore{
		settings: Settings{Enabled: true, DefaultPollIntervalSeconds: 600, DebounceSeconds: 60},
		sources: []Source{{
			ID: "s1", PluginID: "silo.autoscan.arr", CapabilityID: "arr", ConnectionID: strptr("c1"), Enabled: true,
		}},
		staleAdvance: true,
	}
	prov := &fakeProvider{paths: map[string][]string{"arr": {"/mnt/media/Show/S01/E01.mkv"}}, nextMarker: "m1"}
	q := &recordingQueuer{}
	svc := newService(store, prov, q, allowSuppressor{})
	if err := svc.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(q.enqueued) != 1 {
		t.Fatalf("enqueued = %+v, want the window's scan", q.enqueued)
	}
	if _, ok := store.advanced["s1"]; ok {
		t.Fatal("stale advance must not store a marker")
	}
	if msg, ok := store.recorded["s1"]; ok {
		t.Fatalf("recorded error %q, want none", msg)
	}
	if len(store.events) != 1 || store.events[0].Status != EventStatusSuccess {
		t.Fatalf("events = %+v, want one successful event", store.events)
	}
	// The event must not claim an advance the source never stored.
	if got := store.events[0]; got.MarkerAfter != "" || got.ErrorMessage != markerNotStoredMessage {
		t.Fatalf("event marker_after = %q, message = %q; want the starting marker and a not-stored note", got.MarkerAfter, got.ErrorMessage)
	}
}

// A source edited after the cycle listed it is polled from its current row,
// so a marker an admin reset is not sent to the new upstream.
func TestPollOncePollsFromCurrentSourceRow(t *testing.T) {
	stale := "m-old-upstream"
	listed := Source{
		ID: "s1", PluginID: "silo.autoscan.arr", CapabilityID: "arr", ConnectionID: strptr("c1"), Enabled: true,
		Marker: &stale,
	}
	current := listed
	current.Marker = nil // reset by a connection edit after the list was read
	store := &rereadStore{
		fakeStore: &fakeStore{
			settings:   Settings{Enabled: true, DefaultPollIntervalSeconds: 600, DebounceSeconds: 60},
			sources:    []Source{listed},
			connection: Connection{ID: "c1", Kind: "sonarr", BaseURL: "http://sonarr-b.invalid"},
		},
		current: map[string]Source{"s1": current},
	}
	prov := &fakeProvider{nextMarker: "m1"}
	if err := newService(store, prov, &recordingQueuer{}, allowSuppressor{}).PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if prov.calls != 1 || prov.lastMarker != "" {
		t.Fatalf("provider calls = %d with marker %q, want one call from the reset marker", prov.calls, prov.lastMarker)
	}
	if len(store.createdEvents) != 1 || store.createdEvents[0].MarkerBefore != "" {
		t.Fatalf("created events = %+v, want marker_before from the current row", store.createdEvents)
	}
}

// A source disabled or removed after the cycle listed it is not polled.
func TestPollOnceSkipsSourceDisabledOrRemovedSinceListing(t *testing.T) {
	listed := []Source{
		{ID: "disabled", PluginID: "silo.autoscan.arr", CapabilityID: "arr", Enabled: true},
		{ID: "removed", PluginID: "silo.autoscan.arr", CapabilityID: "arr", Enabled: true},
	}
	disabled := listed[0]
	disabled.Enabled = false
	store := &rereadStore{
		fakeStore: &fakeStore{
			settings: Settings{Enabled: true, DefaultPollIntervalSeconds: 600, DebounceSeconds: 60},
			sources:  listed,
		},
		current: map[string]Source{"disabled": disabled},
	}
	prov := &fakeProvider{nextMarker: "m1"}
	if err := newService(store, prov, &recordingQueuer{}, allowSuppressor{}).PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if prov.calls != 0 || len(store.createdEvents) != 0 {
		t.Fatalf("provider calls = %d, events = %d; want none", prov.calls, len(store.createdEvents))
	}
}

// A source whose current row cannot be read is skipped for the cycle rather
// than polled with a listed marker that may belong to a replaced upstream.
func TestPollOnceSkipsSourceWhenRereadFails(t *testing.T) {
	stale := "m-old-upstream"
	store := &rereadStore{
		fakeStore: &fakeStore{
			settings: Settings{Enabled: true, DefaultPollIntervalSeconds: 600, DebounceSeconds: 60},
			sources: []Source{{
				ID: "s1", PluginID: "silo.autoscan.arr", CapabilityID: "arr", ConnectionID: strptr("c1"), Enabled: true,
				Marker: &stale,
			}},
		},
		err: errors.New("connection reset"),
	}
	prov := &fakeProvider{nextMarker: "m1"}
	if err := newService(store, prov, &recordingQueuer{}, allowSuppressor{}).PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if prov.calls != 0 || len(store.createdEvents) != 0 {
		t.Fatalf("provider calls = %d, events = %d; want none", prov.calls, len(store.createdEvents))
	}
}

// rereadStore serves the cycle's source list from fakeStore but answers
// GetSource from current, as if the rows changed after the list was read.
type rereadStore struct {
	*fakeStore
	current map[string]Source
	err     error
}

func (r *rereadStore) GetSource(_ context.Context, id string) (Source, error) {
	if r.err != nil {
		return Source{}, r.err
	}
	src, ok := r.current[id]
	if !ok {
		return Source{}, ErrNotFound
	}
	return src, nil
}

func TestPollOnceDebouncesFileChangesOnReportedPathNotWidenedTarget(t *testing.T) {
	// Two different videos in one directory both widen to the same subtree
	// target. The suppression claim must be keyed on each reported file so a
	// later event for the second file is not swallowed by the first file's
	// window; the queue coalesces it into the running scan and owes a
	// follow-up. Within one cycle they still collapse to one enqueue.
	store := &fakeStore{
		settings: Settings{Enabled: true, DefaultPollIntervalSeconds: 600, DebounceSeconds: 60},
		sources: []Source{{
			ID: "s1", PluginID: "silo.autoscan.cephfs", CapabilityID: "cephfs", Enabled: true,
		}},
	}
	prov := &fakeProvider{changes: map[string][]Change{
		"cephfs": {
			{SourcePath: "/mnt/media/tv/Show/S01/E01.mkv", Scope: ChangeScopeFile},
			{SourcePath: "/mnt/media/tv/Show/S01/E02.mkv", Scope: ChangeScopeFile},
		},
	}, nextMarker: "m1"}
	q := &recordingQueuer{}
	sup := &recordingSuppressor{}
	svc := NewService(store, prov, passthroughConnRes{}, directoryWideningResolver{}, q, sup, nil)
	svc.observe = observeUnchangedFile
	if err := svc.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(q.enqueued) != 1 || q.enqueued[0].Path != "/mnt/media/tv/Show/S01" {
		t.Fatalf("expected one directory scan, got %+v", q.enqueued)
	}
	want := []string{"7|/mnt/media/tv/Show/S01/E01.mkv", "7|/mnt/media/tv/Show/S01/E02.mkv"}
	if len(sup.claimed) != 2 || sup.claimed[0] != want[0] || sup.claimed[1] != want[1] {
		t.Fatalf("claimed keys = %v, want %v", sup.claimed, want)
	}
	if len(store.events) != 1 {
		t.Fatalf("expected one finished event, got %d", len(store.events))
	}
	if ev := store.events[0]; ev.ScansSuppressed != 0 || ev.TargetsClaimed != 1 {
		t.Fatalf("event counts = %+v, want 0 suppressed and 1 target", ev)
	}
}

// directoryWideningResolver mimics the real resolver's handling of video file
// paths: an existing file resolves to a subtree scan of its directory.
type directoryWideningResolver struct{ fakeResolver }

func (directoryWideningResolver) Resolve(_ context.Context, req scantrigger.Request) (*scantrigger.Target, error) {
	if !strings.HasPrefix(req.Path, "/mnt/media/") {
		return nil, nil
	}
	path := req.Path
	if filepath.Ext(path) == ".mkv" {
		path = filepath.Dir(path)
	}
	return &scantrigger.Target{Folder: &models.MediaFolder{ID: 7}, Mode: scantrigger.ModeSubtree, Path: path, Trigger: req.Trigger}, nil
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

// A failed poll's error is written against the rows the poll read, so the
// store can drop it when an admin reset the source while the poll ran.
func TestPollOnceRecordsAFailureAgainstTheRowsThePollRead(t *testing.T) {
	store := &fakeStore{
		settings: Settings{Enabled: true, DefaultPollIntervalSeconds: 600},
		sources: []Source{{
			ID: "s1", PluginID: "silo.autoscan.arr", CapabilityID: "arr", ConnectionID: strptr("c1"),
			Marker: strptr("m0"), SourceConfig: map[string]string{"scope": "all"}, Enabled: true,
		}},
		connection: Connection{ID: "c1", Kind: "sonarr", BaseURL: "http://sonarr.invalid"},
	}
	prov := &fakeProvider{err: errors.New("dial tcp: i/o timeout")}
	svc := NewService(store, prov, passthroughConnRes{}, nil, &recordingQueuer{}, allowSuppressor{}, nil)
	if err := svc.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(store.failures) != 1 {
		t.Fatalf("error writes = %d, want 1", len(store.failures))
	}
	failure := store.failures[0]
	if failure.Source.Marker == nil || *failure.Source.Marker != "m0" || failure.Source.SourceConfig["scope"] != "all" {
		t.Fatalf("failure source = %+v, want the row the poll read", failure.Source)
	}
	if failure.Connection == nil || failure.Connection.ID != "c1" || failure.Connection.BaseURL != "http://sonarr.invalid" {
		t.Fatalf("failure connection = %+v, want the row the poll resolved", failure.Connection)
	}
	if len(store.events) != 1 || store.events[0].Status != EventStatusError || store.events[0].ErrorMessage != failure.Message {
		t.Fatalf("events = %+v, want the error on the poll's event", store.events)
	}
}

type failingConnRes struct{}

func (failingConnRes) Resolve(context.Context, Connection) (ResolvedConnection, error) {
	return ResolvedConnection{}, errors.New("linked Requests integration is unavailable")
}

// A poll that fails resolving its connection compares the connection row when
// it read one, and only the source's own columns when the lookup itself failed.
func TestPollOnceResolveFailureCarriesTheRowItRead(t *testing.T) {
	for name, tc := range map[string]struct {
		getConnErr error
		wantRow    bool
	}{
		"credentials failed after the row was read": {wantRow: true},
		"the row lookup failed":                     {getConnErr: errors.New("connection refused")},
	} {
		t.Run(name, func(t *testing.T) {
			store := &fakeStore{
				settings: Settings{Enabled: true, DefaultPollIntervalSeconds: 600},
				sources: []Source{{
					ID: "s1", PluginID: "silo.autoscan.arr", CapabilityID: "arr", ConnectionID: strptr("c1"), Enabled: true,
				}},
				connection: Connection{ID: "c1", Kind: "sonarr", BaseURL: "http://sonarr.invalid"},
				getConnErr: tc.getConnErr,
			}
			prov := &fakeProvider{}
			svc := NewService(store, prov, failingConnRes{}, nil, &recordingQueuer{}, allowSuppressor{}, nil)
			if err := svc.PollOnce(context.Background()); err != nil {
				t.Fatalf("PollOnce: %v", err)
			}
			if prov.calls != 0 {
				t.Fatalf("plugin polled %d times after the connection failed", prov.calls)
			}
			if len(store.failures) != 1 {
				t.Fatalf("error writes = %d, want 1", len(store.failures))
			}
			got := store.failures[0].Connection
			if tc.wantRow && (got == nil || got.ID != "c1") {
				t.Fatalf("failure connection = %+v, want the row the poll read", got)
			}
			if !tc.wantRow && got != nil {
				t.Fatalf("failure connection = %+v, want none after the lookup failed", got)
			}
		})
	}
}

// TestPollOnceHoldsMarkerOnTransientResolveFailures covers paths the resolver
// fails on INTERNALLY (e.g. a database fault), not because they are outside
// Silo's libraries. Advancing would silently skip real imports, so the marker
// must HOLD and an error be recorded; the window is retried once the fault
// clears. Paths that did resolve still enqueue.
func TestPollOnceHoldsMarkerOnTransientResolveFailures(t *testing.T) {
	for _, tc := range []struct {
		name         string
		resolver     Resolver
		paths        []string
		wantEnqueued int
	}{
		{name: "every path fails", resolver: transientFailureResolver{},
			paths: []string{"/mnt/media/Show/S01/E01.mkv", "/mnt/media/Show/S01/E02.mkv"}},
		{name: "one path resolves", resolver: mixedTransientResolver{},
			paths: []string{"/mnt/media/Show/S01/E01.mkv", "/data/other/Movie/movie.mkv"}, wantEnqueued: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{
				settings: Settings{Enabled: true, DefaultPollIntervalSeconds: 600, DebounceSeconds: 60},
				sources: []Source{{
					ID: "s1", PluginID: "silo.autoscan.arr", CapabilityID: "arr", ConnectionID: strptr("c1"), Enabled: true,
				}},
			}
			prov := &fakeProvider{paths: map[string][]string{"arr": tc.paths}, nextMarker: "m1"}
			q := &recordingQueuer{}
			svc := NewService(store, prov, passthroughConnRes{}, tc.resolver, q, allowSuppressor{}, nil)
			if err := svc.PollOnce(context.Background()); err != nil {
				t.Fatalf("PollOnce: %v", err)
			}
			if len(q.enqueued) != tc.wantEnqueued {
				t.Fatalf("enqueued %d scans, want %d", len(q.enqueued), tc.wantEnqueued)
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
			if event.ChangesReturned != 2 || event.ChangesResolved != tc.wantEnqueued {
				t.Fatalf("event counts = %+v", event)
			}
		})
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

func TestPollNowStillSkipsWhenDisabledAndWebhookSources(t *testing.T) {
	prov := &fakeProvider{paths: map[string][]string{"arr": {"/mnt/media/Show/S01/E01.mkv"}}, nextMarker: "m1"}

	off := &fakeStore{
		settings: Settings{Enabled: false, DefaultPollIntervalSeconds: 600, DebounceSeconds: 60},
		sources:  []Source{{ID: "s1", PluginID: "silo.autoscan.arr", CapabilityID: "arr", Enabled: true}},
	}
	if err := newService(off, prov, &recordingQueuer{}, allowSuppressor{}).PollNow(context.Background()); err != nil {
		t.Fatalf("PollNow: %v", err)
	}
	if len(off.createdEvents) != 0 || len(off.advanced) != 0 {
		t.Fatalf("PollNow must not poll while autoscan is disabled")
	}

	webhook := &fakeStore{
		settings: Settings{Enabled: true, DefaultPollIntervalSeconds: 600, DebounceSeconds: 60},
		sources: []Source{{
			ID: "s1", PluginID: "silo.autoscan.arr", CapabilityID: "arr", Enabled: true, DeliveryMode: DeliveryModeWebhook,
		}},
	}
	if err := newService(webhook, prov, &recordingQueuer{}, allowSuppressor{}).PollNow(context.Background()); err != nil {
		t.Fatalf("PollNow: %v", err)
	}
	if len(webhook.createdEvents) != 0 || len(webhook.advanced) != 0 {
		t.Fatalf("PollNow must not poll webhook sources")
	}
}
