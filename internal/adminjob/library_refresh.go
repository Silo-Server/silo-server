package adminjob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/database/pglock"
	"github.com/Silo-Server/silo-server/internal/libraryingest"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/notifications"
)

const (
	JobTypeLibraryRefresh = "library_refresh"

	defaultUnmatchedRefreshDelay = 2 * time.Second
	libraryRefreshWorkerCount    = 4
)

type LibraryRefreshMode string

const (
	LibraryRefreshModeQuick LibraryRefreshMode = "quick"
	LibraryRefreshModeFull  LibraryRefreshMode = "full"
)

type LibraryRefreshRequest struct {
	LibraryID   int                `json:"library_id"`
	LibraryName string             `json:"library_name"`
	Mode        LibraryRefreshMode `json:"mode,omitempty"`

	// waitForLibraryLock makes Execute wait for the per-library lock instead of
	// returning ErrLibraryRefreshInProgress. The runner sets it for a recovered
	// job, whose earlier attempt may still hold the lock.
	waitForLibraryLock bool
}

type LibraryRefreshResult struct {
	LibraryID       int                `json:"library_id"`
	LibraryName     string             `json:"library_name"`
	Mode            LibraryRefreshMode `json:"mode"`
	TotalItems      int                `json:"total_items"`
	ItemsWithIDs    int                `json:"items_with_ids"`
	ItemsWithoutIDs int                `json:"items_without_ids"`
	RefreshedOK     int                `json:"refreshed_ok"`
	RefreshedFailed int                `json:"refreshed_failed"`
	PipelineOK      int                `json:"pipeline_ok"`
	PipelineFailed  int                `json:"pipeline_failed"`
}

type LibraryRefreshItem struct {
	ContentID string
	TmdbID    string
	TvdbID    string
	ImdbID    string
}

type libraryRefreshItemLister interface {
	ListLibraryItems(ctx context.Context, libraryID int, mode LibraryRefreshMode) ([]LibraryRefreshItem, error)
}

type libraryRefreshFolderRepo interface {
	GetByID(ctx context.Context, id int) (*models.MediaFolder, error)
}

type libraryRefreshScopeResolver interface {
	ResolveForLibrary(ctx context.Context, contentID string, libraryID int) (*ItemRefreshRequest, error)
}

type libraryRefreshIngester interface {
	IngestSubtree(ctx context.Context, folder *models.MediaFolder, subtreePath string) (*libraryingest.Result, error)
}

type libraryRefreshRefresher interface {
	RefreshItem(ctx context.Context, contentID string) error
	RefreshItemForLibrary(ctx context.Context, contentID string, folderID int) error
}

type PGLibraryRefreshItemLister struct {
	pool *pgxpool.Pool
}

func NewPGLibraryRefreshItemLister(pool *pgxpool.Pool) *PGLibraryRefreshItemLister {
	return &PGLibraryRefreshItemLister{pool: pool}
}

func (l *PGLibraryRefreshItemLister) ListLibraryItems(ctx context.Context, libraryID int, mode LibraryRefreshMode) ([]LibraryRefreshItem, error) {
	query := `
		SELECT mi.content_id, COALESCE(mi.tmdb_id, ''), COALESCE(mi.tvdb_id, ''), COALESCE(mi.imdb_id, '')
		FROM media_item_libraries mil
		JOIN media_folders f ON f.id = mil.media_folder_id
		JOIN media_items mi ON mi.content_id = mil.content_id
		WHERE mil.media_folder_id = $1`
	args := []any{libraryID}

	if normalizeLibraryRefreshMode(mode) == LibraryRefreshModeQuick {
		query += `
		  AND (
			COALESCE(mi.tmdb_id, '') <> ''
			OR COALESCE(mi.tvdb_id, '') <> ''
			OR COALESCE(mi.imdb_id, '') <> ''
		  )
		  AND (
			mi.last_refreshed IS NULL
			OR COALESCE(mi.overview, '') = ''
			OR COALESCE(mi.poster_path, '') = ''
			OR COALESCE(mi.backdrop_path, '') = ''
			OR COALESCE(mi.logo_path, '') = ''
			OR COALESCE(mi.poster_path, '') LIKE '%//poster/%'
			OR COALESCE(mi.backdrop_path, '') LIKE '%//backdrop/%'
			OR COALESCE(mi.logo_path, '') LIKE '%//logo/%'
			OR LOWER(TRIM(COALESCE(mi.logo_source_path, ''))) LIKE 'tvdb://%'
			OR LOWER(TRIM(COALESCE(mi.logo_path, ''))) LIKE 'tvdb/%/logo/%'
			OR mi.refresh_failures > 0
			OR mi.episode_metadata_incomplete = TRUE
			OR (
				LOWER(TRIM(COALESCE(mi.status, ''))) = 'matched'
				AND COALESCE(mi.tmdb_id, '') = ''
				AND (
					COALESCE(mi.tvdb_id, '') <> ''
					OR COALESCE(mi.imdb_id, '') <> ''
				)
				AND NOT EXISTS (
					SELECT 1
					FROM stale_media_ids rejected_tmdb
					WHERE rejected_tmdb.content_id = mi.content_id
					  AND LOWER(TRIM(rejected_tmdb.provider)) = 'tmdb'
					  AND TRIM(rejected_tmdb.provider_id) <> ''
				)
			)
			OR EXISTS (
				SELECT 1
				FROM stale_media_ids smi
				WHERE smi.content_id = mi.content_id
				  AND LOWER(TRIM(smi.provider)) IN ('tmdb', 'tvdb', 'imdb')
				  AND TRIM(smi.provider_id) <> ''
				  AND (
					LOWER(TRIM(COALESCE(mi.status, ''))) <> 'matched'
					OR (
						LOWER(TRIM(smi.provider)) = 'tmdb'
						AND TRIM(smi.provider_id) = TRIM(COALESCE(mi.tmdb_id, ''))
					)
					OR (
						LOWER(TRIM(smi.provider)) = 'tvdb'
						AND TRIM(smi.provider_id) = TRIM(COALESCE(mi.tvdb_id, ''))
					)
					OR (
						LOWER(TRIM(smi.provider)) = 'imdb'
						AND LOWER(TRIM(smi.provider_id)) = LOWER(TRIM(COALESCE(mi.imdb_id, '')))
					)
					OR LOWER(TRIM(mi.content_id)) =
						'movie-' || LOWER(TRIM(smi.provider)) || '-' || LOWER(TRIM(smi.provider_id))
					OR LOWER(TRIM(mi.content_id)) =
						'series-' || LOWER(TRIM(smi.provider)) || '-' || LOWER(TRIM(smi.provider_id))
				  )
			)
			OR (
				COALESCE(mi.default_metadata_language, '') <> ''
				AND LOWER(TRIM(mi.default_metadata_language))
					<> LOWER(TRIM(COALESCE(NULLIF(f.metadata_language, ''), 'en')))
			)
		  )`
	}

	query += "\n\t\tORDER BY mi.content_id ASC"

	rows, err := l.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query library items: %w", err)
	}
	defer rows.Close()

	items := make([]LibraryRefreshItem, 0)
	for rows.Next() {
		var item LibraryRefreshItem
		if err := rows.Scan(&item.ContentID, &item.TmdbID, &item.TvdbID, &item.ImdbID); err != nil {
			return nil, fmt.Errorf("scan library refresh item: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate library refresh items: %w", err)
	}
	return items, nil
}

type LibraryRefreshExecutor struct {
	itemLister     libraryRefreshItemLister
	folderRepo     libraryRefreshFolderRepo
	resolver       libraryRefreshScopeResolver
	ingester       libraryRefreshIngester
	scanRuns       directScanRunRepository
	refresher      libraryRefreshRefresher
	eventBus       cache.EventBus
	realtimeHub    *notifications.Hub
	unmatchedDelay time.Duration
	wait           func(ctx context.Context, delay time.Duration) error
	lockPool       *pgxpool.Pool
	// lockRetryInterval spaces lock attempts while a recovered job waits;
	// zero means defaultLibraryLockRetryInterval.
	lockRetryInterval time.Duration
}

const defaultLibraryLockRetryInterval = 5 * time.Second

// ErrLibraryRefreshInProgress reports that another refresh of the same
// library holds its lock, on this server or another one.
var ErrLibraryRefreshInProgress = errors.New("another metadata refresh is already running for this library")

// libraryRefreshAdvisoryLockNamespace ("SILR") fills the high 32 bits of a
// library's lock key; the library ID fills the low 32 bits.
const libraryRefreshAdvisoryLockNamespace int64 = 0x53494C52

func libraryRefreshLockKey(libraryID int) int64 {
	return libraryRefreshAdvisoryLockNamespace<<32 | int64(uint32(libraryID))
}

// SetLibraryLockPool makes Execute hold a per-library advisory lock, so a
// library refresh job and the full refresh task never refresh the same library
// at the same time. Without a pool, Execute takes no lock.
func (e *LibraryRefreshExecutor) SetLibraryLockPool(pool *pgxpool.Pool) {
	e.lockPool = pool
}

func NewLibraryRefreshExecutor(
	itemLister libraryRefreshItemLister,
	folderRepo libraryRefreshFolderRepo,
	resolver libraryRefreshScopeResolver,
	ingester libraryRefreshIngester,
	scanRuns directScanRunRepository,
	refresher libraryRefreshRefresher,
	eventBus cache.EventBus,
	realtimeHub *notifications.Hub,
) *LibraryRefreshExecutor {
	return &LibraryRefreshExecutor{
		itemLister:     itemLister,
		folderRepo:     folderRepo,
		resolver:       resolver,
		ingester:       ingester,
		scanRuns:       scanRuns,
		refresher:      refresher,
		eventBus:       eventBus,
		realtimeHub:    realtimeHub,
		unmatchedDelay: defaultUnmatchedRefreshDelay,
		wait:           waitWithContext,
	}
}

func (e *LibraryRefreshExecutor) Execute(
	ctx context.Context,
	req LibraryRefreshRequest,
	progress func(current, total int, message string),
) (*LibraryRefreshResult, error) {
	if e == nil || e.itemLister == nil || e.folderRepo == nil || e.refresher == nil {
		return nil, fmt.Errorf("library refresh executor is not fully configured")
	}
	req.Mode = normalizeLibraryRefreshMode(req.Mode)
	if req.LibraryID <= 0 {
		return nil, fmt.Errorf("library_id is required")
	}
	if err := e.ensureLibraryEnabled(ctx, req.LibraryID); err != nil {
		return nil, err
	}
	if e.lockPool != nil {
		lock, err := e.lockLibrary(ctx, req, progress)
		if err != nil {
			return nil, err
		}
		defer func() {
			if err := lock.Release(ctx); err != nil {
				slog.WarnContext(ctx, "library refresh: releasing library lock failed", "component", "adminjob",
					"library_id", req.LibraryID, "error", err)
			}
		}()
	}

	items, err := e.itemLister.ListLibraryItems(ctx, req.LibraryID, req.Mode)
	if err != nil {
		return nil, fmt.Errorf("load library items: %w", err)
	}

	result := &LibraryRefreshResult{
		LibraryID:   req.LibraryID,
		LibraryName: req.LibraryName,
		Mode:        req.Mode,
		TotalItems:  len(items),
	}
	if result.TotalItems == 0 {
		if progress != nil {
			progress(0, 0, "No library items need refresh")
		}
		return result, nil
	}

	withIDs := make([]string, 0, len(items))
	withoutIDs := make([]string, 0, len(items))
	for _, item := range items {
		if item.TmdbID == "" && item.TvdbID == "" && item.ImdbID == "" {
			withoutIDs = append(withoutIDs, item.ContentID)
		} else {
			withIDs = append(withIDs, item.ContentID)
		}
	}
	result.ItemsWithIDs = len(withIDs)
	result.ItemsWithoutIDs = len(withoutIDs)

	current := 0
	advance := func(message string) {
		current++
		if progress != nil {
			progress(current, result.TotalItems, message)
		}
	}

	if len(withIDs) > 0 {
		if err := e.refreshItemsWithIDs(ctx, withIDs, result, advance); err != nil {
			return nil, err
		}
	}

	if req.Mode == LibraryRefreshModeFull && len(withoutIDs) > 0 {
		if err := e.refreshItemsWithoutIDs(ctx, req.LibraryID, withoutIDs, result, advance); err != nil {
			return nil, err
		}
	}

	if result.RefreshedOK > 0 || result.PipelineOK > 0 {
		e.publish(cache.EventMetadataUpdated, strconv.Itoa(req.LibraryID))
		if e.realtimeHub != nil {
			_ = e.realtimeHub.PublishCatalogItemChanged(ctx, notifications.MetadataUpdateEvent{
				LibraryID: req.LibraryID,
				Change:    "metadata_updated",
			})
		}
	}

	return result, nil
}

func (e *LibraryRefreshExecutor) refreshItemsWithIDs(
	ctx context.Context,
	contentIDs []string,
	result *LibraryRefreshResult,
	advance func(message string),
) error {
	workerCount := libraryRefreshWorkerCount
	if len(contentIDs) < workerCount {
		workerCount = len(contentIDs)
	}

	type refreshResult struct {
		contentID string
		err       error
	}

	jobs := make(chan string, len(contentIDs))
	results := make(chan refreshResult, len(contentIDs))

	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for contentID := range jobs {
				results <- refreshResult{
					contentID: contentID,
					err:       e.refresher.RefreshItemForLibrary(ctx, contentID, result.LibraryID),
				}
			}
		}()
	}

	scheduled := 0
	for _, contentID := range contentIDs {
		if err := e.ensureLibraryEnabled(ctx, result.LibraryID); err != nil {
			close(jobs)
			wg.Wait()
			return err
		}
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return ctx.Err()
		case jobs <- contentID:
			scheduled++
		}
	}
	close(jobs)

	for i := 0; i < scheduled; i++ {
		select {
		case <-ctx.Done():
			wg.Wait()
			return ctx.Err()
		case itemResult := <-results:
			if itemResult.err != nil {
				result.RefreshedFailed++
			} else {
				result.RefreshedOK++
				e.publishCatalogItemChanged(ctx, result.LibraryID, itemResult.contentID)
			}
			advance("Refreshing items with external IDs")
		}
	}

	wg.Wait()
	return nil
}

func (e *LibraryRefreshExecutor) refreshItemsWithoutIDs(
	ctx context.Context,
	libraryID int,
	contentIDs []string,
	result *LibraryRefreshResult,
	advance func(message string),
) error {
	fullPipelineAvailable := e.resolver != nil && e.ingester != nil && e.scanRuns != nil
	for i, contentID := range contentIDs {
		if err := e.ensureLibraryEnabled(ctx, libraryID); err != nil {
			return err
		}
		if i > 0 && e.unmatchedDelay > 0 {
			if err := e.wait(ctx, e.unmatchedDelay); err != nil {
				return err
			}
		}

		if !fullPipelineAvailable {
			result.PipelineFailed++
			advance("Full refresh unavailable for unmatched items")
			continue
		}

		if err := e.refreshUnmatchedItem(ctx, libraryID, contentID); err != nil {
			result.PipelineFailed++
		} else {
			result.PipelineOK++
		}
		advance("Refreshing unmatched items")
	}
	return nil
}

func (e *LibraryRefreshExecutor) refreshUnmatchedItem(ctx context.Context, libraryID int, contentID string) error {
	req, err := e.resolver.ResolveForLibrary(ctx, contentID, libraryID)
	if err != nil {
		return fmt.Errorf("resolve scope: %w", err)
	}
	if req.ScanFolderID != libraryID {
		return fmt.Errorf("resolved scan folder %d does not match requested library %d", req.ScanFolderID, libraryID)
	}

	folder, err := e.folderRepo.GetByID(ctx, req.ScanFolderID)
	if err != nil {
		return fmt.Errorf("load folder %d: %w", req.ScanFolderID, err)
	}
	if !folder.Enabled {
		return fmt.Errorf("load folder %d: library is disabled", req.ScanFolderID)
	}
	scanCtx, scanRun, err := beginDirectSubtreeScan(ctx, e.scanRuns, req.ScanFolderID, req.ScanPath, libraryRefreshScanTrigger)
	if err != nil {
		return fmt.Errorf("ingest subtree: %w", err)
	}
	stopScanHeartbeat := startDirectScanHeartbeat(e.scanRuns, scanRun, directScanHeartbeatEvery)
	ingestResult, err := e.ingester.IngestSubtree(scanCtx, folder, req.ScanPath)
	stopScanHeartbeat()
	if err != nil {
		return fmt.Errorf("ingest subtree: %w", failDirectScan(ctx, e.scanRuns, scanRun, err))
	}
	if err := completeDirectScan(ctx, e.scanRuns, scanRun, ingestResult); err != nil {
		return fmt.Errorf("ingest subtree: %w", err)
	}
	if err := e.refresher.RefreshItemForLibrary(ctx, req.RefreshContentID, req.ScanFolderID); err != nil {
		return fmt.Errorf("refresh metadata: %w", err)
	}
	// Publish only after the metadata refresh: scan_complete advances the resolved
	// list-cache generation, so emitting it earlier lets a rail rebuild from
	// pre-refresh titles/posters and serve them for the whole cache TTL.
	e.publish(cache.EventScanComplete, strconv.Itoa(req.ScanFolderID))
	e.publishCatalogItemChanged(ctx, libraryID, req.RefreshContentID)
	return nil
}

func (e *LibraryRefreshExecutor) publish(eventType, payload string) {
	if e.eventBus == nil {
		return
	}
	_ = e.eventBus.Publish(context.Background(), cache.ChannelCatalog, cache.Event{
		Type:    eventType,
		Payload: payload,
	})
}

func (e *LibraryRefreshExecutor) publishCatalogItemChanged(ctx context.Context, libraryID int, contentID string) {
	if e == nil || e.realtimeHub == nil || contentID == "" {
		return
	}
	_ = e.realtimeHub.PublishCatalogItemChanged(ctx, notifications.MetadataUpdateEvent{
		LibraryID: libraryID,
		ContentID: contentID,
		Change:    "metadata_updated",
	})
}

func decodeLibraryRefreshRequest(data json.RawMessage) (LibraryRefreshRequest, error) {
	var req LibraryRefreshRequest
	if len(data) == 0 {
		return req, fmt.Errorf("missing library refresh payload")
	}
	if err := json.Unmarshal(data, &req); err != nil {
		return req, fmt.Errorf("invalid library refresh payload: %w", err)
	}
	req.Mode = normalizeLibraryRefreshMode(req.Mode)
	return req, nil
}

// lockLibrary takes the library's refresh lock. When it is held, a request
// without waitForLibraryLock gets ErrLibraryRefreshInProgress; a recovered job
// retries until the holder lets go or ctx ends. The holder may be the job's own
// earlier attempt, which keeps the lock until its worker sees the new claim or
// PostgreSQL closes the session of a server that disappeared.
func (e *LibraryRefreshExecutor) lockLibrary(
	ctx context.Context,
	req LibraryRefreshRequest,
	progress func(current, total int, message string),
) (*pglock.Lock, error) {
	interval := e.lockRetryInterval
	if interval <= 0 {
		interval = defaultLibraryLockRetryInterval
	}
	announced := false
	for {
		lock, acquired, err := pglock.TryAcquire(ctx, e.lockPool, libraryRefreshLockKey(req.LibraryID))
		if err != nil {
			return nil, fmt.Errorf("lock library %d for refresh: %w", req.LibraryID, err)
		}
		if acquired {
			return lock, nil
		}
		if !req.waitForLibraryLock {
			return nil, ErrLibraryRefreshInProgress
		}
		if !announced && progress != nil {
			progress(0, 0, "Waiting for an earlier refresh of this library to stop")
			announced = true
		}
		if err := waitWithContext(ctx, interval); err != nil {
			return nil, err
		}
	}
}

func normalizeLibraryRefreshMode(mode LibraryRefreshMode) LibraryRefreshMode {
	if mode == LibraryRefreshModeFull {
		return LibraryRefreshModeFull
	}
	return LibraryRefreshModeQuick
}

func waitWithContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (e *LibraryRefreshExecutor) ensureLibraryEnabled(ctx context.Context, libraryID int) error {
	if e == nil || e.folderRepo == nil {
		return nil
	}
	folder, err := e.folderRepo.GetByID(ctx, libraryID)
	if err != nil {
		return fmt.Errorf("load library %d: %w", libraryID, err)
	}
	if !folder.Enabled {
		return fmt.Errorf("library %d is disabled", libraryID)
	}
	return nil
}
