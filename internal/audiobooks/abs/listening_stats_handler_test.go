package abs

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

type statsFakeStore struct {
	fakePlaybackSessionStore
	stats  Stats
	closed []ABSPlaybackSession
}

func (f *statsFakeStore) AggregateStats(_ context.Context, _, _ string) (Stats, error) {
	return f.stats, nil
}

func (f *statsFakeStore) ListClosedSessions(_ context.Context, _, _ string, limit, offset int) ([]ABSPlaybackSession, int, error) {
	total := len(f.closed)
	if offset >= total {
		return nil, total, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return f.closed[offset:end], total, nil
}

func TestStats_Aggregate_Ok(t *testing.T) {
	fake := &statsFakeStore{
		stats: Stats{TotalTime: 3600, Items: 4, DayOfWeek: [7]int{0, 1800, 0, 1800, 0, 0, 0}},
	}
	h := New(Dependencies{MediaStore: noopMediaStore{}, PlaybackSessionStore: fake})

	rec := dispatchABSWithParams(http.MethodGet, "/api/me/listening-stats", nil, nil, "1", "", h.handleListeningStats)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["totalTime"] != float64(3600) {
		t.Errorf("totalTime = %v, want 3600", got["totalTime"])
	}
	if got["items"] != float64(4) {
		t.Errorf("items = %v, want 4", got["items"])
	}
	dow, _ := got["dayOfWeek"].(map[string]any)
	if dow["1"] != float64(1800) {
		t.Errorf("dayOfWeek[1] = %v, want 1800", dow["1"])
	}
}

func TestStats_Sessions_List_Paginated(t *testing.T) {
	fake := &statsFakeStore{closed: []ABSPlaybackSession{
		{ID: "s1", UserID: "1", ContentID: "book-1"},
		{ID: "s2", UserID: "1", ContentID: "book-2"},
		{ID: "s3", UserID: "1", ContentID: "book-3"},
	}}
	h := New(Dependencies{MediaStore: noopMediaStore{}, PlaybackSessionStore: fake})

	rec := dispatchABSWithParams(http.MethodGet, "/api/me/listening-sessions", nil, nil, "1", "", h.handleListeningSessions)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var env map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env["total"] != float64(3) {
		t.Errorf("total = %v, want 3", env["total"])
	}
	sessions, _ := env["sessions"].([]any)
	if len(sessions) != 3 {
		t.Errorf("sessions len = %d, want 3", len(sessions))
	}
}

// TestStats_Sessions_List_EnvelopeShape asserts the response matches the
// exact envelope real audiobookshelf's MeController.getListeningSessions
// returns ({total, numPages, page, itemsPerPage, sessions}), and that each
// session object carries the PlaybackSession.toJSON() keys strict clients
// (Flutter/Swift decoders) require: mediaType, mediaMetadata, displayTitle.
func TestStats_Sessions_List_EnvelopeShape(t *testing.T) {
	fake := &statsFakeStore{closed: []ABSPlaybackSession{
		{ID: "s1", UserID: "1", ContentID: "book-1", TimeListeningSeconds: 120, CurrentPositionSeconds: 45.5},
	}}
	h := New(Dependencies{MediaStore: noopMediaStore{}, PlaybackSessionStore: fake})

	rec := dispatchABSWithParams(http.MethodGet, "/api/me/listening-sessions", nil, nil, "1", "", h.handleListeningSessions)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var env map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	for _, key := range []string{"total", "numPages", "page", "itemsPerPage", "sessions"} {
		if _, ok := env[key]; !ok {
			t.Errorf("envelope missing key %q; body=%s", key, rec.Body.String())
		}
	}
	if _, ok := env["results"]; ok {
		t.Errorf("envelope must not carry the pagedEnvelope 'results' key")
	}

	sessions, _ := env["sessions"].([]any)
	if len(sessions) != 1 {
		t.Fatalf("sessions len = %d, want 1", len(sessions))
	}
	sess, _ := sessions[0].(map[string]any)
	if sess["mediaType"] != "book" {
		t.Errorf("mediaType = %v, want book", sess["mediaType"])
	}
	if _, ok := sess["mediaMetadata"].(map[string]any); !ok {
		t.Errorf("mediaMetadata missing or wrong type: %v", sess["mediaMetadata"])
	}
	if _, ok := sess["displayTitle"]; !ok {
		t.Errorf("displayTitle missing")
	}
	for _, key := range []string{"id", "userId", "libraryId", "libraryItemId", "bookId", "episodeId",
		"chapters", "displayAuthor", "coverPath", "duration", "playMethod", "mediaPlayer",
		"deviceInfo", "serverVersion", "date", "dayOfWeek", "timeListening", "startTime",
		"currentTime", "startedAt", "updatedAt"} {
		if _, ok := sess[key]; !ok {
			t.Errorf("session missing key %q; body=%s", key, rec.Body.String())
		}
	}
}

func TestStats_Session_Detail_Owner(t *testing.T) {
	fake := &statsFakeStore{}
	_ = fake.InsertPlaybackSession(context.Background(), ABSPlaybackSession{ID: "s1", UserID: "1", ContentID: "book-1"})
	h := New(Dependencies{MediaStore: noopMediaStore{}, PlaybackSessionStore: fake})

	rec := dispatchABSWithParams(http.MethodGet, "/api/me/listening-sessions/s1", map[string]string{"sid": "s1"}, nil, "1", "", h.handleListeningSessionDetail)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["id"] != "s1" {
		t.Errorf("id = %v", got["id"])
	}
}

func TestStats_Session_Detail_NonOwner_404(t *testing.T) {
	fake := &statsFakeStore{}
	_ = fake.InsertPlaybackSession(context.Background(), ABSPlaybackSession{ID: "s1", UserID: "1", ContentID: "book-1"})
	h := New(Dependencies{MediaStore: noopMediaStore{}, PlaybackSessionStore: fake})

	rec := dispatchABSWithParams(http.MethodGet, "/api/me/listening-sessions/s1", map[string]string{"sid": "s1"}, nil, "2", "", h.handleListeningSessionDetail)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
