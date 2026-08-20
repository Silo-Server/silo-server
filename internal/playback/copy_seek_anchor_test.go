package playback

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestResolveCopySeekAnchorUsesKeyPacketTimestamp(t *testing.T) {
	dir := t.TempDir()
	ffmpegPath := filepath.Join(dir, "ffmpeg")
	ffprobePath := filepath.Join(dir, "ffprobe")
	argsPath := filepath.Join(dir, "ffprobe-args")
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	probe := `#!/bin/sh
printf '%s\n' "$@" > "` + argsPath + `"
printf '%s' '{"packets":[{"pts_time":"N/A","dts_time":"14.500000","flags":"K__"}]}'
`
	if err := os.WriteFile(ffprobePath, []byte(probe), 0o755); err != nil {
		t.Fatalf("write fake ffprobe: %v", err)
	}

	anchor, segment, err := ResolveCopySeekAnchor(context.Background(), ffmpegPath, "/media/movie.mkv", 18.261, 2)
	if err != nil {
		t.Fatalf("ResolveCopySeekAnchor: %v", err)
	}
	if anchor != 14.5 || segment != 7 {
		t.Fatalf("resolved anchor = %v, segment = %d; want 14.5, 7", anchor, segment)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read ffprobe args: %v", err)
	}
	if !strings.Contains(string(args), "-select_streams\nV:0\n") {
		t.Fatalf("ffprobe did not select the remux video stream:\n%s", args)
	}
}

func TestResolveCopySeekAnchorCoalescesMatchingConcurrentProbes(t *testing.T) {
	dir := t.TempDir()
	ffmpegPath := filepath.Join(dir, "ffmpeg")
	ffprobePath := filepath.Join(dir, "ffprobe")
	countPath := filepath.Join(dir, "probe-count")
	if err := os.WriteFile(ffmpegPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	probe := "#!/bin/sh\n" +
		"printf x >> \"" + countPath + "\"\n" +
		"sleep 0.1\n" +
		"printf '%s' '{\"packets\":[{\"pts_time\":\"16.000000\",\"flags\":\"K__\"}]}'\n"
	if err := os.WriteFile(ffprobePath, []byte(probe), 0o755); err != nil {
		t.Fatalf("write fake ffprobe: %v", err)
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for range 2 {
		go func() {
			ready.Done()
			<-start
			anchor, segment, err := ResolveCopySeekAnchor(context.Background(), ffmpegPath, "/media/movie.mkv", 18, 2)
			if err == nil && (anchor != 16 || segment != 8) {
				err = fmt.Errorf("resolved anchor = %v, segment = %d", anchor, segment)
			}
			errs <- err
		}()
	}
	ready.Wait()
	close(start)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	count, err := os.ReadFile(countPath)
	if err != nil {
		t.Fatalf("read probe count: %v", err)
	}
	if string(count) != "x" {
		t.Fatalf("ffprobe executions = %d, want 1", len(count))
	}
}

func TestResolveCopySeekAnchorMatchesRealLongGOPHEVC(t *testing.T) {
	if testing.Short() {
		t.Skip("real FFmpeg integration test")
	}
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	if _, err := exec.LookPath(ffprobePathFromFFmpeg(ffmpegPath)); err != nil {
		t.Skip("ffprobe is not installed beside ffmpeg")
	}
	encoders, err := exec.Command(ffmpegPath, "-hide_banner", "-encoders").CombinedOutput()
	if err != nil || !strings.Contains(string(encoders), "libx265") {
		t.Skip("ffmpeg does not provide libx265")
	}

	sourcePath := filepath.Join(t.TempDir(), "long-gop-hevc.mkv")
	encodeCtx, cancelEncode := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelEncode()
	encode := exec.CommandContext(encodeCtx, ffmpegPath,
		"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24",
		"-t", "22",
		"-c:v", "libx265", "-preset", "ultrafast",
		"-x265-params", "keyint=240:min-keyint=240:scenecut=0:log-level=error:pools=1:frame-threads=1",
		"-an", "-y", sourcePath,
	)
	if output, err := encode.CombinedOutput(); err != nil {
		t.Fatalf("generate long-GOP HEVC fixture: %v\n%s", err, output)
	}

	probeCtx, cancelProbe := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelProbe()
	anchor, segment, err := ResolveCopySeekAnchor(probeCtx, ffmpegPath, sourcePath, 18.261, 2)
	if err != nil {
		t.Fatalf("ResolveCopySeekAnchor: %v", err)
	}
	if math.Abs(anchor-10) > 0.001 || segment != 5 {
		t.Fatalf("resolved anchor = %v, segment = %d; want 10, 5", anchor, segment)
	}

	outputDir := filepath.Join(t.TempDir(), "hls")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatalf("create HLS output: %v", err)
	}
	opts := TranscodeOpts{
		InputPath:              sourcePath,
		OutputDir:              outputDir,
		SessionID:              "copy-anchor-integration",
		SourceVideoCodec:       "hevc",
		SeekSeconds:            18.261,
		StreamOriginSeconds:    anchor,
		CopySeekAnchorResolved: true,
		TargetCodecVideo:       "copy",
		TargetCodecAudio:       "copy",
		SegmentDuration:        2,
		StartSegmentNumber:     segment,
		FFmpegPath:             ffmpegPath,
	}
	packageCtx, cancelPackage := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelPackage()
	packageHLS := exec.CommandContext(packageCtx, ffmpegPath, buildFFmpegArgs(opts)...)
	if output, err := packageHLS.CombinedOutput(); err != nil {
		t.Fatalf("package copy HLS: %v\n%s", err, output)
	}

	manifestPath := filepath.Join(outputDir, "stream.m3u8")
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read copy HLS manifest: %v", err)
	}
	if !strings.Contains(string(manifest), "#EXT-X-MEDIA-SEQUENCE:5") {
		t.Fatalf("copy HLS media sequence does not use resolved anchor:\n%s", manifest)
	}
	firstFrame, err := exec.Command(ffprobePathFromFFmpeg(ffmpegPath),
		"-v", "error",
		"-select_streams", "v:0",
		"-read_intervals", "%+0.1",
		"-show_entries", "frame=best_effort_timestamp_time,key_frame",
		"-of", "csv=p=0",
		manifestPath,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("probe copy HLS first frame: %v\n%s", err, firstFrame)
	}
	if !strings.Contains(string(firstFrame), "10.000000") {
		t.Fatalf("copy HLS first frame = %q, want resolved 10-second keyframe", firstFrame)
	}
}
