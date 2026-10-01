package handlers

import (
	"context"
	"errors"
	"math"
	"net/http"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/subtitles/ai"
)

type SubtitleAICreateCommand struct {
	MediaFileID                               int
	Kind                                      ai.JobKind
	SourceIndex                               int
	SourceLanguage, TargetLanguage, SessionID string
	StartPosition                             float64
}
type SubtitleAICreateResult struct {
	Job                  *ai.Job
	LiveDeliveryAttached bool
}

// CreateSubtitleAIJob derives requester/quota authority from the authenticated
// caller. Active-job deduplication does not replay a durable creation receipt.
func (h *SubtitleAIHandler) CreateSubtitleAIJob(ctx context.Context, filter catalog.AccessFilter, command SubtitleAICreateCommand) (SubtitleAICreateResult, error) {
	var zero SubtitleAICreateResult
	account, profile := apimw.GetUserID(ctx), apimw.GetProfileID(ctx)
	if account <= 0 || filter.UserID != account || filter.ProfileID != profile {
		return zero, apiError(http.StatusForbidden, "forbidden", "Subtitle request identity does not match the authenticated viewer")
	}
	if command.MediaFileID <= 0 || command.SourceIndex < -1 || math.IsNaN(command.StartPosition) || math.IsInf(command.StartPosition, 0) || command.StartPosition < 0 {
		return zero, apiError(http.StatusBadRequest, "bad_request", "Invalid subtitle source or start position")
	}
	if err := h.authorizeSubtitleAIFile(ctx, filter, command.MediaFileID); err != nil {
		return zero, err
	}
	var bound *playback.SubtitleReadyNotifier
	if command.SessionID != "" {
		if h.LiveNotifier == nil {
			return zero, apiError(http.StatusServiceUnavailable, "dependency_unavailable", "Live subtitle delivery is unavailable")
		}
		var err error
		bound, err = h.LiveNotifier.BindTranslation(account, profile, command.SessionID, command.MediaFileID)
		if err != nil {
			return zero, apiError(http.StatusNotFound, "not_found", "Playback session not found")
		}
	}
	request := ai.JobRequest{MediaFileID: command.MediaFileID, Kind: command.Kind, SourceIndex: command.SourceIndex, SourceLanguage: command.SourceLanguage, TargetLanguage: command.TargetLanguage, RequestedBy: new(account), QuotaExempt: h.subtitleQuotaExempt(ctx, account, profile, apimw.IsAdmin(ctx)), SessionID: command.SessionID, StartPosition: command.StartPosition}
	if bound != nil {
		request.LiveNotifier = bound
	}
	job, err := h.service.Enqueue(ctx, request)
	switch {
	case errors.Is(err, ai.ErrEngineNotConfigured):
		return zero, apiError(http.StatusServiceUnavailable, "dependency_unavailable", "AI subtitle processing is not configured")
	case errors.Is(err, ai.ErrQuotaExceeded):
		return zero, apiError(http.StatusTooManyRequests, "rate_limited", quotaExceededMessage(err))
	case errors.Is(err, ai.ErrInvalidRequest):
		return zero, apiError(http.StatusBadRequest, "bad_request", "Invalid subtitle processing request")
	case err != nil:
		return zero, apiError(http.StatusInternalServerError, "internal_error", "Failed to start subtitle processing")
	}
	if job == nil {
		return zero, apiError(http.StatusInternalServerError, "internal_error", "Subtitle processing returned no job")
	}
	return SubtitleAICreateResult{Job: job, LiveDeliveryAttached: bound != nil && job.LiveNotifier == bound}, nil
}
