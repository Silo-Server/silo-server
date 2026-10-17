package subtitles

import (
	"bytes"
	"encoding/binary"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// ErrUnreadableSubtitle reports an upload whose bytes hold no subtitle cue the
// declared format can carry: a renamed document, binary data, or plain text.
var ErrUnreadableSubtitle = errors.New("unreadable subtitle file")

var (
	// MicroDVD: {start frame}{end frame}text, with the text on the same line.
	microDVDCue = regexp.MustCompile(`(?m)^[ \t]*\{(\d+)\}\{(\d*)\}[ \t]*(\S[^\r\n]*)`)
	// SubViewer 2: 00:00:01.00,00:00:04.00 followed by a text line.
	subViewerCue = regexp.MustCompile(`(?m)^[ \t]*\d{1,2}:\d{2}:\d{2}[.,]\d{1,3}[ \t]*,[ \t]*\d{1,2}:\d{2}:\d{2}[.,]\d{1,3}[ \t]*\r?\n[^\r\n]*\S`)
)

// ValidateSubtitleContent checks that data holds at least one subtitle cue in
// format. Text in legacy single-byte encodings parses as-is, and UTF-16 files
// with a byte-order mark are decoded for the check.
func ValidateSubtitleContent(format SubtitleFormat, data []byte) error {
	text := bytes.TrimPrefix(decodeUTF16WithBOM(data), utf8BOM)
	var ok bool
	switch normalizeFormat(format) {
	case FormatSRT, FormatVTT, FormatASS, FormatSSA:
		cues, err := ParseCuesForFormat(format, text)
		ok = err == nil && len(cues) > 0
	case FormatSUB:
		ok = hasMicroDVDCue(text) || subViewerCue.Match(text)
	}
	if !ok {
		return ErrUnreadableSubtitle
	}
	return nil
}

// hasMicroDVDCue reports a MicroDVD line with displayed text. A line such as
// {1}{1}25.000 or {0}{0}23.976 in the first three lines declares the frame
// rate and is not a cue; like FFmpeg, that is a start frame of 0 or 1 with a
// rate between 3 and 100. Later lines like it are cues FFmpeg displays.
func hasMicroDVDCue(text []byte) bool {
	for lineNo := 0; len(text) > 0; lineNo++ {
		var line []byte
		line, text, _ = bytes.Cut(text, []byte{'\n'})
		m := microDVDCue.FindSubmatch(line)
		if m == nil {
			continue
		}
		if start := string(m[1]); lineNo < 3 && (start == "0" || start == "1") {
			if fps, err := strconv.ParseFloat(strings.TrimSpace(string(m[3])), 64); err == nil && fps > 3 && fps < 100 {
				continue
			}
		}
		return true
	}
	return false
}

// decodeUTF16WithBOM returns UTF-16 data with a byte-order mark as UTF-8
// without the mark, and any other data unchanged.
func decodeUTF16WithBOM(data []byte) []byte {
	var order binary.ByteOrder
	switch {
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE}):
		order = binary.LittleEndian
	case bytes.HasPrefix(data, []byte{0xFE, 0xFF}):
		order = binary.BigEndian
	default:
		return data
	}
	body := data[2:]
	units := make([]uint16, 0, len(body)/2)
	for i := 0; i+1 < len(body); i += 2 {
		units = append(units, order.Uint16(body[i:]))
	}
	out := make([]byte, 0, len(units))
	for _, r := range utf16.Decode(units) {
		out = utf8.AppendRune(out, r)
	}
	return out
}
