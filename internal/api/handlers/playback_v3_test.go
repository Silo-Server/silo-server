package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/subtitles"
	"github.com/Silo-Server/silo-server/internal/tonemap"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

type mutablePlaybackSettingsV3 struct {
	mu        sync.Mutex
	values    map[string]string
	getCalls  map[string]int
	err       error
	getErrors map[string]error
}

type gatedPlaybackSettingsV3 struct {
	started chan string
	release chan struct{}
}

func (s *gatedPlaybackSettingsV3) Get(_ context.Context, key string) (string, error) {
	s.started <- key
	<-s.release
	return "true", nil
}

type blockingPlaybackSessionSyncerV3 struct {
	started chan struct{}
	release chan struct{}
}

func (s *blockingPlaybackSessionSyncerV3) SyncNow(context.Context) error {
	select {
	case s.started <- struct{}{}:
	default:
	}
	<-s.release
	return nil
}

type orderedBlockingPlaybackScrobblerV3 struct {
	mu           sync.Mutex
	startEntered chan struct{}
	releaseStart chan struct{}
	events       []string
}

type saturatingPlaybackScrobblerV3 struct {
	entered chan string
	release chan struct{}
	mu      sync.Mutex
	starts  []string
}

func (s *saturatingPlaybackScrobblerV3) ScrobbleStart(ctx context.Context, event watchsync.ScrobbleEvent) error {
	select {
	case s.entered <- event.PlaybackSessionID:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-s.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	s.mu.Lock()
	s.starts = append(s.starts, event.PlaybackSessionID)
	s.mu.Unlock()
	return nil
}

func (*saturatingPlaybackScrobblerV3) ScrobblePause(context.Context, watchsync.ScrobbleEvent) error {
	return nil
}

func (*saturatingPlaybackScrobblerV3) ScrobbleStop(context.Context, watchsync.ScrobbleEvent) error {
	return nil
}

func (s *saturatingPlaybackScrobblerV3) recordedStarts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.starts...)
}

func (s *orderedBlockingPlaybackScrobblerV3) ScrobbleStart(context.Context, watchsync.ScrobbleEvent) error {
	select {
	case s.startEntered <- struct{}{}:
	default:
	}
	<-s.releaseStart
	s.mu.Lock()
	s.events = append(s.events, "start")
	s.mu.Unlock()
	return nil
}

func (s *orderedBlockingPlaybackScrobblerV3) ScrobblePause(context.Context, watchsync.ScrobbleEvent) error {
	s.mu.Lock()
	s.events = append(s.events, "pause")
	s.mu.Unlock()
	return nil
}

func (s *orderedBlockingPlaybackScrobblerV3) ScrobbleStop(context.Context, watchsync.ScrobbleEvent) error {
	s.mu.Lock()
	s.events = append(s.events, "stop")
	s.mu.Unlock()
	return nil
}

func (s *orderedBlockingPlaybackScrobblerV3) recordedEvents() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.events...)
}

type failingAudioPreferenceStoreV3 struct {
	userstore.UserStore
	err error
}

func (s failingAudioPreferenceStoreV3) GetAudioPreference(context.Context, string, string) (*userstore.AudioPreference, error) {
	return nil, s.err
}

type failingProgressStoreV3 struct {
	userstore.UserStore
	err error
}

func (s failingProgressStoreV3) GetProgress(context.Context, string, string) (*userstore.WatchProgress, error) {
	return nil, s.err
}

type failingSettingsResolutionStoreV3 struct {
	userstore.UserStore
	err error
}

func (s failingSettingsResolutionStoreV3) ListSettingValuesForResolution(context.Context, userstore.SettingResolutionQuery) ([]userstore.SettingValue, error) {
	return nil, s.err
}

type failingCompletePlanStoreV3 struct {
	playback.PlanStoreV3
}

type recordingRouteEventPlanStoreV3 struct {
	playback.PlanStoreV3
	events chan playback.RouteEventRecordV3
}

type releaseDeadlinePlanStoreV3 struct {
	playback.PlanStoreV3
	called       bool
	hasDeadline  bool
	timeToExpiry time.Duration
	contextErr   error
}

func (s *recordingRouteEventPlanStoreV3) RecordRouteEvent(_ context.Context, event playback.RouteEventRecordV3) error {
	s.events <- event
	return nil
}

func (s *releaseDeadlinePlanStoreV3) ReleaseReplan(ctx context.Context, _, _, _ string) error {
	s.called = true
	deadline, ok := ctx.Deadline()
	s.hasDeadline = ok
	if ok {
		s.timeToExpiry = time.Until(deadline)
	}
	s.contextErr = ctx.Err()
	return nil
}

type staticNodePlannerV3 struct {
	plan nodepool.Plan
}

func (p staticNodePlannerV3) PlanSession(string, string, bool, int) nodepool.Plan {
	return p.plan
}

func (f *failingCompletePlanStoreV3) CompleteReplan(context.Context, string, string, string, string, json.RawMessage, playback.AttemptRecordV3) error {
	return fmt.Errorf("injected complete replan failure")
}

func TestShouldTryAlternateFileV3PinsOriginalQuality(t *testing.T) {
	if shouldTryAlternateFileV3("original") || shouldTryAlternateFileV3(" ORIGINAL ") {
		t.Fatal("original quality must pin the requested media file")
	}
	for _, quality := range []string{"auto", "2160p", "1080p", "480p"} {
		if !shouldTryAlternateFileV3(quality) {
			t.Fatalf("quality %q should permit alternate selection", quality)
		}
	}
}

func TestTerminalAllowsAlternateFileV3IncludesHDRIncompatibility(t *testing.T) {
	for _, reason := range []string{"no_alternate_version", "hdr_transcode_unsupported"} {
		if !terminalAllowsAlternateFileV3(&playback.TerminalV3{Reason: reason}) {
			t.Fatalf("terminal reason %q should permit alternate selection", reason)
		}
	}
	if terminalAllowsAlternateFileV3(&playback.TerminalV3{Reason: "client_hls_unsupported"}) {
		t.Fatal("unrelated terminal reason should not permit alternate selection")
	}
}

func TestValidateAdvertisedTransformationsV3RejectsOldVideoRecipe(t *testing.T) {
	plan := &playback.PlanV3{Transformations: []playback.TransformationV3{{
		Name:          playback.TransformationVideoToH264V3,
		Executor:      playback.ExecutorServerV3,
		RecipeVersion: playback.TransformationVideoToH264RecipeVersionV3,
	}}}
	oldNode := []playback.TransformationV3{{
		Name:          playback.TransformationVideoToH264V3,
		Executor:      playback.ExecutorServerV3,
		RecipeVersion: "1",
	}}
	if err := validateAdvertisedTransformationsV3(plan, oldNode); err == nil || !strings.Contains(err.Error(), "video_to_h264@2") {
		t.Fatalf("old-node validation error = %v, want video_to_h264@2 mismatch", err)
	}
	currentNode := []playback.TransformationV3{{
		Name:          playback.TransformationVideoToH264V3,
		Executor:      playback.ExecutorServerV3,
		RecipeVersion: "2",
	}}
	if err := validateAdvertisedTransformationsV3(plan, currentNode); err != nil {
		t.Fatalf("current-node validation failed: %v", err)
	}
}

func TestHandleStartPlaybackV3ExplainsOriginalQuality4KPinWhenAlternateExists(t *testing.T) {
	for _, test := range []struct {
		name                string
		alternateResolution string
		wantMessage         string
	}{
		{name: "lower-resolution alternate exists", alternateResolution: "1080p", wantMessage: "compatible lower-resolution version of this title is available"},
		{name: "only 4K alternate exists", alternateResolution: "UHD", wantMessage: playback.TerminalMessage4KTranscodeDisabledV3},
		{name: "only label-only 8K alternate exists", alternateResolution: "8K", wantMessage: playback.TerminalMessage4KTranscodeDisabledV3},
		{name: "no alternate", wantMessage: playback.TerminalMessage4KTranscodeDisabledV3},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := v3HandlerFixtureFile(t)
			source.Resolution = "2160p"
			source.Bitrate = 32_000
			source.VideoTracks[0].Width = 3840
			source.VideoTracks[0].Height = 2160
			source.VideoTracks[0].Level = 51
			source.VideoTracks[0].Bitrate = 32_000

			versions := []*models.MediaFile{source}
			if test.alternateResolution != "" {
				alternateValue := *source
				alternate := &alternateValue
				alternate.ID = 84
				alternate.Resolution = test.alternateResolution
				alternate.Bitrate = 8_000
				alternate.VideoTracks = append([]models.VideoTrack(nil), source.VideoTracks...)
				switch test.alternateResolution {
				case "1080p":
					alternate.VideoTracks[0].Width = 1920
					alternate.VideoTracks[0].Height = 1080
					alternate.VideoTracks[0].Level = 41
				case "8K":
					alternate.VideoTracks[0].Width = 0
					alternate.VideoTracks[0].Height = 0
				}
				alternate.VideoTracks[0].Bitrate = 8_000
				versions = append(versions, alternate)
			}

			handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: source})
			handler.FileVersionFetcher = testPlaybackFileVersionFetcher{byContent: map[string][]*models.MediaFile{source.ContentID: versions}}
			handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "false"}}
			handler.PlaybackConfig = playbackTestConfig("", "")
			handler.ItemAccess = allowAllPlaybackItemAccess{}

			start := v3HandlerStartRequest()
			start.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3] = playback.DeliveryCapabilityV3{Enabled: true, SupportedOnDevice: true}
			rr := httptest.NewRecorder()
			handler.HandleStartPlayback(rr, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, start))).WithContext(newAuthorizedPlaybackContext()))

			var response playback.DecisionResponseV3
			if rr.Code != http.StatusCreated || json.Unmarshal(rr.Body.Bytes(), &response) != nil || response.Terminal == nil {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if response.Terminal.Reason != "no_alternate_version" || !strings.Contains(response.Terminal.Message, test.wantMessage) {
				t.Fatalf("terminal = %#v, want message containing %q", response.Terminal, test.wantMessage)
			}
		})
	}
}

func TestHandleStartPlaybackV3TriesAlternateAfterHDRTerminal(t *testing.T) {
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
		FrameRate: "24000/1001", Bitrate: 8_000, BitDepth: 8, VideoRange: "SDR", VideoRangeType: "SDR",
	}}

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: source})
	handler.FileVersionFetcher = testPlaybackFileVersionFetcher{byContent: map[string][]*models.MediaFile{
		source.ContentID: {source, alternate},
	}}
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "false"}}
	handler.PlaybackConfig = playbackTestConfig("", "")
	handler.ItemAccess = allowAllPlaybackItemAccess{}

	start := v3HandlerStartRequest()
	start.QualityPreference = "auto"
	start.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3] = playback.DeliveryCapabilityV3{
		Enabled: true, SupportedOnDevice: true, Containers: []string{"hls"},
		VideoCodecs: []string{"h264"}, AudioDecodeCodecs: []string{"aac"},
	}
	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, start))).WithContext(newAuthorizedPlaybackContext()))

	var response playback.DecisionResponseV3
	if rr.Code != http.StatusCreated || json.Unmarshal(rr.Body.Bytes(), &response) != nil || response.PlaybackPlan == nil {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if response.PlaybackPlan.EffectiveMediaFileID != alternate.ID {
		t.Fatalf("effective file = %d, want alternate %d", response.PlaybackPlan.EffectiveMediaFileID, alternate.ID)
	}
}

func TestHandleStartPlaybackV3TriesLater4KAlternateAfterNon4KTerminal(t *testing.T) {
	source := v3HandlerFixtureFile(t)
	source.CodecVideo = "hevc"
	source.Resolution = "2160p"
	source.Bitrate = 32_000
	source.VideoTracks[0] = models.VideoTrack{
		Codec: "hevc", Profile: "main 10", Level: 150, Width: 3840, Height: 2160,
		FrameRate: "24000/1001", Bitrate: 32_000, BitDepth: 10,
		VideoRange: "DolbyVision", VideoRangeType: "DOVIWithHDR10", DVProfile: 8, DVBLCompatID: 1,
	}

	lowerValue := *source
	lower := &lowerValue
	lower.ID = 84
	lower.Resolution = "1080p"
	lower.Bitrate = 8_000
	lower.VideoTracks = append([]models.VideoTrack(nil), source.VideoTracks...)
	lower.VideoTracks[0].Width = 1920
	lower.VideoTracks[0].Height = 1080
	lower.VideoTracks[0].Bitrate = 8_000

	playableValue := *source
	playable := &playableValue
	playable.ID = 85
	playable.CodecVideo = "h264"
	playable.Resolution = "UHD"
	playable.Bitrate = 12_000
	playable.VideoTracks = []models.VideoTrack{{
		Codec: "h264", Profile: "high", Level: 52, Width: 3840, Height: 2160,
		FrameRate: "24000/1001", Bitrate: 12_000, BitDepth: 8,
		VideoRange: "SDR", VideoRangeType: "SDR",
	}}

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: source})
	handler.FileVersionFetcher = testPlaybackFileVersionFetcher{byContent: map[string][]*models.MediaFile{
		source.ContentID: {source, lower, playable},
	}}
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "false"}}
	handler.PlaybackConfig = playbackTestConfig("", "")
	handler.ItemAccess = allowAllPlaybackItemAccess{}

	start := v3HandlerStartRequest()
	start.QualityPreference = "auto"
	start.Capabilities.MaxResolution = "2160p"
	start.Capabilities.VideoDecode[0].Levels = []int{52}
	start.Capabilities.VideoDecode[0].MaxWidth = 3840
	start.Capabilities.VideoDecode[0].MaxHeight = 2160
	start.Capabilities.VideoDecode[0].MaxBitrateKbps = 50_000
	start.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3] = playback.DeliveryCapabilityV3{
		Enabled: true, SupportedOnDevice: true, Containers: []string{"hls"},
		VideoCodecs: []string{"h264"}, AudioDecodeCodecs: []string{"aac"},
	}
	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, start))).WithContext(newAuthorizedPlaybackContext()))

	var response playback.DecisionResponseV3
	if rr.Code != http.StatusCreated || json.Unmarshal(rr.Body.Bytes(), &response) != nil || response.PlaybackPlan == nil {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if response.PlaybackPlan.EffectiveMediaFileID != playable.ID {
		t.Fatalf("effective file = %d, want later directly playable 4K alternate %d", response.PlaybackPlan.EffectiveMediaFileID, playable.ID)
	}
}

func TestHandleReplanPlaybackV3TriesLater4KAlternateAfterNon4KTerminal(t *testing.T) {
	source := v3HandlerFixtureFile(t)
	source.Resolution = "2160p"
	source.Bitrate = 32_000
	source.VideoTracks[0].Level = 52
	source.VideoTracks[0].Width = 3840
	source.VideoTracks[0].Height = 2160
	source.VideoTracks[0].Bitrate = 32_000

	lowerValue := *source
	lower := &lowerValue
	lower.ID = 84
	lower.Resolution = "1080p"
	lower.Bitrate = 8_000
	lower.HDR = true
	lower.VideoTracks = append([]models.VideoTrack(nil), source.VideoTracks...)
	lower.VideoTracks[0].Width = 1920
	lower.VideoTracks[0].Height = 1080
	lower.VideoTracks[0].Bitrate = 8_000
	lower.VideoTracks[0].VideoRange = "HDR10"
	lower.VideoTracks[0].VideoRangeType = "HDR10"

	playableValue := *source
	playable := &playableValue
	playable.ID = 85
	playable.CodecVideo = "hevc"
	playable.Resolution = "UHD"
	playable.Bitrate = 12_000
	playable.VideoTracks = []models.VideoTrack{{
		Codec: "hevc", Profile: "main", Level: 52, Width: 3840, Height: 2160,
		FrameRate: "24000/1001", Bitrate: 12_000, BitDepth: 8,
		VideoRange: "SDR", VideoRangeType: "SDR",
	}}

	manager := playback.NewSessionManager(0, 0)
	files := map[int]*models.MediaFile{source.ID: source, lower.ID: lower, playable.ID: playable}
	handler := NewPlaybackHandler(manager, mapPlaybackFileResolver{files: files})
	handler.FileVersionFetcher = testPlaybackFileVersionFetcher{byContent: map[string][]*models.MediaFile{
		source.ContentID: {source, lower, playable},
	}}
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "false"}}
	handler.PlaybackConfig = playbackTestConfig("", "")
	handler.ItemAccess = allowAllPlaybackItemAccess{}

	startRequest := v3HandlerStartRequest()
	startRequest.Capabilities.MaxResolution = "2160p"
	startRequest.Capabilities.VideoDecode[0].Levels = []int{52}
	startRequest.Capabilities.VideoDecode[0].MaxWidth = 3840
	startRequest.Capabilities.VideoDecode[0].MaxHeight = 2160
	startRequest.Capabilities.VideoDecode[0].MaxBitrateKbps = 50_000
	startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3] = playback.DeliveryCapabilityV3{
		Enabled: true, SupportedOnDevice: true, Containers: []string{"hls"},
		VideoCodecs: []string{"hevc"}, AudioDecodeCodecs: []string{"aac"},
	}
	startRR := httptest.NewRecorder()
	handler.HandleStartPlayback(startRR, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, startRequest))).WithContext(newAuthorizedPlaybackContext()))
	var started playback.DecisionResponseV3
	if startRR.Code != http.StatusCreated || json.Unmarshal(startRR.Body.Bytes(), &started) != nil || started.PlaybackPlan == nil {
		t.Fatalf("start status=%d body=%s", startRR.Code, startRR.Body.String())
	}

	replanCapabilities := startRequest.Capabilities
	replanCapabilities.CodecsVideo = []string{"hevc"}
	replanCapabilities.CodecsVideoHardware = []string{"hevc"}
	replanCapabilities.VideoDecode = []playback.VideoDecodeCapabilityV3{{
		Codec: "hevc", Profiles: []string{"main"}, Levels: []int{52}, BitDepths: []int{8},
		MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 50_000, Hardware: true,
	}}
	currentKey := playback.PlanAttemptKeyV3(*started.PlaybackPlan, startRequest.ClientPlaybackContext.Output.OutputContextID, nil)
	replanned := postPlaybackReplanV3(t, handler, started.SessionID, playback.ReplanRequestV3{
		ProtocolVersion: playback.ProtocolV3, Operation: playback.ReplanOperationFailureRecoveryV3,
		PlaybackAttemptID: startRequest.PlaybackAttemptID, ReplanRequestID: "later-4k-replan-0001",
		FailedPlanID: started.PlaybackPlan.PlanID, PlanAttemptID: "later-4k-plan-0001",
		PlanAttemptKey: currentKey, AttemptedPlanKeys: []string{currentKey}, AttemptCount: 1,
		QualityPreference: "auto", SelectedTracks: started.PlaybackPlan.SelectedTracks,
		Failure:      playback.FailureV3{Classification: "playback_error"},
		Capabilities: replanCapabilities, ClientPlaybackContext: startRequest.ClientPlaybackContext,
	})
	if replanned.Terminal != nil {
		t.Fatalf("replan terminal = %+v", *replanned.Terminal)
	}
	if replanned.PlaybackPlan == nil {
		t.Fatalf("replan = %#v", replanned)
	}
	if replanned.PlaybackPlan.EffectiveMediaFileID != playable.ID {
		t.Fatalf("effective file = %d, want later directly playable 4K alternate %d", replanned.PlaybackPlan.EffectiveMediaFileID, playable.ID)
	}
}

func TestReplanAllowsAlternateFileV3PinsSeekOperations(t *testing.T) {
	tests := []struct {
		name      string
		operation playback.ReplanOperationV3
		quality   string
		want      bool
	}{
		{name: "ordinary failure may use another version", operation: playback.ReplanOperationFailureRecoveryV3, quality: "auto", want: true},
		{name: "output change may use another version", operation: playback.ReplanOperationOutputChangeV3, quality: "auto", want: true},
		{name: "track change may use another version", operation: playback.ReplanOperationTrackChangeV3, quality: "auto", want: true},
		{name: "original quality remains pinned", operation: playback.ReplanOperationFailureRecoveryV3, quality: "original", want: false},
		{name: "exact seek reanchor pins current version", operation: playback.ReplanOperationSeekReanchorV3, quality: "auto", want: false},
		{name: "failed seek recovery pins current version", operation: playback.ReplanOperationSeekFailureRecoveryV3, quality: "auto", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := replanAllowsAlternateFileV3(test.operation, test.quality); got != test.want {
				t.Fatalf("replanAllowsAlternateFileV3(%q, %q) = %v, want %v", test.operation, test.quality, got, test.want)
			}
		})
	}
}

func (s *mutablePlaybackSettingsV3) Get(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getCalls != nil {
		s.getCalls[key]++
	}
	if s.err != nil {
		return "", s.err
	}
	if err := s.getErrors[key]; err != nil {
		return "", err
	}
	return s.values[key], nil
}

func TestPlannerSettingsV3ResultPreservesStoreFailure(t *testing.T) {
	handler := &PlaybackHandler{SettingsRepo: &mutablePlaybackSettingsV3{err: context.DeadlineExceeded}}
	_, err := handler.plannerSettingsV3Result(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("plannerSettingsV3Result() error = %v, want context deadline", err)
	}
}

func TestPlannerSettingsV3ResultPreservesAllow4KStoreFailure(t *testing.T) {
	store := &mutablePlaybackSettingsV3{
		values: map[string]string{
			config.PlaybackTranscodeHardwareToneMapSettingKey: "true",
			config.PlaybackTranscodeSoftwareToneMapSettingKey: "true",
		},
	}
	store.getErrors = map[string]error{config.Allow4KTranscodeSettingKey: context.DeadlineExceeded}
	handler := &PlaybackHandler{SettingsRepo: store}

	_, err := handler.plannerSettingsV3Result(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("plannerSettingsV3Result() error = %v, want allow-4K deadline", err)
	}
}

func TestPlannerSettingsV3ResultReadsPolicyKeysConcurrently(t *testing.T) {
	store := &gatedPlaybackSettingsV3{
		started: make(chan string, 3),
		release: make(chan struct{}),
	}
	handler := &PlaybackHandler{SettingsRepo: store}
	result := make(chan error, 1)
	go func() {
		_, err := handler.plannerSettingsV3Result(context.Background())
		result <- err
	}()

	started := make(map[string]bool, 3)
	for len(started) < 3 {
		select {
		case key := <-store.started:
			started[key] = true
		case <-time.After(time.Second):
			t.Fatalf("settings reads started concurrently = %v, want all three keys", started)
		}
	}
	close(store.release)
	if err := <-result; err != nil {
		t.Fatalf("plannerSettingsV3Result() error = %v", err)
	}
	for _, key := range []string{
		config.Allow4KTranscodeSettingKey,
		config.PlaybackTranscodeHardwareToneMapSettingKey,
		config.PlaybackTranscodeSoftwareToneMapSettingKey,
	} {
		if !started[key] {
			t.Fatalf("settings read missing key %q", key)
		}
	}
}

// A build that predates the neutral contract cannot interpret a plan, so the
// start endpoint refuses it outright instead of allocating a session it would
// have no way to drive.
func TestHandleStartPlaybackRejectsRequestsThatDoNotDeclareV3(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "no protocol version", body: `{"file_id":1,"profile_id":"profile-1"}`},
		{name: "legacy protocol version", body: `{"protocol_version":2,"file_id":1,"profile_id":"profile-1"}`},
		{name: "draft v3 without evidence markers", body: `{"protocol_version":3,"file_id":1,"profile_id":"profile-1","client_capabilities":{"codecs_video":["h264"]}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := playback.NewSessionManager(0, 0)
			handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: v3HandlerFixtureFile(t)})

			req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(tc.body))
			req = req.WithContext(newAuthorizedPlaybackContext())
			rr := httptest.NewRecorder()
			handler.HandleStartPlayback(rr, req)

			if rr.Code != http.StatusUpgradeRequired {
				t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
			}
			var response playback.ErrorResponseV3
			if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response != playback.LegacyUpgradeErrorV3() {
				t.Fatalf("error = %q, body = %s", response.Error, rr.Body.String())
			}
			if got := len(manager.AllSessions()); got != 0 {
				t.Fatalf("sessions = %d, want 0", got)
			}
		})
	}
}

func TestHandleStartPlaybackV3CommitsStartSideEffectsOnce(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	file.EpisodeID = "episode-1"
	file.MediaFolderID = 7
	scrobbler := &recordingPlaybackWatchScrobbler{}
	analyzer := &fakePlaybackIntroAnalyzer{started: make(chan struct{}, 1)}
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{
		markers.SettingLazyPlayback: "true",
		markers.SettingMode:         "local",
	}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.EpisodeLookup = testEpisodeLookup{episode: &models.Episode{ContentID: file.EpisodeID, SeriesID: "series-1"}}
	handler.WatchScrobbler = scrobbler
	handler.IntroRepository = fakePlaybackIntroEligibility{eligible: true}
	handler.IntroAnalyzer = analyzer
	handler.MarkerLazyContext = context.Background()

	startRequest := v3HandlerStartRequest()
	position := 37.0
	startRequest.StartPosition = &position
	body := marshalV3StartRequest(t, startRequest)
	start := func() playback.DecisionResponseV3 {
		rr := httptest.NewRecorder()
		handler.HandleStartPlayback(rr, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(body)).WithContext(newAuthorizedPlaybackContext()))
		var response playback.DecisionResponseV3
		if rr.Code != http.StatusCreated || json.Unmarshal(rr.Body.Bytes(), &response) != nil || response.PlaybackPlan == nil {
			t.Fatalf("start status=%d body=%s", rr.Code, rr.Body.String())
		}
		return response
	}
	first := start()
	second := start()
	if first.SessionID != second.SessionID {
		t.Fatalf("idempotent replay session = %q, want %q", second.SessionID, first.SessionID)
	}
	handler.waitForPlaybackStartSideEffectsV3(context.Background(), first.SessionID)
	if len(scrobbler.starts) != 1 {
		t.Fatalf("start scrobbles = %d, want 1", len(scrobbler.starts))
	}
	event := scrobbler.starts[0]
	if event.PlaybackSessionID != first.SessionID || event.MediaItemID != file.EpisodeID || event.PositionSeconds != position || event.DurationSeconds != float64(file.Duration) {
		t.Fatalf("start scrobble = %#v", event)
	}
	select {
	case <-analyzer.started:
	case <-time.After(time.Second):
		t.Fatal("v3 start did not queue lazy marker analysis")
	}
	if analyzer.callCount() != 1 {
		t.Fatalf("lazy marker calls = %d, want 1", analyzer.callCount())
	}
}

func TestHandleStartPlaybackV3DoesNotWaitForSessionSync(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	syncer := &blockingPlaybackSessionSyncerV3{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.SessionSyncer = syncer
	body := marshalV3StartRequest(t, v3HandlerStartRequest())
	responseReady := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rr := httptest.NewRecorder()
		handler.HandleStartPlayback(rr, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(body)).WithContext(newAuthorizedPlaybackContext()))
		responseReady <- rr
	}()

	select {
	case <-syncer.started:
	case <-time.After(time.Second):
		t.Fatal("session sync did not start")
	}
	select {
	case rr := <-responseReady:
		if rr.Code != http.StatusCreated {
			t.Fatalf("start status=%d body=%s", rr.Code, rr.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("playback start waited for the blocked session sync")
	}
	close(syncer.release)
}

func TestPlaybackStartSideEffectsPreserveImmediateStopScrobbleOrder(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	scrobbler := &orderedBlockingPlaybackScrobblerV3{
		startEntered: make(chan struct{}, 1),
		releaseStart: make(chan struct{}),
	}
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.WatchScrobbler = scrobbler
	body := marshalV3StartRequest(t, v3HandlerStartRequest())
	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(body)).WithContext(newAuthorizedPlaybackContext()))
	var response playback.DecisionResponseV3
	if rr.Code != http.StatusCreated || json.Unmarshal(rr.Body.Bytes(), &response) != nil {
		t.Fatalf("start status=%d body=%s", rr.Code, rr.Body.String())
	}
	select {
	case <-scrobbler.startEntered:
	case <-time.After(time.Second):
		t.Fatal("start scrobble did not begin")
	}
	session, err := handler.sessionMgr.GetSession(response.SessionID)
	if err != nil {
		t.Fatalf("GetSession() error = %v", err)
	}
	stopped := make(chan struct{})
	go func() {
		handler.finalizeSessionStop(context.Background(), session, false, "", true)
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("stop completed before the start side effects were released")
	default:
	}
	close(scrobbler.releaseStart)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("stop did not complete after start side effects")
	}
	if got := scrobbler.recordedEvents(); !reflect.DeepEqual(got, []string{"start", "stop"}) {
		t.Fatalf("scrobble events = %v, want [start stop]", got)
	}
}

func TestQueuedPlaybackStartSideEffectsAreDroppedOnStop(t *testing.T) {
	scrobbler := &saturatingPlaybackScrobblerV3{
		entered: make(chan string, playbackStartSideEffectsWorkersV3+1),
		release: make(chan struct{}),
	}
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.WatchScrobbler = scrobbler
	file := &models.MediaFile{ID: 1, ContentID: "movie-1", Duration: 600}

	for i := range playbackStartSideEffectsWorkersV3 {
		sessionID := fmt.Sprintf("running-%d", i)
		handler.enqueuePlaybackStartSideEffectsV3(context.Background(), &playback.Session{ID: sessionID}, file, 1, "profile-1", 0)
	}
	for range playbackStartSideEffectsWorkersV3 {
		select {
		case <-scrobbler.entered:
		case <-time.After(time.Second):
			t.Fatal("start side-effect workers did not saturate")
		}
	}

	queuedSessionID := "queued-stop"
	handler.enqueuePlaybackStartSideEffectsV3(context.Background(), &playback.Session{ID: queuedSessionID}, file, 1, "profile-1", 0)
	handler.cancelPlaybackStartSideEffectsV3(context.Background(), queuedSessionID)
	close(scrobbler.release)
	handler.waitForPlaybackStartSideEffectsV3(context.Background(), queuedSessionID)

	if starts := scrobbler.recordedStarts(); slices.Contains(starts, queuedSessionID) {
		t.Fatalf("queued stopped session emitted a start scrobble: %v", starts)
	}
}

func TestHandleStartPlaybackV3RejectsAttemptReplayFromAnotherDevice(t *testing.T) {
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: v3HandlerFixtureFile(t)})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	body := marshalV3StartRequest(t, v3HandlerStartRequest())

	start := func(deviceID string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(body)).WithContext(newAuthorizedPlaybackContext())
		req.Header.Set(deviceIDHeader, deviceID)
		rr := httptest.NewRecorder()
		handler.HandleStartPlayback(rr, req)
		return rr
	}

	first := start("  living-room  ")
	sameDevice := start("living-room")
	otherDevice := start("bedroom")
	if first.Code != http.StatusCreated || sameDevice.Code != http.StatusCreated {
		t.Fatalf("same-device statuses = %d, %d; first=%s replay=%s", first.Code, sameDevice.Code, first.Body.String(), sameDevice.Body.String())
	}
	if first.Body.String() != sameDevice.Body.String() {
		t.Fatalf("normalized same-device replay changed response:\nfirst=%s\nreplay=%s", first.Body.String(), sameDevice.Body.String())
	}
	if otherDevice.Code != http.StatusConflict || !strings.Contains(otherDevice.Body.String(), "playback_attempt_reused") {
		t.Fatalf("other-device status = %d, body = %s", otherDevice.Code, otherDevice.Body.String())
	}
	if got := len(manager.AllSessions()); got != 1 {
		t.Fatalf("sessions = %d, want 1", got)
	}
}

func TestHandleStartPlaybackV3AcceptsPreDeviceDigestReplay(t *testing.T) {
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: v3HandlerFixtureFile(t)})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	startRequest := v3HandlerStartRequest()
	body := marshalV3StartRequest(t, startRequest)

	start := func(requestBody, deviceID string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(requestBody)).WithContext(newAuthorizedPlaybackContext())
		req.Header.Set(deviceIDHeader, deviceID)
		rr := httptest.NewRecorder()
		handler.HandleStartPlayback(rr, req)
		return rr
	}

	first := start(body, "living-room")
	if first.Code != http.StatusCreated {
		t.Fatalf("first status = %d, body = %s", first.Code, first.Body.String())
	}
	var response playback.DecisionResponseV3
	if err := json.Unmarshal(first.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	record, err := handler.PlanStoreV3.GetAttempt(context.Background(), response.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	legacyDigest := sha256.Sum256([]byte(body))
	record.RequestDigest = hex.EncodeToString(legacyDigest[:])
	handler.PlanStoreV3.(*playback.MemoryPlanStoreV3).ReplaceAttempt(context.Background(), *record)

	replay := start(body, "living-room")
	if replay.Code != http.StatusCreated || replay.Body.String() != first.Body.String() {
		t.Fatalf("legacy replay status = %d, body = %s; first = %s", replay.Code, replay.Body.String(), first.Body.String())
	}

	changedRequest := startRequest
	position := 42.0
	changedRequest.StartPosition = &position
	conflict := start(marshalV3StartRequest(t, changedRequest), "living-room")
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "playback_attempt_reused") {
		t.Fatalf("changed legacy replay status = %d, body = %s", conflict.Code, conflict.Body.String())
	}
}

func TestHandlePlaybackCapabilityV3AdvertisesTheFinalizedContract(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/playback/capability", nil).WithContext(newAuthorizedPlaybackContext())
	rr := httptest.NewRecorder()
	handler.HandlePlaybackCapabilityV3(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var response playback.CapabilityResponseV3
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	// v3 is the only playback protocol, so the endpoint cannot report itself
	// disabled: there is no second protocol left to fall back to.
	if !response.Enabled || response.Reason != "" ||
		len(response.ProtocolVersions) != 1 || response.ProtocolVersions[0] != playback.ProtocolV3 ||
		len(response.Deliveries) != 4 ||
		!playback.HasFeatureV3(response.Features, playback.FeatureSeekReanchorV3) ||
		!playback.HasFeatureV3(response.Features, playback.FeatureDirectStreamResumeV3) {
		t.Fatalf("capability response = %#v", response)
	}
}

func TestHandleStartPlaybackV3ReturnsExecutableDirectPlan(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "true"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, v3HandlerStartRequest())))
	req = req.WithContext(newAuthorizedPlaybackContext())
	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var response playback.DecisionResponseV3
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Outcome != playback.OutcomePlayableV3 || response.PlaybackPlan == nil {
		t.Fatalf("response = %#v", response)
	}
	if response.PlaybackPlan.Delivery != playback.DeliveryOriginalHTTPV3 || response.PlaybackPlan.PlanAttemptKey == "" || response.PlaybackPlan.Stream.URL == "" {
		t.Fatalf("plan = %#v", response.PlaybackPlan)
	}
	if response.PlaybackPlan.RequestedMediaFileID != file.ID || response.PlaybackPlan.EffectiveMediaFileID != file.ID || response.PlaybackPlan.Source.MediaFileID != file.ID {
		t.Fatalf("source identity = %#v", response.PlaybackPlan)
	}
	if got := len(manager.AllSessions()); got != 1 {
		t.Fatalf("sessions = %d, want 1", got)
	}
}

func TestHandleStartPlaybackV3NegotiatesHeaderAuthenticatedDirectAndSubtitleURLs(t *testing.T) {
	for _, test := range []struct {
		name       string
		optIn      bool
		wantStream bool
	}{
		{name: "opted-in URLs carry no playback credential", optIn: true},
		{name: "legacy URL keeps restart token", wantStream: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			file := v3HandlerFixtureFile(t)
			file.ExternalSubtitles = []models.ExternalSubtitle{{Path: writePlaybackTestMediaFile(t, "movie.eng.srt"), Language: "eng", Format: "srt"}}
			manager := playback.NewSessionManager(0, 0)
			handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: file})
			handler.JWTSecret = "test-stream-signing-secret"
			handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "true"}}
			handler.ItemAccess = allowAllPlaybackItemAccess{}

			start := v3HandlerStartRequest()
			if test.optIn {
				start.ClientFeatures = append(start.ClientFeatures, playback.FeatureHeaderAuthenticatedMediaV3)
			}
			subtitleIndex := 0
			start.SubtitleTrackID = playback.TrackIDV3(file.ID, "subtitle", subtitleIndex)
			start.SubtitleTrackIndex = &subtitleIndex
			rr := httptest.NewRecorder()
			handler.HandleStartPlayback(rr, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, start))).WithContext(newAuthorizedPlaybackContext()))

			var response playback.DecisionResponseV3
			if rr.Code != http.StatusCreated || json.Unmarshal(rr.Body.Bytes(), &response) != nil || response.PlaybackPlan == nil {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			streamURL, err := url.Parse(response.PlaybackPlan.Stream.URL)
			if err != nil {
				t.Fatal(err)
			}
			if streamURL.IsAbs() || streamURL.Path != "/stream/"+response.SessionID {
				t.Fatalf("stream URL = %q, want API-local session route", response.PlaybackPlan.Stream.URL)
			}
			if got := streamURL.Query().Get(streamTokenParam); (got != "") != test.wantStream {
				t.Fatalf("stream token present = %v for URL %q, want %v", got != "", response.PlaybackPlan.Stream.URL, test.wantStream)
			}
			if len(response.PlaybackPlan.Stream.Headers) != 0 {
				t.Fatalf("plan persisted bearer material in headers: %#v", response.PlaybackPlan.Stream.Headers)
			}

			artifact := response.PlaybackPlan.Subtitle.Artifact
			if artifact == nil || len(response.PlaybackPlan.Subtitle.Inventory) != 1 {
				t.Fatalf("subtitle contract = %#v", response.PlaybackPlan.Subtitle)
			}
			for _, raw := range []string{artifact.URL, response.PlaybackPlan.Subtitle.Inventory[0].URL} {
				parsed, parseErr := url.Parse(raw)
				if parseErr != nil || parsed.IsAbs() || parsed.Query().Get(streamTokenParam) != "" || !strings.HasPrefix(parsed.Path, "/stream/"+response.SessionID+"/subtitles/") {
					t.Fatalf("subtitle URL = %q, want tokenless API-local route (parse error %v)", raw, parseErr)
				}
			}
			if test.optIn && !playback.HasFeatureV3(response.ServerFeatures, playback.FeatureHeaderAuthenticatedMediaV3) {
				t.Fatalf("server features = %v, want %q", response.ServerFeatures, playback.FeatureHeaderAuthenticatedMediaV3)
			}
		})
	}
}

func TestHandleReplanPlaybackV3CannotDowngradeHeaderAuthenticatedAttempt(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	handler.JWTSecret = "test-stream-signing-secret"
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "true"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}

	start := v3HandlerStartRequest()
	start.ClientFeatures = append(start.ClientFeatures, playback.FeatureHeaderAuthenticatedMediaV3)
	startRR := httptest.NewRecorder()
	handler.HandleStartPlayback(startRR, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, start))).WithContext(newAuthorizedPlaybackContext()))
	var started playback.DecisionResponseV3
	if startRR.Code != http.StatusCreated || json.Unmarshal(startRR.Body.Bytes(), &started) != nil || started.PlaybackPlan == nil {
		t.Fatalf("start status=%d body=%s", startRR.Code, startRR.Body.String())
	}

	nextContext := start.ClientPlaybackContext
	nextContext.Output.OutputContextID = "route-2"
	replanned := postPlaybackReplanV3(t, handler, started.SessionID, playback.ReplanRequestV3{
		ProtocolVersion:       playback.ProtocolV3,
		ClientFeatures:        []string{}, // explicit attempted downgrade
		Operation:             playback.ReplanOperationOutputChangeV3,
		PlaybackAttemptID:     start.PlaybackAttemptID,
		ReplanRequestID:       "header-auth-replan-0001",
		FailedPlanID:          started.PlaybackPlan.PlanID,
		PlanAttemptID:         "header-auth-attempt-0001",
		PlanAttemptKey:        started.PlaybackPlan.PlanAttemptKey,
		AttemptCount:          1,
		PositionSeconds:       12,
		SelectedTracks:        started.PlaybackPlan.SelectedTracks,
		Capabilities:          start.Capabilities,
		ClientPlaybackContext: nextContext,
	})
	if replanned.PlaybackPlan == nil {
		t.Fatalf("replan = %#v", replanned)
	}
	parsed, err := url.Parse(replanned.PlaybackPlan.Stream.URL)
	if err != nil || parsed.IsAbs() || parsed.Query().Get(streamTokenParam) != "" {
		t.Fatalf("replan URL = %q, want tokenless API-local route (parse error %v)", replanned.PlaybackPlan.Stream.URL, err)
	}
	record, err := handler.PlanStoreV3.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !playback.HasFeatureV3(record.NormalizedRequest.ClientFeatures, playback.FeatureHeaderAuthenticatedMediaV3) {
		t.Fatalf("durable client features = %v, secure transport mode was downgraded", record.NormalizedRequest.ClientFeatures)
	}
}

// The inventory is the authoritative subtitle menu, so it has to be fetchable
// before the user has picked anything. A start that resolves to `off` still
// publishes session-scoped URLs on every sidecar entry; gating them on the
// current selection left clients that build their picker from the inventory —
// the Cast receiver's text tracks, for one — with nothing to offer.
func TestHandleStartPlaybackV3PublishesSubtitleURLsWithSubtitlesOff(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	file.ExternalSubtitles = []models.ExternalSubtitle{{Path: "/media/movie.eng.srt", Language: "eng", Format: "srt"}}
	file.SubtitleTracks = []models.SubtitleTrack{
		{Index: 0, Codec: "ass", Language: "jpn"},
		{Index: 1, Codec: "dvd_subtitle", Language: "fra"},
	}
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "true"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, v3HandlerStartRequest())))
	req = req.WithContext(newAuthorizedPlaybackContext())
	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var response playback.DecisionResponseV3
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.PlaybackPlan == nil {
		t.Fatalf("response = %#v", response)
	}
	if response.PlaybackPlan.Subtitle.Mode != playback.SubtitleOffV3 || response.PlaybackPlan.Subtitle.Artifact != nil {
		t.Fatalf("subtitle decision = %#v, want off with no artifact", response.PlaybackPlan.Subtitle)
	}
	inventory := response.PlaybackPlan.Subtitle.Inventory
	if len(inventory) != 3 {
		t.Fatalf("inventory = %#v, want all three tracks", inventory)
	}
	for _, item := range inventory {
		switch item.Delivery {
		case playback.SubtitleDeliverySidecarV3:
			if !strings.HasPrefix(item.URL, "/stream/"+response.SessionID+"/subtitles/") {
				t.Errorf("track %d (%s) url = %q, want a session-scoped sidecar URL", item.CombinedIndex, item.Codec, item.URL)
			}
		case playback.SubtitleDeliveryBurnInOnlyV3:
			if item.URL != "" {
				t.Errorf("track %d (%s) is burn-in only but published url %q", item.CombinedIndex, item.Codec, item.URL)
			}
		default:
			t.Errorf("track %d has unknown delivery %q", item.CombinedIndex, item.Delivery)
		}
	}
	if inventory[1].FontBundleURL == "" {
		t.Errorf("embedded ASS track published no font bundle: %#v", inventory[1])
	}
}

func TestHandleStartPlaybackV3DuplicateAttemptReturnsOriginalSession(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "true"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	body := marshalV3StartRequest(t, v3HandlerStartRequest())

	start := func() playback.DecisionResponseV3 {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(body)).WithContext(newAuthorizedPlaybackContext())
		rr := httptest.NewRecorder()
		handler.HandleStartPlayback(rr, req)
		if rr.Code != http.StatusCreated {
			t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
		}
		var response playback.DecisionResponseV3
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	first := start()
	second := start()
	if first.SessionID == "" || second.SessionID != first.SessionID {
		t.Fatalf("first session %q, second %q", first.SessionID, second.SessionID)
	}
	if got := len(manager.AllSessions()); got != 1 {
		t.Fatalf("sessions = %d, want 1", got)
	}
}

func TestHandleStartPlaybackV3PersistsAndReplaysTerminalDecision(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"transcode_enabled": "false"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	request := v3HandlerStartRequest()
	request.Capabilities.CodecsVideo = nil
	request.Capabilities.CodecsVideoHardware = nil
	request.Capabilities.VideoDecode = nil
	request.Capabilities.Containers = nil
	body := marshalV3StartRequest(t, request)

	start := func(payload string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(payload)).WithContext(newAuthorizedPlaybackContext())
		rr := httptest.NewRecorder()
		handler.HandleStartPlayback(rr, req)
		return rr
	}
	first := start(body)
	second := start(body)
	if first.Code != http.StatusCreated || second.Code != http.StatusCreated {
		t.Fatalf("terminal statuses = %d, %d; first=%s second=%s", first.Code, second.Code, first.Body.String(), second.Body.String())
	}
	if first.Body.String() != second.Body.String() {
		t.Fatalf("terminal replay changed body:\nfirst %s\nsecond %s", first.Body.String(), second.Body.String())
	}
	var response playback.DecisionResponseV3
	if err := json.Unmarshal(first.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Terminal == nil || response.PlaybackPlan != nil {
		t.Fatalf("response = %#v, want terminal", response)
	}
	record, err := handler.PlanStoreV3.GetAttemptByPlaybackAttemptID(context.Background(), request.PlaybackAttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if record.SessionID != "" || record.StartResponse.Terminal == nil || record.RequestDigest == "" {
		t.Fatalf("terminal attempt = %#v", record)
	}

	changed := request
	changed.QualityPreference = "auto"
	conflict := start(marshalV3StartRequest(t, changed))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("changed request status = %d, body = %s", conflict.Code, conflict.Body.String())
	}
}

func TestHandleStartPlaybackV3RejectsProfileMismatch(t *testing.T) {
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: v3HandlerFixtureFile(t)})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{}}
	request := v3HandlerStartRequest()
	request.ProfileID = "profile-other"
	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, request))).WithContext(newAuthorizedPlaybackContext())
	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, req)
	if rr.Code != http.StatusBadRequest || len(manager.AllSessions()) != 0 {
		t.Fatalf("status = %d, sessions = %d, body = %s", rr.Code, len(manager.AllSessions()), rr.Body.String())
	}
}

func TestPreferredAudioTrackIndexV3PropagatesSeriesPreferenceReadFailure(t *testing.T) {
	wantErr := errors.New("audio preference store unavailable")
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.StoreProvider = testUserStoreProvider{store: failingAudioPreferenceStoreV3{err: wantErr}}
	handler.EpisodeLookup = testEpisodeLookup{episode: &models.Episode{SeriesID: "series-1"}}
	file := &models.MediaFile{
		EpisodeID:   "episode-1",
		AudioTracks: []models.AudioTrack{{Codec: "aac", Language: "eng"}, {Codec: "aac", Language: "spa"}},
	}

	if _, err := handler.preferredAudioTrackIndexV3(context.Background(), 1, "profile-1", "", file); !errors.Is(err, wantErr) {
		t.Fatalf("preferredAudioTrackIndexV3 error = %v, want %v", err, wantErr)
	}
}

func TestPreferredAudioTrackIndexV3PropagatesCanonicalPreferenceReadFailure(t *testing.T) {
	wantErr := errors.New("settings resolution unavailable")
	store := failingSettingsResolutionStoreV3{UserStore: newPlaybackTestStore(t), err: wantErr}
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.StoreProvider = testUserStoreProvider{store: store}
	file := &models.MediaFile{
		AudioTracks: []models.AudioTrack{{Codec: "aac", Language: "eng"}, {Codec: "aac", Language: "spa"}},
	}

	if _, err := handler.preferredAudioTrackIndexV3(context.Background(), 1, "profile-1", "living-room", file); !errors.Is(err, wantErr) {
		t.Fatalf("preferredAudioTrackIndexV3 error = %v, want %v", err, wantErr)
	}
}

func TestResumePositionV3PropagatesProgressReadFailure(t *testing.T) {
	wantErr := errors.New("progress store unavailable")
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.StoreProvider = testUserStoreProvider{store: failingProgressStoreV3{err: wantErr}}
	file := &models.MediaFile{ContentID: "movie-1"}

	if _, err := handler.resumePositionV3(context.Background(), 1, "profile-1", file); !errors.Is(err, wantErr) {
		t.Fatalf("resumePositionV3 error = %v, want %v", err, wantErr)
	}
}

func TestHandleReplanPlaybackV3UpdatesSelectedAudioAndReplaysIdempotently(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	file.AudioTracks = append(file.AudioTracks, models.AudioTrack{Codec: "aac", Channels: 2, Layout: "stereo", Language: "spa"})
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	handler.JWTSecret = "test-secret"
	stubCopySeekAnchorV3(handler)
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "true"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	startRequest := v3HandlerStartRequest()
	startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassProgressiveV3] = playback.DeliveryCapabilityV3{Enabled: true, SupportedOnDevice: true}
	startRequest.ClientFeatures = append(startRequest.ClientFeatures, playback.FeatureClientVideoTransforms)
	delivery := startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3]
	delivery.Transformations = []playback.TransformationV3{{Name: playback.ClientDV7ToDV81V3, Executor: playback.ExecutorClientV3, RecipeVersion: playback.ClientDVTransformVersionV3}}
	startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3] = delivery
	startBody := marshalV3StartRequest(t, startRequest)
	startReq := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(startBody)).WithContext(newAuthorizedPlaybackContext())
	startRR := httptest.NewRecorder()
	handler.HandleStartPlayback(startRR, startReq)
	if startRR.Code != http.StatusCreated {
		t.Fatalf("start status = %d, body = %s", startRR.Code, startRR.Body.String())
	}
	var started playback.DecisionResponseV3
	if err := json.Unmarshal(startRR.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if started.PlaybackPlan == nil {
		t.Fatal("start returned no plan")
	}
	originalSession, err := manager.GetSession(started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	originalClaims := streamClaimsFromPlanURL(t, started.PlaybackPlan.Stream.URL, handler.JWTSecret)
	if originalClaims.OriginalStartedAtUnixNano != originalSession.StartedAt.UnixNano() {
		t.Fatalf("start ostn = %d, want %d", originalClaims.OriginalStartedAtUnixNano, originalSession.StartedAt.UnixNano())
	}
	// Cross the JWT NumericDate second boundary so the replan proves ostn stays
	// immutable even though Sign rewrites iat on the replacement token.
	time.Sleep(time.Until(time.Unix(time.Now().Unix()+1, 0)) + 10*time.Millisecond)
	audioIndex := 1
	bandwidthEstimate := 3_500
	bandwidthCap := 4_000
	failedKey := playback.PlanAttemptKeyV3(*started.PlaybackPlan, startRequest.ClientPlaybackContext.Output.OutputContextID, nil)
	replan := playback.ReplanRequestV3{ProtocolVersion: playback.ProtocolV3, PlaybackAttemptID: startRequest.PlaybackAttemptID, ReplanRequestID: "replan-0001", FailedPlanID: started.PlaybackPlan.PlanID, PlanAttemptID: "plan-attempt-0001", PlanAttemptKey: failedKey, AttemptedPlanKeys: []string{failedKey}, AttemptCount: 1, QualityPreference: "original", PositionSeconds: 12, Metered: true, BandwidthEstimateKbps: &bandwidthEstimate, BandwidthCapKbps: &bandwidthCap, SelectedTracks: playback.SelectedTracksV3{Audio: &playback.TrackIdentityV3{ID: playback.TrackIDV3(file.ID, "audio", audioIndex), Index: &audioIndex}}, Failure: playback.FailureV3{Classification: "audio_renderer_error"}, Capabilities: startRequest.Capabilities, ClientPlaybackContext: startRequest.ClientPlaybackContext}
	replan.ClientPlaybackContext.AppBuild = "nightly\x00secret"
	replan.ClientPlaybackContext.AppChannel = "beta\x00secret"
	replanBody, err := json.Marshal(replan)
	if err != nil {
		t.Fatal(err)
	}

	call := func() playback.DecisionResponseV3 {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/"+started.SessionID+"/replan", strings.NewReader(string(replanBody))).WithContext(newAuthorizedPlaybackContext())
		req = withPlaybackRouteParam(req, "session_id", started.SessionID)
		rr := httptest.NewRecorder()
		handler.HandleReplanPlaybackV3(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("replan status = %d, body = %s", rr.Code, rr.Body.String())
		}
		var response playback.DecisionResponseV3
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	first := call()
	second := call()
	if first.PlaybackPlan == nil || second.PlaybackPlan == nil || first.PlaybackPlan.PlanID != second.PlaybackPlan.PlanID {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	replanClaims := streamClaimsFromPlanURL(t, first.PlaybackPlan.Stream.URL, handler.JWTSecret)
	if replanClaims.OriginalStartedAtUnixNano != originalSession.StartedAt.UnixNano() {
		t.Fatalf("replan ostn = %d, want original %d", replanClaims.OriginalStartedAtUnixNano, originalSession.StartedAt.UnixNano())
	}
	if originalClaims.IssuedAt == nil || replanClaims.IssuedAt == nil || originalClaims.IssuedAt.Equal(replanClaims.IssuedAt.Time) {
		t.Fatalf("iat did not move across replan: original=%v replan=%v", originalClaims.IssuedAt, replanClaims.IssuedAt)
	}
	session, err := manager.GetSession(started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if session.AudioTrackIndex != audioIndex {
		t.Fatalf("audio index = %d, want %d", session.AudioTrackIndex, audioIndex)
	}
	record, err := handler.PlanStoreV3.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !record.NormalizedRequest.Metered || record.NormalizedRequest.BandwidthEstimateKbps == nil || *record.NormalizedRequest.BandwidthEstimateKbps != bandwidthEstimate ||
		record.NormalizedRequest.BandwidthCapKbps == nil || *record.NormalizedRequest.BandwidthCapKbps != bandwidthCap {
		t.Fatalf("stored replan network evidence = %#v", record.NormalizedRequest)
	}
	if !playback.HasFeatureV3(record.NormalizedRequest.ClientFeatures, playback.FeatureClientVideoTransforms) {
		t.Fatalf("omitted replan client_features lost durable start features: %#v", record.NormalizedRequest.ClientFeatures)
	}
	if got := record.NormalizedRequest.ClientPlaybackContext.AppBuild; got != "nightlysecret" {
		t.Fatalf("stored normalized app_build = %q, want %q", got, "nightlysecret")
	}
	if got := record.NormalizedRequest.ClientPlaybackContext.AppChannel; got != "betasecret" {
		t.Fatalf("stored normalized app_channel = %q, want %q", got, "betasecret")
	}
	retryReq := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(startBody)).WithContext(newAuthorizedPlaybackContext())
	retryRR := httptest.NewRecorder()
	handler.HandleStartPlayback(retryRR, retryReq)
	var retried playback.DecisionResponseV3
	if retryRR.Code != http.StatusCreated || json.Unmarshal(retryRR.Body.Bytes(), &retried) != nil || retried.PlaybackPlan == nil {
		t.Fatalf("start replay status=%d body=%s", retryRR.Code, retryRR.Body.String())
	}
	if retried.PlaybackPlan.PlanID != started.PlaybackPlan.PlanID {
		t.Fatalf("start replay plan = %q, want original %q", retried.PlaybackPlan.PlanID, started.PlaybackPlan.PlanID)
	}
	replan.PositionSeconds++
	conflictBody, err := json.Marshal(replan)
	if err != nil {
		t.Fatal(err)
	}
	conflictReq := httptest.NewRequest(http.MethodPost, "/api/v1/playback/"+started.SessionID+"/replan", strings.NewReader(string(conflictBody))).WithContext(newAuthorizedPlaybackContext())
	conflictReq = withPlaybackRouteParam(conflictReq, "session_id", started.SessionID)
	conflictRR := httptest.NewRecorder()
	handler.HandleReplanPlaybackV3(conflictRR, conflictReq)
	if conflictRR.Code != http.StatusConflict || !strings.Contains(conflictRR.Body.String(), "idempotency_key_reused") {
		t.Fatalf("conflict status = %d, body = %s", conflictRR.Code, conflictRR.Body.String())
	}
}

func streamClaimsFromPlanURL(t *testing.T, rawURL, secret string) *streamtoken.Claims {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	token := u.Query().Get(streamTokenParam)
	if token == "" {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		for i, part := range parts {
			if part == "transcode" && i+1 < len(parts) {
				token = parts[i+1]
				break
			}
		}
	}
	if token == "" {
		t.Fatalf("plan URL has no stream token: %q", rawURL)
	}
	claims, err := streamtoken.Verify(token, secret)
	if err != nil {
		t.Fatal(err)
	}
	return claims
}

func TestHandleReplanPlaybackV3FailureDoesNotReplaceDurableStartDecision(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	startRequest := v3HandlerStartRequest()
	startBody := marshalV3StartRequest(t, startRequest)
	startRR := httptest.NewRecorder()
	handler.HandleStartPlayback(startRR, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(startBody)).WithContext(newAuthorizedPlaybackContext()))
	if startRR.Code != http.StatusCreated {
		t.Fatalf("start status = %d, body = %s", startRR.Code, startRR.Body.String())
	}
	var started playback.DecisionResponseV3
	if err := json.Unmarshal(startRR.Body.Bytes(), &started); err != nil || started.PlaybackPlan == nil {
		t.Fatalf("start response: err=%v response=%#v", err, started)
	}

	currentKey := playback.PlanAttemptKeyV3(*started.PlaybackPlan, startRequest.ClientPlaybackContext.Output.OutputContextID, nil)
	handler.fileResolver = testPlaybackFileResolver{}
	replan := playback.ReplanRequestV3{
		ProtocolVersion:       playback.ProtocolV3,
		Operation:             playback.ReplanOperationFailureRecoveryV3,
		PlaybackAttemptID:     startRequest.PlaybackAttemptID,
		ReplanRequestID:       "failed-replan-0001",
		FailedPlanID:          started.PlaybackPlan.PlanID,
		PlanAttemptID:         "plan-attempt-failed-0001",
		PlanAttemptKey:        currentKey,
		AttemptedPlanKeys:     []string{currentKey},
		AttemptCount:          1,
		PositionSeconds:       12,
		Failure:               playback.FailureV3{Classification: "decoder_failure"},
		Capabilities:          startRequest.Capabilities,
		ClientPlaybackContext: startRequest.ClientPlaybackContext,
	}
	failed := postPlaybackReplanV3(t, handler, started.SessionID, replan)
	if failed.Terminal == nil || failed.Terminal.Reason != "source_unavailable" {
		t.Fatalf("failed replan response = %#v", failed)
	}

	record, err := handler.PlanStoreV3.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if record.CurrentReplanRequestID != replan.ReplanRequestID {
		t.Fatalf("current replan request id = %q, want completed terminal %q", record.CurrentReplanRequestID, replan.ReplanRequestID)
	}
	if record.StartResponse.PlaybackPlan == nil || record.StartResponse.PlaybackPlan.PlanID != started.PlaybackPlan.PlanID || record.StartResponse.Terminal != nil {
		t.Fatalf("durable start response changed after failed replan: %#v", record.StartResponse)
	}
	replayedTerminal := postPlaybackReplanV3(t, handler, started.SessionID, replan)
	if replayedTerminal.Terminal == nil || replayedTerminal.Terminal.Reason != failed.Terminal.Reason {
		t.Fatalf("terminal replan replay = %#v, want %#v", replayedTerminal, failed)
	}

	replayRR := httptest.NewRecorder()
	handler.HandleStartPlayback(replayRR, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(startBody)).WithContext(newAuthorizedPlaybackContext()))
	var replayed playback.DecisionResponseV3
	if replayRR.Code != http.StatusCreated || json.Unmarshal(replayRR.Body.Bytes(), &replayed) != nil || replayed.PlaybackPlan == nil {
		t.Fatalf("start replay status=%d body=%s", replayRR.Code, replayRR.Body.String())
	}
	if replayed.PlaybackPlan.PlanID != started.PlaybackPlan.PlanID || replayed.Terminal != nil {
		t.Fatalf("start replay = %#v, want original plan %q", replayed, started.PlaybackPlan.PlanID)
	}
}

func TestHandleReplanPlaybackV3BoundsDeferredLeaseRelease(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	startRequest := v3HandlerStartRequest()
	startRR := httptest.NewRecorder()
	handler.HandleStartPlayback(startRR, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, startRequest))).WithContext(newAuthorizedPlaybackContext()))
	var started playback.DecisionResponseV3
	if startRR.Code != http.StatusCreated || json.Unmarshal(startRR.Body.Bytes(), &started) != nil || started.PlaybackPlan == nil {
		t.Fatalf("start status=%d body=%s", startRR.Code, startRR.Body.String())
	}

	recordingStore := &releaseDeadlinePlanStoreV3{PlanStoreV3: handler.PlanStoreV3}
	handler.PlanStoreV3 = recordingStore
	failedKey := playback.PlanAttemptKeyV3(*started.PlaybackPlan, startRequest.ClientPlaybackContext.Output.OutputContextID, nil)
	replan := playback.ReplanRequestV3{
		ProtocolVersion:       playback.ProtocolV3,
		PlaybackAttemptID:     startRequest.PlaybackAttemptID,
		ReplanRequestID:       "replan-release-deadline-0001",
		FailedPlanID:          "stale-plan-id",
		PlanAttemptID:         "plan-attempt-release-deadline-0001",
		PlanAttemptKey:        failedKey,
		AttemptedPlanKeys:     []string{failedKey},
		AttemptCount:          1,
		QualityPreference:     "original",
		Failure:               playback.FailureV3{Classification: "decoder_failure"},
		Capabilities:          startRequest.Capabilities,
		ClientPlaybackContext: startRequest.ClientPlaybackContext,
	}
	body, err := json.Marshal(replan)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/"+started.SessionID+"/replan", strings.NewReader(string(body))).WithContext(newAuthorizedPlaybackContext())
	req = withPlaybackRouteParam(req, "session_id", started.SessionID)
	rr := httptest.NewRecorder()
	handler.HandleReplanPlaybackV3(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("replan status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !recordingStore.called || !recordingStore.hasDeadline || recordingStore.contextErr != nil {
		t.Fatalf("release context: called=%v deadline=%v err=%v", recordingStore.called, recordingStore.hasDeadline, recordingStore.contextErr)
	}
	if recordingStore.timeToExpiry < 2500*time.Millisecond || recordingStore.timeToExpiry > replanReleaseTimeoutV3 {
		t.Fatalf("release deadline remaining = %v, want approximately %v", recordingStore.timeToExpiry, replanReleaseTimeoutV3)
	}
}

func TestHandleReplanPlaybackV3RollsBackLiveSessionWhenPersistenceFails(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	file.AudioTracks = append(file.AudioTracks, models.AudioTrack{Codec: "aac", Channels: 2, Layout: "stereo", Language: "spa"})
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "true"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	startRequest := v3HandlerStartRequest()
	startReq := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, startRequest))).WithContext(newAuthorizedPlaybackContext())
	startRR := httptest.NewRecorder()
	handler.HandleStartPlayback(startRR, startReq)
	if startRR.Code != http.StatusCreated {
		t.Fatalf("start status = %d, body = %s", startRR.Code, startRR.Body.String())
	}
	var started playback.DecisionResponseV3
	if err := json.Unmarshal(startRR.Body.Bytes(), &started); err != nil || started.PlaybackPlan == nil {
		t.Fatalf("start response: err=%v response=%#v", err, started)
	}
	beforeSession, err := manager.GetSession(started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	beforeRecord, err := handler.PlanStoreV3.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	underlyingStore := handler.PlanStoreV3
	handler.PlanStoreV3 = &failingCompletePlanStoreV3{PlanStoreV3: underlyingStore}

	audioIndex := 1
	failedKey := playback.PlanAttemptKeyV3(*started.PlaybackPlan, startRequest.ClientPlaybackContext.Output.OutputContextID, nil)
	replan := playback.ReplanRequestV3{
		ProtocolVersion:   playback.ProtocolV3,
		PlaybackAttemptID: startRequest.PlaybackAttemptID,
		ReplanRequestID:   "replan-rollback-0001",
		FailedPlanID:      started.PlaybackPlan.PlanID,
		PlanAttemptID:     "plan-attempt-rollback-0001",
		PlanAttemptKey:    failedKey,
		AttemptedPlanKeys: []string{failedKey},
		AttemptCount:      1,
		QualityPreference: "original",
		PositionSeconds:   12,
		SelectedTracks: playback.SelectedTracksV3{Audio: &playback.TrackIdentityV3{
			ID: playback.TrackIDV3(file.ID, "audio", audioIndex), Index: &audioIndex,
		}},
		Failure:               playback.FailureV3{Classification: "audio_renderer_error"},
		Capabilities:          startRequest.Capabilities,
		ClientPlaybackContext: startRequest.ClientPlaybackContext,
	}
	body, err := json.Marshal(replan)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/"+started.SessionID+"/replan", strings.NewReader(string(body))).WithContext(newAuthorizedPlaybackContext())
	req = withPlaybackRouteParam(req, "session_id", started.SessionID)
	rr := httptest.NewRecorder()
	handler.HandleReplanPlaybackV3(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("replan status = %d, body = %s", rr.Code, rr.Body.String())
	}

	afterSession, err := manager.GetSession(started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if afterSession.MediaFileID != beforeSession.MediaFileID || afterSession.AudioTrackIndex != beforeSession.AudioTrackIndex ||
		afterSession.PlayMethod != beforeSession.PlayMethod || afterSession.TranscodeNodeURL != beforeSession.TranscodeNodeURL {
		t.Fatalf("live session was not rolled back: before=%#v after=%#v", beforeSession, afterSession)
	}
	afterRecord, err := underlyingStore.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if afterRecord.CurrentPlanID != beforeRecord.CurrentPlanID || afterRecord.CurrentReplanRequestID != beforeRecord.CurrentReplanRequestID {
		t.Fatalf("durable attempt changed after failed commit: before=%#v after=%#v", beforeRecord, afterRecord)
	}
}

func TestHandleReplanPlaybackV3SeekReanchorKeepsCurrentRecipeEligible(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	file.SubtitleTracks = []models.SubtitleTrack{{Index: 0, Codec: "ass", Language: "eng"}}
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "false"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	startRequest := v3HandlerStartRequest()
	subtitleIndex := 0
	startRequest.SubtitleTrackID = playback.TrackIDV3(file.ID, "subtitle", subtitleIndex)
	startRequest.SubtitleTrackIndex = &subtitleIndex
	startReq := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, startRequest))).WithContext(newAuthorizedPlaybackContext())
	startRR := httptest.NewRecorder()
	handler.HandleStartPlayback(startRR, startReq)
	if startRR.Code != http.StatusCreated {
		t.Fatalf("start status = %d, body = %s", startRR.Code, startRR.Body.String())
	}
	var started playback.DecisionResponseV3
	if err := json.Unmarshal(startRR.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if started.PlaybackPlan == nil {
		t.Fatal("start returned no plan")
	}
	if started.PlaybackPlan.Subtitle.Artifact == nil || started.PlaybackPlan.Subtitle.Artifact.Format != "ass" {
		t.Fatalf("start returned no ASS artifact: %#v", started.PlaybackPlan.Subtitle)
	}
	if err := manager.UpdateProgress(started.SessionID, 12, true); err != nil {
		t.Fatal(err)
	}
	currentKey := playback.PlanAttemptKeyV3(*started.PlaybackPlan, startRequest.ClientPlaybackContext.Output.OutputContextID, nil)
	reanchor := playback.ReplanRequestV3{
		ProtocolVersion: playback.ProtocolV3, Operation: playback.ReplanOperationSeekReanchorV3,
		PlaybackAttemptID: startRequest.PlaybackAttemptID,
		ReplanRequestID:   "seek-reanchor-0001", FailedPlanID: started.PlaybackPlan.PlanID,
		PlanAttemptID: "plan-attempt-seek-0001", PlanAttemptKey: currentKey,
		// Clients may include the current key defensively. A seek reanchor must
		// ignore it because the recipe did not fail.
		AttemptedPlanKeys: []string{currentKey}, AttemptCount: 1,
		QualityPreference: "original", PositionSeconds: 321,
		SelectedTracks: started.PlaybackPlan.SelectedTracks,
		Failure:        playback.FailureV3{},
		Capabilities:   startRequest.Capabilities, ClientPlaybackContext: startRequest.ClientPlaybackContext,
	}
	body, err := json.Marshal(reanchor)
	if err != nil {
		t.Fatal(err)
	}
	call := func() playback.DecisionResponseV3 {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/"+started.SessionID+"/replan", strings.NewReader(string(body))).WithContext(newAuthorizedPlaybackContext())
		req = withPlaybackRouteParam(req, "session_id", started.SessionID)
		rr := httptest.NewRecorder()
		handler.HandleReplanPlaybackV3(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("reanchor status = %d, body = %s", rr.Code, rr.Body.String())
		}
		var response playback.DecisionResponseV3
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	first := call()
	second := call()
	for index, response := range []playback.DecisionResponseV3{first, second} {
		if response.PlaybackPlan == nil || response.SessionID != started.SessionID ||
			response.PlaybackPlan.SessionID != started.SessionID ||
			response.PlaybackPlan.PlanID != started.PlaybackPlan.PlanID ||
			response.PlaybackPlan.RequestedMediaFileID != started.PlaybackPlan.RequestedMediaFileID ||
			response.PlaybackPlan.EffectiveMediaFileID != started.PlaybackPlan.EffectiveMediaFileID ||
			!sameSelectedTracksV3(response.PlaybackPlan.SelectedTracks, started.PlaybackPlan.SelectedTracks) ||
			response.PlaybackPlan.Timeline.SourceStartSeconds != 321 ||
			playback.PlanAttemptKeyV3(*response.PlaybackPlan, startRequest.ClientPlaybackContext.Output.OutputContextID, nil) != currentKey {
			t.Fatalf("reanchored response %d = %#v", index, response.PlaybackPlan)
		}
		if response.PlaybackPlan.Subtitle.Artifact == nil ||
			response.PlaybackPlan.Subtitle.Artifact.Format != started.PlaybackPlan.Subtitle.Artifact.Format ||
			response.PlaybackPlan.Subtitle.Artifact.MIMEType != started.PlaybackPlan.Subtitle.Artifact.MIMEType {
			t.Fatalf("reanchored response %d changed the ASS artifact: %#v", index, response.PlaybackPlan.Subtitle)
		}
		if !playback.HasFeatureV3(response.ServerFeatures, playback.FeatureSeekReanchorV3) {
			t.Fatalf("reanchored response %d omitted %q: %#v", index, playback.FeatureSeekReanchorV3, response.ServerFeatures)
		}
	}
	liveSessionManager := handler.sessionMgr
	handler.sessionMgr = playback.NewSessionManager(0, 0)
	restartReplayReq := httptest.NewRequest(http.MethodPost, "/api/v1/playback/"+started.SessionID+"/replan", strings.NewReader(string(body))).WithContext(newAuthorizedPlaybackContext())
	restartReplayReq = withPlaybackRouteParam(restartReplayReq, "session_id", started.SessionID)
	restartReplayRR := httptest.NewRecorder()
	handler.HandleReplanPlaybackV3(restartReplayRR, restartReplayReq)
	handler.sessionMgr = liveSessionManager
	if restartReplayRR.Code != http.StatusNotFound || !strings.Contains(restartReplayRR.Body.String(), playbackSessionNotFoundErrorCode) {
		t.Fatalf("restart replay status = %d, body = %s", restartReplayRR.Code, restartReplayRR.Body.String())
	}
	session, err := manager.GetSession(started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if session.Position != 321 || !session.IsPaused {
		t.Fatalf("session progress = (%v, paused=%v), want (321, paused=true)", session.Position, session.IsPaused)
	}
	record, err := handler.PlanStoreV3.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if record.CurrentPlanID != started.PlaybackPlan.PlanID || record.EffectiveMediaFileID != file.ID ||
		record.NormalizedRequest.StartPosition == nil || *record.NormalizedRequest.StartPosition != 321 {
		t.Fatalf("stored reanchor = %#v", record)
	}

	mismatch := reanchor
	mismatch.ReplanRequestID = "seek-reanchor-mismatch-0001"
	mismatch.QualityPreference = "480p"
	mismatchBody, err := json.Marshal(mismatch)
	if err != nil {
		t.Fatal(err)
	}
	mismatchReq := httptest.NewRequest(http.MethodPost, "/api/v1/playback/"+started.SessionID+"/replan", strings.NewReader(string(mismatchBody))).WithContext(newAuthorizedPlaybackContext())
	mismatchReq = withPlaybackRouteParam(mismatchReq, "session_id", started.SessionID)
	mismatchRR := httptest.NewRecorder()
	handler.HandleReplanPlaybackV3(mismatchRR, mismatchReq)
	if mismatchRR.Code != http.StatusOK {
		t.Fatalf("mismatch status = %d, body = %s", mismatchRR.Code, mismatchRR.Body.String())
	}
	var mismatchResponse playback.DecisionResponseV3
	if err := json.Unmarshal(mismatchRR.Body.Bytes(), &mismatchResponse); err != nil {
		t.Fatal(err)
	}
	if mismatchResponse.Terminal == nil || mismatchResponse.Terminal.Reason != "seek_reanchor_intent_mismatch" {
		t.Fatalf("mismatch response = %#v", mismatchResponse)
	}
	recordAfterMismatch, err := handler.PlanStoreV3.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if recordAfterMismatch.CurrentPlanID != record.CurrentPlanID || recordAfterMismatch.NormalizedRequest.StartPosition == nil || *recordAfterMismatch.NormalizedRequest.StartPosition != 321 {
		t.Fatalf("intent mismatch changed stored route: before=%#v after=%#v", record, recordAfterMismatch)
	}

	newer := reanchor
	newer.ReplanRequestID = "seek-reanchor-0002"
	newer.PositionSeconds = 500
	newerBody, err := json.Marshal(newer)
	if err != nil {
		t.Fatal(err)
	}
	newerReq := httptest.NewRequest(http.MethodPost, "/api/v1/playback/"+started.SessionID+"/replan", strings.NewReader(string(newerBody))).WithContext(newAuthorizedPlaybackContext())
	newerReq = withPlaybackRouteParam(newerReq, "session_id", started.SessionID)
	newerRR := httptest.NewRecorder()
	handler.HandleReplanPlaybackV3(newerRR, newerReq)
	if newerRR.Code != http.StatusOK {
		t.Fatalf("newer reanchor status = %d, body = %s", newerRR.Code, newerRR.Body.String())
	}

	staleRetryReq := httptest.NewRequest(http.MethodPost, "/api/v1/playback/"+started.SessionID+"/replan", strings.NewReader(string(body))).WithContext(newAuthorizedPlaybackContext())
	staleRetryReq = withPlaybackRouteParam(staleRetryReq, "session_id", started.SessionID)
	staleRetryRR := httptest.NewRecorder()
	handler.HandleReplanPlaybackV3(staleRetryRR, staleRetryReq)
	if staleRetryRR.Code != http.StatusConflict || !strings.Contains(staleRetryRR.Body.String(), "stale_playback_plan") {
		t.Fatalf("stale reanchor replay status = %d, body = %s", staleRetryRR.Code, staleRetryRR.Body.String())
	}
	latestRecord, err := handler.PlanStoreV3.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if latestRecord.CurrentReplanRequestID != newer.ReplanRequestID || latestRecord.NormalizedRequest.StartPosition == nil || *latestRecord.NormalizedRequest.StartPosition != newer.PositionSeconds {
		t.Fatalf("stale replay changed latest reanchor: %#v", latestRecord)
	}
	// Simulate a rolling-deploy writer which updated the current plan but did
	// not know about CurrentReplanRequestID. The durable plan comparison must
	// still reject A's cached response after B became active.
	mixedWriterRecord := *latestRecord
	mixedWriterRecord.CurrentReplanRequestID = reanchor.ReplanRequestID
	handler.PlanStoreV3.(*playback.MemoryPlanStoreV3).ReplaceAttempt(context.Background(), mixedWriterRecord)
	mixedWriterRetryReq := httptest.NewRequest(http.MethodPost, "/api/v1/playback/"+started.SessionID+"/replan", strings.NewReader(string(body))).WithContext(newAuthorizedPlaybackContext())
	mixedWriterRetryReq = withPlaybackRouteParam(mixedWriterRetryReq, "session_id", started.SessionID)
	mixedWriterRetryRR := httptest.NewRecorder()
	handler.HandleReplanPlaybackV3(mixedWriterRetryRR, mixedWriterRetryReq)
	if mixedWriterRetryRR.Code != http.StatusConflict || !strings.Contains(mixedWriterRetryRR.Body.String(), "stale_playback_plan") {
		t.Fatalf("mixed-writer stale replay status = %d, body = %s", mixedWriterRetryRR.Code, mixedWriterRetryRR.Body.String())
	}

	beyondEnd := newer
	beyondEnd.ReplanRequestID = "seek-reanchor-beyond-end-0001"
	beyondEnd.PositionSeconds = float64(file.Duration) + 1
	beyondBody, err := json.Marshal(beyondEnd)
	if err != nil {
		t.Fatal(err)
	}
	beyondReq := httptest.NewRequest(http.MethodPost, "/api/v1/playback/"+started.SessionID+"/replan", strings.NewReader(string(beyondBody))).WithContext(newAuthorizedPlaybackContext())
	beyondReq = withPlaybackRouteParam(beyondReq, "session_id", started.SessionID)
	beyondRR := httptest.NewRecorder()
	handler.HandleReplanPlaybackV3(beyondRR, beyondReq)
	if beyondRR.Code != http.StatusOK || !strings.Contains(beyondRR.Body.String(), "invalid_seek_position") {
		t.Fatalf("beyond-end reanchor status = %d, body = %s", beyondRR.Code, beyondRR.Body.String())
	}

	missingSince := time.Now()
	file.MissingSince = &missingSince
	missing := newer
	missing.ReplanRequestID = "seek-reanchor-missing-source-0001"
	missing.PositionSeconds = 600
	missingBody, err := json.Marshal(missing)
	if err != nil {
		t.Fatal(err)
	}
	missingReq := httptest.NewRequest(http.MethodPost, "/api/v1/playback/"+started.SessionID+"/replan", strings.NewReader(string(missingBody))).WithContext(newAuthorizedPlaybackContext())
	missingReq = withPlaybackRouteParam(missingReq, "session_id", started.SessionID)
	missingRR := httptest.NewRecorder()
	handler.HandleReplanPlaybackV3(missingRR, missingReq)
	if missingRR.Code != http.StatusOK || !strings.Contains(missingRR.Body.String(), "source_unavailable") {
		t.Fatalf("missing-source reanchor status = %d, body = %s", missingRR.Code, missingRR.Body.String())
	}
	unchanged, err := handler.PlanStoreV3.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.CurrentPlanID != latestRecord.CurrentPlanID || unchanged.EffectiveMediaFileID != latestRecord.EffectiveMediaFileID ||
		unchanged.NormalizedRequest.StartPosition == nil || *unchanged.NormalizedRequest.StartPosition != newer.PositionSeconds {
		t.Fatalf("rejected seeks changed the active route: before=%#v after=%#v", latestRecord, unchanged)
	}
}

func TestHandleReplanPlaybackV3SeekReanchorIgnoresRefreshedProbeMetadata(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	file.AudioTracks[0].Default = false
	file.AudioTracks = append(file.AudioTracks, models.AudioTrack{Codec: "aac", Channels: 2, Layout: "stereo", Default: true})
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	startRequest := v3HandlerStartRequest()
	selectedAudio := 1
	startRequest.AudioTrackIndex = &selectedAudio
	startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassProgressiveV3] = playback.DeliveryCapabilityV3{Enabled: true, SupportedOnDevice: true, Subtitles: playback.DeliverySubtitleCapabilitiesV3{EmbeddedText: true, SidecarText: true}}

	startReq := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, startRequest))).WithContext(newAuthorizedPlaybackContext())
	startRR := httptest.NewRecorder()
	handler.HandleStartPlayback(startRR, startReq)
	if startRR.Code != http.StatusCreated {
		t.Fatalf("start status = %d, body = %s", startRR.Code, startRR.Body.String())
	}
	var started playback.DecisionResponseV3
	if err := json.Unmarshal(startRR.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if started.PlaybackPlan == nil || started.PlaybackPlan.Delivery != playback.DeliveryOriginalHTTPV3 {
		t.Fatalf("start plan = %#v", started.PlaybackPlan)
	}

	// Simulate a refreshed probe changing a live planner input after the route
	// was durably accepted. A seek is not authority to consume that drift.
	file.Container = "mkv"
	file.AudioTracks = file.AudioTracks[:1]
	currentKey := playback.PlanAttemptKeyV3(*started.PlaybackPlan, startRequest.ClientPlaybackContext.Output.OutputContextID, nil)
	reanchor := playback.ReplanRequestV3{
		ProtocolVersion: playback.ProtocolV3, Operation: playback.ReplanOperationSeekReanchorV3,
		PlaybackAttemptID: startRequest.PlaybackAttemptID,
		ReplanRequestID:   "seek-reanchor-probe-drift-0001", FailedPlanID: started.PlaybackPlan.PlanID,
		PlanAttemptID: "plan-attempt-seek-probe-0001", PlanAttemptKey: currentKey,
		AttemptCount: 1, QualityPreference: startRequest.QualityPreference, PositionSeconds: 321,
		SelectedTracks:        started.PlaybackPlan.SelectedTracks,
		Capabilities:          startRequest.Capabilities,
		ClientPlaybackContext: startRequest.ClientPlaybackContext,
	}
	body, err := json.Marshal(reanchor)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/"+started.SessionID+"/replan", strings.NewReader(string(body))).WithContext(newAuthorizedPlaybackContext())
	req = withPlaybackRouteParam(req, "session_id", started.SessionID)
	rr := httptest.NewRecorder()
	handler.HandleReplanPlaybackV3(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("reanchor status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var response playback.DecisionResponseV3
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.PlaybackPlan == nil || response.PlaybackPlan.PlanID != started.PlaybackPlan.PlanID ||
		response.PlaybackPlan.Delivery != started.PlaybackPlan.Delivery ||
		response.PlaybackPlan.Timeline.SourceStartSeconds != 321 {
		t.Fatalf("reanchored plan = %#v, terminal = %#v", response.PlaybackPlan, response.Terminal)
	}
}

func TestStartPlaybackV3FreezesDownloadedSubtitleFromPlanningSnapshot(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	repo := newMockSubtitleRepoForHandler()
	first := subtitles.DownloadedSubtitle{ID: 71, MediaFileID: file.ID, Format: subtitles.FormatSRT}
	reordered := subtitles.DownloadedSubtitle{ID: 72, MediaFileID: file.ID, Format: subtitles.FormatSRT}
	repo.listResults = [][]subtitles.DownloadedSubtitle{{first}, {reordered, first}}
	repo.subtitles[first.ID] = &first
	repo.subtitles[reordered.ID] = &reordered

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.SubtitleRepo = repo
	request := v3HandlerStartRequest()
	downloadedIndex := 0
	request.SubtitleTrackIndex = &downloadedIndex
	request.SubtitleTrackID = playback.TrackIDV3(file.ID, "subtitle", downloadedIndex)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, request))).WithContext(newAuthorizedPlaybackContext())
	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("start status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var response playback.DecisionResponseV3
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.PlaybackPlan == nil || response.PlaybackPlan.Subtitle.Artifact == nil ||
		!strings.Contains(response.PlaybackPlan.Subtitle.Artifact.URL, "downloaded_subtitle_id=71") {
		t.Fatalf("playback plan = %#v, want downloaded subtitle 71", response.PlaybackPlan)
	}
	if repo.listCalls != 1 {
		t.Fatalf("downloaded inventory listed %d times, want one planning snapshot", repo.listCalls)
	}
	record, err := handler.PlanStoreV3.GetAttempt(context.Background(), response.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if record.FrozenRecipe.DownloadedSubtitleID != 71 {
		t.Fatalf("frozen downloaded subtitle = %d, want 71", record.FrozenRecipe.DownloadedSubtitleID)
	}
}

func TestHandleReplanPlaybackV3SeekReanchorPreservesFallbackRecipe(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	stubCopySeekAnchorV3(handler)
	handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpeg(t), t.TempDir())
	presetLocalRegistryV3(handler, playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{
		{Name: "audio_to_aac", RecipeVersion: "2", Available: true},
		{Name: "video_to_h264", RecipeVersion: "2", Available: true},
		{Name: "server_dv7_to_hdr10", RecipeVersion: "1", Available: true},
	}))
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	startRequest := v3HandlerStartRequest()
	startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassProgressiveV3] = playback.DeliveryCapabilityV3{Enabled: true, SupportedOnDevice: true}
	startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3] = playback.DeliveryCapabilityV3{Enabled: true, SupportedOnDevice: true}

	startReq := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, startRequest))).WithContext(newAuthorizedPlaybackContext())
	startRR := httptest.NewRecorder()
	handler.HandleStartPlayback(startRR, startReq)
	if startRR.Code != http.StatusCreated {
		t.Fatalf("start status = %d, body = %s", startRR.Code, startRR.Body.String())
	}
	var active playback.DecisionResponseV3
	if err := json.Unmarshal(startRR.Body.Bytes(), &active); err != nil {
		t.Fatal(err)
	}
	if active.PlaybackPlan == nil || active.PlaybackPlan.Delivery != playback.DeliveryOriginalHTTPV3 {
		t.Fatalf("initial plan = %#v", active.PlaybackPlan)
	}

	attempted := []string{}
	wantFallbacks := []playback.DeliveryV3{playback.DeliveryRemuxProgressiveV3, playback.DeliveryRemuxHLSV3}
	for index, classification := range []string{"playback_error", "decoder_error"} {
		currentKey := playback.PlanAttemptKeyV3(*active.PlaybackPlan, startRequest.ClientPlaybackContext.Output.OutputContextID, nil)
		attempted = append(attempted, currentKey)
		response := postPlaybackReplanV3(t, handler, active.SessionID, playback.ReplanRequestV3{
			ProtocolVersion: playback.ProtocolV3, Operation: playback.ReplanOperationFailureRecoveryV3,
			PlaybackAttemptID: startRequest.PlaybackAttemptID,
			ReplanRequestID:   fmt.Sprintf("fallback-replan-%04d", index+1),
			FailedPlanID:      active.PlaybackPlan.PlanID,
			PlanAttemptID:     fmt.Sprintf("fallback-plan-attempt-%04d", index+1),
			PlanAttemptKey:    currentKey, AttemptedPlanKeys: append([]string(nil), attempted...),
			AttemptCount: index + 1, QualityPreference: startRequest.QualityPreference,
			SelectedTracks:        active.PlaybackPlan.SelectedTracks,
			Failure:               playback.FailureV3{Classification: classification},
			Capabilities:          startRequest.Capabilities,
			ClientPlaybackContext: startRequest.ClientPlaybackContext,
		})
		if response.PlaybackPlan == nil {
			t.Fatalf("fallback %d = %#v", index, response)
		}
		if response.PlaybackPlan.Delivery != wantFallbacks[index] {
			t.Fatalf("fallback %d delivery = %q, want %q; response=%#v", index, response.PlaybackPlan.Delivery, wantFallbacks[index], response)
		}
		active = response
	}
	if active.PlaybackPlan.Delivery != playback.DeliveryRemuxHLSV3 {
		t.Fatalf("fallback route = %#v", active.PlaybackPlan)
	}
	frozen := *active.PlaybackPlan
	currentKey := playback.PlanAttemptKeyV3(frozen, startRequest.ClientPlaybackContext.Output.OutputContextID, nil)
	reanchored := postPlaybackReplanV3(t, handler, active.SessionID, playback.ReplanRequestV3{
		ProtocolVersion: playback.ProtocolV3, Operation: playback.ReplanOperationSeekReanchorV3,
		PlaybackAttemptID: startRequest.PlaybackAttemptID,
		ReplanRequestID:   "fallback-seek-reanchor-0001", FailedPlanID: frozen.PlanID,
		PlanAttemptID: "fallback-seek-plan-0001", PlanAttemptKey: currentKey,
		AttemptCount: 3, QualityPreference: startRequest.QualityPreference, PositionSeconds: 819.185,
		SelectedTracks:        frozen.SelectedTracks,
		Capabilities:          startRequest.Capabilities,
		ClientPlaybackContext: startRequest.ClientPlaybackContext,
	})
	if reanchored.PlaybackPlan == nil || reanchored.PlaybackPlan.PlanID != frozen.PlanID ||
		reanchored.PlaybackPlan.Delivery != frozen.Delivery ||
		reanchored.PlaybackPlan.Timeline.SourceStartSeconds != 819.185 {
		t.Fatalf("reanchored fallback = %#v, terminal = %#v", reanchored.PlaybackPlan, reanchored.Terminal)
	}
}

// A pre-migration attempt row carries frozen_recipe = '{}', which decodes to
// an invalid zero recipe. A seek reanchor against it must fail with the
// dedicated retryable reason — telling the client to mint a fresh playback
// attempt — while leaving the durable attempt fully usable for subsequent
// failure replans.
func TestHandleReplanPlaybackV3SeekReanchorWithoutFrozenRecipeFailsRetryably(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	stubCopySeekAnchorV3(handler)
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	startRequest := v3HandlerStartRequest()
	startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassProgressiveV3] = playback.DeliveryCapabilityV3{Enabled: true, SupportedOnDevice: true, Subtitles: playback.DeliverySubtitleCapabilitiesV3{EmbeddedText: true, SidecarText: true}}

	startReq := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, startRequest))).WithContext(newAuthorizedPlaybackContext())
	startRR := httptest.NewRecorder()
	handler.HandleStartPlayback(startRR, startReq)
	if startRR.Code != http.StatusCreated {
		t.Fatalf("start status = %d, body = %s", startRR.Code, startRR.Body.String())
	}
	var started playback.DecisionResponseV3
	if err := json.Unmarshal(startRR.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if started.PlaybackPlan == nil {
		t.Fatalf("start response = %s", startRR.Body.String())
	}

	// Simulate the row predating the frozen_recipe migration: the JSONB
	// default '{}' unmarshals to the zero recipe.
	record, err := handler.PlanStoreV3.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	record.FrozenRecipe = playback.ExecutableRecipeV3{}
	handler.PlanStoreV3.(*playback.MemoryPlanStoreV3).ReplaceAttempt(context.Background(), *record)

	currentKey := playback.PlanAttemptKeyV3(*started.PlaybackPlan, startRequest.ClientPlaybackContext.Output.OutputContextID, nil)
	seekResponse := postPlaybackReplanV3(t, handler, started.SessionID, playback.ReplanRequestV3{
		ProtocolVersion: playback.ProtocolV3, Operation: playback.ReplanOperationSeekReanchorV3,
		PlaybackAttemptID: startRequest.PlaybackAttemptID,
		ReplanRequestID:   "legacy-seek-reanchor-0001", FailedPlanID: started.PlaybackPlan.PlanID,
		PlanAttemptID: "legacy-seek-plan-0001", PlanAttemptKey: currentKey,
		AttemptCount: 1, QualityPreference: startRequest.QualityPreference, PositionSeconds: 456,
		SelectedTracks:        started.PlaybackPlan.SelectedTracks,
		Capabilities:          startRequest.Capabilities,
		ClientPlaybackContext: startRequest.ClientPlaybackContext,
	})
	if seekResponse.Terminal == nil || seekResponse.Terminal.Reason != "seek_reanchor_recipe_unavailable" || !seekResponse.Terminal.Retryable {
		t.Fatalf("legacy seek reanchor = %#v, terminal = %#v", seekResponse.PlaybackPlan, seekResponse.Terminal)
	}

	// The failed seek must not have consumed the durable attempt: the current
	// plan is unchanged and an ordinary failure replan still succeeds.
	after, err := handler.PlanStoreV3.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if after.CurrentPlanID != started.PlaybackPlan.PlanID {
		t.Fatalf("failed legacy seek moved the current plan: %q -> %q", started.PlaybackPlan.PlanID, after.CurrentPlanID)
	}
	recovery := postPlaybackReplanV3(t, handler, started.SessionID, playback.ReplanRequestV3{
		ProtocolVersion: playback.ProtocolV3, Operation: playback.ReplanOperationFailureRecoveryV3,
		PlaybackAttemptID: startRequest.PlaybackAttemptID,
		ReplanRequestID:   "legacy-recovery-0001", FailedPlanID: started.PlaybackPlan.PlanID,
		PlanAttemptID: "legacy-recovery-plan-0001", PlanAttemptKey: currentKey,
		AttemptedPlanKeys: []string{currentKey},
		AttemptCount:      2, QualityPreference: startRequest.QualityPreference, PositionSeconds: 456,
		SelectedTracks:        started.PlaybackPlan.SelectedTracks,
		Failure:               playback.FailureV3{Classification: "playback_error"},
		Capabilities:          startRequest.Capabilities,
		ClientPlaybackContext: startRequest.ClientPlaybackContext,
	})
	if recovery.PlaybackPlan == nil {
		t.Fatalf("failure replan after legacy seek = %#v, terminal = %#v", recovery.PlaybackPlan, recovery.Terminal)
	}
}

func TestHandleReplanPlaybackV3SeekFailureRecoveryNeverChangesMediaVersion(t *testing.T) {
	// A seek is not a capability-authority boundary. Even if the replan body
	// carries narrower request-only evidence, recovery stays on the mounted
	// edition and plans from the durable start evidence.

	source := v3HandlerFixtureFile(t)
	source.Resolution = "2160p"
	source.Bitrate = 32_000
	source.VideoTracks = append([]models.VideoTrack(nil), source.VideoTracks...)
	source.VideoTracks[0].Level = 51
	source.VideoTracks[0].Width = 3840
	source.VideoTracks[0].Height = 2160
	source.VideoTracks[0].Bitrate = 32_000

	alternateValue := *source
	alternate := &alternateValue
	alternate.ID = 84
	alternate.Resolution = "1080p"
	alternate.Bitrate = 8_000
	alternate.VideoTracks = append([]models.VideoTrack(nil), source.VideoTracks...)
	alternate.VideoTracks[0].Level = 41
	alternate.VideoTracks[0].Width = 1920
	alternate.VideoTracks[0].Height = 1080
	alternate.VideoTracks[0].Bitrate = 8_000

	manager := playback.NewSessionManager(0, 0)
	files := map[int]*models.MediaFile{
		source.ID: source, alternate.ID: alternate,
	}
	handler := NewPlaybackHandler(manager, mapPlaybackFileResolver{files: files})
	stubCopySeekAnchorV3(handler)
	handler.FileVersionFetcher = testPlaybackFileVersionFetcher{byContent: map[string][]*models.MediaFile{
		source.ContentID: {source, alternate},
	}}
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "false"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpegSleep(t, "5"), t.TempDir())

	startRequest := v3HandlerStartRequest()
	startRequest.QualityPreference = "auto"
	startRequest.Capabilities.MaxResolution = "2160p"
	startRequest.Capabilities.VideoDecode[0].Levels = []int{51}
	startRequest.Capabilities.VideoDecode[0].MaxWidth = 3840
	startRequest.Capabilities.VideoDecode[0].MaxHeight = 2160
	startRequest.Capabilities.VideoDecode[0].MaxBitrateKbps = 50_000
	startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3] = playback.DeliveryCapabilityV3{Enabled: true, SupportedOnDevice: true}

	startReq := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, startRequest))).WithContext(newAuthorizedPlaybackContext())
	startRR := httptest.NewRecorder()
	handler.HandleStartPlayback(startRR, startReq)
	if startRR.Code != http.StatusCreated {
		t.Fatalf("start status = %d, body = %s", startRR.Code, startRR.Body.String())
	}
	var started playback.DecisionResponseV3
	if err := json.Unmarshal(startRR.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if started.PlaybackPlan == nil || started.PlaybackPlan.EffectiveMediaFileID != source.ID {
		t.Fatalf("initial 4K plan = %#v", started.PlaybackPlan)
	}
	if err := manager.UpdateProgress(started.SessionID, 15, true); err != nil {
		t.Fatal(err)
	}

	seekCapabilities := startRequest.Capabilities
	seekCapabilities.VideoDecode = append([]playback.VideoDecodeCapabilityV3(nil), startRequest.Capabilities.VideoDecode...)
	seekCapabilities.MaxResolution = "1080p"
	seekCapabilities.VideoDecode[0].MaxWidth = 1920
	seekCapabilities.VideoDecode[0].MaxHeight = 1080
	seekCapabilities.VideoDecode[0].MaxBitrateKbps = 20_000
	seekContext := startRequest.ClientPlaybackContext
	seekContext.Device.Model = "request-only-model"
	currentKey := playback.PlanAttemptKeyV3(*started.PlaybackPlan, startRequest.ClientPlaybackContext.Output.OutputContextID, nil)
	staleClientKey := "v3:0000000000000000"
	replan := playback.ReplanRequestV3{
		ProtocolVersion:       playback.ProtocolV3,
		Operation:             playback.ReplanOperationSeekFailureRecoveryV3,
		PlaybackAttemptID:     startRequest.PlaybackAttemptID,
		ReplanRequestID:       "seek-failure-recovery-0001",
		FailedPlanID:          started.PlaybackPlan.PlanID,
		PlanAttemptID:         "plan-attempt-seek-failure-0001",
		PlanAttemptKey:        staleClientKey,
		AttemptedPlanKeys:     nil,
		AttemptCount:          1,
		QualityPreference:     startRequest.QualityPreference,
		PositionSeconds:       417,
		SelectedTracks:        started.PlaybackPlan.SelectedTracks,
		Failure:               playback.FailureV3{Classification: "decoder_failure", Message: "reanchored route failed"},
		Capabilities:          seekCapabilities,
		ClientPlaybackContext: seekContext,
	}
	body, err := json.Marshal(replan)
	if err != nil {
		t.Fatal(err)
	}
	replanReq := httptest.NewRequest(http.MethodPost, "/api/v1/playback/"+started.SessionID+"/replan", strings.NewReader(string(body))).WithContext(newAuthorizedPlaybackContext())
	replanReq = withPlaybackRouteParam(replanReq, "session_id", started.SessionID)
	replanRR := httptest.NewRecorder()
	handler.HandleReplanPlaybackV3(replanRR, replanReq)
	if replanRR.Code != http.StatusOK {
		t.Fatalf("replan status = %d, body = %s", replanRR.Code, replanRR.Body.String())
	}
	var response playback.DecisionResponseV3
	if err := json.Unmarshal(replanRR.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.PlaybackPlan == nil || response.Terminal != nil ||
		response.PlaybackPlan.RequestedMediaFileID != source.ID || response.PlaybackPlan.EffectiveMediaFileID != source.ID {
		t.Fatalf("failed seek did not recover on the pinned media version: response=%#v terminal=%#v", response, response.Terminal)
	}
	if response.PlaybackPlan.Delivery == playback.DeliveryTranscodeHLSV3 ||
		response.PlaybackPlan.EffectiveRecipe.Width == nil || *response.PlaybackPlan.EffectiveRecipe.Width != 3840 ||
		response.PlaybackPlan.EffectiveRecipe.Height == nil || *response.PlaybackPlan.EffectiveRecipe.Height != 2160 {
		t.Fatalf("failed seek downgraded or video-transcoded the pinned 4K source: %#v", response.PlaybackPlan)
	}
	if playback.PlanAttemptKeyV3(*response.PlaybackPlan, startRequest.ClientPlaybackContext.Output.OutputContextID, nil) == currentKey {
		t.Fatalf("failed seek retried the failed route: %#v", response.PlaybackPlan)
	}
	record, err := handler.PlanStoreV3.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if record.EffectiveMediaFileID != source.ID || record.CurrentPlan.EffectiveMediaFileID != source.ID {
		t.Fatalf("failed seek changed the durable media version: %#v", record)
	}
	if record.NormalizedRequest.Capabilities.MaxResolution != startRequest.Capabilities.MaxResolution ||
		record.NormalizedRequest.ClientPlaybackContext.Device.Model != startRequest.ClientPlaybackContext.Device.Model {
		t.Fatalf("failed seek accepted request-only capability evidence: %#v", record.NormalizedRequest)
	}
	session, err := manager.GetSession(started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if session.Position != replan.PositionSeconds || !session.IsPaused {
		t.Fatalf("session progress = (%v, paused=%v), want (%v, paused=true)", session.Position, session.IsPaused, replan.PositionSeconds)
	}

	delete(files, source.ID)
	for index, operation := range []playback.ReplanOperationV3{
		playback.ReplanOperationSeekReanchorV3,
		playback.ReplanOperationSeekFailureRecoveryV3,
	} {
		missing := replan
		missing.Operation = operation
		missing.ReplanRequestID = fmt.Sprintf("seek-missing-current-%04d", index)
		missing.FailedPlanID = response.PlaybackPlan.PlanID
		missing.PlanAttemptKey = playback.PlanAttemptKeyV3(*response.PlaybackPlan, startRequest.ClientPlaybackContext.Output.OutputContextID, nil)
		missing.AttemptedPlanKeys = []string{missing.PlanAttemptKey}
		missing.SelectedTracks = response.PlaybackPlan.SelectedTracks
		missing.PositionSeconds++
		if operation == playback.ReplanOperationSeekReanchorV3 {
			missing.Failure = playback.FailureV3{}
		}
		missingBody, err := json.Marshal(missing)
		if err != nil {
			t.Fatal(err)
		}
		missingReq := httptest.NewRequest(http.MethodPost, "/api/v1/playback/"+started.SessionID+"/replan", strings.NewReader(string(missingBody))).WithContext(newAuthorizedPlaybackContext())
		missingReq = withPlaybackRouteParam(missingReq, "session_id", started.SessionID)
		missingRR := httptest.NewRecorder()
		handler.HandleReplanPlaybackV3(missingRR, missingReq)
		if missingRR.Code != http.StatusOK || !strings.Contains(missingRR.Body.String(), "source_unavailable") {
			t.Fatalf("missing current %s status = %d, body = %s", operation, missingRR.Code, missingRR.Body.String())
		}
	}
	finalRecord, err := handler.PlanStoreV3.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	finalSession, err := manager.GetSession(started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if finalRecord.EffectiveMediaFileID != source.ID || finalRecord.CurrentPlan.EffectiveMediaFileID != source.ID || finalSession.MediaFileID != source.ID {
		t.Fatalf("missing 4K source fell through to alternate: record=%#v session=%#v", finalRecord, finalSession)
	}
}

func TestHandleReplanPlaybackV3SeekUsesEffectiveEditionWhenRequestedEditionIsGone(t *testing.T) {
	effective := v3HandlerFixtureFile(t)
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, mapPlaybackFileResolver{files: map[int]*models.MediaFile{effective.ID: effective}})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}

	startRequest := v3HandlerStartRequest()
	startReq := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, startRequest))).WithContext(newAuthorizedPlaybackContext())
	startRR := httptest.NewRecorder()
	handler.HandleStartPlayback(startRR, startReq)
	if startRR.Code != http.StatusCreated {
		t.Fatalf("start status = %d, body = %s", startRR.Code, startRR.Body.String())
	}
	var started playback.DecisionResponseV3
	if err := json.Unmarshal(startRR.Body.Bytes(), &started); err != nil || started.PlaybackPlan == nil {
		t.Fatalf("start response: err=%v response=%#v", err, started)
	}

	const missingRequestedID = 84
	record, err := handler.PlanStoreV3.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	record.RequestedMediaFileID = missingRequestedID
	record.NormalizedRequest.FileID = missingRequestedID
	record.CurrentPlan.RequestedMediaFileID = missingRequestedID
	record.CurrentPlan.PlanID = playback.DeterministicPlanIDV3(
		record.PlaybackAttemptID,
		missingRequestedID,
		effective.ID,
		record.CurrentPlan,
	)
	record.CurrentPlanID = record.CurrentPlan.PlanID
	record.FrozenRecipe.PlanID = record.CurrentPlan.PlanID
	handler.PlanStoreV3.(*playback.MemoryPlanStoreV3).ReplaceAttempt(context.Background(), *record)

	currentKey := playback.PlanAttemptKeyV3(record.CurrentPlan, record.NormalizedRequest.ClientPlaybackContext.Output.OutputContextID, nil)
	reanchor := playback.ReplanRequestV3{
		ProtocolVersion:       playback.ProtocolV3,
		Operation:             playback.ReplanOperationSeekReanchorV3,
		PlaybackAttemptID:     record.PlaybackAttemptID,
		ReplanRequestID:       "seek-missing-requested-0001",
		FailedPlanID:          record.CurrentPlanID,
		PlanAttemptID:         "plan-attempt-missing-requested-0001",
		PlanAttemptKey:        currentKey,
		AttemptedPlanKeys:     []string{currentKey},
		AttemptCount:          1,
		QualityPreference:     record.NormalizedRequest.QualityPreference,
		PositionSeconds:       300,
		SelectedTracks:        record.CurrentPlan.SelectedTracks,
		Capabilities:          record.NormalizedRequest.Capabilities,
		ClientPlaybackContext: record.NormalizedRequest.ClientPlaybackContext,
	}
	body, err := json.Marshal(reanchor)
	if err != nil {
		t.Fatal(err)
	}
	reanchorReq := httptest.NewRequest(http.MethodPost, "/api/v1/playback/"+started.SessionID+"/replan", strings.NewReader(string(body))).WithContext(newAuthorizedPlaybackContext())
	reanchorReq = withPlaybackRouteParam(reanchorReq, "session_id", started.SessionID)
	reanchorRR := httptest.NewRecorder()
	handler.HandleReplanPlaybackV3(reanchorRR, reanchorReq)
	if reanchorRR.Code != http.StatusOK {
		t.Fatalf("reanchor status = %d, body = %s", reanchorRR.Code, reanchorRR.Body.String())
	}
	var response playback.DecisionResponseV3
	if err := json.Unmarshal(reanchorRR.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.PlaybackPlan == nil || response.PlaybackPlan.RequestedMediaFileID != missingRequestedID ||
		response.PlaybackPlan.EffectiveMediaFileID != effective.ID || response.PlaybackPlan.Timeline.SourceStartSeconds != reanchor.PositionSeconds {
		t.Fatalf("reanchor did not retain the effective edition: %#v", response)
	}

	current, err := handler.PlanStoreV3.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	outputContext := current.NormalizedRequest.ClientPlaybackContext
	currentKey = playback.PlanAttemptKeyV3(current.CurrentPlan, current.NormalizedRequest.ClientPlaybackContext.Output.OutputContextID, nil)
	ordinary := playback.ReplanRequestV3{
		ProtocolVersion:       playback.ProtocolV3,
		Operation:             playback.ReplanOperationOutputChangeV3,
		PlaybackAttemptID:     current.PlaybackAttemptID,
		ReplanRequestID:       "ordinary-missing-requested-0001",
		FailedPlanID:          current.CurrentPlanID,
		PlanAttemptID:         "plan-attempt-ordinary-missing-0001",
		PlanAttemptKey:        currentKey,
		AttemptedPlanKeys:     []string{currentKey},
		AttemptCount:          1,
		QualityPreference:     current.NormalizedRequest.QualityPreference,
		PositionSeconds:       320,
		SelectedTracks:        current.CurrentPlan.SelectedTracks,
		Failure:               playback.FailureV3{},
		Capabilities:          current.NormalizedRequest.Capabilities,
		ClientPlaybackContext: outputContext,
	}
	ordinaryBody, err := json.Marshal(ordinary)
	if err != nil {
		t.Fatal(err)
	}
	ordinaryReq := httptest.NewRequest(http.MethodPost, "/api/v1/playback/"+started.SessionID+"/replan", strings.NewReader(string(ordinaryBody))).WithContext(newAuthorizedPlaybackContext())
	ordinaryReq = withPlaybackRouteParam(ordinaryReq, "session_id", started.SessionID)
	ordinaryRR := httptest.NewRecorder()
	handler.HandleReplanPlaybackV3(ordinaryRR, ordinaryReq)
	if ordinaryRR.Code != http.StatusOK {
		t.Fatalf("ordinary replan status = %d, body = %s", ordinaryRR.Code, ordinaryRR.Body.String())
	}
	var ordinaryResponse playback.DecisionResponseV3
	if err := json.Unmarshal(ordinaryRR.Body.Bytes(), &ordinaryResponse); err != nil {
		t.Fatal(err)
	}
	if ordinaryResponse.PlaybackPlan == nil || ordinaryResponse.PlaybackPlan.RequestedMediaFileID != missingRequestedID ||
		ordinaryResponse.PlaybackPlan.EffectiveMediaFileID != effective.ID {
		t.Fatalf("ordinary replan lost the requested/effective split: %#v", ordinaryResponse)
	}
}

func TestValidateSeekRecoveryRequestV3PinsCurrentIntent(t *testing.T) {
	audioIndex := 0
	start := v3HandlerStartRequest()
	start.AudioTrackID = playback.TrackIDV3(start.FileID, "audio", audioIndex)
	start.AudioTrackIndex = &audioIndex
	selected := playback.SelectedTracksV3{Audio: &playback.TrackIdentityV3{ID: start.AudioTrackID, Index: &audioIndex}}
	plan := playback.PlanV3{
		PlanID:               "plan:seek-current",
		RequestedMediaFileID: start.FileID,
		EffectiveMediaFileID: start.FileID,
		SelectedTracks:       selected,
	}
	record := &playback.AttemptRecordV3{
		RequestedMediaFileID: start.FileID,
		EffectiveMediaFileID: start.FileID,
		CurrentPlanID:        plan.PlanID,
		CurrentPlan:          plan,
		NormalizedRequest:    start,
	}
	request := playback.ReplanRequestV3{
		Operation:             playback.ReplanOperationSeekReanchorV3,
		QualityPreference:     start.QualityPreference,
		ClientPlaybackContext: start.ClientPlaybackContext,
		SelectedTracks:        selected,
	}
	if err := validateSeekRecoveryRequestV3(record, request); err != nil {
		t.Fatalf("valid seek reanchor guard: %v", err)
	}

	// Capabilities and client context are structurally validated by the request
	// decoder but are not route inputs for a same-recipe reanchor.
	request.Capabilities.CodecsVideo = []string{"av1"}
	request.ClientPlaybackContext.Device.Model = "changed-client-claim"
	if err := validateSeekRecoveryRequestV3(record, request); err != nil {
		t.Fatalf("ignored client evidence changed stored intent: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*playback.ReplanRequestV3)
	}{
		{name: "quality", mutate: func(value *playback.ReplanRequestV3) { value.QualityPreference = "480p" }},
		{name: "output route", mutate: func(value *playback.ReplanRequestV3) {
			value.ClientPlaybackContext.Output.OutputContextID = "route-changed"
		}},
		{name: "audio track", mutate: func(value *playback.ReplanRequestV3) {
			index := 1
			value.SelectedTracks.Audio = &playback.TrackIdentityV3{ID: playback.TrackIDV3(start.FileID, "audio", index), Index: &index}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := request
			test.mutate(&candidate)
			if err := validateSeekRecoveryRequestV3(record, candidate); err == nil {
				t.Fatalf("%s change was accepted", test.name)
			}
		})
	}
}

func TestValidateSeekReanchorPlanV3RejectsRouteDrift(t *testing.T) {
	audioIndex := 0
	frameRate := 23.976
	audioChannels := 2
	current := playback.PlanV3{
		PlanID:          "plan:seek-current",
		Delivery:        playback.DeliveryOriginalHTTPV3,
		Stream:          playback.StreamV3{Protocol: playback.StreamHTTPProgressiveV3, Container: "mp4", MIMEType: "video/mp4", HeaderRefresh: playback.HeaderRefreshNoneV3},
		SelectedTracks:  playback.SelectedTracksV3{Audio: &playback.TrackIdentityV3{ID: playback.TrackIDV3(42, "audio", audioIndex), Index: &audioIndex}},
		EffectiveRecipe: playback.EffectiveRecipeV3{VideoCodec: "h264", AudioCodec: "aac", FrameRate: &frameRate, AudioChannels: &audioChannels, AudioLayout: "stereo"},
		Claims:          playback.ValidationClaimsV3{Video: playback.VideoClaimsV3{HDR10: true}},
		Subtitle: playback.SubtitleDecisionV3{Mode: playback.SubtitleRenderV3, TrackID: playback.TrackIDV3(42, "subtitle", 0), Artifact: &playback.SubtitleArtifactV3{
			MIMEType: "text/x-ssa", Format: "ass",
		}},
		Transformations:        []playback.TransformationV3{{Name: "subtitle-convert", Executor: "ffmpeg", RecipeVersion: "1", ValidatedClaims: []string{"ass"}}},
		AppliedQuirks:          []playback.AppliedQuirkV3{{ID: "quirk-1", RegistryRevision: "1", Action: "force-remux"}},
		RuntimeCorrections:     []string{"pcm_fallback"},
		RequestedMediaFileID:   42,
		EffectiveMediaFileID:   42,
		SubtitleFidelityPolicy: "preserve_styling",
	}
	record := &playback.AttemptRecordV3{
		RequestedMediaFileID: 42,
		EffectiveMediaFileID: 42,
		CurrentPlanID:        current.PlanID,
		CurrentPlan:          current,
		NormalizedRequest:    playback.StartRequestV3{ClientPlaybackContext: playback.ClientPlaybackContextV3{Output: playback.OutputContextV3{OutputContextID: "9"}}},
	}
	candidate := current
	candidate.Timeline.SourceStartSeconds = 321
	if err := validateSeekReanchorPlanV3(record, &candidate); err != nil {
		t.Fatalf("timeline-only change rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*playback.PlanV3)
	}{
		{name: "delivery recipe", mutate: func(value *playback.PlanV3) { value.Delivery = playback.DeliveryRemuxProgressiveV3 }},
		{name: "stream MIME", mutate: func(value *playback.PlanV3) { value.Stream.MIMEType = "application/x-mpegURL" }},
		{name: "header refresh", mutate: func(value *playback.PlanV3) { value.Stream.HeaderRefresh = playback.HeaderRefreshSessionV3 }},
		{name: "frame rate", mutate: func(value *playback.PlanV3) {
			changed := 24.0
			value.EffectiveRecipe.FrameRate = &changed
		}},
		{name: "audio channels", mutate: func(value *playback.PlanV3) {
			changed := 6
			value.EffectiveRecipe.AudioChannels = &changed
		}},
		{name: "audio layout", mutate: func(value *playback.PlanV3) { value.EffectiveRecipe.AudioLayout = "5.1" }},
		{name: "claims", mutate: func(value *playback.PlanV3) { value.Claims.Video.HDR10 = false }},
		{name: "subtitle artifact MIME", mutate: func(value *playback.PlanV3) {
			copy := *value.Subtitle.Artifact
			copy.MIMEType = "text/vtt"
			value.Subtitle.Artifact = &copy
		}},
		{name: "subtitle artifact format", mutate: func(value *playback.PlanV3) {
			copy := *value.Subtitle.Artifact
			copy.Format = "vtt"
			value.Subtitle.Artifact = &copy
		}},
		{name: "subtitle fidelity", mutate: func(value *playback.PlanV3) { value.SubtitleFidelityPolicy = "compatibility" }},
		{name: "transformation claims", mutate: func(value *playback.PlanV3) {
			value.Transformations = append([]playback.TransformationV3(nil), value.Transformations...)
			value.Transformations[0].ValidatedClaims = []string{"vtt"}
		}},
		{name: "quirk action", mutate: func(value *playback.PlanV3) {
			value.AppliedQuirks = append([]playback.AppliedQuirkV3(nil), value.AppliedQuirks...)
			value.AppliedQuirks[0].Action = "disable-passthrough"
		}},
		{name: "effective version", mutate: func(value *playback.PlanV3) { value.EffectiveMediaFileID = 84 }},
		{name: "track", mutate: func(value *playback.PlanV3) {
			index := 1
			value.SelectedTracks.Audio = &playback.TrackIdentityV3{ID: playback.TrackIDV3(42, "audio", index), Index: &index}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			drifted := candidate
			test.mutate(&drifted)
			if err := validateSeekReanchorPlanV3(record, &drifted); err == nil {
				t.Fatalf("%s drift was accepted", test.name)
			}
		})
	}
}

func TestSeekReanchorIdentityChangesV3ReportsOnlyChangedFieldNames(t *testing.T) {
	current := playback.PlanV3{
		PlanID: "plan:current", Delivery: playback.DeliveryOriginalHTTPV3,
		Stream:               playback.StreamV3{Protocol: playback.StreamHTTPProgressiveV3, Container: "mp4"},
		EffectiveRecipe:      playback.EffectiveRecipeV3{VideoCodec: "h264", AudioCodec: "aac"},
		RequestedMediaFileID: 42, EffectiveMediaFileID: 42,
	}
	record := &playback.AttemptRecordV3{
		RequestedMediaFileID: 42, EffectiveMediaFileID: 42,
		CurrentPlanID: current.PlanID, CurrentPlan: current,
	}
	candidate := current
	candidate.PlanID = "plan:candidate"
	candidate.Delivery = playback.DeliveryRemuxHLSV3
	candidate.Stream.Container = "hls"
	candidate.EffectiveRecipe.AudioCodec = "ac3"
	got := strings.Join(seekReanchorIdentityChangesV3(record, &candidate), ",")
	if want := "plan_id,delivery,container,audio_codec"; got != want {
		t.Fatalf("changed fields = %q, want %q", got, want)
	}
}

func TestFrozenSeekReanchorResultV3PreservesRouteMatrix(t *testing.T) {
	audioIndex := 0
	basePlan := func(name string) playback.PlanV3 {
		return playback.PlanV3{
			ProtocolVersion: playback.ProtocolV3, PlanID: "plan:" + name,
			Delivery:        playback.DeliveryOriginalHTTPV3,
			Stream:          playback.StreamV3{Protocol: playback.StreamHTTPProgressiveV3, Container: "mp4", MIMEType: "video/mp4", HeaderRefresh: playback.HeaderRefreshSessionV3},
			SelectedTracks:  playback.SelectedTracksV3{Audio: &playback.TrackIdentityV3{ID: playback.TrackIDV3(42, "audio", audioIndex), Index: &audioIndex}},
			EffectiveRecipe: playback.EffectiveRecipeV3{VideoCodec: "h264", AudioCodec: "aac", DynamicRange: "sdr"},
			Subtitle:        playback.SubtitleDecisionV3{Mode: playback.SubtitleOffV3},
			Transformations: []playback.TransformationV3{}, AppliedQuirks: []playback.AppliedQuirkV3{}, RuntimeCorrections: []string{},
			RequestedMediaFileID: 42, EffectiveMediaFileID: 42,
		}
	}
	tests := []struct {
		name   string
		mutate func(*playback.PlanV3, *playback.PlannerResultV3)
	}{
		{name: "direct"},
		{name: "progressive remux", mutate: func(plan *playback.PlanV3, result *playback.PlannerResultV3) {
			plan.Delivery = playback.DeliveryRemuxProgressiveV3
			result.PlayMethod = playback.PlayRemux
		}},
		{name: "HLS remux", mutate: func(plan *playback.PlanV3, result *playback.PlannerResultV3) {
			plan.Delivery = playback.DeliveryRemuxHLSV3
			plan.Stream = playback.StreamV3{Protocol: playback.StreamHLSV3, Container: "hls", MIMEType: "application/vnd.apple.mpegurl", HeaderRefresh: playback.HeaderRefreshSessionV3}
			result.PlayMethod = playback.PlayRemux
			result.TargetVideoCodec = "copy"
			result.TargetAudioCodec = "copy"
		}},
		{name: "audio converting remux", mutate: func(plan *playback.PlanV3, result *playback.PlannerResultV3) {
			plan.Delivery = playback.DeliveryRemuxProgressiveV3
			plan.Transformations = []playback.TransformationV3{{Name: "audio_to_aac", Executor: "server", RecipeVersion: "2"}}
			result.PlayMethod = playback.PlayRemux
			result.TranscodeAudio = true
			result.TargetAudioCodec = "aac"
			result.TargetAudioChannels = 2
		}},
		{name: "downloaded subtitle", mutate: func(plan *playback.PlanV3, result *playback.PlannerResultV3) {
			subtitleIndex := 7
			plan.SelectedTracks.Subtitle = &playback.TrackIdentityV3{ID: playback.TrackIDV3(42, "subtitle", subtitleIndex), Index: &subtitleIndex}
			plan.Subtitle = playback.SubtitleDecisionV3{Mode: playback.SubtitleConvertV3, TrackID: plan.SelectedTracks.Subtitle.ID, Artifact: &playback.SubtitleArtifactV3{MIMEType: "text/vtt", Format: "vtt"}}
			result.SubtitleTrackIndex = subtitleIndex
			result.SubtitleTransportTrackIndex = subtitleIndex
			result.SubtitleCodec = "srt"
			result.DownloadedSubtitleID = 71
		}},
		{name: "Dolby Vision transformation", mutate: func(plan *playback.PlanV3, _ *playback.PlannerResultV3) {
			plan.EffectiveRecipe.DynamicRange = "hdr10"
			plan.Transformations = []playback.TransformationV3{{Name: "server_dv7_to_hdr10", Executor: "server", RecipeVersion: "1"}}
		}},
		{name: "pooled node only transformation", mutate: func(plan *playback.PlanV3, result *playback.PlannerResultV3) {
			plan.Delivery = playback.DeliveryTranscodeHLSV3
			plan.Stream = playback.StreamV3{Protocol: playback.StreamHLSV3, Container: "hls", MIMEType: "application/vnd.apple.mpegurl", HeaderRefresh: playback.HeaderRefreshSessionV3}
			plan.Transformations = []playback.TransformationV3{{Name: "video_to_h264", Executor: "server", RecipeVersion: "2"}}
			result.PlayMethod = playback.PlayTranscode
			result.TargetVideoCodec = "h264"
			result.TargetAudioCodec = "aac"
			result.TargetResolution = "1080p"
		}},
		{name: "device quirks and runtime corrections", mutate: func(plan *playback.PlanV3, _ *playback.PlannerResultV3) {
			plan.AppliedQuirks = []playback.AppliedQuirkV3{{ID: "apple-hls-audio", RegistryRevision: "3", Action: "force-aac"}}
			plan.RuntimeCorrections = []string{"pcm_fallback"}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := basePlan(strings.ReplaceAll(test.name, " ", "-"))
			operational := playback.PlannerResultV3{Plan: &plan, PlayMethod: playback.PlayDirect, SubtitleTrackIndex: -1, SubtitleTransportTrackIndex: -1}
			if test.mutate != nil {
				test.mutate(&plan, &operational)
			}
			operational.Plan = &plan
			record := &playback.AttemptRecordV3{
				PlaybackAttemptID: "attempt-matrix-0001", RequestedMediaFileID: 42, EffectiveMediaFileID: 42,
				CurrentPlanID: plan.PlanID, CurrentPlan: plan, FrozenRecipe: playback.FreezeExecutableRecipeV3(operational),
			}
			result, err := frozenSeekReanchorResultV3(record, 321.25, time.Unix(1_786_000_000, 0))
			if err != nil || result.Plan == nil {
				t.Fatalf("frozen reanchor: result=%#v err=%v", result, err)
			}
			if err := validateSeekReanchorPlanV3(record, result.Plan); err != nil {
				t.Fatalf("route semantics changed: %v, plan=%#v", err, result.Plan)
			}
			if result.Plan.Timeline.SourceStartSeconds != 321.25 || result.PlayMethod != operational.PlayMethod ||
				result.TranscodeAudio != operational.TranscodeAudio || result.TargetVideoCodec != operational.TargetVideoCodec ||
				result.TargetAudioCodec != operational.TargetAudioCodec || result.TargetAudioChannels != operational.TargetAudioChannels ||
				result.TargetResolution != operational.TargetResolution || result.SubtitleTrackIndex != operational.SubtitleTrackIndex ||
				result.SubtitleTransportTrackIndex != operational.SubtitleTransportTrackIndex || result.SubtitleBurnIn != operational.SubtitleBurnIn ||
				result.SubtitleCodec != operational.SubtitleCodec || result.DownloadedSubtitleID != operational.DownloadedSubtitleID {
				t.Fatalf("operational recipe changed: got=%#v want=%#v", result, operational)
			}
		})
	}
}

func TestFrozenDownloadedSubtitleV3AcceptsInventoryReordering(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	file.ExternalSubtitles = []models.ExternalSubtitle{{Format: "srt"}}
	file.SubtitleTracks = []models.SubtitleTrack{{Index: 0, Codec: "ass"}}
	repo := newMockSubtitleRepoForHandler()
	repo.subtitles[71] = &subtitles.DownloadedSubtitle{ID: 71, MediaFileID: file.ID, Format: subtitles.FormatSRT}
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.SubtitleRepo = repo
	downloadedIndex := len(file.ExternalSubtitles) + len(file.SubtitleTracks)
	plan := &playback.PlanV3{PlanID: "plan:downloaded", SelectedTracks: playback.SelectedTracksV3{Subtitle: &playback.TrackIdentityV3{ID: playback.TrackIDV3(file.ID, "subtitle", downloadedIndex), Index: &downloadedIndex}}}
	result := playback.PlannerResultV3{Plan: plan, PlayMethod: playback.PlayDirect, SubtitleTrackIndex: downloadedIndex, SubtitleTransportTrackIndex: downloadedIndex, DownloadedSubtitleID: 71}
	recipe, err := handler.freezeExecutableRecipeV3(context.Background(), file, result)
	if err != nil {
		t.Fatalf("freeze: %v", err)
	}
	if recipe.SubtitleSource != playback.SubtitleSourceDownloadedV3 || recipe.DownloadedSubtitleID != 71 {
		t.Fatalf("frozen subtitle identity = %q/%d, want downloaded/71", recipe.SubtitleSource, recipe.DownloadedSubtitleID)
	}
	if err := handler.validateFrozenSubtitleIdentityV3(context.Background(), file, recipe); err != nil {
		t.Fatalf("stable inventory rejected: %v", err)
	}
	repo.list = []subtitles.DownloadedSubtitle{{ID: 72, MediaFileID: file.ID, Format: subtitles.FormatSRT}, {ID: 71, MediaFileID: file.ID, Format: subtitles.FormatSRT}}
	repo.listErr = errors.New("mutable inventory must not be consulted")
	if err := handler.validateFrozenSubtitleIdentityV3(context.Background(), file, recipe); err != nil {
		t.Fatalf("reordered downloaded subtitle inventory rejected stable identity: %v", err)
	}
	repo.getErr = errors.New("database unavailable")
	if err := handler.validateFrozenSubtitleIdentityV3(context.Background(), file, recipe); !errors.Is(err, errSubtitleStoreUnavailableV3) {
		t.Fatalf("validation error = %v, want wrapped subtitle-store failure", err)
	}
}

func TestAttachSubtitleArtifactV3UsesFrozenDownloadedIdentityWithoutOrdinalLookup(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	file.ExternalSubtitles = nil
	file.SubtitleTracks = nil
	repo := newMockSubtitleRepoForHandler()
	repo.subtitles[71] = &subtitles.DownloadedSubtitle{
		ID: 71, MediaFileID: file.ID, Format: subtitles.FormatVTT, S3Key: "selected-71.vtt",
	}
	// A second ordinal lookup would either fail or select a different row.
	// Artifact attachment must use GetDownloadedSubtitle(71) exclusively.
	repo.listErr = errors.New("mutable inventory must not be consulted")
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.SubtitleRepo = repo
	selectedIndex := 0
	plan := &playback.PlanV3{
		Subtitle: playback.SubtitleDecisionV3{Mode: playback.SubtitleRenderV3},
		Timeline: playback.TimelineV3{StreamOriginSeconds: 321},
	}
	recipe := playback.ExecutableRecipeV3{
		SubtitleSource: playback.SubtitleSourceDownloadedV3, DownloadedSubtitleID: 71,
		SubtitleTrackIndex: selectedIndex, SubtitleCodec: "vtt",
	}
	if err := handler.attachSubtitleArtifactV3(context.Background(), "session-frozen-subtitle", file, plan, selectedIndex, &recipe); err != nil {
		t.Fatalf("attach frozen downloaded subtitle: %v", err)
	}
	if plan.Subtitle.Artifact == nil || !strings.Contains(plan.Subtitle.Artifact.URL, "downloaded_subtitle_id=71") {
		t.Fatalf("artifact = %#v, want frozen downloaded subtitle 71", plan.Subtitle.Artifact)
	}
}

// A plan struct can arrive at artifact attachment already carrying the previous
// plan's subtitle artifact — a seek reanchor replays record.CurrentPlan
// verbatim. Every mode without a client-fetchable artifact must therefore clear
// it rather than let the stale URL ride along, and an `off` decision must shed
// its track_id with it. The inventory URLs are selection-independent and stay.
func TestAttachSubtitleArtifactV3ClearsStaleArtifactWhenNoArtifactMode(t *testing.T) {
	stale := func() *playback.SubtitleArtifactV3 {
		return &playback.SubtitleArtifactV3{URL: "/stream/old-session/subtitles/1.vtt?file_id=42", MIMEType: "text/vtt", Format: "vtt", TimingOriginSeconds: 12}
	}
	tests := []struct {
		name          string
		mode          playback.SubtitleModeV3
		selectedIndex int
		wantTrackID   string
	}{
		{name: "off", mode: playback.SubtitleOffV3, selectedIndex: -1},
		{name: "off with a carried-over ordinal", mode: playback.SubtitleOffV3, selectedIndex: 1},
		{name: "burn in keeps its track", mode: playback.SubtitleBurnInV3, selectedIndex: 1, wantTrackID: "file:42:subtitle:1"},
		{name: "render without a resolvable selection", mode: playback.SubtitleRenderV3, selectedIndex: -1, wantTrackID: "file:42:subtitle:1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			file := v3HandlerFixtureFile(t)
			file.ExternalSubtitles = []models.ExternalSubtitle{{Path: "/subs/a.srt", Language: "eng", Format: "srt"}}
			file.SubtitleTracks = []models.SubtitleTrack{{Index: 0, Codec: "subrip", Language: "fra"}}
			handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
			plan := &playback.PlanV3{
				Subtitle: playback.SubtitleDecisionV3{
					Mode:      test.mode,
					TrackID:   playback.TrackIDV3(file.ID, "subtitle", 1),
					Artifact:  stale(),
					Inventory: playback.BuildSubtitleInventoryV3(file, nil),
				},
			}
			if err := handler.attachSubtitleArtifactV3(context.Background(), "session-current", file, plan, test.selectedIndex, nil); err != nil {
				t.Fatalf("attach: %v", err)
			}
			if plan.Subtitle.Artifact != nil {
				t.Fatalf("artifact = %#v, want nil for mode %q with selected index %d", plan.Subtitle.Artifact, test.mode, test.selectedIndex)
			}
			if plan.Subtitle.TrackID != test.wantTrackID {
				t.Fatalf("track_id = %q, want %q", plan.Subtitle.TrackID, test.wantTrackID)
			}
			if len(plan.Subtitle.Inventory) != 2 {
				t.Fatalf("inventory = %#v, want both tracks published", plan.Subtitle.Inventory)
			}
			for _, item := range plan.Subtitle.Inventory {
				if !strings.HasPrefix(item.URL, "/stream/session-current/subtitles/") {
					t.Fatalf("inventory entry %d url = %q, want a current-session sidecar url", item.CombinedIndex, item.URL)
				}
			}
		})
	}
}

// The regression this guards: a session that rendered a sidecar, then replanned
// with subtitles off, kept republishing the old artifact.
func TestAttachSubtitleArtifactV3DropsArtifactAcrossRenderToOffReplan(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	file.ExternalSubtitles = []models.ExternalSubtitle{{Path: "/subs/a.srt", Language: "eng", Format: "srt"}}
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	rendered := &playback.PlanV3{
		Subtitle: playback.SubtitleDecisionV3{
			Mode:      playback.SubtitleRenderV3,
			TrackID:   playback.TrackIDV3(file.ID, "subtitle", 0),
			Inventory: playback.BuildSubtitleInventoryV3(file, nil),
		},
	}
	if err := handler.attachSubtitleArtifactV3(context.Background(), "session-1", file, rendered, 0, nil); err != nil {
		t.Fatalf("attach render: %v", err)
	}
	if rendered.Subtitle.Artifact == nil {
		t.Fatal("render mode published no artifact")
	}
	// Replan reuses the accepted plan (the seek-reanchor shape) and turns
	// subtitles off; the durable artifact must not survive the transition.
	replanned := *rendered
	replanned.Subtitle.Mode = playback.SubtitleOffV3
	if err := handler.attachSubtitleArtifactV3(context.Background(), "session-1", file, &replanned, -1, nil); err != nil {
		t.Fatalf("attach off: %v", err)
	}
	if replanned.Subtitle.Artifact != nil || replanned.Subtitle.TrackID != "" {
		t.Fatalf("off plan = %#v, want no artifact and no track_id", replanned.Subtitle)
	}
	if rendered.Subtitle.Artifact == nil {
		t.Fatal("clearing the successor mutated the accepted plan's artifact")
	}
}

func TestSubtitleArtifactStoreFailuresAreRetryable(t *testing.T) {
	storeErr := errors.New("database unavailable")
	wantRetryable := subtitleArtifactErrorV3("subtitle lookup failed", wrapSubtitleStoreErrorV3(storeErr))
	if !wantRetryable.retryable || !errors.Is(wantRetryable.cause, errSubtitleStoreUnavailableV3) {
		t.Fatalf("store failure = %#v, want retryable subtitle error", wantRetryable)
	}
	wantPermanent := subtitleArtifactErrorV3("subtitle identity changed", errors.New("identity changed"))
	if wantPermanent.retryable {
		t.Fatalf("identity failure = %#v, want non-retryable subtitle error", wantPermanent)
	}

	file := v3HandlerFixtureFile(t)
	file.ExternalSubtitles = nil
	file.SubtitleTracks = nil
	repo := newMockSubtitleRepoForHandler()
	repo.getErr = storeErr
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.SubtitleRepo = repo
	plan := &playback.PlanV3{
		Subtitle: playback.SubtitleDecisionV3{Mode: playback.SubtitleRenderV3},
	}
	recipe := playback.ExecutableRecipeV3{
		SubtitleSource: playback.SubtitleSourceDownloadedV3, DownloadedSubtitleID: 71,
		SubtitleTrackIndex: 0, SubtitleCodec: "vtt",
	}
	err := handler.attachSubtitleArtifactV3(context.Background(), "session-store-error", file, plan, 0, &recipe)
	if !errors.Is(err, errSubtitleStoreUnavailableV3) {
		t.Fatalf("attach error = %v, want wrapped subtitle-store failure", err)
	}
}

func TestFreezeExecutableRecipeV3FailsLoudlyWhenDownloadedIdentityUnavailable(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	downloadedIndex := len(file.ExternalSubtitles) + len(file.SubtitleTracks)
	result := playback.PlannerResultV3{Plan: &playback.PlanV3{PlanID: "plan:downloaded"}, PlayMethod: playback.PlayDirect, SubtitleTrackIndex: downloadedIndex, SubtitleTransportTrackIndex: downloadedIndex}
	if _, err := handler.freezeExecutableRecipeV3(context.Background(), file, result); err == nil {
		t.Fatal("downloaded subtitle without a planning-snapshot identity was frozen")
	}
	result.DownloadedSubtitleID = 71
	file.ExternalSubtitles = []models.ExternalSubtitle{{Path: "/subs/added-after-planning.srt", Format: "srt"}}
	recipe, err := handler.freezeExecutableRecipeV3(context.Background(), file, result)
	if err != nil || recipe.SubtitleSource != playback.SubtitleSourceDownloadedV3 || recipe.DownloadedSubtitleID != 71 {
		t.Fatalf("freeze planning snapshot identity: recipe=%#v err=%v", recipe, err)
	}
}

func TestFrozenSubtitleIdentityV3RejectsExternalAndEmbeddedInventoryDrift(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	file.ExternalSubtitles = []models.ExternalSubtitle{{Path: "/subs/en.srt", Format: "srt"}, {Path: "/subs/de.srt", Format: "srt"}}
	file.SubtitleTracks = []models.SubtitleTrack{{Index: 3, Codec: "ass"}}
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))

	externalIndex := 1 // /subs/de.srt
	externalResult := playback.PlannerResultV3{Plan: &playback.PlanV3{PlanID: "plan:external"}, PlayMethod: playback.PlayDirect, SubtitleTrackIndex: externalIndex, SubtitleTransportTrackIndex: externalIndex}
	externalRecipe, err := handler.freezeExecutableRecipeV3(context.Background(), file, externalResult)
	if err != nil {
		t.Fatalf("freeze external: %v", err)
	}
	if externalRecipe.SubtitleSource != playback.SubtitleSourceExternalV3 || externalRecipe.ExternalSubtitlePath != "/subs/de.srt" {
		t.Fatalf("frozen external identity = %q/%q", externalRecipe.SubtitleSource, externalRecipe.ExternalSubtitlePath)
	}

	embeddedIndex := 2 // combined index of SubtitleTracks[0]
	embeddedResult := playback.PlannerResultV3{Plan: &playback.PlanV3{PlanID: "plan:embedded"}, PlayMethod: playback.PlayDirect, SubtitleTrackIndex: embeddedIndex, SubtitleTransportTrackIndex: 3}
	embeddedRecipe, err := handler.freezeExecutableRecipeV3(context.Background(), file, embeddedResult)
	if err != nil {
		t.Fatalf("freeze embedded: %v", err)
	}
	if embeddedRecipe.SubtitleSource != playback.SubtitleSourceEmbeddedV3 || embeddedRecipe.EmbeddedStreamIndex != 3 {
		t.Fatalf("frozen embedded identity = %q/%d", embeddedRecipe.SubtitleSource, embeddedRecipe.EmbeddedStreamIndex)
	}

	if err := handler.validateFrozenSubtitleIdentityV3(context.Background(), file, externalRecipe); err != nil {
		t.Fatalf("stable external inventory rejected: %v", err)
	}
	if err := handler.validateFrozenSubtitleIdentityV3(context.Background(), file, embeddedRecipe); err != nil {
		t.Fatalf("stable embedded inventory rejected: %v", err)
	}

	// Deleting the first external subtitle shifts every later combined index.
	// The frozen external selection now points past the external segment and
	// the frozen embedded selection resolves one entry early; both must be
	// rejected rather than silently re-resolved to a different artifact.
	file.ExternalSubtitles = file.ExternalSubtitles[1:]
	if err := handler.validateFrozenSubtitleIdentityV3(context.Background(), file, externalRecipe); err == nil {
		t.Fatal("external subtitle deletion was accepted for the frozen external selection")
	}
	if err := handler.validateFrozenSubtitleIdentityV3(context.Background(), file, embeddedRecipe); err == nil {
		t.Fatal("external subtitle deletion was accepted for the frozen embedded selection")
	}
}

func TestPrepareTransportV3ProgressiveRemuxUsesResolvedCopyAnchor(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.copySeekAnchor = func(_ context.Context, _ string, inputPath string, requested float64, segmentDuration int) (float64, int, error) {
		if inputPath != "/media/movie.mkv" || segmentDuration != 2 {
			t.Fatalf("copy seek probe input=%q segment=%d", inputPath, segmentDuration)
		}
		return requested - 0.75, int((requested - 0.75) / float64(segmentDuration)), nil
	}
	session := &playback.Session{ID: "session-progressive", UserID: 7, ProfileID: "profile-1", MediaFileID: 42, PlayMethod: playback.PlayRemux, BasePlayMethod: playback.PlayRemux, AudioTrackIndex: 0}
	file := &models.MediaFile{ID: 42, FilePath: "/media/movie.mkv"}
	for index, requested := range []float64{321.25, 654.5} {
		plan := &playback.PlanV3{
			PlanID:               "plan:progressive",
			Delivery:             playback.DeliveryRemuxProgressiveV3,
			EffectiveMediaFileID: 42,
			Timeline:             playback.TimelineV3{SourceStartSeconds: requested, PlayerStartSeconds: requested, CanSeekAnywhere: true, SeekRestoration: "player_position"},
		}
		transport, transportErr := handler.prepareTransportV3(httptest.NewRequest(http.MethodPost, "/", nil), session, file, playback.PlannerResultV3{Plan: plan, PlayMethod: playback.PlayRemux}, mediaAuthModeV3{})
		if transportErr != nil {
			t.Fatalf("prepare progressive transport: %v", transportErr)
		}

		parsed, err := url.Parse(transport.url)
		if err != nil {
			transport.rollback()
			t.Fatal(err)
		}
		if parsed.Query().Get("st") == "" || parsed.Query().Get("seek") != strconv.FormatFloat(requested, 'f', -1, 64) {
			transport.rollback()
			t.Fatalf("progressive reanchor URL %d = %q", index, transport.url)
		}
		origin := requested - 0.75
		if plan.Timeline.PlayerStartSeconds != 0.75 || plan.Timeline.StreamOriginSeconds != origin ||
			plan.Timeline.TimelineOffsetSeconds != origin || plan.Timeline.CanSeekAnywhere ||
			plan.Timeline.SeekWindowStartSeconds == nil || *plan.Timeline.SeekWindowStartSeconds != origin ||
			plan.Timeline.SeekWindowEndSeconds != nil || plan.Timeline.SeekRestoration != "source_position" {
			transport.rollback()
			t.Fatalf("progressive reanchor timeline %d = %#v", index, plan.Timeline)
		}
		transport.rollback()
	}
}

func TestPrepareTransportV3AudioOnlyRemuxSkipsVideoCopyAnchor(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	probeCalls := 0
	handler.copySeekAnchor = func(context.Context, string, string, float64, int) (float64, int, error) {
		probeCalls++
		return 0, 0, errors.New("audio-only remux must not probe a video keyframe")
	}
	requested := 321.25
	plan := &playback.PlanV3{
		PlanID:   "plan:audio-only-progressive",
		Delivery: playback.DeliveryRemuxProgressiveV3,
		Timeline: playback.TimelineV3{
			SourceStartSeconds: requested,
			PlayerStartSeconds: requested,
			CanSeekAnywhere:    true,
			SeekRestoration:    "player_position",
		},
	}
	file := &models.MediaFile{
		ID:          42,
		BaseType:    "audiobook",
		FilePath:    "/media/book.m4b",
		CodecAudio:  "aac",
		AudioTracks: []models.AudioTrack{{Codec: "aac", Channels: 2}},
	}
	transport, transportErr := handler.prepareTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		&playback.Session{ID: "session-audio-only", MediaFileID: 42},
		file,
		playback.PlannerResultV3{Plan: plan, PlayMethod: playback.PlayRemux, TargetAudioCodec: "aac"}, mediaAuthModeV3{})
	if transportErr != nil {
		t.Fatalf("prepare audio-only transport: %v", transportErr)
	}
	defer transport.rollback()
	if probeCalls != 0 {
		t.Fatalf("audio-only copy anchor probes = %d, want 0", probeCalls)
	}
	parsed, err := url.Parse(transport.url)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Query().Get("seek") != strconv.FormatFloat(requested, 'f', -1, 64) {
		t.Fatalf("audio-only seek URL = %q", transport.url)
	}
	if plan.Timeline.PlayerStartSeconds != 0 || plan.Timeline.CanSeekAnywhere ||
		plan.Timeline.SeekRestoration != "source_position" || plan.Timeline.StreamOriginSeconds != requested ||
		plan.Timeline.TimelineOffsetSeconds != requested || plan.Timeline.SeekWindowStartSeconds == nil ||
		*plan.Timeline.SeekWindowStartSeconds != requested || plan.Timeline.SeekWindowEndSeconds != nil {
		t.Fatalf("audio-only timeline changed = %#v", plan.Timeline)
	}
}

func TestPrepareTransportV3CopyAnchorFailureIsRetryable(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.copySeekAnchor = func(context.Context, string, string, float64, int) (float64, int, error) {
		return 0, 0, errors.New("probe failed")
	}
	plan := &playback.PlanV3{PlanID: "plan:copy-failure", Delivery: playback.DeliveryRemuxHLSV3, Timeline: playback.TimelineV3{SourceStartSeconds: 120}}
	_, transportErr := handler.prepareTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		&playback.Session{ID: "session-copy-failure"},
		&models.MediaFile{ID: 42, FilePath: "/media/movie.mkv"},
		playback.PlannerResultV3{Plan: plan, PlayMethod: playback.PlayRemux}, mediaAuthModeV3{})
	if transportErr == nil || transportErr.reason != "transcode_start_failed" || !transportErr.retryable || transportErr.cause == nil || transportErr.cause.Error() != "probe failed" {
		t.Fatalf("transport error = %#v, want retryable copy anchor failure", transportErr)
	}
}

func TestPrepareTransportV3RejectsNodeMissingRequiredTransformation(t *testing.T) {
	startHits := 0
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hw-capabilities":
			writeJSON(w, http.StatusOK, playback.HWAccelInfo{Transformations: []playback.TransformationV3{{Name: "video_to_h264", Executor: "server", RecipeVersion: "1"}}})
		case "/transcode/start":
			startHits++
			w.WriteHeader(http.StatusAccepted)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer remote.Close()

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.NodePlanner = staticNodePlannerV3{plan: nodepool.Plan{TranscodeNode: &nodepool.Node{URL: remote.URL}}}
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"playback.local_transcode_fallback": "false"}}
	plan := &playback.PlanV3{
		PlanID:   "plan:remote-capability",
		Delivery: playback.DeliveryTranscodeHLSV3,
		Transformations: []playback.TransformationV3{
			{Name: "video_to_h264", Executor: "server", RecipeVersion: "2"},
			{Name: "audio_to_aac", Executor: "server", RecipeVersion: "2"},
		},
	}
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	_, transportErr := handler.prepareTransportV3(request, &playback.Session{ID: "session-capability"}, v3HandlerFixtureFile(t), playback.PlannerResultV3{Plan: plan, PlayMethod: playback.PlayTranscode, TargetVideoCodec: "h264", TargetAudioCodec: "aac"}, mediaAuthModeV3{})
	if transportErr == nil || transportErr.reason != "transcode_node_capability_unavailable" {
		t.Fatalf("transport error = %#v", transportErr)
	}
	if startHits != 0 {
		t.Fatalf("incompatible node received %d start requests", startHits)
	}
}

// TestPrepareTransportV3RequiresRemoteManifestReadiness verifies remote startup waits for playable output.
func TestPrepareTransportV3RequiresRemoteManifestReadiness(t *testing.T) {
	var startRequest transcodenode.TranscodeStartRequest
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/hw-capabilities":
			writeJSON(w, http.StatusOK, playback.HWAccelInfo{Transformations: []playback.TransformationV3{
				{Name: "video_to_h264", Executor: "server", RecipeVersion: "2"},
				{Name: "audio_to_aac", Executor: "server", RecipeVersion: "2"},
			}})
		case r.Method == http.MethodPost && r.URL.Path == "/transcode/start":
			if err := json.NewDecoder(r.Body).Decode(&startRequest); err != nil {
				t.Errorf("decode remote start: %v", err)
			}
			writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{SessionID: startRequest.SessionID, Status: "started", HWAccel: "qsv"})
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer remote.Close()

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodePlanner = staticNodePlannerV3{plan: nodepool.Plan{TranscodeNode: &nodepool.Node{URL: remote.URL}}}
	plan := &playback.PlanV3{
		PlanID:   "plan:remote-ready",
		Delivery: playback.DeliveryTranscodeHLSV3,
		Transformations: []playback.TransformationV3{
			{Name: "video_to_h264", Executor: "server", RecipeVersion: "2"},
			{Name: "audio_to_aac", Executor: "server", RecipeVersion: "2"},
		},
	}
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	transport, transportErr := handler.prepareTransportV3(request, &playback.Session{ID: "session-ready", UserID: 7, ProfileID: "profile-1"}, v3HandlerFixtureFile(t), playback.PlannerResultV3{Plan: plan, PlayMethod: playback.PlayTranscode, TargetVideoCodec: "h264", TargetAudioCodec: "aac"}, mediaAuthModeV3{})
	if transportErr != nil {
		t.Fatalf("prepare remote transport: %v", transportErr)
	}
	defer transport.rollback()
	if !startRequest.RequireReady {
		t.Fatal("protocol-v3 remote start did not require manifest readiness")
	}
	if transport.hwAccel != "qsv" || transport.toneMapMode != "" {
		t.Fatalf("remote execution facts = hw %q tone_map %q, want qsv and empty", transport.hwAccel, transport.toneMapMode)
	}
}

func TestPrepareTransportV3KeepsHeaderAuthenticatedRemoteHLSBehindAPI(t *testing.T) {
	for _, test := range []struct {
		name      string
		features  []string
		wantProxy bool
	}{
		{name: "header authenticated", features: []string{playback.FeatureHeaderAuthenticatedMediaV3}},
		{name: "legacy proxy URL", wantProxy: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/hw-capabilities":
					writeJSON(w, http.StatusOK, playback.HWAccelInfo{Transformations: []playback.TransformationV3{
						{Name: playback.TransformationVideoToH264V3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationVideoToH264RecipeVersionV3},
						{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3},
					}})
				case r.Method == http.MethodPost && r.URL.Path == "/transcode/start":
					writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{Status: "started"})
				case r.Method == http.MethodDelete:
					w.WriteHeader(http.StatusNoContent)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer remote.Close()

			handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
			handler.JWTSecret = "test-stream-signing-secret"
			handler.NodePlanner = staticNodePlannerV3{plan: nodepool.Plan{
				TranscodeNode: &nodepool.Node{URL: remote.URL},
				ProxyNode:     &nodepool.Node{URL: "http://proxy.example"},
			}}
			plan := &playback.PlanV3{
				PlanID:   "plan:remote-header-auth",
				Delivery: playback.DeliveryTranscodeHLSV3,
				Transformations: []playback.TransformationV3{
					{Name: playback.TransformationVideoToH264V3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationVideoToH264RecipeVersionV3},
					{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3},
				},
			}
			request := httptest.NewRequest(http.MethodPost, "/", nil)
			transport, transportErr := handler.prepareTransportV3(request, &playback.Session{ID: "session-remote-auth", UserID: 7, ProfileID: "profile-1"}, v3HandlerFixtureFile(t), playback.PlannerResultV3{Plan: plan, PlayMethod: playback.PlayTranscode, TargetVideoCodec: "h264", TargetAudioCodec: "aac"}, headerAuthenticatedMediaV3(test.features))
			if transportErr != nil {
				t.Fatalf("prepare remote HLS: %v", transportErr)
			}
			defer transport.rollback()

			parsed, err := url.Parse(transport.url)
			if err != nil {
				t.Fatal(err)
			}
			if test.wantProxy {
				if !parsed.IsAbs() || parsed.Host != "proxy.example" || !strings.HasPrefix(parsed.Path, "/stream/transcode/") {
					t.Fatalf("legacy HLS URL = %q, want signed proxy route", transport.url)
				}
			} else if parsed.IsAbs() || parsed.Path != "/playback/transcode/session-remote-auth/master.m3u8" || parsed.RawQuery != "" || strings.Contains(transport.url, "proxy.example") {
				t.Fatalf("header-authenticated HLS URL = %q, want tokenless API-local manifest", transport.url)
			}
			if transport.nodeURL != remote.URL {
				t.Fatalf("remote executor = %q, want %q behind API facade", transport.nodeURL, remote.URL)
			}
		})
	}
}

func TestPrepareTransportV3SendsResolvedCopyAnchorToRemoteExecutor(t *testing.T) {
	var startRequest transcodenode.TranscodeStartRequest
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/hw-capabilities":
			writeJSON(w, http.StatusOK, playback.HWAccelInfo{})
		case r.Method == http.MethodPost && r.URL.Path == "/transcode/start":
			if err := json.NewDecoder(r.Body).Decode(&startRequest); err != nil {
				t.Errorf("decode remote start: %v", err)
			}
			writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{SessionID: startRequest.SessionID, Status: "started"})
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer remote.Close()

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodePlanner = staticNodePlannerV3{plan: nodepool.Plan{TranscodeNode: &nodepool.Node{URL: remote.URL}}}
	handler.copySeekAnchor = func(context.Context, string, string, float64, int) (float64, int, error) {
		return 1085.501, 542, nil
	}
	plan := &playback.PlanV3{
		PlanID:   "plan:remote-copy-anchor",
		Delivery: playback.DeliveryRemuxHLSV3,
		Source:   playback.SourceDescriptorV3{DVProfile: 8},
		EffectiveRecipe: playback.EffectiveRecipeV3{
			DynamicRange: playback.DynamicRangeDolbyVisionV3,
		},
		Timeline: playback.TimelineV3{SourceStartSeconds: 1086.2},
	}
	transport, transportErr := handler.prepareTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		&playback.Session{ID: "session-remote-copy-anchor", UserID: 7, ProfileID: "profile-1"},
		&models.MediaFile{ID: 42, FilePath: "/media/movie.mkv", CodecVideo: "h264"},
		playback.PlannerResultV3{Plan: plan, PlayMethod: playback.PlayRemux, TargetAudioCodec: "aac"}, mediaAuthModeV3{})
	if transportErr != nil {
		t.Fatalf("prepare remote copy transport: %v", transportErr)
	}
	defer transport.rollback()
	if startRequest.SeekSeconds != 1086.2 || startRequest.StreamOriginSeconds != 1085.501 ||
		!startRequest.CopySeekAnchorResolved || startRequest.StartSegmentNumber != 542 {
		t.Fatalf("remote copy timeline = %#v", startRequest)
	}
	if startRequest.VideoSampleEntry != playback.VideoSampleEntryDVH1 {
		t.Fatalf("remote copy VideoSampleEntry = %q", startRequest.VideoSampleEntry)
	}
	if plan.Timeline.StreamOriginSeconds != 1085.501 || plan.Timeline.TimelineOffsetSeconds != 1085.501 ||
		math.Abs(plan.Timeline.PlayerStartSeconds-0.699) > 0.0001 {
		t.Fatalf("advertised copy timeline = %#v", plan.Timeline)
	}
}

func TestVideoSampleEntryForPlanV3(t *testing.T) {
	tests := []struct {
		name string
		plan *playback.PlanV3
		want string
	}{
		{
			name: "preserved profile 8",
			plan: &playback.PlanV3{Delivery: playback.DeliveryRemuxHLSV3,
				Source:          playback.SourceDescriptorV3{DVProfile: 8},
				EffectiveRecipe: playback.EffectiveRecipeV3{DynamicRange: playback.DynamicRangeDolbyVisionV3}},
			want: playback.VideoSampleEntryDVH1,
		},
		{
			name: "stripped HDR10",
			plan: &playback.PlanV3{Delivery: playback.DeliveryRemuxHLSV3,
				Transformations: []playback.TransformationV3{{Name: playback.TransformationServerDV7HDR10V3}}},
			want: playback.VideoSampleEntryHVC1,
		},
		{
			name: "progressive unchanged",
			plan: &playback.PlanV3{Delivery: playback.DeliveryRemuxProgressiveV3,
				Source:          playback.SourceDescriptorV3{DVProfile: 8},
				EffectiveRecipe: playback.EffectiveRecipeV3{DynamicRange: playback.DynamicRangeDolbyVisionV3}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := videoSampleEntryForPlanV3(tc.plan); got != tc.want {
				t.Fatalf("sample entry = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPrepareTransportV3UsesFrozenSourceMetadataAfterProbeDrift(t *testing.T) {
	var startRequest transcodenode.TranscodeStartRequest
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/hw-capabilities":
			writeJSON(w, http.StatusOK, playback.HWAccelInfo{})
		case r.Method == http.MethodPost && r.URL.Path == "/transcode/start":
			if err := json.NewDecoder(r.Body).Decode(&startRequest); err != nil {
				t.Errorf("decode remote start: %v", err)
			}
			writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{SessionID: startRequest.SessionID, Status: "started"})
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer remote.Close()

	file := v3HandlerFixtureFile(t)
	file.CodecVideo = "h264"
	file.VideoTracks[0].Profile = "High 10"
	file.VideoTracks[0].BitDepth = 10
	file.Duration = 7_201
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodePlanner = staticNodePlannerV3{plan: nodepool.Plan{TranscodeNode: &nodepool.Node{URL: remote.URL}}}
	plan := &playback.PlanV3{PlanID: "plan:frozen-source", Delivery: playback.DeliveryTranscodeHLSV3}
	initial := playback.PlannerResultV3{
		Plan: plan, PlayMethod: playback.PlayTranscode, TargetVideoCodec: "h264", TargetAudioCodec: "aac",
		SubtitleTrackIndex: -1, SubtitleTransportTrackIndex: -1,
	}
	recipe, err := handler.freezeExecutableRecipeV3(context.Background(), file, initial)
	if err != nil {
		t.Fatalf("freeze executable recipe: %v", err)
	}
	file.CodecVideo = "mpeg2video"
	file.VideoTracks[0].Profile = "main"
	file.VideoTracks[0].BitDepth = 8
	file.Duration = 99
	result := recipe.PlannerResult(plan)
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	transport, transportErr := handler.prepareTransportV3(request, &playback.Session{ID: "session-frozen-source", UserID: 7, ProfileID: "profile-1"}, file, result, mediaAuthModeV3{})
	if transportErr != nil {
		t.Fatalf("prepare remote transport: %v", transportErr)
	}
	defer transport.rollback()
	if startRequest.SourceVideoCodec != "h264" || !startRequest.SoftwareVideoDecode || startRequest.TotalDuration != 7_201 {
		t.Fatalf("remote start consumed refreshed probe metadata: %#v", startRequest)
	}
}

func TestSourceExecutionMetadataV3FreezesH264High10SoftwareDecode(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	file.VideoTracks[0].Profile = "High 10"
	file.VideoTracks[0].BitDepth = 10

	metadata := sourceExecutionMetadataV3(file, playback.PlannerResultV3{})
	if !metadata.SoftwareVideoDecode {
		t.Fatalf("High 10 AVC source metadata = %#v, want software decode", metadata)
	}
	file.CodecVideo = ""
	metadata = sourceExecutionMetadataV3(file, playback.PlannerResultV3{})
	if metadata.VideoCodec != "h264" || !metadata.SoftwareVideoDecode {
		t.Fatalf("track-backed High 10 AVC source metadata = %#v, want h264 software decode", metadata)
	}
	plan := &playback.PlanV3{PlanID: "plan:high10", Delivery: playback.DeliveryTranscodeHLSV3}
	recipe, err := NewPlaybackHandler(playback.NewSessionManager(0, 0)).freezeExecutableRecipeV3(context.Background(), file, playback.PlannerResultV3{
		Plan: plan, PlayMethod: playback.PlayTranscode, TargetVideoCodec: "h264", TargetAudioCodec: "aac",
		SubtitleTrackIndex: -1, SubtitleTransportTrackIndex: -1,
	})
	if err != nil {
		t.Fatalf("freeze High 10 recipe: %v", err)
	}
	if !recipe.SoftwareVideoDecode {
		t.Fatalf("frozen recipe = %#v, want software decode", recipe)
	}
	thawed := recipe.PlannerResult(plan)
	if thawed.FrozenSourceMetadata == nil || !thawed.FrozenSourceMetadata.SoftwareVideoDecode {
		t.Fatalf("thawed source metadata = %#v, want software decode", thawed.FrozenSourceMetadata)
	}
}

func TestSourceExecutionMetadataV3ScopesDolbyVisionProvenanceToToneMapRecipe(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	file.VideoTracks[0].DVConfigPresent = true
	file.VideoTracks[0].DVBLCompatIDPresent = true
	file.VideoTracks[0].DVBLPresent = true
	file.VideoTracks[0].DVRPUPresent = true

	metadata := sourceExecutionMetadataV3(file, playback.PlannerResultV3{})
	if metadata.ToneMapDVConfigPresent || metadata.ToneMapDVBLCompatIDPresent || metadata.ToneMapDVBLPresent || metadata.ToneMapDVRPUPresent {
		t.Fatalf("non-tone-map source metadata retained Dolby Vision provenance: %#v", metadata)
	}

	toneMapResult := playback.PlannerResultV3{
		ToneMapMode:              tonemap.ModeHardware,
		ToneMapSourceKind:        tonemap.SourcePQ,
		ToneMapPreflightRequired: true,
		ToneMapSourceRevision:    tonemap.RevisionForFile(file),
	}
	metadata = sourceExecutionMetadataV3(file, toneMapResult)
	if !metadata.ToneMapDVConfigPresent || !metadata.ToneMapDVBLCompatIDPresent || !metadata.ToneMapDVBLPresent || !metadata.ToneMapDVRPUPresent {
		t.Fatalf("tone-map source metadata lost Dolby Vision provenance: %#v", metadata)
	}

	frozen := metadata
	result := playback.PlannerResultV3{FrozenSourceMetadata: &frozen}
	metadata = sourceExecutionMetadataV3(file, result)
	if metadata.ToneMapSourceKind != "" || metadata.ToneMapPreflightRequired || !metadata.ToneMapSourceRevision.IsZero() ||
		metadata.ToneMapDVConfigPresent || metadata.ToneMapDVBLCompatIDPresent || metadata.ToneMapDVBLPresent || metadata.ToneMapDVRPUPresent {
		t.Fatalf("non-tone-map frozen metadata retained a partial recipe: %#v", metadata)
	}
}

func TestPrepareLocalTransportV3AllowsDolbyVisionVideoCopyAudioAdaptation(t *testing.T) {
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager)
	ffmpegPath := writePlaybackTestFFmpeg(t)
	transcodeDir := t.TempDir()
	handler.PlaybackConfig = func() config.PlaybackConfig {
		return config.PlaybackConfig{FFmpegPath: ffmpegPath, TranscodeDir: transcodeDir, TranscodeEnabled: true, HWAccel: playback.HWAccelNone}
	}
	file := v3HandlerFixtureFile(t)
	file.CodecVideo = "hevc"
	file.CodecAudio = "eac3"
	file.Resolution = "2160p"
	file.VideoTracks[0].Codec = "hevc"
	file.VideoTracks[0].DVProfile = 8
	file.VideoTracks[0].DVBLCompatID = 1
	file.VideoTracks[0].DVConfigPresent = true
	file.VideoTracks[0].DVBLCompatIDPresent = true
	file.VideoTracks[0].DVBLPresent = true
	file.VideoTracks[0].DVRPUPresent = true
	result := playback.PlannerResultV3{
		Plan:       &playback.PlanV3{PlanID: "plan:dv-copy-audio-adaptation", Delivery: playback.DeliveryRemuxHLSV3},
		PlayMethod: playback.PlayRemux, TargetVideoCodec: "copy", TargetAudioCodec: "aac", TargetResolution: "2160p",
		SubtitleTrackIndex: -1, SubtitleTransportTrackIndex: -1,
	}
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	transport, transportErr := handler.prepareLocalTransportV3(request,
		&playback.Session{ID: "session-dv-copy-audio-adaptation", UserID: 7, ProfileID: "profile-1"},
		file, result, preparedTimelineV3{}, mediaAuthModeV3{})
	if transportErr != nil {
		t.Fatalf("prepare Dolby Vision remux transport: %v (cause: %v)", transportErr, transportErr.cause)
	}
	transport.rollback()
}

func TestPrepareRemoteTransportV3OmitsToneMapProvenanceForDolbyVisionRemux(t *testing.T) {
	var startRequest transcodenode.TranscodeStartRequest
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/transcode/start":
			if err := json.NewDecoder(r.Body).Decode(&startRequest); err != nil {
				t.Errorf("decode remote start: %v", err)
			}
			if startRequest.ToneMapMode == "" && (startRequest.ToneMapDVConfigPresent || startRequest.ToneMapDVBLCompatIDPresent || startRequest.ToneMapDVBLPresent || startRequest.ToneMapDVRPUPresent) {
				http.Error(w, "incomplete tone-map recipe", http.StatusUnprocessableEntity)
				return
			}
			writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{SessionID: startRequest.SessionID, Status: "started"})
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer remote.Close()

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	file := v3HandlerFixtureFile(t)
	file.CodecVideo = "hevc"
	file.VideoTracks[0].Codec = "hevc"
	file.VideoTracks[0].DVConfigPresent = true
	file.VideoTracks[0].DVBLCompatIDPresent = true
	file.VideoTracks[0].DVBLPresent = true
	file.VideoTracks[0].DVRPUPresent = true
	result := playback.PlannerResultV3{
		Plan:       &playback.PlanV3{PlanID: "plan:remote-dv-remux", Delivery: playback.DeliveryRemuxHLSV3},
		PlayMethod: playback.PlayRemux, TargetVideoCodec: "copy", TargetAudioCodec: "aac", TargetResolution: "2160p",
		SubtitleTrackIndex: -1, SubtitleTransportTrackIndex: -1,
	}
	transport, transportErr := handler.prepareRemoteTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		&playback.Session{ID: "session-remote-dv-remux", UserID: 7, ProfileID: "profile-1"},
		file, result, nodepool.Plan{TranscodeNode: &nodepool.Node{URL: remote.URL}}, preparedTimelineV3{}, mediaAuthModeV3{})
	if transportErr != nil {
		t.Fatalf("prepare remote Dolby Vision remux transport: %v (cause: %v)", transportErr, transportErr.cause)
	}
	defer transport.rollback()
	if startRequest.TargetCodecVideo != "copy" || startRequest.ToneMapMode != "" || startRequest.ToneMapDVConfigPresent || startRequest.ToneMapDVBLCompatIDPresent || startRequest.ToneMapDVBLPresent || startRequest.ToneMapDVRPUPresent {
		t.Fatalf("remote remux request carried a partial tone-map recipe: %#v", startRequest)
	}
}

func TestSourceVideoTranscodeFactsV3UsesFrozenProfileAndBitDepth(t *testing.T) {
	var recipe playback.ExecutableRecipeV3
	if err := json.Unmarshal([]byte(`{"version":2,"plan_id":"plan:frozen-source-facts","play_method":"transcode","source_video_profile":"Main 10","source_video_bit_depth":10}`), &recipe); err != nil {
		t.Fatalf("unmarshal frozen recipe: %v", err)
	}
	result := recipe.PlannerResult(&playback.PlanV3{PlanID: "plan:frozen-source-facts"})
	profile, depth := sourceVideoTranscodeFactsV3(&models.MediaFile{}, result)
	if profile != "Main 10" || depth != 10 {
		t.Fatalf("frozen source facts = %q/%d, want Main 10/10", profile, depth)
	}
}

func TestPrepareLocalTransportV3ReturnsStableTerminalWhenFFmpegExitsBeforeReady(t *testing.T) {
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager)
	handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpegAlwaysFailing(t), t.TempDir())
	file := v3HandlerFixtureFile(t)
	plan := &playback.PlanV3{PlanID: "plan:startup-failure", Delivery: playback.DeliveryTranscodeHLSV3}
	result := playback.PlannerResultV3{
		Plan: plan, PlayMethod: playback.PlayTranscode, TargetVideoCodec: "h264", TargetAudioCodec: "aac", TargetResolution: "720p",
		SubtitleTrackIndex: -1, SubtitleTransportTrackIndex: -1,
	}
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	timeline, timelineErr := handler.prepareTransportTimelineV3(request.Context(), &playback.Session{ID: "session-startup-failure"}, file, result)
	if timelineErr != nil {
		t.Fatalf("prepare timeline: %v", timelineErr)
	}
	transport, transportErr := handler.prepareLocalTransportV3(request, &playback.Session{ID: "session-startup-failure", UserID: 7, ProfileID: "profile-1"}, file, result, timeline, mediaAuthModeV3{})
	if transportErr == nil {
		transport.rollback()
		t.Fatal("failed ffmpeg startup returned a playable transport")
	}
	if transportErr.reason != "transcode_start_failed" || transportErr.retryable {
		t.Fatalf("transport error = %#v, want stable non-retryable startup terminal", transportErr)
	}
	if transportErr.cause == nil || !strings.Contains(transportErr.cause.Error(), "intentional startup failure") {
		t.Fatalf("startup error lost ffmpeg cause: %#v", transportErr)
	}
}

func TestManifestStartupTimeoutWhileRunningIsPersistedIdempotently(t *testing.T) {
	if playback.ManifestStartupTimeout != 30*time.Second {
		t.Fatalf("manifest startup timeout = %v, want 30s", playback.ManifestStartupTimeout)
	}
	session, err := playback.StartTranscode(context.Background(), playback.TranscodeOpts{
		InputPath:          "/media/slow.mkv",
		OutputDir:          t.TempDir(),
		SessionID:          "slow-startup",
		TargetCodecVideo:   "h264",
		TargetCodecAudio:   "aac",
		SegmentDuration:    2,
		FFmpegPath:         writePlaybackTestFFmpegNeverReady(t),
		HWAccel:            "none",
		AudioTrackIndex:    -1,
		SubtitleTrackIndex: -1,
	})
	if err != nil {
		t.Fatalf("start fake transcode: %v", err)
	}
	defer func() { _ = session.Close() }()
	_, waitErr := session.WaitForManifest(20 * time.Millisecond)
	if waitErr == nil || !session.IsRunning() {
		t.Fatalf("manifest wait err=%v running=%v, want running timeout", waitErr, session.IsRunning())
	}
	failure := manifestStartupTransportErrorV3(session.IsRunning(), waitErr)
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	request := v3HandlerStartRequest()
	requestDigests := playbackStartRequestDigestsV3{current: "digest"}
	response, err := handler.startFailureDecisionV3(context.Background(), 1, request.ProfileID, request, requestDigests, request.FileID, request.FileID, failure)
	if err != nil {
		t.Fatalf("start failure decision: %v", err)
	}
	if response.Terminal == nil || !response.Terminal.Retryable {
		t.Fatalf("timeout response = %#v, want retryable terminal", response)
	}
	record, err := handler.PlanStoreV3.GetAttemptByPlaybackAttemptID(context.Background(), request.PlaybackAttemptID)
	if err != nil || record.StartResponse.Terminal == nil || !record.StartResponse.Terminal.Retryable {
		t.Fatalf("retryable startup timeout attempt = %#v, err=%v", record, err)
	}
	replayed, err := handler.startFailureDecisionV3(context.Background(), 1, request.ProfileID, request, requestDigests, request.FileID, request.FileID, failure)
	if err != nil || replayed.Terminal == nil || replayed.Terminal.Reason != response.Terminal.Reason {
		t.Fatalf("retryable timeout replay = %#v, err=%v", replayed, err)
	}
}

func TestToneMapExecutionTransportErrorClassifiesLiveValidation(t *testing.T) {
	tests := []struct {
		name          string
		err           error
		wantRetryable bool
	}{
		{name: "stale metadata", err: tonemap.ErrSourceRevisionChanged},
		{name: "preflight rejected", err: tonemap.ErrSourcePreflightRejected},
		{name: "probe unavailable", err: playback.ErrToneMapSourceValidationUnavailable, wantRetryable: true},
		{name: "executor unavailable", err: playback.ErrToneMapExecutorUnavailable, wantRetryable: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toneMapExecutionTransportErrorV3(tt.err, "failed")
			if got.reason != transcodeStartFailedReasonV3 || got.retryable != tt.wantRetryable || !errors.Is(got.cause, tt.err) {
				t.Fatalf("error = %+v, want retryable=%t wrapping %v", got, tt.wantRetryable, tt.err)
			}
		})
	}
}

func TestRemotePlaybackTransportSanitizesNodeURLFromTransportError(t *testing.T) {
	handler := &PlaybackHandler{}
	_, _, err := handler.startRemotePlaybackTransport(
		context.Background(),
		"http://node-user:node-password@127.0.0.1:1?api_key=node-secret#node-fragment",
		transcodenode.TranscodeStartRequest{},
	)
	if err == nil {
		t.Fatal("startRemotePlaybackTransport() error = nil, want connection failure")
	}
	for _, secret := range []string{"node-user", "node-password", "node-secret", "node-fragment"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("transport error leaked %q: %v", secret, err)
		}
	}
}

func TestCombineTransportErrorsV3KeepsAnyRetryableValidationFailure(t *testing.T) {
	stale := toneMapExecutionTransportErrorV3(tonemap.ErrSourceRevisionChanged, "stale")
	transient := toneMapExecutionTransportErrorV3(playback.ErrToneMapSourceValidationUnavailable, "transient")

	mixed := combineTransportErrorsV3(stale, transient)
	if mixed == nil || !mixed.retryable || !errors.Is(mixed.cause, tonemap.ErrSourceRevisionChanged) ||
		!errors.Is(mixed.cause, playback.ErrToneMapSourceValidationUnavailable) {
		t.Fatalf("mixed error = %#v, want retryable error preserving both causes", mixed)
	}

	onlyStale := combineTransportErrorsV3(stale, toneMapExecutionTransportErrorV3(tonemap.ErrSourceRevisionChanged, "stale again"))
	if onlyStale == nil || onlyStale.retryable || !errors.Is(onlyStale.cause, tonemap.ErrSourceRevisionChanged) {
		t.Fatalf("stale errors = %#v, want permanent stale cause", onlyStale)
	}
}

func TestConfigureHLSTimelineV3MatchesTransportSeekSemantics(t *testing.T) {
	// A copy remux streams FFmpeg's growing playlist, so its seek window must
	// stay open-ended even though the runtime is known. A bounded window reads
	// as "complete", which clients treat as proof that any target inside it is
	// locally seekable — sending them past the produced head instead of back
	// to the server. The runtime belongs on source.duration_seconds.
	copyPlan := &playback.PlanV3{Timeline: playback.TimelineV3{SourceStartSeconds: 17.3}}
	configureCopyRemuxTimelineV3(copyPlan, 16)
	if copyPlan.Timeline.StreamOriginSeconds != 16 || copyPlan.Timeline.TimelineOffsetSeconds != 16 || math.Abs(copyPlan.Timeline.PlayerStartSeconds-1.3) > 0.0001 || copyPlan.Timeline.CanSeekAnywhere ||
		copyPlan.Timeline.SeekWindowStartSeconds == nil || *copyPlan.Timeline.SeekWindowStartSeconds != 16 ||
		copyPlan.Timeline.SeekWindowEndSeconds != nil ||
		copyPlan.Timeline.SeekRestoration != "source_position" {
		t.Fatalf("copy timeline=%#v", copyPlan.Timeline)
	}

	encodePlan := &playback.PlanV3{Timeline: playback.TimelineV3{SourceStartSeconds: 17.3}}
	encodeSeek, encodeSegment := configureHLSTimelineV3(encodePlan, "h264", 2, 600)
	if encodeSeek != 16 || encodeSegment != 8 || encodePlan.Timeline.StreamOriginSeconds != 0 || encodePlan.Timeline.TimelineOffsetSeconds != 0 || encodePlan.Timeline.PlayerStartSeconds != 17.3 || !encodePlan.Timeline.CanSeekAnywhere ||
		encodePlan.Timeline.SeekWindowStartSeconds != nil || encodePlan.Timeline.SeekWindowEndSeconds != nil ||
		encodePlan.Timeline.SeekRestoration != "player_position" {
		t.Fatalf("encode timeline=%#v seek=%v segment=%d", encodePlan.Timeline, encodeSeek, encodeSegment)
	}

	longEncodePlan := &playback.PlanV3{Timeline: playback.TimelineV3{SourceStartSeconds: 17.3}}
	longEncodeSeek, longEncodeSegment := configureHLSTimelineV3(longEncodePlan, "h264", 2, 1_000_000)
	if longEncodeSeek != 16 || longEncodeSegment != 8 || longEncodePlan.Timeline.StreamOriginSeconds != 16 || longEncodePlan.Timeline.TimelineOffsetSeconds != 16 || math.Abs(longEncodePlan.Timeline.PlayerStartSeconds-1.3) > 0.0001 || longEncodePlan.Timeline.CanSeekAnywhere ||
		longEncodePlan.Timeline.SeekWindowStartSeconds == nil || *longEncodePlan.Timeline.SeekWindowStartSeconds != 16 ||
		longEncodePlan.Timeline.SeekWindowEndSeconds != nil ||
		longEncodePlan.Timeline.SeekRestoration != "source_position" {
		t.Fatalf("long encode timeline=%#v seek=%v segment=%d", longEncodePlan.Timeline, longEncodeSeek, longEncodeSegment)
	}

	unknownDurationPlan := &playback.PlanV3{Timeline: playback.TimelineV3{SourceStartSeconds: 17.3}}
	unknownDurationSeek, unknownDurationSegment := configureHLSTimelineV3(unknownDurationPlan, "h264", 2, 0)
	if unknownDurationSeek != 16 || unknownDurationSegment != 8 || unknownDurationPlan.Timeline.StreamOriginSeconds != 16 || unknownDurationPlan.Timeline.TimelineOffsetSeconds != 16 || math.Abs(unknownDurationPlan.Timeline.PlayerStartSeconds-1.3) > 0.0001 || unknownDurationPlan.Timeline.CanSeekAnywhere ||
		unknownDurationPlan.Timeline.SeekWindowStartSeconds == nil || *unknownDurationPlan.Timeline.SeekWindowStartSeconds != 16 ||
		unknownDurationPlan.Timeline.SeekWindowEndSeconds != nil ||
		unknownDurationPlan.Timeline.SeekRestoration != "source_position" {
		t.Fatalf("unknown-duration timeline=%#v seek=%v segment=%d", unknownDurationPlan.Timeline, unknownDurationSeek, unknownDurationSegment)
	}
}

func TestBitmapFastStartKeepsDefaultTimelineAndSessionSegments(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	plan := &playback.PlanV3{
		Delivery: playback.DeliveryTranscodeHLSV3,
		Timeline: playback.TimelineV3{SourceStartSeconds: 658},
	}
	result := playback.PlannerResultV3{
		Plan:                 plan,
		TargetVideoCodec:     "h264",
		SubtitleBurnIn:       true,
		SubtitleCodec:        "hdmv_pgs_subtitle",
		FrozenSourceMetadata: &playback.SourceExecutionMetadataV3{DurationSeconds: 1_000_000},
	}
	timeline, timelineErr := handler.prepareTransportTimelineV3(context.Background(), &playback.Session{ID: "bitmap-fast-start"}, nil, result)
	if timelineErr != nil {
		t.Fatalf("prepare timeline: %v", timelineErr)
	}
	if timeline.seekSeconds != 658 || timeline.startSegmentNumber != 329 {
		t.Fatalf("timeline = %#v, want default segment alignment", timeline)
	}
	state := handler.v3SessionStreamState(context.Background(), &playback.Session{}, nil, result, preparedTransportV3{}, mediaAuthModeV3{})
	if state.SegmentDuration != playback.DefaultSegmentDuration {
		t.Fatalf("session segment duration = %d, want %d", state.SegmentDuration, playback.DefaultSegmentDuration)
	}
}

func TestTransportGenerationV3IsUniqueAndSessionScoped(t *testing.T) {
	first := transportGenerationV3("session-1", "plan:abcdef")
	second := transportGenerationV3("session-1", "plan:abcdef")
	if first == second || !strings.HasPrefix(first, "session-1-abcdef-") || !strings.HasPrefix(second, "session-1-abcdef-") {
		t.Fatalf("generations = %q, %q", first, second)
	}
}

func TestRemuxDVModeForPlanV3ExecutesProfile8Strip(t *testing.T) {
	plan := &playback.PlanV3{Source: playback.SourceDescriptorV3{DVProfile: 8}, Transformations: []playback.TransformationV3{{Name: "server_dv7_to_hdr10"}}}
	if got := remuxDVModeForPlanV3(plan); got != playback.RemuxDVStripToHDR10V3 {
		t.Fatalf("mode = %q", got)
	}
}

func TestHandlePlaybackRouteEventV3RejectsMalformedEvents(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/route-events", strings.NewReader(`{}`)).WithContext(newAuthorizedPlaybackContext())
	rr := httptest.NewRecorder()
	handler.HandlePlaybackRouteEventV3(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
}

func TestHandlePlaybackRouteEventV3AuthorizesPersistedTerminalStart(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	store := &recordingRouteEventPlanStoreV3{
		PlanStoreV3: handler.PlanStoreV3,
		events:      make(chan playback.RouteEventRecordV3, 1),
	}
	handler.PlanStoreV3 = store
	if err := handler.PlanStoreV3.SaveAttempt(context.Background(), playback.AttemptRecordV3{
		PlaybackAttemptID:    "attempt-terminal-0001",
		UserID:               1,
		ProfileID:            "profile-1",
		RequestedMediaFileID: 42,
		EffectiveMediaFileID: 42,
		StartResponse:        playback.NewTerminalResponseV3("no_alternate_version", "No compatible version is available.", false),
		RequestDigest:        "digest-terminal",
		ExpiresAt:            time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	event := playback.RouteEventV3{
		ProtocolVersion:   playback.ProtocolV3,
		PlaybackAttemptID: "attempt-terminal-0001",
		Event:             playback.RouteEventTerminalV3,
		FallbackReason:    "no_alternate_version",
		OutputContextID:   "shield-hdmi-1",
		Diagnostics:       map[string]string{"reason": "hlg_output_unsupported"},
	}
	body, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/route-events", strings.NewReader(string(body))).WithContext(newAuthorizedPlaybackContext())
	rr := httptest.NewRecorder()
	handler.HandlePlaybackRouteEventV3(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	select {
	case stored := <-store.events:
		if stored.PlaybackAttemptID != event.PlaybackAttemptID || stored.SessionID != "" || stored.Event != playback.RouteEventTerminalV3 || stored.UserID != 1 || stored.ProfileID != "profile-1" {
			t.Fatalf("stored terminal route event = %#v", stored)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal route event was not persisted")
	}

	// A terminal attempt has no plan, so it cannot authorize a plan-scoped
	// diagnostic even when the attempt ownership matches.
	event.Event = playback.RouteEventPlanFailedV3
	body, err = json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/playback/route-events", strings.NewReader(string(body))).WithContext(newAuthorizedPlaybackContext())
	rr = httptest.NewRecorder()
	handler.HandlePlaybackRouteEventV3(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("plan event for terminal attempt status = %d, body = %s", rr.Code, rr.Body.String())
	}

	// Unknown attempts cannot be attributed to the authenticated profile, even
	// when they claim to describe a terminal start.
	event.Event = playback.RouteEventTerminalV3
	event.PlaybackAttemptID = "attempt-terminal-unknown"
	body, err = json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/playback/route-events", strings.NewReader(string(body))).WithContext(newAuthorizedPlaybackContext())
	rr = httptest.NewRecorder()
	handler.HandlePlaybackRouteEventV3(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("unknown terminal status = %d, body = %s", rr.Code, rr.Body.String())
	}
}

func TestSanitizeDiagnosticsV3PreservesPlayerFailureEvidence(t *testing.T) {
	got := sanitizeDiagnosticsV3(map[string]string{
		"error_code":                     "2004",
		"error_code_name":                "ERROR_CODE_PARSING_CONTAINER_MALFORMED",
		"error_cause":                    "ParserException",
		"network_transport":              "wifi",
		"network_metered":                "true",
		"network_validated":              "true",
		"bandwidth_estimate_kbps":        "3500",
		"link_downstream_kbps":           "5000",
		"target_source_position_seconds": "321.5",
		"reason":                         "seek_reanchor",
		"message":                        "must not be persisted",
	})
	if got["error_code"] != "2004" || got["error_code_name"] == "" || got["error_cause"] != "ParserException" {
		t.Fatalf("failure diagnostics = %#v", got)
	}
	if _, ok := got["message"]; ok {
		t.Fatalf("unapproved message persisted: %#v", got)
	}
	for _, key := range []string{"network_transport", "network_metered", "network_validated", "bandwidth_estimate_kbps", "link_downstream_kbps", "target_source_position_seconds", "reason"} {
		if got[key] == "" {
			t.Errorf("client diagnostic %q was stripped: %#v", key, got)
		}
	}
}

func TestRouteEventV3AcceptsAndroidSeekEvents(t *testing.T) {
	base := playback.RouteEventV3{
		ProtocolVersion:   playback.ProtocolV3,
		PlaybackAttemptID: "attempt-route-0001",
		OutputContextID:   "route-1",
	}
	for _, event := range []string{playback.RouteEventSeekReanchorRequestedV3, playback.RouteEventSeekReanchoredV3} {
		candidate := base
		candidate.Event = event
		if !validRouteEventV3(candidate) {
			t.Errorf("Android route event %q was rejected", event)
		}
	}
}

func TestRemapSubtitleSelectionV3RejectsNegativeIndex(t *testing.T) {
	index := -1
	request := playback.StartRequestV3{SubtitleTrackIndex: &index}
	source := &models.MediaFile{ID: 1, ExternalSubtitles: []models.ExternalSubtitle{{Language: "eng", Format: "srt"}}}
	target := &models.MediaFile{ID: 2, ExternalSubtitles: []models.ExternalSubtitle{{Language: "eng", Format: "srt"}}}
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	if err := handler.remapSubtitleSelectionV3(context.Background(), source, target, &request); err == nil {
		t.Fatal("negative subtitle index was accepted")
	}
}

func TestRouteEventV3HasPerUserLimitAcrossAttemptIDs(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	for i := 0; i < 600; i++ {
		attemptID := "attempt-" + strconv.Itoa(i/100)
		if !handler.allowRouteEventV3(7, attemptID) {
			t.Fatalf("event %d was rejected before the user limit", i)
		}
	}
	if handler.allowRouteEventV3(7, "attempt-rotated") {
		t.Fatal("rotating attempt IDs bypassed the per-user limit")
	}
}

func v3HandlerFixtureFile(t *testing.T) *models.MediaFile {
	t.Helper()
	return &models.MediaFile{ID: 42, ContentID: "movie-1", FilePath: writePlaybackTestMediaFile(t, "movie.mp4"), Container: "mp4", CodecVideo: "h264", CodecAudio: "aac", Resolution: "1080p", Bitrate: 8_000, AudioChannels: 2, Duration: 3600, VideoTracks: []models.VideoTrack{{Codec: "h264", Profile: "high", Level: 41, Width: 1920, Height: 1080, FrameRate: "24000/1001", Bitrate: 8_000, BitDepth: 8, VideoRange: "SDR", VideoRangeType: "SDR"}}, AudioTracks: []models.AudioTrack{{Codec: "aac", Channels: 2, Layout: "stereo", Default: true}}}
}

func stubCopySeekAnchorV3(handler *PlaybackHandler) {
	handler.copySeekAnchor = func(_ context.Context, _ string, _ string, requested float64, segmentDuration int) (float64, int, error) {
		return requested, computeStartSegment(requested, segmentDuration), nil
	}
}

func v3HandlerStartRequest() playback.StartRequestV3 {
	return playback.StartRequestV3{ProtocolVersion: playback.ProtocolV3, ClientFeatures: []string{playback.FeaturePlaybackPlanV3}, FileID: 42, ProfileID: "profile-1", PlaybackAttemptID: "attempt-handler-0001", QualityPreference: "original", SubtitleFidelityPreference: playback.SubtitleFidelityCompatibleV3, Capabilities: playback.ClientCodecCapabilitiesV3{VideoEvidence: playback.EvidenceExactV3, AudioEvidence: playback.EvidenceExactV3, CodecsVideo: []string{"h264"}, CodecsVideoHardware: []string{"h264"}, CodecsAudio: []string{"aac"}, Containers: []string{"mp4"}, MaxResolution: "1080p", VideoDecode: []playback.VideoDecodeCapabilityV3{{Codec: "h264", Profiles: []string{"high"}, Levels: []int{41}, BitDepths: []int{8}, MaxWidth: 1920, MaxHeight: 1080, MaxFrameRate: 60, MaxBitrateKbps: 20_000, Hardware: true}}}, ClientPlaybackContext: playback.ClientPlaybackContextV3{ProtocolVersion: playback.ProtocolV3, FormFactor: "tv", AppVersion: "test", Device: playback.DeviceContextV3{Platform: "android"}, Output: playback.OutputContextV3{OutputContextID: "route-1"}, Deliveries: map[string]playback.DeliveryCapabilityV3{playback.DeliveryClassOriginalHTTPV3: {Enabled: true, SupportedOnDevice: true, Subtitles: playback.DeliverySubtitleCapabilitiesV3{EmbeddedText: true, SidecarText: true}}}}}
}

func TestHandleReplanPlaybackV3PreservesOmittedSubtitleAndReportsUnavailableInFallbackVersion(t *testing.T) {
	source := v3HandlerFixtureFile(t)
	source.Resolution = "2160p"
	source.Bitrate = 32_000
	source.VideoTracks = append([]models.VideoTrack(nil), source.VideoTracks...)
	source.VideoTracks[0].Level = 51
	source.VideoTracks[0].Width = 3840
	source.VideoTracks[0].Height = 2160
	source.VideoTracks[0].Bitrate = 32_000
	source.ExternalSubtitles = []models.ExternalSubtitle{{Path: writePlaybackTestMediaFile(t, "movie.eng.ass"), Language: "eng", Format: "ass"}}
	alternateValue := *source
	alternate := &alternateValue
	alternate.ID = 84
	alternate.Resolution = "1080p"
	alternate.Bitrate = 8_000
	alternate.VideoTracks = append([]models.VideoTrack(nil), source.VideoTracks...)
	alternate.VideoTracks[0].Level = 41
	alternate.VideoTracks[0].Width = 1920
	alternate.VideoTracks[0].Height = 1080
	alternate.VideoTracks[0].Bitrate = 8_000
	alternate.ExternalSubtitles = nil

	files := map[int]*models.MediaFile{source.ID: source, alternate.ID: alternate}
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), mapPlaybackFileResolver{files: files})
	handler.FileVersionFetcher = testPlaybackFileVersionFetcher{byContent: map[string][]*models.MediaFile{source.ContentID: {source, alternate}}}
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "false"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	startRequest := v3HandlerStartRequest()
	startRequest.QualityPreference = "auto"
	startRequest.Capabilities.MaxResolution = "2160p"
	startRequest.Capabilities.VideoDecode[0].Levels = []int{51}
	startRequest.Capabilities.VideoDecode[0].MaxWidth = 3840
	startRequest.Capabilities.VideoDecode[0].MaxHeight = 2160
	startRequest.Capabilities.VideoDecode[0].MaxBitrateKbps = 50_000
	subtitleIndex := 0
	startRequest.SubtitleTrackID = playback.TrackIDV3(source.ID, "subtitle", subtitleIndex)
	startRequest.SubtitleTrackIndex = &subtitleIndex
	startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassOriginalHTTPV3] = playback.DeliveryCapabilityV3{
		Enabled: true, SupportedOnDevice: true,
		Subtitles: playback.DeliverySubtitleCapabilitiesV3{SidecarText: true, ASSStyling: true},
	}
	startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3] = playback.DeliveryCapabilityV3{
		Enabled: true, SupportedOnDevice: true,
		Subtitles: playback.DeliverySubtitleCapabilitiesV3{SidecarText: true, ASSStyling: true},
	}

	startReq := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, startRequest))).WithContext(newAuthorizedPlaybackContext())
	startRR := httptest.NewRecorder()
	handler.HandleStartPlayback(startRR, startReq)
	if startRR.Code != http.StatusCreated {
		t.Fatalf("start status = %d, body = %s", startRR.Code, startRR.Body.String())
	}
	var started playback.DecisionResponseV3
	if err := json.Unmarshal(startRR.Body.Bytes(), &started); err != nil || started.PlaybackPlan == nil {
		t.Fatalf("start response: err=%v response=%#v", err, started)
	}
	currentKey := playback.PlanAttemptKeyV3(*started.PlaybackPlan, startRequest.ClientPlaybackContext.Output.OutputContextID, nil)
	response := postPlaybackReplanV3(t, handler, started.SessionID, playback.ReplanRequestV3{
		ProtocolVersion: playback.ProtocolV3, Operation: playback.ReplanOperationQualityChangeV3,
		PlaybackAttemptID: startRequest.PlaybackAttemptID,
		ReplanRequestID:   "subtitle-version-fallback-0001", FailedPlanID: started.PlaybackPlan.PlanID,
		PlanAttemptID: "subtitle-version-attempt-0001", PlanAttemptKey: currentKey,
		AttemptedPlanKeys: []string{currentKey}, AttemptCount: 1, QualityPreference: "1080p",
		// A non-track-change replan may omit an unchanged subtitle identity.
		// The server must preserve it, then fail explicitly because the 1080p
		// fallback has no equivalent track. Clearing it would incorrectly make
		// the alternate version playable with subtitles off.
		SelectedTracks:        playback.SelectedTracksV3{Audio: started.PlaybackPlan.SelectedTracks.Audio},
		Capabilities:          startRequest.Capabilities,
		ClientPlaybackContext: startRequest.ClientPlaybackContext,
	})
	if response.Terminal == nil || response.Terminal.Reason != "subtitle_unavailable_in_version" || response.Terminal.Retryable {
		t.Fatalf("fallback terminal = %#v, plan = %#v", response.Terminal, response.PlaybackPlan)
	}
	record, err := handler.PlanStoreV3.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if record.NormalizedRequest.SubtitleTrackID != startRequest.SubtitleTrackID || record.CurrentPlan.SelectedTracks.Subtitle == nil {
		t.Fatalf("terminal fallback changed durable subtitle selection: %#v", record)
	}
}

func TestHandleReplanPlaybackV3BitmapSubtitleFallsBackFromHDRToSDRVersion(t *testing.T) {
	source := v3HandlerFixtureFile(t)
	source.Container = "mkv"
	source.FilePath = writePlaybackTestMediaFile(t, "movie-4k.mkv")
	source.CodecVideo = "hevc"
	source.Resolution = "2160p"
	source.Bitrate = 64_000
	source.VideoTracks = []models.VideoTrack{{
		Codec: "hevc", Profile: "main 10", Level: 153, Width: 3840, Height: 2160,
		FrameRate: "24000/1001", Bitrate: 64_000, BitDepth: 10,
		VideoRange: "HDR10", VideoRangeType: "HDR10",
	}}
	source.SubtitleTracks = []models.SubtitleTrack{{Index: 4, Codec: "hdmv_pgs_subtitle", Language: "eng", Title: "English"}}

	alternateValue := *source
	alternate := &alternateValue
	alternate.ID = 84
	alternate.FilePath = writePlaybackTestMediaFile(t, "movie-1080p.mkv")
	alternate.CodecVideo = "h264"
	alternate.Resolution = "1080p"
	alternate.Bitrate = 8_000
	alternate.VideoTracks = []models.VideoTrack{{
		Codec: "h264", Profile: "high", Level: 41, Width: 1920, Height: 1080,
		FrameRate: "24000/1001", Bitrate: 8_000, BitDepth: 8,
		VideoRange: "SDR", VideoRangeType: "SDR",
	}}

	files := map[int]*models.MediaFile{source.ID: source, alternate.ID: alternate}
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, mapPlaybackFileResolver{files: files})
	handler.FileVersionFetcher = testPlaybackFileVersionFetcher{byContent: map[string][]*models.MediaFile{
		source.ContentID: {source, alternate},
	}}
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"transcode_enabled": "true", "allow_4k_transcode": "true"}}
	handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpeg(t), t.TempDir())
	presetLocalRegistryV3(handler, playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{
		{Name: playback.TransformationAudioToAACV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3, Available: true},
		{Name: playback.TransformationVideoToH264V3, RecipeVersion: "2", Available: true},
	}))
	handler.ItemAccess = allowAllPlaybackItemAccess{}

	startRequest := v3HandlerStartRequest()
	startRequest.QualityPreference = "auto"
	startRequest.Capabilities.CodecsVideo = []string{"hevc", "h264"}
	startRequest.Capabilities.CodecsVideoHardware = []string{"hevc", "h264"}
	startRequest.Capabilities.Containers = []string{"mkv", "hls"}
	startRequest.Capabilities.MaxResolution = "2160p"
	startRequest.Capabilities.VideoDecode = []playback.VideoDecodeCapabilityV3{{
		Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10},
		MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true,
	}}
	hdr := &playback.HDRCapabilitiesV3{HDR10: true}
	startRequest.Capabilities.HDRDetails = hdr
	startRequest.ClientPlaybackContext.Output.HDRDetails = hdr
	startRequest.ClientPlaybackContext.Deliveries = map[string]playback.DeliveryCapabilityV3{
		playback.DeliveryClassOriginalHTTPV3: {
			Enabled: true, SupportedOnDevice: true, Containers: []string{"mkv"},
			VideoCodecs: []string{"hevc", "h264"}, AudioDecodeCodecs: []string{"aac"},
		},
		playback.DeliveryClassHLSV3: {
			Enabled: true, SupportedOnDevice: true, Containers: []string{"hls"},
			VideoCodecs: []string{"h264"}, AudioDecodeCodecs: []string{"aac"},
		},
	}

	startRR := httptest.NewRecorder()
	handler.HandleStartPlayback(startRR, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, startRequest))).WithContext(newAuthorizedPlaybackContext()))
	var started playback.DecisionResponseV3
	if startRR.Code != http.StatusCreated || json.Unmarshal(startRR.Body.Bytes(), &started) != nil || started.PlaybackPlan == nil {
		t.Fatalf("start status=%d body=%s", startRR.Code, startRR.Body.String())
	}
	if started.PlaybackPlan.EffectiveMediaFileID != source.ID {
		t.Fatalf("start effective file = %d, want HDR source %d", started.PlaybackPlan.EffectiveMediaFileID, source.ID)
	}

	subtitleIndex := 0
	replanned := postPlaybackReplanV3(t, handler, started.SessionID, playback.ReplanRequestV3{
		ProtocolVersion: playback.ProtocolV3, Operation: playback.ReplanOperationTrackChangeV3,
		PlaybackAttemptID: startRequest.PlaybackAttemptID, ReplanRequestID: "hdr-subtitle-fallback-0001",
		FailedPlanID: started.PlaybackPlan.PlanID, PlanAttemptID: "hdr-subtitle-attempt-0001",
		PlanAttemptKey: started.PlaybackPlan.PlanAttemptKey, AttemptCount: 1, PositionSeconds: 30,
		QualityPreference: "auto",
		SelectedTracks: playback.SelectedTracksV3{
			Audio: started.PlaybackPlan.SelectedTracks.Audio,
			Subtitle: &playback.TrackIdentityV3{
				ID: playback.TrackIDV3(source.ID, "subtitle", subtitleIndex), Index: &subtitleIndex,
			},
		},
		Capabilities: startRequest.Capabilities, ClientPlaybackContext: startRequest.ClientPlaybackContext,
	})
	if replanned.PlaybackPlan == nil || replanned.Terminal != nil {
		t.Fatalf("subtitle replan = %#v", replanned)
	}
	if replanned.PlaybackPlan.EffectiveMediaFileID != alternate.ID {
		t.Fatalf("subtitle effective file = %d, want SDR alternate %d", replanned.PlaybackPlan.EffectiveMediaFileID, alternate.ID)
	}
	if replanned.PlaybackPlan.Subtitle.Mode != playback.SubtitleBurnInV3 || replanned.PlaybackPlan.SelectedTracks.Subtitle == nil {
		t.Fatalf("subtitle plan = %#v, want burn-in selection", replanned.PlaybackPlan)
	}
	t.Cleanup(func() { handler.tm.CloseTranscodeSession(started.SessionID, "") })
}

func TestHandleReplanPlaybackV3TrackChangeStaysOnEffectiveAlternate(t *testing.T) {
	source := v3HandlerFixtureFile(t)
	source.Resolution = "2160p"
	source.Bitrate = 32_000
	source.VideoTracks[0].Width, source.VideoTracks[0].Height = 3840, 2160
	source.VideoTracks[0].Level, source.VideoTracks[0].Bitrate = 51, 32_000
	alternateValue := *source
	alternate := &alternateValue
	alternate.ID, alternate.Resolution, alternate.Bitrate = 84, "1080p", 8_000
	alternate.VideoTracks = append([]models.VideoTrack(nil), source.VideoTracks...)
	alternate.VideoTracks[0].Width, alternate.VideoTracks[0].Height = 1920, 1080
	alternate.VideoTracks[0].Level, alternate.VideoTracks[0].Bitrate = 41, 8_000
	alternate.AudioTracks = append([]models.AudioTrack(nil), source.AudioTracks...)
	alternate.AudioTracks = append(alternate.AudioTracks, models.AudioTrack{Codec: "aac", Channels: 2, Layout: "stereo", Language: "spa"})
	alternate.ExternalSubtitles = []models.ExternalSubtitle{{Path: writePlaybackTestMediaFile(t, "alternate.eng.ass"), Language: "eng", Format: "ass"}}

	files := map[int]*models.MediaFile{source.ID: source, alternate.ID: alternate}
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, mapPlaybackFileResolver{files: files})
	stubCopySeekAnchorV3(handler)
	handler.FileVersionFetcher = testPlaybackFileVersionFetcher{byContent: map[string][]*models.MediaFile{source.ContentID: {source, alternate}}}
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "false"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	startRequest := v3HandlerStartRequest()
	startRequest.QualityPreference = "auto"
	startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassProgressiveV3] = playback.DeliveryCapabilityV3{Enabled: true, SupportedOnDevice: true}
	startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3] = playback.DeliveryCapabilityV3{Enabled: true, SupportedOnDevice: true}
	startRR := httptest.NewRecorder()
	handler.HandleStartPlayback(startRR, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, startRequest))).WithContext(newAuthorizedPlaybackContext()))
	var started playback.DecisionResponseV3
	if startRR.Code != http.StatusCreated || json.Unmarshal(startRR.Body.Bytes(), &started) != nil || started.PlaybackPlan == nil || started.PlaybackPlan.EffectiveMediaFileID != alternate.ID {
		t.Fatalf("alternate start status=%d body=%s", startRR.Code, startRR.Body.String())
	}
	audioIndex := 1
	response := postPlaybackReplanV3(t, handler, started.SessionID, playback.ReplanRequestV3{
		ProtocolVersion: playback.ProtocolV3, Operation: playback.ReplanOperationTrackChangeV3,
		PlaybackAttemptID: startRequest.PlaybackAttemptID, ReplanRequestID: "alternate-track-change-0001",
		FailedPlanID: started.PlaybackPlan.PlanID, PlanAttemptID: "alternate-track-attempt-0001",
		PlanAttemptKey: started.PlaybackPlan.PlanAttemptKey, AttemptCount: 1, PositionSeconds: 30,
		SelectedTracks: playback.SelectedTracksV3{Audio: &playback.TrackIdentityV3{Index: &audioIndex}},
		Capabilities:   startRequest.Capabilities, ClientPlaybackContext: startRequest.ClientPlaybackContext,
	})
	if response.PlaybackPlan == nil || response.PlaybackPlan.EffectiveMediaFileID != alternate.ID {
		t.Fatalf("track change abandoned effective alternate: %#v", response)
	}
	if response.PlaybackPlan.SelectedTracks.Audio == nil || response.PlaybackPlan.SelectedTracks.Audio.Index == nil || *response.PlaybackPlan.SelectedTracks.Audio.Index != audioIndex {
		t.Fatalf("alternate audio selection = %#v", response.PlaybackPlan.SelectedTracks.Audio)
	}

	subtitleIndex := 0
	withSubtitle := postPlaybackReplanV3(t, handler, started.SessionID, playback.ReplanRequestV3{
		ProtocolVersion: playback.ProtocolV3, Operation: playback.ReplanOperationTrackChangeV3,
		PlaybackAttemptID: startRequest.PlaybackAttemptID, ReplanRequestID: "alternate-subtitle-change-0001",
		FailedPlanID: response.PlaybackPlan.PlanID, PlanAttemptID: "alternate-subtitle-attempt-0001",
		PlanAttemptKey: response.PlaybackPlan.PlanAttemptKey, AttemptCount: 1, PositionSeconds: 31,
		SelectedTracks: playback.SelectedTracksV3{
			Audio:    response.PlaybackPlan.SelectedTracks.Audio,
			Subtitle: &playback.TrackIdentityV3{Index: &subtitleIndex},
		},
		Capabilities: startRequest.Capabilities, ClientPlaybackContext: startRequest.ClientPlaybackContext,
	})
	if withSubtitle.PlaybackPlan == nil || withSubtitle.PlaybackPlan.EffectiveMediaFileID != alternate.ID || withSubtitle.PlaybackPlan.SelectedTracks.Subtitle == nil {
		t.Fatalf("alternate subtitle selection failed: %#v", withSubtitle)
	}

	upgradedCapabilities := startRequest.Capabilities
	upgradedCapabilities.MaxResolution = "2160p"
	upgradedCapabilities.VideoDecode = append([]playback.VideoDecodeCapabilityV3(nil), startRequest.Capabilities.VideoDecode...)
	upgradedCapabilities.VideoDecode[0].Levels = []int{51}
	upgradedCapabilities.VideoDecode[0].MaxWidth = 3840
	upgradedCapabilities.VideoDecode[0].MaxHeight = 2160
	upgradedCapabilities.VideoDecode[0].MaxBitrateKbps = 50_000
	outputResponse := postPlaybackReplanV3(t, handler, started.SessionID, playback.ReplanRequestV3{
		ProtocolVersion: playback.ProtocolV3, Operation: playback.ReplanOperationOutputChangeV3,
		PlaybackAttemptID: startRequest.PlaybackAttemptID, ReplanRequestID: "alternate-output-change-0001",
		FailedPlanID: withSubtitle.PlaybackPlan.PlanID, PlanAttemptID: "alternate-output-attempt-0001",
		PlanAttemptKey: withSubtitle.PlaybackPlan.PlanAttemptKey, AttemptCount: 1, PositionSeconds: 32,
		SelectedTracks:        withSubtitle.PlaybackPlan.SelectedTracks,
		Capabilities:          upgradedCapabilities,
		ClientPlaybackContext: startRequest.ClientPlaybackContext,
	})
	if outputResponse.PlaybackPlan == nil || outputResponse.PlaybackPlan.EffectiveMediaFileID != alternate.ID {
		t.Fatalf("output change abandoned alternate-only subtitle: %#v", outputResponse)
	}
	if outputResponse.PlaybackPlan.SelectedTracks.Subtitle == nil || !strings.HasPrefix(outputResponse.PlaybackPlan.SelectedTracks.Subtitle.ID, "file:84:subtitle:") {
		t.Fatalf("output change lost alternate subtitle: %#v", outputResponse.PlaybackPlan.SelectedTracks.Subtitle)
	}
}

func TestHandleReplanPlaybackV3OutputChangeRetriesActiveAlternateAfterRequestedTerminal(t *testing.T) {
	source := v3HandlerFixtureFile(t)
	source.Resolution, source.Bitrate = "2160p", 32_000
	source.VideoTracks = append([]models.VideoTrack(nil), source.VideoTracks...)
	source.VideoTracks[0].Width, source.VideoTracks[0].Height = 3840, 2160
	source.VideoTracks[0].Level, source.VideoTracks[0].Bitrate = 51, 32_000

	alternateValue := *source
	alternate := &alternateValue
	alternate.ID, alternate.Container, alternate.Resolution, alternate.Bitrate = 84, "mp4", "1080p", 8_000
	alternate.VideoTracks = append([]models.VideoTrack(nil), source.VideoTracks...)
	alternate.VideoTracks[0].Width, alternate.VideoTracks[0].Height = 1920, 1080
	alternate.VideoTracks[0].Level, alternate.VideoTracks[0].Bitrate = 41, 8_000

	files := map[int]*models.MediaFile{source.ID: source, alternate.ID: alternate}
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), mapPlaybackFileResolver{files: files})
	stubCopySeekAnchorV3(handler)
	handler.FileVersionFetcher = testPlaybackFileVersionFetcher{byContent: map[string][]*models.MediaFile{source.ContentID: {source, alternate}}}
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "false"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	startRequest := v3HandlerStartRequest()
	startRequest.QualityPreference = "auto"
	startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassProgressiveV3] = playback.DeliveryCapabilityV3{
		Enabled: true, SupportedOnDevice: true, Containers: []string{"mp4"},
		VideoCodecs: []string{"h264"}, AudioDecodeCodecs: []string{"aac"},
	}
	startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3] = playback.DeliveryCapabilityV3{
		Enabled: true, SupportedOnDevice: true, Containers: []string{"hls"},
		VideoCodecs: []string{"h264"}, AudioDecodeCodecs: []string{"aac"},
	}

	startRR := httptest.NewRecorder()
	handler.HandleStartPlayback(startRR, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, startRequest))).WithContext(newAuthorizedPlaybackContext()))
	var started playback.DecisionResponseV3
	if startRR.Code != http.StatusCreated || json.Unmarshal(startRR.Body.Bytes(), &started) != nil || started.PlaybackPlan == nil || started.PlaybackPlan.EffectiveMediaFileID != alternate.ID {
		t.Fatalf("alternate start status=%d body=%s", startRR.Code, startRR.Body.String())
	}
	// The inactive requested edition can acquire a different constraint before
	// an output refresh (for example after a rescan). Its speculative terminal
	// must not displace the already-playing alternate.
	source.Container = "mkv"
	source.CodecVideo = "av1"
	source.VideoTracks[0].Codec = "av1"
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"transcode_enabled": "false", "allow_4k_transcode": "false"}}

	upgradedCapabilities := startRequest.Capabilities
	upgradedCapabilities.MaxResolution = "2160p"
	upgradedCapabilities.VideoDecode = append([]playback.VideoDecodeCapabilityV3(nil), startRequest.Capabilities.VideoDecode...)
	upgradedCapabilities.VideoDecode[0].Levels = []int{51}
	upgradedCapabilities.VideoDecode[0].MaxWidth = 3840
	upgradedCapabilities.VideoDecode[0].MaxHeight = 2160
	upgradedCapabilities.VideoDecode[0].MaxBitrateKbps = 50_000
	response := postPlaybackReplanV3(t, handler, started.SessionID, playback.ReplanRequestV3{
		ProtocolVersion: playback.ProtocolV3, Operation: playback.ReplanOperationOutputChangeV3,
		PlaybackAttemptID: startRequest.PlaybackAttemptID, ReplanRequestID: "alternate-terminal-output-0001",
		FailedPlanID: started.PlaybackPlan.PlanID, PlanAttemptID: "alternate-terminal-attempt-0001",
		PlanAttemptKey: started.PlaybackPlan.PlanAttemptKey, AttemptCount: 1, PositionSeconds: 33,
		SelectedTracks:        started.PlaybackPlan.SelectedTracks,
		Capabilities:          upgradedCapabilities,
		ClientPlaybackContext: startRequest.ClientPlaybackContext,
	})
	if response.Terminal != nil || response.PlaybackPlan == nil || response.PlaybackPlan.EffectiveMediaFileID != alternate.ID {
		t.Fatalf("requested-edition terminal abandoned active alternate: %#v", response)
	}
}

func marshalV3StartRequest(t *testing.T, request playback.StartRequestV3) string {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// track_change is a user operation, not a failure: no classification is sent,
// the previous route stays eligible, and duplicates replay idempotently.
func TestHandleReplanPlaybackV3TrackChangeOperation(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	file.AudioTracks = append(file.AudioTracks, models.AudioTrack{Codec: "aac", Channels: 2, Layout: "stereo", Language: "spa"})
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	stubCopySeekAnchorV3(handler)
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "true"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	startRequest := v3HandlerStartRequest()
	startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassProgressiveV3] = playback.DeliveryCapabilityV3{Enabled: true, SupportedOnDevice: true}
	startReq := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, startRequest))).WithContext(newAuthorizedPlaybackContext())
	startRR := httptest.NewRecorder()
	handler.HandleStartPlayback(startRR, startReq)
	if startRR.Code != http.StatusCreated {
		t.Fatalf("start status = %d, body = %s", startRR.Code, startRR.Body.String())
	}
	var started playback.DecisionResponseV3
	if err := json.Unmarshal(startRR.Body.Bytes(), &started); err != nil || started.PlaybackPlan == nil {
		t.Fatalf("start response: err=%v response=%#v", err, started)
	}
	audioIndex := 1
	currentKey := playback.PlanAttemptKeyV3(*started.PlaybackPlan, startRequest.ClientPlaybackContext.Output.OutputContextID, nil)
	replan := playback.ReplanRequestV3{
		ProtocolVersion:   playback.ProtocolV3,
		Operation:         playback.ReplanOperationTrackChangeV3,
		PlaybackAttemptID: startRequest.PlaybackAttemptID,
		ReplanRequestID:   "track-change-0001",
		FailedPlanID:      started.PlaybackPlan.PlanID,
		PlanAttemptID:     "plan-attempt-track-0001",
		PlanAttemptKey:    currentKey,
		// A defensive echo of the current key must not push the current route
		// off the table: nothing failed.
		AttemptedPlanKeys:     []string{currentKey},
		AttemptCount:          1,
		PositionSeconds:       45,
		SelectedTracks:        playback.SelectedTracksV3{Audio: &playback.TrackIdentityV3{ID: playback.TrackIDV3(file.ID, "audio", audioIndex), Index: &audioIndex}},
		Capabilities:          startRequest.Capabilities,
		ClientPlaybackContext: startRequest.ClientPlaybackContext,
	}
	body, err := json.Marshal(replan)
	if err != nil {
		t.Fatal(err)
	}
	call := func() playback.DecisionResponseV3 {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/"+started.SessionID+"/replan", strings.NewReader(string(body))).WithContext(newAuthorizedPlaybackContext())
		req = withPlaybackRouteParam(req, "session_id", started.SessionID)
		rr := httptest.NewRecorder()
		handler.HandleReplanPlaybackV3(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("track change status = %d, body = %s", rr.Code, rr.Body.String())
		}
		var response playback.DecisionResponseV3
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	first := call()
	second := call()
	if first.PlaybackPlan == nil || second.PlaybackPlan == nil || first.PlaybackPlan.PlanID != second.PlaybackPlan.PlanID {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	if first.PlaybackPlan.Delivery != playback.DeliveryRemuxProgressiveV3 {
		t.Fatalf("track change did not map the non-default audio stream: %#v", first.PlaybackPlan)
	}
	if first.PlaybackPlan.SelectedTracks.Audio == nil || first.PlaybackPlan.SelectedTracks.Audio.Index == nil || *first.PlaybackPlan.SelectedTracks.Audio.Index != audioIndex {
		t.Fatalf("selected tracks = %#v", first.PlaybackPlan.SelectedTracks)
	}
	session, err := manager.GetSession(started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if session.AudioTrackIndex != audioIndex {
		t.Fatalf("audio index = %d, want %d", session.AudioTrackIndex, audioIndex)
	}
	record, err := handler.PlanStoreV3.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	// A track change without a quality field keeps the durable quality intact.
	if record.NormalizedRequest.QualityPreference != "original" {
		t.Fatalf("quality after track change = %q", record.NormalizedRequest.QualityPreference)
	}
}

func TestHandleReplanPlaybackV3SidecarChangeReusesCopyHLSTransport(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	file.Container = "mkv"
	file.FilePath = writePlaybackTestMediaFile(t, "movie.mkv")
	file.ExternalSubtitles = []models.ExternalSubtitle{
		{Path: writePlaybackTestMediaFile(t, "movie.de.srt"), Language: "de", Format: "srt"},
		{Path: writePlaybackTestMediaFile(t, "movie.en.srt"), Language: "en", Format: "srt"},
	}
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpeg(t), t.TempDir())
	presetLocalRegistryV3(handler, playback.NewTransformationRegistryV3(nil))
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "true"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}

	startRequest := v3HandlerStartRequest()
	startRequest.Capabilities.Containers = []string{"m3u8"}
	startRequest.ClientPlaybackContext.Deliveries = map[string]playback.DeliveryCapabilityV3{
		playback.DeliveryClassHLSV3: {
			Enabled: true, SupportedOnDevice: true,
			Subtitles: playback.DeliverySubtitleCapabilitiesV3{SidecarText: true},
		},
	}
	german := 0
	startRequest.SubtitleTrackID = playback.TrackIDV3(file.ID, "subtitle", german)
	startRequest.SubtitleTrackIndex = &german
	startRR := httptest.NewRecorder()
	handler.HandleStartPlayback(startRR, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, startRequest))).WithContext(newAuthorizedPlaybackContext()))
	var started playback.DecisionResponseV3
	if startRR.Code != http.StatusCreated || json.Unmarshal(startRR.Body.Bytes(), &started) != nil || started.PlaybackPlan == nil {
		t.Fatalf("start status=%d body=%s", startRR.Code, startRR.Body.String())
	}
	if started.PlaybackPlan.Delivery != playback.DeliveryRemuxHLSV3 || started.PlaybackPlan.Subtitle.Mode != playback.SubtitleRenderV3 {
		t.Fatalf("start plan = %#v, want copy HLS with sidecar subtitle", started.PlaybackPlan)
	}
	before := handler.tm.GetTranscodeSession(started.SessionID)
	if before == nil {
		t.Fatal("start created no local HLS transport")
	}
	t.Cleanup(func() { handler.tm.CloseTranscodeSession(started.SessionID, "") })
	beforeOpts := before.Opts()
	beforeTimeline := started.PlaybackPlan.Timeline
	beforeURL := started.PlaybackPlan.Stream.URL
	before.ReportSegmentDownloaded(25)
	beforeRequestedSegment := before.LastRequestedSegment()

	english := 1
	replanned := postPlaybackReplanV3(t, handler, started.SessionID, playback.ReplanRequestV3{
		ProtocolVersion:       playback.ProtocolV3,
		Operation:             playback.ReplanOperationTrackChangeV3,
		PlaybackAttemptID:     startRequest.PlaybackAttemptID,
		ReplanRequestID:       "sidecar-track-change-0001",
		FailedPlanID:          started.PlaybackPlan.PlanID,
		PlanAttemptID:         "sidecar-plan-attempt-0001",
		PlanAttemptKey:        started.PlaybackPlan.PlanAttemptKey,
		AttemptCount:          1,
		PositionSeconds:       120,
		SelectedTracks:        playback.SelectedTracksV3{Audio: started.PlaybackPlan.SelectedTracks.Audio, Subtitle: &playback.TrackIdentityV3{ID: playback.TrackIDV3(file.ID, "subtitle", english), Index: &english}},
		Capabilities:          startRequest.Capabilities,
		ClientPlaybackContext: startRequest.ClientPlaybackContext,
	})
	if replanned.PlaybackPlan == nil || replanned.PlaybackPlan.SelectedTracks.Subtitle == nil ||
		replanned.PlaybackPlan.SelectedTracks.Subtitle.Index == nil || *replanned.PlaybackPlan.SelectedTracks.Subtitle.Index != english {
		t.Fatalf("replanned subtitle = %#v", replanned.PlaybackPlan)
	}
	after := handler.tm.GetTranscodeSession(started.SessionID)
	if after != before {
		t.Fatalf("sidecar-only replan replaced HLS transport: before=%p after=%p", before, after)
	}
	if !after.IsRunning() {
		t.Fatal("sidecar-only replan stopped the active HLS transport")
	}
	if afterOpts := after.Opts(); afterOpts.OutputDir != beforeOpts.OutputDir || afterOpts.SeekSeconds != beforeOpts.SeekSeconds || afterOpts.StartSegmentNumber != beforeOpts.StartSegmentNumber {
		t.Fatalf("sidecar-only replan changed HLS generation: before=%#v after=%#v", beforeOpts, afterOpts)
	}
	if after.LastRequestedSegment() != beforeRequestedSegment {
		t.Fatalf("sidecar-only replan reset throttle progress: before=%d after=%d", beforeRequestedSegment, after.LastRequestedSegment())
	}
	manifest, err := after.BuildPlaybackManifest("segment/", "")
	if err != nil {
		t.Fatalf("build reused copy-HLS manifest: %v", err)
	}
	for _, want := range []string{"#EXT-X-PLAYLIST-TYPE:EVENT", "#EXT-X-START:TIME-OFFSET=0.001,PRECISE=YES"} {
		if !strings.Contains(string(manifest), want) {
			t.Fatalf("reused copy-HLS manifest missing stable remount tag %q:\n%s", want, manifest)
		}
	}
	afterTimeline := replanned.PlaybackPlan.Timeline
	if replanned.PlaybackPlan.Stream.URL != beforeURL ||
		afterTimeline.StreamOriginSeconds != beforeTimeline.StreamOriginSeconds ||
		afterTimeline.TimelineOffsetSeconds != beforeTimeline.TimelineOffsetSeconds ||
		!reflect.DeepEqual(afterTimeline.SeekWindowStartSeconds, beforeTimeline.SeekWindowStartSeconds) ||
		!reflect.DeepEqual(afterTimeline.SeekWindowEndSeconds, beforeTimeline.SeekWindowEndSeconds) ||
		afterTimeline.CanSeekAnywhere != beforeTimeline.CanSeekAnywhere ||
		afterTimeline.SeekRestoration != beforeTimeline.SeekRestoration {
		t.Fatalf("sidecar-only replan changed stream window: url %q -> %q, timeline %#v -> %#v", beforeURL, replanned.PlaybackPlan.Stream.URL, beforeTimeline, afterTimeline)
	}
	if afterTimeline.SourceStartSeconds != 120 || afterTimeline.PlayerStartSeconds != max(0, 120-beforeTimeline.StreamOriginSeconds) {
		t.Fatalf("sidecar-only replan lost requested position: timeline %#v", afterTimeline)
	}
	if replanned.PlaybackPlan.PlanID == started.PlaybackPlan.PlanID || replanned.PlaybackPlan.PlanAttemptKey == started.PlaybackPlan.PlanAttemptKey {
		t.Fatal("subtitle identity changed without minting a distinct plan identity")
	}
	record, err := handler.PlanStoreV3.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if record.FrozenRecipe.SubtitleTrackIndex != english || record.CurrentPlan.SelectedTracks.Subtitle == nil || *record.CurrentPlan.SelectedTracks.Subtitle.Index != english {
		t.Fatalf("durable sidecar selection = %#v", record)
	}
}

func TestSidecarOnlyHLSReplanKeepsEffectiveToneMapFallback(t *testing.T) {
	currentPlan := playback.PlanV3{
		PlanID:               "current-plan",
		Delivery:             playback.DeliveryTranscodeHLSV3,
		Stream:               playback.StreamV3{URL: "/stream/current.m3u8"},
		RequestedMediaFileID: 1,
		EffectiveMediaFileID: 1,
	}
	candidatePlan := currentPlan
	candidatePlan.PlanID = "candidate-plan"
	revision := tonemap.SourceRevision{MediaFileID: 1}
	currentRecipe := playback.FreezeExecutableRecipeV3(playback.PlannerResultV3{
		Plan: &currentPlan, PlayMethod: playback.PlayTranscode,
		ToneMapPolicy: tonemap.PolicyHardwareThenSoftware, ToneMapMode: tonemap.ModeSoftware,
		ToneMapSourceKind: tonemap.SourcePQ, ToneMapRecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3,
		ToneMapSourceRevision: revision,
	})
	candidateRecipe := playback.FreezeExecutableRecipeV3(playback.PlannerResultV3{
		Plan: &candidatePlan, PlayMethod: playback.PlayTranscode,
		ToneMapPolicy: tonemap.PolicyHardwareThenSoftware, ToneMapMode: tonemap.ModeHardware,
		ToneMapSourceKind: tonemap.SourcePQ, ToneMapRecipeVersion: playback.TransformationHDRToSDRToneMapRecipeVersionV3,
		ToneMapSourceRevision: revision,
	})
	record := &playback.AttemptRecordV3{
		EffectiveMediaFileID: 1,
		CurrentPlan:          currentPlan,
		FrozenRecipe:         currentRecipe,
	}
	record.NormalizedRequest.ClientPlaybackContext.Output.OutputContextID = "output-context"

	reusedRecipe, ok := sidecarOnlyHLSReplanV3(record, &candidatePlan, candidateRecipe, "output-context")
	if !ok {
		t.Fatal("byte-identical sidecar replan did not reuse the active transport")
	}
	if reusedRecipe.ToneMapMode != tonemap.ModeSoftware {
		t.Fatalf("reused tone-map mode = %q, want active software fallback", reusedRecipe.ToneMapMode)
	}
}

func TestSidecarOnlyHLSReplanRejectsSourceVideoExecutionFactDrift(t *testing.T) {
	currentPlan := playback.PlanV3{
		PlanID:               "current-plan",
		Delivery:             playback.DeliveryTranscodeHLSV3,
		Stream:               playback.StreamV3{URL: "/stream/current.m3u8"},
		RequestedMediaFileID: 1,
		EffectiveMediaFileID: 1,
	}
	candidatePlan := currentPlan
	candidatePlan.PlanID = "candidate-plan"
	currentSource := playback.SourceExecutionMetadataV3{
		VideoCodec: "h264", VideoProfile: "High 10", VideoBitDepth: 10,
	}
	currentRecipe := playback.FreezeExecutableRecipeV3(playback.PlannerResultV3{
		Plan: &currentPlan, PlayMethod: playback.PlayTranscode,
		TargetVideoCodec: "h264", TargetAudioCodec: "aac",
		FrozenSourceMetadata: &currentSource,
	})
	record := &playback.AttemptRecordV3{
		EffectiveMediaFileID: 1,
		CurrentPlan:          currentPlan,
		FrozenRecipe:         currentRecipe,
	}
	record.NormalizedRequest.ClientPlaybackContext.Output.OutputContextID = "output-context"

	for _, test := range []struct {
		name   string
		mutate func(*playback.SourceExecutionMetadataV3)
	}{
		{name: "profile", mutate: func(source *playback.SourceExecutionMetadataV3) { source.VideoProfile = "Main 10" }},
		{name: "bit depth", mutate: func(source *playback.SourceExecutionMetadataV3) { source.VideoBitDepth = 8 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidateSource := currentSource
			test.mutate(&candidateSource)
			candidateRecipe := playback.FreezeExecutableRecipeV3(playback.PlannerResultV3{
				Plan: &candidatePlan, PlayMethod: playback.PlayTranscode,
				TargetVideoCodec: "h264", TargetAudioCodec: "aac",
				FrozenSourceMetadata: &candidateSource,
			})
			if _, reused := sidecarOnlyHLSReplanV3(record, &candidatePlan, candidateRecipe, "output-context"); reused {
				t.Fatalf("sidecar-only replan reused A/V bytes after source video %s changed", test.name)
			}
		})
	}
}

func TestHandleReplanPlaybackV3FailureRecoveryPreservesOmittedQuality(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"transcode_enabled": "true", "allow_4k_transcode": "true"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpeg(t), t.TempDir())
	handler.v3Registry = playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{
		{Name: playback.TransformationAudioToAACV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3, Available: true},
		{Name: playback.TransformationVideoToH264V3, RecipeVersion: "2", Available: true},
	})

	startRequest := v3HandlerStartRequest()
	startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3] = playback.DeliveryCapabilityV3{Enabled: true, SupportedOnDevice: true}
	startRR := httptest.NewRecorder()
	handler.HandleStartPlayback(startRR, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, startRequest))).WithContext(newAuthorizedPlaybackContext()))
	var started playback.DecisionResponseV3
	if startRR.Code != http.StatusCreated || json.Unmarshal(startRR.Body.Bytes(), &started) != nil || started.PlaybackPlan == nil {
		t.Fatalf("start status=%d body=%s", startRR.Code, startRR.Body.String())
	}
	t.Cleanup(func() { handler.tm.CloseTranscodeSession(started.SessionID, "") })

	store := handler.PlanStoreV3.(*playback.MemoryPlanStoreV3)
	record, err := store.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	record.NormalizedRequest.QualityPreference = "720p"
	store.ReplaceAttempt(context.Background(), *record)

	currentKey := playback.PlanAttemptKeyV3(*started.PlaybackPlan, startRequest.ClientPlaybackContext.Output.OutputContextID, nil)
	response := postPlaybackReplanV3(t, handler, started.SessionID, playback.ReplanRequestV3{
		ProtocolVersion:       playback.ProtocolV3,
		Operation:             playback.ReplanOperationFailureRecoveryV3,
		PlaybackAttemptID:     startRequest.PlaybackAttemptID,
		ReplanRequestID:       "quality-preserve-recovery-0001",
		FailedPlanID:          started.PlaybackPlan.PlanID,
		PlanAttemptID:         "quality-preserve-attempt-0001",
		PlanAttemptKey:        currentKey,
		AttemptedPlanKeys:     []string{currentKey},
		AttemptCount:          1,
		PositionSeconds:       30,
		Failure:               playback.FailureV3{Classification: "decoder_failure"},
		Capabilities:          startRequest.Capabilities,
		ClientPlaybackContext: startRequest.ClientPlaybackContext,
	})
	if response.PlaybackPlan == nil || response.PlaybackPlan.Delivery != playback.DeliveryTranscodeHLSV3 {
		t.Fatalf("recovery plan = %#v, want 720p transcode", response)
	}
	if response.PlaybackPlan.EffectiveRecipe.Height == nil || *response.PlaybackPlan.EffectiveRecipe.Height != 720 {
		t.Fatalf("recovery recipe = %#v, want 720p", response.PlaybackPlan.EffectiveRecipe)
	}
	record, err = store.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if record.NormalizedRequest.QualityPreference != "720p" {
		t.Fatalf("durable quality = %q, want 720p", record.NormalizedRequest.QualityPreference)
	}
}

func TestDecisionResponseFromAttemptV3NormalizesRequiredArrays(t *testing.T) {
	response := decisionResponseFromAttemptV3(&playback.AttemptRecordV3{
		StartResponse: playback.DecisionResponseV3{
			ProtocolVersion: playback.ProtocolV3,
			Outcome:         playback.OutcomePlayableV3,
			PlaybackPlan:    &playback.PlanV3{},
		},
	})
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"server_features":null`)) {
		t.Fatalf("required server_features encoded as null: %s", raw)
	}
	for _, field := range []string{"transformations", "applied_quirks", "runtime_corrections", "available_qualities", "degradation_warnings", "inventory"} {
		if !bytes.Contains(raw, []byte(`"`+field+`":[]`)) {
			t.Fatalf("required %s did not encode as []: %s", field, raw)
		}
	}
}

// quality_change carries a new preference with no failure classification and
// runs through the same transaction: idempotent duplicates, durable quality.
func TestHandleReplanPlaybackV3QualityChangeOperation(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "true"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	startRequest := v3HandlerStartRequest()
	startRequest.QualityPreference = "auto"
	startReq := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, startRequest))).WithContext(newAuthorizedPlaybackContext())
	startRR := httptest.NewRecorder()
	handler.HandleStartPlayback(startRR, startReq)
	if startRR.Code != http.StatusCreated {
		t.Fatalf("start status = %d, body = %s", startRR.Code, startRR.Body.String())
	}
	var started playback.DecisionResponseV3
	if err := json.Unmarshal(startRR.Body.Bytes(), &started); err != nil || started.PlaybackPlan == nil {
		t.Fatalf("start response: err=%v response=%#v", err, started)
	}
	if len(started.PlaybackPlan.AvailableQualities) == 0 || started.PlaybackPlan.AvailableQualities[0].Label != "original" {
		t.Fatalf("available qualities = %#v", started.PlaybackPlan.AvailableQualities)
	}
	currentKey := playback.PlanAttemptKeyV3(*started.PlaybackPlan, startRequest.ClientPlaybackContext.Output.OutputContextID, nil)
	replan := playback.ReplanRequestV3{
		ProtocolVersion:       playback.ProtocolV3,
		Operation:             playback.ReplanOperationQualityChangeV3,
		PlaybackAttemptID:     startRequest.PlaybackAttemptID,
		ReplanRequestID:       "quality-change-0001",
		FailedPlanID:          started.PlaybackPlan.PlanID,
		PlanAttemptID:         "plan-attempt-quality-0001",
		PlanAttemptKey:        currentKey,
		AttemptedPlanKeys:     []string{currentKey},
		AttemptCount:          1,
		QualityPreference:     "original",
		PositionSeconds:       60,
		SelectedTracks:        started.PlaybackPlan.SelectedTracks,
		Capabilities:          startRequest.Capabilities,
		ClientPlaybackContext: startRequest.ClientPlaybackContext,
	}
	body, err := json.Marshal(replan)
	if err != nil {
		t.Fatal(err)
	}
	call := func() playback.DecisionResponseV3 {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/"+started.SessionID+"/replan", strings.NewReader(string(body))).WithContext(newAuthorizedPlaybackContext())
		req = withPlaybackRouteParam(req, "session_id", started.SessionID)
		rr := httptest.NewRecorder()
		handler.HandleReplanPlaybackV3(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("quality change status = %d, body = %s", rr.Code, rr.Body.String())
		}
		var response playback.DecisionResponseV3
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	first := call()
	second := call()
	if first.PlaybackPlan == nil || second.PlaybackPlan == nil || first.PlaybackPlan.PlanID != second.PlaybackPlan.PlanID {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	if first.PlaybackPlan.Delivery != playback.DeliveryOriginalHTTPV3 {
		t.Fatalf("quality change plan = %#v", first.PlaybackPlan)
	}
	record, err := handler.PlanStoreV3.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if record.NormalizedRequest.QualityPreference != "original" {
		t.Fatalf("durable quality = %q", record.NormalizedRequest.QualityPreference)
	}
}

// An audio-only source (audiobook) must start over the v3 planner end to end.
func TestHandleStartPlaybackV3AudioOnlySource(t *testing.T) {
	file := &models.MediaFile{ID: 42, ContentID: "book-1", BaseType: "audiobook", FilePath: writePlaybackTestMediaFile(t, "book.m4b"), Container: "mp4", CodecVideo: "mjpeg", CodecAudio: "aac", Bitrate: 128, AudioChannels: 2, Duration: 39_600, VideoTracks: []models.VideoTrack{{Codec: "mjpeg", Width: 500, Height: 500}}, AudioTracks: []models.AudioTrack{{Codec: "aac", Channels: 2, Layout: "stereo"}}}
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	request := v3HandlerStartRequest()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, request))).WithContext(newAuthorizedPlaybackContext())
	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var response playback.DecisionResponseV3
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Outcome != playback.OutcomePlayableV3 || response.PlaybackPlan == nil {
		t.Fatalf("response = %#v", response)
	}
	if response.PlaybackPlan.Delivery != playback.DeliveryOriginalHTTPV3 || response.PlaybackPlan.Stream.URL == "" {
		t.Fatalf("plan = %#v", response.PlaybackPlan)
	}
	if response.PlaybackPlan.Source.VideoCodec != "" || response.PlaybackPlan.Subtitle.Mode != playback.SubtitleOffV3 {
		t.Fatalf("audio-only plan carried video state: %#v", response.PlaybackPlan)
	}
}

// Resume state is keyed on the item, so a session playing one part of a
// multipart presentation must not write its file-local position as the item's
// resume point. The server derives this from the media rather than trusting the
// client to ask for it.
func TestHandleStartPlaybackV3MultipartSuppressesProgressPersistence(t *testing.T) {
	for _, tc := range []struct {
		name         string
		partTotal    int
		persistence  playback.ProgressPersistenceV3
		wantDisabled bool
	}{
		{name: "single part owns its item timeline", partTotal: 1, wantDisabled: false},
		{name: "multipart shares one resume key", partTotal: 6, wantDisabled: true},
		{name: "client owns a single part timeline", partTotal: 1, persistence: playback.ProgressPersistenceClientV3, wantDisabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := &models.MediaFile{
				ID: 42, ContentID: "book-1", BaseType: "audiobook", FilePath: writePlaybackTestMediaFile(t, "book.m4b"),
				Container: "mp4", CodecAudio: "aac", Bitrate: 128, AudioChannels: 2, Duration: 39_600,
				AudioTracks:           []models.AudioTrack{{Codec: "aac", Channels: 2, Layout: "stereo"}},
				PresentationKind:      "multipart",
				PresentationGroupKey:  "book-1",
				PresentationPartIndex: 4,
				PresentationPartTotal: tc.partTotal,
			}
			manager := playback.NewSessionManager(0, 0)
			handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: file})
			handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{}}
			handler.ItemAccess = allowAllPlaybackItemAccess{}
			request := v3HandlerStartRequest()
			request.ProgressPersistence = tc.persistence
			if tc.persistence == playback.ProgressPersistenceClientV3 {
				zero := 0.0
				request.StartPosition = &zero
			}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, request))).WithContext(newAuthorizedPlaybackContext())
			rr := httptest.NewRecorder()
			handler.HandleStartPlayback(rr, req)
			if rr.Code != http.StatusCreated {
				t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
			}
			var response playback.DecisionResponseV3
			if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.PlaybackPlan == nil {
				t.Fatalf("response = %#v", response)
			}
			session, err := manager.GetSession(response.PlaybackPlan.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if session.DisableProgressPersistence != tc.wantDisabled {
				t.Fatalf("DisableProgressPersistence = %v, want %v", session.DisableProgressPersistence, tc.wantDisabled)
			}
		})
	}
}

func postPlaybackReplanV3(t *testing.T, handler *PlaybackHandler, sessionID string, request playback.ReplanRequestV3) playback.DecisionResponseV3 {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/"+sessionID+"/replan", strings.NewReader(string(body))).WithContext(newAuthorizedPlaybackContext())
	req = withPlaybackRouteParam(req, "session_id", sessionID)
	rr := httptest.NewRecorder()
	handler.HandleReplanPlaybackV3(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("replan status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var response playback.DecisionResponseV3
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response
}

// recordingNodePlannerV3 is a planner double that records what the transport
// asked for and whether the reservation was released.
type recordingNodePlannerV3 struct {
	plan             nodepool.Plan
	needsTranscode   bool
	estBitrateKbps   int
	plannedSessionID string
	released         []string
	// releasedProxy records the proxy-half releases: a start that keeps its
	// transcode node but publishes a URL the planned proxy does not serve.
	releasedProxy []string
}

func (p *recordingNodePlannerV3) PlanSession(sessionID, _ string, needsTranscode bool, estBitrateKbps int) nodepool.Plan {
	p.plannedSessionID = sessionID
	p.needsTranscode = needsTranscode
	p.estBitrateKbps = estBitrateKbps
	return p.plan
}

func (p *recordingNodePlannerV3) ReleaseSession(sessionID string) {
	p.released = append(p.released, sessionID)
}

func (p *recordingNodePlannerV3) ReleaseSessionProxy(sessionID string) {
	p.releasedProxy = append(p.releasedProxy, sessionID)
}

// PlanSessionWith mirrors the real planner: the eligibility predicate narrows
// the pool before selection, so a proxy that cannot execute the recipe is
// skipped rather than picked and then rejected.
func (p *recordingNodePlannerV3) PlanSessionWith(sessionID, currentTranscodeURL string, needsTranscode bool, estBitrateKbps int, eligible func(*nodepool.Node) bool) nodepool.Plan {
	plan := p.PlanSession(sessionID, currentTranscodeURL, needsTranscode, estBitrateKbps)
	if eligible == nil {
		return plan
	}
	if !needsTranscode && plan.ProxyNode != nil && !eligible(plan.ProxyNode) {
		return nodepool.Plan{}
	}
	if needsTranscode && plan.TranscodeNode != nil && !eligible(plan.TranscodeNode) {
		return nodepool.Plan{}
	}
	return plan
}

func (p *recordingNodePlannerV3) ProxyNodeURLs() []string {
	if p.plan.ProxyNode == nil {
		return nil
	}
	return []string{p.plan.ProxyNode.URL}
}

func (p *recordingNodePlannerV3) TranscodeNodeURLs() []string {
	if p.plan.TranscodeNode == nil {
		return nil
	}
	return []string{p.plan.TranscodeNode.URL}
}

func identityProxyPlanV3(delivery playback.DeliveryV3, transformations ...playback.TransformationV3) *playback.PlanV3 {
	return &playback.PlanV3{
		PlanID:          "plan:identity-proxy",
		Delivery:        delivery,
		Transformations: transformations,
		Source:          playback.SourceDescriptorV3{BitrateKbps: 5_673},
	}
}

func TestPrepareTransportV3RoutesDirectPlayThroughProxyNode(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	planner := &recordingNodePlannerV3{plan: nodepool.Plan{ProxyNode: &nodepool.Node{URL: "http://proxy-1"}}}
	handler.NodePlanner = planner

	transport, transportErr := handler.prepareTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		&playback.Session{ID: "session-direct-proxy", UserID: 7, ProfileID: "profile-1"},
		v3HandlerFixtureFile(t),
		playback.PlannerResultV3{Plan: identityProxyPlanV3(playback.DeliveryOriginalHTTPV3), PlayMethod: playback.PlayDirect}, mediaAuthModeV3{})
	if transportErr != nil {
		t.Fatalf("prepare identity transport: %v", transportErr)
	}
	defer transport.rollback()

	if !strings.HasPrefix(transport.url, "http://proxy-1/stream/direct/") {
		t.Fatalf("direct-play stream url = %q, want a proxy /stream/direct URL", transport.url)
	}
	if planner.needsTranscode {
		t.Fatal("direct play must not request a transcode node")
	}
	if planner.plannedSessionID != "session-direct-proxy" {
		t.Fatalf("reservation session id = %q, want the playback session id", planner.plannedSessionID)
	}
	if planner.estBitrateKbps != 5_673 {
		t.Fatalf("estimated bitrate = %d, want the source bitrate for cap admission", planner.estBitrateKbps)
	}

	token := strings.TrimPrefix(transport.url, "http://proxy-1/stream/direct/")
	claims, err := streamtoken.Verify(token, handler.JWTSecret)
	if err != nil {
		t.Fatalf("verify proxy stream token: %v", err)
	}
	if claims.MediaPath == "" {
		t.Fatal("proxy token carries no media path; the proxy cannot open the source")
	}
	if claims.PlayMethod != string(playback.PlayDirect) {
		t.Fatalf("token play method = %q, want direct", claims.PlayMethod)
	}
}

func TestPrepareTransportV3RoutesProgressiveRemuxThroughProxyNodeWithSeekAndDV(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	stubCopySeekAnchorV3(handler)
	proxy := capableProxyStubV3(t)
	handler.NodePlanner = &recordingNodePlannerV3{plan: nodepool.Plan{ProxyNode: &nodepool.Node{URL: proxy.URL + "/"}}}

	file := v3HandlerFixtureFile(t)
	file.VideoTracks[0].DVProfile = 7

	plan := identityProxyPlanV3(playback.DeliveryRemuxProgressiveV3, playback.TransformationV3{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3})
	plan.Timeline = playback.TimelineV3{SourceStartSeconds: 39.5}

	transport, transportErr := handler.prepareTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		&playback.Session{ID: "session-remux-proxy", UserID: 7, ProfileID: "profile-1"},
		file,
		playback.PlannerResultV3{Plan: plan, PlayMethod: playback.PlayRemux, TranscodeAudio: true, TargetAudioCodec: "aac"}, mediaAuthModeV3{})
	if transportErr != nil {
		t.Fatalf("prepare identity transport: %v", transportErr)
	}
	defer transport.rollback()

	remuxPrefix := proxy.URL + "/stream/remux/"
	if !strings.HasPrefix(transport.url, remuxPrefix) {
		t.Fatalf("remux stream url = %q, want a proxy /stream/remux URL", transport.url)
	}
	if !strings.Contains(transport.url, "seek=39.5") {
		t.Fatalf("remux stream url = %q, want the resolved seek preserved", transport.url)
	}
	token := strings.TrimSuffix(strings.TrimPrefix(transport.url, remuxPrefix), "?seek=39.5")
	claims, err := streamtoken.Verify(token, handler.JWTSecret)
	if err != nil {
		t.Fatalf("verify proxy stream token: %v", err)
	}
	if claims.DVProfile != 7 {
		t.Fatalf("token DV profile = %d, want 7 so the proxy strips the dangling RPU", claims.DVProfile)
	}
	if !claims.TranscodeAudio {
		t.Fatal("token must tell the proxy to convert audio")
	}
}

func TestIdentityStreamURLV3VersionsOnlyBoostedRemuxRoutes(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	file := v3HandlerFixtureFile(t)
	proxy := &nodepool.Node{URL: "http://proxy-1/"}

	boosted := &playback.Session{
		ID: "boosted", UserID: 7, ProfileID: "profile-1", MediaFileID: file.ID,
		PlayMethod: playback.PlayRemux, TranscodeAudio: true,
		TargetAudioCodec: "aac", SourceAudioChannels: 6, TargetAudioChannels: 2,
	}
	boostedURL, servedByProxy := handler.identityStreamURLV3(boosted, file, proxy)
	boostedPrefix := "http://proxy-1/stream/remux/audio-v2/"
	if !servedByProxy || !strings.HasPrefix(boostedURL, boostedPrefix) {
		t.Fatalf("boosted remux URL = %q (proxy %v), want the audio-v2 route", boostedURL, servedByProxy)
	}
	claims, err := streamtoken.Verify(strings.TrimPrefix(boostedURL, boostedPrefix), handler.JWTSecret)
	if err != nil {
		t.Fatal(err)
	}
	if claims.PlayMethod != streamtoken.PlayMethodAudioDownmixRemux || claims.SourceAudioChannels != 6 {
		t.Fatalf("boosted claims = %#v", claims)
	}

	ordinaryCases := []struct {
		name   string
		mutate func(*playback.Session)
	}{
		{name: "unknown source", mutate: func(s *playback.Session) { s.SourceAudioChannels = 0 }},
		{name: "stereo source", mutate: func(s *playback.Session) { s.SourceAudioChannels = 2 }},
		{name: "copy-only remux", mutate: func(s *playback.Session) { s.TranscodeAudio = false }},
		{name: "surround output", mutate: func(s *playback.Session) { s.TargetAudioChannels = 6 }},
		{name: "non AAC output", mutate: func(s *playback.Session) { s.TargetAudioCodec = "eac3" }},
	}
	for _, test := range ordinaryCases {
		t.Run(test.name, func(t *testing.T) {
			ordinary := *boosted
			ordinary.ID = "ordinary"
			test.mutate(&ordinary)
			ordinaryURL, ordinaryByProxy := handler.identityStreamURLV3(&ordinary, file, proxy)
			legacyPrefix := "http://proxy-1/stream/remux/"
			if !ordinaryByProxy || !strings.HasPrefix(ordinaryURL, legacyPrefix) || strings.HasPrefix(ordinaryURL, boostedPrefix) {
				t.Fatalf("ordinary remux URL = %q (proxy %v), want the legacy route", ordinaryURL, ordinaryByProxy)
			}
		})
	}
}

func TestPrepareTransportV3NegotiatesHeaderAuthenticatedProgressiveRemuxURL(t *testing.T) {
	for _, test := range []struct {
		name      string
		features  []string
		wantToken bool
	}{
		{name: "header authenticated", features: []string{playback.FeatureHeaderAuthenticatedMediaV3}},
		{name: "legacy", wantToken: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
			handler.JWTSecret = "test-stream-signing-secret"
			stubCopySeekAnchorV3(handler)
			plan := identityProxyPlanV3(playback.DeliveryRemuxProgressiveV3)
			plan.EffectiveMediaFileID = 42
			plan.Timeline = playback.TimelineV3{SourceStartSeconds: 39.5}
			request := httptest.NewRequest(http.MethodPost, "/", nil)

			transport, transportErr := handler.prepareTransportV3(
				request,
				&playback.Session{ID: "session-remux-auth", UserID: 7, ProfileID: "profile-1", MediaFileID: 42},
				v3HandlerFixtureFile(t),
				playback.PlannerResultV3{Plan: plan, PlayMethod: playback.PlayRemux, TargetAudioCodec: "aac"},
				headerAuthenticatedMediaV3(test.features),
			)
			if transportErr != nil {
				t.Fatalf("prepare remux: %v", transportErr)
			}
			defer transport.rollback()

			parsed, err := url.Parse(transport.url)
			if err != nil {
				t.Fatal(err)
			}
			if parsed.IsAbs() || parsed.Path != "/stream/session-remux-auth" || parsed.Query().Get("seek") != "39.5" {
				t.Fatalf("remux URL = %q, want API-local seek route", transport.url)
			}
			if got := parsed.Query().Get(streamTokenParam); (got != "") != test.wantToken {
				t.Fatalf("remux stream token present = %v for URL %q, want %v", got != "", transport.url, test.wantToken)
			}
		})
	}
}

func TestPrepareTransportV3FallsBackLocallyWithoutEligibleProxy(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodePlanner = &recordingNodePlannerV3{plan: nodepool.Plan{}}

	transport, transportErr := handler.prepareTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		&playback.Session{ID: "session-direct-local", UserID: 7, ProfileID: "profile-1"},
		v3HandlerFixtureFile(t),
		playback.PlannerResultV3{Plan: identityProxyPlanV3(playback.DeliveryOriginalHTTPV3), PlayMethod: playback.PlayDirect}, mediaAuthModeV3{})
	if transportErr != nil {
		t.Fatalf("prepare identity transport: %v", transportErr)
	}
	defer transport.rollback()

	if !strings.HasPrefix(transport.url, "/stream/session-direct-local") {
		t.Fatalf("stream url = %q, want the API-local path when no proxy is eligible", transport.url)
	}
}

func TestPrepareTransportV3RefusesLocalRemuxWhenFallbackDisabled(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	stubCopySeekAnchorV3(handler)
	handler.NodePlanner = &recordingNodePlannerV3{plan: nodepool.Plan{}}
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"playback.local_transcode_fallback": "false"}}

	_, transportErr := handler.prepareTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		&playback.Session{ID: "session-remux-refused", UserID: 7, ProfileID: "profile-1"},
		v3HandlerFixtureFile(t),
		playback.PlannerResultV3{
			Plan:           identityProxyPlanV3(playback.DeliveryRemuxProgressiveV3, playback.TransformationV3{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3}),
			PlayMethod:     playback.PlayRemux,
			TranscodeAudio: true,
		}, mediaAuthModeV3{})
	if transportErr == nil || transportErr.reason != "capacity_unavailable" {
		t.Fatalf("transport error = %#v, want capacity_unavailable when local remux work is disabled", transportErr)
	}
}

func TestPrepareTransportV3AllowsLocalDirectPlayWhenFallbackDisabled(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodePlanner = &recordingNodePlannerV3{plan: nodepool.Plan{}}
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"playback.local_transcode_fallback": "false"}}

	// Direct play moves bytes rather than running ffmpeg, so the transcode
	// fallback gate must not strand a single-node deployment.
	transport, transportErr := handler.prepareTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		&playback.Session{ID: "session-direct-allowed", UserID: 7, ProfileID: "profile-1"},
		v3HandlerFixtureFile(t),
		playback.PlannerResultV3{Plan: identityProxyPlanV3(playback.DeliveryOriginalHTTPV3), PlayMethod: playback.PlayDirect}, mediaAuthModeV3{})
	if transportErr != nil {
		t.Fatalf("direct play refused with local fallback disabled: %#v", transportErr)
	}
	transport.rollback()
}

func TestPrepareTransportV3ReleasesProxyReservationOnRollback(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	planner := &recordingNodePlannerV3{plan: nodepool.Plan{ProxyNode: &nodepool.Node{URL: "http://proxy-1"}}}
	handler.NodePlanner = planner

	transport, transportErr := handler.prepareTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		&playback.Session{ID: "session-rollback", UserID: 7, ProfileID: "profile-1"},
		v3HandlerFixtureFile(t),
		playback.PlannerResultV3{Plan: identityProxyPlanV3(playback.DeliveryOriginalHTTPV3), PlayMethod: playback.PlayDirect}, mediaAuthModeV3{})
	if transportErr != nil {
		t.Fatalf("prepare identity transport: %v", transportErr)
	}
	transport.rollback()

	if len(planner.released) != 1 || planner.released[0] != "session-rollback" {
		t.Fatalf("released reservations = %v, want the session released once", planner.released)
	}
}

// capableProxyStubV3 is a proxy that advertises the audio_to_aac and DV strip
// recipes, mirroring the /hw-capabilities endpoint a real proxy now serves.
func capableProxyStubV3(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hw-capabilities" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, playback.HWAccelInfo{Transformations: []playback.TransformationV3{
			{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3},
			{Name: playback.TransformationServerDV7HDR10V3, Executor: playback.ExecutorServerV3, RecipeVersion: "1"},
		}})
	}))
	t.Cleanup(server.Close)
	return server
}

func TestPrepareTransportV3KeepsBoostedDownmixLocalWhenProxyHasOldRecipe(t *testing.T) {
	// A rolling-upgrade proxy can support AAC while still advertising the old
	// recipe that does not apply the stereo-downmix loudness filter. Selection
	// must reject it so mixed-version deployments never silently lose the boost.
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, playback.HWAccelInfo{Transformations: []playback.TransformationV3{
			{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: "1"},
		}})
	}))
	defer proxy.Close()

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	stubCopySeekAnchorV3(handler)
	planner := &recordingNodePlannerV3{plan: nodepool.Plan{ProxyNode: &nodepool.Node{URL: proxy.URL}}}
	handler.NodePlanner = planner

	transport, transportErr := handler.prepareTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		&playback.Session{ID: "session-incapable-proxy", UserID: 7, ProfileID: "profile-1"},
		v3HandlerFixtureFile(t),
		playback.PlannerResultV3{
			Plan:                identityProxyPlanV3(playback.DeliveryRemuxProgressiveV3, playback.TransformationV3{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3}),
			PlayMethod:          playback.PlayRemux,
			TranscodeAudio:      true,
			SourceAudioChannels: 6,
			TargetAudioChannels: 2,
		}, mediaAuthModeV3{})
	if transportErr != nil {
		t.Fatalf("prepare identity transport: %v", transportErr)
	}
	defer transport.rollback()

	if strings.HasPrefix(transport.url, proxy.URL) {
		t.Fatalf("stream url = %q, want local fallback when the proxy only has audio recipe v1", transport.url)
	}
	// Narrowing happens before selection, so no reservation is made against an
	// incapable proxy in the first place and none needs releasing.
	if len(planner.released) != 0 {
		t.Fatalf("released = %v, want no reservation taken on an incapable proxy", planner.released)
	}
}

func TestPrepareTransportV3DirectPlaySkipsProxyCapabilityProbe(t *testing.T) {
	// Direct play copies bytes and needs no recipe, so it must not pay a
	// capability fetch nor be rejected by one.
	probes := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probes++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer proxy.Close()

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodePlanner = &recordingNodePlannerV3{plan: nodepool.Plan{ProxyNode: &nodepool.Node{URL: proxy.URL}}}

	transport, transportErr := handler.prepareTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		&playback.Session{ID: "session-direct-noprobe", UserID: 7, ProfileID: "profile-1"},
		v3HandlerFixtureFile(t),
		playback.PlannerResultV3{Plan: identityProxyPlanV3(playback.DeliveryOriginalHTTPV3), PlayMethod: playback.PlayDirect}, mediaAuthModeV3{})
	if transportErr != nil {
		t.Fatalf("prepare identity transport: %v", transportErr)
	}
	defer transport.rollback()

	if !strings.HasPrefix(transport.url, proxy.URL+"/stream/direct/") {
		t.Fatalf("stream url = %q, want the proxy direct URL", transport.url)
	}
	if probes != 0 {
		t.Fatalf("capability probes = %d, want none for direct play", probes)
	}
}

func TestIdentityStreamBitrateKbpsV3PrefersEffectiveRecipe(t *testing.T) {
	// A downmixing remux egresses less than the source, so the effective
	// recipe — not the source descriptor — is what a capped proxy admits on.
	effective := 1_200
	plan := identityProxyPlanV3(playback.DeliveryRemuxProgressiveV3)
	plan.EffectiveRecipe.BitrateKbps = &effective

	if got := identityStreamBitrateKbpsV3(playback.PlannerResultV3{Plan: plan}); got != effective {
		t.Fatalf("bitrate estimate = %d, want the effective recipe value %d", got, effective)
	}

	plan.EffectiveRecipe.BitrateKbps = nil
	if got := identityStreamBitrateKbpsV3(playback.PlannerResultV3{Plan: plan}); got != 5_673 {
		t.Fatalf("bitrate estimate = %d, want the source fallback 5673", got)
	}
}

func TestPrepareTransportV3MarksProxySessionsAsRemotelyTransported(t *testing.T) {
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager)
	handler.JWTSecret = "test-secret"
	handler.NodePlanner = &recordingNodePlannerV3{plan: nodepool.Plan{ProxyNode: &nodepool.Node{URL: "http://proxy-1"}}}

	session, err := manager.StartSession(7, "profile-1", 42, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("start session: %v", err)
	}

	transport, transportErr := handler.prepareTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		session,
		v3HandlerFixtureFile(t),
		playback.PlannerResultV3{Plan: identityProxyPlanV3(playback.DeliveryOriginalHTTPV3), PlayMethod: playback.PlayDirect}, mediaAuthModeV3{})
	if transportErr != nil {
		t.Fatalf("prepare identity transport: %v", transportErr)
	}
	transport.commit()

	// The client talks to the proxy, so this server sees no transport request.
	// Without the remote-transport mark the idle sweep would reap a healthy
	// stream as soon as heartbeats lapsed.
	if expired := manager.CleanInactive(time.Nanosecond, time.Nanosecond); len(expired) != 0 {
		t.Fatalf("expired %d proxy-served sessions, want them preserved", len(expired))
	}
	if _, err := manager.GetSession(session.ID); err != nil {
		t.Fatalf("proxy-served session was reaped: %v", err)
	}
}

// poolNodePlannerV3 models a multi-proxy pool so capability narrowing can be
// observed end to end rather than only at the planner unit level.
type poolNodePlannerV3 struct {
	proxies  []*nodepool.Node
	released []string
}

func (p *poolNodePlannerV3) PlanSession(string, string, bool, int) nodepool.Plan {
	if len(p.proxies) == 0 {
		return nodepool.Plan{}
	}
	return nodepool.Plan{ProxyNode: p.proxies[0]}
}

func (p *poolNodePlannerV3) PlanSessionWith(sessionID, currentTranscodeURL string, needsTranscode bool, estBitrateKbps int, eligible func(*nodepool.Node) bool) nodepool.Plan {
	for _, node := range p.proxies {
		if eligible == nil || eligible(node) {
			return nodepool.Plan{ProxyNode: node}
		}
	}
	return nodepool.Plan{}
}

func (p *poolNodePlannerV3) ProxyNodeURLs() []string {
	urls := make([]string, 0, len(p.proxies))
	for _, node := range p.proxies {
		urls = append(urls, node.URL)
	}
	return urls
}

func (p *poolNodePlannerV3) TranscodeNodeURLs() []string { return nil }

func (p *poolNodePlannerV3) ReleaseSession(sessionID string) {
	p.released = append(p.released, sessionID)
}

func TestPrepareTransportV3PrefersACapableSiblingProxy(t *testing.T) {
	// A rolling upgrade leaves one proxy on an ffmpeg without an aac encoder.
	// The remux must land on its capable sibling rather than falling back to
	// the API node, which would be refused outright with local fallback off.
	stale := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, playback.HWAccelInfo{Transformations: []playback.TransformationV3{
			{Name: playback.TransformationVideoToH264V3, Executor: playback.ExecutorServerV3, RecipeVersion: "2"},
		}})
	}))
	defer stale.Close()
	upgraded := capableProxyStubV3(t)

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	stubCopySeekAnchorV3(handler)
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"playback.local_transcode_fallback": "false"}}
	handler.NodePlanner = &poolNodePlannerV3{proxies: []*nodepool.Node{
		{ID: 1, URL: stale.URL},
		{ID: 2, URL: upgraded.URL},
	}}

	transport, transportErr := handler.prepareTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		&playback.Session{ID: "session-sibling", UserID: 7, ProfileID: "profile-1"},
		v3HandlerFixtureFile(t),
		playback.PlannerResultV3{
			Plan:           identityProxyPlanV3(playback.DeliveryRemuxProgressiveV3, playback.TransformationV3{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3}),
			PlayMethod:     playback.PlayRemux,
			TranscodeAudio: true,
		}, mediaAuthModeV3{})
	if transportErr != nil {
		t.Fatalf("prepare identity transport: %#v", transportErr)
	}
	defer transport.rollback()

	if !strings.HasPrefix(transport.url, upgraded.URL+"/stream/remux/") {
		t.Fatalf("stream url = %q, want the capable sibling proxy %q", transport.url, upgraded.URL)
	}
}

func TestPrepareTransportV3ClearsRemoteTransportMarkWhenServingLocally(t *testing.T) {
	// A session replanned from a proxy onto this server must lose the widened
	// idle grace, or its stream and transcode slots stay held for minutes after
	// a local stream disconnects without an explicit stop.
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager)
	handler.JWTSecret = "test-secret"
	handler.NodePlanner = &recordingNodePlannerV3{plan: nodepool.Plan{ProxyNode: &nodepool.Node{URL: "http://proxy-1"}}}

	session, err := manager.StartSession(7, "profile-1", 42, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	file := v3HandlerFixtureFile(t)

	proxied, transportErr := handler.prepareTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil), session, file,
		playback.PlannerResultV3{Plan: identityProxyPlanV3(playback.DeliveryOriginalHTTPV3), PlayMethod: playback.PlayDirect}, mediaAuthModeV3{})
	if transportErr != nil {
		t.Fatalf("prepare proxy transport: %v", transportErr)
	}
	proxied.commit()

	// Re-plan with no eligible proxy: the same delivery now serves locally.
	handler.NodePlanner = &recordingNodePlannerV3{plan: nodepool.Plan{}}
	local, transportErr := handler.prepareTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil), session, file,
		playback.PlannerResultV3{Plan: identityProxyPlanV3(playback.DeliveryOriginalHTTPV3), PlayMethod: playback.PlayDirect}, mediaAuthModeV3{})
	if transportErr != nil {
		t.Fatalf("prepare local transport: %v", transportErr)
	}
	local.commit()

	if expired := manager.CleanInactive(time.Nanosecond, time.Nanosecond); len(expired) != 1 {
		t.Fatalf("expired %d sessions, want the locally-served session to lose its widened grace", len(expired))
	}
}

// A burn-in requirement that only an SDR alternate can satisfy must still reach
// the alternate-version fallback. The planner reports that refusal in terms of
// the subtitle rather than the HDR pipeline, so a gate listing only the HDR and
// version reasons silently retires the fallback and refuses playback outright.
func TestTerminalAllowsAlternateFileV3CoversSubtitleForcedRefusals(t *testing.T) {
	for _, reason := range []string{
		terminalNoAlternateVersionV3,
		terminalHDRTranscodeUnsupportedV3,
		terminalSubtitleConversionUnsupportedV3,
	} {
		if !terminalAllowsAlternateFileV3(&playback.TerminalV3{Reason: reason}) {
			t.Fatalf("terminal %q must allow an alternate-version retry", reason)
		}
	}
	if terminalAllowsAlternateFileV3(&playback.TerminalV3{Reason: "client_hls_unsupported"}) {
		t.Fatal("a client-route refusal must not trigger an alternate-version retry")
	}
	if terminalAllowsAlternateFileV3(nil) {
		t.Fatal("a nil terminal must not trigger an alternate-version retry")
	}
}
func TestPlaybackV3ToneMapBudgetsCoverColdNodeWork(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.PlaybackConfig = func() config.PlaybackConfig {
		return config.PlaybackConfig{HWAccel: tonemap.BackendQSV, HWDevice: "/dev/dri/renderD128"}
	}

	if got, want := handler.localToneMapProbeTimeoutV3(), tonemap.ProbeEndpointTimeout(tonemap.BackendQSV, "/dev/dri/renderD128"); got != want {
		t.Fatalf("local tone-map probe timeout = %s, want %s", got, want)
	}
	if got, want := handler.remoteToneMapProbeTimeoutV3("https://unknown.example"), remoteNodeProbeFallbackTimeout; got != want {
		t.Fatalf("remote tone-map probe timeout = %s, want %s", got, want)
	}
	if got := handler.toneMapPlanningTimeoutV3(true); got != v3NodeCapabilityPlanTimeout {
		t.Fatalf("planning timeout with local fallback = %s, want %s", got, v3NodeCapabilityPlanTimeout)
	}
	if got, want := handler.toneMapPlanningTimeoutV3(false), remoteNodeProbeFallbackTimeout; got != want {
		t.Fatalf("remote-only planning timeout = %s, want %s", got, want)
	}
	request := transcodenode.TranscodeStartRequest{
		ToneMapMode:              tonemap.ModeHardware,
		ToneMapPreflightRequired: true,
		TotalDuration:            100,
		RequireReady:             true,
		HWAccel:                  tonemap.BackendQSV,
	}
	want := remoteNodeProbeFallbackTimeout + playback.ManifestStartupTimeout +
		tonemap.SourcePreflightTimeout(100) + transcodenode.TranscodeStartReadinessTimeout
	if got := handler.remotePlaybackTransportTimeout("https://unknown.example", request); got != want {
		t.Fatalf("remote tone-map start timeout = %s, want %s", got, want)
	}
}

func TestLookupRemoteCapabilitiesStartsCacheTTLAfterRequestCompletes(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		ttl    time.Duration
	}{
		{name: "success", status: http.StatusOK, ttl: v3NodeCapabilityTTL},
		{name: "error", status: http.StatusServiceUnavailable, ttl: v3NodeCapabilityErrorTTL},
	} {
		t.Run(test.name, func(t *testing.T) {
			const requestDelay = 80 * time.Millisecond
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				time.Sleep(requestDelay)
				w.WriteHeader(test.status)
				if test.status == http.StatusOK {
					_ = json.NewEncoder(w).Encode(playback.HWAccelInfo{})
				}
			}))
			defer remote.Close()

			handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
			startedAt := time.Now()
			_, _ = handler.lookupRemoteCapabilitiesV3(context.Background(), remote.URL, false)
			entry := handler.v3NodeCapabilities[remote.URL]
			minimumLifetime := test.ttl + requestDelay - 20*time.Millisecond
			if lifetime := entry.expiresAt.Sub(startedAt); lifetime < minimumLifetime {
				t.Fatalf("cache lifetime from request start = %s, want at least %s", lifetime, minimumLifetime)
			}
		})
	}
}

func TestRemoteToneMapProbeTimeoutUsesTargetNodeBudget(t *testing.T) {
	probeTimeoutMillis := int64(137000)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(playback.HWAccelInfo{ProbeRequestTimeoutMillis: probeTimeoutMillis})
	}))
	defer remote.Close()
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.PlaybackConfig = func() config.PlaybackConfig {
		return config.PlaybackConfig{HWAccel: tonemap.BackendQSV, HWDevice: "/central/device/one,/central/device/two"}
	}

	if _, err := handler.lookupRemoteCapabilitiesV3(context.Background(), remote.URL, false); err != nil {
		t.Fatal(err)
	}
	if got, want := handler.remoteToneMapProbeTimeoutV3(remote.URL), 137*time.Second; got != want {
		t.Fatalf("remote probe timeout = %s, want target node budget %s", got, want)
	}
	request := transcodenode.TranscodeStartRequest{ToneMapMode: tonemap.ModeHardware}
	if got, want := handler.remotePlaybackTransportTimeout(remote.URL, request), 137*time.Second+playback.ManifestStartupTimeout; got != want {
		t.Fatalf("remote start timeout = %s, want target node budget %s", got, want)
	}

	handler.v3NodeCapabilitiesMu.Lock()
	delete(handler.v3NodeCapabilities, remote.URL)
	handler.v3NodeCapabilitiesMu.Unlock()
	probeTimeoutMillis = (10 * time.Minute).Milliseconds()
	if _, err := handler.lookupRemoteCapabilitiesV3(context.Background(), remote.URL, false); err != nil {
		t.Fatal(err)
	}
	if got, want := handler.remoteToneMapProbeTimeoutV3(remote.URL), 5*time.Minute; got != want {
		t.Fatalf("bounded remote probe timeout = %s, want %s", got, want)
	}
}
