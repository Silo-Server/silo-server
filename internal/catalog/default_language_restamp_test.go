package catalog

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

func newRestampTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestItemUpsertRestampsDefaultMetadataLanguage covers the persistence half of
// issue #211: when a refresh adopts a new canonical language, the upsert must
// accept the incoming non-empty default_metadata_language instead of pinning
// to the stamp written at first match. An empty incoming value must still
// keep the existing stamp (scanner skeleton writes send the zero value).
func TestItemUpsertRestampsDefaultMetadataLanguage(t *testing.T) {
	ctx := context.Background()
	pool := newRestampTestPool(t)
	repo := NewItemRepository(pool)

	contentID := fmt.Sprintf("restamp-item-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID)
	})

	base := &models.MediaItem{
		ContentID:               contentID,
		Type:                    "movie",
		Title:                   "Gammel Titel",
		Status:                  "matched",
		DefaultMetadataLanguage: "zh",
		Studios:                 []string{},
		Networks:                []string{},
		Countries:               []string{},
		Genres:                  []string{},
	}
	if err := repo.Upsert(ctx, base); err != nil {
		t.Fatalf("initial upsert: %v", err)
	}

	restamped := *base
	restamped.Title = "Ny Titel"
	restamped.DefaultMetadataLanguage = "da"
	if err := repo.Upsert(ctx, &restamped); err != nil {
		t.Fatalf("restamp upsert: %v", err)
	}
	got, err := repo.GetByID(ctx, contentID)
	if err != nil {
		t.Fatalf("get item: %v", err)
	}
	if got.DefaultMetadataLanguage != "da" {
		t.Errorf("default_metadata_language = %q, want da (incoming non-empty stamp must win)", got.DefaultMetadataLanguage)
	}

	emptied := restamped
	emptied.DefaultMetadataLanguage = ""
	if err := repo.Upsert(ctx, &emptied); err != nil {
		t.Fatalf("empty-language upsert: %v", err)
	}
	got, err = repo.GetByID(ctx, contentID)
	if err != nil {
		t.Fatalf("get item after empty upsert: %v", err)
	}
	if got.DefaultMetadataLanguage != "da" {
		t.Errorf("default_metadata_language = %q, want da kept when incoming is empty", got.DefaultMetadataLanguage)
	}
}

// TestSeasonAndEpisodeUpsertRestampDefaultMetadataLanguage verifies the same
// restamp-on-upsert semantics for the season and episode tables, which carry
// their own default_metadata_language pins.
func TestSeasonAndEpisodeUpsertRestampDefaultMetadataLanguage(t *testing.T) {
	ctx := context.Background()
	pool := newRestampTestPool(t)

	suffix := time.Now().UnixNano()
	seriesID := fmt.Sprintf("restamp-series-%d", suffix)
	seasonID := fmt.Sprintf("restamp-season-%d", suffix)
	episodeID := fmt.Sprintf("restamp-episode-%d", suffix)
	t.Cleanup(func() {
		// Seasons and episodes cascade from the series row (FK ON DELETE
		// CASCADE), so a single delete cleans up everything seeded here.
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, seriesID)
	})

	itemRepo := NewItemRepository(pool)
	if err := itemRepo.Upsert(ctx, &models.MediaItem{
		ContentID:               seriesID,
		Type:                    "series",
		Title:                   "Restamp Series",
		Status:                  "matched",
		DefaultMetadataLanguage: "zh",
		Studios:                 []string{},
		Networks:                []string{},
		Countries:               []string{},
		Genres:                  []string{},
	}); err != nil {
		t.Fatalf("seed series: %v", err)
	}

	seasonRepo := NewSeasonRepository(pool)
	season := &models.Season{
		ContentID:               seasonID,
		SeriesID:                seriesID,
		SeasonNumber:            1,
		Title:                   "Sæson 1",
		DefaultMetadataLanguage: "zh",
	}
	if err := seasonRepo.Upsert(ctx, season); err != nil {
		t.Fatalf("seed season: %v", err)
	}
	season.DefaultMetadataLanguage = "da"
	if err := seasonRepo.Upsert(ctx, season); err != nil {
		t.Fatalf("restamp season: %v", err)
	}
	var seasonLang string
	if err := pool.QueryRow(ctx, `SELECT default_metadata_language FROM seasons WHERE content_id = $1`, seasonID).Scan(&seasonLang); err != nil {
		t.Fatalf("read season language: %v", err)
	}
	if seasonLang != "da" {
		t.Errorf("season default_metadata_language = %q, want da", seasonLang)
	}

	episodeRepo := NewEpisodeRepository(pool)
	episode := &models.Episode{
		ContentID:               episodeID,
		SeriesID:                seriesID,
		SeasonID:                seasonID,
		SeasonNumber:            1,
		EpisodeNumber:           1,
		Title:                   "Afsnit 1",
		DefaultMetadataLanguage: "zh",
	}
	if err := episodeRepo.Upsert(ctx, episode); err != nil {
		t.Fatalf("seed episode: %v", err)
	}
	episode.DefaultMetadataLanguage = "da"
	if err := episodeRepo.Upsert(ctx, episode); err != nil {
		t.Fatalf("restamp episode: %v", err)
	}
	var episodeLang string
	if err := pool.QueryRow(ctx, `SELECT default_metadata_language FROM episodes WHERE content_id = $1`, episodeID).Scan(&episodeLang); err != nil {
		t.Fatalf("read episode language: %v", err)
	}
	if episodeLang != "da" {
		t.Errorf("episode default_metadata_language = %q, want da", episodeLang)
	}
}
