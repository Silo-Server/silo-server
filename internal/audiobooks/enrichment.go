package audiobooks

// Enricher periodically enriches audiobook media_items that are missing
// metadata (poster_path, overview, etc.) by querying the configured
// metadata-provider chain for each item's library folder.
//
// Design: a periodic, state-backed sweep claims batches of up to batchSize audiobooks
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
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
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

	// defaultEnrichBatchSize is the maximum number of audiobook items claimed
	// per batch. A sweep runs batches back to back while books are due (see
	// Enricher.sweep), so this bounds each claim's lease, not the sweep.
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

	// enrichSweepLock lets one node sweep at a time ("SILOABEN"). Every API
	// node runs the task; without it, N nodes would claim N disjoint batches
	// and multiply the request rate against the same providers.
	enrichSweepLock int64 = 0x53494C4F4142454E
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
	ContentID  string
	ClaimToken string
	Title      string
	Year       int
	FolderID   int
	Language   string // resolved from media_folders.metadata_language
	Author     string // from item_people where kind = PersonKindAuthor (7), best-effort
	// Narrators are the item's narrator credits (kind 8) as tagged; one entry
	// may name several people.
	Narrators []string
	// SeriesPosition is the item's whole-number audiobook_series index, or 0.
	SeriesPosition int
	// SeriesName is the item's audiobook_series name, or empty.
	SeriesName  string
	ProviderIDs map[string]string
	// Chain holds the slugs of the providers resolved for this item, so a
	// failure takes its retry-after from those providers' pauses only.
	Chain []string
}

type audiobookProviderIDRepository interface {
	metadata.ProviderIDOwnerLookup
	GetByContentID(ctx context.Context, contentID string) ([]*models.MediaItemProviderID, error)
	GetByContentIDs(ctx context.Context, contentIDs []string) (map[string][]*models.MediaItemProviderID, error)
	ReplaceByContentID(ctx context.Context, contentID string, providerIDs map[string]string) error
	ReplaceByContentIDTx(ctx context.Context, tx pgx.Tx, contentID, itemType string, providerIDs map[string]string) error
}

type audiobookItemRepository interface {
	UpdateMetadata(ctx context.Context, contentID string, upd *catalog.MetadataUpdate) error
	UpdateMetadataTx(ctx context.Context, tx pgx.Tx, contentID string, upd *catalog.MetadataUpdate) error
	ReplacePeopleKindsTx(ctx context.Context, tx pgx.Tx, contentID string, kinds []models.PersonKind, people []models.ItemPerson) error
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
	gate             providerGate
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

// HasPendingItems reports whether a scheduled sweep has any audiobook rows to
// process. It mirrors claimBatch's eligibility predicate without loading rows
// or provider IDs, so the scheduler can skip no-op executions without adding
// task-history noise.
func (e *Enricher) HasPendingItems(ctx context.Context) (bool, error) {
	if e == nil || e.pool == nil || e.chainRepo == nil {
		return false, nil
	}

	// A sweep in progress on another node owns the work; this node's scheduled
	// run would only find the lock taken.
	if held, err := e.sweepLockHeld(ctx); err != nil || held {
		return false, err
	}

	var exists bool
	err := e.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM media_items mi
			WHERE mi.type = 'audiobook'
			  AND mi.match_ignored_at IS NULL
			  AND EXISTS (
			      SELECT 1
			      FROM media_item_libraries mil
			      WHERE mil.content_id = mi.content_id
			  )
			  AND NOT EXISTS (
			      SELECT 1
			      FROM media_item_provider_ids p
			      WHERE p.content_id = mi.content_id
			        AND p.provider <> 'asin'
			  )
			  AND (
			      mi.last_refreshed IS NULL
			      OR EXISTS (
			          SELECT 1
			          FROM audiobook_enrichment_state retry
			          WHERE retry.content_id = mi.content_id
			            AND retry.outcome IN ('no_match', 'skipped')
			            AND (retry.next_attempt_at IS NULL OR retry.next_attempt_at <= now())
			            AND (retry.outcome = 'skipped' OR retry.no_matches < $1)
			      )
			      OR EXISTS (
			          SELECT 1
			          FROM audiobook_enrichment_state pending
			          WHERE pending.content_id = mi.content_id
			            AND pending.outcome IS NULL
			      )
			  )
			  AND NOT EXISTS (
			      SELECT 1
			      FROM audiobook_enrichment_state s
			      WHERE s.content_id = mi.content_id
			        AND (
			            s.next_attempt_at > now()
			            OR s.lease_until > now()
			            OR s.outcome = 'failed'
			        )
			  )
			LIMIT 1
		)
	`, audiobookNoMatchRetryLimit).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("checking pending audiobook enrichment: %w", err)
	}
	return exists, nil
}

// runBatch processes items in parallel using e.workers goroutines. The
// enrichFn parameter exists for testing: production calls pass e.enrichItem.
func (e *Enricher) runBatch(ctx context.Context, items []enrichmentItemRow, enrichFn func(context.Context, enrichmentItemRow) error) {
	if len(items) == 0 {
		return
	}
	workers := e.workers
	if workers <= 0 {
		workers = 1
	}
	if workers > len(items) {
		workers = len(items)
	}

	ch := make(chan enrichmentItemRow, workers)
	var wg sync.WaitGroup
	// Claim cleanup must outlive the canceled batch context, but it cannot
	// hand every remaining item its own five-second timeout: the drain is
	// serial, so an unreachable database would stretch cancellation by five
	// seconds per queued item. One lazily-created deadline bounds the whole
	// cleanup phase.
	var (
		cleanupOnce   sync.Once
		cleanupCtx    context.Context
		cleanupCancel context.CancelFunc
	)
	defer func() {
		if cleanupCancel != nil {
			cleanupCancel()
		}
	}()
	release := func(item enrichmentItemRow) {
		cleanupOnce.Do(func() {
			cleanupCtx, cleanupCancel = context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second) //nolint:fatcontext,gosec // set once under cleanupOnce from the batch ctx; the deferred func above calls cleanupCancel
		})
		e.releaseClaim(cleanupCtx, item)
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range ch {
				if ctx.Err() != nil {
					release(item)
					continue // drain and return queued claims to the pool
				}
				if err := enrichFn(ctx, item); err != nil {
					if ctx.Err() != nil {
						release(item)
					}
					slog.WarnContext(ctx, "audiobook enrichment: item failed", "component", "audiobooks",
						"content_id", item.ContentID,
						"title", item.Title,
						"error", redactProviderText(err.Error()),
					)
				}
			}
		}()
	}
	for _, item := range items {
		select {
		case ch <- item:
		case <-ctx.Done():
			// Items not sent to a worker still hold a database lease from
			// claimBatch. Return them immediately instead of waiting for the
			// two-hour recovery lease to expire.
			release(item)
		}
	}
	close(ch)
	wg.Wait()
}

// releaseClaim returns an unprocessed claim to the pool. ctx is the batch's
// shared cleanup context, not the canceled request context.
func (e *Enricher) releaseClaim(ctx context.Context, item enrichmentItemRow) {
	if e == nil || e.state == nil || item.ClaimToken == "" {
		return
	}
	if err := e.state.ReleaseClaim(ctx, item.ContentID, item.ClaimToken); err != nil {
		slog.WarnContext(ctx, "audiobook enrichment: could not release canceled claim", "component", "audiobooks", "content_id", item.ContentID, "error", err)
	}
}

// claimBatch returns up to batchSize audiobook items that need enrichment.
// "Needs enrichment" means the item has no provider identity at all AND
// last_refreshed IS NULL, with a bounded retry exception for clean no-match
// results and a slower retry loop for skipped items (for example, a library
// that had no provider chain configured yet). Provider errors park a due
// retry in the state row and stay eligible even after an earlier outcome
// stamped last_refreshed, so a transient failure mid-retry cannot silently
// end the retry sequence.
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
			  AND mi.match_ignored_at IS NULL
			  AND EXISTS (
			      SELECT 1
			      FROM media_item_libraries mil
			      WHERE mil.content_id = mi.content_id
			  )
			  AND NOT EXISTS (
			      SELECT 1
			      FROM media_item_provider_ids p
			      WHERE p.content_id = mi.content_id
			        AND p.provider <> 'asin'
			  )
			  AND (
			      mi.last_refreshed IS NULL
			      OR EXISTS (
			          SELECT 1
			          FROM audiobook_enrichment_state retry
			          WHERE retry.content_id = mi.content_id
			            AND retry.outcome IN ('no_match', 'skipped')
			            AND (retry.next_attempt_at IS NULL OR retry.next_attempt_at <= now())
			            AND (retry.outcome = 'skipped' OR retry.no_matches < $3)
			      )
			      OR EXISTS (
			          SELECT 1
			          FROM audiobook_enrichment_state pending
			          WHERE pending.content_id = mi.content_id
			            AND pending.outcome IS NULL
			      )
			  )
			  AND NOT EXISTS (
			      SELECT 1
			      FROM audiobook_enrichment_state s
			      WHERE s.content_id = mi.content_id
			        AND (
			            s.next_attempt_at > now()
			            OR s.lease_until > now()
			            OR s.outcome = 'failed'
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
		SELECT mi.content_id, c.claim_token, `+enrichmentItemColumns+`
		FROM claimed c
		JOIN media_items mi ON mi.content_id = c.content_id
		ORDER BY mi.created_at ASC, mi.content_id ASC
	`, e.batchSize, defaultEnrichClaimLease.Seconds(), audiobookNoMatchRetryLimit)
	if err != nil {
		return nil, fmt.Errorf("querying unenriched audiobooks: %w", err)
	}
	defer rows.Close()

	var items []enrichmentItemRow
	for rows.Next() {
		var item enrichmentItemRow
		if err := rows.Scan(append([]any{&item.ContentID, &item.ClaimToken}, item.columnTargets()...)...); err != nil {
			return nil, fmt.Errorf("scanning audiobook enrichment row: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating audiobook enrichment rows: %w", err)
	}

	// Load any durable provider IDs for the batch in one query.
	if e.providerIDs != nil && len(items) > 0 {
		ids := make([]string, len(items))
		for i := range items {
			ids[i] = items[i].ContentID
		}
		pids, err := e.providerIDs.GetByContentIDs(ctx, ids)
		if err != nil {
			// The claim stands; the items search without their stored IDs.
			slog.WarnContext(ctx, "audiobook enrichment: could not load provider IDs for the batch", "component", "audiobooks", "error", err)
		}
		for i := range items {
			items[i].ProviderIDs = providerIDMapFromRows(pids[items[i].ContentID])
		}
	}

	return items, nil
}

// enrichmentItemColumns are the media_items (alias mi) columns an
// enrichmentItemRow holds after its content ID and claim token, in the order
// columnTargets scans them.
const enrichmentItemColumns = `
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
			) AS author,
			ARRAY(
				SELECT p.name
				FROM item_people ip
				JOIN people p ON p.id = ip.person_id
				WHERE ip.content_id = mi.content_id
				  AND ip.kind = 8
				ORDER BY ip.sort_order, ip.id
			) AS narrators,
			COALESCE((
				SELECT s.series_index::int
				FROM audiobook_series s
				WHERE s.content_id = mi.content_id
				  AND s.series_index > 0
				  AND s.series_index < 100000
				  AND s.series_index = trunc(s.series_index)
			), 0) AS series_position,
			COALESCE((
				SELECT s.series_name FROM audiobook_series s WHERE s.content_id = mi.content_id
			), '') AS series_name
`

// columnTargets returns the scan targets for enrichmentItemColumns.
func (item *enrichmentItemRow) columnTargets() []any {
	return []any{&item.Title, &item.Year, &item.FolderID, &item.Language, &item.Author,
		&item.Narrators, &item.SeriesPosition, &item.SeriesName}
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

	providers, err := e.resolveChain(ctx, item.FolderID)
	if err != nil {
		resolveErr := fmt.Errorf("resolving audiobook chain for folder %d: %w", item.FolderID, err)
		// Classify the cause, not the wrapper: folder 403 must not read as
		// an HTTP status.
		e.recordFailure(ctx, item, classifyProviderError(err), resolveErr.Error(), time.Time{})
		return resolveErr
	}
	if len(providers) == 0 {
		slog.DebugContext(ctx, "audiobook enrichment: no providers in chain", "component", "audiobooks",
			"content_id", item.ContentID,
			"folder_id", item.FolderID,
		)
		return e.completeWithoutMetadata(ctx, item, EnrichmentOutcomeSkipped)
	}
	// Route calls through the rate-limit gate. When every provider in the
	// chain is paused, nothing would be asked: hand the item back for after the
	// pause without counting an attempt.
	providers = e.gate.wrap(providers)
	for _, p := range providers {
		item.Chain = append(item.Chain, p.Slug())
	}
	if until, paused := e.gate.allPaused(providers); paused {
		return e.deferItem(ctx, item, until)
	}

	match := e.findMatch(ctx, item, providers)
	accumulatedIDs, accumulator, providerErrs := match.ids, match.metadata, match.providerErrs

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
			return e.failOrDefer(ctx, item, providerErrs)
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
		if errors.Is(err, access.ErrMetadataCurationScope) {
			// A refused curator's refresh says nothing about the providers.
			return persistErr
		}
		e.recordFailure(ctx, item, classifyProviderError(persistErr), persistErr.Error(), time.Time{})
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

// resolveChain returns the audiobook provider chain of library folderID.
func (e *Enricher) resolveChain(ctx context.Context, folderID int) ([]metadata.Provider, error) {
	if e.resolveProviders != nil {
		return e.resolveProviders(ctx, folderID, audiobookItemType)
	}
	return metadata.ResolveChain(ctx, folderID, audiobookItemType, e.chainRepo, e.resolver)
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

// persist writes the enriched metadata back to the database. With a database
// wired, it first clears from result the fields an admin has locked and a
// cover the book already has, so the remote artwork queued from result
// afterwards skips them too. Credits commit with the rest of the match: a
// failure leaves the item unmatched and retryable, never half written.
func (e *Enricher) persist(ctx context.Context, item enrichmentItemRow, providerIDs map[string]string, result *metadata.MetadataResult) error {
	if e.pool == nil || e.itemRepo == nil {
		return e.persistMetadataWithoutTx(ctx, item, providerIDs, result)
	}
	people, err := e.resolvePeople(ctx, result.People)
	if err != nil {
		return err
	}
	return e.persistMetadataAndProviderIDsTx(ctx, item, providerIDs, result, people)
}

// clearLockedAudiobookFields drops the provider values for every field an
// admin has locked on the item, so enrichment leaves those fields as the admin
// set them. The tagline shares the overview lock. Audiobook credits, authors
// and narrators, are crew, so the crew lock keeps them.
func clearLockedAudiobookFields(result *metadata.MetadataResult, locked []int) {
	isLocked := func(field metadata.MetadataField) bool {
		return slices.Contains(locked, int(field))
	}
	if isLocked(metadata.FieldOverview) {
		result.Overview, result.Tagline = "", ""
	}
	if isLocked(metadata.FieldReleaseDates) {
		result.Year, result.ReleaseDate = 0, ""
	}
	if isLocked(metadata.FieldGenres) {
		result.Genres = nil
	}
	if isLocked(metadata.FieldStudios) {
		result.Studios = nil
	}
	if isLocked(metadata.FieldContentRating) {
		result.ContentRating = ""
	}
	if isLocked(metadata.FieldCrew) {
		result.People = nil
	}
	if isLocked(metadata.FieldImages) {
		result.PosterPath, result.PosterThumbhash = "", ""
		result.BackdropPath, result.BackdropThumbhash = "", ""
		result.LogoPath = ""
	}
}

// audiobookMetadataUpdate builds the update for the fields the provider
// returned.
func audiobookMetadataUpdate(result *metadata.MetadataResult) *catalog.MetadataUpdate {
	upd := &catalog.MetadataUpdate{}

	if result.PosterPath != "" {
		upd.PosterPath = &result.PosterPath
		if isRemoteHTTPImage(result.PosterPath) {
			upd.PosterSourcePath = &result.PosterPath
		}
		// Written even when empty, so a thumbhash never describes a cover
		// that is no longer stored. Image caching fills it in later.
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
	// result.Runtime is left out on purpose: it describes whichever edition
	// the provider matched. Active audio files determine the book's runtime.
	if result.Year > 0 {
		upd.Year = &result.Year
	}
	return upd
}

// persistMetadataWithoutTx serves partially wired tests. Production always
// takes persistMetadataAndProviderIDsTx so an identity can never commit
// without its metadata and terminal timestamp.
func (e *Enricher) persistMetadataWithoutTx(
	ctx context.Context,
	item enrichmentItemRow,
	providerIDs map[string]string,
	result *metadata.MetadataResult,
) error {
	if e.providerIDs != nil && len(providerIDs) > 0 {
		if err := e.providerIDs.ReplaceByContentID(ctx, item.ContentID, providerIDs); err != nil {
			return fmt.Errorf("persisting audiobook provider IDs: %w", err)
		}
	}
	return e.updateMetadataAndTimestamps(ctx, item.ContentID, audiobookMetadataUpdate(result))
}

func (e *Enricher) persistMetadataAndProviderIDsTx(
	ctx context.Context,
	item enrichmentItemRow,
	providerIDs map[string]string,
	result *metadata.MetadataResult,
	people []models.ItemPerson,
) error {
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning audiobook enrichment transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := e.state.AssertClaimTx(ctx, tx, item.ContentID, item.ClaimToken); err != nil {
		return err
	}
	// A delegated curator's refresh writes only while every library holding
	// the book is in the curator's scope. The sweep carries no authorizer.
	if err := catalog.GuardMetadataCurationItems(ctx, tx, item.ContentID); err != nil {
		return err
	}
	// Read the locks under the row lock, after the provider calls: an admin
	// who locks a field while the providers answer must still win.
	var locked []int
	var posterPath string
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(locked_fields, '{}'::integer[]), COALESCE(poster_path, '')
		FROM media_items
		WHERE content_id = $1
		FOR UPDATE
	`, item.ContentID).Scan(&locked, &posterPath); err != nil {
		return fmt.Errorf("reading audiobook field locks: %w", err)
	}
	clearLockedAudiobookFields(result, locked)
	if strings.TrimSpace(posterPath) != "" {
		// The book's own cover (a folder image or embedded art) stays; a
		// provider poster only fills a missing one.
		result.PosterPath, result.PosterThumbhash = "", ""
	}
	upd := audiobookMetadataUpdate(result)
	if e.providerIDs != nil && len(providerIDs) > 0 {
		if err := e.providerIDs.ReplaceByContentIDTx(ctx, tx, item.ContentID, "audiobook", providerIDs); err != nil {
			return fmt.Errorf("persisting audiobook provider IDs: %w", err)
		}
	}
	if err := catalog.ApplyWorkClaimsTx(ctx, tx, item.ContentID, audiobookItemType, result.WorkClaims); err != nil {
		return fmt.Errorf("persisting audiobook work ids: %w", err)
	}
	if err := e.itemRepo.UpdateMetadataTx(ctx, tx, item.ContentID, upd); err != nil {
		return fmt.Errorf("updating audiobook metadata: %w", err)
	}
	// The crew lock cleared result.People. Otherwise replace only the credit
	// kinds the provider supplied, so a narrator read from the file's tags
	// stays when the provider names none.
	if len(result.People) > 0 && len(people) > 0 {
		if err := e.itemRepo.ReplacePeopleKindsTx(ctx, tx, item.ContentID, catalog.PersonKindsOf(people), people); err != nil {
			return fmt.Errorf("persisting audiobook credits: %w", err)
		}
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
	if err := catalog.GuardMetadataCurationItems(ctx, tx, item.ContentID); err != nil {
		return err
	}
	now := time.Now().UTC()
	if _, err := tx.Exec(ctx, `
		UPDATE media_items
		SET last_refreshed = $1,
		    matched_at     = CASE WHEN $3 = 'success' THEN COALESCE(matched_at, $1) ELSE matched_at END,
		    status         = CASE WHEN $3 = 'success' AND status = 'pending' THEN 'matched' ELSE status END
		WHERE content_id = $2
	`, now, item.ContentID, string(outcome)); err != nil {
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

// resolvePeople finds or creates the provider's people and returns their
// credits with person IDs. A person row nobody is credited with is harmless,
// so this runs before the match transaction.
func (e *Enricher) resolvePeople(ctx context.Context, people []models.ItemPerson) ([]models.ItemPerson, error) {
	if len(people) == 0 || e.personRepo == nil {
		return nil, nil
	}
	persons := make([]models.Person, len(people))
	for i := range people {
		persons[i] = people[i].Person
	}

	personIDs, err := e.personRepo.BatchFindOrCreate(ctx, persons)
	if err != nil {
		return nil, fmt.Errorf("BatchFindOrCreate people: %w", err)
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
	return linked, nil
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
		  AND NOT ($4 = ANY(COALESCE(locked_fields, '{}'::integer[])))
	`, poster, thumb, contentID, int(metadata.FieldImages)); err != nil {
		return fmt.Errorf("update poster_path: %w", err)
	}
	return nil
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

// failOrDefer records an item that got no metadata because providers failed.
// When every error came from a paused provider, no provider was asked, so the
// item is deferred to the end of the pause instead of charged a failure.
func (e *Enricher) failOrDefer(ctx context.Context, item enrichmentItemRow, providerErrs []error) error {
	if until, ok := pausedOnly(providerErrs); ok {
		return e.deferItem(ctx, item, until)
	}
	joined := errors.Join(providerErrs...)
	e.recordFailure(ctx, item, classifyProviderError(joined), joined.Error(), latestPauseMet(providerErrs))
	return fmt.Errorf("no metadata obtained, %d provider error(s): %w", len(providerErrs), joined)
}

// pausedOnly reports whether every error came from a paused provider, and the
// earliest pause end among them.
func pausedOnly(errs []error) (time.Time, bool) {
	var until time.Time
	for _, err := range errs {
		var paused *providerPausedError
		if !errors.As(err, &paused) {
			return time.Time{}, false
		}
		if until.IsZero() || paused.until.Before(until) {
			until = paused.until
		}
	}
	return until, len(errs) > 0
}

// latestPauseMet returns the latest pause end among the paused providers the
// item met, or zero when it met none.
func latestPauseMet(errs []error) time.Time {
	var latest time.Time
	for _, err := range errs {
		var paused *providerPausedError
		if errors.As(err, &paused) && paused.until.After(latest) {
			latest = paused.until
		}
	}
	return latest
}

// deferItem returns a claimed item untouched until the providers' pause ends.
func (e *Enricher) deferItem(ctx context.Context, item enrichmentItemRow, until time.Time) error {
	slog.DebugContext(ctx, "audiobook enrichment: providers paused; deferring item", "component", "audiobooks",
		"content_id", item.ContentID,
		"until", until,
	)
	if e.state == nil {
		return nil
	}
	return e.state.Defer(ctx, item.ContentID, item.ClaimToken, until)
}

// recordFailure stamps a classified failure and parks a retry. Swallowed for
// the same reason as recordOutcome. A rate-limited item stays out at least
// until the gate lets its own providers be called again, which carries a
// provider's retry-after over to the item: the end of the chain's rate-limit
// pauses and of pausedUntil, the latest pause the item itself met. A route
// paused as down that the item never needed does not hold it back.
func (e *Enricher) recordFailure(ctx context.Context, item enrichmentItemRow, class EnrichmentErrorClass, cause string, pausedUntil time.Time) {
	if e == nil || e.state == nil {
		return
	}
	var retryAfter time.Duration
	if class == EnrichmentErrorRateLimited {
		until := e.gate.latestRateLimit(item.Chain)
		if pausedUntil.After(until) {
			until = pausedUntil
		}
		if !until.IsZero() {
			retryAfter = time.Until(until)
		}
	}
	if err := e.state.RecordFailure(ctx, item.ContentID, item.ClaimToken, class, retryAfter, cause); err != nil {
		slog.WarnContext(ctx, "audiobook enrichment: could not record failure", "component", "audiobooks",
			"content_id", item.ContentID,
			"class", string(class),
			"error", err,
		)
	}
}

// audiobookProviderErrors reads an untyped HTTP 403 as rate limiting. The
// audiobook metadata plugin forwards source errors such as "itunes: HTTP 403"
// from GetMetadata without a gRPC code, and Apple's API answers throttling
// with 403. Under the default policy that text parked the item for 30 days.
var audiobookProviderErrors = metadata.ProviderErrorPolicy{ForbiddenIsRateLimit: true}

// classifyProviderError sorts a provider failure into the classes that decide
// how long the item is parked. Rate limiting is singled out because retrying
// into a closed window is what turns a throttle into a backlog -- and because
// a throttled answer recorded as a plain no-match is precisely how the ebook
// side ended up with 90,721 unreadable rows.
func classifyProviderError(err error) EnrichmentErrorClass {
	class, _ := audiobookProviderErrors.Classify(err)
	switch class {
	case metadata.ProviderErrorRateLimited:
		return EnrichmentErrorRateLimited
	case metadata.ProviderErrorPermanent:
		return EnrichmentErrorPermanent
	default:
		return EnrichmentErrorTransient
	}
}
