package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/playback"
)

var errRehomeOutcomeUnknown = errors.New("transcode move outcome unavailable")

func writeRehomeUnavailableV3(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "1")
	writeError(w, http.StatusServiceUnavailable, "unavailable", "Transcode move authority is temporarily unavailable")
}

func rehomeRuntimeWorkV3(candidate *playback.TranscodeSession) string {
	opts := candidate.Opts()
	if opts.TranscodeTransportID != "" {
		return opts.TranscodeTransportID
	}
	return opts.SessionID
}

func requiresCachedRehomeConfirmationV3(session *playback.Session, candidate *playback.TranscodeSession) bool {
	if candidate == nil {
		return false
	}
	_, pending := candidate.PendingRouteMoveV3()
	return pending || session.TranscodeNodeURL != "" || rehomeRuntimeWorkV3(candidate) != remoteTransportID(session)
}

// Every entry and post-flight local return uses this same decision. A known
// matching local runtime returns immediately without querying the plan store.
func (h *PlaybackHandler) readyLocalRehomeRuntimeV3(ctx context.Context, session *playback.Session, candidate *playback.TranscodeSession) (*playback.Session, *playback.TranscodeSession, error) {
	if !requiresCachedRehomeConfirmationV3(session, candidate) {
		return session, candidate, nil
	}
	return h.reconcileCachedRehomeV3(ctx, session, candidate)
}

// A runtime alongside a remote session pointer is a candidate, not evidence
// that its route committed. Ordinary confirmed local requests skip this path.
func (h *PlaybackHandler) reconcileCachedRehomeV3(ctx context.Context, observed *playback.Session, candidate *playback.TranscodeSession) (*playback.Session, *playback.TranscodeSession, error) {
	joinCtx, cancel := context.WithTimeout(ctx, replanReleaseTimeoutV3)
	defer cancel()
	guard, release, err := h.acquireRehomeRouteV3(joinCtx, observed.ID)
	if err != nil {
		return nil, nil, errors.Join(errRehomeOutcomeUnknown, err)
	}
	defer release()
	unlock := h.tm.LockSessionLifecycle(observed.ID)
	defer unlock()
	current, err := h.sessionMgr.GetSession(observed.ID)
	if err != nil || h.tm.GetTranscodeSession(observed.ID) != candidate {
		// A successor owns the live slot. Never close it using a stale pointer.
		return nil, nil, errors.Join(errRehomeOutcomeUnknown, err)
	}
	opts := candidate.Opts()
	work := rehomeRuntimeWorkV3(candidate)
	identity, pending := candidate.PendingRouteMoveV3()
	sameOwner := current.UserID == observed.UserID && current.ProfileID == observed.ProfileID && current.MediaFileID == observed.MediaFileID
	confirmed := guard.record != nil && validRehomeAttemptV3(guard.record, current) && sameOwner &&
		guard.record.FrozenRecipe.TranscodeNodeURL == "" && guard.record.FrozenRecipe.TranscodeTransportID == current.TranscodeTransportID
	if guard.record == nil && sameOwner && current.TranscodeNodeURL == "" {
		confirmed = true // legacy move already accepted by its local manager
	}
	confirmed = confirmed && work == remoteTransportID(current) && (!pending || identity.Matches(guard.record, work))
	if !confirmed {
		h.tm.CloseTranscodeSessionIf(observed.ID, candidate, "")
		return nil, nil, errRehomeOutcomeUnknown
	}
	if current.TranscodeNodeURL == "" {
		candidate.ConfirmRouteMoveV3()
		return current, candidate, nil
	}
	if current.TranscodeNodeURL != observed.TranscodeNodeURL || current.TranscodeTransportID != observed.TranscodeTransportID {
		return nil, nil, errRehomeOutcomeUnknown
	}
	mover, ok := h.sessionMgr.(transcodeExecutorMoverV3)
	if !ok {
		return nil, nil, errRehomeOutcomeUnknown
	}
	moved, err := mover.MoveTranscodeExecutor(current.ID, playback.TranscodeRoute{NodeURL: current.TranscodeNodeURL, TransportID: current.TranscodeTransportID}, playback.TranscodeExecutorMove{Execution: string(noderouting.ExecutionAPI), TranscodeHWAccel: opts.EffectiveEncoderHWAccel()})
	if err != nil || !moved {
		return nil, nil, errors.Join(errRehomeOutcomeUnknown, err)
	}
	current, err = h.sessionMgr.GetSession(current.ID)
	if err != nil {
		return nil, nil, errors.Join(errRehomeOutcomeUnknown, err)
	}
	candidate.ConfirmRouteMoveV3()
	h.retireUnreachableTransportV3(ctx, observed.TranscodeNodeURL, remoteTransportID(observed), nil)
	return current, candidate, nil
}

type rehomeRouteV3 struct {
	owned  playback.RouteSessionV3
	record *playback.AttemptRecordV3
}

func (h *PlaybackHandler) acquireRehomeRouteV3(ctx context.Context, sessionID string) (*rehomeRouteV3, func(), error) {
	releaseSlot, err := h.acquireReplanSlotV3(ctx)
	if err != nil {
		return nil, nil, err
	}
	unlockLocal, err := h.lockReplanV3(ctx, sessionID)
	if err != nil {
		releaseSlot()
		return nil, nil, err
	}
	release := func() { unlockLocal(); releaseSlot() }
	if h.PlanStoreV3 == nil {
		return &rehomeRouteV3{}, release, nil
	}
	store, ok := h.PlanStoreV3.(playback.RouteSessionStoreV3)
	if !ok {
		release()
		return nil, nil, errors.New("plan store cannot serialize executor moves")
	}
	owned, err := store.AcquireRouteSession(ctx, sessionID)
	if err != nil {
		release()
		return nil, nil, err
	}
	releaseOwned := func() { owned.Close(); release() }
	record, err := owned.GetAttempt(ctx)
	if err != nil && !errors.Is(err, playback.ErrSessionNotFound) {
		releaseOwned()
		return nil, nil, err
	}
	return &rehomeRouteV3{owned: owned, record: record}, releaseOwned, nil
}

func validRehomeAttemptV3(record *playback.AttemptRecordV3, session *playback.Session) bool {
	return liveRehomeAttemptV3(record) && record.SessionID == session.ID && record.UserID == session.UserID && record.ProfileID == session.ProfileID &&
		record.EffectiveMediaFileID == session.MediaFileID
}

func liveRehomeAttemptV3(record *playback.AttemptRecordV3) bool {
	return record != nil && record.StoppedAt == nil && record.ExpiresAt.After(time.Now())
}

// adoptRehomeRouteV3 follows a move confirmed by another API. Only the SAME
// transport can be adopted; a replacement plan's different work is untouched.
func (h *PlaybackHandler) adoptRehomeRouteV3(ctx context.Context, guard *rehomeRouteV3, session *playback.Session, dead playback.TranscodeRoute) (bool, error) {
	if guard.record == nil {
		return false, nil
	}
	if !validRehomeAttemptV3(guard.record, session) {
		return false, playback.ErrAttemptStoppedV3
	}
	if guard.record.FrozenRecipe.TranscodeTransportID != session.TranscodeTransportID {
		return false, playback.ErrReplanSupersededV3
	}
	nodeURL := guard.record.FrozenRecipe.TranscodeNodeURL
	if nodeURL == dead.NodeURL {
		return false, nil
	}
	// A confirmed local route is rebuilt through the ordinary local move below;
	// it needs this API's hardware/capability validation and local runtime.
	if nodeURL == "" {
		return false, nil
	}
	reader, ok := h.NodePlanner.(interface {
		TranscodeNodeByURL(string) (*nodepool.Node, bool)
	})
	if !ok {
		return false, errors.New("cannot resolve confirmed executor")
	}
	node, found := reader.TranscodeNodeByURL(nodeURL)
	if !found || node == nil || !node.Enabled {
		return false, errors.New("confirmed executor unavailable")
	}
	move := playback.TranscodeExecutorMove{NodeURL: nodeURL, Execution: string(noderouting.ExecutionTranscode), ExecutionNodeID: node.ID}
	moved, err := h.commitRehomeRouteV3(ctx, guard, session, dead, move)
	return moved, err
}

// commitRehomeRouteV3 never acquires PG ownership under the TM lifecycle lock.
// An uncertain SQL write releases TM first, joins the old backend through the
// same store boundary, and only then attempts local adoption again.
func (h *PlaybackHandler) commitRehomeRouteV3(ctx context.Context, guard *rehomeRouteV3, session *playback.Session, dead playback.TranscodeRoute, move playback.TranscodeExecutorMove) (bool, error) {
	next := playback.TranscodeRoute{NodeURL: move.NodeURL, TransportID: session.TranscodeTransportID}
	mover, ok := h.sessionMgr.(transcodeExecutorMoverV3)
	if !ok {
		return false, errRehomeAuthorityUnavailable
	}
	unlock := h.tm.LockSessionLifecycle(session.ID)
	current, err := h.sessionMgr.GetSession(session.ID)
	if err != nil || current.TranscodeNodeURL != dead.NodeURL || current.TranscodeTransportID != dead.TransportID {
		unlock()
		return false, err
	}
	if guard.record == nil {
		moved, err := mover.MoveTranscodeExecutor(session.ID, dead, move)
		unlock()
		return moved, err
	}
	original := playback.NewRouteMoveIdentityV3(guard.record, next.TransportID)
	confirmed := guard.record.FrozenRecipe.TranscodeNodeURL == next.NodeURL && guard.record.FrozenRecipe.TranscodeTransportID == next.TransportID
	if !confirmed {
		confirmed, err = guard.owned.CompareAndSwap(ctx, *guard.record, next)
	}
	if err != nil || !confirmed {
		unlock()
		joinCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), replanReleaseTimeoutV3)
		settled, joinErr := guard.owned.GetAttempt(joinCtx)
		cancel()
		if joinErr != nil && !errors.Is(joinErr, playback.ErrSessionNotFound) {
			return false, fmt.Errorf("%w: %w", errRehomeOutcomeUnknown, errors.Join(err, joinErr))
		}
		guard.record = settled
		if liveRehomeAttemptV3(settled) && settled.FrozenRecipe.TranscodeTransportID == next.TransportID &&
			(!original.Matches(settled, next.TransportID) || settled.FrozenRecipe.TranscodeNodeURL != dead.NodeURL && settled.FrozenRecipe.TranscodeNodeURL != next.NodeURL) {
			// Another plan now owns this same transport on any executor. Its route proves
			// neither acceptance of our old start nor permission to DELETE it.
			return false, errors.Join(errRehomeOutcomeUnknown, playback.ErrReplanSupersededV3)
		}
		if !validRehomeAttemptV3(settled, session) || settled.FrozenRecipe.TranscodeNodeURL != next.NodeURL || settled.FrozenRecipe.TranscodeTransportID != next.TransportID {
			return false, errors.Join(err, playback.ErrReplanSupersededV3)
		}
		guard.record = settled
		unlock = h.tm.LockSessionLifecycle(session.ID)
	}
	moved, moveErr := mover.MoveTranscodeExecutor(session.ID, dead, move)
	unlock()
	if moveErr != nil || !moved {
		// Shared authority already accepted the candidate. A local adoption
		// failure cannot authorize stopping that committed executor.
		return false, errors.Join(errRehomeOutcomeUnknown, moveErr)
	}
	return true, nil
}
