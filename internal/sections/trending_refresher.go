package sections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/sections/recipes"
)

// trendingFetchCap is the over-fetch size for each refresh. Library-only
// matching drops globally-trending titles the server does not own, so we fetch
// well beyond any section's display limit and store the matched, ordered list.
const trendingFetchCap = 200

// trendingSectionConfigLister enumerates enabled trending_discover section
// configs. Satisfied by *Repository.
type trendingSectionConfigLister interface {
	ListTrendingDiscoverConfigs(ctx context.Context) ([]json.RawMessage, error)
}

// trendingSnapshotStore is the write side of the snapshot table. Satisfied by
// *TrendingSnapshotRepository.
type trendingSnapshotStore interface {
	SaveSuccess(ctx context.Context, source, window string, contentIDs []string, entryCount int, status string, at time.Time) error
	RecordAttempt(ctx context.Context, source, window, status, message string, at time.Time) error
}

// trendingExternalIDResolver resolves external IDs to library content IDs.
// Satisfied by *catalog.ItemRepository.
type trendingExternalIDResolver interface {
	GetByExternalIDs(ctx context.Context, batch catalog.ExternalIDBatch, itemType string) (*catalog.ExternalIDLookup, error)
}

// TrendingRefresher fetches external global trending (TMDB/Trakt), resolves it
// to library content IDs, and persists one snapshot per canonical
// (source, window). It is driven by a TaskManager task on an interval.
type TrendingRefresher struct {
	Sections      trendingSectionConfigLister
	Snapshots     trendingSnapshotStore
	Resolver      trendingExternalIDResolver
	TMDBTrending  catalog.TMDBCollectionFetcher
	TraktTrending catalog.TraktCollectionFetcher

	// Clock defaults to recipes.RealClock{}. Tests inject recipes.FixedClock.
	Clock  recipes.Clock
	logger *slog.Logger
}

// NewTrendingRefresher creates a refresher with real-clock and default logger.
func NewTrendingRefresher(
	sectionsRepo trendingSectionConfigLister,
	snapshots trendingSnapshotStore,
	resolver trendingExternalIDResolver,
	tmdb catalog.TMDBCollectionFetcher,
	trakt catalog.TraktCollectionFetcher,
) *TrendingRefresher {
	return &TrendingRefresher{
		Sections:      sectionsRepo,
		Snapshots:     snapshots,
		Resolver:      resolver,
		TMDBTrending:  tmdb,
		TraktTrending: trakt,
		Clock:         recipes.RealClock{},
		logger:        slog.Default(),
	}
}

func (r *TrendingRefresher) now() time.Time {
	if r.Clock != nil {
		return r.Clock.Now()
	}
	return time.Now()
}

func (r *TrendingRefresher) log() *slog.Logger {
	if r.logger != nil {
		return r.logger
	}
	return slog.Default()
}

// TrendingRefreshResult is the JSON summary attached to the task execution.
type TrendingRefreshResult struct {
	Combos    int `json:"combos"`
	Refreshed int `json:"refreshed"`
	Empty     int `json:"empty"`
	Failed    int `json:"failed"`
}

type trendingCombo struct {
	source string
	window string
}

// distinctTrendingCombos parses section configs and returns the deduplicated set
// of canonical (source, window) pairs that need a snapshot.
func distinctTrendingCombos(configs []json.RawMessage) []trendingCombo {
	seen := make(map[trendingCombo]struct{}, len(configs))
	out := make([]trendingCombo, 0, len(configs))
	for _, raw := range configs {
		var p recipes.TrendingDiscoverParams
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &p)
		}
		source, window := canonicalTrendingKey(p.Source, p.Window)
		c := trendingCombo{source: source, window: window}
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	return out
}

// RunOnce refreshes every (source, window) used by an enabled trending_discover
// section. Per-combo failures are recorded and never abort the others. The JSON
// summary is suitable for task result data.
func (r *TrendingRefresher) RunOnce(ctx context.Context) (json.RawMessage, error) {
	configs, err := r.Sections.ListTrendingDiscoverConfigs(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing trending_discover sections: %w", err)
	}

	combos := distinctTrendingCombos(configs)
	result := TrendingRefreshResult{Combos: len(combos)}
	for _, c := range combos {
		switch r.refreshCombo(ctx, c.source, c.window) {
		case "ok":
			result.Refreshed++
		case "empty":
			result.Empty++
		default:
			result.Failed++
		}
	}

	data, _ := json.Marshal(result)
	return data, nil
}

// refreshCombo refreshes a single canonical (source, window) and returns its
// outcome: "ok", "empty", or "error". A fetch failure or an unconfigured/empty
// provider preserves the last-good content list (RecordAttempt). When the
// provider returns entries, the list is replaced even if nothing matched the
// catalog ("empty" status with an empty list) — that genuinely reflects current
// trending having no library matches.
func (r *TrendingRefresher) refreshCombo(ctx context.Context, source, window string) string {
	now := r.now()

	entries, err := r.fetchEntries(ctx, source, window, trendingFetchCap)
	if err != nil {
		r.log().Error("trending refresh: fetch failed", "source", source, "window", window, "error", err)
		_ = r.Snapshots.RecordAttempt(ctx, source, window, "error", err.Error(), now)
		return "error"
	}
	if len(entries) == 0 {
		// Provider unconfigured or returned nothing: keep last-good, mark empty.
		_ = r.Snapshots.RecordAttempt(ctx, source, window, "empty", "", now)
		return "empty"
	}

	contentIDs, err := r.resolveIDs(ctx, entries)
	if err != nil {
		r.log().Error("trending refresh: resolve failed", "source", source, "window", window, "error", err)
		_ = r.Snapshots.RecordAttempt(ctx, source, window, "error", err.Error(), now)
		return "error"
	}

	status := "ok"
	if len(contentIDs) == 0 {
		status = "empty"
	}
	if err := r.Snapshots.SaveSuccess(ctx, source, window, contentIDs, len(entries), status, now); err != nil {
		r.log().Error("trending refresh: save failed", "source", source, "window", window, "error", err)
		return "error"
	}
	return status
}

// fetchEntries pulls the raw trending list from the configured provider. A
// nil/unconfigured provider yields an empty list (no error).
func (r *TrendingRefresher) fetchEntries(ctx context.Context, source, window string, fetchLimit int) ([]trendingDiscoverEntry, error) {
	if source == sourceTrakt {
		if r.TraktTrending == nil {
			return nil, nil
		}
		// Trakt has no mixed endpoint; fetch movies + shows separately. Treat ANY
		// failure as fatal so a partial result never overwrites the last-good
		// snapshot with one media type missing.
		movies, movieErr := r.TraktTrending.GetCollectionPreset(ctx, "trending", "movie", fetchLimit, "")
		shows, showErr := r.TraktTrending.GetCollectionPreset(ctx, "trending", "tv", fetchLimit, "")
		if movieErr != nil || showErr != nil {
			return nil, fmt.Errorf("trakt trending: %w", errors.Join(movieErr, showErr))
		}
		// Interleave by rank so the mixed row actually shows both movies and
		// series; plain concatenation would bury all series past the display
		// limit whenever enough movies match the library.
		return interleaveTraktEntries(movies, shows), nil
	}

	if r.TMDBTrending == nil {
		return nil, nil
	}
	entries, err := r.TMDBTrending.GetCollectionPreset(ctx, "trending", "all", window, fetchLimit)
	if err != nil {
		return nil, err
	}
	out := make([]trendingDiscoverEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, newTrendingEntry(e.ID, e.TVDBID, e.IMDbID, e.MediaType))
	}
	return out, nil
}

// interleaveTraktEntries alternates rank-ordered movies and shows so the mixed
// trending row surfaces both media types. Each input list is already in trending
// order; alternating preserves that order within each type while mixing them.
func interleaveTraktEntries(movies, shows []catalog.TraktCollectionEntry) []trendingDiscoverEntry {
	out := make([]trendingDiscoverEntry, 0, len(movies)+len(shows))
	for i := 0; i < len(movies) || i < len(shows); i++ {
		if i < len(movies) {
			m := movies[i]
			out = append(out, newTrendingEntry(m.TMDBID, m.TVDBID, m.IMDbID, m.MediaType))
		}
		if i < len(shows) {
			s := shows[i]
			out = append(out, newTrendingEntry(s.TMDBID, s.TVDBID, s.IMDbID, s.MediaType))
		}
	}
	return out
}

// resolveIDs matches trending entries to library content IDs via two batched
// external-ID lookups (movies, series), preserving trending order.
func (r *TrendingRefresher) resolveIDs(ctx context.Context, entries []trendingDiscoverEntry) ([]string, error) {
	if r.Resolver == nil {
		return nil, fmt.Errorf("trending_discover: external ID resolver not configured")
	}
	var movieBatch, seriesBatch catalog.ExternalIDBatch
	for _, e := range entries {
		var batch *catalog.ExternalIDBatch
		switch e.mediaType {
		case "movie":
			batch = &movieBatch
		case "tv":
			batch = &seriesBatch
		default:
			// Skip non-title entries (TMDB trending/all can return "person") so
			// they never match an unrelated library title by shared external ID.
			continue
		}
		if e.tmdbID != "" {
			batch.TMDBIDs = append(batch.TMDBIDs, e.tmdbID)
		}
		if e.imdbID != "" {
			batch.IMDbIDs = append(batch.IMDbIDs, e.imdbID)
		}
		if e.tvdbID != "" {
			batch.TVDBIDs = append(batch.TVDBIDs, e.tvdbID)
		}
	}
	movieLookup, err := r.Resolver.GetByExternalIDs(ctx, movieBatch, "movie")
	if err != nil {
		return nil, err
	}
	seriesLookup, err := r.Resolver.GetByExternalIDs(ctx, seriesBatch, "series")
	if err != nil {
		return nil, err
	}
	return orderedTrendingContentIDs(entries, movieLookup, seriesLookup), nil
}
