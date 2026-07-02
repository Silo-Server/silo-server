// Package librarykind is the single home of media_folders.type
// classification. The type column is free text ("movies", "TV", " Audiobooks "
// ...), so every consumer must apply the same normalization and vocabulary;
// scanner, libraryingest, and metadata previously kept private copies of these
// predicates that drifted apart.
//
// Predicates are strict: IsMovie does not include mixed libraries. Call sites
// that treat mixed libraries as movie-bearing must say so explicitly
// (IsMovie(t) || IsMixed(t)).
package librarykind

import "strings"

// Kinds reports every classification of one media_folders.type value.
// Movie/TV/Mixed feed the video scan pipeline; Audiobook/Ebook/Podcast/Manga
// are single-purpose library types with dedicated scan pipelines and are
// never part of a mixed library.
type Kinds struct {
	Movie     bool
	TV        bool
	Mixed     bool
	Audiobook bool
	Ebook     bool
	Podcast   bool
	Manga     bool
}

// Of resolves every predicate for one library type in a single call.
func Of(libraryType string) Kinds {
	return Kinds{
		Movie:     IsMovie(libraryType),
		TV:        IsTV(libraryType),
		Mixed:     IsMixed(libraryType),
		Audiobook: IsAudiobook(libraryType),
		Ebook:     IsEbook(libraryType),
		Podcast:   IsPodcast(libraryType),
		Manga:     IsManga(libraryType),
	}
}

func normalize(libraryType string) string {
	return strings.ToLower(strings.TrimSpace(libraryType))
}

// IsMovie reports whether the library type is a dedicated movie library.
func IsMovie(libraryType string) bool {
	switch normalize(libraryType) {
	case "movie", "movies":
		return true
	default:
		return false
	}
}

// IsTV reports whether the library type is a dedicated TV/series library.
func IsTV(libraryType string) bool {
	switch normalize(libraryType) {
	case "series", "tv", "show", "tvshows":
		return true
	default:
		return false
	}
}

// IsMixed reports whether the library type mixes movies and TV.
func IsMixed(libraryType string) bool {
	return normalize(libraryType) == "mixed"
}

// IsAudiobook reports whether the library type is an audiobook library.
func IsAudiobook(libraryType string) bool {
	switch normalize(libraryType) {
	case "audiobook", "audiobooks":
		return true
	default:
		return false
	}
}

// IsEbook reports whether the library type is an ebook library.
func IsEbook(libraryType string) bool {
	switch normalize(libraryType) {
	case "ebook", "ebooks":
		return true
	default:
		return false
	}
}

// IsPodcast reports whether the library type is a podcast library.
func IsPodcast(libraryType string) bool {
	switch normalize(libraryType) {
	case "podcast", "podcasts":
		return true
	default:
		return false
	}
}

// IsManga reports whether the library type is a manga library.
func IsManga(libraryType string) bool {
	return normalize(libraryType) == "manga"
}
