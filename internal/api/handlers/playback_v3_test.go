package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/noderouting"
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

func (s *recordingRouteEventPlanStoreV3) RecordRouteEvent(_ context.Context, event playback.RouteEventRecordV3) (bool, error) {
	s.events <- event
	return true, nil
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

func TestHandleStartPlaybackV3TriesAlternateAfterHDRTerminal(t *testing.T) {
	for _, mode := range []string{"v2-auto", "v2-fixed"} {
		t.Run(mode, func(t *testing.T) {
			fixed := mode == "v2-fixed"
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

			handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: source})
			handler.FileVersionFetcher = testPlaybackFileVersionFetcher{byContent: map[string][]*models.MediaFile{
				source.ContentID: {source, alternate},
			}}
			handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "false"}}
			handler.PlaybackConfig = playbackTestConfig("", "")
			handler.ItemAccess = allowAllPlaybackItemAccess{}

			start := v3HandlerStartRequest()
			if fixed {
				start.AllowAlternateVersions = new(false)
			}
			start.QualityPreference = "auto"
			start.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3] = playback.DeliveryCapabilityV3{
				Enabled: true, SupportedOnDevice: true, Containers: []string{"hls"},
				VideoCodecs: []string{"h264"}, AudioDecodeCodecs: []string{"aac"},
			}
			rr := httptest.NewRecorder()
			startPlaybackV2IntoRecorder(t, handler, rr, start)

			var response playback.DecisionResponseV3
			if fixed {
				if rr.Code != http.StatusCreated || json.Unmarshal(rr.Body.Bytes(), &response) != nil || response.Terminal == nil || response.PlaybackPlan != nil {
					t.Fatalf("fixed source unexpectedly changed versions: status=%d body=%s", rr.Code, rr.Body.String())
				}
				return
			}

			if rr.Code != http.StatusCreated || json.Unmarshal(rr.Body.Bytes(), &response) != nil || response.PlaybackPlan == nil {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			if response.PlaybackPlan.EffectiveMediaFileID != alternate.ID {
				t.Fatalf("effective file = %d, want alternate %d", response.PlaybackPlan.EffectiveMediaFileID, alternate.ID)
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

func (s *mutablePlaybackSettingsV3) GetMany(ctx context.Context, keys ...string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		value, err := s.Get(ctx, key)
		if err != nil {
			return nil, err
		}
		if value != "" {
			out[key] = value
		}
	}
	return out, nil
}

// preparedAtSessionManager records when the start path committed the session
// state its prepared transport produced.
type preparedAtSessionManager struct {
	*playback.SessionManager
	preparedAt time.Time
}

func (m *preparedAtSessionManager) UpdateStreamState(sessionID string, state playback.SessionStreamState) error {
	err := m.SessionManager.UpdateStreamState(sessionID, state)
	m.preparedAt = time.Now()
	return err
}

// The startup grace must run from when the client gets its plan, not from
// admission: transport preparation can take long enough that a grace started
// at admission expires before the client's startup timeout (issue #3202).
func TestStartPlaybackV2StartsStartupGraceAfterTransportPreparation(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	manager := &preparedAtSessionManager{SessionManager: playback.NewSessionManager(0, 0)}
	handler := newKeyedPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}

	response := startPlayableDecisionV2(t, handler, marshalV3StartRequest(t, v3HandlerStartRequest()))
	session, err := manager.GetSession(response.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if manager.preparedAt.IsZero() || session.PlanCommittedAt.Before(manager.preparedAt) {
		t.Fatalf("PlanCommittedAt = %v, before the transport was prepared at %v", session.PlanCommittedAt, manager.preparedAt)
	}
}

func TestQueuedPlaybackStartSideEffectsAreDroppedOnStop(t *testing.T) {
	scrobbler := &saturatingPlaybackScrobblerV3{
		entered: make(chan string, playbackStartSideEffectsWorkersV3+1),
		release: make(chan struct{}),
	}
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.WatchScrobbler = scrobbler
	file := &models.MediaFile{ID: 1, ContentID: "movie-1", Duration: 600}

	for i := range playbackStartSideEffectsWorkersV3 {
		sessionID := fmt.Sprintf("running-%d", i)
		handler.enqueuePlaybackStartSideEffectsV3(context.Background(), &playback.Session{ID: sessionID}, file, nil, 1, "profile-1", nil)
	}
	for range playbackStartSideEffectsWorkersV3 {
		select {
		case <-scrobbler.entered:
		case <-time.After(time.Second):
			t.Fatal("start side-effect workers did not saturate")
		}
	}

	queuedSessionID := "queued-stop"
	handler.enqueuePlaybackStartSideEffectsV3(context.Background(), &playback.Session{ID: queuedSessionID}, file, nil, 1, "profile-1", nil)
	handler.cancelPlaybackStartSideEffectsV3(context.Background(), queuedSessionID)
	close(scrobbler.release)
	handler.waitForPlaybackStartSideEffectsV3(context.Background(), queuedSessionID)

	if starts := scrobbler.recordedStarts(); slices.Contains(starts, queuedSessionID) {
		t.Fatalf("queued stopped session emitted a start scrobble: %v", starts)
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

func TestRollbackFailedReplanV3KeepsSuccessorWhenCancellationFails(t *testing.T) {
	cancelErr := errors.New("node unavailable")
	normalRollbackCalled := false
	sessionRollbackCalled := false
	transport := &preparedTransportV3{
		rollback: func() { normalRollbackCalled = true },
		rollbackRequired: func() error {
			return cancelErr
		},
	}

	transportErr, sessionErr := rollbackFailedReplanV3(transport, func() error {
		sessionRollbackCalled = true
		return nil
	})

	if !errors.Is(transportErr, cancelErr) || sessionErr != nil {
		t.Fatalf("rollback errors = transport %v session %v, want cancellation failure only", transportErr, sessionErr)
	}
	if normalRollbackCalled {
		t.Fatal("required rollback failure fell through to best-effort cleanup")
	}
	if sessionRollbackCalled {
		t.Fatal("predecessor session was restored while the successor transport remained active")
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
			artifact := *value.Subtitle.Artifact
			artifact.MIMEType = "text/vtt"
			value.Subtitle.Artifact = &artifact
		}},
		{name: "subtitle artifact format", mutate: func(value *playback.PlanV3) {
			artifact := *value.Subtitle.Artifact
			artifact.Format = "vtt"
			value.Subtitle.Artifact = &artifact
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
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
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
			handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
			plan := &playback.PlanV3{
				Subtitle: playback.SubtitleDecisionV3{
					Mode:      test.mode,
					TrackID:   playback.TrackIDV3(file.ID, "subtitle", 1),
					Artifact:  stale(),
					Inventory: playback.BuildSubtitleInventoryV3(file, nil),
				},
			}
			if err := handler.attachSubtitleArtifactV3(context.Background(), "session-current", file, plan, test.selectedIndex, nil, nil); err != nil {
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
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.SubtitleRepo = repo
	plan := &playback.PlanV3{
		Subtitle: playback.SubtitleDecisionV3{Mode: playback.SubtitleRenderV3},
	}
	recipe := playback.ExecutableRecipeV3{
		SubtitleSource: playback.SubtitleSourceDownloadedV3, DownloadedSubtitleID: 71,
		SubtitleTrackIndex: 0, SubtitleCodec: "vtt",
	}
	err := handler.attachSubtitleArtifactV3(context.Background(), "session-store-error", file, plan, 0, &recipe, nil)
	if !errors.Is(err, errSubtitleStoreUnavailableV3) {
		t.Fatalf("attach error = %v, want wrapped subtitle-store failure", err)
	}
}

func TestFreezeExecutableRecipeV3FailsLoudlyWhenDownloadedIdentityUnavailable(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
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
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))

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
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodeKeys = nodeauthtest.SameKey("test-secret")
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

func TestPrepareTransportV3CopyAnchorFailureIsRetryable(t *testing.T) {
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
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

	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodeKeys = nodeauthtest.SameKey("test-secret")
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

			handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
			handler.JWTSecret = "test-stream-signing-secret"
			handler.NodeKeys = nodeauthtest.SameKey("test-stream-signing-secret")
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
			writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{
				SessionID: startRequest.SessionID, Status: "started",
				CopyFMP4RecipeVersion: startRequest.CopyFMP4RecipeVersion,
			})
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer remote.Close()

	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodeKeys = nodeauthtest.SameKey("test-secret")
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

func TestPrepareRemoteTransportV3RemuxOmitsToneMapOnlyDolbyVisionEvidence(t *testing.T) {
	var startRequest transcodenode.TranscodeStartRequest
	var startCalled bool
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/hw-capabilities":
			writeJSON(w, http.StatusOK, playback.HWAccelInfo{})
		case r.Method == http.MethodPost && r.URL.Path == "/transcode/start":
			startCalled = true
			if err := json.NewDecoder(r.Body).Decode(&startRequest); err != nil {
				t.Errorf("decode remote start: %v", err)
			}
			writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{
				SessionID: startRequest.SessionID, Status: "started",
				CopyFMP4RecipeVersion: startRequest.CopyFMP4RecipeVersion,
			})
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer remote.Close()

	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodeKeys = nodeauthtest.SameKey("test-secret")
	handler.NodePlanner = staticNodePlannerV3{plan: nodepool.Plan{TranscodeNode: &nodepool.Node{URL: remote.URL}}}
	file := v3HandlerFixtureFile(t)
	file.CodecVideo = "hevc"
	file.VideoTracks[0].Codec = "hevc"
	file.VideoTracks[0].DVConfigPresent = true
	file.VideoTracks[0].DVBLCompatIDPresent = true
	file.VideoTracks[0].DVBLPresent = true
	file.VideoTracks[0].DVRPUPresent = true
	plan := &playback.PlanV3{PlanID: "plan:remote-dv8-remux", Delivery: playback.DeliveryRemuxHLSV3}
	transport, transportErr := handler.prepareTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		&playback.Session{ID: "session-remote-dv8-remux", UserID: 7, ProfileID: "profile-1"},
		file,
		playback.PlannerResultV3{Plan: plan, PlayMethod: playback.PlayRemux, TargetVideoCodec: "copy", TargetAudioCodec: "aac"},
		mediaAuthModeV3{},
	)
	if transportErr != nil {
		t.Fatalf("prepare remote Dolby Vision remux: %v", transportErr)
	}
	defer transport.rollback()
	if !startCalled {
		t.Fatal("remote transcode start was not requested")
	}
	if startRequest.CopyFMP4RecipeVersion != playback.CopyFMP4RecipeVersion {
		t.Fatalf("copy fMP4 recipe version = %q, want %q", startRequest.CopyFMP4RecipeVersion, playback.CopyFMP4RecipeVersion)
	}
	if startRequest.ToneMapDVConfigPresent || startRequest.ToneMapDVBLCompatIDPresent || startRequest.ToneMapDVBLPresent || startRequest.ToneMapDVRPUPresent {
		t.Fatalf("remote copy/remux request carried tone-map-only Dolby Vision evidence: %#v", startRequest)
	}
}

// A fresh remote start of an MPEG-TS copy remux asks the node for MPEG-TS and
// refuses, and stops, a node that does not attest it, so an fMP4 stream is
// never published under a plan that promises MPEG-TS.
func TestPrepareRemoteTransportV3RefusesUnattestedMPEGTSCopy(t *testing.T) {
	for _, attests := range []bool{true, false} {
		t.Run(fmt.Sprintf("attests=%t", attests), func(t *testing.T) {
			var startRequest transcodenode.TranscodeStartRequest
			stopRequests := 0
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/hw-capabilities":
					writeJSON(w, http.StatusOK, playback.HWAccelInfo{})
				case r.Method == http.MethodPost && r.URL.Path == "/transcode/start":
					if err := json.NewDecoder(r.Body).Decode(&startRequest); err != nil {
						t.Errorf("decode remote start: %v", err)
					}
					writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{
						SessionID: startRequest.SessionID, Status: "started",
						CopyFMP4RecipeVersion: startRequest.CopyFMP4RecipeVersion,
						CopyVideoMPEGTS:       attests && startRequest.CopyVideoMPEGTS,
					})
				case r.Method == http.MethodDelete:
					stopRequests++
					w.WriteHeader(http.StatusNoContent)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer remote.Close()

			handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
			handler.JWTSecret = "test-secret"
			handler.NodeKeys = nodeauthtest.SameKey("test-secret")
			handler.NodePlanner = staticNodePlannerV3{plan: nodepool.Plan{TranscodeNode: &nodepool.Node{URL: remote.URL}}}
			plan := &playback.PlanV3{PlanID: "plan:remote-mpegts-copy", Delivery: playback.DeliveryRemuxHLSV3,
				EffectiveRecipe: playback.EffectiveRecipeV3{SegmentContainer: playback.SegmentContainerMPEGTSV3}}
			// Pin the remux to the worker so a refusal cannot fall back to a
			// local FFmpeg on the test host.
			policy := config.DefaultPlaybackRoutingPolicy()
			policy.RemuxExecution = config.PlaybackExecutionWorkerOnly
			transport, transportErr := handler.prepareTransportWithPolicyV3(
				httptest.NewRequest(http.MethodPost, "/", nil),
				&playback.Session{ID: "session-remote-mpegts-copy", UserID: 7, ProfileID: "profile-1"},
				v3HandlerFixtureFile(t),
				playback.PlannerResultV3{Plan: plan, PlayMethod: playback.PlayRemux, TargetVideoCodec: "copy", TargetAudioCodec: "copy"},
				mediaAuthModeV3{}, policy,
			)
			if !startRequest.CopyVideoMPEGTS {
				t.Fatalf("remote start did not request MPEG-TS: %#v", startRequest)
			}
			if attests {
				if transportErr != nil {
					t.Fatalf("prepare attested MPEG-TS copy: %#v", transportErr)
				}
				transport.rollback()
				return
			}
			if transportErr == nil || transportErr.reason != transcodeStartFailedReasonV3 || !errors.Is(transportErr.cause, transcodenode.ErrCopyVideoMPEGTSAttestationMismatch) {
				transport.rollback()
				t.Fatalf("transport error = %#v, want the MPEG-TS attestation refusal", transportErr)
			}
			if stopRequests != 1 {
				t.Fatalf("stop requests = %d, want one cleanup for the unattested job", stopRequests)
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
			writeJSON(w, http.StatusAccepted, transcodenode.TranscodeStartResponse{
				SessionID: startRequest.SessionID, Status: "started",
				CopyFMP4RecipeVersion: startRequest.CopyFMP4RecipeVersion,
			})
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
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodeKeys = nodeauthtest.SameKey("test-secret")
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

func TestPrepareLocalTransportV3AllowsDolbyVisionVideoCopyAudioAdaptation(t *testing.T) {
	manager := playback.NewSessionManager(0, 0)
	handler := newKeyedPlaybackHandler(manager)
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

func TestPrepareLocalTransportV3ReturnsStableTerminalWhenFFmpegExitsBeforeReady(t *testing.T) {
	manager := playback.NewSessionManager(0, 0)
	handler := newKeyedPlaybackHandler(manager)
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

func TestPrepareLocalTransportV3CommitRejectsPublicationDuringShutdown(t *testing.T) {
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpeg(t), t.TempDir())
	file := v3HandlerFixtureFile(t)
	plan := &playback.PlanV3{PlanID: "plan:shutdown-race", Delivery: playback.DeliveryTranscodeHLSV3}
	result := playback.PlannerResultV3{
		Plan: plan, PlayMethod: playback.PlayTranscode, TargetVideoCodec: "h264", TargetAudioCodec: "aac", TargetResolution: "720p",
		SubtitleTrackIndex: -1, SubtitleTransportTrackIndex: -1,
	}
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	timeline, timelineErr := handler.prepareTransportTimelineV3(request.Context(), &playback.Session{ID: "session-shutdown-race"}, file, result)
	if timelineErr != nil {
		t.Fatalf("prepare timeline: %v", timelineErr)
	}
	transport, transportErr := handler.prepareLocalTransportV3(request, &playback.Session{ID: "session-shutdown-race", UserID: 7, ProfileID: "profile-1"}, file, result, timeline, mediaAuthModeV3{})
	if transportErr != nil {
		t.Fatalf("prepare local transport: %v", transportErr)
	}
	shutdownCtx, cancelShutdown := context.WithCancel(context.Background())
	shutdownDone := handler.tm.StartShutdownCleanup(shutdownCtx)
	cancelShutdown()
	<-shutdownDone

	commitErr := transport.commit()
	if commitErr == nil || commitErr.reason != transcodeStartFailedReasonV3 || !commitErr.retryable {
		t.Fatalf("commit error = %#v, want retryable transcode start failure", commitErr)
	}
	if live := handler.tm.GetTranscodeSession("session-shutdown-race"); live != nil {
		t.Fatal("rejected transport was published during shutdown")
	}
}

// A per-account cap must not present as server capacity: a busy server means
// wait, a spent allowance means stop the other session. A fresh attempt works
// once that session ends, so both caps stay retryable.
func TestSessionStartErrorV3SeparatesAccountLimitsFromServerCapacity(t *testing.T) {
	for _, tc := range []struct {
		err       error
		reason    string
		retryable bool
	}{
		{err: playback.ErrTooManyStreams, reason: "stream_limit_reached", retryable: true},
		{err: playback.ErrTooManyTranscodes, reason: "transcode_limit_reached", retryable: true},
		{err: playback.ErrTranscodingDisabled, reason: "transcoding_disabled"},
		{err: playback.ErrPlaybackNotAllowed, reason: "policy_denied"},
	} {
		got := sessionStartErrorV3(fmt.Errorf("wrapped: %w", tc.err))
		if got == nil || got.reason != tc.reason || got.retryable != tc.retryable {
			t.Fatalf("sessionStartErrorV3(%v) = %#v, want reason %q retryable %v", tc.err, got, tc.reason, tc.retryable)
		}
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
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
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

func TestBitmapFastStartKeepsDefaultTimelineAndSessionSegments(t *testing.T) {
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
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

// The admin views read the target bitrate from session state. A rebuilt
// session reports the card's bitrate unless video is copied, so a native start
// must report the same or the value changes across a server restart.
func TestV3SessionStreamStateTargetBitrateMatchesReconstruction(t *testing.T) {
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
	for _, tc := range []struct {
		name       string
		method     playback.PlayMethod
		delivery   playback.DeliveryV3
		videoCodec string
		want       int
	}{
		{"video transcode", playback.PlayTranscode, playback.DeliveryTranscodeHLSV3, "h264", 4000},
		{"audio-only transcode", playback.PlayTranscode, playback.DeliveryTranscodeHLSV3, "copy", 0},
		{"hls remux", playback.PlayRemux, playback.DeliveryRemuxHLSV3, "copy", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := playback.PlannerResultV3{
				Plan: &playback.PlanV3{Delivery: tc.delivery}, PlayMethod: tc.method,
				TargetVideoCodec: tc.videoCodec, TargetAudioCodec: "aac", TargetBitrateKbps: 4000,
			}
			state := handler.v3SessionStreamState(context.Background(), &playback.Session{}, nil, result, preparedTransportV3{}, mediaAuthModeV3{})
			if state.TargetBitrateKbps != tc.want {
				t.Fatalf("target bitrate = %d, want %d", state.TargetBitrateKbps, tc.want)
			}
		})
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

func TestRouteEventV3HasPerUserLimitAcrossAttemptIDs(t *testing.T) {
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
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

// uhdSourceFileV3 is the fixture file as a 2160p, 32 Mbps level-5.1 source.
func uhdSourceFileV3(t *testing.T) *models.MediaFile {
	source := v3HandlerFixtureFile(t)
	source.Resolution = "2160p"
	source.Bitrate = 32_000
	source.VideoTracks = append([]models.VideoTrack(nil), source.VideoTracks...)
	source.VideoTracks[0].Level = 51
	source.VideoTracks[0].Width = 3840
	source.VideoTracks[0].Height = 2160
	source.VideoTracks[0].Bitrate = 32_000
	return source
}

// lowerAlternateFileV3 is a 1080p, 8 Mbps version (ID 84) of source's title.
func lowerAlternateFileV3(source *models.MediaFile) *models.MediaFile {
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
	return alternate
}

func v3HandlerStartRequest() playback.StartRequestV3 {
	return playback.StartRequestV3{ProtocolVersion: playback.ProtocolV3, ClientFeatures: []string{playback.FeaturePlaybackPlanV3}, FileID: 42, ProfileID: "profile-1", PlaybackAttemptID: "attempt-handler-0001", QualityPreference: "original", SubtitleFidelityPreference: playback.SubtitleFidelityCompatibleV3, Capabilities: playback.ClientCodecCapabilitiesV3{VideoEvidence: playback.EvidenceExactV3, AudioEvidence: playback.EvidenceExactV3, CodecsVideo: []string{"h264"}, CodecsVideoHardware: []string{"h264"}, CodecsAudio: []string{"aac"}, Containers: []string{"mp4"}, MaxResolution: "1080p", VideoDecode: []playback.VideoDecodeCapabilityV3{{Codec: "h264", Profiles: []string{"high"}, Levels: []int{41}, BitDepths: []int{8}, MaxWidth: 1920, MaxHeight: 1080, MaxFrameRate: 60, MaxBitrateKbps: 20_000, Hardware: true}}}, ClientPlaybackContext: playback.ClientPlaybackContextV3{ProtocolVersion: playback.ProtocolV3, FormFactor: "tv", AppVersion: "test", Device: playback.DeviceContextV3{Platform: "android"}, Output: playback.OutputContextV3{OutputContextID: "route-1"}, Deliveries: map[string]playback.DeliveryCapabilityV3{playback.DeliveryClassOriginalHTTPV3: {Enabled: true, SupportedOnDevice: true, Subtitles: playback.DeliverySubtitleCapabilitiesV3{EmbeddedText: true, SidecarText: true}}}}}
}

// repairingProbeEnsurer answers a probe repair with the stored repaired row
// for a file, standing in for a first probe that fills in its metadata.
type repairingProbeEnsurer struct {
	repaired map[int]*models.MediaFile
}

func (e repairingProbeEnsurer) EnsureProbeOnly(ctx context.Context, file *models.MediaFile) (*models.MediaFile, error) {
	return e.EnsureCopySafetyCached(ctx, file)
}

func (e repairingProbeEnsurer) EnsureCopySafetyCached(_ context.Context, file *models.MediaFile) (*models.MediaFile, error) {
	if repaired, ok := e.repaired[file.ID]; ok {
		return repaired, nil
	}
	return file, nil
}

// findAlternateFiles is the one sibling source for every fallback reason, at
// start and at replan, so the access filter applies there.
func TestFindAlternateFilesDropsVersionsTheViewerCannotPlay(t *testing.T) {
	missingSince := time.Now().UTC()
	source := &models.MediaFile{ID: 1, ContentID: "movie-1", MediaFolderID: 1, Resolution: "2160p"}
	files := []*models.MediaFile{
		source,
		{ID: 2, ContentID: "movie-1", MediaFolderID: 2, Resolution: "1080p"},
		{ID: 3, ContentID: "movie-1", MediaFolderID: 1, Resolution: "2160p"},
		{ID: 4, ContentID: "movie-1", MediaFolderID: 1, Resolution: "1080p"},
		{ID: 5, ContentID: "movie-1", MediaFolderID: 1, Resolution: "720p", MissingSince: &missingSince},
	}
	handler := &PlaybackHandler{FileVersionFetcher: testPlaybackFileVersionFetcher{byContent: map[string][]*models.MediaFile{"movie-1": files}}}

	got, err := handler.findAlternateFiles(context.Background(), source, catalog.AccessFilter{AllowedLibraryIDs: []int{1}, MaxPlaybackQuality: access.PlaybackQualityStandard})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != 4 {
		t.Fatalf("alternates = %+v, want only file 4", got)
	}

	got, err = handler.findAlternateFiles(context.Background(), source, catalog.AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("unrestricted alternates = %d, want the three present siblings", len(got))
	}
}

// versionChangeFixtureV3 is a movie with a 4K version (English and Japanese
// 5.1 AAC, an English sidecar subtitle) and a 1080p version (Japanese and
// English stereo AAC in the other order, no subtitles), started on the 4K
// version with Japanese audio and the subtitle. replan sends a quality change
// that echoes the playing audio and omits the subtitle, as clients do.
type versionChangeFixtureV3 struct {
	handler           *PlaybackHandler
	store             userstore.UserStore
	source, alternate *models.MediaFile
	startRequest      playback.StartRequestV3
	started           playback.DecisionResponseV3
}

func selectedTrackIDV3(identity *playback.TrackIdentityV3) string {
	if identity == nil {
		return ""
	}
	return identity.ID
}

// perFileSubtitleRepoV3 lists each file's own downloaded subtitles.
type perFileSubtitleRepoV3 struct{ *handlerMockSubtitleRepo }

func (m perFileSubtitleRepoV3) ListDownloadedSubtitles(_ context.Context, fileID int) ([]subtitles.DownloadedSubtitle, error) {
	m.listCalls++
	var rows []subtitles.DownloadedSubtitle
	for _, row := range m.list {
		if row.MediaFileID == fileID {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func marshalV3StartRequest(t *testing.T, request playback.StartRequestV3) string {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestReusedHLSRouteAllowedV3HonorsCurrentHardPolicy(t *testing.T) {
	result := playback.PlannerResultV3{Plan: &playback.PlanV3{Delivery: playback.DeliveryRemuxHLSV3}}
	local := &playback.Session{
		RoutingWorkload:  string(noderouting.WorkloadRemux),
		RoutingExecution: string(noderouting.ExecutionAPI),
		RoutingEgress:    string(noderouting.EgressAPI),
	}
	remote := &playback.Session{
		RoutingWorkload:  string(noderouting.WorkloadRemux),
		RoutingExecution: string(noderouting.ExecutionTranscode),
		RoutingEgress:    string(noderouting.EgressProxy),
	}
	tests := []struct {
		name         string
		session      *playback.Session
		mutate       func(*config.PlaybackRoutingPolicy)
		proxyAllowed bool
		want         bool
	}{
		{name: "local remains legal under soft preferences", session: local, proxyAllowed: true, want: true},
		{name: "local violates worker only", session: local, proxyAllowed: true, mutate: func(policy *config.PlaybackRoutingPolicy) {
			policy.RemuxExecution = config.PlaybackExecutionWorkerOnly
		}},
		{name: "local violates proxy only", session: local, proxyAllowed: true, mutate: func(policy *config.PlaybackRoutingPolicy) {
			policy.RemuxEgress = config.PlaybackEgressProxyOnly
		}},
		{name: "remote violates API execution only", session: remote, proxyAllowed: true, mutate: func(policy *config.PlaybackRoutingPolicy) {
			policy.RemuxExecution = config.PlaybackExecutionAPIOnly
		}},
		{name: "remote violates API egress only", session: remote, proxyAllowed: true, mutate: func(policy *config.PlaybackRoutingPolicy) {
			policy.RemuxEgress = config.PlaybackEgressAPIOnly
		}},
		{name: "remote proxy is client incompatible", session: remote, proxyAllowed: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := config.DefaultPlaybackRoutingPolicy()
			if test.mutate != nil {
				test.mutate(&policy)
			}
			if got := reusedHLSRouteAllowedV3(test.session, result, policy, test.proxyAllowed); got != test.want {
				t.Fatalf("reusedHLSRouteAllowedV3() = %t, want %t", got, test.want)
			}
		})
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

func TestHandleStartPlaybackV3FixedFileDoesNotResumeIntoAnotherPart(t *testing.T) {
	store := newPlaybackTestStore(t)
	if err := store.SetProgress(context.Background(), "profile-1", "book-1", 250, 400, userstore.ProgressThresholds{}); err != nil {
		t.Fatalf("seed progress: %v", err)
	}
	file := &models.MediaFile{
		ID: 42, ContentID: "book-1", BaseType: "audiobook", FilePath: writePlaybackTestMediaFile(t, "book.m4b"),
		Container: "mp4", CodecAudio: "aac", Bitrate: 128, AudioChannels: 2, Duration: 100,
		AudioTracks:           []models.AudioTrack{{Codec: "aac", Channels: 2, Layout: "stereo"}},
		PresentationKind:      "multipart",
		PresentationGroupKey:  "book-1",
		PresentationPartIndex: 1,
		PresentationPartTotal: 2,
	}
	manager := playback.NewSessionManager(0, 0)
	handler := newKeyedPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.StoreProvider = testUserStoreProvider{store: store}
	second := *file
	second.ID = 43
	second.PresentationPartIndex = 2
	second.Duration = 300
	handler.FileVersionFetcher = testPlaybackFileVersionFetcher{byContent: map[string][]*models.MediaFile{"book-1": {file, &second}}}

	request := v3HandlerStartRequest()
	request.AllowAlternateVersions = new(false)
	rr := httptest.NewRecorder()
	startPlaybackV2IntoRecorder(t, handler, rr, request)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var response playback.DecisionResponseV3
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.PlaybackPlan == nil || response.PlaybackPlan.EffectiveMediaFileID != file.ID {
		t.Fatalf("response = %#v", response)
	}
	if got := response.PlaybackPlan.Timeline.PlayerStartSeconds; got != 0 {
		t.Fatalf("part-local start = %v, want 0; an item-absolute resume position leaked into a part timeline", got)
	}
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

type progressiveFallbackPlannerV3 struct {
	transcode *nodepool.Node
	proxy     *nodepool.Node
	requests  []nodepool.RouteRequest
	released  []string
	onPlan    func(nodepool.RouteRequest)
}

func (*progressiveFallbackPlannerV3) PlanSession(string, string, bool, int) nodepool.Plan {
	return nodepool.Plan{}
}

func (p *progressiveFallbackPlannerV3) PlanRoute(request nodepool.RouteRequest) nodepool.Plan {
	p.requests = append(p.requests, request)
	if p.onPlan != nil {
		p.onPlan(request)
	}
	if request.NeedsTranscode && (p.transcode == nil || request.TranscodeEligible != nil && !request.TranscodeEligible(p.transcode)) {
		return nodepool.Plan{}
	}
	if request.NeedsProxy && (p.proxy == nil || request.ProxyEligible != nil && !request.ProxyEligible(p.proxy)) {
		return nodepool.Plan{}
	}
	plan := nodepool.Plan{}
	if request.NeedsTranscode {
		plan.TranscodeNode = p.transcode
	}
	if request.NeedsProxy {
		plan.ProxyNode = p.proxy
	}
	return plan
}

func (p *progressiveFallbackPlannerV3) ReleaseSession(sessionID string) {
	p.released = append(p.released, sessionID)
}

func (p *progressiveFallbackPlannerV3) ProxyNodeURLs() []string {
	return []string{p.proxy.URL}
}

func (p *progressiveFallbackPlannerV3) TranscodeNodeURLs() []string {
	return []string{p.transcode.URL}
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
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodeKeys = nodeauthtest.SameKey("test-secret")
	planner := &recordingNodePlannerV3{plan: nodepool.Plan{ProxyNode: &nodepool.Node{ID: 41, URL: "http://proxy-1"}}}
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
	if claims.RoutingWorkload != string(noderouting.WorkloadDirectPlay) ||
		claims.RoutingExecution != string(noderouting.ExecutionNone) ||
		claims.RoutingEgress != string(noderouting.EgressProxy) ||
		claims.RoutingEgressNodeID != 41 {
		t.Fatalf("token route = %q/%q/%q on node %d, want direct/none/proxy on node 41", claims.RoutingWorkload, claims.RoutingExecution, claims.RoutingEgress, claims.RoutingEgressNodeID)
	}
}

// A progressive remux URL carries remux_range=refuse only where its egress
// refuses the range: an older proxy would stream it from the origin instead.
func TestPrepareTransportV3PublishesRemuxRangeRefusalOnlyToAProxyThatHonorsIt(t *testing.T) {
	for _, tc := range []struct {
		name     string
		features []string
		want     bool
	}{
		{name: "upgraded proxy", features: []string{playback.TransportFeatureProgressiveRemuxRelayV1, playback.TransportFeatureRemuxRangeRefusalV1}, want: true},
		{name: "older proxy", features: []string{playback.TransportFeatureProgressiveRemuxRelayV1}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
			handler.JWTSecret = "test-secret"
			handler.NodeKeys = nodeauthtest.SameKey("test-secret")
			stubCopySeekAnchorV3(handler)
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/hw-capabilities" {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				writeJSON(w, http.StatusOK, playback.HWAccelInfo{
					TransportFeatures: tc.features,
					Transformations: []playback.TransformationV3{
						{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3},
					},
				})
			}))
			t.Cleanup(proxy.Close)
			handler.NodePlanner = &recordingNodePlannerV3{plan: nodepool.Plan{ProxyNode: &nodepool.Node{ID: 42, URL: proxy.URL + "/"}}}

			plan := identityProxyPlanV3(playback.DeliveryRemuxProgressiveV3, playback.TransformationV3{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3})
			transport, transportErr := handler.prepareTransportV3(
				httptest.NewRequest(http.MethodPost, "/", nil),
				&playback.Session{ID: "session-remux-range", UserID: 7, ProfileID: "profile-1"},
				v3HandlerFixtureFile(t),
				playback.PlannerResultV3{Plan: plan, PlayMethod: playback.PlayRemux, TranscodeAudio: true, TargetAudioCodec: "aac"},
				mediaAuthModeV3{progressiveRangeRefusal: true})
			if transportErr != nil {
				t.Fatalf("prepare identity transport: %v", transportErr)
			}
			defer transport.rollback()

			if !strings.HasPrefix(transport.url, proxy.URL+"/stream/remux/") {
				t.Fatalf("remux stream url = %q, want a proxy /stream/remux URL", transport.url)
			}
			if got := strings.Contains(transport.url, playback.RemuxRangeQueryParam+"="+playback.RemuxRangeRefuse); got != tc.want {
				t.Fatalf("remux stream url = %q, carries the refusal = %v, want %v", transport.url, got, tc.want)
			}
		})
	}
}

// The resume leading-picture workaround primes the copy from the keyframe the
// plan's timeline starts on, so its URL carries that origin, which can sit
// before the requested seek.
func TestPrepareTransportV3CarriesThePlanOriginForTheResumePrimer(t *testing.T) {
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodeKeys = nodeauthtest.SameKey("test-secret")
	handler.copySeekAnchor = func(context.Context, string, string, float64, int) (float64, int, error) {
		return 38.25, 19, nil
	}
	proxy := capableProxyStubV3(t)
	handler.NodePlanner = &recordingNodePlannerV3{plan: nodepool.Plan{ProxyNode: &nodepool.Node{ID: 42, URL: proxy.URL + "/"}}}

	plan := identityProxyPlanV3(playback.DeliveryRemuxProgressiveV3, playback.TransformationV3{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3})
	plan.Timeline = playback.TimelineV3{SourceStartSeconds: 39.5}
	transport, transportErr := handler.prepareTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		&playback.Session{ID: "session-remux-primer", UserID: 7, ProfileID: "profile-1"},
		v3HandlerFixtureFile(t),
		playback.PlannerResultV3{Plan: plan, PlayMethod: playback.PlayRemux, TranscodeAudio: true, TargetAudioCodec: "aac", RemuxResumeLeadingPictureDrop: true}, mediaAuthModeV3{})
	if transportErr != nil {
		t.Fatalf("prepare identity transport: %v", transportErr)
	}
	defer transport.rollback()

	parsed, err := url.Parse(transport.url)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(transport.url, proxy.URL+"/stream/remux/") {
		t.Fatalf("remux stream url = %q, want the proxy's route", transport.url)
	}
	if origin := playback.RemuxStreamOrigin(parsed.Query()); origin == nil || *origin != 38.25 || parsed.Query().Get("seek") != "39.5" {
		t.Fatalf("remux stream url = %q, want seek=39.5 and the plan's origin 38.25", transport.url)
	}
}

func TestPrepareTransportV3RunsProgressiveRemuxOnTranscodeNodeThroughProxy(t *testing.T) {
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodeKeys = nodeauthtest.SameKey("test-secret")
	stubCopySeekAnchorV3(handler)
	proxyServer := capableProxyStubV3(t)
	transcodeServer := progressiveRemuxExecutionStubV3(t)
	proxy := &nodepool.Node{ID: 42, URL: proxyServer.URL}
	transcode := &nodepool.Node{ID: 84, URL: transcodeServer.URL}
	handler.NodePlanner = &recordingNodePlannerV3{plan: nodepool.Plan{ProxyNode: proxy, TranscodeNode: transcode}}
	recipes := &recordingRecipeCardStoreV3{}
	handler.NodeRecipeStore = recipes
	policy := config.DefaultPlaybackRoutingPolicy()
	policy.RemuxExecution = config.PlaybackExecutionPreferTranscode
	handler.PlaybackConfig = func() config.PlaybackConfig { return config.PlaybackConfig{Routing: policy} }

	plan := identityProxyPlanV3(playback.DeliveryRemuxProgressiveV3)
	transport, transportErr := handler.prepareTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		&playback.Session{ID: "session-remote-remux", UserID: 7, ProfileID: "profile-1"},
		v3HandlerFixtureFile(t),
		playback.PlannerResultV3{Plan: plan, PlayMethod: playback.PlayRemux, TargetAudioCodec: "aac"}, mediaAuthModeV3{})
	if transportErr != nil {
		t.Fatalf("prepare identity transport: %v", transportErr)
	}
	defer transport.rollback()

	prefix := proxy.URL + "/stream/remux/"
	if !strings.HasPrefix(transport.url, prefix) {
		t.Fatalf("stream URL = %q, want proxy remux URL", transport.url)
	}
	if transport.nodeURL != transcode.URL || transport.transportID == "" ||
		transport.routingExecution != noderouting.ExecutionTranscode || transport.routingExecutorID != transcode.ID ||
		transport.routingEgress != noderouting.EgressProxy || transport.routingEgressID != proxy.ID {
		t.Fatalf("prepared route = %#v, want transcode execution through proxy", transport)
	}
	claims, err := streamtoken.Verify(strings.TrimPrefix(transport.url, prefix), handler.JWTSecret)
	if err != nil {
		t.Fatal(err)
	}
	if claims.TranscodeNode != transcode.URL || claims.TranscodeTransportID != transport.transportID ||
		claims.RoutingExecution != string(noderouting.ExecutionTranscode) || claims.RoutingExecutionNodeID != transcode.ID ||
		claims.RoutingEgressNodeID != proxy.ID {
		t.Fatalf("claims = %#v, want bound transcode-to-proxy route", claims)
	}
	stored, ok := recipes.cards[transport.transportID]
	if !ok || stored.TranscodeTransportID != transport.transportID || stored.RoutingExecutionNodeID != transcode.ID || stored.InputPath == "" {
		t.Fatalf("durable remux authority = %#v, want active transport %q", stored, transport.transportID)
	}
}

func TestPrepareTransportV3FallsBackWhenTranscodeRemuxAuthorityWriteFails(t *testing.T) {
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodeKeys = nodeauthtest.SameKey("test-secret")
	stubCopySeekAnchorV3(handler)
	proxyServer := capableProxyStubV3(t)
	transcodeServer := progressiveRemuxExecutionStubV3(t)
	planner := &progressiveFallbackPlannerV3{
		proxy:     &nodepool.Node{ID: 42, URL: proxyServer.URL},
		transcode: &nodepool.Node{ID: 84, URL: transcodeServer.URL},
	}
	handler.NodePlanner = planner
	recipes := &recordingRecipeCardStoreV3{putErr: errors.New("redis is down")}
	handler.NodeRecipeStore = recipes
	policy := config.DefaultPlaybackRoutingPolicy()
	policy.RemuxExecution = config.PlaybackExecutionPreferTranscode
	handler.PlaybackConfig = func() config.PlaybackConfig { return config.PlaybackConfig{Routing: policy} }

	transport, transportErr := handler.prepareTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		&playback.Session{ID: "session-authority-fallback", UserID: 7, ProfileID: "profile-1"},
		v3HandlerFixtureFile(t),
		playback.PlannerResultV3{Plan: identityProxyPlanV3(playback.DeliveryRemuxProgressiveV3), PlayMethod: playback.PlayRemux},
		mediaAuthModeV3{})
	if transportErr != nil {
		t.Fatalf("prepare identity transport: %v", transportErr)
	}
	defer transport.rollback()

	if !strings.HasPrefix(transport.url, proxyServer.URL+"/stream/remux/") ||
		transport.routingExecution != noderouting.ExecutionProxy || transport.nodeURL != "" {
		t.Fatalf("fallback transport = %#v, want proxy-executed remux", transport)
	}
	if len(planner.requests) != 2 || !planner.requests[0].NeedsTranscode || !planner.requests[0].NeedsProxy ||
		planner.requests[1].NeedsTranscode || !planner.requests[1].NeedsProxy {
		t.Fatalf("route requests = %#v, want transcode+proxy then proxy-only", planner.requests)
	}
	if len(planner.released) != 1 || planner.released[0] != "session-authority-fallback" {
		t.Fatalf("released reservations = %v, want failed transcode route released", planner.released)
	}
	if len(recipes.deleted) != 1 {
		t.Fatalf("ambiguous authority cleanup = %v, want failed transport deleted", recipes.deleted)
	}
}

func TestPrepareTransportV3FallsBackWhenSelectedRemuxNodeLosesCapability(t *testing.T) {
	var transcodeCapable atomic.Bool
	transcodeCapable.Store(true)
	transcodeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hw-capabilities" || !transcodeCapable.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusOK, playback.HWAccelInfo{
			TransportFeatures: []string{playback.TransportFeatureProgressiveRemuxExecutionV1},
		})
	}))
	defer transcodeServer.Close()

	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodeKeys = nodeauthtest.SameKey("test-secret")
	stubCopySeekAnchorV3(handler)
	proxyServer := capableProxyStubV3(t)
	planner := &progressiveFallbackPlannerV3{
		proxy:     &nodepool.Node{ID: 42, URL: proxyServer.URL},
		transcode: &nodepool.Node{ID: 84, URL: transcodeServer.URL},
	}
	planner.onPlan = func(request nodepool.RouteRequest) {
		if !request.NeedsTranscode || !transcodeCapable.Swap(false) {
			return
		}
		handler.v3NodeCapabilitiesMu.Lock()
		delete(handler.v3NodeCapabilities, transcodeCapabilityKey(transcodeServer.URL))
		handler.v3NodeCapabilitiesMu.Unlock()
	}
	handler.NodePlanner = planner
	handler.NodeRecipeStore = &recordingRecipeCardStoreV3{}
	policy := config.DefaultPlaybackRoutingPolicy()
	policy.RemuxExecution = config.PlaybackExecutionPreferTranscode
	handler.PlaybackConfig = func() config.PlaybackConfig { return config.PlaybackConfig{Routing: policy} }

	transport, transportErr := handler.prepareTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		&playback.Session{ID: "session-capability-fallback", UserID: 7, ProfileID: "profile-1"},
		v3HandlerFixtureFile(t),
		playback.PlannerResultV3{Plan: identityProxyPlanV3(playback.DeliveryRemuxProgressiveV3), PlayMethod: playback.PlayRemux},
		mediaAuthModeV3{})
	if transportErr != nil {
		t.Fatalf("prepare identity transport: %v", transportErr)
	}
	defer transport.rollback()

	if !strings.HasPrefix(transport.url, proxyServer.URL+"/stream/remux/") ||
		transport.routingExecution != noderouting.ExecutionProxy || transport.nodeURL != "" {
		t.Fatalf("fallback transport = %#v, want proxy-executed remux", transport)
	}
	if len(planner.requests) != 2 || !planner.requests[0].NeedsTranscode || planner.requests[1].NeedsTranscode {
		t.Fatalf("route requests = %#v, want failed transcode route followed by proxy-only fallback", planner.requests)
	}
	if len(planner.released) != 1 || planner.released[0] != "session-capability-fallback" {
		t.Fatalf("released reservations = %v, want failed transcode route released", planner.released)
	}
}

func TestPrepareTransportV3ExcludesTranscodeRemuxWithoutDurableAuthority(t *testing.T) {
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodeKeys = nodeauthtest.SameKey("test-secret")
	stubCopySeekAnchorV3(handler)
	proxyServer := capableProxyStubV3(t)
	transcodeServer := progressiveRemuxExecutionStubV3(t)
	proxy := &nodepool.Node{ID: 42, URL: proxyServer.URL}
	transcode := &nodepool.Node{ID: 84, URL: transcodeServer.URL}
	handler.NodePlanner = &recordingNodePlannerV3{plan: nodepool.Plan{ProxyNode: proxy, TranscodeNode: transcode}}
	policy := config.DefaultPlaybackRoutingPolicy()
	policy.RemuxExecution = config.PlaybackExecutionPreferTranscode
	handler.PlaybackConfig = func() config.PlaybackConfig { return config.PlaybackConfig{Routing: policy} }

	transport, transportErr := handler.prepareTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		&playback.Session{ID: "session-remux-no-authority", UserID: 7, ProfileID: "profile-1"},
		v3HandlerFixtureFile(t),
		playback.PlannerResultV3{Plan: identityProxyPlanV3(playback.DeliveryRemuxProgressiveV3), PlayMethod: playback.PlayRemux}, mediaAuthModeV3{})
	if transportErr != nil {
		t.Fatalf("prepare identity transport: %v", transportErr)
	}
	defer transport.rollback()

	if transport.routingExecution == noderouting.ExecutionTranscode || transport.nodeURL != "" || transport.transportID != "" {
		t.Fatalf("prepared route = %#v, want no transcode execution without a durable node authority store", transport)
	}
}

func TestIdentityStreamURLV3VersionsOnlyBoostedRemuxRoutes(t *testing.T) {
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodeKeys = nodeauthtest.SameKey("test-secret")
	file := v3HandlerFixtureFile(t)
	proxy := &nodepool.Node{URL: "http://proxy-1/"}

	boosted := &playback.Session{
		ID: "boosted", UserID: 7, ProfileID: "profile-1", MediaFileID: file.ID,
		PlayMethod: playback.PlayRemux, TranscodeAudio: true,
		TargetAudioCodec: "aac", SourceAudioChannels: 6, TargetAudioChannels: 2,
	}
	boostedURL, servedByProxy := handler.identityStreamURLV3(boosted, file, proxy, netaccess.Path{})
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
			ordinaryURL, ordinaryByProxy := handler.identityStreamURLV3(&ordinary, file, proxy, netaccess.Path{})
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
			handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
			handler.JWTSecret = "test-stream-signing-secret"
			handler.NodeKeys = nodeauthtest.SameKey("test-stream-signing-secret")
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

func TestPrepareTransportV3RefusesLocalRemuxWhenFallbackDisabled(t *testing.T) {
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodeKeys = nodeauthtest.SameKey("test-secret")
	stubCopySeekAnchorV3(handler)
	handler.NodePlanner = &recordingNodePlannerV3{plan: nodepool.Plan{}}
	requireWorkerRoutingV3(handler)

	_, transportErr := handler.prepareTransportV3(
		httptest.NewRequest(http.MethodPost, "/", nil),
		&playback.Session{ID: "session-remux-refused", UserID: 7, ProfileID: "profile-1"},
		v3HandlerFixtureFile(t),
		playback.PlannerResultV3{
			Plan:           identityProxyPlanV3(playback.DeliveryRemuxProgressiveV3, playback.TransformationV3{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3}),
			PlayMethod:     playback.PlayRemux,
			TranscodeAudio: true,
		}, mediaAuthModeV3{})
	if transportErr == nil || transportErr.reason != string(noderouting.OutcomeCapacityUnavailable) {
		t.Fatalf("transport error = %#v, want route capacity unavailable when worker-only remux has no worker", transportErr)
	}
}

func TestPrepareTransportV3ReleasesProxyReservationOnRollback(t *testing.T) {
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.JWTSecret = "test-secret"
	handler.NodeKeys = nodeauthtest.SameKey("test-secret")
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
		writeJSON(w, http.StatusOK, playback.HWAccelInfo{
			TransportFeatures: []string{playback.TransportFeatureProgressiveRemuxRelayV1},
			Transformations: []playback.TransformationV3{
				{Name: playback.TransformationAudioToAACV3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3},
				{Name: playback.TransformationServerDV7HDR10V3, Executor: playback.ExecutorServerV3, RecipeVersion: playback.TransformationServerDV7HDR10RecipeVersionV3},
			},
		})
	}))
	t.Cleanup(server.Close)
	return server
}

func progressiveRemuxExecutionStubV3(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hw-capabilities" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, playback.HWAccelInfo{
			TransportFeatures: []string{playback.TransportFeatureProgressiveRemuxExecutionV1},
		})
	}))
	t.Cleanup(server.Close)
	return server
}

func TestPrepareTransportV3MarksProxySessionsAsRemotelyTransported(t *testing.T) {
	manager := playback.NewSessionManager(0, 0)
	handler := newKeyedPlaybackHandler(manager)
	handler.JWTSecret = "test-secret"
	handler.NodeKeys = nodeauthtest.SameKey("test-secret")
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

func TestPrepareTransportV3ClearsRemoteTransportMarkWhenServingLocally(t *testing.T) {
	// A session replanned from a proxy onto this server must lose the widened
	// idle grace, or its stream and transcode slots stay held for minutes after
	// a local stream disconnects without an explicit stop.
	manager := playback.NewSessionManager(0, 0)
	handler := newKeyedPlaybackHandler(manager)
	handler.JWTSecret = "test-secret"
	handler.NodeKeys = nodeauthtest.SameKey("test-secret")
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

func TestPlaybackV3ToneMapBudgetsCoverColdNodeWork(t *testing.T) {
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.PlaybackConfig = func() config.PlaybackConfig {
		return config.PlaybackConfig{HWAccel: tonemap.BackendQSV, HWDevice: "/dev/dri/renderD128"}
	}

	if got, want := handler.localToneMapProbeTimeoutV3(), playback.CapabilityEndpointTimeout(tonemap.BackendQSV, "/dev/dri/renderD128"); got != want {
		t.Fatalf("local tone-map probe timeout = %s, want %s", got, want)
	}
	// A node this handler cannot resolve is priced at the cluster's own policy,
	// which is what such a node runs unless it carries an override — closer than
	// a flat guess, and the flat fallback is reserved for having no policy either.
	coldUnknown := playback.CapabilityRequestTimeout(tonemap.BackendQSV, "/dev/dri/renderD128")
	if got := handler.remoteToneMapProbeTimeoutV3("https://unknown.example"); got != coldUnknown {
		t.Fatalf("remote tone-map probe timeout = %s, want %s", got, coldUnknown)
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
	want := coldUnknown + playback.ManifestStartupTimeout +
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
				time.Sleep(requestDelay) //nolint:forbidigo // simulates a slow remote; the test measures the timeout
				w.WriteHeader(test.status)
				if test.status == http.StatusOK {
					_ = json.NewEncoder(w).Encode(playback.HWAccelInfo{})
				}
			}))
			defer remote.Close()

			handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
			startedAt := time.Now()
			_, _ = handler.lookupRemoteCapabilitiesV3(context.Background(), nodeauth.TargetTranscode, remote.URL, false)
			entry := handler.v3NodeCapabilities[transcodeCapabilityKey(remote.URL)]
			minimumLifetime := test.ttl + requestDelay - 20*time.Millisecond
			if lifetime := entry.expiresAt.Sub(startedAt); lifetime < minimumLifetime {
				t.Fatalf("cache lifetime from request start = %s, want at least %s", lifetime, minimumLifetime)
			}
		})
	}
}

// waitForCachedNodeCapabilitiesV3 returns the first successful cache entry the
// background refresh installs for nodeURL.
func waitForCachedNodeCapabilitiesV3(t *testing.T, handler *PlaybackHandler, nodeURL string) v3NodeCapabilityCache {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		handler.v3NodeCapabilitiesMu.Lock()
		entry, ok := handler.v3NodeCapabilities[transcodeCapabilityKey(nodeURL)]
		handler.v3NodeCapabilitiesMu.Unlock()
		if ok && entry.err == nil {
			return entry
		}
		time.Sleep(5 * time.Millisecond) //nolint:forbidigo // polls until the asynchronous write lands
	}
	t.Fatal("no node capability report was cached before the deadline")
	return v3NodeCapabilityCache{}
}

// A report fetched before the health sweep reported the node's hardware changed
// must not be installed after the invalidation that change fired, and the
// invalidation still owes a re-probe: a cache that outlives the hardware it
// describes plans transcodes onto a GPU that is gone.
func TestRefreshNodeCapabilitiesDropsReportFetchedBeforeInvalidation(t *testing.T) {
	var requests atomic.Int64
	firstRequest := make(chan struct{})
	release := make(chan struct{})
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		transformation := "post_change"
		if requests.Add(1) == 1 {
			close(firstRequest)
			<-release
			transformation = "pre_change"
		}
		_ = json.NewEncoder(w).Encode(playback.HWAccelInfo{
			Transformations: []playback.TransformationV3{{Name: transformation}},
		})
	}))
	defer remote.Close()

	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.refreshRemoteCapabilitiesV3(transcodeCapabilityKey(remote.URL))
	<-firstRequest

	// The sweep's callback lands while the earlier probe is still outstanding.
	handler.RefreshNodeCapabilitiesV3(remote.URL)
	close(release)

	entry := waitForCachedNodeCapabilitiesV3(t, handler, remote.URL)
	if len(entry.transformations) != 1 || entry.transformations[0].Name != "post_change" {
		t.Fatalf("cached transformations = %v, want the report fetched after the invalidation", entry.transformations)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("node was probed %d time(s), want the invalidation's own re-probe and no more", got)
	}
}

func TestRemoteToneMapProbeTimeoutUsesTargetNodeBudget(t *testing.T) {
	probeTimeoutMillis := int64(161000)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(playback.HWAccelInfo{ProbeRequestTimeoutMillis: probeTimeoutMillis})
	}))
	defer remote.Close()
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.PlaybackConfig = func() config.PlaybackConfig {
		return config.PlaybackConfig{HWAccel: tonemap.BackendQSV, HWDevice: "/central/device/one"}
	}

	if _, err := handler.lookupRemoteCapabilitiesV3(context.Background(), nodeauth.TargetTranscode, remote.URL, false); err != nil {
		t.Fatal(err)
	}
	// The node's own figure is what this path exists to use. It has to exceed
	// what the cluster setting prices, or the floor below it would be answering
	// and this would pass without the target node being consulted at all.
	if cluster := playback.CapabilityRequestTimeout(tonemap.BackendQSV, "/central/device/one"); cluster >= 161*time.Second {
		t.Fatalf("fixture is inert: the cluster price %s must stay under the node's 161s", cluster)
	}
	if got, want := handler.remoteToneMapProbeTimeoutV3(remote.URL), 161*time.Second; got != want {
		t.Fatalf("remote probe timeout = %s, want target node budget %s", got, want)
	}
	request := transcodenode.TranscodeStartRequest{ToneMapMode: tonemap.ModeHardware}
	if got, want := handler.remotePlaybackTransportTimeout(remote.URL, request), 161*time.Second+playback.ManifestStartupTimeout; got != want {
		t.Fatalf("remote start timeout = %s, want target node budget %s", got, want)
	}

	handler.v3NodeCapabilitiesMu.Lock()
	delete(handler.v3NodeCapabilities, transcodeCapabilityKey(remote.URL))
	handler.v3NodeCapabilitiesMu.Unlock()
	probeTimeoutMillis = (24 * time.Hour).Milliseconds()
	if _, err := handler.lookupRemoteCapabilitiesV3(context.Background(), nodeauth.TargetTranscode, remote.URL, false); err != nil {
		t.Fatal(err)
	}
	// Still bounded — the value comes off the wire from a worker — but at the
	// ceiling the probe formula produces rather than a round number that a real
	// nine-device node already exceeds.
	if got, want := handler.remoteToneMapProbeTimeoutV3(remote.URL), playback.MaxCapabilityRequestTimeout(); got != want {
		t.Fatalf("bounded remote probe timeout = %s, want %s", got, want)
	}
}

// v3NodeLookupPlanner plans nothing and only resolves nodes by URL, which is all
// a cold probe budget needs from a planner.
type v3NodeLookupPlanner struct {
	node *nodepool.Node
}

func (p *v3NodeLookupPlanner) PlanSession(string, string, bool, int) nodepool.Plan {
	return nodepool.Plan{}
}

func (p *v3NodeLookupPlanner) TranscodeNodeByURL(nodeURL string) (*nodepool.Node, bool) {
	if p.node == nil || p.node.URL != nodeURL {
		return nil, false
	}
	return p.node, true
}

// The first read of a node after an API restart is also its most expensive: no
// probe cache on the node survives, so the whole matrix runs. The flat fallback
// is shorter than a two-device node legitimately takes, and paying it would
// cancel exactly the multi-GPU nodes this path exists to plan onto — so the
// durable report the node last advertised is what prices it.
func TestPlaybackV3ColdNodeProbeBudgetComesFromTheStoredReport(t *testing.T) {
	const nodeURL = "https://gpu-1.example"
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.PlaybackConfig = func() config.PlaybackConfig {
		return config.PlaybackConfig{HWAccel: tonemap.BackendQSV, HWDevice: "/dev/dri/renderD128"}
	}
	handler.NodePlanner = &v3NodeLookupPlanner{node: &nodepool.Node{
		ID: 1, URL: nodeURL,
		Capabilities: json.RawMessage(`{"resolved":"qsv","probe_request_timeout_ms":163000}`),
	}}

	if got, want := handler.remoteToneMapProbeTimeoutV3(nodeURL), 163*time.Second; got != want {
		t.Fatalf("cold probe timeout = %s, want the node's advertised %s", got, want)
	}
	if remoteNodeProbeFallbackTimeout >= 163*time.Second {
		t.Fatal("fixture is inert: the advertised budget must exceed the flat fallback")
	}
}

// A per-node policy edit invalidates the inventory through the same path a
// hardware change does, and the learned budget deliberately survives that. But
// widening hw_device_override is exactly the change that makes the learned
// number too small: the node reloads, walks four devices instead of one, and is
// canceled at the one-device deadline on every retry — with no way back, since a
// budget is only learned from a read that completes.
func TestPlaybackV3RepricesALearnedBudgetAfterTheDeviceSetGrows(t *testing.T) {
	const nodeURL = "https://gpu-3.example"
	devices := "/dev/dri/renderD128,/dev/dri/renderD129,/dev/dri/renderD130,/dev/dri/renderD131"
	backend := tonemap.BackendQSV
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.PlaybackConfig = func() config.PlaybackConfig {
		return config.PlaybackConfig{HWAccel: tonemap.BackendQSV, HWDevice: "/dev/dri/renderD128"}
	}
	handler.NodePlanner = &v3NodeLookupPlanner{node: &nodepool.Node{
		ID: 3, URL: nodeURL, HWAccelOverride: &backend, HWDeviceOverride: &devices,
	}}
	// Learned while the node was still on the cluster's single device.
	learned := playback.CapabilityRequestTimeout(backend, "/dev/dri/renderD128")
	handler.v3NodeCapabilitiesMu.Lock()
	handler.rememberNodeProbeBudgetLockedV3(nodeURL, learned)
	handler.v3NodeCapabilitiesMu.Unlock()

	handler.RefreshNodeCapabilitiesV3(nodeURL)

	want := playback.CapabilityRequestTimeout(backend, devices)
	if got := handler.remoteToneMapProbeTimeoutV3(nodeURL); got != want {
		t.Fatalf("probe timeout after the override grew = %s, want the four-device %s", got, want)
	}
	if want <= learned {
		t.Fatalf("fixture is inert: the four-device budget %s must exceed the learned %s", want, learned)
	}
}

// The admin route invalidates with the URL exactly as the row stores it, which
// may carry a trailing slash the pools have already dropped. Keyed verbatim, the
// entry this deletes is not the entry planning reads, so the node keeps serving
// the backend it was just moved off until the old key expires.
func TestRefreshNodeCapabilitiesV3NormalizesTheCacheKey(t *testing.T) {
	const nodeURL = "https://gpu-5.example"
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.v3NodeCapabilitiesMu.Lock()
	if handler.v3NodeCapabilities == nil {
		handler.v3NodeCapabilities = map[v3NodeCapabilityKey]v3NodeCapabilityCache{}
	}
	handler.v3NodeCapabilities[transcodeCapabilityKey(nodeURL)] = v3NodeCapabilityCache{expiresAt: time.Now().Add(time.Hour)}
	handler.v3NodeCapabilitiesMu.Unlock()

	handler.RefreshNodeCapabilitiesV3(nodeURL + "/")

	handler.v3NodeCapabilitiesMu.Lock()
	_, present := handler.v3NodeCapabilities[transcodeCapabilityKey(nodeURL)]
	invalidations := handler.v3NodeCapabilityInvalidations[nodeURL]
	handler.v3NodeCapabilitiesMu.Unlock()
	if present {
		t.Fatal("the canonical entry survived an invalidation made with a trailing slash")
	}
	if invalidations == 0 {
		t.Fatal("the invalidation was counted under a different key than planning reads")
	}
}

func startPlaybackV2IntoRecorder(t *testing.T, h *PlaybackHandler, rr *httptest.ResponseRecorder, request playback.StartRequestV3) {
	t.Helper()
	h.InstallationID = serviceInstallation
	response, err := h.StartPlaybackV2(newAuthorizedPlaybackContext(), PlaybackCaller{UserID: 1, ProfileID: "profile-1", InstallationID: serviceInstallation}, request)
	if err != nil {
		t.Fatal(err)
	}
	writeJSON(rr, http.StatusCreated, response)
}

// writePlaybackTestFFmpegFailingOn writes a fake FFmpeg that prints failMessage
// and exits before its first manifest when its arguments contain failPattern,
// and otherwise behaves like writePlaybackTestFFmpeg. Every real transcode
// invocation is logged.
func writePlaybackTestFFmpegFailingOn(t *testing.T, failPattern, failMessage string) (ffmpegPath, logPath string) {
	t.Helper()
	dir := t.TempDir()
	ffmpegPath = filepath.Join(dir, "fake-ffmpeg.sh")
	logPath = filepath.Join(dir, "invocations.log")
	script := "#!/bin/sh\n" +
		"last=\"\"\n" +
		"for arg in \"$@\"; do last=\"$arg\"; done\n" +
		"case \"$last\" in *.m3u8) printf '%s\\n' \"$*\" >> \"" + logPath + "\" ;; esac\n" +
		"case \"$*\" in *'" + failPattern + "'*) echo '" + failMessage + "' >&2; exit 1 ;; esac\n" +
		"case \"$last\" in\n" +
		"  *.m3u8) out=\"$(dirname \"$last\")\"; mkdir -p \"$out\"; " +
		"printf x > \"$out/init.mp4\"; printf x > \"$out/seg_0.m4s\"; " +
		"printf x > \"$out/seg_1.m4s\"; printf x > \"$out/seg_2.m4s\"; " +
		"printf '#EXTM3U\\n#EXT-X-VERSION:7\\n#EXT-X-TARGETDURATION:2\\n" +
		"#EXT-X-MEDIA-SEQUENCE:0\\n#EXT-X-MAP:URI=\"init.mp4\"\\n" +
		"#EXTINF:2.0,\\nseg_0.m4s\\n#EXTINF:2.0,\\nseg_1.m4s\\n" +
		"#EXTINF:2.0,\\nseg_2.m4s\\n' > \"$last\" ;;\n" +
		"esac\n" +
		"exec sleep 30\n"
	if err := os.WriteFile(ffmpegPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	return ffmpegPath, logPath
}

func readPlaybackTestFFmpegInvocations(t *testing.T, logPath string) []string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read ffmpeg invocations: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// autoNVENCPipelineSeamV3 stands in for hw_accel=auto resolving to NVENC, which
// the test host cannot probe.
func autoNVENCPipelineSeamV3(t *testing.T) func(context.Context, playback.TranscodeOpts) *playback.AutoTranscodePipeline {
	return func(_ context.Context, opts playback.TranscodeOpts) *playback.AutoTranscodePipeline {
		if opts.HWAccel != "auto" {
			t.Errorf("pipeline built from HWAccel %q, want configured auto", opts.HWAccel)
		}
		opts.HWAccel = "nvenc"
		return playback.NewResolvedAutoTranscodePipelineForTest(opts)
	}
}

// prepareLocalTransportForTestV3 starts a local H.264 transcode under the
// configured hwAccel. Under auto it stands in for auto resolving to NVENC.
func prepareLocalTransportForTestV3(t *testing.T, hwAccel, ffmpegPath, sessionID string) (*PlaybackHandler, preparedTransportV3, *transportErrorV3) {
	t.Helper()
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
	transcodeDir := t.TempDir()
	handler.PlaybackConfig = func() config.PlaybackConfig {
		return config.PlaybackConfig{FFmpegPath: ffmpegPath, TranscodeDir: transcodeDir, TranscodeEnabled: true, HWAccel: hwAccel}
	}
	if hwAccel == "auto" {
		handler.autoTranscodePipelineV3 = autoNVENCPipelineSeamV3(t)
	}
	file := v3HandlerFixtureFile(t)
	result := playback.PlannerResultV3{
		Plan:       &playback.PlanV3{PlanID: "plan:" + sessionID, Delivery: playback.DeliveryTranscodeHLSV3},
		PlayMethod: playback.PlayTranscode, TargetVideoCodec: "h264", TargetAudioCodec: "aac", TargetResolution: "720p",
		SubtitleTrackIndex: -1, SubtitleTransportTrackIndex: -1,
	}
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	timeline, timelineErr := handler.prepareTransportTimelineV3(request.Context(), &playback.Session{ID: sessionID}, file, result)
	if timelineErr != nil {
		t.Fatalf("prepare timeline: %v", timelineErr)
	}
	transport, transportErr := handler.prepareLocalTransportV3(request, &playback.Session{ID: sessionID, UserID: 7, ProfileID: "profile-1"}, file, result, timeline, mediaAuthModeV3{})
	return handler, transport, transportErr
}

func TestPrepareLocalTransportV3AutoKeepsGPUEncodeWithCPUDecode(t *testing.T) {
	ffmpegPath, logPath := writePlaybackTestFFmpegFailingOn(t, "-hwaccel cuda", "intentional hardware failure")
	handler, transport, transportErr := prepareLocalTransportForTestV3(t, "auto", ffmpegPath, "session-auto-mixed")
	if transportErr != nil {
		t.Fatalf("prepare auto local transport: %v (cause: %v)", transportErr, transportErr.cause)
	}
	if commitErr := transport.commit(); commitErr != nil {
		t.Fatalf("commit: %v", commitErr)
	}
	defer handler.tm.CloseTranscodeSession("session-auto-mixed", "")

	live := handler.tm.GetTranscodeSession("session-auto-mixed")
	if live == nil {
		t.Fatal("committed transport did not register a transcode session")
	}
	if opts := live.Opts(); opts.HWAccel != "nvenc" || !opts.SoftwareVideoDecode {
		t.Fatalf("live session = %s software decode %v, want NVENC with CPU decode", opts.HWAccel, opts.SoftwareVideoDecode)
	}
	if transport.hwAccel != "nvenc" {
		t.Fatalf("transport hwAccel = %q, want nvenc", transport.hwAccel)
	}
	invocations := readPlaybackTestFFmpegInvocations(t, logPath)
	if len(invocations) != 2 || !strings.Contains(invocations[0], "-hwaccel cuda") ||
		strings.Contains(invocations[1], "-hwaccel cuda") || !strings.Contains(invocations[1], "h264_nvenc") {
		t.Fatalf("invocations = %q, want full hardware then CPU decode with NVENC", invocations)
	}
}

func TestPrepareLocalTransportV3AutoTerminalFailureKeepsMainError(t *testing.T) {
	ffmpegPath, logPath := writePlaybackTestFFmpegFailingOn(t, "-f hls", "intentional hardware failure")
	_, transport, transportErr := prepareLocalTransportForTestV3(t, "auto", ffmpegPath, "session-auto-terminal")
	if transportErr == nil {
		transport.rollback()
		t.Fatal("failed ffmpeg startup returned a playable transport")
	}
	if transportErr.reason != transcodeStartFailedReasonV3 || transportErr.retryable {
		t.Fatalf("transport error = %#v, want stable non-retryable startup terminal", transportErr)
	}
	if transportErr.cause == nil || !strings.Contains(transportErr.cause.Error(), "intentional hardware failure") {
		t.Fatalf("startup error lost ffmpeg cause: %#v", transportErr)
	}
	invocations := readPlaybackTestFFmpegInvocations(t, logPath)
	if len(invocations) != 3 || !strings.Contains(invocations[2], "libx264") {
		t.Fatalf("invocations = %q, want full hardware, mixed, then software", invocations)
	}
}

// An explicit NVENC setting on a card whose NVDEC cannot decode the source
// retried the identical CUDA decode. It now decodes on the CPU and keeps NVENC.
func TestPrepareLocalTransportV3NVENCRetriesRejectedDecodeOnCPU(t *testing.T) {
	ffmpegPath, logPath := writePlaybackTestFFmpegFailingOn(t, "-hwaccel cuda", "[hevc @ 0x1] Hardware is lacking required capabilities")
	handler, transport, transportErr := prepareLocalTransportForTestV3(t, "nvenc", ffmpegPath, "session-nvenc-decode")
	if transportErr != nil {
		t.Fatalf("prepare local transport: %v (cause: %v)", transportErr, transportErr.cause)
	}
	if commitErr := transport.commit(); commitErr != nil {
		t.Fatalf("commit: %v", commitErr)
	}
	defer handler.tm.CloseTranscodeSession("session-nvenc-decode", "")

	invocations := readPlaybackTestFFmpegInvocations(t, logPath)
	if len(invocations) != 2 || !strings.Contains(invocations[0], "-hwaccel cuda") ||
		strings.Contains(invocations[1], "-hwaccel cuda") || !strings.Contains(invocations[1], "h264_nvenc") {
		t.Fatalf("invocations = %q, want CUDA decode then CPU decode with NVENC", invocations)
	}
}

type failingProgressStoreV3 struct {
	userstore.UserStore
	err error
}

func (s failingProgressStoreV3) GetProgress(context.Context, string, string) (*userstore.WatchProgress, error) {
	return nil, s.err
}

func TestResumePositionV3PropagatesProgressReadFailure(t *testing.T) {
	wantErr := errors.New("progress store unavailable")
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.StoreProvider = testUserStoreProvider{store: failingProgressStoreV3{err: wantErr}}
	file := &models.MediaFile{ContentID: "movie-1"}

	if _, err := handler.resumePositionV3(context.Background(), 1, "profile-1", file); !errors.Is(err, wantErr) {
		t.Fatalf("resumePositionV3 error = %v, want %v", err, wantErr)
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
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
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
	if err := handler.attachSubtitleArtifactV3(context.Background(), "session-frozen-subtitle", file, plan, selectedIndex, &recipe, nil); err != nil {
		t.Fatalf("attach frozen downloaded subtitle: %v", err)
	}
	if plan.Subtitle.Artifact == nil || !strings.Contains(plan.Subtitle.Artifact.URL, "downloaded_subtitle_id=71") {
		t.Fatalf("artifact = %#v, want frozen downloaded subtitle 71", plan.Subtitle.Artifact)
	}
	t.Run("restored inventory includes a newly added stored track", func(t *testing.T) {
		repo.listErr = nil
		repo.list = []subtitles.DownloadedSubtitle{
			*repo.subtitles[71],
			{ID: 72, MediaFileID: file.ID, Format: subtitles.FormatSRT},
		}
		before := playback.PlanV3{
			Subtitle: playback.SubtitleDecisionV3{
				Mode:      playback.SubtitleRenderV3,
				Inventory: playback.BuildSubtitleInventoryV3(file, downloadedSubtitleEntriesV3(file, repo.list[:1])),
			},
			Timeline: playback.TimelineV3{StreamOriginSeconds: 321},
		}
		raw, err := json.Marshal(before)
		if err != nil {
			t.Fatal(err)
		}
		var restored playback.PlanV3
		if err := json.Unmarshal(raw, &restored); err != nil {
			t.Fatal(err)
		}
		if err := handler.attachSubtitleArtifactV3(t.Context(), "session-frozen-subtitle", file, &restored, selectedIndex, &recipe, nil); err != nil {
			t.Fatal(err)
		}
		if len(restored.Subtitle.Inventory) != 2 || !strings.Contains(restored.Subtitle.Inventory[1].URL, "downloaded_subtitle_id=72") {
			t.Fatalf("restored menu lost newly added subtitle 72: %+v", restored.Subtitle.Inventory)
		}
		if restored.Subtitle.Artifact == nil || !strings.Contains(restored.Subtitle.Artifact.URL, "downloaded_subtitle_id=71") || restored.Subtitle.Mode != before.Subtitle.Mode || restored.Timeline != before.Timeline {
			t.Fatalf("restoring the menu changed the selected recipe: %+v", restored)
		}
	})

}

func newVersionChangeFixtureV3(t *testing.T, configure ...func(*versionChangeFixtureV3)) *versionChangeFixtureV3 {
	t.Helper()
	source := uhdSourceFileV3(t)
	source.AudioTracks = []models.AudioTrack{
		{Language: "en", Codec: "aac", Channels: 6, Layout: "5.1", Default: true},
		{Language: "ja", Codec: "aac", Channels: 6, Layout: "5.1"},
	}
	source.ExternalSubtitles = []models.ExternalSubtitle{{Path: writePlaybackTestMediaFile(t, "movie.eng.ass"), Language: "eng", Format: "ass"}}
	alternate := lowerAlternateFileV3(source)
	alternate.AudioTracks = []models.AudioTrack{
		{Language: "ja", Codec: "aac", Channels: 2, Layout: "stereo"},
		{Language: "en", Codec: "aac", Channels: 2, Layout: "stereo", Default: true},
	}
	alternate.ExternalSubtitles = nil

	store := newPlaybackTestStore(t)
	files := map[int]*models.MediaFile{source.ID: source, alternate.ID: alternate}
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0), mapPlaybackFileResolver{files: files})
	handler.FileVersionFetcher = testPlaybackFileVersionFetcher{byContent: map[string][]*models.MediaFile{source.ContentID: {source, alternate}}}
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "false"}}
	handler.StoreProvider = testUserStoreProvider{store: store}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	startRequest := v3HandlerStartRequest()
	startRequest.QualityPreference = "auto"
	startRequest.Capabilities.MaxResolution = "2160p"
	startRequest.Capabilities.VideoDecode[0].Levels = []int{51}
	startRequest.Capabilities.VideoDecode[0].MaxWidth = 3840
	startRequest.Capabilities.VideoDecode[0].MaxHeight = 2160
	startRequest.Capabilities.VideoDecode[0].MaxBitrateKbps = 50_000
	japanese, subtitleIndex := 1, 0
	startRequest.AudioTrackID = playback.TrackIDV3(source.ID, "audio", japanese)
	startRequest.AudioTrackIndex = &japanese
	startRequest.SubtitleTrackID = playback.TrackIDV3(source.ID, "subtitle", subtitleIndex)
	startRequest.SubtitleTrackIndex = &subtitleIndex
	for _, delivery := range []string{playback.DeliveryClassOriginalHTTPV3, playback.DeliveryClassHLSV3} {
		startRequest.ClientPlaybackContext.Deliveries[delivery] = playback.DeliveryCapabilityV3{
			Enabled: true, SupportedOnDevice: true,
			Subtitles:       playback.DeliverySubtitleCapabilitiesV3{SidecarText: true, ASSStyling: true},
			ValidatedClaims: []string{playback.ClaimClientSelectedAudioTrackV3},
		}
	}
	f := &versionChangeFixtureV3{handler: handler, store: store, source: source, alternate: alternate, startRequest: startRequest}
	for _, apply := range configure {
		apply(f)
	}
	// Starts and replans save no audio preference.
	rr := httptest.NewRecorder()
	serveStartPlaybackV2(t, handler, rr, httptest.NewRequest(http.MethodPost, "/api/v2/playback/start", strings.NewReader(marshalV3StartRequest(t, f.startRequest))).WithContext(WithNativeAPIV2(newAuthorizedPlaybackContext())))
	if rr.Code != http.StatusCreated || json.Unmarshal(rr.Body.Bytes(), &f.started) != nil || f.started.PlaybackPlan == nil {
		t.Fatalf("start status = %d, body = %s", rr.Code, rr.Body.String())
	}
	return f
}

func (f *versionChangeFixtureV3) replan(t *testing.T, previous playback.DecisionResponseV3, id string, operation playback.ReplanOperationV3, quality string, tracks playback.SelectedTracksV3) playback.DecisionResponseV3 {
	t.Helper()
	key := playback.PlanAttemptKeyV3(*previous.PlaybackPlan, f.startRequest.ClientPlaybackContext.Output.OutputContextID, nil)
	request := playback.ReplanRequestV3{
		ProtocolVersion: playback.ProtocolV3, Operation: operation,
		PlaybackAttemptID: f.startRequest.PlaybackAttemptID, ReplanRequestID: id,
		FailedPlanID: previous.PlaybackPlan.PlanID, PlanAttemptID: id + "-attempt",
		PlanAttemptKey: key, AttemptCount: 1, QualityPreference: quality,
		SelectedTracks:        tracks,
		Capabilities:          f.startRequest.Capabilities,
		ClientPlaybackContext: f.startRequest.ClientPlaybackContext,
	}
	switch operation {
	case playback.ReplanOperationQualityChangeV3:
		request.AttemptedPlanKeys = []string{key}
	case playback.ReplanOperationSeekFailureRecoveryV3:
		request.Failure = playback.FailureV3{Classification: "decoder_failure"}
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	rr := replanPlaybackRecorderV2(t, f.handler, previous.SessionID, body)
	var response playback.DecisionResponseV3
	if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &response) != nil || response.Terminal != nil || response.PlaybackPlan == nil {
		t.Fatalf("%s status = %d, body = %s", id, rr.Code, rr.Body.String())
	}
	return response
}

func (f *versionChangeFixtureV3) qualityChange(t *testing.T, previous playback.DecisionResponseV3, id, quality string) playback.DecisionResponseV3 {
	t.Helper()
	return f.replan(t, previous, id, playback.ReplanOperationQualityChangeV3, quality, playback.SelectedTracksV3{Audio: previous.PlaybackPlan.SelectedTracks.Audio})
}

func TestReplanPlaybackV2TriesLater4KAlternateAfterNon4KTerminal(t *testing.T) { //nolint:funlen // ported unchanged from the retired v1 handler test
	for _, fixed := range []bool{false, true} {
		t.Run(fmt.Sprintf("fixed=%t", fixed), func(t *testing.T) {
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
			handler := newKeyedPlaybackHandler(manager, mapPlaybackFileResolver{files: files})
			handler.FileVersionFetcher = testPlaybackFileVersionFetcher{byContent: map[string][]*models.MediaFile{
				source.ContentID: {source, lower, playable},
			}}
			handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "false"}}
			handler.PlaybackConfig = playbackTestConfig("", "")
			handler.ItemAccess = allowAllPlaybackItemAccess{}

			startRequest := v3HandlerStartRequest()
			if fixed {
				startRequest.AllowAlternateVersions = new(false)
			}
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
			startPlaybackV2IntoRecorder(t, handler, startRR, startRequest)
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
			replanned := replanPlaybackDecisionV2(t, handler, started.SessionID, playback.ReplanRequestV3{
				ProtocolVersion: playback.ProtocolV3, Operation: playback.ReplanOperationFailureRecoveryV3,
				PlaybackAttemptID: startRequest.PlaybackAttemptID, ReplanRequestID: "later-4k-replan-0001",
				FailedPlanID: started.PlaybackPlan.PlanID, PlanAttemptID: "later-4k-plan-0001",
				PlanAttemptKey: currentKey, AttemptedPlanKeys: []string{currentKey}, AttemptCount: 1,
				QualityPreference: "auto", SelectedTracks: started.PlaybackPlan.SelectedTracks,
				Failure:      playback.FailureV3{Classification: "playback_error"},
				Capabilities: replanCapabilities, ClientPlaybackContext: startRequest.ClientPlaybackContext,
			})
			if fixed {
				if replanned.Terminal == nil || replanned.PlaybackPlan != nil {
					t.Fatalf("fixed source unexpectedly changed versions: %+v", replanned)
				}
				record, err := handler.PlanStoreV3.GetAttempt(t.Context(), started.SessionID)
				if err != nil || record.EffectiveMediaFileID != source.ID || record.NormalizedRequest.AllowsAlternateVersions() {
					t.Fatalf("fixed source was not retained: record=%+v error=%v", record, err)
				}
				return
			}

			if replanned.Terminal != nil {
				t.Fatalf("replan terminal = %+v", *replanned.Terminal)
			}
			if replanned.PlaybackPlan == nil {
				t.Fatalf("replan = %#v", replanned)
			}
			if replanned.PlaybackPlan.EffectiveMediaFileID != playable.ID {
				t.Fatalf("effective file = %d, want later directly playable 4K alternate %d", replanned.PlaybackPlan.EffectiveMediaFileID, playable.ID)
			}

		})
	}
}

func TestStartPlaybackV2CommitsStartSideEffectsOnce(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	file.EpisodeID = "episode-1"
	file.MediaFolderID = 7
	scrobbler := &recordingPlaybackWatchScrobbler{}
	analyzer := &fakePlaybackIntroAnalyzer{started: make(chan struct{}, 1)}
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
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
		response := startPlayableDecisionV2(t, handler, body)
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

func TestStartPlaybackV2DoesNotWaitForSessionSync(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	syncer := &blockingPlaybackSessionSyncerV3{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.SessionSyncer = syncer
	body := marshalV3StartRequest(t, v3HandlerStartRequest())
	responseReady := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rr := httptest.NewRecorder()
		serveStartPlaybackV2(t, handler, rr, httptest.NewRequest(http.MethodPost, "/api/v2/playback/start", strings.NewReader(body)).WithContext(newAuthorizedPlaybackContext()))
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
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.WatchScrobbler = scrobbler
	body := marshalV3StartRequest(t, v3HandlerStartRequest())
	response := startPlaybackDecisionV2(t, handler, body)
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
		handler.finalizeSessionStopWithResult(context.Background(), session, false, "", PlaybackEndStopped)
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

func TestStartPlaybackV2RejectsAttemptReplayFromAnotherDevice(t *testing.T) {
	manager := playback.NewSessionManager(0, 0)
	handler := newKeyedPlaybackHandler(manager, testPlaybackFileResolver{file: v3HandlerFixtureFile(t)})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	body := marshalV3StartRequest(t, v3HandlerStartRequest())

	start := func(deviceID string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v2/playback/start", strings.NewReader(body)).WithContext(newAuthorizedPlaybackContext())
		req.Header.Set(deviceIDHeader, deviceID)
		rr := httptest.NewRecorder()
		serveStartPlaybackV2(t, handler, rr, req)
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

func TestReplanPlaybackV2CannotDowngradeHeaderAuthenticatedAttempt(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	manager := playback.NewSessionManager(0, 0)
	handler := newKeyedPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	handler.JWTSecret = "test-stream-signing-secret"
	handler.NodeKeys = nodeauthtest.SameKey("test-stream-signing-secret")
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "true"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}

	start := v3HandlerStartRequest()
	start.ClientFeatures = append(start.ClientFeatures, playback.FeatureHeaderAuthenticatedMediaV3)
	started := startPlayableDecisionV2(t, handler, marshalV3StartRequest(t, start))

	nextContext := start.ClientPlaybackContext
	nextContext.Output.OutputContextID = "route-2"
	replanned := replanPlaybackDecisionV2(t, handler, started.SessionID, playback.ReplanRequestV3{
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

func TestStartPlaybackV2PersistsAndReplaysTerminalDecision(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"transcode_enabled": "false"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	request := v3HandlerStartRequest()
	request.Capabilities.CodecsVideo = nil
	request.Capabilities.CodecsVideoHardware = nil
	request.Capabilities.VideoDecode = nil
	request.Capabilities.Containers = nil
	body := marshalV3StartRequest(t, request)

	start := func(payload string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v2/playback/start", strings.NewReader(payload)).WithContext(newAuthorizedPlaybackContext())
		rr := httptest.NewRecorder()
		serveStartPlaybackV2(t, handler, rr, req)
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

func TestStartPlaybackV2RejectsProfileMismatch(t *testing.T) {
	manager := playback.NewSessionManager(0, 0)
	handler := newKeyedPlaybackHandler(manager, testPlaybackFileResolver{file: v3HandlerFixtureFile(t)})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{}}
	request := v3HandlerStartRequest()
	request.ProfileID = "profile-other"
	req := httptest.NewRequest(http.MethodPost, "/api/v2/playback/start", strings.NewReader(marshalV3StartRequest(t, request))).WithContext(newAuthorizedPlaybackContext())
	rr := httptest.NewRecorder()
	serveStartPlaybackV2(t, handler, rr, req)
	if rr.Code != http.StatusBadRequest || len(manager.AllSessions()) != 0 {
		t.Fatalf("status = %d, sessions = %d, body = %s", rr.Code, len(manager.AllSessions()), rr.Body.String())
	}
}

func TestReplanPlaybackV2UpdatesSelectedAudioAndReplaysIdempotently(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	file.AudioTracks = append(file.AudioTracks, models.AudioTrack{Codec: "aac", Channels: 2, Layout: "stereo", Language: "spa"})
	manager := playback.NewSessionManager(0, 0)
	handler := newKeyedPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	handler.JWTSecret = "test-secret"
	handler.NodeKeys = nodeauthtest.SameKey("test-secret")
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
	started := startPlaybackDecisionV2(t, handler, startBody)
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
	time.Sleep(time.Until(time.Unix(time.Now().Unix()+1, 0)) + 10*time.Millisecond) //nolint:forbidigo // must cross a real JWT NumericDate second boundary
	audioIndex := 1
	bandwidthEstimate := 3_500
	bandwidthCap := 10_000
	failedKey := playback.PlanAttemptKeyV3(*started.PlaybackPlan, startRequest.ClientPlaybackContext.Output.OutputContextID, nil)
	replan := playback.ReplanRequestV3{ProtocolVersion: playback.ProtocolV3, PlaybackAttemptID: startRequest.PlaybackAttemptID, ReplanRequestID: "replan-0001", FailedPlanID: started.PlaybackPlan.PlanID, PlanAttemptID: "plan-attempt-0001", PlanAttemptKey: failedKey, AttemptedPlanKeys: []string{failedKey}, AttemptCount: 1, QualityPreference: "original", PositionSeconds: 12, Metered: true, BandwidthEstimateKbps: &bandwidthEstimate, BandwidthCapKbps: &bandwidthCap, SelectedTracks: playback.SelectedTracksV3{Audio: &playback.TrackIdentityV3{ID: playback.TrackIDV3(file.ID, "audio", audioIndex), Index: &audioIndex}}, Failure: playback.FailureV3{Classification: "audio_renderer_error"}, Capabilities: startRequest.Capabilities, ClientPlaybackContext: startRequest.ClientPlaybackContext}
	replan.ClientPlaybackContext.AppBuild = "nightly\x00secret"
	replan.ClientPlaybackContext.AppChannel = "beta\x00secret"
	replanBody, err := json.Marshal(replan)
	if err != nil {
		t.Fatal(err)
	}

	call := func() playback.DecisionResponseV3 {
		rr := replanPlaybackRecorderV2(t, handler, started.SessionID, replanBody)
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
	retried := startPlayableDecisionV2(t, handler, startBody)
	if retried.PlaybackPlan.PlanID != started.PlaybackPlan.PlanID {
		t.Fatalf("start replay plan = %q, want original %q", retried.PlaybackPlan.PlanID, started.PlaybackPlan.PlanID)
	}
	replan.PositionSeconds++
	conflictRR := replanPlaybackRequestRecorderV2(t, handler, started.SessionID, replan)
	if conflictRR.Code != http.StatusConflict || !strings.Contains(conflictRR.Body.String(), "idempotency_key_reused") {
		t.Fatalf("conflict status = %d, body = %s", conflictRR.Code, conflictRR.Body.String())
	}
}

func TestReplanPlaybackV2FailureDoesNotReplaceDurableStartDecision(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	manager := playback.NewSessionManager(0, 0)
	handler := newKeyedPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	startRequest := v3HandlerStartRequest()
	startBody := marshalV3StartRequest(t, startRequest)
	started := startPlayableDecisionV2(t, handler, startBody)

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
	failed := replanPlaybackDecisionV2(t, handler, started.SessionID, replan)
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
	replayedTerminal := replanPlaybackDecisionV2(t, handler, started.SessionID, replan)
	if replayedTerminal.Terminal == nil || replayedTerminal.Terminal.Reason != failed.Terminal.Reason {
		t.Fatalf("terminal replan replay = %#v, want %#v", replayedTerminal, failed)
	}

	replayed := startPlayableDecisionV2(t, handler, startBody)
	if replayed.PlaybackPlan.PlanID != started.PlaybackPlan.PlanID || replayed.Terminal != nil {
		t.Fatalf("start replay = %#v, want original plan %q", replayed, started.PlaybackPlan.PlanID)
	}
}

func TestReplanPlaybackV2BoundsDeferredLeaseRelease(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	startRequest := v3HandlerStartRequest()
	started := startPlayableDecisionV2(t, handler, marshalV3StartRequest(t, startRequest))

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
	rr := replanPlaybackRecorderV2(t, handler, started.SessionID, body)

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

func TestReplanPlaybackV2RollsBackLiveSessionWhenPersistenceFails(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	file.AudioTracks = append(file.AudioTracks, models.AudioTrack{Codec: "aac", Channels: 2, Layout: "stereo", Language: "spa"})
	manager := playback.NewSessionManager(0, 0)
	handler := newKeyedPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "true"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	startRequest := v3HandlerStartRequest()
	started := startPlayableDecisionV2(t, handler, marshalV3StartRequest(t, startRequest))
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
	rr := replanPlaybackRecorderV2(t, handler, started.SessionID, body)
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

func TestReplanPlaybackV2SeekReanchorKeepsCurrentRecipeEligible(t *testing.T) { //nolint:funlen,gocyclo // ported unchanged from the retired v1 handler test
	file := v3HandlerFixtureFile(t)
	file.SubtitleTracks = []models.SubtitleTrack{{Index: 0, Codec: "ass", Language: "eng"}}
	manager := playback.NewSessionManager(0, 0)
	handler := newKeyedPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "false"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	startRequest := v3HandlerStartRequest()
	subtitleIndex := 0
	startRequest.SubtitleTrackID = playback.TrackIDV3(file.ID, "subtitle", subtitleIndex)
	startRequest.SubtitleTrackIndex = &subtitleIndex
	started := startPlaybackDecisionV2(t, handler, marshalV3StartRequest(t, startRequest))
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
		rr := replanPlaybackRecorderV2(t, handler, started.SessionID, body)
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
	restartReplayRR := replanPlaybackRecorderV2(t, handler, started.SessionID, body)
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
	mismatchRR := replanPlaybackRequestRecorderV2(t, handler, started.SessionID, mismatch)
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
	newerRR := replanPlaybackRequestRecorderV2(t, handler, started.SessionID, newer)
	if newerRR.Code != http.StatusOK {
		t.Fatalf("newer reanchor status = %d, body = %s", newerRR.Code, newerRR.Body.String())
	}

	staleRetryRR := replanPlaybackRecorderV2(t, handler, started.SessionID, body)
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
	mixedWriterRetryRR := replanPlaybackRecorderV2(t, handler, started.SessionID, body)
	if mixedWriterRetryRR.Code != http.StatusConflict || !strings.Contains(mixedWriterRetryRR.Body.String(), "stale_playback_plan") {
		t.Fatalf("mixed-writer stale replay status = %d, body = %s", mixedWriterRetryRR.Code, mixedWriterRetryRR.Body.String())
	}

	beyondEnd := newer
	beyondEnd.ReplanRequestID = "seek-reanchor-beyond-end-0001"
	beyondEnd.PositionSeconds = float64(file.Duration) + 1
	beyondRR := replanPlaybackRequestRecorderV2(t, handler, started.SessionID, beyondEnd)
	if beyondRR.Code != http.StatusOK || !strings.Contains(beyondRR.Body.String(), "invalid_seek_position") {
		t.Fatalf("beyond-end reanchor status = %d, body = %s", beyondRR.Code, beyondRR.Body.String())
	}

	missingSince := time.Now()
	file.MissingSince = &missingSince
	missing := newer
	missing.ReplanRequestID = "seek-reanchor-missing-source-0001"
	missing.PositionSeconds = 600
	missingRR := replanPlaybackRequestRecorderV2(t, handler, started.SessionID, missing)
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

func TestStartPlaybackV2FreezesDownloadedSubtitleFromPlanningSnapshot(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	repo := newMockSubtitleRepoForHandler()
	first := subtitles.DownloadedSubtitle{ID: 71, MediaFileID: file.ID, Format: subtitles.FormatSRT}
	reordered := subtitles.DownloadedSubtitle{ID: 72, MediaFileID: file.ID, Format: subtitles.FormatSRT}
	repo.listResults = [][]subtitles.DownloadedSubtitle{{first}, {reordered, first}}
	repo.subtitles[first.ID] = &first
	repo.subtitles[reordered.ID] = &reordered

	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.SubtitleRepo = repo
	request := v3HandlerStartRequest()
	downloadedIndex := 0
	request.SubtitleTrackIndex = &downloadedIndex
	request.SubtitleTrackID = playback.TrackIDV3(file.ID, "subtitle", downloadedIndex)

	response := startPlaybackDecisionV2(t, handler, marshalV3StartRequest(t, request))
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

func TestReplanPlaybackV2SeekReanchorPreservesFallbackRecipe(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	manager := playback.NewSessionManager(0, 0)
	handler := newKeyedPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	stubCopySeekAnchorV3(handler)
	handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpeg(t), t.TempDir())
	presetLocalRegistryV3(handler, playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{
		{Name: "audio_to_aac", RecipeVersion: "2", Available: true},
		{Name: "video_to_h264", RecipeVersion: "2", Available: true},
		{Name: "server_dv7_to_hdr10", RecipeVersion: playback.TransformationServerDV7HDR10RecipeVersionV3, Available: true},
	}))
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	startRequest := v3HandlerStartRequest()
	startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassProgressiveV3] = playback.DeliveryCapabilityV3{Enabled: true, SupportedOnDevice: true}
	startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3] = playback.DeliveryCapabilityV3{Enabled: true, SupportedOnDevice: true}

	active := startPlaybackDecisionV2(t, handler, marshalV3StartRequest(t, startRequest))
	if active.PlaybackPlan == nil || active.PlaybackPlan.Delivery != playback.DeliveryOriginalHTTPV3 {
		t.Fatalf("initial plan = %#v", active.PlaybackPlan)
	}

	attempted := []string{}
	wantFallbacks := []playback.DeliveryV3{playback.DeliveryRemuxProgressiveV3, playback.DeliveryRemuxHLSV3}
	for index, classification := range []string{"playback_error", "decoder_error"} {
		currentKey := playback.PlanAttemptKeyV3(*active.PlaybackPlan, startRequest.ClientPlaybackContext.Output.OutputContextID, nil)
		attempted = append(attempted, currentKey)
		response := replanPlaybackDecisionV2(t, handler, active.SessionID, playback.ReplanRequestV3{
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
	reanchored := replanPlaybackDecisionV2(t, handler, active.SessionID, playback.ReplanRequestV3{
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

func TestReplanPlaybackV2SeekFailureRecoveryNeverChangesMediaVersion(t *testing.T) { //nolint:funlen,gocyclo // ported unchanged from the retired v1 handler test
	// A seek is not a capability-authority boundary. Even if the replan body
	// carries narrower request-only evidence, recovery stays on the mounted
	// edition and plans from the durable start evidence.

	source := uhdSourceFileV3(t)

	alternate := lowerAlternateFileV3(source)

	manager := playback.NewSessionManager(0, 0)
	files := map[int]*models.MediaFile{
		source.ID: source, alternate.ID: alternate,
	}
	handler := newKeyedPlaybackHandler(manager, mapPlaybackFileResolver{files: files})
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

	started := startPlaybackDecisionV2(t, handler, marshalV3StartRequest(t, startRequest))
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
	replanRR := replanPlaybackRecorderV2(t, handler, started.SessionID, body)
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
		missingRR := replanPlaybackRequestRecorderV2(t, handler, started.SessionID, missing)
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

func TestReplanPlaybackV2SeekUsesEffectiveEditionWhenRequestedEditionIsGone(t *testing.T) {
	effective := v3HandlerFixtureFile(t)
	manager := playback.NewSessionManager(0, 0)
	handler := newKeyedPlaybackHandler(manager, mapPlaybackFileResolver{files: map[int]*models.MediaFile{effective.ID: effective}})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}

	startRequest := v3HandlerStartRequest()
	started := startPlayableDecisionV2(t, handler, marshalV3StartRequest(t, startRequest))

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
	reanchorRR := replanPlaybackRecorderV2(t, handler, started.SessionID, body)
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
	ordinaryRR := replanPlaybackRequestRecorderV2(t, handler, started.SessionID, ordinary)
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

// A start whose hls delivery lists hls_copy_mpegts_v1 runs its copy remux as
// MPEG-TS, and a seek reanchor keeps that packaging.
func TestStartPlaybackV2HLSCopyMPEGTSSurvivesSeekReanchor(t *testing.T) {
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: v3HandlerFixtureFile(t)})
	stubCopySeekAnchorV3(handler)
	handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpeg(t), t.TempDir())
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	request := v3HandlerStartRequest()
	request.ClientPlaybackContext.Deliveries = map[string]playback.DeliveryCapabilityV3{
		playback.DeliveryClassHLSV3: {Enabled: true, SupportedOnDevice: true, Features: []string{playback.FeatureHLSCopyMPEGTSV3}},
	}

	v2, err := handler.startPlaybackV2ForTest(t, request)
	if err != nil {
		t.Fatal(err)
	}
	if v2.PlaybackPlan == nil || v2.PlaybackPlan.Delivery != playback.DeliveryRemuxHLSV3 || v2.PlaybackPlan.EffectiveRecipe.SegmentContainer != playback.SegmentContainerMPEGTSV3 {
		t.Fatalf("v2 plan = %#v, terminal = %#v", v2.PlaybackPlan, v2.Terminal)
	}
	if ts := handler.tm.GetTranscodeSession(v2.SessionID); ts == nil || !ts.Opts().CopyVideoMPEGTS {
		t.Fatal("the v2 MPEG-TS plan started an fMP4 transport")
	}
	frozen := *v2.PlaybackPlan
	reanchored, err := handler.ReplanPlaybackV2(newAuthorizedPlaybackContext(), PlaybackCaller{UserID: 1, ProfileID: "profile-1", InstallationID: serviceInstallation}, v2.SessionID, PlaybackReplanCommand{Digest: "mpegts-seek-digest", Request: playback.ReplanRequestV3{
		ProtocolVersion: playback.ProtocolV3, Operation: playback.ReplanOperationSeekReanchorV3,
		PlaybackAttemptID: request.PlaybackAttemptID,
		ReplanRequestID:   "mpegts-seek-reanchor-0001", FailedPlanID: frozen.PlanID,
		PlanAttemptID: "mpegts-seek-plan-0001", PlanAttemptKey: frozen.PlanAttemptKey,
		AttemptCount: 1, QualityPreference: request.QualityPreference, PositionSeconds: 600,
		SelectedTracks:        frozen.SelectedTracks,
		Capabilities:          request.Capabilities,
		ClientPlaybackContext: request.ClientPlaybackContext,
	}})
	if err != nil || reanchored.PlaybackPlan == nil || reanchored.PlaybackPlan.PlanID != frozen.PlanID {
		t.Fatalf("reanchored = %#v, terminal = %#v, err = %v", reanchored.PlaybackPlan, reanchored.Terminal, err)
	}
	if ts := handler.tm.GetTranscodeSession(v2.SessionID); ts == nil || !ts.Opts().CopyVideoMPEGTS || ts.Opts().SeekSeconds == 0 {
		t.Fatal("the seek reanchor did not reopen the MPEG-TS transport")
	}

}

func TestStartPlaybackV2SafariDolbyVisionRemuxServesHLSManifest(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	file.FilePath = writePlaybackTestMediaFile(t, "movie-dv8.mkv")
	file.Container = "mkv"
	file.CodecVideo = "hevc"
	file.CodecAudio = "eac3"
	file.Resolution = "2160p"
	file.Bitrate = 25_108
	file.AudioChannels = 6
	file.VideoTracks[0] = models.VideoTrack{
		Codec: "hevc", Profile: "main 10", Level: 153, Width: 3840, Height: 2160,
		FrameRate: "24000/1001", Bitrate: 25_108, BitDepth: 10, PixelFormat: "yuv420p10le",
		VideoRange: "DolbyVision", VideoRangeType: "DOVIWithHDR10", DVProfile: 8, DVLevel: 6, DVBLCompatID: 1,
		DVConfigPresent: true, DVBLCompatIDPresent: true, DVBLPresent: true, DVRPUPresent: true,
	}
	file.AudioTracks[0] = models.AudioTrack{Codec: "eac3", Channels: 6, Layout: "5.1", Default: true}

	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
	handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpeg(t), t.TempDir())
	presetLocalRegistryV3(handler, playback.NewTransformationRegistryV3(nil))
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"transcode_enabled": "true", "allow_4k_transcode": "true"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	stubCopySeekAnchorV3(handler)

	start := v3HandlerStartRequest()
	start.QualityPreference = "auto"
	start.ClientFeatures = append(start.ClientFeatures, playback.FeatureHeaderAuthenticatedMediaV3)
	start.Capabilities.Containers = []string{"mp4"}
	start.Capabilities.CodecsVideo = []string{"hevc"}
	start.Capabilities.CodecsVideoHardware = []string{"hevc"}
	start.Capabilities.CodecsAudio = []string{"eac3"}
	start.Capabilities.MaxResolution = "2160p"
	start.Capabilities.VideoDecode = []playback.VideoDecodeCapabilityV3{{
		Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10},
		MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true,
	}}
	hdr := &playback.HDRCapabilitiesV3{
		DolbyVisionProfiles: []int{8},
		DolbyVisionProfileLevels: []playback.DolbyVisionProfileCapabilityV3{{
			Profile: 8, MaxLevel: 6, BLCompatibilityIDs: []int{1},
		}},
	}
	start.Capabilities.HDRDetails = hdr
	start.ClientPlaybackContext.Device.Platform = "safari"
	start.ClientPlaybackContext.Output.HDRDetails = hdr
	start.ClientPlaybackContext.Deliveries[playback.DeliveryClassProgressiveV3] = playback.DeliveryCapabilityV3{
		Enabled: true, SupportedOnDevice: true, Containers: []string{"mp4"},
		VideoCodecs: []string{"hevc"}, AudioDecodeCodecs: []string{"eac3"}, HDRDetails: &playback.HDRCapabilitiesV3{},
	}
	start.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3] = playback.DeliveryCapabilityV3{
		Enabled: true, SupportedOnDevice: true, Containers: []string{"hls"},
		VideoCodecs: []string{"hevc"}, AudioDecodeCodecs: []string{"eac3"}, HDRDetails: hdr,
	}

	response := startPlaybackDecisionV2(t, handler, marshalV3StartRequest(t, start))
	if response.Terminal != nil || response.PlaybackPlan == nil || response.PlaybackPlan.Delivery != playback.DeliveryRemuxHLSV3 || response.PlaybackPlan.Stream.URL == "" {
		t.Fatalf("start response = %#v, want playable HLS remux", response)
	}
	defer handler.tm.CloseTranscodeSession(response.SessionID, "")

	manifestRequest := httptest.NewRequest(http.MethodGet, "/api/v2"+response.PlaybackPlan.Stream.URL, nil).WithContext(newAuthorizedPlaybackContext())
	manifestRequest = withPlaybackRouteParam(manifestRequest, "session_id", response.SessionID)
	manifestRecorder := httptest.NewRecorder()
	handler.HandleGetTranscodeManifest(manifestRecorder, manifestRequest)
	if manifestRecorder.Code != http.StatusOK || !strings.Contains(manifestRecorder.Body.String(), "#EXTM3U") {
		t.Fatalf("manifest status=%d body=%s", manifestRecorder.Code, manifestRecorder.Body.String())
	}
}

func TestReportRouteEventV2AuthorizesPersistedTerminalStart(t *testing.T) {
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0))
	handler.InstallationID = serviceInstallation
	store := &recordingRouteEventPlanStoreV3{
		PlanStoreV3: handler.PlanStoreV3,
		events:      make(chan playback.RouteEventRecordV3, 1),
	}
	handler.PlanStoreV3 = store
	caller := PlaybackCaller{UserID: 1, ProfileID: "profile-1", InstallationID: serviceInstallation}
	report := func(event playback.RouteEventV3) error {
		return handler.ReportRouteEventV2(newAuthorizedPlaybackContext(), caller, PlaybackRouteEventCommand{EventID: uuid.NewString(), Event: event})
	}

	if err := report(playback.RouteEventV3{}); err == nil {
		t.Fatal("a malformed route event was accepted")
	} else {
		assertPlaybackOperationError(t, err, http.StatusBadRequest, "bad_request")
	}

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
	if err := report(event); err != nil {
		t.Fatalf("terminal route event: %v", err)
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
	assertPlaybackOperationError(t, report(event), http.StatusForbidden, "forbidden")

	// Unknown attempts cannot be attributed to the authenticated profile, even
	// when they claim to describe a terminal start.
	event.Event = playback.RouteEventTerminalV3
	event.PlaybackAttemptID = "attempt-terminal-unknown"
	assertPlaybackOperationError(t, report(event), http.StatusForbidden, "forbidden")
}

// A file ffprobe rejected is refused with a non-retryable reason that names the
// file, and a readable version of the same item plays in its place.
func TestStartPlaybackV2RefusesUnreadableFileAndFallsBackToReadableVersion(t *testing.T) {
	failedAt := time.Now().UTC()
	broken := &models.MediaFile{ID: 42, ContentID: "movie-1", FilePath: writePlaybackTestMediaFile(t, "broken.mkv"), ProbeFailedAt: &failedAt}
	start := func(t *testing.T, files ...*models.MediaFile) playback.DecisionResponseV3 {
		t.Helper()
		byID := make(map[int]*models.MediaFile, len(files))
		for _, file := range files {
			byID[file.ID] = file
		}
		handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0), mapPlaybackFileResolver{files: byID})
		handler.FileVersionFetcher = testPlaybackFileVersionFetcher{byContent: map[string][]*models.MediaFile{"movie-1": files}}
		handler.ItemAccess = allowAllPlaybackItemAccess{}
		request := v3HandlerStartRequest()
		request.QualityPreference = "auto"
		response := startPlaybackDecisionV2(t, handler, marshalV3StartRequest(t, request))
		return response
	}

	response := start(t, broken)
	if response.Terminal == nil || response.Terminal.Reason != playback.TerminalSourceUnreadableV3 || response.Terminal.Retryable {
		t.Fatalf("terminal = %#v, plan = %#v", response.Terminal, response.PlaybackPlan)
	}

	readable := v3HandlerFixtureFile(t)
	readable.ID = 84
	response = start(t, broken, readable)
	if response.Terminal != nil || response.PlaybackPlan == nil || response.PlaybackPlan.EffectiveMediaFileID != readable.ID {
		t.Fatalf("fallback terminal = %#v, plan = %#v", response.Terminal, response.PlaybackPlan)
	}
}

// The alternate-version fallback only considers versions the viewer may play.
// The sibling lookup is unfiltered, so a version in a library the viewer
// cannot open, or above their playback-quality ceiling, must never stand in
// for the requested one.
func TestStartPlaybackV2AlternateFallbackHonorsViewerAccess(t *testing.T) {
	failedAt := time.Now().UTC()
	broken := &models.MediaFile{ID: 42, ContentID: "movie-1", MediaFolderID: 1, FilePath: writePlaybackTestMediaFile(t, "broken.mkv"), ProbeFailedAt: &failedAt}
	restricted := v3HandlerFixtureFile(t)
	restricted.ID, restricted.MediaFolderID = 84, 2
	// The client could direct-play this 4K version; only the viewer's
	// playback-quality ceiling rules it out.
	overCeiling := v3HandlerFixtureFile(t)
	overCeiling.ID, overCeiling.MediaFolderID, overCeiling.Resolution = 85, 1, "2160p"
	overCeiling.VideoTracks = append([]models.VideoTrack(nil), overCeiling.VideoTracks...)
	overCeiling.VideoTracks[0].Width, overCeiling.VideoTracks[0].Height, overCeiling.VideoTracks[0].Level = 3840, 2160, 51
	allowed := v3HandlerFixtureFile(t)
	allowed.ID, allowed.MediaFolderID = 86, 1
	scope := access.Scope{UserID: 1, ProfileID: "profile-1", AllowedLibraryIDs: []int{1}, LibrariesRestricted: true, MaxPlaybackQuality: access.PlaybackQualityStandard}
	var probeEnsurer PlaybackProbeEnsurer

	start := func(t *testing.T, files ...*models.MediaFile) playback.DecisionResponseV3 {
		t.Helper()
		byID := make(map[int]*models.MediaFile, len(files))
		for _, file := range files {
			byID[file.ID] = file
		}
		handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0), mapPlaybackFileResolver{files: byID})
		handler.FileVersionFetcher = testPlaybackFileVersionFetcher{byContent: map[string][]*models.MediaFile{"movie-1": files}}
		handler.ItemAccess = allowAllPlaybackItemAccess{}
		handler.ProbeEnsurer = probeEnsurer
		request := v3HandlerStartRequest()
		request.QualityPreference = "auto"
		request.Capabilities.MaxResolution = "2160p"
		request.Capabilities.VideoDecode[0].Levels = []int{51}
		request.Capabilities.VideoDecode[0].MaxWidth = 3840
		request.Capabilities.VideoDecode[0].MaxHeight = 2160
		ctx := access.SetScope(newAuthorizedPlaybackContext(), scope)
		rr := httptest.NewRecorder()
		serveStartPlaybackV2(t, handler, rr, httptest.NewRequest(http.MethodPost, "/api/v2/playback/start", strings.NewReader(marshalV3StartRequest(t, request))).WithContext(ctx))
		if rr.Code != http.StatusCreated {
			t.Fatalf("start status = %d, body = %s", rr.Code, rr.Body.String())
		}
		var response playback.DecisionResponseV3
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}

	for name, sibling := range map[string]*models.MediaFile{"restricted library": restricted, "over quality ceiling": overCeiling} {
		response := start(t, broken, sibling)
		if response.Terminal == nil || response.Terminal.Reason != playback.TerminalSourceUnreadableV3 {
			t.Fatalf("%s sibling: terminal = %#v, plan = %#v", name, response.Terminal, response.PlaybackPlan)
		}
	}

	response := start(t, broken, restricted, overCeiling, allowed)
	if response.Terminal != nil || response.PlaybackPlan == nil || response.PlaybackPlan.EffectiveMediaFileID != allowed.ID {
		t.Fatalf("allowed sibling: terminal = %#v, plan = %#v", response.Terminal, response.PlaybackPlan)
	}

	// A sibling that was never probed has no stored resolution, so it passes
	// the ceiling until the probe repair run on it as a candidate reports 4K.
	unprobedValue := *overCeiling
	unprobed := &unprobedValue
	unprobed.Resolution = ""
	probeEnsurer = repairingProbeEnsurer{repaired: map[int]*models.MediaFile{unprobed.ID: overCeiling}}
	response = start(t, broken, unprobed)
	if response.Terminal == nil || response.Terminal.Reason != playback.TerminalSourceUnreadableV3 {
		t.Fatalf("sibling probed above the ceiling: terminal = %#v, plan = %#v", response.Terminal, response.PlaybackPlan)
	}
}

// Deleting a stored subtitle moves the later ones down an ordinal, and an
// added one takes the freed place. A replan that echoes the viewer's subtitle
// at the ordinal the playing plan gave it keeps that stored subtitle rather
// than the one now at that ordinal, and turns subtitles off when the playing
// stored subtitle itself was deleted.
func TestReplanPlaybackV2KeepsDownloadedSubtitleWhenEarlierOneIsDeleted(t *testing.T) {
	for _, tc := range []struct {
		name      string
		operation playback.ReplanOperationV3
		deleted   int
		wantID    int // stored subtitle the refreshed plan selects; 0 means none
	}{
		{name: "track change, earlier deleted", operation: playback.ReplanOperationTrackChangeV3, deleted: 71, wantID: 72},
		{name: "seek failure recovery, earlier deleted", operation: playback.ReplanOperationSeekFailureRecoveryV3, deleted: 71, wantID: 72},
		{name: "track change, playing deleted", operation: playback.ReplanOperationTrackChangeV3, deleted: 72},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var repo *handlerMockSubtitleRepo
			f := newVersionChangeFixtureV3(t, func(f *versionChangeFixtureV3) {
				repo = newMockSubtitleRepoForHandler()
				repo.list = []subtitles.DownloadedSubtitle{
					{ID: 71, MediaFileID: f.source.ID, Language: "en", Format: subtitles.FormatSRT, ReleaseName: "x"},
					{ID: 72, MediaFileID: f.source.ID, Language: "es", Format: subtitles.FormatSRT, ReleaseName: "y"},
				}
				for i := range repo.list {
					repo.subtitles[repo.list[i].ID] = &repo.list[i]
				}
				f.handler.SubtitleRepo = perFileSubtitleRepoV3{repo}
				if tc.operation == playback.ReplanOperationSeekFailureRecoveryV3 {
					// Recovery leaves the failed route, so its remux must start.
					f.handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpegSleep(t, "5"), t.TempDir())
				}
				spanish := len(f.source.ExternalSubtitles) + len(f.source.SubtitleTracks) + 1
				f.startRequest.SubtitleTrackIndex = &spanish
				f.startRequest.SubtitleTrackID = playback.TrackIDV3(f.source.ID, "subtitle", spanish)
			})
			base := len(f.source.ExternalSubtitles) + len(f.source.SubtitleTracks)
			if got, want := selectedTrackIDV3(f.started.PlaybackPlan.SelectedTracks.Subtitle), playback.TrackIDV3(f.source.ID, "subtitle", base+1); got != want {
				t.Fatalf("start selected subtitle %q, want %q", got, want)
			}

			// One stored subtitle is deleted and 73 is added: the survivor
			// moves to base, and 73 takes base+1.
			french := subtitles.DownloadedSubtitle{ID: 73, MediaFileID: f.source.ID, Language: "fr", Format: subtitles.FormatSRT, ReleaseName: "z"}
			survivor := repo.list[0]
			if tc.deleted == survivor.ID {
				survivor = repo.list[1]
			}
			repo.list = []subtitles.DownloadedSubtitle{survivor, french}
			delete(repo.subtitles, tc.deleted)
			repo.subtitles[survivor.ID] = &repo.list[0]
			repo.subtitles[73] = &repo.list[1]

			refreshed := f.replan(t, f.started, "deleted-subtitle-0001", tc.operation, "", f.started.PlaybackPlan.SelectedTracks)
			plan := refreshed.PlaybackPlan
			if tc.wantID == 0 {
				if plan.SelectedTracks.Subtitle != nil {
					t.Fatalf("refreshed plan selected subtitle %+v, want none", plan.SelectedTracks.Subtitle)
				}
				if !slices.ContainsFunc(plan.DegradationWarnings, func(warning playback.DegradationWarningV3) bool {
					return warning.Code == playback.DegradationWarningSubtitleTrackUnavailableV3
				}) {
					t.Fatalf("dropped subtitle not reported: %#v", plan.DegradationWarnings)
				}
				return
			}
			if got, want := selectedTrackIDV3(plan.SelectedTracks.Subtitle), playback.TrackIDV3(f.source.ID, "subtitle", base); got != want {
				t.Fatalf("refreshed plan selected subtitle %q, want %q (stored subtitle %d)", got, want, tc.wantID)
			}
			if item, ok := playback.SubtitleInventoryItemAtV3(plan.Subtitle.Inventory, base); !ok || item.StoredSubtitleID != tc.wantID {
				t.Fatalf("inventory at the selected ordinal = %+v, want stored subtitle %d", item, tc.wantID)
			}
		})
	}
}

func TestReplanPlaybackV2SidecarChangeReusesCopyHLSTransport(t *testing.T) { //nolint:funlen,gocyclo // ported unchanged from the retired v1 handler test
	file := v3HandlerFixtureFile(t)
	file.Container = "mkv"
	file.FilePath = writePlaybackTestMediaFile(t, "movie.mkv")
	file.ExternalSubtitles = []models.ExternalSubtitle{
		{Path: writePlaybackTestMediaFile(t, "movie.de.srt"), Language: "de", Format: "srt"},
		{Path: writePlaybackTestMediaFile(t, "movie.en.srt"), Language: "en", Format: "srt"},
	}
	manager := playback.NewSessionManager(0, 0)
	handler := newKeyedPlaybackHandler(manager, testPlaybackFileResolver{file: file})
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
	started := startPlayableDecisionV2(t, handler, marshalV3StartRequest(t, startRequest))
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
	replanned := replanPlaybackDecisionV2(t, handler, started.SessionID, playback.ReplanRequestV3{
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

func TestReplanPlaybackV2FailureRecoveryPreservesOmittedQuality(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	manager := playback.NewSessionManager(0, 0)
	handler := newKeyedPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"transcode_enabled": "true", "allow_4k_transcode": "true"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpeg(t), t.TempDir())
	handler.v3Registry = playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{
		{Name: playback.TransformationAudioToAACV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3, Available: true},
		{Name: playback.TransformationVideoToH264V3, RecipeVersion: "2", Available: true},
	})

	startRequest := v3HandlerStartRequest()
	startRequest.ClientPlaybackContext.Deliveries[playback.DeliveryClassHLSV3] = playback.DeliveryCapabilityV3{Enabled: true, SupportedOnDevice: true}
	started := startPlayableDecisionV2(t, handler, marshalV3StartRequest(t, startRequest))
	t.Cleanup(func() { handler.tm.CloseTranscodeSession(started.SessionID, "") })

	store := handler.PlanStoreV3.(*playback.MemoryPlanStoreV3)
	record, err := store.GetAttempt(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	record.NormalizedRequest.QualityPreference = "720p"
	store.ReplaceAttempt(context.Background(), *record)

	currentKey := playback.PlanAttemptKeyV3(*started.PlaybackPlan, startRequest.ClientPlaybackContext.Output.OutputContextID, nil)
	response := replanPlaybackDecisionV2(t, handler, started.SessionID, playback.ReplanRequestV3{
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

// Resume state is keyed on the item, so a session playing one part of a
// multipart presentation must not write its file-local position as the item's
// resume point. The server derives this from the media rather than trusting the
// client to ask for it.
func TestStartPlaybackV2MultipartSuppressesProgressPersistence(t *testing.T) {
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
			handler := newKeyedPlaybackHandler(manager, testPlaybackFileResolver{file: file})
			handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{}}
			handler.ItemAccess = allowAllPlaybackItemAccess{}
			request := v3HandlerStartRequest()
			request.ProgressPersistence = tc.persistence
			if tc.persistence == playback.ProgressPersistenceClientV3 {
				zero := 0.0
				request.StartPosition = &zero
			}
			response := startPlaybackDecisionV2(t, handler, marshalV3StartRequest(t, request))
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

// loginBoundPlaybackContextV3 is newAuthorizedPlaybackContext for a request
// made with an access token, which carries the login session that starts an
// attempt.
func loginBoundPlaybackContextV3() context.Context {
	ctx := apimw.SetClaims(context.Background(), &auth.Claims{UserID: 1, Role: "user", TokenType: auth.TokenTypeAccess, SessionID: "login-session-1"})
	return apimw.SetProfileID(ctx, "profile-1")
}

// parallel_range_url names an origin that gets each range request with the
// plan's stream reference alone, and the reference authorizes only an attempt
// that a login session started. So a /api/v2 start or replan publishes it only
// for a login-bound attempt that negotiated the feature, while the admin
// setting is set. An attempt with no login session keeps stream.url and its st.
func TestParallelRangeURLV3(t *testing.T) {
	const origin = "https://media.example.test"
	tests := []struct {
		name     string
		features []string
		setting  string
		noLogin  bool
		// The prefix the published URL adds to stream.url; "none" publishes nothing.
		want string
	}{
		{name: "feature negotiated and origin configured", features: []string{playback.FeatureParallelRangeOriginV1}, setting: origin, want: origin + "/api/v2"},
		{name: "feature not negotiated", setting: origin, want: "none"},
		{name: "no origin configured", features: []string{playback.FeatureParallelRangeOriginV1}},
		{name: "no login session", features: []string{playback.FeatureParallelRangeOriginV1}, setting: origin, noLogin: true, want: "none"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			file := v3HandlerFixtureFile(t)
			handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
			handler.JWTSecret = "test-secret"
			handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{config.WebMediaURLSettingKey: tc.setting}}
			handler.ItemAccess = allowAllPlaybackItemAccess{}
			start := v3HandlerStartRequest()
			start.ClientFeatures = append(start.ClientFeatures, tc.features...)

			startCtx := loginBoundPlaybackContextV3()
			if tc.noLogin {
				startCtx = newAuthorizedPlaybackContext()
			}
			rr := httptest.NewRecorder()
			serveStartPlaybackV2(t, handler, rr, httptest.NewRequest(http.MethodPost, "/api/v2/playback/start", strings.NewReader(marshalV3StartRequest(t, start))).WithContext(startCtx))
			var started playback.DecisionResponseV3
			if rr.Code != http.StatusCreated || json.Unmarshal(rr.Body.Bytes(), &started) != nil || started.PlaybackPlan == nil {
				t.Fatalf("start status = %d, body = %s", rr.Code, rr.Body.String())
			}
			plan := started.PlaybackPlan
			if plan.Delivery != playback.DeliveryOriginalHTTPV3 || !strings.Contains(plan.Stream.URL, "st=") {
				t.Fatalf("start plan = %s %q, want original_http with st", plan.Delivery, plan.Stream.URL)
			}
			wantURL := ""
			if tc.want != "none" {
				wantURL = tc.want + plan.Stream.URL
				if !strings.Contains(wantURL, "?st=") {
					t.Fatalf("parallel range URL %q has no st", wantURL)
				}
			}
			if plan.Stream.ParallelRangeURL != wantURL {
				t.Fatalf("start ParallelRangeURL = %q, want %q", plan.Stream.ParallelRangeURL, wantURL)
			}

			currentKey := playback.PlanAttemptKeyV3(*plan, start.ClientPlaybackContext.Output.OutputContextID, nil)
			replan := replanPlaybackRequestRecorderV2(t, handler, started.SessionID, playback.ReplanRequestV3{
				ProtocolVersion: playback.ProtocolV3, Operation: playback.ReplanOperationQualityChangeV3,
				PlaybackAttemptID: start.PlaybackAttemptID, ReplanRequestID: "parallel-range-replan-0001",
				FailedPlanID: plan.PlanID, PlanAttemptID: "parallel-range-attempt-0001",
				PlanAttemptKey: currentKey, AttemptedPlanKeys: []string{currentKey}, AttemptCount: 1,
				QualityPreference:     "original",
				SelectedTracks:        playback.SelectedTracksV3{Audio: plan.SelectedTracks.Audio},
				Capabilities:          start.Capabilities,
				ClientPlaybackContext: start.ClientPlaybackContext,
			})
			var replanned playback.DecisionResponseV3
			if replan.Code != http.StatusOK || json.Unmarshal(replan.Body.Bytes(), &replanned) != nil || replanned.PlaybackPlan == nil {
				t.Fatalf("replan status = %d, body = %s", replan.Code, replan.Body.String())
			}
			replannedPlan := replanned.PlaybackPlan
			if replannedPlan.Delivery != playback.DeliveryOriginalHTTPV3 {
				t.Fatalf("replan delivery = %s, want original_http", replannedPlan.Delivery)
			}
			wantURL = ""
			if tc.want != "none" {
				wantURL = tc.want + replannedPlan.Stream.URL
			}
			if replannedPlan.Stream.ParallelRangeURL != wantURL {
				t.Fatalf("replan ParallelRangeURL = %q, want %q", replannedPlan.Stream.ParallelRangeURL, wantURL)
			}
		})
	}
}

// The other publication checks, unit-level: delivery kind, egress, URL shape,
// the configured origin, the login session and the access path.
func TestParallelRangeURLV3Conditions(t *testing.T) {
	const origin = "https://media.example.test"
	const node = "https://node.example.test/stream/direct/opaque"
	feature := []string{playback.FeatureParallelRangeOriginV1}
	tests := []struct {
		name     string
		features []string
		setting  string
		delivery playback.DeliveryV3
		egress   noderouting.Egress
		url      string
		noLogin  bool
		overlay  bool // the client arrived through a network access provider
		want     string
	}{
		{name: "original over the API with a media origin", features: feature, setting: origin, delivery: playback.DeliveryOriginalHTTPV3, egress: noderouting.EgressAPI, url: "/stream/s1?st=abc", want: origin + "/api/v2/stream/s1?st=abc"},
		{name: "keeps st and seek", features: feature, setting: origin, delivery: playback.DeliveryOriginalHTTPV3, egress: noderouting.EgressAPI, url: "/stream/s1?seek=12.5&st=abc", want: origin + "/api/v2/stream/s1?seek=12.5&st=abc"},
		{name: "original over the API with no media origin", features: feature, delivery: playback.DeliveryOriginalHTTPV3, egress: noderouting.EgressAPI, url: "/stream/s1?st=abc", want: "/stream/s1?st=abc"},
		{name: "media origin, client on a network access provider", features: feature, setting: origin, delivery: playback.DeliveryOriginalHTTPV3, egress: noderouting.EgressAPI, url: "/stream/s1?st=abc", overlay: true, want: "/stream/s1?st=abc"},
		{name: "API stream with no login session", features: feature, setting: origin, delivery: playback.DeliveryOriginalHTTPV3, egress: noderouting.EgressAPI, url: "/stream/s1?st=abc", noLogin: true},
		{name: "API stream without st", features: feature, setting: origin, delivery: playback.DeliveryOriginalHTTPV3, egress: noderouting.EgressAPI, url: "/stream/s1?seek=3"},
		{name: "without feature", setting: origin, delivery: playback.DeliveryOriginalHTTPV3, egress: noderouting.EgressAPI, url: "/stream/s1?st=abc"},
		{name: "proxy node token route", features: feature, setting: origin, delivery: playback.DeliveryOriginalHTTPV3, egress: noderouting.EgressProxy, url: node, want: node},
		{name: "proxy node token route with no login session", features: feature, delivery: playback.DeliveryOriginalHTTPV3, egress: noderouting.EgressProxy, url: node, noLogin: true, want: node},
		{name: "proxy node header-authorized route", features: feature, delivery: playback.DeliveryOriginalHTTPV3, egress: noderouting.EgressProxy, url: "https://node.example.test/stream/v3/s1"},
		{name: "proxy node without feature", delivery: playback.DeliveryOriginalHTTPV3, egress: noderouting.EgressProxy, url: node},
		{name: "remux over the API", features: feature, setting: origin, delivery: playback.DeliveryRemuxProgressiveV3, egress: noderouting.EgressAPI, url: "/stream/s1?st=abc"},
		{name: "remux through a proxy node", features: feature, delivery: playback.DeliveryRemuxProgressiveV3, egress: noderouting.EgressProxy, url: "https://node.example.test/stream/remux/opaque"},
		{name: "header authenticated media", features: []string{playback.FeatureParallelRangeOriginV1, playback.FeatureHeaderAuthenticatedMediaV3}, setting: origin, delivery: playback.DeliveryOriginalHTTPV3, egress: noderouting.EgressAPI, url: "/stream/s1"},
		{name: "header authenticated media through a proxy node", features: []string{playback.FeatureParallelRangeOriginV1, playback.FeatureHeaderAuthenticatedMediaV3}, delivery: playback.DeliveryOriginalHTTPV3, egress: noderouting.EgressProxy, url: node},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := &PlaybackHandler{SettingsRepo: &mutablePlaybackSettingsV3{values: map[string]string{config.WebMediaURLSettingKey: tc.setting}}}
			plan := &playback.PlanV3{Delivery: tc.delivery}
			ctx := context.Background()
			if tc.overlay {
				ctx = netaccess.WithPath(ctx, netaccess.Path{Provider: "overlay"})
			}
			h.setPlanStreamURLsV3(ctx, plan, preparedTransportV3{url: tc.url, routingEgress: tc.egress}, tc.features, !tc.noLogin)
			if plan.Stream.URL != tc.url {
				t.Fatalf("Stream.URL = %q, want the transport URL %q", plan.Stream.URL, tc.url)
			}
			if plan.Stream.ParallelRangeURL != tc.want {
				t.Fatalf("ParallelRangeURL = %q, want %q", plan.Stream.ParallelRangeURL, tc.want)
			}
		})
	}
}

// fakeLoginSessionsV3 answers ActiveSession for the login sessions it lists.
type fakeLoginSessionsV3 map[string]bool

func (s fakeLoginSessionsV3) ActiveSession(_ context.Context, sessionID string, _ int) (auth.SessionAuthority, bool, error) {
	return auth.SessionAuthority{Role: models.RoleUser, Purpose: models.SessionPurposeAccount}, s[sessionID], nil
}

// streamRangeFixtureV3 runs one range GET through the same two pieces /api/v2
// puts in front of a file: RequireStreamReferenceAuth, wired to the attempt
// store the way the router wires it, then the stream handler.
type streamRangeFixtureV3 struct {
	jwt    *auth.JWTService
	router http.Handler
}

func newStreamRangeFixtureV3(t *testing.T, stream *StreamHandler, attempts playback.PlanStoreV3) streamRangeFixtureV3 {
	t.Helper()
	jwt := auth.NewJWTService("test-secret", 15*time.Minute, 7*24*time.Hour)
	am := apimw.NewAuthMiddleware(jwt, fakeLoginSessionsV3{"login-session-1": true}, nil, nil)
	am.SetStreamReferenceVerifier(func(reference string) (*streamtoken.Claims, error) {
		return streamtoken.Verify(reference, "test-secret")
	}, func(ctx context.Context, sessionID string) (apimw.StreamReferenceAttempt, error) {
		identity, err := attempts.GetAttemptIdentity(ctx, sessionID)
		if errors.Is(err, playback.ErrSessionNotFound) {
			return apimw.StreamReferenceAttempt{}, nil
		}
		if err != nil {
			return apimw.StreamReferenceAttempt{}, err
		}
		return apimw.StreamReferenceAttempt{UserID: identity.UserID, ProfileID: identity.ProfileID, LoginSessionID: identity.LoginSessionID, Live: !identity.Stopped}, nil
	})
	router := chi.NewRouter()
	router.With(am.RequireStreamReferenceAuth).Get("/api/v2/stream/{session_id}", stream.HandleStream)
	return streamRangeFixtureV3{jwt: jwt, router: router}
}

// get requests bytes 0-99 of streamURL, with the account's access token when
// withToken is set.
func (f streamRangeFixtureV3) get(t *testing.T, streamURL string, withToken bool) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/v2"+streamURL, nil)
	request.Header.Set("Range", "bytes=0-99")
	if withToken {
		token, err := f.jwt.GenerateAccessToken(1, models.RoleUser, "login-session-1")
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	f.router.ServeHTTP(rr, request)
	return rr
}

// startDirectPlayV3 starts a direct-play attempt as ctx and returns its
// handler, attempt store and plan.
func startDirectPlayV3(t *testing.T, ctx context.Context) (*PlaybackHandler, playback.PlanStoreV3, *playback.PlanV3) {
	t.Helper()
	file := v3HandlerFixtureFile(t)
	handler := newKeyedPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
	handler.JWTSecret = "test-secret"
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{config.WebMediaURLSettingKey: "https://media.example.test"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	start := v3HandlerStartRequest()
	start.ClientFeatures = append(start.ClientFeatures, playback.FeatureParallelRangeOriginV1)
	rr := httptest.NewRecorder()
	serveStartPlaybackV2(t, handler, rr, httptest.NewRequest(http.MethodPost, "/api/v2/playback/start", strings.NewReader(marshalV3StartRequest(t, start))).WithContext(ctx))
	var started playback.DecisionResponseV3
	if rr.Code != http.StatusCreated || json.Unmarshal(rr.Body.Bytes(), &started) != nil || started.PlaybackPlan == nil ||
		started.PlaybackPlan.Delivery != playback.DeliveryOriginalHTTPV3 {
		t.Fatalf("start status = %d, body = %s", rr.Code, rr.Body.String())
	}
	return handler, handler.PlanStoreV3, started.PlaybackPlan
}

// The stream reference alone authorizes each range request of a login-bound
// attempt: no Authorization header, no account token.
func TestParallelRangeURLV3StreamReferenceAuthorizesLoginBoundRange(t *testing.T) {
	handler, attempts, plan := startDirectPlayV3(t, loginBoundPlaybackContextV3())
	if !strings.HasPrefix(plan.Stream.ParallelRangeURL, "https://media.example.test/api/v2/stream/") {
		t.Fatalf("ParallelRangeURL = %q, want the media origin", plan.Stream.ParallelRangeURL)
	}
	stream := NewStreamHandler(handler.sessionMgr, testPlaybackFileResolver{file: v3HandlerFixtureFile(t)})
	stream.TM = handler.TranscodeManager()
	stream.JWTSecret = "test-secret"
	rr := newStreamRangeFixtureV3(t, stream, attempts).get(t, plan.Stream.URL, false)
	if rr.Code != http.StatusPartialContent {
		t.Fatalf("range with st only = %d (%s), want 206", rr.Code, rr.Body.String())
	}
}

// An attempt with no login session (an API key started it, or it predates the
// record) is read from stream.url: after its playback session is evicted, st
// plus the account token still rebuilds the session and answers the range.
func TestParallelRangeURLV3NoLoginSessionRecoversWithAccountToken(t *testing.T) {
	_, attempts, plan := startDirectPlayV3(t, newAuthorizedPlaybackContext())
	if !strings.Contains(plan.Stream.URL, "?st=") {
		t.Fatalf("stream.url = %q, want st", plan.Stream.URL)
	}
	// A fresh session manager stands in for the eviction: the attempt row
	// survives, the live session does not.
	manager := playback.NewSessionManager(0, 0)
	stream := NewStreamHandler(manager, testPlaybackFileResolver{file: v3HandlerFixtureFile(t)})
	stream.TM = newKeyedPlaybackHandler(manager).TranscodeManager()
	stream.PlanStoreV3 = attempts
	stream.JWTSecret = "test-secret"
	rr := newStreamRangeFixtureV3(t, stream, attempts).get(t, plan.Stream.URL, true)
	if rr.Code != http.StatusPartialContent {
		t.Fatalf("range with st and the account token after eviction = %d (%s), want 206", rr.Code, rr.Body.String())
	}
}

func TestQualityReplanFindsLowerCopyWhileKeepingAliasSubtitle(t *testing.T) {
	f := newVersionChangeFixtureV3(t, func(f *versionChangeFixtureV3) {
		f.source.EditionKey, f.alternate.EditionKey = "extended_cut", "extended"
		f.source.ProbeUpdatedAt, f.alternate.ProbeUpdatedAt = new(time.Now()), new(time.Now())
		f.alternate.ExternalSubtitles = append([]models.ExternalSubtitle(nil), f.source.ExternalSubtitles...)
		f.startRequest.BandwidthEstimateKbps = new(12000)
		f.startRequest.ClientFeatures = append(f.startRequest.ClientFeatures, playback.FeatureBufferingQualityV1)
		f.handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpeg(t), t.TempDir())
		presetLocalRegistryV3(f.handler, playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{
			{Name: playback.TransformationVideoToH264V3, RecipeVersion: playback.TransformationVideoToH264RecipeVersionV3, Available: true},
			{Name: playback.TransformationAudioToAACV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3, Available: true},
		}))
	})
	t.Cleanup(func() { f.handler.tm.CloseTranscodeSession(f.started.SessionID, "") })
	plan := f.started.PlaybackPlan
	if plan.EffectiveMediaFileID != f.alternate.ID || plan.SelectedTracks.Subtitle == nil {
		t.Fatalf("initial plan did not play the mounted alias with its subtitle: %+v", plan)
	}
	// Add a lighter copy with the playing subtitle. The requested file no
	// longer carries that track, so the replan keeps the mounted alias first.
	f.source.ExternalSubtitles = nil
	light := *f.alternate
	light.ID, light.EditionKey, light.Bitrate = 126, "extended_version", 1000
	light.VideoTracks = slices.Clone(light.VideoTracks)
	light.VideoTracks[0].Bitrate = 1000
	fetcher := f.handler.FileVersionFetcher.(testPlaybackFileVersionFetcher)
	fetcher.byContent[f.source.ContentID] = append(fetcher.byContent[f.source.ContentID], &light)
	response, err := f.handler.ReplanPlaybackV2(newAuthorizedPlaybackContext(), PlaybackCaller{
		UserID: 1, ProfileID: "profile-1", InstallationID: f.handler.InstallationID,
	}, f.started.SessionID, PlaybackReplanCommand{Digest: "alias-with-subtitle", Request: playback.ReplanRequestV3{
		ProtocolVersion: playback.ProtocolV3, ClientFeatures: f.startRequest.ClientFeatures,
		Operation: playback.ReplanOperationQualityChangeV3, PlaybackAttemptID: f.startRequest.PlaybackAttemptID,
		ReplanRequestID: "alias-with-subtitle", FailedPlanID: plan.PlanID,
		PlanAttemptID: "alias-with-subtitle-attempt", PlanAttemptKey: plan.PlanAttemptKey,
		AttemptedPlanKeys: []string{plan.PlanAttemptKey}, AttemptCount: 1,
		QualityPreference: "auto", BandwidthEstimateKbps: new(2600), BandwidthEstimateSource: playback.BandwidthEstimateObservedRebufferV3,
		SelectedTracks: plan.SelectedTracks, Capabilities: f.startRequest.Capabilities,
		ClientPlaybackContext: f.startRequest.ClientPlaybackContext,
	}})
	if err != nil || response.PlaybackPlan == nil {
		t.Fatalf("replan: %v, terminal = %#v", err, response.Terminal)
	}
	got := response.PlaybackPlan
	if got.EffectiveMediaFileID != light.ID || got.Delivery != playback.DeliveryOriginalHTTPV3 || got.SelectedTracks.Subtitle == nil {
		t.Fatalf("replan should direct-play the fitting copy and keep subtitles: file=%d delivery=%s subtitle=%v", got.EffectiveMediaFileID, got.Delivery, got.SelectedTracks.Subtitle)
	}
}

// Exercise the route used when neither original fits the stream budget and the
// 1080p alias is useful only as input to an encode.
func TestPlaybackV2EditionAliasFallback(t *testing.T) {
	const bandwidthEstimateKbps = 2600
	for _, mode := range []string{"start", "replan", "replan-conflict", "pinned"} {
		t.Run(mode, func(t *testing.T) {
			f := newVersionChangeFixtureV3(t, func(f *versionChangeFixtureV3) {
				f.source.EditionKey, f.alternate.EditionKey = "extended_cut", "extended"
				f.source.ProbeUpdatedAt, f.alternate.ProbeUpdatedAt = new(time.Now()), new(time.Now())
				f.alternate.FilePath = writePlaybackTestMediaFile(t, "movie-hd.mp4")
				f.startRequest.SubtitleTrackID, f.startRequest.SubtitleTrackIndex = "", nil
				f.startRequest.ClientFeatures = append(f.startRequest.ClientFeatures, playback.FeatureBufferingQualityV1)
				f.handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpeg(t), t.TempDir())
				presetLocalRegistryV3(f.handler, playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{
					{Name: playback.TransformationVideoToH264V3, RecipeVersion: playback.TransformationVideoToH264RecipeVersionV3, Available: true},
					{Name: playback.TransformationAudioToAACV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3, Available: true},
				}))
				if mode == "start" || mode == "pinned" {
					f.startRequest.BandwidthEstimateKbps = new(bandwidthEstimateKbps)
				}
				if mode == "pinned" {
					f.startRequest.AllowAlternateVersions = new(false)
				}
			})
			t.Cleanup(func() { f.handler.tm.CloseTranscodeSession(f.started.SessionID, "") })
			plan := f.started.PlaybackPlan
			switch mode {
			case "pinned":
				if plan.EffectiveMediaFileID != f.source.ID {
					t.Fatalf("pinned file switched to %d", plan.EffectiveMediaFileID)
				}
				return
			case "replan", "replan-conflict":
				if plan.EffectiveMediaFileID != f.source.ID {
					t.Fatalf("initial plan uses file %d, want %d", plan.EffectiveMediaFileID, f.source.ID)
				}
				if mode == "replan-conflict" {
					conflict := *f.alternate
					conflict.ID, conflict.EditionKey, conflict.Duration = 126, "extended_edition", f.source.Duration+120
					fetcher := f.handler.FileVersionFetcher.(testPlaybackFileVersionFetcher)
					fetcher.byContent[f.source.ContentID] = append(fetcher.byContent[f.source.ContentID], &conflict)
				}
				response, err := f.handler.ReplanPlaybackV2(newAuthorizedPlaybackContext(), PlaybackCaller{
					UserID: 1, ProfileID: "profile-1", InstallationID: f.handler.InstallationID,
				}, f.started.SessionID, PlaybackReplanCommand{Digest: "edition-alias-replan", Request: playback.ReplanRequestV3{
					ProtocolVersion: playback.ProtocolV3, ClientFeatures: f.startRequest.ClientFeatures,
					Operation: playback.ReplanOperationQualityChangeV3, PlaybackAttemptID: f.startRequest.PlaybackAttemptID,
					ReplanRequestID: "edition-alias-replan", FailedPlanID: plan.PlanID,
					PlanAttemptID: "edition-alias-replan-attempt", PlanAttemptKey: plan.PlanAttemptKey,
					AttemptedPlanKeys: []string{plan.PlanAttemptKey}, AttemptCount: 1,
					QualityPreference: "auto", BandwidthEstimateKbps: new(bandwidthEstimateKbps), BandwidthEstimateSource: playback.BandwidthEstimateObservedRebufferV3,
					SelectedTracks: plan.SelectedTracks, Capabilities: f.startRequest.Capabilities,
					ClientPlaybackContext: f.startRequest.ClientPlaybackContext,
				}})
				if err != nil || response.PlaybackPlan == nil {
					t.Fatalf("replan: %v, terminal = %#v", err, response.Terminal)
				}
				plan = response.PlaybackPlan
				if mode == "replan-conflict" {
					if plan.EffectiveMediaFileID != f.source.ID {
						t.Fatalf("replan used file %d after the alias group split", plan.EffectiveMediaFileID)
					}
					return
				}
			}
			if plan.RequestedMediaFileID != f.source.ID || plan.EffectiveMediaFileID != f.alternate.ID ||
				plan.Delivery != playback.DeliveryTranscodeHLSV3 || plan.EffectiveRecipe.BitrateKbps == nil ||
				*plan.EffectiveRecipe.BitrateKbps > playback.StreamBudgetKbpsV3(bandwidthEstimateKbps) {
				t.Fatalf("plan did not transcode the alternate under budget: %+v", plan)
			}
			if transport := f.handler.tm.GetTranscodeSession(f.started.SessionID); transport == nil || transport.Opts().InputPath != f.alternate.FilePath {
				t.Fatal("transcode transport did not open the 1080p input")
			}
			f.handler.waitForPlaybackStartSideEffectsV3(t.Context(), f.started.SessionID)
			preference, err := f.store.GetSeriesPlaybackPreference(t.Context(), "profile-1", f.source.ContentID)
			if err != nil || preference == nil || preference.FileID != f.source.ID || preference.EditionKey != f.source.EditionKey {
				t.Fatalf("remembered version = %+v, error = %v", preference, err)
			}
			recovered := f.startRequest
			recovered.PlaybackAttemptID = "edition-alias-recovered"
			recovered.FileID, recovered.BandwidthEstimateKbps = preference.FileID, nil
			response := startPlaybackDecisionV2(t, f.handler, marshalV3StartRequest(t, recovered))
			if response.PlaybackPlan == nil || response.PlaybackPlan.EffectiveMediaFileID != f.source.ID {
				t.Fatalf("fresh start after bandwidth recovery did not use the requested 4K file: %+v", response)
			}
		})
	}
}

// A replan that starts a new HLS transport under the same session id publishes
// a stream URL naming that transport, and the superseded plan's playlist URLs
// stop serving: the old player gets a terminal answer instead of seek-restarting
// the transport its replacement is about to play.
func TestReplanPlaybackV2SupersedesThePreviousTransportsURLs(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	manager := playback.NewSessionManager(0, 0)
	handler := newKeyedPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpeg(t), t.TempDir())
	presetLocalRegistryV3(handler, playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{
		{Name: playback.TransformationAudioToAACV3, RecipeVersion: playback.TransformationAudioToAACRecipeVersionV3, Available: true},
		{Name: playback.TransformationVideoToH264V3, RecipeVersion: playback.TransformationVideoToH264RecipeVersionV3, Available: true},
	}))
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	stubCopySeekAnchorV3(handler)

	start := v3HandlerStartRequest()
	start.ClientFeatures = append(start.ClientFeatures, playback.FeatureHeaderAuthenticatedMediaV3)
	start.ClientPlaybackContext.Deliveries = map[string]playback.DeliveryCapabilityV3{playback.DeliveryClassHLSV3: {
		Enabled: true, SupportedOnDevice: true, Containers: []string{"hls"},
		VideoCodecs: []string{"h264"}, AudioDecodeCodecs: []string{"aac"}, Features: []string{playback.FeatureHLSTransportMarkerV3},
	}}
	started := startPlayableDecisionV2(t, handler, marshalV3StartRequest(t, start))
	t.Cleanup(func() {
		if ts := handler.tm.GetTranscodeSession(started.SessionID); ts != nil {
			_ = ts.Close()
		}
	})
	transportMarker := func(streamURL string) string {
		t.Helper()
		parsed, err := url.Parse(streamURL)
		if err != nil || !strings.HasSuffix(parsed.Path, "/master.m3u8") {
			t.Fatalf("stream URL = %q, want an HLS playlist (parse error %v)", streamURL, err)
		}
		marker := parsed.Query().Get(playback.TransportQueryParam)
		session, err := manager.GetSession(started.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		if marker == "" || marker != playback.TransportMarker(session.ID, session.TransportGeneration) {
			t.Fatalf("stream URL %q marker = %q, session transport generation = %q", streamURL, marker, session.TransportGeneration)
		}
		return marker
	}
	before := transportMarker(started.PlaybackPlan.Stream.URL)

	replanned := replanPlaybackDecisionV2(t, handler, started.SessionID, playback.ReplanRequestV3{
		ProtocolVersion:       playback.ProtocolV3,
		Operation:             playback.ReplanOperationQualityChangeV3,
		QualityPreference:     "720p-medium",
		PlaybackAttemptID:     start.PlaybackAttemptID,
		ReplanRequestID:       "superseded-transport-replan-0001",
		FailedPlanID:          started.PlaybackPlan.PlanID,
		PlanAttemptID:         "superseded-transport-attempt-0001",
		PlanAttemptKey:        started.PlaybackPlan.PlanAttemptKey,
		AttemptCount:          1,
		PositionSeconds:       12,
		SelectedTracks:        started.PlaybackPlan.SelectedTracks,
		Capabilities:          start.Capabilities,
		ClientPlaybackContext: start.ClientPlaybackContext,
	})
	if replanned.PlaybackPlan == nil {
		t.Fatalf("replan terminal = %#v", replanned.Terminal)
	}
	after := transportMarker(replanned.PlaybackPlan.Stream.URL)
	if after == before {
		t.Fatalf("replan kept transport marker %q; the test needs a replan that starts a new transport", after)
	}

	requireSuperseded := func(marker string) {
		t.Helper()
		rr := httptest.NewRecorder()
		req := playbackTestRequest(http.MethodGet, "/api/v2/playback/transcode/"+started.SessionID+"/segment/seg_00072.ts?"+playback.TransportQueryParam+"="+marker,
			nil, map[string]string{"session_id": started.SessionID, "name": "seg_00072.ts"})
		handler.HandleGetTranscodeSegment(rr, req)
		if rr.Code != http.StatusNotFound || !strings.Contains(rr.Body.String(), playback.TransportSupersededCode) {
			t.Fatalf("superseded segment status = %d body = %s, want 404 %s", rr.Code, rr.Body.String(), playback.TransportSupersededCode)
		}
	}
	requireSuperseded(before)

	// Leaving HLS altogether supersedes the last HLS transport too: its player
	// must not revive the transcode from the recipe its URLs carry.
	direct := start.ClientPlaybackContext
	direct.Deliveries = map[string]playback.DeliveryCapabilityV3{playback.DeliveryClassOriginalHTTPV3: {Enabled: true, SupportedOnDevice: true}}
	directPlan := replanPlaybackDecisionV2(t, handler, started.SessionID, playback.ReplanRequestV3{
		ProtocolVersion:       playback.ProtocolV3,
		Operation:             playback.ReplanOperationQualityChangeV3,
		QualityPreference:     "original",
		PlaybackAttemptID:     start.PlaybackAttemptID,
		ReplanRequestID:       "superseded-transport-replan-0002",
		FailedPlanID:          replanned.PlaybackPlan.PlanID,
		PlanAttemptID:         "superseded-transport-attempt-0002",
		PlanAttemptKey:        replanned.PlaybackPlan.PlanAttemptKey,
		AttemptCount:          1,
		PositionSeconds:       30,
		SelectedTracks:        replanned.PlaybackPlan.SelectedTracks,
		Capabilities:          start.Capabilities,
		ClientPlaybackContext: direct,
	})
	if directPlan.PlaybackPlan == nil || directPlan.PlaybackPlan.Delivery != playback.DeliveryOriginalHTTPV3 {
		t.Fatalf("direct replan = %#v", directPlan)
	}
	requireSuperseded(after)
	if handler.tm.GetTranscodeSession(started.SessionID) != nil {
		t.Fatal("a superseded HLS request revived a transcode after the move to direct play")
	}
}

// A replan that reuses the running HLS transport republishes the URL the
// current player holds. When the session lost its recorded generation (a
// session rebuilt after an API restart), the reused transport must still name
// that URL's transport, or the server would refuse the player it just kept.
func TestReusedHLSTransportKeepsTheMarkerOfTheURLItRepublishes(t *testing.T) {
	const sessionID = "6f8a2c1e-0b3d-4e5f-9a7b-1c2d3e4f5a6b"
	streamURL := "/api/v2/playback/transcode/" + sessionID + "/master.m3u8?" + playback.TransportQueryParam + "=planaaaaaaaa-cccc1111"
	for _, recorded := range []string{"", sessionID + "-planaaaaaaaa-cccc1111"} {
		transport := reusedHLSTransportV3(&playback.Session{ID: sessionID, TransportGeneration: recorded}, streamURL)
		parsed, err := url.Parse(transport.url)
		if err != nil {
			t.Fatal(err)
		}
		if transport.generation == "" || playback.TransportSuperseded(sessionID, transport.generation, parsed.Query()) {
			t.Fatalf("recorded generation %q: reused transport generation %q does not match the URL it republishes", recorded, transport.generation)
		}
	}
}
