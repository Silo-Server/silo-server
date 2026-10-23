package playback

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func hdr10PlusFixtureFileV3() *models.MediaFile {
	file := detailedFixtureFileV3()
	file.VideoTracks[0].ColorTransfer = "smpte2084"
	file.VideoTracks[0].HDR10Plus = true
	file.VideoTracks[0].VideoRangeType = "HDR10Plus"
	return file
}

func hdrRequestV3(hdr *HDRCapabilitiesV3) StartRequestV3 {
	req := validStartRequestV3()
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = hdr
	req.ClientPlaybackContext.Output.HDRDetails = hdr
	return req
}

func planHDRFileV3(req StartRequestV3, file *models.MediaFile) PlannerResultV3 {
	return PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3()})
}

// HDR10+ is HDR10 plus dynamic metadata that an HDR10-only display ignores,
// so for such a client an HDR10+ file must get the result the same file gets
// when its range type is HDR10, on every delivery. A video encode has no
// HDR-preserving recipe, so without a tone-map executor both end in the same
// terminal.
func TestPlanPlaybackV3PlaysHDR10PlusAsHDR10ForHDR10OnlyOutput(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*StartRequestV3)
		want   DeliveryV3
	}{
		{name: "original file", want: DeliveryOriginalHTTPV3},
		{name: "remux", mutate: func(req *StartRequestV3) {
			req.Capabilities.Containers = []string{"mp4"}
			original := req.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3]
			original.Enabled = false
			req.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3] = original
		}, want: DeliveryRemuxProgressiveV3},
		{name: "lower quality", mutate: func(req *StartRequestV3) {
			req.QualityPreference = "480p"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := hdrRequestV3(&HDRCapabilitiesV3{HDR10: true})
			if tc.mutate != nil {
				tc.mutate(&req)
			}
			hdr10File := hdr10PlusFixtureFileV3()
			hdr10File.VideoTracks[0].HDR10Plus = false
			hdr10File.VideoTracks[0].VideoRangeType = "HDR10"
			baseline := planHDRFileV3(req, hdr10File)
			result := planHDRFileV3(req, hdr10PlusFixtureFileV3())
			if baseline.Plan == nil {
				if tc.want != "" || result.Plan != nil || result.Terminal == nil || result.Terminal.Reason != baseline.Terminal.Reason {
					t.Fatalf("HDR10+ result = %s; want the HDR10 result %s", ExplainPlannerResultV3(result), ExplainPlannerResultV3(baseline))
				}
				return
			}
			if tc.want != "" && baseline.Plan.Delivery != tc.want {
				t.Fatalf("HDR10 baseline delivery = %s, want %s", baseline.Plan.Delivery, tc.want)
			}
			if result.Plan == nil {
				t.Fatalf("HDR10+ file has no plan: %s", ExplainPlannerResultV3(result))
			}
			if result.Plan.Delivery != baseline.Plan.Delivery || len(result.Plan.Transformations) != len(baseline.Plan.Transformations) {
				t.Fatalf("HDR10+ plan = %s; want the HDR10 route %s", ExplainPlannerResultV3(result), ExplainPlannerResultV3(baseline))
			}
			if result.Plan.Claims.Video.HDR10Plus || result.Plan.Claims.Video.HDR10 != baseline.Plan.Claims.Video.HDR10 {
				t.Fatalf("HDR10+ video claims = %+v, want the HDR10 baseline's %+v", result.Plan.Claims.Video, baseline.Plan.Claims.Video)
			}
		})
	}
}

func TestPlanPlaybackV3ClaimsHDR10PlusForHDR10PlusOutput(t *testing.T) {
	result := planHDRFileV3(hdrRequestV3(&HDRCapabilitiesV3{HDR10: true, HDR10Plus: true}), hdr10PlusFixtureFileV3())
	if result.Plan == nil || result.Plan.Delivery != DeliveryOriginalHTTPV3 || !result.Plan.Claims.Video.HDR10Plus {
		t.Fatalf("result = %s, want original playback claiming HDR10+", ExplainPlannerResultV3(result))
	}
}

func TestPlanPlaybackV3KeepsHDR10PlusOffSDROutput(t *testing.T) {
	result := planHDRFileV3(hdrRequestV3(nil), hdr10PlusFixtureFileV3())
	if result.Plan != nil && result.Plan.Delivery == DeliveryOriginalHTTPV3 {
		t.Fatalf("result = %s, an SDR output must not receive the HDR10+ original", ExplainPlannerResultV3(result))
	}
}

func TestHDRDetailsSupportHDR10PlusPlanWithinHDR10Limits(t *testing.T) {
	width, height, frameRate, bitrate := 3840, 2160, 24.0, 80_000
	plan := PlanV3{EffectiveRecipe: EffectiveRecipeV3{
		VideoCodec: "hevc", Width: &width, Height: &height, FrameRate: &frameRate,
		BitrateKbps: &bitrate, DynamicRange: DynamicRangeHDR10PlusV3,
	}}
	if !hdrDetailsSupportPlanV3(HDRCapabilitiesV3{HDR10Plus: true}, plan) {
		t.Fatal("an HDR10+ output rejected an HDR10+ plan")
	}
	hdr10 := HDRCapabilitiesV3{HDR10: true, HDR10MaxFrameRate: 24}
	if !hdrDetailsSupportPlanV3(hdr10, plan) {
		t.Fatal("an HDR10 output rejected an HDR10+ plan within its HDR10 limits")
	}
	claimsHDR10Plus := plan
	claimsHDR10Plus.Claims.Video.HDR10Plus = true
	if hdrDetailsSupportPlanV3(hdr10, claimsHDR10Plus) {
		t.Fatal("an HDR10-only delivery accepted a plan that claims HDR10+")
	}
	tooFast := 60.0
	plan.EffectiveRecipe.FrameRate = &tooFast
	if hdrDetailsSupportPlanV3(hdr10, plan) {
		t.Fatal("an HDR10 output accepted an HDR10+ plan above its HDR10 frame-rate ceiling")
	}
	if hdrDetailsSupportPlanV3(HDRCapabilitiesV3{HLG: true}, plan) {
		t.Fatal("an output without HDR10 or HDR10+ accepted an HDR10+ plan")
	}
}

// HDR10+ is HDR10 plus dynamic metadata, so an HDR10+ output's HDR10 ceilings
// bound an HDR10+ source the same way they bound an HDR10 one.
func TestHDR10PlusOutputKeepsHDR10Ceilings(t *testing.T) {
	hdr := HDRCapabilitiesV3{HDR10: true, HDR10Plus: true, HDR10MaxFrameRate: 30}
	width, height, frameRate, bitrate := 3840, 2160, 60.0, 80_000
	plan := PlanV3{EffectiveRecipe: EffectiveRecipeV3{
		VideoCodec: "hevc", Width: &width, Height: &height, FrameRate: &frameRate,
		BitrateKbps: &bitrate, DynamicRange: DynamicRangeHDR10PlusV3,
	}}
	if hdrDetailsSupportPlanV3(hdr, plan) {
		t.Fatal("an HDR10+ output accepted an HDR10+ plan above its HDR10 frame-rate ceiling")
	}

	req := hdrRequestV3(&hdr)
	source := SourceDescriptorV3{DynamicRange: DynamicRangeHDR10PlusV3, HDR10Plus: true, Width: 3840, Height: 2160, FrameRate: 60}
	if eligible, claims := outputRangeEligibleV3(source, req); eligible || claims.HDR10Plus {
		t.Fatalf("eligible = %v, claims = %+v; want a 60 fps HDR10+ source refused by a 30 fps HDR10 ceiling", eligible, claims)
	}
	source.FrameRate = 24
	if eligible, claims := outputRangeEligibleV3(source, req); !eligible || !claims.HDR10Plus {
		t.Fatalf("eligible = %v, claims = %+v; want a 24 fps HDR10+ source to claim HDR10+", eligible, claims)
	}
}
