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
// library scope Because You Watched was asked for.
type poolReader struct {
	pool      []recommendations.ScoredItem
	anchor    string
	scopes    [][]int
	readError error
}

func (p *poolReader) SectionForYouMain(context.Context, int, string, catalog.AccessFilter) (*recommendations.ForYouRow, error) {
	if p.readError != nil {
		return nil, p.readError
	}
	return &recommendations.ForYouRow{Label: "For You", Items: p.pool}, nil
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
	return &recommendations.ForYouRow{Items: p.pool}, nil
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
