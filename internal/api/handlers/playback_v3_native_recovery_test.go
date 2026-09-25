package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestNativeSubtitleFailureFallsBackWithoutChangingVideoAndStaysDisabled(t *testing.T) {
	for name, features := range map[string][]string{
		"canonical":             {playback.FeatureEmbeddedSubtitlesV3},
		"normalized_duplicates": {playback.FeatureEmbeddedSubtitlesV3, " EMBEDDED_SUBTITLES_V1 ", "Embedded_Subtitles_V1"},
	} {
		t.Run(name, func(t *testing.T) {
			assertNativeSubtitleFailureFallsBackAndStaysDisabled(t, features)
		})
	}
}

func assertNativeSubtitleFailureFallsBackAndStaysDisabled(t *testing.T, features []string) {
	t.Helper()
	file := v3HandlerFixtureFile(t)
	file.SubtitleTracks = []models.SubtitleTrack{{Index: 3, ContainerTrackID: "4", Codec: "mov_text", Language: "eng"}}
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "true"}}
	start := v3HandlerStartRequest()
	start.ClientFeatures = append(start.ClientFeatures, features...)
	start.SubtitleTrackIndex = new(0)
	start.SubtitleTrackID = playback.TrackIDV3(file.ID, "subtitle", 0)
	caps := start.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3]
	caps.Subtitles.NativeEmbedded = []playback.NativeEmbeddedSubtitleCapabilityV3{{Container: "mp4", Codecs: []string{"mov_text"}, TrackIdentity: "container_track_id"}}
	start.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3] = caps
	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, start))).WithContext(newAuthorizedPlaybackContext()))
	var started playback.DecisionResponseV3
	if rr.Code != http.StatusCreated || json.Unmarshal(rr.Body.Bytes(), &started) != nil || started.PlaybackPlan == nil {
		t.Fatalf("start: %d %s", rr.Code, rr.Body.String())
	}
	native := started.PlaybackPlan
	if native.Delivery != playback.DeliveryOriginalHTTPV3 || native.Subtitle.Embedded == nil || native.Subtitle.Embedded.StreamIndex != 3 || native.Subtitle.Embedded.ContainerTrackID != "4" || native.Subtitle.Artifact != nil {
		t.Fatalf("native plan=%+v subtitle=%+v", native, native.Subtitle)
	}
	if len(native.Subtitle.Inventory) != 1 || native.Subtitle.Inventory[0].URL == "" {
		t.Fatal("fallback inventory missing")
	}
	request := playback.ReplanRequestV3{
		ProtocolVersion: playback.ProtocolV3, ClientFeatures: start.ClientFeatures,
		Operation: playback.ReplanOperationFailureRecoveryV3, PlaybackAttemptID: start.PlaybackAttemptID,
		ReplanRequestID: "native-recovery-0001", FailedPlanID: native.PlanID, PlanAttemptID: "native-attempt-0001",
		PlanAttemptKey: native.PlanAttemptKey, AttemptedPlanKeys: []string{native.PlanAttemptKey}, AttemptCount: 1, PositionSeconds: 120,
		SelectedTracks: native.SelectedTracks, Failure: playback.FailureV3{Classification: "subtitle_embedded_failed"},
		Capabilities: start.Capabilities, ClientPlaybackContext: start.ClientPlaybackContext,
	}
	recovered := postPlaybackReplanV3(t, handler, started.SessionID, request)
	if recovered.PlaybackPlan == nil {
		t.Fatalf("fallback=%+v", recovered)
	}
	sidecar := recovered.PlaybackPlan
	if sidecar.Delivery != playback.DeliveryOriginalHTTPV3 || sidecar.Subtitle.Embedded != nil || sidecar.Subtitle.Artifact == nil || sidecar.Subtitle.Artifact.Format != "vtt" || sidecar.Subtitle.Artifact.TimingOriginSeconds != 0 {
		t.Fatalf("fallback plan=%+v subtitle=%+v", sidecar, sidecar.Subtitle)
	}
	if sidecar.PlanID == native.PlanID || sidecar.PlanAttemptKey == native.PlanAttemptKey {
		t.Fatal("native failure retried same route")
	}
	// A refreshed capability report cannot accidentally reactivate a failed route.
	request.Operation = playback.ReplanOperationOutputChangeV3
	request.ReplanRequestID = "native-recovery-output-0002"
	request.FailedPlanID = sidecar.PlanID
	request.PlanAttemptID = "native-attempt-0002"
	request.PlanAttemptKey = sidecar.PlanAttemptKey
	request.AttemptedPlanKeys = nil
	request.Failure = playback.FailureV3{}
	request.ClientPlaybackContext.Output.OutputContextID = "route-2"
	next := postPlaybackReplanV3(t, handler, started.SessionID, request)
	if next.PlaybackPlan == nil || next.PlaybackPlan.Subtitle.Embedded != nil || next.PlaybackPlan.Subtitle.Artifact == nil {
		t.Fatalf("native route reenabled: %+v", next)
	}
	record, err := handler.PlanStoreV3.GetAttempt(t.Context(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if playback.HasFeatureV3(record.NormalizedRequest.ClientFeatures, playback.FeatureEmbeddedSubtitlesV3) {
		t.Fatal("failure did not persist native disablement")
	}
}

func TestRemapSubtitleSelectionRetainsHearingImpairedVariant(t *testing.T) {
	for _, external := range []bool{true, false} {
		source := &models.MediaFile{ID: 1}
		target := &models.MediaFile{ID: 2}
		if external {
			source.ExternalSubtitles = []models.ExternalSubtitle{{Language: "eng", Format: "srt", HearingImpaired: true}}
			target.ExternalSubtitles = []models.ExternalSubtitle{{Language: "eng", Format: "srt"}, {Language: "eng", Format: "srt", HearingImpaired: true}}
		} else {
			source.SubtitleTracks = []models.SubtitleTrack{{Language: "eng", Codec: "subrip", HearingImpaired: true}}
			target.SubtitleTracks = []models.SubtitleTrack{{Language: "eng", Codec: "subrip"}, {Language: "eng", Codec: "subrip", HearingImpaired: true}}
		}
		request := playback.StartRequestV3{SubtitleTrackIndex: new(0)}
		handler := &PlaybackHandler{}
		if err := handler.remapSubtitleSelectionV3(t.Context(), source, target, &request); err != nil {
			t.Fatal(err)
		}
		if *request.SubtitleTrackIndex != 1 {
			t.Fatalf("external=%v chose non-SDH track", external)
		}
	}
}

// TestRemapSubtitleSelectionAcrossSubtitleFormats covers #1034: auto quality
// swaps from an HDR edition with PGS subtitles to an SDR edition with SRT ones.
// The selection keeps its language and forced/SDH variant in the effective
// file's format instead of failing playback.
func TestRemapSubtitleSelectionAcrossSubtitleFormats(t *testing.T) {
	source := &models.MediaFile{ID: 1, SubtitleTracks: []models.SubtitleTrack{
		{Language: "fre", Codec: "hdmv_pgs_subtitle", Forced: true},
		{Language: "fre", Codec: "hdmv_pgs_subtitle"},
	}}
	cases := []struct {
		name   string
		target *models.MediaFile
		from   int
		want   int
	}{
		{"embedded srt", &models.MediaFile{ID: 2, SubtitleTracks: []models.SubtitleTrack{
			{Language: "fre", Codec: "subrip", Forced: true},
			{Language: "fre", Codec: "subrip"},
		}}, 1, 1},
		{"embedded teletext", &models.MediaFile{ID: 2, SubtitleTracks: []models.SubtitleTrack{
			{Language: "fre", Codec: "dvb_teletext"},
		}}, 1, 0},
		{"keeps forced variant", &models.MediaFile{ID: 2, SubtitleTracks: []models.SubtitleTrack{
			{Language: "fre", Codec: "subrip"},
			{Language: "fre", Codec: "subrip", Forced: true},
		}}, 0, 1},
		{"external srt", &models.MediaFile{ID: 2,
			ExternalSubtitles: []models.ExternalSubtitle{{Language: "fre", Format: "srt"}},
			SubtitleTracks:    []models.SubtitleTrack{{Language: "eng", Codec: "subrip"}},
		}, 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := playback.StartRequestV3{SubtitleTrackIndex: new(tc.from)}
			handler := &PlaybackHandler{}
			if err := handler.remapSubtitleSelectionV3(t.Context(), source, tc.target, &request); err != nil {
				t.Fatal(err)
			}
			if *request.SubtitleTrackIndex != tc.want {
				t.Fatalf("remapped to %d, want %d", *request.SubtitleTrackIndex, tc.want)
			}
			if request.SubtitleTrackID != playback.TrackIDV3(tc.target.ID, "subtitle", tc.want) {
				t.Fatalf("track id = %q", request.SubtitleTrackID)
			}
		})
	}

	request := playback.StartRequestV3{SubtitleTrackIndex: new(1)}
	other := &models.MediaFile{ID: 3, SubtitleTracks: []models.SubtitleTrack{{Language: "eng", Codec: "subrip"}}}
	if err := (&PlaybackHandler{}).remapSubtitleSelectionV3(t.Context(), source, other, &request); err == nil {
		t.Fatal("a language the effective file lacks was remapped")
	}
}

// TestRemapSubtitleSelectionAcrossFormatsNeedsOneDeliverableMatch: the
// cross-format fallback skips formats the subtitle policy cannot deliver, and
// refuses a match it cannot tell apart from another track by title.
func TestRemapSubtitleSelectionAcrossFormatsNeedsOneDeliverableMatch(t *testing.T) {
	remap := func(source, target *models.MediaFile, from int) (int, error) {
		request := playback.StartRequestV3{SubtitleTrackIndex: new(from)}
		err := (&PlaybackHandler{}).remapSubtitleSelectionV3(t.Context(), source, target, &request)
		if err != nil {
			return -1, err
		}
		return *request.SubtitleTrackIndex, nil
	}
	pgs := &models.MediaFile{ID: 1, SubtitleTracks: []models.SubtitleTrack{{Language: "fre", Codec: "hdmv_pgs_subtitle"}}}

	undeliverableFirst := &models.MediaFile{ID: 2,
		ExternalSubtitles: []models.ExternalSubtitle{{Language: "fre", Format: "sub"}},
		SubtitleTracks:    []models.SubtitleTrack{{Language: "fre", Codec: "subrip"}},
	}
	if got, err := remap(pgs, undeliverableFirst, 0); err != nil || got != 1 {
		t.Fatalf("undeliverable external first: got %d, %v; want embedded SRT at 1", got, err)
	}

	ambiguous := &models.MediaFile{ID: 2, SubtitleTracks: []models.SubtitleTrack{
		{Language: "fre", Codec: "subrip"},
		{Language: "fre", Codec: "subrip", Title: "Commentary"},
	}}
	if _, err := remap(pgs, ambiguous, 0); err == nil {
		t.Fatal("an untitled selection was remapped to one of two same-language tracks")
	}

	commentary := &models.MediaFile{ID: 1, SubtitleTracks: []models.SubtitleTrack{
		{Language: "fre", Codec: "hdmv_pgs_subtitle"},
		{Language: "fre", Codec: "hdmv_pgs_subtitle", Title: "Commentary"},
	}}
	if got, err := remap(commentary, ambiguous, 1); err != nil || got != 1 {
		t.Fatalf("title disambiguation: got %d, %v; want the Commentary track at 1", got, err)
	}

	// External bitmaps can be neither burned in nor served as a sidecar, so an
	// external PGS neither wins the fallback nor makes it ambiguous.
	externalBitmap := &models.MediaFile{ID: 2,
		ExternalSubtitles: []models.ExternalSubtitle{{Language: "fre", Format: "hdmv_pgs_subtitle"}},
		SubtitleTracks:    []models.SubtitleTrack{{Language: "fre", Codec: "subrip"}},
	}
	if got, err := remap(pgs, externalBitmap, 0); err != nil || got != 1 {
		t.Fatalf("external bitmap: got %d, %v; want embedded SRT at 1", got, err)
	}
	onlyExternalBitmap := &models.MediaFile{ID: 2, ExternalSubtitles: []models.ExternalSubtitle{{Language: "fre", Format: "hdmv_pgs_subtitle"}}}
	if _, err := remap(pgs, onlyExternalBitmap, 0); err == nil {
		t.Fatal("an external bitmap was chosen as the only fallback")
	}

	// The container's embedded title is what the viewer sees when no stored
	// title exists, so it disambiguates too.
	embeddedTitled := &models.MediaFile{ID: 1, SubtitleTracks: []models.SubtitleTrack{
		{Language: "fre", Codec: "hdmv_pgs_subtitle"},
		{Language: "fre", Codec: "hdmv_pgs_subtitle", EmbeddedTitle: "Commentary"},
	}}
	embeddedTargets := &models.MediaFile{ID: 2, SubtitleTracks: []models.SubtitleTrack{
		{Language: "fre", Codec: "subrip", EmbeddedTitle: "Main"},
		{Language: "fre", Codec: "subrip", EmbeddedTitle: "Commentary"},
	}}
	if got, err := remap(embeddedTitled, embeddedTargets, 1); err != nil || got != 1 {
		t.Fatalf("embedded title disambiguation: got %d, %v; want the Commentary track at 1", got, err)
	}
}
