package playback

import (
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

// qualityReasonsRequestV3 plays activeQualityFixtureV3 as it is: a client
// that declares H.264, AAC and Matroska.
func qualityReasonsRequestV3() StartRequestV3 {
	req := validStartRequestV3()
	req.QualityPreference = QualityAutoV3
	req.Capabilities.VideoEvidence = EvidenceDeclaredV3
	req.Capabilities.AudioEvidence = EvidenceDeclaredV3
	req.Capabilities.CodecsVideo = []string{"h264"}
	req.Capabilities.MaxResolution = ""
	return req
}

// qualityReasonCaseV3 plans activeQualityFixtureV3 after edit adjusts the
// request and source. requested, when set, is the version the viewer asked
// for while the edited source plays.
type qualityReasonCaseV3 struct {
	name      string
	edit      func(req *StartRequestV3, file *models.MediaFile)
	requested *models.MediaFile
	serverCap int
	toneMap   bool
	// learnedLimit stood in for the estimate; limitCleared marks one the
	// handler found lowered nothing.
	learnedLimit *BandwidthLimitV3
	limitCleared bool
	want         []QualityReasonV3
}

var qualityReasonsLimitV3 = &BandwidthLimitV3{Kbps: 5_000, Source: BandwidthEstimateObservedRebufferV3, ExpiresAt: "2026-10-23T12:00:00Z"}

var qualityReasonCasesV3 = []qualityReasonCaseV3{
	{name: "original direct play", want: []QualityReasonV3{{Code: QualityReasonOriginalV3}}},
	{name: "a container the device cannot open", edit: func(req *StartRequestV3, _ *models.MediaFile) {
		req.Capabilities.Containers = []string{"mp4"}
	}, want: []QualityReasonV3{{Code: QualityReasonOriginalV3}, {Code: QualityReasonContainerUnsupportedV3}}},
	{name: "original with its audio converted", edit: func(_ *StartRequestV3, file *models.MediaFile) {
		file.CodecAudio, file.AudioChannels = "truehd", 6
		file.AudioTracks = []models.AudioTrack{{Codec: "truehd", Channels: 6, Layout: "5.1"}}
	}, want: []QualityReasonV3{{Code: QualityReasonOriginalV3}, {Code: QualityReasonAudioConvertedV3, From: "truehd", To: "aac", Channels: 2}}},
	{name: "a same-codec downmix", edit: func(req *StartRequestV3, file *models.MediaFile) {
		hevcSource(file)
		file.AudioChannels, file.AudioTracks[0].Channels, file.AudioTracks[0].Layout = 6, 6, "5.1"
		hls := req.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3]
		hls.MaxChannels = new(2)
		req.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3] = hls
	}, want: []QualityReasonV3{{Code: QualityReasonVideoCodecUnsupportedV3, From: "hevc"}, {Code: QualityReasonAudioConvertedV3, From: "aac", To: "aac", Channels: 2}}},
	{name: "a fixed rung", edit: func(req *StartRequestV3, _ *models.MediaFile) {
		req.QualityPreference = QualityRung720pMediumV3
	}, want: []QualityReasonV3{{Code: QualityReasonUserSelectedV3}}},
	{name: "a fixed rung a cap lowered", edit: func(req *StartRequestV3, _ *models.MediaFile) {
		req.QualityPreference = QualityRung1080pMediumV3
		req.BandwidthCapKbps = new(4_500)
	}, want: []QualityReasonV3{{Code: QualityReasonUserSelectedV3}, {Code: QualityReasonClientCapV3, Kbps: 4_500}}},
	// A version pin is not a quality pick.
	{name: "a pinned version played as it is", edit: func(req *StartRequestV3, _ *models.MediaFile) {
		req.AllowAlternateVersions = new(false)
	}, want: []QualityReasonV3{{Code: QualityReasonOriginalV3}}},
	{name: "a pinned version over the estimate", edit: func(req *StartRequestV3, _ *models.MediaFile) {
		req.AllowAlternateVersions = new(false)
		req.BandwidthEstimateKbps = new(5_100)
	}, want: []QualityReasonV3{{Code: QualityReasonNetworkEstimateV3, Kbps: 5_100}}},
	{name: "the request's cap", edit: func(req *StartRequestV3, _ *models.MediaFile) {
		req.BandwidthCapKbps = new(3_000)
	}, want: []QualityReasonV3{{Code: QualityReasonClientCapV3, Kbps: 3_000}}},
	{name: "the administrator's cap", serverCap: 4_000, want: []QualityReasonV3{{Code: QualityReasonServerCapV3, Kbps: 4_000}}},
	{name: "the request's cap under the administrator's", serverCap: 4_000, edit: func(req *StartRequestV3, _ *models.MediaFile) {
		req.BandwidthCapKbps = new(3_000)
	}, want: []QualityReasonV3{{Code: QualityReasonClientCapV3, Kbps: 3_000}}},
	{name: "the administrator's cap under the request's", serverCap: 4_000, edit: func(req *StartRequestV3, _ *models.MediaFile) {
		req.BandwidthCapKbps = new(5_000)
	}, want: []QualityReasonV3{{Code: QualityReasonServerCapV3, Kbps: 4_000}}},
	// The source's video fits the cap, but its total rate is unknown, so
	// the cap alone forces the encode.
	{name: "the administrator's cap over a source of unknown rate", serverCap: 20_000, edit: func(_ *StartRequestV3, file *models.MediaFile) {
		file.Bitrate = 0
	}, want: []QualityReasonV3{{Code: QualityReasonServerCapV3, Kbps: 20_000}}},
	// The estimate lowered the encode far below the administrator's cap,
	// so the cap decided nothing.
	{name: "the administrator's cap over a tighter estimate", serverCap: 8_000, edit: func(req *StartRequestV3, _ *models.MediaFile) {
		req.BandwidthEstimateKbps = new(3_000)
	}, want: []QualityReasonV3{{Code: QualityReasonNetworkEstimateV3, Kbps: 3_000}}},
	{name: "the administrator's cap over a learned limit", serverCap: 8_000, learnedLimit: &BandwidthLimitV3{Kbps: 3_000, ExpiresAt: qualityReasonsLimitV3.ExpiresAt}, edit: func(req *StartRequestV3, _ *models.MediaFile) {
		req.BandwidthEstimateKbps = new(3_000)
	}, want: []QualityReasonV3{{Code: QualityReasonNetworkLearnedLimitV3, Kbps: 3_000, ExpiresAt: qualityReasonsLimitV3.ExpiresAt}}},
	{name: "the estimate", edit: func(req *StartRequestV3, _ *models.MediaFile) {
		req.BandwidthEstimateKbps = new(5_100)
	}, want: []QualityReasonV3{{Code: QualityReasonNetworkEstimateV3, Kbps: 5_100}}},
	{name: "a learned limit", learnedLimit: qualityReasonsLimitV3, edit: func(req *StartRequestV3, _ *models.MediaFile) {
		req.BandwidthEstimateKbps = new(qualityReasonsLimitV3.Kbps)
	}, want: []QualityReasonV3{{Code: QualityReasonNetworkLearnedLimitV3, Kbps: qualityReasonsLimitV3.Kbps, ExpiresAt: qualityReasonsLimitV3.ExpiresAt}}},
	// The codec forced the encode and the limit lowered nothing, so its
	// figure is not named as a measured estimate.
	{name: "a learned limit that lowered nothing", learnedLimit: qualityReasonsLimitV3, limitCleared: true, edit: func(req *StartRequestV3, file *models.MediaFile) {
		hevcSource(file)
		req.BandwidthEstimateKbps = new(qualityReasonsLimitV3.Kbps)
	}, want: []QualityReasonV3{{Code: QualityReasonVideoCodecUnsupportedV3, From: "hevc"}}},
	{name: "a learned limit that alone lowered nothing", learnedLimit: qualityReasonsLimitV3, limitCleared: true, edit: func(req *StartRequestV3, _ *models.MediaFile) {
		req.BandwidthEstimateKbps = new(qualityReasonsLimitV3.Kbps)
	}, want: []QualityReasonV3{{Code: QualityReasonCompatibilityConversionV3}}},
	// The cap is tighter than the estimate, so it decides the quality.
	{name: "a cap tighter than the estimate", edit: func(req *StartRequestV3, _ *models.MediaFile) {
		req.BandwidthEstimateKbps = new(10_000)
		req.BandwidthCapKbps = new(2_000)
	}, want: []QualityReasonV3{{Code: QualityReasonClientCapV3, Kbps: 2_000}, {Code: QualityReasonNetworkEstimateV3, Kbps: 10_000}}},
	{name: "the device's maximum resolution", edit: func(req *StartRequestV3, _ *models.MediaFile) {
		req.Capabilities.MaxResolution = "720p"
	}, want: []QualityReasonV3{{Code: QualityReasonDeviceResolutionLimitV3, Height: 720}}},
	{name: "an attested decoder's maximum resolution", edit: func(req *StartRequestV3, file *models.MediaFile) {
		hevcSource(file)
		req.Capabilities.VideoEvidence = EvidencePlatformAttestedV3
		req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "h264", Hardware: true, MaxWidth: 1280, MaxHeight: 720, MaxFrameRate: 60, BitDepths: []int{8}}}
	}, want: []QualityReasonV3{{Code: QualityReasonDeviceResolutionLimitV3, Height: 720}, {Code: QualityReasonVideoCodecUnsupportedV3, From: "hevc"}}},
	// A conversion at the source's own size that fits the estimate is
	// the codec's doing alone.
	{name: "a codec the device cannot decode", edit: func(req *StartRequestV3, file *models.MediaFile) {
		hevcSource(file)
		req.BandwidthEstimateKbps = new(100_000)
	}, want: []QualityReasonV3{{Code: QualityReasonVideoCodecUnsupportedV3, From: "hevc"}}},
	{name: "a codec the device cannot decode over the estimate", edit: func(req *StartRequestV3, file *models.MediaFile) {
		hevcSource(file)
		req.BandwidthEstimateKbps = new(5_000)
	}, want: []QualityReasonV3{{Code: QualityReasonNetworkEstimateV3, Kbps: 5_000}, {Code: QualityReasonVideoCodecUnsupportedV3, From: "hevc"}}},
	{name: "a codec and a range the device cannot play", toneMap: true, edit: func(_ *StartRequestV3, file *models.MediaFile) {
		hevcSource(file)
		file.VideoTracks[0].BitDepth, file.VideoTracks[0].Profile = 10, "Main 10"
		file.VideoTracks[0].VideoRange, file.VideoTracks[0].VideoRangeType = "HDR", "HDR10"
		file.VideoTracks[0].ColorPrimaries, file.VideoTracks[0].ColorTransfer, file.VideoTracks[0].ColorSpace = "bt2020", "smpte2084", "bt2020nc"
	}, want: []QualityReasonV3{{Code: QualityReasonVideoCodecUnsupportedV3, From: "hevc"}, {Code: QualityReasonDynamicRangeConvertedV3, From: "hdr10", To: "sdr"}}},
	{name: "a burned-in subtitle", edit: func(req *StartRequestV3, file *models.MediaFile) {
		burnSubtitle(req, file)
	}, want: []QualityReasonV3{{Code: QualityReasonSubtitleBurnInV3}}},
	{name: "a burned-in subtitle over the estimate", edit: func(req *StartRequestV3, file *models.MediaFile) {
		burnSubtitle(req, file)
		req.BandwidthEstimateKbps = new(5_000)
	}, want: []QualityReasonV3{{Code: QualityReasonNetworkEstimateV3, Kbps: 5_000}, {Code: QualityReasonSubtitleBurnInV3}}},
	{name: "a source whose video cannot be copied", edit: func(req *StartRequestV3, file *models.MediaFile) {
		req.Capabilities.Containers = []string{"mp4"}
		file.VideoTracks[0].VideoCopyUnsafe = true
	}, want: []QualityReasonV3{{Code: QualityReasonCompatibilityConversionV3}}},
	// The requested 4K version is over the estimate's budget; the 1080p
	// version that plays fits it.
	{name: "another version under the estimate", requested: fourKVersionV3(), edit: func(req *StartRequestV3, _ *models.MediaFile) {
		req.BandwidthEstimateKbps = new(20_000)
	}, want: []QualityReasonV3{{Code: QualityReasonNetworkEstimateV3, Kbps: 20_000}, {Code: QualityReasonAlternateVersionV3, MediaFileID: 42}}},
	{name: "another version under a learned limit", requested: fourKVersionV3(), learnedLimit: &BandwidthLimitV3{Kbps: 20_000, ExpiresAt: qualityReasonsLimitV3.ExpiresAt}, edit: func(req *StartRequestV3, _ *models.MediaFile) {
		req.BandwidthEstimateKbps = new(20_000)
	}, want: []QualityReasonV3{{Code: QualityReasonNetworkLearnedLimitV3, Kbps: 20_000, ExpiresAt: qualityReasonsLimitV3.ExpiresAt}, {Code: QualityReasonAlternateVersionV3, MediaFileID: 42}}},
	// Resuming a two-part title in its second part: part 1 of the
	// requested version is the request, part 2 plays.
	{name: "the requested version's next part", requested: multipartV3(41, 1, 7), edit: func(_ *StartRequestV3, file *models.MediaFile) {
		*file = *multipartV3(42, 2, 7)
	}, want: []QualityReasonV3{{Code: QualityReasonOriginalV3}}},
	{name: "another version's next part", requested: multipartV3(41, 1, 7), edit: func(_ *StartRequestV3, file *models.MediaFile) {
		*file = *multipartV3(52, 2, 8)
	}, want: []QualityReasonV3{{Code: QualityReasonAlternateVersionV3, MediaFileID: 52}}},
	// A version chosen after the requested one was refused has no limit
	// to name.
	{name: "another version for a refusal", requested: fourKVersionV3(),
		want: []QualityReasonV3{{Code: QualityReasonAlternateVersionV3, MediaFileID: 42}}},
}

func TestPlanPlaybackV3QualityReasons(t *testing.T) {
	for _, test := range qualityReasonCasesV3 {
		t.Run(test.name, func(t *testing.T) {
			req := qualityReasonsRequestV3()
			file := activeQualityFixtureV3()
			if test.edit != nil {
				test.edit(&req, file)
			}
			requested := file
			if test.requested != nil {
				requested = test.requested
			}
			input := PlannerInputV3{
				Request: req, RequestedFile: requested, EffectiveFile: file, AudioTrackIndex: 0,
				Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: testTransformationRegistryV3(),
				ServerBitrateCapKbps: test.serverCap,
			}
			if test.toneMap {
				input.Settings.SoftwareToneMapEnabled = true
				input.Registry = toneMapTestRegistryV3()
				input.ToneMapCapabilities = tonemap.Capabilities{{Mode: tonemap.ModeSoftware, Backend: tonemap.BackendSoftware, Filter: tonemap.SoftwareFilterBT2390, SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ}}}
			}
			result := PlanPlaybackV3(input)
			if result.Plan == nil {
				t.Fatalf("result = %s", ExplainPlannerResultV3(result))
			}
			if !test.limitCleared {
				result.Plan.BandwidthLimit = test.learnedLimit
			}
			ApplyLearnedLimitReasonV3(result.Plan, test.learnedLimit)
			if got := result.Plan.QualityReasons; !reflect.DeepEqual(got, test.want) {
				t.Fatalf("quality reasons = %+v, want %+v (delivery %s, decision %s)", got, test.want, result.Plan.Delivery, result.Plan.DecisionReason)
			}
		})
	}
}

// hevcSource makes activeQualityFixtureV3 an HEVC source.
func hevcSource(file *models.MediaFile) {
	file.CodecVideo = "hevc"
	file.VideoTracks[0].Codec, file.VideoTracks[0].Profile = "hevc", "Main"
}

// burnSubtitle selects an embedded PGS track the client cannot render.
func burnSubtitle(req *StartRequestV3, file *models.MediaFile) {
	file.SubtitleTracks = []models.SubtitleTrack{{Index: 0, Codec: "hdmv_pgs_subtitle"}}
	req.SubtitleTrackIndex = new(0)
	req.SubtitleTrackID = TrackIDV3(file.ID, "subtitle", 0)
}

// fourKVersionV3 is a 4K version of activeQualityFixtureV3's title at 60 Mbps.
func fourKVersionV3() *models.MediaFile {
	file := activeQualityFixtureV3()
	file.ID, file.Resolution, file.Bitrate = 41, "2160p", 60_000
	file.VideoTracks[0].Width, file.VideoTracks[0].Height, file.VideoTracks[0].Bitrate = 3840, 2160, 60_000
	return file
}

// multipartV3 is part of a two-part version of activeQualityFixtureV3's
// title, kept in folder.
func multipartV3(id, part, folder int) *models.MediaFile {
	file := activeQualityFixtureV3()
	file.ID, file.MediaFolderID = id, folder
	file.PresentationGroupKey, file.PresentationPartIndex, file.PresentationPartTotal = "movie", part, 2
	return file
}
