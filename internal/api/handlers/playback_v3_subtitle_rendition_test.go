package handlers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/embeddedsub"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
)

func subtitleRenditionPlanV3(file *models.MediaFile, delivery playback.DeliveryV3, mode playback.SubtitleModeV3) *playback.PlanV3 {
	return &playback.PlanV3{
		Delivery: delivery,
		Subtitle: playback.SubtitleDecisionV3{
			Mode: mode, TrackID: playback.TrackIDV3(file.ID, "subtitle", 0),
			HLSRendition: &playback.HLSSubtitleRenditionV3{GroupID: playback.SubtitleRenditionGroupID, Name: playback.SubtitleRenditionName, Format: "vtt", MIMEType: "text/vtt"},
			Artifact:     &playback.SubtitleArtifactV3{URL: "/stale.vtt", Format: "vtt"},
			Inventory:    playback.BuildSubtitleInventoryV3(file, nil),
		},
	}
}

func TestAttachSubtitleArtifactKeepsHLSSubtitleRendition(t *testing.T) {
	file := &models.MediaFile{ID: 42, SubtitleTracks: []models.SubtitleTrack{{Index: 4, Codec: "subrip"}}}
	handler := &PlaybackHandler{}
	for _, delivery := range []playback.DeliveryV3{playback.DeliveryTranscodeHLSV3, playback.DeliveryRemuxHLSV3} {
		plan := subtitleRenditionPlanV3(file, delivery, playback.SubtitleRenderV3)
		if err := handler.attachSubtitleArtifactV3(t.Context(), "session", file, plan, 0, nil, nil); err != nil {
			t.Fatalf("%s: %v", delivery, err)
		}
		if plan.Subtitle.HLSRendition == nil || plan.Subtitle.Artifact != nil {
			t.Fatalf("%s: an in-manifest subtitle publishes no sidecar artifact: %#v", delivery, plan.Subtitle)
		}
		if plan.Subtitle.Inventory[0].URL == "" {
			t.Fatalf("%s: the inventory keeps its sidecar URL for other selections", delivery)
		}
	}
}

func TestAttachSubtitleArtifactRejectsOrClearsMisplacedRendition(t *testing.T) {
	file := &models.MediaFile{ID: 42, SubtitleTracks: []models.SubtitleTrack{{Index: 4, Codec: "subrip"}}}
	handler := &PlaybackHandler{}
	for _, delivery := range []playback.DeliveryV3{playback.DeliveryRemuxProgressiveV3, playback.DeliveryOriginalHTTPV3} {
		plan := subtitleRenditionPlanV3(file, delivery, playback.SubtitleRenderV3)
		if err := handler.attachSubtitleArtifactV3(t.Context(), "session", file, plan, 0, nil, nil); err == nil {
			t.Fatalf("%s accepted an HLS subtitle rendition", delivery)
		}
	}
	plan := subtitleRenditionPlanV3(file, playback.DeliveryTranscodeHLSV3, playback.SubtitleOffV3)
	if err := handler.attachSubtitleArtifactV3(t.Context(), "session", file, plan, -1, nil, nil); err != nil {
		t.Fatal(err)
	}
	if plan.Subtitle.HLSRendition != nil {
		t.Fatalf("subtitles-off plan kept a rendition: %#v", plan.Subtitle)
	}
}

func TestSidecarOnlyHLSReplanReusesTransportOnlyForAnUnchangedRendition(t *testing.T) {
	rendition := &playback.HLSSubtitleRenditionV3{GroupID: "subs", Name: "Subtitles", Format: "vtt", MIMEType: "text/vtt"}
	freeze := func(plan *playback.PlanV3, track int, withRendition bool) playback.ExecutableRecipeV3 {
		return playback.FreezeExecutableRecipeV3(playback.PlannerResultV3{
			Plan: plan, PlayMethod: playback.PlayTranscode, TargetVideoCodec: "h264", TargetAudioCodec: "aac",
			SubtitleTrackIndex: track, SubtitleTransportTrackIndex: track, SubtitleCodec: "subrip", SubtitleRendition: withRendition,
		})
	}
	for _, tc := range []struct {
		name                       string
		current, candidate         bool
		currentTrack, candidateTrk int
		want                       bool
	}{
		{"sidecar to rendition", false, true, 0, 0, false},
		{"rendition to another track", true, true, 0, 1, false},
		{"rendition kept", true, true, 0, 0, true},
		{"rendition dropped", true, false, 0, -1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			currentPlan := playback.PlanV3{PlanID: "current-plan", Delivery: playback.DeliveryTranscodeHLSV3,
				Stream: playback.StreamV3{URL: "/stream/current.m3u8"}, RequestedMediaFileID: 1, EffectiveMediaFileID: 1}
			candidatePlan := currentPlan
			candidatePlan.PlanID = "candidate-plan"
			if tc.current {
				currentPlan.Subtitle.HLSRendition = rendition
			}
			if tc.candidate {
				candidatePlan.Subtitle.HLSRendition = rendition
			}
			record := &playback.AttemptRecordV3{EffectiveMediaFileID: 1, CurrentPlan: currentPlan,
				FrozenRecipe: freeze(&currentPlan, tc.currentTrack, tc.current)}
			record.NormalizedRequest.ClientPlaybackContext.Output.OutputContextID = "output-context"
			_, reused := sidecarOnlyHLSReplanV3(record, &candidatePlan, freeze(&candidatePlan, tc.candidateTrk, tc.candidate), "output-context")
			if reused != tc.want {
				t.Fatalf("reused = %t, want %t", reused, tc.want)
			}
		})
	}
}

func TestHLSSubtitleRenditionsAreNegotiatedOnlyOnAPIV2(t *testing.T) {
	for _, feature := range []string{playback.FeatureHLSSubtitleRenditionsV3, playback.FeatureHLSRemuxSubtitleRenditionsV3, playback.FeatureHLSSubtitleRenditionsAllV3} {
		if slices.Contains(playback.ServerFeaturesV3(), feature) {
			t.Fatalf("the frozen /api/v1 surface must not advertise %s", feature)
		}
		if !slices.Contains(playback.NativeServerFeaturesV3(), feature) ||
			!slices.Contains(playback.AttemptStickyFeaturesV3(), feature) {
			t.Fatalf("/api/v2 advertises %s as an attempt-sticky feature", feature)
		}
	}
}

func TestFailedHLSSubtitleRenditionStaysOffForTheAttempt(t *testing.T) {
	negotiated := []string{playback.FeaturePlaybackPlanV3, playback.FeatureHLSSubtitleRenditionsV3}
	features := withoutFailedSubtitleRoutesV3(negotiated, negotiated, playback.FailureSubtitleHLSRenditionFailedV3)
	if playback.HasFeatureV3(features, playback.FeatureHLSSubtitleRenditionsV3) {
		t.Fatal("a rendition the player could not select is offered again")
	}
	// Pinning on the next replan restores sticky features from the durable
	// request, which no longer carries the rendition.
	if playback.HasFeatureV3(playback.PinAttemptStickyFeaturesV3(negotiated, features), playback.FeatureHLSSubtitleRenditionsV3) {
		t.Fatal("attempt-sticky pinning re-enabled the failed rendition")
	}
	for _, classification := range []string{"", "network_error", "subtitle_embedded_failed"} {
		if !playback.HasFeatureV3(withoutFailedSubtitleRoutesV3(negotiated, negotiated, classification), playback.FeatureHLSSubtitleRenditionsV3) {
			t.Fatalf("%q dropped the rendition", classification)
		}
	}
}

// A rendition plan is routable only when one node both emits renditions and
// runs the plan's transformations; a pool that splits the two across nodes
// must keep the sidecar, which the node without the feature can run.
func TestSubtitleRenditionRoutableRequiresOneNodeForTheWholeRecipe(t *testing.T) {
	aac := playback.TransformationV3{
		Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3,
		RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3,
	}
	node := func(info playback.HWAccelInfo) string {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, info)
		}))
		t.Cleanup(server.Close)
		return server.URL
	}
	renditionOnly := node(playback.HWAccelInfo{TransportFeatures: []string{playback.TransportFeatureHLSSubtitleRenditionV1}})
	transformOnly := node(playback.HWAccelInfo{Transformations: []playback.TransformationV3{aac}})
	both := node(playback.HWAccelInfo{
		TransportFeatures: []string{playback.TransportFeatureHLSSubtitleRenditionV1},
		Transformations:   []playback.TransformationV3{aac},
	})

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	policy := config.DefaultPlaybackRoutingPolicy()
	policy.VideoTranscodeExecution = config.PlaybackExecutionWorkerOnly
	handler.PlaybackConfig = func() config.PlaybackConfig { return config.PlaybackConfig{Routing: policy} }
	result := playback.PlannerResultV3{
		Plan:              &playback.PlanV3{Delivery: playback.DeliveryTranscodeHLSV3, Transformations: []playback.TransformationV3{aac}},
		SubtitleRendition: true,
	}

	handler.NodePlanner = predicateEnumeratingNodePlannerV3{enumeratingNodePlannerV3{urls: []string{renditionOnly, transformOnly}}}
	if handler.subtitleRenditionRoutableV3(t.Context(), result, true) {
		t.Fatal("a pool with no node running the whole recipe was routable")
	}
	handler.NodePlanner = predicateEnumeratingNodePlannerV3{enumeratingNodePlannerV3{urls: []string{renditionOnly, both}}}
	if !handler.subtitleRenditionRoutableV3(t.Context(), result, true) {
		t.Fatal("a node running the whole recipe was not counted")
	}
}

// A copy remux's rendition runs under the remux routing policy and needs a
// node that advertises the remux rendition: a node that only emits a
// transcode's would attest no rendition for a remux.
func TestRemuxSubtitleRenditionNeedsTheRemuxTransportFeature(t *testing.T) {
	node := func(features ...string) string {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, playback.HWAccelInfo{TransportFeatures: features})
		}))
		t.Cleanup(server.Close)
		return server.URL
	}
	transcodeOnly := node(playback.TransportFeatureHLSSubtitleRenditionV1)
	remux := node(playback.TransportFeatureHLSSubtitleRenditionV1, playback.TransportFeatureHLSRemuxSubtitleRenditionV1)

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	policy := config.DefaultPlaybackRoutingPolicy()
	policy.RemuxExecution = config.PlaybackExecutionWorkerOnly
	policy.VideoTranscodeExecution = config.PlaybackExecutionWorkerOnly
	handler.PlaybackConfig = func() config.PlaybackConfig { return config.PlaybackConfig{Routing: policy} }
	result := playback.PlannerResultV3{
		Plan:              &playback.PlanV3{Delivery: playback.DeliveryRemuxHLSV3},
		PlayMethod:        playback.PlayRemux,
		TargetVideoCodec:  "copy",
		SubtitleRendition: true,
	}

	handler.NodePlanner = predicateEnumeratingNodePlannerV3{enumeratingNodePlannerV3{urls: []string{transcodeOnly}}}
	if handler.hlsSubtitleRenditionExecutableV3(t.Context(), true, playback.PlayRemux, false) || handler.subtitleRenditionRoutableV3(t.Context(), result, true) {
		t.Fatal("a node without the remux rendition was counted for a remux")
	}
	if !handler.hlsSubtitleRenditionExecutableV3(t.Context(), true, playback.PlayTranscode, false) {
		t.Fatal("the same node still emits a transcode's rendition")
	}
	handler.NodePlanner = predicateEnumeratingNodePlannerV3{enumeratingNodePlannerV3{urls: []string{transcodeOnly, remux}}}
	if !handler.hlsSubtitleRenditionExecutableV3(t.Context(), true, playback.PlayRemux, false) || !handler.subtitleRenditionRoutableV3(t.Context(), result, true) {
		t.Fatal("a node with the remux rendition was not counted")
	}
	eligible := handler.transcodeEligibilityV3(t.Context(), result, nil)
	if eligible(&nodepool.Node{URL: transcodeOnly}) || !eligible(&nodepool.Node{URL: remux}) {
		t.Fatal("node selection must admit only nodes with the remux rendition")
	}
}

// A track change among on-demand renditions keeps the stream: they are its
// playlists, and the selection is the player's. Only a different set, or a
// move to or from the pipeline rendition, needs another stream.
func TestSidecarOnlyHLSReplanKeepsOnDemandRenditions(t *testing.T) {
	tracks := func(selected uint64, numbers ...uint64) []playback.SubtitleRenditionTrack {
		var out []playback.SubtitleRenditionTrack
		for _, number := range numbers {
			out = append(out, playback.SubtitleRenditionTrack{TrackNumber: number, Name: fmt.Sprintf("Track %d", number), Default: number == selected})
		}
		return out
	}
	rendition := func(index int) *playback.HLSSubtitleRenditionV3 {
		return &playback.HLSSubtitleRenditionV3{GroupID: "subs", Name: "Subtitles", Format: "vtt", MIMEType: "text/vtt", Index: index}
	}
	freeze := func(plan *playback.PlanV3, track int, onDemand []playback.SubtitleRenditionTrack) playback.ExecutableRecipeV3 {
		return playback.FreezeExecutableRecipeV3(playback.PlannerResultV3{
			Plan: plan, PlayMethod: playback.PlayTranscode, TargetVideoCodec: "h264", TargetAudioCodec: "aac",
			SubtitleTrackIndex: track, SubtitleTransportTrackIndex: track, SubtitleCodec: "subrip",
			SubtitleRendition: track >= 0 || len(onDemand) > 0, SubtitleRenditionTracks: onDemand,
		})
	}
	for _, tc := range []struct {
		name               string
		currentTrack       int
		current            []playback.SubtitleRenditionTrack
		candidateTrack     int
		candidate          []playback.SubtitleRenditionTrack
		candidateRendition *playback.HLSSubtitleRenditionV3
		currentRendition   *playback.HLSSubtitleRenditionV3
		want               bool
	}{
		{"to another rendition", 0, tracks(3, 3, 4), 1, tracks(4, 3, 4), rendition(2), rendition(1), true},
		{"to off", 0, tracks(3, 3, 4), -1, tracks(0, 3, 4), nil, rendition(1), true},
		{"from off", -1, tracks(0, 3, 4), 0, tracks(3, 3, 4), rendition(1), nil, true},
		{"another set", 0, tracks(3, 3, 4), 0, tracks(3, 3), rendition(1), rendition(1), false},
		{"pipeline to on-demand", 0, nil, 0, tracks(3, 3, 4), rendition(1), rendition(0), false},
		{"on-demand to pipeline", 0, tracks(3, 3, 4), 2, nil, rendition(0), rendition(1), false},
		{"on-demand dropped", 0, tracks(3, 3, 4), -1, nil, nil, rendition(1), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			currentPlan := playback.PlanV3{PlanID: "current-plan", Delivery: playback.DeliveryTranscodeHLSV3,
				Stream: playback.StreamV3{URL: "/stream/current.m3u8"}, RequestedMediaFileID: 1, EffectiveMediaFileID: 1}
			candidatePlan := currentPlan
			candidatePlan.PlanID = "candidate-plan"
			currentPlan.Subtitle.HLSRendition, candidatePlan.Subtitle.HLSRendition = tc.currentRendition, tc.candidateRendition
			record := &playback.AttemptRecordV3{EffectiveMediaFileID: 1, CurrentPlan: currentPlan,
				FrozenRecipe: freeze(&currentPlan, tc.currentTrack, tc.current)}
			record.NormalizedRequest.ClientPlaybackContext.Output.OutputContextID = "output-context"
			candidateRecipe := freeze(&candidatePlan, tc.candidateTrack, tc.candidate)
			reusedRecipe, reused := sidecarOnlyHLSReplanV3(record, &candidatePlan, candidateRecipe, "output-context")
			if reused != tc.want {
				t.Fatalf("reused = %t, want %t", reused, tc.want)
			}
			if reused && !slices.Equal(reusedRecipe.SubtitleRenditionTracks, tc.candidate) {
				t.Fatalf("reused recipe tracks = %+v, want the candidate's selection %+v", reusedRecipe.SubtitleRenditionTracks, tc.candidate)
			}
		})
	}
}

// On-demand renditions need a node that serves them as well as the
// rendition feature for the plan's video target.
func TestOnDemandSubtitleRenditionsNeedTheOnDemandTransportFeature(t *testing.T) {
	node := func(features ...string) string {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, playback.HWAccelInfo{TransportFeatures: features})
		}))
		t.Cleanup(server.Close)
		return server.URL
	}
	pipelineOnly := node(playback.TransportFeatureHLSSubtitleRenditionV1, playback.TransportFeatureHLSRemuxSubtitleRenditionV1)
	onDemand := node(playback.TransportFeatureHLSSubtitleRenditionV1, playback.TransportFeatureHLSRemuxSubtitleRenditionV1,
		playback.TransportFeatureHLSOnDemandSubtitleRenditionsV1)

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	policy := config.DefaultPlaybackRoutingPolicy()
	policy.RemuxExecution = config.PlaybackExecutionWorkerOnly
	policy.VideoTranscodeExecution = config.PlaybackExecutionWorkerOnly
	handler.PlaybackConfig = func() config.PlaybackConfig { return config.PlaybackConfig{Routing: policy} }
	result := playback.PlannerResultV3{
		Plan:                    &playback.PlanV3{Delivery: playback.DeliveryTranscodeHLSV3},
		PlayMethod:              playback.PlayTranscode,
		TargetVideoCodec:        "h264",
		SubtitleRendition:       true,
		SubtitleRenditionTracks: []playback.SubtitleRenditionTrack{{TrackNumber: 3, Name: "English"}},
	}

	handler.NodePlanner = predicateEnumeratingNodePlannerV3{enumeratingNodePlannerV3{urls: []string{pipelineOnly}}}
	if handler.hlsSubtitleRenditionExecutableV3(t.Context(), true, playback.PlayTranscode, true) || handler.subtitleRenditionRoutableV3(t.Context(), result, true) {
		t.Fatal("a node without on-demand renditions was counted for them")
	}
	if !handler.hlsSubtitleRenditionExecutableV3(t.Context(), true, playback.PlayTranscode, false) {
		t.Fatal("the same node still emits the pipeline rendition")
	}
	handler.NodePlanner = predicateEnumeratingNodePlannerV3{enumeratingNodePlannerV3{urls: []string{pipelineOnly, onDemand}}}
	if !handler.hlsSubtitleRenditionExecutableV3(t.Context(), true, playback.PlayRemux, true) || !handler.subtitleRenditionRoutableV3(t.Context(), result, true) {
		t.Fatal("a node with on-demand renditions was not counted")
	}
	eligible := handler.transcodeEligibilityV3(t.Context(), result, nil)
	if eligible(&nodepool.Node{URL: pipelineOnly}) || !eligible(&nodepool.Node{URL: onDemand}) {
		t.Fatal("node selection must admit only nodes with on-demand renditions")
	}
}

// writeOnDemandRenditionSource writes a Matroska file with two SubRip tracks
// that FFmpeg cues, and returns it with its subtitle track numbers.
func writeOnDemandRenditionSource(t *testing.T) (string, []uint64) {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	dir := t.TempDir()
	var args []string
	for i, text := range []string{"One", "Uno"} {
		srt := filepath.Join(dir, fmt.Sprintf("%d.srt", i))
		if err := os.WriteFile(srt, []byte("1\n00:00:01,000 --> 00:00:02,000\n"+text+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		args = append(args, "-i", srt)
	}
	path := filepath.Join(dir, "movie.mkv")
	args = append([]string{"-v", "error", "-f", "lavfi", "-i", "color=c=black:s=16x16:r=1:d=4"}, args...)
	args = append(args, "-map", "0", "-map", "1", "-map", "2", "-c:v", "ffv1", "-c:s", "srt", path)
	if output, err := exec.CommandContext(t.Context(), ffmpeg, args...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, output)
	}
	idx, err := embeddedsub.ReadIndex(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	var numbers []uint64
	for _, track := range idx.Tracks {
		numbers = append(numbers, track.Number)
	}
	return path, numbers
}

// onDemandRenditionStartV3 starts, on /api/v2, an HLS copy remux of media,
// whose two SubRip tracks have container track numbers, for a client with
// every rendition feature, with the first track selected.
func onDemandRenditionStartV3(t *testing.T, media string, numbers []uint64) (*PlaybackHandler, playback.StartRequestV3, playback.DecisionResponseV3) {
	t.Helper()
	file := v3HandlerFixtureFile(t)
	file.Container, file.FilePath = "mkv", media
	file.SubtitleTracks = []models.SubtitleTrack{
		{Index: 1, ContainerTrackID: strconv.FormatUint(numbers[0], 10), Codec: "subrip", Language: "eng", Title: "English"},
		{Index: 2, ContainerTrackID: strconv.FormatUint(numbers[1], 10), Codec: "subrip", Language: "spa", Title: "Español"},
	}
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
	handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpeg(t), t.TempDir())
	presetLocalRegistryV3(handler, playback.NewTransformationRegistryV3(nil))
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "true"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}

	request := v3HandlerStartRequest()
	request.ClientFeatures = append(request.ClientFeatures, playback.FeatureHLSSubtitleRenditionsV3,
		playback.FeatureHLSRemuxSubtitleRenditionsV3, playback.FeatureHLSSubtitleRenditionsAllV3)
	request.Capabilities.Containers = []string{"m3u8"}
	request.ClientPlaybackContext.Deliveries = map[string]playback.DeliveryCapabilityV3{
		playback.DeliveryClassHLSV3: {Enabled: true, SupportedOnDevice: true,
			Subtitles: playback.DeliverySubtitleCapabilitiesV3{SidecarText: true, EmbeddedText: true}},
	}
	english := 0
	request.SubtitleTrackID, request.SubtitleTrackIndex = playback.TrackIDV3(file.ID, "subtitle", english), &english
	// The renditions and the inventory fields that place them are /api/v2's.
	started, err := handler.startPlaybackV2ForTest(t, request)
	if err != nil || started.PlaybackPlan == nil {
		t.Fatalf("start: %+v, %v", started, err)
	}
	t.Cleanup(func() { handler.tm.CloseTranscodeSession(started.SessionID, "") })
	return handler, request, started
}

// TestHandleReplanPlaybackV3SwitchesOnDemandRenditionsOnTheRunningStream
// starts an HLS copy remux that lists both of a file's embedded text tracks,
// then switches between them with track_change replans: the stream, its URL
// and its generation stay, and only the selection moves.
func TestHandleReplanPlaybackV3SwitchesOnDemandRenditionsOnTheRunningStream(t *testing.T) {
	media, numbers := writeOnDemandRenditionSource(t)
	handler, request, started := onDemandRenditionStartV3(t, media, numbers)
	english, spanish := 0, 1
	plan := started.PlaybackPlan
	if plan.Delivery != playback.DeliveryRemuxHLSV3 || plan.Subtitle.HLSRendition == nil || plan.Subtitle.HLSRendition.Index != 1 ||
		plan.Subtitle.HLSRendition.Name != "English" || !slices.Contains(started.ServerFeatures, playback.FeatureHLSSubtitleRenditionsAllV3) {
		t.Fatalf("start plan = %#v", plan)
	}
	for i, want := range []playback.SubtitleInventoryHLSRenditionV3{{GroupID: "subs", Name: "English", Index: 1}, {GroupID: "subs", Name: "Español", Index: 2}} {
		if got := plan.Subtitle.Inventory[i].HLSRendition; got == nil || *got != want {
			t.Fatalf("inventory row %d hls_rendition = %+v, want %+v", i, got, want)
		}
	}
	before := handler.tm.GetTranscodeSession(started.SessionID)
	if before == nil || len(before.Opts().SubtitleRenditionTracks) != 2 {
		t.Fatalf("stream lists no on-demand renditions: %#v", before)
	}
	master, err := before.BuildPlaybackManifest("segment/", "")
	if err != nil || strings.Count(string(master), "#EXT-X-MEDIA:TYPE=SUBTITLES") != 2 || !strings.Contains(string(master), `NAME="English",LANGUAGE="en",DEFAULT=YES`) {
		t.Fatalf("multivariant playlist (%v):\n%s", err, master)
	}
	beforeOpts := before.Opts()

	for i, selected := range []int{spanish, english} {
		next := switchOnDemandRenditionV3(t, handler, request, started.SessionID, plan, i, &selected, false)
		if next.Stream.URL != started.PlaybackPlan.Stream.URL || next.Subtitle.HLSRendition == nil || next.Subtitle.HLSRendition.Index != selected+1 {
			t.Fatalf("switch to %d: plan = %#v", selected, next)
		}
		after := handler.tm.GetTranscodeSession(started.SessionID)
		if after != before || !after.IsRunning() {
			t.Fatalf("switch to %d replaced the stream: before=%p after=%p", selected, before, after)
		}
		if afterOpts := after.Opts(); afterOpts.OutputDir != beforeOpts.OutputDir || afterOpts.StartSegmentNumber != beforeOpts.StartSegmentNumber {
			t.Fatalf("switch to %d started another generation", selected)
		}
		record, err := handler.PlanStoreV3.GetAttempt(t.Context(), started.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		if tracks := record.FrozenRecipe.SubtitleRenditionTracks; len(tracks) != 2 || !tracks[selected].Default || tracks[1-selected].Default {
			t.Fatalf("switch to %d: durable rendition tracks = %+v", selected, tracks)
		}
		plan = next
	}
}

// switchOnDemandRenditionV3 reports the client's switch to subtitle ordinal
// selected, or Off when it is nil, with the n-th replan on /api/v2 and
// returns the plan. The replan is a track_change, or a failure_recovery
// classified subtitle_track_changed when recovery is set.
func switchOnDemandRenditionV3(t *testing.T, handler *PlaybackHandler, request playback.StartRequestV3, sessionID string, plan *playback.PlanV3, n int, selected *int, recovery bool) *playback.PlanV3 {
	t.Helper()
	replan := playback.ReplanRequestV3{
		ProtocolVersion: playback.ProtocolV3, Operation: playback.ReplanOperationTrackChangeV3,
		PlaybackAttemptID: request.PlaybackAttemptID, ReplanRequestID: fmt.Sprintf("rendition-switch-%04d", n),
		FailedPlanID: plan.PlanID, PlanAttemptID: fmt.Sprintf("rendition-plan-attempt-%04d", n), PlanAttemptKey: plan.PlanAttemptKey,
		AttemptCount: 1, PositionSeconds: 30,
		SelectedTracks: playback.SelectedTracksV3{Audio: plan.SelectedTracks.Audio},
		Capabilities:   request.Capabilities, ClientPlaybackContext: request.ClientPlaybackContext,
	}
	if selected != nil {
		replan.SelectedTracks.Subtitle = &playback.TrackIdentityV3{ID: playback.TrackIDV3(request.FileID, "subtitle", *selected), Index: selected}
	}
	if recovery {
		replan.Operation, replan.Failure = playback.ReplanOperationFailureRecoveryV3, playback.FailureV3{Classification: "subtitle_track_changed"}
	}
	caller := PlaybackCaller{UserID: 1, ProfileID: "profile-1", InstallationID: serviceInstallation}
	response, err := handler.ReplanPlaybackV2(newAuthorizedPlaybackContext(), caller, sessionID,
		PlaybackReplanCommand{Request: replan, Digest: fmt.Sprintf("rendition-switch-digest-%d", n)})
	if err != nil || response.PlaybackPlan == nil {
		t.Fatalf("replan: %+v, %v", response, err)
	}
	return response.PlaybackPlan
}

// A client may report its own switch among on-demand renditions as a
// subtitle_track_changed recovery. It moves the selection as a track_change
// does, on the same stream, and naming no track turns subtitles off.
func TestHandleReplanPlaybackV3SubtitleTrackChangedRecoveryKeepsOnDemandRenditions(t *testing.T) {
	media, numbers := writeOnDemandRenditionSource(t)
	handler, request, started := onDemandRenditionStartV3(t, media, numbers)
	before := handler.tm.GetTranscodeSession(started.SessionID)
	spanish := 1
	plan := switchOnDemandRenditionV3(t, handler, request, started.SessionID, started.PlaybackPlan, 0, &spanish, true)
	if plan.Stream.URL != started.PlaybackPlan.Stream.URL || plan.EffectiveMediaFileID != started.PlaybackPlan.EffectiveMediaFileID ||
		plan.Subtitle.HLSRendition == nil || plan.Subtitle.HLSRendition.Index != 2 {
		t.Fatalf("recovery to the second rendition: plan = %#v", plan)
	}
	off := switchOnDemandRenditionV3(t, handler, request, started.SessionID, plan, 1, nil, true)
	if off.Stream.URL != started.PlaybackPlan.Stream.URL || off.Subtitle.Mode != playback.SubtitleOffV3 || off.Subtitle.HLSRendition != nil ||
		off.SelectedTracks.Subtitle != nil {
		t.Fatalf("recovery naming no track: plan = %#v", off)
	}
	for i, item := range off.Subtitle.Inventory {
		if item.HLSRendition == nil || item.HLSRendition.Index != i+1 {
			t.Fatalf("subtitles off dropped row %d's rendition: %+v", i, item.HLSRendition)
		}
	}
	if after := handler.tm.GetTranscodeSession(started.SessionID); after != before || !after.IsRunning() {
		t.Fatalf("recoveries replaced the stream: before=%p after=%p", before, after)
	}
}

// A stream that started on the single pipeline rendition, here because the
// index couldn't be read then, keeps running when subtitles are turned off,
// even though the index can be read by now and a fresh plan would list
// on-demand renditions.
func TestHandleReplanPlaybackV3TurnsOffThePipelineRenditionOnTheRunningStream(t *testing.T) {
	media, numbers := writeOnDemandRenditionSource(t)
	unread := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(unread, []byte("not yet readable"), 0o644); err != nil {
		t.Fatal(err)
	}
	handler, request, started := onDemandRenditionStartV3(t, unread, numbers)
	if started.PlaybackPlan.Subtitle.HLSRendition == nil || started.PlaybackPlan.Subtitle.HLSRendition.Index != 0 {
		t.Fatalf("start plan = %#v, want the pipeline rendition", started.PlaybackPlan.Subtitle)
	}
	data, err := os.ReadFile(media)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unread, data, 0o644); err != nil {
		t.Fatal(err)
	}
	before := handler.tm.GetTranscodeSession(started.SessionID)
	off := switchOnDemandRenditionV3(t, handler, request, started.SessionID, started.PlaybackPlan, 0, nil, false)
	if off.Stream.URL != started.PlaybackPlan.Stream.URL || off.Subtitle.Mode != playback.SubtitleOffV3 {
		t.Fatalf("subtitles off: plan = %#v", off)
	}
	if after := handler.tm.GetTranscodeSession(started.SessionID); after != before || !after.IsRunning() {
		t.Fatalf("turning the pipeline rendition off replaced the stream: before=%p after=%p", before, after)
	}
}

// A card rebuilt from a stream token knows its on-demand renditions by
// number. Moving the transport gets their names, languages and FORCED back
// from the attempt's frozen recipe; a token for other tracks keeps numbers.
func TestRehomeRestoresOnDemandRenditionNamesFromTheRecord(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	source := playback.SubtitleRenditionSource{Size: 4096, ModTimeUnixNano: 1_800_000_000_000_000_000}
	frozen := []playback.SubtitleRenditionTrack{
		{StreamIndex: 2, TrackNumber: 3, Name: "English", Language: "eng"},
		{StreamIndex: 3, TrackNumber: 4, Name: "English (2)", Language: "eng", Forced: true, Default: true},
	}
	if err := handler.PlanStoreV3.SaveAttempt(t.Context(), playback.AttemptRecordV3{
		PlaybackAttemptID: "attempt-rehome", SessionID: "session-rehome", ExpiresAt: time.Now().Add(time.Hour),
		FrozenRecipe: playback.ExecutableRecipeV3{SubtitleRendition: true, SubtitleRenditionTracks: frozen, SubtitleRenditionSource: source},
	}); err != nil {
		t.Fatal(err)
	}
	session := &playback.Session{ID: "session-rehome", UserID: 7}
	tokenCard := func(tracks []playback.SubtitleRenditionTrack) *playback.RecipeCard {
		opts := playback.TranscodeOpts{SessionID: session.ID, InputPath: "/media/movie.mkv", TargetCodecVideo: "h264", SegmentDuration: 2,
			SubtitleRendition: true, SubtitleTrackIndex: -1, SubtitleRenditionTracks: tracks, SubtitleRenditionSource: source}
		claims := playback.NewRecipeCard(session.UserID, "profile", 42, "", opts).ToClaims()
		card := playback.RecipeCardFromClaims(&claims)
		return &card
	}
	card, stored := handler.rehomeRecipeCardV3(t.Context(), session, session.ID, tokenCard(frozen))
	if card == nil || stored || !slices.Equal(card.SubtitleRenditionTracks, frozen) {
		t.Fatalf("rehomed renditions = %+v", card)
	}
	other := []playback.SubtitleRenditionTrack{{TrackNumber: 3}, {TrackNumber: 5}}
	card, _ = handler.rehomeRecipeCardV3(t.Context(), session, session.ID, tokenCard(other))
	if card == nil || card.SubtitleRenditionTracks[0].Name != "" || card.SubtitleRenditionTracks[1].TrackNumber != 5 {
		t.Fatalf("renditions of another plan = %+v", card)
	}
}
