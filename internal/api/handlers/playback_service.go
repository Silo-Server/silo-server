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
	UserID                                                int
	ProfileID, InstallationID                             string
	DeviceID, DeviceName, Platform                        string
	UserAgent, RemoteAddr                                 string
	ClientName, ClientVersion, ClientBuild, ClientChannel string
}

// PlaybackCapabilitiesView is the v2 capabilities body. State is always
// "available": there is no admission step in front of playback.
type PlaybackCapabilitiesView struct {
	InstallationID, Revision, State string
	Allowed                         bool
	ProtocolVersions                []int
	Features                        []string
	Deliveries                      []playback.DeliveryV3
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
func writePlaybackOperationError(w http.ResponseWriter, err error) {
	if e, ok := errors.AsType[*PlaybackOperationError](err); ok {
		writeError(w, e.Status, e.Code, e.Message)
		return
	}
	writeError(w, http.StatusInternalServerError, "internal_error", "Playback operation failed")
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
	view.Features = playbackServerFeaturesV2()
	view.Deliveries = []playback.DeliveryV3{playback.DeliveryOriginalHTTPV3, playback.DeliveryRemuxProgressiveV3, playback.DeliveryRemuxHLSV3}
	if h.playbackConfig().TranscodeEnabled {
		view.Deliveries = append(view.Deliveries, playback.DeliveryTranscodeHLSV3)
	}
	capability, _ := json.Marshal(view) // This view contains only JSON-safe scalar values.
	digest := sha256.Sum256(capability)
	view.Revision = hex.EncodeToString(digest[:])
	return view, nil
}

// playbackServerFeaturesV2 is the v3 feature set plus the v2 sequenced
// progress/stop contract.
func playbackServerFeaturesV2() []string {
	return append(playback.ServerFeaturesV3(), "sequenced_progress_v1")
}

// The application pipeline still uses private request-based routing helpers.
// This request contains only caller facts; it is never dispatched to an HTTP
// handler and never carries credentials, a body stream, or a response writer.
func playbackCallerRequest(ctx context.Context, caller PlaybackCaller) *http.Request {
	headers := make(http.Header)
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
	return h.startPlaybackApplicationV3(playbackCallerRequest(ctx, caller), body)
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
		return PlaybackMutationView{}, playbackSessionNotFoundOperationError()
	}
	sample := playback.ProgressSampleV3{Sequence: command.Sequence, Position: command.Position, IsPaused: command.IsPaused}
	receipt, err := store.ApplyProgress(ctx, sessionID, sample)
	switch {
	case errors.Is(err, playback.ErrProgressConflictV3):
		return PlaybackMutationView{}, playbackOperationError(http.StatusConflict, PlaybackCodeProgressConflict, "The sequence already has different progress")
	case errors.Is(err, playback.ErrAttemptStoppedV3), errors.Is(err, playback.ErrSessionNotFound):
		return PlaybackMutationView{}, playbackSessionNotFoundOperationError()
	case err != nil:
		return PlaybackMutationView{}, playbackStoreOperationError()
	}
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
		if err := h.sessionMgr.UpdateProgress(sessionID, sample.Position, sample.IsPaused); err != nil && !errors.Is(err, playback.ErrSessionNotFound) {
			slog.WarnContext(ctx, "failed to update live playback progress", "component", "api", "session", sessionID, "playback_session_id", sessionID, "error", err)
		}
		h.syncSessionsNow(ctx, "progress")
		if current, getErr := h.sessionMgr.GetSession(sessionID); getErr == nil && current != nil {
			h.persistProgress(ctx, current)
			h.scrobblePauseTransitionV2(ctx, current, wasPaused)
			return
		}
	}
	h.persistProgress(ctx, h.attemptSessionV2(ctx, record, sample.Position, sample.IsPaused))
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
	if h == nil {
		return
	}
	h.progressSideEffectLocks.Delete(sessionID)
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
	claimed, err := store.ClaimStopFinalization(ctx, sessionID, time.Now().Add(stopFinalizationLease))
	if err != nil {
		slog.WarnContext(ctx, "failed to claim playback stop finalization", "component", "api", "session", sessionID, "playback_session_id", sessionID, "error", err)
		return receipt
	}
	if !claimed {
		return receipt
	}
	receipt.HistoryID = h.finishStopV2(ctx, record, sessionID, receipt.Accepted)
	receipt.Finalized = true
	if err := store.RecordStopReceipt(ctx, sessionID, receipt); err != nil {
		slog.WarnContext(ctx, "failed to record playback stop receipt", "component", "api", "session", sessionID, "playback_session_id", sessionID, "error", err)
	}
	return receipt
}

// finishStopV2 runs the v1 stop side effects for the winning stop: the
// session stop + transcode teardown when this replica holds the session (its
// finalizer runs the history writer), the history writer directly from a
// session synthesized from the attempt row otherwise. The deny marker is
// written in both cases. It returns the watch-history id when one was made.
func (h *PlaybackHandler) finishStopV2(ctx context.Context, record *playback.AttemptRecordV3, sessionID string, accepted *playback.ProgressSampleV3) string {
	h.StreamDeny.Deny(ctx, sessionID)
	position, paused := 0.0, false
	if accepted != nil {
		position, paused = accepted.Position, accepted.IsPaused
	}
	if session, err := h.sessionMgr.GetSession(sessionID); err == nil && session != nil {
		if accepted != nil {
			if err := h.sessionMgr.UpdateProgress(sessionID, position, paused); err != nil && !errors.Is(err, playback.ErrSessionNotFound) {
				slog.WarnContext(ctx, "failed to apply final playback position", "component", "api", "session", sessionID, "playback_session_id", sessionID, "error", err)
			}
		}
		result, err := h.stopPlaybackSessionWithResult(ctx, session, true)
		if err == nil {
			return result.HistoryID
		}
		if !errors.Is(err, playback.ErrSessionNotFound) {
			slog.WarnContext(ctx, "failed to stop playback session", "component", "api", "session", sessionID, "playback_session_id", sessionID, "error", err)
			return ""
		}
		// Lost the race with another stop of the live session; fall through to
		// the row-synthesized writer so the final sample is still recorded.
	}
	return h.persistStopAndHistory(ctx, h.attemptSessionV2(ctx, record, position, paused)).HistoryID
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

// ownedAttemptV2 loads the live attempt row for sessionID and checks it
// belongs to the caller.
func (h *PlaybackHandler) ownedAttemptV2(ctx context.Context, caller PlaybackCaller, sessionID string) (*playback.AttemptRecordV3, error) {
	record, err := h.PlanStoreV3.GetAttempt(ctx, sessionID)
	if err != nil {
		if errors.Is(err, playback.ErrSessionNotFound) {
			return nil, playbackSessionNotFoundOperationError()
		}
		return nil, playbackStoreOperationError()
	}
	if record.UserID != caller.UserID || record.ProfileID != caller.ProfileID {
		return nil, playbackOperationError(http.StatusForbidden, "forbidden", "Session belongs to another profile")
	}
	return record, nil
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
	if record, err := h.PlanStoreV3.GetAttempt(ctx, sessionID); err == nil && record != nil && record.StoppedAt != nil {
		return playback.DecisionResponseV3{}, playbackSessionNotFoundOperationError()
	}
	response, err := h.replanPlaybackApplicationV3(playbackCallerSessionRequest(ctx, caller, sessionID), sessionID, body)
	if errors.Is(err, playback.ErrAttemptStoppedV3) {
		return playback.DecisionResponseV3{}, playbackSessionNotFoundOperationError()
	}
	return response, err
}

// ReportRouteEventV2 is POST /api/v2/playback/route-events. The event is
// queued and never waits on the store; event_id dedups a retried report.
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
	h.enqueueRouteEventV3(playback.RouteEventRecordV3{RouteEventV3: event, EventID: command.EventID, UserID: caller.UserID, ProfileID: caller.ProfileID, ClientName: caller.ClientName, ClientVersion: caller.ClientVersion, ClientBuild: caller.ClientBuild, ClientChannel: caller.ClientChannel, ClientModel: event.Diagnostics["device_model"]})
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
// row when a session ends without a client stop (expiry, abort).
func expiredStopID(sessionID string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("silo-expired:"+sessionID)).String()
}

// markAttemptStoppedServerSide stops the attempt row under the server-minted
// stop id and writes the deny marker, so a stopped or expired session cannot
// be replayed from its start attempt or served from a valid token on another
// replica. Best effort: the local teardown has already happened.
func (h *PlaybackHandler) markAttemptStoppedServerSide(ctx context.Context, sessionID string) {
	if h == nil {
		return
	}
	markAttemptStoppedServerSide(ctx, h.PlanStoreV3, h.StreamDeny, sessionID)
	h.forgetProgressSideEffectLock(sessionID)
}

func markAttemptStoppedServerSide(ctx context.Context, planStore playback.PlanStoreV3, deny *playback.StreamDeny, sessionID string) {
	if sessionID == "" {
		return
	}
	deny.Deny(ctx, sessionID)
	store, ok := planStore.(playback.ProgressStoreV3)
	if !ok {
		return
	}
	receipt, first, err := store.StopAttempt(ctx, sessionID, expiredStopID(sessionID), nil)
	if err != nil {
		if !errors.Is(err, playback.ErrSessionNotFound) {
			slog.WarnContext(ctx, "failed to stop expired playback attempt", "component", "api", "session", sessionID, "playback_session_id", sessionID, "error", err)
		}
		return
	}
	if first {
		// The local teardown already ran; the receipt is complete.
		receipt.Finalized = true
		if err := store.RecordStopReceipt(ctx, sessionID, receipt); err != nil {
			slog.WarnContext(ctx, "failed to record expired playback stop receipt", "component", "api", "session", sessionID, "playback_session_id", sessionID, "error", err)
		}
	}
}

// attemptLive reports whether the attempt row exists and is not stopped.
func (h *PlaybackHandler) attemptLive(ctx context.Context, sessionID string) bool {
	if h == nil || h.PlanStoreV3 == nil || sessionID == "" {
		return false
	}
	record, err := h.PlanStoreV3.GetAttempt(ctx, sessionID)
	return err == nil && record != nil && record.StoppedAt == nil
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
