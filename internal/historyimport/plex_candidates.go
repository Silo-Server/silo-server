package historyimport

import (
	"net/url"
	"slices"
	"strings"
)

// MaxPlexConnectionCandidates bounds how many advertised Plex connections one
// run races. It is exported so the history-import capability document can
// report the real limit instead of restating it.
const MaxPlexConnectionCandidates = 8

// plexBaseURLCandidates is the single normalization point for Plex base URLs:
// it trims, drops empties, dedupes, and caps the list. Everything downstream
// expects an already-normalized slice.
func plexBaseURLCandidates(primary string, alternatives []string) []string {
	result := make([]string, 0, min(1+len(alternatives), MaxPlexConnectionCandidates))
	seen := make(map[string]struct{}, cap(result))
	appendCandidate := func(candidate string) {
		candidate = strings.TrimRight(strings.TrimSpace(candidate), "/")
		if candidate == "" || len(result) >= MaxPlexConnectionCandidates {
			return
		}
		if _, exists := seen[candidate]; exists {
			return
		}
		seen[candidate] = struct{}{}
		result = append(result, candidate)
	}

	appendCandidate(primary)
	for _, candidate := range alternatives {
		appendCandidate(candidate)
	}
	return result
}

// plexOAuthBaseURLCandidates is plexBaseURLCandidates for a profile OAuth run,
// which gets the public HTTPS-only transport. Cleartext addresses are dropped
// before the cap is applied rather than after.
//
// Order matters more than it looks. A server advertising more connections than
// MaxPlexConnectionCandidates would otherwise spend the whole budget on http://
// entries the transport refuses and never reach a working https:// one. The
// usual casualty is the Plex relay: plex.tv advertises it last, and it is the
// connection that rescues a broken port forward.
//
// A server advertising nothing but cleartext keeps its original list. There is
// no usable address either way, and preserving it leaves the refusal where it
// already happens — at the transport, with the reason attached — instead of
// turning it into an empty candidate set here.
func plexOAuthBaseURLCandidates(primary string, alternatives []string) []string {
	secure := make([]string, 0, len(alternatives))
	for _, candidate := range alternatives {
		if isSecurePlexURL(candidate) {
			secure = append(secure, candidate)
		}
	}
	if isSecurePlexURL(primary) {
		return plexBaseURLCandidates(primary, secure)
	}
	if len(secure) > 0 {
		return plexBaseURLCandidates("", secure)
	}
	return plexBaseURLCandidates(primary, alternatives)
}

// isSecurePlexURL applies publicPlexTransport's scheme rule early enough to
// matter for the candidate cap.
func isSecurePlexURL(candidate string) bool {
	parsed, err := url.Parse(strings.TrimSpace(candidate))
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Scheme, plexSecureScheme)
}

// plexSessionCandidates is the exact address list a session-backed Plex run may
// use, derived only from what the server itself stored for that session. The
// enqueue transaction recomputes it and refuses a credential that does not
// match, so a caller cannot smuggle an address of its own into the list.
//
// ConnectionURLs carries every advertised connection, local ones included, so
// only the preferred remote address is promoted. LocalURL is appended for
// sessions persisted before ConnectionURLs existed: those decode with the field
// empty, and the stored local address is all a local-only server has left.
func plexSessionCandidates(server PlexServer) []string {
	alternatives := make([]string, 0, len(server.ConnectionURLs)+1)
	alternatives = append(alternatives, server.ConnectionURLs...)
	alternatives = append(alternatives, server.LocalURL)
	return plexOAuthBaseURLCandidates(server.RemoteURL, alternatives)
}

// plexServersEqual compares two stored session server lists. PlexServer stopped
// being comparable when it gained a slice field, so the session revalidation
// cannot use slices.Equal on it directly.
func plexServersEqual(a, b []PlexServer) bool {
	return slices.EqualFunc(a, b, func(x, y PlexServer) bool {
		return x.Name == y.Name &&
			x.ClientIdentifier == y.ClientIdentifier &&
			x.AccessToken == y.AccessToken &&
			x.RemoteURL == y.RemoteURL &&
			x.LocalURL == y.LocalURL &&
			x.Owned == y.Owned &&
			x.HasRemoteURL == y.HasRemoteURL &&
			x.HasLocalURL == y.HasLocalURL &&
			slices.Equal(x.ConnectionURLs, y.ConnectionURLs)
	})
}
