package handlers

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/webhooksync"
)

func (h *WebhookSyncHandler) ListWebhookConnections(ctx context.Context, userID int, after *webhooksync.PageKey, limit int) ([]webhooksync.Connection, bool, error) {
	return h.service.ListConnectionsPage(ctx, userID, after, limit)
}
func (h *WebhookSyncHandler) CreateWebhookConnection(ctx context.Context, userID int, input webhooksync.CreateConnectionInput) (*webhooksync.CreateConnectionResult, error) {
	result, err := h.service.CreateConnection(ctx, userID, input, "")
	return result, webhookManagementError(err)
}
func (h *WebhookSyncHandler) UpdateWebhookConnection(ctx context.Context, userID int, id string, input webhooksync.UpdateConnectionInput) (*webhooksync.Connection, error) {
	result, err := h.service.UpdateConnection(ctx, userID, id, input)
	return result, webhookManagementError(err)
}
func (h *WebhookSyncHandler) DeleteWebhookConnection(ctx context.Context, userID int, id string) error {
	return webhookManagementError(h.service.DeleteConnection(ctx, userID, id))
}
func (h *WebhookSyncHandler) RotateWebhookConnection(ctx context.Context, userID int, id string) (*webhooksync.RotateWebhookResult, error) {
	result, err := h.service.RotateWebhook(ctx, userID, id, "")
	return result, webhookManagementError(err)
}
func (h *WebhookSyncHandler) GetWebhookMappings(ctx context.Context, userID int, id string) (*webhooksync.ProfileMappingsResponse, error) {
	result, err := h.service.GetProfileMappings(ctx, userID, id)
	return result, webhookManagementError(err)
}
func (h *WebhookSyncHandler) UpdateWebhookMappings(ctx context.Context, userID int, id string, input webhooksync.UpdateProfileMappingsInput) ([]webhooksync.ProfileMapping, error) {
	result, err := h.service.UpdateProfileMappings(ctx, userID, id, input)
	return result, webhookManagementError(err)
}
func (h *WebhookSyncHandler) ListWebhookEvents(ctx context.Context, userID int, id string, after *webhooksync.PageKey, limit int) ([]webhooksync.WebhookEventLog, bool, error) {
	result, more, err := h.service.ListEventLogsPage(ctx, userID, id, after, limit)
	return result, more, webhookManagementError(err)
}
func webhookManagementError(err error) error {
	if err == nil {
		return nil
	}
	status := webhookErrorStatus(err)
	message := err.Error()
	if refused, ok := historyimport.ServerAddressMessage(err); ok {
		message = refused
	}
	code := policyErrorBadRequest
	if status >= http.StatusInternalServerError {
		message = "Webhook sync request failed"
		code = policyErrorInternal
	}
	if status == http.StatusNotFound {
		code = policyErrorNotFound
	}
	return &APIError{Status: status, Code: code, Message: message, cause: err}
}
