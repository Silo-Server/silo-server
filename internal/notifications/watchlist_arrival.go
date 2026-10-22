package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/oklog/ulid/v2"

	"github.com/Silo-Server/silo-server/internal/requests"
	"github.com/Silo-Server/silo-server/internal/watchlist"
)

// DeliveryTypeWatchlistAvailable tells a profile that a title it put on its
// watchlist before the library had it has arrived. It goes out whether or not
// anyone requested the title, so a profile that turned off watchlist requests,
// or a server without requests, still hears about it. Its reason_flags carry
// TitleFlags; series_id names the arrived catalog item, as request.fulfilled
// does, and a partial unique index keeps one per account, profile and item
// (docs/architecture/notifications.md, "Watchlist arrivals").
const DeliveryTypeWatchlistAvailable = "watchlist.available"

// TitleFlags is the decoded reason_flags shape of a watchlist.available
// delivery: the watchlisted title, by its current TMDB ID.
type TitleFlags struct {
	TMDBID    int    `json:"tmdb_id"`
	MediaType string `json:"media_type"`
}

// watchlistWaiters finds the profiles whose watchlist waits for catalog items.
// *watchlist.Arrivals implements it.
type watchlistWaiters interface {
	WaitingFor(ctx context.Context, contentIDs []string) (map[string][]watchlist.Waiter, error)
}

// requestWaiters reports which profiles a title's open request will still
// tell it arrived. *requests.Repository implements it.
type requestWaiters interface {
	AwaitingFulfillment(ctx context.Context, mediaType requests.MediaType, tmdbIDs []int, profiles []requests.Follower) (map[requests.Follower]bool, error)
}

// SetWatchlistArrivals wires the watchlist arrival notice into fanout. Call
// it before Start; without it a watchlisted title arriving notifies nobody
// unless a request covers it.
func (s *System) SetWatchlistArrivals(waiters *watchlist.Arrivals, requestRepo *requests.Repository) {
	if s == nil || s.Fanout == nil || waiters == nil || requestRepo == nil {
		return
	}
	s.Fanout.watchlist = waiters
	s.Fanout.requests = requestRepo
}

// watchlistArrival is one profile to tell that a catalog item it watchlisted
// arrived.
type watchlistArrival struct {
	contentID string
	waiter    watchlist.Waiter
}

func (a watchlistArrival) key() profileKey { return profileKey{a.waiter.UserID, a.waiter.ProfileID} }

// arrivalContentIDs names the catalog item each event brings: the movie of a
// movie event, the series of an episode event. Other kinds have no watchlist.
// Order is kept and repeats dropped.
func arrivalContentIDs(events []ReleaseEvent) []string {
	seen := make(map[string]struct{}, len(events))
	ids := make([]string, 0, len(events))
	for _, event := range events {
		var id string
		switch normalizeEventKind(event.Kind) {
		case EventKindMovie:
			id = event.ItemID
		case EventKindEpisode:
			id = event.SeriesID
		}
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

// captureWatchlistArrivals selects the profiles to tell that an item the
// events bring arrived: their watchlist holds it, their notifications are on,
// they may open it, and no request of the title has told or will tell them.
// A profile that both requested and watchlisted a title hears once, from the
// request; a request.fulfilled already sent for the title also counts.
func (w *FanoutWorker) captureWatchlistArrivals(ctx context.Context, tx pgx.Tx, recipients *recipientAccess, events []ReleaseEvent) ([]watchlistArrival, error) {
	if w.watchlist == nil {
		return nil, nil
	}
	contentIDs := arrivalContentIDs(events)
	waiting, err := w.watchlist.WaitingFor(ctx, contentIDs)
	if err != nil || len(waiting) == 0 {
		return nil, err
	}
	var arrivals []watchlistArrival
	for _, contentID := range contentIDs {
		// Two watchlisted titles can match one item; a profile hears once.
		var candidates []watchlistArrival
		var keys []profileKey
		for _, waiter := range waiting[contentID] {
			arrival := watchlistArrival{contentID: contentID, waiter: waiter}
			if slices.Contains(keys, arrival.key()) {
				continue
			}
			keys = append(keys, arrival.key())
			candidates = append(candidates, arrival)
		}
		prefs, err := w.preferences.GetMany(ctx, tx, keys)
		if err != nil {
			return nil, err
		}
		for _, candidate := range candidates {
			if !prefs[candidate.key()].Enabled {
				continue
			}
			told, err := w.titleToldElsewhere(ctx, tx, candidate.waiter)
			if err != nil {
				return nil, err
			}
			if told {
				continue
			}
			allowed, err := recipients.canOpen(ctx, tx, candidate.waiter.UserID, candidate.waiter.ProfileID, contentID, 0)
			if err != nil {
				return nil, err
			}
			if allowed {
				arrivals = append(arrivals, candidate)
			}
		}
	}
	return arrivals, nil
}

// titleToldElsewhere reports whether a request of the waiter's title already
// told the profile it arrived, or still will.
func (w *FanoutWorker) titleToldElsewhere(ctx context.Context, tx pgx.Tx, waiter watchlist.Waiter) (bool, error) {
	told, err := w.deliveries.HasTitleFulfilled(ctx, tx, waiter.UserID, waiter.ProfileID, waiter.MediaType, waiter.TMDBIDs)
	if err != nil || told {
		return told, err
	}
	if w.requests == nil {
		return false, nil
	}
	follower := requests.Follower{UserID: waiter.UserID, ProfileID: waiter.ProfileID}
	awaiting, err := w.requests.AwaitingFulfillment(ctx, requests.MediaType(waiter.MediaType), waiter.TMDBIDs, []requests.Follower{follower})
	if err != nil {
		return false, err
	}
	return awaiting[follower], nil
}

// deliverWatchlistArrivals inserts one watchlist.available delivery per
// arrival and queues its channel attempts in the fanout transaction. The
// caller holds the recipients' inbox locks. It returns the IDs of the rows
// actually inserted; a profile already told about the item dedupes away.
func (w *FanoutWorker) deliverWatchlistArrivals(ctx context.Context, tx pgx.Tx, arrivals []watchlistArrival) ([]string, error) {
	if len(arrivals) == 0 {
		return nil, nil
	}
	deliveries := make([]Delivery, 0, len(arrivals))
	for _, arrival := range arrivals {
		flags, _ := json.Marshal(TitleFlags{TMDBID: arrival.waiter.TMDBIDs[0], MediaType: arrival.waiter.MediaType})
		contentID := arrival.contentID
		deliveries = append(deliveries, Delivery{
			ID:          ulid.Make().String(),
			UserID:      arrival.waiter.UserID,
			ProfileID:   arrival.waiter.ProfileID,
			SeriesID:    &contentID,
			Type:        DeliveryTypeWatchlistAvailable,
			ReasonFlags: flags,
		})
	}
	inserted, err := w.deliveries.BulkInsert(ctx, tx, deliveries)
	if err != nil || len(inserted) == 0 {
		return nil, err
	}
	// Webhooks carry it under their request switch: both say a wanted title
	// arrived.
	if err := w.enqueueWebhookAttempts(ctx, tx, inserted, func(_ InsertedDelivery, hook Webhook) bool {
		return hook.NotifyRequests
	}); err != nil {
		return nil, err
	}
	if err := w.enqueueWebPushOutbox(ctx, tx, inserted); err != nil {
		return nil, err
	}
	if err := w.enqueuePushOutbox(ctx, tx, inserted); err != nil {
		return nil, err
	}
	ids := make([]string, len(inserted))
	for i, row := range inserted {
		ids[i] = row.ID
	}
	return ids, nil
}

// dispatchInserted publishes rows the fanout transaction committed, reloading
// each with its display fields. Failures are tolerable: the durable rows
// appear on reconnect.
func (w *FanoutWorker) dispatchInserted(ctx context.Context, ids []string) {
	for _, id := range ids {
		row, err := w.deliveries.GetRowByID(ctx, id)
		if err == nil && row != nil {
			err = w.dispatcher.Dispatch(ctx, *row)
		}
		if err != nil {
			w.logger.WarnContext(ctx, "notification dispatch failed", "delivery_id", id, "error", err)
		}
	}
}

// HasTitleFulfilled reports whether the account's profile already has a
// request.fulfilled delivery for the title, under any TMDB ID it has held.
func (r *DeliveryRepository) HasTitleFulfilled(ctx context.Context, tx pgx.Tx, userID int, profileID, mediaType string, tmdbIDs []int) (bool, error) {
	ids := make([]string, len(tmdbIDs))
	for i, id := range tmdbIDs {
		ids[i] = strconv.Itoa(id)
	}
	var exists bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM notification_deliveries
			WHERE type = '`+DeliveryTypeRequestFulfilled+`'
			  AND user_id = $1 AND profile_id = $2
			  AND reason_flags->>'media_type' = $3 AND reason_flags->>'tmdb_id' = ANY($4))`,
		userID, profileID, mediaType, ids).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check title request.fulfilled delivery: %w", err)
	}
	return exists, nil
}
