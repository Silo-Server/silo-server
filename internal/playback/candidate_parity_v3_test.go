package playback

// TestCandidatePlannerMatchesWalk holds planPlaybackCandidatesV3 to the walk
// in PlanPlaybackV3 over a generated corpus, until the walk is deleted. It
// compares the whole result at every step of an attempted-key chain, and the
// lazy lookups each planner made.
//
// SILO_PLANNER_CORPUS=full runs every input; the default runs a fixed sample.
// SILO_PLANNER_CORPUS_ID=<id> runs one input and prints both results.

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

type parityStatsV3 struct {
	inputs, runs    int
	outcomes        map[string]bool
	originalLookups map[string]int
	failures        int
	only            string
	sampleMod       uint32
}

func TestCandidatePlannerMatchesWalk(t *testing.T) {
	stats := &parityStatsV3{outcomes: map[string]bool{}, originalLookups: map[string]int{}, only: os.Getenv("SILO_PLANNER_CORPUS_ID"), sampleMod: 97}
	if os.Getenv("SILO_PLANNER_CORPUS") == "full" || stats.only != "" {
		stats.sampleMod = 1
	}
	files, requests := parityFilesV3(), parityRequestsV3()
	for _, fileName := range slices.Sorted(maps.Keys(files)) {
		for _, requestName := range slices.Sorted(maps.Keys(requests)) {
			for _, settingsName := range slices.Sorted(maps.Keys(paritySettingsV3)) {
				for _, registryName := range slices.Sorted(maps.Keys(parityRegistriesV3)) {
					for _, quality := range parityQualitiesV3 {
						for _, v := range parityVariantsV3 {
							id := strings.Join([]string{fileName, requestName, settingsName, registryName, quality, v.name}, "|")
							// The corpus the walk was first compared on: one input in five.
							if parityHashV3(id)%5 != 0 {
								continue
							}
							build := func() PlannerInputV3 {
								return v.input(files[fileName](), requests[requestName](), paritySettingsV3[settingsName], parityRegistriesV3[registryName](), quality)
							}
							stats.compare(t, id, build, 10)
						}
					}
				}
			}
		}
	}
	parityAudioOnlyV3(t, stats, requests)
	paritySpecialsV3(t, stats)
	t.Logf("%d inputs, %d planner runs per planner, %d distinct outcomes", stats.inputs, stats.runs, len(stats.outcomes))
	t.Logf("original-file plans that made lookups, by lookup: %v", stats.originalLookups)
}

func parityHashV3(id string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	return h.Sum32()
}

// compare runs both planners on one input and then on each attempted-key
// step after it, stopping at the first refusal or difference. It then replays
// each key of the chain alone, as a client that echoes only its last failure.
func (s *parityStatsV3) compare(t *testing.T, id string, build func() PlannerInputV3, steps int) {
	t.Helper()
	if s.only != "" && s.only != id || s.only == "" && parityHashV3("sample|"+id)%s.sampleMod != 0 || s.failures >= 10 {
		return
	}
	s.inputs++
	var attempted []string
	for step := range steps {
		want, ok := s.compareStep(t, fmt.Sprintf("%s step %d", id, step), build, attempted)
		if !ok || want.Plan == nil {
			break
		}
		attempted = append(attempted, want.Plan.PlanAttemptKey)
	}
	for i, key := range attempted[min(1, len(attempted)):] {
		if _, ok := s.compareStep(t, fmt.Sprintf("%s only key %d", id, i+1), build, []string{key}); !ok {
			return
		}
	}
}

// dvStripLookupsV3 are the lookups the walk makes for a Dolby Vision source
// before it tries the original file, because the original plan's warnings
// depend on whether the strip is open.
var dvStripLookupsV3 = map[string]bool{"progressive_remux_registry": true, "hls_remux_registry": true, "dv_rpu_strippable": true}

func (s *parityStatsV3) compareStep(t *testing.T, label string, build func() PlannerInputV3, attempted []string) (PlannerResultV3, bool) {
	t.Helper()
	oldIn, newIn := build(), build()
	oldIn.AttemptedKeys = append(slices.Clone(oldIn.AttemptedKeys), attempted...)
	newIn.AttemptedKeys = append(slices.Clone(newIn.AttemptedKeys), attempted...)
	oldCalls, newCalls := map[string]bool{}, map[string]bool{}
	want := PlanPlaybackV3(countLookupsV3(oldIn, oldCalls))
	got := planPlaybackCandidatesV3(countLookupsV3(newIn, newCalls))
	s.runs++
	if s.only != "" {
		t.Logf("%s\nwalk lookups %v:\n%s\ncandidates lookups %v:\n%s", label, oldCalls, parityJSONV3(want), newCalls, parityJSONV3(got))
	}
	if !reflect.DeepEqual(want, got) {
		s.failures++
		t.Errorf("%s: results differ\n%s", label, parityDiffV3(parityJSONV3(want), parityJSONV3(got)))
		return want, false
	}
	original := got.Plan != nil && got.Plan.Delivery == DeliveryOriginalHTTPV3
	for name := range newCalls {
		switch {
		case !oldCalls[name]:
			s.failures++
			t.Errorf("%s: the candidate planner called %s and the walk did not", label, name)
		case original && (!dvStripLookupsV3[name] || got.Plan.Source.DynamicRange != DynamicRangeDolbyVisionV3):
			s.failures++
			t.Errorf("%s: an original-file plan called %s", label, name)
		case original:
			s.originalLookups[name]++
		}
	}
	s.outcomes[parityOutcomeV3(got)] = true
	return want, true
}

func countV3[T any](name string, lookup func() T, calls map[string]bool) func() T {
	if lookup == nil {
		return nil
	}
	return func() T { calls[name] = true; return lookup() }
}

func countLookupsV3(input PlannerInputV3, calls map[string]bool) PlannerInputV3 {
	input.ProgressiveRemuxRegistry = countV3("progressive_remux_registry", input.ProgressiveRemuxRegistry, calls)
	input.HLSRemuxRegistry = countV3("hls_remux_registry", input.HLSRemuxRegistry, calls)
	input.HLSVideoRegistry = countV3("hls_video_registry", input.HLSVideoRegistry, calls)
	input.HLSRegistry = countV3("hls_registry", input.HLSRegistry, calls)
	input.HLSToneMapCapabilities = countV3("hls_tone_map", input.HLSToneMapCapabilities, calls)
	input.DVRPUStrippable = countV3("dv_rpu_strippable", input.DVRPUStrippable, calls)
	input.SubtitleRenditionExecutable = countV3("rendition", input.SubtitleRenditionExecutable, calls)
	input.RemuxSubtitleRenditionExecutable = countV3("remux_rendition", input.RemuxSubtitleRenditionExecutable, calls)
	input.OnDemandSubtitleRenditionsExecutable = countV3("on_demand_renditions", input.OnDemandSubtitleRenditionsExecutable, calls)
	input.RemuxOnDemandSubtitleRenditionsExecutable = countV3("remux_on_demand_renditions", input.RemuxOnDemandSubtitleRenditionsExecutable, calls)
	input.CueIndexedSubtitleTracks = countV3("cue_index", input.CueIndexedSubtitleTracks, calls)
	return input
}

func parityJSONV3(result PlannerResultV3) string {
	raw, err := json.MarshalIndent(result, "", " ")
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// parityDiffV3 prints the lines around the first difference.
func parityDiffV3(want, got string) string {
	a, b := strings.Split(want, "\n"), strings.Split(got, "\n")
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	from := max(0, i-8)
	return fmt.Sprintf("walk:\n%s\ncandidates:\n%s", strings.Join(a[from:min(len(a), i+8)], "\n"), strings.Join(b[from:min(len(b), i+8)], "\n"))
}

func parityOutcomeV3(result PlannerResultV3) string {
	if result.Terminal != nil {
		return "T:" + result.Terminal.Reason
	}
	parts := []string{string(result.Plan.Delivery), result.Plan.DecisionReason}
	for _, transformation := range result.Plan.Transformations {
		parts = append(parts, "+"+transformation.Name)
	}
	for _, quirk := range result.Plan.AppliedQuirks {
		parts = append(parts, "~"+quirk.ID)
	}
	if result.SubtitleRendition {
		parts = append(parts, "#rendition")
	}
	return strings.Join(parts, " ")
}

var paritySettingsV3 = map[string]PlannerSettingsV3{
	"off":       {},
	"on":        {TranscodeEnabled: true},
	"on4k":      {TranscodeEnabled: true, Allow4KTranscode: true},
	"on4ktm":    {TranscodeEnabled: true, Allow4KTranscode: true, SoftwareToneMapEnabled: true},
	"all":       {TranscodeEnabled: true, Allow4KTranscode: true, SoftwareToneMapEnabled: true, AllowHEVCEncoding: true},
	"lowerpref": {TranscodeEnabled: true, Allow4KTranscode: true, SoftwareToneMapEnabled: true, PreferLowerVersionTranscode: true},
}

var parityQualitiesV3 = []string{"original", "auto", QualityRung1080pMediumV3, QualityRung720pLowV3}

var parityToneCapsV3 = tonemap.Capabilities{{Mode: tonemap.ModeSoftware, Backend: "software", Filter: "tonemapx", SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ, tonemap.SourceHLG}}}

func parityRegistryV3(names ...string) func() *TransformationRegistryV3 {
	versions := map[string]string{
		TransformationAudioToAACV3: TransformationAudioToAACRecipeVersionV3, TransformationVideoToH264V3: TransformationVideoToH264RecipeVersionV3,
		TransformationVideoToHEVCV3: TransformationVideoToHEVCRecipeVersionV3, TransformationHDRToSDRToneMapV3: TransformationHDRToSDRToneMapRecipeVersionV3,
		TransformationServerDV7HDR10V3: TransformationServerDV7HDR10RecipeVersionV3, TransformationServerDV7DV81V3: TransformationServerDV7DV81RecipeVersionV3,
	}
	return func() *TransformationRegistryV3 {
		specs := make([]TransformationSpecV3, 0, len(names))
		for _, name := range names {
			specs = append(specs, TransformationSpecV3{Name: name, RecipeVersion: versions[name], Available: true})
		}
		return NewTransformationRegistryV3(specs)
	}
}

var parityRegistriesV3 = map[string]func() *TransformationRegistryV3{
	"std":  testTransformationRegistryV3,
	"tone": toneMapTestRegistryV3,
	"none": parityRegistryV3(),
	"full": parityRegistryV3(TransformationAudioToAACV3, TransformationVideoToH264V3, TransformationVideoToHEVCV3, TransformationHDRToSDRToneMapV3, TransformationServerDV7HDR10V3, TransformationServerDV7DV81V3),
	"aac":  parityRegistryV3(TransformationAudioToAACV3),
}

type parityVariantV3 struct {
	name                     string
	serverCap, cap, estimate int
	subtitle                 string
	lazy, unstrippable       bool
	local, lower             bool
	renditions               bool // on-demand and pipeline renditions available
	downloaded               bool // the selected subtitle is a downloaded one
	facts                    bool
}

var parityVariantsV3 = []parityVariantV3{
	{name: "plain"}, {name: "lazy", lazy: true}, {name: "cap", serverCap: 3000}, {name: "cap-high", serverCap: 30_000, lazy: true},
	{name: "reqcap", cap: 5000}, {name: "estimate", estimate: 4000, local: true}, {name: "pgs", subtitle: "hdmv_pgs_subtitle", lazy: true},
	{name: "srt", subtitle: "subrip", lazy: true}, {name: "ass", subtitle: "ass"}, {name: "nostrip", unstrippable: true, lazy: true},
	{name: "lower", lower: true}, {name: "local", local: true},
	{name: "rend", subtitle: "subrip", lazy: true, renditions: true}, {name: "rend-ass", subtitle: "ass", lazy: true, renditions: true, facts: true},
	{name: "downloaded", downloaded: true, lazy: true},
}

type parityFactsV3 struct{}

func (parityFactsV3) OverrideTags(*models.MediaFile, models.SubtitleTrack) SubtitleFactV3 {
	return SubtitleFactYesV3
}

func (v parityVariantV3) input(file *models.MediaFile, req StartRequestV3, settings PlannerSettingsV3, registry *TransformationRegistryV3, quality string) PlannerInputV3 {
	req.QualityPreference = quality
	if v.cap > 0 {
		req.BandwidthCapKbps = &v.cap
	}
	if v.estimate > 0 {
		req.BandwidthEstimateKbps = &v.estimate
	}
	input := PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, Settings: settings, Registry: registry, ToneMapCapabilities: parityToneCapsV3,
		Now: time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC), ServerBitrateCapKbps: v.serverCap, LocalNetwork: v.local}
	video := len(file.VideoTracks) > 0
	if v.subtitle != "" && video {
		file.SubtitleTracks = []models.SubtitleTrack{{Index: 0, Codec: v.subtitle, ContainerTrackID: "3", Language: "en"}, {Index: 1, Codec: "subrip", ContainerTrackID: "4", Language: "fr"}}
		selected := 0
		input.Request.SubtitleTrackIndex, input.Request.SubtitleTrackID = &selected, TrackIDV3(file.ID, "subtitle", selected)
	}
	if v.downloaded && video {
		selected := 0
		input.AdditionalSubtitles = []SubtitleInventoryEntryV3{{CombinedIndex: 0, Codec: "srt", Source: SubtitleSourceDownloadedV3, Language: "es", DownloadedSubtitleID: 71}}
		input.Request.SubtitleTrackIndex, input.Request.SubtitleTrackID = &selected, TrackIDV3(file.ID, "subtitle", selected)
	}
	if v.lazy {
		input.ProgressiveRemuxRegistry = func() *TransformationRegistryV3 { return registry }
		input.HLSRemuxRegistry = func() *TransformationRegistryV3 { return registry }
		input.HLSVideoRegistry = func() *TransformationRegistryV3 { return registry }
		input.HLSToneMapCapabilities = func() tonemap.Capabilities { return parityToneCapsV3 }
		input.DVRPUStrippable = func() bool { return !v.unstrippable }
		input.SubtitleRenditionExecutable = func() bool { return true }
		input.RemuxSubtitleRenditionExecutable = func() bool { return true }
	} else if v.unstrippable {
		input.DVRPUStrippable = func() bool { return false }
	}
	if v.renditions {
		parityRenditionsV3(&input)
	}
	if v.facts {
		input.SubtitleTrackFacts = parityFactsV3{}
	}
	if v.lower && video {
		lower := detailedFixtureFileV3()
		parityHDV3(lower)
		input.LowerVersion = &LowerVersionV3{Requested: SourceDescriptorFromFileV3(file, 0), Lower: SourceDescriptorFromFileV3(lower, 0)}
	}
	return input
}

// parityRenditionsV3 lets every HLS route carry subtitle renditions, on
// demand or from the pipeline, for a client that shows text subtitles on
// progressive and HLS deliveries.
func parityRenditionsV3(input *PlannerInputV3) {
	input.EffectiveFile.Duration = 7200
	for _, class := range []string{DeliveryClassProgressiveV3, DeliveryClassHLSV3} {
		d := input.Request.ClientPlaybackContext.Deliveries[class]
		d.Subtitles = DeliverySubtitleCapabilitiesV3{SidecarText: true, EmbeddedText: true}
		input.Request.ClientPlaybackContext.Deliveries[class] = d
	}
	input.Request.ClientFeatures = append(slices.Clone(input.Request.ClientFeatures), FeatureHLSSubtitleRenditionsV3, FeatureHLSRemuxSubtitleRenditionsV3, FeatureHLSSubtitleRenditionsAllV3)
	input.SubtitleRenditionExecutable = func() bool { return true }
	input.RemuxSubtitleRenditionExecutable = func() bool { return true }
	input.OnDemandSubtitleRenditionsExecutable = func() bool { return true }
	input.RemuxOnDemandSubtitleRenditionsExecutable = func() bool { return true }
	input.CueIndexedSubtitleTracks = func() *CueIndexedSubtitles { return &CueIndexedSubtitles{Tracks: map[uint64]bool{3: true, 4: true}} }
}

func paritySDRV3(f *models.MediaFile) {
	t := &f.VideoTracks[0]
	t.VideoRange, t.VideoRangeType = "SDR", "SDR"
	t.ColorTransfer, t.ColorPrimaries, t.ColorSpace = "bt709", "bt709", "bt709"
	t.BitDepth, t.Profile = 8, "Main"
}

func parityHDV3(f *models.MediaFile) {
	f.Resolution, f.Bitrate = "1080p", 12_000
	t := &f.VideoTracks[0]
	t.Width, t.Height, t.Bitrate, t.Level = 1920, 1080, 11_000, 120
}

func parityFilesV3() map[string]func() *models.MediaFile {
	files := map[string]func() *models.MediaFile{
		"hdr10-4k":    detailedFixtureFileV3,
		"dv7-el":      dv7WithELFixtureV3,
		"dv7-nostrip": unstrippableProfile7FixtureV3,
		"dv8-1":       func() *models.MediaFile { return dv8BaseLayerFixtureV3(1) },
		"dv8-4":       func() *models.MediaFile { return dv8BaseLayerFixtureV3(4) },
		"dv8-2":       func() *models.MediaFile { return dv8BaseLayerFixtureV3(2) },
		"audio-only":  audioOnlyFixtureFileV3,
	}
	files["dv7-convertible"] = func() *models.MediaFile {
		f := dv7WithELFixtureV3()
		t := &f.VideoTracks[0]
		t.DVConfigPresent, t.DVRPUPresent, t.DVELPresent, t.DVBLPresent = true, true, true, true
		return f
	}
	files["dv8-1-hdr10plus"] = func() *models.MediaFile { f := dv8BaseLayerFixtureV3(1); f.VideoTracks[0].HDR10Plus = true; return f }
	files["sdr-4k"] = func() *models.MediaFile { f := detailedFixtureFileV3(); paritySDRV3(f); return f }
	files["hdr10-1080"] = func() *models.MediaFile { f := detailedFixtureFileV3(); parityHDV3(f); return f }
	hlg := func(f *models.MediaFile) *models.MediaFile {
		f.VideoTracks[0].VideoRangeType, f.VideoTracks[0].ColorTransfer = "HLG", "arib-std-b67"
		return f
	}
	files["hlg-4k"] = func() *models.MediaFile { return hlg(detailedFixtureFileV3()) }
	files["hlg-1080"] = func() *models.MediaFile { f := hlg(detailedFixtureFileV3()); parityHDV3(f); return f }
	files["hdr-unknown"] = func() *models.MediaFile { f := detailedFixtureFileV3(); f.VideoTracks[0].VideoRangeType = ""; return f }
	for _, v := range []struct{ name, codec, profile, container string }{
		{"h264-mp4", "h264", "High", "mp4"}, {"h264-mkv", "h264", "High", "mkv"}, {"hevc-mp4", "hevc", "Main", "mp4"},
		{"av1-mkv", "av1", "Main", "mkv"}, {"vp9-webm", "vp9", "Profile 0", "webm"}, {"h264-hi10", "h264", "High 10", "mkv"},
	} {
		files["sdr-1080-"+v.name] = func() *models.MediaFile {
			f := detailedFixtureFileV3()
			paritySDRV3(f)
			parityHDV3(f)
			f.CodecVideo, f.VideoTracks[0].Codec, f.VideoTracks[0].Profile, f.Container = v.codec, v.codec, v.profile, v.container
			f.FilePath = "/media/movie." + v.container
			if v.profile == "High 10" {
				f.VideoTracks[0].BitDepth = 10
			}
			return f
		}
	}
	// Level 5.1 is what the Fire TV High 10 decode quirk covers.
	files["sdr-1080-h264-hi10-l51"] = func() *models.MediaFile { f := files["sdr-1080-h264-hi10"](); f.VideoTracks[0].Level = 51; return f }
	for _, a := range []struct {
		codec    string
		channels int
		layout   string
	}{{"ac3", 6, "5.1"}, {"eac3", 6, "5.1"}, {"eac3", 8, "7.1"}, {"dts", 6, "5.1"}, {"truehd", 8, "7.1"}, {"opus", 2, "stereo"}, {"flac", 2, "stereo"}, {"aac", 6, "5.1"}, {"aac", 1, "mono"}} {
		for _, base := range []string{"hdr10-4k", "sdr-1080-h264-mkv", "sdr-1080-h264-mp4", "dv8-1", "dv7-el", "dv7-convertible", "sdr-1080-hevc-mp4"} {
			mk := files[base]
			files[fmt.Sprintf("%s+%s%d", base, a.codec, a.channels)] = func() *models.MediaFile {
				f := mk()
				f.CodecAudio, f.AudioChannels = a.codec, a.channels
				f.AudioTracks = []models.AudioTrack{{Codec: a.codec, Channels: a.channels, Layout: a.layout}}
				return f
			}
		}
	}
	for _, channels := range []int{2, 6, 8} {
		for _, codec := range []string{"eac3", "ac3", "dts"} {
			files[fmt.Sprintf("avi-h264+%s%d", codec, channels)] = func() *models.MediaFile {
				return &models.MediaFile{
					ID: 42, FilePath: "/media/movie.avi", Container: "avi", CodecVideo: "h264", CodecAudio: codec,
					Resolution: "1080p", Bitrate: 12_000, AudioChannels: channels,
					VideoTracks: []models.VideoTrack{{Codec: "h264", Profile: "High", Level: 42, Width: 1920, Height: 1080, FrameRate: "24", Bitrate: 12_000, BitDepth: 8, VideoRange: "SDR", VideoRangeType: "SDR"}},
					AudioTracks: []models.AudioTrack{{Codec: codec, Channels: channels, Layout: "x"}},
				}
			}
		}
	}
	files["sdr-1080-noaudio"] = func() *models.MediaFile {
		f := files["sdr-1080-h264-mkv"]()
		f.CodecAudio, f.AudioChannels, f.AudioTracks = "", 0, nil
		return f
	}
	files["sdr-1080-nobitrate"] = func() *models.MediaFile {
		f := files["sdr-1080-h264-mkv"]()
		f.Bitrate, f.VideoTracks[0].Bitrate = 0, 0
		return f
	}
	files["sdr-1080-twoaudio"] = func() *models.MediaFile {
		f := files["sdr-1080-h264-mkv"]()
		f.AudioTracks = []models.AudioTrack{{Codec: "truehd", Channels: 8, Layout: "7.1"}, {Codec: "eac3", Channels: 6, Layout: "5.1"}}
		f.CodecAudio, f.AudioChannels = "truehd", 8
		return f
	}
	return files
}

func parityRequestsV3() map[string]func() StartRequestV3 {
	clientDV := func(transforms ...TransformationV3) func() StartRequestV3 {
		return func() StartRequestV3 {
			hdr := &HDRCapabilitiesV3{HDR10: true, DolbyVisionProfiles: []int{5, 8}}
			if len(transforms) == 1 {
				hdr = &HDRCapabilitiesV3{HDR10: true}
			}
			return clientManagedDV7RequestV3(hdr, hdr, hdr, transforms...)
		}
	}
	dv81 := TransformationV3{Name: ClientDV7ToDV81V3, Executor: ExecutorClientV3, RecipeVersion: ClientDVTransformVersionV3}
	hdr10 := TransformationV3{Name: ClientDV7ToHDR10V3, Executor: ExecutorClientV3, RecipeVersion: ClientDVTransformVersionV3}
	reqs := map[string]func() StartRequestV3{
		"tv-hevc":          validStartRequestV3,
		"tv-hdr10":         func() StartRequestV3 { return hdr10ClientRequestV3(true) },
		"tv-hdr10-dev":     func() StartRequestV3 { return hdr10ClientRequestV3(false) },
		"quirk":            quirkRequestV3,
		"dv7-client":       clientDV(dv81, hdr10),
		"dv7-client-hdr10": clientDV(hdr10),
		"dv7-managed": func() StartRequestV3 {
			hdr := &HDRCapabilitiesV3{HDR10: true, DolbyVisionProfiles: []int{5, 7, 8}}
			return clientManagedDV7RequestV3(hdr, hdr, hdr)
		},
	}
	wide := func() StartRequestV3 {
		r := hdr10ClientRequestV3(true)
		r.Capabilities.CodecsVideo, r.Capabilities.CodecsAudio, r.Capabilities.Containers = []string{"hevc", "h264", "av1"}, []string{"aac", "ac3", "eac3"}, []string{"mkv", "mp4"}
		dv := &HDRCapabilitiesV3{HDR10: true, HLG: true, DolbyVisionProfiles: []int{5, 8}}
		r.Capabilities.HDRDetails, r.ClientPlaybackContext.Output.HDRDetails = dv, dv
		return r
	}
	reqs["wide-dv"] = wide
	web := func() StartRequestV3 {
		r := validStartRequestV3()
		r.Capabilities.VideoEvidence, r.Capabilities.AudioEvidence = EvidenceDeclaredV3, EvidenceDeclaredV3
		r.Capabilities.CodecsVideo, r.Capabilities.CodecsAudio, r.Capabilities.Containers = []string{"h264"}, []string{"aac", "opus"}, []string{"mp4"}
		r.Capabilities.MaxResolution = "1080p"
		r.ClientPlaybackContext.FormFactor, r.ClientPlaybackContext.Device = "web", DeviceContextV3{Platform: "web", OSVersion: "macos"}
		return r
	}
	reqs["web-h264"] = web
	firefox := func() StartRequestV3 {
		r := web()
		r.ClientFeatures = append(r.ClientFeatures, FeatureDeviceQuirksV3)
		r.Capabilities.CodecsVideo = []string{"h264", "hevc"}
		r.ClientPlaybackContext.Device.PlatformDetails = map[string]string{"user_agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:130.0) Gecko/20100101 Firefox/130.0"}
		return r
	}
	reqs["web-firefox"] = firefox
	reqs["web-firefox-resume"] = func() StartRequestV3 { r := firefox(); pos := 1200.0; r.StartPosition = &pos; return r }
	firetv := func() StartRequestV3 {
		r := quirkRequestV3()
		r.Capabilities.CodecsVideo, r.Capabilities.CodecsAudio, r.Capabilities.Containers = []string{"hevc", "h264"}, []string{"aac", "ac3", "eac3"}, []string{"mp4"}
		return r
	}
	reqs["firetv-eac3"] = firetv
	firetvDecode := func() StartRequestV3 {
		r := quirkRequestV3()
		r.Capabilities.Containers, r.Capabilities.CodecsAudio = []string{"mkv"}, []string{"aac", "eac3"}
		r.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "h264", Profiles: []string{"high", "high 10"}, Levels: []int{42, 51}, BitDepths: []int{8}, MaxWidth: 1920, MaxHeight: 1080, MaxFrameRate: 60, MaxBitrateKbps: 20_000, Hardware: true}}
		return r
	}
	reqs["firetv-decode"] = firetvDecode
	// A Fire TV whose native Dolby Vision path needs HDR10+ SEI removed.
	reqs["firetv-dv"] = func() StartRequestV3 {
		r := wide()
		r.ClientFeatures = append(r.ClientFeatures, FeatureDeviceQuirksV3)
		r.ClientPlaybackContext.Device = DeviceContextV3{Platform: "android", Manufacturer: "Amazon", Model: "AFTKA", PlatformDetails: map[string]string{"sdk_int": "30"}}
		for _, class := range []string{DeliveryClassOriginalHTTPV3, DeliveryClassProgressiveV3, DeliveryClassHLSV3} {
			d := r.ClientPlaybackContext.Deliveries[class]
			d.Features = append(slices.Clone(d.Features), ClientDV8HDR10PlusSanitizerV3)
			r.ClientPlaybackContext.Deliveries[class] = d
		}
		return r
	}
	withDV8Fallback := func(mk func() StartRequestV3, hdr *HDRCapabilitiesV3) func() StartRequestV3 {
		return func() StartRequestV3 {
			r := mk()
			d := r.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3]
			d.ValidatedClaims = append(slices.Clone(d.ValidatedClaims), ClaimClientDV8BaseLayerFallbackV3)
			if hdr != nil {
				d.HDRDetails = hdr
			}
			r.ClientPlaybackContext.Deliveries[DeliveryClassOriginalHTTPV3] = d
			return r
		}
	}
	reqs["dv8-fallback"] = withDV8Fallback(func() StartRequestV3 { return hdr10ClientRequestV3(true) }, nil)
	reqs["dv8-fallback-dv"] = withDV8Fallback(wide, &HDRCapabilitiesV3{HDR10: true})
	reqs["web-attested"] = func() StartRequestV3 {
		r := web()
		r.Capabilities.VideoEvidence, r.Capabilities.AudioEvidence = EvidencePlatformAttestedV3, EvidencePlatformAttestedV3
		return r
	}
	for name, mk := range map[string]func() StartRequestV3{"tv-hevc": validStartRequestV3, "wide-dv": wide, "web-h264": web, "web-firefox": firefox, "firetv-eac3": firetv, "firetv-decode": firetvDecode} {
		parityDeliveryVariantsV3(reqs, name, mk)
	}
	return reqs
}

// parityDeliveryVariantsV3 adds a request with each delivery disabled, one
// with a stereo ceiling, and one with a narrower HLS delivery.
func parityDeliveryVariantsV3(reqs map[string]func() StartRequestV3, name string, mk func() StartRequestV3) {
	two, six := 2, 6
	for _, off := range []string{DeliveryClassOriginalHTTPV3, DeliveryClassProgressiveV3, DeliveryClassHLSV3} {
		reqs[name+"-no-"+off] = func() StartRequestV3 {
			r := mk()
			d := r.ClientPlaybackContext.Deliveries[off]
			d.Enabled = false
			r.ClientPlaybackContext.Deliveries[off] = d
			return r
		}
	}
	reqs[name+"-stereo-ceiling"] = func() StartRequestV3 {
		r := mk()
		for _, class := range []string{DeliveryClassProgressiveV3, DeliveryClassHLSV3} {
			d := r.ClientPlaybackContext.Deliveries[class]
			d.MaxChannels = &two
			r.ClientPlaybackContext.Deliveries[class] = d
		}
		return r
	}
	reqs[name+"-hls-51"] = func() StartRequestV3 {
		r := mk()
		d := r.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3]
		d.MaxChannels, d.AudioDecodeCodecs, d.VideoCodecs = &six, []string{"aac", "eac3"}, []string{"hevc", "h264"}
		d.Features = []string{FeatureHLSCopyMPEGTSV3, ClientNativeHLSPlaybackV3}
		r.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3] = d
		return r
	}
}

// parityAudioOnlyV3 covers files without video: codecs, containers,
// bitrates, track selection, caps and attempted keys.
func parityAudioOnlyV3(t *testing.T, stats *parityStatsV3, requests map[string]func() StartRequestV3) {
	type audio struct{ codec, container string }
	formats := []audio{{"mp3", "mp3"}, {"aac", "mp4"}, {"aac", "m4a"}, {"flac", "flac"}, {"opus", "ogg"}, {"vorbis", "ogg"}, {"alac", "mp4"}, {"eac3", "mka"}, {"dts", "mka"}, {"truehd", "mka"}, {"pcm_s16le", "wav"}, {"", "mp3"}}
	caps := []struct {
		name               string
		serverCap, reqCap  int
		lazy, audioTrack1  bool
		progressiveOffline bool
	}{{name: "plain"}, {name: "lazy", lazy: true}, {name: "cap", serverCap: 64}, {name: "cap-low", serverCap: 20, lazy: true}, {name: "cap-high", serverCap: 3000},
		{name: "reqcap", reqCap: 96}, {name: "reqcap-high", reqCap: 5000}, {name: "track1", audioTrack1: true, lazy: true}, {name: "no-progressive", progressiveOffline: true}}
	for _, format := range formats {
		for _, bitrate := range []int{0, 128, 320, 1500} {
			for _, channels := range []int{1, 2, 6} {
				for _, requestName := range slices.Sorted(maps.Keys(requests)) {
					for _, registryName := range slices.Sorted(maps.Keys(parityRegistriesV3)) {
						for _, c := range caps {
							id := fmt.Sprintf("audio|%s.%s|%d|%d|%s|%s|%s", format.codec, format.container, bitrate, channels, requestName, registryName, c.name)
							build := func() PlannerInputV3 {
								file := &models.MediaFile{ID: 77, BaseType: "audiobook", FilePath: "/media/book." + format.container, Container: format.container, CodecAudio: format.codec, Bitrate: bitrate, AudioChannels: channels, Duration: 39_600,
									AudioTracks: []models.AudioTrack{{Codec: format.codec, Channels: channels}, {Codec: "aac", Channels: 2, Default: true}}}
								req := requests[requestName]()
								if c.reqCap > 0 {
									req.BandwidthCapKbps = &c.reqCap
								}
								if c.progressiveOffline {
									d := req.ClientPlaybackContext.Deliveries[DeliveryClassProgressiveV3]
									d.Enabled = false
									req.ClientPlaybackContext.Deliveries[DeliveryClassProgressiveV3] = d
								}
								registry := parityRegistriesV3[registryName]()
								input := PlannerInputV3{Request: req, RequestedFile: file, EffectiveFile: file, Settings: PlannerSettingsV3{TranscodeEnabled: true}, Registry: registry, ServerBitrateCapKbps: c.serverCap,
									Now: time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)}
								if c.audioTrack1 {
									input.AudioTrackIndex = 1
								}
								if c.lazy {
									input.ProgressiveRemuxRegistry = func() *TransformationRegistryV3 { return registry }
								}
								return input
							}
							stats.compare(t, id, build, 4)
						}
					}
				}
			}
		}
	}
}

// paritySpecialsV3 replays the planner inputs other tests build by hand.
func paritySpecialsV3(t *testing.T, stats *parityStatsV3) {
	special := map[string]func() PlannerInputV3{"bitrate-policy": bitratePolicyFixtureV3, "on-demand": func() PlannerInputV3 { return onDemandPlannerInputV3(t) }}
	for _, codec := range []string{"subrip", "ass", "mov_text", "webvtt"} {
		for _, fidelity := range []SubtitleFidelityV3{SubtitleFidelityCompatibleV3, SubtitleFidelityPreserveV3} {
			special[fmt.Sprintf("rendition-%s-%s", codec, fidelity)] = func() PlannerInputV3 { return subtitleRenditionPlannerInputV3(t, codec, fidelity) }
		}
		special["rendition-remux-"+codec] = func() PlannerInputV3 { return subtitleRenditionRemuxInputV3(t, codec) }
	}
	for i := range 8 {
		special[fmt.Sprintf("hevc-%d", i)] = func() PlannerInputV3 { return hevcTranscodePlannerInputV3(i&1 != 0, i&2 != 0, i&4 != 0) }
		special[fmt.Sprintf("hevc-rend-%d", i)] = func() PlannerInputV3 {
			input := hevcTranscodePlannerInputV3(i&1 != 0, i&2 != 0, i&4 != 0)
			input.EffectiveFile.SubtitleTracks = []models.SubtitleTrack{{Index: 0, Codec: "subrip", ContainerTrackID: "3", Language: "en"}, {Index: 1, Codec: "subrip", ContainerTrackID: "4", Language: "fr"}}
			selected := i % 3
			if selected < 2 {
				input.Request.SubtitleTrackIndex, input.Request.SubtitleTrackID = &selected, TrackIDV3(input.EffectiveFile.ID, "subtitle", selected)
			}
			parityRenditionsV3(&input)
			return input
		}
	}
	for _, name := range slices.Sorted(maps.Keys(special)) {
		for _, quality := range append(slices.Clone(parityQualitiesV3), QualityRung2160pMediumV3) {
			for _, serverCap := range []int{0, 3000, 30_000} {
				for _, local := range []bool{false, true} {
					id := fmt.Sprintf("special|%s|%s|%d|%t", name, quality, serverCap, local)
					build := func() PlannerInputV3 {
						input := special[name]()
						input.Now, input.ServerBitrateCapKbps, input.LocalNetwork = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC), serverCap, local
						input.Request.QualityPreference = quality
						return input
					}
					// Specials always run: there are few and they reach rare routes.
					only := stats.sampleMod
					stats.sampleMod = 1
					stats.compare(t, id, build, 12)
					stats.sampleMod = only
				}
			}
		}
	}
}
