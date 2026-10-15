package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/noderecipe"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

type memoryRehomeRestoration struct {
	store  *sharedRecipeStore
	key    string
	card   playback.RecipeCard
	expiry time.Time
}

func (r memoryRehomeRestoration) ExpiresAt() time.Time { return r.expiry }
func (r memoryRehomeRestoration) Restore(ctx context.Context, _ time.Time) error {
	return r.store.Put(ctx, r.key, r.card)
}

func (s *sharedRecipeStore) CaptureRestore(_ context.Context, key string, card playback.RecipeCard) (noderecipe.Restoration, error) {
	current, ok := s.Get(context.Background(), key)
	want, _ := json.Marshal(card)
	got, _ := json.Marshal(current)
	if !ok || string(want) != string(got) {
		return nil, errors.New("memory predecessor changed")
	}
	return memoryRehomeRestoration{s, key, card, time.Now().Add(time.Hour)}, nil
}

type failingRehomeCaptureStore struct {
	*sharedRecipeStore
	change bool
}

func (s failingRehomeCaptureStore) CaptureRestore(ctx context.Context, key string, card playback.RecipeCard) (noderecipe.Restoration, error) {
	if !s.change {
		return nil, errors.New("capture unavailable")
	}
	successor := card
	successor.InputPath = "/approved/successor"
	if err := s.Put(ctx, key, successor); err != nil {
		return nil, err
	}
	return s.sharedRecipeStore.CaptureRestore(ctx, key, card)
}

func TestRehomeRestoreCaptureFailsBeforeDispatch(t *testing.T) {
	for _, mode := range []string{"unsupported", "unknown", "changed"} {
		t.Run(mode, func(t *testing.T) {
			node := newFakeRehomeNode(t, false)
			f := newRehomeFixture(t, []*nodepool.Node{pooledNode(1, unreachableNodeURL(t)), pooledNode(2, node.server.URL)}, "ffmpeg")
			cfg := f.handler.playbackConfig()
			cfg.Routing.VideoTranscodeExecution = config.PlaybackExecutionWorkerOnly
			f.handler.PlaybackConfig = func() config.PlaybackConfig { return cfg }
			card, _ := verifiedStreamCardFromToken(f.token, f.session.ID, f.handler.JWTSecret)
			base := newSharedRecipeStore(*card)
			if mode == "unsupported" {
				f.handler.NodeRecipeStore = struct{ recipeCardStoreV3 }{base}
			} else {
				f.handler.NodeRecipeStore = failingRehomeCaptureStore{base, mode == "changed"}
			}
			f.handler.PlanStoreV3 = rehomeDurableStore(t, f)
			err := f.handler.moveTranscodeOffNodeV3(t.Context(), f.session.ID, playback.TranscodeRoute{NodeURL: f.deadURL, TransportID: rehomeTestTransport}, nil, 3)
			if !errors.Is(err, errRehomeOutcomeUnknown) || node.starts.Load() != 0 || node.stops.Load() != 0 {
				t.Fatalf("unsafe capture failure: mode=%s err=%v POST=%d DELETE=%d", mode, err, node.starts.Load(), node.stops.Load())
			}
		})
	}
}

// The candidate DELETE uses the shared reconstruction store just as a legacy
// node does. Exercise the accepted-start/rejected-route path, not its helper.
func TestRejectedRehomeRouteRestoresTokenlessRecipe(t *testing.T) {
	var recipes *sharedRecipeStore
	var starts, deletes atomic.Int32
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletes.Add(1)
			_ = recipes.Delete(r.Context(), strings.TrimPrefix(r.URL.Path, "/transcode/"))
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var request transcodenode.TranscodeStartRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		starts.Add(1)
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(transcodenode.TranscodeStartResponse{SessionID: request.SessionID, ThrottleSeconds: request.ThrottleSeconds})
	}))
	t.Cleanup(node.Close)
	f := newRehomeFixture(t, []*nodepool.Node{pooledNode(1, unreachableNodeURL(t)), pooledNode(2, node.URL)}, "ffmpeg")
	cfg := f.handler.playbackConfig()
	cfg.Routing.VideoTranscodeExecution = config.PlaybackExecutionWorkerOnly
	f.handler.PlaybackConfig = func() config.PlaybackConfig { return cfg }
	card, _ := verifiedStreamCardFromToken(f.token, f.session.ID, f.handler.JWTSecret)
	if card == nil {
		t.Fatal("fixture recipe missing")
	}
	recipes = newSharedRecipeStore(*card)
	f.handler.NodeRecipeStore = recipes
	store := &rehomeFaultStore{MemoryPlanStoreV3: rehomeDurableStore(t, f), fault: "rejected"}
	f.handler.PlanStoreV3 = store
	dead := playback.TranscodeRoute{NodeURL: f.deadURL, TransportID: rehomeTestTransport}
	if err := f.handler.moveTranscodeOffNodeV3(t.Context(), f.session.ID, dead, nil, 3); err == nil {
		t.Fatal("rejected route unexpectedly committed")
	}
	if starts.Load() != 1 || deletes.Load() != 1 || store.cas.Load() != 1 {
		t.Fatalf("start/DELETE/CAS = %d/%d/%d, want 1/1/1", starts.Load(), deletes.Load(), store.cas.Load())
	}
	after, ok := recipes.Get(t.Context(), rehomeTestTransport)
	if !ok || after.TranscodeNodeURL != f.deadURL || after.InputPath != card.InputPath {
		t.Fatalf("rejected route lost original tokenless recipe: present=%v card=%+v", ok, after)
	}
	// A later tokenless request must load the retained card and start again.
	store.fault = "lost-ack"
	if err := f.handler.moveTranscodeOffNodeV3(context.Background(), f.session.ID, dead, nil, 4); err != nil {
		t.Fatal("tokenless retry failed", err)
	}
	if starts.Load() != 2 || deletes.Load() != 1 {
		t.Fatalf("retry starts/DELETEs = %d/%d, want 2/1", starts.Load(), deletes.Load())
	}
	waitForStoredRecipeNode(t, recipes, node.URL, 2)
}

type rejectedRehomeStateStore struct {
	*playback.MemoryPlanStoreV3
	reject  func(context.Context, playback.AttemptRecordV3, playback.TranscodeRoute)
	restore func(context.Context, func(context.Context) error) error
}

type rejectedRehomeStateSession struct {
	playback.RouteSessionV3
	store *rejectedRehomeStateStore
}

func (s *rejectedRehomeStateStore) AcquireRouteSession(ctx context.Context, id string) (playback.RouteSessionV3, error) {
	owned, err := s.MemoryPlanStoreV3.AcquireRouteSession(ctx, id)
	if err != nil {
		return nil, err
	}
	return &rejectedRehomeStateSession{owned, s}, nil
}

func (s *rejectedRehomeStateSession) CompareAndSwap(ctx context.Context, expected playback.AttemptRecordV3, next playback.TranscodeRoute) (bool, error) {
	s.store.reject(ctx, expected, next)
	return false, errors.New("injected rejected route commit")
}

func (s *rejectedRehomeStateSession) WithLiveRoute(ctx context.Context, expected playback.AttemptRecordV3, run func(context.Context) error) (bool, error) {
	if s.store.restore == nil {
		return s.RouteSessionV3.WithLiveRoute(ctx, expected, run)
	}
	return s.RouteSessionV3.WithLiveRoute(ctx, expected, func(ctx context.Context) error { return s.store.restore(ctx, run) })
}

func TestRejectedRehomeRoutePreservesSettledAuthority(t *testing.T) {
	for _, outcome := range []string{"stopped", "expired", "different-work", "same-work-candidate", "same-work-other-node", "same-work-old-node", "same-work-same-plan-third-node", "same-work-new-owner", "token-card"} {
		t.Run(outcome, func(t *testing.T) {
			var recipes *sharedRecipeStore
			var starts, deletes atomic.Int32
			node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete {
					deletes.Add(1)
					_ = recipes.Delete(r.Context(), strings.TrimPrefix(r.URL.Path, "/transcode/"))
					w.WriteHeader(http.StatusNoContent)
					return
				}
				var request transcodenode.TranscodeStartRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				starts.Add(1)
				w.WriteHeader(http.StatusAccepted)
				_ = json.NewEncoder(w).Encode(transcodenode.TranscodeStartResponse{SessionID: request.SessionID, ThrottleSeconds: request.ThrottleSeconds})
			}))
			t.Cleanup(node.Close)
			f := newRehomeFixture(t, []*nodepool.Node{pooledNode(1, unreachableNodeURL(t)), pooledNode(2, node.URL)}, "ffmpeg")
			cfg := f.handler.playbackConfig()
			cfg.Routing.VideoTranscodeExecution = config.PlaybackExecutionWorkerOnly
			f.handler.PlaybackConfig = func() config.PlaybackConfig { return cfg }
			card, _ := verifiedStreamCardFromToken(f.token, f.session.ID, f.handler.JWTSecret)
			recipes = newSharedRecipeStore(*card)
			f.handler.NodeRecipeStore = recipes
			base := rehomeDurableStore(t, f)
			expectedKey := rehomeTestTransport
			expectedCard := *card
			expectedCard.InputPath = "/media/successor.mkv"
			store := &rejectedRehomeStateStore{MemoryPlanStoreV3: base}
			store.reject = func(ctx context.Context, record playback.AttemptRecordV3, next playback.TranscodeRoute) {
				switch outcome {
				case "stopped":
					if _, _, err := base.StopAttempt(ctx, record.SessionID, uuid.NewString(), nil); err != nil {
						t.Error(err)
					}
					_ = recipes.Delete(ctx, rehomeTestTransport)
				case "expired":
					record.ExpiresAt = time.Now().Add(-time.Hour)
					base.ReplaceAttempt(ctx, record)
					_ = recipes.Delete(ctx, rehomeTestTransport)
				case "different-work", "same-work-candidate", "same-work-other-node", "same-work-old-node", "same-work-same-plan-third-node", "same-work-new-owner":
					if outcome != "same-work-same-plan-third-node" {
						record.CurrentPlanID, record.CurrentReplanRequestID = "successor-plan", "successor-replan"
					}
					record.FrozenRecipe.TranscodeNodeURL = "https://successor.example"
					switch outcome {
					case "different-work":
						expectedKey = "successor-work"
						record.FrozenRecipe.TranscodeTransportID = expectedKey
						_ = recipes.Delete(ctx, rehomeTestTransport)
					case "same-work-candidate":
						record.FrozenRecipe.TranscodeNodeURL = next.NodeURL
					case "same-work-old-node":
						record.FrozenRecipe.TranscodeNodeURL = f.deadURL
					case "same-work-new-owner":
						record.UserID++
					}
					base.ReplaceAttempt(ctx, record)
					expectedCard.TranscodeNodeURL = record.FrozenRecipe.TranscodeNodeURL
					expectedCard.TranscodeTransportID = expectedKey
					_ = recipes.Put(ctx, expectedKey, expectedCard)
				}
			}
			f.handler.PlanStoreV3 = store
			var tokenCard *playback.RecipeCard
			if outcome == "token-card" {
				tokenCard = card
			}
			err := f.handler.moveTranscodeOffNodeV3(t.Context(), f.session.ID, playback.TranscodeRoute{NodeURL: f.deadURL, TransportID: rehomeTestTransport}, tokenCard, 3)
			unknown := strings.HasPrefix(outcome, "same-work-")
			if err == nil || errors.Is(err, errRehomeOutcomeUnknown) != unknown || starts.Load() != 1 {
				t.Fatalf("outcome=%s err=%v starts=%d", outcome, err, starts.Load())
			}
			wantDeletes := int32(1)
			if unknown {
				wantDeletes = 0
			}
			if deletes.Load() != wantDeletes {
				t.Fatalf("outcome=%s DELETEs=%d want%d", outcome, deletes.Load(), wantDeletes)
			}
			after, found := recipes.Get(t.Context(), expectedKey)
			wantCard := unknown || outcome == "different-work"
			if found != wantCard || found && (after.InputPath != expectedCard.InputPath || after.TranscodeNodeURL != expectedCard.TranscodeNodeURL) {
				t.Fatalf("outcome=%s recipe present=%v card=%+v", outcome, found, after)
			}
		})
	}
}

func TestRejectedRehomeRestorationDoesNotWaitOnStoppingLifecycle(t *testing.T) {
	node := newFakeRehomeNode(t, false)
	f := newRehomeFixture(t, []*nodepool.Node{pooledNode(1, unreachableNodeURL(t)), pooledNode(2, node.server.URL)}, "ffmpeg")
	cfg := f.handler.playbackConfig()
	cfg.Routing.VideoTranscodeExecution = config.PlaybackExecutionWorkerOnly
	f.handler.PlaybackConfig = func() config.PlaybackConfig { return cfg }
	card, _ := verifiedStreamCardFromToken(f.token, f.session.ID, f.handler.JWTSecret)
	f.handler.NodeRecipeStore = newSharedRecipeStore(*card)
	base := rehomeDurableStore(t, f)
	store := &rejectedRehomeStateStore{MemoryPlanStoreV3: base, reject: func(context.Context, playback.AttemptRecordV3, playback.TranscodeRoute) {}}
	stopped := make(chan error, 1)
	stopCtx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	store.restore = func(ctx context.Context, restore func(context.Context) error) error {
		// The row is already owned. A stop holds TM and waits for that row;
		// restoration must release it without waiting back on TM.
		held := make(chan struct{})
		go func() {
			unlock := f.handler.tm.LockSessionLifecycle(f.session.ID)
			defer unlock()
			close(held)
			_, _, err := base.StopAttempt(stopCtx, f.session.ID, uuid.NewString(), nil)
			stopped <- err
		}()
		<-held
		return restore(ctx)
	}
	f.handler.PlanStoreV3 = store
	started := time.Now()
	err := f.handler.moveTranscodeOffNodeV3(t.Context(), f.session.ID, playback.TranscodeRoute{NodeURL: f.deadURL, TransportID: rehomeTestTransport}, nil, 3)
	if !errors.Is(err, errRehomeOutcomeUnknown) || time.Since(started) > time.Second {
		t.Fatalf("busy lifecycle did not promptly release row ownership: %v elapsed=%s", err, time.Since(started))
	}
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal("stop could not settle after restoration backed off", err)
		}
	case <-time.After(time.Second):
		t.Fatal("restoration retained the row needed by stop")
	}
}

type rehomeStopAckStore struct {
	*sharedRecipeStore
	puts atomic.Int32
}

type rejectingLegacyRehomeSessions struct{ *playback.SessionManager }

func (s rejectingLegacyRehomeSessions) MoveTranscodeExecutor(string, playback.TranscodeRoute, playback.TranscodeExecutorMove) (bool, error) {
	return false, errors.New("legacy route commit rejected")
}

func TestRejectedLegacyRehomeRouteUsesBoundedRestore(t *testing.T) {
	var recipes *rehomeStopAckStore
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			_ = recipes.Delete(r.Context(), strings.TrimPrefix(r.URL.Path, "/transcode/"))
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"session_id":"rehome-transport"}`))
	}))
	t.Cleanup(node.Close)
	f := newRehomeFixture(t, []*nodepool.Node{pooledNode(1, unreachableNodeURL(t)), pooledNode(2, node.URL)}, "ffmpeg")
	cfg := f.handler.playbackConfig()
	cfg.Routing.VideoTranscodeExecution = config.PlaybackExecutionWorkerOnly
	f.handler.PlaybackConfig = func() config.PlaybackConfig { return cfg }
	card, _ := verifiedStreamCardFromToken(f.token, f.session.ID, f.handler.JWTSecret)
	recipes = &rehomeStopAckStore{sharedRecipeStore: newSharedRecipeStore(*card)}
	f.handler.NodeRecipeStore = recipes
	f.handler.sessionMgr = rejectingLegacyRehomeSessions{f.sessions}
	err := f.handler.moveTranscodeOffNodeV3(t.Context(), f.session.ID, playback.TranscodeRoute{NodeURL: f.deadURL, TransportID: rehomeTestTransport}, nil, 3)
	after, found := recipes.Get(t.Context(), rehomeTestTransport)
	if err == nil || errors.Is(err, errRehomeOutcomeUnknown) || recipes.puts.Load() != 1 || !found || after.InputPath != card.InputPath {
		t.Fatalf("legacy rejected move did not use bounded restoration: err=%v restores=%d found=%v card=%+v", err, recipes.puts.Load(), found, after)
	}
}

func (s *rehomeStopAckStore) Put(ctx context.Context, k string, c playback.RecipeCard) error {
	s.puts.Add(1)
	return s.sharedRecipeStore.Put(ctx, k, c)
}

type countedRehomeRestoration struct {
	noderecipe.Restoration
	calls *atomic.Int32
}

func (r countedRehomeRestoration) Restore(ctx context.Context, expiry time.Time) error {
	r.calls.Add(1)
	return r.Restoration.Restore(ctx, expiry)
}

func (s *rehomeStopAckStore) CaptureRestore(ctx context.Context, k string, c playback.RecipeCard) (noderecipe.Restoration, error) {
	r, err := s.sharedRecipeStore.CaptureRestore(ctx, k, c)
	if err != nil {
		return nil, err
	}
	return countedRehomeRestoration{r, &s.puts}, nil
}
func TestRejectedRehomeRouteRequiresStopAcknowledgement(t *testing.T) {
	var starts, deletes atomic.Int32
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deletes.Add(1)
			http.Error(w, "authority revocation unavailable", http.StatusServiceUnavailable)
			return
		}
		var req transcodenode.TranscodeStartRequest
		if e := json.NewDecoder(r.Body).Decode(&req); e != nil {
			t.Error(e)
			return
		}
		starts.Add(1)
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(transcodenode.TranscodeStartResponse{SessionID: req.SessionID, ThrottleSeconds: req.ThrottleSeconds})
	}))
	defer node.Close()
	f := newRehomeFixture(t, []*nodepool.Node{pooledNode(1, unreachableNodeURL(t)), pooledNode(2, node.URL)}, "ffmpeg")
	cfg := f.handler.playbackConfig()
	cfg.Routing.VideoTranscodeExecution = config.PlaybackExecutionWorkerOnly
	f.handler.PlaybackConfig = func() config.PlaybackConfig { return cfg }
	card, _ := verifiedStreamCardFromToken(f.token, f.session.ID, f.handler.JWTSecret)
	if card == nil {
		t.Fatal("missing card")
	}
	recipes := &rehomeStopAckStore{sharedRecipeStore: newSharedRecipeStore(*card)}
	f.handler.NodeRecipeStore = recipes
	f.handler.PlanStoreV3 = &rehomeFaultStore{MemoryPlanStoreV3: rehomeDurableStore(t, f), fault: "rejected"}
	err := f.handler.moveTranscodeOffNodeV3(t.Context(), f.session.ID, playback.TranscodeRoute{NodeURL: f.deadURL, TransportID: rehomeTestTransport}, nil, 3)
	if starts.Load() != 1 || deletes.Load() != 1 {
		t.Fatalf("schedule start=%d DELETE=%d", starts.Load(), deletes.Load())
	}
	if !errors.Is(err, errRehomeOutcomeUnknown) || recipes.puts.Load() != 0 {
		t.Fatalf("unacknowledged DELETE permitted restore: err=%v puts=%d", err, recipes.puts.Load())
	}
}

type failedRehomeReadStore struct {
	*playback.MemoryPlanStoreV3
	unknown atomic.Bool
}

type failedRehomeReadSession struct {
	playback.RouteSessionV3
	store *failedRehomeReadStore
}

func (s *failedRehomeReadStore) AcquireRouteSession(ctx context.Context, id string) (playback.RouteSessionV3, error) {
	owned, err := s.MemoryPlanStoreV3.AcquireRouteSession(ctx, id)
	if err != nil {
		return nil, err
	}
	return &failedRehomeReadSession{owned, s}, nil
}

func (s *failedRehomeReadSession) GetAttempt(ctx context.Context) (*playback.AttemptRecordV3, error) {
	if s.store.unknown.Load() {
		return nil, errors.New("fresh route outcome unavailable")
	}
	return s.RouteSessionV3.GetAttempt(ctx)
}

func TestFailedRehomeStartPreservesFreshAuthority(t *testing.T) {
	for _, mode := range []string{"restored", "stop-failure", "stopped", "expired", "different-work", "same-work-candidate", "same-work-old-node", "same-plan-third-node", "same-work-new-owner", "read-unknown", "token-card"} {
		t.Run(mode, func(t *testing.T) {
			var recipes *rehomeStopAckStore
			var store *failedRehomeReadStore
			var starts, deletes atomic.Int32
			var mutate func()
			node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete {
					if mode == "stop-failure" {
						deletes.Add(1)
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					_ = recipes.Delete(r.Context(), rehomeTestTransport)
					deletes.Add(1)
					w.WriteHeader(http.StatusNoContent)
					return
				}
				starts.Add(1)
				mutate()
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			t.Cleanup(node.Close)
			f := newRehomeFixture(t, []*nodepool.Node{pooledNode(1, unreachableNodeURL(t)), pooledNode(2, node.URL)}, "ffmpeg")
			cfg := f.handler.playbackConfig()
			cfg.Routing.VideoTranscodeExecution = config.PlaybackExecutionWorkerOnly
			f.handler.PlaybackConfig = func() config.PlaybackConfig { return cfg }
			card, _ := verifiedStreamCardFromToken(f.token, f.session.ID, f.handler.JWTSecret)
			recipes = &rehomeStopAckStore{sharedRecipeStore: newSharedRecipeStore(*card)}
			f.handler.NodeRecipeStore = recipes
			store = &failedRehomeReadStore{MemoryPlanStoreV3: rehomeDurableStore(t, f)}
			f.handler.PlanStoreV3 = store
			expectedKey := rehomeTestTransport
			expectedCard := *card
			mutate = func() {
				record, err := store.GetAttempt(t.Context(), f.session.ID)
				if err != nil {
					t.Error(err)
					return
				}
				switch mode {
				case "stopped":
					_, _, err = store.StopAttempt(t.Context(), f.session.ID, uuid.NewString(), nil)
					if err != nil {
						t.Error(err)
					}
					_ = recipes.Delete(t.Context(), rehomeTestTransport)
				case "expired":
					record.ExpiresAt = time.Now().Add(-time.Hour)
					store.ReplaceAttempt(t.Context(), *record)
					_ = recipes.Delete(t.Context(), rehomeTestTransport)
				case "different-work", "same-work-candidate", "same-work-old-node", "same-plan-third-node", "same-work-new-owner":
					if mode != "same-plan-third-node" {
						record.CurrentPlanID = "successor-plan"
					}
					record.FrozenRecipe.TranscodeNodeURL = "https://successor.example"
					switch mode {
					case "different-work":
						expectedKey = "successor-work"
						record.FrozenRecipe.TranscodeTransportID = expectedKey
						_ = recipes.Delete(t.Context(), rehomeTestTransport)
					case "same-work-candidate":
						record.FrozenRecipe.TranscodeNodeURL = node.URL
					case "same-work-old-node":
						record.FrozenRecipe.TranscodeNodeURL = f.deadURL
					case "same-work-new-owner":
						record.UserID++
					}
					store.ReplaceAttempt(t.Context(), *record)
					expectedCard.InputPath = "/approved/successor"
					expectedCard.TranscodeNodeURL = record.FrozenRecipe.TranscodeNodeURL
					_ = recipes.sharedRecipeStore.Put(t.Context(), expectedKey, expectedCard)
				case "read-unknown":
					store.unknown.Store(true)
				}
			}
			var tokenCard *playback.RecipeCard
			if mode == "token-card" {
				tokenCard = card
			}
			err := f.handler.moveTranscodeOffNodeV3(t.Context(), f.session.ID, playback.TranscodeRoute{NodeURL: f.deadURL, TransportID: rehomeTestTransport}, tokenCard, 3)
			if mode == "token-card" {
				deadline := time.Now().Add(time.Second)
				for deletes.Load() == 0 && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
			}
			unknown := strings.HasPrefix(mode, "same-work-") || mode == "same-plan-third-node" || mode == "read-unknown" || mode == "stop-failure"
			wantDeletes := int32(1)
			if unknown && mode != "stop-failure" {
				wantDeletes = 0
			}
			if err == nil || errors.Is(err, errRehomeOutcomeUnknown) != unknown || starts.Load() != 1 || deletes.Load() != wantDeletes {
				t.Fatalf("mode=%s err=%v POST=%d DELETE=%d want=1/%d", mode, err, starts.Load(), deletes.Load(), wantDeletes)
			}
			if mode == "stopped" || mode == "expired" || mode == "different-work" {
				if !errors.Is(err, playback.ErrReplanSupersededV3) {
					t.Fatal("terminal route did not end candidate iteration", err)
				}
			}
			wantRestores := int32(0)
			if mode == "restored" {
				wantRestores = 1
			}
			if recipes.puts.Load() != wantRestores {
				t.Fatalf("mode=%s restores=%d want=%d", mode, recipes.puts.Load(), wantRestores)
			}
			after, found := recipes.Get(t.Context(), expectedKey)
			wantFound := mode != "stopped" && mode != "expired" && mode != "token-card"
			if found != wantFound || found && after.InputPath != expectedCard.InputPath {
				t.Fatalf("mode=%s card=%+v found=%v want=%v", mode, after, found, wantFound)
			}
		})
	}
}
