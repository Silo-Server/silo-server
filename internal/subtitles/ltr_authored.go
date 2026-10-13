package subtitles

import (
	"html"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/bidi"
)

// Many community Arabic and Hebrew subtitle files are written for players that
// lay every line out left to right. To look right there, the author moves each
// line's neutral punctuation to the opposite logical end: the sentence's
// closing "..." or "!" comes first, a dialog dash comes last. Laid out the
// Unicode way, right to left, that punctuation lands on the wrong side.
//
// A file reads as LTR-authored when its right-to-left lines carry those
// signals and almost none of the logical-order ones. silo-apple applies the
// same rule to text tracks embedded in the media file.
const (
	// leftToRightMark (U+200E), as a line's first strong character, sets the
	// line's base direction to left to right (Unicode bidi rules P2/P3).
	// Arabic and Hebrew words still read right to left inside the line, and
	// renderers that already lay every line out left to right are unaffected.
	leftToRightMark = "\u200e"

	// Three signals rule out a lone stray dash; the ratio keeps a logical
	// file that opens a few lines with an ellipsis on its own layout.
	ltrAuthoredMinimumSignals = 3
	ltrAuthoredDominanceRatio = 3
)

// MarkLTRAuthoredLines returns SRT or WebVTT text with a left-to-right mark
// at the start of each right-to-left cue line when the file is LTR-authored,
// and the input unchanged otherwise. Only marks are inserted; timing lines,
// headers and line endings stay byte for byte. Marked lines no longer read as
// right to left, so marking a file twice changes nothing.
func MarkLTRAuthoredLines(data []byte) []byte {
	if !utf8.Valid(data) {
		return data
	}
	lines := splitLinesKeepingEnds(string(data))

	var rightToLeft []int
	ltrSignals, logicalSignals := 0, 0
	for _, i := range cueTextLines(lines) {
		visible := visibleSubtitleText(lines[i])
		if !firstStrongIsRightToLeft(visible) {
			continue
		}
		rightToLeft = append(rightToLeft, i)
		if hasLTRAuthoredPunctuation(visible) {
			ltrSignals++
		}
		if hasLogicalPunctuation(visible) {
			logicalSignals++
		}
	}
	if ltrSignals < ltrAuthoredMinimumSignals || ltrSignals <= ltrAuthoredDominanceRatio*logicalSignals {
		return data
	}

	for _, i := range rightToLeft {
		lines[i] = markLine(lines[i])
	}
	return []byte(strings.Join(lines, ""))
}

// splitLinesKeepingEnds splits at LF, CRLF, CRCRLF and lone CR, keeping each
// line's ending so joining the result restores the input exactly. CRCRLF is
// one ending, as the SRT to WebVTT conversion reads it.
func splitLinesKeepingEnds(text string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '\n':
			lines = append(lines, text[start:i+1])
			start = i + 1
		case '\r':
			if strings.HasPrefix(text[i+1:], "\n") || strings.HasPrefix(text[i+1:], "\r\n") {
				continue
			}
			lines = append(lines, text[start:i+1])
			start = i + 1
		}
	}
	if start < len(text) {
		lines = append(lines, text[start:])
	}
	return lines
}

// cueTextLines returns the indexes of cue payload lines: those after a timing
// line, up to the blank line that ends the cue. Indexes, headers, NOTE and
// STYLE blocks are never payload, and dialog containing "-->" is.
func cueTextLines(lines []string) []int {
	var out []int
	inCue := false
	for i, raw := range lines {
		line := strings.TrimRight(raw, "\r\n")
		_, _, timingErr := parseTimingLine(line)
		switch {
		case timingErr == nil:
			inCue = true
		case strings.TrimSpace(line) == "":
			inCue = false
		case inCue:
			out = append(out, i)
		}
	}
	return out
}

// markLine puts the mark after any leading ASS override blocks such as
// {\an8}, which decoders only honor at the start of a cue.
func markLine(line string) string {
	prefix := 0
	for strings.HasPrefix(line[prefix:], "{") {
		end := strings.IndexByte(line[prefix:], '}')
		if end < 0 {
			break
		}
		prefix += end + 1
	}
	return line[:prefix] + leftToRightMark + line[prefix:]
}

// visibleSubtitleText drops markup (<i>, <font ...>, WebVTT tags, {\...}
// override blocks), decodes character references such as &nbsp; and &rlm;,
// and trims surrounding whitespace, leaving the text a viewer sees.
func visibleSubtitleText(line string) string {
	var b strings.Builder
	var closing rune
	for _, r := range line {
		switch {
		case closing != 0:
			if r == closing {
				closing = 0
			}
		case r == '<':
			closing = '>'
		case r == '{':
			closing = '}'
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(html.UnescapeString(b.String()))
}

func firstStrongIsRightToLeft(text string) bool {
	for _, r := range text {
		props, _ := bidi.LookupRune(r)
		switch props.Class() {
		case bidi.L:
			return false
		case bidi.R, bidi.AL:
			return true
		}
	}
	return false
}

// isSentencePunctuation reports the weak or neutral punctuation an LTR author
// moves. The Arabic comma is weak like the ASCII one; the Arabic question mark
// and full stop are strong right-to-left characters, already land on the
// correct side in either layout, and so are never moved.
func isSentencePunctuation(r rune) bool {
	return r == '.' || r == '…' || r == '!' || r == '?' || r == ',' || r == '،'
}

// hasLTRAuthoredPunctuation reports a sentence end moved to the logical start
// ("...لأنه") or a dialog dash moved to the logical end ("ماذا حدث؟ -").
func hasLTRAuthoredPunctuation(line string) bool {
	if first, _ := utf8.DecodeRuneInString(line); isSentencePunctuation(first) {
		if rest := strings.TrimLeftFunc(line, isSentencePunctuation); strings.TrimSpace(rest) != "" {
			return true
		}
	}
	if strings.HasSuffix(line, "-") {
		return strings.TrimFunc(line, func(r rune) bool { return r == '-' || unicode.IsSpace(r) }) != ""
	}
	return false
}

// hasLogicalPunctuation reports a dialog dash at the logical start or a
// sentence end at the logical end.
func hasLogicalPunctuation(line string) bool {
	if strings.HasPrefix(line, "-") && strings.TrimSpace(line[1:]) != "" {
		return true
	}
	if last, _ := utf8.DecodeLastRuneInString(line); isSentencePunctuation(last) {
		return strings.IndexFunc(line, func(r rune) bool {
			return !unicode.IsSpace(r) && !isSentencePunctuation(r)
		}) >= 0
	}
	return false
}
