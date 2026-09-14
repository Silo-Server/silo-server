package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/downloads"
	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/nodepool"
)

// recordingDownloadPlanner hands out one proxy and records reservations, so a
// test can prove the reservation of a proxy the client cannot reach is given
// back rather than pinned until it ages out.
type recordingDownloadPlanner struct {
	proxy    *nodepool.Node
	plans    int
	released []string
}

func (p *recordingDownloadPlanner) PlanDownload(string, ...string) nodepool.Plan {
	p.plans++
	return nodepool.Plan{ProxyNode: p.proxy}
}

func (p *recordingDownloadPlanner) ReleaseSession(sessionID string) {
	p.released = append(p.released, sessionID)
}

// A download requested through a network access provider is served from this
// server when the planned proxy has no origin on that provider: the proxy is
// never even preflighted, and its reservation is released.
func TestDirectDownloadViaProxyServesLocallyWhenTheProxyIsUnreachableOnTheProviderPath(t *testing.T) {
	proxyRequests := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyRequests++
		w.WriteHeader(http.StatusOK)
	}))
	defer proxy.Close()
	svc := &proxyDownloadService{
		fakeDownloadService: &fakeDownloadService{},
		directTarget:        &downloads.FileTarget{Path: "/media/movie.mkv", MediaFileID: 42, ProxyEligible: true},
	}
	planner := &recordingDownloadPlanner{proxy: &nodepool.Node{ID: 3, URL: proxy.URL, Enabled: true, Healthy: true}}
	h := NewDownloadHandler(svc)
	h.SetProxyDelivery(planner, func() string { return "secret" })

	req := downloadTestRequest(http.MethodGet, "/direct-download-proxy?file_id=42", nil, 7, "", "")
	req = req.WithContext(netaccess.WithPath(req.Context(), netaccess.Path{Provider: "tailscale"}))
	rec := httptest.NewRecorder()
	h.HandleDirectDownloadViaProxy(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "served" || svc.gotDirectFileID != 42 {
		t.Fatalf("status = %d body = %q file = %d, want the file served locally", rec.Code, rec.Body.String(), svc.gotDirectFileID)
	}
	if rec.Header().Get("Location") != "" {
		t.Fatalf("Location = %q, want none", rec.Header().Get("Location"))
	}
	if proxyRequests != 0 {
		t.Fatalf("proxy was preflighted %d times for a client that cannot reach it", proxyRequests)
	}
	if planner.plans != 1 || len(planner.released) != 1 {
		t.Fatalf("plans = %d releases = %v, want the unusable reservation released", planner.plans, planner.released)
	}
}

// With a connected origin on the client's provider the redirect names that
// origin, while the preflight dials the proxy's backend address: the overlay
// origin is only resolvable by overlay members, which this server need not be.
func TestDirectDownloadViaProxyRedirectsToTheProviderOrigin(t *testing.T) {
	preflights := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		preflights++
		if r.Method != http.MethodHead {
			t.Errorf("preflight method = %s", r.Method)
		}
		if !strings.HasPrefix(r.URL.Path, "/downloads/file/") {
			t.Errorf("preflight path = %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer proxy.Close()
	// The tailnet origin is undialable from the test on purpose; only the
	// backend URL (the test server) may be probed.
	const origin = "https://proxy-1.tail1234.ts.net"
	svc := &proxyDownloadService{
		fakeDownloadService: &fakeDownloadService{},
		directTarget:        &downloads.FileTarget{Path: "/media/movie.mkv", MediaFileID: 42, ProxyEligible: true},
	}
	proxies := nodepool.NewProxyPool()
	proxies.SetNodes([]*nodepool.Node{{
		ID: 3, URL: proxy.URL, Enabled: true, Healthy: true,
		NetworkAccess: netaccess.NodeNetworkAccess{"tailscale": {State: netaccess.StateConnected, Origin: origin}},
	}})
	h := NewDownloadHandler(svc)
	h.SetProxyDelivery(nodepool.NewPlanner(proxies, nodepool.NewTranscodePool()), func() string { return "secret" })

	req := downloadTestRequest(http.MethodGet, "/direct-download-proxy?file_id=42", nil, 7, "", "")
	req = req.WithContext(netaccess.WithPath(req.Context(), netaccess.Path{Provider: "tailscale"}))
	rec := httptest.NewRecorder()
	h.HandleDirectDownloadViaProxy(rec, req)

	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want 307 (body: %s)", rec.Code, rec.Body.String())
	}
	location := rec.Header().Get("Location")
	if !strings.HasPrefix(location, origin+"/downloads/file/") || strings.Contains(location, proxy.URL) {
		t.Fatalf("Location = %q, want the provider origin and no backend address", location)
	}
	if preflights != 1 {
		t.Fatalf("preflights = %d, want 1", preflights)
	}

	// The same proxy on the default path redirects to its backend URL (no
	// public_url is set). The preflight verdict is about the proxy, not the
	// path, so it is shared and not repeated.
	rec = httptest.NewRecorder()
	h.HandleDirectDownloadViaProxy(rec, downloadTestRequest(http.MethodGet, "/direct-download-proxy?file_id=42", nil, 7, "", ""))
	if rec.Code != http.StatusTemporaryRedirect || !strings.HasPrefix(rec.Header().Get("Location"), proxy.URL+"/downloads/file/") {
		t.Fatalf("default-path status = %d Location = %q, want the backend origin", rec.Code, rec.Header().Get("Location"))
	}
	if preflights != 1 {
		t.Fatalf("preflights after the default-path request = %d, want the cached verdict reused", preflights)
	}
}
