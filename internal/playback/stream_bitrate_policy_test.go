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

func TestServerBitrateCapDirectPlaysSourceWithinLimit(t *testing.T) {
	input := bitratePolicyFixtureV3()
	input.ServerBitrateCapKbps = 10_000
	result := PlanPlaybackV3(input)
	if result.Plan == nil || result.PlayMethod != PlayDirect {
		t.Fatalf("expected direct play: %s", ExplainPlannerResultV3(result))
	}
}

func TestServerBitrateCapTranscodesAndBudgetsAudio(t *testing.T) {
	input := bitratePolicyFixtureV3()
	input.ServerBitrateCapKbps = 4_000
	result := PlanPlaybackV3(input)
	if result.Plan == nil || result.PlayMethod != PlayTranscode {
		t.Fatalf("expected transcode: %s", ExplainPlannerResultV3(result))
	}
	if result.TargetBitrateKbps+result.TargetAudioBitrateKbps > 3_800 {
		t.Fatalf("video/audio targets exceed reserved cap: %d + %d", result.TargetBitrateKbps, result.TargetAudioBitrateKbps)
	}
	lowerClientCap := 2_000
	input.Request.BandwidthCapKbps = &lowerClientCap
	result = PlanPlaybackV3(input)
	if result.Plan == nil || result.TargetBitrateKbps+result.TargetAudioBitrateKbps > 1_900 {
		t.Fatalf("client's lower cap was not honored: %s", ExplainPlannerResultV3(result))
	}
}

func TestServerBitrateCapCountsAudioInSourceTotal(t *testing.T) {
	input := bitratePolicyFixtureV3()
	input.EffectiveFile.Bitrate = 4_200
	input.EffectiveFile.VideoTracks[0].Bitrate = 3_900
	input.ServerBitrateCapKbps = 4_000
	result := PlanPlaybackV3(input)
	if result.Plan == nil || result.PlayMethod != PlayTranscode {
		t.Fatalf("expected total bitrate to require a transcode: %s", ExplainPlannerResultV3(result))
	}
	if result.TargetBitrateKbps+result.TargetAudioBitrateKbps > 3_800 {
		t.Fatalf("video/audio targets exceed reserved cap: %d + %d", result.TargetBitrateKbps, result.TargetAudioBitrateKbps)
	}
}

func TestServerBitrateCapUnknownSourceTotalRequiresTranscode(t *testing.T) {
	input := bitratePolicyFixtureV3()
	input.EffectiveFile.Bitrate = 0
	input.EffectiveFile.VideoTracks[0].Bitrate = 3_900
	input.ServerBitrateCapKbps = 4_000
	result := PlanPlaybackV3(input)
	if result.Plan == nil || result.PlayMethod != PlayTranscode {
		t.Fatalf("expected unknown total bitrate to require a transcode: %s", ExplainPlannerResultV3(result))
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

func TestServerBitrateCapHonorsLowerClientCapAgainstSourceTotal(t *testing.T) {
	input := bitratePolicyFixtureV3()
	input.EffectiveFile.Bitrate = 2_300
	input.EffectiveFile.VideoTracks[0].Bitrate = 1_800
	input.ServerBitrateCapKbps = 4_000
	clientCap := 2_000
	input.Request.BandwidthCapKbps = &clientCap
	result := PlanPlaybackV3(input)
	if result.Plan == nil || result.PlayMethod != PlayTranscode {
		t.Fatalf("expected lower client cap to require a transcode: %s", ExplainPlannerResultV3(result))
	}
	if result.TargetBitrateKbps+result.TargetAudioBitrateKbps > 1_900 {
		t.Fatalf("video/audio targets exceed client's reserved cap: %d + %d", result.TargetBitrateKbps, result.TargetAudioBitrateKbps)
	}
}

func TestServerBitrateCapFailsClosedWithoutTranscode(t *testing.T) {
	input := bitratePolicyFixtureV3()
	input.ServerBitrateCapKbps = 4_000
	delete(input.Request.ClientPlaybackContext.Deliveries, DeliveryClassHLSV3)
	result := PlanPlaybackV3(input)
	if result.Terminal == nil || result.Terminal.Reason != "bitrate_policy_unavailable" {
		t.Fatalf("expected clear bitrate policy terminal: %s", ExplainPlannerResultV3(result))
	}
}

func TestServerBitrateCapTooLowFailsClosed(t *testing.T) {
	input := bitratePolicyFixtureV3()
	input.ServerBitrateCapKbps = 100
	result := PlanPlaybackV3(input)
	if result.Terminal == nil || result.Terminal.Reason != "bitrate_policy_unavailable" {
		t.Fatalf("expected bitrate policy terminal: %s", ExplainPlannerResultV3(result))
	}
}

func TestServerBitrateCapKeepsTransientBlockerRetryable(t *testing.T) {
	input := bitratePolicyFixtureV3()
	input.ServerBitrateCapKbps = 4_000
	input.HLSVideoRegistry = func() *TransformationRegistryV3 {
		return NewTransformationRegistryV3(nil)
	}
	result := PlanPlaybackV3(input)
	if result.Terminal == nil || result.Terminal.Reason != TerminalBitratePolicyUnavailableV3 || !result.Terminal.Retryable {
		t.Fatalf("expected retryable bitrate policy terminal: %s", ExplainPlannerResultV3(result))
	}
}
