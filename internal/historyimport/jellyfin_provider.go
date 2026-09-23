package historyimport

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"strings"
)

type JellyfinProvider struct {
	client *JellyfinClient
	auth   jellyfinLocalAuth
}

func NewJellyfinProvider(client *JellyfinClient, auth jellyfinLocalAuth) *JellyfinProvider {
	return &JellyfinProvider{client: client, auth: auth}
}

// Fetch reads played and resumable movies and episodes plus favorite movies,
// shows, and episodes. Jellyfin records a whole-show or whole-season "mark
// played" on each episode, so episode leaves carry those markers too.
func (p *JellyfinProvider) Fetch(ctx context.Context) ([]Record, []string, error) {
	played, err := p.client.FetchItems(ctx, p.auth, "IsPlayed", jellyfinPlayableItemTypes)
	if err != nil {
		return nil, nil, err
	}
	resumable, err := p.client.FetchResumableItems(ctx, p.auth)
	if err != nil {
		return nil, nil, err
	}
	var warnings []string
	// Warnings store fixed text: v1 returns them verbatim, and upstream errors
	// can carry the server's response body. The error itself is logged.
	favorites, err := p.client.FetchItems(ctx, p.auth, "IsFavorite", jellyfinFavoriteItemTypes)
	if err != nil {
		slog.WarnContext(ctx, "jellyfin history import: favorites unavailable", "component", "historyimport", "error", err)
		warnings = append(warnings, warnJellyfinFavoritesUnavailable)
		favorites = nil
	}
	watched := slices.Concat(played, resumable)
	seriesMeta, err := p.fetchSeriesMetadata(ctx, watched)
	if err != nil {
		return nil, nil, err
	}
	// Series that only favorites need are looked up separately so that, like
	// the favorites query, a failure there cannot discard the watch history.
	// Favorite episodes then keep only their own provider IDs.
	favoriteSeries := slices.DeleteFunc(slices.Clone(favorites), func(item jellyfinItem) bool {
		_, ok := seriesMeta[item.SeriesID]
		return ok
	})
	if extra, err := p.fetchSeriesMetadata(ctx, favoriteSeries); err != nil {
		slog.WarnContext(ctx, "jellyfin history import: favorite series metadata unavailable", "component", "historyimport", "error", err)
		warnings = append(warnings, warnJellyfinFavoriteSeriesUnavailable)
	} else {
		maps.Copy(seriesMeta, extra)
	}
	merged := map[string]Record{}
	add := func(record Record) {
		if existing, ok := merged[record.ExternalID]; ok {
			merged[record.ExternalID] = mergeRecords(existing, record)
		} else {
			merged[record.ExternalID] = record
		}
	}
	for _, item := range watched {
		add(normalizeJellyfinItem(item, seriesMeta[item.SeriesID]))
	}
	for _, item := range favorites {
		record := normalizeJellyfinItem(item, seriesMeta[item.SeriesID])
		record.Favorite = true
		record.FavoriteOnly = true
		add(record)
	}
	records := make([]Record, 0, len(merged))
	for _, record := range merged {
		records = append(records, record)
	}
	return records, warnings, nil
}

func (p *JellyfinProvider) fetchSeriesMetadata(ctx context.Context, items []jellyfinItem) (map[string]jellyfinItem, error) {
	seen := map[string]struct{}{}
	ids := []string{}
	for _, item := range items {
		if strings.ToLower(item.Type) != "episode" || strings.TrimSpace(item.SeriesID) == "" {
			continue
		}
		if _, ok := seen[item.SeriesID]; ok {
			continue
		}
		seen[item.SeriesID] = struct{}{}
		ids = append(ids, item.SeriesID)
	}
	if len(ids) == 0 {
		return map[string]jellyfinItem{}, nil
	}
	seriesItems, err := p.client.FetchItemsByIDs(ctx, p.auth, ids, "Series")
	if err != nil {
		return nil, err
	}
	result := make(map[string]jellyfinItem, len(seriesItems))
	for _, item := range seriesItems {
		result[item.ID] = item
	}
	return result, nil
}

func normalizeJellyfinItem(item jellyfinItem, series jellyfinItem) Record {
	// A missing LastPlayedDate leaves UpdatedAt zero, which the import treats
	// as older than any local activity instead of as "now".
	record := Record{ExternalID: item.ID, Title: item.Name, Year: item.ProductionYear, Played: item.UserData.Played, PlayCount: item.UserData.PlayCount, PositionSeconds: ticksToSeconds(item.UserData.PlaybackPositionTicks), DurationSeconds: ticksToSeconds(item.RunTimeTicks), Favorite: item.UserData.IsFavorite}
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
