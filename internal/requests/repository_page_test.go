package requests

import (
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRequestListKeysetDatabase(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	_, err = pool.Exec(t.Context(), `CREATE TEMP TABLE media_requests (
 id text PRIMARY KEY, provider text, media_type text, tmdb_id int, tvdb_id int, imdb_id text, title text, year int,
 overview text, poster_path text, backdrop_path text, status text, outcome text,
 requested_by_user_id int, requested_by_profile_id text, is_anime bool,
 last_error text, created_at timestamptz, updated_at timestamptz, approved_at timestamptz, completed_at timestamptz);
 CREATE INDEX ON media_requests (requested_by_user_id, created_at DESC, id DESC)`)
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.UTC)
	insert := func(id string, user int, at time.Time) {
		t.Helper()
		_, err := pool.Exec(t.Context(), `INSERT INTO media_requests VALUES ($1,'tmdb','movie',1,NULL,'','',NULL,'','','','pending','active',$2,'profile',false,'',$3,$3,NULL,NULL)`, id, user, at)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"a", "b", "c", "d"} {
		insert(id, 1, stamp)
	}
	insert("foreign", 2, stamp.Add(-time.Second))
	repo := NewRepository(pool, nil)
	first, err := repo.ListMine(t.Context(), 1, ListFilter{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].ID != "d" || first[1].ID != "c" {
		t.Fatalf("first page: %+v", first)
	}
	key := &RequestPageKey{CreatedAt: first[1].CreatedAt, ID: first[1].ID}
	// Removing the last witness and inserting ahead of it cannot shift continuation.
	if _, err := pool.Exec(t.Context(), `DELETE FROM media_requests WHERE id='c'`); err != nil {
		t.Fatal(err)
	}
	insert("new", 1, stamp.Add(time.Second))
	second, err := repo.ListMine(t.Context(), 1, ListFilter{Limit: 2, Before: key})
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 2 || second[0].ID != "b" || second[1].ID != "a" {
		t.Fatalf("continuation: %+v", second)
	}
	filtered, err := repo.ListMine(t.Context(), 1, ListFilter{Limit: 2, Before: key, Status: StatusCompleted})
	if err != nil || len(filtered) != 0 {
		t.Fatalf("filter: %+v, %v", filtered, err)
	}
}
