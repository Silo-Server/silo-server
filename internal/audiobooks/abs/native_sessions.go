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
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// PlaybackSessionManager is the native Silo playback-session surface ABS needs
// to make compatibility playback visible to live admin/session monitoring.
type PlaybackSessionManager interface {
	StartSessionWithFilesContext(ctx context.Context, userID int, profileID string, effectiveFileID int, requestedFileID int, method playback.PlayMethod, transcodeAudio bool) (*playback.Session, error)
	UpdateProgress(sessionID string, position float64, isPaused bool) error
	UpdateProgressListened(sessionID string, position, listened float64) error
	UpdateProgressReported(sessionID string, position, total float64) error
	UpdateStreamState(sessionID string, state playback.SessionStreamState) error
	BeginTransport(sessionID string) error
	EndTransport(sessionID string) error
	SetProgressPersistenceDisabled(sessionID string, disabled bool) error
	// SetTimelineDuration records the whole book's length, since ABS reports
	// positions in the book rather than in the session's first file.
	SetTimelineDuration(sessionID string, seconds float64) error
	// FinishSession ends the play and runs the manager's finish hooks, which
	// write the admin playback history row.
	FinishSession(ctx context.Context, sessionID string) error
	// RegisterReportOnly adds a report-only session under a caller-chosen ID
	// unless the account already holds maxPerUser of them.
	RegisterReportOnly(s *playback.Session, maxPerUser int) bool
}

const (
	// reportedPlaybackGrace is how long after its last report a play that a
	// client only reports through /session/local is over. Such clients send
	// no stop, so silence is the only end there is.
	reportedPlaybackGrace = 90 * time.Second
	// maxReportedPlaysPerAccount bounds the reported plays one account can
	// have live at once. They are outside stream limits, so without it an
	// account could post any number of fresh session IDs. It is well above a
	// household's devices.
	maxReportedPlaysPerAccount = 10
	// liveReportWindow separates live playback from catch-up: a session last
	// updated longer ago than this is offline listening synced late, or a
	// client re-sending an old session, not a play happening now.
	liveReportWindow = 2 * time.Minute
)

// reportedPlaybackNamespace derives the native session ID of a play a client
// reports through /session/local from the client's own session ID.
var reportedPlaybackNamespace = uuid.MustParse("3f6c1a2e-8b4d-4e7a-9c15-6d2f0b8e7a41")

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
	device absDeviceInfo,
) (*playback.Session, error) {
	if h == nil || h.deps.NativeSessions == nil || len(files) == 0 {
		return nil, nil
	}
	userID, err := strconv.Atoi(a.UserID)
	if err != nil {
		return nil, fmt.Errorf("invalid ABS user id %q: %w", a.UserID, err)
	}

	file := files[0]
	sessionCtx := playback.WithClientInfo(r.Context(), absPlaybackClientInfo(r, device))
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

	bookDuration := bookDurationSeconds(files)
	if err := h.deps.NativeSessions.SetTimelineDuration(session.ID, bookDuration); err != nil {
		slog.WarnContext(r.Context(), "abs play: set native session book duration failed", "component", "audiobooks",
			"session_id", session.ID, "error", err)
	} else {
		session.TimelineDuration = bookDuration
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

// updateNativePlaybackProgress mirrors a session sync. Its timeListened is the
// time actually listened since the previous sync, so it is what the native
// session credits as listening time; the position may have jumped by a seek.
func (h *Handler) updateNativePlaybackProgress(ctx context.Context, sessionID string, position, listened float64) {
	if h == nil || h.deps.NativeSessions == nil || sessionID == "" {
		return
	}
	if err := h.deps.NativeSessions.UpdateProgressListened(sessionID, position, listened); err != nil {
		if !errors.Is(err, playback.ErrSessionNotFound) {
			slog.WarnContext(ctx, "abs session sync: update native session progress failed", "component", "audiobooks",
				"session_id", sessionID, "position", position, "error", err)
		}
		return
	}
	h.syncNativeSessionsNow(ctx, "abs_progress")
}

// trackReportedPlayback mirrors live playback that a client reports only
// through /session/local or /session/local-all into a native playback
// session, as handlePlayStart does for a client that opens a session. Some
// clients (Plappa and Prologue on iOS) never call /play: they stream the files
// directly and post their local session every few seconds while playing.
//
// The native session ID is derived from the account, profile and client
// session ID, so every report of one play, on any replica and after an idle
// expiry, lands on the same session and the same admin history row. The
// session is report-only: it may be playing a downloaded copy, so it never
// counts toward stream limits. A report never fails because of this: the
// client is playing whether or not Silo shows it. It reports whether it
// started a session, which the caller syncs to the admin views.
func (h *Handler) trackReportedPlayback(r *http.Request, a ctxAuth, access catalog.AccessFilter, sess localPlaybackSession) bool {
	now := time.Now()
	if h == nil || h.deps.NativeSessions == nil || sess.ID == "" || !sess.isLive(now) {
		return false
	}
	userID, err := strconv.Atoi(a.UserID)
	if err != nil {
		return false
	}
	id := uuid.NewSHA1(reportedPlaybackNamespace, []byte(a.UserID+"\x00"+a.ProfileID+"\x00"+sess.ID)).String()
	err = h.deps.NativeSessions.UpdateProgressReported(id, sess.CurrentTime, sess.TimeListening)
	if !errors.Is(err, playback.ErrSessionNotFound) {
		return false
	}
	if !h.registerReportedPlayback(r, a, access, userID, id, sess, now) {
		return false
	}
	if err := h.deps.NativeSessions.UpdateProgressReported(id, sess.CurrentTime, sess.TimeListening); err != nil {
		slog.WarnContext(r.Context(), "abs local session sync: update native session progress failed", "component", "audiobooks",
			"session_id", id, "error", err)
	}
	return true
}

// registerReportedPlayback starts the report-only native session for a
// reported play (see trackReportedPlayback). It reports whether it did.
func (h *Handler) registerReportedPlayback(r *http.Request, a ctxAuth, access catalog.AccessFilter, userID int, id string, sess localPlaybackSession, now time.Time) bool {
	ctx := r.Context()
	files, err := h.deps.MediaStore.GetMediaFiles(ctx, sess.LibraryItemID, access)
	if err != nil || len(files) == 0 {
		if err != nil {
			slog.WarnContext(ctx, "abs local session sync: load files for native session failed", "component", "audiobooks",
				"library_item_id", sess.LibraryItemID, "error", err)
		}
		return false
	}
	client := absPlaybackClientInfo(r, sess.DeviceInfo).Normalized()
	file := files[0]
	native := &playback.Session{
		ID:                         id,
		UserID:                     userID,
		ProfileID:                  a.ProfileID,
		MediaFileID:                file.ID,
		RequestedMediaFileID:       file.ID,
		PlayMethod:                 playback.PlayDirect,
		BasePlayMethod:             playback.PlayDirect,
		ClientIP:                   requestClientIP(r),
		ClientName:                 client.Name,
		ClientVersion:              client.Version,
		ClientUserAgent:            client.UserAgent,
		StreamBitrateKbps:          max(file.Bitrate, 0),
		DisableProgressPersistence: true,
		TimelineDuration:           bookDurationSeconds(files),
		IdleGrace:                  reportedPlaybackGrace,
		ReportOnly:                 true,
	}
	// A play that began moments ago is measured from its start, so the
	// listening before its first report counts. A session rebuilt for an
	// older play (after an idle expiry, or on another replica) only takes
	// the client's running total as the count to measure from: what it
	// covers was credited before, or was not seen live.
	if began, ok := sess.beganRecently(now); ok {
		native.StartedAt = began
		native.Watch.Observe(sess.StartTime, false, began)
	} else {
		native.ReportedListening = sess.TimeListening
	}
	if !h.deps.NativeSessions.RegisterReportOnly(native, maxReportedPlaysPerAccount) {
		slog.WarnContext(ctx, "abs local session sync: too many reported plays for the account; not tracking this one", "component", "audiobooks",
			"user_id", userID, "limit", maxReportedPlaysPerAccount)
		return false
	}
	return true
}

// bookDurationSeconds is the length of a book made of files.
func bookDurationSeconds(files []*models.MediaFile) float64 {
	var total float64
	for _, f := range files {
		total += float64(f.Duration)
	}
	return total
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

// absDeviceInfo is the part of the deviceInfo an Audiobookshelf client sends
// with /play and with its local sessions that names the client.
type absDeviceInfo struct {
	ClientName    string `json:"clientName"`
	ClientVersion string `json:"clientVersion"`
}

// absPlaybackClientInfo names the client of a play: the deviceInfo the client
// sent, else the request headers.
func absPlaybackClientInfo(r *http.Request, device absDeviceInfo) playback.ClientInfo {
	client := absPlaybackClientInfoFromRequest(r)
	if name := strings.TrimSpace(device.ClientName); name != "" {
		// The headers may describe another client, so their version is not
		// carried over to the one deviceInfo names.
		client.Name, client.Version = name, strings.TrimSpace(device.ClientVersion)
	}
	return client
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
