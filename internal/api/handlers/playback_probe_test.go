package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/nodepool/nodepooltest"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/directconn"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
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
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodeKeys = nodeauthtest.SameKey("test-secret")
	nodes := make([]*nodepool.Node, 0, len(proxies))
	for i, proxy := range proxies {
		nodes = append(nodes, &nodepool.Node{ID: i + 1, URL: proxy.URL, Enabled: true, Healthy: true})
	}
	pool := nodepool.NewProxyPool()
	pool.SetNodes(nodes)
	handler.NodePlanner = nodepool.NewPlanner(pool, nodepooltest.NewTranscodePool())
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
				target, err := handler.PlaybackProbeRedirect(probePolicyContext(egress), 7, 1024, "", "")
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
			target, err := handler.PlaybackProbeRedirect(probePolicyContext(egress), 7, 1024, "", "")
			assertProbeUnavailable(t, target, err)
		})
	}
}

func TestPlaybackProbeRedirectMeasuresTheAPIPathWithoutProxies(t *testing.T) {
	handler := probeHandler()
	target, err := handler.PlaybackProbeRedirect(probePolicyContext(config.PlaybackEgressPreferProxy), 7, 1024, "", "")
	if err != nil || target != "" {
		t.Fatalf("probe redirect = %q, %v; want the API path", target, err)
	}
	target, err = handler.PlaybackProbeRedirect(probePolicyContext(config.PlaybackEgressProxyOnly), 7, 1024, "", "")
	assertProbeUnavailable(t, target, err)
}

// A direct_only device is never sent to a proxy: with a proxy that serves
// probes and an egress that allows the API path, its probe measures the API
// path. An automatic device on the same pool is sent to the proxy, so the check
// can fail.
func TestPlaybackProbeRedirectKeepsDirectOnlyDevicesOffProxies(t *testing.T) {
	proxy := probeProxyStub(t, playback.TransportFeatureBandwidthProbeV1, playback.TransportFeatureConnectionReceiptV1)
	for _, tc := range []struct {
		policy    string
		wantProxy bool
	}{
		{"automatic", true},
		{directPolicyDirectOnlyV3, false},
	} {
		t.Run(tc.policy, func(t *testing.T) {
			handler := probeHandler(proxy)
			handler.DirectState = &directStateV3{enrolled: true, device: directconn.DeviceDirect{Policy: tc.policy}}
			target, err := handler.PlaybackProbeRedirect(probePolicyContext(config.PlaybackEgressPreferProxy), 7, 1024, "tv-1", playback.MediaTransportDirectV3)
			if err != nil {
				t.Fatalf("probe redirect: %v", err)
			}
			if onProxy := strings.HasPrefix(target, proxy.URL+"/stream/bandwidth-probe/"); onProxy != tc.wantProxy {
				t.Fatalf("probe target %q: on the proxy = %v, want %v", urlOriginV3(target), onProxy, tc.wantProxy)
			}
		})
	}
}

// An enrolled device must send back the receipt of the proxy that served its
// probe, so a proxy that sends none never serves it: the probe is refused
// rather than measured on the API path or on that proxy. A device that is not
// enrolled still uses it. The probe token names the device for the proxy's
// receipt.
func TestPlaybackProbeRedirectNeedsReceiptsOfProxiesForEnrolledDevices(t *testing.T) {
	for _, tc := range []struct {
		name      string
		features  []string
		enrolled  bool
		wantProxy bool
	}{
		{"enrolled device, old proxy", []string{playback.TransportFeatureBandwidthProbeV1}, true, false},
		{"device not enrolled, old proxy", []string{playback.TransportFeatureBandwidthProbeV1}, false, true},
		{"enrolled device, proxy that sends receipts", []string{playback.TransportFeatureBandwidthProbeV1, playback.TransportFeatureConnectionReceiptV1}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxy := probeProxyStub(t, tc.features...)
			handler := probeHandler(proxy)
			handler.DirectState = &directStateV3{enrolled: tc.enrolled, device: directconn.DeviceDirect{Policy: "automatic"}}
			target, err := handler.PlaybackProbeRedirect(probePolicyContext(config.PlaybackEgressPreferProxy), 7, 1024, "tv-1", playback.MediaTransportRegularV3)
			if !tc.wantProxy {
				assertProbeUnavailable(t, urlOriginV3(target), err)
				return
			}
			if err != nil || !strings.HasPrefix(target, proxy.URL+"/stream/bandwidth-probe/") {
				t.Fatalf("probe redirect %q, %v; want the proxy", urlOriginV3(target), err)
			}
			claims, err := streamtoken.Verify(path.Base(strings.SplitN(target, "?", 2)[0]), handler.JWTSecret)
			if err != nil || claims.ProbeDevice != "tv-1" || claims.UserID != 7 {
				t.Fatalf("probe token device = %q user %d, %v; want tv-1 and 7", claims.ProbeDevice, claims.UserID, err)
			}
		})
	}
}
