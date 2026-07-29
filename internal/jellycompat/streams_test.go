package jellycompat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// withCompatSession attaches a compat session carrying tok to req, so the
// ActiveEncodings ownership guard (CompatToken == session.Token) is satisfied.
func withCompatSession(req *http.Request, tok string) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), compatSessionKey, &Session{Token: tok}))
}

func TestAudioSelectionChanged(t *testing.T) {
	selected := 2
	session := &PlaybackSession{
		MediaSources: []PlaybackMediaSource{
			{ID: "src-a", SelectedAudioStreamIndex: &selected},
			{ID: "src-b", SelectedAudioStreamIndex: nil},
		},
	}

	tests := []struct {
		name          string
		session       *PlaybackSession
		mediaSourceID string
		incoming      int
		want          bool
	}{
		{"same index on known source", session, "src-a", 2, false},
		{"different index on known source", session, "src-a", 3, true},
		{"nil current on known source", session, "src-b", 2, true},
		{"unknown media source id", session, "src-missing", 2, true},
		{"empty media source id uses first match", session, "", 2, false},
		{"nil session", nil, "src-a", 2, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := audioSelectionChanged(tc.session, tc.mediaSourceID, tc.incoming)
			if got != tc.want {
				t.Errorf("audioSelectionChanged(%q, %d) = %v, want %v", tc.mediaSourceID, tc.incoming, got, tc.want)
			}
		})
	}
}

func TestGenerateFullManifest_HLSVersionForResumeStartTag(t *testing.T) {
	cases := []struct {
		name        string
		fmp4        bool
		startOffset float64
		wantVersion string
		wantStart   bool
	}{
		{"ts no resume", false, 0, "#EXT-X-VERSION:3", false},
		{"ts with resume", false, 5.5, "#EXT-X-VERSION:6", true},
		{"fmp4 no resume", true, 0, "#EXT-X-VERSION:7", false},
		{"fmp4 with resume", true, 5.5, "#EXT-X-VERSION:7", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(generateFullManifest(60, 2, tc.fmp4, tc.startOffset))
			if !strings.Contains(got, tc.wantVersion+"\n") {
				t.Fatalf("missing %s; manifest:\n%s", tc.wantVersion, got)
			}
			hasStart := strings.Contains(got, "#EXT-X-START:")
			if hasStart != tc.wantStart {
				t.Fatalf("EXT-X-START presence = %v, want %v; manifest:\n%s", hasStart, tc.wantStart, got)
			}
		})
	}
}

func TestRewriteManifest_PreservesPlaybackAndMediaSourceIDs(t *testing.T) {
	manifest := strings.Join([]string{
		"#EXTM3U",
		"#EXT-X-VERSION:7",
		"#EXT-X-MAP:URI=\"init.mp4\"",
		"#EXTINF:2.000000,",
		"seg_00000.m4s",
		"#EXTINF:2.000000,",
		"stream.m3u8",
		"",
	}, "\n")

	got := string(rewriteManifest([]byte(manifest), "item-1", "play-1", "source-1"))

	if !strings.Contains(got, "#EXT-X-MAP:URI=\"/Videos/item-1/hls/play-1/init.mp4?MediaSourceId=source-1&PlaySessionId=play-1\"") {
		t.Fatalf("expected init segment to include media and playback session ids, got:\n%s", got)
	}
	if !strings.Contains(got, "/Videos/item-1/hls/play-1/seg_00000.m4s?MediaSourceId=source-1&PlaySessionId=play-1") {
		t.Fatalf("expected media segment to include media and playback session ids, got:\n%s", got)
	}
	if !strings.Contains(got, "/Videos/item-1/hls/play-1/stream.m3u8?MediaSourceId=source-1&PlaySessionId=play-1") {
		t.Fatalf("expected nested manifest to include media and playback session ids, got:\n%s", got)
	}
}

func TestEnsureUpstreamPlayback_ReplacesStaleUpstreamWhenRecipeMissing(t *testing.T) {
	store := NewPlaybackSessionStore(time.Hour, nil)
	store.Put(PlaybackSession{
		ID:                 "ps-1",
		CompatToken:        "tok",
		UpstreamSessionID:  "stale-upstream",
		UpstreamPlayMethod: "direct",
	})
	mgr := &testCompatSessionManager{sessions: map[string]*playback.Session{}}
	h := &PlaybackHandler{
		playbackStore: store,
		sessionMgr:    mgr,
		tm:            playback.NewTranscodeManager(),
	}

	got, err := h.ensureUpstreamPlayback(
		context.Background(),
		&Session{Token: "tok", StreamAppUserID: 7, ProfileID: "profile-1"},
		"ps-1",
		PlaybackMediaSource{FileID: 42},
		"direct",
	)
	if err != nil {
		t.Fatalf("ensureUpstreamPlayback returned error: %v", err)
	}
	if got.UpstreamSessionID != "upstream-started" {
		t.Fatalf("UpstreamSessionID = %q, want fresh upstream session", got.UpstreamSessionID)
	}
	if mgr.startCalls != 1 {
		t.Fatalf("StartSession calls = %d, want 1", mgr.startCalls)
	}
	reloaded, ok := store.Get("ps-1")
	if !ok {
		t.Fatal("play session missing after upstream replacement")
	}
	if reloaded.UpstreamSessionID != "upstream-started" || reloaded.UpstreamPlayMethod != "direct" {
		t.Fatalf("store not updated with fresh upstream session: %+v", reloaded)
	}
}

// newActiveEncodingsHandler builds a PlaybackHandler literal directly (not
// NewPlaybackHandler, which touches the filesystem) with a transcode manager
// wired — teardown calls tm.CloseTranscodeSession and would nil-panic otherwise.
func newActiveEncodingsHandler(mgr *testCompatSessionManager) (*PlaybackHandler, *PlaybackSessionStore) {
	store := NewPlaybackSessionStore(time.Hour, nil)
	h := &PlaybackHandler{
		playbackStore: store,
		sessionMgr:    mgr,
		tm:            playback.NewTranscodeManager(),
	}
	return h, store
}

// TestHandleDeleteActiveEncodings_StopsTranscodeAndDeletesSession verifies the
// happy path: the upstream session is stopped and the compat play session is
// removed from the store, returning 204.
func TestHandleDeleteActiveEncodings_StopsTranscodeAndDeletesSession(t *testing.T) {
	mgr := &testCompatSessionManager{sessions: map[string]*playback.Session{"upstream-1": {ID: "upstream-1"}}}
	h, store := newActiveEncodingsHandler(mgr)
	store.Put(PlaybackSession{ID: "ps-1", UpstreamSessionID: "upstream-1", CompatToken: "tok"})

	req := withCompatSession(httptest.NewRequest("DELETE", "/Videos/ActiveEncodings?PlaySessionId=ps-1", nil), "tok")
	rec := httptest.NewRecorder()
	h.HandleDeleteActiveEncodings(rec, req)

	if rec.Code != 204 {
		t.Fatalf("status = %d, body = %s; want 204", rec.Code, rec.Body.String())
	}
	if _, ok := store.Get("ps-1"); ok {
		t.Fatal("play session should be deleted")
	}
	if len(mgr.stopCalls) != 1 || mgr.stopCalls[0] != "upstream-1" {
		t.Fatalf("expected StopSession(upstream-1); got %v", mgr.stopCalls)
	}
}

// TestTeardownPlaySession_DeletesNodeRecipe verifies the deliberate stop path
// drops the node recipe keyed by the upstream session id, so a buffered/retrying
// request after a node restart cannot resurrect ffmpeg for the stopped session.
func TestTeardownPlaySession_DeletesNodeRecipe(t *testing.T) {
	mgr := &testCompatSessionManager{sessions: map[string]*playback.Session{"upstream-1": {ID: "upstream-1"}}}
	h, store := newActiveEncodingsHandler(mgr)
	recipeStore := &stubRecipeNodeStore{cards: map[string]playback.RecipeCard{
		"upstream-1": {SessionID: "upstream-1"},
	}}
	h.RecipeNodeStore = recipeStore
	store.Put(PlaybackSession{ID: "ps-1", UpstreamSessionID: "upstream-1", CompatToken: "tok"})

	playSession, ok := store.Get("ps-1")
	if !ok {
		t.Fatal("expected play session")
	}
	h.teardownPlaySession(context.Background(), playSession, nil, nil)

	if _, ok := recipeStore.Get("upstream-1"); ok {
		t.Fatal("node recipe should be deleted on deliberate teardown")
	}
	if len(mgr.stopCalls) != 1 || mgr.stopCalls[0] != "upstream-1" {
		t.Fatalf("expected StopSession(upstream-1); got %v", mgr.stopCalls)
	}
}

// TestHandleDeleteActiveEncodings_MissingPlaySessionIdReturns204 verifies a
// request with no PlaySessionId is a 204 no-op (no teardown).
func TestHandleDeleteActiveEncodings_MissingPlaySessionIdReturns204(t *testing.T) {
	mgr := &testCompatSessionManager{sessions: map[string]*playback.Session{"upstream-1": {ID: "upstream-1"}}}
	h, store := newActiveEncodingsHandler(mgr)
	store.Put(PlaybackSession{ID: "ps-1", UpstreamSessionID: "upstream-1", CompatToken: "tok"})

	req := withCompatSession(httptest.NewRequest("DELETE", "/Videos/ActiveEncodings", nil), "tok")
	rec := httptest.NewRecorder()
	h.HandleDeleteActiveEncodings(rec, req)

	if rec.Code != 204 {
		t.Fatalf("status = %d, body = %s; want 204", rec.Code, rec.Body.String())
	}
	if _, ok := store.Get("ps-1"); !ok {
		t.Fatal("unrelated play session must not be torn down")
	}
	if len(mgr.stopCalls) != 0 {
		t.Fatalf("expected no StopSession calls; got %v", mgr.stopCalls)
	}
}

// TestHandleDeleteActiveEncodings_UnknownPlaySessionReturns204 verifies an
// unknown PlaySessionId is a 204 no-op (idempotent "already gone" semantics).
func TestHandleDeleteActiveEncodings_UnknownPlaySessionReturns204(t *testing.T) {
	mgr := &testCompatSessionManager{}
	h, _ := newActiveEncodingsHandler(mgr)

	req := withCompatSession(httptest.NewRequest("DELETE", "/Videos/ActiveEncodings?PlaySessionId=does-not-exist", nil), "tok")
	rec := httptest.NewRecorder()
	h.HandleDeleteActiveEncodings(rec, req)

	if rec.Code != 204 {
		t.Fatalf("status = %d, body = %s; want 204", rec.Code, rec.Body.String())
	}
	if len(mgr.stopCalls) != 0 {
		t.Fatalf("expected no StopSession calls; got %v", mgr.stopCalls)
	}
}

// TestHandleDeleteActiveEncodings_CaseInsensitivePlaySessionId verifies a
// lowercase playSessionId key (as Wholphin sends) still resolves and tears down
// the session — the reason newCaseInsensitiveQuery is used.
func TestHandleDeleteActiveEncodings_CaseInsensitivePlaySessionId(t *testing.T) {
	mgr := &testCompatSessionManager{sessions: map[string]*playback.Session{"upstream-1": {ID: "upstream-1"}}}
	h, store := newActiveEncodingsHandler(mgr)
	store.Put(PlaybackSession{ID: "ps-1", UpstreamSessionID: "upstream-1", CompatToken: "tok"})

	req := withCompatSession(httptest.NewRequest("DELETE", "/Videos/ActiveEncodings?playSessionId=ps-1", nil), "tok")
	rec := httptest.NewRecorder()
	h.HandleDeleteActiveEncodings(rec, req)

	if rec.Code != 204 {
		t.Fatalf("status = %d, body = %s; want 204", rec.Code, rec.Body.String())
	}
	if _, ok := store.Get("ps-1"); ok {
		t.Fatal("lowercase playSessionId should still resolve and delete the session")
	}
}

// TestHandleDeleteActiveEncodings_ForeignPlaySessionNotTornDown proves the
// ownership guard: a caller whose token differs from the play session's
// CompatToken gets a uniform 204 no-op and does NOT tear down the foreign
// session (no cross-session IDOR teardown).
func TestHandleDeleteActiveEncodings_ForeignPlaySessionNotTornDown(t *testing.T) {
	mgr := &testCompatSessionManager{sessions: map[string]*playback.Session{"upstream-1": {ID: "upstream-1"}}}
	h, store := newActiveEncodingsHandler(mgr)
	store.Put(PlaybackSession{ID: "ps-1", UpstreamSessionID: "upstream-1", CompatToken: "owner"})

	req := withCompatSession(httptest.NewRequest("DELETE", "/Videos/ActiveEncodings?PlaySessionId=ps-1", nil), "attacker")
	rec := httptest.NewRecorder()
	h.HandleDeleteActiveEncodings(rec, req)

	if rec.Code != 204 {
		t.Fatalf("status = %d, body = %s; want 204", rec.Code, rec.Body.String())
	}
	if _, ok := store.Get("ps-1"); !ok {
		t.Fatal("foreign play session must not be torn down")
	}
	if len(mgr.stopCalls) != 0 {
		t.Fatalf("expected no StopSession calls; got %v", mgr.stopCalls)
	}
}

// TestHandleDeleteActiveEncodings_RealClientShape exercises the dominant real
// JellyCon call shape (DeviceId present alongside PlaySessionId): with a
// matching-token session the session is still torn down (DeviceId ignored).
func TestHandleDeleteActiveEncodings_RealClientShape(t *testing.T) {
	mgr := &testCompatSessionManager{sessions: map[string]*playback.Session{"upstream-1": {ID: "upstream-1"}}}
	h, store := newActiveEncodingsHandler(mgr)
	store.Put(PlaybackSession{ID: "ps-1", UpstreamSessionID: "upstream-1", CompatToken: "tok"})

	req := withCompatSession(httptest.NewRequest("DELETE", "/Videos/ActiveEncodings?DeviceId=dev1&PlaySessionId=ps-1", nil), "tok")
	rec := httptest.NewRecorder()
	h.HandleDeleteActiveEncodings(rec, req)

	if rec.Code != 204 {
		t.Fatalf("status = %d, body = %s; want 204", rec.Code, rec.Body.String())
	}
	if _, ok := store.Get("ps-1"); ok {
		t.Fatal("play session should be torn down when DeviceId accompanies a matching PlaySessionId")
	}
	if len(mgr.stopCalls) != 1 || mgr.stopCalls[0] != "upstream-1" {
		t.Fatalf("expected StopSession(upstream-1); got %v", mgr.stopCalls)
	}
}

// TestHandleDeleteActiveEncodings_NotYetStartedNotTornDown guards the early
// window between PlaybackInfo and the first manifest request, when the play
// session exists but UpstreamSessionID is still empty. A DELETE that lands then
// must be a 204 no-op that leaves the session in the store, so the pending
// manifest request still resolves (mirrors the Stopped report path). Removing
// the UpstreamSessionID == "" guard makes this test fail.
func TestHandleDeleteActiveEncodings_NotYetStartedNotTornDown(t *testing.T) {
	mgr := &testCompatSessionManager{}
	h, store := newActiveEncodingsHandler(mgr)
	store.Put(PlaybackSession{ID: "ps-1", CompatToken: "tok"})

	req := withCompatSession(httptest.NewRequest("DELETE", "/Videos/ActiveEncodings?PlaySessionId=ps-1", nil), "tok")
	rec := httptest.NewRecorder()
	h.HandleDeleteActiveEncodings(rec, req)

	if rec.Code != 204 {
		t.Fatalf("status = %d, body = %s; want 204", rec.Code, rec.Body.String())
	}
	if _, ok := store.Get("ps-1"); !ok {
		t.Fatal("not-yet-started play session must survive teardown so the pending manifest still resolves")
	}
	if len(mgr.stopCalls) != 0 {
		t.Fatalf("expected no StopSession calls; got %v", mgr.stopCalls)
	}
}

// TestRestartCompatTranscodeForAudioSelection_LocalRePersistsRecipe covers the
// integrated/single-box leg of an audio switch: the live ffmpeg is restarted on
// the new track, and the durable PlaybackSession.Recipe must be re-persisted so
// that a reconstruct after a central restart rebuilds ffmpeg from the NEWLY
// selected audio track rather than the stale original. Without the re-persist,
// Recipe.AudioTrackIndex keeps the original value and the stream silently
// resumes on the wrong language after a restart.
func TestRestartCompatTranscodeForAudioSelection_LocalRePersistsRecipe(t *testing.T) {
	codec := NewResourceIDCodec()
	version := testCompatVersion() // 1 video track, 2 audio tracks.

	// Initial source selects the first (main) audio track -> AudioTrackIndex 0.
	mainSource := testCompatSource(codec, version)
	mainSource.SelectedAudioStreamIndex = intPtr(len(version.VideoTracks)) // stream index 1 -> track 0.

	// Switch target selects the second (commentary) audio track -> AudioTrackIndex 1.
	commentarySource := testCompatSource(codec, version)
	commentarySource.SelectedAudioStreamIndex = intPtr(len(version.VideoTracks) + 1) // stream index 2 -> track 1.

	filePath := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(filePath, []byte("video"), 0o644); err != nil {
		t.Fatalf("write media file: %v", err)
	}

	playbackStore := NewPlaybackSessionStore(time.Hour, nil)
	playbackStore.Put(PlaybackSession{
		ID:                 "play-1",
		UpstreamSessionID:  "upstream-1",
		UpstreamPlayMethod: "transcode",
		MediaSources:       []PlaybackMediaSource{commentarySource},
	})

	sessionMgr := &testCompatSessionManager{
		sessions: map[string]*playback.Session{
			"upstream-1": {
				ID:             "upstream-1",
				UserID:         7,
				ProfileID:      "profile-1",
				MediaFileID:    version.FileID,
				PlayMethod:     playback.PlayTranscode,
				BasePlayMethod: playback.PlayTranscode,
			},
		},
	}

	handler := &PlaybackHandler{
		playbackStore: playbackStore,
		sessionMgr:    sessionMgr,
		fileResolver:  testCompatFileResolver{file: &models.MediaFile{ID: version.FileID, FilePath: filePath}},
		TranscodeDir:  t.TempDir(),
		FFmpegPath:    writeCompatTestFFmpeg(t),
		tm:            playback.NewTranscodeManager(),
	}

	// Start the live transcode on the main track and persist its initial recipe
	// (AudioTrackIndex 0), mirroring a normal play start.
	transcodeSession, err := handler.ensureTranscodeSession(context.Background(), "play-1", "upstream-1", mainSource)
	if err != nil {
		t.Fatalf("ensureTranscodeSession: %v", err)
	}
	t.Cleanup(func() { _ = transcodeSession.Close() })

	if got := transcodeSession.Opts().AudioTrackIndex; got != 0 {
		t.Fatalf("initial AudioTrackIndex = %d, want 0", got)
	}
	if initial, ok := playbackStore.Get("play-1"); !ok || initial.Recipe == nil {
		t.Fatal("expected initial recipe persisted after ensureTranscodeSession")
	} else if initial.Recipe.AudioTrackIndex != 0 {
		t.Fatalf("initial Recipe.AudioTrackIndex = %d, want 0", initial.Recipe.AudioTrackIndex)
	}

	playSession, ok := playbackStore.Get("play-1")
	if !ok {
		t.Fatal("expected play session")
	}

	// Switch audio to the commentary track via the LOCAL branch.
	restarted, err := handler.restartCompatTranscodeForAudioSelection(
		context.Background(),
		playSession,
		commentarySource,
		0,
	)
	if err != nil {
		t.Fatalf("restartCompatTranscodeForAudioSelection: %v", err)
	}
	if !restarted {
		t.Fatal("expected local transcode restart to report restarted=true")
	}

	// The live ffmpeg opts must reflect the new track...
	if got := transcodeSession.Opts().AudioTrackIndex; got != 1 {
		t.Fatalf("live AudioTrackIndex after switch = %d, want 1", got)
	}

	// ...and, crucially, the durable recipe must track it so a reconstruct after
	// a central restart rebuilds ffmpeg on the commentary track.
	updated, ok := playbackStore.Get("play-1")
	if !ok {
		t.Fatal("expected play session after audio switch")
	}
	if updated.Recipe == nil {
		t.Fatal("expected Recipe to remain persisted after local audio switch")
	}
	if updated.Recipe.AudioTrackIndex != 1 {
		t.Fatalf("Recipe.AudioTrackIndex = %d, want 1 (re-persisted to newly selected track)", updated.Recipe.AudioTrackIndex)
	}
}

// recordingSessionSyncer counts SyncNow calls and records the context state at
// call time, standing in for the reconciler's immediate-sync trigger.
type recordingSessionSyncer struct {
	calls           int
	lastCtxErr      error
	lastHadDeadline bool
}

func (s *recordingSessionSyncer) SyncNow(ctx context.Context) error {
	s.calls++
	s.lastCtxErr = ctx.Err()
	_, s.lastHadDeadline = ctx.Deadline()
	return nil
}

// TestHandleSessionPlayingStopped_TearsDownAndSyncsImmediately verifies the
// Stopped report path removes the compat session AND flushes the live-session
// snapshot right away, so the activity dashboard doesn't show a ghost stream
// until the next reconciler tick (issue #205).
func TestHandleSessionPlayingStopped_TearsDownAndSyncsImmediately(t *testing.T) {
	mgr := &testCompatSessionManager{sessions: map[string]*playback.Session{"upstream-1": {ID: "upstream-1"}}}
	h, store := newActiveEncodingsHandler(mgr)
	syncer := &recordingSessionSyncer{}
	h.SessionSyncer = syncer
	store.Put(PlaybackSession{ID: "ps-1", UpstreamSessionID: "upstream-1", CompatToken: "tok"})

	body := strings.NewReader(`{"PlaySessionId":"ps-1"}`)
	// Cancel the request context up front to simulate the client dropping the
	// connection right after firing the stop report — the sync must still run.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := withCompatSession(httptest.NewRequest("POST", "/Sessions/Playing/Stopped", body).WithContext(ctx), "tok")
	rec := httptest.NewRecorder()
	h.HandleSessionPlayingStopped(rec, req)

	if rec.Code != 204 {
		t.Fatalf("status = %d, body = %s; want 204", rec.Code, rec.Body.String())
	}
	if _, ok := store.Get("ps-1"); ok {
		t.Fatal("play session should be deleted")
	}
	if len(mgr.stopCalls) != 1 || mgr.stopCalls[0] != "upstream-1" {
		t.Fatalf("expected StopSession(upstream-1); got %v", mgr.stopCalls)
	}
	if syncer.calls != 1 {
		t.Fatalf("SyncNow calls = %d; want 1", syncer.calls)
	}
	if syncer.lastCtxErr != nil {
		t.Fatalf("sync context canceled with request: %v", syncer.lastCtxErr)
	}
	if !syncer.lastHadDeadline {
		t.Fatal("sync context must carry a deadline so a stalled DB cannot pin the request goroutine")
	}
}

// TestHandleSessionPlayingStopped_UnknownSessionDoesNotSync verifies a stop
// report that tears nothing down doesn't trigger a sync round trip.
func TestHandleSessionPlayingStopped_UnknownSessionDoesNotSync(t *testing.T) {
	mgr := &testCompatSessionManager{}
	h, _ := newActiveEncodingsHandler(mgr)
	syncer := &recordingSessionSyncer{}
	h.SessionSyncer = syncer

	body := strings.NewReader(`{"PlaySessionId":"ps-missing"}`)
	req := withCompatSession(httptest.NewRequest("POST", "/Sessions/Playing/Stopped", body), "tok")
	rec := httptest.NewRecorder()
	h.HandleSessionPlayingStopped(rec, req)

	if rec.Code != 204 {
		t.Fatalf("status = %d, body = %s; want 204", rec.Code, rec.Body.String())
	}
	if syncer.calls != 0 {
		t.Fatalf("SyncNow calls = %d; want 0", syncer.calls)
	}
}

// TestHandleDeleteActiveEncodings_SyncsSessionsImmediately verifies the
// explicit encoder-teardown path also flushes the live-session snapshot.
func TestHandleDeleteActiveEncodings_SyncsSessionsImmediately(t *testing.T) {
	mgr := &testCompatSessionManager{sessions: map[string]*playback.Session{"upstream-1": {ID: "upstream-1"}}}
	h, store := newActiveEncodingsHandler(mgr)
	syncer := &recordingSessionSyncer{}
	h.SessionSyncer = syncer
	store.Put(PlaybackSession{ID: "ps-1", UpstreamSessionID: "upstream-1", CompatToken: "tok"})

	req := withCompatSession(httptest.NewRequest("DELETE", "/Videos/ActiveEncodings?PlaySessionId=ps-1", nil), "tok")
	rec := httptest.NewRecorder()
	h.HandleDeleteActiveEncodings(rec, req)

	if rec.Code != 204 {
		t.Fatalf("status = %d, body = %s; want 204", rec.Code, rec.Body.String())
	}
	if syncer.calls != 1 {
		t.Fatalf("SyncNow calls = %d; want 1", syncer.calls)
	}
}

// TestEnsureUpstreamPlayback_SyncsOnNewSession verifies a fresh upstream
// session start flushes the live-session snapshot so the new stream appears in
// the activity dashboard immediately.
func TestEnsureUpstreamPlayback_SyncsOnNewSession(t *testing.T) {
	mgr := &testCompatSessionManager{}
	h, store := newActiveEncodingsHandler(mgr)
	syncer := &recordingSessionSyncer{}
	h.SessionSyncer = syncer
	store.Put(PlaybackSession{ID: "ps-1", CompatToken: "tok"})

	compatSession := &Session{Token: "tok", StreamAppUserID: 7, ProfileID: "prof-1"}
	source := PlaybackMediaSource{ID: "src-1", FileID: 42}
	playSession, err := h.ensureUpstreamPlayback(context.Background(), compatSession, "ps-1", source, "direct")
	if err != nil {
		t.Fatalf("ensureUpstreamPlayback: %v", err)
	}
	if playSession.UpstreamSessionID == "" {
		t.Fatal("expected upstream session to be started")
	}
	if syncer.calls != 1 {
		t.Fatalf("SyncNow calls = %d; want 1", syncer.calls)
	}

	// Re-entering with the same method reuses the session and must not sync again.
	if _, err := h.ensureUpstreamPlayback(context.Background(), compatSession, "ps-1", source, "direct"); err != nil {
		t.Fatalf("ensureUpstreamPlayback reuse: %v", err)
	}
	if syncer.calls != 1 {
		t.Fatalf("SyncNow calls after reuse = %d; want 1", syncer.calls)
	}
}
