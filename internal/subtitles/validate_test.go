package subtitles

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"unicode/utf16"
)

func utf16LEWithBOM(s string) []byte {
	out := []byte{0xFF, 0xFE}
	for _, u := range utf16.Encode([]rune(s)) {
		out = append(out, byte(u), byte(u>>8))
	}
	return out
}

func TestValidateSubtitleContentAcceptsReadableFiles(t *testing.T) {
	for _, tc := range []struct {
		name   string
		format SubtitleFormat
		data   []byte
	}{
		{"srt", FormatSRT, []byte("1\n00:00:01,000 --> 00:00:02,000\nHello\n")},
		{"srt with BOM and CRLF", FormatSRT, []byte("\xef\xbb\xbf1\r\n00:00:01,000 --> 00:00:02,000\r\nHello\r\n")},
		{"srt in Latin-1", FormatSRT, []byte("1\n00:00:01,000 --> 00:00:02,000\nCaf\xe9\n")},
		{"srt in UTF-16", FormatSRT, utf16LEWithBOM("1\r\n00:00:01,000 --> 00:00:02,000\r\nHello\r\n")},
		{"vtt", FormatVTT, []byte("WEBVTT\n\n00:01.000 --> 00:02.000\nHello\n")},
		{"ass", FormatASS, []byte("[Script Info]\nTitle: x\n\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\nDialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,Hello\n")}, //nolint:misspell // ASS event keyword
		{"ssa", FormatSSA, []byte("[Events]\nFormat: Marked, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\nDialogue: Marked=0,0:00:01.00,0:00:02.00,Default,,0,0,0,,Hello\n")},                    //nolint:misspell // ASS event keyword
		{"microdvd sub", FormatSUB, []byte("{25}{50}Hello|World\n")},
		{"subviewer sub", FormatSUB, []byte("[INFORMATION]\n00:00:01.00,00:00:02.00\nHello\n")},
		{"microdvd sub with BOM", FormatSUB, []byte("\xef\xbb\xbf{25}{50}Hello\n")},
		{"subviewer sub with BOM", FormatSUB, []byte("\xef\xbb\xbf00:00:01.00,00:00:02.00\nHello\n")},
		{"microdvd sub after frame rate", FormatSUB, []byte("{1}{1}25.000\n{25}{50}Hello\n")},
		{"microdvd sub after zero-frame rate", FormatSUB, []byte("{0}{0}23.976\n{25}{50}Hello\n")},
		{"microdvd numeric cue after three header lines", FormatSUB, []byte("{0}{0}23.976\n{0}{0}23.976\n{0}{0}23.976\n{0}{25}42\n")},
		{"srt with CRCRLF line endings", FormatSRT, []byte("1\r\r\n00:00:01,000 --> 00:00:02,000\r\r\nHello\r\r\n")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateSubtitleContent(tc.format, tc.data); err != nil {
				t.Fatalf("ValidateSubtitleContent: %v", err)
			}
		})
	}
}

func TestValidateSubtitleContentRejectsUnreadableFiles(t *testing.T) {
	for _, tc := range []struct {
		name   string
		format SubtitleFormat
		data   []byte
	}{
		{"prose renamed to srt", FormatSRT, []byte("Notes for the movie night.\nBring snacks.\n")},
		{"pdf renamed to srt", FormatSRT, []byte("%PDF-1.4\n1 0 obj << /Type /Catalog >> endobj\n")},
		{"binary", FormatSRT, []byte{0x00, 0x9f, 0x13, 0xff, 0x2d, 0x2d, 0x3e, 0x01}},
		{"vtt header without cues", FormatVTT, []byte("WEBVTT\n\nnot a cue line\n")},
		{"srt timing without text", FormatSRT, []byte("1\n00:00:01,000 --> 00:00:02,000\n\n")},
		{"ass without events", FormatASS, []byte("[Script Info]\nTitle: x\n")},
		{"sub prose", FormatSUB, []byte("just some words\n")},
		{"microdvd frame rate only", FormatSUB, []byte("{1}{1}25.000\n")},
		{"microdvd zero-frame rate only", FormatSUB, []byte("{0}{0}23.976\n")},
		{"microdvd frames without text", FormatSUB, []byte("{25}{50}\nNotes\n")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateSubtitleContent(tc.format, tc.data); !errors.Is(err, ErrUnreadableSubtitle) {
				t.Fatalf("ValidateSubtitleContent = %v, want ErrUnreadableSubtitle", err)
			}
		})
	}
}

func TestManagerUploadRejectsUnreadableContentBeforeStoring(t *testing.T) {
	repo := newMockSubtitleRepo()
	blobs := newMockBlobStore()
	manager := NewManager(repo, blobs)
	_, err := manager.Upload(context.Background(), UploadRequest{
		MediaFileID: 1,
		Language:    "en",
		Filename:    "notes.srt",
		Data:        []byte("Notes for the movie night.\n"),
	})
	if !errors.Is(err, ErrUnreadableSubtitle) {
		t.Fatalf("Upload = %v, want ErrUnreadableSubtitle", err)
	}
	if len(blobs.keys) != 0 {
		t.Fatalf("stored %d blobs for a rejected upload", len(blobs.keys))
	}
}

func TestValidateSubtitleContentBoundsHugeASSFormat(t *testing.T) {
	format := "Format: Start, End, " + strings.Repeat("X, ", 100_000) + "Text\n"
	data := []byte("[Events]\n" + format + strings.Repeat("Dialogue: 0,0:00:01.00\n", 1_000)) //nolint:misspell // ASS event keyword
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	before := stats.TotalAlloc
	if err := ValidateSubtitleContent(FormatASS, data); !errors.Is(err, ErrUnreadableSubtitle) {
		t.Fatalf("ValidateSubtitleContent = %v, want ErrUnreadableSubtitle", err)
	}
	runtime.ReadMemStats(&stats)
	if got := stats.TotalAlloc - before; got > 64<<20 {
		t.Fatalf("validating %d bytes allocated %d bytes", len(data), got)
	}
}

func TestParseCuesKeepsWebVTTDoubleCRAsBlockBreak(t *testing.T) {
	cues, err := ParseCues([]byte("WEBVTT\n\n00:01.000 --> 00:02.000\nOne\r\r\n00:03.000 --> 00:04.000\nTwo\n"))
	if err != nil || len(cues) != 2 {
		t.Fatalf("ParseCues = %d cues, %v; want 2", len(cues), err)
	}
}

func TestValidateSubtitleContentStopsAtFirstMicroDVDCue(t *testing.T) {
	data := []byte(strings.Repeat("{2}{3}x\n", 200_000))
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	before := stats.TotalAlloc
	if err := ValidateSubtitleContent(FormatSUB, data); err != nil {
		t.Fatalf("ValidateSubtitleContent: %v", err)
	}
	runtime.ReadMemStats(&stats)
	if got := stats.TotalAlloc - before; got > 1<<20 {
		t.Fatalf("validating %d bytes allocated %d bytes", len(data), got)
	}
}

func TestManagerUploadStoresUTF16AsUTF8(t *testing.T) {
	blobs := newMockBlobStore()
	manager := NewManager(newMockSubtitleRepo(), blobs)
	want := "WEBVTT\n\n00:01.000 --> 00:02.000\nHéllo\n"
	if _, err := manager.Upload(context.Background(), UploadRequest{
		MediaFileID: 1,
		Language:    "en",
		Filename:    "movie.vtt",
		Data:        utf16LEWithBOM(want),
	}); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if len(blobs.keys) != 1 {
		t.Fatalf("stored %d blobs, want 1", len(blobs.keys))
	}
	for _, got := range blobs.keys {
		if string(got) != want {
			t.Fatalf("stored %q, want %q", got, want)
		}
	}
}
