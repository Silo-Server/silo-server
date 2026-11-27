package models

// ValidEpisodeOrderProvider reports whether v is a value
// MediaFolder.EpisodeOrderProvider accepts.
func ValidEpisodeOrderProvider(v string) bool {
	return v == "" || v == "tvdb" || v == "tmdb"
}
