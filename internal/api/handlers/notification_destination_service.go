package handlers

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/notifications"
)

func (h *NotificationsHandler) ListNotificationWebPushPage(ctx context.Context, profile string, limit int, after *notifications.Cursor) ([]notifications.WebPushSubscription, error) {
	svc := h.webPush()
	if svc == nil {
		return nil, apiError(503, "unavailable", "Web push is not available")
	}
	return svc.ListPage(ctx, profile, limit, after)
}
func (h *NotificationsHandler) ListNotificationWebhookPage(ctx context.Context, profile string, limit int, after *notifications.Cursor) ([]notifications.Webhook, error) {
	svc := h.webhooks()
	if svc == nil {
		return nil, apiError(503, "unavailable", "Webhooks are not available")
	}
	return svc.ListPage(ctx, profile, limit, after)
}
func (h *NotificationsHandler) ListNotificationServerChannelPage(ctx context.Context, limit int, after *notifications.Cursor) ([]notifications.ServerChannel, error) {
	if h == nil || h.system == nil || h.system.ServerChannels == nil {
		return nil, apiError(503, "unavailable", "Server channels are not available")
	}
	return h.system.ServerChannels.ListPage(ctx, limit, after)
}

func (h *NotificationsHandler) TestNotificationWebhook(ctx context.Context, profile, id string) (*notifications.WebhookTestResult, error) {
	svc := h.webhooks()
	if svc == nil {
		return nil, apiError(503, "unavailable", "Webhooks are not available")
	}
	return svc.Test(ctx, profile, id)
}
func (h *NotificationsHandler) TestNotificationServerChannel(ctx context.Context, id string) (*notifications.WebhookTestResult, error) {
	if h == nil || h.system == nil || h.system.ServerChannels == nil {
		return nil, apiError(503, "unavailable", "Server channels are not available")
	}
	return h.system.ServerChannels.Test(ctx, id)
}

func (h *NotificationsHandler) CreateNotificationWebhook(ctx context.Context, userID int, profileID string, input notifications.WebhookInput) (*notifications.Webhook, string, error) {
	svc := h.webhooks()
	if svc == nil {
		return nil, "", apiError(503, "unavailable", "Webhooks are not available")
	}
	return svc.Create(ctx, userID, profileID, input)
}
func (h *NotificationsHandler) CreateNotificationServerChannel(ctx context.Context, userID int, input notifications.ServerChannelInput) (*notifications.ServerChannel, string, error) {
	if h == nil || h.system == nil || h.system.ServerChannels == nil {
		return nil, "", apiError(503, "unavailable", "Server channels are not available")
	}
	return h.system.ServerChannels.Create(ctx, userID, input)
}

func (h *NotificationsHandler) DeleteNotificationWebPushSubscription(ctx context.Context, user int, profile, id string) error {
	svc := h.webPush()
	if svc == nil {
		return apiError(503, "unavailable", "Web push is not available")
	}
	return svc.Unsubscribe(ctx, user, profile, id, "")
}

func (h *NotificationsHandler) UnsubscribeNotificationWebPush(ctx context.Context, user int, profile, endpoint string) error {
	svc := h.webPush()
	if svc == nil {
		return apiError(503, "unavailable", "Web push is not available")
	}
	return svc.Unsubscribe(ctx, user, profile, "", endpoint)
}

func (h *NotificationsHandler) SubscribeNotificationWebPush(ctx context.Context, user int, profile, endpoint, p256dh, auth, deviceName string) (*notifications.WebPushSubscription, error) {
	svc := h.webPush()
	if svc == nil {
		return nil, apiError(503, "unavailable", "Web push is not available")
	}
	return svc.Subscribe(ctx, user, profile, endpoint, p256dh, auth, deviceName)
}

func (h *NotificationsHandler) DeleteNotificationWebhook(ctx context.Context, profile, id string, check func(int64) error) error {
	svc := h.webhooks()
	if svc == nil {
		return apiError(503, "unavailable", "Webhooks are not available")
	}
	return svc.DeleteGuarded(ctx, profile, id, check)
}

func (h *NotificationsHandler) DeleteNotificationServerChannel(ctx context.Context, id string) error {
	if h == nil || h.system == nil || h.system.ServerChannels == nil {
		return apiError(503, "unavailable", "Server channels are not available")
	}
	return h.system.ServerChannels.Delete(ctx, id)
}

func (h *NotificationsHandler) RotateNotificationWebhookSecret(ctx context.Context, profile, id string) (string, error) {
	svc := h.webhooks()
	if svc == nil {
		return "", apiError(503, "unavailable", "Webhooks are not available")
	}
	return svc.RotateSecretV2(ctx, profile, id)
}

func (h *NotificationsHandler) UpdateNotificationWebhook(ctx context.Context, profile, id string, input notifications.WebhookInput, check func(int64) error) (*notifications.Webhook, error) {
	svc := h.webhooks()
	if svc == nil {
		return nil, apiError(503, "unavailable", "Webhooks are not available")
	}
	return svc.UpdateGuarded(ctx, profile, id, input, check)
}

func (h *NotificationsHandler) RotateNotificationServerChannelSecret(ctx context.Context, id string) (string, error) {
	if h == nil || h.system == nil || h.system.ServerChannels == nil {
		return "", apiError(503, "unavailable", "Server channels are not available")
	}
	return h.system.ServerChannels.RotateSecretV2(ctx, id)
}

func (h *NotificationsHandler) UpdateNotificationServerChannel(ctx context.Context, id string, input notifications.ServerChannelInput) (*notifications.ServerChannel, error) {
	if h == nil || h.system == nil || h.system.ServerChannels == nil {
		return nil, apiError(503, "unavailable", "Server channels are not available")
	}
	return h.system.ServerChannels.UpdateV2(ctx, id, input)
}
