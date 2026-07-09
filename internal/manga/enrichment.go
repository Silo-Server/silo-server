package manga

// Enricher periodically enriches manga media_items that are missing metadata
// by querying the configured metadata-provider chain for each item's library
// folder.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	mangaMetadataImageProviderID = "manga-metadata"

	// defaultEnrichBatchSize is sized so a sweep finishes just within the
	// 5-minute task interval: the plugin serves GetMetadata from its search
	// cache, so an item costs one AniList request at the plugin's ~28 req/min
	// budget (AniList's degraded-mode ceiling is 30/min) — 140 items ≈ 295s.
	// Larger batches are not faster: the task manager drops a trigger while a
	// sweep is still running, so an overlong sweep idles until the trigger
	// after next and the effective rate drops below the AniList budget.
	defaultEnrichBatchSize = 140
	defaultEnrichWorkers   = 4

	// enrichFailureCap is the manga_enrichment_state.failures count at which
	// a manga stops being claimed for enrichment. Combined with the
	// failure-count-first claim ordering this prevents a head-of-line block
	// of permanently failing items from starving newer items and hammering
	// providers.
	enrichFailureCap = 5
)

// errEnrichmentSkipped marks an item that could not be attempted at all (no
// library folder linked yet, no providers configured). Skipped items are
// neither stamped as refreshed nor counted against the failure cap, so they
// are retried on every sweep until the missing prerequisite appears.
var errEnrichmentSkipped = errors.New("manga enrichment skipped")

// errEnrichmentNoMatch marks an item every provider answered for without a
// confident match. The item was stamped (it will not be re-claimed); the
// sentinel only keeps the sweep counters honest — a no-match is neither an
// enrichment nor a failure.
var errEnrichmentNoMatch = errors.New("manga enrichment: no confident match")

func mangaContentType() string {
	return "manga"
}

func mangaEnrichWorkers() int {
	n := defaultEnrichWorkers
	if v := os.Getenv("SILO_MANGA_ENRICH_WORKERS"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			n = parsed
		}
	}
	if n > mangaEnrichBatchSize() {
		n = mangaEnrichBatchSize()
	}
	return n
}

func mangaEnrichBatchSize() int {
	if v := os.Getenv("SILO_MANGA_ENRICH_BATCH"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			return parsed
		}
	}
	return defaultEnrichBatchSize
}

type enrichmentItemRow struct {
	ContentID   string
	Title       string
	Year        int
	FolderID    int
	Language    string
	Author      string
	ProviderIDs map[string]string

	// HasPoster marks an already-enriched item claimed only because a
	// secondary field (backdrop, status) is missing; the sweep then fetches by
	// stored provider ID and touches only the missing secondary fields.
	HasPoster bool
	// HasBackdrop guards the secondary pass against re-caching an existing
	// backdrop when the item was claimed for another missing field.
	HasBackdrop bool
}

// Enricher drives the manga metadata enrichment sweep.
type Enricher struct {
	pool           *pgxpool.Pool
	chainRepo      *metadata.ChainRepository
	resolver       *metadata.PluginResolverAdapter
	itemRepo       *catalog.ItemRepository
	personRepo     *catalog.PersonRepository
	providerIDs    *catalog.ProviderIDRepository
	imageCacher    metadata.ImageCacher
	imageCacheJobs metadata.ImageCacheJobEnqueuer
	batchSize      int
	workers        int
}

func NewEnricher(
	pool *pgxpool.Pool,
	chainRepo *metadata.ChainRepository,
	resolver *metadata.PluginResolverAdapter,
	itemRepo *catalog.ItemRepository,
	personRepo *catalog.PersonRepository,
	providerIDs *catalog.ProviderIDRepository,
) *Enricher {
	return &Enricher{
		pool:        pool,
		chainRepo:   chainRepo,
		resolver:    resolver,
		itemRepo:    itemRepo,
		personRepo:  personRepo,
		providerIDs: providerIDs,
		batchSize:   mangaEnrichBatchSize(),
		workers:     mangaEnrichWorkers(),
	}
}

func (e *Enricher) SetImageCacher(cacher metadata.ImageCacher) {
	if e == nil {
		return
	}
	e.imageCacher = cacher
}

func (e *Enricher) SetImageCacheJobEnqueuer(enqueuer metadata.ImageCacheJobEnqueuer) {
	if e == nil {
		return
	}
	e.imageCacheJobs = enqueuer
}

func (e *Enricher) Run(ctx context.Context) (int, error) {
	if e == nil || e.pool == nil || e.chainRepo == nil {
		return 0, nil
	}

	items, err := e.claimBatch(ctx)
	if err != nil {
		return 0, fmt.Errorf("manga enrichment: claim batch: %w", err)
	}
	if len(items) == 0 {
		return 0, nil
	}

	slog.InfoContext(ctx, "manga enrichment: sweep started", "component", "manga",
		"count", len(items),
		"workers", e.workers,
	)

	stats := e.runBatch(ctx, items, e.enrichItem, e.recordEnrichFailure)

	slog.InfoContext(ctx, "manga enrichment: sweep complete", "component", "manga",
		"attempted", len(items),
		"enriched", stats.enriched,
		"no_match", stats.noMatch,
		"failed", stats.failed,
	)
	return int(stats.enriched), nil
}

// sweepStats separates the three terminal outcomes of a sweep so the log and
// task result do not overcount: a stamped no-match is not an enrichment.
type sweepStats struct {
	enriched int64
	noMatch  int64
	failed   int64
}

func (e *Enricher) runBatch(
	ctx context.Context,
	items []enrichmentItemRow,
	enrichFn func(context.Context, enrichmentItemRow) error,
	recordFailure func(context.Context, enrichmentItemRow),
) sweepStats {
	workers := e.workers
	if workers <= 0 {
		workers = 1
	}
	if workers > len(items) {
		workers = len(items)
	}

	ch := make(chan enrichmentItemRow, workers)
	var (
		wg    sync.WaitGroup
		stats sweepStats
	)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range ch {
				if ctx.Err() != nil {
					continue
				}
				if err := enrichFn(ctx, item); err != nil {
					if errors.Is(err, errEnrichmentSkipped) {
						slog.DebugContext(ctx, "manga enrichment: item skipped", "component", "manga",
							"content_id", item.ContentID,
							"title", item.Title,
							"reason", err,
						)
						continue
					}
					if errors.Is(err, errEnrichmentNoMatch) {
						atomic.AddInt64(&stats.noMatch, 1)
						continue
					}
					slog.WarnContext(ctx, "manga enrichment: item failed", "component", "manga",
						"content_id", item.ContentID,
						"title", item.Title,
						"error", err,
					)
					// A cancelled sweep says nothing about the item itself,
					// so it does not count against the failure cap.
					if recordFailure != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
						recordFailure(ctx, item)
					}
					atomic.AddInt64(&stats.failed, 1)
					continue
				}
				atomic.AddInt64(&stats.enriched, 1)
			}
		}()
	}
	for _, item := range items {
		if ctx.Err() != nil {
			break
		}
		ch <- item
	}
	close(ch)
	wg.Wait()
	return stats
}

// claimBatchQuery selects manga needing enrichment. Both arms require
// last_refreshed IS NULL:
//   - the common arm is unenriched items (no poster);
//   - the secondary arm (poster present, backdrop or show_status empty) only
//     becomes reachable when an operator resets last_refreshed to backfill a
//     newly-added field across an already-enriched library — exactly how the
//     banner and publication-status backfills were rolled out. It is an
//     efficient fast-path for that admin action (fetch by stored provider id,
//     write only the missing secondary field) and is intentionally NOT an
//     automatic periodic re-check: a series whose provider simply has no
//     banner would otherwise be re-fetched every sweep.
//
// Stamping after the attempt keeps items whose provider has no banner/status
// from being re-claimed within the same backfill. Items with fewer prior
// failures are claimed first and items at/above enrichFailureCap are skipped
// entirely, so a block of permanently failing items cannot occupy every sweep.
const claimBatchQuery = `
	SELECT
		mi.content_id,
		mi.title,
		mi.year,
		COALESCE(mil.media_folder_id, 0) AS folder_id,
		COALESCE(mf.metadata_language, 'en') AS language,
		COALESCE(
			(SELECT p.name
			 FROM item_people ip
			 JOIN people p ON p.id = ip.person_id
			 WHERE ip.content_id = mi.content_id
			   AND ip.kind = 7
			 ORDER BY ip.sort_order, ip.id
			 LIMIT 1),
			''
		) AS author,
		(mi.poster_path IS NOT NULL AND mi.poster_path <> '') AS has_poster,
		(mi.backdrop_path IS NOT NULL AND mi.backdrop_path <> '') AS has_backdrop
	FROM media_items mi
	LEFT JOIN media_item_libraries mil ON mil.content_id = mi.content_id
	LEFT JOIN media_folders mf ON mf.id = mil.media_folder_id
	LEFT JOIN manga_enrichment_state ees ON ees.content_id = mi.content_id
	WHERE mi.type = 'manga'
	  AND ((mi.poster_path IS NULL OR mi.poster_path = '')
	    OR (mi.backdrop_path IS NULL OR mi.backdrop_path = '')
	    OR (mi.show_status IS NULL OR mi.show_status = ''))
	  AND mi.last_refreshed IS NULL
	  AND COALESCE(ees.failures, 0) < $2
	ORDER BY COALESCE(ees.failures, 0) ASC, mi.created_at ASC
	LIMIT $1
`

func (e *Enricher) claimBatch(ctx context.Context) ([]enrichmentItemRow, error) {
	rows, err := e.pool.Query(ctx, claimBatchQuery, e.batchSize, enrichFailureCap)
	if err != nil {
		return nil, fmt.Errorf("querying unenriched manga: %w", err)
	}
	defer rows.Close()

	var items []enrichmentItemRow
	seen := make(map[string]struct{})
	for rows.Next() {
		var item enrichmentItemRow
		if err := rows.Scan(
			&item.ContentID,
			&item.Title,
			&item.Year,
			&item.FolderID,
			&item.Language,
			&item.Author,
			&item.HasPoster,
			&item.HasBackdrop,
		); err != nil {
			return nil, fmt.Errorf("scanning manga enrichment row: %w", err)
		}
		if _, dup := seen[item.ContentID]; dup {
			continue
		}
		seen[item.ContentID] = struct{}{}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating manga enrichment rows: %w", err)
	}

	if e.providerIDs != nil && len(items) > 0 {
		ids := make([]string, len(items))
		for i := range items {
			ids[i] = items[i].ContentID
		}
		if byID, err := e.providerIDs.GetByContentIDs(ctx, ids); err == nil {
			for i := range items {
				items[i].ProviderIDs = providerIDMapFromRows(byID[items[i].ContentID])
			}
		}
	}

	return items, nil
}

func (e *Enricher) enrichItem(ctx context.Context, item enrichmentItemRow) error {
	if item.FolderID == 0 {
		// The scanner inserts the library membership after the item upsert, so
		// a freshly indexed manga can be claimed inside that window. Skip it:
		// stamping here would terminally mark the item refreshed before any
		// provider ever saw it.
		return fmt.Errorf("%w: item %s has no library folder yet", errEnrichmentSkipped, item.ContentID)
	}

	providers, err := metadata.ResolveChain(ctx, item.FolderID, mangaContentType(), e.chainRepo, e.resolver)
	if err != nil {
		return fmt.Errorf("resolving manga chain for folder %d: %w", item.FolderID, err)
	}
	return e.enrichWithProviders(ctx, item, providers)
}

// enrichWithProviders runs the provider chain for one claimed item. Outcomes:
//   - metadata obtained: persist it and stamp last_refreshed (nil error);
//   - providers answered but nothing matched: stamp last_refreshed so the
//     item is not re-claimed every sweep (errEnrichmentNoMatch);
//   - one or more providers errored and no metadata was obtained: return an
//     error so the failure cap/backoff engages, without stamping;
//   - no providers configured: skip (no stamp, no failure) so the item is
//     retried once a chain exists.
func (e *Enricher) enrichWithProviders(ctx context.Context, item enrichmentItemRow, providers []metadata.Provider) error {
	if len(providers) == 0 {
		return fmt.Errorf("%w: no metadata providers configured for folder %d", errEnrichmentSkipped, item.FolderID)
	}

	accumulator, accumulatedIDs, providerErrs := collectMangaMetadata(ctx, item, providers)

	if item.HasPoster {
		return e.enrichSecondaryOnly(ctx, item, accumulator, providerErrs)
	}

	if !accumulator.HasMetadata && accumulator.PosterPath == "" && accumulator.Overview == "" {
		if err := ctx.Err(); err != nil {
			// A cancelled sweep says nothing about the item or the providers.
			return err
		}
		if len(providerErrs) > 0 {
			// Transient provider trouble must not stamp the item terminally;
			// surfacing an error engages the failure cap and backoff instead.
			return fmt.Errorf("no metadata obtained, %d provider error(s): %w",
				len(providerErrs), errors.Join(providerErrs...))
		}
		slog.InfoContext(ctx, "manga enrichment: no metadata found", "component", "manga",
			"content_id", item.ContentID,
			"title", item.Title,
		)
		if err := e.stampLastRefreshed(ctx, item.ContentID); err != nil {
			return err
		}
		return errEnrichmentNoMatch
	}

	if err := e.persist(ctx, item.ContentID, accumulatedIDs, accumulator); err != nil {
		return fmt.Errorf("persisting enrichment for %s: %w", item.ContentID, err)
	}
	e.enqueueRemoteArtwork(ctx, item.ContentID, accumulator)

	slog.InfoContext(ctx, "manga enrichment: enriched", "component", "manga",
		"content_id", item.ContentID,
		"title", item.Title,
		"poster", accumulator.PosterPath != "",
		"backdrop", accumulator.BackdropPath != "",
		"overview", accumulator.Overview != "",
		"people", len(filterMangaPeople(accumulator.People)),
	)

	return nil
}

// enrichSecondaryOnly finishes a secondary-fields claim: an already-enriched
// item missing its backdrop and/or publication status. Only the missing
// secondary fields are written — the existing poster, overview, people, and
// provider IDs stay untouched. Whatever the outcome (fields filled, provider
// has neither), the item is stamped so it is not re-claimed every sweep;
// provider errors engage the failure cap without stamping, like the full path.
func (e *Enricher) enrichSecondaryOnly(ctx context.Context, item enrichmentItemRow, result *metadata.MetadataResult, providerErrs []error) error {
	upd := &catalog.MetadataUpdate{}
	if result != nil && result.BackdropPath != "" && !item.HasBackdrop {
		upd.BackdropPath = &result.BackdropPath
		if isRemoteHTTPImage(result.BackdropPath) {
			upd.BackdropSourcePath = &result.BackdropPath
		}
	}
	if result != nil {
		if status := normalizeMangaStatus(result.ShowStatus); status != "" {
			upd.ShowStatus = &status
		}
	}

	if upd.BackdropPath == nil && upd.ShowStatus == nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(providerErrs) > 0 {
			return fmt.Errorf("no secondary metadata obtained, %d provider error(s): %w",
				len(providerErrs), errors.Join(providerErrs...))
		}
		slog.InfoContext(ctx, "manga enrichment: no secondary metadata available", "component", "manga",
			"content_id", item.ContentID,
			"title", item.Title,
		)
		if err := e.stampLastRefreshed(ctx, item.ContentID); err != nil {
			return err
		}
		return errEnrichmentNoMatch
	}

	if err := e.updateMetadataAndTimestamps(ctx, item.ContentID, upd); err != nil {
		return fmt.Errorf("persisting secondary metadata for %s: %w", item.ContentID, err)
	}
	if result != nil && result.BackdropPath != "" && !item.HasBackdrop {
		e.enqueueRemoteImage(ctx, item.ContentID, result.BackdropPath, metadata.ImageBackdrop)
	}

	slog.InfoContext(ctx, "manga enrichment: secondary metadata added", "component", "manga",
		"content_id", item.ContentID,
		"title", item.Title,
		"backdrop", upd.BackdropPath != nil,
		"status", upd.ShowStatus != nil,
	)
	return nil
}

// collectMangaMetadata queries every provider in the chain and accumulates
// IDs and metadata. Individual provider failures are collected (not fatal) so
// the caller can distinguish "providers answered, no match" from "providers
// were unreachable". The search pass is skipped when the item already carries
// provider IDs (a previously matched item only needs the by-ID fetch).
func collectMangaMetadata(ctx context.Context, item enrichmentItemRow, providers []metadata.Provider) (*metadata.MetadataResult, map[string]string, []error) {
	searchQuery, accumulatedIDs := buildMangaSearchQuery(item)
	var providerErrs []error

	// An item that already carries provider IDs was matched before; the by-ID
	// fetch below is enough and re-searching would spend a rate-limited
	// request (and risk re-matching differently).
	searchProviders := providers
	if len(accumulatedIDs) > 0 {
		searchProviders = nil
	}

	for _, p := range searchProviders {
		sp, ok := p.(metadata.SearchProvider)
		if !ok {
			continue
		}
		results, searchErr := sp.Search(ctx, searchQuery)
		if searchErr != nil {
			slog.WarnContext(ctx, "manga enrichment: search error", "component", "manga",
				"provider", p.Slug(),
				"content_id", item.ContentID,
				"error", searchErr,
			)
			providerErrs = append(providerErrs, fmt.Errorf("%s search: %w", p.Slug(), searchErr))
			continue
		}
		if len(results) == 0 {
			continue
		}
		for k, v := range results[0].ProviderIDs {
			if v != "" {
				if _, exists := accumulatedIDs[k]; !exists {
					accumulatedIDs[k] = v
				}
			}
		}
		slog.DebugContext(ctx, "manga enrichment: search result", "component", "manga",
			"provider", p.Slug(),
			"content_id", item.ContentID,
			"matched_ids", accumulatedIDs,
		)
	}

	accumulator := &metadata.MetadataResult{
		ProviderIDs: accumulatedIDs,
	}

	for _, p := range providers {
		mp, ok := p.(metadata.MetadataProvider)
		if !ok {
			continue
		}
		result, getErr := mp.GetMetadata(ctx, buildMangaMetadataRequest(accumulator.ProviderIDs, item.Language))
		if getErr != nil {
			slog.WarnContext(ctx, "manga enrichment: GetMetadata error", "component", "manga",
				"provider", p.Slug(),
				"content_id", item.ContentID,
				"error", getErr,
			)
			providerErrs = append(providerErrs, fmt.Errorf("%s metadata: %w", p.Slug(), getErr))
			continue
		}
		if result == nil || !result.HasMetadata {
			continue
		}
		mergeEnrichmentProviderIDs(accumulator, result)
		metadata.MergeMetadata(result, accumulator, nil, metadata.MergeFillEmpty)
		// MergeMetadata does not propagate HasMetadata; without this a confident
		// match carrying only genres/authors/status/year (no cover, no overview)
		// would fail the no-match check below and be discarded + stamped.
		accumulator.HasMetadata = true

		slog.DebugContext(ctx, "manga enrichment: metadata received", "component", "manga",
			"provider", p.Slug(),
			"content_id", item.ContentID,
			"has_poster", result.PosterPath != "",
			"has_overview", result.Overview != "",
		)
	}

	return accumulator, accumulator.ProviderIDs, providerErrs
}

// cacheRemoteImages localizes the remote poster and backdrop URLs on a full
// enrichment result, replacing each with the cached path + thumbhash when
// caching succeeds (the provider URL is kept as a fallback otherwise).
func (e *Enricher) cacheRemoteImages(ctx context.Context, contentID string, result *metadata.MetadataResult) {
	if e == nil || result == nil {
		return
	}
	if path, thumbhash := e.cacheRemoteImage(ctx, contentID, result.PosterPath, metadata.ImagePoster); path != "" {
		result.PosterPath = path
		if thumbhash != "" {
			result.PosterThumbhash = thumbhash
		}
	}
	if path, thumbhash := e.cacheRemoteImage(ctx, contentID, result.BackdropPath, metadata.ImageBackdrop); path != "" {
		result.BackdropPath = path
		if thumbhash != "" {
			result.BackdropThumbhash = thumbhash
		}
	}
}

// cacheRemoteImage downloads and caches one remote image, returning the
// stored path and thumbhash. On any failure it returns the original URL (a
// remote URL in the column still renders; the cache is an optimization).
func (e *Enricher) cacheRemoteImage(ctx context.Context, contentID, url string, imageType metadata.ImageType) (string, string) {
	if e == nil || url == "" {
		return url, ""
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return url, ""
	}
	if isNilImageCacher(e.imageCacher) {
		return url, ""
	}

	cached, err := e.imageCacher.CacheImage(ctx, metadata.CacheImageRequest{
		SourceURL:   url,
		ProviderID:  mangaMetadataImageProviderID,
		ContentType: "manga",
		ContentID:   contentID,
		ImageType:   imageType,
	})
	if err != nil {
		slog.WarnContext(ctx, "manga enrichment: image cache failed, keeping provider URL", "component", "manga",
			"content_id", contentID,
			"url", url,
			"error", err,
		)
		return url, ""
	}
	if cached == nil {
		slog.WarnContext(ctx, "manga enrichment: image cache returned no result, keeping provider URL", "component", "manga",
			"content_id", contentID,
			"url", url,
		)
		return url, ""
	}

	storedPath := cachedOriginalImagePath(cached.BasePath, cached.Ext)
	if storedPath == "" {
		return url, ""
	}
	return storedPath, cached.Thumbhash
}

func isNilImageCacher(cacher metadata.ImageCacher) bool {
	if cacher == nil {
		return true
	}
	value := reflect.ValueOf(cacher)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func cachedOriginalImagePath(basePath, ext string) string {
	if basePath == "" {
		return ""
	}
	if strings.Contains(basePath, "/original.") {
		return basePath
	}
	if ext == "" {
		ext = ".jpg"
	}
	return strings.TrimRight(basePath, "/") + "/original" + ext
}

func (e *Enricher) persist(ctx context.Context, contentID string, providerIDs map[string]string, result *metadata.MetadataResult) error {
	upd := &catalog.MetadataUpdate{}

	if result.PosterPath != "" {
		upd.PosterPath = &result.PosterPath
		if isRemoteHTTPImage(result.PosterPath) {
			upd.PosterSourcePath = &result.PosterPath
		}
	}
	if result.PosterThumbhash != "" {
		upd.PosterThumbhash = &result.PosterThumbhash
	}
	if result.BackdropPath != "" {
		upd.BackdropPath = &result.BackdropPath
		if isRemoteHTTPImage(result.BackdropPath) {
			upd.BackdropSourcePath = &result.BackdropPath
		}
	}
	if result.BackdropThumbhash != "" {
		upd.BackdropThumbhash = &result.BackdropThumbhash
	}
	if result.LogoPath != "" {
		upd.LogoPath = &result.LogoPath
		if isRemoteHTTPImage(result.LogoPath) {
			upd.LogoSourcePath = &result.LogoPath
		}
	}
	if result.Overview != "" {
		upd.Overview = &result.Overview
	}
	if result.Tagline != "" {
		upd.Tagline = &result.Tagline
	}
	if result.ReleaseDate != "" {
		upd.ReleaseDate = &result.ReleaseDate
	}
	if len(result.Genres) > 0 {
		genres := append([]string(nil), result.Genres...)
		upd.Genres = &genres
	}
	if len(result.Studios) > 0 {
		studios := append([]string(nil), result.Studios...)
		upd.Studios = &studios
	}
	if result.ContentRating != "" {
		upd.ContentRating = &result.ContentRating
	}
	if result.Runtime > 0 {
		upd.Runtime = &result.Runtime
	}
	if result.Year > 0 {
		upd.Year = &result.Year
	}
	if status := normalizeMangaStatus(result.ShowStatus); status != "" {
		upd.ShowStatus = &status
	}

	providerIDs = filterMangaProviderIDs(providerIDs)
	if e.providerIDs != nil && len(providerIDs) > 0 {
		if err := e.providerIDs.ReplaceByContentID(ctx, contentID, providerIDs); err != nil {
			slog.WarnContext(ctx, "manga enrichment: failed to persist provider IDs", "component", "manga",
				"content_id", contentID,
				"error", err,
			)
		}
	}

	if err := e.updateMetadataAndTimestamps(ctx, contentID, upd); err != nil {
		return err
	}

	authors := filterMangaPeople(result.People)
	if len(authors) > 0 && e.personRepo != nil && e.itemRepo != nil {
		if err := e.persistPeople(ctx, contentID, authors); err != nil {
			slog.WarnContext(ctx, "manga enrichment: failed to persist people", "component", "manga",
				"content_id", contentID,
				"error", err,
			)
		}
	}

	return nil
}

func (e *Enricher) enqueueRemoteArtwork(ctx context.Context, contentID string, result *metadata.MetadataResult) {
	if e == nil || result == nil || contentID == "" {
		return
	}
	e.enqueueRemoteImage(ctx, contentID, result.PosterPath, metadata.ImagePoster)
	e.enqueueRemoteImage(ctx, contentID, result.BackdropPath, metadata.ImageBackdrop)
	e.enqueueRemoteImage(ctx, contentID, result.LogoPath, metadata.ImageLogo)
}

func (e *Enricher) enqueueRemoteImage(ctx context.Context, contentID, sourcePath string, imageType metadata.ImageType) {
	if e == nil || e.imageCacheJobs == nil || contentID == "" || !isRemoteHTTPImage(sourcePath) {
		return
	}
	enqueueCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	_, err := e.imageCacheJobs.EnqueueBatch(enqueueCtx, []metadata.EnqueueImageCacheJobInput{{
		TargetType:        metadata.ImageCacheTargetItem,
		TargetContentID:   contentID,
		SeriesID:          contentID,
		SourcePath:        sourcePath,
		ProviderID:        mangaMetadataImageProviderID,
		ProviderContentID: contentID,
		ContentType:       mangaContentType(),
		ImageType:         metadata.ImageTypeToString(imageType),
	}})
	if err != nil {
		slog.WarnContext(ctx, "manga enrichment: failed to enqueue image cache job", "component", "manga",
			"content_id", contentID,
			"image_type", metadata.ImageTypeToString(imageType),
			"error", err,
		)
	}
}

func isRemoteHTTPImage(path string) bool {
	return strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://")
}

func (e *Enricher) updateMetadataAndTimestamps(ctx context.Context, contentID string, upd *catalog.MetadataUpdate) error {
	if e.itemRepo == nil {
		return nil
	}
	if err := e.itemRepo.UpdateMetadata(ctx, contentID, upd); err != nil {
		return fmt.Errorf("UpdateMetadata: %w", err)
	}
	return e.stampLastRefreshed(ctx, contentID)
}

func (e *Enricher) stampLastRefreshed(ctx context.Context, contentID string) error {
	if e.pool == nil {
		return nil
	}
	now := time.Now().UTC()
	if _, err := e.pool.Exec(ctx, `
		UPDATE media_items
		SET last_refreshed = $1,
		    matched_at = COALESCE(matched_at, $1),
		    status = CASE WHEN status = 'pending' THEN 'matched' ELSE status END
		WHERE content_id = $2
	`, now, contentID); err != nil {
		return err
	}
	// Success clears the enrichment failure backlog. media_items.refresh_failures
	// is intentionally left alone: it belongs to the metadata refresh-debt system.
	_, err := e.pool.Exec(ctx, `
		DELETE FROM manga_enrichment_state WHERE content_id = $1
	`, contentID)
	return err
}

// recordEnrichFailure increments the item's manga_enrichment_state failure
// counter so claimBatch deprioritizes it on the next sweep and stops claiming
// it at enrichFailureCap. The state is dedicated to manga enrichment;
// media_items.refresh_failures is owned by the metadata refresh-debt system
// and is never touched here.
func (e *Enricher) recordEnrichFailure(ctx context.Context, item enrichmentItemRow) {
	if e == nil || e.pool == nil {
		return
	}
	if _, err := e.pool.Exec(ctx, `
		INSERT INTO manga_enrichment_state (content_id, failures, updated_at)
		VALUES ($1, 1, NOW())
		ON CONFLICT (content_id) DO UPDATE SET
			failures   = manga_enrichment_state.failures + 1,
			updated_at = NOW()
	`, item.ContentID); err != nil {
		slog.WarnContext(ctx, "manga enrichment: failed to record enrichment failure", "component", "manga",
			"content_id", item.ContentID,
			"error", err,
		)
	}
}

func (e *Enricher) persistPeople(ctx context.Context, contentID string, people []models.ItemPerson) error {
	people = filterMangaPeople(people)
	if len(people) == 0 {
		return nil
	}

	persons := make([]models.Person, len(people))
	for i := range people {
		persons[i] = people[i].Person
	}

	personIDs, err := e.personRepo.BatchFindOrCreate(ctx, persons)
	if err != nil {
		return fmt.Errorf("BatchFindOrCreate people: %w", err)
	}

	linked := make([]models.ItemPerson, 0, len(people))
	for i := range people {
		if i >= len(personIDs) || personIDs[i] == 0 {
			continue
		}
		ip := people[i]
		ip.Person.ID = personIDs[i]
		linked = append(linked, ip)
	}

	if len(linked) == 0 {
		return nil
	}

	existing, err := e.itemRepo.GetPeople(ctx, contentID)
	if err != nil {
		return fmt.Errorf("get existing people: %w", err)
	}
	return e.itemRepo.ReplacePeople(ctx, contentID, mergeMangaAuthorCredits(existing, linked))
}

// mergeMangaAuthorCredits mirrors the scanner's mergeEbookPeople semantics:
// the provider authors replace existing author (and stale narrator) credits,
// while every other curated people kind on the item is preserved.
func mergeMangaAuthorCredits(existing []models.ItemPerson, authors []models.ItemPerson) []models.ItemPerson {
	merged := make([]models.ItemPerson, 0, len(existing)+len(authors))
	for _, p := range existing {
		if p.Kind == models.PersonKindAuthor || p.Kind == models.PersonKindNarrator {
			continue
		}
		p.SortOrder = len(merged)
		merged = append(merged, p)
	}
	for _, a := range authors {
		a.SortOrder = len(merged)
		merged = append(merged, a)
	}
	return merged
}

func filterMangaPeople(people []models.ItemPerson) []models.ItemPerson {
	authors := make([]models.ItemPerson, 0, len(people))
	for _, person := range people {
		if person.Kind != models.PersonKindAuthor {
			continue
		}
		person.SortOrder = len(authors)
		authors = append(authors, person)
	}
	return authors
}

func buildMangaSearchQuery(item enrichmentItemRow) (metadata.SearchQuery, map[string]string) {
	accumulatedIDs := filterMangaProviderIDs(item.ProviderIDs)
	if accumulatedIDs == nil {
		accumulatedIDs = map[string]string{}
	}
	return metadata.SearchQuery{
		Title:       item.Title,
		Year:        item.Year,
		ContentType: mangaContentType(),
		ProviderIDs: accumulatedIDs,
		Language:    item.Language,
	}, accumulatedIDs
}

func buildMangaMetadataRequest(providerIDs map[string]string, language string) metadata.MetadataRequest {
	return metadata.MetadataRequest{
		ProviderIDs: filterMangaProviderIDs(providerIDs),
		ContentType: mangaContentType(),
		Language:    language,
	}
}

func mergeEnrichmentProviderIDs(dst *metadata.MetadataResult, src *metadata.MetadataResult) {
	if src == nil || len(src.ProviderIDs) == 0 {
		return
	}
	if dst.ProviderIDs == nil {
		dst.ProviderIDs = make(map[string]string, len(src.ProviderIDs))
	}
	for k, v := range filterMangaProviderIDs(src.ProviderIDs) {
		if v != "" {
			if _, exists := dst.ProviderIDs[k]; !exists {
				dst.ProviderIDs[k] = v
			}
		}
	}
}

func filterMangaProviderIDs(providerIDs map[string]string) map[string]string {
	if len(providerIDs) == 0 {
		return nil
	}
	filtered := make(map[string]string, len(providerIDs))
	for provider, providerID := range providerIDs {
		provider = strings.TrimSpace(provider)
		providerID = strings.TrimSpace(providerID)
		if provider == "" || providerID == "" {
			continue
		}
		provider = strings.ToLower(provider)
		if isMangaASINProvider(provider) || isInternalMangaProvider(provider) {
			continue
		}
		filtered[provider] = providerID
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

// normalizeMangaStatus maps the varied publication-status strings returned by
// manga metadata providers (AniList: RELEASING/FINISHED/NOT_YET_RELEASED/
// CANCELLED/HIATUS, MangaDex: ongoing/completed/hiatus/cancelled, and the SDK's
// Continuing/Ended) onto the stable label set the clients render, so the shared
// show_status field carries one consistent manga value-domain instead of raw
// provider casing. Unknown values pass through trimmed so nothing is lost.
func normalizeMangaStatus(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	switch strings.ToLower(strings.ReplaceAll(s, " ", "_")) {
	case "ongoing", "releasing", "current", "publishing", "continuing":
		return "Ongoing"
	case "completed", "finished", "ended":
		return "Completed"
	case "hiatus", "on_hiatus", "paused":
		return "Hiatus"
	case "cancelled", "canceled", "discontinued":
		return "Cancelled"
	case "upcoming", "not_yet_released", "unreleased", "announced":
		return "Upcoming"
	default:
		return s
	}
}

func isMangaASINProvider(provider string) bool {
	normalized := strings.ReplaceAll(strings.ReplaceAll(provider, "_", ""), "-", "")
	return normalized == "asin" || normalized == "audibleasin"
}

// isInternalMangaProvider filters Silo-internal identity providers out of the
// metadata flow. The scanner stamps every manga series with a manga_series
// identity row for idempotency; passing it to the plugin made the
// search-skip-when-already-matched guard treat every item as matched, so
// unmatched items went straight to a by-ID fetch with no usable ID and were
// stamped as no-match without a single search.
func isInternalMangaProvider(provider string) bool {
	return strings.ReplaceAll(provider, "-", "_") == "manga_series"
}

func providerIDMapFromRows(rows []*models.MediaItemProviderID) map[string]string {
	if len(rows) == 0 {
		return nil
	}
	m := make(map[string]string, len(rows))
	for _, r := range rows {
		if r != nil {
			for provider, providerID := range filterMangaProviderIDs(map[string]string{
				r.Provider: r.ProviderID,
			}) {
				m[provider] = providerID
			}
		}
	}
	return m
}
