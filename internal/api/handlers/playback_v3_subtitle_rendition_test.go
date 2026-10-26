package handlers

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
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
	for _, feature := range []string{playback.FeatureHLSSubtitleRenditionsV3, playback.FeatureHLSRemuxSubtitleRenditionsV3} {
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
	if handler.hlsSubtitleRenditionExecutableV3(t.Context(), true, playback.PlayRemux) || handler.subtitleRenditionRoutableV3(t.Context(), result, true) {
		t.Fatal("a node without the remux rendition was counted for a remux")
	}
	if !handler.hlsSubtitleRenditionExecutableV3(t.Context(), true, playback.PlayTranscode) {
		t.Fatal("the same node still emits a transcode's rendition")
	}
	handler.NodePlanner = predicateEnumeratingNodePlannerV3{enumeratingNodePlannerV3{urls: []string{transcodeOnly, remux}}}
	if !handler.hlsSubtitleRenditionExecutableV3(t.Context(), true, playback.PlayRemux) || !handler.subtitleRenditionRoutableV3(t.Context(), result, true) {
		t.Fatal("a node with the remux rendition was not counted")
	}
	eligible := handler.transcodeEligibilityV3(t.Context(), result, nil)
	if eligible(&nodepool.Node{URL: transcodeOnly}) || !eligible(&nodepool.Node{URL: remux}) {
		t.Fatal("node selection must admit only nodes with the remux rendition")
	}
}
