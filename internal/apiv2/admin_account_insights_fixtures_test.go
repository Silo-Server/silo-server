package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/downloads"
	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
)

// fakeAdminAccountDevices returns one registered device and one known only
// from saved settings.
type fakeAdminAccountDevices struct {
	calls int
	user  int
	rows  []handlers.AdminUserDeviceView
	err   error
}

func adminUserDeviceView(summary string, lastSeen string, profiles map[string]string) handlers.AdminUserDeviceView {
	var view handlers.AdminUserDeviceView
	if err := json.Unmarshal([]byte(summary), &view.AdminDeviceSummaryView); err != nil {
		panic(err)
	}
	view.LastSeenAt = lastSeen
	view.ProfileLastSeenAt = profiles
	return view
}

func fixtureAdminUserDevices() []handlers.AdminUserDeviceView {
	return []handlers.AdminUserDeviceView{
		adminUserDeviceView(`{"user_id":7,"device_id":"device-settings-only","device_name":"Living Room","device_platform":"android","override_count":1,"profile_count":1,"profiles":[{"profile_id":"p-owner","profile_name":"Owner","override_count":1,"last_updated":"2026-01-01T09:00:00Z"}],"last_updated":"2026-01-01T09:00:00Z"}`, "", map[string]string{}),
		adminUserDeviceView(`{"user_id":7,"device_id":"device-tv","device_name":"Apple TV","device_platform":"tvOS","override_count":2,"profile_count":2,"profiles":[{"profile_id":"p-owner","profile_name":"Owner","override_count":2,"last_updated":"2026-01-02T03:04:05Z"},{"profile_id":"p-gone","profile_name":"","override_count":0,"last_updated":"2025-12-30T10:00:00Z"}],"last_updated":"2026-01-02T03:04:05Z"}`, "2026-01-02T03:04:05.678Z", map[string]string{"p-owner": "2026-01-02T03:04:05.678Z"}),
	}
}

func (f *fakeAdminAccountDevices) ReadAdminUserDevices(_ context.Context, user int) ([]handlers.AdminUserDeviceView, error) {
	f.calls++
	f.user = user
	if f.err != nil {
		return nil, f.err
	}
	if f.rows != nil {
		return f.rows, nil
	}
	return fixtureAdminUserDevices(), nil
}

// fakeAdminWatchSummary echoes a fixed window so fixtures stay stable; the
// real service derives Since from the query's Now.
type fakeAdminWatchSummary struct {
	calls int
	query handlers.AdminWatchSummaryQuery
	err   error
}

func (f *fakeAdminWatchSummary) ReadAdminWatchSummary(_ context.Context, q handlers.AdminWatchSummaryQuery) (handlers.AdminWatchSummaryView, error) {
	f.calls++
	f.query = q
	if f.err != nil {
		return handlers.AdminWatchSummaryView{}, f.err
	}
	last := fixedTime()
	return handlers.AdminWatchSummaryView{Since: fixedTime().AddDate(0, 0, -q.Days), Plays: 41, CompletedPlays: 29, WatchedSeconds: 66000, LastPlayedAt: &last}, nil
}

// fakeAdminRequestUsage returns a fixed policy.
type fakeAdminRequestUsage struct {
	calls  int
	user   int
	policy mediarequests.EffectivePolicy
	err    error
}

func fixtureRequestUsagePolicy() mediarequests.EffectivePolicy {
	return mediarequests.EffectivePolicy{RequestsEnabled: true, MaxRequests: 20, WindowDays: 30, Used: 12, Remaining: 8, WindowStart: fixedTime().AddDate(0, 0, -30)}
}

func (f *fakeAdminRequestUsage) EffectivePolicy(_ context.Context, user int) (mediarequests.EffectivePolicy, error) {
	f.calls++
	f.user = user
	return f.policy, f.err
}

// fakeAdminAccountDownloads serves synthetic managed rows and monitors.
type fakeAdminAccountDownloads struct {
	calls    int
	user     int
	filter   downloads.AdminDownloadFilter
	after    *downloads.RegistryPosition
	limit    int
	rows     []downloads.AdminDownloadRow
	subs     []downloads.AdminSubscriptionRow
	summary  downloads.AdminDownloadSummary
	err      error
	subsErr  error
	lastCall string
}

func fixtureAdminDownloadRows() []downloads.AdminDownloadRow {
	completed := fixedTime().Add(-time.Hour)
	season, episode := 2, 4
	return []downloads.AdminDownloadRow{
		{Download: downloads.Download{ID: "dl-2", UserID: 7, ProfileID: "p-owner", DeviceID: "device-tv", MediaFileID: 42, ContentID: "series-1", EpisodeID: "episode-2-4", BatchID: "batch-1", Status: downloads.StatusCompleted, Format: downloads.FormatOriginal, Quality: downloads.QualityOriginal, EffectiveQuality: downloads.QualityOriginal, FileSize: 1_500_000_000, CreatedAt: fixedTime(), UpdatedAt: fixedTime(), CompletedAt: &completed, StatusEventAt: &completed},
			Title: "Synthetic Series", MediaType: "series", SeasonNumber: &season, EpisodeNumber: &episode, EpisodeTitle: "Synthetic Episode"},
		{Download: downloads.Download{ID: "dl-1", UserID: 7, ProfileID: "p-owner", DeviceID: "device-tv", MediaFileID: 41, ContentID: "movie-1", Status: downloads.StatusReady, Format: downloads.FormatOriginal, Quality: downloads.QualityOriginal, EffectiveQuality: downloads.QualityOriginal, FileSize: 900_000_000, CreatedAt: fixedTime().Add(-time.Minute), UpdatedAt: fixedTime().Add(-time.Minute)},
			Title: "Synthetic Movie", MediaType: "movie"},
	}
}

func fixtureAdminSubscriptionRows() []downloads.AdminSubscriptionRow {
	return []downloads.AdminSubscriptionRow{{
		Subscription: downloads.Subscription{ID: "sub-1", UserID: 7, ProfileID: "p-owner", DeviceID: "device-tv", SeriesID: "series-1", Mode: downloads.SubModeAll, DeleteWatched: true, Active: true, CreatedAt: fixedTime(), UpdatedAt: fixedTime()},
		SeriesTitle:  "Synthetic Series", OnDevice: 3, InProgress: 1, RemovedEpisodes: 2,
	}}
}

func newFakeAdminAccountDownloads() *fakeAdminAccountDownloads {
	return &fakeAdminAccountDownloads{
		rows:    fixtureAdminDownloadRows(),
		subs:    fixtureAdminSubscriptionRows(),
		summary: downloads.AdminDownloadSummary{Total: 17, Completed: 12, InProgress: 4, Failed: 1, Revoked: 2, TotalBytes: 42_000_000_000, Devices: 2, MonitoredSeries: 3},
	}
}

// fakeDownloadPage applies the keyset position and limit to rows ordered
// newest first.
func fakeDownloadPage[T any](rows []T, key func(T) downloads.RegistryPosition, after *downloads.RegistryPosition, limit int) []T {
	out := make([]T, 0, len(rows))
	for _, row := range rows {
		if after != nil && !fakeRegistryBefore(key(row), *after) {
			continue
		}
		out = append(out, row)
		if len(out) == limit {
			break
		}
	}
	return out
}

// fakeRegistryBefore reports whether k sorts after the keyset position in
// newest-first order.
func fakeRegistryBefore(k, after downloads.RegistryPosition) bool {
	if k.CreatedAt.Equal(after.CreatedAt) {
		return k.ID < after.ID
	}
	return k.CreatedAt.Before(after.CreatedAt)
}

func (f *fakeAdminAccountDownloads) AdminListUserDownloadsPage(_ context.Context, user int, filter downloads.AdminDownloadFilter, after *downloads.RegistryPosition, limit int) ([]downloads.AdminDownloadRow, error) {
	f.calls++
	f.lastCall, f.user, f.filter, f.after, f.limit = "downloads", user, filter, after, limit
	if f.err != nil {
		return nil, f.err
	}
	return fakeDownloadPage(f.rows, func(r downloads.AdminDownloadRow) downloads.RegistryPosition {
		return downloads.RegistryPosition{CreatedAt: r.CreatedAt, ID: r.ID}
	}, after, limit), nil
}

func (f *fakeAdminAccountDownloads) AdminUserDownloadSummary(_ context.Context, user int) (downloads.AdminDownloadSummary, error) {
	f.calls++
	f.lastCall, f.user = "summary", user
	return f.summary, f.err
}

func (f *fakeAdminAccountDownloads) AdminListUserSubscriptionsPage(_ context.Context, user int, filter downloads.AdminDownloadFilter, after *downloads.RegistryPosition, limit int) ([]downloads.AdminSubscriptionRow, error) {
	f.calls++
	f.lastCall, f.user, f.filter, f.after, f.limit = "subscriptions", user, filter, after, limit
	if f.subsErr != nil {
		return nil, f.subsErr
	}
	return fakeDownloadPage(f.subs, func(r downloads.AdminSubscriptionRow) downloads.RegistryPosition {
		return downloads.RegistryPosition{CreatedAt: r.CreatedAt, ID: r.ID}
	}, after, limit), nil
}

// withAdminAccountInsights wires the account-page read fakes.
func withAdminAccountInsights(deps Dependencies) Dependencies {
	deps.AdminAccountDevices = &fakeAdminAccountDevices{}
	deps.AdminWatchSummary = &fakeAdminWatchSummary{}
	deps.AdminAccountDownloads = newFakeAdminAccountDownloads()
	deps.AdminRequestUsage = &fakeAdminRequestUsage{policy: fixtureRequestUsagePolicy()}
	return deps
}

func adminAccountInsightsFixtureCases() []fixtureCase {
	cases := []fixtureCase{
		{name: "admin_account_devices", operationID: "listAdminUserDevices", path: Prefix + "/admin/users/7/devices", schema: "CollectionAdminUserDevice",
			scenario: "An account's devices, most recently seen first; a device known only from saved settings has a null last_seen_at."},
		{name: "admin_account_watch_summary", operationID: "getAdminUserWatchSummary", path: Prefix + "/admin/users/7/watch-summary?days=30", schema: "AdminUserWatchSummary",
			scenario: "Totals of an account's finalized plays over the last 30 days."},
		{name: "admin_account_downloads", operationID: opListAdminUserDownloads, path: Prefix + "/admin/users/7/downloads?limit=1", schema: "CollectionAdminUserDownload",
			scenario: "An account's managed device downloads, newest first: an episode row with its placement and a scoped cursor."},
		{name: "admin_account_download_summary", operationID: "getAdminUserDownloadSummary", path: Prefix + "/admin/users/7/downloads/summary", schema: "AdminUserDownloadSummary",
			scenario: "Totals of an account's managed device downloads and active series monitors."},
		{name: "admin_account_download_subscriptions", operationID: opListAdminUserDownloadSubscriptions, path: Prefix + "/admin/users/7/download-subscriptions", schema: "CollectionAdminUserDownloadSubscription",
			scenario: "An account's series monitors with on-device, in-progress and removed episode counts."},
		{name: "admin_request_user_usage", operationID: "getAdminRequestUserUsage", path: Prefix + "/admin/request-users/7/usage", schema: "AdminRequestUserUsage",
			scenario: "An account's effective request policy and quota use in the current window."},
	}
	for i := range cases {
		c := &cases[i]
		c.method = http.MethodGet
		c.status = http.StatusOK
		c.headers = actingRequestAdmin
		c.schema = "#/components/schemas/" + c.schema
		c.assertHeaders = []string{"Cache-Control", "Content-Type"}
	}
	return cases
}
