package jellycompat

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type countingCompatProfileRefresh struct {
	staled    int
	requested int
}

func (c *countingCompatProfileRefresh) MarkProfileStale(context.Context, int, string) error {
	c.staled++
	return nil
}

func (c *countingCompatProfileRefresh) RequestProfileRefresh(context.Context, int, string) {
	c.requested++
}

// TestPlaybackReportRefreshesTasteProfileOnlyOnCompletionAndStop is the
// Jellyfin twin of the native rule: a progress report that only advances the
// position neither marks the taste profile stale nor queues a rebuild; the
// report that crosses the watched threshold and the Stopped report each do
// once.
func TestPlaybackReportRefreshesTasteProfileOnlyOnCompletionAndStop(t *testing.T) {
	const positionOnlyPings = 100
	handler, mgr, _, sourceID := newReportLivenessHandler("upstream-1", true)
	mgr.sessions["upstream-1"].UserID = 1
	mgr.sessions["upstream-1"].ProfileID = "profile-1"
	mgr.sessions["upstream-1"].MediaFileID = 42
	handler.storeProvider = compatTestUserStoreProvider{store: newJellycompatUserStore(t)}
	refresh := &countingCompatProfileRefresh{}
	handler.profileStaler = refresh
	handler.profileRefreshRequester = refresh

	report := func(stop bool, seconds int64) {
		t.Helper()
		body := fmt.Sprintf(`{"PlaySessionId":"play-1","MediaSourceId":%q,"PositionTicks":%d}`, sourceID, seconds*10_000_000)
		req := httptest.NewRequest(http.MethodPost, "/Sessions/Playing/Progress", strings.NewReader(body))
		req = req.WithContext(context.WithValue(req.Context(), compatSessionKey,
			&Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"}))
		rec := httptest.NewRecorder()
		if stop {
			handler.HandleSessionPlayingStopped(rec, req)
		} else {
			handler.HandleSessionPlayingProgress(rec, req)
		}
		if rec.Code != http.StatusNoContent {
			t.Fatalf("report at %ds (stop=%v): status = %d, body = %s", seconds, stop, rec.Code, rec.Body.String())
		}
	}
	assertRefreshes := func(stage string, want int) {
		t.Helper()
		if refresh.staled != want || refresh.requested != want {
			t.Fatalf("%s: stale marks = %d, refresh requests = %d, want %d each", stage, refresh.staled, refresh.requested, want)
		}
	}

	// The source runs 3600s; the default watched threshold is 90% (3240s).
	for i := range positionOnlyPings {
		report(false, int64(600+10*i))
	}
	assertRefreshes(fmt.Sprintf("after %d position-only reports", positionOnlyPings), 0)

	for _, seconds := range []int64{3230, 3250, 3260, 3270} {
		report(false, seconds)
	}
	assertRefreshes("after the completion crossing", 1)

	report(true, 3280)
	assertRefreshes("after the Stopped report", 2)
}
