package jellycompat

import (
	"context"
	"encoding/json"
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
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

type testCompatSessionManager struct {
	sessions        map[string]*playback.Session
	audioTrackCalls []compatAudioTrackCall
	progressCalls   int
	stopCalls       []string
}

type compatAudioTrackCall struct {
	sessionID       string
	audioTrackIndex int
	method          playback.PlayMethod
}

func (m *testCompatSessionManager) StartSession(userID int, profileID string, fileID int, method playback.PlayMethod, transcodeAudio bool) (*playback.Session, error) {
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

func (m *testCompatSessionManager) UpdateProgress(string, float64, bool) error {
	m.progressCalls++
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

type testCompatFileResolver struct {
	file *models.MediaFile
}

func (r testCompatFileResolver) GetByID(context.Context, int) (*models.MediaFile, error) {
	return r.file, nil
}

func writeCompatTestFFmpeg(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "fake-ffmpeg.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
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
		transcodes:    make(map[string]*playback.TranscodeSession),
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

func TestEnsureTranscodeSession_UsesSelectedAudioTrack(t *testing.T) {
	version := testCompatVersion()
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

	handler := &PlaybackHandler{
		playbackStore: playbackStore,
		fileResolver:  testCompatFileResolver{file: &models.MediaFile{ID: version.FileID, FilePath: filePath}},
		TranscodeDir:  t.TempDir(),
		FFmpegPath:    writeCompatTestFFmpeg(t),
		transcodes:    make(map[string]*playback.TranscodeSession),
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
}

func TestStartRemoteTranscode_IncludesSelectedAudioTrack(t *testing.T) {
	version := testCompatVersion()
	codec := NewResourceIDCodec()
	source := testCompatSource(codec, version)
	filePath := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(filePath, []byte("video"), 0o644); err != nil {
		t.Fatalf("write media file: %v", err)
	}

	var remoteReq transcodenode.TranscodeStartRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&remoteReq); err != nil {
			t.Fatalf("decode remote request: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	handler := &PlaybackHandler{JWTSecret: "secret"}
	if err := handler.startRemoteTranscode(
		context.Background(),
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
}
