package handlers

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodehttp"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/workercontrolcore"
)

func TestRelayFailoverKeepsDurableReportTransport(t *testing.T) {
	healthy := newFakeRehomeNode(t, false)
	f := newRehomeFixture(t, []*nodepool.Node{
		pooledNode(1, unreachableNodeURL(t)),
		pooledNode(2, healthy.server.URL),
	}, "ffmpeg")
	store := playback.NewMemoryPlanStoreV3()
	if err := store.SaveAttempt(t.Context(), playback.AttemptRecordV3{
		PlaybackAttemptID: "report-failover-attempt", SessionID: f.session.ID,
		UserID: 1, ProfileID: "profile-1", EffectiveMediaFileID: 42,
		CurrentPlanID: "report-failover-plan",
		CurrentPlan:   playback.PlanV3{PlanID: "report-failover-plan"},
		FrozenRecipe: playback.ExecutableRecipeV3{
			TranscodeNodeURL: f.deadURL, TranscodeTransportID: rehomeTestTransport,
		},
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	f.handler.PlanStoreV3 = store
	if rr := f.segment("seg_00003.ts"); rr.Code != http.StatusOK || rr.Body.String() != "moved:seg_00003.ts" {
		t.Fatalf("failover status=%d body=%q", rr.Code, rr.Body.String())
	}
	live, err := f.sessions.GetSession(f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if live.TranscodeNodeURL != healthy.server.URL {
		t.Fatalf("failover did not move the live route: %+v", live)
	}
	durable, err := store.GetAttempt(context.Background(), f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if durable.FrozenRecipe.TranscodeNodeURL != live.TranscodeNodeURL || durable.FrozenRecipe.TranscodeTransportID != live.TranscodeTransportID {
		t.Fatalf("failover left stale report authority: durable=%q/%q live=%q/%q", durable.FrozenRecipe.TranscodeNodeURL, durable.FrozenRecipe.TranscodeTransportID, live.TranscodeNodeURL, live.TranscodeTransportID)
	}
}

func TestRelayFailoverLocalCandidateRequiresSettledAuthority(t *testing.T) {
	for _, outcome := range []string{"committed", "old", "replanned", "replanned-same-work", "new-local-work", "stopped"} {
		t.Run(outcome, func(t *testing.T) {
			f := newRehomeFixture(t, []*nodepool.Node{pooledNode(1, unreachableNodeURL(t))}, writePlaybackTestFFmpegSleep(t, "30"))
			base := rehomeDurableStore(t, f)
			before, err := base.GetAttempt(t.Context(), f.session.ID)
			if err != nil {
				t.Fatal(err)
			}
			store := &rehomeFaultStore{MemoryPlanStoreV3: base, fault: "unavailable"}
			f.handler.PlanStoreV3 = store
			manifest := func() *httptest.ResponseRecorder {
				rr := httptest.NewRecorder()
				f.handler.HandleGetTranscodeManifest(rr, playbackTestRequest(http.MethodGet, "/api/v2/playback/transcode/"+f.session.ID+"/master.m3u8?st="+f.token, nil, map[string]string{"session_id": f.session.ID}))
				return rr
			}
			first := manifest()
			candidate := f.handler.tm.GetTranscodeSession(f.session.ID)
			if first.Code != http.StatusServiceUnavailable || candidate == nil || !candidate.IsRunning() {
				t.Fatalf("uncertain local candidate not retained: status%d candidate%v", first.Code, candidate != nil)
			}
			blocked := manifest()
			if blocked.Code != 503 || strings.Contains(blocked.Body.String(), "#EXTM3U") {
				t.Fatalf("cached local candidate served before locked reconciliation: status%d", blocked.Code)
			}
			segment := f.segment("init.mp4")
			if segment.Code != http.StatusServiceUnavailable || segment.Body.Len() == 0 || segment.Header().Get("Retry-After") != "1" {
				t.Fatalf("pending candidate segment bypass: status%d retry%s", segment.Code, segment.Header().Get("Retry-After"))
			}
			if f.handler.tm.GetTranscodeSession(f.session.ID) != candidate || !candidate.IsRunning() {
				t.Fatal("outage discarded possibly committed local candidate")
			}
			store.readUnavailable.Store(false)
			switch outcome {
			case "old":
				base.ReplaceAttempt(t.Context(), *before)
			case "replanned":
				replacement := *before
				replacement.CurrentPlanID, replacement.CurrentPlan.PlanID, replacement.CurrentReplanRequestID = "new-plan", "new-plan", "new-replan"
				replacement.FrozenRecipe.TranscodeNodeURL, replacement.FrozenRecipe.TranscodeTransportID = "https://new-executor", "new-transport"
				base.ReplaceAttempt(t.Context(), replacement)
			case "replanned-same-work", "new-local-work":
				replacement, err := base.GetAttempt(t.Context(), f.session.ID)
				if err != nil {
					t.Fatal(err)
				}
				replacement.CurrentPlanID, replacement.CurrentPlan.PlanID, replacement.CurrentReplanRequestID = "new-plan", "new-plan", "new-replan"
				if outcome == "new-local-work" {
					replacement.FrozenRecipe.TranscodeTransportID = "new-local-transport"
					if err := f.sessions.SetTranscodeRoute(f.session.ID, playback.TranscodeRoute{TransportID: "new-local-transport"}); err != nil {
						t.Fatal(err)
					}
				}
				base.ReplaceAttempt(t.Context(), *replacement)
			case "stopped":
				if _, _, err := base.StopAttempt(t.Context(), f.session.ID, uuid.NewString(), nil); err != nil {
					t.Fatal(err)
				}
			}
			recovered := manifest()
			if outcome == "committed" {
				live, err := f.sessions.GetSession(f.session.ID)
				if err != nil {
					t.Fatal(err)
				}
				if recovered.Code != 200 || !strings.Contains(recovered.Body.String(), "#EXTM3U") || f.handler.tm.GetTranscodeSession(f.session.ID) != candidate || !candidate.IsRunning() || live.TranscodeNodeURL != "" || store.cas.Load() != 1 {
					t.Fatalf("confirmed local candidate was not adopted intact: status%d CAS%d", recovered.Code, store.cas.Load())
				}
				// Confirmed local hot paths need no further route SQL reads.
				store.readUnavailable.Store(true)
				if hot := manifest(); hot.Code != http.StatusOK || f.handler.tm.GetTranscodeSession(f.session.ID) != candidate {
					t.Fatalf("confirmed local hot path depends on route store: status%d", hot.Code)
				}
			} else if recovered.Code == 200 || strings.Contains(recovered.Body.String(), "#EXTM3U") || f.handler.tm.GetTranscodeSession(f.session.ID) != nil || candidate.IsRunning() {
				t.Fatalf("rejected local candidate survived: outcome%s status%d", outcome, recovered.Code)
			}
		})
	}
}

func TestRehomeLocalReconciliationPreservesLiveSuccessor(t *testing.T) {
	f := newRehomeFixture(t, []*nodepool.Node{pooledNode(1, unreachableNodeURL(t))}, writePlaybackTestFFmpegSleep(t, "30"))
	store := &rehomeFaultStore{MemoryPlanStoreV3: rehomeDurableStore(t, f), fault: "unavailable"}
	f.handler.PlanStoreV3 = store
	rr := httptest.NewRecorder()
	f.handler.HandleGetTranscodeManifest(rr, playbackTestRequest(http.MethodGet, "/api/v2/playback/transcode/"+f.session.ID+"/master.m3u8?st="+f.token, nil, map[string]string{"session_id": f.session.ID}))
	candidate := f.handler.tm.GetTranscodeSession(f.session.ID)
	if rr.Code != http.StatusServiceUnavailable || candidate == nil {
		t.Fatalf("uncertain candidate missing: status%d", rr.Code)
	}
	defer func() { _ = candidate.Close() }()
	opts := candidate.Opts()
	opts.OutputDir = t.TempDir()
	successor, err := playback.StartTranscode(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = successor.Close() }()
	if !f.handler.tm.SwapTranscodeSessionIf(f.session.ID, candidate, successor) {
		t.Fatal("successor could not claim runtime slot")
	}
	store.readUnavailable.Store(false)
	observed, err := f.sessions.GetSession(f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.handler.reconcileCachedRehomeV3(t.Context(), observed, candidate); err == nil {
		t.Fatal("stale reconciliation adopted a different live runtime")
	}
	if f.handler.tm.GetTranscodeSession(f.session.ID) != successor || !successor.IsRunning() {
		t.Fatal("stale reconciliation retired its live successor")
	}
}

func TestRelayFailoverToLocalKeepsDurableTuple(t *testing.T) {
	f := newRehomeFixture(t, []*nodepool.Node{pooledNode(1, unreachableNodeURL(t))}, writePlaybackTestFFmpegSleep(t, "30"))
	store := rehomeDurableStore(t, f)
	f.handler.PlanStoreV3 = store
	rr := httptest.NewRecorder()
	f.handler.HandleGetTranscodeManifest(rr, playbackTestRequest(http.MethodGet, "/api/v2/playback/transcode/"+f.session.ID+"/master.m3u8?st="+f.token, nil, map[string]string{"session_id": f.session.ID}))
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "#EXTM3U") {
		t.Fatalf("local recovery status%d body%s", rr.Code, rr.Body.String())
	}
	live, err := f.sessions.GetSession(f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	durable, err := store.GetAttempt(t.Context(), f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if live.TranscodeNodeURL != "" || durable.FrozenRecipe.TranscodeNodeURL != "" || durable.FrozenRecipe.TranscodeTransportID != live.TranscodeTransportID || live.TranscodeTransportID != rehomeTestTransport {
		t.Fatalf("local executor tuple diverged: live=%s/%s durable=%s/%s", live.TranscodeNodeURL, live.TranscodeTransportID, durable.FrozenRecipe.TranscodeNodeURL, durable.FrozenRecipe.TranscodeTransportID)
	}
}

// Owned reads remain available while the ordinary pool read fails. A move
// must use the locked current plan instead of silently dropping capabilities.
type ownedRehomePlanStore struct {
	*playback.MemoryPlanStoreV3
}

func (s *ownedRehomePlanStore) GetAttempt(context.Context, string) (*playback.AttemptRecordV3, error) {
	return nil, errors.New("ordinary plan pool unavailable")
}

type notifyingRehomeStore struct {
	*playback.MemoryPlanStoreV3
	attempted, acquired chan struct{}
}

func (s *notifyingRehomeStore) AcquireRouteSession(ctx context.Context, id string) (playback.RouteSessionV3, error) {
	s.attempted <- struct{}{}
	owned, err := s.MemoryPlanStoreV3.AcquireRouteSession(ctx, id)
	if err == nil {
		s.acquired <- struct{}{}
	}
	return owned, err
}

func TestRelayFailoverSerializesHandlersSharingMemoryStore(t *testing.T) {
	healthy := newFakeRehomeNode(t, true)
	var releaseOnce sync.Once
	releaseStart := func() { releaseOnce.Do(func() { close(healthy.release) }) }
	t.Cleanup(releaseStart)
	f := newRehomeFixture(t, []*nodepool.Node{pooledNode(1, unreachableNodeURL(t)), pooledNode(2, healthy.server.URL)}, "ffmpeg")
	store := &notifyingRehomeStore{MemoryPlanStoreV3: rehomeDurableStore(t, f), attempted: make(chan struct{}, 2), acquired: make(chan struct{}, 2)}
	f.handler.PlanStoreV3 = store
	second := NewPlaybackHandler(f.sessions)
	second.JWTSecret, second.NodePlanner, second.PlaybackConfig, second.PlanStoreV3 = f.handler.JWTSecret, f.planner, f.handler.PlaybackConfig, store
	f2 := *f
	f2.handler = second
	results := make(chan *httptest.ResponseRecorder, 2)
	go func() { results <- f.segment("seg_00003.ts") }()
	select {
	case <-store.attempted:
	case <-time.After(time.Second):
		t.Fatal("first owner never entered")
	}
	select {
	case <-store.acquired:
	case <-time.After(time.Second):
		t.Fatal("first owner never acquired")
	}
	deadline := time.Now().Add(time.Second)
	for healthy.starts.Load() == 0 {
		if time.Now().After(deadline) {
			releaseStart()
			t.Fatal("first POST never entered")
		}
		time.Sleep(time.Millisecond)
	}
	go func() { results <- f2.segment("seg_00004.ts") }()
	select {
	case <-store.attempted:
	case <-time.After(time.Second):
		releaseStart()
		t.Fatal("second handler never reached store boundary")
	}
	select {
	case <-store.acquired:
		releaseStart()
		t.Fatal("distinct handler bypassed shared store owner")
	case <-time.After(30 * time.Millisecond):
	}
	releaseStart()
	for range 2 {
		select {
		case rr := <-results:
			if rr.Code != 200 {
				t.Fatalf("joined recovery status%d body%s", rr.Code, rr.Body.String())
			}
		case <-time.After(3 * time.Second):
			t.Fatal("joined recovery did not complete")
		}
	}
	if healthy.starts.Load() != 1 {
		t.Fatalf("distinct handlers started%d producers", healthy.starts.Load())
	}
}

func TestRehomeRouteWaitsHonorOriginalCancellation(t *testing.T) {
	f := newRehomeFixture(t, []*nodepool.Node{pooledNode(1, unreachableNodeURL(t))}, "ffmpeg")
	store := rehomeDurableStore(t, f)
	f.handler.PlanStoreV3 = store
	for _, boundary := range []string{"handler", "store", "slot"} {
		t.Run(boundary, func(t *testing.T) {
			var release func()
			var err error
			switch boundary {
			case "handler":
				release, err = f.handler.lockReplanV3(t.Context(), f.session.ID)
			case "store":
				release, err = store.AcquireSessionLock(t.Context(), f.session.ID)
			case "slot":
				var slots []func()
				for range maxConcurrentReplansV3 {
					held, e := f.handler.acquireReplanSlotV3(t.Context())
					if e != nil {
						t.Fatal(e)
					}
					slots = append(slots, held)
				}
				release = func() {
					for _, held := range slots {
						held()
					}
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
			defer cancel()
			_, cleanup, err := f.handler.acquireRehomeRouteV3(ctx, f.session.ID)
			if cleanup != nil {
				cleanup()
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("%s wait ignored cancellation: %v", boundary, err)
			}
		})
	}
}

func TestRelayFailoverRequiresFreshEnrolledTransport(t *testing.T) {
	for _, scenario := range []string{"enrolled", "classification-unknown"} {
		t.Run(scenario, func(t *testing.T) {
			var posts atomic.Int32
			worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					posts.Add(1)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(`{"session_id":"rehome-transport","status":"started"}`))
			}))
			defer worker.Close()
			f := newRehomeFixture(t, []*nodepool.Node{pooledNode(1, unreachableNodeURL(t)), pooledNode(17, worker.URL)}, "ffmpeg")
			cfg := f.handler.playbackConfig()
			cfg.Routing.VideoTranscodeExecution = config.PlaybackExecutionWorkerOnly
			f.handler.PlaybackConfig = func() config.PlaybackConfig { return cfg }
			base := rehomeDurableStore(t, f)
			f.handler.PlanStoreV3 = base
			card, _ := verifiedStreamCardFromToken(f.token, f.session.ID, f.handler.JWTSecret)
			if card == nil {
				t.Fatal("fixture stream recipe unavailable")
			}
			recipes := newSharedRecipeStore(*card)
			f.handler.NodeRecipeStore = recipes
			public, private, _ := ed25519.GenerateKey(rand.Reader)
			assignments := workercontrolcore.NewMemoryStore()
			service, err := workercontrolcore.New(workercontrolcore.Options{Origin: "https://core.example", PrivateKey: private, Nodes: standaloneRecipeNodes{workercontrolcore.Node{ID: 17, URL: worker.URL, Enabled: true, AllowHTTP: true, PublicKey: public}}, Store: assignments, Sources: standaloneRecipeSources{}})
			if err != nil {
				t.Fatal(err)
			}
			nodehttp.Configure(service.WrapTransport, func(ctx context.Context, origin string) (bool, error) {
				if scenario == "classification-unknown" && origin == worker.URL {
					return false, errors.New("registry unavailable")
				}
				return service.RequiresRecipe(ctx, origin)
			})
			defer nodehttp.Configure(nil)
			rr := f.segment("seg_00003.ts")
			live, err := f.sessions.GetSession(f.session.ID)
			if err != nil {
				t.Fatal(err)
			}
			durable, err := base.GetAttempt(t.Context(), f.session.ID)
			if err != nil {
				t.Fatal(err)
			}
			after, found := recipes.Get(t.Context(), rehomeTestTransport)
			beforeJSON, _ := json.Marshal(card)
			afterJSON, _ := json.Marshal(after)
			wantStatus := 502
			if scenario == "classification-unknown" {
				wantStatus = 503
			}
			if rr.Code != wantStatus || posts.Load() != 0 || live.TranscodeNodeURL != f.deadURL || durable.FrozenRecipe.TranscodeNodeURL != f.deadURL || !found || string(beforeJSON) != string(afterJSON) {
				t.Fatalf("unsafe enrolled rehome: status%d externalPOST%d live=%s durable=%s recipePreserved=%v", rr.Code, posts.Load(), live.TranscodeNodeURL, durable.FrozenRecipe.TranscodeNodeURL, found && string(beforeJSON) == string(afterJSON))
			}
			pending, err := assignments.Pending(t.Context(), 17)
			if err != nil || len(pending) != 0 {
				t.Fatalf("refused rehome admitted signed worker work: %v", err)
			}
		})
	}
}

type rehomeFaultStore struct {
	*playback.MemoryPlanStoreV3
	fault           string
	cas             atomic.Int32
	readUnavailable atomic.Bool
}

type rehomeFaultSession struct {
	playback.RouteSessionV3
	store *rehomeFaultStore
}

func (s *rehomeFaultStore) AcquireRouteSession(ctx context.Context, id string) (playback.RouteSessionV3, error) {
	owned, err := s.MemoryPlanStoreV3.AcquireRouteSession(ctx, id)
	if err != nil {
		return nil, err
	}
	return &rehomeFaultSession{RouteSessionV3: owned, store: s}, nil
}

func (s *rehomeFaultSession) GetAttempt(ctx context.Context) (*playback.AttemptRecordV3, error) {
	if s.store.readUnavailable.Load() {
		return nil, errors.New("injected reconciliation outage")
	}
	return s.RouteSessionV3.GetAttempt(ctx)
}

func (s *rehomeFaultSession) CompareAndSwap(ctx context.Context, expected playback.AttemptRecordV3, next playback.TranscodeRoute) (bool, error) {
	s.store.cas.Add(1)
	if s.store.fault == "rejected" {
		return false, errors.New("injected rejected update")
	}
	moved, err := s.RouteSessionV3.CompareAndSwap(ctx, expected, next)
	if err == nil && moved {
		if s.store.fault == "unavailable" {
			s.store.readUnavailable.Store(true)
		}
		return false, errors.New("injected lost commit acknowledgement")
	}
	return moved, err
}

func rehomeDurableStore(t *testing.T, f *rehomeFixture) *playback.MemoryPlanStoreV3 {
	t.Helper()
	store := playback.NewMemoryPlanStoreV3()
	if err := store.SaveAttempt(t.Context(), playback.AttemptRecordV3{
		PlaybackAttemptID: "rehome-attempt", SessionID: f.session.ID, UserID: 1, ProfileID: "profile-1", EffectiveMediaFileID: 42,
		CurrentPlanID: "plan", CurrentPlan: playback.PlanV3{PlanID: "plan"},
		FrozenRecipe: playback.ExecutableRecipeV3{TranscodeNodeURL: f.deadURL, TranscodeTransportID: rehomeTestTransport}, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestRelayFailoverReconcilesTupleAcknowledgement(t *testing.T) {
	for _, fault := range []string{"lost-ack", "unavailable", "rejected"} {
		t.Run(fault, func(t *testing.T) {
			healthy := newFakeRehomeNode(t, false)
			other := newFakeRehomeNode(t, false)
			f := newRehomeFixture(t, []*nodepool.Node{pooledNode(1, unreachableNodeURL(t)), pooledNode(2, healthy.server.URL), pooledNode(3, other.server.URL)}, "ffmpeg")
			cfg := f.handler.playbackConfig()
			cfg.Routing.VideoTranscodeExecution = config.PlaybackExecutionWorkerOnly
			f.handler.PlaybackConfig = func() config.PlaybackConfig { return cfg }
			base := rehomeDurableStore(t, f)
			store := &rehomeFaultStore{MemoryPlanStoreV3: base, fault: fault}
			f.handler.PlanStoreV3 = store
			rr := f.segment("seg_00003.ts")
			if healthy.starts.Load()+other.starts.Load() != 1 {
				t.Fatalf("CAS uncertainty tried another executor: starts=%d/%d", healthy.starts.Load(), other.starts.Load())
			}
			candidate := healthy
			if other.starts.Load() == 1 {
				candidate = other
			}
			live, err := f.sessions.GetSession(f.session.ID)
			if err != nil {
				t.Fatal(err)
			}
			durable, err := base.GetAttempt(t.Context(), f.session.ID)
			if err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "lost-ack":
				if rr.Code != 200 || durable.FrozenRecipe.TranscodeNodeURL != candidate.server.URL || live.TranscodeNodeURL != candidate.server.URL || candidate.stops.Load() != 0 {
					t.Fatalf("acknowledged durable winner lost: status%d durable=%s live=%s stops%d", rr.Code, durable.FrozenRecipe.TranscodeNodeURL, live.TranscodeNodeURL, candidate.stops.Load())
				}
			case "unavailable":
				if rr.Code != 503 || live.TranscodeNodeURL != f.deadURL || durable.FrozenRecipe.TranscodeNodeURL != candidate.server.URL || candidate.stops.Load() != 0 {
					t.Fatalf("unknown outcome changed/stopped candidate: status%d durable=%s live=%s stops%d", rr.Code, durable.FrozenRecipe.TranscodeNodeURL, live.TranscodeNodeURL, candidate.stops.Load())
				}
				store.readUnavailable.Store(false)
				rr = f.segment("seg_00004.ts")
				if rr.Code != 200 || candidate.starts.Load() != 1 || candidate.stops.Load() != 0 || store.cas.Load() != 1 {
					t.Fatalf("next request failed to adopt confirmed candidate: status%d starts%d stops%d CAS%d", rr.Code, candidate.starts.Load(), candidate.stops.Load(), store.cas.Load())
				}
			case "rejected":
				if rr.Code != 502 || durable.FrozenRecipe.TranscodeNodeURL != f.deadURL || live.TranscodeNodeURL != f.deadURL || candidate.stops.Load() != 1 {
					t.Fatalf("known rejected candidate not retired: status%d durable=%s live=%s stops%d", rr.Code, durable.FrozenRecipe.TranscodeNodeURL, live.TranscodeNodeURL, candidate.stops.Load())
				}
			}
		})
	}
}

func TestRelayFailoverRejectsExpiredOrStoppedAttempt(t *testing.T) {
	for _, state := range []string{"expired", "stopped", "wrong-owner"} {
		t.Run(state, func(t *testing.T) {
			healthy := newFakeRehomeNode(t, false)
			f := newRehomeFixture(t, []*nodepool.Node{pooledNode(1, unreachableNodeURL(t)), pooledNode(2, healthy.server.URL)}, "ffmpeg")
			store := rehomeDurableStore(t, f)
			record, err := store.GetAttempt(t.Context(), f.session.ID)
			if err != nil {
				t.Fatal(err)
			}
			switch state {
			case "expired":
				record.ExpiresAt = time.Now().Add(-time.Hour)
			case "stopped":
				now := time.Now()
				record.StoppedAt = &now
			case "wrong-owner":
				record.UserID++
			}
			store.ReplaceAttempt(t.Context(), *record)
			f.handler.PlanStoreV3 = store
			if rr := f.segment("seg_00003.ts"); rr.Code != 502 || healthy.starts.Load() != 0 {
				t.Fatalf("invalid native attempt fell through to legacy: status%d starts%d", rr.Code, healthy.starts.Load())
			}
		})
	}
}

type firstReturnRouteManager struct {
	*playback.SessionManager
	handler   *PlaybackHandler
	plans     *playback.MemoryPlanStoreV3
	changed   atomic.Bool
	candidate atomic.Pointer[playback.TranscodeSession]
}

func (m *firstReturnRouteManager) GetSession(id string) (*playback.Session, error) {
	if candidate := m.handler.tm.GetTranscodeSession(id); candidate != nil && m.changed.CompareAndSwap(false, true) {
		m.candidate.Store(candidate)
		record, err := m.plans.GetAttempt(context.Background(), id)
		if err != nil {
			return nil, err
		}
		record.CurrentPlanID, record.CurrentPlan.PlanID, record.CurrentReplanRequestID = "successor-plan", "successor-plan", "successor-replan"
		record.FrozenRecipe.TranscodeNodeURL, record.FrozenRecipe.TranscodeTransportID = "", "successor-local-work"
		m.plans.ReplaceAttempt(context.Background(), *record)
		if err = m.SetTranscodeRoute(id, playback.TranscodeRoute{TransportID: "successor-local-work"}); err != nil {
			return nil, err
		}
	}
	return m.SessionManager.GetSession(id)
}

func TestRelayFailoverFirstLocalReturnRequiresSettledIdentity(t *testing.T) {
	f := newRehomeFixture(t, []*nodepool.Node{pooledNode(1, unreachableNodeURL(t))}, writePlaybackTestFFmpegSleep(t, "30"))
	store := rehomeDurableStore(t, f)
	f.handler.PlanStoreV3 = store
	manager := &firstReturnRouteManager{SessionManager: f.sessions, handler: f.handler, plans: store}
	f.handler.sessionMgr = manager
	rr := httptest.NewRecorder()
	f.handler.HandleGetTranscodeManifest(rr, playbackTestRequest(http.MethodGet, "/api/v2/playback/transcode/"+f.session.ID+"/master.m3u8?st="+f.token, nil, map[string]string{"session_id": f.session.ID}))
	candidate := manager.candidate.Load()
	if !manager.changed.Load() || candidate == nil {
		t.Fatal("first-return route transition did not execute")
	}
	if rr.Code != http.StatusServiceUnavailable || strings.Contains(rr.Body.String(), "#EXTM3U") || f.handler.tm.GetTranscodeSession(f.session.ID) != nil || candidate.IsRunning() {
		t.Fatalf("first post-flight return served a superseded candidate: status%d cached%v running%v", rr.Code, f.handler.tm.GetTranscodeSession(f.session.ID) != nil, candidate.IsRunning())
	}
	current, err := f.sessions.GetSession(f.session.ID)
	if err != nil || current.TranscodeNodeURL != "" || current.TranscodeTransportID != "successor-local-work" {
		t.Fatal(errors.Join(err, errors.New("stale candidate cleanup changed successor route")))
	}
}
