package abs

import (
	"context"
	"encoding/json"
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

// updateNativePlaybackProgress mirrors a session sync. total is the ABS
// session's running listening total after the sync: the time actually
// listened, which the native session credits instead of the position, since
// the position may have jumped by a seek.
func (h *Handler) updateNativePlaybackProgress(ctx context.Context, sessionID string, position, total float64) {
	if h == nil || h.deps.NativeSessions == nil || sessionID == "" {
		return
	}
	if err := h.deps.NativeSessions.UpdateProgressReported(sessionID, position, total); err != nil {
		if !errors.Is(err, playback.ErrSessionNotFound) {
			slog.WarnContext(ctx, "abs session sync: update native session progress failed", "component", "audiobooks",
				"session_id", sessionID, "position", position, "error", err)
		}
		return
	}
	h.syncNativeSessionsNow(ctx, "abs_progress")
}

const (
	// liveReportWindow separates a live play from catch-up: a local session
	// the client updated longer ago than this is offline listening synced
	// late, or a report sent again. With liveReportFutureSkew, the device
	// clock's allowance ahead of the server's, it stays within the lifecycle
	// sweep's 90-second idle window for reported plays, so a late copy of a
	// play's last report cannot start the play again after it ended.
	liveReportWindow     = time.Minute
	liveReportFutureSkew = 30 * time.Second
	// maxReportedPlaysPerAccount bounds the reported plays one account can
	// have open at once across the cluster. They are outside stream limits,
	// so without it an account could post any number of fresh session IDs.
	// It is well above a household's devices.
	maxReportedPlaysPerAccount = 10
)

// reportBudget bounds the live sessions one request tracks as reported
// plays. A client has one live session at a time, so a request carrying more
// gains nothing from the rest. A session the request already tracked is not
// tracked twice, and once the account's cap refuses a new play the request
// starts no other, while its plays already open still take their reports.
type reportBudget struct {
	left    int
	refused bool
	tracked map[string]bool
}

// take reports whether the request may track the client session, spending
// budget for it.
func (b *reportBudget) take(clientSessionID string) bool {
	if b.left <= 0 || b.tracked[clientSessionID] {
		return false
	}
	if b.tracked == nil {
		b.tracked = map[string]bool{}
	}
	b.tracked[clientSessionID] = true
	b.left--
	return true
}

// trackReportedPlay records a live play that a client reports only through
// /session/local or /session/local-all. The play lives in its
// abs_playback_sessions row and its lifecycle row; no server holds it in
// memory, so every server applies the client's reports to the same play, it
// never counts toward stream limits, and no media route can load it. The
// lifecycle sweep ends it once reports stop, and a report after that starts
// a new play with its own id and history row. A report never fails because
// of this: the client is playing whether or not Silo shows it.
func (h *Handler) trackReportedPlay(r *http.Request, a ctxAuth, access catalog.AccessFilter, sess localPlaybackSession, budget *reportBudget) {
	if h == nil || h.deps.PlaybackSessionStore == nil || sess.ID == "" || !sess.isLive(time.Now()) || !budget.take(sess.ID) {
		return
	}
	ctx := r.Context()
	report := ReportedPlay{
		UserID:           a.UserID,
		ProfileID:        a.ProfileID,
		ClientSessionID:  sess.ID,
		ContentID:        sess.LibraryItemID,
		PositionSeconds:  max(sess.CurrentTime, 0),
		ListeningSeconds: sess.TimeListening,
	}
	// Most reports continue a play whose start is recorded: one statement.
	state, found, err := h.deps.PlaybackSessionStore.UpdateReportedPlay(ctx, report)
	if err != nil || (found && state.Announced) {
		if err != nil {
			slog.WarnContext(ctx, "abs local session sync: record reported play failed", "component", "audiobooks",
				"library_item_id", sess.LibraryItemID, "error", err)
		}
		return
	}
	if !found && budget.refused {
		return
	}
	files, err := h.deps.MediaStore.GetMediaFiles(ctx, sess.LibraryItemID, access)
	if err != nil || len(files) == 0 {
		if err != nil {
			slog.WarnContext(ctx, "abs local session sync: load files for reported play failed", "component", "audiobooks",
				"library_item_id", sess.LibraryItemID, "error", err)
		}
		return
	}
	fileID := files[0].ID
	if !found {
		report.MediaFileID = &fileID
		if state, err = h.deps.PlaybackSessionStore.StartReportedPlay(ctx, report, uuid.NewString(), maxReportedPlaysPerAccount); err != nil {
			slog.WarnContext(ctx, "abs local session sync: start reported play failed", "component", "audiobooks",
				"library_item_id", sess.LibraryItemID, "error", err)
			return
		}
		if state.ID == "" {
			budget.refused = true
			// Every report from such a device lands here, every few seconds.
			slog.DebugContext(ctx, "abs local session sync: too many reported plays for the account; not tracking this one", "component", "audiobooks",
				"user_id", a.UserID, "limit", maxReportedPlaysPerAccount)
			return
		}
		if state.Announced {
			return
		}
	}
	userID, err := strconv.Atoi(a.UserID)
	if err != nil {
		return
	}
	// A new play, or one whose start was never recorded (a server that died
	// between the row and its lifecycle start): record the start now.
	announcer, ok := h.deps.NativeSessions.(interface {
		AnnounceDetachedStart(ctx context.Context, s playback.Session, frontend string)
	})
	if !ok {
		return
	}
	client := absPlaybackClientInfo(r, sess.DeviceInfo).Normalized()
	announcer.AnnounceDetachedStart(ctx, playback.Session{
		ID:                         state.ID,
		UserID:                     userID,
		ProfileID:                  a.ProfileID,
		MediaFileID:                fileID,
		RequestedMediaFileID:       fileID,
		PlayMethod:                 playback.PlayDirect,
		BasePlayMethod:             playback.PlayDirect,
		ClientIP:                   requestClientIP(r),
		ClientName:                 client.Name,
		ClientVersion:              client.Version,
		ClientUserAgent:            client.UserAgent,
		DisableProgressPersistence: true,
		TimelineDuration:           bookDurationSeconds(files),
		Position:                   report.PositionSeconds,
		StartedAt:                  state.StartedAt,
	}, "abs")
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

// UnmarshalJSON keeps the string fields and ignores anything else: the
// names are optional, and a malformed one must not reject the request that
// carries it.
func (d *absDeviceInfo) UnmarshalJSON(data []byte) error {
	var raw struct {
		ClientName    json.RawMessage `json:"clientName"`
		ClientVersion json.RawMessage `json:"clientVersion"`
	}
	// Field names match case-insensitively, as for any struct. Anything but
	// an object, or a name that isn't a string, leaves the field empty.
	_ = json.Unmarshal(data, &raw)
	_ = json.Unmarshal(raw.ClientName, &d.ClientName)
	_ = json.Unmarshal(raw.ClientVersion, &d.ClientVersion)
	return nil
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
