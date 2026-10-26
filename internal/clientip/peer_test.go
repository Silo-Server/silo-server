package clientip

import (
	"context"
	"testing"
)

func TestPeerIPMatchesResolvedClientForms(t *testing.T) {
	for _, tc := range []struct {
		peer string
		want string
	}{
		{peer: "203.0.113.10:40000", want: "203.0.113.10"},
		{peer: "[2001:DB8::10]:40000", want: "2001:db8::10"},
		{peer: "[::ffff:192.0.2.1]:40000", want: "192.0.2.1"},
		{peer: "192.0.2.1", want: "192.0.2.1"},
		{peer: "[fe80::1%en0]:40000", want: ""},
		{peer: "@", want: ""},
		{peer: "", want: ""},
	} {
		if got := PeerIP(SetPeerContext(context.Background(), tc.peer)); got != tc.want {
			t.Errorf("PeerIP(%q) = %q, want %q", tc.peer, got, tc.want)
		}
	}
}
