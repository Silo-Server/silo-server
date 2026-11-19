package apiv2

import (
	"context"
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// fakeNotInterested is NotInterestedService over in-memory marks. Episodes,
// whose IDs start with "episode:", are refused as the handler refuses them.
type fakeNotInterested struct {
	marks   []userstore.NotInterestedEntry
	missing map[string]bool
	viewers []handlers.PersonalListViewer
	limits  []int
}

func notInterestedKey(e userstore.NotInterestedEntry) userstore.ListKey {
	return userstore.ListKey{AddedAt: e.AddedAt, MediaItemID: e.MediaItemID}
}

func (f *fakeNotInterested) ListNotInterestedPage(_ context.Context, viewer handlers.PersonalListViewer, after *userstore.ListKey, limit int) ([]userstore.NotInterestedEntry, []handlers.CollectionItemView, error) {
	f.viewers = append(f.viewers, viewer)
	f.limits = append(f.limits, limit)
	page := keysetPage(f.marks, viewer.ProfileID, notInterestedKey,
		func(e userstore.NotInterestedEntry) string { return e.ProfileID }, after, limit)
	cards := make([]handlers.CollectionItemView, 0, len(page))
	for _, e := range page {
		if !f.missing[e.MediaItemID] {
			cards = append(cards, handlers.CollectionItemView{ContentID: e.MediaItemID, Type: "movie", Title: "Title " + e.MediaItemID, Status: "matched"})
		}
	}
	return page, cards, nil
}

func (f *fakeNotInterested) GetNotInterested(_ context.Context, viewer handlers.PersonalListViewer, itemID string) (userstore.NotInterestedEntry, bool, error) {
	if f.missing[itemID] {
		return userstore.NotInterestedEntry{}, false, &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Item not found"}
	}
	for _, e := range f.marks {
		if e.ProfileID == viewer.ProfileID && e.MediaItemID == itemID {
			return e, true, nil
		}
	}
	return userstore.NotInterestedEntry{}, false, nil
}

func (f *fakeNotInterested) MarkNotInterested(_ context.Context, viewer handlers.PersonalListViewer, itemID string) error {
	if f.missing[itemID] {
		return &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Item not found"}
	}
	if strings.HasPrefix(itemID, "episode:") {
		return &handlers.APIError{Status: http.StatusUnprocessableEntity, Code: "validation_failed", Message: "Only a movie or series can be marked Not interested"}
	}
	for _, e := range f.marks {
		if e.ProfileID == viewer.ProfileID && e.MediaItemID == itemID {
			return nil
		}
	}
	f.marks = append([]userstore.NotInterestedEntry{{ProfileID: viewer.ProfileID, MediaItemID: itemID, AddedAt: "2026-01-03T00:00:00Z"}}, f.marks...)
	return nil
}

func (f *fakeNotInterested) UnmarkNotInterested(_ context.Context, viewer handlers.PersonalListViewer, itemID string) error {
	kept := f.marks[:0]
	for _, e := range f.marks {
		if e.ProfileID != viewer.ProfileID || e.MediaItemID != itemID {
			kept = append(kept, e)
		}
	}
	f.marks = kept
	return nil
}

func notInterestedRows() []userstore.NotInterestedEntry {
	return []userstore.NotInterestedEntry{
		{ProfileID: "p-owner", MediaItemID: "movie:c", AddedAt: "2026-01-02T03:04:05Z"},
		{ProfileID: "p-owner", MediaItemID: "series:b", AddedAt: "2026-01-01T00:00:00Z"},
		{ProfileID: "p-owner", MediaItemID: "movie:a", AddedAt: "2026-01-01T00:00:00Z"},
		{ProfileID: "p-other", MediaItemID: "movie:z", AddedAt: "2025-12-30T00:00:00Z"},
	}
}
