package historyimport

import (
	"context"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/Silo-Server/silo-server/internal/netguard"
)

// MaxPlexConnectionCandidates bounds how many advertised Plex connections one
// run races. It is exported so the history-import capability document can
// report the real limit instead of restating it.
const MaxPlexConnectionCandidates = 8

// MaxPlexAdvertisedConnections bounds the list admission checks before it
// picks the MaxPlexConnectionCandidates a run races, counting plex_base_url.
// Some advertised addresses are dropped by the local network policy, so
// admission looks past the run's cap, but the list comes from a client or from
// plex.tv and the work of checking it stays bounded. The v2 schema bounds
// plex_base_urls at one less.
const MaxPlexAdvertisedConnections = 4 * MaxPlexConnectionCandidates

// plexBaseURLCandidates is the single normalization point for Plex base URLs:
// it trims, drops empties, dedupes, and keeps at most limit entries.
// Everything downstream expects an already-normalized slice.
func plexBaseURLCandidates(primary string, alternatives []string, limit int) []string {
	result := make([]string, 0, min(1+len(alternatives), limit))
	seen := make(map[string]struct{}, cap(result))
	appendCandidate := func(candidate string) {
		candidate = strings.TrimRight(strings.TrimSpace(candidate), "/")
		if candidate == "" || len(result) >= limit {
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

// plexSecureBaseURLCandidates is plexBaseURLCandidates with cleartext
// addresses dropped whenever the server advertised an HTTPS one. Every
// candidate is raced with the Plex token attached, so an http:// candidate
// would send the token in the clear even when an encrypted address answers.
//
// The result is not yet capped to MaxPlexConnectionCandidates:
// allowedPlexCandidates does that after the local network policy, so neither
// http:// entries nor addresses the account may not reach can use up the run's
// slots. The usual casualty would be the Plex relay: plex.tv advertises it
// last, and it is the connection that rescues a broken port forward.
//
// A server advertising nothing but cleartext keeps its list, as a single
// plex_base_url did before fallback existed.
func plexSecureBaseURLCandidates(primary string, alternatives []string) []string {
	secure := make([]string, 0, len(alternatives))
	for _, candidate := range alternatives {
		if isSecurePlexURL(candidate) {
			secure = append(secure, candidate)
		}
	}
	if !isSecurePlexURL(primary) {
		if len(secure) == 0 {
			return plexBaseURLCandidates(primary, alternatives, MaxPlexAdvertisedConnections)
		}
		primary = ""
	}
	return plexBaseURLCandidates(primary, secure, MaxPlexAdvertisedConnections)
}

func isSecurePlexURL(candidate string) bool {
	parsed, err := url.Parse(strings.TrimSpace(candidate))
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Scheme, "https")
}

// plexSessionCandidates is the address list a session-backed Plex run may
// use, derived only from what the server itself stored for that session. The
// enqueue transaction recomputes it and refuses a credential naming anything
// outside it, so a caller cannot smuggle an address of its own into the run.
//
// ConnectionURLs carries every advertised connection, local ones included, so
// only the preferred remote address is promoted. LocalURL is appended for
// sessions persisted before ConnectionURLs existed: those decode with the field
// empty, and the stored local address is all a local-only server has left.
func plexSessionCandidates(server PlexServer) []string {
	alternatives := make([]string, 0, len(server.ConnectionURLs)+1)
	alternatives = append(alternatives, server.ConnectionURLs...)
	alternatives = append(alternatives, server.LocalURL)
	return plexSecureBaseURLCandidates(server.RemoteURL, alternatives)
}

// allowedPlexCandidates drops the candidates userID may not reach under the
// local network policy and keeps the first MaxPlexConnectionCandidates of the
// rest, in order. plex.tv advertises a server's LAN addresses alongside its
// public ones, so refusing the whole run over one private candidate would
// break the common case for an account without local network access. When
// every candidate is refused, the first refusal is returned so the user sees
// why.
func (s *Service) allowedPlexCandidates(ctx context.Context, userID int, candidates []string) ([]string, error) {
	ctx = s.localNetwork.Context(ctx, userID)
	allowPrivate := netguard.PrivateAccess(ctx)
	refusals := make([]error, len(candidates))
	var wg sync.WaitGroup
	for i, candidate := range candidates {
		wg.Go(func() { refusals[i] = netguard.CheckURL(ctx, candidate, allowPrivate) })
	}
	wg.Wait()
	allowed := make([]string, 0, len(candidates))
	for i, candidate := range candidates {
		if refusals[i] == nil {
			allowed = append(allowed, candidate)
		}
	}
	if len(allowed) == 0 && len(candidates) > 0 {
		return nil, refusals[0]
	}
	return allowed[:min(len(allowed), MaxPlexConnectionCandidates)], nil
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
