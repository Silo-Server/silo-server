package historyimport

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
)

type EmbyProvider struct {
	client *EmbyClient
	auth   embyLocalAuth
	// row is the Continue Watching row the last Fetch read, or nil when it
	// could not be read reliably.
	row *ContinueWatchingRow
}

// Stages of an Emby listing, in order.
const (
	embyStagePlayed    = "played"
	embyStageRemainder = "resumable"
	embyStageFavorites = "favorites"
)

var embyStages = []string{embyStagePlayed, embyStageRemainder, embyStageFavorites}

// ContinueWatchingRow reports the shows in Emby's Continue Watching row as
// of the last Fetch. ok is false when the row could not be read reliably,
// and then nothing may be hidden on its account.
func (p *EmbyProvider) ContinueWatchingRow() (ContinueWatchingRow, bool) {
	if p.row == nil {
		return ContinueWatchingRow{}, false
	}
	return *p.row, true
}

func NewEmbyProvider(client *EmbyClient, auth embyLocalAuth) *EmbyProvider {
	return &EmbyProvider{client: client, auth: auth}
}

// maxEmbyEpisodeRange bounds how many episodes one multi-episode file expands
// to; a wider range is imported as its first episode only.
const maxEmbyEpisodeRange = 10

// embySeriesLookup resolves a page's series through a fixed-size cache. A
// failed lookup stops further ones for the run: Emby answers them the same
// way, and episodes carrying their own provider IDs still match without it.
type embySeriesLookup struct {
	provider *EmbyProvider
	cache    *lookupCache[embyItem]
	failed   bool
	// unfinished holds every looked-up series Emby counts as having episodes
	// left, which the Continue Watching pass needs for the whole run.
	unfinished map[string]bool
}

func (l *embySeriesLookup) resolve(ctx context.Context, items []embyItem) (map[string]embyItem, []string, error) {
	result := make(map[string]embyItem)
	var pending []string
	seen := make(map[string]struct{})
	for _, item := range items {
		id := strings.TrimSpace(item.SeriesID)
		if !strings.EqualFold(item.Type, "episode") || id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		if series, ok := l.cache.get(id); ok {
			result[id] = series
		} else {
			pending = append(pending, id)
		}
	}
	if len(pending) == 0 || l.failed {
		return result, nil, nil
	}
	seriesItems, err := l.provider.client.FetchItemsByIDs(ctx, l.provider.auth, pending, "Series")
	if errors.Is(err, ErrUpstreamResponseTooLarge) || ctx.Err() != nil {
		return nil, nil, err
	}
	if err != nil {
		slog.WarnContext(ctx, "emby history import: series metadata unavailable", "component", "historyimport", "error", warningLogError("emby", err))
		l.failed = true
		return result, []string{warnEmbySeriesUnavailable}, nil
	}
	for _, series := range seriesItems {
		l.cache.put(series.ID, series)
		result[series.ID] = series
		if unplayed := series.UserData.UnplayedItemCount; unplayed != nil && *unplayed > 0 && len(l.unfinished) < maxTrackedSeries {
			l.unfinished[series.ID] = true
		}
	}
	return result, nil, nil
}

// Stream reads the short lists first (resumable items, the Continue Watching
// row), then the played pages merged against them, what was only resumable,
// and favorites. A favorite needs no merge: it only adds to favorites, while
// the same item's watch state is imported from its played or resumable record.
func (p *EmbyProvider) Stream(ctx context.Context, from *StreamPosition, sink RecordSink) error {
	sink = markSinkErrors(sink)
	p.row = nil
	resumable, err := p.client.fetchAllItems(ctx, p.auth, embyEndpointItems, "IsResumable", embyPlayableTypes)
	if err != nil {
		return err
	}
	var warnings []string
	// The row is read even when nothing is resumable: a show between
	// episodes has no resumable episode, and the row is the only place Emby
	// shows whether it was hidden.
	listed, err := p.client.fetchAllItems(ctx, p.auth, embyEndpointResume, "", embyPlayableTypes)
	// A source over the size limits fails the run here and below, rather
	// than importing without the list it sent.
	if errors.Is(err, ErrUpstreamResponseTooLarge) {
		return err
	}
	rowRead := err == nil
	if err != nil {
		slog.WarnContext(ctx, "emby history import: continue watching list unavailable", "component", "historyimport", "error", warningLogError("emby", err))
		warnings = append(warnings, warnEmbyResumeListUnavailable)
		listed = nil
	}
	hidden := hiddenFromResume(resumable, listed, rowRead)
	held, err := holdItems(ctx, resumable, func(item embyItem) string { return item.ID })
	if err != nil {
		return fmt.Errorf("holding Emby resumable items: %w", err)
	}
	defer held.release(ctx)

	series := &embySeriesLookup{provider: p, cache: newLookupCache[embyItem](seriesLookupCacheEntries), unfinished: map[string]bool{}}
	listedSeries, lookupWarnings, err := series.resolve(ctx, listed)
	if err != nil {
		return err
	}
	warnings = append(warnings, lookupWarnings...)
	if len(warnings) > 0 {
		if err := sink(RecordBatch{Warnings: warnings}); err != nil {
			return err
		}
	}

	if stageReached(from, embyStages, embyStagePlayed) {
		err := p.client.forEachItemsPage(ctx, p.auth, embyEndpointItems, "IsPlayed", embyPlayableTypes, stageOffset(from, embyStagePlayed), func(page []embyItem, start int) error {
			return p.emitWatched(ctx, page, held, hidden, series, StreamPosition{Stage: embyStagePlayed, Offset: start}, sink)
		})
		if err != nil {
			return err
		}
	}
	if stageReached(from, embyStages, embyStageRemainder) {
		for page := range slices.Chunk(held.remaining(), embyPageSize) {
			if err := p.emitWatched(ctx, page, nil, hidden, series, StreamPosition{Stage: embyStageRemainder}, sink); err != nil {
				return err
			}
		}
	}
	if err := p.streamFavorites(ctx, from, series, sink); err != nil {
		return err
	}

	// Without the series' provider IDs a show in the row that the import
	// matched under another Emby series could not be recognized, and with too
	// many shows to track the unfinished counts would be partial, so the row
	// is only reported when both are complete.
	if rowRead && !series.failed && len(series.unfinished) < maxTrackedSeries {
		p.row = continueWatchingRow(listed, listedSeries)
		if p.row == nil {
			slog.WarnContext(ctx, "emby history import: continue watching lists an episode without a series; shows hidden there stay visible", "component", "historyimport")
		} else {
			maps.Copy(p.row.UnfinishedSourceSeries, series.unfinished)
		}
	}
	return nil
}

// emitWatched normalizes one page of watch items, merging held resumable
// state, and hands it to sink.
func (p *EmbyProvider) emitWatched(ctx context.Context, page []embyItem, held *heldItems[embyItem], hidden map[string]bool, series *embySeriesLookup, resume StreamPosition, sink RecordSink) error {
	meta, warnings, err := series.resolve(ctx, page)
	if err != nil {
		return err
	}
	records := make([]Record, 0, len(page))
	for _, item := range page {
		expanded := embyWatchedRecords(item, meta[item.SeriesID])
		if held != nil {
			if progress, ok := held.take(item.ID); ok {
				expanded = mergeWatchedRecords(expanded, embyWatchedRecords(progress, meta[progress.SeriesID]))
			}
		}
		for _, record := range expanded {
			record.HiddenFromResume = hidden[item.ID]
			records = append(records, record)
		}
	}
	return sink(RecordBatch{Records: records, Warnings: warnings, Resume: resume})
}

// mergeWatchedRecords merges an item's in-progress records into its played
// ones by external ID. A multi-episode file expands when played, so the played
// side holds every episode and a partly watched copy merges into its first.
func mergeWatchedRecords(played, progress []Record) []Record {
	for _, update := range progress {
		for i := range played {
			if played[i].ExternalID == update.ExternalID {
				played[i] = mergeRecords(played[i], update)
			}
		}
	}
	return played
}

func (p *EmbyProvider) streamFavorites(ctx context.Context, from *StreamPosition, series *embySeriesLookup, sink RecordSink) error {
	if !stageReached(from, embyStages, embyStageFavorites) {
		return nil
	}
	// Silo has no season favorites, so report them rather than drop them.
	seasonFavorites := 0
	err := p.client.forEachItemsPage(ctx, p.auth, embyEndpointItems, "IsFavorite", embyFavoriteTypes, stageOffset(from, embyStageFavorites), func(page []embyItem, start int) error {
		page = slices.DeleteFunc(page, func(item embyItem) bool {
			isSeason := strings.EqualFold(item.Type, "season")
			if isSeason {
				seasonFavorites++
			}
			return isSeason
		})
		meta, warnings, err := series.resolve(ctx, page)
		if err != nil {
			return err
		}
		records := make([]Record, 0, len(page))
		for _, item := range page {
			record := normalizeEmbyItem(item, meta[item.SeriesID])
			record.Favorite = true
			record.FavoriteOnly = true
			record.PreferTMDB = true
			// Watch records own the runtime; a favorite's full file runtime must
			// not replace a multi-episode file's per-episode share.
			record.DurationSeconds = 0
			record.RepeatOfWatch = item.UserData.Played || item.UserData.PlaybackPositionTicks > 0
			records = append(records, record)
		}
		return sink(RecordBatch{Records: records, Warnings: warnings, Resume: StreamPosition{Stage: embyStageFavorites, Offset: start}})
	})
	if err != nil {
		if errors.Is(err, ErrUpstreamResponseTooLarge) || ctx.Err() != nil || isSinkError(err) {
			return err
		}
		// Warnings store fixed text: v1 returns them verbatim, and upstream
		// errors can carry the server's response body. The error is logged.
		slog.WarnContext(ctx, "emby history import: favorites unavailable", "component", "historyimport", "error", warningLogError("emby", err))
		return sink(RecordBatch{Warnings: []string{warnEmbyFavoritesUnavailable}})
	}
	if seasonFavorites > 0 {
		return sink(RecordBatch{Warnings: []string{fmt.Sprintf(warnEmbySeasonFavorites, seasonFavorites)}})
	}
	return nil
}

// embyWatchedRecords expands a played multi-episode file (S01E01-E02) into
// one record per episode, splitting its runtime evenly. Emby's provider IDs
// describe the first episode only, so the others match by series identity
// and number. A partly watched file stays one record: its position cannot be
// attributed to a single episode.
func embyWatchedRecords(item embyItem, series embyItem) []Record {
	record := normalizeEmbyItem(item, series)
	// The remote server supplies these numbers: bound them before subtracting,
	// and loop over an offset so an end of math.MaxInt cannot wrap the counter.
	first, last := item.IndexNumber, item.IndexNumberEnd
	if record.Kind != KindEpisode || !record.Played || first < 0 || last <= first || last-first >= maxEmbyEpisodeRange {
		return []Record{record}
	}
	count := last - first + 1
	record.DurationSeconds /= float64(count)
	records := []Record{record}
	for offset := 1; offset < count; offset++ {
		episode := first + offset
		next := record
		next.ExternalID = fmt.Sprintf("%s#E%d", item.ID, episode)
		next.EpisodeNumber = episode
		next.IMDbID, next.TMDBID, next.TVDBID = "", "", ""
		records = append(records, next)
	}
	return records
}

// hiddenFromResume returns the resumable items the user hid from Emby's
// Continue Watching. Hiding leaves an item's user data unchanged and the
// IsResumable filter still returns it; only Emby's own resume list leaves it
// out. That list shows one episode per series, so episodes are judged by
// series: hiding an episode hides its whole series, and every resumable
// episode of a series missing from the list is reported. Each gets a
// Continue Watching dismissal, which also covers profiles and runs the
// series pass (see ContinueWatchingRow) cannot act for. Without the list
// nothing is reported hidden.
func hiddenFromResume(resumable, listed []embyItem, listRead bool) map[string]bool {
	if !listRead {
		return nil
	}
	shown := make(map[string]bool, len(listed))
	for _, item := range listed {
		shown[resumeListKey(item)] = true
	}
	hidden := make(map[string]bool)
	for _, item := range resumable {
		if key := resumeListKey(item); key != "" && !shown[key] {
			hidden[item.ID] = true
		}
	}
	return hidden
}

// resumeListKey is the ID Emby's resume list represents an item by: a movie's
// own ID, or an episode's series ID, since the list shows one episode per
// series. Other items have no key.
func resumeListKey(item embyItem) string {
	switch {
	case strings.EqualFold(item.Type, "movie"):
		return item.ID
	case strings.EqualFold(item.Type, "episode"):
		return item.SeriesID
	}
	return ""
}

// continueWatchingRow describes the shows in Emby's Continue Watching row:
// Emby lists one episode per show, either the one in progress or, since Emby
// 4.6 merged Next Up into the row, the next unstarted one. A listed episode
// without a series can't be placed, so such a row is not reported (nil):
// any show the import touched might be the one it stands for.
//
// The row also records whether it lists any unstarted episode (IncludesNextUp):
// Emby can keep Next Up out of the row, and then a show between episodes is
// missing from it whether or not it was hidden. And it records which of the
// import's shows Emby counts as unfinished, from each series' unplayed count:
// a show the user finished at the source is missing from the row too.
func continueWatchingRow(listed []embyItem, seriesMeta map[string]embyItem) *ContinueWatchingRow {
	row := &ContinueWatchingRow{SourceSeriesIDs: map[string]bool{}, UnfinishedSourceSeries: map[string]bool{}}
	for id, series := range seriesMeta {
		if unplayed := series.UserData.UnplayedItemCount; unplayed != nil && *unplayed > 0 {
			row.UnfinishedSourceSeries[id] = true
		}
	}
	for _, item := range listed {
		if !strings.EqualFold(item.Type, "episode") {
			continue
		}
		seriesID := strings.TrimSpace(item.SeriesID)
		if seriesID == "" {
			return nil
		}
		if !item.UserData.Played && item.UserData.PlaybackPositionTicks == 0 {
			row.IncludesNextUp = true
		}
		if row.SourceSeriesIDs[seriesID] {
			continue
		}
		row.SourceSeriesIDs[seriesID] = true
		series, ok := seriesMeta[seriesID]
		if !ok {
			continue
		}
		row.Series = append(row.Series, Record{
			ExternalID: seriesID,
			Kind:       KindSeries,
			Title:      series.Name,
			Year:       series.ProductionYear,
			IMDbID:     providerID(series.ProviderIDs, "imdb"),
			TMDBID:     providerID(series.ProviderIDs, "tmdb"),
			TVDBID:     providerID(series.ProviderIDs, "tvdb"),
			// As for favorite series: Emby's primary TMDB identity beats a
			// stale secondary TVDB ID.
			PreferTMDB: true,
		})
	}
	return row
}

func normalizeEmbyItem(item embyItem, series embyItem) Record {
	record := Record{
		ExternalID:      item.ID,
		Title:           item.Name,
		Year:            item.ProductionYear,
		Played:          item.UserData.Played,
		PlayCount:       item.UserData.PlayCount,
		PositionSeconds: ticksToSeconds(item.UserData.PlaybackPositionTicks),
		DurationSeconds: ticksToSeconds(item.RunTimeTicks),
	}
	if item.UserData.LastPlayedDate != nil {
		record.LastPlayedAt = item.UserData.LastPlayedDate
		record.UpdatedAt = item.UserData.LastPlayedDate.UTC()
	}

	record.IMDbID = providerID(item.ProviderIDs, "imdb")
	record.TMDBID = providerID(item.ProviderIDs, "tmdb")
	record.TVDBID = providerID(item.ProviderIDs, "tvdb")

	switch strings.ToLower(item.Type) {
	case "movie":
		record.Kind = KindMovie
	case "episode":
		record.Kind = KindEpisode
		record.SourceSeriesID = strings.TrimSpace(item.SeriesID)
		record.SeriesTitle = item.SeriesName
		if record.SeriesTitle == "" {
			record.SeriesTitle = series.Name
		}
		record.SeriesIMDbID = providerID(series.ProviderIDs, "imdb")
		record.SeriesTMDBID = providerID(series.ProviderIDs, "tmdb")
		record.SeriesTVDBID = providerID(series.ProviderIDs, "tvdb")
		record.SeriesYear = series.ProductionYear
		record.SeasonNumber = item.ParentIndexNumber
		record.EpisodeNumber = item.IndexNumber
	default:
		record.Kind = strings.ToLower(item.Type)
	}
	return record
}

func mergeRecords(a, b Record) Record {
	result := a
	if b.Played {
		result.Played = true
	}
	if b.Favorite {
		result.Favorite = true
	}
	if b.PreferTMDB {
		result.PreferTMDB = true
	}
	if b.HiddenFromResume {
		result.HiddenFromResume = true
	}
	result.FavoriteOnly = result.FavoriteOnly && b.FavoriteOnly
	if b.PlayCount > result.PlayCount {
		result.PlayCount = b.PlayCount
	}
	if b.PositionSeconds > result.PositionSeconds {
		result.PositionSeconds = b.PositionSeconds
	}
	if b.DurationSeconds > 0 {
		result.DurationSeconds = b.DurationSeconds
	}
	if b.LastPlayedAt != nil && (result.LastPlayedAt == nil || b.LastPlayedAt.After(*result.LastPlayedAt)) {
		result.LastPlayedAt = b.LastPlayedAt
	}
	if b.UpdatedAt.After(result.UpdatedAt) {
		result.UpdatedAt = b.UpdatedAt
	}
	if result.Title == "" {
		result.Title = b.Title
	}
	if result.Year == 0 {
		result.Year = b.Year
	}
	if result.Kind == "" {
		result.Kind = b.Kind
	}
	if result.IMDbID == "" {
		result.IMDbID = b.IMDbID
	}
	if result.TMDBID == "" {
		result.TMDBID = b.TMDBID
	}
	if result.TVDBID == "" {
		result.TVDBID = b.TVDBID
	}
	if result.SeriesTitle == "" {
		result.SeriesTitle = b.SeriesTitle
	}
	if result.SeriesYear == 0 {
		result.SeriesYear = b.SeriesYear
	}
	if result.SeriesIMDbID == "" {
		result.SeriesIMDbID = b.SeriesIMDbID
	}
	if result.SeriesTMDBID == "" {
		result.SeriesTMDBID = b.SeriesTMDBID
	}
	if result.SeriesTVDBID == "" {
		result.SeriesTVDBID = b.SeriesTVDBID
	}
	if result.SeasonNumber == 0 {
		result.SeasonNumber = b.SeasonNumber
	}
	if result.EpisodeNumber == 0 {
		result.EpisodeNumber = b.EpisodeNumber
	}
	return result
}

func providerID(ids map[string]string, key string) string {
	for k, v := range ids {
		if strings.EqualFold(k, key) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func ticksToSeconds(ticks int64) float64 {
	if ticks <= 0 {
		return 0
	}
	return float64(ticks) / 10_000_000
}
