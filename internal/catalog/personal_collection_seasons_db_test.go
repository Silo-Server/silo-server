package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

type seasonCollectionFixture struct {
	pool                      *pgxpool.Pool
	userID                    int
	profile                   string
	library, otherLibrary     int
	collectionID              string
	series, s0, s1, s2, movie string
	resolver                  *CatalogResolver
}

func newSeasonCollectionFixture(t *testing.T) seasonCollectionFixture {
	t.Helper()
	pool := newBatchEquivTestPool(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	f := seasonCollectionFixture{pool: pool, profile: fmt.Sprintf("p-%d", suffix)}
	f.series = fmt.Sprintf("season-member-series-%d", suffix)
	f.s0, f.s1, f.s2 = f.series+"-s0", f.series+"-s1", f.series+"-s2"
	f.movie = fmt.Sprintf("season-member-movie-%d", suffix)
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, fmt.Sprintf("season-member-%d", suffix)).Scan(&f.userID); err != nil {
		t.Fatal(err)
	}
	for i, target := range []*int{&f.library, &f.otherLibrary} {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('series',$1,true) RETURNING id`, fmt.Sprintf("season-member-%d-%d", suffix, i)).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{f.series, f.movie})
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = ANY($1)`, []int{f.library, f.otherLibrary})
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, f.userID)
		_, _ = pool.Exec(ctx, `DELETE FROM user_collection_revisions WHERE user_id=$1`, f.userID)
	})
	batchEquivExec(t, pool, `INSERT INTO user_profiles(id,user_id,name) VALUES($1,$2,'one')`, f.profile, f.userID)
	batchEquivExec(t, pool, `INSERT INTO media_items(content_id,type,title,genres,poster_path,content_rating,content_rating_age,year)
		VALUES($1,'series','Alpha','{}','series/poster.jpg','TV-MA',17,2020)`, f.series)
	// Rated G (age 0) so a PG ceiling keeps it regardless of the unrated-content setting.
	batchEquivExec(t, pool, `INSERT INTO media_items(content_id,type,title,genres,year,content_rating,content_rating_age) VALUES($1,'movie','Gamma','{}',2001,'G',0)`, f.movie)
	for _, id := range []string{f.series, f.movie} {
		batchEquivExec(t, pool, `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, id, f.library)
	}
	batchEquivExec(t, pool, `INSERT INTO seasons(content_id,series_id,season_number) VALUES($1,$2,0)`, f.s0, f.series)
	batchEquivExec(t, pool, `INSERT INTO seasons(content_id,series_id,season_number,air_date) VALUES($1,$2,1,'2020-03-01')`, f.s1, f.series)
	batchEquivExec(t, pool, `INSERT INTO seasons(content_id,series_id,season_number,poster_path,air_date) VALUES($1,$2,2,'season2/poster.jpg','2022-03-01')`, f.s2, f.series)
	provider := pgstore.NewPostgresProvider(pool)
	store, err := provider.ForUser(ctx, f.userID)
	if err != nil {
		t.Fatal(err)
	}
	c, err := store.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: f.profile, Name: "To Watch"})
	if err != nil {
		t.Fatal(err)
	}
	f.collectionID = c.ID
	for i, id := range []string{f.s2, f.movie, f.s1, f.series, f.s0} {
		if err := store.AddCollectionItem(ctx, c.ID, id, i); err != nil {
			t.Fatal(err)
		}
	}
	f.resolver = NewCatalogResolver(NewBrowseRepository(pool), NewItemRepository(pool)).WithUserStoreProvider(provider)
	return f
}

func (f seasonCollectionFixture) access() AccessFilter {
	return AccessFilter{UserID: f.userID, ProfileID: f.profile, AllowedLibraryIDs: []int{f.library}}
}

func (f seasonCollectionFixture) resolve(t *testing.T, filter AccessFilter, sort QuerySort, limit int, previous *CatalogResult) *CatalogResult {
	t.Helper()
	req := CatalogRequest{Source: CatalogSourceUserCollection, CollectionID: f.collectionID, CursorPaging: true, Limit: limit}
	req.Query.Sort = sort
	if previous != nil {
		req.After = previous.Next
		req.ResolvedSort = new(previous.EffectiveSort)
	}
	result, err := f.resolver.Resolve(context.Background(), req, filter)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func (f seasonCollectionFixture) page(t *testing.T, filter AccessFilter, sort QuerySort, limit int) *CatalogResult {
	t.Helper()
	return f.resolve(t, filter, sort, limit, nil)
}

func ids(items []*models.MediaItem) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.ContentID)
	}
	return out
}

func TestPersonalCollectionListsSeasonMembersDB(t *testing.T) {
	f := newSeasonCollectionFixture(t)
	got := f.page(t, f.access(), QuerySort{}, 50)
	if want := []string{f.s2, f.movie, f.s1, f.series, f.s0}; fmt.Sprint(ids(got.Items)) != fmt.Sprint(want) {
		t.Fatalf("manual order = %v, want %v", ids(got.Items), want)
	}
	byID := map[string]*models.MediaItem{}
	for _, item := range got.Items {
		byID[item.ContentID] = item
	}
	if s2 := byID[f.s2]; s2.Type != "season" || s2.Title != "Alpha" || s2.PosterPath != "season2/poster.jpg" || s2.Year != 2022 {
		t.Fatalf("season 2 row = %+v", s2)
	}
	if s1 := byID[f.s1]; s1.PosterPath != "series/poster.jpg" || s1.Year != 2020 {
		t.Fatalf("season 1 should fall back to series poster: %+v", s1)
	}
}

func TestPersonalCollectionSeasonTitleOrderAndPagingDB(t *testing.T) {
	f := newSeasonCollectionFixture(t)
	sort := QuerySort{Field: "title", Order: "asc"}
	want := []string{f.series, f.s0, f.s1, f.s2, f.movie}
	all := f.page(t, f.access(), sort, 50)
	if fmt.Sprint(ids(all.Items)) != fmt.Sprint(want) {
		t.Fatalf("title order = %v, want %v", ids(all.Items), want)
	}
	var paged []string
	var previous *CatalogResult
	for range 10 {
		page := f.resolve(t, f.access(), sort, 2, previous)
		paged = append(paged, ids(page.Items)...)
		if !page.HasMore || page.Next == nil {
			break
		}
		previous = page
	}
	if fmt.Sprint(paged) != fmt.Sprint(want) {
		t.Fatalf("paged = %v, want %v", paged, want)
	}
}

func TestPersonalCollectionSeasonsFollowSeriesAccessDB(t *testing.T) {
	f := newSeasonCollectionFixture(t)
	other := f.access()
	other.AllowedLibraryIDs = []int{f.otherLibrary}
	if got := ids(f.page(t, other, QuerySort{}, 50).Items); len(got) != 0 {
		t.Fatalf("other library sees %v", got)
	}
	disabled := f.access()
	disabled.AllowedLibraryIDs = nil
	disabled.DisabledLibraryIDs = []int{f.library}
	if got := ids(f.page(t, disabled, QuerySort{}, 50).Items); len(got) != 0 {
		t.Fatalf("disabled library sees %v", got)
	}
	rated := f.access()
	rated.MaturityLimits = access.MaturityLimits{MaxContentRating: "PG"}
	if got := ids(f.page(t, rated, QuerySort{}, 50).Items); fmt.Sprint(got) != fmt.Sprint([]string{f.movie}) {
		t.Fatalf("PG profile sees %v, want only the movie", got)
	}
}

func TestPersonalCollectionSeasonsSurviveEverySortDB(t *testing.T) {
	f := newSeasonCollectionFixture(t)
	for field := range querySortDefs {
		for _, order := range []string{"asc", "desc"} {
			got := f.page(t, f.access(), QuerySort{Field: field, Order: order}, 50)
			if len(got.Items) != 5 {
				t.Fatalf("sort %s %s returned %v", field, order, ids(got.Items))
			}
		}
	}
}

func displayRule(field string, value any) string {
	return fmt.Sprintf(`{"match":"all","groups":[{"match":"all","rules":[{"field":%q,"op":"is","value":%s}]}]}`, field, mustJSON(value))
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func (f seasonCollectionFixture) setDisplay(t *testing.T, display string) {
	t.Helper()
	batchEquivExec(t, f.pool, `UPDATE user_personal_collections SET display_query_definition=$1::jsonb WHERE id=$2`, display, f.collectionID)
}

func (f seasonCollectionFixture) count(t *testing.T) int {
	t.Helper()
	var display string
	if err := f.pool.QueryRow(context.Background(), `SELECT COALESCE(display_query_definition::text,'') FROM user_personal_collections WHERE id=$1`, f.collectionID).Scan(&display); err != nil {
		t.Fatal(err)
	}
	counts, err := CountPersonalCollections(context.Background(), f.pool, f.userID, []PersonalCollectionDefinition{{ID: f.collectionID, CollectionType: "manual", DisplayQueryDefinition: display}}, f.access())
	if err != nil {
		t.Fatal(err)
	}
	return counts[f.collectionID]
}

func (f seasonCollectionFixture) addEpisodes(t *testing.T, seasonID string, n int) {
	t.Helper()
	batchEquivExec(t, f.pool, `INSERT INTO episodes (content_id,series_id,season_id,season_number,episode_number,title)
		SELECT $1 || '-e' || n, s.series_id, s.content_id, s.season_number, n, 'Episode ' || n
		FROM seasons s, generate_series(1,$2::int) n WHERE s.content_id = $1`, seasonID, n)
	batchEquivExec(t, f.pool, `INSERT INTO media_files (content_id,episode_id,media_folder_id,file_path)
		SELECT e.series_id, e.content_id, $2, e.content_id || '.mkv' FROM episodes e WHERE e.season_id = $1`, seasonID, f.library)
	batchEquivExec(t, f.pool, `INSERT INTO episode_libraries (episode_id,media_folder_id)
		SELECT content_id, $2 FROM episodes WHERE season_id = $1`, seasonID, f.library)
}

func (f seasonCollectionFixture) markEpisodesWatched(t *testing.T, seasonID string) {
	t.Helper()
	batchEquivExec(t, f.pool, `INSERT INTO user_watch_progress (user_id,profile_id,media_item_id,completed)
		SELECT $1, $2, content_id, TRUE FROM episodes WHERE season_id = $3`, f.userID, f.profile, seasonID)
}

func TestPersonalCollectionTypeFilterIncludesSeasonsDB(t *testing.T) {
	f := newSeasonCollectionFixture(t)
	f.setDisplay(t, displayRule("type", "series"))
	got := ids(f.page(t, f.access(), QuerySort{}, 50).Items)
	if want := []string{f.s2, f.s1, f.series, f.s0}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("series filter = %v, want %v", got, want)
	}
	if n := f.count(t); n != 4 {
		t.Fatalf("series filter count = %d, want 4", n)
	}
	f.setDisplay(t, displayRule("type", "movie"))
	if got := ids(f.page(t, f.access(), QuerySort{}, 50).Items); fmt.Sprint(got) != fmt.Sprint([]string{f.movie}) {
		t.Fatalf("movie filter = %v", got)
	}
	if n := f.count(t); n != 1 {
		t.Fatalf("movie filter count = %d, want 1", n)
	}
}

func TestPersonalCollectionWatchedFilterRollsUpSeasonsDB(t *testing.T) {
	f := newSeasonCollectionFixture(t)
	f.addEpisodes(t, f.s1, 2)
	f.addEpisodes(t, f.s2, 2)
	f.markEpisodesWatched(t, f.s1)
	f.setDisplay(t, displayRule("watched", true))
	got := ids(f.page(t, f.access(), QuerySort{}, 50).Items)
	if fmt.Sprint(got) != fmt.Sprint([]string{f.s1}) {
		t.Fatalf("watched filter = %v, want only fully watched season 1", got)
	}
	if n := f.count(t); n != 1 {
		t.Fatalf("watched count = %d, want 1", n)
	}
}

func TestCountVisiblePersonalCollectionMembersIncludesSeasonsDB(t *testing.T) {
	f := newSeasonCollectionFixture(t)
	repo := NewItemRepository(f.pool)
	cases := []struct {
		name   string
		filter AccessFilter
		want   int
	}{
		{"unrestricted", AccessFilter{}, 5},
		{"own library", f.access(), 5},
		{"other library", AccessFilter{AllowedLibraryIDs: []int{f.otherLibrary}}, 0},
		{"PG ceiling", AccessFilter{AllowedLibraryIDs: []int{f.library}, MaturityLimits: access.MaturityLimits{MaxContentRating: "PG"}}, 1},
	}
	for _, tc := range cases {
		got, err := repo.CountVisiblePersonalCollectionMembers(context.Background(), f.userID, []string{f.collectionID}, tc.filter)
		if err != nil {
			t.Fatal(err)
		}
		if got[f.collectionID] != tc.want {
			t.Fatalf("%s: count = %d, want %d", tc.name, got[f.collectionID], tc.want)
		}
	}
	batchEquivExec(t, f.pool, `DELETE FROM seasons WHERE content_id=$1`, f.s2)
	got, err := repo.CountVisiblePersonalCollectionMembers(context.Background(), f.userID, []string{f.collectionID}, f.access())
	if err != nil {
		t.Fatal(err)
	}
	if got[f.collectionID] != 4 {
		t.Fatalf("after deleting season 2: count = %d, want 4", got[f.collectionID])
	}
	if items := ids(f.page(t, f.access(), QuerySort{}, 50).Items); len(items) != 4 {
		t.Fatalf("after deleting season 2 the grid shows %v", items)
	}
}

func TestGetVisibleSeasonsWithAccessDB(t *testing.T) {
	f := newSeasonCollectionFixture(t)
	repo := NewItemRepository(f.pool)
	got, err := repo.GetVisibleSeasonsWithAccess(context.Background(), []string{f.s0, f.s2, f.movie, f.series + "-S9"}, f.access())
	if err != nil {
		t.Fatal(err)
	}
	titles := map[string]string{}
	for _, s := range got {
		titles[s.SeasonID] = s.DisplayTitle()
	}
	if len(titles) != 2 || titles[f.s0] != "Alpha — Specials" || titles[f.s2] != "Alpha — Season 2" {
		t.Fatalf("visible seasons = %v", titles)
	}
	hidden, err := repo.GetVisibleSeasonsWithAccess(context.Background(), []string{f.s2}, AccessFilter{AllowedLibraryIDs: []int{f.otherLibrary}})
	if err != nil {
		t.Fatal(err)
	}
	if len(hidden) != 0 {
		t.Fatalf("other library sees %v", hidden)
	}
}

func TestPersonalCollectionTitleSortKeepsSeasonsWithSeriesDB(t *testing.T) {
	f := newSeasonCollectionFixture(t)
	between := f.movie + "-between"
	// "alpha beta" sorts after "alpha" but before any "alpha season ..." key.
	batchEquivExec(t, f.pool, `INSERT INTO media_items(content_id,type,title,genres,content_rating,content_rating_age) VALUES($1,'movie','Alpha Beta','{}','G',0)`, between)
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id=$1`, between)
	})
	batchEquivExec(t, f.pool, `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, between, f.library)
	batchEquivExec(t, f.pool, `INSERT INTO user_personal_collection_items(user_id,collection_id,media_item_id,position) VALUES($1,$2,$3,9)`, f.userID, f.collectionID, between)
	sort := QuerySort{Field: "title", Order: "asc"}
	want := []string{f.series, f.s0, f.s1, f.s2, between, f.movie}
	if got := ids(f.page(t, f.access(), sort, 50).Items); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("title order = %v, want %v", got, want)
	}
	var paged []string
	var previous *CatalogResult
	for range 10 {
		page := f.resolve(t, f.access(), sort, 2, previous)
		paged = append(paged, ids(page.Items)...)
		if !page.HasMore || page.Next == nil {
			break
		}
		previous = page
	}
	if fmt.Sprint(paged) != fmt.Sprint(want) {
		t.Fatalf("paged = %v, want %v", paged, want)
	}
}

func (f seasonCollectionFixture) scoped(t *testing.T, scope string) []string {
	t.Helper()
	req := CatalogRequest{Source: CatalogSourceUserCollection, CollectionID: f.collectionID, CursorPaging: true, Limit: 50}
	req.Query.MediaScope = scope
	result, err := f.resolver.Resolve(context.Background(), req, f.access())
	if err != nil {
		t.Fatal(err)
	}
	return ids(result.Items)
}

func TestPersonalCollectionSeriesScopeIncludesSeasonsDB(t *testing.T) {
	f := newSeasonCollectionFixture(t)
	if got, want := f.scoped(t, "series"), []string{f.s2, f.s1, f.series, f.s0}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("series scope = %v, want %v", got, want)
	}
	if got := f.scoped(t, "movie"); fmt.Sprint(got) != fmt.Sprint([]string{f.movie}) {
		t.Fatalf("movie scope = %v", got)
	}
	f.addEpisodes(t, f.s1, 2)
	f.addEpisodes(t, f.s2, 2)
	f.markEpisodesWatched(t, f.s1)
	f.setDisplay(t, displayRule("watched", true))
	if got := f.scoped(t, "series"); fmt.Sprint(got) != fmt.Sprint([]string{f.s1}) {
		t.Fatalf("watched within series scope = %v, want only season 1", got)
	}
}

func TestPersonalCollectionAddedSortPlacesSeasonsWithSeriesDB(t *testing.T) {
	f := newSeasonCollectionFixture(t)
	batchEquivExec(t, f.pool, `UPDATE media_item_libraries SET first_seen_at='2020-01-01' WHERE content_id=$1`, f.movie)
	batchEquivExec(t, f.pool, `UPDATE media_item_libraries SET first_seen_at='2021-01-01' WHERE content_id=$1`, f.series)
	desc := ids(f.page(t, f.access(), QuerySort{Field: "added_at", Order: "desc"}, 50).Items)
	if want := []string{f.series, f.s0, f.s1, f.s2, f.movie}; fmt.Sprint(desc) != fmt.Sprint(want) {
		t.Fatalf("added desc = %v, want %v", desc, want)
	}
	asc := ids(f.page(t, f.access(), QuerySort{Field: "added_at", Order: "asc"}, 50).Items)
	if want := []string{f.movie, f.series, f.s0, f.s1, f.s2}; fmt.Sprint(asc) != fmt.Sprint(want) {
		t.Fatalf("added asc = %v, want %v", asc, want)
	}
}

func TestPersonalCollectionFileFiltersMatchSeasonEpisodesDB(t *testing.T) {
	f := newSeasonCollectionFixture(t)
	f.addEpisodes(t, f.s1, 2)
	f.addEpisodes(t, f.s2, 2)
	batchEquivExec(t, f.pool, `UPDATE media_files SET resolution='1080p' WHERE episode_id IN (SELECT content_id FROM episodes WHERE season_id=$1)`, f.s1)
	batchEquivExec(t, f.pool, `UPDATE media_files SET resolution='2160p' WHERE episode_id IN (SELECT content_id FROM episodes WHERE season_id=$1)`, f.s2)
	req := CatalogRequest{Source: CatalogSourceUserCollection, CollectionID: f.collectionID, CursorPaging: true, Limit: 50}
	req.Query.Match = "all"
	req.Query.Groups = []QueryGroup{{Match: "all", Rules: []QueryRule{{Field: "resolution", Op: "is", Value: "1080p"}}}}
	result, err := f.resolver.Resolve(context.Background(), req, f.access())
	if err != nil {
		t.Fatal(err)
	}
	// Season 1 has 1080p episodes; the series has them too; season 2 is 4K only.
	if got, want := ids(result.Items), []string{f.s1, f.series}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("1080p filter = %v, want %v", got, want)
	}
}
