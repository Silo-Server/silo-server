// Package filesplit moves a subset of a catalog item's files to an existing
// item while preserving file-attributable user state. It is shared by the
// admin Split Versions flow and automatic scanner identity repair.
package filesplit

import (
	"context"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/catalog/reattribute"
	"github.com/Silo-Server/silo-server/internal/contentid"
)

const (
	itemTypeMovie  = "movie"
	itemTypeSeries = "series"
)

// File is the media-file identity needed for a subset move.
type File struct {
	ID                int
	ContentID         string
	MediaFolderID     int
	FilePath          string
	CanonicalRootPath string
	ObservedRootPath  string
	GroupKeyVersion   int
	ContentGroupKey   string
	BaseType          string
	SeasonNumber      int
	EpisodeNumber     int
	EpisodeID         string
}

// Options describes one transactional file-subset move.
type Options struct {
	FromContentID string
	ToContentID   string
	ItemType      string
	Files         []File
	HistoryMode   reattribute.HistoryMode
}

// Result reports the state reattribution performed by Move.
type Result struct {
	EpisodePairs  []reattribute.IDPair
	Reattribution *reattribute.Report
}

// Move reassigns files and file-attributable user state inside the caller's
// transaction. The source predicate makes stale/concurrent selections fail
// closed instead of moving an unexpected row.
func Move(ctx context.Context, tx pgx.Tx, opts Options) (*Result, error) {
	if opts.FromContentID == "" || opts.ToContentID == "" || opts.FromContentID == opts.ToContentID {
		return nil, fmt.Errorf("filesplit: from/to content ids required and must differ")
	}
	if opts.ItemType != itemTypeMovie && opts.ItemType != itemTypeSeries {
		return nil, fmt.Errorf("filesplit: unsupported item type %q", opts.ItemType)
	}
	if len(opts.Files) == 0 {
		return nil, fmt.Errorf("filesplit: at least one file is required")
	}
	if opts.HistoryMode == "" {
		opts.HistoryMode = reattribute.HistoryModeEvidence
	}
	if !reattribute.ValidHistoryMode(opts.HistoryMode) {
		return nil, fmt.Errorf("filesplit: invalid history mode %q", opts.HistoryMode)
	}

	fileIDs := distinctFileIDs(opts.Files)
	if len(fileIDs) != len(opts.Files) {
		return nil, fmt.Errorf("filesplit: file ids must be positive and unique")
	}
	tag, err := tx.Exec(ctx, `
		UPDATE media_files
		SET content_id = $1,
			episode_id = NULL,
			match_attempted_at = NULL,
			updated_at = NOW()
		WHERE content_id = $2
		  AND id = ANY($3::int[])
	`, opts.ToContentID, opts.FromContentID, fileIDs)
	if err != nil {
		return nil, fmt.Errorf("filesplit: moving files: %w", err)
	}
	if tag.RowsAffected() != int64(len(fileIDs)) {
		return nil, fmt.Errorf(
			"filesplit: moved %d of %d selected files; source ownership changed",
			tag.RowsAffected(),
			len(fileIDs),
		)
	}

	for _, folderID := range distinctFolderIDs(opts.Files) {
		if _, err := tx.Exec(ctx, `
			INSERT INTO media_item_libraries (content_id, media_folder_id)
			VALUES ($1, $2)
			ON CONFLICT DO NOTHING
		`, opts.ToContentID, folderID); err != nil {
			return nil, fmt.Errorf("filesplit: adding target library membership: %w", err)
		}
	}

	derivedEpisodePairs := DeriveEpisodePairs(opts.ItemType, opts.Files, opts.ToContentID)
	episodePairs, err := keepFullyMovedEpisodePairs(ctx, tx, derivedEpisodePairs)
	if err != nil {
		return nil, err
	}
	report, err := reattribute.Run(ctx, tx, reattribute.Options{
		FromContentID: opts.FromContentID,
		ToContentID:   opts.ToContentID,
		MovedFileIDs:  fileIDs,
		Mode:          opts.HistoryMode,
		EpisodePairs:  episodePairs,
	})
	if err != nil {
		return nil, fmt.Errorf("filesplit: reattributing user state: %w", err)
	}

	// Existing matched series already have deterministic target episode rows.
	// Relink immediately when they are available; a later metadata refresh can
	// fill any episode that does not exist yet.
	if opts.ItemType == itemTypeSeries {
		if _, err := tx.Exec(ctx, `
			UPDATE media_files mf
			SET episode_id = e.content_id,
				updated_at = NOW()
			FROM episodes e
			WHERE mf.id = ANY($1::int[])
			  AND e.series_id = $2
			  AND e.season_number = mf.season_number
			  AND e.episode_number = mf.episode_number
		`, fileIDs, opts.ToContentID); err != nil {
			return nil, fmt.Errorf("filesplit: relinking target episodes: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO episode_libraries (episode_id, media_folder_id, first_seen_at)
			SELECT mf.episode_id, mf.media_folder_id, MIN(mf.created_at)
			FROM media_files mf
			WHERE mf.id = ANY($1::int[])
			  AND mf.episode_id IS NOT NULL
			GROUP BY mf.episode_id, mf.media_folder_id
			ON CONFLICT (episode_id, media_folder_id) DO NOTHING
		`, fileIDs); err != nil {
			return nil, fmt.Errorf("filesplit: adding target episode library membership: %w", err)
		}
		if len(derivedEpisodePairs) > 0 {
			if _, err := tx.Exec(ctx, `
				DELETE FROM episode_libraries source_membership
				WHERE source_membership.episode_id = ANY($1::text[])
				  AND NOT EXISTS (
					SELECT 1
					FROM media_files remaining
					WHERE remaining.episode_id = source_membership.episode_id
					  AND remaining.media_folder_id = source_membership.media_folder_id
					  AND remaining.missing_since IS NULL
				  )
			`, episodePairSources(derivedEpisodePairs)); err != nil {
				return nil, fmt.Errorf("filesplit: removing stale source episode library membership: %w", err)
			}
		}
		if err := catalog.RecomputeSeriesLatestEpisodeAdded(
			ctx,
			tx,
			[]string{opts.FromContentID, opts.ToContentID},
		); err != nil {
			return nil, fmt.Errorf("filesplit: %w", err)
		}
	}

	return &Result{
		EpisodePairs:  episodePairs,
		Reattribution: report,
	}, nil
}

// DeriveEpisodePairs maps moved files' current episode ids onto deterministic
// episode ids under the target series.
func DeriveEpisodePairs(itemType string, files []File, targetContentID string) []reattribute.IDPair {
	if itemType != itemTypeSeries || !contentid.IsProviderAnchored(targetContentID) {
		return nil
	}
	seen := make(map[string]bool)
	pairs := make([]reattribute.IDPair, 0)
	for _, file := range files {
		if file.EpisodeID == "" || file.SeasonNumber <= 0 || file.EpisodeNumber <= 0 || seen[file.EpisodeID] {
			continue
		}
		newID, ok := contentid.ForEpisode(targetContentID, file.SeasonNumber, file.EpisodeNumber)
		if !ok || newID == file.EpisodeID {
			continue
		}
		seen[file.EpisodeID] = true
		pairs = append(pairs, reattribute.IDPair{From: file.EpisodeID, To: newID})
	}
	return pairs
}

// keepFullyMovedEpisodePairs excludes a source episode when another active
// file still points at it after the selected files were detached. Episode
// progress has no file dimension, so moving a shared episode's state would
// steal progress from the source show.
func keepFullyMovedEpisodePairs(
	ctx context.Context,
	tx pgx.Tx,
	pairs []reattribute.IDPair,
) ([]reattribute.IDPair, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	fromIDs := make([]string, 0, len(pairs))
	toIDs := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		fromIDs = append(fromIDs, pair.From)
		toIDs = append(toIDs, pair.To)
	}

	rows, err := tx.Query(ctx, `
		SELECT pair.from_id, pair.to_id
		FROM UNNEST($1::text[], $2::text[]) AS pair(from_id, to_id)
		WHERE NOT EXISTS (
			SELECT 1
			FROM media_files remaining
			WHERE remaining.episode_id = pair.from_id
			  AND remaining.missing_since IS NULL
		)
	`, fromIDs, toIDs)
	if err != nil {
		return nil, fmt.Errorf("filesplit: filtering shared episode state: %w", err)
	}
	defer rows.Close()

	filtered := make([]reattribute.IDPair, 0, len(pairs))
	for rows.Next() {
		var pair reattribute.IDPair
		if err := rows.Scan(&pair.From, &pair.To); err != nil {
			return nil, fmt.Errorf("filesplit: scanning movable episode state: %w", err)
		}
		filtered = append(filtered, pair)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("filesplit: iterating movable episode state: %w", err)
	}
	return filtered, nil
}

func episodePairSources(pairs []reattribute.IDPair) []string {
	ids := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		ids = append(ids, pair.From)
	}
	return ids
}

func distinctFileIDs(files []File) []int {
	seen := make(map[int]bool, len(files))
	ids := make([]int, 0, len(files))
	for _, file := range files {
		if file.ID > 0 && !seen[file.ID] {
			seen[file.ID] = true
			ids = append(ids, file.ID)
		}
	}
	sort.Ints(ids)
	return ids
}

func distinctFolderIDs(files []File) []int {
	seen := make(map[int]bool, len(files))
	ids := make([]int, 0)
	for _, file := range files {
		if file.MediaFolderID > 0 && !seen[file.MediaFolderID] {
			seen[file.MediaFolderID] = true
			ids = append(ids, file.MediaFolderID)
		}
	}
	sort.Ints(ids)
	return ids
}
