package requests

import (
	"context"
	"log/slog"

	evt "github.com/Silo-Server/silo-server/internal/events"
)

// RequestEventPayload is the data published on ChannelRequests for each
// lifecycle transition of a request.
type RequestEventPayload struct {
	RequestID string    `json:"request_id"`
	UserID    int       `json:"user_id"`
	ProfileID string    `json:"profile_id,omitempty"`
	Title     string    `json:"title"`
	MediaType MediaType `json:"media_type"`
	Quality   Quality   `json:"quality,omitempty"`
	Status    Status    `json:"status"`
}

// SetEventsHub attaches an event hub so the service publishes lifecycle events.
// Call before the service handles requests; nil hub disables publishing (no-op).
func (s *Service) SetEventsHub(hub *evt.Hub) { s.eventsHub = hub }

// publishRequestEvent publishes a lifecycle event for req onto ChannelRequests.
// It is a no-op when eventsHub is nil, and it never fails the caller — errors
// are logged at Warn level only.
func (s *Service) publishRequestEvent(ctx context.Context, event string, req Request) {
	if s.eventsHub == nil {
		return
	}
	payload := RequestEventPayload{
		RequestID: req.ID,
		UserID:    req.RequestedByUserID,
		ProfileID: req.RequestedByProfileID,
		Title:     req.Title,
		MediaType: req.MediaType,
		Status:    req.Status,
	}
	opts := evt.PublishOptions{
		UserID:    req.RequestedByUserID,
		ProfileID: req.RequestedByProfileID,
	}
	if err := s.eventsHub.PublishJSON(ctx, evt.ChannelRequests, event, payload, opts); err != nil {
		slog.WarnContext(ctx, "requests: failed to publish event",
			"event", event,
			"request_id", req.ID,
			"error", err,
		)
	}
}
