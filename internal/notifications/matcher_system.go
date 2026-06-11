package notifications

import (
	"context"
	"fmt"

	evt "github.com/Silo-Server/silo-server/internal/events"
)

// matchSystem materializes account-security notifications from domain events.
// Currently the only source is a password change published by the admin user
// handler on ChannelSessions; keeping it event-driven means the admin handler
// has no dependency on the notifications service.
func (m *Materializer) matchSystem(ctx context.Context, env evt.Envelope) error {
	if env.Channel != evt.ChannelSessions || env.Event != EventUserPasswordChanged {
		return nil
	}
	if env.UserID <= 0 {
		return fmt.Errorf("%s without user_id", EventUserPasswordChanged)
	}
	m.svc.CreateSystem(ctx, env.UserID,
		"system.password_changed", "Password changed",
		"Your account password was changed. If this wasn't you, contact your administrator.")
	return nil
}
