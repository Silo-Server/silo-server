package adminjob

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/libraryingest"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/naming"
	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

const (
	JobTypeItemRefresh    = "item_refresh"
	ScanScopeMovieParent  = "movie_parent"
	ScanScopeSeriesRoot   = "series_root"
	ScanScopeSeasonFolder = "season_folder"
)

type ItemRefreshMode string

const (
	ItemRefreshModeQuick    ItemRefreshMode = "quick"
	ItemRefreshModeComplete ItemRefreshMode = "complete"
)

var ErrScopeHasNoFiles = errors.New("cannot determine scan scope because this item has no indexed files")

type ScopeResolutionError struct {
	StatusCode int
	Message    string
}

func (e *ScopeResolutionError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

type ItemRefreshRequest struct {
	RequestedContentID     string          `json:"requested_content_id"`
	RequestedType          string          `json:"requested_type"`
	RequestedSeriesID      string          `json:"requested_series_id,omitempty"`
	RequestedSeasonNumber  int             `json:"requested_season_number,omitempty"`
	RequestedEpisodeNumber int             `json:"requested_episode_number,omitempty"`
	RefreshTargetType      string          `json:"refresh_target_type"`
	RefreshContentID       string          `json:"refresh_content_id"`
	ScanFolderID           int             `json:"scan_folder_id"`
	ScanPath               string          `json:"scan_path"`
	ScanScope              string          `json:"scan_scope"`
	Mode                   ItemRefreshMode `json:"mode,omitempty"`
	CanonicalRootPath      string          `json:"canonical_root_path,omitempty"`
}

type ItemRefreshResult struct {
	RequestedContentID string              `json:"requested_content_id"`
	RefreshContentID   string              `json:"refresh_content_id"`
	DetailContentID    string              `json:"detail_content_id,omitempty"`
	ScanPath           string              `json:"scan_path"`
	ScanResult         *scanner.ScanResult `json:"scan_result"`
	MatchedFiles       int                 `json:"matched_files"`
}

type itemRefreshItemRepo interface {
	GetByID(ctx context.Context, contentID string) (*models.MediaItem, error)
}

type itemRefreshSeasonRepo interface {
	GetByID(ctx context.Context, contentID string) (*models.Season, error)
	GetBySeriesAndNumber(ctx context.Context, seriesID string, seasonNum int) (*models.Season, error)
}

type itemRefreshEpisodeRepo interface {
	GetByID(ctx context.Context, contentID string) (*models.Episode, error)
	GetBySeriesAndNumber(ctx context.Context, seriesID string, seasonNum int, episodeNum int) (*models.Episode, error)
	ListBySeries(ctx context.Context, seriesID string) ([]*models.Episode, error)
	ListBySeason(ctx context.Context, seriesID string, seasonNum int) ([]*models.Episode, error)
	ListBySeasonID(ctx context.Context, seasonID string) ([]*models.Episode, error)
}

type itemRefreshFolderRepo interface {
	GetByID(ctx context.Context, id int) (*models.MediaFolder, error)
}

type itemRefreshFileRepo interface {
	GetByContentID(ctx context.Context, contentID string) ([]*models.MediaFile, error)
	GetByEpisodeID(ctx context.Context, episodeID string) ([]*models.MediaFile, error)
	GetByFolderAndPathPrefix(ctx context.Context, folderID int, pathPrefix string) ([]*models.MediaFile, error)
	ClearContentLinksByPathPrefix(ctx context.Context, folderID int, pathPrefix string) (int, error)
}

type itemRefreshRootClaimRepo interface {
	DeleteByFolderAndRoot(ctx context.Context, folderID int, rootPath string) error
}

type itemRefreshGroupClaimRepo interface {
	DeleteByFolderAndObservedPathPrefix(ctx context.Context, folderID int, pathPrefix string) error
}

type itemRefreshSkippedRootRepo interface {
	Upsert(ctx context.Context, root models.SkippedMediaRoot) error
	Delete(ctx context.Context, folderID int, rootPath string) error
	DeleteMissingInScope(ctx context.Context, folderID int, scopePath string, seenRoots []string) error
}

type ItemRefreshResolver struct {
	itemRepo    itemRefreshItemRepo
	seasonRepo  itemRefreshSeasonRepo
	episodeRepo itemRefreshEpisodeRepo
	folderRepo  itemRefreshFolderRepo
	fileRepo    itemRefreshFileRepo
}

func NewItemRefreshResolver(
	itemRepo itemRefreshItemRepo,
	seasonRepo itemRefreshSeasonRepo,
	episodeRepo itemRefreshEpisodeRepo,
	folderRepo itemRefreshFolderRepo,
	fileRepo itemRefreshFileRepo,
) *ItemRefreshResolver {
	return &ItemRefreshResolver{
		itemRepo:    itemRepo,
		seasonRepo:  seasonRepo,
		episodeRepo: episodeRepo,
		folderRepo:  folderRepo,
		fileRepo:    fileRepo,
	}
}

func (r *ItemRefreshResolver) Resolve(ctx context.Context, contentID string) (*ItemRefreshRequest, error) {
	return r.resolve(ctx, contentID, 0, ItemRefreshModeQuick)
}

func (r *ItemRefreshResolver) ResolveWithMode(ctx context.Context, contentID string, mode ItemRefreshMode) (*ItemRefreshRequest, error) {
	return r.resolve(ctx, contentID, 0, normalizeItemRefreshMode(mode))
}

func (r *ItemRefreshResolver) ResolveForLibrary(ctx context.Context, contentID string, libraryID int) (*ItemRefreshRequest, error) {
	if libraryID <= 0 {
		return nil, fmt.Errorf("library_id is required")
	}
	return r.resolve(ctx, contentID, libraryID, ItemRefreshModeQuick)
}

func (r *ItemRefreshResolver) resolve(ctx context.Context, contentID string, libraryID int, mode ItemRefreshMode) (*ItemRefreshRequest, error) {
	if item, err := r.itemRepo.GetByID(ctx, contentID); err == nil {
		if item.Type == "series" {
			return r.resolveSeries(ctx, item, libraryID, mode)
		}
		return r.resolveMovie(ctx, item, libraryID, mode)
	} else if !errors.Is(err, catalog.ErrItemNotFound) {
		return nil, err
	}

	if season, err := r.seasonRepo.GetByID(ctx, contentID); err == nil {
		return r.resolveSeason(ctx, season, libraryID, mode)
	} else if !errors.Is(err, catalog.ErrSeasonNotFound) {
		return nil, err
	}

	episode, err := r.episodeRepo.GetByID(ctx, contentID)
	if err != nil {
		if errors.Is(err, catalog.ErrEpisodeNotFound) {
			return nil, &ScopeResolutionError{
				StatusCode: 404,
				Message:    "Item not found",
			}
		}
		return nil, err
	}
	return r.resolveEpisode(ctx, episode, libraryID, mode)
}

func (r *ItemRefreshResolver) resolveMovie(ctx context.Context, item *models.MediaItem, libraryID int, mode ItemRefreshMode) (*ItemRefreshRequest, error) {
	files, err := r.fileRepo.GetByContentID(ctx, item.ContentID)
	if err != nil {
		return nil, err
	}
	file, err := firstPresentFileInLibrary(files, libraryID)
	if err != nil {
		return nil, err
	}
	req := &ItemRefreshRequest{
		RequestedContentID: item.ContentID,
		RequestedType:      item.Type,
		RefreshTargetType:  "item",
		RefreshContentID:   item.ContentID,
		ScanScope:          ScanScopeMovieParent,
		Mode:               mode,
	}
	if mode == ItemRefreshModeComplete {
		return r.buildCompleteRequest(ctx, file, req)
	}
	return r.buildRequest(ctx, file.MediaFolderID, filepath.Dir(file.FilePath), req)
}

func (r *ItemRefreshResolver) resolveSeries(ctx context.Context, item *models.MediaItem, libraryID int, mode ItemRefreshMode) (*ItemRefreshRequest, error) {
	file, err := r.findSeriesRepresentativeFile(ctx, item.ContentID, libraryID)
	if err != nil {
		return nil, err
	}
	req := &ItemRefreshRequest{
		RequestedContentID: item.ContentID,
		RequestedType:      item.Type,
		RefreshTargetType:  "item",
		RefreshContentID:   item.ContentID,
		ScanScope:          ScanScopeSeriesRoot,
		Mode:               mode,
	}
	if mode == ItemRefreshModeComplete {
		return r.buildCompleteRequest(ctx, file, req)
	}
	scanPath := filepath.Dir(file.FilePath)
	if root, ok := naming.DetectSeriesRoot(file.FilePath, "series"); ok && root != nil && root.RootPath != "" {
		scanPath = filepath.Clean(root.RootPath)
	}
	return r.buildRequest(ctx, file.MediaFolderID, scanPath, req)
}

func (r *ItemRefreshResolver) resolveSeason(ctx context.Context, season *models.Season, libraryID int, mode ItemRefreshMode) (*ItemRefreshRequest, error) {
	file, err := r.findSeasonRepresentativeFile(ctx, season.SeriesID, season.SeasonNumber, season.ContentID, libraryID)
	if err != nil {
		return nil, err
	}
	req := &ItemRefreshRequest{
		RequestedContentID:    season.ContentID,
		RequestedType:         "season",
		RequestedSeriesID:     season.SeriesID,
		RequestedSeasonNumber: season.SeasonNumber,
		RefreshTargetType:     "season",
		RefreshContentID:      season.ContentID,
		ScanScope:             ScanScopeSeasonFolder,
		Mode:                  mode,
	}
	if mode == ItemRefreshModeComplete {
		return r.buildCompleteRequest(ctx, file, req)
	}
	return r.buildRequest(ctx, file.MediaFolderID, filepath.Dir(file.FilePath), req)
}

func (r *ItemRefreshResolver) resolveEpisode(ctx context.Context, episode *models.Episode, libraryID int, mode ItemRefreshMode) (*ItemRefreshRequest, error) {
	files, err := r.fileRepo.GetByEpisodeID(ctx, episode.ContentID)
	if err != nil {
		return nil, err
	}
	file, err := firstPresentFileInLibrary(files, libraryID)
	if err != nil {
		file, err = r.findSeasonRepresentativeFile(ctx, episode.SeriesID, episode.SeasonNumber, episode.SeasonID, libraryID)
		if err != nil {
			return nil, err
		}
	}
	req := &ItemRefreshRequest{
		RequestedContentID:     episode.ContentID,
		RequestedType:          "episode",
		RequestedSeriesID:      episode.SeriesID,
		RequestedSeasonNumber:  episode.SeasonNumber,
		RequestedEpisodeNumber: episode.EpisodeNumber,
		RefreshTargetType:      "episode",
		RefreshContentID:       episode.ContentID,
		ScanScope:              ScanScopeSeasonFolder,
		Mode:                   mode,
	}
	if mode == ItemRefreshModeComplete {
		return r.buildCompleteRequest(ctx, file, req)
	}
	return r.buildRequest(ctx, file.MediaFolderID, filepath.Dir(file.FilePath), req)
}

func (r *ItemRefreshResolver) buildRequest(ctx context.Context, folderID int, scanPath string, req *ItemRefreshRequest) (*ItemRefreshRequest, error) {
	if _, err := r.folderRepo.GetByID(ctx, folderID); err != nil {
		return nil, err
	}
	// The scanner persists canonical file paths after following directory
	// symlinks, so the derived scan path may not share a literal prefix with the
	// configured library root. The folder ownership is established by the linked
	// media file we selected above, not by a raw string-prefix match here.
	cleanPath := filepath.Clean(scanPath)
	req.ScanFolderID = folderID
	req.ScanPath = cleanPath
	return req, nil
}

func (r *ItemRefreshResolver) buildCompleteRequest(ctx context.Context, file *models.MediaFile, req *ItemRefreshRequest) (*ItemRefreshRequest, error) {
	scanPath, canonicalRootPath, err := r.deriveCompleteRefreshScope(ctx, file)
	if err != nil {
		return nil, err
	}
	req.CanonicalRootPath = canonicalRootPath
	return r.buildRequest(ctx, file.MediaFolderID, scanPath, req)
}

func (r *ItemRefreshResolver) deriveCompleteRefreshScope(ctx context.Context, file *models.MediaFile) (string, string, error) {
	folder, err := r.folderRepo.GetByID(ctx, file.MediaFolderID)
	if err != nil {
		return "", "", err
	}

	cleanFilePath := filepath.Clean(file.FilePath)
	parentDir := filepath.Clean(filepath.Dir(cleanFilePath))
	if file.CanonicalRootPath != "" {
		canonicalRootPath := filepath.Clean(file.CanonicalRootPath)
		switch {
		case shouldRefreshSeriesCanonicalRoot(folder, file):
			return canonicalRootPath, canonicalRootPath, nil
		case file.ContentID != "" && canonicalRootPath == parentDir:
			return canonicalRootPath, canonicalRootPath, nil
		case file.ContentID != "":
			return cleanFilePath, canonicalRootPath, nil
		default:
			return cleanFilePath, canonicalRootPath, nil
		}
	}
	if file.EpisodeID != "" || file.SeasonNumber != 0 || file.EpisodeNumber != 0 {
		return parentDir, "", nil
	}
	return cleanFilePath, "", nil
}

func shouldRefreshSeriesCanonicalRoot(folder *models.MediaFolder, file *models.MediaFile) bool {
	if file == nil {
		return false
	}
	if file.EpisodeID != "" || file.SeasonNumber != 0 || file.EpisodeNumber != 0 {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(file.BaseType), "series") {
		return true
	}
	if folder == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(folder.Type)) {
	case "series", "tv", "show", "tvshows":
		return true
	default:
		return false
	}
}

func (r *ItemRefreshResolver) findSeriesRepresentativeFile(ctx context.Context, seriesID string, libraryID int) (*models.MediaFile, error) {
	episodes, err := r.episodeRepo.ListBySeries(ctx, seriesID)
	if err != nil {
		return nil, err
	}
	for _, episode := range episodes {
		files, fileErr := r.fileRepo.GetByEpisodeID(ctx, episode.ContentID)
		if fileErr != nil {
			return nil, fileErr
		}
		if file, pickErr := firstPresentFileInLibrary(files, libraryID); pickErr == nil {
			return file, nil
		}
	}

	files, err := r.fileRepo.GetByContentID(ctx, seriesID)
	if err != nil {
		return nil, err
	}
	return firstPresentFileInLibrary(files, libraryID)
}

func (r *ItemRefreshResolver) findSeasonRepresentativeFile(ctx context.Context, seriesID string, seasonNumber int, seasonID string, libraryID int) (*models.MediaFile, error) {
	var (
		episodes []*models.Episode
		err      error
	)
	if seasonID != "" {
		episodes, err = r.episodeRepo.ListBySeasonID(ctx, seasonID)
	} else {
		episodes, err = r.episodeRepo.ListBySeason(ctx, seriesID, seasonNumber)
	}
	if err != nil {
		return nil, err
	}
	for _, episode := range episodes {
		files, fileErr := r.fileRepo.GetByEpisodeID(ctx, episode.ContentID)
		if fileErr != nil {
			return nil, fileErr
		}
		if file, pickErr := firstPresentFileInLibrary(files, libraryID); pickErr == nil {
			return file, nil
		}
	}
	return nil, &ScopeResolutionError{
		StatusCode: 409,
		Message:    ErrScopeHasNoFiles.Error(),
	}
}

func firstPresentFileInLibrary(files []*models.MediaFile, libraryID int) (*models.MediaFile, error) {
	for _, file := range files {
		if file == nil || file.MissingSince != nil {
			continue
		}
		if libraryID > 0 && file.MediaFolderID != libraryID {
			continue
		}
		return file, nil
	}
	return nil, &ScopeResolutionError{
		StatusCode: 409,
		Message:    ErrScopeHasNoFiles.Error(),
	}
}

type ItemRefreshIngester interface {
	IngestSubtree(ctx context.Context, folder *models.MediaFolder, subtreePath string) (*libraryingest.Result, error)
}

type ItemRefreshExecutor struct {
	folderRepo      itemRefreshFolderRepo
	fileRepo        itemRefreshFileRepo
	rootClaimRepo   itemRefreshRootClaimRepo
	groupClaimRepo  itemRefreshGroupClaimRepo
	skippedRootRepo itemRefreshSkippedRootRepo
	seasonRepo      itemRefreshSeasonRepo
	episodeRepo     itemRefreshEpisodeRepo
	ingester        ItemRefreshIngester
	refresher       interface {
		RefreshItem(ctx context.Context, contentID string) error
		RefreshItemForLibrary(ctx context.Context, contentID string, folderID int) error
		RefreshTargetForLibrary(ctx context.Context, targetType, contentID string, folderID int) error
	}
	eventBus    cache.EventBus
	realtimeHub *notifications.Hub
}

func NewItemRefreshExecutor(
	folderRepo itemRefreshFolderRepo,
	fileRepo itemRefreshFileRepo,
	rootClaimRepo itemRefreshRootClaimRepo,
	groupClaimRepo itemRefreshGroupClaimRepo,
	skippedRootRepo itemRefreshSkippedRootRepo,
	seasonRepo itemRefreshSeasonRepo,
	episodeRepo itemRefreshEpisodeRepo,
	ingester ItemRefreshIngester,
	refresher interface {
		RefreshItem(ctx context.Context, contentID string) error
		RefreshItemForLibrary(ctx context.Context, contentID string, folderID int) error
		RefreshTargetForLibrary(ctx context.Context, targetType, contentID string, folderID int) error
	},
	eventBus cache.EventBus,
	realtimeHub *notifications.Hub,
) *ItemRefreshExecutor {
	return &ItemRefreshExecutor{
		folderRepo:      folderRepo,
		fileRepo:        fileRepo,
		rootClaimRepo:   rootClaimRepo,
		groupClaimRepo:  groupClaimRepo,
		skippedRootRepo: skippedRootRepo,
		seasonRepo:      seasonRepo,
		episodeRepo:     episodeRepo,
		ingester:        ingester,
		refresher:       refresher,
		eventBus:        eventBus,
		realtimeHub:     realtimeHub,
	}
}

func (e *ItemRefreshExecutor) Execute(ctx context.Context, req ItemRefreshRequest, progress func(current, total int, message string)) (*ItemRefreshResult, error) {
	if e.folderRepo == nil || e.ingester == nil || e.refresher == nil {
		return nil, fmt.Errorf("resolve scan scope: item refresh executor is not fully configured")
	}
	req.Mode = normalizeItemRefreshMode(req.Mode)
	folder, err := e.folderRepo.GetByID(ctx, req.ScanFolderID)
	if err != nil {
		return nil, fmt.Errorf("resolve scan scope: loading folder: %w", err)
	}
	if !folder.Enabled {
		return nil, fmt.Errorf("resolve scan scope: library is disabled")
	}

	refreshContentID := req.RefreshContentID
	detailContentID := req.RequestedContentID

	if req.Mode == ItemRefreshModeComplete {
		if e.fileRepo == nil || e.rootClaimRepo == nil || e.groupClaimRepo == nil {
			return nil, fmt.Errorf("resolve scan scope: complete item refresh requires file, root claim, and group claim repositories")
		}
		if err := e.prepareCompleteRefresh(ctx, req); err != nil {
			return nil, fmt.Errorf("prepare complete refresh: %w", err)
		}
	}

	if progress != nil {
		progress(1, 3, "Scanning scope")
	}
	ingestResult, err := e.ingester.IngestSubtree(ctx, folder, req.ScanPath)
	if err != nil {
		return nil, fmt.Errorf("scan scope: %w", err)
	}
	scanResult := ingestResult.ScanResult

	if progress != nil {
		progress(2, 3, "Matching discovered files")
	}
	if refreshedFolder, reloadErr := e.folderRepo.GetByID(ctx, req.ScanFolderID); reloadErr == nil && !refreshedFolder.Enabled {
		return nil, fmt.Errorf("match discovered files: library is disabled")
	}
	matched := ingestResult.MatchedFiles

	if req.Mode == ItemRefreshModeComplete {
		resolvedRefreshContentID, resolvedDetailContentID, err := e.resolveCompleteRefreshTargets(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("resolve rebuilt item: %w", err)
		}
		refreshContentID = resolvedRefreshContentID
		if resolvedDetailContentID != "" {
			detailContentID = resolvedDetailContentID
		}
	}

	if progress != nil {
		progress(3, 3, "Refreshing metadata")
	}
	if refreshedFolder, reloadErr := e.folderRepo.GetByID(ctx, req.ScanFolderID); reloadErr == nil && !refreshedFolder.Enabled {
		return nil, fmt.Errorf("refresh metadata: library is disabled")
	}
	refreshTargetType := strings.TrimSpace(req.RefreshTargetType)
	if refreshTargetType == "" {
		switch req.RequestedType {
		case "season", "episode":
			refreshTargetType = req.RequestedType
		default:
			refreshTargetType = "item"
		}
	}
	if err := e.refresher.RefreshTargetForLibrary(ctx, refreshTargetType, refreshContentID, req.ScanFolderID); err != nil {
		return nil, fmt.Errorf("refresh metadata: %w", err)
	}

	e.publish(cache.EventScanComplete, strconv.Itoa(req.ScanFolderID))
	e.publish(cache.EventMetadataUpdated, refreshContentID)
	if e.realtimeHub != nil {
		if scanResult != nil && (scanResult.New > 0 || scanResult.Updated > 0 || scanResult.Missing > 0 || matched > 0) {
			_ = e.realtimeHub.PublishCatalogLibraryChanged(ctx, notifications.LibraryChangeEvent{
				LibraryID:    req.ScanFolderID,
				Reason:       "item_refresh",
				New:          scanResult.New,
				Updated:      scanResult.Updated,
				Missing:      scanResult.Missing,
				MatchedFiles: matched,
			})
		}
		_ = e.realtimeHub.PublishCatalogItemChanged(ctx, notifications.MetadataUpdateEvent{
			LibraryID: req.ScanFolderID,
			ContentID: refreshContentID,
			Change:    "metadata_updated",
		})
	}

	return &ItemRefreshResult{
		RequestedContentID: req.RequestedContentID,
		RefreshContentID:   refreshContentID,
		DetailContentID:    detailContentID,
		ScanPath:           req.ScanPath,
		ScanResult:         scanResult,
		MatchedFiles:       matched,
	}, nil
}

func (e *ItemRefreshExecutor) prepareCompleteRefresh(ctx context.Context, req ItemRefreshRequest) error {
	if req.CanonicalRootPath != "" {
		if err := e.rootClaimRepo.DeleteByFolderAndRoot(ctx, req.ScanFolderID, req.CanonicalRootPath); err != nil {
			return fmt.Errorf("deleting root claim: %w", err)
		}
	}
	if e.groupClaimRepo != nil {
		if err := e.groupClaimRepo.DeleteByFolderAndObservedPathPrefix(ctx, req.ScanFolderID, req.ScanPath); err != nil {
			return fmt.Errorf("deleting group claims: %w", err)
		}
	}
	if _, err := e.fileRepo.ClearContentLinksByPathPrefix(ctx, req.ScanFolderID, req.ScanPath); err != nil {
		return fmt.Errorf("clearing content links: %w", err)
	}
	return nil
}

func (e *ItemRefreshExecutor) resolveCompleteRefreshTargets(ctx context.Context, req ItemRefreshRequest) (string, string, error) {
	files, err := e.fileRepo.GetByFolderAndPathPrefix(ctx, req.ScanFolderID, req.ScanPath)
	if err != nil {
		return "", "", fmt.Errorf("loading rebuilt files: %w", err)
	}

	distinctContentIDs := make(map[string]struct{})
	for _, file := range files {
		if file == nil || file.MissingSince != nil || file.ContentID == "" {
			continue
		}
		distinctContentIDs[file.ContentID] = struct{}{}
	}

	switch len(distinctContentIDs) {
	case 0:
		return "", "", fmt.Errorf("no rebuilt content found in refresh scope")
	case 1:
	default:
		return "", "", fmt.Errorf("multiple rebuilt content IDs found in refresh scope")
	}

	refreshContentID := ""
	for contentID := range distinctContentIDs {
		refreshContentID = contentID
	}

	detailContentID := refreshContentID
	switch req.RequestedType {
	case "season":
		if e.seasonRepo != nil {
			if season, lookupErr := e.seasonRepo.GetBySeriesAndNumber(ctx, refreshContentID, req.RequestedSeasonNumber); lookupErr == nil && season != nil {
				detailContentID = season.ContentID
				refreshContentID = season.ContentID
			} else {
				return "", "", fmt.Errorf("rebuilt season target not found")
			}
		}
	case "episode":
		if e.episodeRepo != nil {
			if episode, lookupErr := e.episodeRepo.GetBySeriesAndNumber(ctx, refreshContentID, req.RequestedSeasonNumber, req.RequestedEpisodeNumber); lookupErr == nil && episode != nil {
				detailContentID = episode.ContentID
				refreshContentID = episode.ContentID
			} else {
				return "", "", fmt.Errorf("rebuilt episode target not found")
			}
		}
	}

	return refreshContentID, detailContentID, nil
}

func normalizeItemRefreshMode(mode ItemRefreshMode) ItemRefreshMode {
	if mode == ItemRefreshModeComplete {
		return ItemRefreshModeComplete
	}
	return ItemRefreshModeQuick
}

func (e *ItemRefreshExecutor) publish(eventType, payload string) {
	if e.eventBus == nil {
		return
	}
	_ = e.eventBus.Publish(context.Background(), cache.ChannelCatalog, cache.Event{
		Type:    eventType,
		Payload: payload,
	})
}
