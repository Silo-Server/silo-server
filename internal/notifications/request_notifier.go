package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/requests"
	"github.com/oklog/ulid/v2"
)

// Request lifecycle delivery types posted to the requesting profile. Their
// reason_flags carry RequestFlags instead of reason booleans; partial unique
// indexes per (profile_id, request_id, type) make the inserts idempotent, and
// the per-webhook notify_requests flag gates the webhook channel.
const (
	// DeliveryTypeRequestFulfilled is the operational notice posted once the
	// requested media is present in the catalog
	// (docs/superpowers/plans/notifications/06, item 2).
	DeliveryTypeRequestFulfilled = "request.fulfilled"
	// DeliveryTypeRequestApproved notifies the requesting profile that an
	// admin (or auto-approval) approved their request.
	DeliveryTypeRequestApproved = "request.approved"
	// DeliveryTypeRequestDeclined notifies the requesting profile that an
	// admin declined their request.
	DeliveryTypeRequestDeclined = "request.declined"
)

// RequestFlags is the decoded reason_flags shape for request.* deliveries.
// Fulfilled rows carry only the identifiers (the catalog join renders their
// display fields); approved/declined rows have no catalog item yet, so the
// title rides along.
type RequestFlags struct {
	RequestID string `json:"request_id"`
	TMDBID    int    `json:"tmdb_id"`
	MediaType string `json:"media_type"`
	Title     string `json:"title,omitempty"`
	Year      int    `json:"year,omitempty"`
	// Reason is the admin's decline message, when one was given.
	Reason string `json:"reason,omitempty"`
}

// parseRequestFlags decodes a request.* delivery's reason_flags; other types
// decode to the zero value.
func parseRequestFlags(raw []byte) RequestFlags {
	var flags RequestFlags
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &flags)
	}
	return flags
}

// isRequestLifecycleType reports whether the delivery is a request-status
// notice rendered from RequestFlags alone (no catalog join exists yet).
func isRequestLifecycleType(deliveryType string) bool {
	return deliveryType == DeliveryTypeRequestApproved ||
		deliveryType == DeliveryTypeRequestDeclined
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
	flags, err := json.Marshal(RequestFlags{
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

// requestLifecycleDispatchTimeout bounds one detached lifecycle dispatch.
const requestLifecycleDispatchTimeout = 30 * time.Second

// RequestLifecycleNotifier adapts request lifecycle transitions (submitted,
// approved, declined) to notifications: community server-channel posts for
// every event, plus a personal delivery to the requesting profile for
// approved and declined. Fulfillment stays on RequestFulfillmentNotifier,
// whose presence-checked flow runs on the reconcile service. Dispatch is
// detached and best-effort per the requests.LifecycleNotifier contract: a
// transition never waits on or fails because of notifications.
type RequestLifecycleNotifier struct {
	system *System
}

// NewRequestLifecycleNotifier creates the adapter; returns nil when there is
// no notification system. Server-channel posts inside it degrade to no-ops
// when server channels are unconfigured (no at-rest cipher); the personal
// delivery path has no such dependency.
func NewRequestLifecycleNotifier(system *System) *RequestLifecycleNotifier {
	if system == nil {
		return nil
	}
	return &RequestLifecycleNotifier{system: system}
}

// RequestSubmitted implements requests.LifecycleNotifier. Submission posts to
// server channels only: the requester performed the action themselves, so a
// personal confirmation would be noise.
func (n *RequestLifecycleNotifier) RequestSubmitted(ctx context.Context, req requests.Request) {
	n.system.PostServerChannelRequestEvent(ctx, ServerChannelEventRequestSubmitted, requestEventInfoFor(req))
}

// RequestApproved implements requests.LifecycleNotifier.
func (n *RequestLifecycleNotifier) RequestApproved(ctx context.Context, req requests.Request) {
	n.system.PostServerChannelRequestEvent(ctx, ServerChannelEventRequestApproved, requestEventInfoFor(req))
	n.dispatchPersonal(ctx, req, DeliveryTypeRequestApproved)
}

// RequestDeclined implements requests.LifecycleNotifier.
func (n *RequestLifecycleNotifier) RequestDeclined(ctx context.Context, req requests.Request) {
	n.system.PostServerChannelRequestEvent(ctx, ServerChannelEventRequestDeclined, requestEventInfoFor(req))
	n.dispatchPersonal(ctx, req, DeliveryTypeRequestDeclined)
}

// dispatchPersonal creates the requester's personal delivery on a detached
// goroutine: the lifecycle contract requires non-blocking dispatch, and the
// caller's context ends with its HTTP request.
func (n *RequestLifecycleNotifier) dispatchPersonal(ctx context.Context, req requests.Request, deliveryType string) {
	dispatchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), requestLifecycleDispatchTimeout)
	go func() {
		defer cancel()
		if err := n.system.dispatchRequestLifecycle(dispatchCtx, req, deliveryType); err != nil {
			n.system.logger.Warn("request lifecycle delivery failed",
				"request_id", req.ID, "type", deliveryType, "error", err)
		}
	}()
}

// dispatchRequestLifecycle creates one durable request-status delivery for
// the requesting profile across all channels. Mirrors the fulfilled path:
// gated on attribution and the profile's master toggle, idempotent per
// (profile, request, type) via the partial unique index.
func (s *System) dispatchRequestLifecycle(ctx context.Context, req requests.Request, deliveryType string) error {
	if req.RequestedByProfileID == "" || req.RequestedByUserID <= 0 {
		return nil // requests without attribution have no recipient
	}
	prefs, err := s.Preferences.Get(ctx, req.RequestedByProfileID)
	if err != nil {
		return err
	}
	if !prefs.Enabled {
		return nil
	}
	flags := RequestFlags{
		RequestID: req.ID,
		TMDBID:    req.TMDBID,
		MediaType: string(req.MediaType),
		Title:     req.Title,
		Reason:    req.DeclineReason,
	}
	if req.Year != nil {
		flags.Year = *req.Year
	}
	encoded, err := json.Marshal(flags)
	if err != nil {
		return fmt.Errorf("marshal request lifecycle flags: %w", err)
	}
	delivery := Delivery{
		ID:          ulid.Make().String(),
		UserID:      req.RequestedByUserID,
		ProfileID:   req.RequestedByProfileID,
		Type:        deliveryType,
		ReasonFlags: encoded,
	}
	_, err = s.DispatchOperational(ctx, delivery, OperationalDispatch{
		WebhookFilter: func(hook Webhook) bool { return hook.NotifyRequests },
	})
	return err
}
