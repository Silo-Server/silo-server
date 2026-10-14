package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type ownerUserRepo struct {
	user *models.User
	err  error
}

func (r ownerUserRepo) GetByID(_ context.Context, id int) (*models.User, error) {
	if r.err != nil {
		return nil, r.err
	}
	if r.user == nil || r.user.ID != id {
		return nil, auth.ErrNotFound
	}
	return r.user, nil
}

// ownerProfileStore answers the profile reads ResolveABSOwnerAccess makes.
// Any other UserStore method panics through the nil embedded interface.
type ownerProfileStore struct {
	userstore.UserStore
	profiles []userstore.Profile
}

func (s ownerProfileStore) ListProfiles(context.Context) ([]userstore.Profile, error) {
	return s.profiles, nil
}

type ownerStoreProvider struct{ store ownerProfileStore }

func (p ownerStoreProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}
func (p ownerStoreProvider) Close() error { return nil }

// ownerScopeResolver stands in for the native scope resolver: it knows the
// profiles in the store and returns ErrProfileNotFound for any other. Like the
// real resolvers it refuses a PIN-protected profile unless the caller skips
// PIN verification, which a feed owner must.
type ownerScopeResolver struct {
	profiles []userstore.Profile
	err      error
}

func (r ownerScopeResolver) Resolve(_ context.Context, input access.ResolveInput) (access.Scope, error) {
	if r.err != nil {
		return access.Scope{}, r.err
	}
	if !input.SkipPINVerification {
		return access.Scope{}, access.ErrProfileUnverified
	}
	if input.ProfileID != "" {
		found := false
		for _, p := range r.profiles {
			found = found || p.ID == input.ProfileID
		}
		if !found {
			return access.Scope{}, access.ErrProfileNotFound
		}
	}
	return access.Scope{UserID: input.UserID, ProfileID: input.ProfileID, AllowedLibraryIDs: []int{3}}, nil
}

func newOwnerResolver(users ownerUserRepo, profiles ...userstore.Profile) *ABSAccessResolver {
	return NewABSAccessResolver(users, ownerStoreProvider{ownerProfileStore{profiles: profiles}}, ownerScopeResolver{profiles: profiles})
}

func TestResolveABSOwnerAccess(t *testing.T) {
	enabled := &models.User{ID: 7, Enabled: true}
	kid := userstore.Profile{ID: "kid"}

	t.Run("enabled owner with profile", func(t *testing.T) {
		filter, err := newOwnerResolver(ownerUserRepo{user: enabled}, kid).ResolveABSOwnerAccess(context.Background(), "7", "kid")
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if filter.UserID != 7 || filter.ProfileID != "kid" || len(filter.AllowedLibraryIDs) != 1 || filter.AllowedLibraryIDs[0] != 3 {
			t.Fatalf("filter = %+v, want the owner's resolved scope", filter)
		}
	})

	t.Run("profileless owner on an account without profiles", func(t *testing.T) {
		if _, err := newOwnerResolver(ownerUserRepo{user: enabled}).ResolveABSOwnerAccess(context.Background(), "7", ""); err != nil {
			t.Fatalf("err = %v", err)
		}
	})

	unavailable := map[string]struct {
		resolver  *ABSAccessResolver
		profileID string
	}{
		"disabled account":       {newOwnerResolver(ownerUserRepo{user: &models.User{ID: 7}}, kid), "kid"},
		"deleted account":        {newOwnerResolver(ownerUserRepo{}, kid), "kid"},
		"deleted profile":        {newOwnerResolver(ownerUserRepo{user: enabled}), "kid"},
		"account gained profile": {newOwnerResolver(ownerUserRepo{user: enabled}, kid), ""},
		"profile refused by policy": {
			NewABSAccessResolver(ownerUserRepo{user: enabled}, ownerStoreProvider{ownerProfileStore{profiles: []userstore.Profile{kid}}},
				ownerScopeResolver{err: fmt.Errorf("%w: revoked by policy", access.ErrProfileUnverified)}),
			"kid",
		},
	}
	for name, tc := range unavailable {
		t.Run(name, func(t *testing.T) {
			_, err := tc.resolver.ResolveABSOwnerAccess(context.Background(), "7", tc.profileID)
			if !errors.Is(err, abs.ErrOwnerUnavailable) {
				t.Fatalf("err = %v, want ErrOwnerUnavailable", err)
			}
		})
	}

	t.Run("account lookup failure is not reported as unavailable", func(t *testing.T) {
		_, err := newOwnerResolver(ownerUserRepo{err: errors.New("db down")}, kid).ResolveABSOwnerAccess(context.Background(), "7", "kid")
		if err == nil || errors.Is(err, abs.ErrOwnerUnavailable) {
			t.Fatalf("err = %v, want a lookup error", err)
		}
	})

	t.Run("unconfigured resolver fails closed", func(t *testing.T) {
		var r *ABSAccessResolver
		if _, err := r.ResolveABSOwnerAccess(context.Background(), "7", "kid"); err == nil {
			t.Fatal("nil resolver returned access")
		}
	})
}
