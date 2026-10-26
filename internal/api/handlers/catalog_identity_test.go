package handlers

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

type rematchedIdentityItems struct{ providerOnly bool }

func (rematchedIdentityItems) ResolveIdentities(context.Context, catalog.IdentityRequest, catalog.AccessFilter) (catalog.IdentityResult, error) {
	return catalog.IdentityResult{Matches: []catalog.IdentityMatch{{Seed: 0, ContentID: "native-rematched"}}}, nil
}
func (f rematchedIdentityItems) GetByIDsWithAccess(context.Context, []string, catalog.AccessFilter) ([]*models.MediaItem, error) {
	id := "999"
	if f.providerOnly {
		id = ""
	}
	return []*models.MediaItem{{ContentID: "native-rematched", Type: "movie", TmdbID: id, Title: "Synthetic card"}}, nil
}
func (f rematchedIdentityItems) GetIdentityProviderIDs(context.Context, []string) (map[string][]*models.MediaItemProviderID, error) {
	if f.providerOnly {
		return map[string][]*models.MediaItemProviderID{"native-rematched": {{ContentID: "native-rematched", ItemType: "movie", Provider: "tmdb", ProviderID: "101"}}}, nil
	}
	return nil, nil
}
func TestResolveIdentitiesRejectsCardRematchedDuringHydration(t *testing.T) {
	h := &CatalogHandler{itemsH: &ItemsHandler{}, identityItems: rematchedIdentityItems{}}
	view, err := h.ResolveIdentities(t.Context(), ItemViewer{}, catalog.IdentityRequest{Seeds: []catalog.IdentitySeed{{Type: "movie", TmdbID: "101"}}, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Matches) != 0 {
		t.Fatalf("confirmed rematched card: %+v", view.Matches)
	}
	h.identityItems = rematchedIdentityItems{providerOnly: true}
	view, err = h.ResolveIdentities(t.Context(), ItemViewer{}, catalog.IdentityRequest{Seeds: []catalog.IdentitySeed{{Type: "movie", TmdbID: "101", ImdbID: "tt101"}}, Limit: 100})
	if err != nil || len(view.Matches) != 1 {
		t.Fatalf("active provider match with missing namespace = %+v, %v", view, err)
	}

}

type sharedIdentityItems struct{ t *testing.T }

func (sharedIdentityItems) ResolveIdentities(context.Context, catalog.IdentityRequest, catalog.AccessFilter) (catalog.IdentityResult, error) {
	return catalog.IdentityResult{Matches: []catalog.IdentityMatch{{Seed: 0, ContentID: "shared"}, {Seed: 1, ContentID: "shared"}}}, nil
}
func (f sharedIdentityItems) GetByIDsWithAccess(_ context.Context, ids []string, _ catalog.AccessFilter) ([]*models.MediaItem, error) {
	if len(ids) != 1 || ids[0] != "shared" {
		f.t.Errorf("item hydration IDs = %v, want [shared]", ids)
	}
	return []*models.MediaItem{{ContentID: "shared", Type: "movie", TmdbID: "101", Title: "Synthetic card"}}, nil
}
func (f sharedIdentityItems) GetIdentityProviderIDs(_ context.Context, ids []string) (map[string][]*models.MediaItemProviderID, error) {
	if len(ids) != 1 || ids[0] != "shared" {
		f.t.Errorf("provider hydration IDs = %v, want [shared]", ids)
	}
	return nil, nil
}
func TestResolveIdentitiesHydratesSharedItemOnce(t *testing.T) {
	h := &CatalogHandler{itemsH: &ItemsHandler{}, identityItems: sharedIdentityItems{t: t}}
	seed := catalog.IdentitySeed{Type: "movie", TmdbID: "101"}
	view, err := h.ResolveIdentities(t.Context(), ItemViewer{}, catalog.IdentityRequest{Seeds: []catalog.IdentitySeed{seed, seed}, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Matches) != 2 {
		t.Fatalf("matches = %+v, want both seeds", view.Matches)
	}
	for i, match := range view.Matches {
		if match.Seed != i || match.Item.ContentID != "shared" {
			t.Errorf("match %d = %+v, want seed %d with shared item", i, match, i)
		}
	}
}
