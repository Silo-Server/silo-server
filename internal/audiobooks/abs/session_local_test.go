package abs

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
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

func TestSyncLocalSession_UpdatesPosition(t *testing.T) {
	prog := &positionRecordingProgressFake{}
	media := &stubMediaStore{known: map[string]*models.MediaItem{"book-1": nil}}
	pub := &recordingPublisher{}
	h := New(Dependencies{MediaStore: media, ProgressStore: prog, Publisher: pub})

	body := []byte(`{"id":"sess-1","libraryItemId":"book-1","currentTime":123.5,"timeListening":60}`)
	rec := dispatchABSWithParams(http.MethodPost, "/api/session/local", nil, body, "1", "", h.handleSyncLocalSession)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	got, ok := prog.pos("book-1")
	if !ok {
		t.Fatalf("progress not persisted for book-1")
	}
	if got != 123.5 {
		t.Errorf("position = %v, want 123.5", got)
	}
	// No pre-existing row: the position must be persisted via a create (upsert),
	// not the UPDATE-only path that would silently drop an offline-only book.
	if p := prog.path("book-1"); p != "create" {
		t.Errorf("persist path = %q, want create", p)
	}
	// Realtime event should fire so other clients catch up.
	found := false
	for _, ev := range pub.snapshot() {
		if ev.Event == "user_item_progress_updated" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected user_item_progress_updated event")
	}
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

func TestSyncLocalSession_UnknownItem_500(t *testing.T) {
	prog := &positionRecordingProgressFake{}
	media := &stubMediaStore{known: map[string]*models.MediaItem{}}
	h := New(Dependencies{MediaStore: media, ProgressStore: prog})

	body := []byte(`{"id":"sess-1","libraryItemId":"ghost","currentTime":10}`)
	rec := dispatchABSWithParams(http.MethodPost, "/api/session/local", nil, body, "1", "", h.handleSyncLocalSession)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
	if _, ok := prog.pos("ghost"); ok {
		t.Errorf("position should not be written for unknown item")
	}
}

func TestSyncLocalSessions_Batch_ResultsShape(t *testing.T) {
	prog := &positionRecordingProgressFake{}
	media := &stubMediaStore{known: map[string]*models.MediaItem{"book-1": nil, "book-2": nil}}
	h := New(Dependencies{MediaStore: media, ProgressStore: prog})

	body := []byte(`{"sessions":[
		{"id":"s1","libraryItemId":"book-1","currentTime":11},
		{"id":"s2","libraryItemId":"ghost","currentTime":22},
		{"id":"s3","libraryItemId":"book-2","currentTime":33}
	]}`)
	rec := dispatchABSWithParams(http.MethodPost, "/api/session/local-all", nil, body, "1", "", h.handleSyncLocalSessions)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Results []localSyncResult `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	if len(resp.Results) != 3 {
		t.Fatalf("results len = %d, want 3", len(resp.Results))
	}
	// s1 and s3 succeed; the unknown ghost item fails but does not sink the batch.
	if !resp.Results[0].Success || !resp.Results[0].ProgressSynced {
		t.Errorf("s1 result = %+v, want success+synced", resp.Results[0])
	}
	if resp.Results[1].Success {
		t.Errorf("s2 (ghost) should not succeed: %+v", resp.Results[1])
	}
	if !resp.Results[2].Success {
		t.Errorf("s3 result = %+v, want success", resp.Results[2])
	}
	if p, _ := prog.pos("book-1"); p != 11 {
		t.Errorf("book-1 position = %v, want 11", p)
	}
	if p, _ := prog.pos("book-2"); p != 33 {
		t.Errorf("book-2 position = %v, want 33", p)
	}
}

func TestSyncLocalSessions_MalformedSessionSkipped(t *testing.T) {
	prog := &positionRecordingProgressFake{}
	media := &stubMediaStore{known: map[string]*models.MediaItem{"book-1": nil}}
	h := New(Dependencies{MediaStore: media, ProgressStore: prog})

	// Second session is not an object — must be skipped, not fatal.
	body := []byte(`{"sessions":[{"id":"s1","libraryItemId":"book-1","currentTime":5}, 42]}`)
	rec := dispatchABSWithParams(http.MethodPost, "/api/session/local-all", nil, body, "1", "", h.handleSyncLocalSessions)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Results []localSyncResult `json:"results"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Results) != 2 {
		t.Fatalf("results len = %d, want 2", len(resp.Results))
	}
	if !resp.Results[0].Success {
		t.Errorf("s1 should succeed: %+v", resp.Results[0])
	}
	if resp.Results[1].Success {
		t.Errorf("malformed session should be marked failure: %+v", resp.Results[1])
	}
}
