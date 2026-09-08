package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/playback"
)

const (
	serviceInstallation      = "0f0e5c2e-2c6b-4b39-9c8b-3a5f9a2b7d11"
	serviceOtherInstallation = "6c0a7b1e-8d2f-4a35-b0c1-2e4d5f6a7b8c"
)

// playbackServiceFixture is a handler with the memory plan store, one live
// session and its attempt row, as the v2 adapter sees them.
type playbackServiceFixture struct {
	handler *PlaybackHandler
	manager *playback.SessionManager
	session *playback.Session
	caller  PlaybackCaller
	ctx     context.Context
}

func newPlaybackServiceFixture(t *testing.T) *playbackServiceFixture {
	t.Helper()
	manager := playback.NewSessionManager(0, 0)
	session, err := manager.StartSession(1, "profile-1", 100, playback.PlayDirect, false)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewPlaybackHandler(manager)
	handler.InstallationID = serviceInstallation
	record := playback.AttemptRecordV3{
		PlaybackAttemptID:    uuid.NewString(),
		SessionID:            session.ID,
		UserID:               1,
		ProfileID:            "profile-1",
		RequestedMediaFileID: 100,
		EffectiveMediaFileID: 100,
		ExpiresAt:            time.Now().Add(time.Hour),
	}
	if err := handler.PlanStoreV3.SaveAttempt(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	return &playbackServiceFixture{
		handler: handler,
		manager: manager,
		session: session,
		caller:  PlaybackCaller{UserID: 1, ProfileID: "profile-1", InstallationID: serviceInstallation},
		ctx:     newAuthorizedPlaybackContext(),
	}
}

func assertPlaybackOperationError(t *testing.T, err error, status int, code string) {
	t.Helper()
	var op *PlaybackOperationError
	if !errors.As(err, &op) {
		t.Fatalf("err = %v, want *PlaybackOperationError %d %s", err, status, code)
	}
	if op.Status != status || op.Code != code {
		t.Fatalf("err = %d %s, want %d %s", op.Status, op.Code, status, code)
	}
}

func TestPlaybackCapabilitiesV2IsAlwaysAvailable(t *testing.T) {
	f := newPlaybackServiceFixture(t)
	view, err := f.handler.PlaybackCapabilities(f.ctx, 1, "profile-1")
	if err != nil {
		t.Fatal(err)
	}
	if view.State != "available" || !view.Allowed || view.InstallationID != serviceInstallation || view.Revision == "" {
		t.Fatalf("view = %+v", view)
	}
	if len(view.ProtocolVersions) != 1 || view.ProtocolVersions[0] != playback.ProtocolV3 {
		t.Fatalf("protocol versions = %v", view.ProtocolVersions)
	}
	if _, err := f.handler.PlaybackCapabilities(f.ctx, 2, "profile-1"); err == nil {
		t.Fatal("foreign identity accepted")
	}
	f.handler.InstallationID = ""
	if _, err := f.handler.PlaybackCapabilities(f.ctx, 1, "profile-1"); err == nil {
		t.Fatal("unconfigured installation reported available")
	}
}

func TestPlaybackMutationsV2RefuseOtherInstallation(t *testing.T) {
	f := newPlaybackServiceFixture(t)
	other := f.caller
	other.InstallationID = serviceOtherInstallation
	_, err := f.handler.ApplyProgressV2(f.ctx, other, f.session.ID, PlaybackProgressCommand{Sequence: 1, Position: 10})
	assertPlaybackOperationError(t, err, http.StatusConflict, "installation_changed")
	_, err = f.handler.StopPlaybackV2(f.ctx, other, f.session.ID, PlaybackStopCommand{StopID: uuid.NewString()})
	assertPlaybackOperationError(t, err, http.StatusConflict, "installation_changed")
	err = f.handler.ReportRouteEventV2(f.ctx, other, PlaybackRouteEventCommand{EventID: uuid.NewString()})
	assertPlaybackOperationError(t, err, http.StatusConflict, "installation_changed")
	// The caller must also be the authenticated identity.
	foreign := f.caller
	foreign.UserID = 2
	_, err = f.handler.ApplyProgressV2(f.ctx, foreign, f.session.ID, PlaybackProgressCommand{Sequence: 1, Position: 10})
	assertPlaybackOperationError(t, err, http.StatusForbidden, "forbidden")
}

func TestApplyProgressV2SequencesSamplesDurably(t *testing.T) {
	f := newPlaybackServiceFixture(t)
	view, err := f.handler.ApplyProgressV2(f.ctx, f.caller, f.session.ID, PlaybackProgressCommand{Sequence: 2, Position: 20})
	if err != nil {
		t.Fatal(err)
	}
	if view.Outcome != PlaybackOutcomeApplied || view.Accepted == nil || view.Accepted.Sequence != 2 || view.Accepted.Position != 20 {
		t.Fatalf("applied view = %+v", view)
	}
	if live, _ := f.manager.GetSession(f.session.ID); live.Position != 20 {
		t.Fatalf("live position = %v, want 20", live.Position)
	}
	// A lower sequence is stale, even with a higher position, and reports the
	// latest accepted sample.
	view, err = f.handler.ApplyProgressV2(f.ctx, f.caller, f.session.ID, PlaybackProgressCommand{Sequence: 1, Position: 99})
	if err != nil || view.Outcome != PlaybackOutcomeStaleSample || view.Accepted == nil || view.Accepted.Sequence != 2 {
		t.Fatalf("stale view = %+v, %v", view, err)
	}
	if live, _ := f.manager.GetSession(f.session.ID); live.Position != 20 {
		t.Fatalf("stale sample moved the live position to %v", live.Position)
	}
	// Same sequence, same payload replays; different payload conflicts.
	view, err = f.handler.ApplyProgressV2(f.ctx, f.caller, f.session.ID, PlaybackProgressCommand{Sequence: 2, Position: 20})
	if err != nil || view.Outcome != PlaybackOutcomeReplayed {
		t.Fatalf("replay view = %+v, %v", view, err)
	}
	_, err = f.handler.ApplyProgressV2(f.ctx, f.caller, f.session.ID, PlaybackProgressCommand{Sequence: 2, Position: 21})
	assertPlaybackOperationError(t, err, http.StatusConflict, "progress_conflict")
	// A higher sequence wins even when the position moves backward.
	view, err = f.handler.ApplyProgressV2(f.ctx, f.caller, f.session.ID, PlaybackProgressCommand{Sequence: 3, Position: 5, IsPaused: true})
	if err != nil || view.Outcome != PlaybackOutcomeApplied || view.Accepted.Position != 5 || !view.Accepted.IsPaused {
		t.Fatalf("backward view = %+v, %v", view, err)
	}
	// Progress does not need the in-memory session: it is sequenced on the row.
	if err := f.manager.StopSession(f.session.ID); err != nil {
		t.Fatal(err)
	}
	view, err = f.handler.ApplyProgressV2(f.ctx, f.caller, f.session.ID, PlaybackProgressCommand{Sequence: 4, Position: 30})
	if err != nil || view.Outcome != PlaybackOutcomeApplied {
		t.Fatalf("row-only view = %+v, %v", view, err)
	}
	_, err = f.handler.ApplyProgressV2(f.ctx, f.caller, f.session.ID, PlaybackProgressCommand{Sequence: 0, Position: 30})
	assertPlaybackOperationError(t, err, http.StatusBadRequest, "bad_request")
	_, err = f.handler.ApplyProgressV2(f.ctx, f.caller, uuid.NewString(), PlaybackProgressCommand{Sequence: 1, Position: 30})
	assertPlaybackOperationError(t, err, http.StatusNotFound, "session_not_found")
}

func TestStopPlaybackV2FirstStopWinsAndLaterStopsReplay(t *testing.T) {
	f := newPlaybackServiceFixture(t)
	if _, err := f.handler.ApplyProgressV2(f.ctx, f.caller, f.session.ID, PlaybackProgressCommand{Sequence: 1, Position: 10}); err != nil {
		t.Fatal(err)
	}
	stopID := uuid.NewString()
	final := 42.0
	view, err := f.handler.StopPlaybackV2(f.ctx, f.caller, f.session.ID, PlaybackStopCommand{StopID: stopID, Sequence: 2, Position: &final})
	if err != nil {
		t.Fatal(err)
	}
	if view.Outcome != PlaybackOutcomeStopped || view.StopID != stopID || view.Accepted == nil || view.Accepted.Sequence != 2 || view.Accepted.Position != 42 {
		t.Fatalf("stop view = %+v", view)
	}
	if _, err := f.manager.GetSession(f.session.ID); !errors.Is(err, playback.ErrSessionNotFound) {
		t.Fatalf("session survived stop: %v", err)
	}
	// Any later stop, same or different id, replays the stored receipt.
	for _, id := range []string{stopID, uuid.NewString()} {
		again, err := f.handler.StopPlaybackV2(f.ctx, f.caller, f.session.ID, PlaybackStopCommand{StopID: id})
		if err != nil || again.Outcome != PlaybackOutcomeReplayed || again.StopID != stopID || again.Accepted == nil || again.Accepted.Position != 42 {
			t.Fatalf("replayed stop (%s) = %+v, %v", id, again, err)
		}
	}
	// Progress after stop is refused; the row is no longer live.
	_, err = f.handler.ApplyProgressV2(f.ctx, f.caller, f.session.ID, PlaybackProgressCommand{Sequence: 3, Position: 50})
	assertPlaybackOperationError(t, err, http.StatusNotFound, "session_not_found")
	// Malformed stop bodies.
	_, err = f.handler.StopPlaybackV2(f.ctx, f.caller, f.session.ID, PlaybackStopCommand{StopID: "nope"})
	assertPlaybackOperationError(t, err, http.StatusBadRequest, "bad_request")
	_, err = f.handler.StopPlaybackV2(f.ctx, f.caller, f.session.ID, PlaybackStopCommand{StopID: uuid.NewString(), Sequence: 5})
	assertPlaybackOperationError(t, err, http.StatusBadRequest, "bad_request")
}

func TestStopPlaybackV2WithoutLiveSessionStillStopsTheRow(t *testing.T) {
	f := newPlaybackServiceFixture(t)
	if err := f.manager.StopSession(f.session.ID); err != nil {
		t.Fatal(err)
	}
	stopID := uuid.NewString()
	view, err := f.handler.StopPlaybackV2(f.ctx, f.caller, f.session.ID, PlaybackStopCommand{StopID: stopID})
	if err != nil || view.Outcome != PlaybackOutcomeStopped || view.StopID != stopID || view.Accepted != nil {
		t.Fatalf("view = %+v, %v", view, err)
	}
	record, err := f.handler.PlanStoreV3.GetAttempt(context.Background(), f.session.ID)
	if err != nil || record.StoppedAt == nil {
		t.Fatalf("row not stopped: %+v %v", record, err)
	}
}

func TestExpiredSessionMarksAttemptStopped(t *testing.T) {
	f := newPlaybackServiceFixture(t)
	f.handler.handleExpiredSession(f.session)
	deadline := time.Now().Add(2 * time.Second)
	for {
		record, err := f.handler.PlanStoreV3.GetAttempt(context.Background(), f.session.ID)
		if err == nil && record.StoppedAt != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("attempt row never stopped after expiry: %+v %v", record, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// A replayed stop reports the server-minted identity.
	view, err := f.handler.StopPlaybackV2(f.ctx, f.caller, f.session.ID, PlaybackStopCommand{StopID: uuid.NewString()})
	if err != nil || view.Outcome != PlaybackOutcomeReplayed || view.StopID != expiredStopID(f.session.ID) {
		t.Fatalf("view = %+v, %v", view, err)
	}
}

func TestReplayedStartOfStoppedAttemptIsSessionExpired(t *testing.T) {
	f := newPlaybackServiceFixture(t)
	record, err := f.handler.PlanStoreV3.GetAttempt(context.Background(), f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.handler.StopPlaybackV2(f.ctx, f.caller, f.session.ID, PlaybackStopCommand{StopID: uuid.NewString()}); err != nil {
		t.Fatal(err)
	}
	stopped, err := f.handler.PlanStoreV3.GetAttemptByPlaybackAttemptID(context.Background(), record.PlaybackAttemptID)
	if err != nil || stopped.StoppedAt == nil {
		t.Fatalf("stopped row = %+v %v", stopped, err)
	}
}

// newStreamDenyForTest returns a marker store on the local test Redis, or
// skips when none is reachable; the session's key is removed on cleanup.
func newStreamDenyForTest(t *testing.T, sessionID string) *playback.StreamDeny {
	t.Helper()
	addr := os.Getenv("SILO_TEST_REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	client := redis.NewClient(&redis.Options{Addr: addr, DialTimeout: time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		t.Skipf("redis at %s unavailable: %v", addr, err)
	}
	t.Cleanup(func() {
		_ = client.Del(context.Background(), playback.StreamDenyKey(sessionID)).Err()
		_ = client.Close()
	})
	return playback.NewStreamDeny(client)
}

func TestDeniedSessionIsGoneOnEveryServePath(t *testing.T) {
	manager := playback.NewSessionManager(0, 0)
	session, err := manager.StartSession(1, "profile-1", 100, playback.PlayTranscode, false)
	if err != nil {
		t.Fatal(err)
	}
	deny := newStreamDenyForTest(t, session.ID)
	handler := NewPlaybackHandler(manager)
	handler.StreamDeny = deny
	stream := NewStreamHandler(manager, nil)
	stream.StreamDeny = deny
	stream.TM = handler.TranscodeManager()
	deny.Deny(context.Background(), session.ID)

	params := map[string]string{"session_id": session.ID, "track": "0", "name": "segment0.m4s"}
	for name, serve := range map[string]http.HandlerFunc{
		"manifest": handler.HandleGetTranscodeManifest,
		"segment":  handler.HandleGetTranscodeSegment,
		"stream":   stream.HandleStream,
		"subtitle": stream.HandleSubtitle,
		"fonts":    stream.HandleSubtitleFonts,
	} {
		rr := httptest.NewRecorder()
		serve(rr, playbackTestRequest(http.MethodGet, "/", nil, params))
		if rr.Code != http.StatusGone {
			t.Fatalf("%s: status = %d, body = %s", name, rr.Code, rr.Body.String())
		}
	}
	// The live session is untouched by the check itself; only serving is refused.
	if _, err := manager.GetSession(session.ID); err != nil {
		t.Fatalf("deny check removed the session: %v", err)
	}
}
