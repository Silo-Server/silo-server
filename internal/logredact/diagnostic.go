package logredact

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strings"
)

// SanitizeQuery preserves diagnostic parameters while masking credentials.
// Parse errors omit the entire query: ParseQuery can otherwise return a partial
// result and tempt callers to fall back to the unsafe original input.
func SanitizeQuery(raw string) string {
	query, err := url.ParseQuery(raw)
	if err != nil {
		return "[query omitted: invalid encoding]"
	}
	for key := range query {
		if diagnosticSecretKey(key) {
			query[key] = []string{Placeholder}
		}
	}
	return query.Encode()
}

// SanitizeRequestURL accepts relative request targets as well as absolute URLs.
// Unlike SanitizeURL (transport error diagnostics), it retains safe query fields.
// The caller's URL and live request are never modified.
func SanitizeRequestURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" {
		return invalidURLPlaceholder
	}
	u.User = nil
	u.RawQuery = SanitizeQuery(u.RawQuery)
	u.Fragment = ""
	u.RawFragment = ""
	return u.String()
}

// SanitizeJSON keeps structured diagnostics, including exact JSON numbers, but
// masks credential fields and credentials in URL strings. Invalid, truncated,
// scalar and non-JSON bodies are omitted rather than logged as raw text.
func SanitizeJSON(body []byte) []byte {
	const omitted = "[body omitted: not a complete JSON object or array]"
	if !json.Valid(body) {
		return []byte(omitted)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return []byte(omitted)
	}
	switch value.(type) {
	case map[string]any, []any:
	default:
		return []byte(omitted)
	}
	result, err := json.MarshalIndent(sanitizeDiagnosticValue(value), "", "  ")
	if err != nil {
		return []byte(omitted)
	}
	return result
}

func diagnosticSecretKey(key string) bool {
	// Pw is the Jellyfin login alias. Normalisation covers the casing and
	// separators used by compatibility clients without changing auth parsing.
	key = strings.ToLower(strings.TrimSpace(key))
	key = strings.ReplaceAll(strings.ReplaceAll(key, "_", ""), "-", "")
	return key == "pw" || SecretKey(key)
}

func sanitizeDiagnosticValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if diagnosticSecretKey(key) {
				v[key] = Placeholder
			} else {
				v[key] = sanitizeDiagnosticValue(child)
			}
		}
	case []any:
		for i, child := range v {
			v[i] = sanitizeDiagnosticValue(child)
		}
	case string:
		// Playback responses contain token-bearing relative and absolute URLs
		// under ordinary keys such as DirectStreamUrl and MediaSources[].Path.
		if looksLikeDiagnosticURL(v) {
			return SanitizeRequestURL(v)
		}
	}
	return value
}

// looksLikeDiagnosticURL matches rooted paths and scheme-prefixed URLs,
// not a question mark or an embedded link in ordinary metadata. Spaces do not
// disqualify a URL: its query can still contain a credential needing redaction.
func looksLikeDiagnosticURL(value string) bool {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "/") {
		return true
	}
	scheme, _, found := strings.Cut(value, "://")
	if !found || scheme == "" {
		return false
	}
	for i, c := range scheme {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' {
			continue
		}
		if i > 0 && (c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.') {
			continue
		}
		return false
	}
	return true
}
