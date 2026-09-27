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

func (l demandAdminLister) ListTrendingDiscoverSections(context.Context) ([]*PageSection, error) {
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
	lister := NewTrendingDemandLister(
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

func TestTrendingDemandIncludesActiveProfileOnlySections(t *testing.T) {
	provider := demandProvider{stores: map[int]userstore.UserStore{
		7: demandStore{overrides: []userstore.SectionOverride{
			{ID: "day", IsUserAdded: true, UserSectionType: "trending_discover", UserConfig: `{"source":"tmdb","window":"day"}`},
			{ID: "week", SectionType: "trending_discover", Config: `{"source":"tmdb","window":"week"}`},
			{ID: "hidden", Hidden: true, IsUserAdded: true, UserSectionType: "trending_discover", UserConfig: `{"source":"tmdb","window":"day"}`},
			{ID: "removed", Removed: true, IsUserAdded: true, UserSectionType: "trending_discover", UserConfig: `{"source":"tmdb","window":"week"}`},
		}},
	}}
	lister := NewTrendingDemandLister(
		demandAdminLister{},
		demandUserLister{users: []*models.User{{ID: 7}}},
		provider,
	)

	configs, err := lister.ListTrendingDiscoverConfigs(context.Background())
	if err != nil {
		t.Fatalf("ListTrendingDiscoverConfigs: %v", err)
	}
	if len(configs) != 2 {
		t.Fatalf("configs = %d, want 2 active profile configs", len(configs))
	}
	var windows []string
	for _, raw := range configs {
		var value struct {
			Window string `json:"window"`
		}
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatalf("unmarshal config: %v", err)
		}
		windows = append(windows, value.Window)
	}
	if windows[0] != "day" || windows[1] != "week" {
		t.Fatalf("windows = %v, want [day week]", windows)
	}
}

func TestTrendingDemandIncludesLinkedProfileConfig(t *testing.T) {
	provider := demandProvider{stores: map[int]userstore.UserStore{
		9: demandStore{overrides: []userstore.SectionOverride{{
			ID: "override", SectionID: "admin-trending", Config: `{"source":"tmdb","window":"day"}`,
		}}},
	}}
	lister := NewTrendingDemandLister(
		demandAdminLister{sections: []*PageSection{{ID: "admin-trending", SectionType: SectionTrendingDiscover, Config: json.RawMessage(`{"source":"tmdb","window":"week"}`)}}},
		demandUserLister{users: []*models.User{{ID: 9}}},
		provider,
	)

	configs, err := lister.ListTrendingDiscoverConfigs(context.Background())
	if err != nil {
		t.Fatalf("ListTrendingDiscoverConfigs: %v", err)
	}
	combos := distinctTrendingCombos(configs)
	if len(combos) != 2 {
		t.Fatalf("combos = %v, want admin week and profile day", combos)
	}
}

func TestTrendingDemandEmptySectionIDPrefersExplicitUserConfig(t *testing.T) {
	provider := demandProvider{stores: map[int]userstore.UserStore{
		11: demandStore{overrides: []userstore.SectionOverride{{
			ID: "conflicting", SectionType: "trending_discover", Config: `{"source":"trakt","window":"week"}`,
			UserSectionType: "trending_discover", UserConfig: `{"source":"tmdb","window":"day"}`,
		}}},
	}}
	lister := NewTrendingDemandLister(
		demandAdminLister{},
		demandUserLister{users: []*models.User{{ID: 11}}},
		provider,
	)

	configs, err := lister.ListTrendingDiscoverConfigs(context.Background())
	if err != nil {
		t.Fatalf("ListTrendingDiscoverConfigs: %v", err)
	}
	combos := distinctTrendingCombos(configs)
	if len(combos) != 1 {
		t.Fatalf("combos = %v, want only explicit TMDB day config", combos)
	}
	if combos[0].source != "tmdb" || combos[0].window != "day" {
		t.Fatalf("combo = %+v, want tmdb/day", combos[0])
	}
}
