package mediasample

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"math"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/processmetrics"
)

// realFFmpeg returns an ffmpeg from PATH and its capabilities, or skips.
func realFFmpeg(t *testing.T) (string, Capabilities) {
	t.Helper()
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	caps, err := LoadCapabilities(ctx, path)
	if err != nil {
		t.Skipf("ffmpeg capabilities unavailable: %v", err)
	}
	return path, caps
}

// TestRunWithRealFFmpeg samples a generated clip: tone, silence from 4 s to
// 6 s, tone.
func TestRunWithRealFFmpeg(t *testing.T) {
	ffmpeg, caps := realFFmpeg(t)
	silence := Request{Audio: &AudioOutput{Silence: &SilenceParams{NoiseDB: -50, MinSeconds: 0.5}}}
	if err := caps.Require(silence); err != nil {
		t.Skipf("ffmpeg cannot detect silence: %v", err)
	}
	clip := filepath.Join(t.TempDir(), "clip.wav")
	generate := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi",
		"-i", "aevalsrc=if(between(t\\,4\\,6)\\,0\\,0.5*sin(2*PI*440*t)):d=12:s=44100", clip)
	if output, err := generate.CombinedOutput(); err != nil {
		t.Fatalf("generate clip: %v: %s", err, output)
	}
	runner := Runner{FFmpegPath: ffmpeg, Workload: processmetrics.Analysis}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	silence.Input = clip
	silence.Window = &Window{StartSeconds: 2, DurationSeconds: 8}
	result, err := runner.Run(ctx, silence)
	if err != nil {
		t.Fatalf("silence run: %v", err)
	}
	if len(result.Silences) != 1 ||
		math.Abs(result.Silences[0].Start-4) > 0.05 || math.Abs(result.Silences[0].End-6) > 0.05 {
		t.Fatalf("silences %+v, want one from 4 s to 6 s in media time", result.Silences)
	}

	fingerprint := Request{Input: clip, Window: &Window{DurationSeconds: 12}, Audio: &AudioOutput{Fingerprint: true}, Threads: 1}
	if err := caps.Require(fingerprint); err != nil {
		t.Logf("skipping the fingerprint run: %v", err)
		return
	}
	result, err = runner.Run(ctx, fingerprint)
	if err != nil {
		t.Fatalf("fingerprint run: %v", err)
	}
	if len(result.Fingerprint) == 0 {
		t.Fatal("fingerprint run returned no points")
	}
}

// TestRunStatsWithRealFFmpeg samples keyframes of a generated clip, one per
// second, with a white bar on black, together with its audio in one run.
func TestRunStatsWithRealFFmpeg(t *testing.T) {
	ffmpeg, caps := realFFmpeg(t)
	req := Request{
		Window: &Window{StartSeconds: 2, DurationSeconds: 8, KeyframesOnly: true},
		Audio:  &AudioOutput{Silence: &SilenceParams{NoiseDB: -50, MinSeconds: 0.5}},
		Stats:  &StatsOutput{CropWidth: 0.9, CropHeight: 0.8, Width: 480, BlackThresholds: []int{20, 26, 32}},
	}
	if err := caps.Require(req); err != nil {
		t.Skipf("ffmpeg cannot sample frame statistics: %v", err)
	}
	if !caps.HasFilter("drawbox") || !caps.HasFilter("aevalsrc") {
		t.Skip("ffmpeg lacks the filters that generate the clip")
	}
	clip := filepath.Join(t.TempDir(), "clip.mkv")
	generate := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=c=black:s=640x360:r=24:d=12,drawbox=x=200:y=170:w=240:h=20:color=white:t=fill",
		"-f", "lavfi", "-i", "aevalsrc=if(between(t\\,4\\,6)\\,0\\,0.5*sin(2*PI*440*t)):d=12:s=44100",
		"-g", "24", "-shortest", clip)
	if output, err := generate.CombinedOutput(); err != nil {
		t.Skipf("cannot generate the clip: %v: %s", err, output)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	req.Input = clip
	result, err := Runner{FFmpegPath: ffmpeg, Workload: processmetrics.Analysis}.Run(ctx, req)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if n := len(result.Frames); n < 6 || n > 10 {
		t.Fatalf("got %d keyframes, want about one per second of the 8 s window", n)
	}
	for _, frame := range result.Frames {
		if frame.Seconds < 1.9 || frame.Seconds > 10.1 {
			t.Fatalf("frame at %.3f s is outside the window in media time", frame.Seconds)
		}
		if len(frame.PBlack) != 3 || frame.PBlack[2] < 85 || frame.YMax-frame.YLow < 60 || frame.SatLow >= 10 {
			t.Fatalf("frame %+v does not look like a white bar on black", frame)
		}
	}
	if len(result.Silences) != 1 || math.Abs(result.Silences[0].Start-4) > 0.05 {
		t.Fatalf("silences %+v, want one starting at 4 s", result.Silences)
	}
}

// TestRunSamplesWithRealFFmpeg samples a generated clip with a keyframe every
// second, black until 16 s and white after, every three seconds from 0.5 s,
// in containers the concat demuxer reads (Matroska, MP4, their timestamps
// shifted to start at 11.4 s) and one it cannot (MPEG-TS). Each frame must
// carry its sample time and show the picture at that time in media time; the
// sample at 15.5 s must decode the keyframe at 15 s (black), not the next one.
func TestRunSamplesWithRealFFmpeg(t *testing.T) {
	ffmpeg, caps := realFFmpeg(t)
	var seconds []float64
	for at := 0.5; at < 30; at += 3 {
		seconds = append(seconds, at)
	}
	req := Request{
		Samples: &Samples{Seconds: seconds},
		Stats:   &StatsOutput{CropWidth: 0.9, CropHeight: 0.8, Width: 160, BlackThresholds: []int{32}},
		Threads: 1,
	}
	if err := caps.Require(req); err != nil {
		t.Skipf("ffmpeg cannot sample frame statistics: %v", err)
	}
	dir := t.TempDir()
	// A path with a quote and a backslash exercises the list's escaping.
	clip := filepath.Join(dir, `it's a \ clip.mkv`)
	generate := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=c=black:s=320x240:r=24:d=16",
		"-f", "lavfi", "-i", "color=c=white:s=320x240:r=24:d=14",
		"-filter_complex", "[0:v][1:v]concat=n=2:v=1:a=0[v]", "-map", "[v]",
		"-g", "24", "-keyint_min", "24", clip)
	if output, err := generate.CombinedOutput(); err != nil {
		t.Skipf("cannot generate the clip: %v: %s", err, output)
	}
	remux := func(name string, options ...string) string {
		path := filepath.Join(dir, name)
		args := append([]string{"-hide_banner", "-loglevel", "error", "-i", clip, "-c", "copy"}, options...)
		if output, err := exec.Command(ffmpeg, append(args, path)...).CombinedOutput(); err != nil {
			t.Skipf("cannot remux the clip to %s: %v: %s", name, err, output)
		}
		return path
	}
	inputs := map[string]string{
		"matroska":         clip,
		"matroska shifted": remux("shifted.mkv", "-output_ts_offset", "11.4"),
		"mp4 shifted":      remux("shifted.mp4", "-output_ts_offset", "11.4"),
		"mpegts":           remux("clip.ts"),
	}
	for name, input := range inputs {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			req := req
			req.Input = input
			result, err := Runner{FFmpegPath: ffmpeg, Workload: processmetrics.Analysis}.Run(ctx, req)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if len(result.Frames) != len(seconds) {
				t.Fatalf("got %d frames, want one per sample (%d): %+v", len(result.Frames), len(seconds), result.Frames)
			}
			for i, frame := range result.Frames {
				if frame.Seconds != seconds[i] {
					t.Fatalf("frame %d at %.3f s, want its sample time %.3f s", i, frame.Seconds, seconds[i])
				}
				black := frame.PBlack[0] >= 90
				if wantBlack := seconds[i] < 16; black != wantBlack {
					t.Fatalf("frame for %.1f s black=%t (pblack %d), want %t", seconds[i], black, frame.PBlack[0], wantBlack)
				}
			}
		})
	}
}

// TestRunImageWithRealFFmpeg extracts single frames of a generated clip,
// black until 2 s and white after: the frame at 3 s must be white, at its
// source size and scaled, and a time past the end must fail.
func TestRunImageWithRealFFmpeg(t *testing.T) {
	ffmpeg, _ := realFFmpeg(t)
	clip := filepath.Join(t.TempDir(), "clip.mkv")
	generate := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=c=black:s=320x240:r=24:d=2",
		"-f", "lavfi", "-i", "color=c=white:s=320x240:r=24:d=2",
		"-filter_complex", "[0:v][1:v]concat=n=2:v=1:a=0[v]", "-map", "[v]", "-g", "24", clip)
	if output, err := generate.CombinedOutput(); err != nil {
		t.Skipf("cannot generate the clip: %v: %s", err, output)
	}
	runner := Runner{FFmpegPath: ffmpeg, Workload: processmetrics.Thumbnail}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, width := range []int{0, 160} {
		req := Request{Input: clip, At: &At{Seconds: 3}, Images: &ImageOutput{Width: width}}
		result, err := runner.Run(ctx, req)
		if err != nil {
			t.Fatalf("width %d: %v", width, err)
		}
		if len(result.Images) != 1 || result.Images[0].Seconds != 3 {
			t.Fatalf("width %d: images %+v, want one at 3 s", width, result.Images)
		}
		picture, err := jpeg.Decode(bytes.NewReader(result.Images[0].JPEG))
		if err != nil {
			t.Fatalf("width %d: decode image: %v", width, err)
		}
		bounds := picture.Bounds()
		wantWidth := 320
		if width > 0 {
			wantWidth = width
		}
		if bounds.Dx() != wantWidth {
			t.Fatalf("image is %d px wide, want %d", bounds.Dx(), wantWidth)
		}
		if luma, _, _, _ := picture.At(bounds.Dx()/2, bounds.Dy()/2).RGBA(); luma < 0xe000 {
			t.Fatalf("width %d: frame at 3 s is not white (red %#x)", width, luma)
		}
	}
	// Depending on the version, ffmpeg writes nothing and succeeds (empty) or
	// fails because nothing was written (exit).
	_, err := runner.Run(ctx, Request{Input: clip, At: &At{Seconds: 60}, Images: &ImageOutput{}})
	var runErr *Error
	if !errors.As(err, &runErr) || (runErr.Reason != ReasonEmpty && runErr.Reason != ReasonExit) {
		t.Fatalf("frame past the end: error %v, want no image", err)
	}
}

// TestRunSheetsWithRealFFmpeg tiles a generated 2.4:1 clip of six ten-second
// gray scenes, keyframes every two seconds, sampled at each scene's middle,
// into two 2x2 sheets: in Matroska and in MP4 with timestamps shifted to
// start at 11.4 s (read through a list), in MPEG-TS (read as a window), and
// read through as one window. Each cell must show its scene's gray at full
// range, and the cells after the last sample black.
func TestRunSheetsWithRealFFmpeg(t *testing.T) {
	ffmpeg, caps := realFFmpeg(t)
	grays := []int{20, 60, 100, 140, 180, 220}
	var sources []string
	var inputs []string
	for i, gray := range grays {
		sources = append(sources, "-f", "lavfi", "-i", fmt.Sprintf("color=c=0x%02x%02x%02x:s=640x266:r=24:d=10", gray, gray, gray))
		inputs = append(inputs, fmt.Sprintf("[%d:v]", i))
	}
	dir := t.TempDir()
	clip := filepath.Join(dir, "clip.mkv")
	args := append([]string{"-hide_banner", "-loglevel", "error"}, sources...)
	args = append(args, "-filter_complex", strings.Join(inputs, "")+"concat=n=6:v=1:a=0,format=yuv420p[v]", "-map", "[v]",
		"-g", "48", "-keyint_min", "48", clip)
	if output, err := exec.Command(ffmpeg, args...).CombinedOutput(); err != nil {
		t.Skipf("cannot generate the clip: %v: %s", err, output)
	}
	remux := func(name string, options ...string) string {
		path := filepath.Join(dir, name)
		args := append([]string{"-hide_banner", "-loglevel", "error", "-i", clip, "-c", "copy"}, options...)
		if output, err := exec.Command(ffmpeg, append(args, path)...).CombinedOutput(); err != nil {
			t.Skipf("cannot remux the clip to %s: %v: %s", name, err, output)
		}
		return path
	}
	base := Request{
		Samples: &Samples{Seconds: []float64{5, 15, 25, 35, 45, 55}},
		Sheets:  &SheetsOutput{TileWidth: 64, TileHeight: 26, Columns: 2, Rows: 2, Quality: 90},
		Threads: 1,
	}
	if err := caps.Require(base); err != nil {
		t.Skipf("ffmpeg cannot sample sheets: %v", err)
	}
	cases := map[string]Request{}
	for name, input := range map[string]string{
		"matroska":    clip,
		"mp4 shifted": remux("shifted.mp4", "-output_ts_offset", "11.4"),
		"mpegts":      remux("clip.ts"),
	} {
		req := base
		req.Input = input
		cases[name] = req
	}
	readThrough := base
	readThrough.Input = clip
	readThrough.Samples = &Samples{Seconds: base.Samples.Seconds, ReadThrough: true}
	cases["read through"] = readThrough

	want := [][]int{{20, 60, 100, 140}, {180, 220, 0, 0}}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			result, err := Runner{FFmpegPath: ffmpeg, Workload: processmetrics.Thumbnail}.Run(ctx, req)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if len(result.Sheets) != 2 || result.SheetFrames != (SheetFrames{Decoded: 6}) {
				t.Fatalf("got %d sheets, frames %+v; want 2 sheets of 6 decoded samples", len(result.Sheets), result.SheetFrames)
			}
			for i, sheet := range result.Sheets {
				img, err := jpeg.Decode(bytes.NewReader(sheet.JPEG))
				if err != nil {
					t.Fatalf("sheet %d: %v", i, err)
				}
				if size := img.Bounds().Size(); size.X != 128 || size.Y != 52 {
					t.Fatalf("sheet %d is %v, want 128x52", i, size)
				}
				ycc := img.(*image.YCbCr)
				for cell, gray := range want[i] {
					x, y := (cell%2)*64+32, (cell/2)*26+13
					if got := int(ycc.Y[ycc.YOffset(x, y)]); got < gray-4 || got > gray+4 {
						t.Fatalf("sheet %d cell %d luma %d, want %d", i, cell, got, gray)
					}
				}
			}
		})
	}
}

// TestRunSheetsOpenGOPHEVCWithRealFFmpeg samples open-GOP HEVC clips whose
// keyframes after the first are CRA pictures, at 25.6 fps with samples ten
// seconds apart: 256 frames, a whole cycle of the 8-bit picture order count.
// After each jump through a list the decoder computes the previous
// keyframe's count again and drops the keyframe as a duplicate. With a
// keyframe every 64 frames, the window that reads the samples again decodes
// every keyframe, and every sample must show its scene. With a keyframe every
// 256 frames the keyframes collide in the window too, since skipped pictures
// do not advance the count, and the run must fail rather than repeat the
// first scene.
func TestRunSheetsOpenGOPHEVCWithRealFFmpeg(t *testing.T) {
	ffmpeg, caps := realFFmpeg(t)
	grays := []int{20, 60, 100, 140, 180, 220}
	var sources []string
	var inputs []string
	for i, gray := range grays {
		sources = append(sources, "-f", "lavfi", "-i", fmt.Sprintf("color=c=0x%02x%02x%02x:s=128x72:r=128/5:d=10", gray, gray, gray))
		inputs = append(inputs, fmt.Sprintf("[%d:v]", i))
	}
	for _, keyint := range []int{64, 256} {
		t.Run(fmt.Sprintf("keyint %d", keyint), func(t *testing.T) {
			clip := filepath.Join(t.TempDir(), "open-gop.mkv")
			args := append([]string{"-hide_banner", "-loglevel", "error"}, sources...)
			args = append(args, "-filter_complex", strings.Join(inputs, "")+"concat=n=6:v=1:a=0,format=yuv420p[v]", "-map", "[v]",
				"-c:v", "libx265", "-preset", "ultrafast",
				"-x265-params", fmt.Sprintf("keyint=%d:min-keyint=%d:scenecut=0:open-gop=1:bframes=3:log2-max-poc-lsb=8:log-level=error", keyint, keyint), clip)
			if output, err := exec.Command(ffmpeg, args...).CombinedOutput(); err != nil {
				t.Skipf("cannot encode the clip with libx265: %v: %s", err, output)
			}
			req := Request{
				Input:   clip,
				Samples: &Samples{Seconds: []float64{5, 15, 25, 35, 45, 55}},
				Sheets:  &SheetsOutput{TileWidth: 64, TileHeight: 36, Columns: 3, Rows: 2, Quality: 90},
				Threads: 1,
			}
			if err := caps.Require(req); err != nil {
				t.Skipf("ffmpeg cannot sample sheets: %v", err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			result, err := Runner{FFmpegPath: ffmpeg, Workload: processmetrics.Trickplay}.Run(ctx, req)
			if keyint == 256 {
				if failure, ok := errors.AsType[*Error](err); !ok || failure.Reason != ReasonEmpty {
					t.Fatalf("got frames %+v, error %v; want the run to fail as empty", result.SheetFrames, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if len(result.Sheets) != 1 || result.SheetFrames != (SheetFrames{Decoded: 6}) {
				t.Fatalf("got %d sheets, frames %+v; want 1 sheet of 6 decoded samples", len(result.Sheets), result.SheetFrames)
			}
			img, err := jpeg.Decode(bytes.NewReader(result.Sheets[0].JPEG))
			if err != nil {
				t.Fatal(err)
			}
			ycc := img.(*image.YCbCr)
			for cell, gray := range grays {
				x, y := (cell%3)*64+32, (cell/3)*36+18
				if got := int(ycc.Y[ycc.YOffset(x, y)]); got < gray-6 || got > gray+6 {
					t.Fatalf("cell %d luma %d, want %d", cell, got, gray)
				}
			}
		})
	}
}

// TestRunSheetsRejectsPrematureEOFWithRealFFmpeg requests an eight-second
// timeline from a two-second MPEG-TS source. A complete source's final
// eight-second GOP must still provide every preview, including when the
// sample window and the container timestamps start after zero.
func TestRunSheetsRejectsPrematureEOFWithRealFFmpeg(t *testing.T) {
	ffmpeg, _ := realFFmpeg(t)
	for _, shift := range []int{0, 10} {
		for _, offset := range []float64{0, 11.4} {
			for _, duration := range []int{2, 8} {
				t.Run(fmt.Sprintf("sample-shift-%d/container-offset-%g/duration-%d", shift, offset, shift+duration), func(t *testing.T) {
					clip := filepath.Join(t.TempDir(), "clip.ts")
					args := []string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i",
						fmt.Sprintf("color=c=gray:s=64x36:r=24:d=%d", shift+duration), "-c:v", "libx264",
						"-threads", "1", "-g", "240", "-keyint_min", "240", "-sc_threshold", "0", "-bf", "2",
						"-output_ts_offset", formatSeconds(offset), clip}
					if output, err := exec.Command(ffmpeg, args...).CombinedOutput(); err != nil {
						t.Skipf("cannot generate the clip: %v: %s", err, output)
					}
					if offset > 0 {
						output, err := exec.Command(ffmpeg, probeArgs(clip)...).CombinedOutput()
						if err != nil {
							t.Fatalf("probe shifted clip: %v: %s", err, output)
						}
						header := &inputHeaderParser{}
						for line := range strings.SplitSeq(string(output), "\n") {
							header.line(line)
						}
						if header.info.StartSeconds < offset {
							t.Fatalf("container starts at %g, want at least %g", header.info.StartSeconds, offset)
						}
					}
					for _, readThrough := range []bool{false, true} {
						t.Run(fmt.Sprintf("read-through-%t", readThrough), func(t *testing.T) {
							req := Request{Input: clip, Samples: &Samples{Seconds: secondsFrom(float64(shift+1), 2, 4), ReadThrough: readThrough},
								Sheets: &SheetsOutput{TileWidth: 24, TileHeight: 16, Columns: 2, Rows: 2, Quality: 80}, Threads: 1}
							ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
							defer cancel()
							result, err := (Runner{FFmpegPath: ffmpeg}).Run(ctx, req)
							if duration == 2 {
								failure, ok := errors.AsType[*Error](err)
								if !ok || failure.Reason != ReasonEmpty {
									t.Fatalf("frames %+v error %v, want sparse output rejected", result.SheetFrames, err)
								}
							} else if err != nil || result.SheetFrames != (SheetFrames{Decoded: 4}) {
								t.Fatalf("valid final GOP: frames %+v error %v", result.SheetFrames, err)
							}
						})
					}
				})
			}
		}
	}
}

// TestRunSheetsToneMapsHDRWithRealFFmpeg tiles a generated 10-bit PQ clip
// with software tone mapping; the sheet must come out, not black.
func TestRunSheetsToneMapsHDRWithRealFFmpeg(t *testing.T) {
	ffmpeg, caps := realFFmpeg(t)
	if !caps.HasFilter("zscale") {
		t.Skip("ffmpeg lacks zscale")
	}
	clip := filepath.Join(t.TempDir(), "hdr.mkv")
	generate := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=s=1280x720:r=24:d=6",
		"-pix_fmt", "yuv420p10le", "-c:v", "libx265", "-preset", "ultrafast",
		"-x265-params", "log-level=error:keyint=24:colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc",
		clip)
	if output, err := generate.CombinedOutput(); err != nil {
		t.Skipf("cannot generate an HDR clip: %v: %s", err, output)
	}
	req := Request{
		Input:   clip,
		Samples: &Samples{Seconds: []float64{1, 3, 5}},
		Sheets:  &SheetsOutput{TileWidth: 160, TileHeight: 90, Columns: 3, Rows: 1, Quality: 80, ToneMap: &ToneMap{AllowSoftware: true}},
		Threads: 1,
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	result, err := Runner{FFmpegPath: ffmpeg, Workload: processmetrics.Thumbnail}.Run(ctx, req)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Sheets) != 1 || result.SheetFrames.Decoded != 3 {
		t.Fatalf("got %d sheets, frames %+v", len(result.Sheets), result.SheetFrames)
	}
	img, err := jpeg.Decode(bytes.NewReader(result.Sheets[0].JPEG))
	if err != nil {
		t.Fatal(err)
	}
	ycc := img.(*image.YCbCr)
	var sum int
	for _, y := range ycc.Y {
		sum += int(y)
	}
	if mean := sum / len(ycc.Y); mean < 30 {
		t.Fatalf("tone-mapped sheet has mean luma %d, want a visible picture", mean)
	}
}
