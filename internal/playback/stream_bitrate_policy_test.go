package playback

import "testing"

func bitratePolicyFixtureV3() PlannerInputV3 {
	file := detailedFixtureFileV3()
	file.Resolution = "1080p"
	file.Bitrate = 8_000
	file.VideoTracks[0].Width = 1920
	file.VideoTracks[0].Height = 1080
	file.VideoTracks[0].Bitrate = 7_800
	file.VideoTracks[0].BitDepth = 8
	file.VideoTracks[0].VideoRange = "SDR"
	file.VideoTracks[0].VideoRangeType = "SDR"
	file.VideoTracks[0].ColorTransfer = "bt709"
	req := validStartRequestV3()
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{8}, MaxWidth: 1920, MaxHeight: 1080, MaxFrameRate: 60, MaxBitrateKbps: 20_000, Hardware: true}}
	return PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3()}
}

// TestServerBitrateCapPolicy covers the server bitrate cap on the fixture's
// 1080p SDR source: whether the source total fits, which cap binds, the
// audio reserve, and the fail-closed terminals.
func TestServerBitrateCapPolicy(t *testing.T) {
	clientCap := func(kbps int) func(*PlannerInputV3) {
		return func(input *PlannerInputV3) { input.Request.BandwidthCapKbps = &kbps }
	}
	sourceBitrates := func(total, video int) func(*PlannerInputV3) {
		return func(input *PlannerInputV3) {
			input.EffectiveFile.Bitrate = total
			input.EffectiveFile.VideoTracks[0].Bitrate = video
		}
	}
	surround := func(input *PlannerInputV3) {
		input.EffectiveFile.AudioChannels, input.EffectiveFile.AudioTracks[0].Channels, input.EffectiveFile.AudioTracks[0].Layout = 6, 6, "5.1"
	}
	tests := []struct {
		name      string
		cap       int
		mutate    []func(*PlannerInputV3)
		method    PlayMethod // checked when set
		maxTotal  int        // video+audio target ceiling when set
		channels  int        // AAC output channels when set
		terminal  string
		retryable bool
	}{
		{name: "direct plays a source within the limit", cap: 10_000, method: PlayDirect},
		{name: "transcodes and budgets audio", cap: 4_000, method: PlayTranscode, maxTotal: 3_800},
		// The client's lower cap wins over the server cap.
		{name: "lower client cap is honored", cap: 4_000, mutate: []func(*PlannerInputV3){clientCap(2_000)}, maxTotal: 1_900},
		{name: "counts audio in the source total", cap: 4_000, mutate: []func(*PlannerInputV3){sourceBitrates(4_200, 3_900)}, method: PlayTranscode, maxTotal: 3_800},
		{name: "keeps 5.1 when its share allows", cap: 4_000, mutate: []func(*PlannerInputV3){surround}, channels: 6},
		// 142 kbps over six channels sounds worse than stereo.
		{name: "downmixes 5.1 under a low limit", cap: 600, mutate: []func(*PlannerInputV3){surround}, maxTotal: 570, channels: 2},
		{name: "unknown source total requires transcode", cap: 4_000, mutate: []func(*PlannerInputV3){sourceBitrates(0, 3_900)}, method: PlayTranscode},
		{name: "lower client cap against the source total", cap: 4_000, mutate: []func(*PlannerInputV3){sourceBitrates(2_300, 1_800), clientCap(2_000)}, method: PlayTranscode, maxTotal: 1_900},
		{name: "fails closed without transcode", cap: 4_000, mutate: []func(*PlannerInputV3){func(input *PlannerInputV3) {
			delete(input.Request.ClientPlaybackContext.Deliveries, DeliveryClassHLSV3)
		}}, terminal: TerminalBitratePolicyUnavailableV3},
		{name: "too low fails closed", cap: 100, terminal: TerminalBitratePolicyUnavailableV3},
		{name: "keeps a transient blocker retryable", cap: 4_000, mutate: []func(*PlannerInputV3){func(input *PlannerInputV3) {
			input.HLSVideoRegistry = func() *TransformationRegistryV3 { return NewTransformationRegistryV3(nil) }
		}}, terminal: TerminalBitratePolicyUnavailableV3, retryable: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := bitratePolicyFixtureV3()
			input.ServerBitrateCapKbps = tt.cap
			for _, mutate := range tt.mutate {
				mutate(&input)
			}
			result := PlanPlaybackV3(input)
			if tt.terminal != "" {
				if result.Terminal == nil || result.Terminal.Reason != tt.terminal || tt.retryable && !result.Terminal.Retryable {
					t.Fatalf("expected %s terminal (retryable %v): %s", tt.terminal, tt.retryable, ExplainPlannerResultV3(result))
				}
				return
			}
			if result.Plan == nil || tt.method != "" && result.PlayMethod != tt.method {
				t.Fatalf("expected %s: %s", tt.method, ExplainPlannerResultV3(result))
			}
			if tt.maxTotal > 0 && result.TargetBitrateKbps+result.TargetAudioBitrateKbps > tt.maxTotal {
				t.Fatalf("video/audio targets exceed reserved cap %d: %d + %d", tt.maxTotal, result.TargetBitrateKbps, result.TargetAudioBitrateKbps)
			}
			if tt.channels > 0 && (result.TargetAudioChannels != tt.channels || optionalValueV3(result.Plan.EffectiveRecipe.AudioChannels) != tt.channels) {
				t.Fatalf("AAC channels = %d (recipe %s) at %d kbps, want %d", result.TargetAudioChannels, optionalIntV3(result.Plan.EffectiveRecipe.AudioChannels), result.TargetAudioBitrateKbps, tt.channels)
			}
		})
	}
}

func TestServerBitrateCapAudioOnlyUnknownTotalConvertsAAC(t *testing.T) {
	file := audioOnlyFixtureFileV3()
	file.Bitrate = 0
	file.AudioTracks[0].Bitrate = 128
	req := validStartRequestV3()
	req.FileID = file.ID
	req.Capabilities.Containers = []string{"mp4"}
	result := PlanPlaybackV3(PlannerInputV3{
		Request:              req,
		RequestedFile:        file,
		EffectiveFile:        file,
		AudioTrackIndex:      0,
		ServerBitrateCapKbps: 256,
		Settings:             PlannerSettingsV3{TranscodeEnabled: true},
		Registry:             testTransformationRegistryV3(),
	})
	if result.Plan == nil || result.PlayMethod != PlayRemux || !result.TranscodeAudio || result.TargetAudioCodec != "aac" {
		t.Fatalf("expected AAC conversion for unknown container bitrate: %s", ExplainPlannerResultV3(result))
	}
	if result.TargetAudioBitrateKbps <= 0 || result.TargetAudioBitrateKbps >= 256 {
		t.Fatalf("AAC target %d must fit under 256 kbps cap", result.TargetAudioBitrateKbps)
	}
}

// A client lowering Auto because of buffering sends 70% of the delivered
// bitrate, and only while the menu has an entry below what plays. Every plan
// along the way fits video and audio within the estimate's budget, and the
// steps reach a stream a 500 kbps link carries.
func TestAutoLowersUntilTheStreamFitsA500KbpsLink(t *testing.T) {
	input := bitratePolicyFixtureV3()
	estimate := 8_000
	for range 8 {
		input.Request.BandwidthEstimateKbps = &estimate
		result := PlanPlaybackV3(input)
		if result.Plan == nil || result.PlayMethod != PlayTranscode {
			t.Fatalf("estimate %d: %s", estimate, ExplainPlannerResultV3(result))
		}
		total := result.TargetBitrateKbps + result.TargetAudioBitrateKbps
		if total > StreamBudgetKbpsV3(estimate) {
			t.Fatalf("estimate %d: %d + %d kbps exceeds its budget", estimate, result.TargetBitrateKbps, result.TargetAudioBitrateKbps)
		}
		if total <= 400 {
			return
		}
		delivered := *result.Plan.EffectiveRecipe.BitrateKbps
		lower := false
		for _, quality := range result.Plan.AvailableQualities {
			lower = lower || quality.BitrateKbps < delivered
		}
		if !lower {
			t.Fatalf("lowering stops at %d kbps: no menu entry below it", delivered)
		}
		estimate = delivered * 7 / 10
	}
	t.Fatalf("still above 400 kbps at estimate %d", estimate)
}
