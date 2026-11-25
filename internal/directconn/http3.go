package directconn

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/netip"

	"github.com/darthShadow/shunpike/coordination/protocol"
	"github.com/darthShadow/shunpike/engine"
	"github.com/darthShadow/shunpike/engine/n4"
	"github.com/quic-go/quic-go/http3"
)

// NewHTTP3Server builds a fresh N4 server for e with the deployment's HTTP/3
// TLS identity and policy. Requests carry only the admitted peer's observed
// endpoint and direct path; missing direct evidence returns 421. The caller
// serves e.PacketConn() and shuts down the server before closing e.
func NewHTTP3Server(e *engine.Engine, tlsConfig *tls.Config, policy n4.Policy, hostID string, handler http.Handler) *http3.Server {
	source := func(ctx context.Context) (protocol.NodeKey, netip.AddrPort) {
		peer, ok := n4.PeerFromContext(ctx)
		if !ok {
			return protocol.NodeKey{}, netip.AddrPort{}
		}
		return peer, e.Path(peer).Endpoint
	}
	return n4.NewServer(e, tlsConfig, policy, wrapDirect(hostID, source, handler))
}
