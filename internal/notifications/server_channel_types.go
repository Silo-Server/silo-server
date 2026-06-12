package notifications

import (
	"time"
)

// Server channel request lifecycle events. Fulfilled deliberately shares the
// request.fulfilled delivery-type string so payload consumers see one
// vocabulary across the per-profile and server-channel paths.
const (
	ServerChannelEventRequestSubmitted = "request.submitted"
	ServerChannelEventRequestApproved  = "request.approved"
	ServerChannelEventRequestDeclined  = "request.declined"
	ServerChannelEventRequestFulfilled = DeliveryTypeRequestFulfilled
)

// ServerChannelEventContentAdded is the generic-webhook event name for the
// grouped new-content digest posts.
const ServerChannelEventContentAdded = "content.added"

// ServerChannel is an admin-owned broadcast destination ("community
// channel"): a Discord or generic webhook fed straight from release_events by
// a per-channel watermark sweep plus best-effort request lifecycle posts. URL
// and signing secret are stored as enc:v1: envelopes and never leave the
// server.
type ServerChannel struct {
	ID                      string
	Name                    string
	Type                    string
	URLCiphertext           string
	URLHost                 string
	SigningSecretCiphertext *string
	Enabled                 bool
	NotifyNewMovies         bool
	NotifyNewEpisodes       bool
	NotifyRequestSubmitted  bool
	NotifyRequestApproved   bool
	NotifyRequestDeclined   bool
	NotifyRequestFulfilled  bool
	WatermarkCreatedAt      time.Time
	WatermarkID             string
	LastAttemptAt           *time.Time
	ConsecutiveFailures     int
	DisabledReason          *string
	LastSuccessAt           *time.Time
	LastFailureAt           *time.Time
	LastFailureStatus       *int
	LastFailureMessage      *string
	CreatedByUserID         int
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

// AAD strings bind ciphertexts to this row and column. The prefix must stay
// distinct from the profile-webhook namespace ("notification_webhook:") so
// envelopes can never decrypt across tables.
func serverChannelURLAAD(id string) string    { return "server_channel:" + id + ":url" }
func serverChannelSecretAAD(id string) string { return "server_channel:" + id + ":signing_secret" }

// WantsContentKind reports whether the channel's toggles include the given
// release event kind. Unknown kinds (added by future versions) are skipped:
// an old node must not announce content it cannot render.
func (c ServerChannel) WantsContentKind(kind string) bool {
	switch normalizeEventKind(kind) {
	case EventKindEpisode:
		return c.NotifyNewEpisodes
	case EventKindMovie:
		return c.NotifyNewMovies
	default:
		return false
	}
}

// WantsRequestEvent reports whether the channel's toggles include the given
// request lifecycle event.
func (c ServerChannel) WantsRequestEvent(event string) bool {
	switch event {
	case ServerChannelEventRequestSubmitted:
		return c.NotifyRequestSubmitted
	case ServerChannelEventRequestApproved:
		return c.NotifyRequestApproved
	case ServerChannelEventRequestDeclined:
		return c.NotifyRequestDeclined
	case ServerChannelEventRequestFulfilled:
		return c.NotifyRequestFulfilled
	default:
		return false
	}
}

// RequestEventInfo carries the request fields server-channel payloads render.
// It is a local shape so the payload layer does not import internal/requests.
type RequestEventInfo struct {
	RequestID     string
	TMDBID        int
	TVDBID        int
	IMDBID        string
	MediaType     string // "movie" | "series"
	Title         string
	Year          int
	Overview      string
	PosterPath    string // raw TMDB image path ("/abc.jpg") from discovery
	RequesterName string
	// RequesterUserID is the requesting login account; 0 for requests
	// without one. The sweep worker resolves it to the linked Discord
	// identity just before a Discord channel receives the event.
	RequesterUserID int
	// RequesterDiscordID is the requester's OAuth-linked Discord user id,
	// filled in by the worker only when the admin enabled requester
	// mentions. Empty renders the plain RequesterName with no mention.
	RequesterDiscordID string
}
