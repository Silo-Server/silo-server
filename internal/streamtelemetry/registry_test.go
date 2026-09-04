package streamtelemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/httpstream"
)

func testConfig() Config {
	cfg := DefaultConfig("test-node")
	cfg.Enabled = true
	cfg.PublisherID = "test-publisher"
	cfg.PublisherEpoch = 1
	cfg.Retention = time.Millisecond
	return cfg
}

func testRoute(class Class) MediaRoute {
	return MediaRoute{Family: FamilyNative, Method: http.MethodGet, Pattern: "/media/{id}",
		Class: class, Role: RoleViewerEgress, CapRelevant: class != ClassTransfer, Enrolled: true}
}

func testAttachment(id string) Attachment {
	return Attachment{Subject: UserSubject(7), ProfileID: "profile", SessionID: id, MediaFileID: 42,
		PlayMethod: "direct", StartedAt: time.Unix(100, 0), StartedAtSource: StartedAtSourceSession,
		TokenIssuedAtSource: TokenIssuedAtSourceNone}
}

func TestProvisionalObservationDoesNotCreateLogicalActivity(t *testing.T) {
	registry := NewRegistry(testConfig(), NewLocalStore(), slog.New(slog.DiscardHandler))
	handler := registry.Observe(testRoute(ClassPlayback))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("denied"))
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/media/x", nil))
	snapshot := registry.Sweep()
	if len(snapshot.Sessions) != 0 || len(snapshot.Transfers) != 0 {
		t.Fatalf("provisional request created logical activity: %+v", snapshot)
	}
	if snapshot.UnattributedObservations != 1 || snapshot.UnattributedBytes != 6 {
		t.Fatalf("unattributed counters = %d/%d", snapshot.UnattributedObservations, snapshot.UnattributedBytes)
	}
}

func TestReleaseFoldsShortObservationAndCollectorAdvancesByteClock(t *testing.T) {
	registry := NewRegistry(testConfig(), NewLocalStore(), nil)
	handler := registry.Observe(testRoute(ClassPlayback))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Attach(r.Context(), testAttachment("session-1"))
		_, _ = w.Write([]byte("payload"))
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/media/x", nil))
	before := registry.Snapshot()
	if len(before.Sessions) != 1 || before.Sessions[0].OpenObservations != 0 {
		t.Fatalf("released session = %+v", before.Sessions)
	}
	swept := registry.Sweep()
	if swept.Sessions[0].BytesAccepted != 7 || swept.Sessions[0].LastByteAccepted.IsZero() {
		t.Fatalf("swept session = %+v", swept.Sessions[0])
	}
	if got := swept.Sessions[0].Routes[0].BytesAccepted; got != 7 {
		t.Fatalf("route bytes = %d", got)
	}
}

func TestReleaseConcurrentWithSweepDoesNotLoseOrDoubleCount(t *testing.T) {
	registry := NewRegistry(testConfig(), NewLocalStore(), nil)
	obs := registry.begin(testRoute(ClassPlayback), CaptureSet{Method: http.MethodGet, Pattern: "/media/{id}", ReceivedAt: time.Now()})
	registry.attach(obs, testAttachment("session-race"))
	obs.AddBytes(4096)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); registry.release(obs, httpstreamOutcomeCompleted) }()
	go func() { defer wg.Done(); _ = registry.Sweep() }()
	wg.Wait()
	snapshot := registry.Sweep()
	if got := snapshot.Sessions[0].BytesAccepted; got != 4096 {
		t.Fatalf("bytes after concurrent release/sweep = %d", got)
	}
}

const httpstreamOutcomeCompleted = "completed"

func TestExactObservationBoundServesThroughAndCountsDrops(t *testing.T) {
	cfg := testConfig()
	cfg.MaxObservations = 2
	registry := NewRegistry(cfg, NewLocalStore(), nil)
	one := registry.begin(testRoute(ClassPlayback), CaptureSet{})
	two := registry.begin(testRoute(ClassPlayback), CaptureSet{})
	three := registry.begin(testRoute(ClassPlayback), CaptureSet{})
	three.AddBytes(9)
	registry.release(three, OutcomeUnknown)
	if !three.countingOnly || registry.observationReservations.Load() != 2 {
		t.Fatalf("bound was not exact: counting=%t reservations=%d", three.countingOnly, registry.observationReservations.Load())
	}
	registry.release(one, OutcomeUnknown)
	registry.release(two, OutcomeUnknown)
	snapshot := registry.Snapshot()
	if !snapshot.Truncated || snapshot.DroppedObservations != 1 || snapshot.DroppedBytes != 9 {
		t.Fatalf("drop counters = %+v", snapshot)
	}
}

func TestStartedAtImprovementAndIdentityConflict(t *testing.T) {
	registry := NewRegistry(testConfig(), NewLocalStore(), nil)
	obs := registry.begin(testRoute(ClassPlayback), CaptureSet{Method: http.MethodGet, Pattern: "/media/{id}", ReceivedAt: time.Unix(200, 0)})
	first := testAttachment("session-conflict")
	first.StartedAt = time.Time{}
	first.StartedAtSource = ""
	registry.attach(obs, first)
	offered := first
	offered.Subject = UserSubject(8)
	offered.StartedAt = time.Unix(50, 0)
	offered.StartedAtSource = StartedAtSourceClaim
	registry.attach(obs, offered)
	registry.release(obs, httpstreamOutcomeCompleted)
	snapshot := registry.Sweep()
	session := snapshot.Sessions[0]
	if !session.HasIdentityConflict || session.Subject != UserSubject(7) {
		t.Fatalf("conflict did not preserve identity: %+v", session)
	}
	if session.StartedAtSource != StartedAtSourceClaim || !session.StartedAt.Equal(offered.StartedAt) || session.StartedAtDegraded {
		t.Fatalf("started-at authority was not improved: %+v", session)
	}
}

func TestMidPlaybackReplanUpdatesStateWithoutIdentityConflict(t *testing.T) {
	registry := NewRegistry(testConfig(), NewLocalStore(), nil)
	first := testAttachment("session-replan")
	first.MediaFileID = 100
	first.PlayMethod = "direct"
	obs := registry.begin(testRoute(ClassPlayback), CaptureSet{Method: http.MethodGet, Pattern: "/media/{id}", ReceivedAt: time.Now()})
	registry.attach(obs, first)
	registry.release(obs, httpstreamOutcomeCompleted)

	replanned := first
	replanned.MediaFileID = 200
	replanned.PlayMethod = "transcode"
	obs = registry.begin(testRoute(ClassPlayback), CaptureSet{Method: http.MethodGet, Pattern: "/media/{id}", ReceivedAt: time.Now()})
	registry.attach(obs, replanned)
	registry.release(obs, httpstreamOutcomeCompleted)

	session := registry.Sweep().Sessions[0]
	if session.HasIdentityConflict || len(session.IdentityConflicts) != 0 {
		t.Fatalf("replan recorded identity conflict: %+v", session.IdentityConflicts)
	}
	if session.MediaFileID != 200 || session.PlayMethod != "transcode" {
		t.Fatalf("current replan state = media %d, method %q", session.MediaFileID, session.PlayMethod)
	}
	if len(session.MediaFileIDs) != 2 || session.MediaFileIDs[0] != 100 || session.MediaFileIDs[1] != 200 {
		t.Fatalf("observed media files = %v", session.MediaFileIDs)
	}
	if len(session.PlayMethods) != 2 || session.PlayMethods[0] != "direct" || session.PlayMethods[1] != "transcode" {
		t.Fatalf("observed play methods = %v", session.PlayMethods)
	}

	changedOwner := replanned
	changedOwner.Subject = UserSubject(8)
	obs = registry.begin(testRoute(ClassPlayback), CaptureSet{Method: http.MethodGet, Pattern: "/media/{id}", ReceivedAt: time.Now()})
	registry.attach(obs, changedOwner)
	registry.release(obs, httpstreamOutcomeCompleted)
	if session = registry.Sweep().Sessions[0]; !session.HasIdentityConflict {
		t.Fatal("changed subject did not record an identity conflict")
	}
}

func TestUnknownAttachmentFieldsDoNotDisagreeWithSession(t *testing.T) {
	registry := NewRegistry(testConfig(), NewLocalStore(), nil)
	first := testAttachment("session-partial")
	obs := registry.begin(testRoute(ClassPlayback), CaptureSet{Method: http.MethodGet, Pattern: "/media/{id}", ReceivedAt: time.Now()})
	registry.attach(obs, first)
	registry.release(obs, httpstreamOutcomeCompleted)

	partial := Attachment{SessionID: first.SessionID}
	obs = registry.begin(testRoute(ClassPlayback), CaptureSet{Method: http.MethodGet, Pattern: "/media/{id}", ReceivedAt: time.Now()})
	registry.attach(obs, partial)
	registry.release(obs, httpstreamOutcomeCompleted)

	session := registry.Sweep().Sessions[0]
	if session.HasIdentityConflict || len(session.IdentityConflicts) != 0 {
		t.Fatalf("unknown fields recorded disagreement: %+v", session.IdentityConflicts)
	}
	if session.MediaFileID != first.MediaFileID || session.PlayMethod != first.PlayMethod {
		t.Fatalf("unknown fields replaced current state: media %d, method %q", session.MediaFileID, session.PlayMethod)
	}
}

func TestPruneReleasesReservations(t *testing.T) {
	cfg := testConfig()
	registry := NewRegistry(cfg, NewLocalStore(), nil)
	obs := registry.begin(testRoute(ClassPlayback), CaptureSet{Method: http.MethodGet, Pattern: "/media/{id}", ReceivedAt: time.Now()})
	registry.attach(obs, testAttachment("prune"))
	registry.release(obs, httpstreamOutcomeCompleted)
	registry.sweep(time.Now().Add(2 * cfg.Retention))
	if registry.sessionReservations.Load() != 0 || registry.observationReservations.Load() != 0 {
		t.Fatalf("reservations leaked: sessions=%d observations=%d", registry.sessionReservations.Load(), registry.observationReservations.Load())
	}
}

func recordSessionBytes(t *testing.T, registry *Registry, id, pattern string, role Role, bytes int64, endedAt time.Time) {
	t.Helper()
	route := testRoute(ClassPlayback)
	route.Pattern = pattern
	route.Role = role
	obs := registry.begin(route, CaptureSet{Method: http.MethodGet, Pattern: pattern, ReceivedAt: endedAt.Add(-time.Second)})
	registry.attach(obs, testAttachment(id))
	obs.AddBytes(bytes)
	previousNow := now
	now = func() time.Time { return endedAt }
	registry.release(obs, httpstreamOutcomeCompleted)
	now = previousNow
}

func TestPruneLeavesFrozenByteTombstone(t *testing.T) {
	cfg := testConfig()
	cfg.TombstoneRetention = time.Hour
	registry := NewRegistry(cfg, NewLocalStore(), nil)
	endedAt := time.Unix(1_700_000_000, 0)
	recordSessionBytes(t, registry, "remembered", "/viewer", RoleViewerEgress, 4096, endedAt)

	prunedAt := endedAt.Add(2 * cfg.Retention)
	snapshot := registry.sweep(prunedAt)
	if len(snapshot.Sessions) != 1 {
		t.Fatalf("sessions = %+v, want one tombstone", snapshot.Sessions)
	}
	got := snapshot.Sessions[0]
	if !got.MeasurementPruned || got.BytesAccepted != 4096 || got.OpenObservations != 0 || got.RealtimeConnectionAlive {
		t.Fatalf("tombstone liveness/bytes = %+v", got)
	}
	if len(got.Routes) != 1 || got.Routes[0].Role != RoleViewerEgress || got.Routes[0].BytesAccepted != 4096 ||
		got.Routes[0].Open != 0 || !got.Routes[0].LastByteAccepted.Equal(prunedAt) {
		t.Fatalf("tombstone routes = %+v", got.Routes)
	}
	if len(got.ViewerIPs) != 0 || len(got.DeviceIDs) != 0 || len(got.UserAgents) != 0 ||
		len(got.ClientVariants) != 0 || len(got.MediaFileIDs) != 0 || len(got.PlayMethods) != 0 ||
		len(got.TokenIssuedAts) != 0 || len(got.IdentityConflicts) != 0 || len(got.Outcomes) != 0 {
		t.Fatalf("tombstone retained bounded-set state: %+v", got)
	}
}

func TestPruneWithoutBytesLeavesNoTombstone(t *testing.T) {
	cfg := testConfig()
	registry := NewRegistry(cfg, NewLocalStore(), nil)
	endedAt := time.Unix(1_700_000_000, 0)
	recordSessionBytes(t, registry, "empty", "/viewer", RoleViewerEgress, 0, endedAt)
	if snapshot := registry.sweep(endedAt.Add(2 * cfg.Retention)); len(snapshot.Sessions) != 0 {
		t.Fatalf("byte-less session left a tombstone: %+v", snapshot.Sessions)
	}
}

func TestReattachClearsSessionTombstone(t *testing.T) {
	cfg := testConfig()
	cfg.TombstoneRetention = time.Hour
	registry := NewRegistry(cfg, NewLocalStore(), nil)
	endedAt := time.Unix(1_700_000_000, 0)
	recordSessionBytes(t, registry, "revived", "/viewer", RoleViewerEgress, 100, endedAt)
	registry.sweep(endedAt.Add(2 * cfg.Retention))

	recordSessionBytes(t, registry, "revived", "/viewer", RoleViewerEgress, 7, endedAt.Add(time.Minute))
	snapshot := registry.sweep(endedAt.Add(time.Minute + cfg.Retention/2))
	if len(snapshot.Sessions) != 1 || snapshot.Sessions[0].MeasurementPruned || snapshot.Sessions[0].BytesAccepted != 7 {
		t.Fatalf("revived session counted against its tombstone: %+v", snapshot.Sessions)
	}
}

func TestTombstonesExpireAtRetention(t *testing.T) {
	cfg := testConfig()
	cfg.TombstoneRetention = 10 * time.Millisecond
	registry := NewRegistry(cfg, NewLocalStore(), nil)
	endedAt := time.Unix(1_700_000_000, 0)
	recordSessionBytes(t, registry, "expires", "/viewer", RoleViewerEgress, 1, endedAt)
	prunedAt := endedAt.Add(2 * cfg.Retention)
	registry.sweep(prunedAt)
	if snapshot := registry.sweep(prunedAt.Add(cfg.TombstoneRetention)); len(snapshot.Sessions) != 0 {
		t.Fatalf("expired tombstone remained: %+v", snapshot.Sessions)
	}
}

func TestTombstoneBoundEvictsOldest(t *testing.T) {
	cfg := testConfig()
	cfg.MaxSessions = 1
	cfg.TombstoneRetention = time.Hour
	registry := NewRegistry(cfg, NewLocalStore(), slog.New(slog.DiscardHandler))
	ids := make([]string, 0, 2)
	for candidate := 0; len(ids) < 2; candidate++ {
		id := fmt.Sprintf("same-shard-%d", candidate)
		if len(ids) == 0 || registry.shard(id) == registry.shard(ids[0]) {
			ids = append(ids, id)
		}
	}
	base := time.Unix(1_700_000_000, 0)
	for i, id := range ids {
		endedAt := base.Add(time.Duration(i) * time.Minute)
		recordSessionBytes(t, registry, id, "/viewer", RoleViewerEgress, int64(i+1), endedAt)
		registry.sweep(endedAt.Add(2 * cfg.Retention))
	}
	snapshot := registry.SnapshotAt(base.Add(2 * time.Minute))
	if len(snapshot.Sessions) != 1 || snapshot.Sessions[0].SessionID != ids[1] {
		t.Fatalf("bounded tombstones = %+v, want only freshest %q", snapshot.Sessions, ids[1])
	}
	if snapshot.Truncated || snapshot.DroppedObservations != 0 {
		t.Fatalf("tombstone eviction reported observation loss: truncated=%v dropped=%d", snapshot.Truncated, snapshot.DroppedObservations)
	}
}

func TestTombstoneKeepsRouteOverflowDegradation(t *testing.T) {
	cfg := testConfig()
	cfg.MaxRoutesPerSession = 1
	cfg.TombstoneRetention = time.Hour
	registry := NewRegistry(cfg, NewLocalStore(), nil)
	endedAt := time.Unix(1_700_000_000, 0)
	recordSessionBytes(t, registry, "overflow", "/one", RoleViewerEgress, 1, endedAt)
	recordSessionBytes(t, registry, "overflow", "/two", RoleViewerEgress, 2, endedAt)
	snapshot := registry.sweep(endedAt.Add(2 * cfg.Retention))
	if len(snapshot.Sessions) != 1 || !snapshot.Sessions[0].MeasurementPruned || !snapshot.Sessions[0].RoutesOverflowed {
		t.Fatalf("route-overflow tombstone = %+v", snapshot.Sessions)
	}
}

func TestRouteBoundDropsNewestRouteWithoutDroppingObservation(t *testing.T) {
	cfg := testConfig()
	cfg.MaxRoutesPerSession = 1
	registry := NewRegistry(cfg, NewLocalStore(), nil)
	for index, pattern := range []string{"/one", "/two"} {
		obs := registry.begin(testRoute(ClassPlayback), CaptureSet{Method: http.MethodGet, Pattern: pattern, ReceivedAt: time.Now()})
		registry.attach(obs, testAttachment("route-bound"))
		obs.AddBytes(int64(index + 1))
		registry.release(obs, httpstreamOutcomeCompleted)
	}
	snapshot := registry.Sweep()
	session := snapshot.Sessions[0]
	if !session.RoutesOverflowed || len(session.Routes) != 1 || session.BytesAccepted != 3 || snapshot.DroppedObservations != 0 {
		t.Fatalf("route overflow = %+v", snapshot)
	}
}

func TestSetRealtimeConnectionIgnoresUnknownAndIsIdempotent(t *testing.T) {
	registry := NewRegistry(testConfig(), NewLocalStore(), nil)
	registry.SetRealtimeConnection("missing", true)
	if len(registry.Snapshot().Sessions) != 0 {
		t.Fatal("realtime update created a session")
	}
	obs := registry.begin(testRoute(ClassPlayback), CaptureSet{Method: http.MethodGet, Pattern: "/media/{id}", ReceivedAt: time.Now()})
	registry.attach(obs, testAttachment("known"))
	registry.SetRealtimeConnection("known", true)
	registry.SetRealtimeConnection("known", true)
	if !registry.Snapshot().Sessions[0].RealtimeConnectionAlive {
		t.Fatal("realtime connection not recorded")
	}
	registry.release(obs, httpstreamOutcomeCompleted)
}

type failingStore struct{ published atomic.Int64 }

func (s *failingStore) Publish(context.Context, Snapshot) error {
	s.published.Add(1)
	return errors.New("publish failed")
}
func (s *failingStore) Load(context.Context) (Snapshot, error) { return Snapshot{}, nil }

func TestStartContinuesAfterPublishError(t *testing.T) {
	cfg := testConfig()
	cfg.SweepInterval = time.Millisecond
	store := &failingStore{}
	registry := NewRegistry(cfg, store, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithCancel(context.Background())
	registry.Start(ctx)
	time.Sleep(8 * time.Millisecond)
	cancel()
	// Wait for the collector to actually exit. Returning while it still runs
	// leaks a goroutine that keeps reading the package-level now() seam, which
	// races with any later test that replaces it.
	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err := registry.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	if store.published.Load() < 2 {
		t.Fatalf("collector stopped after publish error: %d publishes", store.published.Load())
	}
}

type lifecycleStore struct {
	mu             sync.Mutex
	published      []Snapshot
	leaveCalls     int
	failFirstLeave bool
}

type firstSnapshotStore struct {
	published chan Snapshot
}

func (s *firstSnapshotStore) Publish(_ context.Context, snapshot Snapshot) error {
	select {
	case s.published <- snapshot:
	default:
	}
	return nil
}

func (s *firstSnapshotStore) Load(context.Context) (Snapshot, error) { return Snapshot{}, nil }

func (s *lifecycleStore) Publish(_ context.Context, snapshot Snapshot) error {
	s.mu.Lock()
	s.published = append(s.published, snapshot)
	s.mu.Unlock()
	return nil
}
func (s *lifecycleStore) Load(context.Context) (Snapshot, error)        { return Snapshot{}, nil }
func (s *lifecycleStore) LoadAll(context.Context) (PublisherSet, error) { return PublisherSet{}, nil }
func (s *lifecycleStore) Leave(ctx context.Context) error {
	s.mu.Lock()
	s.leaveCalls++
	call := s.leaveCalls
	s.mu.Unlock()
	if s.failFirstLeave && call == 1 {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func TestRegistryStartOnceAndPublishedSequence(t *testing.T) {
	cfg := testConfig()
	cfg.SweepInterval = time.Millisecond
	store := &lifecycleStore{}
	registry := NewRegistry(cfg, store, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithCancel(context.Background())
	registry.Start(ctx)
	registry.Start(ctx)
	time.Sleep(6 * time.Millisecond)
	cancel()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err := registry.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.published) < 2 {
		t.Fatalf("publishes = %d", len(store.published))
	}
	for index, snapshot := range store.published {
		if snapshot.Sequence != uint64(index+1) {
			t.Fatalf("sequence[%d] = %d", index, snapshot.Sequence)
		}
		if snapshot.PublisherEpoch != cfg.PublisherEpoch {
			t.Fatalf("epoch = %d", snapshot.PublisherEpoch)
		}
	}
}

func TestRegistryConcurrentStopLeavesOnce(t *testing.T) {
	store := &lifecycleStore{}
	registry := NewRegistry(testConfig(), store, nil)
	registry.Start(context.Background())
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := registry.Stop(context.Background()); err != nil {
				t.Errorf("Stop: %v", err)
			}
		}()
	}
	wg.Wait()
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.leaveCalls != 1 {
		t.Fatalf("leave calls = %d", store.leaveCalls)
	}
}

func TestRegistryStopTimeoutCanRetryLeave(t *testing.T) {
	store := &lifecycleStore{failFirstLeave: true}
	registry := NewRegistry(testConfig(), store, nil)
	registry.Start(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := registry.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first Stop = %v", err)
	}
	if err := registry.Stop(context.Background()); err != nil {
		t.Fatalf("retry Stop = %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.leaveCalls != 2 {
		t.Fatalf("leave calls = %d", store.leaveCalls)
	}
}

func TestRegistryStopNilDisabledAndNeverStarted(t *testing.T) {
	var nilRegistry *Registry
	if err := nilRegistry.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	disabled := testConfig()
	disabled.Enabled = false
	if err := NewRegistry(disabled, NewLocalStore(), nil).Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := NewRegistry(testConfig(), NewLocalStore(), nil).Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryGlobalView(t *testing.T) {
	at := time.Now()
	store := NewLocalStore()
	registry := NewRegistry(testConfig(), store, nil)
	if err := store.Publish(context.Background(), Snapshot{PublisherID: "test-publisher", PublisherEpoch: 1, Sequence: 1, CapturedAt: at, Coverage: fullCoverage()}); err != nil {
		t.Fatal(err)
	}
	originalNow := now
	now = func() time.Time { return at }
	defer func() { now = originalNow }()
	view, err := registry.GlobalView(context.Background())
	if err != nil || !view.Complete || len(view.Publishers) != 1 {
		t.Fatalf("view = %+v, err=%v", view, err)
	}
	unsupported := NewRegistry(testConfig(), &failingStore{}, nil)
	if _, err := unsupported.GlobalView(context.Background()); err == nil {
		t.Fatal("non-global store accepted")
	} else {
		var typed errGlobalSnapshotStoreUnsupported
		if !errors.As(err, &typed) {
			t.Fatalf("error = %T %v", err, err)
		}
	}
	var nilRegistry *Registry
	if zero, err := nilRegistry.GlobalView(context.Background()); err != nil || !reflect.DeepEqual(zero, GlobalMonitoringView{}) {
		t.Fatalf("nil view = %+v, %v", zero, err)
	}
}

func TestRegistryDeclaresConfiguredCoverageAndPredeclaredReporter(t *testing.T) {
	cfg := testConfig()
	cfg.SweepInterval = time.Millisecond
	store := &firstSnapshotStore{published: make(chan Snapshot, 1)}
	registry := NewRegistry(cfg, store, nil)
	registry.DeclareReportingPublisher("test-publisher#reported")
	ctx, cancel := context.WithCancel(context.Background())
	registry.Start(ctx)
	var first Snapshot
	select {
	case first = <-store.published:
	case <-time.After(time.Second):
		t.Fatal("registry did not publish its first snapshot")
	}
	cancel()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err := registry.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	if !first.Coverage.Declared || !reflect.DeepEqual(first.Coverage.ConfiguredFamilies, AllFamilies) {
		t.Fatalf("default coverage = %+v", first.Coverage)
	}
	if first.ReportingPublisherID != "test-publisher#reported" {
		t.Fatalf("first snapshot reporter = %q", first.ReportingPublisherID)
	}

	cfg.Families = map[Family]bool{FamilyProxy: true}
	narrowed := NewRegistry(cfg, NewLocalStore(), nil).SnapshotAt(time.Unix(1, 0))
	if !narrowed.Coverage.Declared || !reflect.DeepEqual(narrowed.Coverage.ConfiguredFamilies, []Family{FamilyProxy}) {
		t.Fatalf("narrowed coverage = %+v", narrowed.Coverage)
	}
}

// The single-process, Redis-less deployment is the default one now that
// telemetry ships on: observed traffic has to reach a complete merged view
// through LocalStore alone, with no publisher marked missing or stale. If this
// breaks, every household running one container gets a degraded parity read.
func TestLocalOnlyViewIsCompleteForASinglePublisher(t *testing.T) {
	cfg := testConfig()
	cfg.Retention = time.Minute
	store := NewLocalStore()
	registry := NewRegistry(cfg, store, slog.New(slog.DiscardHandler))
	handler := registry.Observe(testRoute(ClassPlayback))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Attach(r.Context(), testAttachment("session-1"))
		_, _ = w.Write([]byte("payload"))
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/media/x", nil))

	at := time.Now()
	originalNow := now
	now = func() time.Time { return at }
	defer func() { now = originalNow }()
	snapshot := registry.Sweep()
	if err := store.Publish(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	view, err := registry.GlobalView(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !view.Complete || len(view.IncompleteReasons) != 0 {
		t.Fatalf("local-only view degraded: complete=%v reasons=%v", view.Complete, view.IncompleteReasons)
	}
	if len(view.Publishers) != 1 || len(view.Sessions) != 1 {
		t.Fatalf("view = %d publishers, %d sessions", len(view.Publishers), len(view.Sessions))
	}
	if session := view.Sessions[0]; session.SessionID != "session-1" || session.ViewerBytesAccepted != 7 {
		t.Fatalf("session = %+v", session)
	}
}

func TestLocalStoreDeepCopies(t *testing.T) {
	store := NewLocalStore()
	source := Snapshot{Sessions: []SessionView{{ViewerIPs: []string{"one"}, Routes: []RouteActivityView{{Pattern: "/one"}}, Outcomes: map[httpstream.StreamOutcome]int64{"completed": 1}}}}
	if err := store.Publish(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	source.Sessions[0].ViewerIPs[0] = "mutated-source"
	loaded, _ := store.Load(context.Background())
	loaded.Sessions[0].ViewerIPs[0] = "mutated-load"
	loaded.Sessions[0].Routes[0].Pattern = "/mutated"
	loadedAgain, _ := store.Load(context.Background())
	if loadedAgain.Sessions[0].ViewerIPs[0] != "one" || loadedAgain.Sessions[0].Routes[0].Pattern != "/one" {
		t.Fatalf("store returned aliased snapshot: %+v", loadedAgain)
	}
}

// Truncated states current blindness — BuildGlobalView pins Complete=false while
// a publisher reports it — so one transient drop must not mark a process
// degraded for the rest of its life.
func TestTruncatedDecaysAfterFreshness(t *testing.T) {
	cfg := testConfig()
	cfg.MaxObservations = 0 // force the very first observation to be dropped
	registry := NewRegistry(cfg, NewLocalStore(), slog.New(slog.DiscardHandler))
	handler := registry.Observe(testRoute(ClassPlayback))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Attach(r.Context(), testAttachment("session-1"))
		_, _ = w.Write([]byte("payload"))
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/media/x", nil))

	dropAt := time.Now()
	if !registry.SnapshotAt(dropAt).Truncated {
		t.Fatal("snapshot taken at the drop is not truncated")
	}
	if snapshot := registry.SnapshotAt(dropAt.Add(cfg.Freshness / 2)); !snapshot.Truncated {
		t.Fatal("snapshot inside the freshness window is not truncated")
	}
	later := registry.SnapshotAt(dropAt.Add(cfg.Freshness + time.Second))
	if later.Truncated {
		t.Fatal("Truncated is still set an entire freshness window after the drop")
	}
	// The permanent record stays monotonic.
	if later.DroppedObservations == 0 {
		t.Fatalf("dropped observations = %d, want the drop to still be counted", later.DroppedObservations)
	}
}

// Clients open the realtime control socket as soon as they have a sessionId,
// which is before they request any media route. State that arrives then has to
// survive until the session exists, or every live session reports a dead socket.
func TestRealtimeConnectionSetBeforeAttachIsApplied(t *testing.T) {
	registry := NewRegistry(testConfig(), NewLocalStore(), slog.New(slog.DiscardHandler))
	registry.SetRealtimeConnection("session-1", true)

	handler := registry.Observe(testRoute(ClassPlayback))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Attach(r.Context(), testAttachment("session-1"))
		_, _ = w.Write([]byte("payload"))
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/media/x", nil))

	snapshot := registry.SnapshotAt(time.Now())
	if len(snapshot.Sessions) != 1 {
		t.Fatalf("sessions = %+v", snapshot.Sessions)
	}
	if !snapshot.Sessions[0].RealtimeConnectionAlive {
		t.Fatal("realtime state set before the first media route was dropped")
	}
}

// Held state must not outlive the sessions it waits for.
func TestPendingRealtimeStateIsBounded(t *testing.T) {
	cfg := testConfig()
	cfg.MaxSessions = 0
	registry := NewRegistry(cfg, NewLocalStore(), slog.New(slog.DiscardHandler))
	registry.SetRealtimeConnection("session-1", true)

	shard := registry.shard("session-1")
	shard.RLock()
	held := len(shard.pendingRealtime)
	shard.RUnlock()
	if held != 0 {
		t.Fatalf("pending realtime entries = %d, want the capacity bound to refuse it", held)
	}
}

// A socket that opens and closes without the client ever requesting media leaves
// state behind; the sweep has to reclaim it.
func TestPendingRealtimeStateIsPrunedBySweep(t *testing.T) {
	registry := NewRegistry(testConfig(), NewLocalStore(), slog.New(slog.DiscardHandler))
	registry.SetRealtimeConnection("orphan-session", true)

	shard := registry.shard("orphan-session")
	shard.RLock()
	held := len(shard.pendingRealtime)
	shard.RUnlock()
	if held != 1 {
		t.Fatalf("pending realtime entries = %d, want 1", held)
	}

	// testConfig sets Retention to 1ms, so one sweep past it collects the entry.
	registry.sweep(time.Now().Add(time.Second))
	shard.RLock()
	held = len(shard.pendingRealtime)
	shard.RUnlock()
	if held != 0 {
		t.Fatalf("pending realtime entries after sweep = %d, want 0", held)
	}
}

// Ranged byte routes issue many small GETs for one file. A record per request
// would exhaust MaxTransfers inside a retention window and leave RequestCount —
// which exists to count exactly this — pinned at 1.
func TestRangedTransferRequestsFoldIntoOneRecord(t *testing.T) {
	cfg := testConfig()
	cfg.Retention = time.Hour // keep the record alive across the sweep below
	registry := NewRegistry(cfg, NewLocalStore(), slog.New(slog.DiscardHandler))
	handler := registry.Observe(testRoute(ClassTransfer))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Attach(r.Context(), testAttachment(""))
		_, _ = w.Write([]byte("chunk"))
	}))
	const requests = 25
	for range requests {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/media/x", nil))
	}

	snapshot := registry.Sweep()
	if len(snapshot.Transfers) != 1 {
		t.Fatalf("transfers = %d, want 1 folded record", len(snapshot.Transfers))
	}
	transfer := snapshot.Transfers[0]
	if transfer.RequestCount != requests {
		t.Fatalf("request count = %d, want %d", transfer.RequestCount, requests)
	}
	if transfer.BytesAccepted != requests*int64(len("chunk")) {
		t.Fatalf("bytes = %d, want %d", transfer.BytesAccepted, requests*int64(len("chunk")))
	}
	if registry.transferReservations.Load() != 1 {
		t.Fatalf("reservations = %d, want 1", registry.transferReservations.Load())
	}
}

// A different file, subject or route is a different pour.
func TestTransfersSeparateByFileAndSubject(t *testing.T) {
	cfg := testConfig()
	cfg.Retention = time.Hour
	registry := NewRegistry(cfg, NewLocalStore(), slog.New(slog.DiscardHandler))
	serve := func(attachment Attachment) {
		handler := registry.Observe(testRoute(ClassTransfer))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			Attach(r.Context(), attachment)
			_, _ = w.Write([]byte("chunk"))
		}))
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/media/x", nil))
	}
	base := testAttachment("")
	serve(base)
	otherFile := base
	otherFile.MediaFileID = 43
	serve(otherFile)
	otherUser := base
	otherUser.Subject = UserSubject(8)
	serve(otherUser)

	if snapshot := registry.Sweep(); len(snapshot.Transfers) != 3 {
		t.Fatalf("transfers = %d, want 3 distinct pours", len(snapshot.Transfers))
	}
}

// Two viewers are two transfers, and the shared table stays globally bounded.
// Each viewer here is a distinct principal on purpose: the per-subject budget is
// clamped to an eighth of the table (config.resolve), so a MaxTransfers small
// enough to exercise the GLOBAL cap can only be reached by several principals —
// which is exactly what that clamp guarantees. Same-principal viewer
// distinctness is covered by TestOneSubjectCannotConsumeAnotherSubjectsTransferBudget.
func TestTransfersSeparateViewersAndRemainGloballyBounded(t *testing.T) {
	cfg := testConfig()
	cfg.Retention = time.Hour
	cfg.MaxTransfers = 2
	registry := NewRegistry(cfg, NewLocalStore(), slog.New(slog.DiscardHandler))
	handler := registry.Observe(testTransferRoute())(transferAttachHandler("chunk"))
	serve := func(subject Subject, viewer, device string) {
		attachment := testAttachment("")
		attachment.Subject = subject
		serveTransferAs(handler, attachment, viewer, device)
	}

	serve(UserSubject(7), "192.0.2.1", "living-room")
	serve(UserSubject(8), "192.0.2.2", "phone")
	serve(UserSubject(7), "192.0.2.1", "living-room") // folds into the first viewer's transfer
	serve(UserSubject(9), "192.0.2.3", "tablet")      // exceeds the global transfer cap

	snapshot := registry.Sweep()
	if len(snapshot.Transfers) != 2 {
		t.Fatalf("transfers = %+v, want two viewer-scoped records", snapshot.Transfers)
	}
	counts := make(map[string]int64, len(snapshot.Transfers))
	for _, transfer := range snapshot.Transfers {
		counts[transfer.ViewerIP+"\x00"+transfer.DeviceID] = transfer.RequestCount
	}
	if counts["192.0.2.1\x00living-room"] != 2 || counts["192.0.2.2\x00phone"] != 1 {
		t.Fatalf("viewer transfer counts = %+v", counts)
	}
	// The GLOBAL table filled, which is blindness about downloads and says
	// nothing about whether the session picture is complete. It must therefore
	// never reach Truncated: that flag becomes publisher_truncated, which makes
	// the merged view incomplete, which switches ghost classification off for the
	// whole fleet.
	if snapshot.Truncated || snapshot.DroppedObservations != 0 {
		t.Fatalf("transfer exhaustion leaked into the session picture: truncated=%v dropped=%d",
			snapshot.Truncated, snapshot.DroppedObservations)
	}
	if !snapshot.TransfersTruncated || snapshot.DroppedTransferObservations != 1 {
		t.Fatalf("transfer cap state: truncated=%v dropped=%d",
			snapshot.TransfersTruncated, snapshot.DroppedTransferObservations)
	}
}

// HasIdentityConflict and IdentityConflicts must agree. A started-at authority
// upgrade that confirms the recorded instant is not a conflict at all and must
// not consume the per-session budget; one that moves it is, and sets the flag.
func TestStartedAtAuthorityUpgradeAndConflictAgree(t *testing.T) {
	at := time.Unix(100, 0)

	t.Run("pure upgrade records nothing", func(t *testing.T) {
		session := newLogicalSession(Attachment{Subject: UserSubject(7), SessionID: "s",
			StartedAt: at, StartedAtSource: StartedAtSourceFirstSeen}, testConfig(), at)
		session.recordConflicts(Attachment{Subject: UserSubject(7), SessionID: "s",
			StartedAt: at, StartedAtSource: StartedAtSourceClaim}, at, 16)
		if session.hasIdentityConflict || len(session.identityConflicts) != 0 {
			t.Fatalf("benign upgrade recorded a conflict: %+v", session.identityConflicts)
		}
		if session.startedAtSource != StartedAtSourceClaim {
			t.Fatalf("authority did not upgrade: %v", session.startedAtSource)
		}
	})

	t.Run("moved value sets the flag and the list", func(t *testing.T) {
		session := newLogicalSession(Attachment{Subject: UserSubject(7), SessionID: "s",
			StartedAt: at, StartedAtSource: StartedAtSourceFirstSeen}, testConfig(), at)
		moved := at.Add(-90 * time.Second)
		session.recordConflicts(Attachment{Subject: UserSubject(7), SessionID: "s",
			StartedAt: moved, StartedAtSource: StartedAtSourceClaim}, at, 16)
		if !session.hasIdentityConflict {
			t.Fatal("started_at was replaced but HasIdentityConflict is false")
		}
		if len(session.identityConflicts) != 1 || session.identityConflicts[0].Field != "started_at_replaced" {
			t.Fatalf("conflicts = %+v", session.identityConflicts)
		}
		if !session.startedAt.Equal(moved) {
			t.Fatalf("started at = %v, want %v", session.startedAt, moved)
		}
	})
}

// The transfer id becomes a Redis hash field name, so it must not leak the
// identity it is derived from. A viewer's IP address and their client-supplied
// device id belong in the record's fields, where reading them is a deliberate
// act, not in a key name that SCAN, MONITOR, slowlog and an RDB dump all expose.
// The id must also stay ASCII-safe: the pre-hash id joined its components with
// NUL, which silently truncates every line-oriented reader of the keyspace.
func TestTransferIDHidesViewerIdentity(t *testing.T) {
	cfg := testConfig()
	cfg.Retention = time.Hour
	route := testRoute(ClassTransfer)
	route.Capture = func(r *http.Request) CaptureSet {
		return CaptureSet{Method: r.Method, Pattern: route.Pattern,
			ViewerIP: "198.51.100.77", DeviceID: "kitchen-tv-8842"}
	}
	registry := NewRegistry(cfg, NewLocalStore(), slog.New(slog.DiscardHandler))
	handler := registry.Observe(route)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Attach(r.Context(), testAttachment(""))
		_, _ = w.Write([]byte("chunk"))
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/media/x", nil))

	snapshot := registry.Sweep()
	if len(snapshot.Transfers) != 1 {
		t.Fatalf("transfers = %d, want 1", len(snapshot.Transfers))
	}
	transfer := snapshot.Transfers[0]
	for _, secret := range []string{"198.51.100.77", "kitchen-tv-8842", route.Pattern, "\x00"} {
		if strings.Contains(transfer.ID, secret) {
			t.Fatalf("transfer id %q leaks %q", transfer.ID, secret)
		}
	}
	if transfer.ViewerIP != "198.51.100.77" || transfer.DeviceID != "kitchen-tv-8842" {
		t.Fatalf("identity must survive as fields: ip=%q device=%q", transfer.ViewerIP, transfer.DeviceID)
	}
}

// Hashing the id must not fold two viewers together, and must be stable across
// processes — a runtime-seeded hash would give two publishers different ids for
// the same logical transfer, which the merge is entitled to assume it can trust.
func TestTransferKeyIsStableAndViewerDistinct(t *testing.T) {
	attachment := testAttachment("")
	base := CaptureSet{Method: http.MethodGet, Pattern: "/media/{id}", ViewerIP: "203.0.113.5", DeviceID: "den"}
	other := base
	other.ViewerIP = "203.0.113.6"

	first, again, elsewhere := transferKey(attachment, base), transferKey(attachment, base), transferKey(attachment, other)
	if first != again {
		t.Fatalf("transfer key is not deterministic: %q then %q", first, again)
	}
	if first == elsewhere {
		t.Fatalf("two viewers folded into one transfer key: %q", first)
	}
	if len(first) != 32 {
		t.Fatalf("transfer key = %q, want 32 hex characters", first)
	}

	// The per-subject catch-all row shares the transfer keyspace, so its id has to
	// be just as deterministic, just as opaque, and unable to collide with any
	// ordinary identity for the same principal.
	subject, otherSubject := attachment.Subject, UserSubject(8)
	fold, foldAgain := overflowTransferKey(subject), overflowTransferKey(subject)
	if fold != foldAgain {
		t.Fatalf("overflow transfer key is not deterministic: %q then %q", fold, foldAgain)
	}
	if len(fold) != 32 {
		t.Fatalf("overflow transfer key = %q, want 32 hex characters", fold)
	}
	if fold == overflowTransferKey(otherSubject) {
		t.Fatalf("two subjects share one overflow transfer key: %q", fold)
	}
	for _, capture := range []CaptureSet{base, other, {}, {Method: http.MethodHead}} {
		if ordinary := transferKey(attachment, capture); ordinary == fold {
			t.Fatalf("overflow key collides with an ordinary transfer key: %q", fold)
		}
	}
}

// serveTransferAs issues one transfer-class request carrying the given identity.
// The route Capture in these tests reads the viewer and device from headers, so
// this is how a test mints a distinct transfer key.
func serveTransferAs(handler http.Handler, attachment Attachment, viewer, device string) {
	request := httptest.NewRequest(http.MethodGet, "/media/x", nil)
	request.Header.Set("X-Test-Viewer", viewer)
	request.Header.Set("X-Test-Device", device)
	handler.ServeHTTP(httptest.NewRecorder(), request.WithContext(withTestAttachment(request, attachment)))
}

// transferAttachHandler attaches whatever identity serveTransferAs put on the
// request and writes payload, so one handler serves many principals.
func transferAttachHandler(payload string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if attachment, ok := r.Context().Value(testAttachmentKey{}).(Attachment); ok {
			Attach(r.Context(), attachment)
		} else {
			Attach(r.Context(), testAttachment(""))
		}
		_, _ = w.Write([]byte(payload))
	}
}

type testAttachmentKey struct{}

func withTestAttachment(r *http.Request, attachment Attachment) context.Context {
	return context.WithValue(r.Context(), testAttachmentKey{}, attachment)
}

// testTransferRoute is a transfer-class route whose capture reads the viewer
// address and device id from request headers, so a test can mint distinct
// transfer identities the way a real client's device-id churn does.
func testTransferRoute() MediaRoute {
	route := testRoute(ClassTransfer)
	route.Capture = func(r *http.Request) CaptureSet {
		return CaptureSet{Method: r.Method, Pattern: "/media/{id}",
			ViewerIP: r.Header.Get("X-Test-Viewer"), DeviceID: r.Header.Get("X-Test-Device")}
	}
	return route
}

// The whole point of splitting the drop bookkeeping: a full transfer table is a
// download-attribution problem, and a transfer key is minted partly from
// client-supplied device ids, so it must never make the merged view incomplete —
// that would clear no_delivery and unclaimed_idle on every row for every reader.
// A genuine session-picture drop still does.
func TestTransferCapDoesNotMakeTheViewIncomplete(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	originalNow := now
	now = func() time.Time { return at }
	defer func() { now = originalNow }()

	t.Run("transfer exhaustion is an advisory", func(t *testing.T) {
		cfg := testConfig()
		cfg.Retention = time.Hour
		cfg.MaxTransfers = 1
		store := NewLocalStore()
		registry := NewRegistry(cfg, store, slog.New(slog.DiscardHandler))
		handler := registry.Observe(testTransferRoute())(transferAttachHandler("chunk"))
		for _, subject := range []Subject{UserSubject(7), UserSubject(8)} {
			attachment := testAttachment("")
			attachment.Subject = subject
			serveTransferAs(handler, attachment, "192.0.2.1", "device")
		}
		snapshot := registry.Sweep()
		if !snapshot.TransfersTruncated || snapshot.Truncated {
			t.Fatalf("snapshot flags: transfers=%v sessions=%v", snapshot.TransfersTruncated, snapshot.Truncated)
		}
		if err := store.Publish(context.Background(), snapshot); err != nil {
			t.Fatal(err)
		}
		view, err := registry.GlobalView(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !view.Complete || len(view.IncompleteReasons) != 0 {
			t.Fatalf("transfer exhaustion made the view incomplete: complete=%v reasons=%v",
				view.Complete, view.IncompleteReasons)
		}
		if !slices.Contains(view.Advisories, "publisher_transfer_capacity") {
			t.Fatalf("advisories = %v, want publisher_transfer_capacity", view.Advisories)
		}
		if !view.TransfersTruncated || view.DroppedTransferObservations != 1 {
			t.Fatalf("merged transfer counters: truncated=%v dropped=%d",
				view.TransfersTruncated, view.DroppedTransferObservations)
		}
	})

	t.Run("session exhaustion still makes it incomplete", func(t *testing.T) {
		cfg := testConfig()
		cfg.Retention = time.Hour
		cfg.MaxSessions = 0
		store := NewLocalStore()
		registry := NewRegistry(cfg, store, slog.New(slog.DiscardHandler))
		handler := registry.Observe(testRoute(ClassPlayback))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			Attach(r.Context(), testAttachment("session-1"))
			_, _ = w.Write([]byte("chunk"))
		}))
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/media/x", nil))
		snapshot := registry.Sweep()
		if !snapshot.Truncated {
			t.Fatal("session capacity exhaustion did not set Truncated")
		}
		if err := store.Publish(context.Background(), snapshot); err != nil {
			t.Fatal(err)
		}
		view, err := registry.GlobalView(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if view.Complete || !slices.Contains(view.IncompleteReasons, "publisher_truncated") {
			t.Fatalf("session truncation did not degrade the view: complete=%v reasons=%v",
				view.Complete, view.IncompleteReasons)
		}
	})
}

// TransfersTruncated states CURRENT transfer-table blindness, so it decays on the
// same horizon Truncated does — and the same event must never set Truncated.
func TestTransfersTruncatedDecaysAfterFreshness(t *testing.T) {
	cfg := testConfig()
	cfg.MaxTransfers = 0 // force the very first transfer observation to be dropped
	registry := NewRegistry(cfg, NewLocalStore(), slog.New(slog.DiscardHandler))
	handler := registry.Observe(testTransferRoute())(transferAttachHandler("payload"))
	serveTransferAs(handler, testAttachment(""), "192.0.2.1", "device")

	dropAt := time.Now()
	atDrop := registry.SnapshotAt(dropAt)
	if !atDrop.TransfersTruncated {
		t.Fatal("snapshot taken at the drop is not transfer-truncated")
	}
	inside := registry.SnapshotAt(dropAt.Add(cfg.Freshness / 2))
	if !inside.TransfersTruncated {
		t.Fatal("snapshot inside the freshness window is not transfer-truncated")
	}
	later := registry.SnapshotAt(dropAt.Add(cfg.Freshness + time.Second))
	if later.TransfersTruncated {
		t.Fatal("TransfersTruncated is still set an entire freshness window after the drop")
	}
	// The permanent record stays monotonic.
	if later.DroppedTransferObservations == 0 {
		t.Fatalf("dropped transfer observations = %d, want the drop to still be counted",
			later.DroppedTransferObservations)
	}
	for _, snapshot := range []Snapshot{atDrop, inside, later} {
		if snapshot.Truncated || snapshot.DroppedObservations != 0 {
			t.Fatalf("a transfer drop leaked into the session picture: truncated=%v dropped=%d",
				snapshot.Truncated, snapshot.DroppedObservations)
		}
	}
}

// The attack this budget exists to stop: one authenticated client rotating device
// ids used to be able to mint an unbounded share of the shared transfer table and
// take every other principal's attribution with it.
func TestOneSubjectCannotConsumeAnotherSubjectsTransferBudget(t *testing.T) {
	cfg := testConfig()
	cfg.Retention = time.Hour
	cfg.MaxTransfersPerSubject = 2
	registry := NewRegistry(cfg, NewLocalStore(), slog.New(slog.DiscardHandler))
	handler := registry.Observe(testTransferRoute())(transferAttachHandler("chunk"))
	serve := func(subject Subject, device string) {
		attachment := testAttachment("")
		attachment.Subject = subject
		serveTransferAs(handler, attachment, "192.0.2.1", device)
	}

	noisy, quiet := UserSubject(7), UserSubject(8)
	for i := range 20 {
		serve(noisy, fmt.Sprintf("churned-device-%d", i))
	}
	for i := range 3 {
		serve(quiet, fmt.Sprintf("household-device-%d", i))
	}

	snapshot := registry.Sweep()
	bySubject := make(map[string][]TransferView)
	for _, transfer := range snapshot.Transfers {
		bySubject[transfer.Subject.ID] = append(bySubject[transfer.Subject.ID], transfer)
	}
	for name, subject := range map[string]Subject{"noisy": noisy, "quiet": quiet} {
		records := bySubject[subject.ID]
		if len(records) != 3 {
			t.Fatalf("%s subject holds %d records, want 2 ordinary plus one fold: %+v", name, len(records), records)
		}
		folds := 0
		for _, record := range records {
			if record.Overflowed {
				folds++
				continue
			}
			if record.DeviceID == "" {
				t.Fatalf("%s subject's ordinary record has no device id: %+v", name, record)
			}
		}
		if folds != 1 {
			t.Fatalf("%s subject has %d fold rows, want exactly 1: %+v", name, folds, records)
		}
	}
	if snapshot.Truncated || snapshot.TransfersTruncated {
		t.Fatalf("folding is not blindness: truncated=%v transfers=%v", snapshot.Truncated, snapshot.TransfersTruncated)
	}
}

// Overflow is degradation of DETAIL, not loss. The bytes stay attributed to the
// principal, and the fold row asserts nothing it cannot know: a plausible wrong
// identity is worse for an operator than an absent one.
func TestPerSubjectOverflowFoldsInsteadOfLosingBytes(t *testing.T) {
	const payload = "chunk"
	const devices = 12
	cfg := testConfig()
	cfg.Retention = time.Hour
	cfg.MaxTransfersPerSubject = 1
	registry := NewRegistry(cfg, NewLocalStore(), slog.New(slog.DiscardHandler))
	handler := registry.Observe(testTransferRoute())(transferAttachHandler(payload))
	for i := range devices {
		serveTransferAs(handler, testAttachment(""), "192.0.2.1", fmt.Sprintf("device-%d", i))
	}

	snapshot := registry.Sweep()
	total, folds := int64(0), 0
	var fold TransferView
	for _, transfer := range snapshot.Transfers {
		total += transfer.BytesAccepted
		if transfer.Overflowed {
			folds++
			fold = transfer
		}
	}
	if want := int64(devices * len(payload)); total != want {
		t.Fatalf("bytes across the subject's transfers = %d, want %d", total, want)
	}
	if snapshot.DroppedObservations != 0 || snapshot.DroppedTransferObservations != 0 {
		t.Fatalf("folding dropped observations: sessions=%d transfers=%d",
			snapshot.DroppedObservations, snapshot.DroppedTransferObservations)
	}
	if folds != 1 {
		t.Fatalf("fold rows = %d, want exactly 1: %+v", folds, snapshot.Transfers)
	}
	if fold.Subject != UserSubject(7) {
		t.Fatalf("fold row lost the one identity it may keep: %+v", fold.Subject)
	}
	if fold.ViewerIP != "" || fold.DeviceID != "" || fold.Client != (ClientVariant{}) || fold.UserAgent != "" ||
		fold.ProfileID != "" || fold.MediaFileID != 0 || fold.Method != "" || fold.Pattern != "" || fold.Role != "" {
		t.Fatalf("fold row asserts an identity it cannot know: %+v", fold)
	}
	if fold.RequestCount != devices-1 {
		t.Fatalf("fold row request count = %d, want %d", fold.RequestCount, devices-1)
	}
}

// sweep() is the ONLY release path for the per-subject allowance. A leak here is
// a permanent lockout: the principal folds forever even after its records prune.
func TestTransferBudgetIsReleasedOnPrune(t *testing.T) {
	newClock := func(t *testing.T, start time.Time) *time.Time {
		t.Helper()
		clock := start
		originalNow := now
		now = func() time.Time { return clock }
		t.Cleanup(func() { now = originalNow })
		return &clock
	}

	t.Run("full prune returns the whole allowance", func(t *testing.T) {
		start := time.Unix(1_700_000_000, 0)
		clock := newClock(t, start)
		cfg := testConfig()
		cfg.Retention = time.Minute
		cfg.MaxTransfersPerSubject = 1
		registry := NewRegistry(cfg, NewLocalStore(), slog.New(slog.DiscardHandler))
		handler := registry.Observe(testTransferRoute())(transferAttachHandler("chunk"))

		serveTransferAs(handler, testAttachment(""), "192.0.2.1", "device-1")
		serveTransferAs(handler, testAttachment(""), "192.0.2.1", "device-2") // folds
		*clock = start.Add(2 * time.Minute)
		if pruned := registry.Sweep(); len(pruned.Transfers) != 0 {
			t.Fatalf("records survived the retention window: %+v", pruned.Transfers)
		}
		registry.transfersMu.RLock()
		held := len(registry.transfersBySubject)
		registry.transfersMu.RUnlock()
		if held != 0 {
			t.Fatalf("per-subject counters = %d entries, want the map emptied", held)
		}

		serveTransferAs(handler, testAttachment(""), "192.0.2.1", "device-3")
		snapshot := registry.Sweep()
		if len(snapshot.Transfers) != 1 || snapshot.Transfers[0].Overflowed {
			t.Fatalf("a fresh identity did not get an ordinary record: %+v", snapshot.Transfers)
		}
	})

	// The shape that catches counting the fold row against the allowance: a
	// long-lived fold row would pin the counter at the cap after the ordinary
	// rows prune, and the subject would fold forever.
	t.Run("partial prune returns the ordinary allowance", func(t *testing.T) {
		start := time.Unix(1_700_000_000, 0)
		clock := newClock(t, start)
		cfg := testConfig()
		cfg.Retention = time.Minute
		cfg.MaxTransfersPerSubject = 1
		registry := NewRegistry(cfg, NewLocalStore(), slog.New(slog.DiscardHandler))
		handler := registry.Observe(testTransferRoute())(transferAttachHandler("chunk"))

		serveTransferAs(handler, testAttachment(""), "192.0.2.1", "device-1") // ordinary
		serveTransferAs(handler, testAttachment(""), "192.0.2.1", "device-2") // folds
		*clock = start.Add(45 * time.Second)
		serveTransferAs(handler, testAttachment(""), "192.0.2.1", "device-3") // keeps the fold row alive
		*clock = start.Add(70 * time.Second)

		snapshot := registry.Sweep()
		if len(snapshot.Transfers) != 1 || !snapshot.Transfers[0].Overflowed {
			t.Fatalf("want only the still-active fold row to survive: %+v", snapshot.Transfers)
		}
		*clock = start.Add(71 * time.Second)
		serveTransferAs(handler, testAttachment(""), "192.0.2.1", "device-4")
		after := registry.Sweep()
		ordinary := 0
		for _, transfer := range after.Transfers {
			if !transfer.Overflowed {
				ordinary++
				if transfer.DeviceID != "device-4" {
					t.Fatalf("unexpected ordinary record: %+v", transfer)
				}
			}
		}
		if ordinary != 1 {
			t.Fatalf("the released allowance was not reusable: %+v", after.Transfers)
		}
	})
}

// The per-subject budget is a sub-allocation of the shared table. An operator who
// lowers MaxTransfers below it must not hand one principal the whole table back.
func TestPerSubjectBudgetIsClampedToAShareOfTheTable(t *testing.T) {
	for _, test := range []struct {
		name                        string
		transfers, perSubject, want int64
	}{
		{"defaults do not bind", 10_000, 128, 128},
		{"lowered table clamps to an eighth", 16, 128, 2},
		{"tiny table keeps a floor of one", 4, 128, 1},
		{"an already-small budget is left alone", 10_000, 8, 8},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := DefaultConfig("node")
			cfg.MaxTransfers, cfg.MaxTransfersPerSubject = test.transfers, test.perSubject
			cfg.resolve()
			if cfg.MaxTransfersPerSubject != test.want {
				t.Fatalf("resolved per-subject budget = %d, want %d", cfg.MaxTransfersPerSubject, test.want)
			}
			// A Config built in code must resolve exactly as one parsed from the
			// environment does, or the invariant protects only half the callers.
			built := DefaultConfig("node")
			built.MaxTransfers, built.MaxTransfersPerSubject = test.transfers, test.perSubject
			registry := NewRegistry(built, NewLocalStore(), slog.New(slog.DiscardHandler))
			if got := registry.Config().MaxTransfersPerSubject; got != test.want {
				t.Fatalf("NewRegistry resolved per-subject budget = %d, want %d", got, test.want)
			}
		})
	}
}

// One client must not be able to switch ghost detection off for everybody by
// hammering a single session id. Exceeding MaxObservationsPerSession under-counts
// that ONE session's bytes; it cannot hide a session, so it must not raise
// Snapshot.Truncated — which becomes publisher_truncated, which makes the view
// incomplete, which clears no_delivery and unclaimed_idle on every row for every
// reader. The overflow still has to be recorded and operator-visible.
func TestPerSessionObservationCapDegradesOnlyThatSession(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	originalNow := now
	now = func() time.Time { return at }
	defer func() { now = originalNow }()

	cfg := testConfig()
	cfg.Retention = time.Hour
	cfg.MaxObservationsPerSession = 2
	store := NewLocalStore()
	registry := NewRegistry(cfg, store, slog.New(slog.DiscardHandler))

	// Held open, so the observation table stays full rather than draining between
	// requests — the shape a client produces with concurrent range GETs.
	release := make(chan struct{})
	var wg sync.WaitGroup
	handler := registry.Observe(testRoute(ClassPlayback))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Attach(r.Context(), testAttachment("session-1"))
		_, _ = w.Write([]byte("chunk"))
		<-release
	}))
	for range cfg.MaxObservationsPerSession + 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/media/x", nil))
		}()
	}
	// Wait for the table to be full before releasing, otherwise the cap may never
	// be reached and the test would pass without exercising anything.
	for {
		snapshot := registry.SnapshotAt(at)
		if len(snapshot.Sessions) == 1 && snapshot.Sessions[0].ObservationsOverflowed {
			break
		}
		runtime.Gosched()
	}
	close(release)
	wg.Wait()

	snapshot := registry.Sweep()
	if snapshot.Truncated {
		t.Fatal("per-session observation overflow set Snapshot.Truncated")
	}
	if len(snapshot.Sessions) != 1 || !snapshot.Sessions[0].ObservationsOverflowed {
		t.Fatalf("sessions = %+v", snapshot.Sessions)
	}
	if snapshot.DroppedObservations == 0 {
		t.Fatal("overflow was not counted")
	}
	if err := store.Publish(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	view, err := registry.GlobalView(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !view.Complete || len(view.IncompleteReasons) != 0 {
		t.Fatalf("per-session overflow made the view incomplete: complete=%v reasons=%v",
			view.Complete, view.IncompleteReasons)
	}
	if len(view.Sessions) != 1 || !view.Sessions[0].ObservationsOverflowed || !view.Sessions[0].BytesDegraded {
		t.Fatalf("merged session = %+v", view.Sessions)
	}
}
