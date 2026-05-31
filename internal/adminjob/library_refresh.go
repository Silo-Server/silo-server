package adminjob

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/cache"
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
			OR COALESCE(mi.poster_path, '') LIKE '%//poster/%'
			OR COALESCE(mi.backdrop_path, '') LIKE '%//backdrop/%'
			OR COALESCE(mi.logo_path, '') LIKE '%//logo/%'
			OR mi.refresh_failures > 0
			OR mi.episode_metadata_incomplete = TRUE
			OR (
				LOWER(TRIM(COALESCE(mi.status, ''))) = 'matched'
				AND COALESCE(mi.tmdb_id, '') = ''
				AND (
					COALESCE(mi.tvdb_id, '') <> ''
					OR COALESCE(mi.imdb_id, '') <> ''
				)
			)
			OR EXISTS (
				SELECT 1
				FROM stale_media_ids smi
				WHERE smi.content_id = mi.content_id
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
	refresher      libraryRefreshRefresher
	eventBus       cache.EventBus
	realtimeHub    *notifications.Hub
	unmatchedDelay time.Duration
	wait           func(ctx context.Context, delay time.Duration) error
}

func NewLibraryRefreshExecutor(
	itemLister libraryRefreshItemLister,
	folderRepo libraryRefreshFolderRepo,
	resolver libraryRefreshScopeResolver,
	ingester libraryRefreshIngester,
	refresher libraryRefreshRefresher,
	eventBus cache.EventBus,
	realtimeHub *notifications.Hub,
) *LibraryRefreshExecutor {
	return &LibraryRefreshExecutor{
		itemLister:     itemLister,
		folderRepo:     folderRepo,
		resolver:       resolver,
		ingester:       ingester,
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
	fullPipelineAvailable := e.resolver != nil && e.ingester != nil
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
	if _, err := e.ingester.IngestSubtree(ctx, folder, req.ScanPath); err != nil {
		return fmt.Errorf("ingest subtree: %w", err)
	}
	if err := e.refresher.RefreshItemForLibrary(ctx, req.RefreshContentID, req.ScanFolderID); err != nil {
		return fmt.Errorf("refresh metadata: %w", err)
	}
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
