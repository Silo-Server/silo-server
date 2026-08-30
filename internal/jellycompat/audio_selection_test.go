package jellycompat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/tonemap"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

type testCompatSessionManager struct {
	sessions            map[string]*playback.Session
	audioTrackCalls     []compatAudioTrackCall
	progressCalls       int
	progressUpdates     []compatProgressCall
	stopCalls           []string
	startCalls          int
	beginTransportCalls []string
	endTransportCalls   []string
}

type compatAudioTrackCall struct {
	sessionID       string
	audioTrackIndex int
	method          playback.PlayMethod
}

type compatProgressCall struct {
	sessionID string
	position  float64
	isPaused  bool
}

type failAfterMutationPlaybackStore struct {
	CompatPlaybackStore
	failNext bool
}

func (s *failAfterMutationPlaybackStore) Update(id string, fn func(*PlaybackSession) error) error {
	err := s.CompatPlaybackStore.Update(id, fn)
	if err == nil && s.failNext {
		s.failNext = false
		return errors.New("durable update failed after cache mutation")
	}
	return err
}

func (m *testCompatSessionManager) StartSession(userID int, profileID string, fileID int, method playback.PlayMethod, transcodeAudio bool) (*playback.Session, error) {
	m.startCalls++
	session := &playback.Session{
		ID:             "upstream-started",
		UserID:         userID,
		ProfileID:      profileID,
		MediaFileID:    fileID,
		PlayMethod:     method,
		BasePlayMethod: method,
		TranscodeAudio: transcodeAudio,
	}
	if m.sessions == nil {
		m.sessions = make(map[string]*playback.Session)
	}
	m.sessions[session.ID] = session
	return session, nil
}

func (m *testCompatSessionManager) UpdateProgress(sessionID string, position float64, isPaused bool) error {
	m.progressCalls++
	m.progressUpdates = append(m.progressUpdates, compatProgressCall{
		sessionID: sessionID,
		position:  position,
		isPaused:  isPaused,
	})
	if m.sessions != nil {
		session, ok := m.sessions[sessionID]
		if !ok {
			return playback.ErrSessionNotFound
		}
		session.Position = position
		session.IsPaused = isPaused
	}
	return nil
}

func (m *testCompatSessionManager) BeginTransport(sessionID string) error {
	m.beginTransportCalls = append(m.beginTransportCalls, sessionID)
	if m.sessions != nil {
		if _, ok := m.sessions[sessionID]; !ok {
			return playback.ErrSessionNotFound
		}
	}
	return nil
}

func (m *testCompatSessionManager) EndTransport(sessionID string) error {
	m.endTransportCalls = append(m.endTransportCalls, sessionID)
	if m.sessions != nil {
		if _, ok := m.sessions[sessionID]; !ok {
			return playback.ErrSessionNotFound
		}
	}
	return nil
}

func (m *testCompatSessionManager) UpdateAudioTrack(sessionID string, audioTrackIndex int, method playback.PlayMethod) error {
	m.audioTrackCalls = append(m.audioTrackCalls, compatAudioTrackCall{
		sessionID:       sessionID,
		audioTrackIndex: audioTrackIndex,
		method:          method,
	})
	if session, ok := m.sessions[sessionID]; ok {
		session.AudioTrackIndex = audioTrackIndex
		session.BasePlayMethod = method
		if session.PlayMethod != playback.PlayTranscode || method == playback.PlayTranscode {
			session.PlayMethod = method
		}
	}
	return nil
}

func (m *testCompatSessionManager) StopSession(sessionID string) error {
	m.stopCalls = append(m.stopCalls, sessionID)
	delete(m.sessions, sessionID)
	return nil
}

func (m *testCompatSessionManager) GetSession(sessionID string) (*playback.Session, error) {
	if session, ok := m.sessions[sessionID]; ok {
		return session, nil
	}
	return nil, playback.ErrSessionNotFound
}

func (m *testCompatSessionManager) SetTranscodeNodeURL(sessionID, url string) error {
	if session, ok := m.sessions[sessionID]; ok {
		session.TranscodeNodeURL = url
	}
	return nil
}

// SetTranscodeStreamDetails records the execution facts supplied by the compatibility handler.
func (m *testCompatSessionManager) SetTranscodeStreamDetails(sessionID, targetVideoCodec, targetAudioCodec string, transcodeAudio bool, hwAccel string, toneMapMode tonemap.Mode) error {
	session, ok := m.sessions[sessionID]
	if !ok {
		return playback.ErrSessionNotFound
	}
	session.TargetVideoCodec = targetVideoCodec
	session.TargetAudioCodec = targetAudioCodec
	session.TranscodeAudio = transcodeAudio
	session.TranscodeHWAccel = hwAccel
	session.ToneMapMode = toneMapMode
	return nil
}

type testCompatFileResolver struct {
	file *models.MediaFile
}

func (r testCompatFileResolver) GetByID(context.Context, int) (*models.MediaFile, error) {
	return r.file, nil
}

func writeCompatTestFFmpeg(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "fake-ffmpeg.sh")
	script := "#!/bin/sh\n" +
		"case \"$2\" in\n" +
		"  -bsfs) exit 0;;\n" +
		"  -encoders) printf ' A....D aac AAC\\n'; exit 0;;\n" +
		"esac\n" +
		"case \" $* \" in\n" +
		"  *\" -f lavfi \"*) exit 0;;\n" +
		"esac\n" +
		"sleep 30\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	return path
}

func testCompatVersion() catalog.FileVersion {
	return catalog.FileVersion{
		FileID:    42,
		Duration:  3600,
		Container: "mkv",
		Bitrate:   8000,
		VideoTracks: []models.VideoTrack{
			{Codec: "h264", Width: 1920, Height: 1080},
		},
		AudioTracks: []models.AudioTrack{
			{Codec: "ac3", Default: true, Title: "Main"},
			{Codec: "aac", Title: "Commentary"},
		},
	}
}

func testCompatSource(codec *ResourceIDCodec, version catalog.FileVersion) PlaybackMediaSource {
	return PlaybackMediaSource{
		ID:                       codec.EncodeIntID(EncodedIDMediaSource, int64(version.FileID)),
		FileID:                   version.FileID,
		Version:                  version,
		SupportsDirectPlay:       true,
		SupportsDirectStream:     true,
		SupportsTranscoding:      true,
		DefaultAudioStreamIndex:  defaultAudioStreamIndex(version),
		SelectedAudioStreamIndex: intPtr(len(version.VideoTracks) + 1),
		ETag:                     mediaSourceETag(version),
	}
}

func TestBuildPlaybackSource_SeedsRequestedAudioStreamIndex(t *testing.T) {
	handler := &PlaybackHandler{codec: NewResourceIDCodec()}
	version := testCompatVersion()
	requestedAudioStreamIndex := len(version.VideoTracks) + 1

	source := handler.buildPlaybackSource(
		"route-1",
		"play-1",
		version,
		DeviceProfile{},
		playbackInfoRequest{AudioStreamIndex: compatIntValuePtr(requestedAudioStreamIndex)},
		true,
	)

	if source.SelectedAudioStreamIndex == nil {
		t.Fatal("expected selected audio stream index")
	}
	if got := *source.SelectedAudioStreamIndex; got != requestedAudioStreamIndex {
		t.Fatalf("SelectedAudioStreamIndex = %d, want %d", got, requestedAudioStreamIndex)
	}
}

func TestPlaybackInfoRequest_AcceptsStringAudioStreamIndex(t *testing.T) {
	var req playbackInfoRequest
	if err := json.Unmarshal([]byte(`{"AudioStreamIndex":"1"}`), &req); err != nil {
		t.Fatalf("unmarshal playback request: %v", err)
	}
	if req.AudioStreamIndex == nil {
		t.Fatal("expected audio stream index")
	}
	if got := int(*req.AudioStreamIndex); got != 1 {
		t.Fatalf("AudioStreamIndex = %d, want 1", got)
	}
}

func TestHandlePlaybackReport_UpdatesSelectedAudioStreamAndUpstreamTrack(t *testing.T) {
	codec := NewResourceIDCodec()
	version := testCompatVersion()
	source := testCompatSource(codec, version)
	source.SelectedAudioStreamIndex = defaultAudioStreamIndex(version)

	playbackStore := NewPlaybackSessionStore(time.Hour, nil)
	playbackStore.Put(PlaybackSession{
		ID:                 "play-1",
		CompatToken:        "token-1",
		ItemID:             "movie-1",
		UpstreamSessionID:  "upstream-1",
		UpstreamPlayMethod: "remux",
		MediaSources:       []PlaybackMediaSource{source},
	})

	sessionMgr := &testCompatSessionManager{
		sessions: map[string]*playback.Session{
			"upstream-1": {
				ID:             "upstream-1",
				PlayMethod:     playback.PlayRemux,
				BasePlayMethod: playback.PlayRemux,
			},
		},
	}
	handler := &PlaybackHandler{
		playbackStore: playbackStore,
		sessionMgr:    sessionMgr,
		tm:            playback.NewTranscodeManager(),
	}

	req := httptest.NewRequest(http.MethodPost, "/Sessions/Playing/Progress", strings.NewReader(`{"PlaySessionId":"play-1","MediaSourceId":"`+source.ID+`","AudioStreamIndex":2,"PositionTicks":30000000}`))
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, &Session{Token: "token-1"}))

	rr := httptest.NewRecorder()
	handler.HandleSessionPlayingProgress(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	updated, ok := playbackStore.Get("play-1")
	if !ok {
		t.Fatal("expected playback session")
	}
	if updated.MediaSources[0].SelectedAudioStreamIndex == nil {
		t.Fatal("expected selected audio stream index to be stored")
	}
	if got := *updated.MediaSources[0].SelectedAudioStreamIndex; got != 2 {
		t.Fatalf("SelectedAudioStreamIndex = %d, want 2", got)
	}
	if len(sessionMgr.audioTrackCalls) != 1 {
		t.Fatalf("audio track update calls = %d, want 1", len(sessionMgr.audioTrackCalls))
	}
	if got := sessionMgr.audioTrackCalls[0].audioTrackIndex; got != 1 {
		t.Fatalf("upstream audio track index = %d, want 1", got)
	}
	if got := sessionMgr.audioTrackCalls[0].method; got != playback.PlayRemux {
		t.Fatalf("upstream play method = %q, want %q", got, playback.PlayRemux)
	}
}

func TestHandlePlaybackReportRetriesRejectedLocalSurroundSelectionWithoutMutation(t *testing.T) {
	version := testCompatVersion()
	version.AudioTracks[0].Channels = 2
	version.AudioTracks[1].Channels = 6
	source := testCompatSource(NewResourceIDCodec(), version)
	source.SelectedAudioStreamIndex = intPtr(len(version.VideoTracks))
	inputPath := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(inputPath, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{
		ID: "play-1", CompatToken: "token-1", UpstreamSessionID: "upstream-1", UpstreamPlayMethod: "transcode",
		MediaSources: []PlaybackMediaSource{source},
	})
	manager := &testCompatSessionManager{sessions: map[string]*playback.Session{
		"upstream-1": {ID: "upstream-1", UserID: 7, ProfileID: "profile-1", MediaFileID: version.FileID, PlayMethod: playback.PlayTranscode, BasePlayMethod: playback.PlayTranscode},
	}}
	handler := &PlaybackHandler{
		playbackStore: store,
		sessionMgr:    manager,
		fileResolver:  testCompatFileResolver{file: &models.MediaFile{ID: version.FileID, FilePath: inputPath}},
		TranscodeDir:  t.TempDir(),
		FFmpegPath:    writeCompatTestFFmpeg(t),
		HWAccel:       playback.HWAccelNone,
		tm:            playback.NewTranscodeManager(),
	}
	transcodeSession, err := handler.ensureTranscodeSession(t.Context(), "play-1", "upstream-1", source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = transcodeSession.Close() })
	var probes int
	handler.compatAudioRegistryProbe = func(context.Context, string, tonemap.Capabilities) (*playback.TransformationRegistryV3, error) {
		probes++
		return playback.NewTransformationRegistryV3(nil), context.DeadlineExceeded
	}

	for attempt := 1; attempt <= 2; attempt++ {
		req := httptest.NewRequest(http.MethodPost, "/Sessions/Playing/Progress", strings.NewReader(`{"PlaySessionId":"play-1","MediaSourceId":"`+source.ID+`","AudioStreamIndex":2}`))
		req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, &Session{Token: "token-1"}))
		recorder := httptest.NewRecorder()
		handler.HandleSessionPlayingProgress(recorder, req)
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("attempt %d status = %d body %s", attempt, recorder.Code, recorder.Body.String())
		}
	}
	if probes != 2 {
		t.Fatalf("capability probes = %d, want identical report retried", probes)
	}
	persisted, _ := store.Get("play-1")
	if got := *persisted.MediaSources[0].SelectedAudioStreamIndex; got != len(version.VideoTracks) {
		t.Fatalf("persisted stream index = %d, want original stereo", got)
	}
	if len(manager.audioTrackCalls) != 0 || manager.sessions["upstream-1"].AudioTrackIndex != 0 {
		t.Fatalf("upstream selection mutated: calls %#v session %#v", manager.audioTrackCalls, manager.sessions["upstream-1"])
	}
	if opts := transcodeSession.Opts(); opts.AudioTrackIndex != 0 || opts.SourceAudioChannels != 0 {
		t.Fatalf("live opts = track %d channels %d, want original stereo", opts.AudioTrackIndex, opts.SourceAudioChannels)
	}
}

func TestApplyCompatAudioSelectionRejectsUnsupportedLocalHLSRemuxAudioCopy(t *testing.T) {
	version := testCompatVersion()
	version.VideoTracks[0].Codec = "hevc"
	version.VideoTracks[0].DVProfile = 8
	version.AudioTracks[0].Codec = "eac3"
	version.AudioTracks[1].Codec = "dts"
	defaultStreamIndex := len(version.VideoTracks)
	unsupportedStreamIndex := defaultStreamIndex + 1
	source := testCompatSource(NewResourceIDCodec(), version)
	source.HLSRemux = true
	source.HLSRemuxAudioStreamIndexes = []int{defaultStreamIndex}
	source.TranscodeAudio = false
	source.SelectedAudioStreamIndex = intPtr(defaultStreamIndex)
	inputPath := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(inputPath, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{
		ID: "play-1", CompatToken: "token-1", UpstreamSessionID: "upstream-1", UpstreamPlayMethod: "transcode",
		MediaSources: []PlaybackMediaSource{source},
	})
	manager := &testCompatSessionManager{sessions: map[string]*playback.Session{
		"upstream-1": {ID: "upstream-1", UserID: 7, ProfileID: "profile-1", MediaFileID: version.FileID, PlayMethod: playback.PlayTranscode, BasePlayMethod: playback.PlayTranscode},
	}}
	handler := &PlaybackHandler{
		playbackStore: store,
		sessionMgr:    manager,
		fileResolver: testCompatFileResolver{file: &models.MediaFile{
			ID: version.FileID, FilePath: inputPath,
			VideoTracks: []models.VideoTrack{{Codec: "hevc", DVProfile: 8}},
		}},
		TranscodeDir: t.TempDir(),
		FFmpegPath:   writeCompatTestFFmpeg(t),
		HWAccel:      playback.HWAccelNone,
		tm:           playback.NewTranscodeManager(),
	}
	live, err := handler.ensureTranscodeSession(t.Context(), "play-1", "upstream-1", source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = live.Close() })

	playSession, _ := store.Get("play-1")
	_, _, restarted, err := handler.applyCompatAudioSelection(t.Context(), playSession, source.ID, unsupportedStreamIndex, 12)
	if !errors.Is(err, errCompatHLSRemuxAudioUnsupported) || restarted {
		t.Fatalf("selection = restarted %t error %v, want unsupported remux audio", restarted, err)
	}
	persisted, _ := store.Get("play-1")
	if got := *persisted.MediaSources[0].SelectedAudioStreamIndex; got != defaultStreamIndex {
		t.Fatalf("persisted stream index = %d, want %d", got, defaultStreamIndex)
	}
	if opts := live.Opts(); opts.AudioTrackIndex != 0 || opts.TargetCodecVideo != compatCopyCodec ||
		opts.TargetCodecAudio != compatCopyCodec || opts.VideoSampleEntry != playback.VideoSampleEntryDVH1 {
		t.Fatalf("live recipe = track %d video %q audio %q sample entry %q, want original copy/copy/dvh1 recipe",
			opts.AudioTrackIndex, opts.TargetCodecVideo, opts.TargetCodecAudio, opts.VideoSampleEntry)
	}
	if len(manager.audioTrackCalls) != 0 {
		t.Fatalf("upstream selection calls = %#v, want none", manager.audioTrackCalls)
	}
}

func TestApplyCompatAudioSelectionRejectsUnsupportedRemoteHLSRemuxAudioCopy(t *testing.T) {
	version := testCompatVersion()
	version.VideoTracks[0].Codec = "hevc"
	version.AudioTracks[0].Codec = "eac3"
	version.AudioTracks[1].Codec = "truehd"
	defaultStreamIndex := len(version.VideoTracks)
	unsupportedStreamIndex := defaultStreamIndex + 1
	source := testCompatSource(NewResourceIDCodec(), version)
	source.HLSRemux = true
	source.HLSRemuxAudioStreamIndexes = []int{defaultStreamIndex}
	source.TranscodeAudio = false
	source.SelectedAudioStreamIndex = intPtr(defaultStreamIndex)
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{
		ID: "play-1", CompatToken: "token-1", UpstreamSessionID: "upstream-1", UpstreamPlayMethod: "transcode",
		MediaSources: []PlaybackMediaSource{source},
	})
	manager := &testCompatSessionManager{sessions: map[string]*playback.Session{
		"upstream-1": {
			ID: "upstream-1", UserID: 7, ProfileID: "profile-1", MediaFileID: version.FileID,
			PlayMethod: playback.PlayTranscode, BasePlayMethod: playback.PlayTranscode, TranscodeNodeURL: "http://remote-node.invalid",
		},
	}}
	handler := &PlaybackHandler{
		playbackStore: store,
		sessionMgr:    manager,
		fileResolver:  testCompatFileResolver{file: &models.MediaFile{ID: version.FileID, FilePath: "/media/movie.mkv"}},
		tm:            playback.NewTranscodeManager(),
	}

	playSession, _ := store.Get("play-1")
	_, _, restarted, err := handler.applyCompatAudioSelection(t.Context(), playSession, source.ID, unsupportedStreamIndex, 12)
	if !errors.Is(err, errCompatHLSRemuxAudioUnsupported) || restarted {
		t.Fatalf("selection = restarted %t error %v, want unsupported remux audio", restarted, err)
	}
	persisted, _ := store.Get("play-1")
	if got := *persisted.MediaSources[0].SelectedAudioStreamIndex; got != defaultStreamIndex {
		t.Fatalf("persisted stream index = %d, want %d", got, defaultStreamIndex)
	}
	if len(manager.audioTrackCalls) != 0 {
		t.Fatalf("upstream selection calls = %#v, want none", manager.audioTrackCalls)
	}
}

func TestHandlePlaybackReportRollsBackRemoteAttestationFailureAndRetries(t *testing.T) {
	version := testCompatVersion()
	version.AudioTracks[0].Channels = 2
	version.AudioTracks[1].Channels = 6
	source := testCompatSource(NewResourceIDCodec(), version)
	source.SelectedAudioStreamIndex = intPtr(len(version.VideoTracks))
	var starts int
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/hw-capabilities":
			writeJSON(w, http.StatusOK, playback.HWAccelInfo{Transformations: []playback.TransformationV3{{
				Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3,
			}}})
		case r.Method == http.MethodPost && r.URL.Path == "/transcode/start":
			starts++
			writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{Status: "started"})
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer node.Close()

	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{
		ID: "play-1", CompatToken: "token-1", UpstreamSessionID: "upstream-1", UpstreamPlayMethod: "transcode",
		MediaSources: []PlaybackMediaSource{source},
	})
	manager := &testCompatSessionManager{sessions: map[string]*playback.Session{
		"upstream-1": {
			ID: "upstream-1", UserID: 7, ProfileID: "profile-1", MediaFileID: version.FileID,
			PlayMethod: playback.PlayTranscode, BasePlayMethod: playback.PlayTranscode, TranscodeNodeURL: node.URL,
		},
	}}
	handler := &PlaybackHandler{
		JWTSecret:     "secret",
		playbackStore: store,
		sessionMgr:    manager,
		fileResolver:  testCompatFileResolver{file: &models.MediaFile{ID: version.FileID, FilePath: "/media/movie.mkv"}},
		tm:            playback.NewTranscodeManager(),
	}

	for attempt := 1; attempt <= 2; attempt++ {
		req := httptest.NewRequest(http.MethodPost, "/Sessions/Playing/Progress", strings.NewReader(`{"PlaySessionId":"play-1","MediaSourceId":"`+source.ID+`","AudioStreamIndex":2}`))
		req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, &Session{Token: "token-1"}))
		recorder := httptest.NewRecorder()
		handler.HandleSessionPlayingProgress(recorder, req)
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("attempt %d status = %d body %s", attempt, recorder.Code, recorder.Body.String())
		}
		persisted, _ := store.Get("play-1")
		if got := *persisted.MediaSources[0].SelectedAudioStreamIndex; got != len(version.VideoTracks) {
			t.Fatalf("attempt %d persisted stream index = %d, want original stereo", attempt, got)
		}
		if got := manager.sessions["upstream-1"].AudioTrackIndex; got != 0 {
			t.Fatalf("attempt %d upstream track = %d, want rollback to 0", attempt, got)
		}
	}
	if starts != 2 {
		t.Fatalf("remote starts = %d, want identical report retried", starts)
	}
	if len(manager.audioTrackCalls) != 4 {
		t.Fatalf("upstream selection calls = %#v, want select+rollback twice", manager.audioTrackCalls)
	}
}

func TestApplyCompatAudioSelectionRollsBackStoreMutationReportedAsFailure(t *testing.T) {
	version := testCompatVersion()
	source := testCompatSource(NewResourceIDCodec(), version)
	source.SelectedAudioStreamIndex = intPtr(len(version.VideoTracks))
	base := NewPlaybackSessionStore(time.Hour, nil)
	base.Put(PlaybackSession{
		ID: "play-1", UpstreamSessionID: "upstream-1", UpstreamPlayMethod: "remux",
		MediaSources: []PlaybackMediaSource{source},
	})
	store := &failAfterMutationPlaybackStore{CompatPlaybackStore: base, failNext: true}
	manager := &testCompatSessionManager{sessions: map[string]*playback.Session{
		"upstream-1": {ID: "upstream-1", PlayMethod: playback.PlayRemux, BasePlayMethod: playback.PlayRemux},
	}}
	handler := &PlaybackHandler{playbackStore: store, sessionMgr: manager, tm: playback.NewTranscodeManager()}
	playSession, _ := store.Get("play-1")

	restored, _, restarted, err := handler.applyCompatAudioSelection(t.Context(), playSession, source.ID, len(version.VideoTracks)+1, 0)
	if err == nil || restarted {
		t.Fatalf("selection result = restarted %t error %v, want durable failure", restarted, err)
	}
	if restored == nil || restored.MediaSources[0].SelectedAudioStreamIndex == nil ||
		*restored.MediaSources[0].SelectedAudioStreamIndex != len(version.VideoTracks) {
		t.Fatalf("restored session = %#v, want original stereo selection", restored)
	}
	persisted, _ := base.Get("play-1")
	if got := *persisted.MediaSources[0].SelectedAudioStreamIndex; got != len(version.VideoTracks) {
		t.Fatalf("cache selection = %d, want original after reported durable failure", got)
	}
	if got := manager.sessions["upstream-1"].AudioTrackIndex; got != 0 {
		t.Fatalf("upstream track = %d, want original", got)
	}
}

func TestRollbackCompatAudioSelectionDoesNotClobberNewerWinner(t *testing.T) {
	version := testCompatVersion()
	version.AudioTracks = append(version.AudioTracks, models.AudioTrack{Codec: "aac", Title: "New winner"})
	source := testCompatSource(NewResourceIDCodec(), version)
	original := source
	original.SelectedAudioStreamIndex = intPtr(len(version.VideoTracks))
	attempted := source
	attempted.SelectedAudioStreamIndex = intPtr(len(version.VideoTracks) + 1)
	newer := source
	newer.SelectedAudioStreamIndex = intPtr(len(version.VideoTracks) + 2)
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{
		ID: "play-1", UpstreamSessionID: "upstream-1", UpstreamPlayMethod: "remux",
		MediaSources: []PlaybackMediaSource{newer},
	})
	manager := &testCompatSessionManager{sessions: map[string]*playback.Session{
		"upstream-1": {ID: "upstream-1", AudioTrackIndex: 2, PlayMethod: playback.PlayRemux, BasePlayMethod: playback.PlayRemux},
	}}
	handler := &PlaybackHandler{playbackStore: store, sessionMgr: manager}

	restored, err := handler.rollbackCompatAudioSelection("play-1", source.ID, original, attempted)
	if err != nil {
		t.Fatal(err)
	}
	if got := *restored.MediaSources[0].SelectedAudioStreamIndex; got != len(version.VideoTracks)+2 {
		t.Fatalf("selection = %d, want newer winner", got)
	}
	if len(manager.audioTrackCalls) != 0 || manager.sessions["upstream-1"].AudioTrackIndex != 2 {
		t.Fatalf("rollback touched newer upstream winner: calls %#v session %#v", manager.audioTrackCalls, manager.sessions["upstream-1"])
	}
}

func TestRestartCompatAudioSelectionTreatsPublishedRemoteAsSuccess(t *testing.T) {
	version := testCompatVersion()
	source := testCompatSource(NewResourceIDCodec(), version)
	const nodeURL = "http://transcode-node"
	recipe := playback.NewRecipeCard(7, "profile-1", version.FileID, nodeURL, playback.TranscodeOpts{
		SessionID: "upstream-1", InputPath: "/media/movie.mkv", TargetCodecVideo: "h264", TargetCodecAudio: "aac",
		AudioTrackIndex: 1, SegmentDuration: 2,
	})
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{
		ID: "play-1", UpstreamSessionID: "upstream-1", UpstreamPlayMethod: "transcode", TranscodeStarted: true,
		MediaSources: []PlaybackMediaSource{source}, Recipe: &recipe,
	})
	manager := &testCompatSessionManager{sessions: map[string]*playback.Session{
		"upstream-1": {ID: "upstream-1", UserID: 7, ProfileID: "profile-1", MediaFileID: version.FileID, PlayMethod: playback.PlayTranscode, TranscodeNodeURL: nodeURL},
	}}
	handler := &PlaybackHandler{
		playbackStore: store,
		sessionMgr:    manager,
		fileResolver:  testCompatFileResolver{file: &models.MediaFile{ID: version.FileID, FilePath: "/media/movie.mkv"}},
		tm:            playback.NewTranscodeManager(),
	}
	playSession, _ := store.Get("play-1")
	restarted, err := handler.restartCompatTranscodeForAudioSelection(t.Context(), playSession, source, 0)
	if err != nil || !restarted {
		t.Fatalf("published remote adoption = restarted %t error %v, want success", restarted, err)
	}
}

func TestPersistTranscodeRecipeCompensatesCacheMutationOnDurableFailure(t *testing.T) {
	oldRecipe := playback.NewRecipeCard(7, "profile-1", 42, "", playback.TranscodeOpts{
		SessionID: "upstream-1", InputPath: "/media/movie.mkv", TargetCodecVideo: "h264", TargetCodecAudio: "aac",
		AudioTrackIndex: 0, SegmentDuration: 2,
	})
	base := NewPlaybackSessionStore(time.Hour, nil)
	base.Put(PlaybackSession{ID: "play-1", UpstreamSessionID: "upstream-1", TranscodeStarted: false, Recipe: &oldRecipe})
	store := &failAfterMutationPlaybackStore{CompatPlaybackStore: base, failNext: true}
	manager := &testCompatSessionManager{sessions: map[string]*playback.Session{
		"upstream-1": {ID: "upstream-1", UserID: 7, ProfileID: "profile-1", MediaFileID: 42, PlayMethod: playback.PlayTranscode},
	}}
	handler := &PlaybackHandler{playbackStore: store, sessionMgr: manager, tm: playback.NewTranscodeManager()}
	err := handler.persistTranscodeRecipe(t.Context(), "play-1", "upstream-1", playback.TranscodeOpts{
		SessionID: "upstream-1", InputPath: "/media/movie.mkv", TargetCodecVideo: "h264", TargetCodecAudio: "aac",
		AudioTrackIndex: 1, SourceAudioChannels: 6, TargetAudioChannels: 2, SegmentDuration: 2,
	})
	if err == nil {
		t.Fatal("persistTranscodeRecipe succeeded despite durable failure")
	}
	persisted, _ := base.Get("play-1")
	if persisted.TranscodeStarted || persisted.Recipe == nil || persisted.Recipe.AudioTrackIndex != 0 || persisted.Recipe.SourceAudioChannels != 0 {
		t.Fatalf("compat cache was not compensated: started %t recipe %#v", persisted.TranscodeStarted, persisted.Recipe)
	}
}

// TestEnsureTranscodeSession_UsesSelectedAudioTrack verifies compatibility playback keeps the requested audio stream.
func TestEnsureTranscodeSession_UsesSelectedAudioTrack(t *testing.T) {
	version := testCompatVersion()
	version.AudioTracks[1].Channels = 6
	codec := NewResourceIDCodec()
	source := testCompatSource(codec, version)
	filePath := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(filePath, []byte("video"), 0o644); err != nil {
		t.Fatalf("write media file: %v", err)
	}

	playbackStore := NewPlaybackSessionStore(time.Hour, nil)
	playbackStore.Put(PlaybackSession{
		ID:           "play-1",
		MediaSources: []PlaybackMediaSource{source},
	})
	sessionMgr := &testCompatSessionManager{sessions: map[string]*playback.Session{
		"upstream-1": {ID: "upstream-1", PlayMethod: playback.PlayTranscode},
	}}

	handler := &PlaybackHandler{
		playbackStore: playbackStore,
		sessionMgr:    sessionMgr,
		fileResolver:  testCompatFileResolver{file: &models.MediaFile{ID: version.FileID, FilePath: filePath}},
		TranscodeDir:  t.TempDir(),
		FFmpegPath:    writeCompatTestFFmpeg(t),
		HWAccel:       playback.HWAccelNone,
		tm:            playback.NewTranscodeManager(),
	}

	transcodeSession, err := handler.ensureTranscodeSession(context.Background(), "play-1", "upstream-1", source)
	if err != nil {
		t.Fatalf("ensureTranscodeSession: %v", err)
	}
	t.Cleanup(func() {
		_ = transcodeSession.Close()
	})

	if got := transcodeSession.Opts().AudioTrackIndex; got != 1 {
		t.Fatalf("AudioTrackIndex = %d, want 1", got)
	}
	if got := transcodeSession.Opts().SourceAudioChannels; got != 6 {
		t.Fatalf("SourceAudioChannels = %d, want 6", got)
	}
	upstream := sessionMgr.sessions["upstream-1"]
	if upstream.TranscodeHWAccel != playback.HWAccelNone || upstream.ToneMapMode != "" {
		t.Fatalf("reported execution facts = hw %q tone_map %q, want none and empty", upstream.TranscodeHWAccel, upstream.ToneMapMode)
	}
}

func TestEnsureTranscodeSession_RejectsLocalSurroundDownmixWithoutAudioToAACV2(t *testing.T) {
	version := testCompatVersion()
	version.AudioTracks[1].Channels = 6
	source := testCompatSource(NewResourceIDCodec(), version)
	filePath := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(filePath, []byte("video"), 0o644); err != nil {
		t.Fatalf("write media file: %v", err)
	}
	ffmpegPath, probeMarker, executionMarker := writeCompatAudioRecipeFFmpeg(t, false, "")
	playbackStore := NewPlaybackSessionStore(time.Hour, nil)
	playbackStore.Put(PlaybackSession{ID: "play-1", MediaSources: []PlaybackMediaSource{source}})
	handler := &PlaybackHandler{
		playbackStore: playbackStore,
		fileResolver:  testCompatFileResolver{file: &models.MediaFile{ID: version.FileID, FilePath: filePath}},
		TranscodeDir:  t.TempDir(),
		FFmpegPath:    ffmpegPath,
		tm:            playback.NewTranscodeManager(),
	}

	transcodeSession, err := handler.ensureTranscodeSession(context.Background(), "play-1", "upstream-1", source)
	if transcodeSession != nil || !errors.Is(err, errAudioDownmixCapabilityUnavailable) {
		t.Fatalf("ensureTranscodeSession = (%v, %v), want nil capability unavailable", transcodeSession, err)
	}
	if _, err := os.Stat(probeMarker); err != nil {
		t.Fatalf("exact v2 filter smoke test was not run: %v", err)
	}
	if _, err := os.Stat(executionMarker); !os.IsNotExist(err) {
		t.Fatalf("incompatible FFmpeg execution marker error = %v, want not started", err)
	}
}

func TestEnsureTranscodeSessionRejectsLiveRuntimeForDifferentAudioFacts(t *testing.T) {
	version := testCompatVersion()
	version.AudioTracks[0].Channels = 2
	version.AudioTracks[1].Channels = 6
	stereo := testCompatSource(NewResourceIDCodec(), version)
	stereo.SelectedAudioStreamIndex = intPtr(len(version.VideoTracks))
	surround := stereo
	surround.SelectedAudioStreamIndex = intPtr(len(version.VideoTracks) + 1)
	inputPath := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(inputPath, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{ID: "play-1", UpstreamSessionID: "upstream-1", MediaSources: []PlaybackMediaSource{stereo}})
	manager := &testCompatSessionManager{sessions: map[string]*playback.Session{
		"upstream-1": {ID: "upstream-1", UserID: 7, ProfileID: "profile-1", MediaFileID: version.FileID, PlayMethod: playback.PlayTranscode},
	}}
	handler := &PlaybackHandler{
		playbackStore: store,
		sessionMgr:    manager,
		fileResolver:  testCompatFileResolver{file: &models.MediaFile{ID: version.FileID, FilePath: inputPath}},
		TranscodeDir:  t.TempDir(),
		FFmpegPath:    writeCompatTestFFmpeg(t),
		HWAccel:       playback.HWAccelNone,
		tm:            playback.NewTranscodeManager(),
	}
	live, err := handler.ensureTranscodeSession(t.Context(), "play-1", "upstream-1", stereo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = live.Close() })

	got, err := handler.ensureTranscodeSession(t.Context(), "play-1", "upstream-1", surround)
	if got != nil || !errors.Is(err, errCompatRecipeSourceMismatch) {
		t.Fatalf("mismatched live runtime = (%v, %v), want fail-closed source mismatch", got, err)
	}
}

// TestEnsureTranscodeSessionReportsReconstructedRecipeFacts verifies reconstructed sessions retain execution facts.
func TestEnsureTranscodeSessionReportsReconstructedRecipeFacts(t *testing.T) {
	inputPath := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(inputPath, []byte("video"), 0o644); err != nil {
		t.Fatalf("write media file: %v", err)
	}
	card := playback.NewRecipeCard(7, "profile-1", 42, "", playback.TranscodeOpts{
		SessionID:        "upstream-1",
		InputPath:        inputPath,
		TargetCodecVideo: "h264",
		TargetCodecAudio: "aac",
		AudioTrackIndex:  1,
		SegmentDuration:  2,
		HWAccel:          playback.HWAccelNone,
	})
	playbackStore := NewPlaybackSessionStore(time.Hour, nil)
	playbackStore.Put(PlaybackSession{ID: "play-1", UpstreamSessionID: "upstream-1", Recipe: &card})
	sessionMgr := &testCompatSessionManager{sessions: map[string]*playback.Session{
		"upstream-1": {ID: "upstream-1", PlayMethod: playback.PlayTranscode},
	}}
	tm := playback.NewTranscodeManager()
	transcodeDir := t.TempDir()
	ffmpegPath := writeCompatTestFFmpeg(t)
	tm.Config = func() playback.TranscodeRuntimeConfig {
		return playback.TranscodeRuntimeConfig{TranscodeDir: transcodeDir, FFmpegPath: ffmpegPath, HWAccel: playback.HWAccelNone}
	}
	handler := &PlaybackHandler{playbackStore: playbackStore, sessionMgr: sessionMgr, tm: tm}

	transcodeSession, err := handler.ensureTranscodeSession(context.Background(), "play-1", "upstream-1", testCompatSource(NewResourceIDCodec(), testCompatVersion()))
	if err != nil {
		t.Fatalf("ensureTranscodeSession: %v", err)
	}
	t.Cleanup(func() { _ = transcodeSession.Close() })

	upstream := sessionMgr.sessions["upstream-1"]
	if upstream.TargetVideoCodec != "h264" || upstream.TargetAudioCodec != "aac" || upstream.TranscodeHWAccel != playback.HWAccelNone || upstream.ToneMapMode != "" {
		t.Fatalf("reconstructed execution facts not reported: %+v", upstream)
	}
}

func TestEnsureTranscodeSessionReplacesRecipeForDifferentSelectedSourceFacts(t *testing.T) {
	version := testCompatVersion()
	version.AudioTracks[0].Channels = 2
	version.AudioTracks[1].Channels = 6
	source := testCompatSource(NewResourceIDCodec(), version)
	inputPath := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(inputPath, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	stale := playback.NewRecipeCard(7, "profile-1", version.FileID, "", playback.TranscodeOpts{
		SessionID:           "upstream-1",
		InputPath:           inputPath,
		TargetCodecVideo:    "h264",
		TargetCodecAudio:    "aac",
		AudioTrackIndex:     0,
		SourceAudioChannels: 0,
		SegmentDuration:     2,
	})
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{
		ID: "play-1", UpstreamSessionID: "upstream-1", MediaSources: []PlaybackMediaSource{source}, Recipe: &stale,
	})
	manager := &testCompatSessionManager{sessions: map[string]*playback.Session{
		"upstream-1": {ID: "upstream-1", UserID: 7, ProfileID: "profile-1", MediaFileID: version.FileID, PlayMethod: playback.PlayTranscode},
	}}
	ffmpegPath, _, _ := writeCompatAudioRecipeFFmpeg(t, true, "")
	tm := playback.NewTranscodeManager()
	tm.Config = func() playback.TranscodeRuntimeConfig {
		return playback.TranscodeRuntimeConfig{TranscodeDir: t.TempDir(), FFmpegPath: ffmpegPath, HWAccel: playback.HWAccelNone}
	}
	handler := &PlaybackHandler{
		playbackStore: store,
		sessionMgr:    manager,
		fileResolver:  testCompatFileResolver{file: &models.MediaFile{ID: version.FileID, FilePath: inputPath}},
		TranscodeDir:  t.TempDir(),
		FFmpegPath:    ffmpegPath,
		HWAccel:       playback.HWAccelNone,
		tm:            tm,
	}

	transcodeSession, err := handler.ensureTranscodeSession(t.Context(), "play-1", "upstream-1", source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = transcodeSession.Close() })
	if opts := transcodeSession.Opts(); opts.AudioTrackIndex != 1 || opts.SourceAudioChannels != 6 {
		t.Fatalf("fresh opts = track %d channels %d, want track 1 channels 6", opts.AudioTrackIndex, opts.SourceAudioChannels)
	}
	persisted, ok := store.Get("play-1")
	if !ok || !compatRecipeMatchesSource(persisted.Recipe, source) {
		t.Fatalf("persisted recipe = %#v, want current selected source facts", persisted)
	}
}

func TestStartRemoteTranscode_IncludesSelectedAudioTrack(t *testing.T) {
	version := testCompatVersion()
	version.AudioTracks[1].Channels = 6
	codec := NewResourceIDCodec()
	source := testCompatSource(codec, version)
	filePath := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(filePath, []byte("video"), 0o644); err != nil {
		t.Fatalf("write media file: %v", err)
	}

	var remoteReq transcodenode.TranscodeStartRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(playback.HWAccelInfo{Transformations: []playback.TransformationV3{{
				Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3,
			}}})
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&remoteReq); err != nil {
			t.Fatalf("decode remote request: %v", err)
		}
		writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{AudioRecipeVersion: remoteReq.AudioRecipeVersion})
	}))
	defer server.Close()

	playbackStore := NewPlaybackSessionStore(time.Hour, nil)
	playbackStore.Put(PlaybackSession{ID: "play-1", UpstreamSessionID: "upstream-1"})
	handler := &PlaybackHandler{
		JWTSecret:     "secret",
		playbackStore: playbackStore,
		sessionMgr: &testCompatSessionManager{sessions: map[string]*playback.Session{
			"upstream-1": {ID: "upstream-1", UserID: 7, ProfileID: "profile-1", MediaFileID: version.FileID, PlayMethod: playback.PlayTranscode},
		}},
		tm: playback.NewTranscodeManager(),
	}
	if err := handler.startRemoteTranscode(
		context.Background(),
		"play-1",
		"upstream-1",
		source,
		&models.MediaFile{ID: version.FileID, FilePath: filePath},
		12,
		server.URL,
	); err != nil {
		t.Fatalf("startRemoteTranscode: %v", err)
	}

	if got := remoteReq.AudioTrackIndex; got != 1 {
		t.Fatalf("remote AudioTrackIndex = %d, want 1", got)
	}
	if got := remoteReq.SourceAudioChannels; got != 6 {
		t.Fatalf("remote SourceAudioChannels = %d, want 6", got)
	}
	if got := remoteReq.TargetAudioChannels; got != 2 {
		t.Fatalf("remote TargetAudioChannels = %d, want 2", got)
	}
	if !remoteReq.RequireReady {
		t.Fatal("remote surround downmix did not require recipe readiness")
	}
	if got := remoteReq.AudioRecipeVersion; got != playback.TransformationAudioToAACRecipeVersionV3 {
		t.Fatalf("remote AudioRecipeVersion = %q, want %q", got, playback.TransformationAudioToAACRecipeVersionV3)
	}
	persisted, ok := playbackStore.Get("play-1")
	if !ok || persisted.Recipe == nil || persisted.Recipe.SourceAudioChannels != 6 {
		t.Fatalf("persisted remote recipe = %#v, want six source channels", persisted)
	}
}

func TestStartRemoteTranscodeRejectsOldNodeAfterStaleAudioCapabilityProbe(t *testing.T) {
	version := testCompatVersion()
	version.AudioTracks[1].Channels = 6
	source := testCompatSource(NewResourceIDCodec(), version)
	deleted := make(chan string, 1)
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/hw-capabilities":
			writeJSON(w, http.StatusOK, playback.HWAccelInfo{Transformations: []playback.TransformationV3{{
				Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3,
			}}})
		case r.Method == http.MethodPost && r.URL.Path == "/transcode/start":
			// Simulate a pre-v2 replacement: it accepts the unknown request fields
			// but cannot return the audio recipe receipt.
			writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{Status: "started"})
		case r.Method == http.MethodDelete:
			deleted <- r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer node.Close()

	playbackStore := NewPlaybackSessionStore(time.Hour, nil)
	playbackStore.Put(PlaybackSession{ID: "play-1", UpstreamSessionID: "upstream-1"})
	handler := &PlaybackHandler{
		JWTSecret: "secret", playbackStore: playbackStore,
		sessionMgr: &testCompatSessionManager{sessions: map[string]*playback.Session{
			"upstream-1": {ID: "upstream-1", UserID: 7, ProfileID: "profile-1", MediaFileID: version.FileID, PlayMethod: playback.PlayTranscode, TranscodeNodeURL: node.URL},
		}},
		tm: playback.NewTranscodeManager(),
	}
	err := handler.startRemoteTranscode(context.Background(), "play-1", "upstream-1", source, &models.MediaFile{ID: version.FileID, FilePath: "/media/movie.mkv"}, 0, node.URL)
	if !errors.Is(err, transcodenode.ErrAudioRecipeAttestationMismatch) {
		t.Fatalf("startRemoteTranscode error = %v, want audio recipe attestation mismatch", err)
	}
	select {
	case path := <-deleted:
		if path != "/transcode/upstream-1" {
			t.Fatalf("cleanup path = %q, want /transcode/upstream-1", path)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("unattested remote job was not stopped")
	}
}
