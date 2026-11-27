package playback

import "testing"

// TestPlanPlaybackV3AudioConvertingRemuxKeepsSurround covers a copied video
// whose 5.1 audio the client can't decode: the AAC conversion keeps 5.1, as
// the full transcode does, unless the delivery's ceiling or the server's
// bitrate cap leaves room only for stereo.
func TestPlanPlaybackV3AudioConvertingRemuxKeepsSurround(t *testing.T) {
	surround := func(codec string) func(*PlannerInputV3) {
		return func(input *PlannerInputV3) {
			input.EffectiveFile.CodecAudio, input.EffectiveFile.AudioChannels = codec, 6
			input.EffectiveFile.AudioTracks[0].Codec, input.EffectiveFile.AudioTracks[0].Channels, input.EffectiveFile.AudioTracks[0].Layout = codec, 6, "5.1"
		}
	}
	hlsOnly := func(input *PlannerInputV3) {
		delete(input.Request.ClientPlaybackContext.Deliveries, DeliveryClassOriginalHTTPV3)
		delete(input.Request.ClientPlaybackContext.Deliveries, DeliveryClassProgressiveV3)
	}
	noHLS := func(input *PlannerInputV3) {
		delete(input.Request.ClientPlaybackContext.Deliveries, DeliveryClassHLSV3)
	}
	ceiling := func(class string, channels int) func(*PlannerInputV3) {
		return func(input *PlannerInputV3) {
			delivery := input.Request.ClientPlaybackContext.Deliveries[class]
			delivery.MaxChannels = &channels
			input.Request.ClientPlaybackContext.Deliveries[class] = delivery
		}
	}
	// The 8,000 kbps fixture fits under 8,300 with stereo AAC (192) but not
	// with 5.1 (384).
	serverCap := func(input *PlannerInputV3) { input.ServerBitrateCapKbps = 8_300 }
	tests := []struct {
		name     string
		mutate   []func(*PlannerInputV3)
		delivery DeliveryV3
		channels int
	}{
		{name: "progressive DTS 5.1", mutate: []func(*PlannerInputV3){surround("dts")}, delivery: DeliveryRemuxProgressiveV3, channels: 6},
		{name: "progressive DTS 5.1 under a stereo ceiling", mutate: []func(*PlannerInputV3){surround("dts"), ceiling(DeliveryClassProgressiveV3, 2)}, delivery: DeliveryRemuxProgressiveV3, channels: 2},
		{name: "progressive DTS 5.1 under the server cap", mutate: []func(*PlannerInputV3){surround("dts"), noHLS, serverCap}, delivery: DeliveryRemuxProgressiveV3, channels: 2},
		{name: "HLS unclaimed E-AC-3 5.1", mutate: []func(*PlannerInputV3){surround("eac3"), hlsOnly}, delivery: DeliveryRemuxHLSV3, channels: 6},
		{name: "HLS unclaimed E-AC-3 5.1 under a stereo ceiling", mutate: []func(*PlannerInputV3){surround("eac3"), hlsOnly, ceiling(DeliveryClassHLSV3, 2)}, delivery: DeliveryRemuxHLSV3, channels: 2},
		{name: "HLS unclaimed E-AC-3 5.1 under the server cap", mutate: []func(*PlannerInputV3){surround("eac3"), hlsOnly, serverCap}, delivery: DeliveryRemuxHLSV3, channels: 2},
		{name: "HLS DTS 5.1", mutate: []func(*PlannerInputV3){surround("dts"), hlsOnly}, delivery: DeliveryRemuxHLSV3, channels: 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := bitratePolicyFixtureV3()
			for _, mutate := range tt.mutate {
				mutate(&input)
			}
			result := PlanPlaybackV3(input)
			if result.Plan == nil || result.Plan.Delivery != tt.delivery || !result.TranscodeAudio {
				t.Fatalf("expected an audio-converting %s: %s", tt.delivery, ExplainPlannerResultV3(result))
			}
			assertAACChannelsV3(t, result, tt.channels)
		})
	}
}

func assertAACChannelsV3(t *testing.T, result PlannerResultV3, channels int) {
	t.Helper()
	if result.TargetAudioChannels != channels || optionalValueV3(result.Plan.EffectiveRecipe.AudioChannels) != channels {
		t.Fatalf("AAC channels = %d (recipe %s), want %d", result.TargetAudioChannels, optionalIntV3(result.Plan.EffectiveRecipe.AudioChannels), channels)
	}
	// Only a stereo output takes the surround-to-stereo downmix recipe.
	if downmix := result.SourceAudioChannels > 2; downmix != (channels == 2) {
		t.Fatalf("downmix source channels = %d with %d output channels", result.SourceAudioChannels, channels)
	}
}
