package handlers

import (
	"context"
	"time"

	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/notifications"
)

type NotificationInboxPageView struct {
	Items   []notifications.DeliveryRowPayload
	Through notifications.Cursor
	More    bool
}

func (h *NotificationsHandler) ListNotificationInbox(ctx context.Context, profile string, unread bool, limit int, before, through *notifications.Cursor) (NotificationInboxPageView, error) {
	var boundary notifications.Cursor
	var err error
	if through == nil {
		boundary, err = h.system.Deliveries.InboxCutoff(ctx, profile)
		if err != nil {
			return NotificationInboxPageView{}, err
		}
	} else {
		boundary = *through
	}
	rows, more, err := h.system.Deliveries.ListInboxWindow(ctx, profile, unread, limit, before, boundary)
	if err != nil {
		return NotificationInboxPageView{}, err
	}
	return NotificationInboxPageView{Items: h.system.PayloadsForRows(ctx, rows), Through: boundary, More: more}, nil
}
func (h *NotificationsHandler) SyncNotificationInbox(ctx context.Context, profile string, limit int, since *notifications.Cursor) ([]notifications.DeliveryRowPayload, bool, int, error) {
	// Initial sync is the existing bounded newest snapshot, returned ascending.
	// A saved cursor subsequently advances through every newer delivery.
	requested := limit
	if since != nil {
		requested++
	}
	rows, err := h.system.Deliveries.ListSync(ctx, profile, since, requested)
	if err != nil {
		return nil, false, 0, err
	}
	more := since != nil && len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	count, err := h.system.Deliveries.UnreadCount(ctx, profile)
	if err != nil {
		return nil, false, 0, err
	}
	return h.system.PayloadsForRows(ctx, rows), more, count, nil
}
func (h *NotificationsHandler) GetNotificationInboxItem(ctx context.Context, profile, id string) (notifications.DeliveryRowPayload, error) {
	row, err := h.system.Deliveries.GetByID(ctx, profile, id)
	if err != nil {
		return notifications.DeliveryRowPayload{}, err
	}
	if row == nil {
		return notifications.DeliveryRowPayload{}, apiError(404, "not_found", "Notification not found")
	}
	return h.system.PayloadForRow(ctx, *row), nil
}
func (h *NotificationsHandler) NotificationUnreadCount(ctx context.Context, profile string) (int, error) {
	return h.system.Deliveries.UnreadCount(ctx, profile)
}
func (h *NotificationsHandler) MarkNotificationRead(ctx context.Context, userID int, profile, id string) error {
	changed, err := h.system.Deliveries.MarkRead(ctx, profile, id)
	if err != nil {
		return err
	}
	if !changed {
		exists, err := h.system.Deliveries.Exists(ctx, profile, id)
		if err != nil {
			return err
		}
		if !exists {
			return apiError(404, "not_found", "Notification not found")
		}
	}
	if changed && h.hub != nil {
		_ = h.hub.PublishJSON(ctx, evt.ChannelNotifications, notifications.EventNotificationRead, map[string]any{settingFieldProfileID: profile, "id": id}, evt.PublishOptions{UserID: userID, ProfileID: profile})
	}
	return nil
}
func (h *NotificationsHandler) MarkNotificationInboxThrough(ctx context.Context, userID int, profile string, through notifications.Cursor) error {
	changed, err := h.system.Deliveries.MarkReadThrough(ctx, profile, through)
	if err != nil {
		return err
	}
	if changed > 0 && h.hub != nil {
		_ = h.hub.PublishJSON(ctx, evt.ChannelNotifications, notifications.EventNotificationRead, map[string]any{settingFieldProfileID: profile, "through_created_at": through.CreatedAt.UTC().Format(time.RFC3339Nano), "through_id": through.ID}, evt.PublishOptions{UserID: userID, ProfileID: profile})
	}
	return nil
}
func (h *NotificationsHandler) NotificationPreferences(ctx context.Context, profile string) (notifications.Preferences, error) {
	return h.system.Preferences.Get(ctx, profile)
}
func (h *NotificationsHandler) PatchNotificationPreferences(ctx context.Context, profile string, patch notifications.PreferencePatch) (notifications.Preferences, error) {
	return h.system.Preferences.Patch(ctx, profile, patch)
}

// NotificationPushDisplay uses the same profile-scoped row and rendering as the
// bridge notification extension endpoint without hydrating unrelated inbox data.
func (h *NotificationsHandler) NotificationPushDisplay(ctx context.Context, profile, id string) (notifications.NotificationDisplay, error) {
	row, err := h.system.Deliveries.GetByID(ctx, profile, id)
	if err != nil {
		return notifications.NotificationDisplay{}, err
	}
	if row == nil {
		return notifications.NotificationDisplay{}, apiError(404, "not_found", "Notification not found")
	}
	return notifications.BuildNotificationDisplay(*row), nil
}
