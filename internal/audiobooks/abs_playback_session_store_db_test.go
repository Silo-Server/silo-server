package audiobooks

import (
	"context"
	"os"
	"strconv"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
)

// TestABSPlaybackSessionKeepsItsFileDB pins that a stored ABS session reads
// back with its media file, which a close on a server without the native
// copy needs to record the play.
func TestABSPlaybackSessionKeepsItsFileDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	unique := "abs-file-" + uuid.NewString()
	var folder, account, file int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name) VALUES('audiobooks',$1) RETURNING id`, unique).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, unique).Scan(&account); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM abs_playback_sessions WHERE user_id=$1`, account)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, account)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=$1`, folder)
	})
	if err := pool.QueryRow(ctx, `INSERT INTO media_files(media_folder_id,file_path) VALUES($1,$2) RETURNING id`, folder, "/"+unique+".m4b").Scan(&file); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_items(content_id,type,title) VALUES($1,'audiobook','Book')`, unique); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id=$1`, unique) })
	store := &ABSPlaybackSessionStore{Pool: pool}
	id := uuid.NewString()
	if err := store.InsertPlaybackSession(ctx, abs.ABSPlaybackSession{ID: id, UserID: strconv.Itoa(account), ProfileID: "p-1", ContentID: unique, MediaFileID: &file}); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetPlaybackSession(ctx, id)
	if err != nil || got.MediaFileID == nil || *got.MediaFileID != file {
		t.Fatalf("session = %+v, %v; want media file %d", got, err, file)
	}
}

// TestReportPlayKeepsOnePlayPerClientSessionDB pins how reports of plays a
// client only reports through /session/local land: concurrent first reports
// start one play, later reports update it and credit listening from the
// total it began at, the account cap counts every open reported play, and a
// report after the play closed starts a new play with its own id.
func TestReportPlayKeepsOnePlayPerClientSessionDB(t *testing.T) {
	ctx, pool, account, unique := reportedPlayTestDB(t)
	store := &ABSPlaybackSessionStore{Pool: pool}
	report := func(clientSession string, position, listening float64) (string, bool) {
		t.Helper()
		p := abs.ReportedPlay{
			UserID: strconv.Itoa(account), ProfileID: "p-1", ClientSessionID: clientSession, ContentID: unique,
			PositionSeconds: position, ListeningSeconds: listening,
		}
		st, found, err := store.UpdateReportedPlay(ctx, p)
		if err == nil && !found {
			st, err = store.StartReportedPlay(ctx, p, uuid.NewString(), 2)
		}
		if err != nil {
			t.Fatal(err)
		}
		return st.ID, st.Announced
	}
	// Concurrent first reports, as two API servers would take them.
	first := concurrentFirstReports(t, func() string { id, _ := report("local-1", 100, 300); return id })
	// The row began at a client total of 300; 10 more seconds were heard.
	// Its age caps the credit, so backdate it as if it began a minute ago.
	if _, err := pool.Exec(ctx, `UPDATE abs_playback_sessions SET started_at = now() - interval '1 minute' WHERE id = $1`, first); err != nil {
		t.Fatal(err)
	}
	if id, announced := report("local-1", 110, 310); id != first || announced {
		t.Fatalf("report = (%q, %v), want the open play %s, not yet announced", id, announced, first)
	}
	// An older report arriving late keeps the newer position.
	if id, _ := report("local-1", 105, 305); id != first {
		t.Fatalf("late report landed on %q, want %s", id, first)
	}
	got, err := store.GetPlaybackSession(ctx, first)
	if err != nil || !got.ReportOnly || got.CurrentPositionSeconds != 110 || got.TimeListeningSeconds != 10 {
		t.Fatalf("play = %+v, %v; want report-only at 110 s with 10 s listened", got, err)
	}
	// Totals that round to the same credited second still order the reports.
	report("local-1", 112, 310.9)
	report("local-1", 108, 310.5)
	if got, err := store.GetPlaybackSession(ctx, first); err != nil || got.CurrentPositionSeconds != 112 {
		t.Fatalf("play = %+v, %v; want the newer position 112 s", got, err)
	}

	// The cap counts every open reported play of the account.
	if id, _ := report("local-2", 0, 0); id == "" {
		t.Fatal("second play refused under a cap of two")
	}
	if id, _ := report("local-3", 0, 0); id != "" {
		t.Fatalf("third play %q admitted over a cap of two", id)
	}
	// The first play's start is recorded; the second's never was, and once
	// it falls silent it gives its slot up to the next new play.
	if _, err := pool.Exec(ctx, `INSERT INTO playback_session_lifecycle
		(session_id, frontend, state, seq, user_id, profile_id, media_file_id, requested_media_file_id, started_at)
		VALUES ($1, 'abs', 'playing', nextval('playback_session_lifecycle_seq'), $2, 'p-1', 0, 0, NOW())`, first, account); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM playback_session_lifecycle WHERE user_id=$1`, account)
	})
	if id, announced := report("local-1", 110, 310); id != first || !announced {
		t.Fatalf("report = (%q, %v), want the announced play %s", id, announced, first)
	}
	if _, err := pool.Exec(ctx, `UPDATE abs_playback_sessions SET last_sync_at = now() - interval '2 minutes'
		WHERE user_id = $1 AND client_session_id = 'local-2'`, account); err != nil {
		t.Fatal(err)
	}
	if id, _ := report("local-3", 0, 0); id == "" {
		t.Fatal("a silent play whose start was never recorded still holds a slot")
	}
	// A nonsensical listening total does not fail the report.
	if id, _ := report("local-3", 5, -1e15); id == "" {
		t.Fatal("report with a negative listening total failed")
	}

	// After the play ends, the client session's next report is a new play.
	if _, err := pool.Exec(ctx, `UPDATE abs_playback_sessions SET closed_at = now() WHERE id = $1`, first); err != nil {
		t.Fatal(err)
	}
	if id, announced := report("local-1", 500, 900); id == "" || id == first || announced {
		t.Fatalf("report after the end = (%q, %v), want a new play", id, announced)
	}
}

// concurrentFirstReports runs four reports at once and returns the one play
// they all landed on, failing when they started more than one.
func concurrentFirstReports(t *testing.T, report func() string) string {
	t.Helper()
	ids := make(chan string, 4)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids <- report()
		}()
	}
	wg.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if id == "" || id != first {
			t.Errorf("concurrent first reports started more than one play (%q, %q)", first, id)
		}
	}
	return first
}

// reportedPlayTestDB is an account and an audiobook to report plays of.
func reportedPlayTestDB(t *testing.T) (context.Context, *pgxpool.Pool, int, string) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	unique := "abs-report-" + uuid.NewString()
	var account int
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, unique).Scan(&account); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_items(content_id,type,title) VALUES($1,'audiobook','Book')`, unique); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM abs_playback_sessions WHERE user_id=$1`, account)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id=$1`, unique)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, account)
	})
	return ctx, pool, account, unique
}
