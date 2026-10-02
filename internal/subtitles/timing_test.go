package subtitles

import (
	"bytes"
	"errors"
	"slices"
	"testing"
	"time"
)

func TestTimingApplyScalesBeforeOffset(t *testing.T) {
	cases := []struct {
		name   string
		timing Timing
		in     time.Duration
		want   time.Duration
	}{
		{"zero scale is identity", Timing{}, 1500 * time.Millisecond, 1500 * time.Millisecond},
		{"offset only", Timing{OffsetMS: -250}, time.Second, 750 * time.Millisecond},
		{"scale then offset", Timing{Scale: 2, OffsetMS: 100}, time.Second, 2100 * time.Millisecond},
		{"negative result", Timing{OffsetMS: -2000}, time.Second, -time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.timing.Apply(tc.in); got != tc.want {
				t.Fatalf("Apply(%v)=%v want %v", tc.in, got, tc.want)
			}
		})
	}
	if !(Timing{}).IsIdentity() || !(Timing{Scale: 1}).IsIdentity() || (Timing{OffsetMS: 1}).IsIdentity() || (Timing{Scale: 1.001}).IsIdentity() {
		t.Fatal("IsIdentity misreports")
	}
	if (Timing{}).Normalized().Scale != 1 {
		t.Fatal("Normalized did not default scale")
	}
}

func TestRetimeIdentityReturnsInputUnchanged(t *testing.T) {
	for _, format := range []SubtitleFormat{FormatSRT, FormatVTT, FormatASS, FormatSSA} {
		data := []byte("1\n00:00:01,000 --> 00:00:02,000\nhi\n")
		got, err := Retime(format, data, Timing{Scale: 1})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(data) || &got[0] != &data[0] {
			t.Fatalf("%s: identity retime allocated a new slice", format)
		}
	}
}

func TestRetimeSRT(t *testing.T) {
	cases := []struct {
		name   string
		timing Timing
		in     string
		want   string
	}{
		{
			name:   "positive offset keeps indices text and coordinates",
			timing: Timing{OffsetMS: 1500},
			in:     "1\n00:00:01,000 --> 00:00:02,500  X1:10 X2:20\n<i>Hello, world</i>\n\n2\n00:59:59.900 --> 01:00:00.000\nDot separator\n",
			want:   "1\n00:00:02,500 --> 00:00:04,000  X1:10 X2:20\n<i>Hello, world</i>\n\n2\n01:00:01,400 --> 01:00:01,500\nDot separator\n",
		},
		{
			name:   "negative offset clamps start and drops ended cues",
			timing: Timing{OffsetMS: -2000},
			in:     "1\n00:00:00,500 --> 00:00:01,000\nGone\n\n2\n00:00:01,000 --> 00:00:02,000\nAlso gone\n\n3\n00:00:01,500 --> 00:00:03,000\nClamped\n\n4\n00:00:05,000 --> 00:00:06,000\nShifted\n",
			want:   "3\n00:00:00,000 --> 00:00:01,000\nClamped\n\n4\n00:00:03,000 --> 00:00:04,000\nShifted\n",
		},
		{
			name:   "CRLF and BOM preserved, dropped first cue keeps BOM",
			timing: Timing{OffsetMS: -1000},
			in:     "\ufeff1\r\n00:00:00,100 --> 00:00:00,900\r\nGone\r\n\r\n2\r\n00:00:02,000 --> 00:00:03,000\r\nKept\r\n\r\n",
			want:   "\ufeff2\r\n00:00:01,000 --> 00:00:02,000\r\nKept\r\n\r\n",
		},
		{
			name:   "PAL to film scale",
			timing: Timing{Scale: 25 / 23.976},
			in:     "1\n00:00:23,976 --> 00:47:57,120\nScaled\n",
			want:   "1\n00:00:25,000 --> 00:50:00,000\nScaled\n",
		},
		{
			name:   "malformed timing left untouched",
			timing: Timing{OffsetMS: 1000},
			in:     "1\nbad --> 00:00:02,000\nKeep\n\n2\n00:00:01,000 --> 00:00:02,000\nMove\n",
			want:   "1\nbad --> 00:00:02,000\nKeep\n\n2\n00:00:02,000 --> 00:00:03,000\nMove\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Retime(FormatSRT, []byte(tc.in), tc.timing)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("got\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

func TestRetimeVTT(t *testing.T) {
	cases := []struct {
		name   string
		timing Timing
		in     string
		want   string
	}{
		{
			name:   "cue ids settings metadata and inline timestamps",
			timing: Timing{OffsetMS: 2000},
			in: "WEBVTT - title\nX-TIMESTAMP-MAP=LOCAL:00:00:00.000,MPEGTS:0\n\n" +
				"STYLE\n::cue { color: red } /* 00:00:01.000 --> 00:00:02.000 */\n\n" +
				"NOTE 00:00:01.000 --> 00:00:02.000 is not a cue\n\n" +
				"REGION\nid:fred width:40%\n\n" +
				"intro\n00:01.000 --> 00:00:03.000 align:start position:10%\n<v Bob>Hi <00:00:02.000><b>there</b></v>\n",
			want: "WEBVTT - title\n\n" +
				"STYLE\n::cue { color: red } /* 00:00:01.000 --> 00:00:02.000 */\n\n" +
				"NOTE 00:00:01.000 --> 00:00:02.000 is not a cue\n\n" +
				"REGION\nid:fred width:40%\n\n" +
				"intro\n00:00:03.000 --> 00:00:05.000 align:start position:10%\n<v Bob>Hi <00:00:04.000><b>there</b></v>\n",
		},
		{
			name:   "CRLF BOM drop and clamp",
			timing: Timing{OffsetMS: -1500},
			in:     "\ufeffWEBVTT\r\n\r\n00:00:00.000 --> 00:00:01.000\r\nGone\r\n\r\n2\r\n00:00:01.000 --> 00:00:04.000\r\nA <00:00:01.200>b <00:00:02.000>c\r\n",
			want:   "\ufeffWEBVTT\r\n\r\n2\r\n00:00:00.000 --> 00:00:02.500\r\nA b <00:00:00.500>c\r\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Retime(FormatVTT, []byte(tc.in), tc.timing)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("got\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

const assHeader = "[Script Info]\r\nTitle: Test, with comma\r\nScriptType: v4.00+\r\n\r\n" +
	"[V4+ Styles]\r\nFormat: Name, Fontname, Fontsize\r\nStyle: Default,Arial,20\r\n\r\n"

func TestRetimeASS(t *testing.T) {
	cases := []struct {
		name   string
		format SubtitleFormat
		timing Timing
		in     string
		want   string
	}{
		{
			name:   "standard format with commas in text and comments",
			format: FormatASS,
			timing: Timing{OffsetMS: 1234},
			in: assHeader + "[Events]\r\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\r\n" +
				assDisplayedEventKey + " 0,0:00:01.00,0:00:02.50,Default,,0,0,0,,Hello, {\\i1}world{\\i0}, 0:00:09.00\r\n" +
				"Comment: 0,0:00:03.00,0:00:04.00,Default,,0,0,0,,note\r\n",
			want: assHeader + "[Events]\r\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\r\n" +
				assDisplayedEventKey + " 0,0:00:02.23,0:00:03.73,Default,,0,0,0,,Hello, {\\i1}world{\\i0}, 0:00:09.00\r\n" +
				"Comment: 0,0:00:04.23,0:00:05.23,Default,,0,0,0,,note\r\n",
		},
		{
			name:   "custom format order and centisecond rounding",
			format: FormatASS,
			timing: Timing{OffsetMS: 5},
			in:     "[Events]\nFormat: End, Style, Start, Text\n" + assDisplayedEventKey + " 0:00:02.00, Default, 0:00:01.00, a,b\n",
			want:   "[Events]\nFormat: End, Style, Start, Text\n" + assDisplayedEventKey + " 0:00:02.01, Default, 0:00:01.01, a,b\n",
		},
		{
			name:   "SSA without format line drops ended events and clamps start",
			format: FormatSSA,
			timing: Timing{OffsetMS: -1500},
			in:     "[Events]\n" + assDisplayedEventKey + " Marked=0,0:00:00.50,0:00:01.00,Default,,0,0,0,,gone\n" + assDisplayedEventKey + " Marked=0,0:00:01.00,0:00:03.00,Default,,0,0,0,,clamped\nbroken line\n",
			want:   "[Events]\n" + assDisplayedEventKey + " Marked=0,0:00:00.00,0:00:01.50,Default,,0,0,0,,clamped\nbroken line\n",
		},
		{
			name:   "hours beyond one digit and scale",
			format: FormatASS,
			timing: Timing{Scale: 2},
			in:     "[Events]\n" + assDisplayedEventKey + " 0,5:00:00.00,6:00:00.00,Default,,0,0,0,,long\n",
			want:   "[Events]\n" + assDisplayedEventKey + " 0,10:00:00.00,12:00:00.00,Default,,0,0,0,,long\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Retime(tc.format, []byte(tc.in), tc.timing)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("got\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

func TestRetimeRejectsUnsupportedFormatAndScale(t *testing.T) {
	if SupportsRetime(FormatSUB) || !SupportsRetime(FormatASS) {
		t.Fatal("SupportsRetime misreports")
	}
	if _, err := Retime(FormatSUB, []byte("{1}{2}hi"), Timing{OffsetMS: 1}); !errors.Is(err, ErrRetimeUnsupported) {
		t.Fatalf("err=%v", err)
	}
	if _, err := Retime(FormatSRT, nil, Timing{Scale: -1}); err == nil {
		t.Fatal("negative scale accepted")
	}
}

func TestParseCuesForFormat(t *testing.T) {
	ass := "\ufeff[Script Info]\nTitle: x\n\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n" +
		assDisplayedEventKey + " 0,0:00:05.00,0:00:06.00,Default,,0,0,0,,{\\an8}Later, then\n" +
		"Comment: 0,0:00:00.00,0:00:01.00,Default,,0,0,0,,hidden\n" +
		assDisplayedEventKey + " 0,0:00:01.50,0:00:02.25,Default,,0,0,0,,{\\i1}Hello{\\i0},\\Nworld\\hagain\n" +
		assDisplayedEventKey + " 0,0:00:03.00,0:00:04.00,Default,,0,0,0,,{\\p1}m 0 0 l 10 10{\\p0}\n"
	got, err := ParseCuesForFormat(FormatASS, []byte(ass))
	if err != nil {
		t.Fatal(err)
	}
	want := []SubtitleCue{
		{Start: 1500 * time.Millisecond, End: 2250 * time.Millisecond, Lines: []string{"Hello,", "world again"}},
		{Start: 5 * time.Second, End: 6 * time.Second, Lines: []string{"Later, then"}},
	}
	if !slices.EqualFunc(got, want, func(a, b SubtitleCue) bool {
		return a.Start == b.Start && a.End == b.End && slices.Equal(a.Lines, b.Lines)
	}) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}

	srt, err := ParseCuesForFormat(FormatSRT, []byte("1\n00:00:01,000 --> 00:00:02,000\nhi\n"))
	if err != nil || len(srt) != 1 || srt[0].Start != time.Second {
		t.Fatalf("srt cues=%v err=%v", srt, err)
	}
	if _, err := ParseCuesForFormat(FormatSUB, nil); !errors.Is(err, ErrRetimeUnsupported) {
		t.Fatalf("err=%v", err)
	}
}

func TestRetimeASSScalesOverrideTimes(t *testing.T) {
	in := "[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n" +
		assDisplayedEventKey + ` 0,0:00:10.00,0:00:12.00,Default,,0,0,0,,{\k20\kf40}La{\K100}la, {\t(0,1000,\fscx120)\move(1,2,3,4,500,1500)\fad(200,300)}hi` + "\n" +
		assDisplayedEventKey + ` 0,0:00:20.00,0:00:21.00,Default,,0,0,0,,{\fade(255,0,255,0,100,900,1000)\t(\frz10)}no ms in \t here` + "\n"
	got, err := Retime(FormatASS, []byte(in), Timing{Scale: 2})
	if err != nil {
		t.Fatal(err)
	}
	want := "[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n" +
		assDisplayedEventKey + ` 0,0:00:20.00,0:00:24.00,Default,,0,0,0,,{\k40\kf80}La{\K200}la, {\t(0,2000,\fscx120)\move(1,2,3,4,1000,3000)\fad(400,600)}hi` + "\n" +
		assDisplayedEventKey + ` 0,0:00:40.00,0:00:42.00,Default,,0,0,0,,{\fade(255,0,255,0,200,1800,2000)\t(\frz10)}no ms in \t here` + "\n"
	if string(got) != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	// An offset alone leaves relative times untouched.
	shifted, _ := Retime(FormatASS, []byte(in), Timing{OffsetMS: 1000})
	if !bytes.Contains(shifted, []byte(`{\k20\kf40}`)) {
		t.Fatalf("offset changed override times: %s", shifted)
	}
}
