package handlers

import (
	"context"
	"errors"
)

var ErrAdminDashboardUnavailable = errors.New("administrator dashboard data is not configured")

func (h *AdminHandler) ReadAdminTimeseries(ctx context.Context, hours int, refresh bool) (*AdminTimeseries, error) {
	if h.TimeseriesSource != nil {
		if refresh {
			h.TimeseriesSource.Invalidate()
		}
		return h.TimeseriesSource.Get(ctx, hours)
	}
	if h.pool == nil {
		return nil, ErrAdminDashboardUnavailable
	}
	return queryAdminTimeseries(ctx, h.pool, hours)
}
func (h *AdminHandler) ReadAdminPlaybackActivity(ctx context.Context, hours int, refresh bool) (*AdminPlaybackActivity, error) {
	if h.PlaybackActivitySource != nil {
		if refresh {
			h.PlaybackActivitySource.Invalidate()
		}
		return h.PlaybackActivitySource.Get(ctx, hours)
	}
	if h.pool == nil {
		return nil, ErrAdminDashboardUnavailable
	}
	return queryAdminPlaybackActivity(ctx, h.pool, hours)
}
func (h *AdminHandler) ReadAdminTopActivity(ctx context.Context, days, limit int, refresh bool) (*AdminTopActivity, error) {
	if h.TopActivitySource != nil {
		if refresh {
			h.TopActivitySource.Invalidate()
		}
		return h.TopActivitySource.Get(ctx, days, limit)
	}
	if h.pool == nil {
		return nil, ErrAdminDashboardUnavailable
	}
	return queryAdminTopActivity(ctx, h.pool, days, limit)
}
func (h *AdminHandler) ReadAdminDownloadsStats(ctx context.Context, limit int, refresh bool) (*AdminDownloadsStats, error) {
	if h.DownloadsStatsSource != nil {
		if refresh {
			h.DownloadsStatsSource.Invalidate()
		}
		return h.DownloadsStatsSource.Get(ctx, limit)
	}
	if h.pool == nil {
		return nil, ErrAdminDashboardUnavailable
	}
	return queryAdminDownloadsStats(ctx, h.pool, limit)
}
