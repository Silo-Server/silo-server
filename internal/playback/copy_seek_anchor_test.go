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
	argsPath := filepath.Join(dir, "ffmpeg-args")
	probe := `#!/bin/sh
printf '%s\n' "$@" > "` + argsPath + `"
printf '%s\n' '#tb 0: 1/1000'
printf '%s\n' '0,      14500,      14750,       41,   178989, 0xd16e41c4'
`
	if err := os.WriteFile(ffmpegPath, []byte(probe), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}

	anchor, segment, err := ResolveCopySeekAnchor(context.Background(), ffmpegPath, "/media/movie.mkv", 18.261, 2)
	if err != nil {
		t.Fatalf("ResolveCopySeekAnchor: %v", err)
	}
	if anchor != 14.75 || segment != 7 {
		t.Fatalf("resolved anchor = %v, segment = %d; want 14.75, 7", anchor, segment)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read ffmpeg args: %v", err)
	}
	for _, want := range []string{"-fflags\n+genpts+fastseek\n", "-analyzeduration\n3000000\n", "-probesize\n5000000\n", "-ss\n18.261\n", "-map\n0:V:0\n", "-c:v\ncopy\n", "-copyts\n", "-avoid_negative_ts\ndisabled\n", "-frames:v\n1\n", "-f\nframecrc\n"} {
		if !strings.Contains(string(args), want) {
			t.Fatalf("ffmpeg args missing %q:\n%s", want, args)
		}
	}
}

func TestResolveCopySeekAnchorCoalescesMatchingConcurrentProbes(t *testing.T) {
	dir := t.TempDir()
	ffmpegPath := filepath.Join(dir, "ffmpeg")
	countPath := filepath.Join(dir, "probe-count")
	probe := "#!/bin/sh\n" +
		"printf x >> \"" + countPath + "\"\n" +
		"sleep 0.1\n" +
		"printf '%s\\n' '#tb 0: 1/1000'\n" +
		"printf '%s\\n' '0,      15833,      16000,       41,   178989, 0xd16e41c4'\n"
	if err := os.WriteFile(ffmpegPath, []byte(probe), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
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
		t.Fatalf("ffmpeg probe executions = %d, want 1", len(count))
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

	probeCtx, cancelProbe := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelProbe()
	anchor, segment, err := ResolveCopySeekAnchor(probeCtx, ffmpegPath, sourcePath, 18.261, 2)
	if err != nil {
		t.Fatalf("ResolveCopySeekAnchor: %v", err)
	}
	if math.Abs(anchor-10) > 0.001 || segment != 5 {
		t.Fatalf("resolved anchor = %v, segment = %d; want 10, 5", anchor, segment)
	}

	// An exact keyframe seek is the recovery boundary that regressed in #839:
	// Matroska emits the preceding keyframe while MP4 starts at the requested
	// keyframe. The resolver must model FFmpeg's real copy path for both.
	exactMKVAnchor, exactMKVSegment, err := ResolveCopySeekAnchor(probeCtx, ffmpegPath, sourcePath, 10, 2)
	if err != nil {
		t.Fatalf("ResolveCopySeekAnchor exact MKV keyframe: %v", err)
	}
	if math.Abs(exactMKVAnchor) > 0.001 || exactMKVSegment != 0 {
		t.Fatalf("exact MKV anchor = %v, segment = %d; want 0, 0", exactMKVAnchor, exactMKVSegment)
	}

	mp4Path := filepath.Join(t.TempDir(), "long-gop-hevc.mp4")
	remux := exec.Command(ffmpegPath,
		"-hide_banner", "-loglevel", "error",
		"-i", sourcePath,
		"-map", "0:V:0", "-c:v", "copy", "-an", "-y", mp4Path,
	)
	if output, err := remux.CombinedOutput(); err != nil {
		t.Fatalf("remux long-GOP HEVC fixture to MP4: %v\n%s", err, output)
	}
	exactMP4Anchor, exactMP4Segment, err := ResolveCopySeekAnchor(probeCtx, ffmpegPath, mp4Path, 10, 2)
	if err != nil {
		t.Fatalf("ResolveCopySeekAnchor exact MP4 keyframe: %v", err)
	}
	if math.Abs(exactMP4Anchor-10) > 0.001 || exactMP4Segment != 5 {
		t.Fatalf("exact MP4 anchor = %v, segment = %d; want 10, 5", exactMP4Anchor, exactMP4Segment)
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
		VideoSampleEntry:       VideoSampleEntryHVC1,
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
	if !strings.Contains(string(manifest), `#EXT-X-MAP:URI="init.mp4"`) {
		t.Fatalf("copy HLS manifest missing init map:\n%s", manifest)
	}
	initInfo, err := os.Stat(filepath.Join(outputDir, "init.mp4"))
	if err != nil || initInfo.Size() == 0 {
		t.Fatalf("copy HLS init segment: info=%v err=%v", initInfo, err)
	}
	segments, err := filepath.Glob(filepath.Join(outputDir, "seg_*.m4s"))
	if err != nil || len(segments) == 0 {
		t.Fatalf("copy HLS media segments = %v err=%v", segments, err)
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
	tag, err := exec.Command(ffprobePathFromFFmpeg(ffmpegPath),
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=codec_tag_string",
		"-of", "default=nw=1:nk=1",
		manifestPath,
	).CombinedOutput()
	tags := strings.Fields(string(tag))
	if err != nil || len(tags) == 0 {
		t.Fatalf("copy HLS sample entry = %q err=%v", tag, err)
	}
	for _, got := range tags {
		if got != VideoSampleEntryHVC1 {
			t.Fatalf("copy HLS sample entry = %q, want only %q", tag, VideoSampleEntryHVC1)
		}
	}
}
