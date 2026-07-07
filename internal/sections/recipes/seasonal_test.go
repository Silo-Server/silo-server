package recipes

import (
	"encoding/json"
	"testing"
	"time"
)

// TestHalloweenInOctoberMatches verifies the halloween predicate matches October
// and rejects November.
func TestHalloweenInOctoberMatches(t *testing.T) {
	pred, ok := SeasonalPredicates["halloween"]
	if !ok {
		t.Fatal("halloween predicate not found")
	}

	oct15 := time.Date(2024, time.October, 15, 12, 0, 0, 0, time.UTC)
	if !pred(oct15) {
		t.Error("halloween predicate should match Oct 15")
	}

	nov1 := time.Date(2024, time.November, 1, 12, 0, 0, 0, time.UTC)
	if pred(nov1) {
		t.Error("halloween predicate should reject Nov 1")
	}
}

// TestChristmasDateRangeMatches verifies the christmas predicate matches Dec 24
// and rejects Nov 30. Also pins the boundaries: the window is Dec 1 inclusive
// through Dec 31 inclusive.
func TestChristmasDateRangeMatches(t *testing.T) {
	pred, ok := SeasonalPredicates["christmas"]
	if !ok {
		t.Fatal("christmas predicate not found")
	}

	dec24 := time.Date(2024, time.December, 24, 12, 0, 0, 0, time.UTC)
	if !pred(dec24) {
		t.Error("christmas predicate should match Dec 24")
	}

	nov30 := time.Date(2024, time.November, 30, 12, 0, 0, 0, time.UTC)
	if pred(nov30) {
		t.Error("christmas predicate should reject Nov 30")
	}

	// Boundary: start of window (Dec 1 00:00:00) must match.
	dec1Start := time.Date(2026, time.December, 1, 0, 0, 0, 0, time.UTC)
	if !pred(dec1Start) {
		t.Error("christmas predicate should match Dec 1 00:00:00 (start boundary)")
	}

	// Boundary: end of window (Dec 31 23:59:59) must match.
	dec31End := time.Date(2026, time.December, 31, 23, 59, 59, 0, time.UTC)
	if !pred(dec31End) {
		t.Error("christmas predicate should match Dec 31 23:59:59 (end boundary)")
	}

	// Boundary: just past end of window (Jan 1 of next year) must reject.
	jan1Next := time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC)
	if pred(jan1Next) {
		t.Error("christmas predicate should reject Jan 1 (just past window)")
	}
}

// TestSaturdayMorningPredicate verifies the saturday_morning predicate matches
// Saturday before 13:00, rejects Monday at 10am, and rejects Saturday at 3pm.
func TestSaturdayMorningPredicate(t *testing.T) {
	pred, ok := SeasonalPredicates["saturday_morning"]
	if !ok {
		t.Fatal("saturday_morning predicate not found")
	}

	// Saturday at 10am — should match
	sat10am := time.Date(2024, time.October, 5, 10, 0, 0, 0, time.UTC) // Oct 5, 2024 is a Saturday
	if !pred(sat10am) {
		t.Error("saturday_morning predicate should match Saturday 10am")
	}

	// Monday at 10am — should reject
	mon10am := time.Date(2024, time.October, 7, 10, 0, 0, 0, time.UTC) // Oct 7, 2024 is a Monday
	if pred(mon10am) {
		t.Error("saturday_morning predicate should reject Monday 10am")
	}

	// Saturday at 3pm (15:00) — should reject
	sat3pm := time.Date(2024, time.October, 5, 15, 0, 0, 0, time.UTC)
	if pred(sat3pm) {
		t.Error("saturday_morning predicate should reject Saturday 3pm")
	}

	// Boundary: Saturday at exactly 13:00 must reject (predicate is `t.Hour() < 13`).
	// May 2, 2026 is a Saturday.
	sat13 := time.Date(2026, time.May, 2, 13, 0, 0, 0, time.UTC)
	if pred(sat13) {
		t.Error("saturday_morning predicate should reject Saturday at exactly 13:00")
	}

	// Boundary: Saturday at 12:59:59 must match.
	sat1259 := time.Date(2026, time.May, 2, 12, 59, 59, 0, time.UTC)
	if !pred(sat1259) {
		t.Error("saturday_morning predicate should match Saturday 12:59:59")
	}
}

// TestSeasonalRecipeRegistered verifies the recipe is in the registry.
func TestSeasonalRecipeRegistered(t *testing.T) {
	_, ok := Get("seasonal_themed")
	if !ok {
		t.Fatal("seasonal_themed not registered")
	}
}

// TestSeasonalValidatesTheme verifies that an unknown theme is rejected and a
// known theme with mode "auto" is accepted.
func TestSeasonalValidatesTheme(t *testing.T) {
	rec, ok := Get("seasonal_themed")
	if !ok {
		t.Fatal("seasonal_themed not registered")
	}

	// Unknown theme must be rejected.
	badTheme := json.RawMessage(`{"theme":"unicorn"}`)
	if err := rec.Validate(badTheme); err == nil {
		t.Error("unknown theme should be rejected")
	}

	// Known theme with mode "auto" must be accepted.
	good := json.RawMessage(`{"theme":"halloween","mode":"auto"}`)
	if err := rec.Validate(good); err != nil {
		t.Errorf("valid params rejected: %v", err)
	}
}

// TestSeasonalValidatesMode verifies that only valid mode values are accepted.
func TestSeasonalValidatesMode(t *testing.T) {
	rec, ok := Get("seasonal_themed")
	if !ok {
		t.Fatal("seasonal_themed not registered")
	}

	cases := []struct {
		mode    string
		wantErr bool
	}{
		{"", false},       // empty string → defaults to "auto", valid
		{"auto", false},   // explicit auto
		{"pinned", false}, // pinned (always show in-season items)
		{"forced", true},  // unknown mode — rejected
	}

	for _, tc := range cases {
		raw := json.RawMessage(`{"theme":"halloween","mode":"` + tc.mode + `"}`)
		if tc.mode == "" {
			raw = json.RawMessage(`{"theme":"halloween"}`)
		}
		err := rec.Validate(raw)
		if tc.wantErr && err == nil {
			t.Errorf("mode=%q should be rejected", tc.mode)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("mode=%q should be accepted, got: %v", tc.mode, err)
		}
	}
}

// TestSeasonalRequiresTheme verifies that missing/empty theme is rejected.
func TestSeasonalRequiresTheme(t *testing.T) {
	rec, ok := Get("seasonal_themed")
	if !ok {
		t.Fatal("seasonal_themed not registered")
	}

	// Empty raw
	if err := rec.Validate(nil); err == nil {
		t.Error("nil raw should be rejected")
	}
	if err := rec.Validate(json.RawMessage(``)); err == nil {
		t.Error("empty raw should be rejected")
	}

	// Empty JSON object
	if err := rec.Validate(json.RawMessage(`{}`)); err == nil {
		t.Error("empty object should be rejected (theme required)")
	}

	// Explicit empty theme string
	if err := rec.Validate(json.RawMessage(`{"theme":""}`)); err == nil {
		t.Error(`{"theme":""} should be rejected`)
	}
}

// TestAllSeasonalPredicatesPresent verifies every theme referenced by a preset
// (in either the legacy single-theme form or the new EnabledThemes list) has a
// corresponding predicate in SeasonalPredicates.
func TestAllSeasonalPredicatesPresent(t *testing.T) {
	rec, ok := Get("seasonal_themed")
	if !ok {
		t.Fatal("seasonal_themed not registered")
	}

	for _, preset := range rec.Definition().Presets {
		var p SeasonalThemedParams
		if err := json.Unmarshal(preset.DefaultParams, &p); err != nil {
			t.Errorf("preset %q: failed to unmarshal DefaultParams: %v", preset.Key, err)
			continue
		}
		themes := append([]string{}, p.EnabledThemes...)
		if p.Theme != "" {
			themes = append(themes, p.Theme)
		}
		if len(themes) == 0 {
			t.Errorf("preset %q: neither enabled_themes nor theme set", preset.Key)
			continue
		}
		for _, th := range themes {
			if _, hasPred := SeasonalPredicates[th]; !hasPred {
				t.Errorf("preset %q: theme %q has no predicate in SeasonalPredicates", preset.Key, th)
			}
		}
	}
}

// TestActiveSeasonalThemePicksMatchingTheme verifies the auto-cycle resolver
// returns the highest-priority enabled theme whose predicate matches now.
func TestActiveSeasonalThemePicksMatchingTheme(t *testing.T) {
	enabled := []string{"halloween", "christmas", "saturday_morning"}

	// Mid-October Wednesday — only halloween fires.
	got := ActiveSeasonalTheme(enabled, time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC))
	if got != "halloween" {
		t.Errorf("Oct 14 → %q, want halloween", got)
	}

	// October Saturday at 10am — both halloween and saturday_morning fire;
	// halloween wins because it's earlier in SeasonalThemeOrder.
	got = ActiveSeasonalTheme(enabled, time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC))
	if got != "halloween" {
		t.Errorf("Oct 17 Sat 10am → %q, want halloween (priority over saturday_morning)", got)
	}

	// March Saturday at 10am — only saturday_morning fires.
	got = ActiveSeasonalTheme(enabled, time.Date(2026, 3, 7, 10, 0, 0, 0, time.UTC))
	if got != "saturday_morning" {
		t.Errorf("March 7 Sat 10am → %q, want saturday_morning", got)
	}

	// July 4 with only halloween and christmas enabled — no themes fire.
	// (Excluding saturday_morning here because July 4 2026 happens to be a
	// Saturday, which would otherwise match.)
	got = ActiveSeasonalTheme([]string{"halloween", "christmas"}, time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC))
	if got != "" {
		t.Errorf("July 4 with halloween/christmas enabled → %q, want empty", got)
	}
}

// TestActiveSeasonalThemeIgnoresDisabledThemes confirms a theme not in the
// enabled list never wins, even when its predicate matches.
func TestActiveSeasonalThemeIgnoresDisabledThemes(t *testing.T) {
	// Mid-October but halloween is NOT enabled — auto-cycle returns "".
	got := ActiveSeasonalTheme([]string{"christmas"}, time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC))
	if got != "" {
		t.Errorf("halloween disabled → got %q, want empty", got)
	}
}

// TestSeasonalTitleOverridePicksActiveThemeTitle verifies the helper returns
// the per-theme title configured for whichever theme is currently active.
func TestSeasonalTitleOverridePicksActiveThemeTitle(t *testing.T) {
	p := SeasonalThemedParams{
		EnabledThemes: []string{"halloween", "christmas"},
		ThemeTitles: map[string]string{
			"halloween": "Spooky Picks",
			"christmas": "Festive Films",
		},
	}

	// October — halloween fires.
	got := SeasonalTitleOverride(p, time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC), nil)
	if got != "Spooky Picks" {
		t.Errorf("Oct 14 → %q, want Spooky Picks", got)
	}

	// December — christmas fires.
	got = SeasonalTitleOverride(p, time.Date(2026, 12, 20, 12, 0, 0, 0, time.UTC), nil)
	if got != "Festive Films" {
		t.Errorf("Dec 20 → %q, want Festive Films", got)
	}

	// April — nothing fires, no override.
	got = SeasonalTitleOverride(p, time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC), nil)
	if got != "" {
		t.Errorf("Apr 15 → %q, want empty", got)
	}
}

// TestSeasonalTitleOverrideHandlesMissingEntry returns empty when the active
// theme has no title configured (the section's saved Title is the fallback).
func TestSeasonalTitleOverrideHandlesMissingEntry(t *testing.T) {
	p := SeasonalThemedParams{
		EnabledThemes: []string{"halloween", "christmas"},
		ThemeTitles:   map[string]string{"christmas": "Festive Films"},
	}
	got := SeasonalTitleOverride(p, time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC), nil)
	if got != "" {
		t.Errorf("halloween active but no entry → %q, want empty (caller falls back to section Title)", got)
	}
}

// TestSeasonalTitleOverrideLegacyMode returns the override only when the
// legacy theme's predicate fires.
func TestSeasonalTitleOverrideLegacyMode(t *testing.T) {
	p := SeasonalThemedParams{
		Theme:       "halloween",
		Mode:        "auto",
		ThemeTitles: map[string]string{"halloween": "Spooky"},
	}
	got := SeasonalTitleOverride(p, time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC), nil)
	if got != "Spooky" {
		t.Errorf("legacy in-season → %q, want Spooky", got)
	}
	got = SeasonalTitleOverride(p, time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC), nil)
	if got != "" {
		t.Errorf("legacy off-season → %q, want empty", got)
	}
}

// TestSeasonalValidatesEnabledThemes verifies the validator accepts the new
// multi-theme shape and rejects unknown theme keys.
func TestSeasonalValidatesEnabledThemes(t *testing.T) {
	rec, _ := Get("seasonal_themed")

	good := json.RawMessage(`{"enabled_themes":["halloween","christmas"]}`)
	if err := rec.Validate(good); err != nil {
		t.Errorf("good params rejected: %v", err)
	}

	bad := json.RawMessage(`{"enabled_themes":["halloween","made_up"]}`)
	if err := rec.Validate(bad); err == nil {
		t.Error("expected error for unknown theme in enabled_themes")
	}
}

// TestFamilyMovieNightPredicate pins the window: Friday and Saturday from 5pm,
// rejecting earlier hours and other weekdays.
func TestFamilyMovieNightPredicate(t *testing.T) {
	pred, ok := SeasonalPredicates["family_movie_night"]
	if !ok {
		t.Fatal("family_movie_night predicate not found")
	}

	// 2026-04-10 is a Friday, 2026-04-11 a Saturday, 2026-04-12 a Sunday.
	friEvening := time.Date(2026, time.April, 10, 19, 0, 0, 0, time.UTC)
	if !pred(friEvening) {
		t.Error("should match Friday 7pm")
	}
	satFivePM := time.Date(2026, time.April, 11, 17, 0, 0, 0, time.UTC)
	if !pred(satFivePM) {
		t.Error("should match Saturday 5pm (inclusive)")
	}
	friAfternoon := time.Date(2026, time.April, 10, 16, 59, 0, 0, time.UTC)
	if pred(friAfternoon) {
		t.Error("should reject Friday 4:59pm")
	}
	sunEvening := time.Date(2026, time.April, 12, 19, 0, 0, 0, time.UTC)
	if pred(sunEvening) {
		t.Error("should reject Sunday evening")
	}
}

// TestActiveSeasonalThemeWhereSkipsUnusable verifies that an in-season theme
// failing the usable filter yields to a lower-priority in-season theme instead
// of blacking out the section — the December regression where an enabled
// christmas theme without query support suppressed halloween/saturday rails.
func TestActiveSeasonalThemeWhereSkipsUnusable(t *testing.T) {
	enabled := []string{"christmas", "saturday_morning"}
	// A Saturday morning in December: christmas outranks saturday_morning.
	saturdayInDecember := time.Date(2026, time.December, 12, 9, 0, 0, 0, time.UTC)

	got := ActiveSeasonalThemeWhere(enabled, saturdayInDecember, nil)
	if got != "christmas" {
		t.Fatalf("nil filter should pick christmas, got %q", got)
	}

	noChristmas := func(theme string) bool { return theme != "christmas" }
	got = ActiveSeasonalThemeWhere(enabled, saturdayInDecember, noChristmas)
	if got != "saturday_morning" {
		t.Errorf("filtered selection should fall through to saturday_morning, got %q", got)
	}

	nothingUsable := func(string) bool { return false }
	if got := ActiveSeasonalThemeWhere(enabled, saturdayInDecember, nothingUsable); got != "" {
		t.Errorf("expected no theme when nothing is usable, got %q", got)
	}
}

// TestSeasonalTitleOverrideHonorsUsableFilter verifies the title override
// follows the same filtered selection as the fetcher: when the top-priority
// in-season theme is unusable, the override comes from the theme that
// actually resolves.
func TestSeasonalTitleOverrideHonorsUsableFilter(t *testing.T) {
	p := SeasonalThemedParams{
		EnabledThemes: []string{"christmas", "saturday_morning"},
		ThemeTitles: map[string]string{
			"christmas":        "Christmas Movies",
			"saturday_morning": "Saturday Cartoons",
		},
	}
	saturdayInDecember := time.Date(2026, time.December, 12, 9, 0, 0, 0, time.UTC)

	if got := SeasonalTitleOverride(p, saturdayInDecember, nil); got != "Christmas Movies" {
		t.Errorf("nil filter: got %q, want Christmas Movies", got)
	}
	noChristmas := func(theme string) bool { return theme != "christmas" }
	if got := SeasonalTitleOverride(p, saturdayInDecember, noChristmas); got != "Saturday Cartoons" {
		t.Errorf("filtered: got %q, want Saturday Cartoons", got)
	}
}
