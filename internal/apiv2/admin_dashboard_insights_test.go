package apiv2

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type dashboardInsightsStub struct {
	calls   int
	window  int
	limit   int
	refresh bool
	series  *handlers.AdminTimeseries
	err     error
}

func (s *dashboardInsightsStub) ReadAdminTimeseries(_ context.Context, hours int, refresh bool) (*handlers.AdminTimeseries, error) {
	s.calls++
	s.window = hours
	s.refresh = refresh
	return s.series, s.err
}
func (s *dashboardInsightsStub) ReadAdminPlaybackActivity(_ context.Context, hours int, refresh bool) (*handlers.AdminPlaybackActivity, error) {
	s.calls++
	s.window = hours
	s.refresh = refresh
	return &handlers.AdminPlaybackActivity{Hours: hours, BucketSeconds: 3600, From: s.series.From, To: s.series.To}, s.err
}
func (s *dashboardInsightsStub) ReadAdminTopActivity(_ context.Context, days, limit int, refresh bool) (*handlers.AdminTopActivity, error) {
	s.calls++
	s.window = days
	s.limit = limit
	s.refresh = refresh
	return &handlers.AdminTopActivity{Days: days, Limit: limit, Profiles: []handlers.AdminTopProfile{{UserID: 7, ProfileID: "household-profile", Plays: 3}}}, s.err
}
func (s *dashboardInsightsStub) ReadAdminDownloadsStats(_ context.Context, limit int, refresh bool) (*handlers.AdminDownloadsStats, error) {
	s.calls++
	s.limit = limit
	s.refresh = refresh
	return &handlers.AdminDownloadsStats{Limit: limit, ActiveDownloads: 3, TopUsers: []handlers.AdminDownloadsUser{{UserID: 7, Downloads: 3}}}, s.err
}
func TestAdminDashboardInsights(t *testing.T) {
	at := time.Date(2026, 9, 1, 1, 2, 3, 123456789, time.FixedZone("test", 3600))
	s := &dashboardInsightsStub{series: &handlers.AdminTimeseries{ResolutionSeconds: 60, From: at, To: at.Add(time.Hour), Points: []handlers.AdminTimeseriesPoint{{T: at, EgressKbps: 50, DownloadEgressKbps: 20}}}}
	deps := requestDeps(fixtureRequests())
	deps.AdminDashboardInsights = s
	h := NewHandler(deps)
	path := Prefix + "/admin/stats/"
	if r := do(t, h, "GET", path+"timeseries", "", nil); r.Code != 401 || s.calls != 0 {
		t.Fatal(r.Code, s.calls)
	}
	for _, tc := range []struct {
		path      string
		fragments []string
	}{{"timeseries?hours=1&refresh=true", []string{`"from":"2026-09-01T00:02:03.123Z"`, `"oldest_sample_at":null`, `"download_egress_kbps":20`}}, {"playback-activity?hours=1&refresh=true", []string{`"hours":1`, `"buckets":[]`, `"profiles_active_24h":0`}}, {"top-activity?days=1&limit=2&refresh=true", []string{`"titles":[]`, `"user_id":"7"`, `"profile_id":"household-profile"`}}, {"downloads?limit=2&refresh=true", []string{`"active_downloads":3`, `"user_id":"7"`}}} {
		r := do(t, h, "GET", path+tc.path, "", actingRequestAdmin)
		if r.Code != 200 || !s.refresh {
			t.Fatal(tc.path, r.Code, r.Body.String(), s)
		}
		for _, want := range tc.fragments {
			if !strings.Contains(r.Body.String(), want) {
				t.Fatal(tc.path, r.Body.String(), want)
			}
		}
	}
	if s.window != 1 || s.limit != 2 || s.series.From != at || s.series.Points[0].T != at {
		t.Fatal("provider input or cached sample mutated", s)
	}
	calls := s.calls
	for _, suffix := range []string{"timeseries?hours=745", "playback-activity?hours=0", "top-activity?limit=26", "downloads?limit=0"} {
		r := do(t, h, "GET", path+suffix, "", actingRequestAdmin)
		if r.Code != 422 || s.calls != calls {
			t.Fatal(suffix, r.Code, s.calls)
		}
	}
	s.err = handlers.ErrAdminDashboardUnavailable
	for _, suffix := range []string{"timeseries", "playback-activity", "top-activity", "downloads"} {
		r := do(t, h, "GET", path+suffix, "", actingRequestAdmin)
		if r.Code != 503 {
			t.Fatal(suffix, r.Code, r.Body.String())
		}
	}
}
