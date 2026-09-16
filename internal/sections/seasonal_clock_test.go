package sections

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/sections/recipes"
)

// halloweenSeasonalSection builds a ResolvedSection configured for the
// halloween theme in auto mode.
func halloweenSeasonalSection() ResolvedSection {
	cfg, _ := json.Marshal(recipes.SeasonalThemedParams{Theme: "halloween", Mode: "auto"})
	return ResolvedSection{
		ID:          "test-halloween",
		SectionType: SectionSeasonalThemed,
		Title:       "Halloween Picks",
		Config:      cfg,
		ItemLimit:   10,
	}
}

// TestSeasonalThemedSuppressesOffSeason verifies that an auto-mode seasonal
// section returns no items when the injected clock is outside the theme's
// date window. This exercises the off-season branch of fetchSeasonalThemed
// without needing a database — the early return runs before any SQL.
func TestSeasonalThemedSuppressesOffSeason(t *testing.T) {
	f := &Fetcher{
		Clock: recipes.FixedClock(time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)), // Independence Day, well clear of October
	}
	items, total, err := f.fetchSection(
		context.Background(),
		halloweenSeasonalSection(),
		nil, nil, 0, "",
		catalog.AccessFilter{},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 0 || total != 0 {
		t.Errorf("expected empty result off-season; got %d items, total %d", len(items), total)
	}
}

// TestSeasonalThemedPinnedModeAttemptsQuery verifies that mode=pinned
// bypasses the off-season suppression. A pinned section in July should
// proceed past the clock check and attempt the query (which will fail with
// nil pool — we assert the right kind of failure to confirm the path was reached).
func TestSeasonalThemedPinnedModeAttemptsQuery(t *testing.T) {
	f := &Fetcher{
		Clock: recipes.FixedClock(time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)),
	}
	cfg, _ := json.Marshal(recipes.SeasonalThemedParams{Theme: "halloween", Mode: "pinned"})
	s := ResolvedSection{
		ID:          "test-halloween-pinned",
		SectionType: SectionSeasonalThemed,
		Config:      cfg,
		ItemLimit:   10,
	}

	var reached bool
	func() {
		defer func() {
			if r := recover(); r != nil {
				// Panic from nil pool confirms we got past the off-season check.
				reached = true
			}
		}()
		_, _, err := f.fetchSection(
			context.Background(),
			s,
			nil, nil, 0, "",
			catalog.AccessFilter{},
		)
		if err != nil {
			// Non-nil error from nil pool also confirms the SQL path was reached.
			reached = true
		}
	}()

	if !reached {
		t.Fatal("pinned mode should have attempted the query (reached SQL path); clean nil result suggests off-season suppression triggered despite mode=pinned")
	}
}

// TestSeasonalThemedInSeasonAttemptsQuery confirms that auto mode in October
// bypasses the off-season check and attempts the query.
func TestSeasonalThemedInSeasonAttemptsQuery(t *testing.T) {
	f := &Fetcher{
		Clock: recipes.FixedClock(time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)), // mid-October
	}

	var reached bool

	func() {
		defer func() {
			if r := recover(); r != nil {
				// Panic from nil pool confirms we got past the off-season check.
				reached = true
			}
		}()
		_, _, err := f.fetchSection(
			context.Background(),
			halloweenSeasonalSection(),
			nil, nil, 0, "",
			catalog.AccessFilter{},
		)
		if err != nil {
			// Non-nil error from nil pool also confirms the SQL path was reached.
			reached = true
		}
	}()

	if !reached {
		t.Fatal("in-season auto mode should have attempted the query (reached SQL path); clean nil result suggests off-season suppression triggered incorrectly")
	}
}

// TestSeasonalTitleOverrideWithoutCustomTitles verifies the fetcher resolves
// the in-season theme's default label when theme_titles is omitted, and that it
// leaves a section the admin renamed alone.
func TestSeasonalTitleOverrideWithoutCustomTitles(t *testing.T) {
	f := &Fetcher{
		Clock: recipes.FixedClock(time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)),
	}
	section := ResolvedSection{
		SectionType: SectionSeasonalThemed,
		Title:       recipes.SeasonalPicksTitle,
		Config:      json.RawMessage(`{"enabled_themes":["summer_blockbuster"]}`),
	}

	theme := f.inSeasonTheme(section)
	if theme != "summer_blockbuster" {
		t.Fatalf("inSeasonTheme() = %q, want summer_blockbuster", theme)
	}
	if got := f.seasonalTitleOverride(section, theme); got != "Summer Blockbusters" {
		t.Fatalf("seasonalTitleOverride() = %q, want Summer Blockbusters", got)
	}

	renamed := section
	renamed.Title = "Mom's Picks"
	if got := f.seasonalTitleOverride(renamed, theme); got != "" {
		t.Fatalf("renamed section = %q, want empty so the stored title stands", got)
	}
}

// TestInSeasonThemeOffSeason verifies the fetcher reports no theme outside every
// enabled window, which both leaves the title alone and keeps the off-season
// cache entry separate from the in-season one.
func TestInSeasonThemeOffSeason(t *testing.T) {
	f := &Fetcher{
		Clock: recipes.FixedClock(time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)),
	}
	section := ResolvedSection{
		SectionType: SectionSeasonalThemed,
		Title:       recipes.SeasonalPicksTitle,
		Config:      json.RawMessage(`{"enabled_themes":["halloween","christmas"]}`),
	}

	if got := f.inSeasonTheme(section); got != "" {
		t.Fatalf("inSeasonTheme() = %q, want empty in April", got)
	}
	if got := f.seasonalTitleOverride(section, ""); got != "" {
		t.Fatalf("seasonalTitleOverride() = %q, want empty off-season", got)
	}
}

// TestInSeasonThemeIgnoresPinnedLegacyMode verifies a legacy pinned section,
// which renders its theme's items year-round, is not treated as in season: the
// section keeps its own title outside the theme's window.
func TestInSeasonThemeIgnoresPinnedLegacyMode(t *testing.T) {
	f := &Fetcher{
		Clock: recipes.FixedClock(time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)),
	}
	section := ResolvedSection{
		SectionType: SectionSeasonalThemed,
		Title:       recipes.SeasonalPicksTitle,
		Config:      json.RawMessage(`{"theme":"christmas","mode":"pinned"}`),
	}

	if got := f.inSeasonTheme(section); got != "" {
		t.Fatalf("inSeasonTheme() = %q, want empty for a pinned section in April", got)
	}
}
