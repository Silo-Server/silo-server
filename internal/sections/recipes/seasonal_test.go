package recipes

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/collectionutil"
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

// TestActiveSeasonalThemePicksMatchingTheme verifies the auto-cycle resolver
// returns the highest-priority enabled theme whose predicate matches now.
func TestActiveSeasonalThemePicksMatchingTheme(t *testing.T) {
	enabled := []string{"halloween", "christmas", "saturday_morning"}

	// Mid-October Wednesday — only halloween fires.
	got := (SeasonalThemedParams{EnabledThemes: enabled}).ActiveTheme(time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC))
	if got != "halloween" {
		t.Errorf("Oct 14 → %q, want halloween", got)
	}

	// October Saturday at 10am — both halloween and saturday_morning fire;
	// halloween wins because it's earlier in SeasonalThemeOrder.
	got = (SeasonalThemedParams{EnabledThemes: enabled}).ActiveTheme(time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC))
	if got != "halloween" {
		t.Errorf("Oct 17 Sat 10am → %q, want halloween (priority over saturday_morning)", got)
	}

	// March Saturday at 10am — only saturday_morning fires.
	got = (SeasonalThemedParams{EnabledThemes: enabled}).ActiveTheme(time.Date(2026, 3, 7, 10, 0, 0, 0, time.UTC))
	if got != "saturday_morning" {
		t.Errorf("March 7 Sat 10am → %q, want saturday_morning", got)
	}

	// July 4 with only halloween and christmas enabled — no themes fire.
	// (Excluding saturday_morning here because July 4 2026 happens to be a
	// Saturday, which would otherwise match.)
	got = (SeasonalThemedParams{EnabledThemes: []string{"halloween", "christmas"}}).ActiveTheme(time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC))
	if got != "" {
		t.Errorf("July 4 with halloween/christmas enabled → %q, want empty", got)
	}
}

// TestActiveSeasonalThemeIgnoresDisabledThemes confirms a theme not in the
// enabled list never wins, even when its predicate matches.
func TestActiveSeasonalThemeIgnoresDisabledThemes(t *testing.T) {
	// Mid-October but halloween is NOT enabled — auto-cycle returns "".
	got := (SeasonalThemedParams{EnabledThemes: []string{"christmas"}}).ActiveTheme(time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC))
	if got != "" {
		t.Errorf("halloween disabled → got %q, want empty", got)
	}
}

// TestActiveSeasonalPick pins what a row shows and what it is called: the
// in-season theme's chosen vibe (else its default), titled by theme_titles,
// else by the vibe, with custom holidays ahead of built-in themes.
func TestActiveSeasonalPick(t *testing.T) {
	halloween, _ := SeasonalThemeByKey("halloween")
	familyHalloween := halloween.Vibe("family")
	oct14 := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	july2 := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	jan1 := time.Date(2027, 1, 1, 12, 0, 0, 0, time.UTC)
	fourth := SeasonalCustomHoliday{Name: "Fourth of July", Start: "07-01", End: "07-04", ListURL: "https://mdblist.com/lists/a/fourth"}
	winter := SeasonalCustomHoliday{Name: "Winter Break", Title: "Snow Days", Start: "12-20", End: "01-05", ListURL: "https://mdblist.com/lists/a/winter"}

	cases := []struct {
		name   string
		p      SeasonalThemedParams
		now    time.Time
		want   SeasonalPick
		wantOK bool
	}{
		{
			name:   "default vibe titles the row",
			p:      SeasonalThemedParams{EnabledThemes: []string{"halloween"}},
			now:    oct14,
			want:   SeasonalPick{Theme: "halloween", Vibe: halloween.Vibes[0].Key, ListURL: halloween.Vibes[0].ListURL, Title: halloween.Vibes[0].Title},
			wantOK: true,
		},
		{
			name:   "chosen vibe",
			p:      SeasonalThemedParams{EnabledThemes: []string{"halloween"}, Vibes: map[string]string{"halloween": "family"}},
			now:    oct14,
			want:   SeasonalPick{Theme: "halloween", Vibe: "family", ListURL: familyHalloween.ListURL, Title: familyHalloween.Title},
			wantOK: true,
		},
		{
			name:   "theme title overrides the vibe title",
			p:      SeasonalThemedParams{EnabledThemes: []string{"halloween"}, Vibes: map[string]string{"halloween": "family"}, ThemeTitles: map[string]string{"halloween": "Spooky Picks"}},
			now:    oct14,
			want:   SeasonalPick{Theme: "halloween", Vibe: "family", ListURL: familyHalloween.ListURL, Title: "Spooky Picks"},
			wantOK: true,
		},
		{
			name:   "custom holiday outranks a built-in theme",
			p:      SeasonalThemedParams{EnabledThemes: []string{"summer_blockbuster"}, CustomHolidays: []SeasonalCustomHoliday{fourth}},
			now:    july2,
			want:   SeasonalPick{ListURL: fourth.ListURL, Title: "Fourth of July"},
			wantOK: true,
		},
		{
			name:   "custom holiday wraps across New Year and uses its title",
			p:      SeasonalThemedParams{CustomHolidays: []SeasonalCustomHoliday{winter}},
			now:    jan1,
			want:   SeasonalPick{ListURL: winter.ListURL, Title: "Snow Days"},
			wantOK: true,
		},
		{
			name: "custom holidays only, none in season",
			p:    SeasonalThemedParams{CustomHolidays: []SeasonalCustomHoliday{fourth}},
			now:  oct14,
		},
		{
			name:   "legacy theme still shows beside a custom holiday out of season",
			p:      SeasonalThemedParams{Theme: "halloween", CustomHolidays: []SeasonalCustomHoliday{fourth}},
			now:    oct14,
			want:   SeasonalPick{Theme: "halloween", Vibe: halloween.Vibes[0].Key, ListURL: halloween.Vibes[0].ListURL, Title: halloween.Vibes[0].Title},
			wantOK: true,
		},
		{
			name: "off season",
			p:    SeasonalThemedParams{EnabledThemes: []string{"christmas"}},
			now:  oct14,
		},
		{
			name: "legacy auto row off season",
			p:    SeasonalThemedParams{Theme: "halloween", Mode: "auto"},
			now:  july2,
		},
		{
			name:   "legacy pinned row shows off season",
			p:      SeasonalThemedParams{Theme: "halloween", Mode: "pinned"},
			now:    july2,
			want:   SeasonalPick{Theme: "halloween", Vibe: halloween.Vibes[0].Key, ListURL: halloween.Vibes[0].ListURL, Title: halloween.Vibes[0].Title},
			wantOK: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ActiveSeasonalPick(tc.p, tc.now)
			if ok != tc.wantOK || got != tc.want {
				t.Fatalf("ActiveSeasonalPick = %+v, %v; want %+v, %v", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// TestActiveSeasonalPickHonorsThemeDates covers a row that moves a built-in
// holiday to dates of its own.
func TestActiveSeasonalPickHonorsThemeDates(t *testing.T) {
	halloween, _ := SeasonalThemeByKey("halloween")
	christmas, _ := SeasonalThemeByKey("christmas")
	oct14 := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	july2 := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name   string
		p      SeasonalThemedParams
		now    time.Time
		want   SeasonalPick
		wantOK bool
	}{
		{
			name: "own dates move a theme out of its default window",
			p:    SeasonalThemedParams{EnabledThemes: []string{"halloween"}, ThemeDates: map[string]SeasonalDates{"halloween": {Start: "10-20", End: "11-02"}}},
			now:  oct14,
		},
		{
			name:   "own dates move a theme into season",
			p:      SeasonalThemedParams{EnabledThemes: []string{"christmas", "summer_blockbuster"}, ThemeDates: map[string]SeasonalDates{"christmas": {Start: "07-01", End: "07-31"}}},
			now:    july2,
			want:   SeasonalPick{Theme: "christmas", Vibe: christmas.Vibes[0].Key, ListURL: christmas.Vibes[0].ListURL, Title: christmas.Vibes[0].Title},
			wantOK: true,
		},
		{
			name:   "own dates on a legacy single-theme row",
			p:      SeasonalThemedParams{Theme: "halloween", ThemeDates: map[string]SeasonalDates{"halloween": {Start: "06-25", End: "07-05"}}},
			now:    july2,
			want:   SeasonalPick{Theme: "halloween", Vibe: halloween.Vibes[0].Key, ListURL: halloween.Vibes[0].ListURL, Title: halloween.Vibes[0].Title},
			wantOK: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ActiveSeasonalPick(tc.p, tc.now)
			if ok != tc.wantOK || got != tc.want {
				t.Fatalf("ActiveSeasonalPick = %+v, %v; want %+v, %v", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// TestSeasonalValidatesVibesSourceAndCustomHolidays covers the config the
// editor saves beyond the theme list.
func TestSeasonalValidatesVibesSourceAndCustomHolidays(t *testing.T) {
	rec, _ := Get("seasonal_themed")
	holiday := func(start, end, listURL string) string {
		return `{"custom_holidays":[{"name":"Fourth","start":"` + start + `","end":"` + end + `","list_url":"` + listURL + `"}]}`
	}
	cases := []struct {
		name    string
		config  string
		wantErr bool
	}{
		{"known vibe", `{"enabled_themes":["halloween"],"vibes":{"halloween":"horror"}}`, false},
		{"unknown vibe", `{"enabled_themes":["halloween"],"vibes":{"halloween":"cozy"}}`, true},
		{"vibe for an unknown theme", `{"enabled_themes":["halloween"],"vibes":{"arbor_day":"favorites"}}`, true},
		{"lists only", `{"enabled_themes":["halloween"],"source":"lists"}`, false},
		{"unknown source", `{"enabled_themes":["halloween"],"source":"tmdb"}`, true},
		{"custom holiday alone", holiday("07-01", "07-04", "https://mdblist.com/lists/a/b"), false},
		{"leap day", holiday("02-29", "03-01", "https://mdblist.com/lists/a/b"), false},
		{"impossible date", holiday("02-30", "03-01", "https://mdblist.com/lists/a/b"), true},
		{"date not MM-DD", holiday("7-1", "07-04", "https://mdblist.com/lists/a/b"), true},
		{"trailing junk", holiday("07-0x", "07-04", "https://mdblist.com/lists/a/b"), true},
		{"not an MDBList list", holiday("07-01", "07-04", "https://example.com/lists/a/b"), true},
		{"own dates", `{"enabled_themes":["easter"],"theme_dates":{"easter":{"start":"04-01","end":"04-14"}}}`, false},
		{"own dates wrapping New Year", `{"enabled_themes":["christmas"],"theme_dates":{"christmas":{"start":"11-25","end":"01-06"}}}`, false},
		{"own dates missing the end", `{"enabled_themes":["christmas"],"theme_dates":{"christmas":{"start":"11-25"}}}`, true},
		{"own dates for an unknown theme", `{"enabled_themes":["christmas"],"theme_dates":{"arbor_day":{"start":"04-01","end":"04-02"}}}`, true},
		{"own dates for a weekly theme", `{"enabled_themes":["saturday_morning"],"theme_dates":{"saturday_morning":{"start":"04-01","end":"04-02"}}}`, true},
		{"custom holiday without a name", `{"custom_holidays":[{"name":" ","start":"07-01","end":"07-04","list_url":"https://mdblist.com/lists/a/b"}]}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := rec.Validate(json.RawMessage(tc.config))
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate(%s) error = %v, wantErr %v", tc.config, err, tc.wantErr)
			}
		})
	}
}

// TestMovableHolidayWindows pins the windows computed rather than fixed:
// Easter week (Palm Sunday to Easter Monday) and New Year's across Jan 1.
func TestMovableHolidayWindows(t *testing.T) {
	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 12, 0, 0, 0, time.UTC) }
	cases := []struct {
		theme string
		at    time.Time
		want  bool
	}{
		// Easter 2027 is March 28; 2026 is April 5.
		{"easter", day(2027, time.March, 20), false},
		{"easter", day(2027, time.March, 21), true},
		{"easter", day(2027, time.March, 29), true},
		{"easter", day(2027, time.March, 30), false},
		{"easter", day(2026, time.April, 5), true},
		{"new_year", day(2026, time.December, 25), false},
		{"new_year", day(2026, time.December, 31), true},
		{"new_year", day(2027, time.January, 1), true},
		{"new_year", day(2027, time.January, 2), false},
	}
	for _, tc := range cases {
		if got := SeasonalPredicates[tc.theme](tc.at); got != tc.want {
			t.Errorf("%s at %s = %v, want %v", tc.theme, tc.at.Format("2006-01-02"), got, tc.want)
		}
	}
}

// TestSeasonalCatalogIsUsable guards what configs and the refresher rely on:
// every theme can be in season, has a default vibe, keeps vibe keys unique,
// and points at a list the MDBList fetcher accepts.
func TestSeasonalCatalogIsUsable(t *testing.T) {
	for _, theme := range SeasonalThemes {
		if _, ok := SeasonalPredicates[theme.Key]; !ok {
			t.Errorf("theme %q has no date predicate", theme.Key)
		}
		if theme.Weekly && theme.Start != "" {
			t.Errorf("weekly theme %q has a yearly window", theme.Key)
		}
		if theme.Start != "" || theme.End != "" {
			if _, _, ok := parseMonthDay(theme.Start); !ok {
				t.Errorf("theme %q start %q is not MM-DD", theme.Key, theme.Start)
			}
			if _, _, ok := parseMonthDay(theme.End); !ok {
				t.Errorf("theme %q end %q is not MM-DD", theme.Key, theme.End)
			}
		}
		if len(theme.Vibes) == 0 {
			t.Errorf("theme %q has no vibes", theme.Key)
		}
		seen := map[string]bool{}
		for _, vibe := range theme.Vibes {
			if seen[vibe.Key] {
				t.Errorf("theme %q repeats vibe %q", theme.Key, vibe.Key)
			}
			seen[vibe.Key] = true
			if _, err := collectionutil.CanonicalMDBListURL(vibe.ListURL); err != nil {
				t.Errorf("theme %q vibe %q list %q: %v", theme.Key, vibe.Key, vibe.ListURL, err)
			}
			if vibe.Name == "" || vibe.Title == "" {
				t.Errorf("theme %q vibe %q needs a name and title", theme.Key, vibe.Key)
			}
		}
	}
	if err := (seasonalRecipe{}).Validate(json.RawMessage(DefaultSeasonalConfig)); err != nil {
		t.Errorf("DefaultSeasonalConfig is invalid: %v", err)
	}
}

// TestSeasonalThemeOrders keeps the two orders apart: the catalog runs by
// date for the editor, and SeasonalThemeOrder ranks every theme exactly once.
func TestSeasonalThemeOrders(t *testing.T) {
	ranked := map[string]int{}
	for _, key := range SeasonalThemeOrder {
		ranked[key]++
	}
	for _, theme := range SeasonalThemes {
		if ranked[theme.Key] != 1 {
			t.Errorf("theme %q appears %d times in SeasonalThemeOrder", theme.Key, ranked[theme.Key])
		}
	}
	if len(SeasonalThemeOrder) != len(SeasonalThemes) {
		t.Errorf("SeasonalThemeOrder has %d themes, catalog %d", len(SeasonalThemeOrder), len(SeasonalThemes))
	}

	// Dated themes by usual start (Easter, which moves, sits between March
	// and June), then the weekly ones.
	usualStart := func(theme SeasonalTheme) string {
		if theme.Key == ThemeEaster {
			return "04-01"
		}
		if theme.Weekly {
			return "13-01"
		}
		return theme.Start
	}
	for i := 1; i < len(SeasonalThemes); i++ {
		prev, next := SeasonalThemes[i-1], SeasonalThemes[i]
		if usualStart(prev) > usualStart(next) {
			t.Errorf("catalog lists %q before %q, out of calendar order", prev.Key, next.Key)
		}
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
