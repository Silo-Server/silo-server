// Package showstatus holds the canonical series lifecycle statuses Silo
// stores and serves. It imports nothing, so the catalog's metadata refresh
// and request routing share one mapping.
package showstatus

import "strings"

// The canonical statuses. A provider's own spellings map onto these.
const (
	Returning    = "returning"
	InProduction = "in_production"
	Upcoming     = "upcoming"
	Ended        = "ended"
	Canceled     = "cancelled" //nolint:misspell // the catalog's show_status spelling
)

// Known lists the canonical statuses, airing ones first.
var Known = []string{Returning, InProduction, Upcoming, Ended, Canceled}

// Normalize maps provider-reported series lifecycle statuses onto the
// canonical domain persisted in media_items.show_status for series: one of
// Known, or "".
// Providers spell these differently (TMDB "Returning Series" / "Canceled",
// TVDB "Continuing" / "Upcoming"), so persistence converges on one spelling
// clients can rely on. Unrecognized values pass through trimmed and
// lowercased rather than being dropped, so a new provider status still
// reaches clients (which fall back to displaying the raw value).
//
// This is series-domain only. Manga statuses use a different value domain
// ("Ongoing", "Completed", ...) normalized by the manga enrichment pipeline;
// callers must not route manga values through this function.
func Normalize(raw string) string {
	cleaned := strings.ToLower(strings.TrimSpace(raw))
	switch cleaned {
	case "":
		return ""
	case Returning, "returning series", "continuing":
		return Returning
	case Ended:
		return Ended
	case Canceled, "canceled":
		return Canceled
	case "in production", InProduction, "pilot":
		return InProduction
	case Upcoming, "planned":
		return Upcoming
	default:
		return cleaned
	}
}
