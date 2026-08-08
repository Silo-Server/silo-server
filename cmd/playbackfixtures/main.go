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
	"os"
	"path/filepath"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// goldenSessionID is a fixed UUID: fixtures must be byte-stable across runs,
// so nothing in this generator may read the clock or a random source.
const (
	goldenSessionID   = "11111111-1111-4111-8111-111111111111"
	goldenAttemptID   = "attempt-golden-0001"
	goldenExpiresAt   = "2030-01-01T00:00:00Z"
	goldenMediaFileID = 42
	// The fixed key the request fixtures echo. Requests carry a key the server
	// minted on an earlier response, so its value is arbitrary; what matters is
	// that it satisfies the "v3:" prefix the replan validator enforces.
	goldenPriorAttemptKey = "v3:0000000000000001"
	// Codec and container tokens the fixtures are built from. Named so the
	// capability lists, the plan recipe, and the source descriptor cannot drift
	// into describing different media by a one-character typo.
	codecH264         = "h264"
	codecHEVC         = "hevc"
	codecAAC          = "aac"
	containerMP4      = "mp4"
	containerMKV      = "mkv"
	containerHLS      = "hls"
	profileHigh       = "high"
	resolutionHD      = "720p"
	resolutionFHD     = "1080p"
	qualityOriginal   = "original"
	audioLayoutStereo = "stereo"
	videoRangeSDR     = "SDR"
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
	write(*out, "capability_response.json", goldenCapabilityResponse())
	write(*out, "route_event.json", goldenRouteEvent())
	write(*out, "subtitle_inventory.json", goldenSubtitleInventory())
	write(*out, "attempt_keys.json", goldenAttemptKeys())
	write(*out, "conformance_matrix.json", goldenConformanceMatrix())
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
	return playback.ReplanRequestV3{
		ProtocolVersion:   playback.ProtocolV3,
		ClientFeatures:    []string{playback.FeaturePlaybackPlanV3},
		Operation:         playback.ReplanOperationFailureRecoveryV3,
		PlaybackAttemptID: goldenAttemptID,
		ReplanRequestID:   "replan-golden-0001",
		FailedPlanID:      "plan:golden-0001",
		PlanAttemptID:     "plan-attempt-golden-0001",
		// Attempt keys are server-owned opaque tokens. A client echoes the
		// values it was handed; it never computes one.
		PlanAttemptKey:        goldenPriorAttemptKey,
		AttemptedPlanKeys:     []string{goldenPriorAttemptKey},
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
			{Path: "/library/movie.en.srt", Language: "eng", Format: "srt", Title: "English"},
		},
		SubtitleTracks: []models.SubtitleTrack{
			{Index: 0, Language: "eng", Codec: "ass", Title: "English (Signs)", Forced: true},
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
	width := 1920
	height := 1080
	frameRate := 23.976
	bitrate := 8_000
	channels := 2
	audioIndex := 0
	duration := 7_265.5

	plan := playback.PlanV3{
		ProtocolVersion: playback.ProtocolV3,
		SessionID:       goldenSessionID,
		ExpiresAt:       goldenExpiresAt,
		Delivery:        playback.DeliveryOriginalHTTPV3,
		Stream: playback.StreamV3{
			URL:           "/stream/" + goldenSessionID,
			Protocol:      playback.StreamHTTPProgressiveV3,
			Container:     containerMP4,
			MIMEType:      "video/mp4",
			Headers:       map[string]string{},
			HeaderRefresh: playback.HeaderRefreshSessionV3,
		},
		Timeline: playback.TimelineV3{
			SourceStartSeconds: 12.5,
			PlayerStartSeconds: 12.5,
			CanSeekAnywhere:    true,
			SeekRestoration:    "player_position",
		},
		SelectedTracks: playback.SelectedTracksV3{
			Audio: &playback.TrackIdentityV3{ID: playback.TrackIDV3(goldenMediaFileID, "audio", 0), Index: &audioIndex},
		},
		EffectiveRecipe: playback.EffectiveRecipeV3{
			VideoCodec:    codecH264,
			AudioCodec:    codecAAC,
			Width:         &width,
			Height:        &height,
			FrameRate:     &frameRate,
			BitrateKbps:   &bitrate,
			DynamicRange:  playback.DynamicRangeSDRV3,
			AudioChannels: &channels,
			AudioLayout:   audioLayoutStereo,
		},
		Claims: playback.ValidationClaimsV3{
			Audio: playback.AudioClaimsV3{Codec: codecAAC, Reason: "client_decode_supported"},
		},
		Subtitle: playback.SubtitleDecisionV3{
			Mode: playback.SubtitleOffV3,
			// No track is selected, but the inventory is still complete: it is
			// the authoritative ordinal space a client selects from.
			Inventory: playback.SubtitleInventoryV3(goldenSessionID, goldenMediaFile(), goldenSubtitleAdditional()),
		},
		Transformations:    []playback.TransformationV3{},
		AppliedQuirks:      []playback.AppliedQuirkV3{},
		RuntimeCorrections: []string{},
		AvailableQualities: []playback.AvailableQualityV3{
			{Label: playback.QualityOriginalV3, Height: 1080, BitrateKbps: 8_000, PreservesSource: true},
			{Label: resolutionHD, Height: 720, BitrateKbps: 2_000},
			{Label: "480p", Height: 480, BitrateKbps: 1_500},
		},
		DegradationWarnings:  []playback.DegradationWarningV3{},
		DecisionReason:       "validated_original_playback",
		RequestedMediaFileID: goldenMediaFileID,
		EffectiveMediaFileID: goldenMediaFileID,
		Source: playback.SourceDescriptorV3{
			MediaFileID:        goldenMediaFileID,
			DurationSeconds:    &duration,
			Container:          containerMP4,
			VideoCodec:         codecH264,
			VideoProfile:       profileHigh,
			VideoLevel:         41,
			BitDepth:           8,
			Width:              1920,
			Height:             1080,
			FrameRate:          23.976,
			BitrateKbps:        8_000,
			DynamicRange:       playback.DynamicRangeSDRV3,
			DVEnhancementLayer: playback.EnhancementNoneV3,
			AudioCodec:         codecAAC,
			AudioChannels:      2,
			AudioLayout:        audioLayoutStereo,
		},
		SubtitleFidelityPolicy: "allow_simplified_rendering",
	}
	// Derive plan identity with the shipping functions rather than pinning
	// literals, so the fixture doubles as a vector for the identity rules in
	// docs/architecture/playback-protocol-v3.md.
	plan.PlanID = playback.DeterministicPlanIDV3(goldenAttemptID, plan.RequestedMediaFileID, plan.EffectiveMediaFileID, plan)
	plan.PlanAttemptKey = playback.PlanAttemptKeyV3(plan, "7", nil)

	return playback.DecisionResponseV3{
		ProtocolVersion: playback.ProtocolV3,
		ServerFeatures:  playback.ServerFeaturesV3(),
		Outcome:         playback.OutcomePlayableV3,
		SessionID:       goldenSessionID,
		PlaybackPlan:    &plan,
	}
}

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
			{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: "1", ValidatedClaims: []string{playback.ClaimAudioDecodeV3}},
			{Name: playback.TransformationServerDV7HDR10V3, Executor: playback.ExecutorServerV3, RecipeVersion: "1", ValidatedClaims: playback.DV7ToHDR10ClaimsV3()},
			{Name: playback.TransformationVideoToH264V3, Executor: playback.ExecutorServerV3, RecipeVersion: "1", ValidatedClaims: []string{playback.ClaimH264DecodeV3}},
		},
	}
}

func goldenRouteEvent() playback.RouteEventV3 {
	return playback.RouteEventV3{
		ProtocolVersion:   playback.ProtocolV3,
		PlaybackAttemptID: goldenAttemptID,
		SessionID:         goldenSessionID,
		PlanID:            "plan:golden-0001",
		PlanAttemptID:     "plan-attempt-golden-0001",
		PlanAttemptKey:    goldenPriorAttemptKey,
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
				{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: "1", ValidatedClaims: []string{}},
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
			AudioCodec:     "truehd",
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

type conformanceMatrix struct {
	SchemaVersion int                `json:"schema_version"`
	Planner       []plannerScenario  `json:"planner_scenarios"`
	Replans       []replanScenario   `json:"replan_scenarios"`
	Protocol      []protocolScenario `json:"protocol_scenarios"`
}

type plannerScenario struct {
	Name          string                      `json:"name"`
	Category      string                      `json:"category"`
	Request       playback.StartRequestV3     `json:"request"`
	Source        playback.SourceDescriptorV3 `json:"source"`
	AttemptedKeys []string                    `json:"attempted_plan_keys,omitempty"`
	Expected      plannerExpectation          `json:"expected"`
}

type plannerExpectation struct {
	Outcome            playback.DecisionOutcomeV3    `json:"outcome"`
	Delivery           playback.DeliveryV3           `json:"delivery,omitempty"`
	DecisionReason     string                        `json:"decision_reason,omitempty"`
	PlanID             string                        `json:"plan_id,omitempty"`
	PlanAttemptKey     string                        `json:"plan_attempt_key,omitempty"`
	AvailableQualities []playback.AvailableQualityV3 `json:"available_qualities,omitempty"`
	TerminalReason     string                        `json:"terminal_reason,omitempty"`
}

type replanScenario struct {
	Name     string                   `json:"name"`
	Category string                   `json:"category"`
	Request  playback.ReplanRequestV3 `json:"request"`
	Expected map[string]any           `json:"expected"`
}

type protocolScenario struct {
	Name     string         `json:"name"`
	Category string         `json:"category"`
	Input    map[string]any `json:"input"`
	Expected map[string]any `json:"expected"`
}

func goldenConformanceMatrix() conformanceMatrix {
	videoFile := conformanceVideoFile()
	base := conformanceStartRequest()
	registry := conformanceRegistry()
	settings := playback.PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}

	planner := make([]plannerScenario, 0, 10)
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

	audioFile := &models.MediaFile{ID: 77, Container: containerMP4, CodecAudio: codecAAC, Bitrate: 128, AudioChannels: 2, Duration: 39_600, AudioTracks: []models.AudioTrack{{Codec: codecAAC, Channels: 2, Layout: audioLayoutStereo}}}
	audioRequest := conformanceStartRequest()
	audioRequest.FileID = audioFile.ID
	audioRequest.PlaybackAttemptID = "attempt-audio-only"
	audioRequest.Capabilities.Containers = []string{containerMP4}
	planner = append(planner, makePlannerScenario("audio_only_original", "audio_only_planning", audioRequest, audioFile, nil, settings, registry))

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
	seekReanchor := goldenReplanRequest()
	seekReanchor.Operation = playback.ReplanOperationSeekReanchorV3
	seekReanchor.ReplanRequestID = "replan-seek-reanchor-0001"
	seekReanchor.Failure = playback.FailureV3{}
	seekReanchor.PositionSeconds = 321.25

	replans := []replanScenario{
		{Name: "track_change", Category: "track_change_replan", Request: trackChange, Expected: map[string]any{"preserve_unmodified_tracks": true, "idempotent_duplicate_status": 200}},
		{Name: "quality_change", Category: "quality_change_replan", Request: qualityChange, Expected: map[string]any{"selected_quality": resolutionHD, "idempotent_duplicate_status": 200}},
		{Name: "idempotent_duplicate", Category: "idempotent_replan", Request: qualityChange, Expected: map[string]any{"same_request_id_and_body_status": 200, "response_replayed_verbatim": true, "changed_body_status": 409, "changed_body_error": "idempotency_key_reused"}},
		{Name: "mid_seek_reanchor", Category: "mid_seek_replan", Request: seekReanchor, Expected: map[string]any{"position_seconds": 321.25, "route_identity_stable_when_recipe_unchanged": true}},
		{Name: "concurrent_duplicate", Category: "concurrent_replan", Request: seekReanchor, Expected: map[string]any{"while_first_lease_active_status": 409, "error": "replan_in_progress", "after_completion_status": 200, "response_replayed_verbatim": true}},
	}

	outputA := playback.PlanAttemptKeyV3(*plan, "output-a", nil)
	outputB := playback.PlanAttemptKeyV3(*plan, "output-b", nil)
	protocol := []protocolScenario{
		{Name: "legacy_start_requires_upgrade", Category: "legacy_426", Input: map[string]any{"body": map[string]any{"file_id": goldenMediaFileID}}, Expected: map[string]any{"http_status": 426, "error": "client_upgrade_required"}},
		{Name: "output_context_change_invalidates_attempt", Category: "output_context_invalidation", Input: map[string]any{"plan_id": plan.PlanID, "first_output_context_id": "output-a", "second_output_context_id": "output-b", "first_plan_attempt_key": outputA, "second_plan_attempt_key": outputB}, Expected: map[string]any{"plan_id_unchanged": true, "plan_attempt_key_changed": outputA != outputB}},
		{Name: "opaque_attempt_key_loop", Category: "attempt_key_echo_and_loop", Input: map[string]any{"server_plan_attempt_key": plan.PlanAttemptKey, "replan_echo": plan.PlanAttemptKey, "attempted_plan_keys": []string{plan.PlanAttemptKey}}, Expected: map[string]any{"action": "reject_already_attempted_plan"}},
	}

	return conformanceMatrix{SchemaVersion: 1, Planner: planner, Replans: replans, Protocol: protocol}
}

func makePlannerScenario(name, category string, request playback.StartRequestV3, file *models.MediaFile, attempted []string, settings playback.PlannerSettingsV3, registry *playback.TransformationRegistryV3) plannerScenario {
	result := playback.PlanPlaybackV3(playback.PlannerInputV3{
		Request: request, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
		Settings: settings, Registry: registry, AttemptedKeys: attempted,
	})
	expected := plannerExpectation{Outcome: playback.OutcomeAdaptationUnavailableV3}
	if result.Plan != nil {
		expected = plannerExpectation{
			Outcome:            playback.OutcomePlayableV3,
			Delivery:           result.Plan.Delivery,
			DecisionReason:     result.Plan.DecisionReason,
			PlanID:             result.Plan.PlanID,
			PlanAttemptKey:     result.Plan.PlanAttemptKey,
			AvailableQualities: result.Plan.AvailableQualities,
		}
	} else if result.Terminal != nil {
		expected.TerminalReason = result.Terminal.Reason
	}
	return plannerScenario{
		Name: name, Category: category, Request: request,
		Source:        playback.SourceDescriptorFromFileV3(file, 0),
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
			playback.DeliveryClassOriginalHTTPV3: {Enabled: true, SupportedOnDevice: true, Containers: []string{containerMKV, containerMP4}, VideoCodecs: []string{codecHEVC, codecH264}, AudioDecodeCodecs: []string{codecAAC}},
			playback.DeliveryClassProgressiveV3:  {Enabled: true, SupportedOnDevice: true, Containers: []string{containerMP4}, VideoCodecs: []string{codecHEVC, codecH264}, AudioDecodeCodecs: []string{codecAAC}},
			playback.DeliveryClassHLSV3:          {Enabled: true, SupportedOnDevice: true, Containers: []string{containerHLS}, VideoCodecs: []string{codecHEVC, codecH264}, AudioDecodeCodecs: []string{codecAAC}},
		},
	}
	return request
}

func conformanceVideoFile() *models.MediaFile {
	return &models.MediaFile{ID: goldenMediaFileID, Container: containerMKV, CodecVideo: codecHEVC, CodecAudio: codecAAC, Resolution: resolutionFHD, Bitrate: 8_000, AudioChannels: 2, Duration: 7_200, VideoTracks: []models.VideoTrack{{Codec: codecHEVC, Profile: "Main", Level: 41, Width: 1920, Height: 1080, FrameRate: "24000/1001", Bitrate: 8_000, BitDepth: 8, VideoRange: videoRangeSDR, VideoRangeType: videoRangeSDR}}, AudioTracks: []models.AudioTrack{{Codec: codecAAC, Channels: 2, Layout: audioLayoutStereo}}}
}

func conformanceFallbackFile() *models.MediaFile {
	return &models.MediaFile{ID: goldenMediaFileID, Container: containerMP4, CodecVideo: codecH264, CodecAudio: codecAAC, Resolution: resolutionFHD, Bitrate: 8_000, AudioChannels: 2, Duration: 7_200, VideoTracks: []models.VideoTrack{{Codec: codecH264, Profile: profileHigh, Level: 41, Width: 1920, Height: 1080, FrameRate: "24000/1001", Bitrate: 8_000, BitDepth: 8, VideoRange: videoRangeSDR, VideoRangeType: videoRangeSDR}}, AudioTracks: []models.AudioTrack{{Codec: codecAAC, Channels: 2, Layout: audioLayoutStereo}}}
}

func conformanceRegistry() *playback.TransformationRegistryV3 {
	return playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{
		{Name: playback.TransformationAudioToAACV3, RecipeVersion: "1", Available: true},
		{Name: playback.TransformationVideoToH264V3, RecipeVersion: "1", Available: true},
		{Name: playback.TransformationServerDV7HDR10V3, RecipeVersion: "1", Available: true},
	})
}
