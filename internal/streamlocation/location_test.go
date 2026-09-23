package streamlocation

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/netaccess"
)

func TestIsRemote(t *testing.T) {
	tests := []struct {
		name     string
		ip       string
		provider string
		remote   bool
	}{
		{name: "public IPv4", ip: "203.0.113.7", remote: true},
		{name: "public IPv6", ip: "2001:db8::7", remote: true},
		{name: "private IPv4", ip: "192.168.1.7"},
		{name: "private IPv6", ip: "fd00::7"},
		{name: "loopback", ip: "127.0.0.1"},
		{name: "link local", ip: "169.254.2.7"},
		{name: "provider path", ip: "192.168.1.7", provider: "tailscale", remote: true},
		{name: "missing", remote: true},
		{name: "invalid", ip: "not-an-ip", remote: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wantLocation := Local
			if test.remote {
				wantLocation = Remote
			}
			if got := FromMetadata(test.ip, test.provider); got != wantLocation {
				t.Fatalf("FromMetadata() = %q, want %q", got, wantLocation)
			}
			ctx := clientip.SetContext(context.Background(), test.ip)
			if test.provider != "" {
				ctx = netaccess.WithPath(ctx, netaccess.Path{Provider: test.provider})
			}
			if got := IsRemote(ctx); got != test.remote {
				t.Fatalf("IsRemote() = %t, want %t", got, test.remote)
			}
			wantCap := 4_000
			if test.remote {
				wantCap = 2_000
			}
			if got := BitrateCap(ctx, 4_000, 2_000); got != wantCap {
				t.Fatalf("BitrateCap() = %d, want %d", got, wantCap)
			}
		})
	}
}
