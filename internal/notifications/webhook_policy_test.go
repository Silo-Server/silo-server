package notifications

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/netguard"
)

// Policy-only fixture check. IP literals avoid resolution; no delivery or dial occurs.
func TestWebhookPrivatePolicyLiteral(t *testing.T) {
	for _, tc := range []struct {
		name, host string
		allowed    bool
	}{
		{"private-ipv4", "127.0.0.1", true},
		{"private-ipv6", "[::1]", true},
		{"blocked-unspecified", "0.0.0.0", false},
		{"blocked-multicast", "[ff02::1]", false},
		{"blocked-reserved", "240.0.0.1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidateWebhookURL(fmt.Sprintf("https://%s/hook", tc.host), true)
			if (err == nil) != tc.allowed {
				t.Errorf("accepted = %v, want %v", err == nil, tc.allowed)
			}
		})
	}
}

// All addresses below are inert fixtures: Control is called directly, without
// DialContext, and hostname resolution is injected. No outbound connection occurs.
func TestWebhookDestinationPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, address string
		class         netguard.Class
	}{
		{"public-v4", "8.8.8.8", netguard.Public},
		{"public-v6", "2606:4700::1111", netguard.Public},
		{"loopback", "127.0.0.1", netguard.Private},
		{"lan", "192.168.1.10", netguard.Private},
		{"ula", "fd12:3456::1", netguard.Private},
		{"zoned-ula", "fd12:3456::1%fixture", netguard.Private},
		{"mapped-private", "::ffff:127.0.0.1", netguard.Private},
		{"nat64-private", "64:ff9b::a00:1", netguard.Private},
		{"unspecified-v4", "0.0.0.0", netguard.Blocked},
		{"unspecified-v6", "::", netguard.Blocked},
		{"link-local-v4", "169.254.1.1", netguard.Blocked},
		{"zoned-link-local", "fe80::1%fixture", netguard.Blocked},
		{"metadata-v4", "100.100.100.200", netguard.Blocked},
		{"metadata-v6", "fd00:ec2::254", netguard.Blocked},
		{"multicast-v4", "224.0.0.1", netguard.Blocked},
		{"multicast-v6", "ff02::1", netguard.Blocked},
		{"reserved", "240.0.0.1", netguard.Blocked},
		{"mapped-blocked", "::ffff:169.254.1.1", netguard.Blocked},
		{"nat64-blocked", "64:ff9b::a9fe:101", netguard.Blocked},
		{"local-nat64", "64:ff9b:1::1", netguard.Blocked},
		{"compatible-v4", "::7f00:1", netguard.Blocked},
	} {
		for _, allowPrivate := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/private=%v", tc.name, allowPrivate), func(t *testing.T) {
				addr := netip.MustParseAddr(tc.address)
				if netguard.Classify(addr) != tc.class {
					t.Fatal("fixture class changed")
				}
				want := tc.class == netguard.Public || (allowPrivate && tc.class == netguard.Private)
				u := &url.URL{Scheme: schemeHTTPS, Host: net.JoinHostPort(tc.address, "443"), Path: "/hook"}
				_, err := ValidateWebhookURL(u.String(), allowPrivate)
				if (err == nil) != want {
					t.Errorf("registration accepted = %v, want %v", err == nil, want)
				}
				dialer := newNotificationDialer(allowPrivate)
				err = dialer.Control("tcp", net.JoinHostPort(tc.address, "443"), nil)
				if (err == nil) != want {
					t.Errorf("connect accepted = %v, want %v", err == nil, want)
				}
				if !want && (!errors.Is(err, errPrivateDestination) || classifyWebhookError(err) != "destination resolves to a private network") {
					t.Errorf("refusal lost existing error classification: %v", err)
				}
			})
		}
	}
	if webhookIPAllowed(netip.Addr{}, true) {
		t.Fatal("invalid address accepted")
	}
}

func TestWebhookResolvedDestinationPolicy(t *testing.T) {
	for _, tc := range []struct {
		name                string
		addresses           []netip.Addr
		err                 error
		publicOK, privateOK bool
	}{
		{"public", []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil, true, true},
		{"private", []netip.Addr{netip.MustParseAddr("192.168.1.10")}, nil, false, true},
		{"mixed-private", []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("::1")}, nil, false, true},
		{"mixed-blocked", []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("ff02::1")}, nil, false, false},
		{"invalid", []netip.Addr{{}}, nil, false, false},
		{"empty", nil, nil, false, false},
		{"lookup-error", nil, errors.New("synthetic lookup failure"), false, false},
		{"lookup-deadline", nil, context.DeadlineExceeded, false, false},
	} {
		for _, allowPrivate := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/private=%v", tc.name, allowPrivate), func(t *testing.T) {
				calls := 0
				lookup := func(ctx context.Context, network, host string) ([]netip.Addr, error) {
					calls++
					if network != "ip" || host != "sink.example" {
						t.Fatalf("unexpected lookup: %s %s", network, host)
					}
					deadline, ok := ctx.Deadline()
					if remaining := time.Until(deadline); !ok || remaining <= 0 || remaining > webhookLookupTimeout {
						t.Fatal("lookup lacks bounded deadline")
					}
					return tc.addresses, tc.err
				}
				host, err := validateWebhookURL("https://sink.example/hook", allowPrivate, lookup)
				want := tc.publicOK || (allowPrivate && tc.privateOK)
				if (err == nil) != want || calls != 1 || (want && host != "sink.example") {
					t.Fatalf("accepted=%v, host=%q, calls=%d; want allowed=%v", err == nil, host, calls, want)
				}
			})
		}
	}
	for _, raw := range []string{"http://sink.example/hook", "https://user:pass@sink.example/hook", "https:///hook"} {
		for _, allowPrivate := range []bool{false, true} {
			_, err := validateWebhookURL(raw, allowPrivate, func(context.Context, string, string) ([]netip.Addr, error) {
				t.Fatal("invalid URL reached resolver")
				return nil, nil
			})
			if err == nil {
				t.Fatal("invalid URL accepted")
			}
		}
	}
}

func TestWebhookDialPolicyFixtures(t *testing.T) {
	if err := newNotificationDialer(false).Control("tcp", "8.8.8.8:443", nil); err != nil {
		t.Fatalf("default connect guard refused public fixture: %v", err)
	}
	if err := newNotificationDialer(true).Control("tcp", net.JoinHostPort("192.168.1.10", "443"), nil); err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{"missing-port", "hostname.example:443", "[invalid]:443", "192.168.1.10:443"} {
		if err := newNotificationDialer(false).Control("tcp", address, nil); err == nil {
			t.Errorf("default connect guard accepted %q", address)
		}
	}
}

// trustTestServer makes both of client's connection pools trust an httptest
// TLS server's certificate.
func trustTestServer(t *testing.T, client *http.Client, server *httptest.Server) {
	t.Helper()
	rootCAs := server.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	transport := client.Transport.(*notificationTransport)
	transport.public.TLSClientConfig = &tls.Config{RootCAs: rootCAs}
	transport.private.TLSClientConfig = &tls.Config{RootCAs: rootCAs}
}

// The production client must refuse through its own dialer, including on a
// redirect hop. Every destination is loopback or 0.0.0.0 (which Linux dials as
// loopback), so nothing leaves the machine; the server certificate does not
// cover 0.0.0.0, so a dial the guard misses surfaces as a TLS failure instead.
func TestWebhookClientDialPolicy(t *testing.T) {
	var finished atomic.Int32
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
			http.Redirect(w, r, "https://"+net.JoinHostPort("0.0.0.0", port)+"/finish", http.StatusTemporaryRedirect)
			return
		}
		finished.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	for _, tc := range []struct {
		name, path   string
		allowPrivate bool
		ok           bool
		message      string
	}{
		{"private-refused-by-default", "/finish", false, false, "destination resolves to a private network"},
		{"private-allowed", "/finish", true, true, ""},
		{"blocked-redirect-refused-in-private-mode", "/start", true, false, "destination resolves to a private network"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			finished.Store(0)
			client := newWebhookHTTPClient(func() bool { return tc.allowPrivate })
			trustTestServer(t, client, server)
			result := sendWebhook(t.Context(), client, server.URL+tc.path, []byte("{}"), nil)
			if result.OK != tc.ok || result.Message != tc.message {
				t.Fatalf("OK=%v message=%q, want OK=%v message=%q", result.OK, result.Message, tc.ok, tc.message)
			}
			if want := int32(map[bool]int{true: 1, false: 0}[tc.ok]); finished.Load() != want {
				t.Fatalf("destination reached %d times, want %d", finished.Load(), want)
			}
		})
	}
}

func TestWebhookTransportGuards(t *testing.T) {
	client := newWebhookHTTPClient(nil)
	pools := client.Transport.(*notificationTransport)
	for _, transport := range []*http.Transport{pools.public, pools.private} {
		if client.Timeout != webhookRequestTimeout || transport.ResponseHeaderTimeout != webhookRequestTimeout ||
			transport.TLSHandshakeTimeout != 5*time.Second || transport.Proxy != nil || transport.DialContext == nil ||
			(transport.TLSClientConfig != nil && transport.TLSClientConfig.InsecureSkipVerify) {
			t.Fatal("delivery transport protections changed")
		}
	}
	if newNotificationDialer(false).Timeout != 5*time.Second {
		t.Fatal("dial timeout changed")
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://redirect.example/hook", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CheckRedirect(req, make([]*http.Request, webhookMaxRedirects)); err == nil {
		t.Fatal("redirect cap not enforced")
	}
	req.URL.Scheme = "http"
	if err := client.CheckRedirect(req, nil); err == nil {
		t.Fatal("HTTPS downgrade accepted")
	}
}

// Turning private destinations off must also stop deliveries over keep-alive
// connections the client opened while they were on (#2794).
func TestWebhookClientRefusesPooledPrivateConnectionAfterSettingOff(t *testing.T) {
	var reached atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	var allowPrivate atomic.Bool
	allowPrivate.Store(true)
	client := newWebhookHTTPClient(allowPrivate.Load)
	trustTestServer(t, client, server)

	if result := sendWebhook(t.Context(), client, server.URL, []byte("{}"), nil); !result.OK {
		t.Fatalf("delivery with private destinations on failed: %q", result.Message)
	}
	allowPrivate.Store(false)
	result := sendWebhook(t.Context(), client, server.URL, []byte("{}"), nil)
	if result.OK || result.Message != "destination resolves to a private network" {
		t.Fatalf("OK=%v message=%q after turning private destinations off, want refusal", result.OK, result.Message)
	}
	if reached.Load() != 1 {
		t.Fatalf("destination reached %d times, want 1", reached.Load())
	}
	allowPrivate.Store(true)
	if result := sendWebhook(t.Context(), client, server.URL, []byte("{}"), nil); !result.OK {
		t.Fatalf("delivery after turning private destinations back on failed: %q", result.Message)
	}
}
