package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

// downloadedSubtitleSessionV3 starts playback of a file with one downloaded
// English SRT selected.
func downloadedSubtitleSessionV3(t *testing.T) (*PlaybackHandler, *handlerMockSubtitleRepo, *models.MediaFile, playback.StartRequestV3, playback.DecisionResponseV3, int) {
	t.Helper()
	file := v3HandlerFixtureFile(t)
	repo := newMockSubtitleRepoForHandler()
	downloaded := subtitles.DownloadedSubtitle{ID: 71, MediaFileID: file.ID, Format: subtitles.FormatSRT, Language: "eng"}
	repo.list = []subtitles.DownloadedSubtitle{downloaded}
	repo.subtitles[downloaded.ID] = &downloaded

	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.SubtitleRepo = repo
	startRequest := v3HandlerStartRequest()
	downloadedIndex := len(file.ExternalSubtitles) + len(file.SubtitleTracks)
	startRequest.SubtitleTrackIndex = &downloadedIndex
	startRequest.SubtitleTrackID = playback.TrackIDV3(file.ID, "subtitle", downloadedIndex)

	rec := httptest.NewRecorder()
	handler.HandleStartPlayback(rec, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, startRequest))).WithContext(newAuthorizedPlaybackContext()))
	var started playback.DecisionResponseV3
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &started) != nil || started.PlaybackPlan == nil || started.PlaybackPlan.SelectedTracks.Subtitle == nil {
		t.Fatalf("start with the downloaded subtitle: status=%d body=%s", rec.Code, rec.Body.String())
	}
	return handler, repo, file, startRequest, started, downloadedIndex
}

func requireDownloadedSubtitleOutageV3(t *testing.T, response playback.DecisionResponseV3) {
	t.Helper()
	if response.PlaybackPlan != nil || response.Terminal == nil {
		t.Fatalf("plan = %#v terminal = %#v, want a retryable refusal instead of a plan without the subtitle", response.PlaybackPlan, response.Terminal)
	}
	if response.Terminal.Reason != subtitleUnavailableReasonV3 || !response.Terminal.Retryable {
		t.Fatalf("terminal = %#v, want retryable %s", response.Terminal, subtitleUnavailableReasonV3)
	}
}

// A downloaded subtitle that cannot be looked up for a moment is not a
// subtitle the file lacks. A quality change during the outage is refused as
// retryable, so the playing plan keeps the subtitle and the next change after
// the store recovers still shows it. Planning it as subtitles off would keep
// it off for the rest of the session, because replans start from the current
// plan's tracks.
func TestHandleReplanPlaybackV3DownloadedSubtitleLookupOutageKeepsTheSubtitle(t *testing.T) {
	handler, repo, file, startRequest, started, downloadedIndex := downloadedSubtitleSessionV3(t)
	qualityChange := func(id string) playback.DecisionResponseV3 {
		t.Helper()
		current := started.PlaybackPlan
		return postPlaybackReplanV3(t, handler, started.SessionID, playback.ReplanRequestV3{
			ProtocolVersion: playback.ProtocolV3, Operation: playback.ReplanOperationQualityChangeV3,
			PlaybackAttemptID: startRequest.PlaybackAttemptID, ReplanRequestID: "downloaded-outage-" + id,
			FailedPlanID: current.PlanID, PlanAttemptID: "downloaded-outage-attempt-" + id,
			PlanAttemptKey: current.PlanAttemptKey, AttemptCount: 1, PositionSeconds: 30,
			QualityPreference: "auto",
			// A quality change may omit the unchanged subtitle; the server
			// carries it over from the current plan.
			SelectedTracks: playback.SelectedTracksV3{Audio: current.SelectedTracks.Audio},
			Capabilities:   startRequest.Capabilities, ClientPlaybackContext: startRequest.ClientPlaybackContext,
		})
	}

	repo.listErr = errors.New("database unavailable")
	requireDownloadedSubtitleOutageV3(t, qualityChange("0001"))

	repo.listErr = nil
	recovered := qualityChange("0002")
	if recovered.PlaybackPlan == nil || recovered.Terminal != nil {
		t.Fatalf("quality change after the outage = %#v", recovered)
	}
	selected := recovered.PlaybackPlan.SelectedTracks.Subtitle
	if selected == nil || selected.ID != playback.TrackIDV3(file.ID, "subtitle", downloadedIndex) {
		t.Fatalf("subtitle after the outage = %#v, want the downloaded subtitle still selected", selected)
	}
}

// Choosing a downloaded subtitle during the outage is refused as retryable
// too, rather than answered with subtitles off.
func TestHandleReplanPlaybackV3DownloadedSubtitleLookupOutageRefusesTrackChange(t *testing.T) {
	handler, repo, file, startRequest, started, downloadedIndex := downloadedSubtitleSessionV3(t)
	repo.listErr = errors.New("database unavailable")
	current := started.PlaybackPlan
	requireDownloadedSubtitleOutageV3(t, postPlaybackReplanV3(t, handler, started.SessionID, playback.ReplanRequestV3{
		ProtocolVersion: playback.ProtocolV3, Operation: playback.ReplanOperationTrackChangeV3,
		PlaybackAttemptID: startRequest.PlaybackAttemptID, ReplanRequestID: "downloaded-outage-track-0001",
		FailedPlanID: current.PlanID, PlanAttemptID: "downloaded-outage-track-attempt-0001",
		PlanAttemptKey: current.PlanAttemptKey, AttemptCount: 1, PositionSeconds: 30,
		QualityPreference: "auto",
		SelectedTracks: playback.SelectedTracksV3{
			Audio:    current.SelectedTracks.Audio,
			Subtitle: &playback.TrackIdentityV3{ID: playback.TrackIDV3(file.ID, "subtitle", downloadedIndex), Index: &downloadedIndex},
		},
		Capabilities: startRequest.Capabilities, ClientPlaybackContext: startRequest.ClientPlaybackContext,
	}))
}

// A start has no playing plan to fall back on, so it still plays without the
// downloaded subtitle and says so, as before; refusing it would leave the
// viewer with nothing.
func TestHandleStartPlaybackV3DownloadedSubtitleLookupOutageStillStarts(t *testing.T) {
	file := v3HandlerFixtureFile(t)
	repo := newMockSubtitleRepoForHandler()
	repo.listErr = errors.New("database unavailable")
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.SubtitleRepo = repo
	request := v3HandlerStartRequest()
	downloadedIndex := len(file.ExternalSubtitles) + len(file.SubtitleTracks)
	request.SubtitleTrackIndex = &downloadedIndex
	request.SubtitleTrackID = playback.TrackIDV3(file.ID, "subtitle", downloadedIndex)

	rec := httptest.NewRecorder()
	handler.HandleStartPlayback(rec, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, request))).WithContext(newAuthorizedPlaybackContext()))
	var response playback.DecisionResponseV3
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &response) != nil || response.PlaybackPlan == nil {
		t.Fatalf("start status=%d body=%s", rec.Code, rec.Body.String())
	}
	if response.PlaybackPlan.SelectedTracks.Subtitle != nil {
		t.Fatalf("subtitle = %#v, want playback without the unreadable subtitle", response.PlaybackPlan.SelectedTracks.Subtitle)
	}
}

// The same outage during a quality change made on a lower alternate, where
// the server first maps the subtitle back to the requested edition, is
// refused as retryable too rather than as a permanent track_unavailable.
func TestHandleReplanPlaybackV3DownloadedSubtitleLookupOutageOnAnAlternateIsRetryable(t *testing.T) {
	source := v3HandlerFixtureFile(t)
	source.Resolution = "2160p"
	source.Bitrate = 32_000
	source.VideoTracks = append([]models.VideoTrack(nil), source.VideoTracks...)
	source.VideoTracks[0].Level, source.VideoTracks[0].Bitrate = 51, 32_000
	source.VideoTracks[0].Width, source.VideoTracks[0].Height = 3840, 2160
	alternateValue := *source
	alternate := &alternateValue
	alternate.ID, alternate.Resolution, alternate.Bitrate = 84, "1080p", 8_000
	alternate.VideoTracks = append([]models.VideoTrack(nil), source.VideoTracks...)
	alternate.VideoTracks[0].Level, alternate.VideoTracks[0].Bitrate = 41, 8_000
	alternate.VideoTracks[0].Width, alternate.VideoTracks[0].Height = 1920, 1080

	repo := newMockSubtitleRepoForHandler()
	downloaded := subtitles.DownloadedSubtitle{ID: 71, MediaFileID: alternate.ID, Format: subtitles.FormatSRT, Language: "eng"}
	repo.list = []subtitles.DownloadedSubtitle{downloaded}
	repo.subtitles[downloaded.ID] = &downloaded
	files := map[int]*models.MediaFile{source.ID: source, alternate.ID: alternate}
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), mapPlaybackFileResolver{files: files})
	handler.FileVersionFetcher = testPlaybackFileVersionFetcher{byContent: map[string][]*models.MediaFile{source.ContentID: {source, alternate}}}
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"allow_4k_transcode": "false"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.SubtitleRepo = repo
	// A 1080p client cannot play the 4K requested edition, so the start
	// lands on the 1080p alternate.
	startRequest := v3HandlerStartRequest()
	startRequest.QualityPreference = "auto"
	for _, delivery := range []string{playback.DeliveryClassOriginalHTTPV3, playback.DeliveryClassHLSV3} {
		startRequest.ClientPlaybackContext.Deliveries[delivery] = playback.DeliveryCapabilityV3{
			Enabled: true, SupportedOnDevice: true,
			Subtitles: playback.DeliverySubtitleCapabilitiesV3{SidecarText: true},
		}
	}
	rec := httptest.NewRecorder()
	handler.HandleStartPlayback(rec, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, startRequest))).WithContext(newAuthorizedPlaybackContext()))
	var started playback.DecisionResponseV3
	if json.Unmarshal(rec.Body.Bytes(), &started) != nil || started.PlaybackPlan == nil || started.PlaybackPlan.EffectiveMediaFileID != alternate.ID {
		t.Fatalf("start did not land on the alternate: status=%d body=%s", rec.Code, rec.Body.String())
	}

	downloadedIndex := len(alternate.ExternalSubtitles) + len(alternate.SubtitleTracks)
	subtitled := postPlaybackReplanV3(t, handler, started.SessionID, playback.ReplanRequestV3{
		ProtocolVersion: playback.ProtocolV3, Operation: playback.ReplanOperationTrackChangeV3,
		PlaybackAttemptID: startRequest.PlaybackAttemptID, ReplanRequestID: "alternate-outage-track-0001",
		FailedPlanID: started.PlaybackPlan.PlanID, PlanAttemptID: "alternate-outage-track-attempt-0001",
		PlanAttemptKey: started.PlaybackPlan.PlanAttemptKey, AttemptCount: 1, QualityPreference: "auto",
		SelectedTracks: playback.SelectedTracksV3{
			Audio:    started.PlaybackPlan.SelectedTracks.Audio,
			Subtitle: &playback.TrackIdentityV3{ID: playback.TrackIDV3(alternate.ID, "subtitle", downloadedIndex), Index: &downloadedIndex},
		},
		Capabilities: startRequest.Capabilities, ClientPlaybackContext: startRequest.ClientPlaybackContext,
	})
	if subtitled.PlaybackPlan == nil || subtitled.PlaybackPlan.SelectedTracks.Subtitle == nil {
		t.Fatalf("downloaded subtitle on the alternate was not selected: %#v", subtitled.Terminal)
	}

	repo.listErr = errors.New("database unavailable")
	currentKey := playback.PlanAttemptKeyV3(*subtitled.PlaybackPlan, startRequest.ClientPlaybackContext.Output.OutputContextID, nil)
	requireDownloadedSubtitleOutageV3(t, postPlaybackReplanV3(t, handler, started.SessionID, playback.ReplanRequestV3{
		ProtocolVersion: playback.ProtocolV3, Operation: playback.ReplanOperationQualityChangeV3,
		PlaybackAttemptID: startRequest.PlaybackAttemptID, ReplanRequestID: "alternate-outage-quality-0001",
		FailedPlanID: subtitled.PlaybackPlan.PlanID, PlanAttemptID: "alternate-outage-quality-attempt-0001",
		PlanAttemptKey: currentKey, AttemptedPlanKeys: []string{currentKey}, AttemptCount: 1,
		QualityPreference: "1080p",
		SelectedTracks:    playback.SelectedTracksV3{Audio: subtitled.PlaybackPlan.SelectedTracks.Audio},
		Capabilities:      startRequest.Capabilities, ClientPlaybackContext: startRequest.ClientPlaybackContext,
	}))
}
