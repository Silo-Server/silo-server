package requests

import (
	"context"
	"regexp"
	"strings"
)

// tmdbImageFile matches an image path as TMDB returns it: one file name under
// the root, such as "/kqjL17yufvn9OVLyXYpvtyrFfak.jpg".
var tmdbImageFile = regexp.MustCompile(`(?i)^/[a-z0-9_-]{1,128}\.(jpg|jpeg|png|webp)$`)

// tmdbPosterBase is the size the server fetches a request poster at. A card
// shown at 360x540 or smaller scales it down.
const tmdbPosterBase = "https://image.tmdb.org/t/p/w500"

// ValidTMDBImagePath reports whether path is a TMDB image path, the only
// shape a request's poster_path and backdrop_path are meant to hold.
func ValidTMDBImagePath(path string) bool {
	return tmdbImageFile.MatchString(path)
}

// tmdbImagePathOrEmpty keeps a TMDB image path and drops anything else, so
// the TMDB detail read can fill the field instead.
func tmdbImagePathOrEmpty(path string) string {
	path = strings.TrimSpace(path)
	if !ValidTMDBImagePath(path) {
		return ""
	}
	return path
}

// AdminRequestPosterURL returns the TMDB URL of the poster of the request id
// names, or "" when its poster_path is not a TMDB image path. It reads the
// stored row only, so it answers while requests are turned off, as the admin
// request list does.
func (s *Service) AdminRequestPosterURL(ctx context.Context, viewer Viewer, id string) (string, error) {
	if !viewer.IsAdmin {
		return "", ErrForbidden
	}
	req, err := s.store.GetRequest(ctx, strings.TrimSpace(id))
	if err != nil {
		return "", err
	}
	if !ValidTMDBImagePath(req.PosterPath) {
		return "", nil
	}
	return tmdbPosterBase + req.PosterPath, nil
}
