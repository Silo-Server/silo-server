package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

type recordingVirtualPlaybackResolver struct {
	path, profileID string
	userID          int
	ownerID         int
	streamURL       string
	err             error
}

func virtualPlaybackTestProber(_ context.Context, sourceURL string, file *models.MediaFile) (*models.MediaFile, error) {
	probed := *file
	probed.Container = "mp4"
	if strings.Contains(sourceURL, ".m3u8") {
		probed.Container = "hls"
	}
	probed.CodecVideo = "h264"
	probed.CodecAudio = "aac"
	probed.Resolution = "1080p"
	probed.Bitrate = 8_000
	probed.VideoTracks = []models.VideoTrack{{
		Codec: "h264", Profile: "high", Level: 41, BitDepth: 8,
		Width: 1920, Height: 1080, FrameRate: "24", Bitrate: 8_000,
		VideoRange: "SDR",
	}}
	probed.AudioTracks = []models.AudioTrack{{Codec: "aac", Channels: 2}}
	return &probed, nil
}

func (r *recordingVirtualPlaybackResolver) ResolveVirtualPlayback(_ context.Context, path string, userID int, profileID string, ownerInstallationID int) (string, error) {
	r.path, r.userID, r.profileID, r.ownerID = path, userID, profileID, ownerInstallationID
	return r.streamURL, r.err
}

func TestHandleStartPlaybackVirtualLegacyKeepsProviderURLServerSide(t *testing.T) {
	file := &models.MediaFile{ID: 42, ContentID: "movie-1", FilePath: "virtual://movie/tt0133093", Duration: 8160, VirtualOwnerInstallationID: 7}
	resolver := &recordingVirtualPlaybackResolver{streamURL: "https://stream.example/movie.mkv?token=secret"}
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.VirtualPlaybackResolver = resolver
	handler.VirtualPlaybackSourceProber = virtualPlaybackTestProber

	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(`{"file_id":42,"profile_id":"profile-1","play_method":"direct"}`))
	req = req.WithContext(newAuthorizedPlaybackContext())
	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var response playbackSessionResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.StreamURL == resolver.streamURL || strings.Contains(response.StreamURL, "token=secret") || !strings.HasPrefix(response.StreamURL, "/stream/") {
		t.Fatalf("response = %#v", response)
	}
	if resolver.path != file.FilePath || resolver.userID != 1 || resolver.profileID != "profile-1" || resolver.ownerID != 7 {
		t.Fatalf("resolver call = path %q user %d profile %q owner %d", resolver.path, resolver.userID, resolver.profileID, resolver.ownerID)
	}
	if len(manager.AllSessions()) != 1 {
		t.Fatalf("sessions = %d, want 1", len(manager.AllSessions()))
	}
}

func TestHandleStartPlaybackVirtualV3KeepsProviderURLServerSide(t *testing.T) {
	file := &models.MediaFile{ID: 42, ContentID: "movie-1", FilePath: "virtual://movie/tt0133093", Duration: 8160, VirtualOwnerInstallationID: 7}
	resolver := &recordingVirtualPlaybackResolver{streamURL: "https://stream.example/movie.mkv?token=secret"}
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"playback.protocol_v3_enabled": "true"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.VirtualPlaybackResolver = resolver
	handler.VirtualPlaybackSourceProber = virtualPlaybackTestProber

	start := v3HandlerStartRequest()
	start.FileID = file.ID
	start.ClientPlaybackContext.Engines[string(playback.EngineMedia3HLSV3)] = playback.EngineCapabilityV3{
		Enabled: true, SupportedOnDevice: true,
		Subtitles: playback.EngineSubtitleCapabilitiesV3{EmbeddedText: true, SidecarText: true},
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, start)))
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
	if response.PlaybackPlan == nil || response.PlaybackPlan.Stream.URL == resolver.streamURL ||
		strings.Contains(response.PlaybackPlan.Stream.URL, "token=secret") {
		t.Fatalf("response = %#v", response)
	}
	if response.PlaybackPlan.Source.MediaFileID != file.ID {
		t.Fatalf("plan = %#v", response.PlaybackPlan)
	}
	if response.PlaybackPlan.Engine != playback.EngineMedia3DirectV3 || response.PlaybackPlan.Stream.Protocol != playback.StreamHTTPProgressiveV3 {
		t.Fatalf("expected media3_direct / http_progressive, got engine=%s protocol=%s", response.PlaybackPlan.Engine, response.PlaybackPlan.Stream.Protocol)
	}
	if len(manager.AllSessions()) != 1 {
		t.Fatalf("sessions = %d, want 1", len(manager.AllSessions()))
	}
}

func TestHandleStartPlaybackVirtualV3DoesNotExposeHLSProviderURLOnTransportFailure(t *testing.T) {
	file := &models.MediaFile{ID: 43, ContentID: "movie-2", FilePath: "virtual://movie/tt0133094", Duration: 8160, VirtualOwnerInstallationID: 7}
	resolver := &recordingVirtualPlaybackResolver{streamURL: "https://stream.example/manifest.m3u8?token=secret"}
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"playback.protocol_v3_enabled": "true"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.VirtualPlaybackResolver = resolver
	handler.VirtualPlaybackSourceProber = virtualPlaybackTestProber

	start := v3HandlerStartRequest()
	start.FileID = file.ID
	start.ClientPlaybackContext.Engines[string(playback.EngineMedia3HLSV3)] = playback.EngineCapabilityV3{
		Enabled: true, SupportedOnDevice: true,
		Subtitles: playback.EngineSubtitleCapabilitiesV3{EmbeddedText: true, SidecarText: true},
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, start)))
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
	if strings.Contains(rr.Body.String(), resolver.streamURL) || strings.Contains(rr.Body.String(), "token=secret") {
		t.Fatalf("provider credential leaked in response: %s", rr.Body.String())
	}
	if response.Terminal == nil || response.Terminal.Reason == "" {
		t.Fatalf("response = %#v, want a safe transport-unavailable terminal", response)
	}
}
