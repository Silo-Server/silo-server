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

// Stages of a Plex server listing: each section's watched pages, then what is
// only in progress in that section, and last the account watchlist.
const (
	plexStageWatched   = "watched"
	plexStageRemainder = "in-progress"
	plexStageWatchlist = "watchlist"
)

// Stream reads every section's watched pages, merging each section's held
// in-progress list into them, then emits what was only in progress. A rewatch
// appears in both listings and imports as played.
func (p *PlexServerProvider) Stream(ctx context.Context, from *StreamPosition, sink RecordSink) error {
	sink = markSinkErrors(sink)
	sections, err := p.fetchLibrarySections(ctx)
	if err != nil {
		return err
	}
	series := newPlexSeriesLookup(p.client, p.baseURL, p.token)
	if from == nil || from.Stage != plexStageWatchlist {
		if err := p.streamSections(ctx, sections, series, from, sink); err != nil {
			return err
		}
	}
	if warning := series.warning(); warning != "" {
		if err := sink(RecordBatch{Warnings: []string{warning}}); err != nil {
			return err
		}
	}
	return p.streamWatchlist(ctx, sink)
}

func (p *PlexServerProvider) streamSections(ctx context.Context, sections []struct{ Key, Type, Title string }, series *plexSeriesLookup, from *StreamPosition, sink RecordSink) error {
	first := 0
	if from != nil {
		// A section that is gone restarts the listing; re-applying is safe.
		first = max(0, slices.IndexFunc(sections, func(s struct{ Key, Type, Title string }) bool { return s.Key == from.Section }))
	}
	for i, section := range sections[first:] {
		kind, ok := plexSectionMediaTypes[section.Type]
		if !ok {
			continue
		}
		var resume *StreamPosition
		if i == 0 && from != nil && from.Section == section.Key {
			resume = from
		}
		if err := p.streamSection(ctx, section.Key, section.Title, kind.mediaType, kind.noun, series, resume, sink); err != nil {
			return err
		}
	}
	return nil
}

func (p *PlexServerProvider) streamSection(ctx context.Context, key, title string, mediaType int, noun string, series *plexSeriesLookup, from *StreamPosition, sink RecordSink) error {
	unavailable := func(listing string, err error) error {
		// A source over the size limits fails the run rather than importing
		// without the section.
		if err == nil || isFatalListingError(ctx, err) {
			return err
		}
		return sink(RecordBatch{Warnings: []string{fmt.Sprintf(warnPlexSectionUnavailable, listing, noun, title, err)}})
	}

	items, err := p.client.fetchInProgressItems(ctx, p.baseURL, p.token, key, mediaType)
	if err := unavailable("in-progress", err); err != nil {
		return err
	}
	held := holdItems(items, func(item PlexItem) string { return item.RatingKey })
	defer held.release(ctx)

	if from == nil || from.Stage == plexStageWatched {
		err := p.streamWatchedPages(ctx, key, mediaType, stageOffset(from, plexStageWatched), held, series, sink)
		if err := unavailable("watched", err); err != nil {
			return err
		}
	}

	for page := range slices.Chunk(held.remaining(), plexPageSize) {
		records, err := p.normalizePage(ctx, page, nil, series)
		if err != nil {
			return err
		}
		if err := sink(RecordBatch{Records: records, Resume: StreamPosition{Stage: plexStageRemainder, Section: key}}); err != nil {
			return err
		}
	}
	return nil
}

func (p *PlexServerProvider) streamWatchedPages(ctx context.Context, key string, mediaType, offset int, held *heldItems[PlexItem], series *plexSeriesLookup, sink RecordSink) error {
	return p.client.forEachSectionPage(ctx, p.baseURL, p.token, key, mediaType, plexFilterWatched, offset, func(page []PlexItem, start int) error {
		records, err := p.normalizePage(ctx, page, held, series)
		if err != nil {
			return err
		}
		return sink(RecordBatch{Records: records, Resume: StreamPosition{Stage: plexStageWatched, Section: key, Offset: start}})
	})
}

// normalizePage turns one page into records, merging held in-progress state.
func (p *PlexServerProvider) normalizePage(ctx context.Context, page []PlexItem, held *heldItems[PlexItem], series *plexSeriesLookup) ([]Record, error) {
	keys := make([]string, 0, len(page))
	for _, item := range page {
		if item.Type == KindEpisode {
			keys = append(keys, item.GrandparentRatingKey)
		}
	}
	meta, err := series.resolve(ctx, keys)
	if err != nil {
		return nil, err
	}
	records := make([]Record, 0, len(page))
	for _, item := range page {
		record := NormalizePlexItem(item, meta[item.GrandparentRatingKey])
		if progress, ok := held.take(item.RatingKey); ok {
			record = mergeRecords(record, NormalizePlexItem(progress, meta[progress.GrandparentRatingKey]))
		}
		if record.Played || record.PositionSeconds > 0 {
			records = append(records, record)
		}
	}
	return records, nil
}

// streamWatchlist delivers the account watchlist, which rides along with the
// history import. Best effort: a fetch failure downgrades to a warning so the
// watch-history import still completes (issue #245).
func (p *PlexServerProvider) streamWatchlist(ctx context.Context, sink RecordSink) error {
	if p.accountToken == "" {
		return nil
	}
	items, warnings, err := p.client.FetchWatchlist(ctx, p.accountToken)
	if errors.Is(err, ErrUpstreamResponseTooLarge) {
		return err
	}
	if err != nil {
		return sink(RecordBatch{Warnings: []string{warnPlexWatchlistUnavailablePrefix + err.Error()}})
	}
	records := make([]Record, 0, len(items))
	for _, item := range items {
		records = append(records, NormalizePlexWatchlistItem(item))
	}
	return sink(RecordBatch{Records: records, Warnings: warnings, Resume: StreamPosition{Stage: plexStageWatchlist}})
}
