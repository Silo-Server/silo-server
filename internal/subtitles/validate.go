package subtitles

import (
	"bytes"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/text/encoding/unicode"
)

// ErrUnreadableSubtitle reports an upload whose bytes hold no subtitle cue the
// declared format can carry: a renamed document, binary data, or plain text.
var ErrUnreadableSubtitle = errors.New("unreadable subtitle file")

var (
	// MicroDVD: {start frame}{end frame}text, with the text on the same line.
	microDVDCue = regexp.MustCompile(`(?m)^[ \t]*\{(\d+)\}\{(\d*)\}[ \t]*(\S[^\r\n]*)`)
	// SubViewer 2: 00:00:01.00,00:00:04.00 followed by a text line. Lines may
	// end in LF, CRLF or a bare CR, as FFmpeg reads them.
	subViewerCue = regexp.MustCompile(`(?m)^[ \t]*\d{1,2}:\d{2}:\d{2}[.,]\d{1,3}[ \t]*,[ \t]*\d{1,2}:\d{2}:\d{2}[.,]\d{1,3}[ \t]*(?:\r\n?|\n)[^\r\n]*\S`)
)

// FFmpeg's demuxers for the two text formats a .sub can hold.
const (
	microDVDDemuxer  = "microdvd"
	subViewerDemuxer = "subviewer"
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
		ok = subDemuxer(text) != ""
	}
	if !ok {
		return ErrUnreadableSubtitle
	}
	return nil
}

// SUBDemuxer returns the FFmpeg input format for .sub subtitle data, which
// FFmpeg has no "sub" demuxer for: "microdvd" or "subviewer", read from the
// cues as ValidateSubtitleContent reads them, or "" when data holds neither,
// such as a binary VobSub .sub. Like those demuxers, it reads UTF-8 with or
// without a BOM and not UTF-16, which they turn into no cues or an error;
// DeliveryBytes hands over UTF-8.
func SUBDemuxer(data []byte) string {
	return subDemuxer(bytes.TrimPrefix(data, utf8BOM))
}

// subDemuxer names the format of the first cue in text. A SubViewer cue whose
// text looks like a MicroDVD line starts before that line, so SubViewer is
// looked for only up to the end of the first MicroDVD cue.
func subDemuxer(text []byte) string {
	end := microDVDCueEnd(text)
	searched := text
	if end >= 0 {
		searched = text[:end]
	}
	switch {
	case subViewerCue.Match(searched):
		return subViewerDemuxer
	case end >= 0:
		return microDVDDemuxer
	}
	return ""
}

// microDVDCueEnd returns the offset just past the first MicroDVD line with
// displayed text, or -1 when there is none. A line such as
// {1}{1}25.000 or {0}{0}23.976 in the first three non-empty lines declares
// the frame rate and is not a cue; like FFmpeg, that is a start frame of 0 or
// 1 with a rate between 3 and 100, and empty lines are skipped before
// counting. Later lines like it are cues FFmpeg displays.
func microDVDCueEnd(text []byte) int {
	size := len(text)
	for lineNo := 0; len(text) > 0; {
		// A line ends in LF, CRLF or a bare CR.
		end := bytes.IndexAny(text, "\r\n")
		if end < 0 {
			end = len(text)
		}
		line := text[:end]
		text = text[end:]
		if bytes.HasPrefix(text, []byte("\r\n")) {
			text = text[2:]
		} else if len(text) > 0 {
			text = text[1:]
		}
		if len(line) == 0 {
			continue
		}
		lineNo++
		m := microDVDCue.FindSubmatch(line)
		if m == nil {
			continue
		}
		if start := string(m[1]); lineNo <= 3 && (start == "0" || start == "1") {
			if fps, err := strconv.ParseFloat(strings.TrimSpace(string(m[3])), 64); err == nil && fps > 3 && fps < 100 {
				continue
			}
		}
		return size - len(text)
	}
	return -1
}

// decodeUTF16WithBOM returns UTF-16 data with a byte-order mark as UTF-8
// without the mark, and any other data unchanged.
func decodeUTF16WithBOM(data []byte) []byte {
	if !bytes.HasPrefix(data, []byte{0xFF, 0xFE}) && !bytes.HasPrefix(data, []byte{0xFE, 0xFF}) {
		return data
	}
	// ExpectBOM reads the byte order from the mark and drops it.
	decoded, err := unicode.UTF16(unicode.LittleEndian, unicode.ExpectBOM).NewDecoder().Bytes(data)
	if err != nil {
		return data
	}
	return decoded
}
