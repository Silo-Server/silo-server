package playback

import (
	"encoding/json"
	"strings"
	"testing"
)

func remuxVideoBitstreamFilterArg(args []string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-bsf:v" {
			return args[i+1]
		}
	}
	return ""
}

func TestBuildRemuxArgsDropsLeadingPicturesOnlyForSeekedVideo(t *testing.T) {
	for _, test := range []struct {
		name      string
		seek      float64
		audioOnly bool
		drop      bool
		dvProfile int
		want      string
	}{
		{name: "seeked copy", seek: 1_234.5, drop: true, want: ResumeLeadingPictureDropBitstreamFilter},
		{name: "start at zero has no leading pictures", seek: 0, drop: true, want: ""},
		{name: "not requested", seek: 1_234.5, drop: false, want: ""},
		{name: "audio only", seek: 1_234.5, drop: true, audioOnly: true, want: ""},
		{name: "chained ahead of the DV7 strip", seek: 1_234.5, drop: true, dvProfile: 7, want: ResumeLeadingPictureDropBitstreamFilter + "," + DV7ToHDR10BitstreamFilter},
		{name: "DV7 strip alone at zero", seek: 0, drop: true, dvProfile: 7, want: DV7ToHDR10BitstreamFilter},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := buildRemuxArgsWithLeadingPictureDropV3("/x.mkv", "mp4", test.seek, false, -1, test.dvProfile, false, test.audioOnly, 0, 0, 0, test.drop)
			if got := remuxVideoBitstreamFilterArg(args); got != test.want {
				t.Fatalf("-bsf:v = %q, want %q (args=%s)", got, test.want, strings.Join(args, " "))
			}
		})
	}
}

func TestPlanPlaybackV3RequestsLeadingPictureDropOnlyForMacOSFirefoxHEVCProgressive(t *testing.T) {
	const macFirefox = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:153.0) Gecko/20100101 Firefox/153.0"
	registry := NewTransformationRegistryV3([]TransformationSpecV3{
		{Name: TransformationAudioToAACV3, RecipeVersion: "1", Available: true, ValidatedClaims: []string{ClaimAudioDecodeV3}},
	})
	plan := func(userAgent, videoCodec string, start float64) PlannerResultV3 {
		file := detailedFixtureFileV3()
		file.VideoTracks[0].Codec = videoCodec
		file.CodecVideo = videoCodec
		file.VideoTracks[0].Profile = "Main"
		file.VideoTracks[0].BitDepth = 8
		file.VideoTracks[0].VideoRange = "SDR"
		file.VideoTracks[0].VideoRangeType = "SDR"
		request := validStartRequestV3()
		request.ClientPlaybackContext.FormFactor = "desktop"
		request.ClientPlaybackContext.Device = DeviceContextV3{Platform: "web", PlatformDetails: map[string]string{"user_agent": userAgent}}
		request.Capabilities.VideoEvidence = EvidenceDeclaredV3
		request.Capabilities.AudioEvidence = EvidenceDeclaredV3
		request.Capabilities.Containers = []string{"mp4"}
		request.ClientPlaybackContext.Deliveries = map[string]DeliveryCapabilityV3{
			DeliveryClassProgressiveV3: {Enabled: true, SupportedOnDevice: true, Containers: []string{"mp4"}, VideoCodecs: []string{videoCodec}, AudioDecodeCodecs: []string{"aac"}},
		}
		request.StartPosition = floatPointerV3(start)
		return PlanPlaybackV3(PlannerInputV3{Request: request, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: registry})
	}

	for _, start := range []float64{0, 1_234.5} {
		result := plan(macFirefox, "hevc", start)
		if result.Plan == nil || result.Plan.Delivery != DeliveryRemuxProgressiveV3 {
			t.Fatalf("start %v: %s", start, ExplainPlannerResultV3(result))
		}
		// The flag is frozen for the whole session: a later seek in the same
		// remux needs it even when playback started at zero.
		if !result.RemuxResumeLeadingPictureDrop {
			t.Fatalf("start %v: leading-picture drop not requested for macOS Firefox HEVC", start)
		}
		// Best effort means the plan never names it as a required server
		// transformation, so it cannot narrow the executor pool.
		for _, transformation := range result.Plan.Transformations {
			if strings.Contains(transformation.Name, "leading") {
				t.Fatalf("start %v: leading-picture drop leaked into plan transformations: %#v", start, result.Plan.Transformations)
			}
		}
	}

	for _, test := range []struct {
		name, userAgent, codec string
	}{
		{name: "Windows Firefox", userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:153.0) Gecko/20100101 Firefox/153.0", codec: "hevc"},
		{name: "macOS Safari", userAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/19.0 Safari/605.1.15", codec: "hevc"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := plan(test.userAgent, test.codec, 1_234.5)
			if test.codec == "hevc" && (result.Plan == nil || result.Plan.Delivery != DeliveryRemuxProgressiveV3) {
				t.Fatalf("%s", ExplainPlannerResultV3(result))
			}
			if result.RemuxResumeLeadingPictureDrop {
				t.Fatalf("leading-picture drop requested outside macOS Firefox HEVC: %s", ExplainPlannerResultV3(result))
			}
		})
	}
}

func TestLeadingPictureDropScopeIsHEVCOnly(t *testing.T) {
	request := validStartRequestV3()
	request.ClientPlaybackContext.Device = DeviceContextV3{Platform: "web", PlatformDetails: map[string]string{
		"user_agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:153.0) Gecko/20100101 Firefox/153.0",
	}}
	for codec, want := range map[string]bool{"hevc": true, "h265": true, "h264": false, "av1": false} {
		if got := firefoxMacOSHEVCResumeLeadingPictureDropV3(SourceDescriptorV3{VideoCodec: codec}, request); got != want {
			t.Errorf("codec %s: drop = %v, want %v", codec, got, want)
		}
	}
}

// Pin the exact argv token: Quick104 validated this string against real
// open-GOP HEVC media, including the escaped comma when it is chained with
// dovi_rpu=strip=1.
func TestResumeLeadingPictureDropFilterString(t *testing.T) {
	if ResumeLeadingPictureDropBitstreamFilter != `noise=drop=lt(pts\,startpts)*not(key)` {
		t.Fatalf("filter = %q", ResumeLeadingPictureDropBitstreamFilter)
	}
}

func TestRemuxLeadingPictureDropSurvivesTokenAndRecipeRoundTrips(t *testing.T) {
	card := NewRemuxRecipeCard("session-1", 42, "profile-1", 77, false, 0)
	card.RemuxResumeLeadingPictureDrop = true
	claims := card.ToClaims()
	if !claims.RemuxResumeLeadingPictureDrop {
		t.Fatal("recipe card did not carry the leading-picture drop into stream claims")
	}
	encoded, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"rlpd":true`) {
		t.Fatalf("claims JSON = %s, want rlpd", encoded)
	}
	if restored := RecipeCardFromClaims(&claims); !restored.RemuxResumeLeadingPictureDrop {
		t.Fatal("stream claims did not restore the leading-picture drop")
	}

	plan := &PlanV3{PlanID: "plan:remux", Delivery: DeliveryRemuxProgressiveV3}
	recipe := FreezeExecutableRecipeV3(PlannerResultV3{Plan: plan, PlayMethod: PlayRemux, RemuxResumeLeadingPictureDrop: true})
	if !recipe.Valid() {
		t.Fatalf("frozen remux recipe is invalid: %#v", recipe)
	}
	if !recipe.PlannerResult(plan).RemuxResumeLeadingPictureDrop {
		t.Fatal("a seek reanchor from the frozen recipe lost the leading-picture drop")
	}
}

func TestSessionStreamStateCarriesLeadingPictureDrop(t *testing.T) {
	session := &Session{RemuxResumeLeadingPictureDrop: true}
	state := snapshotSessionStreamStateLocked(session)
	if !state.RemuxResumeLeadingPictureDrop {
		t.Fatal("stream state snapshot dropped the leading-picture drop")
	}
	restored := &Session{}
	restoreSessionStreamStateLocked(restored, state)
	if !restored.RemuxResumeLeadingPictureDrop {
		t.Fatal("stream state restore dropped the leading-picture drop")
	}
}
