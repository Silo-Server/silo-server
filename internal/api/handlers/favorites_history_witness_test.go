package handlers

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestHistoryPageCardsExcludeEarlierPageWitness(t *testing.T) {
	ctx := t.Context()
	store := newProfileTestStore(t)
	repo := &fakeHistoryItemRepo{items: map[string]*models.MediaItem{"movie": {ContentID: "movie", Type: "movie", Title: "Movie"}}}
	h := NewPersonalDataHandler(testUserStoreProvider{store: store}, repo)
	newer := userstore.WatchHistoryEntry{ID: "new", ProfileID: "profile-1", MediaItemID: "movie", WatchedAt: "2026-01-02T00:00:00Z"}
	older := userstore.WatchHistoryEntry{ID: "old", ProfileID: "profile-1", MediaItemID: "movie", WatchedAt: "2026-01-01T00:00:00Z"}
	for _, e := range []userstore.WatchHistoryEntry{newer, older} {
		if err := store.AddHistory(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	viewer := SectionViewer{Access: catalog.AccessFilter{UserID: 1, ProfileID: "profile-1"}}
	cards, err := h.HistoryPageCards(ctx, viewer, []userstore.WatchHistoryEntry{older})
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 0 {
		t.Fatalf("older repeat was rendered: %+v", cards)
	}
	// The v1 renderer retains its existing per-page behavior.
	cards, err = h.HistoryCards(ctx, viewer, []userstore.WatchHistoryEntry{older})
	if err != nil || len(cards) != 1 {
		t.Fatalf("v1 cards = %+v, error %v", cards, err)
	}
}
