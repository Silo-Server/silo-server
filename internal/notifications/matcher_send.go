package notifications

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	evt "github.com/Silo-Server/silo-server/internal/events"
)

// sendPayload is the documented notifications.send contract for plugins.
type sendPayload struct {
	UserID    int    `json:"user_id"`
	ProfileID string `json:"profile_id,omitempty"`
	Category  string `json:"category"`
	Type      string `json:"type"`
	Title     string `json:"title"`
	Body      string `json:"body,omitempty"`
	Link      string `json:"link,omitempty"`
	DedupRef  string `json:"dedup_ref,omitempty"`
}

// matchSend consumes "notifications.send" events. Plugin-published events
// arrive prefixed ("plugin.<id>.notifications.send"); both forms accepted.
// Malformed payloads are dropped with a warning — plugin bugs must not error
// core paths. Plugins may not create announcements.
func (m *Materializer) matchSend(ctx context.Context, env evt.Envelope) error {
	if env.Event != "notifications.send" && !strings.HasSuffix(env.Event, ".notifications.send") {
		return nil
	}
	var p sendPayload
	if err := json.Unmarshal(env.Data, &p); err != nil {
		slog.WarnContext(ctx, "notifications: malformed notifications.send dropped", "event", env.Event, "error", err)
		return nil
	}
	cat := Category(p.Category)
	if p.UserID <= 0 || p.Type == "" || p.Title == "" || !validCategory(cat) || cat == CategoryAnnouncement {
		slog.WarnContext(ctx, "notifications: invalid notifications.send dropped", "event", env.Event, "user_id", p.UserID)
		return nil
	}
	return m.svc.Create(ctx, CreateInput{
		UserID: p.UserID, ProfileID: p.ProfileID, Category: cat, Type: p.Type,
		Title: p.Title, Body: p.Body, Link: p.Link,
		SourceEvent: env.Event, DedupRef: p.DedupRef,
	})
}
