package requests

import (
	"context"
	"log/slog"
)

// FulfillmentNotifier delivers the request.fulfilled notification once a
// completed request's media is confirmed present in the catalog. contentID is
// the matched catalog item id. Implementations must tolerate repeat calls for
// the same request (delivery creation is idempotent); returning nil means the
// request counts as handled and will not be retried.
type FulfillmentNotifier interface {
	// NotifyFulfilled tells the requester, and every profile in
	// req.Followers, that the title is available. It may run again for the
	// same request, so each recipient's delivery must be idempotent.
	NotifyFulfilled(ctx context.Context, req Request, contentID string) error
	// AnnounceFulfilled posts the server-wide announcement. It runs once per
	// request, after the request is stamped as notified, and is best-effort.
	AnnounceFulfilled(ctx context.Context, req Request)
}

// SetFulfillmentNotifier wires the notification system into the reconcile
// service. Optional; without it completed requests are never notified.
func (s *Service) SetFulfillmentNotifier(n FulfillmentNotifier) { s.notifier = n }

// ApprovalOrigin says who approved a request. Distinct from the ApprovalMode
// policy setting, which decides whether approval happens at all. Destinations
// treat anything other than ApprovalOriginAdmin as not worth telling the
// requester about, so the zero value is safe by default.
type ApprovalOrigin string

const (
	// ApprovalOriginUnspecified is the zero value; no approval path passes it
	// deliberately.
	ApprovalOriginUnspecified ApprovalOrigin = ""
	// ApprovalOriginAdmin is an administrator acting on a pending request.
	ApprovalOriginAdmin ApprovalOrigin = "admin"
	// ApprovalOriginPolicy is auto-approval answering a submission.
	ApprovalOriginPolicy ApprovalOrigin = "policy"
)

// LifecycleNotifier observes request lifecycle transitions (submitted,
// approved, declined) for broadcast destinations such as admin server
// channels. Implementations must be fast and non-blocking (dispatch async)
// and must never fail the transition: methods return nothing.
//
// RequestApproved carries an ApprovalOrigin because destinations treat the
// two kinds of approval differently.
//
// Fulfillment is deliberately not part of this interface — it stays on
// FulfillmentNotifier, whose presence-checked, idempotent flow runs on the
// reconcile service rather than the API service.
type LifecycleNotifier interface {
	RequestSubmitted(ctx context.Context, req Request)
	RequestApproved(ctx context.Context, req Request, origin ApprovalOrigin)
	RequestDeclined(ctx context.Context, req Request)
}

// SetLifecycleNotifier wires lifecycle observation into the API-facing
// service. Optional; without it transitions are not broadcast.
func (s *Service) SetLifecycleNotifier(n LifecycleNotifier) { s.lifecycle = n }

// notifyLifecycle resolves requester display identity and invokes one
// lifecycle hook. Best-effort by construction: the notifier cannot return an
// error and identity resolution failures just leave the name empty.
func (s *Service) notifyLifecycle(ctx context.Context, req Request, notify func(LifecycleNotifier, context.Context, Request)) {
	if s.lifecycle == nil {
		return
	}
	s.populateRequesterIdentity(ctx, &req)
	notify(s.lifecycle, ctx, req)
}

func (s *Service) notifyApproval(ctx context.Context, req Request, origin ApprovalOrigin) {
	s.notifyLifecycle(ctx, req, func(notifier LifecycleNotifier, ctx context.Context, req Request) {
		notifier.RequestApproved(ctx, req, origin)
	})
}

// notifyFulfilledLimit bounds one notification pass; the remainder lands on
// the next reconcile run.
const notifyFulfilledLimit = 100

// markNotifyChecked stamps a completed request the fulfilled pass checked
// without notifying, which rotates it behind the others.
func (s *Service) markNotifyChecked(ctx context.Context, id string) {
	if err := s.store.MarkReconciled(ctx, id); err != nil {
		slog.WarnContext(ctx, "request fulfill-notify: stamp check failed", "component", "requests",
			"request_id", id, "err", err)
	}
}

// notifyFulfilledPending notifies completed requests whose media has arrived
// in the catalog. Requests completed by an integration before the library
// scan imports the files stay pending (fulfilled_notified_at IS NULL) and are
// re-checked every run until presence confirms — the notification means
// "watchable in Silo", not "download finished". The delivery insert is
// idempotent (partial unique index per request), so the notify-then-stamp
// ordering can never double-send: a crash between the two retries into a
// dedupe no-op.
func (s *Service) notifyFulfilledPending(ctx context.Context) {
	if s.notifier == nil {
		return
	}
	candidates, err := s.store.ListFulfilledUnnotified(ctx, notifyFulfilledLimit)
	if err != nil {
		slog.WarnContext(ctx, "request fulfill-notify: list candidates failed", "component", "requests", "err", err)
		return
	}
	for _, req := range candidates {
		if ctx.Err() != nil {
			return
		}
		matches, err := s.lookupPresence(ctx, req.MediaType, []PresenceCandidate{requestPresenceCandidate(*req)})
		if err != nil {
			slog.WarnContext(ctx, "request fulfill-notify: presence lookup failed", "component", "requests",
				"request_id", req.ID, "tmdb_id", req.TMDBID, "err", err)
			s.markNotifyChecked(ctx, req.ID)
			continue
		}
		match := matches[req.TMDBID]
		fulfilled, _, err := s.requestFulfilled(ctx, *req, match)
		if err != nil {
			slog.WarnContext(ctx, "request fulfill-notify: season lookup failed", "component", "requests",
				"request_id", req.ID, "err", err)
			s.markNotifyChecked(ctx, req.ID)
			continue
		}
		if !fulfilled {
			// Not in the catalog yet (or not every requested season); retry
			// next run, after the requests not checked as recently.
			s.markNotifyChecked(ctx, req.ID)
			continue
		}
		followers, err := s.store.ListRequestFollowers(ctx, *req)
		if err != nil {
			slog.WarnContext(ctx, "request fulfill-notify: list followers failed", "component", "requests",
				"request_id", req.ID, "err", err)
			continue
		}
		req.Followers = followers
		if err := s.notifier.NotifyFulfilled(ctx, *req, match.ContentID); err != nil {
			slog.WarnContext(ctx, "request fulfill-notify: dispatch failed", "component", "requests",
				"request_id", req.ID, "err", err)
			continue
		}
		// The followers have been told. Only the listed rows are cleared,
		// leaving the follows made for other requests of the title. They are
		// cleared before the request is stamped, so a failed clear leaves it
		// unstamped and the next run retries it (the deliveries dedupe)
		// instead of leaving follows behind for a later request of the title.
		if err := s.store.ClearRequestFollowers(ctx, *req, followers); err != nil {
			slog.WarnContext(ctx, "request fulfill-notify: clear followers failed", "component", "requests",
				"request_id", req.ID, "err", err)
			continue
		}
		stamped, err := s.store.MarkFulfilledNotified(ctx, req.ID)
		if err != nil {
			slog.WarnContext(ctx, "request fulfill-notify: mark failed", "component", "requests",
				"request_id", req.ID, "err", err)
			continue
		}
		// The announcement has no per-recipient dedupe, so it goes out only
		// from the pass whose stamp took: a retry after a failed write above
		// does not repeat it.
		if stamped {
			s.notifier.AnnounceFulfilled(ctx, *req)
		}
	}
}
