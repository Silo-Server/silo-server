package playback

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// directPlayEvidenceFileV3 is an ordinary 1080p H.264/AAC Matroska file: the
// shape a browser or a legacy native build over-claims.
func directPlayEvidenceFileV3() *models.MediaFile {
	return &models.MediaFile{
		ID: 42, FilePath: "/media/movie.mkv", Container: "mkv", CodecVideo: "h264", CodecAudio: "aac",
		Resolution: "1080p", Bitrate: 8_000, AudioChannels: 2,
		VideoTracks: []models.VideoTrack{{Codec: "h264", Profile: "High", Level: 41, Width: 1920, Height: 1080, FrameRate: "24", Bitrate: 8_000, BitDepth: 8, VideoRange: "SDR", VideoRangeType: "SDR"}},
		AudioTracks: []models.AudioTrack{{Codec: "aac", Channels: 2, Layout: "stereo"}},
	}
}

// directPlayEvidenceRequestV3 declares both containers and keeps the
// progressive delivery available, so a remux to MP4 is a reachable route.
func directPlayEvidenceRequestV3() StartRequestV3 {
	req := validStartRequestV3()
	req.Capabilities.CodecsVideo = []string{"h264", "hevc"}
	req.Capabilities.CodecsAudio = []string{"aac"}
	req.Capabilities.Containers = []string{"mkv", "mp4"}
	req.Capabilities.MaxResolution = "1080p"
	req.ClientPlaybackContext.Deliveries = map[string]DeliveryCapabilityV3{
		DeliveryClassOriginalHTTPV3: {Enabled: true, SupportedOnDevice: true, Subtitles: DeliverySubtitleCapabilitiesV3{EmbeddedText: true, SidecarText: true}},
		DeliveryClassProgressiveV3:  {Enabled: true, SupportedOnDevice: true},
		DeliveryClassHLSV3:          {Enabled: true, SupportedOnDevice: true},
	}
	return req
}

// TestDeclaredMKVEvidenceTakesARemuxNotDirectPlay is the server-side fallback
// for a client we cannot update: a boolean container claim ("mkv: yes") is not
// proof the player can render the particular Matroska bytes, so the planner
// packages an MP4 remux instead of shipping the source container untouched.
func TestDeclaredMKVEvidenceTakesARemuxNotDirectPlay(t *testing.T) {
	file := directPlayEvidenceFileV3()
	req := directPlayEvidenceRequestV3()
	req.Capabilities.VideoEvidence = EvidenceDeclaredV3
	req.Capabilities.AudioEvidence = EvidenceDeclaredV3

	result := PlanPlaybackV3(PlannerInputV3{
		Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
		Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: testTransformationRegistryV3(),
	})
	if result.Plan == nil {
		t.Fatalf("declared mkv client got no plan: %#v", result.Terminal)
	}
	if result.Plan.Delivery == DeliveryOriginalHTTPV3 || result.PlayMethod == PlayDirect {
		t.Fatalf("declared mkv client got direct play: %s (%s)", result.Plan.Delivery, result.Plan.DecisionReason)
	}
	if result.Plan.Stream.Container == "mkv" {
		t.Fatalf("declared mkv client got the source container: %#v", result.Plan.Stream)
	}
	if result.RouteDowngrade != RouteDowngradeUnverifiedContainerV3 {
		t.Fatalf("route downgrade = %q, want %q", result.RouteDowngrade, RouteDowngradeUnverifiedContainerV3)
	}
}

// TestProbedMKVEvidenceKeepsDirectPlay is the no-regression half of the rule:
// ExoPlayer-class clients report exact decoder facts from the platform, and
// platform_attested Apple clients report the decoder stack. Both keep the
// original container; the known AVPlayer Matroska mis-advertisement is
// corrected by its own quirk, not by the evidence rule.
func TestProbedMKVEvidenceKeepsDirectPlay(t *testing.T) {
	file := directPlayEvidenceFileV3()
	for _, evidence := range []CapabilityEvidenceV3{EvidenceExactV3, EvidencePlatformAttestedV3} {
		req := directPlayEvidenceRequestV3()
		req.Capabilities.VideoEvidence = evidence
		req.Capabilities.AudioEvidence = evidence
		req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{
			Codec: "h264", Profiles: []string{"high"}, Levels: []int{41}, BitDepths: []int{8},
			MaxWidth: 1920, MaxHeight: 1080, MaxFrameRate: 60, MaxBitrateKbps: 20_000, Hardware: true,
		}}

		result := PlanPlaybackV3(PlannerInputV3{
			Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
			Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: testTransformationRegistryV3(),
		})
		if result.Plan == nil || result.Plan.Delivery != DeliveryOriginalHTTPV3 || result.PlayMethod != PlayDirect {
			t.Fatalf("%s evidence lost direct play: %#v", evidence, result)
		}
		if result.RouteDowngrade != "" {
			t.Fatalf("%s evidence recorded a downgrade: %q", evidence, result.RouteDowngrade)
		}
	}
}

// TestDeclaredMP4EvidenceKeepsDirectPlay bounds the rule to the container that
// actually stalled: a declared client playing an MP4 source is untouched.
func TestDeclaredMP4EvidenceKeepsDirectPlay(t *testing.T) {
	file := directPlayEvidenceFileV3()
	file.Container = "mp4"
	file.FilePath = "/media/movie.mp4"
	req := directPlayEvidenceRequestV3()
	req.Capabilities.VideoEvidence = EvidenceDeclaredV3
	req.Capabilities.AudioEvidence = EvidenceDeclaredV3
	req.Capabilities.Containers = []string{"mp4"}
	req.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3] = DeliveryCapabilityV3{
		Enabled: true, SupportedOnDevice: true, Containers: []string{"mp4"},
		Subtitles: DeliverySubtitleCapabilitiesV3{EmbeddedText: true, SidecarText: true},
	}

	result := PlanPlaybackV3(PlannerInputV3{
		Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
		Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: testTransformationRegistryV3(),
	})
	if result.Plan == nil || result.Plan.Delivery != DeliveryOriginalHTTPV3 || result.PlayMethod != PlayDirect {
		t.Fatalf("declared mp4 client lost direct play: %#v", result)
	}
	if result.RouteDowngrade != "" {
		t.Fatalf("declared mp4 client recorded a downgrade: %q", result.RouteDowngrade)
	}
}
