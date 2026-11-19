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
		{name: "index-only track", change: func(f *models.MediaFile, r *StartRequestV3, d *string) { f.SubtitleTracks[0].IndexOnly = true }},
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
			got := ResolveSubtitlePolicyV3(f, r, true, d, nil, 0, nil)
			if (got.Decision.Embedded != nil) != tc.wantNative {
				t.Fatalf("decision=%+v terminal=%+v", got.Decision, got.Terminal)
			}
			if tc.wantNative && (got.Decision.Mode != SubtitleRenderV3 || got.Decision.Embedded.StreamIndex != 5 || got.Decision.TrackID != TrackIDV3(f.ID, "subtitle", 1) || got.Decision.Artifact != nil) {
				t.Fatalf("wrong identity or route: %+v", got.Decision)
			}
		})
	}
}

// overrideTagFactsV3 answers OverrideTags by stream index; missing entries are
// unknown.
type overrideTagFactsV3 map[int]SubtitleFactV3

func (f overrideTagFactsV3) OverrideTags(_ *models.MediaFile, track models.SubtitleTrack) SubtitleFactV3 {
	return f[track.Index]
}

// rokuLimitationsV3 is what Roku OS 16 declares on its mkv/subrip entry.
var rokuLimitationsV3 = []string{NativeEmbeddedLimitationForcedUnlistedV3, NativeEmbeddedLimitationHiddenTrackPreemptionV3, NativeEmbeddedLimitationLiteralOverrideTagsV3}

func TestNativeEmbeddedSubtitleV3HonorsLimitations(t *testing.T) {
	forcedUnlisted := []string{NativeEmbeddedLimitationForcedUnlistedV3}
	preemption := []string{NativeEmbeddedLimitationHiddenTrackPreemptionV3}
	overrideTags := []string{NativeEmbeddedLimitationLiteralOverrideTagsV3}
	for _, tc := range []struct {
		name string
		// entries lists one native_embedded entry per element, by its
		// limitations.
		entries    [][]string
		selected   int
		audio      int
		change     func(*models.MediaFile)
		facts      SubtitleTrackFactsV3
		wantNative bool
	}{
		{name: "no limitations keep a forced track native", entries: [][]string{nil}, selected: 1, wantNative: true},
		{name: "forced_unlisted refuses a forced track", entries: [][]string{forcedUnlisted}, selected: 1},
		{name: "forced_unlisted keeps an unflagged track", entries: [][]string{forcedUnlisted}, wantNative: true},
		{name: "forced track in the audio language preempts", entries: [][]string{preemption}},
		{name: "forced track preempts across ISO 639-1 and 639-2", entries: [][]string{preemption}, change: func(f *models.MediaFile) {
			f.AudioTracks[0].Language = "en"
		}},
		{name: "forced track in another language does not preempt", entries: [][]string{preemption}, wantNative: true, change: func(f *models.MediaFile) {
			f.SubtitleTracks[1].Language = "jpn"
		}},
		{name: "selected audio in another language avoids preemption", entries: [][]string{preemption}, audio: 1, wantNative: true},
		{name: "no audio selection plays the default-flagged audio", entries: [][]string{preemption}, audio: -1, wantNative: true, change: func(f *models.MediaFile) {
			f.AudioTracks[0].Default, f.AudioTracks[1].Default = false, true
		}},
		{name: "default-flagged PGS preempts", entries: [][]string{preemption}, change: func(f *models.MediaFile) {
			f.SubtitleTracks[1].Forced = false
			f.SubtitleTracks = append(f.SubtitleTracks, models.SubtitleTrack{Index: 5, Codec: "hdmv_pgs_subtitle", Language: "dan", Default: true})
		}},
		{name: "default-flagged DVD subtitle preempts", entries: [][]string{preemption}, change: func(f *models.MediaFile) {
			f.SubtitleTracks[1].Forced = false
			f.SubtitleTracks = append(f.SubtitleTracks, models.SubtitleTrack{Index: 5, Codec: "dvd_subtitle", Default: true})
		}},
		{name: "unflagged PGS does not preempt", entries: [][]string{preemption}, wantNative: true, change: func(f *models.MediaFile) {
			f.SubtitleTracks[1].Forced = false
			f.SubtitleTracks = append(f.SubtitleTracks, models.SubtitleTrack{Index: 5, Codec: "hdmv_pgs_subtitle"})
		}},
		{name: "default-flagged text track does not preempt", entries: [][]string{preemption}, wantNative: true, change: func(f *models.MediaFile) {
			f.SubtitleTracks[1].Forced = false
			f.SubtitleTracks[2].Default = true
		}},
		{name: "known override tags refuse the track", entries: [][]string{overrideTags}, facts: overrideTagFactsV3{2: SubtitleFactYesV3}},
		{name: "known clean track stays native", entries: [][]string{overrideTags}, facts: overrideTagFactsV3{2: SubtitleFactNoV3}, wantNative: true},
		{name: "unknown override tags are allowed", entries: [][]string{overrideTags}, wantNative: true},
		{name: "override tags matter only to a client that declares them", entries: [][]string{nil}, facts: overrideTagFactsV3{2: SubtitleFactYesV3}, wantNative: true},
		{name: "unknown limitation rules out its entry", entries: [][]string{{"future_limitation"}}},
		{name: "unknown limitation leaves the other entries", entries: [][]string{{"future_limitation"}, nil}, wantNative: true},
		{name: "limitation spelling is normalized", entries: [][]string{{" Forced_Unlisted "}}, selected: 1},
		{name: "every Roku limitation on a file without override tracks", entries: [][]string{rokuLimitationsV3}, wantNative: true, change: func(f *models.MediaFile) {
			f.SubtitleTracks[1].Forced = false
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := detailedFixtureFileV3()
			f.AudioTracks = []models.AudioTrack{{Codec: "aac", Channels: 2, Language: "eng", Default: true}, {Codec: "aac", Channels: 2, Language: "spa"}}
			f.SubtitleTracks = []models.SubtitleTrack{
				{Index: 2, ContainerTrackID: "3", Codec: "subrip", Language: "eng"},
				{Index: 3, ContainerTrackID: "4", Codec: "subrip", Language: "eng", Forced: true},
				{Index: 4, ContainerTrackID: "5", Codec: "subrip", Language: "spa"},
			}
			if tc.change != nil {
				tc.change(f)
			}
			r := validStartRequestV3()
			r.SubtitleTrackIndex = new(tc.selected)
			r.ClientFeatures = append(r.ClientFeatures, FeatureEmbeddedSubtitlesV3)
			caps := r.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3]
			for _, limitations := range tc.entries {
				caps.Subtitles.NativeEmbedded = append(caps.Subtitles.NativeEmbedded, NativeEmbeddedSubtitleCapabilityV3{Container: "mkv", Codecs: []string{"subrip"}, TrackIdentity: "container_track_id", Limitations: limitations})
			}
			r.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3] = caps

			got := ResolveSubtitlePolicyV3(f, r, true, DeliveryClassOriginalHTTPV3, nil, tc.audio, tc.facts)
			if got.Terminal != nil || (got.Decision.Embedded != nil) != tc.wantNative {
				t.Fatalf("decision=%+v terminal=%+v, want native=%v", got.Decision, got.Terminal, tc.wantNative)
			}
			if !tc.wantNative && got.Decision.Mode != SubtitleRenderV3 {
				t.Fatalf("refused native track fell to %q, want the sidecar", got.Decision.Mode)
			}
		})
	}
}
