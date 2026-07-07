package ebooks

// Enricher periodically enriches ebook media_items that are missing metadata
// by querying the configured metadata-provider chain for each item's library
// folder.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"regexp"
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
	ebookMetadataImageProviderID = "ebook-metadata"

	defaultEnrichBatchSize = 50
	defaultEnrichWorkers   = 4

	// enrichFailureCap is the ebook_enrichment_state.failures count at which
	// an ebook stops being claimed for enrichment. Combined with the
	// failure-count-first claim ordering this prevents a head-of-line block
	// of permanently failing items from starving newer items and hammering
	// providers.
	enrichFailureCap = 5
)

// errEnrichmentSkipped marks an item that could not be attempted at all (no
// library folder linked yet, no providers configured). Skipped items are
// neither stamped as refreshed nor counted against the failure cap, so they
// are retried on every sweep until the missing prerequisite appears.
var errEnrichmentSkipped = errors.New("ebook enrichment skipped")

func ebookContentType() string {
	return "ebook"
}

func ebookEnrichWorkers() int {
	n := defaultEnrichWorkers
	if v := os.Getenv("SILO_EBOOK_ENRICH_WORKERS"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			n = parsed
		}
	}
	if n > defaultEnrichBatchSize {
		n = defaultEnrichBatchSize
	}
	return n
}

type enrichmentItemRow struct {
	ContentID   string
	Title       string
	Year        int
	FolderID    int
	Language    string
	Author      string
	ProviderIDs map[string]string
}

// Enricher drives the ebook metadata enrichment sweep.
type Enricher struct {
	pool           *pgxpool.Pool
	chainRepo      *metadata.ChainRepository
	resolver       *metadata.PluginResolverAdapter
	itemRepo       *catalog.ItemRepository
	personRepo     *catalog.PersonRepository
	providerIDs    *catalog.ProviderIDRepository
	imageCacher    metadata.ImageCacher
	imageCacheJobs metadata.ImageCacheJobEnqueuer
	workLinker     literaryWorkLinker
	batchSize      int
	workers        int
}

type literaryWorkLinker interface {
	AutoLinkContent(ctx context.Context, contentID string) (workID string, linked bool, err error)
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
		batchSize:   defaultEnrichBatchSize,
		workers:     ebookEnrichWorkers(),
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

func (e *Enricher) SetLiteraryWorkLinker(linker literaryWorkLinker) {
	if e == nil {
		return
	}
	e.workLinker = linker
}

func (e *Enricher) Run(ctx context.Context) (int, error) {
	if e == nil || e.pool == nil || e.chainRepo == nil {
		return 0, nil
	}

	items, err := e.claimBatch(ctx)
	if err != nil {
		return 0, fmt.Errorf("ebook enrichment: claim batch: %w", err)
	}
	if len(items) == 0 {
		return 0, nil
	}

	slog.Info("ebook enrichment: sweep started",
		"count", len(items),
		"workers", e.workers,
	)

	enriched := e.runBatch(ctx, items, e.enrichItem, e.recordEnrichFailure)

	slog.Info("ebook enrichment: sweep complete",
		"attempted", len(items),
		"enriched", enriched,
	)
	return enriched, nil
}

func (e *Enricher) runBatch(
	ctx context.Context,
	items []enrichmentItemRow,
	enrichFn func(context.Context, enrichmentItemRow) error,
	recordFailure func(context.Context, enrichmentItemRow),
) int {
	workers := e.workers
	if workers <= 0 {
		workers = 1
	}
	if workers > len(items) {
		workers = len(items)
	}

	ch := make(chan enrichmentItemRow, workers)
	var (
		wg       sync.WaitGroup
		enriched int64
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
						slog.Debug("ebook enrichment: item skipped",
							"content_id", item.ContentID,
							"title", item.Title,
							"reason", err,
						)
						continue
					}
					slog.Warn("ebook enrichment: item failed",
						"content_id", item.ContentID,
						"title", item.Title,
						"error", err,
					)
					// A cancelled sweep says nothing about the item itself,
					// so it does not count against the failure cap.
					if recordFailure != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
						recordFailure(ctx, item)
					}
					continue
				}
				atomic.AddInt64(&enriched, 1)
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
	return int(enriched)
}

// claimBatchQuery selects unenriched ebooks. Items with fewer prior failures
// are claimed first and items at/above enrichFailureCap are skipped entirely,
// so a block of permanently failing items cannot occupy every sweep.
var claimBatchQuery = `
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
		) AS author
	FROM media_items mi
	LEFT JOIN media_item_libraries mil ON mil.content_id = mi.content_id
	LEFT JOIN media_folders mf ON mf.id = mil.media_folder_id
	LEFT JOIN ebook_enrichment_state ees ON ees.content_id = mi.content_id
	WHERE mi.type = 'ebook'
	  -- Manga chapters are type='ebook' but are parts of a series, not
	  -- standalone books. They are enriched via their type='manga' series (a
	  -- separate path), never individually against book sources — excluding
	  -- them here stops a pointless search storm over Gutenberg/Anna's/etc.
	  AND ` + catalog.MangaChapterExclusionWhere("mi") + `
	  AND (mi.poster_path IS NULL OR mi.poster_path = '')
	  AND mi.last_refreshed IS NULL
	  AND COALESCE(ees.failures, 0) < $2
	ORDER BY COALESCE(ees.failures, 0) ASC, mi.created_at ASC
	LIMIT $1
`

func (e *Enricher) claimBatch(ctx context.Context) ([]enrichmentItemRow, error) {
	rows, err := e.pool.Query(ctx, claimBatchQuery, e.batchSize, enrichFailureCap)
	if err != nil {
		return nil, fmt.Errorf("querying unenriched ebooks: %w", err)
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
		); err != nil {
			return nil, fmt.Errorf("scanning ebook enrichment row: %w", err)
		}
		if _, dup := seen[item.ContentID]; dup {
			continue
		}
		seen[item.ContentID] = struct{}{}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating ebook enrichment rows: %w", err)
	}

	if e.providerIDs != nil {
		for i := range items {
			pids, err := e.providerIDs.GetByContentID(ctx, items[i].ContentID)
			if err == nil {
				items[i].ProviderIDs = providerIDMapFromRows(pids)
			}
		}
	}

	return items, nil
}

func (e *Enricher) enrichItem(ctx context.Context, item enrichmentItemRow) error {
	if item.FolderID == 0 {
		// The scanner inserts the library membership after the item upsert, so
		// a freshly indexed ebook can be claimed inside that window. Skip it:
		// stamping here would terminally mark the item refreshed before any
		// provider ever saw it.
		return fmt.Errorf("%w: item %s has no library folder yet", errEnrichmentSkipped, item.ContentID)
	}

	providers, err := metadata.ResolveChain(ctx, item.FolderID, ebookContentType(), e.chainRepo, e.resolver)
	if err != nil {
		return fmt.Errorf("resolving ebook chain for folder %d: %w", item.FolderID, err)
	}
	return e.enrichWithProviders(ctx, item, providers)
}

// enrichWithProviders runs the provider chain for one claimed item. Outcomes:
//   - metadata obtained: persist it and stamp last_refreshed (nil error);
//   - providers answered but nothing matched: stamp last_refreshed so the
//     item is not re-claimed every sweep (nil error);
//   - one or more providers errored and no metadata was obtained: return an
//     error so the failure cap/backoff engages, without stamping;
//   - no providers configured: skip (no stamp, no failure) so the item is
//     retried once a chain exists.
func (e *Enricher) enrichWithProviders(ctx context.Context, item enrichmentItemRow, providers []metadata.Provider) error {
	if len(providers) == 0 {
		return fmt.Errorf("%w: no metadata providers configured for folder %d", errEnrichmentSkipped, item.FolderID)
	}

	var owner providerIDOwnerLookup
	if e.providerIDs != nil {
		owner = e.providerIDs
	}
	accumulator, accumulatedIDs, providerErrs := collectEbookMetadata(ctx, item, providers, owner)

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
		slog.Info("ebook enrichment: no metadata found",
			"content_id", item.ContentID,
			"title", item.Title,
		)
		return e.stampLastRefreshed(ctx, item.ContentID)
	}

	if err := e.persist(ctx, item.ContentID, accumulatedIDs, accumulator); err != nil {
		return fmt.Errorf("persisting enrichment for %s: %w", item.ContentID, err)
	}
	e.enqueueRemoteArtwork(ctx, item.ContentID, accumulator)
	e.autoLinkLiteraryWork(ctx, item.ContentID)

	slog.Info("ebook enrichment: enriched",
		"content_id", item.ContentID,
		"title", item.Title,
		"poster", accumulator.PosterPath != "",
		"overview", accumulator.Overview != "",
		"people", len(filterEbookPeople(accumulator.People)),
	)

	return nil
}

// providerIDOwnerLookup reports the content item (if any) that already owns a
// given set of durable provider IDs. *catalog.ProviderIDRepository satisfies it.
type providerIDOwnerLookup interface {
	FindContentIDByProviderIDs(ctx context.Context, providerIDs map[string]string, itemType, excludeContentID string) (string, error)
}

// collectEbookMetadata queries every provider in the chain and accumulates
// IDs and metadata. Individual provider failures are collected (not fatal) so
// the caller can distinguish "providers answered, no match" from "providers
// were unreachable". When owner is non-nil, a search-result provider ID already
// claimed by a different content item is skipped: distinct books that resolve to
// the same provider work (e.g. two series volumes searched as the bare series
// name) must not steal each other's identity, which would mis-tag the loser and
// violate the (provider, provider_id, item_type) uniqueness constraint on persist.
func collectEbookMetadata(ctx context.Context, item enrichmentItemRow, providers []metadata.Provider, owner providerIDOwnerLookup) (*metadata.MetadataResult, map[string]string, []error) {
	searchQuery, accumulatedIDs := buildEbookSearchQuery(item)
	var providerErrs []error

	for _, p := range providers {
		sp, ok := p.(metadata.SearchProvider)
		if !ok {
			continue
		}
		results, searchErr := sp.Search(ctx, searchQuery)
		if searchErr != nil {
			slog.Warn("ebook enrichment: search error",
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
			if v == "" {
				continue
			}
			if _, exists := accumulatedIDs[k]; exists {
				continue
			}
			if owner != nil {
				ownerID, ownErr := owner.FindContentIDByProviderIDs(ctx, map[string]string{k: v}, ebookContentType(), item.ContentID)
				if ownErr != nil {
					// Don't claim an ID we couldn't verify is free, and surface
					// the error so the item retries rather than terminally
					// stamping as "no match".
					providerErrs = append(providerErrs, fmt.Errorf("%s ownership check %s=%s: %w", p.Slug(), k, v, ownErr))
					continue
				}
				if ownerID != "" {
					slog.Info("ebook enrichment: provider id already owned by another item; skipping match",
						"provider", k,
						"provider_id", v,
						"content_id", item.ContentID,
						"owned_by", ownerID,
					)
					continue
				}
			}
			accumulatedIDs[k] = v
		}
		slog.Debug("ebook enrichment: search result",
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
		result, getErr := mp.GetMetadata(ctx, buildEbookMetadataRequest(accumulator.ProviderIDs, item.Language))
		if getErr != nil {
			slog.Warn("ebook enrichment: GetMetadata error",
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

		slog.Debug("ebook enrichment: metadata received",
			"provider", p.Slug(),
			"content_id", item.ContentID,
			"has_poster", result.PosterPath != "",
			"has_overview", result.Overview != "",
		)
	}

	return accumulator, accumulator.ProviderIDs, providerErrs
}

func (e *Enricher) autoLinkLiteraryWork(ctx context.Context, contentID string) {
	if e == nil || e.workLinker == nil || strings.TrimSpace(contentID) == "" {
		return
	}
	workID, linked, err := e.workLinker.AutoLinkContent(ctx, contentID)
	if err != nil {
		slog.Warn("ebook enrichment: literary work auto-link failed", "content_id", contentID, "error", err)
		return
	}
	if linked {
		slog.Info("ebook enrichment: literary work auto-linked", "content_id", contentID, "work_id", workID)
	}
}

func (e *Enricher) cacheRemotePoster(ctx context.Context, contentID string, result *metadata.MetadataResult) {
	if e == nil || result == nil || result.PosterPath == "" {
		return
	}
	if !strings.HasPrefix(result.PosterPath, "http://") && !strings.HasPrefix(result.PosterPath, "https://") {
		return
	}
	if isNilImageCacher(e.imageCacher) {
		return
	}

	cached, err := e.imageCacher.CacheImage(ctx, metadata.CacheImageRequest{
		SourceURL:   result.PosterPath,
		ProviderID:  ebookMetadataImageProviderID,
		ContentType: "ebooks",
		ContentID:   contentID,
		ImageType:   metadata.ImagePoster,
	})
	if err != nil {
		slog.Warn("ebook enrichment: poster cache failed, keeping provider URL",
			"content_id", contentID,
			"url", result.PosterPath,
			"error", err,
		)
		return
	}
	if cached == nil {
		slog.Warn("ebook enrichment: poster cache returned no result, keeping provider URL",
			"content_id", contentID,
			"url", result.PosterPath,
		)
		return
	}

	if storedPath := cachedOriginalImagePath(cached.BasePath, cached.Ext); storedPath != "" {
		result.PosterPath = storedPath
	}
	if cached.Thumbhash != "" {
		result.PosterThumbhash = cached.Thumbhash
	}
}

func (e *Enricher) enqueueRemoteArtwork(ctx context.Context, contentID string, result *metadata.MetadataResult) {
	if e == nil || e.imageCacheJobs == nil || result == nil || contentID == "" {
		return
	}
	inputs := make([]metadata.EnqueueImageCacheJobInput, 0, 3)
	add := func(sourcePath string, imageType metadata.ImageType) {
		if !isRemoteHTTPImage(sourcePath) {
			return
		}
		inputs = append(inputs, metadata.EnqueueImageCacheJobInput{
			TargetType:        metadata.ImageCacheTargetItem,
			TargetContentID:   contentID,
			SeriesID:          contentID,
			SourcePath:        sourcePath,
			ProviderID:        ebookMetadataImageProviderID,
			ProviderContentID: contentID,
			ContentType:       "ebooks",
			ImageType:         metadata.ImageTypeToString(imageType),
		})
	}
	add(result.PosterPath, metadata.ImagePoster)
	add(result.BackdropPath, metadata.ImageBackdrop)
	add(result.LogoPath, metadata.ImageLogo)
	if len(inputs) == 0 {
		return
	}
	enqueueCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if _, err := e.imageCacheJobs.EnqueueBatch(enqueueCtx, inputs); err != nil {
		slog.Warn("ebook enrichment: failed to enqueue image cache jobs",
			"content_id", contentID,
			"count", len(inputs),
			"error", err,
		)
	}
}

func isRemoteHTTPImage(path string) bool {
	return strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://")
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

	providerIDs = filterEbookProviderIDs(providerIDs)
	if e.providerIDs != nil && len(providerIDs) > 0 {
		if err := e.providerIDs.ReplaceByContentID(ctx, contentID, providerIDs); err != nil {
			slog.Warn("ebook enrichment: failed to persist provider IDs",
				"content_id", contentID,
				"error", err,
			)
		}
	}

	if err := e.updateMetadataAndTimestamps(ctx, contentID, upd); err != nil {
		return err
	}

	authors := filterEbookPeople(result.People)
	if len(authors) > 0 && e.personRepo != nil && e.itemRepo != nil {
		if err := e.persistPeople(ctx, contentID, authors); err != nil {
			slog.Warn("ebook enrichment: failed to persist people",
				"content_id", contentID,
				"error", err,
			)
		}
	}

	return nil
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
		DELETE FROM ebook_enrichment_state WHERE content_id = $1
	`, contentID)
	return err
}

// recordEnrichFailure increments the item's ebook_enrichment_state failure
// counter so claimBatch deprioritizes it on the next sweep and stops claiming
// it at enrichFailureCap. The state is dedicated to ebook enrichment;
// media_items.refresh_failures is owned by the metadata refresh-debt system
// and is never touched here.
func (e *Enricher) recordEnrichFailure(ctx context.Context, item enrichmentItemRow) {
	if e == nil || e.pool == nil {
		return
	}
	if _, err := e.pool.Exec(ctx, `
		INSERT INTO ebook_enrichment_state (content_id, failures, updated_at)
		VALUES ($1, 1, NOW())
		ON CONFLICT (content_id) DO UPDATE SET
			failures   = ebook_enrichment_state.failures + 1,
			updated_at = NOW()
	`, item.ContentID); err != nil {
		slog.Warn("ebook enrichment: failed to record enrichment failure",
			"content_id", item.ContentID,
			"error", err,
		)
	}
}

func (e *Enricher) persistPeople(ctx context.Context, contentID string, people []models.ItemPerson) error {
	people = filterEbookPeople(people)
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
	return e.itemRepo.ReplacePeople(ctx, contentID, mergeEbookAuthorCredits(existing, linked))
}

// mergeEbookAuthorCredits mirrors the scanner's mergeEbookPeople semantics:
// the provider authors replace existing author (and stale narrator) credits,
// while every other curated people kind on the item is preserved.
func mergeEbookAuthorCredits(existing []models.ItemPerson, authors []models.ItemPerson) []models.ItemPerson {
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

func filterEbookPeople(people []models.ItemPerson) []models.ItemPerson {
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

// cleanEbookSearchTitle normalizes a stored title for provider search. Scanner
// titles are often filesystem-derived: underscores stand in for colons or
// spaces ("Exit Strategy_ The Murderbot" / "LTB_067_Micky_Maus"), and
// path-fallback titles keep a trailing " - <Author>" segment. Both wreck a
// title search, so collapse underscores to spaces and drop a trailing author
// suffix (the author is searched as its own field).
// ebookTrailingGroupRE matches a single trailing (...) or [...] group.
var ebookTrailingGroupRE = regexp.MustCompile(`\s*[\(\[]([^\)\]]*)[\)\]]\s*$`)

// ebookSeriesNoiseRE flags a parenthetical as series/edition noise rather than
// part of the real title: a book/volume/part marker, a "#N", or a bare year.
var ebookSeriesNoiseRE = regexp.MustCompile(`(?i)\b(book|bk|vol|volume|series|part|saga|edition|novella?)\b|#\s*\d|^\s*\d{1,4}\s*$|\b(19|20)\d{2}\b`)

// ebookYearOnlyRE matches a parenthetical that is nothing but a year. Years are
// already carried by SearchQuery.Year, so they are dropped from the text rather
// than folded back in.
var ebookYearOnlyRE = regexp.MustCompile(`^\s*(19|20)\d{2}\s*$`)

func cleanEbookSearchTitle(title, author string) string {
	title = strings.ReplaceAll(title, "_", " ")
	if a := strings.TrimSpace(author); a != "" {
		// Strip the author only when it is a true trailing suffix (optionally
		// followed by a series/volume parenthetical). Anchoring to the end avoids
		// truncating valid title text when " - <author>" appears mid-title.
		authorSuffixRE := regexp.MustCompile(`(?i)\s-\s*` + regexp.QuoteMeta(a) + `(?:\s*[\(\[][^)\]]*[\)\]])*\s*$`)
		title = authorSuffixRE.ReplaceAllString(title, "")
	}
	// Normalize trailing series/edition parentheticals. A bare year ("(2019)")
	// is dropped because SearchQuery.Year already carries it. A series/volume
	// marker ("(The Raven Brothers Book 4)", "[#3]") is UNWRAPPED — its words
	// are kept, only the brackets removed — because the volume number is the
	// per-volume disambiguator: dropping it makes every entry in a series search
	// as the bare series name and collapse onto a single provider work. Other
	// parentheticals ("(Illustrated)") are meaningful title text and survive.
	for {
		m := ebookTrailingGroupRE.FindStringSubmatch(title)
		if m == nil {
			break
		}
		inner := strings.TrimSpace(m[1])
		base := strings.TrimSpace(title[:len(title)-len(m[0])])
		if base == "" {
			break // never reduce the title to nothing
		}
		if ebookYearOnlyRE.MatchString(inner) {
			title = base
			continue // peel stacked groups (e.g. a year behind a series marker)
		}
		if ebookSeriesNoiseRE.MatchString(inner) {
			title = base + " " + inner
			break
		}
		break // meaningful parenthetical — leave intact
	}
	return strings.Join(strings.Fields(title), " ")
}

func buildEbookSearchQuery(item enrichmentItemRow) (metadata.SearchQuery, map[string]string) {
	accumulatedIDs := filterEbookProviderIDs(item.ProviderIDs)
	if accumulatedIDs == nil {
		accumulatedIDs = map[string]string{}
	}
	return metadata.SearchQuery{
		Title:       cleanEbookSearchTitle(item.Title, item.Author),
		Author:      item.Author,
		Year:        item.Year,
		ContentType: ebookContentType(),
		ProviderIDs: accumulatedIDs,
		Language:    item.Language,
	}, accumulatedIDs
}

func buildEbookMetadataRequest(providerIDs map[string]string, language string) metadata.MetadataRequest {
	return metadata.MetadataRequest{
		ProviderIDs: filterEbookProviderIDs(providerIDs),
		ContentType: ebookContentType(),
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
	for k, v := range filterEbookProviderIDs(src.ProviderIDs) {
		if v != "" {
			if _, exists := dst.ProviderIDs[k]; !exists {
				dst.ProviderIDs[k] = v
			}
		}
	}
}

func filterEbookProviderIDs(providerIDs map[string]string) map[string]string {
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
		if isEbookASINProvider(provider) {
			continue
		}
		filtered[provider] = providerID
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

func isEbookASINProvider(provider string) bool {
	normalized := strings.ReplaceAll(strings.ReplaceAll(provider, "_", ""), "-", "")
	return normalized == "asin" || normalized == "audibleasin"
}

func providerIDMapFromRows(rows []*models.MediaItemProviderID) map[string]string {
	if len(rows) == 0 {
		return nil
	}
	m := make(map[string]string, len(rows))
	for _, r := range rows {
		if r != nil {
			for provider, providerID := range filterEbookProviderIDs(map[string]string{
				r.Provider: r.ProviderID,
			}) {
				m[provider] = providerID
			}
		}
	}
	return m
}
