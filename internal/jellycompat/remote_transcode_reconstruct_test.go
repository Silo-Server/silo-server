package jellycompat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/nodepool/nodepooltest"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/tonemap"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

// stubRecipeNodeStore is an in-memory stand-in for the control-plane recipe
// store (*noderecipe.Store) so the round-trip tests can assert what central
// wrote and let the "node" read it back without Redis.
type stubRecipeNodeStore struct {
	mu         sync.Mutex
	cards      map[string]playback.RecipeCard
	putErr     error
	operations []string
}

func (s *stubRecipeNodeStore) Put(_ context.Context, sessionID string, card playback.RecipeCard) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.operations = append(s.operations, "put:"+sessionID)
	if s.putErr != nil {
		return s.putErr
	}
	if s.cards == nil {
		s.cards = make(map[string]playback.RecipeCard)
	}
	s.cards[sessionID] = card
	return nil
}

func TestStartRemoteTranscodeRequiresDurableNodeRecipe(t *testing.T) {
	recipeStore := &stubRecipeNodeStore{putErr: context.DeadlineExceeded}
	node := fakeTranscodeNode(t, nil)
	handler, _, playbackStore := newRemoteTranscodeHandler(t, node.URL, recipeStore)
	playbackStore.Put(PlaybackSession{ID: "play-1", UpstreamSessionID: "upstream-1"})

	err := handler.startRemoteTranscode(context.Background(), "play-1", "upstream-1", testRemoteTranscodeSource(), &models.MediaFile{ID: 42, FilePath: "/media/movie.mkv"}, 0, node.URL)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("startRemoteTranscode() error = %v, want recipe-store deadline", err)
	}
	stored, ok := playbackStore.Get("play-1")
	if !ok {
		t.Fatal("playback session missing")
	}
	if stored.TranscodeStarted || stored.Recipe != nil {
		t.Fatalf("failed durable commit published transcode state: %+v", stored)
	}
}

func TestStartRemoteTranscodeRedactsInvalidNodeURL(t *testing.T) {
	handler, _, _ := newRemoteTranscodeHandler(t, "http://node.invalid", &stubRecipeNodeStore{})
	err := handler.startRemoteTranscode(
		context.Background(), "play-1", "upstream-1", testRemoteTranscodeSource(),
		&models.MediaFile{ID: 42, FilePath: "/media/movie.mkv"}, 0,
		"http://user:super-secret@%zz",
	)
	if err == nil {
		t.Fatal("startRemoteTranscode() error = nil")
	}
	if strings.Contains(err.Error(), "user") || strings.Contains(err.Error(), "super-secret") {
		t.Fatalf("startRemoteTranscode() leaked credentials: %v", err)
	}
}

func (s *stubRecipeNodeStore) Get(sessionID string) (playback.RecipeCard, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	card, ok := s.cards[sessionID]
	return card, ok
}

func (s *stubRecipeNodeStore) Delete(_ context.Context, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cards, sessionID)
	s.operations = append(s.operations, "delete:"+sessionID)
	return nil
}

func (s *stubRecipeNodeStore) Operations() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.operations...)
}

type failLocalRecipeStore struct {
	CompatPlaybackStore
}

func (s failLocalRecipeStore) Update(id string, fn func(*PlaybackSession) error) error {
	return s.CompatPlaybackStore.Update(id, func(session *PlaybackSession) error {
		if err := fn(session); err != nil {
			return err
		}
		if session.Recipe != nil && session.Recipe.TranscodeNodeURL == "" {
			return context.DeadlineExceeded
		}
		return nil
	})
}

func TestPersistLocalTranscodeStopsReplacedRemoteAfterCommit(t *testing.T) {
	baseStore := NewPlaybackSessionStore(time.Hour, nil)
	var recipeAtStop atomic.Pointer[playback.RecipeCard]
	remoteStopped := make(chan struct{}, 1)
	remoteNode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == "/transcode/upstream-1" {
			if stored, ok := baseStore.Get("play-1"); ok {
				recipeAtStop.Store(stored.Recipe)
			}
			remoteStopped <- struct{}{}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(remoteNode.Close)

	oldRecipe := playback.NewRecipeCard(7, "profile-1", 42, remoteNode.URL, playback.TranscodeOpts{
		SessionID: "upstream-1", InputPath: "/media/movie.mkv", TargetCodecVideo: "h264", TargetCodecAudio: "aac",
		SegmentDuration: 2,
	})
	baseStore.Put(PlaybackSession{
		ID: "play-1", UpstreamSessionID: "upstream-1", TranscodeStarted: true, Recipe: &oldRecipe,
	})
	recipeStore := &stubRecipeNodeStore{cards: map[string]playback.RecipeCard{"upstream-1": oldRecipe}}
	handler := &PlaybackHandler{
		playbackStore: baseStore,
		sessionMgr: &lockedCompatSessionManager{session: playback.Session{
			ID: "upstream-1", UserID: 7, ProfileID: "profile-1", MediaFileID: 42,
			PlayMethod: playback.PlayTranscode, BasePlayMethod: playback.PlayTranscode,
		}},
		tm:              keyedTranscodeManager(),
		JWTSecret:       "test-secret",
		NodeKeys:        nodeauthtest.SameKey("test-secret"),
		RecipeNodeStore: recipeStore,
	}

	if err := handler.persistTranscodeRecipe(context.Background(), "play-1", "upstream-1", playback.TranscodeOpts{
		SessionID: "upstream-1", InputPath: "/media/movie.mkv", TargetCodecVideo: "h264", TargetCodecAudio: "aac",
		SegmentDuration: 2, HWAccel: playback.HWAccelNone,
	}); err != nil {
		t.Fatalf("persist local recipe: %v", err)
	}
	select {
	case <-remoteStopped:
	default:
		t.Fatal("replaced remote runtime was not stopped after the local recipe committed")
	}
	if got := recipeAtStop.Load(); got == nil || got.TranscodeNodeURL != "" {
		t.Fatalf("central recipe when the remote stop arrived = %+v, want the committed local recipe", got)
	}
}

type lockedCompatSessionManager struct {
	mu      sync.Mutex
	session playback.Session
}

func (m *lockedCompatSessionManager) StartSession(int, string, int, playback.PlayMethod, bool) (*playback.Session, error) {
	return nil, errors.New("unexpected StartSession")
}
func (m *lockedCompatSessionManager) UpdateProgress(string, float64, bool) error { return nil }
func (m *lockedCompatSessionManager) UpdateAudioTrack(string, int, playback.PlayMethod) error {
	return nil
}
func (m *lockedCompatSessionManager) StopSession(string) error    { return nil }
func (m *lockedCompatSessionManager) BeginTransport(string) error { return nil }
func (m *lockedCompatSessionManager) EndTransport(string) error   { return nil }

func (m *lockedCompatSessionManager) GetSession(string) (*playback.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	copy := m.session
	return &copy, nil
}

func (m *lockedCompatSessionManager) SetTranscodeNodeURL(_ string, nodeURL string) error {
	m.mu.Lock()
	m.session.TranscodeNodeURL = nodeURL
	m.mu.Unlock()
	return nil
}

func (m *lockedCompatSessionManager) SetTranscodeStreamDetails(
	_ string,
	targetVideoCodec, targetAudioCodec string,
	transcodeAudio bool,
	hwAccel string,
	mode tonemap.Mode,
	targetResolution string,
	targetBitrateKbps int,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.session.TargetVideoCodec = targetVideoCodec
	m.session.TargetAudioCodec = targetAudioCodec
	m.session.TranscodeAudio = transcodeAudio
	m.session.TranscodeHWAccel = hwAccel
	m.session.ToneMapMode = mode
	m.session.TargetResolution = targetResolution
	m.session.TargetBitrateKbps = targetBitrateKbps
	return nil
}

func TestConcurrentRemoteStartsPublishOneTrackedRoute(t *testing.T) {
	firstArrived := make(chan struct{})
	releaseFirst := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseFirst) }) })
	var starts atomic.Int32
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/transcode/start" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if starts.Add(1) == 1 {
			close(firstArrived)
			<-releaseFirst
		}
		writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{HWAccel: tonemap.BackendQSV, RequireAudioTrack: true})
	}))
	t.Cleanup(node.Close)

	handler, _, store := newRemoteTranscodeHandler(t, node.URL, &stubRecipeNodeStore{})
	store.Put(PlaybackSession{ID: "play-1", UpstreamSessionID: "upstream-1"})
	start := func() error {
		return handler.startRemoteTranscode(context.Background(), "play-1", "upstream-1", testRemoteTranscodeSource(), &models.MediaFile{ID: 42, FilePath: "/media/movie.mkv"}, 0, node.URL)
	}
	firstResult := make(chan error, 1)
	secondResult := make(chan error, 1)
	go func() { firstResult <- start() }()
	select {
	case <-firstArrived:
	case <-time.After(2 * time.Second):
		t.Fatal("first remote start did not reach node")
	}
	go func() { secondResult <- start() }()
	time.Sleep(50 * time.Millisecond)
	startsBeforePublication := starts.Load()
	releaseOnce.Do(func() { close(releaseFirst) })
	if err := <-firstResult; err != nil {
		t.Fatalf("publishing remote start: %v", err)
	}
	if err := <-secondResult; !errors.Is(err, errRemoteStartAdoptedRemote) {
		t.Fatalf("waiting remote start error = %v, want adopted remote", err)
	}
	if startsBeforePublication != 1 {
		t.Fatalf("concurrent remote starts reached node %d times before publication, want 1", startsBeforePublication)
	}
	if got := starts.Load(); got != 1 {
		t.Fatalf("node starts = %d, want one tracked remote runtime", got)
	}
}

// TestMasterManifestGatesUnhealthyRemoteAdoption verifies the adoption redirect
// only fires while the winner's node is still healthy: a recipe another API
// server published is re-checked against the local pool before the client is
// sent to it.
func TestMasterManifestGatesUnhealthyRemoteAdoption(t *testing.T) {
	adoptedNode := httptest.NewServer(http.NotFoundHandler())
	defer adoptedNode.Close()
	adoptedURL := adoptedNode.URL
	const healthyURL = "http://healthy.invalid"
	newPool := func(adoptedHealthy bool) *nodepool.Planner {
		transcodes := nodepooltest.NewTranscodePool()
		transcodes.SetNodes([]*nodepool.Node{
			{URL: adoptedURL, Enabled: true, Healthy: adoptedHealthy},
			{URL: healthyURL, Enabled: true, Healthy: true},
		})
		proxies := nodepool.NewProxyPool()
		proxies.SetNodes([]*nodepool.Node{{URL: "https://proxy.example", Enabled: true, Healthy: true}})
		return nodepool.NewPlanner(proxies, transcodes)
	}

	tests := []struct {
		name            string
		adoptedHealthy  bool
		wantStatus      int
		wantRedirectURL string
	}{
		{name: "healthy winner adopts", adoptedHealthy: true, wantStatus: http.StatusTemporaryRedirect, wantRedirectURL: "https://proxy.example/stream/transcode/"},
		{name: "unhealthy winner is rejected", adoptedHealthy: false, wantStatus: http.StatusBadGateway},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, _, playbackStore := newRemoteTranscodeHandler(t, adoptedURL, &stubRecipeNodeStore{})
			handler.NodePlanner = newPool(tt.adoptedHealthy)
			requireCompatWorkerRouting(handler)
			source := testRemoteTranscodeSource()
			playbackStore.Put(PlaybackSession{
				ID: "play-1", CompatToken: "compat-token", RouteItemID: "item",
				MediaSources:      []PlaybackMediaSource{source},
				UpstreamSessionID: "upstream-1", UpstreamPlayMethod: "transcode",
				TranscodeStarted: true,
				Recipe: &playback.RecipeCard{
					TranscodeNodeURL:    adoptedURL,
					MediaFileID:         source.FileID,
					AudioTrackIndex:     compatAudioTrackIndexOrDefault(source),
					RequireAudioTrack:   true,
					SourceAudioChannels: compatSourceAudioChannels(source),
				},
			})
			request := httptest.NewRequest(http.MethodGet, "/Videos/item/master.m3u8?PlaySessionId=play-1&MediaSourceId="+source.ID, nil)
			request = request.WithContext(context.WithValue(request.Context(), compatSessionKey, &Session{Token: "compat-token", StreamAppUserID: 7, ProfileID: "profile-1"}))
			recorder := httptest.NewRecorder()
			handler.HandleMasterManifest(recorder, request)
			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", recorder.Code, tt.wantStatus, recorder.Body.String())
			}
			if tt.wantRedirectURL != "" {
				location := recorder.Header().Get("Location")
				if !strings.HasPrefix(location, tt.wantRedirectURL) {
					t.Fatalf("redirect = %q, want prefix %q", location, tt.wantRedirectURL)
				}
			} else if _, ok := playbackStore.Get("play-1"); ok {
				t.Fatal("rejected adoption left the published session in place")
			}
		})
	}
}

// newRemoteTranscodeHandler builds a handler wired for the remote (offloaded)
// transcode path: a fake node that accepts /transcode/start, an upstream
// session carrying the native identity used to build the recipe, and a stub
// recipe store standing in for the control-plane Redis store.
func newRemoteTranscodeHandler(t *testing.T, nodeURL string, recipeStore *stubRecipeNodeStore) (*PlaybackHandler, *testCompatSessionManager, *PlaybackSessionStore) {
	t.Helper()

	playbackStore := NewPlaybackSessionStore(time.Hour, nil)
	sessionMgr := &testCompatSessionManager{
		sessions: map[string]*playback.Session{
			"upstream-1": {
				ID:               "upstream-1",
				UserID:           7,
				ProfileID:        "profile-1",
				MediaFileID:      42,
				PlayMethod:       playback.PlayTranscode,
				BasePlayMethod:   playback.PlayTranscode,
				TranscodeNodeURL: nodeURL,
			},
		},
	}
	handler := &PlaybackHandler{
		playbackStore:   playbackStore,
		sessionMgr:      sessionMgr,
		fileResolver:    testCompatFileResolver{file: &models.MediaFile{ID: 42, FilePath: "/media/movie.mkv"}},
		tm:              keyedTranscodeManager(),
		JWTSecret:       "test-secret",
		NodeKeys:        nodeauthtest.SameKey("test-secret"),
		RecipeNodeStore: recipeStore,
	}
	return handler, sessionMgr, playbackStore
}

// fakeTranscodeNode returns an httptest server that accepts /transcode/start
// with 202 Accepted (the only response the start path inspects) and records
// the request body it received.
func fakeTranscodeNode(t *testing.T, received *transcodenode.TranscodeStartRequest) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/transcode/start" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var request transcodenode.TranscodeStartRequest
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &request)
		if received != nil {
			*received = request
		}
		writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{
			SessionID: request.SessionID, Status: "started",
			AudioRecipeVersion:    request.AudioRecipeVersion,
			RequireAudioTrack:     request.RequireAudioTrack,
			CopyFMP4RecipeVersion: request.CopyFMP4RecipeVersion,
			ThrottleSeconds:       request.ThrottleSeconds,
			CopyAudioCodec:        request.CopyAudioCodec,
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testRemoteTranscodeSource() PlaybackMediaSource {
	codec := NewResourceIDCodec()
	version := testCompatVersion()
	source := testCompatSource(codec, version)
	return source
}

// TestStartRemoteToneMapReportsConfirmedExecutorAndFallback verifies remote fallback facts remain accurate.
func TestStartRemoteToneMapReportsConfirmedExecutorAndFallback(t *testing.T) {
	tests := []struct {
		name                 string
		rejectHardwareStatus int
		rejectSoftware       bool
		wantDispatches       int32
		wantHWAccel          string
		wantMode             tonemap.Mode
		wantErrParts         []string
	}{
		{name: "hardware", wantDispatches: 1, wantHWAccel: tonemap.BackendQSV, wantMode: tonemap.ModeHardware},
		{name: "unprocessable software fallback", rejectHardwareStatus: http.StatusUnprocessableEntity, wantDispatches: 2, wantHWAccel: playback.HWAccelNone, wantMode: tonemap.ModeSoftware},
		{name: "not implemented software fallback", rejectHardwareStatus: http.StatusNotImplemented, wantDispatches: 2, wantHWAccel: playback.HWAccelNone, wantMode: tonemap.ModeSoftware},
		{name: "software fallback rejected", rejectHardwareStatus: http.StatusUnprocessableEntity, rejectSoftware: true, wantDispatches: 2, wantErrParts: []string{"initial status 422", "retry status 503"}},
		{name: "authentication rejection is not retried", rejectHardwareStatus: http.StatusUnauthorized, wantDispatches: 1, wantErrParts: []string{"status 401"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var dispatches atomic.Int32
			capabilities := tonemap.Capabilities{
				{Mode: tonemap.ModeHardware, Backend: tonemap.BackendQSV, Filter: tonemap.HardwareFilterOpenCL, SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ}},
				{Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterBT2390, SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ}},
			}
			node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/hw-capabilities":
					writeJSON(w, http.StatusOK, playback.HWAccelInfo{ToneMapCapabilities: capabilities})
				case "/transcode/start":
					dispatches.Add(1)
					var request transcodenode.TranscodeStartRequest
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Errorf("decode start request: %v", err)
						http.Error(w, "invalid transcode start request", http.StatusBadRequest)
						return
					}
					if test.rejectHardwareStatus != 0 && request.ToneMapMode == tonemap.ModeHardware {
						w.WriteHeader(test.rejectHardwareStatus)
						return
					}
					if test.rejectSoftware && request.ToneMapMode == tonemap.ModeSoftware {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusAccepted)
					_ = json.NewEncoder(w).Encode(transcodenode.TranscodeStartResponse{RequireAudioTrack: request.RequireAudioTrack, HWAccel: request.HWAccel, ToneMapMode: request.ToneMapMode, ThrottleSeconds: request.ThrottleSeconds})
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(node.Close)

			recipeStore := &stubRecipeNodeStore{}
			handler, sessionMgr, playbackStore := newRemoteTranscodeHandler(t, node.URL, recipeStore)
			handler.SettingsRepo = stubSettingsReader{values: map[string]string{
				config.PlaybackTranscodeHardwareToneMapSettingKey: "true",
				config.PlaybackTranscodeSoftwareToneMapSettingKey: "true",
			}}
			playbackStore.Put(PlaybackSession{ID: "play-1", UpstreamSessionID: "upstream-1"})
			file := &models.MediaFile{ID: 42, FilePath: "/media/movie.mkv", HDR: true, VideoTracks: []models.VideoTrack{{Codec: "hevc", VideoRangeType: "HDR10", ColorTransfer: "smpte2084", ColorPrimaries: "bt2020", ColorSpace: "bt2020nc", BitDepth: 10}}}

			err := handler.startRemoteTranscode(context.Background(), "play-1", "upstream-1", testRemoteTranscodeSource(), file, 0, node.URL)
			if got := dispatches.Load(); got != test.wantDispatches {
				t.Fatalf("start dispatches = %d, want %d", got, test.wantDispatches)
			}
			if len(test.wantErrParts) > 0 {
				if err == nil {
					t.Fatal("startRemoteTranscode succeeded after the node rejected the request")
				}
				for _, want := range test.wantErrParts {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("startRemoteTranscode error = %q, want %q", err, want)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("startRemoteTranscode: %v", err)
			}

			session := sessionMgr.sessions["upstream-1"]
			if session.TranscodeHWAccel != test.wantHWAccel || session.ToneMapMode != test.wantMode {
				t.Fatalf("reported execution facts = hw %q tone_map %q, want %q %q", session.TranscodeHWAccel, session.ToneMapMode, test.wantHWAccel, test.wantMode)
			}
			card, ok := recipeStore.Get("upstream-1")
			if !ok || card.HWAccel != test.wantHWAccel || card.ToneMapMode != test.wantMode {
				t.Fatalf("persisted execution facts = hw %q tone_map %q, found=%v", card.HWAccel, card.ToneMapMode, ok)
			}
		})
	}
}

func TestStartRemoteToneMapTimeoutFallsBackToSoftwareAfterCleanup(t *testing.T) {
	previousTimeout := compatRemoteTranscodeStartTimeout
	compatRemoteTranscodeStartTimeout = 200 * time.Millisecond
	t.Cleanup(func() { compatRemoteTranscodeStartTimeout = previousTimeout })

	var dispatches atomic.Int32
	var cleaned atomic.Bool
	capabilities := tonemap.Capabilities{
		{Mode: tonemap.ModeHardware, Backend: tonemap.BackendQSV, Filter: tonemap.HardwareFilterOpenCL, SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ}},
		{Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterBT2390, SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ}},
	}
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/hw-capabilities":
			writeJSON(w, http.StatusOK, playback.HWAccelInfo{ToneMapCapabilities: capabilities})
		case r.Method == http.MethodPost && r.URL.Path == "/transcode/start":
			dispatches.Add(1)
			var request transcodenode.TranscodeStartRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode start request: %v", err)
				return
			}
			if request.ToneMapMode == tonemap.ModeHardware {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusAccepted)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
				return
			}
			if !cleaned.Load() {
				t.Error("software retry started before the indeterminate hardware session was cleaned up")
			}
			writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{RequireAudioTrack: request.RequireAudioTrack, HWAccel: request.HWAccel, ToneMapMode: request.ToneMapMode, ThrottleSeconds: request.ThrottleSeconds})
		case r.Method == http.MethodDelete && r.URL.Path == "/transcode/upstream-1":
			cleaned.Store(true)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(node.Close)

	recipeStore := &stubRecipeNodeStore{}
	handler, sessionMgr, playbackStore := newRemoteTranscodeHandler(t, node.URL, recipeStore)
	handler.SettingsRepo = stubSettingsReader{values: map[string]string{
		config.PlaybackTranscodeHardwareToneMapSettingKey: "true",
		config.PlaybackTranscodeSoftwareToneMapSettingKey: "true",
	}}
	playbackStore.Put(PlaybackSession{ID: "play-1", UpstreamSessionID: "upstream-1"})
	file := &models.MediaFile{ID: 42, FilePath: "/media/movie.mkv", HDR: true, VideoTracks: []models.VideoTrack{{Codec: "hevc", VideoRangeType: "HDR10", ColorTransfer: "smpte2084", ColorPrimaries: "bt2020", ColorSpace: "bt2020nc", BitDepth: 10}}}

	if err := handler.startRemoteTranscode(context.Background(), "play-1", "upstream-1", testRemoteTranscodeSource(), file, 0, node.URL); err != nil {
		t.Fatalf("startRemoteTranscode: %v", err)
	}
	if got := dispatches.Load(); got != 2 {
		t.Fatalf("start dispatches = %d, want hardware attempt plus software retry", got)
	}
	if !cleaned.Load() {
		t.Fatal("indeterminate hardware session was not cleaned up")
	}
	session := sessionMgr.sessions["upstream-1"]
	if session.TranscodeHWAccel != playback.HWAccelNone || session.ToneMapMode != tonemap.ModeSoftware {
		t.Fatalf("reported execution facts = hw %q tone_map %q, want software", session.TranscodeHWAccel, session.ToneMapMode)
	}
	card, ok := recipeStore.Get("upstream-1")
	if !ok || card.HWAccel != playback.HWAccelNone || card.ToneMapMode != tonemap.ModeSoftware {
		t.Fatalf("persisted execution facts = hw %q tone_map %q, found=%v", card.HWAccel, card.ToneMapMode, ok)
	}
}

func TestStartRemoteToneMapConfirmationMismatchRollsBackNode(t *testing.T) {
	deleted := make(chan string, 1)
	capabilities := tonemap.Capabilities{{
		Mode: tonemap.ModeHardware, Backend: tonemap.BackendQSV, Filter: tonemap.HardwareFilterOpenCL,
		SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ},
	}}
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/hw-capabilities":
			writeJSON(w, http.StatusOK, playback.HWAccelInfo{ToneMapCapabilities: capabilities})
		case r.Method == http.MethodPost && r.URL.Path == "/transcode/start":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodDelete:
			deleted <- r.URL.Path
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(node.Close)

	handler, _, playbackStore := newRemoteTranscodeHandler(t, node.URL, &stubRecipeNodeStore{})
	handler.SettingsRepo = stubSettingsReader{values: map[string]string{
		config.PlaybackTranscodeHardwareToneMapSettingKey: "true",
	}}
	playbackStore.Put(PlaybackSession{ID: "play-1", UpstreamSessionID: "upstream-1"})
	file := &models.MediaFile{ID: 42, FilePath: "/media/movie.mkv", HDR: true, VideoTracks: []models.VideoTrack{{Codec: "hevc", VideoRangeType: "HDR10", ColorTransfer: "smpte2084", ColorPrimaries: "bt2020", ColorSpace: "bt2020nc", BitDepth: 10}}}

	err := handler.startRemoteTranscode(context.Background(), "play-1", "upstream-1", testRemoteTranscodeSource(), file, 0, node.URL)
	if err == nil || !strings.Contains(err.Error(), "did not confirm tone-map mode") {
		t.Fatalf("startRemoteTranscode error = %v, want confirmation mismatch", err)
	}
	select {
	case path := <-deleted:
		if path != "/transcode/upstream-1" {
			t.Fatalf("rollback DELETE path = %q, want /transcode/upstream-1", path)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected rollback DELETE after tone-map confirmation mismatch")
	}
}

func TestStartRemoteTranscodeIndeterminateAcceptedResponseRollsBackNode(t *testing.T) {
	for _, test := range []struct {
		name string
		body func(http.ResponseWriter, *http.Request)
	}{
		{
			name: "malformed response",
			body: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(`{"status":`))
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			previousTimeout := compatRemoteTranscodeStartTimeout
			compatRemoteTranscodeStartTimeout = 200 * time.Millisecond
			t.Cleanup(func() { compatRemoteTranscodeStartTimeout = previousTimeout })

			deleted := make(chan string, 1)
			node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/transcode/start":
					test.body(w, r)
				case r.Method == http.MethodDelete:
					deleted <- r.URL.Path
					w.WriteHeader(http.StatusNoContent)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(node.Close)

			handler, _, playbackStore := newRemoteTranscodeHandler(t, node.URL, &stubRecipeNodeStore{})
			playbackStore.Put(PlaybackSession{ID: "play-1", UpstreamSessionID: "upstream-1"})
			err := handler.startRemoteTranscode(context.Background(), "play-1", "upstream-1", testRemoteTranscodeSource(), &models.MediaFile{ID: 42, FilePath: "/media/movie.mkv"}, 0, node.URL)
			if err == nil {
				t.Fatal("startRemoteTranscode() error = nil, want indeterminate accepted response failure")
			}
			select {
			case path := <-deleted:
				if path != "/transcode/upstream-1" {
					t.Fatalf("rollback DELETE path = %q, want /transcode/upstream-1", path)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("expected rollback DELETE after indeterminate accepted response")
			}
		})
	}
}

// TestStartRemoteTranscode_UpdateFailureRollsBackNode asserts the local-path
// rollback is mirrored: a compat-store Update failure closes the already-started
// node ffmpeg (DELETE /transcode/{id}) so it isn't leaked.
func TestStartRemoteTranscode_UpdateFailureRollsBackNode(t *testing.T) {
	recipeStore := &stubRecipeNodeStore{}
	deleted := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/transcode/start":
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodDelete:
			deleted <- r.URL.Path
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	handler, _, _ := newRemoteTranscodeHandler(t, srv.URL, recipeStore)
	// No play session put → Update("missing") fails, triggering rollback.
	source := testRemoteTranscodeSource()
	err := handler.startRemoteTranscode(context.Background(), "missing", "upstream-1", source, &models.MediaFile{ID: 42, FilePath: "/media/movie.mkv"}, 0, srv.URL)
	if err == nil {
		t.Fatal("expected error when compat Update fails")
	}

	select {
	case path := <-deleted:
		if path != "/transcode/upstream-1" {
			t.Fatalf("rollback DELETE path = %q, want /transcode/upstream-1", path)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected rollback DELETE to the node after Update failure")
	}
}

// keyedTranscodeManager is a transcode manager that signs its DELETEs to
// nodes, as the handler's constructor wires it.
func keyedTranscodeManager() *playback.TranscodeManager {
	tm := playback.NewTranscodeManager()
	tm.NodeKeysFn = func() nodeauth.Keyring { return nodeauthtest.SameKey("test-secret") }
	return tm
}

func TestStartRemoteTranscode_NodeRestartReconstruct(t *testing.T) {
	recipeStore := &stubRecipeNodeStore{}
	node := fakeTranscodeNode(t, nil)
	handler, _, playbackStore := newRemoteTranscodeHandler(t, node.URL, recipeStore)

	playbackStore.Put(PlaybackSession{
		ID:                 "play-1",
		CompatToken:        "token-1",
		UpstreamSessionID:  "upstream-1",
		UpstreamPlayMethod: "transcode",
		MediaSources:       []PlaybackMediaSource{testRemoteTranscodeSource()},
	})

	source := testRemoteTranscodeSource()
	err := handler.startRemoteTranscode(
		context.Background(),
		"play-1",
		"upstream-1",
		source,
		&models.MediaFile{ID: 42, FilePath: "/media/movie.mkv"},
		0,
		node.URL,
	)
	if err != nil {
		t.Fatalf("startRemoteTranscode: %v", err)
	}

	// Node side: it lost its session map; reconstruct reads the recipe central
	// wrote keyed by the upstream session id.
	card, ok := recipeStore.Get("upstream-1")
	if !ok {
		t.Fatal("expected recipe in control-plane store after remote start")
	}
	if card.SessionID != "upstream-1" {
		t.Fatalf("recipe SessionID = %q, want upstream-1", card.SessionID)
	}
	if card.TranscodeNodeURL != node.URL {
		t.Fatalf("recipe TranscodeNodeURL = %q, want %q", card.TranscodeNodeURL, node.URL)
	}
	// The recipe must be reconstruct-complete: the node refuses to rebuild from a
	// recipe lacking encode parameters (server.go reconstructFromToken).
	if card.SegmentDuration <= 0 || card.TargetCodecVideo == "" {
		t.Fatalf("recipe not reconstruct-complete: seg=%d video=%q", card.SegmentDuration, card.TargetCodecVideo)
	}
	if card.UserID != 7 || card.ProfileID != "profile-1" || card.MediaFileID != 42 {
		t.Fatalf("recipe identity wrong: user=%d profile=%q file=%d", card.UserID, card.ProfileID, card.MediaFileID)
	}
	if card.InputPath != "/media/movie.mkv" {
		t.Fatalf("recipe InputPath = %q, want /media/movie.mkv", card.InputPath)
	}
}

func TestRemoteTranscodeStartTimeoutCoversColdProbePreflightAndReadiness(t *testing.T) {
	handler := &PlaybackHandler{HWAccel: tonemap.BackendQSV}
	request := transcodenode.TranscodeStartRequest{
		ToneMapMode:              tonemap.ModeHardware,
		ToneMapPreflightRequired: true,
		TotalDuration:            100,
		RequireReady:             true,
	}
	want := 137*time.Second + playback.ManifestStartupTimeout + tonemap.SourcePreflightTimeout(100) + transcodenode.TranscodeStartReadinessTimeout
	if got := handler.remoteTranscodeStartTimeout(request, (137 * time.Second).Milliseconds()); got != want {
		t.Fatalf("remote transcode start timeout = %v, want %v", got, want)
	}
	// Still bounded — the advertisement comes off the wire from a worker — but
	// at the ceiling the probe formula produces, not a round number a real
	// nine-device node already exceeds.
	maxWant := playback.MaxCapabilityRequestTimeout() + playback.ManifestStartupTimeout + tonemap.SourcePreflightTimeout(100) + transcodenode.TranscodeStartReadinessTimeout
	if got := handler.remoteTranscodeStartTimeout(request, (24 * time.Hour).Milliseconds()); got != maxWant {
		t.Fatalf("bounded remote transcode start timeout = %v, want %v", got, maxWant)
	}
	// Without an advertisement, the local hardware policy can price a cold
	// probe above the fixed fallback, which is a floor rather than a ceiling.
	coldProbeBudget := playback.ColdCapabilityRequestTimeout(nil, handler.HWAccel, "", compatRemoteNodeProbeFallbackTimeout)
	fallbackWant := coldProbeBudget + playback.ManifestStartupTimeout + tonemap.SourcePreflightTimeout(100) + transcodenode.TranscodeStartReadinessTimeout
	if got := handler.remoteTranscodeStartTimeout(request, 0); got != fallbackWant {
		t.Fatalf("missing-budget remote transcode start timeout = %v, want %v", got, fallbackWant)
	}
}

func TestStartRemoteTranscodeDoesNotAdoptMismatchedAudioRecipe(t *testing.T) {
	tests := []struct {
		name               string
		oldMediaFileID     int
		oldAudioTrackIndex int
		oldSourceChannels  int
		oldOptionalAudio   bool
	}{
		{name: "media file changed with same audio facts", oldMediaFileID: 41, oldAudioTrackIndex: 1, oldSourceChannels: 0},
		{name: "selected track changed channel layout", oldMediaFileID: 42, oldAudioTrackIndex: 1, oldSourceChannels: 6},
		{name: "selected track changed at same channel layout", oldMediaFileID: 42, oldAudioTrackIndex: 0, oldSourceChannels: 0},
		{name: "legacy optional audio recipe", oldMediaFileID: 42, oldAudioTrackIndex: 1, oldOptionalAudio: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var received transcodenode.TranscodeStartRequest
			node := fakeTranscodeNode(t, &received)
			handler, _, playbackStore := newRemoteTranscodeHandler(t, node.URL, &stubRecipeNodeStore{})
			oldRecipe := playback.NewRecipeCard(7, "profile-1", tt.oldMediaFileID, node.URL, playback.TranscodeOpts{
				SessionID:           "upstream-1",
				InputPath:           "/media/movie.mkv",
				TargetCodecVideo:    compatTargetVideoCodec,
				TargetCodecAudio:    compatTargetAudioCodec,
				SegmentDuration:     compatSegmentDuration,
				AudioTrackIndex:     tt.oldAudioTrackIndex,
				RequireAudioTrack:   !tt.oldOptionalAudio,
				SourceAudioChannels: tt.oldSourceChannels,
			})
			playbackStore.Put(PlaybackSession{
				ID: "play-1", UpstreamSessionID: "upstream-1", TranscodeStarted: true, Recipe: &oldRecipe,
			})

			err := handler.startRemoteTranscode(
				context.Background(), "play-1", "upstream-1", testRemoteTranscodeSource(),
				&models.MediaFile{ID: 42, FilePath: "/media/movie.mkv"}, 0, node.URL,
			)
			if err != nil {
				t.Fatalf("startRemoteTranscode: %v", err)
			}
			if received.AudioTrackIndex != 1 || received.SourceAudioChannels != 0 || !received.RequireAudioTrack {
				t.Fatalf("replacement request = track %d, channels %d; want track 1, channels 0", received.AudioTrackIndex, received.SourceAudioChannels)
			}
			stored, ok := playbackStore.Get("play-1")
			if !ok || stored.Recipe == nil || stored.Recipe.AudioTrackIndex != 1 || stored.Recipe.SourceAudioChannels != 0 || !stored.Recipe.RequireAudioTrack {
				t.Fatalf("replacement recipe = %+v, found=%v; want track 1, channels 0", stored.Recipe, ok)
			}
		})
	}
}
