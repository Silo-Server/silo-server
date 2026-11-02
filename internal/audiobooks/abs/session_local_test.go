package abs

import (
	"context"
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
