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
)

type preparedTransportV3 struct {
	url         string
	nodeURL     string
	transportID string
	commit      func()
	rollback    func()
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

func (e *transportErrorV3) Error() string {
	if e.cause != nil {
		return e.reason + ": " + e.cause.Error()
	}
	return e.reason
}

func (h *PlaybackHandler) protocolV3Enabled(ctx context.Context) bool {
	if h == nil || h.SettingsRepo == nil {
		return false
	}
	value, err := h.SettingsRepo.Get(ctx, "playback.protocol_v3_enabled")
	return err == nil && strings.EqualFold(strings.TrimSpace(value), "true")
}

func (h *PlaybackHandler) transformationRegistryV3(ctx context.Context) *playback.TransformationRegistryV3 {
	h.v3RegistryOnce.Do(func() {
		h.v3Registry = playback.ProbeTransformationRegistryV3(context.WithoutCancel(ctx), h.playbackConfig().FFmpegPath)
	})
	return h.v3Registry
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
	response.Features = []string{playback.FeaturePlaybackPlanV3, playback.FeatureMedia3Only, playback.FeatureDetailedDecodeV3, playback.FeatureLayoutPassthrough, playback.FeatureRouteDiagnostics}
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
	if existing, lookupErr := h.PlanStoreV3.GetAttemptByPlaybackAttemptID(r.Context(), req.PlaybackAttemptID); lookupErr == nil {
		if existing.UserID != userID || existing.ProfileID != profileID || existing.RequestedMediaFileID != req.FileID {
			writeError(w, http.StatusConflict, "playback_attempt_reused", "The playback attempt ID belongs to a different request")
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
		h.enqueueRouteEventV3(playback.RouteEventRecordV3{RouteEventV3: playback.RouteEventV3{ProtocolVersion: playback.ProtocolV3, PlaybackAttemptID: req.PlaybackAttemptID, Event: "terminal", FallbackReason: result.Terminal.Reason, OutputRouteGeneration: req.OutputRouteGeneration}, UserID: userID, ProfileID: profileID, ClientName: playbackClientInfoFromRequest(r).Name, ClientVersion: playbackClientInfoFromRequest(r).Version, ClientModel: req.ClientPlaybackContext.Device.Model})
		writeJSON(w, http.StatusCreated, response)
		return
	}
	result.Plan.DegradationWarnings = append(result.Plan.DegradationWarnings, warnings...)
	response, statusErr := h.startPlannedPlaybackV3(r, userID, profileID, req, requestedFile, effectiveFile, audioIndex, result)
	if statusErr != nil {
		if statusErr.reason == "internal_error" {
			slog.ErrorContext(r.Context(), "protocol v3 start failed", "component", "api", "reason", statusErr.reason, "error", statusErr.cause)
		}
		writeJSON(w, http.StatusCreated, playback.NewTerminalResponseV3(statusErr.reason, statusErr.message, statusErr.retryable))
		return
	}
	writeJSON(w, http.StatusCreated, response)
}

func (h *PlaybackHandler) startPlannedPlaybackV3(r *http.Request, userID int, profileID string, req playback.StartRequestV3, requestedFile, effectiveFile *models.MediaFile, audioIndex int, result playback.PlannerResultV3) (playback.DecisionResponseV3, *transportErrorV3) {
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
	response := playback.DecisionResponseV3{ProtocolVersion: playback.ProtocolV3, ServerFeatures: []string{playback.FeaturePlaybackPlanV3, playback.FeatureMedia3Only, playback.FeatureRouteDiagnostics}, Outcome: playback.OutcomePlayableV3, SessionID: session.ID, PlaybackPlan: result.Plan}
	record := playback.AttemptRecordV3{PlaybackAttemptID: req.PlaybackAttemptID, SessionID: session.ID, UserID: userID, ProfileID: profileID, RequestedMediaFileID: requestedFile.ID, EffectiveMediaFileID: effectiveFile.ID, CurrentPlanID: result.Plan.PlanID, CurrentPlan: *result.Plan, NormalizedRequest: req, ExpiresAt: time.Now().Add(playback.MaxTokenTTL)}
	if err := h.PlanStoreV3.SaveAttempt(r.Context(), record); err != nil {
		transport.rollback()
		abort()
		if errors.Is(err, playback.ErrPlaybackAttemptExistsV3) {
			existing, lookupErr := h.PlanStoreV3.GetAttemptByPlaybackAttemptID(r.Context(), req.PlaybackAttemptID)
			if lookupErr == nil && existing.UserID == userID && existing.ProfileID == profileID && existing.RequestedMediaFileID == req.FileID {
				return decisionResponseFromAttemptV3(existing), nil
			}
		}
		return playback.DecisionResponseV3{}, &transportErrorV3{reason: "internal_error", message: "Failed to persist the playback plan.", cause: err}
	}
	transport.commit()
	h.updateV3SessionState(r.Context(), session, effectiveFile, result, transport)
	h.syncSessionsNow(r.Context(), "v3_start")
	h.enqueueRouteEventV3(playback.RouteEventRecordV3{RouteEventV3: playback.RouteEventV3{ProtocolVersion: playback.ProtocolV3, PlaybackAttemptID: req.PlaybackAttemptID, SessionID: session.ID, PlanID: result.Plan.PlanID, Event: "plan_selected", OutputRouteGeneration: req.OutputRouteGeneration}, UserID: userID, ProfileID: profileID, ClientName: clientInfo.Name, ClientVersion: clientInfo.Version, ClientModel: req.ClientPlaybackContext.Device.Model})
	return response, nil
}

func (h *PlaybackHandler) prepareTransportV3(r *http.Request, session *playback.Session, file *models.MediaFile, result playback.PlannerResultV3) (preparedTransportV3, *transportErrorV3) {
	if result.Plan.Delivery != playback.DeliveryTranscodeHLSV3 && result.Plan.Delivery != playback.DeliveryRemuxHLSV3 {
		return h.prepareIdentityTransportV3(session, result), nil
	}
	if h.NodePlanner != nil {
		plan := h.NodePlanner.PlanSession(session.ID, session.TranscodeNodeURL, true, result.TargetBitrateKbps)
		if plan.TranscodeNode != nil {
			return h.prepareRemoteTransportV3(r, session, file, result, plan)
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
	return preparedTransportV3{
		url: h.playbackStreamURL(&routeSession),
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
	ts, err := h.startLocalPlaybackTransport(r.Context(), playback.TranscodeOpts{InputPath: file.FilePath, OutputDir: outputDir, OutputSubdir: outputSubdir, SessionID: session.ID, SourceVideoCodec: file.CodecVideo, SeekSeconds: seekSeconds, StartSegmentNumber: startSegment, TargetResolution: result.TargetResolution, TargetCodecVideo: videoCodec, TargetCodecAudio: result.TargetAudioCodec, TargetBitrateKbps: result.TargetBitrateKbps, SegmentDuration: 2, FFmpegPath: cfg.FFmpegPath, HWAccel: cfg.HWAccel, HWDevice: cfg.HWDevice, AudioTrackIndex: plannedAudioTrackIndexV3(result, session.AudioTrackIndex), SubtitleTrackIndex: result.SubtitleTransportTrackIndex, SubtitleBurnIn: result.SubtitleBurnIn, SubtitleCodec: result.SubtitleCodec, TotalDuration: float64(file.Duration), FastStart: true, NodeType: "integrated", ExecutionMode: "integrated", FFmpegLogSink: h.FFmpegLogSink})
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
	req := transcodenode.TranscodeStartRequest{SessionID: transportID, InputPath: file.FilePath, SourceVideoCodec: file.CodecVideo, SeekSeconds: seekSeconds, StartSegmentNumber: startSegment, TargetResolution: result.TargetResolution, TargetCodecVideo: videoCodec, TargetCodecAudio: result.TargetAudioCodec, TargetBitrateKbps: result.TargetBitrateKbps, SegmentDuration: 2, HWAccel: h.playbackConfig().HWAccel, AudioTrackIndex: plannedAudioTrackIndexV3(result, session.AudioTrackIndex), SubtitleTrackIndex: result.SubtitleTransportTrackIndex, SubtitleBurnIn: result.SubtitleBurnIn, SubtitleCodec: result.SubtitleCodec, TotalDuration: float64(file.Duration)}
	nodeResp, status, err := h.startRemotePlaybackTransport(r.Context(), node.URL, req)
	if err != nil {
		return preparedTransportV3{}, &transportErrorV3{reason: "transcode_node_unavailable", message: "The selected transcode node is unavailable.", retryable: true, cause: err}
	}
	if status != http.StatusAccepted {
		return preparedTransportV3{}, &transportErrorV3{reason: "transcode_start_failed", message: "The selected transcode node rejected the playback transport.", retryable: true}
	}
	hw := firstNonEmptyHandlerV3(strings.TrimSpace(nodeResp.HWAccel), strings.TrimSpace(req.HWAccel))
	card := playback.NewRecipeCard(session.UserID, session.ProfileID, file.ID, node.URL, playback.TranscodeOpts{InputPath: req.InputPath, SessionID: session.ID, TranscodeTransportID: transportID, SourceVideoCodec: req.SourceVideoCodec, SeekSeconds: req.SeekSeconds, StartSegmentNumber: req.StartSegmentNumber, TargetResolution: req.TargetResolution, TargetCodecVideo: req.TargetCodecVideo, TargetCodecAudio: req.TargetCodecAudio, TargetBitrateKbps: req.TargetBitrateKbps, SegmentDuration: req.SegmentDuration, HWAccel: hw, AudioTrackIndex: req.AudioTrackIndex, SubtitleTrackIndex: req.SubtitleTrackIndex, SubtitleBurnIn: req.SubtitleBurnIn, SubtitleCodec: req.SubtitleCodec, TotalDuration: req.TotalDuration})
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
		unlock()
	}}, nil
}

func (h *PlaybackHandler) updateV3SessionState(ctx context.Context, session *playback.Session, file *models.MediaFile, result playback.PlannerResultV3, transport preparedTransportV3) {
	state := playback.SessionStreamState{PlayMethod: result.PlayMethod, BasePlayMethod: result.PlayMethod, AudioTrackIndex: plannedAudioTrackIndexV3(result, session.AudioTrackIndex), TranscodeAudio: result.TranscodeAudio, RemuxDVMode: remuxDVModeForPlanV3(result.Plan), TranscodeNodeURL: transport.nodeURL, TranscodeTransportID: transport.transportID, TranscodeRouteSet: true, ClientIP: clientip.FromContext(ctx), ClientName: session.ClientName, ClientVersion: session.ClientVersion, ClientUserAgent: session.ClientUserAgent, StreamBitrateKbps: result.TargetBitrateKbps, TargetVideoCodec: result.TargetVideoCodec, TargetAudioCodec: result.TargetAudioCodec, TargetResolution: result.TargetResolution, SubtitleTrackIndex: result.SubtitleTransportTrackIndex, SubtitleBurnIn: result.SubtitleBurnIn}
	if result.Plan != nil && (result.Plan.Delivery == playback.DeliveryTranscodeHLSV3 || result.Plan.Delivery == playback.DeliveryRemuxHLSV3) {
		state.SegmentDuration = 2
	}
	if state.StreamBitrateKbps <= 0 {
		state.StreamBitrateKbps = fileBitrateKbps(file)
	}
	if err := h.sessionMgr.UpdateStreamState(session.ID, state); err != nil {
		slog.WarnContext(ctx, "protocol v3 session state update failed", "session", session.ID, "error", err)
	}
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
	digestBytes := sha256.Sum256(body)
	digest := hex.EncodeToString(digestBytes[:])
	lease, err := h.PlanStoreV3.BeginReplan(r.Context(), sessionID, req.ReplanRequestID, digest, time.Now().Add(replanLeaseDurationV3))
	if errors.Is(err, playback.ErrIdempotencyKeyReusedV3) {
		writeError(w, http.StatusConflict, "idempotency_key_reused", "The replan request ID was reused with different input")
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
	encoded, _ := json.Marshal(response)
	if err := h.PlanStoreV3.CompleteReplan(r.Context(), sessionID, req.ReplanRequestID, encoded, updated); err != nil {
		if transport != nil {
			transport.rollback()
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to commit the replacement plan")
		return
	}
	if transport != nil {
		transport.commit()
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
	// Replan track identities are scoped to the currently effective file, which
	// may differ from the originally requested 4K version.
	start.FileID = record.EffectiveMediaFileID
	start.QualityPreference = req.QualityPreference
	start.StartPosition = &req.PositionSeconds
	start.OutputRouteGeneration = req.OutputRouteGeneration
	start.Capabilities = req.Capabilities
	start.ClientPlaybackContext = req.ClientPlaybackContext
	if req.SelectedTracks.Audio != nil {
		start.AudioTrackID = req.SelectedTracks.Audio.ID
		start.AudioTrackIndex = req.SelectedTracks.Audio.Index
	}
	if req.SelectedTracks.Subtitle != nil {
		start.SubtitleTrackID = req.SelectedTracks.Subtitle.ID
		start.SubtitleTrackIndex = req.SelectedTracks.Subtitle.Index
	} else {
		start.SubtitleTrackID = ""
		start.SubtitleTrackIndex = nil
	}
	_, err := start.NormalizeAndValidate()
	if err != nil {
		return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{reason: "invalid_replan", message: err.Error()}
	}
	requestedFile, err := h.loadFileByPreferredID(r.Context(), record.RequestedMediaFileID, record.EffectiveMediaFileID)
	if err != nil || requestedFile == nil {
		return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{reason: "source_unavailable", message: "The requested media source is unavailable."}
	}
	effectiveFile, err := h.loadFileByPreferredID(r.Context(), record.EffectiveMediaFileID, record.RequestedMediaFileID)
	if err != nil || effectiveFile == nil {
		return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{reason: "source_unavailable", message: "The effective media source is unavailable."}
	}
	audioIndex, err := resolveV3AudioIndex(effectiveFile, start.AudioTrackID, start.AudioTrackIndex)
	if err != nil {
		return playback.DecisionResponseV3{}, *record, nil, &transportErrorV3{reason: "track_unavailable", message: err.Error()}
	}
	attemptedKeys := append([]string(nil), req.AttemptedPlanKeys...)
	if !containsStringFoldV3(attemptedKeys, req.PlanAttemptKey) {
		attemptedKeys = append(attemptedKeys, req.PlanAttemptKey)
	}
	result := playback.PlanPlaybackV3(playback.PlannerInputV3{Request: start, RequestedFile: requestedFile, EffectiveFile: effectiveFile, AudioTrackIndex: audioIndex, Settings: h.plannerSettingsV3(r.Context()), Registry: h.transformationRegistryV3(r.Context()), Now: time.Now(), AttemptedKeys: attemptedKeys, AdditionalSubtitles: h.downloadedSubtitleInventoryV3(r.Context(), effectiveFile)})
	if result.Terminal != nil && result.Terminal.Reason == "no_alternate_version" && shouldTryAlternateFileV3(start.QualityPreference) {
		if alternate, alternateErr := h.findAlternateFile(r.Context(), requestedFile); alternateErr == nil && alternate != nil {
			alternate = h.ensurePlaybackProbe(r.Context(), alternate)
			remappedAudio := remapAudioIndexV3(effectiveFile, alternate, audioIndex)
			if err := h.remapSubtitleSelectionV3(r.Context(), effectiveFile, alternate, &start); err == nil {
				start.FileID = alternate.ID
				if err := preflightPlaybackFile(r.Context(), alternate, h.MissingMarker, h.EventsHub); err == nil {
					effectiveFile = alternate
					audioIndex = remappedAudio
					result = playback.PlanPlaybackV3(playback.PlannerInputV3{Request: start, RequestedFile: requestedFile, EffectiveFile: effectiveFile, AudioTrackIndex: audioIndex, Settings: h.plannerSettingsV3(r.Context()), Registry: h.transformationRegistryV3(r.Context()), Now: time.Now(), AttemptedKeys: attemptedKeys, AdditionalSubtitles: h.downloadedSubtitleInventoryV3(r.Context(), effectiveFile)})
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
	response := playback.DecisionResponseV3{ProtocolVersion: playback.ProtocolV3, ServerFeatures: []string{playback.FeaturePlaybackPlanV3, playback.FeatureMedia3Only, playback.FeatureRouteDiagnostics}, Outcome: playback.OutcomePlayableV3, SessionID: session.ID, PlaybackPlan: result.Plan}
	updated := *record
	updated.CurrentPlanID = result.Plan.PlanID
	updated.CurrentPlan = *result.Plan
	updated.NormalizedRequest = start
	updated.EffectiveMediaFileID = effectiveFile.ID
	updated.ExpiresAt = time.Now().Add(playback.MaxTokenTTL)
	originalCommit := transport.commit
	originalRollback := transport.rollback
	transport.commit = func() {
		originalCommit()
		_ = h.sessionMgr.SetEffectiveMediaFileID(session.ID, effectiveFile.ID)
		_ = h.sessionMgr.UpdateAudioTrack(session.ID, audioIndex, result.PlayMethod)
		h.updateV3SessionState(r.Context(), session, effectiveFile, result, transport)
		cancelReservation()
		h.syncSessionsNow(r.Context(), "v3_replan")
		h.enqueueRouteEventV3(playback.RouteEventRecordV3{RouteEventV3: playback.RouteEventV3{ProtocolVersion: playback.ProtocolV3, PlaybackAttemptID: req.PlaybackAttemptID, SessionID: session.ID, PlanID: result.Plan.PlanID, PlanAttemptID: req.PlanAttemptID, PlanAttemptKey: playback.PlanAttemptKeyV3(*result.Plan, req.OutputRouteGeneration, nil), Event: "plan_selected", FallbackReason: req.Failure.Classification, OutputRouteGeneration: req.OutputRouteGeneration}, UserID: session.UserID, ProfileID: session.ProfileID, ClientName: session.ClientName, ClientVersion: session.ClientVersion, ClientModel: req.ClientPlaybackContext.Device.Model})
	}
	transport.rollback = func() {
		originalRollback()
		cancelReservation()
	}
	reservationHandedOff = true
	return response, updated, &transport, nil
}

func shouldTryAlternateFileV3(qualityPreference string) bool {
	return !strings.EqualFold(strings.TrimSpace(qualityPreference), "original")
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
	if event.SessionID != "" {
		record, err := h.PlanStoreV3.GetAttempt(r.Context(), event.SessionID)
		if err != nil || record.UserID != userID || record.ProfileID != profileID || record.PlaybackAttemptID != event.PlaybackAttemptID {
			writeError(w, http.StatusForbidden, "forbidden", "Route event does not belong to this profile")
			return
		}
	} else {
		record, err := h.PlanStoreV3.GetAttemptByPlaybackAttemptID(r.Context(), event.PlaybackAttemptID)
		if err != nil || record.UserID != userID || record.ProfileID != profileID {
			writeError(w, http.StatusForbidden, "forbidden", "Route event does not belong to this profile")
			return
		}
	}
	if !h.allowRouteEventV3(userID, event.PlaybackAttemptID) {
		writeError(w, http.StatusTooManyRequests, "event_rate_limited", "Playback route event rate exceeded")
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

func (h *PlaybackHandler) remapSubtitleSelectionV3(ctx context.Context, source, target *models.MediaFile, request *playback.StartRequestV3) error {
	if request == nil || request.SubtitleTrackIndex == nil || source == nil || target == nil || source.ID == target.ID {
		return nil
	}
	index := *request.SubtitleTrackIndex
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
	return playback.DecisionResponseV3{ProtocolVersion: playback.ProtocolV3, ServerFeatures: []string{playback.FeaturePlaybackPlanV3, playback.FeatureMedia3Only, playback.FeatureRouteDiagnostics}, Outcome: playback.OutcomePlayableV3, SessionID: record.SessionID, PlaybackPlan: &plan}
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
		if transformation.Name == "dv_metadata_strip_to_hdr10" {
			return playback.RemuxDVStripToHDR10V3
		}
	}
	if plan.Source.DVProfile == 0 {
		return ""
	}
	if plan.Claims.Video.DolbyVision {
		return playback.RemuxDVPreserveV3
	}
	if plan.Source.DVProfile == 7 {
		return playback.RemuxDVRejectP7V3
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
		plan.Timeline.CanSeekAnywhere = false
		plan.Timeline.SeekRestoration = "source_position"
	} else {
		plan.Timeline.PlayerStartSeconds = requested
		plan.Timeline.StreamOriginSeconds = 0
		plan.Timeline.TimelineOffsetSeconds = 0
		plan.Timeline.CanSeekAnywhere = durationSeconds > 0
		plan.Timeline.SeekRestoration = "player_position"
	}
	return seek, startSegment
}

var routeEventsV3 = map[string]struct{}{"plan_selected": {}, "plan_invalidated": {}, "plan_failed": {}, "first_frame": {}, "terminal": {}, "stopped": {}}
var diagnosticKeysV3 = map[string]struct{}{
	"decoder_name": {}, "decoder_init_ms": {}, "first_frame_ms": {},
	"device_model": {}, "requested_quality": {}, "effective_quality": {},
	"pcm_recovery": {}, "retry_outcome": {}, "replan_request_id": {},
	"video_mime": {}, "video_codecs": {}, "video_width": {}, "video_height": {},
	"color_transfer": {}, "color_range": {},
	"error_code": {}, "error_code_name": {}, "error_cause": {},
}

func validRouteEventV3(event playback.RouteEventV3) bool {
	if event.ProtocolVersion != playback.ProtocolV3 || len(event.PlaybackAttemptID) < 8 || len(event.PlaybackAttemptID) > 128 || event.OutputRouteGeneration < 0 || len(event.SessionID) > 128 || len(event.PlanID) > 128 || len(event.PlanAttemptID) > 128 || len(event.PlanAttemptKey) > 128 || len(event.FailureClassification) > 64 || len(event.FallbackReason) > 64 || len(event.Diagnostics) > 32 {
		return false
	}
	_, ok := routeEventsV3[event.Event]
	return ok
}
func sanitizeDiagnosticsV3(values map[string]string) map[string]string {
	result := make(map[string]string)
	count := 0
	for key, value := range values {
		if count >= 16 {
			break
		}
		if _, ok := diagnosticKeysV3[key]; !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) > 256 {
			value = value[:256]
		}
		result[key] = value
		count++
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
