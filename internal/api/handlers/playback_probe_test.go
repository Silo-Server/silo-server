package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// probeProxyStub is a pooled proxy whose /hw-capabilities lists features.
func probeProxyStub(t *testing.T, features ...string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hw-capabilities" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, playback.HWAccelInfo{TransportFeatures: features})
	}))
	t.Cleanup(server.Close)
	return server
}

func probeHandler(proxies ...*httptest.Server) *PlaybackHandler {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	nodes := make([]*nodepool.Node, 0, len(proxies))
	for i, proxy := range proxies {
		nodes = append(nodes, &nodepool.Node{ID: i + 1, URL: proxy.URL, Enabled: true, Healthy: true})
	}
	pool := nodepool.NewProxyPool()
	pool.SetNodes(nodes)
	handler.NodePlanner = nodepool.NewPlanner(pool, nodepool.NewTranscodePool())
	return handler
}

func probePolicyContext(egress config.PlaybackEgressPreference) context.Context {
	policy := config.DefaultPlaybackRoutingPolicy()
	policy.DirectPlayEgress = egress
	return withPlaybackRoutingPolicySnapshotV3(context.Background(), policy)
}

func assertProbeUnavailable(t *testing.T, target string, err error) {
	t.Helper()
	if operationErr, ok := errors.AsType[*PlaybackOperationError](err); !ok || operationErr.Status != http.StatusServiceUnavailable {
		t.Fatalf("probe redirect = %q, %v; want a 503", target, err)
	}
}

// During a rolling upgrade one proxy may predate probes while another serves
// them. The probe must measure the one that serves them, every time.
func TestPlaybackProbeRedirectPicksTheProxyThatServesProbes(t *testing.T) {
	older := probeProxyStub(t)
	current := probeProxyStub(t, playback.TransportFeatureBandwidthProbeV1)
	for _, egress := range []config.PlaybackEgressPreference{config.PlaybackEgressProxyOnly, config.PlaybackEgressPreferProxy} {
		t.Run(string(egress), func(t *testing.T) {
			handler := probeHandler(older, current)
			for range 3 {
				target, err := handler.PlaybackProbeRedirect(probePolicyContext(egress), 7, 1024)
				if err != nil {
					t.Fatalf("probe redirect: %v", err)
				}
				if !strings.HasPrefix(target, current.URL+"/stream/bandwidth-probe/") {
					t.Fatalf("probe target = %q, want the proxy at %s", target, current.URL)
				}
			}
		})
	}
}

// With no proxy that serves probes the API path is not measured in its place:
// playback would use the older proxy, not the API server.
func TestPlaybackProbeRedirectRefusesWhenNoProxyServesProbes(t *testing.T) {
	for _, egress := range []config.PlaybackEgressPreference{config.PlaybackEgressProxyOnly, config.PlaybackEgressPreferProxy} {
		t.Run(string(egress), func(t *testing.T) {
			handler := probeHandler(probeProxyStub(t))
			target, err := handler.PlaybackProbeRedirect(probePolicyContext(egress), 7, 1024)
			assertProbeUnavailable(t, target, err)
		})
	}
}

func TestPlaybackProbeRedirectMeasuresTheAPIPathWithoutProxies(t *testing.T) {
	handler := probeHandler()
	target, err := handler.PlaybackProbeRedirect(probePolicyContext(config.PlaybackEgressPreferProxy), 7, 1024)
	if err != nil || target != "" {
		t.Fatalf("probe redirect = %q, %v; want the API path", target, err)
	}
	target, err = handler.PlaybackProbeRedirect(probePolicyContext(config.PlaybackEgressProxyOnly), 7, 1024)
	assertProbeUnavailable(t, target, err)
}
