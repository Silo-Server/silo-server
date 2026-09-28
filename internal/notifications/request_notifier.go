package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/requests"
	"github.com/oklog/ulid/v2"
)

// Request lifecycle delivery types posted to the requesting profile (and, for
// request.fulfilled, to followers). Their reason_flags carry RequestFlags
// instead of reason booleans; partial unique indexes make the inserts
// idempotent (request.fulfilled per account, profile and request; approved
// and declined per profile, request and type), and the per-webhook
// notify_requests flag gates the webhook channel.
const (
	// DeliveryTypeRequestFulfilled is the operational notice posted once the
	// requested media is present in the catalog
	// (docs/architecture/notifications.md, "Request notifications").
	DeliveryTypeRequestFulfilled = "request.fulfilled"
	// DeliveryTypeRequestApproved notifies the requesting profile that an
	// admin approved their request; auto-approval does not send it.
	DeliveryTypeRequestApproved = "request.approved"
	// DeliveryTypeRequestDeclined notifies the requesting profile that an
	// admin declined their request.
	DeliveryTypeRequestDeclined = "request.declined"
)

// followedTitleAvailable heads a request.fulfilled copy sent to a follower.
const followedTitleAvailable = "A title you followed is now available"

// RequestFlags is the decoded reason_flags shape for request.* deliveries.
// Fulfilled rows carry only the identifiers (the catalog join renders their
// display fields); approved/declined rows have no catalog item yet, so the
// title rides along.
type RequestFlags struct {
	RequestID  string `json:"request_id"`
	TMDBID     int    `json:"tmdb_id"`
	MediaType  string `json:"media_type"`
	Title      string `json:"title,omitempty"`
	Year       int    `json:"year,omitempty"`
	PosterPath string `json:"poster_path,omitempty"`
	// Reason is the admin's decline message, when one was given.
	Reason string `json:"reason,omitempty"`
	// Follower marks a request.fulfilled copy sent to a profile that followed
	// the title rather than requested it, so the copy does not say "your
	// request".
	Follower bool `json:"follower,omitempty"`
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
// requests.FulfillmentNotifier: it gates on each profile's master toggle and
// dispatches one durable request.fulfilled delivery per recipient across all
// channels.
type RequestFulfillmentNotifier struct {
	backend fulfillmentBackend
}

// fulfillmentBackend is the slice of System the fulfillment adapter uses; the
// unexported methods keep *System its only production implementation.
type fulfillmentBackend interface {
	PostServerChannelRequestEvent(ctx context.Context, event string, info RequestEventInfo)
	notificationsEnabled(ctx context.Context, profileID string) (bool, error)
	dispatchFulfilled(ctx context.Context, delivery Delivery) error
}

// notificationsEnabled reads the profile's master toggle. Notification
// preferences are keyed by profile id alone, so two accounts' legacy
// "default" profiles share one row, as they do for every notification type.
func (s *System) notificationsEnabled(ctx context.Context, profileID string) (bool, error) {
	prefs, err := s.Preferences.Get(ctx, profileID)
	if err != nil {
		return false, err
	}
	return prefs.Enabled, nil
}

func (s *System) dispatchFulfilled(ctx context.Context, delivery Delivery) error {
	_, err := s.DispatchOperational(ctx, delivery, OperationalDispatch{
		WebhookFilter: func(hook Webhook) bool { return hook.NotifyRequests },
	})
	return err
}

// NewRequestFulfillmentNotifier creates the adapter.
func NewRequestFulfillmentNotifier(system *System) *RequestFulfillmentNotifier {
	if system == nil {
		return &RequestFulfillmentNotifier{}
	}
	return &RequestFulfillmentNotifier{backend: system}
}

// NotifyFulfilled implements requests.FulfillmentNotifier. contentID is the
// matched catalog item: deliveryRowSelect joins media_items on series_id, so
// that one field renders the title, poster, and deep link for movies and
// series alike. It tells the requester, then every follower, and returns the
// first dispatch error so the caller retries the whole request later; a
// recipient already told is deduped by the (account, profile, request) unique
// index.
// Skipping a recipient (master toggle off, missing attribution) still counts
// as handled.
func (n *RequestFulfillmentNotifier) NotifyFulfilled(ctx context.Context, req requests.Request, contentID string) error {
	if n == nil || n.backend == nil {
		return nil
	}
	base := RequestFlags{RequestID: req.ID, TMDBID: req.TMDBID, MediaType: string(req.MediaType)}
	// Legacy rows without attribution have no requester recipient.
	// Profile ids repeat across accounts, so recipients are keyed by both.
	told := map[requests.Follower]bool{}
	if req.RequestedByProfileID != "" && req.RequestedByUserID > 0 {
		requester := requests.Follower{UserID: req.RequestedByUserID, ProfileID: req.RequestedByProfileID}
		told[requester] = true
		if err := n.notifyFulfilledProfile(ctx, requester, contentID, base); err != nil {
			return err
		}
	}
	follower := base
	follower.Follower = true
	for _, recipient := range req.Followers {
		if told[recipient] {
			continue
		}
		told[recipient] = true
		if err := n.notifyFulfilledProfile(ctx, recipient, contentID, follower); err != nil {
			return err
		}
	}
	// The server-channel post goes last, and only once every recipient was
	// dispatched: a failure above makes the caller retry the request, and
	// posting first would repeat the community announcement on every retry.
	// It is not gated by anyone's personal preferences, and it is detached
	// and best-effort.
	n.backend.PostServerChannelRequestEvent(ctx, ServerChannelEventRequestFulfilled, requestEventInfoFor(req))
	return nil
}

// notifyFulfilledProfile posts one request.fulfilled delivery to a profile
// whose notifications are on.
func (n *RequestFulfillmentNotifier) notifyFulfilledProfile(ctx context.Context, recipient requests.Follower, contentID string, flags RequestFlags) error {
	enabled, err := n.backend.notificationsEnabled(ctx, recipient.ProfileID)
	if err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	raw, err := json.Marshal(flags)
	if err != nil {
		return fmt.Errorf("marshal request fulfilled flags: %w", err)
	}
	return n.backend.dispatchFulfilled(ctx, Delivery{
		ID:          ulid.Make().String(),
		UserID:      recipient.UserID,
		ProfileID:   recipient.ProfileID,
		SeriesID:    &contentID,
		Type:        DeliveryTypeRequestFulfilled,
		ReasonFlags: raw,
	})
}

// requestLifecycleDispatchTimeout bounds one detached lifecycle dispatch.
const requestLifecycleDispatchTimeout = 30 * time.Second

// requestLifecycleBackend is the slice of System the lifecycle adapter uses.
// Both methods detach internally, so the adapter itself stays synchronous.
// The unexported method keeps the interface in-package, leaving *System as
// the only production implementation.
type requestLifecycleBackend interface {
	PostServerChannelRequestEvent(ctx context.Context, event string, info RequestEventInfo)
	dispatchRequestLifecycleDetached(ctx context.Context, req requests.Request, deliveryType string)
}

// RequestLifecycleNotifier adapts request lifecycle transitions (submitted,
// approved, declined) to notifications: community server-channel posts for
// every event, plus a personal delivery to the requesting profile on admin
// approval and decline. Fulfillment stays on RequestFulfillmentNotifier,
// whose presence-checked flow runs on the reconcile service. Dispatch is
// detached and best-effort per the requests.LifecycleNotifier contract: a
// transition never waits on or fails because of notifications.
type RequestLifecycleNotifier struct {
	backend requestLifecycleBackend
	logger  *slog.Logger
}

// NewRequestLifecycleNotifier creates the adapter; returns nil when there is
// no notification system. Server-channel posts inside it degrade to no-ops
// when server channels are unconfigured (no at-rest cipher); the personal
// delivery path has no such dependency.
func NewRequestLifecycleNotifier(system *System) *RequestLifecycleNotifier {
	if system == nil {
		return nil
	}
	return &RequestLifecycleNotifier{backend: system, logger: system.logger}
}

// RequestSubmitted implements requests.LifecycleNotifier. Submission posts to
// server channels only: the requester performed the action themselves, so a
// personal confirmation would be noise.
func (n *RequestLifecycleNotifier) RequestSubmitted(ctx context.Context, req requests.Request) {
	n.backend.PostServerChannelRequestEvent(ctx, ServerChannelEventRequestSubmitted, requestEventInfoFor(req))
}

// RequestApproved implements requests.LifecycleNotifier. Every approval is
// broadcast to server channels; only an admin's approval is personal news to
// the requester. Anything else — including an origin this build does not
// recognize — skips the personal delivery, so the default is silence.
func (n *RequestLifecycleNotifier) RequestApproved(
	ctx context.Context,
	req requests.Request,
	origin requests.ApprovalOrigin,
) {
	n.backend.PostServerChannelRequestEvent(ctx, ServerChannelEventRequestApproved, requestEventInfoFor(req))
	switch origin {
	case requests.ApprovalOriginAdmin:
		n.backend.dispatchRequestLifecycleDetached(ctx, req, DeliveryTypeRequestApproved)
	case requests.ApprovalOriginPolicy:
		// The policy is answering the requester's own submission, so a notice
		// about it is noise.
	default:
		n.logger.WarnContext(ctx, "unrecognized request approval origin; skipping requester notice",
			"request_id", req.ID, "origin", string(origin))
	}
}

// RequestDeclined implements requests.LifecycleNotifier.
func (n *RequestLifecycleNotifier) RequestDeclined(ctx context.Context, req requests.Request) {
	n.backend.PostServerChannelRequestEvent(ctx, ServerChannelEventRequestDeclined, requestEventInfoFor(req))
	n.backend.dispatchRequestLifecycleDetached(ctx, req, DeliveryTypeRequestDeclined)
}

// dispatchRequestLifecycleDetached creates the requester's delivery on its own
// goroutine, mirroring PostServerChannelRequestEvent: the lifecycle contract
// requires non-blocking dispatch, and the caller's context ends with its HTTP
// request.
func (s *System) dispatchRequestLifecycleDetached(ctx context.Context, req requests.Request, deliveryType string) {
	dispatchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), requestLifecycleDispatchTimeout)
	go func() {
		defer cancel()
		if err := s.dispatchRequestLifecycle(dispatchCtx, req, deliveryType); err != nil {
			s.logger.WarnContext(ctx, "request lifecycle delivery failed",
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
		RequestID:  req.ID,
		TMDBID:     req.TMDBID,
		MediaType:  string(req.MediaType),
		Title:      req.Title,
		PosterPath: req.PosterPath,
		Reason:     req.OutcomeReason,
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
