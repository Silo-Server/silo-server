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
	streamURL       string
	err             error
}

func (r *recordingVirtualPlaybackResolver) ResolveVirtualPlayback(_ context.Context, path string, userID int, profileID string) (string, error) {
	r.path, r.userID, r.profileID = path, userID, profileID
	return r.streamURL, r.err
}

func TestHandleStartPlaybackVirtualLegacyReturnsExternalURL(t *testing.T) {
	file := &models.MediaFile{ID: 42, ContentID: "movie-1", FilePath: "virtual://movie/tt0133093", Duration: 8160}
	resolver := &recordingVirtualPlaybackResolver{streamURL: "https://stream.example/movie.mkv?token=secret"}
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.VirtualPlaybackResolver = resolver

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
	if response.StreamURL != resolver.streamURL || response.PlaybackInfo == nil || response.PlaybackInfo.StreamType != "external_http" {
		t.Fatalf("response = %#v", response)
	}
	if resolver.path != file.FilePath || resolver.userID != 1 || resolver.profileID != "profile-1" {
		t.Fatalf("resolver call = path %q user %d profile %q", resolver.path, resolver.userID, resolver.profileID)
	}
	if len(manager.AllSessions()) != 1 {
		t.Fatalf("sessions = %d, want 1", len(manager.AllSessions()))
	}
}

func TestHandleStartPlaybackVirtualV3ReturnsExternalPlan(t *testing.T) {
	file := &models.MediaFile{ID: 42, ContentID: "movie-1", FilePath: "virtual://movie/tt0133093", Duration: 8160}
	resolver := &recordingVirtualPlaybackResolver{streamURL: "https://stream.example/movie.mkv?token=secret"}
	manager := playback.NewSessionManager(0, 0)
	handler := NewPlaybackHandler(manager, testPlaybackFileResolver{file: file})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"playback.protocol_v3_enabled": "true"}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.VirtualPlaybackResolver = resolver

	start := v3HandlerStartRequest()
	start.FileID = file.ID
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
	if response.PlaybackPlan == nil || response.PlaybackPlan.Stream.URL != resolver.streamURL {
		t.Fatalf("response = %#v", response)
	}
	if response.PlaybackPlan.DecisionReason != "virtual_playback_resolver" || response.PlaybackPlan.Source.MediaFileID != file.ID {
		t.Fatalf("plan = %#v", response.PlaybackPlan)
	}
	if len(manager.AllSessions()) != 1 {
		t.Fatalf("sessions = %d, want 1", len(manager.AllSessions()))
	}
}
