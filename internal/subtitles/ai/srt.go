package ai

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ParseCues parses SRT or WebVTT subtitle bytes into cues. It tolerates both
// comma (SRT) and period (VTT) millisecond separators, a leading WEBVTT header,
// optional cue identifiers, VTT cue settings after the end timestamp, a UTF-8
// BOM, and CRLF line endings. Malformed individual cues are skipped rather than
// failing the whole document.
func ParseCues(data []byte) ([]SubtitleCue, error) {
	text := string(data)
	text = strings.TrimPrefix(text, "\ufeff") // strip UTF-8 BOM
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	var cues []SubtitleCue
	for _, block := range strings.Split(text, "\n\n") {
		block = strings.Trim(block, "\n")
		if strings.TrimSpace(block) == "" {
			continue
		}
		lines := strings.Split(block, "\n")

		timingIdx := -1
		for i, l := range lines {
			if strings.Contains(l, "-->") {
				timingIdx = i
				break
			}
		}
		if timingIdx < 0 {
			continue // WEBVTT header, NOTE/STYLE blocks, etc.
		}

		start, end, err := parseTimingLine(lines[timingIdx])
		if err != nil {
			continue
		}

		textLines := append([]string(nil), lines[timingIdx+1:]...)
		for len(textLines) > 0 && strings.TrimSpace(textLines[len(textLines)-1]) == "" {
			textLines = textLines[:len(textLines)-1]
		}
		if len(textLines) == 0 {
			continue
		}
		cues = append(cues, SubtitleCue{Start: start, End: end, Lines: textLines})
	}

	if len(cues) == 0 {
		return nil, fmt.Errorf("no subtitle cues found")
	}
	return cues, nil
}

// SerializeSRT renders cues as a well-formed SRT document. Output always uses
// comma millisecond separators; the serving layer converts SRT to VTT on read.
func SerializeSRT(cues []SubtitleCue) []byte {
	var buf bytes.Buffer
	for i, cue := range cues {
		fmt.Fprintf(&buf, "%d\n%s --> %s\n", i+1, formatSRTTimestamp(cue.Start), formatSRTTimestamp(cue.End))
		for _, line := range cue.Lines {
			buf.WriteString(line)
			buf.WriteByte('\n')
		}
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

func parseTimingLine(line string) (time.Duration, time.Duration, error) {
	parts := strings.SplitN(line, "-->", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid timing line")
	}
	start, err := parseTimestamp(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, err
	}
	// The end timestamp may be followed by VTT cue settings (e.g. "line:0%").
	endField := strings.TrimSpace(parts[1])
	if sp := strings.IndexAny(endField, " \t"); sp >= 0 {
		endField = endField[:sp]
	}
	end, err := parseTimestamp(endField)
	if err != nil {
		return 0, 0, err
	}
	return start, end, nil
}

// parseTimestamp accepts HH:MM:SS,mmm / HH:MM:SS.mmm / MM:SS.mmm forms.
func parseTimestamp(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.Replace(s, ",", ".", 1))
	colon := strings.Split(s, ":")
	if len(colon) < 2 || len(colon) > 3 {
		return 0, fmt.Errorf("invalid timestamp %q", s)
	}

	var hours int
	idx := 0
	if len(colon) == 3 {
		h, err := strconv.Atoi(colon[0])
		if err != nil {
			return 0, fmt.Errorf("invalid hours in %q", s)
		}
		hours = h
		idx = 1
	}

	minutes, err := strconv.Atoi(colon[idx])
	if err != nil {
		return 0, fmt.Errorf("invalid minutes in %q", s)
	}

	secFrac := strings.SplitN(colon[idx+1], ".", 2)
	seconds, err := strconv.Atoi(secFrac[0])
	if err != nil {
		return 0, fmt.Errorf("invalid seconds in %q", s)
	}

	millis := 0
	if len(secFrac) == 2 {
		frac := secFrac[1]
		for len(frac) < 3 {
			frac += "0"
		}
		millis, err = strconv.Atoi(frac[:3])
		if err != nil {
			return 0, fmt.Errorf("invalid milliseconds in %q", s)
		}
	}

	return time.Duration(hours)*time.Hour +
		time.Duration(minutes)*time.Minute +
		time.Duration(seconds)*time.Second +
		time.Duration(millis)*time.Millisecond, nil
}

func formatSRTTimestamp(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	totalMS := int64(d / time.Millisecond)
	ms := totalMS % 1000
	totalSec := totalMS / 1000
	s := totalSec % 60
	totalMin := totalSec / 60
	m := totalMin % 60
	h := totalMin / 60
	return fmt.Sprintf("%02d:%02d:%02d,%03d", h, m, s, ms)
}
