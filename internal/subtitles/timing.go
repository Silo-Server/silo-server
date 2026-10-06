package subtitles

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Timing is a stored correction mapping original subtitle time t to
// t*Scale + Offset. Scale is applied first, so a frame-rate correction does not
// stretch the offset.
type Timing struct {
	Scale    float64 // 0 means 1
	OffsetMS int
}

// Normalized returns t with the zero Scale replaced by 1.
func (t Timing) Normalized() Timing {
	if t.Scale == 0 {
		t.Scale = 1
	}
	return t
}

// IsIdentity reports whether applying t leaves every timestamp unchanged.
func (t Timing) IsIdentity() bool {
	n := t.Normalized()
	return n.Scale == 1 && n.OffsetMS == 0
}

// Apply maps an original subtitle time to its corrected time:
// round(d*Scale) + Offset. The result may be negative.
func (t Timing) Apply(d time.Duration) time.Duration {
	n := t.Normalized()
	if n.Scale != 1 {
		d = time.Duration(math.Round(float64(d) * n.Scale))
	}
	return d + time.Duration(n.OffsetMS)*time.Millisecond
}

func (t Timing) validate() error {
	s := t.Normalized().Scale
	if math.IsNaN(s) || math.IsInf(s, 0) || s <= 0 {
		return fmt.Errorf("invalid subtitle timing scale %v", t.Scale)
	}
	return nil
}

// ErrRetimeUnsupported reports a subtitle format Retime cannot rewrite.
var ErrRetimeUnsupported = errors.New("subtitle format does not support retiming")

// SupportsRetime reports whether Retime can rewrite this format (srt, vtt, ass, ssa).
func SupportsRetime(format SubtitleFormat) bool {
	switch normalizeFormat(format) {
	case FormatSRT, FormatVTT, FormatASS, FormatSSA:
		return true
	}
	return false
}

func normalizeFormat(format SubtitleFormat) SubtitleFormat {
	return SubtitleFormat(strings.ToLower(strings.TrimSpace(string(format))))
}

// Retime rewrites every timestamp in data by t, leaving all other bytes
// untouched: BOM, line endings, cue identifiers and settings, metadata blocks,
// markup, and ASS sections. A cue whose corrected end is at or before zero is
// dropped; a negative corrected start is clamped to zero. Malformed timing
// lines are left as they are. Identity timing returns data itself.
func Retime(format SubtitleFormat, data []byte, t Timing) ([]byte, error) {
	if !SupportsRetime(format) {
		return nil, fmt.Errorf("%w: %q", ErrRetimeUnsupported, format)
	}
	if err := t.validate(); err != nil {
		return nil, err
	}
	if t.IsIdentity() {
		return data, nil
	}
	t = t.Normalized()

	var out bytes.Buffer
	out.Grow(len(data) + 64)
	body := data
	if bytes.HasPrefix(body, utf8BOM) {
		out.Write(utf8BOM)
		body = body[len(utf8BOM):]
	}
	switch normalizeFormat(format) {
	case FormatSRT:
		retimeBlocks(&out, body, func(out *bytes.Buffer, block []rawLine) bool {
			return retimeSRTBlock(out, block, t)
		})
	case FormatVTT:
		retimeBlocks(&out, body, func(out *bytes.Buffer, block []rawLine) bool {
			return retimeVTTBlock(out, block, t)
		})
	case FormatASS, FormatSSA:
		retimeASS(&out, body, t)
	}
	return out.Bytes(), nil
}

var utf8BOM = []byte("\ufeff")

// rawLine is one input line split from its terminator (CRLF, LF, CR, or none).
type rawLine struct {
	text []byte
	eol  []byte
}

func (l rawLine) writeTo(out *bytes.Buffer) {
	out.Write(l.text)
	out.Write(l.eol)
}

// nextLine splits the first line off data, keeping its original terminator.
func nextLine(data []byte) (rawLine, []byte) {
	i := bytes.IndexAny(data, "\r\n")
	if i < 0 {
		return rawLine{text: data}, nil
	}
	n := 1
	if data[i] == '\r' && i+1 < len(data) && data[i+1] == '\n' {
		n = 2
	}
	return rawLine{text: data[:i], eol: data[i : i+n]}, data[i+n:]
}

func isBlank(line []byte) bool {
	return len(bytes.TrimSpace(line)) == 0
}

// retimeBlocks walks blank-line separated blocks. handle writes a block (it
// may rewrite lines) and returns false when it dropped the block, in which case
// the blank separator lines after it are dropped too.
func retimeBlocks(out *bytes.Buffer, data []byte, handle func(*bytes.Buffer, []rawLine) bool) {
	var block []rawLine
	skipBlanks := false
	for len(data) > 0 {
		var line rawLine
		line, data = nextLine(data)
		if !isBlank(line.text) {
			block = append(block, line)
			skipBlanks = false
			continue
		}
		if len(block) > 0 {
			skipBlanks = !handle(out, block)
			block = block[:0]
		}
		if !skipBlanks {
			line.writeTo(out)
		}
	}
	if len(block) > 0 {
		handle(out, block)
	}
}

func writeLines(out *bytes.Buffer, lines []rawLine) {
	for _, line := range lines {
		line.writeTo(out)
	}
}

// retimeCueRange applies t to an original cue range. ok is false when the cue
// ends at or before zero and must be dropped.
func retimeCueRange(t Timing, start, end, unit time.Duration) (time.Duration, time.Duration, bool) {
	start = t.Apply(start).Round(unit)
	end = t.Apply(end).Round(unit)
	if end <= 0 {
		return 0, 0, false
	}
	return max(start, 0), end, true
}

// writeTimingLine writes line with only its start and end timestamp tokens
// replaced, preserving surrounding whitespace, the arrow, and cue settings.
func writeTimingLine(out *bytes.Buffer, line rawLine, start, end string) {
	text := line.text
	arrow := bytes.Index(text, []byte("-->"))
	left := text[:arrow]
	s0 := len(left) - len(bytes.TrimLeftFunc(left, unicode.IsSpace))
	s1 := len(bytes.TrimRightFunc(left, unicode.IsSpace))
	out.Write(left[:s0])
	out.WriteString(start)
	out.Write(left[s1:])
	out.WriteString("-->")

	right := text[arrow+3:]
	e0 := len(right) - len(bytes.TrimLeftFunc(right, unicode.IsSpace))
	e1 := len(right)
	if sp := bytes.IndexAny(right[e0:], " \t"); sp >= 0 {
		e1 = e0 + sp
	}
	out.Write(right[:e0])
	out.WriteString(end)
	out.Write(right[e1:])
	out.Write(line.eol)
}

func retimeSRTBlock(out *bytes.Buffer, block []rawLine, t Timing) bool {
	timing := slices.IndexFunc(block, func(l rawLine) bool { return bytes.Contains(l.text, []byte("-->")) })
	if timing < 0 {
		writeLines(out, block)
		return true
	}
	start, end, err := parseTimingLine(string(block[timing].text))
	if err != nil {
		writeLines(out, block)
		return true
	}
	start, end, ok := retimeCueRange(t, start, end, time.Millisecond)
	if !ok {
		return false
	}
	writeLines(out, block[:timing])
	writeTimingLine(out, block[timing], formatSRTTimestamp(start), formatSRTTimestamp(end))
	writeLines(out, block[timing+1:])
	return true
}

func isWebVTTMetadataBlock(first string) bool {
	return strings.HasPrefix(first, "WEBVTT") || first == "NOTE" || strings.HasPrefix(first, "NOTE ") ||
		strings.HasPrefix(first, "NOTE\t") || first == "STYLE" || first == "REGION"
}

func retimeVTTBlock(out *bytes.Buffer, block []rawLine, t Timing) bool {
	first := string(block[0].text)
	if strings.HasPrefix(first, "WEBVTT") {
		// X-TIMESTAMP-MAP anchors cue times to the original media clock and
		// would be wrong once the cues move.
		for _, line := range block {
			if !bytes.HasPrefix(line.text, []byte("X-TIMESTAMP-MAP=")) {
				line.writeTo(out)
			}
		}
		return true
	}
	timing := -1
	if !isWebVTTMetadataBlock(first) {
		// A cue has an optional identifier followed immediately by its timing line.
		for i := 0; i < len(block) && i < 2; i++ {
			if bytes.Contains(block[i].text, []byte("-->")) {
				timing = i
				break
			}
		}
	}
	if timing < 0 {
		writeLines(out, block)
		return true
	}
	start, end, err := parseTimingLine(string(block[timing].text))
	if err != nil {
		writeLines(out, block)
		return true
	}
	start, end, ok := retimeCueRange(t, start, end, time.Millisecond)
	if !ok {
		return false
	}
	writeLines(out, block[:timing])
	writeTimingLine(out, block[timing], formatWebVTTTimestamp(start), formatWebVTTTimestamp(end))
	for _, line := range block[timing+1:] {
		if bytes.IndexByte(line.text, '<') >= 0 {
			line.text = inlineWebVTTTimestamp.ReplaceAllFunc(line.text, func(tag []byte) []byte {
				ts, err := parseTimestamp(string(tag[1 : len(tag)-1]))
				if err != nil {
					return tag
				}
				ts = t.Apply(ts).Round(time.Millisecond)
				// Inline timestamps outside the surviving cue interval cannot be active.
				if ts <= start || ts >= end {
					return nil
				}
				return []byte("<" + formatWebVTTTimestamp(ts) + ">")
			})
		}
		line.writeTo(out)
	}
	return true
}

// assEventLayout locates the Start and End fields of [Events] lines.
type assEventLayout struct {
	start, end int // field indexes, -1 when absent
	fields     int // total field count; Text is the last field
}

// defaultASSEventLayout is the standard ASS (Layer, ...) and SSA (Marked, ...)
// event order: X, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text.
var defaultASSEventLayout = assEventLayout{start: 1, end: 2, fields: 10}

func parseASSFormat(spec []byte) assEventLayout {
	layout := assEventLayout{start: -1, end: -1}
	for i, name := range bytes.Split(spec, []byte(",")) {
		switch strings.ToLower(string(bytes.TrimSpace(name))) {
		case "start":
			layout.start = i
		case "end":
			layout.end = i
		}
		layout.fields = i + 1
	}
	return layout
}

// assSection returns the lower-cased section name when line is a [Section] header.
func assSection(line []byte) (string, bool) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) < 2 || trimmed[0] != '[' || trimmed[len(trimmed)-1] != ']' {
		return "", false
	}
	return strings.ToLower(string(trimmed[1 : len(trimmed)-1])), true
}

// assDisplayedEventKey is the ASS/SSA event line key for displayed events.
const assDisplayedEventKey = "Dialogue:" //nolint:misspell // ASS keyword

// assEventBody returns the field list of a displayed or Comment event line and
// whether the event is displayed.
func assEventBody(line []byte) (body []byte, displayed, ok bool) {
	trimmed := bytes.TrimLeftFunc(line, unicode.IsSpace)
	if rest, found := bytes.CutPrefix(trimmed, []byte(assDisplayedEventKey)); found {
		return rest, true, true
	}
	if rest, found := bytes.CutPrefix(trimmed, []byte("Comment:")); found {
		return rest, false, true
	}
	return nil, false, false
}

// assFieldSpans returns the [start,end) byte spans of the first n
// comma-terminated fields of body and the offset just past the n-th comma, or
// nil when body has fewer than n commas. Later commas (in Text) are not split.
func assFieldSpans(body []byte, n int) ([][2]int, int) {
	spans := make([][2]int, 0, n)
	pos := 0
	for len(spans) < n {
		comma := bytes.IndexByte(body[pos:], ',')
		if comma < 0 {
			return nil, 0
		}
		spans = append(spans, [2]int{pos, pos + comma})
		pos += comma + 1
	}
	return spans, pos
}

func retimeASS(out *bytes.Buffer, data []byte, t Timing) {
	inEvents := false
	layout := defaultASSEventLayout
	for len(data) > 0 {
		var line rawLine
		line, data = nextLine(data)
		if name, ok := assSection(line.text); ok {
			inEvents = name == "events"
			line.writeTo(out)
			continue
		}
		if !inEvents {
			line.writeTo(out)
			continue
		}
		if spec, ok := bytes.CutPrefix(bytes.TrimLeftFunc(line.text, unicode.IsSpace), []byte("Format:")); ok {
			layout = parseASSFormat(spec)
			line.writeTo(out)
			continue
		}
		retimeASSEvent(out, line, layout, t)
	}
}

// retimeASSEvent writes line with its Start/End rewritten, or untouched when
// it is not a well-formed event. An event that ends at or before zero is dropped.
func retimeASSEvent(out *bytes.Buffer, line rawLine, layout assEventLayout, t Timing) {
	body, _, ok := assEventBody(line.text)
	if !ok || layout.start < 0 || layout.end < 0 || layout.start == layout.end {
		line.writeTo(out)
		return
	}
	// Never split into the Text field, which is last and may hold commas.
	need := max(layout.start, layout.end) + 1
	if need >= layout.fields {
		line.writeTo(out)
		return
	}
	spans, _ := assFieldSpans(body, need)
	if spans == nil {
		line.writeTo(out)
		return
	}
	start, err1 := parseTimestamp(string(body[spans[layout.start][0]:spans[layout.start][1]]))
	end, err2 := parseTimestamp(string(body[spans[layout.end][0]:spans[layout.end][1]]))
	if err1 != nil || err2 != nil || start < 0 || end < start {
		line.writeTo(out)
		return
	}
	start, end, ok = retimeCueRange(t, start, end, 10*time.Millisecond)
	if !ok {
		return
	}

	out.Write(line.text[:len(line.text)-len(body)])
	type field struct {
		span [2]int
		ts   time.Duration
	}
	fields := [2]field{{spans[layout.start], start}, {spans[layout.end], end}}
	if layout.end < layout.start {
		fields[0], fields[1] = fields[1], fields[0]
	}
	pos := 0
	for _, f := range fields {
		raw := body[f.span[0]:f.span[1]]
		lead := len(raw) - len(bytes.TrimLeftFunc(raw, unicode.IsSpace))
		trail := len(bytes.TrimRightFunc(raw, unicode.IsSpace))
		out.Write(body[pos : f.span[0]+lead])
		out.WriteString(formatASSTimestamp(f.ts))
		pos = f.span[0] + trail
	}
	rest := body[pos:]
	if scale := t.Normalized().Scale; scale != 1 {
		rest = scaleASSOverrides(rest, scale)
	}
	out.Write(rest)
	out.Write(line.eol)
}

// ASS override tags with times relative to their event, which a scaled event
// must scale too: karaoke syllables (\k, \kf, \ko, \K, in centiseconds),
// and the millisecond times of \t, \move, \fad and \fade.
var (
	assOverrideBlock = regexp.MustCompile(`\{[^}]*\}`)
	assKaraoke       = regexp.MustCompile(`(\\(?:kf|ko|k|K))(\d+)`)
	assTransform     = regexp.MustCompile(`(\\t\(\s*)(-?\d+)(\s*,\s*)(-?\d+)`)
	assMove          = regexp.MustCompile(`(\\move\((?:[^,()]*,){4}\s*)(-?\d+)(\s*,\s*)(-?\d+)`)
	assFad           = regexp.MustCompile(`(\\fad\(\s*)(\d+)(\s*,\s*)(\d+)`)
	assFade          = regexp.MustCompile(`(\\fade\((?:[^,()]*,){3}\s*)(-?\d+)(\s*,\s*)(-?\d+)(\s*,\s*)(-?\d+)(\s*,\s*)(-?\d+)`)
)

// scaleASSOverrides scales the event-relative times inside the override
// blocks of an event's remaining fields, leaving every other byte as it is.
func scaleASSOverrides(rest []byte, scale float64) []byte {
	return assOverrideBlock.ReplaceAllFunc(rest, func(block []byte) []byte {
		for _, re := range []*regexp.Regexp{assKaraoke, assTransform, assMove, assFad, assFade} {
			block = re.ReplaceAllFunc(block, func(match []byte) []byte {
				groups := re.FindSubmatch(match)
				var out []byte
				// Odd groups are kept text, even groups are numbers.
				for i, g := range groups[1:] {
					if i%2 == 0 {
						out = append(out, g...)
						continue
					}
					n, err := strconv.Atoi(string(g))
					if err != nil {
						out = append(out, g...)
						continue
					}
					out = strconv.AppendInt(out, int64(math.Round(float64(n)*scale)), 10)
				}
				return out
			})
		}
		return block
	})
}

// formatASSTimestamp renders H:MM:SS.cc; d must already be centisecond-rounded.
func formatASSTimestamp(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	cs := int64(d / (10 * time.Millisecond))
	buf := make([]byte, 0, 11)
	buf = strconv.AppendInt(buf, cs/360000, 10)
	buf = append(buf, ':')
	buf = appendTwoDigits(buf, cs/6000%60)
	buf = append(buf, ':')
	buf = appendTwoDigits(buf, cs/100%60)
	buf = append(buf, '.')
	buf = appendTwoDigits(buf, cs%100)
	return string(buf)
}

func appendTwoDigits(buf []byte, v int64) []byte {
	return append(buf, byte('0'+v/10), byte('0'+v%10))
}

// ParseCuesForFormat returns cues in original timing. SRT and WebVTT use
// ParseCues. ASS/SSA cues come from displayed event lines (Comment lines are
// skipped): the Text field with override blocks and drawings removed and
// \N, \n line breaks split into lines, sorted by start time.
func ParseCuesForFormat(format SubtitleFormat, data []byte) ([]SubtitleCue, error) {
	switch normalizeFormat(format) {
	case FormatSRT, FormatVTT:
		return ParseCues(data)
	case FormatASS, FormatSSA:
		return parseASSCues(data)
	}
	return nil, fmt.Errorf("parse cues: %w: %q", ErrRetimeUnsupported, format)
}

func parseASSCues(data []byte) ([]SubtitleCue, error) {
	data = bytes.TrimPrefix(data, utf8BOM)
	inEvents := false
	layout := defaultASSEventLayout
	var cues []SubtitleCue
	for len(data) > 0 {
		var line rawLine
		line, data = nextLine(data)
		if name, ok := assSection(line.text); ok {
			inEvents = name == "events"
			continue
		}
		if !inEvents {
			continue
		}
		if spec, ok := bytes.CutPrefix(bytes.TrimLeftFunc(line.text, unicode.IsSpace), []byte("Format:")); ok {
			layout = parseASSFormat(spec)
			continue
		}
		body, displayed, ok := assEventBody(line.text)
		if !ok || !displayed || layout.start < 0 || layout.end < 0 {
			continue
		}
		if max(layout.start, layout.end) >= layout.fields-1 {
			continue
		}
		spans, textStart := assFieldSpans(body, layout.fields-1)
		if spans == nil {
			continue
		}
		start, err1 := parseTimestamp(string(body[spans[layout.start][0]:spans[layout.start][1]]))
		end, err2 := parseTimestamp(string(body[spans[layout.end][0]:spans[layout.end][1]]))
		if err1 != nil || err2 != nil || start < 0 || end < start {
			continue
		}
		lines := assTextLines(string(body[textStart:]))
		if len(lines) == 0 {
			continue
		}
		cues = append(cues, SubtitleCue{Start: start, End: end, Lines: lines})
	}
	if len(cues) == 0 {
		return nil, fmt.Errorf("no subtitle cues found")
	}
	slices.SortStableFunc(cues, func(a, b SubtitleCue) int { return cmp.Compare(a.Start, b.Start) })
	return cues, nil
}

// assTextLines strips {...} override blocks and drawing-mode text ({\p1}..{\p0})
// from an ASS Text field and splits it on \N and \n. Blank lines are dropped.
func assTextLines(text string) []string {
	var b strings.Builder
	drawing := false
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case c == '{':
			closeIdx := strings.IndexByte(text[i:], '}')
			if closeIdx < 0 {
				i = len(text)
				continue
			}
			if mode, ok := assDrawingMode(text[i+1 : i+closeIdx]); ok {
				drawing = mode
			}
			i += closeIdx
		case drawing:
		case c == '\\' && i+1 < len(text) && (text[i+1] == 'N' || text[i+1] == 'n'):
			b.WriteByte('\n')
			i++
		case c == '\\' && i+1 < len(text) && text[i+1] == 'h':
			b.WriteByte(' ')
			i++
		default:
			b.WriteByte(c)
		}
	}
	var lines []string
	for _, line := range strings.Split(b.String(), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// assDrawingMode reports the last \pN tag in an override block: drawing is on
// for N > 0. ok is false when the block has no \p tag.
func assDrawingMode(block string) (drawing, ok bool) {
	for _, tag := range strings.Split(block, `\`)[1:] {
		if len(tag) < 2 || tag[0] != 'p' || tag[1] < '0' || tag[1] > '9' {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(tag[1:]))
		if err != nil {
			continue
		}
		drawing, ok = n > 0, true
	}
	return drawing, ok
}
