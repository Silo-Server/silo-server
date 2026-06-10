package notifications

import (
	"context"

	evt "github.com/Silo-Server/silo-server/internal/events"
)

// matchSend handles send-channel events.
// implemented in a later task.
func (m *Materializer) matchSend(ctx context.Context, env evt.Envelope) error {
	return nil
}
