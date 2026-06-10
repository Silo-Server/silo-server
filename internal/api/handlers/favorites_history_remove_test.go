package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type fakeHistoryItemRepo struct {
	items        map[string]*models.MediaItem
	inaccessible map[string]bool
}

func (r *fakeHistoryItemRepo) GetByID(_ context.Context, contentID string) (*models.MediaItem, error) {
	item, ok := r.items[contentID]
	if !ok {
		return nil, catalog.ErrItemNotFound
	}
	return item, nil
}

func (r *fakeHistoryItemRepo) GetByIDs(_ context.Context, contentIDs []string) ([]*models.MediaItem, error) {
	result := make([]*models.MediaItem, 0, len(contentIDs))
	for _, contentID := range contentIDs {
		if item, ok := r.items[contentID]; ok {
			result = append(result, item)
		}
	}
	return result, nil
}

func (r *fakeHistoryItemRepo) EnsureAccessible(_ context.Context, contentID string, _ catalog.AccessFilter) error {
	if r.inaccessible[contentID] {
		return catalog.ErrItemNotFound
	}
	return nil
}

func TestHandleRemoveHistoryAcceptsEbookTargets(t *testing.T) {
	ctx := context.Background()
	store := newProfileTestStore(t)
	handler := NewPersonalDataHandler(testUserStoreProvider{store: store}, &fakeHistoryItemRepo{
		items: map[string]*models.MediaItem{
			"ebook-1": {ContentID: "ebook-1", Type: "ebook", Title: "Book"},
		},
	})

	// Seed a history entry keyed by the ebook content ID so the removal has
	// something to hide. RemoveHistoryItems only touches watch history,
	// watch progress, and the hidden-items gate — PersonalDataHandler has no
	// write access to ebook_reader_progress (its store interface is read-only),
	// so the reader position survives: hidden is not the same as unread.
	if err := store.AddHistory(ctx, userstore.WatchHistoryEntry{
		ProfileID:   "profile-1",
		MediaItemID: "ebook-1",
		WatchedAt:   "2026-06-01T10:00:00Z",
		Completed:   true,
		Source:      userstore.WatchHistorySourceManual,
	}); err != nil {
		t.Fatalf("AddHistory: %v", err)
	}

	req := newAuthorizedProfileRequestWithRole(
		http.MethodPost,
		"/history/remove",
		`{"targets":[{"content_id":"ebook-1"}]}`,
		"user",
		"profile-1",
	)
	rr := httptest.NewRecorder()
	handler.HandleRemoveHistory(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	entries, err := store.ListHistory(ctx, "profile-1", 10, 0)
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("history entries = %+v, want hidden after removal", entries)
	}
}

func TestHandleRemoveHistoryRejectsInaccessibleEbook(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewPersonalDataHandler(testUserStoreProvider{store: store}, &fakeHistoryItemRepo{
		items: map[string]*models.MediaItem{
			"ebook-1": {ContentID: "ebook-1", Type: "ebook", Title: "Book"},
		},
		inaccessible: map[string]bool{"ebook-1": true},
	})

	req := newAuthorizedProfileRequestWithRole(
		http.MethodPost,
		"/history/remove",
		`{"targets":[{"content_id":"ebook-1"}]}`,
		"user",
		"profile-1",
	)
	rr := httptest.NewRecorder()
	handler.HandleRemoveHistory(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
}
