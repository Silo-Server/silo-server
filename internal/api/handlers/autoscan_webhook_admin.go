package handlers

import (
	"context"
	"errors"
	"strings"

	"github.com/Silo-Server/silo-server/internal/autoscan"
)

var ErrAdminSourceWebhookUnavailable = errors.New("source webhook storage unavailable")
var ErrAdminSourceWebhookMode = errors.New("source is not in webhook delivery mode")

func (h *AutoscanHandler) CreateAdminAutoscanSourceWebhook(ctx context.Context, id string) (AdminAutoscanSourceView, error) {
	if h == nil || h.repo == nil {
		return AdminAutoscanSourceView{}, ErrAdminSourceWebhookUnavailable
	}
	id = strings.TrimSpace(id)
	source, err := h.repo.GetSource(ctx, id)
	if err != nil {
		return AdminAutoscanSourceView{}, err
	}
	if source.DeliveryMode != autoscan.DeliveryModeWebhook {
		return AdminAutoscanSourceView{}, ErrAdminSourceWebhookMode
	}
	if _, _, err = h.repo.CreateWebhookEndpoint(ctx, id); err != nil {
		return AdminAutoscanSourceView{}, err
	}
	return h.sourceResponseWithWebhook(ctx, source), nil
}
func (h *AutoscanHandler) RotateAdminAutoscanSourceWebhook(ctx context.Context, id string) (AdminAutoscanSourceView, error) {
	if h == nil || h.repo == nil {
		return AdminAutoscanSourceView{}, ErrAdminSourceWebhookUnavailable
	}
	id = strings.TrimSpace(id)
	source, err := h.repo.GetSource(ctx, id)
	if err != nil {
		return AdminAutoscanSourceView{}, err
	}
	if _, _, err = h.repo.RotateWebhookEndpoint(ctx, id); err != nil {
		return AdminAutoscanSourceView{}, err
	}
	return h.sourceResponseWithWebhook(ctx, source), nil
}
func (h *AutoscanHandler) DeleteAdminAutoscanSourceWebhook(ctx context.Context, id string) error {
	if h == nil || h.repo == nil {
		return ErrAdminSourceWebhookUnavailable
	}
	return h.repo.DeleteWebhookEndpoint(ctx, strings.TrimSpace(id))
}
