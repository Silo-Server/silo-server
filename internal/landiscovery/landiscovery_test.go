package landiscovery

import (
	"errors"
	"net"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestInstanceName(t *testing.T) {
	if got := instanceName("  Living Room  "); got != "Living Room" {
		t.Fatalf("instanceName = %q", got)
	}
	long := strings.Repeat("é", 40) // 80 bytes
	got := instanceName(long)
	if len(got) > maxInstanceNameBytes || !utf8.ValidString(got) {
		t.Fatalf("long name = %q (%d bytes), want valid UTF-8 within %d bytes", got, len(got), maxInstanceNameBytes)
	}
	if len(got+" (9999)") > 63 {
		t.Fatalf("no room for a conflict suffix: %d bytes", len(got))
	}
}

func TestHostLabel(t *testing.T) {
	if got := hostLabel("6F1C2A9B-0D4E-4F7A-9C3B-2E1D0A5B7C8D"); got != "silo-6f1c2a9b" {
		t.Fatalf("hostLabel = %q", got)
	}
	a, b := newHostLabel("6f1c2a9b-0d4e"), newHostLabel("6f1c2a9b-0d4e")
	if !strings.HasPrefix(a, "silo-6f1c2a9b-") || a == b {
		t.Fatalf("process host labels %q, %q must share the deployment prefix and differ", a, b)
	}
}

func TestListener(t *testing.T) {
	cases := []struct {
		addr       net.Addr
		port       int
		ipv4, ipv6 bool
		err        error
	}{
		{&net.TCPAddr{IP: net.IPv6unspecified, Port: 8080}, 8080, true, true, nil},
		{&net.TCPAddr{Port: 8080}, 8080, true, true, nil},
		{&net.TCPAddr{IP: net.IPv4zero, Port: 8080}, 8080, true, false, nil},
		{&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8080}, 0, false, false, ErrLoopbackOnly},
		{&net.TCPAddr{IP: net.ParseIP("::1"), Port: 8080}, 0, false, false, ErrLoopbackOnly},
		{&net.TCPAddr{IP: net.ParseIP("192.168.1.10"), Port: 9000}, 0, false, false, ErrSingleAddress},
	}
	for _, tc := range cases {
		port, v4, v6, err := Listener(tc.addr)
		if !errors.Is(err, tc.err) || port != tc.port || v4 != tc.ipv4 || v6 != tc.ipv6 {
			t.Errorf("Listener(%v) = %d %v %v %v; want %d %v %v %v", tc.addr, port, v4, v6, err, tc.port, tc.ipv4, tc.ipv6, tc.err)
		}
	}
	if _, _, _, err := Listener(&net.UnixAddr{Name: "/tmp/x"}); err == nil {
		t.Error("expected an error for a non-TCP address")
	}
}
