package abs

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

type playStartMediaStore struct {
	noopMediaStore
	item  *models.MediaItem
	files []*models.MediaFile
}

func (s *playStartMediaStore) GetAudiobookByID(_ context.Context, id string, _ catalog.AccessFilter) (*models.MediaItem, error) {
	if s.item != nil && s.item.ContentID == id {
		return s.item, nil
	}
	return nil, nil
}

func (s *playStartMediaStore) GetMediaFiles(_ context.Context, contentID string, _ catalog.AccessFilter) ([]*models.MediaFile, error) {
	if s.item != nil && s.item.ContentID == contentID {
		return s.files, nil
	}
	return nil, nil
}

type recordingPlaybackSessionSyncer struct {
	calls int
}

func (s *recordingPlaybackSessionSyncer) SyncNow(context.Context) error {
	s.calls++
	return nil
}

func TestHandlePlayStartCreatesNativePlaybackSession(t *testing.T) {
	now := time.Now()
	media := &playStartMediaStore{
		item: &models.MediaItem{
			ContentID: "book-1",
			Type:      "audiobook",
			Title:     "Native Session Book",
			UpdatedAt: now,
			AddedAt:   &now,
		},
		files: []*models.MediaFile{{
			ID:         42,
			ContentID:  "book-1",
			FilePath:   "/tmp/book.mp3",
			FileSize:   1024,
			Duration:   3600,
			Bitrate:    128,
			CodecAudio: "mp3",
		}},
	}
	absSessions := &fakePlaybackSessionStore{}
	nativeSessions := playback.NewSessionManager(0, 0)
	syncer := &recordingPlaybackSessionSyncer{}
	progress := &fakeProgressStore{row: &ProgressRow{
		UserID:          "1",
		ProfileID:       "profile-1",
		ContentID:       "book-1",
		CurrentSeconds:  123.5,
		DurationSeconds: 3600,
		UpdatedAt:       now,
	}}
	h := New(Dependencies{
		MediaStore:           media,
		ProgressStore:        progress,
		PlaybackSessionStore: absSessions,
		NativeSessions:       nativeSessions,
		NativeSessionSyncer:  syncer,
	})

	rec := dispatchABSWithParams(
		http.MethodPost,
		"/api/items/book-1/play",
		map[string]string{"libraryItemId": "book-1"},
		nil,
		"1",
		"profile-1",
		h.handlePlayStart,
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	sessionID, _ := body["id"].(string)
	if sessionID == "" {
		t.Fatalf("response id is empty: %#v", body["id"])
	}
	native, err := nativeSessions.GetSession(sessionID)
	if err != nil {
		t.Fatalf("native session %q missing: %v", sessionID, err)
	}
	if native.MediaFileID != 42 || native.RequestedMediaFileID != 42 {
		t.Fatalf("native file ids = (%d, %d), want (42, 42)", native.MediaFileID, native.RequestedMediaFileID)
	}
	if !native.DisableProgressPersistence {
		t.Fatalf("native session should disable progress persistence")
	}
	if native.Position != 123.5 {
		t.Fatalf("native position = %v, want 123.5", native.Position)
	}
	if syncer.calls == 0 {
		t.Fatalf("native session syncer was not called")
	}
	absSession, err := absSessions.GetPlaybackSession(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("ABS session %q missing: %v", sessionID, err)
	}
	if absSession.CurrentPositionSeconds != 123.5 {
		t.Fatalf("ABS session position = %v, want 123.5", absSession.CurrentPositionSeconds)
	}
	tracks, _ := body["audioTracks"].([]any)
	if len(tracks) != 1 {
		t.Fatalf("audioTracks length = %d, want 1", len(tracks))
	}
	track, _ := tracks[0].(map[string]any)
	if got, _ := track["contentUrl"].(string); got == "" || !strings.Contains(got, "/abs/public/session/"+sessionID+"/track/1") {
		t.Fatalf("contentUrl = %q, want session-scoped URL", got)
	}
}

func TestHandleSessionSyncUpdatesNativePlaybackSession(t *testing.T) {
	media := &playStartMediaStore{
		item: &models.MediaItem{ContentID: "book-1", Type: "audiobook", Title: "Book", UpdatedAt: time.Now()},
	}
	absSessions := &fakePlaybackSessionStore{}
	nativeSessions := playback.NewSessionManager(0, 0)
	native, err := nativeSessions.StartSessionWithFilesContext(context.Background(), 1, "profile-1", 42, 42, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("start native session: %v", err)
	}
	_ = absSessions.InsertPlaybackSession(context.Background(), ABSPlaybackSession{
		ID:        native.ID,
		UserID:    "1",
		ProfileID: "profile-1",
		ContentID: "book-1",
	})
	syncer := &recordingPlaybackSessionSyncer{}
	h := New(Dependencies{
		MediaStore:           media,
		ProgressStore:        &fakeProgressStore{},
		PlaybackSessionStore: absSessions,
		NativeSessions:       nativeSessions,
		NativeSessionSyncer:  syncer,
	})

	rec := dispatchABSWithParams(
		http.MethodPatch,
		"/api/session/"+native.ID,
		map[string]string{"sid": native.ID},
		[]byte(`{"currentTime":55.25,"timeListening":10}`),
		"1",
		"profile-1",
		h.handleSessionSync,
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	updated, err := nativeSessions.GetSession(native.ID)
	if err != nil {
		t.Fatalf("native session missing: %v", err)
	}
	if updated.Position != 55.25 {
		t.Fatalf("native position = %v, want 55.25", updated.Position)
	}
	if updated.IsPaused {
		t.Fatalf("native session should be marked playing")
	}
	if syncer.calls == 0 {
		t.Fatalf("native session syncer was not called")
	}
}

func TestHandleSessionCloseStopsNativePlaybackSession(t *testing.T) {
	absSessions := &fakePlaybackSessionStore{}
	nativeSessions := playback.NewSessionManager(0, 0)
	native, err := nativeSessions.StartSessionWithFilesContext(context.Background(), 1, "profile-1", 42, 42, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("start native session: %v", err)
	}
	_ = absSessions.InsertPlaybackSession(context.Background(), ABSPlaybackSession{
		ID:        native.ID,
		UserID:    "1",
		ProfileID: "profile-1",
		ContentID: "book-1",
	})
	syncer := &recordingPlaybackSessionSyncer{}
	h := New(Dependencies{
		MediaStore:           noopMediaStore{},
		PlaybackSessionStore: absSessions,
		NativeSessions:       nativeSessions,
		NativeSessionSyncer:  syncer,
	})

	rec := dispatchABSWithParams(
		http.MethodPost,
		"/api/session/"+native.ID+"/close",
		map[string]string{"sid": native.ID},
		nil,
		"1",
		"profile-1",
		h.handleSessionClose,
	)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	if _, err := nativeSessions.GetSession(native.ID); err == nil {
		t.Fatalf("native session still exists after close")
	}
	if syncer.calls == 0 {
		t.Fatalf("native session syncer was not called")
	}
}
