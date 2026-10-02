package playback

import (
	"fmt"
	"testing"
)

func TestConvertToVTTLeavesPlainSRTCuesUnchanged(t *testing.T) {
	input := "1\n00:00:01,000 --> 00:00:02,500\n<i>First</i> line\nSecond line\n\n2\n00:00:03,000 --> 00:00:04,000\nThird\n"
	want := "WEBVTT\n\n1\n00:00:01.000 --> 00:00:02.500\n<i>First</i> line\nSecond line\n\n2\n00:00:03.000 --> 00:00:04.000\nThird\n\n"

	got, err := ConvertToVTT([]byte(input), "srt")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("plain SRT changed in conversion:\ngot  %q\nwant %q", got, want)
	}
}

// Every \an alignment must reach WebVTT as cue settings, never as cue text.
func TestConvertToVTTMovesASSAlignmentOntoTheTimingLine(t *testing.T) {
	settings := map[int]string{
		1: " align:left",
		2: "",
		3: " align:right",
		4: " line:50%,center align:left",
		5: " line:50%,center",
		6: " line:50%,center align:right",
		7: " line:0 align:left",
		8: " line:0",
		9: " line:0 align:right",
	}
	for alignment, setting := range settings {
		t.Run(fmt.Sprintf("an%d", alignment), func(t *testing.T) {
			input := fmt.Sprintf("1\n00:00:01,000 --> 00:00:02,000\n{\\an%d}Line one\nLine two\n", alignment)
			want := "WEBVTT\n\n1\n00:00:01.000 --> 00:00:02.000" + setting + "\nLine one\nLine two\n\n"

			got, err := ConvertToVTT([]byte(input), "srt")
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != want {
				t.Fatalf("got  %q\nwant %q", got, want)
			}
		})
	}
}

// A right-to-left cue asking for the top of the frame, with the CRLF line
// endings and BOM that Windows-authored SRT files often carry.
func TestConvertToVTTHandlesCRLFBOMRightToLeftCue(t *testing.T) {
	input := "\ufeff1\r\n00:00:01,000 --> 00:00:03,000\r\n{\\an8}(مرحبا)، هذا سطر\r\nسطر ثان\r\n\r\n"
	want := "WEBVTT\n\n1\n00:00:01.000 --> 00:00:03.000 line:0\n(مرحبا)، هذا سطر\nسطر ثان\n\n\n"

	got, err := ConvertToVTT([]byte(input), "srt")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestConvertToVTTStripsOverrideBlocks(t *testing.T) {
	for name, tc := range map[string]struct{ text, settings, want string }{
		"mid-line block":         {`Hello {\i1}there{\i0}`, "", "Hello there\n"},
		"combined tags":          {`{\an8\b1}Top`, " line:0", "Top\n"},
		"first alignment wins":   {"{\\an8}Top\n{\\an2}Still top", " line:0", "Top\nStill top\n"},
		"alignment on 2nd line":  {"First\n{\\an9}Second", " line:0 align:right", "First\nSecond\n"},
		"override-only line":     {"{\\an8}\nText", " line:0", "Text\n"},
		"unclosed block is text": {`Price {\an8 is high`, "", "Price {\\an8 is high\n"},
		"plain braces are text":  {"{Laughs} Hello", "", "{Laughs} Hello\n"},
		"out of range alignment": {`{\an10}Text`, "", "Text\n"},
	} {
		t.Run(name, func(t *testing.T) {
			input := "1\n00:00:01,000 --> 00:00:02,000\n" + tc.text + "\n"
			want := "WEBVTT\n\n1\n00:00:01.000 --> 00:00:02.000" + tc.settings + "\n" + tc.want + "\n"

			got, err := ConvertToVTT([]byte(input), "srt")
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != want {
				t.Fatalf("got  %q\nwant %q", got, want)
			}
		})
	}
}

// Real SRT files break the blank-line rule between cues in several ways. Each
// cue must still come out as its own WebVTT cue.
func TestConvertToVTTSeparatesCuesWithoutBlankLines(t *testing.T) {
	for name, tc := range map[string]struct{ input, want string }{
		"numbered cue without a blank line": {
			"1\n00:00:01,000 --> 00:00:02,000\nOne\n2\n00:00:03,000 --> 00:00:04,000\n{\\an8}Two\n",
			"WEBVTT\n\n1\n00:00:01.000 --> 00:00:02.000\nOne\n\n2\n00:00:03.000 --> 00:00:04.000 line:0\nTwo\n\n",
		},
		"back-to-back timing lines": {
			"00:00:01,000 --> 00:00:02,000\n00:00:03,000 --> 00:00:04,000\nTwo\n",
			"WEBVTT\n\n00:00:01.000 --> 00:00:02.000\n\n00:00:03.000 --> 00:00:04.000\nTwo\n\n",
		},
		"whitespace-only separator": {
			"1\n00:00:01,000 --> 00:00:02,000\nOne\n \n2\n00:00:03,000 --> 00:00:04,000\nTwo\n",
			"WEBVTT\n\n1\n00:00:01.000 --> 00:00:02.000\nOne\n\n2\n00:00:03.000 --> 00:00:04.000\nTwo\n\n",
		},
		// A stray CR before the settings would read as a line break.
		"stray carriage returns": {
			"1\r\r\n00:00:01,000 --> 00:00:02,000\r\r\n{\\an8}Top\r\r\n",
			"WEBVTT\n\n1\n00:00:01.000 --> 00:00:02.000 line:0\nTop\n\n",
		},
		"CR-only line endings": {
			"1\r00:00:01,000 --> 00:00:02,000\r{\\an8}Top\r\r2\r00:00:03,000 --> 00:00:04,000\rTwo\r",
			"WEBVTT\n\n1\n00:00:01.000 --> 00:00:02.000 line:0\nTop\n\n2\n00:00:03.000 --> 00:00:04.000\nTwo\n\n",
		},
		// An arrow in cue text must not start a new cue.
		"arrow in cue text": {
			"1\n00:00:01,000 --> 00:00:02,000\n{\\an8}Meet at 10:30. --> go now\nSecond line\n",
			"WEBVTT\n\n1\n00:00:01.000 --> 00:00:02.000 line:0\nMeet at 10:30. --> go now\nSecond line\n\n",
		},
		"period milliseconds": {
			"1\n00:00:01.000 --> 00:00:02.000\n{\\an8}Top\n",
			"WEBVTT\n\n1\n00:00:01.000 --> 00:00:02.000 line:0\nTop\n\n",
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := ConvertToVTT([]byte(tc.input), "srt")
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestSRTAlignmentTagForVTTCueSettingsInvertsTheConversion(t *testing.T) {
	for alignment := 1; alignment <= 9; alignment++ {
		settings := vttCueSettingsForASSAlignment(alignment)
		want := fmt.Sprintf("{\\an%d}", alignment)
		if alignment == 2 {
			want = "" // bottom center carries no settings, and SRT needs no tag
		}
		if got := SRTAlignmentTagForVTTCueSettings(settings); got != want {
			t.Errorf("an%d: settings %q gave tag %q, want %q", alignment, settings, got, want)
		}
	}
	if got := SRTAlignmentTagForVTTCueSettings("position:10% size:50%"); got != "" {
		t.Errorf("settings the conversion never writes must not become a tag, got %q", got)
	}
}
