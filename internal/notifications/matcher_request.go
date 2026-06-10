package notifications

import (
	"context"
	"encoding/json"
	"fmt"

	evt "github.com/Silo-Server/silo-server/internal/events"
)

// RequestEventData mirrors requests.RequestEventPayload (kept separate to
// avoid an import cycle: requests must not import notifications).
// MediaType and Quality are carried for completeness of the mirror; matchers do not use them yet.
type RequestEventData struct {
	RequestID string `json:"request_id"`
	UserID    int    `json:"user_id"`
	ProfileID string `json:"profile_id,omitempty"`
	Title     string `json:"title"`
	MediaType string `json:"media_type"`
	Quality   string `json:"quality,omitempty"`
	Status    string `json:"status"`
}

var requestEventTitles = map[string]string{
	"request.approved":  "Request approved",
	"request.declined":  "Request declined",
	"request.completed": "Request available",
	"request.failed":    "Request failed",
}

func (m *Materializer) matchRequest(ctx context.Context, env evt.Envelope) error {
	if env.Channel != evt.ChannelRequests {
		return nil
	}
	title, notify := requestEventTitles[env.Event]
	if !notify {
		return nil // request.submitted / request.cancelled: requester acted, no notification
	}
	var data RequestEventData
	if err := json.Unmarshal(env.Data, &data); err != nil {
		return fmt.Errorf("decode %s: %w", env.Event, err)
	}
	if data.UserID <= 0 {
		return fmt.Errorf("%s without user_id", env.Event)
	}
	return m.svc.Create(ctx, CreateInput{
		UserID:      data.UserID,
		ProfileID:   data.ProfileID,
		Category:    CategoryRequest,
		Type:        env.Event,
		Title:       title,
		Body:        data.Title,
		Link:        "/requests",
		SourceEvent: env.Event,
		DedupRef:    fmt.Sprintf("%s:%s", data.RequestID, statusSuffix(env.Event)),
	})
}

func statusSuffix(event string) string {
	switch event {
	case "request.approved":
		return "approved"
	case "request.declined":
		return "declined"
	case "request.completed":
		return "completed"
	case "request.failed":
		return "failed"
	}
	return event
}
