package recipes

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// DatePredicate is a function that reports whether a given time falls within
// the seasonal window.
type DatePredicate func(time.Time) bool

// inMonth returns a predicate that matches any time within the given month.
func inMonth(m time.Month) DatePredicate {
	return func(t time.Time) bool {
		return t.Month() == m
	}
}

// inMonths returns a predicate that matches any time within any of the given months.
func inMonths(months ...time.Month) DatePredicate {
	set := make(map[time.Month]bool, len(months))
	for _, m := range months {
		set[m] = true
	}
	return func(t time.Time) bool {
		return set[t.Month()]
	}
}

// dateRange returns a predicate that matches times inclusively between
// (startMonth, startDay) and (endMonth, endDay) within the same calendar year.
// It does not wrap across year boundaries.
func dateRange(startMonth time.Month, startDay int, endMonth time.Month, endDay int) DatePredicate {
	return func(t time.Time) bool {
		y := t.Year()
		start := time.Date(y, startMonth, startDay, 0, 0, 0, 0, t.Location())
		end := time.Date(y, endMonth, endDay, 23, 59, 59, 999999999, t.Location())
		return !t.Before(start) && !t.After(end)
	}
}

// weekdayBeforeHour returns a predicate that matches times on the given weekday
// strictly before the given hour (24-hour, e.g. 13 for 1pm).
func weekdayBeforeHour(weekday time.Weekday, hour int) DatePredicate {
	return func(t time.Time) bool {
		return t.Weekday() == weekday && t.Hour() < hour
	}
}

// weekdaysFromHour returns a predicate that matches times on any of the given
// weekdays at or after the given hour (24-hour, e.g. 17 for 5pm).
func weekdaysFromHour(hour int, weekdays ...time.Weekday) DatePredicate {
	set := make(map[time.Weekday]bool, len(weekdays))
	for _, d := range weekdays {
		set[d] = true
	}
	return func(t time.Time) bool {
		return set[t.Weekday()] && t.Hour() >= hour
	}
}

// SeasonalPredicates maps theme names to their date predicates.
// Themes without a predicate here are not supported.
var SeasonalPredicates = map[string]DatePredicate{
	"halloween":          inMonth(time.October),
	"christmas":          dateRange(time.December, 1, time.December, 31),
	"valentines":         dateRange(time.February, 7, time.February, 14),
	"st_patricks":        dateRange(time.March, 15, time.March, 17),
	"thanksgiving":       dateRange(time.November, 22, time.November, 30),
	"summer":             inMonths(time.June, time.July, time.August),
	"summer_blockbuster": inMonths(time.June, time.July, time.August),
	"saturday_morning":   weekdayBeforeHour(time.Saturday, 13),
	"family_movie_night": weekdaysFromHour(17, time.Friday, time.Saturday),
}

// SeasonalThemeOrder lists themes from most-specific to most-general. When
// multiple enabled themes match `now` (e.g. a Saturday morning in October
// triggers both halloween and saturday_morning), the earlier entry wins.
// Themes not listed here cannot participate in EnabledThemes auto-cycle.
var SeasonalThemeOrder = []string{
	"valentines",
	"st_patricks",
	"thanksgiving",
	"christmas",
	"halloween",
	"saturday_morning",
	"family_movie_night",
	"summer_blockbuster",
	"summer",
}

// SeasonalThemedParams configures the seasonal_themed resolver.
//
// There are two modes of use:
//
//  1. Multi-theme auto-cycle (preferred): set EnabledThemes to the list of
//     themes the admin wants this section to rotate through. The fetcher picks
//     the first SeasonalThemeOrder entry whose predicate matches now and uses
//     it; if none match, the section is empty (suppressed off-season).
//
//  2. Legacy single-theme: set Theme to a single theme key. Mode controls
//     whether the section is hidden off-season (auto, default) or pinned.
//     Retained for backward compat with sections saved before the multi-theme
//     shape; new gallery saves should use EnabledThemes.
//
// EnabledThemes takes precedence when both fields are populated.
//
// ThemeTitles is an optional per-theme display-name override. When the active
// theme has a non-empty entry here, the API replaces the section's saved Title
// with it for the duration of the in-season window — letting one section read
// "Halloween Picks" in October and "Christmas Movies" in December.
type SeasonalThemedParams struct {
	EnabledThemes []string          `json:"enabled_themes,omitempty"`
	ThemeTitles   map[string]string `json:"theme_titles,omitempty"`

	// Legacy single-theme fields. Still honored when EnabledThemes is empty.
	Theme string `json:"theme,omitempty"`
	Mode  string `json:"mode,omitempty"`
}

var validSeasonalModes = map[string]bool{
	"":       true, // valid; treated as "auto"
	"auto":   true,
	"pinned": true,
}

type seasonalRecipe struct{}

func (seasonalRecipe) Type() string                   { return "seasonal_themed" }
func (seasonalRecipe) NewParams() any                 { return &SeasonalThemedParams{} }
func (seasonalRecipe) DefaultCacheTTL() time.Duration { return 24 * time.Hour }

func (seasonalRecipe) Resolve(rc ResolverContext) (ResolvedItems, error) {
	return delegateResolve("seasonal_themed", rc)
}

func (seasonalRecipe) Validate(raw json.RawMessage) error {
	if len(raw) == 0 {
		return errors.New("seasonal_themed: enabled_themes or theme is required")
	}
	var p SeasonalThemedParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	// Multi-theme mode wins when populated.
	if len(p.EnabledThemes) > 0 {
		for _, t := range p.EnabledThemes {
			if _, ok := SeasonalPredicates[t]; !ok {
				return fmt.Errorf("seasonal_themed: unknown theme %q in enabled_themes", t)
			}
		}
		// Mode is ignored in multi-theme mode (the off-season behaviour is
		// implicit) but still validated if the caller sent something nonsense.
		if !validSeasonalModes[p.Mode] {
			return fmt.Errorf("seasonal_themed: invalid mode %q (want auto|pinned)", p.Mode)
		}
		return nil
	}
	// Legacy single-theme path.
	if p.Theme == "" {
		return errors.New("seasonal_themed: enabled_themes or theme is required")
	}
	if _, ok := SeasonalPredicates[p.Theme]; !ok {
		return fmt.Errorf("seasonal_themed: unknown theme %q", p.Theme)
	}
	if !validSeasonalModes[p.Mode] {
		return fmt.Errorf("seasonal_themed: invalid mode %q (want auto|pinned)", p.Mode)
	}
	return nil
}

func (seasonalRecipe) Definition() RecipeDefinition {
	return RecipeDefinition{
		Type:             "seasonal_themed",
		Category:         CategorySeasonal,
		SupportsRotation: false,
		AvoidDuplicates:  false,
		Presets: []GalleryPreset{
			{
				Key:              "se_auto",
				DisplayName:      "Seasonal Picks",
				Icon:             "🗓️",
				DescriptionShort: "Cycles automatically — Halloween in October, Christmas in December, etc.",
				DescriptionLong:  "One section that swaps in different theme picks based on the calendar. Toggle which holidays this profile celebrates from the Add section dialog.",
				DefaultParams: json.RawMessage(
					`{"enabled_themes":["halloween","christmas","valentines","st_patricks","thanksgiving","summer_blockbuster","saturday_morning"]}`,
				),
			},
			{
				Key:              "se_family_movie_night",
				DisplayName:      "Family Movie Night",
				Icon:             "🍿",
				DescriptionShort: "Family picks on Friday and Saturday evenings.",
				DefaultParams:    json.RawMessage(`{"enabled_themes":["family_movie_night"]}`),
			},
		},
	}
}

// SeasonalTitleOverride returns the per-theme display name for the active
// theme, or "" when no override is configured (or no theme is active). The
// fetcher applies the override only when non-empty, so callers can fall back
// to the section's saved Title. The usable filter must match the one passed
// to ActiveSeasonalThemeWhere so the override tracks the theme that actually
// resolved; pass nil to consider every theme usable.
func SeasonalTitleOverride(p SeasonalThemedParams, now time.Time, usable func(theme string) bool) string {
	var theme string
	switch {
	case len(p.EnabledThemes) > 0:
		theme = ActiveSeasonalThemeWhere(p.EnabledThemes, now, usable)
	case p.Theme != "":
		// Legacy mode — only honour an override if the predicate currently fires.
		pred, ok := SeasonalPredicates[p.Theme]
		if ok && pred(now) {
			theme = p.Theme
		}
	}
	if theme == "" {
		return ""
	}
	return p.ThemeTitles[theme]
}

// ActiveSeasonalTheme returns the highest-priority enabled theme whose
// predicate matches `now`. Returns "" when no enabled theme is in season.
// Themes not in SeasonalThemeOrder are skipped silently.
func ActiveSeasonalTheme(enabled []string, now time.Time) string {
	return ActiveSeasonalThemeWhere(enabled, now, nil)
}

// ActiveSeasonalThemeWhere is ActiveSeasonalTheme with an extra usable filter:
// an in-season theme that fails the filter is skipped so a lower-priority
// in-season theme can win instead. The fetcher passes "has an executable
// query" here so a theme without backing data never blacks out the section
// during its own window (e.g. an enabled theme whose query support hasn't
// landed yet outranking one that works). A nil filter accepts every theme.
func ActiveSeasonalThemeWhere(enabled []string, now time.Time, usable func(theme string) bool) string {
	if len(enabled) == 0 {
		return ""
	}
	enabledSet := make(map[string]bool, len(enabled))
	for _, t := range enabled {
		enabledSet[t] = true
	}
	for _, t := range SeasonalThemeOrder {
		if !enabledSet[t] {
			continue
		}
		if usable != nil && !usable(t) {
			continue
		}
		pred, ok := SeasonalPredicates[t]
		if !ok {
			continue
		}
		if pred(now) {
			return t
		}
	}
	return ""
}

func init() {
	Register(seasonalRecipe{})
}
