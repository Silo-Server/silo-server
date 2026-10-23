package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/recommendations"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type failingStoreProvider struct{}

func (failingStoreProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return nil, errors.New("user store unavailable")
}

func (failingStoreProvider) Close() error { return nil }

// A user store failure leaves the list unfiltered instead of answering the
// watched set from Postgres progress, which a SQLite user store does not
// keep. The repo has no pool, so a fallback read would panic.
func TestWatchedFilterDoesNotFallBackToPostgres(t *testing.T) {
	h := NewRecommendationsHandler(nil, nil, failingStoreProvider{}, nil, recommendations.NewRepo(nil), false)
	items := []recommendations.ScoredItem{{MediaItemID: "a"}, {MediaItemID: "b"}}
	got := h.excludeWatchedRecommendations(context.Background(), 7, "profile-1", items)
	if len(got) != 2 {
		t.Fatalf("items = %v, want both left in", got)
	}
	if _, err := h.watchedItemIDSet(context.Background(), 7, "profile-1"); err == nil {
		t.Fatal("watched set error was swallowed")
	}
}
