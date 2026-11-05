package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/downloads"
	"github.com/Silo-Server/silo-server/internal/httpstream"
	"github.com/Silo-Server/silo-server/internal/logredact"
	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

// DownloadService is the interface that the download handler depends on. A
// non-empty deviceID (from the X-Silo-Device-Id header) selects the managed
// device-library lifecycle; empty is the ephemeral/account-level path.
type DownloadService interface {
	Capability(ctx context.Context, userID int) (downloads.Capability, error)
	Create(ctx context.Context, userID int, req downloads.CreateRequest, filter catalog.AccessFilter) (*downloads.Download, error)
	CreateSeries(ctx context.Context, userID int, req downloads.CreateRequest, filter catalog.AccessFilter) ([]*downloads.Download, string, []downloads.SkippedDownload, error)
	CreateSeason(ctx context.Context, userID int, req downloads.CreateRequest, seasonNumber int, filter catalog.AccessFilter) ([]*downloads.Download, string, []downloads.SkippedDownload, error)
	CreateSubscription(ctx context.Context, userID int, req downloads.SubscriptionRequest, filter catalog.AccessFilter) (*downloads.SubscriptionResult, error)
	ListSubscriptions(ctx context.Context, userID int, profileID, deviceID string) ([]*downloads.Subscription, error)
	GetSubscription(ctx context.Context, userID int, profileID, deviceID, id string) (*downloads.Subscription, error)
	UpdateSubscription(ctx context.Context, userID int, profileID, deviceID, id string, patch downloads.SubscriptionPatch, filter catalog.AccessFilter) (*downloads.SubscriptionResult, error)
	DeleteSubscription(ctx context.Context, userID int, profileID, deviceID, id string) error
	SyncSubscriptions(ctx context.Context, userID int, profileID, deviceID string, filter catalog.AccessFilter) (int, error)
	ServeDirect(ctx context.Context, w http.ResponseWriter, r *http.Request, userID, fileID int, format string, filter catalog.AccessFilter) error
	ServeFile(ctx context.Context, w http.ResponseWriter, r *http.Request, userID int, profileID, deviceID, downloadID string, filter catalog.AccessFilter) error
	List(ctx context.Context, userID int, profileID, deviceID string) ([]*downloads.Download, error)
	Delete(ctx context.Context, userID int, profileID, deviceID, downloadID string) error
	PatchStatus(ctx context.Context, userID int, profileID, deviceID, downloadID, status string) error
	BuildManifest(ctx context.Context, userID int, profileID, deviceID, downloadID string, filter catalog.AccessFilter) (*downloads.OfflineManifest, error)
	BuildBatchManifests(ctx context.Context, userID int, profileID, deviceID, batchID string, filter catalog.AccessFilter) ([]*downloads.OfflineManifest, []downloads.SkippedManifest, error)
	ServeArtwork(ctx context.Context, w http.ResponseWriter, r *http.Request, userID int, profileID, deviceID, downloadID, kind string, filter catalog.AccessFilter) error
	ServeSubtitle(ctx context.Context, w http.ResponseWriter, r *http.Request, userID int, profileID, deviceID, downloadID, ref string, filter catalog.AccessFilter) error
}

// DownloadHandler handles download endpoints.
type DownloadHandler struct {
	svc             DownloadService
	nodePlanner     nodepool.DownloadPlanner
	jwtSecret       func() string
	proxyHTTPClient *http.Client
	linkIssuer      DirectDownloadLinkIssuer
	preflightMu     sync.Mutex
	preflightCache  map[string]proxyPreflightResult
}

type proxyPreflightResult struct {
	reachable bool
	expiresAt time.Time
}

// NewDownloadHandler creates a new DownloadHandler.
func NewDownloadHandler(svc DownloadService) *DownloadHandler {
	return &DownloadHandler{
		svc:            svc,
		preflightCache: make(map[string]proxyPreflightResult),
		proxyHTTPClient: &http.Client{
			Timeout: 3 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// SetProxyDelivery enables stateless proxy-node egress after the API service
// has authorized and resolved a download target. It is optional; integrated
// deployments continue to serve bytes locally.
func (h *DownloadHandler) SetProxyDelivery(planner nodepool.DownloadPlanner, jwtSecret func() string) {
	h.nodePlanner = planner
	h.jwtSecret = jwtSecret
}

// DirectDownloadLinkIssuer signs direct-download link tokens;
// auth.JWTService implements it.
type DirectDownloadLinkIssuer interface {
	GenerateDirectDownloadLinkToken(userID int, role, sessionID, profileID string, impersonatorUserID *int, fileID int) (string, time.Time, error)
}

// ErrDirectDownloadLinksUnavailable reports that link minting is not wired:
// no issuer, or a download service that cannot authorize without serving.
var ErrDirectDownloadLinksUnavailable = errors.New("direct download links are not configured")

// ErrDirectDownloadLinkSession reports a caller without a login session (an
// API key). A link is bound to a session so revoking it revokes the link.
var ErrDirectDownloadLinkSession = errors.New("direct download links require a login session")

// SetDirectDownloadLinks enables CreateDirectDownloadLink.
func (h *DownloadHandler) SetDirectDownloadLinks(issuer DirectDownloadLinkIssuer) {
	h.linkIssuer = issuer
}

// CreateDirectDownloadLink authorizes fileID for the request's verified
// profile exactly as a direct download would (download policy, catalog and
// file access under the profile's scope), then mints a short-lived link token
// carrying the session, profile and file. Nothing is served or recorded. The
// direct-download routes authorize the request again when the link is used.
func (h *DownloadHandler) CreateDirectDownloadLink(ctx context.Context, fileID int) (string, time.Time, error) {
	resolver, ok := h.svc.(downloadFileResolver)
	if !ok || h.linkIssuer == nil {
		return "", time.Time{}, ErrDirectDownloadLinksUnavailable
	}
	claims := apimw.GetClaims(ctx)
	profileID := apimw.GetProfileID(ctx)
	if claims == nil || profileID == "" {
		return "", time.Time{}, downloads.ErrProfileRequired
	}
	if claims.SessionID == "" {
		return "", time.Time{}, ErrDirectDownloadLinkSession
	}
	if _, err := resolver.ResolveDirectFile(ctx, claims.UserID, fileID, "", AccessFilterFromContext(ctx, "")); err != nil {
		return "", time.Time{}, err
	}
	return h.linkIssuer.GenerateDirectDownloadLinkToken(claims.UserID, claims.Role, claims.SessionID, profileID, claims.ImpersonatorUserID, fileID)
}

type downloadFileResolver interface {
	ResolveDirectFile(ctx context.Context, userID, fileID int, format string, filter catalog.AccessFilter) (*downloads.FileTarget, error)
	ResolveManagedFile(ctx context.Context, userID int, profileID, deviceID, downloadID string, filter catalog.AccessFilter) (*downloads.FileTarget, error)
}

const (
	proxyDownloadTokenTTL  = 15 * time.Minute
	proxyPreflightCacheTTL = 5 * time.Second
)

// downloadRequest represents the JSON body for POST /downloads.
type downloadRequest struct {
	ContentID string `json:"content_id"`
	EpisodeID string `json:"episode_id,omitempty"`
	FileID    int    `json:"file_id,omitempty"`
	Quality   string `json:"quality,omitempty"`       // original (default) | 20mbps | 10mbps | 5mbps | 2mbps | 1mbps
	Series    bool   `json:"series,omitempty"`        // if true, downloads all episodes
	Season    *int   `json:"season_number,omitempty"` // with series=true, downloads only this season (0 = Specials)
	// Caps is the device decode capability (original fallback / transcode
	// target). The playback type is decoded directly: its JSON contract is the
	// download `caps` contract, and a mirror struct here could only drift.
	Caps *playback.ClientCapabilities `json:"caps,omitempty"`
}

// patchDownloadRequest is the JSON body for PATCH /downloads/{id}.
type patchDownloadRequest struct {
	Status string `json:"status"`
}

// downloadResponse represents a download entry in API responses.
type downloadResponse struct {
	ID                string  `json:"id"`
	ContentID         string  `json:"content_id"`
	EpisodeID         string  `json:"episode_id,omitempty"`
	BatchID           string  `json:"batch_id,omitempty"`
	DeviceID          string  `json:"device_id,omitempty"`
	MediaFileID       int     `json:"media_file_id"`
	FileSize          int64   `json:"file_size"`
	BytesSent         int64   `json:"bytes_sent"`
	Kind              string  `json:"kind"`
	Status            string  `json:"status"`
	Quality           string  `json:"quality"`
	EffectiveQuality  string  `json:"effective_quality"`
	DeliveryFormat    string  `json:"delivery_format"`
	TargetBitrateKbps int     `json:"target_bitrate_kbps"`
	Revision          int     `json:"revision"`
	CreatedAt         string  `json:"created_at"`
	CompletedAt       *string `json:"completed_at,omitempty"`
}

// downloadsListResponse wraps the downloads list for JSON serialization.
type downloadsListResponse struct {
	Downloads []downloadResponse          `json:"downloads"`
	Skipped   []downloads.SkippedDownload `json:"skipped,omitempty"`
}

type batchManifestsResponse struct {
	Manifests []*downloads.OfflineManifest `json:"manifests"`
	// Skipped lists batch entries whose manifest could not be built (revoked,
	// deleted from the catalog, access-filtered); the rest of the batch is
	// still delivered.
	Skipped []downloads.SkippedManifest `json:"skipped,omitempty"`
}

// downloadCapabilityResponse is the GET /downloads/capability payload clients
// use for feature detection instead of introspecting admin settings.
type downloadCapabilityResponse struct {
	Enabled              bool     `json:"enabled"`
	DownloadAllowed      bool     `json:"download_allowed"`
	QualityPresets       []string `json:"quality_presets"`
	TranscodeEnabled     bool     `json:"transcode_enabled"`
	TranscodeUserAllowed bool     `json:"transcode_user_allowed"`
	SeasonDownload       bool     `json:"season_download"`
	SeriesMonitoring     bool     `json:"series_monitoring"`
	MonitoringModes      []string `json:"monitoring_modes,omitempty"`
	ProxyDelivery        bool     `json:"proxy_delivery"`
}

// managedIdentity returns the (profileID, deviceID) the request is acting as.
// deviceID comes ONLY from the X-Silo-Device-Id header (never the body/query);
// profileID is resolved by the viewer-access middleware from X-Profile-Id.
func managedIdentity(r *http.Request) (profileID, deviceID, deviceName, devicePlatform string) {
	device := deviceMetadataFromRequest(r)
	return apimw.GetProfileID(r.Context()), device.DeviceID, device.DeviceName, device.DevicePlatform
}

// HandleDirectDownload handles GET /direct-download?file_id=N. A legacy
// format=original query is accepted, but direct downloads remain original-only.
func (h *DownloadHandler) HandleDirectDownload(w http.ResponseWriter, r *http.Request) {
	h.handleDirectDownload(w, r, false)
}

// HandleDirectDownloadViaProxy serves the additive proxy-aware direct-download
// route while the established endpoint retains its 200/206 response contract.
func (h *DownloadHandler) HandleDirectDownloadViaProxy(w http.ResponseWriter, r *http.Request) {
	h.handleDirectDownload(w, r, true)
}

func (h *DownloadHandler) handleDirectDownload(w http.ResponseWriter, r *http.Request, delegate bool) {
	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	if h.svc == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Downloads not configured")
		return
	}

	fileIDStr := r.URL.Query().Get("file_id")
	fileID, err := strconv.Atoi(fileIDStr)
	if err != nil || fileID <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "file_id query parameter is required")
		return
	}

	filter := requestAccessFilter(r)
	if delegate {
		if handled, redirectErr := h.redirectDirectDownload(r.Context(), w, r, userID, fileID, r.URL.Query().Get("format"), filter); redirectErr != nil {
			h.writeDownloadError(w, redirectErr)
			return
		} else if handled {
			return
		}
	}
	serveCtx := downloads.WithServeAuthorized(r.Context(), func(target downloads.FileTarget) {
		attachTransfer(r.Context(), userID, apimw.GetProfileID(r.Context()), target.MediaFileID)
	})
	// A multi-gigabyte original outlives the API server's absolute WriteTimeout,
	// exactly as on /downloads/{id}/file above; roll the deadline with progress
	// instead of truncating the body at 120 s.
	sw := httpstream.NewRollingDeadlineWriter(w)
	if err := h.svc.ServeDirect(serveCtx, sw, r, userID, fileID, r.URL.Query().Get("format"), filter); err != nil {
		if errors.Is(err, downloads.ErrResponseCommitted) {
			// Headers and part of the file already went out, so an error body
			// would corrupt the media response. Abort it instead, as the v2
			// stream writer requires. A client that left has canceled the
			// request; any other failure, such as a short read from storage,
			// is the server's.
			if r.Context().Err() == nil {
				slog.WarnContext(r.Context(), "direct download failed after the response started",
					"component", "downloads", "file_id", fileID, "error", err)
			}
			panic(http.ErrAbortHandler)
		}
		h.writeDownloadError(w, err)
	}
}

func (h *DownloadHandler) redirectDirectDownload(ctx context.Context, w http.ResponseWriter, r *http.Request, userID, fileID int, format string, filter catalog.AccessFilter) (bool, error) {
	resolver, secret, ok := h.proxyTarget()
	if !ok {
		return false, nil
	}
	target, err := resolver.ResolveDirectFile(ctx, userID, fileID, format, filter)
	if err != nil {
		return false, err
	}
	// The profile has to travel with the redirect. Hardcoding "" here recorded
	// the telemetry transfer, the proxy's own attach (from the token claim) and
	// the node session against no profile at all, so proxy-served traffic went
	// missing from per-profile attribution while the same file served locally
	// was attributed correctly.
	profileID := apimw.GetProfileID(ctx)
	handled, err := h.redirectToProxy(w, r, secret, target, userID, profileID)
	if handled {
		attachTransfer(ctx, userID, profileID, target.MediaFileID)
	}
	return handled, err
}

func (h *DownloadHandler) redirectManagedDownload(ctx context.Context, w http.ResponseWriter, r *http.Request, userID int, profileID, deviceID, downloadID string, filter catalog.AccessFilter) (bool, error) {
	resolver, secret, ok := h.proxyTarget()
	if !ok {
		return false, nil
	}
	target, err := resolver.ResolveManagedFile(ctx, userID, profileID, deviceID, downloadID, filter)
	if err != nil {
		return false, err
	}
	handled, err := h.redirectToProxy(w, r, secret, target, userID, profileID)
	if handled {
		attachTransfer(ctx, userID, profileID, target.MediaFileID)
	}
	return handled, err
}

func (h *DownloadHandler) proxyTarget() (downloadFileResolver, string, bool) {
	resolver, ok := h.svc.(downloadFileResolver)
	if !ok || h.nodePlanner == nil || h.jwtSecret == nil {
		return nil, "", false
	}
	secret := strings.TrimSpace(h.jwtSecret())
	if secret == "" {
		return nil, "", false
	}
	return resolver, secret, true
}

func (h *DownloadHandler) redirectToProxy(w http.ResponseWriter, r *http.Request, secret string, target *downloads.FileTarget, userID int, profileID string) (bool, error) {
	if target == nil || (strings.TrimSpace(target.Path) == "" && target.OriginArtifactID == "") {
		return false, fmt.Errorf("download target is empty")
	}
	if target.OriginArtifactID != "" && strings.TrimSpace(target.OriginNodeURL) == "" {
		return false, nil
	}
	if !target.ProxyEligible {
		return false, nil
	}
	reservationKey := target.DownloadID
	if reservationKey == "" {
		reservationKey = fmt.Sprintf("direct-%d-%d", userID, target.MediaFileID)
	}
	sessionID := fmt.Sprintf("download-%s-%d", reservationKey, time.Now().UnixNano())
	accessPath := netaccess.PathFromContext(r.Context())
	plan := h.nodePlanner.PlanDownloadWith(sessionID, func(node *nodepool.Node) bool {
		return node.ClientURLFor(accessPath) != ""
	}, target.OriginNodeGroup)
	if plan.ProxyNode == nil {
		return false, nil
	}
	releaseReservation := func() { h.nodePlanner.ReleaseSession(sessionID) }
	clientBase := plan.ProxyNode.ClientURLFor(accessPath)
	if clientBase == "" {
		releaseReservation()
		return false, nil
	}
	mediaPath := target.Path
	downloadFilename := ""
	if target.OriginArtifactID != "" {
		mediaPath = ""
		if strings.TrimSpace(target.Path) != "" {
			downloadFilename = filepath.Base(target.Path)
		}
	}
	playMethod := streamtoken.PlayMethodDownload
	if target.ExpectedExecutionFingerprint != "" {
		if target.ExpectedArtifactSize <= 0 {
			releaseReservation()
			return false, nil
		}
		playMethod = streamtoken.PlayMethodToneMapDownload
	}
	token, err := streamtoken.Sign(streamtoken.Claims{
		SessionID:                    sessionID,
		MediaPath:                    mediaPath,
		PlayMethod:                   playMethod,
		TranscodeNode:                target.OriginNodeURL,
		DownloadArtifactID:           target.OriginArtifactID,
		DownloadArtifactRowID:        target.ArtifactID,
		DownloadArtifactSize:         target.ExpectedArtifactSize,
		DownloadExecutionFingerprint: target.ExpectedExecutionFingerprint,
		DownloadFilename:             downloadFilename,
		UserID:                       userID,
		ProfileID:                    profileID,
		MediaFileID:                  target.MediaFileID,
	}, secret, proxyDownloadTokenTTL)
	if err != nil {
		releaseReservation()
		return false, fmt.Errorf("sign proxy download token: %w", err)
	}
	// The location is what the client downloads from, so it uses the proxy's
	// client-facing URL for this access path. The preflight below dials the
	// proxy's backend URL instead: that is the address this server reaches the
	// node on (health sweeps, force-reload), while a client-facing origin may
	// be unreachable from here — a tailnet origin resolves only on tailnet
	// members, and this process need not be one. The verdict is about the
	// proxy's ability to read the file, not about any one access path, so the
	// cache key is the backend URL plus the target and is shared by every path.
	tokenPath := "/downloads/file/" + url.PathEscape(token)
	location := clientBase + tokenPath
	targetKey := target.Path
	if target.OriginArtifactID != "" {
		targetKey = target.OriginNodeURL + "\x00" + target.OriginArtifactID
	}
	backendBase := strings.TrimRight(plan.ProxyNode.URL, "/")
	cacheKey := backendBase + "\x00" + targetKey
	if !h.proxyCanServe(r.Context(), cacheKey, backendBase+tokenPath) {
		releaseReservation()
		return false, nil
	}
	// A HEAD request only proves that the proxy can read the selected path; the
	// proxy deliberately does not count it as an active transfer. Release the
	// provisional slot before redirecting so a probe cannot hold max_jobs
	// capacity until the planner's reservation bridge expires.
	if r.Method == http.MethodHead {
		releaseReservation()
	}
	http.Redirect(w, r, location, http.StatusTemporaryRedirect)
	return true, nil
}

// proxyCanServe reports whether the proxy answers a HEAD for the signed token
// at probeURL, its backend address. Verdicts are cached briefly per cacheKey.
func (h *DownloadHandler) proxyCanServe(ctx context.Context, cacheKey, probeURL string) bool {
	now := time.Now()
	h.preflightMu.Lock()
	for key, cached := range h.preflightCache {
		if !now.Before(cached.expiresAt) {
			delete(h.preflightCache, key)
		}
	}
	if cached, ok := h.preflightCache[cacheKey]; ok && now.Before(cached.expiresAt) {
		h.preflightMu.Unlock()
		return cached.reachable
	}
	h.preflightMu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, probeURL, nil)
	if err != nil {
		return false
	}
	client := h.proxyHTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	reachable := false
	if err != nil {
		// The probe URL carries the signed download token in its path.
		slog.DebugContext(ctx, "download proxy preflight failed; serving locally", "component", "api", "error", logredact.SanitizeURLError(err))
	} else {
		_ = resp.Body.Close()
		reachable = resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices
	}
	if err == nil && !reachable {
		slog.DebugContext(ctx, "download proxy preflight unavailable; serving locally", "component", "api", "status", resp.StatusCode)
	}
	h.preflightMu.Lock()
	if h.preflightCache == nil {
		h.preflightCache = make(map[string]proxyPreflightResult)
	}
	h.preflightCache[cacheKey] = proxyPreflightResult{reachable: reachable, expiresAt: now.Add(proxyPreflightCacheTTL)}
	h.preflightMu.Unlock()
	return reachable
}

func (h *DownloadHandler) writeDownloadError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, downloads.ErrFeatureDisabled):
		writeError(w, http.StatusForbidden, "feature_disabled", "Downloads are disabled")
	case errors.Is(err, downloads.ErrDownloadNotAllowed):
		writeError(w, http.StatusForbidden, "forbidden", "You are not allowed to download")
	case errors.Is(err, downloads.ErrTranscodeDisabled):
		writeError(w, http.StatusForbidden, "transcode_disabled", "Download transcoding is disabled")
	case errors.Is(err, downloads.ErrInvalidQuality):
		writeError(w, http.StatusBadRequest, "invalid_quality", "Unknown download quality")
	case errors.Is(err, downloads.ErrProfileRequired):
		writeError(w, http.StatusBadRequest, "profile_required", "A profile is required for managed downloads")
	case errors.Is(err, downloads.ErrBulkQualityUnavailable):
		writeError(w, http.StatusNotImplemented, "bulk_quality_unavailable", "Bitrate quality is not available for bulk downloads yet")
	case errors.Is(err, downloads.ErrCapacityUnavailable):
		writeError(w, http.StatusServiceUnavailable, "capacity_unavailable", "Download preparation capacity is currently unavailable")
	case errors.Is(err, downloads.ErrCapabilityUnavailable):
		writeError(w, http.StatusServiceUnavailable, "capability_unavailable", "Download preparation capabilities are temporarily unavailable")
	case errors.Is(err, downloads.ErrQualityUnavailable):
		writeError(w, http.StatusNotImplemented, "quality_unavailable", "This download quality is not available")
	case errors.Is(err, downloads.ErrFormatUnavailable):
		writeError(w, http.StatusNotImplemented, "format_unavailable", "This download format is not available yet")
	case errors.Is(err, downloads.ErrConcurrentLimitReached):
		writeError(w, http.StatusTooManyRequests, "download_limit_exceeded", "Maximum concurrent downloads reached")
	case errors.Is(err, downloads.ErrPeriodLimitReached):
		writeError(w, http.StatusTooManyRequests, "download_quota_exceeded", "Download quota exceeded for this period")
	case errors.Is(err, downloads.ErrNoDownloadableEpisodes):
		writeError(w, http.StatusNotFound, "no_downloadable_episodes", "No downloadable episodes found")
	case errors.Is(err, catalog.ErrItemNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Media item not found")
	default:
		slog.Error("download operation failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to process download")
	}
}
