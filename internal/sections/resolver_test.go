package sections

import (
	"encoding/json"
	"testing"
)

func TestResolve_NoOverrides(t *testing.T) {
	admin := []*PageSection{
		{ID: "1", Position: 0, SectionType: SectionRecentlyAdded, Title: "Recently Added", ItemLimit: 20, Config: json.RawMessage(`{}`)},
		{ID: "2", Position: 1, SectionType: SectionFavorites, Title: "Favorites", ItemLimit: 10, Config: json.RawMessage(`{}`)},
	}

	result := Resolve(admin, nil)
	if len(result) != 2 {
		t.Fatalf("expected 2 sections, got %d", len(result))
	}
	if result[0].Title != "Recently Added" {
		t.Errorf("first section title = %q", result[0].Title)
	}
}

func TestResolve_HiddenOverride(t *testing.T) {
	admin := []*PageSection{
		{ID: "1", Position: 0, SectionType: SectionRecentlyAdded, Title: "Recently Added", ItemLimit: 20, Config: json.RawMessage(`{}`)},
		{ID: "2", Position: 1, SectionType: SectionFavorites, Title: "Favorites", ItemLimit: 10, Config: json.RawMessage(`{}`)},
	}

	overrides := []ProfileSectionOverride{
		{SectionID: "1", Hidden: true},
	}

	result := Resolve(admin, overrides)
	if len(result) != 1 {
		t.Fatalf("expected 1 section (1 hidden), got %d", len(result))
	}
	if result[0].ID != "2" {
		t.Errorf("remaining section ID = %q, want %q", result[0].ID, "2")
	}
}

func TestResolve_RemovedOverrideSkipsAdminSection(t *testing.T) {
	admin := []*PageSection{
		{ID: "1", Position: 0, SectionType: SectionRecentlyAdded, Title: "Recently Added", ItemLimit: 20, Config: json.RawMessage(`{}`)},
		{ID: "2", Position: 1, SectionType: SectionFavorites, Title: "Favorites", ItemLimit: 10, Config: json.RawMessage(`{}`)},
	}

	overrides := []ProfileSectionOverride{
		{SectionID: "1", Removed: true},
	}

	result := Resolve(admin, overrides)
	if len(result) != 1 || result[0].ID != "2" {
		t.Fatalf("resolved IDs = %#v, want only section 2", result)
	}
}

func TestResolve_PositionOverride(t *testing.T) {
	admin := []*PageSection{
		{ID: "1", Position: 0, SectionType: SectionRecentlyAdded, Title: "A", ItemLimit: 20, Config: json.RawMessage(`{}`)},
		{ID: "2", Position: 1, SectionType: SectionFavorites, Title: "B", ItemLimit: 10, Config: json.RawMessage(`{}`)},
	}

	posB := 0
	posA := 1
	overrides := []ProfileSectionOverride{
		{SectionID: "2", Position: &posB}, // Move B to position 0
		{SectionID: "1", Position: &posA}, // Move A to position 1
	}

	result := Resolve(admin, overrides)
	if result[0].ID != "2" {
		t.Errorf("first section should be B (id=2), got id=%s", result[0].ID)
	}
	if result[1].ID != "1" {
		t.Errorf("second section should be A (id=1), got id=%s", result[1].ID)
	}
}

func TestResolve_UserAddedSection(t *testing.T) {
	admin := []*PageSection{
		{ID: "1", Position: 0, SectionType: SectionRecentlyAdded, Title: "A", ItemLimit: 20, Config: json.RawMessage(`{}`)},
	}

	pos := 1
	overrides := []ProfileSectionOverride{
		{ID: "custom-1", SectionID: "", SectionType: "genre", Title: "My Action", Position: &pos, ItemLimit: new(15), Config: json.RawMessage(`{"genres":["Action"]}`)},
	}

	result := Resolve(admin, overrides)
	if len(result) != 2 {
		t.Fatalf("expected 2 sections, got %d", len(result))
	}
	if result[1].Title != "My Action" {
		t.Errorf("second section title = %q, want %q", result[1].Title, "My Action")
	}
	if !result[1].IsCustom {
		t.Error("user-added section should be marked IsCustom")
	}
}

func TestResolve_RemovedCustomSection(t *testing.T) {
	admin := []*PageSection{
		{ID: "1", Position: 0, SectionType: SectionRecentlyAdded, Title: "A", ItemLimit: 20, Config: json.RawMessage(`{}`)},
	}

	overrides := []ProfileSectionOverride{
		{ID: "custom-1", SectionType: "genre", Title: "My Action", Removed: true},
	}

	result := Resolve(admin, overrides)
	if len(result) != 1 {
		t.Fatalf("expected removed custom section to be skipped, got %d sections", len(result))
	}
	if result[0].ID != "1" {
		t.Fatalf("remaining section ID = %q, want %q", result[0].ID, "1")
	}
}

func TestResolve_HiddenCustomSection(t *testing.T) {
	overrides := []ProfileSectionOverride{{
		ID: "custom-1", SectionType: "trending_discover", Hidden: true,
	}}
	if result := Resolve(nil, overrides); len(result) != 0 {
		t.Fatalf("hidden custom section resolved at runtime: %#v", result)
	}
	settings := ResolveForSettings(nil, overrides)
	if len(settings) != 1 || !settings[0].Hidden {
		t.Fatalf("settings result = %#v, want one hidden custom section", settings)
	}
}

func TestResolveForSettings_IncludesHidden(t *testing.T) {
	admin := []*PageSection{
		{ID: "1", Position: 0, SectionType: SectionRecentlyAdded, Title: "Recently Added", ItemLimit: 20, Config: json.RawMessage(`{}`)},
		{ID: "2", Position: 1, SectionType: SectionFavorites, Title: "Favorites", ItemLimit: 10, Config: json.RawMessage(`{}`)},
	}

	overrides := []ProfileSectionOverride{
		{SectionID: "1", Hidden: true},
	}

	result := ResolveForSettings(admin, overrides)
	if len(result) != 2 {
		t.Fatalf("expected 2 sections (including hidden), got %d", len(result))
	}
	if !result[0].Hidden {
		t.Error("section 1 should be marked Hidden")
	}
	if result[0].Customized != true {
		t.Error("hidden section should be marked Customized")
	}
	if result[1].Hidden {
		t.Error("section 2 should not be hidden")
	}
}

func TestResolveForSettings_SkipsRemovedSection(t *testing.T) {
	admin := []*PageSection{
		{ID: "1", Position: 0, SectionType: SectionRecentlyAdded, Title: "Recently Added", ItemLimit: 20, Config: json.RawMessage(`{}`)},
	}

	overrides := []ProfileSectionOverride{
		{SectionID: "1", Removed: true},
	}

	result := ResolveForSettings(admin, overrides)
	if len(result) != 0 {
		t.Fatalf("expected removed section to be omitted from settings, got %d entries", len(result))
	}
}

func TestResolveForSettings_SkipsRemovedCustomSection(t *testing.T) {
	admin := []*PageSection{
		{ID: "1", Position: 0, SectionType: SectionRecentlyAdded, Title: "Recently Added", ItemLimit: 20, Config: json.RawMessage(`{}`)},
	}

	overrides := []ProfileSectionOverride{
		{ID: "custom-1", SectionType: "genre", Title: "My Action", Removed: true},
	}

	result := ResolveForSettings(admin, overrides)
	if len(result) != 1 {
		t.Fatalf("expected removed custom section to be omitted from settings, got %d entries", len(result))
	}
	if result[0].ID != "1" {
		t.Fatalf("remaining section ID = %q, want %q", result[0].ID, "1")
	}
}

func TestResolveIncludesUserAddedRecipeFromOverride(t *testing.T) {
	admin := []*PageSection{}
	overrides := []ProfileSectionOverride{
		{
			ID:              "u1",
			ProfileID:       "p1",
			Scope:           "home",
			SectionID:       "",
			IsUserAdded:     true,
			UserSectionType: "hidden_gems",
			UserConfig:      json.RawMessage(`{"min_rating":7.5}`),
			UserTitle:       "Hidden Gems",
		},
	}
	resolved := Resolve(admin, overrides)
	if len(resolved) != 1 {
		t.Fatalf("got %d sections", len(resolved))
	}
	if resolved[0].SectionType != "hidden_gems" {
		t.Errorf("section_type = %s want hidden_gems", resolved[0].SectionType)
	}
	if resolved[0].Title != "Hidden Gems" {
		t.Errorf("title = %s want Hidden Gems", resolved[0].Title)
	}
	if string(resolved[0].Config) != `{"min_rating":7.5}` {
		t.Errorf("config = %s want {\"min_rating\":7.5}", resolved[0].Config)
	}
	if !resolved[0].IsCustom {
		t.Error("expected IsCustom true")
	}
}

func TestResolveEmptySectionIDPrefersExplicitUserFields(t *testing.T) {
	overrides := []ProfileSectionOverride{{
		ID:              "conflicting",
		SectionType:     SectionTrendingDiscover,
		Config:          json.RawMessage(`{"source":"trakt","window":"week"}`),
		IsUserAdded:     false,
		UserSectionType: SectionTrendingDiscover,
		UserConfig:      json.RawMessage(`{"source":"tmdb","window":"day"}`),
	}}
	resolved := Resolve(nil, overrides)
	if len(resolved) != 1 {
		t.Fatalf("resolved = %+v", resolved)
	}
	if resolved[0].SectionType != SectionTrendingDiscover || string(resolved[0].Config) != `{"source":"tmdb","window":"day"}` {
		t.Fatalf("resolved = %+v, want explicit TMDB user fields", resolved[0])
	}
}

func TestResolveBackwardCompatLegacyUserAdded(t *testing.T) {
	// An override with the OLD shape (no IsUserAdded flag, but SectionID empty)
	// should still resolve correctly using the legacy fields.
	admin := []*PageSection{}
	overrides := []ProfileSectionOverride{
		{
			ID:          "u2",
			ProfileID:   "p1",
			Scope:       "home",
			SectionID:   "",
			SectionType: "recently_added",
			Title:       "My recents",
		},
	}
	resolved := Resolve(admin, overrides)
	if len(resolved) != 1 || resolved[0].SectionType != "recently_added" || resolved[0].Title != "My recents" {
		t.Fatalf("legacy user-added not resolved correctly: %+v", resolved)
	}
}
