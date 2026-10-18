package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchstate"
)

type playbackCommandRecord struct {
	SessionID string
	Name      playback.CommandName
}

// stopPlaybackSession stops a session. userInitiated must be true only for a
// genuine user/admin stop (the recipe card is then deleted); false for system
// teardown such as an ffmpeg-exit cleanup, where the card is kept so the client
// can reconstruct.
func (h *PlaybackHandler) stopPlaybackSession(ctx context.Context, session *playback.Session, userInitiated bool) error {
	_, err := h.stopPlaybackSessionWithResult(ctx, session, userInitiated)
	return err
}

// stopPlaybackSessionWithResult is stopPlaybackSession reporting the history
// writer's result, which the v2 stop receipt carries.
func (h *PlaybackHandler) stopPlaybackSessionWithResult(ctx context.Context, session *playback.Session, userInitiated bool) (watchstate.PlaybackStopResult, error) {
	if h == nil || session == nil || session.ID == "" {
		return watchstate.PlaybackStopResult{}, playback.ErrSessionNotFound
	}
	unlock := h.tm.LockSessionLifecycle(session.ID)
	defer unlock()
	current, err := h.endPlaybackSessionAuthority(ctx, session, userInitiated)
	if err != nil {
		return watchstate.PlaybackStopResult{}, err
	}
	return h.finalizeSessionStopWithResult(ctx, current, true, "stop", userInitiated), nil
}

// endPlaybackSessionAuthority ends the live session and revokes its streams
// before optional provider notifications run. The returned snapshot is used
// for history and ordered provider finalization. The caller holds the
// session lifecycle lock through authority and transport cleanup.
func (h *PlaybackHandler) endPlaybackSessionAuthority(ctx context.Context, session *playback.Session, userInitiated bool) (*playback.Session, error) {
	if h == nil || session == nil || session.ID == "" {
		return nil, playback.ErrSessionNotFound
	}
	// Replacement preparation holds this lifecycle lock until its new live
	// session state and durable plan are committed (or rolled back). Wait for
	// that boundary, then reload the route: callers commonly hold a copy taken
	// before the replacement started, and deleting authority from that stale
	// copy would leave the successor transport playable after a successful stop.
	current, err := h.sessionMgr.GetSession(session.ID)
	if err != nil {
		return nil, err
	}
	session = current
	if userInitiated {
		if err := h.deleteRequiredProgressiveRemuxAuthorityV3(ctx, session); err != nil {
			return nil, fmt.Errorf("persist progressive remux stop: %w", err)
		}
		if requiresProgressiveRemuxAuthorityV3(session) {
			if err := h.tm.CancelRemoteTranscode(ctx, remoteTransportID(session), session.TranscodeNodeURL); err != nil {
				return nil, fmt.Errorf("cancel progressive remux transport: %w", err)
			}
		}
	}

	if err := h.sessionMgr.StopSession(session.ID); err != nil {
		return nil, err
	}
	if userInitiated {
		// A user or admin stop is final: revoke the session's stream tokens on
		// every replica, not just the live entry here.
		h.StreamDeny.Deny(ctx, session.ID)
	}
	return session, nil
}

func (h *PlaybackHandler) stopPlaybackSessionByID(ctx context.Context, sessionID string, userInitiated bool) error {
	if h == nil || sessionID == "" {
		return playback.ErrSessionNotFound
	}
	session, err := h.sessionMgr.GetSession(sessionID)
	if err != nil {
		return err
	}
	return h.stopPlaybackSession(ctx, session, userInitiated)
}

// recordRealtimeStoppedSession shares native history finalization with the
// client's HTTP stop. Authority is already removed, so the client may have
// finalized from the attempt row while this provider work was queued.
func (h *PlaybackHandler) recordRealtimeStoppedSession(ctx context.Context, session *playback.Session) {
	result := h.realtimeStopHistory(ctx, session)
	h.recordAdminStopHistory(ctx, session)
	h.notifyStoppedSession(ctx, session, result, true)
}

func (h *PlaybackHandler) realtimeStopHistory(parent context.Context, session *playback.Session) watchstate.PlaybackStopResult {
	store, ok := h.PlanStoreV3.(playback.ProgressStoreV3)
	if !ok {
		return h.persistStopAndHistory(parent, session)
	}
	ctx, cancel := context.WithTimeout(parent, stopFinalizationLease)
	defer cancel()
	// This queued worker is the stop's only finalizer, so transient store
	// errors are retried rather than abandoning the stop.
	record, err := h.realtimeProgressAttempt(ctx, session.ID)
	if err != nil {
		slog.WarnContext(ctx, "failed to read realtime stop attempt", "session", session.ID, "error", err)
		return watchstate.PlaybackStopResult{}
	}
	if record == nil {
		return h.persistStopAndHistory(ctx, session) // Unsequenced bridge play.
	}
	var final *playback.ProgressSampleV3
	if record.LastSample == nil && session.Position > 0 {
		// A bridge progress report may have updated the live session without
		// a sequenced native sample. Preserve its final resume position.
		final = &playback.ProgressSampleV3{Sequence: record.LastSequence + 1, Position: session.Position, IsPaused: session.IsPaused}
	}
	var receipt playback.StopReceiptV3
	for range 3 {
		// The CAS is idempotent for one stop id, so a retry after an
		// ambiguous failure replays the stored receipt.
		receipt, _, err = store.StopAttempt(ctx, session.ID, adminStopID(session.ID), final)
		if err == nil || errors.Is(err, playback.ErrSessionNotFound) || ctx.Err() != nil {
			break
		}
	}
	if err != nil {
		slog.WarnContext(ctx, "failed to record realtime stop attempt", "session", session.ID, "error", err)
		return watchstate.PlaybackStopResult{}
	}
	// An administrator terminate or unconfirmed-command fallback that ran
	// after this stop removed the live session found nothing to tear down, so
	// its finalized receipt recorded no history. Write it from this snapshot.
	markerWithoutHistory := receipt.Finalized && receipt.HistoryID == "" && receipt.StopID == adminStopID(session.ID)
	if !receipt.Finalized {
		receipt = h.finalizeRealtimeStop(parent, store, record, session.ID, receipt, stopFinalizationLease)
	}
	// Finalization may have spent the first context's whole budget.
	ctx, cancelResult := context.WithTimeout(parent, stopFinalizationLease)
	defer cancelResult()
	if receipt.Accepted != nil {
		session.Position = receipt.Accepted.Position
		session.IsPaused = receipt.Accepted.IsPaused
	}
	if markerWithoutHistory {
		receipt.HistoryID = h.persistStopAndHistory(ctx, session).HistoryID
		if receipt.HistoryID != "" {
			if err := store.RecordStopReceipt(ctx, session.ID, receipt); err != nil {
				slog.WarnContext(ctx, "failed to record realtime stop history receipt", "session", session.ID, "error", err)
			}
		}
	}
	result := watchstate.PlaybackStopResult{HistoryID: receipt.HistoryID, FinalPositionSeconds: session.Position}
	file, err := h.loadFileByPreferredID(ctx, requestedMediaFileID(session), session.MediaFileID)
	if err == nil && file != nil {
		result.MediaItemID = playbackProgressTarget(file)
		result.DurationSeconds = float64(file.Duration)
		result.Completed = result.HistoryID != "" && !session.DisableProgressPersistence && result.DurationSeconds > 0 &&
			session.Position/result.DurationSeconds > userstore.WatchedFraction(h.playbackThresholds(ctx).WatchedPct)
	}
	return result
}

// finalizeRealtimeStop runs stop finalization for the one queued realtime
// worker, which no client request will retry. A finalizer on another replica
// may claim the stop just before this worker and die; its lease then outlasts
// a single round. Each round's budget ends before the lease it claims, so a
// second round outlasts that competitor without finalizing on an expired claim.
func (h *PlaybackHandler) finalizeRealtimeStop(parent context.Context, store playback.ProgressStoreV3, record *playback.AttemptRecordV3, sessionID string, receipt playback.StopReceiptV3, round time.Duration) playback.StopReceiptV3 {
	for range 2 {
		ctx, cancel := context.WithTimeout(parent, round)
		receipt = h.finalizeStopV2(ctx, store, record, sessionID, receipt)
		cancel()
		if receipt.Finalized {
			break
		}
	}
	return receipt
}

func (h *PlaybackHandler) abortPlaybackSession(ctx context.Context, session *playback.Session) error {
	if h == nil || session == nil || session.ID == "" {
		return playback.ErrSessionNotFound
	}

	if err := h.sessionMgr.StopSession(session.ID); err != nil {
		return err
	}
	h.finalizeSessionAbort(ctx, session, true, "abort")
	h.markAttemptStoppedServerSide(ctx, session.ID)
	return nil
}

func (h *PlaybackHandler) abortPlaybackSessionByID(ctx context.Context, sessionID string) error {
	if h == nil || sessionID == "" {
		return playback.ErrSessionNotFound
	}
	session, err := h.sessionMgr.GetSession(sessionID)
	if err != nil {
		return err
	}
	return h.abortPlaybackSession(ctx, session)
}

// CopySafetyPlaybackControl adapts the playback handler to the session control
// playback.CopySafetyNotifier needs: the notifier owns the decision to withdraw
// a plan, the handler owns realtime command bookkeeping and session teardown.
type CopySafetyPlaybackControl struct {
	playback *PlaybackHandler
}

// NewCopySafetyPlaybackControl returns the adapter, or nil without a handler.
func NewCopySafetyPlaybackControl(handler *PlaybackHandler) *CopySafetyPlaybackControl {
	if handler == nil {
		return nil
	}
	return &CopySafetyPlaybackControl{playback: handler}
}

func (c *CopySafetyPlaybackControl) RememberRealtimeCommand(commandID, sessionID string, name playback.CommandName) {
	if c == nil {
		return
	}
	c.playback.rememberRealtimeCommand(commandID, sessionID, name)
}

func (c *CopySafetyPlaybackControl) ForgetRealtimeCommand(commandID string) {
	if c == nil {
		return
	}
	c.playback.forgetRealtimeCommand(commandID)
}

// StopSession ends the session as a system teardown, not a user stop: the
// recipe card is kept so the client's recovery can rebuild from it.
func (c *CopySafetyPlaybackControl) StopSession(ctx context.Context, sessionID string) error {
	if c == nil {
		return playback.ErrSessionNotFound
	}
	return c.playback.stopPlaybackSessionByID(ctx, sessionID, false)
}

func (h *PlaybackHandler) rememberRealtimeCommand(commandID, sessionID string, name playback.CommandName) {
	if h == nil || commandID == "" || sessionID == "" {
		return
	}

	h.realtimeCommandMu.Lock()
	h.realtimeCommands[commandID] = playbackCommandRecord{
		SessionID: sessionID,
		Name:      name,
	}
	h.realtimeCommandMu.Unlock()
}

func (h *PlaybackHandler) forgetRealtimeCommand(commandID string) {
	if h == nil || commandID == "" {
		return
	}

	h.realtimeCommandMu.Lock()
	delete(h.realtimeCommands, commandID)
	h.realtimeCommandMu.Unlock()
}

func (h *PlaybackHandler) getRealtimeCommand(commandID string) (playbackCommandRecord, bool) {
	if h == nil || commandID == "" {
		return playbackCommandRecord{}, false
	}

	h.realtimeCommandMu.Lock()
	record, ok := h.realtimeCommands[commandID]
	h.realtimeCommandMu.Unlock()
	return record, ok
}

func (h *PlaybackHandler) setRealtimeConnectionState(sessionID string, connected bool) bool {
	if h == nil || sessionID == "" {
		return false
	}

	type realtimeStateSetter interface {
		SetRealtimeConnection(sessionID string, connected bool) error
	}

	mgr, ok := h.sessionMgr.(realtimeStateSetter)
	if !ok {
		return false
	}

	session, err := h.sessionMgr.GetSession(sessionID)
	if err == nil && session != nil && session.HasRealtimeConnection == connected {
		return false
	}

	if err := mgr.SetRealtimeConnection(sessionID, connected); err != nil {
		if errors.Is(err, playback.ErrSessionNotFound) {
			return false
		}
		slog.Warn("failed to update realtime connection state", "session", sessionID, "connected", connected, "error", err)
		return false
	}
	if h.StreamTelemetry != nil {
		h.StreamTelemetry.SetRealtimeConnection(sessionID, connected)
	}
	return true
}

const maxPendingRealtimeResults = 32

// realtimeReceiptReadBudget bounds the progress-sequence read a pause or
// resume result makes on the socket reader; see defaultPlaybackControlDeadline.
const realtimeReceiptReadBudget = time.Second

type realtimeResultQueue struct {
	pending        []func()
	done           chan struct{}
	terminalQueued bool
}

// runRealtimeResult orders results through one worker per session. When a
// slow provider fills the queue, its last pending pause state is superseded
// by the newest one. Terminal finalization is always retained: the caller
// has already removed session authority, so later terminal results cannot
// enqueue another finalization for the same session.
func (h *PlaybackHandler) runRealtimeResult(sessionID string, background bool, apply func()) {
	h.realtimeResultMu.Lock()
	if h.realtimeResultQueues == nil {
		h.realtimeResultQueues = make(map[string]*realtimeResultQueue)
	}
	if queue := h.realtimeResultQueues[sessionID]; queue != nil {
		if queue.terminalQueued {
			h.realtimeResultMu.Unlock()
			return
		}
		if !background {
			queue.terminalQueued = true
		}
		if background && len(queue.pending) >= maxPendingRealtimeResults {
			queue.pending[len(queue.pending)-1] = apply
		} else {
			queue.pending = append(queue.pending, apply)
		}
		h.realtimeResultMu.Unlock()
		return
	}
	queue := &realtimeResultQueue{done: make(chan struct{}), terminalQueued: !background}
	h.realtimeResultQueues[sessionID] = queue
	h.realtimeResultMu.Unlock()
	run := func() {
		apply()
		for {
			h.realtimeResultMu.Lock()
			if len(queue.pending) == 0 {
				delete(h.realtimeResultQueues, sessionID)
				close(queue.done)
				h.realtimeResultMu.Unlock()
				return
			}
			next := queue.pending[0]
			queue.pending[0] = nil
			queue.pending = queue.pending[1:]
			h.realtimeResultMu.Unlock()
			next()
		}
	}
	if background {
		go run()
		return
	}
	run()
}

// setConfirmedPauseState applies a pause or resume the player confirmed over
// its control lane. When the state changed it syncs the admin session views
// and sends the watch-provider pause or resume scrobble, which the next
// progress report would otherwise skip because it sees no transition.
func (h *PlaybackHandler) setConfirmedPauseState(sessionID string, paused bool, receivedSequence int64) {
	type pauseSetter interface {
		SetPaused(sessionID string, paused bool, acceptedSequence int64, latest *playback.ProgressSampleV3) (bool, error)
	}
	mgr, ok := h.sessionMgr.(pauseSetter)
	if !ok {
		return
	}
	// Progress side effects hold this lock while they compare the old and new
	// pause state, so a report racing this change scrobbles it at most once.
	unlock := h.progressSideEffectLock(sessionID)
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	attempt, err := h.realtimeProgressAttempt(ctx, sessionID)
	cancel()
	if err != nil {
		slog.WarnContext(ctx, "failed to read progress sequence for confirmed pause", "session", sessionID, "error", err)
		return
	}
	var acceptedSequence int64
	if attempt != nil {
		acceptedSequence = attempt.LastSequence
	}
	// Another replica may have accepted progress without holding this live
	// session, so offer the latest accepted sample even at the receipt
	// sequence; otherwise the snapshot and scrobble below use a stale
	// position. Progress accepted after receipt is a newer player
	// observation, so its pause state also supersedes the command's.
	var latest *playback.ProgressSampleV3
	if attempt != nil {
		latest = attempt.LastSample
	}
	if acceptedSequence > receivedSequence {
		if attempt.LastSample == nil {
			return
		}
		paused = attempt.LastSample.IsPaused
	}
	changed, err := mgr.SetPaused(sessionID, paused, acceptedSequence, latest)
	if err != nil {
		if !errors.Is(err, playback.ErrSessionNotFound) {
			slog.Warn("failed to apply confirmed pause state", "session", sessionID, "playback_session_id", sessionID, "error", err)
		}
		return
	}
	if !changed {
		return
	}
	syncCtx, cancelSync := context.WithTimeout(context.Background(), 10*time.Second)
	h.syncSessionsNow(syncCtx, "realtime_pause")
	cancelSync()
	// A start scrobble still queued behind busy start workers must reach the
	// provider first, or it would land after this pause and leave the
	// provider playing. The admin views above are already up to date.
	h.waitForPlaybackStartSideEffectsV3(context.Background(), sessionID)
	if current, err := h.sessionMgr.GetSession(sessionID); err == nil && current != nil {
		providerCtx, cancelProvider := context.WithTimeout(context.Background(), 10*time.Second)
		h.scrobblePauseTransitionV2(providerCtx, current, !paused)
		cancelProvider()
	}
}

func (h *PlaybackHandler) realtimeProgressAttempt(ctx context.Context, sessionID string) (*playback.AttemptRecordV3, error) {
	if h.PlanStoreV3 == nil {
		return nil, nil
	}
	var err error
	for range 3 {
		var attempt *playback.AttemptRecordV3
		attempt, err = h.PlanStoreV3.GetAttempt(ctx, sessionID)
		if errors.Is(err, playback.ErrSessionNotFound) {
			return nil, nil // Bridge sessions may have no native attempt row.
		}
		if err == nil {
			return attempt, nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	return nil, err
}
