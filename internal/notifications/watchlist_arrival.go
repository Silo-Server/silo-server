package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"time"

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
	WaitingFor(ctx context.Context, q watchlist.Querier, contentIDs []string, arrivedSince time.Time) (map[string][]watchlist.Waiter, error)
}

// requestWaiters reports which profiles a title's open request will still
// tell it arrived. *requests.Repository implements it.
type requestWaiters interface {
	AwaitingFulfillment(ctx context.Context, tx pgx.Tx, mediaType requests.MediaType, tmdbIDs []int, profiles []requests.Follower) (map[requests.Follower]bool, error)
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
// request; a request.fulfilled already sent for the title also counts. Items
// created before arrivedSince count only through unpromoted entries.
func (w *FanoutWorker) captureWatchlistArrivals(ctx context.Context, tx pgx.Tx, recipients *recipientAccess, events []ReleaseEvent, arrivedSince time.Time) ([]watchlistArrival, error) {
	if w.watchlist == nil {
		return nil, nil
	}
	contentIDs := arrivalContentIDs(events)
	waiting, err := w.watchlist.WaitingFor(ctx, tx, contentIDs, arrivedSince)
	if err != nil || len(waiting) == 0 {
		return nil, err
	}
	var arrivals []watchlistArrival
	for _, contentID := range contentIDs {
		// Two watchlist entries can match one item; a profile hears once.
		var waiters []watchlist.Waiter
		var keys []profileKey
		for _, waiter := range waiting[contentID] {
			key := waiterKey(waiter)
			if slices.Contains(keys, key) {
				continue
			}
			keys = append(keys, key)
			waiters = append(waiters, waiter)
		}
		if len(waiters) == 0 {
			continue
		}
		prefs, err := w.preferences.GetMany(ctx, tx, keys)
		if err != nil {
			return nil, err
		}
		told, err := w.titleToldElsewhere(ctx, tx, waiters)
		if err != nil {
			return nil, err
		}
		for _, waiter := range waiters {
			key := waiterKey(waiter)
			if !prefs[key].Enabled || told[key] {
				continue
			}
			allowed, err := recipients.canOpen(ctx, tx, waiter.UserID, waiter.ProfileID, contentID, 0)
			if err != nil {
				return nil, err
			}
			if allowed {
				arrivals = append(arrivals, watchlistArrival{contentID: contentID, waiter: waiter})
			}
		}
	}
	return arrivals, nil
}

func waiterKey(waiter watchlist.Waiter) profileKey {
	return profileKey{waiter.UserID, waiter.ProfileID}
}

// titleToldElsewhere reports which of one item's waiters a request of its
// title already told it arrived, or still will. The waiters share the item's
// media type; any TMDB ID one of them holds names the title.
func (w *FanoutWorker) titleToldElsewhere(ctx context.Context, tx pgx.Tx, waiters []watchlist.Waiter) (map[profileKey]bool, error) {
	mediaType := waiters[0].MediaType
	keys := make([]profileKey, len(waiters))
	var tmdbIDs []int
	for i, waiter := range waiters {
		keys[i] = waiterKey(waiter)
		for _, id := range waiter.TMDBIDs {
			if !slices.Contains(tmdbIDs, id) {
				tmdbIDs = append(tmdbIDs, id)
			}
		}
	}
	told, err := w.deliveries.TitleFulfilledFor(ctx, tx, keys, mediaType, tmdbIDs)
	if err != nil || w.requests == nil {
		return told, err
	}
	var followers []requests.Follower
	for _, key := range keys {
		if !told[key] {
			followers = append(followers, requests.Follower{UserID: key.userID, ProfileID: key.profileID})
		}
	}
	awaiting, err := w.requests.AwaitingFulfillment(ctx, tx, requests.MediaType(mediaType), tmdbIDs, followers)
	if err != nil {
		return nil, err
	}
	for follower := range awaiting {
		told[profileKey{follower.UserID, follower.ProfileID}] = true
	}
	return told, nil
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
		title := TitleFlags{MediaType: arrival.waiter.MediaType}
		if len(arrival.waiter.TMDBIDs) > 0 {
			title.TMDBID = arrival.waiter.TMDBIDs[0]
		}
		flags, _ := json.Marshal(title)
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

// TitleFulfilledFor reports which of the account profiles already have a
// request.fulfilled delivery for the title, under any TMDB ID it has held.
func (r *DeliveryRepository) TitleFulfilledFor(ctx context.Context, tx pgx.Tx, keys []profileKey, mediaType string, tmdbIDs []int) (map[profileKey]bool, error) {
	told := make(map[profileKey]bool)
	if len(keys) == 0 || len(tmdbIDs) == 0 {
		return told, nil
	}
	userIDs := make([]int, len(keys))
	profileIDs := make([]string, len(keys))
	for i, key := range keys {
		userIDs[i], profileIDs[i] = key.userID, key.profileID
	}
	ids := make([]string, len(tmdbIDs))
	for i, id := range tmdbIDs {
		ids[i] = strconv.Itoa(id)
	}
	rows, err := tx.Query(ctx, `
		SELECT p.user_id, p.profile_id
		FROM unnest($1::int[], $2::text[]) AS p(user_id, profile_id)
		WHERE EXISTS (
			SELECT 1 FROM notification_deliveries
			WHERE type = '`+DeliveryTypeRequestFulfilled+`'
			  AND user_id = p.user_id AND profile_id = p.profile_id
			  AND reason_flags->>'media_type' = $3 AND reason_flags->>'tmdb_id' = ANY($4))`,
		userIDs, profileIDs, mediaType, ids)
	if err != nil {
		return nil, fmt.Errorf("check title request.fulfilled deliveries: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key profileKey
		if err := rows.Scan(&key.userID, &key.profileID); err != nil {
			return nil, fmt.Errorf("scan title request.fulfilled delivery: %w", err)
		}
		told[key] = true
	}
	return told, rows.Err()
}
