package catalog

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestEpisodeSearchPostgresAndDocumentSource(t *testing.T) {
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

	suffix := time.Now().UnixNano()
	seriesID := fmt.Sprintf("episode-search-series-%d", suffix)
	episodeID := fmt.Sprintf("episode-search-episode-%d", suffix)
	unavailableID := fmt.Sprintf("episode-search-unavailable-%d", suffix)
	podcastID := fmt.Sprintf("episode-search-podcast-%d", suffix)
	podcastEpisodeID := fmt.Sprintf("episode-search-podcast-episode-%d", suffix)
	var folderID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO media_folders (type, name, enabled) VALUES ('series', $1, true) RETURNING id`,
		seriesID,
	).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{seriesID, podcastID})
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})

	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres)
		VALUES ($1, 'series', 'Silo Search Fixture', 'matched', '{}'::text[]),
		       ($2, 'podcast', 'Podcast Search Fixture', 'matched', '{}'::text[])
	`, seriesID, podcastID); err != nil {
		t.Fatalf("seed parent items: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO episodes (content_id, series_id, season_number, episode_number, title, overview)
		VALUES ($1, $2, 3, 1, 'Who Are You?', 'A buried signal returns. A buried signal changes everything.'),
		       ($3, $2, 3, 2, 'Who Are You?', 'Unavailable metadata-only episode.'),
		       ($4, $5, 1, 1, 'Who Are You?', 'Podcast episode with the same title.')
	`, episodeID, seriesID, unavailableID, podcastEpisodeID, podcastID); err != nil {
		t.Fatalf("seed episodes: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO episode_libraries (episode_id, media_folder_id, first_seen_at)
		VALUES ($1, $3, NOW()), ($2, $3, NOW())
	`, episodeID, podcastEpisodeID, folderID); err != nil {
		t.Fatalf("seed episode memberships: %v", err)
	}

	repo := NewItemRepository(pool)
	for _, types := range [][]string{nil, {"episode"}} {
		items, total, _, exact, err := repo.SearchPage(ctx, "Who Are You?", types, 20, 0, AccessFilter{}, true)
		if err != nil {
			t.Fatalf("search types %v: %v", types, err)
		}
		found := false
		for _, item := range items {
			if item.ContentID == episodeID && item.Type == "episode" {
				found = true
			}
			if item.ContentID == unavailableID || item.ContentID == podcastEpisodeID {
				t.Fatalf("search types %v leaked ineligible episode %s", types, item.ContentID)
			}
		}
		if !exact || total < 1 || !found {
			t.Fatalf("search types %v = total %d exact %v items %+v", types, total, exact, items)
		}
	}

	overviewItems, _, _, _, err := repo.SearchPage(ctx, "buried signal", []string{"episode"}, 20, 0, AccessFilter{}, true)
	if err != nil || len(overviewItems) != 1 || overviewItems[0].ContentID != episodeID {
		t.Fatalf("overview search = items %+v err %v", overviewItems, err)
	}

	indexer := NewCatalogSearchIndexer(pool, nil)
	docs, err := indexer.LoadDocumentsByIDs(ctx, []string{episodeID, unavailableID, podcastEpisodeID}, nil, DefaultMeilisearchEmbedder, true, false)
	if err != nil {
		t.Fatalf("load episode documents: %v", err)
	}
	if len(docs) != 1 || docs[0].ContentID != episodeID || docs[0].Type != "episode" {
		t.Fatalf("episode documents = %+v", docs)
	}
	if len(docs[0].LibraryIDs) != 1 || int(docs[0].LibraryIDs[0]) != folderID {
		t.Fatalf("episode library ids = %v, want [%d]", docs[0].LibraryIDs, folderID)
	}
	if docs[0].Vectors != nil {
		t.Fatalf("episode document unexpectedly has vectors: %#v", docs[0].Vectors)
	}
}

func TestEpisodeSearchOutboxStatementTriggers(t *testing.T) {
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
	var captureFunction *string
	if err := pool.QueryRow(ctx, `SELECT to_regprocedure('public.catalog_search_capture_enabled()')::text`).Scan(&captureFunction); err != nil || captureFunction == nil {
		t.Skip("episode search outbox trigger migration is not applied")
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `UPDATE server_settings SET value = 'meilisearch' WHERE key = 'catalog.search.provider'`); err != nil {
		t.Fatalf("enable trigger capture: %v", err)
	}
	suffix := time.Now().UnixNano()
	seriesID := fmt.Sprintf("episode-trigger-series-%d", suffix)
	episodeID := fmt.Sprintf("episode-trigger-episode-%d", suffix)
	var folderID int
	if err := tx.QueryRow(ctx,
		`INSERT INTO media_folders (type, name, enabled) VALUES ('series', $1, true) RETURNING id`, seriesID,
	).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres)
		VALUES ($1, 'series', 'Trigger Series', 'matched', '{}'::text[])
	`, seriesID); err != nil {
		t.Fatalf("seed series: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, seriesID, folderID); err != nil {
		t.Fatalf("seed item membership: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO episodes (content_id, series_id, season_number, episode_number, title)
		VALUES ($1, $2, 1, 1, 'Trigger Episode')
	`, episodeID, seriesID); err != nil {
		t.Fatalf("seed episode: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO episode_libraries (episode_id, media_folder_id) VALUES ($1, $2)`, episodeID, folderID); err != nil {
		t.Fatalf("seed episode membership: %v", err)
	}

	var upserts int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM catalog_search_index_events
		WHERE action = 'upsert' AND content_id = $1
	`, episodeID).Scan(&upserts); err != nil || upserts < 2 {
		t.Fatalf("episode insert/membership upserts = %d, err %v", upserts, err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, seriesID); err != nil {
		t.Fatalf("delete series: %v", err)
	}
	var deletes int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM catalog_search_index_events
		WHERE action = 'delete' AND content_id = $1
	`, episodeID).Scan(&deletes); err != nil || deletes == 0 {
		t.Fatalf("episode cascade deletes = %d, err %v", deletes, err)
	}
}
