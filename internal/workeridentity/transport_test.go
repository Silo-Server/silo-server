package workeridentity

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
)

func TestTransportPoolRetainsVerifiedTLSDisablesProxyAndReusesConnections(t *testing.T) {
	var proxyCalls, targetCalls atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCalls.Add(1)
		if r.Header.Get("Accept-Encoding") != "" {
			t.Error("transparent compression enabled")
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer target.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxyCalls.Add(1); w.WriteHeader(500) }))
	defer proxy.Close()
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	fixtureBase, ok := target.Client().Transport.(*http.Transport)
	if !ok {
		t.Fatal("TLS fixture transport unavailable")
	}
	base := fixtureBase.Clone()
	defer base.CloseIdleConnections()
	base.Proxy = http.ProxyURL(proxyURL)
	base.TLSClientConfig.MinVersion = tls.VersionTLS10
	var pool TransportPool
	safe, err := pool.Get(base)
	if err != nil {
		t.Fatal(err)
	}
	cloned, ok := safe.(*http.Transport)
	if !ok || cloned == base || cloned.Proxy != nil || !cloned.DisableCompression || cloned.TLSClientConfig.InsecureSkipVerify || cloned.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		t.Fatal("unsafe worker transport clone")
	}
	defer cloned.CloseIdleConnections()
	if base.Proxy == nil || base.TLSClientConfig.MinVersion != tls.VersionTLS10 {
		t.Fatal("shared legacy base transport mutated")
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			same, err := pool.Get(base)
			if err != nil || same != safe {
				t.Error("pool did not reuse verified transport")
			}
		})
	}
	wg.Wait()
	client := &http.Client{Transport: safe}
	for range 2 {
		response, err := client.Get(target.URL)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatal("verified TLS request failed")
		}
	}
	if targetCalls.Load() != 2 || proxyCalls.Load() != 0 {
		t.Fatal("registered worker traffic reached configured proxy")
	}
	untrusted := &http.Transport{}
	defer untrusted.CloseIdleConnections()
	raw, err := pool.Get(untrusted)
	if err != nil {
		t.Fatal(err)
	}
	if response, err := (&http.Client{Transport: raw}).Get(target.URL); err == nil {
		if response != nil {
			_ = response.Body.Close()
		}
		t.Fatal("untrusted worker certificate accepted")
	}
	unsafe := base.Clone()
	unsafe.TLSClientConfig.InsecureSkipVerify = true
	defer unsafe.CloseIdleConnections()
	if _, err = pool.Get(unsafe); err == nil {
		t.Fatal("worker TLS bypass accepted")
	}
	if targetCalls.Load() != 2 {
		t.Fatal("TLS rejection reached worker handler")
	}
}
func TestTransportPoolCapsIndependentNetworkPools(t *testing.T) {
	var pool TransportPool
	for range 32 {
		base := &http.Transport{}
		safe, err := pool.Get(base)
		if err != nil {
			t.Fatal(err)
		}
		transport, ok := safe.(*http.Transport)
		if !ok {
			t.Fatal("expected network transport")
		}
		t.Cleanup(transport.CloseIdleConnections)
	}
	if _, err := pool.Get(&http.Transport{}); err == nil {
		t.Fatal("independent transport pool exceeded bound")
	}
}

func TestTransportPoolVerifiesURLHostnameDespiteBaseOverride(t *testing.T) {
	var calls atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(200) }))
	defer target.Close()
	fixture, ok := target.Client().Transport.(*http.Transport)
	if !ok {
		t.Fatal("TLS fixture transport unavailable")
	}
	base := fixture.Clone()
	defer base.CloseIdleConnections()
	certificate := target.Certificate()
	if len(certificate.DNSNames) == 0 {
		t.Fatal("TLS fixture needs DNS subject alternative name")
	}
	base.TLSClientConfig.ServerName = certificate.DNSNames[0]
	base.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", target.Listener.Addr().String())
	}
	var pool TransportPool
	safe, err := pool.Get(base)
	if err != nil {
		t.Fatal(err)
	}
	if response, err := (&http.Client{Transport: safe}).Get("https://wrong-worker-host.example.test"); err == nil {
		if response != nil {
			_ = response.Body.Close()
		}
		t.Fatal("base TLS name override bypassed worker URL hostname verification")
	}
	if calls.Load() != 0 {
		t.Fatal("wrong hostname reached worker handler")
	}
	if base.TLSClientConfig.ServerName != certificate.DNSNames[0] {
		t.Fatal("legacy base TLS override mutated")
	}
}
