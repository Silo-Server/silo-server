package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

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

func TestProfileOverrideResetStopsOnBaseSectionReadFailure(t *testing.T) {
	store := &sourcePolicyStore{overrides: []userstore.SectionOverride{{
		ID: "existing", SectionID: "admin-trending", Hidden: true,
	}}}
	h := &SectionHandler{
		StoreProvider: sourcePolicyProvider{store: store},
		sectionReader: sourcePolicySectionReader{err: errors.New("database unavailable")},
	}
	err := h.ResetProfileOverrides(t.Context(), SectionOverridesQuery{UserID: 1, ProfileID: "p1", Scope: "home"})
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.Status != 500 || apiErr.Code != "internal_error" {
		t.Fatalf("error = %#v, want internal_error", err)
	}
	if len(store.overrides) != 1 || store.overrides[0].ID != "existing" || !store.overrides[0].Hidden {
		t.Fatalf("failed lookup mutated overrides: %+v", store.overrides)
	}
}

func TestProfileCannotCreateTraktTrendingSection(t *testing.T) {
	store := &sourcePolicyStore{}
	h := &SectionHandler{StoreProvider: sourcePolicyProvider{store: store}}
	err := h.SaveProfileOverrides(t.Context(), SectionOverridesQuery{UserID: 1, ProfileID: "p1", Scope: "home"}, []SectionOverrideWrite{{
		ID: "new", IsUserAdded: true, UserSectionType: "trending_discover",
		UserConfig: json.RawMessage(`{"source":"trakt","window":"week"}`),
	}})
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.Code != "unsupported_source" {
		t.Fatalf("error = %#v, want unsupported_source", err)
	}
}

func TestAdminCannotCreateTraktTrendingSection(t *testing.T) {
	_, err := (&SectionHandler{}).CreateAdminSection(t.Context(), AdminSectionCreate{
		Title: "Trakt Trending", SectionType: "trending_discover",
		Config: json.RawMessage(`{"source":"trakt","window":"week"}`),
	})
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.Code != "unsupported_source" {
		t.Fatalf("error = %#v, want unsupported_source", err)
	}
}

func TestAdminBulkCannotCreateTraktTrendingSection(t *testing.T) {
	_, err := (&SectionBulkHandler{}).BulkCreateAdminSections(t.Context(), AdminSectionBulkCreate{
		Scope: "home", Title: "Trakt Trending", SectionType: "trending_discover",
		Config: json.RawMessage(`{"source":"trakt","window":"week"}`),
	})
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.Code != "unsupported_source" {
		t.Fatalf("error = %#v, want unsupported_source", err)
	}
}

func TestGenericAdminCollectionCannotCreateTraktSource(t *testing.T) {
	_, err := (&LibraryCollectionHandler{}).CreateAdminCollection(t.Context(), AdminCollectionCreate{
		Title: "Trakt", CollectionType: "trakt",
	})
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.Code != "unsupported_source" {
		t.Fatalf("error = %#v, want unsupported_source", err)
	}
}

func TestProfileTMDBTrendingSaveRequestsImmediateRefresh(t *testing.T) {
	store := &sourcePolicyStore{}
	refresher := sourcePolicyRefresher{configs: make(chan json.RawMessage, 1)}
	h := &SectionHandler{
		StoreProvider:     sourcePolicyProvider{store: store},
		TrendingRefresher: refresher,
	}
	want := json.RawMessage(`{"source":"tmdb","window":"day"}`)
	err := h.SaveProfileOverrides(t.Context(), SectionOverridesQuery{UserID: 1, ProfileID: "p1", Scope: "home"}, []SectionOverrideWrite{{
		ID: "new", IsUserAdded: true, UserSectionType: "trending_discover", UserConfig: want,
	}})
	if err != nil {
		t.Fatalf("SaveProfileOverrides: %v", err)
	}
	select {
	case got := <-refresher.configs:
		if string(got) != string(want) {
			t.Fatalf("refresh config = %s, want %s", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("immediate refresh was not requested")
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
	case <-refresher.configs:
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
		Config:          json.RawMessage(`{"source":"trakt","window":"week"}`),
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

func TestLinkedProfileContradictoryUserAddedFlagCannotBypassTraktPolicy(t *testing.T) {
	store := &sourcePolicyStore{}
	h := &SectionHandler{
		StoreProvider: sourcePolicyProvider{store: store},
		sectionReader: sourcePolicySectionReader{sections: map[string]*sectionspkg.PageSection{
			"admin-trending": {
				ID: "admin-trending", SectionType: sectionspkg.SectionTrendingDiscover,
				Config: json.RawMessage(`{"source":"tmdb","window":"week"}`), Enabled: true,
			},
		}},
	}
	err := h.SaveProfileOverrides(t.Context(), SectionOverridesQuery{UserID: 1, ProfileID: "p1", Scope: "home"}, []SectionOverrideWrite{{
		ID: "new-linked", SectionID: "admin-trending", IsUserAdded: true,
		Config:          json.RawMessage(`{"source":"trakt","window":"week"}`),
		UserSectionType: "trending_discover", UserConfig: json.RawMessage(`{"source":"tmdb","window":"day"}`),
	}})
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.Code != "unsupported_source" {
		t.Fatalf("error = %#v, want unsupported_source", err)
	}
}

func TestLinkedProfileInheritedLegacyTraktCannotBeReactivated(t *testing.T) {
	store := &sourcePolicyStore{overrides: []userstore.SectionOverride{{
		ID: "legacy-linked", SectionID: "legacy-admin-trending", Hidden: true,
	}}}
	h := &SectionHandler{
		StoreProvider: sourcePolicyProvider{store: store},
		sectionReader: sourcePolicySectionReader{sections: map[string]*sectionspkg.PageSection{
			"legacy-admin-trending": {
				ID: "legacy-admin-trending", SectionType: sectionspkg.SectionTrendingDiscover,
				Config: json.RawMessage(`{"source":"trakt","window":"week"}`), Enabled: true,
			},
		}},
	}
	err := h.SaveProfileOverrides(t.Context(), SectionOverridesQuery{UserID: 1, ProfileID: "p1", Scope: "home"}, []SectionOverrideWrite{{
		ID: "legacy-linked", SectionID: "legacy-admin-trending",
	}})
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.Code != "legacy_source_immutable" {
		t.Fatalf("error = %#v, want legacy_source_immutable", err)
	}
}

func TestLinkedProfileCanSuppressInheritedLegacyTrakt(t *testing.T) {
	store := &sourcePolicyStore{}
	h := &SectionHandler{
		StoreProvider: sourcePolicyProvider{store: store},
		sectionReader: sourcePolicySectionReader{sections: map[string]*sectionspkg.PageSection{
			"legacy-admin-trending": {
				ID: "legacy-admin-trending", SectionType: sectionspkg.SectionTrendingDiscover,
				Config: json.RawMessage(`{"source":"trakt","window":"week"}`), Enabled: true,
			},
		}},
	}
	err := h.SaveProfileOverrides(t.Context(), SectionOverridesQuery{UserID: 1, ProfileID: "p1", Scope: "home"}, []SectionOverrideWrite{{
		ID: "hide-legacy", SectionID: "legacy-admin-trending", Hidden: true,
	}})
	if err != nil {
		t.Fatalf("SaveProfileOverrides: %v", err)
	}
	if len(store.overrides) != 1 || !store.overrides[0].Hidden || store.overrides[0].IsUserAdded {
		t.Fatalf("stored override = %+v, want linked hidden suppression", store.overrides)
	}
}

func TestLinkedProfileCannotReactivateLegacyTraktByOmission(t *testing.T) {
	store := &sourcePolicyStore{overrides: []userstore.SectionOverride{{
		ID: "hide-legacy", SectionID: "legacy-admin-trending", Hidden: true,
	}}}
	h := &SectionHandler{
		StoreProvider: sourcePolicyProvider{store: store},
		sectionReader: sourcePolicySectionReader{sections: map[string]*sectionspkg.PageSection{
			"legacy-admin-trending": {
				ID: "legacy-admin-trending", SectionType: sectionspkg.SectionTrendingDiscover,
				Config: json.RawMessage(`{"source":"trakt","window":"week"}`), Enabled: true,
			},
		}},
	}
	err := h.SaveProfileOverrides(t.Context(), SectionOverridesQuery{UserID: 1, ProfileID: "p1", Scope: "home"}, nil)
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.Code != "legacy_source_immutable" {
		t.Fatalf("error = %#v, want legacy_source_immutable", err)
	}
}

func TestLinkedProfileCannotExposeLegacyTraktByReusingOverrideID(t *testing.T) {
	store := &sourcePolicyStore{overrides: []userstore.SectionOverride{{
		ID: "movable", SectionID: "legacy-admin-trending", Config: `{"source":"tmdb","window":"day"}`,
	}}}
	h := &SectionHandler{
		StoreProvider: sourcePolicyProvider{store: store},
		sectionReader: sourcePolicySectionReader{sections: map[string]*sectionspkg.PageSection{
			"legacy-admin-trending": {
				ID: "legacy-admin-trending", SectionType: sectionspkg.SectionTrendingDiscover,
				Config: json.RawMessage(`{"source":"trakt","window":"week"}`), Enabled: true,
			},
			"tmdb-admin-trending": {
				ID: "tmdb-admin-trending", SectionType: sectionspkg.SectionTrendingDiscover,
				Config: json.RawMessage(`{"source":"tmdb","window":"week"}`), Enabled: true,
			},
		}},
	}
	err := h.SaveProfileOverrides(t.Context(), SectionOverridesQuery{UserID: 1, ProfileID: "p1", Scope: "home"}, []SectionOverrideWrite{{
		ID: "movable", SectionID: "tmdb-admin-trending",
	}})
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.Code != "legacy_source_immutable" {
		t.Fatalf("error = %#v, want legacy_source_immutable", err)
	}
}

func TestResetCannotReactivateInheritedLegacyTrakt(t *testing.T) {
	store := &sourcePolicyStore{overrides: []userstore.SectionOverride{{
		ID: "hide-legacy", SectionID: "legacy-admin-trending", Removed: true,
	}}}
	h := &SectionHandler{
		StoreProvider: sourcePolicyProvider{store: store},
		sectionReader: sourcePolicySectionReader{sections: map[string]*sectionspkg.PageSection{
			"legacy-admin-trending": {
				ID: "legacy-admin-trending", SectionType: sectionspkg.SectionTrendingDiscover,
				Config: json.RawMessage(`{"source":"trakt","window":"week"}`), Enabled: true,
			},
		}},
	}
	err := h.ResetProfileOverrides(t.Context(), SectionOverridesQuery{UserID: 1, ProfileID: "p1", Scope: "home"})
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.Code != "legacy_source_immutable" {
		t.Fatalf("error = %#v, want legacy_source_immutable", err)
	}
}

func TestProfileLegacyTraktSourceCannotBeChangedOrReactivated(t *testing.T) {
	originalConfig := json.RawMessage(`{"source":"trakt","window":"week"}`)
	query := SectionOverridesQuery{UserID: 1, ProfileID: "p1", Scope: "home"}

	for _, tc := range []struct {
		name   string
		config json.RawMessage
		hidden bool
	}{
		{name: "source config", config: json.RawMessage(`{"source":"trakt","window":"day"}`), hidden: true},
		{name: "source migration", config: json.RawMessage(`{"source":"tmdb","window":"week"}`), hidden: true},
		{name: "reactivation", config: originalConfig, hidden: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &sourcePolicyStore{overrides: []userstore.SectionOverride{{
				ID: "legacy", IsUserAdded: true, UserSectionType: "trending_discover",
				UserConfig: string(originalConfig), Hidden: true,
			}}}
			h := &SectionHandler{StoreProvider: sourcePolicyProvider{store: store}}
			err := h.SaveProfileOverrides(t.Context(), query, []SectionOverrideWrite{{
				ID: "legacy", IsUserAdded: true, UserSectionType: "trending_discover",
				UserConfig: tc.config, Hidden: tc.hidden,
			}})
			apiErr, ok := errors.AsType[*APIError](err)
			if !ok || apiErr.Code != "legacy_source_immutable" {
				t.Fatalf("error = %#v, want legacy_source_immutable", err)
			}
		})
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

func TestTraktCollectionImportsAreDisabled(t *testing.T) {
	if _, err := (&LibraryCollectionHandler{}).ImportAdminTrakt(t.Context(), AdminCollectionImportTrakt{}); err == nil {
		t.Fatalf("admin Trakt import error = %#v", err)
	} else if apiErr, ok := errors.AsType[*APIError](err); !ok || apiErr.Code != "unsupported_source" {
		t.Fatalf("admin Trakt import error = %#v", err)
	}
	if _, err := (&UserCollectionImportHandler{}).ImportTrakt(t.Context(), 1, "p1", UserImportTraktRequest{}); err == nil {
		t.Fatalf("profile Trakt import error = %#v", err)
	} else if apiErr, ok := errors.AsType[*APIError](err); !ok || apiErr.Code != "unsupported_source" {
		t.Fatalf("profile Trakt import error = %#v", err)
	}
}
