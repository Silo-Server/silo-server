package playback

import (
	"testing"
)

// TestConvertToVTTCueText converts whole SRT documents. Real SRT files break
// the blank-line rule between cues in several ways; each cue must still come
// out as its own WebVTT cue.
func TestConvertToVTTCueText(t *testing.T) {
	for name, tc := range map[string]struct{ input, want string }{
		"plain cues unchanged": {
			"1\n00:00:01,000 --> 00:00:02,500\n<i>First</i> line\nSecond line\n\n2\n00:00:03,000 --> 00:00:04,000\nThird\n",
			"WEBVTT\n\n1\n00:00:01.000 --> 00:00:02.500\n<i>First</i> line\nSecond line\n\n2\n00:00:03.000 --> 00:00:04.000\nThird\n\n",
		},
		// A right-to-left cue asking for the top of the frame, with the CRLF line
		// endings and BOM that Windows-authored SRT files often carry.
		"CRLF BOM right-to-left cue": {
			"\ufeff1\r\n00:00:01,000 --> 00:00:03,000\r\n{\\an8}(مرحبا)، هذا سطر\r\nسطر ثان\r\n\r\n",
			"WEBVTT\n\n1\n00:00:01.000 --> 00:00:03.000 line:0\n(مرحبا)، هذا سطر\nسطر ثان\n\n\n",
		},
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
