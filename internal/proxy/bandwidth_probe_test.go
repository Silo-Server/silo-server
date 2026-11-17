package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

func TestBandwidthProbePurposeAndBytes(t *testing.T) {
	const secret = "test-probe-secret"
	srv := newSocketProxyServer(t, secret, nil)
	srv.nodeRowID = func() (int, bool) { return 11, true }
	server := httptest.NewServer(srv.router())
	defer server.Close()
	for _, method := range []string{streamtoken.PlayMethodBandwidthProbe, "direct"} {
		token, err := streamtoken.Sign(streamtoken.Claims{PlayMethod: method, UserID: 7}, secret, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		got := socketProxyRequest(t, server.Client(), http.MethodGet, server.URL+"/stream/bandwidth-probe/"+token+"?size=4096", nil)
		if method == streamtoken.PlayMethodBandwidthProbe {
			if got.status != 200 || len(got.body) != 4096 {
				t.Fatalf("probe: status=%d bytes=%d", got.status, len(got.body))
			}
			if receipt := got.header.Get(streamtoken.ConnectionReceiptHeader); receipt != "" {
				t.Fatal("a probe token without a device got a receipt")
			}
		} else if got.status != http.StatusForbidden {
			t.Fatalf("media token authorized probe: %d", got.status)
		}
	}
}

// A probe token that names a device gets a regular-class receipt for that
// account and device from this proxy, whatever the request carries.
func TestBandwidthProbeSendsRegularReceiptForTheTokenDevice(t *testing.T) {
	const secret = "test-probe-secret"
	srv := newSocketProxyServer(t, secret, nil)
	srv.nodeRowID = func() (int, bool) { return 11, true }
	server := httptest.NewServer(srv.router())
	defer server.Close()
	token, err := streamtoken.Sign(streamtoken.Claims{PlayMethod: streamtoken.PlayMethodBandwidthProbe, UserID: 7, ProbeDevice: "tv-1"}, secret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	got := socketProxyRequest(t, server.Client(), http.MethodGet, server.URL+"/stream/bandwidth-probe/"+token+"?size=4096", map[string]string{"X-Silo-Device-Id": "other-device"})
	if got.status != 200 {
		t.Fatalf("probe: status=%d", got.status)
	}
	claims, err := streamtoken.Verify(got.header.Get(streamtoken.ConnectionReceiptHeader), secret)
	if err != nil {
		t.Fatalf("receipt does not verify: %v", err)
	}
	if claims.PlayMethod != streamtoken.PlayMethodConnectionReceipt || claims.UserID != 7 || claims.DeviceID != "tv-1" ||
		claims.Host != "node:11" || claims.ConnectionClass != streamtoken.ConnectionClassRegular || claims.ConnectionKey != "" || claims.PathGeneration != 0 {
		t.Fatalf("receipt claims = method %q uid %d did %q host %q class %q key %q gen %d", claims.PlayMethod, claims.UserID, claims.DeviceID, claims.Host, claims.ConnectionClass, claims.ConnectionKey, claims.PathGeneration)
	}
	if lifetime := claims.ExpiresAt.Sub(claims.IssuedAt.Time); lifetime != 2*time.Minute {
		t.Fatalf("receipt lifetime = %v, want 2m", lifetime)
	}
	srv.nodeRowID = func() (int, bool) { return 0, false }
	if unnamed := socketProxyRequest(t, server.Client(), http.MethodGet, server.URL+"/stream/bandwidth-probe/"+token+"?size=4096", nil); unnamed.header.Get(streamtoken.ConnectionReceiptHeader) != "" {
		t.Fatal("a proxy that cannot name itself sent a receipt")
	}
}
