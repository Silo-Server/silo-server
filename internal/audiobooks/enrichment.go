package audiobooks

// Enricher periodically enriches audiobook media_items that are missing
// metadata (poster_path, overview, etc.) by querying the configured
// metadata-provider chain for each item's library folder.
//
// Design: periodic sweep (option c from the plan) — no queue table required.
// The sweep selects up to batchSize audiobook items where poster_path is empty,
// resolves the per-folder provider chain at content_level='audiobook', calls
// Search + GetMetadata on each enabled provider in order, and writes results
// back via ItemRepository.UpdateMetadata + PersonRepository + ItemRepository.ReplacePeople.
//
// Movie/TV enrichment is entirely unaffected: it continues through
// internal/metadata.MetadataService.Process via the existing worker.

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

// audiobookCoverCacher is a narrow shape matching scanner.audiobookCoverCacher's
// method set by structural typing. Anything implementing CacheAudiobookCover can
// be passed to scanner.ExtractAndUploadAudiobookCover via this interface.
type audiobookCoverCacher interface {
	CacheAudiobookCover(ctx context.Context, data []byte, contentID string) (basePath string, ext string, thumbhash string, err error)
}

const (
	// defaultEnrichBatchSize is the maximum number of audiobook items processed
	// per sweep invocation. Keeps latency bounded for large libraries.
	defaultEnrichBatchSize = 50
)

// enrichmentItemRow is a minimal projection of media_items joined to
// media_item_libraries. We only read what we need to call the provider chain.
type enrichmentItemRow struct {
	ContentID   string
	Title       string
	Year        int
	FolderID    int
	Language    string // resolved from media_folders.metadata_language
	Author      string // from item_people where kind = PersonKindAuthor (7), best-effort
	ProviderIDs map[string]string
}

// Enricher drives the audiobook metadata enrichment sweep.
type Enricher struct {
	pool        *pgxpool.Pool
	chainRepo   *metadata.ChainRepository
	resolver    *metadata.PluginResolverAdapter
	itemRepo    *catalog.ItemRepository
	personRepo  *catalog.PersonRepository
	providerIDs *catalog.ProviderIDRepository
	imageCacher audiobookCoverCacher
	ffmpegPath  string
	batchSize   int
}

// NewEnricher constructs an Enricher.  All fields are required; a nil pool or
// chainRepo causes the sweep to be a no-op (safe for tests that don't wire
// everything up).
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
	}
}

// SetImageCacher installs the audiobook-cover cacher used by the deferred
// local-cover fallback. Safe to call once after construction; nil disables
// the fallback. Mirrors MetadataService.SetImageCacher / Scanner.SetImageCacher.
func (e *Enricher) SetImageCacher(cacher audiobookCoverCacher) {
	if e == nil {
		return
	}
	e.imageCacher = cacher
}

// SetFFmpegPath installs the ffmpeg binary path used by the deferred
// local-cover fallback. Empty string disables the fallback.
func (e *Enricher) SetFFmpegPath(path string) {
	if e == nil {
		return
	}
	e.ffmpegPath = path
}

// Run executes one sweep. It is safe to call concurrently (each call does its
// own SELECT FOR UPDATE SKIP LOCKED to avoid double-processing) but the task
// manager will only call it from a single goroutine on its interval.
func (e *Enricher) Run(ctx context.Context) (int, error) {
	if e == nil || e.pool == nil || e.chainRepo == nil {
		return 0, nil
	}

	items, err := e.claimBatch(ctx)
	if err != nil {
		return 0, fmt.Errorf("audiobook enrichment: claim batch: %w", err)
	}
	if len(items) == 0 {
		return 0, nil
	}

	slog.Info("audiobook enrichment: sweep started", "count", len(items))

	enriched := 0
	for _, item := range items {
		if ctx.Err() != nil {
			break
		}
		if err := e.enrichItem(ctx, item); err != nil {
			slog.Warn("audiobook enrichment: item failed",
				"content_id", item.ContentID,
				"title", item.Title,
				"error", err,
			)
			continue
		}
		enriched++
	}

	slog.Info("audiobook enrichment: sweep complete",
		"attempted", len(items),
		"enriched", enriched,
	)
	return enriched, nil
}

// claimBatch returns up to batchSize audiobook items that need enrichment.
// "Needs enrichment" means poster_path IS NULL or empty AND last_refreshed IS NULL.
// We skip items where last_refreshed IS NOT NULL — those have already had at
// least one enrichment pass regardless of outcome.
func (e *Enricher) claimBatch(ctx context.Context) ([]enrichmentItemRow, error) {
	// One query: join media_item_libraries to get folder_id, join media_folders
	// for metadata_language, and LEFT JOIN item_people to get the author name.
	rows, err := e.pool.Query(ctx, `
		SELECT
			mi.content_id,
			mi.title,
			mi.year,
			COALESCE(mil.media_folder_id, 0)   AS folder_id,
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
		WHERE mi.type = 'audiobook'
		  AND (mi.poster_path IS NULL OR mi.poster_path = '')
		  AND mi.last_refreshed IS NULL
		ORDER BY mi.created_at ASC
		LIMIT $1
	`, e.batchSize)
	if err != nil {
		return nil, fmt.Errorf("querying unenriched audiobooks: %w", err)
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
			return nil, fmt.Errorf("scanning audiobook enrichment row: %w", err)
		}
		// Deduplicate: a book can be in multiple libraries; process once.
		if _, dup := seen[item.ContentID]; dup {
			continue
		}
		seen[item.ContentID] = struct{}{}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating audiobook enrichment rows: %w", err)
	}

	// Load any durable provider IDs for each item.
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

// enrichItem runs the Search → GetMetadata → persist cycle for a single item.
func (e *Enricher) enrichItem(ctx context.Context, item enrichmentItemRow) error {
	defer e.maybeApplyCoverFallback(ctx, item.ContentID)

	if item.FolderID == 0 {
		slog.Debug("audiobook enrichment: item has no library folder, skipping",
			"content_id", item.ContentID,
			"title", item.Title,
		)
		// Still stamp last_refreshed so we don't loop forever on orphaned items.
		return e.stampLastRefreshed(ctx, item.ContentID)
	}

	providers, err := metadata.ResolveChain(ctx, item.FolderID, "audiobook", e.chainRepo, e.resolver)
	if err != nil {
		return fmt.Errorf("resolving audiobook chain for folder %d: %w", item.FolderID, err)
	}
	if len(providers) == 0 {
		slog.Debug("audiobook enrichment: no providers in chain",
			"content_id", item.ContentID,
			"folder_id", item.FolderID,
		)
		return e.stampLastRefreshed(ctx, item.ContentID)
	}

	// Seed accumulated provider IDs from durable store.
	accumulatedIDs := make(map[string]string, len(item.ProviderIDs))
	for k, v := range item.ProviderIDs {
		accumulatedIDs[k] = v
	}

	// Phase 1: Search — collect provider IDs.
	searchQuery := metadata.SearchQuery{
		Title:       item.Title,
		Year:        item.Year,
		ContentType: "audiobook",
		ProviderIDs: accumulatedIDs,
		Language:    item.Language,
	}
	// Include author in the search query title hint when present.
	// The audnexus plugin uses title+author for ASIN lookup.
	if item.Author != "" {
		searchQuery.Title = item.Title
		// Some plugins accept author via the generic extras pathway; others rely
		// on the title field only. We pass it as a secondary field (no standard
		// slot exists in SearchQuery yet).
	}

	for _, p := range providers {
		sp, ok := p.(metadata.SearchProvider)
		if !ok {
			continue
		}
		results, searchErr := sp.Search(ctx, searchQuery)
		if searchErr != nil {
			slog.Warn("audiobook enrichment: search error",
				"provider", p.Slug(),
				"content_id", item.ContentID,
				"error", searchErr,
			)
			continue
		}
		if len(results) == 0 {
			continue
		}
		// Take the first result's IDs as a candidate; later providers may fill gaps.
		for k, v := range results[0].ProviderIDs {
			if v != "" {
				if _, exists := accumulatedIDs[k]; !exists {
					accumulatedIDs[k] = v
				}
			}
		}
		slog.Debug("audiobook enrichment: search result",
			"provider", p.Slug(),
			"content_id", item.ContentID,
			"matched_ids", accumulatedIDs,
		)
	}

	// Phase 2: GetMetadata — merge results.
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
			ContentType: "audiobook",
			Language:    item.Language,
		})
		if getErr != nil {
			slog.Warn("audiobook enrichment: GetMetadata error",
				"provider", p.Slug(),
				"content_id", item.ContentID,
				"error", getErr,
			)
			continue
		}
		if result == nil || !result.HasMetadata {
			continue
		}
		// Bootstrap subsequent providers with any newly discovered IDs.
		mergeEnrichmentProviderIDs(accumulator, result)
		accumulatedIDs = accumulator.ProviderIDs
		metadata.MergeMetadata(result, accumulator, nil, metadata.MergeFillEmpty)

		slog.Debug("audiobook enrichment: metadata received",
			"provider", p.Slug(),
			"content_id", item.ContentID,
			"has_poster", result.PosterPath != "",
			"has_overview", result.Overview != "",
		)
	}

	// Nothing found — stamp last_refreshed so we skip on the next sweep.
	if !accumulator.HasMetadata && accumulator.PosterPath == "" && accumulator.Overview == "" {
		slog.Info("audiobook enrichment: no metadata found",
			"content_id", item.ContentID,
			"title", item.Title,
		)
		return e.stampLastRefreshed(ctx, item.ContentID)
	}

	// Phase 3: Persist.
	if err := e.persist(ctx, item.ContentID, accumulatedIDs, accumulator); err != nil {
		return fmt.Errorf("persisting enrichment for %s: %w", item.ContentID, err)
	}

	slog.Info("audiobook enrichment: enriched",
		"content_id", item.ContentID,
		"title", item.Title,
		"poster", accumulator.PosterPath != "",
		"overview", accumulator.Overview != "",
		"people", len(accumulator.People),
	)

	return nil
}

// maybeApplyCoverFallback wraps applyLocalCoverFallback with the nil-guards
// and warn-logging, suitable for `defer` at the top of enrichItem so every
// exit path (success and early returns alike) gets a fallback attempt.
// Idempotent — applyLocalCoverFallback no-ops when poster_path is already set.
func (e *Enricher) maybeApplyCoverFallback(ctx context.Context, contentID string) {
	if e.imageCacher == nil || e.ffmpegPath == "" {
		return
	}
	if err := e.applyLocalCoverFallback(ctx, contentID); err != nil {
		slog.Warn("audiobook enrichment: local cover fallback failed",
			"content_id", contentID,
			"error", err,
		)
	}
}

// persist writes the enriched metadata back to the database.
func (e *Enricher) persist(ctx context.Context, contentID string, providerIDs map[string]string, result *metadata.MetadataResult) error {
	// Build the MetadataUpdate — only set fields that the provider returned.
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

	// Write provider IDs (ASIN, etc.) to the durable provider_id table.
	if e.providerIDs != nil && len(providerIDs) > 0 {
		if err := e.providerIDs.ReplaceByContentID(ctx, contentID, providerIDs); err != nil {
			// Non-fatal: log and continue.
			slog.Warn("audiobook enrichment: failed to persist provider IDs",
				"content_id", contentID,
				"error", err,
			)
		}
	}

	// Write scalar metadata and stamp last_refreshed + matched_at.
	if err := e.updateMetadataAndTimestamps(ctx, contentID, upd); err != nil {
		return err
	}

	// Write people (authors / narrators).
	if len(result.People) > 0 && e.personRepo != nil && e.itemRepo != nil {
		if err := e.persistPeople(ctx, contentID, result.People); err != nil {
			// Non-fatal: log and continue so at least scalar metadata is saved.
			slog.Warn("audiobook enrichment: failed to persist people",
				"content_id", contentID,
				"error", err,
			)
		}
	}

	return nil
}

// updateMetadataAndTimestamps runs UpdateMetadata and also stamps
// last_refreshed and matched_at so the item is skipped on future sweeps.
func (e *Enricher) updateMetadataAndTimestamps(ctx context.Context, contentID string, upd *catalog.MetadataUpdate) error {
	if e.itemRepo == nil {
		return nil
	}
	if err := e.itemRepo.UpdateMetadata(ctx, contentID, upd); err != nil {
		return fmt.Errorf("UpdateMetadata: %w", err)
	}
	return e.stampLastRefreshed(ctx, contentID)
}

// stampLastRefreshed updates last_refreshed (and matched_at when still NULL)
// so the sweep won't re-process this item.
func (e *Enricher) stampLastRefreshed(ctx context.Context, contentID string) error {
	if e.pool == nil {
		return nil
	}
	now := time.Now().UTC()
	_, err := e.pool.Exec(ctx, `
		UPDATE media_items
		SET last_refreshed = $1,
		    matched_at     = COALESCE(matched_at, $1),
		    status         = CASE WHEN status = 'pending' THEN 'matched' ELSE status END
		WHERE content_id = $2
	`, now, contentID)
	return err
}

// persistPeople upserts author/narrator records into people + item_people.
func (e *Enricher) persistPeople(ctx context.Context, contentID string, people []models.ItemPerson) error {
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

// applyLocalCoverFallback reads the embedded cover from the audiobook's
// primary audio file and writes it to poster_path if (and only if) the
// item still has no poster after provider enrichment. Best-effort; errors
// are logged by the caller, not returned to fail the sweep.
func (e *Enricher) applyLocalCoverFallback(ctx context.Context, contentID string) error {
	var posterPath string
	var primaryFile string
	err := e.pool.QueryRow(ctx, `
		SELECT COALESCE(mi.poster_path, ''), COALESCE(mf.file_path, '')
		FROM media_items mi
		LEFT JOIN LATERAL (
			SELECT file_path FROM media_files
			WHERE content_id = mi.content_id
			ORDER BY id ASC
			LIMIT 1
		) mf ON TRUE
		WHERE mi.content_id = $1
	`, contentID).Scan(&posterPath, &primaryFile)
	if err != nil {
		return fmt.Errorf("read item for cover fallback: %w", err)
	}
	if posterPath != "" || primaryFile == "" {
		return nil
	}

	poster, thumb := scanner.ExtractAndUploadAudiobookCover(ctx, e.ffmpegPath, e.imageCacher, primaryFile, contentID)
	if poster == "" {
		return nil
	}
	if _, err := e.pool.Exec(ctx, `
		UPDATE media_items
		SET poster_path = $1, poster_thumbhash = $2, updated_at = NOW()
		WHERE content_id = $3 AND (poster_path IS NULL OR poster_path = '')
	`, poster, thumb, contentID); err != nil {
		return fmt.Errorf("update poster_path: %w", err)
	}
	return nil
}

// mergeEnrichmentProviderIDs copies new provider IDs from src into dst,
// without overwriting existing entries.
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

// providerIDMapFromRows converts a slice of MediaItemProviderID DB rows
// into the map form used by provider calls.
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
