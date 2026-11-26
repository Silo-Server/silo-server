package directconn

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/darthShadow/shunpike/coordination/protocol"
	"github.com/darthShadow/shunpike/engine"
	"github.com/darthShadow/shunpike/engine/n4"

	"github.com/Silo-Server/silo-server/internal/lantls"
	"github.com/Silo-Server/silo-server/internal/netaccess"
)

func newN4TestEngine(t *testing.T) *engine.Engine {
	t.Helper()
	var seed [32]byte
	if _, err := rand.Read(seed[:]); err != nil {
		t.Fatal(err)
	}
	e, err := engine.New(t.Context(), engine.Config{PrivateKey: engine.NodePrivateFromBytes(seed), StaticNetwork: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := e.Close(); err != nil {
			t.Error(err)
		}
	})
	return e
}

func n4TestPeer(e *engine.Engine) engine.Peer {
	return engine.Peer{NodeKey: e.PublicKey(), DiscoKey: e.DiscoKey(), Endpoints: []netip.AddrPort{netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), e.LocalPort())}, Expires: time.Now().Add(time.Minute)}
}

func n4TestTransport(t *testing.T, client, server *engine.Engine, pin string) *n4.Transport {
	t.Helper()
	tr := &n4.Transport{Engine: client, Policy: n4.DefaultClientPolicy(), Resolve: func(host string) (protocol.NodeKey, string, bool) {
		return server.PublicKey(), pin, host == "peer.invalid"
	}}
	t.Cleanup(func() {
		if err := tr.Close(); err != nil {
			t.Error(err)
		}
	})
	return tr
}

func TestN4RequestReachesHandler(t *testing.T) {
	server, client := newN4TestEngine(t), newN4TestEngine(t)
	if err := server.SetPeers([]engine.Peer{n4TestPeer(client)}); err != nil {
		t.Fatal(err)
	}
	if err := client.SetPeers([]engine.Peer{n4TestPeer(server)}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for !server.Path(client.PublicKey()).Endpoint.IsValid() || !client.Path(server.PublicKey()).Endpoint.IsValid() {
		select {
		case <-ctx.Done():
			t.Fatal("direct discovery: ", ctx.Err())
		case <-tick.C:
		}
	}
	identity, err := lantls.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/probe" {
			t.Errorf("path = %q, want /probe", r.URL.Path)
		}
		path := netaccess.PathFromContext(r.Context())
		if path.Direct == nil || path.Direct.PeerKey != client.PublicKey().String() {
			t.Errorf("direct path = %+v, want client peer %s", path.Direct, client.PublicKey())
		}
		want := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), client.LocalPort()).String()
		if r.RemoteAddr != want {
			t.Errorf("RemoteAddr = %q, want %q", r.RemoteAddr, want)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	srv := NewHTTP3Server(server, identity.HTTP3Config(), n4.DefaultServerPolicy(), "api-test", handler)
	done := make(chan error, 1)
	go func() { done <- srv.Serve(server.PacketConn()) }()
	t.Cleanup(func() {
		if err := srv.Close(); err != nil {
			t.Error(err)
		}
		if err := <-done; err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Error(err)
		}
	})
	tr := n4TestTransport(t, client, server, identity.Pin())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://peer.invalid/probe", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := res.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusNoContent || calls.Load() != 1 {
		t.Fatalf("status=%d handler calls=%d, want 204/1", res.StatusCode, calls.Load())
	}
	assertN4NonPeerRefused(t, server, identity.Pin(), &calls)
}

func assertN4NonPeerRefused(t *testing.T, server *engine.Engine, pin string, calls *atomic.Int32) {
	t.Helper()
	stranger := newN4TestEngine(t)
	if err := stranger.SetPeers([]engine.Peer{n4TestPeer(server)}); err != nil {
		t.Fatal(err)
	}
	if server.IsPeer(stranger.PublicKey()) {
		t.Fatal("server unexpectedly holds stranger")
	}
	denied := n4TestTransport(t, stranger, server, pin)
	deniedCtx, stop := context.WithTimeout(t.Context(), time.Second)
	defer stop()
	req, err := http.NewRequestWithContext(deniedCtx, http.MethodGet, "https://peer.invalid/probe", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := denied.RoundTrip(req)
	if res != nil {
		if closeErr := res.Body.Close(); closeErr != nil {
			t.Error(closeErr)
		}
		t.Errorf("non-peer received response: %d", res.StatusCode)
	}
	if err == nil {
		t.Error("non-peer request succeeded")
	}
	if calls.Load() != 1 {
		t.Errorf("non-peer reached handler: calls=%d, want 1", calls.Load())
	}
}
