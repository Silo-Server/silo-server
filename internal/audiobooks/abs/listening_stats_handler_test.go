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

// A session belongs to one profile: another account, or another profile on
// the same account, gets the same 404 as an unknown session.
func TestStats_Session_Detail_NonOwner_404(t *testing.T) {
	fake := &statsFakeStore{}
	_ = fake.InsertPlaybackSession(context.Background(), ABSPlaybackSession{ID: "s1", UserID: "1", ProfileID: "parent", ContentID: "book-1"})
	h := New(Dependencies{MediaStore: noopMediaStore{}, PlaybackSessionStore: fake})

	for _, caller := range []struct{ name, userID, profileID string }{
		{"other account", "2", "parent"},
		{"other profile on the account", "1", "kid"},
	} {
		t.Run(caller.name, func(t *testing.T) {
			rec := dispatchABSWithParams(http.MethodGet, "/api/me/listening-sessions/s1", map[string]string{"sid": "s1"}, nil, caller.userID, caller.profileID, h.handleListeningSessionDetail)
			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}
