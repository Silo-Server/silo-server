package workerwire

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
)

const MaxLogMessageBytes = 1024

var (
	logQuotedLocation = regexp.MustCompile(`(?i)["'](?:https?://|file:|s3:|/|[a-z]:(?:\x5c|/))[^"'\r\n]*["']`)
	logURL            = regexp.MustCompile(`(?i)(?:https?|file|s3|ftp|rtsp)://[^\s"'<>]+`)
	logPath           = regexp.MustCompile(`(?:^|[\s(=,:])(?:/[^\s"'<>]+|[a-zA-Z]:(?:\x5c|/)[^\s"'<>]+)`)
	logCredential     = regexp.MustCompile(`(?i)\b(?:authorization|(?:access|refresh|id)[_-]?token|token|(?:jwt|client)[_-]?secret|secret(?:_key)?|password|passwd|api[_-]?key|access[_-]?key|(?:node|core)[_-]?private[_-]?key|private[_-]?key|grant|cookie)["']?\s*[:=]\s*(?:"[^"]*"|'[^']*'|(?:(?:Bearer|Basic)\s+)?[^\s,;]+)`)
	logBearer         = regexp.MustCompile(`(?i)\b(?:Bearer|Basic)\s+[^\s,;]+`)
)

// SanitizeLogMessage strips transport locations and credential-shaped values
// at both report boundaries. Raw argument and error fields are excluded by
// the report type; this function bounds and redacts remaining diagnostics.
func SanitizeLogMessage(message string) string {
	message = strings.ToValidUTF8(message, "?")
	// FFmpeg's own sink has a smaller bound; hostile report senders must not
	// cause the core to scan an arbitrarily large diagnostic string.
	if len(message) > 16<<10 {
		return "diagnostic omitted: message exceeds bound"
	}
	message = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return ' '
		}
		return r
	}, message)
	message = redactLogMatches(message)
	message = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, message)
	message = strings.TrimSpace(message)
	if len(message) > MaxLogMessageBytes {
		message = strings.ToValidUTF8(message[:MaxLogMessageBytes], "")
	}
	return message
}

// Match against the same original text: removing a location can hide a nested
// credential's name, and removing a credential can hide a location's closing
// quote. Merging their spans preserves every match without depending on order.
func redactLogMatches(message string) string {
	var spans [][2]int
	for _, pattern := range []*regexp.Regexp{logCredential, logBearer, logQuotedLocation, logURL, logPath} {
		for _, match := range pattern.FindAllStringIndex(message, -1) {
			spans = append(spans, [2]int{match[0], match[1]})
		}
	}
	if len(spans) == 0 {
		return message
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i][0] < spans[j][0] })
	var redacted strings.Builder
	previous := 0
	for i := 0; i < len(spans); {
		start, end := spans[i][0], spans[i][1]
		i++
		for i < len(spans) && spans[i][0] <= end {
			end = max(end, spans[i][1])
			i++
		}
		redacted.WriteString(message[previous:start])
		redacted.WriteString("[redacted]")
		previous = end
	}
	redacted.WriteString(message[previous:])
	return redacted.String()
}
