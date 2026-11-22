package models

// EpisodeLabelsAirDate marks a series whose season and episode numbers are
// synthetic, such as a sports league Sportarr numbers S2026E296: clients label
// its episodes by air date instead. The numbers still order the episodes.
const EpisodeLabelsAirDate = "air_date"

// NormalizeEpisodeLabels keeps the episode-label values clients understand and
// maps anything else to "" (season and episode numbers).
func NormalizeEpisodeLabels(raw string) string {
	if raw == EpisodeLabelsAirDate {
		return EpisodeLabelsAirDate
	}
	return ""
}
