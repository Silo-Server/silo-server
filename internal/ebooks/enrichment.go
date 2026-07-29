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
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	ebookMetadataImageProviderID = "ebook-metadata"

	defaultEnrichBatchSize = 5000
	defaultEnrichWorkers   = 4
	maxEnrichWorkers       = 5000

	defaultEnrichmentItemTimeout = 2 * time.Minute
)

// errEnrichmentSkipped preserves the direct helper contract for an item that
// cannot be attempted. Queue-backed runs record a short skipped horizon instead
// of counting the missing prerequisite as a provider failure.
var errEnrichmentSkipped = errors.New("ebook enrichment skipped")

type enrichmentClaimCheck func(context.Context) error

type enrichmentClaimCheckContextKey struct{}

func withEnrichmentClaimCheck(ctx context.Context, check enrichmentClaimCheck) context.Context {
	return context.WithValue(ctx, enrichmentClaimCheckContextKey{}, check)
}

func requireEnrichmentClaim(ctx context.Context) error {
	check, _ := ctx.Value(enrichmentClaimCheckContextKey{}).(enrichmentClaimCheck)
	if check == nil {
		return nil
	}
	return check(ctx)
}

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
	if n > maxEnrichWorkers {
		n = maxEnrichWorkers
	}
	return n
}

type enrichmentItemRow struct {
	ContentID       string
	Title           string
	Year            int
	FolderID        int
	Language        string
	Author          string
	ProviderIDs     map[string]string
	Status          string
	Overview        string
	Tagline         string
	ContentRating   string
	Runtime         int
	ReleaseDate     string
	Genres          []string
	Studios         []string
	PosterPath      string
	BackdropPath    string
	LogoPath        string
	LockedFields    []int
	ProtectedFields []string
}

type enrichmentQueue interface {
	ClaimBatch(ctx context.Context, scope EnrichmentScope, limit int, leaseDuration time.Duration) ([]EnrichmentJob, error)
	ReadyCount(ctx context.Context, scope EnrichmentScope) (int, error)
	HasReady(ctx context.Context, scope EnrichmentScope) (bool, error)
	CheckClaim(ctx context.Context, job EnrichmentJob) error
	Complete(ctx context.Context, job EnrichmentJob, outcome EnrichmentOutcome, refreshAfter time.Duration) error
	Fail(ctx context.Context, job EnrichmentJob, errorClass EnrichmentErrorClass, message string, retryAfter time.Duration) error
	Release(ctx context.Context, job EnrichmentJob) error
	Discard(ctx context.Context, job EnrichmentJob) error
}

type EnrichmentRunResult struct {
	Claimed   int  `json:"claimed"`
	Enriched  int  `json:"enriched"`
	NoMatch   int  `json:"no_match"`
	Failed    int  `json:"failed"`
	Deferred  int  `json:"deferred"`
	Discarded int  `json:"discarded"`
	Remaining int  `json:"remaining"`
	HasMore   bool `json:"-"`
}

type ebookProviderIDRepository interface {
	GetByContentIDs(ctx context.Context, contentIDs []string) (map[string][]*models.MediaItemProviderID, error)
	ReplaceByContentID(ctx context.Context, contentID string, providerIDs map[string]string) error
	FindContentIDByProviderIDs(
		ctx context.Context,
		providerIDs map[string]string,
		itemType string,
		excludeContentID string,
	) (string, error)
}

// Enricher drives the ebook metadata enrichment sweep.
type Enricher struct {
	pool           *pgxpool.Pool
	chainRepo      *metadata.ChainRepository
	resolver       *metadata.PluginResolverAdapter
	itemRepo       *catalog.ItemRepository
	personRepo     *catalog.PersonRepository
	providerIDs    ebookProviderIDRepository
	imageCacher    metadata.ImageCacher
	imageCacheJobs metadata.ImageCacheJobEnqueuer
	workLinker     literaryWorkLinker
	batchSize      int
	workers        int
	itemTimeout    time.Duration
	queue          enrichmentQueue

	loadClaimedItemsFn  func(context.Context, []EnrichmentJob) ([]enrichmentItemRow, error)
	enrichClaimedItemFn func(context.Context, enrichmentItemRow) (EnrichmentOutcome, error)
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
	var providerIDStore ebookProviderIDRepository
	if providerIDs != nil {
		providerIDStore = providerIDs
	}
	return &Enricher{
		pool:        pool,
		chainRepo:   chainRepo,
		resolver:    resolver,
		itemRepo:    itemRepo,
		personRepo:  personRepo,
		providerIDs: providerIDStore,
		batchSize:   defaultEnrichBatchSize,
		workers:     ebookEnrichWorkers(),
		queue:       NewEnrichmentQueue(pool),
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

func (e *Enricher) Run(ctx context.Context, scope EnrichmentScope) (EnrichmentRunResult, error) {
	return e.RunLimited(ctx, scope, 0)
}

func (e *Enricher) ReadyCount(ctx context.Context, scope EnrichmentScope) (int, error) {
	if e == nil {
		return 0, nil
	}
	if err := scope.validate(); err != nil {
		return 0, err
	}
	queue := e.queue
	if queue == nil && e.pool != nil {
		queue = NewEnrichmentQueue(e.pool)
	}
	if queue == nil {
		return 0, nil
	}
	return queue.ReadyCount(ctx, scope)
}

func (e *Enricher) RunLimited(ctx context.Context, scope EnrichmentScope, maxClaims int) (EnrichmentRunResult, error) {
	if e == nil {
		return EnrichmentRunResult{}, nil
	}
	if err := scope.validate(); err != nil {
		return EnrichmentRunResult{}, err
	}

	queue := e.queue
	if queue == nil && e.pool != nil {
		queue = NewEnrichmentQueue(e.pool)
	}
	if queue == nil || (e.chainRepo == nil && e.enrichClaimedItemFn == nil) {
		return EnrichmentRunResult{}, nil
	}
	jobs, err := queue.ClaimBatch(ctx, scope, e.claimLimit(maxClaims), defaultEnrichmentLease)
	if err != nil {
		return EnrichmentRunResult{}, fmt.Errorf("ebook enrichment: claim batch: %w", err)
	}
	if len(jobs) == 0 {
		hasMore, readyErr := queue.HasReady(ctx, scope)
		if readyErr != nil {
			return EnrichmentRunResult{}, fmt.Errorf("ebook enrichment: check remaining: %w", readyErr)
		}
		return EnrichmentRunResult{HasMore: hasMore}, nil
	}

	loadItems := e.loadClaimedItems
	if e.loadClaimedItemsFn != nil {
		loadItems = e.loadClaimedItemsFn
	}
	items, err := loadItems(ctx, jobs)
	if err != nil {
		e.releaseJobs(queue, jobs)
		return EnrichmentRunResult{Claimed: len(jobs), Deferred: len(jobs)},
			fmt.Errorf("ebook enrichment: load claimed items: %w", err)
	}

	slog.InfoContext(ctx, "ebook enrichment: sweep started", "component", "ebooks",
		"count", len(items),
		"workers", e.workers,
	)

	enrichItem := e.enrichClaimedItem
	if e.enrichClaimedItemFn != nil {
		enrichItem = e.enrichClaimedItemFn
	}
	result, runErr := e.runQueueBatch(ctx, queue, jobs, items, enrichItem)
	hasMore, readyErr := queue.HasReady(ctx, scope)
	result.HasMore = hasMore
	if readyErr != nil {
		runErr = errors.Join(runErr, fmt.Errorf("ebook enrichment: check remaining: %w", readyErr))
	}

	slog.InfoContext(ctx, "ebook enrichment: sweep complete", "component", "ebooks",
		"attempted", len(items),
		"enriched", result.Enriched,
		"no_match", result.NoMatch,
		"failed", result.Failed,
		"deferred", result.Deferred,
		"discarded", result.Discarded,
		"has_more", result.HasMore,
	)
	return result, runErr
}

func (e *Enricher) claimLimit(maxClaims int) int {
	batchSize := e.batchSize
	if batchSize <= 0 {
		batchSize = defaultEnrichBatchSize
	}
	workers := e.workers
	if workers <= 0 {
		workers = 1
	}
	limit := min(batchSize, workers)
	if maxClaims > 0 {
		limit = min(limit, maxClaims)
	}
	return limit
}

func (e *Enricher) runQueueBatch(
	ctx context.Context,
	queue enrichmentQueue,
	jobs []EnrichmentJob,
	items []enrichmentItemRow,
	enrichFn func(context.Context, enrichmentItemRow) (EnrichmentOutcome, error),
) (EnrichmentRunResult, error) {
	result := EnrichmentRunResult{Claimed: len(jobs)}
	claimedJobs := make(map[string]EnrichmentJob, len(jobs))
	for _, job := range jobs {
		claimedJobs[job.ContentID] = job
	}
	var transitionErrs []error
	loaded := make(map[string]struct{}, len(items))
	for i := range items {
		loaded[items[i].ContentID] = struct{}{}
	}
	for _, job := range jobs {
		if _, ok := loaded[job.ContentID]; !ok {
			if err := e.discardJob(queue, job); err != nil && !errors.Is(err, ErrEnrichmentLeaseLost) {
				transitionErrs = append(transitionErrs, fmt.Errorf("%s: %w", job.ContentID, err))
			} else if err == nil {
				result.Discarded++
			}
		}
	}

	workers := e.workers
	if workers <= 0 {
		workers = 1
	}
	if workers > len(items) {
		workers = len(items)
	}
	if workers == 0 {
		return result, errors.Join(transitionErrs...)
	}
	itemTimeout := e.itemTimeout
	if itemTimeout <= 0 || itemTimeout >= defaultEnrichmentLease {
		itemTimeout = defaultEnrichmentItemTimeout
	}

	ch := make(chan enrichmentItemRow, workers)
	var (
		wg           sync.WaitGroup
		enriched     int64
		noMatch      int64
		failed       int64
		deferred     int64
		transitionMu sync.Mutex
	)
	recordTransitionError := func(contentID string, err error) {
		if err == nil || errors.Is(err, ErrEnrichmentLeaseLost) {
			return
		}
		transitionMu.Lock()
		defer transitionMu.Unlock()
		transitionErrs = append(transitionErrs, fmt.Errorf("%s: %w", contentID, err))
	}

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range ch {
				job := claimedJobs[item.ContentID]
				if ctx.Err() != nil {
					recordTransitionError(item.ContentID, e.releaseJob(queue, job))
					continue
				}

				itemCtx, cancelItem := context.WithTimeout(ctx, itemTimeout)
				itemCtx = withEnrichmentClaimCheck(itemCtx, func(checkCtx context.Context) error {
					return queue.CheckClaim(checkCtx, job)
				})
				outcome, enrichErr := enrichFn(itemCtx, item)
				itemCtxErr := itemCtx.Err()
				cancelItem()
				if ctx.Err() != nil {
					recordTransitionError(item.ContentID, e.releaseJob(queue, job))
					continue
				}
				if errors.Is(itemCtxErr, context.DeadlineExceeded) ||
					errors.Is(enrichErr, context.DeadlineExceeded) {
					timeoutErr := itemCtxErr
					if timeoutErr == nil {
						timeoutErr = enrichErr
					}
					transitionErr := e.failJob(
						queue,
						ctx,
						job,
						EnrichmentErrorTransient,
						fmt.Sprintf("ebook enrichment item timeout: %v", timeoutErr),
						0,
					)
					recordTransitionError(item.ContentID, transitionErr)
					if transitionErr == nil {
						atomic.AddInt64(&failed, 1)
					}
					continue
				}
				if enrichErr != nil {
					errorClass, retryAfter := classifyEnrichmentError(enrichErr)
					transitionErr := e.failJob(
						queue,
						ctx,
						job,
						errorClass,
						enrichErr.Error(),
						retryAfter,
					)
					if transitionErr != nil && (ctx.Err() != nil ||
						errors.Is(transitionErr, context.Canceled) ||
						errors.Is(transitionErr, context.DeadlineExceeded)) {
						recordTransitionError(item.ContentID, e.releaseJob(queue, job))
					}
					recordTransitionError(item.ContentID, transitionErr)
					if transitionErr == nil {
						if errorClass == EnrichmentErrorRateLimited {
							atomic.AddInt64(&deferred, 1)
						} else {
							atomic.AddInt64(&failed, 1)
						}
					}
					continue
				}
				transitionErr := e.completeJob(queue, ctx, job, outcome)
				if transitionErr != nil && (ctx.Err() != nil ||
					errors.Is(transitionErr, context.Canceled) ||
					errors.Is(transitionErr, context.DeadlineExceeded)) {
					recordTransitionError(item.ContentID, e.releaseJob(queue, job))
				}
				if transitionErr != nil {
					recordTransitionError(item.ContentID, transitionErr)
					continue
				}
				if outcome == EnrichmentOutcomeSuccess {
					atomic.AddInt64(&enriched, 1)
				} else if outcome == EnrichmentOutcomeNoMatch {
					atomic.AddInt64(&noMatch, 1)
				} else {
					atomic.AddInt64(&deferred, 1)
				}
			}
		}()
	}
	for _, item := range items {
		ch <- item
	}
	close(ch)
	wg.Wait()

	if ctx.Err() != nil {
		transitionErrs = append([]error{ctx.Err()}, transitionErrs...)
	}
	result.Enriched += int(enriched)
	result.NoMatch += int(noMatch)
	result.Failed += int(failed)
	result.Deferred += int(deferred)
	return result, errors.Join(transitionErrs...)
}

func (e *Enricher) releaseJobs(queue enrichmentQueue, jobs []EnrichmentJob) {
	for _, job := range jobs {
		if err := e.releaseJob(queue, job); err != nil && !errors.Is(err, ErrEnrichmentLeaseLost) {
			slog.Warn("ebook enrichment: failed to release lease", "component", "ebooks",
				"content_id", job.ContentID,
				"error", err,
			)
		}
	}
}

func (e *Enricher) completeJob(
	queue enrichmentQueue,
	ctx context.Context,
	job EnrichmentJob,
	outcome EnrichmentOutcome,
) error {
	return queue.Complete(ctx, job, outcome, enrichmentRefreshHorizon(outcome))
}

func (e *Enricher) failJob(
	queue enrichmentQueue,
	ctx context.Context,
	job EnrichmentJob,
	errorClass EnrichmentErrorClass,
	message string,
	retryAfter time.Duration,
) error {
	return queue.Fail(ctx, job, errorClass, message, retryAfter)
}

func (e *Enricher) releaseJob(queue enrichmentQueue, job EnrichmentJob) error {
	releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return queue.Release(releaseCtx, job)
}

func (e *Enricher) discardJob(queue enrichmentQueue, job EnrichmentJob) error {
	discardCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return queue.Discard(discardCtx, job)
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
						slog.DebugContext(ctx, "ebook enrichment: item skipped", "component", "ebooks",
							"content_id", item.ContentID,
							"title", item.Title,
							"reason", err,
						)
						continue
					}
					slog.WarnContext(ctx, "ebook enrichment: item failed", "component", "ebooks",
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

var loadEnrichmentItemsQuery = `
	SELECT
		mi.content_id,
		mi.title,
		COALESCE(mi.year, 0),
		COALESCE(membership.media_folder_id, 0) AS folder_id,
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
		COALESCE(mi.status, ''),
		COALESCE(mi.overview, ''),
		COALESCE(mi.tagline, ''),
		COALESCE(mi.content_rating, ''),
		COALESCE(mi.runtime, 0),
		COALESCE(mi.release_date::text, ''),
		COALESCE(mi.genres, '{}'),
		COALESCE(mi.studios, '{}'),
		COALESCE(mi.poster_path, ''),
		COALESCE(mi.backdrop_path, ''),
		COALESCE(mi.logo_path, ''),
		COALESCE(mi.locked_fields, '{}'::integer[])
	FROM unnest($1::text[]) WITH ORDINALITY AS claimed(content_id, position)
	JOIN media_items mi ON mi.content_id = claimed.content_id
	LEFT JOIN LATERAL (
		SELECT mil.media_folder_id
		FROM media_item_libraries mil
		WHERE mil.content_id = mi.content_id
		ORDER BY mil.first_seen_at, mil.media_folder_id
		LIMIT 1
	) membership ON true
	LEFT JOIN media_folders mf ON mf.id = membership.media_folder_id
	WHERE mi.type = 'ebook'
	  AND ` + catalog.MangaChapterExclusionWhere("mi") + `
	ORDER BY claimed.position
`

func (e *Enricher) loadClaimedItems(ctx context.Context, jobs []EnrichmentJob) ([]enrichmentItemRow, error) {
	contentIDs := make([]string, 0, len(jobs))
	claimedJobs := make(map[string]EnrichmentJob, len(jobs))
	for _, job := range jobs {
		contentIDs = append(contentIDs, job.ContentID)
		claimedJobs[job.ContentID] = job
	}
	rows, err := e.pool.Query(ctx, loadEnrichmentItemsQuery, contentIDs)
	if err != nil {
		return nil, fmt.Errorf("querying claimed ebooks: %w", err)
	}
	defer rows.Close()

	var items []enrichmentItemRow
	for rows.Next() {
		var item enrichmentItemRow
		if err := rows.Scan(
			&item.ContentID,
			&item.Title,
			&item.Year,
			&item.FolderID,
			&item.Language,
			&item.Author,
			&item.Status,
			&item.Overview,
			&item.Tagline,
			&item.ContentRating,
			&item.Runtime,
			&item.ReleaseDate,
			&item.Genres,
			&item.Studios,
			&item.PosterPath,
			&item.BackdropPath,
			&item.LogoPath,
			&item.LockedFields,
		); err != nil {
			return nil, fmt.Errorf("scanning ebook enrichment row: %w", err)
		}
		item.ProtectedFields = append([]string(nil), claimedJobs[item.ContentID].ProtectedFields...)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating ebook enrichment rows: %w", err)
	}

	if err := e.loadProviderIDs(ctx, contentIDs, items); err != nil {
		return nil, err
	}

	return items, nil
}

func (e *Enricher) loadProviderIDs(
	ctx context.Context,
	contentIDs []string,
	items []enrichmentItemRow,
) error {
	if e == nil || e.providerIDs == nil || len(contentIDs) == 0 {
		return nil
	}
	rowsByContentID, err := e.providerIDs.GetByContentIDs(ctx, contentIDs)
	if err != nil {
		return fmt.Errorf("loading ebook provider IDs: %w", err)
	}
	for i := range items {
		items[i].ProviderIDs = providerIDMapFromRows(rowsByContentID[items[i].ContentID])
		_, hasISBN := items[i].ProviderIDs["isbn"]
		slog.DebugContext(ctx, "ebook enrichment: loaded provider identifiers",
			"component", "ebooks",
			"content_id", items[i].ContentID,
			"identifier_count", len(items[i].ProviderIDs),
			"has_isbn", hasISBN,
		)
	}
	return nil
}

func (e *Enricher) enrichItem(ctx context.Context, item enrichmentItemRow) error {
	outcome, err := e.enrichClaimedItem(ctx, item)
	if err == nil && outcome == EnrichmentOutcomeSkipped {
		return fmt.Errorf("%w: item %s is not ready", errEnrichmentSkipped, item.ContentID)
	}
	return err
}

func (e *Enricher) enrichClaimedItem(ctx context.Context, item enrichmentItemRow) (EnrichmentOutcome, error) {
	if item.FolderID == 0 {
		// The scanner inserts the library membership after the item upsert, so
		// a freshly indexed ebook can be claimed inside that window. Skip it:
		// stamping here would terminally mark the item refreshed before any
		// provider ever saw it.
		return EnrichmentOutcomeSkipped, nil
	}
	if ebookHasCompleteLocalMetadata(item) {
		slog.DebugContext(ctx, "ebook enrichment: local metadata is complete",
			"component", "ebooks",
			"content_id", item.ContentID,
		)
		return EnrichmentOutcomeSuccess, nil
	}
	slog.DebugContext(ctx, "ebook enrichment: remote metadata required",
		"component", "ebooks",
		"content_id", item.ContentID,
		"missing_fields", ebookMissingMetadataFields(item),
	)

	providers, err := metadata.ResolveChain(ctx, item.FolderID, ebookContentType(), e.chainRepo, e.resolver)
	if err != nil {
		return "", fmt.Errorf("resolving ebook chain for folder %d: %w", item.FolderID, err)
	}
	return e.enrichWithProvidersOutcome(ctx, item, providers)
}

func ebookHasCompleteLocalMetadata(item enrichmentItemRow) bool {
	return len(ebookMissingMetadataFields(item)) == 0
}

func ebookMissingMetadataFields(item enrichmentItemRow) []string {
	missing := make([]string, 0, 4)
	if strings.TrimSpace(item.Title) == "" {
		missing = append(missing, "title")
	}
	if strings.TrimSpace(item.Author) == "" {
		missing = append(missing, "author")
	}
	if strings.TrimSpace(item.Overview) == "" {
		missing = append(missing, "description")
	}
	if strings.TrimSpace(item.PosterPath) == "" {
		missing = append(missing, "cover")
	}
	return missing
}

// enrichWithProviders runs the provider chain for one claimed item. Outcomes:
//   - metadata obtained: persist it and stamp last_refreshed (nil error);
//   - providers answered but nothing matched: stamp last_refreshed so the
//     item is not re-claimed every sweep (nil error);
//   - one or more providers errored and no metadata was obtained: return an
//     error so durable queue backoff engages, without stamping;
//   - no providers configured: skip (no stamp, no failure) so the item is
//     retried once a chain exists.
func (e *Enricher) enrichWithProviders(ctx context.Context, item enrichmentItemRow, providers []metadata.Provider) error {
	outcome, err := e.enrichWithProvidersOutcome(ctx, item, providers)
	if err == nil && outcome == EnrichmentOutcomeSkipped {
		return fmt.Errorf("%w: no metadata providers configured for folder %d", errEnrichmentSkipped, item.FolderID)
	}
	return err
}

func (e *Enricher) enrichWithProvidersOutcome(
	ctx context.Context,
	item enrichmentItemRow,
	providers []metadata.Provider,
) (EnrichmentOutcome, error) {
	if len(providers) == 0 {
		return EnrichmentOutcomeSkipped, nil
	}

	var owner providerIDOwnerLookup
	if e.providerIDs != nil {
		owner = e.providerIDs
	}
	accumulator, accumulatedIDs, providerErrs := collectEbookMetadata(ctx, item, providers, owner)

	if !accumulator.HasMetadata && accumulator.PosterPath == "" && accumulator.Overview == "" {
		if err := ctx.Err(); err != nil {
			// A cancelled sweep says nothing about the item or the providers.
			return "", err
		}
		if len(providerErrs) > 0 {
			return "", fmt.Errorf("no metadata obtained, %d provider error(s): %w",
				len(providerErrs), errors.Join(providerErrs...))
		}
		slog.InfoContext(ctx, "ebook enrichment: no metadata found", "component", "ebooks",
			"content_id", item.ContentID,
			"title", item.Title,
		)
		if err := requireEnrichmentClaim(ctx); err != nil {
			return "", err
		}
		if err := e.stampLastRefreshed(ctx, item.ContentID); err != nil {
			return "", err
		}
		return EnrichmentOutcomeNoMatch, nil
	}

	preserveEbookLocalMetadata(item, accumulator)
	if err := e.persist(ctx, item.ContentID, accumulatedIDs, accumulator); err != nil {
		return "", fmt.Errorf("persisting enrichment for %s: %w", item.ContentID, err)
	}
	e.enqueueRemoteArtwork(ctx, item.ContentID, accumulator)
	e.autoLinkLiteraryWork(ctx, item.ContentID)

	slog.InfoContext(ctx, "ebook enrichment: enriched", "component", "ebooks",
		"content_id", item.ContentID,
		"title", item.Title,
		"poster", accumulator.PosterPath != "",
		"overview", accumulator.Overview != "",
		"people", len(filterEbookPeople(accumulator.People)),
	)

	return EnrichmentOutcomeSuccess, nil
}

func preserveEbookLocalMetadata(item enrichmentItemRow, result *metadata.MetadataResult) {
	if result == nil {
		return
	}
	protected := make(map[string]struct{}, len(item.ProtectedFields))
	for _, field := range item.ProtectedFields {
		protected[strings.ToLower(strings.TrimSpace(field))] = struct{}{}
	}
	isProtected := func(field string) bool {
		_, ok := protected[field]
		return ok
	}
	isLocked := func(field metadata.MetadataField) bool {
		for _, locked := range item.LockedFields {
			if locked == int(field) {
				return true
			}
		}
		return false
	}

	if isProtected("title") || isLocked(metadata.FieldName) {
		result.Title = ""
		result.OriginalTitle = ""
		result.SortTitle = ""
	}
	if isProtected("year") || isLocked(metadata.FieldReleaseDates) {
		result.Year = 0
	}
	if isProtected("overview") || isLocked(metadata.FieldOverview) {
		result.Overview = ""
	}
	if isProtected("tagline") {
		result.Tagline = ""
	}
	if isProtected("content_rating") || isLocked(metadata.FieldContentRating) {
		result.ContentRating = ""
	}
	if isProtected("runtime") || isLocked(metadata.FieldRuntime) {
		result.Runtime = 0
	}
	if isProtected("release_date") || isLocked(metadata.FieldReleaseDates) {
		result.ReleaseDate = ""
	}
	if isProtected("genres") || isLocked(metadata.FieldGenres) {
		result.Genres = nil
	}
	if isProtected("studios") || isLocked(metadata.FieldStudios) {
		result.Studios = nil
	}
	if isProtected("authors") || isLocked(metadata.FieldCrew) || isLocked(metadata.FieldCast) {
		result.People = nil
	}

	imagesLocked := isLocked(metadata.FieldImages)
	if imagesLocked || isProtected("poster_path") ||
		(item.PosterPath != "" && !ebookArtworkOwnedByRemoteProvider(item.PosterPath)) {
		result.PosterPath = ""
		result.PosterThumbhash = ""
	}
	if imagesLocked || isProtected("backdrop_path") ||
		(item.BackdropPath != "" && !ebookArtworkOwnedByRemoteProvider(item.BackdropPath)) {
		result.BackdropPath = ""
		result.BackdropThumbhash = ""
	}
	if imagesLocked || isProtected("logo_path") ||
		(item.LogoPath != "" && !ebookArtworkOwnedByRemoteProvider(item.LogoPath)) {
		result.LogoPath = ""
	}
}

func ebookArtworkOwnedByRemoteProvider(path string) bool {
	path = strings.ToLower(strings.TrimSpace(path))
	return isRemoteHTTPImage(path) || strings.HasPrefix(path, ebookMetadataImageProviderID+"/ebooks/")
}

func classifyEnrichmentError(err error) (EnrichmentErrorClass, time.Duration) {
	grpcStatus, ok := status.FromError(err)
	if !ok {
		return EnrichmentErrorTransient, 0
	}

	switch grpcStatus.Code() {
	case codes.ResourceExhausted:
		for _, detail := range grpcStatus.Details() {
			if retry, ok := detail.(*errdetails.RetryInfo); ok && retry.GetRetryDelay() != nil {
				return EnrichmentErrorRateLimited, retry.GetRetryDelay().AsDuration()
			}
		}
		return EnrichmentErrorRateLimited, 0
	case codes.InvalidArgument,
		codes.NotFound,
		codes.PermissionDenied,
		codes.Unauthenticated,
		codes.FailedPrecondition,
		codes.Unimplemented:
		return EnrichmentErrorPermanent, 0
	default:
		return EnrichmentErrorTransient, 0
	}
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
			slog.WarnContext(ctx, "ebook enrichment: search error", "component", "ebooks",
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
					slog.InfoContext(ctx, "ebook enrichment: provider id already owned by another item; skipping match", "component", "ebooks",
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
		slog.DebugContext(ctx, "ebook enrichment: search result", "component", "ebooks",
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
			slog.WarnContext(ctx, "ebook enrichment: GetMetadata error", "component", "ebooks",
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
		accumulator.HasMetadata = true
		mergeEnrichmentProviderIDs(accumulator, result)
		metadata.MergeMetadata(result, accumulator, nil, metadata.MergeFillEmpty)

		slog.DebugContext(ctx, "ebook enrichment: metadata received", "component", "ebooks",
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
		slog.WarnContext(ctx, "ebook enrichment: literary work auto-link failed", "component", "ebooks", "content_id", contentID, "error", err)
		return
	}
	if linked {
		slog.InfoContext(ctx, "ebook enrichment: literary work auto-linked", "component", "ebooks", "content_id", contentID, "work_id", workID)
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
		slog.WarnContext(ctx, "ebook enrichment: poster cache failed, keeping provider URL", "component", "ebooks",
			"content_id", contentID,
			"url", result.PosterPath,
			"error", err,
		)
		return
	}
	if cached == nil {
		slog.WarnContext(ctx, "ebook enrichment: poster cache returned no result, keeping provider URL", "component", "ebooks",
			"content_id", contentID,
			"url", result.PosterPath,
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
		slog.WarnContext(ctx, "ebook enrichment: failed to enqueue image cache jobs", "component", "ebooks",
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

func (e *Enricher) persist(ctx context.Context, contentID string, providerIDs map[string]string, result *metadata.MetadataResult) error {
	if err := requireEnrichmentClaim(ctx); err != nil {
		return err
	}
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
			slog.WarnContext(ctx, "ebook enrichment: failed to persist provider IDs", "component", "ebooks",
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
			slog.WarnContext(ctx, "ebook enrichment: failed to persist people", "component", "ebooks",
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
