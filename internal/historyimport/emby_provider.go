package historyimport

import (
	"context"
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

func (p *EmbyProvider) Fetch(ctx context.Context) ([]Record, []string, error) {
	playedItems, err := p.client.FetchItems(ctx, p.auth, "IsPlayed")
	if err != nil {
		return nil, nil, err
	}
	resumableItems, err := p.client.FetchItems(ctx, p.auth, "IsResumable")
	if err != nil {
		return nil, nil, err
	}
	favoriteItems, err := p.client.FetchFavoriteItems(ctx, p.auth)
	var warnings []string
	if err != nil {
		warnings = append(warnings, "fetching Emby favorites: "+err.Error())
		favoriteItems = nil
	}

	seriesMeta, err := p.fetchSeriesMetadata(ctx, append(playedItems, resumableItems...))
	if err != nil {
		return nil, nil, err
	}

	merged := make(map[string]Record, len(playedItems)+len(resumableItems)+len(favoriteItems))
	for _, item := range append(playedItems, resumableItems...) {
		record := normalizeEmbyItem(item, seriesMeta[item.SeriesID])
		if record.ExternalID == "" {
			record.ExternalID = item.ID
		}
		existing, ok := merged[record.ExternalID]
		if !ok {
			merged[record.ExternalID] = record
			continue
		}
		merged[record.ExternalID] = mergeRecords(existing, record)
	}
	for _, item := range favoriteItems {
		record := normalizeEmbyItem(item, embyItem{})
		record.Favorite = true
		record.FavoriteOnly = true
		record.PreferTMDB = true
		existing, ok := merged[record.ExternalID]
		if !ok {
			merged[record.ExternalID] = record
			continue
		}
		merged[record.ExternalID] = mergeRecords(existing, record)
	}

	records := make([]Record, 0, len(merged))
	for _, record := range merged {
		records = append(records, record)
	}
	return records, warnings, nil
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
