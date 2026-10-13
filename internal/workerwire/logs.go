package workerwire

import (
	"regexp"
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
	message = logQuotedLocation.ReplaceAllString(message, "[location]")
	message = logURL.ReplaceAllString(message, "[location]")
	message = logPath.ReplaceAllString(message, " [location]")
	message = logCredential.ReplaceAllString(message, "[credential]")
	message = logBearer.ReplaceAllString(message, "[credential]")
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
