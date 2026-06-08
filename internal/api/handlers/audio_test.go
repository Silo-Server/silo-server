package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

type stubAudioDetailService struct {
	detail *catalog.ItemDetail
}

func (s stubAudioDetailService) GetItemDetail(context.Context, string, catalog.AccessFilter) (*catalog.ItemDetail, error) {
	return s.detail, nil
}

type stubAudioFiles struct {
	files []*models.MediaFile
}

func (s stubAudioFiles) GetByContentID(context.Context, string) ([]*models.MediaFile, error) {
	return append([]*models.MediaFile(nil), s.files...), nil
}

func (s stubAudioFiles) GetByID(_ context.Context, id int) (*models.MediaFile, error) {
	for _, file := range s.files {
		if file.ID == id {
			return file, nil
		}
	}
	return nil, nil
}

func TestAudioStartPlaybackOrdersTracksAndBuildsGlobalChapters(t *testing.T) {
	handler := NewAudioHandler(
		stubAudioDetailService{detail: testAudiobookDetail()},
		stubAudioFiles{files: []*models.MediaFile{
			testAudioFile(3, "part-03.mp3", 30, 30),
			testAudioFile(1, "part-01.mp3", 10, 10),
			testAudioFile(2, "part-02.mp3", 20, 20),
		}},
		testUserStoreProvider{store: newPlaybackTestStore(t)},
		nil,
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/audio/playback/start", strings.NewReader(`{"content_id":"book-1"}`))
	req = req.WithContext(newAuthorizedPlaybackContext())
	rec := httptest.NewRecorder()

	handler.HandleStartPlayback(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp audioPlaybackResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Tracks) != 3 {
		t.Fatalf("tracks = %d, want 3", len(resp.Tracks))
	}
	if resp.Tracks[0].FileID != 1 || resp.Tracks[1].FileID != 2 || resp.Tracks[2].FileID != 3 {
		t.Fatalf("track order = [%d %d %d], want [1 2 3]", resp.Tracks[0].FileID, resp.Tracks[1].FileID, resp.Tracks[2].FileID)
	}
	if resp.Tracks[1].StartOffsetSeconds != 10 || resp.Tracks[2].StartOffsetSeconds != 30 {
		t.Fatalf("offsets = %.0f %.0f, want 10 30", resp.Tracks[1].StartOffsetSeconds, resp.Tracks[2].StartOffsetSeconds)
	}
	if len(resp.Chapters) != 3 || resp.Chapters[2].StartSeconds != 30 {
		t.Fatalf("global chapters = %+v, want third chapter at 30s", resp.Chapters)
	}
}

func TestAudioSyncPersistsCumulativeProgressAndEnforcesOwner(t *testing.T) {
	store := newPlaybackTestStore(t)
	handler := NewAudioHandler(
		stubAudioDetailService{detail: testAudiobookDetail()},
		stubAudioFiles{files: []*models.MediaFile{
			testAudioFile(1, "part-01.mp3", 10, 10),
			testAudioFile(2, "part-02.mp3", 20, 20),
		}},
		testUserStoreProvider{store: store},
		nil,
	)

	startReq := httptest.NewRequest(http.MethodPost, "/api/v1/audio/playback/start", strings.NewReader(`{"content_id":"book-1"}`))
	startReq = startReq.WithContext(newAuthorizedPlaybackContext())
	startRec := httptest.NewRecorder()
	handler.HandleStartPlayback(startRec, startReq)
	if startRec.Code != http.StatusOK {
		t.Fatalf("start status = %d body=%s", startRec.Code, startRec.Body.String())
	}
	var start audioPlaybackResponse
	if err := json.Unmarshal(startRec.Body.Bytes(), &start); err != nil {
		t.Fatalf("decode start: %v", err)
	}

	otherCtx := apimw.SetClaims(context.Background(), &auth.Claims{UserID: 2, Role: "user", TokenType: auth.TokenTypeAccess})
	otherCtx = apimw.SetProfileID(otherCtx, "profile-1")
	denied := httptest.NewRequest(http.MethodPatch, "/api/v1/audio/playback/"+start.SessionID+"/sync", strings.NewReader(`{"position":12,"duration":30}`))
	denied = withAudioRouteParam(denied.WithContext(otherCtx), "session_id", start.SessionID)
	deniedRec := httptest.NewRecorder()
	handler.HandleSyncPlayback(deniedRec, denied)
	if deniedRec.Code != http.StatusForbidden {
		t.Fatalf("foreign sync status = %d, want 403", deniedRec.Code)
	}

	syncReq := httptest.NewRequest(http.MethodPatch, "/api/v1/audio/playback/"+start.SessionID+"/sync", strings.NewReader(`{"position":18,"duration":30}`))
	syncReq = withAudioRouteParam(syncReq.WithContext(newAuthorizedPlaybackContext()), "session_id", start.SessionID)
	syncRec := httptest.NewRecorder()
	handler.HandleSyncPlayback(syncRec, syncReq)
	if syncRec.Code != http.StatusOK {
		t.Fatalf("sync status = %d body=%s", syncRec.Code, syncRec.Body.String())
	}

	progress, err := store.GetProgress(context.Background(), "profile-1", "book-1")
	if err != nil {
		t.Fatalf("get progress: %v", err)
	}
	if progress == nil || progress.PositionSeconds != 18 || progress.DurationSeconds != 30 {
		t.Fatalf("progress = %+v, want 18/30", progress)
	}
}

func TestAudioSessionExpires(t *testing.T) {
	handler := NewAudioHandler(
		stubAudioDetailService{detail: testAudiobookDetail()},
		stubAudioFiles{files: []*models.MediaFile{testAudioFile(1, "part-01.mp3", 10, 10)}},
		testUserStoreProvider{store: newPlaybackTestStore(t)},
		nil,
	)
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	handler.now = func() time.Time { return now }

	startReq := httptest.NewRequest(http.MethodPost, "/api/v1/audio/playback/start", strings.NewReader(`{"content_id":"book-1"}`))
	startReq = startReq.WithContext(newAuthorizedPlaybackContext())
	startRec := httptest.NewRecorder()
	handler.HandleStartPlayback(startRec, startReq)
	var start audioPlaybackResponse
	if err := json.Unmarshal(startRec.Body.Bytes(), &start); err != nil {
		t.Fatalf("decode start: %v", err)
	}

	now = now.Add(audioPlaybackSessionTTL + time.Second)
	streamReq := httptest.NewRequest(http.MethodHead, "/api/v1/audio/playback/"+start.SessionID+"/tracks/0", nil)
	streamReq = withAudioRouteParam(streamReq.WithContext(newAuthorizedPlaybackContext()), "session_id", start.SessionID)
	streamReq = withAudioRouteParam(streamReq, "track_index", "0")
	streamRec := httptest.NewRecorder()
	handler.HandleStreamTrack(streamRec, streamReq)
	if streamRec.Code != http.StatusNotFound {
		t.Fatalf("expired stream status = %d, want 404", streamRec.Code)
	}
}

func testAudiobookDetail() *catalog.ItemDetail {
	return &catalog.ItemDetail{
		ContentID: "book-1",
		Type:      "audiobook",
		Title:     "Book One",
		Audiobook: &catalog.AudiobookDetailExtension{
			Authors: []catalog.AudiobookPerson{{Name: "Author One"}},
		},
	}
}

func testAudioFile(id int, name string, partIndex int, duration int) *models.MediaFile {
	return &models.MediaFile{
		ID:                    id,
		ContentID:             "book-1",
		FilePath:              "/media/" + name,
		CodecAudio:            "mp3",
		Container:             "mp3",
		Duration:              duration,
		PresentationPartIndex: partIndex,
		Chapters: []models.MediaChapter{{
			Index:        0,
			Title:        name,
			StartSeconds: 0,
			EndSeconds:   float64(duration),
		}},
	}
}

func withAudioRouteParam(req *http.Request, key, value string) *http.Request {
	routeCtx := chi.RouteContext(req.Context())
	if routeCtx == nil {
		routeCtx = chi.NewRouteContext()
	}
	routeCtx.URLParams.Add(key, value)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
}
