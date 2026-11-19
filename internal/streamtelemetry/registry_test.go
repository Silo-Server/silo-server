package streamtelemetry

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

type failingStore struct {
	published     atomic.Int64
	secondPublish chan struct{}
}

func (s *failingStore) Publish(context.Context, Snapshot) error {
	if s.published.Add(1) == 2 && s.secondPublish != nil {
		close(s.secondPublish)
	}
	return errors.New("publish failed")
}
func (s *failingStore) Load(context.Context) (Snapshot, error) { return Snapshot{}, nil }

func TestStartContinuesAfterPublishError(t *testing.T) {
	cfg := testConfig()
	cfg.SweepInterval = time.Millisecond
	store := &failingStore{secondPublish: make(chan struct{})}
	registry := NewRegistry(cfg, store, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithCancel(context.Background())
	registry.Start(ctx)
	select {
	case <-store.secondPublish:
	case <-time.After(time.Second):
		t.Error("collector did not retry after publish error")
	}
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
	secondPublish  chan struct{}
}

func (s *lifecycleStore) Publish(_ context.Context, snapshot Snapshot) error {
	s.mu.Lock()
	s.published = append(s.published, snapshot)
	if len(s.published) == 2 && s.secondPublish != nil {
		close(s.secondPublish)
	}
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
	store := &lifecycleStore{secondPublish: make(chan struct{})}
	registry := NewRegistry(cfg, store, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithCancel(context.Background())
	registry.Start(ctx)
	registry.Start(ctx)
	select {
	case <-store.secondPublish:
	case <-time.After(time.Second):
		t.Error("collector did not publish two snapshots")
	}
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
