package notifications

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// genericWebhookBody is the canonical Silo webhook JSON
// (docs/architecture/notifications.md, "Generic payload and HMAC signing").
// The HMAC signature is computed over the literal bytes Silo sends;
// receivers verify against the literal bytes they received, so no
// canonicalization is required on either side. No server URL, no absolute
// artwork URLs, no library name.
type genericWebhookBody struct {
	Event      string                 `json:"event"`
	DeliveryID string                 `json:"delivery_id"`
	WebhookID  string                 `json:"webhook_id"`
	Timestamp  string                 `json:"timestamp"`
	Version    int                    `json:"version"`
	Test       bool                   `json:"test"`
	ProfileID  string                 `json:"profile_id"`
	LibraryID  *int                   `json:"library_id,omitempty"`
	Type       string                 `json:"type"`
	Reasons    *ReasonFlags           `json:"reason_flags,omitempty"`
	Series     *genericWebhookSeries  `json:"series,omitempty"`
	Episode    *genericWebhookEpisode `json:"episode,omitempty"`
	// Request is present for request.* deliveries. For request.fulfilled the
	// catalog item is in Series (movies included — the field carries the
	// matched item); approved/declined have no catalog item yet, so the
	// request's own title rides here.
	Request *genericWebhookRequest `json:"request,omitempty"`
	// Rating is present for rating.set deliveries.
	Rating *genericWebhookRating `json:"rating,omitempty"`
	// Watchlist is present for watchlist.available deliveries: the title the
	// profile watchlisted. The arrived catalog item is in Series, movies
	// included.
	Watchlist *genericWebhookWatchlist `json:"watchlist,omitempty"`
}

type genericWebhookWatchlist struct {
	TMDBID    int    `json:"tmdb_id,omitempty"`
	MediaType string `json:"media_type"`
}

type genericWebhookSeries struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type genericWebhookEpisode struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	SeasonNumber  *int   `json:"season_number,omitempty"`
	EpisodeNumber *int   `json:"episode_number,omitempty"`
}

type genericWebhookRequest struct {
	ID        string `json:"id"`
	TMDBID    int    `json:"tmdb_id,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	Title     string `json:"title,omitempty"`
	Year      int    `json:"year,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// genericWebhookRating carries the rating payload for rating.set deliveries.
type genericWebhookRating struct {
	Rating int    `json:"rating"`
	ItemID string `json:"item_id"`
}

// BuildGenericWebhookPayload renders a delivery as canonical Silo JSON. Pure
// function.
func BuildGenericWebhookPayload(row DeliveryRow, webhookID string, test bool) ([]byte, error) {
	createdAt := row.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	body := genericWebhookBody{
		Event:      EventNotificationCreated,
		DeliveryID: row.ID,
		WebhookID:  webhookID,
		Timestamp:  createdAt.UTC().Format(time.RFC3339),
		Version:    1,
		Test:       test,
		ProfileID:  row.ProfileID,
		LibraryID:  row.LibraryID,
		Type:       row.Type,
	}
	// C4: reason_flags carries reason booleans (favorite, watchlist,
	// continue_watching, next_up) that only apply to episode.available
	// deliveries. rating.set decodes into all-false booleans, so omit the
	// field for it; the rating itself rides in the rating block.
	if row.Type != DeliveryTypeRatingSet {
		flags := parseReasonFlags(row.ReasonFlags)
		body.Reasons = &flags
	}
	if row.SeriesID != nil {
		body.Series = &genericWebhookSeries{ID: *row.SeriesID, Title: row.SeriesTitle}
	}
	if row.EpisodeID != nil {
		body.Episode = &genericWebhookEpisode{
			ID:            *row.EpisodeID,
			Title:         row.EpisodeTitle,
			SeasonNumber:  row.SeasonNumber,
			EpisodeNumber: row.EpisodeNumber,
		}
	}
	switch row.Type {
	case DeliveryTypeRequestFulfilled, DeliveryTypeRequestApproved, DeliveryTypeRequestDeclined:
		flags := parseRequestFlags(row.ReasonFlags)
		body.Request = &genericWebhookRequest{
			ID:        flags.RequestID,
			TMDBID:    flags.TMDBID,
			MediaType: flags.MediaType,
			Title:     flags.Title,
			Year:      flags.Year,
			Reason:    flags.Reason,
		}
	case DeliveryTypeRatingSet:
		flags := parseRatingFlags(row.ReasonFlags)
		// item_id is the rated content itself: the episode for episode
		// ratings (so receivers can resolve/download the exact episode),
		// otherwise the movie/series content_id.
		itemID := ""
		if row.EpisodeID != nil {
			itemID = *row.EpisodeID
		} else if row.SeriesID != nil {
			itemID = *row.SeriesID
		}
		body.Rating = &genericWebhookRating{Rating: flags.Rating, ItemID: itemID}
	case DeliveryTypeWatchlistAvailable:
		var title TitleFlags
		_ = json.Unmarshal(row.ReasonFlags, &title)
		body.Watchlist = &genericWebhookWatchlist{TMDBID: title.TMDBID, MediaType: title.MediaType}
	}
	return json.Marshal(body)
}

// SignGenericWebhook computes the X-Vio-Signature header value for a body:
// "t=<epoch>,v1=<hex(hmac_sha256(secret, "<epoch>.<body>"))>", following
// Stripe's signing convention so receivers can reuse existing verifiers.
func SignGenericWebhook(secret string, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(strconv.AppendInt(nil, timestamp, 10))
	mac.Write([]byte{'.'})
	mac.Write(body)
	return fmt.Sprintf("t=%d,v1=%s", timestamp, hex.EncodeToString(mac.Sum(nil)))
}

// genericWebhookHeaders builds the delivery headers for a generic webhook
// POST. The timestamp participating in the HMAC is the Unix-epoch header
// value, not the body's RFC3339 timestamp.
func genericWebhookHeaders(webhookID, deliveryID, secret string, now time.Time, body []byte) map[string]string {
	timestamp := now.Unix()
	return map[string]string{
		"X-Vio-Event":       EventNotificationCreated,
		"X-Vio-Webhook-Id":  webhookID,
		"X-Vio-Delivery-Id": deliveryID,
		"X-Vio-Timestamp":   fmt.Sprintf("%d", timestamp),
		"X-Vio-Signature":   SignGenericWebhook(secret, timestamp, body),
	}
}
