package metadata

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/catalog/reattribute"
	"github.com/Silo-Server/silo-server/internal/contentid"
)

// existingEpisode is a stored episode row as the renumber planner sees it.
type existingEpisode struct {
	ContentID string
	Season    int
	Episode   int
	// IDs holds the row's provider episode IDs by provider key (tvdb, tmdb,
	// imdb); empty values are absent.
	IDs map[string]string
}

// episodeMove gives a stored row new numbers.
type episodeMove struct {
	ContentID   string
	FromSeason  int
	FromEpisode int
	ToSeason    int
	ToEpisode   int
}

// episodeMerge folds a duplicate row into the row of the same episode: its
// user state and files move to Into and the row goes.
type episodeMerge struct {
	From string
	Into string
}

// episodeRenumberPlan is what planEpisodeRenumber decided.
type episodeRenumberPlan struct {
	Moves  []episodeMove
	Merges []episodeMerge
}

type episodeSlot struct{ season, episode int }

// renumberIDKeys are the provider ID columns an episode row carries, in the
// order the planner trusts them after the structure provider's own key.
var renumberIDKeys = []string{"tvdb", "tmdb", "imdb"}

// planEpisodeRenumber works out which stored rows must take new numbers so
// the series matches targets, the episodes its structure provider returned.
// Each target claims the row carrying the structure provider's ID for it,
// else a row sharing another provider ID, else the row already at its
// numbers when that row is not another episode of the structure provider.
// A claimed row whose numbers differ moves. A row nobody claims that shares a
// provider ID with a target is a duplicate of that target's row (an earlier
// numbering left it) and merges into it. Any other row nobody claims that
// sits on a target's numbers is parked after the last episode of its season:
// it may hold files or watch history, so it is never dropped here.
//
// Moves come back as the claimed rows' moves, ordered by their current
// numbers, then the parked rows', in the same order.
func planEpisodeRenumber(existing []existingEpisode, targets []EpisodeResult, structureKey string) episodeRenumberPlan {
	keys := make([]string, 0, len(renumberIDKeys))
	keys = append(keys, structureKey)
	for _, key := range renumberIDKeys {
		if key != structureKey {
			keys = append(keys, key)
		}
	}

	claimedBy := make(map[int]int, len(targets)) // existing index -> target index
	claimOf := make(map[int]int, len(targets))   // target index -> existing index
	claim := func(target, row int) {
		claimedBy[row] = target
		claimOf[target] = row
	}

	for _, key := range keys {
		byValue := make(map[string]int)
		for i, row := range existing {
			if _, taken := claimedBy[i]; taken {
				continue
			}
			if value := row.IDs[key]; value != "" {
				byValue[value] = i
			}
		}
		for t, target := range targets {
			if _, done := claimOf[t]; done {
				continue
			}
			value := target.ProviderIDs[key]
			if value == "" {
				continue
			}
			if row, ok := byValue[value]; ok {
				claim(t, row)
				delete(byValue, value)
			}
		}
	}

	atSlot := make(map[episodeSlot]int, len(existing))
	for i, row := range existing {
		atSlot[episodeSlot{row.Season, row.Episode}] = i
	}
	for t, target := range targets {
		if _, done := claimOf[t]; done {
			continue
		}
		row, ok := atSlot[episodeSlot{target.SeasonNumber, target.EpisodeNumber}]
		if !ok {
			continue
		}
		if _, taken := claimedBy[row]; taken {
			continue
		}
		own := existing[row].IDs[structureKey]
		if own != "" && own != target.ProviderIDs[structureKey] {
			continue
		}
		claim(t, row)
	}

	var merges []episodeMerge
	merged := make(map[int]bool)
	targetByID := make(map[string]int)
	for t, target := range targets {
		for key, value := range target.ProviderIDs {
			if value != "" {
				targetByID[key+"\x00"+value] = t
			}
		}
	}
	for i, row := range existing {
		if _, ok := claimedBy[i]; ok {
			continue
		}
		for _, key := range keys {
			value := row.IDs[key]
			if value == "" {
				continue
			}
			t, ok := targetByID[key+"\x00"+value]
			if !ok {
				continue
			}
			if into, claimed := claimOf[t]; claimed {
				merges = append(merges, episodeMerge{From: row.ContentID, Into: existing[into].ContentID})
				merged[i] = true
				break
			}
		}
	}

	var moves []episodeMove
	targetSlots := make(map[episodeSlot]bool, len(targets))
	lastEpisode := make(map[int]int)
	for _, target := range targets {
		targetSlots[episodeSlot{target.SeasonNumber, target.EpisodeNumber}] = true
		lastEpisode[target.SeasonNumber] = max(lastEpisode[target.SeasonNumber], target.EpisodeNumber)
	}
	for i, row := range existing {
		if merged[i] {
			continue
		}
		if t, ok := claimedBy[i]; ok {
			target := targets[t]
			if row.Season != target.SeasonNumber || row.Episode != target.EpisodeNumber {
				moves = append(moves, episodeMove{
					ContentID: row.ContentID, FromSeason: row.Season, FromEpisode: row.Episode,
					ToSeason: target.SeasonNumber, ToEpisode: target.EpisodeNumber,
				})
			}
			continue
		}
		if !targetSlots[episodeSlot{row.Season, row.Episode}] {
			lastEpisode[row.Season] = max(lastEpisode[row.Season], row.Episode)
		}
	}
	sortMoves(moves)

	var parked []episodeMove
	for i, row := range existing {
		if _, ok := claimedBy[i]; ok || merged[i] {
			continue
		}
		if !targetSlots[episodeSlot{row.Season, row.Episode}] {
			continue
		}
		lastEpisode[row.Season]++
		parked = append(parked, episodeMove{
			ContentID: row.ContentID, FromSeason: row.Season, FromEpisode: row.Episode,
			ToSeason: row.Season, ToEpisode: lastEpisode[row.Season],
		})
	}
	sortMoves(parked)
	return episodeRenumberPlan{Moves: append(moves, parked...), Merges: merges}
}

func sortMoves(moves []episodeMove) {
	sort.SliceStable(moves, func(i, j int) bool {
		if moves[i].FromSeason != moves[j].FromSeason {
			return moves[i].FromSeason < moves[j].FromSeason
		}
		return moves[i].FromEpisode < moves[j].FromEpisode
	})
}

// applyEpisodeRenumber gives the series' rows the numbers moves assign, inside
// tx. A row whose content ID was composed from its old numbers takes the ID
// composed from its new ones, and silo_rename_content_ids moves every user
// state row with it, so watch history follows the episode, not the number.
// Rows pass through temporary numbers and IDs first, so episodes can trade
// numbers. Linked files take the new numbers too, as a link always gives them.
// The caller holds the series' episode work and commits.
func applyEpisodeRenumber(ctx context.Context, tx pgx.Tx, seriesID string, moves []episodeMove) error {
	if len(moves) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM media_items WHERE content_id = $1 FOR UPDATE`, seriesID); err != nil {
		return fmt.Errorf("renumber: lock series %s: %w", seriesID, err)
	}

	var sources, temps, finals []string
	finalIDs := make([]string, len(moves))
	for i, move := range moves {
		finalIDs[i] = move.ContentID
		oldID, okOld := contentid.ForEpisode(seriesID, move.FromSeason, move.FromEpisode)
		newID, okNew := contentid.ForEpisode(seriesID, move.ToSeason, move.ToEpisode)
		if okOld && okNew && move.ContentID == oldID && newID != oldID {
			sources = append(sources, move.ContentID)
			temps = append(temps, move.ContentID+"~renumber")
			finals = append(finals, newID)
			finalIDs[i] = newID
		}
	}

	// Free every slot first: numbers go negative, which no episode uses.
	for i, move := range moves {
		if _, err := tx.Exec(ctx, `
			UPDATE episodes SET episode_number = $3
			WHERE series_id = $1 AND content_id = $2`,
			seriesID, move.ContentID, -(i + 1)); err != nil {
			return fmt.Errorf("renumber: park %s: %w", move.ContentID, err)
		}
	}
	if len(sources) > 0 {
		if _, err := tx.Exec(ctx, `SELECT silo_rename_content_ids($1, $2)`, sources, temps); err != nil {
			return fmt.Errorf("renumber: rename to temporary ids: %w", err)
		}
		// A leftover row on a final id merges with the moving one, as in any
		// rename, so an unmarked play cannot count as watched again.
		if _, err := reattribute.MergeHistoryCutoffPairs(ctx, tx, temps, finals); err != nil {
			return fmt.Errorf("renumber: %w", err)
		}
		if _, err := tx.Exec(ctx, `SELECT silo_rename_content_ids($1, $2)`, temps, finals); err != nil {
			return fmt.Errorf("renumber: rename to final ids: %w", err)
		}
		for i := range sources {
			if err := catalog.EnqueueSearchIndexRename(ctx, tx, sources[i], finals[i]); err != nil {
				return fmt.Errorf("renumber: enqueue search rename %s: %w", sources[i], err)
			}
		}
	}
	for i, move := range moves {
		if _, err := tx.Exec(ctx, `
			UPDATE episodes e
			SET season_number = $3, episode_number = $4,
			    season_id = COALESCE(
			        (SELECT s.content_id FROM seasons s WHERE s.series_id = $1 AND s.season_number = $3),
			        e.season_id),
			    updated_at = now()
			WHERE e.series_id = $1 AND e.content_id = $2`,
			seriesID, finalIDs[i], move.ToSeason, move.ToEpisode); err != nil {
			return fmt.Errorf("renumber: move %s to S%dE%d: %w", finalIDs[i], move.ToSeason, move.ToEpisode, err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE media_files SET season_number = $2, episode_number = $3
			WHERE episode_id = $1`,
			finalIDs[i], move.ToSeason, move.ToEpisode); err != nil {
			return fmt.Errorf("renumber: renumber files of %s: %w", finalIDs[i], err)
		}
	}
	return nil
}

// renumberSeriesEpisodes moves the series' stored episode rows to the
// numbers targets give them (planEpisodeRenumber, applyEpisodeRenumber), in
// one transaction.
func (s *MetadataService) renumberSeriesEpisodes(ctx context.Context, seriesID string, targets []EpisodeResult, structureKey string) error {
	if s.dbPool == nil {
		return nil
	}
	tx, err := s.dbPool.BeginTx(ctx, catalog.MetadataCurationTxOptions(ctx))
	if err != nil {
		return fmt.Errorf("renumber: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT 1 FROM media_items WHERE content_id = $1 FOR UPDATE`, seriesID); err != nil {
		return fmt.Errorf("renumber: lock series %s: %w", seriesID, err)
	}
	rows, err := tx.Query(ctx, `
		SELECT content_id, season_number, episode_number, tvdb_id, tmdb_id, imdb_id
		FROM episodes WHERE series_id = $1`, seriesID)
	if err != nil {
		return fmt.Errorf("renumber: list episodes of %s: %w", seriesID, err)
	}
	var existing []existingEpisode
	for rows.Next() {
		var row existingEpisode
		var tvdb, tmdb, imdb string
		if err := rows.Scan(&row.ContentID, &row.Season, &row.Episode, &tvdb, &tmdb, &imdb); err != nil {
			rows.Close()
			return fmt.Errorf("renumber: scan episode: %w", err)
		}
		row.IDs = map[string]string{"tvdb": tvdb, "tmdb": tmdb, "imdb": imdb}
		existing = append(existing, row)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("renumber: list episodes of %s: %w", seriesID, err)
	}
	plan := planEpisodeRenumber(existing, targets, structureKey)
	if len(plan.Moves) == 0 && len(plan.Merges) == 0 {
		return nil
	}
	// Duplicates go first: a moving row may need the composed ID a duplicate
	// still holds, and state merged into a row then moves with it.
	if err := applyEpisodeMerges(ctx, tx, seriesID, plan.Merges); err != nil {
		return err
	}
	if err := applyEpisodeRenumber(ctx, tx, seriesID, plan.Moves); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("renumber: commit: %w", err)
	}
	slog.InfoContext(ctx, "metadata: renumbered episodes", "component", "metadata",
		"series_id", seriesID, "provider", structureKey, "moved", len(plan.Moves), "merged", len(plan.Merges))
	return nil
}

// applyEpisodeMerges folds each duplicate row into the row of the same
// episode, inside tx: user state moves (reattribute.MovePairs, after the
// history cutoffs merge), files and library memberships relink with the
// surviving row's numbers, and the duplicate row is deleted.
func applyEpisodeMerges(ctx context.Context, tx pgx.Tx, seriesID string, merges []episodeMerge) error {
	if len(merges) == 0 {
		return nil
	}
	pairs := make([]reattribute.IDPair, len(merges))
	fromIDs := make([]string, len(merges))
	intoIDs := make([]string, len(merges))
	for i, merge := range merges {
		pairs[i] = reattribute.IDPair{From: merge.From, To: merge.Into}
		fromIDs[i] = merge.From
		intoIDs[i] = merge.Into
	}
	if _, err := reattribute.MergeHistoryCutoffPairs(ctx, tx, fromIDs, intoIDs); err != nil {
		return fmt.Errorf("renumber merge: %w", err)
	}
	if _, err := reattribute.MovePairs(ctx, tx, pairs); err != nil {
		return fmt.Errorf("renumber merge: move user state: %w", err)
	}
	for _, merge := range merges {
		if _, err := tx.Exec(ctx, `
			UPDATE media_files f
			SET episode_id = e.content_id, season_number = e.season_number, episode_number = e.episode_number
			FROM episodes e
			WHERE f.episode_id = $1 AND e.content_id = $2 AND e.series_id = $3`,
			merge.From, merge.Into, seriesID); err != nil {
			return fmt.Errorf("renumber merge: relink files of %s: %w", merge.From, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO episode_libraries (episode_id, media_folder_id)
			SELECT $2, media_folder_id FROM episode_libraries WHERE episode_id = $1
			ON CONFLICT DO NOTHING`,
			merge.From, merge.Into); err != nil {
			return fmt.Errorf("renumber merge: carry library membership of %s: %w", merge.From, err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM episodes WHERE content_id = $1 AND series_id = $2`,
			merge.From, seriesID); err != nil {
			return fmt.Errorf("renumber merge: delete duplicate %s: %w", merge.From, err)
		}
	}
	// Deleting episodes can lower the series' last air date (see
	// TestNoEpisodeDeletePath_ProtectsLastAirDateDenorm).
	if err := catalog.RefreshSeriesAirDates(ctx, tx, []string{seriesID}); err != nil {
		return fmt.Errorf("renumber merge: %w", err)
	}
	return catalog.RecomputeSeriesEpisodeMembershipDenorms(ctx, tx, []string{seriesID})
}
