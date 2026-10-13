package playback

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// undecodableAudioFixtureFileV3 mirrors the #2274 file: the first audio track
// is AVS3, which ffprobe's JSON leaves without a codec name, so the scanner
// took the file-level audio codec from the next track.
func undecodableAudioFixtureFileV3() *models.MediaFile {
	file := detailedFixtureFileV3()
	file.CodecAudio = "eac3"
	file.AudioTracks = []models.AudioTrack{
		{Codec: "", Channels: 6, Language: "zh", Default: true},
		{Codec: "eac3", Channels: 6, Layout: "5.1", Language: "zh"},
		{Codec: "aac", Channels: 2, Layout: "stereo", Language: "zh"},
	}
	return file
}

func TestPlanPlaybackV3RefusesUndecodableAudioTrack(t *testing.T) {
	file := undecodableAudioFixtureFileV3()
	req := validStartRequestV3()
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
	input := PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3()}

	result := PlanPlaybackV3(input)
	if result.Terminal == nil || result.Terminal.Reason != TerminalAudioConversionUnsupportedV3 || result.Terminal.Retryable || result.Terminal.Message != TerminalAudioTrackUndecodableMessageV3 {
		t.Fatalf("undecodable track = %s", ExplainPlannerResultV3(result))
	}

	// A server bitrate cap must not rename the refusal after a policy that
	// has nothing to do with it.
	input.ServerBitrateCapKbps = 4_000
	if capped := PlanPlaybackV3(input); capped.Terminal == nil || capped.Terminal.Reason != TerminalAudioConversionUnsupportedV3 || capped.Terminal.Retryable {
		t.Fatalf("capped undecodable track = %s", ExplainPlannerResultV3(capped))
	}
	input.ServerBitrateCapKbps = 0

	input.AudioTrackIndex = 1
	decodable := PlanPlaybackV3(input)
	if decodable.Terminal != nil || decodable.Plan == nil || decodable.Plan.SelectedTracks.Audio == nil || *decodable.Plan.SelectedTracks.Audio.Index != 1 {
		t.Fatalf("decodable track = %s", ExplainPlannerResultV3(decodable))
	}
}

func TestPlanPlaybackV3RefusesFileWithoutDecodableAudio(t *testing.T) {
	file := undecodableAudioFixtureFileV3()
	file.AudioTracks = file.AudioTracks[:1]
	file.CodecAudio = "" // the scanner found no named audio codec to record
	result := PlanPlaybackV3(PlannerInputV3{Request: validStartRequestV3(), RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3()})
	if result.Terminal == nil || result.Terminal.Reason != TerminalAudioConversionUnsupportedV3 || result.Terminal.Retryable || result.Terminal.Message != TerminalNoDecodableAudioMessageV3 {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
}

func TestPlanPlaybackV3RefusesUndecodableAudioOnlyTrack(t *testing.T) {
	file := audioOnlyFixtureFileV3()
	file.AudioTracks[0].Codec = "unknown"
	req := validStartRequestV3()
	req.FileID = file.ID
	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: testTransformationRegistryV3()})
	if result.Terminal == nil || result.Terminal.Reason != TerminalAudioConversionUnsupportedV3 || result.Terminal.Retryable {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
}

// Older rows recorded only the file-level audio codec. Their codec-less tracks
// are missing metadata, not streams FFmpeg cannot decode.
func TestPlanPlaybackV3KeepsTracksWithoutRecordedCodecs(t *testing.T) {
	file := detailedFixtureFileV3()
	file.AudioTracks[0].Codec = ""
	req := validStartRequestV3()
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	req.Capabilities.HDRDetails = &HDRCapabilitiesV3{HDR10: true}
	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3()})
	if result.Terminal != nil || result.Plan == nil {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
	if plan := PlanPreparedTracks(file, "aac", -1); len(plan.Audio) != 1 || !plan.Audio[0].Default {
		t.Fatalf("prepared audio = %+v, want the track kept", plan.Audio)
	}
}

// A file without a completed probe has no audio tracks to judge, so it keeps
// the retryable metadata terminal rather than the decodability refusal.
func TestPlanPlaybackV3UnprobedAudioStaysRetryable(t *testing.T) {
	file := audioOnlyFixtureFileV3()
	file.CodecAudio = ""
	file.AudioTracks = nil
	req := validStartRequestV3()
	req.FileID = file.ID
	result := PlanPlaybackV3(PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0, Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: testTransformationRegistryV3()})
	if result.Terminal == nil || result.Terminal.Reason != "source_metadata_incomplete" || !result.Terminal.Retryable {
		t.Fatalf("result = %s", ExplainPlannerResultV3(result))
	}
}
