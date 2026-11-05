package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// A visible personal list page holds only entries the viewer may see, by
// the rule the list cards follow: a catalog item the access filter admits,
// or an episode whose series it admits. Entries for items in a library the
// viewer may not see, or that the catalog no longer has, are skipped, and
// the keyset continues past them. The page order comes from each list's
// profile index rather than a full sort, for unrestricted and restricted
// viewers alike.
func TestPersonalListPageFollowsAccessDB(t *testing.T) {
	pool := newBatchEquivTestPool(t)
	ctx := context.Background()
	prefix := fmt.Sprintf("list-page-%d", time.Now().UnixNano())

	var userID, movies, tv int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`, prefix).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	for _, lib := range []struct {
		kind string
		id   *int
	}{{"movies", &movies}, {"series", &tv}} {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ($1, $2, true) RETURNING id`, lib.kind, prefix+"-"+lib.kind).Scan(lib.id); err != nil {
			t.Fatalf("seed %s folder: %v", lib.kind, err)
		}
	}
	movie1, series, episode, movie2, gone := prefix+"-m1", prefix+"-s", prefix+"-s1e1", prefix+"-m2", prefix+"-gone"
	batchEquivExec(t, pool, `INSERT INTO media_items (content_id, type, title) VALUES ($1, 'movie', $1), ($2, 'series', $2), ($3, 'movie', $3)`, movie1, series, movie2)
	batchEquivExec(t, pool, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $4), ($2, $5), ($3, $4)`, movie1, series, movie2, movies, tv)
	batchEquivExec(t, pool, `INSERT INTO episodes (content_id, series_id, season_number, episode_number, title) VALUES ($1, $2, 1, 1, 'Pilot')`, episode, series)
	t.Cleanup(func() {
		batchEquivExec(t, pool, `DELETE FROM users WHERE id = $1`, userID)
		batchEquivExec(t, pool, `DELETE FROM episodes WHERE content_id = $1`, episode)
		batchEquivExec(t, pool, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{movie1, series, movie2})
		batchEquivExec(t, pool, `DELETE FROM media_folders WHERE id = ANY($1)`, []int{movies, tv})
	})
	// Newest first: movie 1, the series, its episode, an item the catalog no
	// longer has, movie 2.
	now := time.Now().UTC().Truncate(time.Second)
	order := []string{movie1, series, episode, gone, movie2}
	lists := []PersonalList{PersonalListFavorites, PersonalListWatchlist, PersonalListNotInterested}
	for _, list := range lists {
		for i, id := range order {
			batchEquivExec(t, pool, `INSERT INTO `+string(list)+` (user_id, profile_id, media_item_id, added_at) VALUES ($1, 'p1', $2, $3)`, userID, id, now.Add(-time.Duration(i)*time.Hour))
		}
	}

	repo := NewItemRepository(pool)
	keyOf := func(id string) *userstore.ListKey {
		for i, o := range order {
			if o == id {
				return &userstore.ListKey{AddedAt: now.Add(-time.Duration(i) * time.Hour).Format(time.RFC3339Nano), MediaItemID: id}
			}
		}
		t.Fatalf("no key for %s", id)
		return nil
	}
	for _, list := range lists {
		for _, tc := range []struct {
			name   string
			filter AccessFilter
			after  *userstore.ListKey
			limit  int
			want   []string
		}{
			{"unrestricted", AccessFilter{}, nil, 10, []string{movie1, series, episode, movie2}},
			{"TV only", AccessFilter{AllowedLibraryIDs: []int{tv}}, nil, 10, []string{series, episode}},
			{"movies only, first page", AccessFilter{AllowedLibraryIDs: []int{movies}}, nil, 1, []string{movie1}},
			{"movies only, past the hidden entries", AccessFilter{AllowedLibraryIDs: []int{movies}}, keyOf(movie1), 1, []string{movie2}},
			{"movies only, last page", AccessFilter{AllowedLibraryIDs: []int{movies}}, keyOf(movie2), 1, []string{}},
			{"TV library disabled", AccessFilter{DisabledLibraryIDs: []int{tv}}, nil, 10, []string{movie1, movie2}},
			{"no libraries allowed", AccessFilter{AllowedLibraryIDs: []int{}}, nil, 10, []string{}},
		} {
			rows, err := repo.ListVisiblePersonalPage(ctx, list, userID, "p1", tc.filter, tc.after, tc.limit)
			if err != nil {
				t.Fatalf("%s %s: %v", list, tc.name, err)
			}
			got := []string{}
			for _, r := range rows {
				got = append(got, r.MediaItemID)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("%s %s: got %v, want %v", list, tc.name, got, tc.want)
			}
		}
	}
	assertPersonalListPagePlans(t, pool, lists, userID, []AccessFilter{{}, {AllowedLibraryIDs: []int{movies}}}, keyOf(movie1))
}

// assertPersonalListPagePlans checks that each list's profile index, not a
// full sort, supplies the page order of the visible page query. Sequential
// scans are disabled only in this transaction because the isolated test
// rows may be too few to favor an index naturally.
func assertPersonalListPagePlans(t *testing.T, pool *pgxpool.Pool, lists []PersonalList, userID int, filters []AccessFilter, after *userstore.ListKey) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
		t.Fatal(err)
	}
	for _, list := range lists {
		for _, filter := range filters {
			query, args, err := personalListPageSQL(list, userID, "p1", filter, after, 50)
			if err != nil {
				t.Fatal(err)
			}
			var plan []byte
			if err := tx.QueryRow(ctx, "EXPLAIN (FORMAT JSON) "+query, args...).Scan(&plan); err != nil {
				t.Fatal(err)
			}
			if !json.Valid(plan) {
				t.Fatalf("invalid plan: %s", plan)
			}
			index := "idx_" + string(list) + "_profile"
			if !strings.Contains(string(plan), `"Index Name": "`+index+`"`) || strings.Contains(string(plan), `"Node Type": "Sort"`) {
				t.Fatalf("%s (filter %+v): page order not supplied by %s: %s", list, filter, index, plan)
			}
		}
	}
}
