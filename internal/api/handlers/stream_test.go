package handlers

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

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

type hookedSessionManager struct {
	*playback.SessionManager
	beginTransportHook func()
}

type errStreamFileResolver struct {
	err error
}

func (r errStreamFileResolver) GetByID(context.Context, int) (*models.MediaFile, error) {
	return nil, r.err
}

func (m *hookedSessionManager) BeginTransport(sessionID string) error {
	if m.beginTransportHook != nil {
		m.beginTransportHook()
	}
	return m.SessionManager.BeginTransport(sessionID)
}

func TestHandleStream_PausedSessionResumesWithDelayedRangeRequest(t *testing.T) {
	const (
		contentID       = "movie-1"
		sessionRouteKey = "session_id"
	)
	filePath := writePlaybackTestMediaFile(t, "movie.mp4")
	file := &models.MediaFile{
		ID:        42,
		ContentID: contentID,
		FilePath:  filePath,
		Duration:  3600,
	}
	sessionMgr := playback.NewSessionManager(0, 0)
	session, err := sessionMgr.StartSession(1, "profile-1", 42, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if err := sessionMgr.UpdateProgress(session.ID, 1, true); err != nil {
		t.Fatalf("UpdateProgress(paused): %v", err)
	}

	handler := NewStreamHandler(sessionMgr, testPlaybackFileResolver{file: file})
	request := func(rangeHeader, ifRange string) *httptest.ResponseRecorder {
		t.Helper()
		req := playbackTestRequest(
			http.MethodGet,
			"/api/v1/stream/"+session.ID,
			nil,
			map[string]string{sessionRouteKey: session.ID},
		)
		if rangeHeader != "" {
			req.Header.Set("Range", rangeHeader)
		}
		if ifRange != "" {
			req.Header.Set("If-Range", ifRange)
		}
		rr := httptest.NewRecorder()
		handler.HandleStream(rr, req)
		return rr
	}

	initial := request("", "")
	if initial.Code != http.StatusOK {
		t.Fatalf("initial status = %d, body = %s", initial.Code, initial.Body.String())
	}
	etag := initial.Header().Get("ETag")
	if etag == "" {
		t.Fatal("initial response omitted ETag")
	}

	const (
		activeGrace = 5 * time.Millisecond
		pausedGrace = 5 * time.Second
	)
	time.Sleep(20 * time.Millisecond)
	sessionMgr.CleanInactive(activeGrace, pausedGrace)
	if _, err := sessionMgr.GetSession(session.ID); err != nil {
		t.Fatalf("paused session expired before ranged resume: %v", err)
	}

	resumed := request("bytes=2-", etag)
	if resumed.Code != http.StatusPartialContent {
		t.Fatalf("resume status = %d, body = %s", resumed.Code, resumed.Body.String())
	}
	if got := resumed.Body.String(); got != "deo" {
		t.Fatalf("resume body = %q, want %q", got, "deo")
	}
	if live, err := sessionMgr.GetSession(session.ID); err != nil || live.ID != session.ID {
		t.Fatalf("ranged request did not preserve session %q: session=%#v err=%v", session.ID, live, err)
	}
}

func TestHandleStream_AbortsSessionWhenDirectPlayFileDisappearsAfterPreflight(t *testing.T) {
	filePath := writePlaybackTestMediaFile(t, "movie.mkv")
	file := &models.MediaFile{
		ID:        42,
		ContentID: "movie-1",
		FilePath:  filePath,
		Duration:  3600,
	}
	baseMgr := playback.NewSessionManager(0, 0)
	session, err := baseMgr.StartSession(1, "profile-1", 42, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	adminStore := &recordingPlaybackAdminStore{}
	syncer := &recordingSessionSyncer{}
	marker := &recordingMissingMarker{}
	sessionMgr := &hookedSessionManager{
		SessionManager: baseMgr,
		beginTransportHook: func() {
			_ = os.Remove(filePath)
		},
	}
	handler := NewStreamHandler(sessionMgr, testPlaybackFileResolver{file: file})
	handler.AdminStore = adminStore
	handler.SessionSyncer = syncer
	handler.MissingMarker = marker

	req := httptest.NewRequest(http.MethodGet, "/api/v1/stream/"+session.ID, nil)
	req = req.WithContext(newAuthorizedPlaybackContext())
	req = withPlaybackRouteParam(req, "session_id", session.ID)

	rr := httptest.NewRecorder()
	handler.HandleStream(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if _, err := baseMgr.GetSession(session.ID); !errors.Is(err, playback.ErrSessionNotFound) {
		t.Fatalf("GetSession error = %v, want %v", err, playback.ErrSessionNotFound)
	}
	if len(marker.ids) != 1 || marker.ids[0] != 42 {
		t.Fatalf("marked ids = %v, want [42]", marker.ids)
	}
	if len(adminStore.deleted) != 1 || adminStore.deleted[0] != session.ID {
		t.Fatalf("deleted sessions = %v, want [%s]", adminStore.deleted, session.ID)
	}
	if len(adminStore.history) != 0 {
		t.Fatalf("history entries = %d, want 0", len(adminStore.history))
	}
	if syncer.calls == 0 {
		t.Fatal("expected session sync after abort")
	}
}

func TestHandleStream_KeepsSessionWhenLookupFailsForNonMissingReason(t *testing.T) {
	baseMgr := playback.NewSessionManager(0, 0)
	session, err := baseMgr.StartSession(1, "profile-1", 42, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	adminStore := &recordingPlaybackAdminStore{}
	syncer := &recordingSessionSyncer{}
	handler := NewStreamHandler(baseMgr, errStreamFileResolver{err: errors.New("db unavailable")})
	handler.AdminStore = adminStore
	handler.SessionSyncer = syncer

	req := httptest.NewRequest(http.MethodGet, "/api/v1/stream/"+session.ID, nil)
	req = req.WithContext(newAuthorizedPlaybackContext())
	req = withPlaybackRouteParam(req, "session_id", session.ID)

	rr := httptest.NewRecorder()
	handler.HandleStream(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if _, err := baseMgr.GetSession(session.ID); err != nil {
		t.Fatalf("GetSession error = %v, want live session", err)
	}
	if len(adminStore.deleted) != 0 {
		t.Fatalf("deleted sessions = %v, want none", adminStore.deleted)
	}
	if syncer.calls != 0 {
		t.Fatalf("sync calls = %d, want 0", syncer.calls)
	}
}

// A v3 plan for an audio-only source promises audio/mp4, because a
// declared-tier client probes the advertised MIME with isTypeSupported before
// it will attach a source buffer and "video/mp4" for a stream carrying no video
// track is exactly the mismatch that makes that probe lie. The remux response
// has to keep the promise the plan made.
func TestHandleStream_AudioOnlyRemuxServesAudioContentType(t *testing.T) {
	for _, tc := range []struct {
		name        string
		file        *models.MediaFile
		wantContent string
	}{
		{
			name: "audio only source",
			file: &models.MediaFile{
				ID:         42,
				ContentID:  "audiobook-1",
				BaseType:   "audiobook",
				CodecAudio: "flac",
				Duration:   39600,
			},
			wantContent: playback.AudioOnlyRemuxMIMEV3,
		},
		{
			name: "video source",
			file: &models.MediaFile{
				ID:          42,
				ContentID:   "movie-1",
				CodecVideo:  "h264",
				CodecAudio:  "flac",
				VideoTracks: []models.VideoTrack{{Codec: "h264"}},
				Duration:    3600,
			},
			wantContent: "video/mp4",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := *tc.file
			file.FilePath = writePlaybackTestMediaFile(t, "source.mkv")

			sessionMgr := playback.NewSessionManager(0, 0)
			session, err := sessionMgr.StartSession(1, "profile-1", file.ID, playback.PlayRemux, true)
			if err != nil {
				t.Fatalf("StartSession: %v", err)
			}

			ffmpeg := filepath.Join(t.TempDir(), "ffmpeg")
			if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\nprintf muxed\n"), 0o755); err != nil {
				t.Fatalf("write fake ffmpeg: %v", err)
			}
			handler := NewStreamHandler(sessionMgr, testPlaybackFileResolver{file: &file})
			handler.PlaybackConfig = func() config.PlaybackConfig {
				return config.PlaybackConfig{FFmpegPath: ffmpeg}
			}

			req := httptest.NewRequest(http.MethodGet, "/api/v1/stream/"+session.ID, nil)
			req = req.WithContext(newAuthorizedPlaybackContext())
			req = withPlaybackRouteParam(req, "session_id", session.ID)

			rr := httptest.NewRecorder()
			handler.HandleStream(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
			}
			if got := rr.Header().Get("Content-Type"); got != tc.wantContent {
				t.Fatalf("Content-Type = %q, want %q", got, tc.wantContent)
			}
		})
	}
}

// TestHandleSubtitle_ListDownloadedSubtitlesErrorReturns500 pins the fix for
// issue #248: a failure listing downloaded subtitles must surface as a 500 with
// an "internal_error" code, not be swallowed and reported to the client as a
// generic "Subtitle track not found" 404 (which made a real backing-store
// failure look like an intermittent client-side subtitle bug).
func TestHandleSubtitle_ListDownloadedSubtitlesErrorReturns500(t *testing.T) {
	// No external or embedded tracks, so track index 0 falls through to the
	// downloaded-subtitle branch that queries the repository.
	file := &models.MediaFile{
		ID:        42,
		ContentID: "movie-1",
		FilePath:  "/tmp/movie.mkv",
		Duration:  3600,
	}
	baseMgr := playback.NewSessionManager(0, 0)
	session, err := baseMgr.StartSession(1, "profile-1", 42, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	handler := NewStreamHandler(baseMgr, testPlaybackFileResolver{file: file})
	handler.SubtitleRepo = &handlerMockSubtitleRepo{listErr: errors.New("db unavailable")}
	handler.S3Client = newMockS3ClientForHandler()
	handler.S3Bucket = "test-bucket"

	req := httptest.NewRequest(http.MethodGet, "/api/v1/stream/"+session.ID+"/subtitles/0.vtt", nil)
	req = req.WithContext(newAuthorizedPlaybackContext())
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("session_id", session.ID)
	routeCtx.URLParams.Add("track", "0.vtt")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))

	rr := httptest.NewRecorder()
	handler.HandleSubtitle(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v (body = %s)", err, rr.Body.String())
	}
	if body.Error != "internal_error" {
		t.Fatalf("error code = %q, want %q (body = %s)", body.Error, "internal_error", rr.Body.String())
	}
}

func TestHandleSubtitleUsesBoundDownloadedIdentityAfterInventoryReorder(t *testing.T) {
	file := &models.MediaFile{ID: 42, ContentID: "movie-1", FilePath: "/tmp/movie.mkv", Duration: 3600}
	baseMgr := playback.NewSessionManager(0, 0)
	session, err := baseMgr.StartSession(1, "profile-1", 42, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	repo := newMockSubtitleRepoForHandler()
	repo.subtitles[71] = &subtitles.DownloadedSubtitle{ID: 71, MediaFileID: 42, Format: subtitles.FormatVTT, S3Key: "selected-71.vtt"}
	// The mutable ordinal now points at a different subtitle. An ID-bound URL
	// must still fetch 71 without consulting this reordered list.
	repo.list = []subtitles.DownloadedSubtitle{
		{ID: 72, MediaFileID: 42, Format: subtitles.FormatVTT, S3Key: "other-72.vtt"},
		{ID: 71, MediaFileID: 42, Format: subtitles.FormatVTT, S3Key: "selected-71.vtt"},
	}
	handler := NewStreamHandler(baseMgr, testPlaybackFileResolver{file: file})
	handler.SubtitleRepo = repo
	handler.S3Client = subtitleContentS3Client{objects: map[string][]byte{
		"selected-71.vtt": []byte("WEBVTT\n\n00:00.000 --> 00:01.000\nselected-71\n"),
		"other-72.vtt":    []byte("WEBVTT\n\n00:00.000 --> 00:01.000\nother-72\n"),
	}}
	handler.S3Bucket = "test-bucket"

	req := httptest.NewRequest(http.MethodGet, "/api/v1/stream/"+session.ID+"/subtitles/0.vtt?file_id=42&downloaded_subtitle_id=71", nil)
	req = req.WithContext(newAuthorizedPlaybackContext())
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("session_id", session.ID)
	routeCtx.URLParams.Add("track", "0.vtt")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))

	rr := httptest.NewRecorder()
	handler.HandleSubtitle(rr, req)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "selected-71") || strings.Contains(rr.Body.String(), "other-72") {
		t.Fatalf("status = %d, body = %q", rr.Code, rr.Body.String())
	}
}

type subtitleContentS3Client struct {
	objects map[string][]byte
}

func (subtitleContentS3Client) PutObject(context.Context, string, string, []byte) error { return nil }
func (c subtitleContentS3Client) GetObject(_ context.Context, _, key string) ([]byte, error) {
	return append([]byte(nil), c.objects[key]...), nil
}
func (subtitleContentS3Client) DeleteObject(context.Context, string, string) error { return nil }

func TestHandleSubtitle_NilMediaFileReturns404(t *testing.T) {
	baseMgr := playback.NewSessionManager(0, 0)
	session, err := baseMgr.StartSession(1, "profile-1", 42, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	handler := NewStreamHandler(baseMgr, errStreamFileResolver{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/stream/"+session.ID+"/subtitles/0.vtt", nil)
	req = req.WithContext(newAuthorizedPlaybackContext())
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("session_id", session.ID)
	routeCtx.URLParams.Add("track", "0.vtt")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))

	rr := httptest.NewRecorder()
	handler.HandleSubtitle(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s; want 404", rr.Code, rr.Body.String())
	}
}

// A .vtt request for a bitmap (PGS) embedded track must be rejected up front:
// PGS passes the burn-in guard because it is deliverable as .sup, but it has
// no text to convert, so forcing WebVTT would spawn an ffmpeg that always
// fails after the 200 and headers are committed.
func TestHandleSubtitle_BitmapTrackVTTRequestReturns415(t *testing.T) {
	file := &models.MediaFile{
		ID:        42,
		ContentID: "movie-1",
		FilePath:  "/tmp/movie.mkv",
		Duration:  3600,
		SubtitleTracks: []models.SubtitleTrack{
			{Index: 0, Language: "eng", Codec: "hdmv_pgs_subtitle"},
		},
	}
	baseMgr := playback.NewSessionManager(0, 0)
	session, err := baseMgr.StartSession(1, "profile-1", 42, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	handler := NewStreamHandler(baseMgr, testPlaybackFileResolver{file: file})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/stream/"+session.ID+"/subtitles/0.vtt", nil)
	req = req.WithContext(newAuthorizedPlaybackContext())
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("session_id", session.ID)
	routeCtx.URLParams.Add("track", "0.vtt")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))

	rr := httptest.NewRecorder()
	handler.HandleSubtitle(rr, req)

	if rr.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, body = %s; want 415", rr.Code, rr.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v (body = %s)", err, rr.Body.String())
	}
	if body.Error != "unsupported_media_type" {
		t.Fatalf("error code = %q, want %q", body.Error, "unsupported_media_type")
	}
}

func TestHandleSubtitle_ExternalTextTrackSRTRequestReturnsVTT(t *testing.T) {
	subtitlePath := filepath.Join(t.TempDir(), "movie.en.srt")
	if err := os.WriteFile(subtitlePath, []byte("1\n00:00:01,000 --> 00:00:02,000\nHello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	file := &models.MediaFile{
		ID:        42,
		ContentID: "movie-1",
		FilePath:  "/tmp/movie.mkv",
		Duration:  3600,
		ExternalSubtitles: []models.ExternalSubtitle{
			{Path: subtitlePath, Language: "eng", Format: "srt"},
		},
	}
	baseMgr := playback.NewSessionManager(0, 0)
	session, err := baseMgr.StartSession(1, "profile-1", 42, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	handler := NewStreamHandler(baseMgr, testPlaybackFileResolver{file: file})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/stream/"+session.ID+"/subtitles/0.srt", nil)
	req = req.WithContext(newAuthorizedPlaybackContext())
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("session_id", session.ID)
	routeCtx.URLParams.Add("track", "0.srt")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	rr := httptest.NewRecorder()
	handler.HandleSubtitle(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, content-type = %q, body = %s; want 200", rr.Code, rr.Header().Get("Content-Type"), rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/vtt") {
		t.Fatalf("content type = %q, want WebVTT", got)
	}
	if body := rr.Body.String(); !strings.HasPrefix(body, "WEBVTT") || !strings.Contains(body, "Hello") {
		t.Fatalf("body = %q, want converted WebVTT", body)
	}
}

func TestHandleSubtitle_ExternalASSTrackASSRequestReturnsRawASS(t *testing.T) {
	const assBody = "[Script Info]\nTitle: Test\n"
	subtitlePath := filepath.Join(t.TempDir(), "movie.en.ass")
	if err := os.WriteFile(subtitlePath, []byte(assBody), 0o644); err != nil {
		t.Fatal(err)
	}
	file := &models.MediaFile{
		ID:                42,
		ContentID:         "movie-1",
		FilePath:          "/tmp/movie.mkv",
		Duration:          3600,
		ExternalSubtitles: []models.ExternalSubtitle{{Path: subtitlePath, Language: "eng", Format: "ass"}},
	}
	baseMgr := playback.NewSessionManager(0, 0)
	session, err := baseMgr.StartSession(1, "profile-1", 42, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	handler := NewStreamHandler(baseMgr, testPlaybackFileResolver{file: file})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/stream/"+session.ID+"/subtitles/0.ass", nil)
	req = req.WithContext(newAuthorizedPlaybackContext())
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("session_id", session.ID)
	routeCtx.URLParams.Add("track", "0.ass")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	rr := httptest.NewRecorder()
	handler.HandleSubtitle(rr, req)
	if rr.Code != http.StatusOK || rr.Body.String() != assBody {
		t.Fatalf("status = %d, content-type = %q, body = %q; want raw ASS", rr.Code, rr.Header().Get("Content-Type"), rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/x-ssa") {
		t.Fatalf("content type = %q, want raw ASS", got)
	}
}

func TestHandleSubtitle_ExternalTextHEADDoesNotLoadTheArtifact(t *testing.T) {
	file := &models.MediaFile{
		ID:                42,
		ContentID:         "movie-1",
		FilePath:          "/missing/movie.mkv",
		Duration:          3600,
		ExternalSubtitles: []models.ExternalSubtitle{{Path: "/missing/movie.en.srt", Language: "eng", Format: "srt"}},
	}
	baseMgr := playback.NewSessionManager(0, 0)
	session, err := baseMgr.StartSession(1, "profile-1", file.ID, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	handler := NewStreamHandler(baseMgr, testPlaybackFileResolver{file: file})
	req := httptest.NewRequest(http.MethodHead, "/api/v1/stream/"+session.ID+"/subtitles/0.vtt", nil)
	req = req.WithContext(newAuthorizedPlaybackContext())
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("session_id", session.ID)
	routeCtx.URLParams.Add("track", "0.vtt")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	rr := httptest.NewRecorder()
	handler.HandleSubtitle(rr, req)

	if rr.Code != http.StatusOK || rr.Body.Len() != 0 || !strings.HasPrefix(rr.Header().Get("Content-Type"), "text/vtt") {
		t.Fatalf("HEAD status=%d type=%q body=%q", rr.Code, rr.Header().Get("Content-Type"), rr.Body.String())
	}
}

func TestHandleSubtitle_EmbeddedPGSSupportsCachedHEADAndRange(t *testing.T) {
	file := &models.MediaFile{
		ID:        42,
		ContentID: "movie-1",
		FilePath:  writePlaybackTestMediaFile(t, "movie.mkv"),
		Duration:  3600,
		// The external track deliberately occupies combined ordinal 0. The PGS
		// container track is therefore addressed as ordinal 1, even though its
		// embedded subtitle-stream index is 0.
		ExternalSubtitles: []models.ExternalSubtitle{{Path: "/tmp/movie.en.srt", Format: "srt"}},
		SubtitleTracks:    []models.SubtitleTrack{{Index: 2, Language: "eng", Codec: "hdmv_pgs_subtitle"}},
	}
	baseMgr := playback.NewSessionManager(0, 0)
	session, err := baseMgr.StartSession(1, "profile-1", file.ID, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	cacheRoot := t.TempDir()
	cache := playback.NewSubtitleCache(func() string { return cacheRoot })
	warmReq := httptest.NewRequest(http.MethodGet, "/sub.sup", nil)
	warmRR := httptest.NewRecorder()
	if err := cache.ServeSUPExtract(warmRR, warmReq, playback.StreamExtractOpts{
		InputPath: file.FilePath, TrackIndex: 0, SourceCodec: "hdmv_pgs_subtitle",
	}, func(_ context.Context, opts playback.StreamExtractOpts) error {
		_, err := opts.Writer.Write([]byte("SUP PAYLOAD"))
		return err
	}); err != nil {
		t.Fatalf("warm PGS cache: %v", err)
	}

	handler := NewStreamHandler(baseMgr, testPlaybackFileResolver{file: file})
	handler.SubtitleCache = cache
	request := func(method, rangeHeader string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "/api/v1/stream/"+session.ID+"/subtitles/1.sup?file_id=42", nil)
		req = req.WithContext(newAuthorizedPlaybackContext())
		if rangeHeader != "" {
			req.Header.Set("Range", rangeHeader)
		}
		routeCtx := chi.NewRouteContext()
		routeCtx.URLParams.Add("session_id", session.ID)
		routeCtx.URLParams.Add("track", "1.sup")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
		rr := httptest.NewRecorder()
		handler.HandleSubtitle(rr, req)
		return rr
	}

	head := request(http.MethodHead, "")
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Type") != "application/octet-stream" || head.Header().Get("Content-Length") != "11" {
		t.Fatalf("HEAD status=%d type=%q length=%q body=%q", head.Code, head.Header().Get("Content-Type"), head.Header().Get("Content-Length"), head.Body.String())
	}
	ranged := request(http.MethodGet, "bytes=4-10")
	if ranged.Code != http.StatusPartialContent || ranged.Body.String() != "PAYLOAD" || ranged.Header().Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("Range status=%d type=%q body=%q", ranged.Code, ranged.Header().Get("Content-Type"), ranged.Body.String())
	}
}

func TestSubtitleSourceFileIDPinsURLAcrossEffectiveFileSwitch(t *testing.T) {
	session := &playback.Session{MediaFileID: 200, RequestedMediaFileID: 100}

	request := httptest.NewRequest(http.MethodGet, "/subtitles/4.vtt?file_id=100", nil)
	fileID, err := subtitleSourceFileID(request, session)
	if err != nil {
		t.Fatalf("subtitleSourceFileID: %v", err)
	}
	if fileID != 100 {
		t.Fatalf("fileID = %d, want original subtitle source 100", fileID)
	}

	request = httptest.NewRequest(http.MethodGet, "/subtitles/4.vtt?file_id=300", nil)
	if _, err := subtitleSourceFileID(request, session); err == nil {
		t.Fatal("expected unrelated subtitle source file to be rejected")
	}
}

func TestHandleTransportStartFailure_KeepsSessionForNonMissingError(t *testing.T) {
	filePath := writePlaybackTestMediaFile(t, "movie.mkv")
	file := &models.MediaFile{
		ID:        42,
		ContentID: "movie-1",
		FilePath:  filePath,
		Duration:  3600,
	}
	baseMgr := playback.NewSessionManager(0, 0)
	session, err := baseMgr.StartSession(1, "profile-1", 42, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	adminStore := &recordingPlaybackAdminStore{}
	syncer := &recordingSessionSyncer{}
	handler := NewStreamHandler(baseMgr, testPlaybackFileResolver{file: file})
	handler.AdminStore = adminStore
	handler.SessionSyncer = syncer

	handler.handleTransportStartFailure(context.Background(), session, file, errors.New("ffmpeg unavailable"))

	if _, err := baseMgr.GetSession(session.ID); err != nil {
		t.Fatalf("GetSession error = %v, want live session", err)
	}
	if len(adminStore.deleted) != 0 {
		t.Fatalf("deleted sessions = %v, want none", adminStore.deleted)
	}
	if syncer.calls != 0 {
		t.Fatalf("sync calls = %d, want 0", syncer.calls)
	}
}
