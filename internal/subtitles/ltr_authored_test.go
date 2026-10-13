package subtitles

import (
	"strings"
	"testing"
)

// Lines from a community Arabic file written for left-to-right players: the
// dialog dash is last and the closing ellipsis is first.
const ltrAuthoredSRT = "\ufeff1\r\n00:00:37,209 --> 00:00:39,334\r\n{\\an8}ماذا حدث للتو؟ -\r\n\r\n" +
	"2\r\n00:00:40,584 --> 00:00:43,292\r\n...لأنه بالنسبة إليهم\r\nOK -\r\n\r\n" +
	"3\r\n00:00:43,459 --> 00:00:46,792\r\n<i>إنها حالة طارئة -</i>\r\n\r\n" +
	"4\r\n00:00:47,250 --> 00:00:49,999\r\nلقد انفجر -\r\n"

// The same cues in logical order, as release-derived files carry them.
const logicalSRT = "1\n00:00:37,209 --> 00:00:39,334\n- ماذا حدث للتو؟\n\n" +
	"2\n00:00:40,584 --> 00:00:43,292\nلأنه بالنسبة إليهم...\n\n" +
	"3\n00:00:43,459 --> 00:00:46,792\n<i>- إنها حالة طارئة</i>\n\n" +
	"4\n00:00:47,250 --> 00:00:49,999\n- لقد انفجر\n"

func TestMarkLTRAuthoredLinesMarksOnlyRightToLeftCueText(t *testing.T) {
	got := string(MarkLTRAuthoredLines([]byte(ltrAuthoredSRT)))
	want := "\ufeff1\r\n00:00:37,209 --> 00:00:39,334\r\n{\\an8}\u200eماذا حدث للتو؟ -\r\n\r\n" +
		"2\r\n00:00:40,584 --> 00:00:43,292\r\n\u200e...لأنه بالنسبة إليهم\r\nOK -\r\n\r\n" +
		"3\r\n00:00:43,459 --> 00:00:46,792\r\n\u200e<i>إنها حالة طارئة -</i>\r\n\r\n" +
		"4\r\n00:00:47,250 --> 00:00:49,999\r\n\u200eلقد انفجر -\r\n"
	if got != want {
		t.Fatalf("marked SRT\n got %q\nwant %q", got, want)
	}
}

func TestMarkLTRAuthoredLinesLeavesOtherFilesByteForByte(t *testing.T) {
	webVTT := "WEBVTT\n\nNOTE لقد انفجر -\n\n" + strings.ReplaceAll(logicalSRT, ",", ".")
	strayLines := logicalSRT + "\n5\n00:00:50,000 --> 00:00:51,000\n...ثم\n\n6\n00:00:52,000 --> 00:00:53,000\nنعم -\n"
	for name, input := range map[string]string{
		"logical SRT":        logicalSRT,
		"logical WebVTT":     webVTT,
		"stray LTR markers":  strayLines,
		"too few signals":    "1\n00:00:01,000 --> 00:00:02,000\nلقد انفجر -\n",
		"left-to-right text": "1\n00:00:01,000 --> 00:00:02,000\nWhat? -\n\n2\n00:00:03,000 --> 00:00:04,000\n...and then -\n\n3\n00:00:05,000 --> 00:00:06,000\nNo -\n",
		"invalid UTF-8":      ltrAuthoredSRT[:len(ltrAuthoredSRT)-4] + "\xff\r\n",
	} {
		if got := string(MarkLTRAuthoredLines([]byte(input))); got != input {
			t.Errorf("%s changed:\n got %q\nwant %q", name, got, input)
		}
	}
}

func TestMarkLTRAuthoredLinesIsIdempotent(t *testing.T) {
	once := MarkLTRAuthoredLines([]byte(ltrAuthoredSRT))
	if twice := MarkLTRAuthoredLines(once); string(twice) != string(once) {
		t.Fatalf("second pass changed the file:\n got %q\nwant %q", twice, once)
	}
}

func TestMarkLTRAuthoredLinesHandlesWebVTT(t *testing.T) {
	input := "WEBVTT\n\ncue-1\n00:00:37.209 --> 00:00:39.334 line:90%\nماذا حدث للتو؟ -\n\n" +
		"00:00:40.584 --> 00:00:43.292\n...لأنه بالنسبة إليهم\n\n" +
		"00:00:47.250 --> 00:00:49.999\nلقد انفجر -\n"
	want := "WEBVTT\n\ncue-1\n00:00:37.209 --> 00:00:39.334 line:90%\n\u200eماذا حدث للتو؟ -\n\n" +
		"00:00:40.584 --> 00:00:43.292\n\u200e...لأنه بالنسبة إليهم\n\n" +
		"00:00:47.250 --> 00:00:49.999\n\u200eلقد انفجر -\n"
	if got := string(MarkLTRAuthoredLines([]byte(input))); got != want {
		t.Fatalf("marked WebVTT\n got %q\nwant %q", got, want)
	}
}

func TestMarkLTRAuthoredLinesCountsAMovedArabicComma(t *testing.T) {
	input := "1\n00:00:01,000 --> 00:00:02,000\n،ثم ذهبنا إلى البيت\n\n" +
		"2\n00:00:03,000 --> 00:00:04,000\n،وبعد ذلك\n\n" +
		"3\n00:00:05,000 --> 00:00:06,000\n،لكنه لم يأت\n"
	want := "1\n00:00:01,000 --> 00:00:02,000\n\u200e،ثم ذهبنا إلى البيت\n\n" +
		"2\n00:00:03,000 --> 00:00:04,000\n\u200e،وبعد ذلك\n\n" +
		"3\n00:00:05,000 --> 00:00:06,000\n\u200e،لكنه لم يأت\n"
	if got := string(MarkLTRAuthoredLines([]byte(input))); got != want {
		t.Fatalf("marked SRT\n got %q\nwant %q", got, want)
	}
}

func TestMarkLTRAuthoredLinesHandlesCROnlyArrowsAndEntities(t *testing.T) {
	input := "1\r00:00:01,000 --> 00:00:02,000\rاذهب --> هناك -\r\r" +
		"2\r00:00:03,000 --> 00:00:04,000\r&nbsp;لقد انفجر -\r\r" +
		"3\r00:00:05,000 --> 00:00:06,000\r...لأنه بالنسبة إليهم\r"
	want := "1\r00:00:01,000 --> 00:00:02,000\r\u200eاذهب --> هناك -\r\r" +
		"2\r00:00:03,000 --> 00:00:04,000\r\u200e&nbsp;لقد انفجر -\r\r" +
		"3\r00:00:05,000 --> 00:00:06,000\r\u200e...لأنه بالنسبة إليهم\r"
	if got := string(MarkLTRAuthoredLines([]byte(input))); got != want {
		t.Fatalf("marked SRT\n got %q\nwant %q", got, want)
	}
}

func TestMarkLTRAuthoredLinesCountsASentenceEndAfterASpace(t *testing.T) {
	input := "1\n00:00:01,000 --> 00:00:02,000\n...ثم\n\n" +
		"2\n00:00:03,000 --> 00:00:04,000\n...وبعد ذلك\n\n" +
		"3\n00:00:05,000 --> 00:00:06,000\n...لكن\n\n" +
		"4\n00:00:07,000 --> 00:00:08,000\nمرحبا ...\n\n" +
		"5\n00:00:09,000 --> 00:00:10,000\nنعم !\n"
	if got := string(MarkLTRAuthoredLines([]byte(input))); got != input {
		t.Fatalf("logical file changed:\n got %q", got)
	}
}

func TestMarkLTRAuthoredLinesReadsCRCRLFAsOneLineEnding(t *testing.T) {
	input := "1\r\r\n00:00:01,000 --> 00:00:02,000\r\r\nماذا حدث للتو؟ -\r\r\n\r\r\n" +
		"2\r\r\n00:00:03,000 --> 00:00:04,000\r\r\n...لأنه بالنسبة إليهم\r\r\n\r\r\n" +
		"3\r\r\n00:00:05,000 --> 00:00:06,000\r\r\nلقد انفجر -\r\r\n"
	want := strings.NewReplacer("ماذا", "\u200eماذا", "...لأنه", "\u200e...لأنه", "لقد", "\u200eلقد").Replace(input)
	if got := string(MarkLTRAuthoredLines([]byte(input))); got != want {
		t.Fatalf("marked SRT\n got %q\nwant %q", got, want)
	}
}
