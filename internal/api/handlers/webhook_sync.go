package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/webhooksync"
)

type WebhookSyncHandler struct {
	service *webhooksync.Service
	stores  userstore.UserStoreProvider
}

// NewWebhookSyncHandler takes the household stores that decide who may manage
// connections. Without stores every management request is refused; the public
// receiver does not need them.
func NewWebhookSyncHandler(service *webhooksync.Service, stores userstore.UserStoreProvider) *WebhookSyncHandler {
	return &WebhookSyncHandler{service: service, stores: stores}
}

// MayManageWebhookSync applies the household-manager rule to connection
// management, reads included. A connection belongs to the account, its
// receiver URL carries the secret that authenticates deliveries, and its
// mappings choose which profile an external user's watch state is written to,
// so a household member who is not the manager may neither see nor change it.
func (h *WebhookSyncHandler) MayManageWebhookSync(ctx context.Context, userID int, profileID string, verify func(*userstore.Profile) error) (bool, error) {
	return mayManageHouseholdOf(ctx, h.stores, userID, profileID, verify)
}

// ReceiveWebhook applies a v2 delivery through the shared processor and delivery log.
func (h *WebhookSyncHandler) ReceiveWebhook(r *http.Request, secret string, limit int64) error {
	return webhookManagementError(h.receiveWebhook(r, secret, limit))
}

func (h *WebhookSyncHandler) receiveWebhook(r *http.Request, secret string, limit int64) error {
	capture := webhooksync.NewBodyCaptureReadCloser(r.Body, webhooksync.WebhookBodyCaptureLimit)
	r.Body = capture

	result, err := h.service.ProcessWebhookBounded(r.Context(), secret, r, limit)
	logContext := webhooksync.BuildWebhookRequestLogContext(
		r,
		webhooksync.SanitizeWebhookBodyExcerpt(r.Header.Get("Content-Type"), capture.Captured()),
	)
	statusCode := http.StatusNoContent
	if err != nil {
		statusCode = webhookErrorStatus(err)
	}

	if result != nil {
		if _, logErr := h.service.CreateEventLog(r.Context(), webhooksync.WebhookEventLog{
			ConnectionID: result.ConnectionID,
			RequestID:    logContext.RequestID,
			HTTPStatus:   statusCode,
			Outcome:      result.Outcome,
			Summary:      result.Summary,
			ErrorMessage: result.ErrorMessage,
			BodyExcerpt:  logContext.BodyExcerpt,
			Attrs:        webhookEventAttrs(logContext, result),
		}); logErr != nil {
			slog.WarnContext(r.Context(), "webhook sync: failed to persist webhook event log", "component", "api", "connection_id", result.ConnectionID, "error", logErr)
		}
	}
	logWebhookDelivery(result, logContext, statusCode, err)

	return err
}

func webhookErrorStatus(err error) int {
	if _, refused := historyimport.ServerAddressMessage(err); refused {
		return http.StatusBadRequest
	}
	switch {
	case errors.Is(err, webhooksync.ErrWebhookTooLarge):
		return http.StatusRequestEntityTooLarge
	case errors.Is(err, webhooksync.ErrWebhookBody):
		return http.StatusBadRequest
	case errors.Is(err, webhooksync.ErrConnectionNotFound), errors.Is(err, historyimport.ErrProfileNotFound):
		return http.StatusNotFound
	default:
		message := err.Error()
		switch {
		case message == "invalid multipart request",
			message == "missing payload",
			message == "invalid Plex payload",
			message == "invalid Plex webhook payload",
			message == "invalid Emby payload",
			message == "invalid Emby webhook payload",
			message == "invalid Jellyfin payload",
			message == "invalid Jellyfin webhook payload",
			strings.Contains(message, "are required"),
			strings.Contains(message, "cannot be empty"),
			strings.Contains(message, "unsupported provider"):
			return http.StatusBadRequest
		default:
			return http.StatusInternalServerError
		}
	}
}

func webhookEventAttrs(meta webhooksync.WebhookRequestLogContext, result *webhooksync.ProcessWebhookResult) map[string]any {
	attrs := map[string]any{
		"client_ip":    meta.ClientIP,
		"content_type": meta.ContentType,
		"user_agent":   meta.UserAgent,
		"path_pattern": meta.PathPattern,
	}
	if result == nil {
		return attrs
	}
	if result.EventKind != "" {
		attrs["event_kind"] = result.EventKind
	}
	if result.Action != "" {
		attrs["action"] = result.Action
	}
	if result.UserID != "" {
		attrs["external_user_id"] = result.UserID
	}
	if result.UserName != "" {
		attrs["external_user_name"] = result.UserName
	}
	if result.ExternalItemID != "" {
		attrs["external_item_id"] = result.ExternalItemID
	}
	if result.MediaKind != "" {
		attrs["media_kind"] = result.MediaKind
	}
	if result.MatchedMediaItemID != "" {
		attrs["matched_media_item_id"] = result.MatchedMediaItemID
	}
	if result.MatchedMediaItemTitle != "" {
		attrs["matched_media_item_title"] = result.MatchedMediaItemTitle
	}
	if result.ProfileID != "" {
		attrs["profile_id"] = result.ProfileID
	}
	return attrs
}

func logWebhookDelivery(result *webhooksync.ProcessWebhookResult, meta webhooksync.WebhookRequestLogContext, statusCode int, err error) {
	args := []any{
		"component", "webhook_sync",
		"request_id", meta.RequestID,
		"http_status", statusCode,
		"path_pattern", meta.PathPattern,
		"client_ip", meta.ClientIP,
		"content_type", meta.ContentType,
	}
	if result != nil {
		args = append(args,
			"connection_id", result.ConnectionID,
			"provider", result.Provider,
			"outcome", result.Outcome,
			"summary", result.Summary,
		)
		if result.EventKind != "" {
			args = append(args, "event_kind", result.EventKind)
		}
		if result.Action != "" {
			args = append(args, "action", result.Action)
		}
		if result.UserID != "" {
			args = append(args, "external_user_id", result.UserID)
		}
		if result.ExternalItemID != "" {
			args = append(args, "external_item_id", result.ExternalItemID)
		}
		if result.MatchedMediaItemID != "" {
			args = append(args, "matched_media_item_id", result.MatchedMediaItemID)
		}
		if result.ErrorMessage != "" {
			args = append(args, "error", result.ErrorMessage)
		}
	} else {
		args = append(args,
			"outcome", webhooksync.OutcomeRejected,
			"summary", "Rejected webhook request because the secret was not found",
		)
		if err != nil {
			args = append(args, "error", err.Error())
		}
	}

	switch {
	case statusCode >= 500:
		slog.Error("webhook delivery", args...)
	case statusCode >= 400:
		slog.Warn("webhook delivery", args...)
	default:
		slog.Info("webhook delivery", args...)
	}
}

// requestBaseURL is the origin r arrived on. X-Forwarded-Proto and
// X-Forwarded-Host count only from a trusted proxy (see clientip.RequestOrigin),
// so a client cannot use them to change the scheme or host of the links built
// from it.
func requestBaseURL(r *http.Request) string {
	return clientip.RequestOrigin(r)
}
