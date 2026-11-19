package proxy

import (
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/nodeconfig"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

var updateRouteManifest = flag.Bool("update-route-manifest", false, "update checked-in route manifest")

func TestMediaRouteManifest(t *testing.T) {
	declareProxyMediaRoutes()
	makeRouter := func() chi.Routes {
		return NewServer(nodeconfig.NewWatcher(nil, nil, nil, nodeconfig.BootstrapOverrides{}), nodesessions.NewTracker(nil, "", "", "")).router()
	}
	assertMediaManifest(t, []chi.Routes{makeRouter(), makeRouter()}, proxyMediaRoutes, "testdata/media_routes.txt")
}

func TestProxyTokenRoutesEnforceCommittedProxyEgress(t *testing.T) {
	const secret = "proxy-route-secret"
	mediaPath := writeSocketProxyMedia(t)
	srv := newSocketProxyServer(t, secret, nil)
	srv.nodeRowID = func() (int, bool) { return 11, true }
	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)

	sign := func(claims streamtoken.Claims) string { return signToken(t, claims, secret, time.Minute) }
	base := streamtoken.Claims{
		SessionID: "route-bound", MediaPath: mediaPath, PlayMethod: string(playback.PlayDirect),
		UserID: 7, ProfileID: "profile-1", MediaFileID: 42,
		RoutingWorkload:  string(noderouting.WorkloadDirectPlay),
		RoutingExecution: string(noderouting.ExecutionNone),
		RoutingEgress:    string(noderouting.EgressAPI),
	}
	apiToken := sign(base)
	for _, route := range []string{
		"/stream/direct/" + apiToken,
		"/stream/remux/" + apiToken,
		"/stream/remux/audio-v2/" + apiToken,
		"/stream/transcode/" + apiToken + "/master.m3u8",
		"/stream/transcode/" + apiToken + "/segment/000.ts",
		"/stream/subtitles/" + apiToken + "/0",
		"/stream/subtitles/" + apiToken + "/0/fonts",
	} {
		t.Run(route, func(t *testing.T) {
			response := socketProxyRequest(t, server.Client(), http.MethodGet, server.URL+route, nil)
			if response.status != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503; body = %q", response.status, response.body)
			}
		})
	}

	partial := base
	partial.RoutingWorkload = ""
	partialToken := sign(partial)
	partialResponse := socketProxyRequest(t, server.Client(), http.MethodGet, server.URL+"/stream/direct/"+partialToken, nil)
	if partialResponse.status != http.StatusConflict {
		t.Fatalf("partial route status = %d, want 409; body = %q", partialResponse.status, partialResponse.body)
	}

	proxy := base
	proxy.RoutingEgress = string(noderouting.EgressProxy)
	proxy.RoutingEgressNodeID = 11
	proxyToken := sign(proxy)
	proxyResponse := socketProxyRequest(t, server.Client(), http.MethodGet, server.URL+"/stream/direct/"+proxyToken, nil)
	if proxyResponse.status != http.StatusOK || proxyResponse.body != socketProxyMedia {
		t.Fatalf("proxy route response = %d %q, want 200 %q", proxyResponse.status, proxyResponse.body, socketProxyMedia)
	}

	for _, route := range []string{
		"/stream/remux/" + proxyToken,
		"/stream/transcode/" + proxyToken + "/master.m3u8",
	} {
		response := socketProxyRequest(t, server.Client(), http.MethodGet, server.URL+route, nil)
		if response.status != http.StatusServiceUnavailable {
			t.Fatalf("route-swapped status = %d, want 503; body = %q", response.status, response.body)
		}
	}
	remux := proxy
	remux.PlayMethod = string(playback.PlayRemux)
	remux.RoutingWorkload = string(noderouting.WorkloadRemux)
	remux.RoutingExecution = string(noderouting.ExecutionProxy)
	remuxResponse := socketProxyRequest(t, server.Client(), http.MethodGet, server.URL+"/stream/direct/"+sign(remux), nil)
	if remuxResponse.status != http.StatusServiceUnavailable {
		t.Fatalf("remux token on direct route = %d, want 503; body = %q", remuxResponse.status, remuxResponse.body)
	}
	transcode := proxy
	transcode.PlayMethod = string(playback.PlayTranscode)
	transcode.RoutingWorkload = string(noderouting.WorkloadVideoTranscode)
	transcode.RoutingExecution = string(noderouting.ExecutionTranscode)
	transcodeResponse := socketProxyRequest(t, server.Client(), http.MethodGet, server.URL+"/stream/remux/"+sign(transcode), nil)
	if transcodeResponse.status != http.StatusServiceUnavailable {
		t.Fatalf("transcode token on remux route = %d, want 503; body = %q", transcodeResponse.status, transcodeResponse.body)
	}

	sibling := newSocketProxyServer(t, secret, nil)
	sibling.nodeRowID = func() (int, bool) { return 12, true }
	siblingServer := httptest.NewServer(sibling.Handler())
	t.Cleanup(siblingServer.Close)
	siblingResponse := socketProxyRequest(t, siblingServer.Client(), http.MethodGet, siblingServer.URL+"/stream/direct/"+proxyToken, nil)
	if siblingResponse.status != http.StatusServiceUnavailable {
		t.Fatalf("sibling proxy status = %d, want 503; body = %q", siblingResponse.status, siblingResponse.body)
	}
}

func assertMediaManifest(t *testing.T, fixtures []chi.Routes, declared []streamtelemetry.MediaRoute, path string) {
	t.Helper()
	actual, err := streamtelemetry.BuildRouteManifest(fixtures, declared)
	if err != nil {
		t.Fatal(err)
	}
	if *updateRouteManifest {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(actual), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != actual {
		t.Fatalf("route manifest changed; inspect it and run go test . -update-route-manifest")
	}
	for _, route := range declared {
		if !route.Enrolled || route.Capture == nil {
			t.Fatalf("proxy route not fully enrolled: %s %s", route.Method, route.Pattern)
		}
	}
}
