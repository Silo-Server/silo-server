package push

import (
	"context"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/notifications"
)

// deviceSource is the slice of Store the enqueuer needs (interface for testing).
type deviceSource interface {
	EligibleDevices(ctx context.Context, userID int, profileID, category string) ([]Device, error)
	EnqueueDelivery(ctx context.Context, notificationID int64, d Device, notBefore time.Time) error
}

var _ deviceSource = (*Store)(nil)

// Enqueuer mirrors created notifications to per-device delivery rows with a
// presence-grace not_before. Best-effort: failures log and return.
type Enqueuer struct {
	src   deviceSource
	grace time.Duration
	now   func() time.Time
}

func NewEnqueuer(src deviceSource, grace time.Duration, now func() time.Time) *Enqueuer {
	if now == nil {
		now = time.Now
	}
	return &Enqueuer{src: src, grace: grace, now: now}
}

func (e *Enqueuer) EnqueueForNotification(ctx context.Context, n *notifications.Notification) {
	if n == nil || n.UserID <= 0 {
		return
	}
	profileID := ""
	if n.ProfileID != nil {
		profileID = *n.ProfileID
	}
	devices, err := e.src.EligibleDevices(ctx, n.UserID, profileID, string(n.Category))
	if err != nil {
		slog.WarnContext(ctx, "push: eligible devices failed", "notification_id", n.ID, "error", err)
		return
	}
	notBefore := e.now().Add(e.grace)
	for _, d := range devices {
		if err := e.src.EnqueueDelivery(ctx, n.ID, d, notBefore); err != nil {
			slog.WarnContext(ctx, "push: enqueue delivery failed", "notification_id", n.ID, "device_id", d.DeviceID, "error", err)
		}
	}
}

// (compile-time check that Enqueuer satisfies the notifications seam)
var _ notifications.PushEnqueuer = (*Enqueuer)(nil)
