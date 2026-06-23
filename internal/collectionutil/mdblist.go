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
