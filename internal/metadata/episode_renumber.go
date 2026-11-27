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
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/naming"
)

// existingEpisode is a stored episode row as the renumber planner sees it.
type existingEpisode struct {
	ContentID string
	Season    int
	Episode   int
	// IDs holds the row's provider episode IDs by provider key (tvdb, tmdb,
	// imdb); empty values are absent.
	IDs map[string]string
	// Fallback marks a placeholder the scanner made for a file no order
	// placed.
	Fallback bool
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
// it may hold files or watch history, so it is never dropped here. A scanner
// placeholder there stands in for the episode at its numbers, so it merges
// into the row that moves there instead.
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
	targetAt := make(map[episodeSlot]int, len(targets))
	lastEpisode := make(map[int]int)
	for t, target := range targets {
		targetSlots[episodeSlot{target.SeasonNumber, target.EpisodeNumber}] = true
		targetAt[episodeSlot{target.SeasonNumber, target.EpisodeNumber}] = t
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
		if t, ok := targetAt[episodeSlot{row.Season, row.Episode}]; ok && row.Fallback {
			if into, claimed := claimOf[t]; claimed {
				merges = append(merges, episodeMerge{From: row.ContentID, Into: existing[into].ContentID})
				continue
			}
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

	// A row outside this move can hold a composed ID a moving row needs (its
	// ID stopped matching its numbers in earlier history). That moving row
	// takes a fresh ID instead, as a re-anchored series' children do.
	if len(finals) > 0 {
		takenRows, err := tx.Query(ctx, `
			SELECT content_id FROM episodes
			WHERE content_id = ANY($1) AND NOT (content_id = ANY($2))`, finals, sources)
		if err != nil {
			return fmt.Errorf("renumber: check final ids: %w", err)
		}
		taken := make(map[string]bool)
		for takenRows.Next() {
			var id string
			if err := takenRows.Scan(&id); err != nil {
				takenRows.Close()
				return fmt.Errorf("renumber: check final ids: %w", err)
			}
			taken[id] = true
		}
		takenRows.Close()
		if err := takenRows.Err(); err != nil {
			return fmt.Errorf("renumber: check final ids: %w", err)
		}
		for k, final := range finals {
			if !taken[final] {
				continue
			}
			fresh, err := generateContentID()
			if err != nil {
				return fmt.Errorf("renumber: mint id for %s: %w", sources[k], err)
			}
			slog.WarnContext(ctx, "metadata: renumbered episode takes a fresh id; its composed id is taken",
				"component", "metadata", "series_id", seriesID, "episode_id", sources[k], "target_id", final, "fresh_id", fresh)
			finals[k] = fresh
			for i := range moves {
				if moves[i].ContentID == sources[k] {
					finalIDs[i] = fresh
				}
			}
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
func (s *MetadataService) renumberSeriesEpisodes(ctx context.Context, seriesID string, seasons []SeasonResult, targets []EpisodeResult, structureKey string) error {
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
		SELECT content_id, season_number, episode_number, tvdb_id, tmdb_id, imdb_id,
		       metadata_source = 'scanner_fallback'
		FROM episodes WHERE series_id = $1`, seriesID)
	if err != nil {
		return fmt.Errorf("renumber: list episodes of %s: %w", seriesID, err)
	}
	var existing []existingEpisode
	for rows.Next() {
		var row existingEpisode
		var tvdb, tmdb, imdb string
		if err := rows.Scan(&row.ContentID, &row.Season, &row.Episode, &tvdb, &tmdb, &imdb, &row.Fallback); err != nil {
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
	// Duplicates go first: a moving row may need the composed ID a duplicate
	// still holds, and state merged into a row then moves with it.
	if err := applyEpisodeMerges(ctx, tx, seriesID, plan.Merges); err != nil {
		return err
	}
	if err := applyEpisodeRenumber(ctx, tx, seriesID, plan.Moves); err != nil {
		return err
	}
	if episodeFileNumbersOf(ctx) {
		if err := writeEpisodeFileNumbers(ctx, tx, seriesID, structureKey, targets); err != nil {
			return err
		}
	}
	// Moved rows took their files along; files named by number go back to
	// the episode their names give. This also runs when nothing moved, so a
	// refresh repairs links an earlier order left behind.
	relinked, retired, err := s.relinkSeriesFiles(ctx, tx, seriesID)
	if err != nil {
		return err
	}
	pruned, err := pruneVacatedSeasons(ctx, tx, seriesID, targetSeasonNumbers(seasons, targets))
	if err != nil {
		return err
	}
	changed := len(plan.Moves) > 0 || len(plan.Merges) > 0 || relinked > 0 || pruned > 0
	if !changed && !episodeFileNumbersOf(ctx) {
		return nil
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("renumber: commit: %w", err)
	}
	if !changed {
		return nil
	}
	slog.InfoContext(ctx, "metadata: renumbered episodes", "component", "metadata",
		"series_id", seriesID, "provider", structureKey, "moved", len(plan.Moves), "merged", len(plan.Merges),
		"files_relinked", relinked, "fallbacks_retired", retired, "seasons_pruned", pruned)
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

// seriesFileLink is a present file of a series with the numbers its name
// gives (Numbered when the name has a season and an episode number).
type seriesFileLink struct {
	ID        int
	Path      string
	FolderID  int
	EpisodeID string
	Season    int
	Episode   int
	Numbered  bool
}

// fileRelink moves a file's link From one episode row To another; an empty
// To clears the link.
type fileRelink struct {
	FileID          int
	From, To        string
	Season, Episode int
}

// planFileRelinks points every number-named file at the row its numbers name
// in rows (slot → content ID). A renumber moves rows with their links, but a
// file named S02E01 is season 2 episode 1 of the order its name follows,
// whatever row carried it before. Files linked by air date or title keep
// their row. A file with no row at its numbers is unlinked, unless it sits on
// a fallback placeholder (its own episode until an order places it). A
// fallback row whose files all moved to one real episode merges into it.
func planFileRelinks(files []seriesFileLink, rows map[episodeSlot]string, fallback map[string]bool) ([]fileRelink, []episodeMerge) {
	var relinks []fileRelink
	kept := make(map[string]bool)
	movedTo := make(map[string]map[string]bool)
	for _, file := range files {
		if !file.Numbered {
			if file.EpisodeID != "" {
				kept[file.EpisodeID] = true
			}
			continue
		}
		to, ok := rows[episodeSlot{file.Season, file.Episode}]
		if !ok && fallback[file.EpisodeID] {
			to = file.EpisodeID
		}
		if to == file.EpisodeID {
			if to != "" {
				kept[to] = true
			}
			continue
		}
		relinks = append(relinks, fileRelink{FileID: file.ID, From: file.EpisodeID, To: to, Season: file.Season, Episode: file.Episode})
		if fallback[file.EpisodeID] {
			if movedTo[file.EpisodeID] == nil {
				movedTo[file.EpisodeID] = make(map[string]bool)
			}
			movedTo[file.EpisodeID][to] = true
		}
	}
	var merges []episodeMerge
	for from, targets := range movedTo {
		if kept[from] || len(targets) != 1 {
			continue
		}
		for into := range targets {
			if into != "" && !fallback[into] {
				merges = append(merges, episodeMerge{From: from, Into: into})
			}
		}
	}
	sort.Slice(merges, func(i, j int) bool { return merges[i].From < merges[j].From })
	return relinks, merges
}

// applyFileRelinks moves each file's link as planned inside tx and keeps the
// episode library memberships in step: the new episode joins the file's
// library, the old one leaves it when no other present file serves it there.
// A cleared link keeps the file's own numbers so the next link pass finds or
// creates its episode.
func applyFileRelinks(ctx context.Context, tx pgx.Tx, seriesID string, relinks []fileRelink) error {
	if len(relinks) == 0 {
		return nil
	}
	var oldIDs []string
	for _, relink := range relinks {
		var to any
		if relink.To != "" {
			to = relink.To
		}
		if _, err := tx.Exec(ctx, `
			UPDATE media_files SET episode_id = $2, season_number = $3, episode_number = $4, updated_at = now()
			WHERE id = $1 AND content_id = $5`,
			relink.FileID, to, relink.Season, relink.Episode, seriesID); err != nil {
			return fmt.Errorf("relink file %d: %w", relink.FileID, err)
		}
		if relink.To != "" {
			if _, err := tx.Exec(ctx, `
				INSERT INTO episode_libraries (episode_id, media_folder_id, first_seen_at, first_seen_scan_run_id)
				SELECT episode_id, media_folder_id, created_at, first_seen_scan_run_id
				FROM media_files WHERE id = $1 AND missing_since IS NULL
				ON CONFLICT (episode_id, media_folder_id) DO NOTHING`, relink.FileID); err != nil {
				return fmt.Errorf("relink file %d: library membership: %w", relink.FileID, err)
			}
		}
		if relink.From != "" {
			oldIDs = append(oldIDs, relink.From)
		}
	}
	if len(oldIDs) > 0 {
		if _, err := tx.Exec(ctx, `
			DELETE FROM episode_libraries el
			WHERE el.episode_id IN `+catalog.SpanReachSQL("$1::text[]")+`
			  AND `+catalog.NoServingEpisodeFileSQL, oldIDs); err != nil {
			return fmt.Errorf("relink files: drop stale library memberships: %w", err)
		}
	}
	if _, err := catalog.InsertSpannedEpisodeMemberships(ctx, tx, "mf.content_id = $1", seriesID); err != nil {
		return err
	}
	return catalog.RecomputeSeriesEpisodeMembershipDenorms(ctx, tx, []string{seriesID})
}

// loadSeriesFileLinks reads the series' present files inside tx with the
// numbers their names give.
func (s *MetadataService) loadSeriesFileLinks(ctx context.Context, tx interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}, seriesID string) ([]seriesFileLink, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, file_path, media_folder_id, COALESCE(episode_id, '')
		FROM media_files
		WHERE content_id = $1 AND missing_since IS NULL AND extra_id IS NULL
		ORDER BY id`, seriesID)
	if err != nil {
		return nil, fmt.Errorf("relink: list files of %s: %w", seriesID, err)
	}
	var files []*models.MediaFile
	for rows.Next() {
		file := &models.MediaFile{}
		if err := rows.Scan(&file.ID, &file.FilePath, &file.MediaFolderID, &file.EpisodeID); err != nil {
			rows.Close()
			return nil, fmt.Errorf("relink: scan file: %w", err)
		}
		files = append(files, file)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("relink: list files of %s: %w", seriesID, err)
	}
	rootsByFolder, err := s.configuredNamingRootsForFiles(ctx, files)
	if err != nil {
		return nil, err
	}
	links := make([]seriesFileLink, 0, len(files))
	for _, file := range files {
		link := seriesFileLink{ID: file.ID, Path: file.FilePath, FolderID: file.MediaFolderID, EpisodeID: file.EpisodeID}
		if hints := naming.ParseFilename(file.FilePath, "series", rootsByFolder[file.MediaFolderID]...); hints != nil &&
			hints.SeasonKnown && hints.EpisodeNum > 0 && hints.AirDate == "" {
			link.Season, link.Episode, link.Numbered = hints.SeasonNum, hints.EpisodeNum, true
		}
		links = append(links, link)
	}
	return links, nil
}

// relinkSeriesFiles points the series' number-named files at the episodes
// their names give and retires emptied fallback rows (planFileRelinks),
// inside tx, after the rows took their new numbers.
func (s *MetadataService) relinkSeriesFiles(ctx context.Context, tx pgx.Tx, seriesID string) (int, int, error) {
	files, err := s.loadSeriesFileLinks(ctx, tx, seriesID)
	if err != nil || len(files) == 0 {
		return 0, 0, err
	}
	rows, err := tx.Query(ctx, `
		SELECT content_id, COALESCE(file_season_number, season_number),
		       COALESCE(file_episode_number, episode_number), metadata_source = 'scanner_fallback'
		FROM episodes WHERE series_id = $1
		ORDER BY file_season_number IS NOT NULL`, seriesID)
	if err != nil {
		return 0, 0, fmt.Errorf("relink: list episodes of %s: %w", seriesID, err)
	}
	slots := make(map[episodeSlot]string)
	fallback := make(map[string]bool)
	for rows.Next() {
		var id string
		var slot episodeSlot
		var isFallback bool
		if err := rows.Scan(&id, &slot.season, &slot.episode, &isFallback); err != nil {
			rows.Close()
			return 0, 0, fmt.Errorf("relink: scan episode: %w", err)
		}
		slots[slot] = id
		if isFallback {
			fallback[id] = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, fmt.Errorf("relink: list episodes of %s: %w", seriesID, err)
	}
	relinks, merges := planFileRelinks(files, slots, fallback)
	if err := applyFileRelinks(ctx, tx, seriesID, relinks); err != nil {
		return 0, 0, err
	}
	if err := applyEpisodeMerges(ctx, tx, seriesID, merges); err != nil {
		return 0, 0, err
	}
	return len(relinks), len(merges), nil
}

// pruneVacatedSeasons removes the series' season rows that the new order
// does not have (keep lists its season numbers) and that no episode is in any
// more, inside tx: an order with fewer seasons, or an absolute layout, leaves
// them behind empty. Seasons cascade to their episodes, so a row that still
// holds one stays.
func pruneVacatedSeasons(ctx context.Context, tx pgx.Tx, seriesID string, keep []int32) (int64, error) {
	tag, err := tx.Exec(ctx, `
		DELETE FROM seasons s
		WHERE s.series_id = $1
		  AND NOT (s.season_number = ANY($2::int[]))
		  AND NOT EXISTS (
			SELECT 1 FROM episodes e
			WHERE e.season_id = s.content_id
			   OR (e.series_id = s.series_id AND e.season_number = s.season_number)
		  )`, seriesID, keep)
	if err != nil {
		return 0, fmt.Errorf("renumber: prune vacated seasons of %s: %w", seriesID, err)
	}
	return tag.RowsAffected(), nil
}

// targetSeasonNumbers lists the season numbers of the new order: the
// provider's seasons and every season an episode lands in.
func targetSeasonNumbers(seasons []SeasonResult, episodes []EpisodeResult) []int32 {
	seen := make(map[int]bool)
	var out []int32
	add := func(n int) {
		if !seen[n] && catalog.FitsPostgresInteger(n) {
			seen[n] = true
			out = append(out, int32(n))
		}
	}
	for _, season := range seasons {
		add(season.SeasonNumber)
	}
	for _, ep := range episodes {
		add(ep.SeasonNumber)
	}
	return out
}
