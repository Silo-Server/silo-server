package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// failingSaveAttemptPlanStoreV3 fails every terminal-start save with one fixed
// error while leaving the attempt lookup on the memory store. It models the
// production shape where the plan/transport failed and the subsequent terminal
// save fails on the same request budget.
type failingSaveAttemptPlanStoreV3 struct {
	playback.PlanStoreV3
	err error
}

func (s *failingSaveAttemptPlanStoreV3) SaveAttempt(context.Context, playback.AttemptRecordV3) error {
	return s.err
}

// contextCheckingSaveAttemptPlanStoreV3 fails a save whose context is already
// done, the way a database round-trip does. The memory store ignores the
// context, so without this seam a test cannot tell a live persistence context
// from the expired start phase.
type contextCheckingSaveAttemptPlanStoreV3 struct {
	playback.PlanStoreV3
}

func (s contextCheckingSaveAttemptPlanStoreV3) SaveAttempt(ctx context.Context, record playback.AttemptRecordV3) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.PlanStoreV3.SaveAttempt(ctx, record)
}

// A start whose own commit fails on the server side — the plan store cannot
// save the attempt — is a transient server fault, not a verdict about the
// request or the release. It used to be published as a 201 terminal with
// retryable:false, telling the client never to retry a condition that is
// retryable by definition; it must answer the retryable 503 instead.
func TestHandleStartPlaybackV3ServerFaultAnswersRetryableNotTerminal(t *testing.T) {
	source := v3HandlerFixtureFile(t)
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), mapPlaybackFileResolver{files: map[int]*models.MediaFile{source.ID: source}})
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.PlanStoreV3 = &failingSaveAttemptPlanStoreV3{PlanStoreV3: handler.PlanStoreV3, err: errors.New("database unavailable")}

	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, httptest.NewRequest(
		http.MethodPost,
		"/api/v1/playback/start",
		strings.NewReader(marshalV3StartRequest(t, v3HandlerStartRequest())),
	).WithContext(newAuthorizedPlaybackContext()))

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("start status = %d, want 503, body = %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Retry-After"); got != "5" {
		t.Fatalf("Retry-After = %q, want 5", got)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Error != "dependency_unavailable" {
		t.Fatalf("error code = %q, want dependency_unavailable", body.Error)
	}
	if strings.Contains(rr.Body.String(), `"outcome"`) {
		t.Fatalf("body = %s, want no playback verdict for a server fault", rr.Body.String())
	}
}

// A start that runs out of its resolve budget established nothing about the
// release, so it must answer the retryable 503 with Retry-After and store no
// verdict. The pre-fix path persisted a terminal on the expired phase and
// answered 201 with a verdict the start never reached — and the retry either
// replayed it (same attempt id) or re-walked into the same wall.
func TestHandleStartPlaybackV3ExhaustedResolveBudgetAnswersRetryable(t *testing.T) {
	source := v3HandlerFixtureFile(t)
	source.ID = 900
	source.ContentID = "movie-phase-budget"
	source.FilePath = "virtual://movie/movie-phase-budget?result=pin"
	source.VirtualOwnerInstallationID = 5
	// No probe stamp, so the start must go through the virtual resolve: that is
	// the branch that narrows the request onto the start phase.
	source.ProbeUpdatedAt = nil

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), mapPlaybackFileResolver{files: map[int]*models.MediaFile{source.ID: source}})
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.PlaybackConfig = playbackTestConfig("", "")
	handler.VirtualMediaDetailedResolver = VirtualMediaDetailedResolverFunc(func(context.Context, string, int, int, string, bool, []string, string) (ResolvedVirtualMedia, error) {
		return ResolvedVirtualMedia{}, context.DeadlineExceeded
	})
	handler.PlanStoreV3 = contextCheckingSaveAttemptPlanStoreV3{PlanStoreV3: handler.PlanStoreV3}

	previousBudget := virtualStartPhaseBudget
	virtualStartPhaseBudget = time.Nanosecond
	t.Cleanup(func() { virtualStartPhaseBudget = previousBudget })

	start := v3HandlerStartRequest()
	start.FileID = source.ID
	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, httptest.NewRequest(
		http.MethodPost,
		"/api/v1/playback/start",
		strings.NewReader(marshalV3StartRequest(t, start)),
	).WithContext(newAuthorizedPlaybackContext()))

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("start status = %d, want the retryable 503, body = %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Retry-After"); got != "5" {
		t.Fatalf("Retry-After = %q, want 5", got)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Error != "dependency_unavailable" {
		t.Fatalf("error code = %q, want dependency_unavailable", body.Error)
	}
	if strings.Contains(rr.Body.String(), `"terminal"`) || strings.Contains(rr.Body.String(), `"outcome"`) {
		t.Fatalf("body = %s, want no verdict for an exhausted budget", rr.Body.String())
	}
	// Nothing may be stored: the automatic retry replays a stored decision for
	// the same attempt id, so a row here is the wall the retry cannot climb.
	if _, err := handler.PlanStoreV3.GetAttemptByPlaybackAttemptID(context.Background(), start.PlaybackAttemptID); !errors.Is(err, playback.ErrSessionNotFound) {
		t.Fatalf("attempt lookup err = %v, want no stored attempt for an exhausted budget", err)
	}
}

// A start whose phase runs out while the transport is becoming ready must not
// publish the readiness wait's own verdict. The transport never got the window
// the phase reserved for it, so the answer is the retryable 503 rather than a
// transcode_start_failed terminal the client would treat as a bad release.
func TestHandleStartPlaybackV3ExhaustedTransportPhaseAnswersRetryable(t *testing.T) {
	source := v3HandlerFixtureFile(t)
	source.ID = 950
	source.Container = "mp4"
	source.FilePath = "virtual://movie/transport-phase-budget"
	source.VirtualOwnerInstallationID = 5

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), mapPlaybackFileResolver{files: map[int]*models.MediaFile{source.ID: source}})
	// The fake FFmpeg starts and never writes a manifest, so the start can only
	// end at a deadline.
	handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpegStall(t), t.TempDir())
	stubCopySeekAnchorV3(handler)
	handler.VirtualPlaybackResolver = VirtualPlaybackResolverFunc(func(_ context.Context, path string, _ int, _ string, _ int) (string, error) {
		return "http://127.0.0.1:8080/stream?path=" + path, nil
	})
	handler.VirtualMediaResolver = VirtualMediaResolverFunc(func(_ context.Context, uri string, _ int, _ int, _ string) (string, error) {
		return "http://127.0.0.1:8080/stream?uri=" + uri, nil
	})
	handler.ItemAccess = allowAllPlaybackItemAccess{}

	previousBudget := virtualStartPhaseBudget
	virtualStartPhaseBudget = 300 * time.Millisecond
	t.Cleanup(func() { virtualStartPhaseBudget = previousBudget })

	start := v3HandlerStartRequest()
	start.FileID = source.ID
	// Ask for HLS with the fixture's own codecs so the plan commits a local
	// HLS transport and the readiness wait is the phase's to cut.
	start.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3] = playback.DeliveryCapabilityV3{Enabled: false}
	start.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3] = playback.DeliveryCapabilityV3{
		Enabled: true, SupportedOnDevice: true, Containers: []string{"hls"},
		VideoCodecs: []string{"h264"}, AudioDecodeCodecs: []string{"aac"},
	}
	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, httptest.NewRequest(
		http.MethodPost,
		"/api/v1/playback/start",
		strings.NewReader(marshalV3StartRequest(t, start)),
	).WithContext(newAuthorizedPlaybackContext()))

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("start status = %d, want the retryable 503, body = %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Retry-After"); got != "5" {
		t.Fatalf("Retry-After = %q, want 5", got)
	}
	if body := rr.Body.String(); strings.Contains(body, transcodeStartFailedReasonV3) || strings.Contains(body, `"terminal"`) {
		t.Fatalf("body = %s, want the budget answer, not a transcode verdict", body)
	}
	if _, err := handler.PlanStoreV3.GetAttemptByPlaybackAttemptID(context.Background(), start.PlaybackAttemptID); !errors.Is(err, playback.ErrSessionNotFound) {
		t.Fatalf("attempt lookup err = %v, want no stored attempt for an exhausted budget", err)
	}
}

// A start that hits its request budget after a retryable transport failure must
// answer the retryable problem with Retry-After, not the 500 internal_error the
// failed terminal save used to produce. The session limit stands in for the
// transport failure: it makes startPlannedPlaybackV3 return a retryable
// statusErr, and the store makes the terminal save fail with the expired
// request budget, exactly as a 30 s virtual start does in production.
func TestHandleStartPlaybackV3RetryableStartTimeoutAnswers503(t *testing.T) {
	source := v3HandlerFixtureFile(t)
	manager := playback.NewSessionManager(1, 0)
	if _, err := manager.StartSession(1, "profile-1", 99, playback.PlayDirect, false); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	handler := NewPlaybackHandler(manager, mapPlaybackFileResolver{files: map[int]*models.MediaFile{source.ID: source}})
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.PlanStoreV3 = &failingSaveAttemptPlanStoreV3{PlanStoreV3: handler.PlanStoreV3, err: context.DeadlineExceeded}

	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, httptest.NewRequest(
		http.MethodPost,
		"/api/v1/playback/start",
		strings.NewReader(marshalV3StartRequest(t, v3HandlerStartRequest())),
	).WithContext(newAuthorizedPlaybackContext()))

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("start status = %d, want 503, body = %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Retry-After"); got != "5" {
		t.Fatalf("Retry-After = %q, want 5", got)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Error != "dependency_unavailable" {
		t.Fatalf("error code = %q, want dependency_unavailable", body.Error)
	}
}

// A genuine store fault is still a server error when the verdict itself is real.
// The start terminal here is the explicit-pick HDR refusal — a plan verdict the
// server reached on its own, whose first store write is the terminal save. A
// store fault on that save must keep reporting the fault rather than disguising
// the verdict as a retryable refusal.
func TestHandleStartPlaybackV3NonRetryablePersistFaultAnswers500(t *testing.T) {
	source := v3HandlerFixtureFile(t)
	source.CodecVideo = "hevc"
	source.Resolution = "2160p"
	source.Bitrate = 32_000
	source.VideoTracks[0] = models.VideoTrack{
		Codec: "hevc", Profile: "main 10", Level: 150, Width: 3840, Height: 2160,
		FrameRate: "24000/1001", Bitrate: 32_000, BitDepth: 10,
		VideoRange: "DolbyVision", VideoRangeType: "DOVIWithHDR10", DVProfile: 8, DVBLCompatID: 1,
	}
	alternateValue := *source
	alternate := &alternateValue
	alternate.ID = 84
	alternate.CodecVideo = "h264"
	alternate.Resolution = "1080p"
	alternate.Bitrate = 8_000
	alternate.VideoTracks = []models.VideoTrack{{
		Codec: "h264", Profile: "high", Level: 41, Width: 1920, Height: 1080,
		FrameRate: "24000/1001", Bitrate: 8_000, BitDepth: 8, VideoRange: videoRangeSDR, VideoRangeType: videoRangeSDR,
	}}

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), mapPlaybackFileResolver{files: map[int]*models.MediaFile{source.ID: source}})
	handler.FileVersionFetcher = testPlaybackFileVersionFetcher{byContent: map[string][]*models.MediaFile{
		source.ContentID: {source, alternate},
	}}
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "false"}}
	handler.PlaybackConfig = playbackTestConfig("", "")
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.PlanStoreV3 = &failingSaveAttemptPlanStoreV3{PlanStoreV3: handler.PlanStoreV3, err: errors.New("database unavailable")}

	start := v3HandlerStartRequest()
	start.QualityPreference = "auto"
	start.FileSelection = playback.FileSelectionExplicitV3
	start.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3] = playback.DeliveryCapabilityV3{
		Enabled: true, SupportedOnDevice: true, Containers: []string{"hls"},
		VideoCodecs: []string{"h264"}, AudioDecodeCodecs: []string{"aac"},
	}

	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, httptest.NewRequest(
		http.MethodPost,
		"/api/v1/playback/start",
		strings.NewReader(marshalV3StartRequest(t, start)),
	).WithContext(newAuthorizedPlaybackContext()))

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("start status = %d, want 500, body = %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Retry-After"); got != "" {
		t.Fatalf("Retry-After = %q, want none on a genuine internal fault", got)
	}
	if !strings.Contains(rr.Body.String(), `"error":"internal_error"`) {
		t.Fatalf("body = %s, want internal_error", rr.Body.String())
	}
}

// Route-capacity exhaustion is a real transient state, not a defect: it keeps
// its existing machine-readable code and the retryable status even when the
// terminal save also fails.
func TestStartTerminalPersistOperationErrorV3RouteCapacityIsRetryable(t *testing.T) {
	err := startTerminalPersistOperationErrorV3(
		context.Background(),
		errors.New("plan store unavailable"),
		&transportErrorV3{
			reason:    string(noderouting.OutcomeCapacityUnavailable),
			message:   "No playback route satisfies the configured policy and current node availability.",
			retryable: true,
		},
	)
	var operation *PlaybackOperationError
	if !errors.As(err, &operation) {
		t.Fatalf("err = %v, want *PlaybackOperationError", err)
	}
	if operation.Status != http.StatusServiceUnavailable || operation.Code != string(noderouting.OutcomeCapacityUnavailable) {
		t.Fatalf("operation = %+v, want 503 %s", operation, noderouting.OutcomeCapacityUnavailable)
	}
	if operation.RetryAfter != playbackStartRetryAfterSeconds {
		t.Fatalf("RetryAfter = %d, want %d", operation.RetryAfter, playbackStartRetryAfterSeconds)
	}
	if !strings.Contains(strings.ToLower(operation.Message), "capacity") {
		t.Fatalf("message = %q, want it to name the capacity state", operation.Message)
	}
}

// The request deadline can expire on a non-retryable verdict too; the client
// still gets a retryable answer because the save was cut off by the budget, not
// by a store fault.
func TestStartTerminalPersistOperationErrorV3ExpiredBudgetIsRetryable(t *testing.T) {
	err := startTerminalPersistOperationErrorV3(context.Background(), context.DeadlineExceeded, nil)
	var operation *PlaybackOperationError
	if !errors.As(err, &operation) {
		t.Fatalf("err = %v, want *PlaybackOperationError", err)
	}
	if operation.Status != http.StatusServiceUnavailable || operation.Code != "dependency_unavailable" || operation.RetryAfter == 0 {
		t.Fatalf("operation = %+v, want a retryable 503", operation)
	}
}
