package scanner

import (
	"context"
	"fmt"
	"log/slog"
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

// scopedScanKey marks a scan of part of a library: a subtree or one file.
type scopedScanKey struct{}

// withScopedScan marks ctx as a scoped scan unless fullScan is set.
func withScopedScan(ctx context.Context, fullScan bool) context.Context {
	if fullScan {
		return ctx
	}
	return context.WithValue(ctx, scopedScanKey{}, true)
}

// confirmUnchangedFiles is called before a scan skips the unchanged files of
// a book at paths. A full scan may be running beside a scoped scan, and its
// walk can have missed a book that was removed and then put back with its old
// size and modification time. Skipping writes nothing, so that scan would
// still mark the book missing. A scoped scan therefore records that it saw
// the rows present (ConfirmPresent), which leaves them to it
// (ListScanPresenceWrittenBefore, MarkMissingWrittenBefore). A full scan
// writes nothing: only one runs per library on a server, and touching every
// unchanged row would add a write per book to each library scan. A scoped
// scan pays one small write per unchanged book it skips.
//
// It reports false when the book must not be skipped: a row was marked
// missing or moved since the caller read it, so the book is indexed again. A
// failed write is logged and also reports false.
func (s *Scanner) confirmUnchangedFiles(ctx context.Context, folderID int, paths []string) bool {
	if scoped, _ := ctx.Value(scopedScanKey{}).(bool); !scoped || s.fileRepo == nil {
		return true
	}
	ok, err := s.fileRepo.ConfirmPresent(ctx, folderID, paths)
	if err != nil {
		slog.WarnContext(ctx, "scanner: confirming unchanged files failed, indexing them again", "component", "scanner",
			"folder_id", folderID, "files", len(paths), "error", err)
		return false
	}
	return ok
}

// ConfirmPresent records that a scan saw the files at paths on disk by
// updating their rows' updated_at, and reports whether every path still had
// a present row in the folder. A row marked missing is left as it is.
func (r *FileRepository) ConfirmPresent(ctx context.Context, folderID int, paths []string) (bool, error) {
	if len(paths) == 0 {
		return true, nil
	}
	tag, err := r.pool.Exec(ctx, `UPDATE media_files SET updated_at = NOW()
		WHERE media_folder_id = $1 AND file_path = ANY($2) AND missing_since IS NULL`, folderID, paths)
	if err != nil {
		return false, fmt.Errorf("confirming present files: %w", err)
	}
	return tag.RowsAffected() == int64(len(paths)), nil
}
