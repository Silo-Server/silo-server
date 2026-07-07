package metadata

import "strings"

// NormalizeShowStatus maps provider-reported series lifecycle statuses onto
// the canonical domain persisted in media_items.show_status for series:
// "returning", "ended", "cancelled", "in_production", "upcoming", or "".
// Providers spell these differently (TMDB "Returning Series" / "Canceled",
// TVDB "Continuing" / "Upcoming"), so persistence converges on one spelling
// clients can rely on. Unrecognized values pass through trimmed and
// lowercased rather than being dropped, so a new provider status still
// reaches clients (which fall back to displaying the raw value).
//
// This is series-domain only. Manga statuses use a different value domain
// ("Ongoing", "Completed", ...) normalized by the manga enrichment pipeline;
// callers must not route manga values through this function.
func NormalizeShowStatus(raw string) string {
	cleaned := strings.ToLower(strings.TrimSpace(raw))
	switch cleaned {
	case "":
		return ""
	case "returning", "returning series", "continuing":
		return "returning"
	case "ended":
		return "ended"
	case "cancelled", "canceled":
		return "cancelled"
	case "in production", "in_production", "pilot":
		return "in_production"
	case "upcoming", "planned":
		return "upcoming"
	default:
		return cleaned
	}
}
