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

func (s *playStartMediaStore) GetItemType(ctx context.Context, id string, access catalog.AccessFilter) (string, error) {
	return itemTypeFromLookup(s.GetAudiobookByID(ctx, id, access))
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
			ContentID: testBookID,
			Type:      mediaTypeAudiobook,
			Title:     "Native Session Book",
			UpdatedAt: now,
			AddedAt:   &now,
		},
		files: []*models.MediaFile{{
			ID:         42,
			ContentID:  testBookID,
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
		ProfileID:       testProfileID,
		ContentID:       testBookID,
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
		map[string]string{"libraryItemId": testBookID}, //nolint:goconst // External ABS route key.
		nil,
		"1",
		testProfileID,
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

func TestHandlePlayStartRejectsEbookWithoutCreatingSessions(t *testing.T) {
	media := &playStartMediaStore{
		item:  &models.MediaItem{ContentID: testEbookID, Type: mediaTypeEbook, Title: "Reader Test"}, //nolint:goconst // Stable fixture label.
		files: []*models.MediaFile{{ID: 42, ContentID: testEbookID, FilePath: "/tmp/book.epub"}},
	}
	absSessions := &fakePlaybackSessionStore{}
	nativeSessions := playback.NewSessionManager(0, 0)
	h := New(Dependencies{
		MediaStore:           media,
		PlaybackSessionStore: absSessions,
		NativeSessions:       nativeSessions,
	})

	rec := dispatchABSWithParams(http.MethodPost, "/api/items/ebook-1/play",
		map[string]string{"libraryItemId": testEbookID}, nil, "1", testProfileID, h.handlePlayStart)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	if len(absSessions.sessions) != 0 || nativeSessions.ActiveCount(1) != 0 {
		t.Fatalf("ebook play created sessions: abs=%d native=%d", len(absSessions.sessions), nativeSessions.ActiveCount(1))
	}
}

func TestHandleSessionSyncUpdatesNativePlaybackSession(t *testing.T) {
	media := &playStartMediaStore{
		item: &models.MediaItem{ContentID: testBookID, Type: mediaTypeAudiobook, Title: "Book", UpdatedAt: time.Now()},
	}
	absSessions := &fakePlaybackSessionStore{}
	nativeSessions := playback.NewSessionManager(0, 0)
	native, err := nativeSessions.StartSessionWithFilesContext(context.Background(), 1, testProfileID, 42, 42, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("start native session: %v", err)
	}
	_ = absSessions.InsertPlaybackSession(context.Background(), ABSPlaybackSession{
		ID:        native.ID,
		UserID:    "1",
		ProfileID: testProfileID,
		ContentID: testBookID,
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
		testProfileID,
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

func TestHandleSessionSyncRejectsEbookWithoutUpdatingAudioProgress(t *testing.T) {
	media := &playStartMediaStore{
		item: &models.MediaItem{ContentID: testEbookID, Type: mediaTypeEbook, Title: "Reader Test"},
	}
	absSessions := &fakePlaybackSessionStore{}
	_ = absSessions.InsertPlaybackSession(context.Background(), ABSPlaybackSession{
		ID: "ebook-session", UserID: "1", ProfileID: testProfileID, ContentID: testEbookID,
	})
	progress := &positionRecordingProgressFake{}
	h := New(Dependencies{MediaStore: media, ProgressStore: progress, PlaybackSessionStore: absSessions})

	rec := dispatchABSWithParams(http.MethodPatch, "/api/session/ebook-session",
		map[string]string{"sid": "ebook-session"}, []byte(`{"currentTime":55.25}`), "1", testProfileID, h.handleSessionSync)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	if absSessions.syncCalls != 0 {
		t.Fatalf("ebook audio session sync calls = %d, want 0", absSessions.syncCalls)
	}
	if _, ok := progress.pos(testEbookID); ok {
		t.Fatal("ebook audio session sync wrote user_watch_progress")
	}
}

func TestHandleSessionCloseStopsNativePlaybackSession(t *testing.T) {
	absSessions := &fakePlaybackSessionStore{}
	nativeSessions := playback.NewSessionManager(0, 0)
	native, err := nativeSessions.StartSessionWithFilesContext(context.Background(), 1, testProfileID, 42, 42, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("start native session: %v", err)
	}
	_ = absSessions.InsertPlaybackSession(context.Background(), ABSPlaybackSession{
		ID:        native.ID,
		UserID:    "1",
		ProfileID: testProfileID,
		ContentID: testBookID,
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
		testProfileID,
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
