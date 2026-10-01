package netguard

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		addr string
		want Class
	}{
		{"8.8.8.8", Public},
		{"2606:4700:4700::1111", Public},
		{"127.0.0.1", Private},
		{"::1", Private},
		{"10.1.2.3", Private},
		{"172.16.0.9", Private},
		{"192.168.1.10", Private},
		{"100.101.102.103", Private}, // Tailscale
		{"fd12:3456::1", Private},
		{"fec0::1", Private}, // site-local
		{"198.18.0.1", Private},
		{"64:ff9b::a00:1", Private},     // NAT64 of 10.0.0.1
		{"64:ff9b::808:808", Private},   // NAT64 is never public, even for 8.8.8.8
		{"64:ff9b::a9fe:a9fe", Blocked}, // NAT64 of 169.254.169.254
		{"64:ff9b::7f00:1", Private},    // NAT64 of 127.0.0.1
		{"64:ff9b:1::a9fe:a9fe", Blocked},
		{"64:ff9b:1:ffff::1", Blocked},
		{"::ffff:127.0.0.1", Private},
		{"::ffff:192.168.1.10", Private},
		{"169.254.169.254", Blocked},
		{"::ffff:169.254.169.254", Blocked},
		{"100.100.100.200", Blocked},
		{"fd00:ec2::254", Blocked},
		{"fe80::1%eth0", Blocked},
		{"0.0.0.0", Blocked},
		{"::", Blocked},
		{"224.0.0.1", Blocked},
		{"ff02::1", Blocked},
		{"255.255.255.255", Blocked},
		{"::127.0.0.1", Blocked}, // IPv4-compatible form
		{"::169.254.169.254", Blocked},
		{"::8.8.8.8", Blocked},
	}
	for _, tc := range cases {
		if got := Classify(netip.MustParseAddr(tc.addr)); got != tc.want {
			t.Errorf("Classify(%s) = %v, want %v", tc.addr, got, tc.want)
		}
	}
	if got := Classify(netip.Addr{}); got != Blocked {
		t.Errorf("Classify(zero) = %v, want Blocked", got)
	}
}

func TestCheckAddrPolicy(t *testing.T) {
	cases := []struct {
		addr         string
		allowPrivate bool
		want         error
	}{
		{"8.8.8.8", false, nil},
		{"8.8.8.8", true, nil},
		{"192.168.1.10", false, ErrPrivateDestination},
		{"192.168.1.10", true, nil},
		{"127.0.0.1", false, ErrPrivateDestination},
		{"127.0.0.1", true, nil},
		{"169.254.169.254", false, ErrBlockedDestination},
		{"169.254.169.254", true, ErrBlockedDestination},
		{"fd00:ec2::254", true, ErrBlockedDestination},
		{"64:ff9b::a9fe:a9fe", true, ErrBlockedDestination},
		{"64:ff9b::a00:1", false, ErrPrivateDestination},
		{"64:ff9b::a00:1", true, nil},
	}
	for _, tc := range cases {
		if err := CheckAddr(netip.MustParseAddr(tc.addr), tc.allowPrivate); !errors.Is(err, tc.want) {
			t.Errorf("CheckAddr(%s, %v) = %v, want %v", tc.addr, tc.allowPrivate, err, tc.want)
		}
	}
}

// A public address assigned to this machine answers the host itself, past a
// cloud firewall, so untrusted callers may not dial it. Loopback stands in for
// such an address: every host has it, and hostAddress sees it the same way.
func TestHostAddressMatchesThisMachine(t *testing.T) {
	if !hostAddress(netip.MustParseAddr("127.0.0.1")) {
		t.Fatal("loopback is assigned to this machine but was not recognized")
	}
	if hostAddress(netip.MustParseAddr("192.0.2.123")) {
		t.Fatal("a documentation address was reported as this machine's")
	}
}

func TestCheckURL(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		url          string
		allowPrivate bool
		want         error
	}{
		{"http://192.168.1.10:8096", false, ErrPrivateDestination},
		{"http://192.168.1.10:8096", true, nil},
		{"https://[::1]:8920/", false, ErrPrivateDestination},
		{"http://localhost:8096", false, ErrPrivateDestination},
		{"http://localhost:8096", true, nil},
		{"http://169.254.169.254/latest/meta-data/", true, ErrBlockedDestination},
		{"http://[::ffff:169.254.169.254]/", true, ErrBlockedDestination},
		{"http://8.8.8.8/", false, nil},
		// Malformed input is left to the request, which reports it.
		{"", false, nil},
		{"not a url", false, nil},
		{"http://%zz", false, nil},
	}
	for _, tc := range cases {
		if err := CheckURL(ctx, tc.url, tc.allowPrivate); !errors.Is(err, tc.want) {
			t.Errorf("CheckURL(%q, %v) = %v, want %v", tc.url, tc.allowPrivate, err, tc.want)
		}
	}
}

func TestClientRefusesPrivateDestinationWithoutTrust(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
	}))
	defer server.Close()
	client := NewClient(5 * time.Second)

	if err := get(context.Background(), client, server.URL); !errors.Is(err, ErrPrivateDestination) {
		t.Fatalf("untrusted request error = %v, want ErrPrivateDestination", err)
	}
	if hits.Load() != 0 {
		t.Fatal("untrusted request reached the loopback server")
	}

	if err := get(WithPrivateAccess(context.Background()), client, server.URL); err != nil {
		t.Fatalf("trusted request: %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("trusted request hits = %d, want 1", hits.Load())
	}
}

func TestClientChecksEveryRedirectHop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer server.Close()

	err := get(WithPrivateAccess(context.Background()), NewClient(5*time.Second), server.URL)
	if !errors.Is(err, ErrBlockedDestination) {
		t.Fatalf("redirect to metadata error = %v, want ErrBlockedDestination", err)
	}
}

// A trusted request leaves an idle keep-alive connection behind; an untrusted
// request to the same address must dial again, and so be refused.
func TestUntrustedRequestDoesNotReuseTrustedConnection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	client := NewClient(5 * time.Second)

	if err := get(WithPrivateAccess(context.Background()), client, server.URL); err != nil {
		t.Fatalf("trusted request: %v", err)
	}

	if err := get(context.Background(), client, server.URL); !errors.Is(err, ErrPrivateDestination) {
		t.Fatalf("untrusted request after trusted one = %v, want ErrPrivateDestination", err)
	}
}

func TestPrivateAccessDefaultsOff(t *testing.T) {
	if PrivateAccess(context.Background()) {
		t.Fatal("a plain context must not carry private access")
	}
	if !PrivateAccess(WithPrivateAccess(context.Background())) {
		t.Fatal("WithPrivateAccess was not recorded")
	}
}

// get sends a GET and drains and closes the response, which returns the
// connection to its pool for reuse.
func get(ctx context.Context, client *http.Client, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.Body.Close()
}
