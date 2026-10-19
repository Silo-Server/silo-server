package titleutil

import "strings"

// IsProvisionalEpisodeTitle reports whether title is a provider placeholder
// ("TBA"/"TBD") that stands in until the real episode title is announced.
func IsProvisionalEpisodeTitle(title string) bool {
	title = strings.TrimSpace(title)
	return strings.EqualFold(title, "tba") || strings.EqualFold(title, "tbd")
}
