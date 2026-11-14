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

// ListPage is unused here: the access filter is applied in its SQL, which
// the catalog database tests cover.
func (m *memoryRatings) ListPage(context.Context, int, string, catalog.AccessFilter, *catalog.RatingKey, int) ([]catalog.UserRating, error) {
	return nil, nil
}

// idsFailingItemRepo is a markItemRepo whose batch access check fails.
type idsFailingItemRepo struct {
	markItemRepo
}

func (idsFailingItemRepo) EnsureAccessibleIDs(context.Context, []string, catalog.AccessFilter) (map[string]bool, error) {
	return nil, context.DeadlineExceeded
}

// A restricted profile reads only the ratings of items it may see: a hidden
// item answers 404 whether or not it was rated. The list applies the same
// filter in its page query, which the catalog database tests cover.
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

	// A failed access check is a server error, not a missing rating.
	failing := NewRatingsHandler(repo, failingItemRepo{markItemRepo: items, accessErr: fmt.Errorf("checking item access: %w", context.DeadlineExceeded)})
	var apiErr *APIError
	if _, _, err := failing.GetRating(ctx, 1, "p1", "movie:visible", access); !errors.As(err, &apiErr) || apiErr.Status != http.StatusInternalServerError {
		t.Fatalf("failed access check = %v, want 500", err)
	}
}
