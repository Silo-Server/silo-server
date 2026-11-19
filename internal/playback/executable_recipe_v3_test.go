package playback

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/tonemap"
)

// TestExecutableRecipeV3RoundTripPreservesOperationalFields verifies frozen recipes restore all execution inputs.
func TestExecutableRecipeV3RoundTripPreservesOperationalFields(t *testing.T) {
	plan := &PlanV3{PlanID: "plan:frozen", Delivery: DeliveryTranscodeHLSV3}
	revision := tonemap.SourceRevision{MediaFileID: 42, FileSize: 100, FileModifiedUnixNano: 200, StreamSignature: "stream"}
	want := PlannerResultV3{
		Plan: plan, PlayMethod: PlayTranscode, TranscodeAudio: true,
		TargetVideoCodec: "h264", TargetAudioCodec: "aac", SourceAudioChannels: 8, TargetAudioChannels: 6, TargetAudioBitrateKbps: 320,
		TargetResolution: "1080p", TargetBitrateKbps: 18_000,
		ToneMapPolicy: tonemap.PolicyHardwareThenSoftware, ToneMapMode: tonemap.ModeHardware,
		ToneMapSourceKind: tonemap.SourceSDRBT709, ToneMapRecipeVersion: TransformationHDRToSDRToneMapRecipeVersionV3,
		ToneMapPreflightRequired: true, ToneMapSourceRevision: revision,
		FrozenSourceMetadata: &SourceExecutionMetadataV3{VideoCodec: "hevc", VideoProfile: "Main 10", VideoBitDepth: 10, SoftwareVideoDecode: false, DurationSeconds: 7_201, ToneMapSourceKind: tonemap.SourceSDRBT709, ToneMapPreflightRequired: true, ToneMapSourceRevision: revision, ToneMapDVConfigPresent: true, ToneMapDVBLCompatIDPresent: true, ToneMapDVBLPresent: true, ToneMapDVRPUPresent: true},
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
		got.SourceAudioChannels != want.SourceAudioChannels || got.TargetAudioChannels != want.TargetAudioChannels || got.TargetAudioBitrateKbps != want.TargetAudioBitrateKbps || got.TargetResolution != want.TargetResolution ||
		got.TargetBitrateKbps != want.TargetBitrateKbps || got.SubtitleTrackIndex != want.SubtitleTrackIndex ||
		got.ToneMapPolicy != want.ToneMapPolicy || got.ToneMapMode != want.ToneMapMode ||
		got.ToneMapSourceKind != want.ToneMapSourceKind || got.ToneMapRecipeVersion != want.ToneMapRecipeVersion || got.ToneMapPreflightRequired != want.ToneMapPreflightRequired || got.ToneMapSourceRevision != revision ||
		got.SubtitleTransportTrackIndex != want.SubtitleTransportTrackIndex || got.SubtitleBurnIn != want.SubtitleBurnIn ||
		got.SubtitleCodec != want.SubtitleCodec || got.DownloadedSubtitleID != want.DownloadedSubtitleID || got.FrozenSourceMetadata == nil ||
		got.FrozenSourceMetadata.VideoCodec != want.FrozenSourceMetadata.VideoCodec || got.FrozenSourceMetadata.VideoProfile != "Main 10" || got.FrozenSourceMetadata.VideoBitDepth != 10 || got.FrozenSourceMetadata.SoftwareVideoDecode != want.FrozenSourceMetadata.SoftwareVideoDecode ||
		got.FrozenSourceMetadata.DurationSeconds != want.FrozenSourceMetadata.DurationSeconds || got.FrozenSourceMetadata.ToneMapSourceKind != want.FrozenSourceMetadata.ToneMapSourceKind || got.FrozenSourceMetadata.ToneMapPreflightRequired != want.FrozenSourceMetadata.ToneMapPreflightRequired || got.FrozenSourceMetadata.ToneMapSourceRevision != revision || !got.FrozenSourceMetadata.ToneMapDVConfigPresent || !got.FrozenSourceMetadata.ToneMapDVBLCompatIDPresent || !got.FrozenSourceMetadata.ToneMapDVBLPresent || !got.FrozenSourceMetadata.ToneMapDVRPUPresent {
		t.Fatalf("thawed result = %#v, want %#v", got, want)
	}
}

// TestExecutableRecipeV3RejectsToneMapFieldsOnLegacyVersion verifies old recipes cannot carry new tone-map facts.
func TestExecutableRecipeV3RejectsToneMapFieldsOnLegacyVersion(t *testing.T) {
	recipe := ExecutableRecipeV3{Version: executableRecipeVersionLegacyV3, PlanID: "plan:legacy", PlayMethod: PlayTranscode}
	if !recipe.Valid() {
		t.Fatal("non-tone-mapped version 1 recipe should remain valid")
	}
	recipe.ToneMapMode = tonemap.ModeSoftware
	if recipe.Valid() {
		t.Fatal("version 1 recipe accepted tone-map execution fields")
	}
	recipe.ToneMapMode = ""
	recipe.ToneMapDVConfigPresent = true
	if recipe.Valid() {
		t.Fatal("version 1 recipe accepted additive Dolby presence fields")
	}
}

// TestExecutableRecipeV3AllowsDolbyPresenceMetadataOnSourcePreservingRoutes verifies direct routes retain source facts.
func TestExecutableRecipeV3AllowsDolbyPresenceMetadataOnSourcePreservingRoutes(t *testing.T) {
	for _, method := range []PlayMethod{PlayDirect, PlayRemux} {
		t.Run(string(method), func(t *testing.T) {
			recipe := ExecutableRecipeV3{
				Version: executableRecipeVersionV3, PlanID: "plan:source-preserving", PlayMethod: method,
				ToneMapDVConfigPresent: true, ToneMapDVBLCompatIDPresent: true,
				ToneMapDVBLPresent: true, ToneMapDVRPUPresent: true,
			}
			if !recipe.Valid() {
				t.Fatalf("source-preserving recipe rejected Dolby source metadata: %#v", recipe)
			}
		})
	}
}

// TestExecutableRecipeV3RejectsIncompleteOrContradictoryToneMapRecipe verifies frozen recipes are internally consistent.
func TestExecutableRecipeV3RejectsIncompleteOrContradictoryToneMapRecipe(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ExecutableRecipeV3)
	}{
		{name: "policy without mode", mutate: func(recipe *ExecutableRecipeV3) { recipe.ToneMapMode = "" }},
		{name: "mode not allowed by policy", mutate: func(recipe *ExecutableRecipeV3) { recipe.ToneMapPolicy = tonemap.PolicySoftwareOnly }},
		{name: "missing source kind", mutate: func(recipe *ExecutableRecipeV3) { recipe.ToneMapSourceKind = "" }},
		{name: "missing source revision", mutate: func(recipe *ExecutableRecipeV3) { recipe.ToneMapSourceRevision = tonemap.SourceRevision{} }},
		{name: "stale transformation recipe", mutate: func(recipe *ExecutableRecipeV3) { recipe.ToneMapRecipeVersion = "0" }},
		{name: "non-transcode play method", mutate: func(recipe *ExecutableRecipeV3) { recipe.PlayMethod = PlayRemux }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recipe := FreezeExecutableRecipeV3(PlannerResultV3{
				Plan: &PlanV3{PlanID: "plan-1"}, PlayMethod: PlayTranscode,
				ToneMapPolicy: tonemap.PolicyHardwareOnly, ToneMapMode: tonemap.ModeHardware,
				ToneMapSourceKind: tonemap.SourcePQ, ToneMapRecipeVersion: TransformationHDRToSDRToneMapRecipeVersionV3,
				ToneMapSourceRevision: tonemap.SourceRevision{MediaFileID: 1, FileSize: 1, StreamSignature: "stream"},
			})
			tt.mutate(&recipe)
			if recipe.Valid() {
				t.Fatal("invalid frozen tone-map recipe was accepted")
			}
		})
	}
}
