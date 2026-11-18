package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/remotestream"
)

// subtitleCandidateSpy records the rotation intent and failure stamps a
// subtitle request observes. A subtitle path must never claim rotation and must
// never stamp the video candidate failed.
type subtitleCandidateSpy struct {
	rotationSeen     []bool
	excludedSeen     [][]string
	candidateStamped bool
}

func (s *subtitleCandidateSpy) rotation(ctx context.Context, excluded []string) {
	s.rotationSeen = append(s.rotationSeen, VirtualCandidateRotationAllowed(ctx))
	s.excludedSeen = append(s.excludedSeen, append([]string(nil), excluded...))
}

// newVirtualSubtitleCandidateFixture builds a live virtual session (bound to a
// result= pinned candidate) whose provider resolve always fails. The caller
// drives either subtitle route and asserts the resolve stayed subtitle-local.
func newVirtualSubtitleCandidateFixture(t *testing.T, tracks []models.SubtitleTrack) (*StreamHandler, *playback.Session, *models.MediaFile, *subtitleCandidateSpy, string) {
	t.Helper()
	virtualURI := "virtual://movie/tt-sub-invariant?result=session"
	file := &models.MediaFile{
		ID: 77, ContentID: "movie-sub-invariant", FilePath: virtualURI,
		SubtitleTracks: tracks, VirtualOwnerInstallationID: 5,
	}
	manager := playback.NewSessionManager(0, 0)
	session, err := manager.StartSession(1, "profile-1", file.ID, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if err := manager.UpdateStreamState(session.ID, playback.SessionStreamState{
		VirtualSourceSet:                 true,
		VirtualSourceURI:                 virtualURI,
		VirtualSourceOwnerInstallationID: 5,
		VirtualSubtitleEvidenceSet:       true,
		VirtualSubtitleTracks:            tracks,
	}); err != nil {
		t.Fatalf("UpdateStreamState: %v", err)
	}

	relay := remotestream.NewRelay()
	t.Cleanup(func() { _ = relay.Close(context.Background()) })

	spy := &subtitleCandidateSpy{}
	handler := NewStreamHandler(manager, testPlaybackFileResolver{file: file})
	handler.PlaybackConfig = playbackTestConfig("", t.TempDir())
	handler.RemoteStreamRelay = relay
	handler.VirtualMediaDetailedResolver = VirtualMediaDetailedResolverFunc(func(ctx context.Context, _ string, _ int, _ int, _ string, _ bool, excluded []string, _ string) (ResolvedVirtualMedia, error) {
		spy.rotation(ctx, excluded)
		return ResolvedVirtualMedia{}, errors.New("provider dropped the pinned release")
	})
	handler.VirtualCandidateFailMarker = func(context.Context, int, string, *time.Time) error {
		spy.candidateStamped = true
		return nil
	}
	return handler, session, file, spy, virtualURI
}

// A subtitle sidecar extraction whose virtual resolve fails is a subtitle
// problem: it must answer with the subtitle-local code, must not authorize a
// release substitution, and must not stamp the video candidate failed.
func TestSubtitleSidecarResolveFailureStaysSubtitleLocalAndDoesNotRotate(t *testing.T) {
	handler, session, _, spy, _ := newVirtualSubtitleCandidateFixture(t, []models.SubtitleTrack{{Index: 0, Codec: "subrip"}})
	recorder := httptest.NewRecorder()
	handler.HandleSubtitle(recorder, playbackTestRequest(http.MethodGet,
		"/api/v1/stream/"+session.ID+"/subtitles/0.vtt", nil,
		map[string]string{"session_id": session.ID, "track": "0.vtt"}))

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("sidecar resolve failure status = %d, body = %s, want 502", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v (body = %s)", err, recorder.Body.String())
	}
	if body.Error != subtitleSourceUnavailableErrorCode {
		t.Fatalf("sidecar error code = %q, want the subtitle-local %q", body.Error, subtitleSourceUnavailableErrorCode)
	}
	// The refusal says nothing about the file: the provider or relay may answer
	// on the next attempt. It carries the same Retry-After the remux start
	// failure does, so the client backs off instead of hammering.
	if got := recorder.Header().Get("Retry-After"); got != strconv.Itoa(virtualServeResolveRetryAfterSeconds) {
		t.Fatalf("Retry-After = %q, want %d", got, virtualServeResolveRetryAfterSeconds)
	}
	if spy.candidateStamped {
		t.Fatal("a subtitle sidecar resolve failure stamped the video candidate failed")
	}
	if len(spy.rotationSeen) == 0 {
		t.Fatal("sidecar extraction never reached the provider resolver")
	}
	for _, allowed := range spy.rotationSeen {
		if allowed {
			t.Fatal("subtitle sidecar resolve declared candidate rotation; a subtitle path must not authorize a release swap")
		}
	}
	for _, excluded := range spy.excludedSeen {
		if len(excluded) != 0 {
			t.Fatalf("subtitle sidecar resolve excluded candidates %v; it must not indict the release", excluded)
		}
	}
}

// A font-bundle resolve failure is likewise subtitle-local: the asset is part
// of subtitle delivery, so it must answer with a subtitle code, never rotate,
// and never stamp the video candidate failed.
func TestSubtitleFontResolveFailureDoesNotAffectVideoCandidate(t *testing.T) {
	handler, session, _, spy, _ := newVirtualSubtitleCandidateFixture(t, []models.SubtitleTrack{{Index: 4, Codec: "ass"}})
	recorder := httptest.NewRecorder()
	handler.HandleSubtitleFonts(recorder, playbackFontRequest(session.ID, "0"))

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("font resolve failure status = %d, body = %s, want 502", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v (body = %s)", err, recorder.Body.String())
	}
	if body.Error != subtitleSourceUnavailableErrorCode {
		t.Fatalf("font error code = %q, want the subtitle-local %q", body.Error, subtitleSourceUnavailableErrorCode)
	}
	if spy.candidateStamped {
		t.Fatal("a font-bundle resolve failure stamped the video candidate failed")
	}
	if len(spy.rotationSeen) == 0 {
		t.Fatal("font bundle never reached the provider resolver")
	}
	for _, allowed := range spy.rotationSeen {
		if allowed {
			t.Fatal("font-bundle resolve declared candidate rotation; a subtitle asset must not move the release")
		}
	}
}

func playbackFontRequest(sessionID, track string) *http.Request {
	return playbackTestRequest(http.MethodGet,
		"/api/v1/stream/"+sessionID+"/subtitles/"+track+"/fonts", nil,
		map[string]string{"session_id": sessionID, "track": track})
}

// A subtitle remap miss on every candidate is a subtitle-local outcome. It must
// not be classified as a failed media source, which is what would feed the
// candidate-failure machinery and rotate the release.
func TestSubtitleRemapExhaustionIsSubtitleLocal(t *testing.T) {
	errs := []*candidateV3Error{{
		Stage:   candidateStageSubtitleRemap,
		Message: "candidate 84 subtitle remap failed",
		Err:     errSubtitleUnavailableInTargetV3,
	}}
	got := classifyVirtualReplanExhaustionV3(nil, errs)
	if got == nil {
		t.Fatal("subtitle remap exhaustion returned no error")
	}
	if got.reason != subtitleUnavailableReasonV3 {
		t.Fatalf("subtitle remap exhaustion reason = %q, want %q", got.reason, subtitleUnavailableReasonV3)
	}
	if got.reason == virtualSourceUnavailableReasonV3 {
		t.Fatal("a subtitle remap miss was classified as a virtual source failure")
	}

	artifactErrs := []*candidateV3Error{{
		Stage:   candidateStageSubtitleArtifact,
		Message: "candidate 84 attach subtitle artifact failed",
		Err:     errors.New("subtitle store unavailable"),
	}}
	if got := classifyVirtualReplanExhaustionV3(nil, artifactErrs); got.reason != subtitleUnavailableReasonV3 {
		t.Fatalf("subtitle artifact exhaustion reason = %q, want %q", got.reason, subtitleUnavailableReasonV3)
	}

	if !candidateSubtitleLocalFailureV3(&candidateV3Error{Stage: candidateStageSubtitleRemap, Err: errSubtitleUnavailableInTargetV3}) {
		t.Fatal("a subtitle remap miss was not recognized as subtitle-local")
	}
	if !candidateSubtitleLocalFailureV3(&candidateV3Error{Stage: candidateStageSubtitleArtifact, Err: errors.New("store")}) {
		t.Fatal("a subtitle artifact failure was not recognized as subtitle-local")
	}
	if candidateSubtitleLocalFailureV3(&candidateV3Error{Stage: candidateStageTransport, Err: errors.New("no bytes")}) {
		t.Fatal("a transport failure was misclassified as subtitle-local")
	}
}

// A subtitle-only refusal keeps the release; every video/policy reason keeps
// its existing alternate-file failover.
func TestSubtitleOnlyTerminalKeepsReleaseButVideoReasonsStillFailOver(t *testing.T) {
	if !subtitleOnlyTerminalV3(&playback.TerminalV3{Reason: terminalSubtitleConversionUnsupportedV3}) {
		t.Fatal("subtitle_conversion_unsupported was not recognized as subtitle-only")
	}
	for _, reason := range []string{terminalNoAlternateVersionV3, terminalHDRTranscodeUnsupportedV3, sourceDecodeFailedReasonV3} {
		if subtitleOnlyTerminalV3(&playback.TerminalV3{Reason: reason}) {
			t.Fatalf("video/policy terminal %q must keep its alternate-file failover", reason)
		}
	}
	if subtitleOnlyTerminalV3(nil) {
		t.Fatal("a nil terminal must not be treated as subtitle-only")
	}
}

// The in-place subtitle degrade must not re-resolve the provider candidate.
// Re-resolving could return a different release, which is exactly what a
// subtitle problem must never cause.
func TestSubtitleDegradeInPlaceNeverResolvesProviderCandidate(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	file.FilePath = "virtual://movie/tt-inplace?result=session"
	file.VirtualOwnerInstallationID = 5
	// A virtual parent row has no probed streams, so planning terminalls before
	// transport preparation; the only path that could reach the provider
	// resolver here is a candidate re-resolve, which is what this test forbids.
	file.VideoTracks = nil
	file.AudioTracks = nil

	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.PlaybackConfig = playbackTestConfig("", t.TempDir())

	resolverCalled := false
	handler.VirtualMediaDetailedResolver = VirtualMediaDetailedResolverFunc(func(context.Context, string, int, int, string, bool, []string, string) (ResolvedVirtualMedia, error) {
		resolverCalled = true
		return ResolvedVirtualMedia{URL: "http://127.0.0.1:1/other", URI: "virtual://movie/tt-inplace?result=other", CandidateID: "other"}, nil
	})

	session, err := manager.StartSession(1, "profile-1", file.ID, playback.PlayDirect, false)
	if err != nil {
		t.Fatal(err)
	}
	record := &playback.AttemptRecordV3{SessionID: session.ID, ProfileID: "profile-1", EffectiveMediaFileID: file.ID, RequestedMediaFileID: file.ID}
	index := 0
	start := playback.StartRequestV3{ProtocolVersion: playback.ProtocolV3, FileID: file.ID, SubtitleTrackIndex: &index, SubtitleTrackID: playback.TrackIDV3(file.ID, "subtitle", index)}

	_, _ = handler.evaluateSubtitleDegradeInPlaceV3(
		httptest.NewRequest(http.MethodPost, "/api/v1/playback/replan", nil),
		session, record, playback.ReplanRequestV3{}, start, file, ProbeProvenance(""), file,
		playback.PlannerSettingsV3{}, nil, nil,
	)
	if resolverCalled {
		t.Fatal("the in-place subtitle degrade re-resolved the provider candidate")
	}
}

// A burn-in readiness timeout while FFmpeg is still running is a subtitle-local
// condition, never a transport failure that could substitute another release.
// A genuine pre-start crash keeps the transport classification, so video
// failover is unchanged.
func TestBurnInReadinessTimeoutKeepsTransportFailoverForRealCrashes(t *testing.T) {
	cause := errors.New("no segments yet")
	burnIn := localTransportReadinessErrorV3(playback.TranscodeOpts{SubtitleBurnIn: true, SubtitleTrackIndex: 0}, true, cause)
	if burnIn.reason != subtitleUnavailableReasonV3 {
		t.Fatalf("slow burn-in reason = %q, want the subtitle-local %q", burnIn.reason, subtitleUnavailableReasonV3)
	}
	if !burnIn.retryable {
		t.Fatal("a slow burn-in should stay retryable on the same release")
	}

	plain := localTransportReadinessErrorV3(playback.TranscodeOpts{SubtitleTrackIndex: -1}, true, cause)
	if plain.reason != transcodeStartFailedReasonV3 || !plain.retryable {
		t.Fatalf("non-burn-in readiness reason = %q retryable=%v, want the transport failure", plain.reason, plain.retryable)
	}

	crashed := localTransportReadinessErrorV3(playback.TranscodeOpts{SubtitleBurnIn: true, SubtitleTrackIndex: 0}, false, cause)
	if crashed.reason != transcodeStartFailedReasonV3 {
		t.Fatalf("crashed burn-in reason = %q, want the transport failure so video failover still runs", crashed.reason)
	}
}

// The degraded start clears the selection and stamps a plain warning that the
// subtitle was dropped; a plan that did not drop a subtitle is untouched.
func TestSubtitleDegradedStartDropsSelectionAndStatesTheDrop(t *testing.T) {
	index := 2
	base := playback.StartRequestV3{SubtitleTrackIndex: &index, SubtitleTrackID: "file:1:subtitle:2"}
	dropped := subtitleDegradedStartV3(base)
	if dropped.SubtitleTrackIndex != nil || dropped.SubtitleTrackID != "" {
		t.Fatalf("degraded start kept the subtitle selection: %#v", dropped)
	}

	result := playback.PlannerResultV3{Plan: &playback.PlanV3{}}
	annotateSubtitleDroppedV3(&result)
	found := false
	for _, warning := range result.Plan.DegradationWarnings {
		if warning.Code == "subtitle_dropped_unavailable" {
			found = true
		}
	}
	if !found {
		t.Fatalf("degraded plan did not state the subtitle drop: %#v", result.Plan.DegradationWarnings)
	}

	terminal := playback.PlannerResultV3{Terminal: &playback.TerminalV3{Reason: terminalSubtitleConversionUnsupportedV3}, Plan: &playback.PlanV3{}}
	annotateSubtitleDroppedV3(&terminal)
	if len(terminal.Plan.DegradationWarnings) != 0 {
		t.Fatal("a terminal plan was annotated as a subtitle drop")
	}
}
