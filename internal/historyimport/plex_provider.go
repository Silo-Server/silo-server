package historyimport

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

type PlexServerProvider struct {
	client   *PlexClient
	baseURLs []string
	// baseURL is the connection that answered first, fixed for the rest of
	// the run once fetchLibrarySections picks it.
	baseURL      string
	token        string
	accountToken string
}

// NewPlexServerProvider takes the advertised connections in preference order,
// already normalized by plexBaseURLCandidates. The slice is copied so a
// caller's later mutation cannot reorder a run's fallbacks mid-flight.
func NewPlexServerProvider(client *PlexClient, baseURLs []string, token string) *PlexServerProvider {
	return &PlexServerProvider{client: client, baseURLs: slices.Clone(baseURLs), token: token}
}

// WithAccountToken enables account-level fetches (the watchlist). Empty
// disables them: server-token-only imports still work, minus the watchlist.
func (p *PlexServerProvider) WithAccountToken(token string) *PlexServerProvider {
	p.accountToken = token
	return p
}

type plexConnectionProbe struct {
	index    int
	baseURL  string
	sections []struct{ Key, Type, Title string }
	err      error
}

// fetchLibrarySections races the advertised connections and keeps the first
// that returns a Plex library listing. Racing rather than trying them in turn
// matters because the common failure is a connection that hangs: a serial walk
// would spend the whole run budget on it before reaching a reachable address.
func (p *PlexServerProvider) fetchLibrarySections(ctx context.Context) ([]struct{ Key, Type, Title string }, error) {
	if len(p.baseURLs) == 0 {
		return nil, fmt.Errorf("selected Plex server has no usable address")
	}

	probeCtx, cancelProbes := context.WithCancel(ctx)
	defer cancelProbes()
	results := make(chan plexConnectionProbe, len(p.baseURLs))
	for i, baseURL := range p.baseURLs {
		go func() {
			sections, err := p.client.FetchLibrarySections(probeCtx, baseURL, p.token)
			results <- plexConnectionProbe{index: i, baseURL: baseURL, sections: sections, err: err}
		}()
	}

	connectionErrors := make([]error, len(p.baseURLs))
	for range p.baseURLs {
		var result plexConnectionProbe
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case result = <-results:
		}
		// A probe can finish in the same instant the run is canceled; the
		// cancellation wins so a stopped run never starts importing.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if result.err == nil {
			p.baseURL = result.baseURL
			return result.sections, nil
		}
		connectionErrors[result.index] = fmt.Errorf("connection %d: %w", result.index+1, result.err)
	}
	return nil, fmt.Errorf("all advertised Plex connections failed: %w", errors.Join(connectionErrors...))
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
	sections, err := p.fetchLibrarySections(ctx)
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
