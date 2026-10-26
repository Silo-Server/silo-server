package chapterthumbs

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"reflect"
	"testing"
	"time"
)

// goldenExtraction is one recorded extraction: the arguments and timeout of
// each attempt, and the final reason, when every attempt fails.
type goldenExtraction struct {
	Name     string     `json:"name"`
	Attempts [][]string `json:"attempts"`
	Timeouts []float64  `json:"timeouts"`
	Reason   string     `json:"reason"`
}

// TestExtractFrameArgumentsMatchGolden pins every hardware, tone-map, SDR,
// and HDR case to the ffmpeg arguments, attempt timeouts, and reasons that
// chapter thumbnails produced before they moved onto mediasample
// (testdata/extract_argv_golden.json, recorded from the previous extractor).
// Every attempt fails with a generic error so that each fallback runs.
func TestExtractFrameArgumentsMatchGolden(t *testing.T) {
	data, err := os.ReadFile("testdata/extract_argv_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden []goldenExtraction
	if err := json.Unmarshal(data, &golden); err != nil {
		t.Fatal(err)
	}
	bt2390 := []string{"zscale", "tonemapx"}
	hable := []string{"zscale", "tonemap"}
	sdr := func(input string, seek float64, accel, device string) FrameExtractOptions {
		return FrameExtractOptions{InputPath: input, SeekSeconds: seek, HWAccel: accel, HWDevice: device}
	}
	hdr := func(accel, device string, allowSoftware bool) FrameExtractOptions {
		return FrameExtractOptions{InputPath: "/media/hdr.mkv", SeekSeconds: 42.5, HWAccel: accel, HWDevice: device, ToneMap: true, AllowSoftwareToneMap: allowSoftware}
	}
	cases := map[string]struct {
		opts    FrameExtractOptions
		filters []string
	}{
		"none sdr":                  {opts: sdr("/media/movie.mkv", 42.5, "none", "")},
		"none sdr rounding":         {opts: sdr("/media/a b/movie's.mkv", 1234.56789, "none", "")},
		"none sdr zero":             {opts: sdr("/media/movie.mkv", 0, "none", "")},
		"none hdr bt2390":           {opts: hdr("none", "", true), filters: bt2390},
		"none hdr hable":            {opts: hdr("none", "", true), filters: hable},
		"none hdr disabled":         {opts: hdr("none", "", false), filters: bt2390},
		"empty accel sdr":           {opts: sdr("/media/movie.mkv", 42.5, "", "")},
		"nvenc sdr":                 {opts: sdr("/media/movie.mkv", 42.5, "nvenc", "0")},
		"nvenc hdr bt2390":          {opts: hdr("nvenc", "", true), filters: bt2390},
		"vaapi sdr":                 {opts: sdr("/media/movie.mkv", 42.5, "vaapi", "/dev/dri/renderD128")},
		"vaapi hdr bt2390":          {opts: hdr("vaapi", "/dev/dri/renderD128", true), filters: bt2390},
		"vaapi hdr hardware only":   {opts: hdr("vaapi", "/dev/dri/renderD128", false), filters: bt2390},
		"qsv sdr":                   {opts: sdr("/media/movie.mkv", 42.5, "qsv", "/dev/dri/renderD129")},
		"qsv hdr hable":             {opts: hdr("qsv", "/dev/dri/renderD129", true), filters: hable},
		"videotoolbox sdr":          {opts: sdr("/media/movie.mkv", 42.5, "videotoolbox", "")},
		"videotoolbox hdr bt2390":   {opts: hdr("videotoolbox", "", true), filters: bt2390},
		"videotoolbox hdr hable":    {opts: hdr("videotoolbox", "", true), filters: hable},
		"videotoolbox hdr disabled": {opts: hdr("videotoolbox", "", false), filters: bt2390},
	}
	if len(golden) != len(cases) {
		t.Fatalf("golden file has %d cases, test has %d", len(golden), len(cases))
	}
	for _, want := range golden {
		t.Run(want.Name, func(t *testing.T) {
			tc, ok := cases[want.Name]
			if !ok {
				t.Fatalf("no options for golden case %q", want.Name)
			}
			var got goldenExtraction
			opts := tc.opts
			opts.FFmpegPath = "/test/ffmpeg"
			opts.loadCapabilities = capabilitiesWithFilters(t, tc.filters...)
			opts.RunFunc = func(ctx context.Context, _ string, args []string) ([]byte, error) {
				deadline, ok := ctx.Deadline()
				if !ok {
					t.Fatal("attempt has no deadline")
				}
				got.Attempts = append(got.Attempts, append([]string(nil), args...))
				got.Timeouts = append(got.Timeouts, math.Round(time.Until(deadline).Seconds()))
				return nil, errors.New("boom")
			}
			_, reason, err := ExtractFrame(context.Background(), opts)
			if err == nil {
				t.Fatal("ExtractFrame() succeeded, want every attempt to fail")
			}
			// An extraction refused before ffmpeg starts has no attempts.
			if len(got.Attempts)+len(want.Attempts) > 0 && !reflect.DeepEqual(got.Attempts, want.Attempts) {
				t.Fatalf("attempt args\n got %q\nwant %q", got.Attempts, want.Attempts)
			}
			if len(got.Timeouts)+len(want.Timeouts) > 0 && !reflect.DeepEqual(got.Timeouts, want.Timeouts) {
				t.Fatalf("attempt timeouts %v, want %v", got.Timeouts, want.Timeouts)
			}
			if reason != want.Reason {
				t.Fatalf("reason %q, want %q", reason, want.Reason)
			}
		})
	}
}
