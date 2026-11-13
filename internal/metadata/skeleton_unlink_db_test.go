package metadata

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

// interveningFolderRepo reports the folder disabled on the first check after
// the file was linked, after running intervene, so createOrFindSkeleton takes
// its undo path with another writer's change already committed.
type interveningFolderRepo struct {
	folders   *catalog.FolderRepository
	pool      *pgxpool.Pool
	fileID    int
	intervene func(ctx context.Context, linked string) error
	linked    string
}

func (r *interveningFolderRepo) GetByID(ctx context.Context, id int) (*models.MediaFolder, error) {
	folder, err := r.folders.GetByID(ctx, id)
	if err != nil || folder == nil || r.linked != "" {
		return folder, err
	}
	if err := r.pool.QueryRow(ctx, `SELECT COALESCE(content_id, '') FROM media_files WHERE id = $1`, r.fileID).Scan(&r.linked); err != nil {
		return nil, err
	}
	if r.linked == "" {
		return folder, nil
	}
	if err := r.intervene(ctx, r.linked); err != nil {
		return nil, err
	}
	disabled := *folder
	disabled.Enabled = false
	return &disabled, nil
}

// TestSkeletonUndoFollowsTheLinkDB checks the undo createOrFindSkeleton runs
// when a step after its skeleton link fails: it clears exactly the link this
// call made, even after a scan moved the row with its links (a same-title
// rename), and leaves a link another writer put on the row in its place.
func TestSkeletonUndoFollowsTheLinkDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	cases := []struct {
		name string
		// intervene is the other writer's change between link and undo; it
		// returns the content_id the row must end with.
		intervene func(ctx context.Context, fileID int, linked string) (string, error)
	}{
		{"same-title move keeps the link and changes the path", func(ctx context.Context, fileID int, _ string) (string, error) {
			_, err := pool.Exec(ctx, `UPDATE media_files SET file_path = file_path || '.moved.mkv' WHERE id = $1`, fileID)
			return "", err
		}},
		{"another writer's link stays", func(ctx context.Context, fileID int, _ string) (string, error) {
			other := fmt.Sprintf("movie-tmdb-undo-%d", time.Now().UnixNano())
			if _, err := pool.Exec(ctx, `INSERT INTO media_items (content_id, type, title) VALUES ($1, 'movie', 'Other')`, other); err != nil {
				return "", err
			}
			t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, other) })
			_, err := pool.Exec(ctx, `UPDATE media_files SET content_id = $2 WHERE id = $1`, fileID, other)
			return other, err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			var folderID int
			if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`,
				fmt.Sprintf("skeleton-undo-%d", time.Now().UnixNano())).Scan(&folderID); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = pool.Exec(context.Background(), `DELETE FROM media_files WHERE media_folder_id = $1`, folderID)
				_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, folderID)
			})
			files := scanner.NewFileRepository(pool)
			file, err := files.Upsert(ctx, models.MediaFile{
				MediaFolderID: folderID,
				FilePath:      fmt.Sprintf("/library-%d/Undo Movie (2020)/Undo Movie (2020).mkv", folderID),
				FileSize:      1024,
				BaseTitle:     "Undo Movie",
				BaseYear:      2020,
				BaseType:      "movie",
			})
			if err != nil {
				t.Fatal(err)
			}
			items := catalog.NewItemRepository(pool)
			s := NewMetadataService(nil, nil, nil, items, catalog.NewProviderIDRepository(pool),
				catalog.NewEpisodeRepository(pool), catalog.NewSeasonRepository(pool),
				catalog.NewLibraryItemRepository(pool), catalog.NewFolderRepository(pool),
				nil, files, NewSkippedRootRepository(pool), nil, catalog.NewRootClaimRepository(pool))
			var want string
			folders := &interveningFolderRepo{folders: catalog.NewFolderRepository(pool), pool: pool, fileID: file.ID}
			folders.intervene = func(ctx context.Context, linked string) (err error) {
				want, err = tc.intervene(ctx, file.ID, linked)
				return err
			}
			s.folderRepo = folders

			if _, err := s.createOrFindSkeleton(ctx, file, folderID); err == nil {
				t.Fatal("createOrFindSkeleton succeeded; want the disabled-folder failure")
			}
			if folders.linked == "" {
				t.Fatal("the file was never linked to a skeleton")
			}
			var got string
			if err := pool.QueryRow(ctx, `SELECT COALESCE(content_id, '') FROM media_files WHERE id = $1`, file.ID).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("file content_id after the undo = %q, want %q", got, want)
			}
			if item, err := items.GetByID(ctx, folders.linked); err == nil && item != nil {
				t.Fatalf("skeleton %s outlived its undone link", folders.linked)
			}
		})
	}
}
