package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestNativeHLSRoutingExcludesAnOlderRelay(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, playback.HWAccelInfo{Transformations: []playback.TransformationV3{{Name: playback.TransformationHLSSeparateAudioV3, Executor: playback.ExecutorServerV3, RecipeVersion: "1"}}})
	}))
	defer worker.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, playback.HWAccelInfo{}) }))
	defer proxy.Close()
	proxies := nodepool.NewProxyPool()
	proxies.SetNodes([]*nodepool.Node{{ID: 1, URL: proxy.URL, Type: nodepool.NodeTypeProxy, Enabled: true, Healthy: true}})
	workers := nodepool.NewTranscodePool()
	workers.SetNodes([]*nodepool.Node{{ID: 2, URL: worker.URL, Type: nodepool.NodeTypeTranscode, Enabled: true, Healthy: true}})
	planner := nodepool.NewPlanner(proxies, workers)
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "native-hls-test"
	handler.NodePlanner = planner
	policy := config.DefaultPlaybackRoutingPolicy()
	policy.RemuxExecution = config.PlaybackExecutionWorkerOnly
	policy.RemuxEgress = config.PlaybackEgressProxyOnly
	result := playback.PlannerResultV3{PlayMethod: playback.PlayRemux, TargetVideoCodec: "copy", Plan: &playback.PlanV3{Delivery: playback.DeliveryRemuxHLSV3}}
	legacy := handler.resolveHLSRouteWithPolicyV3(context.Background(), &playback.Session{ID: "legacy"}, result, policy, true, nil, nil)
	if legacy.Plan.ProxyNode == nil {
		t.Fatalf("legacy relay route is unavailable: %+v", legacy)
	}
	planner.ReleaseSession("legacy")
	result.Plan.Transformations = []playback.TransformationV3{{Name: playback.TransformationHLSSeparateAudioV3, Executor: playback.ExecutorServerV3, RecipeVersion: "1"}}
	separate := handler.resolveHLSRouteWithPolicyV3(context.Background(), &playback.Session{ID: "separate"}, result, policy, true, nil, nil)
	if separate.Plan.ProxyNode != nil {
		t.Fatal("separate audio was routed through an older relay")
	}

	upgraded := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, playback.HWAccelInfo{TransportFeatures: []string{playback.TransportFeatureNativeHLSRenditionsRelayV1}})
	}))
	defer upgraded.Close()
	proxies.SetNodes([]*nodepool.Node{{ID: 3, URL: upgraded.URL, Type: nodepool.NodeTypeProxy, Enabled: true, Healthy: true}})
	current := handler.resolveHLSRouteWithPolicyV3(context.Background(), &playback.Session{ID: "upgraded"}, result, policy, true, nil, nil)
	if current.Plan.ProxyNode == nil || current.Plan.ProxyNode.URL != upgraded.URL {
		t.Fatalf("current relay was not selected: %+v", current)
	}
}
