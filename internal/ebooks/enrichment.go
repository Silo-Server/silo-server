package ebooks

// Enricher periodically enriches ebook media_items that are missing metadata
// by querying the configured metadata-provider chain for each item's library
// folder.

import (
	"context"
	"fmt"
	"log/slog"
	"os"
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
)

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
	pool        *pgxpool.Pool
	chainRepo   *metadata.ChainRepository
	resolver    *metadata.PluginResolverAdapter
	itemRepo    *catalog.ItemRepository
	personRepo  *catalog.PersonRepository
	providerIDs *catalog.ProviderIDRepository
	imageCacher metadata.ImageCacher
	batchSize   int
	workers     int
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

	enriched := e.runBatch(ctx, items, e.enrichItem)

	slog.Info("ebook enrichment: sweep complete",
		"attempted", len(items),
		"enriched", enriched,
	)
	return enriched, nil
}

func (e *Enricher) runBatch(ctx context.Context, items []enrichmentItemRow, enrichFn func(context.Context, enrichmentItemRow) error) int {
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
					slog.Warn("ebook enrichment: item failed",
						"content_id", item.ContentID,
						"title", item.Title,
						"error", err,
					)
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

func (e *Enricher) claimBatch(ctx context.Context) ([]enrichmentItemRow, error) {
	rows, err := e.pool.Query(ctx, `
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
		WHERE mi.type = 'ebook'
		  AND (mi.poster_path IS NULL OR mi.poster_path = '')
		  AND mi.last_refreshed IS NULL
		ORDER BY mi.created_at ASC
		LIMIT $1
	`, e.batchSize)
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
		slog.Debug("ebook enrichment: item has no library folder, skipping",
			"content_id", item.ContentID,
			"title", item.Title,
		)
		return e.stampLastRefreshed(ctx, item.ContentID)
	}

	providers, err := metadata.ResolveChain(ctx, item.FolderID, ebookContentType(), e.chainRepo, e.resolver)
	if err != nil {
		return fmt.Errorf("resolving ebook chain for folder %d: %w", item.FolderID, err)
	}
	if len(providers) == 0 {
		slog.Debug("ebook enrichment: no providers in chain",
			"content_id", item.ContentID,
			"folder_id", item.FolderID,
		)
		return e.stampLastRefreshed(ctx, item.ContentID)
	}

	accumulatedIDs := make(map[string]string, len(item.ProviderIDs))
	for k, v := range item.ProviderIDs {
		accumulatedIDs[k] = v
	}

	searchQuery := metadata.SearchQuery{
		Title:       item.Title,
		Year:        item.Year,
		ContentType: ebookContentType(),
		ProviderIDs: accumulatedIDs,
		Language:    item.Language,
	}

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
		result, getErr := mp.GetMetadata(ctx, metadata.MetadataRequest{
			ProviderIDs: accumulatedIDs,
			ContentType: ebookContentType(),
			Language:    item.Language,
		})
		if getErr != nil {
			slog.Warn("ebook enrichment: GetMetadata error",
				"provider", p.Slug(),
				"content_id", item.ContentID,
				"error", getErr,
			)
			continue
		}
		if result == nil || !result.HasMetadata {
			continue
		}
		mergeEnrichmentProviderIDs(accumulator, result)
		accumulatedIDs = accumulator.ProviderIDs
		metadata.MergeMetadata(result, accumulator, nil, metadata.MergeFillEmpty)

		slog.Debug("ebook enrichment: metadata received",
			"provider", p.Slug(),
			"content_id", item.ContentID,
			"has_poster", result.PosterPath != "",
			"has_overview", result.Overview != "",
		)
	}

	if !accumulator.HasMetadata && accumulator.PosterPath == "" && accumulator.Overview == "" {
		slog.Info("ebook enrichment: no metadata found",
			"content_id", item.ContentID,
			"title", item.Title,
		)
		return e.stampLastRefreshed(ctx, item.ContentID)
	}

	e.cacheRemotePoster(ctx, item.ContentID, accumulator)

	if err := e.persist(ctx, item.ContentID, accumulatedIDs, accumulator); err != nil {
		return fmt.Errorf("persisting enrichment for %s: %w", item.ContentID, err)
	}

	slog.Info("ebook enrichment: enriched",
		"content_id", item.ContentID,
		"title", item.Title,
		"poster", accumulator.PosterPath != "",
		"overview", accumulator.Overview != "",
		"people", len(filterEbookPeople(accumulator.People)),
	)

	return nil
}

func (e *Enricher) cacheRemotePoster(ctx context.Context, contentID string, result *metadata.MetadataResult) {
	if e == nil || result == nil || result.PosterPath == "" {
		return
	}
	if !strings.HasPrefix(result.PosterPath, "http://") && !strings.HasPrefix(result.PosterPath, "https://") {
		return
	}
	if e.imageCacher == nil {
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

	if storedPath := cachedOriginalImagePath(cached.BasePath, cached.Ext); storedPath != "" {
		result.PosterPath = storedPath
	}
	if cached.Thumbhash != "" {
		result.PosterThumbhash = cached.Thumbhash
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
	}
	if result.PosterThumbhash != "" {
		upd.PosterThumbhash = &result.PosterThumbhash
	}
	if result.BackdropPath != "" {
		upd.BackdropPath = &result.BackdropPath
	}
	if result.BackdropThumbhash != "" {
		upd.BackdropThumbhash = &result.BackdropThumbhash
	}
	if result.LogoPath != "" {
		upd.LogoPath = &result.LogoPath
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
	_, err := e.pool.Exec(ctx, `
		UPDATE media_items
		SET last_refreshed = $1,
		    matched_at = COALESCE(matched_at, $1),
		    status = CASE WHEN status = 'pending' THEN 'matched' ELSE status END
		WHERE content_id = $2
	`, now, contentID)
	return err
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
	return e.itemRepo.ReplacePeople(ctx, contentID, linked)
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

func mergeEnrichmentProviderIDs(dst *metadata.MetadataResult, src *metadata.MetadataResult) {
	if src == nil || len(src.ProviderIDs) == 0 {
		return
	}
	if dst.ProviderIDs == nil {
		dst.ProviderIDs = make(map[string]string, len(src.ProviderIDs))
	}
	for k, v := range src.ProviderIDs {
		if v != "" {
			if _, exists := dst.ProviderIDs[k]; !exists {
				dst.ProviderIDs[k] = v
			}
		}
	}
}

func providerIDMapFromRows(rows []*models.MediaItemProviderID) map[string]string {
	if len(rows) == 0 {
		return nil
	}
	m := make(map[string]string, len(rows))
	for _, r := range rows {
		if r != nil && strings.TrimSpace(r.Provider) != "" && strings.TrimSpace(r.ProviderID) != "" {
			m[strings.TrimSpace(r.Provider)] = strings.TrimSpace(r.ProviderID)
		}
	}
	return m
}
