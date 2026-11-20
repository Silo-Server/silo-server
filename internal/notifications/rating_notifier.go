package notifications

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/oklog/ulid/v2"
)

// The resolver repos are narrowed to lookup-only interfaces so the notifier
// can be exercised with stubs; the concrete catalog repositories satisfy them.
type (
	episodeLookuper interface {
		GetByID(ctx context.Context, contentID string) (*models.Episode, error)
	}
	seasonLookuper interface {
		GetByID(ctx context.Context, contentID string) (*models.Season, error)
	}
	itemLookuper interface {
		GetByID(ctx context.Context, contentID string) (*models.MediaItem, error)
	}
)

// RatingFlags is the decoded reason_flags shape for rating.set deliveries.
type RatingFlags struct {
	Rating int `json:"rating"`
}

// parseRatingFlags decodes a rating.set delivery's reason_flags.
func parseRatingFlags(raw []byte) RatingFlags {
	var flags RatingFlags
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &flags)
	}
	return flags
}

// RatingNotifier adapts the notification system to the ratings API: when a
// profile rates an item, it posts one durable rating.set delivery gated on
// the profile's master toggle. Webhooks whose notify_ratings flag is set
// receive the payload; web push and Apple push receive it too when enabled.
type RatingNotifier struct {
	system      *System
	itemRepo    itemLookuper
	episodeRepo episodeLookuper
	seasonRepo  seasonLookuper
}

// NewRatingNotifier creates the adapter. Returns nil when there is no
// notification system. itemRepo is required; episode/season repos are used
// to resolve episodes/seasons up to their parent series.
func NewRatingNotifier(
	system *System,
	itemRepo *catalog.ItemRepository,
	episodeRepo *catalog.EpisodeRepository,
	seasonRepo *catalog.SeasonRepository,
) *RatingNotifier {
	if system == nil || itemRepo == nil {
		return nil
	}
	return &RatingNotifier{
		system:      system,
		itemRepo:    itemRepo,
		episodeRepo: episodeRepo,
		seasonRepo:  seasonRepo,
	}
}

// NotifyRating posts one rating.set delivery for a rated item. contentID is
// the rated media item (movie, series, season, or episode); the rated item's
// parent series is resolved so the delivery row can join catalog metadata for
// outbound rendering. Best-effort and non-blocking: a delivery failure must
// never fail the rating write. When the profile has no rating-enabled
// subscriber (webhook with notify_ratings, web push device, or Apple/Android
// push device), no delivery row is written at all (C8), so a rating on an
// unsubscribed profile costs nothing but the pre-checks.
func (n *RatingNotifier) NotifyRating(ctx context.Context, userID int, profileID, contentID string, rating int) error {
	if n == nil || n.system == nil || contentID == "" {
		return nil
	}
	if userID <= 0 || profileID == "" {
		return nil
	}
	prefs, err := n.system.Preferences.Get(ctx, userID, profileID)
	if err != nil {
		return err
	}
	if !prefs.Enabled {
		return nil
	}
	// C2: skip delivery entirely when nothing would receive it, instead of
	// writing a durable delivery row that no channel targets. The channel
	// check must match what DispatchOperational's post-commit enqueuers
	// consider recipient-eligible.
	if !n.hasRatingSubscribers(ctx, userID, profileID) {
		return nil
	}

	seriesID, episodeID := n.resolveParent(ctx, contentID)

	flags, err := json.Marshal(RatingFlags{Rating: rating})
	if err != nil {
		return fmt.Errorf("marshal rating flags: %w", err)
	}
	delivery := Delivery{
		ID:          ulid.Make().String(),
		UserID:      userID,
		ProfileID:   profileID,
		SeriesID:    seriesID,
		EpisodeID:   episodeID,
		Type:        DeliveryTypeRatingSet,
		ReasonFlags: flags,
	}
	_, err = n.system.DispatchOperational(ctx, delivery, OperationalDispatch{
		WebhookFilter: func(hook Webhook) bool { return hook.NotifyRatings },
	})
	return err
}

// hasRatingSubscribers reports whether any delivery channel would actually
// carry a rating.set notice for this profile: a webhook with notify_ratings,
// a web-push subscription, or an admin-enabled push platform with a device.
// The checks mirror DispatchOperational's per-target gates; nil repos mean
// that channel is unconfigured (no at-rest cipher / no VAPID provisioner) and
// contribute no recipients.
func (n *RatingNotifier) hasRatingSubscribers(ctx context.Context, userID int, profileID string) bool {
	if n.system == nil {
		return false
	}
	// Check webhooks first — the primary rating delivery channel. The
	// webhook repo has a lightweight EXISTS query for this.
	if n.system.Settings.WebhooksEnabled(ctx) && n.system.webhookRepo != nil {
		if has, err := n.system.webhookRepo.HasRatingSubscribers(ctx, userID, profileID); err == nil && has {
			return true
		}
	}
	// Web push subscriptions also receive rating deliveries.
	if n.system.Settings.WebPushEnabled(ctx) && n.system.webPushRepo != nil {
		if subs, err := n.system.webPushRepo.ListByProfile(ctx, userID, profileID); err == nil && len(subs) > 0 {
			return true
		}
	}
	// Apple/Android push: a profile whose only rating consumers are native
	// devices must still produce a delivery, so the gate has to count them.
	// ListEnabledPushByProfiles needs a tx; run a short read-only one so this
	// check reuses the exact query DispatchOperational's post-commit enqueuer
	// runs (platform, provider, push_mode, enabled) instead of drifting from it.
	if n.system.pushDeviceRepo != nil && n.system.pool != nil {
		if platforms := n.system.Settings.EnabledPushPlatforms(ctx); len(platforms) > 0 {
			tx, err := n.system.pool.Begin(ctx)
			if err == nil {
				key := profileKey{userID: userID, profileID: profileID}
				devices, listErr := n.system.pushDeviceRepo.ListEnabledPushByProfiles(ctx, tx, []profileKey{key}, platforms)
				_ = tx.Rollback(ctx)
				if listErr == nil && len(devices[key]) > 0 {
					return true
				}
			}
		}
	}
	return false
}

// resolveParent maps a rated content ID to its parent series (and itself when
// the rated item is an episode or season). contentID may be a movie, series,
// season, or episode:
//   - movie / series → seriesID = contentID
//   - season → seriesID = parent series, episodeID = contentID (the rated
//     season itself, so webhook item_id is the season, not the series)
//   - episode → seriesID = parent series, episodeID = contentID
//   - unknown → seriesID = contentID (fallback, see C3), episodeID = nil
func (n *RatingNotifier) resolveParent(ctx context.Context, contentID string) (seriesID, episodeID *string) {
	if n.episodeRepo != nil {
		if ep, err := n.episodeRepo.GetByID(ctx, contentID); err == nil && ep != nil {
			sid := ep.SeriesID
			eid := ep.ContentID
			return &sid, &eid
		}
	}
	if n.seasonRepo != nil {
		if season, err := n.seasonRepo.GetByID(ctx, contentID); err == nil && season != nil {
			sid := season.SeriesID
			cid := contentID
			return &sid, &cid
		}
	}
	if item, err := n.itemRepo.GetByID(ctx, contentID); err == nil && item != nil {
		sid := item.ContentID
		return &sid, nil
	}
	// C3: none of the repos resolved contentID (unknown or already-deleted
	// catalog row), yet the rating itself was persisted. Fall back to using
	// contentID as the seriesID so the delivery's item_id is never empty and
	// outbound receivers still have the rated entity to act on.
	cid := contentID
	return &cid, nil
}
