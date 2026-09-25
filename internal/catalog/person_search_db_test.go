package catalog

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/access"
)

func TestPersonSearchScopeAndRankingPostgres(t *testing.T) {
	pool := collectionSortTestPool(t)
	ctx := t.Context()
	prefix := "person-search-" + uuid.NewString()
	name := "Nathan " + prefix
	names := []string{name, "Alice, " + name, "Bob, " + name, name + " Jr", "Zoe, " + name}
	baseID := time.Now().UnixNano()
	ids := make([]int64, len(names))
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanup, `DELETE FROM item_people WHERE person_id = ANY($1)`, ids)
		_, _ = pool.Exec(cleanup, `DELETE FROM people WHERE id = ANY($1)`, ids)
		_, _ = pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
	})
	for i, personName := range names {
		ids[i] = baseID + int64(i)
		exec(`INSERT INTO people(id, name) VALUES ($1, $2)`, ids[i], personName)
	}
	for i, credit := range []struct {
		person   int
		typeName string
		kind     int
	}{
		{0, "movie", 1}, {0, "series", 1}, {0, "audiobook", 8},
		{1, "audiobook", 7}, {2, "movie", 2}, {3, "series", 1},
	} {
		contentID := fmt.Sprintf("%s-%d", prefix, i)
		exec(`INSERT INTO media_items(content_id, type, title) VALUES ($1, $2, 'Synthetic title')`, contentID, credit.typeName)
		exec(`INSERT INTO item_people(id, content_id, person_id, kind) VALUES ($1, $2, $3, $4)`, baseID+int64(i), contentID, ids[credit.person], credit.kind)
	}
	repo := NewPersonRepository(pool)
	for _, tc := range []struct {
		name, scope string
		limit       int
		want        []int64
	}{
		{"all exact before limit", "", 1, ids[:1]},
		{"all retains credited matches", "", 20, ids[:4]},
		{"media excludes audiobook-only credits", "video", 20, []int64{ids[0], ids[2], ids[3]}},
		{"media exact before limit", "video", 1, ids[:1]},
		{"audiobooks include narrators and authors", "audiobook", 20, ids[:2]},
		{"movies include directors", "movie", 20, []int64{ids[0], ids[2]}},
		{"series", "series", 20, []int64{ids[0], ids[3]}},
		{"empty scope results", "ebook", 20, []int64{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			people, err := repo.SearchScoped(t.Context(), "  "+strings.ToLower(name)+"  ", tc.limit, tc.scope, AccessFilter{})
			if err != nil {
				t.Fatal(err)
			}
			got := make([]int64, len(people))
			for i, person := range people {
				got[i] = person.ID
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
	legacy, err := repo.Search(ctx, name, 1)
	if err != nil || len(legacy) != 1 || legacy[0].ID != ids[1] {
		t.Fatalf("legacy alphabetical search changed: %+v, %v", legacy, err)
	}
}

func TestPersonSearchViewerAccessPostgres(t *testing.T) {
	pool := collectionSortTestPool(t)
	ctx := t.Context()
	prefix := "person-access-" + uuid.NewString()
	baseID := time.Now().UnixNano()
	ids := make([]int64, 8)
	libraries := make([]int, 2)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanup, `DELETE FROM item_people WHERE person_id = ANY($1)`, ids)
		_, _ = pool.Exec(cleanup, `DELETE FROM people WHERE id = ANY($1)`, ids)
		_, _ = pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(cleanup, `DELETE FROM media_folders WHERE id = ANY($1)`, libraries)
	})
	for i := range libraries {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('movies',$1,true) RETURNING id`, prefix).Scan(&libraries[i]); err != nil {
			t.Fatal(err)
		}
	}
	for i := range ids {
		ids[i] = baseID + int64(i)
		name := prefix
		if i > 0 {
			name = fmt.Sprintf("%c %s", 'A'+i-1, prefix)
		}
		exec(`INSERT INTO people(id, name) VALUES ($1, $2)`, ids[i], name)
		if i == 6 {
			continue
		} // No credited items.
		contentID := fmt.Sprintf("%s-%d", prefix, i)
		itemType, rating := "movie", "G"
		if i == 3 {
			rating = "R"
		}
		if i == 4 {
			itemType = "ebook"
		}
		if i == 7 {
			itemType = "series"
		}
		exec(`INSERT INTO media_items(content_id,type,title,content_rating,content_rating_age) VALUES($1,$2,'Synthetic title',$3,$4)`, contentID, itemType, rating, access.StoredRating(rating))
		exec(`INSERT INTO item_people(id,content_id,person_id,kind) VALUES($1,$2,$1,1)`, ids[i], contentID)
		if i != 5 { // Orphan item has no library membership.
			library := libraries[0]
			if i == 0 {
				library = libraries[1]
			}
			exec(`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, contentID, library)
		}
		if i == 2 {
			exec(`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, contentID, libraries[1])
		}
	}
	repo := NewPersonRepository(pool)
	for _, tc := range []struct {
		name, scope string
		limit       int
		filter      AccessFilter
		want        []int64
	}{
		{"restricted library", "movie", 20, AccessFilter{AllowedLibraryIDs: libraries[:1]}, []int64{ids[1], ids[2], ids[3]}},
		{"access before ranking and limit", "movie", 1, AccessFilter{AllowedLibraryIDs: libraries[:1]}, []int64{ids[1]}},
		{"no allowed libraries", "", 20, AccessFilter{AllowedLibraryIDs: []int{}}, nil},
		{"disabled membership hides shared and orphan items", "movie", 20, AccessFilter{DisabledLibraryIDs: libraries[1:]}, []int64{ids[1], ids[3]}},
		{"rating ceiling", "movie", 20, AccessFilter{AllowedLibraryIDs: libraries[:1], MaturityLimits: access.MaturityLimits{MaxContentRating: "PG-13"}}, []int64{ids[1], ids[2]}},
		{"excluded type across all scopes", "", 20, AccessFilter{AllowedLibraryIDs: libraries[:1], ExcludedMediaTypes: []string{"ebook"}}, []int64{ids[1], ids[2], ids[3], ids[7]}},
		{"combined restrictions across all scopes", "", 20, AccessFilter{AllowedLibraryIDs: libraries[:1], DisabledLibraryIDs: libraries[1:], MaturityLimits: access.MaturityLimits{MaxContentRating: "PG-13"}, ExcludedMediaTypes: []string{"ebook"}}, []int64{ids[1], ids[7]}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			people, err := repo.SearchScoped(t.Context(), prefix, tc.limit, tc.scope, tc.filter)
			if err != nil {
				t.Fatal(err)
			}
			got := make([]int64, len(people))
			for i, p := range people {
				got[i] = p.ID
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPersonSearchEpisodeParentAccessPostgres(t *testing.T) {
	pool := collectionSortTestPool(t)
	ctx := t.Context()
	prefix := "person-episode-" + uuid.NewString()
	baseID := time.Now().UnixNano()
	ids := make([]int64, 6)
	libraries := make([]int, 2)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanup, `DELETE FROM item_people WHERE person_id = ANY($1)`, ids)
		_, _ = pool.Exec(cleanup, `DELETE FROM people WHERE id = ANY($1)`, ids)
		_, _ = pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(cleanup, `DELETE FROM media_folders WHERE id = ANY($1)`, libraries)
	})
	for i := range libraries {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('tv',$1,true) RETURNING id`, prefix).Scan(&libraries[i]); err != nil {
			t.Fatal(err)
		}
	}
	for i := range ids {
		ids[i] = baseID + int64(i)
		name := prefix
		if i > 0 {
			name = fmt.Sprintf("%c %s", 'A'+i-1, prefix)
		}
		exec(`INSERT INTO people(id,name) VALUES($1,$2)`, ids[i], name)
		episodeID := fmt.Sprintf("%s-episode-%d", prefix, i)
		exec(`INSERT INTO media_items(content_id,type,title,content_rating,content_rating_age) VALUES($1,'episode','Synthetic episode','G',0)`, episodeID)
		exec(`INSERT INTO item_people(id,content_id,person_id,kind) VALUES($1,$2,$1,1)`, ids[i], episodeID)
		if i != 1 { // Accessible episode has no independent library membership.
			exec(`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, episodeID, libraries[0])
		}
		if i == 5 { // An episode without a parent cannot establish visibility.
			continue
		}
		seriesID := fmt.Sprintf("%s-series-%d", prefix, i)
		rating := "G"
		if i == 3 {
			rating = "R"
		}
		exec(`INSERT INTO media_items(content_id,type,title,content_rating,content_rating_age) VALUES($1,'series','Synthetic series',$2,$3)`, seriesID, rating, access.StoredRating(rating))
		exec(`INSERT INTO episodes(content_id,series_id,season_number,episode_number,title) VALUES($1,$2,1,1,'Synthetic episode')`, episodeID, seriesID)
		if i != 4 { // Orphan parent, despite the child's permissive membership.
			library := libraries[0]
			if i == 0 {
				library = libraries[1]
			}
			exec(`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, seriesID, library)
		}
		if i == 2 { // A disabled membership hides a series in both libraries.
			exec(`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, seriesID, libraries[1])
		}
	}
	repo := NewPersonRepository(pool)
	for _, scope := range []string{"", "episode"} {
		for _, tc := range []struct {
			name   string
			filter AccessFilter
			limit  int
			want   []int64
		}{
			{"allowed library", AccessFilter{AllowedLibraryIDs: libraries[:1]}, 20, []int64{ids[1], ids[2], ids[3]}},
			{"disabled library", AccessFilter{DisabledLibraryIDs: libraries[1:]}, 20, []int64{ids[1], ids[3]}},
			{"parent rating", AccessFilter{AllowedLibraryIDs: libraries[:1], MaturityLimits: access.MaturityLimits{MaxContentRating: "PG-13"}}, 20, []int64{ids[1], ids[2]}},
			{"access before limit", AccessFilter{AllowedLibraryIDs: libraries[:1], DisabledLibraryIDs: libraries[1:], MaturityLimits: access.MaturityLimits{MaxContentRating: "PG-13"}}, 1, []int64{ids[1]}},
			{"excluded credit type", AccessFilter{ExcludedMediaTypes: []string{"episode"}}, 20, nil},
			{"missing parent", AccessFilter{}, 20, ids[:5]},
		} {
			t.Run(scope+"/"+tc.name, func(t *testing.T) {
				people, err := repo.SearchScoped(t.Context(), prefix, tc.limit, scope, tc.filter)
				if err != nil {
					t.Fatal(err)
				}
				got := make([]int64, len(people))
				for i, person := range people {
					got[i] = person.ID
				}
				if !slices.Equal(got, tc.want) {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			})
		}
	}
}
