package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

// headerAuthRestartFixtureV3 starts a header-authenticated transcode on a
// pooled node, then stands in for the restarted API: a handler with an empty
// session manager that shares only what survives a restart (the attempt rows,
// the Redis recipe stores, the node pool).
type headerAuthRestartFixtureV3 struct {
	started   playback.DecisionResponseV3
	startedOn string
	other     string
	nodes     map[string]*httptest.Server
	attempts  *playback.MemoryPlanStoreV3
	recipes   *recordingRecipeCardStoreV3
	configure func(*PlaybackHandler)
}

func newHeaderAuthRestartFixtureV3(t *testing.T, startOpts ...func(*playback.StartRequestV3)) headerAuthRestartFixtureV3 {
	t.Helper()
	newNode := func() *httptest.Server {
		var node *httptest.Server
		node = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/hw-capabilities":
				writeJSON(w, http.StatusOK, playback.HWAccelInfo{Transformations: remoteHLSPlanV3().Transformations})
			case r.Method == http.MethodPost && r.URL.Path == "/transcode/start":
				var request transcodenode.TranscodeStartRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Errorf("decode remote start: %v", err)
				}
				writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{SessionID: request.SessionID, Status: "started", AudioRecipeVersion: request.AudioRecipeVersion, ThrottleSeconds: request.ThrottleSeconds})
			case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/transcode/"):
				// Name the node in the body so a test can tell which executor served.
				_, _ = io.WriteString(w, "served-by "+node.URL)
			default:
				w.WriteHeader(http.StatusNoContent)
			}
		}))
		t.Cleanup(node.Close)
		return node
	}
	first, second := newNode(), newNode()
	transcodes := nodepool.NewTranscodePool()
	transcodes.SetNodes([]*nodepool.Node{
		{ID: 1, URL: first.URL, Enabled: true, Healthy: true},
		{ID: 2, URL: second.URL, Enabled: true, Healthy: true},
	})
	planner := nodepool.NewPlanner(nodepool.NewProxyPool(), transcodes)
	file := v3HandlerFixtureFile(t)
	file.CodecVideo, file.VideoTracks[0].Codec = "mpeg4", "mpeg4"
	ffmpeg, transcodeDir := writePlaybackTestFFmpeg(t), t.TempDir()
	f := headerAuthRestartFixtureV3{
		nodes:    map[string]*httptest.Server{first.URL: first, second.URL: second},
		attempts: playback.NewMemoryPlanStoreV3(),
		recipes:  &recordingRecipeCardStoreV3{},
	}
	nodeRecipes := &lockedRecipeCardStoreV3{}
	f.configure = func(handler *PlaybackHandler) {
		handler.fileResolver = testPlaybackFileResolver{file: file}
		handler.JWTSecret = "test-secret"
		handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"transcode_enabled": "true", "allow_4k_transcode": "true"}}
		handler.ItemAccess = allowAllPlaybackItemAccess{}
		handler.PlaybackConfig = playbackTestConfig(ffmpeg, transcodeDir)
		handler.NodePlanner = planner
		requireWorkerRoutingV3(handler)
		handler.PlanStoreV3 = f.attempts
		handler.SessionRecipeStore = f.recipes
		handler.NodeRecipeStore = nodeRecipes
		handler.v3Registry = playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{
			{Name: playback.TransformationAudioToAACV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3, Available: true},
			{Name: playback.TransformationVideoToH264V3, RecipeVersion: playback.TransformationVideoToH264RecipeVersionV3, Available: true},
		})
	}
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	f.configure(handler)

	start := v3HandlerStartRequest()
	start.ClientFeatures = append(start.ClientFeatures, playback.FeatureHeaderAuthenticatedMediaV3)
	start.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3] = playback.DeliveryCapabilityV3{Enabled: true, SupportedOnDevice: true}
	for _, opt := range startOpts {
		opt(&start)
	}
	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, start))).WithContext(newAuthorizedPlaybackContext()))
	if rr.Code != http.StatusCreated || json.Unmarshal(rr.Body.Bytes(), &f.started) != nil || f.started.PlaybackPlan == nil {
		t.Fatalf("start status=%d body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(f.started.PlaybackPlan.Stream.URL, streamTokenParam+"=") {
		t.Fatalf("header-authenticated plan URL carries a token: %s", f.started.PlaybackPlan.Stream.URL)
	}
	session, err := handler.sessionMgr.GetSession(f.started.SessionID)
	if err != nil || session.TranscodeNodeURL == "" {
		t.Fatalf("start did not place the transcode on a node: %#v", session)
	}
	f.startedOn, f.other = session.TranscodeNodeURL, second.URL
	if f.startedOn == second.URL {
		f.other = first.URL
	}
	return f
}

// restart is the API process after a restart: nothing in memory.
func (f headerAuthRestartFixtureV3) restart() *PlaybackHandler {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	f.configure(handler)
	return handler
}

func transcodeServeRequestV3(ctx context.Context, sessionID, segment string) *http.Request {
	path := "/api/v1/playback/transcode/" + sessionID + "/master.m3u8"
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("session_id", sessionID)
	if segment != "" {
		path = "/api/v1/playback/transcode/" + sessionID + "/segment/" + segment
		routeCtx.URLParams.Add("name", segment)
	}
	return httptest.NewRequest(http.MethodGet, path, nil).WithContext(context.WithValue(ctx, chi.RouteCtxKey, routeCtx))
}

func playbackCallerContextV3(userID int, profileID string) context.Context {
	ctx := context.Background()
	if userID != 0 {
		ctx = apimw.SetClaims(ctx, &auth.Claims{UserID: userID, Role: "user", TokenType: auth.TokenTypeAccess})
	}
	if profileID != "" {
		ctx = apimw.SetProfileID(ctx, profileID)
	}
	return ctx
}

// A header-authenticated session's URLs carry no stream token, so the API
// restart that every deploy performs used to leave its manifest and segments
// answering 404 forever (#2495). The owner's next request now rebuilds it from
// the server-side recovery card, and only the owner's.
func TestHeaderAuthenticatedTranscodeRecoversAfterAnAPIRestart(t *testing.T) {
	owner := func() context.Context { return newAuthorizedPlaybackContext() }
	for _, test := range []struct {
		name      string
		mutate    func(t *testing.T, f headerAuthRestartFixtureV3)
		restarted func(*PlaybackHandler)
		caller    func() context.Context
		want      int
	}{
		{name: "owner", caller: owner, want: http.StatusOK},
		{name: "unauthenticated", caller: func() context.Context { return context.Background() }, want: http.StatusNotFound},
		{name: "another account", caller: func() context.Context { return playbackCallerContextV3(2, "profile-1") }, want: http.StatusNotFound},
		{name: "another profile of the account", caller: func() context.Context { return playbackCallerContextV3(1, "profile-2") }, want: http.StatusNotFound},
		{
			// The attempt row is the authority: a stop that reached Postgres but
			// never cleared Redis must not let the card rebuild the session.
			name: "stopped attempt", caller: owner, want: http.StatusNotFound,
			mutate: func(t *testing.T, f headerAuthRestartFixtureV3) {
				if _, _, err := f.attempts.StopAttempt(context.Background(), f.started.SessionID, "6f9619ff-8b86-4d01-b42d-00cf4fc964ff", nil); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			// A replan that committed but failed to overwrite the card leaves
			// the previous transport's card behind; it must not rebuild that one.
			name: "card from a superseded plan", caller: owner, want: http.StatusNotFound,
			mutate: func(t *testing.T, f headerAuthRestartFixtureV3) {
				card := f.recipes.cards[f.started.SessionID]
				card.PlanID = "plan:superseded"
				f.recipes.cards[f.started.SessionID] = card
			},
		},
		{
			// Redis being unreachable says nothing about the session either.
			name: "recipe store unavailable", caller: owner, want: http.StatusInternalServerError,
			mutate: func(_ *testing.T, f headerAuthRestartFixtureV3) {
				f.recipes.lookupErr = errors.New("dial tcp: connection refused")
			},
		},
		{
			// The post-deploy reconnect wave can find Postgres busy. That must
			// read as a retryable failure, not as the session being gone, which
			// is the answer that makes a client tear playback down.
			name: "attempt store unavailable", caller: owner, want: http.StatusInternalServerError,
			restarted: func(h *PlaybackHandler) {
				h.PlanStoreV3 = unavailableAttemptStoreV3{h.PlanStoreV3}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newHeaderAuthRestartFixtureV3(t)
			if test.mutate != nil {
				test.mutate(t, f)
			}
			restarted := f.restart()
			if test.restarted != nil {
				test.restarted(restarted)
			}

			rr := httptest.NewRecorder()
			restarted.HandleGetTranscodeManifest(rr, transcodeServeRequestV3(test.caller(), f.started.SessionID, ""))
			if rr.Code != test.want {
				t.Fatalf("manifest after restart = %d (%s), want %d", rr.Code, rr.Body.String(), test.want)
			}
			if test.want != http.StatusOK {
				// Refused before anything was registered or started for it.
				if _, err := restarted.sessionMgr.GetSession(f.started.SessionID); err == nil {
					t.Fatal("a refused recovery registered the playback session")
				}
				return
			}
			if body := rr.Body.String(); body != "served-by "+f.startedOn {
				t.Fatalf("manifest body = %q, want the relay from the node the transcode runs on", body)
			}
			rr = httptest.NewRecorder()
			restarted.HandleGetTranscodeSegment(rr, transcodeServeRequestV3(owner(), f.started.SessionID, "seg_00004.ts"))
			if rr.Code != http.StatusOK {
				t.Fatalf("segment after restart = %d (%s)", rr.Code, rr.Body.String())
			}
			// The rebuilt session is still header-authenticated: its UUID did not
			// become a bearer credential on the way back.
			rr = httptest.NewRecorder()
			restarted.HandleGetTranscodeSegment(rr, transcodeServeRequestV3(context.Background(), f.started.SessionID, "seg_00005.ts"))
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("unauthenticated segment on the rebuilt session = %d, want 401", rr.Code)
			}
		})
	}
}

// A client that owns its progress must not have the rebuilt session start
// writing server-side resume state for it.
func TestHeaderAuthenticatedRecoveryKeepsAClientOwnedProgressPolicy(t *testing.T) {
	f := newHeaderAuthRestartFixtureV3(t, func(start *playback.StartRequestV3) {
		position := 120.0
		start.ProgressPersistence = playback.ProgressPersistenceClientV3
		start.StartPosition = &position
	})
	restarted := f.restart()

	rr := httptest.NewRecorder()
	restarted.HandleGetTranscodeManifest(rr, transcodeServeRequestV3(newAuthorizedPlaybackContext(), f.started.SessionID, ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("manifest after restart = %d (%s)", rr.Code, rr.Body.String())
	}
	session, err := restarted.sessionMgr.GetSession(f.started.SessionID)
	if err != nil {
		t.Fatalf("rebuilt session: %v", err)
	}
	if !session.DisableProgressPersistence || session.RequestedMediaFileID != 42 {
		t.Fatalf("rebuilt session progress policy = disabled %v, requested file %d; want the start's client-owned policy and requested file 42",
			session.DisableProgressPersistence, session.RequestedMediaFileID)
	}
}

// After a restart the node the session ran on may be gone too. The move to
// another node must keep the recovery card on the executor that now runs it,
// or the next restart sends the session back to the dead node.
func TestHeaderAuthenticatedRecoveryMovesOffADeadNodeAndRecordsTheMove(t *testing.T) {
	f := newHeaderAuthRestartFixtureV3(t)
	f.nodes[f.startedOn].Close()
	restarted := f.restart()

	rr := httptest.NewRecorder()
	restarted.HandleGetTranscodeManifest(rr, transcodeServeRequestV3(newAuthorizedPlaybackContext(), f.started.SessionID, ""))
	if rr.Code != http.StatusOK || rr.Body.String() != "served-by "+f.other {
		t.Fatalf("manifest after restart with a dead node = %d %q, want the relay from %s", rr.Code, rr.Body.String(), f.other)
	}
	card := f.recipes.cards[f.started.SessionID]
	if card.TranscodeNodeURL != f.other || card.PlanID != f.started.PlaybackPlan.PlanID || !card.RequireMediaAuthorization {
		t.Fatalf("recovery card after the move = node %q plan %q media-auth %v, want node %q under plan %q",
			card.TranscodeNodeURL, card.PlanID, card.RequireMediaAuthorization, f.other, f.started.PlaybackPlan.PlanID)
	}
}

// Direct play and progressive remux are served by the stream handler, which
// rebuilds a header-authenticated session the same way.
func TestHeaderAuthenticatedDirectStreamRecoversAfterAnAPIRestart(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	attempts := playback.NewMemoryPlanStoreV3()
	recipes := &recordingRecipeCardStoreV3{}
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
	handler.JWTSecret = "test-secret"
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "true"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.PlanStoreV3 = attempts
	handler.SessionRecipeStore = recipes

	start := v3HandlerStartRequest()
	start.ClientFeatures = append(start.ClientFeatures, playback.FeatureHeaderAuthenticatedMediaV3)
	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, start))).WithContext(newAuthorizedPlaybackContext()))
	var started playback.DecisionResponseV3
	if rr.Code != http.StatusCreated || json.Unmarshal(rr.Body.Bytes(), &started) != nil || started.PlaybackPlan == nil ||
		started.PlaybackPlan.Delivery != playback.DeliveryOriginalHTTPV3 {
		t.Fatalf("start status=%d body=%s", rr.Code, rr.Body.String())
	}

	manager := playback.NewSessionManager(0, 0)
	restarted := NewPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	stream := NewStreamHandler(manager, testPlaybackFileResolver{file: file})
	stream.TM = restarted.TranscodeManager()
	stream.PlanStoreV3 = attempts
	stream.SessionRecipeStore = recipes

	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("session_id", started.SessionID)
	request := httptest.NewRequest(http.MethodGet, started.PlaybackPlan.Stream.URL, nil).
		WithContext(context.WithValue(newAuthorizedPlaybackContext(), chi.RouteCtxKey, routeCtx))
	rr = httptest.NewRecorder()
	stream.HandleStream(rr, request)
	if rr.Code != http.StatusOK || rr.Body.Len() == 0 {
		t.Fatalf("direct stream after restart = %d (%s), want the file bytes", rr.Code, rr.Body.String())
	}
}

// lockedRecipeCardStoreV3 is a recipe store safe for the goroutine that writes
// a moved transport's node recipe after the move has returned.
type lockedRecipeCardStoreV3 struct {
	mu    sync.Mutex
	inner recordingRecipeCardStoreV3
}

func (s *lockedRecipeCardStoreV3) Enabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inner.Enabled()
}

// unavailableAttemptStoreV3 is an attempt store whose reads fail the way a
// saturated or failing-over Postgres does.
type unavailableAttemptStoreV3 struct {
	playback.PlanStoreV3
}

func (unavailableAttemptStoreV3) GetAttempt(context.Context, string) (*playback.AttemptRecordV3, error) {
	return nil, errors.New("connection reset by peer")
}

func (s *lockedRecipeCardStoreV3) Get(ctx context.Context, key string) (*playback.RecipeCard, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inner.Get(ctx, key)
}

func (s *lockedRecipeCardStoreV3) Put(ctx context.Context, key string, card playback.RecipeCard) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inner.Put(ctx, key, card)
}

func (s *lockedRecipeCardStoreV3) Delete(ctx context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inner.Delete(ctx, key)
}

// An FFmpeg exit or a copy-safety stop tears the session down without ending
// the attempt, so the client's next request rebuilds it, as a stream token
// would. A user's stop retires the recovery card, and so does any teardown of
// a remote transcode, whose node job and node recipe go with it.
func TestSessionRecipeRetentionOnStop(t *testing.T) {
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	t.Cleanup(node.Close)
	for _, test := range []struct {
		name          string
		userInitiated bool
		nodeURL       string
		wantDeleted   bool
	}{
		{name: "system teardown", userInitiated: false, wantDeleted: false},
		{name: "user stop", userInitiated: true, wantDeleted: true},
		// The teardown stops the node job and drops its recipe, so the card
		// could only rebuild a session the node no longer serves.
		{name: "system teardown of a remote transcode", userInitiated: false, nodeURL: node.URL, wantDeleted: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := playback.NewSessionManager(0, 0)
			manager.RegisterReconstructed(&playback.Session{ID: "session-recipe-stop", UserID: 1, ProfileID: "profile-1", PlayMethod: playback.PlayTranscode, RequireMediaAuthorization: true,
				TranscodeNodeURL: test.nodeURL, TranscodeTransportID: "session-recipe-stop-transport"})
			handler := NewPlaybackHandler(manager)
			recipes := &recordingRecipeCardStoreV3{cards: map[string]playback.RecipeCard{
				"session-recipe-stop": {SessionID: "session-recipe-stop", UserID: 1, PlanID: "plan:current", RequireMediaAuthorization: true},
			}}
			handler.SessionRecipeStore = recipes

			if err := handler.stopPlaybackSessionByID(context.Background(), "session-recipe-stop", test.userInitiated); err != nil {
				t.Fatalf("stop: %v", err)
			}
			if deleted := len(recipes.deleted) == 1 && recipes.deleted[0] == "session-recipe-stop"; deleted != test.wantDeleted {
				t.Fatalf("session recipes deleted = %v, want deleted %v", recipes.deleted, test.wantDeleted)
			}
		})
	}
}
