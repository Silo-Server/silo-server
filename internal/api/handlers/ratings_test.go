package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// memoryRatings is a ratingsRepository over a slice kept newest first.
type memoryRatings struct {
	rows []catalog.UserRating
}

func (m *memoryRatings) Set(context.Context, int, string, string, int) error { return nil }

func (m *memoryRatings) Get(_ context.Context, userID int, profileID, itemID string) (*catalog.UserRating, error) {
	for _, r := range m.rows {
		if r.UserID == userID && r.ProfileID == profileID && r.MediaItemID == itemID {
			return &r, nil
		}
	}
	return nil, nil
}

func (m *memoryRatings) Delete(context.Context, int, string, string) error { return nil }

func (m *memoryRatings) List(_ context.Context, _ int, _ string, limit, offset int) ([]catalog.UserRating, error) {
	return append([]catalog.UserRating(nil), m.rows[min(offset, len(m.rows)):min(offset+limit, len(m.rows))]...), nil
}

func (m *memoryRatings) ListPage(_ context.Context, _ int, _ string, after *catalog.RatingKey, limit int) ([]catalog.UserRating, error) {
	start := 0
	if after != nil {
		for start < len(m.rows) && (m.rows[start].RatedAt.After(after.RatedAt) ||
			(m.rows[start].RatedAt.Equal(after.RatedAt) && m.rows[start].MediaItemID >= after.MediaItemID)) {
			start++
		}
	}
	return append([]catalog.UserRating(nil), m.rows[start:min(start+limit, len(m.rows))]...), nil
}

// idsFailingItemRepo is a markItemRepo whose batch access check fails.
type idsFailingItemRepo struct {
	markItemRepo
}

func (idsFailingItemRepo) EnsureAccessibleIDs(context.Context, []string, catalog.AccessFilter) (map[string]bool, error) {
	return nil, context.DeadlineExceeded
}

// A restricted profile reads only the ratings of items it may see: a hidden
// item answers 404 whether or not it was rated, and the lists leave it out.
func TestRatingsFollowViewerAccess(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	repo := &memoryRatings{rows: []catalog.UserRating{
		{UserID: 1, ProfileID: "p1", MediaItemID: "movie:visible", Rating: 4, RatedAt: now},
		{UserID: 1, ProfileID: "p1", MediaItemID: "series:hidden", Rating: 5, RatedAt: now.Add(-time.Hour)},
		{UserID: 1, ProfileID: "p1", MediaItemID: "movie:also-visible", Rating: 2, RatedAt: now.Add(-2 * time.Hour)},
	}}
	// The item repository holds only what the viewer may see.
	items := markItemRepo{items: map[string]*models.MediaItem{
		"movie:visible":      {ContentID: "movie:visible", Type: "movie"},
		"movie:also-visible": {ContentID: "movie:also-visible", Type: "movie"},
		"movie:unrated":      {ContentID: "movie:unrated", Type: "movie"},
	}}
	h := NewRatingsHandler(repo, items)
	ctx := context.Background()
	access := catalog.AccessFilter{UserID: 1, ProfileID: "p1"}

	if r, found, err := h.GetRating(ctx, 1, "p1", "movie:visible", access); err != nil || !found || r.Rating != 4 {
		t.Fatalf("visible = %+v, %v, %v", r, found, err)
	}
	if _, found, err := h.GetRating(ctx, 1, "p1", "movie:unrated", access); err != nil || found {
		t.Fatalf("unrated visible item = %v, %v, want not found without error", found, err)
	}
	for _, id := range []string{"series:hidden", "series:hidden-unrated", "bad\x00id"} {
		var apiErr *APIError
		if _, _, err := h.GetRating(ctx, 1, "p1", id, access); !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
			t.Fatalf("get %q = %v, want 404", id, err)
		}
	}

	list, err := h.ListRatings(ctx, 1, "p1", access, 50, 0)
	if err != nil || len(list) != 2 || list[0].MediaItemID != "movie:visible" || list[1].MediaItemID != "movie:also-visible" {
		t.Fatalf("list = %+v, %v", list, err)
	}

	page, err := h.ListRatingsPage(ctx, 1, "p1", access, nil, 50)
	if err != nil || len(page) != 2 || page[0].MediaItemID != "movie:visible" || page[1].MediaItemID != "movie:also-visible" {
		t.Fatalf("page = %+v, %v", page, err)
	}
	var listErr *APIError
	if _, err := NewRatingsHandler(repo, idsFailingItemRepo{items}).ListRatings(ctx, 1, "p1", access, 50, 0); !errors.As(err, &listErr) || listErr.Status != http.StatusInternalServerError {
		t.Fatalf("failed list access check = %v, want 500", err)
	}

	// A failed access check is a server error, not a missing rating.
	failing := NewRatingsHandler(repo, failingItemRepo{markItemRepo: items, accessErr: fmt.Errorf("checking item access: %w", context.DeadlineExceeded)})
	var apiErr *APIError
	if _, _, err := failing.GetRating(ctx, 1, "p1", "movie:visible", access); !errors.As(err, &apiErr) || apiErr.Status != http.StatusInternalServerError {
		t.Fatalf("failed access check = %v, want 500", err)
	}
}

// The keyset page reads past runs of hidden ratings longer than one store
// batch, so it answers only visible rows, a full page whenever enough remain,
// and never a hidden row a caller could build its cursor from.
func TestListRatingsPageReadsPastHiddenRows(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	repo := &memoryRatings{}
	items := markItemRepo{items: map[string]*models.MediaItem{}}
	// Newest first: 2 visible, 250 hidden, then 3 visible.
	for i := range 255 {
		id := fmt.Sprintf("movie:%03d", i)
		repo.rows = append(repo.rows, catalog.UserRating{UserID: 1, ProfileID: "p1", MediaItemID: id, Rating: 3, RatedAt: now.Add(-time.Duration(i) * time.Minute)})
		if i < 2 || i >= 252 {
			items.items[id] = &models.MediaItem{ContentID: id, Type: "movie"}
		}
	}
	h := NewRatingsHandler(repo, items)
	ctx := context.Background()
	access := catalog.AccessFilter{UserID: 1, ProfileID: "p1"}

	page, err := h.ListRatingsPage(ctx, 1, "p1", access, nil, 4)
	if err != nil || len(page) != 4 || page[1].MediaItemID != "movie:001" || page[2].MediaItemID != "movie:252" || page[3].MediaItemID != "movie:253" {
		t.Fatalf("page = %+v, %v", page, err)
	}
	last := page[3]
	page, err = h.ListRatingsPage(ctx, 1, "p1", access, &catalog.RatingKey{RatedAt: last.RatedAt, MediaItemID: last.MediaItemID}, 4)
	if err != nil || len(page) != 1 || page[0].MediaItemID != "movie:254" {
		t.Fatalf("next page = %+v, %v", page, err)
	}
}
