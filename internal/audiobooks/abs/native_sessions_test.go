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
		}, {
			ID:         43,
			ContentID:  "book-1",
			FilePath:   "/tmp/book-2.mp3",
			FileSize:   512,
			Duration:   1800,
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
		[]byte(`{"deviceInfo":{"clientName":"AudioBooth","clientVersion":"1.2.0"}}`),
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
	if native.ClientName != "AudioBooth" || native.ClientVersion != "1.2.0" {
		t.Fatalf("native client = %q %q, want the deviceInfo's AudioBooth 1.2.0", native.ClientName, native.ClientVersion)
	}
	if native.Position != 123.5 {
		t.Fatalf("native position = %v, want 123.5", native.Position)
	}
	// ABS positions are in the whole book, so the session measures them
	// against every file, not the first one it was started on.
	if native.TimelineDuration != 5400 {
		t.Fatalf("native timeline duration = %v, want 5400", native.TimelineDuration)
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
	if len(tracks) != 2 {
		t.Fatalf("audioTracks length = %d, want 2", len(tracks))
	}
	track, _ := tracks[0].(map[string]any)
	if got, _ := track["contentUrl"].(string); got == "" || !strings.Contains(got, "/abs/public/session/"+sessionID+"/track/1") {
		t.Fatalf("contentUrl = %q, want session-scoped URL", got)
	}
}

func TestHandleSessionCloseFinishesNativePlaybackSession(t *testing.T) {
	for _, tc := range []struct {
		name         string
		body         string
		closed       bool
		wantPosition float64
	}{
		{name: "empty body", wantPosition: 100},
		// A close body is the client's final sync (#3099): the play is
		// recorded where the listener stopped, not at the last sync.
		{name: "final sync", body: `{"currentTime":3700,"timeListened":30}`, wantPosition: 3700},
		// The final sync is best effort; the session closes regardless.
		{name: "unreadable body", body: `not json`, wantPosition: 100},
		// A body without a position is no final sync, not a sync to 0.
		{name: "no position", body: `{}`, wantPosition: 100},
		// A repeated close applies no stale final sync.
		{name: "already closed", body: `{"currentTime":3700,"timeListened":30}`, closed: true, wantPosition: 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			absSessions := &fakePlaybackSessionStore{}
			nativeSessions := playback.NewSessionManager(0, 0)
			var finished []playback.Session
			nativeSessions.AddFinishHook(func(_ context.Context, s *playback.Session) {
				finished = append(finished, *s)
			})
			native, err := nativeSessions.StartSessionWithFilesContext(context.Background(), 1, "profile-1", 42, 42, playback.PlayDirect, false)
			if err != nil {
				t.Fatalf("start native session: %v", err)
			}
			if err := nativeSessions.UpdateProgress(native.ID, 100, false); err != nil {
				t.Fatalf("seed native position: %v", err)
			}
			var closedAt *time.Time
			if tc.closed {
				closedAt = new(time.Now())
			}
			_ = absSessions.InsertPlaybackSession(context.Background(), ABSPlaybackSession{
				ID:        native.ID,
				UserID:    "1",
				ProfileID: "profile-1",
				ContentID: "book-1",
				ClosedAt:  closedAt,
			})
			syncer := &recordingPlaybackSessionSyncer{}
			h := New(Dependencies{
				MediaStore:           &playStartMediaStore{item: &models.MediaItem{ContentID: "book-1", Type: "audiobook"}},
				PlaybackSessionStore: absSessions,
				NativeSessions:       nativeSessions,
				NativeSessionSyncer:  syncer,
			})

			var body []byte
			if tc.body != "" {
				body = []byte(tc.body)
			}
			rec := dispatchABSWithParams(
				http.MethodPost,
				"/api/session/"+native.ID+"/close",
				map[string]string{"sid": native.ID},
				body,
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
			// The finish hook is what records the play in admin playback history.
			if len(finished) != 1 || finished[0].ID != native.ID {
				t.Fatalf("finish hooks ran %d times, want once for %s", len(finished), native.ID)
			}
			if finished[0].Position != tc.wantPosition {
				t.Fatalf("finished position = %v, want %v", finished[0].Position, tc.wantPosition)
			}
			if syncer.calls == 0 {
				t.Fatalf("native session syncer was not called")
			}
		})
	}
}

// A sync's listening time is added to the session's stored total, so it is
// bounded: a negative value must not lower the total, and a huge one must not
// overflow it.
func TestSyncPayloadTimeDeltaIsBounded(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    syncPayload
		want float64
	}{
		{name: "timeListening", p: syncPayload{TimeListening: 20}, want: 20},
		{name: "timeListened", p: syncPayload{TimeListened: 20}, want: 20},
		{name: "negative", p: syncPayload{TimeListened: -500}, want: 0},
		{name: "huge", p: syncPayload{TimeListened: 1e12}, want: maxSyncListeningSeconds},
	} {
		if got := tc.p.timeDelta(); got != tc.want {
			t.Errorf("%s: timeDelta = %v, want %v", tc.name, got, tc.want)
		}
	}
}
