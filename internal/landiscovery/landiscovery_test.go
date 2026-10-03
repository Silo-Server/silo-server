package landiscovery

import (
	"errors"
	"net"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestServiceConfig(t *testing.T) {
	cfg, err := serviceConfig(Config{Name: " Living Room ", ServerID: "6F1C2A9B-0D4E-4F7A-9C3B-2E1D0A5B7C8D", Port: 8080})
	if err != nil {
		t.Fatalf("serviceConfig: %v", err)
	}
	if cfg.Name != "Living Room" || cfg.Type != "_silo._tcp" || cfg.Port != 8080 {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.Host != "silo-6f1c2a9b" {
		t.Fatalf("host = %q, want silo-6f1c2a9b", cfg.Host)
	}
	if cfg.Text["v"] != "1" || cfg.Text["id"] != "6F1C2A9B-0D4E-4F7A-9C3B-2E1D0A5B7C8D" || len(cfg.Text) != 2 {
		t.Fatalf("unexpected TXT: %v", cfg.Text)
	}
}

func TestServiceConfigRejectsMissingIdentityOrPort(t *testing.T) {
	if _, err := serviceConfig(Config{Name: defaultInstanceName, Port: 8080}); err == nil {
		t.Fatal("expected an error without a server ID")
	}
	if _, err := serviceConfig(Config{Name: defaultInstanceName, ServerID: "abc", Port: 0}); err == nil {
		t.Fatal("expected an error without a port")
	}
}

func TestInstanceName(t *testing.T) {
	if got := instanceName("  "); got != defaultInstanceName {
		t.Fatalf("empty name = %q, want %q", got, defaultInstanceName)
	}
	long := strings.Repeat("é", 40) // 80 bytes
	got := instanceName(long)
	if len(got) > maxInstanceNameBytes || !utf8.ValidString(got) {
		t.Fatalf("long name = %q (%d bytes), want valid UTF-8 within %d bytes", got, len(got), maxInstanceNameBytes)
	}
}

func TestInstanceNameDropsEmptyParentheses(t *testing.T) {
	// dnssd's conflict-suffix parser panics on a name ending in "()".
	for in, want := range map[string]string{"Room ()": "Room", "Room()": "Room", "()": defaultInstanceName, " Den () () ": "Den", "Den (2)": "Den (2)"} {
		if got := instanceName(in); got != want {
			t.Errorf("instanceName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPortFromAddr(t *testing.T) {
	cases := []struct {
		addr    net.Addr
		want    int
		wantErr error
	}{
		{&net.TCPAddr{IP: net.IPv6zero, Port: 8080}, 8080, nil},
		{&net.TCPAddr{Port: 8080}, 8080, nil},
		{&net.TCPAddr{IP: net.ParseIP("192.168.1.10"), Port: 9000}, 0, ErrSingleAddress},
		{&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8080}, 0, ErrLoopbackOnly},
		{&net.TCPAddr{IP: net.ParseIP("::1"), Port: 8080}, 0, ErrLoopbackOnly},
	}
	for _, tc := range cases {
		got, err := PortFromAddr(tc.addr)
		if !errors.Is(err, tc.wantErr) || got != tc.want {
			t.Errorf("PortFromAddr(%v) = %d, %v; want %d, %v", tc.addr, got, err, tc.want, tc.wantErr)
		}
	}
	if _, err := PortFromAddr(&net.UnixAddr{Name: "/tmp/x"}); err == nil {
		t.Error("expected an error for a non-TCP address")
	}
}
