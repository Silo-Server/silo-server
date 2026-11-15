package scanner

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/pathscope"
)

// scanPresence is the part of a cataloged file's row a full scan needs to
// reconcile files that are gone from disk. A full scan lists it for every file
// under a root before walking, so it stays this small; the rest of a file's
// scan state is read a batch at a time (GetScanStateByFolderAndPaths).
type scanPresence struct {
	ID           int
	FilePath     string
	MissingSince *time.Time
}

// ListScanPresenceByFolderAndPathPrefix returns the presence rows of a folder
// subtree: the same files GetScanStateByFolderAndPathPrefix returns, in the
// same order.
func (r *FileRepository) ListScanPresenceByFolderAndPathPrefix(ctx context.Context, folderID int, pathPrefix string) ([]scanPresence, error) {
	query, args := folderPathPrefixQuery(`id, file_path, missing_since`, folderID, pathPrefix)
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying scan presence by folder and path prefix: %w", err)
	}
	return scanPresenceRows(rows)
}

// scanInventoryTime returns the database time for a scan to take before it
// walks. Its missing-file pass lists only rows written before then
// (ListScanPresenceWrittenBefore): a row another scan wrote since is a file
// this walk may have missed, and stays that scan's to reconcile.
func (s *Scanner) scanInventoryTime(ctx context.Context) (time.Time, error) {
	var at time.Time
	if s.fileRepo == nil {
		return at, nil
	}
	if err := s.fileRepo.Pool().QueryRow(ctx, `SELECT now()`).Scan(&at); err != nil {
		return at, fmt.Errorf("reading database time: %w", err)
	}
	return at, nil
}

// ListScanPresenceWrittenBefore returns the presence rows of a folder subtree
// that no write has touched at or after before. A scan passes the database
// time it began walking, so rows another scan wrote since stay that scan's.
func (r *FileRepository) ListScanPresenceWrittenBefore(ctx context.Context, folderID int, pathPrefix string, before time.Time) ([]scanPresence, error) {
	clauses, args := pathscope.RangeCoverageClauses("file_path", []string{pathPrefix}, 3)
	query := `SELECT id, file_path, missing_since FROM media_files
		WHERE media_folder_id = $1 AND updated_at < $2 AND (` + strings.Join(clauses, " OR ") + `)
		ORDER BY file_path ASC`
	rows, err := r.pool.Query(ctx, query, append([]any{folderID, before}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("querying scan presence written before %s: %w", before, err)
	}
	return scanPresenceRows(rows)
}

func scanPresenceRows(rows pgx.Rows) ([]scanPresence, error) {
	defer rows.Close()
	files := make([]scanPresence, 0)
	for rows.Next() {
		var file scanPresence
		if err := rows.Scan(&file.ID, &file.FilePath, &file.MissingSince); err != nil {
			return nil, fmt.Errorf("scanning scan presence row: %w", err)
		}
		files = append(files, file)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating scan presence rows: %w", err)
	}
	return files, nil
}

// GetScanStateByFolderAndPaths returns the lightweight scan-state rows of the
// folder's files at exactly the given paths. Paths with no row are left out.
func (r *FileRepository) GetScanStateByFolderAndPaths(ctx context.Context, folderID int, paths []string) ([]*scanStateFile, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	query := `SELECT ` + scanStateColumns + ` FROM media_files
		WHERE media_folder_id = $1 AND file_path = ANY($2::text[])`
	rows, err := r.pool.Query(ctx, query, folderID, paths)
	if err != nil {
		return nil, fmt.Errorf("querying scan state by folder and paths: %w", err)
	}
	return scanScanStateRows(rows)
}

// scanPresenceOf returns the presence part of scan-state rows.
func scanPresenceOf(files []*scanStateFile) []scanPresence {
	presence := make([]scanPresence, 0, len(files))
	for _, file := range files {
		presence = append(presence, scanPresence{ID: file.ID, FilePath: file.FilePath, MissingSince: file.MissingSince})
	}
	return presence
}

// unseenScanPresence returns the cataloged files a walk did not see, in their
// listed order.
func unseenScanPresence(existing []scanPresence, walkedPaths []string) []scanPresence {
	seen := make(map[string]struct{}, len(walkedPaths))
	for _, path := range walkedPaths {
		seen[path] = struct{}{}
	}
	unseen := make([]scanPresence, 0)
	for _, file := range existing {
		if _, ok := seen[file.FilePath]; !ok {
			unseen = append(unseen, file)
		}
	}
	return unseen
}
