package metadata

import (
	"context"
	"fmt"
	"strings"
)

// pinnedUnmatchedBySplit reports whether an admin split deliberately left
// contentID unmatched. Split Versions writes an identity override for the
// files it moves; for an unmatched target that override forces no provider
// ID. Split Versions is the only writer of media_identity_overrides (the
// library Root Override feature uses media_root_overrides), so a providerless
// row there means an unmatched split; a new writer must keep that true.
//
// While such an item is still provisional and every one of its present files
// sits under one of those overrides, automatic matching must leave it alone:
// a provider tag in the folder name, or the source's title, would otherwise
// match it straight back to the item it was split from. Identify still works,
// and once the item is matched this no longer applies.
func (s *MetadataService) pinnedUnmatchedBySplit(ctx context.Context, contentID string) (bool, error) {
	contentID = strings.TrimSpace(contentID)
	if s == nil || s.dbPool == nil || contentID == "" {
		return false, nil
	}
	var pinned bool
	if err := s.dbPool.QueryRow(ctx, `
		WITH provisional AS (
			SELECT content_id
			FROM media_items
			WHERE content_id = $1
			  AND lower(trim(status)) IN ('pending', 'unmatched', 'ambiguous')
		),
		present AS (
			SELECT mf.media_folder_id, mf.file_path, mf.observed_root_path
			FROM media_files mf
			JOIN provisional ON provisional.content_id = mf.content_id
			WHERE mf.missing_since IS NULL
		)
		SELECT EXISTS (SELECT 1 FROM present)
		   AND NOT EXISTS (
			SELECT 1
			FROM present
			WHERE NOT EXISTS (
				SELECT 1
				FROM media_identity_overrides o
				WHERE o.media_folder_id = present.media_folder_id
				  AND o.forced_tmdb_id = ''
				  AND o.forced_imdb_id = ''
				  AND o.forced_tvdb_id = ''
				  AND (
					(o.scope = 'root' AND o.root_path = present.observed_root_path) OR
					(o.scope = 'file' AND o.file_path = present.file_path)
				  )
			)
		   )
	`, contentID).Scan(&pinned); err != nil {
		return false, fmt.Errorf("checking split identity pin for %s: %w", contentID, err)
	}
	return pinned, nil
}

// splitPinForFile reports whether an unmatched split's identity override
// covers filePath, and whether that override covers its whole observed root.
// Such a root keeps its provider tag in the folder name, and the source item
// can still hold its ownership claims, so both would resolve new files there
// back to the item it was split from. Only a root-wide pin means every file
// at the root belongs to the split target; a file-scope pin comes from a
// partial split whose root still holds the source's own files.
func (s *MetadataService) splitPinForFile(ctx context.Context, folderID int, observedRootPath, filePath string) (pinned, rootWide bool, err error) {
	if s == nil || s.dbPool == nil || folderID <= 0 {
		return false, false, nil
	}
	var fileScope bool
	if err := s.dbPool.QueryRow(ctx, `
		SELECT
			COALESCE(bool_or(o.scope = 'root'), false),
			COALESCE(bool_or(o.scope = 'file'), false)
		FROM media_identity_overrides o
		WHERE o.media_folder_id = $1
		  AND o.forced_tmdb_id = ''
		  AND o.forced_imdb_id = ''
		  AND o.forced_tvdb_id = ''
		  AND (
			(o.scope = 'root' AND o.root_path = $2 AND $2 <> '') OR
			(o.scope = 'file' AND o.file_path = $3 AND $3 <> '')
		  )
	`, folderID, observedRootPath, filePath).Scan(&rootWide, &fileScope); err != nil {
		return false, false, fmt.Errorf("checking split identity pin for %s: %w", observedRootPath, err)
	}
	return rootWide || fileScope, rootWide, nil
}
