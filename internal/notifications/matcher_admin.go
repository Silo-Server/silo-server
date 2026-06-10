package notifications

import (
	"context"

	evt "github.com/Silo-Server/silo-server/internal/events"
)

// matchAdmin handles admin-channel events.
// implemented in a later task.
func (m *Materializer) matchAdmin(ctx context.Context, env evt.Envelope) error {
	return nil
}
