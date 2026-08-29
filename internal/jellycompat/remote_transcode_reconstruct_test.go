package jellycompat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	restoreErr error
	deleted    bool
	operations []string
}

func (s *stubRecipeNodeStore) Put(_ context.Context, sessionID string, card playback.RecipeCard) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.operations = append(s.operations, "put:"+sessionID)
	if s.putErr != nil {
		return s.putErr
	}
	if s.deleted && s.restoreErr != nil {
		return s.restoreErr
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
	s.deleted = true
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

func TestEnsureLocalTranscodeDeletesRemoteRecipeAndRestoresItWhenCentralUpdateFails(t *testing.T) {
	previousTimeout := compatManifestStartupTimeout
	compatManifestStartupTimeout = time.Second
	t.Cleanup(func() { compatManifestStartupTimeout = previousTimeout })

	remoteStopped := make(chan struct{}, 1)
	remoteNode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == "/transcode/upstream-1" {
			remoteStopped <- struct{}{}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(remoteNode.Close)

	handler, _, sessionMgr, baseStore, source := newManifestToneMapFailoverHandler(t, remoteNode.URL, true)
	software := tonemap.Capability{
		Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterBT2390,
		SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ},
	}
	handler.compatToneMapProbe = func(context.Context, string, string, string) (tonemap.Capabilities, error) {
		return tonemap.Capabilities{software}, nil
	}
	handler.HWAccel = playback.HWAccelNone
	handler.TranscodeDir = t.TempDir()
	handler.FFmpegPath = filepath.Join(t.TempDir(), "ffmpeg")
	ffmpegScript := "#!/bin/sh\n" +
		"out=\"\"\n" +
		"for arg in \"$@\"; do case \"$arg\" in *.m3u8) out=\"$(dirname \"$arg\")\";; esac; done\n" +
		"mkdir -p \"$out\"\n" +
		"for name in seg_00000.m4s seg_00001.m4s seg_00002.m4s; do printf segment > \"$out/$name\"; done\n" +
		"printf '#EXTM3U\\n#EXT-X-TARGETDURATION:2\\n#EXT-X-MEDIA-SEQUENCE:0\\n#EXTINF:2,\\nseg_00000.m4s\\n#EXTINF:2,\\nseg_00001.m4s\\n#EXTINF:2,\\nseg_00002.m4s\\n' > \"$out/stream.m3u8\"\n" +
		"sleep 30\n"
	if err := os.WriteFile(handler.FFmpegPath, []byte(ffmpegScript), 0o755); err != nil {
		t.Fatal(err)
	}
	writeMatchingToneMapFFprobe(t, handler.FFmpegPath, source.Version.VideoTracks[0])

	oldRecipe := playback.NewRecipeCard(7, "profile-1", 42, remoteNode.URL, playback.TranscodeOpts{
		SessionID: "upstream-1", InputPath: "/media/movie.mkv", TargetCodecVideo: "h264", TargetCodecAudio: "aac",
		SegmentDuration: 2, ToneMapMode: tonemap.ModeHardware,
	})
	if err := baseStore.Update("play-1", func(session *PlaybackSession) error {
		session.TranscodeStarted = true
		session.Recipe = &oldRecipe
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	recipeStore := &stubRecipeNodeStore{cards: map[string]playback.RecipeCard{"upstream-1": oldRecipe}}
	handler.RecipeNodeStore = recipeStore
	handler.playbackStore = failLocalRecipeStore{CompatPlaybackStore: baseStore}
	if err := sessionMgr.SetTranscodeNodeURL("upstream-1", ""); err != nil {
		t.Fatal(err)
	}

	_, err := handler.ensureTranscodeSessionWithToneMapMode(
		context.Background(), "play-1", "upstream-1", source, tonemap.ModeSoftware,
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ensure local transcode error = %v, want central update failure", err)
	}
	select {
	case <-remoteStopped:
	case <-time.After(2 * time.Second):
		t.Fatal("remote runtime was not stopped during local replacement")
	}
	if got := recipeStore.Operations(); len(got) != 2 || got[0] != "delete:upstream-1" || got[1] != "put:upstream-1" {
		t.Fatalf("node recipe operations = %v, want delete followed by rollback put", got)
	}
	restored, ok := recipeStore.Get("upstream-1")
	if !ok || restored.TranscodeNodeURL != remoteNode.URL || restored.ToneMapMode != tonemap.ModeHardware {
		t.Fatalf("restored node recipe = %+v, found=%v, want prior remote recipe", restored, ok)
	}
	stored, ok := baseStore.Get("play-1")
	if !ok || stored.Recipe == nil || stored.Recipe.TranscodeNodeURL != remoteNode.URL {
		t.Fatalf("central recipe = %+v, found=%v, want prior remote recipe", stored.Recipe, ok)
	}
}

func TestPersistLocalTranscodeReportsFailedRemoteRecipeRestore(t *testing.T) {
	restoreErr := errors.New("restore node recipe failed")
	remoteNode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && r.URL.Path == "/transcode/upstream-1" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(remoteNode.Close)

	baseStore := NewPlaybackSessionStore(time.Hour, nil)
	oldRecipe := playback.NewRecipeCard(7, "profile-1", 42, remoteNode.URL, playback.TranscodeOpts{
		SessionID: "upstream-1", InputPath: "/media/movie.mkv", TargetCodecVideo: "h264", TargetCodecAudio: "aac",
		SegmentDuration: 2, ToneMapMode: tonemap.ModeHardware,
	})
	baseStore.Put(PlaybackSession{
		ID: "play-1", UpstreamSessionID: "upstream-1", TranscodeStarted: true, Recipe: &oldRecipe,
	})
	recipeStore := &stubRecipeNodeStore{
		cards:      map[string]playback.RecipeCard{"upstream-1": oldRecipe},
		restoreErr: restoreErr,
	}
	sessionMgr := &lockedCompatSessionManager{session: playback.Session{
		ID: "upstream-1", UserID: 7, ProfileID: "profile-1", MediaFileID: 42,
		PlayMethod: playback.PlayTranscode, BasePlayMethod: playback.PlayTranscode,
	}}
	handler := &PlaybackHandler{
		playbackStore:   failLocalRecipeStore{CompatPlaybackStore: baseStore},
		sessionMgr:      sessionMgr,
		tm:              playback.NewTranscodeManager(),
		JWTSecret:       "test-secret",
		RecipeNodeStore: recipeStore,
	}

	err := handler.persistTranscodeRecipe(context.Background(), "play-1", "upstream-1", playback.TranscodeOpts{
		SessionID: "upstream-1", InputPath: "/media/movie.mkv", TargetCodecVideo: "h264", TargetCodecAudio: "aac",
		SegmentDuration: 2, ToneMapMode: tonemap.ModeSoftware, HWAccel: playback.HWAccelNone,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("persist error = %v, want central update failure", err)
	}
	if !errors.Is(err, restoreErr) {
		t.Fatalf("persist error = %v, want observable restore failure", err)
	}
	if got := recipeStore.Operations(); len(got) != 2 || got[0] != "delete:upstream-1" || got[1] != "put:upstream-1" {
		t.Fatalf("node recipe operations = %v, want delete followed by attempted rollback put", got)
	}
	if _, ok := recipeStore.Get("upstream-1"); ok {
		t.Fatal("node recipe unexpectedly present after injected restore failure")
	}
	stored, ok := baseStore.Get("play-1")
	if !ok || stored.Recipe == nil || stored.Recipe.TranscodeNodeURL != remoteNode.URL {
		t.Fatalf("central recipe = %+v, found=%v, want retryable prior remote state", stored.Recipe, ok)
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
) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.session.TargetVideoCodec = targetVideoCodec
	m.session.TargetAudioCodec = targetAudioCodec
	m.session.TranscodeAudio = transcodeAudio
	m.session.TranscodeHWAccel = hwAccel
	m.session.ToneMapMode = mode
	return nil
}

func TestStartRemoteToneMapDelayedSuccessCannotOverwriteLocalSoftwareWinner(t *testing.T) {
	previousTimeout := compatManifestStartupTimeout
	compatManifestStartupTimeout = time.Second
	t.Cleanup(func() { compatManifestStartupTimeout = previousTimeout })

	hardware := tonemap.Capability{
		Mode: tonemap.ModeHardware, Backend: tonemap.BackendQSV, Filter: tonemap.HardwareFilterOpenCL,
		SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ},
	}
	software := tonemap.Capability{
		Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterBT2390,
		SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ},
	}
	remoteStartArrived := make(chan struct{})
	releaseRemoteStart := make(chan struct{})
	var releaseOnce sync.Once
	var arrivedOnce sync.Once
	remoteDeleted := make(chan struct{}, 1)
	remoteNode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/hw-capabilities":
			writeJSON(w, http.StatusOK, playback.HWAccelInfo{ToneMapCapabilities: tonemap.Capabilities{hardware}})
		case r.Method == http.MethodPost && r.URL.Path == "/transcode/start":
			arrivedOnce.Do(func() { close(remoteStartArrived) })
			<-releaseRemoteStart
			writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{
				HWAccel: tonemap.BackendQSV, ToneMapMode: tonemap.ModeHardware,
			})
		case r.Method == http.MethodDelete && r.URL.Path == "/transcode/upstream-1":
			remoteDeleted <- struct{}{}
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(remoteNode.Close)
	// Registered after server cleanup so LIFO cleanup releases a blocked handler
	// before httptest waits for that handler to exit.
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseRemoteStart) }) })

	handler, _, _, _, source := newManifestToneMapFailoverHandler(t, remoteNode.URL, true)
	handler.compatToneMapProbe = func(context.Context, string, string, string) (tonemap.Capabilities, error) {
		return tonemap.Capabilities{software}, nil
	}
	handler.HWAccel = playback.HWAccelNone
	handler.TranscodeDir = t.TempDir()
	handler.FFmpegPath = filepath.Join(t.TempDir(), "ffmpeg")
	ffmpegScript := "#!/bin/sh\n" +
		"out=\"\"\n" +
		"for arg in \"$@\"; do case \"$arg\" in *.m3u8) out=\"$(dirname \"$arg\")\";; esac; done\n" +
		"mkdir -p \"$out\"\n" +
		"for name in seg_00000.m4s seg_00001.m4s seg_00002.m4s; do printf segment > \"$out/$name\"; done\n" +
		"printf '#EXTM3U\\n#EXT-X-TARGETDURATION:2\\n#EXT-X-MEDIA-SEQUENCE:0\\n#EXTINF:2,\\nseg_00000.m4s\\n#EXTINF:2,\\nseg_00001.m4s\\n#EXTINF:2,\\nseg_00002.m4s\\n' > \"$out/stream.m3u8\"\n" +
		"sleep 30\n"
	if err := os.WriteFile(handler.FFmpegPath, []byte(ffmpegScript), 0o755); err != nil {
		t.Fatal(err)
	}
	writeMatchingToneMapFFprobe(t, handler.FFmpegPath, source.Version.VideoTracks[0])
	sessionMgr := &lockedCompatSessionManager{session: playback.Session{
		ID: "upstream-1", UserID: 7, ProfileID: "profile-1", MediaFileID: 42,
		PlayMethod: playback.PlayTranscode, BasePlayMethod: playback.PlayTranscode, TranscodeNodeURL: remoteNode.URL,
	}}
	handler.sessionMgr = sessionMgr
	request := httptest.NewRequest(http.MethodGet, "/Videos/item/master.m3u8?PlaySessionId=play-1&MediaSourceId="+source.ID, nil)
	request = request.WithContext(context.WithValue(request.Context(), compatSessionKey, &Session{Token: "compat-token", StreamAppUserID: 7, ProfileID: "profile-1"}))
	recorder := httptest.NewRecorder()
	remoteResult := make(chan struct{}, 1)
	go func() {
		handler.HandleMasterManifest(recorder, request)
		remoteResult <- struct{}{}
	}()
	select {
	case <-remoteStartArrived:
	case <-time.After(3 * time.Second):
		t.Fatal("remote hardware start did not reach the node")
	}
	if err := sessionMgr.SetTranscodeNodeURL("upstream-1", ""); err != nil {
		t.Fatal(err)
	}
	localWinner, err := handler.ensureTranscodeSessionWithToneMapMode(
		context.Background(), "play-1", "upstream-1", source, tonemap.ModeSoftware,
	)
	if err != nil {
		t.Fatalf("start local software winner: %v", err)
	}
	t.Cleanup(func() { handler.tm.CloseTranscodeSessionIf("upstream-1", localWinner, "") })

	releaseOnce.Do(func() { close(releaseRemoteStart) })
	select {
	case <-remoteResult:
	case <-time.After(5 * time.Second):
		t.Fatal("master manifest did not adopt the local winner")
	}
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "seg_00000") {
		t.Fatalf("manifest response = %d %q, want adopted local manifest", recorder.Code, recorder.Body.String())
	}
	select {
	case <-remoteDeleted:
	case <-time.After(2 * time.Second):
		t.Fatal("stale remote runtime was not cleaned up")
	}
	if live := handler.tm.GetTranscodeSession("upstream-1"); live != localWinner || !localWinner.IsRunning() {
		t.Fatalf("live runtime = %#v running=%v, want local software winner %#v", live, localWinner.IsRunning(), localWinner)
	}
	stored, ok := handler.playbackStore.Get("play-1")
	if !ok || stored.Recipe == nil || stored.Recipe.ToneMapMode != tonemap.ModeSoftware || stored.Recipe.TranscodeNodeURL != "" {
		t.Fatalf("stored recipe = %+v, found=%v, want local software winner", stored.Recipe, ok)
	}
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
		writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{HWAccel: tonemap.BackendQSV})
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

func TestStartRemoteTranscodeDoesNotAdoptMismatchedAudioRecipe(t *testing.T) {
	tests := []struct {
		name               string
		oldMediaFileID     int
		oldAudioTrackIndex int
		oldSourceChannels  int
	}{
		{name: "media file changed with same audio facts", oldMediaFileID: 41, oldAudioTrackIndex: 1, oldSourceChannels: 0},
		{name: "selected track changed channel layout", oldMediaFileID: 42, oldAudioTrackIndex: 1, oldSourceChannels: 6},
		{name: "selected track changed at same channel layout", oldMediaFileID: 42, oldAudioTrackIndex: 0, oldSourceChannels: 0},
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
			if received.AudioTrackIndex != 1 || received.SourceAudioChannels != 0 {
				t.Fatalf("replacement request = track %d, channels %d; want track 1, channels 0", received.AudioTrackIndex, received.SourceAudioChannels)
			}
			stored, ok := playbackStore.Get("play-1")
			if !ok || stored.Recipe == nil || stored.Recipe.AudioTrackIndex != 1 || stored.Recipe.SourceAudioChannels != 0 {
				t.Fatalf("replacement recipe = %+v, found=%v; want track 1, channels 0", stored.Recipe, ok)
			}
		})
	}
}

// TestMasterManifestGatesUnhealthyRemoteAdoption verifies the adoption redirect
// only fires while the winner's node is still healthy: a recipe another API
// server published is re-checked against the local pool before the client is
// sent to it.
func TestMasterManifestGatesUnhealthyRemoteAdoption(t *testing.T) {
	const adoptedURL = "http://adopted.invalid"
	const healthyURL = "http://healthy.invalid"
	newPool := func(adoptedHealthy bool) *nodepool.Planner {
		transcodes := nodepool.NewTranscodePool()
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

// localSessionRegistry is a GetSession + RegisterReconstructed double for
// exercising TranscodeManager reconstruction from the jellycompat package
// (the playback package's own fake is not exported across packages).
type localSessionRegistry struct {
	sessions map[string]*playback.Session
}

func (r *localSessionRegistry) GetSession(id string) (*playback.Session, error) {
	if s, ok := r.sessions[id]; ok {
		return s, nil
	}
	return nil, playback.ErrSessionNotFound
}

func (r *localSessionRegistry) RegisterReconstructed(s *playback.Session) *playback.Session {
	if r.sessions == nil {
		r.sessions = map[string]*playback.Session{}
	}
	if existing, ok := r.sessions[s.ID]; ok {
		return existing
	}
	r.sessions[s.ID] = s
	return s
}

func (r *localSessionRegistry) RegisterReconstructedWithLimits(_ context.Context, s *playback.Session) (*playback.Session, error) {
	return r.RegisterReconstructed(s), nil
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
		tm:              playback.NewTranscodeManager(),
		JWTSecret:       "test-secret",
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
		if received != nil {
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, received)
		}
		w.WriteHeader(http.StatusAccepted)
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

// TestStartRemoteTranscodeReportsConfirmedExecutor verifies remote execution facts are recorded.
func TestStartRemoteTranscodeReportsConfirmedExecutor(t *testing.T) {
	recipeStore := &stubRecipeNodeStore{}
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/transcode/start" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(transcodenode.TranscodeStartResponse{HWAccel: "qsv"})
	}))
	t.Cleanup(node.Close)
	handler, sessionMgr, playbackStore := newRemoteTranscodeHandler(t, node.URL, recipeStore)
	playbackStore.Put(PlaybackSession{ID: "play-1", UpstreamSessionID: "upstream-1"})

	if err := handler.startRemoteTranscode(context.Background(), "play-1", "upstream-1", testRemoteTranscodeSource(), &models.MediaFile{ID: 42, FilePath: "/media/movie.mkv"}, 0, node.URL); err != nil {
		t.Fatalf("startRemoteTranscode: %v", err)
	}

	session := sessionMgr.sessions["upstream-1"]
	if session.TargetVideoCodec != compatTargetVideoCodec || session.TargetAudioCodec != compatTargetAudioCodec || session.TranscodeHWAccel != "qsv" || session.ToneMapMode != "" {
		t.Fatalf("reported remote execution facts are incomplete: %+v", session)
	}
	card, ok := recipeStore.Get("upstream-1")
	if !ok || card.HWAccel != "qsv" {
		t.Fatalf("persisted remote recipe executor = %q, found=%v", card.HWAccel, ok)
	}
}

func TestStartRemoteTranscodeRecordsUnknownExecutorForLegacyEmptyResponse(t *testing.T) {
	recipeStore := &stubRecipeNodeStore{}
	node := fakeTranscodeNode(t, nil)
	handler, sessionMgr, playbackStore := newRemoteTranscodeHandler(t, node.URL, recipeStore)
	handler.HWAccel = tonemap.BackendQSV
	playbackStore.Put(PlaybackSession{ID: "play-1", UpstreamSessionID: "upstream-1"})

	if err := handler.startRemoteTranscode(context.Background(), "play-1", "upstream-1", testRemoteTranscodeSource(), &models.MediaFile{ID: 42, FilePath: "/media/movie.mkv"}, 0, node.URL); err != nil {
		t.Fatalf("startRemoteTranscode: %v", err)
	}

	session := sessionMgr.sessions["upstream-1"]
	if session.TranscodeHWAccel != "" {
		t.Fatalf("reported executor = %q, want unknown", session.TranscodeHWAccel)
	}
	card, ok := recipeStore.Get("upstream-1")
	if !ok || card.HWAccel != "" {
		t.Fatalf("persisted executor = %q, found=%v", card.HWAccel, ok)
	}
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
					_ = json.NewEncoder(w).Encode(transcodenode.TranscodeStartResponse{HWAccel: request.HWAccel, ToneMapMode: request.ToneMapMode})
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

func TestStartRemoteVideoToolboxToneMapUsesResolutionAwareBitrate(t *testing.T) {
	var requests []transcodenode.TranscodeStartRequest
	capabilities := tonemap.Capabilities{
		{Mode: tonemap.ModeHardware, Backend: tonemap.BackendVideoToolbox, Filter: tonemap.HardwareFilterVideoToolbox, SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ}},
		{Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterHable, SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ}},
	}
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hw-capabilities":
			writeJSON(w, http.StatusOK, playback.HWAccelInfo{ToneMapCapabilities: capabilities})
		case "/transcode/start":
			var request transcodenode.TranscodeStartRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode start request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			requests = append(requests, request)
			if request.ToneMapMode == tonemap.ModeHardware {
				w.WriteHeader(http.StatusUnprocessableEntity)
				return
			}
			writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{HWAccel: request.HWAccel, ToneMapMode: request.ToneMapMode})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(node.Close)

	recipeStore := &stubRecipeNodeStore{}
	handler, _, playbackStore := newRemoteTranscodeHandler(t, node.URL, recipeStore)
	handler.SettingsRepo = stubSettingsReader{values: map[string]string{
		config.Allow4KTranscodeSettingKey:                 "true",
		config.PlaybackTranscodeHardwareToneMapSettingKey: "true",
		config.PlaybackTranscodeSoftwareToneMapSettingKey: "true",
	}}
	playbackStore.Put(PlaybackSession{ID: "play-1", UpstreamSessionID: "upstream-1"})
	source := testRemoteTranscodeSource()
	source.Version.Resolution = "2160p"
	source.Version.VideoTracks[0].Height = 2160
	file := &models.MediaFile{ID: 42, FilePath: "/media/movie.mkv", HDR: true, VideoTracks: []models.VideoTrack{{Codec: "hevc", VideoRangeType: "HDR10", ColorTransfer: "smpte2084", ColorPrimaries: "bt2020", ColorSpace: "bt2020nc", BitDepth: 10}}}

	if err := handler.startRemoteTranscode(t.Context(), "play-1", "upstream-1", source, file, 0, node.URL); err != nil {
		t.Fatalf("startRemoteTranscode: %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("start requests = %d, want hardware attempt plus software fallback", len(requests))
	}
	if got := requests[0].TargetBitrateKbps; got != 20_000 {
		t.Fatalf("hardware target bitrate = %d, want 20000", got)
	}
	if requests[0].TargetResolution != "" {
		t.Fatalf("hardware target resolution = %q, want source dimensions preserved", requests[0].TargetResolution)
	}
	if got := requests[1].TargetBitrateKbps; got != 0 {
		t.Fatalf("software fallback target bitrate = %d, want unconstrained CRF", got)
	}
	card, ok := recipeStore.Get("upstream-1")
	if !ok || card.TargetBitrateKbps != 0 || card.ToneMapMode != tonemap.ModeSoftware {
		t.Fatalf("persisted fallback recipe = found %t bitrate %d mode %q", ok, card.TargetBitrateKbps, card.ToneMapMode)
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
			writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{HWAccel: request.HWAccel, ToneMapMode: request.ToneMapMode})
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
		{
			name: "timeout after accepted headers",
			body: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusAccepted)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
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
	fallbackWant := compatRemoteNodeProbeFallbackTimeout + playback.ManifestStartupTimeout + tonemap.SourcePreflightTimeout(100) + transcodenode.TranscodeStartReadinessTimeout
	if got := handler.remoteTranscodeStartTimeout(request, 0); got != fallbackWant {
		t.Fatalf("missing-budget remote transcode start timeout = %v, want %v", got, fallbackWant)
	}
}

// TestStartRemoteTranscode_NodeRestartReconstruct covers the node-restart leg:
// central persists the recipe to the control-plane store at start, the node
// loses all in-memory state, and a reconstruct fetch via the store's Get
// rebuilds the same recipe the node would re-spawn ffmpeg from (rather than a
// Redis miss → 404).
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

// TestStartRemoteTranscode_CentralRestartReconstruct covers the central-restart
// leg: the recipe lives in the compat store (PlaybackSession.Recipe), the
// in-memory native session is gone, and LoadOrReconstructSession rebuilds the
// session from the stored recipe rather than returning SessionMissing (which a
// remote segment serve renders as a 404).
func TestStartRemoteTranscode_CentralRestartReconstruct(t *testing.T) {
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
	if err := handler.startRemoteTranscode(context.Background(), "play-1", "upstream-1", source, &models.MediaFile{ID: 42, FilePath: "/media/movie.mkv"}, 0, node.URL); err != nil {
		t.Fatalf("startRemoteTranscode: %v", err)
	}

	// The compat store must now carry the recipe so central can reconstruct.
	stored, ok := playbackStore.Get("play-1")
	if !ok {
		t.Fatal("expected compat session")
	}
	if !stored.TranscodeStarted {
		t.Fatal("expected TranscodeStarted=true after remote start")
	}
	if stored.Recipe == nil {
		t.Fatal("expected PlaybackSession.Recipe persisted after remote start")
	}

	// Simulate a central restart: the in-memory native session map is empty, so
	// GetSession misses. The recipe from the compat store must let
	// LoadOrReconstructSession rebuild the session rather than 404.
	reg := &localSessionRegistry{}
	tm := playback.NewTranscodeManager()
	tm.Sessions = reg
	tm.JWTSecretFn = func() string { return "test-secret" }

	session, status := tm.LoadOrReconstructSession(
		context.Background(),
		reg.GetSession,
		"upstream-1",
		stored.Recipe.UserID,
		stored.Recipe,
	)
	if status != playback.SessionLoaded || session == nil {
		t.Fatalf("LoadOrReconstructSession status=%v session=%v, want reconstructed", status, session)
	}
	if session.ID != "upstream-1" || session.UserID != 7 || session.MediaFileID != 42 {
		t.Fatalf("reconstructed session wrong: %+v", session)
	}
	if session.TranscodeNodeURL != node.URL {
		t.Fatalf("reconstructed TranscodeNodeURL = %q, want %q", session.TranscodeNodeURL, node.URL)
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
