package catalogseed

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// A seed carries a season's and episode's locks with their values: without
// them the edits it exports would be overwritten by the receiver's next refresh.
// It also carries the title details only an admin edits.
func TestSeedCarriesSeasonEpisodeLocksDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	seriesID := fmt.Sprintf("seed-locks-%d", time.Now().UnixNano())
	var folderID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name) VALUES ('series', $1) RETURNING id`, seriesID).Scan(&folderID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM media_items WHERE content_id = $1`, seriesID)
		_, _ = pool.Exec(bg, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	if err := catalog.NewItemRepository(pool).Upsert(ctx, &models.MediaItem{ContentID: seriesID, Type: "series", Title: "Show", Status: "matched"}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, seriesID, folderID); err != nil {
		t.Fatal(err)
	}
	seasons, episodes := catalog.NewSeasonRepository(pool), catalog.NewEpisodeRepository(pool)
	if err := seasons.Upsert(ctx, &models.Season{ContentID: seriesID + "-s1", SeriesID: seriesID, SeasonNumber: 1, Title: "Season"}); err != nil {
		t.Fatal(err)
	}
	if err := episodes.Upsert(ctx, &models.Episode{ContentID: seriesID + "-e1", SeriesID: seriesID, SeasonNumber: 1, EpisodeNumber: 1, Title: "Episode"}); err != nil {
		t.Fatal(err)
	}
	locks := []int{0, 1}
	tagline, tvdbID := "Edited tagline", "1234567"
	if err := seasons.UpdateMetadata(ctx, seriesID+"-s1", &catalog.MetadataUpdate{LockedFields: &locks, Tagline: &tagline, TvdbID: &tvdbID}); err != nil {
		t.Fatal(err)
	}
	if err := episodes.UpdateMetadata(ctx, seriesID+"-e1", &catalog.MetadataUpdate{LockedFields: &locks, Tagline: &tagline}); err != nil {
		t.Fatal(err)
	}

	service := NewService(pool, catalog.NewPersonRepository(pool), nil)
	var bundle Bundle
	bundle.Manifest.FormatVersion = CurrentBundleVersion
	if err := service.streamSeasonRecords(ctx, []int{folderID}, func(record SeasonRecord) error {
		bundle.Seasons = append(bundle.Seasons, record)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.streamEpisodeRecords(ctx, []int{folderID}, func(record EpisodeRecord) error {
		bundle.Episodes = append(bundle.Episodes, record)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(bundle.Seasons) != 1 || !slices.Equal(bundle.Seasons[0].LockedFields, locks) ||
		bundle.Seasons[0].Tagline != tagline || bundle.Seasons[0].TvdbID != tvdbID {
		t.Fatalf("exported seasons = %+v, want one with locks %v and the edited title details", bundle.Seasons, locks)
	}
	if len(bundle.Episodes) != 1 || !slices.Equal(bundle.Episodes[0].LockedFields, locks) || bundle.Episodes[0].Tagline != tagline {
		t.Fatalf("exported episodes = %+v, want one with locks %v and the edited tagline", bundle.Episodes, locks)
	}

	// An overwrite takes the bundle's row, locks and title details included.
	bundle.Seasons[0].LockedFields = []int{1}
	bundle.Seasons[0].SortTitle = "Imported sort title"
	bundle.Episodes[0].LockedFields = nil // A bundle from before these locks has none.
	bundle.Episodes[0].OriginalTitle = "Imported original title"
	data, err := encodeBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Import(ctx, data, ImportOptions{ConflictMode: ConflictModeOverwrite}); err != nil {
		t.Fatal(err)
	}
	season, err := seasons.GetByID(ctx, seriesID+"-s1")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(season.LockedFields, []int{1}) {
		t.Errorf("imported season locks = %v, want [1]", season.LockedFields)
	}
	if season.SortTitle != "Imported sort title" || season.Tagline != tagline || season.TvdbID != tvdbID {
		t.Errorf("imported season title details = %q, %q, %q, want the bundle's", season.SortTitle, season.Tagline, season.TvdbID)
	}
	episode, err := episodes.GetByID(ctx, seriesID+"-e1")
	if err != nil {
		t.Fatal(err)
	}
	if len(episode.LockedFields) != 0 {
		t.Errorf("imported episode locks = %v, want none", episode.LockedFields)
	}
	if episode.OriginalTitle != "Imported original title" || episode.Tagline != tagline {
		t.Errorf("imported episode title details = %q, %q, want the bundle's", episode.OriginalTitle, episode.Tagline)
	}
}
