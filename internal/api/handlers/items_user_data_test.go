package handlers

import (
	"context"
	"errors"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestGetLeafUserDataUsesEbookReaderProgress(t *testing.T) {
	handler := &ItemsHandler{
		ebookProgressStore: &fakeEbookReaderProgressLister{
			progress: map[string]EbookReaderProgress{
				"ebook-progress": {
					UserID:    7,
					ProfileID: "profile-1",
					ContentID: "ebook-progress",
					Progress:  0.42,
					UpdatedAt: time.Date(2026, 6, 5, 10, 0, 0, 0, time.UTC),
				},
				"ebook-complete": {
					UserID:    7,
					ProfileID: "profile-1",
					ContentID: "ebook-complete",
					Progress:  0.93,
					UpdatedAt: time.Date(2026, 6, 5, 11, 0, 0, 0, time.UTC),
				},
			},
		},
	}
	req := httptest.NewRequest("GET", "/items/ebook-progress", nil)
	ctx := apimw.SetClaims(req.Context(), &auth.Claims{UserID: 7})
	ctx = apimw.SetProfileID(ctx, "profile-1")
	req = req.WithContext(ctx)

	progress := handler.getLeafUserData(req, "ebook-progress", "ebook")
	if progress == nil || progress.Played || !progress.IsInProgress || progress.PositionSeconds != 0.42 || progress.DurationSeconds != 1 {
		t.Fatalf("partial ebook user data = %#v", progress)
	}

	complete := handler.getLeafUserData(req, "ebook-complete", "ebook")
	if complete == nil || !complete.Played || complete.IsInProgress {
		t.Fatalf("completed ebook user data = %#v", complete)
	}
}

// Audiobooks resolve user_data through the same watch-progress store as
// movies/episodes (not the ebook reader store), so an in-progress audiobook
// reports its saved position_seconds — which is what lets the player resume.
func TestGetLeafUserDataReturnsAudiobookProgress(t *testing.T) {
	store := newPlaybackTestStore(t)
	if err := store.SetProgress(context.Background(), "profile-1", "audiobook-1", 1234, 5000, userstore.ProgressThresholds{}); err != nil {
		t.Fatalf("seed progress: %v", err)
	}
	handler := &ItemsHandler{storeProvider: testUserStoreProvider{store: store}}

	req := httptest.NewRequest("GET", "/watch/audiobook-1", nil)
	ctx := apimw.SetClaims(req.Context(), &auth.Claims{UserID: 1})
	ctx = apimw.SetProfileID(ctx, "profile-1")
	req = req.WithContext(ctx)

	progress := handler.getLeafUserData(req, "audiobook-1", "audiobook")
	if progress == nil {
		t.Fatal("audiobook user data = nil, want saved progress")
	}
	if progress.PositionSeconds != 1234 {
		t.Fatalf("PositionSeconds = %v, want 1234", progress.PositionSeconds)
	}
}

func TestGetAggregateUserDataReturnsNilWhenProgressBatchFails(t *testing.T) {
	store := &failingBatchProgressStore{}
	handler := &ItemsHandler{storeProvider: testUserStoreProvider{store: store}}

	req := httptest.NewRequest("GET", "/series/series-1/seasons/1", nil)
	ctx := apimw.SetClaims(req.Context(), &auth.Claims{UserID: 1})
	ctx = apimw.SetProfileID(ctx, "profile-1")
	req = req.WithContext(ctx)

	episodes := make([]*models.Episode, 501)
	for i := range episodes {
		episodes[i] = &models.Episode{ContentID: "episode-" + strconv.Itoa(i+1)}
	}

	if userData := handler.getAggregateUserData(req, episodes); userData != nil {
		t.Fatalf("getAggregateUserData() = %#v, want nil after batch failure", userData)
	}
	if store.calls != 2 {
		t.Fatalf("ListProgressByMediaItems calls = %d, want 2", store.calls)
	}
}

type failingBatchProgressStore struct {
	userstore.UserStore
	calls int
}

func (s *failingBatchProgressStore) ListProgressByMediaItems(
	_ context.Context,
	_ string,
	mediaItemIDs []string,
) (map[string]userstore.WatchProgress, error) {
	s.calls++
	if s.calls == 2 {
		return nil, errors.New("progress batch failed")
	}
	progress := make(map[string]userstore.WatchProgress, len(mediaItemIDs))
	for _, id := range mediaItemIDs {
		progress[id] = userstore.WatchProgress{MediaItemID: id, Completed: true}
	}
	return progress, nil
}
