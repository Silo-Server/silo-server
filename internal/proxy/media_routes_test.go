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
		return NewServer(nodeconfig.NewWatcher(nil, nil, nil, nodeconfig.BootstrapOverrides{}), nodesessions.NewTracker(nil, "", "", "")).Handler().(chi.Routes)
	}
	assertMediaManifest(t, []chi.Routes{makeRouter(), makeRouter()}, proxyMediaRoutes, "testdata/media_routes.txt")
}

// A pre-v2 proxy only has /stream/remux/{token}. The extra literal segment is
// intentional: chi must return 404 instead of treating "audio-v2" as the token
// and ever reaching the legacy FFmpeg path.
func TestAudioV2RemuxPathIsNotCapturedByLegacyRoute(t *testing.T) {
	router := chi.NewRouter()
	router.Get("/stream/remux/{token}", func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("versioned request reached the legacy remux handler")
	})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/stream/remux/audio-v2/signed-token", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("legacy router status = %d, want 404", recorder.Code)
	}
}

func TestAudioV2RemuxClaimsRequireExactStereoEncodeShape(t *testing.T) {
	valid := streamtoken.Claims{
		PlayMethod:          streamtoken.PlayMethodAudioDownmixRemux,
		TranscodeAudio:      true,
		TargetCodecAudio:    "aac",
		SourceAudioChannels: 6,
		TargetAudioChannels: 2,
	}
	tests := []struct {
		name   string
		mutate func(*streamtoken.Claims)
		want   bool
	}{
		{name: "complete recipe", want: true},
		{name: "ordinary method", mutate: func(c *streamtoken.Claims) { c.PlayMethod = "remux" }},
		{name: "audio copy", mutate: func(c *streamtoken.Claims) { c.TranscodeAudio = false }},
		{name: "default AAC codec", mutate: func(c *streamtoken.Claims) { c.TargetCodecAudio = "" }, want: true},
		{name: "non AAC codec", mutate: func(c *streamtoken.Claims) { c.TargetCodecAudio = "eac3" }},
		{name: "stereo source", mutate: func(c *streamtoken.Claims) { c.SourceAudioChannels = 2 }},
		{name: "missing target", mutate: func(c *streamtoken.Claims) { c.TargetAudioChannels = 0 }},
		{name: "surround target", mutate: func(c *streamtoken.Claims) { c.TargetAudioChannels = 6 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			claims := valid
			if test.mutate != nil {
				test.mutate(&claims)
			}
			if got := validAudioV2RemuxClaims(&claims); got != test.want {
				t.Fatalf("validAudioV2RemuxClaims() = %t, want %t for %#v", got, test.want, claims)
			}
		})
	}
}

func TestProxyTokenRoutesEnforceCommittedProxyEgress(t *testing.T) {
	const secret = "proxy-route-secret"
	mediaPath := writeSocketProxyMedia(t)
	srv := newSocketProxyServer(t, secret, nil)
	srv.nodeRowID = func() (int, bool) { return 11, true }
	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)

	sign := func(claims streamtoken.Claims) string {
		t.Helper()
		token, err := streamtoken.Sign(claims, secret, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
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

func TestProxyPlaybackEndpointStatusV3BindsRecipeFamily(t *testing.T) {
	direct := streamtoken.Claims{
		PlayMethod: string(playback.PlayDirect), RoutingWorkload: string(noderouting.WorkloadDirectPlay),
		RoutingExecution: string(noderouting.ExecutionNone), RoutingEgress: string(noderouting.EgressProxy),
		RoutingEgressNodeID: 11,
	}
	remux := streamtoken.Claims{
		PlayMethod: string(playback.PlayRemux), RoutingWorkload: string(noderouting.WorkloadRemux),
		RoutingExecution: string(noderouting.ExecutionProxy), RoutingEgress: string(noderouting.EgressProxy),
		RoutingEgressNodeID: 11,
	}
	remoteRemux := remux
	remoteRemux.RoutingExecution = string(noderouting.ExecutionTranscode)
	transcodeRemux := streamtoken.Claims{
		PlayMethod: string(playback.PlayTranscode), RoutingWorkload: string(noderouting.WorkloadRemux),
		RoutingExecution: string(noderouting.ExecutionTranscode), RoutingEgress: string(noderouting.EgressProxy),
		RoutingEgressNodeID: 11,
	}
	transcodeVideo := transcodeRemux
	transcodeVideo.RoutingWorkload = string(noderouting.WorkloadVideoTranscode)

	for _, test := range []struct {
		name     string
		claims   streamtoken.Claims
		endpoint proxyPlaybackEndpointV3
		want     int
	}{
		{name: "direct", claims: direct, endpoint: proxyPlaybackEndpointDirectV3},
		{name: "direct on remux", claims: direct, endpoint: proxyPlaybackEndpointRemuxV3, want: http.StatusServiceUnavailable},
		{name: "remux", claims: remux, endpoint: proxyPlaybackEndpointRemuxV3},
		{name: "remote remux", claims: remoteRemux, endpoint: proxyPlaybackEndpointRemuxV3},
		{name: "remote remux identity", claims: remoteRemux, endpoint: proxyPlaybackEndpointIdentityV3},
		{name: "versioned remux", claims: func() streamtoken.Claims {
			claims := remux
			claims.PlayMethod = streamtoken.PlayMethodAudioDownmixRemux
			return claims
		}(), endpoint: proxyPlaybackEndpointRemuxV3},
		{name: "remux on direct", claims: remux, endpoint: proxyPlaybackEndpointDirectV3, want: http.StatusServiceUnavailable},
		{name: "HLS remux", claims: transcodeRemux, endpoint: proxyPlaybackEndpointTranscodeV3},
		{name: "video transcode", claims: transcodeVideo, endpoint: proxyPlaybackEndpointTranscodeV3},
		{name: "tone-map transcode", claims: func() streamtoken.Claims {
			claims := transcodeVideo
			claims.PlayMethod = streamtoken.PlayMethodToneMapTranscode
			return claims
		}(), endpoint: proxyPlaybackEndpointTranscodeV3},
		{name: "audio-downmix transcode", claims: func() streamtoken.Claims {
			claims := transcodeVideo
			claims.PlayMethod = streamtoken.PlayMethodAudioDownmixTranscode
			return claims
		}(), endpoint: proxyPlaybackEndpointTranscodeV3},
		{name: "copy-fmp4 transcode", claims: func() streamtoken.Claims {
			claims := transcodeVideo
			claims.PlayMethod = streamtoken.PlayMethodCopyFMP4Transcode
			return claims
		}(), endpoint: proxyPlaybackEndpointTranscodeV3},
		{name: "transcode on identity", claims: transcodeVideo, endpoint: proxyPlaybackEndpointIdentityV3, want: http.StatusServiceUnavailable},
		{name: "direct auxiliary", claims: direct, endpoint: proxyPlaybackEndpointAuxiliaryV3},
		{name: "remux auxiliary", claims: remux, endpoint: proxyPlaybackEndpointAuxiliaryV3},
		{name: "transcode auxiliary", claims: transcodeVideo, endpoint: proxyPlaybackEndpointAuxiliaryV3},
		{name: "download auxiliary", claims: streamtoken.Claims{PlayMethod: streamtoken.PlayMethodDownload}, endpoint: proxyPlaybackEndpointAuxiliaryV3, want: http.StatusServiceUnavailable},
		{name: "legacy direct", claims: streamtoken.Claims{PlayMethod: string(playback.PlayDirect)}, endpoint: proxyPlaybackEndpointDirectV3},
		{name: "legacy direct on remux", claims: streamtoken.Claims{PlayMethod: string(playback.PlayDirect)}, endpoint: proxyPlaybackEndpointRemuxV3, want: http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := proxyPlaybackEndpointStatusV3(&test.claims, test.endpoint); got != test.want {
				t.Fatalf("status = %d, want %d", got, test.want)
			}
		})
	}
}

func TestProxyEgressStatusV3RequiresSelectedNode(t *testing.T) {
	const (
		workload  = "direct_play"
		execution = "none"
		egress    = "proxy"
	)
	for _, test := range []struct {
		name               string
		egressNodeID       int
		currentNodeID      int
		currentNodeIDKnown bool
		want               int
	}{
		{name: "selected node", egressNodeID: 11, currentNodeID: 11, currentNodeIDKnown: true},
		{name: "sibling node", egressNodeID: 11, currentNodeID: 12, currentNodeIDKnown: true, want: http.StatusServiceUnavailable},
		{name: "unresolved self", egressNodeID: 11, want: http.StatusServiceUnavailable},
		{name: "unbound routed artifact", currentNodeID: 11, currentNodeIDKnown: true, want: http.StatusConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := proxyEgressStatusV3(workload, execution, egress, test.egressNodeID, test.currentNodeID, test.currentNodeIDKnown)
			if got != test.want {
				t.Fatalf("status = %d, want %d", got, test.want)
			}
		})
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
