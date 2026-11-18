package subtitles

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// largeSubtitle repeats lines as cues in format until the file reaches size bytes.
func largeSubtitle(format SubtitleFormat, size int, lines []string) []byte {
	var b strings.Builder
	switch format {
	case FormatVTT:
		b.WriteString("WEBVTT\n\n")
	case FormatASS:
		b.WriteString("[Script Info]\nScriptType: v4.00+\n\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")
	}
	for i := 0; b.Len() < size; i++ {
		line := lines[i%len(lines)]
		switch format {
		case FormatVTT:
			fmt.Fprintf(&b, "00:00:%02d.000 --> 00:00:%02d.500\n%s\n\n", i%60, i%60, line)
		case FormatASS:
			fmt.Fprintf(&b, "Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,{\\an8}%s\n", line) //nolint:misspell // ASS event keyword
		default:
			fmt.Fprintf(&b, "%d\n00:00:%02d,000 --> 00:00:%02d,500\n%s\n\n", i+1, i%60, i%60, line)
		}
	}
	return []byte(b.String())
}

var englishDialogue = []string{
	"I told you we should have left the city before the storm arrived.",
	"Nobody in this village remembers what happened to the old lighthouse keeper.",
	"If you open that door again, I promise I will not come looking for you.",
	"We have three hours until sunrise, so keep your voice down and follow me.",
}

func TestContentDetectionBoundsDetectorInput(t *testing.T) {
	const size = MaxUploadSize
	inputs := map[string]struct {
		format SubtitleFormat
		data   []byte
	}{
		"srt": {FormatSRT, largeSubtitle(FormatSRT, size, englishDialogue)},
		"vtt": {FormatVTT, largeSubtitle(FormatVTT, size, englishDialogue)},
		"ass": {FormatASS, largeSubtitle(FormatASS, size, englishDialogue)},
		// Each invalid byte becomes a three-byte replacement rune.
		"invalid utf-8":            {FormatSRT, largeSubtitle(FormatSRT, size, []string{string(bytes.Repeat([]byte{0xff}, 70))})},
		"invalid utf-8 long lines": {FormatSRT, bytes.Repeat(append(bytes.Repeat([]byte{0xff}, 32<<10), '\n'), 64)},
	}
	for name, in := range inputs {
		text := contentDetectionText(in.data, in.format)
		if len(text) > contentSampleBytes || !utf8.ValidString(text) {
			t.Errorf("%s: detector input is %d bytes (valid UTF-8: %v), want valid text of at most %d", name, len(text), utf8.ValidString(text), contentSampleBytes)
		}
	}
}

func TestDetectionLimiterBoundsAccountAndServer(t *testing.T) {
	l := NewDetectionLimiter()
	var held []func()
	take := func(userID int) bool {
		release, ok := l.Acquire(userID)
		if ok {
			held = append(held, release)
		}
		return ok
	}
	first, second := take(1), take(1)
	if !first || !second || take(1) {
		t.Fatal("an account must hold two detections and no third")
	}
	if !take(2) || !take(3) || take(4) {
		t.Fatal("a server must hold four detections and no fifth")
	}
	held[0]()
	held[0]()
	if !take(4) || take(1) {
		t.Fatal("a release, even repeated, must free exactly one slot")
	}
}
