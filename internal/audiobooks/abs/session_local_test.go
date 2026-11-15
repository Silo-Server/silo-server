package abs

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// positionRecordingProgressFake models just enough of the progress store for the
// offline-sync tests: GetProgress returns any pre-seeded row, and both the
// create (UpsertProgress) and update (UpdateProgressPosition) paths record the
// persisted position plus which path handled each item.
type positionRecordingProgressFake struct {
	fakeProgressStore
	mu      sync.Mutex
	seeded  map[string]*ProgressRow // contentID → existing row GetProgress returns
	calls   map[string]float64      // contentID → last persisted position (either path)
	viaPath map[string]string       // contentID → "create" | "update"
}

func (f *positionRecordingProgressFake) GetProgress(_ context.Context, _, _, contentID string) (*ProgressRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.seeded[contentID]; ok {
		return r, nil
	}
	return nil, nil
}

func (f *positionRecordingProgressFake) UpsertProgress(_ context.Context, row ProgressRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordLocked(row.ContentID, row.CurrentSeconds, "create")
	return nil
}

func (f *positionRecordingProgressFake) UpdateProgressPosition(_ context.Context, _, _, contentID string, pos float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordLocked(contentID, pos, "update")
	return nil
}

func (f *positionRecordingProgressFake) recordLocked(contentID string, pos float64, path string) {
	if f.calls == nil {
		f.calls = map[string]float64{}
		f.viaPath = map[string]string{}
	}
	f.calls[contentID] = pos
	f.viaPath[contentID] = path
}

func (f *positionRecordingProgressFake) pos(contentID string) (float64, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.calls[contentID]
	return v, ok
}

func (f *positionRecordingProgressFake) path(contentID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.viaPath[contentID]
}

func TestSyncLocalSession_ExistingRow_UsesUpdate(t *testing.T) {
	// A row already exists (book was played online before), so the offline sync
	// must advance it via the monotonic UPDATE path — not recreate it — to avoid
	// clobbering is_finished / progress_pct the user set explicitly.
	prog := &positionRecordingProgressFake{
		seeded: map[string]*ProgressRow{"book-1": {ContentID: "book-1", CurrentSeconds: 5}},
	}
	media := &stubMediaStore{known: map[string]*models.MediaItem{"book-1": nil}}
	h := New(Dependencies{MediaStore: media, ProgressStore: prog})

	body := []byte(`{"id":"sess-1","libraryItemId":"book-1","currentTime":200}`)
	rec := dispatchABSWithParams(http.MethodPost, "/api/session/local", nil, body, "1", "", h.handleSyncLocalSession)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if p := prog.path("book-1"); p != "update" {
		t.Errorf("persist path = %q, want update", p)
	}
	if got, _ := prog.pos("book-1"); got != 200 {
		t.Errorf("position = %v, want 200", got)
	}
}

// A live local-session report records a reported play, announced once to
// the lifecycle hooks, and a late sync records none. The play's id opens no
// media and takes no /play sync or close: the server serves nothing for a
// play it only hears about, and such plays are outside stream limits.
func TestLiveLocalSessionReportsAPlayThatIsNeverServed(t *testing.T) {
	audio := makeTempAudio(t)
	media := &playStartMediaStore{
		item:  &models.MediaItem{ContentID: "book-1", Type: "audiobook", Title: "Book"},
		files: []*models.MediaFile{{ID: 42, ContentID: "book-1", FilePath: audio, Duration: 3600}},
	}
	store := &fakePlaybackSessionStore{}
	sessions := playback.NewSessionManager(0, 0)
	var started []playback.Session
	sessions.AddLifecycleHooks(playback.LifecycleHooks{Started: func(_ context.Context, s *playback.Session, frontend string) {
		if frontend != "abs" {
			t.Errorf("frontend = %q, want abs", frontend)
		}
		started = append(started, *s)
	}})
	h := New(Dependencies{MediaStore: media, ProgressStore: &positionRecordingProgressFake{}, PlaybackSessionStore: store, NativeSessions: sessions})

	report := func(clientSession string, updated time.Time, position int) {
		t.Helper()
		body := fmt.Sprintf(`{"id":%q,"libraryItemId":"book-1","currentTime":%d,"timeListening":10,"updatedAt":%d,"deviceInfo":{"clientName":"Plappa"}}`,
			clientSession, position, updated.UnixMilli())
		if rec := dispatchABSWithParams(http.MethodPost, "/api/session/local", nil, []byte(body), "1", "p-1", h.handleSyncLocalSession); rec.Code != http.StatusOK {
			t.Fatalf("report status = %d, body %s", rec.Code, rec.Body.String())
		}
	}
	report("local-1", time.Now(), 100)
	report("local-1", time.Now(), 110)
	report("local-late", time.Now().Add(-10*time.Minute), 50)

	if len(started) != 1 || started[0].ClientName != "Plappa" || started[0].MediaFileID != 42 || started[0].TimelineDuration != 3600 {
		t.Fatalf("lifecycle starts = %+v, want one for the live play", started)
	}
	id := started[0].ID
	if sess, err := store.GetPlaybackSession(context.Background(), id); err != nil || !sess.ReportOnly || sess.CurrentPositionSeconds != 110 {
		t.Fatalf("reported play row = %+v, %v; want report-only at 110 s", sess, err)
	}
	if len(store.sessions) != 1 {
		t.Fatalf("%d session rows, want only the live play's", len(store.sessions))
	}

	if rec := dispatchTrack(h, http.MethodGet, id, "1"); rec.Code != http.StatusNotFound {
		t.Errorf("public track status = %d, want 404", rec.Code)
	}
	params := map[string]string{"sid": id}
	if rec := dispatchABSWithParams(http.MethodPatch, "/api/session/"+id+"/sync", params, []byte(`{"currentTime":500,"timeListened":30}`), "1", "p-1", h.handleSessionSync); rec.Code != http.StatusNotFound {
		t.Errorf("sync status = %d, want 404", rec.Code)
	}
	if rec := dispatchABSWithParams(http.MethodPost, "/api/session/"+id+"/close", params, nil, "1", "p-1", h.handleSessionClose); rec.Code != http.StatusNotFound {
		t.Errorf("close status = %d, want 404", rec.Code)
	}
}
