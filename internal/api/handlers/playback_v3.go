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
	"github.com/google/uuid"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/settingsresolve"
	"github.com/Silo-Server/silo-server/internal/subtitles"
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
	v3NodeCapabilityPlanTimeout = 3 * time.Second
)

var errSubtitleStoreUnavailableV3 = errors.New("subtitle store unavailable")

type v3NodeCapabilityCache struct {
	transformations []playback.TransformationV3
	err             error
	expiresAt       time.Time
}

type preparedTransportV3 struct {
	url                string
	nodeURL            string
	transportID        string
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

func (e *transportErrorV3) Error() string {
	if e.cause != nil {
		return e.reason + ": " + e.cause.Error()
	}
	return e.reason
}

func (h *PlaybackHandler) transformationRegistryV3(ctx context.Context) *playback.TransformationRegistryV3 {
	h.v3RegistryOnce.Do(func() {
		h.v3Registry = playback.ProbeTransformationRegistryV3(context.WithoutCancel(ctx), h.playbackConfig().FFmpegPath)
	})
	return h.v3Registry
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

func (h *PlaybackHandler) lookupRemoteTransformationsV3(ctx context.Context, nodeURL string, honorCachedFailure bool) ([]playback.TransformationV3, error) {
	now := time.Now()
	h.v3NodeCapabilitiesMu.Lock()
	entry, ok := h.v3NodeCapabilities[nodeURL]
	h.v3NodeCapabilitiesMu.Unlock()
	if ok && now.Before(entry.expiresAt) {
		if entry.err == nil {
			return append([]playback.TransformationV3(nil), entry.transformations...), nil
		}
		if honorCachedFailure {
			return nil, entry.err
		}
	}

	info, err := fetchRemoteTranscodeCapabilities(ctx, nodeURL, h.JWTSecret)
	if err != nil {
		h.v3NodeCapabilitiesMu.Lock()
		if h.v3NodeCapabilities == nil {
			h.v3NodeCapabilities = make(map[string]v3NodeCapabilityCache)
		}
		h.v3NodeCapabilities[nodeURL] = v3NodeCapabilityCache{err: err, expiresAt: now.Add(v3NodeCapabilityErrorTTL)}
		h.v3NodeCapabilitiesMu.Unlock()
		return nil, err
	}
	entry = v3NodeCapabilityCache{
		transformations: append([]playback.TransformationV3(nil), info.Transformations...),
		expiresAt:       now.Add(v3NodeCapabilityTTL),
	}
	h.v3NodeCapabilitiesMu.Lock()
	if h.v3NodeCapabilities == nil {
		h.v3NodeCapabilities = make(map[string]v3NodeCapabilityCache)
	}
	h.v3NodeCapabilities[nodeURL] = entry
	h.v3NodeCapabilitiesMu.Unlock()
	return append([]playback.TransformationV3(nil), entry.transformations...), nil
}

// transcodeNodeEnumeratorV3 exposes the pooled transcode nodes whose
// advertised transformations widen HLS planning; *nodepool.Planner implements
// it.
type transcodeNodeEnumeratorV3 interface {
	TranscodeNodeURLs() []string
}

// hlsPlanningRegistryV3 returns the registry HLS deliveries plan against: the
// local probe plus every pooled transcode node's advertised transformations.
// Only availability of locally-defined specs widens (name and recipe version
// pinned by this server), so any plan built from it passes the per-node
// advertisement validation when that node is selected, and the local-fallback
// validation in prepareTransportV3 rejects recipes only nodes can run.
// Without pooled nodes this is exactly the local registry.
func (h *PlaybackHandler) hlsPlanningRegistryV3(ctx context.Context) *playback.TransformationRegistryV3 {
	local := h.transformationRegistryV3(ctx)
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

// lazyHLSPlanningRegistryV3 defers (and memoizes) the widened-registry build
// so the planner only pays for node capability lookups when a route decision
// actually depends on them; direct-play and other source-preserving starts
// never touch the pool.
func (h *PlaybackHandler) lazyHLSPlanningRegistryV3(ctx context.Context) func() *playback.TransformationRegistryV3 {
	var once sync.Once
	var registry *playback.TransformationRegistryV3
	return func() *playback.TransformationRegistryV3 {
		once.Do(func() { registry = h.hlsPlanningRegistryV3(ctx) })
		return registry
	}
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
				slog.DebugContext(ctx, "protocol v3 node capability unavailable for planning", "component", "api", "node", nodeURL, "error", err)
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

// planNodeSessionV3 selects transcode/proxy nodes for the session. Plans that
// carry server transformations restrict selection to nodes whose advertised
// capabilities validate against the plan, so load balancing in a
// heterogeneous pool cannot land a recipe on a node that would reject it when
// a capable sibling exists. Capability-blind selection remains for
// transformation-free plans and non-enumerating planners.
func (h *PlaybackHandler) planNodeSessionV3(ctx context.Context, session *playback.Session, result playback.PlannerResultV3) nodepool.Plan {
	selector, selectable := h.NodePlanner.(capabilitySessionPlannerV3)
	enumerator, enumerable := h.NodePlanner.(transcodeNodeEnumeratorV3)
	if !selectable || !enumerable || !planRequiresServerTransformationsV3(result.Plan) {
		return h.NodePlanner.PlanSession(session.ID, session.TranscodeNodeURL, true, result.TargetBitrateKbps)
	}
	capable := make(map[string]struct{})
	for nodeURL, advertised := range h.pooledNodeTransformationsV3(ctx, enumerator.TranscodeNodeURLs()) {
		if validateAdvertisedTransformationsV3(result.Plan, advertised) == nil {
			capable[nodeURL] = struct{}{}
		}
	}
	// The predicate runs under the planner lock: a set lookup only.
	return selector.PlanSessionWith(session.ID, session.TranscodeNodeURL, true, result.TargetBitrateKbps, func(node *nodepool.Node) bool {
		if node == nil {
			return false
		}
		_, ok := capable[node.URL]
		return ok
	})
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
	response.Transformations = h.transformationRegistryV3(r.Context()).Advertised()
	writeJSON(w, http.StatusOK, response)
}

func (h *PlaybackHandler) handleStartPlaybackV3(w http.ResponseWriter, r *http.Request, body []byte) {
	var req playback.StartRequestV3
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid protocol v3 request body")
		return
	}
	warnings, err := req.NormalizeAndValidate()
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
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
	requestedFile, err := h.loadAuthorizedFile(r, req.FileID)
	if err != nil {
		writeV3FileError(w, err)
		return
	}
	requestedFile = h.ensurePlaybackProbe(r.Context(), requestedFile)
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
	effectiveFile := requestedFile
	settings := h.plannerSettingsV3(r.Context())
	if err := preflightPlaybackFile(r.Context(), effectiveFile, h.MissingMarker, h.EventsHub); err != nil {
		writePlaybackFilePreflightError(w, err)
		return
	}
	if req.StartPosition == nil {
		req.StartPosition, err = h.resumePositionV3(r.Context(), userID, profileID, effectiveFile)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load saved playback progress")
			return
		}
	}
	result := playback.PlanPlaybackV3(playback.PlannerInputV3{
		Request: req, RequestedFile: requestedFile, EffectiveFile: effectiveFile,
		AudioTrackIndex: audioIndex, Settings: settings,
		Registry: h.transformationRegistryV3(r.Context()), HLSRegistry: h.lazyHLSPlanningRegistryV3(r.Context()), DVRPUStrippable: h.lazyDVRPUStrippableV3(r.Context(), effectiveFile), Now: time.Now(),
		AdditionalSubtitles: h.downloadedSubtitleInventoryV3(r.Context(), effectiveFile),
	})
	if terminalAllowsAlternateFileV3(result.Terminal) && shouldTryAlternateFileV3(req.QualityPreference) {
		if alternate, alternateErr := h.findAlternateFile(r.Context(), requestedFile); alternateErr == nil && alternate != nil {
			effectiveFile = h.ensurePlaybackProbe(r.Context(), alternate)
			audioIndex = remapAudioIndexV3(requestedFile, effectiveFile, audioIndex)
			if err := h.remapSubtitleSelectionV3(r.Context(), requestedFile, effectiveFile, &req); err != nil {
				response, persistErr := h.persistTerminalStartDecisionV3(r.Context(), userID, profileID, req, requestDigests, requestedFile.ID, effectiveFile.ID, playback.NewTerminalResponseV3("subtitle_unavailable_in_version", err.Error(), false))
				if persistErr != nil {
					writeStartAttemptPersistenceErrorV3(w, persistErr)
					return
				}
				writeJSON(w, http.StatusCreated, response)
				return
			}
			if err := preflightPlaybackFile(r.Context(), effectiveFile, h.MissingMarker, h.EventsHub); err != nil {
				writePlaybackFilePreflightError(w, err)
				return
			}
			result = playback.PlanPlaybackV3(playback.PlannerInputV3{Request: req, RequestedFile: requestedFile, EffectiveFile: effectiveFile, AudioTrackIndex: audioIndex, Settings: settings, Registry: h.transformationRegistryV3(r.Context()), HLSRegistry: h.lazyHLSPlanningRegistryV3(r.Context()), DVRPUStrippable: h.lazyDVRPUStrippableV3(r.Context(), effectiveFile), Now: time.Now(), AdditionalSubtitles: h.downloadedSubtitleInventoryV3(r.Context(), effectiveFile)})
		}
	}
	h.clarifyOriginalQuality4KTerminalV3(r.Context(), result.Terminal, requestedFile, !shouldTryAlternateFileV3(req.QualityPreference))
	if result.Terminal != nil {
		slog.InfoContext(r.Context(), "playback plan decided", "component", "playback",
			"outcome", "terminal",
			"reason", result.Terminal.Reason,
			"file_id", effectiveFile.ID,
			"quality_preference", req.QualityPreference,
		)
		response, persistErr := h.persistTerminalStartDecisionV3(r.Context(), userID, profileID, req, requestDigests, requestedFile.ID, effectiveFile.ID, playback.NewTerminalResponseV3(result.Terminal.Reason, result.Terminal.Message, result.Terminal.Retryable))
		if persistErr != nil {
			writeStartAttemptPersistenceErrorV3(w, persistErr)
			return
		}
		if response.Terminal != nil {
			h.enqueueRouteEventV3(playback.RouteEventRecordV3{RouteEventV3: playback.RouteEventV3{ProtocolVersion: playback.ProtocolV3, PlaybackAttemptID: req.PlaybackAttemptID, Event: playback.RouteEventTerminalV3, FallbackReason: response.Terminal.Reason, OutputContextID: req.ClientPlaybackContext.Output.OutputContextID}, UserID: userID, ProfileID: profileID, ClientName: playbackClientInfoFromRequest(r).Name, ClientVersion: playbackClientInfoFromRequest(r).Version, ClientModel: req.ClientPlaybackContext.Device.Model})
		}
		writeJSON(w, http.StatusCreated, response)
		return
	}
	// One line per plan decision so route selection is reconstructible from
	// server logs alone (finding a mis-planned route previously required
	// correlating client logcat, ffmpeg commands, and session rows).
	slog.InfoContext(r.Context(), "playback plan decided", "component", "playback",
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
	)
	result.Plan.DegradationWarnings = append(result.Plan.DegradationWarnings, warnings...)
	response, statusErr := h.startPlannedPlaybackV3(r, userID, profileID, req, requestDigests, requestedFile, effectiveFile, audioIndex, result)
	if statusErr != nil {
		if statusErr.reason == "playback_attempt_reused" {
			writeError(w, http.StatusConflict, "playback_attempt_reused", statusErr.message)
			return
		}
		if statusErr.reason == "internal_error" {
			slog.ErrorContext(r.Context(), "protocol v3 start failed", "component", "api", "reason", statusErr.reason, "error", statusErr.cause)
		}
		persistedResponse, persistErr := h.startFailureDecisionV3(r.Context(), userID, profileID, req, requestDigests, requestedFile.ID, effectiveFile.ID, statusErr)
		if persistErr != nil {
			writeStartAttemptPersistenceErrorV3(w, persistErr)
			return
		}
		writeJSON(w, http.StatusCreated, persistedResponse)
		return
	}
	writeJSON(w, http.StatusCreated, response)
}

type playbackStartRequestDigestsV3 struct {
	current string
	legacy  string
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

func (h *PlaybackHandler) startPlannedPlaybackV3(r *http.Request, userID int, profileID string, req playback.StartRequestV3, requestDigests playbackStartRequestDigestsV3, requestedFile, effectiveFile *models.MediaFile, audioIndex int, result playback.PlannerResultV3) (playback.DecisionResponseV3, *transportErrorV3) {
	if result.Plan == nil {
		return playback.DecisionResponseV3{}, &transportErrorV3{reason: "internal_error", message: "The server produced no playback plan."}
	}
	if checker, ok := h.sessionMgr.(transcodePermissionChecker); ok && (result.PlayMethod == playback.PlayTranscode || result.TranscodeAudio) {
		if err := checker.CheckTranscodingAllowed(r.Context(), userID, result.PlayMethod == playback.PlayTranscode); err != nil {
			reason := "transcoding_disabled"
			if errors.Is(err, playback.ErrAudioTranscodingDisabled) {
				reason = "audio_transcoding_disabled"
			}
			return playback.DecisionResponseV3{}, &transportErrorV3{reason: reason, message: "The selected server adaptation is disabled for this user."}
		}
	}
	clientInfo := playbackClientInfoFromRequest(r)
	ctx := playback.WithClientInfo(r.Context(), clientInfo)
	var session *playback.Session
	var err error
	if starter, ok := h.sessionMgr.(sessionStarterWithFilesContext); ok {
		session, err = starter.StartSessionWithFilesContext(ctx, userID, profileID, effectiveFile.ID, requestedFile.ID, result.PlayMethod, result.TranscodeAudio)
	} else {
		session, err = h.sessionMgr.StartSessionWithFiles(userID, profileID, effectiveFile.ID, requestedFile.ID, result.PlayMethod, result.TranscodeAudio)
	}
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
	frozenRecipe, frozenErr := h.freezeExecutableRecipeV3(r.Context(), effectiveFile, result)
	if frozenErr != nil {
		abort()
		return playback.DecisionResponseV3{}, subtitleArtifactErrorV3("Failed to freeze the selected subtitle identity.", frozenErr)
	}
	transport, transportErr := h.prepareTransportV3(r, session, effectiveFile, result)
	if transportErr != nil {
		abort()
		return playback.DecisionResponseV3{}, transportErr
	}
	result.Plan.Stream.URL = transport.url
	if err := h.attachSubtitleArtifactV3(r.Context(), session.ID, effectiveFile, result.Plan, result.SubtitleTrackIndex, &frozenRecipe); err != nil {
		transport.rollback()
		abort()
		return playback.DecisionResponseV3{}, subtitleArtifactErrorV3("Failed to prepare the selected subtitle artifact.", err)
	}
	response := playback.DecisionResponseV3{ProtocolVersion: playback.ProtocolV3, ServerFeatures: playback.ServerFeaturesV3(), Outcome: playback.OutcomePlayableV3, SessionID: session.ID, PlaybackPlan: result.Plan}
	record := playback.AttemptRecordV3{PlaybackAttemptID: req.PlaybackAttemptID, SessionID: session.ID, UserID: userID, ProfileID: profileID, RequestedMediaFileID: requestedFile.ID, EffectiveMediaFileID: effectiveFile.ID, CurrentPlanID: result.Plan.PlanID, CurrentPlan: *result.Plan, FrozenRecipe: frozenRecipe, NormalizedRequest: req, StartResponse: response, RequestDigest: requestDigests.current, ExpiresAt: time.Now().Add(playback.MaxTokenTTL)}
	if err := h.updateV3SessionState(r.Context(), session, effectiveFile, result, transport); err != nil {
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
	if !session.DisableProgressPersistence && h.WatchScrobbler != nil && effectiveFile != nil {
		targetID := playbackProgressTarget(effectiveFile)
		if targetID != "" {
			event := h.scrobbleEventForSession(r.Context(), session, targetID, float64(effectiveFile.Duration), session.Position)
			if err := h.WatchScrobbler.ScrobbleStart(r.Context(), event); err != nil {
				slog.WarnContext(r.Context(), "failed to queue watch provider start scrobble", "component", "api", "session", session.ID, "error", err)
			}
		}
	}
	if h.ChapterThumbnailQueuer != nil && effectiveFile != nil {
		slog.InfoContext(r.Context(),
			"queueing chapter thumbnails", "component", "api",
			"source", "playback_start",
			"content_id", effectiveFile.ContentID,
			"file_id", effectiveFile.ID,
			"target_seconds", session.Position,
		)
		h.ChapterThumbnailQueuer.QueuePriorityFileAtPosition(r.Context(), effectiveFile.ID, session.Position)
	}
	h.maybeQueueLazyPlaybackMarkers(r.Context(), session, effectiveFile)
	h.persistSeriesSelectionsV3(r.Context(), userID, profileID, effectiveFile, plannedAudioTrackIndexV3(result, audioIndex))
	h.syncSessionsNow(r.Context(), "v3_start")
	h.enqueueRouteEventV3(playback.RouteEventRecordV3{RouteEventV3: playback.RouteEventV3{ProtocolVersion: playback.ProtocolV3, PlaybackAttemptID: req.PlaybackAttemptID, SessionID: session.ID, PlanID: result.Plan.PlanID, Event: playback.RouteEventPlanSelectedV3, AppliedQuirkIDs: appliedQuirkIDsV3(result.Plan), QuirkRegistryRevision: appliedQuirkRevisionV3(result.Plan), OutputContextID: req.ClientPlaybackContext.Output.OutputContextID}, UserID: userID, ProfileID: profileID, ClientName: clientInfo.Name, ClientVersion: clientInfo.Version, ClientModel: req.ClientPlaybackContext.Device.Model})
	return response, nil
}

// persistSeriesSelectionsV3 records the version and audio-track choices this
// plan settled on, so the next episode of the same series opens with them
// already applied. The catalog reads both preferences back
// (internal/catalog/detail.go), and v3 is now the only writer: the legacy start
// and audio-PATCH endpoints that used to record them are gone.
func (h *PlaybackHandler) persistSeriesSelectionsV3(ctx context.Context, userID int, profileID string, file *models.MediaFile, audioTrackIndex int) {
	h.persistSeriesPlaybackPreference(ctx, userID, profileID, file)
	h.persistAudioPreference(ctx, userID, profileID, file, audioTrackIndex)
}

func (h *PlaybackHandler) prepareTransportV3(r *http.Request, session *playback.Session, file *models.MediaFile, result playback.PlannerResultV3) (preparedTransportV3, *transportErrorV3) {
	timeline, timelineErr := h.prepareTransportTimelineV3(r.Context(), session, file, result)
	if timelineErr != nil {
		return preparedTransportV3{}, timelineErr
	}
	if result.Plan.Delivery != playback.DeliveryTranscodeHLSV3 && result.Plan.Delivery != playback.DeliveryRemuxHLSV3 {
		return h.prepareIdentityTransportV3(session, result, timeline), nil
	}
	if h.NodePlanner != nil {
		plan := h.planNodeSessionV3(r.Context(), session, result)
		if plan.TranscodeNode != nil {
			transformations, err := h.remoteTransformationsV3(r.Context(), plan.TranscodeNode.URL)
			if err == nil {
				err = validateAdvertisedTransformationsV3(result.Plan, transformations)
			}
			if err == nil {
				transport, transportErr := h.prepareRemoteTransportV3(r, session, file, result, plan, timeline)
				if transportErr != nil {
					if releaser, ok := h.NodePlanner.(sessionReservationReleaserV3); ok {
						releaser.ReleaseSession(session.ID)
					}
				}
				return transport, transportErr
			}
			slog.WarnContext(r.Context(), "protocol v3 transcode node capability mismatch", "node", plan.TranscodeNode.URL, "error", err)
			if releaser, ok := h.NodePlanner.(sessionReservationReleaserV3); ok {
				releaser.ReleaseSession(session.ID)
			}
			if !nodepool.LocalTranscodeFallbackAllowed(r.Context(), h.SettingsRepo) {
				return preparedTransportV3{}, &transportErrorV3{reason: "transcode_node_capability_unavailable", message: "No transcode node can execute the selected playback recipe.", retryable: true, cause: err}
			}
		}
		if !nodepool.LocalTranscodeFallbackAllowed(r.Context(), h.SettingsRepo) {
			return preparedTransportV3{}, &transportErrorV3{reason: "capacity_unavailable", message: "No transcode node is available and local fallback is disabled.", retryable: true}
		}
	}
	// Capability-union planning may select transformations only pooled nodes
	// can execute; the local binary must prove it carries the recipe before
	// this fallback spawns an ffmpeg that would fail at runtime. Retryable:
	// a capable node freeing up satisfies the same plan. Transformation-free
	// plans skip the check (and the local probe behind it) entirely.
	if planRequiresServerTransformationsV3(result.Plan) {
		if err := validateAdvertisedTransformationsV3(result.Plan, h.transformationRegistryV3(r.Context()).Advertised()); err != nil {
			return preparedTransportV3{}, &transportErrorV3{reason: "transcode_node_capability_unavailable", message: "No available transcode executor can run the selected playback recipe.", retryable: true, cause: err}
		}
	}
	return h.prepareLocalTransportV3(r, session, file, result, timeline)
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
		seekSeconds, startSegment := configureHLSTimelineV3(result.Plan, result.TargetVideoCodec, 2, sourceMetadata.DurationSeconds)
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

func (h *PlaybackHandler) prepareIdentityTransportV3(session *playback.Session, result playback.PlannerResultV3, timeline preparedTimelineV3) preparedTransportV3 {
	routeSession := *session
	routeSession.PlayMethod = result.PlayMethod
	routeSession.BasePlayMethod = result.PlayMethod
	routeSession.MediaFileID = result.Plan.EffectiveMediaFileID
	routeSession.AudioTrackIndex = plannedAudioTrackIndexV3(result, session.AudioTrackIndex)
	routeSession.TranscodeAudio = result.TranscodeAudio
	routeSession.TargetAudioCodec = result.TargetAudioCodec
	routeSession.TargetAudioChannels = result.TargetAudioChannels
	routeSession.TargetAudioBitrateKbps = result.TargetAudioBitrateKbps
	routeSession.RemuxDVMode = remuxDVModeForPlanV3(result.Plan)
	previousNodeURL := session.TranscodeNodeURL
	previousTransportID := remoteTransportID(session)
	unlock := h.tm.LockSessionLifecycle(session.ID)
	committed := false
	streamURL := h.playbackStreamURL(&routeSession)
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
			}
			unlock()
		},
		rollback: func() {
			if committed {
				return
			}
			committed = true
			unlock()
		},
	}
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

func (h *PlaybackHandler) prepareLocalTransportV3(r *http.Request, session *playback.Session, file *models.MediaFile, result playback.PlannerResultV3, timeline preparedTimelineV3) (preparedTransportV3, *transportErrorV3) {
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
	opts := playback.TranscodeOpts{InputPath: file.FilePath, OutputDir: outputDir, OutputSubdir: outputSubdir, SessionID: session.ID, SourceVideoCodec: sourceMetadata.VideoCodec, SourceVideoProfile: sourceProfile, SourceVideoBitDepth: sourceBitDepth, SoftwareVideoDecode: sourceMetadata.SoftwareVideoDecode, VideoBitstreamFilter: videoBitstreamFilterForPlanV3(result.Plan), SeekSeconds: timeline.seekSeconds, StreamOriginSeconds: timeline.streamOriginSeconds, CopySeekAnchorResolved: timeline.copySeekAnchorResolved, StartSegmentNumber: timeline.startSegmentNumber, TargetResolution: result.TargetResolution, TargetCodecVideo: videoCodec, TargetCodecAudio: result.TargetAudioCodec, TargetAudioChannels: result.TargetAudioChannels, TargetAudioBitrateKbps: result.TargetAudioBitrateKbps, TargetBitrateKbps: result.TargetBitrateKbps, SegmentDuration: 2, FFmpegPath: cfg.FFmpegPath, HWAccel: cfg.HWAccel, HWDevice: cfg.HWDevice, AudioTrackIndex: plannedAudioTrackIndexV3(result, session.AudioTrackIndex), SubtitleTrackIndex: result.SubtitleTransportTrackIndex, SubtitleBurnIn: result.SubtitleBurnIn, SubtitleCodec: result.SubtitleCodec, TotalDuration: sourceMetadata.DurationSeconds, FastStart: true, NodeType: playbackNodeIntegratedV3, ExecutionMode: playbackNodeIntegratedV3, FFmpegLogSink: h.FFmpegLogSink}
	ts, err := h.startLocalPlaybackTransport(r.Context(), opts)
	if err != nil {
		unlock()
		return preparedTransportV3{}, &transportErrorV3{reason: transcodeStartFailedReasonV3, message: "Failed to start the playback transport.", retryable: true, cause: err}
	}
	if _, readyErr := ts.WaitForManifest(playback.ManifestStartupTimeout); readyErr != nil {
		wasRunning := ts.IsRunning()
		failedDevice := ts.Opts().HWDevice
		transportErr := manifestStartupTransportErrorV3(wasRunning, readyErr)
		_ = ts.Close()
		if wasRunning {
			unlock()
			return preparedTransportV3{}, transportErr
		}

		// FFmpeg and GPU drivers can fail before producing their first segment
		// even though the recipe is valid. Retry one clean generation, preferring
		// another configured render device so a transient device failure does not
		// become an immediate client-visible transport error.
		retryOpts := opts
		retryOpts.AvoidHWDevice = failedDevice
		slog.WarnContext(r.Context(), "local transcode crashed during startup; retrying once",
			"component", "playback",
			"playback_session_id", session.ID,
			"failed_device", failedDevice,
			"configured_devices", retryOpts.HWDevice,
			"error", readyErr)
		ts, err = h.startLocalPlaybackTransport(r.Context(), retryOpts)
		if err != nil {
			unlock()
			return preparedTransportV3{}, &transportErrorV3{reason: transcodeStartFailedReasonV3, message: "Failed to start the playback transport.", retryable: true, cause: err}
		}
		if _, retryReadyErr := ts.WaitForManifest(playback.ManifestStartupTimeout); retryReadyErr != nil {
			transportErr = manifestStartupTransportErrorV3(ts.IsRunning(), retryReadyErr)
			_ = ts.Close()
			unlock()
			return preparedTransportV3{}, transportErr
		}
	}
	card := playback.NewRecipeCard(session.UserID, session.ProfileID, file.ID, "", ts.Opts())
	url := appendStreamToken(fmt.Sprintf("/playback/transcode/%s/master.m3u8", session.ID), h.signSessionToken(card))
	committed := false
	previousNodeURL := session.TranscodeNodeURL
	previousTransportID := remoteTransportID(session)
	return preparedTransportV3{
		url: url,
		commit: func() {
			if committed {
				return
			}
			committed = true
			previous := h.tm.SwapTranscodeSession(session.ID, ts)
			unlock()
			if previous != nil && previous != ts {
				_ = previous.Close()
			}
			if previousNodeURL != "" {
				h.tm.StopRemoteTranscode(previousTransportID, previousNodeURL)
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

func (h *PlaybackHandler) prepareRemoteTransportV3(r *http.Request, session *playback.Session, file *models.MediaFile, result playback.PlannerResultV3, nodePlan nodepool.Plan, timeline preparedTimelineV3) (preparedTransportV3, *transportErrorV3) {
	node := nodePlan.TranscodeNode
	transportID := transportGenerationV3(session.ID, result.Plan.PlanID)
	videoCodec := result.TargetVideoCodec
	if result.Plan.Delivery == playback.DeliveryRemuxHLSV3 {
		videoCodec = "copy"
	}
	sourceMetadata := sourceExecutionMetadataV3(file, result)
	sourceProfile, sourceBitDepth := sourceVideoTranscodeFactsV3(file, result)
	req := transcodenode.TranscodeStartRequest{SessionID: transportID, InputPath: file.FilePath, SourceVideoCodec: sourceMetadata.VideoCodec, SourceVideoProfile: sourceProfile, SourceVideoBitDepth: sourceBitDepth, SoftwareVideoDecode: sourceMetadata.SoftwareVideoDecode, VideoBitstreamFilter: videoBitstreamFilterForPlanV3(result.Plan), SeekSeconds: timeline.seekSeconds, StreamOriginSeconds: timeline.streamOriginSeconds, CopySeekAnchorResolved: timeline.copySeekAnchorResolved, StartSegmentNumber: timeline.startSegmentNumber, TargetResolution: result.TargetResolution, TargetCodecVideo: videoCodec, TargetCodecAudio: result.TargetAudioCodec, TargetAudioChannels: result.TargetAudioChannels, TargetAudioBitrateKbps: result.TargetAudioBitrateKbps, TargetBitrateKbps: result.TargetBitrateKbps, SegmentDuration: 2, HWAccel: h.playbackConfig().HWAccel, AudioTrackIndex: plannedAudioTrackIndexV3(result, session.AudioTrackIndex), SubtitleTrackIndex: result.SubtitleTransportTrackIndex, SubtitleBurnIn: result.SubtitleBurnIn, SubtitleCodec: result.SubtitleCodec, TotalDuration: sourceMetadata.DurationSeconds, RequireReady: true}
	nodeResp, status, err := h.startRemotePlaybackTransport(r.Context(), node.URL, req)
	if err != nil {
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
	hw := firstNonEmptyHandlerV3(strings.TrimSpace(nodeResp.HWAccel), strings.TrimSpace(req.HWAccel))
	card := playback.NewRecipeCard(session.UserID, session.ProfileID, file.ID, node.URL, playback.TranscodeOpts{InputPath: req.InputPath, SessionID: session.ID, TranscodeTransportID: transportID, SourceVideoCodec: req.SourceVideoCodec, SourceVideoProfile: req.SourceVideoProfile, SourceVideoBitDepth: req.SourceVideoBitDepth, SoftwareVideoDecode: req.SoftwareVideoDecode, VideoBitstreamFilter: req.VideoBitstreamFilter, SeekSeconds: req.SeekSeconds, StreamOriginSeconds: req.StreamOriginSeconds, CopySeekAnchorResolved: req.CopySeekAnchorResolved, StartSegmentNumber: req.StartSegmentNumber, TargetResolution: req.TargetResolution, TargetCodecVideo: req.TargetCodecVideo, TargetCodecAudio: req.TargetCodecAudio, TargetAudioChannels: req.TargetAudioChannels, TargetAudioBitrateKbps: req.TargetAudioBitrateKbps, TargetBitrateKbps: req.TargetBitrateKbps, SegmentDuration: req.SegmentDuration, HWAccel: hw, AudioTrackIndex: req.AudioTrackIndex, SubtitleTrackIndex: req.SubtitleTrackIndex, SubtitleBurnIn: req.SubtitleBurnIn, SubtitleCodec: req.SubtitleCodec, TotalDuration: req.TotalDuration})
	url := h.buildProxyManifestURL(card, nodePlan.ProxyNode)
	committed := false
	previousNodeURL := session.TranscodeNodeURL
	previousTransportID := remoteTransportID(session)
	unlock := h.tm.LockSessionLifecycle(session.ID)
	return preparedTransportV3{url: url, nodeURL: node.URL, transportID: transportID, commit: func() {
		if committed {
			return
		}
		committed = true
		h.tm.CloseTranscodeSession(session.ID, "")
		if previousNodeURL != "" {
			h.tm.StopRemoteTranscode(previousTransportID, previousNodeURL)
		}
		unlock()
	}, rollback: func() {
		if committed {
			return
		}
		committed = true
		h.tm.StopRemoteTranscode(transportID, node.URL)
		// The accepted node job is gone; drop the planner reservation too so
		// repeated failed starts cannot pin the node's max-job or bandwidth
		// budget until the reservation ages out.
		if releaser, ok := h.NodePlanner.(sessionReservationReleaserV3); ok {
			releaser.ReleaseSession(session.ID)
		}
		unlock()
	}}, nil
}

func sourceExecutionMetadataV3(file *models.MediaFile, result playback.PlannerResultV3) playback.SourceExecutionMetadataV3 {
	if result.FrozenSourceMetadata != nil {
		return *result.FrozenSourceMetadata
	}
	if file == nil {
		return playback.SourceExecutionMetadataV3{}
	}
	videoCodec, profile, bitDepth := playback.SourceVideoTranscodeFacts(file)
	return playback.SourceExecutionMetadataV3{
		VideoCodec:          videoCodec,
		SoftwareVideoDecode: playback.RequiresSoftwareVideoDecode(videoCodec, profile, bitDepth),
		DurationSeconds:     float64(file.Duration),
	}
}

func sourceVideoTranscodeFactsV3(file *models.MediaFile, result playback.PlannerResultV3) (string, int) {
	if result.FrozenSourceMetadata != nil {
		return "", 0
	}
	_, profile, bitDepth := playback.SourceVideoTranscodeFacts(file)
	return profile, bitDepth
}

func (h *PlaybackHandler) v3SessionStreamState(ctx context.Context, session *playback.Session, file *models.MediaFile, result playback.PlannerResultV3, transport preparedTransportV3) playback.SessionStreamState {
	state := playback.SessionStreamState{PlayMethod: result.PlayMethod, BasePlayMethod: result.PlayMethod, AudioTrackIndex: plannedAudioTrackIndexV3(result, session.AudioTrackIndex), TranscodeAudio: result.TranscodeAudio, RemuxDVMode: remuxDVModeForPlanV3(result.Plan), TranscodeNodeURL: transport.nodeURL, TranscodeTransportID: transport.transportID, TranscodeRouteSet: true, ClientIP: clientip.FromContext(ctx), ClientName: session.ClientName, ClientVersion: session.ClientVersion, ClientUserAgent: session.ClientUserAgent, StreamBitrateKbps: result.TargetBitrateKbps, TargetVideoCodec: result.TargetVideoCodec, TargetAudioCodec: result.TargetAudioCodec, TargetAudioChannels: result.TargetAudioChannels, TargetAudioBitrateKbps: result.TargetAudioBitrateKbps, TargetResolution: result.TargetResolution, SubtitleTrackIndex: result.SubtitleTransportTrackIndex, SubtitleBurnIn: result.SubtitleBurnIn}
	if result.Plan != nil && (result.Plan.Delivery == playback.DeliveryTranscodeHLSV3 || result.Plan.Delivery == playback.DeliveryRemuxHLSV3) {
		state.SegmentDuration = 2
	}
	if state.StreamBitrateKbps <= 0 {
		state.StreamBitrateKbps = result.TargetAudioBitrateKbps
	}
	if state.StreamBitrateKbps <= 0 {
		state.StreamBitrateKbps = fileBitrateKbps(file)
	}
	return state
}

func (h *PlaybackHandler) updateV3SessionState(ctx context.Context, session *playback.Session, file *models.MediaFile, result playback.PlannerResultV3, transport preparedTransportV3) error {
	return h.sessionMgr.UpdateStreamState(session.ID, h.v3SessionStreamState(ctx, session, file, result, transport))
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
	if selectedIndex < 0 || (plan.Subtitle.Mode != playback.SubtitleRenderV3 && plan.Subtitle.Mode != playback.SubtitleConvertV3) {
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
	writeJSON(w, http.StatusOK, response)
}

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
		result = playback.PlanPlaybackV3(playback.PlannerInputV3{Request: start, RequestedFile: plannerRequestedFile, EffectiveFile: effectiveFile, AudioTrackIndex: audioIndex, Settings: h.plannerSettingsV3(r.Context()), Registry: h.transformationRegistryV3(r.Context()), HLSRegistry: h.lazyHLSPlanningRegistryV3(r.Context()), DVRPUStrippable: h.lazyDVRPUStrippableV3(r.Context(), effectiveFile), Now: time.Now(), AttemptedKeys: attemptedKeys, AdditionalSubtitles: h.downloadedSubtitleInventoryV3(r.Context(), effectiveFile)})
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
		result = playback.PlanPlaybackV3(playback.PlannerInputV3{Request: start, RequestedFile: plannerRequestedFile, EffectiveFile: effectiveFile, AudioTrackIndex: audioIndex, Settings: h.plannerSettingsV3(r.Context()), Registry: h.transformationRegistryV3(r.Context()), HLSRegistry: h.lazyHLSPlanningRegistryV3(r.Context()), DVRPUStrippable: h.lazyDVRPUStrippableV3(r.Context(), effectiveFile), Now: time.Now(), AttemptedKeys: attemptedKeys, AdditionalSubtitles: h.downloadedSubtitleInventoryV3(r.Context(), effectiveFile)})
	}
	if terminalAllowsAlternateFileV3(result.Terminal) && replanAllowsAlternateFileV3(operation, start.QualityPreference) {
		if alternate, alternateErr := h.findAlternateFile(r.Context(), requestedFile); alternateErr == nil && alternate != nil {
			alternate = h.ensurePlaybackProbe(r.Context(), alternate)
			remappedAudio := remapAudioIndexV3(effectiveFile, alternate, audioIndex)
			if err := h.remapSubtitleSelectionV3(r.Context(), effectiveFile, alternate, &start); err == nil {
				start.FileID = alternate.ID
				if err := preflightPlaybackFile(r.Context(), alternate, h.MissingMarker, h.EventsHub); err == nil {
					effectiveFile = alternate
					audioIndex = remappedAudio
					result = playback.PlanPlaybackV3(playback.PlannerInputV3{Request: start, RequestedFile: plannerRequestedFile, EffectiveFile: effectiveFile, AudioTrackIndex: audioIndex, Settings: h.plannerSettingsV3(r.Context()), Registry: h.transformationRegistryV3(r.Context()), HLSRegistry: h.lazyHLSPlanningRegistryV3(r.Context()), DVRPUStrippable: h.lazyDVRPUStrippableV3(r.Context(), effectiveFile), Now: time.Now(), AttemptedKeys: attemptedKeys, AdditionalSubtitles: h.downloadedSubtitleInventoryV3(r.Context(), effectiveFile)})
				}
			} else if start.SubtitleTrackIndex != nil || start.SubtitleTrackID != "" {
				result = playback.PlannerResultV3{Terminal: &playback.TerminalV3{
					Reason:    "subtitle_unavailable_in_version",
					Message:   "The selected subtitle track is unavailable in the fallback media version.",
					Retryable: false,
				}}
			}
		}
	}
	h.clarifyOriginalQuality4KTerminalV3(r.Context(), result.Terminal, requestedFile, replanAlternateFilePinnedByOriginalQualityV3(operation, start.QualityPreference))
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
	transportReused := trackChange && h.hasActiveHLSTransportV3(session) && sidecarOnlyHLSReplanV3(record, result.Plan, artifactRecipe, req.ClientPlaybackContext.Output.OutputContextID)
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
			"component", "playback",
			"playback_session_id", session.ID,
			"previous_plan_id", record.CurrentPlanID,
			"plan_id", result.Plan.PlanID,
		)
	} else {
		var transportErr *transportErrorV3
		transport, transportErr = h.prepareTransportV3(r, session, effectiveFile, result)
		if transportErr != nil {
			return playback.DecisionResponseV3{}, *record, nil, transportErr
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
		StreamState:          h.v3SessionStreamState(r.Context(), session, effectiveFile, result, transport),
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
			h.persistAudioPreference(r.Context(), session.UserID, session.ProfileID, effectiveFile, plannedAudioTrackIndexV3(result, session.AudioTrackIndex))
		}
		h.syncSessionsNow(r.Context(), "v3_replan")
		event := playback.RouteEventPlanSelectedV3
		clientModel := req.ClientPlaybackContext.Device.Model
		if seekReanchor {
			event = playback.RouteEventRuntimeCorrectionSucceededV3
			clientModel = start.ClientPlaybackContext.Device.Model
		}
		h.enqueueRouteEventV3(playback.RouteEventRecordV3{RouteEventV3: playback.RouteEventV3{ProtocolVersion: playback.ProtocolV3, PlaybackAttemptID: req.PlaybackAttemptID, SessionID: session.ID, PlanID: result.Plan.PlanID, PlanAttemptID: req.PlanAttemptID, PlanAttemptKey: playback.PlanAttemptKeyV3(*result.Plan, start.ClientPlaybackContext.Output.OutputContextID, nil), Event: event, FallbackReason: req.Failure.Classification, AppliedQuirkIDs: appliedQuirkIDsV3(result.Plan), QuirkRegistryRevision: appliedQuirkRevisionV3(result.Plan), OutputContextID: start.ClientPlaybackContext.Output.OutputContextID}, UserID: session.UserID, ProfileID: session.ProfileID, ClientName: session.ClientName, ClientVersion: session.ClientVersion, ClientModel: clientModel})
	}
	transport.rollback = func() {
		originalRollback()
		cancelReservation()
	}
	reservationHandedOff = true
	return response, updated, &transport, nil
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
func sidecarOnlyHLSReplanV3(record *playback.AttemptRecordV3, candidate *playback.PlanV3, candidateRecipe playback.ExecutableRecipeV3, outputContextID string) bool {
	if record == nil || candidate == nil || record.CurrentPlan.Stream.URL == "" ||
		!record.FrozenRecipe.ValidFor(record.CurrentPlan) || !candidateRecipe.ValidFor(*candidate) ||
		record.NormalizedRequest.ClientPlaybackContext.Output.OutputContextID != outputContextID ||
		record.EffectiveMediaFileID != candidate.EffectiveMediaFileID ||
		record.CurrentPlan.RequestedMediaFileID != candidate.RequestedMediaFileID ||
		record.CurrentPlan.EffectiveMediaFileID != candidate.EffectiveMediaFileID ||
		!isHLSDeliveryV3(record.CurrentPlan.Delivery) || record.CurrentPlan.Delivery != candidate.Delivery ||
		record.CurrentPlan.Subtitle.Mode == playback.SubtitleBurnInV3 || candidate.Subtitle.Mode == playback.SubtitleBurnInV3 ||
		!sameTrackIdentityV3(record.CurrentPlan.SelectedTracks.Audio, candidate.SelectedTracks.Audio) {
		return false
	}
	if record.CurrentPlan.Stream.Protocol != candidate.Stream.Protocol ||
		record.CurrentPlan.Stream.Container != candidate.Stream.Container ||
		record.CurrentPlan.Stream.MIMEType != candidate.Stream.MIMEType ||
		record.CurrentPlan.Stream.HeaderRefresh != candidate.Stream.HeaderRefresh ||
		!sameExecutableAVRecipeV3(record.FrozenRecipe, candidateRecipe) ||
		!sameEffectiveAVRecipeV3(record.CurrentPlan.EffectiveRecipe, candidate.EffectiveRecipe) ||
		record.CurrentPlan.Claims.Video != candidate.Claims.Video ||
		record.CurrentPlan.Claims.Audio != candidate.Claims.Audio ||
		!sameTransformationsV3(record.CurrentPlan.Transformations, candidate.Transformations) ||
		!sameAppliedQuirksV3(record.CurrentPlan.AppliedQuirks, candidate.AppliedQuirks) ||
		!sameStringMultisetV3(record.CurrentPlan.RuntimeCorrections, candidate.RuntimeCorrections) {
		return false
	}
	return true
}

func isHLSDeliveryV3(delivery playback.DeliveryV3) bool {
	return delivery == playback.DeliveryRemuxHLSV3 || delivery == playback.DeliveryTranscodeHLSV3
}

func sameExecutableAVRecipeV3(left, right playback.ExecutableRecipeV3) bool {
	return left.PlayMethod == right.PlayMethod &&
		left.TranscodeAudio == right.TranscodeAudio &&
		left.TargetVideoCodec == right.TargetVideoCodec &&
		left.TargetAudioCodec == right.TargetAudioCodec &&
		left.TargetAudioChannels == right.TargetAudioChannels &&
		left.TargetAudioBitrateKbps == right.TargetAudioBitrateKbps &&
		left.TargetResolution == right.TargetResolution &&
		left.TargetBitrateKbps == right.TargetBitrateKbps &&
		left.SourceVideoCodec == right.SourceVideoCodec &&
		left.SoftwareVideoDecode == right.SoftwareVideoDecode &&
		left.SourceDurationSeconds == right.SourceDurationSeconds
}

func sameEffectiveAVRecipeV3(left, right playback.EffectiveRecipeV3) bool {
	return left.VideoCodec == right.VideoCodec && left.AudioCodec == right.AudioCodec &&
		optionalIntEqualV3(left.Width, right.Width) && optionalIntEqualV3(left.Height, right.Height) &&
		optionalFloatEqualV3(left.FrameRate, right.FrameRate) &&
		optionalIntEqualV3(left.BitrateKbps, right.BitrateKbps) &&
		left.DynamicRange == right.DynamicRange &&
		optionalIntEqualV3(left.AudioChannels, right.AudioChannels) && left.AudioLayout == right.AudioLayout
}

func reusedHLSTransportV3(session *playback.Session, streamURL string) preparedTransportV3 {
	transport := preparedTransportV3{url: streamURL}
	if session != nil {
		transport.nodeURL = session.TranscodeNodeURL
		transport.transportID = session.TranscodeTransportID
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
	terminalNoAlternateVersionV3      = "no_alternate_version"
	terminalHDRTranscodeUnsupportedV3 = "hdr_transcode_unsupported"
)

func terminalAllowsAlternateFileV3(terminal *playback.TerminalV3) bool {
	if terminal == nil {
		return false
	}
	return terminal.Reason == terminalNoAlternateVersionV3 || terminal.Reason == terminalHDRTranscodeUnsupportedV3
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
	if alternate, err := h.findAlternateFile(ctx, requestedFile); err == nil && alternate != nil {
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
	client := playbackClientInfoFromRequest(r)
	h.enqueueRouteEventV3(playback.RouteEventRecordV3{RouteEventV3: event, UserID: userID, ProfileID: profileID, ClientName: client.Name, ClientVersion: client.Version, ClientModel: event.Diagnostics["device_model"]})
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

func (h *PlaybackHandler) plannerSettingsV3(ctx context.Context) playback.PlannerSettingsV3 {
	settings := playback.PlannerSettingsV3{TranscodeEnabled: h.playbackConfig().TranscodeEnabled}
	if h.SettingsRepo != nil {
		value, _ := h.SettingsRepo.Get(ctx, "allow_4k_transcode")
		settings.Allow4KTranscode = strings.EqualFold(value, "true")
	}
	return settings
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
