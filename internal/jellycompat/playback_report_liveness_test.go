package jellycompat

import (
	"context"
	"errors"
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
		tm:            playback.NewTranscodeManager(),
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

// TestHandlePlaybackReport_ClientPlaySessionIDFallsBackToItemRoute proves a
// progress report whose PlaySessionId is unknown to the server (the client
// generated it because Static=true direct play skipped PlaybackInfo) still
// reaches the upstream session via the item-route fallback — the same
// route-scoped reuse the stream path performs in resolvePlaybackRoute.
// Without the fallback the report is a silent 204 no-op, the Activity View
// position freezes, and stale cleanup later drops the live session.
func TestHandlePlaybackReport_ClientPlaySessionIDFallsBackToItemRoute(t *testing.T) {
	handler, mgr, encodedItemID, sourceID := newReportLivenessHandler("upstream-1", true)

	rec := postProgressReport(handler,
		`{"PlaySessionId":"infuse-client-psid","ItemId":"`+encodedItemID+`","MediaSourceId":"`+sourceID+`","PositionTicks":1234500000}`)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(mgr.progressUpdates) != 1 {
		t.Fatalf("UpdateProgress calls = %d, want 1 (client-generated PlaySessionId must fall back to the item route)", len(mgr.progressUpdates))
	}
	got := mgr.progressUpdates[0]
	if got.sessionID != "upstream-1" {
		t.Fatalf("UpdateProgress session = %q, want upstream-1", got.sessionID)
	}
	if got.position != 123.45 {
		t.Fatalf("UpdateProgress position = %v, want 123.45", got.position)
	}
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

// TestHandlePlaybackReport_RevivesReapedUpstreamSession proves a progress
// report for a play session whose upstream session was reaped by stale cleanup
// recreates the upstream session instead of silently dropping the report, so
// the still-playing client reappears in the admin Activity View.
func TestHandlePlaybackReport_RevivesReapedUpstreamSession(t *testing.T) {
	handler, mgr, _, sourceID := newReportLivenessHandler("upstream-reaped", false)

	rec := postProgressReport(handler,
		`{"PlaySessionId":"play-1","MediaSourceId":"`+sourceID+`","PositionTicks":9000000000}`)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if mgr.startCalls != 1 {
		t.Fatalf("StartSession calls = %d, want 1 (reaped upstream session must be recreated)", mgr.startCalls)
	}
	updated, ok := handler.playbackStore.Get("play-1")
	if !ok {
		t.Fatal("expected play session to remain in store")
	}
	if updated.UpstreamSessionID != "upstream-started" {
		t.Fatalf("UpstreamSessionID = %q, want upstream-started", updated.UpstreamSessionID)
	}
	last := mgr.progressUpdates[len(mgr.progressUpdates)-1]
	if last.sessionID != "upstream-started" || last.position != 900 {
		t.Fatalf("last progress update = %+v, want session upstream-started at 900s", last)
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

// TestEnsureUpstreamPlayback_RecreatesReapedSession proves the stream path
// heals a reaped upstream session: the next range request must recreate it
// rather than early-returning a dangling UpstreamSessionID forever.
func TestEnsureUpstreamPlayback_RecreatesReapedSession(t *testing.T) {
	handler, mgr, _, _ := newReportLivenessHandler("upstream-reaped", false)
	playSession, _ := handler.playbackStore.Get("play-1")
	source := playSession.MediaSources[0]

	got, err := handler.ensureUpstreamPlayback(context.Background(),
		&Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"},
		"play-1", source, "direct")
	if err != nil {
		t.Fatalf("ensureUpstreamPlayback: %v", err)
	}
	if mgr.startCalls != 1 {
		t.Fatalf("StartSession calls = %d, want 1 (reaped upstream must be recreated)", mgr.startCalls)
	}
	if got.UpstreamSessionID != "upstream-started" {
		t.Fatalf("UpstreamSessionID = %q, want upstream-started", got.UpstreamSessionID)
	}
}

// TestEnsureUpstreamPlayback_ReusesLiveSession guards the reuse path: a live
// upstream session must not be torn down or recreated by subsequent requests.
func TestEnsureUpstreamPlayback_ReusesLiveSession(t *testing.T) {
	handler, mgr, _, _ := newReportLivenessHandler("upstream-1", true)
	playSession, _ := handler.playbackStore.Get("play-1")
	source := playSession.MediaSources[0]

	got, err := handler.ensureUpstreamPlayback(context.Background(),
		&Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"},
		"play-1", source, "direct")
	if err != nil {
		t.Fatalf("ensureUpstreamPlayback: %v", err)
	}
	if mgr.startCalls != 0 {
		t.Fatalf("StartSession calls = %d, want 0 (live upstream must be reused)", mgr.startCalls)
	}
	if got.UpstreamSessionID != "upstream-1" {
		t.Fatalf("UpstreamSessionID = %q, want upstream-1", got.UpstreamSessionID)
	}
}

// TestHandlePlaybackReport_AliasBindsDeterministicallyAmongDuplicates proves
// that when two play sessions for the same item live under one compat token,
// a report carrying the client's own PlaySessionId binds the session that
// recorded it as an alias — not whichever the route scan happens to hit first.
func TestHandlePlaybackReport_AliasBindsDeterministicallyAmongDuplicates(t *testing.T) {
	handler, mgr, encodedItemID, sourceID := newReportLivenessHandler("upstream-1", true)
	// A second live play of the same item under the same token, no alias.
	other, _ := handler.playbackStore.Get("play-1")
	sibling := *other
	sibling.ID = "play-2"
	sibling.UpstreamSessionID = "upstream-2"
	handler.playbackStore.Put(sibling)
	mgr.sessions["upstream-2"] = &playback.Session{ID: "upstream-2", PlayMethod: playback.PlayDirect}
	// The stream path recorded the client's PlaySessionId on play-1.
	if err := handler.playbackStore.Update("play-1", func(current *PlaybackSession) error {
		current.ClientPlaySessionID = "infuse-client-psid"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	rec := postProgressReport(handler,
		`{"PlaySessionId":"infuse-client-psid","ItemId":"`+encodedItemID+`","MediaSourceId":"`+sourceID+`","PositionTicks":1234500000}`)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(mgr.progressUpdates) != 1 {
		t.Fatalf("UpdateProgress calls = %d, want 1", len(mgr.progressUpdates))
	}
	if got := mgr.progressUpdates[0].sessionID; got != "upstream-1" {
		t.Fatalf("UpdateProgress session = %q, want upstream-1 (the aliased session)", got)
	}
}

// TestHandleVideoStream_StaticRecordsClientPlaySessionAlias proves the stream
// path records the client-generated PlaySessionId so subsequent playback
// reports resolve the session without relying on ItemId/route matching.
func TestHandleVideoStream_StaticRecordsClientPlaySessionAlias(t *testing.T) {
	handler, encodedID, _ := newStaticDirectPlayHandler(t)

	rec := serveStaticStream(handler, encodedID, "Static=true&PlaySessionId=infuse-client-psid")
	if rec.Code != 200 {
		t.Fatalf("expected status 200; got %d, body=%s", rec.Code, rec.Body.String())
	}

	playSession, ok := handler.playbackStore.FindByClientPlaySessionID("token-1", "infuse-client-psid")
	if !ok {
		t.Fatal("expected the static play session to record the client PlaySessionId alias")
	}
	if playSession.UpstreamSessionID != "upstream-started" {
		t.Fatalf("UpstreamSessionID = %q, want upstream-started", playSession.UpstreamSessionID)
	}

	// The full Infuse loop: a progress report carrying only the client id
	// (no ItemId) must reach the upstream session via the alias.
	mgr := handler.sessionMgr.(*testCompatSessionManager)
	rep := postProgressReport(handler, `{"PlaySessionId":"infuse-client-psid","PositionTicks":600000000}`)
	if rep.Code != http.StatusNoContent {
		t.Fatalf("report status = %d", rep.Code)
	}
	if len(mgr.progressUpdates) != 1 || mgr.progressUpdates[0].sessionID != "upstream-started" {
		t.Fatalf("progress updates = %+v, want one update on upstream-started", mgr.progressUpdates)
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

// TestHandlePlaybackReport_StopViaAmbiguousRouteDoesNotTearDown proves route
// fallback refuses to choose when the same item/source is playing twice under
// one token.
func TestHandlePlaybackReport_StopViaAmbiguousRouteDoesNotTearDown(t *testing.T) {
	handler, mgr, encodedItemID, sourceID := newReportLivenessHandler("upstream-1", true)
	original, ok := handler.playbackStore.Get("play-1")
	if !ok {
		t.Fatal("original play session missing")
	}
	sibling := *original
	sibling.ID = "play-2"
	sibling.UpstreamSessionID = "upstream-2"
	handler.playbackStore.Put(sibling)
	mgr.sessions["upstream-2"] = &playback.Session{ID: "upstream-2", PlayMethod: playback.PlayDirect}

	req := httptest.NewRequest(http.MethodPost, "/Sessions/Playing/Stopped", strings.NewReader(
		`{"PlaySessionId":"never-seen-psid","ItemId":"`+encodedItemID+`","MediaSourceId":"`+sourceID+`","PositionTicks":9000000000}`))
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey,
		&Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"}))
	rec := httptest.NewRecorder()
	handler.HandleSessionPlayingStopped(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(mgr.stopCalls) != 0 || len(mgr.progressUpdates) != 0 {
		t.Fatalf("ambiguous route mutated playback: stops=%v progress=%+v", mgr.stopCalls, mgr.progressUpdates)
	}
	if _, ok := handler.playbackStore.Get("play-1"); !ok {
		t.Fatal("ambiguous route removed play-1")
	}
	if _, ok := handler.playbackStore.Get("play-2"); !ok {
		t.Fatal("ambiguous route removed play-2")
	}
}

func TestHandlePlaybackReport_StopRejectsMixedRouteIdentifiers(t *testing.T) {
	handler, mgr, encodedItemID, _ := newReportLivenessHandler("upstream-1", true)
	original, ok := handler.playbackStore.Get("play-1")
	if !ok {
		t.Fatal("original play session missing")
	}
	sibling := *original
	sibling.ID = "play-2"
	sibling.ItemID = "movie-2"
	sibling.RouteItemID = handler.codec.EncodeStringID(EncodedIDItem, "movie-2")
	sibling.UpstreamSessionID = "upstream-2"
	sibling.MediaSources = []PlaybackMediaSource{{ID: "source-2", FileID: 43}}
	handler.playbackStore.Put(sibling)
	mgr.sessions["upstream-2"] = &playback.Session{ID: "upstream-2", PlayMethod: playback.PlayDirect}

	req := httptest.NewRequest(http.MethodPost, "/Sessions/Playing/Stopped", strings.NewReader(
		`{"PlaySessionId":"stale-play-id","ItemId":"`+encodedItemID+`","MediaSourceId":"source-2","PositionTicks":9000000000}`))
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey,
		&Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"}))
	rec := httptest.NewRecorder()
	handler.HandleSessionPlayingStopped(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(mgr.stopCalls) != 0 || len(mgr.progressUpdates) != 0 {
		t.Fatalf("mixed route identifiers mutated playback: stops=%v progress=%+v", mgr.stopCalls, mgr.progressUpdates)
	}
	if _, ok := handler.playbackStore.Get("play-1"); !ok {
		t.Fatal("mixed route identifiers removed play-1")
	}
	if _, ok := handler.playbackStore.Get("play-2"); !ok {
		t.Fatal("mixed route identifiers removed play-2")
	}
}

// TestEnsureUpstreamPlayback_ReviveClosesStaleTranscode proves that when a
// reaped upstream session is recreated under the same play method, any
// transcode still keyed to the stale upstream id is closed first — otherwise
// a second ffmpeg would start alongside the orphaned one.
func TestEnsureUpstreamPlayback_ReviveClosesStaleTranscode(t *testing.T) {
	handler, mgr, _, _ := newReportLivenessHandler("upstream-reaped", false)
	// Stale transcode still keyed by the reaped id (never-started session; Close is a no-op).
	staleTranscode := &playback.TranscodeSession{}
	handler.tm.RegisterTranscodeSession("upstream-reaped", staleTranscode)

	playSession, _ := handler.playbackStore.Get("play-1")
	source := playSession.MediaSources[0]
	if _, err := handler.ensureUpstreamPlayback(context.Background(),
		&Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"},
		"play-1", source, "direct"); err != nil {
		t.Fatalf("ensureUpstreamPlayback: %v", err)
	}

	if mgr.startCalls != 1 {
		t.Fatalf("StartSession calls = %d, want 1", mgr.startCalls)
	}
	if handler.tm.GetTranscodeSession("upstream-reaped") != nil {
		t.Fatal("expected the stale transcode entry to be closed before recreating the upstream session")
	}
}

// TestHandlePlaybackReport_AliasResolvedAudioSelectionApplies proves an
// audio-track change carried by an alias-resolved report is applied: store
// mutations must key on the resolved play session id, not the client's own
// PlaySessionId (which is not a store key for Static direct play).
func TestHandlePlaybackReport_AliasResolvedAudioSelectionApplies(t *testing.T) {
	handler, mgr, _, sourceID := newReportLivenessHandler("upstream-1", true)
	if err := handler.playbackStore.Update("play-1", func(current *PlaybackSession) error {
		current.ClientPlaySessionID = "infuse-client-psid"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// testCompatSource pre-selects audio stream index 2; switch to index 1.
	rec := postProgressReport(handler,
		`{"PlaySessionId":"infuse-client-psid","MediaSourceId":"`+sourceID+`","AudioStreamIndex":1,"PositionTicks":600000000}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	updated, ok := handler.playbackStore.Get("play-1")
	if !ok {
		t.Fatal("expected play session in store")
	}
	if updated.MediaSources[0].SelectedAudioStreamIndex == nil || *updated.MediaSources[0].SelectedAudioStreamIndex != 1 {
		t.Fatalf("SelectedAudioStreamIndex = %v, want 1 (audio change must apply to the resolved session)", updated.MediaSources[0].SelectedAudioStreamIndex)
	}
	if len(mgr.audioTrackCalls) != 1 {
		t.Fatalf("upstream audio track calls = %d, want 1", len(mgr.audioTrackCalls))
	}
}

// TestHandlePlaybackReport_DuplicateAliasFallsBackToRoute proves a client that
// reuses one PlaySessionId across different items cannot misbind reports: the
// ambiguous alias is skipped and the report resolves by ItemId route instead,
// and a Stopped report with only the ambiguous alias (no ItemId) tears nothing
// down.
func TestHandlePlaybackReport_DuplicateAliasFallsBackToRoute(t *testing.T) {
	handler, mgr, encodedItemID, sourceID := newReportLivenessHandler("upstream-1", true)
	// Same client alias on a second live session for a DIFFERENT item.
	otherItemID := handler.codec.EncodeStringID(EncodedIDItem, "movie-2")
	handler.playbackStore.Put(PlaybackSession{
		ID:                  "play-2",
		CompatToken:         "token-1",
		ItemID:              "movie-2",
		RouteItemID:         otherItemID,
		ClientPlaySessionID: "reused-psid",
		UpstreamSessionID:   "upstream-2",
		UpstreamPlayMethod:  "direct",
	})
	mgr.sessions["upstream-2"] = &playback.Session{ID: "upstream-2", PlayMethod: playback.PlayDirect}
	if err := handler.playbackStore.Update("play-1", func(current *PlaybackSession) error {
		current.ClientPlaySessionID = "reused-psid"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Progress with the duplicate alias plus ItemId: must bind play-1 by route.
	rec := postProgressReport(handler,
		`{"PlaySessionId":"reused-psid","ItemId":"`+encodedItemID+`","MediaSourceId":"`+sourceID+`","PositionTicks":600000000}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(mgr.progressUpdates) != 1 || mgr.progressUpdates[0].sessionID != "upstream-1" {
		t.Fatalf("progress updates = %+v, want one update on upstream-1 via item route", mgr.progressUpdates)
	}

	// Stopped with only the duplicate alias: ambiguous, must tear nothing down.
	req := httptest.NewRequest(http.MethodPost, "/Sessions/Playing/Stopped",
		strings.NewReader(`{"PlaySessionId":"reused-psid","PositionTicks":600000000}`))
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey,
		&Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"}))
	rr := httptest.NewRecorder()
	handler.HandleSessionPlayingStopped(rr, req)
	if len(mgr.stopCalls) != 0 {
		t.Fatalf("StopSession calls = %v, want none for an ambiguous duplicate alias", mgr.stopCalls)
	}
}

// TestHandlePlaybackReport_AliasContradictedByItemFallsBack proves an alias
// whose session disagrees with the report's ItemId is rejected (a stale or
// reused client id) and the report resolves by route instead.
func TestHandlePlaybackReport_AliasContradictedByItemFallsBack(t *testing.T) {
	handler, mgr, _, _ := newReportLivenessHandler("upstream-1", true)
	// Alias points at play-1 (movie-1), but the report is about movie-2.
	if err := handler.playbackStore.Update("play-1", func(current *PlaybackSession) error {
		current.ClientPlaySessionID = "stale-psid"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	otherItemID := handler.codec.EncodeStringID(EncodedIDItem, "movie-2")
	handler.playbackStore.Put(PlaybackSession{
		ID:                 "play-2",
		CompatToken:        "token-1",
		ItemID:             "movie-2",
		RouteItemID:        otherItemID,
		UpstreamSessionID:  "upstream-2",
		UpstreamPlayMethod: "direct",
	})
	mgr.sessions["upstream-2"] = &playback.Session{ID: "upstream-2", PlayMethod: playback.PlayDirect}

	rec := postProgressReport(handler,
		`{"PlaySessionId":"stale-psid","ItemId":"`+otherItemID+`","PositionTicks":600000000}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(mgr.progressUpdates) != 1 || mgr.progressUpdates[0].sessionID != "upstream-2" {
		t.Fatalf("progress updates = %+v, want one update on upstream-2 (the reported item)", mgr.progressUpdates)
	}
}

// racingStartSessionManager simulates a concurrent request winning the
// upstream-attach race: while this caller is inside StartSession, the store's
// play session is re-pointed at a different upstream id/method.
type racingStartSessionManager struct {
	testCompatSessionManager
	store        CompatPlaybackStore
	winnerMethod string
}

func (m *racingStartSessionManager) StartSession(userID int, profileID string, fileID int, method playback.PlayMethod, transcodeAudio bool) (*playback.Session, error) {
	_ = m.store.Update("play-1", func(current *PlaybackSession) error {
		current.UpstreamSessionID = "upstream-winner"
		current.UpstreamPlayMethod = m.winnerMethod
		return nil
	})
	return m.testCompatSessionManager.StartSession(userID, profileID, fileID, method, transcodeAudio)
}

// TestEnsureUpstreamPlayback_CASLoserAdoptsSameMethodWinner proves the loser
// of a concurrent attach race stops its own freshly created session and
// adopts the winner when the winner serves the same play method.
func TestEnsureUpstreamPlayback_CASLoserAdoptsSameMethodWinner(t *testing.T) {
	handler, _, _, _ := newReportLivenessHandler("", false)
	base := handler.sessionMgr.(*testCompatSessionManager)
	racer := &racingStartSessionManager{
		testCompatSessionManager: *base,
		store:                    handler.playbackStore,
		winnerMethod:             "direct",
	}
	handler.sessionMgr = racer
	playSession, _ := handler.playbackStore.Get("play-1")
	source := playSession.MediaSources[0]

	got, err := handler.ensureUpstreamPlayback(context.Background(),
		&Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"},
		"play-1", source, "direct")
	if err != nil {
		t.Fatalf("ensureUpstreamPlayback: %v", err)
	}
	if got.UpstreamSessionID != "upstream-winner" {
		t.Fatalf("UpstreamSessionID = %q, want the concurrent winner", got.UpstreamSessionID)
	}
	if len(racer.stopCalls) != 1 || racer.stopCalls[0] != "upstream-started" {
		t.Fatalf("StopSession calls = %v, want rollback of the loser's session", racer.stopCalls)
	}
}

// TestEnsureUpstreamPlayback_CASLoserRejectsMethodSwitch proves the loser does
// NOT adopt a winner running a different play method — it rolls back its
// session and surfaces a conflict instead of continuing with mismatched
// transcode bookkeeping.
func TestEnsureUpstreamPlayback_CASLoserRejectsMethodSwitch(t *testing.T) {
	handler, _, _, _ := newReportLivenessHandler("", false)
	base := handler.sessionMgr.(*testCompatSessionManager)
	racer := &racingStartSessionManager{
		testCompatSessionManager: *base,
		store:                    handler.playbackStore,
		winnerMethod:             "transcode",
	}
	handler.sessionMgr = racer
	playSession, _ := handler.playbackStore.Get("play-1")
	source := playSession.MediaSources[0]

	_, err := handler.ensureUpstreamPlayback(context.Background(),
		&Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"},
		"play-1", source, "direct")
	if !errors.Is(err, errUpstreamReplaced) {
		t.Fatalf("error = %v, want errUpstreamReplaced", err)
	}
	if len(racer.stopCalls) != 1 || racer.stopCalls[0] != "upstream-started" {
		t.Fatalf("StopSession calls = %v, want rollback of the loser's session", racer.stopCalls)
	}
}

// TestHandleVideoStream_DirectPlayMarksTransport proves the compat stream
// handler marks an in-flight media transport on the upstream session while
// serving, mirroring the native stream handler. Without it, a long-lived
// direct-play range transfer emits no activity and stale cleanup reaps the
// session mid-transfer.
func TestHandleVideoStream_DirectPlayMarksTransport(t *testing.T) {
	handler, encodedID, body := newStaticDirectPlayHandler(t)
	mgr := handler.sessionMgr.(*testCompatSessionManager)

	rec := serveStaticStream(handler, encodedID, "Static=true")
	if rec.Code != 200 {
		t.Fatalf("expected status 200; got %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != body {
		t.Fatalf("expected file content %q; got %q", body, got)
	}

	if len(mgr.beginTransportCalls) != 1 || mgr.beginTransportCalls[0] != "upstream-started" {
		t.Fatalf("BeginTransport calls = %v, want exactly one for upstream-started", mgr.beginTransportCalls)
	}
	if len(mgr.endTransportCalls) != 1 || mgr.endTransportCalls[0] != "upstream-started" {
		t.Fatalf("EndTransport calls = %v, want exactly one for upstream-started", mgr.endTransportCalls)
	}
}
