package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

type countingProfileStaler struct{ calls int }

func (c *countingProfileStaler) MarkProfileStale(context.Context, int, string) error {
	c.calls++
	return nil
}

// playbackRefreshDriver plays one movie through a native playback surface.
type playbackRefreshDriver struct {
	ping func(t *testing.T, position float64)
	stop func(t *testing.T)
}

func newV1PlaybackRefreshDriver(t *testing.T, h *PlaybackHandler, manager *playback.SessionManager, file *models.MediaFile) playbackRefreshDriver {
	session, err := manager.StartSession(1, "profile-1", file.ID, playback.PlayDirect, false)
	if err != nil {
		t.Fatal(err)
	}
	params := map[string]string{"session_id": session.ID}
	return playbackRefreshDriver{
		ping: func(t *testing.T, position float64) {
			rr := httptest.NewRecorder()
			body := fmt.Appendf(nil, `{"position":%g,"is_paused":false}`, position)
			h.HandleUpdateProgress(rr, playbackTestRequest(http.MethodPost, "/", body, params))
			if rr.Code != http.StatusNoContent {
				t.Fatalf("v1 progress at %v: status = %d, body = %s", position, rr.Code, rr.Body.String())
			}
		},
		stop: func(t *testing.T) {
			rr := httptest.NewRecorder()
			h.HandleStopPlayback(rr, playbackTestRequest(http.MethodDelete, "/", nil, params))
			if rr.Code != http.StatusNoContent {
				t.Fatalf("v1 stop: status = %d, body = %s", rr.Code, rr.Body.String())
			}
		},
	}
}

func newV2PlaybackRefreshDriver(t *testing.T, h *PlaybackHandler, manager *playback.SessionManager, file *models.MediaFile) playbackRefreshDriver {
	session, err := manager.StartSession(1, "profile-1", file.ID, playback.PlayDirect, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.PlanStoreV3.SaveAttempt(context.Background(), playback.AttemptRecordV3{
		PlaybackAttemptID: uuid.NewString(), SessionID: session.ID, UserID: 1, ProfileID: "profile-1",
		RequestedMediaFileID: file.ID, EffectiveMediaFileID: file.ID, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	ctx := newAuthorizedPlaybackContext()
	caller := PlaybackCaller{UserID: 1, ProfileID: "profile-1", InstallationID: serviceInstallation}
	var sequence int64
	return playbackRefreshDriver{
		ping: func(t *testing.T, position float64) {
			sequence++
			view, err := h.ApplyProgressV2(ctx, caller, session.ID, PlaybackProgressCommand{Sequence: sequence, Position: position})
			if err != nil || view.Outcome != PlaybackOutcomeApplied {
				t.Fatalf("v2 progress at %v: view = %+v, err = %v", position, view, err)
			}
		},
		stop: func(t *testing.T) {
			if _, err := h.StopPlaybackV2(ctx, caller, session.ID, PlaybackStopCommand{StopID: uuid.NewString()}); err != nil {
				t.Fatalf("v2 stop: %v", err)
			}
		},
	}
}

// TestPlaybackProgressRefreshesTasteProfileOnlyOnCompletionAndStop pins the
// taste-profile refresh to changes that move the profile: a heartbeat that
// only advances the position neither marks the profile stale nor queues a
// rebuild; the heartbeat that crosses the watched threshold and the stop each
// do once. Both native surfaces share the progress writer.
func TestPlaybackProgressRefreshesTasteProfileOnlyOnCompletionAndStop(t *testing.T) {
	const positionOnlyPings = 100
	// 90% of 3600s is the default watched threshold (3240s).
	file := &models.MediaFile{ID: 42, ContentID: "movie-1", Duration: 3600}

	for name, newDriver := range map[string]func(*testing.T, *PlaybackHandler, *playback.SessionManager, *models.MediaFile) playbackRefreshDriver{
		"v1": newV1PlaybackRefreshDriver,
		"v2": newV2PlaybackRefreshDriver,
	} {
		t.Run(name, func(t *testing.T) {
			manager := playback.NewSessionManager(0, 0)
			h := NewPlaybackHandler(manager, testPlaybackFileResolver{file: file})
			h.InstallationID = serviceInstallation
			h.StoreProvider = testUserStoreProvider{store: newPlaybackTestStore(t)}
			staler := &countingProfileStaler{}
			requester := &countingProfileRefresher{}
			h.SetProfileStaler(staler)
			h.SetProfileRefreshRequester(requester)
			driver := newDriver(t, h, manager, file)
			assertRefreshes := func(stage string, want int) {
				t.Helper()
				if staler.calls != want || requester.calls != want {
					t.Fatalf("%s: stale marks = %d, refresh requests = %d, want %d each", stage, staler.calls, requester.calls, want)
				}
			}

			for i := range positionOnlyPings {
				driver.ping(t, float64(600+10*i))
			}
			assertRefreshes(fmt.Sprintf("after %d position-only pings", positionOnlyPings), 0)

			// 3230s is below the threshold, 3250s crosses it, and the later
			// pings play the credits of an already-watched item.
			for _, position := range []float64{3230, 3250, 3260, 3270} {
				driver.ping(t, position)
			}
			assertRefreshes("after the completion crossing", 1)

			driver.stop(t)
			assertRefreshes("after stop", 2)
		})
	}
}
