package notifications

import (
	"context"

	evt "github.com/Silo-Server/silo-server/internal/events"
)

// ContentResolver resolves content items to notification recipients.
// narrowed in a later task.
type ContentResolver interface{}

// matchContent handles content-channel events.
// implemented in a later task.
func (m *Materializer) matchContent(ctx context.Context, env evt.Envelope) error {
	return nil
}
