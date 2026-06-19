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
}

// ItemResolver looks up media items.
type ItemResolver interface {
	GetByID(ctx context.Context, contentID string) (*models.MediaItem, error)
}

// EpisodeResolver lists episodes for a series.
type EpisodeResolver interface {
	ListBySeries(ctx context.Context, seriesID string) ([]*models.Episode, error)
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
	Formats              []string
	TranscodeEnabled     bool
	TranscodeUserAllowed bool
}

// Service orchestrates download permission checks, quota enforcement, format
// policy, file resolution, and file serving for both ephemeral/account-level
// rows (DeviceID == "") and managed device-library entries (DeviceID set).
type Service struct {
	repo        *Repository
	policy      FormatPolicyResolver
	bandwidth   *BandwidthManager
	limiter     *QuantityLimiter
	fileRepo    FileResolver
	itemRepo    ItemResolver
	episodeRepo EpisodeResolver
	userRepo    UserResolver
	itemAccess  ItemAccessChecker
	settings    SettingsReader

	// Offline-manifest dependencies (Phase 2); nil until SetOfflineDeps wires them.
	manifest       *ManifestBuilder
	subtitleSource SubtitleSource
	artworkSource  ManifestSource
	httpClient     *http.Client

	// Prepare-to-file pipeline (Phase 3); nil until SetArtifactManager wires it.
	artifacts *ArtifactManager

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
	s.manifest = NewManifestBuilder(detail, subs, s.fileRepo)
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
	c := Capability{
		Enabled:              cfg.Enabled,
		DownloadAllowed:      user.DownloadAllowed,
		Formats:              []string{},
		TranscodeEnabled:     cfg.TranscodeEnabled,
		TranscodeUserAllowed: user.DownloadTranscodeAllowed,
	}
	// Advertise only fulfillable formats so a client never requests one it cannot
	// receive: original always; remux/transcode once the prepare-to-file pipeline
	// is wired (transcode additionally gated by the server + per-user flags).
	if cfg.Enabled && user.DownloadAllowed {
		c.Formats = append(c.Formats, FormatOriginal)
		if s.artifacts != nil {
			c.Formats = append(c.Formats, FormatRemux)
			if cfg.TranscodeEnabled && user.DownloadTranscodeAllowed {
				c.Formats = append(c.Formats, FormatTranscode)
			}
		}
	}
	return c, nil
}

// CreateRequest holds the parameters for creating a download. A non-empty
// DeviceID makes it a managed device-library entry; empty is ephemeral/web.
type CreateRequest struct {
	ContentID      string
	EpisodeID      string
	FileID         int
	Format         string // "" defaults to original
	ProfileID      string // managed identity (X-Profile-Id via viewer access)
	DeviceID       string // "" => ephemeral; set => managed device entry
	DeviceName     string
	DevicePlatform string
	// Caps describes the requesting device's decode capability; used to pick the
	// remux/transcode target (ignored for original).
	Caps playback.ClientCapabilities
}

// Create creates a download for a single item (movie or episode). For
// `original` it registers an idempotent managed entry (DeviceID set) or queues
// an ephemeral row. For remux/transcode it ensures a prepared artifact and
// links the row (preparing until the encode completes).
func (s *Service) Create(ctx context.Context, userID int, req CreateRequest, filter catalog.AccessFilter) (*Download, error) {
	cfg, err := s.enabledConfig(ctx)
	if err != nil {
		return nil, err
	}
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("loading user: %w", err)
	}
	if !user.DownloadAllowed {
		return nil, ErrDownloadNotAllowed
	}
	format, err := s.policy.Resolve(req.Format, user, cfg)
	if err != nil {
		return nil, err
	}
	if format != FormatOriginal && s.artifacts == nil {
		return nil, ErrFormatUnavailable // prepare-to-file pipeline not wired
	}
	file, err := s.resolveFile(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := s.itemAccess.EnsureAccessible(ctx, file.ContentID, filter); err != nil {
		return nil, err
	}

	if format != FormatOriginal {
		return s.createArtifactDownload(ctx, userID, req, file, format, cfg)
	}

	if req.DeviceID != "" {
		rows, err := s.ensureManaged(ctx, userID, req, []managedItem{{file: file, contentID: file.ContentID, episodeID: file.EpisodeID}}, FormatOriginal, "")
		if err != nil {
			return nil, err
		}
		return rows[0], nil
	}

	if err := s.limiter.Check(ctx, userID, 1); err != nil {
		return nil, err
	}
	id, err := idgen.NextID()
	if err != nil {
		return nil, fmt.Errorf("generating download ID: %w", err)
	}
	now := time.Now()
	d := &Download{
		ID:          id,
		UserID:      userID,
		MediaFileID: file.ID,
		ContentID:   file.ContentID,
		EpisodeID:   file.EpisodeID,
		Kind:        KindQueued,
		Status:      StatusQueued,
		Format:      FormatOriginal,
		FileSize:    file.FileSize,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.repo.Create(ctx, d); err != nil {
		return nil, err
	}
	return d, nil
}

// createArtifactDownload ensures a prepared (remux/transcode) artifact for file
// and creates the linked download row — ready when the artifact already exists,
// otherwise preparing until the encode worker completes it. Handles both managed
// (idempotent per device) and ephemeral rows.
func (s *Service) createArtifactDownload(ctx context.Context, userID int, req CreateRequest, file *models.MediaFile, format string, cfg config.DownloadConfig) (*Download, error) {
	managed := req.DeviceID != ""
	if managed && req.ProfileID == "" {
		return nil, ErrProfileRequired
	}

	target := playback.ResolvePrepareTarget(file, format, req.Caps, playback.AdminSettings{
		TranscodeEnabled: cfg.TranscodeEnabled,
		Allow4KTranscode: true,
	})
	artifact, err := s.artifacts.Ensure(ctx, file, format, target)
	if err != nil {
		return nil, err
	}

	status := StatusPreparing
	size := file.FileSize
	if artifact.Status == ArtifactReady {
		status = StatusReady
		size = artifact.FileSize
	}

	if managed {
		if existing, err := s.repo.GetManagedEntry(ctx, userID, req.ProfileID, req.DeviceID, file.ContentID, file.EpisodeID); err == nil {
			return existing, nil // idempotent re-register
		} else if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		if err := s.repo.EnsureDevice(ctx, userID, req.ProfileID, req.DeviceID, req.DeviceName, req.DevicePlatform); err != nil {
			return nil, err
		}
	}
	if err := s.limiter.Check(ctx, userID, 1); err != nil {
		return nil, err
	}

	id, err := idgen.NextID()
	if err != nil {
		return nil, fmt.Errorf("generating download ID: %w", err)
	}
	now := time.Now()
	d := &Download{
		ID:          id,
		UserID:      userID,
		MediaFileID: file.ID,
		ContentID:   file.ContentID,
		EpisodeID:   file.EpisodeID,
		Kind:        KindQueued,
		Status:      status,
		Format:      format,
		ArtifactID:  artifact.ID,
		FileSize:    size,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if managed {
		d.ProfileID = req.ProfileID
		d.DeviceID = req.DeviceID
	}
	if err := s.repo.Create(ctx, d); err != nil {
		if managed {
			if existing, gerr := s.repo.GetManagedEntry(ctx, userID, req.ProfileID, req.DeviceID, file.ContentID, file.EpisodeID); gerr == nil {
				return existing, nil
			}
		}
		return nil, err
	}
	return d, nil
}

// CreateSeries creates download records for every episode in a series. Managed
// (req.DeviceID set) registers one idempotent managed entry per episode;
// ephemeral queues them as before. Returns the rows and a shared batch ID.
func (s *Service) CreateSeries(ctx context.Context, userID int, req CreateRequest, filter catalog.AccessFilter) ([]*Download, string, error) {
	cfg, err := s.enabledConfig(ctx)
	if err != nil {
		return nil, "", err
	}
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, "", fmt.Errorf("loading user: %w", err)
	}
	if !user.DownloadAllowed {
		return nil, "", ErrDownloadNotAllowed
	}
	format, err := s.resolveFormat(req.Format, user, cfg)
	if err != nil {
		return nil, "", err
	}

	item, err := s.itemRepo.GetByID(ctx, req.ContentID)
	if err != nil {
		return nil, "", fmt.Errorf("loading series: %w", err)
	}
	if item.Type != "series" {
		return nil, "", fmt.Errorf("content_id is not a series")
	}
	if err := s.itemAccess.EnsureAccessible(ctx, req.ContentID, filter); err != nil {
		return nil, "", err
	}

	episodes, err := s.episodeRepo.ListBySeries(ctx, req.ContentID)
	if err != nil {
		return nil, "", fmt.Errorf("listing episodes: %w", err)
	}

	var items []managedItem
	for _, ep := range episodes {
		files, err := s.fileRepo.GetByEpisodeID(ctx, ep.ContentID)
		if err != nil {
			return nil, "", fmt.Errorf("resolving files for episode %s: %w", ep.ContentID, err)
		}
		if len(files) == 0 {
			continue // skip episodes with no files
		}
		items = append(items, managedItem{file: pickBestFile(files), contentID: req.ContentID, episodeID: ep.ContentID})
	}
	if len(items) == 0 {
		return nil, "", fmt.Errorf("no downloadable episodes found")
	}

	batchID, err := idgen.NextID()
	if err != nil {
		return nil, "", fmt.Errorf("generating batch ID: %w", err)
	}

	if req.DeviceID != "" {
		rows, err := s.ensureManaged(ctx, userID, req, items, format, batchID)
		if err != nil {
			return nil, "", err
		}
		return rows, batchID, nil
	}

	now := time.Now()
	downloads := make([]*Download, 0, len(items))
	for _, it := range items {
		id, err := idgen.NextID()
		if err != nil {
			return nil, "", fmt.Errorf("generating download ID: %w", err)
		}
		downloads = append(downloads, &Download{
			ID:          id,
			UserID:      userID,
			MediaFileID: it.file.ID,
			ContentID:   it.contentID,
			EpisodeID:   it.episodeID,
			BatchID:     batchID,
			Kind:        KindQueued,
			Status:      StatusQueued,
			Format:      format,
			FileSize:    it.file.FileSize,
			CreatedAt:   now,
			UpdatedAt:   now,
		})
	}
	if err := s.limiter.Check(ctx, userID, len(downloads)); err != nil {
		return nil, "", err
	}
	if err := s.repo.CreateBatch(ctx, downloads); err != nil {
		return nil, "", err
	}
	return downloads, batchID, nil
}

// managedItem pairs a resolved file with the (content, episode) identity its
// managed entry is keyed on.
type managedItem struct {
	file      *models.MediaFile
	contentID string
	episodeID string
}

// ensureManaged idempotently registers managed entries for the given items,
// preserving input order. Existing entries are returned untouched; only new
// entries consume quota. The device is upserted into user_devices so the
// composite FK holds. Original entries are created ready-to-serve.
func (s *Service) ensureManaged(ctx context.Context, userID int, req CreateRequest, items []managedItem, format, batchID string) ([]*Download, error) {
	if req.ProfileID == "" {
		return nil, ErrProfileRequired
	}
	if err := s.repo.EnsureDevice(ctx, userID, req.ProfileID, req.DeviceID, req.DeviceName, req.DevicePlatform); err != nil {
		return nil, err
	}

	results := make([]*Download, len(items))
	var newIdx []int
	for i, it := range items {
		existing, err := s.repo.GetManagedEntry(ctx, userID, req.ProfileID, req.DeviceID, it.contentID, it.episodeID)
		switch {
		case err == nil:
			results[i] = existing
		case errors.Is(err, ErrNotFound):
			newIdx = append(newIdx, i)
		default:
			return nil, err
		}
	}
	if len(newIdx) == 0 {
		return results, nil
	}
	if err := s.limiter.Check(ctx, userID, len(newIdx)); err != nil {
		return nil, err
	}

	now := time.Now()
	for _, i := range newIdx {
		it := items[i]
		id, err := idgen.NextID()
		if err != nil {
			return nil, fmt.Errorf("generating download ID: %w", err)
		}
		d := &Download{
			ID:          id,
			UserID:      userID,
			ProfileID:   req.ProfileID,
			DeviceID:    req.DeviceID,
			MediaFileID: it.file.ID,
			ContentID:   it.contentID,
			EpisodeID:   it.episodeID,
			BatchID:     batchID,
			Kind:        KindQueued,
			Status:      StatusReady, // original is immediately servable
			Format:      format,
			FileSize:    it.file.FileSize,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if err := s.repo.Create(ctx, d); err != nil {
			// Lost a race with a concurrent register — return the winning row.
			if existing, gerr := s.repo.GetManagedEntry(ctx, userID, req.ProfileID, req.DeviceID, it.contentID, it.episodeID); gerr == nil {
				results[i] = existing
				continue
			}
			return nil, err
		}
		results[i] = d
	}
	return results, nil
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
	cfg, err := s.enabledConfig(ctx)
	if err != nil {
		return err
	}
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("loading user: %w", err)
	}
	if !user.DownloadAllowed {
		return ErrDownloadNotAllowed
	}
	if _, err := s.resolveFormat(format, user, cfg); err != nil {
		return err
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
	return s.serveLocalFile(ctx, w, r, file.FilePath, userID)
}

// ServeFile serves a download's file. Managed entries (device header present)
// authorize on (user, profile, device) and re-check per-profile content access
// before serving; ephemeral rows keep today's queued→downloading→completed
// behavior. The ephemeral path never serves a managed row, and vice versa.
func (s *Service) ServeFile(ctx context.Context, w http.ResponseWriter, r *http.Request, userID int, profileID, deviceID, downloadID string, filter catalog.AccessFilter) error {
	// Re-check policy — admin may have disabled downloads or revoked permission.
	if _, err := s.enabledConfig(ctx); err != nil {
		return err
	}
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("loading user: %w", err)
	}
	if !user.DownloadAllowed {
		return ErrDownloadNotAllowed
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

	if err := s.serveDownloadBytes(ctx, w, r, dl, userID); err != nil {
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
	return s.serveDownloadBytes(ctx, w, r, dl, userID)
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

// resolveFormat applies the format policy and restricts the result to
// `original`. Series batches and the one-shot browser direct-download support
// only original; prepared formats (remux/transcode) are created via single-item
// POST /downloads, which routes through the artifact pipeline.
func (s *Service) resolveFormat(requested string, user *models.User, cfg config.DownloadConfig) (string, error) {
	format, err := s.policy.Resolve(requested, user, cfg)
	if err != nil {
		return "", err
	}
	if format != FormatOriginal {
		return "", ErrFormatUnavailable
	}
	return format, nil
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
// original rows.
func (s *Service) serveDownloadBytes(ctx context.Context, w http.ResponseWriter, r *http.Request, dl *Download, userID int) error {
	if dl.Format != FormatOriginal && dl.ArtifactID != "" {
		if s.artifacts == nil {
			return ErrFormatUnavailable
		}
		artifact, err := s.artifacts.Ready(ctx, dl.ArtifactID)
		if err != nil {
			return err
		}
		return s.serveLocalFile(ctx, w, r, artifact.OutputPath, userID)
	}
	file, err := s.fileRepo.GetByID(ctx, dl.MediaFileID)
	if err != nil {
		return fmt.Errorf("loading media file: %w", err)
	}
	if file == nil || file.MissingSince != nil {
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
		if resolutionRank(f.Resolution) > resolutionRank(best.Resolution) {
			best = f
		}
	}
	return best
}

func resolutionRank(res string) int {
	switch strings.ToLower(res) {
	case "2160p":
		return 4
	case "1080p":
		return 3
	case "720p":
		return 2
	case "480p":
		return 1
	default:
		return 0
	}
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
