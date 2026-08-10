package playback

import (
	"encoding/json"
	"testing"
)

func TestExecutableRecipeV3RoundTripPreservesOperationalFields(t *testing.T) {
	plan := &PlanV3{PlanID: "plan:frozen", Delivery: DeliveryRemuxHLSV3}
	want := PlannerResultV3{
		Plan: plan, PlayMethod: PlayRemux, TranscodeAudio: true,
		TargetVideoCodec: "copy", TargetAudioCodec: "aac", TargetAudioChannels: 6, TargetAudioBitrateKbps: 320,
		TargetResolution: "1080p", TargetBitrateKbps: 18_000,
		FrozenSourceMetadata: &SourceExecutionMetadataV3{VideoCodec: "h264", SoftwareVideoDecode: true, DurationSeconds: 7_201},
		SubtitleTrackIndex:   4, SubtitleTransportTrackIndex: 2,
		SubtitleBurnIn: true, SubtitleCodec: "hdmv_pgs_subtitle", DownloadedSubtitleID: 71,
	}
	recipe := FreezeExecutableRecipeV3(want)
	if !recipe.Valid() {
		t.Fatalf("frozen recipe is invalid: %#v", recipe)
	}
	if !recipe.ValidFor(*plan) {
		t.Fatalf("frozen recipe does not match its plan: %#v", recipe)
	}
	changedPlan := *plan
	changedPlan.PlanID = "plan:newer"
	if recipe.ValidFor(changedPlan) {
		t.Fatal("stale frozen recipe matched a newer plan")
	}
	got := recipe.PlannerResult(plan)
	if got.Plan != plan || got.PlayMethod != want.PlayMethod || got.TranscodeAudio != want.TranscodeAudio ||
		got.TargetVideoCodec != want.TargetVideoCodec || got.TargetAudioCodec != want.TargetAudioCodec ||
		got.TargetAudioChannels != want.TargetAudioChannels || got.TargetAudioBitrateKbps != want.TargetAudioBitrateKbps || got.TargetResolution != want.TargetResolution ||
		got.TargetBitrateKbps != want.TargetBitrateKbps || got.SubtitleTrackIndex != want.SubtitleTrackIndex ||
		got.SubtitleTransportTrackIndex != want.SubtitleTransportTrackIndex || got.SubtitleBurnIn != want.SubtitleBurnIn ||
		got.SubtitleCodec != want.SubtitleCodec || got.DownloadedSubtitleID != want.DownloadedSubtitleID || got.FrozenSourceMetadata == nil ||
		got.FrozenSourceMetadata.VideoCodec != want.FrozenSourceMetadata.VideoCodec || got.FrozenSourceMetadata.SoftwareVideoDecode != want.FrozenSourceMetadata.SoftwareVideoDecode || got.FrozenSourceMetadata.DurationSeconds != want.FrozenSourceMetadata.DurationSeconds {
		t.Fatalf("thawed result = %#v, want %#v", got, want)
	}
}

func TestExecutableRecipeV3SurvivesJSONRoundTrip(t *testing.T) {
	plan := &PlanV3{PlanID: "plan:frozen"}
	recipe := FreezeExecutableRecipeV3(PlannerResultV3{
		Plan: plan, PlayMethod: PlayRemux,
		FrozenSourceMetadata: &SourceExecutionMetadataV3{VideoCodec: "h264", SoftwareVideoDecode: true, DurationSeconds: 7_201},
		SubtitleTrackIndex:   -1, SubtitleTransportTrackIndex: 0,
	})
	recipe.SubtitleSource = SubtitleSourceDownloadedV3
	recipe.DownloadedSubtitleID = 71

	encoded, err := json.Marshal(recipe)
	if err != nil {
		t.Fatalf("marshal recipe: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("unmarshal recipe fields: %v", err)
	}
	for _, field := range []string{"subtitle_track_index", "subtitle_transport_track_index"} {
		if _, ok := fields[field]; !ok {
			t.Fatalf("encoded recipe omitted meaningful zero-value field %q: %s", field, encoded)
		}
	}
	var decoded ExecutableRecipeV3
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal recipe: %v", err)
	}
	if decoded != recipe {
		t.Fatalf("decoded recipe = %#v, want %#v", decoded, recipe)
	}
	if !decoded.ValidFor(*plan) {
		t.Fatalf("decoded recipe no longer matches its plan: %#v", decoded)
	}
}
