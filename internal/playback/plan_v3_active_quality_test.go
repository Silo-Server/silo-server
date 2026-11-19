package playback

import (
	"testing"
)

// The lower version's plain resolution entry means "play that version
// unchanged": it is active while that version plays as-is, requested or not,
// and an encode of that version names the encode's rung instead.
func TestActiveQualityV3LowerVersionEntryNeedsAnUnchangedPlay(t *testing.T) {
	height, bitrate := 720, 2_000
	plan := PlanV3{
		RequestedMediaFileID: 42,
		EffectiveMediaFileID: 84,
		Source:               SourceDescriptorV3{Height: 1080},
		AvailableQualities: []AvailableQualityV3{
			{Label: QualityOriginalV3, Height: 2160, BitrateKbps: 60_000, PreservesSource: true},
			{Label: "1080p", DisplayName: "1080p", Height: 1080, BitrateKbps: 12_000},
			{Label: QualityRung720pMediumV3, DisplayName: "720p Medium", Height: 720, BitrateKbps: 2_000},
		},
	}

	plan.Delivery = DeliveryOriginalHTTPV3
	for _, preference := range []string{"1080p", QualityAutoV3} {
		if got := activeQualityV3(StartRequestV3{QualityPreference: preference}, plan); got != "1080p" {
			t.Fatalf("%s playing the lower version unchanged = %q, want 1080p", preference, got)
		}
	}

	plan.Delivery = DeliveryTranscodeHLSV3
	plan.EffectiveRecipe.Width, plan.EffectiveRecipe.Height, plan.EffectiveRecipe.BitrateKbps = intPointerV3(1280), &height, &bitrate
	if got := activeQualityV3(StartRequestV3{QualityPreference: "1080p"}, plan); got != QualityRung720pMediumV3 {
		t.Fatalf("encoded lower version = %q, want %s", got, QualityRung720pMediumV3)
	}
}
