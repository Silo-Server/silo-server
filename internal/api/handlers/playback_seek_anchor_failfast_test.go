package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/remotestream"
)

// TestPrepareTransportTimelineSlowAnchorIndexTimeoutStartsWithoutTheAnchor
// proves a keyframe index that cannot be read inside the start budget no longer
// fails the start. The copied stream begins from the requested position with the
// non-anchored timeline it had before the anchor existed, the plan says so
// (copy_seek_anchor_resolved=false), and the start logs the degradation. The
// anchor resolve is still bounded and still retried once.
func TestPrepareTransportTimelineSlowAnchorIndexTimeoutStartsWithoutTheAnchor(t *testing.T) {
	previous := copySeekAnchorSeekResolveTimeout
	copySeekAnchorSeekResolveTimeout = 40 * time.Millisecond
	defer func() { copySeekAnchorSeekResolveTimeout = previous }()

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	var probes atomic.Int32
	handler.copySeekAnchor = func(ctx context.Context, _ string, _ string, _ float64, _ int) (float64, int, error) {
		probes.Add(1)
		<-ctx.Done()
		return 0, 0, ctx.Err()
	}
	plan := &playback.PlanV3{
		PlanID:   "plan:slow-anchor",
		Delivery: playback.DeliveryRemuxHLSV3,
		Timeline: playback.TimelineV3{SourceStartSeconds: 120, PlayerStartSeconds: 120},
	}
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previousLogger)

	started := time.Now()
	timeline, transportErr := handler.prepareTransportTimelineV3(
		context.Background(),
		&playback.Session{ID: "session-slow-anchor"},
		&models.MediaFile{ID: 42, FilePath: "/media/movie.mkv"},
		playback.PlannerResultV3{Plan: plan, PlayMethod: playback.PlayRemux},
	)
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("slow anchor index took %v, want it bounded near two attempts of %v", elapsed, copySeekAnchorSeekResolveTimeout)
	}
	if got := probes.Load(); got != 2 {
		t.Fatalf("anchor probes = %d, want exactly 2: one attempt and the one same-pin retry a budget timeout allows", got)
	}
	if transportErr != nil {
		t.Fatalf("transport error = %#v, want a start from the requested position without the anchor", transportErr)
	}
	if timeline.copySeekAnchorResolved {
		t.Fatalf("timeline = %#v, want the unanchored shape", timeline)
	}
	if timeline.seekSeconds != 120 || timeline.streamOriginSeconds != 120 {
		t.Fatalf("timeline = %#v, want seek and origin at the requested 120 s", timeline)
	}
	// The plan carries the same unanchored timeline the client reads: the player
	// clock starts at the requested position, not at an unknown keyframe.
	if got := plan.Timeline.PlayerStartSeconds; got != 0 {
		t.Fatalf("plan player start = %v, want 0 for the requested origin", got)
	}
	if got := plan.Timeline.StreamOriginSeconds; got != 120 {
		t.Fatalf("plan stream origin = %v, want the requested 120 s", got)
	}
	if !strings.Contains(logs.String(), "starting from the requested position without it") {
		t.Fatalf("degraded start left no log line:\n%s", logs.String())
	}
}

// TestPrepareTransportTimelineSlowAnchorIndexRefusesAProgressiveRemuxStart
// proves a copy-video start without an anchor does not hand back a progressive
// remux that produces no bytes. A segmented HLS start can degrade because its
// segment layer waits or restarts for the target; a progressive remux is one
// forward-only response with nothing to wait for, and production showed it
// answering 15 s of silence. The start answers the retryable seek verdict at
// the end of the probe budget instead, so the client keeps a retry affordance
// and never a frozen playhead.
func TestPrepareTransportTimelineSlowAnchorIndexRefusesAProgressiveRemuxStart(t *testing.T) {
	previous := copySeekAnchorSeekResolveTimeout
	copySeekAnchorSeekResolveTimeout = 40 * time.Millisecond
	defer func() { copySeekAnchorSeekResolveTimeout = previous }()

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	var probes atomic.Int32
	handler.copySeekAnchor = func(ctx context.Context, _ string, _ string, _ float64, _ int) (float64, int, error) {
		probes.Add(1)
		<-ctx.Done()
		return 0, 0, ctx.Err()
	}
	plan := &playback.PlanV3{
		PlanID:   "plan:slow-progressive-anchor",
		Delivery: playback.DeliveryRemuxProgressiveV3,
		Timeline: playback.TimelineV3{SourceStartSeconds: 919.35, PlayerStartSeconds: 919.35},
	}
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previousLogger)

	started := time.Now()
	timeline, transportErr := handler.prepareTransportTimelineV3(
		context.Background(),
		&playback.Session{ID: "session-slow-progressive-anchor"},
		&models.MediaFile{ID: 37985, FilePath: "/media/episode.mkv"},
		playback.PlannerResultV3{Plan: plan, PlayMethod: playback.PlayRemux},
	)
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("slow anchor index took %v, want it bounded near two attempts of %v", elapsed, copySeekAnchorSeekResolveTimeout)
	}
	if got := probes.Load(); got != 2 {
		t.Fatalf("anchor probes = %d, want exactly 2: one attempt and the one same-pin retry a budget timeout allows", got)
	}
	if transportErr == nil {
		t.Fatalf("timeline = %#v, want a retryable refusal with no progressive stream", timeline)
	}
	if !transportErr.retryable || !transportErr.seekFailure || transportErr.reason != transcodeStartFailedReasonV3 {
		t.Fatalf("transport error = %#v, want a retryable seek failure with the existing transcode_start_failed code", transportErr)
	}
	if !strings.Contains(logs.String(), "refusing a progressive remux start that would produce no bytes") {
		t.Fatalf("refused start left no log line:\n%s", logs.String())
	}
	// The plan must not be rewritten to an unanchored timeline the client would
	// hand to a progressive remux: the start never returns it.
	if plan.Timeline.StreamOriginSeconds != 0 {
		t.Fatalf("plan stream origin = %v, want the plan left unconfigured", plan.Timeline.StreamOriginSeconds)
	}
}

// TestSeekAnchorProgressiveProbeWindowFitsTheFirstOutputWindow pins the
// production numbers: a progressive remux seek must show a frame inside 8 s,
// the rebuilt transport needs 3 s of that for spawn and its first byte, and
// the anchor probe may therefore spend at most 5 s. The bound exists because a
// probe capped by its own 15 s budget (and a reserved retry) decided the seek
// after 15-25 s, long after the viewer could still get a picture.
func TestSeekAnchorProgressiveProbeWindowFitsTheFirstOutputWindow(t *testing.T) {
	if got := seekAnchorFirstOutputWindowV3; got != 8*time.Second {
		t.Fatalf("seek first-output window = %v, want 8s", got)
	}
	if got := seekAnchorTransportReserveV3; got != 3*time.Second {
		t.Fatalf("transport reserve = %v, want 3s", got)
	}
	if got := seekAnchorProgressiveProbeWindowV3(); got != 5*time.Second {
		t.Fatalf("progressive probe window = %v, want the remaining 5s", got)
	}
}

// TestPrepareTransportTimelineProgressiveSeekAnchorFailsInsideTheWindow proves
// a progressive-remux anchor probe that cannot answer is bounded by the seek's
// first-output window, not by the probe's own cap. It answers the existing
// retryable seek verdict after one bounded attempt instead of paying a second
// probe the seek cannot afford.
func TestPrepareTransportTimelineProgressiveSeekAnchorFailsInsideTheWindow(t *testing.T) {
	previousWindow, previousReserve := seekAnchorFirstOutputWindowV3, seekAnchorTransportReserveV3
	seekAnchorFirstOutputWindowV3, seekAnchorTransportReserveV3 = 400*time.Millisecond, 100*time.Millisecond
	defer func() {
		seekAnchorFirstOutputWindowV3, seekAnchorTransportReserveV3 = previousWindow, previousReserve
	}()
	// A source whose own probe history would otherwise ask for the full 15 s
	// cap: the seek window, not the probe, must decide.
	previousHint := copySeekAnchorProbeBudgetHint
	copySeekAnchorProbeBudgetHint = func(string, string, int) time.Duration {
		return playback.CopySeekProbeTimeout
	}
	defer func() { copySeekAnchorProbeBudgetHint = previousHint }()

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	var probes atomic.Int32
	var granted time.Duration
	handler.copySeekAnchor = func(ctx context.Context, _ string, _ string, _ float64, _ int) (float64, int, error) {
		probes.Add(1)
		if deadline, ok := ctx.Deadline(); ok {
			granted = time.Until(deadline)
		}
		<-ctx.Done()
		return 0, 0, ctx.Err()
	}
	plan := &playback.PlanV3{
		PlanID:   "plan:windowed-progressive-anchor",
		Delivery: playback.DeliveryRemuxProgressiveV3,
		Timeline: playback.TimelineV3{SourceStartSeconds: 939.32, PlayerStartSeconds: 939.32},
	}

	started := time.Now()
	_, transportErr := handler.prepareTransportTimelineV3(
		context.Background(),
		&playback.Session{ID: "session-windowed-progressive-anchor"},
		&models.MediaFile{ID: 37985, FilePath: "/media/episode.mkv"},
		playback.PlannerResultV3{Plan: plan, PlayMethod: playback.PlayRemux},
	)
	elapsed := time.Since(started)

	if elapsed > seekAnchorFirstOutputWindowV3 {
		t.Fatalf("seek anchor decided in %v, want it inside the %v first-output window", elapsed, seekAnchorFirstOutputWindowV3)
	}
	if got := probes.Load(); got != 1 {
		t.Fatalf("anchor probes = %d, want exactly 1: a retry would spend a second probe window the seek cannot afford", got)
	}
	if granted <= 0 || granted > seekAnchorProgressiveProbeWindowV3()+50*time.Millisecond {
		t.Fatalf("probe budget = %v, want it bounded by the %v probe window", granted, seekAnchorProgressiveProbeWindowV3())
	}
	if transportErr == nil || !transportErr.retryable || !transportErr.seekFailure || transportErr.reason != transcodeStartFailedReasonV3 {
		t.Fatalf("transport error = %#v, want the retryable seek verdict inside the window", transportErr)
	}
}

// TestPrepareTransportTimelineTransientAnchorMarksSeek proves an upstream 5xx
// during the anchor probe is classified as a seek failure rather than a verdict
// about the release, so the start path cannot rotate the version over it. The
// existing bounded retry still runs; only the final routing changes.
func TestPrepareTransportTimelineTransientAnchorMarksSeek(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	var probes atomic.Int32
	handler.copySeekAnchor = func(context.Context, string, string, float64, int) (float64, int, error) {
		probes.Add(1)
		return 0, 0, fmt.Errorf("%w: Server returned 5XX Server Error reply", playback.ErrTransientProvider)
	}
	handler.copySeekAnchorBackoff = func(context.Context, time.Duration) bool { return true }
	plan := &playback.PlanV3{
		PlanID:   "plan:transient-anchor",
		Delivery: playback.DeliveryRemuxHLSV3,
		Timeline: playback.TimelineV3{SourceStartSeconds: 120},
	}
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	ctx, cancel := context.WithTimeout(req.Context(), time.Minute)
	defer cancel()
	_, transportErr := handler.prepareTransportTimelineV3(
		ctx,
		&playback.Session{ID: "session-transient-anchor"},
		&models.MediaFile{ID: 42, FilePath: "/media/movie.mkv"},
		playback.PlannerResultV3{Plan: plan, PlayMethod: playback.PlayRemux},
	)
	if got := probes.Load(); got != 2 {
		t.Fatalf("anchor probes = %d, want the existing bounded retry (2)", got)
	}
	if transportErr == nil || !transportErr.retryable || !transportErr.seekFailure || transportErr.reason != transcodeStartFailedReasonV3 {
		t.Fatalf("transport error = %#v, want a retryable seek failure with the existing transcode_start_failed code", transportErr)
	}
	if !playback.IsTransientProviderError(transportErr.cause) {
		t.Fatalf("cause = %v, want the transient provider classification", transportErr.cause)
	}
}

// TestHandleStartPlaybackV3TransientSeekAnchorDoesNotRotate maps the whole start
// path: the primary release plans and its seek anchor hits relay 5xx while a
// playable alternate exists. The start must answer a retryable seek terminal and
// leave the alternate untouched instead of rotating the release.
func TestHandleStartPlaybackV3TransientSeekAnchorDoesNotRotate(t *testing.T) {
	const (
		neutral   = "virtual://movie/tt-seek-failfast"
		pinnedURI = neutral + "?result=pinned"
		altURI    = neutral + "?result=alt"
	)
	stamp := time.Now()
	source := v3HandlerFixtureFile(t)
	source.ID = 910
	source.ContentID = "movie-seek-failfast"
	source.Container = "mkv"
	source.FilePath = pinnedURI
	source.VirtualOwnerInstallationID = 5
	source.ProbeUpdatedAt = &stamp

	alternateValue := *source
	alternate := &alternateValue
	alternate.ID = 911
	alternate.FilePath = altURI
	alternate.ProbeUpdatedAt = &stamp

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), mapPlaybackFileResolver{files: map[int]*models.MediaFile{source.ID: source, alternate.ID: alternate}})
	handler.FileVersionFetcher = testPlaybackFileVersionFetcher{byContent: map[string][]*models.MediaFile{
		source.ContentID: {source, alternate},
	}}
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "true"}}
	handler.PlaybackConfig = playbackTestConfig("", "")
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	relay := remotestream.NewRelay()
	defer func() { _ = relay.Close(context.Background()) }()
	handler.RemoteStreamRelay = relay
	handler.AllowPrivateStreams = func(int) bool { return true }
	handler.VirtualPlaybackResolver = VirtualPlaybackResolverFunc(func(_ context.Context, path string, _ int, _ string, _ int) (string, error) {
		return "http://127.0.0.1:9/stream?path=" + path, nil
	})
	var resolvedURIs []string
	handler.VirtualMediaDetailedResolver = VirtualMediaDetailedResolverFunc(func(_ context.Context, uri string, _ int, _ int, _ string, _ bool, _ []string, _ string) (ResolvedVirtualMedia, error) {
		resolvedURIs = append(resolvedURIs, uri)
		switch {
		case strings.Contains(uri, "pinned"):
			return ResolvedVirtualMedia{URL: "http://127.0.0.1:9/pinned.mkv", URI: pinnedURI, CandidateID: "pinned"}, nil
		case strings.Contains(uri, "alt"):
			return ResolvedVirtualMedia{URL: "http://127.0.0.1:9/alt.mkv", URI: altURI, CandidateID: "alt"}, nil
		default:
			return ResolvedVirtualMedia{}, fmt.Errorf("unexpected resolve %q", uri)
		}
	})
	handler.copySeekAnchorBackoff = func(context.Context, time.Duration) bool { return false }
	var probes atomic.Int32
	handler.copySeekAnchor = func(_ context.Context, _ string, _ string, requested float64, segmentDuration int) (float64, int, error) {
		if probes.Add(1) == 1 {
			// The first anchor probe is the primary release: the relay answers
			// 5xx. The same-release retry is disabled, so the start must answer
			// the seek error without walking to the alternate.
			return 0, 0, fmt.Errorf("%w: Server returned 5XX Server Error reply", playback.ErrTransientProvider)
		}
		return requested, computeStartSegment(requested, segmentDuration), nil
	}

	start := v3HandlerStartRequest()
	start.FileID = source.ID
	start.QualityPreference = "auto"
	start.ClientPlaybackContext.Deliveries = map[string]playback.DeliveryCapabilityV3{
		playback.DeliveryClassProgressiveV3: {Enabled: true, SupportedOnDevice: true},
	}
	startPosition := 30.0
	start.StartPosition = &startPosition

	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, start))).WithContext(newAuthorizedPlaybackContext()))
	if rr.Code != http.StatusCreated {
		t.Fatalf("start status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var response playback.DecisionResponseV3
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("start response invalid: %v", err)
	}
	if response.Terminal == nil || response.Terminal.Reason != transcodeStartFailedReasonV3 || !response.Terminal.Retryable {
		t.Fatalf("terminal = %#v, want the retryable seek terminal", response.Terminal)
	}
	if response.PlaybackPlan != nil {
		t.Fatalf("plan = %#v, want no rotation after a failed anchor probe", response.PlaybackPlan)
	}
	if containsStringExactV3(resolvedURIs, altURI) {
		t.Fatalf("resolved URIs = %v, want no alternate resolve after a seek failure", resolvedURIs)
	}
	if !containsStringExactV3(resolvedURIs, pinnedURI) {
		t.Fatalf("resolved URIs = %v, want the primary anchor to have been resolved", resolvedURIs)
	}
}

// TestHandleStartPlaybackV3ResumeReusesRetainedVirtualEvidence proves a start
// that re-enters while the previous session is still live serves that session's
// bound candidate and probed inventory. The provider discovery resolver and the
// stream lister must not run: that is the discovery/listing/probe round-trip a
// post-failure resume used to pay before its first byte.
func TestHandleStartPlaybackV3ResumeReusesRetainedVirtualEvidence(t *testing.T) {
	const (
		neutral   = "virtual://movie/tt-retained-resume"
		pinnedURI = neutral + "?result=pinned"
	)
	stamp := time.Now()
	requested := v3HandlerFixtureFile(t)
	requested.ID = 42
	requested.ContentID = "movie-retained-resume"
	requested.Container = "mkv"
	requested.FilePath = neutral
	requested.VirtualOwnerInstallationID = 5
	requested.ProbeUpdatedAt = nil
	requested.VideoTracks = nil
	requested.AudioTracks = nil

	effective := v3HandlerFixtureFile(t)
	effective.ID = 800
	effective.ContentID = requested.ContentID
	effective.Container = "mkv"
	effective.FilePath = pinnedURI
	effective.VirtualOwnerInstallationID = 5
	effective.ProbeUpdatedAt = nil
	effective.ResolvedURL = "http://127.0.0.1:9/pinned.mkv"
	expiresAt := time.Now().Add(3 * time.Hour)
	effective.ResolvedURLExpiresAt = &expiresAt

	manager := playback.NewSessionManager(0, 0)
	manager.RegisterReconstructed(&playback.Session{
		ID:                               "session-retained-resume",
		UserID:                           1,
		ProfileID:                        "profile-1",
		MediaFileID:                      effective.ID,
		RequestedMediaFileID:             requested.ID,
		VirtualSourceURI:                 pinnedURI,
		VirtualSourceOwnerInstallationID: 5,
		VirtualSubtitleEvidenceSet:       true,
		VirtualSubtitleEvidenceURI:       pinnedURI,
		VirtualSubtitleEvidenceFileID:    effective.ID,
		VirtualAudioTracks:               append([]models.AudioTrack(nil), effective.AudioTracks...),
		VirtualSubtitleTracks:            append([]models.SubtitleTrack(nil), effective.SubtitleTracks...),
		LastActivityAt:                   stamp,
	})

	var detailedCalls, listerCalls atomic.Int32
	handler := NewPlaybackHandler(manager, mapPlaybackFileResolver{files: map[int]*models.MediaFile{requested.ID: requested, effective.ID: effective}})
	handler.VirtualFileLookup = storedURLLookup(effective)
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "true"}}
	handler.PlaybackConfig = playbackTestConfig("", "")
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	relay := remotestream.NewRelay()
	defer func() { _ = relay.Close(context.Background()) }()
	handler.RemoteStreamRelay = relay
	handler.AllowPrivateStreams = func(int) bool { return true }
	handler.VirtualMediaDetailedResolver = VirtualMediaDetailedResolverFunc(func(context.Context, string, int, int, string, bool, []string, string) (ResolvedVirtualMedia, error) {
		detailedCalls.Add(1)
		return ResolvedVirtualMedia{}, errors.New("a retained-evidence resume must not resolve the provider")
	})
	handler.VirtualPlaybackStreamLister = VirtualPlaybackStreamListerFunc(func(context.Context, string, int, string, int) ([]VirtualPlaybackStream, error) {
		listerCalls.Add(1)
		return nil, errors.New("a retained-evidence resume must not list the provider")
	})
	handler.copySeekAnchor = func(_ context.Context, _ string, _ string, requested float64, segmentDuration int) (float64, int, error) {
		return requested, computeStartSegment(requested, segmentDuration), nil
	}

	start := v3HandlerStartRequest()
	start.FileID = requested.ID
	start.QualityPreference = "auto"
	start.ClientPlaybackContext.Deliveries = map[string]playback.DeliveryCapabilityV3{
		playback.DeliveryClassProgressiveV3: {Enabled: true, SupportedOnDevice: true},
	}
	startPosition := 120.0
	start.StartPosition = &startPosition

	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, start))).WithContext(newAuthorizedPlaybackContext()))
	if rr.Code != http.StatusCreated {
		t.Fatalf("start status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var response playback.DecisionResponseV3
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("start response invalid: %v", err)
	}
	if response.Terminal != nil || response.PlaybackPlan == nil {
		t.Fatalf("terminal = %#v, plan = %#v, want a plan from the retained evidence", response.Terminal, response.PlaybackPlan)
	}
	if response.PlaybackPlan.EffectiveMediaFileID != effective.ID {
		t.Fatalf("effective file = %d, want the retained candidate row %d", response.PlaybackPlan.EffectiveMediaFileID, effective.ID)
	}
	if detailedCalls.Load() != 0 {
		t.Fatalf("provider resolves = %d, want 0 on a retained-evidence resume", detailedCalls.Load())
	}
	if listerCalls.Load() != 0 {
		t.Fatalf("provider listings = %d, want 0 on a retained-evidence resume", listerCalls.Load())
	}
}
