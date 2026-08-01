package remotestream

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"
)

type staticResolver map[string][]netip.Addr

func (r staticResolver) LookupNetIP(_ context.Context, _ string, host string) ([]netip.Addr, error) {
	addresses, ok := r[host]
	if !ok {
		return nil, errors.New("not found")
	}
	return append([]netip.Addr(nil), addresses...), nil
}

type recordingDialer struct {
	address string
	err     error
}

func (d *recordingDialer) DialContext(_ context.Context, _, address string) (net.Conn, error) {
	d.address = address
	return nil, d.err
}

func TestValidateURLRejectsNonPublicAndReservedAddresses(t *testing.T) {
	for _, raw := range []string{
		"http://127.0.0.1/stream",
		"http://10.0.0.7/stream",
		"http://100.64.0.1/stream",
		"http://169.254.169.254/latest/meta-data",
		"http://192.0.2.1/stream",
		"http://[::1]/stream",
		"http://[2001:db8::1]/stream",
		"http://localhost/stream",
		"file:///tmp/media",
	} {
		if _, err := ValidateURL(context.Background(), raw); err == nil {
			t.Fatalf("ValidateURL(%q) succeeded, want rejection", raw)
		}
	}
}

func TestValidateURLRejectsMixedPublicAndPrivateDNSAnswers(t *testing.T) {
	resolver := staticResolver{
		"provider.example": {netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("127.0.0.1")},
	}
	if _, err := validateURL(context.Background(), "https://provider.example/stream", resolver); err == nil {
		t.Fatal("validateURL succeeded with a private DNS answer")
	}
}

func TestValidateURLAcceptsPublicAddress(t *testing.T) {
	got, err := ValidateURL(context.Background(), "https://1.1.1.1/stream?token=x")
	if err != nil {
		t.Fatalf("ValidateURL returned error: %v", err)
	}
	if got.String() != "https://1.1.1.1/stream?token=x" {
		t.Fatalf("validated URL = %q", got.String())
	}
}

func TestValidateURLSyntaxRejectsUnsafeStructureWithoutDNS(t *testing.T) {
	for _, raw := range []string{
		"file:///etc/passwd",
		"https://user:secret@1.1.1.1/stream",
		"https://1.1.1.1/stream\ninjected",
		"http://127.0.0.1/admin",
	} {
		if _, err := ValidateURLSyntax(raw); err == nil {
			t.Fatalf("ValidateURLSyntax(%q) succeeded, want rejection", raw)
		}
	}
	parsed, err := ValidateURLSyntax("https://provider.invalid/stream?token=x")
	if err != nil || parsed.String() != "https://provider.invalid/stream?token=x" {
		t.Fatalf("ValidateURLSyntax deferred DNS incorrectly: %v, %v", parsed, err)
	}
}

func TestSafeTransportDialsResolvedIPAddress(t *testing.T) {
	resolver := staticResolver{"provider.example": {netip.MustParseAddr("1.1.1.1")}}
	dialer := &recordingDialer{err: errors.New("stop after recording")}
	transport := newSafeTransport(resolver, dialer)

	_, err := transport.DialContext(context.Background(), "tcp", "provider.example:443")
	if err == nil {
		t.Fatal("DialContext succeeded, want recording dialer error")
	}
	if dialer.address != "1.1.1.1:443" {
		t.Fatalf("dialed %q, want pinned public address", dialer.address)
	}
}

func TestSafeTransportRejectsReboundPrivateAddress(t *testing.T) {
	resolver := staticResolver{"provider.example": {netip.MustParseAddr("127.0.0.1")}}
	dialer := &recordingDialer{}
	transport := newSafeTransport(resolver, dialer)

	if _, err := transport.DialContext(context.Background(), "tcp", "provider.example:443"); err == nil {
		t.Fatal("DialContext succeeded after DNS rebinding to loopback")
	}
	if dialer.address != "" {
		t.Fatalf("unsafe address reached dialer: %q", dialer.address)
	}
}

func TestSafeTransportBoundsResponseHeadersWithoutClientBodyTimeout(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
	})
	resolver := staticResolver{"provider.example": {netip.MustParseAddr("1.1.1.1")}}
	transport := newSafeTransport(resolver, contextDialerFunc(func(context.Context, string, string) (net.Conn, error) {
		return clientConn, nil
	}))
	if transport.ResponseHeaderTimeout != responseHeaderTimeout {
		t.Fatalf("ResponseHeaderTimeout = %s, want %s", transport.ResponseHeaderTimeout, responseHeaderTimeout)
	}
	transport.ResponseHeaderTimeout = 20 * time.Millisecond

	go func() {
		reader := bufio.NewReader(serverConn)
		for {
			line, err := reader.ReadString('\n')
			if err != nil || line == "\r\n" {
				return
			}
		}
	}()
	request, err := http.NewRequest(http.MethodGet, "http://provider.example/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	response, err := transport.RoundTrip(request)
	if response != nil {
		_ = response.Body.Close()
	}
	if err == nil {
		t.Fatal("RoundTrip succeeded without response headers")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("response header timeout took %s", elapsed)
	}

	client := NewSafeClient()
	if client.Timeout != 0 {
		t.Fatalf("client body timeout = %s, want no whole-response timeout", client.Timeout)
	}
	if closer, ok := client.Transport.(io.Closer); ok {
		_ = closer.Close()
	}
}

func TestCheckRedirectRejectsDowngradeAndExcessiveRedirects(t *testing.T) {
	previous := &http.Request{URL: mustURL(t, "https://1.1.1.1/start")}
	next := &http.Request{URL: mustURL(t, "http://1.1.1.1/next")}
	if err := checkRedirect(next, []*http.Request{previous}); err == nil {
		t.Fatal("HTTPS downgrade redirect was accepted")
	}
	via := make([]*http.Request, maxRedirects)
	for i := range via {
		via[i] = previous
	}
	if err := checkRedirect(previous, via); err == nil {
		t.Fatal("excessive redirects were accepted")
	}
	private := &http.Request{URL: mustURL(t, "https://127.0.0.1/private")}
	if err := checkRedirect(private, []*http.Request{previous}); err == nil {
		t.Fatal("redirect to a private address was accepted")
	}
}

type contextDialerFunc func(context.Context, string, string) (net.Conn, error)

func (f contextDialerFunc) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return f(ctx, network, address)
}

func TestRedactURLRemovesCredentialsPathQueryAndFragment(t *testing.T) {
	raw := "https://user:pass@provider.example/stremio/secret-token/manifest.json?api_key=secret#fragment"
	got := RedactURL(raw)
	for _, secret := range []string{"user", "pass", "secret-token", "api_key", "secret", "fragment"} {
		if strings.Contains(got, secret) {
			t.Fatalf("RedactURL leaked %q in %q", secret, got)
		}
	}
	if got != "https://provider.example/%3Credacted%3E" {
		t.Fatalf("RedactURL = %q", got)
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
