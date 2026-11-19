package jellycompat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// The tests in this file cover issue #244: an Infuse Static=true direct play
// vanished from the admin Activity View mid-playback with a frozen position.
// Three defects compounded:
//
//  1. handlePlaybackReport resolved the play session strictly by PlaySessionId,
//     but a Static direct play that skipped PlaybackInfo reports under the
//     client's own generated id, so every progress report silently no-op'd and
//     the position never advanced.
//  2. With progress reports dropped, nothing refreshed the upstream session's
//     activity, so stale cleanup reaped it ~45s in while the client kept
//     streaming. HandleVideoStream never marked an in-flight transport the way
//     the native stream handler does.
//  3. ensureUpstreamPlayback early-returned whenever UpstreamSessionID was set
//     without checking the session still existed, so a reaped session was
//     never recreated and the stream card never came back.

// newReportLivenessHandler builds a PlaybackHandler with one stored play
// session whose upstream session id is registered in the fake session manager.
func newReportLivenessHandler(upstreamID string, registerUpstream bool) (*PlaybackHandler, *testCompatSessionManager, string, string) {
	codec := NewResourceIDCodec()
	version := testCompatVersion()
	source := testCompatSource(codec, version)
	encodedItemID := codec.EncodeStringID(EncodedIDItem, "movie-1")

	playbackStore := NewPlaybackSessionStore(time.Hour, nil)
	playbackStore.Put(PlaybackSession{
		ID:                       "play-1",
		CompatToken:              "token-1",
		ItemID:                   "movie-1",
		RouteItemID:              encodedItemID,
		UserID:                   "user-1",
		UpstreamSessionID:        upstreamID,
		UpstreamPlayMethod:       "direct",
		ProgressPersistenceKnown: true,
		MediaSources:             []PlaybackMediaSource{source},
	})

	sessions := map[string]*playback.Session{}
	if registerUpstream {
		sessions[upstreamID] = &playback.Session{
			ID:             upstreamID,
			PlayMethod:     playback.PlayDirect,
			BasePlayMethod: playback.PlayDirect,
		}
	}
	sessionMgr := &testCompatSessionManager{sessions: sessions}
	handler := &PlaybackHandler{
		codec:         codec,
		playbackStore: playbackStore,
		sessionMgr:    sessionMgr,
		tm:            keyedTranscodeManager(),
	}
	return handler, sessionMgr, encodedItemID, source.ID
}

func postProgressReport(handler *PlaybackHandler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/Sessions/Playing/Progress", strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey,
		&Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"}))
	rec := httptest.NewRecorder()
	handler.HandleSessionPlayingProgress(rec, req)
	return rec
}

// TestHandlePlaybackReport_MediaSourceIDFallbackWhenItemUnknown covers clients
// that omit or send an unmatchable ItemId: the MediaSourceId in the report
// still identifies the play session.
func TestHandlePlaybackReport_MediaSourceIDFallbackWhenItemUnknown(t *testing.T) {
	handler, mgr, _, sourceID := newReportLivenessHandler("upstream-1", true)

	rec := postProgressReport(handler,
		`{"PlaySessionId":"infuse-client-psid","MediaSourceId":"`+sourceID+`","PositionTicks":600000000}`)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(mgr.progressUpdates) != 1 {
		t.Fatalf("UpdateProgress calls = %d, want 1 (MediaSourceId must resolve the play session)", len(mgr.progressUpdates))
	}
	if got := mgr.progressUpdates[0].sessionID; got != "upstream-1" {
		t.Fatalf("UpdateProgress session = %q, want upstream-1", got)
	}
}

// TestHandlePlaybackReport_ForeignTokenDoesNotMatch ensures the fallback stays
// scoped to the caller's own compat token: a report authenticated with a
// different token must not read or touch another user's play session.
func TestHandlePlaybackReport_ForeignTokenDoesNotMatch(t *testing.T) {
	handler, mgr, encodedItemID, sourceID := newReportLivenessHandler("upstream-1", true)

	req := httptest.NewRequest(http.MethodPost, "/Sessions/Playing/Progress", strings.NewReader(
		`{"PlaySessionId":"infuse-client-psid","ItemId":"`+encodedItemID+`","MediaSourceId":"`+sourceID+`","PositionTicks":600000000}`))
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey,
		&Session{Token: "other-token", StreamAppUserID: 2, ProfileID: "profile-2"}))
	rec := httptest.NewRecorder()
	handler.HandleSessionPlayingProgress(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(mgr.progressUpdates) != 0 {
		t.Fatalf("UpdateProgress calls = %d, want 0 (foreign token must not bind another caller's session)", len(mgr.progressUpdates))
	}
}

// TestHandlePlaybackReport_StoppedDoesNotReviveUpstream ensures the revival
// path does not resurrect a session for a Stopped report: stopping playback of
// an already-reaped session must stay a no-op teardown, not create a ghost.
func TestHandlePlaybackReport_StoppedDoesNotReviveUpstream(t *testing.T) {
	handler, mgr, _, sourceID := newReportLivenessHandler("upstream-reaped", false)

	req := httptest.NewRequest(http.MethodPost, "/Sessions/Playing/Stopped", strings.NewReader(
		`{"PlaySessionId":"play-1","MediaSourceId":"`+sourceID+`","PositionTicks":9000000000}`))
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey,
		&Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"}))
	rec := httptest.NewRecorder()
	handler.HandleSessionPlayingStopped(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if mgr.startCalls != 0 {
		t.Fatalf("StartSession calls = %d, want 0 (Stopped must not revive a reaped session)", mgr.startCalls)
	}
	if _, ok := handler.playbackStore.Get("play-1"); ok {
		t.Fatal("expected Stopped report to tear down the play session")
	}
}

// TestHandlePlaybackReport_StopViaAliasTearsDown proves a Stopped report
// resolved through the recorded alias still tears the play session down —
// the alias is an exact, caller-owned match.
func TestHandlePlaybackReport_StopViaAliasTearsDown(t *testing.T) {
	handler, mgr, _, sourceID := newReportLivenessHandler("upstream-1", true)
	if err := handler.playbackStore.Update("play-1", func(current *PlaybackSession) error {
		current.ClientPlaySessionID = "infuse-client-psid"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/Sessions/Playing/Stopped", strings.NewReader(
		`{"PlaySessionId":"infuse-client-psid","MediaSourceId":"`+sourceID+`","PositionTicks":9000000000}`))
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey,
		&Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"}))
	rec := httptest.NewRecorder()
	handler.HandleSessionPlayingStopped(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(mgr.stopCalls) != 1 || mgr.stopCalls[0] != "upstream-1" {
		t.Fatalf("StopSession calls = %v, want exactly one for upstream-1", mgr.stopCalls)
	}
	if _, ok := handler.playbackStore.Get("play-1"); ok {
		t.Fatal("expected alias-matched Stopped report to tear down the play session")
	}
}

// TestHandlePlaybackReport_StopViaUniqueRouteDoesNotTearDown proves a delayed
// Stopped report cannot use a same-item route to tear down a newer play.
func TestHandlePlaybackReport_StopViaUniqueRouteDoesNotTearDown(t *testing.T) {
	handler, mgr, encodedItemID, sourceID := newReportLivenessHandler("upstream-1", true)

	req := httptest.NewRequest(http.MethodPost, "/Sessions/Playing/Stopped", strings.NewReader(
		`{"PlaySessionId":"never-seen-psid","ItemId":"`+encodedItemID+`","MediaSourceId":"`+sourceID+`","PositionTicks":9000000000}`))
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey,
		&Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"}))
	rec := httptest.NewRecorder()
	handler.HandleSessionPlayingStopped(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(mgr.stopCalls) != 0 {
		t.Fatalf("StopSession calls = %v, want none", mgr.stopCalls)
	}
	if _, ok := handler.playbackStore.Get("play-1"); !ok {
		t.Fatal("route-only Stopped report tore down the active play session")
	}
	if len(mgr.progressUpdates) != 0 {
		t.Fatalf("progress updates = %+v, want none", mgr.progressUpdates)
	}
}

// racingStartSessionManager simulates a concurrent request winning the
// upstream-attach race: while this caller is inside StartSession, the store's
// play session is re-pointed at a different upstream id/method. winnerLocal
// says whether the winner attached in this process (so its native session is
// here) or on another replica.
type racingStartSessionManager struct {
	testCompatSessionManager
	store        CompatPlaybackStore
	winnerMethod string
	winnerLocal  bool
}

func (m *racingStartSessionManager) StartSession(userID int, profileID string, fileID int, method playback.PlayMethod, transcodeAudio bool) (*playback.Session, error) {
	_ = m.store.Update("play-1", func(current *PlaybackSession) error {
		current.UpstreamSessionID = "upstream-winner"
		current.UpstreamPlayMethod = m.winnerMethod
		return nil
	})
	if m.winnerLocal {
		if m.sessions == nil {
			m.sessions = make(map[string]*playback.Session)
		}
		m.sessions["upstream-winner"] = &playback.Session{ID: "upstream-winner", PlayMethod: method}
	}
	return m.testCompatSessionManager.StartSession(userID, profileID, fileID, method, transcodeAudio)
}
