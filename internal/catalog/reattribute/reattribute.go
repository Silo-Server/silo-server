// Package reattribute moves per-user state (watch progress, history,
// downloads, favorites, ...) between content ids when an item is split or
// merged. It is the shared engine behind the admin split/merge endpoints and
// the metadata merge path, so both directions apply identical rules:
//
//   - Rows carrying a media_file_id are moved exactly: the file's plays belong
//     to whichever item the file now belongs to.
//   - user_watch_history has no file reference; rows are classified per
//     (user, profile) against the per-session playback_history_admin log —
//     moved when every recorded session for the source item is on moved files,
//     kept when none are, ambiguous otherwise (including when no session
//     evidence exists). HistoryMode controls what happens to ambiguous rows.
//   - Item-level intent rows (favorites, watchlist, ratings, collections,
//     dismissals) carry no signal about which title the user meant; they stay
//     on the source item unless the whole item is moving (merge) or the
//     operator chose HistoryModeMoveAll.
//
// All statements run on the caller's transaction: callers preview a split by
// running Run and rolling back.
package reattribute

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// HistoryMode selects how user_watch_history rows without decisive per-file
// evidence are handled during a file-subset move.
type HistoryMode string

const (
	// HistoryModeEvidence moves rows whose session evidence is unanimous for
	// the moved files and leaves ambiguous rows on the source item.
	HistoryModeEvidence HistoryMode = "evidence"
	// HistoryModeKeep leaves every history row on the source item (exact
	// per-file tables still move).
	HistoryModeKeep HistoryMode = "keep"
	// HistoryModeMoveAll moves every history row and the item-level intent
	// rows — for the "this item was always the other title" case.
	HistoryModeMoveAll HistoryMode = "move_all"
)

// ValidHistoryMode reports whether mode is a recognized HistoryMode.
func ValidHistoryMode(mode HistoryMode) bool {
	switch mode {
	case HistoryModeEvidence, HistoryModeKeep, HistoryModeMoveAll:
		return true
	}
	return false
}

// IDPair maps a source content id to its destination.
type IDPair struct {
	From string
	To   string
}

// Options describes one reattribution run.
type Options struct {
	// FromContentID / ToContentID are the item-level ids (movie or series).
	FromContentID string
	ToContentID   string
	// MovedFileIDs are the media_files ids moving From→To. Ignored when
	// WholeItem is set.
	MovedFileIDs []int
	// WholeItem moves all state regardless of files (the merge case).
	WholeItem bool
	// Mode controls history/intent handling in file-subset mode.
	Mode HistoryMode
	// EpisodePairs maps old→new episode content ids for series splits and
	// merges. Episode-level state always moves wholesale per pair: an episode
	// either moved or it did not.
	EpisodePairs []IDPair
}

// Report tallies what moved. Counts are rows updated per table family.
type Report struct {
	PlaybackSessionLog int `json:"playback_session_log"`
	Downloads          int `json:"downloads"`
	ProgressMoved      int `json:"progress_moved"`
	ProgressConflicts  int `json:"progress_conflicts"`
	HistoryMoved       int `json:"history_moved"`
	HistoryStayed      int `json:"history_stayed"`
	HistoryAmbiguous   int `json:"history_ambiguous"`
	IntentMoved        int `json:"intent_moved"`
	EpisodePairsMoved  int `json:"episode_pairs_moved"`

	// AmbiguousHistory samples ambiguous rows (capped) so a dry-run UI can
	// show the operator what the evidence could not decide.
	AmbiguousHistory []AmbiguousHistoryRow `json:"ambiguous_history,omitempty"`
}

// AmbiguousHistoryRow identifies one history row left behind for lack of
// decisive evidence.
type AmbiguousHistoryRow struct {
	UserID    int    `json:"user_id"`
	ProfileID string `json:"profile_id"`
	WatchedAt string `json:"watched_at"`
}

const ambiguousHistorySampleCap = 100

// Run applies the reattribution on the caller's transaction.
func Run(ctx context.Context, tx pgx.Tx, opts Options) (*Report, error) {
	if opts.FromContentID == "" || opts.ToContentID == "" || opts.FromContentID == opts.ToContentID {
		return nil, fmt.Errorf("reattribute: from/to content ids required and must differ")
	}
	if opts.Mode == "" {
		opts.Mode = HistoryModeEvidence
	}
	if !ValidHistoryMode(opts.Mode) {
		return nil, fmt.Errorf("reattribute: invalid history mode %q", opts.Mode)
	}

	report := &Report{}

	if opts.WholeItem {
		if err := movePairs(ctx, tx, []IDPair{{From: opts.FromContentID, To: opts.ToContentID}}, report); err != nil {
			return nil, err
		}
	} else {
		if err := moveFileSubset(ctx, tx, opts, report); err != nil {
			return nil, err
		}
	}

	if len(opts.EpisodePairs) > 0 {
		episodeReport := &Report{}
		if err := movePairs(ctx, tx, opts.EpisodePairs, episodeReport); err != nil {
			return nil, err
		}
		report.PlaybackSessionLog += episodeReport.PlaybackSessionLog
		report.Downloads += episodeReport.Downloads
		report.ProgressMoved += episodeReport.ProgressMoved
		report.ProgressConflicts += episodeReport.ProgressConflicts
		report.HistoryMoved += episodeReport.HistoryMoved
		report.IntentMoved += episodeReport.IntentMoved
		report.EpisodePairsMoved = len(opts.EpisodePairs)
	}

	return report, nil
}

// pairsCTE is the shared FROM clause exposing (from_id, to_id) rows.
const pairsCTE = `(SELECT unnest($1::text[]) AS from_id, unnest($2::text[]) AS to_id) p`

// intentTables are the item-level rows with no per-file dimension. Each entry
// names the content-id column being remapped and the non-id PK columns used to
// detect collisions at the destination (destination wins; the duplicate source
// row is dropped). The series_id-keyed preference tables ride along so a
// series merge/split carries audio/subtitle/quality preferences with it
// (mirrors the provider-merge remap in internal/metadata/provider_id_integrity.go).
const (
	colMediaItemID = "media_item_id"
	colSeriesID    = "series_id"
	colUserID      = "user_id"
	colProfileID   = "profile_id"
)

var intentTables = []struct {
	table    string
	idColumn string
	keyCols  []string
}{
	{"user_favorites", colMediaItemID, []string{colUserID, colProfileID}},
	{"user_watchlist", colMediaItemID, []string{colUserID, colProfileID}},
	{"user_ratings", colMediaItemID, []string{colUserID, colProfileID}},
	{"user_personal_collection_items", colMediaItemID, []string{colUserID, "collection_id", "sub_item_id"}},
	{"library_collection_items", colMediaItemID, []string{"collection_id"}},
	{"user_home_item_dismissals", colMediaItemID, []string{colUserID, colProfileID, "surface"}},
	{"user_history_hidden_items", colMediaItemID, []string{colUserID, colProfileID}},
	{"user_audio_preferences", colSeriesID, []string{colUserID, colProfileID}},
	{"user_subtitle_preferences", colSeriesID, []string{colUserID, colProfileID}},
	{"user_series_playback_preferences", colSeriesID, []string{colUserID, colProfileID}},
}

// movePairs moves ALL state rows for each (from,to) pair: the whole-item path
// used for merges and for per-episode moves during series splits.
func movePairs(ctx context.Context, tx pgx.Tx, pairs []IDPair, report *Report) (err error) {
	fromIDs := make([]string, 0, len(pairs))
	toIDs := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		if pair.From == "" || pair.To == "" || pair.From == pair.To {
			return fmt.Errorf("reattribute: invalid id pair %q -> %q", pair.From, pair.To)
		}
		fromIDs = append(fromIDs, pair.From)
		toIDs = append(toIDs, pair.To)
	}

	// Exact tables: no cross-user PK on media_item_id, plain remap.
	tag, err := tx.Exec(ctx, `
		UPDATE playback_history_admin t SET media_item_id = p.to_id
		FROM `+pairsCTE+` WHERE t.media_item_id = p.from_id
	`, fromIDs, toIDs)
	if err != nil {
		return fmt.Errorf("reattribute: playback_history_admin pairs: %w", err)
	}
	report.PlaybackSessionLog += int(tag.RowsAffected())

	tag, err = tx.Exec(ctx, `
		UPDATE user_downloads t SET media_item_id = p.to_id
		FROM `+pairsCTE+` WHERE t.media_item_id = p.from_id
	`, fromIDs, toIDs)
	if err != nil {
		return fmt.Errorf("reattribute: user_downloads pairs: %w", err)
	}
	report.Downloads += int(tag.RowsAffected())

	// Managed offline downloads (downloads-v2): soft content_id + episode_id.
	// PK is the download id, so plain remaps — both columns swept because
	// episode pairs land in episode_id while item pairs land in content_id.
	tag, err = tx.Exec(ctx, `
		UPDATE downloads t SET content_id = p.to_id, updated_at = NOW()
		FROM `+pairsCTE+` WHERE t.content_id = p.from_id
	`, fromIDs, toIDs)
	if err != nil {
		return fmt.Errorf("reattribute: downloads content pairs: %w", err)
	}
	report.Downloads += int(tag.RowsAffected())
	if _, err := tx.Exec(ctx, `
		UPDATE downloads t SET episode_id = p.to_id, updated_at = NOW()
		FROM `+pairsCTE+` WHERE t.episode_id = p.from_id
	`, fromIDs, toIDs); err != nil {
		return fmt.Errorf("reattribute: downloads episode pairs: %w", err)
	}

	// series_id is denormalized onto dismissals (not part of their PK), so a
	// plain sweep keeps continue-watching/next-up dismissals series-scoped.
	if _, err := tx.Exec(ctx, `
		UPDATE user_home_item_dismissals t SET series_id = p.to_id
		FROM `+pairsCTE+` WHERE t.series_id = p.from_id
	`, fromIDs, toIDs); err != nil {
		return fmt.Errorf("reattribute: dismissal series pairs: %w", err)
	}

	tag, err = tx.Exec(ctx, `
		UPDATE user_watch_history t SET media_item_id = p.to_id
		FROM `+pairsCTE+` WHERE t.media_item_id = p.from_id
	`, fromIDs, toIDs)
	if err != nil {
		return fmt.Errorf("reattribute: user_watch_history pairs: %w", err)
	}
	report.HistoryMoved += int(tag.RowsAffected())

	moved, conflicts, err := moveProgressPairs(ctx, tx, fromIDs, toIDs)
	if err != nil {
		return err
	}
	report.ProgressMoved += moved
	report.ProgressConflicts += conflicts

	for _, intent := range intentTables {
		movedRows, err := moveIntentPairs(ctx, tx, intent.table, intent.idColumn, intent.keyCols, fromIDs, toIDs)
		if err != nil {
			return err
		}
		report.IntentMoved += movedRows
	}
	return nil
}

// moveProgressPairs moves user_watch_progress rows. PK is
// (user_id, profile_id, media_item_id); when both sides have a row for the
// same user+profile the row with the newer updated_at survives.
func moveProgressPairs(ctx context.Context, tx pgx.Tx, fromIDs, toIDs []string) (moved, conflicts int, err error) {
	// Source rows that lose to a same-or-newer destination row.
	tag, err := tx.Exec(ctx, `
		DELETE FROM user_watch_progress src
		USING `+pairsCTE+`, user_watch_progress dest
		WHERE src.media_item_id = p.from_id
		  AND dest.media_item_id = p.to_id
		  AND dest.user_id = src.user_id
		  AND dest.profile_id = src.profile_id
		  AND dest.updated_at >= src.updated_at
	`, fromIDs, toIDs)
	if err != nil {
		return 0, 0, fmt.Errorf("reattribute: progress source-loses dedupe: %w", err)
	}
	conflicts += int(tag.RowsAffected())

	// Destination rows that lose to a still-present newer source row.
	tag, err = tx.Exec(ctx, `
		DELETE FROM user_watch_progress dest
		USING `+pairsCTE+`, user_watch_progress src
		WHERE dest.media_item_id = p.to_id
		  AND src.media_item_id = p.from_id
		  AND src.user_id = dest.user_id
		  AND src.profile_id = dest.profile_id
	`, fromIDs, toIDs)
	if err != nil {
		return 0, 0, fmt.Errorf("reattribute: progress dest-loses dedupe: %w", err)
	}
	conflicts += int(tag.RowsAffected())

	tag, err = tx.Exec(ctx, `
		UPDATE user_watch_progress t SET media_item_id = p.to_id
		FROM `+pairsCTE+` WHERE t.media_item_id = p.from_id
	`, fromIDs, toIDs)
	if err != nil {
		return 0, 0, fmt.Errorf("reattribute: progress move: %w", err)
	}
	return int(tag.RowsAffected()), conflicts, nil
}

// moveIntentPairs moves one intent table for the pairs; on a destination
// collision the destination row wins and the source duplicate is dropped.
func moveIntentPairs(ctx context.Context, tx pgx.Tx, table, idColumn string, keyCols []string, fromIDs, toIDs []string) (int, error) {
	join := ""
	for _, col := range keyCols {
		join += fmt.Sprintf(" AND dest.%s = src.%s", col, col)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM `+table+` src
		USING `+pairsCTE+`, `+table+` dest
		WHERE src.`+idColumn+` = p.from_id
		  AND dest.`+idColumn+` = p.to_id`+join+`
	`, fromIDs, toIDs); err != nil {
		return 0, fmt.Errorf("reattribute: %s dedupe: %w", table, err)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE `+table+` t SET `+idColumn+` = p.to_id
		FROM `+pairsCTE+` WHERE t.`+idColumn+` = p.from_id
	`, fromIDs, toIDs)
	if err != nil {
		return 0, fmt.Errorf("reattribute: %s move: %w", table, err)
	}
	return int(tag.RowsAffected()), nil
}

// moveFileSubset is the split path: only the state attributable to
// MovedFileIDs follows the files to the destination item.
func moveFileSubset(ctx context.Context, tx pgx.Tx, opts Options, report *Report) error {
	if len(opts.MovedFileIDs) == 0 {
		return fmt.Errorf("reattribute: file-subset move requires moved file ids")
	}

	// History classification MUST run before the session log is re-pointed:
	// its evidence query reads playback_history_admin rows still keyed to the
	// source item. Moving the log first would erase exactly the evidence that
	// proves a profile's plays were all on moved files, leaving that profile's
	// history behind as "ambiguous".
	if err := moveHistorySubset(ctx, tx, opts, report); err != nil {
		return err
	}

	// Per-session log: exact.
	tag, err := tx.Exec(ctx, `
		UPDATE playback_history_admin
		SET media_item_id = $3
		WHERE media_item_id = $1 AND media_file_id = ANY($2::int[])
	`, opts.FromContentID, opts.MovedFileIDs, opts.ToContentID)
	if err != nil {
		return fmt.Errorf("reattribute: playback_history_admin subset: %w", err)
	}
	report.PlaybackSessionLog = int(tag.RowsAffected())

	// Downloads: exact (history rows and managed offline downloads).
	tag, err = tx.Exec(ctx, `
		UPDATE user_downloads
		SET media_item_id = $3
		WHERE media_item_id = $1 AND media_file_id = ANY($2::int[])
	`, opts.FromContentID, opts.MovedFileIDs, opts.ToContentID)
	if err != nil {
		return fmt.Errorf("reattribute: user_downloads subset: %w", err)
	}
	report.Downloads = int(tag.RowsAffected())

	// Managed offline downloads: soft content_id, remapped per file. The
	// episode_id column re-derives via EpisodePairs in Run for series splits.
	tag, err = tx.Exec(ctx, `
		UPDATE downloads
		SET content_id = $3, updated_at = NOW()
		WHERE content_id = $1 AND media_file_id = ANY($2::int[])
	`, opts.FromContentID, opts.MovedFileIDs, opts.ToContentID)
	if err != nil {
		return fmt.Errorf("reattribute: downloads subset: %w", err)
	}
	report.Downloads += int(tag.RowsAffected())

	// Progress: rows whose resume point sits on a moved file follow it.
	moved, conflicts, err := moveProgressSubset(ctx, tx, opts)
	if err != nil {
		return err
	}
	report.ProgressMoved = moved
	report.ProgressConflicts = conflicts

	// Intent rows only move when the operator asserts the whole identity was
	// wrong; evidence about individual files cannot attribute intent.
	if opts.Mode == HistoryModeMoveAll {
		for _, intent := range intentTables {
			movedRows, err := moveIntentPairs(ctx, tx, intent.table, intent.idColumn, intent.keyCols,
				[]string{opts.FromContentID}, []string{opts.ToContentID})
			if err != nil {
				return err
			}
			report.IntentMoved += movedRows
		}
	}
	return nil
}

func moveProgressSubset(ctx context.Context, tx pgx.Tx, opts Options) (moved, conflicts int, err error) {
	tag, err := tx.Exec(ctx, `
		DELETE FROM user_watch_progress src
		USING user_watch_progress dest
		WHERE src.media_item_id = $1
		  AND src.last_file_id = ANY($2::int[])
		  AND dest.media_item_id = $3
		  AND dest.user_id = src.user_id
		  AND dest.profile_id = src.profile_id
		  AND dest.updated_at >= src.updated_at
	`, opts.FromContentID, opts.MovedFileIDs, opts.ToContentID)
	if err != nil {
		return 0, 0, fmt.Errorf("reattribute: progress subset source-loses dedupe: %w", err)
	}
	conflicts += int(tag.RowsAffected())

	tag, err = tx.Exec(ctx, `
		DELETE FROM user_watch_progress dest
		USING user_watch_progress src
		WHERE dest.media_item_id = $3
		  AND src.media_item_id = $1
		  AND src.last_file_id = ANY($2::int[])
		  AND src.user_id = dest.user_id
		  AND src.profile_id = dest.profile_id
	`, opts.FromContentID, opts.MovedFileIDs, opts.ToContentID)
	if err != nil {
		return 0, 0, fmt.Errorf("reattribute: progress subset dest-loses dedupe: %w", err)
	}
	conflicts += int(tag.RowsAffected())

	tag, err = tx.Exec(ctx, `
		UPDATE user_watch_progress
		SET media_item_id = $3
		WHERE media_item_id = $1 AND last_file_id = ANY($2::int[])
	`, opts.FromContentID, opts.MovedFileIDs, opts.ToContentID)
	if err != nil {
		return 0, 0, fmt.Errorf("reattribute: progress subset move: %w", err)
	}
	return int(tag.RowsAffected()), conflicts, nil
}

// moveHistorySubset classifies user_watch_history rows per (user, profile)
// against the playback_history_admin session log:
//
//	unanimous moved sessions → the profile's history rows move
//	unanimous stayed sessions → they stay
//	mixed or no evidence      → ambiguous (HistoryMode decides)
//
// Classification is per profile, not per row: individual history rows carry no
// file linkage, so a profile that played both halves of the split cannot have
// its rows divided truthfully — those stay behind (evidence mode) rather than
// being guessed.
func moveHistorySubset(ctx context.Context, tx pgx.Tx, opts Options, report *Report) error {
	if opts.Mode == HistoryModeMoveAll {
		tag, err := tx.Exec(ctx, `
			UPDATE user_watch_history SET media_item_id = $2 WHERE media_item_id = $1
		`, opts.FromContentID, opts.ToContentID)
		if err != nil {
			return fmt.Errorf("reattribute: history move_all: %w", err)
		}
		report.HistoryMoved = int(tag.RowsAffected())
		return nil
	}

	const evidenceCTE = `
		WITH evidence AS (
			SELECT user_id, profile_id,
			       bool_or(media_file_id = ANY($2::int[])) AS any_moved,
			       bool_or(NOT (media_file_id = ANY($2::int[]))) AS any_stayed
			FROM playback_history_admin
			WHERE media_item_id = $1
			GROUP BY user_id, profile_id
		)`

	if opts.Mode == HistoryModeEvidence {
		tag, err := tx.Exec(ctx, evidenceCTE+`
			UPDATE user_watch_history h
			SET media_item_id = $3
			FROM evidence e
			WHERE h.media_item_id = $1
			  AND h.user_id = e.user_id
			  AND h.profile_id = e.profile_id
			  AND e.any_moved AND NOT e.any_stayed
		`, opts.FromContentID, opts.MovedFileIDs, opts.ToContentID)
		if err != nil {
			return fmt.Errorf("reattribute: history evidence move: %w", err)
		}
		report.HistoryMoved = int(tag.RowsAffected())
	}

	// Tally what stayed and what was ambiguous (also reported in keep mode so
	// dry-runs show the evidence split the operator is overriding).
	rows, err := tx.Query(ctx, evidenceCTE+`
		SELECT h.user_id, h.profile_id, h.watched_at::text,
		       COALESCE(e.any_moved, false) AS any_moved,
		       COALESCE(e.any_stayed, false) AS any_stayed
		FROM user_watch_history h
		LEFT JOIN evidence e ON e.user_id = h.user_id AND e.profile_id = h.profile_id
		WHERE h.media_item_id = $1
	`, opts.FromContentID, opts.MovedFileIDs)
	if err != nil {
		return fmt.Errorf("reattribute: history classification tally: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var row AmbiguousHistoryRow
		var anyMoved, anyStayed bool
		if err := rows.Scan(&row.UserID, &row.ProfileID, &row.WatchedAt, &anyMoved, &anyStayed); err != nil {
			return fmt.Errorf("reattribute: scanning history tally: %w", err)
		}
		switch {
		case anyStayed && !anyMoved:
			report.HistoryStayed++
		case anyMoved && !anyStayed:
			// Unanimous-moved rows were already moved in evidence mode; in
			// keep mode they are deliberate stay-behinds, count them stayed.
			if opts.Mode == HistoryModeKeep {
				report.HistoryStayed++
			}
		default:
			report.HistoryAmbiguous++
			if len(report.AmbiguousHistory) < ambiguousHistorySampleCap {
				report.AmbiguousHistory = append(report.AmbiguousHistory, row)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("reattribute: iterating history tally: %w", err)
	}
	return nil
}
