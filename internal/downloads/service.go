package downloads

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/idgen"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// FileResolver looks up media files by various keys.
type FileResolver interface {
	GetByID(ctx context.Context, id int) (*models.MediaFile, error)
	GetByContentID(ctx context.Context, contentID string) ([]*models.MediaFile, error)
	GetByEpisodeID(ctx context.Context, episodeID string) ([]*models.MediaFile, error)
	ListByEpisodeIDs(ctx context.Context, episodeIDs []string) (map[string][]*models.MediaFile, error)
}

// ItemResolver looks up media items.
type ItemResolver interface {
	GetByID(ctx context.Context, contentID string) (*models.MediaItem, error)
}

// EpisodeResolver lists episodes for a series or one of its seasons.
type EpisodeResolver interface {
	ListBySeries(ctx context.Context, seriesID string) ([]*models.Episode, error)
	ListBySeason(ctx context.Context, seriesID string, seasonNumber int) ([]*models.Episode, error)
	ListSeasons(ctx context.Context, seriesID string) ([]catalog.SeasonSummary, error)
}

// UserResolver looks up users.
type UserResolver interface {
	GetByID(ctx context.Context, id int) (*models.User, error)
}

// ItemAccessChecker checks library/content-rating access.
type ItemAccessChecker interface {
	EnsureAccessible(ctx context.Context, contentID string, filter catalog.AccessFilter) error
}

// SettingsReader loads all server settings as a flat map.
type SettingsReader interface {
	GetAll(ctx context.Context) (map[string]string, error)
}

const configCacheTTL = 30 * time.Second

// Capability describes what download functionality is available to a user,
// for client feature detection (GET /downloads/capability).
type Capability struct {
	Enabled              bool
	DownloadAllowed      bool
	QualityPresets       []string
	TranscodeEnabled     bool
	TranscodeUserAllowed bool
	// SeasonDownload reports whether per-season series downloads are available;
	// SeriesMonitoring reports whether auto-download subscriptions are available
	// (and MonitoringModes the modes a client may request).
	SeasonDownload   bool
	SeriesMonitoring bool
	MonitoringModes  []string
}

// Service orchestrates download permission checks, quota enforcement, quality
// policy, file resolution, and file serving for both ephemeral/account-level
// rows (DeviceID == "") and managed device-library entries (DeviceID set).
type Service struct {
	repo          *Repository
	policy        DownloadQualityResolver
	actionDecider ActionDecider
	bandwidth     *BandwidthManager
	limiter       *QuantityLimiter
	fileRepo      FileResolver
	itemRepo      ItemResolver
	episodeRepo   EpisodeResolver
	userRepo      UserResolver
	groupProvider access.GroupPolicyProvider
	itemAccess    ItemAccessChecker
	settings      SettingsReader

	// Offline-manifest dependencies (Phase 2); nil until SetOfflineDeps wires them.
	manifest       *ManifestBuilder
	subtitleSource SubtitleSource
	artworkSource  ManifestSource
	httpClient     *http.Client

	// Prepare-to-file pipeline (Phase 3); nil until SetArtifactManager wires it.
	artifacts *ArtifactManager

	// Series-monitoring subscriptions (auto-download); nil until SetSubscriptions
	// wires the repo, in which case the subscription endpoints report unavailable.
	subRepo *SubscriptionRepository

	cfgMu       sync.RWMutex
	cfg         config.DownloadConfig
	cfgLoadedAt time.Time
}

// SetOfflineDeps wires the offline-manifest dependencies (catalog detail for
// manifest + artwork, subtitle assets, and an HTTP client for streaming
// artwork bytes). When unset, the manifest/artwork/subtitle endpoints report
// unavailable.
func (s *Service) SetOfflineDeps(detail ManifestSource, subs SubtitleSource, client *http.Client) {
	s.artworkSource = detail
	s.subtitleSource = subs
	// The artifact lookup reads s.artifacts at call time so the wiring order of
	// SetOfflineDeps and SetArtifactManager doesn't matter.
	s.manifest = NewManifestBuilder(detail, subs, s.fileRepo, func(ctx context.Context, id string) (*Artifact, error) {
		if s.artifacts == nil {
			return nil, ErrFormatUnavailable
		}
		return s.artifacts.repo.GetByID(ctx, id)
	})
	if client == nil {
		client = http.DefaultClient
	}
	s.httpClient = client
}

// SetArtifactManager wires the prepare-to-file pipeline. When unset, remux/
// transcode requests report unavailable (only `original` is servable).
func (s *Service) SetArtifactManager(m *ArtifactManager) {
	s.artifacts = m
}

// SetSubscriptions wires the series-monitoring (download subscription) repo.
// When unset, the subscription endpoints report unavailable.
func (s *Service) SetSubscriptions(subRepo *SubscriptionRepository) {
	s.subRepo = subRepo
}

// SetGroupPolicyProvider wires access-group policy composition into download
// checks. Nil keeps legacy user-row behavior.
func (s *Service) SetGroupPolicyProvider(provider access.GroupPolicyProvider) {
	s.groupProvider = provider
}

// Config returns the current (live, cache-refreshed) download config. Used by
// the artifact worker to read non-restart settings.
func (s *Service) Config(ctx context.Context) config.DownloadConfig {
	return s.loadConfig(ctx)
}

// NewService creates a new download service with the given dependencies.
func NewService(
	repo *Repository,
	bandwidth *BandwidthManager,
	limiter *QuantityLimiter,
	fileRepo FileResolver,
	itemRepo ItemResolver,
	episodeRepo EpisodeResolver,
	userRepo UserResolver,
	itemAccess ItemAccessChecker,
	settings SettingsReader,
	initialCfg *config.DownloadConfig,
) *Service {
	s := &Service{
		repo:        repo,
		bandwidth:   bandwidth,
		limiter:     limiter,
		fileRepo:    fileRepo,
		itemRepo:    itemRepo,
		episodeRepo: episodeRepo,
		userRepo:    userRepo,
		itemAccess:  itemAccess,
		settings:    settings,
	}
	if initialCfg != nil {
		s.cfg = *initialCfg
		s.cfgLoadedAt = time.Now()
	}
	return s
}

// loadConfig returns the current download config, refreshing from DB if stale.
func (s *Service) loadConfig(ctx context.Context) config.DownloadConfig {
	s.cfgMu.RLock()
	if time.Since(s.cfgLoadedAt) < configCacheTTL {
		cfg := s.cfg
		s.cfgMu.RUnlock()
		return cfg
	}
	s.cfgMu.RUnlock()

	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()

	// Double-check after acquiring write lock.
	if time.Since(s.cfgLoadedAt) < configCacheTTL {
		return s.cfg
	}

	if s.settings == nil {
		return s.cfg
	}

	allSettings, err := s.settings.GetAll(ctx)
	if err != nil {
		slog.Warn("failed to reload download config from DB, using cached", "error", err)
		return s.cfg
	}

	newFullCfg, err := config.LoadFromDB(allSettings)
	if err != nil {
		slog.Warn("failed to parse download config from DB, using cached", "error", err)
		return s.cfg
	}

	oldCfg := s.cfg
	s.cfg = newFullCfg.Download
	s.cfgLoadedAt = time.Now()

	// Update bandwidth manager if limits changed.
	if s.bandwidth != nil && (oldCfg.ServerBandwidthBPS != s.cfg.ServerBandwidthBPS || oldCfg.UserBandwidthBPS != s.cfg.UserBandwidthBPS) {
		s.bandwidth.Reload(s.cfg.ServerBandwidthBPS, s.cfg.UserBandwidthBPS)
		slog.Info("download bandwidth config reloaded", "server_bps", s.cfg.ServerBandwidthBPS, "user_bps", s.cfg.UserBandwidthBPS)
	}

	// Update quantity limiter if limits changed.
	if s.limiter != nil && (oldCfg.MaxConcurrentPerUser != s.cfg.MaxConcurrentPerUser || oldCfg.MaxPerPeriod != s.cfg.MaxPerPeriod || oldCfg.PeriodDuration != s.cfg.PeriodDuration) {
		s.limiter.Reload(s.cfg.MaxConcurrentPerUser, s.cfg.MaxPerPeriod, s.cfg.PeriodDuration)
		slog.Info("download quantity limits reloaded", "max_concurrent", s.cfg.MaxConcurrentPerUser, "max_per_period", s.cfg.MaxPerPeriod, "period", s.cfg.PeriodDuration)
	}

	return s.cfg
}

// enabledConfig returns the current download config, or ErrFeatureDisabled when
// downloads are turned off server-wide.
func (s *Service) enabledConfig(ctx context.Context) (config.DownloadConfig, error) {
	cfg := s.loadConfig(ctx)
	if !cfg.Enabled {
		return cfg, ErrFeatureDisabled
	}
	return cfg, nil
}

// Capability reports the download capability for a user (feature detection).
func (s *Service) Capability(ctx context.Context, userID int) (Capability, error) {
	cfg := s.loadConfig(ctx)
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return Capability{}, fmt.Errorf("loading user: %w", err)
	}
	user, err = s.effectiveDownloadUser(ctx, user)
	if err != nil {
		return Capability{}, fmt.Errorf("loading access group policy: %w", err)
	}
	c := Capability{
		Enabled:              cfg.Enabled,
		DownloadAllowed:      user.DownloadAllowed,
		QualityPresets:       []string{},
		TranscodeEnabled:     cfg.TranscodeEnabled,
		TranscodeUserAllowed: user.DownloadTranscodeAllowed,
	}
	if s.actionDecider != nil {
		c.QualityPresets = s.policyPresetsFor(ctx, user, cfg, s.artifacts != nil)
	} else {
		c.QualityPresets = s.policy.PresetsFor(user, cfg, s.artifacts != nil)
	}
	if len(c.QualityPresets) > 0 {
		// Per-season download is always available when downloads are enabled;
		// auto-download monitoring additionally requires the subscription repo.
		c.SeasonDownload = true
		if s.subRepo != nil {
			c.SeriesMonitoring = true
			c.MonitoringModes = []string{SubModeAll, SubModeFuture, SubModeLatestSeason, SubModeSpecificSeasons}
		}
	}
	return c, nil
}

func (s *Service) effectiveDownloadUser(ctx context.Context, user *models.User) (*models.User, error) {
	if user == nil {
		return nil, nil
	}
	effective, err := access.EffectivePolicyForUser(ctx, user, s.groupProvider)
	if err != nil {
		return nil, err
	}
	out := *user
	out.LibraryIDs = effective.LibraryIDs
	out.MaxPlaybackQuality = effective.MaxPlaybackQuality
	out.MaxStreams = effective.MaxStreams
	out.MaxTranscodes = effective.MaxTranscodes
	out.Permissions = effective.Permissions
	out.DownloadAllowed = effective.DownloadAllowed
	out.DownloadTranscodeAllowed = effective.DownloadTranscodeAllowed
	return &out, nil
}

// CreateRequest holds the parameters for creating a download. A non-empty
// DeviceID makes it a managed device-library entry; empty is ephemeral/web.
type CreateRequest struct {
	ContentID      string
	EpisodeID      string
	FileID         int
	Quality        string // "" defaults to original
	ProfileID      string // managed identity (X-Profile-Id via viewer access)
	DeviceID       string // "" => ephemeral; set => managed device entry
	DeviceName     string
	DevicePlatform string
	// Caps describes the requesting device's decode capability; used to decide
	// whether original can be delivered directly or needs a compatibility artifact.
	Caps playback.ClientCapabilities
}

// Create creates a download for a single item (movie or episode). For
// public `original` it registers an idempotent managed entry or queues an
// ephemeral row unless compatibility requires a prepared artifact. Bitrate
// qualities always prepare a transcode artifact before the row becomes ready.
func (s *Service) Create(ctx context.Context, userID int, req CreateRequest, filter catalog.AccessFilter) (*Download, error) {
	cfg, user, err := s.downloadConfigForUser(ctx, userID, req.DeviceID)
	if err != nil {
		return nil, err
	}
	file, err := s.resolveFile(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := s.itemAccess.EnsureAccessible(ctx, file.ContentID, filter); err != nil {
		return nil, err
	}

	decision, err := s.policy.Resolve(ctx, req.Quality, user, cfg, file, req.Caps, s.artifacts != nil, req.DeviceID)
	if err != nil {
		return nil, err
	}
	if decision.RequiresArtifact {
		return s.createArtifactDownload(ctx, userID, req, file, decision)
	}

	if req.DeviceID != "" {
		rows, err := s.ensureManaged(ctx, userID, req, []managedItem{{file: file, contentID: file.ContentID, episodeID: file.EpisodeID}}, decision, "")
		if err != nil {
			return nil, err
		}
		return rows[0], nil
	}

	var d *Download
	err = s.repo.WithUserQuotaLock(ctx, userID, func(ctx context.Context) error {
		if err := s.limiter.Check(ctx, userID, 1); err != nil {
			return err
		}
		id, err := idgen.NextID()
		if err != nil {
			return fmt.Errorf("generating download ID: %w", err)
		}
		now := time.Now()
		d = &Download{
			ID:               id,
			UserID:           userID,
			MediaFileID:      file.ID,
			ContentID:        file.ContentID,
			EpisodeID:        file.EpisodeID,
			Kind:             KindQueued,
			Status:           StatusQueued,
			Format:           FormatOriginal,
			Quality:          QualityOriginal,
			EffectiveQuality: QualityOriginal,
			Revision:         1,
			FileSize:         file.FileSize,
			CreatedAt:        now,
			UpdatedAt:        now,
		}
		return s.repo.Create(ctx, d)
	})
	if err != nil {
		return nil, err
	}
	return d, nil
}

// createArtifactDownload ensures a prepared (remux/transcode) artifact for file
// and creates the linked download row — ready when the artifact already exists,
// otherwise preparing until the encode worker completes it. Handles both managed
// (idempotent per device) and ephemeral rows.
func (s *Service) createArtifactDownload(ctx context.Context, userID int, req CreateRequest, file *models.MediaFile, decision QualityDecision) (*Download, error) {
	managed := req.DeviceID != ""
	if managed && req.ProfileID == "" {
		return nil, ErrProfileRequired
	}

	// Resolve any existing managed entry first: replacing one doesn't add a
	// row, so it stays quota-exempt and needs no quota lock.
	var existing *Download
	if managed {
		ex, err := s.repo.GetManagedEntry(ctx, userID, req.ProfileID, req.DeviceID, file.ContentID, file.EpisodeID)
		switch {
		case err == nil:
			existing = ex
		case !errors.Is(err, ErrNotFound):
			return nil, err
		}
	}
	if existing != nil {
		artifact, err := s.artifacts.Ensure(ctx, file, decision.DeliveryFormat, decision.PrepareTarget)
		if err != nil {
			return nil, err
		}
		status, size := artifactRowStatus(artifact, file)
		replacement := buildManagedDownload(userID, req.ProfileID, req.DeviceID, managedItem{file: file, contentID: file.ContentID, episodeID: file.EpisodeID}, decision, "", status, size, artifact.ID)
		return s.reuseOrReplaceManaged(ctx, existing, replacement)
	}

	// New row: the quota lock serializes check + insert across concurrent
	// creates so they cannot all observe free quota before any row exists. The
	// limiter must still pass BEFORE artifacts.Ensure — a rejected request must
	// not leave an encode job behind (the worker would run it even though the
	// caller saw 429) — so the lock spans Ensure too.
	var d *Download
	err := s.repo.WithUserQuotaLock(ctx, userID, func(ctx context.Context) error {
		if err := s.limiter.Check(ctx, userID, 1); err != nil {
			return err
		}
		artifact, err := s.artifacts.Ensure(ctx, file, decision.DeliveryFormat, decision.PrepareTarget)
		if err != nil {
			return err
		}
		status, size := artifactRowStatus(artifact, file)

		if managed {
			if err := s.repo.EnsureDevice(ctx, userID, req.ProfileID, req.DeviceID, req.DeviceName, req.DevicePlatform); err != nil {
				return err
			}
		}

		id, err := idgen.NextID()
		if err != nil {
			return fmt.Errorf("generating download ID: %w", err)
		}
		now := time.Now()
		d = &Download{
			ID:                id,
			UserID:            userID,
			MediaFileID:       file.ID,
			ContentID:         file.ContentID,
			EpisodeID:         file.EpisodeID,
			Kind:              KindQueued,
			Status:            status,
			Format:            decision.DeliveryFormat,
			Quality:           decision.RequestedQuality,
			EffectiveQuality:  decision.EffectiveQuality,
			TargetBitrateKbps: decision.TargetBitrateKbps,
			Revision:          1,
			ArtifactID:        artifact.ID,
			FileSize:          size,
			CreatedAt:         now,
			UpdatedAt:         now,
		}
		if managed {
			d.ProfileID = req.ProfileID
			d.DeviceID = req.DeviceID
		}
		if err := s.repo.Create(ctx, d); err != nil {
			if managed {
				if existing, gerr := s.repo.GetManagedEntry(ctx, userID, req.ProfileID, req.DeviceID, file.ContentID, file.EpisodeID); gerr == nil {
					replacement := buildManagedDownload(userID, req.ProfileID, req.DeviceID, managedItem{file: file, contentID: file.ContentID, episodeID: file.EpisodeID}, decision, "", status, size, artifact.ID)
					row, rerr := s.reuseOrReplaceManaged(ctx, existing, replacement)
					if rerr != nil {
						return rerr
					}
					d = row
					return nil
				}
			}
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return d, nil
}

// artifactRowStatus maps an ensured artifact to the download row status and
// recorded size: ready artifacts serve immediately, anything else is preparing.
func artifactRowStatus(artifact *Artifact, file *models.MediaFile) (string, int64) {
	if artifact.Status == ArtifactReady {
		return StatusReady, artifact.FileSize
	}
	return StatusPreparing, file.FileSize
}

// CreateSeries creates download records for every episode in a series. Managed
// (req.DeviceID set) registers one idempotent managed entry per episode;
// ephemeral queues them as before. Returns the rows and a shared batch ID.
func (s *Service) CreateSeries(ctx context.Context, userID int, req CreateRequest, filter catalog.AccessFilter) ([]*Download, string, []SkippedDownload, error) {
	return s.createSeriesScoped(ctx, userID, req, filter, func(ctx context.Context) ([]*models.Episode, error) {
		return s.episodeRepo.ListBySeries(ctx, req.ContentID)
	})
}

// CreateSeason creates download records for every episode in a single season of
// a series. It shares CreateSeries' managed/ephemeral behavior, shared batch ID,
// and original-only restriction; only the episode set differs. seasonNumber 0
// is the Specials season (the handler routes here whenever one is supplied).
func (s *Service) CreateSeason(ctx context.Context, userID int, req CreateRequest, seasonNumber int, filter catalog.AccessFilter) ([]*Download, string, []SkippedDownload, error) {
	return s.createSeriesScoped(ctx, userID, req, filter, func(ctx context.Context) ([]*models.Episode, error) {
		return s.episodeRepo.ListBySeason(ctx, req.ContentID, seasonNumber)
	})
}

// createSeriesScoped is the shared body of CreateSeries/CreateSeason: it runs the
// permission/quality/access checks, resolves the episode set via listEpisodes,
// picks the best file per episode, and registers managed entries (device set) or
// queues ephemeral rows under one shared batch ID. Series/season downloads are
// original-only.
func (s *Service) createSeriesScoped(ctx context.Context, userID int, req CreateRequest, filter catalog.AccessFilter, listEpisodes func(context.Context) ([]*models.Episode, error)) ([]*Download, string, []SkippedDownload, error) {
	cfg, user, err := s.downloadConfigForUser(ctx, userID, req.DeviceID)
	if err != nil {
		return nil, "", nil, err
	}
	decision, err := s.resolveBulkQuality(req.Quality, user, cfg)
	if err != nil {
		return nil, "", nil, err
	}

	item, err := s.itemRepo.GetByID(ctx, req.ContentID)
	if err != nil {
		return nil, "", nil, fmt.Errorf("loading series: %w", err)
	}
	if item.Type != "series" {
		return nil, "", nil, ErrNotSeries
	}
	if err := s.itemAccess.EnsureAccessible(ctx, req.ContentID, filter); err != nil {
		return nil, "", nil, err
	}

	episodes, err := listEpisodes(ctx)
	if err != nil {
		return nil, "", nil, fmt.Errorf("listing episodes: %w", err)
	}

	items, skipped, err := s.episodeItemsWithSkipped(ctx, req.ContentID, episodes)
	if err != nil {
		return nil, "", nil, err
	}
	if len(items) == 0 {
		return nil, "", skipped, ErrNoDownloadableEpisodes
	}

	batchID, err := idgen.NextID()
	if err != nil {
		return nil, "", nil, fmt.Errorf("generating batch ID: %w", err)
	}

	if req.DeviceID != "" {
		rows, err := s.ensureManaged(ctx, userID, req, items, decision, batchID)
		if err != nil {
			return nil, "", nil, err
		}
		return rows, batchID, skipped, nil
	}

	now := time.Now()
	dls := make([]*Download, 0, len(items))
	for _, it := range items {
		id, err := idgen.NextID()
		if err != nil {
			return nil, "", nil, fmt.Errorf("generating download ID: %w", err)
		}
		dls = append(dls, &Download{
			ID:               id,
			UserID:           userID,
			MediaFileID:      it.file.ID,
			ContentID:        it.contentID,
			EpisodeID:        it.episodeID,
			BatchID:          batchID,
			Kind:             KindQueued,
			Status:           StatusQueued,
			Format:           decision.DeliveryFormat,
			Quality:          decision.RequestedQuality,
			EffectiveQuality: decision.EffectiveQuality,
			Revision:         1,
			FileSize:         it.file.FileSize,
			CreatedAt:        now,
			UpdatedAt:        now,
		})
	}
	if err := s.repo.WithUserQuotaLock(ctx, userID, func(ctx context.Context) error {
		if err := s.limiter.Check(ctx, userID, len(dls)); err != nil {
			return err
		}
		return s.repo.CreateBatch(ctx, dls)
	}); err != nil {
		return nil, "", nil, err
	}
	return dls, batchID, skipped, nil
}

// episodeItems resolves the best downloadable file per episode into managedItems,
// skipping episodes that have no file. It batches the file lookup into one query
// (not one per episode) and preserves episode order. Shared by series/season
// downloads and subscription backfill so file selection stays identical.
func (s *Service) episodeItems(ctx context.Context, seriesID string, episodes []*models.Episode) ([]managedItem, error) {
	items, _, err := s.episodeItemsWithSkipped(ctx, seriesID, episodes)
	return items, err
}

func (s *Service) episodeItemsWithSkipped(ctx context.Context, seriesID string, episodes []*models.Episode) ([]managedItem, []SkippedDownload, error) {
	if len(episodes) == 0 {
		return nil, nil, nil
	}
	episodeIDs := make([]string, len(episodes))
	for i, ep := range episodes {
		episodeIDs[i] = ep.ContentID
	}
	filesByEpisode, err := s.fileRepo.ListByEpisodeIDs(ctx, episodeIDs)
	if err != nil {
		return nil, nil, fmt.Errorf("resolving files for %d episodes: %w", len(episodes), err)
	}
	items := make([]managedItem, 0, len(episodes))
	skipped := make([]SkippedDownload, 0)
	for _, ep := range episodes {
		files := filesByEpisode[ep.ContentID]
		if len(files) == 0 {
			skipped = append(skipped, SkippedDownload{EpisodeID: ep.ContentID, Reason: "no_file"})
			continue
		}
		items = append(items, managedItem{file: pickBestFile(files), contentID: seriesID, episodeID: ep.ContentID})
	}
	return items, skipped, nil
}

// managedItem pairs a resolved file with the (content, episode) identity its
// managed entry is keyed on.
type managedItem struct {
	file      *models.MediaFile
	contentID string
	episodeID string
}

// ensureManaged idempotently registers managed entries for the given items,
// preserving input order. New entries consume quota; existing entries are reused
// when the target is unchanged, revived when terminal, or replaced when the file
// or quality target changed. The device is upserted into user_devices so the
// composite FK holds. Original entries are created ready-to-serve.
func (s *Service) ensureManaged(ctx context.Context, userID int, req CreateRequest, items []managedItem, decision QualityDecision, batchID string) ([]*Download, error) {
	if req.ProfileID == "" {
		return nil, ErrProfileRequired
	}
	if err := s.repo.EnsureDevice(ctx, userID, req.ProfileID, req.DeviceID, req.DeviceName, req.DevicePlatform); err != nil {
		return nil, err
	}

	keys := make([]ManagedEntryKey, len(items))
	for i, it := range items {
		keys[i] = ManagedEntryKey{ContentID: it.contentID, EpisodeID: it.episodeID}
	}
	existing, err := s.repo.GetManagedEntriesByKeys(ctx, userID, req.ProfileID, req.DeviceID, keys)
	if err != nil {
		return nil, err
	}
	results := make([]*Download, len(items))
	var newIdx []int
	for i, it := range items {
		if ex, ok := existing[keys[i]]; ok {
			replacement := buildManagedDownload(userID, req.ProfileID, req.DeviceID, it, decision, batchID, StatusReady, it.file.FileSize, "")
			row, err := s.reuseOrReplaceManaged(ctx, ex, replacement)
			if err != nil {
				return nil, err
			}
			results[i] = row
			continue
		}
		newIdx = append(newIdx, i)
	}
	if len(newIdx) == 0 {
		return results, nil
	}
	var inserted []*Download
	if err := s.repo.WithUserQuotaLock(ctx, userID, func(ctx context.Context) error {
		if err := s.limiter.Check(ctx, userID, len(newIdx)); err != nil {
			return err
		}
		toInsert := make([]*Download, 0, len(newIdx))
		for _, i := range newIdx {
			d, err := buildManagedOriginal(userID, req.ProfileID, req.DeviceID, items[i], decision, batchID)
			if err != nil {
				return err
			}
			toInsert = append(toInsert, d)
		}
		rows, err := s.repo.CreateManagedEntriesBatch(ctx, toInsert)
		if err != nil {
			return err
		}
		inserted = rows
		return nil
	}); err != nil {
		return nil, err
	}
	byKey := make(map[ManagedEntryKey]*Download, len(inserted))
	for _, d := range inserted {
		byKey[ManagedEntryKey{ContentID: d.ContentID, EpisodeID: d.EpisodeID}] = d
	}
	for _, i := range newIdx {
		if row, ok := byKey[keys[i]]; ok {
			results[i] = row
			continue
		}
		// A concurrent create won this identity between the fetch and the
		// batch insert (ON CONFLICT skipped it); return the winning row.
		row, err := s.repo.GetManagedEntry(ctx, userID, req.ProfileID, req.DeviceID, items[i].contentID, items[i].episodeID)
		if err != nil {
			return nil, err
		}
		results[i] = row
	}
	return results, nil
}

// buildManagedOriginal constructs a ready managed original entry for one item
// with a fresh ID. Shared by the interactive series flow and subscription
// backfill so every original row is built the same.
func buildManagedOriginal(userID int, profileID, deviceID string, it managedItem, decision QualityDecision, batchID string) (*Download, error) {
	id, err := idgen.NextID()
	if err != nil {
		return nil, fmt.Errorf("generating download ID: %w", err)
	}
	d := buildManagedDownload(userID, profileID, deviceID, it, decision, batchID, StatusReady, it.file.FileSize, "")
	d.ID = id
	d.CreatedAt = time.Now()
	d.UpdatedAt = d.CreatedAt
	d.Revision = 1
	return d, nil
}

func buildManagedDownload(userID int, profileID, deviceID string, it managedItem, decision QualityDecision, batchID, status string, fileSize int64, artifactID string) *Download {
	now := time.Now()
	return &Download{
		UserID:            userID,
		ProfileID:         profileID,
		DeviceID:          deviceID,
		MediaFileID:       it.file.ID,
		ContentID:         it.contentID,
		EpisodeID:         it.episodeID,
		BatchID:           batchID,
		Kind:              KindQueued,
		Status:            status,
		Format:            decision.DeliveryFormat,
		Quality:           decision.RequestedQuality,
		EffectiveQuality:  decision.EffectiveQuality,
		TargetBitrateKbps: decision.TargetBitrateKbps,
		Revision:          1,
		ArtifactID:        artifactID,
		FileSize:          fileSize,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
}

func (s *Service) reuseOrReplaceManaged(ctx context.Context, existing, replacement *Download) (*Download, error) {
	if sameManagedTarget(existing, replacement) {
		if !reusableManagedStatus(existing.Status) {
			return s.repo.ReplaceManagedEntry(ctx, existing, replacement)
		}
		if replacement.BatchID != "" && existing.BatchID != replacement.BatchID {
			return s.repo.UpdateManagedBatch(ctx, existing, replacement.BatchID)
		}
		return existing, nil
	}
	return s.repo.ReplaceManagedEntry(ctx, existing, replacement)
}

func reusableManagedStatus(status string) bool {
	switch status {
	case StatusCancelled, StatusFailed, StatusRevoked:
		return false
	default:
		return true
	}
}

func sameManagedTarget(a, b *Download) bool {
	return a.MediaFileID == b.MediaFileID &&
		a.Format == b.Format &&
		a.Quality == b.Quality &&
		a.EffectiveQuality == b.EffectiveQuality &&
		a.TargetBitrateKbps == b.TargetBitrateKbps &&
		a.ArtifactID == b.ArtifactID &&
		a.FileSize == b.FileSize
}

// registerManagedItems idempotently registers each item as a ready original
// managed entry for (userID, profileID, deviceID) with one batched fetch and
// one batched insert, skipping items that already exist. The device row must
// already exist (composite FK). Unlike the interactive ensureManaged path it
// does NOT consume the QuantityLimiter — the subscription is the
// authorization. Returns only the NEWLY registered rows: the sync response's
// "registered" count is documented as new episodes, so a steady-state sync
// must report 0, not the full in-scope set.
func registerManagedItems(ctx context.Context, repo *Repository, userID int, profileID, deviceID string, items []managedItem, batchID string) ([]*Download, error) {
	if len(items) == 0 {
		return nil, nil
	}
	keys := make([]ManagedEntryKey, len(items))
	for i, it := range items {
		keys[i] = ManagedEntryKey{ContentID: it.contentID, EpisodeID: it.episodeID}
	}
	existing, err := repo.GetManagedEntriesByKeys(ctx, userID, profileID, deviceID, keys)
	if err != nil {
		return nil, err
	}
	toInsert := make([]*Download, 0, len(items))
	for i, it := range items {
		if _, ok := existing[keys[i]]; ok {
			continue
		}
		d, err := buildManagedOriginal(userID, profileID, deviceID, it, originalDecision(), batchID)
		if err != nil {
			return nil, err
		}
		toInsert = append(toInsert, d)
	}
	return repo.CreateManagedEntriesBatch(ctx, toInsert)
}

// List returns the calling device's managed entries, or the user's
// ephemeral/account-level rows when no device header is present.
func (s *Service) List(ctx context.Context, userID int, profileID, deviceID string) ([]*Download, error) {
	if deviceID != "" {
		if profileID == "" {
			return nil, ErrProfileRequired
		}
		return s.repo.ListManaged(ctx, userID, profileID, deviceID)
	}
	return s.repo.ListEphemeral(ctx, userID)
}

// ServeDirect validates permissions and serves a file directly for browser
// download. No persistent download record is created.
func (s *Service) ServeDirect(ctx context.Context, w http.ResponseWriter, r *http.Request, userID, fileID int, format string, filter catalog.AccessFilter) error {
	if _, _, err := s.downloadConfigForUser(ctx, userID, ""); err != nil {
		return err
	}
	if format != "" && format != FormatOriginal {
		return ErrFormatUnavailable
	}
	file, err := s.fileRepo.GetByID(ctx, fileID)
	if err != nil {
		return fmt.Errorf("loading media file: %w", err)
	}
	if file == nil || file.MissingSince != nil {
		return catalog.ErrItemNotFound
	}
	if err := s.itemAccess.EnsureAccessible(ctx, file.ContentID, filter); err != nil {
		return err
	}
	if !catalog.FileAllowedByAccess(file, filter) {
		return catalog.ErrItemNotFound
	}
	return s.serveLocalFile(ctx, w, r, file.FilePath, userID)
}

// ServeFile serves a download's file. Managed entries (device header present)
// authorize on (user, profile, device) and re-check per-profile content access
// before serving; ephemeral rows keep today's queued→downloading→completed
// behavior. The ephemeral path never serves a managed row, and vice versa.
func (s *Service) ServeFile(ctx context.Context, w http.ResponseWriter, r *http.Request, userID int, profileID, deviceID, downloadID string, filter catalog.AccessFilter) error {
	// Re-check policy — admin may have disabled downloads or revoked permission.
	if _, _, err := s.downloadConfigForUser(ctx, userID, deviceID); err != nil {
		return err
	}

	if deviceID != "" {
		return s.serveManaged(ctx, w, r, userID, profileID, deviceID, downloadID, filter)
	}

	dl, err := s.repo.GetByID(ctx, downloadID)
	if err != nil {
		return err
	}
	if dl.UserID != userID || dl.IsManaged() {
		return ErrNotFound // don't reveal existence; ephemeral path never serves managed rows
	}
	if dl.Status == StatusCancelled || dl.Status == StatusFailed {
		return fmt.Errorf("download is %s: %w", dl.Status, ErrDownloadNotActive)
	}

	if dl.Status == StatusPreparing {
		return fmt.Errorf("download is preparing: %w", ErrDownloadNotActive)
	}

	// Atomically transition queued → downloading for original rows. Artifact
	// (remux/transcode) rows are already ready by the time bytes are served.
	if dl.Format == FormatOriginal && dl.Status == StatusQueued {
		if err := s.repo.TransitionStatus(ctx, dl.ID, StatusQueued, StatusDownloading, 0, nil); err != nil {
			if errors.Is(err, ErrStatusConflict) {
				return fmt.Errorf("download already in progress: %w", ErrDownloadNotActive)
			}
			slog.Warn("failed to transition download to downloading", "download_id", dl.ID, "error", err)
		}
	}

	if err := s.serveDownloadBytes(ctx, w, r, dl, userID, filter); err != nil {
		if dl.Format == FormatOriginal {
			if updateErr := s.repo.UpdateStatus(ctx, dl.ID, StatusFailed, 0, nil); updateErr != nil {
				slog.Error("failed to mark download as failed", "download_id", dl.ID, "error", updateErr)
			}
		}
		return err
	}

	now := time.Now()
	if err := s.repo.UpdateStatus(ctx, dl.ID, StatusCompleted, dl.FileSize, &now); err != nil {
		slog.Error("failed to mark download as completed", "download_id", dl.ID, "error", err)
	}
	return nil
}

// serveManaged authorizes a managed entry on (user, profile, device), re-checks
// per-profile content access (invariant 2), and streams the original source.
func (s *Service) serveManaged(ctx context.Context, w http.ResponseWriter, r *http.Request, userID int, profileID, deviceID, downloadID string, filter catalog.AccessFilter) error {
	if profileID == "" {
		return ErrProfileRequired
	}
	dl, err := s.repo.GetManagedByID(ctx, downloadID, userID, profileID, deviceID)
	if err != nil {
		return err
	}
	if dl.Status == StatusRevoked {
		return fmt.Errorf("download is revoked: %w", ErrDownloadNotActive)
	}
	// A download id alone never authorizes access: re-check the requesting
	// profile's content/library scope before serving any bytes.
	if err := s.itemAccess.EnsureAccessible(ctx, dl.ContentID, filter); err != nil {
		return err
	}
	return s.serveDownloadBytes(ctx, w, r, dl, userID, filter)
}

// PatchStatus lets a client confirm a managed entry's local state
// (downloading/completed), authorized on (user, profile, device).
func (s *Service) PatchStatus(ctx context.Context, userID int, profileID, deviceID, downloadID, status string) error {
	if deviceID == "" || profileID == "" {
		return ErrProfileRequired
	}
	switch status {
	case StatusDownloading, StatusCompleted:
	default:
		return ErrInvalidStatus
	}
	var completedAt *time.Time
	if status == StatusCompleted {
		now := time.Now()
		completedAt = &now
	}
	return s.repo.UpdateManagedStatus(ctx, downloadID, userID, profileID, deviceID, status, completedAt)
}

// Delete removes a managed entry (authorized on user, profile, device) or
// cancels/deletes an ephemeral row. Each path is scoped to its own row mode so
// neither can touch the other's rows.
func (s *Service) Delete(ctx context.Context, userID int, profileID, deviceID, downloadID string) error {
	if deviceID != "" {
		if profileID == "" {
			return ErrProfileRequired
		}
		return s.repo.DeleteManaged(ctx, downloadID, userID, profileID, deviceID)
	}

	dl, err := s.repo.GetByID(ctx, downloadID)
	if err != nil {
		return err
	}
	if dl.UserID != userID || dl.IsManaged() {
		return ErrNotFound
	}
	switch dl.Status {
	case StatusQueued, StatusDownloading:
		return s.repo.CancelByID(ctx, downloadID, userID)
	default:
		return s.repo.Delete(ctx, downloadID, userID)
	}
}

func (s *Service) resolveBulkQuality(requested string, _ *models.User, _ config.DownloadConfig) (QualityDecision, error) {
	quality := normalizeQuality(requested)
	if !ValidQuality(quality) {
		return QualityDecision{}, ErrInvalidQuality
	}
	if quality != QualityOriginal {
		return QualityDecision{}, ErrBulkQualityUnavailable
	}
	return originalDecision(), nil
}

func originalDecision() QualityDecision {
	return QualityDecision{
		RequestedQuality: QualityOriginal,
		EffectiveQuality: QualityOriginal,
		DeliveryFormat:   FormatOriginal,
	}
}

func (s *Service) resolveFile(ctx context.Context, req CreateRequest) (*models.MediaFile, error) {
	if req.FileID > 0 {
		file, err := s.fileRepo.GetByID(ctx, req.FileID)
		if err != nil {
			return nil, fmt.Errorf("loading media file: %w", err)
		}
		if file == nil || file.MissingSince != nil {
			return nil, catalog.ErrItemNotFound
		}
		return file, nil
	}

	var files []*models.MediaFile
	var err error
	if req.EpisodeID != "" {
		files, err = s.fileRepo.GetByEpisodeID(ctx, req.EpisodeID)
	} else {
		files, err = s.fileRepo.GetByContentID(ctx, req.ContentID)
	}
	if err != nil {
		return nil, fmt.Errorf("resolving files: %w", err)
	}
	if len(files) == 0 {
		return nil, catalog.ErrItemNotFound
	}

	return pickBestFile(files), nil
}

// serveDownloadBytes serves the bytes for a download row: the prepared artifact
// for remux/transcode rows (which must be ready), or the source media file for
// original rows. Both paths mirror playback's per-file authorization
// (catalog.FileAllowedByAccess): library scope and the profile's max playback
// quality can change after a row was registered.
func (s *Service) serveDownloadBytes(ctx context.Context, w http.ResponseWriter, r *http.Request, dl *Download, userID int, filter catalog.AccessFilter) error {
	file, err := s.fileRepo.GetByID(ctx, dl.MediaFileID)
	if err != nil {
		return fmt.Errorf("loading media file: %w", err)
	}
	if file == nil {
		return catalog.ErrItemNotFound
	}
	if dl.Format != FormatOriginal && dl.ArtifactID != "" {
		if s.artifacts == nil {
			return ErrFormatUnavailable
		}
		artifact, err := s.artifacts.Ready(ctx, dl.ArtifactID)
		if err != nil {
			return err
		}
		// The quality ceiling applies to what is actually served — the prepared
		// artifact's resolution, not the source's (a 720p transcode of a 4K
		// source must stay downloadable under a 1080p ceiling).
		served := *file
		if artifact.Resolution != "" {
			served.Resolution = artifact.Resolution
		}
		if !catalog.FileAllowedByAccess(&served, filter) {
			return catalog.ErrItemNotFound
		}
		return s.serveLocalFile(ctx, w, r, artifact.OutputPath, userID)
	}
	if file.MissingSince != nil {
		return catalog.ErrItemNotFound
	}
	if !catalog.FileAllowedByAccess(file, filter) {
		return catalog.ErrItemNotFound
	}
	return s.serveLocalFile(ctx, w, r, file.FilePath, userID)
}

func (s *Service) serveLocalFile(ctx context.Context, w http.ResponseWriter, r *http.Request, path string, userID int) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return catalog.ErrItemNotFound
		}
		return fmt.Errorf("opening file: %w", err)
	}
	defer func() { _ = f.Close() }()

	stat, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat file: %w", err)
	}

	filename := sanitizeFilename(filepath.Base(path))
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("Content-Type", playback.MimeFromExtension(path))

	var reader io.ReadSeeker = f
	if s.bandwidth != nil {
		reader = s.bandwidth.ThrottledReader(ctx, f, userID)
	}

	http.ServeContent(w, r, stat.Name(), stat.ModTime(), reader)
	return nil
}

// pickBestFile selects the highest-resolution file from a list.
func pickBestFile(files []*models.MediaFile) *models.MediaFile {
	if len(files) == 1 {
		return files[0]
	}
	best := files[0]
	for _, f := range files[1:] {
		// access.CompareQuality is the codebase's one resolution ordering
		// (includes 4320p); download file selection must agree with playback.
		if access.CompareQuality(f.Resolution, best.Resolution) > 0 {
			best = f
		}
	}
	return best
}

func sanitizeFilename(name string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', '"', '<', '>', '|', '?', '*', ':':
			return '_'
		}
		return r
	}, name)
}
