package collectionutil

import "strings"

// NormalizeMDBListURL accepts either an MDBList page URL or its JSON variant
// and returns the canonical JSON URL. Trailing slashes and accidental repeated
// /json suffixes are tolerated.
func NormalizeMDBListURL(url string) string {
	url = strings.TrimSpace(url)
	if url == "" {
		return ""
	}
	url = strings.TrimRight(url, "/")
	for strings.HasSuffix(url, "/json/json") {
		url = strings.TrimSuffix(url, "/json")
	}
	if !strings.HasSuffix(url, "/json") {
		url += "/json"
	}
	return url
}

// FetchMDBListWithFallback tries each candidate URL in order and returns the
// entries from the first successful fetch, recovering when source_config and
// source_url drift. It returns the last error when every candidate fails.
// Callers should guard against an empty url list beforehand; an empty list
// yields a nil result and nil error.
func FetchMDBListWithFallback[T any](urls []string, fetch func(string) ([]T, error)) ([]T, error) {
	var entries []T
	var err error
	for _, url := range urls {
		entries, err = fetch(url)
		if err == nil {
			return entries, nil
		}
	}
	return entries, err
}

// MDBListURLCandidates returns unique canonical JSON URLs, preserving argument
// order. It lets syncers recover when source_config and source_url drift.
func MDBListURLCandidates(urls ...string) []string {
	candidates := make([]string, 0, len(urls))
	seen := make(map[string]struct{}, len(urls))
	for _, url := range urls {
		normalized := NormalizeMDBListURL(url)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		candidates = append(candidates, normalized)
	}
	return candidates
}
