package handlers

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type collectionGuardReader struct {
	items   []*models.MediaItem
	seasons []catalog.VisibleSeasonMember
	filter  catalog.AccessFilter
	err     error
}

func (r *collectionGuardReader) GetVisibleSeasonsWithAccess(_ context.Context, _ []string, filter catalog.AccessFilter) ([]catalog.VisibleSeasonMember, error) {
	r.filter = filter
	return r.seasons, r.err
}

func (r *collectionGuardReader) GetByIDsWithAccess(_ context.Context, _ []string, filter catalog.AccessFilter) ([]*models.MediaItem, error) {
	r.filter = filter
	return r.items, r.err
}

func TestPersonalCollectionManualMutationGuards(t *testing.T) {
	for _, kind := range []string{"manual", "smart", "mdblist", "trakt", ""} {
		for _, visible := range []bool{true, false} {
			t.Run(kind+"/"+map[bool]string{true: "visible", false: "missing-or-hidden"}[visible], func(t *testing.T) {
				store := &lifecycleStore{collection: userstore.Collection{ID: "c", CreatorProfileID: "owner", AllowedProfileIDs: []string{"owner"}, CollectionType: kind}}
				reader := &collectionGuardReader{}
				if visible {
					reader.items = []*models.MediaItem{{ContentID: "item"}}
				}
				h := NewCollectionHandler(lifecycleProvider{store: store})
				h.ItemReader = reader
				scope := access.Scope{AllowedLibraryIDs: []int{7}, DisabledLibraryIDs: []int{9}, MaturityLimits: access.MaturityLimits{MaxContentRating: "PG"}}
				ctx := access.SetScope(t.Context(), scope)
				err := h.AddPersonalCollectionItem(ctx, 1, "owner", "c", "item", 0)
				allowed := kind == "manual" && visible
				if (err == nil) != allowed {
					t.Fatalf("add: %v", err)
				}
				if !allowed && store.mutations != 0 {
					t.Fatal("denied add mutated membership")
				}
				if kind == "manual" {
					if !reflect.DeepEqual(reader.filter.AllowedLibraryIDs, scope.AllowedLibraryIDs) || !reflect.DeepEqual(reader.filter.DisabledLibraryIDs, scope.DisabledLibraryIDs) || reader.filter.MaxContentRating != "PG" {
						t.Fatalf("viewer scope lost: %+v", reader.filter)
					}
				}
				if kind != "manual" {
					for _, operation := range []func() error{func() error { return h.RemovePersonalCollectionItem(ctx, 1, "owner", "c", "item") }, func() error { return h.ReorderPersonalCollectionItems(ctx, 1, "owner", "c", []string{"item"}) }} {
						err := operation()
						e, ok := errors.AsType[*APIError](err)
						if !ok || e.Status != 409 {
							t.Fatalf("nonmanual mutation: %v", err)
						}
					}
					if store.mutations != 0 {
						t.Fatal("nonmanual mutation reached store")
					}
				}
			})
		}
	}
}

func seasonGuardStore(sqlState bool) *lifecycleStore {
	return &lifecycleStore{sqlState: sqlState, collection: userstore.Collection{ID: "c", CreatorProfileID: "owner", AllowedProfileIDs: []string{"owner"}, CollectionType: "manual"}}
}

func TestPersonalCollectionAddAcceptsVisibleSeasonOnPostgres(t *testing.T) {
	store := seasonGuardStore(true)
	h := NewCollectionHandler(lifecycleProvider{store: store})
	h.ItemReader = &collectionGuardReader{seasons: []catalog.VisibleSeasonMember{{SeasonID: "season-2", SeriesID: "series-1", SeriesTitle: "Alpha", SeasonNumber: 2}}}
	if err := h.AddPersonalCollectionItem(t.Context(), 1, "owner", "c", "season-2", 0); err != nil {
		t.Fatalf("add season: %v", err)
	}
	if store.mutations != 1 {
		t.Fatalf("mutations = %d, want 1", store.mutations)
	}
}

func TestPersonalCollectionAddRefusesSeasonOnSQLite(t *testing.T) {
	store := seasonGuardStore(false)
	h := NewCollectionHandler(lifecycleProvider{store: store})
	h.ItemReader = &collectionGuardReader{seasons: []catalog.VisibleSeasonMember{{SeasonID: "season-2", SeriesID: "series-1"}}}
	err := h.AddPersonalCollectionItem(t.Context(), 1, "owner", "c", "season-2", 0)
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.Status != http.StatusNotImplemented || apiErr.Code != "capability_unsupported" {
		t.Fatalf("err = %v, want 501 capability_unsupported", err)
	}
	if store.mutations != 0 {
		t.Fatal("refused season add mutated membership")
	}
}

func TestPersonalCollectionAddRefusesHiddenOrUnknownSeason(t *testing.T) {
	store := seasonGuardStore(true)
	h := NewCollectionHandler(lifecycleProvider{store: store})
	h.ItemReader = &collectionGuardReader{}
	err := h.AddPersonalCollectionItem(t.Context(), 1, "owner", "c", "series-1-S2", 0)
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.Status != http.StatusNotFound {
		t.Fatalf("err = %v, want 404", err)
	}
	if store.mutations != 0 {
		t.Fatal("refused add mutated membership")
	}
}
