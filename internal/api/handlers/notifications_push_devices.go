package handlers

import (
	"github.com/Silo-Server/silo-server/internal/notifications"
)

func (h *NotificationsHandler) pushDevices() *notifications.PushDeviceService {
	if h == nil || h.system == nil {
		return nil
	}
	return h.system.PushDevices
}
