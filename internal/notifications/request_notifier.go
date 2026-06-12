package notifications

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/requests"
	"github.com/oklog/ulid/v2"
)

// DeliveryTypeRequestFulfilled is the operational notice posted to the
// requesting profile once its media request is present in the catalog
// (docs/superpowers/plans/notifications/06, item 2). Its reason_flags carry
// {"request_id","tmdb_id","media_type"} instead of reason booleans; a partial
// unique index on (profile_id, request_id) makes the insert idempotent, and
// the per-webhook notify_requests flag gates the webhook channel.
const DeliveryTypeRequestFulfilled = "request.fulfilled"

// RequestFulfilledFlags is the decoded reason_flags shape for
// request.fulfilled deliveries.
type RequestFulfilledFlags struct {
	RequestID string `json:"request_id"`
	TMDBID    int    `json:"tmdb_id"`
	MediaType string `json:"media_type"`
}

// parseRequestFulfilledFlags decodes a request.fulfilled delivery's
// reason_flags; other types decode to the zero value.
func parseRequestFulfilledFlags(raw []byte) RequestFulfilledFlags {
	var flags RequestFulfilledFlags
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &flags)
	}
	return flags
}

// RequestFulfillmentNotifier adapts the notification system to
// requests.FulfillmentNotifier: it gates on the profile's master toggle and
// dispatches one durable request.fulfilled delivery across all channels.
type RequestFulfillmentNotifier struct {
	system *System
}

// NewRequestFulfillmentNotifier creates the adapter.
func NewRequestFulfillmentNotifier(system *System) *RequestFulfillmentNotifier {
	return &RequestFulfillmentNotifier{system: system}
}

// NotifyFulfilled implements requests.FulfillmentNotifier. contentID is the
// matched catalog item: deliveryRowSelect joins media_items on series_id, so
// that one field renders the title, poster, and deep link for movies and
// series alike. Returning nil without dispatching (master toggle off, missing
// attribution) still counts as handled — the caller stamps the request either
// way.
func (n *RequestFulfillmentNotifier) NotifyFulfilled(ctx context.Context, req requests.Request, contentID string) error {
	if n == nil || n.system == nil {
		return nil
	}
	// Server-channel broadcast first: it is community-facing and must not be
	// gated by the requester's personal preferences or attribution. Detached
	// and best-effort — a failure here must never block the
	// fulfilled_notified_at stamp, or the per-profile path would re-fire.
	n.system.PostServerChannelRequestEvent(ctx, ServerChannelEventRequestFulfilled, requestEventInfoFor(req))

	if req.RequestedByProfileID == "" || req.RequestedByUserID <= 0 {
		return nil // legacy rows without attribution have no recipient
	}
	prefs, err := n.system.Preferences.Get(ctx, req.RequestedByProfileID)
	if err != nil {
		return err
	}
	if !prefs.Enabled {
		return nil
	}
	flags, err := json.Marshal(RequestFulfilledFlags{
		RequestID: req.ID,
		TMDBID:    req.TMDBID,
		MediaType: string(req.MediaType),
	})
	if err != nil {
		return fmt.Errorf("marshal request fulfilled flags: %w", err)
	}
	delivery := Delivery{
		ID:          ulid.Make().String(),
		UserID:      req.RequestedByUserID,
		ProfileID:   req.RequestedByProfileID,
		SeriesID:    &contentID,
		Type:        DeliveryTypeRequestFulfilled,
		ReasonFlags: flags,
	}
	_, err = n.system.DispatchOperational(ctx, delivery, OperationalDispatch{
		WebhookFilter: func(hook Webhook) bool { return hook.NotifyRequests },
	})
	return err
}
