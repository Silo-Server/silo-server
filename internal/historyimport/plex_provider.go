package historyimport

import (
	"context"
	"fmt"
)

type PlexServerProvider struct {
	client       *PlexClient
	baseURL      string
	token        string
	accountToken string
}

func NewPlexServerProvider(client *PlexClient, baseURL, token string) *PlexServerProvider {
	return &PlexServerProvider{client: client, baseURL: baseURL, token: token}
}

// WithAccountToken enables account-level fetches (the watchlist). Empty
// disables them: server-token-only imports still work, minus the watchlist.
func (p *PlexServerProvider) WithAccountToken(token string) *PlexServerProvider {
	p.accountToken = token
	return p
}

// Plex library section types that hold watch state.
const (
	plexSectionMovie = "movie"
	plexSectionShow  = "show"
)

// plexSectionMediaTypes maps a library section type to the PMS media type
// whose items carry watch state: movies, and episodes rather than shows.
var plexSectionMediaTypes = map[string]struct {
	mediaType int
	noun      string
}{
	plexSectionMovie: {mediaType: 1, noun: "movies"},
	plexSectionShow:  {mediaType: 4, noun: "episodes"},
}

func (p *PlexServerProvider) Fetch(ctx context.Context) ([]Record, []string, error) {
	sections, err := p.client.FetchLibrarySections(ctx, p.baseURL, p.token)
	if err != nil {
		return nil, nil, err
	}

	var allItems []PlexItem
	var warnings []string

	// Watched items include titles marked watched without playback, and whole
	// shows or seasons marked watched, which Plex records on every episode.
	// Plex counts started-but-unfinished items as unwatched, so they come from
	// their own listing. A rewatch appears in both and imports as played.
	for _, section := range sections {
		kind, ok := plexSectionMediaTypes[section.Type]
		if !ok {
			continue
		}
		watched, err := p.client.FetchWatchedItems(ctx, p.baseURL, p.token, section.Key, kind.mediaType)
		if err != nil {
			if ctx.Err() != nil {
				return nil, warnings, ctx.Err()
			}
			warnings = append(warnings, fmt.Sprintf("failed to fetch watched %s from section %q: %v", kind.noun, section.Title, err))
		}
		allItems = append(allItems, watched...)
		inProgress, err := p.client.FetchInProgressItems(ctx, p.baseURL, p.token, section.Key, kind.mediaType)
		if err != nil {
			if ctx.Err() != nil {
				return nil, warnings, ctx.Err()
			}
			warnings = append(warnings, fmt.Sprintf("failed to fetch in-progress %s from section %q: %v", kind.noun, section.Title, err))
		}
		allItems = append(allItems, inProgress...)
	}

	var seriesKeys []string
	for _, item := range allItems {
		if item.Type == KindEpisode {
			seriesKeys = append(seriesKeys, item.GrandparentRatingKey)
		}
	}
	seriesMeta, err := fetchPlexSeriesMetadata(ctx, p.client, p.baseURL, p.token, seriesKeys, &warnings)
	if err != nil {
		return nil, warnings, err
	}

	merged := make(map[string]Record, len(allItems))
	for _, item := range allItems {
		record := NormalizePlexItem(item, seriesMeta[item.GrandparentRatingKey])
		if !record.Played && record.PositionSeconds <= 0 {
			continue
		}
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
	// The account watchlist rides along with the history import. Best
	// effort: a watchlist fetch failure downgrades to a warning so the
	// watch-history import still completes (issue #245).
	if p.accountToken != "" {
		items, watchlistWarnings, err := p.client.FetchWatchlist(ctx, p.accountToken)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("watchlist fetch failed: %v", err))
		} else {
			warnings = append(warnings, watchlistWarnings...)
			for _, item := range items {
				records = append(records, NormalizePlexWatchlistItem(item))
			}
		}
	}

	return records, warnings, nil
}
