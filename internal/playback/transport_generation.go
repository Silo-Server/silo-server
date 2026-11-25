package playback

import (
	"net/url"
	"strings"
)

// TransportQueryParam carries the transport generation on an HLS stream URL a
// plan publishes. The playlist echoes the query into every segment URI, so each
// request a player derives from that playlist names the transport it belongs
// to. See docs/architecture/playback-protocol-v3.md §6, "Superseded transports".
const TransportQueryParam = "silo_transport"

// TransportSupersededCode answers a media request that names a transport the
// session's current plan no longer publishes. It is a 404, so a client that
// predates the code treats it as the missing segment it is: the player that
// asked is being replaced. ErrorHeader repeats the code, because the v2 problem
// mapping sees only the response status and headers.
const TransportSupersededCode = "playback_transport_superseded"

// TransportSupersededMessage is the human-readable detail sent with
// TransportSupersededCode.
const TransportSupersededMessage = "The playback plan that published this URL was superseded"

// ErrorHeader names a media error the response status alone does not
// distinguish.
const ErrorHeader = "X-Silo-Playback-Error"

// TransportMarker is the URL form of a transport generation: the generation
// without the "<sessionID>-" prefix the URL path already carries. Empty when
// there is no recorded generation.
func TransportMarker(sessionID, generation string) string {
	return strings.TrimPrefix(generation, sessionID+"-")
}

// TransportGenerationFromURL recovers the transport generation an HLS stream
// URL names, for a session that lost its recorded one (a session rebuilt after
// an API restart). Empty when the URL carries no marker.
func TransportGenerationFromURL(sessionID, rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	marker := parsed.Query().Get(TransportQueryParam)
	if marker == "" {
		return ""
	}
	return sessionID + "-" + marker
}

// TransportSuperseded reports whether a media request names a transport other
// than generation, the one the session's current plan published. A request
// without the marker, or a session without a recorded generation, is never
// superseded: older URLs and sessions keep the behavior they had.
func TransportSuperseded(sessionID, generation string, query url.Values) bool {
	requested := query.Get(TransportQueryParam)
	if requested == "" || generation == "" {
		return false
	}
	return requested != TransportMarker(sessionID, generation)
}
