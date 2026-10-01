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

// compatProfileRefreshPlay drives one Jellyfin play of the 3600s test source
// and counts taste-profile refreshes.
type compatProfileRefreshPlay struct {
	t        *testing.T
	handler  *PlaybackHandler
	sourceID string
	refresh  *countingCompatProfileRefresh
}

func newCompatProfileRefreshPlay(t *testing.T) *compatProfileRefreshPlay {
	t.Helper()
	handler, mgr, _, sourceID := newReportLivenessHandler("upstream-1", true)
	mgr.sessions["upstream-1"].UserID = 1
	mgr.sessions["upstream-1"].ProfileID = "profile-1"
	mgr.sessions["upstream-1"].MediaFileID = 42
	handler.storeProvider = compatTestUserStoreProvider{store: newJellycompatUserStore(t)}
	refresh := &countingCompatProfileRefresh{}
	handler.profileStaler = refresh
	handler.profileRefreshRequester = refresh
	return &compatProfileRefreshPlay{t: t, handler: handler, sourceID: sourceID, refresh: refresh}
}

// report posts a progress or Stopped report. positionTicks is the raw JSON
// value for PositionTicks; an empty string omits the field.
func (p *compatProfileRefreshPlay) report(stop bool, positionTicks string) {
	p.t.Helper()
	body := fmt.Sprintf(`{"PlaySessionId":"play-1","MediaSourceId":%q}`, p.sourceID)
	if positionTicks != "" {
		body = fmt.Sprintf(`{"PlaySessionId":"play-1","MediaSourceId":%q,"PositionTicks":%s}`, p.sourceID, positionTicks)
	}
	req := httptest.NewRequest(http.MethodPost, "/Sessions/Playing/Progress", strings.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey,
		&Session{Token: "token-1", StreamAppUserID: 1, ProfileID: "profile-1"}))
	rec := httptest.NewRecorder()
	if stop {
		p.handler.HandleSessionPlayingStopped(rec, req)
	} else {
		p.handler.HandleSessionPlayingProgress(rec, req)
	}
	if rec.Code != http.StatusNoContent {
		p.t.Fatalf("report %s (stop=%v): status = %d, body = %s", body, stop, rec.Code, rec.Body.String())
	}
}

func (p *compatProfileRefreshPlay) reportAt(stop bool, seconds int64) {
	p.t.Helper()
	p.report(stop, fmt.Sprint(seconds*10_000_000))
}

func (p *compatProfileRefreshPlay) assertRefreshes(stage string, want int) {
	p.t.Helper()
	if p.refresh.staled != want || p.refresh.requested != want {
		p.t.Fatalf("%s: stale marks = %d, refresh requests = %d, want %d each", stage, p.refresh.staled, p.refresh.requested, want)
	}
}

// TestPlaybackReportRefreshesTasteProfileOnlyOnCompletionAndStop is the
// Jellyfin twin of the native rule: a progress report that only advances the
// position neither marks the taste profile stale nor queues a rebuild; the
// report that crosses the watched threshold and the Stopped report each do
// once.
func TestPlaybackReportRefreshesTasteProfileOnlyOnCompletionAndStop(t *testing.T) {
	const positionOnlyPings = 100
	play := newCompatProfileRefreshPlay(t)

	// The source runs 3600s; the default watched threshold is 90% (3240s).
	for i := range positionOnlyPings {
		play.reportAt(false, int64(600+10*i))
	}
	play.assertRefreshes(fmt.Sprintf("after %d position-only reports", positionOnlyPings), 0)

	for _, seconds := range []int64{3230, 3250, 3260, 3270} {
		play.reportAt(false, seconds)
	}
	play.assertRefreshes("after the completion crossing", 1)

	play.reportAt(true, 3280)
	play.assertRefreshes("after the Stopped report", 2)
}

// TestPlaybackReportStoppedWithoutPositionRefreshesTasteProfile covers a
// Stopped report that writes no progress. The play's earlier reports already
// wrote its progress, and teardown does not run the native stop finalizer, so
// the Stopped report is the only refresh the play gets.
func TestPlaybackReportStoppedWithoutPositionRefreshesTasteProfile(t *testing.T) {
	for _, tc := range []struct {
		name          string
		positionTicks string
	}{
		{name: "PositionTicks omitted", positionTicks: ""},
		{name: "PositionTicks zero", positionTicks: "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			play := newCompatProfileRefreshPlay(t)
			for i := range 20 {
				play.reportAt(false, int64(600+10*i))
			}
			play.assertRefreshes("after 20 position-only reports", 0)

			play.report(true, tc.positionTicks)
			play.assertRefreshes("after the Stopped report", 1)
		})
	}
}
