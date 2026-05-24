package audiobooks

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

// ABSMediaStore implements abs.MediaStore using silo's catalog.ItemRepository,
// scanner.FileRepository, and a direct pgxpool.Pool for media_folders queries.
type ABSMediaStore struct {
	Items *catalog.ItemRepository
	Files *scanner.FileRepository
	Pool  *pgxpool.Pool
}

var _ abs.MediaStore = (*ABSMediaStore)(nil)

// GetAudiobookByID returns the media_item with the given content_id, provided
// it is of type 'audiobook'. Returns nil and a wrapped error for any other
// outcome; the caller interprets a nil result as not-found.
func (s *ABSMediaStore) GetAudiobookByID(ctx context.Context, contentID string) (*models.MediaItem, error) {
	item, err := s.Items.GetByID(ctx, contentID)
	if err != nil {
		return nil, fmt.Errorf("abs_media_store: get audiobook %q: %w", contentID, err)
	}
	if item == nil || item.Type != "audiobook" {
		return nil, nil
	}
	return item, nil
}

// ListAudiobooks returns a page of media_items with type='audiobook'.
// When libraryID is non-zero, results are filtered to items in that
// media_folder (via the media_item_libraries junction); 0 means all libraries.
func (s *ABSMediaStore) ListAudiobooks(ctx context.Context, libraryID int64, limit, offset int) ([]*models.MediaItem, int, error) {
	filter := catalog.AccessFilter{}
	if libraryID != 0 {
		filter.AllowedLibraryIDs = []int{int(libraryID)}
	}
	items, total, err := s.Items.Search(ctx, "", []string{"audiobook"}, limit, offset, filter)
	if err != nil {
		return nil, 0, fmt.Errorf("abs_media_store: list audiobooks: %w", err)
	}
	return items, total, nil
}

// GetMediaFiles returns all media_files for the given content_id, ordered by
// file_path so ABS clients receive a stable chapter ordering.
func (s *ABSMediaStore) GetMediaFiles(ctx context.Context, contentID string) ([]*models.MediaFile, error) {
	files, err := s.Files.GetByContentID(ctx, contentID)
	if err != nil {
		return nil, fmt.Errorf("abs_media_store: get media files for %q: %w", contentID, err)
	}
	return files, nil
}

// GetMediaFileByID fetches a single media_file by its integer PK.
func (s *ABSMediaStore) GetMediaFileByID(ctx context.Context, fileID int) (*models.MediaFile, error) {
	file, err := s.Files.GetByID(ctx, fileID)
	if err != nil {
		return nil, fmt.Errorf("abs_media_store: get media file %d: %w", fileID, err)
	}
	return file, nil
}

// ListAudiobookLibraries returns media_folder rows where type='audiobooks'
// (the canonical silo type for the audiobooks sub-plan).
func (s *ABSMediaStore) ListAudiobookLibraries(ctx context.Context) ([]abs.AudiobookLibrary, error) {
	if s.Pool == nil {
		return nil, nil
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id, name, type
		FROM media_folders
		WHERE type IN ('audiobooks', 'audiobook')
		  AND enabled = TRUE
		ORDER BY sort_order, id`)
	if err != nil {
		return nil, fmt.Errorf("abs_media_store: list audiobook libraries: %w", err)
	}
	defer rows.Close()

	var libs []abs.AudiobookLibrary
	for rows.Next() {
		var lib abs.AudiobookLibrary
		if err := rows.Scan(&lib.ID, &lib.Name, &lib.Type); err != nil {
			return nil, fmt.Errorf("abs_media_store: scan library: %w", err)
		}
		libs = append(libs, lib)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("abs_media_store: iterate libraries: %w", err)
	}
	return libs, nil
}
