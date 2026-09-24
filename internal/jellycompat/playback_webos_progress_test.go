package jellycompat

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// webOS direct play reports item/source IDs but an empty PlaySessionId.
// Dropping these reports freezes both the resume point and session liveness.
func TestWebOSProgressPersistsResumeWithoutPlaySessionID(t *testing.T) {
	h, mgr, item, source := newReportLivenessHandler("upstream-1", true)
	store := newJellycompatUserStore(t)
	h.storeProvider = compatTestUserStoreProvider{store: store}
	for _, sample := range []struct {
		ticks  int64
		paused bool
		want   float64
	}{
		{15377280000, false, 1537.728},
		{15523810000, true, 1552.381},
		{14000000000, true, 1400}, // A deliberate backward seek must also persist.
	} {
		body := fmt.Sprintf(`{"PlaySessionId":"","ItemId":%q,"MediaSourceId":%q,"PositionTicks":%d,"IsPaused":%t}`, item, source, sample.ticks, sample.paused)
		rec := postProgressReport(h, body)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status=%d: %s", rec.Code, rec.Body.String())
		}
		progress, err := store.GetProgress(t.Context(), "profile-1", "movie-1")
		if err != nil || progress == nil || progress.PositionSeconds != sample.want {
			t.Fatalf("saved progress=%+v err=%v, want %v", progress, err, sample.want)
		}
		if len(mgr.progressUpdates) == 0 || mgr.progressUpdates[len(mgr.progressUpdates)-1].position != sample.want {
			t.Fatalf("native progress=%+v, want %v", mgr.progressUpdates, sample.want)
		}
	}
}

func TestWebOSProgressIgnoresUnstartedNegotiation(t *testing.T) {
	h, mgr, item, source := newReportLivenessHandler("upstream-1", true)
	original, _ := h.playbackStore.Get("play-1")
	pending := *original
	pending.ID = "pending"
	pending.UpstreamSessionID = ""
	h.playbackStore.Put(pending)
	for range 30 {
		postProgressReport(h, fmt.Sprintf(`{"ItemId":%q,"MediaSourceId":%q,"PositionTicks":15523810000}`, item, source))
	}
	if len(mgr.progressUpdates) != 30 {
		t.Fatalf("updates=%d, want 30", len(mgr.progressUpdates))
	}
}

func TestWebOSProgressRejectsUnidentifiablePlayback(t *testing.T) {
	for _, kind := range []string{"foreign token", "mixed source", "missing route", "ambiguous active sessions"} {
		t.Run(kind, func(t *testing.T) {
			h, mgr, item, source := newReportLivenessHandler("upstream-1", true)
			auth := &Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"}
			switch kind {
			case "foreign token":
				auth.Token = "other-token"
			case "mixed source":
				source = "other-source"
			case "missing route":
				item = ""
				source = ""
			case "ambiguous active sessions":
				sibling, _ := h.playbackStore.Get("play-1")
				sibling.ID = "play-2"
				sibling.UpstreamSessionID = "upstream-2"
				h.playbackStore.Put(*sibling)
				mgr.sessions["upstream-2"] = &playback.Session{ID: "upstream-2"}
			}
			rec := httptest.NewRecorder()
			h.HandleSessionPlayingProgress(rec, viewerRequest("POST", "/Sessions/Playing/Progress", fmt.Sprintf(`{"PlaySessionId":"","ItemId":%q,"MediaSourceId":%q,"PositionTicks":15523810000}`, item, source), "", "", auth))
			if len(mgr.progressUpdates) != 0 {
				t.Fatalf("unsafe progress write: %+v", mgr.progressUpdates)
			}
		})
	}
}

func TestWebOSUnidentifiedStopSavesFinalPositionWithoutTearingDownPlayback(t *testing.T) {
	h, mgr, item, source := newReportLivenessHandler("upstream-1", true)
	store := newJellycompatUserStore(t)
	h.storeProvider = compatTestUserStoreProvider{store: store}
	rec := httptest.NewRecorder()
	h.HandleSessionPlayingStopped(rec, viewerRequest("POST", "/Sessions/Playing/Stopped", fmt.Sprintf(`{"PlaySessionId":"","ItemId":%q,"MediaSourceId":%q,"PositionTicks":15523810000}`, item, source), "", "", &Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"}))
	if len(mgr.stopCalls) != 0 {
		t.Fatalf("unidentified stop tore down playback: %v", mgr.stopCalls)
	}
	progress, err := store.GetProgress(t.Context(), "profile-1", "movie-1")
	if err != nil || progress == nil || progress.PositionSeconds != 1552.381 {
		t.Fatalf("stop progress=%+v err=%v, want 1552.381", progress, err)
	}
}

func TestWebOSStaticResumeReusesStartedSession(t *testing.T) {
	h, _, item, source := newReportLivenessHandler("upstream-1", true)
	active, _ := h.playbackStore.Get("play-1")
	pending := *active
	pending.ID = "new-negotiation"
	pending.UpstreamSessionID = ""
	h.playbackStore.Put(pending)
	for range 30 {
		req := httptest.NewRequest("GET", "/stream?Static=true&mediaSourceId="+source, nil)
		got, _, err := h.resolvePlaybackRoute(req, &Session{Token: "token-1"}, item, source)
		if err != nil || got == nil || got.ID != "play-1" {
			t.Fatalf("resume route=%+v err=%v, want existing started session", got, err)
		}
	}
}

func TestWebOSUnidentifiedStopDoesNotChangeAudio(t *testing.T) {
	h, mgr, item, source := newReportLivenessHandler("upstream-1", true)
	rec := httptest.NewRecorder()
	h.HandleSessionPlayingStopped(rec, viewerRequest("POST", "/Sessions/Playing/Stopped", fmt.Sprintf(`{"PlaySessionId":"","ItemId":%q,"MediaSourceId":%q,"PositionTicks":15523810000,"AudioStreamIndex":1}`, item, source), "", "", &Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"}))
	current, _ := h.playbackStore.Get("play-1")
	if len(mgr.audioTrackCalls) != 0 || *current.MediaSources[0].SelectedAudioStreamIndex != 2 {
		t.Fatalf("unidentified stop changed audio: calls=%v source=%+v", mgr.audioTrackCalls, current.MediaSources[0])
	}
}

func TestWebOSDurableLookupSeesOtherReplicas(t *testing.T) {
	pool := newCompatTestPool(t)
	writer := NewDurableCompatPlaybackStore(pool, 0, nil)
	reader := NewDurableCompatPlaybackStore(pool, 0, nil)
	h, mgr, item, source := newReportLivenessHandler("upstream-1", true)
	active, _ := h.playbackStore.Get("play-1")
	active.ID = t.Name() + "-one"
	writer.Put(*active)
	t.Cleanup(func() { writer.Delete(active.ID) })
	h.playbackStore = reader
	body := fmt.Sprintf(`{"ItemId":%q,"MediaSourceId":%q,"PositionTicks":15523810000}`, item, source)
	postProgressReport(h, body)
	if len(mgr.progressUpdates) != 1 {
		t.Fatalf("cross-replica report updates=%d, want 1", len(mgr.progressUpdates))
	}
	// A second API process starts the same item after this reader cached one
	// match. A cache hit must not bypass the durable uniqueness check.
	sibling := *active
	sibling.ID = t.Name() + "-two"
	sibling.UpstreamSessionID = "upstream-2"
	writer.Put(sibling)
	t.Cleanup(func() { writer.Delete(sibling.ID) })
	postProgressReport(h, body)
	if len(mgr.progressUpdates) != 1 {
		t.Fatal("ambiguous cross-replica report updated playback")
	}
	writer.Delete(sibling.ID)
	postProgressReport(h, body)
	if len(mgr.progressUpdates) != 2 {
		t.Fatal("removed remote sibling remained in cached route matching")
	}
}
