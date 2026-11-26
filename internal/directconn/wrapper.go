package directconn

import (
	"context"
	"net/http"
	"net/netip"
	"strings"

	"github.com/darthShadow/shunpike/coordination/protocol"

	"github.com/Silo-Server/silo-server/internal/netaccess"
)

// wrapDirect replaces proxy assertions with the admitted peer's observed path
// before the root handler resolves client IPs or validates ingress tokens.
func wrapDirect(hostID string, source func(context.Context) (protocol.NodeKey, netip.AddrPort), next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for name := range r.Header {
			lower := strings.ToLower(name)
			if lower == "forwarded" || strings.HasPrefix(lower, "x-forwarded-") || lower == "x-real-ip" || strings.EqualFold(name, netaccess.IngressTokenHeader) || strings.EqualFold(name, netaccess.IngressPeerHeader) {
				delete(r.Header, name)
			}
		}
		peer, endpoint := source(r.Context())
		if peer.IsZero() || !endpoint.IsValid() {
			http.Error(w, "direct path unavailable", http.StatusMisdirectedRequest)
			return
		}
		r.RemoteAddr = endpoint.String()
		path := netaccess.Path{Direct: &netaccess.DirectPath{HostID: hostID, PeerKey: peer.String(), Class: "direct"}}
		next.ServeHTTP(w, r.WithContext(netaccess.WithPath(r.Context(), path)))
	})
}
