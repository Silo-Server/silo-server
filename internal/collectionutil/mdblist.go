package collectionutil

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// ErrMDBListURL is returned when a caller-supplied list URL is not an
// MDBList list page. Sync fetches that URL with the server's HTTP client, so
// anything else is an SSRF primitive.
var ErrMDBListURL = errors.New("mdblist url must be an https://mdblist.com/lists/... list")

var allowedMDBListHosts = map[string]struct{}{
	"mdblist.com":     {},
	"www.mdblist.com": {},
}

// NormalizeMDBListURL accepts either an MDBList page URL or its JSON variant
// and returns the canonical JSON URL. Trailing slashes and accidental repeated
// /json suffixes are tolerated. It does not validate the host; call
// ValidateMDBListURL (or CanonicalMDBListURL) before fetching.
func NormalizeMDBListURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	raw = strings.TrimRight(raw, "/")
	for strings.HasSuffix(raw, "/json/json") {
		raw = strings.TrimSuffix(raw, "/json")
	}
	if !strings.HasSuffix(raw, "/json") {
		raw += "/json"
	}
	return raw
}

// CanonicalMDBListURL normalizes then allowlists the URL used for MDBList
// JSON fetches.
func CanonicalMDBListURL(raw string) (string, error) {
	normalized := NormalizeMDBListURL(raw)
	if err := ValidateMDBListURL(normalized); err != nil {
		return "", err
	}
	return normalized, nil
}

// ValidateMDBListURL rejects anything that is not an MDBList list page.
// Scheme may be http or https (mdblist redirects http→https); host must be
// mdblist.com or www.mdblist.com; path must be under /lists/.
func ValidateMDBListURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrMDBListURL, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ErrMDBListURL
	}
	if parsed.User != nil {
		return ErrMDBListURL
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if _, ok := allowedMDBListHosts[host]; !ok {
		return ErrMDBListURL
	}
	if port := parsed.Port(); port != "" && port != "80" && port != "443" {
		return ErrMDBListURL
	}
	path := parsed.EscapedPath()
	if path == "" {
		path = parsed.Path
	}
	if !strings.HasPrefix(path, "/lists/") {
		return ErrMDBListURL
	}
	return nil
}

// MDBListHTTPClient returns a clone of base whose redirects are re-checked
// against ValidateMDBListURL so an mdblist.com 3xx cannot bounce the fetch
// onto loopback or RFC1918. A nil base uses http.DefaultClient.
func MDBListHTTPClient(base *http.Client) *http.Client {
	if base == nil {
		base = http.DefaultClient
	}
	clone := *base
	parentRedirect := base.CheckRedirect
	clone.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req == nil || req.URL == nil {
			return ErrMDBListURL
		}
		if err := ValidateMDBListURL(req.URL.String()); err != nil {
			return err
		}
		if parentRedirect != nil {
			return parentRedirect(req, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return &clone
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
