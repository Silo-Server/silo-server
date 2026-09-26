package subtitles

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
)

// AIJobPublication binds generated output to one active job. Complete marks the
// final output; transcription preceding translation remains an intermediate output.
type AIJobPublication struct {
	JobID    int64
	Complete bool
}

// ErrAIJobInactive means publication was refused before any metadata commit.
var ErrAIJobInactive = errors.New("subtitle AI job is no longer active")

// ErrAIPublicationUncertain means the transaction may have committed. Callers
// must not report a terminal outcome or attempt another terminal transition
// without reconciling persisted state.
var ErrAIPublicationUncertain = errors.New("subtitle AI publication outcome is uncertain")

type aiPublicationRepository interface {
	PublishAISubtitle(context.Context, *DownloadedSubtitle, AIJobPublication, *DownloadedSubtitle) (*DownloadedSubtitle, error)
}

func (m *Manager) storeAISubtitle(ctx context.Context, req StoreSubtitleRequest) (*DownloadedSubtitle, error) {
	repo, ok := m.repo.(aiPublicationRepository)
	if !ok {
		return nil, errors.New("atomic subtitle AI publication is unavailable")
	}
	sub := &DownloadedSubtitle{MediaFileID: req.MediaFileID, Provider: req.Provider, Language: req.Language, Format: req.Format,
		ReleaseName: req.ReleaseName, Score: req.Score, HearingImpaired: req.HearingImpaired, DownloadedBy: req.UserID, ContentSHA256: subtitleContentHash(req.Data)}
	// Verify legacy bytes before offering that row for reuse. The transaction
	// rechecks its immutable key and logical identity while locking the row.
	legacy, err := m.repo.GetDownloadedSubtitleByS3Key(ctx, buildSubtitleS3Key(req.MediaFileID, req.Language, req.Provider, req.Format, req.Data))
	if err != nil {
		return nil, fmt.Errorf("check legacy AI subtitle: %w", err)
	}
	if legacy != nil {
		data, readErr := m.blobs.Get(ctx, legacy.S3Key)
		if readErr != nil {
			return nil, fmt.Errorf("verify legacy AI subtitle: %w", readErr)
		}
		if subtitleContentHash(data) != sub.ContentSHA256 {
			legacy = nil
		}
	}
	// Upload outside the transaction. Cancellation is allowed to win while storage
	// is slow; the candidate is private until the guarded metadata commit.
	sub.S3Key = fmt.Sprintf("subtitles/%d/%s.%s", req.MediaFileID, uuid.NewString(), req.Format)
	if err := m.blobs.Put(ctx, sub.S3Key, req.Data); err != nil {
		return nil, fmt.Errorf("upload AI subtitle: %w", err)
	}
	published, err := repo.PublishAISubtitle(ctx, sub, *req.Publication, legacy)
	if err != nil {
		if errors.Is(err, ErrAIJobInactive) {
			m.cleanupSubtitleObject(ctx, sub.S3Key)
		} else {
			// A lost COMMIT reply may conceal successful publication. Never delete
			// that candidate; durable reconciliation remains separate work.
			slog.ErrorContext(ctx, "AI subtitle publication uncertain; retain candidate", "component", "subtitles", "object_key", sub.S3Key, "error", err)
		}
		return nil, err
	}
	if published.S3Key != sub.S3Key {
		m.cleanupSubtitleObject(ctx, sub.S3Key)
	}
	return published, nil
}
