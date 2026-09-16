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

// boundaryClock returns the last second of October on its first read and
// December on every read after it, standing in for a request that starts just
// before a theme window closes and reaches the query just after.
type boundaryClock struct{ reads int }

func (c *boundaryClock) Now() time.Time {
	c.reads++
	if c.reads == 1 {
		return time.Date(2026, 10, 31, 23, 59, 59, 0, time.UTC)
	}
	return time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
}

// TestSeasonalFetchPinsOneInstant verifies every clock-dependent step of one
// fetch reads the same instant. The section enables halloween alone, so a
// second clock read would resolve no theme and suppress the section — while the
// cache key and title, built from the first read, still said halloween. That
// mismatch would file an empty list under halloween's key for the rest of its
// TTL.
func TestSeasonalFetchPinsOneInstant(t *testing.T) {
	clock := &boundaryClock{}
	f := &Fetcher{Clock: clock}
	section := ResolvedSection{
		SectionType: SectionSeasonalThemed,
		Title:       recipes.SeasonalPicksTitle,
		Config:      json.RawMessage(`{"enabled_themes":["halloween"]}`),
	}

	// Stands in for FetchOne's pin, which the fetch path below must reuse.
	section.fetchNow = f.now()

	theme := f.inSeasonTheme(section)
	if theme != "halloween" {
		t.Fatalf("inSeasonTheme() = %q, want halloween from the pinned instant", theme)
	}
	if got := f.seasonalTitleOverride(section, theme); got != "Halloween" {
		t.Fatalf("seasonalTitleOverride() = %q, want Halloween", got)
	}

	// fetchSeasonalThemed reaches the query for the pinned theme instead of
	// suppressing the section. The nil pool makes the SQL attempt observable.
	var reached bool
	func() {
		defer func() {
			if recover() != nil {
				reached = true
			}
		}()
		_, _, err := f.fetchSection(
			context.Background(),
			section,
			nil, nil, 0, "",
			catalog.AccessFilter{},
		)
		if err != nil {
			reached = true
		}
	}()
	if !reached {
		t.Fatal("pinned halloween theme should have reached the query; a clean empty result means the fetch re-read the clock and found itself off-season")
	}
	if clock.reads != 1 {
		t.Fatalf("clock read %d times, want exactly 1 for the whole fetch", clock.reads)
	}
}
