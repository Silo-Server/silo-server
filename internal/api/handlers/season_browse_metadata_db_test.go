package handlers

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestListBrowseMetadataFillsSeasonRowsDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	series := fmt.Sprintf("browse-season-series-%d", time.Now().UnixNano())
	season := series + "-s2"
	if _, err := pool.Exec(t.Context(), `INSERT INTO media_items(content_id,type,title,genres) VALUES($1,'series','Alpha','{}')`, series); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id=$1`, series)
	})
	if _, err := pool.Exec(t.Context(), `INSERT INTO seasons(content_id,series_id,season_number,title,overview,poster_path,poster_source_path,poster_thumbhash,metadata_s3_path,metadata_etag,metadata_source)
		VALUES($1,$2,2,'Season 2','','','','','','','')`, season, series); err != nil {
		t.Fatal(err)
	}
	h := &ItemsHandler{
		itemRepo:    catalog.NewItemRepository(pool),
		episodeRepo: catalog.NewEpisodeRepository(pool),
		seasonRepo:  catalog.NewSeasonRepository(pool),
	}
	meta := h.listEpisodeBrowseMetadata(t.Context(), []*models.MediaItem{{ContentID: season, Type: "season"}})
	got, ok := meta[season]
	if !ok || got.SeriesID != series || got.SeriesTitle != "Alpha" || got.SeasonNumber == nil || *got.SeasonNumber != 2 || got.EpisodeNumber != nil {
		t.Fatalf("season metadata = %+v (found %v)", got, ok)
	}
}
