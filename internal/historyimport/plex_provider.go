package historyimport

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

type PlexServerProvider struct {
	client       *PlexClient
	baseURLs     []string
	baseURL      string
	token        string
	accountToken string
}

// NewPlexServerProvider takes the advertised connections in preference order,
// already trimmed, deduped, and capped by plexBaseURLCandidates — resolvePlexAuth
// owns that normalization because it also decides, from the resulting list,
// whether the request names a usable server at all. The slice is copied so a
// caller's later mutation cannot reorder a run's fallbacks mid-flight.
func NewPlexServerProvider(client *PlexClient, baseURLs []string, token string) *PlexServerProvider {
	return &PlexServerProvider{
		client:   client,
		baseURLs: append([]string(nil), baseURLs...),
		token:    token,
	}
}

// WithAccountToken enables account-level fetches (the watchlist). Empty
// disables them: server-token-only imports still work, minus the watchlist.
func (p *PlexServerProvider) WithAccountToken(token string) *PlexServerProvider {
	p.accountToken = token
	return p
}

// WithPublicConnectionsOnly prevents profile OAuth imports from reaching LAN,
// loopback, link-local, and other special-use destinations. Admin-configured
// saved sources intentionally keep the unrestricted client.
func (p *PlexServerProvider) WithPublicConnectionsOnly() *PlexServerProvider {
	p.client = p.client.publicDestinationsOnly()
	return p
}

func (p *PlexServerProvider) Fetch(ctx context.Context) ([]Record, []string, error) {
	sections, err := p.fetchLibrarySections(ctx)
	if err != nil {
		return nil, nil, err
	}

	var allItems []PlexItem
	var warnings []string

	for _, section := range sections {
		switch section.Type {
		case "movie":
			items, err := p.client.FetchWatchedItems(ctx, p.baseURL, p.token, section.Key, 1)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("failed to fetch movies from section %q: %v", section.Title, err))
				continue
			}
			allItems = append(allItems, items...)
		case "show":
			items, err := p.client.FetchWatchedItems(ctx, p.baseURL, p.token, section.Key, 4)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("failed to fetch episodes from section %q: %v", section.Title, err))
				continue
			}
			allItems = append(allItems, items...)
		}
	}

	onDeck, err := p.client.FetchOnDeck(ctx, p.baseURL, p.token)
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("failed to fetch on-deck items: %v", err))
	} else {
		allItems = append(allItems, onDeck...)
	}

	seriesMeta := p.fetchSeriesMetadata(ctx, allItems, &warnings)

	merged := make(map[string]Record, len(allItems))
	for _, item := range allItems {
		record := NormalizePlexItem(item, seriesMeta[item.GrandparentRatingKey])
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

type plexConnectionProbe struct {
	index    int
	baseURL  string
	sections []plexLibrarySection
	err      error
}

func (p *PlexServerProvider) fetchLibrarySections(ctx context.Context) ([]plexLibrarySection, error) {
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
		if result.err == nil {
			p.baseURL = result.baseURL
			cancelProbes()
			return result.sections, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		connectionErrors[result.index] = fmt.Errorf("connection %d: %w", result.index+1, result.err)
	}
	return nil, fmt.Errorf("all advertised Plex connections failed: %w", errors.Join(connectionErrors...))
}

func (p *PlexServerProvider) fetchSeriesMetadata(ctx context.Context, items []PlexItem, warnings *[]string) map[string]*PlexItem {
	seen := make(map[string]struct{})
	var seriesKeys []string
	for _, item := range items {
		if item.Type != "episode" || item.GrandparentRatingKey == "" {
			continue
		}
		if _, ok := seen[item.GrandparentRatingKey]; ok {
			continue
		}
		seen[item.GrandparentRatingKey] = struct{}{}
		seriesKeys = append(seriesKeys, item.GrandparentRatingKey)
	}

	result := make(map[string]*PlexItem, len(seriesKeys))
	for _, key := range seriesKeys {
		meta, err := p.client.FetchMetadata(ctx, p.baseURL, p.token, key)
		if err != nil {
			slog.WarnContext(ctx, "plex history import: failed to fetch series metadata", "component", "historyimport", "rating_key", key, "error", err)
			*warnings = append(*warnings, fmt.Sprintf("failed to fetch series metadata for %s: %v", key, err))
			continue
		}
		if meta != nil {
			result[key] = meta
		}
	}
	return result
}
