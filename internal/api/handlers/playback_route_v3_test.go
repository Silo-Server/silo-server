package handlers

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/directconn"
	"github.com/Silo-Server/silo-server/internal/noderouting"
)

// The device rule of every native route compile: a device whose connection path
// is direct_only, or whose state could not be read, is offered no proxy-egress
// shape for any workload, delivery or operator policy. A device that takes a
// proxy, and a request with no device state, keep the proxy shapes. The rule
// sits in the wrappers, so this table covers every native path that compiles a
// route; the forbidigo rule in .golangci.yml keeps a path from compiling
// outside them.
func TestNativeRouteCompilesLeaveProxyShapesOutForADirectOnlyDevice(t *testing.T) {
	devices := []struct {
		name       string
		state      *directRequestV3 // nil is a request with no device state
		takesProxy bool
	}{
		{"direct_only", &directRequestV3{enrolled: true, device: directconn.DeviceDirect{Policy: directPolicyDirectOnlyV3}}, false},
		{"unreadable", &directRequestV3{unreadable: true}, false},
		{"automatic", &directRequestV3{enrolled: true, device: directconn.DeviceDirect{Policy: "automatic"}}, true},
		{"no device state", nil, true},
	}
	routes := []struct {
		name     string
		workload noderouting.Workload
		delivery noderouting.Delivery
	}{
		{"direct play", noderouting.WorkloadDirectPlay, noderouting.DeliveryDirect},
		{"progressive remux", noderouting.WorkloadRemux, noderouting.DeliveryProgressiveRemux},
		{"HLS remux", noderouting.WorkloadRemux, noderouting.DeliveryHLSRemux},
		{"HLS video transcode", noderouting.WorkloadVideoTranscode, noderouting.DeliveryHLSVideo},
	}
	policies := map[string]config.PlaybackRoutingPolicy{"default": config.DefaultPlaybackRoutingPolicy()}
	proxyOnly := config.DefaultPlaybackRoutingPolicy()
	proxyOnly.DirectPlayEgress = config.PlaybackEgressProxyOnly
	proxyOnly.RemuxEgress = config.PlaybackEgressProxyOnly
	proxyOnly.VideoTranscodeEgress = config.PlaybackEgressProxyOnly
	policies["proxy_only"] = proxyOnly

	for _, device := range devices {
		for _, route := range routes {
			for policyName, policy := range policies {
				t.Run(device.name+"/"+route.name+"/"+policyName, func(t *testing.T) {
					request := noderouting.Request{Workload: route.workload, Delivery: route.delivery, Policy: policy, ProxyAllowed: true}
					ctx := t.Context()
					if device.state != nil {
						ctx = context.WithValue(ctx, directRequestKeyV3{}, device.state)
					}
					result, err := routeCandidatesV3(ctx, request)
					if err != nil {
						t.Fatal(err)
					}
					proxyShapes := 0
					for _, shape := range result.Candidates {
						if shape.Egress == noderouting.EgressProxy {
							proxyShapes++
						}
					}
					if device.takesProxy && proxyShapes == 0 {
						t.Fatalf("a device that takes a proxy got no proxy shape in %v", result.Candidates)
					}
					if !device.takesProxy && proxyShapes != 0 {
						t.Fatalf("a device that takes no proxy got %d proxy shapes in %v", proxyShapes, result.Candidates)
					}
				})
			}
		}
	}
}

// The resolver wrapper applies the same rule: under hard proxy_only egress a
// device that takes no proxy has no legal shape, which is a policy refusal, not
// the capacity loss a device that takes a proxy gets when no node can serve it.
func TestNativeRouteResolveRefusesADirectOnlyDeviceUnderProxyOnly(t *testing.T) {
	policy := config.DefaultPlaybackRoutingPolicy()
	policy.DirectPlayEgress = config.PlaybackEgressProxyOnly
	request := noderouting.ResolveRequest{Request: noderouting.Request{
		Workload: noderouting.WorkloadDirectPlay, Delivery: noderouting.DeliveryDirect, Policy: policy, ProxyAllowed: true,
	}, SessionID: "route-test"}

	directOnly := context.WithValue(t.Context(), directRequestKeyV3{}, &directRequestV3{enrolled: true, device: directconn.DeviceDirect{Policy: directPolicyDirectOnlyV3}})
	refused, err := resolveRouteV3(directOnly, nil, request)
	if err != nil || refused.Outcome != noderouting.OutcomePolicyUnsatisfied {
		t.Fatalf("direct_only device: outcome %q, err %v; want %q", refused.Outcome, err, noderouting.OutcomePolicyUnsatisfied)
	}
	// With no planner to reserve a node, a device that takes a proxy has a legal
	// shape and no node to serve it.
	busy, err := resolveRouteV3(t.Context(), nil, request)
	if err != nil || busy.Outcome != noderouting.OutcomeCapacityUnavailable {
		t.Fatalf("device with no state: outcome %q, err %v; want %q", busy.Outcome, err, noderouting.OutcomeCapacityUnavailable)
	}
}
