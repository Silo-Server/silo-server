package sections

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/recommendations"
)

// poolReader answers every section read with fixed rows and records the
// library scope Because You Watched was asked for. The For You and taste
// match rows have rowType and rowLabel when set. The For You fill is fill,
// read fillReads times, failing with fillError when set.
type poolReader struct {
	pool              []recommendations.ScoredItem
	anchor            string
	scopes            [][]int
	readError         error
	rowType, rowLabel string
	fill              []recommendations.ScoredItem
	fillReads         int
	fillError         error
}

func (p *poolReader) row(label string) *recommendations.ForYouRow {
	if p.rowLabel != "" {
		label = p.rowLabel
	}
	return &recommendations.ForYouRow{Type: p.rowType, Label: label, Items: p.pool}
}

func (p *poolReader) SectionForYouMain(context.Context, int, string, catalog.AccessFilter) (*recommendations.ForYouRow, error) {
	if p.readError != nil {
		return nil, p.readError
	}
	return p.row(recommendations.ForYouLabel), nil
}

func (p *poolReader) SectionForYouFill(context.Context, int, string, catalog.AccessFilter) ([]recommendations.ScoredItem, error) {
	p.fillReads++
	return p.fill, p.fillError
}

func (p *poolReader) SectionBecauseYouWatched(_ context.Context, _ int, _, _ string, libraryIDs []int, _ catalog.AccessFilter) ([]recommendations.ScoredItem, string, error) {
	p.scopes = append(p.scopes, libraryIDs)
	return p.pool, p.anchor, p.readError
}

func (p *poolReader) SectionSimilarUsersLiked(context.Context, int, string, catalog.AccessFilter) ([]recommendations.ScoredItem, error) {
	return p.pool, p.readError
}

func (p *poolReader) SectionTasteMatchRow(context.Context, int, string, string, catalog.AccessFilter) (*recommendations.ForYouRow, error) {
	if p.readError != nil {
		return nil, p.readError
	}
	return p.row(""), nil
}

// A For You or taste-match section keeps a default heading only while it
// serves the profile's own row; served a row every profile gets, it takes
// that row's title. A heading an admin chose is never replaced.
func TestRecommendationSectionTitle(t *testing.T) {
	popular := &recommendations.ForYouRow{Type: recommendations.RecTypePopular, Label: "Popular on This Server"}
	genre := &recommendations.ForYouRow{Type: "genre_sampler", Label: "Top Drama"}
	highlyRated := &recommendations.ForYouRow{Type: recommendations.RecTypeTopRated, Label: "Highly Rated in Your Library"}
	recent := &recommendations.ForYouRow{Type: recommendations.RecTypeRecentlyAdded, Label: "Recently Added"}
	forYou := &recommendations.ForYouRow{Type: "cluster", Label: recommendations.ForYouLabel}
	cluster := &recommendations.ForYouRow{Type: "cluster", Label: "Because you enjoy Crime"}
	for _, tc := range []struct {
		name, title string
		row         *recommendations.ForYouRow
		want        string
	}{
		{"new profile's For You row", "Recommended for You", popular, "Popular on This Server"},
		{"default heading in another case", "  recommended FOR you ", recent, "Recently Added"},
		{"highly rated", "Recommended for You", highlyRated, "Highly Rated in Your Library"},
		{"taste match without a cluster", "Top Picks Today", genre, "Top Drama"},
		{"untitled section", "", genre, "Top Drama"},
		{"profile's own row", "Recommended for You", forYou, "Recommended for You"},
		{"profile's cluster row", "Top Picks Today", cluster, "Top Picks Today"},
		{"admin title", "Tonight's Picks", popular, "Tonight's Picks"},
		{"no row", "Recommended for You", nil, "Recommended for You"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := recommendationSectionTitle(tc.title, tc.row); got != tc.want {
				t.Fatalf("title = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLimitRecommendationItems(t *testing.T) {
	items := make([]*models.MediaItem, 30)
	for _, tc := range []struct{ limit, want int }{{0, 20}, {-3, 20}, {5, 5}, {25, 25}, {40, 30}} {
		if got := len(limitRecommendationItems(items, tc.limit)); got != tc.want {
			t.Errorf("limitRecommendationItems(30 items, %d) = %d items, want %d", tc.limit, got, tc.want)
		}
	}
}

// A library's recommendation sections fill from the row's whole cached pool:
// with the profile's 40 strongest picks in one movie library, the other
// library's row still gets its own 20, and a widened home fetch gets them all.
func TestRecommendationSectionsFillFromTheWholePoolPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	prefix := "recs-pool-" + uuid.NewString()[:8] + "-"
	var libraries [2]int
	for i := range libraries {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`, fmt.Sprintf("%slib-%d", prefix, i)).Scan(&libraries[i]); err != nil {
			t.Fatalf("seed library: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = ANY($1)`, libraries[:])
	})

	scored := make([]recommendations.ScoredItem, recommendations.CacheCandidateLimit)
	var firstLibrary, secondLibrary []string
	for i := range scored {
		id := fmt.Sprintf("%smovie-%02d", prefix, i)
		scored[i] = recommendations.ScoredItem{MediaItemID: id, Score: float64(len(scored) - i)}
		library := libraries[0]
		if i >= 40 {
			library = libraries[1]
			secondLibrary = append(secondLibrary, id)
		} else {
			firstLibrary = append(firstLibrary, id)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title, status, genres) VALUES ($1, 'movie', $1, 'matched', '{}'::text[])`, id); err != nil {
			t.Fatalf("seed item: %v", err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at) VALUES ($1, $2, $3)`, id, library, time.Now()); err != nil {
			t.Fatalf("seed membership: %v", err)
		}
	}
	reader := &poolReader{pool: scored, anchor: firstLibrary[0]}
	fetcher := NewFetcher(pool)
	fetcher.RecommendationReader = reader

	fetch := func(sectionType SectionType, itemLimit int, libraryID *int) []string {
		t.Helper()
		got, err := fetcher.FetchOne(ctx, ResolvedSection{ID: "recs", SectionType: sectionType, Title: "Recommended", ItemLimit: itemLimit}, libraryID, nil, 7, "p1", catalog.AccessFilter{})
		if err != nil {
			t.Fatalf("FetchOne(%s): %v", sectionType, err)
		}
		if got.TotalCount != len(got.Items) {
			t.Fatalf("FetchOne(%s) total %d for %d items", sectionType, got.TotalCount, len(got.Items))
		}
		return contentIDs(got.Items)
	}

	for _, sectionType := range []SectionType{SectionRecommendedForYou, SectionSimilarUsersLiked, SectionTasteMatch, SectionBecauseYouWatched} {
		if got := fetch(sectionType, 20, &libraries[1]); !slices.Equal(got, secondLibrary) {
			t.Fatalf("%s in the second library = %v, want its 20 picks %v", sectionType, got, secondLibrary)
		}
		if got := fetch(sectionType, 20, &libraries[0]); !slices.Equal(got, firstLibrary[:20]) {
			t.Fatalf("%s in the first library = %v, want its 20 strongest picks", sectionType, got)
		}
		// Home with hide-watched widens the fetch window past the row size.
		if got := fetch(sectionType, 100, nil); len(got) != recommendations.CacheCandidateLimit {
			t.Fatalf("%s on a widened home fetch = %d items, want %d", sectionType, len(got), recommendations.CacheCandidateLimit)
		}
	}

	// Because You Watched picks its anchor within the section's scope.
	reader.scopes = nil
	fetch(SectionBecauseYouWatched, 20, &libraries[1])
	fetch(SectionBecauseYouWatched, 20, nil)
	if len(reader.scopes) != 2 || !slices.Equal(reader.scopes[0], []int{libraries[1]}) || reader.scopes[1] != nil {
		t.Fatalf("anchor scopes = %v, want [[%d] []]", reader.scopes, libraries[1])
	}
}

// A library's For You section the main row leaves short continues with the
// profile's other personal rows in that library, in the reader's order and
// without repeating a main-row title. A row the main row fills, a home
// section and a row every profile is offered read no fill.
func TestForYouSectionFillsALibraryFromOtherPersonalRowsPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	prefix := "recs-fill-" + uuid.NewString()[:8] + "-"
	var movies, anime int
	for name, id := range map[string]*int{"movies": &movies, "anime": &anime} {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`, prefix+name).Scan(id); err != nil {
			t.Fatalf("seed library: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = ANY($1)`, []int{movies, anime})
	})
	seed := func(name string, n, library int) []recommendations.ScoredItem {
		t.Helper()
		items := make([]recommendations.ScoredItem, n)
		for i := range items {
			id := fmt.Sprintf("%s%s-%02d", prefix, name, i)
			items[i] = recommendations.ScoredItem{MediaItemID: id, Score: float64(n - i)}
			if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title, status, genres) VALUES ($1, 'movie', $1, 'matched', '{}'::text[])`, id); err != nil {
				t.Fatalf("seed item: %v", err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id, first_seen_at) VALUES ($1, $2, $3)`, id, library, time.Now()); err != nil {
				t.Fatalf("seed membership: %v", err)
			}
		}
		return items
	}
	// The main row holds 30 titles, all in the movie library. The fill holds
	// 25 anime titles, 5 more movie titles and 2 main-row titles.
	main := seed("main", 30, movies)
	animeFill := seed("anime", 25, anime)
	movieFill := seed("movie", 5, movies)
	fill := slices.Concat(animeFill[:10], main[:2], movieFill, animeFill[10:])
	reader := &poolReader{pool: main, rowType: "cluster", fill: fill}
	fetcher := NewFetcher(pool)
	fetcher.RecommendationReader = reader

	fetch := func(itemLimit int, libraryID *int) []string {
		t.Helper()
		got, err := fetcher.FetchOne(ctx, ResolvedSection{ID: "recs", SectionType: SectionRecommendedForYou, Title: "Recommended for You", ItemLimit: itemLimit}, libraryID, nil, 7, "p1", catalog.AccessFilter{})
		if err != nil {
			t.Fatalf("FetchOne: %v", err)
		}
		if got.TotalCount != len(got.Items) {
			t.Fatalf("total %d for %d items", got.TotalCount, len(got.Items))
		}
		if len(got.Items) > 0 && got.Title != "Recommended for You" {
			t.Fatalf("title = %q, want the section's heading", got.Title)
		}
		return contentIDs(got.Items)
	}
	ids := func(items ...[]recommendations.ScoredItem) []string {
		var out []string
		for _, row := range items {
			for _, item := range row {
				out = append(out, item.MediaItemID)
			}
		}
		return out
	}

	for _, tc := range []struct {
		name      string
		itemLimit int
		libraryID *int
		want      []string
		fillReads int
	}{
		{"anime library, no main-row title in it", 20, &anime, ids(animeFill[:20]), 1},
		{"movie library the main row fills", 20, &movies, ids(main[:20]), 0},
		{"movie library past the main row", 40, &movies, ids(main, movieFill), 1},
		{"home", 100, nil, ids(main), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader.fillReads = 0
			if got := fetch(tc.itemLimit, tc.libraryID); !slices.Equal(got, tc.want) {
				t.Fatalf("items = %v, want %v", got, tc.want)
			}
			if reader.fillReads != tc.fillReads {
				t.Fatalf("fill reads = %d, want %d", reader.fillReads, tc.fillReads)
			}
		})
	}

	// A new profile's Popular row is served as it is, titled after itself.
	reader.rowType, reader.rowLabel, reader.fillReads = recommendations.RecTypePopular, "Popular on This Server", 0
	got, err := fetcher.FetchOne(ctx, ResolvedSection{ID: "recs", SectionType: SectionRecommendedForYou, Title: "Recommended for You", ItemLimit: 20}, &anime, nil, 7, "p1", catalog.AccessFilter{})
	if err != nil || len(got.Items) != 0 || reader.fillReads != 0 {
		t.Fatalf("Popular row in the anime library = %d items, %d fill reads, %v; want an empty row and no fill", len(got.Items), reader.fillReads, err)
	}
}

// A failed fill fails the section, like a failed main-row read.
func TestForYouSectionFillErrorFailsTheSection(t *testing.T) {
	library := 3
	fetcher := &Fetcher{RecommendationReader: &poolReader{rowType: "cluster", fillError: fmt.Errorf("cache read failed")}}
	if _, err := fetcher.FetchOne(context.Background(), ResolvedSection{ID: "recs", SectionType: SectionRecommendedForYou, ItemLimit: 20}, &library, nil, 7, "p1", catalog.AccessFilter{}); err == nil {
		t.Fatal("FetchOne succeeded, want the fill's error")
	}
}

// A reader failure fails the section rather than answering an empty row.
func TestRecommendationSectionReadErrorFailsTheSection(t *testing.T) {
	fetcher := &Fetcher{RecommendationReader: &poolReader{readError: fmt.Errorf("cache read failed")}}
	for _, sectionType := range []SectionType{SectionRecommendedForYou, SectionSimilarUsersLiked, SectionTasteMatch, SectionBecauseYouWatched} {
		if _, err := fetcher.FetchOne(context.Background(), ResolvedSection{ID: "recs", SectionType: sectionType, ItemLimit: 20}, nil, nil, 7, "p1", catalog.AccessFilter{}); err == nil {
			t.Fatalf("%s: FetchOne succeeded, want the reader's error", sectionType)
		}
	}

	// Without a reader the section degrades to empty.
	got, err := (&Fetcher{}).FetchOne(context.Background(), ResolvedSection{ID: "recs", SectionType: SectionRecommendedForYou, ItemLimit: 20}, nil, nil, 7, "p1", catalog.AccessFilter{})
	if err != nil || len(got.Items) != 0 {
		t.Fatalf("FetchOne without a reader = %d items, %v; want an empty section", len(got.Items), err)
	}
}

// FetchOne titles a For You or taste-match section after the row it serves
// when that row is not the profile's own, and keeps an admin's heading; a
// row filtered empty leaves the heading alone.
func TestRecommendationSectionsTakeTheServedRowTitlePostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	id := "recs-title-" + uuid.NewString()[:8]
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, id) })
	if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title, status, genres) VALUES ($1, 'movie', $1, 'matched', '{}'::text[])`, id); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	fetcher := NewFetcher(pool)
	for _, tc := range []struct {
		name              string
		sectionType       SectionType
		title             string
		rowType, rowLabel string
		pool              []recommendations.ScoredItem
		want              string
	}{
		{"new profile", SectionRecommendedForYou, "Recommended for You", recommendations.RecTypePopular, "Popular on This Server", []recommendations.ScoredItem{{MediaItemID: id}}, "Popular on This Server"},
		{"warm profile", SectionRecommendedForYou, "Recommended for You", "cluster", recommendations.ForYouLabel, []recommendations.ScoredItem{{MediaItemID: id}}, "Recommended for You"},
		{"taste match fallback", SectionTasteMatch, "Top Picks Today", "genre_sampler", "Top Drama", []recommendations.ScoredItem{{MediaItemID: id}}, "Top Drama"},
		{"admin title", SectionRecommendedForYou, "Movie Night", recommendations.RecTypePopular, "Popular on This Server", []recommendations.ScoredItem{{MediaItemID: id}}, "Movie Night"},
		{"row filtered empty", SectionRecommendedForYou, "Recommended for You", recommendations.RecTypePopular, "Popular on This Server", []recommendations.ScoredItem{{MediaItemID: id + "-missing"}}, "Recommended for You"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fetcher.RecommendationReader = &poolReader{pool: tc.pool, rowType: tc.rowType, rowLabel: tc.rowLabel}
			got, err := fetcher.FetchOne(ctx, ResolvedSection{ID: "recs", SectionType: tc.sectionType, Title: tc.title, ItemLimit: 20}, nil, nil, 7, "p1", catalog.AccessFilter{})
			if err != nil {
				t.Fatal(err)
			}
			if got.Title != tc.want {
				t.Fatalf("title = %q, want %q", got.Title, tc.want)
			}
		})
	}
}
