package jellycompat

import (
	"context"
	"errors"
	"log/slog"
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
	position := upstreamSession.Position
	if positionOverride != nil {
		position = *positionOverride
	} else if position <= 0 && playSession.InitialSeekSeconds > 0 {
		position = playSession.InitialSeekSeconds
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
