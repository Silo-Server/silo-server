package directconn

import (
	"context"
	"net/http"
	"net/netip"
	"strings"

	"github.com/darthShadow/shunpike/coordination/protocol"
	"github.com/darthShadow/shunpike/coordination/server"

	"github.com/Silo-Server/silo-server/internal/netaccess"
)

// observedPeer is what the engine knows about the peer of one request.
type observedPeer struct {
	Key        protocol.NodeKey
	Endpoint   netip.AddrPort
	LocalPort  uint16
	Generation uint64
}

// wrapDirect replaces proxy assertions with the admitted peer's observed path
// before the root handler resolves client IPs or validates ingress tokens. The
// device and its policy come from lookup, the peer cache, on every request, so a
// policy change reaches the wrapper with the cache's next refresh. A peer the
// cache does not hold gets 421 like a peer with no direct evidence.
func wrapDirect(hostID string, source func(context.Context) observedPeer, lookup func(context.Context, protocol.NodeKey) (server.PeerEntry, bool), next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for name := range r.Header {
			lower := strings.ToLower(name)
			if lower == "forwarded" || strings.HasPrefix(lower, "x-forwarded-") || lower == "x-real-ip" || strings.EqualFold(name, netaccess.IngressTokenHeader) || strings.EqualFold(name, netaccess.IngressPeerHeader) {
				delete(r.Header, name)
			}
		}
		observed := source(r.Context())
		peer, endpoint := observed.Key, observed.Endpoint
		if peer.IsZero() || !endpoint.IsValid() {
			http.Error(w, "direct path unavailable", http.StatusMisdirectedRequest)
			return
		}
		entry, held := lookup(r.Context(), peer)
		userID, ok := accountNumber(entry.Ref)
		if !held || !ok {
			http.Error(w, "direct path unavailable", http.StatusMisdirectedRequest)
			return
		}
		r.RemoteAddr = endpoint.String()
		path := netaccess.Path{Direct: &netaccess.DirectPath{HostID: hostID, PeerKey: peer.String(), DeviceID: string(entry.Ref.Device),
			UserID: userID, Class: "direct", Policy: policyOf(entry.Policy), LocalPort: observed.LocalPort, PathGeneration: observed.Generation}}
		next.ServeHTTP(w, r.WithContext(netaccess.WithPath(r.Context(), path)))
	})
}
