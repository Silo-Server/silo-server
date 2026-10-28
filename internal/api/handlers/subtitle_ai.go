package handlers

import (
	"context"
	"errors"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/subtitles/ai"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// SubtitleAIHandler exposes on-demand AI subtitle translation backed by the
// configured OpenAI-compatible engine. Generated tracks are stored as ordinary
// downloaded subtitles, so they reach every client through the existing
// subtitle pipeline.
type SubtitleAIHandler struct {
	service        *ai.Service
	LiveNotifier   *playback.SubtitleReadyNotifier
	FileAuthorizer *MediaFileAuthorizer
	// StoreProvider resolves household profiles for the transcription quota
	// exemption check; when nil the whole admin account is exempt.
	StoreProvider userstore.UserStoreProvider
}

// NewSubtitleAIHandler creates a handler backed by the given service.
func NewSubtitleAIHandler(service *ai.Service) *SubtitleAIHandler {
	return &SubtitleAIHandler{service: service}
}

type translateSubtitleRequest struct {
	MediaFileID int `json:"media_file_id"`
	// Kind selects the job: "translate" (default), "transcribe", or
	// "transcribe_translate". For ASR kinds, source_index is the audio track
	// index (-1 = default track) and target_language is optional for plain
	// transcribe (acts as a language hint).
	Kind           string  `json:"kind"`
	SourceIndex    int     `json:"source_index"`
	SourceLanguage string  `json:"source_language"`
	TargetLanguage string  `json:"target_language"`
	SessionID      string  `json:"session_id"`
	StartPosition  float64 `json:"start_position"`
}

type SubtitleAIStatusView struct {
	Enabled           bool `json:"enabled"`
	TranscribeEnabled bool `json:"transcribe_enabled"`
}

// SubtitleAIStatus reports configured engine capabilities without starting work.
func (h *SubtitleAIHandler) SubtitleAIStatus(ctx context.Context) SubtitleAIStatusView {
	if h == nil || h.service == nil {
		return SubtitleAIStatusView{}
	}
	return SubtitleAIStatusView{Enabled: h.service.Enabled(ctx), TranscribeEnabled: h.service.TranscribeEnabled()}
}

// quotaExceededMessage turns a quota error into a user-facing message with the
// limit and window when available.
func quotaExceededMessage(err error) string {
	var qe *ai.QuotaExceededError
	if errors.As(err, &qe) {
		return fmt.Sprintf(
			"You've reached your transcription limit (%d per %s). Try again later or ask an admin to raise it.",
			qe.Limit, qe.Period)
	}
	return "You've reached your transcription limit. Try again later or ask an admin to raise it."
}
