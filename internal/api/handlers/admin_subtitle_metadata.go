package handlers

import (
	"context"
	"errors"

	"github.com/Silo-Server/silo-server/internal/subtitles"
)

var ErrAdminSubtitleMetadataUnavailable = errors.New("subtitle metadata administration unavailable")

func (h *AdminSubtitleHandler) GetAdminSubtitleMetadata(ctx context.Context, id int) (*subtitles.DownloadedSubtitle, error) {
	if h == nil || h.repo == nil {
		return nil, ErrAdminSubtitleMetadataUnavailable
	}
	row, err := h.repo.GetDownloadedSubtitle(ctx, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, subtitles.ErrSubtitleNotFound
	}
	return row, nil
}

func (h *AdminSubtitleHandler) UpdateAdminSubtitleMetadata(ctx context.Context, id int, patch subtitles.SubtitleMetadataPatch, revision *int64) (*subtitles.DownloadedSubtitle, error) {
	if h == nil || h.manager == nil {
		return nil, ErrAdminSubtitleMetadataUnavailable
	}
	return h.manager.UpdateDownloadedSubtitleWithRevision(ctx, id, patch, revision)
}
