package apiv2

import "net/http"

const (
	playbackMediaOperation     = "getPlaybackMedia"
	playbackMediaHeadOperation = "headPlaybackMedia"
	playbackManifestOperation  = "getPlaybackManifest"
)

// streamReferenceOperations are the delivery routes whose gate chain also
// accepts the plan's signed stream reference (`st`) in place of the account
// credential, so a receiver handed only the URL (a Google Cast device) can
// play the session. Subtitle fonts are left out: they are read by a client
// that renders ASS itself and sends its own credential.
var streamReferenceOperations = map[string]bool{
	playbackMediaOperation:     true,
	playbackMediaHeadOperation: true,
	playbackManifestOperation:  true,
	playbackSegmentOperation:   true,
	playbackSubtitleOperation:  true,
	playbackSubtitleHead:       true,
}

// streamReferenceGateChain swaps the account authentication at the head of a
// delivery route's chain for RequireStreamReferenceAuth, which falls back to
// it. Viewer access and the rest of the chain still run.
func streamReferenceGateChain(deps Dependencies, chain []func(http.Handler) http.Handler) []func(http.Handler) http.Handler {
	out := append([]func(http.Handler) http.Handler(nil), chain...)
	out[0] = deps.Auth.RequireStreamReferenceAuth
	return out
}
