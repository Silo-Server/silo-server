package recommendations

import "strings"

// Row titles. Reads and the cold-start rows title rows with these words, and
// the web's fallback labels copy them. Clients key rows by type and section
// kind, never by title, so a title can change without breaking one.
const (
	// ForYouLabel titles the profile's main row.
	ForYouLabel               = "For You"
	similarUsersLabel         = "Profiles Like You Enjoyed"
	popularLabel              = "Popular on This Server"
	recentlyAddedLabel        = "Recently Added"
	highlyRatedLabel          = "Highly Rated in Your Library"
	becauseYouWatchedRowLabel = "Because You Watched"
	// clusterTitlePrefix starts the title of a taste-cluster row whose label
	// names genres, and the Reason of every item in one.
	clusterTitlePrefix = "Because you enjoy "
	// unlabeledClusterTitle titles a taste-cluster row whose label names no
	// genre. It says where the titles come from without naming a taste.
	unlabeledClusterTitle = "Picked from your history"
)

// genreRowLabel titles a genre row. Its titles are the genre's best rated,
// not its most watched.
func genreRowLabel(genre string) string {
	return "Top " + genre
}

// clusterTitle titles the taste-cluster row with the given label.
func clusterTitle(label string) string {
	if label == "" {
		return unlabeledClusterTitle
	}
	return clusterTitlePrefix + label
}

// clusterTitleLabel returns the label a taste-cluster row title was made
// from by clusterTitle, and false for a title clusterTitle does not make.
func clusterTitleLabel(title string) (string, bool) {
	if title == unlabeledClusterTitle {
		return "", true
	}
	label, ok := strings.CutPrefix(title, clusterTitlePrefix)
	if !ok || label == "" {
		return "", false
	}
	return label, true
}

// Global reports whether the row is one every profile is offered (Popular,
// Recently Added, Highly Rated in Your Library or a genre row) rather than
// one built from the profile's own history.
func (r ForYouRow) Global() bool {
	switch r.Type {
	case RecTypePopular, RecTypeRecentlyAdded, RecTypeTopRated, genreSamplerRowType:
		return true
	}
	return false
}
