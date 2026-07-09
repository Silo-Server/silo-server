package playback

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"
)

const defaultChapterThumbnailEventTTL = 15 * time.Minute

type chapterThumbnailSessionLookup interface {
	GetSessionsByMediaFileID(fileID int) []*Session
}

type chapterThumbnailURLPresigner interface {
	PresignGetURL(ctx context.Context, bucket, key string, expiry time.Duration) (string, error)
	Bucket() string
}

// ChapterThumbnailNotifier publishes live chapter thumbnail updates to active playback sessions.
type ChapterThumbnailNotifier struct {
	sessions chapterThumbnailSessionLookup
	hub      *RealtimeHub
	presign  chapterThumbnailURLPresigner
	ttl      time.Duration
}

// NewChapterThumbnailNotifier creates a notifier that targets active sessions for a file.
func NewChapterThumbnailNotifier(
	sessions chapterThumbnailSessionLookup,
	hub *RealtimeHub,
	presign chapterThumbnailURLPresigner,
	ttl time.Duration,
) *ChapterThumbnailNotifier {
	if sessions == nil || hub == nil || presign == nil {
		return nil
	}
	if ttl <= 0 {
		ttl = defaultChapterThumbnailEventTTL
	}
	return &ChapterThumbnailNotifier{
		sessions: sessions,
		hub:      hub,
		presign:  presign,
		ttl:      ttl,
	}
}

// ChapterThumbnailReady notifies active sessions that a chapter thumbnail is now available.
func (n *ChapterThumbnailNotifier) ChapterThumbnailReady(
	ctx context.Context,
	fileID int,
	chapterIndex int,
	thumbnailPath string,
	thumbnailThumbhash string,
) {
	if n == nil || fileID <= 0 || chapterIndex < 0 || thumbnailPath == "" {
		return
	}

	thumbnailURL, err := n.presign.PresignGetURL(
		ctx,
		n.presign.Bucket(),
		strings.Replace(thumbnailPath, "/original.", "/w300.", 1),
		n.ttl,
	)
	if err != nil {
		slog.WarnContext(ctx,
			"failed to presign chapter thumbnail for realtime event", "component", "playback",
			"file_id",
			fileID,
			"chapter_index",
			chapterIndex,
			"error",
			err,
		)
		return
	}

	for _, session := range n.sessions.GetSessionsByMediaFileID(fileID) {
		if session == nil || session.ID == "" || !session.HasRealtimeConnection {
			continue
		}
		event, err := NewChapterThumbnailReadyEvent(
			session.ID,
			fileID,
			chapterIndex,
			thumbnailURL,
			thumbnailThumbhash,
		)
		if err != nil {
			slog.WarnContext(ctx,
				"failed to encode chapter thumbnail realtime event", "component", "playback",
				"session_id",
				session.ID,
				"file_id",
				fileID,
				"chapter_index",
				chapterIndex,
				"error",
				err,
			)
			continue
		}
		if err := n.hub.Send(session.ID, event); err != nil && !errors.Is(err, ErrRealtimeConnectionNotFound) {
			slog.WarnContext(ctx,
				"failed to deliver chapter thumbnail realtime event", "component", "playback",
				"session_id",
				session.ID,
				"file_id",
				fileID,
				"chapter_index",
				chapterIndex,
				"error",
				err,
			)
		}
	}
}
