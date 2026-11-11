package handlers

import (
	"github.com/Silo-Server/silo-server/internal/notifications"
)

func (h *NotificationsHandler) webhooks() *notifications.WebhookService {
	if h == nil || h.system == nil {
		return nil
	}
	return h.system.Webhooks
}
