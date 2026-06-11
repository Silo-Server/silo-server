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
	NotifyFulfilled(ctx context.Context, req Request, contentID string) error
}

// SetFulfillmentNotifier wires the notification system into the reconcile
// service. Optional; without it completed requests are never notified.
func (s *Service) SetFulfillmentNotifier(n FulfillmentNotifier) { s.notifier = n }

// notifyFulfilledLimit bounds one notification pass; the remainder lands on
// the next reconcile run.
const notifyFulfilledLimit = 100

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
		slog.WarnContext(ctx, "request fulfill-notify: list candidates failed", "err", err)
		return
	}
	for _, req := range candidates {
		if ctx.Err() != nil {
			return
		}
		matches, err := s.lookupPresence(ctx, req.MediaType, []PresenceCandidate{requestPresenceCandidate(*req)})
		if err != nil {
			slog.WarnContext(ctx, "request fulfill-notify: presence lookup failed",
				"request_id", req.ID, "tmdb_id", req.TMDBID, "err", err)
			continue
		}
		match := matches[req.TMDBID]
		if !match.Available {
			continue // not in the catalog yet; retry next run
		}
		if err := s.notifier.NotifyFulfilled(ctx, *req, match.ContentID); err != nil {
			slog.WarnContext(ctx, "request fulfill-notify: dispatch failed",
				"request_id", req.ID, "err", err)
			continue
		}
		if err := s.store.MarkFulfilledNotified(ctx, req.ID); err != nil {
			slog.WarnContext(ctx, "request fulfill-notify: mark failed",
				"request_id", req.ID, "err", err)
		}
	}
}
