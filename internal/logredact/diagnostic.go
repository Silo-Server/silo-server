package logredact

import (
	"bytes"
	"encoding/json"
	"net/url"
	"regexp"
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
		// Credentials can only sit in a query, fragment or userinfo; without
		// those, keep the value verbatim so on-disk file paths are not
		// re-encoded as URLs.
		if looksLikeDiagnosticURL(v) && strings.ContainsAny(v, "?#@") {
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

// assignmentKey matches the key half of a key=value pair in free text. The
// value is not part of the match, so a non-secret pair such as desc=api_key=x
// cannot swallow the secret assignment that follows it.
var assignmentKey = regexp.MustCompile(`[A-Za-z0-9_.-]+=`)

// RedactSecretAssignments masks the value of every key=value pair in free text
// whose key is secret-bearing (api_key=, access_token=, password=, ...). It is
// for error text that crosses a boundary as a plain string, such as a gRPC
// status description, where no structured URL is left to sanitize.
func RedactSecretAssignments(text string) string {
	if !strings.Contains(text, "=") {
		return text
	}
	var out strings.Builder
	last := 0
	for _, m := range assignmentKey.FindAllStringIndex(text, -1) {
		if m[0] < last || !diagnosticSecretKey(text[m[0]:m[1]-1]) {
			continue
		}
		start, end := assignmentValue(text, m[1])
		if start == end {
			continue
		}
		out.WriteString(text[last:start])
		out.WriteString(Placeholder)
		last = end
	}
	if last == 0 {
		return text
	}
	out.WriteString(text[last:])
	return out.String()
}

// assignmentValue returns the bounds of the value that starts at i. A quoted
// value runs to its closing quote; a bare one ends at whitespace, a query or
// list separator, a quote or a closing bracket.
func assignmentValue(text string, i int) (start, end int) {
	if i < len(text) && (text[i] == '"' || text[i] == '\'') {
		quote := text[i]
		if j := strings.IndexByte(text[i+1:], quote); j >= 0 {
			return i + 1, i + 1 + j
		}
		return i + 1, len(text)
	}
	end = i
	for end < len(text) && !strings.ContainsRune(" \t\r\n&;,\"')]}>", rune(text[end])) {
		end++
	}
	return i, end
}

// secretAssignmentPattern matches a key=value or key: value pair whose key
// names a credential, anywhere in free text. Matching on the key alone means a
// non-secret outer assignment (desc = api_key=SECRET) cannot hide a nested one.
var secretAssignmentPattern = regexp.MustCompile(`(?i)([\w.-]*(?:password|passwd|secret|token|api[_-]?key|authorization|cookie)[\w.-]*"?\s*[:=]\s*)(?:(?:bearer|basic)\s+)?(?:"[^"]*"|[^\s"&,;]+)`)

// urlUserinfoPattern matches the userinfo of a URL embedded in free text.
var urlUserinfoPattern = regexp.MustCompile(`://[^/\s@]+@`)

// SanitizeText masks credential assignments and URL userinfo in free-form
// diagnostic text, such as a stored error message, and keeps everything else.
func SanitizeText(text string) string {
	text = secretAssignmentPattern.ReplaceAllString(text, "${1}"+Placeholder)
	return urlUserinfoPattern.ReplaceAllString(text, "://"+Placeholder+"@")
}
