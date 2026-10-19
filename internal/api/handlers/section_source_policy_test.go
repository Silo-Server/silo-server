package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	sectionspkg "github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type sourcePolicyStore struct {
	userstore.UserStore
	mu        sync.Mutex
	overrides []userstore.SectionOverride
}

func (s *sourcePolicyStore) ListSectionOverrides(context.Context, string, string, string) ([]userstore.SectionOverride, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]userstore.SectionOverride(nil), s.overrides...), nil
}

func (s *sourcePolicyStore) SaveSectionOverrides(_ context.Context, _, _, _ string, overrides []userstore.SectionOverride) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.overrides = append([]userstore.SectionOverride(nil), overrides...)
	return nil
}

type sourcePolicyProvider struct{ store userstore.UserStore }

func (p sourcePolicyProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}
func (sourcePolicyProvider) Close() error { return nil }

type sourcePolicyRefresher struct{ configs chan json.RawMessage }

func (r sourcePolicyRefresher) RefreshConfig(_ context.Context, config json.RawMessage) {
	r.configs <- config
}

type sourcePolicySectionReader struct {
	sections map[string]*sectionspkg.PageSection
	err      error
}

func (r sourcePolicySectionReader) GetByID(_ context.Context, id string) (*sectionspkg.PageSection, error) {
	if r.err != nil {
		return nil, r.err
	}
	section, ok := r.sections[id]
	if !ok {
		return nil, sectionspkg.ErrSectionNotFound
	}
	return section, nil
}

func TestProfileOverrideSaveStopsOnBaseSectionReadFailure(t *testing.T) {
	store := &sourcePolicyStore{overrides: []userstore.SectionOverride{{
		ID: "existing", SectionID: "admin-trending", Hidden: true,
	}}}
	h := &SectionHandler{
		StoreProvider: sourcePolicyProvider{store: store},
		sectionReader: sourcePolicySectionReader{err: errors.New("database unavailable")},
	}
	err := h.SaveProfileOverrides(t.Context(), SectionOverridesQuery{UserID: 1, ProfileID: "p1", Scope: "home"}, []SectionOverrideWrite{{
		ID: "existing", SectionID: "admin-trending",
	}})
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.Status != 500 || apiErr.Code != "internal_error" {
		t.Fatalf("error = %#v, want internal_error", err)
	}
	if len(store.overrides) != 1 || store.overrides[0].ID != "existing" || !store.overrides[0].Hidden {
		t.Fatalf("failed lookup mutated overrides: %+v", store.overrides)
	}
}

func TestProfileTMDBTrendingUnchangedSaveDoesNotRefreshAgain(t *testing.T) {
	store := &sourcePolicyStore{}
	refresher := sourcePolicyRefresher{configs: make(chan json.RawMessage, 2)}
	h := &SectionHandler{
		StoreProvider:     sourcePolicyProvider{store: store},
		TrendingRefresher: refresher,
	}
	write := SectionOverrideWrite{
		ID: "new", IsUserAdded: true, UserSectionType: "trending_discover",
		UserConfig: json.RawMessage(`{"source":"tmdb","window":"day"}`),
	}
	query := SectionOverridesQuery{UserID: 1, ProfileID: "p1", Scope: "home"}
	if err := h.SaveProfileOverrides(t.Context(), query, []SectionOverrideWrite{write}); err != nil {
		t.Fatalf("first SaveProfileOverrides: %v", err)
	}
	select {
	case config := <-refresher.configs:
		if string(config) != string(write.UserConfig) {
			t.Fatalf("refresh config = %s, want %s", config, write.UserConfig)
		}
	case <-time.After(time.Second):
		t.Fatal("first save did not request an immediate refresh")
	}
	if err := h.SaveProfileOverrides(t.Context(), query, []SectionOverrideWrite{write}); err != nil {
		t.Fatalf("second SaveProfileOverrides: %v", err)
	}
	select {
	case config := <-refresher.configs:
		t.Fatalf("unchanged save unexpectedly refreshed %s", config)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestProfileConflictingLegacyFieldsResolveToValidatedUserFields(t *testing.T) {
	store := &sourcePolicyStore{}
	refresher := sourcePolicyRefresher{configs: make(chan json.RawMessage, 1)}
	h := &SectionHandler{
		StoreProvider:     sourcePolicyProvider{store: store},
		TrendingRefresher: refresher,
	}
	err := h.SaveProfileOverrides(t.Context(), SectionOverridesQuery{UserID: 1, ProfileID: "p1", Scope: "home"}, []SectionOverrideWrite{{
		ID:              "conflicting",
		SectionType:     "trending_discover",
		Config:          json.RawMessage(`{"source":"tmdb","window":"week"}`),
		UserSectionType: "trending_discover",
		UserConfig:      json.RawMessage(`{"source":"tmdb","window":"day"}`),
	}})
	if err != nil {
		t.Fatalf("SaveProfileOverrides: %v", err)
	}
	if len(store.overrides) != 1 || !store.overrides[0].IsUserAdded {
		t.Fatalf("stored override = %+v, want canonical user-added row", store.overrides)
	}
	resolved := sectionspkg.Resolve(nil, []sectionspkg.ProfileSectionOverride{{
		ID:              store.overrides[0].ID,
		SectionType:     sectionspkg.SectionType(store.overrides[0].SectionType),
		Config:          json.RawMessage(store.overrides[0].Config),
		IsUserAdded:     store.overrides[0].IsUserAdded,
		UserSectionType: sectionspkg.SectionType(store.overrides[0].UserSectionType),
		UserConfig:      json.RawMessage(store.overrides[0].UserConfig),
	}})
	if len(resolved) != 1 || string(resolved[0].Config) != `{"source":"tmdb","window":"day"}` {
		t.Fatalf("resolved = %+v, want validated TMDB config", resolved)
	}
	select {
	case config := <-refresher.configs:
		if string(config) != `{"source":"tmdb","window":"day"}` {
			t.Fatalf("refresh config = %s", config)
		}
	case <-time.After(time.Second):
		t.Fatal("canonical TMDB config was not refreshed")
	}
}

func TestLinkedProfileTMDBConfigChangeRequestsImmediateRefresh(t *testing.T) {
	store := &sourcePolicyStore{overrides: []userstore.SectionOverride{{
		ID: "linked", SectionID: "admin-trending", Config: `{"source":"tmdb","window":"week"}`,
	}}}
	refresher := sourcePolicyRefresher{configs: make(chan json.RawMessage, 1)}
	h := &SectionHandler{
		StoreProvider:     sourcePolicyProvider{store: store},
		TrendingRefresher: refresher,
		sectionReader: sourcePolicySectionReader{sections: map[string]*sectionspkg.PageSection{
			"admin-trending": {
				ID: "admin-trending", SectionType: sectionspkg.SectionTrendingDiscover,
				Config: json.RawMessage(`{"source":"tmdb","window":"week"}`), Enabled: true,
			},
		}},
	}
	err := h.SaveProfileOverrides(t.Context(), SectionOverridesQuery{UserID: 1, ProfileID: "p1", Scope: "home"}, []SectionOverrideWrite{{
		ID: "linked", SectionID: "admin-trending", Config: json.RawMessage(`{"source":"tmdb","window":"day"}`),
	}})
	if err != nil {
		t.Fatalf("SaveProfileOverrides: %v", err)
	}
	select {
	case config := <-refresher.configs:
		if string(config) != `{"source":"tmdb","window":"day"}` {
			t.Fatalf("refresh config = %s", config)
		}
	case <-time.After(time.Second):
		t.Fatal("linked TMDB config change did not request immediate refresh")
	}
}

func TestProfileOverrideIDsMustBeUnique(t *testing.T) {
	h := &SectionHandler{StoreProvider: sourcePolicyProvider{store: &sourcePolicyStore{}}}
	err := h.SaveProfileOverrides(t.Context(), SectionOverridesQuery{UserID: 1, ProfileID: "p1", Scope: "home"}, []SectionOverrideWrite{
		{ID: "duplicate", IsUserAdded: true, UserSectionType: "trending_discover", UserConfig: json.RawMessage(`{"source":"tmdb"}`)},
		{ID: "duplicate", IsUserAdded: true, UserSectionType: "trending_discover", UserConfig: json.RawMessage(`{"source":"tmdb"}`)},
	})
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.Code != "duplicate_override" {
		t.Fatalf("error = %#v, want duplicate_override", err)
	}
}

// A profile that is not an admin may always add rule rows, and keeps and
// changes the Editor's picks rows it already has, but can't add a new one.
func TestProfileRuleRowsAndKeptEditorsPicks(t *testing.T) {
	query := SectionOverridesQuery{UserID: 1, ProfileID: "p1", Scope: "home"}
	picks := SectionOverrideWrite{ID: "picks", IsUserAdded: true, UserSectionType: "admin_curated_list", UserConfig: json.RawMessage(`{"item_ids":["a","b"]}`)}
	rules := SectionOverrideWrite{ID: "rules", IsUserAdded: true, UserSectionType: "custom_filter", UserConfig: json.RawMessage(`{"filter":{}}`)}
	stored := []userstore.SectionOverride{{ID: "picks", IsUserAdded: true, UserSectionType: "admin_curated_list", UserConfig: `{"item_ids":["a"]}`}}
	profile := apimw.SetClaims(t.Context(), &auth.Claims{Role: "user", UserID: 1})
	admin := apimw.SetActingAdminDecider(apimw.SetClaims(t.Context(), &auth.Claims{Role: "admin", UserID: 1}),
		func(context.Context, apimw.ActingAdminRequest) (bool, error) { return true, nil })

	cases := []struct {
		name   string
		ctx    context.Context
		stored []userstore.SectionOverride
		writes []SectionOverrideWrite
		refuse bool
	}{
		{"rule row with nothing saved", profile, nil, []SectionOverrideWrite{rules}, false},
		{"kept and changed Editor's picks beside a new rule row", profile, stored, []SectionOverrideWrite{picks, rules}, false},
		{"new Editor's picks", profile, nil, []SectionOverrideWrite{picks}, true},
		{"Editor's picks over another row's ID", profile, []userstore.SectionOverride{{ID: "picks", IsUserAdded: true, UserSectionType: "hidden_gems"}}, []SectionOverrideWrite{picks}, true},
		{"Editor's picks over a server row's override", profile, []userstore.SectionOverride{{ID: "picks", SectionID: "admin-1", SectionType: "admin_curated_list"}}, []SectionOverrideWrite{picks}, true},
		{"admin adds Editor's picks", admin, nil, []SectionOverrideWrite{picks}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &sourcePolicyStore{overrides: tc.stored}
			h := &SectionHandler{StoreProvider: sourcePolicyProvider{store: store}}
			err := h.SaveProfileOverrides(tc.ctx, query, tc.writes)
			if !tc.refuse {
				if err != nil {
					t.Fatalf("SaveProfileOverrides: %v", err)
				}
				if len(store.overrides) != len(tc.writes) {
					t.Fatalf("saved %+v", store.overrides)
				}
				return
			}
			apiErr, ok := errors.AsType[*APIError](err)
			if !ok || apiErr.Status != 403 || apiErr.Code != "custom_disabled" {
				t.Fatalf("error = %#v, want 403 custom_disabled", err)
			}
			if len(store.overrides) != len(tc.stored) {
				t.Fatalf("refused save changed the store: %+v", store.overrides)
			}
		})
	}
}

// No profile adds a Next Up row: the server inserts the only one when the
// profile's ui.next_up_mode is separate. A profile keeps one it saved before.
func TestProfileCannotAddNextUpRow(t *testing.T) {
	query := SectionOverridesQuery{UserID: 1, ProfileID: "p1", Scope: "home"}
	nextUp := SectionOverrideWrite{ID: "deck", IsUserAdded: true, UserSectionType: "next_up", UserTitle: "On deck"}
	stored := []userstore.SectionOverride{{ID: "deck", IsUserAdded: true, UserSectionType: "next_up"}}
	admin := apimw.SetActingAdminDecider(apimw.SetClaims(t.Context(), &auth.Claims{Role: "admin", UserID: 1}),
		func(context.Context, apimw.ActingAdminRequest) (bool, error) { return true, nil })

	cases := []struct {
		name   string
		stored []userstore.SectionOverride
		write  SectionOverrideWrite
		refuse bool
	}{
		{"new row", nil, nextUp, true},
		{"new row without an ID", nil, SectionOverrideWrite{IsUserAdded: true, UserSectionType: "next_up"}, true},
		{"new row by the legacy section_type", nil, SectionOverrideWrite{ID: "deck", IsUserAdded: true, SectionType: "next_up"}, true},
		{"over another row's ID", []userstore.SectionOverride{{ID: "deck", IsUserAdded: true, UserSectionType: "hidden_gems"}}, nextUp, true},
		{"kept row", stored, nextUp, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &sourcePolicyStore{overrides: tc.stored}
			h := &SectionHandler{StoreProvider: sourcePolicyProvider{store: store}}
			err := h.SaveProfileOverrides(admin, query, []SectionOverrideWrite{tc.write})
			if !tc.refuse {
				if err != nil {
					t.Fatalf("SaveProfileOverrides: %v", err)
				}
				return
			}
			apiErr, ok := errors.AsType[*APIError](err)
			if !ok || apiErr.Status != http.StatusBadRequest || !strings.Contains(apiErr.Message, "next_up") {
				t.Fatalf("error = %#v, want a 400 naming next_up", err)
			}
			if len(store.overrides) != len(tc.stored) {
				t.Fatalf("refused save changed the store: %+v", store.overrides)
			}
		})
	}
}
