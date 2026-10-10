package downloads

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// TestAdminAccountReadsPostgres proves the administrator reads over a real
// schema: managed rows only, both filters, keyset order, catalog joins, the
// summary counts, and the monitor counts including user exclusions.
func TestAdminAccountReadsPostgres(t *testing.T) {
	f := seedManagedFixture(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	movie, series, episode := fmt.Sprintf("adm-movie-%d", suffix), fmt.Sprintf("adm-series-%d", suffix), fmt.Sprintf("adm-ep-%d", suffix)
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO media_items (content_id, type, title) VALUES ($1, 'movie', 'Admin Movie')`, []any{movie}},
		{`INSERT INTO media_items (content_id, type, title) VALUES ($1, 'series', 'Admin Series')`, []any{series}},
		{`INSERT INTO episodes (content_id, series_id, season_number, episode_number, title) VALUES ($1, $2, 2, 4, 'Admin Episode')`, []any{episode, series}},
	} {
		if _, err := f.pool.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatalf("seed catalog: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM episodes WHERE content_id = $1`, episode)
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{movie, series})
	})

	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	create := func(id, profile, device, content, ep, status string, size int64, at time.Time) {
		t.Helper()
		if err := f.repo.Create(ctx, &Download{
			ID: id, UserID: f.userID, ProfileID: profile, DeviceID: device, MediaFileID: f.fileID,
			ContentID: content, EpisodeID: ep, Kind: KindQueued, Status: status, Format: FormatOriginal,
			FileSize: size, CreatedAt: at, UpdatedAt: at,
		}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	id := func(name string) string { return fmt.Sprintf("adm-%d-%s", suffix, name) }
	create(id("movie-a"), f.profileA, f.deviceA, movie, "", StatusCompleted, 100, base)
	create(id("ep3-a"), f.profileA, f.deviceA, series, series+"-gone-3", StatusCompleted, 70, base.Add(30*time.Second))
	create(id("ep-a"), f.profileA, f.deviceA, series, episode, StatusReady, 200, base.Add(time.Minute))
	create(id("movie-b"), f.profileB, f.deviceB, movie, "", StatusDownloading, 300, base.Add(2*time.Minute))
	create(id("gone-a"), f.profileA, f.deviceA, series, series+"-gone-9", StatusFailed, 50, base.Add(3*time.Minute))
	create(id("ep-b"), f.profileB, f.deviceB, series, episode, StatusRevoked, 1000, base.Add(4*time.Minute))
	create(id("ephemeral"), "", "", movie, "", StatusCompleted, 5000, base.Add(5*time.Minute))

	svc := &Service{repo: f.repo, subRepo: NewSubscriptionRepository(f.pool)}
	ids := func(rows []AdminDownloadRow) []string {
		out := make([]string, 0, len(rows))
		for _, row := range rows {
			out = append(out, row.ID)
		}
		return out
	}
	list := func(filter AdminDownloadFilter, after *RegistryPosition, limit int, want ...string) []AdminDownloadRow {
		t.Helper()
		rows, err := svc.AdminListUserDownloadsPage(ctx, f.userID, filter, after, limit)
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprint(ids(rows)); got != fmt.Sprint(want) {
			t.Fatalf("filter %+v after %+v: got %s want %v", filter, after, got, want)
		}
		return rows
	}

	t.Run("managed rows newest first with catalog joins", func(t *testing.T) {
		rows := list(AdminDownloadFilter{}, nil, 50, id("ep-b"), id("gone-a"), id("movie-b"), id("ep-a"), id("ep3-a"), id("movie-a"))
		byID := map[string]AdminDownloadRow{}
		for _, row := range rows {
			byID[row.ID] = row
		}
		ep := byID[id("ep-a")]
		if ep.Title != "Admin Series" || ep.MediaType != "series" || ep.SeasonNumber == nil || *ep.SeasonNumber != 2 || ep.EpisodeNumber == nil || *ep.EpisodeNumber != 4 || ep.EpisodeTitle != "Admin Episode" || ep.DeviceID != f.deviceA || ep.FileSize != 200 {
			t.Fatalf("episode row: %+v", ep)
		}
		if gone := byID[id("gone-a")]; gone.Title != "Admin Series" || gone.SeasonNumber != nil || gone.EpisodeNumber != nil || gone.EpisodeTitle != "" {
			t.Fatalf("unknown episode row: %+v", gone)
		}
		if m := byID[id("movie-a")]; m.Title != "Admin Movie" || m.MediaType != "movie" || m.SeasonNumber != nil || m.Status != StatusCompleted {
			t.Fatalf("movie row: %+v", m)
		}
	})
	t.Run("filters", func(t *testing.T) {
		list(AdminDownloadFilter{ProfileID: f.profileA}, nil, 50, id("gone-a"), id("ep-a"), id("ep3-a"), id("movie-a"))
		list(AdminDownloadFilter{DeviceID: f.deviceB}, nil, 50, id("ep-b"), id("movie-b"))
		list(AdminDownloadFilter{ProfileID: f.profileB, DeviceID: f.deviceA}, nil, 50)
		if rows, err := svc.AdminListUserDownloadsPage(ctx, f.userID+1_000_000, AdminDownloadFilter{}, nil, 50); err != nil || len(rows) != 0 {
			t.Fatalf("foreign account: %v %v", ids(rows), err)
		}
	})
	t.Run("keyset", func(t *testing.T) {
		first := list(AdminDownloadFilter{}, nil, 2, id("ep-b"), id("gone-a"))
		last := first[len(first)-1]
		list(AdminDownloadFilter{}, &RegistryPosition{CreatedAt: last.CreatedAt, ID: last.ID}, 2, id("movie-b"), id("ep-a"))
		for _, limit := range []int{0, 202} {
			if _, err := svc.AdminListUserDownloadsPage(ctx, f.userID, AdminDownloadFilter{}, nil, limit); err == nil {
				t.Fatalf("limit %d accepted", limit)
			}
		}
	})

	subA, subB := id("sub-a"), id("sub-b")
	for _, s := range []struct {
		id, profile, device string
		active              bool
		at                  time.Time
	}{{subA, f.profileA, f.deviceA, true, base}, {subB, f.profileB, f.deviceB, false, base.Add(time.Minute)}} {
		if _, err := f.pool.Exec(ctx, `INSERT INTO download_subscriptions (id, user_id, profile_id, device_id, series_id, mode, active, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, 'all', $6, $7, $7)`, s.id, f.userID, s.profile, s.device, series, s.active, s.at); err != nil {
			t.Fatalf("seed monitor: %v", err)
		}
	}
	for _, ep := range []string{series + "-gone-1", series + "-gone-2"} {
		if _, err := f.pool.Exec(ctx, `INSERT INTO download_subscription_exclusions (subscription_id, episode_id) VALUES ($1, $2)`, subA, ep); err != nil {
			t.Fatalf("seed exclusion: %v", err)
		}
	}

	t.Run("summary", func(t *testing.T) {
		got, err := svc.AdminUserDownloadSummary(ctx, f.userID)
		if err != nil {
			t.Fatal(err)
		}
		want := AdminDownloadSummary{Total: 5, Completed: 2, InProgress: 2, Failed: 1, Revoked: 1, TotalBytes: 720, Devices: 2, MonitoredSeries: 1}
		if got != want {
			t.Fatalf("summary %+v, want %+v", got, want)
		}
		empty, err := svc.AdminUserDownloadSummary(ctx, f.userID+1_000_000)
		if err != nil || empty != (AdminDownloadSummary{}) {
			t.Fatalf("empty summary %+v %v", empty, err)
		}
	})
	t.Run("subscriptions", func(t *testing.T) {
		rows, err := svc.AdminListUserSubscriptionsPage(ctx, f.userID, AdminDownloadFilter{}, nil, 50)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 2 || rows[0].ID != subB || rows[1].ID != subA {
			t.Fatalf("order: %+v", rows)
		}
		b, a := rows[0], rows[1]
		if a.SeriesTitle != "Admin Series" || a.OnDevice != 1 || a.InProgress != 1 || a.RemovedEpisodes != 2 || !a.Active || a.Mode != SubModeAll || len(a.SeasonNumbers) != 0 {
			t.Fatalf("device A monitor: %+v", a)
		}
		// The revoked row on device B counts neither on the device nor in progress.
		if b.OnDevice != 0 || b.InProgress != 0 || b.RemovedEpisodes != 0 || b.Active {
			t.Fatalf("device B monitor: %+v", b)
		}
		filtered, err := svc.AdminListUserSubscriptionsPage(ctx, f.userID, AdminDownloadFilter{DeviceID: f.deviceA}, nil, 50)
		if err != nil || len(filtered) != 1 || filtered[0].ID != subA {
			t.Fatalf("device filter: %+v %v", filtered, err)
		}
		next, err := svc.AdminListUserSubscriptionsPage(ctx, f.userID, AdminDownloadFilter{}, &RegistryPosition{CreatedAt: b.CreatedAt, ID: b.ID}, 1)
		if err != nil || len(next) != 1 || next[0].ID != subA {
			t.Fatalf("keyset: %+v %v", next, err)
		}
		if _, err := (&Service{repo: f.repo}).AdminListUserSubscriptionsPage(ctx, f.userID, AdminDownloadFilter{}, nil, 50); !errors.Is(err, ErrSubscriptionsUnavailable) {
			t.Fatalf("without monitoring: %v", err)
		}
	})
}
