package playback

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// declaredProviderRow is a virtual release as the provider listing declares it,
// before any probe: one 720p H.264 video track and two audio tracks that name a
// language and a channel count but no codec id. This is the shape every
// first play of a virtual row plans from, and the shape a provider rotation can
// put back under a row that already had evidence.
func declaredProviderRow() *models.MediaFile {
	file := detailedFixtureFileV3()
	file.FilePath = "virtual://movie/tt-declared?result=41a38178c18fbc5f90fdb422"
	file.Container = "mkv"
	file.Resolution = "720p"
	file.Bitrate = 4_000
	file.CodecVideo = "h264"
	file.CodecAudio = ""
	file.VideoTracks = []models.VideoTrack{{
		Codec: "h264", Profile: "high", Level: 31, Width: 1280, Height: 720,
		FrameRate: "24000/1001", Bitrate: 4_000, BitDepth: 8,
		VideoRange: "SDR", VideoRangeType: "SDR",
	}}
	file.AudioTracks = []models.AudioTrack{
		{Index: 0, Language: "ita", Channels: 2, Layout: "stereo", Default: true},
		{Index: 1, Language: "eng", Channels: 2, Layout: "stereo"},
	}
	file.SubtitleTracks = nil
	file.ProbeUpdatedAt = nil
	return file
}

// audioCodecEvidencePlanInput is a plan request for the supplied file with the
// audio index the caller names, which is what a failure_recovery replan passes:
// the previous plan's selected audio track is pinned onto the replanned request.
func audioCodecEvidencePlanInput(file *models.MediaFile, audioIndex int) PlannerInputV3 {
	req := validStartRequestV3()
	return PlannerInputV3{
		Request:         req,
		RequestedFile:   file,
		EffectiveFile:   file,
		AudioTrackIndex: audioIndex,
		Settings:        PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true},
		Registry: NewTransformationRegistryV3([]TransformationSpecV3{
			{Name: "video_to_h264", RecipeVersion: "2", Available: true},
			{Name: "audio_to_aac", RecipeVersion: TransformationAudioToAACRecipeVersionV3, Available: true},
		}),
	}
}

// TestPlanPlaybackV3DoesNotConcludeACodecVerdictFromAnUnmeasuredRow pins the
// production regression: a remux delivery timed out at the provider, the client
// replanned with operation=failure_recovery, the replan pinned the previous
// plan's audio index onto a row the provider had re-declared without codec ids,
// and the planner answered audio_conversion_unsupported with retryable=false —
// "None of this file's audio tracks use a codec the server can decode" about a
// file that played fine on the next attempt.
//
// The gate may only publish that verdict from measurement. A row no probe has
// measured plans the way its first play does; whatever the delivery does next
// answers a retryable transport cause instead of a codec verdict.
func TestPlanPlaybackV3DoesNotConcludeACodecVerdictFromAnUnmeasuredRow(t *testing.T) {
	file := declaredProviderRow()
	if file.AudioTracks[0].Codec != "" || file.CodecAudio != "" {
		t.Fatal("fixture declares a codec; it must declare none")
	}
	if AudioCodecVerdictSupported(file) {
		t.Fatal("an unprobed row reported itself as measured audio codec evidence")
	}

	result := PlanPlaybackV3(audioCodecEvidencePlanInput(file, 0))
	if result.Terminal != nil && result.Terminal.Reason == TerminalAudioConversionUnsupportedV3 {
		t.Fatalf("an unmeasured row produced a codec verdict: %s", ExplainPlannerResultV3(result))
	}
	if result.Terminal != nil && !result.Terminal.Retryable {
		t.Fatalf("an unmeasured row produced a non-retryable terminal: %s", ExplainPlannerResultV3(result))
	}
}

// TestPlanPlaybackV3KeepsTheCodecVerdictForAProbedRow pins the other direction:
// when the probe did name the codec and there is no decoder for it (AVS3 audio,
// whose codec id ffprobe cannot print), the refusal is real evidence and stays
// the non-retryable codec verdict. Retrying cannot help; choosing another track
// can.
func TestPlanPlaybackV3KeepsTheCodecVerdictForAProbedRow(t *testing.T) {
	probedAt := time.Now().Add(-time.Hour)
	for _, tc := range []struct {
		name       string
		codecs     []string
		fileCodec  string
		wantReason string
	}{
		{
			name:       "probe could not name the only audio codec",
			codecs:     []string{""},
			fileCodec:  "",
			wantReason: TerminalNoDecodableAudioMessageV3,
		},
		{
			name:       "probe printed unknown for the selected track",
			codecs:     []string{"unknown", "aac"},
			fileCodec:  "aac",
			wantReason: TerminalAudioTrackUndecodableMessageV3,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := declaredProviderRow()
			file.ProbeUpdatedAt = &probedAt
			file.CodecAudio = tc.fileCodec
			tracks := make([]models.AudioTrack, len(tc.codecs))
			for i, codec := range tc.codecs {
				tracks[i] = models.AudioTrack{Index: i, Codec: codec, Channels: 2, Layout: "stereo"}
			}
			file.AudioTracks = tracks
			if !AudioCodecVerdictSupported(file) {
				t.Fatal("a probed row did not report measured audio codec evidence")
			}

			result := PlanPlaybackV3(audioCodecEvidencePlanInput(file, 0))
			if result.Terminal == nil || result.Terminal.Reason != TerminalAudioConversionUnsupportedV3 {
				t.Fatalf("a probed undecodable track = %s, want the codec verdict", ExplainPlannerResultV3(result))
			}
			if result.Terminal.Retryable {
				t.Fatal("a genuine codec incompatibility was reported as retryable")
			}
			if result.Terminal.Message != tc.wantReason {
				t.Fatalf("message = %q, want %q", result.Terminal.Message, tc.wantReason)
			}
		})
	}
}
