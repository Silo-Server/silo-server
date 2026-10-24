package metadata

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/catalog/reattribute"
	"github.com/Silo-Server/silo-server/internal/contentid"
	"github.com/Silo-Server/silo-server/internal/database/queryattr"
	"github.com/Silo-Server/silo-server/internal/idgen"
	"github.com/Silo-Server/silo-server/internal/lang"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/naming"
	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
	UpdateContentIDByObservedRootPath(ctx context.Context, folderID int, observedRootPath, contentID string) (int, []string, error)
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
	SetStatusUnlessMatched(ctx context.Context, contentID, status string) (bool, error)
	IncrementRefreshFailure(ctx context.Context, contentID string) error
	ReplacePeople(ctx context.Context, contentID string, people []models.ItemPerson) error
	ListUnmatchedByFolderAndPathPrefix(ctx context.Context, folderID int, pathPrefix string, limit int) ([]string, error)
}

// metadataPersonRepo resolves credited people and claims their local photos.
// The concrete *catalog.PersonRepository satisfies this interface.
type metadataPersonRepo interface {
	localPersonPhotoRepo
	BatchFindOrCreate(ctx context.Context, people []models.Person) ([]int64, error)
}

type metadataItemDeleteRepo interface {
	Delete(ctx context.Context, contentID string) ([]string, error)
}

// metadataItemGuardedDeleteRepo deletes an item only while nothing references it.
type metadataItemGuardedDeleteRepo interface {
	DeleteIfUnreferenced(ctx context.Context, contentID string) (bool, error)
}

// metadataItemInsertRepo creates an item only when its content_id is free.
type metadataItemInsertRepo interface {
	InsertIfAbsent(ctx context.Context, item *models.MediaItem) (bool, error)
}

// metadataTrailerRefreshRepo is the cooldown gate behind
// RequestTrailersRefresh. It is a separate optional interface (asserted on
// itemRepo) because only the viewer-facing trailer action needs it; the
// concrete *catalog.ItemRepository satisfies it.
type metadataTrailerRefreshRepo interface {
	TryClaimTrailersRefresh(ctx context.Context, contentID string, cooldown time.Duration) (bool, *time.Time, error)
	ReleaseTrailersRefreshClaim(ctx context.Context, contentID string, claimedAt time.Time) error
	TrailersRefreshRequestedAt(ctx context.Context, contentID string) (*time.Time, error)
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
	SnapshotEpisodeDebts(ctx context.Context, seriesID string) (map[string]string, error)
	DeleteEpisodeDebts(ctx context.Context, contentIDs []string, versions map[string]string) error
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
	ListBySeriesAndNumbers(ctx context.Context, seriesID string, seasonNumbers, episodeNumbers []int32) ([]*models.Episode, error)
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
	ListBySeriesAndNumbers(ctx context.Context, seriesID string, seasonNumbers []int32) ([]*models.Season, error)
	Upsert(ctx context.Context, s *models.Season) error
	BulkUpsert(ctx context.Context, seasons []*models.Season) error
}

type metadataItemLocalizationRepo interface {
	Get(ctx context.Context, contentID, language string) (*models.MediaItemLocalization, error)
	Upsert(ctx context.Context, loc *models.MediaItemLocalization) error
}

type metadataSeasonLocalizationRepo interface {
	Get(ctx context.Context, seasonContentID, language string) (*models.SeasonLocalization, error)
	GetBySeasonIDs(ctx context.Context, seasonIDs []string, language string) (map[string]*models.SeasonLocalization, error)
	Upsert(ctx context.Context, loc *models.SeasonLocalization) error
	BulkUpsert(ctx context.Context, localizations []*models.SeasonLocalization) error
}

type metadataEpisodeLocalizationRepo interface {
	Get(ctx context.Context, episodeContentID, language string) (*models.EpisodeLocalization, error)
	GetByEpisodeIDs(ctx context.Context, episodeIDs []string, language string) (map[string]*models.EpisodeLocalization, error)
	Upsert(ctx context.Context, loc *models.EpisodeLocalization) error
	BulkUpsert(ctx context.Context, localizations []*models.EpisodeLocalization) error
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

// metadataRatingSourceRepo persists per-source ratings. The concrete
// *catalog.RatingSourceRepository satisfies this.
type metadataRatingSourceRepo interface {
	Upsert(ctx context.Context, contentID string, sources []models.ItemRatingSource, replace bool) error
	Replace(ctx context.Context, contentID string, sources []models.ItemRatingSource) error
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
	updateItemStatus          func(ctx context.Context, contentID, status string) (bool, error)
	linkSeriesFilesToEpisodes func(ctx context.Context, seriesID string)
	ensureSeriesEpisodeLinks  func(ctx context.Context, seriesID string) error
	bulkEnrichmentTargets     func(ctx context.Context) ([]bulkEnrichmentTarget, error)
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

type providerIDValueSet map[string]map[string]struct{}

func (s providerIDValueSet) add(provider, providerID string) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	providerID = normalizeProviderIDComparisonValue(provider, providerID)
	if provider == "" || providerID == "" {
		return
	}
	if s[provider] == nil {
		s[provider] = make(map[string]struct{})
	}
	s[provider][providerID] = struct{}{}
}

func (s providerIDValueSet) has(provider, providerID string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	_, ok := s[provider][normalizeProviderIDComparisonValue(provider, providerID)]
	return ok
}

func (s providerIDValueSet) remove(provider, providerID string) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	providerID = normalizeProviderIDComparisonValue(provider, providerID)
	values := s[provider]
	if providerID == "" || len(values) == 0 {
		return
	}
	delete(values, providerID)
	if len(values) == 0 {
		delete(s, provider)
	}
}

func cloneProviderIDValueSet(src providerIDValueSet) providerIDValueSet {
	if len(src) == 0 {
		return make(providerIDValueSet)
	}
	dst := make(providerIDValueSet, len(src))
	for provider, values := range src {
		dst[provider] = maps.Clone(values)
	}
	return dst
}

type provider404State struct {
	// dropped tracks every provider value rejected in this run so an
	// accumulator copy cannot resurrect it for later phases.
	dropped providerIDValueSet
	// stale tracks durable values that must not be persisted as current item
	// identity and must remain in the negative cache.
	stale providerIDValueSet
}

func newProvider404State() *provider404State {
	return &provider404State{
		dropped: make(providerIDValueSet),
		stale:   make(providerIDValueSet),
	}
}

func (s *provider404State) record(provider, providerID string) {
	if s == nil {
		return
	}
	s.dropped.add(provider, providerID)
	provider = strings.ToLower(strings.TrimSpace(provider))
	providerID = strings.TrimSpace(providerID)
	if isDurableProviderSlug(provider) && providerID != "" {
		s.stale.add(provider, providerID)
	}
}

func upsertStaleProviderIDValues(
	ctx context.Context,
	repo metadataStaleIDRepo,
	contentID string,
	values providerIDValueSet,
) {
	if repo == nil || strings.TrimSpace(contentID) == "" {
		return
	}
	for provider, providerIDs := range values {
		for providerID := range providerIDs {
			if err := repo.Upsert(ctx, contentID, provider, providerID); err != nil {
				slog.WarnContext(ctx, "metadata: failed to record stale ID", "component", "metadata",
					"content_id", contentID, "provider", provider, "provider_id", providerID, "error", err)
			}
		}
	}
}

func handleProvider404(
	provider404s *provider404State,
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
		provider404s.record(provider, providerID)
		logAttrs = append(logAttrs, "provider_id", providerID)
		dropProviderID(providerIDs, provider)
	}
	logAttrs = append(logAttrs, "error", err)
	slog.Info("metadata: provider returned 404 for stale or invalid external id", logAttrs...)
	return true
}

func handleScopedProvider404(
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
	slog.Info("metadata: provider returned 404 for unavailable scoped metadata", logAttrs...)
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
	itemLocalizationRepo    metadataItemLocalizationRepo
	itemBackdropRepo        metadataItemBackdropRepo
	itemAliasRepo           *catalog.ItemAliasRepository
	seasonLocalizationRepo  metadataSeasonLocalizationRepo
	episodeLocalizationRepo metadataEpisodeLocalizationRepo
	autoTranslator          AutoTranslator // optional; set via SetAutoTranslator
	personRepo              metadataPersonRepo
	videoRepo               metadataVideoRepo
	enrichmentState         enrichmentStateStore
	ratingSourceRepo        metadataRatingSourceRepo
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
	localSidecars           localSidecarStore
	dbPool                  *pgxpool.Pool
	recsStaler              RecommendationStaler

	dedupLocks      keyedDedupLocks
	onDemandRefresh keyedRefreshSet
	seriesWorkMu    sync.Mutex
	seriesWork      map[string]*seriesEpisodeWork
	hooks           metadataServiceHooks
	imageCacher     ImageCacher
	imageCacheJobs  ImageCacheJobEnqueuer
	localArtwork    LocalArtworkCopier
	libraryRoots    LibraryRootResolver
	// localImageProvider overrides the built-in sidecar discovery in tests.
	localImageProvider ImageProvider
	autoCacheImages    atomic.Bool  // hot-reloaded from metadata.cache_images
	fallbackLanguage   atomic.Value // string; hot-reloaded from metadata.fallback_language
	imageResolver      interface {
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
	var itemLocalizationRepo metadataItemLocalizationRepo
	var itemBackdropRepo metadataItemBackdropRepo
	var itemAliasRepo *catalog.ItemAliasRepository
	var seasonLocalizationRepo metadataSeasonLocalizationRepo
	var episodeLocalizationRepo metadataEpisodeLocalizationRepo
	var scannedRootRepo metadataScannedRootRepo
	var scannedGroupRepo metadataScannedGroupRepo
	var groupClaimRepo metadataGroupClaimRepo
	var groupOverrideRepo metadataGroupOverrideRepo
	var observedLocationRepo metadataObservedLocationRepo
	var videoRepo metadataVideoRepo
	var enrichmentState enrichmentStateStore
	var ratingSourceRepo metadataRatingSourceRepo
	var dbPool *pgxpool.Pool
	if folderRepo != nil {
		pool := folderRepo.Pool()
		dbPool = pool
		videoRepo = catalog.NewVideoRepository(pool)
		enrichmentState = newEnrichmentStateRepository(pool)
		ratingSourceRepo = catalog.NewRatingSourceRepository(pool)
		itemLocalizationRepo = catalog.NewMediaItemLocalizationRepository(pool)
		itemBackdropRepo = catalog.NewItemBackdropRepository(pool)
		itemAliasRepo = catalog.NewItemAliasRepository(pool)
		seasonLocalizationRepo = catalog.NewSeasonLocalizationRepository(pool)
		episodeLocalizationRepo = catalog.NewEpisodeLocalizationRepository(pool)
		scannedRootRepo = scanner.NewScannedRootRepository(pool)
		scannedGroupRepo = scanner.NewScannedGroupRepository(pool)
		groupClaimRepo = catalog.NewGroupClaimRepository(pool)
		groupOverrideRepo = scanner.NewMediaGroupOverrideRepository(pool)
		observedLocationRepo = scanner.NewObservedLocationRepository(pool)
	}
	svc := &MetadataService{
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
		itemBackdropRepo:        itemBackdropRepo,
		itemAliasRepo:           itemAliasRepo,
		seasonLocalizationRepo:  seasonLocalizationRepo,
		episodeLocalizationRepo: episodeLocalizationRepo,
		videoRepo:               videoRepo,
		ratingSourceRepo:        ratingSourceRepo,
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
		enrichmentState:         enrichmentState,
		dbPool:                  dbPool,
		chainCache:              make(map[string]chainCacheEntry),
		chainCacheTTL:           60 * time.Second,
	}
	// A nil *PersonRepository stored in the interface would pass s.personRepo != nil.
	if personRepo != nil {
		svc.personRepo = personRepo
	}
	return svc
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

// SetLibraryRootResolver sets the resolver that confines local sidecar artwork
// reads for the admin image picker to the item's library roots.
func (s *MetadataService) SetLibraryRootResolver(resolver LibraryRootResolver) {
	s.libraryRoots = resolver
}

// SetAutoCacheImages controls whether refresh pipelines cache artwork into
// object storage and select local artwork at all (metadata.cache_images).
// Explicit admin image applies still use the configured image cacher when
// available. Safe for concurrent use.
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

	if req.Mode != ModeIdentify {
		pinned, err := s.pinnedUnmatched(ctx, req.ContentID)
		if err != nil {
			return nil, err
		}
		if pinned {
			return &ProcessResult{ContentID: req.ContentID, Pinned: true}, nil
		}
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
		if result.Decision != nil {
			final.Decision = result.Decision
		}
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
	go s.autoTranslator.AutoEnqueue(queryattr.Detach(context.WithoutCancel(ctx)), contentID, library)
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
		slog.WarnContext(ctx, "metadata: failed to look up library languages for adoption gate; keeping current stamp", "component", "metadata",
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
		slog.WarnContext(ctx, "metadata: failed to look up folder language, defaulting to English", "component", "metadata",
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
//
// The empty (non-nil) result is load-bearing in the other direction — it means
// every containing library turned remote videos off, which filters everything
// out and which RequestTrailersRefresh reports to the viewer as "disabled". So
// a partially-resolved union cannot be returned as if it were complete: an
// unreadable library might be the one that enables trailers, and answering
// "disabled" (or filtering everything away) on its behalf would be a guess.
// Any lookup failure therefore degrades the whole answer to unknown scope. A
// folder that is genuinely gone is not a failure and is simply skipped — a
// library that no longer exists cannot be the one enabling trailers.
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
			slog.WarnContext(ctx, "metadata: resolving item libraries for video kinds failed", "component", "metadata",
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
		switch {
		case err != nil && !errors.Is(err, catalog.ErrFolderNotFound):
			slog.WarnContext(ctx, "metadata: reading library trailer kinds failed; treating video scope as unknown",
				"component", "metadata", "content_id", contentID, "folder_id", id, "error", err)
			return nil
		case err != nil, folder == nil:
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

// itemRatingSourcesFromResult converts pipeline rating sources into
// media_item_rating_sources rows, in display order, the source name breaking
// ties so the rows are written in the same order every time.
func itemRatingSourcesFromResult(contentID string, sources map[string]RatingSource) []models.ItemRatingSource {
	rows := make([]models.ItemRatingSource, 0, len(sources))
	for name, source := range sources {
		row := models.ItemRatingSource{
			ContentID: contentID,
			Source:    name,
			Score:     source.Score,
			Provider:  source.Provider,
		}
		if source.Votes > 0 {
			votes := source.Votes
			row.Votes = &votes
		}
		rows = append(rows, row)
	}
	slices.SortFunc(rows, func(a, b models.ItemRatingSource) int {
		if rank := models.RatingSourceRank(a.Source) - models.RatingSourceRank(b.Source); rank != 0 {
			return rank
		}
		return strings.Compare(a.Source, b.Source)
	})
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
	localCtx, _ := s.buildLocalProviderContext(ctx, contentID, folderID, false)
	return localCtx
}

// loadLocalProviderContext is localProviderContextForContent that also
// reports a failure to read the item's files or their observed roots, which
// would otherwise look like an item without local sidecars.
func (s *MetadataService) loadLocalProviderContext(ctx context.Context, contentID string, folderID int) (localProviderContext, error) {
	return s.buildLocalProviderContext(ctx, contentID, folderID, true)
}

// buildLocalProviderContext loads the local-provider context of an item's
// files in a library. Strict, it fails when an observed root cannot be looked
// up; otherwise such a root is only left out, as before.
func (s *MetadataService) buildLocalProviderContext(ctx context.Context, contentID string, folderID int, strict bool) (localProviderContext, error) {
	if s == nil || s.fileRepo == nil || contentID == "" {
		return localProviderContext{}, nil
	}
	lister, ok := s.fileRepo.(metadataContentFileLister)
	if !ok {
		return localProviderContext{}, nil
	}
	files, err := lister.GetByContentID(ctx, contentID)
	if err != nil {
		return localProviderContext{}, fmt.Errorf("loading files of %s: %w", contentID, err)
	}
	if len(files) == 0 {
		return localProviderContext{}, nil
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
			return localProviderContext{}, nil
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
		return localProviderContext{}, nil
	}
	usableRoot := func(file *models.MediaFile) (bool, error) {
		if strict {
			return s.observedRootUsableForDirectorySidecars(ctx, file.MediaFolderID, file.ObservedRootPath, file.GroupKeyVersion, file.ContentGroupKey)
		}
		return s.canUseObservedRootForDirectorySidecars(ctx, file.MediaFolderID, file.ObservedRootPath, file.GroupKeyVersion, file.ContentGroupKey), nil
	}
	if representativeFile != nil && representativeFile.ObservedRootPath != "" {
		usable, err := usableRoot(representativeFile)
		if err != nil {
			return localProviderContext{}, fmt.Errorf("loading observed root of %s: %w", contentID, err)
		}
		if usable {
			observedRootPath = representativeFile.ObservedRootPath
		}
	}
	searchPaths, err := s.collectDirectorySidecarSearchPaths(files, usableRoot)
	if err != nil {
		return localProviderContext{}, fmt.Errorf("loading observed roots of %s: %w", contentID, err)
	}

	return localProviderContext{
		filePath:                  representativeFilePath,
		representativeFilePath:    representativeFilePath,
		observedRootPath:          observedRootPath,
		allGroupFilePaths:         compactUniqueFilePaths(groupFilePaths),
		primarySidecarSearchPaths: searchPaths,
	}, nil
}

// seriesChildLocalContext carries the local-sidecar context for season- and
// episode-level provider fetches: the series root sidecar directories plus
// the per-season directories and per-episode media file paths derived from
// filename parsing (naming owns structure; NFOs never create it).
type seriesChildLocalContext struct {
	seriesRootPaths      []string
	seasonDirectoryPaths map[int][]string
	episodeFilePaths     map[int]map[int][]string // season -> episode -> media files
}

// buildSeriesChildLocalContext derives the season/episode sidecar context
// from the series' media file paths. Files without a filename-parseable
// episode number contribute nothing — exactly the set fallback synthesis
// also skips.
func buildSeriesChildLocalContext(seriesRootPaths []string, filePaths []string, libraryRoots ...string) seriesChildLocalContext {
	childCtx := seriesChildLocalContext{
		seriesRootPaths:      compactUniqueFilePaths(seriesRootPaths),
		seasonDirectoryPaths: make(map[int][]string),
		episodeFilePaths:     make(map[int]map[int][]string),
	}
	for _, path := range compactUniqueFilePaths(filePaths) {
		hints := naming.ParseFilename(path, "series", libraryRoots...)
		if hints == nil || hints.EpisodeNum == 0 || !hints.SeasonKnown {
			continue
		}
		seasonNum, episodeNum := hints.SeasonNum, hints.EpisodeNum
		dir := filepath.Dir(path)
		if !slices.Contains(childCtx.seasonDirectoryPaths[seasonNum], dir) {
			childCtx.seasonDirectoryPaths[seasonNum] = append(childCtx.seasonDirectoryPaths[seasonNum], dir)
		}
		if childCtx.episodeFilePaths[seasonNum] == nil {
			childCtx.episodeFilePaths[seasonNum] = make(map[int][]string)
		}
		childCtx.episodeFilePaths[seasonNum][episodeNum] = append(childCtx.episodeFilePaths[seasonNum][episodeNum], path)
	}
	return childCtx
}

// seriesChildLocalContextForContent builds the season/episode sidecar context
// from a series' persisted media files (refresh paths, where no scan hints
// are available), given the series' local provider context.
func (s *MetadataService) seriesChildLocalContextForContent(ctx context.Context, contentID string, folderID int, localCtx localProviderContext) (seriesChildLocalContext, error) {
	roots, err := s.configuredNamingRootsForContent(ctx, contentID, folderID)
	if err != nil {
		return seriesChildLocalContext{}, err
	}
	return buildSeriesChildLocalContext(localCtx.primarySidecarSearchPaths, localCtx.allGroupFilePaths, roots...), nil
}

func (s *MetadataService) directorySidecarSearchPathsForFiles(ctx context.Context, files []*models.MediaFile) []string {
	paths, _ := s.collectDirectorySidecarSearchPaths(files, func(file *models.MediaFile) (bool, error) {
		return s.canUseObservedRootForDirectorySidecars(ctx, file.MediaFolderID, file.ObservedRootPath, file.GroupKeyVersion, file.ContentGroupKey), nil
	})
	return paths
}

func (s *MetadataService) collectDirectorySidecarSearchPaths(files []*models.MediaFile, usable func(*models.MediaFile) (bool, error)) ([]string, error) {
	if len(files) == 0 {
		return nil, nil
	}
	// Every episode of a series usually shares one observed root, so check
	// each distinct root and group once rather than once per file.
	type observedRootKey struct {
		folderID        int
		root            string
		groupKeyVersion int
		contentGroupKey string
	}
	checkedRoots := make(map[observedRootKey]bool)
	paths := make([]string, 0, len(files))
	for _, file := range files {
		if file == nil || file.ObservedRootPath == "" {
			continue
		}
		key := observedRootKey{file.MediaFolderID, file.ObservedRootPath, file.GroupKeyVersion, file.ContentGroupKey}
		ok, checked := checkedRoots[key]
		if !checked {
			var err error
			if ok, err = usable(file); err != nil {
				return nil, err
			}
			checkedRoots[key] = ok
		}
		if ok {
			paths = append(paths, file.ObservedRootPath)
		}
	}
	return compactUniqueFilePaths(paths), nil
}

func (s *MetadataService) canUseObservedRootForDirectorySidecars(
	ctx context.Context,
	folderID int,
	observedRootPath string,
	groupKeyVersion int,
	contentGroupKey string,
) bool {
	usable, err := s.observedRootUsableForDirectorySidecars(ctx, folderID, observedRootPath, groupKeyVersion, contentGroupKey)
	if err != nil {
		slog.WarnContext(ctx, "metadata: observed location lookup failed", "component", "metadata",
			"folder_id", folderID,
			"observed_root_path", filepath.Clean(strings.TrimSpace(observedRootPath)),
			"error", err,
		)
		return false
	}
	return usable
}

// observedRootUsableForDirectorySidecars is canUseObservedRootForDirectorySidecars
// that returns a failed lookup instead of reading it as an unusable root.
func (s *MetadataService) observedRootUsableForDirectorySidecars(
	ctx context.Context,
	folderID int,
	observedRootPath string,
	groupKeyVersion int,
	contentGroupKey string,
) (bool, error) {
	cleanRoot := filepath.Clean(strings.TrimSpace(observedRootPath))
	if cleanRoot == "" || cleanRoot == "." {
		return false, nil
	}
	if s == nil || s.observedLocationRepo == nil || folderID <= 0 {
		return true, nil
	}
	location, err := s.observedLocationRepo.Get(ctx, folderID, cleanRoot)
	if err != nil {
		return false, err
	}
	if location == nil || location.ContentGroupCount > 1 {
		return false, nil
	}
	if groupKeyVersion != 0 && location.PrimaryGroupKeyVersion != 0 && location.PrimaryGroupKeyVersion != groupKeyVersion {
		return false, nil
	}
	if contentGroupKey != "" && location.PrimaryContentGroupKey != "" && location.PrimaryContentGroupKey != contentGroupKey {
		return false, nil
	}
	return true, nil
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

func (s *MetadataService) loadRecordedStaleProviderIDs(ctx context.Context, contentID string) (providerIDValueSet, error) {
	staleValues := make(providerIDValueSet)
	if s == nil || s.staleIDRepo == nil || strings.TrimSpace(contentID) == "" {
		return staleValues, nil
	}

	staleIDs, err := s.staleIDRepo.GetByContentID(ctx, contentID)
	if err != nil {
		return nil, fmt.Errorf("loading stale provider ids for %s: %w", contentID, err)
	}
	for _, staleID := range staleIDs {
		if staleID == nil {
			continue
		}
		provider := strings.ToLower(strings.TrimSpace(staleID.Provider))
		providerID := strings.TrimSpace(staleID.ProviderID)
		staleValues.add(provider, providerID)
	}
	return staleValues, nil
}

func suppressProviderIDValues(providerIDs map[string]string, staleValues providerIDValueSet) {
	if len(providerIDs) == 0 || len(staleValues) == 0 {
		return
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
	for provider, staleProviderValues := range staleValues {
		provider = strings.ToLower(strings.TrimSpace(provider))
		for _, key := range keysByProvider[provider] {
			value := normalizeProviderIDComparisonValue(provider, providerIDs[key])
			if _, stale := staleProviderValues[value]; !stale {
				continue
			}
			delete(providerIDs, key)
		}
	}
}

func applyProvider404sToAccumulator(accumulator *MetadataResult, provider404s *provider404State) {
	if accumulator == nil || provider404s == nil {
		return
	}
	accumulator.sameRunStaleProviderIDs = cloneProviderIDValueSet(provider404s.stale)
	suppressProviderIDValues(accumulator.ProviderIDs, provider404s.dropped)
}

// shouldReanchorProviderContentID reports whether a provider-anchored item may
// move to the anchor its current identity derives. A manual refresh may. So may
// an Identify whose correction rejected the item's live anchor
// (anchorRejected): that anchor names a different title, and leaving the item
// on it would let that title merge into this item when it is scanned in. The
// same holds for an initial match whose NFO ids replaced the folder-tag ids
// the scanner anchored the skeleton on. Scheduled jobs, and an Identify that
// confirms or extends the match or only replaces a dead anchor, keep the
// client-visible content_id.
func shouldReanchorProviderContentID(
	contentID string,
	isNew bool,
	mode RefreshMode,
	anchorRejected bool,
) bool {
	if isNew || !contentid.IsProviderAnchored(contentID) {
		return false
	}
	return mode == ModeManualRefresh || ((mode == ModeIdentify || mode == ModeInitialMatch) && anchorRejected)
}

// identityCorrectionRejectsLiveAnchor reports whether a correction rejected
// the provider ID contentID is anchored on while that ID is not known to be
// dead. A dead anchor (recorded stale, or 404 in this run) can't be claimed by
// another title, so it is safe to keep. resolved holds the provider IDs this
// run's providers returned for the chosen identity: when they restate the
// anchor, the choice only added a provider ID to the same title (an admin
// picking a show's TVDB candidate for an item anchored on its TMDB ID), so the
// anchor stands.
func identityCorrectionRejectsLiveAnchor(
	contentID string,
	resolved map[string]string,
	rejected providerIDValueSet,
	dead ...providerIDValueSet,
) bool {
	provider, providerID, ok := contentid.ProviderAnchor(contentID)
	if !ok || !rejected.has(provider, providerID) {
		return false
	}
	if value := strings.TrimSpace(resolved[provider]); value != "" &&
		normalizeProviderIDComparisonValue(provider, value) == normalizeProviderIDComparisonValue(provider, providerID) {
		return false
	}
	for _, set := range dead {
		if set.has(provider, providerID) {
			return false
		}
	}
	return true
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
	req.callerProviderIDs = maps.Clone(req.ProviderIDs)
	durableIDs, err := s.loadDurableProviderIDs(ctx, req.ContentID)
	if err != nil {
		return req, err
	}
	req.durableProviderIDs = maps.Clone(durableIDs)
	req.recordedStaleProviderIDs, err = s.loadRecordedStaleProviderIDs(ctx, req.ContentID)
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
	suppressProviderIDValues(durableIDs, req.recordedStaleProviderIDs)
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
	var libraryRoots []string
	if contentType == matchContentTypeSeries {
		if req.Hints != nil {
			libraryRoots = req.Hints.LibraryRoots
		}
		if libraryRoots == nil {
			var err error
			libraryRoots, err = s.configuredNamingRootsForContent(ctx, req.ContentID, scopeFolderID)
			if err != nil {
				return nil, err
			}
		}
		if req.Hints != nil {
			hints := *req.Hints
			hints.LibraryRoots = libraryRoots
			req.Hints = &hints
		}
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
	sanitizeCanonicalProviderIDsInPlace(accumulatedIDs)

	// Track provider 404s as stale external IDs. This applies to initial match
	// as well so bad embedded folder/file IDs can be recorded and dropped
	// without surfacing as generic provider failures.
	provider404s := newProvider404State()
	recordStaleIDs := req.ContentID != ""
	var matchDecision *MatchDecision
	var providerMatchErrors []error
	// nfoReplacedTagIDs holds the folder- and filename-tag ids an NFO's id set
	// (nfoHintIDs) replaced at initial match; they are rejected once an NFO id
	// a remote provider returned (nfoVerifiedIDs) survives the lookups.
	var nfoReplacedTagIDs, nfoHintIDs, nfoVerifiedIDs map[string]string
	quarantinedProviderIDKeys := make(map[string]struct{})
	replacedProviderIDKeys := make(map[string]struct{})
	rejectedIdentityIDs := make(providerIDValueSet)
	// skipLocalProvider leaves the local (identity-hint) provider out of every
	// item-level phase: an Identify, and a refresh whose local files conflict
	// with an admin's Identify choice.
	skipLocalProvider := req.Mode == ModeIdentify

	switch req.Mode {
	case ModeInitialMatch:
		if req.Hints == nil {
			return nil, fmt.Errorf("initial match requires hints")
		}
		selectionHints := sanitizedMatchHintProviderIDs(req.Hints)
		// Seed external IDs from hints. Hints.ContentID is Silo's local
		// skeleton item ID, not a searchable provider ID.
		if selectionHints.FileHash != "" {
			accumulatedIDs["oshash"] = selectionHints.FileHash
		}
		if selectionHints.TmdbID != "" {
			accumulatedIDs["tmdb"] = selectionHints.TmdbID
		}
		if selectionHints.TvdbID != "" {
			accumulatedIDs["tvdb"] = selectionHints.TvdbID
		}
		if selectionHints.ImdbID != "" {
			accumulatedIDs["imdb"] = selectionHints.ImdbID
		}
		// Add filepath for local providers.
		if req.Hints.RepresentativeFilePath != "" {
			accumulatedIDs["_filepath"] = req.Hints.RepresentativeFilePath
		} else if req.Hints.FilePath != "" {
			accumulatedIDs["_filepath"] = req.Hints.FilePath
		}
		suppressProviderIDValues(accumulatedIDs, req.recordedStaleProviderIDs)
		// The folder- and filename-tag ids an NFO's ids may replace.
		tagIDs := unprotectedTrustedIDs(accumulatedIDs, req.ProviderIDs)

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
		// Seed curated identity hints (NFO <uniqueid>) before Phase-1 search.
		// At initial match NFO hints beat folder-name-derived hints, but stored
		// durable IDs (injected into req.ProviderIDs) beat NFO hints.
		protectedKeys := make(map[string]bool, len(req.ProviderIDs))
		for key := range req.ProviderIDs {
			protectedKeys[key] = true
		}
		localHints := collectLocalIdentityHints(ctx, itemChain, searchQuery, req.recordedStaleProviderIDs)
		if len(req.durableProviderIDs) > 0 {
			// A stored item keeps its durable identity; local metadata for
			// another title is left out (identify_sticky.go).
			skipLocalProvider, err = s.localIdentityConflict(ctx, req.Mode, req.ContentID, localHints, canonicalIdentityProviderIDs(req.durableProviderIDs), req.recordedStaleProviderIDs)
			if err != nil {
				return nil, err
			}
			if skipLocalProvider {
				localHints = nil
			}
		}
		wonHints := applyLocalIdentityHints(ctx, localHints, accumulatedIDs, protectedKeys)
		if len(wonHints) > 0 {
			staleHints := staleLocalHints(ctx, itemChain, searchQuery, req.recordedStaleProviderIDs)
			restoredKeys := dropStaleHints(wonHints, accumulatedIDs, tagIDs, staleHints)
			// The NFO's ids replace the tag ids as a set, except a tag id that
			// came back because the NFO's id for its provider is dead.
			replaceProtected := protectedKeys
			if len(restoredKeys) > 0 {
				replaceProtected = maps.Clone(protectedKeys)
				for _, key := range restoredKeys {
					replaceProtected[key] = true
				}
			}
			droppedHintKeys := dropIDsNotRestatedByHints(ctx, wonHints, accumulatedIDs, replaceProtected)
			if len(wonHints) > 0 {
				nfoReplacedTagIDs, nfoHintIDs = tagIDs, wonHints
				if !chainChecksIDs(itemChain) {
					// Nothing else can look the NFO's ids up, so they stand.
					nfoVerifiedIDs = maps.Clone(wonHints)
				}
			}
			hintsCopy := *selectionHints
			overrideHintIDs(&hintsCopy, wonHints)
			clearHintIDs(&hintsCopy, droppedHintKeys)
			selectionHints = &hintsCopy
		}
		searchQuery = suppressTitleYearFallbackForTrustedIDs(searchQuery)
		searchProviders := func(query SearchQuery) []SearchResult {
			resultsForQuery := make([]SearchResult, 0)
			for _, p := range itemChain {
				sp, ok := p.(SearchProvider)
				if !ok {
					continue
				}
				if _, local := p.(IdentityHintProvider); local && skipLocalProvider {
					continue
				}
				results, searchErr := sp.Search(ctx, query)
				if localSidecarDenied(searchErr) {
					slog.WarnContext(ctx, "metadata: matching without an unreadable local sidecar", "component", "metadata",
						"provider", p.Slug(), "error", searchErr)
					continue
				}
				if searchErr != nil {
					if handleProvider404(provider404s, accumulatedIDs, p.Slug(), searchErr,
						"title", query.Title,
						"year", query.Year,
					) {
						providerMatchErrors = append(providerMatchErrors, searchErr)
						continue
					}
					slog.WarnContext(ctx, "metadata: search provider error", "component", "metadata",
						"provider", p.Slug(), "error", searchErr)
					providerMatchErrors = append(providerMatchErrors, searchErr)
					continue
				}
				slog.DebugContext(ctx, "metadata: provider search result", "component", "metadata",
					"provider", p.Slug(),
					"query_title", query.Title,
					"query_year", query.Year,
					"result_count", len(results),
				)
				for _, result := range results {
					if searchResultConflictsWithTrustedIDs(accumulatedIDs, result.ProviderIDs) {
						slog.WarnContext(ctx, "metadata: retaining conflicting search result for rejection diagnostics", "component", "metadata",
							"provider", p.Slug(),
							"title", query.Title,
							"year", query.Year,
							"hinted_ids", accumulatedIDs,
							"candidate_ids", result.ProviderIDs,
						)
					}
					resultsForQuery = append(resultsForQuery, result)
				}
				if _, local := p.(IdentityHintProvider); !local {
					for _, r := range results {
						nfoVerifiedIDs = recordVerifiedIDs(nfoVerifiedIDs, r.ProviderIDs, nfoHintIDs)
					}
				}
			}
			return resultsForQuery
		}

		allResults := searchProviders(searchQuery)
		// A dead NFO identity leaves the tag ids alone: when a provider
		// rejected every id the NFO named, search again with the ids it
		// replaced, less any the providers just rejected. Clearing the NFO's
		// ids stops their tag ids from being rejected later. A transient
		// failure is not a rejection; that match waits instead.
		if len(nfoReplacedTagIDs) > 0 && !anyProviderIDKept(accumulatedIDs, nfoHintIDs) &&
			!hasTransientMatchError(providerMatchErrors) {
			maps.Copy(accumulatedIDs, nfoReplacedTagIDs)
			suppressProviderIDValues(accumulatedIDs, provider404s.dropped)
			selectionHints = sanitizedMatchHintProviderIDs(req.Hints)
			for key := range nfoReplacedTagIDs {
				if accumulatedIDs[key] == "" {
					clearHintIDs(selectionHints, []string{key})
				}
			}
			nfoReplacedTagIDs, nfoHintIDs, nfoVerifiedIDs = nil, nil, nil
			allResults = searchProviders(searchQuery)
		}

		candidates := NormalizeCandidatesForLanguage(allResults, contentType, searchQuery.Language)
		slog.DebugContext(ctx, "metadata: search candidates assembled", "component", "metadata",
			"query_title", searchQuery.Title,
			"query_year", searchQuery.Year,
			"raw_results", len(allResults),
			"candidates", len(candidates),
		)
		providerPriority := make([]string, 0, len(itemChain))
		for _, p := range itemChain {
			providerPriority = append(providerPriority, p.Slug())
		}
		trySelection := func(hints *MatchHints, selectionCandidates []MatchCandidate) (*MatchCandidate, bool) {
			selected, ok := selectInitialMatchCandidate(hints, selectionCandidates, providerPriority)
			if ok {
				return selected, true
			}
			episodeWinner, validationErrors := validateSeriesMatchByEpisodes(
				ctx, hints, selectionCandidates, itemChain, searchQuery.Language,
			)
			providerMatchErrors = append(providerMatchErrors, validationErrors...)
			return episodeWinner, episodeWinner != nil
		}

		effectiveHints := selectionHints
		// Every identity's results, primary first, for the alias check below.
		type aliasAttempt struct {
			hints      *MatchHints
			candidates []MatchCandidate
		}
		aliasAttempts := []aliasAttempt{{selectionHints, candidates}}
		winner, matched := trySelection(selectionHints, candidates)
		// Structured IDs remain decisive and never fall back to a title search.
		// For title/year identities, try independently parsed path hypotheses only
		// after the primary identity fails. Each attempt is bounded and scored
		// against the identity that produced its provider query.
		if !matched && !trustedHintIDsPresent(selectionHints) {
			for _, alternate := range compactAlternateMatchIdentities(selectionHints) {
				// A bare year may belong to the series title. Keep any primary
				// candidates for review rather than replacing them with that guess.
				if alternate.Source == seriesReleaseYearHintSource && len(candidates) != 0 {
					continue
				}
				alternateHints := *selectionHints
				alternateHints.Title = alternate.Title
				alternateHints.Year = alternate.Year
				alternateHints.HintSource = alternate.Source
				alternateHints.AlternateIdentities = nil

				alternateQuery := searchQuery
				alternateQuery.Title = alternate.Title
				alternateQuery.Year = alternate.Year
				alternateResults := searchProviders(alternateQuery)
				alternateCandidates := NormalizeCandidatesForLanguage(alternateResults, contentType, alternateQuery.Language)
				if len(alternateCandidates) == 0 {
					continue
				}
				aliasAttempts = append(aliasAttempts, aliasAttempt{&alternateHints, alternateCandidates})
				alternateWinner, alternateMatched := trySelection(&alternateHints, alternateCandidates)
				if len(candidates) == 0 || alternateMatched || bestCandidateScore(&alternateHints, alternateCandidates) > bestCandidateScore(effectiveHints, candidates) {
					candidates = alternateCandidates
					effectiveHints = &alternateHints
				}
				if alternateMatched {
					winner = alternateWinner
					matched = true
					break
				}
			}
		}
		// Last: a search may still hold the right result under a title it didn't
		// return, such as a regional release title, and the provider's details
		// can confirm it. Check each identity's results in the order searched.
		if !matched {
			aliasCtx, cancelAlias := context.WithTimeout(ctx, titleAliasValidationTimeout)
			aliasBudget := maxTitleAliasValidationCandidates
			for _, attempt := range aliasAttempts {
				aliasWinner, aliasErrors := validateMatchByProviderTitleAliases(
					aliasCtx, attempt.hints, attempt.candidates, itemChain, searchQuery.Language, &aliasBudget,
				)
				providerMatchErrors = append(providerMatchErrors, aliasErrors...)
				if aliasWinner != nil {
					winner, matched = aliasWinner, true
					effectiveHints, candidates = attempt.hints, attempt.candidates
					break
				}
				if len(aliasErrors) > 0 {
					break
				}
			}
			cancelAlias()
		}
		trustedIDTypeMismatch := false
		if !matched && len(candidates) == 0 && trustedHintIDsPresent(selectionHints) {
			if mismatchCandidates := s.trustedIDTypeMismatchCandidates(
				ctx, selectionHints, accumulatedIDs, searchQuery.Language, resolveChain,
			); len(mismatchCandidates) > 0 {
				candidates = mismatchCandidates
				effectiveHints = selectionHints
				trustedIDTypeMismatch = true
			}
		}
		matchDecision = buildMatchDecision(effectiveHints, candidates, winner, matched, providerMatchErrors)
		if trustedIDTypeMismatch {
			matchDecision.Outcome = MatchOutcomeTrustedIDTypeMismatch
		}
		if matched && winner != nil {
			maps.Copy(replacedProviderIDKeys,
				applyCandidateProviderIDConsensus(accumulatedIDs, winner, quarantinedProviderIDKeys))
		}

	case ModeIdentify:
		// Use user-provided IDs directly.
		maps.Copy(accumulatedIDs, req.ProviderIDs)
		sanitizeCanonicalProviderIDsInPlace(accumulatedIDs)
		var existing *models.MediaItem
		if req.ContentID != "" {
			var err error
			existing, err = s.itemRepo.GetByID(ctx, req.ContentID)
			switch {
			case errors.Is(err, catalog.ErrItemNotFound):
				existing = nil
			case err != nil:
				// The correction below compares the choice with the stored
				// columns; guessing them missing would restore the wrong match.
				return nil, fmt.Errorf("loading item to identify: %w", err)
			}
		}
		// An admin whose choice corrects the item's match (see
		// identityChoiceCorrects) sets its identity: the stored identity IDs
		// they didn't name came from the previous match, so they're dropped
		// rather than fetched and kept, and the providers re-supply the right
		// ones. A choice that confirms or extends the match keeps them.
		chosen := canonicalIdentityProviderIDs(req.callerProviderIDs)
		if stored := storedItemIdentity(existing, req.durableProviderIDs); identityChoiceCorrects(chosen, stored) {
			for _, key := range trustedSearchIDKeys {
				if chosen[key] == "" {
					delete(accumulatedIDs, key)
				}
			}
			rejectIdentityProviderIDs(rejectedIdentityIDs, chosen, storedIdentityProviderIDs(existing), req.durableProviderIDs)
		}
		if existing != nil && contentType == "" {
			contentType = existing.Type
			itemLevel = providerChainContentLevel(contentType)
			var err error
			itemChain, err = resolveChain(itemLevel)
			if err != nil {
				return nil, err
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
		sanitizeCanonicalProviderIDsInPlace(accumulatedIDs)
		contentType = existing.Type
		suppressProviderIDValues(accumulatedIDs, req.recordedStaleProviderIDs)

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
		// Seed curated identity hints (NFO <uniqueid>) before the refresh
		// search. On a scheduled refresh stored durable IDs win over NFO hints
		// (no background identity flips); on a manual refresh NFO hints win,
		// which is the recovery path for a corrected NFO.
		var protectedKeys map[string]bool
		if req.Mode == ModeScheduledRefresh {
			protectedKeys = make(map[string]bool, len(accumulatedIDs))
			for key := range accumulatedIDs {
				protectedKeys[key] = true
			}
		}
		// Local metadata for a different title than the one kept is left
		// out (identify_sticky.go).
		stored := storedItemIdentity(existing, req.durableProviderIDs)
		localHints := collectLocalIdentityHints(ctx, itemChain, searchQuery, req.recordedStaleProviderIDs)
		skipLocalProvider, err = s.localIdentityConflict(ctx, req.Mode, req.ContentID, localHints, stored, req.recordedStaleProviderIDs)
		if err != nil {
			return nil, err
		}
		if skipLocalProvider {
			localHints = nil
		}
		wonHints := applyLocalIdentityHints(ctx, localHints, accumulatedIDs, protectedKeys)
		hinted := canonicalIdentityProviderIDs(wonHints)
		if req.Mode == ModeManualRefresh && identityChoiceCorrects(hinted, stored) {
			// The stored IDs came from the match the corrected NFO overrides,
			// so any the NFO doesn't restate are dropped, not re-fetched.
			for _, key := range trustedSearchIDKeys {
				if wonHints[key] == "" {
					delete(accumulatedIDs, key)
				}
			}
			searchQuery.ProviderIDs = accumulatedIDs
			rejectIdentityProviderIDs(rejectedIdentityIDs, hinted, storedIdentityProviderIDs(existing), req.durableProviderIDs)
		}
		searchQuery = suppressTitleYearFallbackForTrustedIDs(searchQuery)
		allResults := make([]SearchResult, 0)
		for _, p := range itemChain {
			sp, ok := p.(SearchProvider)
			if !ok {
				continue
			}
			if _, local := p.(IdentityHintProvider); local && skipLocalProvider {
				continue
			}
			results, searchErr := sp.Search(ctx, searchQuery)
			if searchErr != nil {
				if handleProvider404(provider404s, accumulatedIDs, p.Slug(), searchErr,
					"content_id", req.ContentID,
				) {
					continue
				}
				slog.WarnContext(ctx, "metadata: refresh search error", "component", "metadata",
					"provider", p.Slug(), "error", searchErr)
				continue
			}
			for _, result := range results {
				if searchResultConflictsWithTrustedIDs(accumulatedIDs, result.ProviderIDs) {
					slog.WarnContext(ctx, "metadata: retaining conflicting refresh search result for trusted-id rejection", "component", "metadata",
						"provider", p.Slug(), "content_id", req.ContentID,
						"hinted_ids", accumulatedIDs, "candidate_ids", result.ProviderIDs)
				}
				allResults = append(allResults, result)
			}
		}
		candidates := NormalizeCandidatesForLanguage(allResults, contentType, searchQuery.Language)
		selectionItem := mediaItemWithProviderIDs(existing, accumulatedIDs)
		if winner, ok := selectRefreshMatchCandidate(selectionItem, wonHints, candidates); ok && winner != nil {
			maps.Copy(replacedProviderIDKeys,
				applyCandidateProviderIDConsensus(accumulatedIDs, winner, quarantinedProviderIDKeys))
		}
	}
	if req.Mode != ModeIdentify {
		suppressProviderIDValues(accumulatedIDs, req.recordedStaleProviderIDs)
	}

	// Phase 2: Metadata — all MetadataProviders run, results merge into accumulator.
	recordedStaleIDs := cloneProviderIDValueSet(req.recordedStaleProviderIDs)
	if req.Mode == ModeIdentify {
		// A caller-provided identify value is an explicit retry. If it succeeds,
		// allow it to replace its old stale record; a new 404 will still enter the
		// same-run rejection set below.
		for provider, providerID := range req.ProviderIDs {
			recordedStaleIDs.remove(provider, providerID)
		}
	}
	accumulator := &MetadataResult{
		ProviderIDs:              copyMap(accumulatedIDs),
		recordedStaleProviderIDs: recordedStaleIDs,
	}
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

	// enrichedBy lists the enrichment-only providers that answered, so the bulk
	// enrichment pass does not look the item up with them again.
	var enrichedBy []string
	// localMetadataUnreadable marks the levels where a fill-empty pass went on
	// without a local sidecar it could not read (withLocalMetadataUnreadable).
	var localMetadataUnreadable localArtworkLevel
	for _, p := range itemChain {
		mp, ok := p.(MetadataProvider)
		if !ok {
			continue
		}
		_, isIdentityHinter := p.(IdentityHintProvider)
		// A manual Identify is the user's explicit identification: the local
		// hint provider (NFO) is skipped entirely so a stale sidecar cannot
		// re-apply its title/ids over the user's choice in the same operation,
		// nor in a later refresh while its files still name another title.
		if isIdentityHinter && skipLocalProvider {
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
		if keepStoredValuesOnLocalReadError(req.Mode, err) {
			return nil, fmt.Errorf("metadata: %s: %w", p.Slug(), err)
		}
		if isIdentityHinter && errors.Is(err, ErrLocalMetadataUnavailable) {
			localMetadataUnreadable |= localArtworkItem
		}
		if err != nil {
			if handleProvider404(provider404s, accumulatedIDs, p.Slug(), err,
				"content_id", req.ContentID,
			) {
				if req.Mode == ModeInitialMatch {
					providerMatchErrors = append(providerMatchErrors, err)
				}
				continue
			}
			if isRoutineEnrichmentError(p, err) {
				slog.DebugContext(ctx, "metadata: enrichment provider cannot answer now", "component", "metadata",
					"provider", p.Slug(), "error", err)
			} else {
				slog.WarnContext(ctx, "metadata: provider error", "component", "metadata",
					"provider", p.Slug(), "error", err)
			}
			if req.Mode == ModeInitialMatch && !localSidecarDenied(err) {
				providerMatchErrors = append(providerMatchErrors, err)
			}
			continue
		}
		if result == nil || !result.HasMetadata {
			continue
		}
		if isEnrichmentProvider(p) {
			enrichedBy = append(enrichedBy, p.Slug())
		}
		if !isIdentityHinter {
			nfoVerifiedIDs = recordVerifiedIDs(nfoVerifiedIDs, result.ProviderIDs, nfoHintIDs)
		}
		foldProviderResult(accumulator, result, req.Language, p.Slug(), isIdentityHinter, quarantinedProviderIDKeys)
		// Bootstrap: feed new IDs to subsequent providers.
		accumulatedIDs = accumulator.ProviderIDs
	}
	if len(quarantinedProviderIDKeys) > 0 {
		accumulator.quarantinedProviderIDKeys = maps.Clone(quarantinedProviderIDKeys)
	}
	// Search and item-metadata 404s are identity evidence. Apply them before
	// artwork and child phases so a copied accumulator cannot reintroduce a
	// rejected ID. Detail responses may also repeat a value recorded stale by an
	// earlier run, so suppress that set again after all detail merges. Scoped
	// 404s below are deliberately non-destructive.
	suppressProviderIDValues(accumulator.ProviderIDs, accumulator.recordedStaleProviderIDs)
	applyProvider404sToAccumulator(accumulator, provider404s)
	accumulatedIDs = accumulator.ProviderIDs
	// Tag ids an NFO replaced are rejected for this run once a remote
	// provider returned one of the NFO's ids and it survived the lookups, so
	// persistence drops them from the scanner's tag-anchored skeleton and
	// moves it to the NFO's anchor (see shouldReanchorProviderContentID). A
	// dead or unchecked NFO id leaves the tag ids alone.
	if anyProviderIDKept(accumulatedIDs, nfoVerifiedIDs) {
		rejectIdentityProviderIDs(rejectedIdentityIDs, accumulatedIDs, nfoReplacedTagIDs)
	}
	if len(replacedProviderIDKeys) > 0 {
		accumulator.replacedProviderIDKeys = maps.Clone(replacedProviderIDKeys)
	}
	if len(rejectedIdentityIDs) > 0 {
		accumulator.rejectedIdentityProviderIDs = rejectedIdentityIDs
	}
	// The NFO may name a different title than the search found; retry once it
	// can be read instead of committing an identity it may contradict. Retry
	// too when the NFO's ids replaced the tag ids and a transient failure kept
	// every remote provider from returning one of them; a failure after one
	// did (an enrichment quota, say) does not hold the match. Retry as well
	// when a provider confirmed an NFO id in search and then rejected it (a
	// 404 on its details): the retry, with that id recorded stale, searches
	// with the tag ids instead of keeping them under the NFO's fields. Later
	// phases add no match errors, so this is decided before their fetches.
	localUnreadable := localMetadataTemporarilyUnavailable(providerMatchErrors)
	nfoIDsUnconfirmed := len(nfoReplacedTagIDs) > 0 && !anyProviderIDKept(accumulatedIDs, nfoVerifiedIDs)
	nfoIDsUnchecked := nfoIDsUnconfirmed && hasTransientMatchError(providerMatchErrors)
	nfoIDsRejected := nfoIDsUnconfirmed && anyProviderIDIn(provider404s.dropped, nfoVerifiedIDs)
	if req.Mode == ModeInitialMatch && (localUnreadable || nfoIDsUnchecked || nfoIDsRejected) {
		upsertStaleProviderIDValues(ctx, s.staleIDRepo, req.ContentID, provider404s.stale)
		if matchDecision == nil {
			matchDecision = &MatchDecision{Threshold: automaticMatchAcceptanceFloor}
		}
		matchDecision.Outcome = MatchOutcomeProviderTransient
		matchDecision.localSidecarUnreadable = localUnreadable
		return &ProcessResult{Updated: false, Decision: matchDecision}, nil
	}
	// Phase 3: Images — all ImageProviders run, collect all available images.
	var allImages []RemoteImage
	var localArtworkSearched localArtworkLevel
	for _, p := range itemChain {
		ip, ok := p.(ImageProvider)
		if !ok {
			continue
		}
		// When the local hint provider (NFO) is skipped (skipLocalProvider), it
		// is skipped across every phase: its sidecar art is resolved by file
		// path, not by the kept id, so a stale poster/fanart must not be
		// re-applied over that identity.
		_, isIdentityHinter := p.(IdentityHintProvider)
		if isIdentityHinter && skipLocalProvider {
			continue
		}
		images, err := ip.GetImages(ctx, ImageRequest{
			ProviderIDs:               accumulatedIDs,
			ContentType:               contentType,
			Language:                  req.Language,
			RepresentativeFilePath:    representativeFilePath,
			AllGroupFilePaths:         allGroupFilePaths,
			PrimarySidecarSearchPaths: primarySidecarSearchPaths,
		})
		if err != nil {
			if handleScopedProvider404(p.Slug(), accumulatedIDs, err,
				"content_id", req.ContentID,
			) {
				continue
			}
			slog.WarnContext(ctx, "metadata: image provider error", "component", "metadata",
				"provider", p.Slug(), "error", err)
			continue
		}
		if isIdentityHinter {
			localArtworkSearched |= localArtworkItem
		}
		allImages = append(allImages, images...)
	}
	if !s.localArtworkUsable() {
		allImages = withoutLocalImages(allImages)
	}

	// Phase 4a: Seasons — resolve with "season" content level.
	var allSeasons []SeasonResult
	var allEpisodes []EpisodeResult
	var seasonChain, episodeChain []Provider
	if contentType == "series" {
		if libraryRoots == nil {
			libraryRoots, err = s.configuredNamingRootsForContent(ctx, req.ContentID, scopeFolderID)
			if err != nil {
				return nil, err
			}
		}
		childCtx := buildSeriesChildLocalContext(
			primarySidecarSearchPaths,
			append(append([]string(nil), representativeFilePath), allGroupFilePaths...),
			libraryRoots...,
		)
		seasonChain, err = resolveChain("season")
		if err != nil {
			return nil, err
		}
		seasonResults := make(map[int]*SeasonResult)
		for _, p := range seasonChain {
			ep, ok := p.(EpisodeProvider)
			if !ok {
				continue
			}
			// A skipped local hint provider (NFO): a stale season.nfo must not
			// overlay season names on the identity the run keeps.
			_, isIdentityHinter := p.(IdentityHintProvider)
			if isIdentityHinter && skipLocalProvider {
				continue
			}
			seasons, err := ep.GetSeasons(ctx, SeasonsRequest{
				ProviderIDs:          accumulatedIDs,
				ContentType:          contentType,
				Language:             req.Language,
				SeriesRootPaths:      childCtx.seriesRootPaths,
				SeasonDirectoryPaths: childCtx.seasonDirectoryPaths,
				ShowFilePaths:        showFilePaths(childCtx.episodeFilePaths),
			})
			if keepStoredValuesOnLocalReadError(req.Mode, err) {
				return nil, fmt.Errorf("metadata: %s seasons: %w", p.Slug(), err)
			}
			if err != nil {
				// A missing season endpoint is scoped metadata, not proof that the
				// parent series identity is stale.
				if handleScopedProvider404(p.Slug(), accumulatedIDs, err,
					"content_id", req.ContentID,
					"season", 0,
				) {
					continue
				}
				slog.WarnContext(ctx, "metadata: season provider error", "component", "metadata",
					"provider", p.Slug(), "error", err)
				if !errors.Is(err, ErrLocalMetadataUnavailable) {
					continue
				}
				// A fill-empty pass keeps the seasons the local provider could read.
				localMetadataUnreadable |= localArtworkSeason
			}
			if !s.localArtworkUsable() {
				dropLocalSeasonArtwork(seasons)
			}
			if isIdentityHinter {
				localArtworkSearched |= localArtworkSeason
			}
			accumulateSeasonResults(seasonResults, seasons, req.Language)
		}
		allSeasons = flattenSeasonResults(seasonResults)

		// Phase 4b: Episodes — resolve with "episode" content level. The
		// season set is the provider-returned seasons plus any filename-
		// derived on-disk seasons, so local episode NFOs are read even when
		// no provider returned their season (persist then creates the
		// implicit "Season N" rows).
		episodeSeasonNumbers := make([]int, 0, len(allSeasons)+len(childCtx.episodeFilePaths))
		for _, season := range allSeasons {
			episodeSeasonNumbers = append(episodeSeasonNumbers, season.SeasonNumber)
		}
		for seasonNumber := range childCtx.episodeFilePaths {
			if !slices.Contains(episodeSeasonNumbers, seasonNumber) {
				episodeSeasonNumbers = append(episodeSeasonNumbers, seasonNumber)
			}
		}
		sort.Ints(episodeSeasonNumbers)
		if len(episodeSeasonNumbers) > 0 {
			episodeChain, err = resolveChain("episode")
			if err != nil {
				return nil, err
			}
			episodeResults := make(map[episodeResultKey]*EpisodeResult)
			showFiles := showFilePaths(childCtx.episodeFilePaths)
			for _, seasonNumber := range episodeSeasonNumbers {
				for _, p := range episodeChain {
					ep, ok := p.(EpisodeProvider)
					if !ok {
						continue
					}
					// A skipped local hint provider (NFO): stale episode NFOs/thumbs
					// must not overlay the identity the run keeps.
					_, isIdentityHinter := p.(IdentityHintProvider)
					if isIdentityHinter && skipLocalProvider {
						continue
					}
					episodes, err := ep.GetEpisodes(ctx, EpisodesRequest{
						ProviderIDs:      accumulatedIDs,
						SeasonNumber:     seasonNumber,
						Language:         req.Language,
						SeriesRootPaths:  childCtx.seriesRootPaths,
						EpisodeFilePaths: childCtx.episodeFilePaths[seasonNumber],
						ShowFilePaths:    showFiles,
					})
					if keepStoredValuesOnLocalReadError(req.Mode, err) {
						return nil, fmt.Errorf("metadata: %s episodes: %w", p.Slug(), err)
					}
					if err != nil {
						if handleScopedProvider404(p.Slug(), accumulatedIDs, err,
							"content_id", req.ContentID,
							"season", seasonNumber,
						) {
							continue
						}
						slog.WarnContext(ctx, "metadata: episode provider error", "component", "metadata",
							"provider", p.Slug(), "season", seasonNumber, "error", err)
						if !errors.Is(err, ErrLocalMetadataUnavailable) {
							continue
						}
						// A fill-empty pass keeps the episodes the local provider could read.
						localMetadataUnreadable |= localArtworkEpisode
					}
					if !s.localArtworkUsable() {
						dropLocalEpisodeArtwork(episodes)
					}
					if isIdentityHinter {
						localArtworkSearched |= localArtworkEpisode
						markLocalEpisodeTitles(episodes)
					}
					accumulateEpisodeResults(episodeResults, episodes, req.Language)
				}
			}
			allEpisodes = flattenEpisodeResults(episodeResults)
		}
	}
	// Phase 5: Merge & Persist.
	if !accumulator.HasMetadata && accumulator.Title == "" {
		// Record stale IDs for providers that returned 404.
		upsertStaleProviderIDValues(ctx, s.staleIDRepo, req.ContentID, provider404s.stale)
		if matchDecision == nil {
			matchDecision = &MatchDecision{Outcome: MatchOutcomeMetadataEmpty, Threshold: automaticMatchAcceptanceFloor}
		} else if matchDecision.Outcome == MatchOutcomeMatched {
			matchDecision.Outcome = MatchOutcomeMetadataEmpty
		}
		if hasTransientMatchError(providerMatchErrors) {
			matchDecision.Outcome = MatchOutcomeProviderTransient
		} else if len(providerMatchErrors) > 0 &&
			(matchDecision.Outcome == MatchOutcomeNoCandidates || matchDecision.Outcome == MatchOutcomeMetadataEmpty) {
			// Optional corroboration failures must not turn a successfully
			// completed search with rejected candidates into a permanent provider
			// failure. The candidate decision remains actionable and deterministic.
			matchDecision.Outcome = MatchOutcomeProviderPermanent
		}
		return &ProcessResult{Updated: false, Decision: matchDecision}, nil
	}

	// Text the providers lack in req.Language is fetched in the fallback
	// language now, before persistence takes its locks.
	req.fallback = s.fetchFallbackText(ctx, req, contentType, accumulatedIDs, accumulator, allSeasons, allEpisodes,
		fallbackSources{item: itemChain, season: seasonChain, episode: episodeChain})

	ctx = withLocalMetadataUnreadable(ctx, localMetadataUnreadable)
	if s.localArtworkUsable() {
		ctx = withLocalArtworkSearched(ctx, localArtworkSearched)
	}
	result, err := s.mergeAndPersist(ctx, req, accumulator, allImages, allSeasons, allEpisodes, contentType)
	if err != nil {
		return nil, err
	}
	if result != nil && strings.TrimSpace(result.ContentID) != "" {
		if err := s.persistItemAliases(ctx, result.ContentID, req.Language, accumulator); err != nil {
			return nil, err
		}
		s.recordEnrichedBy(ctx, result.ContentID, enrichedBy)
	}

	// Refresh stale ID records on successful refresh: clear anything explicitly
	// resolved, then retain every earlier or current rejected value.
	// Stale-ID follow-up targets the canonical content ID the item was
	// persisted/merged into. When mergeAndPersist canonicalizes this item into
	// an existing one, req.ContentID is the now-deleted source: clearing or
	// recording stale IDs against it would touch nothing (or FK-violate), so
	// the still-404ing providers must be recorded on result.ContentID instead.
	// recordStaleIDs preserves the original gating so a content-id-less refresh
	// that canonicalizes into an existing item does not wipe that item's stale
	// rows without re-recording any.
	followUpContentID := refreshFollowUpContentID(req.ContentID, result)
	if s.staleIDRepo != nil && recordStaleIDs && followUpContentID != "" {
		if delErr := s.staleIDRepo.DeleteByContentID(ctx, followUpContentID); delErr != nil {
			slog.WarnContext(ctx, "metadata: failed to clear stale IDs after refresh", "component", "metadata",
				"content_id", followUpContentID, "error", delErr)
		}
		// Explicit ModeIdentify retries remove a successful value from
		// recordedStaleProviderIDs; current-run 404s add it back through stale.
		staleValues := cloneProviderIDValueSet(accumulator.recordedStaleProviderIDs)
		for provider, providerIDs := range provider404s.stale {
			for providerID := range providerIDs {
				staleValues.add(provider, providerID)
			}
		}
		upsertStaleProviderIDValues(ctx, s.staleIDRepo, followUpContentID, staleValues)
	}
	if result != nil && strings.TrimSpace(result.ContentID) != "" {
		syncCtx := ctx
		// A claimed item merged into another one hands its counted attempt to
		// the item it became. The merge keeps the higher attempt count of the
		// two rows, so a merge into a row already at or past the terminal count
		// can repeat the notice or miss it; it changes no scheduling.
		if result.ContentID != req.ContentID && refreshAttemptCounted(ctx, RefreshTargetItem, req.ContentID) {
			syncCtx = withCountedRefreshAttempt(ctx, RefreshTargetItem, result.ContentID)
		}
		if syncErr := s.syncRefreshDebtForItem(syncCtx, result.ContentID); syncErr != nil {
			slog.WarnContext(ctx, "metadata: failed to sync refresh debt after successful metadata refresh", "component", "metadata",
				"content_id", result.ContentID,
				"error", syncErr)
		}
	}

	if result != nil {
		result.Decision = matchDecision
	}
	return result, nil
}

func buildMatchDecision(hints *MatchHints, candidates []MatchCandidate, winner *MatchCandidate, matched bool, providerErrors []error) *MatchDecision {
	decision := &MatchDecision{CandidateCount: len(candidates), Threshold: automaticMatchAcceptanceFloor}
	switch {
	case matched && winner != nil:
		decision.Outcome = MatchOutcomeMatched
	case hasTransientMatchError(providerErrors):
		decision.Outcome = MatchOutcomeProviderTransient
	case len(providerErrors) > 0 && len(candidates) == 0:
		decision.Outcome = MatchOutcomeProviderPermanent
	case len(candidates) == 0:
		decision.Outcome = MatchOutcomeNoCandidates
	case candidatesConflictWithTrustedIDs(hints, candidates):
		decision.Outcome = MatchOutcomeTrustedIDConflict
	default:
		decision.Outcome = MatchOutcomeCandidateRejected
	}

	scored := make([]MatchCandidate, len(candidates))
	copy(scored, candidates)
	for i := range scored {
		annotateCandidateMatch(&scored[i], hints)
	}
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].MatchScore > scored[j].MatchScore })
	if len(scored) > 3 {
		scored = scored[:3]
	}
	for _, candidate := range scored {
		reasons := append([]string(nil), candidate.MatchReasons...)
		if matched && winner != nil && sameMatchCandidate(candidate, *winner) {
			for _, reason := range winner.MatchReasons {
				if !slices.Contains(reasons, reason) {
					reasons = append(reasons, reason)
				}
			}
		}
		decision.TopCandidates = append(decision.TopCandidates, MatchDecisionCandidate{
			Title: candidate.Title, MatchedTitle: candidate.MatchedTitle, Year: candidate.Year,
			ProviderIDs: copyMap(candidate.ProviderIDs), Sources: append([]string(nil), candidate.Sources...),
			Score: candidate.MatchScore, Reasons: reasons,
		})
	}
	return decision
}

func sameMatchCandidate(left, right MatchCandidate) bool {
	leftKey, rightKey := normalizedKey(left.ProviderIDs), normalizedKey(right.ProviderIDs)
	if leftKey != "" || rightKey != "" {
		return leftKey != "" && leftKey == rightKey
	}
	if left.Title != right.Title || left.OriginalTitle != right.OriginalTitle ||
		left.Year != right.Year || left.ContentType != right.ContentType ||
		!slices.Equal(left.Sources, right.Sources) || len(left.ProviderIDs) != len(right.ProviderIDs) {
		return false
	}
	for key, value := range left.ProviderIDs {
		if right.ProviderIDs[key] != value {
			return false
		}
	}
	return true
}

func hasTransientMatchError(errs []error) bool {
	if localMetadataTemporarilyUnavailable(errs) {
		return true
	}
	for _, err := range errs {
		if err == nil || errors.Is(err, ErrLocalMetadataUnavailable) {
			continue // classified above, not by its message
		}
		switch status.Code(err) {
		case codes.DeadlineExceeded, codes.ResourceExhausted, codes.Aborted, codes.Unavailable:
			return true
		}
		message := strings.ToLower(err.Error())
		for _, marker := range []string{"timeout", "deadline exceeded", "connection reset", "connection refused", "temporarily unavailable", "unavailable", "rate limit", "too many requests", "http 429", "http 500", "http 502", "http 503", "http 504"} {
			if strings.Contains(message, marker) {
				return true
			}
		}
	}
	return false
}

// foldProviderResult merges one provider's GetMetadata result into the
// accumulator of a pipeline run, fill-empty, so the first provider in the chain
// wins each field. Identity-hint providers contribute IDs exclusively through
// the trusted-hint phase: their results merge metadata fields but never inject
// provider-id keys that conflict with or extend the established identity
// (kills chimeric ID sets). The identity-hint provider is the local one (NFO):
// the lists it supplies are not extended by later results (localListFields).
func foldProviderResult(accumulator, result *MetadataResult, language, providerSlug string, isIdentityHinter bool, quarantined map[string]struct{}) {
	result.ProviderIDs = sanitizeCandidateProviderIDs(result.ProviderIDs)
	if isIdentityHinter {
		result.ProviderIDs = nil
		accumulator.localLists.record(result)
	} else {
		result = accumulator.localLists.restrict(accumulator, result)
	}
	for key := range quarantined {
		delete(result.ProviderIDs, key)
	}
	mergePreferredTitleMetadata(accumulator, result, language, providerSlug, !isIdentityHinter)
	taglineWasEmpty := strings.TrimSpace(accumulator.Tagline) == ""
	if preferRequestedLanguageText(&accumulator.Overview, &accumulator.OverviewLanguage, result.Overview, result.OverviewLanguage, language) &&
		isForeignText(accumulator.Tagline, accumulator.taglineLanguage, language) {
		// A tagline in another language goes with the overview it came with;
		// one from another provider, such as the library's own files, stays.
		accumulator.Tagline, accumulator.taglineLanguage = result.Tagline, result.OverviewLanguage
	}
	mergeProviderIDs(accumulator, result)
	MergeMetadata(result, accumulator, nil, MergeFillEmpty)
	if taglineWasEmpty && strings.TrimSpace(accumulator.Tagline) != "" {
		accumulator.taglineLanguage = result.OverviewLanguage
	}
}

func mergePreferredTitleMetadata(accumulator, result *MetadataResult, language, provider string, attributeAliases bool) {
	if accumulator == nil || result == nil {
		return
	}
	if attributeAliases {
		if accumulator.titleAliasProviders == nil {
			accumulator.titleAliasProviders = make(map[string]bool)
		}
		providerComplete, providerSeen := accumulator.titleAliasProviders[provider]
		if !providerSeen {
			accumulator.titleAliasProviders[provider] = result.TitleAliasesComplete
		} else {
			// Multiple installations can share a provider slug. The aggregate is
			// authoritative only when every contributing response says its alias
			// snapshot is complete; one legacy/partial response removes deletion
			// authority for the shared provider scope.
			accumulator.titleAliasProviders[provider] = providerComplete && result.TitleAliasesComplete
		}
		if strings.TrimSpace(result.Title) != "" {
			kind := titleAliasKindLocalized
			if strings.TrimSpace(result.OriginalTitle) != "" && strings.EqualFold(strings.TrimSpace(result.Title), strings.TrimSpace(result.OriginalTitle)) &&
				(baseMetadataLanguage(result.OriginalLanguage) == "" || baseMetadataLanguage(result.TitleLanguage) == baseMetadataLanguage(result.OriginalLanguage)) {
				kind = titleAliasKindOriginal
			}
			accumulator.TitleAliases = appendUniqueTitleAlias(accumulator.TitleAliases, TitleAlias{
				Title: result.Title, Language: baseMetadataLanguage(result.TitleLanguage), Kind: kind, Provider: provider,
			})
		}
		for _, alias := range result.TitleAliases {
			if alias.Provider == "" {
				alias.Provider = provider
			}
			accumulator.TitleAliases = appendUniqueTitleAlias(accumulator.TitleAliases, alias)
		}
		if result.OriginalTitle != "" && !strings.EqualFold(result.OriginalTitle, result.Title) {
			accumulator.TitleAliases = appendUniqueTitleAlias(accumulator.TitleAliases, TitleAlias{
				Title: result.OriginalTitle, Language: baseMetadataLanguage(result.OriginalLanguage), Kind: titleAliasKindOriginal, Provider: provider,
			})
		}
	}
	if accumulator.OriginalTitle == "" && strings.TrimSpace(result.OriginalTitle) != "" {
		accumulator.OriginalTitle = result.OriginalTitle
	}

	title, titleLanguage, fallback, rank := preferredMetadataResultTitle(result, language)
	currentRank := metadataTitlePreferenceRank(accumulator.TitleLanguage, accumulator.TitleIsFallback, language)
	if strings.TrimSpace(accumulator.Title) != "" && currentRank == 0 {
		currentRank = 1
	}
	if strings.TrimSpace(accumulator.Title) == "" || rank > currentRank {
		accumulator.Title = title
		accumulator.TitleLanguage = titleLanguage
		accumulator.TitleIsFallback = fallback
	}
}

func (s *MetadataService) persistItemAliases(ctx context.Context, contentID, language string, result *MetadataResult) error {
	if s == nil || s.itemAliasRepo == nil || result == nil {
		return nil
	}
	byProvider := make(map[string][]models.MediaItemAlias)
	for _, alias := range result.TitleAliases {
		provider := strings.TrimSpace(alias.Provider)
		if provider == "" {
			continue
		}
		byProvider[provider] = append(byProvider[provider], models.MediaItemAlias{
			ContentID: contentID, Title: alias.Title, Language: alias.Language, Kind: alias.Kind, Provider: provider,
		})
	}
	for provider, complete := range result.titleAliasProviders {
		if err := s.itemAliasRepo.RefreshProviderLanguage(ctx, contentID, provider, language, byProvider[provider], complete); err != nil {
			return fmt.Errorf("persisting %s title aliases: %w", provider, err)
		}
	}
	return nil
}

func preferredMetadataResultTitle(result *MetadataResult, language string) (string, string, bool, int) {
	requested := baseMetadataLanguage(language)
	titleLanguage := baseMetadataLanguage(result.TitleLanguage)
	if strings.TrimSpace(result.Title) != "" && requested != "" && titleLanguage == requested && !result.TitleIsFallback {
		return result.Title, titleLanguage, false, 3
	}
	for _, alias := range result.TitleAliases {
		if strings.TrimSpace(alias.Title) != "" && requested != "" && baseMetadataLanguage(alias.Language) == requested {
			return alias.Title, requested, false, 2
		}
	}
	if strings.TrimSpace(result.Title) != "" && titleLanguage == "" && !result.TitleIsFallback {
		return result.Title, "", false, 3
	}
	if requested != "" && strings.TrimSpace(result.OriginalTitle) != "" {
		return result.OriginalTitle, baseMetadataLanguage(result.OriginalLanguage), true, 1
	}
	if strings.TrimSpace(result.Title) != "" {
		return result.Title, titleLanguage, result.TitleIsFallback, 1
	}
	return result.OriginalTitle, baseMetadataLanguage(result.OriginalLanguage), true, 1
}

func metadataTitlePreferenceRank(titleLanguage string, fallback bool, language string) int {
	if strings.TrimSpace(titleLanguage) == "" {
		// A provider that does not participate in the language contract (notably
		// the local NFO provider and older plugins) retains normal first-provider
		// priority. Only an explicitly marked fallback may be displaced by a
		// later requested-language result.
		if fallback {
			return 1
		}
		return 3
	}
	if baseMetadataLanguage(titleLanguage) == baseMetadataLanguage(language) && !fallback {
		return 3
	}
	return 1
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
	// Quarantined IDs are unsafe for deduplication, canonical ID derivation, and
	// persistence regardless of whether this is a new item or a refresh.
	for key := range accumulator.quarantinedProviderIDKeys {
		delete(accumulator.ProviderIDs, key)
	}

	// Determine merge mode.
	var mergeMode MergeMode
	switch req.Mode {
	case ModeInitialMatch, ModeScheduledRefresh:
		mergeMode = MergeFillEmpty
	case ModeManualRefresh, ModeIdentify:
		mergeMode = MergeReplaceUnlocked
	}
	if req.localReapply {
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
	// An enrichment or local re-apply write only merges into an item that
	// exists. It must not recreate one deleted since the pass selected it.
	if req.writesFieldsOnly() && existingItem == nil {
		return nil, fmt.Errorf("metadata target %q no longer exists", contentID)
	}
	// Nor may an Identify: a concurrent Identify can move the item to its
	// corrected id after this one loaded it, and writing on without the stored
	// item would drop its field locks.
	if req.Mode == ModeIdentify && !isNew && contentID != "" && existingItem == nil {
		return nil, fmt.Errorf("identify target %q: %w", contentID, catalog.ErrItemNotFound)
	}
	// Identity repairs (rebinding, local-ID promotion) belong to matching and
	// refreshes, never to an enrichment write.
	if !req.writesFieldsOnly() && !isNew && contentID != "" && existingItem != nil && (isProvisionalOwnershipStatus(existingItem.Status) || len(durableIDs) == 0) {
		reboundTo, err := s.rebindItemByProviderIDsLocked(ctx, contentID, accumulator.ProviderIDs, contentType, len(durableIDs) == 0, req.Mode == ModeIdentify)
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
	if !req.writesFieldsOnly() && !isNew && contentid.IsLocal(contentID) {
		canonical, err := s.canonicalizeLocalContentID(
			ctx, contentID, providerIDsStruct(accumulator.ProviderIDs), contentType, req.Mode == ModeIdentify)
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

	// Re-anchor an already provider-anchored item whose corrected identity now
	// derives a different anchor — the recovery path when an admin replaces a
	// wrong match with Identify or fixes a wrong <uniqueid> in an NFO. See
	// shouldReanchorProviderContentID for which modes may move the id. Reuses
	// the local-promotion machinery under the provider-dedup lock; a no-op when
	// the derived anchor is unchanged.
	anchorRejected := identityCorrectionRejectsLiveAnchor(contentID, accumulator.ProviderIDs,
		accumulator.rejectedIdentityProviderIDs, req.recordedStaleProviderIDs, accumulator.sameRunStaleProviderIDs)
	if shouldReanchorProviderContentID(contentID, isNew, req.Mode, anchorRejected) {
		reanchored, err := s.reanchorContentID(
			ctx, contentID, providerIDsStruct(accumulator.ProviderIDs), contentType, req.Mode == ModeIdentify)
		if err != nil {
			return nil, fmt.Errorf("reanchor provider content id: %w", err)
		}
		if reanchored != contentID {
			contentID = reanchored
			existingItem, err = s.itemRepo.GetByID(ctx, contentID)
			if err != nil {
				return nil, fmt.Errorf("loading reanchored item: %w", err)
			}
			locked = intSliceToFields(existingItem.LockedFields)
			durableIDs, err = s.loadDurableProviderIDs(ctx, contentID)
			if err != nil {
				return nil, err
			}
		}
	}

	suppressProviderIDValues(durableIDs, accumulator.recordedStaleProviderIDs)
	suppressProviderIDValues(durableIDs, accumulator.sameRunStaleProviderIDs)
	suppressProviderIDValues(durableIDs, accumulator.rejectedIdentityProviderIDs)
	if len(durableIDs) > 0 {
		if accumulator.ProviderIDs == nil {
			accumulator.ProviderIDs = make(map[string]string, len(durableIDs))
		}
		for key, value := range durableIDs {
			if _, quarantined := accumulator.quarantinedProviderIDKeys[key]; quarantined {
				continue
			}
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
	canonicalLanguage, isCanonicalWrite := canonicalMetadataLanguage(req, existingItem, locked)
	// Read once, so artwork and text in this write follow the same setting.
	fallbackLanguage := s.configuredFallbackLanguage()
	switch {
	case isCanonicalWrite:
		// Fallback text belongs to the provider IDs it was fetched with, so it
		// applies after persistence moved the item to another content ID too.
		keepStoredTitle := req.Mode != ModeInitialMatch || existingItem == nil || !isSkeletonLikeStatus(existingItem.Status)
		req.fallback.applyToItem(accumulator, req.Language, existingItem, keepStoredTitle)
		req.fallback.applyToChildren(seasons, episodes, req.Language, contentID)
	case fallbackLanguage != "":
		// Text a provider marked as another language is no translation: the
		// localization row stays empty and falls back to the base row. With
		// the fallback off the original language is wanted, so it stays.
		if isForeignText(accumulator.Title, accumulator.TitleLanguage, req.Language) {
			// localizedItemText leaves a fallback title out of the row.
			accumulator.TitleIsFallback = true
		}
		dropForeignText(&accumulator.Overview, &accumulator.OverviewLanguage, req.Language)
		dropForeignText(&accumulator.Tagline, &accumulator.taglineLanguage, req.Language)
		dropForeignChildText(seasons, episodes, req.Language)
	}
	// Whether the providers listed any people, before the merge below folds
	// the stored item into the accumulator.
	providersListPeople := len(accumulator.People) > 0
	// The merge below replaces accumulator with the stored item, whose text is
	// in the canonical language; a non-canonical write keeps the text it
	// fetched in req.Language for the localization row.
	var fetchedText *MetadataResult
	if !isCanonicalWrite {
		fetchedText = &MetadataResult{
			Title:           accumulator.Title,
			SortTitle:       accumulator.SortTitle,
			Overview:        accumulator.Overview,
			Tagline:         accumulator.Tagline,
			TitleIsFallback: accumulator.TitleIsFallback,
		}
	}

	if existingItem != nil {
		existingResult := itemToMetadataResult(existingItem)
		suppressProviderIDValues(existingResult.ProviderIDs, accumulator.recordedStaleProviderIDs)
		suppressProviderIDValues(existingResult.ProviderIDs, accumulator.sameRunStaleProviderIDs)
		suppressProviderIDValues(existingResult.ProviderIDs, accumulator.rejectedIdentityProviderIDs)
		for key := range accumulator.replacedProviderIDKeys {
			delete(existingResult.ProviderIDs, key)
		}
		// A skeleton's title and year came from the filename, so the match
		// replaces them, unless an admin locked them.
		if req.Mode == ModeInitialMatch && isSkeletonLikeStatus(existingItem.Status) {
			if !isFieldLocked(locked, FieldName) {
				existingResult.Title = ""
				existingResult.SortTitle = ""
			}
			if !isFieldLocked(locked, FieldReleaseDates) {
				existingResult.Year = 0
			}
		}
		if mergeMode == MergeFillEmpty {
			accumulator.localLists.keepStoredLists(accumulator, existingResult)
			s.keepStoredLocalCredits(ctx, contentID, accumulator)
		}
		mergeLocked := locked
		if !req.enrichmentOnly {
			mergeLocked = s.peopleLocksForMerge(ctx, contentID, locked, existingResult, accumulator)
		}
		if isCanonicalWrite {
			MergeMetadata(accumulator, existingResult, mergeLocked, mergeMode)
		} else {
			MergeGlobalMetadata(accumulator, existingResult, mergeLocked, mergeMode)
		}
		for key := range accumulator.quarantinedProviderIDKeys {
			delete(existingResult.ProviderIDs, key)
		}
		existingResult.quarantinedProviderIDKeys = accumulator.quarantinedProviderIDKeys
		existingResult.replacedProviderIDKeys = accumulator.replacedProviderIDKeys
		existingResult.recordedStaleProviderIDs = accumulator.recordedStaleProviderIDs
		existingResult.sameRunStaleProviderIDs = accumulator.sameRunStaleProviderIDs
		existingResult.rejectedIdentityProviderIDs = accumulator.rejectedIdentityProviderIDs
		accumulator = existingResult
	}

	ApplyDefaultSortTitle(accumulator, isFieldLocked(locked, FieldName))

	// Build the item for persistence.
	now := time.Now()
	item := metadataResultToItem(accumulator, contentType)
	carryItemOnlyArtwork(item, existingItem)
	item.DefaultMetadataLanguage = canonicalLanguage
	item.MatchedAt = &now
	item.LastRefreshed = &now
	item.RefreshFailures = 0
	item.Status = "matched"
	if req.writesFieldsOnly() {
		// An enrichment write is not a refresh: the refresh bookkeeping stays as
		// the last match or refresh left it. So does what no metadata result
		// carries: the series episode state, which only a refresh that
		// rewrites episodes recomputes, and the stored metadata object.
		item.MatchedAt = existingItem.MatchedAt
		item.LastRefreshed = existingItem.LastRefreshed
		item.RefreshFailures = existingItem.RefreshFailures
		item.Status = existingItem.Status
		item.EpisodeMetadataIncomplete = existingItem.EpisodeMetadataIncomplete
		item.EpisodeMetadataLastCheckedAt = existingItem.EpisodeMetadataLastCheckedAt
		item.MetadataS3Path = existingItem.MetadataS3Path
		item.MetadataEtag = existingItem.MetadataEtag
	}
	// The stored locks decide whether artwork below, and the season and
	// episode artwork of a series, may be replaced.
	if existingItem != nil {
		item.LockedFields = existingItem.LockedFields
	}
	// Stored artwork copies stay unless caching is off and the artwork is
	// unlocked; clients then load provider artwork straight from providers.
	keepStored := s.autoCacheImages.Load() || artworkLocked(item)

	// An enrichment write carries no artwork, and the image handling below
	// would read the missing images as "no artwork": it would drop artwork
	// still waiting to be cached. The item keeps exactly what is stored.
	handleArtwork := isCanonicalWrite && !req.enrichmentOnly
	if req.enrichmentOnly {
		keepStoredArtwork(item, existingItem)
	}

	// Apply best images. One retention serves the item level, so a library
	// folder that does not answer is probed once per refresh.
	var localArt *localArtworkRetention
	if handleArtwork {
		localArt = newLocalArtworkRetention(ctx, contentID, localArtworkItem)
		imageMode := mergeMode
		if req.localReapply {
			// Local images always apply; fill-empty keeps the rest of the
			// stored artwork, which no remote provider was asked about.
			imageMode = MergeFillEmpty
		}
		var stored storedItemArtwork
		if !keepStored {
			// With caching off a stored copy gives way to any provider image,
			// rated or not; fill-empty would keep it over an unrated one.
			stored = clearStoredItemArtwork(item)
		}
		applyBestImages(item, images, imageMode, req.Language, fallbackLanguage)
		stored.restoreUnfilled(item)
		if s.localArtworkUsable() || artworkLocked(item) {
			// With local art unusable a pending local source is dropped too,
			// so the slot's source is the provider image it serves.
			keepUnconfirmedLocalItemArtwork(localArt, item, existingItem)
		}
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

	if handleArtwork {
		prepareItemImagesForQueue(item, existingItem, keepStored)
		dropGoneItemOnlyArtwork(localArt, item, images)
		applyLocalArtworkFallback(item, existingItem, images, req.Language, fallbackLanguage)
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
		req.Mode == ModeIdentify,
	)
	if err != nil {
		return nil, err
	}
	contentID = persistedContentID
	item.ContentID = contentID
	unlockProviderDedup()
	providerDedupReleased = true
	if handleArtwork {
		// The failure is logged; a local re-apply also fails on it, so its
		// fingerprint does not advance past artwork that was never queued.
		if err := s.enqueueItemImages(ctx, item, accumulator.ProviderIDs, images); err != nil && req.localReapply {
			return nil, fmt.Errorf("queueing artwork: %w", err)
		}
		if err := s.syncItemExtraBackdrops(ctx, localArt, item, accumulator.ProviderIDs, images); err != nil && req.localReapply {
			return nil, fmt.Errorf("storing backdrops: %w", err)
		}
	}

	if !isCanonicalWrite && s.itemLocalizationRepo != nil {
		existingLoc, err := s.itemLocalizationRepo.Get(ctx, contentID, req.Language)
		if err != nil {
			return nil, fmt.Errorf("loading item localization: %w", err)
		}
		// A pass that could not read the NFO keeps what the NFO set, even
		// where it matches the canonical text.
		copiesOf := accumulator
		if localMetadataUnreadableLevels(ctx)&localArtworkItem != 0 {
			copiesOf = nil
		}
		loc := buildItemLocalizationRecord(
			existingLoc, contentID, req.Language, contentType, localizedItemText(fetchedText, accumulator, locked), copiesOf,
			images, mergeMode, req.Language, fallbackLanguage,
			isFieldLocked(locked, FieldName),
			keepStored,
		)
		if err := s.itemLocalizationRepo.Upsert(ctx, loc); err != nil {
			return nil, fmt.Errorf("upserting item localization: %w", err)
		}
		s.enqueueItemLocalizationImages(ctx, item, loc, accumulator.ProviderIDs, images)
	}

	// Persist people to the unified people table. The set is replaced
	// wholesale from providers' people, and the stored cast is not merged in,
	// so an enrichment write, which hears from one provider, leaves it alone.
	// A local re-apply whose files list no people (artwork only, or an NFO
	// without credits) leaves the stored cast as it is.
	if !req.enrichmentOnly && (!req.localReapply || providersListPeople) && len(item.People) > 0 && s.personRepo != nil {
		persons := make([]models.Person, len(item.People))
		for i := range item.People {
			persons[i] = item.People[i].Person
		}
		personIDs, err := s.personRepo.BatchFindOrCreate(ctx, persons)
		if err != nil {
			if req.localReapply {
				// A failed local re-apply is retried by the next check.
				return nil, fmt.Errorf("finding people: %w", err)
			}
			// Keep the stored cast: no credit got an ID, so replacing it now
			// would store only the credits that already had one, often none.
			slog.WarnContext(ctx, "metadata: batch find/create people failed; keeping stored people", "component", "metadata", "content_id", contentID, "error", err)
		} else {
			for i := range item.People {
				item.People[i].Person.ID = personIDs[i]
			}
			// Filter out entries where FindOrCreate failed (Person.ID stayed 0).
			valid := item.People[:0]
			for _, p := range item.People {
				if p.ID != 0 {
					valid = append(valid, p)
				}
			}
			if req.localReapply && len(valid) != len(item.People) {
				// A partial cast would replace the stored one.
				return nil, fmt.Errorf("resolving people: %d of %d unresolved", len(item.People)-len(valid), len(item.People))
			}
			if err := s.itemRepo.ReplacePeople(ctx, contentID, valid); err != nil {
				if req.localReapply {
					return nil, fmt.Errorf("replacing people: %w", err)
				}
				slog.WarnContext(ctx, "metadata: failed to replace people", "component", "metadata", "content_id", contentID, "error", err)
			}
			s.applyLocalPersonPhotos(ctx, contentID, valid)
		}
	}

	// Persist remote videos (trailers etc.), filtered by the library's
	// trailer_kinds allow-list. Replace-unlocked refreshes replace the whole
	// set — including clearing it — so narrowing the allow-list converges on
	// the next refresh; fill-empty refreshes only write when providers
	// returned something, so a transient provider failure cannot wipe data.
	// An enrichment write skips them for the same reason as people.
	if !req.writesFieldsOnly() && isCanonicalWrite && s.videoRepo != nil && !isFieldLocked(locked, FieldVideos) {
		allowed := s.resolveAllowedVideoKinds(ctx, contentID, parseProcessFolderID(req.FolderID))
		filtered := filterVideosByKinds(accumulator.Videos, allowed)
		if len(filtered) > 0 || mergeMode == MergeReplaceUnlocked {
			if err := s.videoRepo.ReplaceByContentID(ctx, contentID, itemVideosFromRemote(contentID, filtered)); err != nil {
				slog.WarnContext(ctx, "metadata: failed to replace item videos", "component", "metadata", "content_id", contentID, "error", err)
				// A failed write is invisible in ProcessResult by design (the
				// rest of the refresh still succeeded), so tell any observer
				// that asked — today, the viewer trailer action, which must
				// not charge a cooldown for trailers it did not store.
				reportVideoPersistFailure(ctx, err)
			}
		}
	}

	// Persist per-source ratings. The item row does not carry them, so the
	// merge above saw no stored sources and passed every reported one through
	// (or none, under a FieldRating lock); the chain's provider order already
	// picked one entry per source. The stored rows are merged by the write
	// instead. A manual or scheduled refresh overwrites the sources this
	// refresh reported: scores and vote counts keep moving after release, and
	// discovery rows rank by the TMDB count. The enrichment-only bulk pass
	// carries one provider's answer, so it only fills sources the item lacks
	// and never overwrites the chain's choice. Identify replaces the whole set,
	// even with an empty one, because it keeps the content_id while changing
	// the title: a source only the previous match reported would otherwise
	// stay on the item for good. Like the rating columns, they are
	// provider-invariant and written for every language.
	if s.ratingSourceRepo != nil && !isFieldLocked(locked, FieldRating) {
		sources := itemRatingSourcesFromResult(contentID, accumulator.RatingSources)
		replace := mergeMode == MergeReplaceUnlocked || (req.Mode == ModeScheduledRefresh && !req.enrichmentOnly)
		var err error
		switch {
		case req.Mode == ModeIdentify:
			err = s.ratingSourceRepo.Replace(ctx, contentID, sources)
		case len(sources) > 0:
			err = s.ratingSourceRepo.Upsert(ctx, contentID, sources, replace)
		}
		if err != nil {
			slog.WarnContext(ctx, "metadata: failed to store rating sources", "component", "metadata", "content_id", contentID, "error", err)
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

	// Persist seasons and episodes for series. Episode-only results (e.g.
	// local episode NFOs without a season.nfo) persist too — the persist
	// path creates their implicit "Season N" rows. Fallback synthesis then
	// covers any files the providers left unlinked (episodes with no NFO
	// and no remote row); it is a no-op when every file is linked. An
	// enrichment write carries no seasons or episodes and leaves them alone.
	if contentType == "series" && !req.writesFieldsOnly() {
		if len(seasons) > 0 || len(episodes) > 0 {
			s.persistSeasonsAndEpisodes(ctx, item, accumulator.ProviderIDs, canonicalLanguage, req.Language, seasons, episodes, mergeMode)
		}
		if err := s.SynthesizeFallbackEpisodes(ctx, contentID); err != nil {
			slog.WarnContext(ctx, "metadata: failed to synthesize fallback series structure", "component", "metadata",
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
	identityChosen bool,
) (string, error) {
	if item == nil || strings.TrimSpace(item.ContentID) == "" {
		return "", fmt.Errorf("content_id is required")
	}
	if itemRepo, ok := s.itemRepo.(*catalog.ItemRepository); ok && s.dbPool != nil {
		if providerRepo, ok := s.providerIDRepo.(*catalog.ProviderIDRepository); ok && providerRepo != nil {
			return s.persistItemAndProviderIDsTx(ctx, itemRepo, providerRepo, item, providerIDs, isNew, existingItem, missingDurableIDs, contentType, identityChosen)
		}
	}
	contentID, err := s.persistItemAndProviderIDsLegacy(ctx, item, providerIDs, isNew, existingItem, missingDurableIDs, contentType)
	if err == nil && identityChosen {
		err = s.recordIdentityChoice(ctx, contentID)
	}
	return contentID, err
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
	identityChosen bool,
) (string, error) {
	contentID := item.ContentID
	err := s.persistItemAndProviderIDsOnceTx(ctx, itemRepo, providerRepo, item, providerIDs, identityChosen)
	if err == nil {
		return contentID, nil
	}
	if !isProviderIDUniqueConflict(err) {
		return "", fmt.Errorf("persisting provider IDs for %s: %w", contentID, err)
	}

	reboundTo, dropped, recoverErr := s.recoverProviderIDConflict(ctx, contentID, providerIDs, contentType, true, identityChosen)
	if recoverErr != nil {
		return "", fmt.Errorf("recovering provider ID conflict for %s: %w", contentID, recoverErr)
	}
	if reboundTo == "" {
		return "", fmt.Errorf("persisting provider IDs for %s: %w", contentID, err)
	}
	item.ContentID = reboundTo
	clearDroppedIdentityColumns(item, dropped)
	if retryErr := s.persistItemAndProviderIDsOnceTx(ctx, itemRepo, providerRepo, item, providerIDs, identityChosen); retryErr != nil {
		return "", fmt.Errorf("persisting provider IDs after conflict recovery for %s: %w", reboundTo, retryErr)
	}
	return reboundTo, nil
}

// clearDroppedIdentityColumns blanks the item's TMDB, TVDB and IMDb columns
// that still hold an ID conflict recovery removed from its provider IDs.
func clearDroppedIdentityColumns(item *models.MediaItem, dropped map[string]string) {
	for key, value := range dropped {
		var column *string
		switch key {
		case "tmdb":
			column = &item.TmdbID
		case "tvdb":
			column = &item.TvdbID
		case "imdb":
			column = &item.ImdbID
		default:
			continue
		}
		if *column == value {
			*column = ""
		}
	}
}

func (s *MetadataService) persistItemAndProviderIDsOnceTx(
	ctx context.Context,
	itemRepo *catalog.ItemRepository,
	providerRepo *catalog.ProviderIDRepository,
	item *models.MediaItem,
	providerIDs map[string]string,
	identityChosen bool,
) error {
	tx, err := s.dbPool.BeginTx(ctx, catalog.MetadataCurationTxOptions(ctx))
	if err != nil {
		return fmt.Errorf("begin metadata item/provider transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := catalog.GuardMetadataCurationItems(ctx, tx, item.ContentID); err != nil {
		return err
	}

	if err := itemRepo.UpsertTx(ctx, tx, item); err != nil {
		return fmt.Errorf("upserting item: %w", err)
	}
	if providerRepo != nil {
		if err := providerRepo.ReplaceByContentIDTx(ctx, tx, item.ContentID, item.Type, providerIDs); err != nil {
			return err
		}
	}
	// An Identify's choice commits with the identity it chose
	// (identify_sticky.go).
	if identityChosen {
		if err := catalog.MarkIdentityChosenTx(ctx, tx, item.ContentID); err != nil {
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
			reboundTo, dropped, reboundErr := s.recoverProviderIDConflict(ctx, contentID, providerIDs, contentType, true, false)
			if reboundErr != nil {
				return "", fmt.Errorf("recovering provider ID conflict for %s: %w", contentID, reboundErr)
			}
			if reboundTo == "" {
				return "", fmt.Errorf("persisting provider IDs for %s: %w", contentID, err)
			}
			item.ContentID = reboundTo
			clearDroppedIdentityColumns(item, dropped)
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
	if result != nil && result.Pinned {
		// An admin left the item unmatched on purpose: there is nothing to
		// refresh, and that is not a failure. Settle its debt row as a
		// successful refresh would, or a claimed row keeps coming back.
		if syncErr := s.syncRefreshDebtForItem(ctx, contentID); syncErr != nil {
			slog.WarnContext(ctx, "metadata: failed to sync refresh debt after a pinned item refresh", "component", "metadata",
				"content_id", contentID,
				"error", syncErr)
		}
		return nil
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
// episode target using the background refresh merge policy. The caller must
// have claimed the target's debt row: the claim counted this attempt, so a
// target that reaches its terminal attempts here reports it.
//
// A queued item may be the durable recovery for a viewer's trailer request
// whose process died mid-refresh (see RequestTrailersRefresh). That request
// consumed a week-long cooldown slot and took its release hook down with the
// process, so this path adopts both: it carries the same failure semantics, and
// a recovery that fails hands the slot back instead of leaving the viewer
// blocked for a week over trailers nobody ever stored.
func (s *MetadataService) RefreshScheduledTarget(ctx context.Context, targetType, contentID string) error {
	ctx = withCountedRefreshAttempt(ctx, targetType, contentID)
	if NormalizeRefreshTargetType(targetType) == RefreshTargetItem {
		if claim := s.adoptTrailersRefreshClaim(ctx, contentID); claim != nil {
			return claim.run(ctx)
		}
	}
	return s.refreshTarget(ctx, targetType, contentID, 0, ModeScheduledRefresh, false)
}

// trailersRefreshRecovery is an inherited trailer-refresh cooldown claim, held
// across the scheduled refresh that is recovering the request which consumed
// it.
type trailersRefreshRecovery struct {
	service   *MetadataService
	gate      metadataTrailerRefreshRepo
	contentID string
	claimedAt time.Time
}

// adoptTrailersRefreshClaim reports the cooldown claim a queued item's refresh
// is responsible for, or nil when the refresh owes nobody a release.
//
// The debt row's trailers-requested reason bit is what makes the claim
// identifiable: RequestTrailersRefresh sets it exactly when it consumes a slot,
// and the first refresh that resolves the row clears it. Reading the stored
// timestamp gives the same key the original request held, so the release stays
// equality-guarded — a slot re-claimed by a newer request in the meantime is
// that request's to release, not this one's.
func (s *MetadataService) adoptTrailersRefreshClaim(ctx context.Context, contentID string) *trailersRefreshRecovery {
	if s == nil || strings.TrimSpace(contentID) == "" {
		return nil
	}
	gate, ok := s.itemRepo.(metadataTrailerRefreshRepo)
	if !ok || gate == nil {
		return nil
	}
	reasonMask, err := s.currentRefreshDebtTargetReasonMask(ctx, RefreshTargetItem, contentID)
	if err != nil || !hasRefreshDebtReason(reasonMask, RefreshDebtReasonTrailersRequested) {
		return nil
	}
	claimedAt, err := gate.TrailersRefreshRequestedAt(ctx, contentID)
	if err != nil {
		slog.WarnContext(ctx, "metadata: failed to read the trailers refresh claim a queued refresh inherits",
			"component", "metadata", "content_id", contentID, "error", err)
		return nil
	}
	if claimedAt == nil {
		// The slot was already handed back (or the window lapsed), so this
		// refresh owes nothing.
		return nil
	}
	return &trailersRefreshRecovery{service: s, gate: gate, contentID: contentID, claimedAt: *claimedAt}
}

// run performs the recovery refresh under the inherited claim, releasing the
// slot on the same failures the original request's hook covered — including a
// videos write that failed and was only logged, which leaves the refresh
// "successful" while storing none of the trailers the cooldown was charged for.
func (r *trailersRefreshRecovery) run(ctx context.Context) error {
	var videoPersistErr atomic.Pointer[error]
	refreshCtx := withVideoPersistFailureObserver(ctx, func(persistErr error) {
		videoPersistErr.CompareAndSwap(nil, &persistErr)
	})

	err := r.service.refreshTarget(refreshCtx, RefreshTargetItem, r.contentID, 0, ModeScheduledRefresh, false)
	releaseErr := err
	if releaseErr == nil {
		if stored := videoPersistErr.Load(); stored != nil {
			releaseErr = fmt.Errorf("persisting item videos: %w", *stored)
		}
	}
	if releaseErr != nil {
		r.service.releaseTrailersRefreshClaim(r.gate, r.contentID, r.claimedAt, releaseErr)
	}
	return err
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
		seriesID, err := s.refreshSeasonTarget(ctx, contentID, folderID, mode)
		if err != nil {
			s.recordRefreshTargetFailure(ctx, targetType, contentID, err, incrementDebtAttempt)
			return err
		}
		return s.syncRefreshDebtForTargetOrDefer(ctx, targetType, contentID, seriesID)
	case RefreshTargetEpisode:
		seriesID, err := s.refreshEpisodeTarget(ctx, contentID, folderID, mode)
		if err != nil {
			s.recordRefreshTargetFailure(ctx, targetType, contentID, err, incrementDebtAttempt)
			return err
		}
		return s.syncRefreshDebtForTargetOrDefer(ctx, targetType, contentID, seriesID)
	default:
		return fmt.Errorf("unsupported metadata refresh target type %q", targetType)
	}
}

// RequestStaleMetadataRefresh nudges a stale target into the durable refresh
// queue and starts a detached refresh when the target is due. Provider work is
// never done inline on the caller's request path.
//
// Detail views call this for incomplete items. A target whose only debt is
// episode-incomplete debt parked after its terminal attempts stays parked: a
// view adds no evidence that another provider fetch would help. A view no
// longer lifts it to the top priority band or refreshes it; its terminal
// re-check, an operator refresh, a rescan, or a trailer request still does.
func (s *MetadataService) RequestStaleMetadataRefresh(ctx context.Context, targetType, contentID string) error {
	if s == nil || s.refreshDebtRepo == nil {
		return nil
	}
	targetType = NormalizeRefreshTargetType(targetType)
	contentID = strings.TrimSpace(contentID)
	if targetType == "" || contentID == "" {
		return nil
	}
	debt, err := s.refreshDebtRepo.GetTarget(ctx, targetType, contentID)
	if err != nil && !errors.Is(err, ErrRefreshDebtNotFound) {
		return err
	}
	if debt != nil && isParkedEpisodeDebt(debt.ReasonMask, debt.AttemptCount) {
		return nil
	}
	return s.queueStaleMetadataRefresh(ctx, targetType, contentID)
}

// queueStaleMetadataRefresh is RequestStaleMetadataRefresh without the terminal
// check, for callers whose own work needs the next refresh whatever the
// target's episode debt is.
func (s *MetadataService) queueStaleMetadataRefresh(ctx context.Context, targetType, contentID string) error {
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

// Trailer refresh outcome statuses returned by RequestTrailersRefresh.
const (
	// TrailerRefreshStatusQueued means the request won the cooldown gate and a
	// detached refresh was started.
	TrailerRefreshStatusQueued = "queued"
	// TrailerRefreshStatusCooldown means the item was refreshed within the
	// cooldown window; NextAllowedAt says when the next request may win.
	TrailerRefreshStatusCooldown = "cooldown"
	// TrailerRefreshStatusDisabled means every library containing the item has
	// remote videos turned off, so a refresh could not produce trailers.
	TrailerRefreshStatusDisabled = "disabled"
)

// TrailerRefreshCooldown is the per-item window between viewer-triggered
// trailer refreshes. A full single-item refresh is not cheap, and provider
// video sets change slowly, so the window is deliberately long.
const TrailerRefreshCooldown = 7 * 24 * time.Hour

// TrailerRefreshOutcome reports what a viewer's "find trailers" request did.
// NextAllowedAt is set only for the cooldown status.
type TrailerRefreshOutcome struct {
	Status        string
	NextAllowedAt *time.Time
}

// trailerRefreshReleaseTimeout bounds the write that hands a cooldown slot back
// after a failed refresh. It runs on its own context because the refresh's
// context is frequently already expired — a timeout is one of the failures the
// release exists for.
const trailerRefreshReleaseTimeout = 15 * time.Second

// trailerRefreshClaimTimeout bounds the durable claim. The claim runs on a
// context detached from the request (see RequestTrailersRefresh) and so needs
// a deadline of its own; it is a single indexed UPDATE, so this is generous.
const trailerRefreshClaimTimeout = 15 * time.Second

// trailerRefreshRecoveryDelay holds the durable recovery row back until after
// the detached fast path can possibly still be running.
//
// The debt row exists only to survive a process that dies mid-refresh. Due
// immediately, it is claimable by the refresh_metadata task the moment it is
// written, and that task calls RefreshScheduledTarget without consulting the
// in-process claim — so the worker and the goroutine would run the same full
// provider refresh at once, burning provider quota and racing each other's
// writes. Delaying past metadataOnDemandRefreshTimeout means the row can only
// come due once the goroutine is guaranteed finished (or gone with its
// process); on the normal path the refresh's own debt sync resolves the row
// long before then.
const trailerRefreshRecoveryDelay = 5 * time.Minute

// videoPersistFailureContextKey scopes a videos-persistence observer to one
// refresh. mergeAndPersist logs and continues when videoRepo.ReplaceByContentID
// fails, because a video write failure must not fail a whole metadata refresh
// that otherwise succeeded — but the viewer-triggered trailer action needs to
// know, since "refresh succeeded" is then not the same as "trailers were
// saved", and it would otherwise consume a week-long cooldown for nothing.
type videoPersistFailureContextKey struct{}

// withVideoPersistFailureObserver returns a context that reports a failed
// item_videos write to the supplied callback. Refreshes that do not install
// one — every background and admin path — are unaffected.
func withVideoPersistFailureObserver(ctx context.Context, observe func(error)) context.Context {
	if observe == nil {
		return ctx
	}
	return context.WithValue(ctx, videoPersistFailureContextKey{}, observe)
}

// reportVideoPersistFailure notifies an installed observer, if any.
func reportVideoPersistFailure(ctx context.Context, err error) {
	observe, _ := ctx.Value(videoPersistFailureContextKey{}).(func(error))
	if observe != nil {
		observe(err)
	}
}

// RequestTrailersRefresh is the viewer-facing trailer fetch: it starts a full
// single-item metadata refresh at most once per TrailerRefreshCooldown.
//
// The refresh runs in scheduled mode (MergeFillEmpty), so this non-admin
// trigger cannot overwrite unlocked admin edits, while found videos still
// persist — mergeAndPersist writes item_videos whenever providers returned
// any, and skips the write when they returned none, so a transient empty
// result cannot wipe stored trailers.
//
// Ordering matters, and each step is a way to answer without burning the
// item's weekly slot on work that will not happen:
//   - the disabled check runs first, so an item whose libraries have remote
//     videos turned off never consumes a slot;
//   - the in-process dedup claim runs next, so a request that lands while an
//     equivalent refresh is already in flight reports "queued" (truthfully —
//     one is running) and leaves the slot for a real retry;
//   - only then is the durable slot consumed, and it is handed back if the
//     refresh it started fails.
//
// A refresh that succeeds but finds no videos keeps the slot: that is the
// accepted "nothing to find, come back next week" outcome.
func (s *MetadataService) RequestTrailersRefresh(ctx context.Context, contentID string) (TrailerRefreshOutcome, error) {
	if s == nil {
		return TrailerRefreshOutcome{}, ErrMetadataNotFound
	}
	contentID = strings.TrimSpace(contentID)
	if contentID == "" {
		return TrailerRefreshOutcome{}, catalog.ErrItemNotFound
	}

	// An admin lock on the videos field makes mergeAndPersist skip the
	// item_videos write entirely (its isFieldLocked(locked, FieldVideos)
	// guard), so a refresh started here would report success and consume the
	// week having saved nothing. From the viewer's side that is the same
	// answer as a library with
	// remote videos turned off — trailers cannot be fetched for this item — so
	// it reuses "disabled" rather than inventing a status clients do not know:
	// the Apple coordinator treats an unrecognized status as "stop, nothing
	// found", which would be a worse answer than the one disabled already
	// gives.
	if s.trailerVideosLocked(ctx, contentID) {
		return TrailerRefreshOutcome{Status: TrailerRefreshStatusDisabled}, nil
	}

	// A non-nil empty allow-list means every containing library disabled
	// remote videos. A nil map means allow-all (unknown scope or a transient
	// lookup failure) and must not short-circuit.
	if allowed := s.resolveAllowedVideoKinds(ctx, contentID, 0); allowed != nil && len(allowed) == 0 {
		return TrailerRefreshOutcome{Status: TrailerRefreshStatusDisabled}, nil
	}

	gate, ok := s.itemRepo.(metadataTrailerRefreshRepo)
	if !ok || gate == nil {
		return TrailerRefreshOutcome{}, ErrMetadataNotFound
	}

	// Losing the in-process claim means an equivalent full refresh for this
	// item is already running (this action or the detail view's stale nudge —
	// they share the key). Report it as queued and leave the slot alone: if
	// that refresh fails, the viewer can retry immediately.
	if !s.claimOnDemandMetadataRefresh(RefreshTargetItem, contentID) {
		return TrailerRefreshOutcome{Status: TrailerRefreshStatusQueued}, nil
	}
	// The claim is ours from here: either the detached refresh takes ownership
	// of it, or it is released before this call returns.
	startedRefresh := false
	defer func() {
		if !startedRefresh {
			s.releaseOnDemandMetadataRefresh(RefreshTargetItem, contentID)
		}
	}()

	// The claim is a durable side effect, so it must not ride the request's
	// context: a cancellation landing after Postgres commits the UPDATE but
	// before pgx returns would consume the slot for the whole window with no
	// refresh started and nothing left holding the information needed to
	// release it. Detaching from cancellation (with a deadline of its own)
	// keeps the claim and the goroutine that owns its release inseparable.
	claimCtx, cancelClaim := context.WithTimeout(context.WithoutCancel(ctx), trailerRefreshClaimTimeout)
	claimed, requestedAt, err := gate.TryClaimTrailersRefresh(claimCtx, contentID, TrailerRefreshCooldown)
	cancelClaim()
	if err != nil {
		return TrailerRefreshOutcome{}, err
	}
	if !claimed {
		// A nil timestamp on a lost claim means the repository saw the slot
		// freed underneath it twice over: another request is claiming it right
		// now, so the honest answer is the same one a lost in-process claim
		// gets rather than a cooldown nobody can date.
		if requestedAt == nil {
			return TrailerRefreshOutcome{Status: TrailerRefreshStatusQueued}, nil
		}
		next := requestedAt.Add(TrailerRefreshCooldown).UTC()
		return TrailerRefreshOutcome{
			Status:        TrailerRefreshStatusCooldown,
			NextAllowedAt: &next,
		}, nil
	}

	// Record the refresh in the durable debt queue as well. The goroutine below
	// is the fast path and normally finishes in seconds, but it does not
	// survive a restart; the debt row does, so a process that dies mid-refresh
	// leaves behind work the refresh worker will pick up instead of an item
	// that waits out the window having fetched nothing. The row is deliberately
	// not due yet (trailerRefreshRecoveryDelay) so the worker cannot run the
	// same refresh alongside the goroutine, and the goroutine clears it on
	// success, so it fires only when the fast path really did not finish. The
	// queue is idempotent (RequestDue merges into any existing row and never
	// pulls a leased or recently-attempted target forward), so this is additive.
	s.enqueueTrailersRefreshDebt(ctx, contentID)

	// Hand the slot back if the refresh this request started fails, including
	// on timeout: otherwise a provider outage would lock the item for the whole
	// cooldown window without ever having fetched anything.
	hooks := onDemandRefreshHooks{}
	if requestedAt != nil {
		claimedAt := *requestedAt
		// A refresh can report success while the item_videos write inside it
		// failed and was logged — from this action's point of view that is a
		// failure, because the cooldown is a budget for *fetching trailers*.
		var videoPersistErr atomic.Pointer[error]
		hooks.decorateContext = func(refreshCtx context.Context) context.Context {
			return withVideoPersistFailureObserver(refreshCtx, func(persistErr error) {
				videoPersistErr.CompareAndSwap(nil, &persistErr)
			})
		}
		hooks.onComplete = func(refreshErr error) {
			if refreshErr == nil {
				if stored := videoPersistErr.Load(); stored != nil {
					refreshErr = fmt.Errorf("persisting item videos: %w", *stored)
				}
			}
			if refreshErr == nil {
				// The fast path did the work, so the recovery row has nothing
				// left to recover. Clearing it keeps the worker from re-running
				// a refresh that already happened; the refresh's own debt sync
				// usually gets there first, and this is idempotent either way.
				s.settleTrailersRefreshDebt(contentID)
				return
			}
			s.releaseTrailersRefreshClaim(gate, contentID, claimedAt, refreshErr)
		}
	}
	s.runOnDemandMetadataRefresh(RefreshTargetItem, contentID, hooks)
	startedRefresh = true
	return TrailerRefreshOutcome{Status: TrailerRefreshStatusQueued}, nil
}

// enqueueTrailersRefreshDebt records the item in the durable refresh-debt queue
// so a restart that kills the detached goroutine does not leave the cooldown
// consumed with no refresh ever performed. Best effort by design: failing to
// write the safety net must not fail a request whose refresh is about to start.
func (s *MetadataService) enqueueTrailersRefreshDebt(ctx context.Context, contentID string) {
	if s == nil || s.refreshDebtRepo == nil {
		return
	}
	// RefreshDebtReasonTrailersRequested rather than the generic failure reason:
	// nothing is wrong with this item, so it must not land in the failure band
	// ahead of real debt, nor be counted as a failure in the operator metrics.
	// Nothing recomputes the bit, so the next successful refresh clears it.
	reasonMask := RefreshDebtReasonTrailersRequested
	// Not due until the fast path cannot still be running: RequestDue keeps the
	// earlier of the two timestamps when a row already exists, so genuinely due
	// debt for this item is never pushed out by the delay.
	dueAt := time.Now().UTC().Add(trailerRefreshRecoveryDelay)
	dueCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), trailerRefreshClaimTimeout)
	defer cancel()
	if err := s.refreshDebtRepo.RequestDue(
		dueCtx,
		RefreshTargetItem,
		contentID,
		refreshDebtPriority(reasonMask),
		reasonMask,
		dueAt,
		metadataRefreshNudgeCooldown,
	); err != nil {
		slog.WarnContext(dueCtx, "metadata: failed to record durable debt for a trailers refresh", "component", "metadata",
			"content_id", contentID, "error", err)
	}
}

// settleTrailersRefreshDebt resolves the recovery row after the fast path
// finished the work it was insurance for.
//
// It runs on its own context in the detached goroutine, after the refresh's own
// debt sync has normally already rewritten or deleted the row — so this is a
// no-op in the common case and matters only when that sync did not clear the
// trailers-requested bit. Clearing just that bit (rather than deleting the row)
// keeps any real debt the item still carries: another reason left in the mask
// means the item genuinely needs refreshing again, and the queue should keep
// saying so.
func (s *MetadataService) settleTrailersRefreshDebt(contentID string) {
	if s == nil || s.refreshDebtRepo == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), trailerRefreshClaimTimeout)
	defer cancel()

	debt, err := s.refreshDebtRepo.GetTarget(ctx, RefreshTargetItem, contentID)
	if err != nil {
		if !errors.Is(err, ErrRefreshDebtNotFound) {
			slog.WarnContext(ctx, "metadata: failed to read durable debt after a trailers refresh", "component", "metadata",
				"content_id", contentID, "error", err)
		}
		return
	}
	if debt == nil || !hasRefreshDebtReason(debt.ReasonMask, RefreshDebtReasonTrailersRequested) {
		return
	}
	remaining := debt.ReasonMask &^ RefreshDebtReasonTrailersRequested
	if remaining == 0 {
		if err := s.refreshDebtRepo.DeleteTargetDebt(ctx, RefreshTargetItem, contentID); err != nil {
			slog.WarnContext(ctx, "metadata: failed to clear durable debt after a trailers refresh", "component", "metadata",
				"content_id", contentID, "error", err)
		}
		return
	}
	if err := s.refreshDebtRepo.MarkTargetSuccess(
		ctx,
		RefreshTargetItem,
		contentID,
		effectiveRefreshDebtPriority(remaining, debt.AttemptCount),
		remaining,
		nextRefreshAtForDebt(remaining, debt.AttemptCount, time.Now().UTC()),
	); err != nil {
		slog.WarnContext(ctx, "metadata: failed to settle durable debt after a trailers refresh", "component", "metadata",
			"content_id", contentID, "error", err)
	}
}

// trailerVideosLocked reports that an admin has locked the item's videos field,
// which makes mergeAndPersist skip the item_videos write no matter what the
// providers return. A refresh started in that state would report success and
// charge the viewer a week for trailers it could never save.
//
// A lookup failure answers false: the preflight exists to avoid a pointless
// refresh, and refusing the action because the database blinked would be a
// worse failure than performing one.
func (s *MetadataService) trailerVideosLocked(ctx context.Context, contentID string) bool {
	if s == nil || s.itemRepo == nil {
		return false
	}
	item, err := s.itemRepo.GetByID(ctx, contentID)
	if err != nil || item == nil {
		return false
	}
	return isFieldLocked(intSliceToFields(item.LockedFields), FieldVideos)
}

// artworkLocked reports that the item holds manually selected artwork. An
// admin image selection locks FieldImages on the item, or on the parent series
// for a season poster or episode still, so a refresh keeps existing artwork
// under that lock and only fills empty slots.
func artworkLocked(item *models.MediaItem) bool {
	return item != nil && isFieldLocked(intSliceToFields(item.LockedFields), FieldImages)
}

// releaseTrailersRefreshClaim clears the cooldown slot this request consumed.
// The repository's equality guard means a slot already re-claimed by a newer
// request is left alone, so this is safe to run long after the fact.
func (s *MetadataService) releaseTrailersRefreshClaim(
	gate metadataTrailerRefreshRepo,
	contentID string,
	claimedAt time.Time,
	refreshErr error,
) {
	ctx, cancel := context.WithTimeout(context.Background(), trailerRefreshReleaseTimeout)
	defer cancel()
	if err := gate.ReleaseTrailersRefreshClaim(ctx, contentID, claimedAt); err != nil {
		slog.WarnContext(ctx, "metadata: failed to release trailers refresh cooldown slot", "component", "metadata",
			"content_id", contentID,
			"refresh_error", refreshErr,
			"error", err)
		return
	}
	slog.InfoContext(ctx, "metadata: released trailers refresh cooldown slot after a failed refresh",
		"component", "metadata",
		"content_id", contentID,
		"refresh_error", refreshErr)
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

// startOnDemandMetadataRefresh takes the in-process claim for the target and,
// if it wins, runs a detached refresh. Losing the claim means an equivalent
// refresh is already in flight and this call is a no-op.
func (s *MetadataService) startOnDemandMetadataRefresh(targetType, contentID string) {
	if !s.claimOnDemandMetadataRefresh(targetType, contentID) {
		return
	}
	s.runOnDemandMetadataRefresh(targetType, contentID, onDemandRefreshHooks{})
}

// onDemandRefreshHooks lets a caller that consumed durable state to start a
// detached refresh observe how that refresh went, so it can put the state back.
// The zero value is the plain fire-and-forget refresh every background caller
// wants.
type onDemandRefreshHooks struct {
	// decorateContext wraps the detached refresh's context before the refresh
	// runs — the way a caller installs an observer scoped to just this refresh
	// (see withVideoPersistFailureObserver).
	decorateContext func(context.Context) context.Context
	// onComplete runs in the detached goroutine once the refresh has finished,
	// with the refresh error or nil on success. "Success" here is only the
	// pipeline's own verdict: a caller that cares about a specific sub-result
	// has to observe that separately, because a refresh can succeed overall
	// while a single persistence step logged and continued.
	onComplete func(error)
}

// runOnDemandMetadataRefresh runs the detached refresh for a claim the caller
// already holds, and takes ownership of releasing it.
//
// hooks.onComplete runs *before* the in-process claim is released, which keeps
// a useful invariant for whoever picks the claim up next: by the time it is
// free, the durable state has already been put back. The alternative ordering
// leaves a window in which a concurrent request sees consumed state for a
// refresh that has already finished.
func (s *MetadataService) runOnDemandMetadataRefresh(targetType, contentID string, hooks onDemandRefreshHooks) {
	go func() {
		defer s.releaseOnDemandMetadataRefresh(targetType, contentID)
		ctx, cancel := context.WithTimeout(context.Background(), metadataOnDemandRefreshTimeout)
		defer cancel()
		if hooks.decorateContext != nil {
			ctx = hooks.decorateContext(ctx)
		}
		slog.Info("metadata: starting on-demand stale refresh",
			"target_type", targetType,
			"content_id", contentID)
		err := s.refreshTarget(ctx, targetType, contentID, 0, ModeScheduledRefresh, false)
		if err != nil {
			slog.Warn("metadata: on-demand stale refresh failed",
				"target_type", targetType,
				"content_id", contentID,
				"error", err)
		} else {
			slog.Info("metadata: completed on-demand stale refresh",
				"target_type", targetType,
				"content_id", contentID)
		}
		if hooks.onComplete != nil {
			hooks.onComplete(err)
		}
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
		slog.WarnContext(ctx, "metadata: failed to record refresh failure", "component", "metadata",
			"content_id", contentID,
			"refresh_error", refreshErr,
			"error", err)
		return
	}
	if err := s.syncRefreshDebtFailure(ctx, contentID, refreshErr, incrementDebtAttempt); err != nil {
		slog.WarnContext(ctx, "metadata: failed to sync refresh debt after refresh failure", "component", "metadata",
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
		slog.WarnContext(ctx, "metadata: failed to sync refresh debt after target refresh failure", "component", "metadata",
			"target_type", targetType,
			"content_id", contentID,
			"refresh_error", refreshErr,
			"error", err)
		return
	}
	noteScheduledRefreshFailure(ctx, targetType, contentID)
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
	staleReason, missingTMDBRejected, err := s.currentStaleRefreshDebtState(ctx, contentID, item)
	if err != nil {
		return err
	}
	reasonMask |= staleReason
	if missingTMDBRejected {
		reasonMask &^= RefreshDebtReasonProviderIDIncomplete
	}

	if reasonMask == 0 {
		return s.refreshDebtRepo.DeleteDebt(ctx, contentID)
	}

	attemptCount, err := s.currentRefreshDebtAttemptCount(ctx, contentID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	// A series row that keeps episode debt must wake when its episodes' recent
	// window closes, like the season and episode rows that hold the same debt.
	var actionable []*models.Episode
	if hasRefreshDebtReason(reasonMask, RefreshDebtReasonEpisodeIncomplete) && s.episodeRepo != nil {
		episodes, err := s.episodeRepo.ListBySeries(ctx, contentID)
		if err != nil {
			return err
		}
		for _, episode := range episodes {
			if EpisodeHasActionableMetadataDebt(episode, now) {
				actionable = append(actionable, episode)
			}
		}
	}
	logRefreshDebtTerminal(RefreshTargetItem, contentID, reasonMask, attemptCount,
		refreshAttemptCounted(ctx, RefreshTargetItem, contentID))
	return s.refreshDebtRepo.MarkSuccess(
		ctx,
		contentID,
		effectiveRefreshDebtPriority(reasonMask, attemptCount),
		reasonMask,
		nextRefreshAtForEpisodeDebt(reasonMask, attemptCount, now, actionable...),
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
	var actionable []*models.Episode
	for _, episode := range episodes {
		if EpisodeHasActionableMetadataDebt(episode, now) {
			reasonMask = RefreshDebtReasonEpisodeIncomplete
			actionable = append(actionable, episode)
		}
	}
	if reasonMask == 0 {
		return s.refreshDebtRepo.DeleteTargetDebt(ctx, RefreshTargetSeason, seasonID)
	}
	attemptCount, err := s.currentRefreshDebtTargetAttemptCount(ctx, RefreshTargetSeason, seasonID)
	if err != nil {
		return err
	}
	logRefreshDebtTerminal(RefreshTargetSeason, seasonID, reasonMask, attemptCount,
		refreshAttemptCounted(ctx, RefreshTargetSeason, seasonID))
	return s.refreshDebtRepo.MarkTargetSuccess(
		ctx,
		RefreshTargetSeason,
		seasonID,
		effectiveRefreshDebtPriority(reasonMask, attemptCount),
		reasonMask,
		nextRefreshAtForEpisodeDebt(reasonMask, attemptCount, now, actionable...),
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
	logRefreshDebtTerminal(RefreshTargetEpisode, episodeID, reasonMask, attemptCount,
		refreshAttemptCounted(ctx, RefreshTargetEpisode, episodeID))
	return s.refreshDebtRepo.MarkTargetSuccess(
		ctx,
		RefreshTargetEpisode,
		episodeID,
		effectiveRefreshDebtPriority(reasonMask, attemptCount),
		reasonMask,
		nextRefreshAtForEpisodeDebt(reasonMask, attemptCount, now, episode),
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
	staleReason, missingTMDBRejected, err := s.currentStaleRefreshDebtState(ctx, contentID, item)
	if err != nil {
		return err
	}
	reasonMask |= staleReason
	if missingTMDBRejected {
		reasonMask &^= RefreshDebtReasonProviderIDIncomplete
	}
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
	logRefreshDebtTerminal(RefreshTargetItem, contentID, reasonMask, attemptCount,
		incrementAttempt || refreshAttemptCounted(ctx, RefreshTargetItem, contentID))
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
	logRefreshDebtTerminal(targetType, contentID, reasonMask, attemptCount,
		incrementAttempt || refreshAttemptCounted(ctx, targetType, contentID))
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

func (s *MetadataService) currentStaleRefreshDebtState(
	ctx context.Context,
	contentID string,
	item *models.MediaItem,
) (reason int64, missingTMDBRejected bool, err error) {
	if s == nil || s.staleIDRepo == nil || strings.TrimSpace(contentID) == "" {
		return 0, false, nil
	}
	ids, err := s.staleIDRepo.GetByContentID(ctx, contentID)
	if err != nil {
		return 0, false, err
	}
	for _, staleID := range ids {
		if staleID != nil && strings.EqualFold(strings.TrimSpace(staleID.Provider), contentid.ProviderTMDB) &&
			strings.TrimSpace(staleID.ProviderID) != "" {
			missingTMDBRejected = true
		}
		if IsActionableStaleProviderID(item, staleID) {
			reason = RefreshDebtReasonStaleProviderID
		}
	}
	return reason, missingTMDBRejected, nil
}

func itemHasEpisodeMetadataDebt(item *models.MediaItem) bool {
	return item != nil &&
		strings.EqualFold(strings.TrimSpace(item.Type), "series") &&
		item.EpisodeMetadataIncomplete
}

// refreshSeasonTarget refreshes one season and reports the series it belongs to.
func (s *MetadataService) refreshSeasonTarget(ctx context.Context, seasonID string, folderID int, mode RefreshMode) (string, error) {
	if s == nil || s.seasonRepo == nil {
		return "", ErrMetadataNotFound
	}
	season, err := s.seasonRepo.GetByID(ctx, seasonID)
	if err != nil {
		return "", err
	}
	return season.SeriesID, s.refreshSeriesChildTarget(ctx, season.SeriesID, season.SeasonNumber, 0, folderID, mode)
}

// refreshEpisodeTarget refreshes one episode and reports the series it belongs to.
func (s *MetadataService) refreshEpisodeTarget(ctx context.Context, episodeID string, folderID int, mode RefreshMode) (string, error) {
	if s == nil || s.episodeRepo == nil {
		return "", ErrMetadataNotFound
	}
	episode, err := s.episodeRepo.GetByID(ctx, episodeID)
	if err != nil {
		return "", err
	}
	return episode.SeriesID, s.refreshSeriesChildTarget(ctx, episode.SeriesID, episode.SeasonNumber, episode.EpisodeNumber, folderID, mode)
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
	// A season or episode refresh searches providers for the series too, so
	// it must not run for a series an admin ignored.
	if ignored, err := s.matchIgnored(ctx, seriesID); err != nil {
		return err
	} else if ignored {
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
	// The link and debt passes cover the whole series, so a scheduled refresh
	// batch runs them once per series instead of once per season or episode.
	// They also run when a later language fails after an earlier one wrote
	// rows, on a context detached from the refresh's own cancellation, since
	// that failure may be the refresh's deadline.
	defer func() {
		if updated {
			syncCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), seriesEpisodeSyncTimeout)
			defer cancel()
			s.syncSeriesEpisodeStateOrDefer(syncCtx, seriesID)
		}
	}()
	// Listing the series' files once serves both the sidecar context and the
	// local identity check.
	localCtx := s.localProviderContextForContent(ctx, seriesID, folderID)
	childCtx, err := s.seriesChildLocalContextForContent(ctx, seriesID, folderID, localCtx)
	if err != nil {
		return err
	}
	skipLocal, err := s.seriesChildSkipsLocal(ctx, series, folderID, localCtx)
	if err != nil {
		return err
	}
	for _, language := range languages {
		canonicalLanguage := strings.TrimSpace(series.DefaultMetadataLanguage)
		if canonicalLanguage == "" {
			canonicalLanguage = strings.TrimSpace(language)
		}
		if canonicalLanguage == "" {
			canonicalLanguage = "en"
		}

		providerIDs, err := s.resolveSeriesRefreshProviderIDs(ctx, series, folderID, language, skipLocal)
		if err != nil {
			return err
		}
		seasons, seasonSearched, err := s.fetchTargetSeasonResults(ctx, providerIDs, folderID, language, seasonNumber, childCtx, skipLocal)
		if err != nil {
			return err
		}
		episodes, episodeSearched, err := s.fetchTargetEpisodeResults(ctx, providerIDs, folderID, language, seasonNumber, episodeNumber, childCtx, skipLocal)
		if err != nil {
			return err
		}
		if len(seasons) == 0 && len(episodes) == 0 {
			continue
		}
		if strings.EqualFold(language, canonicalLanguage) {
			s.fillTargetChildText(ctx, series, providerIDs, folderID, language, seasonNumber, episodeNumber, childCtx, seasons, episodes, mode, mergeMode)
		} else if s.configuredFallbackLanguage() != "" {
			// Text a provider marked as another language is no translation.
			dropForeignChildText(seasons, episodes, language)
		}
		persistCtx := ctx
		if s.localArtworkUsable() {
			persistCtx = withLocalArtworkSearched(ctx, seasonSearched|episodeSearched)
		}
		if s.persistSeasonAndEpisodeRows(persistCtx, series, providerIDs, canonicalLanguage, language, seasons, episodes, mergeMode) {
			updated = true
		}
	}
	if !updated {
		return ErrMetadataNotFound
	}
	return nil
}

func (s *MetadataService) resolveSeriesRefreshProviderIDs(ctx context.Context, series *models.MediaItem, folderID int, language string, skipLocal bool) (map[string]string, error) {
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
	sanitizeCanonicalProviderIDsInPlace(accumulatedIDs)
	recordedStaleIDs, err := s.loadRecordedStaleProviderIDs(ctx, series.ContentID)
	if err != nil {
		return nil, err
	}
	suppressProviderIDValues(accumulatedIDs, recordedStaleIDs)

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
	provider404s := newProvider404State()
	for _, p := range itemChain {
		sp, ok := p.(SearchProvider)
		if !ok {
			continue
		}
		if _, local := p.(IdentityHintProvider); local && skipLocal {
			continue
		}
		results, searchErr := sp.Search(ctx, searchQuery)
		if searchErr != nil {
			if handleProvider404(provider404s, accumulatedIDs, p.Slug(), searchErr,
				"content_id", series.ContentID,
			) {
				continue
			}
			slog.WarnContext(ctx, "metadata: target refresh search error", "component", "metadata",
				"provider", p.Slug(), "error", searchErr)
			continue
		}
		for _, result := range results {
			if searchResultConflictsWithTrustedIDs(accumulatedIDs, result.ProviderIDs) {
				slog.WarnContext(ctx, "metadata: retaining conflicting target-refresh result for consensus", "component", "metadata",
					"provider", p.Slug(), "content_id", series.ContentID,
					"hinted_ids", accumulatedIDs, "candidate_ids", result.ProviderIDs)
			}
			allResults = append(allResults, result)
		}
	}
	candidates := NormalizeCandidatesForLanguage(allResults, series.Type, searchQuery.Language)
	selectionItem := mediaItemWithProviderIDs(series, accumulatedIDs)
	if winner, ok := selectRefreshMatchCandidate(selectionItem, nil, candidates); ok && winner != nil {
		applyCandidateProviderIDConsensus(accumulatedIDs, winner, nil)
	}
	suppressProviderIDValues(accumulatedIDs, recordedStaleIDs)
	suppressProviderIDValues(accumulatedIDs, provider404s.dropped)
	return accumulatedIDs, nil
}

// fetchTargetSeasonResults also reports localArtworkSeason when the local
// provider answered, as processInternal records it.
func (s *MetadataService) fetchTargetSeasonResults(ctx context.Context, providerIDs map[string]string, folderID int, language string, seasonNumber int, childCtx seriesChildLocalContext, skipLocal bool) ([]SeasonResult, localArtworkLevel, error) {
	seasonChain, err := s.resolveChainCached(ctx, folderID, "season")
	if err != nil {
		return nil, 0, fmt.Errorf("resolve season provider chain: %w", err)
	}
	var searched localArtworkLevel
	seasonResults := make(map[int]*SeasonResult)
	for _, p := range seasonChain {
		ep, ok := p.(EpisodeProvider)
		if !ok {
			continue
		}
		if _, local := p.(IdentityHintProvider); local && skipLocal {
			continue
		}
		seasons, err := ep.GetSeasons(ctx, SeasonsRequest{
			ProviderIDs:          providerIDs,
			ContentType:          "series",
			Language:             language,
			SeriesRootPaths:      childCtx.seriesRootPaths,
			SeasonDirectoryPaths: targetSeasonPaths(childCtx.seasonDirectoryPaths, seasonNumber),
			ShowFilePaths:        showFilePaths(childCtx.episodeFilePaths),
		})
		if errors.Is(err, ErrLocalMetadataUnavailable) {
			return nil, 0, fmt.Errorf("metadata: %s seasons: %w", p.Slug(), err)
		}
		if err != nil {
			if handleScopedProvider404(p.Slug(), providerIDs, err, "season", seasonNumber) {
				continue
			}
			slog.WarnContext(ctx, "metadata: target season provider error", "component", "metadata",
				"provider", p.Slug(), "season", seasonNumber, "error", err)
			continue
		}
		if !s.localArtworkUsable() {
			dropLocalSeasonArtwork(seasons)
		}
		if _, local := p.(IdentityHintProvider); local {
			searched |= localArtworkSeason
		}
		for _, season := range seasons {
			if season.SeasonNumber != seasonNumber {
				continue
			}
			accumulateSeasonResults(seasonResults, []SeasonResult{season}, language)
		}
	}
	return flattenSeasonResults(seasonResults), searched, nil
}

// fetchTargetEpisodeResults also reports localArtworkEpisode when the local
// provider answered, as processInternal records it.
func (s *MetadataService) fetchTargetEpisodeResults(ctx context.Context, providerIDs map[string]string, folderID int, language string, seasonNumber int, episodeNumber int, childCtx seriesChildLocalContext, skipLocal bool) ([]EpisodeResult, localArtworkLevel, error) {
	episodeChain, err := s.resolveChainCached(ctx, folderID, "episode")
	if err != nil {
		return nil, 0, fmt.Errorf("resolve episode provider chain: %w", err)
	}
	var searched localArtworkLevel
	episodeResults := make(map[episodeResultKey]*EpisodeResult)
	for _, p := range episodeChain {
		ep, ok := p.(EpisodeProvider)
		if !ok {
			continue
		}
		if _, local := p.(IdentityHintProvider); local && skipLocal {
			continue
		}
		episodes, err := ep.GetEpisodes(ctx, EpisodesRequest{
			ProviderIDs:      providerIDs,
			SeasonNumber:     seasonNumber,
			Language:         language,
			SeriesRootPaths:  childCtx.seriesRootPaths,
			EpisodeFilePaths: targetEpisodeFilePaths(childCtx.episodeFilePaths[seasonNumber], episodeNumber),
			ShowFilePaths:    showFilePaths(childCtx.episodeFilePaths),
		})
		if errors.Is(err, ErrLocalMetadataUnavailable) {
			return nil, 0, fmt.Errorf("metadata: %s episodes: %w", p.Slug(), err)
		}
		if err != nil {
			if handleScopedProvider404(p.Slug(), providerIDs, err, "season", seasonNumber) {
				continue
			}
			slog.WarnContext(ctx, "metadata: target episode provider error", "component", "metadata",
				"provider", p.Slug(), "season", seasonNumber, "error", err)
			continue
		}
		if !s.localArtworkUsable() {
			dropLocalEpisodeArtwork(episodes)
		}
		if _, local := p.(IdentityHintProvider); local {
			searched |= localArtworkEpisode
			markLocalEpisodeTitles(episodes)
		}
		for _, episode := range episodes {
			if episode.SeasonNumber != seasonNumber {
				continue
			}
			if episodeNumber > 0 && episode.EpisodeNumber != episodeNumber {
				continue
			}
			accumulateEpisodeResults(episodeResults, []EpisodeResult{episode}, language)
		}
	}
	return flattenEpisodeResults(episodeResults), searched, nil
}

func (s *MetadataService) enqueueSeriesChildImages(ctx context.Context, seriesID string, inputs []EnqueueImageCacheJobInput) {
	_ = s.enqueueImageCacheJobs(ctx, "series child", seriesID, inputs)
}

func (s *MetadataService) enqueueImageCacheJobs(ctx context.Context, targetKind, targetID string, inputs []EnqueueImageCacheJobInput) error {
	// Caching off stores nothing: clients load artwork straight from the
	// providers, and refreshes select no local artwork.
	if s == nil || s.imageCacheJobs == nil || len(inputs) == 0 || !s.autoCacheImages.Load() {
		return nil
	}
	enqueueCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	inputs = s.copyLocalArtwork(enqueueCtx, targetKind, targetID, inputs)
	if len(inputs) == 0 {
		return nil
	}
	if _, err := s.imageCacheJobs.EnqueueBatch(enqueueCtx, inputs); err != nil {
		slog.WarnContext(ctx, "metadata: failed to enqueue image cache jobs", "component", "metadata",
			"target_kind", targetKind, "target_id", targetID, "count", len(inputs), "error", err)
		return err
	}
	return nil
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
		!isProviderImagePath(path) &&
		!isLocalImageSourcePath(path)
}

func isRemoteImageSourcePath(path string) bool {
	path = strings.TrimSpace(path)
	lower := strings.ToLower(path)
	return path != "" &&
		path != "-" &&
		strings.Contains(path, "://") &&
		!isNonProviderImageScheme(lower)
}

// isLocalImageSourcePath reports whether path is an artwork source the
// item's local metadata supplies (produced by the NFO provider): a sidecar
// file (file://) or an image URL an NFO names (nfo+http(s)://, see
// LocalMetadataURLSource). Local sources route into *_source_path like remote
// sources and are copied into the S3 image cache by the processor; they are
// never served directly, and they outrank remote providers' artwork.
func isLocalImageSourcePath(path string) bool {
	lower := strings.ToLower(strings.TrimSpace(path))
	return strings.HasPrefix(lower, "file://") || isLocalMetadataURLSource(lower)
}

// localArtworkUsable reports whether refreshes may select local artwork: a
// sidecar file (file://) or an image URL an NFO names (nfo+http(s)://). Neither
// is served directly; only an image-cache job copies it into artwork storage.
// With metadata.cache_images off nothing is copied, because clients load
// artwork straight from the providers, and without the queue (no artwork store
// wired) a chosen local image would leave the slot empty instead of falling
// back to provider artwork. Either way refreshes ignore local art altogether.
func (s *MetadataService) localArtworkUsable() bool {
	return s != nil && s.autoCacheImages.Load() && s.imageCacheJobs != nil
}

// withoutLocalImages returns images minus local candidates. It returns images
// itself when none are local.
func withoutLocalImages(images []RemoteImage) []RemoteImage {
	local := func(img RemoteImage) bool { return isLocalImageSourcePath(img.URL) }
	if !slices.ContainsFunc(images, local) {
		return images
	}
	return slices.DeleteFunc(slices.Clone(images), local)
}

// dropLocalSeasonArtwork clears local posters so a later provider in the chain
// can fill the slot.
func dropLocalSeasonArtwork(seasons []SeasonResult) {
	for i := range seasons {
		if isLocalImageSourcePath(seasons[i].PosterPath) {
			seasons[i].PosterPath = ""
		}
	}
}

// dropLocalEpisodeArtwork clears local stills so a later provider in the chain
// can fill the slot.
func dropLocalEpisodeArtwork(episodes []EpisodeResult) {
	for i := range episodes {
		if isLocalImageSourcePath(episodes[i].StillPath) {
			episodes[i].StillPath = ""
		}
	}
}

// isCacheableImageSourcePath gates the image-cache enqueue paths: remote
// provider URLs and local file:// sidecars both queue cache jobs.
func isCacheableImageSourcePath(path string) bool {
	return isRemoteImageSourcePath(path) || isLocalImageSourcePath(path)
}

func isNonProviderImageScheme(lowerPath string) bool {
	return strings.HasPrefix(lowerPath, "s3://") ||
		strings.HasPrefix(lowerPath, "file://") ||
		strings.HasPrefix(lowerPath, localMetadataURLPrefix) ||
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

// splitProviderImagePath separates a provider-returned image path into the
// served path and the cacheable source path. A local file:// sidecar must
// never land in the served column: it routes into *_source_path only (the
// cache job fills the served path later). Remote sources keep the existing
// contract: the URL occupies both columns until the cache job lands.
func splitProviderImagePath(path string) (servedPath, sourcePath string) {
	if isLocalImageSourcePath(path) {
		return "", strings.TrimSpace(path)
	}
	return path, providerImageSourcePath(path)
}

// preserveCachedArtwork merges a slot's provider path with what the slot
// stores. keepStored is false while metadata.cache_images is off and the
// slot's artwork is not locked: clients then load provider artwork straight
// from the providers, so a provider path replaces a stored copy instead of
// only becoming its source.
func preserveCachedArtwork(providerPath, providerThumbhash, existingCachedPath, existingSourcePath, existingThumbhash string, keepStored bool) (string, string, string) {
	// A local file:// source must never land in the served *_path column: it
	// routes into *_source_path and, during the pre-cache window, the path
	// keeps the prior cached key or remote stand-in (or stays empty) until the
	// job completes.
	keepExisting := isCachedImagePath(existingCachedPath) || isLocalArtworkStandIn(existingCachedPath, existingSourcePath)
	if isLocalImageSourcePath(providerPath) {
		providerPath = strings.TrimSpace(providerPath)
		if keepExisting {
			return existingCachedPath, existingThumbhash, providerPath
		}
		return "", "", providerPath
	}
	if !isRemoteImageSourcePath(providerPath) {
		// With nothing offered, a slot keeps what it has, including a local
		// source whose copy is still pending.
		if strings.TrimSpace(providerPath) == "" && (isCachedImagePath(existingCachedPath) || isLocalImageSourcePath(existingSourcePath)) {
			return existingCachedPath, existingThumbhash, existingSourcePath
		}
		return providerPath, providerThumbhash, ""
	}
	if keepStored && isCachedImagePath(existingCachedPath) {
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
		slog.WarnContext(ctx, "metadata: cannot cache plugin image without resolver", "component", "metadata", "url", path)
		return "", false
	}
	resolved := s.imageResolver.ResolveImageURL(ctx, path, "original")
	if resolved == "" {
		slog.WarnContext(ctx, "metadata: resolver returned empty URL for image", "component", "metadata", "url", path)
		return "", false
	}
	return resolved, true
}

type episodeResultKey struct {
	seasonNumber  int
	episodeNumber int
}

// accumulateSeasonResults merges a provider's seasons into accumulator,
// earlier providers first, except that text in language replaces text a
// provider marked as another language.
func accumulateSeasonResults(accumulator map[int]*SeasonResult, seasons []SeasonResult, language string) {
	for _, season := range seasons {
		existing := accumulator[season.SeasonNumber]
		if existing == nil {
			cp := season
			accumulator[season.SeasonNumber] = &cp
			continue
		}

		preferRequestedLanguageText(&existing.Title, &existing.TitleLanguage, season.Title, season.TitleLanguage, language)
		preferRequestedLanguageText(&existing.Overview, &existing.OverviewLanguage, season.Overview, season.OverviewLanguage, language)
		preferLocalSeasonPoster(existing, &season)
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

// accumulateEpisodeResults merges a provider's episodes into accumulator the
// way accumulateSeasonResults merges seasons.
func accumulateEpisodeResults(accumulator map[episodeResultKey]*EpisodeResult, episodes []EpisodeResult, language string) {
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

		foldEpisodeTitle(existing, episode, language)
		preferRequestedLanguageText(&existing.Overview, &existing.OverviewLanguage, episode.Overview, episode.OverviewLanguage, language)

		recordEpisodeStillFallback(existing, episode)
		preferLocalEpisodeStill(existing, &episode)
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

// dropAIText clears localized text an AI translation wrote before provider
// text is merged in, so provider text replaces it in either merge mode. With
// no provider text the field goes out empty, and the localization repository
// keeps the stored AI text and its ai source; carrying it forward would label
// it provider.
func dropAIText(value *string, source string) {
	if source == models.LocalizationSourceAI {
		*value = ""
	}
}

// localizedItemText is the text a non-canonical write stores in the item's
// localization row: the text fetched in the request language, except that a
// locked field keeps the stored canonical value, as the base item does. A
// provider's fallback title (the original title, for want of one in the
// request language) is no translation, so the row keeps what it has or falls
// back to the base title.
func localizedItemText(fetched, canonical *MetadataResult, locked []MetadataField) *MetadataResult {
	text := *fetched
	if text.TitleIsFallback {
		text.Title, text.SortTitle = "", ""
	}
	if isFieldLocked(locked, FieldName) {
		text.Title, text.SortTitle = canonical.Title, canonical.SortTitle
	}
	if isFieldLocked(locked, FieldOverview) {
		text.Overview, text.Tagline = canonical.Overview, canonical.Tagline
	}
	return &text
}

// clearCanonicalCopy empties a stored localized value identical to the
// canonical one. Such a value is a copy of the base text, not a translation:
// earlier releases wrote the canonical text into the localization row of
// every non-canonical refresh. Cleared, it takes this refresh's text in either
// merge mode, and with none it falls back to the identical base value.
func clearCanonicalCopy(value *string, canonical string) {
	if *value != "" && *value == canonical {
		*value = ""
	}
}

func buildItemLocalizationRecord(
	existing *models.MediaItemLocalization,
	contentID string,
	language string,
	contentType string,
	accumulator *MetadataResult,
	canonical *MetadataResult,
	images []RemoteImage,
	mergeMode MergeMode,
	preferredLanguage, fallbackLanguage string,
	titleLocked bool,
	keepStored bool,
) *models.MediaItemLocalization {
	loc := &models.MediaItemLocalization{
		ContentID: contentID,
		Language:  language,
	}
	if existing != nil {
		*loc = *existing
		loc.ContentID = contentID
		loc.Language = language
		// AI text is not dropped here, unlike seasons and episodes: a locked
		// field passes the canonical value as accumulator, so a dropped field
		// would fill with canonical text. A nil canonical leaves stored copies
		// alone.
		if canonical != nil {
			clearCanonicalCopy(&loc.Title, canonical.Title)
			clearCanonicalCopy(&loc.SortTitle, canonical.SortTitle)
			if loc.OverviewSource == models.LocalizationSourceProvider {
				clearCanonicalCopy(&loc.Overview, canonical.Overview)
			}
			if loc.TaglineSource == models.LocalizationSourceProvider {
				clearCanonicalCopy(&loc.Tagline, canonical.Tagline)
			}
		}
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
	// Local sidecar art is language-neutral: it must not duplicate into every
	// localization row, so local candidates only compete at the item level.
	applyBestImages(locItem, withoutLocalImages(images), mergeMode, preferredLanguage, fallbackLanguage)
	prepareItemImagesForQueue(locItem, existingLocItem, keepStored)

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
	keepStored bool,
) *models.SeasonLocalization {
	loc := &models.SeasonLocalization{
		SeasonContentID: seasonContentID,
		Language:        language,
	}
	if existing != nil {
		*loc = *existing
		loc.SeasonContentID = seasonContentID
		loc.Language = language
		dropAIText(&loc.Overview, loc.OverviewSource)
	}

	previousPosterPath := loc.PosterPath
	previousPosterSourcePath := loc.PosterSourcePath
	previousPosterThumbhash := loc.PosterThumbhash

	// Local art belongs to the base season row; the localization row takes
	// the provider poster a local one displaced.
	if season.remotePosterPath != "" {
		season.PosterPath, season.PosterThumbhash = season.remotePosterPath, season.remotePosterThumbhash
	}

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
		keepStored,
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
		dropAIText(&loc.Overview, loc.OverviewSource)
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

// existingItemArtworkField finds the stored item's slot for imageType.
func existingItemArtworkField(item *models.MediaItem, imageType ImageType) (itemArtworkField, bool) {
	for _, field := range itemArtworkFields(item) {
		if field.imageType == imageType {
			return field, true
		}
	}
	return itemArtworkField{}, false
}

func existingImagePath(item *models.MediaItem, imageType ImageType) string {
	if field, ok := existingItemArtworkField(item, imageType); ok {
		return *field.path
	}
	return ""
}

func existingImageThumbhash(item *models.MediaItem, imageType ImageType) string {
	if field, ok := existingItemArtworkField(item, imageType); ok && field.thumbhash != nil {
		return *field.thumbhash
	}
	return ""
}

func existingImageSourcePath(item *models.MediaItem, imageType ImageType) string {
	if field, ok := existingItemArtworkField(item, imageType); ok {
		return *field.source
	}
	return ""
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

// ResolveSeriesTVDBID asks the enabled series providers for the TVDB ID of a
// series known by its IMDb or TMDB ID (the TVDB provider resolves these through
// TVDB's remote-ID search). IMDb goes first because its IDs are unambiguous. A
// result counts only when it echoes the ID it was looked up by and does not
// name a different TMDB series, so neither a bare TMDB number that matches some
// other source's ID on TVDB nor an IMDb ID for another series can resolve to the
// wrong series. Returns 0 and a nil error when every provider answered and none
// knows a match; returns 0 and the provider errors when nothing matched and at
// least one provider failed, so callers can tell an outage from a miss.
func (s *MetadataService) ResolveSeriesTVDBID(ctx context.Context, tmdbID int, imdbID string) (int, error) {
	var lookups []map[string]string
	if imdb := strings.TrimSpace(imdbID); imdb != "" {
		lookups = append(lookups, map[string]string{"imdb": imdb})
	}
	if tmdbID > 0 {
		lookups = append(lookups, map[string]string{"tmdb": strconv.Itoa(tmdbID)})
	}
	if len(lookups) == 0 {
		return 0, nil
	}
	chain, err := s.resolveChainCached(ctx, 0, "series")
	if err != nil {
		return 0, fmt.Errorf("resolving provider chain: %w", err)
	}
	var searchErrs []error
	for _, ids := range lookups {
		for _, p := range chain {
			sp, ok := p.(SearchProvider)
			if !ok {
				continue
			}
			results, err := sp.Search(ctx, SearchQuery{ContentType: "series", ProviderIDs: ids})
			if err != nil {
				searchErrs = append(searchErrs, fmt.Errorf("%s: %w", p.Slug(), err))
				continue
			}
			for _, result := range results {
				if !providerIDsConfirm(result.ProviderIDs, ids) || conflictsWithTMDBID(result.ProviderIDs, tmdbID) {
					continue
				}
				if tvdbID, err := strconv.Atoi(strings.TrimSpace(result.ProviderIDs["tvdb"])); err == nil && tvdbID > 0 {
					return tvdbID, nil
				}
			}
		}
	}
	return 0, errors.Join(searchErrs...)
}

// conflictsWithTMDBID reports whether a result names a TMDB series other than
// the one being resolved.
func conflictsWithTMDBID(ids map[string]string, tmdbID int) bool {
	got := strings.TrimSpace(ids["tmdb"])
	return tmdbID > 0 && got != "" && got != strconv.Itoa(tmdbID)
}

// providerIDsConfirm reports whether got carries every ID in want.
func providerIDsConfirm(got, want map[string]string) bool {
	for key, value := range want {
		if !strings.EqualFold(strings.TrimSpace(got[key]), value) {
			return false
		}
	}
	return true
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

func bulkUpsertWithFallback[T any](
	items []T,
	bulk func([]T) error,
	onBulkErr func(error),
	single func(T) error,
	onRowErr func(T, error),
) []T {
	if err := bulk(items); err == nil {
		return items
	} else {
		onBulkErr(err)
	}

	succeeded := make([]T, 0, len(items))
	for _, item := range items {
		if err := single(item); err != nil {
			onRowErr(item, err)
			continue
		}
		succeeded = append(succeeded, item)
	}
	return succeeded
}

// persistSeasonsAndEpisodes writes provider seasons and episodes for a whole
// series refresh, then relinks the series' files and re-syncs its episode
// debt straight away: the caller's own debt sync reads the result.
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
	if !s.persistSeasonAndEpisodeRows(ctx, series, providerIDs, canonicalLanguage, language, seasons, episodes, mergeMode) {
		return
	}
	s.syncSeriesEpisodeState(ctx, series.ContentID)
}

// persistSeasonAndEpisodeRows writes provider seasons and episodes and queues
// their images without the series-wide link and debt passes. It reports false
// when there is no series to write to.
func (s *MetadataService) persistSeasonAndEpisodeRows(
	ctx context.Context,
	series *models.MediaItem,
	providerIDs map[string]string,
	canonicalLanguage string,
	language string,
	seasons []SeasonResult,
	episodes []EpisodeResult,
	mergeMode MergeMode,
) bool {
	if series == nil || strings.TrimSpace(series.ContentID) == "" {
		return false
	}
	seriesID := series.ContentID
	seasonIDs := make(map[int]string, len(seasons))
	isCanonicalWrite := strings.EqualFold(canonicalLanguage, language)
	imagesLocked := artworkLocked(series)
	imageJobs := make([]EnqueueImageCacheJobInput, 0, len(seasons)+len(episodes))
	seasonArtwork := newLocalArtworkRetention(ctx, seriesID, localArtworkSeason)
	episodeArtwork := seasonArtwork.atLevel(localArtworkEpisode)
	fallbackProvider := primaryProviderID(providerIDs)
	keyAttribution := func(sourcePath string) (string, string) {
		// Local sidecar sources attribute to the synthetic "local" provider,
		// keyed by the series' own content ID (mirrors item attribution).
		if isLocalImageSourcePath(sourcePath) {
			return localSourceProviderID(sourcePath), seriesID
		}
		providerID := providerIDFromPluginURL(sourcePath)
		if providerID == "" {
			providerID = fallbackProvider
		}
		return providerID, findContentID(series, providerID)
	}
	addSeasonImageJob := func(season *models.Season) {
		if season == nil || !isCacheableImageSourcePath(season.PosterSourcePath) {
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
			// Seasons of a provider-anchored series keep their content ID
			// when the series is rebuilt; see enqueueItemImages.
			RequeueSucceeded: !isCachedImagePath(season.PosterPath),
		})
	}
	addEpisodeImageJob := func(episode *models.Episode) {
		if episode == nil || !isCacheableImageSourcePath(episode.StillSourcePath) {
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
			RequeueSucceeded:  !isCachedImagePath(episode.StillPath),
		})
	}
	addSeasonLocalizationImageJob := func(season *models.Season, loc *models.SeasonLocalization) {
		if season == nil || loc == nil || !isCacheableImageSourcePath(loc.PosterSourcePath) {
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
			RequeueSucceeded:  !isCachedImagePath(loc.PosterPath),
		})
	}

	type preparedSeasonWrite struct {
		model    *models.Season
		provider SeasonResult
	}
	type preparedEpisodeWrite struct {
		model    *models.Episode
		provider EpisodeResult
	}

	// One targeted read replaces the per-season point lookups without loading
	// unrelated seasons during a scoped refresh. If it fails, each phase falls
	// back to the original point-read behavior so a transient prefetch failure
	// cannot turn existing rows into new identities.
	existingSeasons := make(map[int]*models.Season)
	seasonsPrefetched := false
	if len(seasons) > 0 || len(episodes) > 0 {
		seasonNumberSet := make(map[int]struct{}, len(seasons)+len(episodes))
		seasonNumbers := make([]int32, 0, len(seasons)+len(episodes))
		seasonNumbersValid := true
		addSeasonNumber := func(seasonNumber int) {
			if _, ok := seasonNumberSet[seasonNumber]; ok {
				return
			}
			seasonNumberSet[seasonNumber] = struct{}{}
			if !catalog.FitsPostgresInteger(seasonNumber) {
				seasonNumbersValid = false
				return
			}
			seasonNumbers = append(seasonNumbers, int32(seasonNumber))
		}
		for _, season := range seasons {
			addSeasonNumber(season.SeasonNumber)
		}
		for _, episode := range episodes {
			addSeasonNumber(episode.SeasonNumber)
		}

		if seasonNumbersValid {
			storedSeasons, err := s.seasonRepo.ListBySeriesAndNumbers(ctx, seriesID, seasonNumbers)
			if err != nil {
				slog.WarnContext(ctx, "metadata: failed to prefetch seasons before bulk upsert", "component", "metadata",
					"series_id", seriesID, "error", err)
			} else {
				seasonsPrefetched = true
				for _, storedSeason := range storedSeasons {
					if storedSeason != nil {
						existingSeasons[storedSeason.SeasonNumber] = storedSeason
					}
				}
			}
		}
	}
	loadSeason := func(seasonNumber int, forcePointRead bool) (*models.Season, error) {
		if seasonsPrefetched && !forcePointRead {
			if storedSeason := existingSeasons[seasonNumber]; storedSeason != nil {
				return storedSeason, nil
			}
			return nil, catalog.ErrSeasonNotFound
		}
		return s.seasonRepo.GetBySeriesAndNumber(ctx, seriesID, seasonNumber)
	}

	prepareExplicitSeason := func(season SeasonResult, forcePointRead bool) (preparedSeasonWrite, bool) {
		existingSeason, err := loadSeason(season.SeasonNumber, forcePointRead)
		if err != nil && !errors.Is(err, catalog.ErrSeasonNotFound) {
			slog.WarnContext(ctx, "metadata: failed to load season before upsert", "component", "metadata",
				"series_id", seriesID, "season", season.SeasonNumber, "error", err)
			return preparedSeasonWrite{}, false
		}
		providerSeason := season
		providerSeason.PosterPath, providerSeason.PosterSourcePath = splitProviderImagePath(season.PosterPath)
		if existingSeason != nil && isCanonicalWrite {
			mergedSeason := seasonResultFromModel(existingSeason)
			MergeSeasonResult(&providerSeason, &mergedSeason, mergeMode)
			// preserveCachedArtwork sees the raw provider path so a local
			// file:// source still routes into *_source_path here.
			nextPath, nextThumbhash, nextSourcePath := preserveCachedArtwork(
				seasonArtwork.providerPath(ImagePoster, season.PosterPath, existingSeason.PosterSourcePath),
				season.PosterThumbhash,
				existingSeason.PosterPath,
				existingSeason.PosterSourcePath,
				existingSeason.PosterThumbhash,
				s.autoCacheImages.Load(),
			)
			if imagesLocked && existingSeason.PosterPath != "" {
				nextPath = existingSeason.PosterPath
				nextThumbhash = existingSeason.PosterThumbhash
				nextSourcePath = existingSeason.PosterSourcePath
			}
			mergedSeason.PosterPath = nextPath
			mergedSeason.PosterThumbhash = nextThumbhash
			mergedSeason.PosterSourcePath = nextSourcePath
			providerSeason = mergedSeason
		}
		if !imagesLocked && providerSeason.PosterPath == "" {
			// The stand-in is the remote poster local art displaced or, when
			// retention kept a local source this refresh could not see, the
			// remote poster the refresh chose.
			fallback, fallbackThumbhash := season.remotePosterPath, season.remotePosterThumbhash
			if fallback == "" {
				fallback, fallbackThumbhash = season.PosterPath, season.PosterThumbhash
			}
			providerSeason.PosterPath = localArtworkFallbackPath("", providerSeason.PosterSourcePath, fallback)
			if providerSeason.PosterPath != "" {
				providerSeason.PosterThumbhash = fallbackThumbhash
			}
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
				slog.WarnContext(ctx, "metadata: failed to generate season id", "component", "metadata",
					"series_id", seriesID, "season", season.SeasonNumber, "error", genErr)
				return preparedSeasonWrite{}, false
			}
			dbSeason.ContentID = sid
		}
		if providerSeason.AirDate != "" {
			if t, parseErr := time.Parse("2006-01-02", providerSeason.AirDate); parseErr == nil {
				dbSeason.AirDate = &t
			}
		}
		// An artwork-only season (poster but no season.nfo) would otherwise
		// persist a blank title and then be skipped by fallback synthesis.
		if dbSeason.Title == "" {
			dbSeason.Title = fallbackSeasonTitle(dbSeason.SeasonNumber)
		}
		return preparedSeasonWrite{model: dbSeason, provider: providerSeason}, true
	}

	finishExplicitSeasonSequential := func(write preparedSeasonWrite) {
		dbSeason := write.model
		seasonIDs[dbSeason.SeasonNumber] = dbSeason.ContentID
		addSeasonImageJob(dbSeason)
		if !isCanonicalWrite && s.seasonLocalizationRepo != nil {
			existingLoc, locErr := s.seasonLocalizationRepo.Get(ctx, dbSeason.ContentID, language)
			if locErr != nil {
				slog.WarnContext(ctx, "metadata: failed to load season localization", "component", "metadata",
					"series_id", seriesID, "season", dbSeason.SeasonNumber, "error", locErr)
			}
			loc := buildSeasonLocalizationRecord(
				existingLoc,
				dbSeason.ContentID,
				language,
				write.provider,
				mergeMode,
				s.autoCacheImages.Load() || imagesLocked,
			)
			if err := s.seasonLocalizationRepo.Upsert(ctx, loc); err != nil {
				slog.WarnContext(ctx, "metadata: failed to upsert season localization", "component", "metadata",
					"series_id", seriesID, "season", dbSeason.SeasonNumber, "error", err)
			} else {
				addSeasonLocalizationImageJob(dbSeason, loc)
			}
		}
	}

	upsertExplicitModel := func(write preparedSeasonWrite) bool {
		if err := s.seasonRepo.Upsert(ctx, write.model); err != nil {
			slog.WarnContext(ctx, "metadata: failed to upsert season", "component", "metadata",
				"series_id", seriesID, "season", write.model.SeasonNumber, "error", err)
			return false
		}
		return true
	}

	finishExplicitSeasons := func(writes []preparedSeasonWrite) {
		for _, write := range writes {
			seasonIDs[write.model.SeasonNumber] = write.model.ContentID
		}

		var localizations []*models.SeasonLocalization
		localizationPersisted := make([]bool, len(writes))
		if !isCanonicalWrite && s.seasonLocalizationRepo != nil && len(writes) > 0 {
			seasonContentIDs := make([]string, len(writes))
			for i, write := range writes {
				seasonContentIDs[i] = write.model.ContentID
			}
			existingLocalizations, err := s.seasonLocalizationRepo.GetBySeasonIDs(ctx, seasonContentIDs, language)
			if err != nil {
				slog.WarnContext(ctx, "metadata: failed to batch load season localizations; falling back to point reads",
					"component", "metadata", "series_id", seriesID, "count", len(writes), "error", err)
				existingLocalizations = make(map[string]*models.SeasonLocalization, len(writes))
				for _, write := range writes {
					existingLoc, locErr := s.seasonLocalizationRepo.Get(ctx, write.model.ContentID, language)
					if locErr != nil {
						slog.WarnContext(ctx, "metadata: failed to load season localization", "component", "metadata",
							"series_id", seriesID, "season", write.model.SeasonNumber, "error", locErr)
						continue
					}
					existingLocalizations[write.model.ContentID] = existingLoc
				}
			}

			localizations = make([]*models.SeasonLocalization, len(writes))
			for i, write := range writes {
				localizations[i] = buildSeasonLocalizationRecord(
					existingLocalizations[write.model.ContentID],
					write.model.ContentID,
					language,
					write.provider,
					mergeMode,
					s.autoCacheImages.Load() || imagesLocked,
				)
			}
			localizationIndexes := make(map[*models.SeasonLocalization]int, len(localizations))
			for i, loc := range localizations {
				localizationIndexes[loc] = i
			}
			persistedLocalizations := bulkUpsertWithFallback(localizations,
				func(items []*models.SeasonLocalization) error {
					return s.seasonLocalizationRepo.BulkUpsert(ctx, items)
				},
				func(err error) {
					slog.WarnContext(ctx, "metadata: failed to bulk upsert season localizations; falling back to single-row writes",
						"component", "metadata", "series_id", seriesID, "count", len(localizations), "error", err)
				},
				func(loc *models.SeasonLocalization) error {
					return s.seasonLocalizationRepo.Upsert(ctx, loc)
				},
				func(loc *models.SeasonLocalization, err error) {
					i := localizationIndexes[loc]
					slog.WarnContext(ctx, "metadata: failed to upsert season localization", "component", "metadata",
						"series_id", seriesID, "season", writes[i].model.SeasonNumber, "error", err)
				},
			)
			for _, loc := range persistedLocalizations {
				localizationPersisted[localizationIndexes[loc]] = true
			}
		}

		for i, write := range writes {
			addSeasonImageJob(write.model)
			if localizationPersisted[i] {
				addSeasonLocalizationImageJob(write.model, localizations[i])
			}
		}
	}

	// Phase 1: Upsert explicit seasons. Duplicate natural keys are processed
	// sequentially because PostgreSQL cannot affect one conflict row twice in a
	// single INSERT, and the original path allowed last-writer behavior.
	if len(seasons) > 0 {
		seenSeasonNumbers := make(map[int]struct{}, len(seasons))
		hasDuplicateSeason := false
		for _, season := range seasons {
			if _, ok := seenSeasonNumbers[season.SeasonNumber]; ok {
				hasDuplicateSeason = true
				break
			}
			seenSeasonNumbers[season.SeasonNumber] = struct{}{}
		}

		if hasDuplicateSeason {
			for _, season := range seasons {
				if write, ok := prepareExplicitSeason(season, true); ok {
					if upsertExplicitModel(write) {
						finishExplicitSeasonSequential(write)
					}
				}
			}
		} else {
			writes := make([]preparedSeasonWrite, 0, len(seasons))
			modelsToPersist := make([]*models.Season, 0, len(seasons))
			for _, season := range seasons {
				if write, ok := prepareExplicitSeason(season, false); ok {
					writes = append(writes, write)
					modelsToPersist = append(modelsToPersist, write.model)
				}
			}
			if len(modelsToPersist) > 0 {
				successfulWrites := bulkUpsertWithFallback(writes,
					func([]preparedSeasonWrite) error {
						return s.seasonRepo.BulkUpsert(ctx, modelsToPersist)
					},
					func(err error) {
						slog.WarnContext(ctx, "metadata: failed to bulk upsert seasons; falling back to single-row writes",
							"component", "metadata", "series_id", seriesID, "count", len(modelsToPersist), "error", err)
					},
					func(write preparedSeasonWrite) error {
						if upsertExplicitModel(write) {
							return nil
						}
						return errors.New("explicit season upsert failed")
					},
					func(preparedSeasonWrite, error) {},
				)
				finishExplicitSeasons(successfulWrites)
			}
		}
	}

	// Phase 2: Persist implicit seasons referenced by episodes but absent from
	// the successfully persisted explicit-season map. Keeping this as a second
	// batch preserves the existing retry opportunity when an explicit write
	// fails but an episode still references that season.
	if len(episodes) > 0 {
		implicitSeen := make(map[int]bool)
		writes := make([]preparedSeasonWrite, 0)
		modelsToPersist := make([]*models.Season, 0)
		for _, ep := range episodes {
			if _, ok := seasonIDs[ep.SeasonNumber]; ok || implicitSeen[ep.SeasonNumber] {
				continue
			}
			implicitSeen[ep.SeasonNumber] = true
			title := fallbackSeasonTitle(ep.SeasonNumber)
			seasonModel := &models.Season{
				SeriesID:                seriesID,
				SeasonNumber:            ep.SeasonNumber,
				Title:                   title,
				DefaultMetadataLanguage: canonicalLanguage,
				MetadataSource:          "provider",
			}
			if existingSeason, err := loadSeason(ep.SeasonNumber, false); err == nil && existingSeason != nil {
				seasonModel.ContentID = existingSeason.ContentID
				seasonModel.DefaultMetadataLanguage = existingSeason.DefaultMetadataLanguage
				if isCanonicalWrite {
					mergedSeason := seasonResultFromModel(existingSeason)
					// The fallback title only names a season that has none:
					// a season referenced only by its episodes keeps its
					// stored title, even in a replacing merge.
					implicit := SeasonResult{SeasonNumber: ep.SeasonNumber}
					if strings.TrimSpace(existingSeason.Title) == "" {
						implicit.Title = title
					}
					MergeSeasonResult(&implicit, &mergedSeason, mergeMode)
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
					slog.WarnContext(ctx, "metadata: failed to generate implicit season id", "component", "metadata",
						"series_id", seriesID, "season", ep.SeasonNumber, "error", genErr)
					continue
				}
				seasonModel.ContentID = sid
			}
			write := preparedSeasonWrite{model: seasonModel}
			writes = append(writes, write)
			modelsToPersist = append(modelsToPersist, seasonModel)
		}

		finishImplicitSeason := func(write preparedSeasonWrite) {
			seasonIDs[write.model.SeasonNumber] = write.model.ContentID
			addSeasonImageJob(write.model)
		}
		upsertImplicitOne := func(write preparedSeasonWrite) error {
			if err := s.seasonRepo.Upsert(ctx, write.model); err != nil {
				return err
			}
			return nil
		}

		if len(modelsToPersist) > 0 {
			bulkFailed := false
			successfulWrites := bulkUpsertWithFallback(writes,
				func([]preparedSeasonWrite) error {
					return s.seasonRepo.BulkUpsert(ctx, modelsToPersist)
				},
				func(err error) {
					bulkFailed = true
					slog.WarnContext(ctx, "metadata: failed to bulk upsert implicit seasons; falling back to single-row writes",
						"component", "metadata", "series_id", seriesID, "count", len(modelsToPersist), "error", err)
				},
				func(write preparedSeasonWrite) error {
					if err := upsertImplicitOne(write); err != nil {
						return err
					}
					finishImplicitSeason(write)
					return nil
				},
				func(write preparedSeasonWrite, err error) {
					slog.WarnContext(ctx, "metadata: failed to upsert implicit season", "component", "metadata",
						"series_id", seriesID, "season", write.model.SeasonNumber, "error", err)
				},
			)
			if !bulkFailed {
				for _, write := range successfulWrites {
					finishImplicitSeason(write)
				}
			}
		}
	}

	// Phase 3: Prefetch the requested episode rows, including provider-only rows
	// without episode_libraries membership, then persist the prepared models as
	// one transaction-backed batch.
	if len(episodes) > 0 {
		seenEpisodeKeys := make(map[episodeResultKey]struct{}, len(episodes))
		seasonNumbers := make([]int32, 0, len(episodes))
		episodeNumbers := make([]int32, 0, len(episodes))
		hasDuplicateEpisode := false
		episodeNumbersValid := true
		for _, ep := range episodes {
			key := episodeResultKey{seasonNumber: ep.SeasonNumber, episodeNumber: ep.EpisodeNumber}
			if _, ok := seenEpisodeKeys[key]; ok {
				hasDuplicateEpisode = true
				continue
			}
			seenEpisodeKeys[key] = struct{}{}
			if !catalog.FitsPostgresInteger(ep.SeasonNumber) || !catalog.FitsPostgresInteger(ep.EpisodeNumber) {
				episodeNumbersValid = false
				continue
			}
			seasonNumbers = append(seasonNumbers, int32(ep.SeasonNumber))
			episodeNumbers = append(episodeNumbers, int32(ep.EpisodeNumber))
		}

		existingEpisodes := make(map[episodeResultKey]*models.Episode)
		episodesPrefetched := false
		if !hasDuplicateEpisode && episodeNumbersValid {
			storedEpisodes, err := s.episodeRepo.ListBySeriesAndNumbers(ctx, seriesID, seasonNumbers, episodeNumbers)
			if err != nil {
				slog.WarnContext(ctx, "metadata: failed to prefetch episodes before bulk upsert", "component", "metadata",
					"series_id", seriesID, "error", err)
			} else {
				episodesPrefetched = true
				for _, storedEpisode := range storedEpisodes {
					if storedEpisode != nil {
						existingEpisodes[episodeResultKey{
							seasonNumber:  storedEpisode.SeasonNumber,
							episodeNumber: storedEpisode.EpisodeNumber,
						}] = storedEpisode
					}
				}
			}
		}
		loadEpisode := func(ep EpisodeResult, forcePointRead bool) (*models.Episode, error) {
			if episodesPrefetched && !forcePointRead {
				storedEpisode := existingEpisodes[episodeResultKey{
					seasonNumber:  ep.SeasonNumber,
					episodeNumber: ep.EpisodeNumber,
				}]
				if storedEpisode != nil {
					return storedEpisode, nil
				}
				return nil, catalog.ErrEpisodeNotFound
			}
			return s.episodeRepo.GetBySeriesAndNumber(ctx, seriesID, ep.SeasonNumber, ep.EpisodeNumber)
		}

		prepareEpisode := func(ep EpisodeResult, forcePointRead bool) (preparedEpisodeWrite, bool) {
			existingEpisode, err := loadEpisode(ep, forcePointRead)
			if err != nil && !errors.Is(err, catalog.ErrEpisodeNotFound) {
				slog.WarnContext(ctx, "metadata: failed to load episode before upsert", "component", "metadata",
					"series_id", seriesID, "season", ep.SeasonNumber, "episode", ep.EpisodeNumber, "error", err)
				return preparedEpisodeWrite{}, false
			}
			providerEpisode := ep
			providerEpisode.StillPath, providerEpisode.StillSourcePath = splitProviderImagePath(ep.StillPath)
			if existingEpisode != nil && isCanonicalWrite {
				mergedEpisode := episodeResultFromModel(existingEpisode)
				MergeEpisodeResult(&providerEpisode, &mergedEpisode, mergeMode)
				// preserveCachedArtwork sees the raw provider path so a local
				// file:// source still routes into *_source_path here.
				nextPath, nextThumbhash, nextSourcePath := preserveCachedArtwork(
					episodeArtwork.providerPath(ImageStill, ep.StillPath, existingEpisode.StillSourcePath),
					ep.StillThumbhash,
					existingEpisode.StillPath,
					existingEpisode.StillSourcePath,
					existingEpisode.StillThumbhash,
					s.autoCacheImages.Load(),
				)
				if imagesLocked && existingEpisode.StillPath != "" {
					nextPath = existingEpisode.StillPath
					nextThumbhash = existingEpisode.StillThumbhash
					nextSourcePath = existingEpisode.StillSourcePath
				}
				mergedEpisode.StillPath = nextPath
				mergedEpisode.StillThumbhash = nextThumbhash
				mergedEpisode.StillSourcePath = nextSourcePath
				providerEpisode = mergedEpisode
			}
			if !imagesLocked {
				// As for season posters, ep.StillPath stands in when retention
				// kept a local source.
				providerEpisode.StillPath = localArtworkFallbackPath(providerEpisode.StillPath, providerEpisode.StillSourcePath, firstNonEmpty(ep.stillFallback, ep.StillPath))
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
					slog.WarnContext(ctx, "metadata: failed to generate episode id", "component", "metadata",
						"series_id", seriesID, "season", ep.SeasonNumber,
						"episode", ep.EpisodeNumber, "error", genErr)
					return preparedEpisodeWrite{}, false
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
			if dbEp.Title == "" {
				dbEp.Title = fallbackEpisodeTitle(dbEp.EpisodeNumber)
			}
			return preparedEpisodeWrite{model: dbEp, provider: providerEpisode}, true
		}

		finishEpisodeSequential := func(write preparedEpisodeWrite) {
			addEpisodeImageJob(write.model)
			if !isCanonicalWrite && s.episodeLocalizationRepo != nil {
				existingLoc, locErr := s.episodeLocalizationRepo.Get(ctx, write.model.ContentID, language)
				if locErr != nil {
					slog.WarnContext(ctx, "metadata: failed to load episode localization", "component", "metadata",
						"series_id", seriesID, "season", write.model.SeasonNumber,
						"episode", write.model.EpisodeNumber, "error", locErr)
				}
				if err := s.episodeLocalizationRepo.Upsert(ctx, buildEpisodeLocalizationRecord(
					existingLoc,
					write.model.ContentID,
					language,
					write.provider,
					mergeMode,
				)); err != nil {
					slog.WarnContext(ctx, "metadata: failed to upsert episode localization", "component", "metadata",
						"series_id", seriesID, "season", write.model.SeasonNumber,
						"episode", write.model.EpisodeNumber, "error", err)
				}
			}
		}
		upsertEpisodeModel := func(write preparedEpisodeWrite) bool {
			if err := s.episodeRepo.Upsert(ctx, write.model); err != nil {
				slog.WarnContext(ctx, "metadata: failed to upsert episode", "component", "metadata",
					"series_id", seriesID, "season", write.model.SeasonNumber,
					"episode", write.model.EpisodeNumber, "error", err)
				return false
			}
			return true
		}
		finishEpisodes := func(writes []preparedEpisodeWrite) {
			if !isCanonicalWrite && s.episodeLocalizationRepo != nil && len(writes) > 0 {
				episodeContentIDs := make([]string, len(writes))
				for i, write := range writes {
					episodeContentIDs[i] = write.model.ContentID
				}
				existingLocalizations, err := s.episodeLocalizationRepo.GetByEpisodeIDs(ctx, episodeContentIDs, language)
				if err != nil {
					slog.WarnContext(ctx, "metadata: failed to batch load episode localizations; falling back to point reads",
						"component", "metadata", "series_id", seriesID, "count", len(writes), "error", err)
					existingLocalizations = make(map[string]*models.EpisodeLocalization, len(writes))
					for _, write := range writes {
						existingLoc, locErr := s.episodeLocalizationRepo.Get(ctx, write.model.ContentID, language)
						if locErr != nil {
							slog.WarnContext(ctx, "metadata: failed to load episode localization", "component", "metadata",
								"series_id", seriesID, "season", write.model.SeasonNumber,
								"episode", write.model.EpisodeNumber, "error", locErr)
							continue
						}
						existingLocalizations[write.model.ContentID] = existingLoc
					}
				}

				localizations := make([]*models.EpisodeLocalization, len(writes))
				for i, write := range writes {
					localizations[i] = buildEpisodeLocalizationRecord(
						existingLocalizations[write.model.ContentID],
						write.model.ContentID,
						language,
						write.provider,
						mergeMode,
					)
				}
				localizationIndexes := make(map[*models.EpisodeLocalization]int, len(localizations))
				for i, loc := range localizations {
					localizationIndexes[loc] = i
				}
				_ = bulkUpsertWithFallback(localizations,
					func(items []*models.EpisodeLocalization) error {
						return s.episodeLocalizationRepo.BulkUpsert(ctx, items)
					},
					func(err error) {
						slog.WarnContext(ctx, "metadata: failed to bulk upsert episode localizations; falling back to single-row writes",
							"component", "metadata", "series_id", seriesID, "count", len(localizations), "error", err)
					},
					func(loc *models.EpisodeLocalization) error {
						return s.episodeLocalizationRepo.Upsert(ctx, loc)
					},
					func(loc *models.EpisodeLocalization, err error) {
						i := localizationIndexes[loc]
						slog.WarnContext(ctx, "metadata: failed to upsert episode localization", "component", "metadata",
							"series_id", seriesID, "season", writes[i].model.SeasonNumber,
							"episode", writes[i].model.EpisodeNumber, "error", err)
					},
				)
			}
			for _, write := range writes {
				addEpisodeImageJob(write.model)
			}
		}

		if hasDuplicateEpisode {
			for _, ep := range episodes {
				if write, ok := prepareEpisode(ep, true); ok {
					if upsertEpisodeModel(write) {
						finishEpisodeSequential(write)
					}
				}
			}
		} else {
			writes := make([]preparedEpisodeWrite, 0, len(episodes))
			modelsToPersist := make([]*models.Episode, 0, len(episodes))
			freshEpisodeIDs := make([]map[string]string, 0, len(episodes))
			for _, ep := range episodes {
				if write, ok := prepareEpisode(ep, false); ok {
					writes = append(writes, write)
					modelsToPersist = append(modelsToPersist, write.model)
					freshEpisodeIDs = append(freshEpisodeIDs, ep.ProviderIDs)
				}
			}
			releaseInheritedEpisodeProviderIDs(ctx, seriesID, modelsToPersist, freshEpisodeIDs)
			if len(modelsToPersist) > 0 {
				successfulWrites := bulkUpsertWithFallback(writes,
					func([]preparedEpisodeWrite) error {
						return s.episodeRepo.BulkUpsert(ctx, seriesID, modelsToPersist)
					},
					func(err error) {
						slog.WarnContext(ctx, "metadata: failed to bulk upsert episodes; falling back to single-row writes",
							"component", "metadata", "series_id", seriesID, "count", len(modelsToPersist), "error", err)
					},
					func(write preparedEpisodeWrite) error {
						if upsertEpisodeModel(write) {
							return nil
						}
						return errors.New("episode upsert failed")
					},
					func(preparedEpisodeWrite, error) {},
				)
				finishEpisodes(successfulWrites)
			}
		}
	}

	s.enqueueSeriesChildImages(ctx, seriesID, imageJobs)
	return true
}

// uniqueEpisodeProviderIDs are the episode provider ID columns that the
// episodes table keeps unique.
var uniqueEpisodeProviderIDs = []struct {
	provider string
	field    func(*models.Episode) *string
}{
	{"imdb", func(ep *models.Episode) *string { return &ep.ImdbID }},
	{"tmdb", func(ep *models.Episode) *string { return &ep.TmdbID }},
	{"tvdb", func(ep *models.Episode) *string { return &ep.TvdbID }},
}

// releaseInheritedEpisodeProviderIDs keeps each provider ID on one episode of
// a write batch; freshIDs holds each episode's provider result IDs. The
// canonical merge keeps an episode's stored IDs, so after a provider moves an
// ID to another episode number, as when it renumbers specials, both episodes
// carry it. The repository then rejects the whole bulk write, and the
// single-row fallback that follows leaves the ID on whichever row it writes
// last. An episode that only inherited the ID gives it up to the episode whose
// provider result carries it, and takes its own provider result's ID instead
// when no other episode in the batch holds that one. When several provider
// results carry the ID, the first in batch order keeps it.
func releaseInheritedEpisodeProviderIDs(ctx context.Context, seriesID string, episodes []*models.Episode, freshIDs []map[string]string) {
	for _, column := range uniqueEpisodeProviderIDs {
		holders := make(map[string][]int)
		var ids []string
		for i, ep := range episodes {
			id := *column.field(ep)
			if id == "" {
				continue
			}
			if len(holders[id]) == 0 {
				ids = append(ids, id)
			}
			holders[id] = append(holders[id], i)
		}
		for _, id := range ids {
			indexes := holders[id]
			if len(indexes) < 2 {
				continue
			}
			keep := indexes[0]
			for _, i := range indexes {
				if freshIDs[i][column.provider] == id {
					keep = i
					break
				}
			}
			kept := episodes[keep]
			for _, i := range indexes {
				if i == keep {
					continue
				}
				released := episodes[i]
				fresh := freshIDs[i][column.provider]
				if fresh == id {
					*column.field(released) = ""
					slog.DebugContext(ctx, "metadata: provider gave one episode ID to several episodes", "component", "metadata",
						"series_id", seriesID,
						"provider", column.provider,
						"provider_id", id,
						"kept_season", kept.SeasonNumber,
						"kept_episode", kept.EpisodeNumber,
						"released_season", released.SeasonNumber,
						"released_episode", released.EpisodeNumber)
					continue
				}
				replacement := ""
				if fresh != "" && len(holders[fresh]) == 0 {
					replacement = fresh
					holders[fresh] = []int{i}
				}
				*column.field(released) = replacement
				slog.InfoContext(ctx, "metadata: moved an episode provider ID to the episode its provider now gives it", "component", "metadata",
					"series_id", seriesID,
					"provider", column.provider,
					"provider_id", id,
					"kept_season", kept.SeasonNumber,
					"kept_episode", kept.EpisodeNumber,
					"released_season", released.SeasonNumber,
					"released_episode", released.EpisodeNumber,
					"released_new_id", replacement)
			}
		}
	}
}

// syncSeriesEpisodeState relinks a series' files to its episodes and re-syncs
// the series' episode metadata debt. Both passes walk the whole series.
func (s *MetadataService) syncSeriesEpisodeState(ctx context.Context, seriesID string) {
	if err := s.ensureSeriesEpisodeLinks(ctx, seriesID); err != nil {
		slog.WarnContext(ctx, "metadata: failed to ensure series episode links", "component", "metadata",
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
	rootsByFolder, err := s.configuredNamingRootsForFiles(ctx, files)
	if err != nil {
		return err
	}

	seasonIDs := make(map[int]string)
	for _, file := range files {
		if file == nil {
			continue
		}
		seasonNum, episodeNum, ok := fallbackEpisodeNumbers(file, rootsByFolder[file.MediaFolderID]...)
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
	rootsByFolder, err := s.configuredNamingRootsForFiles(ctx, files)
	if err != nil {
		return err
	}

	needsSynthesis := false
	for _, file := range files {
		if file == nil {
			continue
		}
		seasonNum, episodeNum, ok := fallbackEpisodeNumbers(file, rootsByFolder[file.MediaFolderID]...)
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

	slog.InfoContext(ctx, "metadata: entering fallback episode synthesis for incomplete series", "component", "metadata", "series_id", seriesID)
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
	rootsByFolder, err := s.configuredNamingRootsForFiles(ctx, files)
	if err != nil {
		return err
	}

	hints := make(map[int]episodeLinkHint, len(files))
	airDateSet := make(map[string]struct{})
	needsUnseasonedLookup := false
	for _, file := range files {
		if file == nil {
			continue
		}
		hint := parseEpisodeLinkHint(file, rootsByFolder[file.MediaFolderID]...)
		if !hint.ok {
			continue
		}
		hints[file.ID] = hint
		if hint.airDate != "" {
			airDateSet[hint.airDate] = struct{}{}
		} else if !hint.seasonKnown {
			needsUnseasonedLookup = true
		}
	}

	var unseasonedEpisodes unseasonedEpisodeIndex
	if needsUnseasonedLookup {
		seriesEpisodes, err := s.episodeRepo.ListBySeries(ctx, seriesID)
		if err != nil {
			return fmt.Errorf("loading episodes for unseasoned file matching: %w", err)
		}
		unseasonedEpisodes = newUnseasonedEpisodeIndex(seriesEpisodes)
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
				slog.WarnContext(ctx, "metadata: failed to load series item for air-date episode preference", "component", "metadata",
					"series_id", seriesID,
					"error", itemErr)
			}
		}
	}

	// Ambiguous dates are reported once per series on each pass: the same
	// unlinked files come back every pass until their metadata or names change.
	ambiguousFiles := 0
	ambiguousAirDates := make(map[string]struct{})
	var exampleAmbiguous *models.MediaFile
	exampleAmbiguousAirDate := ""
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
			selected, ok := selectAirDateEpisodeCandidate(candidates, seriesItem, hint)
			if !ok {
				if len(candidates) > 1 {
					ambiguousFiles++
					ambiguousAirDates[hint.airDate] = struct{}{}
					if exampleAmbiguous == nil {
						exampleAmbiguous, exampleAmbiguousAirDate = file, hint.airDate
					}
					slog.DebugContext(ctx, "metadata: skipped ambiguous air-date episode link", "component", "metadata",
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
		} else if !hint.seasonKnown {
			selected, ok := unseasonedEpisodes.resolve(hint.episodeNum, extractEpisodeMatchTitle(file.FilePath, rootsByFolder[file.MediaFolderID]...))
			if !ok {
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
				slog.WarnContext(ctx, "metadata: failed to resolve episode for file", "component", "metadata",
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
			slog.WarnContext(ctx, "metadata: failed to link file to episode", "component", "metadata",
				"series_id", seriesID,
				"file_id", file.ID,
				"file_path", file.FilePath,
				"episode_id", episode.ContentID,
				"error", err)
		}
	}
	if ambiguousFiles > 0 {
		slog.WarnContext(ctx, "metadata: skipped ambiguous air-date episode links", "component", "metadata",
			"series_id", seriesID,
			"files", ambiguousFiles,
			"air_dates", len(ambiguousAirDates),
			"example_file_id", exampleAmbiguous.ID,
			"example_air_date", exampleAmbiguousAirDate)
	}
	return nil
}

// selectAirDateEpisodeCandidate picks the episode a by-date file belongs to
// among the series' episodes that aired that day. Each rule narrows the
// candidates only when it keeps at least one, so a rule that matches none of
// them never rules out the rest: the series' own provider numbering, the
// episode title in the filename, the file's season folder, and finally
// regular episodes over same-day specials.
func selectAirDateEpisodeCandidate(candidates []*models.Episode, seriesItem *models.MediaItem, hint episodeLinkHint) (*models.Episode, bool) {
	remaining := make([]*models.Episode, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate != nil {
			remaining = append(remaining, candidate)
		}
	}
	// narrow reports false when the rule matched none of several candidates.
	narrow := func(keep func(*models.Episode) bool) bool {
		if len(remaining) < 2 {
			return true
		}
		kept := make([]*models.Episode, 0, len(remaining))
		for _, candidate := range remaining {
			if keep(candidate) {
				kept = append(kept, candidate)
			}
		}
		if len(kept) == 0 {
			return false
		}
		remaining = kept
		return true
	}

	for _, provider := range preferredEpisodeProviders(seriesItem) {
		if len(remaining) < 2 {
			break
		}
		if filtered := filterEpisodesByProviderID(remaining, provider); len(filtered) > 0 {
			remaining = filtered
			break
		}
	}
	titleMatched := true
	if airDateTitleIsEvidence(hint.title) {
		title := normalizeTitleForScoring(hint.title)
		titleMatched = narrow(func(candidate *models.Episode) bool { return normalizeTitleForScoring(candidate.Title) == title })
	}
	if hint.seasonKnown {
		narrow(func(candidate *models.Episode) bool { return candidate.SeasonNumber == hint.seasonNum })
	}
	// A titled file that matches no candidate may be a special its provider
	// titles differently, so only then is a regular episode not the default.
	if titleMatched {
		narrow(func(candidate *models.Episode) bool { return candidate.SeasonNumber > 0 })
	}

	if len(remaining) != 1 {
		return nil, false
	}
	return remaining[0], true
}

// unknownEpisodeTitle is the normalized placeholder renamers write for an
// episode without a title.
const unknownEpisodeTitle = "unknown"

// airDateTitleIsEvidence reports whether a by-date filename's title can pick
// between same-day episodes. Generic titles and the Unknown placeholder cannot.
func airDateTitleIsEvidence(title string) bool {
	// Check the normalized form, which is what the title match compares, so
	// spellings such as "Episode-83" or "(TBA)" count as generic too.
	normalized := normalizeTitleForScoring(title)
	return !isGenericEpisodeMatchTitle(normalized) && normalized != unknownEpisodeTitle
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
	seasonNum   int
	seasonKnown bool
	episodeNum  int
	airDate     string
	// title is the episode title a by-date filename carries after its date.
	title string
	ok    bool
}

func (s *MetadataService) updateEpisodeMetadataState(ctx context.Context, seriesID string, incomplete bool, lastCheckedAt *time.Time) {
	item, err := s.itemRepo.GetByID(ctx, seriesID)
	if err != nil {
		slog.WarnContext(ctx, "metadata: failed to load series item for episode metadata state", "component", "metadata",
			"series_id", seriesID, "error", err)
		return
	}

	item.EpisodeMetadataIncomplete = incomplete
	item.EpisodeMetadataLastCheckedAt = lastCheckedAt
	if err := s.itemRepo.Upsert(ctx, item); err != nil {
		slog.WarnContext(ctx, "metadata: failed to update episode metadata state", "component", "metadata",
			"series_id", seriesID, "error", err)
	}
}

// refreshSeriesEpisodeMetadataState re-syncs the series' episode refresh debt
// and incomplete flag. It reports false when it could not read the series'
// debt or episodes; failures on single rows are logged and do not count.
func (s *MetadataService) refreshSeriesEpisodeMetadataState(ctx context.Context, seriesID string, now time.Time) bool {
	if s == nil || s.episodeRepo == nil {
		return true
	}

	// Observe debt versions before reading completeness so a concurrent refresh's
	// new or updated debt cannot be cleared using a stale episode snapshot.
	var debtVersions map[string]string
	if s.refreshDebtRepo != nil {
		var err error
		debtVersions, err = s.refreshDebtRepo.SnapshotEpisodeDebts(ctx, seriesID)
		if err != nil {
			slog.WarnContext(ctx, "metadata: failed to snapshot episode refresh debt", "component", "metadata",
				"series_id", seriesID, "error", err)
			return false
		}
	}

	episodes, err := s.episodeRepo.ListBySeries(ctx, seriesID)
	if err != nil {
		slog.WarnContext(ctx, "metadata: failed to list series episodes for completeness check", "component", "metadata",
			"series_id", seriesID, "error", err)
		return false
	}

	// Episode targets that failed during a scheduled refresh batch keep the
	// failure their own refresh recorded while they still have debt; a later
	// target in the batch that completed one lets the sweep clear its row.
	failedEpisodes := failedEpisodeDebtFromContext(ctx)
	keptActionable := false
	var completeEpisodeIDs []string
	var actionableEpisodes []*models.Episode
	for _, episode := range episodes {
		if episode != nil {
			if _, failed := failedEpisodes[strings.TrimSpace(episode.ContentID)]; failed &&
				EpisodeHasActionableMetadataDebt(episode, now) {
				keptActionable = true
				continue
			}
		}
		if !EpisodeHasActionableMetadataDebt(episode, now) {
			if s.refreshDebtRepo != nil && episode != nil {
				if id := strings.TrimSpace(episode.ContentID); id != "" {
					completeEpisodeIDs = append(completeEpisodeIDs, id)
				}
			}
			continue
		}
		actionableEpisodes = append(actionableEpisodes, episode)
	}
	if len(completeEpisodeIDs) > 0 {
		if err := s.refreshDebtRepo.DeleteEpisodeDebts(ctx, completeEpisodeIDs, debtVersions); err != nil {
			slog.WarnContext(ctx, "metadata: failed to clear complete episode refresh debt", "component", "metadata",
				"series_id", seriesID, "episode_count", len(completeEpisodeIDs), "error", err)
		}
	}

	for _, episode := range actionableEpisodes {
		if err := s.syncVisibleEpisodeRefreshDebt(ctx, episode, now); err != nil {
			slog.WarnContext(ctx, "metadata: failed to sync episode refresh debt", "component", "metadata",
				"series_id", seriesID,
				"episode_id", episode.ContentID,
				"error", err)
		}
	}

	s.updateEpisodeMetadataState(ctx, seriesID, len(actionableEpisodes) > 0 || keptActionable, new(now))
	return true
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
	// No terminal notice here: this sweep re-syncs every incomplete episode in
	// the series, and a success leaves attempt_count unchanged, so a row at the
	// terminal count would be reported again on every sweep. The episode's own
	// target sync reports the claim that took it there.
	return s.refreshDebtRepo.MarkTargetSuccess(
		ctx,
		RefreshTargetEpisode,
		episode.ContentID,
		effectiveRefreshDebtPriority(reasonMask, attemptCount),
		reasonMask,
		nextRefreshAtForEpisodeDebt(reasonMask, attemptCount, now.UTC(), episode),
	)
}

func fallbackEpisodeNumbers(file *models.MediaFile, libraryRoots ...string) (seasonNum int, episodeNum int, ok bool) {
	hint := parseEpisodeLinkHint(file, libraryRoots...)
	if !hint.ok || hint.airDate != "" || !hint.seasonKnown {
		return 0, 0, false
	}
	return hint.seasonNum, hint.episodeNum, true
}

func parseEpisodeLinkHint(file *models.MediaFile, libraryRoots ...string) episodeLinkHint {
	if file == nil {
		return episodeLinkHint{}
	}
	if file.SeasonNumber != 0 && file.EpisodeNumber != 0 {
		return episodeLinkHint{seasonNum: file.SeasonNumber, seasonKnown: true, episodeNum: file.EpisodeNumber, ok: true}
	}

	fnh := naming.ParseFilename(file.FilePath, "series", libraryRoots...)
	if fnh == nil {
		return episodeLinkHint{}
	}
	if fnh.EpisodeNum != 0 {
		return episodeLinkHint{seasonNum: fnh.SeasonNum, seasonKnown: fnh.SeasonKnown, episodeNum: fnh.EpisodeNum, ok: true}
	}
	if fnh.AirDate != "" {
		// A by-date name has no episode number, but its season folder and
		// title still tell same-day episodes apart.
		return episodeLinkHint{
			seasonNum:   fnh.SeasonNum,
			seasonKnown: fnh.SeasonKnown,
			airDate:     fnh.AirDate,
			title:       extractAirDateMatchTitle(file.FilePath),
			ok:          true,
		}
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

// findExistingByProviderIDs returns the item that owns one of ids and is the
// same title, or nil. An item that shares one ID but contradicts ids on
// identity (identityProviderIDsContradict) is a different title: provider
// cross-ID mappings are community-edited and can list one TVDB or IMDb ID on
// two neighboring titles, and merging on that would fold a tagged movie into
// another (issue #2263).
func (s *MetadataService) findExistingByProviderIDs(
	ctx context.Context,
	ids map[string]string,
	itemType string,
	excludeContentID string,
) (*models.MediaItem, error) {
	item, _, err := s.findProviderIDOwner(ctx, ids, itemType, excludeContentID)
	return item, err
}

// findProviderIDOwner is findExistingByProviderIDs that also reports the keys
// of ids whose owner it refused as a different title, with that owner.
func (s *MetadataService) findProviderIDOwner(
	ctx context.Context,
	ids map[string]string,
	itemType string,
	excludeContentID string,
) (*models.MediaItem, map[string]*models.MediaItem, error) {
	if s == nil || s.itemRepo == nil {
		return nil, nil, nil
	}
	contested := make(map[string]*models.MediaItem)
	owner, err := s.findDurableProviderIDOwner(ctx, ids, itemType, excludeContentID, contested)
	if err != nil || owner != nil {
		return owner, contested, err
	}

	tmdbID := ids["tmdb"]
	imdbID := ids["imdb"]
	tvdbID := ids["tvdb"]
	if tmdbID != "" || imdbID != "" || tvdbID != "" {
		if item, err := s.itemRepo.GetByExternalID(ctx, tmdbID, imdbID, tvdbID, itemType); err == nil {
			if excludeContentID != "" && item.ContentID == excludeContentID {
				return nil, contested, nil
			}
			if identityProviderIDsContradict(itemType, ids, itemIdentityProviderIDs(item, nil)) {
				return nil, contested, nil
			}
			return item, contested, nil
		}
	}
	return nil, contested, nil
}

// findDurableProviderIDOwner returns the first owner of one of ids in the
// provider-ID repository that is the same title, looking up one owner per ID
// in the repository's priority order so a refused owner of one ID does not
// hide the right owner of another. It records each refused key in contested.
func (s *MetadataService) findDurableProviderIDOwner(
	ctx context.Context,
	ids map[string]string,
	itemType string,
	excludeContentID string,
	contested map[string]*models.MediaItem,
) (*models.MediaItem, error) {
	if s.providerIDRepo == nil {
		return nil, nil
	}
	refused := make(map[string]*models.MediaItem)
	for _, key := range providerIDLookupKeys(ids) {
		contentID, err := s.providerIDRepo.FindContentIDByProviderIDs(ctx, map[string]string{key: ids[key]}, itemType, excludeContentID)
		if err != nil {
			return nil, err
		}
		if contentID == "" {
			continue
		}
		if owner := refused[contentID]; owner != nil {
			contested[key] = owner
			continue
		}
		item, err := s.itemRepo.GetByID(ctx, contentID)
		if err != nil {
			return nil, err
		}
		same, err := s.sameTitleAs(ctx, item, ids, itemType)
		if err != nil {
			return nil, err
		}
		if same {
			return item, nil
		}
		refused[contentID] = item
		contested[key] = item
	}
	return nil, nil
}

// sameTitleAs reports whether item, with its stored provider IDs, agrees with
// ids on identity (identityProviderIDsContradict).
func (s *MetadataService) sameTitleAs(ctx context.Context, item *models.MediaItem, ids map[string]string, itemType string) (bool, error) {
	stored, err := s.loadDurableProviderIDs(ctx, item.ContentID)
	if err != nil {
		return false, err
	}
	return !identityProviderIDsContradict(itemType, ids, itemIdentityProviderIDs(item, stored)), nil
}

// providerIDLookupKeys returns the durable keys of ids with a value, TMDB,
// TVDB and IMDb first, as the provider-ID repository ranks them.
func providerIDLookupKeys(ids map[string]string) []string {
	keys := make([]string, 0, len(ids))
	for key, value := range ids {
		if strings.TrimSpace(value) == "" || isEphemeralProviderIDKey(key) {
			continue
		}
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b string) int {
		rankA, rankB := slices.Index(trustedSearchIDKeys, a), slices.Index(trustedSearchIDKeys, b)
		switch {
		case rankA >= 0 && rankB >= 0:
			return rankA - rankB
		case rankA >= 0:
			return -1
		case rankB >= 0:
			return 1
		}
		return strings.Compare(a, b)
	})
	return keys
}

// itemIdentityProviderIDs returns an item's identity IDs: its stored provider
// ID rows, with its TMDB, TVDB and IMDb columns filling any row it lacks.
func itemIdentityProviderIDs(item *models.MediaItem, stored map[string]string) map[string]string {
	out := storedIdentityProviderIDs(item)
	if out == nil {
		out = make(map[string]string, len(trustedSearchIDKeys))
	}
	for _, key := range trustedSearchIDKeys {
		if value := strings.TrimSpace(stored[key]); value != "" {
			out[key] = value
		}
	}
	return out
}

// identityProviderIDsContradict reports whether two ID sets of itemType name
// different titles: they disagree on the first ID both carry, in the order
// the type's content IDs anchor (contentid.AnchorPrecedence). For a movie
// that is TMDB, IMDb, then TVDB, whose movie mappings are the cross-IDs
// providers most often get wrong; for a series it is TVDB, TMDB, then IMDb.
// Later IDs do not decide, so a movie whose TVDB mapping changed since it was
// matched still matches on its TMDB ID, and a series whose TMDB mapping
// changed still matches on its TVDB ID. A stored TMDB ID in TMDB's "id-slug"
// URL form is the same ID as its number.
func identityProviderIDsContradict(itemType string, a, b map[string]string) bool {
	for _, key := range contentid.AnchorPrecedence(itemType) {
		left, right := strings.TrimSpace(a[key]), strings.TrimSpace(b[key])
		if key == "tmdb" {
			left, right = catalog.NormalizeTMDBID(left), catalog.NormalizeTMDBID(right)
		}
		if left == "" || right == "" {
			continue
		}
		return !strings.EqualFold(left, right)
	}
	return false
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

// groupOverrideProviderIDs returns the provider IDs an operator's group
// override forces, or nil when it forces none.
func groupOverrideProviderIDs(override *models.MediaGroupOverride) *naming.FolderIDHints {
	if override == nil {
		return nil
	}
	return mergeFolderIDHints(nil, &naming.FolderIDHints{
		TmdbID: override.ForcedTmdbID,
		ImdbID: override.ForcedImdbID,
		TvdbID: override.ForcedTvdbID,
	})
}

// applyGroupOverride forces an operator's group override onto a skeleton.
// The override settles the group's identity, so an ambiguous skeleton becomes
// matchable again; any other status is the item's own and is kept.
func applyGroupOverride(res *skeletonResult, override *models.MediaGroupOverride) {
	if res == nil || override == nil {
		return
	}
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
	if res.ItemStatus == "ambiguous" { //nolint:goconst // Item statuses are literals throughout this package.
		res.ItemStatus = "pending" //nolint:goconst // queueStatePending is a test-local constant.
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

func trustedStructuredIDsForSkeleton(filePath, observedRootPath, contentRootPath string, libraryRoots ...string) *naming.FolderIDHints {
	folderIDs := naming.ParseStructuredFolderIDs(skeletonFolderAnchorName(observedRootPath, libraryRoots))
	if folderIDs == nil && contentRootPath != "" && contentRootPath != observedRootPath {
		folderIDs = naming.ParseStructuredFolderIDs(skeletonFolderAnchorName(contentRootPath, libraryRoots))
	}
	fileIDs := naming.ParseStructuredFolderIDs(strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath)))
	return mergeFolderIDHints(folderIDs, fileIDs)
}

// seriesContentIDForGroup returns the series item already linked to another
// file of the scanned group, preferring a confirmed match.
func (s *MetadataService) seriesContentIDForGroup(ctx context.Context, folderID, groupKeyVersion int, contentGroupKey string, excludeFileID int) (string, error) {
	files, err := s.fileRepo.ListByGroupKey(ctx, folderID, groupKeyVersion, contentGroupKey)
	if err != nil {
		return "", fmt.Errorf("loading series group files: %w", err)
	}
	provisional := ""
	checked := map[string]bool{}
	for _, member := range files {
		if member == nil || member.ID == excludeFileID || member.ContentID == "" || checked[member.ContentID] {
			continue
		}
		checked[member.ContentID] = true
		item, err := s.itemRepo.GetByID(ctx, member.ContentID)
		if err != nil {
			return "", fmt.Errorf("loading series group item: %w", err)
		}
		if item == nil || item.Type != matchContentTypeSeries {
			continue
		}
		if isConfirmedOwnershipStatus(item.Status) {
			return item.ContentID, nil
		}
		if provisional == "" {
			provisional = item.ContentID
		}
	}
	return provisional, nil
}

// A configured library folder contains many titles. Its own name cannot anchor
// a media item's provider identity, even when an older scan stored it as the
// file's observed or canonical content root.
func skeletonFolderAnchorName(rootPath string, libraryRoots []string) string {
	for _, libraryRoot := range libraryRoots {
		if strings.TrimSpace(libraryRoot) != "" && filepath.Clean(rootPath) == filepath.Clean(libraryRoot) {
			return ""
		}
	}
	return filepath.Base(rootPath)
}

const manualIdentityOverrideSource = "manual"

// scannedGroupStateResolved marks a scanner group whose members agree on identity.
const scannedGroupStateResolved = "resolved"

func scannedGroupIdentityChanged(group *models.ScannedMediaGroup, file *models.MediaFile, currentIDs *naming.FolderIDHints, libraryRoots ...string) bool {
	if group == nil || file == nil || strings.EqualFold(strings.TrimSpace(group.OverrideSource), manualIdentityOverrideSource) {
		return false
	}
	if file.BaseType != "" && group.InferredType != "" && !strings.EqualFold(file.BaseType, group.InferredType) {
		return true
	}
	if currentIDs != nil {
		_, conflicts := providerIDMergeEvidence(
			map[string]string{"tmdb": group.TmdbID, "imdb": group.ImdbID, "tvdb": group.TvdbID},
			map[string]string{"tmdb": currentIDs.TmdbID, "imdb": currentIDs.ImdbID, "tvdb": currentIDs.TvdbID},
		)
		return len(conflicts) > 0
	}
	// Some callers rely entirely on the scanned group and supply no parsed
	// identity. Punctuation differences are not identity changes.
	if strings.TrimSpace(file.BaseTitle) == "" || strings.TrimSpace(group.BaseTitle) == "" {
		return false
	}
	matches := func(title string, year int, itemType string) bool {
		return normalizeTitleForScoring(title) == normalizeTitleForScoring(group.BaseTitle) && year == group.BaseYear &&
			(itemType == "" || group.InferredType == "" || strings.EqualFold(itemType, group.InferredType))
	}
	if matches(file.BaseTitle, file.BaseYear, file.BaseType) {
		return false
	}
	// Per-file names can contain edition/alias text below a shared movie root.
	// Apply the scanner's existing group rules to that root before declaring its
	// identity stale; a rescan would preserve those valid presentation variants.
	root := firstNonEmpty(file.CanonicalRootPath, file.ObservedRootPath, group.SampleObservedRootPath)
	pathContext := naming.ResolvePathContext(file.FilePath, file.BaseType, libraryRoots...)
	current := naming.InferGroupIdentity(file.FilePath, file.BaseType, naming.RootAssignment{
		RootPath: root, InferredType: file.BaseType, Title: file.BaseTitle, Year: file.BaseYear, LibraryRootPath: pathContext.LibraryRootPath,
	})
	if current.ContentGroupKey == group.ContentGroupKey {
		return false
	}
	return !matches(current.BaseTitle, current.BaseYear, current.BaseType)
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
func (s *MetadataService) createOrFindSkeleton(ctx context.Context, file *models.MediaFile, folderID int, libraryRoots ...string) (*skeletonResult, error) {
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
	var scannedIdentity *models.ScannedMediaGroup
	if s.scannedGroupRepo != nil && contentGroupKey != "" {
		scannedGroup, err := s.scannedGroupRepo.Get(ctx, folderID, groupKeyVersion, contentGroupKey)
		if err != nil {
			slog.WarnContext(ctx, "metadata: scanned group lookup failed", "component", "metadata",
				"folder_id", folderID,
				"group_key_version", groupKeyVersion,
				"content_group_key", contentGroupKey,
				"error", err,
			)
		} else if scannedGroup != nil {
			scannedIdentity = scannedGroup
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
	var groupOverride *models.MediaGroupOverride
	if s.groupOverrideRepo != nil && contentGroupKey != "" {
		override, err := s.groupOverrideRepo.Get(ctx, folderID, groupKeyVersion, contentGroupKey)
		if err != nil {
			slog.WarnContext(ctx, "metadata: group override lookup failed", "component", "metadata",
				"folder_id", folderID,
				"group_key_version", groupKeyVersion,
				"content_group_key", contentGroupKey,
				"error", err,
			)
		} else if override != nil {
			groupOverride = override
			applyGroupOverride(res, override)
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
	trustedIDs := trustedStructuredIDsForSkeleton(file.FilePath, observedRootPath, contentRootPath, libraryRoots...)
	if trustedIDs != nil {
		applyFolderIDHints(res, trustedIDs)
		res.ItemStatus = "pending"
	}

	// Parse observed location names for external IDs before falling back to the
	// legacy canonical root path. This preserves existing heuristic behavior for
	// roots without explicit structured tags.
	folderIDs := naming.ParseFolderIDs(skeletonFolderAnchorName(observedRootPath, libraryRoots))
	if folderIDs == nil && contentRootPath != "" && contentRootPath != observedRootPath {
		folderIDs = naming.ParseFolderIDs(skeletonFolderAnchorName(contentRootPath, libraryRoots))
	}
	// A heuristic folder ID is a guess, so an operator's forced ID for the same
	// provider beats it, as it does when the match worker reuses a linked item.
	if folderIDs != nil {
		folderIDs = mergeFolderIDHints(folderIDs, groupOverrideProviderIDs(groupOverride))
	}

	effectiveExternalIDs := folderIDs
	if trustedIDs != nil {
		effectiveExternalIDs = trustedIDs
	}
	// An admin split this root into an unmatched item. Its folder still carries
	// the source's provider tag and the source may still hold the root or group
	// claim, so resolve it only through the item its files already link to.
	splitPinned, splitPinnedRoot, err := s.splitPinForFile(ctx, folderID, observedRootPath, file.FilePath)
	if err != nil {
		return nil, err
	}
	if splitPinned {
		trustedIDs, folderIDs, effectiveExternalIDs = nil, nil, nil
		res.TmdbID, res.ImdbID, res.TvdbID = "", "", ""
	}
	// A queued file can outlive the parser that assigned its group. Matching
	// refreshed title/year hints under the old automatic group key would let a
	// subsequent claim relink unrelated files. Let a scan rebuild that grouping
	// before any catalog writes; operator overrides and explicit IDs establish
	// identity independently of how the filename currently parses.
	if groupOverride == nil && scannedGroupIdentityChanged(scannedIdentity, file, effectiveExternalIDs, libraryRoots...) {
		return nil, errors.New("filename identity changed since the last scan; rescan the library to update file grouping")
	}
	if effectiveExternalIDs != nil {
		if trustedIDs == nil {
			applyFolderIDHints(res, folderIDs)
		}
		// Clear any legacy skipped-root record since we now have provider IDs.
		if s != nil && s.skippedRootRepo != nil {
			if err := s.skippedRootRepo.Delete(ctx, folderID, contentRootPath); err != nil {
				slog.WarnContext(ctx, "metadata: failed to clear skipped root", "component", "metadata",
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
		naming.IsMisplacedSeriesFile(file.FilePath, libraryRoots...) {
		s.recordSkippedRoot(ctx, folderID, observedRootPath, skippedReasonSeriesInMovieLibrary, file.FilePath)
		res.ItemStatus = "skipped"
		return res, nil
	}
	if res.ItemStatus == "ambiguous" && !splitPinned {
		confirmedIDs, err := s.resolveMovieTitleAmbiguity(ctx, file, res, libraryRoots...)
		if err != nil {
			return nil, err
		}
		if confirmedIDs != nil {
			applyFolderIDHints(res, confirmedIDs)
			effectiveExternalIDs = confirmedIDs
			res.ItemStatus = "pending"
		}
	}
	if effectiveExternalIDs == nil && !splitPinned && !s.movieRootHasTaggedFile(ctx, folderID, observedRootPath, res.Type) {
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
	if contentGroupKey != "" && s.groupClaimRepo != nil && !splitPinned {
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
	if contentRootPath != "" && s.rootClaimRepo != nil && !splitPinned {
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
	// A root pinned whole by a split does the same for movies: its files all
	// link to the split target, and a new version there belongs with them.
	if res.Type == "series" || splitPinnedRoot {
		existingContentID, err := s.fileRepo.FindContentIDByObservedRootPath(ctx, folderID, observedRootPath, res.Type)
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

	// Dedup 3b: a file directly in a series library root is its own observed
	// root, so matching one show cannot relink its neighbors. Its episodes
	// still form one resolved scanner group; reuse the series item another
	// episode created instead of adding a provisional item per episode.
	flatSeriesGroup := res.Type == "series" && contentGroupKey != "" && filepath.Clean(observedRootPath) == filepath.Clean(file.FilePath) &&
		(groupOverride != nil || (scannedIdentity != nil && scannedIdentity.State == scannedGroupStateResolved))
	if flatSeriesGroup {
		existingContentID, err := s.seriesContentIDForGroup(ctx, folderID, groupKeyVersion, contentGroupKey, file.ID)
		if err != nil {
			return nil, err
		}
		if existingContentID != "" {
			if linkErr := s.fileRepo.UpdateContentID(ctx, file.ID, existingContentID); linkErr != nil {
				return nil, fmt.Errorf("linking file to existing group item: %w", linkErr)
			}
			if err := s.upsertLibraryMembership(ctx, existingContentID, folderID); err != nil {
				s.logLibraryMembershipError("upserting existing group item membership", existingContentID, folderID, err)
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
			// A wrong provider cross-ID can put one tagged ID on a different
			// title (issue #2263), so the item must also agree with the tags
			// on identity.
			tagged := map[string]string{"tmdb": effectiveExternalIDs.TmdbID, "imdb": effectiveExternalIDs.ImdbID, "tvdb": effectiveExternalIDs.TvdbID}
			same, sameErr := s.sameTitleAs(ctx, existing, tagged, res.Type)
			if sameErr != nil {
				return nil, fmt.Errorf("loading provider IDs of existing external-id item: %w", sameErr)
			}
			if !same {
				// The lookup can pass over a matched item at the tags' own
				// content ID; link to it rather than reset it to a skeleton.
				existing = nil
				anchored, deriveErr := deriveLogicalContentID(res.Type, contentid.ProviderIDs{Tmdb: res.TmdbID, Imdb: res.ImdbID, Tvdb: res.TvdbID}, "")
				if deriveErr == nil && contentid.IsProviderAnchored(anchored) {
					existing, _ = s.confirmedOwnershipItem(ctx, anchored)
				}
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
	if flatSeriesGroup {
		// The dedup lock is per process. Anchoring a flat show's provisional
		// item on its group lets episodes matched concurrently on other nodes
		// converge on one item instead of each minting its own.
		localAnchorPath = fmt.Sprintf("group:%d:%d:%s", folderID, groupKeyVersion, contentGroupKey)
	}
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

	if inserter, ok := s.itemRepo.(metadataItemInsertRepo); ok && flatSeriesGroup {
		// Another node may have created, and even matched, this group's item
		// after the group check above. Upsert would reset it to a skeleton, so
		// only the first creator writes it and later ones link to it.
		inserted, err := inserter.InsertIfAbsent(ctx, item)
		if err != nil {
			return nil, fmt.Errorf("creating skeleton item: %w", err)
		}
		if !inserted {
			if linkErr := s.fileRepo.UpdateContentID(ctx, file.ID, contentID); linkErr != nil {
				return nil, fmt.Errorf("linking file to existing group item: %w", linkErr)
			}
			if err := s.upsertLibraryMembership(ctx, contentID, folderID); err != nil {
				s.logLibraryMembershipError("upserting existing group item membership", contentID, folderID, err)
			}
			res.ContentID = contentID
			return res, nil
		}
	} else if err := s.itemRepo.Upsert(ctx, item); err != nil {
		return nil, fmt.Errorf("creating skeleton item: %w", err)
	}

	// Link file, claim root, and create library membership.
	if err := s.fileRepo.UpdateContentID(ctx, file.ID, contentID); err != nil {
		if cleanupErr := s.deleteCreatedSkeleton(ctx, contentID); cleanupErr != nil {
			slog.WarnContext(ctx, "metadata: failed to clean up unlinked skeleton", "component", "metadata",
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
			slog.WarnContext(ctx, "metadata: failed to clear file link after disabled skeleton folder", "component", "metadata",
				"content_id", contentID,
				"file_id", file.ID,
				"folder_id", folderID,
				"error", clearErr,
			)
		}
		if cleanupErr := s.deleteCreatedSkeleton(ctx, contentID); cleanupErr != nil {
			slog.WarnContext(ctx, "metadata: failed to clean up skeleton for disabled folder", "component", "metadata",
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
			slog.WarnContext(ctx, "metadata: failed to clear file link after skeleton membership failure", "component", "metadata",
				"content_id", contentID,
				"file_id", file.ID,
				"folder_id", folderID,
				"error", clearErr,
			)
		}
		if cleanupErr := s.deleteCreatedSkeleton(ctx, contentID); cleanupErr != nil {
			slog.WarnContext(ctx, "metadata: failed to clean up membershipless skeleton", "component", "metadata",
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

// movieRootHasTaggedFile reports whether another file of a movie root carries a
// provider tag in its own name. A scan counts such a root as tagged, so an
// untagged sibling version must not flag it again.
func (s *MetadataService) movieRootHasTaggedFile(ctx context.Context, folderID int, observedRootPath, contentType string) bool {
	if s == nil || s.fileRepo == nil || contentType != matchContentTypeMovie {
		return false
	}
	files, err := s.fileRepo.ListByObservedRootPath(ctx, folderID, observedRootPath)
	if err != nil {
		slog.WarnContext(ctx, "metadata: failed to list root files for skipped-root check", "component", "metadata",
			"folder_id", folderID,
			"root_path", observedRootPath,
			"error", err)
		return false
	}
	return naming.AnyFileNameHasProviderTag(files)
}

// recordSkippedRoot records the root of file for admin diagnostics in
// skipped_media_roots. Failures are logged and swallowed: diagnostics must
// never block skeleton creation.
func (s *MetadataService) recordSkippedRoot(ctx context.Context, folderID int, rootPath, reason, sampleFilePath string) {
	if s == nil || s.skippedRootRepo == nil {
		return
	}
	if err := s.skippedRootRepo.UpsertObservedFile(ctx, folderID, rootPath, reason, sampleFilePath); err != nil {
		slog.WarnContext(ctx, "metadata: failed to record skipped root", "component", "metadata",
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
	// A skeleton's content_id can be deterministic, so another node may have
	// linked the same item since this call created it. Delete it only while
	// nothing references it.
	if repo, ok := s.itemRepo.(metadataItemGuardedDeleteRepo); ok {
		_, err := repo.DeleteIfUnreferenced(ctx, contentID)
		return err
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
		slog.WarnContext(ctx, "metadata: failed to claim root path", "component", "metadata",
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
			slog.WarnContext(ctx, "metadata: failed to claim confirmed series group", "component", "metadata",
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

// updateItemStatus sets the status field on a media_items row that is not
// matched, and reports whether the row changed. Neither a failed enrichment
// retry nor an override settled a moment too late invalidates an accepted
// catalog match: a matched item keeps its status, metadata, and ownership while
// the queue records the retry failure. The repository checks that inside the
// UPDATE, so a match stored while this runs is kept too. A missing item is an
// error.
func (s *MetadataService) updateItemStatus(ctx context.Context, contentID, status string) (bool, error) {
	if s != nil && s.hooks.updateItemStatus != nil {
		return s.hooks.updateItemStatus(ctx, contentID, status)
	}
	if s == nil || s.itemRepo == nil {
		return false, fmt.Errorf("metadata item repository is not configured")
	}
	if strings.TrimSpace(contentID) == "" {
		return false, fmt.Errorf("content id is required to update item status")
	}

	changed, err := s.itemRepo.SetStatusUnlessMatched(ctx, contentID, status)
	if err != nil {
		return false, fmt.Errorf("setting item %s to status %s: %w", contentID, status, err)
	}
	return changed, nil
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
	return s.rebindItemByProviderIDsLocked(ctx, skeletonContentID, providerIDs, itemType, false, false)
}

// rebindItemByProviderIDsLocked merges the source onto the confirmed item
// that already owns its provider IDs. With markIdentity (an Identify) the
// merge also records the choice on that item, as reanchorContentID does.
func (s *MetadataService) rebindItemByProviderIDsLocked(
	ctx context.Context,
	sourceContentID string,
	providerIDs map[string]string,
	itemType string,
	allowMatchedSource bool,
	markIdentity bool,
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
	if err := s.rebindItemOnto(ctx, sourceContentID, existing.ContentID, allowMatchedSource, markIdentity); err != nil {
		return "", err
	}
	slog.InfoContext(ctx, "metadata: rebound skeleton to existing item", "component", "metadata",
		"from_content_id", sourceContentID,
		"to_content_id", existing.ContentID,
		"item_type", itemType,
	)
	return existing.ContentID, nil
}

// recoverProviderIDConflict resolves a provider-ID unique conflict when
// persisting sourceContentID: it merges the source onto the item that owns
// its IDs and is the same title, or makes room for the source. It returns the
// content ID to persist under, "" when the conflict stands, and the IDs it
// removed from providerIDs.
func (s *MetadataService) recoverProviderIDConflict(
	ctx context.Context,
	sourceContentID string,
	providerIDs map[string]string,
	itemType string,
	allowMatchedSource bool,
	markIdentity bool,
) (string, map[string]string, error) {
	existing, contested, err := s.findProviderIDOwner(ctx, providerIDs, itemType, sourceContentID)
	if err != nil {
		return "", nil, fmt.Errorf("finding existing item for provider conflict recovery: %w", err)
	}
	// A matched title keeps an ID that the source shares with it while
	// contradicting it on identity; the source is persisted without that ID.
	// An unconfirmed owner gives its IDs up below, as it always has.
	dropped := make(map[string]string)
	for key, owner := range contested {
		if isConfirmedOwnershipStatus(owner.Status) {
			dropped[key] = providerIDs[key]
			delete(providerIDs, key)
			slog.WarnContext(ctx, "metadata: left a provider ID with the other title that holds it", "component", "metadata",
				"content_id", sourceContentID,
				"provider", key,
				"provider_id", dropped[key],
				"owner_content_id", owner.ContentID,
			)
		} else if existing == nil {
			existing = owner
		}
	}
	reboundTo, err := s.recoverProviderIDConflictWith(ctx, existing, sourceContentID, providerIDs, itemType, allowMatchedSource, markIdentity)
	if reboundTo == "" && err == nil && len(dropped) > 0 {
		reboundTo = sourceContentID
	}
	return reboundTo, dropped, err
}

func (s *MetadataService) recoverProviderIDConflictWith(
	ctx context.Context,
	existing *models.MediaItem,
	sourceContentID string,
	providerIDs map[string]string,
	itemType string,
	allowMatchedSource bool,
	markIdentity bool,
) (string, error) {
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
			canonicalID, err := canonicalizeProviderIDDuplicateInto(ctx, s.dbPool, s.recsStaler, sourceContentID, existing.ContentID, false, markIdentity)
			if err != nil {
				return "", err
			}
			return canonicalID, nil
		}
		canonicalID, err := s.canonicalizeProviderIDDuplicate(ctx, sourceContentID, existing.ContentID, allowMatchedSource, markIdentity)
		if err != nil {
			return "", err
		}
		if canonicalID != "" {
			return canonicalID, nil
		}
	}
	return s.rebindItemByProviderIDsLocked(ctx, sourceContentID, providerIDs, itemType, allowMatchedSource, markIdentity)
}

func (s *MetadataService) rebindSkeletonToExistingItem(ctx context.Context, fromContentID, toContentID string) error {
	return s.rebindItemToExistingItem(ctx, fromContentID, toContentID, false)
}

func (s *MetadataService) rebindItemToExistingItem(ctx context.Context, fromContentID, toContentID string, allowMatchedSource bool) error {
	return s.rebindItemOnto(ctx, fromContentID, toContentID, allowMatchedSource, false)
}

// rebindItemOnto is rebindItemToExistingItem that, with markIdentity, also
// records an Identify choice on the target in the same transaction.
func (s *MetadataService) rebindItemOnto(ctx context.Context, fromContentID, toContentID string, allowMatchedSource, markIdentity bool) error {
	if s == nil || fromContentID == "" || toContentID == "" || fromContentID == toContentID {
		return nil
	}
	if s.dbPool == nil {
		return fmt.Errorf("rebind skeleton requires database pool")
	}

	tx, err := s.dbPool.BeginTx(ctx, catalog.MetadataCurationTxOptions(ctx))
	if err != nil {
		return fmt.Errorf("begin skeleton rebind transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := catalog.LockMetadataCurationFiles(ctx, tx, fromContentID); err != nil {
		return err
	}
	err = catalog.GuardMetadataCurationItems(ctx, tx, fromContentID, toContentID)
	if errors.Is(err, catalog.ErrItemNotFound) {
		return rebindOntoAfterMissingItem(ctx, tx, fromContentID, toContentID, markIdentity, err)
	}
	if err != nil {
		return err
	}

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
		// A target with no embedding keeps the source's until the backfill
		// re-embeds it from the target's own text, as the provider-ID merge
		// does. The source's row goes with the source item (ON DELETE
		// CASCADE), and stays when the source item survives.
		{
			name: "merge embeddings",
			sql: `
				INSERT INTO media_item_embeddings (media_item_id, embedding, model, canonical_text, created_at, updated_at)
				SELECT $2, embedding, model, canonical_text, created_at, updated_at
				FROM media_item_embeddings
				WHERE media_item_id = $1
				ON CONFLICT (media_item_id) DO NOTHING
			`,
			args: []any{fromContentID, toContentID},
		},
		{
			// An admin's Identify choice survives the merge (identify_sticky.go).
			name: "keep identity choice",
			sql: `
				UPDATE media_items dest
				SET identity_chosen_at = GREATEST(dest.identity_chosen_at, src.identity_chosen_at)
				FROM media_items src
				WHERE dest.content_id = $2 AND src.content_id = $1
				  AND src.identity_chosen_at IS NOT NULL
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
	report, err := reattribute.Run(ctx, tx, reattribute.Options{
		FromContentID: fromContentID,
		ToContentID:   toContentID,
		WholeItem:     true,
		EpisodePairs:  episodePairs,
	})
	if err != nil {
		return fmt.Errorf("reattributing user state %s -> %s: %w", fromContentID, toContentID, err)
	}

	for _, step := range steps {
		if _, err := tx.Exec(ctx, step.sql, step.args...); err != nil {
			return fmt.Errorf("%s: %w", step.name, err)
		}
	}
	if markIdentity {
		if err := catalog.MarkIdentityChosenTx(ctx, tx, toContentID); err != nil {
			return err
		}
	}
	if err := catalog.RecomputeSeriesEpisodeMembershipDenorms(ctx, tx, []string{fromContentID}); err != nil {
		return err
	}

	if err := catalog.EnqueueSearchIndexRename(ctx, tx, fromContentID, toContentID); err != nil {
		return fmt.Errorf("enqueue catalog search skeleton rebind %s -> %s: %w", fromContentID, toContentID, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit skeleton rebind transaction: %w", err)
	}
	MarkRecommendationsStale(ctx, s.recsStaler, MovedStateTargets(report, toContentID, episodePairs)...)
	return nil
}

// rebindOntoAfterMissingItem finishes a curated rebind whose guard found the
// source or the target missing (notFound).
func rebindOntoAfterMissingItem(ctx context.Context, tx pgx.Tx, fromContentID, toContentID string, markIdentity bool, notFound error) error {
	// Lock and authorize the target again: one created since the guard's read
	// is visible now. A missing target still fails.
	if err := catalog.GuardMetadataCurationItems(ctx, tx, toContentID); err != nil {
		return err
	}
	var sourceExists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM media_items WHERE content_id = $1)`, fromContentID).Scan(&sourceExists); err != nil {
		return err
	}
	if sourceExists {
		return notFound
	}
	// A source merged or deleted after the caller found it leaves nothing to
	// move. An Identify still marks the target, as without curation authority;
	// no source rows are touched.
	if !markIdentity {
		return nil
	}
	if err := catalog.MarkIdentityChosenTx(ctx, tx, toContentID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit skeleton rebind transaction: %w", err)
	}
	return nil
}

// mergeEpisodeIDPairs maps the source series' episode content ids onto the
// target series' episodes by (season, episode) number, so episode-level user
// state survives a series merge. A source episode with no counterpart on the
// target is paired with the id the target composes for it, when both series are
// provider-anchored and the source episode is on its series' composition: the
// source's composed id does not die with it, because the show the source
// anchor names mints it again when it is scanned in, so its state moves to the
// id the target's episode takes when a scan creates it. Other unpaired
// episodes keep their state on ids that die with the source series.
func mergeEpisodeIDPairs(ctx context.Context, tx pgx.Tx, fromContentID, toContentID string) ([]reattribute.IDPair, error) {
	rows, err := tx.Query(ctx, `
		SELECT src.content_id, src.season_number, src.episode_number, dest.content_id
		FROM episodes src
		LEFT JOIN episodes dest
		  ON dest.series_id = $2
		 AND dest.season_number = src.season_number
		 AND dest.episode_number = src.episode_number
		WHERE src.series_id = $1
	`, fromContentID, toContentID)
	if err != nil {
		return nil, fmt.Errorf("mapping episode ids for merge %s -> %s: %w", fromContentID, toContentID, err)
	}
	var pairs, composed []reattribute.IDPair
	for rows.Next() {
		var sourceID string
		var seasonNumber, episodeNumber int
		var destID *string
		if err := rows.Scan(&sourceID, &seasonNumber, &episodeNumber, &destID); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scanning episode id pair: %w", err)
		}
		if destID != nil {
			if *destID != sourceID {
				pairs = append(pairs, reattribute.IDPair{From: sourceID, To: *destID})
			}
			continue
		}
		oldID, okOld := contentid.ForEpisode(fromContentID, seasonNumber, episodeNumber)
		newID, okNew := contentid.ForEpisode(toContentID, seasonNumber, episodeNumber)
		if okOld && okNew && sourceID == oldID && newID != sourceID {
			composed = append(composed, reattribute.IDPair{From: sourceID, To: newID})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mapping episode ids for merge %s -> %s: %w", fromContentID, toContentID, err)
	}
	if len(composed) == 0 {
		return pairs, nil
	}

	// An id held by another series' episode is not this target's to give.
	targets := make([]string, len(composed))
	for i, pair := range composed {
		targets[i] = pair.To
	}
	takenRows, err := tx.Query(ctx, `SELECT content_id FROM episodes WHERE content_id = ANY($1)`, targets)
	if err != nil {
		return nil, fmt.Errorf("checking composed episode ids for merge %s -> %s: %w", fromContentID, toContentID, err)
	}
	taken := make(map[string]struct{})
	for takenRows.Next() {
		var id string
		if err := takenRows.Scan(&id); err != nil {
			takenRows.Close()
			return nil, fmt.Errorf("scanning composed episode id: %w", err)
		}
		taken[id] = struct{}{}
	}
	takenRows.Close()
	if err := takenRows.Err(); err != nil {
		return nil, fmt.Errorf("checking composed episode ids for merge %s -> %s: %w", fromContentID, toContentID, err)
	}
	for _, pair := range composed {
		if _, ok := taken[pair.To]; !ok {
			pairs = append(pairs, pair)
		}
	}
	return pairs, nil
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

// advisoryAgeValue flattens a stored advisory age for merging. The stored
// column is nullable and MetadataResult uses 0 for "none", so a missing
// advisory and a zero both mean the same thing to the merge.
func advisoryAgeValue(age *int) int {
	if age == nil || *age <= 0 {
		return 0
	}
	return *age
}

// advisoryAgePointer is the inverse: a non-positive age stores NULL rather
// than a zero row, so "no advisory" is one state in the column, not two.
func advisoryAgePointer(age int) *int {
	if age <= 0 {
		return nil
	}
	return &age
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
		AdvisoryAge:       advisoryAgeValue(item.AdvisoryAge),
		AdvisorySource:    item.AdvisorySource,
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
	result.ShowStatus = item.ShowStatus

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
		AdvisoryAge:       advisoryAgePointer(r.AdvisoryAge),
		AdvisorySource:    r.AdvisorySource,
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

	// show_status is shared by two value domains: the series lifecycle
	// (normalized here) and the manga publication domain (normalized by the
	// manga enrichment pipeline). Pass non-series values through untouched so
	// a round-trip through itemToMetadataResult can never mangle them.
	if contentType == "series" {
		item.ShowStatus = NormalizeShowStatus(r.ShowStatus)
	} else {
		item.ShowStatus = r.ShowStatus
	}

	return item
}

// applyBestImages picks the item's artwork from images. preferredLang is the
// library's metadata language; fallbackLang is metadata.fallback_language,
// empty when it is off.
func applyBestImages(item *models.MediaItem, images []RemoteImage, mode MergeMode, preferredLang, fallbackLang string) {
	// Images arrive in provider-chain order (highest priority first).
	// Within each language tier, the first provider with a match wins; within
	// that provider, pick the highest-rated image.
	type best struct {
		url        string
		rating     float64
		providerID string
	}

	selectBest := func(imageType ImageType, filters []func(RemoteImage) bool) *best {
		for _, img := range images {
			if img.Type == imageType && img.URL != "" && isLocalImageSourcePath(img.URL) && isWordmarkLogoCandidate(img) {
				return &best{url: img.URL, rating: img.Rating, providerID: img.ProviderID}
			}
		}

		for _, accept := range filters {
			candidate := &best{}
			for _, img := range images {
				if img.Type != imageType || img.URL == "" || !isWordmarkLogoCandidate(img) || !accept(img) {
					continue
				}
				if candidate.url == "" {
					candidate.url = img.URL
					candidate.rating = img.Rating
					candidate.providerID = img.ProviderID
				} else if img.ProviderID == candidate.providerID && img.Rating > candidate.rating {
					candidate.url = img.URL
					candidate.rating = img.Rating
				}
			}
			if candidate.url != "" {
				return candidate
			}
		}
		return &best{}
	}

	posterFilters, logoFilters := artworkLanguageFilters(preferredLang, fallbackLang)

	bestByType := map[ImageType]*best{
		ImagePoster: selectBest(ImagePoster, posterFilters),
		// Provider backdrops tagged with a language may contain text, so
		// language-neutral backgrounds win. A tagged backdrop is still better
		// than none, so it stays as the terminal tier: items whose backdrops
		// are all language-tagged must not end up with no backdrop at all.
		ImageBackdrop: selectBest(ImageBackdrop, []func(RemoteImage) bool{imageLanguageNeutral, imageHasLanguage}),
		ImageLogo:     selectBest(ImageLogo, logoFilters),
		// Banners, thumbs, clear art and disc art usually carry the title, so
		// they follow the library language like logos.
		ImageBanner:   selectBest(ImageBanner, logoFilters),
		ImageThumb:    selectBest(ImageThumb, logoFilters),
		ImageClearArt: selectBest(ImageClearArt, logoFilters),
		ImageDisc:     selectBest(ImageDisc, logoFilters),
	}

	imagesLocked := artworkLocked(item)
	applyIfBetter := func(current *string, b *best) {
		if b.url == "" {
			return
		}
		// Locked artwork keeps what the item has; only an empty slot fills.
		if imagesLocked && *current != "" {
			return
		}
		// Local sidecar candidates always apply: they carry rating 0, so an
		// already-matched item could otherwise never pick up local art, and a
		// rated remote image could stickily displace it after a transient
		// local read failure.
		if *current == "" || mode == MergeReplaceUnlocked || b.rating > 0 || isLocalImageSourcePath(b.url) {
			*current = b.url
		}
	}
	applyIfBetter(&item.PosterPath, bestByType[ImagePoster])
	applyIfBetter(&item.BackdropPath, bestByType[ImageBackdrop])
	if bestByType[ImageLogo].url == "" && mode == MergeReplaceUnlocked && !imagesLocked && itemHasClearArtLogo(item) {
		// A user-triggered refresh must not preserve clear art stored as the
		// logo when no wordmark replacement exists. A logo kept under the
		// Images lock was chosen by an admin and stays.
		item.LogoPath = ""
		item.LogoSourcePath = ""
	} else {
		applyIfBetter(&item.LogoPath, bestByType[ImageLogo])
	}
	for _, field := range itemOnlyArtworkFields(item) {
		// These slots arrive carried over from the stored item, so under the
		// lock a source still waiting for its cache job holds the slot too.
		if imagesLocked && *field.source != "" {
			continue
		}
		applyIfBetter(field.path, bestByType[field.imageType])
	}
}

func imageLanguageIs(want string) func(RemoteImage) bool {
	return func(img RemoteImage) bool {
		return strings.EqualFold(strings.TrimSpace(img.Language), want)
	}
}

func imageHasLanguage(img RemoteImage) bool {
	return strings.TrimSpace(img.Language) != ""
}

func imageLanguageNeutral(img RemoteImage) bool {
	return strings.TrimSpace(img.Language) == ""
}

func posterHasText(img RemoteImage) bool {
	if img.IncludesText != nil {
		return *img.IncludesText
	}
	return imageHasLanguage(img)
}

func posterTextless(img RemoteImage) bool {
	return !posterHasText(img)
}

func posterLanguageIs(want string) func(RemoteImage) bool {
	return func(img RemoteImage) bool {
		return posterHasText(img) && strings.EqualFold(strings.TrimSpace(img.Language), want)
	}
}

// artworkLanguageFilters returns the tiers applyBestImages tries, in order,
// for posters and for logo-like artwork. Artwork with text follows the
// library's metadata language, then the fallback language, then English as
// the cross-language fallback.
func artworkLanguageFilters(preferredLang, fallbackLang string) (posters, logos []func(RemoteImage) bool) {
	fallbackLang = strings.TrimSpace(fallbackLang)
	textLanguages := make([]string, 0, 3)
	for _, language := range []string{strings.TrimSpace(preferredLang), fallbackLang, "en"} {
		if language != "" && !slices.ContainsFunc(textLanguages, func(seen string) bool { return strings.EqualFold(seen, language) }) {
			textLanguages = append(textLanguages, language)
		}
	}
	for _, language := range textLanguages {
		posters = append(posters, posterLanguageIs(language))
		logos = append(logos, imageLanguageIs(language))
	}
	// With the fallback on, textless artwork beats a poster titled in another
	// language (usually the original one, which the fallback exists to
	// avoid); with it off, any text-bearing poster beats textless artwork.
	if fallbackLang != "" {
		posters = append(posters, posterTextless, posterHasText)
	} else {
		posters = append(posters, posterHasText, posterTextless)
	}
	// A language-neutral logo is a safer fallback than a logo explicitly
	// tagged with an unrelated language.
	logos = append(logos, imageLanguageNeutral, imageHasLanguage)
	return posters, logos
}

// isWordmarkLogoCandidate rejects clear art offered as a logo. Clear art is an
// illustrated composite of the title with characters or props, not the title
// wordmark a logo slot expects. TVDB plugins before v1.4.0 reported series
// ClearArt as ImageLogo; TVDB serves ClearArt under a /clearart/ path and
// ClearLogo under /clearlogo/, so the path identifies it for every plugin
// version. Local sidecars stay authoritative.
func isWordmarkLogoCandidate(img RemoteImage) bool {
	if img.Type != ImageLogo || isLocalImageSourcePath(img.URL) {
		return true
	}
	return !isClearArtPath(img.URL)
}

func itemHasClearArtLogo(item *models.MediaItem) bool {
	if item == nil {
		return false
	}
	return isClearArtPath(item.LogoSourcePath) || isClearArtPath(item.LogoPath)
}

func isClearArtPath(path string) bool {
	return strings.Contains(strings.ToLower(path), "/clearart/")
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
	return append([]itemArtworkField{
		{imageType: ImagePoster, path: &item.PosterPath, source: &item.PosterSourcePath, thumbhash: &item.PosterThumbhash},
		{imageType: ImageBackdrop, path: &item.BackdropPath, source: &item.BackdropSourcePath, thumbhash: &item.BackdropThumbhash},
		{imageType: ImageLogo, path: &item.LogoPath, source: &item.LogoSourcePath},
	}, itemOnlyArtworkFields(item)...)
}

// keepStoredArtwork copies every artwork path, source path and thumbhash from
// the stored item, so a write that fetched no artwork leaves it untouched.
func keepStoredArtwork(item, existing *models.MediaItem) {
	stored := itemArtworkFields(existing)
	for i, field := range itemArtworkFields(item) {
		*field.path = *stored[i].path
		*field.source = *stored[i].source
		if field.thumbhash != nil {
			*field.thumbhash = *stored[i].thumbhash
		}
	}
}

// storedItemArtwork holds the stored poster, backdrop and logo copies
// clearStoredItemArtwork emptied.
type storedItemArtwork struct{ poster, backdrop, logo string }

func (s *storedItemArtwork) slots(item *models.MediaItem) [3][2]*string {
	return [3][2]*string{{&item.PosterPath, &s.poster}, {&item.BackdropPath, &s.backdrop}, {&item.LogoPath, &s.logo}}
}

// clearStoredItemArtwork empties the poster, backdrop and logo slots that
// serve a stored copy, so applyBestImages fills them with any provider image.
func clearStoredItemArtwork(item *models.MediaItem) storedItemArtwork {
	var stored storedItemArtwork
	for _, slot := range stored.slots(item) {
		if isCachedImagePath(*slot[0]) {
			*slot[1], *slot[0] = *slot[0], ""
		}
	}
	return stored
}

// restoreUnfilled gives each slot no provider image filled its stored copy
// back, so the rest of the merge sees it as if it had never been cleared.
func (s storedItemArtwork) restoreUnfilled(item *models.MediaItem) {
	for _, slot := range s.slots(item) {
		if *slot[0] == "" && *slot[1] != "" {
			*slot[0] = *slot[1]
		}
	}
}

// keepStored is preserveCachedArtwork's: false while metadata.cache_images is
// off and the item's artwork is not locked.
func prepareItemImagesForQueue(item, existing *models.MediaItem, keepStored bool) {
	for _, field := range itemArtworkFields(item) {
		// applyBestImages intentionally clears a clear-art logo on a manual
		// refresh when no wordmark replacement exists. Do not let the
		// generic cached-art preservation path restore that rejected logo.
		if field.imageType == ImageLogo && *field.path == "" && !artworkLocked(existing) && itemHasClearArtLogo(existing) {
			*field.source = ""
			continue
		}
		existingPath := existingImagePath(existing, field.imageType)
		existingThumbhash := existingImageThumbhash(existing, field.imageType)
		existingSource := existingImageSourcePath(existing, field.imageType)
		// A banner, thumb, clear art or disc slot is carried over from the
		// stored item; one still waiting for its cache job keeps its source
		// so the queued job still matches it.
		if isItemOnlyArtworkType(field.imageType) && *field.path == "" && existingPath == "" &&
			*field.source != "" && *field.source == existingSource {
			continue
		}
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
			keepStored,
		)
		*field.path = nextPath
		*field.source = nextSource
		if field.thumbhash != nil {
			*field.thumbhash = nextThumbhash
		}
	}
}

func (s *MetadataService) enqueueItemImages(ctx context.Context, item *models.MediaItem, providerIDs map[string]string, images []RemoteImage) error {
	if s == nil || s.imageCacheJobs == nil || item == nil || item.ContentID == "" {
		return nil
	}
	inputs := make([]EnqueueImageCacheJobInput, 0, 3)
	for _, field := range itemArtworkFields(item) {
		sourcePath := strings.TrimSpace(*field.source)
		if !isCacheableImageSourcePath(sourcePath) {
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
			// An item rebuilt under the same content ID (a Complete Refresh,
			// or removing and re-adding a local-only series) has no cached
			// copy, but its earlier job for this source still reads
			// succeeded. Without a requeue, local artwork stays blank and
			// remote artwork stays uncached.
			RequeueSucceeded: !isCachedImagePath(*field.path),
		})
	}
	return s.enqueueImageCacheJobs(ctx, "item", item.ContentID, inputs)
}

func (s *MetadataService) enqueueItemLocalizationImages(ctx context.Context, item *models.MediaItem, loc *models.MediaItemLocalization, providerIDs map[string]string, images []RemoteImage) {
	if s == nil || s.imageCacheJobs == nil || item == nil || loc == nil || loc.ContentID == "" || loc.Language == "" {
		return
	}
	locItem := &models.MediaItem{
		ContentID:          loc.ContentID,
		Type:               item.Type,
		PosterPath:         loc.PosterPath,
		PosterSourcePath:   loc.PosterSourcePath,
		BackdropPath:       loc.BackdropPath,
		BackdropSourcePath: loc.BackdropSourcePath,
		LogoPath:           loc.LogoPath,
		LogoSourcePath:     loc.LogoSourcePath,
	}
	inputs := make([]EnqueueImageCacheJobInput, 0, 3)
	for _, field := range itemArtworkFields(locItem) {
		sourcePath := strings.TrimSpace(*field.source)
		if !isCacheableImageSourcePath(sourcePath) {
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
			// Localization rows are deleted with their item, so a rebuild
			// leaves them in the same state; see enqueueItemImages.
			RequeueSucceeded: !isCachedImagePath(*field.path),
		})
	}
	_ = s.enqueueImageCacheJobs(ctx, "item localization", loc.ContentID, inputs)
}

func itemImageCacheAttribution(item *models.MediaItem, providerIDs map[string]string, images []RemoteImage, sourcePath string) (string, string) {
	// Local sidecar sources are attributed to the synthetic "local" provider
	// (the generic scheme parse below would derive "file") and keyed by the
	// item's own content ID, matching the audiobook/ebook local/ precedent.
	if isLocalImageSourcePath(sourcePath) {
		contentID := ""
		if item != nil {
			contentID = item.ContentID
		}
		return localSourceProviderID(sourcePath), contentID
	}
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
				slog.WarnContext(ctx, "metadata: image cache failed, keeping CDN URL", "component", "metadata",
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
		*j.field.path = CachedImageOriginalPath(cr.result)
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
	chain, err := s.resolveChainCached(ctx, folderID, contentType)
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
			slog.WarnContext(ctx, "fetch item images: provider error", "component", "metadata",
				"provider", p.Slug(), "error", err)
			providerErrors[p.Slug()] = err.Error()
			continue
		}
		for _, image := range images {
			if isWordmarkLogoCandidate(image) {
				allImages = append(allImages, image)
			}
		}
	}

	// Sort by rating descending (popularity).
	sort.Slice(allImages, func(i, j int) bool {
		return allImages[i].Rating > allImages[j].Rating
	})

	return allImages, providerErrors, nil
}

// FetchSeasonImages queries the configured season provider chain for the full
// artwork gallery of one exact season. Providers must echo SeasonNumber on
// scoped results, preventing an older plugin that ignores the request field
// from leaking show artwork into a numbered season. If a provider has not yet
// adopted the gallery contract, its exact primary poster from GetSeasons is a
// compatibility fallback. Specials additionally include ordinary show posters
// after every exact Specials result, giving users a useful fallback when no
// dedicated Specials artwork exists.
func (s *MetadataService) FetchSeasonImages(ctx context.Context, providerIDs map[string]string, language string, folderID int, seasonNumber int) ([]RemoteImage, map[string]string, error) {
	chain, err := s.resolveChainCached(ctx, folderID, "season")
	if err != nil {
		return nil, nil, fmt.Errorf("resolving provider chain: %w", err)
	}

	var exactImages []RemoteImage
	var specialsFallback []RemoteImage
	providerErrors := make(map[string]string)
	// Exact and fallback results are deduplicated separately so a show poster
	// from an earlier provider cannot shadow the same URL confirmed as exact
	// season art by a later provider. Fallbacks that duplicate an exact result
	// are dropped once every provider has answered.
	exactSeen := make(map[string]struct{})
	fallbackSeen := make(map[string]struct{})
	appendPoster := func(target *[]RemoteImage, seen map[string]struct{}, image RemoteImage) bool {
		if image.Type != ImagePoster || strings.TrimSpace(image.URL) == "" {
			return false
		}
		if _, duplicate := seen[image.URL]; duplicate {
			return false
		}
		seen[image.URL] = struct{}{}
		*target = append(*target, image)
		return true
	}

	for _, p := range chain {
		exactFound := false
		ip, imageCapable := p.(ImageProvider)
		if imageCapable {
			requestedSeason := seasonNumber
			images, imageErr := ip.GetImages(ctx, ImageRequest{
				ProviderIDs:  providerIDs,
				ContentType:  "series",
				Language:     language,
				SeasonNumber: &requestedSeason,
			})
			if imageErr != nil {
				slog.WarnContext(ctx, "fetch season images: provider gallery error", "component", "metadata",
					"provider", p.Slug(), "season", seasonNumber, "error", imageErr)
				providerErrors[p.Slug()] = imageErr.Error()
			} else {
				for _, image := range images {
					if image.SeasonNumber == nil || *image.SeasonNumber != seasonNumber {
						continue
					}
					if strings.TrimSpace(image.ProviderID) == "" {
						image.ProviderID = p.Slug()
					}
					if appendPoster(&exactImages, exactSeen, image) {
						exactFound = true
					}
				}
			}
		}

		// Backward-compatible exact primary for providers that do not yet emit
		// season-scoped gallery records.
		if !exactFound {
			if ep, ok := p.(EpisodeProvider); ok {
				seasons, seasonErr := ep.GetSeasons(ctx, SeasonsRequest{
					ProviderIDs: providerIDs,
					ContentType: "series",
					Language:    language,
				})
				if seasonErr != nil {
					slog.WarnContext(ctx, "fetch season images: provider season error", "component", "metadata",
						"provider", p.Slug(), "season", seasonNumber, "error", seasonErr)
					providerErrors[p.Slug()] = seasonErr.Error()
				} else {
					for _, season := range seasons {
						if season.SeasonNumber != seasonNumber || strings.TrimSpace(season.PosterPath) == "" {
							continue
						}
						n := seasonNumber
						appendPoster(&exactImages, exactSeen, RemoteImage{
							ProviderID:   p.Slug(),
							URL:          season.PosterPath,
							Type:         ImagePoster,
							SeasonNumber: &n,
						})
						break
					}
				}
			}
		}

		if seasonNumber == 0 && imageCapable {
			images, imageErr := ip.GetImages(ctx, ImageRequest{
				ProviderIDs: providerIDs,
				ContentType: "series",
				Language:    language,
			})
			if imageErr != nil {
				slog.WarnContext(ctx, "fetch season images: Specials show fallback error", "component", "metadata",
					"provider", p.Slug(), "error", imageErr)
				providerErrors[p.Slug()] = imageErr.Error()
				continue
			}
			for _, image := range images {
				if image.SeasonNumber != nil {
					continue
				}
				if strings.TrimSpace(image.ProviderID) == "" {
					image.ProviderID = p.Slug()
				}
				appendPoster(&specialsFallback, fallbackSeen, image)
			}
		}
	}

	fallbacks := specialsFallback[:0]
	for _, image := range specialsFallback {
		if _, exact := exactSeen[image.URL]; !exact {
			fallbacks = append(fallbacks, image)
		}
	}

	sort.SliceStable(exactImages, func(i, j int) bool { return exactImages[i].Rating > exactImages[j].Rating })
	sort.SliceStable(fallbacks, func(i, j int) bool { return fallbacks[i].Rating > fallbacks[j].Rating })
	return append(exactImages, fallbacks...), providerErrors, nil
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

	storedPath := CachedImageOriginalPath(result)
	return &ApplyItemImageResult{
		StoredPath: storedPath,
		Revision:   result.Revision,
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
	Revision   string
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
	case ImageBanner:
		return ImageCacheImageBanner
	case ImageThumb:
		return ImageCacheImageThumb
	case ImageClearArt:
		return ImageCacheImageClearArt
	case ImageDisc:
		return ImageCacheImageDisc
	default:
		return "poster"
	}
}

// ImageTypeFromString converts a string to an ImageType. It parses only the
// types the admin image picker can apply; anything else stays a poster, as it
// always has for that endpoint.
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

// applyCandidateProviderIDConsensus replaces accumulated canonical IDs with a
// normalized winner. Compatible IDs retain the historical aggregator/plugin
// bootstrap behavior. When providers conflict, an owning-provider value wins;
// unresolved keys are removed and carried in quarantine through Phase 2 so
// detail responses cannot reintroduce them. The returned keys are deliberate
// replacements that must overwrite stored provider IDs at merge.
func applyCandidateProviderIDConsensus(accumulatedIDs map[string]string, winner *MatchCandidate, quarantine map[string]struct{}) map[string]struct{} {
	replaced := make(map[string]struct{})
	if winner == nil {
		return replaced
	}
	locallyQuarantined := make(map[string]struct{}, len(winner.ConflictingProviderIDKeys))
	for _, key := range winner.ConflictingProviderIDKeys {
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			continue
		}
		delete(accumulatedIDs, key)
		if replacement := strings.TrimSpace(winner.ConfirmedProviderIDs[key]); replacement != "" {
			accumulatedIDs[key] = replacement
			delete(quarantine, key)
			replaced[key] = struct{}{}
			continue
		}
		locallyQuarantined[key] = struct{}{}
		if quarantine != nil {
			quarantine[key] = struct{}{}
		}
	}
	for key, value := range winner.ProviderIDs {
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		if _, quarantined := locallyQuarantined[key]; quarantined {
			continue
		}
		if _, quarantined := quarantine[key]; quarantined {
			continue
		}
		accumulatedIDs[key] = value
	}
	return replaced
}

// storedIdentityProviderIDs returns an item's stored TMDB, TVDB and IMDb IDs.
func storedIdentityProviderIDs(item *models.MediaItem) map[string]string {
	if item == nil {
		return nil
	}
	return map[string]string{"tmdb": item.TmdbID, "tvdb": item.TvdbID, "imdb": item.ImdbID}
}

// canonicalIdentityProviderIDs returns the valid TMDB, TVDB and IMDb IDs in ids.
func canonicalIdentityProviderIDs(ids map[string]string) map[string]string {
	chosen := make(map[string]string, len(trustedSearchIDKeys))
	for key, value := range ids {
		key = strings.ToLower(strings.TrimSpace(key))
		if !slices.Contains(trustedSearchIDKeys, key) {
			continue
		}
		if value, valid := sanitizeProviderIDValue(key, value); valid {
			chosen[key] = value
		}
	}
	return chosen
}

// storedItemIdentity returns the TMDB, TVDB and IMDb IDs an item
// already has: its columns, then its durable provider-ID rows for any key the
// columns leave empty.
func storedItemIdentity(item *models.MediaItem, durable map[string]string) map[string]string {
	stored := storedIdentityProviderIDs(item)
	if stored == nil {
		stored = map[string]string{}
	}
	for key, value := range canonicalIdentityProviderIDs(durable) {
		if strings.TrimSpace(stored[key]) == "" {
			stored[key] = value
		}
	}
	return stored
}

// identityChoiceCorrects reports whether chosen identity IDs (an Identify's,
// or an NFO's on a manual refresh) correct the item's stored match: a chosen
// ID differs from the stored value for its key, or the item stores identity
// IDs and none of the chosen ones agrees with them. Restating a stored ID,
// with or without adding one the item lacks, confirms or extends the match.
// Values compare normalized, so an IMDb ID stored in another case agrees.
func identityChoiceCorrects(chosen, stored map[string]string) bool {
	agrees, hasStored := false, false
	for _, key := range trustedSearchIDKeys {
		old := normalizeProviderIDComparisonValue(key, stored[key])
		hasStored = hasStored || old != ""
		next := normalizeProviderIDComparisonValue(key, chosen[key])
		if old == "" || next == "" {
			continue
		}
		if old != next {
			return true
		}
		agrees = true
	}
	return hasStored && !agrees && len(chosen) > 0
}

// rejectIdentityProviderIDs records the stored identity values a correction
// rejects: each TMDB, TVDB or IMDb ID in any of the item's stored sources (its
// columns and its durable rows, which can disagree) that the chosen IDs don't
// restate. They came from the wrong match. Recording values rather than keys
// leaves a different value for the same key alone, such as the IDs of an
// existing item that a re-anchor merges into.
//
// Stored values are taken as they are, not validated: a malformed legacy ID
// from the wrong match must be rejected too, or the merge would restore it.
func rejectIdentityProviderIDs(rejected providerIDValueSet, chosen map[string]string, sources ...map[string]string) {
	for _, source := range sources {
		for rawKey, rawValue := range source {
			key := strings.ToLower(strings.TrimSpace(rawKey))
			value := strings.TrimSpace(rawValue)
			if value == "" || !slices.Contains(trustedSearchIDKeys, key) {
				continue
			}
			if normalizeProviderIDComparisonValue(key, value) != normalizeProviderIDComparisonValue(key, chosen[key]) {
				rejected.add(key, value)
			}
		}
	}
}

func mediaItemWithProviderIDs(item *models.MediaItem, providerIDs map[string]string) *models.MediaItem {
	if item == nil {
		return nil
	}
	selectionItem := *item
	selectionItem.TmdbID = strings.TrimSpace(providerIDs[contentid.ProviderTMDB])
	selectionItem.TvdbID = strings.TrimSpace(providerIDs[contentid.ProviderTVDB])
	selectionItem.ImdbID = strings.TrimSpace(providerIDs[contentid.ProviderIMDB])
	return &selectionItem
}

// applyLocalIdentityHints folds the curated tmdb/imdb/tvdb ids the chain's
// IdentityHintProviders (the built-in NFO provider) declared, as collected by
// collectLocalIdentityHints, into accumulatedIDs ahead of Phase-1 search. Keys
// in protected (stored durable ids) are never overwritten — a conflict is
// logged and the stored value kept. The returned map holds the hint values
// that won a slot in accumulatedIDs, so candidate selection can anchor on them
// through the trusted-hint machinery.
func applyLocalIdentityHints(
	ctx context.Context,
	local []localIdentityHint,
	accumulatedIDs map[string]string,
	protected map[string]bool,
) map[string]string {
	var won map[string]string
	for _, hint := range local {
		hints := hint.ids
		for _, key := range trustedSearchIDKeys {
			value, valid := sanitizeProviderIDValue(key, hints[key])
			if !valid {
				if strings.TrimSpace(hints[key]) != "" {
					slog.WarnContext(ctx, "metadata: ignoring malformed local identity hint", "component", "metadata",
						"provider", hint.provider, "key", key, "value", strings.TrimSpace(hints[key]))
				}
				continue
			}
			current := strings.TrimSpace(accumulatedIDs[key])
			if current != "" && protected[key] {
				if current != value {
					slog.WarnContext(ctx, "metadata: local identity hint conflicts with stored id; keeping stored id", "component", "metadata",
						"provider", hint.provider, "key", key, "stored", current, "hint", value)
				}
				continue
			}
			if current != "" && current != value {
				slog.WarnContext(ctx, "metadata: local identity hint overrides conflicting hint id", "component", "metadata",
					"provider", hint.provider, "key", key, "previous", current, "hint", value)
			}
			accumulatedIDs[key] = value
			if won == nil {
				won = make(map[string]string, len(trustedSearchIDKeys))
			}
			won[key] = value
		}
	}
	return won
}

// overrideHintIDs applies won identity-hint values onto a MatchHints copy so
// trustedHintIDsPresent/candidateMatchesTrustedIDs anchor selection on them.
func overrideHintIDs(hints *MatchHints, won map[string]string) {
	if v := won["tmdb"]; v != "" {
		hints.TmdbID = v
	}
	if v := won["tvdb"]; v != "" {
		hints.TvdbID = v
	}
	if v := won["imdb"]; v != "" {
		hints.ImdbID = v
	}
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
