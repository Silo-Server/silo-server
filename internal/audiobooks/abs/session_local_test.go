package abs

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
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
	report("local-ahead", time.Now().Add(45*time.Second), 50)

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

// A batch names its client once, in the envelope's deviceInfo, and a play it
// reports carries that client.
func TestLocalSessionBatchNamesItsClient(t *testing.T) {
	media := &playStartMediaStore{
		item:  &models.MediaItem{ContentID: "book-1", Type: "audiobook", Title: "Book"},
		files: []*models.MediaFile{{ID: 42, ContentID: "book-1", Duration: 3600}},
	}
	sessions := playback.NewSessionManager(0, 0)
	var started []playback.Session
	sessions.AddLifecycleHooks(playback.LifecycleHooks{Started: func(_ context.Context, s *playback.Session, _ string) {
		started = append(started, *s)
	}})
	h := New(Dependencies{MediaStore: media, ProgressStore: &positionRecordingProgressFake{}, PlaybackSessionStore: &fakePlaybackSessionStore{}, NativeSessions: sessions})
	body := fmt.Sprintf(`{"deviceInfo":{"ClientName":"Prologue","clientVersion":"2026.1"},"sessions":[{"id":"local-1","libraryItemId":"book-1","currentTime":10,"updatedAt":%d}]}`,
		time.Now().UnixMilli())
	if rec := dispatchABSWithParams(http.MethodPost, "/api/session/local-all", nil, []byte(body), "1", "p-1", h.handleSyncLocalSessions); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if len(started) != 1 || started[0].ClientName != "Prologue" || started[0].ClientVersion != "2026.1" {
		t.Fatalf("lifecycle starts = %+v, want one for Prologue 2026.1", started)
	}
}

// Malformed optional fields (a client name that is not a string, an update
// time that is not a number) never reject the sessions that carry them.
func TestLocalSessionMalformedOptionalFieldsStillSync(t *testing.T) {
	prog := &positionRecordingProgressFake{}
	media := &stubMediaStore{known: map[string]*models.MediaItem{"book-1": nil}}
	h := New(Dependencies{MediaStore: media, ProgressStore: prog})
	body := []byte(`{"deviceInfo":{"clientName":7},"sessions":[{"id":"s1","libraryItemId":"book-1","currentTime":42,"updatedAt":"soon","deviceInfo":{"clientVersion":[]}}]}`)
	rec := dispatchABSWithParams(http.MethodPost, "/api/session/local-all", nil, body, "1", "", h.handleSyncLocalSessions)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if pos, ok := prog.pos("book-1"); !ok || pos != 42 {
		t.Fatalf("synced position = %v (%v), want 42", pos, ok)
	}
}

// One request tracks at most the account cap's worth of distinct live
// sessions. Once the cap refuses a play it starts no other, while plays
// already open still take their reports.
func TestLocalSessionBatchTracksABoundedNumberOfPlays(t *testing.T) {
	media := &playStartMediaStore{
		item:  &models.MediaItem{ContentID: "book-1", Type: "audiobook", Title: "Book"},
		files: []*models.MediaFile{{ID: 42, ContentID: "book-1", Duration: 3600}},
	}
	store := &countingReportStore{open: map[string]bool{}}
	h := New(Dependencies{MediaStore: media, ProgressStore: &positionRecordingProgressFake{}, PlaybackSessionStore: store})
	batch := func(ids ...string) {
		t.Helper()
		var sessionsJSON []string
		for _, id := range ids {
			sessionsJSON = append(sessionsJSON, fmt.Sprintf(`{"id":%q,"libraryItemId":"book-1","currentTime":10,"updatedAt":%d}`, id, time.Now().UnixMilli()))
		}
		body := `{"sessions":[` + strings.Join(sessionsJSON, ",") + `]}`
		if rec := dispatchABSWithParams(http.MethodPost, "/api/session/local-all", nil, []byte(body), "1", "p-1", h.handleSyncLocalSessions); rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
	}
	var ids []string
	for i := range 3 * maxReportedPlaysPerAccount {
		ids = append(ids, fmt.Sprintf("local-%d", i), fmt.Sprintf("local-%d", i))
	}
	batch(ids...)
	if distinct := slices.Compact(slices.Sorted(slices.Values(store.updated))); len(store.updated) != maxReportedPlaysPerAccount || len(distinct) != len(store.updated) {
		t.Fatalf("tracked %v, want %d distinct sessions", store.updated, maxReportedPlaysPerAccount)
	}

	// At the cap: a new session is refused, and an open play after it in the
	// same request still takes its report.
	store.refuse, store.updated, store.starts, store.open["open-1"] = true, nil, 0, true
	batch("new-1", "new-2", "open-1")
	if store.starts != 1 || !slices.Contains(store.updated, "open-1") {
		t.Fatalf("after a refusal: %d starts, updated %v; want one start and open-1 updated", store.starts, store.updated)
	}
}

// countingReportStore records reported-play updates and starts. Sessions in
// open have a play; every other start succeeds, or with refuse is refused as
// the account cap would.
type countingReportStore struct {
	fakePlaybackSessionStore
	open    map[string]bool
	updated []string
	starts  int
	refuse  bool
}

func (s *countingReportStore) UpdateReportedPlay(_ context.Context, p ReportedPlay) (ReportedPlayState, bool, error) {
	s.updated = append(s.updated, p.ClientSessionID)
	if s.open[p.ClientSessionID] {
		return ReportedPlayState{ID: "p", Announced: true}, true, nil
	}
	return ReportedPlayState{}, false, nil
}

func (s *countingReportStore) StartReportedPlay(context.Context, ReportedPlay, string, int) (ReportedPlayState, error) {
	s.starts++
	if s.refuse {
		return ReportedPlayState{}, nil
	}
	return ReportedPlayState{ID: "p", Announced: true}, nil
}
