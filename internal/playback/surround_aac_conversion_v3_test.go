package playback

import "testing"

// TestPlanPlaybackV3AudioConvertingRemuxKeepsSurround covers a copied video
// whose 5.1 audio the client can't decode: the AAC conversion keeps 5.1, as
// the full transcode does, unless the delivery's ceiling is lower.
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
	ceiling := func(class string, channels int) func(*PlannerInputV3) {
		return func(input *PlannerInputV3) {
			delivery := input.Request.ClientPlaybackContext.Deliveries[class]
			delivery.MaxChannels = &channels
			input.Request.ClientPlaybackContext.Deliveries[class] = delivery
		}
	}
	tests := []struct {
		name     string
		mutate   []func(*PlannerInputV3)
		channels int
	}{
		{name: "progressive DTS 5.1", mutate: []func(*PlannerInputV3){surround("dts")}, channels: 6},
		{name: "progressive DTS 5.1 under a stereo ceiling", mutate: []func(*PlannerInputV3){surround("dts"), ceiling(DeliveryClassProgressiveV3, 2)}, channels: 2},
		{name: "HLS unclaimed E-AC-3 5.1", mutate: []func(*PlannerInputV3){surround("eac3"), hlsOnly}, channels: 6},
		{name: "HLS unclaimed E-AC-3 5.1 under a stereo ceiling", mutate: []func(*PlannerInputV3){surround("eac3"), hlsOnly, ceiling(DeliveryClassHLSV3, 2)}, channels: 2},
		{name: "HLS DTS 5.1", mutate: []func(*PlannerInputV3){surround("dts"), hlsOnly}, channels: 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := bitratePolicyFixtureV3()
			for _, mutate := range tt.mutate {
				mutate(&input)
			}
			result := PlanPlaybackV3(input)
			if result.Plan == nil || result.PlayMethod != PlayRemux || !result.TranscodeAudio || result.TargetVideoCodec != "" && result.TargetVideoCodec != codecCopyV3 {
				t.Fatalf("expected an audio-converting remux: %s", ExplainPlannerResultV3(result))
			}
			if result.TargetAudioChannels != tt.channels || optionalValueV3(result.Plan.EffectiveRecipe.AudioChannels) != tt.channels {
				t.Fatalf("AAC channels = %d (recipe %s), want %d", result.TargetAudioChannels, optionalIntV3(result.Plan.EffectiveRecipe.AudioChannels), tt.channels)
			}
			wantDownmixSource := 0
			if tt.channels == 2 {
				wantDownmixSource = 6
			}
			if result.SourceAudioChannels != wantDownmixSource {
				t.Fatalf("downmix source channels = %d, want %d", result.SourceAudioChannels, wantDownmixSource)
			}
		})
	}
}
