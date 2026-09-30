package metadata

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestPinnedUnmatchedBySplit covers the rule that keeps automatic matching
// from folding an unmatched split target back into the item it came from.
func TestPinnedUnmatchedBySplit(t *testing.T) {
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
	var folderID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO media_folders (type, name, enabled) VALUES ('series', $1, true) RETURNING id`,
		fmt.Sprintf("Split pin %d", suffix),
	).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	contentID := fmt.Sprintf("local-split-pin-%d", suffix)
	root := fmt.Sprintf("/split-pin-%d/Show (2025) {tvdb-1}", suffix)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres, poster_path, backdrop_path, logo_path)
		VALUES ($1, 'series', 'Show', 'unmatched', '{}'::text[], '', '', '')
	`, contentID); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	for _, name := range []string{"S01E01.mkv", "S01E02.mkv"} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_files (content_id, media_folder_id, file_path, observed_root_path, file_size)
			VALUES ($1, $2, $3, $4, 1024)
		`, contentID, folderID, root+"/Season 01/"+name, root); err != nil {
			t.Fatalf("seed file: %v", err)
		}
	}
	svc := &MetadataService{dbPool: pool}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	expect := func(step string, want bool) {
		t.Helper()
		got, err := svc.pinnedUnmatchedBySplit(ctx, contentID)
		if err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		if got != want {
			t.Fatalf("%s: pinned = %v, want %v", step, got, want)
		}
	}

	expect("no override", false)

	exec(`INSERT INTO media_identity_overrides (media_folder_id, scope, root_path, forced_type, forced_title)
		VALUES ($1, 'root', $2, 'series', 'Show split')`, folderID, root)
	expect("root override without provider IDs", true)

	result, err := svc.Process(ctx, ProcessRequest{ContentID: contentID, Mode: ModeInitialMatch})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result == nil || !result.Pinned || result.Updated || result.ContentID != contentID {
		t.Fatalf("Process result = %+v, want pinned and not updated", result)
	}

	exec(`INSERT INTO media_files (content_id, media_folder_id, file_path, observed_root_path, file_size)
		VALUES ($1, $2, $3, $4, 1024)`, contentID, folderID, fmt.Sprintf("/split-pin-%d/elsewhere/S01E03.mkv", suffix), fmt.Sprintf("/split-pin-%d/elsewhere", suffix))
	expect("a present file outside the override", false)
	exec(`UPDATE media_files SET missing_since = NOW() WHERE content_id = $1 AND file_path LIKE '%/elsewhere/%'`, contentID)
	expect("the uncovered file went missing", true)

	exec(`UPDATE media_identity_overrides SET forced_tvdb_id = '1' WHERE media_folder_id = $1`, folderID)
	expect("override that forces a provider ID", false)
	exec(`UPDATE media_identity_overrides SET forced_tvdb_id = '' WHERE media_folder_id = $1`, folderID)

	exec(`UPDATE media_items SET status = 'matched' WHERE content_id = $1`, contentID)
	expect("identified item", false)
}

// A new file under a split root must join the unmatched target already at
// that root. The folder's provider tag and the source's root claim both point
// back at the source, which would pull the whole root back with it.
func TestCreateOrFindSkeletonKeepsNewFileWithUnmatchedSplitTarget(t *testing.T) {
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

	nonce := time.Now().UnixNano() % 100_000_000
	tvdbID := fmt.Sprintf("%d", 900_000_000+nonce)
	libraryRoot := fmt.Sprintf("/split-skeleton-%d/tv", nonce)
	root := libraryRoot + "/Show (2025) {tvdb-" + tvdbID + "}"
	source, target := "series-tvdb-"+tvdbID, fmt.Sprintf("local-split-skeleton-%d", nonce)
	var folderID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO media_folders (type, name, enabled) VALUES ('series', $1, true) RETURNING id`,
		fmt.Sprintf("Split skeleton %d", nonce),
	).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{source, target})
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	exec(`INSERT INTO media_items (content_id, type, title, year, status, tvdb_id, genres, poster_path, backdrop_path, logo_path)
		VALUES ($1, 'series', 'Show', 2025, 'matched', $2, '{}'::text[], '', '', ''),
		       ($3, 'series', 'Show', 2025, 'unmatched', '', '{}'::text[], '', '', '')`, source, tvdbID, target)
	exec(`INSERT INTO media_item_roots (media_folder_id, canonical_root_path, content_id) VALUES ($1, $2, $3)`, folderID, root, source)
	exec(`INSERT INTO media_identity_overrides (media_folder_id, scope, root_path, forced_type, forced_title, forced_year)
		VALUES ($1, 'root', $2, 'series', 'Show', 2025)`, folderID, root)
	insertFile := func(name, contentID string, episode int) int {
		t.Helper()
		var id int
		var link any
		if contentID != "" {
			link = contentID
		}
		if err := pool.QueryRow(ctx, `
			INSERT INTO media_files (content_id, media_folder_id, file_path, observed_root_path, canonical_root_path,
			                         base_type, base_title, base_year, season_number, episode_number, file_size)
			VALUES ($1, $2, $3, $4, $4, 'series', 'Show', 2025, 1, $5, 1024)
			RETURNING id
		`, link, folderID, root+"/Season 01/"+name, root, episode).Scan(&id); err != nil {
			t.Fatalf("seed file: %v", err)
		}
		return id
	}
	insertFile("Show (2025) - S01E01.mkv", target, 1)
	newFileID := insertFile("Show (2025) - S01E02.mkv", "", 2)

	service := NewMetadataService(nil, nil, nil,
		catalog.NewItemRepository(pool), catalog.NewProviderIDRepository(pool),
		catalog.NewEpisodeRepository(pool), catalog.NewSeasonRepository(pool),
		catalog.NewLibraryItemRepository(pool), catalog.NewFolderRepository(pool),
		nil, scanner.NewFileRepository(pool), NewSkippedRootRepository(pool), nil, catalog.NewRootClaimRepository(pool))
	skeleton, err := service.createOrFindSkeleton(ctx, &models.MediaFile{
		ID:                newFileID,
		MediaFolderID:     folderID,
		FilePath:          root + "/Season 01/Show (2025) - S01E02.mkv",
		ObservedRootPath:  root,
		CanonicalRootPath: root,
		BaseType:          "series",
		BaseTitle:         "Show",
		BaseYear:          2025,
		SeasonNumber:      1,
		EpisodeNumber:     2,
	}, folderID, libraryRoot)
	if err != nil {
		t.Fatalf("createOrFindSkeleton: %v", err)
	}
	if skeleton == nil || skeleton.ContentID != target {
		t.Fatalf("new file resolved to %+v, want the split target %s", skeleton, target)
	}
	var linked string
	if err := pool.QueryRow(ctx, `SELECT content_id FROM media_files WHERE id = $1`, newFileID).Scan(&linked); err != nil {
		t.Fatalf("read new file link: %v", err)
	}
	if linked != target {
		t.Fatalf("new file linked to %s, want %s", linked, target)
	}
}
