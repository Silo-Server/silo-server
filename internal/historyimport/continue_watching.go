package historyimport

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// SeriesDropStore is the dropped-series store the Continue Watching pass
// writes through. notifications.DroppedSeriesTracker satisfies it, so an
// imported drop recomputes interest like a drop made in Silo.
type SeriesDropStore interface {
	EpisodeSeriesIDs(ctx context.Context, episodeIDs []string) (map[string]string, error)
	ListDropped(ctx context.Context, userID int, profileID string, seriesIDs []string) ([]catalog.DroppedSeries, error)
	LatestActivity(ctx context.Context, userID int, profileID string, seriesIDs []string) (map[string]time.Time, error)
	ImportDrop(ctx context.Context, userID int, profileID, seriesID string, droppedAt time.Time, observed *time.Time) (bool, error)
	DeleteIfUnchanged(ctx context.Context, userID int, profileID, seriesID string, observed time.Time) (bool, error)
}

// NextUpLister reports what a profile's Continue Watching and Next Up would
// show for a series. catalog.NextUpRepository satisfies it.
type NextUpLister interface {
	ListNextUp(ctx context.Context, q catalog.NextUpQuery) ([]catalog.NextUpResult, error)
}

// SetContinueWatchingStores enables the pass that hides the shows a source
// hid from its own Continue Watching. Without the stores runs skip it.
func (s *Service) SetContinueWatchingStores(drops SeriesDropStore, nextUp NextUpLister) {
	s.seriesDrops = drops
	s.nextUp = nextUp
}

// importedEpisode is an episode whose watch state a run imported.
type importedEpisode struct {
	itemID         string
	sourceSeriesID string
	// at is the stamp the episode's progress was imported with.
	at time.Time
	// inProgress marks an episode imported with a resume point.
	inProgress bool
}

// newImportedEpisode records a matched episode record for the pass, stamped
// as applyImportedWatch stamps its progress.
func newImportedEpisode(itemID string, record Record) importedEpisode {
	at := record.UpdatedAt
	if at.IsZero() {
		at = undatedImportTime
	}
	return importedEpisode{
		itemID:         itemID,
		sourceSeriesID: record.SourceSeriesID,
		at:             at,
		inProgress:     !record.Played && record.PositionSeconds > 0,
	}
}

// importedShow is what a run imported of one Silo series.
type importedShow struct {
	// lastPlay is the newest imported stamp of its episodes.
	lastPlay time.Time
	// inProgress: its most recently played episode was imported with a
	// resume point.
	inProgress bool
	// unfinished: the source counts the show as having episodes left.
	unfinished bool
}

// reconcileContinueWatching drops each show the run put in the profile's
// Continue Watching or Next Up that the source's own row leaves out. Emby
// hides per show, both a show paused mid-episode and one between episodes,
// and keeps it hidden until one of its episodes is played again; a Silo drop
// behaves the same way. It returns how many shows it dropped.
//
// A show missing from the row is only taken as hidden when the source gives
// evidence the row would otherwise list it: an episode in progress, or, when
// the row lists shows between episodes at all, the source counting the show
// as unfinished. A show the user finished at the source is left alone, even
// when Silo has episodes the source lacks.
//
// The drop is dated at the run, so it ends at the first playback after the
// import, in Silo or at the source on a later run. A show whose last play at
// the source is stamped after this server's clock waits for a later import. Shows with Silo activity
// newer than their last imported play are left alone, as are the profile's
// own drops, so re-running an import changes nothing.
//
// A show counts as listed when any of its copies at the source is in the
// row: by the source series of the episodes the run imported, or by the
// listed series' provider IDs, which mark every Silo series that has them.
func (s *Service) reconcileContinueWatching(ctx context.Context, userID int, profileID string, row ContinueWatchingRow, episodes []importedEpisode) (int, error) {
	if s.seriesDrops == nil || s.nextUp == nil {
		slog.InfoContext(ctx, "history import: continue watching pass skipped: stores not configured", "component", "historyimport")
		return 0, nil
	}
	// One summary line says where the pass stopped and why: how many shows
	// the run imported, how many the source's row lists, how many of the
	// rest the source shows as unfinished and Silo would surface, and how
	// many of those were dropped or kept.
	var stats struct {
		shows, listed, settled, surfaced, keptNewer, keptDropped, keptFuture, undone, dropped int
		unidentified                                                                          bool
	}
	defer func() {
		slog.InfoContext(ctx, "history import: continue watching pass",
			"component", "historyimport", "profile_id", profileID,
			"episodes", len(episodes), "row_shows", len(row.SourceSeriesIDs), "row_includes_next_up", row.IncludesNextUp,
			"shows", stats.shows, "listed", stats.listed, "not_hideable", stats.settled, "row_unidentified", stats.unidentified,
			"surfaced_unlisted", stats.surfaced, "kept_newer_activity", stats.keptNewer,
			"kept_existing_drop", stats.keptDropped, "kept_future_play", stats.keptFuture,
			"undone_for_new_activity", stats.undone, "dropped", stats.dropped)
	}()
	if len(episodes) == 0 {
		return 0, nil
	}

	// Resolve every episode, in batches: one source series can span several
	// Silo series, as when a show is split into seasons differently.
	itemIDs := make([]string, 0, len(episodes))
	seen := make(map[string]bool, len(episodes))
	for _, episode := range episodes {
		if !seen[episode.itemID] {
			seen[episode.itemID] = true
			itemIDs = append(itemIDs, episode.itemID)
		}
	}
	seriesOf, err := s.seriesDrops.EpisodeSeriesIDs(ctx, itemIDs)
	if err != nil {
		return 0, err
	}
	shows := make(map[string]*importedShow)
	bySource := make(map[string][]string)
	for _, episode := range episodes {
		seriesID := seriesOf[episode.itemID]
		if seriesID == "" {
			continue
		}
		if episode.sourceSeriesID != "" && !slices.Contains(bySource[episode.sourceSeriesID], seriesID) {
			bySource[episode.sourceSeriesID] = append(bySource[episode.sourceSeriesID], seriesID)
		}
		show := shows[seriesID]
		if show == nil {
			show = &importedShow{}
			shows[seriesID] = show
		}
		// The show is in progress when its most recently played episode is:
		// Emby leaves out a show whose paused episode is older than a later
		// finished one, without it being hidden.
		switch {
		case episode.at.After(show.lastPlay):
			show.lastPlay = episode.at
			show.inProgress = episode.inProgress
		case episode.at.Equal(show.lastPlay):
			show.inProgress = show.inProgress || episode.inProgress
		}
		show.unfinished = show.unfinished || row.UnfinishedSourceSeries[episode.sourceSeriesID]
	}
	stats.shows = len(shows)
	if len(shows) == 0 {
		return 0, nil
	}

	listed := make(map[string]bool)
	for sourceSeriesID := range row.SourceSeriesIDs {
		for _, seriesID := range bySource[sourceSeriesID] {
			listed[seriesID] = true
		}
	}
	withProviderIDs := make(map[string]bool, len(row.Series))
	for _, series := range row.Series {
		if series.TMDBID == "" && series.TVDBID == "" && series.IMDbID == "" {
			continue
		}
		withProviderIDs[series.ExternalID] = true
		ids, err := s.seriesIDsByProviderIDs(ctx, series)
		if err != nil {
			return 0, err
		}
		for _, id := range ids {
			listed[id] = true
		}
	}
	// A listed show the run imported nothing of and that has no provider ID
	// might be a copy of any imported show; with it in the row nothing can be
	// called missing. One with provider IDs that match no Silo series is a
	// show Silo lacks, which hides nothing.
	for sourceSeriesID := range row.SourceSeriesIDs {
		if len(bySource[sourceSeriesID]) == 0 && !withProviderIDs[sourceSeriesID] {
			stats.unidentified = true
		}
	}
	if stats.unidentified {
		return 0, nil
	}

	// Only shows Silo would surface are dropped: a show Silo would not list
	// is not in its Continue Watching either way.
	candidates := make([]string, 0)
	for seriesID, show := range shows {
		if listed[seriesID] {
			stats.listed++
			continue
		}
		if !show.inProgress && !(row.IncludesNextUp && show.unfinished) {
			stats.settled++
			continue
		}
		surfaced, err := s.nextUp.ListNextUp(ctx, catalog.NextUpQuery{
			UserID: userID, ProfileID: profileID, SeriesID: seriesID, Limit: 1, EnableResumable: true,
		})
		if err != nil {
			return 0, err
		}
		if len(surfaced) > 0 {
			candidates = append(candidates, seriesID)
		}
	}
	stats.surfaced = len(candidates)
	if len(candidates) == 0 {
		return 0, nil
	}
	slices.Sort(candidates)

	// The drop time is taken before activity is read. Playback in Silo
	// stamps its progress with this server's clock when it saves, so playback
	// the read misses is newer than the drop and ends it.
	now := s.clock()
	activity, err := s.seriesDrops.LatestActivity(ctx, userID, profileID, candidates)
	if err != nil {
		return 0, err
	}
	existingDrops, err := s.seriesDrops.ListDropped(ctx, userID, profileID, candidates)
	if err != nil {
		return 0, err
	}
	existing := make(map[string]catalog.DroppedSeries, len(existingDrops))
	for _, drop := range existingDrops {
		existing[drop.SeriesID] = drop
	}

	written := make(map[string]time.Time)
	for _, seriesID := range candidates {
		// Postgres keeps microseconds, so compare stamps as stored.
		lastPlay := shows[seriesID].lastPlay.UTC().Truncate(time.Microsecond)
		// A play stamped after this server's clock comes from a source clock
		// that runs ahead. A drop dated before it would end at once, and one
		// dated after it would outlast playback here, so the show waits for a
		// later import.
		if lastPlay.After(now) {
			stats.keptFuture++
			continue
		}
		// Playback newer than anything imported means the user is watching
		// the show in Silo, whatever the source's row says.
		if latest, ok := activity[seriesID]; ok && latest.After(lastPlay) {
			stats.keptNewer++
			continue
		}
		var observed *time.Time
		if drop, ok := existing[seriesID]; ok {
			// Already hidden, or dropped since the source's last play and
			// watched again: the profile's own choice stands.
			if drop.Active || !drop.DroppedAt.Before(lastPlay) {
				stats.keptDropped++
				continue
			}
			previous := drop.DroppedAt
			observed = &previous
		}
		applied, err := s.seriesDrops.ImportDrop(ctx, userID, profileID, seriesID, now, observed)
		if err != nil {
			return len(written), err
		}
		if applied {
			written[seriesID] = now
		}
	}
	if len(written) == 0 {
		return 0, nil
	}

	// Playback that saved between the activity read and the write, stamped
	// just before the drop time, would leave the drop active. Read activity
	// again and take back each drop it would wrongly keep.
	writtenIDs := slices.Sorted(maps.Keys(written))
	recheck, err := s.seriesDrops.LatestActivity(ctx, userID, profileID, writtenIDs)
	if err != nil {
		return len(written), err
	}
	for _, seriesID := range writtenIDs {
		lastPlay := shows[seriesID].lastPlay.UTC().Truncate(time.Microsecond)
		if latest, ok := recheck[seriesID]; ok && latest.After(lastPlay) {
			if _, err := s.seriesDrops.DeleteIfUnchanged(ctx, userID, profileID, seriesID, written[seriesID]); err != nil {
				return len(written), err
			}
			delete(written, seriesID)
			stats.undone++
		}
	}
	dropped := len(written)
	stats.dropped = dropped
	return dropped, nil
}

// seriesIDsByProviderIDs returns every Silo series with the first of
// series' provider IDs that matches any, in the matcher's order (TVDB, then
// TMDB, then IMDb, or TMDB first when PreferTMDB is set). Unlike
// Matcher.Match it keeps every series with that ID, so a show held in two
// libraries marks both as listed; a stale secondary ID is never consulted
// once the primary one matches.
func (s *Service) seriesIDsByProviderIDs(ctx context.Context, series Record) ([]string, error) {
	candidates := []struct{ column, value string }{
		{"tvdb_id", series.TVDBID}, {"tmdb_id", series.TMDBID}, {"imdb_id", series.IMDbID},
	}
	if series.PreferTMDB {
		candidates[0], candidates[1] = candidates[1], candidates[0]
	}
	for _, candidate := range candidates {
		if candidate.value == "" {
			continue
		}
		rows, err := s.matcher.repo.MatchMediaByExternalID(ctx, KindSeries, candidate.column, candidate.value)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			continue
		}
		ids := make([]string, 0, len(rows))
		for _, row := range rows {
			if !slices.Contains(ids, row.ContentID) {
				ids = append(ids, row.ContentID)
			}
		}
		return ids, nil
	}
	return nil, nil
}

// clock returns the current time; tests replace now.
func (s *Service) clock() time.Time {
	if s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}
