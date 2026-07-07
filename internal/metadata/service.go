package metadata

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/catalog/reattribute"
	"github.com/Silo-Server/silo-server/internal/contentid"
	"github.com/Silo-Server/silo-server/internal/idgen"
	"github.com/Silo-Server/silo-server/internal/lang"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/naming"
	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// FileContentUpdater updates content_id on media_files.
type FileContentUpdater interface {
	UpdateContentID(ctx context.Context, fileID int, contentID string) error
	ReplaceContentID(ctx context.Context, oldContentID, newContentID string) (int, error)
	FindContentIDByRootPath(ctx context.Context, folderID int, rootPath, preferredType string) (string, error)
	FindContentIDByObservedRootPath(ctx context.Context, folderID int, observedRootPath, preferredType string) (string, error)
	FindContentIDByGroupKey(ctx context.Context, folderID int, groupKeyVersion int, contentGroupKey, preferredType string) (string, error)
	ListByGroupKey(ctx context.Context, folderID int, groupKeyVersion int, contentGroupKey string) ([]*models.MediaFile, error)
	ListByObservedRootPath(ctx context.Context, folderID int, observedRootPath string) ([]*models.MediaFile, error)
	UpdateContentIDByObservedRootPath(ctx context.Context, folderID int, observedRootPath, contentID string) (int, error)
}

// EpisodeLinker extends FileContentUpdater with episode linking.
type EpisodeLinker interface {
	FileContentUpdater
	UpdateEpisodeLink(ctx context.Context, fileID int, episodeID string, seasonNum, episodeNum int) error
	ListBySeriesUnlinked(ctx context.Context, seriesContentID string) ([]*models.MediaFile, error)
}

type bulkSeriesEpisodeLinker interface {
	BulkLinkEpisodesBySeries(ctx context.Context, seriesContentID string) (int, error)
}

// UnmatchedFileLister lists files that have no content_id.
type UnmatchedFileLister interface {
	ClaimUnmatched(ctx context.Context, limit int) ([]*models.MediaFile, error)
	ClaimUnmatchedByFolderAndPathPrefix(ctx context.Context, folderID int, pathPrefix string, limit int, attemptBefore time.Time) ([]*models.MediaFile, error)
	MarkMatchAttempted(ctx context.Context, fileID int) error
}

// UnmatchedItemLister lists unmatched items scoped to a library subtree.
type UnmatchedItemLister interface {
	ListUnmatchedByFolderAndPathPrefix(ctx context.Context, folderID int, pathPrefix string, limit int) ([]string, error)
}

// metadataItemRepo defines item repository methods used by MetadataService.
// The concrete *catalog.ItemRepository satisfies this interface.
type metadataItemRepo interface {
	GetByID(ctx context.Context, contentID string) (*models.MediaItem, error)
	GetByExternalID(ctx context.Context, tmdbID, imdbID, tvdbID, itemType string) (*models.MediaItem, error)
	GetByTitleYearType(ctx context.Context, title string, year int, itemType string) (*models.MediaItem, error)
	Upsert(ctx context.Context, item *models.MediaItem) error
	IncrementRefreshFailure(ctx context.Context, contentID string) error
	ReplacePeople(ctx context.Context, contentID string, people []models.ItemPerson) error
	ListUnmatchedByFolderAndPathPrefix(ctx context.Context, folderID int, pathPrefix string, limit int) ([]string, error)
}

type metadataItemDeleteRepo interface {
	Delete(ctx context.Context, contentID string) ([]string, error)
}

type metadataProviderIDRepo interface {
	GetByContentID(ctx context.Context, contentID string) ([]*models.MediaItemProviderID, error)
	ReplaceByContentID(ctx context.Context, contentID string, providerIDs map[string]string) error
	FindContentIDByProviderIDs(ctx context.Context, providerIDs map[string]string, itemType, excludeContentID string) (string, error)
}

type metadataStaleIDRepo interface {
	GetByContentID(ctx context.Context, contentID string) ([]*models.StaleMediaID, error)
	Upsert(ctx context.Context, contentID, provider, providerID string) error
	DeleteByContentID(ctx context.Context, contentID string) error
}

type metadataRefreshDebtRepo interface {
	Get(ctx context.Context, contentID string) (*models.MetadataRefreshDebt, error)
	GetTarget(ctx context.Context, targetType, contentID string) (*models.MetadataRefreshDebt, error)
	UpsertDebt(ctx context.Context, contentID string, priority int, reasonMask int64, nextRefreshAt time.Time) error
	UpsertTargetDebt(ctx context.Context, targetType, contentID string, priority int, reasonMask int64, nextRefreshAt time.Time) error
	RequestDue(ctx context.Context, targetType, contentID string, priority int, reasonMask int64, nextRefreshAt time.Time, cooldown time.Duration) error
	MarkFailure(ctx context.Context, contentID string, priority int, reasonMask int64, nextRefreshAt time.Time, attemptCount int, lastError string) error
	MarkTargetFailure(ctx context.Context, targetType, contentID string, priority int, reasonMask int64, nextRefreshAt time.Time, attemptCount int, lastError string) error
	MarkSuccess(ctx context.Context, contentID string, priority int, reasonMask int64, nextRefreshAt time.Time) error
	MarkTargetSuccess(ctx context.Context, targetType, contentID string, priority int, reasonMask int64, nextRefreshAt time.Time) error
	DeleteDebt(ctx context.Context, contentID string) error
	DeleteTargetDebt(ctx context.Context, targetType, contentID string) error
}

// metadataLibraryRepo defines library membership methods used by
// MetadataService. The concrete *catalog.LibraryItemRepository satisfies this.
type metadataLibraryRepo interface {
	Upsert(ctx context.Context, contentID string, folderID int, firstSeenAt time.Time) error
	GetFolderIDsForItem(ctx context.Context, contentID string) ([]int, error)
	GetDistinctMetadataLanguagesForItem(ctx context.Context, contentID string) ([]string, error)
	CountFoldersForItem(ctx context.Context, contentID string) (int, error)
}

// metadataRootClaimRepo defines root-claim methods used by createOrFindSkeleton
// for canonical-root deduplication. The concrete *catalog.RootClaimRepository
// satisfies this through its Get and ClaimRoot methods.
type metadataRootClaimRepo interface {
	Get(ctx context.Context, folderID int, rootPath string) (*models.MediaItemRoot, error)
	ClaimRoot(ctx context.Context, folderID int, rootPath, contentID string) error
}

type metadataScannedRootRepo interface {
	Get(ctx context.Context, folderID int, rootPath string) (*models.ScannedMediaRoot, error)
}

type metadataGroupClaimRepo interface {
	Get(ctx context.Context, folderID int, groupKeyVersion int, contentGroupKey string) (*models.MediaItemGroup, error)
	ClaimGroup(ctx context.Context, folderID int, groupKeyVersion int, contentGroupKey, contentID string) error
	ClaimAndRelinkFiles(ctx context.Context, folderID int, groupKeyVersion int, contentGroupKey, contentID string) (int, error)
}

type metadataScannedGroupRepo interface {
	Get(ctx context.Context, folderID int, groupKeyVersion int, contentGroupKey string) (*models.ScannedMediaGroup, error)
}

type metadataGroupOverrideRepo interface {
	Get(ctx context.Context, folderID int, groupKeyVersion int, contentGroupKey string) (*models.MediaGroupOverride, error)
}

type metadataObservedLocationRepo interface {
	Get(ctx context.Context, folderID int, observedRootPath string) (*models.ObservedMediaLocation, error)
}

// metadataSkippedRootRepo defines skipped-root methods used during skeleton
// creation for legacy diagnostics. The concrete *SkippedRootRepository
// satisfies this interface.
type metadataSkippedRootRepo interface {
	UpsertObservedFile(ctx context.Context, folderID int, rootPath, reason, sampleFilePath string) error
	Delete(ctx context.Context, folderID int, rootPath string) error
}

// metadataEpisodeRepo defines episode repository methods used by MetadataService.
// The concrete *catalog.EpisodeRepository satisfies this interface.
type metadataEpisodeRepo interface {
	GetByID(ctx context.Context, contentID string) (*models.Episode, error)
	GetBySeriesAndNumber(ctx context.Context, seriesID string, season, episode int) (*models.Episode, error)
	ListBySeriesAndAirDates(ctx context.Context, seriesID string, airDates []string) (map[string][]*models.Episode, error)
	ListBySeries(ctx context.Context, seriesID string) ([]*models.Episode, error)
	ListBySeasonID(ctx context.Context, seasonID string) ([]*models.Episode, error)
	Upsert(ctx context.Context, ep *models.Episode) error
	BulkUpsert(ctx context.Context, seriesID string, episodes []*models.Episode) error
}

// metadataSeasonRepo defines season repository methods used by MetadataService.
// The concrete *catalog.SeasonRepository satisfies this interface.
type metadataSeasonRepo interface {
	GetByID(ctx context.Context, contentID string) (*models.Season, error)
	GetBySeriesAndNumber(ctx context.Context, seriesID string, seasonNum int) (*models.Season, error)
	Upsert(ctx context.Context, s *models.Season) error
	BulkUpsert(ctx context.Context, seasons []*models.Season) error
}

// metadataFolderRepo defines folder repository methods used by MetadataService
// for looking up per-library settings like metadata language.
type metadataFolderRepo interface {
	GetByID(ctx context.Context, id int) (*models.MediaFolder, error)
}

// metadataVideoRepo persists remote provider videos. The concrete
// *catalog.VideoRepository satisfies this.
type metadataVideoRepo interface {
	ReplaceByContentID(ctx context.Context, contentID string, videos []models.ItemVideo) error
}

// AutoTranslator is the seam to the metadata AI translation service: after a
// refresh, libraries that opted in get missing localizations filled by AI.
// Implemented by *translation.Service; AutoEnqueue must be cheap and must
// never fail the refresh (it logs its own errors).
type AutoTranslator interface {
	AutoEnqueue(ctx context.Context, itemContentID, language string)
}

type metadataContentFileLister interface {
	GetByContentID(ctx context.Context, contentID string) ([]*models.MediaFile, error)
}

type metadataServiceHooks struct {
	process                   func(ctx context.Context, req ProcessRequest) (*ProcessResult, error)
	createOrFindSkeleton      func(ctx context.Context, file *models.MediaFile, folderID int) (*skeletonResult, error)
	updateItemStatus          func(ctx context.Context, contentID, status string) error
	linkSeriesFilesToEpisodes func(ctx context.Context, seriesID string)
	ensureSeriesEpisodeLinks  func(ctx context.Context, seriesID string) error
}

var trustedSearchIDKeys = []string{"tmdb", "tvdb", "imdb"}

var ErrMetadataNotFound = errors.New("no metadata found from any provider")

const (
	metadataRefreshNudgeCooldown   = time.Hour
	metadataOnDemandRefreshTimeout = 2 * time.Minute
)

// isProvider404 returns true if the error string indicates an HTTP 404 from a provider.
func isProvider404(err error) bool {
	return err != nil && strings.Contains(err.Error(), "HTTP 404")
}

func isDurableProviderSlug(slug string) bool {
	switch strings.ToLower(strings.TrimSpace(slug)) {
	case "tmdb", "imdb", "tvdb":
		return true
	default:
		return false
	}
}

func dropProviderID(providerIDs map[string]string, provider string) {
	if providerIDs == nil {
		return
	}
	delete(providerIDs, strings.ToLower(strings.TrimSpace(provider)))
}

func handleProvider404(
	provider404s map[string]string,
	providerIDs map[string]string,
	provider string,
	err error,
	attrs ...any,
) bool {
	if !isProvider404(err) {
		return false
	}

	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return true
	}

	logAttrs := append([]any{"provider", provider}, attrs...)
	if providerID := strings.TrimSpace(providerIDs[provider]); providerID != "" {
		if provider404s != nil && isDurableProviderSlug(provider) {
			provider404s[provider] = providerID
		}
		logAttrs = append(logAttrs, "provider_id", providerID)
		dropProviderID(providerIDs, provider)
	}
	logAttrs = append(logAttrs, "error", err)
	slog.Info("metadata: provider returned 404 for stale or invalid external id", logAttrs...)
	return true
}

func handleChildProvider404(
	provider string,
	providerIDs map[string]string,
	err error,
	attrs ...any,
) bool {
	if !isProvider404(err) {
		return false
	}

	provider = strings.ToLower(strings.TrimSpace(provider))
	logAttrs := append([]any{"provider", provider}, attrs...)
	if provider != "" && providerIDs != nil {
		if providerID := strings.TrimSpace(providerIDs[provider]); providerID != "" {
			logAttrs = append(logAttrs, "provider_id", providerID)
		}
	}
	logAttrs = append(logAttrs, "error", err)
	slog.Info("metadata: provider returned 404 for unavailable child metadata", logAttrs...)
	return true
}

// MetadataService is the unified pipeline orchestrator.
type MetadataService struct {
	chainRepo               *ChainRepository
	pluginResolver          pluginMetadataResolver
	enabledChecker          InstallationEnabledChecker
	itemRepo                metadataItemRepo
	providerIDRepo          metadataProviderIDRepo
	episodeRepo             metadataEpisodeRepo
	seasonRepo              metadataSeasonRepo
	libraryRepo             metadataLibraryRepo
	folderRepo              metadataFolderRepo
	itemLocalizationRepo    *catalog.MediaItemLocalizationRepository
	seasonLocalizationRepo  *catalog.SeasonLocalizationRepository
	episodeLocalizationRepo *catalog.EpisodeLocalizationRepository
	autoTranslator          AutoTranslator // optional; set via SetAutoTranslator
	personRepo              *catalog.PersonRepository
	videoRepo               metadataVideoRepo
	fileRepo                FileContentUpdater
	skippedRootRepo         metadataSkippedRootRepo
	staleIDRepo             metadataStaleIDRepo
	refreshDebtRepo         metadataRefreshDebtRepo
	rootClaimRepo           metadataRootClaimRepo
	scannedRootRepo         metadataScannedRootRepo
	groupClaimRepo          metadataGroupClaimRepo
	scannedGroupRepo        metadataScannedGroupRepo
	groupOverrideRepo       metadataGroupOverrideRepo
	observedLocationRepo    metadataObservedLocationRepo
	dbPool                  *pgxpool.Pool

	dedupLocks      keyedDedupLocks
	onDemandRefresh keyedRefreshSet
	seriesWorkMu    sync.Mutex
	seriesWork      map[string]*seriesEpisodeWork
	hooks           metadataServiceHooks
	imageCacher     ImageCacher
	imageCacheJobs  ImageCacheJobEnqueuer
	autoCacheImages atomic.Bool // hot-reloaded from metadata.cache_images
	imageResolver   interface {
		ResolveImageURL(ctx context.Context, path string, variant string) string
	}

	// chainCache caches resolved provider chains keyed by "folderID:contentLevel".
	// Chains are static between admin edits, so a short TTL avoids thousands of
	// redundant DB queries per batch.
	chainCacheMu  sync.RWMutex
	chainCache    map[string]chainCacheEntry
	chainCacheTTL time.Duration
}

type chainCacheEntry struct {
	providers []Provider
	expiresAt time.Time
}

type seriesEpisodeWork struct {
	done chan struct{}
	err  error
}

type keyedDedupLocks struct {
	mu    sync.Mutex
	locks map[string]*dedupLockRef
}

type keyedRefreshSet struct {
	mu      sync.Mutex
	running map[string]struct{}
}

type dedupLockRef struct {
	mu   sync.Mutex
	refs int
}

type localProviderContext struct {
	filePath                  string
	representativeFilePath    string
	observedRootPath          string
	allGroupFilePaths         []string
	primarySidecarSearchPaths []string
}

type chainResolver func(contentLevel string) ([]Provider, error)

// NewMetadataService creates a new MetadataService.
func NewMetadataService(
	chainRepo *ChainRepository,
	pluginResolver pluginMetadataResolver,
	enabledChecker InstallationEnabledChecker,
	itemRepo *catalog.ItemRepository,
	providerIDRepo *catalog.ProviderIDRepository,
	episodeRepo *catalog.EpisodeRepository,
	seasonRepo *catalog.SeasonRepository,
	libraryRepo *catalog.LibraryItemRepository,
	folderRepo *catalog.FolderRepository,
	personRepo *catalog.PersonRepository,
	fileRepo FileContentUpdater,
	skippedRootRepo *SkippedRootRepository,
	staleIDRepo metadataStaleIDRepo,
	rootClaimRepo *catalog.RootClaimRepository,
) *MetadataService {
	var itemLocalizationRepo *catalog.MediaItemLocalizationRepository
	var seasonLocalizationRepo *catalog.SeasonLocalizationRepository
	var episodeLocalizationRepo *catalog.EpisodeLocalizationRepository
	var scannedRootRepo metadataScannedRootRepo
	var scannedGroupRepo metadataScannedGroupRepo
	var groupClaimRepo metadataGroupClaimRepo
	var groupOverrideRepo metadataGroupOverrideRepo
	var observedLocationRepo metadataObservedLocationRepo
	var videoRepo metadataVideoRepo
	var dbPool *pgxpool.Pool
	if folderRepo != nil {
		pool := folderRepo.Pool()
		dbPool = pool
		videoRepo = catalog.NewVideoRepository(pool)
		itemLocalizationRepo = catalog.NewMediaItemLocalizationRepository(pool)
		seasonLocalizationRepo = catalog.NewSeasonLocalizationRepository(pool)
		episodeLocalizationRepo = catalog.NewEpisodeLocalizationRepository(pool)
		scannedRootRepo = scanner.NewScannedRootRepository(pool)
		scannedGroupRepo = scanner.NewScannedGroupRepository(pool)
		groupClaimRepo = catalog.NewGroupClaimRepository(pool)
		groupOverrideRepo = scanner.NewMediaGroupOverrideRepository(pool)
		observedLocationRepo = scanner.NewObservedLocationRepository(pool)
	}
	return &MetadataService{
		chainRepo:               chainRepo,
		pluginResolver:          pluginResolver,
		enabledChecker:          enabledChecker,
		itemRepo:                itemRepo,
		providerIDRepo:          providerIDRepo,
		episodeRepo:             episodeRepo,
		seasonRepo:              seasonRepo,
		libraryRepo:             libraryRepo,
		folderRepo:              folderRepo,
		itemLocalizationRepo:    itemLocalizationRepo,
		seasonLocalizationRepo:  seasonLocalizationRepo,
		episodeLocalizationRepo: episodeLocalizationRepo,
		personRepo:              personRepo,
		videoRepo:               videoRepo,
		fileRepo:                fileRepo,
		skippedRootRepo:         skippedRootRepo,
		staleIDRepo:             staleIDRepo,
		refreshDebtRepo:         NewRefreshDebtRepository(dbPool),
		rootClaimRepo:           rootClaimRepo,
		scannedRootRepo:         scannedRootRepo,
		groupClaimRepo:          groupClaimRepo,
		scannedGroupRepo:        scannedGroupRepo,
		groupOverrideRepo:       groupOverrideRepo,
		observedLocationRepo:    observedLocationRepo,
		dbPool:                  dbPool,
		chainCache:              make(map[string]chainCacheEntry),
		chainCacheTTL:           60 * time.Second,
	}
}

// SetImageCacher enables S3 image caching during metadata persistence.
// SetAutoTranslator wires the metadata AI translation fallback. Optional;
// without it, refreshes simply skip the auto-translate hook.
func (s *MetadataService) SetAutoTranslator(t AutoTranslator) {
	s.autoTranslator = t
}

func (s *MetadataService) SetImageCacher(c ImageCacher) {
	s.imageCacher = c
}

func (s *MetadataService) SetImageCacheJobEnqueuer(enqueuer ImageCacheJobEnqueuer) {
	s.imageCacheJobs = enqueuer
}

// SetAutoCacheImages controls whether refresh pipelines automatically cache
// provider images into object storage. Explicit admin image applies still use
// the configured image cacher when available. Safe for concurrent use.
func (s *MetadataService) SetAutoCacheImages(enabled bool) {
	s.autoCacheImages.Store(enabled)
}

// SetImageResolver sets the resolver used to convert plugin-prefixed image
// paths (e.g. "tmdb://poster/abc.jpg") to HTTP URLs before caching.
func (s *MetadataService) SetImageResolver(r interface {
	ResolveImageURL(ctx context.Context, path string, variant string) string
}) {
	s.imageResolver = r
}

// resolveChainCached wraps ResolveChain with a short-lived in-memory cache.
// Provider chains are determined by admin configuration and change rarely, so
// caching avoids repeated DB queries when processing batches of files.
func (s *MetadataService) resolveChainCached(ctx context.Context, folderID int, contentLevel string) ([]Provider, error) {
	key := fmt.Sprintf("%d:%s", folderID, contentLevel)

	s.chainCacheMu.RLock()
	if entry, ok := s.chainCache[key]; ok && time.Now().Before(entry.expiresAt) {
		s.chainCacheMu.RUnlock()
		return entry.providers, nil
	}
	s.chainCacheMu.RUnlock()

	providers, err := ResolveChainWithChecker(ctx, folderID, contentLevel, s.chainRepo, s.pluginResolver, s.enabledChecker)
	if err != nil {
		return nil, err
	}

	s.chainCacheMu.Lock()
	s.chainCache[key] = chainCacheEntry{
		providers: providers,
		expiresAt: time.Now().Add(s.chainCacheTTL),
	}
	s.chainCacheMu.Unlock()

	return providers, nil
}

// InvalidateChainCache clears the cached provider chains, forcing the next
// resolution to re-query the database. Call this after admin chain edits.
func (s *MetadataService) InvalidateChainCache() {
	s.chainCacheMu.Lock()
	s.chainCache = make(map[string]chainCacheEntry)
	s.chainCacheMu.Unlock()
}

// Process runs the unified metadata pipeline with per-level chain resolution.
func (s *MetadataService) Process(ctx context.Context, req ProcessRequest) (*ProcessResult, error) {
	if s != nil && s.hooks.process != nil {
		return s.hooks.process(ctx, req)
	}

	var err error
	req, err = s.prepareProcessRequest(ctx, req)
	if err != nil {
		return nil, err
	}

	folderID := parseProcessFolderID(req.FolderID)
	languages, err := s.resolveProcessLanguages(ctx, req, folderID)
	if err != nil {
		return nil, err
	}

	resolveChain := func(contentLevel string) ([]Provider, error) {
		chain, err := s.resolveChainCached(ctx, folderID, contentLevel)
		if err != nil {
			return nil, fmt.Errorf("resolve %s provider chain: %w", contentLevel, err)
		}
		return chain, nil
	}

	// A folder-scoped manual refresh resolves exactly one language — the
	// library's setting. When that differs from the item's stamped canonical
	// language, adopt it so the base row is re-fetched in the library's
	// language instead of the fetch being shunted into localization tables
	// forever (issue #211). Adoption is decided here (not in mergeAndPersist)
	// so identify/initial-match and multi-language item-scoped refreshes are
	// never affected. Adoption also requires every library containing the item
	// to agree on the target language — otherwise two libraries with different
	// languages would flip the canonical row back and forth on each refresh.
	adoptTarget := ""
	if folderID > 0 && req.ContentID != "" && len(languages) == 1 && s.itemRepo != nil {
		if item, err := s.itemRepo.GetByID(ctx, req.ContentID); err == nil && item != nil {
			if adoptableFolderLanguage(req.Mode, item.DefaultMetadataLanguage, languages[0]) &&
				s.itemLibrariesAgreeOnLanguage(ctx, req.ContentID, languages[0]) {
				adoptTarget = languages[0]
			}
		}
	}

	var final ProcessResult
	for _, language := range languages {
		langReq := req
		langReq.Language = language
		langReq.AdoptLanguage = adoptTarget != "" && strings.EqualFold(language, adoptTarget)
		result, err := s.processInternal(ctx, langReq, resolveChain)
		if err != nil {
			return nil, err
		}
		if result == nil {
			continue
		}
		if result.ContentID != "" {
			final.ContentID = result.ContentID
			req.ContentID = result.ContentID
		}
		final.IsNew = final.IsNew || result.IsNew
		final.Updated = final.Updated || result.Updated
	}

	s.maybeAutoTranslate(ctx, folderID, final.ContentID)

	return &final, nil
}

// maybeAutoTranslate queues an AI translation for libraries that opted in,
// when the item's default metadata language differs from the library's. The
// translation service itself checks whether anything is actually missing, so
// this fires cheaply on every refresh. Runs in the background — a refresh
// never waits on (or fails because of) the translation seam.
func (s *MetadataService) maybeAutoTranslate(ctx context.Context, folderID int, contentID string) {
	if s.autoTranslator == nil || contentID == "" || folderID <= 0 || s.folderRepo == nil || s.itemRepo == nil {
		return
	}
	folder, err := s.folderRepo.GetByID(ctx, folderID)
	if err != nil || folder == nil || !folder.AutoTranslateMetadata {
		return
	}
	library := strings.TrimSpace(folder.MetadataLanguage)
	if library == "" {
		return
	}
	item, err := s.itemRepo.GetByID(ctx, contentID)
	if err != nil || item == nil {
		return
	}
	if strings.EqualFold(strings.TrimSpace(item.DefaultMetadataLanguage), library) {
		return
	}
	go s.autoTranslator.AutoEnqueue(context.WithoutCancel(ctx), contentID, library)
}

// adoptableFolderLanguage reports whether a folder-scoped refresh should
// adopt the folder's language as the item's new canonical metadata language.
// Only manual refreshes adopt: their MergeReplaceUnlocked policy actually
// rewrites title/overview, whereas a scheduled refresh merges fill-empty and
// would restamp the language without replacing the text — mislabeling the
// base row.
func adoptableFolderLanguage(mode RefreshMode, stampedLanguage, folderLanguage string) bool {
	if mode != ModeManualRefresh {
		return false
	}
	stamp := strings.TrimSpace(stampedLanguage)
	target := strings.TrimSpace(folderLanguage)
	return stamp != "" && target != "" && !strings.EqualFold(stamp, target)
}

// itemLibrariesAgreeOnLanguage reports whether every library containing the
// item resolves to the same effective metadata language as target. An item can
// live in several libraries (media_item_libraries); adopting the requesting
// library's language while another library is configured differently would
// rewrite the canonical base row to whichever library refreshed last, forever.
// Disagreement (or a lookup failure) skips adoption — a stable stamp beats
// flip-flopping. Unset library languages count as "en", the same default
// resolveFolderLanguage applies, so both sides of the comparison use the same
// effective-language rules.
func (s *MetadataService) itemLibrariesAgreeOnLanguage(ctx context.Context, contentID, target string) bool {
	if s.libraryRepo == nil {
		return false
	}
	languages, err := s.libraryRepo.GetDistinctMetadataLanguagesForItem(ctx, contentID)
	if err != nil {
		slog.Warn("metadata: failed to look up library languages for adoption gate; keeping current stamp",
			"content_id", contentID, "error", err)
		return false
	}
	for _, language := range languages {
		if !strings.EqualFold(strings.TrimSpace(language), strings.TrimSpace(target)) {
			return false
		}
	}
	return true
}

func parseProcessFolderID(raw string) int {
	folderID := 0
	if raw != "" {
		fmt.Sscanf(raw, "%d", &folderID)
	}
	return folderID
}

func (s *MetadataService) resolveProcessLanguages(ctx context.Context, req ProcessRequest, folderID int) ([]string, error) {
	if strings.TrimSpace(req.Language) != "" {
		return []string{strings.TrimSpace(req.Language)}, nil
	}
	if folderID > 0 {
		return []string{s.resolveFolderLanguage(ctx, folderID)}, nil
	}
	if req.ContentID != "" {
		defaultLanguage := ""
		if s.itemRepo != nil {
			if item, err := s.itemRepo.GetByID(ctx, req.ContentID); err == nil && strings.TrimSpace(item.DefaultMetadataLanguage) != "" {
				defaultLanguage = strings.TrimSpace(item.DefaultMetadataLanguage)
			}
		}
		var languages []string
		if s.libraryRepo != nil {
			langs, err := s.libraryRepo.GetDistinctMetadataLanguagesForItem(ctx, req.ContentID)
			if err != nil {
				return nil, err
			}
			languages = append(languages, langs...)
		}
		if defaultLanguage != "" {
			languages = prependUniqueString(languages, defaultLanguage)
		}
		languages = compactMetadataLanguages(languages)
		if len(languages) > 0 {
			return languages, nil
		}
	}
	return []string{"en"}, nil
}

func (s *MetadataService) resolveFolderLanguage(ctx context.Context, folderID int) string {
	if folderID <= 0 || s.folderRepo == nil {
		return "en"
	}
	folder, err := s.folderRepo.GetByID(ctx, folderID)
	if err != nil {
		slog.Warn("metadata: failed to look up folder language, defaulting to English",
			"folder_id", folderID, "error", err)
		return "en"
	}
	if strings.TrimSpace(folder.MetadataLanguage) == "" {
		return "en"
	}
	return strings.TrimSpace(folder.MetadataLanguage)
}

// resolveAllowedVideoKinds returns the union of trailer_kinds across the
// libraries containing the item, or just the scoped folder's when a folder id
// is provided. The union (most-permissive) mirrors the multi-library language
// posture. A nil return means "allow all": unknown scope or a transient
// lookup failure must never wipe stored trailers.
func (s *MetadataService) resolveAllowedVideoKinds(ctx context.Context, contentID string, folderID int) map[models.ExtraKind]bool {
	if s.folderRepo == nil {
		return nil
	}
	var folderIDs []int
	if folderID > 0 {
		folderIDs = []int{folderID}
	} else if s.libraryRepo != nil {
		ids, err := s.libraryRepo.GetFolderIDsForItem(ctx, contentID)
		if err != nil {
			slog.Warn("metadata: resolving item libraries for video kinds failed",
				"content_id", contentID, "error", err)
			return nil
		}
		folderIDs = ids
	}
	if len(folderIDs) == 0 {
		return nil
	}
	allowed := make(map[models.ExtraKind]bool)
	resolvedAny := false
	for _, id := range folderIDs {
		folder, err := s.folderRepo.GetByID(ctx, id)
		if err != nil || folder == nil {
			continue
		}
		resolvedAny = true
		for _, kind := range folder.TrailerKinds {
			allowed[models.ExtraKind(kind)] = true
		}
	}
	if !resolvedAny {
		return nil
	}
	return allowed
}

// filterVideosByKinds applies the per-library allow-list; a nil map allows
// everything, an empty map filters everything (remote videos disabled).
func filterVideosByKinds(videos []RemoteVideo, allowed map[models.ExtraKind]bool) []RemoteVideo {
	if allowed == nil || len(videos) == 0 {
		return videos
	}
	filtered := make([]RemoteVideo, 0, len(videos))
	for _, v := range videos {
		if allowed[v.Kind] {
			filtered = append(filtered, v)
		}
	}
	return filtered
}

// itemVideosFromRemote converts pipeline videos into item_videos rows with a
// stable display order: kinds in vocabulary order (trailers first), official
// before unofficial, provider order otherwise preserved.
func itemVideosFromRemote(contentID string, videos []RemoteVideo) []models.ItemVideo {
	rank := make(map[models.ExtraKind]int, len(models.AllExtraKinds))
	for i, k := range models.AllExtraKinds {
		rank[k] = i
	}
	ordered := append([]RemoteVideo(nil), videos...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ri, rj := rank[ordered[i].Kind], rank[ordered[j].Kind]; ri != rj {
			return ri < rj
		}
		return ordered[i].IsOfficial && !ordered[j].IsOfficial
	})

	rows := make([]models.ItemVideo, 0, len(ordered))
	for i, v := range ordered {
		var publishedAt *time.Time
		if v.PublishedAt != "" {
			if t, err := time.Parse(time.RFC3339, v.PublishedAt); err == nil {
				publishedAt = &t
			}
		}
		rows = append(rows, models.ItemVideo{
			ContentID:   contentID,
			Provider:    v.Provider,
			ProviderKey: v.ProviderKey,
			Kind:        v.Kind,
			Site:        v.Site,
			SiteKey:     v.SiteKey,
			Name:        v.Name,
			Language:    v.Language,
			IsOfficial:  v.IsOfficial,
			SizeHint:    v.SizeHint,
			PublishedAt: publishedAt,
			SortOrder:   i,
		})
	}
	return rows
}

func prependUniqueString(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if strings.EqualFold(strings.TrimSpace(existing), value) {
			return values
		}
	}
	return append([]string{value}, values...)
}

func compactMetadataLanguages(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, trimmed)
	}
	return result
}

func (s *MetadataService) localProviderContextForContent(ctx context.Context, contentID string, folderID int) localProviderContext {
	if s == nil || s.fileRepo == nil || contentID == "" {
		return localProviderContext{}
	}
	lister, ok := s.fileRepo.(metadataContentFileLister)
	if !ok {
		return localProviderContext{}
	}
	files, err := lister.GetByContentID(ctx, contentID)
	if err != nil || len(files) == 0 {
		return localProviderContext{}
	}
	if folderID > 0 {
		filtered := files[:0]
		for _, file := range files {
			if file != nil && file.MediaFolderID == folderID {
				filtered = append(filtered, file)
			}
		}
		files = filtered
		if len(files) == 0 {
			return localProviderContext{}
		}
	}

	groupFilePaths := make([]string, 0, len(files))
	representativeFilePath := ""
	observedRootPath := ""
	var representativeFile *models.MediaFile
	for _, file := range files {
		if file == nil {
			continue
		}
		if representativeFilePath == "" {
			representativeFilePath = file.FilePath
			representativeFile = file
		}
		groupFilePaths = append(groupFilePaths, file.FilePath)
	}
	if representativeFilePath == "" {
		return localProviderContext{}
	}
	if representativeFile != nil && representativeFile.ObservedRootPath != "" &&
		s.canUseObservedRootForDirectorySidecars(
			ctx,
			representativeFile.MediaFolderID,
			representativeFile.ObservedRootPath,
			representativeFile.GroupKeyVersion,
			representativeFile.ContentGroupKey,
		) {
		observedRootPath = representativeFile.ObservedRootPath
	}

	return localProviderContext{
		filePath:                  representativeFilePath,
		representativeFilePath:    representativeFilePath,
		observedRootPath:          observedRootPath,
		allGroupFilePaths:         compactUniqueFilePaths(groupFilePaths),
		primarySidecarSearchPaths: s.directorySidecarSearchPathsForFiles(ctx, files),
	}
}

func (s *MetadataService) directorySidecarSearchPathsForFiles(ctx context.Context, files []*models.MediaFile) []string {
	if len(files) == 0 {
		return nil
	}
	paths := make([]string, 0, len(files))
	for _, file := range files {
		if file == nil || file.ObservedRootPath == "" {
			continue
		}
		if !s.canUseObservedRootForDirectorySidecars(
			ctx,
			file.MediaFolderID,
			file.ObservedRootPath,
			file.GroupKeyVersion,
			file.ContentGroupKey,
		) {
			continue
		}
		paths = append(paths, file.ObservedRootPath)
	}
	return compactUniqueFilePaths(paths)
}

func (s *MetadataService) canUseObservedRootForDirectorySidecars(
	ctx context.Context,
	folderID int,
	observedRootPath string,
	groupKeyVersion int,
	contentGroupKey string,
) bool {
	cleanRoot := filepath.Clean(strings.TrimSpace(observedRootPath))
	if cleanRoot == "" || cleanRoot == "." {
		return false
	}
	if s == nil || s.observedLocationRepo == nil || folderID <= 0 {
		return true
	}
	location, err := s.observedLocationRepo.Get(ctx, folderID, cleanRoot)
	if err != nil {
		slog.Warn("metadata: observed location lookup failed",
			"folder_id", folderID,
			"observed_root_path", cleanRoot,
			"error", err,
		)
		return false
	}
	if location == nil || location.ContentGroupCount > 1 {
		return false
	}
	if groupKeyVersion != 0 && location.PrimaryGroupKeyVersion != 0 && location.PrimaryGroupKeyVersion != groupKeyVersion {
		return false
	}
	if contentGroupKey != "" && location.PrimaryContentGroupKey != "" && location.PrimaryContentGroupKey != contentGroupKey {
		return false
	}
	return true
}

func compactUniqueFilePaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		clean := filepath.Clean(strings.TrimSpace(path))
		if clean == "" || clean == "." {
			continue
		}
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	sort.Strings(out)
	return out
}

func (s *MetadataService) loadDurableProviderIDs(ctx context.Context, contentID string) (map[string]string, error) {
	if s == nil || s.providerIDRepo == nil || contentID == "" {
		return nil, nil
	}
	ids, err := s.providerIDRepo.GetByContentID(ctx, contentID)
	if err != nil {
		return nil, err
	}
	return providerIDMapFromRows(ids), nil
}

func (s *MetadataService) suppressRecordedStaleProviderIDs(
	ctx context.Context,
	contentID string,
	providerIDs map[string]string,
) error {
	if s == nil || s.staleIDRepo == nil || strings.TrimSpace(contentID) == "" || len(providerIDs) == 0 {
		return nil
	}

	staleIDs, err := s.staleIDRepo.GetByContentID(ctx, contentID)
	if err != nil {
		return fmt.Errorf("loading stale provider ids for %s: %w", contentID, err)
	}
	if len(staleIDs) == 0 {
		return nil
	}
	// Index the incoming map by normalized provider key so suppression cannot
	// be bypassed by casing or padding differences between the stored stale
	// row and the caller-supplied map keys (e.g. "TMDB" vs "tmdb").
	keysByProvider := make(map[string][]string, len(providerIDs))
	for key := range providerIDs {
		normalized := strings.ToLower(strings.TrimSpace(key))
		if normalized == "" {
			continue
		}
		keysByProvider[normalized] = append(keysByProvider[normalized], key)
	}
	for _, staleID := range staleIDs {
		if staleID == nil {
			continue
		}
		provider := strings.ToLower(strings.TrimSpace(staleID.Provider))
		if provider == "" {
			continue
		}
		staleValue := strings.TrimSpace(staleID.ProviderID)
		for _, key := range keysByProvider[provider] {
			if strings.TrimSpace(providerIDs[key]) != staleValue {
				continue
			}
			delete(providerIDs, key)
		}
	}
	return nil
}

// ProcessWithProviders runs the pipeline with explicit providers (for testing).
// All phases use the same chain regardless of content level.
func (s *MetadataService) ProcessWithProviders(ctx context.Context, req ProcessRequest, chain []Provider) (*ProcessResult, error) {
	var err error
	req, err = s.prepareProcessRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	return s.processInternal(ctx, req, func(string) ([]Provider, error) { return chain, nil })
}

func (s *MetadataService) prepareProcessRequest(ctx context.Context, req ProcessRequest) (ProcessRequest, error) {
	durableIDs, err := s.loadDurableProviderIDs(ctx, req.ContentID)
	if err != nil {
		return req, err
	}
	if len(durableIDs) == 0 {
		return req, nil
	}
	// Strip durable IDs already recorded as stale before merging them into
	// the request. Without this, a manual rematch (ModeIdentify) re-injects
	// the known-dead ID, the Phase-2 fetch 404s again, and the item is
	// re-recorded in stale_media_ids instead of leaving the stale list.
	// Only the injected set is filtered: IDs the caller supplied explicitly
	// in req.ProviderIDs stay untouched, so an admin deliberately
	// re-selecting a previously-stale ID still retries it.
	if err := s.suppressRecordedStaleProviderIDs(ctx, req.ContentID, durableIDs); err != nil {
		return req, err
	}
	if len(durableIDs) == 0 {
		return req, nil
	}
	if req.ProviderIDs == nil {
		req.ProviderIDs = make(map[string]string, len(durableIDs))
	}
	for key, value := range durableIDs {
		if _, exists := req.ProviderIDs[key]; !exists {
			req.ProviderIDs[key] = value
		}
	}
	return req, nil
}

// processInternal contains the core pipeline logic. The resolveChain function
// returns providers for a given content level, enabling per-level resolution.
func (s *MetadataService) processInternal(ctx context.Context, req ProcessRequest, resolveChain chainResolver) (*ProcessResult, error) {
	scopeFolderID := parseProcessFolderID(req.FolderID)

	// Determine content type from hints or existing item.
	contentType := ""
	if req.Hints != nil {
		contentType = req.Hints.Type
	}

	// Determine the item-level content level for phases 1-3.
	itemLevel := providerChainContentLevel(contentType)
	itemChain, err := resolveChain(itemLevel)
	if err != nil {
		return nil, err
	}

	// Phase 1: Search — find provider IDs.
	accumulatedIDs := make(map[string]string)
	maps.Copy(accumulatedIDs, req.ProviderIDs)

	// Track provider 404s as stale external IDs. This applies to initial match
	// as well so bad embedded folder/file IDs can be recorded and dropped
	// without surfacing as generic provider failures.
	var provider404s map[string]string
	if req.ContentID != "" {
		provider404s = make(map[string]string)
	}

	switch req.Mode {
	case ModeInitialMatch:
		if req.Hints == nil {
			return nil, fmt.Errorf("initial match requires hints")
		}
		// Seed external IDs from hints. Hints.ContentID is Silo's local
		// skeleton item ID, not a searchable provider ID.
		if req.Hints.FileHash != "" {
			accumulatedIDs["oshash"] = req.Hints.FileHash
		}
		if req.Hints.TmdbID != "" {
			accumulatedIDs["tmdb"] = req.Hints.TmdbID
		}
		if req.Hints.TvdbID != "" {
			accumulatedIDs["tvdb"] = req.Hints.TvdbID
		}
		if req.Hints.ImdbID != "" {
			accumulatedIDs["imdb"] = req.Hints.ImdbID
		}
		// Add filepath for local providers.
		if req.Hints.RepresentativeFilePath != "" {
			accumulatedIDs["_filepath"] = req.Hints.RepresentativeFilePath
		} else if req.Hints.FilePath != "" {
			accumulatedIDs["_filepath"] = req.Hints.FilePath
		}
		if err := s.suppressRecordedStaleProviderIDs(ctx, req.ContentID, accumulatedIDs); err != nil {
			return nil, err
		}

		// Run search providers and choose a decisive normalized winner instead of
		// letting the first non-empty result win.
		searchQuery := SearchQuery{
			Title:                     req.Hints.Title,
			Year:                      req.Hints.Year,
			ContentType:               contentType,
			ProviderIDs:               accumulatedIDs,
			Language:                  req.Language,
			FilePath:                  req.Hints.FilePath,
			RepresentativeFilePath:    req.Hints.RepresentativeFilePath,
			ObservedRootPath:          req.Hints.ObservedRootPath,
			AllGroupFilePaths:         append([]string(nil), req.Hints.AllGroupFilePaths...),
			PrimarySidecarSearchPaths: append([]string(nil), req.Hints.PrimarySidecarSearchPaths...),
		}
		searchQuery = suppressTitleYearFallbackForTrustedIDs(searchQuery)
		allResults := make([]SearchResult, 0)
		for _, p := range itemChain {
			sp, ok := p.(SearchProvider)
			if !ok {
				continue
			}
			results, err := sp.Search(ctx, searchQuery)
			if err != nil {
				if handleProvider404(provider404s, accumulatedIDs, p.Slug(), err,
					"title", req.Hints.Title,
					"year", req.Hints.Year,
				) {
					continue
				}
				slog.Warn("metadata: search provider error",
					"provider", p.Slug(), "error", err)
				continue
			}
			slog.Debug("metadata: provider search result",
				"provider", p.Slug(),
				"query_title", searchQuery.Title,
				"query_year", searchQuery.Year,
				"result_count", len(results),
			)
			for _, result := range results {
				if searchResultConflictsWithTrustedIDs(accumulatedIDs, result.ProviderIDs) {
					slog.Warn("metadata: skipping conflicting search result",
						"provider", p.Slug(),
						"title", req.Hints.Title,
						"year", req.Hints.Year,
						"hinted_ids", accumulatedIDs,
						"candidate_ids", result.ProviderIDs,
					)
					continue
				}
				allResults = append(allResults, result)
			}
		}

		candidates := NormalizeCandidates(allResults, contentType)
		slog.Debug("metadata: search candidates assembled",
			"query_title", searchQuery.Title,
			"query_year", searchQuery.Year,
			"raw_results", len(allResults),
			"candidates", len(candidates),
		)
		providerPriority := make([]string, 0, len(itemChain))
		for _, p := range itemChain {
			providerPriority = append(providerPriority, p.Slug())
		}
		if winner, ok := selectInitialMatchCandidate(req.Hints, candidates, providerPriority); ok && winner != nil {
			for k, v := range winner.ProviderIDs {
				if v != "" {
					accumulatedIDs[k] = v
				}
			}
		}

	case ModeIdentify:
		// Use user-provided IDs directly.
		maps.Copy(accumulatedIDs, req.ProviderIDs)
		if contentType == "" && req.ContentID != "" {
			if existing, err := s.itemRepo.GetByID(ctx, req.ContentID); err == nil {
				contentType = existing.Type
				itemLevel = providerChainContentLevel(contentType)
				itemChain, err = resolveChain(itemLevel)
				if err != nil {
					return nil, err
				}
			}
		}

	case ModeScheduledRefresh, ModeManualRefresh:
		// Load existing item's IDs from DB.
		if req.ContentID == "" {
			return nil, fmt.Errorf("refresh requires content_id")
		}
		existing, err := s.itemRepo.GetByID(ctx, req.ContentID)
		if err != nil {
			return nil, fmt.Errorf("loading existing item: %w", err)
		}
		if existing.TmdbID != "" {
			accumulatedIDs["tmdb"] = existing.TmdbID
		}
		if existing.TvdbID != "" {
			accumulatedIDs["tvdb"] = existing.TvdbID
		}
		if existing.ImdbID != "" {
			accumulatedIDs["imdb"] = existing.ImdbID
		}
		contentType = existing.Type
		if err := s.suppressRecordedStaleProviderIDs(ctx, req.ContentID, accumulatedIDs); err != nil {
			return nil, err
		}

		// Re-resolve itemChain now that contentType is known.
		itemLevel = providerChainContentLevel(contentType)
		itemChain, err = resolveChain(itemLevel)
		if err != nil {
			return nil, err
		}

		// Run search providers to refresh provider IDs before fetching full metadata.
		searchQuery := SearchQuery{
			Title:       existing.Title,
			Year:        existing.Year,
			ContentType: contentType,
			ProviderIDs: accumulatedIDs,
			Language:    req.Language,
		}
		localCtx := s.localProviderContextForContent(ctx, req.ContentID, scopeFolderID)
		searchQuery.FilePath = localCtx.filePath
		searchQuery.RepresentativeFilePath = localCtx.representativeFilePath
		searchQuery.ObservedRootPath = localCtx.observedRootPath
		searchQuery.AllGroupFilePaths = append([]string(nil), localCtx.allGroupFilePaths...)
		searchQuery.PrimarySidecarSearchPaths = append([]string(nil), localCtx.primarySidecarSearchPaths...)
		searchQuery = suppressTitleYearFallbackForTrustedIDs(searchQuery)
		allResults := make([]SearchResult, 0)
		for _, p := range itemChain {
			sp, ok := p.(SearchProvider)
			if !ok {
				continue
			}
			results, searchErr := sp.Search(ctx, searchQuery)
			if searchErr != nil {
				if handleProvider404(provider404s, accumulatedIDs, p.Slug(), searchErr,
					"content_id", req.ContentID,
				) {
					continue
				}
				slog.Warn("metadata: refresh search error",
					"provider", p.Slug(), "error", searchErr)
				continue
			}
			for _, result := range results {
				if searchResultConflictsWithTrustedIDs(accumulatedIDs, result.ProviderIDs) {
					continue
				}
				allResults = append(allResults, result)
			}
		}
		candidates := NormalizeCandidates(allResults, contentType)
		if winner, ok := selectRefreshMatchCandidate(existing, candidates); ok && winner != nil {
			for k, v := range winner.ProviderIDs {
				if v != "" {
					accumulatedIDs[k] = v
				}
			}
		}
	}
	if req.Mode != ModeIdentify {
		if err := s.suppressRecordedStaleProviderIDs(ctx, req.ContentID, accumulatedIDs); err != nil {
			return nil, err
		}
	}

	// Phase 2: Metadata — all MetadataProviders run, results merge into accumulator.
	accumulator := &MetadataResult{ProviderIDs: copyMap(accumulatedIDs)}
	filePath := ""
	representativeFilePath := ""
	observedRootPath := ""
	allGroupFilePaths := []string{}
	primarySidecarSearchPaths := []string{}
	groupTitle := ""
	groupYear := 0
	if req.Hints != nil {
		filePath = req.Hints.FilePath
		representativeFilePath = req.Hints.RepresentativeFilePath
		observedRootPath = req.Hints.ObservedRootPath
		allGroupFilePaths = req.Hints.AllGroupFilePaths
		primarySidecarSearchPaths = req.Hints.PrimarySidecarSearchPaths
		groupTitle = req.Hints.Title
		groupYear = req.Hints.Year
	} else if req.ContentID != "" {
		localCtx := s.localProviderContextForContent(ctx, req.ContentID, scopeFolderID)
		filePath = localCtx.filePath
		representativeFilePath = localCtx.representativeFilePath
		observedRootPath = localCtx.observedRootPath
		allGroupFilePaths = localCtx.allGroupFilePaths
		primarySidecarSearchPaths = localCtx.primarySidecarSearchPaths
	}

	for _, p := range itemChain {
		mp, ok := p.(MetadataProvider)
		if !ok {
			continue
		}
		result, err := mp.GetMetadata(ctx, MetadataRequest{
			ProviderIDs:               accumulatedIDs,
			ContentType:               contentType,
			Language:                  req.Language,
			FilePath:                  filePath,
			RepresentativeFilePath:    representativeFilePath,
			ObservedRootPath:          observedRootPath,
			AllGroupFilePaths:         allGroupFilePaths,
			PrimarySidecarSearchPaths: primarySidecarSearchPaths,
			GroupTitle:                groupTitle,
			GroupYear:                 groupYear,
		})
		if err != nil {
			if handleProvider404(provider404s, accumulatedIDs, p.Slug(), err,
				"content_id", req.ContentID,
			) {
				continue
			}
			slog.Warn("metadata: provider error",
				"provider", p.Slug(), "error", err)
			continue
		}
		if result == nil || !result.HasMetadata {
			continue
		}
		// Bootstrap: feed new IDs to subsequent providers.
		mergeProviderIDs(accumulator, result)
		accumulatedIDs = accumulator.ProviderIDs
		// Merge fields into accumulator (FillEmpty — first provider wins).
		MergeMetadata(result, accumulator, nil, MergeFillEmpty)
	}
	// Phase 3: Images — all ImageProviders run, collect all available images.
	var allImages []RemoteImage
	for _, p := range itemChain {
		ip, ok := p.(ImageProvider)
		if !ok {
			continue
		}
		images, err := ip.GetImages(ctx, ImageRequest{
			ProviderIDs: accumulatedIDs,
			ContentType: contentType,
			Language:    req.Language,
		})
		if err != nil {
			if handleProvider404(provider404s, accumulatedIDs, p.Slug(), err,
				"content_id", req.ContentID,
			) {
				continue
			}
			slog.Warn("metadata: image provider error",
				"provider", p.Slug(), "error", err)
			continue
		}
		allImages = append(allImages, images...)
	}

	// Phase 4a: Seasons — resolve with "season" content level.
	var allSeasons []SeasonResult
	var allEpisodes []EpisodeResult
	if contentType == "series" {
		seasonChain, err := resolveChain("season")
		if err != nil {
			return nil, err
		}
		seasonResults := make(map[int]*SeasonResult)
		for _, p := range seasonChain {
			ep, ok := p.(EpisodeProvider)
			if !ok {
				continue
			}
			seasons, err := ep.GetSeasons(ctx, SeasonsRequest{
				ProviderIDs: accumulatedIDs,
				ContentType: contentType,
				Language:    req.Language,
			})
			if err != nil {
				// Pass nil for provider404s so this refresh can drop the
				// provider from the in-memory merge without recording a durable
				// stale item ID from the season chain.
				if handleProvider404(nil, accumulatedIDs, p.Slug(), err,
					"content_id", req.ContentID,
					"season", 0,
				) {
					continue
				}
				slog.Warn("metadata: season provider error",
					"provider", p.Slug(), "error", err)
				continue
			}
			accumulateSeasonResults(seasonResults, seasons)
		}
		allSeasons = flattenSeasonResults(seasonResults)

		// Phase 4b: Episodes — resolve with "episode" content level.
		if len(allSeasons) > 0 {
			episodeChain, err := resolveChain("episode")
			if err != nil {
				return nil, err
			}
			episodeResults := make(map[episodeResultKey]*EpisodeResult)
			for _, season := range allSeasons {
				for _, p := range episodeChain {
					ep, ok := p.(EpisodeProvider)
					if !ok {
						continue
					}
					episodes, err := ep.GetEpisodes(ctx, EpisodesRequest{
						ProviderIDs:  accumulatedIDs,
						SeasonNumber: season.SeasonNumber,
						Language:     req.Language,
					})
					if err != nil {
						if handleChildProvider404(p.Slug(), accumulatedIDs, err,
							"content_id", req.ContentID,
							"season", season.SeasonNumber,
						) {
							continue
						}
						slog.Warn("metadata: episode provider error",
							"provider", p.Slug(), "season", season.SeasonNumber, "error", err)
						continue
					}
					accumulateEpisodeResults(episodeResults, episodes)
				}
			}
			allEpisodes = flattenEpisodeResults(episodeResults)
		}
	}

	// Phase 5: Merge & Persist.
	if !accumulator.HasMetadata && accumulator.Title == "" {
		// Record stale IDs for providers that returned 404.
		if s.staleIDRepo != nil && req.ContentID != "" && provider404s != nil {
			for slug, providerID := range provider404s {
				if providerID != "" {
					if err := s.staleIDRepo.Upsert(ctx, req.ContentID, slug, providerID); err != nil {
						slog.Warn("metadata: failed to record stale ID",
							"content_id", req.ContentID, "provider", slug, "provider_id", providerID, "error", err)
					}
				}
			}
		}
		return &ProcessResult{Updated: false}, nil
	}

	result, err := s.mergeAndPersist(ctx, req, accumulator, allImages, allSeasons, allEpisodes, contentType)
	if err != nil {
		return nil, err
	}

	// Refresh stale ID records on successful refresh: clear anything resolved,
	// then keep only the providers that still 404ed during this run.
	// Stale-ID follow-up targets the canonical content ID the item was
	// persisted/merged into. When mergeAndPersist canonicalizes this item into
	// an existing one, req.ContentID is the now-deleted source: clearing or
	// recording stale IDs against it would touch nothing (or FK-violate), so
	// the still-404ing providers must be recorded on result.ContentID instead.
	// provider404s is only allocated when req.ContentID was set (the refresh
	// targeted a known item). Guarding on it preserves the original gating so a
	// content-id-less refresh that canonicalizes into an existing item does not
	// wipe that item's stale rows without re-recording any.
	followUpContentID := refreshFollowUpContentID(req.ContentID, result)
	if s.staleIDRepo != nil && followUpContentID != "" && provider404s != nil {
		if delErr := s.staleIDRepo.DeleteByContentID(ctx, followUpContentID); delErr != nil {
			slog.Warn("metadata: failed to clear stale IDs after refresh",
				"content_id", followUpContentID, "error", delErr)
		}
		for slug, providerID := range provider404s {
			if providerID == "" {
				continue
			}
			if upsertErr := s.staleIDRepo.Upsert(ctx, followUpContentID, slug, providerID); upsertErr != nil {
				slog.Warn("metadata: failed to persist stale provider ID after partial refresh",
					"content_id", followUpContentID,
					"provider", slug,
					"provider_id", providerID,
					"error", upsertErr)
			}
		}
	}
	if result != nil && strings.TrimSpace(result.ContentID) != "" {
		if syncErr := s.syncRefreshDebtForItem(ctx, result.ContentID); syncErr != nil {
			slog.Warn("metadata: failed to sync refresh debt after successful metadata refresh",
				"content_id", result.ContentID,
				"error", syncErr)
		}
	}

	return result, nil
}

// refreshFollowUpContentID returns the content ID that a completed refresh's
// follow-up writes (stale-ID clear/record, debt sync) should target: the
// canonical ID the item was persisted or merged into when available, falling
// back to the requested ID. mergeAndPersist can canonicalize an item into an
// existing one, deleting the requested ID, so follow-up writes keyed on
// req.ContentID would miss the surviving item.
func refreshFollowUpContentID(reqContentID string, result *ProcessResult) string {
	if result != nil && strings.TrimSpace(result.ContentID) != "" {
		return result.ContentID
	}
	return reqContentID
}

// mergeAndPersist handles the final merge into existing item and DB persistence.
func (s *MetadataService) mergeAndPersist(
	ctx context.Context,
	req ProcessRequest,
	accumulator *MetadataResult,
	images []RemoteImage,
	seasons []SeasonResult,
	episodes []EpisodeResult,
	contentType string,
) (*ProcessResult, error) {
	// Determine merge mode.
	var mergeMode MergeMode
	switch req.Mode {
	case ModeInitialMatch, ModeScheduledRefresh:
		mergeMode = MergeFillEmpty
	case ModeManualRefresh, ModeIdentify:
		mergeMode = MergeReplaceUnlocked
	}

	isNew := req.ContentID == "" && req.Mode == ModeInitialMatch
	contentID := req.ContentID
	var existingItem *models.MediaItem

	if isNew {
		// Check for existing item by provider IDs (dedup).
		unlockDedup := s.lockDedupKey(dedupKeyFromProviderIDs(contentType, accumulator.ProviderIDs))
		existing, _ := s.findExistingByProviderIDs(ctx, accumulator.ProviderIDs, contentType, contentID)
		if existing != nil && isConfirmedOwnershipStatus(existing.Status) {
			contentID = existing.ContentID
			isNew = false
			existingItem = existing
		}
		unlockDedup()
	}

	// Hold the provider-ID dedup lock through canonicalization and durable-ID
	// persistence so concurrent versions with the same provider IDs cannot both
	// slip past rebind and race on the unique provider constraint.
	unlockProviderDedup := func() {}
	if key := dedupKeyFromProviderIDs(contentType, accumulator.ProviderIDs); key != "" {
		unlockProviderDedup = s.lockDedupKey(key)
	}
	providerDedupReleased := false
	defer func() {
		if !providerDedupReleased {
			unlockProviderDedup()
		}
	}()

	// Load locked fields and durable provider IDs if refreshing an existing item.
	var locked []MetadataField
	var durableIDs map[string]string
	if !isNew && contentID != "" {
		existing, err := s.itemRepo.GetByID(ctx, contentID)
		if err == nil {
			existingItem = existing
			locked = intSliceToFields(existing.LockedFields)
		}
		durableIDs, err = s.loadDurableProviderIDs(ctx, contentID)
		if err != nil {
			return nil, err
		}
	}
	if !isNew && contentID != "" && existingItem != nil && (isProvisionalOwnershipStatus(existingItem.Status) || len(durableIDs) == 0) {
		reboundTo, err := s.rebindItemByProviderIDsLocked(ctx, contentID, accumulator.ProviderIDs, contentType, len(durableIDs) == 0)
		if err != nil {
			return nil, err
		}
		if reboundTo != "" {
			contentID = reboundTo
			existingItem, err = s.itemRepo.GetByID(ctx, contentID)
			if err != nil {
				return nil, fmt.Errorf("loading rebound item: %w", err)
			}
			locked = intSliceToFields(existingItem.LockedFields)
			durableIDs, err = s.loadDurableProviderIDs(ctx, contentID)
			if err != nil {
				return nil, err
			}
		}
	}

	// Promote a local: skeleton to its deterministic provider-anchored id now
	// that the match supplied provider IDs (the untagged-then-matched re-ID).
	// Runs under the provider-dedup lock acquired above, so the target id cannot
	// be claimed underneath us; movies and first-match series move a handful of
	// rows. Placed before the durableIDs merge below so the canonical row's
	// provider IDs are folded into the accumulator. See canonicalizeLocalContentID.
	if !isNew && contentid.IsLocal(contentID) {
		canonical, err := s.canonicalizeLocalContentID(
			ctx, contentID, providerIDsStruct(accumulator.ProviderIDs), contentType)
		if err != nil {
			return nil, fmt.Errorf("canonicalize local content id: %w", err)
		}
		if canonical != contentID {
			contentID = canonical
			existingItem, err = s.itemRepo.GetByID(ctx, contentID)
			if err != nil {
				return nil, fmt.Errorf("loading canonicalized item: %w", err)
			}
			locked = intSliceToFields(existingItem.LockedFields)
			durableIDs, err = s.loadDurableProviderIDs(ctx, contentID)
			if err != nil {
				return nil, err
			}
		}
	}

	if len(durableIDs) > 0 {
		if accumulator.ProviderIDs == nil {
			accumulator.ProviderIDs = make(map[string]string, len(durableIDs))
		}
		for key, value := range durableIDs {
			if _, exists := accumulator.ProviderIDs[key]; !exists {
				accumulator.ProviderIDs[key] = value
			}
		}
	}

	// Adoption restamps default_metadata_language to req.Language while the
	// merge below rewrites the language-bearing text (title, overview). Field
	// locks win over adoption: if BOTH are locked, nothing would actually be
	// rewritten, and restamping would permanently mislabel the old-language
	// text as the new language (the quick-refresh mismatch predicate would
	// never flag the item again). Fall back to the non-adopting behavior in
	// that case — keep the existing stamp and let the fetch route to the
	// localization tables like any other non-canonical refresh.
	adoptLanguage := req.AdoptLanguage
	if adoptLanguage && isFieldLocked(locked, FieldName) && isFieldLocked(locked, FieldOverview) {
		adoptLanguage = false
	}

	canonicalLanguage := strings.TrimSpace(req.Language)
	if !adoptLanguage && existingItem != nil && strings.TrimSpace(existingItem.DefaultMetadataLanguage) != "" {
		canonicalLanguage = strings.TrimSpace(existingItem.DefaultMetadataLanguage)
	}
	if canonicalLanguage == "" {
		canonicalLanguage = "en"
	}
	isCanonicalWrite := existingItem == nil || strings.EqualFold(req.Language, canonicalLanguage)

	if existingItem != nil {
		existingResult := itemToMetadataResult(existingItem)
		if req.Mode == ModeInitialMatch && isSkeletonLikeStatus(existingItem.Status) {
			existingResult.Title = ""
			existingResult.SortTitle = ""
			existingResult.Year = 0
		}
		if isCanonicalWrite {
			MergeMetadata(accumulator, existingResult, locked, mergeMode)
		} else {
			MergeGlobalMetadata(accumulator, existingResult, locked, mergeMode)
		}
		accumulator = existingResult
	}

	ApplyDefaultSortTitle(accumulator, isFieldLocked(locked, FieldName))

	// Build the item for persistence.
	now := time.Now()
	item := metadataResultToItem(accumulator, contentType)
	item.DefaultMetadataLanguage = canonicalLanguage
	item.MatchedAt = &now
	item.LastRefreshed = &now
	item.RefreshFailures = 0
	item.Status = "matched"

	// Apply best images.
	if isCanonicalWrite {
		applyBestImages(item, images, mergeMode, req.Language)
		item.PosterThumbhash = mergedImageThumbhash(
			existingImagePath(existingItem, ImagePoster),
			existingImageThumbhash(existingItem, ImagePoster),
			item.PosterPath,
			"",
		)
		item.BackdropThumbhash = mergedImageThumbhash(
			existingImagePath(existingItem, ImageBackdrop),
			existingImageThumbhash(existingItem, ImageBackdrop),
			item.BackdropPath,
			"",
		)
	}

	if isCanonicalWrite {
		prepareItemImagesForQueue(item, existingItem)
	}

	if isNew && contentID == "" {
		// A confirmed provider match carries provider IDs, so this derives a
		// deterministic movie:/series: id; it only falls back when the match
		// somehow lacks all anchors.
		var genErr error
		contentID, genErr = deriveLogicalContentID(
			item.Type,
			contentid.ProviderIDs{Tmdb: item.TmdbID, Imdb: item.ImdbID, Tvdb: item.TvdbID},
			"",
		)
		if genErr != nil {
			return nil, fmt.Errorf("generate content id: %w", genErr)
		}
	}
	item.ContentID = contentID
	persistedContentID, err := s.persistItemAndProviderIDs(
		ctx,
		item,
		accumulator.ProviderIDs,
		isNew,
		existingItem,
		len(durableIDs) == 0,
		contentType,
	)
	if err != nil {
		return nil, err
	}
	contentID = persistedContentID
	item.ContentID = contentID
	unlockProviderDedup()
	providerDedupReleased = true
	if isCanonicalWrite {
		s.enqueueItemImages(ctx, item, accumulator.ProviderIDs, images)
	}

	if !isCanonicalWrite && s.itemLocalizationRepo != nil {
		existingLoc, err := s.itemLocalizationRepo.Get(ctx, contentID, req.Language)
		if err != nil {
			return nil, fmt.Errorf("loading item localization: %w", err)
		}
		loc := buildItemLocalizationRecord(
			existingLoc, contentID, req.Language, contentType, accumulator, images, mergeMode, req.Language,
			isFieldLocked(locked, FieldName),
		)
		if err := s.itemLocalizationRepo.Upsert(ctx, loc); err != nil {
			return nil, fmt.Errorf("upserting item localization: %w", err)
		}
		s.enqueueItemLocalizationImages(ctx, item, loc, accumulator.ProviderIDs, images)
	}

	// Persist people to the unified people table.
	if len(item.People) > 0 && s.personRepo != nil {
		persons := make([]models.Person, len(item.People))
		for i := range item.People {
			persons[i] = item.People[i].Person
		}
		personIDs, err := s.personRepo.BatchFindOrCreate(ctx, persons)
		if err != nil {
			slog.Warn("metadata: batch find/create people failed", "error", err)
		} else {
			for i := range item.People {
				item.People[i].Person.ID = personIDs[i]
			}
		}
		// Filter out entries where FindOrCreate failed (Person.ID stayed 0).
		valid := item.People[:0]
		for _, p := range item.People {
			if p.Person.ID != 0 {
				valid = append(valid, p)
			}
		}
		if err := s.itemRepo.ReplacePeople(ctx, contentID, valid); err != nil {
			slog.Warn("metadata: failed to replace people", "content_id", contentID, "error", err)
		}
	}

	// Persist remote videos (trailers etc.), filtered by the library's
	// trailer_kinds allow-list. Replace-unlocked refreshes replace the whole
	// set — including clearing it — so narrowing the allow-list converges on
	// the next refresh; fill-empty refreshes only write when providers
	// returned something, so a transient provider failure cannot wipe data.
	if isCanonicalWrite && s.videoRepo != nil && !isFieldLocked(locked, FieldVideos) {
		allowed := s.resolveAllowedVideoKinds(ctx, contentID, parseProcessFolderID(req.FolderID))
		filtered := filterVideosByKinds(accumulator.Videos, allowed)
		if len(filtered) > 0 || mergeMode == MergeReplaceUnlocked {
			if err := s.videoRepo.ReplaceByContentID(ctx, contentID, itemVideosFromRemote(contentID, filtered)); err != nil {
				slog.Warn("metadata: failed to replace item videos", "content_id", contentID, "error", err)
			}
		}
	}

	// Ensure library membership exists after successful enrichment. This call
	// site runs inside mergeAndPersist, so the item is known to be matched at
	// this point; it uses the matched-only upsert as a secondary confirmation.
	if req.Mode == ModeInitialMatch && req.FolderID != "" {
		folderID := 0
		fmt.Sscanf(req.FolderID, "%d", &folderID)
		if err := s.upsertLibraryMembershipIfMatched(ctx, contentID, folderID); err != nil {
			s.logLibraryMembershipError("ensuring matched library membership", contentID, folderID, err)
		}
	}

	// Persist seasons and episodes for series.
	if contentType == "series" {
		if len(seasons) > 0 {
			s.persistSeasonsAndEpisodes(ctx, item, accumulator.ProviderIDs, canonicalLanguage, req.Language, seasons, episodes, mergeMode)
		} else if err := s.SynthesizeFallbackEpisodes(ctx, contentID); err != nil {
			slog.Warn("metadata: failed to synthesize fallback series structure",
				"content_id", contentID, "error", err)
		}
	}

	return &ProcessResult{
		ContentID: contentID,
		IsNew:     isNew,
		Updated:   true,
	}, nil
}

func (s *MetadataService) persistItemAndProviderIDs(
	ctx context.Context,
	item *models.MediaItem,
	providerIDs map[string]string,
	isNew bool,
	existingItem *models.MediaItem,
	missingDurableIDs bool,
	contentType string,
) (string, error) {
	if item == nil || strings.TrimSpace(item.ContentID) == "" {
		return "", fmt.Errorf("content_id is required")
	}
	if itemRepo, ok := s.itemRepo.(*catalog.ItemRepository); ok && s.dbPool != nil {
		if providerRepo, ok := s.providerIDRepo.(*catalog.ProviderIDRepository); ok && providerRepo != nil {
			return s.persistItemAndProviderIDsTx(ctx, itemRepo, providerRepo, item, providerIDs, isNew, existingItem, missingDurableIDs, contentType)
		}
	}
	return s.persistItemAndProviderIDsLegacy(ctx, item, providerIDs, isNew, existingItem, missingDurableIDs, contentType)
}

func (s *MetadataService) persistItemAndProviderIDsTx(
	ctx context.Context,
	itemRepo *catalog.ItemRepository,
	providerRepo *catalog.ProviderIDRepository,
	item *models.MediaItem,
	providerIDs map[string]string,
	isNew bool,
	existingItem *models.MediaItem,
	missingDurableIDs bool,
	contentType string,
) (string, error) {
	contentID := item.ContentID
	err := s.persistItemAndProviderIDsOnceTx(ctx, itemRepo, providerRepo, item, providerIDs)
	if err == nil {
		return contentID, nil
	}
	if !isProviderIDUniqueConflict(err) {
		return "", fmt.Errorf("persisting provider IDs for %s: %w", contentID, err)
	}

	reboundTo, recoverErr := s.recoverProviderIDConflict(ctx, contentID, providerIDs, contentType, true)
	if recoverErr != nil {
		return "", fmt.Errorf("recovering provider ID conflict for %s: %w", contentID, recoverErr)
	}
	if reboundTo == "" {
		return "", fmt.Errorf("persisting provider IDs for %s: %w", contentID, err)
	}
	item.ContentID = reboundTo
	if retryErr := s.persistItemAndProviderIDsOnceTx(ctx, itemRepo, providerRepo, item, providerIDs); retryErr != nil {
		return "", fmt.Errorf("persisting provider IDs after conflict recovery for %s: %w", reboundTo, retryErr)
	}
	return reboundTo, nil
}

func (s *MetadataService) persistItemAndProviderIDsOnceTx(
	ctx context.Context,
	itemRepo *catalog.ItemRepository,
	providerRepo *catalog.ProviderIDRepository,
	item *models.MediaItem,
	providerIDs map[string]string,
) error {
	tx, err := s.dbPool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin metadata item/provider transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := itemRepo.UpsertTx(ctx, tx, item); err != nil {
		return fmt.Errorf("upserting item: %w", err)
	}
	if providerRepo != nil {
		if err := providerRepo.ReplaceByContentIDTx(ctx, tx, item.ContentID, item.Type, providerIDs); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit metadata item/provider transaction: %w", err)
	}
	return nil
}

func (s *MetadataService) persistItemAndProviderIDsLegacy(
	ctx context.Context,
	item *models.MediaItem,
	providerIDs map[string]string,
	isNew bool,
	existingItem *models.MediaItem,
	missingDurableIDs bool,
	contentType string,
) (string, error) {
	contentID := item.ContentID
	if err := s.itemRepo.Upsert(ctx, item); err != nil {
		if isNew {
			return "", fmt.Errorf("creating item: %w", err)
		}
		return "", fmt.Errorf("updating item: %w", err)
	}
	if s.providerIDRepo == nil {
		return contentID, nil
	}
	if err := s.providerIDRepo.ReplaceByContentID(ctx, contentID, providerIDs); err != nil {
		if isProviderIDUniqueConflict(err) {
			reboundTo, reboundErr := s.recoverProviderIDConflict(ctx, contentID, providerIDs, contentType, true)
			if reboundErr != nil {
				return "", fmt.Errorf("recovering provider ID conflict for %s: %w", contentID, reboundErr)
			}
			if reboundTo == "" {
				return "", fmt.Errorf("persisting provider IDs for %s: %w", contentID, err)
			}
			item.ContentID = reboundTo
			if err := s.itemRepo.Upsert(ctx, item); err != nil {
				return "", fmt.Errorf("updating rebound item after provider conflict: %w", err)
			}
			if err := s.providerIDRepo.ReplaceByContentID(ctx, reboundTo, providerIDs); err != nil {
				return "", fmt.Errorf("persisting provider IDs after conflict recovery for %s: %w", reboundTo, err)
			}
			return reboundTo, nil
		}
		return "", fmt.Errorf("persisting provider IDs for %s: %w", contentID, err)
	}
	return contentID, nil
}

// RefreshItem re-fetches metadata for a single content item.
func (s *MetadataService) RefreshItem(ctx context.Context, contentID string) error {
	return s.refreshItemTarget(ctx, contentID, 0, ModeManualRefresh, true)
}

func (s *MetadataService) refreshItemTarget(ctx context.Context, contentID string, folderID int, mode RefreshMode, incrementDebtAttempt bool) error {
	result, err := s.Process(ctx, ProcessRequest{
		ContentID: contentID,
		FolderID:  formatFolderID(folderID),
		Mode:      mode,
	})
	if err != nil {
		s.recordRefreshFailure(ctx, contentID, err, incrementDebtAttempt)
		return err
	}
	if result == nil || !result.Updated {
		s.recordRefreshFailure(ctx, contentID, ErrMetadataNotFound, incrementDebtAttempt)
		return ErrMetadataNotFound
	}
	return nil
}

// RefreshScheduledItem re-fetches metadata for a single content item using the
// background refresh merge policy.
func (s *MetadataService) RefreshScheduledItem(ctx context.Context, contentID string) error {
	return s.RefreshScheduledTarget(ctx, RefreshTargetItem, contentID)
}

// RefreshScheduledTarget re-fetches metadata for a queued item, season, or
// episode target using the background refresh merge policy.
func (s *MetadataService) RefreshScheduledTarget(ctx context.Context, targetType, contentID string) error {
	return s.refreshTarget(ctx, targetType, contentID, 0, ModeScheduledRefresh, false)
}

// RefreshItemForLibrary re-fetches metadata for an item using a specific
// library's provider chain and metadata language preferences.
func (s *MetadataService) RefreshItemForLibrary(ctx context.Context, contentID string, folderID int) error {
	return s.RefreshTargetForLibrary(ctx, RefreshTargetItem, contentID, folderID)
}

// RefreshTargetForLibrary re-fetches metadata for a specific target using a
// specific library's provider chain and metadata language preferences.
func (s *MetadataService) RefreshTargetForLibrary(ctx context.Context, targetType, contentID string, folderID int) error {
	return s.refreshTarget(ctx, targetType, contentID, folderID, ModeManualRefresh, true)
}

func (s *MetadataService) refreshTarget(ctx context.Context, targetType, contentID string, folderID int, mode RefreshMode, incrementDebtAttempt bool) error {
	targetType = NormalizeRefreshTargetType(targetType)
	if targetType == "" {
		return fmt.Errorf("unsupported metadata refresh target type")
	}
	switch targetType {
	case RefreshTargetItem:
		return s.refreshItemTarget(ctx, contentID, folderID, mode, incrementDebtAttempt)
	case RefreshTargetSeason:
		err := s.refreshSeasonTarget(ctx, contentID, folderID, mode)
		if err != nil {
			s.recordRefreshTargetFailure(ctx, targetType, contentID, err, incrementDebtAttempt)
			return err
		}
		return s.syncRefreshDebtForTarget(ctx, targetType, contentID)
	case RefreshTargetEpisode:
		err := s.refreshEpisodeTarget(ctx, contentID, folderID, mode)
		if err != nil {
			s.recordRefreshTargetFailure(ctx, targetType, contentID, err, incrementDebtAttempt)
			return err
		}
		return s.syncRefreshDebtForTarget(ctx, targetType, contentID)
	default:
		return fmt.Errorf("unsupported metadata refresh target type %q", targetType)
	}
}

// RequestStaleMetadataRefresh nudges a stale target into the durable refresh
// queue and starts a detached refresh when the target is due. Provider work is
// never done inline on the caller's request path.
func (s *MetadataService) RequestStaleMetadataRefresh(ctx context.Context, targetType, contentID string) error {
	if s == nil || s.refreshDebtRepo == nil {
		return nil
	}
	targetType = NormalizeRefreshTargetType(targetType)
	contentID = strings.TrimSpace(contentID)
	if targetType == "" || contentID == "" {
		return nil
	}
	now := time.Now().UTC()
	reasonMask := RefreshDebtReasonEpisodeIncomplete
	if err := s.refreshDebtRepo.RequestDue(
		ctx,
		targetType,
		contentID,
		refreshDebtPriority(reasonMask),
		reasonMask,
		now,
		metadataRefreshNudgeCooldown,
	); err != nil {
		return err
	}
	due, err := s.refreshDebtTargetIsDue(ctx, targetType, contentID, now)
	if err != nil {
		return err
	}
	if due {
		s.startOnDemandMetadataRefresh(targetType, contentID)
	}
	return nil
}

func (s *MetadataService) refreshDebtTargetIsDue(ctx context.Context, targetType, contentID string, now time.Time) (bool, error) {
	if s == nil || s.refreshDebtRepo == nil {
		return false, nil
	}
	debt, err := s.refreshDebtRepo.GetTarget(ctx, targetType, contentID)
	if err != nil {
		if errors.Is(err, ErrRefreshDebtNotFound) {
			return false, nil
		}
		return false, err
	}
	if debt == nil {
		return false, nil
	}
	if debt.LeaseExpiresAt != nil && !debt.LeaseExpiresAt.Before(now) {
		return false, nil
	}
	return !debt.NextRefreshAt.After(now), nil
}

func (s *MetadataService) startOnDemandMetadataRefresh(targetType, contentID string) {
	if !s.claimOnDemandMetadataRefresh(targetType, contentID) {
		return
	}
	go func() {
		defer s.releaseOnDemandMetadataRefresh(targetType, contentID)
		ctx, cancel := context.WithTimeout(context.Background(), metadataOnDemandRefreshTimeout)
		defer cancel()
		slog.Info("metadata: starting on-demand stale refresh",
			"target_type", targetType,
			"content_id", contentID)
		if err := s.refreshTarget(ctx, targetType, contentID, 0, ModeScheduledRefresh, false); err != nil {
			slog.Warn("metadata: on-demand stale refresh failed",
				"target_type", targetType,
				"content_id", contentID,
				"error", err)
			return
		}
		slog.Info("metadata: completed on-demand stale refresh",
			"target_type", targetType,
			"content_id", contentID)
	}()
}

func (s *MetadataService) claimOnDemandMetadataRefresh(targetType, contentID string) bool {
	if s == nil {
		return false
	}
	key := refreshTargetKey(targetType, contentID)
	if key == "" {
		return false
	}
	s.onDemandRefresh.mu.Lock()
	defer s.onDemandRefresh.mu.Unlock()
	if s.onDemandRefresh.running == nil {
		s.onDemandRefresh.running = make(map[string]struct{})
	}
	if _, ok := s.onDemandRefresh.running[key]; ok {
		return false
	}
	s.onDemandRefresh.running[key] = struct{}{}
	return true
}

func (s *MetadataService) releaseOnDemandMetadataRefresh(targetType, contentID string) {
	if s == nil {
		return
	}
	key := refreshTargetKey(targetType, contentID)
	if key == "" {
		return
	}
	s.onDemandRefresh.mu.Lock()
	defer s.onDemandRefresh.mu.Unlock()
	delete(s.onDemandRefresh.running, key)
}

func refreshTargetKey(targetType, contentID string) string {
	targetType = NormalizeRefreshTargetType(targetType)
	contentID = strings.TrimSpace(contentID)
	if targetType == "" || contentID == "" {
		return ""
	}
	return targetType + ":" + contentID
}

func (s *MetadataService) recordRefreshFailure(ctx context.Context, contentID string, refreshErr error, incrementDebtAttempt bool) {
	if s == nil || s.itemRepo == nil || strings.TrimSpace(contentID) == "" || refreshErr == nil {
		return
	}
	if errors.Is(refreshErr, context.Canceled) || errors.Is(refreshErr, context.DeadlineExceeded) {
		return
	}
	if err := s.itemRepo.IncrementRefreshFailure(ctx, contentID); err != nil && !errors.Is(err, catalog.ErrItemNotFound) {
		slog.Warn("metadata: failed to record refresh failure",
			"content_id", contentID,
			"refresh_error", refreshErr,
			"error", err)
		return
	}
	if err := s.syncRefreshDebtFailure(ctx, contentID, refreshErr, incrementDebtAttempt); err != nil {
		slog.Warn("metadata: failed to sync refresh debt after refresh failure",
			"content_id", contentID,
			"refresh_error", refreshErr,
			"error", err)
	}
}

func (s *MetadataService) recordRefreshTargetFailure(ctx context.Context, targetType, contentID string, refreshErr error, incrementDebtAttempt bool) {
	if NormalizeRefreshTargetType(targetType) == RefreshTargetItem {
		s.recordRefreshFailure(ctx, contentID, refreshErr, incrementDebtAttempt)
		return
	}
	if s == nil || s.refreshDebtRepo == nil || strings.TrimSpace(contentID) == "" || refreshErr == nil {
		return
	}
	if errors.Is(refreshErr, context.Canceled) || errors.Is(refreshErr, context.DeadlineExceeded) {
		return
	}
	if err := s.syncRefreshDebtTargetFailure(ctx, targetType, contentID, refreshErr, incrementDebtAttempt); err != nil {
		slog.Warn("metadata: failed to sync refresh debt after target refresh failure",
			"target_type", targetType,
			"content_id", contentID,
			"refresh_error", refreshErr,
			"error", err)
	}
}

func (s *MetadataService) syncRefreshDebtForItem(ctx context.Context, contentID string) error {
	if s == nil || s.refreshDebtRepo == nil || s.itemRepo == nil || strings.TrimSpace(contentID) == "" {
		return nil
	}

	item, err := s.itemRepo.GetByID(ctx, contentID)
	if err != nil {
		if errors.Is(err, catalog.ErrItemNotFound) {
			return s.refreshDebtRepo.DeleteDebt(ctx, contentID)
		}
		return err
	}

	reasonMask := refreshDebtReasonsForItem(item)
	existingReasonMask, err := s.currentRefreshDebtTargetReasonMask(ctx, RefreshTargetItem, contentID)
	if err != nil {
		return err
	}
	if itemHasEpisodeMetadataDebt(item) && hasRefreshDebtReason(existingReasonMask, RefreshDebtReasonEpisodeIncomplete) {
		reasonMask |= RefreshDebtReasonEpisodeIncomplete
	}
	if staleReason, err := s.currentStaleRefreshDebtReason(ctx, contentID); err != nil {
		return err
	} else {
		reasonMask |= staleReason
	}

	if reasonMask == 0 {
		return s.refreshDebtRepo.DeleteDebt(ctx, contentID)
	}

	attemptCount, err := s.currentRefreshDebtAttemptCount(ctx, contentID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	logRefreshDebtTerminal(RefreshTargetItem, contentID, reasonMask, attemptCount)
	return s.refreshDebtRepo.MarkSuccess(
		ctx,
		contentID,
		effectiveRefreshDebtPriority(reasonMask, attemptCount),
		reasonMask,
		nextRefreshAtForDebt(reasonMask, attemptCount, now),
	)
}

func (s *MetadataService) syncRefreshDebtForTarget(ctx context.Context, targetType, contentID string) error {
	targetType = NormalizeRefreshTargetType(targetType)
	switch targetType {
	case "":
		return nil
	case RefreshTargetItem:
		return s.syncRefreshDebtForItem(ctx, contentID)
	case RefreshTargetSeason:
		return s.syncRefreshDebtForSeason(ctx, contentID)
	case RefreshTargetEpisode:
		return s.syncRefreshDebtForEpisode(ctx, contentID)
	default:
		return nil
	}
}

func (s *MetadataService) syncRefreshDebtForSeason(ctx context.Context, seasonID string) error {
	if s == nil || s.refreshDebtRepo == nil || s.episodeRepo == nil || strings.TrimSpace(seasonID) == "" {
		return nil
	}
	episodes, err := s.episodeRepo.ListBySeasonID(ctx, seasonID)
	if err != nil {
		if errors.Is(err, catalog.ErrSeasonNotFound) {
			return s.refreshDebtRepo.DeleteTargetDebt(ctx, RefreshTargetSeason, seasonID)
		}
		return err
	}
	reasonMask := int64(0)
	now := time.Now().UTC()
	for _, episode := range episodes {
		if EpisodeHasActionableMetadataDebt(episode, now) {
			reasonMask = RefreshDebtReasonEpisodeIncomplete
			break
		}
	}
	if reasonMask == 0 {
		return s.refreshDebtRepo.DeleteTargetDebt(ctx, RefreshTargetSeason, seasonID)
	}
	attemptCount, err := s.currentRefreshDebtTargetAttemptCount(ctx, RefreshTargetSeason, seasonID)
	if err != nil {
		return err
	}
	logRefreshDebtTerminal(RefreshTargetSeason, seasonID, reasonMask, attemptCount)
	return s.refreshDebtRepo.MarkTargetSuccess(
		ctx,
		RefreshTargetSeason,
		seasonID,
		effectiveRefreshDebtPriority(reasonMask, attemptCount),
		reasonMask,
		nextRefreshAtForDebt(reasonMask, attemptCount, now),
	)
}

func (s *MetadataService) syncRefreshDebtForEpisode(ctx context.Context, episodeID string) error {
	if s == nil || s.refreshDebtRepo == nil || s.episodeRepo == nil || strings.TrimSpace(episodeID) == "" {
		return nil
	}
	episode, err := s.episodeRepo.GetByID(ctx, episodeID)
	if err != nil {
		if errors.Is(err, catalog.ErrEpisodeNotFound) {
			return s.refreshDebtRepo.DeleteTargetDebt(ctx, RefreshTargetEpisode, episodeID)
		}
		return err
	}
	reasonMask := int64(0)
	now := time.Now().UTC()
	if EpisodeHasActionableMetadataDebt(episode, now) {
		reasonMask = RefreshDebtReasonEpisodeIncomplete
	}
	if reasonMask == 0 {
		return s.refreshDebtRepo.DeleteTargetDebt(ctx, RefreshTargetEpisode, episodeID)
	}
	attemptCount, err := s.currentRefreshDebtTargetAttemptCount(ctx, RefreshTargetEpisode, episodeID)
	if err != nil {
		return err
	}
	logRefreshDebtTerminal(RefreshTargetEpisode, episodeID, reasonMask, attemptCount)
	return s.refreshDebtRepo.MarkTargetSuccess(
		ctx,
		RefreshTargetEpisode,
		episodeID,
		effectiveRefreshDebtPriority(reasonMask, attemptCount),
		reasonMask,
		nextRefreshAtForDebt(reasonMask, attemptCount, now),
	)
}

func (s *MetadataService) syncRefreshDebtFailure(ctx context.Context, contentID string, refreshErr error, incrementAttempt bool) error {
	if s == nil || s.refreshDebtRepo == nil || s.itemRepo == nil || strings.TrimSpace(contentID) == "" {
		return nil
	}

	item, err := s.itemRepo.GetByID(ctx, contentID)
	if err != nil {
		if errors.Is(err, catalog.ErrItemNotFound) {
			return s.refreshDebtRepo.DeleteDebt(ctx, contentID)
		}
		return err
	}

	reasonMask := refreshDebtReasonsForItem(item)
	existingReasonMask, err := s.currentRefreshDebtTargetReasonMask(ctx, RefreshTargetItem, contentID)
	if err != nil {
		return err
	}
	if itemHasEpisodeMetadataDebt(item) && hasRefreshDebtReason(existingReasonMask, RefreshDebtReasonEpisodeIncomplete) {
		reasonMask |= RefreshDebtReasonEpisodeIncomplete
	}
	staleReason, err := s.currentStaleRefreshDebtReason(ctx, contentID)
	if err != nil {
		return err
	}
	reasonMask |= staleReason
	if strings.EqualFold(strings.TrimSpace(item.Status), "matched") &&
		!hasRefreshDebtReason(reasonMask, RefreshDebtReasonProviderIDIncomplete) {
		// Items missing provider IDs fail for that reason, not because the
		// refresh itself errored. Tagging them with RefreshFailure muddies
		// the metrics — the priority logic already prefers ProviderIDIncomplete.
		reasonMask |= RefreshDebtReasonRefreshFailure
	}
	if reasonMask == 0 {
		return s.refreshDebtRepo.DeleteDebt(ctx, contentID)
	}

	attemptCount, err := s.currentRefreshDebtAttemptCount(ctx, contentID)
	if err != nil {
		return err
	}
	if incrementAttempt {
		attemptCount++
	}
	now := time.Now().UTC()
	logRefreshDebtTerminal(RefreshTargetItem, contentID, reasonMask, attemptCount)
	return s.refreshDebtRepo.MarkFailure(
		ctx,
		contentID,
		effectiveRefreshDebtPriority(reasonMask, attemptCount),
		reasonMask,
		nextRefreshAtForDebt(reasonMask, attemptCount, now),
		attemptCount,
		refreshErr.Error(),
	)
}

func (s *MetadataService) syncRefreshDebtTargetFailure(ctx context.Context, targetType, contentID string, refreshErr error, incrementAttempt bool) error {
	if s == nil || s.refreshDebtRepo == nil || strings.TrimSpace(contentID) == "" {
		return nil
	}
	targetType = NormalizeRefreshTargetType(targetType)
	if targetType == "" || targetType == RefreshTargetItem {
		return s.syncRefreshDebtFailure(ctx, contentID, refreshErr, incrementAttempt)
	}

	reasonMask := RefreshDebtReasonEpisodeIncomplete | RefreshDebtReasonRefreshFailure
	attemptCount, err := s.currentRefreshDebtTargetAttemptCount(ctx, targetType, contentID)
	if err != nil {
		return err
	}
	if incrementAttempt {
		attemptCount++
	}
	now := time.Now().UTC()
	logRefreshDebtTerminal(targetType, contentID, reasonMask, attemptCount)
	return s.refreshDebtRepo.MarkTargetFailure(
		ctx,
		targetType,
		contentID,
		effectiveRefreshDebtPriority(reasonMask, attemptCount),
		reasonMask,
		nextRefreshAtForDebt(reasonMask, attemptCount, now),
		attemptCount,
		refreshErr.Error(),
	)
}

func (s *MetadataService) currentRefreshDebtAttemptCount(ctx context.Context, contentID string) (int, error) {
	return s.currentRefreshDebtTargetAttemptCount(ctx, RefreshTargetItem, contentID)
}

func (s *MetadataService) currentRefreshDebtTargetAttemptCount(ctx context.Context, targetType, contentID string) (int, error) {
	if s == nil || s.refreshDebtRepo == nil || strings.TrimSpace(contentID) == "" {
		return 0, nil
	}
	debt, err := s.refreshDebtRepo.GetTarget(ctx, targetType, contentID)
	if err != nil {
		if errors.Is(err, ErrRefreshDebtNotFound) {
			return 0, nil
		}
		return 0, err
	}
	if debt == nil {
		return 0, nil
	}
	return debt.AttemptCount, nil
}

func (s *MetadataService) currentRefreshDebtTargetReasonMask(ctx context.Context, targetType, contentID string) (int64, error) {
	if s == nil || s.refreshDebtRepo == nil || strings.TrimSpace(contentID) == "" {
		return 0, nil
	}
	debt, err := s.refreshDebtRepo.GetTarget(ctx, targetType, contentID)
	if err != nil {
		if errors.Is(err, ErrRefreshDebtNotFound) {
			return 0, nil
		}
		return 0, err
	}
	if debt == nil {
		return 0, nil
	}
	return debt.ReasonMask, nil
}

func (s *MetadataService) currentStaleRefreshDebtReason(ctx context.Context, contentID string) (int64, error) {
	if s == nil || s.staleIDRepo == nil || strings.TrimSpace(contentID) == "" {
		return 0, nil
	}
	ids, err := s.staleIDRepo.GetByContentID(ctx, contentID)
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	return RefreshDebtReasonStaleProviderID, nil
}

func itemHasEpisodeMetadataDebt(item *models.MediaItem) bool {
	return item != nil &&
		strings.EqualFold(strings.TrimSpace(item.Type), "series") &&
		item.EpisodeMetadataIncomplete
}

func (s *MetadataService) refreshSeasonTarget(ctx context.Context, seasonID string, folderID int, mode RefreshMode) error {
	if s == nil || s.seasonRepo == nil {
		return ErrMetadataNotFound
	}
	season, err := s.seasonRepo.GetByID(ctx, seasonID)
	if err != nil {
		return err
	}
	return s.refreshSeriesChildTarget(ctx, season.SeriesID, season.SeasonNumber, 0, folderID, mode)
}

func (s *MetadataService) refreshEpisodeTarget(ctx context.Context, episodeID string, folderID int, mode RefreshMode) error {
	if s == nil || s.episodeRepo == nil {
		return ErrMetadataNotFound
	}
	episode, err := s.episodeRepo.GetByID(ctx, episodeID)
	if err != nil {
		return err
	}
	return s.refreshSeriesChildTarget(ctx, episode.SeriesID, episode.SeasonNumber, episode.EpisodeNumber, folderID, mode)
}

func (s *MetadataService) refreshSeriesChildTarget(
	ctx context.Context,
	seriesID string,
	seasonNumber int,
	episodeNumber int,
	folderID int,
	mode RefreshMode,
) error {
	if s == nil || s.itemRepo == nil || s.seasonRepo == nil || s.episodeRepo == nil {
		return ErrMetadataNotFound
	}
	series, err := s.itemRepo.GetByID(ctx, seriesID)
	if err != nil {
		return err
	}
	if !strings.EqualFold(strings.TrimSpace(series.Type), "series") {
		return ErrMetadataNotFound
	}

	languages, err := s.resolveProcessLanguages(ctx, ProcessRequest{
		ContentID: seriesID,
		FolderID:  formatFolderID(folderID),
	}, folderID)
	if err != nil {
		return err
	}
	mergeMode := MergeFillEmpty
	if mode == ModeScheduledRefresh || mode == ModeManualRefresh || mode == ModeIdentify {
		mergeMode = MergeReplaceUnlocked
	}

	updated := false
	for _, language := range languages {
		canonicalLanguage := strings.TrimSpace(series.DefaultMetadataLanguage)
		if canonicalLanguage == "" {
			canonicalLanguage = strings.TrimSpace(language)
		}
		if canonicalLanguage == "" {
			canonicalLanguage = "en"
		}

		providerIDs, err := s.resolveSeriesRefreshProviderIDs(ctx, series, folderID, language)
		if err != nil {
			return err
		}
		seasons, err := s.fetchTargetSeasonResults(ctx, providerIDs, folderID, language, seasonNumber)
		if err != nil {
			return err
		}
		episodes, err := s.fetchTargetEpisodeResults(ctx, providerIDs, folderID, language, seasonNumber, episodeNumber)
		if err != nil {
			return err
		}
		if len(seasons) == 0 && len(episodes) == 0 {
			continue
		}
		s.persistSeasonsAndEpisodes(ctx, series, providerIDs, canonicalLanguage, language, seasons, episodes, mergeMode)
		updated = true
	}
	if !updated {
		return ErrMetadataNotFound
	}
	return nil
}

func (s *MetadataService) resolveSeriesRefreshProviderIDs(ctx context.Context, series *models.MediaItem, folderID int, language string) (map[string]string, error) {
	accumulatedIDs := make(map[string]string)
	if series == nil {
		return accumulatedIDs, nil
	}
	if series.TmdbID != "" {
		accumulatedIDs["tmdb"] = series.TmdbID
	}
	if series.TvdbID != "" {
		accumulatedIDs["tvdb"] = series.TvdbID
	}
	if series.ImdbID != "" {
		accumulatedIDs["imdb"] = series.ImdbID
	}
	durableIDs, err := s.loadDurableProviderIDs(ctx, series.ContentID)
	if err != nil {
		return nil, err
	}
	maps.Copy(accumulatedIDs, durableIDs)
	if err := s.suppressRecordedStaleProviderIDs(ctx, series.ContentID, accumulatedIDs); err != nil {
		return nil, err
	}

	itemChain, err := s.resolveChainCached(ctx, folderID, "series")
	if err != nil {
		return nil, fmt.Errorf("resolve series provider chain: %w", err)
	}
	searchQuery := SearchQuery{
		Title:       series.Title,
		Year:        series.Year,
		ContentType: series.Type,
		ProviderIDs: accumulatedIDs,
		Language:    language,
	}
	localCtx := s.localProviderContextForContent(ctx, series.ContentID, folderID)
	searchQuery.FilePath = localCtx.filePath
	searchQuery.RepresentativeFilePath = localCtx.representativeFilePath
	searchQuery.ObservedRootPath = localCtx.observedRootPath
	searchQuery.AllGroupFilePaths = append([]string(nil), localCtx.allGroupFilePaths...)
	searchQuery.PrimarySidecarSearchPaths = append([]string(nil), localCtx.primarySidecarSearchPaths...)
	searchQuery = suppressTitleYearFallbackForTrustedIDs(searchQuery)

	allResults := make([]SearchResult, 0)
	provider404s := make(map[string]string)
	for _, p := range itemChain {
		sp, ok := p.(SearchProvider)
		if !ok {
			continue
		}
		results, searchErr := sp.Search(ctx, searchQuery)
		if searchErr != nil {
			if handleProvider404(provider404s, accumulatedIDs, p.Slug(), searchErr,
				"content_id", series.ContentID,
			) {
				continue
			}
			slog.Warn("metadata: target refresh search error",
				"provider", p.Slug(), "error", searchErr)
			continue
		}
		for _, result := range results {
			if searchResultConflictsWithTrustedIDs(accumulatedIDs, result.ProviderIDs) {
				continue
			}
			allResults = append(allResults, result)
		}
	}
	candidates := NormalizeCandidates(allResults, series.Type)
	if winner, ok := selectRefreshMatchCandidate(series, candidates); ok && winner != nil {
		for k, v := range winner.ProviderIDs {
			if v != "" {
				accumulatedIDs[k] = v
			}
		}
	}
	if err := s.suppressRecordedStaleProviderIDs(ctx, series.ContentID, accumulatedIDs); err != nil {
		return nil, err
	}
	return accumulatedIDs, nil
}

func (s *MetadataService) fetchTargetSeasonResults(ctx context.Context, providerIDs map[string]string, folderID int, language string, seasonNumber int) ([]SeasonResult, error) {
	seasonChain, err := s.resolveChainCached(ctx, folderID, "season")
	if err != nil {
		return nil, fmt.Errorf("resolve season provider chain: %w", err)
	}
	seasonResults := make(map[int]*SeasonResult)
	for _, p := range seasonChain {
		ep, ok := p.(EpisodeProvider)
		if !ok {
			continue
		}
		seasons, err := ep.GetSeasons(ctx, SeasonsRequest{
			ProviderIDs: providerIDs,
			ContentType: "series",
			Language:    language,
		})
		if err != nil {
			if handleProvider404(nil, providerIDs, p.Slug(), err, "season", seasonNumber) {
				continue
			}
			slog.Warn("metadata: target season provider error",
				"provider", p.Slug(), "season", seasonNumber, "error", err)
			continue
		}
		for _, season := range seasons {
			if season.SeasonNumber != seasonNumber {
				continue
			}
			accumulateSeasonResults(seasonResults, []SeasonResult{season})
		}
	}
	return flattenSeasonResults(seasonResults), nil
}

func (s *MetadataService) fetchTargetEpisodeResults(ctx context.Context, providerIDs map[string]string, folderID int, language string, seasonNumber int, episodeNumber int) ([]EpisodeResult, error) {
	episodeChain, err := s.resolveChainCached(ctx, folderID, "episode")
	if err != nil {
		return nil, fmt.Errorf("resolve episode provider chain: %w", err)
	}
	episodeResults := make(map[episodeResultKey]*EpisodeResult)
	for _, p := range episodeChain {
		ep, ok := p.(EpisodeProvider)
		if !ok {
			continue
		}
		episodes, err := ep.GetEpisodes(ctx, EpisodesRequest{
			ProviderIDs:  providerIDs,
			SeasonNumber: seasonNumber,
			Language:     language,
		})
		if err != nil {
			if handleChildProvider404(p.Slug(), providerIDs, err, "season", seasonNumber) {
				continue
			}
			slog.Warn("metadata: target episode provider error",
				"provider", p.Slug(), "season", seasonNumber, "error", err)
			continue
		}
		for _, episode := range episodes {
			if episode.SeasonNumber != seasonNumber {
				continue
			}
			if episodeNumber > 0 && episode.EpisodeNumber != episodeNumber {
				continue
			}
			accumulateEpisodeResults(episodeResults, []EpisodeResult{episode})
		}
	}
	return flattenEpisodeResults(episodeResults), nil
}

func (s *MetadataService) enqueueSeriesChildImages(ctx context.Context, seriesID string, inputs []EnqueueImageCacheJobInput) {
	s.enqueueImageCacheJobs(ctx, "series child", seriesID, inputs)
}

func (s *MetadataService) enqueueImageCacheJobs(ctx context.Context, targetKind, targetID string, inputs []EnqueueImageCacheJobInput) {
	if s == nil || !s.autoCacheImages.Load() || s.imageCacheJobs == nil || len(inputs) == 0 {
		return
	}
	enqueueCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if _, err := s.imageCacheJobs.EnqueueBatch(enqueueCtx, inputs); err != nil {
		slog.Warn("metadata: failed to enqueue image cache jobs",
			"target_kind", targetKind, "target_id", targetID, "count", len(inputs), "error", err)
	}
}

// providerIDFromPluginURL extracts the plugin slug from a plugin-scheme URL
// (e.g. "tvdb://banners/..." -> "tvdb"). Returns "" for HTTP(S) URLs and
// any other input lacking a scheme.
func providerIDFromPluginURL(url string) string {
	url = strings.TrimSpace(url)
	if url == "" {
		return ""
	}
	if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
		return ""
	}
	i := strings.Index(url, "://")
	if i <= 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(url[:i]))
}

func isProviderImagePath(path string) bool {
	path = strings.TrimSpace(path)
	lower := strings.ToLower(path)
	return path != "" &&
		strings.Contains(path, "://") &&
		!strings.HasPrefix(lower, "http://") &&
		!strings.HasPrefix(lower, "https://") &&
		!isNonProviderImageScheme(lower)
}

func isCachedImagePath(path string) bool {
	path = strings.TrimSpace(path)
	return path != "" &&
		path != "-" &&
		!strings.HasPrefix(path, "http://") &&
		!strings.HasPrefix(path, "https://") &&
		!isProviderImagePath(path)
}

func isRemoteImageSourcePath(path string) bool {
	path = strings.TrimSpace(path)
	lower := strings.ToLower(path)
	return path != "" &&
		path != "-" &&
		strings.Contains(path, "://") &&
		!isNonProviderImageScheme(lower)
}

func isNonProviderImageScheme(lowerPath string) bool {
	return strings.HasPrefix(lowerPath, "s3://") ||
		strings.HasPrefix(lowerPath, "file://") ||
		strings.HasPrefix(lowerPath, "local://") ||
		strings.HasPrefix(lowerPath, "upload://") ||
		strings.HasPrefix(lowerPath, "generated://")
}

func providerImageSourcePath(path string) string {
	path = strings.TrimSpace(path)
	if isRemoteImageSourcePath(path) {
		return path
	}
	return ""
}

func preserveCachedArtwork(providerPath, providerThumbhash, existingCachedPath, existingSourcePath, existingThumbhash string) (string, string, string) {
	if !isRemoteImageSourcePath(providerPath) {
		if strings.TrimSpace(providerPath) == "" && isCachedImagePath(existingCachedPath) {
			return existingCachedPath, existingThumbhash, existingSourcePath
		}
		return providerPath, providerThumbhash, ""
	}
	if isCachedImagePath(existingCachedPath) && existingSourcePath == providerPath {
		return existingCachedPath, existingThumbhash, providerPath
	}
	if isCachedImagePath(existingCachedPath) {
		return existingCachedPath, existingThumbhash, providerPath
	}
	return providerPath, providerThumbhash, providerPath
}

// resolveImageURLForCache normalizes a stored image path into a downloadable
// HTTP(S) URL suitable for the image cacher. Returns ok=false if the path is
// empty, has no scheme, or a plugin-prefixed URL cannot be resolved.
func (s *MetadataService) resolveImageURLForCache(ctx context.Context, path string) (string, bool) {
	if path == "" || !strings.Contains(path, "://") {
		return "", false
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path, true
	}
	if s.imageResolver == nil {
		slog.Warn("metadata: cannot cache plugin image without resolver", "url", path)
		return "", false
	}
	resolved := s.imageResolver.ResolveImageURL(ctx, path, "original")
	if resolved == "" {
		slog.Warn("metadata: resolver returned empty URL for image", "url", path)
		return "", false
	}
	return resolved, true
}

type episodeResultKey struct {
	seasonNumber  int
	episodeNumber int
}

func accumulateSeasonResults(accumulator map[int]*SeasonResult, seasons []SeasonResult) {
	for _, season := range seasons {
		existing := accumulator[season.SeasonNumber]
		if existing == nil {
			cp := season
			accumulator[season.SeasonNumber] = &cp
			continue
		}

		MergeSeasonResult(&season, existing, MergeFillEmpty)
	}
}

func flattenSeasonResults(accumulator map[int]*SeasonResult) []SeasonResult {
	if len(accumulator) == 0 {
		return nil
	}

	seasonNumbers := make([]int, 0, len(accumulator))
	for seasonNumber := range accumulator {
		seasonNumbers = append(seasonNumbers, seasonNumber)
	}
	sort.Ints(seasonNumbers)

	results := make([]SeasonResult, 0, len(seasonNumbers))
	for _, seasonNumber := range seasonNumbers {
		results = append(results, *accumulator[seasonNumber])
	}
	return results
}

func accumulateEpisodeResults(accumulator map[episodeResultKey]*EpisodeResult, episodes []EpisodeResult) {
	for _, episode := range episodes {
		key := episodeResultKey{
			seasonNumber:  episode.SeasonNumber,
			episodeNumber: episode.EpisodeNumber,
		}
		existing := accumulator[key]
		if existing == nil {
			cp := episode
			accumulator[key] = &cp
			continue
		}

		MergeEpisodeResult(&episode, existing, MergeFillEmpty)
	}
}

func flattenEpisodeResults(accumulator map[episodeResultKey]*EpisodeResult) []EpisodeResult {
	if len(accumulator) == 0 {
		return nil
	}

	keys := make([]episodeResultKey, 0, len(accumulator))
	for key := range accumulator {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].seasonNumber != keys[j].seasonNumber {
			return keys[i].seasonNumber < keys[j].seasonNumber
		}
		return keys[i].episodeNumber < keys[j].episodeNumber
	})

	results := make([]EpisodeResult, 0, len(keys))
	for _, key := range keys {
		results = append(results, *accumulator[key])
	}
	return results
}

func buildItemLocalizationRecord(
	existing *models.MediaItemLocalization,
	contentID string,
	language string,
	contentType string,
	accumulator *MetadataResult,
	images []RemoteImage,
	mergeMode MergeMode,
	preferredLanguage string,
	titleLocked bool,
) *models.MediaItemLocalization {
	loc := &models.MediaItemLocalization{
		ContentID: contentID,
		Language:  language,
	}
	if existing != nil {
		*loc = *existing
		loc.ContentID = contentID
		loc.Language = language
	}

	mergeScalar(&loc.Title, accumulator.Title, mergeMode)
	mergeScalar(&loc.SortTitle, accumulator.SortTitle, mergeMode)
	mergeScalar(&loc.Overview, accumulator.Overview, mergeMode)
	mergeScalar(&loc.Tagline, accumulator.Tagline, mergeMode)

	existingLocItem := &models.MediaItem{
		Type:               contentType,
		PosterPath:         loc.PosterPath,
		PosterSourcePath:   loc.PosterSourcePath,
		PosterThumbhash:    loc.PosterThumbhash,
		BackdropPath:       loc.BackdropPath,
		BackdropSourcePath: loc.BackdropSourcePath,
		BackdropThumbhash:  loc.BackdropThumbhash,
		LogoPath:           loc.LogoPath,
		LogoSourcePath:     loc.LogoSourcePath,
	}
	locItem := &models.MediaItem{
		Type:               contentType,
		PosterPath:         loc.PosterPath,
		PosterSourcePath:   loc.PosterSourcePath,
		PosterThumbhash:    loc.PosterThumbhash,
		BackdropPath:       loc.BackdropPath,
		BackdropSourcePath: loc.BackdropSourcePath,
		BackdropThumbhash:  loc.BackdropThumbhash,
		LogoPath:           loc.LogoPath,
		LogoSourcePath:     loc.LogoSourcePath,
	}
	applyBestImages(locItem, images, mergeMode, preferredLanguage)
	prepareItemImagesForQueue(locItem, existingLocItem)

	loc.PosterPath = locItem.PosterPath
	loc.PosterSourcePath = locItem.PosterSourcePath
	loc.PosterThumbhash = locItem.PosterThumbhash
	loc.BackdropPath = locItem.BackdropPath
	loc.BackdropSourcePath = locItem.BackdropSourcePath
	loc.BackdropThumbhash = locItem.BackdropThumbhash
	loc.LogoPath = locItem.LogoPath
	loc.LogoSourcePath = locItem.LogoSourcePath

	ApplyDefaultSortTitleToLocalization(loc, titleLocked)

	return loc
}

func buildSeasonLocalizationRecord(
	existing *models.SeasonLocalization,
	seasonContentID string,
	language string,
	season SeasonResult,
	mergeMode MergeMode,
) *models.SeasonLocalization {
	loc := &models.SeasonLocalization{
		SeasonContentID: seasonContentID,
		Language:        language,
	}
	if existing != nil {
		*loc = *existing
		loc.SeasonContentID = seasonContentID
		loc.Language = language
	}

	previousPosterPath := loc.PosterPath
	previousPosterSourcePath := loc.PosterSourcePath
	previousPosterThumbhash := loc.PosterThumbhash

	mergeScalar(&loc.Title, season.Title, mergeMode)
	mergeScalar(&loc.Overview, season.Overview, mergeMode)
	mergeScalar(&loc.PosterPath, season.PosterPath, mergeMode)
	if isCachedImagePath(loc.PosterPath) && loc.PosterPath == previousPosterPath {
		loc.PosterSourcePath = previousPosterSourcePath
		loc.PosterThumbhash = previousPosterThumbhash
		return loc
	}
	loc.PosterPath, loc.PosterThumbhash, loc.PosterSourcePath = preserveCachedArtwork(
		loc.PosterPath,
		season.PosterThumbhash,
		previousPosterPath,
		previousPosterSourcePath,
		previousPosterThumbhash,
	)

	return loc
}

func buildEpisodeLocalizationRecord(
	existing *models.EpisodeLocalization,
	episodeContentID string,
	language string,
	episode EpisodeResult,
	mergeMode MergeMode,
) *models.EpisodeLocalization {
	loc := &models.EpisodeLocalization{
		EpisodeContentID: episodeContentID,
		Language:         language,
	}
	if existing != nil {
		*loc = *existing
		loc.EpisodeContentID = episodeContentID
		loc.Language = language
	}

	mergeScalar(&loc.Title, episode.Title, mergeMode)
	mergeScalar(&loc.Overview, episode.Overview, mergeMode)

	return loc
}

func seasonResultFromModel(season *models.Season) SeasonResult {
	if season == nil {
		return SeasonResult{}
	}

	result := SeasonResult{
		SeasonNumber:     season.SeasonNumber,
		Title:            season.Title,
		Overview:         season.Overview,
		PosterPath:       season.PosterPath,
		PosterSourcePath: season.PosterSourcePath,
		PosterThumbhash:  season.PosterThumbhash,
	}
	if season.AirDate != nil {
		result.AirDate = season.AirDate.Format("2006-01-02")
	}
	return result
}

func episodeResultFromModel(episode *models.Episode) EpisodeResult {
	if episode == nil {
		return EpisodeResult{}
	}

	result := EpisodeResult{
		SeasonNumber:    episode.SeasonNumber,
		EpisodeNumber:   episode.EpisodeNumber,
		Title:           episode.Title,
		Overview:        episode.Overview,
		Runtime:         episode.Runtime,
		StillPath:       episode.StillPath,
		StillSourcePath: episode.StillSourcePath,
		StillThumbhash:  episode.StillThumbhash,
		ProviderIDs: map[string]string{
			"imdb": episode.ImdbID,
			"tmdb": episode.TmdbID,
			"tvdb": episode.TvdbID,
		},
	}
	if episode.AirDate != nil {
		result.AirDate = episode.AirDate.Format("2006-01-02")
	}
	if episode.RatingIMDB != nil {
		result.Ratings.IMDB = *episode.RatingIMDB
	}
	if episode.RatingTMDB != nil {
		result.Ratings.TMDB = *episode.RatingTMDB
	}
	return result
}

func existingImagePath(item *models.MediaItem, imageType ImageType) string {
	if item == nil {
		return ""
	}
	switch imageType {
	case ImagePoster:
		return item.PosterPath
	case ImageBackdrop:
		return item.BackdropPath
	default:
		return item.LogoPath
	}
}

func existingImageThumbhash(item *models.MediaItem, imageType ImageType) string {
	if item == nil {
		return ""
	}
	switch imageType {
	case ImagePoster:
		return item.PosterThumbhash
	case ImageBackdrop:
		return item.BackdropThumbhash
	default:
		return ""
	}
}

func existingImageSourcePath(item *models.MediaItem, imageType ImageType) string {
	if item == nil {
		return ""
	}
	switch imageType {
	case ImagePoster:
		return item.PosterSourcePath
	case ImageBackdrop:
		return item.BackdropSourcePath
	case ImageLogo:
		return item.LogoSourcePath
	default:
		return ""
	}
}

func mergedImageThumbhash(previousPath, previousThumbhash, nextPath, nextThumbhash string) string {
	if nextThumbhash != "" {
		return nextThumbhash
	}
	if nextPath != "" && nextPath == previousPath {
		return previousThumbhash
	}
	return ""
}

// SearchProviders runs only the search phase and returns results for the UI.
func (s *MetadataService) SearchProviders(ctx context.Context, query SearchQuery, folderID int) ([]SearchResult, error) {
	if strings.TrimSpace(query.Language) == "" && folderID > 0 {
		query.Language = s.resolveFolderLanguage(ctx, folderID)
	}

	// Search uses the item-level chain. Video child records search through the
	// series chain; non-video content searches through its own content level.
	contentLevel := providerChainContentLevel(query.ContentType)
	chain, err := s.resolveChainCached(ctx, folderID, contentLevel)
	if err != nil {
		return nil, fmt.Errorf("resolving provider chain: %w", err)
	}

	var allResults []SearchResult
	for _, p := range chain {
		sp, ok := p.(SearchProvider)
		if !ok {
			continue
		}
		results, err := sp.Search(ctx, query)
		if err != nil {
			continue
		}
		allResults = append(allResults, results...)
	}
	return allResults, nil
}

func providerChainContentLevel(contentType string) string {
	switch normalized := strings.ToLower(strings.TrimSpace(contentType)); normalized {
	case "movie", "movies":
		return "movie"
	case "series", "show", "shows", "tv", "season", "seasons", "episode", "episodes":
		return "series"
	case "":
		return "series"
	default:
		return normalized
	}
}

// persistSeasonsAndEpisodes creates/updates seasons and episodes in the DB.
func (s *MetadataService) persistSeasonsAndEpisodes(
	ctx context.Context,
	series *models.MediaItem,
	providerIDs map[string]string,
	canonicalLanguage string,
	language string,
	seasons []SeasonResult,
	episodes []EpisodeResult,
	mergeMode MergeMode,
) {
	if series == nil || strings.TrimSpace(series.ContentID) == "" {
		return
	}
	seriesID := series.ContentID
	seasonIDs := make(map[int]string, len(seasons))
	isCanonicalWrite := strings.EqualFold(canonicalLanguage, language)
	imageJobs := make([]EnqueueImageCacheJobInput, 0, len(seasons)+len(episodes))
	fallbackProvider := primaryProviderID(providerIDs)
	keyAttribution := func(sourcePath string) (string, string) {
		providerID := providerIDFromPluginURL(sourcePath)
		if providerID == "" {
			providerID = fallbackProvider
		}
		return providerID, findContentID(series, providerID)
	}
	addSeasonImageJob := func(season *models.Season) {
		if season == nil || !isRemoteImageSourcePath(season.PosterSourcePath) {
			return
		}
		providerID, providerContentID := keyAttribution(season.PosterSourcePath)
		seasonNumber := season.SeasonNumber
		imageJobs = append(imageJobs, EnqueueImageCacheJobInput{
			TargetType:        ImageCacheTargetSeason,
			TargetContentID:   season.ContentID,
			SeriesID:          seriesID,
			SourcePath:        season.PosterSourcePath,
			ProviderID:        providerID,
			ProviderContentID: providerContentID,
			ContentType:       "series",
			ImageType:         ImageCacheImagePoster,
			SeasonNumber:      &seasonNumber,
		})
	}
	addEpisodeImageJob := func(episode *models.Episode) {
		if episode == nil || !isRemoteImageSourcePath(episode.StillSourcePath) {
			return
		}
		providerID, providerContentID := keyAttribution(episode.StillSourcePath)
		seasonNumber := episode.SeasonNumber
		episodeNumber := episode.EpisodeNumber
		imageJobs = append(imageJobs, EnqueueImageCacheJobInput{
			TargetType:        ImageCacheTargetEpisode,
			TargetContentID:   episode.ContentID,
			SeriesID:          seriesID,
			SourcePath:        episode.StillSourcePath,
			ProviderID:        providerID,
			ProviderContentID: providerContentID,
			ContentType:       "series",
			ImageType:         ImageCacheImageStill,
			SeasonNumber:      &seasonNumber,
			EpisodeNumber:     &episodeNumber,
		})
	}
	addSeasonLocalizationImageJob := func(season *models.Season, loc *models.SeasonLocalization) {
		if season == nil || loc == nil || !isRemoteImageSourcePath(loc.PosterSourcePath) {
			return
		}
		providerID, providerContentID := keyAttribution(loc.PosterSourcePath)
		seasonNumber := season.SeasonNumber
		imageJobs = append(imageJobs, EnqueueImageCacheJobInput{
			TargetType:        ImageCacheTargetSeasonLocalization,
			TargetContentID:   season.ContentID,
			TargetLanguage:    loc.Language,
			SeriesID:          seriesID,
			SourcePath:        loc.PosterSourcePath,
			ProviderID:        providerID,
			ProviderContentID: providerContentID,
			ContentType:       "series",
			ImageType:         ImageCacheImagePoster,
			SeasonNumber:      &seasonNumber,
		})
	}

	// Phase 1: Upsert explicit seasons.
	if len(seasons) > 0 {
		for _, season := range seasons {
			existingSeason, err := s.seasonRepo.GetBySeriesAndNumber(ctx, seriesID, season.SeasonNumber)
			if err != nil && !errors.Is(err, catalog.ErrSeasonNotFound) {
				slog.Warn("metadata: failed to load season before upsert",
					"series_id", seriesID, "season", season.SeasonNumber, "error", err)
				continue
			}
			providerSeason := season
			providerSeason.PosterSourcePath = providerImageSourcePath(providerSeason.PosterPath)
			if existingSeason != nil && isCanonicalWrite {
				mergedSeason := seasonResultFromModel(existingSeason)
				MergeSeasonResult(&providerSeason, &mergedSeason, mergeMode)
				nextPath, nextThumbhash, nextSourcePath := preserveCachedArtwork(
					providerSeason.PosterPath,
					providerSeason.PosterThumbhash,
					existingSeason.PosterPath,
					existingSeason.PosterSourcePath,
					existingSeason.PosterThumbhash,
				)
				mergedSeason.PosterPath = nextPath
				mergedSeason.PosterThumbhash = nextThumbhash
				mergedSeason.PosterSourcePath = nextSourcePath
				providerSeason = mergedSeason
			}
			dbSeason := &models.Season{
				SeriesID:                seriesID,
				SeasonNumber:            providerSeason.SeasonNumber,
				Title:                   providerSeason.Title,
				DefaultMetadataLanguage: canonicalLanguage,
				Overview:                providerSeason.Overview,
				PosterPath:              providerSeason.PosterPath,
				PosterSourcePath:        providerSeason.PosterSourcePath,
				PosterThumbhash:         providerSeason.PosterThumbhash,
				MetadataSource:          "provider",
			}
			if existingSeason != nil {
				dbSeason.ContentID = existingSeason.ContentID
				if !isCanonicalWrite {
					dbSeason.Title = existingSeason.Title
					dbSeason.Overview = existingSeason.Overview
					dbSeason.PosterPath = existingSeason.PosterPath
					dbSeason.PosterSourcePath = existingSeason.PosterSourcePath
					dbSeason.PosterThumbhash = existingSeason.PosterThumbhash
					dbSeason.DefaultMetadataLanguage = existingSeason.DefaultMetadataLanguage
				}
			} else {
				sid, genErr := deriveSeasonContentID(seriesID, providerSeason.SeasonNumber)
				if genErr != nil {
					slog.Warn("metadata: failed to generate season id",
						"series_id", seriesID, "season", season.SeasonNumber, "error", genErr)
					continue
				}
				dbSeason.ContentID = sid
			}
			if providerSeason.AirDate != "" {
				if t, parseErr := time.Parse("2006-01-02", providerSeason.AirDate); parseErr == nil {
					dbSeason.AirDate = &t
				}
			}
			if err := s.seasonRepo.Upsert(ctx, dbSeason); err != nil {
				slog.Warn("metadata: failed to upsert season",
					"series_id", seriesID, "season", season.SeasonNumber, "error", err)
				continue
			}
			seasonIDs[dbSeason.SeasonNumber] = dbSeason.ContentID
			addSeasonImageJob(dbSeason)
			if !isCanonicalWrite && s.seasonLocalizationRepo != nil {
				existingLoc, locErr := s.seasonLocalizationRepo.Get(ctx, dbSeason.ContentID, language)
				if locErr != nil {
					slog.Warn("metadata: failed to load season localization",
						"series_id", seriesID, "season", season.SeasonNumber, "error", locErr)
				}
				loc := buildSeasonLocalizationRecord(
					existingLoc,
					dbSeason.ContentID,
					language,
					providerSeason,
					mergeMode,
				)
				if err := s.seasonLocalizationRepo.Upsert(ctx, loc); err != nil {
					slog.Warn("metadata: failed to upsert season localization",
						"series_id", seriesID, "season", season.SeasonNumber, "error", err)
				} else {
					addSeasonLocalizationImageJob(dbSeason, loc)
				}
			}
		}
	}

	// Phase 2: Identify implicit seasons referenced by episodes but not returned
	// explicitly by the provider.
	if len(episodes) > 0 {
		implicitSeen := make(map[int]bool)
		for _, ep := range episodes {
			if _, ok := seasonIDs[ep.SeasonNumber]; ok || implicitSeen[ep.SeasonNumber] {
				continue
			}
			implicitSeen[ep.SeasonNumber] = true
			title := fmt.Sprintf("Season %d", ep.SeasonNumber)
			if ep.SeasonNumber == 0 {
				title = "Specials"
			}
			seasonModel := &models.Season{
				SeriesID:                seriesID,
				SeasonNumber:            ep.SeasonNumber,
				Title:                   title,
				DefaultMetadataLanguage: canonicalLanguage,
				MetadataSource:          "provider",
			}
			if existingSeason, err := s.seasonRepo.GetBySeriesAndNumber(ctx, seriesID, ep.SeasonNumber); err == nil {
				seasonModel.ContentID = existingSeason.ContentID
				seasonModel.DefaultMetadataLanguage = existingSeason.DefaultMetadataLanguage
				if isCanonicalWrite {
					mergedSeason := seasonResultFromModel(existingSeason)
					MergeSeasonResult(&SeasonResult{
						SeasonNumber: ep.SeasonNumber,
						Title:        title,
					}, &mergedSeason, mergeMode)
					mergedSeason.PosterThumbhash = mergedImageThumbhash(
						existingSeason.PosterPath,
						existingSeason.PosterThumbhash,
						mergedSeason.PosterPath,
						"",
					)
					seasonModel.Title = mergedSeason.Title
					seasonModel.Overview = mergedSeason.Overview
					seasonModel.PosterPath = mergedSeason.PosterPath
					seasonModel.PosterSourcePath = mergedSeason.PosterSourcePath
					seasonModel.PosterThumbhash = mergedSeason.PosterThumbhash
				} else {
					seasonModel.Title = existingSeason.Title
					seasonModel.Overview = existingSeason.Overview
					seasonModel.PosterPath = existingSeason.PosterPath
					seasonModel.PosterSourcePath = existingSeason.PosterSourcePath
					seasonModel.PosterThumbhash = existingSeason.PosterThumbhash
				}
			} else {
				sid, genErr := deriveSeasonContentID(seriesID, ep.SeasonNumber)
				if genErr != nil {
					slog.Warn("metadata: failed to generate implicit season id",
						"series_id", seriesID, "season", ep.SeasonNumber, "error", genErr)
					continue
				}
				seasonModel.ContentID = sid
			}
			if err := s.seasonRepo.Upsert(ctx, seasonModel); err != nil {
				slog.Warn("metadata: failed to upsert implicit season",
					"series_id", seriesID, "season", ep.SeasonNumber, "error", err)
				continue
			}
			seasonIDs[seasonModel.SeasonNumber] = seasonModel.ContentID
			addSeasonImageJob(seasonModel)
		}
	}

	// Phase 3: Upsert episodes.
	if len(episodes) > 0 {
		for _, ep := range episodes {
			existingEpisode, err := s.episodeRepo.GetBySeriesAndNumber(ctx, seriesID, ep.SeasonNumber, ep.EpisodeNumber)
			if err != nil && !errors.Is(err, catalog.ErrEpisodeNotFound) {
				slog.Warn("metadata: failed to load episode before upsert",
					"series_id", seriesID, "season", ep.SeasonNumber, "episode", ep.EpisodeNumber, "error", err)
				continue
			}
			providerEpisode := ep
			providerEpisode.StillSourcePath = providerImageSourcePath(providerEpisode.StillPath)
			if existingEpisode != nil && isCanonicalWrite {
				mergedEpisode := episodeResultFromModel(existingEpisode)
				MergeEpisodeResult(&providerEpisode, &mergedEpisode, mergeMode)
				nextPath, nextThumbhash, nextSourcePath := preserveCachedArtwork(
					providerEpisode.StillPath,
					providerEpisode.StillThumbhash,
					existingEpisode.StillPath,
					existingEpisode.StillSourcePath,
					existingEpisode.StillThumbhash,
				)
				mergedEpisode.StillPath = nextPath
				mergedEpisode.StillThumbhash = nextThumbhash
				mergedEpisode.StillSourcePath = nextSourcePath
				providerEpisode = mergedEpisode
			}
			dbEp := &models.Episode{
				SeriesID:                seriesID,
				SeasonID:                seasonIDs[providerEpisode.SeasonNumber],
				SeasonNumber:            providerEpisode.SeasonNumber,
				EpisodeNumber:           providerEpisode.EpisodeNumber,
				Title:                   providerEpisode.Title,
				DefaultMetadataLanguage: canonicalLanguage,
				Overview:                providerEpisode.Overview,
				Runtime:                 providerEpisode.Runtime,
				ImdbID:                  providerEpisode.ProviderIDs["imdb"],
				TmdbID:                  providerEpisode.ProviderIDs["tmdb"],
				TvdbID:                  providerEpisode.ProviderIDs["tvdb"],
				StillPath:               providerEpisode.StillPath,
				StillSourcePath:         providerEpisode.StillSourcePath,
				StillThumbhash:          providerEpisode.StillThumbhash,
				MetadataSource:          "provider",
			}
			if existingEpisode != nil {
				dbEp.ContentID = existingEpisode.ContentID
				if !isCanonicalWrite {
					dbEp.Title = existingEpisode.Title
					dbEp.Overview = existingEpisode.Overview
					dbEp.DefaultMetadataLanguage = existingEpisode.DefaultMetadataLanguage
					dbEp.StillPath = existingEpisode.StillPath
					dbEp.StillSourcePath = existingEpisode.StillSourcePath
					dbEp.StillThumbhash = existingEpisode.StillThumbhash
				}
			} else {
				eid, genErr := deriveEpisodeContentID(seriesID, providerEpisode.SeasonNumber, providerEpisode.EpisodeNumber)
				if genErr != nil {
					slog.Warn("metadata: failed to generate episode id",
						"series_id", seriesID, "season", ep.SeasonNumber,
						"episode", ep.EpisodeNumber, "error", genErr)
					continue
				}
				dbEp.ContentID = eid
			}
			if providerEpisode.AirDate != "" {
				if t, parseErr := time.Parse("2006-01-02", providerEpisode.AirDate); parseErr == nil {
					dbEp.AirDate = &t
				}
			}
			if providerEpisode.Ratings.TMDB > 0 {
				v := providerEpisode.Ratings.TMDB
				dbEp.RatingTMDB = &v
			}
			if providerEpisode.Ratings.IMDB > 0 {
				v := providerEpisode.Ratings.IMDB
				dbEp.RatingIMDB = &v
			}
			if err := s.episodeRepo.Upsert(ctx, dbEp); err != nil {
				slog.Warn("metadata: failed to upsert episode",
					"series_id", seriesID, "season", ep.SeasonNumber,
					"episode", ep.EpisodeNumber, "error", err)
				continue
			}
			addEpisodeImageJob(dbEp)
			if !isCanonicalWrite && s.episodeLocalizationRepo != nil {
				existingLoc, locErr := s.episodeLocalizationRepo.Get(ctx, dbEp.ContentID, language)
				if locErr != nil {
					slog.Warn("metadata: failed to load episode localization",
						"series_id", seriesID, "season", ep.SeasonNumber,
						"episode", ep.EpisodeNumber, "error", locErr)
				}
				if err := s.episodeLocalizationRepo.Upsert(ctx, buildEpisodeLocalizationRecord(
					existingLoc,
					dbEp.ContentID,
					language,
					providerEpisode,
					mergeMode,
				)); err != nil {
					slog.Warn("metadata: failed to upsert episode localization",
						"series_id", seriesID, "season", ep.SeasonNumber,
						"episode", ep.EpisodeNumber, "error", err)
				}
			}
		}

	}

	s.enqueueSeriesChildImages(ctx, seriesID, imageJobs)

	if err := s.ensureSeriesEpisodeLinks(ctx, seriesID); err != nil {
		slog.Warn("metadata: failed to ensure series episode links",
			"series_id", seriesID, "error", err)
	}
	s.refreshSeriesEpisodeMetadataState(ctx, seriesID, time.Now())
}

// SynthesizeFallbackEpisodes is the stable public API for external callers
// (worker, admin jobs) to create scanner-derived season and episode rows for a
// series with unlinked files that have parseable SxxEyy numbers. It is safe to
// call for both unmatched and partially matched series. The private
// synthesizeFallbackSeriesStructure implementation may evolve; callers should
// use this method so their call-sites remain stable.
func (s *MetadataService) SynthesizeFallbackEpisodes(ctx context.Context, seriesID string) error {
	return s.withSeriesEpisodeWork(ctx, seriesID, func() error {
		return s.synthesizeFallbackSeriesStructure(ctx, seriesID)
	})
}

func (s *MetadataService) synthesizeFallbackSeriesStructure(ctx context.Context, seriesID string) error {
	item, err := s.itemRepo.GetByID(ctx, seriesID)
	if err != nil {
		return fmt.Errorf("loading series item: %w", err)
	}
	if item.Type != "series" {
		return nil
	}

	linker, ok := s.fileRepo.(EpisodeLinker)
	if !ok {
		return nil
	}

	files, err := linker.ListBySeriesUnlinked(ctx, seriesID)
	if err != nil {
		return fmt.Errorf("listing unlinked series files: %w", err)
	}

	seasonIDs := make(map[int]string)
	for _, file := range files {
		seasonNum, episodeNum, ok := fallbackEpisodeNumbers(file)
		if !ok {
			continue
		}

		seasonID, ok := seasonIDs[seasonNum]
		if !ok {
			existingSeason, err := s.seasonRepo.GetBySeriesAndNumber(ctx, seriesID, seasonNum)
			switch {
			case err == nil:
				seasonID = existingSeason.ContentID
				seasonIDs[seasonNum] = seasonID
			case errors.Is(err, catalog.ErrSeasonNotFound):
				seasonID, err = deriveSeasonContentID(seriesID, seasonNum)
				if err != nil {
					return fmt.Errorf("generating fallback season id: %w", err)
				}
				season := &models.Season{
					ContentID:      seasonID,
					SeriesID:       seriesID,
					SeasonNumber:   seasonNum,
					Title:          fallbackSeasonTitle(seasonNum),
					MetadataSource: "scanner_fallback",
				}
				if err := s.seasonRepo.Upsert(ctx, season); err != nil {
					return fmt.Errorf("upserting fallback season: %w", err)
				}
				seasonID = season.ContentID
				seasonIDs[seasonNum] = seasonID
			default:
				return fmt.Errorf("loading fallback season: %w", err)
			}
		}

		existingEpisode, err := s.episodeRepo.GetBySeriesAndNumber(ctx, seriesID, seasonNum, episodeNum)
		switch {
		case err == nil:
			if existingEpisode.SeasonID == seasonID {
				continue
			}
			existingEpisode.SeasonID = seasonID
			if err := s.episodeRepo.Upsert(ctx, existingEpisode); err != nil {
				return fmt.Errorf("updating fallback episode season link: %w", err)
			}
			continue
		case errors.Is(err, catalog.ErrEpisodeNotFound):
			episodeID, err := deriveEpisodeContentID(seriesID, seasonNum, episodeNum)
			if err != nil {
				return fmt.Errorf("generating fallback episode id: %w", err)
			}

			episode := &models.Episode{
				ContentID:      episodeID,
				SeriesID:       seriesID,
				SeasonID:       seasonID,
				SeasonNumber:   seasonNum,
				EpisodeNumber:  episodeNum,
				Title:          fallbackEpisodeTitle(episodeNum),
				MetadataSource: "scanner_fallback",
			}
			if err := s.episodeRepo.Upsert(ctx, episode); err != nil {
				return fmt.Errorf("upserting fallback episode: %w", err)
			}
		default:
			return fmt.Errorf("loading fallback episode: %w", err)
		}
	}

	if err := s.linkSeriesFilesToEpisodesWithOptions(ctx, seriesID, true); err != nil {
		return err
	}
	s.refreshSeriesEpisodeMetadataState(ctx, seriesID, time.Now())
	return nil
}

func (s *MetadataService) linkSeriesFilesToEpisodes(ctx context.Context, seriesID string) {
	if err := s.linkSeriesFilesToEpisodesWithOptions(ctx, seriesID, false); err == nil {
		s.refreshSeriesEpisodeMetadataState(ctx, seriesID, time.Now())
	}
}

func (s *MetadataService) ensureSeriesEpisodeLinks(ctx context.Context, seriesID string) error {
	return s.withSeriesEpisodeWork(ctx, seriesID, func() error {
		return s.ensureSeriesEpisodeLinksCore(ctx, seriesID)
	})
}

func (s *MetadataService) ensureSeriesEpisodeLinksCore(ctx context.Context, seriesID string) error {
	if s != nil && s.hooks.ensureSeriesEpisodeLinks != nil {
		return s.hooks.ensureSeriesEpisodeLinks(ctx, seriesID)
	}
	if s != nil && s.hooks.linkSeriesFilesToEpisodes != nil &&
		(s.itemRepo == nil || s.episodeRepo == nil || s.fileRepo == nil) {
		s.hooks.linkSeriesFilesToEpisodes(ctx, seriesID)
		return nil
	}

	item, err := s.itemRepo.GetByID(ctx, seriesID)
	if err != nil {
		return fmt.Errorf("loading series item: %w", err)
	}

	linker, ok := s.fileRepo.(EpisodeLinker)
	if !ok {
		return nil
	}

	files, err := linker.ListBySeriesUnlinked(ctx, seriesID)
	if err != nil {
		return fmt.Errorf("listing unlinked series files: %w", err)
	}
	if len(files) == 0 {
		return nil
	}

	needsSynthesis := false
	for _, file := range files {
		seasonNum, episodeNum, ok := fallbackEpisodeNumbers(file)
		if !ok {
			continue
		}
		if _, err := s.episodeRepo.GetBySeriesAndNumber(ctx, seriesID, seasonNum, episodeNum); errors.Is(err, catalog.ErrEpisodeNotFound) {
			needsSynthesis = true
			break
		}
	}

	if !needsSynthesis {
		if err := s.linkSeriesFilesToEpisodesWithOptions(ctx, seriesID, item.EpisodeMetadataIncomplete); err != nil {
			return err
		}
		s.refreshSeriesEpisodeMetadataState(ctx, seriesID, time.Now())
		return nil
	}

	slog.Info("metadata: entering fallback episode synthesis for incomplete series", "series_id", seriesID)
	if err := s.synthesizeFallbackSeriesStructure(ctx, seriesID); err != nil {
		return err
	}
	return nil
}

func (s *MetadataService) withSeriesEpisodeWork(ctx context.Context, seriesID string, fn func() error) error {
	if s == nil || seriesID == "" {
		return fn()
	}

	s.seriesWorkMu.Lock()
	if s.seriesWork == nil {
		s.seriesWork = make(map[string]*seriesEpisodeWork)
	}
	if work, ok := s.seriesWork[seriesID]; ok {
		s.seriesWorkMu.Unlock()
		select {
		case <-work.done:
			return work.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	work := &seriesEpisodeWork{done: make(chan struct{})}
	s.seriesWork[seriesID] = work
	s.seriesWorkMu.Unlock()

	work.err = fn()
	close(work.done)

	s.seriesWorkMu.Lock()
	delete(s.seriesWork, seriesID)
	s.seriesWorkMu.Unlock()

	return work.err
}

func (s *MetadataService) linkSeriesFilesToEpisodesWithOptions(ctx context.Context, seriesID string, suppressMissing bool) error {
	if s != nil && s.hooks.linkSeriesFilesToEpisodes != nil {
		s.hooks.linkSeriesFilesToEpisodes(ctx, seriesID)
		return nil
	}

	linker, ok := s.fileRepo.(EpisodeLinker)
	if !ok {
		return nil
	}

	if bulkLinker, ok := s.fileRepo.(bulkSeriesEpisodeLinker); ok {
		if _, err := bulkLinker.BulkLinkEpisodesBySeries(ctx, seriesID); err != nil {
			return fmt.Errorf("bulk-linking series files to episodes: %w", err)
		}
	}

	files, err := linker.ListBySeriesUnlinked(ctx, seriesID)
	if err != nil {
		return fmt.Errorf("loading unlinked series files: %w", err)
	}

	hints := make(map[int]episodeLinkHint, len(files))
	airDateSet := make(map[string]struct{})
	for _, file := range files {
		hint := parseEpisodeLinkHint(file)
		if !hint.ok {
			continue
		}
		hints[file.ID] = hint
		if hint.airDate != "" {
			airDateSet[hint.airDate] = struct{}{}
		}
	}

	episodesByAirDate := map[string][]*models.Episode{}
	var seriesItem *models.MediaItem
	if len(airDateSet) > 0 {
		airDates := make([]string, 0, len(airDateSet))
		for airDate := range airDateSet {
			airDates = append(airDates, airDate)
		}
		sort.Strings(airDates)
		var lookupErr error
		episodesByAirDate, lookupErr = s.episodeRepo.ListBySeriesAndAirDates(ctx, seriesID, airDates)
		if lookupErr != nil {
			return fmt.Errorf("loading episodes by air date: %w", lookupErr)
		}
		if s.itemRepo != nil {
			if item, itemErr := s.itemRepo.GetByID(ctx, seriesID); itemErr == nil {
				seriesItem = item
			} else if !errors.Is(itemErr, catalog.ErrItemNotFound) {
				slog.Warn("metadata: failed to load series item for air-date episode preference",
					"series_id", seriesID,
					"error", itemErr)
			}
		}
	}

	for _, file := range files {
		hint, ok := hints[file.ID]
		if !ok {
			continue
		}

		var episode *models.Episode
		seasonNum := hint.seasonNum
		episodeNum := hint.episodeNum
		if hint.airDate != "" {
			candidates := episodesByAirDate[hint.airDate]
			selected, ok := selectAirDateEpisodeCandidate(candidates, seriesItem)
			if !ok {
				if len(candidates) > 1 {
					slog.Warn("metadata: skipped ambiguous air-date episode link",
						"series_id", seriesID,
						"file_id", file.ID,
						"file_path", file.FilePath,
						"air_date", hint.airDate,
						"matches", len(candidates))
				}
				continue
			}
			episode = selected
			seasonNum = episode.SeasonNumber
			episodeNum = episode.EpisodeNumber
		} else {
			var err error
			episode, err = s.episodeRepo.GetBySeriesAndNumber(ctx, seriesID, seasonNum, episodeNum)
			if err != nil {
				if suppressMissing && errors.Is(err, catalog.ErrEpisodeNotFound) {
					continue
				}
				slog.Warn("metadata: failed to resolve episode for file",
					"series_id", seriesID,
					"file_id", file.ID,
					"file_path", file.FilePath,
					"season", seasonNum,
					"episode", episodeNum,
					"error", err)
				continue
			}
		}

		if err := linker.UpdateEpisodeLink(ctx, file.ID, episode.ContentID, seasonNum, episodeNum); err != nil {
			slog.Warn("metadata: failed to link file to episode",
				"series_id", seriesID,
				"file_id", file.ID,
				"file_path", file.FilePath,
				"episode_id", episode.ContentID,
				"error", err)
		}
	}
	return nil
}

func selectAirDateEpisodeCandidate(candidates []*models.Episode, seriesItem *models.MediaItem) (*models.Episode, bool) {
	if len(candidates) == 0 {
		return nil, false
	}
	if len(candidates) == 1 {
		return candidates[0], true
	}
	for _, provider := range preferredEpisodeProviders(seriesItem) {
		filtered := filterEpisodesByProviderID(candidates, provider)
		if len(filtered) == 1 {
			return filtered[0], true
		}
		if len(filtered) > 1 {
			return nil, false
		}
	}
	return nil, false
}

func preferredEpisodeProviders(seriesItem *models.MediaItem) []string {
	if seriesItem == nil {
		return nil
	}
	providers := make([]string, 0, 3)
	if strings.TrimSpace(seriesItem.TvdbID) != "" {
		providers = append(providers, "tvdb")
	}
	if strings.TrimSpace(seriesItem.TmdbID) != "" {
		providers = append(providers, "tmdb")
	}
	if strings.TrimSpace(seriesItem.ImdbID) != "" {
		providers = append(providers, "imdb")
	}
	return providers
}

func filterEpisodesByProviderID(candidates []*models.Episode, provider string) []*models.Episode {
	filtered := make([]*models.Episode, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate == nil {
			continue
		}
		switch provider {
		case "tvdb":
			if strings.TrimSpace(candidate.TvdbID) != "" {
				filtered = append(filtered, candidate)
			}
		case "tmdb":
			if strings.TrimSpace(candidate.TmdbID) != "" {
				filtered = append(filtered, candidate)
			}
		case "imdb":
			if strings.TrimSpace(candidate.ImdbID) != "" {
				filtered = append(filtered, candidate)
			}
		}
	}
	return filtered
}

type episodeLinkHint struct {
	seasonNum  int
	episodeNum int
	airDate    string
	ok         bool
}

func (s *MetadataService) updateEpisodeMetadataState(ctx context.Context, seriesID string, incomplete bool, lastCheckedAt *time.Time) {
	item, err := s.itemRepo.GetByID(ctx, seriesID)
	if err != nil {
		slog.Warn("metadata: failed to load series item for episode metadata state",
			"series_id", seriesID, "error", err)
		return
	}

	item.EpisodeMetadataIncomplete = incomplete
	item.EpisodeMetadataLastCheckedAt = lastCheckedAt
	if err := s.itemRepo.Upsert(ctx, item); err != nil {
		slog.Warn("metadata: failed to update episode metadata state",
			"series_id", seriesID, "error", err)
	}
}

func (s *MetadataService) refreshSeriesEpisodeMetadataState(ctx context.Context, seriesID string, now time.Time) {
	if s == nil || s.episodeRepo == nil {
		return
	}

	episodes, err := s.episodeRepo.ListBySeries(ctx, seriesID)
	if err != nil {
		slog.Warn("metadata: failed to list series episodes for completeness check",
			"series_id", seriesID, "error", err)
		return
	}

	incomplete := false
	for _, episode := range episodes {
		if EpisodeHasActionableMetadataDebt(episode, now) {
			incomplete = true
		}
		if err := s.syncVisibleEpisodeRefreshDebt(ctx, episode, now); err != nil {
			slog.Warn("metadata: failed to sync episode refresh debt",
				"series_id", seriesID,
				"episode_id", episode.ContentID,
				"error", err)
		}
	}

	lastCheckedAt := now
	s.updateEpisodeMetadataState(ctx, seriesID, incomplete, &lastCheckedAt)
}

func (s *MetadataService) syncVisibleEpisodeRefreshDebt(ctx context.Context, episode *models.Episode, now time.Time) error {
	if s == nil || s.refreshDebtRepo == nil || episode == nil || strings.TrimSpace(episode.ContentID) == "" {
		return nil
	}
	if !EpisodeHasActionableMetadataDebt(episode, now) {
		return s.refreshDebtRepo.DeleteTargetDebt(ctx, RefreshTargetEpisode, episode.ContentID)
	}
	reasonMask := RefreshDebtReasonEpisodeIncomplete
	attemptCount, err := s.currentRefreshDebtTargetAttemptCount(ctx, RefreshTargetEpisode, episode.ContentID)
	if err != nil {
		return err
	}
	logRefreshDebtTerminal(RefreshTargetEpisode, episode.ContentID, reasonMask, attemptCount)
	return s.refreshDebtRepo.MarkTargetSuccess(
		ctx,
		RefreshTargetEpisode,
		episode.ContentID,
		effectiveRefreshDebtPriority(reasonMask, attemptCount),
		reasonMask,
		nextRefreshAtForDebt(reasonMask, attemptCount, now.UTC()),
	)
}

func fallbackEpisodeNumbers(file *models.MediaFile) (seasonNum int, episodeNum int, ok bool) {
	hint := parseEpisodeLinkHint(file)
	if !hint.ok || hint.airDate != "" {
		return 0, 0, false
	}
	return hint.seasonNum, hint.episodeNum, true
}

func parseEpisodeLinkHint(file *models.MediaFile) episodeLinkHint {
	if file == nil {
		return episodeLinkHint{}
	}
	if file.SeasonNumber != 0 && file.EpisodeNumber != 0 {
		return episodeLinkHint{seasonNum: file.SeasonNumber, episodeNum: file.EpisodeNumber, ok: true}
	}

	fnh := naming.ParseFilename(file.FilePath, "series")
	if fnh == nil {
		return episodeLinkHint{}
	}
	if fnh.EpisodeNum != 0 {
		return episodeLinkHint{seasonNum: fnh.SeasonNum, episodeNum: fnh.EpisodeNum, ok: true}
	}
	if fnh.AirDate != "" {
		return episodeLinkHint{airDate: fnh.AirDate, ok: true}
	}
	return episodeLinkHint{}
}

func fallbackSeasonTitle(seasonNum int) string {
	if seasonNum == 0 {
		return "Specials"
	}
	return fmt.Sprintf("Season %d", seasonNum)
}

func fallbackEpisodeTitle(episodeNum int) string {
	return fmt.Sprintf("Episode %d", episodeNum)
}

// findExistingByProviderIDs checks if an item with matching provider IDs exists.
func (s *MetadataService) findExistingByProviderIDs(
	ctx context.Context,
	ids map[string]string,
	itemType string,
	excludeContentID string,
) (*models.MediaItem, error) {
	if s == nil || s.itemRepo == nil {
		return nil, nil
	}
	if s != nil && s.providerIDRepo != nil {
		contentID, err := s.providerIDRepo.FindContentIDByProviderIDs(ctx, ids, itemType, excludeContentID)
		if err != nil {
			return nil, err
		}
		if contentID != "" {
			item, err := s.itemRepo.GetByID(ctx, contentID)
			if err != nil {
				return nil, err
			}
			return item, nil
		}
	}

	tmdbID := ids["tmdb"]
	imdbID := ids["imdb"]
	tvdbID := ids["tvdb"]
	if tmdbID != "" || imdbID != "" || tvdbID != "" {
		if item, err := s.itemRepo.GetByExternalID(ctx, tmdbID, imdbID, tvdbID, itemType); err == nil {
			if excludeContentID != "" && item.ContentID == excludeContentID {
				return nil, nil
			}
			return item, nil
		}
	}
	return nil, nil
}

// skeletonResult carries the outcome of skeleton creation plus parsed hints so
// the caller can seed them into the enrichment pipeline.
type skeletonResult struct {
	ContentID        string
	IsNew            bool
	ItemStatus       string
	RootPath         string
	ObservedRootPath string
	GroupKeyVersion  int
	ContentGroupKey  string
	Title            string
	Year             int
	Type             string
	TmdbID           string
	ImdbID           string
	TvdbID           string
}

func hasFolderIDHints(hints *naming.FolderIDHints) bool {
	return hints != nil && (hints.TmdbID != "" || hints.ImdbID != "" || hints.TvdbID != "")
}

func mergeFolderIDHints(base, override *naming.FolderIDHints) *naming.FolderIDHints {
	if !hasFolderIDHints(base) && !hasFolderIDHints(override) {
		return nil
	}

	merged := &naming.FolderIDHints{}
	if base != nil {
		merged.TmdbID = base.TmdbID
		merged.ImdbID = base.ImdbID
		merged.TvdbID = base.TvdbID
	}
	if override != nil {
		if override.TmdbID != "" {
			merged.TmdbID = override.TmdbID
		}
		if override.ImdbID != "" {
			merged.ImdbID = override.ImdbID
		}
		if override.TvdbID != "" {
			merged.TvdbID = override.TvdbID
		}
	}
	if !hasFolderIDHints(merged) {
		return nil
	}
	return merged
}

func applyFolderIDHints(res *skeletonResult, hints *naming.FolderIDHints) {
	if res == nil || hints == nil {
		return
	}
	if hints.TmdbID != "" {
		res.TmdbID = hints.TmdbID
	}
	if hints.ImdbID != "" {
		res.ImdbID = hints.ImdbID
	}
	if hints.TvdbID != "" {
		res.TvdbID = hints.TvdbID
	}
}

func providerIDsFromSkeletonResult(res *skeletonResult) map[string]string {
	if res == nil {
		return nil
	}

	providerIDs := make(map[string]string, 3)
	if value := strings.TrimSpace(res.TmdbID); value != "" {
		providerIDs["tmdb"] = value
	}
	if value := strings.TrimSpace(res.ImdbID); value != "" {
		providerIDs["imdb"] = value
	}
	if value := strings.TrimSpace(res.TvdbID); value != "" {
		providerIDs["tvdb"] = value
	}
	if len(providerIDs) == 0 {
		return nil
	}
	return providerIDs
}

func trustedStructuredIDsForSkeleton(filePath, observedRootPath, contentRootPath string) *naming.FolderIDHints {
	folderIDs := naming.ParseStructuredFolderIDs(filepath.Base(observedRootPath))
	if folderIDs == nil && contentRootPath != "" && contentRootPath != observedRootPath {
		folderIDs = naming.ParseStructuredFolderIDs(filepath.Base(contentRootPath))
	}
	fileIDs := naming.ParseStructuredFolderIDs(strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath)))
	return mergeFolderIDHints(folderIDs, fileIDs)
}

// createOrFindSkeleton creates a skeleton media_items row or finds an existing
// item to link the file to. Returns a skeletonResult with parsed hints.
//
// Dedup order: group claim, then root claim, then explicit external IDs, then
// title/year fallback. When a winner is found its current ownership is reused
// and the relevant group/root claims are refreshed.
//
// Missing folder IDs: roots without embedded provider IDs (e.g. {tmdb-12345})
// are still recorded in skipped_media_roots for admin diagnostics, but they
// are no longer skipped — a skeleton item is created and entered the match
// queue with status "pending".
func (s *MetadataService) createOrFindSkeleton(ctx context.Context, file *models.MediaFile, folderID int) (*skeletonResult, error) {
	if s != nil && s.hooks.createOrFindSkeleton != nil {
		return s.hooks.createOrFindSkeleton(ctx, file, folderID)
	}

	libraryType, err := s.folderTypeForSkeleton(ctx, folderID)
	if err != nil {
		return nil, err
	}
	libraryTypeNorm := strings.ToLower(strings.TrimSpace(libraryType))

	contentRootPath := filepath.Dir(file.FilePath)
	if file.CanonicalRootPath != "" {
		contentRootPath = filepath.Clean(file.CanonicalRootPath)
	}
	observedRootPath := filepath.Dir(file.FilePath)
	if file.ObservedRootPath != "" {
		observedRootPath = filepath.Clean(file.ObservedRootPath)
	}
	groupKeyVersion := file.GroupKeyVersion
	if groupKeyVersion == 0 {
		groupKeyVersion = naming.ContentGroupKeyVersion
	}
	contentGroupKey := strings.TrimSpace(file.ContentGroupKey)
	// Build result with parsed hints (always returned regardless of dedup outcome).
	res := &skeletonResult{
		RootPath:         contentRootPath,
		ObservedRootPath: observedRootPath,
		GroupKeyVersion:  groupKeyVersion,
		ContentGroupKey:  contentGroupKey,
		ItemStatus:       "pending",
		Title:            file.BaseTitle,
		Year:             file.BaseYear,
		Type:             file.BaseType,
	}
	if s.scannedGroupRepo != nil && contentGroupKey != "" {
		scannedGroup, err := s.scannedGroupRepo.Get(ctx, folderID, groupKeyVersion, contentGroupKey)
		if err != nil {
			slog.Warn("metadata: scanned group lookup failed",
				"folder_id", folderID,
				"group_key_version", groupKeyVersion,
				"content_group_key", contentGroupKey,
				"error", err,
			)
		} else if scannedGroup != nil {
			if scannedGroup.BaseTitle != "" {
				res.Title = scannedGroup.BaseTitle
			}
			if scannedGroup.BaseYear != 0 {
				res.Year = scannedGroup.BaseYear
			}
			if scannedGroup.InferredType != "" {
				res.Type = scannedGroup.InferredType
			}
			if scannedGroup.TmdbID != "" {
				res.TmdbID = scannedGroup.TmdbID
			}
			if scannedGroup.ImdbID != "" {
				res.ImdbID = scannedGroup.ImdbID
			}
			if scannedGroup.TvdbID != "" {
				res.TvdbID = scannedGroup.TvdbID
			}
			if scannedGroup.State == "ambiguous" {
				res.ItemStatus = "ambiguous"
			}
			if scannedGroup.SampleObservedRootPath != "" {
				res.ObservedRootPath = scannedGroup.SampleObservedRootPath
			}
		}
	}
	if s.groupOverrideRepo != nil && contentGroupKey != "" {
		override, err := s.groupOverrideRepo.Get(ctx, folderID, groupKeyVersion, contentGroupKey)
		if err != nil {
			slog.Warn("metadata: group override lookup failed",
				"folder_id", folderID,
				"group_key_version", groupKeyVersion,
				"content_group_key", contentGroupKey,
				"error", err,
			)
		} else if override != nil {
			if override.ForcedType != "" {
				res.Type = override.ForcedType
			}
			if override.ForcedTitle != "" {
				res.Title = override.ForcedTitle
			}
			if override.ForcedYear > 0 {
				res.Year = override.ForcedYear
			}
			if override.ForcedTmdbID != "" {
				res.TmdbID = override.ForcedTmdbID
			}
			if override.ForcedImdbID != "" {
				res.ImdbID = override.ForcedImdbID
			}
			if override.ForcedTvdbID != "" {
				res.TvdbID = override.ForcedTvdbID
			}
			res.ItemStatus = "pending"
		}
	}
	if res.Type == "" {
		switch libraryTypeNorm {
		case "series", "tv", "show", "tvshows":
			res.Type = "series"
		case "movie", "movies":
			res.Type = "movie"
		case "mixed":
			if file.EpisodeID != "" || file.SeasonNumber != 0 || file.EpisodeNumber != 0 {
				res.Type = "series"
			}
		}
	}
	if res.Type == "" {
		res.Type = "movie"
	}

	// Explicit structured IDs from the file or folder are treated as trusted.
	// When present, they override scanner ambiguity and become the authoritative
	// external IDs used for dedup/link/create. Filename tags take precedence
	// over folder tags because the file is generally the freshest artifact.
	trustedIDs := trustedStructuredIDsForSkeleton(file.FilePath, observedRootPath, contentRootPath)
	if trustedIDs != nil {
		applyFolderIDHints(res, trustedIDs)
		res.ItemStatus = "pending"
	}

	// Parse observed location names for external IDs before falling back to the
	// legacy canonical root path. This preserves existing heuristic behavior for
	// roots without explicit structured tags.
	folderIDs := naming.ParseFolderIDs(filepath.Base(observedRootPath))
	if folderIDs == nil && contentRootPath != "" && contentRootPath != observedRootPath {
		folderIDs = naming.ParseFolderIDs(filepath.Base(contentRootPath))
	}

	effectiveExternalIDs := folderIDs
	if trustedIDs != nil {
		effectiveExternalIDs = trustedIDs
	}
	if effectiveExternalIDs != nil {
		if trustedIDs == nil {
			applyFolderIDHints(res, folderIDs)
		}
		// Clear any legacy skipped-root record since we now have provider IDs.
		if s != nil && s.skippedRootRepo != nil {
			if err := s.skippedRootRepo.Delete(ctx, folderID, contentRootPath); err != nil {
				slog.Warn("metadata: failed to clear skipped root",
					"folder_id", folderID,
					"root_path", observedRootPath,
					"error", err)
			}
		}
	}
	// Misplaced-TV guard: a strict movie-type library should never turn a TV
	// episode living inside a "Season NN/" (or "Specials") directory into a
	// per-episode "Season NN" movie item. Such trees are series dropped into a
	// movie library (e.g. fan supercut packs) and never match a movie provider;
	// creating an item per episode just pollutes the catalog. Record the root
	// for admin visibility and skip skeleton creation; the movie match queue
	// excludes files beneath such roots so they are not re-enqueued on every
	// sync (see movieQueueFileEligibleCond). "mixed" libraries are left
	// untouched because they legitimately host series.
	//
	// This fires on the structural signal alone (season dir + SxxExx), even when
	// a provider id was parsed — a "Season NN" folder otherwise yields a bogus
	// id (the season number, e.g. tmdb="01"), so effectiveExternalIDs must NOT
	// gate the skip.
	if (libraryTypeNorm == "movie" || libraryTypeNorm == "movies") &&
		naming.IsMisplacedSeriesFile(file.FilePath) {
		s.recordSkippedRoot(ctx, folderID, observedRootPath, skippedReasonSeriesInMovieLibrary, file.FilePath)
		res.ItemStatus = "skipped"
		return res, nil
	}
	if effectiveExternalIDs == nil {
		// Record for admin diagnostics only — no longer bail out.
		s.recordSkippedRoot(ctx, folderID, observedRootPath, skippedReasonMissingFolderIDs, file.FilePath)
	}

	unlockDedup := s.lockDedupKey(dedupKeyForSkeleton(
		res.Type,
		providerIDsFromSkeletonResult(res),
		res.Title,
		res.Year,
		folderID,
		groupKeyVersion,
		contentGroupKey,
		contentRootPath,
		file.FilePath,
	))
	defer unlockDedup()

	// Dedup 1: confirmed content-group ownership always wins, including for
	// movies. Provisional claims are intentionally ignored.
	if contentGroupKey != "" && s.groupClaimRepo != nil {
		claimedGroup, err := s.groupClaimRepo.Get(ctx, folderID, groupKeyVersion, contentGroupKey)
		if err != nil {
			return nil, fmt.Errorf("loading claimed content group: %w", err)
		}
		if claimedGroup != nil {
			if existing, ok := s.confirmedOwnershipItem(ctx, claimedGroup.ContentID); ok {
				if _, linkErr := s.claimGroupAndRelink(ctx, folderID, groupKeyVersion, contentGroupKey, contentRootPath, existing.ContentID); linkErr != nil {
					return nil, fmt.Errorf("claiming confirmed content group: %w", linkErr)
				}
				if linkErr := s.fileRepo.UpdateContentID(ctx, file.ID, existing.ContentID); linkErr != nil {
					return nil, fmt.Errorf("linking file to confirmed group item: %w", linkErr)
				}
				if err := s.upsertLibraryMembership(ctx, existing.ContentID, folderID); err != nil {
					s.logLibraryMembershipError("upserting confirmed group membership", existing.ContentID, folderID, err)
				}
				res.ContentID = existing.ContentID
				return res, nil
			}
		}
	}

	// Dedup 2: confirmed root ownership also wins for both movies and series.
	if contentRootPath != "" && s.rootClaimRepo != nil {
		claimedRoot, err := s.rootClaimRepo.Get(ctx, folderID, contentRootPath)
		if err != nil {
			return nil, fmt.Errorf("loading claimed root path: %w", err)
		}
		if claimedRoot != nil {
			if existing, ok := s.confirmedOwnershipItem(ctx, claimedRoot.ContentID); ok {
				s.claimRoot(ctx, folderID, contentRootPath, existing.ContentID)
				if linkErr := s.fileRepo.UpdateContentID(ctx, file.ID, existing.ContentID); linkErr != nil {
					return nil, fmt.Errorf("linking file to confirmed root item: %w", linkErr)
				}
				if err := s.upsertLibraryMembership(ctx, existing.ContentID, folderID); err != nil {
					s.logLibraryMembershipError("upserting confirmed root membership", existing.ContentID, folderID, err)
				}
				res.ContentID = existing.ContentID
				return res, nil
			}
		}
	}

	// Dedup 3: same observed TV root reuses the already-linked root-scoped item.
	if res.Type == "series" {
		res.Type = "series"
		existingContentID, err := s.fileRepo.FindContentIDByObservedRootPath(ctx, folderID, observedRootPath, "series")
		if err != nil {
			return nil, fmt.Errorf("finding existing item by observed root path: %w", err)
		}
		if existingContentID != "" {
			if linkErr := s.fileRepo.UpdateContentID(ctx, file.ID, existingContentID); linkErr != nil {
				return nil, fmt.Errorf("linking file to existing root item: %w", linkErr)
			}
			if err := s.upsertLibraryMembership(ctx, existingContentID, folderID); err != nil {
				s.logLibraryMembershipError("upserting existing root item membership", existingContentID, folderID, err)
			}
			res.ContentID = existingContentID
			return res, nil
		}
	}

	// Dedup 4: Check by external IDs from trusted file/folder tags first, then
	// fall back to the legacy folder/root parsing behavior.
	if effectiveExternalIDs != nil {
		existing, err := s.itemRepo.GetByExternalID(ctx, effectiveExternalIDs.TmdbID, effectiveExternalIDs.ImdbID, effectiveExternalIDs.TvdbID, res.Type)
		if err == nil && existing != nil {
			if (res.Type == "movie" || res.Type == "series") && !isConfirmedOwnershipStatus(existing.Status) {
				existing = nil
			}
		}
		if existing != nil {
			if isConfirmedOwnershipStatus(existing.Status) {
				if _, linkErr := s.claimGroupAndRelink(ctx, folderID, groupKeyVersion, contentGroupKey, contentRootPath, existing.ContentID); linkErr != nil {
					return nil, fmt.Errorf("claiming existing group: %w", linkErr)
				}
			}
			if linkErr := s.fileRepo.UpdateContentID(ctx, file.ID, existing.ContentID); linkErr != nil {
				return nil, fmt.Errorf("linking file to existing item: %w", linkErr)
			}
			if err := s.upsertLibraryMembership(ctx, existing.ContentID, folderID); err != nil {
				s.logLibraryMembershipError("upserting existing external-id membership", existing.ContentID, folderID, err)
			}
			res.ContentID = existing.ContentID
			return res, nil
		}
	}

	// Scanner skeleton creation intentionally avoids fuzzy title/year dedup and
	// pre-confirmation ownership claims. Matching is driven by explicit IDs or
	// later provider confirmation to avoid cross-root false merges.

	// No existing item — create skeleton. Re-check the folder state while still
	// under the dedup lock so a library disabled for deletion cannot receive new
	// skeleton items from a matcher that passed an older outer enabled check.
	if _, err := s.folderTypeForSkeleton(ctx, folderID); err != nil {
		return nil, err
	}
	// Derive a deterministic content_id from the structured provider tags the
	// scanner already parsed from the folder/file name (the common case for
	// Radarr/Sonarr/Plex-tagged libraries), so two servers seeing the same tags
	// mint the same id at scan time with no provider API call. Untagged files
	// fall back to a per-file local: id (keyed on the file path, not the folder)
	// so distinct skeletons in a shared folder stay separate until the dedup
	// machinery confirms a real shared identity — matching the pre-existing
	// one-skeleton-per-file behavior while staying stable across rescans.
	localAnchorPath := firstNonEmpty(file.FilePath, contentRootPath, observedRootPath)
	contentID, err := deriveLogicalContentID(
		res.Type,
		contentid.ProviderIDs{Tmdb: res.TmdbID, Imdb: res.ImdbID, Tvdb: res.TvdbID},
		localAnchorPath,
	)
	if err != nil {
		return nil, fmt.Errorf("generate content id: %w", err)
	}
	item := &models.MediaItem{
		ContentID: contentID,
		Status:    res.ItemStatus,
		Studios:   []string{},
		Networks:  []string{},
		Countries: []string{},
		Genres:    []string{},
		Keywords:  []string{},
	}
	item.Title = res.Title
	item.Year = res.Year
	item.Type = res.Type
	item.TmdbID = res.TmdbID
	item.ImdbID = res.ImdbID
	item.TvdbID = res.TvdbID

	if err := s.itemRepo.Upsert(ctx, item); err != nil {
		return nil, fmt.Errorf("creating skeleton item: %w", err)
	}

	// Link file, claim root, and create library membership.
	if err := s.fileRepo.UpdateContentID(ctx, file.ID, contentID); err != nil {
		if cleanupErr := s.deleteCreatedSkeleton(ctx, contentID); cleanupErr != nil {
			slog.Warn("metadata: failed to clean up unlinked skeleton",
				"content_id", contentID,
				"file_id", file.ID,
				"folder_id", folderID,
				"error", cleanupErr,
			)
		}
		return nil, fmt.Errorf("linking file to skeleton: %w", err)
	}
	if _, err := s.folderTypeForSkeleton(ctx, folderID); err != nil {
		if clearErr := s.fileRepo.UpdateContentID(ctx, file.ID, ""); clearErr != nil {
			slog.Warn("metadata: failed to clear file link after disabled skeleton folder",
				"content_id", contentID,
				"file_id", file.ID,
				"folder_id", folderID,
				"error", clearErr,
			)
		}
		if cleanupErr := s.deleteCreatedSkeleton(ctx, contentID); cleanupErr != nil {
			slog.Warn("metadata: failed to clean up skeleton for disabled folder",
				"content_id", contentID,
				"file_id", file.ID,
				"folder_id", folderID,
				"error", cleanupErr,
			)
		}
		return nil, fmt.Errorf("validating skeleton folder before membership: %w", err)
	}
	if err := s.upsertLibraryMembership(ctx, contentID, folderID); err != nil {
		if clearErr := s.fileRepo.UpdateContentID(ctx, file.ID, ""); clearErr != nil {
			slog.Warn("metadata: failed to clear file link after skeleton membership failure",
				"content_id", contentID,
				"file_id", file.ID,
				"folder_id", folderID,
				"error", clearErr,
			)
		}
		if cleanupErr := s.deleteCreatedSkeleton(ctx, contentID); cleanupErr != nil {
			slog.Warn("metadata: failed to clean up membershipless skeleton",
				"content_id", contentID,
				"file_id", file.ID,
				"folder_id", folderID,
				"error", cleanupErr,
			)
		}
		return nil, fmt.Errorf("upserting skeleton membership: %w", err)
	}

	res.ContentID = contentID
	res.IsNew = true
	return res, nil
}

func (s *MetadataService) folderTypeForSkeleton(ctx context.Context, folderID int) (string, error) {
	if s == nil || s.folderRepo == nil || folderID <= 0 {
		return "", nil
	}

	folder, err := s.folderRepo.GetByID(ctx, folderID)
	if err != nil {
		return "", fmt.Errorf("loading folder context for skeleton: %w", err)
	}
	if folder == nil {
		return "", fmt.Errorf("folder %d is unavailable for skeleton creation", folderID)
	}
	if !folder.Enabled {
		return "", fmt.Errorf("folder %d is disabled for skeleton creation", folderID)
	}
	return folder.Type, nil
}

// recordSkippedRoot records the root of file for admin diagnostics in
// skipped_media_roots. Failures are logged and swallowed: diagnostics must
// never block skeleton creation.
func (s *MetadataService) recordSkippedRoot(ctx context.Context, folderID int, rootPath, reason, sampleFilePath string) {
	if s == nil || s.skippedRootRepo == nil {
		return
	}
	if err := s.skippedRootRepo.UpsertObservedFile(ctx, folderID, rootPath, reason, sampleFilePath); err != nil {
		slog.Warn("metadata: failed to record skipped root",
			"folder_id", folderID,
			"root_path", rootPath,
			"reason", reason,
			"error", err)
	}
}

func (s *MetadataService) deleteCreatedSkeleton(ctx context.Context, contentID string) error {
	if s == nil || strings.TrimSpace(contentID) == "" {
		return nil
	}
	if repo, ok := s.itemRepo.(metadataItemDeleteRepo); ok {
		if _, err := repo.Delete(ctx, contentID); err != nil && !errors.Is(err, catalog.ErrItemNotFound) {
			return err
		}
		return nil
	}
	if s.dbPool == nil {
		return nil
	}
	tx, err := s.dbPool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
		DELETE FROM media_items
		WHERE content_id = $1
		  AND NOT EXISTS (
			SELECT 1 FROM media_item_libraries mil
			WHERE mil.content_id = media_items.content_id
		  )`, contentID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		if err := catalog.EnqueueSearchIndexDelete(ctx, tx, contentID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// claimRoot records a canonical root path claim for dedup. Failures are logged
// but not fatal — the root claim is an optimization, not a hard requirement.
func (s *MetadataService) claimRoot(ctx context.Context, folderID int, rootPath, contentID string) {
	if s == nil || s.rootClaimRepo == nil {
		return
	}
	if err := s.rootClaimRepo.ClaimRoot(ctx, folderID, rootPath, contentID); err != nil {
		slog.Warn("metadata: failed to claim root path",
			"folder_id", folderID,
			"root_path", rootPath,
			"content_id", contentID,
			"error", err)
	}
}

func (s *MetadataService) claimConfirmedSeriesRootOwnership(
	ctx context.Context,
	folderID int,
	rootPath string,
	contentID string,
	files []*models.MediaFile,
) {
	if s == nil || strings.TrimSpace(contentID) == "" {
		return
	}
	if rootPath != "" {
		s.claimRoot(ctx, folderID, rootPath, contentID)
	}
	if s.groupClaimRepo == nil {
		return
	}

	seenGroups := make(map[string]struct{})
	for _, file := range files {
		if file == nil || file.MediaFolderID != folderID {
			continue
		}
		groupKey := strings.TrimSpace(file.ContentGroupKey)
		if file.GroupKeyVersion <= 0 || groupKey == "" {
			continue
		}
		claimKey := fmt.Sprintf("%d:%s", file.GroupKeyVersion, groupKey)
		if _, ok := seenGroups[claimKey]; ok {
			continue
		}
		seenGroups[claimKey] = struct{}{}
		if err := s.groupClaimRepo.ClaimGroup(ctx, folderID, file.GroupKeyVersion, groupKey, contentID); err != nil {
			slog.Warn("metadata: failed to claim confirmed series group",
				"folder_id", folderID,
				"group_key_version", file.GroupKeyVersion,
				"content_group_key", groupKey,
				"content_id", contentID,
				"error", err)
		}
	}
}

func (s *MetadataService) claimGroupAndRelink(
	ctx context.Context,
	folderID int,
	groupKeyVersion int,
	contentGroupKey string,
	rootPath string,
	contentID string,
) (int, error) {
	if s != nil && s.rootClaimRepo != nil && rootPath != "" {
		s.claimRoot(ctx, folderID, rootPath, contentID)
	}
	if s == nil || s.groupClaimRepo == nil || contentGroupKey == "" {
		return 0, nil
	}
	return s.groupClaimRepo.ClaimAndRelinkFiles(ctx, folderID, groupKeyVersion, contentGroupKey, contentID)
}

// updateItemStatus sets the status field on a media_items row.
func (s *MetadataService) updateItemStatus(ctx context.Context, contentID, status string) error {
	if s != nil && s.hooks.updateItemStatus != nil {
		return s.hooks.updateItemStatus(ctx, contentID, status)
	}
	if s == nil || s.itemRepo == nil {
		return fmt.Errorf("metadata item repository is not configured")
	}
	if strings.TrimSpace(contentID) == "" {
		return fmt.Errorf("content id is required to update item status")
	}

	existing, err := s.itemRepo.GetByID(ctx, contentID)
	if err != nil {
		return fmt.Errorf("loading item %s before status update: %w", contentID, err)
	}
	existing.Status = status
	if err := s.itemRepo.Upsert(ctx, existing); err != nil {
		return fmt.Errorf("upserting item %s with status %s: %w", contentID, status, err)
	}
	return nil
}

// upsertLibraryMembership creates a library membership for the given item
// regardless of status. Pending and unmatched items are now visible in library
// browse results so users can see and manually match them.
func (s *MetadataService) upsertLibraryMembership(ctx context.Context, contentID string, folderID int) error {
	if s == nil || s.libraryRepo == nil || folderID <= 0 || contentID == "" {
		return nil
	}
	return s.libraryRepo.Upsert(ctx, contentID, folderID, time.Now())
}

// upsertLibraryMembershipIfMatched creates a library membership only when the
// item has status "matched". This is kept for the mergeAndPersist flow where
// the item was just enriched and is known to be matched.
func (s *MetadataService) upsertLibraryMembershipIfMatched(ctx context.Context, contentID string, folderID int) error {
	if s == nil || s.libraryRepo == nil || s.itemRepo == nil || folderID <= 0 || contentID == "" {
		return nil
	}

	item, err := s.itemRepo.GetByID(ctx, contentID)
	if err != nil {
		return err
	}
	if item.Status != "matched" {
		return nil
	}

	return s.libraryRepo.Upsert(ctx, contentID, folderID, time.Now())
}

func (s *MetadataService) logLibraryMembershipError(action, contentID string, folderID int, err error) {
	if err == nil {
		return
	}
	slog.Warn("metadata: library membership upsert failed",
		"action", action,
		"content_id", contentID,
		"folder_id", folderID,
		"error", err,
	)
}

func isSkeletonLikeStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "pending", "unmatched":
		return true
	default:
		return false
	}
}

func isProvisionalOwnershipStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "pending", "unmatched", "ambiguous":
		return true
	default:
		return false
	}
}

func isConfirmedOwnershipStatus(status string) bool {
	return strings.EqualFold(strings.TrimSpace(status), "matched")
}

func (s *MetadataService) confirmedOwnershipItem(ctx context.Context, contentID string) (*models.MediaItem, bool) {
	if s == nil || s.itemRepo == nil || strings.TrimSpace(contentID) == "" {
		return nil, false
	}
	item, err := s.itemRepo.GetByID(ctx, contentID)
	if err != nil || item == nil || !isConfirmedOwnershipStatus(item.Status) {
		return nil, false
	}
	return item, true
}

func (s *MetadataService) rebindSkeletonByProviderIDs(
	ctx context.Context,
	skeletonContentID string,
	providerIDs map[string]string,
	itemType string,
) (string, error) {
	if s == nil || skeletonContentID == "" {
		return "", nil
	}
	unlockDedup := s.lockDedupKey(dedupKeyFromProviderIDs(itemType, providerIDs))
	defer unlockDedup()
	return s.rebindSkeletonByProviderIDsLocked(ctx, skeletonContentID, providerIDs, itemType)
}

func (s *MetadataService) rebindSkeletonByProviderIDsLocked(
	ctx context.Context,
	skeletonContentID string,
	providerIDs map[string]string,
	itemType string,
) (string, error) {
	return s.rebindItemByProviderIDsLocked(ctx, skeletonContentID, providerIDs, itemType, false)
}

func (s *MetadataService) rebindItemByProviderIDsLocked(
	ctx context.Context,
	sourceContentID string,
	providerIDs map[string]string,
	itemType string,
	allowMatchedSource bool,
) (string, error) {
	if s == nil || sourceContentID == "" {
		return "", nil
	}
	existing, err := s.findExistingByProviderIDs(ctx, providerIDs, itemType, sourceContentID)
	if err != nil {
		return "", fmt.Errorf("finding existing item for skeleton rebind: %w", err)
	}
	if existing != nil && !isConfirmedOwnershipStatus(existing.Status) {
		return "", nil
	}
	if existing == nil || existing.ContentID == "" || existing.ContentID == sourceContentID {
		return "", nil
	}
	if err := s.rebindItemToExistingItem(ctx, sourceContentID, existing.ContentID, allowMatchedSource); err != nil {
		return "", err
	}
	slog.Info("metadata: rebound skeleton to existing item",
		"from_content_id", sourceContentID,
		"to_content_id", existing.ContentID,
		"item_type", itemType,
	)
	return existing.ContentID, nil
}

func (s *MetadataService) recoverProviderIDConflict(
	ctx context.Context,
	sourceContentID string,
	providerIDs map[string]string,
	itemType string,
	allowMatchedSource bool,
) (string, error) {
	existing, err := s.findExistingByProviderIDs(ctx, providerIDs, itemType, sourceContentID)
	if err != nil {
		return "", fmt.Errorf("finding existing item for provider conflict recovery: %w", err)
	}
	if existing == nil || existing.ContentID == "" || existing.ContentID == sourceContentID {
		return "", nil
	}
	if !isConfirmedOwnershipStatus(existing.Status) {
		if s.dbPool == nil {
			return "", nil
		}
		if err := clearProvisionalProviderIDsLocked(ctx, s.dbPool, existing.ContentID); err != nil {
			return "", err
		}
		return sourceContentID, nil
	}
	if s.dbPool != nil {
		var sourceStatus string
		statusErr := s.dbPool.QueryRow(ctx, `SELECT status FROM media_items WHERE content_id = $1`, sourceContentID).Scan(&sourceStatus)
		if errors.Is(statusErr, pgx.ErrNoRows) {
			return existing.ContentID, nil
		}
		if statusErr != nil {
			return "", fmt.Errorf("loading source status for provider conflict recovery: %w", statusErr)
		}
		if isProvisionalOwnershipStatus(sourceStatus) && isConfirmedOwnershipStatus(existing.Status) {
			canonicalID, err := canonicalizeProviderIDDuplicateInto(ctx, s.dbPool, sourceContentID, existing.ContentID, false)
			if err != nil {
				return "", err
			}
			return canonicalID, nil
		}
		canonicalID, err := s.canonicalizeProviderIDDuplicate(ctx, sourceContentID, existing.ContentID, allowMatchedSource)
		if err != nil {
			return "", err
		}
		if canonicalID != "" {
			return canonicalID, nil
		}
	}
	return s.rebindItemByProviderIDsLocked(ctx, sourceContentID, providerIDs, itemType, allowMatchedSource)
}

func (s *MetadataService) rebindSkeletonToExistingItem(ctx context.Context, fromContentID, toContentID string) error {
	return s.rebindItemToExistingItem(ctx, fromContentID, toContentID, false)
}

func (s *MetadataService) rebindItemToExistingItem(ctx context.Context, fromContentID, toContentID string, allowMatchedSource bool) error {
	if s == nil || fromContentID == "" || toContentID == "" || fromContentID == toContentID {
		return nil
	}
	if s.dbPool == nil {
		return fmt.Errorf("rebind skeleton requires database pool")
	}

	tx, err := s.dbPool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin skeleton rebind transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	steps := []struct {
		name string
		sql  string
		args []any
	}{
		{
			name: "dedupe library memberships",
			sql: `
				DELETE FROM media_item_libraries src
				USING media_item_libraries dest
				WHERE src.content_id = $1
				  AND dest.content_id = $2
				  AND src.media_folder_id = dest.media_folder_id
			`,
			args: []any{fromContentID, toContentID},
		},
		{
			name: "move library memberships",
			sql: `
				UPDATE media_item_libraries
				SET content_id = $2
				WHERE content_id = $1
			`,
			args: []any{fromContentID, toContentID},
		},
		// Legacy claims owned by provisional shells remain quarantined. They are
		// ignored for reuse elsewhere, and confirmation writes fresh claims
		// explicitly rather than promoting provisional ones during rebind.
		{
			name: "move file links",
			sql: `
				UPDATE media_files
				SET content_id = $2,
					episode_id = NULL,
					updated_at = NOW()
				WHERE content_id = $1
			`,
			args: []any{fromContentID, toContentID},
		},
		{
			name: "clear episode library memberships for rebound source series",
			sql: `
				DELETE FROM episode_libraries el
				USING episodes e
				WHERE e.content_id = el.episode_id
				  AND e.series_id = $1
			`,
			args: []any{fromContentID},
		},
		{
			name: "dedupe provider ids",
			sql: `
				DELETE FROM media_item_provider_ids src
				USING media_item_provider_ids dest
				WHERE src.content_id = $1
				  AND dest.content_id = $2
				  AND src.provider = dest.provider
			`,
			args: []any{fromContentID, toContentID},
		},
		{
			name: "move provider ids",
			sql: `
				UPDATE media_item_provider_ids src
				SET content_id = $2,
					item_type = (SELECT type FROM media_items WHERE content_id = $2),
					updated_at = NOW()
				WHERE src.content_id = $1
				  AND NOT EXISTS (
					SELECT 1
					FROM media_item_provider_ids other
					WHERE other.content_id <> $1
					  AND other.provider = src.provider
					  AND other.provider_id = src.provider_id
					  AND other.item_type = (SELECT type FROM media_items WHERE content_id = $2)
				  )
			`,
			args: []any{fromContentID, toContentID},
		},
		{
			name: "delete orphaned skeleton",
			sql: `
				DELETE FROM media_items
				WHERE content_id = $1
				  AND lower(trim(status)) = ANY($2::text[])
			`,
			args: []any{fromContentID, rebindDeletableStatuses(allowMatchedSource)},
		},
	}

	// Move user state (progress, history, favorites, ...) onto the surviving
	// item before the source rows are touched: the source media_items row is
	// deleted below, which would strand the soft-referenced watch state and
	// cascade-delete FK children like collection memberships. Runs first so
	// the episode S/E mapping still sees the source series' episode rows.
	episodePairs, err := mergeEpisodeIDPairs(ctx, tx, fromContentID, toContentID)
	if err != nil {
		return err
	}
	if _, err := reattribute.Run(ctx, tx, reattribute.Options{
		FromContentID: fromContentID,
		ToContentID:   toContentID,
		WholeItem:     true,
		EpisodePairs:  episodePairs,
	}); err != nil {
		return fmt.Errorf("reattributing user state %s -> %s: %w", fromContentID, toContentID, err)
	}

	for _, step := range steps {
		if _, err := tx.Exec(ctx, step.sql, step.args...); err != nil {
			return fmt.Errorf("%s: %w", step.name, err)
		}
	}
	if err := catalog.RecomputeSeriesLatestEpisodeAdded(ctx, tx, []string{fromContentID}); err != nil {
		return err
	}

	if err := catalog.EnqueueSearchIndexRename(ctx, tx, fromContentID, toContentID); err != nil {
		return fmt.Errorf("enqueue catalog search skeleton rebind %s -> %s: %w", fromContentID, toContentID, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit skeleton rebind transaction: %w", err)
	}
	return nil
}

// mergeEpisodeIDPairs maps the source series' episode content ids onto the
// target series' episodes by (season, episode) number, so episode-level user
// state survives a series merge. Episodes with no counterpart on the target
// are skipped: their state stays on ids that die with the source series, which
// is today's behavior, and the next scan recreates the episodes on the target.
func mergeEpisodeIDPairs(ctx context.Context, tx pgx.Tx, fromContentID, toContentID string) ([]reattribute.IDPair, error) {
	rows, err := tx.Query(ctx, `
		SELECT src.content_id, dest.content_id
		FROM episodes src
		JOIN episodes dest
		  ON dest.series_id = $2
		 AND dest.season_number = src.season_number
		 AND dest.episode_number = src.episode_number
		WHERE src.series_id = $1
		  AND src.content_id <> dest.content_id
	`, fromContentID, toContentID)
	if err != nil {
		return nil, fmt.Errorf("mapping episode ids for merge %s -> %s: %w", fromContentID, toContentID, err)
	}
	defer rows.Close()

	var pairs []reattribute.IDPair
	for rows.Next() {
		var pair reattribute.IDPair
		if err := rows.Scan(&pair.From, &pair.To); err != nil {
			return nil, fmt.Errorf("scanning episode id pair: %w", err)
		}
		pairs = append(pairs, pair)
	}
	return pairs, rows.Err()
}

func rebindDeletableStatuses(allowMatchedSource bool) []string {
	statuses := []string{"pending", "unmatched", "ambiguous"}
	if allowMatchedSource {
		statuses = append(statuses, "matched")
	}
	return statuses
}

func isProviderIDUniqueConflict(err error) bool {
	return isPgConstraintViolation(err, "23505", "media_item_provider_ids_provider_provider_id_item_type_key")
}

func (s *MetadataService) repairMatchedDuplicateProviderOwnersByFolderAndPathPrefix(ctx context.Context, folderID int, pathPrefix string) (int, error) {
	if s == nil || s.dbPool == nil || folderID <= 0 || strings.TrimSpace(pathPrefix) == "" {
		return 0, nil
	}

	pathPrefix = filepath.Clean(pathPrefix)
	scopeLike := pathPrefix + "/%"

	rows, err := s.dbPool.Query(ctx, `
		WITH source_candidates AS (
			SELECT DISTINCT
				src.content_id,
				src.type,
				COALESCE(NULLIF(src.tmdb_id, ''), '') AS tmdb_id,
				COALESCE(NULLIF(src.imdb_id, ''), '') AS imdb_id,
				COALESCE(NULLIF(src.tvdb_id, ''), '') AS tvdb_id
			FROM media_items src
			JOIN media_item_libraries mil
			  ON mil.content_id = src.content_id
			JOIN media_folders folders
			  ON folders.id = mil.media_folder_id
			JOIN media_files mf
			  ON mf.content_id = src.content_id
			 AND mf.media_folder_id = mil.media_folder_id
			WHERE mil.media_folder_id = $1
			  AND folders.enabled = true
			  AND lower(trim(src.status)) = 'matched'
			  AND mf.missing_since IS NULL
			  AND (mf.file_path = $2 OR mf.file_path LIKE $3)
			  AND NOT EXISTS (
				SELECT 1
				FROM media_item_provider_ids pid
				WHERE pid.content_id = src.content_id
			  )
			  AND (
				COALESCE(NULLIF(src.tmdb_id, ''), '') <> '' OR
				COALESCE(NULLIF(src.imdb_id, ''), '') <> '' OR
				COALESCE(NULLIF(src.tvdb_id, ''), '') <> ''
			  )
		),
		ranked_targets AS (
			SELECT
				src.content_id AS source_content_id,
				dest.content_id AS target_content_id,
				ROW_NUMBER() OVER (
					PARTITION BY src.content_id
					ORDER BY dest.updated_at DESC, dest.content_id ASC
				) AS rn
			FROM source_candidates src
			JOIN media_items dest
			  ON dest.content_id <> src.content_id
			 AND dest.type = src.type
			 AND lower(trim(dest.status)) = 'matched'
			 AND (
				(src.tmdb_id <> '' AND dest.tmdb_id = src.tmdb_id) OR
				(src.imdb_id <> '' AND dest.imdb_id = src.imdb_id) OR
				(src.tvdb_id <> '' AND dest.tvdb_id = src.tvdb_id)
			 )
			WHERE EXISTS (
				SELECT 1
				FROM media_item_provider_ids pid
				WHERE pid.content_id = dest.content_id
			)
		)
		SELECT source_content_id, target_content_id
		FROM ranked_targets
		WHERE rn = 1
	`, folderID, pathPrefix, scopeLike)
	if err != nil {
		return 0, fmt.Errorf("querying scoped matched duplicate repairs: %w", err)
	}
	defer rows.Close()

	type repairPair struct {
		source string
		target string
	}
	pairs := make([]repairPair, 0)
	for rows.Next() {
		var pair repairPair
		if err := rows.Scan(&pair.source, &pair.target); err != nil {
			return 0, fmt.Errorf("scanning scoped matched duplicate repair: %w", err)
		}
		pairs = append(pairs, pair)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterating scoped matched duplicate repairs: %w", err)
	}

	repaired := 0
	for _, pair := range pairs {
		if pair.source == "" || pair.target == "" || pair.source == pair.target {
			continue
		}
		if err := s.rebindItemToExistingItem(ctx, pair.source, pair.target, true); err != nil {
			return repaired, fmt.Errorf("repairing duplicate matched item %s -> %s: %w", pair.source, pair.target, err)
		}
		repaired++
	}

	return repaired, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

var ephemeralProviderIDKeys = map[string]struct{}{
	"metadb":    {},
	"_filepath": {},
	"oshash":    {},
}

func isEphemeralProviderIDKey(key string) bool {
	_, ok := ephemeralProviderIDKeys[strings.ToLower(strings.TrimSpace(key))]
	return ok
}

func copyMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	maps.Copy(out, m)
	return out
}

func providerIDMapFromRows(rows []*models.MediaItemProviderID) map[string]string {
	if len(rows) == 0 {
		return nil
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		if row == nil {
			continue
		}
		provider := strings.TrimSpace(row.Provider)
		providerID := strings.TrimSpace(row.ProviderID)
		if provider == "" || providerID == "" || isEphemeralProviderIDKey(provider) {
			continue
		}
		out[provider] = providerID
	}
	return out
}

func (s *MetadataService) lockDedupKey(key string) func() {
	if s == nil || strings.TrimSpace(key) == "" {
		return func() {}
	}

	s.dedupLocks.mu.Lock()
	if s.dedupLocks.locks == nil {
		s.dedupLocks.locks = make(map[string]*dedupLockRef)
	}
	ref := s.dedupLocks.locks[key]
	if ref == nil {
		ref = &dedupLockRef{}
		s.dedupLocks.locks[key] = ref
	}
	ref.refs++
	s.dedupLocks.mu.Unlock()

	ref.mu.Lock()
	return func() {
		ref.mu.Unlock()

		s.dedupLocks.mu.Lock()
		defer s.dedupLocks.mu.Unlock()
		ref.refs--
		if ref.refs == 0 {
			delete(s.dedupLocks.locks, key)
		}
	}
}

func dedupKeyFromProviderIDs(itemType string, providerIDs map[string]string) string {
	if len(providerIDs) == 0 {
		return ""
	}

	keys := make([]string, 0, len(providerIDs))
	for key, value := range providerIDs {
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		if key == "" || value == "" || isEphemeralProviderIDKey(key) {
			continue
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys)+1)
	parts = append(parts, "provider", strings.ToLower(strings.TrimSpace(itemType)))
	for _, key := range keys {
		parts = append(parts, key+"="+strings.TrimSpace(providerIDs[key]))
	}
	return strings.Join(parts, "|")
}

func dedupKeyFromTitleYear(itemType, title string, year int) string {
	if strings.TrimSpace(title) == "" || year <= 0 {
		return ""
	}
	return strings.Join([]string{
		"title",
		strings.ToLower(strings.TrimSpace(itemType)),
		strings.ToLower(strings.TrimSpace(title)),
		fmt.Sprintf("%04d", year),
	}, "|")
}

func dedupKeyFromGroup(folderID, groupKeyVersion int, contentGroupKey string) string {
	if folderID <= 0 || strings.TrimSpace(contentGroupKey) == "" {
		return ""
	}
	return strings.Join([]string{
		"group",
		strconv.Itoa(folderID),
		strconv.Itoa(groupKeyVersion),
		strings.TrimSpace(contentGroupKey),
	}, "|")
}

func dedupKeyFromRoot(folderID int, rootPath string) string {
	if folderID <= 0 || strings.TrimSpace(rootPath) == "" {
		return ""
	}
	return strings.Join([]string{
		"root",
		strconv.Itoa(folderID),
		filepath.Clean(rootPath),
	}, "|")
}

func dedupKeyForSkeleton(
	itemType string,
	providerIDs map[string]string,
	title string,
	year int,
	folderID int,
	groupKeyVersion int,
	contentGroupKey string,
	rootPath string,
	fallbackPath string,
) string {
	if key := dedupKeyFromProviderIDs(itemType, providerIDs); key != "" {
		return key
	}
	if key := dedupKeyFromTitleYear(itemType, title, year); key != "" {
		return key
	}
	if key := dedupKeyFromGroup(folderID, groupKeyVersion, contentGroupKey); key != "" {
		return key
	}
	if key := dedupKeyFromRoot(folderID, rootPath); key != "" {
		return key
	}
	if strings.TrimSpace(fallbackPath) == "" {
		return ""
	}
	return strings.Join([]string{"file", strconv.Itoa(folderID), filepath.Clean(fallbackPath)}, "|")
}

func intSliceToFields(ints []int) []MetadataField {
	fields := make([]MetadataField, len(ints))
	for i, v := range ints {
		fields[i] = MetadataField(v)
	}
	return fields
}

func itemToMetadataResult(item *models.MediaItem) *MetadataResult {
	result := &MetadataResult{
		HasMetadata:       true,
		Title:             item.Title,
		OriginalTitle:     item.OriginalTitle,
		SortTitle:         item.SortTitle,
		Overview:          item.Overview,
		Tagline:           item.Tagline,
		Year:              item.Year,
		Runtime:           item.Runtime,
		ContentRating:     item.ContentRating,
		Genres:            item.Genres,
		Studios:           item.Studios,
		Networks:          item.Networks,
		Countries:         item.Countries,
		Keywords:          item.Keywords,
		OriginalLanguage:  item.OriginalLanguage,
		People:            item.People,
		PosterPath:        item.PosterPath,
		PosterThumbhash:   item.PosterThumbhash,
		BackdropPath:      item.BackdropPath,
		BackdropThumbhash: item.BackdropThumbhash,
		LogoPath:          item.LogoPath,
		ProviderIDs:       make(map[string]string),
	}
	if item.TmdbID != "" {
		result.ProviderIDs["tmdb"] = item.TmdbID
	}
	if item.TvdbID != "" {
		result.ProviderIDs["tvdb"] = item.TvdbID
	}
	if item.ImdbID != "" {
		result.ProviderIDs["imdb"] = item.ImdbID
	}
	result.ProviderIDs["metadb"] = item.ContentID

	if item.RatingIMDB != nil {
		result.Ratings.IMDB = *item.RatingIMDB
	}
	if item.RatingTMDB != nil {
		result.Ratings.TMDB = *item.RatingTMDB
	}
	if item.RatingRTCritic != nil {
		result.Ratings.RTCritic = float64(*item.RatingRTCritic)
	}
	if item.RatingRTAudience != nil {
		result.Ratings.RTAudience = float64(*item.RatingRTAudience)
	}

	if item.FirstAirDate != nil {
		result.FirstAirDate = *item.FirstAirDate
	}
	if item.LastAirDate != nil {
		result.LastAirDate = *item.LastAirDate
	}
	if item.AirTime != nil {
		result.AirTime = *item.AirTime
	}
	if item.AirTimezone != nil {
		result.AirTimezone = *item.AirTimezone
	}
	if item.ReleaseDate != nil {
		result.ReleaseDate = *item.ReleaseDate
	}
	if item.SeasonCount != nil {
		result.SeasonCount = *item.SeasonCount
	}

	return result
}

func metadataResultToItem(r *MetadataResult, contentType string) *models.MediaItem {
	item := &models.MediaItem{
		Type:              contentType,
		Title:             r.Title,
		OriginalTitle:     r.OriginalTitle,
		SortTitle:         r.SortTitle,
		Overview:          r.Overview,
		Tagline:           r.Tagline,
		Year:              r.Year,
		Runtime:           r.Runtime,
		ContentRating:     r.ContentRating,
		Genres:            r.Genres,
		Studios:           r.Studios,
		Networks:          r.Networks,
		Countries:         lang.CanonicalCountries(r.Countries),
		Keywords:          r.Keywords,
		OriginalLanguage:  lang.Canonical(r.OriginalLanguage),
		People:            r.People,
		TmdbID:            r.ProviderIDs["tmdb"],
		TvdbID:            r.ProviderIDs["tvdb"],
		ImdbID:            r.ProviderIDs["imdb"],
		PosterPath:        r.PosterPath,
		PosterThumbhash:   r.PosterThumbhash,
		BackdropPath:      r.BackdropPath,
		BackdropThumbhash: r.BackdropThumbhash,
		LogoPath:          r.LogoPath,
	}

	if r.Ratings.IMDB > 0 {
		v := r.Ratings.IMDB
		item.RatingIMDB = &v
	}
	if r.Ratings.TMDB > 0 {
		v := r.Ratings.TMDB
		item.RatingTMDB = &v
	}
	if r.Ratings.RTCritic > 0 {
		v := int(r.Ratings.RTCritic)
		item.RatingRTCritic = &v
	}
	if r.Ratings.RTAudience > 0 {
		v := int(r.Ratings.RTAudience)
		item.RatingRTAudience = &v
	}

	if r.FirstAirDate != "" {
		item.FirstAirDate = &r.FirstAirDate
	}
	if r.LastAirDate != "" {
		item.LastAirDate = &r.LastAirDate
	}
	if r.AirTime != "" {
		item.AirTime = &r.AirTime
	}
	if r.AirTime != "" && r.AirTimezone == "" {
		r.AirTimezone = catalog.InferAirTimezone(r.Networks, r.Countries)
	}
	if r.AirTimezone != "" {
		item.AirTimezone = &r.AirTimezone
	}
	if r.ReleaseDate != "" {
		item.ReleaseDate = &r.ReleaseDate
	}
	if r.SeasonCount > 0 {
		item.SeasonCount = &r.SeasonCount
	}

	return item
}

func applyBestImages(item *models.MediaItem, images []RemoteImage, mode MergeMode, preferredLang string) {
	// Images arrive in provider-chain order (highest priority first).
	// For each image type, pick the best image using a fallback chain:
	//   1. Preferred language or language-neutral
	//   2. English or language-neutral (if preferred != "en")
	//   3. Any language
	// Within each pass, the first provider with a match wins; within
	// that provider, pick the highest-rated image.
	type best struct {
		url        string
		rating     float64
		providerID string
	}

	filters := []func(string) bool{
		func(l string) bool { return l == "" || l == preferredLang },
	}
	if preferredLang != "en" {
		filters = append(filters, func(l string) bool { return l == "" || l == "en" })
	}
	filters = append(filters, func(string) bool { return true })

	bestByType := map[ImageType]*best{
		ImagePoster:   {},
		ImageBackdrop: {},
		ImageLogo:     {},
	}

	for _, accept := range filters {
		for _, img := range images {
			if img.URL == "" || !accept(img.Language) {
				continue
			}
			b := bestByType[img.Type]
			if b == nil {
				continue
			}
			if b.url == "" {
				b.url = img.URL
				b.rating = img.Rating
				b.providerID = img.ProviderID
			} else if img.ProviderID == b.providerID && img.Rating > b.rating {
				b.url = img.URL
				b.rating = img.Rating
			}
		}
		// Stop if every type has a candidate.
		allFilled := true
		for _, b := range bestByType {
			if b.url == "" {
				allFilled = false
				break
			}
		}
		if allFilled {
			break
		}
	}

	applyIfBetter := func(current *string, b *best) {
		if b.url == "" {
			return
		}
		if *current == "" || mode == MergeReplaceUnlocked || b.rating > 0 {
			*current = b.url
		}
	}
	applyIfBetter(&item.PosterPath, bestByType[ImagePoster])
	applyIfBetter(&item.BackdropPath, bestByType[ImageBackdrop])
	applyIfBetter(&item.LogoPath, bestByType[ImageLogo])
}

type itemArtworkField struct {
	imageType ImageType
	path      *string
	source    *string
	thumbhash *string
}

func itemArtworkFields(item *models.MediaItem) []itemArtworkField {
	if item == nil {
		return nil
	}
	return []itemArtworkField{
		{imageType: ImagePoster, path: &item.PosterPath, source: &item.PosterSourcePath, thumbhash: &item.PosterThumbhash},
		{imageType: ImageBackdrop, path: &item.BackdropPath, source: &item.BackdropSourcePath, thumbhash: &item.BackdropThumbhash},
		{imageType: ImageLogo, path: &item.LogoPath, source: &item.LogoSourcePath},
	}
}

func prepareItemImagesForQueue(item, existing *models.MediaItem) {
	for _, field := range itemArtworkFields(item) {
		existingPath := existingImagePath(existing, field.imageType)
		existingThumbhash := existingImageThumbhash(existing, field.imageType)
		existingSource := existingImageSourcePath(existing, field.imageType)
		currentThumbhash := ""
		if field.thumbhash != nil {
			currentThumbhash = *field.thumbhash
		}
		if isCachedImagePath(*field.path) && *field.path == existingPath && existingSource != "" {
			*field.source = existingSource
			if field.thumbhash != nil && currentThumbhash == "" {
				*field.thumbhash = existingThumbhash
			}
			continue
		}
		nextPath, nextThumbhash, nextSource := preserveCachedArtwork(
			*field.path,
			currentThumbhash,
			existingPath,
			existingSource,
			existingThumbhash,
		)
		*field.path = nextPath
		*field.source = nextSource
		if field.thumbhash != nil {
			*field.thumbhash = nextThumbhash
		}
	}
}

func (s *MetadataService) enqueueItemImages(ctx context.Context, item *models.MediaItem, providerIDs map[string]string, images []RemoteImage) {
	if s == nil || !s.autoCacheImages.Load() || s.imageCacheJobs == nil || item == nil || item.ContentID == "" {
		return
	}
	inputs := make([]EnqueueImageCacheJobInput, 0, 3)
	for _, field := range itemArtworkFields(item) {
		sourcePath := strings.TrimSpace(*field.source)
		if !isRemoteImageSourcePath(sourcePath) {
			continue
		}
		providerID, providerContentID := itemImageCacheAttribution(item, providerIDs, images, sourcePath)
		inputs = append(inputs, EnqueueImageCacheJobInput{
			TargetType:        ImageCacheTargetItem,
			TargetContentID:   item.ContentID,
			SeriesID:          item.ContentID,
			SourcePath:        sourcePath,
			ProviderID:        providerID,
			ProviderContentID: providerContentID,
			ContentType:       imageCacheContentType(item.Type),
			ImageType:         ImageTypeToString(field.imageType),
		})
	}
	s.enqueueImageCacheJobs(ctx, "item", item.ContentID, inputs)
}

func (s *MetadataService) enqueueItemLocalizationImages(ctx context.Context, item *models.MediaItem, loc *models.MediaItemLocalization, providerIDs map[string]string, images []RemoteImage) {
	if s == nil || !s.autoCacheImages.Load() || s.imageCacheJobs == nil || item == nil || loc == nil || loc.ContentID == "" || loc.Language == "" {
		return
	}
	locItem := &models.MediaItem{
		ContentID:          loc.ContentID,
		Type:               item.Type,
		PosterSourcePath:   loc.PosterSourcePath,
		BackdropSourcePath: loc.BackdropSourcePath,
		LogoSourcePath:     loc.LogoSourcePath,
	}
	inputs := make([]EnqueueImageCacheJobInput, 0, 3)
	for _, field := range itemArtworkFields(locItem) {
		sourcePath := strings.TrimSpace(*field.source)
		if !isRemoteImageSourcePath(sourcePath) {
			continue
		}
		providerID, providerContentID := itemImageCacheAttribution(item, providerIDs, images, sourcePath)
		inputs = append(inputs, EnqueueImageCacheJobInput{
			TargetType:        ImageCacheTargetItemLocalization,
			TargetContentID:   loc.ContentID,
			TargetLanguage:    loc.Language,
			SeriesID:          item.ContentID,
			SourcePath:        sourcePath,
			ProviderID:        providerID,
			ProviderContentID: providerContentID,
			ContentType:       imageCacheContentType(item.Type),
			ImageType:         ImageTypeToString(field.imageType),
		})
	}
	s.enqueueImageCacheJobs(ctx, "item localization", loc.ContentID, inputs)
}

func itemImageCacheAttribution(item *models.MediaItem, providerIDs map[string]string, images []RemoteImage, sourcePath string) (string, string) {
	providerID := providerIDFromPluginURL(sourcePath)
	if providerID == "" {
		providerID = findProviderID(images, sourcePath)
	}
	if providerID == "" {
		providerID = primaryProviderID(providerIDs)
	}
	if providerID == "" {
		providerID = "remote"
	}
	providerContentID := findContentID(item, providerID)
	if providerContentID == "" && item != nil {
		providerContentID = item.ContentID
	}
	return providerID, providerContentID
}

// cacheItemImages downloads, processes, and uploads item images to S3
// concurrently. Failures are logged and the original CDN URL is preserved.
func (s *MetadataService) cacheItemImages(ctx context.Context, item *models.MediaItem, images []RemoteImage) {
	type imageField struct {
		path      *string
		thumbhash *string
		imageType ImageType
	}
	fields := []imageField{
		{&item.PosterPath, &item.PosterThumbhash, ImagePoster},
		{&item.BackdropPath, &item.BackdropThumbhash, ImageBackdrop},
		{&item.LogoPath, nil, ImageLogo},
	}

	type cacheJob struct {
		field       imageField
		url         string
		downloadURL string
		providerID  string
	}

	// Prepare jobs, resolving plugin URLs upfront.
	var jobs []cacheJob
	for _, f := range fields {
		url := *f.path
		downloadURL, ok := s.resolveImageURLForCache(ctx, url)
		if !ok {
			continue
		}

		providerID := findProviderID(images, url)
		if providerID == "" {
			providerID = "unknown"
		}
		jobs = append(jobs, cacheJob{field: f, url: url, downloadURL: downloadURL, providerID: providerID})
	}

	// Cache all images concurrently.
	type cacheResult struct {
		idx    int
		result *CacheImageResult
	}
	results := make(chan cacheResult, len(jobs))
	var wg sync.WaitGroup
	for i, job := range jobs {
		wg.Add(1)
		go func(idx int, j cacheJob) {
			defer wg.Done()
			r, err := s.imageCacher.CacheImage(ctx, CacheImageRequest{
				SourceURL:   j.downloadURL,
				ProviderID:  j.providerID,
				ContentType: pluralContentType(item.Type),
				ContentID:   findContentID(item, j.providerID),
				ImageType:   j.field.imageType,
			})
			if err != nil {
				slog.Warn("metadata: image cache failed, keeping CDN URL",
					"url", j.url, "error", err)
				return
			}
			results <- cacheResult{idx: idx, result: r}
		}(i, job)
	}
	wg.Wait()
	close(results)

	for cr := range results {
		j := jobs[cr.idx]
		if j.field.imageType == ImagePoster {
			// Keep the provider-origin path the cache rewrite erases below:
			// outbound notification embeds build public provider-CDN poster
			// URLs from it (local storage URLs never leave the server).
			item.PosterSourcePath = j.url
		}
		*j.field.path = cachedOriginalImagePath(cr.result.BasePath, cr.result.Ext)
		if j.field.thumbhash != nil && cr.result.Thumbhash != "" {
			*j.field.thumbhash = cr.result.Thumbhash
		}
	}
}

// findProviderID returns the ProviderID of the RemoteImage matching the given URL.
func findProviderID(images []RemoteImage, url string) string {
	for _, img := range images {
		if img.URL == url {
			return img.ProviderID
		}
	}
	return ""
}

// pluralContentType converts "movie"/"series" to "movies"/"series" for S3 keys.
func pluralContentType(ct string) string {
	if ct == "movie" {
		return "movies"
	}
	return ct
}

// findContentID returns the best durable identifier available for S3 key
// construction, preferring the image provider's native ID when present.
func findContentID(item *models.MediaItem, providerID string) string {
	if item == nil {
		return ""
	}

	for _, key := range contentIDPreferenceOrder(providerID) {
		switch key {
		case "tmdb":
			if item.TmdbID != "" {
				return item.TmdbID
			}
		case "tvdb":
			if item.TvdbID != "" {
				return item.TvdbID
			}
		case "imdb":
			if item.ImdbID != "" {
				return item.ImdbID
			}
		case "metadb":
			if item.ContentID != "" {
				return item.ContentID
			}
		}
	}

	return ""
}

func contentIDPreferenceOrder(providerID string) []string {
	switch providerID {
	case "tmdb":
		return []string{"tmdb", "tvdb", "imdb", "metadb"}
	case "tvdb":
		return []string{"tvdb", "tmdb", "imdb", "metadb"}
	case "imdb":
		return []string{"imdb", "tmdb", "tvdb", "metadb"}
	default:
		return []string{"tmdb", "tvdb", "imdb", "metadb"}
	}
}

// primaryProviderID picks the most common provider ID for S3 key construction.
func primaryProviderID(ids map[string]string) string {
	for _, key := range []string{"tmdb", "tvdb", "imdb"} {
		if ids[key] != "" {
			return key
		}
	}
	for k := range ids {
		return k
	}
	return "unknown"
}

// FetchItemImages queries all enabled ImageProviders for available images.
// providerIDs should come from the parent MediaItem (for seasons/episodes,
// the caller resolves up to the series item). contentType is "movie" or "series".
func (s *MetadataService) FetchItemImages(ctx context.Context, providerIDs map[string]string, contentType string, language string, folderID int) ([]RemoteImage, map[string]string, error) {
	contentLevel := contentType
	chain, err := s.resolveChainCached(ctx, folderID, contentLevel)
	if err != nil {
		return nil, nil, fmt.Errorf("resolving provider chain: %w", err)
	}

	var allImages []RemoteImage
	providerErrors := make(map[string]string)

	for _, p := range chain {
		ip, ok := p.(ImageProvider)
		if !ok {
			continue
		}
		images, err := ip.GetImages(ctx, ImageRequest{
			ProviderIDs: providerIDs,
			ContentType: contentType,
			Language:    language,
		})
		if err != nil {
			slog.Warn("fetch item images: provider error",
				"provider", p.Slug(), "error", err)
			providerErrors[p.Slug()] = err.Error()
			continue
		}
		allImages = append(allImages, images...)
	}

	// Sort by rating descending (popularity).
	sort.Slice(allImages, func(i, j int) bool {
		return allImages[i].Rating > allImages[j].Rating
	})

	return allImages, providerErrors, nil
}

// ApplyItemImage downloads a single image, caches it to S3, and returns
// the stored path and thumbhash. The caller is responsible for persisting
// the result to the correct table and locking FieldImages.
func (s *MetadataService) ApplyItemImage(ctx context.Context, req ApplyItemImageRequest) (*ApplyItemImageResult, error) {
	if s.imageCacher == nil {
		return nil, fmt.Errorf("image caching is not configured")
	}

	// Resolve plugin-prefixed URLs to downloadable HTTP URLs.
	downloadURL := req.OriginalURL
	if s.imageResolver != nil && !strings.HasPrefix(downloadURL, "http://") && !strings.HasPrefix(downloadURL, "https://") {
		resolved := s.imageResolver.ResolveImageURL(ctx, downloadURL, "original")
		if resolved == "" {
			return nil, fmt.Errorf("failed to resolve image URL: %s", downloadURL)
		}
		downloadURL = resolved
	}

	result, err := s.imageCacher.CacheImage(ctx, CacheImageRequest{
		SourceURL:     downloadURL,
		ProviderID:    req.ProviderID,
		ContentType:   pluralContentType(req.ContentType),
		ContentID:     req.ContentID,
		ImageType:     req.ImageType,
		SeasonNumber:  req.SeasonNumber,
		EpisodeNumber: req.EpisodeNumber,
	})
	if err != nil {
		return nil, fmt.Errorf("caching image: %w", err)
	}

	storedPath := cachedOriginalImagePath(result.BasePath, result.Ext)
	return &ApplyItemImageResult{
		StoredPath: storedPath,
		Thumbhash:  result.Thumbhash,
	}, nil
}

// ApplyItemImageRequest describes an image to download and cache. For
// season posters and episode stills, ContentID is the parent series's
// provider ID and SeasonNumber / EpisodeNumber scope the S3 key.
type ApplyItemImageRequest struct {
	OriginalURL   string    // Plugin-prefixed path or HTTP URL
	ProviderID    string    // Plugin slug (e.g. "tmdb")
	ContentType   string    // "movie" or "series"
	ContentID     string    // Provider-specific ID for S3 key construction
	ImageType     ImageType // Which image field to update
	SeasonNumber  *int      // nil for item-level images
	EpisodeNumber *int      // nil for season poster; set for episode still
}

// ApplyItemImageResult contains the stored S3 path and thumbhash.
type ApplyItemImageResult struct {
	StoredPath string
	Thumbhash  string
}

// ImageTypeToString converts an ImageType to its string representation.
func ImageTypeToString(t ImageType) string {
	switch t {
	case ImageBackdrop:
		return "backdrop"
	case ImageLogo:
		return "logo"
	case ImageStill:
		return "still"
	case ImageProfile:
		return "profile"
	default:
		return "poster"
	}
}

// ImageTypeFromString converts a string to an ImageType.
func ImageTypeFromString(s string) ImageType {
	switch s {
	case "backdrop":
		return ImageBackdrop
	case "logo":
		return ImageLogo
	case "still":
		return ImageStill
	case "profile":
		return ImageProfile
	default:
		return ImagePoster
	}
}

// generateContentID mints a content_id when no deterministic anchor is
// available (non movie/series item types, or items with neither provider tags
// nor a path to hash). Provider-anchored movies/series should go through
// deriveLogicalContentID so the same title yields the same id on every server.
func generateContentID() (string, error) {
	return idgen.NextID()
}

// deriveLogicalContentID computes a deterministic, cross-server stable
// content_id for a movie or series from its provider IDs (see the contentid
// package). When no provider anchor is present it falls back to a local: id
// derived from fallbackPath so the item stays stable across rescans on this
// server; with neither, it falls back to a Sonyflake id. Item types other than
// movie/series are out of the deterministic scheme's scope and always get a
// Sonyflake id.
func deriveLogicalContentID(itemType string, ids contentid.ProviderIDs, fallbackPath string) (string, error) {
	switch normalizeItemTypeForContentID(itemType) {
	case "movie":
		if id, ok := contentid.ForMovie(ids); ok {
			return id, nil
		}
		if strings.TrimSpace(fallbackPath) != "" {
			return contentid.ForLocal(fallbackPath), nil
		}
	case "series":
		if id, ok := contentid.ForSeries(ids); ok {
			return id, nil
		}
		if strings.TrimSpace(fallbackPath) != "" {
			return contentid.ForLocal(fallbackPath), nil
		}
	}
	return generateContentID()
}

// deriveSeasonContentID composes a deterministic season content_id from its
// parent series content_id. When the series is provider-anchored the season id
// embeds the series anchor (the load-bearing format invariant); otherwise it
// falls back to a Sonyflake id so legacy/local series keep working.
func deriveSeasonContentID(seriesContentID string, seasonNumber int) (string, error) {
	if id, ok := contentid.ForSeason(seriesContentID, seasonNumber); ok {
		return id, nil
	}
	return generateContentID()
}

// deriveEpisodeContentID composes a deterministic episode content_id from its
// parent series content_id plus the season/episode numbers, falling back to a
// Sonyflake id when the series is not provider-anchored.
func deriveEpisodeContentID(seriesContentID string, seasonNumber, episodeNumber int) (string, error) {
	if id, ok := contentid.ForEpisode(seriesContentID, seasonNumber, episodeNumber); ok {
		return id, nil
	}
	return generateContentID()
}

// firstNonEmpty returns the first non-blank string, or "" if all are blank.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// normalizeItemTypeForContentID maps the media_items.type values onto the two
// entity kinds the deterministic scheme covers.
func normalizeItemTypeForContentID(itemType string) string {
	switch strings.ToLower(strings.TrimSpace(itemType)) {
	case "movie", "movies":
		return "movie"
	case "series", "show", "tv":
		return "series"
	default:
		return ""
	}
}

func searchResultConflictsWithTrustedIDs(hintedIDs, candidateIDs map[string]string) bool {
	for _, key := range trustedSearchIDKeys {
		hinted := strings.TrimSpace(hintedIDs[key])
		candidate := strings.TrimSpace(candidateIDs[key])
		if hinted != "" && candidate != "" && hinted != candidate {
			return true
		}
	}
	return false
}

func suppressTitleYearFallbackForTrustedIDs(query SearchQuery) SearchQuery {
	for _, key := range trustedSearchIDKeys {
		if strings.TrimSpace(query.ProviderIDs[key]) != "" {
			query.Title = ""
			query.Year = 0
			return query
		}
	}
	return query
}
