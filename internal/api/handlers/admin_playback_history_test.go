package handlers

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestAdminPlaybackHistoryPageDB proves the keyset page over synthetic
// finalized attempts: newest ended first with session id tiebreak, every v1
// filter, an exact page boundary, and the projection's joins and fallbacks.
func TestAdminPlaybackHistoryPageDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx := t.Context()
	suffix := uuid.NewString()
	var account int
	if err = pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, "playback-history-"+suffix).Scan(&account); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, account) }()
	var folder int
	if err = pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('movies',$1,true) RETURNING id`, suffix).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=$1`, folder) }()
	movie := "movie-" + suffix
	if _, err = pool.Exec(ctx, `INSERT INTO media_items(content_id,type,title) VALUES($1,'movie','Synthetic Movie')`, movie); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id=$1`, movie) }()
	var file int
	if err = pool.QueryRow(ctx, `INSERT INTO media_files(media_folder_id,file_path) VALUES($1,$2) RETURNING id`, folder, "/fixture/"+suffix).Scan(&file); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_files WHERE id=$1`, file) }()
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM admin_playback_history WHERE session_id LIKE $1`, "ph-"+suffix+"-%")
	}()
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	insert := func(name string, user int, profile, item string, ended time.Time, completed bool, duration *float64) string {
		t.Helper()
		id := "ph-" + suffix + "-" + name
		if _, err := pool.Exec(ctx, `INSERT INTO admin_playback_history(session_id,user_id,profile_id,profile_name,media_item_id,media_file_id,play_method,started_at,ended_at,watched_seconds,duration_seconds,completed,client_ip)
			VALUES($1,$2,$3,$4,$5,$6,'direct_play',$7,$8,42.5,$9,$10,'203.0.113.9'::inet)`,
			id, user, profile, "", item, file, ended.Add(-time.Minute), ended, duration, completed); err != nil {
			t.Fatal(err)
		}
		return id
	}
	duration := 5400.0
	// Two rows share an ended_at so the tiebreak is exercised; "b" sorts
	// after "a" and therefore lists first under DESC.
	a := insert("a", account, "p-1", movie, base, true, &duration)
	b := insert("b", account, "p-1", movie, base, false, nil)
	older := insert("older", account, "p-2", "", base.Add(-time.Hour), true, nil)
	// An attempt whose item has since left the catalog: it must still list,
	// with the title and type falling back to empty rather than the row vanishing.
	other := insert("other", account, "p-9", "movie-gone-"+suffix, base.Add(time.Hour), false, nil)
	h := &AdminHandler{pool: pool}
	only := func(t *testing.T, filter AdminPlaybackHistoryFilter, limit int, after *AdminPlaybackHistoryPageKey) AdminPlaybackHistoryPage {
		t.Helper()
		page, err := h.ListAdminPlaybackHistoryPage(ctx, filter, after, limit)
		if err != nil {
			t.Fatal(err)
		}
		// Rows from other tests share the table; keep only this run's.
		kept := page.Items[:0]
		for _, row := range page.Items {
			if len(row.SessionID) > len("ph-"+suffix) && row.SessionID[:len("ph-"+suffix)] == "ph-"+suffix {
				kept = append(kept, row)
			}
		}
		page.Items = kept
		return page
	}
	ids := func(page AdminPlaybackHistoryPage) []string {
		out := make([]string, 0, len(page.Items))
		for _, row := range page.Items {
			out = append(out, row.SessionID)
		}
		return out
	}
	equal := func(t *testing.T, got, want []string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("got %v want %v", got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("got %v want %v", got, want)
			}
		}
	}
	t.Run("order and projection", func(t *testing.T) {
		page := only(t, AdminPlaybackHistoryFilter{UserID: account}, 200, nil)
		equal(t, ids(page), []string{other, b, a, older})
		if r := page.Items[0]; r.MediaTitle != "" || r.MediaType != "" || r.ProfileName != "p-9" || r.MediaItemID != "movie-gone-"+suffix {
			t.Fatalf("missing catalog item projection: %+v", r)
		}
		if r := page.Items[2]; r.Username != "playback-history-"+suffix || r.MediaTitle != "Synthetic Movie" || r.MediaType != "movie" || r.MediaFileID != file || r.DurationSeconds == nil || *r.DurationSeconds != duration || !r.Completed || r.WatchedSeconds != 42.5 {
			t.Fatalf("projection: %+v", r)
		}
		if page.Items[1].DurationSeconds != nil {
			t.Fatal("null duration became a value")
		}
	})
	t.Run("filters", func(t *testing.T) {
		equal(t, ids(only(t, AdminPlaybackHistoryFilter{MediaItemID: movie}, 200, nil)), []string{b, a})
		equal(t, ids(only(t, AdminPlaybackHistoryFilter{UserID: account, ProfileID: "p-2"}, 200, nil)), []string{older})
		yes, no := true, false
		equal(t, ids(only(t, AdminPlaybackHistoryFilter{UserID: account, Completed: &yes}, 200, nil)), []string{a, older})
		equal(t, ids(only(t, AdminPlaybackHistoryFilter{UserID: account, Completed: &no}, 200, nil)), []string{other, b})
		if unrelated := only(t, AdminPlaybackHistoryFilter{UserID: account + 1_000_000}, 200, nil); len(unrelated.Items) != 0 {
			t.Fatalf("foreign account filter leaked rows: %v", ids(unrelated))
		}
	})
	t.Run("keyset page boundary", func(t *testing.T) {
		filter := AdminPlaybackHistoryFilter{UserID: account}
		first, err := h.ListAdminPlaybackHistoryPage(ctx, filter, nil, 2)
		if err != nil || !first.HasMore || len(first.Items) != 2 {
			t.Fatalf("first page: %+v %v", first, err)
		}
		equal(t, ids(first), []string{other, b})
		// The boundary falls between the two rows sharing ended_at, so the
		// tiebreaker alone decides where the next page resumes.
		last := first.Items[1]
		second, err := h.ListAdminPlaybackHistoryPage(ctx, filter, &AdminPlaybackHistoryPageKey{EndedAt: last.EndedAt, SessionID: last.SessionID}, 2)
		if err != nil || second.HasMore || len(second.Items) != 2 {
			t.Fatalf("second page: %+v %v", second, err)
		}
		equal(t, ids(second), []string{a, older})
		exact, err := h.ListAdminPlaybackHistoryPage(ctx, filter, nil, 4)
		if err != nil || exact.HasMore || len(exact.Items) != 4 {
			t.Fatalf("exact page must not claim more: %+v %v", exact, err)
		}
	})
	t.Run("limits", func(t *testing.T) {
		for _, limit := range []int{0, 201} {
			if _, err := h.ListAdminPlaybackHistoryPage(ctx, AdminPlaybackHistoryFilter{}, nil, limit); err == nil {
				t.Fatalf("limit %d accepted", limit)
			}
		}
		if _, err := (&AdminHandler{}).ListAdminPlaybackHistoryPage(ctx, AdminPlaybackHistoryFilter{}, nil, 1); err == nil {
			t.Fatal("nil pool accepted")
		}
	})
}
