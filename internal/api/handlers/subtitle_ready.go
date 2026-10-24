package handlers

import (
	"context"
	"time"

	"github.com/Silo-Server/silo-server/internal/subtitles"
)

// SubtitleReadyAnnouncer tells the active playback sessions of a file that a
// stored subtitle was added. Players add the track to their list without
// selecting it, so another viewer's current subtitle never changes.
type SubtitleReadyAnnouncer interface {
	SubtitleReady(ctx context.Context, mediaFileID, subtitleID int, language, label string)
}

// subtitleReadyTimeout bounds the announcement, which resolves each watching
// session's track and runs after the request has answered.
const subtitleReadyTimeout = 10 * time.Second

// SetSubtitleReadyAnnouncer wires the realtime announcement of downloaded and
// uploaded subtitles.
func (h *SubtitleSearchHandler) SetSubtitleReadyAnnouncer(announcer SubtitleReadyAnnouncer) {
	h.ready = announcer
}

// announceAddedSubtitle lets every session already playing the file offer the
// new track at once, on every API server, instead of on its next start.
func (h *SubtitleSearchHandler) announceAddedSubtitle(ctx context.Context, sub *subtitles.DownloadedSubtitle) {
	if h.ready == nil || sub == nil {
		return
	}
	announcer := h.ready
	ctx = context.WithoutCancel(ctx)
	go func() {
		ctx, cancel := context.WithTimeout(ctx, subtitleReadyTimeout)
		defer cancel()
		announcer.SubtitleReady(ctx, sub.MediaFileID, sub.ID, sub.Language, sub.ReleaseName)
	}()
}
