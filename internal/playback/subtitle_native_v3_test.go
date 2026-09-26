package playback

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestNativeEmbeddedSubtitleV3RequiresExactNegotiatedRoute(t *testing.T) {
	for _, tc := range []struct {
		name       string
		change     func(*models.MediaFile, *StartRequestV3, *string)
		wantNative bool
	}{
		{name: "exact FFmpeg index", wantNative: true},
		{name: "container ID", wantNative: true, change: func(f *models.MediaFile, r *StartRequestV3, d *string) {
			r.ClientPlaybackContext.Deliveries[*d].Subtitles.NativeEmbedded[0].TrackIdentity = "container_track_id"
		}},
		{name: "feature absent", change: func(f *models.MediaFile, r *StartRequestV3, d *string) { r.ClientFeatures = nil }},
		{name: "wrong container", change: func(f *models.MediaFile, r *StartRequestV3, d *string) { f.Container = "mp4" }},
		{name: "wrong codec", change: func(f *models.MediaFile, r *StartRequestV3, d *string) { f.SubtitleTracks[0].Codec = "ass" }},
		{name: "ambiguous stream index", change: func(f *models.MediaFile, r *StartRequestV3, d *string) {
			f.SubtitleTracks = append(f.SubtitleTracks, f.SubtitleTracks[0])
		}},
		{name: "missing container ID", change: func(f *models.MediaFile, r *StartRequestV3, d *string) {
			f.SubtitleTracks[0].ContainerTrackID = ""
			r.ClientPlaybackContext.Deliveries[*d].Subtitles.NativeEmbedded[0].TrackIdentity = "container_track_id"
		}},
		{name: "noncanonical container ID", change: func(f *models.MediaFile, r *StartRequestV3, d *string) {
			f.SubtitleTracks[0].ContainerTrackID = "0x3"
			r.ClientPlaybackContext.Deliveries[*d].Subtitles.NativeEmbedded[0].TrackIdentity = "container_track_id"
		}},
		{name: "ambiguous container ID", change: func(f *models.MediaFile, r *StartRequestV3, d *string) {
			f.SubtitleTracks = append(f.SubtitleTracks, models.SubtitleTrack{Index: 7, Codec: "srt", ContainerTrackID: "3"})
			r.ClientPlaybackContext.Deliveries[*d].Subtitles.NativeEmbedded[0].TrackIdentity = "container_track_id"
		}},
		{name: "progressive remux", change: func(f *models.MediaFile, r *StartRequestV3, d *string) {
			r.ClientPlaybackContext.Deliveries[DeliveryClassProgressiveV3] = r.ClientPlaybackContext.Deliveries[*d]
			*d = DeliveryClassProgressiveV3
		}},
		{name: "HLS", change: func(f *models.MediaFile, r *StartRequestV3, d *string) {
			r.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3] = r.ClientPlaybackContext.Deliveries[*d]
			*d = DeliveryClassHLSV3
		}},
		{name: "external selected", change: func(f *models.MediaFile, r *StartRequestV3, d *string) { r.SubtitleTrackIndex = new(0) }},
		{name: "ASS preserve requires fonts", change: func(f *models.MediaFile, r *StartRequestV3, d *string) {
			f.SubtitleTracks[0].Codec = "ass"
			r.SubtitleFidelityPreference = SubtitleFidelityPreserveV3
			r.ClientPlaybackContext.Deliveries[*d].Subtitles.NativeEmbedded[0].Codecs = []string{"ass"}
		}},
		{name: "ASS compatible", wantNative: true, change: func(f *models.MediaFile, r *StartRequestV3, d *string) {
			f.SubtitleTracks[0].Codec = "ass"
			r.ClientPlaybackContext.Deliveries[*d].Subtitles.NativeEmbedded[0].Codecs = []string{"ass"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := detailedFixtureFileV3()
			f.ExternalSubtitles = []models.ExternalSubtitle{{Format: "srt"}}
			f.SubtitleTracks = []models.SubtitleTrack{{Index: 5, ContainerTrackID: "3", Codec: "srt"}}
			r := validStartRequestV3()
			r.SubtitleTrackIndex = new(1)
			r.ClientFeatures = append(r.ClientFeatures, FeatureEmbeddedSubtitlesV3)
			d := DeliveryClassOriginalHTTPV3
			caps := r.ClientPlaybackContext.Deliveries[d]
			caps.Subtitles.NativeEmbedded = []NativeEmbeddedSubtitleCapabilityV3{{Container: "mkv", Codecs: []string{"subrip"}, TrackIdentity: "ffmpeg_stream_index"}}
			r.ClientPlaybackContext.Deliveries[d] = caps
			if tc.change != nil {
				tc.change(f, &r, &d)
			}
			got := ResolveSubtitlePolicyV3(f, r, true, d, nil)
			if (got.Decision.Embedded != nil) != tc.wantNative {
				t.Fatalf("decision=%+v terminal=%+v", got.Decision, got.Terminal)
			}
			if tc.wantNative && (got.Decision.Mode != SubtitleRenderV3 || got.Decision.Embedded.StreamIndex != 5 || got.Decision.TrackID != TrackIDV3(f.ID, "subtitle", 1) || got.Decision.Artifact != nil) {
				t.Fatalf("wrong identity or route: %+v", got.Decision)
			}
		})
	}
}

func TestNativeSubtitlePlanIdentityDistinguishesFallbackAndTrack(t *testing.T) {
	sidecar := PlanV3{Subtitle: SubtitleDecisionV3{Mode: SubtitleRenderV3, TrackID: TrackIDV3(42, "subtitle", 0)}}
	native := sidecar
	native.Subtitle.Embedded = &EmbeddedSubtitleV3{StreamIndex: 3, ContainerTrackID: "4"}
	other := native
	other.Subtitle.Embedded = &EmbeddedSubtitleV3{StreamIndex: 4, ContainerTrackID: "5"}
	for _, p := range []PlanV3{sidecar, other} {
		if DeterministicPlanIDV3("attempt", 42, 42, p) == DeterministicPlanIDV3("attempt", 42, 42, native) || PlanAttemptKeyV3(p, "route", nil) == PlanAttemptKeyV3(native, "route", nil) {
			t.Fatal("different subtitle routes share identity")
		}
	}
}
