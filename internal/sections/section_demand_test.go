package sections

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type demandAdminLister struct{ sections []*PageSection }

func (l demandAdminLister) ListEnabledSections(context.Context, SectionType) ([]*PageSection, error) {
	return l.sections, nil
}

type demandUserLister struct{ users []*models.User }

func (l demandUserLister) List(context.Context) ([]*models.User, error) { return l.users, nil }

type demandStore struct {
	userstore.UserStore
	overrides []userstore.SectionOverride
}

func (s demandStore) ListAllSectionOverrides(context.Context) ([]userstore.SectionOverride, error) {
	return s.overrides, nil
}

type demandProvider struct {
	stores map[int]userstore.UserStore
	errs   map[int]error
}

func (p demandProvider) ForUser(_ context.Context, userID int) (userstore.UserStore, error) {
	if err := p.errs[userID]; err != nil {
		return nil, err
	}
	return p.stores[userID], nil
}

func TestTrendingDemandSkipsBrokenAccountAndKeepsOtherDemand(t *testing.T) {
	provider := demandProvider{
		stores: map[int]userstore.UserStore{
			2: demandStore{overrides: []userstore.SectionOverride{{
				ID: "day", IsUserAdded: true, UserSectionType: "trending_discover", UserConfig: `{"source":"tmdb","window":"day"}`,
			}}},
		},
		errs: map[int]error{1: errors.New("broken user database")},
	}
	lister := NewSectionDemandLister(
		demandAdminLister{sections: []*PageSection{{ID: "admin", SectionType: SectionTrendingDiscover, Config: json.RawMessage(`{"source":"tmdb","window":"week"}`)}}},
		demandUserLister{users: []*models.User{{ID: 1}, {ID: 2}}},
		provider,
	)

	configs, err := lister.ListTrendingDiscoverConfigs(context.Background())
	if err != nil {
		t.Fatalf("ListTrendingDiscoverConfigs: %v", err)
	}
	if combos := distinctTrendingCombos(configs); len(combos) != 2 {
		t.Fatalf("combos = %v, want admin week and healthy profile day", combos)
	}
}
func (demandProvider) Close() error { return nil }
