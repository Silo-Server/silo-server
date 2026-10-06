package jellycompat

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

type compatScrobbleAction string

const (
	compatScrobbleStart compatScrobbleAction = "start"
	compatScrobblePause compatScrobbleAction = "pause"
	compatScrobbleStop  compatScrobbleAction = "stop"
)

const (
	// compatResumeScrobbleTolerance is how far a report may drift from where
	// the start scrobble places playback before the start is re-sent. It
	// exceeds startup buffering and report jitter, so ordinary play from the
	// start or from StartTimeTicks sends a single start.
	compatResumeScrobbleTolerance = 30.0
	// compatResumeScrobbleWindow bounds how long after a start a report may
	// still correct it. Clients seek to their resume point as playback
	// begins; a later jump is an ordinary seek, which scrobbles only on the
	// next pause or stop, as on the native API.
	compatResumeScrobbleWindow = 2 * time.Minute
)

func (h *PlaybackHandler) dispatchCompatScrobble(
	ctx context.Context,
	action compatScrobbleAction,
	playSession *PlaybackSession,
	upstreamSession *playback.Session,
	preferredSource *PlaybackMediaSource,
) error {
	return h.dispatchCompatScrobbleAt(ctx, action, playSession, upstreamSession, preferredSource, nil)
}

func (h *PlaybackHandler) dispatchCompatScrobbleAt(
	ctx context.Context,
	action compatScrobbleAction,
	playSession *PlaybackSession,
	upstreamSession *playback.Session,
	preferredSource *PlaybackMediaSource,
	positionOverride *float64,
) error {
	event, ok := h.compatScrobbleEvent(
		ctx, action, playSession, upstreamSession, preferredSource, positionOverride,
	)
	if !ok {
		return nil
	}
	return h.dispatchCompatScrobbleEvent(ctx, action, event)
}

func (h *PlaybackHandler) compatScrobbleEvent(
	ctx context.Context,
	action compatScrobbleAction,
	playSession *PlaybackSession,
	upstreamSession *playback.Session,
	preferredSource *PlaybackMediaSource,
	positionOverride *float64,
) (watchsync.ScrobbleEvent, bool) {
	if h == nil || h.WatchScrobbler == nil || playSession == nil || upstreamSession == nil ||
		upstreamSession.DisableProgressPersistence || playSession.ItemID == "" {
		return watchsync.ScrobbleEvent{}, false
	}
	scrobbleCtx, cancel := compatDetachedContext(ctx)
	defer cancel()

	source := compatScrobbleSource(playSession, upstreamSession, preferredSource)
	duration := 0.0
	if source != nil {
		duration = float64(source.Version.Duration)
	}
	position := compatScrobblePosition(playSession, upstreamSession)
	if positionOverride != nil {
		position = *positionOverride
	}
	completed := false
	if action == compatScrobbleStop && duration > 0 {
		_, completed, _ = userstore.ResolveProgressState(position, duration, h.playbackThresholds(scrobbleCtx))
	}
	event := watchsync.ResolveScrobbleIdentity(scrobbleCtx, h.StableIdentityResolver, watchsync.ScrobbleEvent{
		PlaybackSessionID: upstreamSession.ID,
		UserID:            upstreamSession.UserID,
		ProfileID:         upstreamSession.ProfileID,
		MediaItemID:       playSession.ItemID,
		PositionSeconds:   position,
		DurationSeconds:   duration,
		Completed:         completed,
		OccurredAt:        time.Now().UTC(),
	})
	return event, true
}

// compatScrobblePosition is the position a scrobble reports when the caller
// has no fresher sample: the upstream position, or the StartTimeTicks seek
// before the first report moves it.
func compatScrobblePosition(playSession *PlaybackSession, upstreamSession *playback.Session) float64 {
	position := upstreamSession.Position
	if position <= 0 && playSession.InitialSeekSeconds > 0 {
		position = playSession.InitialSeekSeconds
	}
	return position
}

// compatScrobbleLocks serializes the scrobble decisions of each upstream
// session. An upstream session lives in one node's session manager, and only
// that node sends its start or applies its reports, so a node-local lock
// covers both single-node and multi-node deployments.
type compatScrobbleLocks struct {
	mu    sync.Mutex
	locks map[string]*compatScrobbleLock
}

type compatScrobbleLock struct {
	sync.Mutex
	// holders counts the owner and waiters, so the entry is dropped once
	// nobody needs it.
	holders int
}

func (l *compatScrobbleLocks) lock(key string) (unlock func()) {
	l.mu.Lock()
	if l.locks == nil {
		l.locks = make(map[string]*compatScrobbleLock)
	}
	entry := l.locks[key]
	if entry == nil {
		entry = &compatScrobbleLock{}
		l.locks[key] = entry
	}
	entry.holders++
	l.mu.Unlock()
	entry.Lock()
	return func() {
		entry.Unlock()
		l.mu.Lock()
		entry.holders--
		if entry.holders == 0 {
			delete(l.locks, key)
		}
		l.mu.Unlock()
	}
}

// sendCompatResumeStart sends the start for a new or rebuilt upstream session
// and records it so a client report can still correct its position (#1712).
// Some clients resume through PositionTicks on their first Playing report
// instead of StartTimeTicks on PlaybackInfo, and a session revived mid-play
// starts again from zero.
//
// It holds the upstream session's scrobble lock, so a report that arrives
// while the start is being queued waits and then compares itself with the
// start's record. The start's position is sampled once and used for both the
// event and the record; the session manager's live session keeps moving as
// reports arrive.
func (h *PlaybackHandler) sendCompatResumeStart(
	ctx context.Context,
	playSession *PlaybackSession,
	upstreamSession *playback.Session,
	source *PlaybackMediaSource,
) {
	if h == nil || playSession == nil || upstreamSession == nil {
		return
	}
	unlock := h.compatScrobbleLocks.lock(upstreamSession.ID)
	defer unlock()
	sample := upstreamSession
	if h.sessionMgr != nil {
		if current, err := h.sessionMgr.GetSession(upstreamSession.ID); err == nil && current != nil {
			copy := *current
			sample = &copy
		}
	}
	position := compatScrobblePosition(playSession, sample)
	sentAt := time.Now()
	_ = h.dispatchCompatScrobbleAt(ctx, compatScrobbleStart, playSession, sample, source, &position)
	h.recordCompatResumeScrobble(playSession.ID, sample.ID, position, sentAt)
}

// recordCompatResumeScrobble stores the start sent for upstreamID, if that is
// still the play's upstream session. The durable store may replay this
// callback from another request after a failed write, so it only reads
// captured values and writes the session; a mismatch is skipped rather than
// failed, since a replay error would fail every later durable write for the
// session.
func (h *PlaybackHandler) recordCompatResumeScrobble(playSessionID, upstreamID string, position float64, sentAt time.Time) {
	if h == nil || h.playbackStore == nil || upstreamID == "" {
		return
	}
	_ = h.playbackStore.Update(playSessionID, func(current *PlaybackSession) error {
		if current.UpstreamSessionID == upstreamID {
			current.ResumeScrobbleUpstreamID = upstreamID
			current.ResumeScrobblePosition = position
			current.ResumeScrobbleSentAt = sentAt
		}
		return nil
	})
}

// clearCompatResumeScrobble ends the correction window once a correction,
// pause, or resume has been queued. It clears only the record identified by
// upstreamID and sentAt, so it cannot end the window of a start sent since for
// a replacement upstream session. Like recordCompatResumeScrobble, the
// callback is replay-safe.
func (h *PlaybackHandler) clearCompatResumeScrobble(playSessionID, upstreamID string, sentAt time.Time) {
	if h == nil || h.playbackStore == nil || upstreamID == "" {
		return
	}
	_ = h.playbackStore.Update(playSessionID, func(current *PlaybackSession) error {
		if current.ResumeScrobbleUpstreamID == upstreamID && current.ResumeScrobbleSentAt.Equal(sentAt) {
			current.ResumeScrobbleUpstreamID = ""
		}
		return nil
	})
}

// applyCompatReport moves the play's upstream session to a report's position
// and pause state and, when scrobble is set, sends the scrobble the report
// calls for. It returns the session manager's error, such as
// playback.ErrSessionNotFound for a reaped session.
//
// The snapshot, the update, and the scrobble decision run under the upstream
// session's scrobble lock, so overlapping reports apply one after another and
// each compares itself with the state the last one left. Their events are
// queued in that same order.
func (h *PlaybackHandler) applyCompatReport(
	ctx context.Context,
	playSession *PlaybackSession,
	source *PlaybackMediaSource,
	position float64,
	paused bool,
	scrobble bool,
) error {
	upstreamID := playSession.UpstreamSessionID
	unlock := h.compatScrobbleLocks.lock(upstreamID)
	defer unlock()
	var previous *playback.Session
	if current, err := h.sessionMgr.GetSession(upstreamID); err == nil && current != nil {
		copy := *current
		previous = &copy
	}
	if err := h.sessionMgr.UpdateProgress(upstreamID, position, paused); err != nil {
		return err
	}
	if scrobble && previous != nil {
		h.scrobbleCompatReport(ctx, playSession, previous, source, position, paused)
	}
	return nil
}

// scrobbleCompatReport sends the scrobble a playing or progress report calls
// for: a pause or resume when the pause state changed, or a start carrying the
// reported position when the report shows the resume start in the wrong spot.
// previous is the upstream session as it was before this report. The caller
// holds the upstream session's scrobble lock (see applyCompatReport).
//
// It reads the record fresh, so each report decides against the record the
// last one left. The record is consumed only after its event is queued; when
// queueing fails, a later report can still correct the start.
func (h *PlaybackHandler) scrobbleCompatReport(
	ctx context.Context,
	playSession *PlaybackSession,
	previous *playback.Session,
	source *PlaybackMediaSource,
	position float64,
	paused bool,
) {
	if h == nil || playSession == nil || previous == nil {
		return
	}
	pauseChanged := previous.IsPaused != paused
	if !pauseChanged && (paused || position <= 0) {
		return
	}
	var record *PlaybackSession
	if h.playbackStore != nil {
		// The report may have waited behind another start or correction. A
		// Stopped report or method switch may have ended this upstream session
		// meanwhile, and its terminal stop must stay the last event.
		current, ok := h.playbackStore.Get(playSession.ID)
		if !ok || current == nil || current.UpstreamSessionID != previous.ID {
			return
		}
		record = current
	}
	if !pauseChanged && !compatResumeScrobbleNeedsCorrection(record, previous.ID, position, time.Now()) {
		return
	}
	updated := *previous
	updated.Position = position
	updated.IsPaused = paused
	action := compatScrobbleStart
	if paused {
		action = compatScrobblePause
	}
	if err := h.dispatchCompatScrobbleAt(ctx, action, playSession, &updated, source, &position); err != nil {
		return
	}
	if record != nil && record.ResumeScrobbleUpstreamID == previous.ID {
		h.clearCompatResumeScrobble(playSession.ID, record.ResumeScrobbleUpstreamID, record.ResumeScrobbleSentAt)
	}
}

// compatResumeScrobbleNeedsCorrection reports whether a playing report for
// upstreamID shows the start scrobble placed playback in the wrong spot. While
// playing, the provider advances from the start's position, so the report is
// compared with that extrapolation. A zero report is never a resume point:
// clients send one while still seeking, and acting on it would replace a
// correct StartTimeTicks start with zero.
func compatResumeScrobbleNeedsCorrection(record *PlaybackSession, upstreamID string, reportedSeconds float64, now time.Time) bool {
	if record == nil || reportedSeconds <= 0 || upstreamID == "" ||
		record.ResumeScrobbleUpstreamID != upstreamID || record.UpstreamSessionID != upstreamID {
		return false
	}
	elapsed := now.Sub(record.ResumeScrobbleSentAt)
	if elapsed > compatResumeScrobbleWindow {
		return false
	}
	expected := record.ResumeScrobblePosition + math.Max(elapsed.Seconds(), 0)
	return math.Abs(reportedSeconds-expected) > compatResumeScrobbleTolerance
}

func (h *PlaybackHandler) dispatchCompatScrobbleEvent(
	ctx context.Context,
	action compatScrobbleAction,
	event watchsync.ScrobbleEvent,
) error {
	return h.dispatchCompatScrobbleEventConfirmed(ctx, action, event, false)
}

func (h *PlaybackHandler) dispatchCompatScrobbleEventConfirmed(
	ctx context.Context,
	action compatScrobbleAction,
	event watchsync.ScrobbleEvent,
	confirmStop bool,
) error {
	if h == nil || h.WatchScrobbler == nil {
		return nil
	}
	scrobbleCtx, cancel := compatDetachedContext(ctx)
	defer cancel()
	var err error
	switch action {
	case compatScrobblePause:
		err = h.WatchScrobbler.ScrobblePause(scrobbleCtx, event)
	case compatScrobbleStop:
		if confirmer, ok := h.WatchScrobbler.(PlaybackWatchStopConfirmer); confirmStop && ok {
			err = confirmer.ScrobbleStopConfirmed(scrobbleCtx, event)
		} else if confirmStop {
			err = errors.New("watch scrobbler does not support confirmed stops")
		} else {
			err = h.WatchScrobbler.ScrobbleStop(scrobbleCtx, event)
		}
	default:
		err = h.WatchScrobbler.ScrobbleStart(scrobbleCtx, event)
	}
	if err != nil {
		slog.WarnContext(scrobbleCtx, "failed to queue jellycompat watch provider scrobble",
			"component", "jellycompat",
			"action", action,
			"playback_session_id", event.PlaybackSessionID,
			"error", err,
		)
	}
	return err
}

func compatScrobbleSource(
	playSession *PlaybackSession,
	upstreamSession *playback.Session,
	preferredSource *PlaybackMediaSource,
) *PlaybackMediaSource {
	if preferredSource != nil {
		return preferredSource
	}
	if playSession == nil {
		return nil
	}
	if upstreamSession != nil {
		for _, source := range playSession.MediaSources {
			if source.FileID == upstreamSession.MediaFileID {
				copy := source
				return &copy
			}
		}
	}
	return firstMediaSource(playSession)
}

// compatScrobbleFallbackSession preserves enough authenticated report state to
// emit a terminal event after the in-memory upstream session has already been
// reaped. The compat play session remains the source of media identity.
func compatScrobbleFallbackSession(
	compatSession *Session,
	playSession *PlaybackSession,
	preferredSource *PlaybackMediaSource,
	position float64,
	positionKnown bool,
	isPaused bool,
) *playback.Session {
	if compatSession == nil || playSession == nil || playSession.UpstreamSessionID == "" || !positionKnown {
		return nil
	}
	source := compatScrobbleSource(playSession, nil, preferredSource)
	fileID := 0
	if source != nil {
		fileID = source.FileID
	}
	return &playback.Session{
		ID:                         playSession.UpstreamSessionID,
		UserID:                     compatSession.StreamAppUserID,
		ProfileID:                  compatSession.ProfileID,
		MediaFileID:                fileID,
		Position:                   position,
		IsPaused:                   isPaused,
		DisableProgressPersistence: !playSession.ProgressPersistenceKnown || playSession.DisableProgressPersistence,
	}
}
