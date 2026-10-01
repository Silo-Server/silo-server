package noderouting

import (
	"slices"
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

func TestCandidatesExpressGPUOffload(t *testing.T) {
	policy := config.PlaybackRoutingPolicy{
		DirectPlayEgress:        config.PlaybackEgressPreferAPI,
		RemuxExecution:          config.PlaybackExecutionPreferAPI,
		RemuxEgress:             config.PlaybackEgressPreferAPI,
		VideoTranscodeExecution: config.PlaybackExecutionPreferWorker,
		VideoTranscodeEgress:    config.PlaybackEgressPreferProxy,
	}
	wants := map[Delivery]string{
		DeliveryDirect:           "direct_api",
		DeliveryProgressiveRemux: "progressive_remux_api",
		DeliveryHLSRemux:         "hls_remux_api",
		DeliveryHLSVideo:         "hls_video_transcode_proxy",
	}
	for delivery, want := range wants {
		var workload Workload
		switch delivery {
		case DeliveryDirect:
			workload = WorkloadDirectPlay
		case DeliveryHLSVideo:
			workload = WorkloadVideoTranscode
		default:
			workload = WorkloadRemux
		}
		result, err := Candidates(Request{Workload: workload, Delivery: delivery, Policy: policy, ProxyAllowed: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Candidates) == 0 || result.Candidates[0].ID != want {
			t.Fatalf("%s candidates = %#v, want %q first", delivery, result.Candidates, want)
		}
	}
}

func TestCandidatesKeepExecutionPreferenceAheadOfEgressOnTie(t *testing.T) {
	policy := config.DefaultPlaybackRoutingPolicy()
	policy.VideoTranscodeEgress = config.PlaybackEgressPreferAPI
	result, err := Candidates(Request{
		Workload: WorkloadVideoTranscode, Delivery: DeliveryHLSVideo,
		Policy: policy, ProxyAllowed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Candidates[0].ID; got != "hls_video_transcode_api" {
		t.Fatalf("first candidate = %q, want worker execution with API egress", got)
	}
	if got := result.Candidates[1].ID; got != "hls_video_transcode_proxy" {
		t.Fatalf("second candidate = %q, want worker execution before local API", got)
	}
}

func TestCandidatesPreferTranscodeNodeBeforeOtherExecutors(t *testing.T) {
	policy := config.DefaultPlaybackRoutingPolicy()
	policy.RemuxExecution = config.PlaybackExecutionPreferTranscode

	tests := []struct {
		name     string
		delivery Delivery
		want     []string
	}{
		{
			name:     "HLS remux",
			delivery: DeliveryHLSRemux,
			want: []string{
				"hls_remux_transcode_proxy",
				"hls_remux_transcode_api",
				"hls_remux_api",
			},
		},
		{
			name:     "progressive remux",
			delivery: DeliveryProgressiveRemux,
			want: []string{
				"progressive_remux_transcode_proxy",
				"progressive_remux_proxy",
				"progressive_remux_api",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := Candidates(Request{
				Workload: WorkloadRemux, Delivery: test.delivery,
				Policy: policy, ProxyAllowed: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			got := make([]string, len(result.Candidates))
			for i, candidate := range result.Candidates {
				got[i] = candidate.ID
			}
			if !slices.Equal(got, test.want) {
				t.Fatalf("candidates = %v, want %v", got, test.want)
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
