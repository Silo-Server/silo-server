package notifications

import (
	"context"
	"encoding/json"
	"time"

	evt "github.com/Silo-Server/silo-server/internal/events"
)

// EventNotificationCreated is published on ChannelNotifications when a new
// delivery is created; EventNotificationRead when one is marked read
// (multi-tab coherence).
const (
	EventNotificationCreated = "notification.created"
	EventNotificationRead    = "notification.read"
)

// Dispatcher fans one committed notification delivery out to a channel.
// Called once per notification_deliveries row, AFTER the row commits. The
// websocket dispatcher is idempotent by delivery_id alone (re-publishing the
// same delivery is a no-op for connected clients); per-target channels (push,
// webhooks — see specs 02-04) claim durable `pending` outbox attempt rows
// enqueued in the fanout transaction instead of deciding their own work.
type Dispatcher interface {
	Dispatch(ctx context.Context, delivery DeliveryRow) error
}

// DeliveryRowPayload is the JSON shape shared by the inbox API, the websocket
// snapshot, and notification.created events. Keep these in lockstep.
type DeliveryRowPayload struct {
	ID              string          `json:"id"`
	Type            string          `json:"type"`
	ProfileID       string          `json:"profile_id"`
	LibraryID       *int            `json:"library_id,omitempty"`
	SeriesID        *string         `json:"series_id,omitempty"`
	EpisodeID       *string         `json:"episode_id,omitempty"`
	SeriesTitle     string          `json:"series_title,omitempty"`
	EpisodeTitle    string          `json:"episode_title,omitempty"`
	SeasonNumber    *int            `json:"season_number,omitempty"`
	EpisodeNumber   *int            `json:"episode_number,omitempty"`
	PosterPath      string          `json:"poster_path,omitempty"`
	PosterURL       string          `json:"poster_url,omitempty"`
	PosterThumbhash string          `json:"poster_thumbhash,omitempty"`
	ReasonFlags     json.RawMessage `json:"reason_flags"`
	CreatedAt       time.Time       `json:"created_at"`
	ReadAt          *time.Time      `json:"read_at"`
}

// PayloadForRow converts a DeliveryRow into its wire shape.
func PayloadForRow(row DeliveryRow) DeliveryRowPayload {
	reasonFlags := json.RawMessage(row.ReasonFlags)
	if len(reasonFlags) == 0 {
		reasonFlags = json.RawMessage("{}")
	}
	return DeliveryRowPayload{
		ID:              row.ID,
		Type:            row.Type,
		ProfileID:       row.ProfileID,
		LibraryID:       row.LibraryID,
		SeriesID:        row.SeriesID,
		EpisodeID:       row.EpisodeID,
		SeriesTitle:     row.SeriesTitle,
		EpisodeTitle:    row.EpisodeTitle,
		SeasonNumber:    row.SeasonNumber,
		EpisodeNumber:   row.EpisodeNumber,
		PosterPath:      row.PosterPath,
		PosterThumbhash: row.PosterThumbhash,
		ReasonFlags:     reasonFlags,
		CreatedAt:       row.CreatedAt,
		ReadAt:          row.ReadAt,
	}
}

// WebsocketDispatcher publishes notification.created on ChannelNotifications,
// scoped to the delivery's (user_id, profile_id). Best-effort: the durable
// inbox row is the source of truth and covers reconnect.
type WebsocketDispatcher struct {
	hub *evt.Hub
	// payload overrides the default PayloadForRow conversion (e.g. to attach
	// presigned poster URLs). Optional.
	payload func(ctx context.Context, row DeliveryRow) DeliveryRowPayload
}

// NewWebsocketDispatcher creates a WebsocketDispatcher.
func NewWebsocketDispatcher(hub *evt.Hub) *WebsocketDispatcher {
	return &WebsocketDispatcher{hub: hub}
}

// Dispatch publishes the delivery to connected clients.
func (d *WebsocketDispatcher) Dispatch(ctx context.Context, delivery DeliveryRow) error {
	if d == nil || d.hub == nil {
		return nil
	}
	payload := PayloadForRow(delivery)
	if d.payload != nil {
		payload = d.payload(ctx, delivery)
	}
	return d.hub.PublishJSON(ctx, evt.ChannelNotifications, EventNotificationCreated,
		payload, evt.PublishOptions{
			UserID:    delivery.UserID,
			ProfileID: delivery.ProfileID,
		})
}

// MultiDispatcher runs all configured dispatchers; channel failures are
// isolated so a downed channel never blocks the others.
type MultiDispatcher struct {
	dispatchers []Dispatcher
}

// NewMultiDispatcher creates a MultiDispatcher over the given channels.
func NewMultiDispatcher(dispatchers ...Dispatcher) *MultiDispatcher {
	out := make([]Dispatcher, 0, len(dispatchers))
	for _, d := range dispatchers {
		if d != nil {
			out = append(out, d)
		}
	}
	return &MultiDispatcher{dispatchers: out}
}

// Dispatch fans the delivery to every channel, returning the first error
// (after attempting all channels).
func (m *MultiDispatcher) Dispatch(ctx context.Context, delivery DeliveryRow) error {
	if m == nil {
		return nil
	}
	var firstErr error
	for _, d := range m.dispatchers {
		if err := d.Dispatch(ctx, delivery); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
