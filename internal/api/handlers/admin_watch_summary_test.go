package handlers

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// playbackHistorySeed is one synthetic account with a media file its
// finalized attempts can point at.
type playbackHistorySeed struct {
	pool          *pgxpool.Pool
	account, file int
	suffix        string
}

func seedPlaybackHistoryAccount(t *testing.T) *playbackHistorySeed {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	s := &playbackHistorySeed{pool: pool, suffix: uuid.NewString()}
	ctx := t.Context()
	cleanup := func(query string, arg any) {
		t.Cleanup(func() { _, _ = pool.Exec(context.Background(), query, arg) })
	}
	var folder int
	if err = pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('movies',$1,true) RETURNING id`, s.suffix).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	cleanup(`DELETE FROM media_folders WHERE id=$1`, folder)
	if err = pool.QueryRow(ctx, `INSERT INTO media_files(media_folder_id,file_path) VALUES($1,$2) RETURNING id`, folder, "/fixture/"+s.suffix).Scan(&s.file); err != nil {
		t.Fatal(err)
	}
	cleanup(`DELETE FROM media_files WHERE id=$1`, s.file)
	s.account = s.user(t, "a")
	return s
}

// user creates another synthetic account removed at cleanup.
func (s *playbackHistorySeed) user(t *testing.T, name string) int {
	t.Helper()
	var id int
	if err := s.pool.QueryRow(t.Context(), `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, "watch-summary-"+name+"-"+s.suffix).Scan(&id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM admin_playback_history WHERE user_id=$1`, id)
		_, _ = s.pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, id)
	})
	return id
}

// insert records one finalized attempt and returns its session id.
func (s *playbackHistorySeed) insert(t *testing.T, name string, user int, profile, item string, ended time.Time, watched float64, completed bool) string {
	t.Helper()
	id := "ws-" + s.suffix + "-" + name
	if _, err := s.pool.Exec(t.Context(), `INSERT INTO admin_playback_history(session_id,user_id,profile_id,profile_name,media_item_id,media_file_id,play_method,started_at,ended_at,watched_seconds,duration_seconds,completed)
		VALUES($1,$2,$3,'',$4,$5,'direct_play',$6,$7,$8,NULL,$9)`,
		id, user, profile, item, s.file, ended.Add(-time.Minute), ended, watched, completed); err != nil {
		t.Fatal(err)
	}
	return id
}

// TestAdminWatchSummaryDB proves the totals over synthetic attempts: the
// window start is inclusive, the profile filter narrows, other accounts never
// count, and an empty window has no last play.
func TestAdminWatchSummaryDB(t *testing.T) {
	s := seedPlaybackHistoryAccount(t)
	ctx := t.Context()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	since := now.AddDate(0, 0, -7)
	s.insert(t, "p1-completed", s.account, "p-1", "", now.Add(-time.Hour), 100, true)
	s.insert(t, "p2-partial", s.account, "p-2", "", now.Add(-2*time.Hour), 50.5, false)
	s.insert(t, "edge", s.account, "p-1", "", since, 10, false)
	s.insert(t, "before-window", s.account, "p-1", "", since.Add(-time.Millisecond), 1000, true)
	other := s.user(t, "b")
	s.insert(t, "other-account", other, "p-1", "", now.Add(-time.Hour), 1000, true)
	h := &AdminHandler{pool: s.pool}

	got, err := h.ReadAdminWatchSummary(ctx, AdminWatchSummaryQuery{UserID: s.account, Days: 7, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Since.Equal(since) || got.Plays != 3 || got.CompletedPlays != 1 || got.WatchedSeconds != 160.5 || got.LastPlayedAt == nil || !got.LastPlayedAt.Equal(now.Add(-time.Hour)) {
		t.Fatalf("account totals: %+v", got)
	}
	got, err = h.ReadAdminWatchSummary(ctx, AdminWatchSummaryQuery{UserID: s.account, Days: 7, ProfileID: "p-1", Now: now})
	if err != nil || got.Plays != 2 || got.CompletedPlays != 1 || got.WatchedSeconds != 110 {
		t.Fatalf("profile totals: %+v %v", got, err)
	}
	got, err = h.ReadAdminWatchSummary(ctx, AdminWatchSummaryQuery{UserID: s.account, Days: 1, Now: now.AddDate(1, 0, 0)})
	if err != nil || got.Plays != 0 || got.WatchedSeconds != 0 || got.LastPlayedAt != nil {
		t.Fatalf("empty window: %+v %v", got, err)
	}
	for _, q := range []AdminWatchSummaryQuery{{UserID: s.account, Days: 0, Now: now}, {UserID: s.account, Days: 366, Now: now}, {UserID: 0, Days: 7, Now: now}, {UserID: s.account, Days: 7}} {
		if _, err := h.ReadAdminWatchSummary(ctx, q); err == nil {
			t.Fatalf("invalid query accepted: %+v", q)
		}
	}
	if _, err := (&AdminHandler{}).ReadAdminWatchSummary(ctx, AdminWatchSummaryQuery{UserID: 1, Days: 1, Now: now}); err == nil {
		t.Fatal("nil pool accepted")
	}
}
