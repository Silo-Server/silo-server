package catalog

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// batchImageResolver records every batch resolve so a test can assert the
// page was signed once, and answers with a recognizable URL per path.
type batchImageResolver struct {
	batches [][]string
}

func (r *batchImageResolver) ResolveImageURL(_ context.Context, path, _ string) string {
	return "single://" + path
}

func (r *batchImageResolver) ResolveImageURLs(_ context.Context, paths []string, _ string) map[string]string {
	r.batches = append(r.batches, append([]string(nil), paths...))
	out := make(map[string]string, len(paths))
	for _, p := range paths {
		out[p] = "batch://" + p
	}
	return out
}

func TestGetItemCardsByIDsSignsThePageInOneBatch(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	itemRepo := NewItemRepository(pool)
	svc := NewDetailService(itemRepo, NewEpisodeRepository(pool), NewSeasonRepository(pool), NewPersonRepository(pool), nil)
	resolver := &batchImageResolver{}
	svc.SetImageResolver(resolver)

	ids := []string{"cards-movie-" + t.Name(), "cards-series-" + t.Name()}
	for i, id := range ids {
		item := &models.MediaItem{ContentID: id, Type: "movie", Title: "Card " + id, Year: 2020 + i, Status: "matched",
			PosterPath: "tmdb/movies/" + id + "/poster/original.abc.webp", BackdropPath: "tmdb/movies/" + id + "/backdrop/original.def.webp", Genres: []string{}}
		if i == 1 {
			item.Type = "series"
		}
		if err := itemRepo.Upsert(t.Context(), item); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = itemRepo.Delete(context.Background(), id) })
	}

	cards, err := svc.GetItemCardsByIDs(t.Context(), append(ids, "cards-missing"), AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 2 {
		t.Fatalf("cards = %d, want 2 (missing id dropped)", len(cards))
	}
	if len(resolver.batches) != 1 || len(resolver.batches[0]) != 4 {
		t.Fatalf("artwork was not signed in one batch of four paths: %+v", resolver.batches)
	}
	movie := cards[ids[0]]
	if movie == nil || movie.Title != "Card "+ids[0] || movie.PosterURL == "" || movie.BackdropURL == "" {
		t.Fatalf("movie card = %+v", movie)
	}
	if movie.PosterURL[:8] != "batch://" {
		t.Fatalf("poster resolved outside the batch: %s", movie.PosterURL)
	}
	if len(movie.Versions) != 0 || movie.Cast != nil {
		t.Fatalf("card carried item-page payload: %+v", movie)
	}
}
