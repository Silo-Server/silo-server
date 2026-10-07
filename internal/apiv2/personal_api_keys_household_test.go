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

// TestPersonalAPIKeyCreationRequiresActingAdmin: a key skips every profile's
// PIN, so on an admin account only the acting admin — the primary profile,
// verified when it has a PIN — mints one. A child profile on the admin's
// account is refused, and so is a profile-less request once the household
// has a limited profile.
func TestPersonalAPIKeyCreationRequiresActingAdmin(t *testing.T) {
	restricted := []userstore.Profile{
		{ID: "p-primary", Name: "Ada", IsPrimary: true},
		{ID: "p-primary-locked", Name: "Ada locked", PINHash: "hash"},
		{ID: "p-owner", Name: "Kiddo", IsChild: true, MaxContentRating: "PG"},
	}
	lockedPrimary := []userstore.Profile{
		{ID: "p-primary-locked", Name: "Ada", IsPrimary: true, PINHash: "hash"},
		{ID: "p-owner", Name: "Kiddo"},
	}
	unrestricted := []userstore.Profile{
		{ID: "p-primary", Name: "Ada", IsPrimary: true},
		{ID: "p-owner", Name: "Sam"},
	}
	for _, tc := range []struct {
		name     string
		profiles []userstore.Profile
		headers  map[string]string
		want     ProblemType
	}{
		{"child profile on the admin account", restricted, with(bearer(adminToken), "X-Profile-Id", "p-owner"), TypePermissionDenied},
		{"non-primary on an unrestricted household", unrestricted, with(bearer(adminToken), "X-Profile-Id", "p-owner"), TypePermissionDenied},
		{"profile-less on a restricted household", restricted, bearer(adminToken), TypePermissionDenied},
		{"locked primary without a token", lockedPrimary, with(bearer(adminToken), "X-Profile-Id", "p-primary-locked"), TypeProfileVerificationRequired},
		{"primary without a PIN", restricted, with(bearer(adminToken), "X-Profile-Id", "p-primary"), ProblemType{}},
		{"locked primary with a token", lockedPrimary, with(with(bearer(adminToken), "X-Profile-Id", "p-primary-locked"), "X-Profile-Token", "pvt"), ProblemType{}},
		{"profile-less on an unrestricted household", unrestricted, bearer(adminToken), ProblemType{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &scopeCatalogStore{}
			svc := handlers.NewAPIKeyHandler(store)
			svc.Stores = householdStores{profiles: tc.profiles}
			deps := pilotDeps(nil, nil)
			deps.PersonalAPIKeys = svc
			rec := do(t, NewHandler(deps), http.MethodPost, Prefix+"/api-keys", `{"label":"Tool"}`, tc.headers)
			if tc.want == (ProblemType{}) {
				if rec.Code != http.StatusCreated || !store.created {
					t.Fatalf("status %d created %v body %s", rec.Code, store.created, rec.Body.String())
				}
				return
			}
			requireProblem(t, rec, tc.want)
			if store.created {
				t.Fatal("a refused create reached storage")
			}
		})
	}
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
