package abs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// PlaybackSessionManager is the native Silo playback-session surface ABS needs
// to make compatibility playback visible to live admin/session monitoring.
type PlaybackSessionManager interface {
	StartSessionWithFilesContext(ctx context.Context, userID int, profileID string, effectiveFileID int, requestedFileID int, method playback.PlayMethod, transcodeAudio bool) (*playback.Session, error)
	UpdateProgress(sessionID string, position float64, isPaused bool) error
	UpdateStreamState(sessionID string, state playback.SessionStreamState) error
	BeginTransport(sessionID string) error
	EndTransport(sessionID string) error
	SetProgressPersistenceDisabled(sessionID string, disabled bool) error
	// FinishSession ends the play and runs the manager's finish hooks, which
	// write the admin playback history row.
	FinishSession(ctx context.Context, sessionID string) error
}

// PlaybackSessionSyncer flushes the local native-session snapshot into the
// shared admin live-session table.
type PlaybackSessionSyncer interface {
	SyncNow(ctx context.Context) error
}

func (h *Handler) startNativePlaybackSession(
	r *http.Request,
	a ctxAuth,
	files []*models.MediaFile,
	startPosition float64,
) (*playback.Session, error) {
	if h == nil || h.deps.NativeSessions == nil || len(files) == 0 {
		return nil, nil
	}
	userID, err := strconv.Atoi(a.UserID)
	if err != nil {
		return nil, fmt.Errorf("invalid ABS user id %q: %w", a.UserID, err)
	}

	file := files[0]
	sessionCtx := playback.WithClientInfo(r.Context(), absPlaybackClientInfoFromRequest(r))
	session, err := h.deps.NativeSessions.StartSessionWithFilesContext(
		sessionCtx,
		userID,
		a.ProfileID,
		file.ID,
		file.ID,
		playback.PlayDirect,
		false,
	)
	if err != nil {
		return nil, err
	}

	if err := h.deps.NativeSessions.SetProgressPersistenceDisabled(session.ID, true); err != nil {
		slog.WarnContext(r.Context(), "abs play: disable native progress persistence failed", "component", "audiobooks",
			"session_id", session.ID, "error", err)
	} else {
		session.DisableProgressPersistence = true
	}

	if startPosition > 0 {
		if err := h.deps.NativeSessions.UpdateProgress(session.ID, startPosition, false); err != nil {
			slog.WarnContext(r.Context(), "abs play: seed native session progress failed", "component", "audiobooks",
				"session_id", session.ID, "position", startPosition, "error", err)
		} else {
			session.Position = startPosition
			session.IsPaused = false
		}
	}

	streamBitrateKbps := 0
	if file.Bitrate > 0 {
		streamBitrateKbps = file.Bitrate
	}
	if err := h.deps.NativeSessions.UpdateStreamState(session.ID, playback.SessionStreamState{
		PlayMethod:        playback.PlayDirect,
		BasePlayMethod:    playback.PlayDirect,
		ClientIP:          requestClientIP(r),
		ClientName:        session.ClientName,
		ClientVersion:     session.ClientVersion,
		ClientUserAgent:   session.ClientUserAgent,
		StreamBitrateKbps: streamBitrateKbps,
	}); err != nil {
		slog.WarnContext(r.Context(), "abs play: update native session stream state failed", "component", "audiobooks",
			"session_id", session.ID, "error", err)
	} else {
		session.PlayMethod = playback.PlayDirect
		session.BasePlayMethod = playback.PlayDirect
		session.ClientIP = requestClientIP(r)
		session.StreamBitrateKbps = streamBitrateKbps
	}

	h.syncNativeSessionsNow(r.Context(), "abs_start")
	if announcer, ok := h.deps.NativeSessions.(interface {
		AnnounceStart(ctx context.Context, sessionID, frontend string)
	}); ok {
		announcer.AnnounceStart(r.Context(), session.ID, "abs")
	}
	return session, nil
}

func (h *Handler) updateNativePlaybackProgress(ctx context.Context, sessionID string, position float64) {
	if h == nil || h.deps.NativeSessions == nil || sessionID == "" {
		return
	}
	if err := h.deps.NativeSessions.UpdateProgress(sessionID, position, false); err != nil {
		if !errors.Is(err, playback.ErrSessionNotFound) {
			slog.WarnContext(ctx, "abs session sync: update native session progress failed", "component", "audiobooks",
				"session_id", sessionID, "position", position, "error", err)
		}
		return
	}
	h.syncNativeSessionsNow(ctx, "abs_progress")
}

func (h *Handler) stopNativePlaybackSession(ctx context.Context, sess *ABSPlaybackSession) {
	if h == nil || h.deps.NativeSessions == nil || sess == nil || sess.ID == "" {
		return
	}
	err := h.deps.NativeSessions.FinishSession(ctx, sess.ID)
	switch {
	case err == nil:
		h.syncNativeSessionsNow(ctx, "abs_close")
	case errors.Is(err, playback.ErrSessionNotFound):
		// The close reached a replica without the native copy: record the
		// play from the durable ABS row instead.
		finisher, ok := h.deps.NativeSessions.(interface {
			FinishDetached(context.Context, playback.Session)
		})
		if snapshot, known := absDetachedSession(sess); ok && known {
			finisher.FinishDetached(ctx, snapshot)
		}
	default:
		slog.WarnContext(ctx, "abs session close: finish native session failed", "component", "audiobooks",
			"session_id", sess.ID, "error", err)
	}
}

// absDetachedSession is the native session an ABS row describes, for a
// close that finds no native copy on this replica.
func absDetachedSession(sess *ABSPlaybackSession) (playback.Session, bool) {
	userID, err := strconv.Atoi(sess.UserID)
	if err != nil || sess.MediaFileID == nil {
		return playback.Session{}, false
	}
	return playback.Session{
		ID:                         sess.ID,
		UserID:                     userID,
		ProfileID:                  sess.ProfileID,
		MediaFileID:                *sess.MediaFileID,
		RequestedMediaFileID:       *sess.MediaFileID,
		PlayMethod:                 playback.PlayDirect,
		Position:                   sess.CurrentPositionSeconds,
		StartedAt:                  sess.StartedAt,
		DisableProgressPersistence: true,
	}, true
}

func (h *Handler) beginNativePlaybackTransport(sessionID string) bool {
	if h == nil || h.deps.NativeSessions == nil || sessionID == "" {
		return false
	}
	if err := h.deps.NativeSessions.BeginTransport(sessionID); err != nil {
		if !errors.Is(err, playback.ErrSessionNotFound) {
			slog.Warn("abs public track: begin native transport failed",
				"session_id", sessionID, "error", err)
		}
		return false
	}
	return true
}

func (h *Handler) endNativePlaybackTransport(sessionID string) {
	if h == nil || h.deps.NativeSessions == nil || sessionID == "" {
		return
	}
	if err := h.deps.NativeSessions.EndTransport(sessionID); err != nil && !errors.Is(err, playback.ErrSessionNotFound) {
		slog.Warn("abs public track: end native transport failed",
			"session_id", sessionID, "error", err)
	}
}

func (h *Handler) syncNativeSessionsNow(ctx context.Context, reason string) {
	if h == nil || h.deps.NativeSessionSyncer == nil {
		return
	}
	if err := h.deps.NativeSessionSyncer.SyncNow(ctx); err != nil {
		slog.ErrorContext(ctx, "abs: failed to sync native playback sessions", "component", "audiobooks", "reason", reason, "error", err)
	}
}

func writeNativePlaybackStartError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, playback.ErrTooManyStreams):
		http.Error(w, "too many concurrent streams", http.StatusTooManyRequests)
	case errors.Is(err, playback.ErrTooManyTranscodes):
		http.Error(w, "too many concurrent transcodes", http.StatusTooManyRequests)
	case errors.Is(err, playback.ErrTranscodingDisabled):
		http.Error(w, "transcoding is disabled for your user", http.StatusForbidden)
	case errors.Is(err, playback.ErrAudioTranscodingDisabled):
		http.Error(w, "audio transcoding is disabled for your user", http.StatusForbidden)
	default:
		slog.Error("abs play: start native playback session failed", "error", err)
		http.Error(w, "failed to start playback session", http.StatusInternalServerError)
	}
}

func absPlaybackClientInfoFromRequest(r *http.Request) playback.ClientInfo {
	if r == nil {
		return playback.ClientInfo{Name: "Audiobookshelf"}
	}
	name := firstHeaderValue(r,
		"X-Silo-Client",
		"X-Client-Name",
		"X-Device-Name",
		"X-Emby-Client",
	)
	if name == "" {
		name = "Audiobookshelf"
	}
	return playback.ClientInfo{
		Name: name,
		Version: firstHeaderValue(r,
			"X-Silo-Client-Version",
			"X-Client-Version",
			"X-Emby-Client-Version",
		),
		UserAgent: r.UserAgent(),
	}
}

func firstHeaderValue(r *http.Request, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(r.Header.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

func requestClientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	if ip := clientip.FromContext(r.Context()); ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}
