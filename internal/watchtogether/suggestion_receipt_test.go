package watchtogether

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func suggestionReceiptPG(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := selectionPG(t)
	_, err := pool.Exec(t.Context(), `CREATE TABLE users(id integer PRIMARY KEY);INSERT INTO users VALUES(1),(2);
 CREATE TABLE watch_together_suggestions(id text PRIMARY KEY,room_id text,suggester_user_id integer,suggester_profile_id text,content_id text,content_type text,title text,subtitle text,poster_url text,note text,vote_count integer,created_at timestamptz);
 INSERT INTO watch_together_rooms(id,phase) VALUES('room','lobby'),('other','lobby'),('ended','ended');`)
	if err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../migrations/sql/20260906171024_add_watch_together_suggestion_receipts.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(t.Context(), strings.Split(string(migration), "-- +goose Down")[0]); err != nil {
		t.Fatal(err)
	}
	return pool
}

// waitForLockWaiters returns once n of the test schema's connections are
// blocked on a lock.
func waitForLockWaiters(ctx context.Context, t *testing.T, pool *pgxpool.Pool, n int) {
	t.Helper()
	for {
		var waiting int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE application_name=current_setting('application_name') AND wait_event_type='Lock'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting >= n {
			return
		}
		runtime.Gosched()
	}
}

func TestSuggestionReceiptNoResurrectionPG(t *testing.T) {
	pool := suggestionReceiptPG(t)
	repo := NewSuggestionRepository(pool)
	original := Suggestion{ID: "request", RoomID: "room", SuggesterUserID: 1, SuggesterProfileID: "profile", ContentID: "movie", ContentType: "movie", Title: "Title", Note: "Note", CreatedAt: time.Now().UTC()}
	var wg sync.WaitGroup
	type receipt struct {
		created bool
		err     error
	}
	results := make(chan receipt, 2)
	for range 2 {
		wg.Go(func() {
			_, created, err := repo.CreateSuggestionOnce(t.Context(), original)
			results <- receipt{created, err}
		})
	}
	wg.Wait()
	close(results)
	created := 0
	for r := range results {
		if r.err != nil {
			t.Fatal(r.err)
		}
		if r.created {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("created %d times", created)
	}
	// Server-generated creation time and current tally are not client intent.
	retry := original
	retry.CreatedAt = retry.CreatedAt.Add(time.Hour)
	retry.VoteCount = 99
	if _, made, err := repo.CreateSuggestionOnce(t.Context(), retry); err != nil || made {
		t.Fatalf("retry: %v %v", made, err)
	}
	for _, alter := range []func(*Suggestion){func(s *Suggestion) { s.Title = "different" }, func(s *Suggestion) { s.RoomID = "other" }, func(s *Suggestion) { s.SuggesterUserID = 2 }, func(s *Suggestion) { s.SuggesterProfileID = "other" }} {
		changed := original
		alter(&changed)
		if _, _, err := repo.CreateSuggestionOnce(t.Context(), changed); !errors.Is(err, ErrSuggestionIdentityConflict) {
			t.Fatalf("changed request: %v", err)
		}
	}
	if _, err := pool.Exec(t.Context(), `DELETE FROM watch_together_suggestions WHERE id='request'`); err != nil {
		t.Fatal(err)
	}
	_, _, err := repo.CreateSuggestionOnce(t.Context(), original)
	if !errors.Is(err, ErrSuggestionIdentityConflict) {
		t.Fatalf("resurrection: %v", err)
	}
	var suggestions, receipts int
	if err = pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM watch_together_suggestions),(SELECT count(*) FROM watch_together_suggestion_receipts)`).Scan(&suggestions, &receipts); err != nil || suggestions != 0 || receipts != 1 {
		t.Fatalf("rows %d %d %v", suggestions, receipts, err)
	}
	ended := original
	ended.ID = "ended-request"
	ended.RoomID = "ended"
	if _, _, err = repo.CreateSuggestionOnce(t.Context(), ended); !errors.Is(err, ErrRoomClosed) {
		t.Fatalf("ended %v", err)
	}
	if _, err = pool.Exec(t.Context(), `DELETE FROM users WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM watch_together_suggestion_receipts`).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("account cleanup: %d %v", receipts, err)
	}
	// Observe the writer blocked behind closure, then commit closure before admission.
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	if _, err = tx.Exec(t.Context(), `UPDATE watch_together_rooms SET phase='ended' WHERE id='other'`); err != nil {
		t.Fatal(err)
	}
	blockedInput := original
	blockedInput.ID = "blocked"
	blockedInput.RoomID = "other"
	blockedInput.SuggesterUserID = 2
	done := make(chan error, 1)
	go func() { _, _, err := repo.CreateSuggestionOnce(t.Context(), blockedInput); done <- err }()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	waitForLockWaiters(ctx, t, pool, 1)
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrRoomClosed) {
			t.Fatalf("closure admission: %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err = pool.QueryRow(t.Context(), `SELECT count(*) FROM watch_together_suggestion_receipts`).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("closed receipt: %d %v", receipts, err)
	}

}

// Another API server's create holds the room lock while it adds the last
// allowed row; both create paths must wait for it and then refuse.
func TestSuggestionRoomLimitPG(t *testing.T) {
	pool := suggestionReceiptPG(t)
	repo := NewSuggestionRepository(pool)
	base := Suggestion{RoomID: "room", SuggesterUserID: 2, SuggesterProfileID: "guest", ContentID: "movie", ContentType: "movie", Title: "Title", CreatedAt: time.Now().UTC()}
	kept := base
	kept.ID = "kept"
	if _, created, err := repo.CreateSuggestionOnce(t.Context(), kept); err != nil || !created {
		t.Fatalf("kept: %v %v", created, err)
	}
	const seed = `INSERT INTO watch_together_suggestions(id,room_id,suggester_user_id,suggester_profile_id,content_id,content_type,title,subtitle,poster_url,note,vote_count,created_at)
 SELECT $2::text||n,'room',1,'host','movie','movie','Title','','','',0,now() FROM generate_series(1,$1) n`
	if _, err := pool.Exec(t.Context(), seed, maxRoomSuggestions-2, "seed-"); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	if _, err = tx.Exec(t.Context(), `SELECT id FROM watch_together_rooms WHERE id='room' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), seed, 1, "last-"); err != nil {
		t.Fatal(err)
	}
	legacy, once := base, base
	legacy.ID, once.ID = "legacy", "once"
	refused := make(chan error, 2)
	go func() { _, err := repo.CreateSuggestion(t.Context(), legacy); refused <- err }()
	go func() { _, _, err := repo.CreateSuggestionOnce(t.Context(), once); refused <- err }()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	waitForLockWaiters(ctx, t, pool, 2)
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		select {
		case err := <-refused:
			if !errors.Is(err, ErrSuggestionLimitReached) {
				t.Fatalf("create in a full room: %v", err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	var suggestions, receipts int
	if err = pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM watch_together_suggestions WHERE room_id='room'),(SELECT count(*) FROM watch_together_suggestion_receipts)`).Scan(&suggestions, &receipts); err != nil || suggestions != maxRoomSuggestions || receipts != 1 {
		t.Fatalf("rows %d receipts %d %v", suggestions, receipts, err)
	}
	if _, created, err := repo.CreateSuggestionOnce(t.Context(), kept); err != nil || created {
		t.Fatalf("replay in a full room: %v %v", created, err)
	}
	if _, err = pool.Exec(t.Context(), `DELETE FROM watch_together_suggestions WHERE id='last-1'`); err != nil {
		t.Fatal(err)
	}
	if _, created, err := repo.CreateSuggestionOnce(t.Context(), once); err != nil || !created {
		t.Fatalf("refused identity after a removal: %v %v", created, err)
	}
}
