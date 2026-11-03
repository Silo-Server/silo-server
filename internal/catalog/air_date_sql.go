package catalog

import "fmt"

// effectiveLastAirDateExpr returns a SQL expression yielding the most recent
// aired-episode date for a series. For non-series rows it returns the
// stored release/first-air date trimmed.
//
// As of migration 103, this reads the denormalized media_items.last_air_date_at
// column instead of issuing a correlated MAX(episodes.air_date) subquery
// per row (audit 2026-05-01 §2.1).
//
// Behavior note: prior to migration 103, non-series rows used the stored
// mi.last_air_date text column with a `<= CURRENT_DATE` guard. As of 103,
// non-series rows fall back to mi.first_air_date (trimmed). last_air_date
// on media_items is currently only populated by TV-series metadata
// providers, so this is benign in practice; if a future provider populates
// it for movies, the API field LastAirDate (browse.go projection) will
// surface first_air_date instead. Restore the prior fallback if this
// matters.
func effectiveLastAirDateExpr(alias string) string {
	return fmt.Sprintf(
		"(CASE WHEN %s.type = 'series' "+
			"THEN COALESCE(%s.last_air_date_at::text, NULLIF(BTRIM(%s.first_air_date), '')) "+
			"ELSE NULLIF(BTRIM(%s.first_air_date), '') END)",
		alias, alias, alias, alias,
	)
}

// latestAvailableAirDateSortExpr returns the library browse sort key for
// last_air_date: for a series, the newest aired episode with a file in a
// library (media_items.latest_available_episode_air_date_at, issue #2741).
// A series without one has no value and sorts last, as in catalog queries,
// rather than by its premiere date. Other types fall back to first_air_date.
// The series' last_air_date field keeps reading effectiveLastAirDateExpr.
func latestAvailableAirDateSortExpr(alias string) string {
	return fmt.Sprintf(
		"(CASE WHEN %s.type = 'series' THEN %s.%s::text "+
			"ELSE NULLIF(BTRIM(%s.first_air_date), '') END)",
		alias, alias, latestAvailableAirDateColumn, alias,
	)
}
