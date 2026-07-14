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
	"github.com/Silo-Server/silo-server/internal/subtitles"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
)

const (
	maxPlaybackV3BodyBytes      = 256 << 10
	maxPlaybackV3EventBodyBytes = 32 << 10
	replanLeaseDurationV3       = 15 * time.Second
	v3NodeCapabilityTTL         = time.Minute
)

type v3NodeCapabilityCache struct {
	transformations []playback.TransformationV3
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

type transportErrorV3 struct {
	reason    string
	message   string
	retryable bool
	cause     error
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

// v3FlagCacheTTL bounds how stale a v3 feature-flag read may be. Flags stay
// DB-backed so enabling or rolling back needs no process restart, but reading
// them once per start/replan/event would put an uncached settings SELECT on
// every latency-sensitive playback request.
const v3FlagCacheTTL = 5 * time.Second

type v3FlagCacheEntry struct {
	value     bool
	expiresAt time.Time
}

func (h *PlaybackHandler) settingFlagCachedV3(ctx context.Context, key string) bool {
	if h == nil || h.SettingsRepo == nil {
		return false
	}
	now := time.Now()
	h.v3FlagMu.Lock()
	entry, ok := h.v3Flags[key]
	h.v3FlagMu.Unlock()
	if ok && now.Before(entry.expiresAt) {
		return entry.value
	}
	value, err := h.SettingsRepo.Get(ctx, key)
	enabled := err == nil && strings.EqualFold(strings.TrimSpace(value), "true")
	if err == nil {
		h.v3FlagMu.Lock()
		if h.v3Flags == nil {
			h.v3Flags = make(map[string]v3FlagCacheEntry)
		}
		h.v3Flags[key] = v3FlagCacheEntry{value: enabled, expiresAt: now.Add(v3FlagCacheTTL)}
		h.v3FlagMu.Unlock()
	}
	return enabled
}

func (h *PlaybackHandler) protocolV3Enabled(ctx context.Context) bool {
	return h.settingFlagCachedV3(ctx, "playback.protocol_v3_enabled")
}

func (h *PlaybackHandler) transformationRegistryV3(ctx context.Context) *playback.TransformationRegistryV3 {
	h.v3RegistryOnce.Do(func() {
		h.v3Registry = playback.ProbeTransformationRegistryV3(context.WithoutCancel(ctx), h.playbackConfig().FFmpegPath)
	})
	return h.v3Registry
}

func (h *PlaybackHandler) remoteTransformationsV3(ctx context.Context, nodeURL string) ([]playback.TransformationV3, error) {
	now := time.Now()
	h.v3NodeCapabilitiesMu.Lock()
	entry, ok := h.v3NodeCapabilities[nodeURL]
	h.v3NodeCapabilitiesMu.Unlock()
	if ok && now.Before(entry.expiresAt) {
		return append([]playback.TransformationV3(nil), entry.transformations...), nil
	}

	info, err := fetchRemoteTranscodeCapabilities(ctx, nodeURL, h.JWTSecret)
	if err != nil {
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

func validateRemoteTransformationsV3(plan *playback.PlanV3, advertised []playback.TransformationV3) error {
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
			return fmt.Errorf("transcode node lacks transformation %s@%s", required.Name, required.RecipeVersion)
		}
	}
	return nil
}

// HandlePlaybackCapabilityV3 reports only transformations that the installed
// runtime has actually probed. The feature flag is read per request so rollback
// does not require a process restart.
func (h *PlaybackHandler) HandlePlaybackCapabilityV3(w http.ResponseWriter, r *http.Request) {
	if apimw.GetUserID(r.Context()) == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	enabled := h.protocolV3Enabled(r.Context())
	response := playback.CapabilityResponseV3{Enabled: enabled, ProtocolVersions: []int{playback.ProtocolV3}}
	if !enabled {
		response.Features = []string{}
		response.Deliveries = []playback.DeliveryV3{}
		response.Transformations = []playback.TransformationV3{}
		response.Reason = "disabled"
		writeJSON(w, http.StatusOK, response)
		return
	}
	response.Features = []string{playback.FeaturePlaybackPlanV3, playback.FeatureMedia3Only, playback.FeatureDetailedDecodeV3, playback.FeatureLayoutPassthrough, playback.FeatureRouteDiagnostics, playback.FeatureDeviceQuirksV3, playback.FeatureSeekReanchorV3}
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
	if !h.protocolV3Enabled(r.Context()) {
		writeJSON(w, http.StatusCreated, playback.DisabledResponseV3())
		return
	}
	userID := apimw.GetUserID(r.Context())
	digestBytes := sha256.Sum256(body)
	requestDigest := hex.EncodeToString(digestBytes[:])
	if existing, lookupErr := h.PlanStoreV3.GetAttemptByPlaybackAttemptID(r.Context(), req.PlaybackAttemptID); lookupErr == nil {
		if existing.UserID != userID || existing.ProfileID != profileID || existing.RequestedMediaFileID != req.FileID ||
			(existing.RequestDigest != "" && existing.RequestDigest != requestDigest) {
			writeError(w, http.StatusConflict, "playback_attempt_reused", "The playback attempt ID belongs to a different request")
			return
		}
		// The replayed plan is only usable while its session is alive; a dead
		// session must surface as a retryable terminal so the client mints a
		// fresh attempt instead of replaying a plan it can never stream.
		if _, sessionErr := h.sessionMgr.GetSession(existing.SessionID); sessionErr != nil {
			writeJSON(w, http.StatusCreated, playback.NewTerminalResponseV3("session_expired", "The playback session for this attempt has ended.", true))
			return
		}
		writeJSON(w, http.StatusCreated, decisionResponseFromAttemptV3(existing))
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
	effectiveFile := requestedFile
	settings := h.plannerSettingsV3(r.Context())
	if err := preflightPlaybackFile(r.Context(), effectiveFile, h.MissingMarker, h.EventsHub); err != nil {
		writePlaybackFilePreflightError(w, err)
		return
	}
	result := playback.PlanPlaybackV3(playback.PlannerInputV3{
		Request: req, RequestedFile: requestedFile, EffectiveFile: effectiveFile,
		AudioTrackIndex: audioIndex, Settings: settings,
		Registry: h.transformationRegistryV3(r.Context()), Now: time.Now(),
		AdditionalSubtitles: h.downloadedSubtitleInventoryV3(r.Context(), effectiveFile),
	})
	if result.Terminal != nil && result.Terminal.Reason == "no_alternate_version" && shouldTryAlternateFileV3(req.QualityPreference) {
		if alternate, alternateErr := h.findAlternateFile(r.Context(), requestedFile); alternateErr == nil && alternate != nil {
			effectiveFile = h.ensurePlaybackProbe(r.Context(), alternate)
			audioIndex = remapAudioIndexV3(requestedFile, effectiveFile, audioIndex)
			if err := h.remapSubtitleSelectionV3(r.Context(), requestedFile, effectiveFile, &req); err != nil {
				writeJSON(w, http.StatusCreated, playback.NewTerminalResponseV3("subtitle_unavailable_in_version", err.Error(), false))
				return
			}
			if err := preflightPlaybackFile(r.Context(), effectiveFile, h.MissingMarker, h.EventsHub); err != nil {
				writePlaybackFilePreflightError(w, err)
				return
			}
			result = playback.PlanPlaybackV3(playback.PlannerInputV3{Request: req, RequestedFile: requestedFile, EffectiveFile: effectiveFile, AudioTrackIndex: audioIndex, Settings: settings, Registry: h.transformationRegistryV3(r.Context()), Now: time.Now(), AdditionalSubtitles: h.downloadedSubtitleInventoryV3(r.Context(), effectiveFile)})
		}
	}
	if result.Terminal != nil {
		response := playback.NewTerminalResponseV3(result.Terminal.Reason, result.Terminal.Message, result.Terminal.Retryable)
		h.enqueueRouteEventV3(playback.RouteEventRecordV3{RouteEventV3: playback.RouteEventV3{ProtocolVersion: playback.ProtocolV3, PlaybackAttemptID: req.PlaybackAttemptID, Event: playback.RouteEventTerminalV3, FallbackReason: result.Terminal.Reason, OutputRouteGeneration: req.OutputRouteGeneration}, UserID: userID, ProfileID: profileID, ClientName: playbackClientInfoFromRequest(r).Name, ClientVersion: playbackClientInfoFromRequest(r).Version, ClientModel: req.ClientPlaybackContext.Device.Model})
		writeJSON(w, http.StatusCreated, response)
		return
	}
	result.Plan.DegradationWarnings = append(result.Plan.DegradationWarnings, warnings...)
	response, statusErr := h.startPlannedPlaybackV3(r, userID, profileID, req, requestDigest, requestedFile, effectiveFile, audioIndex, result)
	if statusErr != nil {
		if statusErr.reason == "internal_error" {
			slog.ErrorContext(r.Context(), "protocol v3 start failed", "component", "api", "reason", statusErr.reason, "error", statusErr.cause)
		}
		writeJSON(w, http.StatusCreated, playback.NewTerminalResponseV3(statusErr.reason, statusErr.message, statusErr.retryable))
		return
	}
	writeJSON(w, http.StatusCreated, response)
}

func (h *PlaybackHandler) startPlannedPlaybackV3(r *http.Request, userID int, profileID string, req playback.StartRequestV3, requestDigest string, requestedFile, effectiveFile *models.MediaFile, audioIndex int, result playback.PlannerResultV3) (playback.DecisionResponseV3, *transportErrorV3) {
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
	transport, transportErr := h.prepareTransportV3(r, session, effectiveFile, result)
	if transportErr != nil {
		abort()
		return playback.DecisionResponseV3{}, transportErr
	}
	result.Plan.Stream.URL = transport.url
	if err := h.attachSubtitleArtifactV3(r.Context(), session.ID, effectiveFile, result.Plan, result.SubtitleTrackIndex); err != nil {
		transport.rollback()
		abort()
		return playback.DecisionResponseV3{}, &transportErrorV3{reason: "subtitle_artifact_unavailable", message: "Failed to prepare the selected subtitle artifact.", cause: err}
	}
	response := playback.DecisionResponseV3{ProtocolVersion: playback.ProtocolV3, ServerFeatures: []string{playback.FeaturePlaybackPlanV3, playback.FeatureMedia3Only, playback.FeatureRouteDiagnostics, playback.FeatureDeviceQuirksV3, playback.FeatureSeekReanchorV3}, Outcome: playback.OutcomePlayableV3, SessionID: session.ID, PlaybackPlan: result.Plan}
	record := playback.AttemptRecordV3{PlaybackAttemptID: req.PlaybackAttemptID, SessionID: session.ID, UserID: userID, ProfileID: profileID, RequestedMediaFileID: requestedFile.ID, EffectiveMediaFileID: effectiveFile.ID, CurrentPlanID: result.Plan.PlanID, CurrentPlan: *result.Plan, NormalizedRequest: req, RequestDigest: requestDigest, ExpiresAt: time.Now().Add(playback.MaxTokenTTL)}
	if err := h.updateV3SessionState(r.Context(), session, effectiveFile, result, transport); err != nil {
		transport.rollback()
		abort()
		return playback.DecisionResponseV3{}, &transportErrorV3{reason: "internal_error", message: "Failed to commit the live playback session.", cause: err}
	}
	if err := h.PlanStoreV3.SaveAttempt(r.Context(), record); err != nil {
		transport.rollback()
		abort()
		if errors.Is(err, playback.ErrIdempotencyKeyReusedV3) {
			return playback.DecisionResponseV3{}, &transportErrorV3{reason: "playback_attempt_reused", message: "The playback attempt ID was reused with different input."}
		}
		if errors.Is(err, playback.ErrPlaybackAttemptExistsV3) {
			existing, lookupErr := h.PlanStoreV3.GetAttemptByPlaybackAttemptID(r.Context(), req.PlaybackAttemptID)
			if lookupErr == nil && existing.UserID == userID && existing.ProfileID == profileID && existing.RequestedMediaFileID == req.FileID {
				// Replaying a concurrent duplicate is only valid while its
				// session is alive; otherwise tell the client to mint a new
				// attempt rather than hand it a plan it can never stream.
				if _, sessionErr := h.sessionMgr.GetSession(existing.SessionID); sessionErr != nil {
					return playback.DecisionResponseV3{}, &transportErrorV3{reason: "session_expired", message: "The playback session for this attempt has ended.", retryable: true}
				}
				return decisionResponseFromAttemptV3(existing), nil
			}
		}
		return playback.DecisionResponseV3{}, &transportErrorV3{reason: "internal_error", message: "Failed to persist the playback plan.", cause: err}
	}
	transport.commit()
	h.syncSessionsNow(r.Context(), "v3_start")
	h.enqueueRouteEventV3(playback.RouteEventRecordV3{RouteEventV3: playback.RouteEventV3{ProtocolVersion: playback.ProtocolV3, PlaybackAttemptID: req.PlaybackAttemptID, SessionID: session.ID, PlanID: result.Plan.PlanID, Event: playback.RouteEventPlanSelectedV3, AppliedQuirkIDs: appliedQuirkIDsV3(result.Plan), QuirkRegistryRevision: appliedQuirkRevisionV3(result.Plan), OutputRouteGeneration: req.OutputRouteGeneration}, UserID: userID, ProfileID: profileID, ClientName: clientInfo.Name, ClientVersion: clientInfo.Version, ClientModel: req.ClientPlaybackContext.Device.Model})
	return response, nil
}

func (h *PlaybackHandler) prepareTransportV3(r *http.Request, session *playback.Session, file *models.MediaFile, result playback.PlannerResultV3) (preparedTransportV3, *transportErrorV3) {
	if result.Plan.Delivery != playback.DeliveryTranscodeHLSV3 && result.Plan.Delivery != playback.DeliveryRemuxHLSV3 {
		return h.prepareIdentityTransportV3(session, result), nil
	}
	if h.NodePlanner != nil {
		plan := h.NodePlanner.PlanSession(session.ID, session.TranscodeNodeURL, true, result.TargetBitrateKbps)
		if plan.TranscodeNode != nil {
			transformations, err := h.remoteTransformationsV3(r.Context(), plan.TranscodeNode.URL)
			if err == nil {
				err = validateRemoteTransformationsV3(result.Plan, transformations)
			}
			if err == nil {
				transport, transportErr := h.prepareRemoteTransportV3(r, session, file, result, plan)
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
	return h.prepareLocalTransportV3(r, session, file, result)
}

func (h *PlaybackHandler) prepareIdentityTransportV3(session *playback.Session, result playback.PlannerResultV3) preparedTransportV3 {
	routeSession := *session
	routeSession.PlayMethod = result.PlayMethod
	routeSession.BasePlayMethod = result.PlayMethod
	routeSession.MediaFileID = result.Plan.EffectiveMediaFileID
	routeSession.AudioTrackIndex = plannedAudioTrackIndexV3(result, session.AudioTrackIndex)
	routeSession.TranscodeAudio = result.TranscodeAudio
	routeSession.RemuxDVMode = remuxDVModeForPlanV3(result.Plan)
	previousNodeURL := session.TranscodeNodeURL
	previousTransportID := remoteTransportID(session)
	unlock := h.tm.LockSessionLifecycle(session.ID)
	committed := false
	streamURL := h.playbackStreamURL(&routeSession)
	if result.Plan != nil && result.Plan.Delivery == playback.DeliveryRemuxProgressiveV3 {
		configureProgressiveRemuxTimelineV3(result.Plan)
		if seek := result.Plan.Timeline.StreamOriginSeconds; seek > 0 {
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

// A progressive remux is a freshly generated, chunked MP4 response and does
// not implement byte ranges. Its player clock therefore begins at zero at the
// requested source origin, and arbitrary seeks must request another server
// reanchor rather than issuing a Range request against the remux pipe.
func configureProgressiveRemuxTimelineV3(plan *playback.PlanV3) {
	if plan == nil {
		return
	}
	origin := plan.Timeline.SourceStartSeconds
	plan.Timeline.PlayerStartSeconds = 0
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

func (h *PlaybackHandler) prepareLocalTransportV3(r *http.Request, session *playback.Session, file *models.MediaFile, result playback.PlannerResultV3) (preparedTransportV3, *transportErrorV3) {
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
	seekSeconds, startSegment := configureHLSTimelineV3(result.Plan, videoCodec, 2, float64(file.Duration))
	unlock := h.tm.LockSessionLifecycle(session.ID)
	ts, err := h.startLocalPlaybackTransport(r.Context(), playback.TranscodeOpts{InputPath: file.FilePath, OutputDir: outputDir, OutputSubdir: outputSubdir, SessionID: session.ID, SourceVideoCodec: file.CodecVideo, VideoBitstreamFilter: videoBitstreamFilterForPlanV3(result.Plan), SeekSeconds: seekSeconds, StartSegmentNumber: startSegment, TargetResolution: result.TargetResolution, TargetCodecVideo: videoCodec, TargetCodecAudio: result.TargetAudioCodec, TargetBitrateKbps: result.TargetBitrateKbps, SegmentDuration: 2, FFmpegPath: cfg.FFmpegPath, HWAccel: cfg.HWAccel, HWDevice: cfg.HWDevice, AudioTrackIndex: plannedAudioTrackIndexV3(result, session.AudioTrackIndex), SubtitleTrackIndex: result.SubtitleTransportTrackIndex, SubtitleBurnIn: result.SubtitleBurnIn, SubtitleCodec: result.SubtitleCodec, TotalDuration: float64(file.Duration), FastStart: true, NodeType: "integrated", ExecutionMode: "integrated", FFmpegLogSink: h.FFmpegLogSink})
	if err != nil {
		unlock()
		return preparedTransportV3{}, &transportErrorV3{reason: "transcode_start_failed", message: "Failed to start the playback transport.", retryable: true, cause: err}
	}
	if !ts.IsRunning() {
		_ = ts.Close()
		unlock()
		return preparedTransportV3{}, &transportErrorV3{reason: "transcode_start_failed", message: "The playback transport exited during startup.", retryable: true}
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

func (h *PlaybackHandler) prepareRemoteTransportV3(r *http.Request, session *playback.Session, file *models.MediaFile, result playback.PlannerResultV3, nodePlan nodepool.Plan) (preparedTransportV3, *transportErrorV3) {
	node := nodePlan.TranscodeNode
	transportID := transportGenerationV3(session.ID, result.Plan.PlanID)
	videoCodec := result.TargetVideoCodec
	if result.Plan.Delivery == playback.DeliveryRemuxHLSV3 {
		videoCodec = "copy"
	}
	seekSeconds, startSegment := configureHLSTimelineV3(result.Plan, videoCodec, 2, float64(file.Duration))
	req := transcodenode.TranscodeStartRequest{SessionID: transportID, InputPath: file.FilePath, SourceVideoCodec: file.CodecVideo, VideoBitstreamFilter: videoBitstreamFilterForPlanV3(result.Plan), SeekSeconds: seekSeconds, StartSegmentNumber: startSegment, TargetResolution: result.TargetResolution, TargetCodecVideo: videoCodec, TargetCodecAudio: result.TargetAudioCodec, TargetBitrateKbps: result.TargetBitrateKbps, SegmentDuration: 2, HWAccel: h.playbackConfig().HWAccel, AudioTrackIndex: plannedAudioTrackIndexV3(result, session.AudioTrackIndex), SubtitleTrackIndex: result.SubtitleTransportTrackIndex, SubtitleBurnIn: result.SubtitleBurnIn, SubtitleCodec: result.SubtitleCodec, TotalDuration: float64(file.Duration), RequireReady: true}
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
		return preparedTransportV3{}, &transportErrorV3{reason: "transcode_start_failed", message: "The selected transcode node rejected the playback transport.", retryable: true}
	}
	hw := firstNonEmptyHandlerV3(strings.TrimSpace(nodeResp.HWAccel), strings.TrimSpace(req.HWAccel))
	card := playback.NewRecipeCard(session.UserID, session.ProfileID, file.ID, node.URL, playback.TranscodeOpts{InputPath: req.InputPath, SessionID: session.ID, TranscodeTransportID: transportID, SourceVideoCodec: req.SourceVideoCodec, VideoBitstreamFilter: req.VideoBitstreamFilter, SeekSeconds: req.SeekSeconds, StartSegmentNumber: req.StartSegmentNumber, TargetResolution: req.TargetResolution, TargetCodecVideo: req.TargetCodecVideo, TargetCodecAudio: req.TargetCodecAudio, TargetBitrateKbps: req.TargetBitrateKbps, SegmentDuration: req.SegmentDuration, HWAccel: hw, AudioTrackIndex: req.AudioTrackIndex, SubtitleTrackIndex: req.SubtitleTrackIndex, SubtitleBurnIn: req.SubtitleBurnIn, SubtitleCodec: req.SubtitleCodec, TotalDuration: req.TotalDuration})
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

func (h *PlaybackHandler) v3SessionStreamState(ctx context.Context, session *playback.Session, file *models.MediaFile, result playback.PlannerResultV3, transport preparedTransportV3) playback.SessionStreamState {
	state := playback.SessionStreamState{PlayMethod: result.PlayMethod, BasePlayMethod: result.PlayMethod, AudioTrackIndex: plannedAudioTrackIndexV3(result, session.AudioTrackIndex), TranscodeAudio: result.TranscodeAudio, RemuxDVMode: remuxDVModeForPlanV3(result.Plan), TranscodeNodeURL: transport.nodeURL, TranscodeTransportID: transport.transportID, TranscodeRouteSet: true, ClientIP: clientip.FromContext(ctx), ClientName: session.ClientName, ClientVersion: session.ClientVersion, ClientUserAgent: session.ClientUserAgent, StreamBitrateKbps: result.TargetBitrateKbps, TargetVideoCodec: result.TargetVideoCodec, TargetAudioCodec: result.TargetAudioCodec, TargetResolution: result.TargetResolution, SubtitleTrackIndex: result.SubtitleTransportTrackIndex, SubtitleBurnIn: result.SubtitleBurnIn}
	if result.Plan != nil && (result.Plan.Delivery == playback.DeliveryTranscodeHLSV3 || result.Plan.Delivery == playback.DeliveryRemuxHLSV3) {
		state.SegmentDuration = 2
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

func (h *PlaybackHandler) attachSubtitleArtifactV3(ctx context.Context, sessionID string, file *models.MediaFile, plan *playback.PlanV3, selectedIndex int) error {
	if plan == nil || file == nil || selectedIndex < 0 || (plan.Subtitle.Mode != playback.SubtitleRenderV3 && plan.Subtitle.Mode != playback.SubtitleConvertV3) {
		return nil
	}
	var downloaded []subtitles.DownloadedSubtitle
	if h.SubtitleRepo != nil {
		var err error
		downloaded, err = h.SubtitleRepo.ListDownloadedSubtitles(ctx, file.ID)
		if err != nil {
			return err
		}
	}
	for _, value := range buildSubtitleURLs(sessionID, file, downloaded, true) {
		if value.Index != selectedIndex {
			continue
		}
		format := strings.ToLower(value.Codec)
		mime := subtitleMIMEV3(format)
		url := value.URL
		if plan.Subtitle.Mode == playback.SubtitleConvertV3 {
			format = "vtt"
			mime = "text/vtt"
			url = forceSubtitleExtensionV3(value.URL, ".vtt")
		}
		plan.Subtitle.Artifact = &playback.SubtitleArtifactV3{URL: url, MIMEType: mime, Format: format, TimingOriginSeconds: plan.Timeline.StreamOriginSeconds}
		return nil
	}
	return errors.New("selected subtitle artifact is absent from the frozen inventory")
}

func (h *PlaybackHandler) downloadedSubtitleInventoryV3(ctx context.Context, file *models.MediaFile) []playback.SubtitleInventoryEntryV3 {
	if h == nil || h.SubtitleRepo == nil || file == nil {
		return nil
	}
	downloaded, err := h.SubtitleRepo.ListDownloadedSubtitles(ctx, file.ID)
	if err != nil {
		return nil
	}
	base := len(file.ExternalSubtitles) + len(file.SubtitleTracks)
	result := make([]playback.SubtitleInventoryEntryV3, 0, len(downloaded))
	for index, value := range downloaded {
		result = append(result, playback.SubtitleInventoryEntryV3{CombinedIndex: base + index, Codec: string(value.Format), Source: "downloaded"})
	}
	return result
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
	if !h.protocolV3Enabled(r.Context()) {
		writeJSON(w, http.StatusOK, playback.DisabledResponseV3())
		return
	}
	body, err := readBoundedV3Body(w, r, maxPlaybackV3BodyBytes)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	var req playback.ReplanRequestV3
	if err := json.Unmarshal(body, &req); err != nil || req.Validate() != nil {
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
	if record.CurrentPlanID != req.FailedPlanID {
		writeError(w, http.StatusConflict, "stale_playback_plan", "The failed plan is no longer current")
		return
	}
	response, updated, transport, replanErr := h.executeReplanV3(r, record, req)
	if replanErr != nil {
		response = playback.NewTerminalResponseV3(replanErr.reason, replanErr.message, replanErr.retryable)
		updated = *record
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
	if err := h.PlanStoreV3.CompleteReplan(r.Context(), sessionID, req.ReplanRequestID, record.CurrentReplanRequestID, encoded, updated); err != nil {
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
		switch req.Failure.Classification {
		case "quality_changed":
			nextQuality, _ := playback.NormalizeQualityV3(req.QualityPreference)
			intentChange = nextQuality != start.QualityPreference
		case "audio_track_changed":
			intentChange = req.SelectedTracks.Audio != nil &&
				(req.SelectedTracks.Audio.ID != start.AudioTrackID || !optionalIntEqualV3(req.SelectedTracks.Audio.Index, start.AudioTrackIndex))
		case "subtitle_track_changed":
			intentChange = req.SelectedTracks.Subtitle == nil && start.SubtitleTrackIndex != nil ||
				req.SelectedTracks.Subtitle != nil &&
					(req.SelectedTracks.Subtitle.ID != start.SubtitleTrackID || !optionalIntEqualV3(req.SelectedTracks.Subtitle.Index, start.SubtitleTrackIndex))
		case "output_route_changed":
			intentChange = req.OutputRouteGeneration != start.OutputRouteGeneration
		}
		// Failure replans use the current effective file. Explicit user/output
		// intent changes restart source selection from the requested edition.
		start.FileID = record.EffectiveMediaFileID
		if intentChange {
			start.FileID = record.RequestedMediaFileID
		}
		start.QualityPreference = req.QualityPreference
		start.StartPosition = &req.PositionSeconds
		start.OutputRouteGeneration = req.OutputRouteGeneration
		start.Metered = req.Metered
		start.BandwidthEstimateKbps = copyOptionalIntV3(req.BandwidthEstimateKbps)
		start.BandwidthCapKbps = copyOptionalIntV3(req.BandwidthCapKbps)
		start.Capabilities = req.Capabilities
		start.ClientPlaybackContext = req.ClientPlaybackContext
		applySelectedTracksToStartV3(&start, req.SelectedTracks)
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
	if intentChange {
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
			if err := remapAudioSelectionV3(currentEffectiveFile, effectiveFile, &start); err != nil {
				return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{reason: "track_unavailable", message: err.Error()}
			}
			if start.SubtitleTrackIndex != nil || start.SubtitleTrackID != "" {
				if err := h.remapSubtitleSelectionV3(r.Context(), currentEffectiveFile, effectiveFile, &start); err != nil {
					return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{reason: "track_unavailable", message: err.Error()}
				}
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
	if seekScopedRecovery && effectiveFile.Duration > 0 && req.PositionSeconds > float64(effectiveFile.Duration) {
		return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{
			reason:  "invalid_seek_position",
			message: "The requested seek position is beyond the end of the selected media source.",
		}
	}
	if _, err := start.NormalizeAndValidate(); err != nil {
		return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{reason: "invalid_replan", message: err.Error()}
	}
	audioIndex, err := resolveV3AudioIndex(effectiveFile, start.AudioTrackID, start.AudioTrackIndex)
	if err != nil {
		return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{reason: "track_unavailable", message: err.Error()}
	}
	attemptedKeys := []string(nil)
	if !intentChange && !seekReanchor {
		attemptedKeys = append(attemptedKeys, req.AttemptedPlanKeys...)
		if !containsStringExactV3(attemptedKeys, req.PlanAttemptKey) {
			attemptedKeys = append(attemptedKeys, req.PlanAttemptKey)
		}
	}
	if !seekReanchor && (!intentChange || seekFailureRecovery) {
		// Client attempt keys may include local mutations that the server cannot
		// reproduce. Always exclude the durable server recipe as well so stale or
		// malformed client history cannot immediately re-select the route that
		// just failed and ping-pong the session.
		currentKey := playback.PlanAttemptKeyV3(record.CurrentPlan, record.NormalizedRequest.OutputRouteGeneration, nil)
		if !containsStringExactV3(attemptedKeys, currentKey) {
			attemptedKeys = append(attemptedKeys, currentKey)
		}
	}
	result := playback.PlanPlaybackV3(playback.PlannerInputV3{Request: start, RequestedFile: plannerRequestedFile, EffectiveFile: effectiveFile, AudioTrackIndex: audioIndex, Settings: h.plannerSettingsV3(r.Context()), Registry: h.transformationRegistryV3(r.Context()), Now: time.Now(), AttemptedKeys: attemptedKeys, AdditionalSubtitles: h.downloadedSubtitleInventoryV3(r.Context(), effectiveFile)})
	if result.Terminal != nil && result.Terminal.Reason == "no_alternate_version" && replanAllowsAlternateFileV3(operation, start.QualityPreference) {
		if alternate, alternateErr := h.findAlternateFile(r.Context(), requestedFile); alternateErr == nil && alternate != nil {
			alternate = h.ensurePlaybackProbe(r.Context(), alternate)
			remappedAudio := remapAudioIndexV3(effectiveFile, alternate, audioIndex)
			if err := h.remapSubtitleSelectionV3(r.Context(), effectiveFile, alternate, &start); err == nil {
				start.FileID = alternate.ID
				if err := preflightPlaybackFile(r.Context(), alternate, h.MissingMarker, h.EventsHub); err == nil {
					effectiveFile = alternate
					audioIndex = remappedAudio
					result = playback.PlanPlaybackV3(playback.PlannerInputV3{Request: start, RequestedFile: plannerRequestedFile, EffectiveFile: effectiveFile, AudioTrackIndex: audioIndex, Settings: h.plannerSettingsV3(r.Context()), Registry: h.transformationRegistryV3(r.Context()), Now: time.Now(), AttemptedKeys: attemptedKeys, AdditionalSubtitles: h.downloadedSubtitleInventoryV3(r.Context(), effectiveFile)})
				}
			}
		}
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
	transport, transportErr := h.prepareTransportV3(r, session, effectiveFile, result)
	if transportErr != nil {
		return playback.DecisionResponseV3{}, *record, nil, transportErr
	}
	result.Plan.Stream.URL = transport.url
	if err := h.attachSubtitleArtifactV3(r.Context(), session.ID, effectiveFile, result.Plan, result.SubtitleTrackIndex); err != nil {
		transport.rollback()
		return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{reason: "subtitle_artifact_unavailable", message: "Failed to prepare the selected subtitle artifact.", cause: err}
	}
	if seekReanchor {
		if err := validateSeekReanchorPlanV3(record, result.Plan); err != nil {
			transport.rollback()
			return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{
				reason:  "seek_reanchor_route_changed",
				message: err.Error(),
			}
		}
	}
	response := playback.DecisionResponseV3{ProtocolVersion: playback.ProtocolV3, ServerFeatures: []string{playback.FeaturePlaybackPlanV3, playback.FeatureMedia3Only, playback.FeatureRouteDiagnostics, playback.FeatureDeviceQuirksV3, playback.FeatureSeekReanchorV3}, Outcome: playback.OutcomePlayableV3, SessionID: session.ID, PlaybackPlan: result.Plan}
	updated := *record
	updated.CurrentPlanID = result.Plan.PlanID
	updated.CurrentPlan = *result.Plan
	updated.NormalizedRequest = start
	updated.EffectiveMediaFileID = effectiveFile.ID
	updated.ExpiresAt = time.Now().Add(playback.MaxTokenTTL)
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
		h.syncSessionsNow(r.Context(), "v3_replan")
		event := playback.RouteEventPlanSelectedV3
		clientModel := req.ClientPlaybackContext.Device.Model
		if seekReanchor {
			event = playback.RouteEventRuntimeCorrectionSucceededV3
			clientModel = start.ClientPlaybackContext.Device.Model
		}
		h.enqueueRouteEventV3(playback.RouteEventRecordV3{RouteEventV3: playback.RouteEventV3{ProtocolVersion: playback.ProtocolV3, PlaybackAttemptID: req.PlaybackAttemptID, SessionID: session.ID, PlanID: result.Plan.PlanID, PlanAttemptID: req.PlanAttemptID, PlanAttemptKey: playback.PlanAttemptKeyV3(*result.Plan, start.OutputRouteGeneration, nil), Event: event, FallbackReason: req.Failure.Classification, AppliedQuirkIDs: appliedQuirkIDsV3(result.Plan), QuirkRegistryRevision: appliedQuirkRevisionV3(result.Plan), OutputRouteGeneration: start.OutputRouteGeneration}, UserID: session.UserID, ProfileID: session.ProfileID, ClientName: session.ClientName, ClientVersion: session.ClientVersion, ClientModel: clientModel})
	}
	transport.rollback = func() {
		originalRollback()
		cancelReservation()
	}
	reservationHandedOff = true
	return response, updated, &transport, nil
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
	if req.OutputRouteGeneration != record.NormalizedRequest.OutputRouteGeneration {
		return errors.New("seek recovery cannot change the output route")
	}
	if !sameSelectedTracksV3(req.SelectedTracks, record.CurrentPlan.SelectedTracks) {
		return errors.New("seek recovery cannot change selected tracks")
	}
	return nil
}

func validateSeekReanchorPlanV3(record *playback.AttemptRecordV3, candidate *playback.PlanV3) error {
	if record == nil || candidate == nil {
		return errors.New("seek reanchor produced no playback route")
	}
	current := record.CurrentPlan
	if candidate.PlanID != record.CurrentPlanID || candidate.PlanID != current.PlanID {
		return errors.New("seek reanchor changed the playback plan identity")
	}
	if candidate.RequestedMediaFileID != record.RequestedMediaFileID || candidate.EffectiveMediaFileID != record.EffectiveMediaFileID {
		return errors.New("seek reanchor changed the selected media version")
	}
	if !sameSelectedTracksV3(candidate.SelectedTracks, current.SelectedTracks) {
		return errors.New("seek reanchor changed selected tracks")
	}
	if candidate.Engine != current.Engine ||
		candidate.Stream.MIMEType != current.Stream.MIMEType ||
		candidate.Stream.HeaderRefresh != current.Stream.HeaderRefresh ||
		!sameEffectiveRecipeV3(candidate.EffectiveRecipe, current.EffectiveRecipe) ||
		candidate.Claims != current.Claims ||
		candidate.Subtitle.Mode != current.Subtitle.Mode ||
		candidate.Subtitle.TrackID != current.Subtitle.TrackID ||
		!sameSubtitleArtifactRouteV3(candidate.Subtitle.Artifact, current.Subtitle.Artifact) ||
		candidate.SubtitleFidelityPolicy != current.SubtitleFidelityPolicy ||
		!sameTransformationsV3(candidate.Transformations, current.Transformations) ||
		!sameAppliedQuirksV3(candidate.AppliedQuirks, current.AppliedQuirks) ||
		!sameStringMultisetV3(candidate.RuntimeCorrections, current.RuntimeCorrections) {
		return errors.New("seek reanchor changed the playback route semantics")
	}
	generation := record.NormalizedRequest.OutputRouteGeneration
	currentKey := playback.PlanAttemptKeyV3(current, generation, nil)
	candidateKey := playback.PlanAttemptKeyV3(*candidate, generation, nil)
	if candidateKey != currentKey {
		return errors.New("seek reanchor changed the playback route recipe")
	}
	return nil
}

func sameSubtitleArtifactRouteV3(left, right *playback.SubtitleArtifactV3) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	// Signed URLs and timing origins are allowed to rotate when a transport is
	// reopened; the player-facing artifact representation is not.
	return left.MIMEType == right.MIMEType && left.Format == right.Format
}

func sameEffectiveRecipeV3(left, right playback.EffectiveRecipeV3) bool {
	return left.VideoCodec == right.VideoCodec &&
		left.AudioCodec == right.AudioCodec &&
		optionalIntEqualV3(left.Width, right.Width) &&
		optionalIntEqualV3(left.Height, right.Height) &&
		optionalFloatEqualV3(left.FrameRate, right.FrameRate) &&
		optionalIntEqualV3(left.BitrateKbps, right.BitrateKbps) &&
		left.DynamicRange == right.DynamicRange &&
		optionalIntEqualV3(left.AudioChannels, right.AudioChannels) &&
		left.AudioLayout == right.AudioLayout
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

func replanAllowsAlternateFileV3(operation playback.ReplanOperationV3, qualityPreference string) bool {
	return operation == playback.ReplanOperationFailureRecoveryV3 && shouldTryAlternateFileV3(qualityPreference)
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
	if !h.protocolV3Enabled(r.Context()) {
		writeError(w, http.StatusConflict, "protocol_disabled", "Playback protocol v3 is disabled")
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
		(event.SessionID != "" && identity.PlaybackAttemptID != event.PlaybackAttemptID) {
		writeError(w, http.StatusForbidden, "forbidden", "Route event does not belong to this profile")
		return
	}
	event.Diagnostics = sanitizeDiagnosticsV3(event.Diagnostics)
	client := playbackClientInfoFromRequest(r)
	h.enqueueRouteEventV3(playback.RouteEventRecordV3{RouteEventV3: event, UserID: userID, ProfileID: profileID, ClientName: client.Name, ClientVersion: client.Version, ClientModel: event.Diagnostics["device_model"]})
	w.WriteHeader(http.StatusAccepted)
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

func decisionResponseFromAttemptV3(record *playback.AttemptRecordV3) playback.DecisionResponseV3 {
	if record == nil {
		return playback.DecisionResponseV3{}
	}
	plan := record.CurrentPlan
	if plan.AppliedQuirks == nil {
		plan.AppliedQuirks = []playback.AppliedQuirkV3{}
	}
	if plan.RuntimeCorrections == nil {
		plan.RuntimeCorrections = []string{}
	}
	return playback.DecisionResponseV3{ProtocolVersion: playback.ProtocolV3, ServerFeatures: []string{playback.FeaturePlaybackPlanV3, playback.FeatureMedia3Only, playback.FeatureRouteDiagnostics, playback.FeatureDeviceQuirksV3, playback.FeatureSeekReanchorV3}, Outcome: playback.OutcomePlayableV3, SessionID: record.SessionID, PlaybackPlan: &plan}
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
		return "text/vtt"
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
		if transformation.Name == "server_dv7_to_hdr10" {
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
		if transformation.Executor == "server" && transformation.Name == "server_dv7_to_hdr10" && transformation.RecipeVersion == "1" {
			return playback.DV7ToHDR10BitstreamFilter
		}
	}
	return ""
}

func configureHLSTimelineV3(plan *playback.PlanV3, videoCodec string, segmentDuration int, durationSeconds float64) (float64, int) {
	if plan == nil {
		return 0, 0
	}
	requested := plan.Timeline.SourceStartSeconds
	seek := alignedSeekSeconds(requested, segmentDuration, videoCodec)
	startSegment := computeStartSegment(seek, segmentDuration)
	plan.Timeline.SourceStartSeconds = requested
	if strings.EqualFold(videoCodec, "copy") {
		plan.Timeline.PlayerStartSeconds = 0
		plan.Timeline.StreamOriginSeconds = seek
		plan.Timeline.TimelineOffsetSeconds = seek
		windowStart := seek
		plan.Timeline.SeekWindowStartSeconds = &windowStart
		if durationSeconds > 0 {
			windowEnd := durationSeconds
			plan.Timeline.SeekWindowEndSeconds = &windowEnd
		} else {
			plan.Timeline.SeekWindowEndSeconds = nil
		}
		plan.Timeline.CanSeekAnywhere = false
		plan.Timeline.SeekRestoration = "source_position"
	} else {
		plan.Timeline.PlayerStartSeconds = requested
		plan.Timeline.StreamOriginSeconds = 0
		plan.Timeline.TimelineOffsetSeconds = 0
		plan.Timeline.SeekWindowStartSeconds = nil
		plan.Timeline.SeekWindowEndSeconds = nil
		plan.Timeline.CanSeekAnywhere = durationSeconds > 0
		plan.Timeline.SeekRestoration = "player_position"
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
	if event.ProtocolVersion != playback.ProtocolV3 || len(event.PlaybackAttemptID) < 8 || len(event.PlaybackAttemptID) > 128 || event.OutputRouteGeneration < 0 || len(event.SessionID) > 128 || len(event.PlanID) > 128 || len(event.PlanAttemptID) > 128 || len(event.PlanAttemptKey) > 128 || len(event.FailureClassification) > 64 || len(event.FallbackReason) > 64 || len(event.AppliedQuirkIDs) > 16 || len(event.QuirkRegistryRevision) > 128 || len(event.Diagnostics) > 32 {
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
