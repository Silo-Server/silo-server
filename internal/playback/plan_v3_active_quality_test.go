package playback

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// activeQualityFixtureV3 is a 1080p SDR H.264 source at 15 Mbps, the shape
// that exposed clients guessing the server's rung from their own request.
func activeQualityFixtureV3() *models.MediaFile {
	return &models.MediaFile{ID: 42, FilePath: "/media/movie.mkv", Container: "mkv", CodecVideo: "h264", CodecAudio: "aac", Resolution: "1080p", Bitrate: 15_000, AudioChannels: 2, VideoTracks: []models.VideoTrack{{Codec: "h264", Profile: "High", Level: 41, Width: 1920, Height: 1080, FrameRate: "24000/1001", Bitrate: 15_000, BitDepth: 8, VideoRange: "SDR", VideoRangeType: "SDR", ColorRange: "tv"}}, AudioTracks: []models.AudioTrack{{Codec: "aac", Channels: 2, Layout: "stereo"}}}
}

func TestPlanPlaybackV3NamesTheActiveQuality(t *testing.T) {
	for _, test := range []struct {
		name            string
		quality         string
		capKbps         int
		serverLimitKbps int
		decoder720p     bool
		width, height   int
		want            string
	}{
		{name: "a requested rung", quality: QualityRung1080pLowV3, want: QualityRung1080pLowV3},
		{name: "a requested rung a cap lowered", quality: QualityRung1080pMediumV3, capKbps: 4_500, want: QualityRung1080pMediumV3},
		{name: "a requested rung under a server limit", quality: QualityRung1080pMediumV3, serverLimitKbps: 4_000, want: QualityRung1080pMediumV3},
		{name: "a requested rung a decoder held to 720p", quality: QualityRung1080pMediumV3, decoder720p: true, want: QualityRung720pMediumV3},
		{name: "a requested rung of a scope film", quality: QualityRung720pMediumV3, width: 1920, height: 800, want: QualityRung720pMediumV3},
		{name: "a requested rung of an ultrawide film", quality: QualityRung1080pMediumV3, width: 2560, height: 1080, want: QualityRung1080pMediumV3},
		{name: "a requested rung of a scope film a decoder held to 720p", quality: QualityRung1080pMediumV3, width: 1920, height: 800, decoder720p: true, want: QualityRung720pMediumV3},
		{name: "auto plays the source", quality: QualityAutoV3, want: ""},
		{name: "a resolution the source fits plays the source", quality: "1080p", want: ""},
		{name: "a lower resolution names its class rung", quality: "720p", want: QualityRung720pMediumV3},
		{name: "a resolution a cap held to 720p", quality: "1080p", capKbps: 3_000, want: QualityRung720pMediumV3},
		{name: "a cap between two rungs", quality: "1080p", capKbps: 1_800, want: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := validStartRequestV3()
			req.QualityPreference = test.quality
			req.Capabilities.CodecsVideo = []string{"h264", "hevc"}
			if test.capKbps > 0 {
				req.BandwidthCapKbps = &test.capKbps
			}
			if test.decoder720p {
				req.Capabilities.CodecsVideo = []string{"h264"}
				req.Capabilities.VideoEvidence = EvidencePlatformAttestedV3
				req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "h264", Hardware: true, MaxWidth: 1280, MaxHeight: 720, MaxFrameRate: 60, BitDepths: []int{8}}}
			}
			file := activeQualityFixtureV3()
			if test.width > 0 {
				file.VideoTracks[0].Width, file.VideoTracks[0].Height = test.width, test.height
			}
			result := PlanPlaybackV3(PlannerInputV3{
				Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
				Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: testTransformationRegistryV3(),
				ServerBitrateCapKbps: test.serverLimitKbps,
			})
			if result.Plan == nil {
				t.Fatalf("result = %s", ExplainPlannerResultV3(result))
			}
			plan := result.Plan
			if plan.ActiveQuality != test.want {
				t.Fatalf("active quality = %q, want %q (delivery %s, recipe %s@%s)", plan.ActiveQuality, test.want,
					plan.Delivery, optionalIntV3(plan.EffectiveRecipe.Height), optionalIntV3(plan.EffectiveRecipe.BitrateKbps))
			}
			if test.want == "" {
				return
			}
			for _, quality := range plan.AvailableQualities {
				if quality.Label == test.want {
					return
				}
			}
			t.Fatalf("active quality %q is not on the menu %#v", test.want, plan.AvailableQualities)
		})
	}
}

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

// A rung the source already fits plays the source, and stays the viewer's pick.
func TestActiveQualityV3RequestedRungTheSourceFits(t *testing.T) {
	plan := PlanV3{
		Delivery: DeliveryOriginalHTTPV3,
		AvailableQualities: []AvailableQualityV3{
			{Label: QualityOriginalV3, Height: 1080, BitrateKbps: 8_000, PreservesSource: true},
			{Label: QualityRung1080pHighV3, DisplayName: "1080p High", Height: 1080, BitrateKbps: 10_000},
		},
	}
	if got := activeQualityV3(StartRequestV3{QualityPreference: QualityRung1080pHighV3}, plan); got != QualityRung1080pHighV3 {
		t.Fatalf("active quality = %q, want %s", got, QualityRung1080pHighV3)
	}
	if got := activeQualityV3(StartRequestV3{QualityPreference: QualityAutoV3}, plan); got != "" {
		t.Fatalf("auto playing the source = %q, want none", got)
	}
}
