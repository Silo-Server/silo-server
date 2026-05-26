package abs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/models"
)

// fakePlaybackSessionStore is an in-memory ABSPlaybackSessionStore for the
// public-track tests. Only Get is exercised; the other methods are no-ops.
type fakePlaybackSessionStore struct {
	sessions map[string]ABSPlaybackSession
}

func (f *fakePlaybackSessionStore) InsertPlaybackSession(_ context.Context, s ABSPlaybackSession) error {
	if f.sessions == nil {
		f.sessions = map[string]ABSPlaybackSession{}
	}
	f.sessions[s.ID] = s
	return nil
}
func (f *fakePlaybackSessionStore) GetPlaybackSession(_ context.Context, id string) (ABSPlaybackSession, error) {
	s, ok := f.sessions[id]
	if !ok {
		return ABSPlaybackSession{}, ErrNotFound
	}
	return s, nil
}
func (f *fakePlaybackSessionStore) SyncPlaybackSession(context.Context, string, float64, int) error {
	return nil
}
func (f *fakePlaybackSessionStore) ClosePlaybackSession(context.Context, string) error { return nil }

func (f *fakePlaybackSessionStore) AggregateStats(_ context.Context, userID, profileID string) (Stats, error) {
	return Stats{Days: []DayStat{}, Monthly: []MonthStat{}}, nil
}

func (f *fakePlaybackSessionStore) ListClosedSessions(_ context.Context, userID, profileID string, limit, offset int) ([]ABSPlaybackSession, int, error) {
	return nil, 0, nil
}

// filesMediaStore returns a fixed slice of MediaFile entries for the
// configured contentID, satisfying the MediaStore interface for the
// public-track tests. Unconfigured methods inherit no-op behavior from
// noopMediaStore via embedding.
type filesMediaStore struct {
	noopMediaStore
	contentID string
	files     []*models.MediaFile
}

func (f *filesMediaStore) GetMediaFiles(_ context.Context, contentID string) ([]*models.MediaFile, error) {
	if contentID != f.contentID {
		return nil, nil
	}
	return f.files, nil
}

// makeTempAudio writes minimal bytes to a .mp3 file in t.TempDir() and
// returns the path. ServeDirectPlay only needs the file to exist and be
// readable; content correctness is not asserted by these tests.
func makeTempAudio(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "track.mp3")
	if err := os.WriteFile(p, []byte("\xff\xfb\x00\x00audio-bytes"), 0o644); err != nil {
		t.Fatalf("write temp audio: %v", err)
	}
	return p
}

// newPublicTrackHandler builds a Handler with the minimum deps to serve
// /public/session/{sid}/track/{idx}: a seeded session store + a media store
// holding ONE audio file for that session's contentID.
func newPublicTrackHandler(t *testing.T, sid, contentID string, closed bool) (*Handler, string) {
	t.Helper()
	audioPath := makeTempAudio(t)
	sessStore := &fakePlaybackSessionStore{}
	sess := ABSPlaybackSession{ID: sid, UserID: "u1", ContentID: contentID}
	if closed {
		now := time.Now()
		sess.ClosedAt = &now
	}
	_ = sessStore.InsertPlaybackSession(context.Background(), sess)

	mediaStore := &filesMediaStore{
		contentID: contentID,
		files:     []*models.MediaFile{{ID: 1, FilePath: audioPath}},
	}
	h := New(Dependencies{
		MediaStore:           mediaStore,
		PlaybackSessionStore: sessStore,
	})
	return h, audioPath
}

// dispatchTrack invokes handlePublicTrack with the URL params chi would
// normally inject from the route. Mirrors how chi.URLParam reads from the
// request context — without this the handler can't see {sid}/{idx}.
func dispatchTrack(h *Handler, method, sid, idx string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/public/session/"+sid+"/track/"+idx, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("sid", sid)
	rctx.URLParams.Add("idx", idx)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h.handlePublicTrack(rec, req)
	return rec
}

func TestHandlePublicTrack_ServesBytesForValidSession(t *testing.T) {
	h, _ := newPublicTrackHandler(t, "sid-1", "book-1", false)
	rec := dispatchTrack(h, http.MethodGet, "sid-1", "1")
	if rec.Code != http.StatusOK && rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() == 0 {
		t.Errorf("response body empty; expected audio bytes")
	}
	if got := rec.Header().Get("Content-Type"); got != "audio/mpeg" {
		t.Errorf("Content-Type = %q, want audio/mpeg", got)
	}
}

// TestHandlePublicTrack_HeadProbe covers the iOS/Android HEAD pre-flight
// some players issue before the GET. http.ServeContent returns headers
// without a body for HEAD; the handler must not 404.
func TestHandlePublicTrack_HeadProbe(t *testing.T) {
	h, _ := newPublicTrackHandler(t, "sid-1", "book-1", false)
	rec := dispatchTrack(h, http.MethodHead, "sid-1", "1")
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD response should have empty body; got %d bytes", rec.Body.Len())
	}
}

func TestHandlePublicTrack_UnknownSession404(t *testing.T) {
	h, _ := newPublicTrackHandler(t, "sid-1", "book-1", false)
	rec := dispatchTrack(h, http.MethodGet, "sid-does-not-exist", "1")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestHandlePublicTrack_ClosedSession410(t *testing.T) {
	h, _ := newPublicTrackHandler(t, "sid-1", "book-1", true)
	rec := dispatchTrack(h, http.MethodGet, "sid-1", "1")
	if rec.Code != http.StatusGone {
		t.Errorf("status = %d, want 410", rec.Code)
	}
}

func TestHandlePublicTrack_IndexOutOfRange404(t *testing.T) {
	h, _ := newPublicTrackHandler(t, "sid-1", "book-1", false)
	rec := dispatchTrack(h, http.MethodGet, "sid-1", "5")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestHandlePublicTrack_BadIndex400(t *testing.T) {
	h, _ := newPublicTrackHandler(t, "sid-1", "book-1", false)
	for _, bad := range []string{"0", "-1", "abc"} {
		rec := dispatchTrack(h, http.MethodGet, "sid-1", bad)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("idx=%q: status = %d, want 400", bad, rec.Code)
		}
	}
}
