package historyimport

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
)

type Provider interface {
	Fetch(ctx context.Context) ([]Record, []string, error)
}

type EmbyProvider struct {
	client *EmbyClient
	auth   embyLocalAuth
}

func NewEmbyProvider(client *EmbyClient, auth embyLocalAuth) *EmbyProvider {
	return &EmbyProvider{client: client, auth: auth}
}

// maxEmbyEpisodeRange bounds how many episodes one multi-episode file expands
// to; a wider range is imported as its first episode only.
const maxEmbyEpisodeRange = 10

func (p *EmbyProvider) Fetch(ctx context.Context) ([]Record, []string, error) {
	playedItems, err := p.client.FetchItems(ctx, p.auth, "IsPlayed")
	if err != nil {
		return nil, nil, err
	}
	resumableItems, err := p.client.FetchItems(ctx, p.auth, "IsResumable")
	if err != nil {
		return nil, nil, err
	}
	var warnings []string
	// Warnings store fixed text: v1 returns them verbatim, and upstream errors
	// can carry the server's response body. The error itself is logged.
	favoriteItems, err := p.client.FetchFavoriteItems(ctx, p.auth)
	if err != nil {
		slog.WarnContext(ctx, "emby history import: favorites unavailable", "component", "historyimport", "error", err)
		warnings = append(warnings, warnEmbyFavoritesUnavailable)
		favoriteItems = nil
	}
	// Silo has no season favorites, so report them rather than drop them.
	seasonFavorites := 0
	favoriteItems = slices.DeleteFunc(favoriteItems, func(item embyItem) bool {
		isSeason := strings.EqualFold(item.Type, "season")
		if isSeason {
			seasonFavorites++
		}
		return isSeason
	})
	if seasonFavorites > 0 {
		warnings = append(warnings, fmt.Sprintf(warnEmbySeasonFavorites, seasonFavorites))
	}

	watchedItems := slices.Concat(playedItems, resumableItems)
	seriesMeta, err := p.fetchSeriesMetadata(ctx, slices.Concat(watchedItems, favoriteItems))
	if err != nil {
		// Episodes carrying their own provider IDs still match without it.
		slog.WarnContext(ctx, "emby history import: series metadata unavailable", "component", "historyimport", "error", err)
		warnings = append(warnings, warnEmbySeriesUnavailable)
		seriesMeta = map[string]embyItem{}
	}

	merged := make(map[string]Record, len(watchedItems)+len(favoriteItems))
	add := func(record Record) {
		if existing, ok := merged[record.ExternalID]; ok {
			record = mergeRecords(existing, record)
		}
		merged[record.ExternalID] = record
	}
	for _, item := range watchedItems {
		for _, record := range embyWatchedRecords(item, seriesMeta[item.SeriesID]) {
			add(record)
		}
	}
	for _, item := range favoriteItems {
		record := normalizeEmbyItem(item, seriesMeta[item.SeriesID])
		record.Favorite = true
		record.FavoriteOnly = true
		record.PreferTMDB = true
		// Watch records own the runtime; a favorite's full file runtime must
		// not replace a multi-episode file's per-episode share.
		record.DurationSeconds = 0
		add(record)
	}

	records := make([]Record, 0, len(merged))
	for _, record := range merged {
		records = append(records, record)
	}
	return records, warnings, nil
}

// embyWatchedRecords expands a played multi-episode file (S01E01-E02) into
// one record per episode, splitting its runtime evenly. Emby's provider IDs
// describe the first episode only, so the others match by series identity
// and number. A partly watched file stays one record: its position cannot be
// attributed to a single episode.
func embyWatchedRecords(item embyItem, series embyItem) []Record {
	record := normalizeEmbyItem(item, series)
	count := item.IndexNumberEnd - item.IndexNumber + 1
	if record.Kind != KindEpisode || !record.Played || count < 2 || count > maxEmbyEpisodeRange {
		return []Record{record}
	}
	record.DurationSeconds /= float64(count)
	records := []Record{record}
	for episode := item.IndexNumber + 1; episode <= item.IndexNumberEnd; episode++ {
		next := record
		next.ExternalID = fmt.Sprintf("%s#E%d", item.ID, episode)
		next.EpisodeNumber = episode
		next.IMDbID, next.TMDBID, next.TVDBID = "", "", ""
		records = append(records, next)
	}
	return records
}

func (p *EmbyProvider) fetchSeriesMetadata(ctx context.Context, items []embyItem) (map[string]embyItem, error) {
	seriesIDs := make([]string, 0)
	seen := make(map[string]struct{})
	for _, item := range items {
		if strings.ToLower(item.Type) != "episode" || strings.TrimSpace(item.SeriesID) == "" {
			continue
		}
		if _, ok := seen[item.SeriesID]; ok {
			continue
		}
		seen[item.SeriesID] = struct{}{}
		seriesIDs = append(seriesIDs, item.SeriesID)
	}
	if len(seriesIDs) == 0 {
		return map[string]embyItem{}, nil
	}
	seriesItems, err := p.client.FetchItemsByIDs(ctx, p.auth, seriesIDs, "Series")
	if err != nil {
		return nil, err
	}
	result := make(map[string]embyItem, len(seriesItems))
	for _, item := range seriesItems {
		result[item.ID] = item
	}
	return result, nil
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
