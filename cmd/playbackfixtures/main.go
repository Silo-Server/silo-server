// Command playbackfixtures writes the protocol-v3 golden contract fixtures
// from the live Go types and planner.
//
// The direction of authority is inverted from where it started: the server
// defines the playback protocol, and clients prove conformance against these
// files. Android and Apple CI vendor them and compare their own decoders and
// opaque-token echo behavior against the values here, so a fixture is
// only trustworthy if it was produced by the same code that serves real
// traffic — hand-editing one would let the contract and the implementation
// drift apart silently.
//
// Usage:
//
//	go run ./cmd/playbackfixtures -out internal/playback/testdata/protocol_v3
//
// `make playback-fixtures` runs it; `make verify-playback-fixtures`
// regenerates into a temporary directory and diffs, so a contract change
// cannot merge without refreshing what every client tests against.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

// goldenSessionID is a fixed UUID: fixtures must be byte-stable across runs,
// so nothing in this generator may read the clock or a random source.
const (
	goldenSessionID   = "11111111-1111-4111-8111-111111111111"
	goldenAttemptID   = "attempt-golden-0001"
	goldenExpiresAt   = "2030-01-01T00:00:00Z"
	goldenMediaFileID = 42
	// Codec and container tokens the fixtures are built from. Named so the
	// capability lists, the plan recipe, and the source descriptor cannot drift
	// into describing different media by a one-character typo.
	codecH264         = "h264"
	codecHEVC         = "hevc"
	codecAAC          = "aac"
	codecTrueHD       = "truehd"
	containerMP4      = "mp4"
	containerMKV      = "mkv"
	containerHLS      = "hls"
	profileHigh       = "high"
	resolutionHD      = "720p"
	resolutionFHD     = "1080p"
	qualityOriginal   = "original"
	audioLayoutStereo = "stereo"
	audioLayout71     = "7.1"
	filmFrameRate     = "24000/1001"
	languageEnglish   = "eng"
	videoRangeSDR     = "SDR"
	categoryMidSeek   = "mid_seek_replan"
)

func main() {
	out := flag.String("out", "", "directory to write fixtures into (required)")
	flag.Parse()
	if *out == "" {
		fail("usage: playbackfixtures -out <dir>")
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fail("create %s: %v", *out, err)
	}

	write(*out, "start_request.json", goldenStartRequest())
	write(*out, "replan_request.json", goldenReplanRequest())
	write(*out, "decision_response.json", goldenDecisionResponse())
	write(*out, "native-decision_response.json", goldenNativeDecisionResponse())
	write(*out, "capability_response.json", goldenCapabilityResponse())
	write(*out, "error_response.json", goldenErrorResponse())
	write(*out, "route_event.json", goldenRouteEvent())
	write(*out, "subtitle_inventory.json", goldenSubtitleInventory())
	write(*out, "attempt_keys.json", goldenAttemptKeys())
	write(*out, "conformance_matrix.json", goldenConformanceMatrix())
}

func goldenErrorResponse() playback.ErrorResponseV3 {
	return playback.LegacyUpgradeErrorV3()
}

func write(dir, name string, value any) {
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		fail("marshal %s: %v", name, err)
	}
	body = append(body, '\n')
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, body, 0o644); err != nil { //nolint:gosec // generated fixture
		fail("write %s: %v", path, err)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

// goldenCapabilities is the exact-evidence Android-shaped capability block the
// request fixtures share. Both request bodies carry the same capability
// contract, so they are built from one function rather than two drifting
// literals.
func goldenCapabilities() playback.ClientCodecCapabilitiesV3 {
	return playback.ClientCodecCapabilitiesV3{
		VideoEvidence:       playback.EvidenceExactV3,
		AudioEvidence:       playback.EvidenceExactV3,
		CodecsVideo:         []string{codecH264},
		CodecsVideoHardware: []string{codecH264},
		CodecsAudio:         []string{codecAAC},
		Containers:          []string{containerMP4},
		MaxResolution:       resolutionFHD,
		VideoDecode: []playback.VideoDecodeCapabilityV3{{
			Codec:          codecH264,
			Profiles:       []string{profileHigh},
			Levels:         []int{41},
			BitDepths:      []int{8},
			MaxWidth:       1920,
			MaxHeight:      1080,
			MaxFrameRate:   60,
			MaxBitrateKbps: 20_000,
			Hardware:       true,
		}},
	}
}

func goldenPlaybackContext() playback.ClientPlaybackContextV3 {
	return playback.ClientPlaybackContextV3{
		ProtocolVersion: playback.ProtocolV3,
		FormFactor:      "tv",
		AppVersion:      "3.0-test",
		Device: playback.DeviceContextV3{
			Platform:     "android",
			OSVersion:    "15",
			Manufacturer: "NVIDIA",
			Model:        "SHIELD Android TV",
			// Everything platform-specific travels here as opaque bounded
			// strings; the contract itself stays neutral.
			PlatformDetails: map[string]string{"sdk_int": "35", "abis": "arm64-v8a"},
		},
		Output: playback.OutputContextV3{OutputContextID: "7"},
		Deliveries: map[string]playback.DeliveryCapabilityV3{
			playback.DeliveryClassOriginalHTTPV3: {
				Enabled:                true,
				SupportedOnDevice:      true,
				Containers:             []string{containerMP4},
				VideoCodecs:            []string{codecH264},
				AudioDecodeCodecs:      []string{codecAAC},
				AudioPassthroughCodecs: []string{},
				Subtitles: playback.DeliverySubtitleCapabilitiesV3{
					EmbeddedText: true,
					SidecarText:  true,
				},
				Features:          []string{},
				AuthHeaderRefresh: true,
				ValidatedClaims:   []string{},
				Transformations:   []playback.TransformationV3{},
			},
		},
	}
}

func goldenStartRequest() playback.StartRequestV3 {
	start := 12.5
	audioIndex := 0
	return playback.StartRequestV3{
		ProtocolVersion:            playback.ProtocolV3,
		ClientFeatures:             []string{playback.FeaturePlaybackPlanV3},
		FileID:                     goldenMediaFileID,
		ProfileID:                  "profile-1",
		PlaybackAttemptID:          goldenAttemptID,
		QualityPreference:          playback.QualityOriginalV3,
		SubtitleFidelityPreference: playback.SubtitleFidelityCompatibleV3,
		StartPosition:              &start,
		ProgressPersistence:        playback.ProgressPersistenceClientV3,
		AudioTrackID:               playback.TrackIDV3(goldenMediaFileID, "audio", 0),
		AudioTrackIndex:            &audioIndex,
		Capabilities:               goldenCapabilities(),
		ClientPlaybackContext:      goldenPlaybackContext(),
	}
}

func goldenReplanRequest() playback.ReplanRequestV3 {
	estimate := 3_500
	cap := 4_000
	audioIndex := 0
	decision := goldenDecisionResponse()
	if decision.PlaybackPlan == nil {
		fail("golden decision has no plan")
	}
	plan := decision.PlaybackPlan
	return playback.ReplanRequestV3{
		ProtocolVersion:   playback.ProtocolV3,
		ClientFeatures:    []string{playback.FeaturePlaybackPlanV3},
		Operation:         playback.ReplanOperationFailureRecoveryV3,
		PlaybackAttemptID: goldenAttemptID,
		ReplanRequestID:   "replan-golden-0001",
		FailedPlanID:      plan.PlanID,
		PlanAttemptID:     "plan-attempt-golden-0001",
		// Attempt keys are server-owned opaque tokens. A client echoes the
		// values it was handed; it never computes one.
		PlanAttemptKey:        plan.PlanAttemptKey,
		AttemptedPlanKeys:     []string{plan.PlanAttemptKey},
		AttemptCount:          1,
		QualityPreference:     "auto",
		PositionSeconds:       42.5,
		Metered:               true,
		BandwidthEstimateKbps: &estimate,
		BandwidthCapKbps:      &cap,
		SelectedTracks: playback.SelectedTracksV3{
			Audio: &playback.TrackIdentityV3{ID: playback.TrackIDV3(goldenMediaFileID, "audio", 0), Index: &audioIndex},
		},
		Failure:               playback.FailureV3{Classification: "network_degraded"},
		Capabilities:          goldenCapabilities(),
		ClientPlaybackContext: goldenPlaybackContext(),
	}
}

// goldenMediaFile is the subtitle-bearing source the inventory fixtures
// describe. The embedded track list deliberately mixes a text track, a PGS
// track, and a DVD bitmap track: the DVD track has no sidecar shape the stream
// handler can serve, and pinning its ordinal is the point of the fixture.
func goldenMediaFile() *models.MediaFile {
	return &models.MediaFile{
		ID: goldenMediaFileID,
		ExternalSubtitles: []models.ExternalSubtitle{
			{Path: "/library/movie.en.srt", Language: languageEnglish, Format: "srt", Title: "English"},
		},
		SubtitleTracks: []models.SubtitleTrack{
			{Index: 0, Language: languageEnglish, Codec: "ass", Title: "English (Signs)", Forced: true},
			{Index: 1, Language: "jpn", Codec: "pgs", Title: "Japanese"},
			{Index: 2, Language: "fre", Codec: "dvd_subtitle", Title: "French"},
		},
	}
}

func goldenSubtitleAdditional() []playback.SubtitleInventoryEntryV3 {
	return []playback.SubtitleInventoryEntryV3{
		{Codec: "srt", Language: "spa", Label: "Spanish (downloaded)", Source: playback.SubtitleSourceDownloadedV3},
	}
}

// subtitleInventoryFixture is a self-describing conformance vector: the inputs
// a client would receive for the same file, and the ordinals the server
// assigns to them. A client reproduces `inventory` from `source` and compares.
type subtitleInventoryFixture struct {
	Description string                             `json:"description"`
	SessionID   string                             `json:"session_id"`
	MediaFileID int                                `json:"media_file_id"`
	Source      subtitleInventorySource            `json:"source"`
	Inventory   []playback.SubtitleInventoryItemV3 `json:"inventory"`
}

type subtitleInventorySource struct {
	ExternalSubtitles []models.ExternalSubtitle           `json:"external_subtitles"`
	SubtitleTracks    []models.SubtitleTrack              `json:"subtitle_tracks"`
	Downloaded        []playback.SubtitleInventoryEntryV3 `json:"downloaded"`
}

func goldenSubtitleInventory() subtitleInventoryFixture {
	file := goldenMediaFile()
	additional := goldenSubtitleAdditional()
	return subtitleInventoryFixture{
		Description: "Combined subtitle ordinals are dense and gap-free across externals, embedded tracks, " +
			"then downloaded tracks. A track with no sidecar representation keeps its ordinal and is " +
			"published as burn_in_only without a URL rather than omitted.",
		SessionID:   goldenSessionID,
		MediaFileID: goldenMediaFileID,
		Source: subtitleInventorySource{
			ExternalSubtitles: file.ExternalSubtitles,
			SubtitleTracks:    file.SubtitleTracks,
			Downloaded:        additional,
		},
		Inventory: playback.SubtitleInventoryV3(goldenSessionID, file, additional),
	}
}

func goldenDecisionResponse() playback.DecisionResponseV3 {
	file := conformanceFallbackFile()
	subtitleSource := goldenMediaFile()
	file.ExternalSubtitles = subtitleSource.ExternalSubtitles
	file.SubtitleTracks = subtitleSource.SubtitleTracks
	return goldenDecisionForSource(file, goldenStartRequest(), goldenSubtitleAdditional())
}

func goldenNativeDecisionResponse() playback.DecisionResponseV3 {
	file := conformanceFallbackFile()
	file.ExternalSubtitles = goldenMediaFile().ExternalSubtitles
	file.SubtitleTracks = []models.SubtitleTrack{
		{Index: 3, ContainerTrackID: "4", Language: languageEnglish, Codec: "mov_text", Title: "English (embedded)"},
	}
	request := goldenStartRequest()
	request.ClientFeatures = append(request.ClientFeatures, playback.FeatureEmbeddedSubtitlesV3)
	request.SubtitleTrackIndex = new(len(file.ExternalSubtitles))
	request.SubtitleTrackID = playback.TrackIDV3(file.ID, "subtitle", *request.SubtitleTrackIndex)
	delivery := request.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3]
	delivery.Subtitles.NativeEmbedded = []playback.NativeEmbeddedSubtitleCapabilityV3{{
		Container: containerMP4, Codecs: []string{"mov_text"}, TrackIdentity: "container_track_id",
	}}
	request.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3] = delivery
	return goldenDecisionForSource(file, request, nil)
}

func goldenDecisionForSource(file *models.MediaFile, request playback.StartRequestV3, additional []playback.SubtitleInventoryEntryV3) playback.DecisionResponseV3 {
	now, err := time.Parse(time.RFC3339, "2029-12-31T23:55:00Z")
	if err != nil {
		fail("parse golden planner time: %v", err)
	}
	result := playback.PlanPlaybackV3(playback.PlannerInputV3{
		Request:             request,
		RequestedFile:       file,
		EffectiveFile:       file,
		AudioTrackIndex:     0,
		Settings:            playback.PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true},
		Registry:            conformanceRegistry(),
		Now:                 now,
		AdditionalSubtitles: additional,
	})
	if result.Plan == nil {
		fail("golden planner returned terminal: %#v", result.Terminal)
	}
	plan := *result.Plan
	// These values belong to session/transport setup, which runs after the
	// planner in the request handler. Keep the planner authoritative for every
	// decision field and bind only the deterministic fixture runtime values.
	plan.SessionID = goldenSessionID
	plan.ExpiresAt = goldenExpiresAt
	plan.Stream.URL = "/stream/" + goldenSessionID
	plan.Subtitle.Inventory = playback.ScopeSubtitleInventoryV3(goldenSessionID, file, plan.Subtitle.Inventory)

	return playback.DecisionResponseV3{
		ProtocolVersion: playback.ProtocolV3,
		ServerFeatures:  playback.ServerFeaturesV3(),
		Outcome:         playback.OutcomePlayableV3,
		SessionID:       goldenSessionID,
		PlaybackPlan:    &plan,
	}
}

// goldenCapabilityResponse returns the stable protocol capability fixture.
func goldenCapabilityResponse() playback.CapabilityResponseV3 {
	return playback.CapabilityResponseV3{
		Enabled:          true,
		ProtocolVersions: []int{playback.ProtocolV3},
		Features:         playback.ServerFeaturesV3(),
		Deliveries: []playback.DeliveryV3{
			playback.DeliveryOriginalHTTPV3,
			playback.DeliveryRemuxProgressiveV3,
			playback.DeliveryRemuxHLSV3,
			playback.DeliveryTranscodeHLSV3,
		},
		// A real server advertises only what its installed FFmpeg probed; the
		// fixture pins the full set so a client sees every shape it must parse.
		Transformations: []playback.TransformationV3{
			{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3, ValidatedClaims: []string{playback.ClaimAudioDecodeV3}},
			{Name: playback.TransformationHDRToSDRToneMapV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3, ValidatedClaims: []string{playback.ClaimHDRMetadataRemovedV3, playback.ClaimSDRBT709OutputV3}},
			{Name: playback.TransformationServerDV7HDR10V3, Executor: playback.ExecutorServerV3, RecipeVersion: "1", ValidatedClaims: playback.DV7ToHDR10ClaimsV3()},
			{Name: playback.TransformationVideoToH264V3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationVideoToH264RecipeVersionV3, ValidatedClaims: []string{playback.ClaimH264DecodeV3}},
		},
	}
}

func goldenRouteEvent() playback.RouteEventV3 {
	decision := goldenDecisionResponse()
	if decision.PlaybackPlan == nil {
		fail("golden decision has no plan")
	}
	plan := decision.PlaybackPlan
	return playback.RouteEventV3{
		ProtocolVersion:   playback.ProtocolV3,
		PlaybackAttemptID: goldenAttemptID,
		SessionID:         goldenSessionID,
		PlanID:            plan.PlanID,
		PlanAttemptID:     "plan-attempt-golden-0001",
		PlanAttemptKey:    plan.PlanAttemptKey,
		Event:             playback.RouteEventFirstFrameV3,
		OutputContextID:   "7",
		Diagnostics: map[string]string{
			"decoder_name":   "c2.android.avc.decoder",
			"first_frame_ms": "412",
			"video_mime":     "video/avc",
		},
	}
}

// attemptKeyInput exists only inside the Go fixture generator. The inputs to
// the server's identity function are deliberately absent from the generated
// JSON: clients receive a token and prove only that they echo it unchanged.
type attemptKeyInput struct {
	Name               string                      `json:"name"`
	PlanID             string                      `json:"plan_id"`
	Delivery           playback.DeliveryV3         `json:"delivery"`
	StreamProtocol     playback.StreamProtocolV3   `json:"stream_protocol"`
	Container          string                      `json:"container"`
	VideoCodec         string                      `json:"video_codec"`
	AudioCodec         string                      `json:"audio_codec"`
	Width              int                         `json:"width"`
	Height             int                         `json:"height"`
	BitrateKbps        int                         `json:"bitrate_kbps"`
	DynamicRange       string                      `json:"dynamic_range"`
	SubtitleMode       playback.SubtitleModeV3     `json:"subtitle_mode"`
	Transformations    []playback.TransformationV3 `json:"transformations"`
	AppliedQuirks      []playback.AppliedQuirkV3   `json:"applied_quirks,omitempty"`
	RuntimeCorrections []string                    `json:"runtime_corrections,omitempty"`
	OutputContextID    string                      `json:"output_context_id"`
	LocalMutations     []string                    `json:"local_mutations"`
}

func (f attemptKeyInput) plan() playback.PlanV3 {
	width, height, bitrate := f.Width, f.Height, f.BitrateKbps
	return playback.PlanV3{
		PlanID:   f.PlanID,
		Delivery: f.Delivery,
		Stream:   playback.StreamV3{Protocol: f.StreamProtocol, Container: f.Container},
		EffectiveRecipe: playback.EffectiveRecipeV3{
			VideoCodec:   f.VideoCodec,
			AudioCodec:   f.AudioCodec,
			Width:        &width,
			Height:       &height,
			BitrateKbps:  &bitrate,
			DynamicRange: f.DynamicRange,
		},
		Subtitle:           playback.SubtitleDecisionV3{Mode: f.SubtitleMode},
		Transformations:    f.Transformations,
		AppliedQuirks:      f.AppliedQuirks,
		RuntimeCorrections: f.RuntimeCorrections,
	}
}

type opaqueAttemptKeyFixture struct {
	Name                 string   `json:"name"`
	ServerPlanAttemptKey string   `json:"server_plan_attempt_key"`
	ReplanEcho           string   `json:"replan_echo"`
	AttemptedPlanKeys    []string `json:"attempted_plan_keys"`
	ExpectedServerAction string   `json:"expected_server_action"`
}

func goldenAttemptKeys() []opaqueAttemptKeyFixture {
	inputs := []attemptKeyInput{
		{
			Name:           "hls_burn_in_sorted_transformations_and_pcm_mutations",
			PlanID:         "plan:fixture",
			Delivery:       playback.DeliveryRemuxHLSV3,
			StreamProtocol: playback.StreamHLSV3,
			Container:      containerHLS,
			VideoCodec:     codecHEVC,
			AudioCodec:     codecAAC,
			Width:          3840,
			Height:         2160,
			BitrateKbps:    20_000,
			DynamicRange:   playback.DynamicRangeHDR10V3,
			SubtitleMode:   playback.SubtitleBurnInV3,
			// Deliberately out of order, and deliberately naming a
			// transformation the registry no longer defines: the key preimage
			// sorts its inputs and never consults the registry, and pinning a
			// retired name proves both properties stay true.
			Transformations: []playback.TransformationV3{
				{Name: "hdr_to_sdr_tonemap", Executor: playback.ExecutorServerV3, RecipeVersion: "1", ValidatedClaims: []string{}},
				{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3, ValidatedClaims: []string{}},
			},
			OutputContextID: "7",
			LocalMutations:  []string{"transport_reopen", "pcm:truehd:8"},
		},
		{
			Name:           "direct_client_dv81_executor_and_version",
			PlanID:         "plan:dv81-fixture",
			Delivery:       playback.DeliveryOriginalHTTPV3,
			StreamProtocol: playback.StreamHTTPProgressiveV3,
			Container:      containerMKV,
			VideoCodec:     codecHEVC,
			AudioCodec:     codecTrueHD,
			Width:          3840,
			Height:         2160,
			BitrateKbps:    65_000,
			DynamicRange:   playback.DynamicRangeDolbyVisionV3,
			SubtitleMode:   playback.SubtitleOffV3,
			Transformations: []playback.TransformationV3{
				{Name: playback.ClientDV7ToDV81V3, Executor: playback.ExecutorClientV3, RecipeVersion: playback.ClientDVTransformVersionV3, ValidatedClaims: []string{}},
			},
			OutputContextID: "9",
			LocalMutations:  []string{},
		},
		{
			Name:            "direct_device_quirk_and_runtime_correction_identity",
			PlanID:          "plan:quirk",
			Delivery:        playback.DeliveryOriginalHTTPV3,
			StreamProtocol:  playback.StreamHTTPProgressiveV3,
			Container:       containerMKV,
			VideoCodec:      codecHEVC,
			AudioCodec:      "eac3",
			Width:           3840,
			Height:          2160,
			BitrateKbps:     60_000,
			DynamicRange:    playback.DynamicRangeDolbyVisionV3,
			SubtitleMode:    playback.SubtitleOffV3,
			Transformations: []playback.TransformationV3{},
			// The private server implementation includes quirk and runtime
			// correction identity; clients see only its opaque result.
			AppliedQuirks: []playback.AppliedQuirkV3{{
				ID:               playback.QuirkFireTVDV8HDR10PlusV3,
				RegistryRevision: playback.DeviceQuirkRegistryRevisionV3,
				Action:           "client_runtime_correction",
			}},
			RuntimeCorrections: []string{playback.ClientDV8HDR10PlusSanitizerV3},
			OutputContextID:    "9",
			LocalMutations:     []string{},
		},
	}
	fixtures := make([]opaqueAttemptKeyFixture, 0, len(inputs))
	for _, input := range inputs {
		key := playback.PlanAttemptKeyV3(input.plan(), input.OutputContextID, input.LocalMutations)
		fixtures = append(fixtures, opaqueAttemptKeyFixture{
			Name:                 input.Name,
			ServerPlanAttemptKey: key,
			ReplanEcho:           key,
			AttemptedPlanKeys:    []string{key},
			ExpectedServerAction: "reject_already_attempted_plan",
		})
	}
	return fixtures
}

// goldenConformanceMatrix returns the deterministic planner conformance matrix.
func goldenConformanceMatrix() playback.ConformanceMatrixV3 {
	videoFile := conformanceVideoFile()
	base := conformanceStartRequest()
	registry := conformanceRegistry()
	settings := playback.PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}

	planner := make([]playback.PlannerScenarioV3, 0, 18)
	for _, tier := range []playback.CapabilityEvidenceV3{
		playback.EvidenceExactV3,
		playback.EvidencePlatformAttestedV3,
		playback.EvidenceDeclaredV3,
	} {
		request := base
		request.PlaybackAttemptID = "attempt-evidence-" + string(tier)
		request.Capabilities.VideoEvidence = tier
		if tier == playback.EvidenceDeclaredV3 {
			request.Capabilities.VideoDecode = nil
		}
		planner = append(planner, makePlannerScenario(
			"evidence_"+string(tier), "evidence_tier_gating", request, videoFile, nil, settings, registry,
		))
	}

	constrainedBaselineFile := conformanceFallbackFile()
	constrainedBaselineFile.Container = containerMKV
	constrainedBaselineFile.FilePath = "/media/constrained-baseline.mkv"
	constrainedBaselineFile.VideoTracks[0].Profile = "Constrained Baseline"
	constrainedBaselineRequest := conformanceStartRequest()
	constrainedBaselineRequest.PlaybackAttemptID = "attempt-h264-constrained-baseline"
	constrainedBaselineRequest.QualityPreference = "auto"
	constrainedBaselineRequest.Capabilities.CodecsVideo = []string{codecH264}
	constrainedBaselineRequest.Capabilities.CodecsVideoHardware = []string{codecH264}
	constrainedBaselineRequest.Capabilities.Containers = []string{containerMKV}
	constrainedBaselineRequest.Capabilities.VideoDecode = []playback.VideoDecodeCapabilityV3{{
		Codec: codecH264, Profiles: []string{"baseline"}, Levels: []int{52}, BitDepths: []int{8},
		MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 20_000, Hardware: true,
	}}
	planner = append(planner, makePlannerScenario(
		"h264_constrained_baseline_direct", "profile_compatibility", constrainedBaselineRequest,
		constrainedBaselineFile, nil, playback.PlannerSettingsV3{TranscodeEnabled: false}, registry,
	))

	fallbackRequest := conformanceStartRequest()
	fallbackRequest.Capabilities.VideoEvidence = playback.EvidenceDeclaredV3
	fallbackRequest.Capabilities.VideoDecode = nil
	fallbackRequest.Capabilities.CodecsVideo = []string{codecH264}
	fallbackRequest.Capabilities.CodecsVideoHardware = []string{codecH264}
	fallbackRequest.Capabilities.Containers = []string{containerMP4}
	fallbackFile := conformanceFallbackFile()
	var attempted []string
	for _, name := range []string{qualityOriginal, "progressive", containerHLS} {
		request := fallbackRequest
		request.PlaybackAttemptID = "attempt-delivery-chain"
		scenario := makePlannerScenario("delivery_"+name, "deliveries_negotiation", request, fallbackFile, attempted, settings, registry)
		planner = append(planner, scenario)
		attempted = append(attempted, scenario.Expected.PlanAttemptKey)
	}
	transcodeRequest := fallbackRequest
	transcodeRequest.PlaybackAttemptID = "attempt-delivery-transcode"
	transcodeRequest.QualityPreference = resolutionHD
	planner = append(planner, makePlannerScenario("delivery_transcode", "deliveries_negotiation", transcodeRequest, fallbackFile, nil, settings, registry))

	audioFile := &models.MediaFile{ID: 77, BaseType: "audiobook", FilePath: "/media/audiobook.m4b", Container: containerMP4, CodecAudio: codecAAC, Bitrate: 128, AudioChannels: 2, Duration: 39_600, AudioTracks: []models.AudioTrack{{Codec: codecAAC, Channels: 2, Layout: audioLayoutStereo}}}
	audioRequest := conformanceStartRequest()
	audioRequest.FileID = audioFile.ID
	audioRequest.AudioTrackID = playback.TrackIDV3(audioFile.ID, "audio", *audioRequest.AudioTrackIndex)
	audioRequest.PlaybackAttemptID = "attempt-audio-only"
	audioRequest.Capabilities.Containers = []string{containerMP4}
	planner = append(planner, makePlannerScenario("audio_only_original", "audio_only_planning", audioRequest, audioFile, nil, settings, registry))

	hdr10Request := conformanceHDRRequest()
	hdr10Request.PlaybackAttemptID = "attempt-hdr10-direct"
	planner = append(planner, makePlannerScenario("hdr10_exact_direct", "hdr_dv_matrix", hdr10Request, conformanceHDRFile(), nil, settings, registry))

	toneMapRequest := conformanceStartRequest()
	toneMapRequest.PlaybackAttemptID = "attempt-hdr10-tone-map"
	toneMapRequest.QualityPreference = resolutionFHD
	toneMapSettings := settings
	toneMapSettings.SoftwareToneMapEnabled = true
	toneMapRegistry := playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{
		{Name: playback.TransformationVideoToH264V3, RecipeVersion: playback.TransformationVideoToH264RecipeVersionV3, Available: true},
		{Name: playback.TransformationAudioToAACV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3, Available: true},
		{Name: playback.TransformationHDRToSDRToneMapV3, RecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3, Available: true},
	})
	softwarePQ := tonemap.Capabilities{{Mode: tonemap.ModeSoftware, Backend: "software", Filter: tonemap.SoftwareFilterBT2390, SourceKinds: []tonemap.SourceKind{tonemap.SourcePQ}}}
	planner = append(planner, makePlannerScenario("hdr10_to_sdr_tone_map", "hdr_dv_matrix", toneMapRequest, conformanceHDRFile(), nil, toneMapSettings, toneMapRegistry, softwarePQ))

	clientManagedFile := conformanceHDRFile()
	clientManagedFile.AudioTracks = append(clientManagedFile.AudioTracks, models.AudioTrack{Codec: codecAAC, Channels: 2, Layout: audioLayoutStereo})
	clientManagedRequest := conformanceHDRRequest()
	clientManagedRequest.PlaybackAttemptID = "attempt-client-managed-original"
	clientManagedRequest.Capabilities.HDR = false
	clientManagedRequest.Capabilities.HDRDetails = &playback.HDRCapabilitiesV3{DolbyVisionProfiles: []int{}}
	clientManagedRequest.ClientPlaybackContext.Output.HDRDetails = &playback.HDRCapabilitiesV3{DolbyVisionProfiles: []int{}}
	clientManagedAudioIndex := 1
	clientManagedRequest.AudioTrackIndex = &clientManagedAudioIndex
	clientManagedRequest.AudioTrackID = playback.TrackIDV3(clientManagedFile.ID, "audio", clientManagedAudioIndex)
	clientManagedDelivery := clientManagedRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3]
	clientManagedDelivery.HDRDetails = &playback.HDRCapabilitiesV3{DolbyVisionProfiles: []int{}}
	clientManagedDelivery.ValidatedClaims = []string{playback.ClaimClientManagedDynamicRangeV3, playback.ClaimClientSelectedAudioTrackV3}
	clientManagedRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3] = clientManagedDelivery
	planner = append(planner, makePlannerScenarioWithAudioIndex(
		"client_managed_hdr_selected_audio", "hdr_dv_matrix", clientManagedRequest, clientManagedFile,
		clientManagedAudioIndex, nil, settings, registry,
	))

	dv8File := conformanceHDRFile()
	dv8File.VideoTracks[0].DVProfile = 8
	dv8File.VideoTracks[0].DVBLCompatID = 1
	dv8File.VideoTracks[0].VideoRange = "DolbyVision"
	dv8File.VideoTracks[0].VideoRangeType = "DOVIWithHDR10"
	dv8Request := conformanceHDRRequest()
	dv8Request.PlaybackAttemptID = "attempt-dv8-direct"
	dv8Request.Capabilities.HDRDetails.DolbyVisionProfiles = []int{8}
	dv8Request.ClientPlaybackContext.Output.HDRDetails.DolbyVisionProfiles = []int{8}
	planner = append(planner, makePlannerScenario("dolby_vision_8_exact_direct", "hdr_dv_matrix", dv8Request, dv8File, nil, settings, registry))

	dv7File := conformanceHDRFile()
	dv7File.VideoTracks[0].DVProfile = 7
	dv7File.VideoTracks[0].DVBLCompatID = 6
	dv7File.VideoTracks[0].VideoRange = "DolbyVision"
	dv7File.VideoTracks[0].VideoRangeType = "DOVIWithEL"
	dv7Request := conformanceHDRRequest()
	dv7Request.PlaybackAttemptID = "attempt-dv7-hdr10"
	dv7Registry := playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{{Name: playback.TransformationServerDV7HDR10V3, RecipeVersion: "1", Available: true}})
	planner = append(planner, makePlannerScenario("dolby_vision_7_hdr10_fallback", "hdr_dv_matrix", dv7Request, dv7File, nil, settings, dv7Registry))

	dv7ToneMapFile := *dv7File
	dv7ToneMapFile.VideoTracks = append([]models.VideoTrack(nil), dv7File.VideoTracks...)
	dv7ToneMapFile.VideoTracks[0].DVConfigPresent = true
	dv7ToneMapFile.VideoTracks[0].DVBLCompatIDPresent = true
	dv7ToneMapFile.VideoTracks[0].DVBLPresent = true
	dv7ToneMapFile.VideoTracks[0].DVRPUPresent = true
	dv7ToneMapFile.VideoTracks[0].ColorRange = "tv"
	dv7ToneMapFile.VideoTracks[0].ColorPrimaries = "bt2020"
	dv7ToneMapFile.VideoTracks[0].ColorTransfer = "smpte2084"
	dv7ToneMapFile.VideoTracks[0].ColorSpace = "bt2020nc"
	dv7ToneMapRequest := conformanceStartRequest()
	dv7ToneMapRequest.PlaybackAttemptID = "attempt-dv7-id6-tone-map"
	dv7ToneMapRequest.QualityPreference = resolutionFHD
	planner = append(planner, makePlannerScenario("dolby_vision_7_id6_to_sdr_tone_map", "hdr_dv_matrix", dv7ToneMapRequest, &dv7ToneMapFile, nil, toneMapSettings, toneMapRegistry, softwarePQ))

	audioAdaptFile := conformanceHDRFile()
	audioAdaptFile.CodecAudio = codecTrueHD
	audioAdaptFile.AudioChannels = 8
	audioAdaptFile.AudioTracks[0] = models.AudioTrack{Codec: codecTrueHD, Channels: 8, Layout: audioLayout71}
	audioAdaptRequest := conformanceHDRRequest()
	audioAdaptRequest.PlaybackAttemptID = "attempt-truehd-aac"
	planner = append(planner, makePlannerScenario("truehd_audio_conversion", "audio_matrix", audioAdaptRequest, audioAdaptFile, nil, settings, registry))

	audioPassthroughFile := conformanceHDRFile()
	audioPassthroughFile.CodecAudio = codecTrueHD
	audioPassthroughFile.AudioChannels = 8
	audioPassthroughFile.AudioTracks[0] = models.AudioTrack{Codec: codecTrueHD, Channels: 8, Layout: audioLayout71}
	audioPassthroughRequest := conformanceHDRRequest()
	audioPassthroughRequest.PlaybackAttemptID = "attempt-truehd-passthrough"
	audioPassthroughRequest.ClientFeatures = append(audioPassthroughRequest.ClientFeatures, playback.FeatureLayoutPassthrough)
	audioPassthroughRequest.Capabilities.CodecsAudio = []string{codecTrueHD}
	audioPassthroughRequest.Capabilities.AudioPassthrough = &playback.AudioPassthroughV3{
		PassthroughCodecs: []string{codecTrueHD}, MaxChannels: 8,
		Entries: []playback.AudioPassthroughEntryV3{{Codec: codecTrueHD, ChannelCounts: []int{8}, Layouts: []string{audioLayout71}}},
	}
	originalDelivery := audioPassthroughRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3]
	originalDelivery.AudioPassthroughCodecs = []string{codecTrueHD}
	audioPassthroughRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3] = originalDelivery
	planner = append(planner, makePlannerScenario("truehd_exact_layout_passthrough", "audio_matrix", audioPassthroughRequest, audioPassthroughFile, nil, settings, registry))

	pgsFile := conformanceHDRFile()
	pgsFile.SubtitleTracks = []models.SubtitleTrack{{Index: 4, Language: "jpn", Codec: "hdmv_pgs_subtitle", Title: "Japanese"}}
	pgsRequest := conformanceHDRRequest()
	pgsRequest.PlaybackAttemptID = "attempt-pgs-sidecar"
	pgsIndex := 0
	pgsRequest.SubtitleTrackIndex = &pgsIndex
	pgsRequest.SubtitleTrackID = playback.TrackIDV3(pgsFile.ID, "subtitle", pgsIndex)
	for _, deliveryClass := range []string{playback.DeliveryClassOriginalHTTPV3, playback.DeliveryClassProgressiveV3, playback.DeliveryClassHLSV3} {
		delivery := pgsRequest.ClientPlaybackContext.Deliveries[deliveryClass]
		delivery.Subtitles.EmbeddedBitmap = true
		pgsRequest.ClientPlaybackContext.Deliveries[deliveryClass] = delivery
	}
	planner = append(planner, makePlannerScenario("embedded_pgs_sidecar", "subtitle_matrix", pgsRequest, pgsFile, nil, settings, registry))

	assFile := conformanceHDRFile()
	assFile.SubtitleTracks = []models.SubtitleTrack{{Index: 4, Language: languageEnglish, Codec: "ass", Title: "English Signs"}}
	assRequest := conformanceHDRRequest()
	assRequest.PlaybackAttemptID = "attempt-ass-authored"
	assRequest.SubtitleFidelityPreference = playback.SubtitleFidelityPreserveV3
	assIndex := 0
	assRequest.SubtitleTrackIndex = &assIndex
	assRequest.SubtitleTrackID = playback.TrackIDV3(assFile.ID, "subtitle", assIndex)
	for _, deliveryClass := range []string{playback.DeliveryClassOriginalHTTPV3, playback.DeliveryClassProgressiveV3, playback.DeliveryClassHLSV3} {
		delivery := assRequest.ClientPlaybackContext.Deliveries[deliveryClass]
		delivery.Subtitles.EmbeddedText = true
		delivery.Subtitles.SidecarText = true
		delivery.Subtitles.ASSStyling = true
		delivery.Subtitles.FontAttachments = true
		assRequest.ClientPlaybackContext.Deliveries[deliveryClass] = delivery
	}
	planner = append(planner, makePlannerScenario("embedded_ass_authored_render", "subtitle_matrix", assRequest, assFile, nil, settings, registry))

	dvdFile := conformanceVideoFile()
	dvdFile.SubtitleTracks = []models.SubtitleTrack{{Index: 4, Language: languageEnglish, Codec: "dvd_subtitle", Title: "English"}}
	dvdRequest := conformanceStartRequest()
	dvdRequest.PlaybackAttemptID = "attempt-dvd-burn-in"
	dvdIndex := 0
	dvdRequest.SubtitleTrackIndex = &dvdIndex
	dvdRequest.SubtitleTrackID = playback.TrackIDV3(dvdFile.ID, "subtitle", dvdIndex)
	planner = append(planner, makePlannerScenario("embedded_dvd_burn_in", "subtitle_matrix", dvdRequest, dvdFile, nil, settings, registry))

	qualityRequest := fallbackRequest
	qualityRequest.PlaybackAttemptID = "attempt-available-qualities"
	qualityRequest.QualityPreference = qualityOriginal
	planner = append(planner, makePlannerScenario("available_qualities", "available_qualities", qualityRequest, fallbackFile, nil, settings, registry))

	decision := goldenDecisionResponse()
	plan := decision.PlaybackPlan
	if plan == nil {
		fail("golden decision has no plan")
	}
	trackIndex := 1
	trackChange := goldenReplanRequest()
	trackChange.Operation = playback.ReplanOperationTrackChangeV3
	trackChange.ReplanRequestID = "replan-track-change-0001"
	trackChange.Failure = playback.FailureV3{}
	trackChange.SelectedTracks.Audio = &playback.TrackIdentityV3{ID: "", Index: &trackIndex}
	qualityChange := goldenReplanRequest()
	qualityChange.Operation = playback.ReplanOperationQualityChangeV3
	qualityChange.ReplanRequestID = "replan-quality-change-0001"
	qualityChange.Failure = playback.FailureV3{}
	qualityChange.QualityPreference = resolutionHD
	outputChange := goldenReplanRequest()
	outputChange.Operation = playback.ReplanOperationOutputChangeV3
	outputChange.ReplanRequestID = "replan-output-change-0001"
	outputChange.Failure = playback.FailureV3{}
	seekReanchor := goldenReplanRequest()
	seekReanchor.Operation = playback.ReplanOperationSeekReanchorV3
	seekReanchor.ReplanRequestID = "replan-seek-reanchor-0001"
	seekReanchor.Failure = playback.FailureV3{}
	seekReanchor.PositionSeconds = 321.25

	trackChange.PositionSeconds = 321.25
	qualityChange.PositionSeconds = 321.25
	trackDuplicate := trackChange
	trackDuplicate.ReplanRequestID = "replan-track-duplicate-0001"
	qualityDuplicate := qualityChange
	qualityDuplicate.ReplanRequestID = "replan-quality-duplicate-0001"
	trackConcurrent := trackChange
	trackConcurrent.ReplanRequestID = "replan-track-concurrent-0001"
	qualityConcurrent := qualityChange
	qualityConcurrent.ReplanRequestID = "replan-quality-concurrent-0001"
	trackMidSeek := trackChange
	trackMidSeek.ReplanRequestID = "replan-track-mid-seek-0001"
	qualityMidSeek := qualityChange
	qualityMidSeek.ReplanRequestID = "replan-quality-mid-seek-0001"
	replans := []playback.ReplanScenarioV3{
		{Name: "track_change", Category: "track_change_replan", Request: trackChange, Expected: playback.ReplanExpectationV3{HTTPStatus: http.StatusOK, PreserveUnmodifiedTracks: true}},
		{Name: "quality_change", Category: "quality_change_replan", Request: qualityChange, Expected: playback.ReplanExpectationV3{HTTPStatus: http.StatusOK, SelectedQuality: resolutionHD}},
		{Name: "output_change", Category: "output_change_replan", Request: outputChange, Expected: playback.ReplanExpectationV3{HTTPStatus: http.StatusOK, PreserveUnmodifiedTracks: true}},
		{Name: "track_change_idempotent_duplicate", Category: "idempotent_replan", Request: trackDuplicate, Expected: playback.ReplanExpectationV3{SameRequestAndBodyStatus: http.StatusOK, ResponseReplayedVerbatim: true, ChangedBodyStatus: http.StatusConflict, ChangedBodyError: "idempotency_key_reused"}},
		{Name: "quality_change_idempotent_duplicate", Category: "idempotent_replan", Request: qualityDuplicate, Expected: playback.ReplanExpectationV3{SameRequestAndBodyStatus: http.StatusOK, ResponseReplayedVerbatim: true, ChangedBodyStatus: http.StatusConflict, ChangedBodyError: "idempotency_key_reused"}},
		{Name: "track_change_concurrent_duplicate", Category: "concurrent_replan", Request: trackConcurrent, Expected: playback.ReplanExpectationV3{WhileFirstLeaseActiveStatus: http.StatusConflict, ConcurrentError: "replan_in_progress", AfterCompletionStatus: http.StatusOK, ResponseReplayedVerbatim: true}},
		{Name: "quality_change_concurrent_duplicate", Category: "concurrent_replan", Request: qualityConcurrent, Expected: playback.ReplanExpectationV3{WhileFirstLeaseActiveStatus: http.StatusConflict, ConcurrentError: "replan_in_progress", AfterCompletionStatus: http.StatusOK, ResponseReplayedVerbatim: true}},
		{Name: "track_change_mid_seek", Category: categoryMidSeek, Request: trackMidSeek, Expected: playback.ReplanExpectationV3{HTTPStatus: http.StatusOK, PositionSeconds: trackMidSeek.PositionSeconds, PositionPreserved: true}},
		{Name: "quality_change_mid_seek", Category: categoryMidSeek, Request: qualityMidSeek, Expected: playback.ReplanExpectationV3{HTTPStatus: http.StatusOK, PositionSeconds: qualityMidSeek.PositionSeconds, PositionPreserved: true}},
		{Name: "mid_seek_reanchor", Category: categoryMidSeek, Request: seekReanchor, Expected: playback.ReplanExpectationV3{HTTPStatus: http.StatusOK, PositionSeconds: seekReanchor.PositionSeconds, PositionPreserved: true}},
	}

	outputA := playback.PlanAttemptKeyV3(*plan, "output-a", nil)
	outputB := playback.PlanAttemptKeyV3(*plan, "output-b", nil)
	if outputA == outputB {
		fail("output context must change the opaque plan attempt key")
	}
	recovery := goldenReplanRequest()
	recovery.ReplanRequestID = "replan-failure-matrix-0001"
	recovery.PositionSeconds = 321.25
	subtitleIndex := 2
	recovery.SelectedTracks.Subtitle = &playback.TrackIdentityV3{
		ID:    playback.TrackIDV3(goldenMediaFileID, "subtitle", subtitleIndex),
		Index: &subtitleIndex,
	}
	restartStart := goldenStartRequest()
	restartStart.PlaybackAttemptID = "attempt-restart-terminal"
	restartTerminal := playback.NewTerminalResponseV3(
		"transcode_start_failed",
		"The playback transport did not become ready in time.",
		true,
	)
	capacityStart := goldenStartRequest()
	capacityStart.PlaybackAttemptID = "attempt-capacity-unavailable"
	capacityAvailable := false
	zeroCapacityDelta := 0
	limitEvent := goldenRouteEvent()
	limitEvent.PlaybackAttemptID = "attempt-route-limit"
	limitEvent.Diagnostics = make(map[string]string, 33)
	for index := range 33 {
		limitEvent.Diagnostics[fmt.Sprintf("diagnostic_%02d", index)] = "value"
	}
	draftProtocolVersion := playback.ProtocolV3
	protocol := []playback.ProtocolScenarioV3{
		{Name: "legacy_start_requires_upgrade", Category: "legacy_426", Input: playback.ProtocolScenarioInputV3{LegacyStartBody: &playback.LegacyStartBodyV3{FileID: goldenMediaFileID}}, Expected: playback.ProtocolExpectationV3{HTTPStatus: http.StatusUpgradeRequired, Error: "client_upgrade_required"}},
		{Name: "draft_v3_start_requires_upgrade", Category: "draft_v3_426", Input: playback.ProtocolScenarioInputV3{LegacyStartBody: &playback.LegacyStartBodyV3{ProtocolVersion: &draftProtocolVersion, FileID: goldenMediaFileID, ClientCapabilities: &playback.DraftClientCapabilitiesV3{CodecsVideo: []string{codecH264}}}}, Expected: playback.ProtocolExpectationV3{HTTPStatus: http.StatusUpgradeRequired, Error: "client_upgrade_required"}},
		{Name: "output_context_change_invalidates_attempt", Category: "output_context_invalidation", Input: playback.ProtocolScenarioInputV3{PlanID: plan.PlanID, FirstOutputContextID: "output-a", SecondOutputContextID: "output-b", FirstPlanAttemptKey: outputA, SecondPlanAttemptKey: outputB}, Expected: playback.ProtocolExpectationV3{PlanIDUnchanged: true, PlanAttemptKeyChanged: true}},
		{Name: "opaque_attempt_key_loop", Category: "attempt_key_echo_and_loop", Input: playback.ProtocolScenarioInputV3{ServerPlanAttemptKey: plan.PlanAttemptKey, ReplanEcho: plan.PlanAttemptKey, AttemptedPlanKeys: []string{plan.PlanAttemptKey}}, Expected: playback.ProtocolExpectationV3{Action: "reject_already_attempted_plan"}},
		{Name: "failure_recovery_preserves_intent", Category: "recovery_matrix", Input: playback.ProtocolScenarioInputV3{ReplanRequest: &recovery}, Expected: playback.ProtocolExpectationV3{HTTPStatus: http.StatusOK, SelectionPreserved: true, PositionPreserved: true, Action: "preserve_selected_tracks_and_position"}},
		{Name: "restart_replays_terminal_attempt", Category: "restart_matrix", Input: playback.ProtocolScenarioInputV3{StartRequest: &restartStart, PersistedDecision: &restartTerminal, Restarted: true}, Expected: playback.ProtocolExpectationV3{HTTPStatus: http.StatusCreated, Outcome: playback.OutcomeAdaptationUnavailableV3, TerminalReason: "transcode_start_failed", ResponseReplayedVerbatim: true, CapacityDelta: &zeroCapacityDelta}},
		{Name: "capacity_unavailable_cleans_up", Category: "capacity_matrix", Input: playback.ProtocolScenarioInputV3{StartRequest: &capacityStart, CapacityAvailable: &capacityAvailable}, Expected: playback.ProtocolExpectationV3{HTTPStatus: http.StatusCreated, Outcome: playback.OutcomeAdaptationUnavailableV3, TerminalReason: "capacity_unavailable", CapacityDelta: &zeroCapacityDelta, CleanupComplete: true}},
		{Name: "route_event_diagnostic_limit", Category: "route_event_limits", Input: playback.ProtocolScenarioInputV3{RouteEvent: &limitEvent}, Expected: playback.ProtocolExpectationV3{HTTPStatus: http.StatusBadRequest, Error: "bad_request", Action: "reject_without_persisting"}},
	}

	return playback.ConformanceMatrixV3{SchemaVersion: 1, Planner: planner, Replans: replans, Protocol: protocol}
}

// makePlannerScenario builds one deterministic protocol-v3 planner fixture.
func makePlannerScenario(name, category string, request playback.StartRequestV3, file *models.MediaFile, attempted []string, settings playback.PlannerSettingsV3, registry *playback.TransformationRegistryV3, toneMapCapabilities ...tonemap.Capabilities) playback.PlannerScenarioV3 {
	return makePlannerScenarioWithAudioIndex(name, category, request, file, 0, attempted, settings, registry, toneMapCapabilities...)
}

func makePlannerScenarioWithAudioIndex(name, category string, request playback.StartRequestV3, file *models.MediaFile, audioTrackIndex int, attempted []string, settings playback.PlannerSettingsV3, registry *playback.TransformationRegistryV3, toneMapCapabilities ...tonemap.Capabilities) playback.PlannerScenarioV3 {
	var capabilities tonemap.Capabilities
	if len(toneMapCapabilities) > 0 {
		capabilities = toneMapCapabilities[0]
	}
	result := playback.PlanPlaybackV3(playback.PlannerInputV3{
		Request: request, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: audioTrackIndex,
		Settings: settings, Registry: registry, AttemptedKeys: attempted, ToneMapCapabilities: capabilities,
	})
	expected := playback.PlannerExpectationV3{Outcome: playback.OutcomeAdaptationUnavailableV3}
	if result.Plan != nil {
		selectedTracks := result.Plan.SelectedTracks
		subtitle := result.Plan.Subtitle
		claims := result.Plan.Claims
		expected = playback.PlannerExpectationV3{
			Outcome:            playback.OutcomePlayableV3,
			Delivery:           result.Plan.Delivery,
			DecisionReason:     result.Plan.DecisionReason,
			PlanID:             result.Plan.PlanID,
			PlanAttemptKey:     result.Plan.PlanAttemptKey,
			SelectedTracks:     &selectedTracks,
			Subtitle:           &subtitle,
			Claims:             &claims,
			Transformations:    append([]playback.TransformationV3(nil), result.Plan.Transformations...),
			AvailableQualities: result.Plan.AvailableQualities,
		}
	} else if result.Terminal != nil {
		expected.TerminalReason = result.Terminal.Reason
	}
	return playback.PlannerScenarioV3{
		Name: name, Category: category, Request: request,
		Source:        playback.SourceDescriptorFromFileV3(file, audioTrackIndex),
		AttemptedKeys: append([]string(nil), attempted...), Expected: expected,
	}
}

func conformanceStartRequest() playback.StartRequestV3 {
	request := goldenStartRequest()
	request.QualityPreference = qualityOriginal
	request.Capabilities = playback.ClientCodecCapabilitiesV3{
		VideoEvidence: playback.EvidenceExactV3, AudioEvidence: playback.EvidenceExactV3,
		CodecsVideo: []string{codecHEVC}, CodecsVideoHardware: []string{codecHEVC}, CodecsAudio: []string{codecAAC}, Containers: []string{containerMKV}, MaxResolution: resolutionFHD,
		VideoDecode: []playback.VideoDecodeCapabilityV3{{Codec: codecHEVC, Profiles: []string{"main 10"}, Levels: []int{41}, BitDepths: []int{8}, MaxWidth: 1920, MaxHeight: 1080, MaxFrameRate: 60, MaxBitrateKbps: 20_000, Hardware: true}},
	}
	request.ClientPlaybackContext = playback.ClientPlaybackContextV3{
		ProtocolVersion: playback.ProtocolV3, FormFactor: "tv", AppVersion: "3.0-test",
		Device: playback.DeviceContextV3{Platform: "fixture"}, Output: playback.OutputContextV3{OutputContextID: "output-a"},
		Deliveries: map[string]playback.DeliveryCapabilityV3{
			playback.DeliveryClassOriginalHTTPV3: conformanceDelivery([]string{containerMKV, containerMP4}),
			playback.DeliveryClassProgressiveV3:  conformanceDelivery([]string{containerMP4}),
			playback.DeliveryClassHLSV3:          conformanceDelivery([]string{containerHLS}),
		},
	}
	return request
}

func conformanceDelivery(containers []string) playback.DeliveryCapabilityV3 {
	return playback.DeliveryCapabilityV3{
		Enabled:                true,
		SupportedOnDevice:      true,
		Containers:             append([]string(nil), containers...),
		VideoCodecs:            []string{codecHEVC, codecH264},
		AudioDecodeCodecs:      []string{codecAAC},
		AudioPassthroughCodecs: []string{},
		Features:               []string{},
		ValidatedClaims:        []string{},
		Transformations:        []playback.TransformationV3{},
	}
}

func conformanceVideoFile() *models.MediaFile {
	return &models.MediaFile{ID: goldenMediaFileID, Container: containerMKV, CodecVideo: codecHEVC, CodecAudio: codecAAC, Resolution: resolutionFHD, Bitrate: 8_000, AudioChannels: 2, Duration: 7_200, VideoTracks: []models.VideoTrack{{Codec: codecHEVC, Profile: "Main", Level: 41, Width: 1920, Height: 1080, FrameRate: filmFrameRate, Bitrate: 8_000, BitDepth: 8, VideoRange: videoRangeSDR, VideoRangeType: videoRangeSDR}}, AudioTracks: []models.AudioTrack{{Codec: codecAAC, Channels: 2, Layout: audioLayoutStereo}}}
}

func conformanceHDRFile() *models.MediaFile {
	return &models.MediaFile{
		ID:            goldenMediaFileID,
		Container:     containerMKV,
		CodecVideo:    codecHEVC,
		CodecAudio:    codecAAC,
		Resolution:    "2160p",
		Bitrate:       60_000,
		AudioChannels: 2,
		Duration:      7_200,
		VideoTracks: []models.VideoTrack{{
			Codec: codecHEVC, Profile: "Main 10", Level: 153,
			Width: 3840, Height: 2160, FrameRate: filmFrameRate, Bitrate: 60_000, BitDepth: 10,
			VideoRange: "HDR", VideoRangeType: "HDR10", ColorRange: "tv",
		}},
		AudioTracks: []models.AudioTrack{{Codec: codecAAC, Channels: 2, Layout: audioLayoutStereo}},
	}
}

func conformanceHDRRequest() playback.StartRequestV3 {
	request := conformanceStartRequest()
	request.Capabilities.CodecsVideo = []string{codecHEVC}
	request.Capabilities.CodecsVideoHardware = []string{codecHEVC}
	request.Capabilities.CodecsAudio = []string{codecAAC}
	request.Capabilities.Containers = []string{containerMKV}
	request.Capabilities.MaxResolution = "2160p"
	request.Capabilities.HDR = true
	request.Capabilities.HDRDetails = &playback.HDRCapabilitiesV3{HDR10: true, DolbyVisionProfiles: []int{}}
	request.Capabilities.VideoDecode = []playback.VideoDecodeCapabilityV3{{
		Codec: codecHEVC, Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10},
		MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true,
	}}
	request.ClientPlaybackContext.Output.HDRDetails = &playback.HDRCapabilitiesV3{HDR10: true, DolbyVisionProfiles: []int{}}
	return request
}

func conformanceFallbackFile() *models.MediaFile {
	return &models.MediaFile{ID: goldenMediaFileID, FilePath: "/media/movie.mp4", Container: containerMP4, CodecVideo: codecH264, CodecAudio: codecAAC, Resolution: resolutionFHD, Bitrate: 8_000, AudioChannels: 2, Duration: 7_200, VideoTracks: []models.VideoTrack{{Codec: codecH264, Profile: profileHigh, Level: 41, Width: 1920, Height: 1080, FrameRate: filmFrameRate, Bitrate: 8_000, BitDepth: 8, VideoRange: videoRangeSDR, VideoRangeType: videoRangeSDR}}, AudioTracks: []models.AudioTrack{{Codec: codecAAC, Channels: 2, Layout: audioLayoutStereo}}}
}

func conformanceRegistry() *playback.TransformationRegistryV3 {
	return playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{
		{Name: playback.TransformationAudioToAACV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3, Available: true},
		{Name: playback.TransformationVideoToH264V3, RecipeVersion: "1", Available: true},
		{Name: playback.TransformationServerDV7HDR10V3, RecipeVersion: "1", Available: true},
	})
}
