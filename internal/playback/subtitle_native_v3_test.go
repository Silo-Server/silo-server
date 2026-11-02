package playback

import (
	"slices"
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

// A remuxed MKV keeps its original Matroska track numbers, so the second
// subtitle stream can be track 6. The planner hands the client the number the
// scanner recorded, never one derived from the stream index.
func TestNativeEmbeddedSubtitleV3UsesRecordedMatroskaTrackNumber(t *testing.T) {
	f := detailedFixtureFileV3()
	f.Container = "mkv"
	f.ExternalSubtitles = nil
	f.SubtitleTracks = []models.SubtitleTrack{
		{Index: 2, ContainerTrackID: "5", Codec: "subrip", Language: "eng"},
		{Index: 3, ContainerTrackID: "6", Codec: "subrip", Language: "eng", HearingImpaired: true},
	}
	r := validStartRequestV3()
	r.SubtitleTrackIndex = new(1)
	r.ClientFeatures = append(r.ClientFeatures, FeatureEmbeddedSubtitlesV3)
	d := DeliveryClassOriginalHTTPV3
	caps := r.ClientPlaybackContext.Deliveries[d]
	caps.Subtitles.NativeEmbedded = []NativeEmbeddedSubtitleCapabilityV3{{Container: "mkv", Codecs: []string{"subrip", "ass"}, TrackIdentity: "container_track_id"}}
	r.ClientPlaybackContext.Deliveries[d] = caps

	got := ResolveSubtitlePolicyV3(f, r, true, d, nil, 0, nil)
	if got.Decision.Embedded == nil || got.Decision.Embedded.StreamIndex != 3 || got.Decision.Embedded.ContainerTrackID != "6" {
		t.Fatalf("decision = %+v, want stream 3 as Matroska track 6", got.Decision)
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

func TestSameTrackLanguageV3(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"eng", "en", true},
		{"EN", "eng", true},
		{"en-US", "eng", true},
		{"ger", "de", true},
		{"fre", "fra", true},
		{"English", "en", true},
		{"eng", "jpn", false},
		{"und", "und", true},
		{"und", "eng", false},
		{"", "eng", false},
		{"", "", false},
	} {
		if got := SameTrackLanguageV3(tc.a, tc.b); got != tc.want {
			t.Errorf("SameTrackLanguageV3(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// native_selectable promises that selecting the row gives the plan a native
// embedded subtitle, so every row is checked against an actual replan with it
// selected.
func TestPlanPlaybackV3MarksNativeSelectableRows(t *testing.T) {
	for _, tc := range []struct {
		name        string
		limitations []string
		audio       int
		disable     bool
		want        []int
	}{
		// Combined ordinals: 0 external, 1 eng, 2 forced eng, 3 ass, 4 PGS.
		{name: "no limitations", audio: 0, want: []int{1, 2}},
		{name: "forced track in the playing audio language", limitations: rokuLimitationsV3, audio: 0, want: nil},
		{name: "audio switched away from the forced language", limitations: rokuLimitationsV3, audio: 1, want: []int{1}},
		{name: "original_http unavailable", audio: 0, disable: true, want: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := detailedFixtureFileV3()
			file.AudioTracks = []models.AudioTrack{{Codec: "aac", Channels: 2, Language: "eng", Default: true}, {Codec: "aac", Channels: 2, Language: "spa"}}
			file.ExternalSubtitles = []models.ExternalSubtitle{{Path: "/media/movie.en.srt", Language: "en", Format: "srt"}}
			file.SubtitleTracks = []models.SubtitleTrack{
				{Index: 3, ContainerTrackID: "4", Codec: "subrip", Language: "eng"},
				{Index: 4, ContainerTrackID: "5", Codec: "subrip", Language: "eng", Forced: true},
				{Index: 5, ContainerTrackID: "6", Codec: "ass", Language: "eng"},
				{Index: 6, ContainerTrackID: "7", Codec: "hdmv_pgs_subtitle", Language: "eng"},
			}
			req := hdr10ClientRequestV3(true)
			req.ClientFeatures = append(req.ClientFeatures, FeatureEmbeddedSubtitlesV3)
			original := req.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3]
			original.ValidatedClaims = append(original.ValidatedClaims, ClaimClientSelectedAudioTrackV3)
			original.Subtitles.NativeEmbedded = []NativeEmbeddedSubtitleCapabilityV3{{Container: "mkv", Codecs: []string{"subrip"}, TrackIdentity: "container_track_id", Limitations: tc.limitations}}
			original.Enabled = !tc.disable
			req.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3] = original
			plan := func(subtitle *int) *PlanV3 {
				r := req
				r.SubtitleTrackIndex = subtitle
				return PlanPlaybackV3(PlannerInputV3{Request: r, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: tc.audio, Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3()}).Plan
			}

			off := plan(nil)
			if off == nil || !tc.disable && off.Delivery != DeliveryOriginalHTTPV3 {
				t.Fatalf("plan = %+v, want original_http unless disabled", off)
			}
			var got []int
			for _, item := range off.Subtitle.Inventory {
				if item.NativeSelectable {
					got = append(got, item.CombinedIndex)
				}
				// The PGS row has no route on an HDR source: no plan at all.
				selected := plan(new(item.CombinedIndex))
				if native := selected != nil && selected.Subtitle.Embedded != nil; native != item.NativeSelectable || native && selected.Delivery != off.Delivery {
					t.Errorf("row %d native_selectable=%v, but selecting it gives plan %+v", item.CombinedIndex, item.NativeSelectable, selected)
				}
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("native_selectable rows = %v, want %v", got, tc.want)
			}
		})
	}
}
