package ai

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// Notifier surfaces translation progress to clients. It is optional: without
// one, the pipeline still stores the finished track and clients pick it up on
// their next subtitle-list refresh. The playback layer implements it over the
// per-session realtime hub.
//
// SubtitleReady broadcasts a finished track to every session watching the file.
// The Translation* methods stream a single requesting session's live job so the
// player can pause, fill in cues as they arrive, and resume.
type Notifier interface {
	SubtitleReady(ctx context.Context, mediaFileID, subtitleID int, language, label string)

	TranslationStarted(ctx context.Context, sessionID string, fileID int, jobID int64, trackKey, language, label string, totalCues int)
	TranslationCues(ctx context.Context, sessionID string, fileID int, jobID int64, trackKey string, cues []playback.StreamCue, done, total int)
	TranslationCompleted(ctx context.Context, sessionID string, fileID int, jobID int64, trackKey string, subtitleID int, language, label string)
	TranslationFailed(ctx context.Context, sessionID string, fileID int, jobID int64, trackKey, message string)
}
