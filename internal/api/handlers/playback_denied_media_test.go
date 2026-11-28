package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// A media request for a session the deny marker ended answers the way
// progress and replan answer for the same end (issue #5500): 410 only when an
// administrator or the paused timeout ended it, so the player leaves, and 404
// for any other end, so a player resuming after an expiry starts over at its
// position instead of giving up. The marker withholds the bytes either way.
func TestDeniedSessionMediaAnswersByHowTheSessionEnded(t *testing.T) {
	for _, tc := range []struct {
		name    string
		stopID  func(sessionID string) string
		owner   int
		profile string
		want    int
	}{
		{name: "idle expiry", stopID: expiredStopID, owner: 1, want: http.StatusNotFound},
		{name: "client stop", stopID: func(string) string { return uuid.NewString() }, owner: 1, want: http.StatusNotFound},
		{name: "abort", stopID: abortStopID, owner: 1, want: http.StatusNotFound},
		{name: "administrator", stopID: adminStopID, owner: 1, want: http.StatusGone},
		{name: "paused timeout", stopID: pausedTimeoutStopID, owner: 1, want: http.StatusGone},
		// Only the owner learns how a session ended.
		{name: "administrator, another account", stopID: adminStopID, owner: 2, want: http.StatusNotFound},
		{name: "administrator, another profile", stopID: adminStopID, owner: 1, profile: "profile-2", want: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sessionID := uuid.NewString()
			profile := tc.profile
			if profile == "" {
				profile = "profile-1"
			}
			store := playback.NewMemoryPlanStoreV3()
			deny := relayStreamDeny(t)
			if err := store.SaveAttempt(context.Background(), playback.AttemptRecordV3{
				PlaybackAttemptID: uuid.NewString(), SessionID: sessionID, UserID: tc.owner, ProfileID: profile,
				RequestedMediaFileID: 100, EffectiveMediaFileID: 100, ExpiresAt: time.Now().Add(time.Hour),
			}); err != nil {
				t.Fatal(err)
			}
			claimNativeEnd(context.Background(), store, deny, sessionID, tc.stopID(sessionID)).finish(context.Background(), sessionID, "")

			stream := NewStreamHandler(playback.NewSessionManager(0, 0), nil)
			stream.StreamDeny = deny
			stream.PlanStoreV3 = store
			direct := httptest.NewRecorder()
			stream.HandleStream(direct, playbackTestRequest(http.MethodGet, "/api/v2/stream/"+sessionID, nil, map[string]string{"session_id": sessionID}))

			hls := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
			hls.StreamDeny = deny
			hls.PlanStoreV3 = store
			manifest := httptest.NewRecorder()
			hls.HandleGetTranscodeManifest(manifest, playbackTestRequest(http.MethodGet,
				"/api/v2/playback/transcode/"+sessionID+"/master.m3u8", nil, map[string]string{"session_id": sessionID}))

			if direct.Code != tc.want || manifest.Code != tc.want {
				t.Fatalf("direct stream = %d (%s), HLS manifest = %d (%s), want %d",
					direct.Code, direct.Body.String(), manifest.Code, manifest.Body.String(), tc.want)
			}
		})
	}
}
