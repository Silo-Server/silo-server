package handlers

import (
	"context"
	"errors"
	"net/http"
	"time"

	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/notifications"
)

type NotificationInboxPageView struct {
	Items   []notifications.DeliveryRowPayload
	Through notifications.Cursor
	More    bool
}

func (h *NotificationsHandler) ListNotificationInbox(ctx context.Context, userID int, profile string, unread bool, limit int, before, through *notifications.Cursor) (NotificationInboxPageView, error) {
	var boundary notifications.Cursor
	var err error
	if through == nil {
		boundary, err = h.system.Deliveries.InboxCutoff(ctx, userID, profile)
		if err != nil {
			return NotificationInboxPageView{}, err
		}
	} else {
		boundary = *through
	}
	rows, more, err := h.system.Deliveries.ListInboxWindow(ctx, userID, profile, unread, limit, before, boundary)
	if err != nil {
		return NotificationInboxPageView{}, err
	}
	return NotificationInboxPageView{Items: h.system.PayloadsForRows(ctx, rows), Through: boundary, More: more}, nil
}
func (h *NotificationsHandler) SyncNotificationInbox(ctx context.Context, userID int, profile string, limit int, since *notifications.Cursor) ([]notifications.DeliveryRowPayload, bool, int, error) {
	// Initial sync is the existing bounded newest snapshot, returned ascending.
	// A saved cursor subsequently advances through every newer delivery.
	requested := limit
	if since != nil {
		requested++
	}
	rows, err := h.system.Deliveries.ListSync(ctx, userID, profile, since, requested)
	if err != nil {
		return nil, false, 0, err
	}
	more := since != nil && len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	count, err := h.system.Deliveries.UnreadCount(ctx, userID, profile)
	if err != nil {
		return nil, false, 0, err
	}
	return h.system.PayloadsForRows(ctx, rows), more, count, nil
}
func (h *NotificationsHandler) GetNotificationInboxItem(ctx context.Context, userID int, profile, id string) (notifications.DeliveryRowPayload, error) {
	row, err := h.system.Deliveries.GetByID(ctx, userID, profile, id)
	if err != nil {
		return notifications.DeliveryRowPayload{}, err
	}
	if row == nil {
		return notifications.DeliveryRowPayload{}, apiError(404, "not_found", "Notification not found")
	}
	return h.system.PayloadForRow(ctx, *row), nil
}
func (h *NotificationsHandler) NotificationUnreadCount(ctx context.Context, userID int, profile string) (int, error) {
	return h.system.Deliveries.UnreadCount(ctx, userID, profile)
}
func (h *NotificationsHandler) MarkNotificationRead(ctx context.Context, userID int, profile, id string) error {
	changed, err := h.system.Deliveries.MarkRead(ctx, userID, profile, id)
	if err != nil {
		return err
	}
	if !changed {
		exists, err := h.system.Deliveries.Exists(ctx, userID, profile, id)
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
	changed, err := h.system.Deliveries.MarkReadThrough(ctx, userID, profile, through)
	if err != nil {
		return err
	}
	if changed > 0 && h.hub != nil {
		_ = h.hub.PublishJSON(ctx, evt.ChannelNotifications, notifications.EventNotificationRead, map[string]any{settingFieldProfileID: profile, "through_created_at": through.CreatedAt.UTC().Format(time.RFC3339Nano), "through_id": through.ID}, evt.PublishOptions{UserID: userID, ProfileID: profile})
	}
	return nil
}
func (h *NotificationsHandler) NotificationPreferences(ctx context.Context, userID int, profile string) (notifications.Preferences, error) {
	return h.system.Preferences.Get(ctx, userID, profile)
}
func (h *NotificationsHandler) PatchNotificationPreferences(ctx context.Context, userID int, profile string, patch notifications.PreferencePatch) (notifications.Preferences, error) {
	return h.system.Preferences.Patch(ctx, userID, profile, patch)
}

// ListNotifiedSeries answers every series whose new episodes the profile has
// a reason to hear about, before catalog access filtering.
func (h *NotificationsHandler) ListNotifiedSeries(ctx context.Context, userID int, profile string) ([]notifications.NotifiedSeries, error) {
	return h.system.ListNotifiedSeries(ctx, userID, profile)
}

// MuteNotificationSeries mutes the series for the profile. The caller has
// checked that the viewer may see the item; an ID that names no series is 404.
func (h *NotificationsHandler) MuteNotificationSeries(ctx context.Context, userID int, profile, seriesID string) error {
	err := h.system.MuteSeries(ctx, userID, profile, seriesID)
	if errors.Is(err, notifications.ErrSeriesNotFound) {
		return apiError(http.StatusNotFound, "not_found", "Series not found")
	}
	return err
}

// UnmuteNotificationSeries lifts the profile's mute of the series.
func (h *NotificationsHandler) UnmuteNotificationSeries(ctx context.Context, userID int, profile, seriesID string) error {
	return h.system.UnmuteSeries(ctx, userID, profile, seriesID)
}

// NotificationPushDisplay uses the same profile-scoped row and rendering as the
// bridge notification extension endpoint without hydrating unrelated inbox data.
func (h *NotificationsHandler) NotificationPushDisplay(ctx context.Context, userID int, profile, id string) (notifications.NotificationDisplay, error) {
	row, err := h.system.Deliveries.GetByID(ctx, userID, profile, id)
	if err != nil {
		return notifications.NotificationDisplay{}, err
	}
	if row == nil {
		return notifications.NotificationDisplay{}, apiError(404, "not_found", "Notification not found")
	}
	return notifications.BuildNotificationDisplay(*row), nil
}
