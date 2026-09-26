package handlers

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// Administrator terminate, v2.
//
// The frozen v1 handler dispatches a terminate command and, when the lane is
// silent, ends the session after a deadline: revocation follows the client.
// On v2 the order is reversed and each fact is reported on its own:
//
//  1. The server first ends the session: the live session and its transcode
//     are stopped, the stop/history writer runs, and the stream deny marker
//     is written so no replica serves the session's tokens again.
//  2. Then the client dismissal is dispatched as best effort over the existing
//     command lane. No wait for an acknowledgement.
//  3. The receipt carries authority_revoked and client_notified separately; it
//     never promises that buffered media stops instantly.
//
// Repeating terminate on a session this replica no longer holds is unknown
// (404), as on the bridge.

const (
	AdminTerminateDeliveryDispatched  = "dispatched"
	AdminTerminateDeliveryUnavailable = "unavailable"
	AdminTerminateDeliveryFailed      = "failed"
	AdminTerminateDeliveryNone        = "none"

	// AdminTerminateDurableStopped is the only durable state a terminate
	// leaves: the session is stopped and its tokens are denied.
	AdminTerminateDurableStopped = "stopped"
)

// ErrAdminTerminateUnavailable means the playback handler is not wired.
var ErrAdminTerminateUnavailable = errors.New("administrator terminate is unavailable")

// AdminTerminateInput is one administrator terminate.
type AdminTerminateInput struct {
	SessionID string
	ActorID   int
	Reason    string
}

// AdminTerminateView reports the two facts separately.
type AdminTerminateView struct {
	SessionID        string
	AuthorityRevoked bool
	AlreadyRevoked   bool
	DurableState     string
	ClientNotified   bool
	Delivery         string
	CommandID        string
}

// AdminTerminateAvailable reports whether terminate can be served from this
// process: the playback handler and its session manager are wired. The
// command lane is optional; without it the client is simply not notified.
func (h *AdminPlaybackControlHandler) AdminTerminateAvailable() bool {
	return h != nil && h.playback != nil && h.playback.sessionMgr != nil
}

// Terminate stops first, then notifies best effort.
func (h *AdminPlaybackControlHandler) Terminate(ctx context.Context, in AdminTerminateInput) (AdminTerminateView, error) {
	if !h.AdminTerminateAvailable() {
		return AdminTerminateView{}, ErrAdminTerminateUnavailable
	}
	if in.SessionID == "" || in.ActorID <= 0 {
		return AdminTerminateView{}, ErrAdminPlaybackCommandInvalid
	}
	view := AdminTerminateView{SessionID: in.SessionID, Delivery: AdminTerminateDeliveryNone, DurableState: AdminTerminateDurableStopped}

	// Terminates for one session serialize so a repeat observes the first
	// outcome instead of racing it.
	unlock := h.terminateLock(in.SessionID)
	defer unlock()

	// 1. Stop the session (user-initiated: the recipe card is deleted, the
	// stop/history writer runs) and deny its tokens everywhere. The attempt
	// row is stopped too, under the server-minted stop id, so progress after
	// a terminate is refused and a start replay reports session_expired on
	// every replica, not just the one that held the live session.
	if err := h.playback.stopPlaybackSessionByID(ctx, in.SessionID, true); err != nil {
		if !errors.Is(err, playback.ErrSessionNotFound) || !h.playback.attemptLive(ctx, in.SessionID) {
			return AdminTerminateView{}, err
		}
		// No live copy here, but the attempt row is live: another replica
		// holds the session. Revoke durably; that replica's copy is reaped
		// by the deny marker and the stopped row.
	}
	h.playback.markAttemptStoppedServerSide(ctx, in.SessionID)
	view.AuthorityRevoked = true
	// The session is gone on this replica; its sequenced command ledger
	// goes with it (a later command finds no session and answers 404).
	h.dropCommandLedger(in.SessionID)

	// 2. Best-effort client dismissal on the existing lane. The session is
	// already removed, so the dispatcher's own session lookup would refuse;
	// the hub lane may still be open until the client's socket notices.
	view.CommandID, view.Delivery, view.ClientNotified = h.notifyTerminated(in)
	return view, nil
}

// notifyTerminated sends the terminate command on the lane if one is open.
func (h *AdminPlaybackControlHandler) notifyTerminated(in AdminTerminateInput) (commandID, delivery string, notified bool) {
	pb := h.playback
	if pb.RealtimeHub == nil {
		return "", AdminTerminateDeliveryUnavailable, false
	}
	commandID = uuid.NewString()
	command, err := playback.NewCommandEnvelope(in.SessionID, commandID, playback.CommandTerminate, nil)
	if err != nil {
		return commandID, AdminTerminateDeliveryFailed, false
	}
	command.Reason = in.Reason
	command.IssuedBy = &playback.CommandIssuedBy{Kind: eventsAdminRole}
	command.DeadlineMS = int(defaultPlaybackControlDeadline / time.Millisecond)
	if err := pb.RealtimeHub.Send(in.SessionID, command); err != nil {
		if errors.Is(err, playback.ErrRealtimeConnectionNotFound) {
			return commandID, AdminTerminateDeliveryUnavailable, false
		}
		slog.Warn("terminate notification failed after revocation", "session", in.SessionID, "playback_session_id", in.SessionID, "error", err)
		return commandID, AdminTerminateDeliveryFailed, false
	}
	return commandID, AdminTerminateDeliveryDispatched, true
}

// adminTerminateLock is one session's terminate lock with the number of
// terminates holding or waiting for it; the entry is removed at zero.
type adminTerminateLock struct {
	mu   sync.Mutex
	refs int
}

// terminateLock acquires the per-session terminate lock and returns its
// release. The map entry is reference counted so a replica does not keep a
// mutex for every session ever terminated.
func (h *AdminPlaybackControlHandler) terminateLock(sessionID string) func() {
	h.terminateMu.Lock()
	if h.terminateLocks == nil {
		h.terminateLocks = map[string]*adminTerminateLock{}
	}
	lock := h.terminateLocks[sessionID]
	if lock == nil {
		lock = &adminTerminateLock{}
		h.terminateLocks[sessionID] = lock
	}
	lock.refs++
	h.terminateMu.Unlock()
	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		h.terminateMu.Lock()
		lock.refs--
		if lock.refs == 0 && h.terminateLocks[sessionID] == lock {
			delete(h.terminateLocks, sessionID)
		}
		h.terminateMu.Unlock()
	}
}

// terminateLockCount is test-only introspection of the lock map size.
func (h *AdminPlaybackControlHandler) terminateLockCount() int {
	h.terminateMu.Lock()
	defer h.terminateMu.Unlock()
	return len(h.terminateLocks)
}
