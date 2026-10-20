package notifications

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
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
				dialer := newNotificationDialer(func() bool { return allowPrivate })
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

func TestWebhookDialPolicyUpdates(t *testing.T) {
	if err := newNotificationDialer(nil).Control("tcp", "8.8.8.8:443", nil); err != nil {
		t.Fatalf("default connect guard refused public fixture: %v", err)
	}
	allowPrivate := true
	dialer := newNotificationDialer(func() bool { return allowPrivate })
	address := net.JoinHostPort("192.168.1.10", "443")
	if err := dialer.Control("tcp", address, nil); err != nil {
		t.Fatal(err)
	}
	allowPrivate = false
	if err := dialer.Control("tcp", address, nil); !errors.Is(err, errPrivateDestination) {
		t.Fatal("connect guard did not read changed setting")
	}
	for _, address := range []string{"missing-port", "hostname.example:443", "[invalid]:443", "192.168.1.10:443"} {
		if err := newNotificationDialer(nil).Control("tcp", address, nil); err == nil {
			t.Errorf("default connect guard accepted %q", address)
		}
	}
}

type webhookPolicyRoundTripper func(*http.Request) (*http.Response, error)

func (fn webhookPolicyRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

// Synthetic redirect responses exercise net/http's redirect processing and the
// production Control callback. Production DialContext wiring is verified in source;
// this test substitutes the transport to avoid opening any connection.
func TestWebhookRedirectPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, destination  string
		allowPrivate, want bool
	}{
		{"public", "8.8.4.4", false, true},
		{"private-denied", "192.168.1.10", false, false},
		{"private-allowed", "192.168.1.10", true, true},
		{"blocked", "ff02::1", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newWebhookHTTPClient(func() bool { return tc.allowPrivate })
			dialer := newNotificationDialer(func() bool { return tc.allowPrivate })
			checks := 0
			client.Transport = webhookPolicyRoundTripper(func(req *http.Request) (*http.Response, error) {
				address := "8.8.8.8"
				if req.URL.Path == "/finish" {
					address = tc.destination
				}
				checks++
				if err := dialer.Control("tcp", net.JoinHostPort(address, "443"), nil); err != nil {
					return nil, err
				}
				resp := &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: req}
				if req.URL.Path == "/start" {
					resp.StatusCode = http.StatusTemporaryRedirect
					resp.Header.Set("Location", "https://redirect.example/finish")
				}
				return resp, nil
			})
			result := sendWebhook(t.Context(), client, "https://initial.example/start", []byte("{}"), nil)
			if result.OK != tc.want || checks != 2 {
				t.Fatalf("OK=%v checks=%d message=%q", result.OK, checks, result.Message)
			}
		})
	}
}

func TestWebhookTransportGuards(t *testing.T) {
	client := newWebhookHTTPClient(nil)
	transport := client.Transport.(*http.Transport)
	if client.Timeout != webhookRequestTimeout || transport.ResponseHeaderTimeout != webhookRequestTimeout ||
		transport.TLSHandshakeTimeout != 5*time.Second || transport.Proxy != nil || transport.DialContext == nil ||
		(transport.TLSClientConfig != nil && transport.TLSClientConfig.InsecureSkipVerify) {
		t.Fatal("delivery transport protections changed")
	}
	if newNotificationDialer(nil).Timeout != 5*time.Second {
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
