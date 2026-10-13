package collectionutil

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// ErrTMDBListURL is returned when a caller-supplied value does not name a
// TMDB list. Sync only ever sends the parsed numeric ID to the TMDB API, so
// the page URL itself is never fetched.
var ErrTMDBListURL = errors.New("tmdb list url must be a https://www.themoviedb.org/list/... list")

var allowedTMDBListHosts = map[string]struct{}{
	"themoviedb.org":     {},
	"www.themoviedb.org": {},
}

// ParseTMDBListURL extracts the list ID from a TMDB list page URL such as
// https://www.themoviedb.org/list/310-my-movie-list, the same URL without a
// scheme, or a bare numeric ID. The slug, query string, fragment, and a
// trailing slash are ignored.
func ParseTMDBListURL(raw string) (int, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, ErrTMDBListURL
	}
	if id, ok := parseTMDBListID(trimmed); ok {
		return id, nil
	}
	if !strings.Contains(trimmed, "://") {
		trimmed = "https://" + trimmed
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrTMDBListURL, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return 0, ErrTMDBListURL
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if _, ok := allowedTMDBListHosts[host]; !ok {
		return 0, ErrTMDBListURL
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 2 || parts[0] != "list" {
		return 0, ErrTMDBListURL
	}
	// The page path is /list/{id}-{slug}; the slug is cosmetic.
	idPart, _, _ := strings.Cut(parts[1], "-")
	id, ok := parseTMDBListID(idPart)
	if !ok {
		return 0, ErrTMDBListURL
	}
	return id, nil
}

// CanonicalTMDBListURL validates raw and returns the list's page URL without
// its slug, the form stored on a collection.
func CanonicalTMDBListURL(raw string) (string, error) {
	id, err := ParseTMDBListURL(raw)
	if err != nil {
		return "", err
	}
	return TMDBListPageURL(id), nil
}

// TMDBListPageURL returns the public page URL of the TMDB list with the given ID.
func TMDBListPageURL(id int) string {
	return "https://www.themoviedb.org/list/" + strconv.Itoa(id)
}

func parseTMDBListID(s string) (int, bool) {
	if s == "" || strings.TrimLeft(s, "0123456789") != "" {
		return 0, false
	}
	id, err := strconv.Atoi(s)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}
