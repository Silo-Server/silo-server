package nodepool

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/netaccess"
)

// A proxy is a regular media origin: a direct_only device never takes one,
// whatever transport the request asks for, and every other device keeps the
// proxy it had.
func TestMediaReachableVia(t *testing.T) {
	proxy := &Node{ID: 1, URL: "http://lan-1/"}
	for _, tc := range []struct {
		name   string
		direct DirectMedia
		want   bool
	}{
		{"regular request, automatic or not enrolled", DirectMedia{}, true},
		{"direct request, automatic", DirectMedia{Requested: true}, true},
		{"regular request, direct_only", DirectMedia{DirectOnly: true}, false},
		{"direct request, direct_only", DirectMedia{Requested: true, DirectOnly: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantURL := ""
			if tc.want {
				wantURL = proxy.ClientURLFor(netaccess.Path{})
			}
			if got := proxy.MediaURLFor(netaccess.Path{}, tc.direct); got != wantURL {
				t.Fatalf("MediaURLFor = %q, want %q", got, wantURL)
			}
			eligible := MediaReachableVia(netaccess.Path{}, tc.direct, nil)
			if got := eligible == nil || eligible(proxy); got != tc.want {
				t.Fatalf("MediaReachableVia accepts the proxy = %v, want %v", got, tc.want)
			}
		})
	}
}

// A device that is not direct_only gets exactly what ClientReachableVia gave
// it, provider paths and the base predicate included.
func TestMediaReachableViaKeepsTheClientRules(t *testing.T) {
	reachable := &Node{ID: 1, URL: "http://lan-1", NetworkAccess: netaccess.NodeNetworkAccess{"tailscale": {State: netaccess.StateConnected, Origin: "https://p1.ts.net"}}}
	lanOnly := &Node{ID: 2, URL: "http://lan-2"}
	tailnet := netaccess.Path{Provider: "tailscale"}
	direct := DirectMedia{Requested: true}

	if MediaReachableVia(netaccess.Path{}, direct, nil) != nil {
		t.Fatal("default path with no base predicate must stay nil so planners accept any healthy proxy")
	}
	base := func(n *Node) bool { return n.ID == 1 }
	wrapped := MediaReachableVia(tailnet, direct, base)
	if !wrapped(reachable) || wrapped(lanOnly) || wrapped(nil) {
		t.Fatal("provider path must accept only the connected proxy that passes the base predicate")
	}
	if got := reachable.MediaURLFor(tailnet, direct); got != "https://p1.ts.net" {
		t.Fatalf("MediaURLFor on a provider path = %q", got)
	}
}
