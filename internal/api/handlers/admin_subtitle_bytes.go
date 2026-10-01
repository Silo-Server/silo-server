package handlers

import (
	"context"
	"errors"

	"github.com/Silo-Server/silo-server/internal/subtitles"
)

var ErrAdminSubtitleBytesUnavailable = errors.New("subtitle byte administration unavailable")

// GetAdminSubtitleBytes returns the captured metadata and complete stored object.
// It does not create a transaction spanning the metadata and object stores.
func (h *AdminSubtitleHandler) GetAdminSubtitleBytes(ctx context.Context, id int) (*subtitles.DownloadedSubtitle, []byte, error) {
	if h == nil || h.manager == nil {
		return nil, nil, ErrAdminSubtitleBytesUnavailable
	}
	return h.manager.GetSubtitleContent(ctx, id)
}
