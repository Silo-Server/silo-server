package scanner

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// MaxUnsupportedFileNames caps the file names kept for one directory and
// reason. A Blu-ray STREAM folder can hold hundreds of clips; the file count
// still counts them all.
const MaxUnsupportedFileNames = 100

// GroupUnsupportedFiles groups the files a scan skipped by directory and
// reason, the shape the admin list keeps them in. Groups are ordered by
// directory and reason, and each keeps its files' names in name order, up to
// MaxUnsupportedFileNames.
func GroupUnsupportedFiles(folderID int, files []UnsupportedFile) []models.UnsupportedMediaFileGroup {
	type groupKey struct{ dir, reason string }
	groups := make(map[groupKey]*models.UnsupportedMediaFileGroup)
	for _, file := range files {
		key := groupKey{dir: filepath.Dir(file.Path), reason: file.Reason}
		group := groups[key]
		if group == nil {
			group = &models.UnsupportedMediaFileGroup{
				MediaFolderID: folderID,
				DirectoryPath: key.dir,
				Reason:        key.reason,
			}
			groups[key] = group
		}
		group.FileCount++
		group.FileNames = append(group.FileNames, filepath.Base(file.Path))
	}
	out := make([]models.UnsupportedMediaFileGroup, 0, len(groups))
	for _, group := range groups {
		sort.Strings(group.FileNames)
		if len(group.FileNames) > MaxUnsupportedFileNames {
			group.FileNames = group.FileNames[:MaxUnsupportedFileNames]
		}
		out = append(out, *group)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DirectoryPath != out[j].DirectoryPath {
			return out[i].DirectoryPath < out[j].DirectoryPath
		}
		return out[i].Reason < out[j].Reason
	})
	return out
}

// UnsupportedFileRepository keeps the files library scans skipped because of
// their type, for the admin list.
type UnsupportedFileRepository struct {
	pool *pgxpool.Pool
}

// NewUnsupportedFileRepository creates an UnsupportedFileRepository.
func NewUnsupportedFileRepository(pool *pgxpool.Pool) *UnsupportedFileRepository {
	return &UnsupportedFileRepository{pool: pool}
}

const unsupportedFileColumns = `media_folder_id, directory_path, reason, file_count, file_names, first_seen_at, last_seen_at`

// Replace records what a scan of the given scopes, started at startedAt, found
// in a library. Groups it found are inserted or refreshed; rows under a scope
// that it did not find are deleted, except at or under a protected path, whose
// contents the scan could not see, and rows another scan refreshed after this
// one started: a subtree scan can run alongside a library scan. A nil scopes
// covers the whole library.
func (r *UnsupportedFileRepository) Replace(
	ctx context.Context,
	folderID int,
	scopes []string,
	protectedPaths []string,
	startedAt time.Time,
	groups []models.UnsupportedMediaFileGroup,
) error {
	if scopes != nil {
		scopes = cleanPaths(scopes)
	}
	protectedPaths = cleanPaths(protectedPaths)
	dirs := make([]string, 0, len(groups))
	reasons := make([]string, 0, len(groups))
	upserts := &pgx.Batch{}
	for _, group := range groups {
		dirs = append(dirs, group.DirectoryPath)
		reasons = append(reasons, group.Reason)
		upserts.Queue(`
			INSERT INTO unsupported_media_files (
				media_folder_id, directory_path, reason, file_count, file_names
			)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (media_folder_id, directory_path, reason) DO UPDATE
			SET file_count = EXCLUDED.file_count,
				file_names = EXCLUDED.file_names,
				last_seen_at = now()
		`, folderID, group.DirectoryPath, group.Reason, group.FileCount, group.FileNames)
	}
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			DELETE FROM unsupported_media_files u
			WHERE u.media_folder_id = $1
			  AND ($2::text[] IS NULL OR EXISTS (
				SELECT 1 FROM unnest($2::text[]) AS scope(path)
				WHERE u.directory_path = scope.path OR strpos(u.directory_path, scope.path || '/') = 1
			  ))
			  AND NOT EXISTS (
				SELECT 1 FROM unnest($3::text[]) AS protected(path)
				WHERE u.directory_path = protected.path OR strpos(u.directory_path, protected.path || '/') = 1
			  )
			  AND NOT EXISTS (
				SELECT 1 FROM unnest($4::text[], $5::text[]) AS found(directory_path, reason)
				WHERE found.directory_path = u.directory_path AND found.reason = u.reason
			  )
			  AND u.last_seen_at < $6
		`, folderID, scopes, protectedPaths, dirs, reasons, startedAt); err != nil {
			return fmt.Errorf("deleting stale unsupported files for folder %d: %w", folderID, err)
		}
		if upserts.Len() == 0 {
			return nil
		}
		if err := tx.SendBatch(ctx, upserts).Close(); err != nil {
			return fmt.Errorf("recording unsupported files for folder %d: %w", folderID, err)
		}
		return nil
	})
}

// unsupportedFileSearchWhere is the search ListPage and Count share, so a
// page and its total always agree on which rows match.
const unsupportedFileSearchWhere = `$1 = '' OR strpos(lower(directory_path), lower($1)) > 0
 OR EXISTS (SELECT 1 FROM unnest(file_names) AS name WHERE strpos(lower(name), lower($1)) > 0)
 OR strpos(lower(reason), lower($1)) > 0
 OR media_folder_id IN (SELECT id FROM media_folders WHERE strpos(lower(name), lower($1)) > 0)`

// ListPage answers a page of rows matching the search, newest first by when
// each was first found. Every library scan refreshes last_seen_at, so that
// order would put whatever the last scan touched first.
func (r *UnsupportedFileRepository) ListPage(ctx context.Context, search string, limit, offset int) ([]models.UnsupportedMediaFileGroup, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+unsupportedFileColumns+` FROM unsupported_media_files
 WHERE `+unsupportedFileSearchWhere+`
 ORDER BY first_seen_at DESC, media_folder_id ASC, directory_path ASC, reason ASC LIMIT $2 OFFSET $3`, search, limit, max(offset, 0))
	if err != nil {
		return nil, fmt.Errorf("listing unsupported file page: %w", err)
	}
	defer rows.Close()
	groups := make([]models.UnsupportedMediaFileGroup, 0)
	for rows.Next() {
		var group models.UnsupportedMediaFileGroup
		if err := rows.Scan(
			&group.MediaFolderID,
			&group.DirectoryPath,
			&group.Reason,
			&group.FileCount,
			&group.FileNames,
			&group.FirstSeenAt,
			&group.LastSeenAt,
		); err != nil {
			return nil, fmt.Errorf("scanning unsupported file row: %w", err)
		}
		groups = append(groups, group)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating unsupported file rows: %w", err)
	}
	return groups, nil
}

// Count answers how many rows match the search across every page.
func (r *UnsupportedFileRepository) Count(ctx context.Context, search string) (int, error) {
	var total int
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM unsupported_media_files WHERE `+unsupportedFileSearchWhere, search).Scan(&total); err != nil {
		return 0, fmt.Errorf("counting unsupported files: %w", err)
	}
	return total, nil
}

func cleanPaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		out = append(out, filepath.Clean(path))
	}
	return out
}
