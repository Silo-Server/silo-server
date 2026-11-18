package playback

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/fspath"
	"github.com/Silo-Server/silo-server/internal/nodesource/nodesourcetest"
)

// A transcode node with core source access hands every consumer a relayed
// input, a loopback relay URL, instead of a library path. These tests run the
// consumers on both and require the same output, frame for frame.

// relayedTestSource is a 12-second Matroska file with H.264 (a keyframe a
// second), AAC and an ASS track with an attached font.
func relayedTestSource(t *testing.T, ffmpeg string) string {
	t.Helper()
	font := testFontFile(t)
	dir := t.TempDir()
	ass := filepath.Join(dir, "subs.ass")
	if err := os.WriteFile(ass, []byte(burnInTestASS), 0o600); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "source.mkv")
	out, err := exec.Command(ffmpeg, "-nostdin", "-v", "error",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=25:duration=12",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=12", "-i", ass,
		"-map", "0:v", "-map", "1:a", "-map", "2:s",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "25", "-bf", "2", "-c:a", "aac", "-c:s", "ass",
		"-attach", font, "-metadata:s:t", "mimetype=application/x-truetype-font", src).CombinedOutput()
	if err != nil {
		t.Skipf("ffmpeg can't make the test file (%v): %s", err, out)
	}
	return src
}

// frameHashes decodes input's video (from seek, for duration seconds when
// positive) through fspath, as consumers launch FFmpeg, and returns its
// framemd5 lines without the header.
func frameHashes(t *testing.T, ffmpeg, input string, seek, duration float64) []string {
	t.Helper()
	args := []string{"-nostdin", "-v", "error"}
	if seek > 0 {
		args = append(args, "-ss", formatFloat(seek))
	}
	args = append(args, "-i", input)
	if duration > 0 {
		args = append(args, "-t", formatFloat(duration))
	}
	args = append(args, "-map", "0:v:0", "-f", "framemd5", "-")
	out, err := fspath.CommandContext(t.Context(), ffmpeg, args...).Output()
	if err != nil {
		t.Fatalf("decode %s: %v", fspath.RedactRelayed(input), err)
	}
	var lines []string
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		if !strings.HasPrefix(line, "#") {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		t.Fatalf("decode %s: no frames", fspath.RedactRelayed(input))
	}
	return lines
}

func formatFloat(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

//nolint:funlen,gocyclo // One fixture through every consumer; the subtests share its relay.
func TestRelayedSourceMatchesTheFileRealFFmpeg(t *testing.T) {
	if testing.Short() {
		t.Skip("real FFmpeg integration test")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	src := relayedTestSource(t, ffmpeg)
	core := nodesourcetest.NewCore(t, src)
	relayed := core.Open(t).Input()
	if !fspath.IsRelayed(relayed) {
		t.Fatalf("input %q is not relayed", relayed)
	}
	timings := map[string][2]time.Duration{}
	timed := func(name string, local, remote func()) {
		started := time.Now()
		local()
		mid := time.Now()
		remote()
		timings[name] = [2]time.Duration{mid.Sub(started), time.Since(mid)}
	}
	t.Cleanup(func() {
		for name, d := range timings {
			t.Logf("%s: local %s, core %s", name, d[0].Round(time.Millisecond), d[1].Round(time.Millisecond))
		}
	})

	t.Run("decode and seek", func(t *testing.T) {
		for _, seek := range []float64{0, 4.4, 9} {
			var want, got []string
			timed("decode from "+formatFloat(seek), func() { want = frameHashes(t, ffmpeg, src, seek, 2) },
				func() { got = frameHashes(t, ffmpeg, relayed, seek, 2) })
			if !slices.Equal(got, want) {
				t.Fatalf("seek %v: relayed frames differ", seek)
			}
		}
	})

	t.Run("copy remux", func(t *testing.T) {
		remux := func(input string) []string {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequestWithContext(t.Context(), "GET", "/remux", nil)
			if err := ServeRemuxWithOptions(recorder, request, input, "mp4", 3, false, -1, 0, RemuxServeOptions{FFmpegPath: ffmpeg}); err != nil {
				t.Fatalf("remux %s: %v", fspath.RedactRelayed(input), err)
			}
			out := filepath.Join(t.TempDir(), "remux.mp4")
			if err := os.WriteFile(out, recorder.Body.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			return frameHashes(t, ffmpeg, out, 0, 0)
		}
		var want, got []string
		timed("copy remux", func() { want = remux(src) }, func() { got = remux(relayed) })
		if !slices.Equal(got, want) {
			t.Fatalf("relayed remux has %d frames, the file's %d, or they differ", len(got), len(want))
		}
	})

	t.Run("download preparation", func(t *testing.T) {
		prepare := func(input string) []string {
			out := filepath.Join(t.TempDir(), "prepared.mp4")
			err := PrepareFile(t.Context(), TranscodeOpts{InputPath: input, FFmpegPath: ffmpeg, HWAccel: HWAccelNone,
				TargetCodecVideo: "copy", TargetCodecAudio: "aac", AudioTrackIndex: 0, SubtitleTrackIndex: -1, TotalDuration: 12}, out)
			if err != nil {
				t.Fatalf("prepare %s: %v", fspath.RedactRelayed(input), err)
			}
			return frameHashes(t, ffmpeg, out, 0, 0)
		}
		var want, got []string
		timed("download preparation", func() { want = prepare(src) }, func() { got = prepare(relayed) })
		if !slices.Equal(got, want) {
			t.Fatalf("relayed preparation differs")
		}
	})

	t.Run("HLS copy with a keyframe-planned playlist", func(t *testing.T) {
		session := func(input string) (durations []float64, segments [][]byte) {
			opts := TranscodeOpts{InputPath: input, OutputDir: filepath.Join(t.TempDir(), "out"), SessionID: "relayed-planned",
				SourceVideoCodec: "h264", TargetCodecVideo: "copy", TargetCodecAudio: "copy", SegmentDuration: 2, TotalDuration: 12,
				KeyframePlaylist: true, FFmpegPath: ffmpeg, HWAccel: HWAccelNone, SubtitleTrackIndex: -1}
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			s, err := StartTranscode(ctx, opts)
			if err != nil {
				t.Fatalf("start %s: %v", fspath.RedactRelayed(input), err)
			}
			defer func() { _ = s.Close() }()
			if s.copyPlan == nil {
				t.Fatalf("%s: no keyframe plan; the index was not read", fspath.RedactRelayed(input))
			}
			select {
			case <-s.Done():
			case <-ctx.Done():
				t.Fatal("the session did not finish")
			}
			for n := range s.copyPlan.durations {
				data, err := os.ReadFile(filepath.Join(opts.OutputDir, plannedSegmentName(n)))
				if err != nil {
					t.Fatal(err)
				}
				segments = append(segments, data)
			}
			return s.copyPlan.durations, segments
		}
		var wantDurations, gotDurations []float64
		var want, got [][]byte
		timed("HLS copy", func() { wantDurations, want = session(src) }, func() { gotDurations, got = session(relayed) })
		if !slices.Equal(gotDurations, wantDurations) || len(got) != len(want) {
			t.Fatalf("relayed plan %v, the file's %v", gotDurations, wantDurations)
		}
		for n := range want {
			if !bytes.Equal(got[n], want[n]) {
				t.Fatalf("segment %d differs", n)
			}
		}
	})

	t.Run("text burn-in from the staged track", func(t *testing.T) {
		stage := func(input string) TranscodeOpts {
			opts := burnInTestOpts(t, ffmpeg, input)
			if err := prepareSubtitleFilterInput(t.Context(), &opts); err != nil {
				t.Fatalf("stage %s: %v", fspath.RedactRelayed(input), err)
			}
			if opts.subtitleFilterFontsDir == "" {
				t.Fatalf("%s: the track was not staged", fspath.RedactRelayed(input))
			}
			return opts
		}
		local, remote := stage(src), stage(relayed)
		wantTrack, err := os.ReadFile(local.subtitleFilterInputPath)
		if err != nil {
			t.Fatal(err)
		}
		gotTrack, err := os.ReadFile(remote.subtitleFilterInputPath)
		if err != nil || !bytes.Equal(gotTrack, wantTrack) {
			t.Fatalf("relayed staged track differs (%v)", err)
		}
		fonts, err := os.ReadDir(remote.subtitleFilterFontsDir)
		if err != nil || len(fonts) != 1 {
			t.Fatalf("relayed staging wrote %d fonts (%v), want the one attached", len(fonts), err)
		}
		for _, seek := range []float64{1.5, 3.5} {
			if got, want := burnInTestFrame(t, ffmpeg, src, seek, textSubtitleFilter(remote)), burnInTestFrame(t, ffmpeg, src, seek, textSubtitleFilter(local)); got != want {
				t.Fatalf("seek %v: the relayed staging draws %s, the file's %s", seek, got, want)
			}
		}
	})

	t.Run("the libass fallback is refused for a relayed source", func(t *testing.T) {
		// A plan whose codec does not match the index cannot be staged.
		unstageable := func(input string) (TranscodeOpts, error) {
			opts := burnInTestOpts(t, ffmpeg, input)
			opts.SubtitleCodec = "subrip"
			err := prepareSubtitleFilterInput(t.Context(), &opts)
			return opts, err
		}
		local, err := unstageable(src)
		if err != nil || local.subtitleFilterInputPath != filepath.Join(local.OutputDir, subtitleFilterAliasName) {
			t.Fatalf("a local source takes the fallback: %v %q", err, local.subtitleFilterInputPath)
		}
		remote, err := unstageable(relayed)
		if !errors.Is(err, ErrRelayedSubtitleBurnIn) {
			t.Fatalf("a relayed source: %v, want ErrRelayedSubtitleBurnIn", err)
		}
		if _, statErr := os.Lstat(filepath.Join(remote.OutputDir, subtitleFilterAliasName)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("an alias was made for a relayed source (%v)", statErr)
		}
	})

	if core.Requests.Load() == 0 {
		t.Fatal("nothing was read through the core")
	}
}
