package handlers

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestSubtitleArtifactDescribesServedBytesOnResumedTransport(t *testing.T) {
	for _, tc := range []struct{ codec, format, mime string }{
		{"subrip", "vtt", "text/vtt"},
		{"srt", "vtt", "text/vtt"},
		{"mov_text", "vtt", "text/vtt"},
		{"ass", "ass", "text/x-ssa"},
		{"ssa", "ass", "text/x-ssa"},
		{"hdmv_pgs_subtitle", "sup", "application/octet-stream"},
		{"pgssub", "sup", "application/octet-stream"},
	} {
		t.Run(tc.codec, func(t *testing.T) {
			file := &models.MediaFile{ID: 42, SubtitleTracks: []models.SubtitleTrack{{Index: 4, Codec: tc.codec}}}
			plan := &playback.PlanV3{
				Delivery: playback.DeliveryRemuxProgressiveV3,
				Timeline: playback.TimelineV3{StreamOriginSeconds: 600, TimelineOffsetSeconds: 600},
				Subtitle: playback.SubtitleDecisionV3{
					Mode: playback.SubtitleRenderV3, TrackID: playback.TrackIDV3(file.ID, "subtitle", 0),
					Inventory: playback.BuildSubtitleInventoryV3(file, nil),
				},
			}
			handler := &PlaybackHandler{}
			if err := handler.attachSubtitleArtifactV3(t.Context(), "session", file, plan, 0, nil); err != nil {
				t.Fatal(err)
			}
			artifact := plan.Subtitle.Artifact
			if artifact == nil || artifact.Format != tc.format || artifact.MIMEType != tc.mime || artifact.TimingOriginSeconds != 0 {
				t.Fatalf("artifact must describe served format and absolute source timestamps: %#v", artifact)
			}
		})
	}
}

func TestAttachNativeSubtitleValidatesRouteAndIdentity(t *testing.T) {
	for _, tc := range []struct {
		name        string
		delivery    playback.DeliveryV3
		mode        playback.SubtitleModeV3
		streamIndex int
		wantError   bool
	}{
		{"native_original", playback.DeliveryOriginalHTTPV3, playback.SubtitleRenderV3, 4, false},
		{"reject_adapted_native", playback.DeliveryRemuxProgressiveV3, playback.SubtitleRenderV3, 4, true},
		{"reject_converted_native", playback.DeliveryOriginalHTTPV3, playback.SubtitleConvertV3, 4, true},
		{"reject_changed_index", playback.DeliveryOriginalHTTPV3, playback.SubtitleRenderV3, 7, true},
		{"clear_off", playback.DeliveryOriginalHTTPV3, playback.SubtitleOffV3, 4, false},
		{"clear_burned_in", playback.DeliveryTranscodeHLSV3, playback.SubtitleBurnInV3, 4, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := &models.MediaFile{ID: 42, SubtitleTracks: []models.SubtitleTrack{{Index: 4, Codec: "subrip"}}}
			plan := &playback.PlanV3{
				Delivery: tc.delivery,
				Subtitle: playback.SubtitleDecisionV3{
					Mode: tc.mode, TrackID: playback.TrackIDV3(file.ID, "subtitle", 0),
					Embedded:  &playback.EmbeddedSubtitleV3{StreamIndex: tc.streamIndex},
					Artifact:  &playback.SubtitleArtifactV3{URL: "/stale.vtt", Format: "vtt"},
					Inventory: playback.BuildSubtitleInventoryV3(file, nil),
				},
			}
			handler := &PlaybackHandler{}
			err := handler.attachSubtitleArtifactV3(t.Context(), "session", file, plan, 0, nil)
			if (err != nil) != tc.wantError {
				t.Fatalf("attach error=%v, wantError=%v", err, tc.wantError)
			}
			if err == nil && plan.Subtitle.Artifact != nil {
				t.Fatalf("native/off/burned-in route retained stale artifact: %#v", plan.Subtitle)
			}
			if err == nil && tc.mode != playback.SubtitleRenderV3 && plan.Subtitle.Embedded != nil {
				t.Fatalf("off/burned-in route retained native track selection: %#v", plan.Subtitle)
			}
		})
	}
}
