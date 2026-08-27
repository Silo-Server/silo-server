package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/logredact"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/settingsresolve"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/subtitles"
	"github.com/Silo-Server/silo-server/internal/tonemap"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

const (
	maxPlaybackV3BodyBytes       = 256 << 10
	maxPlaybackV3EventBodyBytes  = 32 << 10
	replanLeaseDurationV3        = 15 * time.Second
	replanReleaseTimeoutV3       = 3 * time.Second
	v3NodeCapabilityTTL          = time.Minute
	playbackNodeIntegratedV3     = "integrated"
	subtitleFormatVTTV3          = "vtt"
	subtitleMIMEVTTV3            = "text/vtt"
	subtitleUnavailableReasonV3  = "subtitle_artifact_unavailable"
	transcodeStartFailedReasonV3 = "transcode_start_failed"
	seekRestorationPlayerV3      = "player_position"
	// Failed capability fetches are memoized briefly so an unreachable node
	// costs one timeout per window instead of one per planning request.
	v3NodeCapabilityErrorTTL = 15 * time.Second
	// Capability fetches on the planning path run under a deadline well below
	// the fetch helper's own 10s timeout: planning happens on the start
	// request path, where a slow node must degrade the union, not the user.
	// Full cold-probe budgets are reserved for transport-time validation of the
	// executor already selected.
	v3NodeCapabilityPlanTimeout = 3 * time.Second
	// Older or not-yet-contacted nodes cannot advertise their effective probe
	// matrix. This conservative cold-fetch bound avoids borrowing the central
	// server's unrelated hardware configuration.
	remoteNodeProbeFallbackTimeout = 2 * time.Minute
	// The node-recipe handoff is restart insurance written while the client is
	// blocked on the start response, so a stalled store must lose the insurance
	// rather than the start. Matches the jellycompat handoff's budget.
	nodeRecipeWriteTimeoutV3            = 2 * time.Second
	playbackStartSideEffectsTimeoutV3   = 15 * time.Second
	playbackStartSideEffectsWorkersV3   = 4
	playbackStartSideEffectsQueueSizeV3 = 256
	playbackCapabilityWarmupWorkersV3   = 4
	requestIDLogKeyV3                   = "request_id"
	playbackLogValueV3                  = "playback"
	playbackRemoteOutcomeFailedV3       = "failed"
)

var errSubtitleStoreUnavailableV3 = errors.New("subtitle store unavailable")

type v3NodeCapabilityCache struct {
	transformations     []playback.TransformationV3
	toneMapCapabilities tonemap.Capabilities
	err                 error
	expiresAt           time.Time
	probeRequestTimeout time.Duration
}

type preparedTransportV3 struct {
	url                string
	nodeURL            string
	transportID        string
	hwAccel            string
	toneMapMode        tonemap.Mode
	commit             func()
	rollback           func()
	applySession       func() (func() error, error)
	afterDurableCommit func()
}

type preparedTimelineV3 struct {
	seekSeconds            float64
	streamOriginSeconds    float64
	startSegmentNumber     int
	copySeekAnchorResolved bool
}

type playbackStartTimingsV3 struct {
	started time.Time
	last    time.Time
	attrs   []any
}

func newPlaybackStartTimingsV3() *playbackStartTimingsV3 {
	now := time.Now()
	return &playbackStartTimingsV3{started: now, last: now}
}

func (t *playbackStartTimingsV3) mark(stage string) {
	now := time.Now()
	t.attrs = append(t.attrs, stage+"_ms", now.Sub(t.last).Milliseconds())
	t.last = now
}

func (t *playbackStartTimingsV3) log(ctx context.Context, attemptID string) {
	attrs := []any{
		logComponentKey, playbackLogValueV3,
		requestIDLogKeyV3, chimw.GetReqID(ctx),
		"playback_attempt_id", attemptID,
		"total_ms", time.Since(t.started).Milliseconds(),
	}
	attrs = append(attrs, t.attrs...)
	slog.InfoContext(ctx, "protocol v3 start timing", attrs...)
}

type playbackStartSideEffectsV3 struct {
	session         playback.Session
	file            models.MediaFile
	userID          int
	profileID       string
	audioTrackIndex int
	state           *playbackStartSideEffectsStateV3
}

type playbackStartSideEffectsStateV3 struct {
	ctx           context.Context
	cancel        context.CancelFunc
	done          chan struct{}
	started       bool
	stopRequested bool
}

// mediaAuthModeV3 is the attempt's negotiated media transport mode: how a
// client-visible media URL authenticates, and therefore which origins may serve
// it. Both bits are resolved once from the attempt's (pinned) feature list and
// threaded down every branch rather than re-derived per URL builder.
type mediaAuthModeV3 struct {
	// headerAuth is header_authenticated_media_v1: no client-visible URL
	// carries a signed playback credential, and the client authenticates every
	// media request with its own access token instead.
	headerAuth bool
	// proxyEgress is authorized_media_origins_v1 negotiated on top of
	// headerAuth: the client also honors credential-free absolute URLs on
	// server-designated proxy origins, so media bytes need not all egress from
	// the API server. Never true without headerAuth — on a legacy attempt the
	// signed proxy URL already carries its own authority.
	proxyEgress bool
}

// headerAuthenticatedMediaV3 resolves the negotiated media transport mode from
// a client's advertised feature set.
//
// The mode is a bounded value threaded from the v3 request decoder down through
// transport preparation and into the session's stream state, the same way local
// egress is. It deliberately carries no credential, and the durable normalized
// request stays the source of truth for the attempt.
func headerAuthenticatedMediaV3(clientFeatures []string) mediaAuthModeV3 {
	headerAuth := playback.HasFeatureV3(clientFeatures, playback.FeatureHeaderAuthenticatedMediaV3)
	return mediaAuthModeV3{
		headerAuth:  headerAuth,
		proxyEgress: headerAuth && playback.HasFeatureV3(clientFeatures, playback.FeatureAuthorizedMediaOriginsV3),
	}
}

// recipeCardStoreV3 is the shared control-plane store central hands a recipe
// card to another Silo process through (*noderecipe.Store). One instance owns
// one key space; the handler holds two of them, and what a stored card
// authorizes or rebuilds is the field's business, not the interface's — see
// PlaybackHandler.ProxyGrantStore and PlaybackHandler.NodeRecipeStore.
type recipeCardStoreV3 interface {
	// Enabled reports whether the store can actually carry a card. A disabled
	// store accepts Put silently, so a URL that only a stored card can serve
	// must not be published without checking it.
	Enabled() bool
	// Get reads the card currently stored under key. A replan uses it to
	// remember the card it is about to overwrite, so a failed replacement can
	// hand the restored plan its authority back.
	Get(ctx context.Context, key string) (*playback.RecipeCard, bool)
	Put(ctx context.Context, key string, card playback.RecipeCard) error
	Delete(ctx context.Context, key string) error
}

type transportErrorV3 struct {
	reason    string
	message   string
	retryable bool
	cause     error
}

func subtitleArtifactErrorV3(message string, cause error) *transportErrorV3 {
	return &transportErrorV3{
		reason:    subtitleUnavailableReasonV3,
		message:   message,
		retryable: errors.Is(cause, errSubtitleStoreUnavailableV3),
		cause:     cause,
	}
}

func wrapSubtitleStoreErrorV3(err error) error {
	return fmt.Errorf("%w: %w", errSubtitleStoreUnavailableV3, err)
}

type v3ReplanLock struct {
	mu   sync.Mutex
	refs int
}

type v3EventRate struct {
	windowStart time.Time
	count       int
}

type replacementAdmissionCheckerV3 interface {
	CheckReplacementAllowed(context.Context, string, playback.PlayMethod, bool) error
}

type replacementReservationCancellerV3 interface {
	CancelReplacementReservation(string)
}

type replacementStateManagerV3 interface {
	ApplyReplacement(string, playback.SessionReplacement) (playback.SessionReplacementRollback, error)
	RollbackReplacement(string, playback.SessionReplacementRollback) error
}

type sessionReservationReleaserV3 interface {
	ReleaseSession(string)
}

// sessionProxyReservationReleaserV3 gives back only the proxy half of a node
// reservation, for a start that keeps its transcode node but publishes a URL the
// planned proxy does not serve. Optional: a planner without the method simply
// keeps the whole reservation until it ages out. *nodepool.Planner implements it.
type sessionProxyReservationReleaserV3 interface {
	ReleaseSessionProxy(string)
}

func (e *transportErrorV3) Error() string {
	if e.cause != nil {
		return e.reason + ": " + e.cause.Error()
	}
	return e.reason
}

func (h *PlaybackHandler) transformationRegistryV3(ctx context.Context) *playback.TransformationRegistryV3 {
	h.v3RegistryMu.Lock()
	defer h.v3RegistryMu.Unlock()
	if h.v3Registry != nil {
		return h.v3Registry
	}
	probe := playback.ProbeTransformationRegistryWithToneMapV3Result
	if h.v3RegistryProbe != nil {
		probe = h.v3RegistryProbe
	}
	registry, err := probe(context.WithoutCancel(ctx), h.playbackConfig().FFmpegPath, nil)
	if err == nil {
		h.v3Registry = registry
	}
	return registry
}

// localToneMapCapabilitiesV3 returns a defensive copy of the capabilities
// validated for the current local FFmpeg, backend, and device configuration.
func (h *PlaybackHandler) localToneMapCapabilitiesV3(ctx context.Context) (tonemap.Capabilities, error) {
	cfg := h.playbackConfig()
	ffmpegPath := playback.ResolveFFmpegPath(cfg.FFmpegPath)
	resolved := playback.ResolveHWAccelWithFFmpegContext(ctx, cfg.HWAccel, cfg.FFmpegPath)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	hwDevice := strings.TrimSpace(cfg.HWDevice)
	probe := tonemap.Probe
	if h.v3ToneMapProbe != nil {
		probe = h.v3ToneMapProbe
	}
	capabilities, err := probe(ctx, ffmpegPath, resolved, hwDevice)
	return append(tonemap.Capabilities(nil), capabilities...), err
}

func (h *PlaybackHandler) localToneMapCapabilitiesForTransportV3(ctx context.Context) (tonemap.Capabilities, error) {
	probeCtx, cancel := context.WithTimeout(ctx, h.localToneMapProbeTimeoutV3())
	defer cancel()
	return h.localToneMapCapabilitiesV3(probeCtx)
}

func (h *PlaybackHandler) localToneMapProbeTimeoutV3() time.Duration {
	cfg := h.playbackConfig()
	return tonemap.ProbeEndpointTimeout(cfg.HWAccel, cfg.HWDevice)
}

func (h *PlaybackHandler) remoteToneMapProbeTimeoutV3(nodeURL string) time.Duration {
	h.v3NodeCapabilitiesMu.Lock()
	entry := h.v3NodeCapabilities[nodeURL]
	h.v3NodeCapabilitiesMu.Unlock()
	if entry.probeRequestTimeout > 0 {
		return entry.probeRequestTimeout
	}
	return remoteNodeProbeFallbackTimeout
}

func (h *PlaybackHandler) toneMapPlanningTimeoutV3(localFallbackAllowed bool) time.Duration {
	if localFallbackAllowed {
		return v3NodeCapabilityPlanTimeout
	}
	return remoteNodeProbeFallbackTimeout
}

// remoteTransformationsV3 is the transport-time capability lookup for a
// selected node. It never trusts memoized failures: those may be planning
// deadlines far shorter than this path's fetch budget, and rejecting the
// already-selected node on a stale planning timeout would fail a start the
// fetch could still validate.
func (h *PlaybackHandler) remoteTransformationsV3(ctx context.Context, nodeURL string) ([]playback.TransformationV3, error) {
	return h.lookupRemoteTransformationsV3(ctx, nodeURL, false)
}

// remoteTransformationsPlanningV3 is the planning-time variant: it honors
// negatively-cached fetch failures so an unreachable node costs one timeout
// per error-TTL window instead of one per playback start.
func (h *PlaybackHandler) remoteTransformationsPlanningV3(ctx context.Context, nodeURL string) ([]playback.TransformationV3, error) {
	return h.lookupRemoteTransformationsV3(ctx, nodeURL, true)
}

// lookupRemoteTransformationsV3 returns a node's cached or freshly fetched transformation inventory.
func (h *PlaybackHandler) lookupRemoteTransformationsV3(ctx context.Context, nodeURL string, honorCachedFailure bool) ([]playback.TransformationV3, error) {
	entry, err := h.lookupRemoteCapabilitiesV3(ctx, nodeURL, honorCachedFailure)
	if err != nil {
		return nil, err
	}
	return append([]playback.TransformationV3(nil), entry.transformations...), nil
}

// lookupRemoteCapabilitiesV3 fetches and jointly caches a node's transformation
// and tone-map inventory, optionally reusing short-lived fetch failures.
func (h *PlaybackHandler) lookupRemoteCapabilitiesV3(ctx context.Context, nodeURL string, honorCachedFailure bool) (v3NodeCapabilityCache, error) {
	now := time.Now()
	h.v3NodeCapabilitiesMu.Lock()
	entry, ok := h.v3NodeCapabilities[nodeURL]
	h.v3NodeCapabilitiesMu.Unlock()
	if ok && now.Before(entry.expiresAt) {
		if entry.err == nil {
			return entry, nil
		}
		if honorCachedFailure {
			return v3NodeCapabilityCache{}, entry.err
		}
	}
	if ok && entry.err == nil && honorCachedFailure {
		// A successful inventory is safe for planning after its freshness window:
		// transport-time validation still refreshes before relying on it, while
		// planning can use the stale snapshot and refresh behind the request. This
		// keeps sparse traffic from paying a multi-second node probe every minute.
		h.refreshRemoteCapabilitiesV3(nodeURL)
		return entry, nil
	}

	requestCtx, cancel := context.WithTimeout(ctx, h.remoteToneMapProbeTimeoutV3(nodeURL))
	defer cancel()
	info, err := fetchRemoteTranscodeCapabilities(requestCtx, nodeURL, h.JWTSecret)
	completedAt := time.Now()
	if err != nil {
		h.v3NodeCapabilitiesMu.Lock()
		if h.v3NodeCapabilities == nil {
			h.v3NodeCapabilities = make(map[string]v3NodeCapabilityCache)
		}
		if current, currentOK := h.v3NodeCapabilities[nodeURL]; currentOK && current.err == nil && completedAt.Before(current.expiresAt) {
			h.v3NodeCapabilitiesMu.Unlock()
			return current, nil
		}
		h.v3NodeCapabilities[nodeURL] = v3NodeCapabilityCache{err: err, expiresAt: completedAt.Add(v3NodeCapabilityErrorTTL), probeRequestTimeout: entry.probeRequestTimeout}
		h.v3NodeCapabilitiesMu.Unlock()
		return v3NodeCapabilityCache{}, err
	}
	entry = v3NodeCapabilityCache{
		transformations:     append([]playback.TransformationV3(nil), info.Transformations...),
		toneMapCapabilities: append(tonemap.Capabilities(nil), info.ToneMapCapabilities...),
		expiresAt:           completedAt.Add(v3NodeCapabilityTTL),
		probeRequestTimeout: playback.NormalizeProbeRequestTimeout(info.ProbeRequestTimeoutMillis, remoteNodeProbeFallbackTimeout),
	}
	h.v3NodeCapabilitiesMu.Lock()
	if h.v3NodeCapabilities == nil {
		h.v3NodeCapabilities = make(map[string]v3NodeCapabilityCache)
	}
	h.v3NodeCapabilities[nodeURL] = entry
	h.v3NodeCapabilitiesMu.Unlock()
	return entry, nil
}

func (h *PlaybackHandler) refreshRemoteCapabilitiesV3(nodeURL string) {
	if h == nil || nodeURL == "" {
		return
	}
	if _, loaded := h.v3NodeCapabilityRefresh.LoadOrStore(nodeURL, struct{}{}); loaded {
		return
	}
	go func() {
		defer h.v3NodeCapabilityRefresh.Delete(nodeURL)
		ctx, cancel := context.WithTimeout(context.Background(), h.remoteToneMapProbeTimeoutV3(nodeURL))
		defer cancel()
		if _, err := h.lookupRemoteCapabilitiesV3(ctx, nodeURL, false); err != nil {
			slog.Debug("protocol v3 background node capability refresh failed", "component", "api", "node", logredact.SanitizeURL(nodeURL), "error", err)
		}
	}()
}

// StartCapabilityWarmupV3 moves local and pooled-node capability discovery off
// the first viewer's start request. It is best effort: failed probes remain
// retryable through the ordinary lookup path and never prevent API startup.
func (h *PlaybackHandler) StartCapabilityWarmupV3(ctx context.Context) {
	if h == nil || ctx == nil {
		return
	}
	go h.warmPlaybackCapabilitiesV3(ctx)
}

func (h *PlaybackHandler) warmPlaybackCapabilitiesV3(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, remoteNodeProbeFallbackTimeout)
	defer cancel()
	_ = h.transformationRegistryV3(ctx)

	settings := h.plannerSettingsV3(ctx)
	policy := tonemap.NewPolicy(settings.HardwareToneMapEnabled, settings.SoftwareToneMapEnabled)
	if policy != tonemap.PolicyNone {
		if _, err := h.localToneMapCapabilitiesV3(ctx); err != nil {
			slog.DebugContext(ctx, "protocol v3 local capability warmup failed", "component", "api", "error", err)
		}
	}
	// Keep ordinary SDR playback warm independently of the tone-map probe: its
	// smoke graph has different filters and may already be satisfied by cache.
	cfg := h.playbackConfig()
	if err := playback.WarmHardwareEncoder(ctx, cfg.FFmpegPath, cfg.HWAccel, cfg.HWDevice); err != nil {
		slog.DebugContext(ctx, "protocol v3 hardware encoder warmup failed", "component", "api", "error", err)
	}
	enumerator, ok := h.NodePlanner.(transcodeNodeEnumeratorV3)
	if !ok {
		return
	}
	nodeURLs := enumerator.TranscodeNodeURLs()
	workerCount := min(playbackCapabilityWarmupWorkersV3, len(nodeURLs))
	if workerCount == 0 {
		return
	}
	jobs := make(chan string)
	var group sync.WaitGroup
	group.Add(workerCount)
	for range workerCount {
		go func() {
			defer group.Done()
			for nodeURL := range jobs {
				if _, err := h.lookupRemoteCapabilitiesV3(ctx, nodeURL, false); err != nil {
					slog.DebugContext(ctx, "protocol v3 node capability warmup failed", logComponentKey, "api", "node", logredact.SanitizeURL(nodeURL), "error", err)
				}
			}
		}()
	}
	for _, nodeURL := range nodeURLs {
		select {
		case jobs <- nodeURL:
		case <-ctx.Done():
			close(jobs)
			group.Wait()
			return
		}
	}
	close(jobs)
	group.Wait()
}

// remoteToneMapCapabilitiesV3 returns a defensive copy of one node's validated
// tone-map inventory; planning lookups may honor the negative cache.
func (h *PlaybackHandler) remoteToneMapCapabilitiesV3(ctx context.Context, nodeURL string, planning bool) (tonemap.Capabilities, error) {
	entry, err := h.lookupRemoteCapabilitiesV3(ctx, nodeURL, planning)
	if err != nil {
		return nil, err
	}
	return append(tonemap.Capabilities(nil), entry.toneMapCapabilities...), nil
}

// transcodeNodeEnumeratorV3 exposes the pooled transcode nodes whose
// advertised transformations widen HLS planning; *nodepool.Planner implements
// it.
type transcodeNodeEnumeratorV3 interface {
	TranscodeNodeURLs() []string
}

// proxyNodeEnumeratorV3 is the proxy-pool counterpart, letting identity
// planning narrow selection to proxies that can execute the plan's recipe.
type proxyNodeEnumeratorV3 interface {
	ProxyNodeURLs() []string
}

// proxyEgressOriginsAvailableV3 reports whether this deployment can actually
// send an authorized-origins attempt to a proxy. A planner that cannot
// enumerate proxies counts as none: the escalation this gates exists precisely
// for the case where identity work has no executor, and assuming an origin the
// server cannot name would leave the attempt with nowhere to run. An unusable
// grant store counts as none for the same reason — a proxy origin serves only
// from a stored grant, so without one no proxy URL is publishable by this
// process, ever.
//
// The distinction this preserves is between "not right now" and "not ever". A
// configured-but-currently-ineligible proxy (saturated, unhealthy, cannot run
// the recipe) deliberately still suppresses escalation: that attempt gets the
// same retryable capacity_unavailable a legacy attempt gets, and retrying is
// the correct response. Only a deployment that cannot do proxy egress at all —
// no proxies in the pool, or no grant store to authorize one with — escalates
// to HLS.
func (h *PlaybackHandler) proxyEgressOriginsAvailableV3() bool {
	if h == nil || h.NodePlanner == nil {
		return false
	}
	if h.ProxyGrantStore == nil || !h.ProxyGrantStore.Enabled() {
		return false
	}
	enumerator, ok := h.NodePlanner.(proxyNodeEnumeratorV3)
	return ok && len(enumerator.ProxyNodeURLs()) > 0
}

// hlsToneMapCapabilityInventoryV3 separates locally executable tone-map
// capabilities from the union advertised by local and pooled-node executors.
type hlsToneMapCapabilityInventoryV3 struct {
	local                    tonemap.Capabilities
	union                    tonemap.Capabilities
	localTranscodeFallbackOK bool
}

// localHLSExecutionRegistryV3 returns the transformations this API process can
// execute for an HLS delivery. Tone mapping is added only when local execution
// is allowed, the administrator permits a mode, and the configured FFmpeg and
// device have validated an executor in that mode. Planning and transport-time
// validation must share this view or a locally planned recipe can be rejected
// before FFmpeg starts.
func (h *PlaybackHandler) localHLSExecutionRegistryV3(ctx context.Context) (*playback.TransformationRegistryV3, error) {
	settings := h.plannerSettingsV3(ctx)
	policy := tonemap.NewPolicy(settings.HardwareToneMapEnabled, settings.SoftwareToneMapEnabled)
	inventory := hlsToneMapCapabilityInventoryV3{}
	if policy != tonemap.PolicyNone {
		var err error
		inventory.localTranscodeFallbackOK, inventory.local, err = h.localHLSToneMapCapabilitiesForTransportV3(ctx)
		if err != nil {
			return nil, err
		}
	}
	return h.localHLSExecutionRegistryWithInputsV3(ctx, settings, inventory), nil
}

// localHLSExecutionRegistryWithInputsV3 builds the local HLS execution
// registry from one consistent settings and capability snapshot.
func (h *PlaybackHandler) localHLSExecutionRegistryWithInputsV3(
	ctx context.Context,
	settings playback.PlannerSettingsV3,
	inventory hlsToneMapCapabilityInventoryV3,
) *playback.TransformationRegistryV3 {
	local := h.transformationRegistryV3(ctx)
	policy := tonemap.NewPolicy(settings.HardwareToneMapEnabled, settings.SoftwareToneMapEnabled)
	if policy == tonemap.PolicyNone || !inventory.localTranscodeFallbackOK {
		return local
	}
	capabilityAvailable := false
	for _, capability := range inventory.local {
		if policy.Allows(capability.Mode) && len(capability.SourceKinds) > 0 {
			capabilityAvailable = true
			break
		}
	}
	if capabilityAvailable {
		local = local.WithAdvertised([]playback.TransformationV3{{
			Name: playback.TransformationHDRToSDRToneMapV3, Executor: playback.ExecutorServerV3,
			RecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3,
		}})
	}
	return local
}

// hlsPlanningRegistryV3 returns the registry HLS deliveries plan against: the
// local execution registry plus every pooled transcode node's advertised
// transformations. Only availability of locally-defined specs widens (name
// and recipe version pinned by this server), so node-only capabilities remain
// unavailable when transport falls back to the API process.
func (h *PlaybackHandler) hlsPlanningRegistryV3(ctx context.Context) *playback.TransformationRegistryV3 {
	settings := h.plannerSettingsV3(ctx)
	inventory := hlsToneMapCapabilityInventoryV3{}
	policy := tonemap.NewPolicy(settings.HardwareToneMapEnabled, settings.SoftwareToneMapEnabled)
	if policy != tonemap.PolicyNone {
		inventory, _ = h.hlsToneMapCapabilityInventoryV3(ctx)
	}
	return h.hlsPlanningRegistryWithInputsV3(ctx, settings, inventory)
}

// hlsPlanningRegistryWithInputsV3 combines locally executable
// transformations with transformations advertised by pooled transcode nodes.
func (h *PlaybackHandler) hlsPlanningRegistryWithInputsV3(
	ctx context.Context,
	settings playback.PlannerSettingsV3,
	inventory hlsToneMapCapabilityInventoryV3,
) *playback.TransformationRegistryV3 {
	local := h.localHLSExecutionRegistryWithInputsV3(ctx, settings, inventory)
	enumerator, ok := h.NodePlanner.(transcodeNodeEnumeratorV3)
	if !ok {
		return local
	}
	nodeURLs := enumerator.TranscodeNodeURLs()
	if len(nodeURLs) == 0 {
		return local
	}
	var merged []playback.TransformationV3
	for _, transformations := range h.pooledNodeTransformationsV3(ctx, nodeURLs) {
		merged = append(merged, transformations...)
	}
	return local.WithAdvertised(merged)
}

func (h *PlaybackHandler) localHLSToneMapCapabilitiesForTransportV3(ctx context.Context) (bool, tonemap.Capabilities, error) {
	localFallbackAllowed := h.NodePlanner == nil || nodepool.LocalTranscodeFallbackAllowed(ctx, h.SettingsRepo)
	if !localFallbackAllowed {
		return false, nil, nil
	}
	capabilities, err := h.localToneMapCapabilitiesForTransportV3(ctx)
	return true, capabilities, err
}

// hlsToneMapCapabilityInventoryV3 snapshots local and pooled-node tone-map
// capabilities for a single planning operation.
func (h *PlaybackHandler) hlsToneMapCapabilityInventoryV3(ctx context.Context) (hlsToneMapCapabilityInventoryV3, error) {
	localAllowed := h.NodePlanner == nil || nodepool.LocalTranscodeFallbackAllowed(ctx, h.SettingsRepo)
	fetchCtx, cancel := context.WithTimeout(ctx, h.toneMapPlanningTimeoutV3(localAllowed))
	defer cancel()

	type capabilityResult struct {
		capabilities tonemap.Capabilities
		err          error
	}
	var localResult capabilityResult
	var localWG sync.WaitGroup
	if localAllowed {
		localWG.Add(1)
		go func() {
			defer localWG.Done()
			localResult.capabilities, localResult.err = h.localToneMapCapabilitiesV3(fetchCtx)
		}()
	}

	var nodeURLs []string
	if enumerator, ok := h.NodePlanner.(transcodeNodeEnumeratorV3); ok {
		nodeURLs = enumerator.TranscodeNodeURLs()
	}
	results := make([]capabilityResult, len(nodeURLs))
	var wg sync.WaitGroup
	for i, nodeURL := range nodeURLs {
		wg.Add(1)
		go func(i int, nodeURL string) {
			defer wg.Done()
			remote, err := h.remoteToneMapCapabilitiesV3(fetchCtx, nodeURL, true)
			results[i].err = err
			if err != nil {
				slog.DebugContext(ctx, "protocol v3 node tone-map capability unavailable for planning", "component", "api", "node", logredact.SanitizeURL(nodeURL), "error", err)
				return
			}
			results[i].capabilities = remote
		}(i, nodeURL)
	}
	wg.Wait()
	localWG.Wait()

	inventory := hlsToneMapCapabilityInventoryV3{localTranscodeFallbackOK: localAllowed}
	var probeErr error
	if localAllowed {
		if localResult.err != nil {
			probeErr = errors.Join(probeErr, localResult.err)
		} else {
			inventory.local = localResult.capabilities
			inventory.union = append(inventory.union, localResult.capabilities...)
		}
	}
	for _, remote := range results {
		if remote.err != nil {
			probeErr = errors.Join(probeErr, remote.err)
			continue
		}
		inventory.union = append(inventory.union, remote.capabilities...)
	}
	return inventory, probeErr
}

// hlsToneMapCapabilitiesV3 builds the executor union available to HLS planning
// from eligible local fallback and all reachable transcode nodes.
func (h *PlaybackHandler) hlsToneMapCapabilitiesV3(ctx context.Context) tonemap.Capabilities {
	inventory, _ := h.hlsToneMapCapabilityInventoryV3(ctx)
	return inventory.union
}

type hlsPlanningSnapshotV3 struct {
	handler   *PlaybackHandler
	ctx       context.Context
	settings  playback.PlannerSettingsV3
	once      sync.Once
	registry  *playback.TransformationRegistryV3
	inventory hlsToneMapCapabilityInventoryV3
	err       error
	resolved  bool
}

func (snapshot *hlsPlanningSnapshotV3) resolve() {
	snapshot.once.Do(func() {
		snapshot.resolved = true
		policy := tonemap.NewPolicy(snapshot.settings.HardwareToneMapEnabled, snapshot.settings.SoftwareToneMapEnabled)
		if policy != tonemap.PolicyNone {
			snapshot.inventory, snapshot.err = snapshot.handler.hlsToneMapCapabilityInventoryV3(snapshot.ctx)
		}
		snapshot.registry = snapshot.handler.hlsPlanningRegistryWithInputsV3(snapshot.ctx, snapshot.settings, snapshot.inventory)
	})
}

func (snapshot *hlsPlanningSnapshotV3) hlsRegistry() *playback.TransformationRegistryV3 {
	snapshot.resolve()
	return snapshot.registry
}

func (snapshot *hlsPlanningSnapshotV3) toneMapCapabilities() tonemap.Capabilities {
	snapshot.resolve()
	return snapshot.inventory.union
}

func (snapshot *hlsPlanningSnapshotV3) capabilityError() error {
	if !snapshot.resolved {
		return nil
	}
	return snapshot.err
}

func retryIncompleteToneMapPlanningV3(result playback.PlannerResultV3, capabilityErr error) playback.PlannerResultV3 {
	if capabilityErr == nil || result.Terminal == nil {
		return result
	}
	switch result.Terminal.Reason {
	case playback.TerminalHDRTranscodeUnsupportedV3, terminalSubtitleConversionUnsupportedV3:
	default:
		return result
	}
	result.Terminal = &playback.TerminalV3{
		Reason:    transcodeStartFailedReasonV3,
		Message:   "Tone-map capability discovery is temporarily unavailable.",
		Retryable: true,
	}
	return result
}

func retryIncompletePlaybackSettingsV3(result playback.PlannerResultV3, settingsErr error) playback.PlannerResultV3 {
	if settingsErr == nil || result.Terminal == nil {
		return result
	}
	terminal := result.Terminal
	settingsDependent := terminal.Reason == playback.TerminalHDRTranscodeUnsupportedV3 ||
		(terminal.Reason == terminalNoAlternateVersionV3 && terminal.Message == playback.TerminalMessage4KTranscodeDisabledV3) ||
		(terminal.Reason == terminalSubtitleConversionUnsupportedV3 &&
			(strings.Contains(terminal.Message, "this HDR source") || strings.Contains(terminal.Message, "4K transcoding is disabled")))
	if !settingsDependent {
		return result
	}
	result.Terminal = &playback.TerminalV3{
		Reason:    transcodeStartFailedReasonV3,
		Message:   "Playback settings are temporarily unavailable.",
		Retryable: true,
	}
	return result
}

func (h *PlaybackHandler) planPlaybackWithCapabilitiesV3(ctx context.Context, input playback.PlannerInputV3) (playback.PlannerResultV3, error) {
	snapshot := &hlsPlanningSnapshotV3{handler: h, ctx: ctx, settings: input.Settings}
	input.HLSRegistry = snapshot.hlsRegistry
	input.HLSToneMapCapabilities = snapshot.toneMapCapabilities
	result := playback.PlanPlaybackV3(input)
	if result.Terminal != nil {
		switch result.Terminal.Reason {
		case playback.TerminalHDRTranscodeUnsupportedV3, terminalSubtitleConversionUnsupportedV3:
			return result, snapshot.capabilityError()
		}
	}
	return result, nil
}

// pooledNodeTransformationsV3 collects the advertised transformations of the
// given transcode nodes, keyed by node URL. Stale cache entries are refreshed
// concurrently under a short planning deadline; nodes that cannot be reached
// contribute nothing (their failures are negatively cached), so planning
// degrades toward the local registry instead of blocking the start path.
func (h *PlaybackHandler) pooledNodeTransformationsV3(ctx context.Context, nodeURLs []string) map[string][]playback.TransformationV3 {
	fetchCtx, cancel := context.WithTimeout(ctx, v3NodeCapabilityPlanTimeout)
	defer cancel()
	results := make([][]playback.TransformationV3, len(nodeURLs))
	var wg sync.WaitGroup
	for i, nodeURL := range nodeURLs {
		wg.Add(1)
		go func(i int, nodeURL string) {
			defer wg.Done()
			transformations, err := h.remoteTransformationsPlanningV3(fetchCtx, nodeURL)
			if err != nil {
				slog.DebugContext(ctx, "protocol v3 node capability unavailable for planning", "component", "api", "node", logredact.SanitizeURL(nodeURL), "error", err)
				return
			}
			results[i] = transformations
		}(i, nodeURL)
	}
	wg.Wait()
	byURL := make(map[string][]playback.TransformationV3, len(nodeURLs))
	for i, transformations := range results {
		if transformations != nil {
			byURL[nodeURLs[i]] = transformations
		}
	}
	return byURL
}

// capabilitySessionPlannerV3 is implemented by *nodepool.Planner; it lets the
// transport layer restrict node selection to nodes that can execute the
// plan's server transformations.
type capabilitySessionPlannerV3 interface {
	PlanSessionWith(sessionID, currentTranscodeURL string, needsTranscode bool, estBitrateKbps int, eligible func(*nodepool.Node) bool) nodepool.Plan
}

// localEgressSessionPlannerV3 lets a pooled transcode executor feed the API
// server while the API remains the only client-facing media origin. A planner
// that lacks this optional method still works; its proxy selection is discarded
// before a URL is returned, though the concrete nodepool planner implements the
// method so production reservation accounting stays exact.
type localEgressSessionPlannerV3 interface {
	PlanTranscodeSessionWithLocalEgress(sessionID, currentTranscodeURL string, eligible func(*nodepool.Node) bool) nodepool.Plan
}

// planNodeSessionV3 selects execution nodes for the session. Plans that carry
// server transformations restrict selection to nodes whose advertised
// capabilities validate against the plan, so load balancing in a
// heterogeneous pool cannot land a recipe on a node that would reject it when
// a capable sibling exists. In local-egress mode the API relays the selected
// transcode node and no client-facing proxy is selected or returned.
func (h *PlaybackHandler) planNodeSessionV3(ctx context.Context, session *playback.Session, result playback.PlannerResultV3, localEgress bool) nodepool.Plan {
	return h.planNodeSessionExcludingV3(ctx, session, result, localEgress, nil)
}

// planNodeSessionExcludingV3 is planNodeSessionV3 with a set of node URLs the
// caller has already exhausted. The tone-map software fallback uses it so a
// second attempt cannot land back on the node that just refused the recipe.
func (h *PlaybackHandler) planNodeSessionExcludingV3(ctx context.Context, session *playback.Session, result playback.PlannerResultV3, localEgress bool, excluded map[string]struct{}) nodepool.Plan {
	var eligible func(*nodepool.Node) bool
	// The per-node capability fan-out only pays for itself when a planner can
	// actually consume the predicate it produces: PlanSessionWith for an ordinary
	// selection, PlanTranscodeSessionWithLocalEgress for a local-egress one. A
	// planner that implements neither would spend a round of capability lookups
	// on a filter nothing reads.
	capabilitySelector, capabilitySelectable := h.NodePlanner.(capabilitySessionPlannerV3)
	localEgressSelector, localEgressSelectable := h.NodePlanner.(localEgressSessionPlannerV3)
	predicateConsumed := capabilitySelectable || (localEgress && localEgressSelectable)
	if enumerator, ok := h.NodePlanner.(transcodeNodeEnumeratorV3); ok && predicateConsumed && planRequiresServerTransformationsV3(result.Plan) {
		capable := make(map[string]struct{})
		for nodeURL, advertised := range h.pooledNodeTransformationsV3(ctx, enumerator.TranscodeNodeURLs()) {
			if validateAdvertisedTransformationsV3(result.Plan, advertised) != nil {
				continue
			}
			if planRequiresToneMapV3(result.Plan) {
				capabilities, err := h.remoteToneMapCapabilitiesV3(ctx, nodeURL, true)
				if err != nil || !capabilities.Supports(result.ToneMapMode, result.ToneMapSourceKind) {
					continue
				}
			}
			capable[nodeURL] = struct{}{}
		}
		// The predicate runs under the planner lock: a set lookup only.
		eligible = func(node *nodepool.Node) bool {
			if node == nil {
				return false
			}
			if _, skip := excluded[node.URL]; skip {
				return false
			}
			_, found := capable[node.URL]
			return found
		}
	} else if len(excluded) > 0 && predicateConsumed {
		// No capability filter applies to this plan, but an exhausted node must
		// still be kept out of the selection.
		eligible = func(node *nodepool.Node) bool {
			if node == nil {
				return false
			}
			_, skip := excluded[node.URL]
			return !skip
		}
	}

	var plan nodepool.Plan
	switch {
	case localEgress && localEgressSelectable:
		plan = localEgressSelector.PlanTranscodeSessionWithLocalEgress(session.ID, session.TranscodeNodeURL, eligible)
	case capabilitySelectable && eligible != nil:
		plan = capabilitySelector.PlanSessionWith(session.ID, session.TranscodeNodeURL, true, result.TargetBitrateKbps, eligible)
	default:
		plan = h.NodePlanner.PlanSession(session.ID, session.TranscodeNodeURL, true, result.TargetBitrateKbps)
	}
	if plan.TranscodeNode != nil {
		if _, skip := excluded[plan.TranscodeNode.URL]; skip {
			// A planner with no eligibility hook handed back a node this caller has
			// already exhausted. Release the reservation rather than restart the
			// same refused recipe on it.
			if releaser, ok := h.NodePlanner.(sessionReservationReleaserV3); ok {
				releaser.ReleaseSession(session.ID)
			}
			plan.TranscodeNode = nil
		}
	}
	if localEgress {
		// Compatibility fallback for a custom planner that has not learned the
		// exact local-egress reservation method. The proxy may have been selected
		// internally, but it is never exposed as client media authority.
		plan.ProxyNode = nil
	}
	return plan
}

// planRequiresToneMapV3 reports whether a plan contains the server-owned HDR
// to SDR transformation.
func planRequiresToneMapV3(plan *playback.PlanV3) bool {
	if plan == nil {
		return false
	}
	for _, transformation := range plan.Transformations {
		if transformation.Executor == playback.ExecutorServerV3 &&
			transformation.Name == playback.TransformationHDRToSDRToneMapV3 {
			return true
		}
	}
	return false
}

// validateToneMapExecutorV3 confirms that the selected executor still supports
// the exact mode and source kind frozen by planning.
func validateToneMapExecutorV3(result playback.PlannerResultV3, capabilities tonemap.Capabilities) error {
	if !planRequiresToneMapV3(result.Plan) {
		return nil
	}
	if result.ToneMapMode == "" || result.ToneMapSourceKind == "" ||
		!capabilities.Supports(result.ToneMapMode, result.ToneMapSourceKind) {
		return fmt.Errorf("executor lacks %s %s tone mapping", result.ToneMapMode, result.ToneMapSourceKind)
	}
	return nil
}

// validateAdvertisedTransformationsV3 verifies that every server-executed
// transformation the plan requires is advertised — at the exact recipe
// version — by the executor under consideration (a pooled node's capability
// response or the local registry's Advertised set).
func validateAdvertisedTransformationsV3(plan *playback.PlanV3, advertised []playback.TransformationV3) error {
	available := make(map[string]string, len(advertised))
	for _, transformation := range advertised {
		available[strings.ToLower(strings.TrimSpace(transformation.Name))] = strings.TrimSpace(transformation.RecipeVersion)
	}
	if plan == nil {
		return errors.New("playback plan is unavailable")
	}
	for _, required := range plan.Transformations {
		if strings.EqualFold(required.Executor, "client") {
			continue
		}
		version, ok := available[strings.ToLower(strings.TrimSpace(required.Name))]
		if !ok || version != strings.TrimSpace(required.RecipeVersion) {
			return fmt.Errorf("executor lacks transformation %s@%s", required.Name, required.RecipeVersion)
		}
	}
	return nil
}

// HandlePlaybackCapabilityV3 reports only transformations that the installed
// runtime has actually probed. Protocol v3 is the server's only playback
// protocol, so `enabled` is constant; it stays in the response because clients
// feature-detect against it and the field is part of the frozen contract.
func (h *PlaybackHandler) HandlePlaybackCapabilityV3(w http.ResponseWriter, r *http.Request) {
	if apimw.GetUserID(r.Context()) == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	response := playback.CapabilityResponseV3{Enabled: true, ProtocolVersions: []int{playback.ProtocolV3}}
	response.Features = playback.ServerFeaturesV3()
	response.Deliveries = []playback.DeliveryV3{playback.DeliveryOriginalHTTPV3, playback.DeliveryRemuxProgressiveV3, playback.DeliveryRemuxHLSV3, playback.DeliveryTranscodeHLSV3}
	settings := h.plannerSettingsV3(r.Context())
	policy := tonemap.NewPolicy(settings.HardwareToneMapEnabled, settings.SoftwareToneMapEnabled)
	inventory := hlsToneMapCapabilityInventoryV3{}
	if policy != tonemap.PolicyNone {
		inventory, _ = h.hlsToneMapCapabilityInventoryV3(r.Context())
	}
	registry := h.hlsPlanningRegistryWithInputsV3(r.Context(), settings, inventory)
	toneMapAvailable := false
	if policy != tonemap.PolicyNone {
		for _, capability := range inventory.union {
			if policy.Allows(capability.Mode) && len(capability.SourceKinds) > 0 {
				toneMapAvailable = true
				break
			}
		}
	}
	for _, transformation := range registry.Advertised() {
		if transformation.Name == playback.TransformationHDRToSDRToneMapV3 && !toneMapAvailable {
			continue
		}
		response.Transformations = append(response.Transformations, transformation)
	}
	writeJSON(w, http.StatusOK, response)
}

// handleStartPlaybackV3 validates, plans, and starts a protocol-v3 request.
func (h *PlaybackHandler) handleStartPlaybackV3(w http.ResponseWriter, r *http.Request, body []byte) {
	timings := newPlaybackStartTimingsV3()
	var req playback.StartRequestV3
	defer func() { timings.log(r.Context(), req.PlaybackAttemptID) }()
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid protocol v3 request body")
		return
	}
	warnings, err := req.NormalizeAndValidate()
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	timings.mark("decode_validate")
	profileID := apimw.GetProfileID(r.Context())
	if profileID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "X-Profile-Id header is required")
		return
	}
	if req.ProfileID != profileID {
		writeError(w, http.StatusBadRequest, "bad_request", "profile_id must match X-Profile-Id")
		return
	}
	userID := apimw.GetUserID(r.Context())
	deviceID := deviceMetadataFromRequest(r).DeviceID
	requestDigests := newPlaybackStartRequestDigestsV3(body, deviceID)
	if existing, lookupErr := h.PlanStoreV3.GetAttemptByPlaybackAttemptID(r.Context(), req.PlaybackAttemptID); lookupErr == nil {
		if existing.UserID != userID || existing.ProfileID != profileID || existing.RequestedMediaFileID != req.FileID ||
			!requestDigests.matches(existing.RequestDigest) {
			writeError(w, http.StatusConflict, "playback_attempt_reused", "The playback attempt ID belongs to a different request")
			return
		}
		response := decisionResponseFromAttemptV3(existing)
		if response.Terminal != nil {
			writeJSON(w, http.StatusCreated, response)
			return
		}
		// The replayed plan is only usable while its session is alive; a dead
		// session must surface as a retryable terminal so the client mints a
		// fresh attempt instead of replaying a plan it can never stream.
		if existing.SessionID == "" {
			writeError(w, http.StatusInternalServerError, "internal_error", "Stored playback attempt has no replayable decision")
			return
		}
		if _, sessionErr := h.sessionMgr.GetSession(existing.SessionID); sessionErr != nil {
			writeJSON(w, http.StatusCreated, playback.NewTerminalResponseV3("session_expired", "The playback session for this attempt has ended.", true))
			return
		}
		writeJSON(w, http.StatusCreated, response)
		return
	} else if !errors.Is(lookupErr, playback.ErrSessionNotFound) {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to check playback attempt idempotency")
		return
	}
	timings.mark("idempotency")
	requestedFile, err := h.loadAuthorizedFile(r, req.FileID)
	if err != nil {
		writeV3FileError(w, err)
		return
	}
	requestedFile = h.ensurePlaybackProbe(r.Context(), requestedFile)
	timings.mark("file_load_probe")
	audioIndex, err := resolveV3AudioIndex(requestedFile, req.AudioTrackID, req.AudioTrackIndex)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if req.AudioTrackID == "" && req.AudioTrackIndex == nil {
		audioIndex, err = h.preferredAudioTrackIndexV3(r.Context(), userID, profileID, deviceID, requestedFile)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load the saved audio preference")
			return
		}
	}
	timings.mark("audio_preference")
	effectiveFile := requestedFile
	settings, settingsErr := h.plannerSettingsV3Result(r.Context())
	timings.mark("planner_settings")
	if err := preflightPlaybackFile(r.Context(), effectiveFile, h.MissingMarker, h.EventsHub); err != nil {
		writePlaybackFilePreflightError(w, err)
		return
	}
	timings.mark("file_preflight")
	if req.StartPosition == nil {
		req.StartPosition, err = h.resumePositionV3(r.Context(), userID, profileID, effectiveFile)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load saved playback progress")
			return
		}
	}
	timings.mark("resume")
	result, toneMapCapabilityErr := h.planPlaybackWithCapabilitiesV3(r.Context(), playback.PlannerInputV3{
		Request: req, RequestedFile: requestedFile, EffectiveFile: effectiveFile,
		AudioTrackIndex: audioIndex, Settings: settings,
		Registry: h.transformationRegistryV3(r.Context()), DVRPUStrippable: h.lazyDVRPUStrippableV3(r.Context(), effectiveFile), Now: time.Now(),
		AdditionalSubtitles: h.downloadedSubtitleInventoryV3(r.Context(), effectiveFile),
	})
	timings.mark("planning")
	if terminalAllowsAlternateFileV3(result.Terminal) && shouldTryAlternateFileV3(req.QualityPreference) {
		if alternates, alternateErr := h.findAlternateFiles(r.Context(), requestedFile); alternateErr == nil {
			baseReq := req
			baseAudioIndex := audioIndex
			var firstFailureResult playback.PlannerResultV3
			var firstFailureToneMapErr error
			var firstFailureFile *models.MediaFile
			var firstFailureReq playback.StartRequestV3
			firstFailureAudioIndex := 0
			for _, alternate := range alternates {
				candidateFile := h.ensurePlaybackProbe(r.Context(), alternate)
				candidateReq := baseReq
				candidateAudioIndex := remapAudioIndexV3(requestedFile, candidateFile, baseAudioIndex)
				var candidateResult playback.PlannerResultV3
				var candidateToneMapErr error
				if err := h.remapSubtitleSelectionV3(r.Context(), requestedFile, candidateFile, &candidateReq); err != nil {
					candidateResult = playback.PlannerResultV3{Terminal: &playback.TerminalV3{Reason: terminalSubtitleUnavailableInVersionV3, Message: err.Error(), Retryable: false}}
				} else {
					if err := preflightPlaybackFile(r.Context(), candidateFile, h.MissingMarker, h.EventsHub); err != nil {
						continue
					}
					candidateResult, candidateToneMapErr = h.planPlaybackWithCapabilitiesV3(r.Context(), playback.PlannerInputV3{Request: candidateReq, RequestedFile: requestedFile, EffectiveFile: candidateFile, AudioTrackIndex: candidateAudioIndex, Settings: settings, Registry: h.transformationRegistryV3(r.Context()), DVRPUStrippable: h.lazyDVRPUStrippableV3(r.Context(), candidateFile), Now: time.Now(), AdditionalSubtitles: h.downloadedSubtitleInventoryV3(r.Context(), candidateFile)})
				}
				if candidateResult.Terminal == nil {
					req = candidateReq
					effectiveFile = candidateFile
					audioIndex = candidateAudioIndex
					result = candidateResult
					toneMapCapabilityErr = candidateToneMapErr
					break
				}
				if firstFailureFile == nil {
					firstFailureResult = candidateResult
					firstFailureToneMapErr = candidateToneMapErr
					firstFailureFile = candidateFile
					firstFailureReq = candidateReq
					firstFailureAudioIndex = candidateAudioIndex
				}
			}
			if result.Terminal != nil && firstFailureFile != nil {
				req = firstFailureReq
				effectiveFile = firstFailureFile
				audioIndex = firstFailureAudioIndex
				result = firstFailureResult
				toneMapCapabilityErr = firstFailureToneMapErr
			}
		}
	}
	result = retryIncompleteToneMapPlanningV3(result, toneMapCapabilityErr)
	result = retryIncompletePlaybackSettingsV3(result, settingsErr)
	h.clarifyOriginalQuality4KTerminalV3(r.Context(), result.Terminal, requestedFile, !shouldTryAlternateFileV3(req.QualityPreference))
	// The exact app identity is logged with every decision so a route or
	// terminal reported against one build is attributable without asking the
	// user which version they are running.
	clientInfo := playbackClientInfoForStartV3(r, req.ClientPlaybackContext)
	if result.Terminal != nil {
		slog.InfoContext(r.Context(), "playback plan decided", append([]any{
			logComponentKey, playbackLogValueV3,
			requestIDLogKeyV3, chimw.GetReqID(r.Context()),
			"outcome", "terminal",
			"reason", result.Terminal.Reason,
			"file_id", effectiveFile.ID,
			"quality_preference", req.QualityPreference,
		}, clientInfo.LogAttrs()...)...)
		response, persistErr := h.persistTerminalStartDecisionV3(r.Context(), userID, profileID, req, requestDigests, requestedFile.ID, effectiveFile.ID, playback.NewTerminalResponseV3(result.Terminal.Reason, result.Terminal.Message, result.Terminal.Retryable))
		if persistErr != nil {
			writeStartAttemptPersistenceErrorV3(w, persistErr)
			return
		}
		if response.Terminal != nil {
			h.enqueueRouteEventV3(playback.RouteEventRecordV3{RouteEventV3: playback.RouteEventV3{ProtocolVersion: playback.ProtocolV3, PlaybackAttemptID: req.PlaybackAttemptID, Event: playback.RouteEventTerminalV3, FallbackReason: response.Terminal.Reason, OutputContextID: req.ClientPlaybackContext.Output.OutputContextID}, UserID: userID, ProfileID: profileID, ClientName: clientInfo.Name, ClientVersion: clientInfo.Version, ClientBuild: clientInfo.Build, ClientChannel: clientInfo.Channel, ClientModel: req.ClientPlaybackContext.Device.Model})
		}
		writeJSON(w, http.StatusCreated, response)
		return
	}
	// A refused progressive remux is escalated before the decision is logged or
	// a session is opened, so the logged route is the one that will actually run.
	escalated, escalateErr := h.escalateRefusedProgressiveRemuxV3(r.Context(), headerAuthenticatedMediaV3(req.ClientFeatures),
		func() playback.PlannerInputV3 {
			return h.plannerInputV3(r.Context(), req, requestedFile, effectiveFile, audioIndex, nil)
		}, result)
	if escalateErr != nil {
		persistedResponse, persistErr := h.startFailureDecisionV3(r.Context(), userID, profileID, req, requestDigests, requestedFile.ID, effectiveFile.ID, escalateErr)
		if persistErr != nil {
			writeStartAttemptPersistenceErrorV3(w, persistErr)
			return
		}
		writeJSON(w, http.StatusCreated, persistedResponse)
		return
	}
	result = escalated
	timings.mark("remux_escalation")
	// One line per plan decision so route selection is reconstructible from
	// server logs alone (finding a mis-planned route previously required
	// correlating client logcat, ffmpeg commands, and session rows).
	slog.InfoContext(r.Context(), "playback plan decided", append([]any{
		logComponentKey, playbackLogValueV3,
		requestIDLogKeyV3, chimw.GetReqID(r.Context()),
		"outcome", "plan",
		"decision_reason", result.Plan.DecisionReason,
		"delivery", result.Plan.Delivery,
		"play_method", string(result.PlayMethod),
		"requested_file_id", requestedFile.ID,
		"effective_file_id", effectiveFile.ID,
		"dv_profile", result.Plan.Source.DVProfile,
		"dynamic_range", result.Plan.Source.DynamicRange,
		"target_resolution", result.TargetResolution,
		"target_bitrate_kbps", result.TargetBitrateKbps,
		"quality_preference", req.QualityPreference,
		"bandwidth_estimate_kbps", intOrZeroHandlerV3(req.BandwidthEstimateKbps),
	}, clientInfo.LogAttrs()...)...)
	result.Plan.DegradationWarnings = append(result.Plan.DegradationWarnings, warnings...)
	response, statusErr := h.startPlannedPlaybackV3(r, userID, profileID, req, requestDigests, requestedFile, effectiveFile, audioIndex, result, clientInfo)
	timings.mark("session_transport_commit")
	if statusErr != nil {
		if statusErr.reason == "playback_attempt_reused" {
			writeError(w, http.StatusConflict, "playback_attempt_reused", statusErr.message)
			return
		}
		failureAttrs := []any{
			logComponentKey, playbackLogValueV3,
			"reason", statusErr.reason,
			"retryable", statusErr.retryable,
			"playback_attempt_id", req.PlaybackAttemptID,
			"requested_file_id", requestedFile.ID,
			"effective_file_id", effectiveFile.ID,
		}
		if statusErr.cause != nil {
			failureAttrs = append(failureAttrs, "error", statusErr.cause)
		}
		switch {
		case statusErr.reason == policyErrorInternal:
			slog.ErrorContext(r.Context(), "protocol v3 planned playback failed", failureAttrs...)
		case statusErr.cause != nil:
			slog.WarnContext(r.Context(), "protocol v3 planned playback failed", failureAttrs...)
		default:
			slog.InfoContext(r.Context(), "protocol v3 planned playback failed", failureAttrs...)
		}
		persistedResponse, persistErr := h.startFailureDecisionV3(r.Context(), userID, profileID, req, requestDigests, requestedFile.ID, effectiveFile.ID, statusErr)
		if persistErr != nil {
			writeStartAttemptPersistenceErrorV3(w, persistErr)
			return
		}
		writeJSON(w, http.StatusCreated, persistedResponse)
		return
	}
	timings.mark("response_ready")
	writeJSON(w, http.StatusCreated, response)
}

type playbackStartRequestDigestsV3 struct {
	current string
	legacy  string
}

// playbackClientInfoForStartV3 resolves the client's app identity for a v3
// start request. The X-Silo-Client-* headers win because they are present on
// every request; the start body's client_playback_context is the fallback for
// clients that report their app identity only there. All values stay opaque —
// they are trimmed and length-clamped when the session stamps them.
//
// The fallback applies only to a client that named itself. client_playback_context
// carries no app name, so nothing in the body can identify a nameless client
// anyway — it is labeled from its user agent, and its app_version is a
// free-form platform string rather than the marketing version client_version
// promises. The web player, for one, reports the literal "web" there; taking it
// unconditionally would write "web" into the one field that is contractually
// semver, on every browser session.
func playbackClientInfoForStartV3(r *http.Request, clientContext playback.ClientPlaybackContextV3) playback.ClientInfo {
	info := playback.ClientInfoFromRequest(r)
	if info.Name == "" {
		return info
	}
	if info.Version == "" {
		info.Version = strings.TrimSpace(clientContext.AppVersion)
	}
	if info.Build == "" {
		info.Build = strings.TrimSpace(clientContext.AppBuild)
	}
	if info.Channel == "" {
		info.Channel = strings.TrimSpace(clientContext.AppChannel)
	}
	// The header half is already normalized; body-sourced values have to be
	// clamped too before they reach the decision log and the route event.
	return info.Normalized()
}

// playbackClientInfoWithSessionFallbackV3 completes a header-derived identity
// from the session the event belongs to. Route events posted out of band carry
// no client_playback_context, so without this a client that reports its build
// only in the start body would attribute its plan_selected event to a build and
// every later event of the same attempt to none.
func (h *PlaybackHandler) playbackClientInfoWithSessionFallbackV3(sessionID string, info playback.ClientInfo) playback.ClientInfo {
	if sessionID == "" || h.sessionMgr == nil {
		return info
	}
	if info.Name != "" && info.Version != "" && info.Build != "" && info.Channel != "" {
		return info
	}
	session, err := h.sessionMgr.GetSession(sessionID)
	if err != nil || session == nil {
		return info
	}
	stamped := session.ClientInfo()
	if info.Name == "" {
		info.Name = stamped.Name
	}
	if info.Version == "" {
		info.Version = stamped.Version
	}
	if info.Build == "" {
		info.Build = stamped.Build
	}
	if info.Channel == "" {
		info.Channel = stamped.Channel
	}
	return info
}

// newPlaybackStartRequestDigestsV3 fingerprints both the body and normalized
// device identity because either can change the selected playback plan. It
// also retains the pre-device digest while attempts written by an older
// server can still be replayed during a rolling deployment.
func newPlaybackStartRequestDigestsV3(body []byte, deviceID string) playbackStartRequestDigestsV3 {
	hasher := sha256.New()
	_, _ = fmt.Fprintf(hasher, "%d:", len(body))
	_, _ = hasher.Write(body)
	_, _ = hasher.Write([]byte(deviceID))
	legacy := sha256.Sum256(body)
	return playbackStartRequestDigestsV3{
		current: hex.EncodeToString(hasher.Sum(nil)),
		legacy:  hex.EncodeToString(legacy[:]),
	}
}

func (d playbackStartRequestDigestsV3) matches(stored string) bool {
	return stored == "" || stored == d.current || stored == d.legacy
}

// startPlannedPlaybackV3 creates a session and transport for an accepted plan.
func (h *PlaybackHandler) startPlannedPlaybackV3(r *http.Request, userID int, profileID string, req playback.StartRequestV3, requestDigests playbackStartRequestDigestsV3, requestedFile, effectiveFile *models.MediaFile, audioIndex int, result playback.PlannerResultV3, clientInfo playback.ClientInfo) (playback.DecisionResponseV3, *transportErrorV3) {
	if result.Plan == nil {
		return playback.DecisionResponseV3{}, &transportErrorV3{reason: "internal_error", message: "The server produced no playback plan."}
	}
	mode := headerAuthenticatedMediaV3(req.ClientFeatures)
	if checker, ok := h.sessionMgr.(transcodePermissionChecker); ok && (result.PlayMethod == playback.PlayTranscode || result.TranscodeAudio) {
		if err := checker.CheckTranscodingAllowed(r.Context(), userID, result.PlayMethod == playback.PlayTranscode); err != nil {
			reason := "transcoding_disabled"
			if errors.Is(err, playback.ErrAudioTranscodingDisabled) {
				reason = "audio_transcoding_disabled"
			}
			return playback.DecisionResponseV3{}, &transportErrorV3{reason: reason, message: "The selected server adaptation is disabled for this user."}
		}
	}
	ctx := playback.WithClientInfo(r.Context(), clientInfo)
	session, err := h.sessionMgr.StartSessionWithFilesContext(ctx, userID, profileID, effectiveFile.ID, requestedFile.ID, result.PlayMethod, result.TranscodeAudio)
	if err != nil {
		return playback.DecisionResponseV3{}, sessionStartErrorV3(err)
	}
	abort := func() { _ = h.stopPlaybackSessionByID(context.WithoutCancel(r.Context()), session.ID, false) }
	if req.ProgressPersistence == playback.ProgressPersistenceClientV3 || !sessionOwnsResumeTimelineV3(effectiveFile) {
		if err := h.sessionMgr.SetProgressPersistenceDisabled(session.ID, true); err != nil {
			abort()
			return playback.DecisionResponseV3{}, &transportErrorV3{
				reason:  "internal_error",
				message: "Failed to establish the requested progress persistence policy.",
				cause:   err,
			}
		}
	}
	if err := h.sessionMgr.UpdateAudioTrack(session.ID, audioIndex, result.PlayMethod); err != nil {
		abort()
		return playback.DecisionResponseV3{}, &transportErrorV3{reason: "internal_error", message: "Failed to select the playback audio track.", cause: err}
	}
	position := floatOrZeroHandlerV3(req.StartPosition)
	if err := h.sessionMgr.UpdateProgress(session.ID, position, false); err != nil {
		abort()
		return playback.DecisionResponseV3{}, &transportErrorV3{reason: "internal_error", message: "Failed to initialize the playback timeline.", cause: err}
	}
	session, err = h.sessionMgr.GetSession(session.ID)
	if err != nil {
		abort()
		return playback.DecisionResponseV3{}, &transportErrorV3{reason: "internal_error", message: "Failed to load the initialized playback session.", cause: err}
	}
	result.Plan.SessionID = session.ID
	// The recipe is frozen after the transport, not before: only a started
	// transport knows which tone-map executor the node actually confirmed, and
	// the frozen recipe has to record that rather than the requested one.
	transport, transportErr := h.prepareTransportV3(r, session, effectiveFile, result, mode)
	if transportErr != nil {
		abort()
		return playback.DecisionResponseV3{}, transportErr
	}
	applyTransportToneMapModeV3(&result, transport)
	frozenRecipe, frozenErr := h.freezeExecutableRecipeV3(r.Context(), effectiveFile, result)
	if frozenErr != nil {
		transport.rollback()
		abort()
		return playback.DecisionResponseV3{}, subtitleArtifactErrorV3("Failed to freeze the selected subtitle identity.", frozenErr)
	}
	result.Plan.Stream.URL = transport.url
	if err := h.attachSubtitleArtifactV3(r.Context(), session.ID, effectiveFile, result.Plan, result.SubtitleTrackIndex, &frozenRecipe); err != nil {
		transport.rollback()
		abort()
		return playback.DecisionResponseV3{}, subtitleArtifactErrorV3("Failed to prepare the selected subtitle artifact.", err)
	}
	response := playback.DecisionResponseV3{ProtocolVersion: playback.ProtocolV3, ServerFeatures: playback.ServerFeaturesV3(), Outcome: playback.OutcomePlayableV3, SessionID: session.ID, PlaybackPlan: result.Plan}
	record := playback.AttemptRecordV3{PlaybackAttemptID: req.PlaybackAttemptID, SessionID: session.ID, UserID: userID, ProfileID: profileID, RequestedMediaFileID: requestedFile.ID, EffectiveMediaFileID: effectiveFile.ID, CurrentPlanID: result.Plan.PlanID, CurrentPlan: *result.Plan, FrozenRecipe: frozenRecipe, NormalizedRequest: req, StartResponse: response, RequestDigest: requestDigests.current, ExpiresAt: time.Now().Add(playback.MaxTokenTTL)}
	if err := h.updateV3SessionState(r.Context(), session, effectiveFile, result, transport, mode); err != nil {
		transport.rollback()
		abort()
		return playback.DecisionResponseV3{}, &transportErrorV3{reason: "internal_error", message: "Failed to commit the live playback session.", cause: err}
	}
	if err := h.PlanStoreV3.SaveAttempt(r.Context(), record); err != nil {
		transport.rollback()
		abort()
		if errors.Is(err, playback.ErrPlaybackAttemptExistsV3) || errors.Is(err, playback.ErrIdempotencyKeyReusedV3) {
			existing, lookupErr := h.PlanStoreV3.GetAttemptByPlaybackAttemptID(r.Context(), req.PlaybackAttemptID)
			if lookupErr == nil && existing.UserID == userID && existing.ProfileID == profileID && existing.RequestedMediaFileID == req.FileID && requestDigests.matches(existing.RequestDigest) {
				// Replaying a concurrent duplicate is only valid while its
				// session is alive; otherwise tell the client to mint a new
				// attempt rather than hand it a plan it can never stream.
				if _, sessionErr := h.sessionMgr.GetSession(existing.SessionID); sessionErr != nil {
					return playback.DecisionResponseV3{}, &transportErrorV3{reason: "session_expired", message: "The playback session for this attempt has ended.", retryable: true}
				}
				return decisionResponseFromAttemptV3(existing), nil
			}
			if errors.Is(err, playback.ErrIdempotencyKeyReusedV3) {
				return playback.DecisionResponseV3{}, &transportErrorV3{reason: "playback_attempt_reused", message: "The playback attempt ID was reused with different input."}
			}
		}
		return playback.DecisionResponseV3{}, &transportErrorV3{reason: "internal_error", message: "Failed to persist the playback plan.", cause: err}
	}
	transport.commit()
	// Start-side effects belong after both the attempt and transport commits:
	// retries that lose the idempotency race must not emit duplicate provider
	// scrobbles or analysis work for the short-lived session they roll back.
	h.raceCopySafetyV3(effectiveFile.ID, result.Plan)
	h.enqueuePlaybackStartSideEffectsV3(r.Context(), session, effectiveFile, userID, profileID, plannedAudioTrackIndexV3(result, audioIndex))
	h.enqueueRouteEventV3(playback.RouteEventRecordV3{RouteEventV3: playback.RouteEventV3{ProtocolVersion: playback.ProtocolV3, PlaybackAttemptID: req.PlaybackAttemptID, SessionID: session.ID, PlanID: result.Plan.PlanID, Event: playback.RouteEventPlanSelectedV3, AppliedQuirkIDs: appliedQuirkIDsV3(result.Plan), QuirkRegistryRevision: appliedQuirkRevisionV3(result.Plan), OutputContextID: req.ClientPlaybackContext.Output.OutputContextID}, UserID: userID, ProfileID: profileID, ClientName: clientInfo.Name, ClientVersion: clientInfo.Version, ClientBuild: clientInfo.Build, ClientChannel: clientInfo.Channel, ClientModel: req.ClientPlaybackContext.Device.Model})
	return response, nil
}

func (h *PlaybackHandler) enqueuePlaybackStartSideEffectsV3(ctx context.Context, session *playback.Session, file *models.MediaFile, userID int, profileID string, audioTrackIndex int) {
	if h == nil || session == nil || file == nil {
		return
	}
	h.v3StartEffectsOnce.Do(func() {
		h.v3StartEffectsQueue = make(chan playbackStartSideEffectsV3, playbackStartSideEffectsQueueSizeV3)
		h.v3StartEffectsPending = make(map[string]*playbackStartSideEffectsStateV3)
		for range playbackStartSideEffectsWorkersV3 {
			go func() {
				for task := range h.v3StartEffectsQueue {
					h.runPlaybackStartSideEffectsV3(task)
				}
			}()
		}
	})

	taskCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), playbackStartSideEffectsTimeoutV3)
	state := &playbackStartSideEffectsStateV3{
		ctx:    taskCtx,
		cancel: cancel,
		done:   make(chan struct{}),
	}
	task := playbackStartSideEffectsV3{
		session:         *session,
		file:            *file,
		userID:          userID,
		profileID:       profileID,
		audioTrackIndex: audioTrackIndex,
		state:           state,
	}
	h.v3StartEffectsMu.Lock()
	h.v3StartEffectsPending[session.ID] = state
	h.v3StartEffectsMu.Unlock()
	select {
	case h.v3StartEffectsQueue <- task:
	default:
		// Preserve side-effect durability and ordering under overload. The queue
		// bounds background work; a saturated server pays the old synchronous
		// cost instead of silently dropping a provider event or preference write.
		h.runPlaybackStartSideEffectsV3(task)
	}
}

func (h *PlaybackHandler) runPlaybackStartSideEffectsV3(task playbackStartSideEffectsV3) {
	state := task.state
	if state == nil {
		return
	}
	h.v3StartEffectsMu.Lock()
	state.started = true
	stopRequested := state.stopRequested
	h.v3StartEffectsMu.Unlock()
	defer func() {
		state.cancel()
		close(state.done)
		h.v3StartEffectsMu.Lock()
		if h.v3StartEffectsPending[task.session.ID] == state {
			delete(h.v3StartEffectsPending, task.session.ID)
		}
		h.v3StartEffectsMu.Unlock()
	}()
	if stopRequested || state.ctx.Err() != nil {
		return
	}
	ctx := state.ctx

	if !task.session.DisableProgressPersistence && h.WatchScrobbler != nil {
		targetID := playbackProgressTarget(&task.file)
		if targetID != "" {
			event := h.scrobbleEventForSession(ctx, &task.session, targetID, float64(task.file.Duration), task.session.Position)
			if err := h.WatchScrobbler.ScrobbleStart(ctx, event); err != nil {
				slog.WarnContext(ctx, "failed to queue watch provider start scrobble", "component", "api", "session", task.session.ID, "error", err)
			}
		}
	}
	if ctx.Err() != nil {
		return
	}
	if h.ChapterThumbnailQueuer != nil {
		slog.InfoContext(ctx,
			"queueing chapter thumbnails", "component", "api",
			"source", "playback_start",
			"content_id", task.file.ContentID,
			"file_id", task.file.ID,
			"target_seconds", task.session.Position,
		)
		h.ChapterThumbnailQueuer.QueuePriorityFileAtPosition(ctx, task.file.ID, task.session.Position)
	}
	if ctx.Err() != nil {
		return
	}
	h.maybeQueueLazyPlaybackMarkers(ctx, &task.session, &task.file)
	if ctx.Err() != nil {
		return
	}
	h.persistSeriesPlaybackPreference(ctx, task.userID, task.profileID, &task.file)
	h.persistCurrentAudioPreferenceV3(ctx, task.session.ID, task.userID, task.profileID, &task.file, task.audioTrackIndex)
	h.syncSessionsNow(ctx, "v3_start")
}

func (h *PlaybackHandler) waitForPlaybackStartSideEffectsV3(ctx context.Context, sessionID string) {
	if h == nil || sessionID == "" {
		return
	}
	h.v3StartEffectsMu.Lock()
	state := h.v3StartEffectsPending[sessionID]
	h.v3StartEffectsMu.Unlock()
	if state == nil {
		return
	}
	waitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), playbackStartSideEffectsTimeoutV3)
	defer cancel()
	select {
	case <-state.done:
	case <-waitCtx.Done():
		slog.WarnContext(ctx, "timed out waiting for playback start side effects", logComponentKey, "api", "session", sessionID)
	}
}

func (h *PlaybackHandler) cancelPlaybackStartSideEffectsV3(ctx context.Context, sessionID string) {
	if h == nil || sessionID == "" {
		return
	}
	h.v3StartEffectsMu.Lock()
	state := h.v3StartEffectsPending[sessionID]
	if state == nil {
		h.v3StartEffectsMu.Unlock()
		return
	}
	state.stopRequested = true
	state.cancel()
	started := state.started
	done := state.done
	h.v3StartEffectsMu.Unlock()
	// A queued task observes stopRequested before issuing any side effect. A
	// running task is allowed to unwind so provider start cannot cross the stop.
	if !started {
		return
	}
	waitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), playbackStartSideEffectsTimeoutV3)
	defer cancel()
	select {
	case <-done:
	case <-waitCtx.Done():
		slog.WarnContext(ctx, "timed out canceling playback start side effects", logComponentKey, "api", "session", sessionID)
	}
}

// persistCurrentAudioPreferenceV3 serializes start and replan preference writes
// and rejects a delayed start value after the live session has already adopted
// a newer track. The lock makes the final write match replacement commit order.
func (h *PlaybackHandler) persistCurrentAudioPreferenceV3(ctx context.Context, sessionID string, userID int, profileID string, file *models.MediaFile, audioTrackIndex int) {
	h.v3AudioPreferenceMu.Lock()
	defer h.v3AudioPreferenceMu.Unlock()
	current, err := h.sessionMgr.GetSession(sessionID)
	if err != nil || current.AudioTrackIndex != audioTrackIndex {
		return
	}
	h.persistAudioPreference(ctx, userID, profileID, file, audioTrackIndex)
}

// prepareTransportV3 resolves the plan into a live local, remote, or identity
// transport. mode is the attempt's negotiated media-auth mode, resolved once by
// the caller and threaded down every branch (like localEgress) rather than
// re-derived per URL builder.
func (h *PlaybackHandler) prepareTransportV3(r *http.Request, session *playback.Session, file *models.MediaFile, result playback.PlannerResultV3, mode mediaAuthModeV3) (preparedTransportV3, *transportErrorV3) {
	timeline, timelineErr := h.prepareTransportTimelineV3(r.Context(), session, file, result)
	if timelineErr != nil {
		return preparedTransportV3{}, timelineErr
	}
	if result.Plan.Delivery != playback.DeliveryTranscodeHLSV3 && result.Plan.Delivery != playback.DeliveryRemuxHLSV3 {
		return h.prepareIdentityTransportV3(r, session, file, result, timeline, mode)
	}
	localFallbackAllowed := h.NodePlanner == nil || nodepool.LocalTranscodeFallbackAllowed(r.Context(), h.SettingsRepo)
	if h.NodePlanner != nil {
		// Local egress is the header-authenticated mode WITHOUT authorized
		// origins: the API is then the only client-facing media origin, so a
		// proxy must not be selected at all. With authorized origins the normal
		// proxy+transcode pairing applies again.
		plan := h.planNodeSessionV3(r.Context(), session, result, mode.headerAuth && !mode.proxyEgress)
		if plan.TranscodeNode == nil && !localFallbackAllowed {
			if fallback, attempted, fallbackErr := h.prepareSoftwareToneMapFallbackV3(r, session, file, result, timeline, mode); attempted {
				return fallback, fallbackErr
			}
		}
		if plan.TranscodeNode != nil {
			transformations, err := h.remoteTransformationsV3(r.Context(), plan.TranscodeNode.URL)
			if err == nil {
				err = validateAdvertisedTransformationsV3(result.Plan, transformations)
			}
			if err == nil && planRequiresToneMapV3(result.Plan) {
				capabilities, capabilityErr := h.remoteToneMapCapabilitiesV3(r.Context(), plan.TranscodeNode.URL, false)
				if capabilityErr != nil {
					err = capabilityErr
				} else {
					err = validateToneMapExecutorV3(result, capabilities)
				}
			}
			if err == nil {
				transport, transportErr := h.prepareRemoteTransportV3(r, session, file, result, plan, timeline, mode)
				if transportErr == nil {
					return transport, nil
				}
				if releaser, ok := h.NodePlanner.(sessionReservationReleaserV3); ok {
					releaser.ReleaseSession(session.ID)
				}
				if fallback, attempted, fallbackErr := h.prepareSoftwareToneMapFallbackV3(r, session, file, result, timeline, mode); attempted {
					if fallbackErr != nil {
						fallbackErr = combineTransportErrorsV3(transportErr, fallbackErr)
					}
					return fallback, fallbackErr
				}
				return preparedTransportV3{}, transportErr
			}
			slog.WarnContext(r.Context(), "protocol v3 transcode node capability mismatch", "node", logredact.SanitizeURL(plan.TranscodeNode.URL), "error", err)
			if releaser, ok := h.NodePlanner.(sessionReservationReleaserV3); ok {
				releaser.ReleaseSession(session.ID)
			}
			if fallback, attempted, fallbackErr := h.prepareSoftwareToneMapFallbackV3(r, session, file, result, timeline, mode); attempted {
				return fallback, fallbackErr
			}
			if !nodepool.LocalTranscodeFallbackAllowed(r.Context(), h.SettingsRepo) {
				return preparedTransportV3{}, &transportErrorV3{reason: "transcode_node_capability_unavailable", message: "No transcode node can execute the selected playback recipe.", retryable: true, cause: err}
			}
		}
		if !localFallbackAllowed {
			return preparedTransportV3{}, &transportErrorV3{reason: "capacity_unavailable", message: "No transcode node is available and local fallback is disabled.", retryable: true}
		}
	}
	// Capability-union planning may select transformations only pooled nodes
	// can execute; the local binary must prove it carries the recipe before
	// this fallback spawns an ffmpeg that would fail at runtime. Retryable:
	// a capable node freeing up satisfies the same plan. Transformation-free
	// plans skip the check (and the local probe behind it) entirely.
	if capabilityErr := h.validateLocalTransportCapabilitiesV3(r.Context(), result); capabilityErr != nil {
		if fallback, attempted, fallbackErr := h.prepareSoftwareToneMapFallbackV3(r, session, file, result, timeline, mode); attempted {
			if fallbackErr != nil {
				fallbackErr = combineTransportErrorsV3(capabilityErr, fallbackErr)
			}
			return fallback, fallbackErr
		}
		return preparedTransportV3{}, capabilityErr
	}
	return h.prepareLocalTransportV3(r, session, file, result, timeline, mode)
}

// canRetrySoftwareToneMapV3 permits a software retry only for an initial
// hardware selection whose policy allows it; frozen recovery recipes stay exact.
func canRetrySoftwareToneMapV3(result playback.PlannerResultV3) bool {
	return result.FrozenSourceMetadata == nil && result.ToneMapMode == tonemap.ModeHardware &&
		result.ToneMapPolicy.Allows(tonemap.ModeSoftware)
}

// prepareSoftwareToneMapFallbackV3 retries an eligible failed hardware recipe
// on a software-capable remote node or, when allowed, on the API host.
func (h *PlaybackHandler) prepareSoftwareToneMapFallbackV3(r *http.Request, session *playback.Session, file *models.MediaFile, result playback.PlannerResultV3, timeline preparedTimelineV3, mode mediaAuthModeV3) (preparedTransportV3, bool, *transportErrorV3) {
	if !canRetrySoftwareToneMapV3(result) {
		return preparedTransportV3{}, false, nil
	}
	fallbackResult := result
	fallbackResult.ToneMapMode = tonemap.ModeSoftware
	excluded := make(map[string]struct{})
	var lastRemoteErr *transportErrorV3
	for {
		fallbackPlan := h.planNodeSessionExcludingV3(r.Context(), session, fallbackResult, mode.headerAuth && !mode.proxyEgress, excluded)
		if fallbackPlan.TranscodeNode == nil {
			break
		}
		nodeURL := fallbackPlan.TranscodeNode.URL
		if _, repeated := excluded[nodeURL]; repeated {
			break
		}
		fallback, fallbackErr := h.prepareRemoteTransportV3(r, session, file, fallbackResult, fallbackPlan, timeline, mode)
		if fallbackErr == nil {
			return fallback, true, nil
		}
		if releaser, ok := h.NodePlanner.(sessionReservationReleaserV3); ok {
			releaser.ReleaseSession(session.ID)
		}
		excluded[nodeURL] = struct{}{}
		lastRemoteErr = combineTransportErrorsV3(lastRemoteErr, fallbackErr)
	}
	if !nodepool.LocalTranscodeFallbackAllowed(r.Context(), h.SettingsRepo) {
		if lastRemoteErr != nil {
			return preparedTransportV3{}, true, lastRemoteErr
		}
		return preparedTransportV3{}, false, nil
	}
	if capabilityErr := h.validateLocalTransportCapabilitiesV3(r.Context(), fallbackResult); capabilityErr != nil {
		return preparedTransportV3{}, true, capabilityErr
	}
	fallback, fallbackErr := h.prepareLocalTransportV3(r, session, file, fallbackResult, timeline, mode)
	if fallbackErr != nil && lastRemoteErr != nil {
		fallbackErr = combineTransportErrorsV3(lastRemoteErr, fallbackErr)
	}
	return fallback, true, fallbackErr
}

func (h *PlaybackHandler) validateLocalTransportCapabilitiesV3(ctx context.Context, result playback.PlannerResultV3) *transportErrorV3 {
	if !planRequiresServerTransformationsV3(result.Plan) {
		return nil
	}
	localRegistry, capabilityErr := h.localHLSExecutionRegistryV3(ctx)
	if capabilityErr != nil {
		return &transportErrorV3{reason: "transcode_node_capability_unavailable", message: "Local transcode capability validation is temporarily unavailable.", retryable: true, cause: capabilityErr}
	}
	if err := validateAdvertisedTransformationsV3(result.Plan, localRegistry.Advertised()); err != nil {
		return &transportErrorV3{reason: "transcode_node_capability_unavailable", message: "No available transcode executor can run the selected playback recipe.", retryable: true, cause: err}
	}
	if !planRequiresToneMapV3(result.Plan) {
		return nil
	}
	capabilities, capabilityErr := h.localToneMapCapabilitiesForTransportV3(ctx)
	if capabilityErr != nil {
		return &transportErrorV3{reason: "transcode_node_capability_unavailable", message: "Local tone-map capability validation is temporarily unavailable.", retryable: true, cause: capabilityErr}
	}
	if err := validateToneMapExecutorV3(result, capabilities); err != nil {
		return &transportErrorV3{reason: "transcode_node_capability_unavailable", message: "No available transcode executor can run the selected tone-map recipe.", retryable: true, cause: err}
	}
	return nil
}

func (h *PlaybackHandler) prepareTransportTimelineV3(ctx context.Context, session *playback.Session, file *models.MediaFile, result playback.PlannerResultV3) (preparedTimelineV3, *transportErrorV3) {
	if result.Plan == nil {
		return preparedTimelineV3{}, nil
	}

	requested := result.Plan.Timeline.SourceStartSeconds
	switch result.Plan.Delivery {
	case playback.DeliveryRemuxProgressiveV3, playback.DeliveryRemuxHLSV3:
		// Audio-only remuxes have no copied video keyframe to resolve. Keep the
		// requested position as their exact stream origin: the chunked output
		// clock restarts at zero and later seeks require another server reanchor.
		if file != nil && file.IsAudioOnly() {
			configureCopyRemuxTimelineV3(result.Plan, requested)
			return preparedTimelineV3{seekSeconds: requested, streamOriginSeconds: requested}, nil
		}
		origin, startSegment := 0.0, 0
		if requested > 0 {
			if file == nil || strings.TrimSpace(file.FilePath) == "" {
				return preparedTimelineV3{}, &transportErrorV3{reason: transcodeStartFailedReasonV3, message: "Failed to resolve remux seek position.", retryable: true, cause: errors.New("copy seek anchor requires a media file path")}
			}
			resolver := h.copySeekAnchor
			if resolver == nil {
				resolver = playback.ResolveCopySeekAnchor
			}
			var err error
			origin, startSegment, err = resolver(ctx, h.playbackConfig().FFmpegPath, file.FilePath, requested, 2)
			if err != nil {
				slog.ErrorContext(ctx, "failed to resolve protocol v3 copy-video seek anchor",
					"component", "api",
					"playback_session_id", session.ID,
					"requested_seek_seconds", requested,
					"error", err,
				)
				return preparedTimelineV3{}, &transportErrorV3{reason: transcodeStartFailedReasonV3, message: "Failed to resolve remux seek position.", retryable: true, cause: err}
			}
		}
		configureCopyRemuxTimelineV3(result.Plan, origin)
		return preparedTimelineV3{seekSeconds: requested, streamOriginSeconds: origin, startSegmentNumber: startSegment, copySeekAnchorResolved: true}, nil
	case playback.DeliveryTranscodeHLSV3:
		sourceMetadata := sourceExecutionMetadataV3(file, result)
		seekSeconds, startSegment := configureHLSTimelineV3(result.Plan, result.TargetVideoCodec, playback.DefaultSegmentDuration, sourceMetadata.DurationSeconds)
		return preparedTimelineV3{seekSeconds: seekSeconds, streamOriginSeconds: result.Plan.Timeline.StreamOriginSeconds, startSegmentNumber: startSegment}, nil
	default:
		return preparedTimelineV3{}, nil
	}
}

// planRequiresServerTransformationsV3 reports whether the plan carries any
// transformation the serving executor (local binary or transcode node) must
// perform, as opposed to client-executed ones.
func planRequiresServerTransformationsV3(plan *playback.PlanV3) bool {
	if plan == nil {
		return false
	}
	for _, transformation := range plan.Transformations {
		if !strings.EqualFold(transformation.Executor, playback.ExecutorClientV3) {
			return true
		}
	}
	return false
}

func (h *PlaybackHandler) prepareIdentityTransportV3(r *http.Request, session *playback.Session, file *models.MediaFile, result playback.PlannerResultV3, timeline preparedTimelineV3, mode mediaAuthModeV3) (preparedTransportV3, *transportErrorV3) {
	routeSession := *session
	// The URL builders below refuse to mint a stream token for a session that
	// requires media authorization. The live session only learns the mode when
	// its stream state is committed, so stamp the route copy the builders see.
	routeSession.RequireMediaAuthorization = mode.headerAuth
	routeSession.PlayMethod = result.PlayMethod
	routeSession.BasePlayMethod = result.PlayMethod
	routeSession.MediaFileID = result.Plan.EffectiveMediaFileID
	routeSession.AudioTrackIndex = plannedAudioTrackIndexV3(result, session.AudioTrackIndex)
	routeSession.TranscodeAudio = result.TranscodeAudio
	routeSession.TargetAudioCodec = result.TargetAudioCodec
	routeSession.SourceAudioChannels = result.SourceAudioChannels
	routeSession.TargetAudioChannels = result.TargetAudioChannels
	routeSession.TargetAudioBitrateKbps = result.TargetAudioBitrateKbps
	routeSession.RemuxDVMode = remuxDVModeForPlanV3(result.Plan)

	var proxyNode *nodepool.Node
	if mode.headerAuth && !mode.proxyEgress {
		// The legacy proxy identity routes authenticate with a signed token in
		// the URL path. Without authorized origins this mode keeps everything on
		// the authenticated API origin instead, so no client-visible URL can
		// carry or disclose that credential.
		//
		// A remux that must run ffmpeg here has already been escalated onto an
		// HLS delivery (or refused outright) before the session started; this
		// call only refuses the residual cases.
		if localErr := h.refuseLocalIdentityWorkV3(r, result); localErr != nil {
			return preparedTransportV3{}, localErr
		}
	} else {
		// Legacy and authorized-origin attempts plan a proxy identically; only
		// the URL they publish for it differs (signed token path vs. grant).
		var proxyErr *transportErrorV3
		proxyNode, proxyErr = h.planIdentityProxyV3(r, session.ID, result, mode)
		if proxyErr != nil {
			return preparedTransportV3{}, proxyErr
		}
	}
	streamURL := fmt.Sprintf("/stream/%s", routeSession.ID)
	servedByProxy := false
	// priorGrant is the egress authority this attempt overwrote, if any. A
	// replan of a session that was already proxy-served must be able to put it
	// back: rolling back to the restored old plan leaves that plan's published
	// proxy URL live, and a deleted grant would 404 it.
	var priorGrant *playback.RecipeCard
	switch {
	case !mode.headerAuth:
		streamURL, servedByProxy = h.identityStreamURLV3(&routeSession, file, proxyNode)
	case mode.proxyEgress:
		streamURL, servedByProxy, priorGrant = h.identityGrantStreamURLV3(r.Context(), &routeSession, file, proxyNode)
	}
	releaseProxyReservation := func() {
		if releaser, ok := h.NodePlanner.(sessionReservationReleaserV3); ok {
			releaser.ReleaseSession(session.ID)
		}
	}
	if proxyNode != nil && !servedByProxy {
		// A planned proxy that could not be addressed (no signable token, no
		// file record, no writable grant) falls back to the local path, so its
		// reservation must be dropped now rather than pinning that node's budget
		// until it ages out.
		//
		// The fallback is local execution, so it honors the same
		// local-fallback gate the no-origins mode enforces: an authorized-origins
		// remux whose grant could not be written must not quietly spawn the
		// ffmpeg the operator disabled. Start-time escalation cannot cover this
		// case — it was legitimately skipped because the pool does offer a proxy.
		releaseProxyReservation()
		if err := h.refuseLocalIdentityWorkV3(r, result); err != nil {
			return preparedTransportV3{}, err
		}
	}

	previousNodeURL := session.TranscodeNodeURL
	previousTransportID := remoteTransportID(session)
	unlock := h.tm.LockSessionLifecycle(session.ID)
	committed := false
	if result.Plan != nil && result.Plan.Delivery == playback.DeliveryRemuxProgressiveV3 {
		if seek := timeline.seekSeconds; seek > 0 {
			streamURL = appendPlaybackQueryV3(streamURL, "seek", strconv.FormatFloat(seek, 'f', -1, 64))
		}
	}
	return preparedTransportV3{
		url: streamURL,
		commit: func() {
			if committed {
				return
			}
			committed = true
			h.tm.CloseTranscodeSession(session.ID, "")
			if previousNodeURL != "" {
				h.tm.StopRemoteTranscode(previousTransportID, previousNodeURL)
				h.deleteNodeRecipeV3(r.Context(), previousTransportID)
			}
			h.revokeStaleProxyGrantOnCommitV3(r.Context(), session.ID, mode, servedByProxy)
			h.applyRemoteTransportMarkV3(r.Context(), session.ID, servedByProxy)
			unlock()
		},
		rollback: func() {
			if committed {
				return
			}
			committed = true
			// The session never reached the client, so a proxy admitted for it
			// must not keep consuming that node's job/bandwidth budget until the
			// reservation ages out — nor keep an egress grant for a transport
			// that was never committed.
			if servedByProxy {
				releaseProxyReservation()
				h.restoreProxyGrantV3(r.Context(), session.ID, priorGrant)
			}
			unlock()
		},
	}, nil
}

// revokeStaleProxyGrantOnCommitV3 drops the egress grant when an
// authorized-origins attempt commits onto a transport this server serves
// itself. A replan that moves a proxy-served session onto the API origin (or
// onto a local transcode) publishes a URL the proxy has no part in, and the
// surviving grant would keep the proxy authorized to serve the previous recipe
// for the rest of its TTL.
func (h *PlaybackHandler) revokeStaleProxyGrantOnCommitV3(ctx context.Context, sessionID string, mode mediaAuthModeV3, servedByProxy bool) {
	if !mode.proxyEgress || servedByProxy {
		return
	}
	h.deleteProxyGrantV3(ctx, sessionID)
}

// identityGrantStreamURLV3 builds the stream URL for a direct-play or
// progressive-remux session that negotiated authorized media origins: an
// absolute, credential-free proxy URL backed by a server-side grant, otherwise
// the API-local path.
//
// The proxy serves from the grant alone, so the grant has to carry everything
// the API-local path would have read from the session and the file record — the
// media path it opens, and the source facts (Dolby Vision profile, audio-only)
// its remux needs. Omitting either would not fail loudly: the proxy would serve
// a subtly different stream than the plan promised.
//
// The bool reports whether the returned URL is actually a proxy URL, so the
// caller can release the planner reservation when it is not. A grant that
// cannot be written is not fatal: this attempt simply stays on the API origin,
// which is exactly the behavior of a header-authenticated attempt that
// negotiated no origins at all. The third value is the grant this write
// displaced, for the caller's rollback.
func (h *PlaybackHandler) identityGrantStreamURLV3(ctx context.Context, s *playback.Session, file *models.MediaFile, proxyNode *nodepool.Node) (string, bool, *playback.RecipeCard) {
	if proxyNode == nil || file == nil || s == nil {
		return h.playbackStreamURL(s), false, nil
	}
	card := identityRecipeCard(s)
	card.InputPath = file.FilePath
	card.DVProfile = file.PrimaryDVProfile()
	card.AudioOnly = file.IsAudioOnly()
	prior, stored := h.putProxyGrantV3(ctx, s.ID, card)
	if !stored {
		return h.playbackStreamURL(s), false, nil
	}
	return strings.TrimRight(proxyNode.URL, "/") + "/stream/v3/" + s.ID, true, prior
}

// putProxyGrantV3 stores the recipe a designated proxy origin serves this
// session from, reporting whether the grant is actually retrievable. A replan
// overwrites the previous grant under the same session id, so the grant it
// displaces is returned for the caller to thread into its rollback: a
// replacement that fails to commit restores the old plan, and that plan's
// already-published proxy URL is only serviceable while its grant exists.
//
// The overwrite is deliberately not staged behind the commit. Between this Put
// and the transport commit the previously published client URL resolves the new
// recipe — same session, same user, same media authority, bounded by the replan
// window — which is the accepted cost of keeping one grant per session.
//
// A disabled store is a negative answer rather than a silent success: it
// accepts writes it cannot retrieve (the Redis-less integrated box), and
// publishing a proxy URL against one would hand the client a route that 404s.
func (h *PlaybackHandler) putProxyGrantV3(ctx context.Context, sessionID string, card playback.RecipeCard) (*playback.RecipeCard, bool) {
	if h.ProxyGrantStore == nil || !h.ProxyGrantStore.Enabled() || sessionID == "" {
		return nil, false
	}
	prior, hadPrior := h.ProxyGrantStore.Get(ctx, sessionID)
	if !hadPrior {
		prior = nil
	}
	if err := h.ProxyGrantStore.Put(ctx, sessionID, card); err != nil {
		slog.WarnContext(ctx, "protocol v3 proxy egress grant write failed; serving from the API origin",
			"component", "api", "playback_session_id", sessionID, "error", err)
		return nil, false
	}
	return prior, true
}

// restoreProxyGrantV3 undoes a replan's grant overwrite. A session that was
// already egressing from a proxy keeps serving its restored plan, so its grant
// has to come back rather than be revoked; a session that had none is revoked
// as before, because a grant for a transport that never committed would point a
// proxy at work that no longer exists.
func (h *PlaybackHandler) restoreProxyGrantV3(ctx context.Context, sessionID string, prior *playback.RecipeCard) {
	if h == nil || h.ProxyGrantStore == nil || sessionID == "" {
		return
	}
	if prior == nil {
		h.deleteProxyGrantV3(ctx, sessionID)
		return
	}
	if err := h.ProxyGrantStore.Put(context.WithoutCancel(ctx), sessionID, *prior); err != nil {
		slog.WarnContext(ctx, "failed to restore the previous proxy egress grant",
			"component", "api", "playback_session_id", sessionID, "error", err)
	}
}

// deleteProxyGrantV3 revokes a session's proxy egress authority. It runs
// wherever the session ends or its transport fails to commit: a grant that
// outlived its session would let a proxy keep serving bytes for playback the
// server considers over.
func (h *PlaybackHandler) deleteProxyGrantV3(ctx context.Context, sessionID string) {
	if h == nil || h.ProxyGrantStore == nil || sessionID == "" {
		return
	}
	if err := h.ProxyGrantStore.Delete(context.WithoutCancel(ctx), sessionID); err != nil {
		slog.WarnContext(ctx, "failed to revoke proxy egress grant",
			"component", "api", "playback_session_id", sessionID, "error", err)
	}
}

// putNodeRecipeV3 hands the transcode node the recipe it rebuilds this job from
// after a restart, keyed by the transport id the node serves it under (which is
// the id in every relayed node URL, not the playback session id).
//
// It exists because a header-authenticated attempt publishes no stream token, so
// neither the client nor this server's relay has a recipe to forward when the
// node comes back empty — the node would 404 until the client replanned. A node
// dying mid-stream is a normal event, so tokenless playback recovers from it the
// way a legacy token attempt already does.
//
// Best effort, exactly like the jellycompat handoff: the write is bounded and a
// failure only forfeits restart resilience for this session, never the start.
func (h *PlaybackHandler) putNodeRecipeV3(ctx context.Context, transportID string, card playback.RecipeCard) {
	if h == nil || h.NodeRecipeStore == nil || transportID == "" {
		return
	}
	putCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), nodeRecipeWriteTimeoutV3)
	defer cancel()
	if err := h.NodeRecipeStore.Put(putCtx, transportID, card); err != nil {
		slog.WarnContext(ctx, "persist node transcode recipe failed; this session cannot survive a node restart",
			"component", "api", "playback_session_id", card.SessionID, "transport", transportID,
			"node", card.TranscodeNodeURL, "error", err)
	}
}

// deleteNodeRecipeV3 drops a transport's stored recipe so a buffered or retrying
// request cannot resurrect a transcode the server has replaced or ended. The
// store's TTL is only the backstop for the paths that never run (a crashed API
// process); every deliberate teardown deletes here.
func (h *PlaybackHandler) deleteNodeRecipeV3(ctx context.Context, transportID string) {
	if h == nil || h.NodeRecipeStore == nil || transportID == "" {
		return
	}
	if err := h.NodeRecipeStore.Delete(context.WithoutCancel(ctx), transportID); err != nil {
		slog.WarnContext(ctx, "failed to drop the node transcode recipe",
			"component", "api", "transport", transportID, "error", err)
	}
}

// planIdentityProxyV3 selects the proxy node that will serve a direct-play or
// progressive-remux session. These deliveries need no transcode node — the
// bytes are either the source file or a single remux pipe — so the planner is
// asked for a proxy alone, exactly as the Jellyfin-compat transport does.
//
// A nil node (no planner, no signing secret, or no eligible proxy) means the
// API server serves the stream itself, which is both the single-node case and
// the correct degradation when every proxy is unhealthy or at capacity. The one
// exception is a remux that must run ffmpeg: that is transcode work, so it
// honors the same local-fallback gate as the HLS routes rather than quietly
// spawning an encoder on an API-only node.
func (h *PlaybackHandler) planIdentityProxyV3(r *http.Request, sessionID string, result playback.PlannerResultV3, mode mediaAuthModeV3) (*nodepool.Node, *transportErrorV3) {
	// A legacy attempt addresses its proxy with a signed token, so an unset
	// signing secret rules the whole pool out. An authorized-origin attempt
	// addresses it by session id against a server-side grant and needs no
	// signing secret of its own.
	if h.NodePlanner == nil || (h.JWTSecret == "" && !mode.proxyEgress) {
		return nil, h.refuseLocalIdentityWorkV3(r, result)
	}
	// Reserve against the session id the rest of the transport uses, so a
	// re-plan replaces its own reservation instead of double-counting, and the
	// rollback path can release it.
	plan := h.planIdentityProxySessionV3(r.Context(), sessionID, result)
	if plan.ProxyNode == nil {
		return nil, h.refuseLocalIdentityWorkV3(r, result)
	}
	return plan.ProxyNode, nil
}

// applyRemoteTransportMarkV3 records whether the committed route's media bytes
// leave this server or another node.
//
// Every committed transition calls it, including the ones that serve locally:
// a session replanned from a proxy onto the integrated transcoder would
// otherwise keep a stale mark, and the widened idle grace it grants would hold
// that session's stream and transcode slots for five minutes after the local
// stream disconnected without an explicit stop.
func (h *PlaybackHandler) applyRemoteTransportMarkV3(ctx context.Context, sessionID string, remote bool) {
	if err := h.sessionMgr.SetRemoteTransport(sessionID, remote); err != nil &&
		!errors.Is(err, playback.ErrSessionNotFound) {
		slog.WarnContext(ctx, "failed to record transport locality",
			"component", "api", "playback_session_id", sessionID, "remote", remote, "error", err)
	}
}

// planIdentityProxySessionV3 asks the planner for a proxy, narrowing selection
// to proxies that can execute the plan's frozen recipe when one is required.
//
// The narrowing happens before selection rather than after, mirroring how HLS
// filters transcode nodes: rejecting a single round-robin pick would abandon
// the whole pool, so a capable sibling with free capacity would sit unused
// while playback fell back to the API — or was refused outright when local
// fallback is disabled.
//
// Direct play copies bytes and needs no recipe, so it skips the probe entirely
// and any healthy proxy serves it.
func (h *PlaybackHandler) planIdentityProxySessionV3(ctx context.Context, sessionID string, result playback.PlannerResultV3) nodepool.Plan {
	estBitrate := identityStreamBitrateKbpsV3(result)
	selector, selectable := h.NodePlanner.(capabilitySessionPlannerV3)
	enumerator, enumerable := h.NodePlanner.(proxyNodeEnumeratorV3)
	if !selectable || !enumerable || !planRequiresServerTransformationsV3(result.Plan) {
		return h.NodePlanner.PlanSession(sessionID, "", false, estBitrate)
	}

	capable := make(map[string]struct{})
	for nodeURL, advertised := range h.pooledNodeTransformationsV3(ctx, enumerator.ProxyNodeURLs()) {
		if validateAdvertisedTransformationsV3(result.Plan, advertised) == nil {
			capable[nodeURL] = struct{}{}
		}
	}
	if len(capable) == 0 {
		slog.WarnContext(ctx, "protocol v3 no proxy advertises the planned recipe",
			"component", "api", "delivery", result.Plan.Delivery)
	}
	// The predicate runs under the planner lock: a set lookup only.
	return selector.PlanSessionWith(sessionID, "", false, estBitrate, func(node *nodepool.Node) bool {
		if node == nil {
			return false
		}
		_, ok := capable[node.URL]
		return ok
	})
}

// refuseLocalIdentityWorkV3 enforces playback.local_transcode_fallback for the
// identity deliveries. Direct play moves bytes and always falls back locally;
// a progressive remux that must convert audio is ffmpeg work, so on an API-only
// node it is refused for the same reason the HLS routes refuse it, instead of
// silently spawning an encoder the operator disabled.
func (h *PlaybackHandler) refuseLocalIdentityWorkV3(r *http.Request, result playback.PlannerResultV3) *transportErrorV3 {
	if result.Plan == nil || result.Plan.Delivery != playback.DeliveryRemuxProgressiveV3 ||
		!planRequiresServerTransformationsV3(result.Plan) ||
		nodepool.LocalTranscodeFallbackAllowed(r.Context(), h.SettingsRepo) {
		return nil
	}
	return &transportErrorV3{reason: "capacity_unavailable", message: "No proxy node is available and local fallback is disabled.", retryable: true}
}

// plannerInputV3 assembles the planner input for one route decision. The
// escalation below re-plans with the same inputs the original decision used,
// plus the refused route's attempt key. HLSRegistry and HLSToneMapCapabilities
// are deliberately left unset: planPlaybackWithCapabilitiesV3 installs its own
// lazily-memoized snapshot of both, so the two can never disagree.
func (h *PlaybackHandler) plannerInputV3(ctx context.Context, req playback.StartRequestV3, requestedFile, effectiveFile *models.MediaFile, audioIndex int, attemptedKeys []string) playback.PlannerInputV3 {
	return playback.PlannerInputV3{
		Request:             req,
		RequestedFile:       requestedFile,
		EffectiveFile:       effectiveFile,
		AudioTrackIndex:     audioIndex,
		Settings:            h.plannerSettingsV3(ctx),
		Registry:            h.transformationRegistryV3(ctx),
		DVRPUStrippable:     h.lazyDVRPUStrippableV3(ctx, effectiveFile),
		Now:                 time.Now(),
		AttemptedKeys:       attemptedKeys,
		AdditionalSubtitles: h.downloadedSubtitleInventoryV3(ctx, effectiveFile),
	}
}

// escalateRefusedProgressiveRemuxV3 replaces a progressive remux that the
// header-authenticated transport is guaranteed to refuse.
//
// Without authorized media origins that mode bypasses the proxy identity routes
// (a proxy authenticates from the signed URL token this mode exists to remove),
// so a remux carrying server transformations is ffmpeg work with nowhere to run
// once playback.local_transcode_fallback is off — refuseLocalIdentityWorkV3 turns it
// into a retryable capacity_unavailable that nothing will ever satisfy. HLS is
// the same recipe on a delivery the API can relay from a pooled transcode node,
// so plan it here rather than making the client discover the refusal and
// recover through a replan round trip.
//
// A client that cannot execute an HLS delivery has no such alternative: it gets
// a non-retryable error naming the policy, because retrying is exactly what it
// must not do.
//
// An attempt that negotiated authorized media origins has an executor again —
// a proxy runs the remux from its grant, exactly as it does for a legacy
// attempt — so nothing is escalated while the pool actually offers a proxy.
// With origins negotiated but no proxy configured the refusal is back, and so
// is this escalation.
//
// plannerInput is evaluated only on the escalation path: rebuilding it costs a
// settings resolution and a downloaded-subtitle listing, which the overwhelming
// majority of starts must not pay for a route they never take.
func (h *PlaybackHandler) escalateRefusedProgressiveRemuxV3(ctx context.Context, mode mediaAuthModeV3, plannerInput func() playback.PlannerInputV3, result playback.PlannerResultV3) (playback.PlannerResultV3, *transportErrorV3) {
	if !mode.headerAuth || (mode.proxyEgress && h.proxyEgressOriginsAvailableV3()) ||
		result.Terminal != nil || result.Plan == nil ||
		result.Plan.Delivery != playback.DeliveryRemuxProgressiveV3 ||
		!planRequiresServerTransformationsV3(result.Plan) ||
		nodepool.LocalTranscodeFallbackAllowed(ctx, h.SettingsRepo) {
		return result, nil
	}
	input := plannerInput()
	outputContextID := input.Request.ClientPlaybackContext.Output.OutputContextID
	next := input
	next.AttemptedKeys = append(append([]string(nil), input.AttemptedKeys...),
		playback.PlanAttemptKeyV3(*result.Plan, outputContextID, nil))
	next.Now = time.Now()
	escalated, capabilityErr := h.planPlaybackWithCapabilitiesV3(ctx, next)
	if capabilityErr != nil {
		// The escalation target depends on a capability lookup that failed, so
		// nothing is known about whether HLS would run. Ask the client to retry
		// rather than report the remux terminal as final.
		return result, &transportErrorV3{
			reason:    "transcode_node_capability_unavailable",
			message:   "Transcode capability validation is temporarily unavailable.",
			retryable: true,
			cause:     capabilityErr,
		}
	}
	if escalated.Terminal != nil || escalated.Plan == nil || escalated.Plan.Delivery == playback.DeliveryRemuxProgressiveV3 {
		reason := ""
		if escalated.Terminal != nil {
			reason = escalated.Terminal.Reason
		}
		slog.WarnContext(ctx, "protocol v3 header-authenticated remux has no executable delivery",
			logComponentKey, playbackLogValueV3,
			"delivery", result.Plan.Delivery,
			"replanned_terminal_reason", reason,
		)
		return result, &transportErrorV3{
			reason:  "local_transcode_disabled",
			message: "This server does not run playback conversions locally, and the client accepts no delivery that a transcode node can serve.",
		}
	}
	slog.InfoContext(ctx, "protocol v3 escalated refused progressive remux",
		logComponentKey, playbackLogValueV3,
		"delivery", escalated.Plan.Delivery,
		"decision_reason", escalated.Plan.DecisionReason,
	)
	return escalated, nil
}

// identityStreamBitrateKbpsV3 estimates the bitrate a proxy will egress for an
// identity delivery, so bandwidth-capped proxies admit it accurately. The plan's
// effective recipe is authoritative (a remux that downmixes audio egresses less
// than the source); the source descriptor is the fallback.
func identityStreamBitrateKbpsV3(result playback.PlannerResultV3) int {
	if result.Plan == nil {
		return 0
	}
	if effective := result.Plan.EffectiveRecipe.BitrateKbps; effective != nil && *effective > 0 {
		return *effective
	}
	if source := result.Plan.Source.BitrateKbps; source > 0 {
		return source
	}
	return 0
}

// identityStreamURLV3 builds the stream URL for a direct-play or
// progressive-remux session: an absolute proxy URL when one was planned,
// otherwise the API-local path.
//
// The proxy serves from the token alone, so the token has to carry everything
// the API-local path would have read from the session and the file record: the
// media path it opens, and the Dolby Vision profile its remux needs to strip a
// dangling Profile 7 RPU. Omitting either would not fail loudly — the proxy
// would serve a subtly different stream than the plan promised.
//
// The bool reports whether the returned URL is actually a proxy URL, so the
// caller can release the planner reservation when it is not.
//
// A session that requires media authorization never gets a proxy URL: the proxy
// serves from the signed token alone, which is exactly the credential that mode
// keeps out of client-visible URLs. It falls back to the API-local path, whose
// builder omits the token for the same reason.
func (h *PlaybackHandler) identityStreamURLV3(s *playback.Session, file *models.MediaFile, proxyNode *nodepool.Node) (string, bool) {
	if proxyNode == nil || file == nil || (s != nil && s.RequireMediaAuthorization) {
		return h.playbackStreamURL(s), false
	}
	card := identityRecipeCard(s)
	card.InputPath = file.FilePath
	claims := card.ToClaims()
	claims.DVProfile = file.PrimaryDVProfile()
	claims.AudioOnly = file.IsAudioOnly()
	token := h.signStreamClaims(claims)
	if token == "" {
		return h.playbackStreamURL(s), false
	}
	base := strings.TrimRight(proxyNode.URL, "/")
	if s.PlayMethod == playback.PlayRemux {
		if claims.PlayMethod == streamtoken.PlayMethodAudioDownmixRemux {
			return base + "/stream/remux/audio-v2/" + token, true
		}
		return base + "/stream/remux/" + token, true
	}
	return base + "/stream/direct/" + token, true
}

// sessionOwnsResumeTimelineV3 reports whether the session's own position is a
// valid resume point for the item it belongs to.
//
// Resume state is keyed on the item (playbackProgressTarget resolves a file to
// its episode or content ID), but every part of a multipart presentation shares
// that key while carrying its own file-local clock. Persisting part 4's
// position would therefore store "12 minutes into the book" as the book's
// resume point. The client that stitches the parts into one timeline is the
// only party that knows the item-absolute position, and it reports that through
// the sync/progress surface instead.
//
// This is derived rather than requested: the mismatch is a property of the
// media, not of the client, so a client that forgot to ask would corrupt resume
// exactly the same way.
func sessionOwnsResumeTimelineV3(file *models.MediaFile) bool {
	return file == nil || file.PresentationPartTotal <= 1
}

// preferredAudioTrackIndexV3 answers what an omitted audio track means: the
// language this profile has settled on for this series, this library, this
// device, or generally — the same resolution the catalog performs when it
// publishes `effective_audio_track_index`, so the track a client sees on the
// detail page is the track that plays when it does not ask for one.
//
// The client sends a track identity only when the viewer picked one. Defaulting
// to ordinal zero instead would silently play the first track on the reel.
func (h *PlaybackHandler) preferredAudioTrackIndexV3(ctx context.Context, userID int, profileID, deviceID string, file *models.MediaFile) (int, error) {
	if file == nil || len(file.AudioTracks) == 0 || h.StoreProvider == nil {
		return 0, nil
	}
	store, err := h.StoreProvider.ForUser(ctx, userID)
	if err != nil {
		slog.ErrorContext(ctx, "protocol v3 start: audio preference store lookup failed", "component", "api", "user_id", userID, "error", err)
		return 0, err
	}
	seriesID := h.resolveSeriesID(ctx, file)
	var seriesPref *playback.AudioTrackPreference
	if seriesID != "" {
		stored, prefErr := store.GetAudioPreference(ctx, profileID, seriesID)
		if prefErr != nil {
			slog.ErrorContext(ctx, "protocol v3 start: series audio preference lookup failed", "component", "api", "profile_id", profileID, "series_id", seriesID, "error", prefErr)
			return 0, prefErr
		}
		if stored != nil {
			seriesPref = &playback.AudioTrackPreference{AudioTrackIndex: stored.AudioTrackIndex, AudioLanguage: stored.AudioLanguage, TrackSignature: stored.TrackSignature}
		}
	}
	rc := settingsresolve.Context{
		ProfileID:  profileID,
		DeviceID:   deviceID,
		LibraryIDs: []int{file.MediaFolderID},
	}
	if seriesID != "" {
		rc.SeriesIDs = []string{seriesID}
	}
	preferredLang, err := resolvedPlaybackAudioLanguage(ctx, store, rc)
	if err != nil {
		slog.ErrorContext(ctx, "protocol v3 start: canonical audio preference lookup failed", "component", "api", "profile_id", profileID, "device_id", deviceID, "error", err)
		return 0, err
	}
	if preferredLang == playback.OriginalLanguageSentinel {
		preferredLang = h.resolveOriginalLanguage(ctx, file)
		if preferredLang == "" {
			preferredLang, err = resolvedPlaybackAudioLanguage(ctx, store, settingsresolve.Context{ProfileID: profileID})
			if err != nil {
				slog.ErrorContext(ctx, "protocol v3 start: profile audio preference fallback failed", "component", "api", "profile_id", profileID, "error", err)
				return 0, err
			}
			if preferredLang == playback.OriginalLanguageSentinel {
				preferredLang = h.resolveOriginalLanguage(ctx, file)
			}
		}
	}
	if seriesPref != nil {
		// The specialized row supplies concrete track identity; canonical
		// settings own the language and its scope precedence.
		seriesPref.AudioLanguage = preferredLang
	}
	return normalizeAudioTrackIndex(file, playback.SelectAudioTrack(file.AudioTracks, preferredLang, seriesPref)), nil
}

// resumePositionV3 answers what an omitted `start_position` means: resume where
// this profile left off. It runs before planning rather than after session
// creation because the plan's timeline is cut at the start position — a route
// chosen for zero and then seeked to 40 minutes is a different route.
//
// A client that wants to start over sends an explicit `start_position: 0`; only
// omission asks the server for its resume policy. Parts of a multipart item are
// skipped for the same reason their progress is not persisted: they share one
// resume point with the whole item, so a part-local seek to it is meaningless.
func (h *PlaybackHandler) resumePositionV3(ctx context.Context, userID int, profileID string, file *models.MediaFile) (*float64, error) {
	if h.StoreProvider == nil || !sessionOwnsResumeTimelineV3(file) {
		return nil, nil
	}
	targetID := playbackProgressTarget(file)
	if targetID == "" {
		return nil, nil
	}
	store, err := h.StoreProvider.ForUser(ctx, userID)
	if err != nil {
		slog.ErrorContext(ctx, "protocol v3 start: resume store lookup failed", "component", "api", "user_id", userID, "error", err)
		return nil, err
	}
	progress, err := store.GetProgress(ctx, profileID, targetID)
	if err != nil {
		slog.ErrorContext(ctx, "protocol v3 start: resume progress lookup failed", "component", "api", "target", targetID, "error", err)
		return nil, err
	}
	if progress == nil || progress.Completed || progress.PositionSeconds <= 0 {
		return nil, nil
	}
	position := progress.PositionSeconds
	return &position, nil
}

// A copy remux starts at the preceding keyframe selected by the demuxer, not
// necessarily at the requested source position. Its player clock therefore
// begins at the resolved stream origin and advances through the copied pre-roll
// before reaching the requested position. Neither progressive nor growing HLS
// copy transports can seek arbitrarily inside their current response.
func configureCopyRemuxTimelineV3(plan *playback.PlanV3, origin float64) {
	if plan == nil {
		return
	}
	plan.Timeline.PlayerStartSeconds = max(0, plan.Timeline.SourceStartSeconds-origin)
	plan.Timeline.StreamOriginSeconds = origin
	plan.Timeline.TimelineOffsetSeconds = origin
	plan.Timeline.SeekWindowStartSeconds = &origin
	plan.Timeline.SeekWindowEndSeconds = nil
	plan.Timeline.CanSeekAnywhere = false
	plan.Timeline.SeekRestoration = "source_position"
}

func appendPlaybackQueryV3(rawURL, key, value string) string {
	separator := "?"
	if strings.ContainsRune(rawURL, '?') {
		separator = "&"
	}
	return rawURL + separator + key + "=" + value
}

// softwareToneMapRetryOptsV3 returns the software executor for a one-shot
// hardware fallback when the recipe is allowed to adapt to live capabilities.
func (h *PlaybackHandler) softwareToneMapRetryOptsV3(ctx context.Context, opts playback.TranscodeOpts, frozenSourceMetadata bool) (playback.TranscodeOpts, bool) {
	if frozenSourceMetadata || opts.ToneMapMode != tonemap.ModeHardware ||
		!opts.ToneMapPolicy.Allows(tonemap.ModeSoftware) {
		return opts, false
	}
	capabilities, err := h.localToneMapCapabilitiesForTransportV3(ctx)
	if err != nil {
		return opts, false
	}
	if !capabilities.Supports(tonemap.ModeSoftware, opts.ToneMapSourceKind) {
		return opts, false
	}
	opts.ToneMapMode = tonemap.ModeSoftware
	opts.ToneMapFilter = capabilities.FilterFor(tonemap.ModeSoftware, opts.ToneMapSourceKind)
	opts.HWAccel = playback.HWAccelNone
	return opts, true
}

type localTransportStartupFailureV3 struct {
	cause         error
	failedToStart bool
	wasRunning    bool
	failedDevice  string
}

// startReadyLocalPlaybackTransportV3 returns only after the first manifest is
// safe to serve. A session that fails readiness is closed before the failure is
// returned so every caller gets the same startup cleanup behavior.
func (h *PlaybackHandler) startReadyLocalPlaybackTransportV3(ctx context.Context, opts playback.TranscodeOpts) (*playback.TranscodeSession, *localTransportStartupFailureV3) {
	startedAt := time.Now()
	ts, err := h.startLocalPlaybackTransport(ctx, opts)
	spawnFinishedAt := time.Now()
	if err != nil {
		slog.InfoContext(ctx, "playback transport startup timing",
			logComponentKey, playbackLogValueV3,
			requestIDLogKeyV3, chimw.GetReqID(ctx),
			"transport", "local",
			"session", opts.SessionID,
			"spawn_ms", spawnFinishedAt.Sub(startedAt).Milliseconds(),
			"manifest_wait_ms", int64(0),
			"total_ms", time.Since(startedAt).Milliseconds(),
			"outcome", "spawn_failed",
		)
		return nil, &localTransportStartupFailureV3{cause: err, failedToStart: true}
	}
	if _, err := ts.WaitForManifest(playback.ManifestStartupTimeout); err != nil {
		slog.InfoContext(ctx, "playback transport startup timing",
			logComponentKey, playbackLogValueV3,
			requestIDLogKeyV3, chimw.GetReqID(ctx),
			"transport", "local",
			"session", opts.SessionID,
			"spawn_ms", spawnFinishedAt.Sub(startedAt).Milliseconds(),
			"manifest_wait_ms", time.Since(spawnFinishedAt).Milliseconds(),
			"total_ms", time.Since(startedAt).Milliseconds(),
			"outcome", "readiness_failed",
		)
		failure := &localTransportStartupFailureV3{
			cause:        err,
			wasRunning:   ts.IsRunning(),
			failedDevice: ts.Opts().HWDevice,
		}
		_ = ts.Close()
		return nil, failure
	}
	slog.InfoContext(ctx, "playback transport startup timing",
		logComponentKey, playbackLogValueV3,
		requestIDLogKeyV3, chimw.GetReqID(ctx),
		"transport", "local",
		"session", opts.SessionID,
		"spawn_ms", spawnFinishedAt.Sub(startedAt).Milliseconds(),
		"manifest_wait_ms", time.Since(spawnFinishedAt).Milliseconds(),
		"total_ms", time.Since(startedAt).Milliseconds(),
		"outcome", "ready",
	)
	return ts, nil
}

// prepareLocalTransportV3 starts a local HLS generation for the selected plan.
func (h *PlaybackHandler) prepareLocalTransportV3(r *http.Request, session *playback.Session, file *models.MediaFile, result playback.PlannerResultV3, timeline preparedTimelineV3, mode mediaAuthModeV3) (preparedTransportV3, *transportErrorV3) {
	cfg := h.playbackConfig()
	if err := os.MkdirAll(cfg.TranscodeDir, 0o755); err != nil {
		return preparedTransportV3{}, &transportErrorV3{reason: "internal_error", message: "Failed to prepare the transcode directory.", cause: err}
	}
	outputSubdir := transportGenerationV3(session.ID, result.Plan.PlanID)
	outputDir := filepath.Join(cfg.TranscodeDir, outputSubdir)
	videoCodec := result.TargetVideoCodec
	if result.Plan.Delivery == playback.DeliveryRemuxHLSV3 {
		videoCodec = "copy"
	}
	sourceMetadata := sourceExecutionMetadataV3(file, result)
	sourceProfile, sourceBitDepth := sourceVideoTranscodeFactsV3(file, result)
	unlock := h.tm.LockSessionLifecycle(session.ID)
	opts := playback.TranscodeOpts{InputPath: file.FilePath, OutputDir: outputDir, OutputSubdir: outputSubdir, SessionID: session.ID, SourceVideoCodec: sourceMetadata.VideoCodec, SourceVideoProfile: sourceProfile, SourceVideoBitDepth: sourceBitDepth, SourceAudioChannels: result.SourceAudioChannels, SoftwareVideoDecode: sourceMetadata.SoftwareVideoDecode, ToneMapPolicy: result.ToneMapPolicy, ToneMapMode: result.ToneMapMode, ToneMapSourceKind: result.ToneMapSourceKind, ToneMapRecipeVersion: result.ToneMapRecipeVersion, ToneMapPreflightRequired: result.ToneMapPreflightRequired, ToneMapSourceRevision: result.ToneMapSourceRevision, VideoBitstreamFilter: videoBitstreamFilterForPlanV3(result.Plan), VideoSampleEntry: videoSampleEntryForPlanV3(result.Plan), SeekSeconds: timeline.seekSeconds, StreamOriginSeconds: timeline.streamOriginSeconds, CopySeekAnchorResolved: timeline.copySeekAnchorResolved, StartSegmentNumber: timeline.startSegmentNumber, TargetResolution: result.TargetResolution, TargetCodecVideo: videoCodec, TargetCodecAudio: result.TargetAudioCodec, TargetAudioChannels: result.TargetAudioChannels, TargetAudioBitrateKbps: result.TargetAudioBitrateKbps, TargetBitrateKbps: result.TargetBitrateKbps, SegmentDuration: playback.DefaultSegmentDuration, FFmpegPath: cfg.FFmpegPath, HWAccel: cfg.HWAccel, HWDevice: cfg.HWDevice, AudioTrackIndex: plannedAudioTrackIndexV3(result, session.AudioTrackIndex), SubtitleTrackIndex: result.SubtitleTransportTrackIndex, SubtitleBurnIn: result.SubtitleBurnIn, SubtitleCodec: result.SubtitleCodec, TotalDuration: sourceMetadata.DurationSeconds, FastStart: true, NodeType: playbackNodeIntegratedV3, ExecutionMode: playbackNodeIntegratedV3, FFmpegLogSink: h.FFmpegLogSink}
	opts.ToneMapDVConfigPresent = sourceMetadata.ToneMapDVConfigPresent
	opts.ToneMapDVBLCompatIDPresent = sourceMetadata.ToneMapDVBLCompatIDPresent
	opts.ToneMapDVBLPresent = sourceMetadata.ToneMapDVBLPresent
	opts.ToneMapDVRPUPresent = sourceMetadata.ToneMapDVRPUPresent
	if opts.ToneMapMode != "" {
		capabilities, capabilityErr := h.localToneMapCapabilitiesForTransportV3(r.Context())
		if capabilityErr != nil {
			unlock()
			return preparedTransportV3{}, &transportErrorV3{reason: transcodeStartFailedReasonV3, message: "Local tone-map capability validation is temporarily unavailable.", retryable: true, cause: capabilityErr}
		}
		opts.ToneMapFilter = capabilities.FilterFor(opts.ToneMapMode, opts.ToneMapSourceKind)
		if opts.ToneMapMode == tonemap.ModeHardware {
			opts.HWAccel = capabilities.BackendFor(opts.ToneMapMode, opts.ToneMapSourceKind)
		} else {
			opts.HWAccel = playback.HWAccelNone
		}
	}
	usedToneMapFallback := false
	ts, startupFailure := h.startReadyLocalPlaybackTransportV3(r.Context(), opts)
	if startupFailure != nil && startupFailure.failedToStart {
		if softwareOpts, eligible := h.softwareToneMapRetryOptsV3(r.Context(), opts, result.FrozenSourceMetadata != nil); eligible {
			slog.WarnContext(r.Context(), "hardware tone-map failed to start; retrying once in software",
				logComponentKey, playbackLogValueV3, "playback_session_id", session.ID, "error", startupFailure.cause)
			opts = softwareOpts
			usedToneMapFallback = true
			ts, startupFailure = h.startReadyLocalPlaybackTransportV3(r.Context(), opts)
		}
	}
	if startupFailure != nil && startupFailure.failedToStart {
		unlock()
		return preparedTransportV3{}, toneMapExecutionTransportErrorV3(startupFailure.cause, "Failed to start the playback transport.")
	}
	if startupFailure != nil {
		transportErr := manifestStartupTransportErrorV3(startupFailure.wasRunning, startupFailure.cause)
		if usedToneMapFallback {
			unlock()
			return preparedTransportV3{}, transportErr
		}

		if fallbackOpts, eligible := h.softwareToneMapRetryOptsV3(r.Context(), opts, result.FrozenSourceMetadata != nil); eligible {
			slog.WarnContext(r.Context(), "hardware tone-map failed during startup; retrying once in software",
				logComponentKey, playbackLogValueV3,
				"playback_session_id", session.ID,
				"failed_device", startupFailure.failedDevice,
				"error", startupFailure.cause)
			opts = fallbackOpts
			ts, startupFailure = h.startReadyLocalPlaybackTransportV3(r.Context(), opts)
			if startupFailure != nil && startupFailure.failedToStart {
				unlock()
				return preparedTransportV3{}, toneMapExecutionTransportErrorV3(startupFailure.cause, "Failed to start the software tone-map fallback.")
			}
			if startupFailure != nil {
				unlock()
				return preparedTransportV3{}, manifestStartupTransportErrorV3(startupFailure.wasRunning, startupFailure.cause)
			}
		} else if startupFailure.wasRunning {
			unlock()
			return preparedTransportV3{}, transportErr
		} else {
			// FFmpeg and GPU drivers can fail before producing their first segment
			// even though the recipe is valid. Retry one clean generation, preferring
			// another configured render device so a transient device failure does not
			// become an immediate client-visible transport error.
			retryOpts := opts
			retryOpts.AvoidHWDevice = startupFailure.failedDevice
			retryOpts.HWAccel = playback.StartupRetryHWAccel(opts)
			slog.WarnContext(r.Context(), "local transcode crashed during startup; retrying once",
				logComponentKey, playbackLogValueV3,
				"playback_session_id", session.ID,
				"failed_device", startupFailure.failedDevice,
				"configured_devices", retryOpts.HWDevice,
				"error", startupFailure.cause)
			ts, startupFailure = h.startReadyLocalPlaybackTransportV3(r.Context(), retryOpts)
			if startupFailure != nil && startupFailure.failedToStart {
				unlock()
				return preparedTransportV3{}, toneMapExecutionTransportErrorV3(startupFailure.cause, "Failed to start the playback transport.")
			}
			if startupFailure != nil {
				unlock()
				return preparedTransportV3{}, manifestStartupTransportErrorV3(startupFailure.wasRunning, startupFailure.cause)
			}
		}
	}
	url := fmt.Sprintf("/playback/transcode/%s/master.m3u8", session.ID)
	if !mode.headerAuth {
		card := playback.NewRecipeCard(session.UserID, session.ProfileID, file.ID, "", ts.Opts())
		card.OriginalStartedAt = session.StartedAt
		url = appendStreamToken(url, h.signSessionToken(card, mode.headerAuth))
	}
	committed := false
	previousNodeURL := session.TranscodeNodeURL
	previousTransportID := remoteTransportID(session)
	return preparedTransportV3{
		url:         url,
		hwAccel:     ts.Opts().HWAccel,
		toneMapMode: ts.Opts().ToneMapMode,
		commit: func() {
			if committed {
				return
			}
			committed = true
			previous := h.tm.SwapTranscodeSession(session.ID, ts)
			// A local transcode is never proxy-served, so an authorized-origins
			// replan landing here has to revoke the grant it is replacing.
			h.revokeStaleProxyGrantOnCommitV3(r.Context(), session.ID, mode, false)
			h.applyRemoteTransportMarkV3(r.Context(), session.ID, false)
			unlock()
			if previous != nil && previous != ts {
				_ = previous.Close()
			}
			if previousNodeURL != "" {
				h.tm.StopRemoteTranscode(previousTransportID, previousNodeURL)
				h.deleteNodeRecipeV3(r.Context(), previousTransportID)
			}
			ts.SetRestartHook(func(ctx context.Context) {
				h.maybeStartThrottler(ctx, ts)
				h.tm.MonitorLocalTranscodeExit(session.ID, ts)
			})
			h.maybeStartThrottler(r.Context(), ts)
			h.tm.MonitorLocalTranscodeExit(session.ID, ts)
		},
		rollback: func() {
			if committed {
				return
			}
			committed = true
			_ = ts.Close()
			unlock()
		},
	}, nil
}

func manifestStartupTransportErrorV3(running bool, cause error) *transportErrorV3 {
	message := "The playback transport failed before media became ready."
	if running {
		message = "The playback transport did not become ready in time."
	}
	return &transportErrorV3{reason: transcodeStartFailedReasonV3, message: message, retryable: running, cause: cause}
}

func toneMapExecutionTransportErrorV3(cause error, message string) *transportErrorV3 {
	return &transportErrorV3{
		reason:    transcodeStartFailedReasonV3,
		message:   message,
		retryable: !errors.Is(cause, tonemap.ErrSourceRevisionChanged) && !errors.Is(cause, tonemap.ErrSourcePreflightRejected),
		cause:     cause,
	}
}

func combineTransportErrorsV3(first, second *transportErrorV3) *transportErrorV3 {
	if first == nil {
		return second
	}
	if second == nil {
		return first
	}
	combined := *second
	combined.retryable = first.retryable || second.retryable
	combined.cause = errors.Join(first.cause, second.cause)
	return &combined
}

// prepareRemoteTransportV3 starts an HLS generation on the selected transcode node.
func (h *PlaybackHandler) prepareRemoteTransportV3(r *http.Request, session *playback.Session, file *models.MediaFile, result playback.PlannerResultV3, nodePlan nodepool.Plan, timeline preparedTimelineV3, mode mediaAuthModeV3) (preparedTransportV3, *transportErrorV3) {
	node := nodePlan.TranscodeNode
	transportID := transportGenerationV3(session.ID, result.Plan.PlanID)
	videoCodec := result.TargetVideoCodec
	if result.Plan.Delivery == playback.DeliveryRemuxHLSV3 {
		videoCodec = "copy"
	}
	sourceMetadata := sourceExecutionMetadataV3(file, result)
	sourceProfile, sourceBitDepth := sourceVideoTranscodeFactsV3(file, result)
	hwAccel := h.playbackConfig().HWAccel
	toneMapFilter := ""
	if result.ToneMapMode != "" {
		capabilities, err := h.remoteToneMapCapabilitiesV3(r.Context(), node.URL, false)
		if err != nil || !capabilities.Supports(result.ToneMapMode, result.ToneMapSourceKind) {
			return preparedTransportV3{}, &transportErrorV3{reason: "transcode_node_capability_unavailable", message: "The selected node cannot run the tone-map recipe.", retryable: true, cause: err}
		}
		toneMapFilter = capabilities.FilterFor(result.ToneMapMode, result.ToneMapSourceKind)
		if result.ToneMapMode == tonemap.ModeHardware {
			hwAccel = capabilities.BackendFor(result.ToneMapMode, result.ToneMapSourceKind)
		} else {
			hwAccel = playback.HWAccelNone
		}
	}
	req := transcodenode.TranscodeStartRequest{SessionID: transportID, InputPath: file.FilePath, SourceVideoCodec: sourceMetadata.VideoCodec, SourceVideoProfile: sourceProfile, SourceVideoBitDepth: sourceBitDepth, SourceAudioChannels: result.SourceAudioChannels, SoftwareVideoDecode: sourceMetadata.SoftwareVideoDecode, ToneMapPolicy: result.ToneMapPolicy, ToneMapMode: result.ToneMapMode, ToneMapSourceKind: result.ToneMapSourceKind, ToneMapRecipeVersion: result.ToneMapRecipeVersion, ToneMapPreflightRequired: result.ToneMapPreflightRequired, ToneMapSourceRevision: result.ToneMapSourceRevision, VideoBitstreamFilter: videoBitstreamFilterForPlanV3(result.Plan), VideoSampleEntry: videoSampleEntryForPlanV3(result.Plan), SeekSeconds: timeline.seekSeconds, StreamOriginSeconds: timeline.streamOriginSeconds, CopySeekAnchorResolved: timeline.copySeekAnchorResolved, StartSegmentNumber: timeline.startSegmentNumber, TargetResolution: result.TargetResolution, TargetCodecVideo: videoCodec, TargetCodecAudio: result.TargetAudioCodec, TargetAudioChannels: result.TargetAudioChannels, TargetAudioBitrateKbps: result.TargetAudioBitrateKbps, TargetBitrateKbps: result.TargetBitrateKbps, SegmentDuration: playback.DefaultSegmentDuration, HWAccel: hwAccel, AudioTrackIndex: plannedAudioTrackIndexV3(result, session.AudioTrackIndex), SubtitleTrackIndex: result.SubtitleTransportTrackIndex, SubtitleBurnIn: result.SubtitleBurnIn, SubtitleCodec: result.SubtitleCodec, TotalDuration: sourceMetadata.DurationSeconds, RequireReady: true}
	if playback.IsAudioToAACStereoDownmixV3(req.SourceAudioChannels, req.TargetCodecAudio, req.TargetAudioChannels) {
		// Remote attestation uses the explicit effective layout even though zero
		// means stereo to the local AAC argument builder.
		req.TargetAudioChannels = 2
		req.AudioRecipeVersion = playback.TransformationAudioToAACRecipeVersionV3
	} else {
		// SourceAudioChannels is a v2 recipe field at the node boundary. Omit it
		// for ordinary encodes so they cannot be mistaken for partial v2 work.
		req.SourceAudioChannels = 0
	}
	req.ToneMapDVConfigPresent = sourceMetadata.ToneMapDVConfigPresent
	req.ToneMapDVBLCompatIDPresent = sourceMetadata.ToneMapDVBLCompatIDPresent
	req.ToneMapDVBLPresent = sourceMetadata.ToneMapDVBLPresent
	req.ToneMapDVRPUPresent = sourceMetadata.ToneMapDVRPUPresent
	remoteStartAt := time.Now()
	nodeResp, status, err := h.startRemotePlaybackTransport(r.Context(), node.URL, req)
	remoteOutcome := "ready"
	if err != nil || status != http.StatusAccepted {
		remoteOutcome = playbackRemoteOutcomeFailedV3
	}
	slog.InfoContext(r.Context(), "playback transport startup timing",
		logComponentKey, playbackLogValueV3,
		requestIDLogKeyV3, chimw.GetReqID(r.Context()),
		"transport", "remote",
		"session", session.ID,
		"total_ms", time.Since(remoteStartAt).Milliseconds(),
		"status", status,
		"outcome", remoteOutcome,
	)
	if err != nil {
		if req.ToneMapMode != "" && (errors.Is(err, tonemap.ErrSourceRevisionChanged) ||
			errors.Is(err, tonemap.ErrSourcePreflightRejected) ||
			errors.Is(err, playback.ErrToneMapSourceValidationUnavailable) ||
			errors.Is(err, playback.ErrToneMapExecutorUnavailable)) {
			h.tm.StopRemoteTranscode(transportID, node.URL)
			return preparedTransportV3{}, toneMapExecutionTransportErrorV3(err, "The selected transcode node rejected the playback transport.")
		}
		// A timeout can fire after the node actually started the job; the
		// stop is a harmless 404 when it never did, and reaps an orphan
		// full-length transcode when it did.
		h.tm.StopRemoteTranscode(transportID, node.URL)
		return preparedTransportV3{}, &transportErrorV3{reason: "transcode_node_unavailable", message: "The selected transcode node is unavailable.", retryable: true, cause: err}
	}
	if status != http.StatusAccepted {
		h.tm.StopRemoteTranscode(transportID, node.URL)
		return preparedTransportV3{}, &transportErrorV3{reason: transcodeStartFailedReasonV3, message: "The selected transcode node rejected the playback transport.", retryable: true}
	}
	if err := transcodenode.ValidateAudioRecipeAttestation(req, nodeResp); err != nil {
		h.tm.StopRemoteTranscode(transportID, node.URL)
		return preparedTransportV3{}, &transportErrorV3{reason: transcodeStartFailedReasonV3, message: "The selected transcode node did not confirm the audio recipe.", retryable: true, cause: err}
	}
	if req.ToneMapMode != "" && nodeResp.ToneMapMode != req.ToneMapMode {
		h.tm.StopRemoteTranscode(transportID, node.URL)
		return preparedTransportV3{}, &transportErrorV3{reason: transcodeStartFailedReasonV3, message: "The selected transcode node did not confirm the tone-map recipe.", retryable: true}
	}
	confirmedToneMapMode := tonemap.Mode("")
	if req.ToneMapMode != "" {
		confirmedToneMapMode = nodeResp.ToneMapMode
	}
	card := remoteTranscodeRecipeCardV3(session, file, node.URL, transportID, req, nodeResp, toneMapFilter)
	confirmedHWAccel := card.HWAccel
	url := fmt.Sprintf("/playback/transcode/%s/master.m3u8", session.ID)
	// Either URL builder only returns an absolute proxy URL when a proxy was
	// planned and its authority (a signed token, or a stored grant) could
	// actually be established; otherwise the client fetches the manifest from
	// this server and the local liveness path applies.
	servedByProxy := false
	// See prepareIdentityTransportV3: the displaced grant is what a failed
	// replan of an already-proxy-served session has to put back.
	var priorGrant *playback.RecipeCard
	switch {
	case !mode.headerAuth:
		url = h.buildProxyManifestURL(card, nodePlan.ProxyNode, mode.headerAuth)
		servedByProxy = nodePlan.ProxyNode != nil && strings.HasPrefix(url, "http")
	case mode.proxyEgress:
		url, servedByProxy, priorGrant = h.grantManifestURLV3(r.Context(), card, nodePlan.ProxyNode)
	}
	if mode.headerAuth {
		// No client-visible URL carries a stream token in this mode, so neither the
		// client nor the API relay can hand the node its recipe back after the node
		// restarts. Store it for the node to fetch. Both sub-modes need it: the
		// API-local relay is the fallback whenever a proxy origin is not used, and
		// the proxy relays the same tokenless node URLs when it is.
		h.putNodeRecipeV3(r.Context(), transportID, card)
	}
	if nodePlan.ProxyNode != nil && !servedByProxy {
		// The planner charged a proxy for a stream that will not cross it (no
		// writable grant, or a legacy no-token fallback). Give back the proxy half
		// of the reservation now rather than let it pin that node's job slot and
		// estimated bandwidth until it ages out; the transcode node keeps its half,
		// because it is running the job.
		if releaser, ok := h.NodePlanner.(sessionProxyReservationReleaserV3); ok {
			releaser.ReleaseSessionProxy(session.ID)
		}
	}
	committed := false
	previousNodeURL := session.TranscodeNodeURL
	previousTransportID := remoteTransportID(session)
	unlock := h.tm.LockSessionLifecycle(session.ID)
	return preparedTransportV3{url: url, nodeURL: node.URL, transportID: transportID, hwAccel: confirmedHWAccel, toneMapMode: confirmedToneMapMode, commit: func() {
		if committed {
			return
		}
		committed = true
		h.tm.CloseTranscodeSession(session.ID, "")
		if previousNodeURL != "" {
			h.tm.StopRemoteTranscode(previousTransportID, previousNodeURL)
			h.deleteNodeRecipeV3(r.Context(), previousTransportID)
		}
		h.revokeStaleProxyGrantOnCommitV3(r.Context(), session.ID, mode, servedByProxy)
		h.applyRemoteTransportMarkV3(r.Context(), session.ID, servedByProxy)
		unlock()
	}, rollback: func() {
		if committed {
			return
		}
		committed = true
		h.tm.StopRemoteTranscode(transportID, node.URL)
		// The node job this recipe rebuilds is gone, so the recipe must go too.
		h.deleteNodeRecipeV3(r.Context(), transportID)
		// The accepted node job is gone; drop the planner reservation too so
		// repeated failed starts cannot pin the node's max-job or bandwidth
		// budget until the reservation ages out.
		if releaser, ok := h.NodePlanner.(sessionReservationReleaserV3); ok {
			releaser.ReleaseSession(session.ID)
		}
		// An egress grant written for a transport that never committed would
		// point a proxy at a transcode that no longer exists.
		if servedByProxy {
			h.restoreProxyGrantV3(r.Context(), session.ID, priorGrant)
		}
		unlock()
	}}, nil
}

// remoteTranscodeRecipeCardV3 captures the byte-affecting recipe of a started
// remote transcode. It is what a proxy relays from (grant) or a client carries
// (signed token), and what a restarted node reconstructs from, so it must
// reflect the parameters the node accepted rather than the ones requested: the
// node reports the hardware acceleration it actually used.
//
// toneMapFilter is the resolved FFmpeg filter for the confirmed executor; it is
// not part of the node's request/response contract, so the caller supplies it.
func remoteTranscodeRecipeCardV3(session *playback.Session, file *models.MediaFile, nodeURL, transportID string, req transcodenode.TranscodeStartRequest, nodeResp transcodenode.TranscodeStartResponse, toneMapFilter string) playback.RecipeCard {
	hw := firstNonEmptyHandlerV3(strings.TrimSpace(nodeResp.HWAccel), strings.TrimSpace(req.HWAccel))
	card := playback.NewRecipeCard(session.UserID, session.ProfileID, file.ID, nodeURL, playback.TranscodeOpts{InputPath: req.InputPath, SessionID: session.ID, TranscodeTransportID: transportID, SourceVideoCodec: req.SourceVideoCodec, SourceVideoProfile: req.SourceVideoProfile, SourceVideoBitDepth: req.SourceVideoBitDepth, SourceAudioChannels: req.SourceAudioChannels, SoftwareVideoDecode: req.SoftwareVideoDecode, ToneMapPolicy: req.ToneMapPolicy, ToneMapMode: req.ToneMapMode, ToneMapSourceKind: req.ToneMapSourceKind, ToneMapFilter: toneMapFilter, ToneMapRecipeVersion: req.ToneMapRecipeVersion, ToneMapPreflightRequired: req.ToneMapPreflightRequired, ToneMapSourceRevision: req.ToneMapSourceRevision, VideoBitstreamFilter: req.VideoBitstreamFilter, VideoSampleEntry: req.VideoSampleEntry, SeekSeconds: req.SeekSeconds, StreamOriginSeconds: req.StreamOriginSeconds, CopySeekAnchorResolved: req.CopySeekAnchorResolved, StartSegmentNumber: req.StartSegmentNumber, TargetResolution: req.TargetResolution, TargetCodecVideo: req.TargetCodecVideo, TargetCodecAudio: req.TargetCodecAudio, TargetAudioChannels: req.TargetAudioChannels, TargetAudioBitrateKbps: req.TargetAudioBitrateKbps, TargetBitrateKbps: req.TargetBitrateKbps, SegmentDuration: req.SegmentDuration, HWAccel: hw, AudioTrackIndex: req.AudioTrackIndex, SubtitleTrackIndex: req.SubtitleTrackIndex, SubtitleBurnIn: req.SubtitleBurnIn, SubtitleCodec: req.SubtitleCodec, TotalDuration: req.TotalDuration})
	card.ToneMapDVConfigPresent = req.ToneMapDVConfigPresent
	card.ToneMapDVBLCompatIDPresent = req.ToneMapDVBLCompatIDPresent
	card.ToneMapDVBLPresent = req.ToneMapDVBLPresent
	card.ToneMapDVRPUPresent = req.ToneMapDVRPUPresent
	// Stamp the immutable session creation time onto every copy of this recipe —
	// client token, proxy grant, and node recipe alike — so telemetry can age the
	// session correctly after any reconstruct.
	card.OriginalStartedAt = session.StartedAt
	return card
}

// grantManifestURLV3 is buildProxyManifestURL's authorized-origins sibling: it
// stores the session's transcode recipe as a proxy grant and returns the
// credential-free manifest URL on that origin. Segment URIs stay relative to
// the manifest, so the same /stream/v3/{session_id}/... family serves both.
//
// Without a planned proxy — or when the grant cannot be stored — the client
// fetches the manifest from this server, which relays the same node. The third
// value is the grant this write displaced, for the caller's rollback.
func (h *PlaybackHandler) grantManifestURLV3(ctx context.Context, card playback.RecipeCard, proxyNode *nodepool.Node) (string, bool, *playback.RecipeCard) {
	localURL := fmt.Sprintf("/playback/transcode/%s/master.m3u8", card.SessionID)
	if proxyNode == nil {
		return localURL, false, nil
	}
	prior, stored := h.putProxyGrantV3(ctx, card.SessionID, card)
	if !stored {
		return localURL, false, nil
	}
	return strings.TrimRight(proxyNode.URL, "/") + "/stream/v3/" + card.SessionID + "/master.m3u8", true, prior
}

// sourceExecutionMetadataV3 freezes the source facts used by a remote executor.
func sourceExecutionMetadataV3(file *models.MediaFile, result playback.PlannerResultV3) playback.SourceExecutionMetadataV3 {
	if result.FrozenSourceMetadata != nil {
		return scopeToneMapSourceMetadataV3(*result.FrozenSourceMetadata, result.ToneMapMode)
	}
	if file == nil {
		return playback.SourceExecutionMetadataV3{}
	}
	videoCodec, profile, bitDepth := playback.SourceVideoTranscodeFacts(file)
	track := models.VideoTrack{}
	if len(file.VideoTracks) > 0 {
		track = file.VideoTracks[0]
	}
	metadata := playback.SourceExecutionMetadataV3{
		VideoCodec:                 videoCodec,
		VideoProfile:               profile,
		VideoBitDepth:              bitDepth,
		SoftwareVideoDecode:        playback.RequiresSoftwareVideoDecode(videoCodec, profile, bitDepth),
		DurationSeconds:            float64(file.Duration),
		ToneMapSourceKind:          result.ToneMapSourceKind,
		ToneMapPreflightRequired:   result.ToneMapPreflightRequired,
		ToneMapSourceRevision:      result.ToneMapSourceRevision,
		ToneMapDVConfigPresent:     track.DVConfigPresent,
		ToneMapDVBLCompatIDPresent: track.DVBLCompatIDPresent,
		ToneMapDVBLPresent:         track.DVBLPresent,
		ToneMapDVRPUPresent:        track.DVRPUPresent,
	}
	return scopeToneMapSourceMetadataV3(metadata, result.ToneMapMode)
}

// Dolby Vision provenance is part of a frozen tone-map recipe, not a general
// source description. Leaving it attached to a video-copy remux makes the
// execution boundary correctly reject the orphaned fields as a partial recipe.
func scopeToneMapSourceMetadataV3(metadata playback.SourceExecutionMetadataV3, mode tonemap.Mode) playback.SourceExecutionMetadataV3 {
	if mode != "" {
		return metadata
	}
	metadata.ToneMapSourceKind = ""
	metadata.ToneMapPreflightRequired = false
	metadata.ToneMapSourceRevision = tonemap.SourceRevision{}
	metadata.ToneMapDVConfigPresent = false
	metadata.ToneMapDVBLCompatIDPresent = false
	metadata.ToneMapDVBLPresent = false
	metadata.ToneMapDVRPUPresent = false
	return metadata
}

func sourceVideoTranscodeFactsV3(file *models.MediaFile, result playback.PlannerResultV3) (string, int) {
	if result.FrozenSourceMetadata != nil {
		return result.FrozenSourceMetadata.VideoProfile, result.FrozenSourceMetadata.VideoBitDepth
	}
	_, profile, bitDepth := playback.SourceVideoTranscodeFacts(file)
	return profile, bitDepth
}

// v3SessionStreamState builds the durable stream state for a prepared transport.
func (h *PlaybackHandler) v3SessionStreamState(ctx context.Context, session *playback.Session, file *models.MediaFile, result playback.PlannerResultV3, transport preparedTransportV3, mode mediaAuthModeV3) playback.SessionStreamState {
	state := playback.SessionStreamState{
		PlayMethod:                result.PlayMethod,
		BasePlayMethod:            result.PlayMethod,
		AudioTrackIndex:           plannedAudioTrackIndexV3(result, session.AudioTrackIndex),
		TranscodeAudio:            result.TranscodeAudio,
		RemuxDVMode:               remuxDVModeForPlanV3(result.Plan),
		TranscodeHWAccel:          transport.hwAccel,
		ToneMapMode:               transport.toneMapMode,
		TranscodeNodeURL:          transport.nodeURL,
		TranscodeTransportID:      transport.transportID,
		TranscodeRouteSet:         true,
		RequireMediaAuthorization: mode.headerAuth,
		MediaAuthorizationSet:     true,
		ClientIP:                  clientip.FromContext(ctx),
		ClientName:                session.ClientName,
		ClientVersion:             session.ClientVersion,
		ClientUserAgent:           session.ClientUserAgent,
		StreamBitrateKbps:         result.TargetBitrateKbps,
		TargetVideoCodec:          result.TargetVideoCodec,
		TargetAudioCodec:          result.TargetAudioCodec,
		SourceAudioChannels:       result.SourceAudioChannels,
		TargetAudioChannels:       result.TargetAudioChannels,
		TargetAudioBitrateKbps:    result.TargetAudioBitrateKbps,
		TargetResolution:          result.TargetResolution,
		SubtitleTrackIndex:        result.SubtitleTransportTrackIndex,
		SubtitleBurnIn:            result.SubtitleBurnIn,
	}
	if result.Plan != nil && (result.Plan.Delivery == playback.DeliveryTranscodeHLSV3 || result.Plan.Delivery == playback.DeliveryRemuxHLSV3) {
		state.SegmentDuration = playback.DefaultSegmentDuration
	}
	if state.StreamBitrateKbps <= 0 {
		state.StreamBitrateKbps = result.TargetAudioBitrateKbps
	}
	if state.StreamBitrateKbps <= 0 {
		state.StreamBitrateKbps = fileBitrateKbps(file)
	}
	return state
}

func (h *PlaybackHandler) updateV3SessionState(ctx context.Context, session *playback.Session, file *models.MediaFile, result playback.PlannerResultV3, transport preparedTransportV3, mode mediaAuthModeV3) error {
	return h.sessionMgr.UpdateStreamState(session.ID, h.v3SessionStreamState(ctx, session, file, result, transport, mode))
}

func plannedAudioTrackIndexV3(result playback.PlannerResultV3, fallback int) int {
	if result.Plan != nil && result.Plan.SelectedTracks.Audio != nil && result.Plan.SelectedTracks.Audio.Index != nil {
		return *result.Plan.SelectedTracks.Audio.Index
	}
	return fallback
}

func transportGenerationV3(sessionID, planID string) string {
	planSuffix := strings.TrimPrefix(planID, "plan:")
	if len(planSuffix) > 12 {
		planSuffix = planSuffix[:12]
	}
	return sessionID + "-" + planSuffix + "-" + uuid.NewString()[:8]
}

// attachSubtitleArtifactV3 republishes the plan's subtitle inventory with
// session-scoped URLs, then resolves the plan's selected ordinal against it and
// stamps that entry's URL onto the artifact. Publishing and resolution share one
// ordering implementation, so an artifact URL can never point at a different
// track than the inventory entry the client selected.
//
// The inventory is scoped unconditionally, not only when a track is selected:
// spec §8 makes it the authoritative track list and says a sidecar entry carries
// a `url` "once a session exists to scope it to" — which is true here for every
// entry, whatever the current selection is. Gating it on the selection published
// a URL-less menu whenever playback started with subtitles off, so a client
// building its picker from the inventory (the Cast receiver's text tracks, for
// one) had nothing fetchable to offer.
func (h *PlaybackHandler) attachSubtitleArtifactV3(ctx context.Context, sessionID string, file *models.MediaFile, plan *playback.PlanV3, selectedIndex int, recipe *playback.ExecutableRecipeV3) error {
	if plan == nil || file == nil {
		return nil
	}
	var frozenDownloaded *subtitles.DownloadedSubtitle
	if recipe != nil && recipe.SubtitleSource == playback.SubtitleSourceDownloadedV3 {
		if h == nil || h.SubtitleRepo == nil || recipe.DownloadedSubtitleID <= 0 {
			return errors.New("the frozen downloaded subtitle is unavailable")
		}
		selected, err := h.SubtitleRepo.GetDownloadedSubtitle(ctx, recipe.DownloadedSubtitleID)
		if err != nil {
			return wrapSubtitleStoreErrorV3(err)
		}
		if selected == nil || selected.MediaFileID != file.ID {
			return errors.New("the frozen downloaded subtitle is unavailable for the selected media file")
		}
		frozenDownloaded = selected
	}
	inventory := playback.ScopeSubtitleInventoryV3(sessionID, file, plan.Subtitle.Inventory)
	// A plan restored from JSON no longer carries the server-only downloaded
	// row IDs. Rebuild only in that case; a fresh plan stays on the exact
	// planning snapshot instead of listing a mutable repository twice.
	if playback.SubtitleInventoryNeedsDownloadedIdentityV3(plan.Subtitle.Inventory) {
		if h == nil || h.SubtitleRepo == nil {
			return errors.New("the downloaded subtitle inventory is unavailable")
		}
		downloaded, err := h.SubtitleRepo.ListDownloadedSubtitles(ctx, file.ID)
		if err != nil {
			return wrapSubtitleStoreErrorV3(err)
		}
		inventory = playback.SubtitleInventoryV3(sessionID, file, downloadedSubtitleEntriesV3(file, downloaded))
	}
	plan.Subtitle.Inventory = inventory
	// Only render and convert publish a client-fetchable artifact; off and
	// burn_in have none by definition. Clear rather than leave whatever the plan
	// arrived with: a seek reanchor replays record.CurrentPlan verbatim
	// (frozenSeekReanchorResultV3), so a plan that once rendered a sidecar would
	// otherwise keep republishing that artifact after the selection changed —
	// which is exactly the stale `mode: "off"` plus artifact pair observed in
	// the field, and enough for a client to alias the artifact onto the track it
	// is actually playing. An off decision carries no track either, so its
	// track_id goes with the artifact; burn_in keeps the track it burns in.
	if selectedIndex < 0 || (plan.Subtitle.Mode != playback.SubtitleRenderV3 && plan.Subtitle.Mode != playback.SubtitleConvertV3) {
		plan.Subtitle.Artifact = nil
		if plan.Subtitle.Mode == playback.SubtitleOffV3 {
			plan.Subtitle.TrackID = ""
		}
		return nil
	}
	item, ok := playback.SubtitleInventoryItemAtV3(inventory, selectedIndex)
	if !ok && frozenDownloaded == nil {
		return errors.New("selected subtitle artifact is absent from the frozen inventory")
	}
	if frozenDownloaded == nil && item.URL == "" {
		return fmt.Errorf("subtitle track %d is %s and has no fetchable artifact", selectedIndex, item.Delivery)
	}
	format := strings.ToLower(item.Codec)
	mime := subtitleMIMEV3(format)
	url := item.URL
	if frozenDownloaded != nil {
		format = strings.ToLower(string(frozenDownloaded.Format))
		mime = subtitleMIMEV3(format)
		url = playback.DownloadedSubtitleStreamURLV3(sessionID, selectedIndex, string(frozenDownloaded.Format), file.ID, frozenDownloaded.ID)
		// The plan's selected ordinal must advertise the same opaque URL as the
		// artifact even if another downloaded row was inserted before a seek.
		for index := range plan.Subtitle.Inventory {
			if plan.Subtitle.Inventory[index].CombinedIndex == selectedIndex {
				plan.Subtitle.Inventory[index].URL = url
				plan.Subtitle.Inventory[index].Codec = string(frozenDownloaded.Format)
				break
			}
		}
	}
	if plan.Subtitle.Mode == playback.SubtitleConvertV3 {
		format = playback.SubtitleFormatVTTV3
		mime = playback.SubtitleMIMEVTTV3
		url = forceSubtitleExtensionV3(url, playback.SubtitleExtVTTV3)
	}
	plan.Subtitle.Artifact = &playback.SubtitleArtifactV3{URL: url, MIMEType: mime, Format: format, TimingOriginSeconds: plan.Timeline.StreamOriginSeconds}
	return nil
}

// downloadedSubtitleInventoryV3 lists the downloaded and AI-generated tracks
// that follow the file's own tracks in the combined-ordinal space. The
// repository orders by created_at, so the ordinals it produces are stable.
func (h *PlaybackHandler) downloadedSubtitleInventoryV3(ctx context.Context, file *models.MediaFile) []playback.SubtitleInventoryEntryV3 {
	if h == nil || h.SubtitleRepo == nil || file == nil {
		return nil
	}
	downloaded, err := h.SubtitleRepo.ListDownloadedSubtitles(ctx, file.ID)
	if err != nil {
		return nil
	}
	return downloadedSubtitleEntriesV3(file, downloaded)
}

// downloadedSubtitleEntriesV3 converts downloaded rows into inventory entries
// at the ordinals that follow the file's external and embedded tracks.
func downloadedSubtitleEntriesV3(file *models.MediaFile, downloaded []subtitles.DownloadedSubtitle) []playback.SubtitleInventoryEntryV3 {
	if file == nil {
		return nil
	}
	base := len(file.ExternalSubtitles) + len(file.SubtitleTracks)
	result := make([]playback.SubtitleInventoryEntryV3, 0, len(downloaded))
	for index, value := range downloaded {
		result = append(result, playback.SubtitleInventoryEntryV3{
			CombinedIndex:        base + index,
			Codec:                string(value.Format),
			Source:               playback.SubtitleSourceDownloadedV3,
			Language:             value.Language,
			Label:                downloadedSubtitleLabelV3(value),
			HearingImpaired:      value.HearingImpaired,
			DownloadedSubtitleID: value.ID,
		})
	}
	return result
}

func downloadedSubtitleLabelV3(value subtitles.DownloadedSubtitle) string {
	if value.ReleaseName == "" && value.Provider == "" {
		return ""
	}
	return value.ReleaseName + " (" + value.Provider + ")"
}

// HandleReplanPlaybackV3 provides persistent idempotency and preserves the old
// transport until a successor has entered its startup state and the new plan is
// durably committed.
func (h *PlaybackHandler) HandleReplanPlaybackV3(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	if userID == 0 || profileID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication and profile are required")
		return
	}
	body, err := readBoundedV3Body(w, r, maxPlaybackV3BodyBytes)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	var req playback.ReplanRequestV3
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid replan request")
		return
	}
	// Reject malformed identity/bounds before doing any session lookup. When
	// client_features is omitted, temporarily allow the only validation rule
	// that depends on the durable start request; the authoritative merge and a
	// second full validation happen after the attempt is loaded below.
	preflightReq := req
	if preflightReq.ClientFeatures == nil {
		preflightReq.ClientFeatures = []string{playback.FeatureClientVideoTransforms}
	}
	if err := preflightReq.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid replan request")
		return
	}
	sessionID := chiURLParamV3(r, "session_id")
	releaseSlot, err := h.acquireReplanSlotV3(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "replan_capacity_exhausted", "The server is replanning too many sessions; retry shortly")
		return
	}
	defer releaseSlot()
	unlockReplan := h.lockReplanV3(sessionID)
	defer unlockReplan()
	unlockStore, err := h.PlanStoreV3.AcquireSessionLock(r.Context(), sessionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to serialize the replan request")
		return
	}
	defer unlockStore()
	record, err := h.PlanStoreV3.GetAttempt(r.Context(), sessionID)
	if err != nil {
		// A store outage must read as retryable, not as the session being
		// gone: clients tear playback down on session_not_found.
		if !errors.Is(err, playback.ErrSessionNotFound) {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load the playback attempt")
			return
		}
		writePlaybackSessionNotFound(w)
		return
	}
	if record.UserID != userID || record.ProfileID != profileID {
		writeError(w, http.StatusForbidden, "forbidden", "Session belongs to another profile")
		return
	}
	if record.PlaybackAttemptID != req.PlaybackAttemptID {
		writeError(w, http.StatusConflict, "stale_playback_plan", "The failed plan is no longer current")
		return
	}
	// Replan feature advertisement is optional. Validate transformations against
	// the durable start-time features when the client omits the unchanged list;
	// otherwise a valid replan can be rejected before executeReplanV3 gets the
	// chance to perform the same merge.
	if req.ClientFeatures == nil {
		req.ClientFeatures = append([]string(nil), record.NormalizedRequest.ClientFeatures...)
	}
	// The attempt-sticky features are fixed at start. Neither an omitted/empty
	// feature list nor a later opt-in can add or drop one mid-attempt: media
	// authentication would leave two security contracts alive for one session
	// (a legacy URL from an earlier plan stays usable until its signed recipe
	// expires), and a dropped software-decode opt-in would silently convert a
	// direct route into a transcode and persist that downgrade into the durable
	// normalized request. Stop/start is the explicit boundary for both.
	//
	// This is the single place attempt stickiness is enforced: everything
	// downstream — the durable normalized request, the transport's media-auth
	// mode, the planner's evidence tiers — reads the pinned list.
	req.ClientFeatures = playback.PinAttemptStickyFeaturesV3(req.ClientFeatures, record.NormalizedRequest.ClientFeatures)
	if err := req.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid replan request")
		return
	}
	if _, err := h.sessionMgr.GetSession(sessionID); err != nil {
		writePlaybackSessionNotFound(w)
		return
	}
	digestBytes := sha256.Sum256(body)
	digest := hex.EncodeToString(digestBytes[:])
	lease, err := h.PlanStoreV3.BeginReplan(
		r.Context(),
		sessionID,
		req.ReplanRequestID,
		digest,
		record.CurrentReplanRequestID,
		time.Now().Add(replanLeaseDurationV3),
	)
	if errors.Is(err, playback.ErrIdempotencyKeyReusedV3) {
		writeError(w, http.StatusConflict, "idempotency_key_reused", "The replan request ID was reused with different input")
		return
	}
	if errors.Is(err, playback.ErrStaleReplanLeaseV3) {
		writeError(w, http.StatusConflict, "stale_playback_plan", "A newer replacement plan is already active")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to reserve the replan request")
		return
	}
	if lease.State == playback.ReplanLeaseInFlightV3 {
		writeError(w, http.StatusConflict, "replan_in_progress", "An identical replan is still in progress")
		return
	}
	if lease.State == playback.ReplanLeaseCompletedV3 {
		if record.CurrentReplanRequestID != req.ReplanRequestID || !completedReplanResponseMatchesAttemptV3(lease.Response, record) {
			writeError(w, http.StatusConflict, "stale_playback_plan", "A newer replacement plan is already active")
			return
		}
		if _, err := h.sessionMgr.GetSession(sessionID); err != nil {
			writePlaybackSessionNotFound(w)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(lease.Response)
		return
	}
	leaseCompleted := false
	defer func() {
		if leaseCompleted {
			return
		}
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), replanReleaseTimeoutV3)
		defer cancel()
		if err := h.PlanStoreV3.ReleaseReplan(releaseCtx, sessionID, req.ReplanRequestID, lease.LeaseToken); err != nil {
			slog.ErrorContext(r.Context(), "protocol v3 replan lease release failed", "component", "api", "session", sessionID, "replan_request_id", req.ReplanRequestID, "error", err)
		}
	}()
	if record.CurrentPlanID != req.FailedPlanID {
		writeError(w, http.StatusConflict, "stale_playback_plan", "The failed plan is no longer current")
		return
	}
	response, updated, transport, replanErr := h.executeReplanV3(r, record, req)
	if replanErr != nil {
		if transport != nil {
			transport.rollback()
		}
		response := playback.NewTerminalResponseV3(replanErr.reason, replanErr.message, replanErr.retryable)
		encoded, _ := json.Marshal(response)
		terminalRecord := *record
		terminalRecord.CurrentReplanRequestID = req.ReplanRequestID
		if err := h.PlanStoreV3.CompleteReplan(r.Context(), sessionID, req.ReplanRequestID, lease.LeaseToken, record.CurrentReplanRequestID, encoded, terminalRecord); err != nil {
			if errors.Is(err, playback.ErrReplanSupersededV3) {
				writeError(w, http.StatusConflict, "stale_playback_plan", "A newer replacement plan is already active")
				return
			}
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to persist the terminal replan decision")
			return
		}
		leaseCompleted = true
		writeJSON(w, http.StatusOK, response)
		return
	}
	updated.CurrentReplanRequestID = req.ReplanRequestID
	encoded, _ := json.Marshal(response)
	var rollbackSession func() error
	if transport != nil && transport.applySession != nil {
		var err error
		rollbackSession, err = transport.applySession()
		if err != nil {
			transport.rollback()
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to commit the live replacement session")
			return
		}
	}
	if err := h.PlanStoreV3.CompleteReplan(r.Context(), sessionID, req.ReplanRequestID, lease.LeaseToken, record.CurrentReplanRequestID, encoded, updated); err != nil {
		rollbackFailed := false
		if rollbackSession != nil {
			if rollbackErr := rollbackSession(); rollbackErr != nil {
				rollbackFailed = true
				slog.ErrorContext(r.Context(), "protocol v3 replacement rollback failed", "session", sessionID, "error", rollbackErr)
			}
		}
		if transport != nil {
			transport.rollback()
		}
		if rollbackFailed {
			_ = h.stopPlaybackSessionByID(context.WithoutCancel(r.Context()), sessionID, false)
		}
		if errors.Is(err, playback.ErrReplanSupersededV3) {
			writeError(w, http.StatusConflict, "stale_playback_plan", "A newer replacement plan is already active")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to commit the replacement plan")
		return
	}
	leaseCompleted = true
	if transport != nil {
		transport.commit()
		if transport.afterDurableCommit != nil {
			transport.afterDurableCommit()
		}
	}
	h.raceCopySafetyV3(updated.EffectiveMediaFileID, response.PlaybackPlan)
	writeJSON(w, http.StatusOK, response)
}

// raceCopySafetyV3 resolves an unknown H.264 copy-safety verdict behind a plan
// that stream-copies video. It is called after the durable commit on both the
// start and replan paths, so the scan only ever chases a route a client was
// actually handed, and it returns immediately — no response waits on it.
func (h *PlaybackHandler) raceCopySafetyV3(fileID int, plan *playback.PlanV3) {
	if h == nil || h.CopySafetyRacer == nil || fileID <= 0 || plan == nil {
		return
	}
	h.CopySafetyRacer.RaceScanForPlan(fileID, plan)
}

// executeReplanV3 prepares an atomic replacement for a failed playback route.
func (h *PlaybackHandler) executeReplanV3(r *http.Request, record *playback.AttemptRecordV3, req playback.ReplanRequestV3) (playback.DecisionResponseV3, playback.AttemptRecordV3, *preparedTransportV3, *transportErrorV3) {
	reservationHeld := false
	reservationHandedOff := false
	cancelReservation := func() {
		if reservationHeld {
			if canceller, ok := h.sessionMgr.(replacementReservationCancellerV3); ok {
				canceller.CancelReplacementReservation(record.SessionID)
			}
			reservationHeld = false
		}
	}
	defer func() {
		if !reservationHandedOff {
			cancelReservation()
		}
	}()
	start := record.NormalizedRequest
	operation := req.EffectiveOperation()
	seekReanchor := operation == playback.ReplanOperationSeekReanchorV3
	seekFailureRecovery := operation == playback.ReplanOperationSeekFailureRecoveryV3
	seekScopedRecovery := seekReanchor || seekFailureRecovery
	trackChange := operation == playback.ReplanOperationTrackChangeV3
	qualityChange := operation == playback.ReplanOperationQualityChangeV3
	outputChange := operation == playback.ReplanOperationOutputChangeV3
	// User-intent operations replace the legacy audio PATCH and client-recipe
	// transcode start. Nothing failed, so their previous route stays eligible:
	// neither attempted-key history nor the failed-plan exclusion applies.
	userIntentOperation := trackChange || qualityChange || outputChange
	intentChange := false
	if seekScopedRecovery {
		if err := validateSeekRecoveryRequestV3(record, req); err != nil {
			reason := "seek_reanchor_intent_mismatch"
			if seekFailureRecovery {
				reason = "seek_failure_recovery_intent_mismatch"
			}
			return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{
				reason:  reason,
				message: err.Error(),
			}
		}
		// Reconstruct the complete route intent from the durable current attempt.
		// A seek request is not an authority boundary for replacing capability or
		// device evidence: accepting those fields here could make the same file
		// select a materially different route based on request-only claims.
		start.FileID = record.EffectiveMediaFileID
		start.StartPosition = &req.PositionSeconds
		applySelectedTracksToStartV3(&start, record.CurrentPlan.SelectedTracks)
	} else {
		// Failure replans may omit unchanged tracks. The durable current plan
		// holds the authoritative effective-file selections; the normalized
		// request can still carry requested-edition identities after an
		// alternate-version fallback, and validating those against the
		// effective file would reject an otherwise valid replan. Seed from
		// the plan first, then overlay the request's explicit changes.
		applySelectedTracksToStartV3(&start, record.CurrentPlan.SelectedTracks)
		switch {
		case trackChange:
			intentChange = audioSelectionDiffersFromStartV3(req.SelectedTracks, start) ||
				subtitleSelectionDiffersFromStartV3(req.SelectedTracks, start)
		case qualityChange:
			nextQuality, _ := playback.NormalizeQualityV3(req.QualityPreference)
			intentChange = nextQuality != start.QualityPreference
		case outputChange:
			intentChange = true
		default:
			switch req.Failure.Classification {
			case "quality_changed":
				nextQuality, _ := playback.NormalizeQualityV3(req.QualityPreference)
				intentChange = nextQuality != start.QualityPreference
			case "audio_track_changed":
				intentChange = audioSelectionDiffersFromStartV3(req.SelectedTracks, start)
			case "subtitle_track_changed":
				intentChange = subtitleSelectionDiffersFromStartV3(req.SelectedTracks, start)
			case "output_route_changed":
				intentChange = req.ClientPlaybackContext.Output.OutputContextID != start.ClientPlaybackContext.Output.OutputContextID
			}
		}
		// Failure replans use the current effective file. Quality/output intent may
		// restart source selection from the requested edition, but a track change
		// is expressed in the mounted alternate's inventory and must stay pinned to
		// that file or its combined ordinals can select unrelated tracks.
		start.FileID = record.EffectiveMediaFileID
		if intentChange && !trackChange {
			start.FileID = record.RequestedMediaFileID
		}
		if strings.TrimSpace(req.QualityPreference) != "" {
			// Replans may omit unchanged intent. Normalizing an absent quality
			// would silently reset "original" or a fixed rung to "auto".
			start.QualityPreference = req.QualityPreference
		}
		start.StartPosition = &req.PositionSeconds
		start.Metered = req.Metered
		start.BandwidthEstimateKbps = copyOptionalIntV3(req.BandwidthEstimateKbps)
		start.BandwidthCapKbps = copyOptionalIntV3(req.BandwidthCapKbps)
		start.Capabilities = req.Capabilities
		start.ClientPlaybackContext = req.ClientPlaybackContext
		if req.ClientFeatures != nil {
			// Feature advertisement is single-location (top-level); a replan
			// that sends it refreshes the durable request's copy alongside the
			// capability payloads. Omission keeps the start-time features.
			start.ClientFeatures = req.ClientFeatures
		}
		if trackChange {
			// A track_change is the only operation where an omitted subtitle
			// means "subtitles off". Failure, seek, and quality replans may omit
			// unchanged identities and must not erase the durable selection.
			applySelectedTracksToStartV3(&start, req.SelectedTracks)
		} else {
			applySelectedTrackOverridesToStartV3(&start, req.SelectedTracks)
		}
	}
	requestedFallbackID := record.EffectiveMediaFileID
	effectiveFallbackID := record.RequestedMediaFileID
	if seekScopedRecovery {
		// Edition fallback is useful for ordinary failure replans, but never for
		// a seek operation: the caller asked to move within the currently mounted
		// source, not to select another version when that source disappears.
		requestedFallbackID = 0
		effectiveFallbackID = 0
	}
	requestedFile, err := h.loadFileByPreferredID(r.Context(), record.RequestedMediaFileID, requestedFallbackID)
	requestedEditionResolved := err == nil && requestedFile != nil && requestedFile.ID == record.RequestedMediaFileID
	if err != nil || requestedFile == nil {
		if !seekScopedRecovery {
			return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{reason: "source_unavailable", message: "The requested media source is unavailable."}
		}
		// The requested edition is identity-only once another effective edition
		// is mounted. Seeking must depend on that effective file remaining
		// available, not on an inactive original edition still resolving.
		requestedFile = &models.MediaFile{ID: record.RequestedMediaFileID}
	}
	plannerRequestedFile := requestedFile
	if requestedFile.ID != record.RequestedMediaFileID {
		// The live loader may fall back to the current effective file when the
		// original edition is gone. Keep that file for metadata/remapping while
		// preserving the durable requested-edition identity in every new plan.
		plannerRequestedFile = &models.MediaFile{ID: record.RequestedMediaFileID}
	}
	currentEffectiveFile, err := h.loadFileByPreferredID(r.Context(), record.EffectiveMediaFileID, effectiveFallbackID)
	if err != nil || currentEffectiveFile == nil {
		return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{reason: "source_unavailable", message: "The effective media source is unavailable."}
	}
	effectiveFile := currentEffectiveFile
	currentEffectiveStart := start
	if intentChange && !trackChange {
		// Prefer returning to the requested edition, but a quality/output/track
		// change must not abandon a healthy active alternate merely because the
		// inactive original has gone missing since playback started.
		if requestedEditionResolved && preflightPlaybackFile(r.Context(), requestedFile, h.MissingMarker, h.EventsHub) == nil {
			effectiveFile = requestedFile
		}
		// Track identities only need remapping when the effective edition
		// actually changes. Remapping within the same file would degrade an
		// exact selection to a best-match lookup — e.g. moving a listener
		// from an eng/ac3 commentary track to the identically-shaped main
		// track on a quality change.
		if currentEffectiveFile.ID != effectiveFile.ID {
			candidateStart := start
			remapErr := remapAudioSelectionV3(currentEffectiveFile, effectiveFile, &candidateStart)
			if remapErr == nil && (candidateStart.SubtitleTrackIndex != nil || candidateStart.SubtitleTrackID != "") {
				remapErr = h.remapSubtitleSelectionV3(r.Context(), currentEffectiveFile, effectiveFile, &candidateStart)
			}
			if remapErr != nil && outputChange {
				// An output refresh may make the requested edition viable again,
				// but it must not retire a healthy active alternate merely because
				// the viewer selected a track unique to that alternate.
				effectiveFile = currentEffectiveFile
			} else if remapErr != nil {
				return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{reason: "track_unavailable", message: remapErr.Error()}
			} else {
				start = candidateStart
			}
		}
	}
	start.FileID = effectiveFile.ID
	if err := preflightPlaybackFile(r.Context(), effectiveFile, h.MissingMarker, h.EventsHub); err != nil {
		return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{
			reason:  "source_unavailable",
			message: "The effective media source is unavailable.",
			cause:   err,
		}
	}
	seekDuration := float64(effectiveFile.Duration)
	if seekReanchor && record.FrozenRecipe.ValidFor(record.CurrentPlan) {
		seekDuration = record.FrozenRecipe.SourceDurationSeconds
	}
	if seekScopedRecovery && seekDuration > 0 && req.PositionSeconds > seekDuration {
		return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{
			reason:  "invalid_seek_position",
			message: "The requested seek position is beyond the end of the selected media source.",
		}
	}
	if _, err := start.NormalizeAndValidate(); err != nil {
		return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{reason: "invalid_replan", message: err.Error()}
	}
	audioIndex := 0
	if !seekReanchor {
		audioIndex, err = resolveV3AudioIndex(effectiveFile, start.AudioTrackID, start.AudioTrackIndex)
		if err != nil {
			return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{reason: "track_unavailable", message: err.Error()}
		}
	}
	attemptedKeys := []string(nil)
	if !intentChange && !seekReanchor && !userIntentOperation {
		attemptedKeys = append(attemptedKeys, req.AttemptedPlanKeys...)
		if !containsStringExactV3(attemptedKeys, req.PlanAttemptKey) {
			attemptedKeys = append(attemptedKeys, req.PlanAttemptKey)
		}
	}
	if !seekReanchor && !userIntentOperation && (!intentChange || seekFailureRecovery) {
		// Always exclude the durable server recipe so stale or malformed client
		// history cannot immediately re-select the route that just failed and
		// ping-pong the session. A client-reported local mutation (for example a
		// PCM recovery route) is folded into the failed plan's key here — the
		// server owns the hash; clients only echo opaque keys.
		currentKey := playback.PlanAttemptKeyV3(record.CurrentPlan, record.NormalizedRequest.ClientPlaybackContext.Output.OutputContextID, req.LocalMutations)
		if !containsStringExactV3(attemptedKeys, currentKey) {
			attemptedKeys = append(attemptedKeys, currentKey)
		}
		if len(req.LocalMutations) > 0 {
			// The unmutated recipe already failed before the client mutated it
			// locally; exclude it as well.
			unmutatedKey := playback.PlanAttemptKeyV3(record.CurrentPlan, record.NormalizedRequest.ClientPlaybackContext.Output.OutputContextID, nil)
			if !containsStringExactV3(attemptedKeys, unmutatedKey) {
				attemptedKeys = append(attemptedKeys, unmutatedKey)
			}
		}
	}
	var result playback.PlannerResultV3
	var toneMapCapabilityErr error
	var plannerSettings playback.PlannerSettingsV3
	var plannerSettingsErr error
	if !seekReanchor {
		plannerSettings, plannerSettingsErr = h.plannerSettingsV3Result(r.Context())
	}
	if seekReanchor {
		if err := h.validateFrozenSubtitleIdentityV3(r.Context(), effectiveFile, record.FrozenRecipe); err != nil {
			return playback.DecisionResponseV3{}, *record, nil, subtitleArtifactErrorV3("The selected subtitle is no longer available at its frozen route.", err)
		}
		var frozenErr error
		result, frozenErr = frozenSeekReanchorResultV3(record, req.PositionSeconds, time.Now())
		if frozenErr != nil {
			return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{
				reason:    "seek_reanchor_recipe_unavailable",
				message:   "The active playback recipe cannot be reopened; start a new playback attempt.",
				retryable: true,
			}
		}
	} else {
		result, toneMapCapabilityErr = h.planPlaybackWithCapabilitiesV3(r.Context(), playback.PlannerInputV3{Request: start, RequestedFile: plannerRequestedFile, EffectiveFile: effectiveFile, AudioTrackIndex: audioIndex, Settings: plannerSettings, Registry: h.transformationRegistryV3(r.Context()), DVRPUStrippable: h.lazyDVRPUStrippableV3(r.Context(), effectiveFile), Now: time.Now(), AttemptedKeys: attemptedKeys, AdditionalSubtitles: h.downloadedSubtitleInventoryV3(r.Context(), effectiveFile)})
	}
	if outputChange && result.Terminal != nil && effectiveFile.ID != currentEffectiveFile.ID {
		// Returning to the requested edition is speculative during an output
		// refresh. Any terminal from that probe must fall back to the edition
		// already playing, not only HDR/alternate-selection terminals: its audio,
		// subtitle, or delivery constraints may still differ from the active file.
		start = currentEffectiveStart
		start.FileID = currentEffectiveFile.ID
		effectiveFile = currentEffectiveFile
		audioIndex, err = resolveV3AudioIndex(effectiveFile, start.AudioTrackID, start.AudioTrackIndex)
		if err != nil {
			return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{reason: "track_unavailable", message: err.Error()}
		}
		result, toneMapCapabilityErr = h.planPlaybackWithCapabilitiesV3(r.Context(), playback.PlannerInputV3{Request: start, RequestedFile: plannerRequestedFile, EffectiveFile: effectiveFile, AudioTrackIndex: audioIndex, Settings: plannerSettings, Registry: h.transformationRegistryV3(r.Context()), DVRPUStrippable: h.lazyDVRPUStrippableV3(r.Context(), effectiveFile), Now: time.Now(), AttemptedKeys: attemptedKeys, AdditionalSubtitles: h.downloadedSubtitleInventoryV3(r.Context(), effectiveFile)})
	}
	if terminalAllowsAlternateFileV3(result.Terminal) && replanAllowsAlternateFileV3(operation, start.QualityPreference) {
		if alternates, alternateErr := h.findAlternateFiles(r.Context(), requestedFile); alternateErr == nil {
			baseStart := start
			baseEffectiveFile := effectiveFile
			baseAudioIndex := audioIndex
			var firstFailureResult playback.PlannerResultV3
			var firstFailureToneMapErr error
			var firstFailureFile *models.MediaFile
			var firstFailureStart playback.StartRequestV3
			firstFailureAudioIndex := 0
			for _, alternate := range alternates {
				if alternate.ID == baseEffectiveFile.ID {
					continue
				}
				candidateFile := h.ensurePlaybackProbe(r.Context(), alternate)
				candidateStart := baseStart
				candidateAudioIndex := remapAudioIndexV3(baseEffectiveFile, candidateFile, baseAudioIndex)
				var candidateResult playback.PlannerResultV3
				var candidateToneMapErr error
				if err := h.remapSubtitleSelectionV3(r.Context(), baseEffectiveFile, candidateFile, &candidateStart); err != nil {
					candidateResult = playback.PlannerResultV3{Terminal: &playback.TerminalV3{
						Reason:    terminalSubtitleUnavailableInVersionV3,
						Message:   "The selected subtitle track is unavailable in the fallback media version.",
						Retryable: false,
					}}
				} else {
					candidateStart.FileID = candidateFile.ID
					if err := preflightPlaybackFile(r.Context(), candidateFile, h.MissingMarker, h.EventsHub); err != nil {
						continue
					}
					candidateResult, candidateToneMapErr = h.planPlaybackWithCapabilitiesV3(r.Context(), playback.PlannerInputV3{Request: candidateStart, RequestedFile: plannerRequestedFile, EffectiveFile: candidateFile, AudioTrackIndex: candidateAudioIndex, Settings: plannerSettings, Registry: h.transformationRegistryV3(r.Context()), DVRPUStrippable: h.lazyDVRPUStrippableV3(r.Context(), candidateFile), Now: time.Now(), AttemptedKeys: attemptedKeys, AdditionalSubtitles: h.downloadedSubtitleInventoryV3(r.Context(), candidateFile)})
				}
				if candidateResult.Terminal == nil {
					start = candidateStart
					effectiveFile = candidateFile
					audioIndex = candidateAudioIndex
					result = candidateResult
					toneMapCapabilityErr = candidateToneMapErr
					break
				}
				if firstFailureFile == nil {
					firstFailureResult = candidateResult
					firstFailureToneMapErr = candidateToneMapErr
					firstFailureFile = candidateFile
					firstFailureStart = candidateStart
					firstFailureAudioIndex = candidateAudioIndex
				}
			}
			if result.Terminal != nil && firstFailureFile != nil {
				start = firstFailureStart
				effectiveFile = firstFailureFile
				audioIndex = firstFailureAudioIndex
				result = firstFailureResult
				toneMapCapabilityErr = firstFailureToneMapErr
			}
		}
	}
	result = retryIncompleteToneMapPlanningV3(result, toneMapCapabilityErr)
	result = retryIncompletePlaybackSettingsV3(result, plannerSettingsErr)
	h.clarifyOriginalQuality4KTerminalV3(r.Context(), result.Terminal, requestedFile, replanAlternateFilePinnedByOriginalQualityV3(operation, start.QualityPreference))
	// Media authentication is attempt-sticky (pinned in HandleReplanPlaybackV3),
	// so this mode always equals the one the attempt started under: a reused
	// transport cannot change the session's media security contract.
	mode := headerAuthenticatedMediaV3(start.ClientFeatures)
	if !seekReanchor {
		// A freshly planned replan can land on the same refused progressive
		// remux a start would have; escalate it identically. A seek reanchor
		// replays the frozen recipe verbatim and must not change route identity,
		// so it is excluded — its route was escalated when the attempt started.
		escalated, escalateErr := h.escalateRefusedProgressiveRemuxV3(r.Context(), mode,
			func() playback.PlannerInputV3 {
				return h.plannerInputV3(r.Context(), start, plannerRequestedFile, effectiveFile, audioIndex, attemptedKeys)
			}, result)
		if escalateErr != nil {
			return playback.DecisionResponseV3{}, *record, nil, escalateErr
		}
		result = escalated
	}
	if result.Terminal != nil {
		return playback.NewTerminalResponseV3(result.Terminal.Reason, result.Terminal.Message, result.Terminal.Retryable), *record, nil, nil
	}
	session, err := h.sessionMgr.GetSession(record.SessionID)
	if err != nil {
		return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{reason: "session_expired", message: "The playback session has expired.", retryable: true}
	}
	replacementManager, ok := h.sessionMgr.(replacementStateManagerV3)
	if !ok {
		return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{reason: "internal_error", message: "The live session manager does not support atomic replacement."}
	}
	if checker, ok := h.sessionMgr.(replacementAdmissionCheckerV3); ok {
		if err := checker.CheckReplacementAllowed(r.Context(), session.ID, result.PlayMethod, result.TranscodeAudio); err != nil {
			mapped := sessionStartErrorV3(err)
			return playback.DecisionResponseV3{}, *record, nil, mapped
		}
		_, reservationHeld = h.sessionMgr.(replacementReservationCancellerV3)
	}
	if checker, ok := h.sessionMgr.(transcodePermissionChecker); ok && (result.PlayMethod == playback.PlayTranscode || result.TranscodeAudio) {
		if err := checker.CheckTranscodingAllowed(r.Context(), session.UserID, result.PlayMethod == playback.PlayTranscode); err != nil {
			mapped := sessionStartErrorV3(err)
			return playback.DecisionResponseV3{}, *record, nil, mapped
		}
	}
	result.Plan.SessionID = session.ID
	artifactRecipe := record.FrozenRecipe
	if !seekReanchor {
		frozenRecipe, frozenErr := h.freezeExecutableRecipeV3(r.Context(), effectiveFile, result)
		if frozenErr != nil {
			return playback.DecisionResponseV3{}, *record, nil, subtitleArtifactErrorV3("Failed to freeze the selected subtitle identity.", frozenErr)
		}
		artifactRecipe = frozenRecipe
	}
	transportReused := false
	if trackChange && h.hasActiveHLSTransportV3(session) {
		if reusedRecipe, ok := sidecarOnlyHLSReplanV3(record, result.Plan, artifactRecipe, req.ClientPlaybackContext.Output.OutputContextID); ok {
			artifactRecipe = reusedRecipe
			result.ToneMapMode = reusedRecipe.ToneMapMode
			transportReused = true
		}
	}
	var transport preparedTransportV3
	if transportReused {
		// A sidecar selection changes the plan and subtitle artifact, but it does
		// not change the bytes FFmpeg produces. Keep the active HLS generation and
		// its transport window so a client remount cannot strand itself between
		// the killed old window and a replacement window that starts elsewhere.
		// The requested source position still belongs to this replan: translate it
		// onto the reused window instead of rewinding to the previous plan's start.
		result.Plan.Stream = record.CurrentPlan.Stream
		reusedTimeline := record.CurrentPlan.Timeline
		reusedTimeline.SourceStartSeconds = result.Plan.Timeline.SourceStartSeconds
		reusedTimeline.PlayerStartSeconds = max(0, reusedTimeline.SourceStartSeconds-reusedTimeline.StreamOriginSeconds)
		result.Plan.Timeline = reusedTimeline
		result.Plan.ExpiresAt = record.CurrentPlan.ExpiresAt
		transport = reusedHLSTransportV3(session, record.CurrentPlan.Stream.URL)
		slog.InfoContext(r.Context(), "protocol v3 replan reused active HLS A/V transport",
			logComponentKey, playbackLogValueV3,
			"playback_session_id", session.ID,
			"previous_plan_id", record.CurrentPlanID,
			"plan_id", result.Plan.PlanID,
		)
	} else {
		var transportErr *transportErrorV3
		transport, transportErr = h.prepareTransportV3(r, session, effectiveFile, result, mode)
		if transportErr != nil {
			return playback.DecisionResponseV3{}, *record, nil, transportErr
		}
		applyTransportToneMapModeV3(&result, transport)
		if !seekReanchor {
			frozenRecipe, frozenErr := h.freezeExecutableRecipeV3(r.Context(), effectiveFile, result)
			if frozenErr != nil {
				transport.rollback()
				return playback.DecisionResponseV3{}, *record, nil, subtitleArtifactErrorV3("Failed to freeze the selected subtitle identity.", frozenErr)
			}
			artifactRecipe = frozenRecipe
		}
	}
	result.Plan.Stream.URL = transport.url
	if err := h.attachSubtitleArtifactV3(r.Context(), session.ID, effectiveFile, result.Plan, result.SubtitleTrackIndex, &artifactRecipe); err != nil {
		transport.rollback()
		return playback.DecisionResponseV3{}, *record, nil, subtitleArtifactErrorV3("Failed to prepare the selected subtitle artifact.", err)
	}
	if seekReanchor {
		if err := validateSeekReanchorPlanV3(record, result.Plan); err != nil {
			changedFields := seekReanchorIdentityChangesV3(record, result.Plan)
			slog.ErrorContext(r.Context(), "protocol v3 seek reanchor changed route identity",
				"session", record.SessionID,
				"playback_attempt_id", record.PlaybackAttemptID,
				"changed_fields", changedFields,
			)
			transport.rollback()
			return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{
				reason:  "seek_reanchor_route_changed",
				message: err.Error(),
			}
		}
	}
	response := playback.DecisionResponseV3{ProtocolVersion: playback.ProtocolV3, ServerFeatures: playback.ServerFeaturesV3(), Outcome: playback.OutcomePlayableV3, SessionID: session.ID, PlaybackPlan: result.Plan}
	updated := *record
	updated.CurrentPlanID = result.Plan.PlanID
	updated.CurrentPlan = *result.Plan
	// A seek reanchor replays the durable recipe verbatim (updated already
	// carries it); re-freezing from live inventory could only re-introduce
	// the drift this path exists to exclude. Every other replan just accepted
	// a freshly planned route and must freeze its recipe — loudly, because a
	// recipe with a silently missing subtitle identity would disable drift
	// detection for every later seek on this attempt.
	if !seekReanchor {
		updated.FrozenRecipe = artifactRecipe
	}
	updated.NormalizedRequest = start
	updated.EffectiveMediaFileID = effectiveFile.ID
	updated.ExpiresAt = time.Now().Add(playback.MaxTokenTTL)
	if transportReused {
		if expiresAt, parseErr := time.Parse(time.RFC3339, result.Plan.ExpiresAt); parseErr == nil {
			updated.ExpiresAt = expiresAt
		}
	}
	originalRollback := transport.rollback
	replacement := playback.SessionReplacement{
		EffectiveMediaFileID: effectiveFile.ID,
		StreamState:          h.v3SessionStreamState(r.Context(), session, effectiveFile, result, transport, mode),
	}
	if seekScopedRecovery {
		replacement.PositionSeconds = &req.PositionSeconds
		replacement.PreservePaused = true
	}
	transport.applySession = func() (func() error, error) {
		rollback, err := replacementManager.ApplyReplacement(session.ID, replacement)
		if err != nil {
			return nil, err
		}
		return func() error {
			return replacementManager.RollbackReplacement(session.ID, rollback)
		}, nil
	}
	transport.afterDurableCommit = func() {
		cancelReservation()
		if trackChange {
			// A deliberate track switch is the same signal the legacy audio
			// PATCH recorded; a failure recovery is not, so its forced audio
			// route must not be written back as a user preference.
			h.persistCurrentAudioPreferenceV3(r.Context(), session.ID, session.UserID, session.ProfileID, effectiveFile, plannedAudioTrackIndexV3(result, session.AudioTrackIndex))
		}
		h.syncSessionsNow(r.Context(), "v3_replan")
		event := playback.RouteEventPlanSelectedV3
		clientModel := req.ClientPlaybackContext.Device.Model
		if seekReanchor {
			event = playback.RouteEventRuntimeCorrectionSucceededV3
			clientModel = start.ClientPlaybackContext.Device.Model
		}
		h.enqueueRouteEventV3(playback.RouteEventRecordV3{RouteEventV3: playback.RouteEventV3{ProtocolVersion: playback.ProtocolV3, PlaybackAttemptID: req.PlaybackAttemptID, SessionID: session.ID, PlanID: result.Plan.PlanID, PlanAttemptID: req.PlanAttemptID, PlanAttemptKey: playback.PlanAttemptKeyV3(*result.Plan, start.ClientPlaybackContext.Output.OutputContextID, nil), Event: event, FallbackReason: req.Failure.Classification, AppliedQuirkIDs: appliedQuirkIDsV3(result.Plan), QuirkRegistryRevision: appliedQuirkRevisionV3(result.Plan), OutputContextID: start.ClientPlaybackContext.Output.OutputContextID}, UserID: session.UserID, ProfileID: session.ProfileID, ClientName: session.ClientName, ClientVersion: session.ClientVersion, ClientBuild: session.ClientBuild, ClientChannel: session.ClientChannel, ClientModel: clientModel})
	}
	transport.rollback = func() {
		originalRollback()
		cancelReservation()
	}
	reservationHandedOff = true
	return response, updated, &transport, nil
}

// applyTransportToneMapModeV3 records an executor fallback in the result before
// the durable recipe and session state are committed.
func applyTransportToneMapModeV3(result *playback.PlannerResultV3, transport preparedTransportV3) {
	if result != nil && transport.toneMapMode != "" {
		result.ToneMapMode = transport.toneMapMode
	}
}

func frozenSeekReanchorResultV3(record *playback.AttemptRecordV3, position float64, now time.Time) (playback.PlannerResultV3, error) {
	if record == nil || !record.FrozenRecipe.ValidFor(record.CurrentPlan) {
		return playback.PlannerResultV3{}, errors.New("the active playback recipe is unavailable")
	}
	plan := record.CurrentPlan
	plan.ExpiresAt = playback.NewPlanExpiryV3(now)
	plan.Timeline = playback.TimelineV3{
		SourceStartSeconds: position,
		PlayerStartSeconds: position,
		CanSeekAnywhere:    true,
		SeekRestoration:    seekRestorationPlayerV3,
	}
	return record.FrozenRecipe.PlannerResult(&plan), nil
}

type subtitleIndexLocationV3 struct {
	source string
	offset int
}

// classifySubtitleIndexV3 maps the combined subtitle index used by
// buildSubtitleURLs to its inventory segment and segment-local offset.
func classifySubtitleIndexV3(file *models.MediaFile, index int) (subtitleIndexLocationV3, bool) {
	if file == nil || index < 0 {
		return subtitleIndexLocationV3{}, false
	}
	externalCount := len(file.ExternalSubtitles)
	if index < externalCount {
		return subtitleIndexLocationV3{source: playback.SubtitleSourceExternalV3, offset: index}, true
	}
	embeddedOffset := index - externalCount
	if embeddedOffset < len(file.SubtitleTracks) {
		return subtitleIndexLocationV3{source: playback.SubtitleSourceEmbeddedV3, offset: embeddedOffset}, true
	}
	return subtitleIndexLocationV3{
		source: playback.SubtitleSourceDownloadedV3,
		offset: embeddedOffset - len(file.SubtitleTracks),
	}, true
}

// freezeExecutableRecipeV3 extends the pure planner freeze with the identity
// of the selected sidecar subtitle. The combined subtitle index space
// (externals, then embedded, then downloaded — see buildSubtitleURLs) is not
// stable across inventory changes, so the index alone cannot anchor a durable
// selection. A downloaded selection whose identity cannot be established is
// an error: silently omitting it would disable drift detection for exactly
// the seeks this recipe exists to protect.
func (h *PlaybackHandler) freezeExecutableRecipeV3(_ context.Context, file *models.MediaFile, result playback.PlannerResultV3) (playback.ExecutableRecipeV3, error) {
	recipe := playback.FreezeExecutableRecipeV3(result)
	if file != nil {
		sourceMetadata := sourceExecutionMetadataV3(file, playback.PlannerResultV3{})
		recipe.SourceVideoCodec = sourceMetadata.VideoCodec
		recipe.SourceVideoProfile = sourceMetadata.VideoProfile
		recipe.SourceVideoBitDepth = sourceMetadata.VideoBitDepth
		recipe.SoftwareVideoDecode = sourceMetadata.SoftwareVideoDecode
		recipe.SourceDurationSeconds = sourceMetadata.DurationSeconds
	}
	if file == nil || result.SubtitleTrackIndex < 0 {
		return recipe, nil
	}
	// A downloaded row ID was selected from the planner's inventory snapshot.
	// Treat it as authoritative before consulting the mutable combined-index
	// segments: an external or embedded subtitle added after planning must not
	// make this downloaded selection look like a different source.
	if recipe.DownloadedSubtitleID > 0 {
		recipe.SubtitleSource = playback.SubtitleSourceDownloadedV3
		return recipe, nil
	}
	location, ok := classifySubtitleIndexV3(file, result.SubtitleTrackIndex)
	if !ok {
		return recipe, nil
	}
	switch location.source {
	case playback.SubtitleSourceExternalV3:
		recipe.SubtitleSource = playback.SubtitleSourceExternalV3
		recipe.ExternalSubtitlePath = file.ExternalSubtitles[location.offset].Path
	case playback.SubtitleSourceEmbeddedV3:
		recipe.SubtitleSource = playback.SubtitleSourceEmbeddedV3
		recipe.EmbeddedStreamIndex = file.SubtitleTracks[location.offset].Index
	case playback.SubtitleSourceDownloadedV3:
		if recipe.DownloadedSubtitleID <= 0 {
			return playback.ExecutableRecipeV3{}, errors.New("the selected downloaded subtitle has no stable identity")
		}
	}
	return recipe, nil
}

// validateFrozenSubtitleIdentityV3 confirms the frozen combined subtitle
// index still resolves to the identical inventory entry it was frozen
// against. It mirrors the segment layout of buildSubtitleURLs so a change in
// any earlier segment's size — which shifts every later index — is detected
// as an identity mismatch rather than silently re-resolved.
func (h *PlaybackHandler) validateFrozenSubtitleIdentityV3(ctx context.Context, file *models.MediaFile, recipe playback.ExecutableRecipeV3) error {
	if recipe.SubtitleSource == "" {
		return nil
	}
	if file == nil || recipe.SubtitleTrackIndex < 0 {
		return errors.New("the frozen subtitle selection is unavailable")
	}
	if recipe.SubtitleSource == playback.SubtitleSourceDownloadedV3 {
		if h == nil || h.SubtitleRepo == nil || recipe.DownloadedSubtitleID <= 0 {
			return errors.New("the downloaded subtitle inventory is unavailable")
		}
		downloaded, err := h.SubtitleRepo.GetDownloadedSubtitle(ctx, recipe.DownloadedSubtitleID)
		if err != nil {
			return wrapSubtitleStoreErrorV3(err)
		}
		if downloaded == nil || downloaded.MediaFileID != file.ID {
			return errors.New("the frozen downloaded subtitle identity changed")
		}
		return nil
	}
	location, ok := classifySubtitleIndexV3(file, recipe.SubtitleTrackIndex)
	if !ok || location.source != recipe.SubtitleSource {
		return errors.New("the frozen subtitle inventory segment changed")
	}
	switch recipe.SubtitleSource {
	case playback.SubtitleSourceExternalV3:
		if file.ExternalSubtitles[location.offset].Path != recipe.ExternalSubtitlePath {
			return errors.New("the frozen external subtitle identity changed")
		}
	case playback.SubtitleSourceEmbeddedV3:
		if file.SubtitleTracks[location.offset].Index != recipe.EmbeddedStreamIndex {
			return errors.New("the frozen embedded subtitle identity changed")
		}
	default:
		return errors.New("the frozen subtitle identity is unrecognized")
	}
	return nil
}

func validateSeekRecoveryRequestV3(record *playback.AttemptRecordV3, req playback.ReplanRequestV3) error {
	if record == nil {
		return errors.New("the current playback attempt is unavailable")
	}
	wantedQuality, _ := playback.NormalizeQualityV3(record.NormalizedRequest.QualityPreference)
	requestedQuality, _ := playback.NormalizeQualityV3(req.QualityPreference)
	if requestedQuality != wantedQuality {
		return errors.New("seek recovery cannot change playback quality")
	}
	if req.ClientPlaybackContext.Output.OutputContextID != record.NormalizedRequest.ClientPlaybackContext.Output.OutputContextID {
		return errors.New("seek recovery cannot change the output route")
	}
	if !sameSelectedTracksV3(req.SelectedTracks, record.CurrentPlan.SelectedTracks) {
		return errors.New("seek recovery cannot change selected tracks")
	}
	return nil
}

// seekReanchorIdentityChangesV3 returns only bounded, non-secret field names.
// It is safe for structured logs: values, URLs, headers, tokens, and subtitle
// artifact locations are deliberately excluded.
func seekReanchorIdentityChangesV3(record *playback.AttemptRecordV3, candidate *playback.PlanV3) []string {
	if record == nil || candidate == nil {
		return []string{"route"}
	}
	current := record.CurrentPlan
	changed := make([]string, 0, 16)
	add := func(name string, differs bool) {
		if differs {
			changed = append(changed, name)
		}
	}
	add("plan_id", candidate.PlanID != record.CurrentPlanID || candidate.PlanID != current.PlanID)
	add("requested_file_id", candidate.RequestedMediaFileID != record.RequestedMediaFileID)
	add("effective_file_id", candidate.EffectiveMediaFileID != record.EffectiveMediaFileID)
	add("delivery", candidate.Delivery != current.Delivery)
	add("protocol", candidate.Stream.Protocol != current.Stream.Protocol)
	add("container", candidate.Stream.Container != current.Stream.Container)
	add("mime_type", candidate.Stream.MIMEType != current.Stream.MIMEType)
	add("header_refresh", candidate.Stream.HeaderRefresh != current.Stream.HeaderRefresh)
	add("video_codec", candidate.EffectiveRecipe.VideoCodec != current.EffectiveRecipe.VideoCodec)
	add("audio_codec", candidate.EffectiveRecipe.AudioCodec != current.EffectiveRecipe.AudioCodec)
	add("resolution", !optionalIntEqualV3(candidate.EffectiveRecipe.Width, current.EffectiveRecipe.Width) || !optionalIntEqualV3(candidate.EffectiveRecipe.Height, current.EffectiveRecipe.Height))
	add("frame_rate", !optionalFloatEqualV3(candidate.EffectiveRecipe.FrameRate, current.EffectiveRecipe.FrameRate))
	add("bitrate", !optionalIntEqualV3(candidate.EffectiveRecipe.BitrateKbps, current.EffectiveRecipe.BitrateKbps))
	add("dynamic_range", candidate.EffectiveRecipe.DynamicRange != current.EffectiveRecipe.DynamicRange)
	add("audio_channels", !optionalIntEqualV3(candidate.EffectiveRecipe.AudioChannels, current.EffectiveRecipe.AudioChannels) || candidate.EffectiveRecipe.AudioLayout != current.EffectiveRecipe.AudioLayout)
	add("selected_audio", !sameTrackIdentityV3(candidate.SelectedTracks.Audio, current.SelectedTracks.Audio))
	add("selected_subtitle", !sameTrackIdentityV3(candidate.SelectedTracks.Subtitle, current.SelectedTracks.Subtitle))
	add("subtitle_mode", candidate.Subtitle.Mode != current.Subtitle.Mode || candidate.Subtitle.TrackID != current.Subtitle.TrackID)
	add("subtitle_artifact_route", !sameSubtitleArtifactRouteV3(candidate.Subtitle.Artifact, current.Subtitle.Artifact))
	add("subtitle_fidelity", candidate.SubtitleFidelityPolicy != current.SubtitleFidelityPolicy)
	add("transformations", !sameTransformationsV3(candidate.Transformations, current.Transformations))
	add("quirks", !sameAppliedQuirksV3(candidate.AppliedQuirks, current.AppliedQuirks))
	add("runtime_corrections", !sameStringMultisetV3(candidate.RuntimeCorrections, current.RuntimeCorrections))
	add("claims", candidate.Claims != current.Claims)
	return changed
}

func validateSeekReanchorPlanV3(record *playback.AttemptRecordV3, candidate *playback.PlanV3) error {
	if record == nil || candidate == nil {
		return errors.New("seek reanchor produced no playback route")
	}
	changedFields := seekReanchorIdentityChangesV3(record, candidate)
	if len(changedFields) == 0 {
		return nil
	}
	if containsStringExactV3(changedFields, "plan_id") {
		return errors.New("seek reanchor changed the playback plan identity")
	}
	if containsStringExactV3(changedFields, "requested_file_id") || containsStringExactV3(changedFields, "effective_file_id") {
		return errors.New("seek reanchor changed the selected media version")
	}
	if containsStringExactV3(changedFields, "selected_audio") || containsStringExactV3(changedFields, "selected_subtitle") {
		return errors.New("seek reanchor changed selected tracks")
	}
	return errors.New("seek reanchor changed the playback route semantics")
}

func sameSubtitleArtifactRouteV3(left, right *playback.SubtitleArtifactV3) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	// Signed URLs and timing origins are allowed to rotate when a transport is
	// reopened; the player-facing artifact representation is not.
	return left.MIMEType == right.MIMEType && left.Format == right.Format
}

func sameTransformationsV3(left, right []playback.TransformationV3) bool {
	if len(left) != len(right) {
		return false
	}
	matched := make([]bool, len(right))
	for _, candidate := range left {
		found := false
		for index, current := range right {
			if !matched[index] && candidate.Name == current.Name && candidate.Executor == current.Executor &&
				candidate.RecipeVersion == current.RecipeVersion && sameStringMultisetV3(candidate.ValidatedClaims, current.ValidatedClaims) {
				matched[index] = true
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func sameAppliedQuirksV3(left, right []playback.AppliedQuirkV3) bool {
	if len(left) != len(right) {
		return false
	}
	matched := make([]bool, len(right))
	for _, candidate := range left {
		found := false
		for index, current := range right {
			if !matched[index] && candidate == current {
				matched[index] = true
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func sameStringMultisetV3(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	counts := make(map[string]int, len(left))
	for _, value := range left {
		counts[value]++
	}
	for _, value := range right {
		counts[value]--
		if counts[value] < 0 {
			return false
		}
	}
	return true
}

// sidecarOnlyHLSReplanV3 proves that a track-change plan can keep the active
// HLS A/V generation. Subtitle identity and claims are intentionally excluded:
// those are the point of the replan and are delivered by the independently
// addressed sidecar artifact. Every field that can change FFmpeg's audio/video
// output remains part of the comparison.
func sidecarOnlyHLSReplanV3(record *playback.AttemptRecordV3, candidate *playback.PlanV3, candidateRecipe playback.ExecutableRecipeV3, outputContextID string) (playback.ExecutableRecipeV3, bool) {
	if record == nil || candidate == nil || record.CurrentPlan.Stream.URL == "" ||
		!record.FrozenRecipe.ValidFor(record.CurrentPlan) || !candidateRecipe.ValidFor(*candidate) ||
		record.NormalizedRequest.ClientPlaybackContext.Output.OutputContextID != outputContextID ||
		record.EffectiveMediaFileID != candidate.EffectiveMediaFileID ||
		record.CurrentPlan.RequestedMediaFileID != candidate.RequestedMediaFileID ||
		record.CurrentPlan.EffectiveMediaFileID != candidate.EffectiveMediaFileID ||
		!isHLSDeliveryV3(record.CurrentPlan.Delivery) || record.CurrentPlan.Delivery != candidate.Delivery ||
		record.CurrentPlan.Subtitle.Mode == playback.SubtitleBurnInV3 || candidate.Subtitle.Mode == playback.SubtitleBurnInV3 ||
		!sameTrackIdentityV3(record.CurrentPlan.SelectedTracks.Audio, candidate.SelectedTracks.Audio) {
		return candidateRecipe, false
	}
	// The planner prefers hardware whenever both executors are currently
	// available, but the active transport may have fallen back to software after
	// its plan was accepted. A sidecar-only replan must compare against and carry
	// that effective mode or it will replace byte-identical running output merely
	// because planning preferred hardware again.
	effectiveCandidateRecipe := candidateRecipe
	if effectiveMode := record.FrozenRecipe.ToneMapMode; effectiveMode != "" && candidateRecipe.ToneMapPolicy.Allows(effectiveMode) {
		effectiveCandidateRecipe.ToneMapMode = effectiveMode
	}
	if record.CurrentPlan.Stream.Protocol != candidate.Stream.Protocol ||
		record.CurrentPlan.Stream.Container != candidate.Stream.Container ||
		record.CurrentPlan.Stream.MIMEType != candidate.Stream.MIMEType ||
		record.CurrentPlan.Stream.HeaderRefresh != candidate.Stream.HeaderRefresh ||
		!sameExecutableAVRecipeV3(record.FrozenRecipe, effectiveCandidateRecipe) ||
		!sameEffectiveAVRecipeV3(record.CurrentPlan.EffectiveRecipe, candidate.EffectiveRecipe) ||
		record.CurrentPlan.Claims.Video != candidate.Claims.Video ||
		record.CurrentPlan.Claims.Audio != candidate.Claims.Audio ||
		!sameTransformationsV3(record.CurrentPlan.Transformations, candidate.Transformations) ||
		!sameAppliedQuirksV3(record.CurrentPlan.AppliedQuirks, candidate.AppliedQuirks) ||
		!sameStringMultisetV3(record.CurrentPlan.RuntimeCorrections, candidate.RuntimeCorrections) {
		return candidateRecipe, false
	}
	return effectiveCandidateRecipe, true
}

func isHLSDeliveryV3(delivery playback.DeliveryV3) bool {
	return delivery == playback.DeliveryRemuxHLSV3 || delivery == playback.DeliveryTranscodeHLSV3
}

// sameExecutableAVRecipeV3 reports whether two frozen A/V recipes are equivalent.
func sameExecutableAVRecipeV3(left, right playback.ExecutableRecipeV3) bool {
	return left.PlayMethod == right.PlayMethod &&
		left.TranscodeAudio == right.TranscodeAudio &&
		left.TargetVideoCodec == right.TargetVideoCodec &&
		left.TargetAudioCodec == right.TargetAudioCodec &&
		left.SourceAudioChannels == right.SourceAudioChannels &&
		left.TargetAudioChannels == right.TargetAudioChannels &&
		left.TargetAudioBitrateKbps == right.TargetAudioBitrateKbps &&
		left.TargetResolution == right.TargetResolution &&
		left.TargetBitrateKbps == right.TargetBitrateKbps &&
		left.SourceVideoCodec == right.SourceVideoCodec &&
		left.SourceVideoProfile == right.SourceVideoProfile &&
		left.SourceVideoBitDepth == right.SourceVideoBitDepth &&
		left.SoftwareVideoDecode == right.SoftwareVideoDecode &&
		left.SourceDurationSeconds == right.SourceDurationSeconds &&
		left.ToneMapPolicy == right.ToneMapPolicy &&
		left.ToneMapMode == right.ToneMapMode &&
		left.ToneMapSourceKind == right.ToneMapSourceKind &&
		left.ToneMapRecipeVersion == right.ToneMapRecipeVersion &&
		left.ToneMapPreflightRequired == right.ToneMapPreflightRequired &&
		left.ToneMapSourceRevision == right.ToneMapSourceRevision &&
		left.ToneMapDVConfigPresent == right.ToneMapDVConfigPresent &&
		left.ToneMapDVBLCompatIDPresent == right.ToneMapDVBLCompatIDPresent &&
		left.ToneMapDVBLPresent == right.ToneMapDVBLPresent &&
		left.ToneMapDVRPUPresent == right.ToneMapDVRPUPresent
}

func sameEffectiveAVRecipeV3(left, right playback.EffectiveRecipeV3) bool {
	return left.VideoCodec == right.VideoCodec && left.AudioCodec == right.AudioCodec &&
		optionalIntEqualV3(left.Width, right.Width) && optionalIntEqualV3(left.Height, right.Height) &&
		optionalFloatEqualV3(left.FrameRate, right.FrameRate) &&
		optionalIntEqualV3(left.BitrateKbps, right.BitrateKbps) &&
		left.DynamicRange == right.DynamicRange &&
		optionalIntEqualV3(left.AudioChannels, right.AudioChannels) && left.AudioLayout == right.AudioLayout
}

// reusedHLSTransportV3 reconstructs transport facts for an existing HLS session.
func reusedHLSTransportV3(session *playback.Session, streamURL string) preparedTransportV3 {
	transport := preparedTransportV3{url: streamURL}
	if session != nil {
		transport.nodeURL = session.TranscodeNodeURL
		transport.transportID = session.TranscodeTransportID
		transport.hwAccel = session.TranscodeHWAccel
		transport.toneMapMode = session.ToneMapMode
	}
	transport.commit = func() {}
	transport.rollback = func() {}
	return transport
}

func (h *PlaybackHandler) hasActiveHLSTransportV3(session *playback.Session) bool {
	if h == nil || session == nil {
		return false
	}
	if session.TranscodeNodeURL != "" {
		return true
	}
	return h.tm.GetTranscodeSession(session.ID) != nil
}

func applySelectedTracksToStartV3(start *playback.StartRequestV3, selected playback.SelectedTracksV3) {
	if start == nil {
		return
	}
	if selected.Audio != nil {
		start.AudioTrackID = selected.Audio.ID
		start.AudioTrackIndex = copyOptionalIntV3(selected.Audio.Index)
	}
	if selected.Subtitle != nil {
		start.SubtitleTrackID = selected.Subtitle.ID
		start.SubtitleTrackIndex = copyOptionalIntV3(selected.Subtitle.Index)
	} else {
		start.SubtitleTrackID = ""
		start.SubtitleTrackIndex = nil
	}
}

// applySelectedTrackOverridesToStartV3 overlays only identities the caller
// actually sent. Replan bodies are intentionally sparse for every operation
// except track_change, so an omitted subtitle here means "unchanged", not
// "off". The exact-replacement helper above remains the authority for an
// explicit track_change and for reconstructing a durable plan selection.
func applySelectedTrackOverridesToStartV3(start *playback.StartRequestV3, selected playback.SelectedTracksV3) {
	if start == nil {
		return
	}
	if selected.Audio != nil {
		start.AudioTrackID = selected.Audio.ID
		start.AudioTrackIndex = copyOptionalIntV3(selected.Audio.Index)
	}
	if selected.Subtitle != nil {
		start.SubtitleTrackID = selected.Subtitle.ID
		start.SubtitleTrackIndex = copyOptionalIntV3(selected.Subtitle.Index)
	}
}

// audioSelectionDiffersFromStartV3 reports whether the replan's audio
// selection names a track other than the start request's. An omitted audio
// identity means "unchanged" — clients may not resend the current track.
func audioSelectionDiffersFromStartV3(selected playback.SelectedTracksV3, start playback.StartRequestV3) bool {
	return selected.Audio != nil &&
		(selected.Audio.ID != start.AudioTrackID || !optionalIntEqualV3(selected.Audio.Index, start.AudioTrackIndex))
}

// subtitleSelectionDiffersFromStartV3 reports whether the replan's subtitle
// selection differs from the start request's. Unlike audio, a nil subtitle is
// an explicit "subtitles off" and counts as a change when one was selected.
func subtitleSelectionDiffersFromStartV3(selected playback.SelectedTracksV3, start playback.StartRequestV3) bool {
	if selected.Subtitle == nil {
		return start.SubtitleTrackIndex != nil
	}
	return selected.Subtitle.ID != start.SubtitleTrackID || !optionalIntEqualV3(selected.Subtitle.Index, start.SubtitleTrackIndex)
}

func sameSelectedTracksV3(left, right playback.SelectedTracksV3) bool {
	return sameTrackIdentityV3(left.Audio, right.Audio) && sameTrackIdentityV3(left.Subtitle, right.Subtitle)
}

func sameTrackIdentityV3(left, right *playback.TrackIdentityV3) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.ID == right.ID && optionalIntEqualV3(left.Index, right.Index)
}

func copyOptionalIntV3(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func shouldTryAlternateFileV3(qualityPreference string) bool {
	return !strings.EqualFold(strings.TrimSpace(qualityPreference), "original")
}

const (
	terminalNoAlternateVersionV3            = "no_alternate_version"
	terminalHDRTranscodeUnsupportedV3       = playback.TerminalHDRTranscodeUnsupportedV3
	terminalSubtitleConversionUnsupportedV3 = "subtitle_conversion_unsupported"
	terminalSubtitleUnavailableInVersionV3  = "subtitle_unavailable_in_version"
)

// terminalAllowsAlternateFileV3 reports whether a refusal is the kind another
// version of the same item could satisfy.
//
// subtitle_conversion_unsupported belongs here because it is not only a
// subtitle-format refusal: when a burn-in requirement is the sole trigger of an
// adaptation the source cannot take, the planner reports the blocker in terms
// of the subtitle rather than the HDR pipeline or the 4K policy. Those cases
// used to surface as hdr_transcode_unsupported / no_alternate_version and were
// the exact reason this gate exists — a bitmap subtitle needing burn-in that an
// HDR source cannot support while an SDR alternate can. Leaving the new reason
// out silently retired that fallback and refused playback outright.
func terminalAllowsAlternateFileV3(terminal *playback.TerminalV3) bool {
	if terminal == nil {
		return false
	}
	switch terminal.Reason {
	case terminalNoAlternateVersionV3, terminalHDRTranscodeUnsupportedV3, terminalSubtitleConversionUnsupportedV3:
		return true
	default:
		return false
	}
}

func replanAllowsAlternateFileV3(operation playback.ReplanOperationV3, qualityPreference string) bool {
	switch operation {
	case playback.ReplanOperationFailureRecoveryV3, playback.ReplanOperationQualityChangeV3, playback.ReplanOperationOutputChangeV3, playback.ReplanOperationTrackChangeV3:
		// Quality, output, and track changes can make another version the only
		// viable route. In particular, a bitmap subtitle can require video burn-in
		// that an HDR source cannot support while an SDR alternate can. The
		// subtitle identity is remapped before the alternate is adopted; seek-only
		// operations remain pinned to the mounted source.
		return shouldTryAlternateFileV3(qualityPreference)
	default:
		return false
	}
}

func replanAlternateFilePinnedByOriginalQualityV3(operation playback.ReplanOperationV3, qualityPreference string) bool {
	if shouldTryAlternateFileV3(qualityPreference) {
		return false
	}
	return operation == playback.ReplanOperationFailureRecoveryV3 || operation == playback.ReplanOperationQualityChangeV3 || operation == playback.ReplanOperationOutputChangeV3
}

func (h *PlaybackHandler) clarifyOriginalQuality4KTerminalV3(ctx context.Context, terminal *playback.TerminalV3, requestedFile *models.MediaFile, alternateFilePinned bool) {
	if !alternateFilePinned || terminal == nil || terminal.Reason != terminalNoAlternateVersionV3 || terminal.Message != playback.TerminalMessage4KTranscodeDisabledV3 {
		return
	}
	if alternate, err := h.findAlternateFile(ctx, requestedFile); err == nil && alternate != nil && !playback.Is4KMediaFileV3(alternate) {
		terminal.Message = "4K transcoding is disabled and quality 'original' pins the 4K version; a compatible lower-resolution version of this title is available."
	}
}

func (h *PlaybackHandler) lockReplanV3(sessionID string) func() {
	h.v3ReplanMu.Lock()
	if h.v3ReplanLocks == nil {
		h.v3ReplanLocks = make(map[string]*v3ReplanLock)
	}
	entry := h.v3ReplanLocks[sessionID]
	if entry == nil {
		entry = &v3ReplanLock{}
		h.v3ReplanLocks[sessionID] = entry
	}
	entry.refs++
	h.v3ReplanMu.Unlock()
	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		h.v3ReplanMu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(h.v3ReplanLocks, sessionID)
		}
		h.v3ReplanMu.Unlock()
	}
}

// maxConcurrentReplansV3 bounds simultaneous replan executions. Each replan
// pins one pooled DB connection for its advisory session lock while issuing
// further store queries from the same pool; without a bound, a recovery storm
// (a transcode node dying with dozens of active sessions) turns every pool
// connection into a lock holder and the inner queries deadlock against them.
const maxConcurrentReplansV3 = 8

// sessionLockCapacityAdvisorV3 lets a plan store cap replan concurrency below
// the fixed default when its own connection budget is smaller; a pool sized at
// or below the default would otherwise let lock holders starve the inner
// store queries that must complete before any lock is released.
type sessionLockCapacityAdvisorV3 interface {
	SessionLockCapacity() int
}

// acquireReplanSlotV3 blocks until a replan slot frees or the request context
// is cancelled; excess replans queue here holding no DB resources at all.
func (h *PlaybackHandler) acquireReplanSlotV3(ctx context.Context) (func(), error) {
	h.v3ReplanSlotsOnce.Do(func() {
		capacity := maxConcurrentReplansV3
		if advisor, ok := h.PlanStoreV3.(sessionLockCapacityAdvisorV3); ok {
			if advised := advisor.SessionLockCapacity(); advised > 0 && advised < capacity {
				capacity = advised
			}
		}
		h.v3ReplanSlots = make(chan struct{}, capacity)
	})
	select {
	case h.v3ReplanSlots <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-h.v3ReplanSlots }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (h *PlaybackHandler) HandlePlaybackRouteEventV3(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	if userID == 0 || profileID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication and profile are required")
		return
	}
	body, err := readBoundedV3Body(w, r, maxPlaybackV3EventBodyBytes)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid event body")
		return
	}
	var event playback.RouteEventV3
	if err := json.Unmarshal(body, &event); err != nil || !validRouteEventV3(event) {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid route event")
		return
	}
	// The rate limiter runs before the ownership lookup so the per-minute
	// budget bounds the store reads as well as the writes.
	if !h.allowRouteEventV3(userID, event.PlaybackAttemptID) {
		writeError(w, http.StatusTooManyRequests, "event_rate_limited", "Playback route event rate exceeded")
		return
	}
	var identity *playback.AttemptIdentityV3
	var identityErr error
	if event.SessionID != "" {
		identity, identityErr = h.PlanStoreV3.GetAttemptIdentity(r.Context(), event.SessionID)
	} else {
		identity, identityErr = h.PlanStoreV3.GetAttemptIdentityByPlaybackAttemptID(r.Context(), event.PlaybackAttemptID)
	}
	if identityErr != nil {
		// A store outage is not an ownership violation; keep 403 for genuine
		// mismatches so clients stop sending events for foreign sessions.
		if !errors.Is(identityErr, playback.ErrSessionNotFound) {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to authorize the route event")
			return
		}
		writeError(w, http.StatusForbidden, "forbidden", "Route event does not belong to this profile")
		return
	}
	if identity.UserID != userID || identity.ProfileID != profileID ||
		(event.SessionID != "" && identity.PlaybackAttemptID != event.PlaybackAttemptID) ||
		(identity.SessionID == "" && !terminalStartRouteEventV3(event)) {
		writeError(w, http.StatusForbidden, "forbidden", "Route event does not belong to this profile")
		return
	}
	event.Diagnostics = sanitizeDiagnosticsV3(event.Diagnostics)
	client := h.playbackClientInfoWithSessionFallbackV3(firstNonEmptyValue(event.SessionID, identity.SessionID), playback.ClientInfoFromRequest(r))
	h.enqueueRouteEventV3(playback.RouteEventRecordV3{RouteEventV3: event, UserID: userID, ProfileID: profileID, ClientName: client.Name, ClientVersion: client.Version, ClientBuild: client.Build, ClientChannel: client.Channel, ClientModel: event.Diagnostics["device_model"]})
	w.WriteHeader(http.StatusAccepted)
}

func terminalStartRouteEventV3(event playback.RouteEventV3) bool {
	return event.Event == playback.RouteEventTerminalV3 &&
		event.SessionID == "" && event.PlanID == "" &&
		event.PlanAttemptID == "" && event.PlanAttemptKey == ""
}

// StartV3Maintenance expires cached signed responses and old telemetry on the
// application lifecycle rather than on latency-sensitive playback requests.
func (h *PlaybackHandler) StartV3Maintenance(ctx context.Context) {
	if h == nil || h.PlanStoreV3 == nil || ctx == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				if _, err := h.PlanStoreV3.CleanupExpired(cleanupCtx, now); err != nil {
					slog.Warn("playback v3 cleanup failed", "error", err)
				}
				cancel()
			}
		}
	}()
}

func (h *PlaybackHandler) allowRouteEventV3(userID int, attemptID string) bool {
	attemptKey := fmt.Sprintf("attempt:%d:%s", userID, attemptID)
	userKey := fmt.Sprintf("user:%d", userID)
	now := time.Now()
	h.v3EventRateMu.Lock()
	defer h.v3EventRateMu.Unlock()
	if h.v3EventRates == nil {
		h.v3EventRates = make(map[string]v3EventRate)
	}
	attemptEntry := h.v3EventRates[attemptKey]
	if attemptEntry.windowStart.IsZero() || now.Sub(attemptEntry.windowStart) >= time.Minute {
		attemptEntry = v3EventRate{windowStart: now}
	}
	userEntry := h.v3EventRates[userKey]
	if userEntry.windowStart.IsZero() || now.Sub(userEntry.windowStart) >= time.Minute {
		userEntry = v3EventRate{windowStart: now}
	}
	if attemptEntry.count >= 120 || userEntry.count >= 600 {
		return false
	}
	attemptEntry.count++
	userEntry.count++
	h.v3EventRates[attemptKey] = attemptEntry
	h.v3EventRates[userKey] = userEntry
	if len(h.v3EventRates) > 10_000 {
		for candidate, value := range h.v3EventRates {
			if now.Sub(value.windowStart) > 2*time.Minute {
				delete(h.v3EventRates, candidate)
			}
		}
	}
	return true
}

func (h *PlaybackHandler) enqueueRouteEventV3(event playback.RouteEventRecordV3) {
	if h == nil || h.PlanStoreV3 == nil {
		return
	}
	h.v3EventOnce.Do(func() {
		h.v3EventQueue = make(chan playback.RouteEventRecordV3, 512)
		go func() {
			for value := range h.v3EventQueue {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				if err := h.PlanStoreV3.RecordRouteEvent(ctx, value); err != nil {
					slog.Warn("playback route event write failed", "error", err, "event", value.Event)
				}
				cancel()
			}
		}()
	})
	select {
	case h.v3EventQueue <- event:
	default:
		slog.Warn("playback route event dropped", "event", event.Event, "playback_attempt_id", event.PlaybackAttemptID)
	}
}

// plannerSettingsV3 reads the live settings used by capability discovery,
// where failures intentionally degrade to an omitted capability in a 200.
func (h *PlaybackHandler) plannerSettingsV3(ctx context.Context) playback.PlannerSettingsV3 {
	settings, _ := h.plannerSettingsV3Result(ctx)
	return settings
}

// plannerSettingsV3Result reads the live settings used for an actual planning
// decision. Callers must not persist a policy terminal when the store is down.
func (h *PlaybackHandler) plannerSettingsV3Result(ctx context.Context) (playback.PlannerSettingsV3, error) {
	settings := playback.PlannerSettingsV3{TranscodeEnabled: h.playbackConfig().TranscodeEnabled}
	if h.SettingsRepo != nil {
		var values [3]string
		var errs [3]error
		keys := [...]string{
			config.Allow4KTranscodeSettingKey,
			config.PlaybackTranscodeHardwareToneMapSettingKey,
			config.PlaybackTranscodeSoftwareToneMapSettingKey,
		}
		var group sync.WaitGroup
		group.Add(len(keys))
		for index, key := range keys {
			go func() {
				defer group.Done()
				values[index], errs[index] = h.SettingsRepo.Get(ctx, key)
			}()
		}
		group.Wait()
		if errs[0] != nil {
			return settings, fmt.Errorf("load 4K transcode setting: %w", errs[0])
		}
		if errs[1] != nil {
			return settings, fmt.Errorf("load hardware tone-map setting: %w", errs[1])
		}
		if errs[2] != nil {
			return settings, fmt.Errorf("load software tone-map setting: %w", errs[2])
		}
		settings.Allow4KTranscode = strings.EqualFold(values[0], "true")
		settings.HardwareToneMapEnabled = strings.EqualFold(values[1], "true")
		settings.SoftwareToneMapEnabled = strings.EqualFold(values[2], "true")
	}
	return settings, nil
}

func resolveV3AudioIndex(file *models.MediaFile, trackID string, fallback *int) (int, error) {
	index := 0
	if trackID != "" {
		fileID, kind, ordinal, ok := playback.ParseTrackIDV3(trackID)
		if !ok || kind != "audio" || file == nil || fileID != file.ID {
			return 0, errors.New("selected audio track identity is invalid")
		}
		index = ordinal
	} else if fallback != nil {
		index = *fallback
	}
	if file == nil || len(file.AudioTracks) == 0 {
		if index == 0 {
			return 0, nil
		}
		return 0, errors.New("selected audio track is unavailable")
	}
	if index < 0 || index >= len(file.AudioTracks) {
		return 0, errors.New("selected audio track is unavailable")
	}
	return index, nil
}

func remapAudioIndexV3(source, target *models.MediaFile, index int) int {
	if source == nil || target == nil || index < 0 || index >= len(source.AudioTracks) {
		return normalizeAudioTrackIndex(target, index)
	}
	wanted := source.AudioTracks[index]
	for i, candidate := range target.AudioTracks {
		if strings.EqualFold(candidate.Codec, wanted.Codec) && strings.EqualFold(candidate.Language, wanted.Language) && candidate.Channels == wanted.Channels {
			return i
		}
	}
	return normalizeAudioTrackIndex(target, index)
}

// remapAudioSelectionV3 rebinds the request's audio selection when the
// effective media file changes. ID-only selections are equally file-bound:
// the stale ID would be rejected against the new file's track list
// downstream, so derive the source index from it and remap like any other.
func remapAudioSelectionV3(source, target *models.MediaFile, request *playback.StartRequestV3) error {
	if request == nil || source == nil || target == nil || source.ID == target.ID {
		return nil
	}
	if request.AudioTrackIndex == nil {
		if request.AudioTrackID == "" {
			return nil
		}
		fileID, kind, ordinal, ok := playback.ParseTrackIDV3(request.AudioTrackID)
		if !ok || kind != "audio" || fileID != source.ID {
			return errors.New("The selected audio track identity is invalid for the source file.")
		}
		request.AudioTrackIndex = &ordinal
	}
	remapped := remapAudioIndexV3(source, target, *request.AudioTrackIndex)
	request.AudioTrackIndex = &remapped
	request.AudioTrackID = playback.TrackIDV3(target.ID, "audio", remapped)
	return nil
}

func (h *PlaybackHandler) remapSubtitleSelectionV3(ctx context.Context, source, target *models.MediaFile, request *playback.StartRequestV3) error {
	if request == nil || source == nil || target == nil || source.ID == target.ID {
		return nil
	}
	if request.SubtitleTrackIndex == nil {
		// ID-only selections are equally file-bound: the stale ID would be
		// parsed against the alternate file's track list downstream, so
		// derive the source index from it and remap like any other.
		if request.SubtitleTrackID == "" {
			return nil
		}
		fileID, kind, ordinal, ok := playback.ParseTrackIDV3(request.SubtitleTrackID)
		if !ok || kind != "subtitle" || fileID != source.ID {
			return errors.New("The selected subtitle track identity is invalid for the source file.")
		}
		request.SubtitleTrackIndex = &ordinal
	}
	index := *request.SubtitleTrackIndex
	if index < 0 {
		return errors.New("The selected subtitle track index is invalid.")
	}
	targetIndex := -1
	switch {
	case index < len(source.ExternalSubtitles):
		wanted := source.ExternalSubtitles[index]
		for candidateIndex, candidate := range target.ExternalSubtitles {
			if strings.EqualFold(candidate.Language, wanted.Language) && strings.EqualFold(candidate.Format, wanted.Format) && candidate.Forced == wanted.Forced {
				targetIndex = candidateIndex
				break
			}
		}
	case index < len(source.ExternalSubtitles)+len(source.SubtitleTracks):
		wanted := source.SubtitleTracks[index-len(source.ExternalSubtitles)]
		for candidateIndex, candidate := range target.SubtitleTracks {
			if strings.EqualFold(candidate.Language, wanted.Language) && strings.EqualFold(candidate.Codec, wanted.Codec) && candidate.Forced == wanted.Forced {
				targetIndex = len(target.ExternalSubtitles) + candidateIndex
				break
			}
		}
	default:
		if h.SubtitleRepo != nil {
			sourceDownloaded, sourceErr := h.SubtitleRepo.ListDownloadedSubtitles(ctx, source.ID)
			targetDownloaded, targetErr := h.SubtitleRepo.ListDownloadedSubtitles(ctx, target.ID)
			downloadedIndex := index - len(source.ExternalSubtitles) - len(source.SubtitleTracks)
			if sourceErr == nil && targetErr == nil && downloadedIndex >= 0 && downloadedIndex < len(sourceDownloaded) {
				wanted := sourceDownloaded[downloadedIndex]
				for candidateIndex, candidate := range targetDownloaded {
					if strings.EqualFold(candidate.Language, wanted.Language) && strings.EqualFold(string(candidate.Format), string(wanted.Format)) && strings.EqualFold(candidate.ReleaseName, wanted.ReleaseName) {
						targetIndex = len(target.ExternalSubtitles) + len(target.SubtitleTracks) + candidateIndex
						break
					}
				}
			}
		}
	}
	if targetIndex < 0 {
		return errors.New("The selected subtitle track is unavailable in the effective file version.")
	}
	request.SubtitleTrackIndex = &targetIndex
	request.SubtitleTrackID = playback.TrackIDV3(target.ID, "subtitle", targetIndex)
	return nil
}

func sessionStartErrorV3(err error) *transportErrorV3 {
	switch {
	case errors.Is(err, playback.ErrTooManyStreams), errors.Is(err, playback.ErrTooManyTranscodes):
		return &transportErrorV3{reason: "capacity_unavailable", message: "Playback capacity is currently unavailable.", retryable: true}
	case errors.Is(err, playback.ErrTranscodingDisabled), errors.Is(err, playback.ErrAudioTranscodingDisabled):
		return &transportErrorV3{reason: "transcoding_disabled", message: "The selected server adaptation is disabled."}
	case errors.Is(err, playback.ErrPlaybackNotAllowed):
		return &transportErrorV3{reason: "policy_denied", message: "Playback is denied by server policy."}
	default:
		return &transportErrorV3{reason: "internal_error", message: "Failed to start the playback session.", cause: err}
	}
}

func (h *PlaybackHandler) persistTerminalStartDecisionV3(ctx context.Context, userID int, profileID string, req playback.StartRequestV3, requestDigests playbackStartRequestDigestsV3, requestedFileID, effectiveFileID int, response playback.DecisionResponseV3) (playback.DecisionResponseV3, error) {
	record := playback.AttemptRecordV3{
		PlaybackAttemptID:    req.PlaybackAttemptID,
		UserID:               userID,
		ProfileID:            profileID,
		RequestedMediaFileID: requestedFileID,
		EffectiveMediaFileID: effectiveFileID,
		NormalizedRequest:    req,
		StartResponse:        response,
		RequestDigest:        requestDigests.current,
		ExpiresAt:            time.Now().Add(playback.MaxTokenTTL),
	}
	if err := h.PlanStoreV3.SaveAttempt(ctx, record); err == nil {
		return response, nil
	} else if !errors.Is(err, playback.ErrPlaybackAttemptExistsV3) && !errors.Is(err, playback.ErrIdempotencyKeyReusedV3) {
		return playback.DecisionResponseV3{}, err
	}

	existing, err := h.PlanStoreV3.GetAttemptByPlaybackAttemptID(ctx, req.PlaybackAttemptID)
	if err != nil {
		return playback.DecisionResponseV3{}, err
	}
	if existing.UserID != userID || existing.ProfileID != profileID ||
		existing.RequestedMediaFileID != requestedFileID || !requestDigests.matches(existing.RequestDigest) {
		return playback.DecisionResponseV3{}, playback.ErrIdempotencyKeyReusedV3
	}
	return decisionResponseFromAttemptV3(existing), nil
}

func (h *PlaybackHandler) startFailureDecisionV3(ctx context.Context, userID int, profileID string, req playback.StartRequestV3, requestDigests playbackStartRequestDigestsV3, requestedFileID, effectiveFileID int, failure *transportErrorV3) (playback.DecisionResponseV3, error) {
	response := playback.NewTerminalResponseV3(failure.reason, failure.message, failure.retryable)
	return h.persistTerminalStartDecisionV3(ctx, userID, profileID, req, requestDigests, requestedFileID, effectiveFileID, response)
}

func writeStartAttemptPersistenceErrorV3(w http.ResponseWriter, err error) {
	if errors.Is(err, playback.ErrIdempotencyKeyReusedV3) {
		writeError(w, http.StatusConflict, "playback_attempt_reused", "The playback attempt ID belongs to a different request")
		return
	}
	writeError(w, http.StatusInternalServerError, "internal_error", "Failed to persist the playback decision")
}

func decisionResponseFromAttemptV3(record *playback.AttemptRecordV3) playback.DecisionResponseV3 {
	if record == nil {
		return playback.DecisionResponseV3{}
	}
	if record.StartResponse.Outcome != "" || record.StartResponse.Terminal != nil || record.StartResponse.PlaybackPlan != nil {
		return normalizeDecisionResponseV3(record.StartResponse)
	}
	plan := record.CurrentPlan
	if plan.AppliedQuirks == nil {
		plan.AppliedQuirks = []playback.AppliedQuirkV3{}
	}
	if plan.RuntimeCorrections == nil {
		plan.RuntimeCorrections = []string{}
	}
	return normalizeDecisionResponseV3(playback.DecisionResponseV3{ProtocolVersion: playback.ProtocolV3, ServerFeatures: playback.ServerFeaturesV3(), Outcome: playback.OutcomePlayableV3, SessionID: record.SessionID, PlaybackPlan: &plan})
}

func normalizeDecisionResponseV3(response playback.DecisionResponseV3) playback.DecisionResponseV3 {
	if response.ServerFeatures == nil {
		response.ServerFeatures = playback.ServerFeaturesV3()
	}
	if response.PlaybackPlan == nil {
		return response
	}
	plan := response.PlaybackPlan
	if plan.Stream.Headers == nil {
		plan.Stream.Headers = map[string]string{}
	}
	if plan.Transformations == nil {
		plan.Transformations = []playback.TransformationV3{}
	}
	if plan.AppliedQuirks == nil {
		plan.AppliedQuirks = []playback.AppliedQuirkV3{}
	}
	if plan.RuntimeCorrections == nil {
		plan.RuntimeCorrections = []string{}
	}
	if plan.AvailableQualities == nil {
		plan.AvailableQualities = []playback.AvailableQualityV3{}
	}
	if plan.DegradationWarnings == nil {
		plan.DegradationWarnings = []playback.DegradationWarningV3{}
	}
	if plan.Subtitle.Inventory == nil {
		plan.Subtitle.Inventory = []playback.SubtitleInventoryItemV3{}
	}
	return response
}

func completedReplanResponseMatchesAttemptV3(raw json.RawMessage, record *playback.AttemptRecordV3) bool {
	if record == nil {
		return false
	}
	var response playback.DecisionResponseV3
	if len(raw) == 0 || json.Unmarshal(raw, &response) != nil {
		return false
	}
	if response.PlaybackPlan == nil {
		// Terminal responses deliberately leave the attempt plan untouched. Their
		// freshness is carried by CurrentReplanRequestID (and its DB trigger).
		return response.Terminal != nil
	}
	if response.SessionID != record.SessionID || response.PlaybackPlan.SessionID != record.SessionID {
		return false
	}
	candidate, candidateErr := json.Marshal(response.PlaybackPlan)
	current, currentErr := json.Marshal(record.CurrentPlan)
	return candidateErr == nil && currentErr == nil && bytes.Equal(candidate, current)
}

func appliedQuirkIDsV3(plan *playback.PlanV3) []string {
	if plan == nil {
		return nil
	}
	result := make([]string, 0, len(plan.AppliedQuirks))
	for _, quirk := range plan.AppliedQuirks {
		result = append(result, quirk.ID)
	}
	return result
}

func appliedQuirkRevisionV3(plan *playback.PlanV3) string {
	if plan == nil || len(plan.AppliedQuirks) == 0 {
		return ""
	}
	return plan.AppliedQuirks[0].RegistryRevision
}

func writeV3FileError(w http.ResponseWriter, err error) {
	if errors.Is(err, catalog.ErrItemNotFound) || errors.Is(err, catalog.ErrEpisodeNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "Media file not found")
		return
	}
	writeError(w, http.StatusInternalServerError, "internal_error", "Failed to authorize media file")
}
func readBoundedV3Body(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	return ioReadAllV3(http.MaxBytesReader(w, r.Body, limit))
}
func ioReadAllV3(reader interface{ Read([]byte) (int, error) }) ([]byte, error) {
	var buffer bytes.Buffer
	_, err := buffer.ReadFrom(reader)
	return buffer.Bytes(), err
}
func chiURLParamV3(r *http.Request, key string) string { return chi.URLParam(r, key) }
func floatOrZeroHandlerV3(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

func intOrZeroHandlerV3(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}
func firstNonEmptyHandlerV3(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
func subtitleMIMEV3(format string) string {
	switch strings.ToLower(format) {
	case "ass", "ssa":
		return "text/x-ssa"
	case "srt", "subrip":
		return "application/x-subrip"
	case "pgs", "hdmv_pgs_subtitle":
		return "application/octet-stream"
	default:
		return subtitleMIMEVTTV3
	}
}

func forceSubtitleExtensionV3(rawURL, extension string) string {
	pathPart, query, hasQuery := strings.Cut(rawURL, "?")
	if slash := strings.LastIndex(pathPart, "/"); slash >= 0 {
		if dot := strings.LastIndex(pathPart[slash+1:], "."); dot >= 0 {
			pathPart = pathPart[:slash+1+dot] + extension
		} else {
			pathPart += extension
		}
	}
	if hasQuery {
		return pathPart + "?" + query
	}
	return pathPart
}

func remuxDVModeForPlanV3(plan *playback.PlanV3) playback.RemuxDVMode {
	if plan == nil {
		return ""
	}
	for _, transformation := range plan.Transformations {
		if transformation.Name == playback.TransformationServerDV7HDR10V3 {
			return playback.RemuxDVStripToHDR10V3
		}
	}
	if plan.Source.DVProfile == 0 {
		return ""
	}
	if plan.Source.DVProfile == 7 {
		// Without the strip transformation a P7 remux would drop the
		// enhancement layer and leave dangling RPUs. A P7 plan claiming Dolby
		// Vision is a client-side transform of the original bytes, so any
		// remux attempt against this session must still be rejected.
		return playback.RemuxDVRejectP7V3
	}
	if plan.Claims.Video.DolbyVision {
		return playback.RemuxDVPreserveV3
	}
	return ""
}

func videoBitstreamFilterForPlanV3(plan *playback.PlanV3) string {
	if plan == nil {
		return ""
	}
	for _, transformation := range plan.Transformations {
		if transformation.Executor == playback.ExecutorServerV3 && transformation.Name == playback.TransformationServerDV7HDR10V3 && transformation.RecipeVersion == "1" {
			return playback.DV7ToHDR10BitstreamFilter
		}
	}
	return ""
}

func videoSampleEntryForPlanV3(plan *playback.PlanV3) string {
	if plan == nil || plan.Delivery != playback.DeliveryRemuxHLSV3 {
		return ""
	}
	for _, transformation := range plan.Transformations {
		if transformation.Name == playback.TransformationServerDV7HDR10V3 {
			return playback.VideoSampleEntryHVC1
		}
	}
	if plan.EffectiveRecipe.DynamicRange == playback.DynamicRangeDolbyVisionV3 &&
		(plan.Source.DVProfile == 5 || plan.Source.DVProfile == 8) {
		return playback.VideoSampleEntryDVH1
	}
	return ""
}

// lazyDVRPUStrippableV3 defers (and memoizes) the per-source RPU probe so the
// planner only shells out to ffmpeg when a Dolby Vision strip route is
// genuinely on the table; every other start never touches it.
//
// The probe belongs to planning, not to the transport: the plan's HDR10 promise
// and the durable session's RemuxDVMode are both derived from the strip
// decision and are re-read by the restart and audio-switch paths, so
// suppressing the filter downstream would leave those claims describing a
// stream the server is no longer producing.
func (h *PlaybackHandler) lazyDVRPUStrippableV3(ctx context.Context, file *models.MediaFile) func() bool {
	if file == nil || strings.TrimSpace(file.FilePath) == "" {
		return nil
	}
	var once sync.Once
	strippable := true
	return func() bool {
		once.Do(func() {
			strippable = playback.DVRPUStrippable(ctx, h.playbackConfig().FFmpegPath, file.FilePath)
		})
		return strippable
	}
}

func configureHLSTimelineV3(plan *playback.PlanV3, videoCodec string, segmentDuration int, durationSeconds float64) (float64, int) {
	if plan == nil {
		return 0, 0
	}
	requested := plan.Timeline.SourceStartSeconds
	seek := alignedSeekSeconds(requested, segmentDuration, videoCodec)
	startSegment := computeStartSegment(seek, segmentDuration)
	plan.Timeline.SourceStartSeconds = requested
	usesGrowingManifest := !playback.CanGenerateSyntheticManifest(durationSeconds, segmentDuration)
	if usesGrowingManifest {
		// Encoded streams seek to the preceding segment boundary. Preserve the
		// requested sub-segment offset so playback still begins at the exact
		// requested source position. Copy remuxes are configured separately with
		// their probed keyframe origin.
		plan.Timeline.PlayerStartSeconds = max(0, requested-seek)
		plan.Timeline.StreamOriginSeconds = seek
		plan.Timeline.TimelineOffsetSeconds = seek
		windowStart := seek
		plan.Timeline.SeekWindowStartSeconds = &windowStart
		// This transport is served from FFmpeg's live, still-growing playlist
		// (see BuildPlaybackManifest), so the seekable extent is whatever has
		// been produced so far — a value this plan cannot know and could not
		// keep current if it did. Publishing the media runtime here instead
		// made the window look *complete*, which clients read as proof that
		// any target inside it is locally seekable; they then native-seek past
		// the produced head instead of asking for a reanchor. Leaving the end
		// open marks the window incomplete, which with can_seek_anywhere=false
		// routes every seek back through the server.
		//
		// The media runtime is published on source.duration_seconds, which is
		// a fact about the file rather than a claim about this transport.
		plan.Timeline.SeekWindowEndSeconds = nil
		plan.Timeline.CanSeekAnywhere = false
		plan.Timeline.SeekRestoration = "source_position"
	} else {
		plan.Timeline.PlayerStartSeconds = requested
		plan.Timeline.StreamOriginSeconds = 0
		plan.Timeline.TimelineOffsetSeconds = 0
		plan.Timeline.SeekWindowStartSeconds = nil
		plan.Timeline.SeekWindowEndSeconds = nil
		plan.Timeline.CanSeekAnywhere = durationSeconds > 0
		plan.Timeline.SeekRestoration = seekRestorationPlayerV3
	}
	return seek, startSegment
}

var diagnosticKeysV3 = map[string]struct{}{
	"decoder_name": {}, "decoder_init_ms": {}, "first_frame_ms": {},
	"device_model": {}, "requested_quality": {}, "effective_quality": {},
	"pcm_recovery": {}, "retry_outcome": {}, "replan_request_id": {},
	"video_mime": {}, "video_codecs": {}, "video_width": {}, "video_height": {},
	"color_transfer": {}, "color_range": {},
	"error_code": {}, "error_code_name": {}, "error_cause": {},
	"transformation_name": {}, "transformation_version": {}, "transformation_stage": {},
	"input_dv_profile": {}, "output_dv_profile": {}, "rpu_converted_count": {},
	"rpu_failed_count": {}, "el_nal_dropped_count": {}, "sample_count": {},
	"transform_buffer_peak_bytes": {}, "requested_media_file_id": {}, "effective_media_file_id": {},
	"audio_output_mode": {}, "audio_mime": {}, "audio_channels": {}, "audio_decoder_name": {},
	"correction_id": {}, "correction_stage": {},
	"network_transport": {}, "network_metered": {}, "network_validated": {},
	"bandwidth_estimate_kbps": {}, "link_downstream_kbps": {},
	"target_source_position_seconds": {}, "reason": {},
}

func validRouteEventV3(event playback.RouteEventV3) bool {
	if event.ProtocolVersion != playback.ProtocolV3 || len(event.PlaybackAttemptID) < 8 || len(event.PlaybackAttemptID) > 128 || len(event.OutputContextID) > 128 || len(event.SessionID) > 128 || len(event.PlanID) > 128 || len(event.PlanAttemptID) > 128 || len(event.PlanAttemptKey) > 128 || len(event.FailureClassification) > 64 || len(event.FallbackReason) > 64 || len(event.AppliedQuirkIDs) > 16 || len(event.QuirkRegistryRevision) > 128 || len(event.Diagnostics) > 32 {
		return false
	}
	for _, id := range event.AppliedQuirkIDs {
		if len(id) == 0 || len(id) > 128 {
			return false
		}
	}
	return playback.ValidRouteEventNameV3(event.Event)
}
func sanitizeDiagnosticsV3(values map[string]string) map[string]string {
	// Iterate the approved keys, not the client map: map iteration order is
	// random, so a count-limited walk over client keys would keep an
	// arbitrary subset and drop different diagnostics on identical retries.
	result := make(map[string]string)
	for key := range diagnosticKeysV3 {
		value, ok := values[key]
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) > 256 {
			value = value[:256]
		}
		result[key] = value
	}
	return result
}

func containsStringFoldV3(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(value, wanted) {
			return true
		}
	}
	return false
}

// containsStringExactV3 compares attempt keys byte-for-byte: they are
// case-sensitive FNV hex digests, so case-folding would treat distinct keys
// as equal.
func containsStringExactV3(values []string, wanted string) bool {
	wanted = strings.TrimSpace(wanted)
	for _, value := range values {
		if strings.TrimSpace(value) == wanted {
			return true
		}
	}
	return false
}

func optionalIntEqualV3(left, right *int) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func optionalFloatEqualV3(left, right *float64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
