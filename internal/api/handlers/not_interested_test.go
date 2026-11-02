package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// markItemRepo answers the items it holds; anything else is not found.
type markItemRepo struct {
	items map[string]*models.MediaItem
}

func (r markItemRepo) GetByID(_ context.Context, id string) (*models.MediaItem, error) {
	if item, ok := r.items[id]; ok {
		return item, nil
	}
	return nil, catalog.ErrItemNotFound
}

func (r markItemRepo) GetByIDs(_ context.Context, ids []string) ([]*models.MediaItem, error) {
	var out []*models.MediaItem
	for _, id := range ids {
		if item, ok := r.items[id]; ok {
			out = append(out, item)
		}
	}
	return out, nil
}

func (r markItemRepo) EnsureAccessible(_ context.Context, id string, _ catalog.AccessFilter) error {
	if _, ok := r.items[id]; ok {
		return nil
	}
	return catalog.ErrItemNotFound
}

func (r markItemRepo) EnsureAccessibleIDs(ctx context.Context, ids []string, filter catalog.AccessFilter) (map[string]bool, error) {
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = r.EnsureAccessible(ctx, id, filter) == nil
	}
	return out, nil
}

// failingItemRepo is a markItemRepo whose access check or item lookup fails
// with the given error instead of answering.
type failingItemRepo struct {
	markItemRepo
	accessErr, getErr error
}

func (r failingItemRepo) EnsureAccessible(ctx context.Context, id string, filter catalog.AccessFilter) error {
	if r.accessErr != nil {
		return r.accessErr
	}
	return r.markItemRepo.EnsureAccessible(ctx, id, filter)
}

func (r failingItemRepo) GetByID(ctx context.Context, id string) (*models.MediaItem, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	return r.markItemRepo.GetByID(ctx, id)
}

// Marking takes movies and series only, and tells recommendations once per
// change: a repeated mark or unmark changes nothing and notifies nobody.
func TestMarkNotInterested(t *testing.T) {
	store := newHouseholdTestStore(t)
	h := NewPersonalDataHandler(testUserStoreProvider{store: store}, markItemRepo{items: map[string]*models.MediaItem{
		"movie:heat":      {ContentID: "movie:heat", Type: "movie"},
		"series:show":     {ContentID: "series:show", Type: "series"},
		"season:show-1":   {ContentID: "season:show-1", Type: "season"},
		"audiobook:dune":  {ContentID: "audiobook:dune", Type: "audiobook"},
		"episode:show-11": {ContentID: "episode:show-11", Type: "episode"},
	}})
	notifier := &countingSignalsNotifier{}
	h.SetSignalsChangedNotifier(notifier)
	viewer := PersonalListViewer{UserID: 1, ProfileID: "p1"}
	ctx := context.Background()

	for _, id := range []string{"movie:heat", "series:show"} {
		for range 2 {
			if err := h.MarkNotInterested(ctx, viewer, id); err != nil {
				t.Fatalf("mark %s: %v", id, err)
			}
		}
	}
	if notifier.calls != 2 {
		t.Fatalf("notifications = %d, want one per new mark", notifier.calls)
	}
	for _, id := range []string{"season:show-1", "audiobook:dune", "episode:show-11"} {
		var apiErr *APIError
		if err := h.MarkNotInterested(ctx, viewer, id); !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnprocessableEntity {
			t.Fatalf("mark %s = %v, want 422", id, err)
		}
	}
	var apiErr *APIError
	if err := h.MarkNotInterested(ctx, viewer, "movie:missing"); !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Fatalf("mark of an unknown item = %v, want 404", err)
	}

	entry, found, err := h.GetNotInterested(ctx, viewer, "movie:heat")
	if err != nil || !found || entry.MediaItemID != "movie:heat" {
		t.Fatalf("get = %+v, %v, %v", entry, found, err)
	}
	// Another profile on the account has no marks.
	if _, found, _ := h.GetNotInterested(ctx, PersonalListViewer{UserID: 1, ProfileID: "p2"}, "movie:heat"); found {
		t.Fatal("another profile sees p1's mark")
	}

	for range 2 {
		if err := h.UnmarkNotInterested(ctx, viewer, "movie:heat"); err != nil {
			t.Fatalf("unmark: %v", err)
		}
	}
	if notifier.calls != 3 {
		t.Fatalf("notifications = %d after unmark, want 3", notifier.calls)
	}
}

// A catalog query that fails is a server error, not a missing item: a client
// told that an item it can see is gone drops it. The not-found answer stays a
// 404 (TestMarkNotInterested), as does an ID Postgres cannot take as text,
// which names no item and is never queried.
func TestPersonalListLookupFailureIsServerError(t *testing.T) {
	failure := fmt.Errorf("checking item access: %w", context.DeadlineExceeded)
	items := markItemRepo{items: map[string]*models.MediaItem{"movie:heat": {ContentID: "movie:heat", Type: "movie"}}}
	viewer := PersonalListViewer{UserID: 1, ProfileID: "p1"}
	ctx := context.Background()
	getFavorite := func(id string) func(*PersonalDataHandler) error {
		return func(h *PersonalDataHandler) error {
			_, _, err := h.GetFavorite(ctx, viewer, id)
			return err
		}
	}
	for _, tc := range []struct {
		name string
		repo failingItemRepo
		call func(*PersonalDataHandler) error
		want int
	}{
		{"access check", failingItemRepo{markItemRepo: items, accessErr: failure}, getFavorite("movie:heat"), http.StatusInternalServerError},
		{"item lookup", failingItemRepo{markItemRepo: items, getErr: failure}, func(h *PersonalDataHandler) error {
			return h.MarkNotInterested(ctx, viewer, "movie:heat")
		}, http.StatusInternalServerError},
		{"malformed id", failingItemRepo{markItemRepo: items, accessErr: failure}, getFavorite("movie:\x00heat"), http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewPersonalDataHandler(testUserStoreProvider{store: newHouseholdTestStore(t)}, tc.repo)
			var apiErr *APIError
			if err := tc.call(h); !errors.As(err, &apiErr) || apiErr.Status != tc.want {
				t.Fatalf("err = %v, want status %d", err, tc.want)
			}
		})
	}
}
