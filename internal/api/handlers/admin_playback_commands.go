package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// Sequenced administrator playback commands (v2 port of pause, resume, stop
// and message).
//
// The frozen v1 handlers mint a fresh command id on every call and dispatch
// unconditionally, so a delayed retry that lands after the opposite command
// reverts the newer playback state. The v2 port carries an ordered command
// identity chosen by the client: a canonical UUID command_id and a positive
// sequence scoped to the playback session. The server keeps one ledger per
// session and applies each identity at most once:
//
//   - a new command_id with a sequence above the session's latest applied
//     sequence is dispatched once and recorded with its delivery (202,
//     outcome "applied"); a failed dispatch records nothing;
//   - the same command_id, sequence, action, actor and payload again is a
//     replay of the recorded receipt (200, outcome "replayed"), nothing is
//     dispatched;
//   - the same command_id with a different sequence, action, actor or payload
//     is an idempotency conflict (409);
//   - a new command_id whose sequence is at or below the latest applied one is
//     stale and refused (409) so it can never revert newer state. The refusal
//     carries the latest applied sequence: several administrators share one
//     ledger but no counter, so a client whose allocation runs behind (a
//     clock behind another browser's, a step backwards) reissues above it
//     instead of being locked out for as long as the skew lasts.
//
// The ledger lives with the realtime lane the command is delivered on: both
// are per-process, like the v1 dispatch itself. It is bounded per session and
// released once the session is gone: terminate and the stop fallback drop it
// directly, and a throttled sweep on the command path prunes the ledgers of
// sessions that ended by any other route (client stop, liveness reap), so a
// long-lived replica does not retain one entry per session ever commanded.

// Command outcomes and delivery states on the wire.
const (
	AdminPlaybackCommandApplied  = "applied"
	AdminPlaybackCommandReplayed = "replayed"

	AdminPlaybackDeliveryDispatched        = "dispatched"
	AdminPlaybackDeliveryFallbackScheduled = "fallback_scheduled"

	// adminPlaybackCommandLedgerLimit bounds the receipts remembered per
	// session; the oldest sequence is evicted first.
	adminPlaybackCommandLedgerLimit = 256

	// AdminPlaybackCommandMaxSequence is the largest sequence accepted:
	// 2^53-1, so every client, including the web client working in
	// JavaScript numbers, represents the latest applied sequence exactly
	// and can allocate above it. The v2 schema declares the same maximum.
	AdminPlaybackCommandMaxSequence = 1<<53 - 1

	// adminPlaybackLedgerSweepInterval throttles the sweep that prunes the
	// ledgers of ended sessions; it runs on the command path, so the map
	// holds at most the sessions commanded within one interval plus the
	// live ones.
	adminPlaybackLedgerSweepInterval = time.Minute

	// display_message payload keys, matching the frozen v1 HandleMessageSession.
	displayMessageTitleKey = "title"
	displayMessageTextKey  = "message"
)

// Errors the sequenced command path reports; each transport renders its own shape.
var (
	ErrAdminPlaybackCommandUnavailable = errors.New("playback control is unavailable")
	ErrAdminPlaybackCommandInvalid     = errors.New("invalid playback command")
	ErrAdminPlaybackCommandStale       = errors.New("playback command sequence is behind the session's latest applied command")
	ErrAdminPlaybackCommandConflict    = errors.New("playback command identity was already applied with different content")
	ErrAdminPlaybackRealtimeRequired   = errors.New("realtime connection unavailable for playback session")
)

// AdminPlaybackCommandStaleError is the stale refusal with the session's
// latest applied sequence, so the caller can allocate above it. It matches
// ErrAdminPlaybackCommandStale under errors.Is.
type AdminPlaybackCommandStaleError struct {
	Latest int64
}

func (e *AdminPlaybackCommandStaleError) Error() string {
	return ErrAdminPlaybackCommandStale.Error() + " (" + strconv.FormatInt(e.Latest, 10) + ")"
}

func (e *AdminPlaybackCommandStaleError) Is(target error) bool {
	return target == ErrAdminPlaybackCommandStale
}

// AdminPlaybackCommandInput is one sequenced command from an acting administrator.
type AdminPlaybackCommandInput struct {
	SessionID  string
	CommandID  string
	Sequence   int64
	Name       playback.CommandName
	ActorID    int
	Reason     string
	Title      string
	Message    string
	DeadlineMS int
}

// AdminPlaybackCommandView is the receipt a command identity resolves to.
type AdminPlaybackCommandView struct {
	CommandID string
	Sequence  int64
	Outcome   string
	Delivery  string
}

type adminPlaybackCommandReceipt struct {
	sequence int64
	name     playback.CommandName
	actorID  int
	payload  string
	delivery string
}

type adminPlaybackCommandLedger struct {
	latest   int64
	receipts map[string]adminPlaybackCommandReceipt
}

// adminPlaybackLedgerSessions is the slice of the session manager the ledger
// sweep needs.
type adminPlaybackLedgerSessions interface {
	GetSession(sessionID string) (*playback.Session, error)
}

// AdminPlaybackCommandsAvailable reports whether sequenced commands can be
// dispatched from this process.
func (h *AdminPlaybackControlHandler) AdminPlaybackCommandsAvailable() bool {
	return h != nil && h.playback != nil && h.playback.CommandDispatcher != nil
}

// Command applies one sequenced administrator command to a playback session.
func (h *AdminPlaybackControlHandler) Command(ctx context.Context, in AdminPlaybackCommandInput) (AdminPlaybackCommandView, error) {
	if !h.AdminPlaybackCommandsAvailable() {
		return AdminPlaybackCommandView{}, ErrAdminPlaybackCommandUnavailable
	}
	if in.SessionID == "" || in.Sequence <= 0 || in.Sequence > AdminPlaybackCommandMaxSequence || in.ActorID <= 0 || !canonicalCommandID(in.CommandID) {
		return AdminPlaybackCommandView{}, ErrAdminPlaybackCommandInvalid
	}
	var payload json.RawMessage
	switch in.Name {
	case playback.CommandPause, playback.CommandUnpause, playback.CommandStop:
	case playback.CommandDisplayMessage:
		if in.Message == "" {
			return AdminPlaybackCommandView{}, ErrAdminPlaybackCommandInvalid
		}
		encoded, err := json.Marshal(map[string]string{displayMessageTitleKey: in.Title, displayMessageTextKey: in.Message})
		if err != nil {
			return AdminPlaybackCommandView{}, err
		}
		payload = encoded
	default:
		return AdminPlaybackCommandView{}, ErrAdminPlaybackCommandInvalid
	}

	receipt := adminPlaybackCommandReceipt{sequence: in.Sequence, name: in.Name, actorID: in.ActorID, payload: string(payload) + "\x00" + in.Reason}

	// Session lookup, admission, dispatch and recording all happen under the
	// ledger lock. The lane is already serialized per session and the
	// dispatcher only performs one bounded lane write, so holding the lock
	// through dispatch costs nothing and means a concurrent duplicate or stale
	// command can never be dispatched alongside the winner, nor observe a
	// receipt whose delivery is still unknown: it either waits and replays the
	// completed receipt, or the dispatch failed, the slot was released, and
	// the duplicate is admitted on its own. Failures are never recorded. The
	// lookup is under the lock too, so a session that ends between lookup and
	// admission cannot be given a fresh ledger after its own was dropped.
	h.commandMu.Lock()
	defer h.commandMu.Unlock()
	session, err := h.playback.sessionMgr.GetSession(in.SessionID)
	if err != nil {
		if errors.Is(err, playback.ErrSessionNotFound) {
			delete(h.commandLedgers, in.SessionID)
		}
		return AdminPlaybackCommandView{}, err
	}
	if h.commandLedgers == nil {
		h.commandLedgers = map[string]*adminPlaybackCommandLedger{}
	}
	h.sweepCommandLedgersLocked(time.Now())
	ledger := h.commandLedgers[in.SessionID]
	if ledger == nil {
		ledger = &adminPlaybackCommandLedger{receipts: map[string]adminPlaybackCommandReceipt{}}
		h.commandLedgers[in.SessionID] = ledger
	}
	if prior, ok := ledger.receipts[in.CommandID]; ok {
		if prior.sequence != receipt.sequence || prior.name != receipt.name || prior.actorID != receipt.actorID || prior.payload != receipt.payload {
			return AdminPlaybackCommandView{}, ErrAdminPlaybackCommandConflict
		}
		return AdminPlaybackCommandView{CommandID: in.CommandID, Sequence: in.Sequence, Outcome: AdminPlaybackCommandReplayed, Delivery: prior.delivery}, nil
	}
	if in.Sequence <= ledger.latest {
		return AdminPlaybackCommandView{}, &AdminPlaybackCommandStaleError{Latest: ledger.latest}
	}
	if requiresLivePlaybackControl(in.Name) && (session == nil || !session.HasRealtimeConnection) {
		return AdminPlaybackCommandView{}, ErrAdminPlaybackRealtimeRequired
	}
	delivery, err := h.dispatchSequenced(ctx, in, payload)
	if err != nil {
		return AdminPlaybackCommandView{}, err
	}
	receipt.delivery = delivery
	ledger.latest = in.Sequence
	ledger.receipts[in.CommandID] = receipt
	ledger.trim()
	return AdminPlaybackCommandView{CommandID: in.CommandID, Sequence: in.Sequence, Outcome: AdminPlaybackCommandApplied, Delivery: delivery}, nil
}

// dispatchSequenced sends the command exactly as the frozen v1 handlers do:
// display_message is best effort over the realtime lane; pause, resume and
// stop carry a bounded deadline and stop falls back to ending the session
// when the lane is absent or silent.
func (h *AdminPlaybackControlHandler) dispatchSequenced(_ context.Context, in AdminPlaybackCommandInput, payload json.RawMessage) (string, error) {
	command, err := playback.NewCommandEnvelope(in.SessionID, in.CommandID, in.Name, payload)
	if err != nil {
		return "", err
	}
	command.Reason = in.Reason
	command.IssuedBy = &playback.CommandIssuedBy{Kind: eventsAdminRole}

	if in.Name == playback.CommandDisplayMessage {
		result := h.playback.CommandDispatcher.DispatchToSession(command, 0, nil)
		if result.DispatchErr != nil {
			if errors.Is(result.DispatchErr, playback.ErrRealtimeConnectionNotFound) {
				return "", ErrAdminPlaybackRealtimeRequired
			}
			return "", result.DispatchErr
		}
		return AdminPlaybackDeliveryDispatched, nil
	}

	deadline := boundedPlaybackControlDeadline(in.DeadlineMS)
	command.DeadlineMS = int(deadline / time.Millisecond)
	fallback := func() {
		h.playback.forgetRealtimeCommand(in.CommandID)
		err := h.playback.stopPlaybackSessionByID(context.Background(), in.SessionID, true)
		// The ledger goes only once the session is gone. A stop that failed
		// leaves the session live, and its applied-once ordering with it.
		if err == nil || errors.Is(err, playback.ErrSessionNotFound) {
			h.dropCommandLedger(in.SessionID)
		}
	}
	h.playback.rememberRealtimeCommand(in.CommandID, in.SessionID, in.Name)
	result := h.playback.CommandDispatcher.DispatchToSession(command, deadline, fallback)
	if result.DispatchErr == nil {
		return AdminPlaybackDeliveryDispatched, nil
	}
	h.playback.forgetRealtimeCommand(in.CommandID)
	if errors.Is(result.DispatchErr, playback.ErrRealtimeConnectionNotFound) {
		time.AfterFunc(deadline, fallback)
		return AdminPlaybackDeliveryFallbackScheduled, nil
	}
	return "", result.DispatchErr
}

func (h *AdminPlaybackControlHandler) dropCommandLedger(sessionID string) {
	h.commandMu.Lock()
	delete(h.commandLedgers, sessionID)
	h.commandMu.Unlock()
}

// sweepCommandLedgersLocked prunes the ledgers of sessions the manager no
// longer holds, at most once per adminPlaybackLedgerSweepInterval. Called
// with commandMu held; the manager's read lock nests inside it, the same
// order the dispatch under the ledger lock already establishes.
func (h *AdminPlaybackControlHandler) sweepCommandLedgersLocked(now time.Time) {
	if !h.commandSweptAt.IsZero() && now.Sub(h.commandSweptAt) < adminPlaybackLedgerSweepInterval {
		return
	}
	h.commandSweptAt = now
	var sessions adminPlaybackLedgerSessions
	if h.playback != nil && h.playback.sessionMgr != nil {
		sessions = h.playback.sessionMgr
	}
	if sessions == nil {
		return
	}
	for sessionID := range h.commandLedgers {
		if _, err := sessions.GetSession(sessionID); errors.Is(err, playback.ErrSessionNotFound) {
			delete(h.commandLedgers, sessionID)
		}
	}
}

// trim evicts the lowest-sequence receipts beyond the per-session bound.
func (l *adminPlaybackCommandLedger) trim() {
	for len(l.receipts) > adminPlaybackCommandLedgerLimit {
		oldestID, oldest := "", int64(0)
		for id, r := range l.receipts {
			if oldestID == "" || r.sequence < oldest {
				oldestID, oldest = id, r.sequence
			}
		}
		delete(l.receipts, oldestID)
	}
}

func canonicalCommandID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}

// commandLedgerState is test-only introspection of one session's ledger.
func (h *AdminPlaybackControlHandler) commandLedgerState(sessionID string) (latest int64, receipts int, ok bool) {
	h.commandMu.Lock()
	defer h.commandMu.Unlock()
	ledger, ok := h.commandLedgers[sessionID]
	if !ok {
		return 0, 0, false
	}
	return ledger.latest, len(ledger.receipts), true
}

// commandLedgerCount is test-only introspection of the ledger map size.
func (h *AdminPlaybackControlHandler) commandLedgerCount() int {
	h.commandMu.Lock()
	defer h.commandMu.Unlock()
	return len(h.commandLedgers)
}
