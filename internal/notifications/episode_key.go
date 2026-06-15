package notifications

import "math"

// episodeKeySeasonMultiplier folds (season, episode) ordinals into a single
// sortable integer key. The 1,000,000 multiplier accommodates absolute-
// numbered anime catalogs (10,000+ episodes flattened into one season) while
// keeping the combined value inside PostgreSQL's integer range for any
// realistic season number. Scanners must not emit episode numbers at or above
// the multiplier; ValidEpisodeOrdinals rejects such rows at ingest.
const episodeKeySeasonMultiplier = 1_000_000

// episodeKeyMaxSeason is the largest season number whose key still fits in a
// PostgreSQL int4 (episode_key columns). Higher values come from mis-parsed
// metadata (e.g. date-style season folders) and must be excluded everywhere a
// key is computed, in Go and in SQL alike.
const episodeKeyMaxSeason = (math.MaxInt32 - (episodeKeySeasonMultiplier - 1)) / episodeKeySeasonMultiplier

// EpisodeKey returns the canonical progression key for an episode. Every
// component that stores or compares episode progression and release state
// must use this helper so keys stay mutually comparable.
func EpisodeKey(seasonNumber, episodeNumber int) int {
	return seasonNumber*episodeKeySeasonMultiplier + episodeNumber
}

// ValidEpisodeOrdinals reports whether the ordinals can be folded into an
// episode key that fits in an int4 without collisions.
func ValidEpisodeOrdinals(seasonNumber, episodeNumber int) bool {
	return seasonNumber >= 0 && seasonNumber <= episodeKeyMaxSeason &&
		episodeNumber >= 0 && episodeNumber < episodeKeySeasonMultiplier
}
