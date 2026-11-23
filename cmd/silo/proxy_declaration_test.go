package main

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/nodepool"
)

// An unnamed proxy is found by the watcher's fallback name so its declaration
// lands on the row the watcher binds it to, but not when it registers itself:
// then it must get its own row rather than another proxy's.
func TestProxyDeclarationIdentity(t *testing.T) {
	const url, fallback = "http://proxy-2:8080", "proxy"
	for _, tc := range []struct {
		name     string
		given    string
		register bool
		want     nodepool.ProxyIdentity
	}{
		{"named", "edge-1", true, nodepool.ProxyIdentity{URL: url, Name: "edge-1", NewRowName: "edge-1"}},
		{"unnamed", "", false, nodepool.ProxyIdentity{URL: url, Name: fallback}},
		{"unnamed and registering", "", true, nodepool.ProxyIdentity{URL: url}},
	} {
		if got := proxyDeclarationIdentity(url, tc.given, fallback, tc.register); got != tc.want {
			t.Errorf("%s: %+v, want %+v", tc.name, got, tc.want)
		}
	}
}
