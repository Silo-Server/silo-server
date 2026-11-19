package noderouting

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
)

func TestCandidatesFollowSiloDefaults(t *testing.T) {
	tests := []struct {
		name     string
		workload Workload
		delivery Delivery
		want     string
	}{
		{"direct", WorkloadDirectPlay, DeliveryDirect, "direct_proxy"},
		{"progressive remux", WorkloadRemux, DeliveryProgressiveRemux, "progressive_remux_transcode_proxy"},
		{"hls remux", WorkloadRemux, DeliveryHLSRemux, "hls_remux_transcode_proxy"},
		{"video", WorkloadVideoTranscode, DeliveryHLSVideo, "hls_video_transcode_proxy"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := Candidates(Request{
				Workload: test.workload, Delivery: test.delivery,
				Policy: config.DefaultPlaybackRoutingPolicy(), ProxyAllowed: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Candidates) == 0 || result.Candidates[0].ID != test.want {
				t.Fatalf("candidates = %#v, want %q first", result.Candidates, test.want)
			}
		})
	}
}

func TestCandidatesRespectHardPolicyAndClientOriginSupport(t *testing.T) {
	policy := config.DefaultPlaybackRoutingPolicy()
	policy.DirectPlayEgress = config.PlaybackEgressProxyOnly
	result, err := Candidates(Request{
		Workload: WorkloadDirectPlay, Delivery: DeliveryDirect,
		Policy: policy, ProxyAllowed: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 0 {
		t.Fatalf("candidates = %#v, want hard conflict", result.Candidates)
	}
	if len(result.Rejected) != 2 {
		t.Fatalf("rejections = %#v, want both legal shapes explained", result.Rejected)
	}
}

func TestCandidatesNeverInventUnsupportedRelayShapes(t *testing.T) {
	policy := config.DefaultPlaybackRoutingPolicy()
	policy.RemuxExecution = config.PlaybackExecutionAPIOnly
	policy.RemuxEgress = config.PlaybackEgressProxyOnly
	result, err := Candidates(Request{
		Workload: WorkloadRemux, Delivery: DeliveryHLSRemux,
		Policy: policy, ProxyAllowed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 0 {
		t.Fatalf("candidates = %#v, want no API-to-proxy route", result.Candidates)
	}
}
