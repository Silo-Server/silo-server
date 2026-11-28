package directconn

import (
	"context"
	"crypto/tls"
	"net/http"

	"github.com/darthShadow/shunpike/coordination/protocol"
	"github.com/darthShadow/shunpike/coordination/server"
	"github.com/darthShadow/shunpike/engine"
	"github.com/darthShadow/shunpike/engine/n4"
	"github.com/quic-go/quic-go/http3"
)

// NewHTTP3Server builds a fresh N4 server for e with the deployment's HTTP/3
// TLS identity and policy. Requests carry the admitted peer's observed endpoint,
// its device and policy from the peer cache, and the direct path; a missing
// piece of direct evidence returns 421. The caller serves e.PacketConn() and
// shuts down the server before closing e.
func (s *Service) NewHTTP3Server(e *engine.Engine, tlsConfig *tls.Config, policy n4.Policy, handler http.Handler) *http3.Server {
	coord, paths := s.coordination()
	lookup := func(context.Context, protocol.NodeKey) (server.PeerEntry, bool) { return server.PeerEntry{}, false }
	var observe func(protocol.NodeKey, engine.PathStatus) pathRecord
	if coord != nil {
		lookup, observe = coord.directory.Lookup, paths.observe
	}
	return newHTTP3Server(e, tlsConfig, policy, lookup, observe, handler)
}

// newHTTP3Server serves handler behind wrapDirect. observe, when set, sees each
// request's path status so the path record stays current.
func newHTTP3Server(e *engine.Engine, tlsConfig *tls.Config, policy n4.Policy, lookup func(context.Context, protocol.NodeKey) (server.PeerEntry, bool), observe func(protocol.NodeKey, engine.PathStatus) pathRecord, handler http.Handler) *http3.Server {
	source := func(ctx context.Context) observedPeer {
		peer, ok := n4.PeerFromContext(ctx)
		if !ok {
			return observedPeer{}
		}
		status := e.Path(peer)
		out := observedPeer{Key: peer, Endpoint: status.Endpoint, LocalPort: e.LocalPort()}
		if observe != nil {
			out.Generation = observe(peer, status).generation
		}
		return out
	}
	return n4.NewServer(e, tlsConfig, policy, wrapDirect(CapabilityHostID, source, lookup, handler))
}
