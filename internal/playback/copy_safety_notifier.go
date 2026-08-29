package playback

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// CopySafetyInvalidationDeadline bounds how long a client has to ack a
// plan_invalidated command and report the replan it triggered. It is long
// enough for a client to run a full replan round trip and short enough that a
// wedged player is not left decoding a stream its decoder will desync on. When
// it expires the session is stopped, which is the same recovery an
// unnegotiated client gets.
const CopySafetyInvalidationDeadline = 8 * time.Second

// CopySafetySessionSettleWindow is how long a session is treated as still being
// established. A session is registered with the manager well before its start
// handler has written the v3 attempt record, and the client cannot open its
// realtime channel until it has the response, so a verdict landing inside that
// window would see a session that looks unreachable and stop one that is still
// being built — a hard failure exactly where the graceful path was meant to
// apply. Sessions that are already reachable are acted on immediately; only the
// ones that would otherwise be stopped wait out the remainder of this window
// and are then re-examined once.
const CopySafetySessionSettleWindow = 5 * time.Second

// copySafetyReconsiderTimeout bounds the work a deferred second look does. The
// scan context it inherited is already gone by then, so it carries no deadline
// of its own.
const copySafetyReconsiderTimeout = 30 * time.Second

// CommandIssuedByServer marks a command the server originated on its own,
// distinct from the "admin" commands an operator sends from the admin surface.
const CommandIssuedByServer = "server"

type copySafetySessionLookup interface {
	GetSessionsByMediaFileID(fileID int) []*Session
}

// copySafetyAttemptLookup reads the durable attempt for a live session: the
// plan currently issued for it and the features its client negotiated.
type copySafetyAttemptLookup interface {
	GetAttempt(ctx context.Context, sessionID string) (*AttemptRecordV3, error)
}

// CopySafetySessionControl is the small slice of playback-session lifecycle the
// notifier needs and does not own. The API handler implements it: it is the
// component that tracks realtime commands by ID and knows how to tear a
// playback session down completely.
type CopySafetySessionControl interface {
	// RememberRealtimeCommand records a dispatched command so the realtime
	// result handler can attribute it back to this notifier.
	RememberRealtimeCommand(commandID, sessionID string, name CommandName)
	// ForgetRealtimeCommand drops a command that was never delivered.
	ForgetRealtimeCommand(commandID string)
	// StopSession ends a playback session, which is the fallback whenever the
	// command cannot be delivered or is not honored.
	StopSession(ctx context.Context, sessionID string) error
}

// CopySafetyNotifier switches live sessions off a video stream-copy route after
// the asynchronous H.264 copy-safety scan reports the source is unsafe to copy.
//
// Playback no longer waits for that scan, so a session can already be running a
// remux when the verdict lands. For a client that negotiated
// FeaturePlanInvalidatedV3 and is connected, the notifier pushes a
// plan_invalidated command and lets the client replan itself. Every other v3
// session — no feature, no realtime connection, no ack, or a rejected result —
// is stopped, and the client's ordinary recovery mints a fresh attempt that
// plans against the now-persisted verdict and lands on a transcode.
// Jellyfin-compatibility sessions are exempt from the stop because their route
// decision never consults the verdict; see consider.
//
// Delivery is in-process only, matching the other realtime notifiers: it acts
// on the sessions this replica owns, which are exactly the ones whose realtime
// connections it holds.
type CopySafetyNotifier struct {
	sessions   copySafetySessionLookup
	attempts   copySafetyAttemptLookup
	dispatcher *CommandDispatcher
	control    CopySafetySessionControl
	deadline   time.Duration
	settle     time.Duration
}

// NewCopySafetyNotifier returns a notifier, or nil when a dependency it cannot
// work without is missing. A nil notifier is safe to call.
func NewCopySafetyNotifier(
	sessions copySafetySessionLookup,
	attempts copySafetyAttemptLookup,
	dispatcher *CommandDispatcher,
	control CopySafetySessionControl,
) *CopySafetyNotifier {
	if sessions == nil || control == nil {
		return nil
	}
	return &CopySafetyNotifier{
		sessions:   sessions,
		attempts:   attempts,
		dispatcher: dispatcher,
		control:    control,
		deadline:   CopySafetyInvalidationDeadline,
		settle:     CopySafetySessionSettleWindow,
	}
}

// copySafetyDisposition is what one call to consider did with a session, and
// therefore whether the post-settle sweep still owes it a second look.
type copySafetyDisposition int

const (
	// copySafetyUnresolved: consider reached no decision about this session. It
	// did not look like it was stream-copying this file — which is the ordinary
	// answer for an unrelated session, and also the answer during the brief
	// window where a replan has already moved the live session but has not yet
	// committed the durable attempt that names the new file and plan. Only the
	// sweep can tell those apart, so the session stays eligible for it.
	copySafetyUnresolved copySafetyDisposition = iota
	// copySafetyDisposed: consider dealt with the session — invalidated it,
	// stopped it, scheduled its own deferred second look, or deliberately
	// exempted it. The sweep must leave it alone.
	copySafetyDisposed
)

// VideoCopyUnsafe reports that fileID cannot be video stream-copied after all.
// Sessions that are not on a copy route for that file are left alone.
//
// The verdict can land in the gap between a plan being decided and its session
// becoming visible to the lookup — the scan and the start path race, and the
// scan can win by milliseconds. One immediate pass would miss such a session
// entirely and leave it playing a route the verdict just condemned, so a second
// file-wide look runs after the settle window for sessions that appeared late.
//
// Only sessions the first pass actually disposed of are excluded from that
// sweep. A session the first pass merely could not classify has to stay
// eligible: mid-replan, the live session already names the replacement file
// while the durable attempt still names the previous one, and the durable
// record wins the identity test — so the session reads as "serving another
// file" for as long as the commit takes. Marking it seen there would strand it
// permanently, because the persisted verdict also stops any later scan from
// re-notifying. Re-considering a genuinely unrelated session in the sweep costs
// one plan-store read and re-runs every check, so the conservative direction is
// free.
func (n *CopySafetyNotifier) VideoCopyUnsafe(ctx context.Context, fileID int) {
	if n == nil || fileID <= 0 {
		return
	}

	seen := make(map[string]struct{})
	for _, session := range n.sessions.GetSessionsByMediaFileID(fileID) {
		if n.consider(ctx, session, fileID, true) == copySafetyDisposed {
			seen[session.ID] = struct{}{}
		}
	}
	n.sweepLateSessionsAfter(ctx, fileID, seen, n.settleWindow())
}

// sweepLateSessionsAfter re-lists the file's sessions once the settle window
// has passed and considers every one the immediate pass did not dispose of.
// Like reconsiderAfter, it must not inherit the scan context's cancellation.
func (n *CopySafetyNotifier) sweepLateSessionsAfter(ctx context.Context, fileID int, seen map[string]struct{}, wait time.Duration) {
	parent := context.WithoutCancel(ctx)
	go func() {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		<-timer.C
		ctx, cancel := context.WithTimeout(parent, copySafetyReconsiderTimeout)
		defer cancel()
		for _, session := range n.sessions.GetSessionsByMediaFileID(fileID) {
			if session == nil || session.ID == "" {
				continue
			}
			if _, handled := seen[session.ID]; handled {
				continue
			}
			// The settle window has already elapsed since the verdict; a session
			// still younger than that was planned after the verdict persisted and
			// should never have been given a copy route at all.
			n.consider(ctx, session, fileID, false)
		}
	}()
}

// consider decides what to do with one session the file lookup returned, and
// reports whether it disposed of it. maySettle is false on the deferred second
// look, so a session can never be postponed twice.
func (n *CopySafetyNotifier) consider(ctx context.Context, session *Session, fileID int, maySettle bool) copySafetyDisposition {
	if session == nil || session.ID == "" {
		return copySafetyUnresolved
	}
	record := n.attempt(ctx, session.ID)
	if !sessionServesFileV3(session, record, fileID) {
		return copySafetyUnresolved
	}
	if !sessionOnVideoCopyRouteV3(session, record) {
		return copySafetyUnresolved
	}
	if session.IsJellyfinCompat {
		// Stopping only helps a client whose recovery re-decides the route
		// against the verdict. The Jellyfin-protocol surface picks direct
		// stream from the device profile and the catalog version alone
		// (DeviceProfile.SupportsDirectStream) and never reads copy safety, so
		// a compat client would reconnect straight back onto the same remux —
		// the kill would be a pure mid-stream interruption with no remedy.
		// Teaching that surface to read the verdict is the fix; until it does,
		// leave these sessions playing.
		slog.InfoContext(ctx, "leaving a Jellyfin-compatibility session on a copy-unsafe route",
			"component", "playback",
			"session_id", session.ID,
			"file_id", fileID,
			"reason", PlanInvalidatedVideoCopyUnsafe,
		)
		return copySafetyDisposed
	}
	if maySettle && !n.canTellClient(session, record) {
		if wait := n.settleRemaining(session); wait > 0 {
			n.reconsiderAfter(ctx, session.ID, fileID, wait)
			return copySafetyDisposed
		}
	}
	n.invalidate(ctx, session, record, fileID)
	return copySafetyDisposed
}

// reconsiderAfter re-examines one session once the settle window has passed.
// The scan context dies as soon as the caller returns, so the deferred look is
// deliberately not bound to its cancellation.
func (n *CopySafetyNotifier) reconsiderAfter(ctx context.Context, sessionID string, fileID int, wait time.Duration) {
	parent := context.WithoutCancel(ctx)
	go func() {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		<-timer.C
		// The second look does its own plan-store read, which needs a bound of
		// its own now that the scan's is gone.
		ctx, cancel := context.WithTimeout(parent, copySafetyReconsiderTimeout)
		defer cancel()
		for _, session := range n.sessions.GetSessionsByMediaFileID(fileID) {
			if session == nil || session.ID != sessionID {
				continue
			}
			n.consider(ctx, session, fileID, false)
			return
		}
	}()
}

// settleRemaining reports how much of the settle window a session still has
// left. A session with no recorded start is treated as settled.
// settleWindow is the delay before the late-session sweep; zero settle keeps a
// small floor so the sweep still runs after the start path has had time to
// register the session.
func (n *CopySafetyNotifier) settleWindow() time.Duration {
	if n.settle <= 0 {
		return CopySafetySessionSettleWindow
	}
	return n.settle
}

func (n *CopySafetyNotifier) settleRemaining(session *Session) time.Duration {
	if n.settle <= 0 || session.StartedAt.IsZero() {
		return 0
	}
	remaining := n.settle - time.Since(session.StartedAt)
	if remaining <= 0 {
		return 0
	}
	return remaining
}

// canTellClient reports whether the session can be handed a plan_invalidated
// command instead of being stopped.
func (n *CopySafetyNotifier) canTellClient(session *Session, record *AttemptRecordV3) bool {
	_, ok := n.tellablePlanID(session, record)
	return ok
}

// tellablePlanID returns the plan a plan_invalidated command would withdraw,
// and whether the session can be told about it at all.
func (n *CopySafetyNotifier) tellablePlanID(session *Session, record *AttemptRecordV3) (string, bool) {
	if record == nil || n.dispatcher == nil || !session.HasRealtimeConnection {
		return "", false
	}
	if !HasFeatureV3(record.NormalizedRequest.ClientFeatures, FeaturePlanInvalidatedV3) {
		return "", false
	}
	planID := record.CurrentPlan.PlanID
	if planID == "" {
		planID = record.CurrentPlanID
	}
	if planID == "" {
		return "", false
	}
	return planID, true
}

func (n *CopySafetyNotifier) attempt(ctx context.Context, sessionID string) *AttemptRecordV3 {
	if n.attempts == nil {
		return nil
	}
	record, err := n.attempts.GetAttempt(ctx, sessionID)
	if err != nil {
		if !errors.Is(err, ErrSessionNotFound) {
			// A session with no attempt and a session whose attempt could not be
			// read lead to the same fallback, so the failure has to be visible:
			// otherwise a plan-store outage looks exactly like a fleet of
			// unnegotiated clients in the logs.
			slog.WarnContext(ctx, "could not read the playback attempt for a copy-unsafe session",
				"component", "playback", "session_id", sessionID, "error", err)
		}
		return nil
	}
	return record
}

func (n *CopySafetyNotifier) invalidate(ctx context.Context, session *Session, record *AttemptRecordV3, fileID int) {
	negotiated := record != nil && HasFeatureV3(record.NormalizedRequest.ClientFeatures, FeaturePlanInvalidatedV3)
	planID, tellable := n.tellablePlanID(session, record)

	if !tellable {
		slog.InfoContext(ctx, "stopping playback session on a copy-unsafe route",
			"component", "playback",
			"session_id", session.ID,
			"file_id", fileID,
			"reason", PlanInvalidatedVideoCopyUnsafe,
			"negotiated_plan_invalidated", negotiated,
			"realtime_connected", session.HasRealtimeConnection,
		)
		n.stop(ctx, session.ID, fileID)
		return
	}

	commandID := uuid.NewString()
	command, err := NewPlanInvalidatedCommand(session.ID, commandID, planID, PlanInvalidatedVideoCopyUnsafe)
	if err != nil {
		slog.WarnContext(ctx, "failed to encode plan invalidated command",
			"component", "playback", "session_id", session.ID, "file_id", fileID, "error", err)
		n.stop(ctx, session.ID, fileID)
		return
	}
	command.Reason = PlanInvalidatedVideoCopyUnsafe
	command.IssuedBy = &CommandIssuedBy{Kind: CommandIssuedByServer}
	command.DeadlineMS = int(n.commandDeadline() / time.Millisecond)

	sessionID := session.ID
	fallback := func() {
		n.control.ForgetRealtimeCommand(commandID)
		n.stop(context.WithoutCancel(ctx), sessionID, fileID)
	}

	n.control.RememberRealtimeCommand(commandID, sessionID, CommandPlanInvalidated)
	result := n.dispatcher.DispatchToSession(command, n.commandDeadline(), fallback)
	if result.DispatchErr != nil {
		// The command never reached the client, so nothing will ack it and the
		// tracker has already dropped it. Stop the session directly rather than
		// waiting out a deadline that will not fire.
		n.control.ForgetRealtimeCommand(commandID)
		slog.InfoContext(ctx, "plan invalidated command undeliverable; stopping session",
			"component", "playback", "session_id", sessionID, "file_id", fileID, "error", result.DispatchErr)
		n.stop(ctx, sessionID, fileID)
		return
	}
	slog.InfoContext(ctx, "playback plan invalidated",
		"component", "playback",
		"session_id", sessionID,
		"file_id", fileID,
		"plan_id", planID,
		"reason", PlanInvalidatedVideoCopyUnsafe,
		"command_id", commandID,
	)
}

func (n *CopySafetyNotifier) commandDeadline() time.Duration {
	if n.deadline <= 0 {
		return CopySafetyInvalidationDeadline
	}
	return n.deadline
}

func (n *CopySafetyNotifier) stop(ctx context.Context, sessionID string, fileID int) {
	if err := n.control.StopSession(ctx, sessionID); err != nil && !isSessionGoneV3(err) {
		slog.WarnContext(ctx, "failed to stop playback session on a copy-unsafe route",
			"component", "playback", "session_id", sessionID, "file_id", fileID, "error", err)
	}
}

func isSessionGoneV3(err error) bool {
	return errors.Is(err, ErrSessionNotFound)
}

// sessionServesFileV3 reports whether fileID is the source the session is
// actually delivering.
//
// The session lookup matches the requested file as well as the effective one,
// which is right for the additive notifiers that share it but wrong here: after
// the 4K guard or a version replan picks another edition, a session's requested
// file is an identity the session no longer streams a byte of. Withdrawing its
// route because a different edition turned out to be copy-unsafe would cost it
// a perfectly valid remux.
func sessionServesFileV3(session *Session, record *AttemptRecordV3, fileID int) bool {
	if session == nil {
		return false
	}
	if record != nil && record.EffectiveMediaFileID > 0 {
		return record.EffectiveMediaFileID == fileID
	}
	return session.MediaFileID == fileID
}

// sessionOnVideoCopyRouteV3 reports whether the session is currently serving a
// video stream-copy. The durable plan wins when there is one — it advances
// atomically with each completed replan, while the live play method can lag —
// and the session's own play method covers sessions with no v3 attempt at all
// (reconstructed sessions, and sessions whose attempt has expired).
//
// Direct play is deliberately not a copy route here: multi-PPS only breaks the
// avc1/fMP4 repackaging a remux performs, and the planner gates only the remux
// branches on it.
func sessionOnVideoCopyRouteV3(session *Session, record *AttemptRecordV3) bool {
	if session == nil {
		return false
	}
	if record != nil && record.CurrentPlan.Delivery != "" {
		switch record.CurrentPlan.Delivery {
		case DeliveryRemuxHLSV3, DeliveryRemuxProgressiveV3:
			return true
		default:
			return false
		}
	}
	method := session.BasePlayMethod
	if method == "" {
		method = session.PlayMethod
	}
	return method == PlayRemux
}
