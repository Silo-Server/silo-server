package abs

import (
	"context"
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

func TestStats_Session_Detail_NonOwner_404(t *testing.T) {
	fake := &statsFakeStore{}
	_ = fake.InsertPlaybackSession(context.Background(), ABSPlaybackSession{ID: "s1", UserID: "1", ContentID: "book-1"})
	h := New(Dependencies{MediaStore: noopMediaStore{}, PlaybackSessionStore: fake})

	rec := dispatchABSWithParams(http.MethodGet, "/api/me/listening-sessions/s1", map[string]string{"sid": "s1"}, nil, "2", "", h.handleListeningSessionDetail)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
