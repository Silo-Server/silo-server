package audiobooks

// Enricher periodically enriches audiobook media_items that are missing
// metadata (poster_path, overview, etc.) by querying the configured
// metadata-provider chain for each item's library folder.
//
// Design: a periodic, state-backed sweep selects up to batchSize audiobooks
// that have no durable provider identity, have not completed enrichment, and
// are due for retry. It resolves the per-folder provider chain, calls Search +
// GetMetadata, then commits provider IDs, scalar metadata, the search-index
// event, and terminal timestamp atomically. People credits follow best-effort.
//
// Movie/TV enrichment is entirely unaffected: it continues through
// internal/metadata.MetadataService.Process via the existing worker.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
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
	CacheAudiobookCover(ctx context.Context, data []byte, contentID string) (storedPath string, thumbhash string, err error)
}

const (
	audiobookMetadataImageProviderID = "audiobook-metadata"

	// defaultEnrichBatchSize is the maximum number of audiobook items processed
	// per sweep invocation. Keeps latency bounded for large libraries.
	// Override with SILO_AUDIOBOOK_ENRICH_BATCH_SIZE.
	defaultEnrichBatchSize = 250
	// defaultEnrichWorkers is the default fan-out used by Enricher.Run.
	// Network-bound: each worker holds one provider HTTP call at a time, so
	// 4 is enough to mask single-request latency without hammering plugins.
	// Override with SILO_AUDIOBOOK_ENRICH_WORKERS.
	defaultEnrichWorkers = 4
	// A whole batch is claimed before workers fan out. Keep the lease long
	// enough for the final item in the default 250-item batch to reach its
	// provider, while still allowing another replica to recover abandoned work.
	defaultEnrichClaimLease = 2 * time.Hour
)

// audiobookEnrichBatchSize returns the configured maximum sweep size.
func audiobookEnrichBatchSize() int {
	if v := os.Getenv("SILO_AUDIOBOOK_ENRICH_BATCH_SIZE"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			return parsed
		}
	}
	return defaultEnrichBatchSize
}

// audiobookEnrichWorkers returns the configured number of parallel enrichment
// workers, capped to the active batch size so workers never outnumber the batch
// they drain.
func audiobookEnrichWorkers(batchSize int) int {
	n := defaultEnrichWorkers
	if v := os.Getenv("SILO_AUDIOBOOK_ENRICH_WORKERS"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			n = parsed
		}
	}
	if batchSize > 0 && n > batchSize {
		n = batchSize
	}
	return n
}

// enrichmentItemRow is a minimal projection of media_items joined to
// media_item_libraries. We only read what we need to call the provider chain.
type enrichmentItemRow struct {
	ContentID   string
	ClaimToken  string
	Title       string
	Year        int
	FolderID    int
	Language    string // resolved from media_folders.metadata_language
	Author      string // from item_people where kind = PersonKindAuthor (7), best-effort
	ProviderIDs map[string]string
}

type audiobookProviderIDRepository interface {
	metadata.ProviderIDOwnerLookup
	GetByContentID(ctx context.Context, contentID string) ([]*models.MediaItemProviderID, error)
	ReplaceByContentID(ctx context.Context, contentID string, providerIDs map[string]string) error
	ReplaceByContentIDTx(ctx context.Context, tx pgx.Tx, contentID, itemType string, providerIDs map[string]string) error
}

type audiobookItemRepository interface {
	UpdateMetadata(ctx context.Context, contentID string, upd *catalog.MetadataUpdate) error
	UpdateMetadataTx(ctx context.Context, tx pgx.Tx, contentID string, upd *catalog.MetadataUpdate) error
	ReplacePeople(ctx context.Context, contentID string, people []models.ItemPerson) error
}

// Enricher drives the audiobook metadata enrichment sweep.
type Enricher struct {
	pool             *pgxpool.Pool
	chainRepo        *metadata.ChainRepository
	resolver         *metadata.PluginResolverAdapter
	itemRepo         audiobookItemRepository
	personRepo       *catalog.PersonRepository
	providerIDs      audiobookProviderIDRepository
	state            *enrichmentStateStore
	imageCacher      audiobookCoverCacher
	imageCacheJobs   metadata.ImageCacheJobEnqueuer
	workLinker       literaryWorkLinker
	resolveProviders func(context.Context, int, string) ([]metadata.Provider, error)
	ffmpegPath       string
	batchSize        int
	workers          int
}

type literaryWorkLinker interface {
	AutoLinkContent(ctx context.Context, contentID string) (workID string, linked bool, err error)
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
	batchSize := audiobookEnrichBatchSize()
	return &Enricher{
		pool:        pool,
		chainRepo:   chainRepo,
		resolver:    resolver,
		itemRepo:    itemRepo,
		personRepo:  personRepo,
		providerIDs: providerIDs,
		state:       newEnrichmentStateStore(pool),
		batchSize:   batchSize,
		workers:     audiobookEnrichWorkers(batchSize),
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

	slog.InfoContext(ctx, "audiobook enrichment: sweep started", "component", "audiobooks",
		"count", len(items),
		"workers", e.workers,
	)

	enriched := e.runBatch(ctx, items, e.enrichItem)

	slog.InfoContext(ctx, "audiobook enrichment: sweep complete", "component", "audiobooks",
		"attempted", len(items),
		"enriched", enriched,
	)
	return enriched, nil
}

// HasPendingItems reports whether a scheduled sweep has any audiobook rows to
// process. It mirrors claimBatch's eligibility predicate without loading rows
// or provider IDs, so the scheduler can skip no-op executions without adding
// task-history noise.
func (e *Enricher) HasPendingItems(ctx context.Context) (bool, error) {
	if e == nil || e.pool == nil || e.chainRepo == nil {
		return false, nil
	}

	var exists bool
	err := e.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM media_items mi
			WHERE mi.type = 'audiobook'
			  AND NOT EXISTS (
			      SELECT 1
			      FROM media_item_provider_ids p
			      WHERE p.content_id = mi.content_id
			  )
			  AND mi.last_refreshed IS NULL
			  AND NOT EXISTS (
			      SELECT 1
			      FROM audiobook_enrichment_state s
			      WHERE s.content_id = mi.content_id
			        AND (
			            s.next_attempt_at > now()
			            OR s.lease_until > now()
			        )
			  )
			LIMIT 1
		)
	`).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("checking pending audiobook enrichment: %w", err)
	}
	return exists, nil
}

// runBatch processes items in parallel using e.workers goroutines. The
// enrichFn parameter exists for testing: production calls pass e.enrichItem.
// Returns the count of items where enrichFn returned nil.
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
					continue // drain
				}
				if err := enrichFn(ctx, item); err != nil {
					slog.WarnContext(ctx, "audiobook enrichment: item failed", "component", "audiobooks",
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

// claimBatch returns up to batchSize audiobook items that need enrichment.
// "Needs enrichment" means the item has no provider identity at all AND
// last_refreshed IS NULL. We skip items where last_refreshed IS NOT NULL —
// those have already had at least one enrichment pass regardless of outcome,
// which is what bounds retries: enrichItem stamps last_refreshed on a clean
// no-match and deliberately withholds it on provider error, so unmatchable
// items are tried once while transient failures come back.
//
// This keys on identity rather than on poster_path, which is what it used to
// test. Audiobook files essentially always carry embedded cover art, so the
// scanner gave every item a poster before enrichment ever looked at it and the
// old predicate matched nothing: in production it selected 0 rows while 5,712
// audiobooks had no provider ID, 5,710 of them holding a scanner-supplied
// poster. Cover presence says nothing about whether an item was identified.
func (e *Enricher) claimBatch(ctx context.Context) ([]enrichmentItemRow, error) {
	// Select and lease work in one statement. The media-item row locks prevent
	// two replicas from choosing the same fresh row, while the conflict filter
	// fences rows that already have an unexpired lease or parked retry.
	rows, err := e.pool.Query(ctx, `
		WITH candidates AS (
			SELECT mi.content_id
			FROM media_items mi
			WHERE mi.type = 'audiobook'
			  AND NOT EXISTS (
			      SELECT 1
			      FROM media_item_provider_ids p
			      WHERE p.content_id = mi.content_id
			  )
			  AND mi.last_refreshed IS NULL
			  AND NOT EXISTS (
			      SELECT 1
			      FROM audiobook_enrichment_state s
			      WHERE s.content_id = mi.content_id
			        AND (
			            s.next_attempt_at > now()
			            OR s.lease_until > now()
			        )
			  )
			ORDER BY mi.created_at ASC, mi.content_id ASC
			FOR UPDATE OF mi SKIP LOCKED
			LIMIT $1
		), claimed AS (
			INSERT INTO audiobook_enrichment_state (
				content_id, claim_token, lease_until, updated_at
			)
			SELECT
				c.content_id,
				gen_random_uuid()::text,
				now() + make_interval(secs => $2::double precision),
				now()
			FROM candidates c
			ON CONFLICT (content_id) DO UPDATE SET
				claim_token = EXCLUDED.claim_token,
				lease_until = EXCLUDED.lease_until,
				updated_at  = now()
			WHERE (
				audiobook_enrichment_state.next_attempt_at IS NULL
				OR audiobook_enrichment_state.next_attempt_at <= now()
			)
			  AND (
				audiobook_enrichment_state.lease_until IS NULL
				OR audiobook_enrichment_state.lease_until <= now()
			)
			RETURNING content_id, claim_token
		)
		SELECT
			mi.content_id,
			c.claim_token,
			mi.title,
			mi.year,
			COALESCE((
				SELECT mil.media_folder_id
				FROM media_item_libraries mil
				WHERE mil.content_id = mi.content_id
				ORDER BY mil.media_folder_id
				LIMIT 1
			), 0) AS folder_id,
			COALESCE((
				SELECT mf.metadata_language
				FROM media_item_libraries mil
				JOIN media_folders mf ON mf.id = mil.media_folder_id
				WHERE mil.content_id = mi.content_id
				ORDER BY mil.media_folder_id
				LIMIT 1
			), 'en') AS language,
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
		FROM claimed c
		JOIN media_items mi ON mi.content_id = c.content_id
		ORDER BY mi.created_at ASC, mi.content_id ASC
	`, e.batchSize, defaultEnrichClaimLease.Seconds())
	if err != nil {
		return nil, fmt.Errorf("querying unenriched audiobooks: %w", err)
	}
	defer rows.Close()

	var items []enrichmentItemRow
	for rows.Next() {
		var item enrichmentItemRow
		if err := rows.Scan(
			&item.ContentID,
			&item.ClaimToken,
			&item.Title,
			&item.Year,
			&item.FolderID,
			&item.Language,
			&item.Author,
		); err != nil {
			return nil, fmt.Errorf("scanning audiobook enrichment row: %w", err)
		}
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
		slog.DebugContext(ctx, "audiobook enrichment: item has no library folder, skipping", "component", "audiobooks",
			"content_id", item.ContentID,
			"title", item.Title,
		)
		// Still stamp last_refreshed so we don't loop forever on orphaned items.
		return e.completeWithoutMetadata(ctx, item, EnrichmentOutcomeSkipped)
	}

	var providers []metadata.Provider
	var err error
	if e.resolveProviders != nil {
		providers, err = e.resolveProviders(ctx, item.FolderID, "audiobook")
	} else {
		providers, err = metadata.ResolveChain(ctx, item.FolderID, "audiobook", e.chainRepo, e.resolver)
	}
	if err != nil {
		resolveErr := fmt.Errorf("resolving audiobook chain for folder %d: %w", item.FolderID, err)
		e.recordFailure(ctx, item, classifyProviderError(resolveErr), resolveErr.Error())
		return resolveErr
	}
	if len(providers) == 0 {
		slog.DebugContext(ctx, "audiobook enrichment: no providers in chain", "component", "audiobooks",
			"content_id", item.ContentID,
			"folder_id", item.FolderID,
		)
		return e.completeWithoutMetadata(ctx, item, EnrichmentOutcomeSkipped)
	}

	// Seed accumulated provider IDs from durable store.
	accumulatedIDs := make(map[string]string, len(item.ProviderIDs))
	for k, v := range item.ProviderIDs {
		accumulatedIDs[k] = v
	}

	// Phase 1: Search — collect provider IDs.
	searchQuery := metadata.SearchQuery{
		Title:       item.Title,
		Author:      item.Author,
		Year:        item.Year,
		ContentType: "audiobook",
		ProviderIDs: accumulatedIDs,
		Language:    item.Language,
	}

	// Track whether any provider errored during this item's enrichment. When
	// nothing is accumulated AND at least one provider errored, we return an
	// error WITHOUT stamping last_refreshed so the sweep retries the item later
	// rather than burning it terminally on a transient provider failure.
	var providerErrs []error

	// The title the first accepting provider settled on; later providers must
	// agree with it before their IDs are merged in.
	var agreedTitle string

	for _, p := range providers {
		sp, ok := p.(metadata.SearchProvider)
		if !ok {
			continue
		}
		results, searchErr := sp.Search(ctx, searchQuery)
		if searchErr != nil {
			slog.WarnContext(ctx, "audiobook enrichment: search error", "component", "audiobooks",
				"provider", p.Slug(),
				"content_id", item.ContentID,
				"error", searchErr,
			)
			providerErrs = append(providerErrs, fmt.Errorf("%s search: %w", p.Slug(), searchErr))
			continue
		}
		admission, admitErr := metadata.AdmitSearchMatch(ctx, metadata.SearchMatchAdmissionRequest{
			WantTitle:           item.Title,
			WantYear:            item.Year,
			Results:             results,
			AgreedTitle:         agreedTitle,
			ExistingProviderIDs: accumulatedIDs,
			Owner:               e.providerIDs,
			ItemType:            "audiobook",
			ContentID:           item.ContentID,
		})
		if admitErr != nil {
			providerErrs = append(providerErrs, fmt.Errorf("%s candidate admission: %w", p.Slug(), admitErr))
			continue
		}
		for _, conflict := range admission.Conflicts {
			slog.InfoContext(ctx, "audiobook enrichment: provider id already owned by another item; skipping", "component", "audiobooks",
				"provider", conflict.Provider,
				"provider_id", conflict.ProviderID,
				"content_id", item.ContentID,
				"owned_by", conflict.OwnedBy,
			)
		}

		switch admission.Status {
		case metadata.SearchMatchNoCredibleMatch:
			// Info, not Debug: during a backlog drain the rejection rate is
			// what separates "threshold too strict" from "providers answering
			// badly", and it cannot be read from a log level nobody enables.
			slog.InfoContext(ctx, "audiobook enrichment: no credible match", "component", "audiobooks",
				"provider", p.Slug(),
				"content_id", item.ContentID,
				"title", item.Title,
				"candidates", len(results),
			)
			continue
		case metadata.SearchMatchProviderDisagreement:
			slog.WarnContext(ctx, "audiobook enrichment: provider disagreement; skipping", "component", "audiobooks",
				"provider", p.Slug(),
				"content_id", item.ContentID,
				"accepted_title", agreedTitle,
				"rejected_title", admission.MatchedTitle,
			)
			continue
		case metadata.SearchMatchProviderIDConflict:
			slog.WarnContext(ctx, "audiobook enrichment: provider identity contradicts an existing ID; skipping", "component", "audiobooks",
				"provider", p.Slug(),
				"content_id", item.ContentID,
			)
			continue
		case metadata.SearchMatchNoUsableProviderIDs:
			continue
		}
		agreedTitle = admission.AgreedTitle
		for k, v := range admission.ProviderIDs {
			accumulatedIDs[k] = v
		}
		slog.DebugContext(ctx, "audiobook enrichment: search result", "component", "audiobooks",
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
			slog.WarnContext(ctx, "audiobook enrichment: GetMetadata error", "component", "audiobooks",
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
		// Validate each provider before its fields or IDs enter the shared
		// accumulator. Checking only the aggregate lets a later correct author
		// validate an earlier same-title result for a different book.
		if !metadata.AuthorsAgree(item.Author, result.People) {
			slog.InfoContext(ctx, "audiobook enrichment: author mismatch; treating as no match", "component", "audiobooks",
				"provider", p.Slug(),
				"content_id", item.ContentID,
				"title", item.Title,
				"item_author", item.Author,
			)
			if len(providerErrs) > 0 {
				joined := errors.Join(providerErrs...)
				e.recordFailure(ctx, item, classifyProviderError(joined), joined.Error())
				return fmt.Errorf("author mismatch observed after %d provider error(s): %w",
					len(providerErrs), joined)
			}
			return e.completeWithoutMetadata(ctx, item, EnrichmentOutcomeNoMatch)
		}
		identity, identityErr := metadata.AdmitProviderIDs(ctx, metadata.ProviderIDAdmissionRequest{
			CandidateProviderIDs: result.ProviderIDs,
			ExistingProviderIDs:  accumulator.ProviderIDs,
			Owner:                e.providerIDs,
			ItemType:             "audiobook",
			ContentID:            item.ContentID,
		})
		if identityErr != nil {
			providerErrs = append(providerErrs, fmt.Errorf("%s metadata identity admission: %w", p.Slug(), identityErr))
			continue
		}
		if identity.ContradictsExisting {
			slog.WarnContext(ctx, "audiobook enrichment: metadata identity contradicts an existing ID; skipping", "component", "audiobooks",
				"provider", p.Slug(),
				"content_id", item.ContentID,
			)
			continue
		}
		for _, conflict := range identity.Conflicts {
			slog.InfoContext(ctx, "audiobook enrichment: metadata provider id already owned by another item; skipping", "component", "audiobooks",
				"provider", conflict.Provider,
				"provider_id", conflict.ProviderID,
				"content_id", item.ContentID,
				"owned_by", conflict.OwnedBy,
			)
		}
		admittedResult := *result
		admittedResult.ProviderIDs = identity.ProviderIDs
		result = &admittedResult
		// Bootstrap subsequent providers with any newly discovered IDs.
		mergeEnrichmentProviderIDs(accumulator, result)
		accumulatedIDs = accumulator.ProviderIDs
		metadata.MergeMetadata(result, accumulator, nil, metadata.MergeFillEmpty)

		slog.DebugContext(ctx, "audiobook enrichment: metadata received", "component", "audiobooks",
			"provider", p.Slug(),
			"content_id", item.ContentID,
			"has_poster", result.PosterPath != "",
			"has_overview", result.Overview != "",
		)
	}

	// Nothing found.
	if !accumulator.HasMetadata && accumulator.PosterPath == "" && accumulator.Overview == "" {
		if err := ctx.Err(); err != nil {
			// A cancelled sweep says nothing about the item or the providers.
			return err
		}
		if len(providerErrs) > 0 {
			// Transient provider trouble must not stamp the item terminally;
			// surfacing an error lets the sweep retry it later instead. Record
			// the class too, so a rate-limited afternoon is afterwards
			// distinguishable from a genuine no-match -- the distinction the
			// ebook backlog lost.
			joined := errors.Join(providerErrs...)
			e.recordFailure(ctx, item, classifyProviderError(joined), joined.Error())
			return fmt.Errorf("no metadata obtained, %d provider error(s): %w",
				len(providerErrs), joined)
		}
		// Providers ran cleanly but nothing matched — stamp last_refreshed so we
		// skip on the next sweep.
		slog.InfoContext(ctx, "audiobook enrichment: no metadata found", "component", "audiobooks",
			"content_id", item.ContentID,
			"title", item.Title,
		)
		return e.completeWithoutMetadata(ctx, item, EnrichmentOutcomeNoMatch)
	}

	// Phase 3: Persist.
	if err := e.persist(ctx, item, accumulatedIDs, accumulator); err != nil {
		persistErr := fmt.Errorf("persisting enrichment for %s: %w", item.ContentID, err)
		e.recordFailure(ctx, item, classifyProviderError(persistErr), persistErr.Error())
		return persistErr
	}
	e.enqueueRemoteArtwork(ctx, item.ContentID, accumulator)
	e.autoLinkLiteraryWork(ctx, item.ContentID)

	slog.InfoContext(ctx, "audiobook enrichment: enriched", "component", "audiobooks",
		"content_id", item.ContentID,
		"title", item.Title,
		"poster", accumulator.PosterPath != "",
		"overview", accumulator.Overview != "",
		"people", len(accumulator.People),
	)

	return nil
}

func (e *Enricher) autoLinkLiteraryWork(ctx context.Context, contentID string) {
	if e == nil || e.workLinker == nil || strings.TrimSpace(contentID) == "" {
		return
	}
	workID, linked, err := e.workLinker.AutoLinkContent(ctx, contentID)
	if err != nil {
		slog.WarnContext(ctx, "audiobook enrichment: literary work auto-link failed", "component", "audiobooks", "content_id", contentID, "error", err)
		return
	}
	if linked {
		slog.InfoContext(ctx, "audiobook enrichment: literary work auto-linked", "component", "audiobooks", "content_id", contentID, "work_id", workID)
	}
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
		slog.WarnContext(ctx, "audiobook enrichment: local cover fallback failed", "component", "audiobooks",
			"content_id", contentID,
			"error", err,
		)
	}
}

// cacheRemotePoster stores provider-hosted audiobook posters in the same S3
// image cache used by movie/TV metadata. Failures are non-fatal: keeping the
// provider URL is better than dropping the poster.
func (e *Enricher) cacheRemotePoster(ctx context.Context, contentID string, result *metadata.MetadataResult) {
	if e == nil || result == nil || result.PosterPath == "" {
		return
	}
	if !strings.HasPrefix(result.PosterPath, "http://") && !strings.HasPrefix(result.PosterPath, "https://") {
		return
	}

	imageCacher, ok := e.imageCacher.(metadata.ImageCacher)
	if !ok || imageCacher == nil {
		return
	}

	cached, err := imageCacher.CacheImage(ctx, metadata.CacheImageRequest{
		SourceURL:   result.PosterPath,
		ProviderID:  audiobookMetadataImageProviderID,
		ContentType: "audiobooks",
		ContentID:   contentID,
		ImageType:   metadata.ImagePoster,
	})
	if err != nil {
		slog.WarnContext(ctx, "audiobook enrichment: poster cache failed, keeping provider URL", "component", "audiobooks",
			"content_id", contentID,
			"url", result.PosterPath,
			"error", err,
		)
		return
	}

	if storedPath := metadata.CachedImageOriginalPath(cached); storedPath != "" {
		result.PosterPath = storedPath
	}
	if cached.Thumbhash != "" {
		result.PosterThumbhash = cached.Thumbhash
	}
}

// persist writes the enriched metadata back to the database.
func (e *Enricher) persist(ctx context.Context, item enrichmentItemRow, providerIDs map[string]string, result *metadata.MetadataResult) error {
	contentID := item.ContentID
	// Build the MetadataUpdate — only set fields that the provider returned.
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

	if e.pool != nil && e.itemRepo != nil {
		if err := e.persistMetadataAndProviderIDsTx(ctx, item, providerIDs, upd); err != nil {
			return err
		}
	} else {
		// Partially wired tests retain the legacy calls. Production always takes
		// the transaction path above so an identity can never commit without
		// its metadata and terminal timestamp.
		if e.providerIDs != nil && len(providerIDs) > 0 {
			if err := e.providerIDs.ReplaceByContentID(ctx, contentID, providerIDs); err != nil {
				return fmt.Errorf("persisting audiobook provider IDs: %w", err)
			}
		}
		if err := e.updateMetadataAndTimestamps(ctx, contentID, upd); err != nil {
			return err
		}
	}

	// Write people (authors / narrators).
	if len(result.People) > 0 && e.personRepo != nil && e.itemRepo != nil {
		if err := e.persistPeople(ctx, contentID, result.People); err != nil {
			// Non-fatal: log and continue so at least scalar metadata is saved.
			slog.WarnContext(ctx, "audiobook enrichment: failed to persist people", "component", "audiobooks",
				"content_id", contentID,
				"error", err,
			)
		}
	}

	return nil
}

func (e *Enricher) persistMetadataAndProviderIDsTx(
	ctx context.Context,
	item enrichmentItemRow,
	providerIDs map[string]string,
	upd *catalog.MetadataUpdate,
) error {
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning audiobook enrichment transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := e.state.AssertClaimTx(ctx, tx, item.ContentID, item.ClaimToken); err != nil {
		return err
	}
	if e.providerIDs != nil && len(providerIDs) > 0 {
		if err := e.providerIDs.ReplaceByContentIDTx(ctx, tx, item.ContentID, "audiobook", providerIDs); err != nil {
			return fmt.Errorf("persisting audiobook provider IDs: %w", err)
		}
	}
	if err := e.itemRepo.UpdateMetadataTx(ctx, tx, item.ContentID, upd); err != nil {
		return fmt.Errorf("updating audiobook metadata: %w", err)
	}
	now := time.Now().UTC()
	if _, err := tx.Exec(ctx, `
		UPDATE media_items
		SET last_refreshed = $1,
		    matched_at     = COALESCE(matched_at, $1),
		    status         = CASE WHEN status = 'pending' THEN 'matched' ELSE status END
		WHERE content_id = $2
	`, now, item.ContentID); err != nil {
		return fmt.Errorf("stamping audiobook enrichment transaction: %w", err)
	}
	if err := e.state.RecordOutcomeTx(ctx, tx, item.ContentID, item.ClaimToken, EnrichmentOutcomeSuccess); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing audiobook enrichment transaction: %w", err)
	}
	return nil
}

// completeWithoutMetadata atomically records a clean terminal outcome and the
// media-item timestamp. This keeps a stale worker whose lease expired from
// burning an item after another replica has reclaimed it.
func (e *Enricher) completeWithoutMetadata(ctx context.Context, item enrichmentItemRow, outcome EnrichmentOutcome) error {
	if e.pool == nil || e.state == nil || item.ClaimToken == "" {
		e.recordOutcome(ctx, item, outcome)
		return e.stampLastRefreshed(ctx, item.ContentID)
	}
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning audiobook terminal outcome transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := e.state.AssertClaimTx(ctx, tx, item.ContentID, item.ClaimToken); err != nil {
		return err
	}
	now := time.Now().UTC()
	if _, err := tx.Exec(ctx, `
		UPDATE media_items
		SET last_refreshed = $1,
		    matched_at     = COALESCE(matched_at, $1),
		    status         = CASE WHEN status = 'pending' THEN 'matched' ELSE status END
		WHERE content_id = $2
	`, now, item.ContentID); err != nil {
		return fmt.Errorf("stamping audiobook terminal outcome: %w", err)
	}
	if err := e.state.RecordOutcomeTx(ctx, tx, item.ContentID, item.ClaimToken, outcome); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing audiobook terminal outcome: %w", err)
	}
	return nil
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
			ProviderID:        audiobookMetadataImageProviderID,
			ProviderContentID: contentID,
			ContentType:       "audiobooks",
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
		slog.WarnContext(ctx, "audiobook enrichment: failed to enqueue image cache jobs", "component", "audiobooks",
			"content_id", contentID,
			"count", len(inputs),
			"error", err,
		)
	}
}

func isRemoteHTTPImage(path string) bool {
	return strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://")
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

// recordOutcome retains the non-transactional path for partially wired tests
// and administrative repair calls. Production terminal paths use
// completeWithoutMetadata or persistMetadataAndProviderIDsTx instead.
func (e *Enricher) recordOutcome(ctx context.Context, item enrichmentItemRow, outcome EnrichmentOutcome) {
	if e == nil || e.state == nil {
		return
	}
	if err := e.state.RecordOutcome(ctx, item.ContentID, item.ClaimToken, outcome); err != nil {
		slog.WarnContext(ctx, "audiobook enrichment: could not record outcome", "component", "audiobooks",
			"content_id", item.ContentID,
			"outcome", string(outcome),
			"error", err,
		)
	}
}

// recordFailure stamps a classified failure and parks a retry. Swallowed for
// the same reason as recordOutcome.
func (e *Enricher) recordFailure(ctx context.Context, item enrichmentItemRow, class EnrichmentErrorClass, cause string) {
	if e == nil || e.state == nil {
		return
	}
	if err := e.state.RecordFailure(ctx, item.ContentID, item.ClaimToken, class, cause); err != nil {
		slog.WarnContext(ctx, "audiobook enrichment: could not record failure", "component", "audiobooks",
			"content_id", item.ContentID,
			"class", string(class),
			"error", err,
		)
	}
}

// classifyProviderError sorts a provider failure into the classes that decide
// how long the item is parked. Rate limiting is singled out because retrying
// into a closed window is what turns a throttle into a backlog -- and because
// a throttled answer recorded as a plain no-match is precisely how the ebook
// side ended up with 90,721 unreadable rows.
func classifyProviderError(err error) EnrichmentErrorClass {
	class, _ := metadata.ClassifyProviderError(err)
	switch class {
	case metadata.ProviderErrorRateLimited:
		return EnrichmentErrorRateLimited
	case metadata.ProviderErrorPermanent:
		return EnrichmentErrorPermanent
	default:
		return EnrichmentErrorTransient
	}
}
