package apiv2

import (
	"context"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// householdStore is the profile half of a user store: the household check
// reads only the profile rows.
type householdStore struct {
	userstore.UserStore
	profiles []userstore.Profile
}

func (s householdStore) GetProfile(_ context.Context, id string) (*userstore.Profile, error) {
	for i := range s.profiles {
		if s.profiles[i].ID == id {
			return &s.profiles[i], nil
		}
	}
	return nil, nil
}

func (s householdStore) ListProfiles(context.Context) ([]userstore.Profile, error) {
	return s.profiles, nil
}

// householdStores serves the same profiles to every account; an empty one is
// a household with no profiles yet.
type householdStores struct {
	userstore.UserStoreProvider
	profiles []userstore.Profile
}

func (p householdStores) ForUser(context.Context, int) (userstore.UserStore, error) {
	return householdStore{profiles: p.profiles}, nil
}

// TestPersonalAPIKeyCreationFailsClosedWithoutStores: a service wired without
// the household stores cannot tell the acting admin apart, so it refuses.
func TestPersonalAPIKeyCreationFailsClosedWithoutStores(t *testing.T) {
	store := &scopeCatalogStore{}
	deps := pilotDeps(nil, nil)
	deps.PersonalAPIKeys = handlers.NewAPIKeyHandler(store)
	rec := do(t, NewHandler(deps), http.MethodPost, Prefix+"/api-keys", `{"label":"Tool"}`, with(bearer(adminToken), "X-Profile-Id", "p-primary"))
	requireProblem(t, rec, TypePermissionDenied)
	if store.created {
		t.Fatal("an unwired create reached storage")
	}
}
