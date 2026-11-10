package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Verify the migration can satisfy both initial and continuation ordering
// without a Sort node, for an unrestricted viewer and a library-restricted
// one. Sequential scans and sorts are disabled only in this transaction
// because the isolated test table and shared database statistics may not
// favor an index naturally; a sort still appears when no index can supply
// the order.
func TestRatingsPageIndexDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SET LOCAL enable_seqscan = off; SET LOCAL enable_sort = off"); err != nil {
		t.Fatal(err)
	}
	for _, filter := range []AccessFilter{{}, {AllowedLibraryIDs: []int{1, 2}}} {
		for _, after := range []*RatingKey{nil, {RatedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), MediaItemID: "movie:z"}} {
			query, args := ratingsPageSQL(1, "index-test", filter, after, 50)
			var plan []byte
			if err := tx.QueryRow(ctx, "EXPLAIN (FORMAT JSON) "+query, args...).Scan(&plan); err != nil {
				t.Fatal(err)
			}
			if !json.Valid(plan) {
				t.Fatalf("invalid plan: %s", plan)
			}
			if !strings.Contains(string(plan), `"Index Name": "idx_user_ratings_page_order"`) || strings.Contains(string(plan), `"Node Type": "Sort"`) {
				t.Fatalf("page ordering not supplied by index (filter %+v, after %v): %s", filter, after, plan)
			}
		}
	}
}

// A ratings page holds only items the viewer may see: a rating of an item
// in a library outside the viewer's access, or of an item the catalog no
// longer has, is left out, and the keyset continues past it.
func TestRatingsPageFollowsAccessDB(t *testing.T) {
	pool := newBatchEquivTestPool(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	prefix := fmt.Sprintf("ratings-access-%d", suffix)

	var userID, movies, series int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`, prefix).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	for _, lib := range []struct {
		kind string
		id   *int
	}{{"movies", &movies}, {"series", &series}} {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ($1, $2, true) RETURNING id`, lib.kind, prefix+"-"+lib.kind).Scan(lib.id); err != nil {
			t.Fatalf("seed %s folder: %v", lib.kind, err)
		}
	}
	m1, s1, m2, gone := prefix+"-m1", prefix+"-s1", prefix+"-m2", prefix+"-gone"
	for _, item := range []struct {
		id, kind string
		library  int
	}{{m1, "movie", movies}, {s1, "series", series}, {m2, "movie", movies}} {
		batchEquivExec(t, pool, `INSERT INTO media_items (content_id, type, title) VALUES ($1, $2, $1)`, item.id, item.kind)
		batchEquivExec(t, pool, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, item.id, item.library)
	}
	t.Cleanup(func() {
		batchEquivExec(t, pool, `DELETE FROM users WHERE id = $1`, userID)
		batchEquivExec(t, pool, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{m1, s1, m2})
		batchEquivExec(t, pool, `DELETE FROM media_folders WHERE id = ANY($1)`, []int{movies, series})
	})
	now := time.Now().UTC().Truncate(time.Second)
	for i, id := range []string{m1, s1, gone, m2} {
		batchEquivExec(t, pool, `INSERT INTO user_ratings (user_id, profile_id, media_item_id, rating, rated_at) VALUES ($1, 'p1', $2, 3, $3)`, userID, id, now.Add(-time.Duration(i)*time.Hour))
	}

	repo := NewRatingsRepo(pool)
	ids := func(filter AccessFilter, after *RatingKey, limit int) []string {
		t.Helper()
		rows, err := repo.ListPage(ctx, userID, "p1", filter, after, limit)
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, r := range rows {
			out = append(out, r.MediaItemID)
		}
		return out
	}
	keyOf := func(id string, hoursAgo int) *RatingKey {
		return &RatingKey{RatedAt: now.Add(-time.Duration(hoursAgo) * time.Hour), MediaItemID: id}
	}

	for _, tc := range []struct {
		name   string
		filter AccessFilter
		after  *RatingKey
		limit  int
		want   []string
	}{
		{"unrestricted", AccessFilter{}, nil, 10, []string{m1, s1, m2}},
		{"movies only, first page", AccessFilter{AllowedLibraryIDs: []int{movies}}, nil, 1, []string{m1}},
		{"movies only, past the hidden series", AccessFilter{AllowedLibraryIDs: []int{movies}}, keyOf(m1, 0), 1, []string{m2}},
		{"movies only, last page", AccessFilter{AllowedLibraryIDs: []int{movies}}, keyOf(m2, 3), 1, []string{}},
		{"series library disabled", AccessFilter{DisabledLibraryIDs: []int{series}}, nil, 10, []string{m1, m2}},
		{"no libraries allowed", AccessFilter{AllowedLibraryIDs: []int{}}, nil, 10, []string{}},
	} {
		if got := ids(tc.filter, tc.after, tc.limit); strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
