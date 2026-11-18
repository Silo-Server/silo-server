package apiv2

import (
	"context"

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
