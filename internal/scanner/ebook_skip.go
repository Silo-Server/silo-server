package scanner

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Bound the preload to keep memory and the age of the snapshot independent of
// library size. Each worker still stats its file (and OPF sidecar) before skipping.
const ebookSkipBatchSize = 500

type ebookSkipFile struct {
	contentID       string
	path            string
	size            int64
	modifiedAt      *time.Time
	groupKeyVersion int
	status          string
}

type ebookSkipState map[string][]ebookSkipFile

type ebookScanCandidate struct {
	path      string
	skipState ebookSkipState // nil falls back to the per-file check after a preload failure
}

func (r *FileRepository) loadEbookSkipState(ctx context.Context, folderID int, paths []string) (ebookSkipState, error) {
	rows, err := r.pool.Query(ctx, `
  SELECT mf.observed_root_path, COALESCE(mf.content_id, ''), mf.file_path, mf.file_size,
         mf.file_modified_at, mf.group_key_version, COALESCE(mi.status, '')
  FROM media_files mf
  LEFT JOIN media_items mi ON mi.content_id = mf.content_id
  WHERE mf.media_folder_id = $1 AND mf.observed_root_path = ANY($2)
    AND mf.missing_since IS NULL
 `, folderID, paths)
	if err != nil {
		return nil, fmt.Errorf("preload ebook skip state: %w", err)
	}
	defer rows.Close()
	state := make(ebookSkipState, len(paths))
	for rows.Next() {
		var root string
		var file ebookSkipFile
		if err := rows.Scan(&root, &file.contentID, &file.path, &file.size, &file.modifiedAt, &file.groupKeyVersion, &file.status); err != nil {
			return nil, fmt.Errorf("read ebook skip state: %w", err)
		}
		state[root] = append(state[root], file)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read ebook skip state: %w", err)
	}
	return state, nil
}

func unchangedEbookFile(files []ebookSkipFile, path string, size int64, modifiedAt time.Time) (string, bool) {
	if len(files) != 1 {
		return "", false
	}
	file := files[0]
	if file.path != path || file.size != size || file.modifiedAt == nil || !sameFileModifiedAt(file.modifiedAt, modifiedAt) {
		return "", false
	}
	if file.contentID == "" || file.groupKeyVersion != ebookGroupKeyVersion {
		return "", false
	}
	if strings.EqualFold(strings.TrimSpace(file.status), "unmatched") {
		return "", false
	}
	return file.contentID, true
}
