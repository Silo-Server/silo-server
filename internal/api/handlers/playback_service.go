package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/playback/connectionstore"
)

// Playback service seams for the v2 adapter (internal/apiv2/playback.go).
//
// Every operation here runs the same application logic as the frozen v1
// handlers; the seams exist so the typed v2 adapter can call it without an
// http.ResponseWriter. The v2 additions over v1 are the installation check,
// durable per-attempt sequencing of progress and stop on the attempt row
// (playback.ProgressStoreV3), and the stream deny marker written on stop.

// PlaybackCaller carries the authenticated identity and bounded client facts
// of one v2 playback request. InstallationID is the value the client read
// from capabilities; a mutation from a different installation is refused.
type PlaybackCaller struct {
	ConnectionDeviceID                                    string
	UserID                                                int
	ProfileID, InstallationID                             string
	DeviceID, DeviceName, Platform                        string
	UserAgent, RemoteAddr                                 string
	ClientName, ClientVersion, ClientBuild, ClientChannel string
	// DeclaredDevice is the device the client declared in its
	// X-Silo-Device-* headers. A successful start registers it in the
	// profile's device registry. It is separate from DeviceID, the playback
	// device named by the v2 X-Device-ID header.
	DeclaredDevice DeviceMetadata
	// LoginSessionID is the caller's login session ("" for an API key);
	// MayReachOtherDevices is asked before it acts on a stream another
	// login session started (mayActOnAttempt). Nil allows that.
	LoginSessionID       string
	MayReachOtherDevices func(context.Context) error
}

// PlaybackCapabilitiesView is the v2 capabilities body. State is always
// "available": there is no admission step in front of playback.
type PlaybackCapabilitiesView struct {
	InstallationID, Revision, State string
	Allowed                         bool
	ProtocolVersions                []int
	Features                        []string
	Deliveries                      []playback.DeliveryV3
	// The connection speed test's schedule, with connection_speed_v1. A zero
	// target means the server has no bitrates to set one from.
	BandwidthProbeTargetKbps    int
	BandwidthProbeAutomatic     bool
	BandwidthProbeMaxAgeSeconds int
}

const playbackCapabilityStateAvailable = "available"

// PlaybackOperationError is a typed application failure the v2 adapter maps
// to a problem; the v1 handlers write it with writePlaybackOperationError.
type PlaybackOperationError struct {
	Status        int
	Code, Message string
}

func (e *PlaybackOperationError) Error() string { return e.Message }
func playbackOperationError(status int, code, message string) *PlaybackOperationError {
	return &PlaybackOperationError{Status: status, Code: code, Message: message}
}
func playbackFileOperationError(err error) error {
	if errors.Is(err, catalog.ErrItemNotFound) || errors.Is(err, catalog.ErrEpisodeNotFound) {
		return playbackOperationError(http.StatusNotFound, "not_found", "Media file not found")
	}
	return playbackOperationError(http.StatusInternalServerError, "internal_error", "Failed to authorize media file")
}
func playbackPreflightOperationError(err error) error {
	if isPlaybackFileMissing(err) {
		return playbackOperationError(http.StatusNotFound, "not_found", "Source media file is missing")
	}
	return playbackOperationError(http.StatusInternalServerError, "internal_error", "Failed to access source media file")
}
func playbackPersistenceOperationError(err error) error {
	if errors.Is(err, playback.ErrIdempotencyKeyReusedV3) {
		return playbackOperationError(http.StatusConflict, "playback_attempt_reused", "The playback attempt ID belongs to a different request")
	}
	return playbackOperationError(http.StatusInternalServerError, "internal_error", "Failed to persist the playback decision")
}
func playbackSessionNotFoundOperationError() *PlaybackOperationError {
	return playbackOperationError(http.StatusNotFound, "session_not_found", "Playback session not found")
}
func playbackStoreOperationError() *PlaybackOperationError {
	return playbackOperationError(http.StatusServiceUnavailable, "unavailable", "Playback state is temporarily unavailable")
}

// PlaybackProgressCommand is one v2 progress sample. Sequence orders the
// samples of an attempt; a higher sequence wins even when position moves
// backward.
type PlaybackProgressCommand struct {
	Sequence int64   `json:"sequence"`
	Position float64 `json:"position"`
	IsPaused bool    `json:"is_paused"`
}

// PlaybackStopCommand is one v2 stop. StopID is the client-minted identity of
// the stop; the optional final sample (Sequence, Position, IsPaused) is applied
// before the stop writer runs when it is newer than the last progress.
type PlaybackStopCommand struct {
	StopID   string   `json:"stop_id"`
	Sequence int64    `json:"sequence"`
	Position *float64 `json:"position,omitempty"`
	IsPaused bool     `json:"is_paused"`
}

// PlaybackAcceptedProgress is the latest sample the attempt holds after a
// progress or stop call.
type PlaybackAcceptedProgress struct {
	Sequence int64   `json:"sequence"`
	Position float64 `json:"position"`
	IsPaused bool    `json:"is_paused"`
}

// PlaybackMutationView is the v2 progress/stop response body.
type PlaybackMutationView struct {
	Outcome   string                    `json:"outcome"`
	Accepted  *PlaybackAcceptedProgress `json:"accepted,omitempty"`
	StopID    string                    `json:"stop_id,omitempty"`
	HistoryID string                    `json:"history_id,omitempty"`
}

// PlaybackCodeProgressConflict is the error code for an equal sequence with a
// different sample; the v2 adapter maps it to its 409 problem type.
const PlaybackCodeProgressConflict = "progress_conflict"

// Mutation outcomes.
const (
	PlaybackOutcomeApplied     = playback.ProgressAppliedV3
	PlaybackOutcomeReplayed    = playback.ProgressReplayedV3
	PlaybackOutcomeStaleSample = playback.ProgressStaleSampleV3
	PlaybackOutcomeStopped     = "stopped"
)

// PlaybackReplanCommand is the typed v2 replan intent: the v3 wire body plus
// the digest of its canonical encoding, which fingerprints a reused request id.
type PlaybackReplanCommand struct {
	Request playback.ReplanRequestV3
	Digest  string
}

// PlaybackRouteEventCommand is the typed v2 route report: the v3 event plus
// the client-minted identity that makes a retry after a lost 202 a no-op.
type PlaybackRouteEventCommand struct {
	EventID string
	Event   playback.RouteEventV3
}

func (h *PlaybackHandler) validatePlaybackCaller(ctx context.Context, caller PlaybackCaller) error {
	if caller.UserID <= 0 || caller.UserID != apimw.GetUserID(ctx) || caller.ProfileID == "" || caller.ProfileID != apimw.GetProfileID(ctx) {
		return playbackOperationError(http.StatusForbidden, "forbidden", "Playback identity does not match the authenticated profile")
	}
	if h.InstallationID == "" {
		return playbackOperationError(http.StatusConflict, "capability_not_configured", "Playback installation identity is not configured")
	}
	if caller.InstallationID != h.InstallationID {
		return playbackOperationError(http.StatusConflict, "installation_changed", "Playback installation changed; refresh capabilities")
	}
	return nil
}

// PlaybackCapabilities is GET /api/v2/playback/capabilities. The installation
// id is diagnostics.ServerInstanceID, set on the handler at construction.
func (h *PlaybackHandler) PlaybackCapabilities(ctx context.Context, userID int, profileID string) (PlaybackCapabilitiesView, error) {
	view := PlaybackCapabilitiesView{State: playbackCapabilityStateAvailable, Allowed: true, ProtocolVersions: []int{playback.ProtocolV3}, Features: []string{}, Deliveries: []playback.DeliveryV3{}}
	if userID <= 0 || userID != apimw.GetUserID(ctx) || profileID == "" || profileID != apimw.GetProfileID(ctx) {
		return view, playbackOperationError(http.StatusForbidden, "forbidden", "Playback identity does not match the authenticated profile")
	}
	if h.InstallationID == "" {
		return view, playbackOperationError(http.StatusConflict, "capability_not_configured", "Playback installation identity is not configured")
	}
	view.InstallationID = h.InstallationID
	view.Features = append(playback.NativeServerFeaturesV3(), "sequenced_progress_v1", "fixed_media_file_v1", "marker_segments_v1", "trickplay_v1", "bandwidth_probe_v1", playback.FeatureConnectionSpeedV1)
	if h.WatchTogetherAvailable {
		view.Features = append(view.Features, "watch_party_source_fallback_v1", "watch_party_coordinator_v1")
	}
	view.Deliveries = []playback.DeliveryV3{playback.DeliveryOriginalHTTPV3, playback.DeliveryRemuxProgressiveV3, playback.DeliveryRemuxHLSV3}
	settings := h.playbackConfig()
	if settings.TranscodeEnabled {
		view.Deliveries = append(view.Deliveries, playback.DeliveryTranscodeHLSV3)
	}
	view.BandwidthProbeAutomatic = !settings.AutomaticBandwidthProbeOff
	view.BandwidthProbeMaxAgeSeconds = int(settings.BandwidthProbeMaxAge() / time.Second)
	view.BandwidthProbeTargetKbps = h.bandwidthProbeTargetKbps(ctx)
	capability, _ := json.Marshal(view) // This view contains only JSON-safe scalar values.
	digest := sha256.Sum256(capability)
	view.Revision = hex.EncodeToString(digest[:])
	return view, nil
}

// The application pipeline still uses private request-based routing helpers.
// This request contains only caller facts; it is never dispatched to an HTTP
// handler and never carries credentials, a body stream, or a response writer.
// nativeAPIV2ContextKey marks a personal-collection read that renders poster
// collages.
type nativeAPIV2ContextKey struct{}
type playbackConnectionDeviceContextKey struct{}

// playbackDeclaredDeviceContextKey carries PlaybackCaller.DeclaredDevice
// beside the request playbackCallerRequest builds.
type playbackDeclaredDeviceContextKey struct{}

// WithNativeAPIV2 marks a personal-collection read as one of the list
// operations that render poster collages (see collagesForRead).
func WithNativeAPIV2(ctx context.Context) context.Context {
	return context.WithValue(ctx, nativeAPIV2ContextKey{}, true)
}

func isNativeAPIV2(ctx context.Context) bool {
	native, _ := ctx.Value(nativeAPIV2ContextKey{}).(bool)
	return native
}

// attemptNegotiatedSubRipV3 reports whether an attempt negotiated original SRT.
// The SRT representation its current plan published decides when there is
// one. With no SRT published yet, the attempt negotiated it only if the client
// sent subrip_sidecar_v1 and a /api/v2 decision of this server offered it. The
// stored token alone is not enough: a server that predates the feature stored
// it verbatim for any client, on either surface, and never offered it.
func attemptNegotiatedSubRipV3(record *playback.AttemptRecordV3) bool {
	if published, original := playback.PublishedSubRipRepresentationV3(record.CurrentPlan.Subtitle.Inventory); published {
		return original
	}
	return playback.HasFeatureV3(record.NormalizedRequest.ClientFeatures, playback.FeatureSubripSidecarV3) &&
		playback.HasFeatureV3(record.StartResponse.ServerFeatures, playback.FeatureSubripSidecarV3)
}

// requireAttemptAPISurfaceV3 keeps an attempt on the SRT representation it
// negotiated: a start retried with subrip_sidecar_v1 must not replay an
// attempt negotiated without it, which would break the feature's promise.
// requested is the retried start's feature list; replans pass nil because a
// replan keeps the negotiated representation anyway.
func requireAttemptAPISurfaceV3(record *playback.AttemptRecordV3, requested []string) error {
	if record == nil {
		return nil
	}
	if !attemptNegotiatedSubRipV3(record) && playback.HasFeatureV3(requested, playback.FeatureSubripSidecarV3) {
		return playbackOperationError(http.StatusConflict, "playback_attempt_reused", "The playback attempt was negotiated without subrip_sidecar_v1")
	}
	return nil
}

// replanSubtitleFeaturesV3 returns the client features a replan attaches its
// subtitle artifact with: subrip_sidecar_v1 present exactly when the attempt
// negotiated original SRT. Every replan, not only a seek reanchor, keeps that
// representation for the attempt's lifetime.
func replanSubtitleFeaturesV3(record *playback.AttemptRecordV3, clientFeatures []string) []string {
	if record == nil {
		return clientFeatures
	}
	features := playback.WithoutFeatureV3(clientFeatures, playback.FeatureSubripSidecarV3)
	if attemptNegotiatedSubRipV3(record) {
		features = append(features, playback.FeatureSubripSidecarV3)
	}
	return features
}

// withNativeServerFeaturesV3 advertises the /api/v2-only features on a
// decision the shared start/replan application produced.
func withNativeServerFeaturesV3(response playback.DecisionResponseV3) playback.DecisionResponseV3 {
	if len(response.ServerFeatures) > 0 {
		response.ServerFeatures = playback.NativeServerFeaturesV3()
	}
	return response
}

func playbackCallerRequest(ctx context.Context, caller PlaybackCaller) *http.Request {
	headers := make(http.Header)
	ctx = context.WithValue(ctx, playbackConnectionDeviceContextKey{}, caller.ConnectionDeviceID)
	ctx = context.WithValue(ctx, playbackDeclaredDeviceContextKey{}, caller.DeclaredDevice)
	headers.Set(deviceIDHeader, caller.DeviceID)
	headers.Set(deviceNameHeader, caller.DeviceName)
	headers.Set(devicePlatformHeader, caller.Platform)
	headers.Set("User-Agent", caller.UserAgent)
	headers.Set("X-Silo-Client", caller.ClientName)
	headers.Set("X-Silo-Client-Version", caller.ClientVersion)
	headers.Set("X-Silo-Client-Build", caller.ClientBuild)
	headers.Set("X-Silo-Client-Channel", caller.ClientChannel)
	return (&http.Request{Header: headers, RemoteAddr: caller.RemoteAddr, URL: &url.URL{}}).WithContext(ctx)
}

// playbackCallerSessionRequest is playbackCallerRequest with the routed
// session id, for application seams that read chi.URLParam.
func playbackCallerSessionRequest(ctx context.Context, caller PlaybackCaller, sessionID string) *http.Request {
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("session_id", sessionID)
	return playbackCallerRequest(context.WithValue(ctx, chi.RouteCtxKey, routeCtx), caller)
}

// StartPlaybackV2 is POST /api/v2/playback/start: the v1 start application
// (idempotent on playback_attempt_id + request digest) behind the installation
// check.
func (h *PlaybackHandler) StartPlaybackV2(ctx context.Context, caller PlaybackCaller, request playback.StartRequestV3) (playback.DecisionResponseV3, error) {
	if err := h.validatePlaybackCaller(ctx, caller); err != nil {
		return playback.DecisionResponseV3{}, err
	}
	body, err := json.Marshal(request)
	if err != nil {
		return playback.DecisionResponseV3{}, playbackOperationError(http.StatusBadRequest, "bad_request", "Invalid playback request")
	}
	response, err := h.startPlaybackApplicationV3(playbackCallerRequest(ctx, caller), body)
	if err == nil && response.Outcome == playback.OutcomePlayableV3 {
		h.recordStartingDevice(ctx, caller.UserID, caller.ProfileID, caller.DeclaredDevice)
	}
	return withNativeServerFeaturesV3(response), err
}

// ApplyProgressV2 is POST /api/v2/playback/{session_id}/progress. The sample
// is sequenced durably on the attempt row (compare-and-set on last_sequence);
// an applied sample is then persisted through the v1 progress writer, using
// the in-memory session when this replica holds it and a session synthesized
// from the attempt row otherwise. Progress never requires the in-memory
// session to exist.
func (h *PlaybackHandler) ApplyProgressV2(ctx context.Context, caller PlaybackCaller, sessionID string, command PlaybackProgressCommand) (PlaybackMutationView, error) {
	if err := h.validatePlaybackCaller(ctx, caller); err != nil {
		return PlaybackMutationView{}, err
	}
	if err := validatePlaybackSessionID(sessionID); err != nil {
		return PlaybackMutationView{}, err
	}
	if command.Sequence <= 0 || command.Position < 0 || math.IsNaN(command.Position) || math.IsInf(command.Position, 0) {
		return PlaybackMutationView{}, playbackOperationError(http.StatusBadRequest, "bad_request", "A positive progress sequence and a finite position are required")
	}
	store, ok := h.PlanStoreV3.(playback.ProgressStoreV3)
	if !ok {
		return PlaybackMutationView{}, playbackOperationError(http.StatusNotImplemented, "capability_unsupported", "Sequenced playback progress is not supported by this plan store")
	}
	record, err := h.ownedAttemptV2(ctx, caller, sessionID)
	if err != nil {
		return PlaybackMutationView{}, err
	}
	if record.StoppedAt != nil {
		return PlaybackMutationView{}, h.stoppedAttemptError(ctx, sessionID, record)
	}
	sample := playback.ProgressSampleV3{Sequence: command.Sequence, Position: command.Position, IsPaused: command.IsPaused}
	receipt, err := store.ApplyProgress(ctx, sessionID, sample)
	switch {
	case errors.Is(err, playback.ErrProgressConflictV3):
		return PlaybackMutationView{}, playbackOperationError(http.StatusConflict, PlaybackCodeProgressConflict, "The sequence already has different progress")
	case errors.Is(err, playback.ErrAttemptStoppedV3):
		return PlaybackMutationView{}, h.stoppedAttemptError(ctx, sessionID, nil)
	case errors.Is(err, playback.ErrSessionNotFound):
		return PlaybackMutationView{}, playbackSessionNotFoundOperationError()
	case err != nil:
		return PlaybackMutationView{}, playbackStoreOperationError()
	}
	h.publishSequencedPause(ctx, sessionID, receipt.Lifecycle)
	view := PlaybackMutationView{Outcome: receipt.Outcome, Accepted: acceptedProgressV2(receipt.Accepted)}
	// An applied sample persists. A replayed one (the exact latest sample
	// again) persists too: the client retried because the first reply was
	// lost, which may have been before the side effects ran. The writers
	// are idempotent for an identical position, so redoing them is safe.
	if receipt.Outcome != playback.ProgressAppliedV3 && receipt.Outcome != playback.ProgressReplayedV3 {
		return view, nil
	}
	h.persistProgressV2(ctx, store, record, sessionID, sample)
	return view, nil
}

// maxProgressWritePassesV2 bounds how many times persistProgressV2 rewrites
// the side effects after the row moved under it. Each extra pass needs the
// row to have advanced while the previous write ran, so one is the norm.
const maxProgressWritePassesV2 = 3

// latestAcceptedSampleV2 reads the attempt's latest accepted sample by
// probing the row with sample: a repeat of the latest sample is "replayed",
// a stale one carries the newer sample in Accepted. ok is false when the
// attempt is stopped, gone, or the store failed, and nothing should be
// persisted.
func latestAcceptedSampleV2(ctx context.Context, store playback.ProgressStoreV3, sessionID string, sample playback.ProgressSampleV3) (latest playback.ProgressSampleV3, ok bool) {
	receipt, err := store.ApplyProgress(ctx, sessionID, sample)
	if err != nil {
		return playback.ProgressSampleV3{}, false
	}
	if receipt.Outcome == playback.ProgressStaleSampleV3 && receipt.Accepted != nil {
		return *receipt.Accepted, true
	}
	return sample, true
}

// persistProgressV2 runs the v1 progress side effects and leaves them at the
// attempt's latest accepted sample.
//
// The CAS orders samples on the row, but the writers run after it and the
// user-store write is last-write-wins, so two replicas can finish in the
// opposite order to the row: sequence 1 lands on replica A after sequence 2
// landed on replica B. A process-local lock cannot order that, so the row is
// the guard instead. The writers run for the row's latest sample, then the
// row is read again; if it moved while they ran, the newer sample is written
// on top. Whoever writes last therefore writes the latest: a later sample's
// writer either ran after this caller's final check or was the one that moved
// the row, and it finishes with the same check.
func (h *PlaybackHandler) persistProgressV2(ctx context.Context, store playback.ProgressStoreV3, record *playback.AttemptRecordV3, sessionID string, sample playback.ProgressSampleV3) {
	// Within one replica the passes are serialized per session so two local
	// writers cannot interleave inside a pass.
	unlock := h.progressSideEffectLock(sessionID)
	defer unlock()
	target := sample
	for pass := 0; ; pass++ {
		latest, ok := latestAcceptedSampleV2(ctx, store, sessionID, target)
		if !ok {
			return
		}
		if pass > 0 && latest == target {
			return
		}
		if pass == maxProgressWritePassesV2 {
			slog.WarnContext(ctx, "playback progress side effects trail the attempt row", "component", "api", "session", sessionID, "playback_session_id", sessionID, "sequence", latest.Sequence)
			return
		}
		target = latest
		h.writeProgressSideEffectsV2(ctx, record, sessionID, target)
	}
}

// writeProgressSideEffectsV2 applies one sample to the live session when this
// replica holds it and to the user-store writer either way.
func (h *PlaybackHandler) writeProgressSideEffectsV2(ctx context.Context, record *playback.AttemptRecordV3, sessionID string, sample playback.ProgressSampleV3) {
	session, err := h.sessionMgr.GetSession(sessionID)
	if err == nil && session != nil {
		wasPaused := session.IsPaused
		var updateErr error
		if mgr, ok := h.sessionMgr.(interface {
			UpdateProgressSample(string, playback.ProgressSampleV3) error
		}); ok {
			updateErr = mgr.UpdateProgressSample(sessionID, sample)
		} else {
			updateErr = h.sessionMgr.UpdateProgress(sessionID, sample.Position, sample.IsPaused)
		}
		if updateErr != nil && !errors.Is(updateErr, playback.ErrSessionNotFound) {
			slog.WarnContext(ctx, "failed to update live playback progress", "component", "api", "session", sessionID, "playback_session_id", sessionID, "error", updateErr)
		}
		if current, getErr := h.sessionMgr.GetSession(sessionID); getErr == nil && current != nil {
			h.syncSessionsOnPauseChange(ctx, wasPaused, current.IsPaused)
			h.persistProgress(ctx, current, sample)
			h.scrobblePauseTransitionV2(ctx, current, wasPaused)
			return
		}
	}
	h.persistProgress(ctx, h.attemptSessionV2(ctx, record, sample.Position, sample.IsPaused), sample)
}

// progressSideEffectLock serializes progress side effects for one session.
// Entries are dropped when the session stops (forgetProgressSideEffectLock);
// a late caller that still holds a dropped mutex simply finishes on it.
func (h *PlaybackHandler) progressSideEffectLock(sessionID string) func() {
	entry, _ := h.progressSideEffectLocks.LoadOrStore(sessionID, &sync.Mutex{})
	mu, ok := entry.(*sync.Mutex)
	if !ok {
		return func() {}
	}
	mu.Lock()
	return mu.Unlock
}

// forgetProgressSideEffectLock releases the per-session lock entry once the
// attempt is terminal, so a long-lived replica does not retain one entry per
// historical session.
func (h *PlaybackHandler) forgetProgressSideEffectLock(sessionID string) {
	// Keep the mutex identity for the lifetime of the handler. Deleting it
	// while a writer still holds the old mutex lets a late writer create a
	// second mutex for the same session and run side effects concurrently.
}

func (h *PlaybackHandler) scrobblePauseTransitionV2(ctx context.Context, sess *playback.Session, wasPaused bool) {
	if sess.DisableProgressPersistence || h.WatchScrobbler == nil || wasPaused == sess.IsPaused {
		return
	}
	file, loadErr := h.loadFileByPreferredID(ctx, requestedMediaFileID(sess), sess.MediaFileID)
	if loadErr != nil || file == nil {
		return
	}
	targetID := playbackProgressTarget(file)
	if targetID == "" {
		return
	}
	event := h.scrobbleEventForSession(ctx, sess, targetID, float64(file.Duration), sess.Position)
	if sess.IsPaused {
		if err := h.WatchScrobbler.ScrobblePause(ctx, event); err != nil {
			slog.WarnContext(ctx, "failed to queue watch provider pause scrobble", "component", "api", "session", sess.ID, "error", err)
		}
	} else if err := h.WatchScrobbler.ScrobbleStart(ctx, event); err != nil {
		slog.WarnContext(ctx, "failed to queue watch provider resume scrobble", "component", "api", "session", sess.ID, "error", err)
	}
}

// StopPlaybackV2 is DELETE /api/v2/playback/{session_id}. The first stop wins
// the compare-and-set on stopped_at: it applies the optional final sample,
// runs the v1 stop/history writer, stops the local session and transcode,
// writes the stream deny marker, and records the receipt on the row. Every
// later stop, with any stop id, replays the stored receipt.
func (h *PlaybackHandler) StopPlaybackV2(ctx context.Context, caller PlaybackCaller, sessionID string, command PlaybackStopCommand) (PlaybackMutationView, error) {
	if err := h.validatePlaybackCaller(ctx, caller); err != nil {
		return PlaybackMutationView{}, err
	}
	if err := validatePlaybackSessionID(sessionID); err != nil {
		return PlaybackMutationView{}, err
	}
	if id, err := uuid.Parse(command.StopID); err != nil || id == uuid.Nil || id.String() != command.StopID {
		return PlaybackMutationView{}, playbackOperationError(http.StatusBadRequest, "bad_request", "A canonical stop_id is required")
	}
	if serverStopID(sessionID, command.StopID) {
		// The server's own stop ids say who ended the attempt; a client
		// stop must not claim one.
		return PlaybackMutationView{}, playbackOperationError(http.StatusBadRequest, "bad_request", "The stop_id is reserved")
	}
	if command.Sequence < 0 || (command.Position == nil) != (command.Sequence == 0) ||
		(command.Position != nil && (*command.Position < 0 || math.IsNaN(*command.Position) || math.IsInf(*command.Position, 0))) {
		return PlaybackMutationView{}, playbackOperationError(http.StatusBadRequest, "bad_request", "Invalid stop identity or final sample")
	}
	store, ok := h.PlanStoreV3.(playback.ProgressStoreV3)
	if !ok {
		return PlaybackMutationView{}, playbackOperationError(http.StatusNotImplemented, "capability_unsupported", "Sequenced playback stop is not supported by this plan store")
	}
	record, err := h.ownedAttemptV2(ctx, caller, sessionID)
	if err != nil {
		return PlaybackMutationView{}, err
	}
	var final *playback.ProgressSampleV3
	if command.Position != nil {
		final = &playback.ProgressSampleV3{Sequence: command.Sequence, Position: *command.Position, IsPaused: command.IsPaused}
	}
	receipt, first, err := store.StopAttempt(ctx, sessionID, command.StopID, final)
	switch {
	case errors.Is(err, playback.ErrInvalidStopIDV3):
		return PlaybackMutationView{}, playbackOperationError(http.StatusBadRequest, "bad_request", "A canonical stop_id is required")
	case errors.Is(err, playback.ErrSessionNotFound):
		return PlaybackMutationView{}, playbackSessionNotFoundOperationError()
	case err != nil:
		return PlaybackMutationView{}, playbackStoreOperationError()
	}
	outcome := PlaybackOutcomeStopped
	if !first {
		outcome = PlaybackOutcomeReplayed
	}
	if !receipt.Finalized {
		// This stop won, or a replay found the winner's side effects
		// unfinished (the winning replica died between the CAS and the
		// writers). The history writer mints a new row per call, so exactly
		// one caller may run it: claim finalization on the row first. A
		// caller that loses the claim replays the receipt as stored.
		receipt = h.finalizeStopV2(ctx, store, record, sessionID, receipt)
	}
	h.forgetProgressSideEffectLock(sessionID)
	return PlaybackMutationView{Outcome: outcome, Accepted: acceptedProgressV2(receipt.Accepted), StopID: receipt.StopID, HistoryID: receipt.HistoryID}, nil
}

// stopFinalizationLease bounds how long a claimed finalization may run before
// another replay may take it over.
const stopFinalizationLease = 30 * time.Second

// finalizeStopV2 claims finalization, runs the stop side effects, and records
// the finalized receipt. When the claim is lost the stored receipt is returned
// unchanged. The request cannot fail from here: the receipt is already durable.
func (h *PlaybackHandler) finalizeStopV2(ctx context.Context, store playback.ProgressStoreV3, record *playback.AttemptRecordV3, sessionID string, receipt playback.StopReceiptV3) playback.StopReceiptV3 {
	leaseUntil := time.Now().Add(stopFinalizationLease)
	// The attempt already accepted the stop. Let the initial claim reach the
	// store even if the player closed its connection in the meantime.
	claimCtx, cancelClaim := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	claimed, err := store.ClaimStopFinalization(claimCtx, sessionID, leaseUntil)
	cancelClaim()
	if err != nil {
		slog.WarnContext(ctx, "failed to claim playback stop finalization", "component", "api", "session", sessionID, "playback_session_id", sessionID, "error", err)
		return receipt
	}
	if !claimed {
		// Another caller holds the lease. Wait for it to finish, or for its
		// lease to lapse and take over, bounded by the request context. The
		// receipt is re-read each round so the lease judged is the one the
		// store holds now, not the one read before the winner claimed.
		for {
			if replay, _, err := store.StopAttempt(ctx, sessionID, receipt.StopID, nil); err == nil {
				if replay.Finalized {
					return replay
				}
				receipt = replay
			}
			if !receipt.FinalizingUntil.IsZero() && !time.Now().Before(receipt.FinalizingUntil) {
				leaseUntil = time.Now().Add(stopFinalizationLease)
				claimed, err = store.ClaimStopFinalization(ctx, sessionID, leaseUntil)
				if err == nil && claimed {
					break
				}
			}
			timer := time.NewTimer(25 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return receipt
			case <-timer.C:
			}
		}
	}
	// A player may close its HTTP connection once it has sent the stop.
	// Finish the durable claim within its lease even if that happens.
	ctx, cancel := context.WithDeadline(context.WithoutCancel(ctx), leaseUntil)
	defer cancel()
	historyID, err := h.finishStop(ctx, record, sessionID, receipt)
	if err != nil {
		slog.WarnContext(ctx, "failed to finalize playback stop", "component", "api", "session", sessionID, "error", err)
		return receipt
	}
	receipt.HistoryID = historyID
	receipt.Finalized = true
	receipt.FinalizingUntil = time.Time{}
	if err := store.RecordStopReceipt(ctx, sessionID, receipt); err != nil {
		slog.WarnContext(ctx, "failed to record playback stop receipt", "component", "api", "session", sessionID, "playback_session_id", sessionID, "error", err)
	}
	return receipt
}

// finishStop runs the winning stop's writers from the live session or,
// when no copy is held here, one reconstructed from the attempt.
func (h *PlaybackHandler) finishStop(ctx context.Context, record *playback.AttemptRecordV3, sessionID string, receipt playback.StopReceiptV3) (string, error) {
	reason := endReasonForStop(sessionID, receipt.StopID)
	accepted := receipt.Accepted
	ctx = withNativeStopHistory(ctx, sessionID, receipt.StopID)
	h.StreamDeny.Deny(ctx, sessionID)
	position, paused := 0.0, false
	if accepted != nil {
		position, paused = accepted.Position, accepted.IsPaused
	}
	if session, err := h.sessionMgr.GetSession(sessionID); err == nil && session != nil {
		result, err := h.stopPlaybackSessionWithResult(ctx, session, reason, accepted)
		if err == nil {
			return result.HistoryID, result.WatchStateErr
		}
		if !errors.Is(err, playback.ErrSessionNotFound) {
			return "", err
		}
		// Lost the race with another stop of the live session; fall through to
		// the row-synthesized writer so the final sample is still recorded.
	}
	// No copy here to finalize: the copy on another replica, if any, finds
	// the attempt stopped and is dropped without history, so the admin log
	// is written from the attempt row and the live row that copy synced.
	// Re-read the attempt after StopAttempt sealed it: another replica may have
	// committed a replan between ownedAttemptV2 and StopAttempt, and stop
	// finalization must record the final committed plan rather than the earlier
	// snapshot.
	if h.PlanStoreV3 != nil {
		if latest, err := h.PlanStoreV3.GetAttempt(ctx, sessionID); err == nil && latest != nil {
			record = latest
		}
	}
	if accepted == nil {
		position = playback.PositionUnknown
	}
	session := h.attemptSessionV2(ctx, record, position, paused)
	carried := endWatch(ctx)
	session.Watch = carried
	fromLiveRow := h.fillDetachedSession(ctx, session, record.CreatedAt)
	fromRemovedCopy := carried.Measured && session.Watch.Until.Equal(carried.Until)
	if accepted != nil && (accepted.Sequence > record.LastSequence || !fromRemovedCopy) {
		// The latest accepted sample: the stop's own, or, when the
		// measurement was restored from the last save rather than handed
		// over by the copy this end removed, progress accepted since that
		// save. Progress does not trigger a save, so the saved measurement
		// can predate it. A sample it already holds credits nothing.
		observeEndSample(session, time.Now())
	}
	if record.CurrentPlanID != "" {
		// The attempt holds the plan and frozen recipe the last replan
		// committed. The live row can be gone after a restart, leaving only
		// where the play started.
		applyAttemptPlanFacts(session, record, fromLiveRow)
	}
	result := h.recordStopHistory(ctx, session, reason)
	if result.WatchStateErr != nil {
		return "", result.WatchStateErr
	}
	// The copy on another replica is dropped without telling providers, so
	// the end recorded here does, unless its caller tells them itself.
	if !leaveProviderNotice(ctx) {
		h.notifyStoppedSession(ctx, session, result, reason)
	}
	return result.HistoryID, nil
}

// attemptSessionV2 synthesizes the session the v1 writers read when this
// replica does not hold the live one. Only the fields the writers consult are
// populated.
func (h *PlaybackHandler) attemptSessionV2(ctx context.Context, record *playback.AttemptRecordV3, position float64, paused bool) *playback.Session {
	session := &playback.Session{
		ID:                   record.SessionID,
		UserID:               record.UserID,
		ProfileID:            record.ProfileID,
		MediaFileID:          record.EffectiveMediaFileID,
		RequestedMediaFileID: record.RequestedMediaFileID,
		Position:             position,
		IsPaused:             paused,
	}
	session.DisableProgressPersistence = record.NormalizedRequest.ProgressPersistence == playback.ProgressPersistenceClientV3
	if !session.DisableProgressPersistence && h.fileResolver != nil {
		if file, err := h.fileResolver.GetByID(ctx, record.EffectiveMediaFileID); err == nil && !sessionOwnsResumeTimelineV3(file) {
			session.DisableProgressPersistence = true
		}
	}
	return session
}

// attemptOwnedBy reports whether the attempt belongs to the caller's profile.
func attemptOwnedBy(record *playback.AttemptRecordV3, caller PlaybackCaller) bool {
	return record != nil && record.UserID == caller.UserID && record.ProfileID == caller.ProfileID
}

func acceptedProgressV2(sample *playback.ProgressSampleV3) *PlaybackAcceptedProgress {
	if sample == nil {
		return nil
	}
	return &PlaybackAcceptedProgress{Sequence: sample.Sequence, Position: sample.Position, IsPaused: sample.IsPaused}
}

// ReplanPlaybackV2 is POST /api/v2/playback/{session_id}/replan: the full v1
// replan application (seek, track, quality and output changes).
func (h *PlaybackHandler) ReplanPlaybackV2(ctx context.Context, caller PlaybackCaller, sessionID string, command PlaybackReplanCommand) (playback.DecisionResponseV3, error) {
	if err := h.validatePlaybackCaller(ctx, caller); err != nil {
		return playback.DecisionResponseV3{}, err
	}
	if err := validatePlaybackSessionID(sessionID); err != nil {
		return playback.DecisionResponseV3{}, err
	}
	if command.Request.PlaybackAttemptID == "" || command.Request.ReplanRequestID == "" {
		return playback.DecisionResponseV3{}, playbackOperationError(http.StatusBadRequest, "bad_request", "Invalid replan request")
	}
	body, err := json.Marshal(command.Request)
	if err != nil {
		return playback.DecisionResponseV3{}, playbackOperationError(http.StatusBadRequest, "bad_request", "Invalid replan request")
	}
	// A stop accepted on any replica ends the attempt; a late recovery or
	// seek must not launch a replacement transport the deny marker will
	// refuse to serve. The store's commit predicate refuses stopped rows too.
	// Only the attempt's owner learns how it ended; anyone else gets the 403
	// the replan itself answers.
	record, err := h.PlanStoreV3.GetAttempt(ctx, sessionID)
	if refused := refuseOtherStream(ctx, caller, record, err); refused != nil {
		return playback.DecisionResponseV3{}, refused
	}
	if err == nil && record != nil && record.StoppedAt != nil {
		if !attemptOwnedBy(record, caller) {
			return playback.DecisionResponseV3{}, playbackOperationError(http.StatusForbidden, "forbidden", "Session belongs to another profile")
		}
		return playback.DecisionResponseV3{}, h.stoppedAttemptError(ctx, sessionID, record)
	}
	response, err := h.replanPlaybackApplicationV3(playbackCallerSessionRequest(ctx, caller, sessionID), sessionID, body)
	if err != nil {
		// A stop that lands after the check above reaches the replan as a
		// missing live session or a refused commit, both reported as not
		// found. Re-read the attempt so an administrator stop or the paused
		// timeout still answers 410 instead of the 404 the player renews from.
		var opErr *PlaybackOperationError
		if errors.Is(err, playback.ErrAttemptStoppedV3) || (errors.As(err, &opErr) && opErr.Status == http.StatusNotFound) {
			if record, getErr := h.PlanStoreV3.GetAttempt(ctx, sessionID); getErr == nil && attemptOwnedBy(record, caller) {
				if ended := endedAttemptError(record); ended != nil {
					return playback.DecisionResponseV3{}, ended
				}
			}
		}
		if errors.Is(err, playback.ErrAttemptStoppedV3) {
			return playback.DecisionResponseV3{}, playbackSessionNotFoundOperationError()
		}
	}
	return withNativeServerFeaturesV3(response), err
}

// ReportRouteEventV2 is POST /api/v2/playback/route-events. Only the ownership
// check waits on the store; the event itself is queued, and event_id dedups a
// retried report.
func (h *PlaybackHandler) ReportRouteEventV2(ctx context.Context, caller PlaybackCaller, command PlaybackRouteEventCommand) error {
	if err := h.validatePlaybackCaller(ctx, caller); err != nil {
		return err
	}
	if id, err := uuid.Parse(command.EventID); err != nil || id == uuid.Nil || id.String() != command.EventID {
		return playbackOperationError(http.StatusBadRequest, "bad_request", "A canonical event_id is required")
	}
	event := command.Event
	if !validRouteEventV3(event) {
		return playbackOperationError(http.StatusBadRequest, "bad_request", "Invalid route event")
	}
	if !h.allowRouteEventV3(caller.UserID, event.PlaybackAttemptID) {
		return playbackOperationError(http.StatusTooManyRequests, "event_rate_limited", "Playback route event rate exceeded")
	}
	var identity *playback.AttemptIdentityV3
	var err error
	if event.SessionID != "" {
		identity, err = h.PlanStoreV3.GetAttemptIdentity(ctx, event.SessionID)
	} else {
		identity, err = h.PlanStoreV3.GetAttemptIdentityByPlaybackAttemptID(ctx, event.PlaybackAttemptID)
	}
	if err != nil {
		if !errors.Is(err, playback.ErrSessionNotFound) {
			return playbackStoreOperationError()
		}
		return playbackOperationError(http.StatusForbidden, "forbidden", "Route event does not belong to this profile")
	}
	if identity.UserID != caller.UserID || identity.ProfileID != caller.ProfileID ||
		(event.SessionID != "" && identity.PlaybackAttemptID != event.PlaybackAttemptID) ||
		(identity.SessionID == "" && !terminalStartRouteEventV3(event)) {
		return playbackOperationError(http.StatusForbidden, "forbidden", "Route event does not belong to this profile")
	}
	event.Diagnostics = sanitizeDiagnosticsV3(event.Diagnostics)
	record := playback.RouteEventRecordV3{RouteEventV3: event, EventID: command.EventID, UserID: caller.UserID, ProfileID: caller.ProfileID, ClientName: caller.ClientName, ClientVersion: caller.ClientVersion, ClientBuild: caller.ClientBuild, ClientChannel: caller.ClientChannel, ClientModel: event.Diagnostics["device_model"]}
	if event.Event == playback.RouteEventPlanSummaryV3 {
		record.ConnectionDeviceID = connectionDeviceV3(caller.ConnectionDeviceID)
		record.ConnectionKey, _ = connectionstore.Key(ctx, h.JWTSecret)
		record.ReceivedAt = time.Now().UTC()
		record.SampleLifetime = h.playbackConfig().BandwidthProbeMaxAge()
	}
	h.enqueueRouteEventV3(record)
	return nil
}

// ReplanDigestV3 fingerprints the exact replan body so a reused request id with
// different input is a detectable idempotency violation.
func ReplanDigestV3(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func validatePlaybackSessionID(sessionID string) error {
	id, err := uuid.Parse(sessionID)
	if err != nil || id == uuid.Nil || id.String() != sessionID {
		return playbackOperationError(http.StatusBadRequest, "bad_request", "Invalid playback session ID")
	}
	return nil
}

// expiredStopID is the server-minted stop identity recorded on the attempt
// row when the liveness reaper ends a session.
func expiredStopID(sessionID string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("silo-expired:"+sessionID)).String()
}

// abortStopID is the server-minted stop id of an attempt the server
// abandoned: its media went missing, or a replacement transport failed.
func abortStopID(sessionID string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("silo-abort:"+sessionID)).String()
}

// adminStopID is the server-minted stop id of an attempt an administrator
// ended. Progress and replan for such an attempt answer 410
// playback_session_ended, so the player exits instead of starting a fresh
// session the way it recovers from an expiry or a server restart.
func adminStopID(sessionID string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("silo-admin-stop:"+sessionID)).String()
}

// pausedTimeoutStopID is the server-minted stop id of an attempt the server
// ended because it stayed paused past the administrator's timeout. It ends
// as expired, but its requests answer 410 so the player leaves instead of
// starting over: playback_session_paused_timeout for an attempt that
// advertised paused_timeout_v1, playback_session_ended otherwise.
func pausedTimeoutStopID(sessionID string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("silo-paused-timeout:"+sessionID)).String()
}

// endReasonForStop is the end reason a winning stop id records: the
// server-minted ids name who ended the attempt, and any other id is the
// client's own stop.
func endReasonForStop(sessionID, stopID string) PlaybackEndReason {
	switch stopID {
	case adminStopID(sessionID):
		return PlaybackEndTerminated
	case expiredStopID(sessionID), pausedTimeoutStopID(sessionID):
		return PlaybackEndExpired
	case abortStopID(sessionID):
		return PlaybackEndAborted
	default:
		return PlaybackEndStopped
	}
}

// serverStopID reports whether stopID is one the server mints for an end it
// makes itself.
func serverStopID(sessionID, stopID string) bool {
	return stopID == adminStopID(sessionID) || stopID == expiredStopID(sessionID) || stopID == abortStopID(sessionID) ||
		stopID == pausedTimeoutStopID(sessionID)
}

func attemptEndedByAdmin(record *playback.AttemptRecordV3) bool {
	return record != nil && record.StoppedAt != nil && record.SessionID != "" && record.StopID == adminStopID(record.SessionID)
}

func playbackSessionEndedByAdminError() *PlaybackOperationError {
	return playbackOperationError(http.StatusGone, playbackSessionEndedErrorCode, "An administrator ended this playback session")
}

// PlaybackCodeSessionPausedTimeout is the problem type for an attempt the
// server ended after it stayed paused too long, answered to a client that
// advertised paused_timeout_v1.
const PlaybackCodeSessionPausedTimeout = "playback_session_paused_timeout"

// pausedTimeoutEnd reports whether an attempt was ended by the paused
// timeout, and whether its client asked to be told so (paused_timeout_v1).
// The features are the attempt's durable ones, which a replan cannot change.
func pausedTimeoutEnd(sessionID, stopID string, clientFeatures []string) (ended, advertised bool) {
	if sessionID == "" || stopID != pausedTimeoutStopID(sessionID) {
		return false, false
	}
	return true, playback.HasFeatureV3(clientFeatures, playback.FeaturePausedTimeoutV3)
}

// pausedTimeoutError is the 410 for an attempt the paused timeout ended:
// its own problem type for a client that advertised paused_timeout_v1, and
// playback_session_ended, which every client already leaves on, otherwise.
func pausedTimeoutError(advertised bool) *PlaybackOperationError {
	if advertised {
		return playbackOperationError(http.StatusGone, PlaybackCodeSessionPausedTimeout, "Playback stopped because it was paused for too long.")
	}
	return playbackOperationError(http.StatusGone, playbackSessionEndedErrorCode, "Playback stopped because it was paused for too long")
}

// endedAttemptError is the 410 for a stopped attempt the client must not
// start over from (an administrator stop or the paused timeout), or nil.
func endedAttemptError(record *playback.AttemptRecordV3) *PlaybackOperationError {
	if record == nil || record.StoppedAt == nil {
		return nil
	}
	if attemptEndedByAdmin(record) {
		return playbackSessionEndedByAdminError()
	}
	if ended, advertised := pausedTimeoutEnd(record.SessionID, record.StopID, record.NormalizedRequest.ClientFeatures); ended {
		return pausedTimeoutError(advertised)
	}
	return nil
}

// stoppedAttemptError is the answer for a request on an attempt that is no
// longer live: 410 when an administrator or the paused timeout ended it,
// otherwise the 404 that lets the client start over.
func (h *PlaybackHandler) stoppedAttemptError(ctx context.Context, sessionID string, record *playback.AttemptRecordV3) *PlaybackOperationError {
	if record == nil && h != nil && h.PlanStoreV3 != nil {
		record, _ = h.PlanStoreV3.GetAttempt(ctx, sessionID)
	}
	if err := endedAttemptError(record); err != nil {
		return err
	}
	return playbackSessionNotFoundOperationError()
}

// attemptStoppedElsewhere reports whether the attempt row is already stopped,
// so a replica reaping its stale local copy does not overwrite a stop another
// replica accepted. Unknown rows (no plan store, not found) report false.
func (h *PlaybackHandler) attemptStoppedElsewhere(ctx context.Context, sessionID string) bool {
	if h == nil || h.PlanStoreV3 == nil || sessionID == "" {
		return false
	}
	record, err := h.PlanStoreV3.GetAttempt(ctx, sessionID)
	return err == nil && record != nil && record.StoppedAt != nil
}

// attemptActiveElsewhere reports whether the attempt row saw progress more
// recently than this replica's copy did. Media and progress requests can land
// on another replica, leaving the local activity clock stale; that copy is
// dropped without finalizing so the live session elsewhere keeps serving.
func (h *PlaybackHandler) attemptActiveElsewhere(ctx context.Context, session *playback.Session) bool {
	if h == nil || h.PlanStoreV3 == nil || session == nil {
		return false
	}
	record, err := h.PlanStoreV3.GetAttempt(ctx, session.ID)
	if err != nil || record == nil || record.LastSample == nil || record.LastSampleAt.IsZero() {
		return false
	}
	return record.LastSampleAt.After(session.LastActivityAt)
}
