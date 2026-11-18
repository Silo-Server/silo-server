package playback

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback/keyframes"

	"github.com/Silo-Server/silo-server/internal/models"
)

func subtitleRenditionTestOpts(dir string) TranscodeOpts {
	return TranscodeOpts{
		InputPath: "/media/movie.mkv", OutputDir: dir, TargetCodecVideo: "h264", TargetCodecAudio: "aac",
		HWAccel: HWAccelNone, SegmentDuration: 2, TotalDuration: 60,
		SubtitleTrackIndex: 3, SubtitleCodec: "subrip", SubtitleRendition: true,
	}
}

// fakeSubtitleFFprobe installs an ffmpeg/ffprobe pair whose ffprobe counts its
// runs and reports the given first video PTS, or fails when pts is empty.
// When gated, it writes to the FIFO "started" and then reads "release" before
// answering, so a test can act while an observation is in flight.
func fakeSubtitleFFprobe(t *testing.T, pts string, gated bool) (ffmpegPath, dir string) {
	t.Helper()
	dir = t.TempDir()
	script := "#!/bin/sh\necho run >> " + filepath.Join(dir, "runs") + "\n"
	if gated {
		for _, fifo := range []string{"started", "release"} {
			if err := syscall.Mkfifo(filepath.Join(dir, fifo), 0o600); err != nil {
				t.Skipf("mkfifo: %v", err)
			}
		}
		script += "echo started > " + filepath.Join(dir, "started") + "\nread _ < " + filepath.Join(dir, "release") + "\n"
	}
	if pts == "" {
		script += "exit 1\n"
	} else {
		script += "cat >/dev/null\necho '{\"packets\":[{\"pts_time\":\"" + pts + "\"}]}'\n"
	}
	for name, body := range map[string]string{"ffmpeg": "#!/bin/sh\nexit 1\n", "ffprobe": script} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "ffmpeg"), dir
}

func fakeSubtitleFFprobeRuns(t *testing.T, dir string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "runs"))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "run")
}

func subtitleOriginTestSession(t *testing.T, ffmpegPath string, start int) *TranscodeSession {
	t.Helper()
	dir := t.TempDir()
	opts := subtitleRenditionTestOpts(dir)
	opts.FFmpegPath = ffmpegPath
	opts.StartSegmentNumber = start
	opts.subtitleCueFile = subtitleCueFileName(0)
	if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("seg_%05d.ts", start)), []byte("ts"), 0o644); err != nil {
		t.Fatal(err)
	}
	session := &TranscodeSession{outputDir: dir, opts: opts, running: true}
	session.startSubtitleSpanLocked(opts)
	return session
}

func TestSubtitleSpanOriginIsObservedOncePerGeneration(t *testing.T) {
	ffmpegPath, fake := fakeSubtitleFFprobe(t, "27.000000", false)
	session := subtitleOriginTestSession(t, ffmpegPath, 10)
	opts := session.Opts()

	results := make(chan float64, 8)
	for range 8 {
		go func() {
			window := session.subtitleSegmentWindow(t.Context(), opts.subtitleCueFile, 12)
			results <- window.origin + window.offset
		}()
	}
	for range 8 {
		// Segment 10 starts at source 17 s (27 s less the 10 s MPEG-TS shift).
		if got := <-results; got != 21 {
			t.Fatalf("window start = %v, want 21", got)
		}
	}
	if runs := fakeSubtitleFFprobeRuns(t, fake); runs != 1 {
		t.Fatalf("ffprobe ran %d times for one generation", runs)
	}
}

func TestSubtitleSpanOriginBacksOffAfterAFailure(t *testing.T) {
	ffmpegPath, fake := fakeSubtitleFFprobe(t, "", false)
	session := subtitleOriginTestSession(t, ffmpegPath, 0)
	opts := session.Opts()
	for range 3 {
		if got := session.subtitleSegmentWindow(t.Context(), opts.subtitleCueFile, 3); got.observed || got.offset != 6 {
			t.Fatalf("unobserved origin must fall back to the fixed grid: %v", got)
		}
	}
	if runs := fakeSubtitleFFprobeRuns(t, fake); runs != 1 {
		t.Fatalf("a failed observation was retried %d times within the backoff", runs-1)
	}
}

// TestSubtitleRenditionAlignsWithRealFFmpegSegments runs the transcode's own
// command line and checks the contract the player relies on: the cue output
// carries source-clock cues, and the segment X-TIMESTAMP-MAP maps them onto
// the video segments' actual timestamps after a seek restart.
// The cue file keeps the source clock, so a cue belongs in the subtitle
// segment whose video FFmpeg actually produced at that source time. That only
// equals [N*d, (N+1)*d) when the source clock starts at zero: a container can
// start later, and its video can start after its audio, in which case the
// first generation's grid follows the video while a seek's follows the
// container.
func TestSubtitleRenditionAlignsWithRealFFmpegSegments(t *testing.T) {
	if testing.Short() {
		t.Skip("real FFmpeg integration test")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ffprobe := ffprobePathFromFFmpeg(ffmpeg)
	if _, err := exec.LookPath(ffprobe); err != nil {
		t.Skip("ffprobe is not installed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	run := func(t *testing.T, binary string, args ...string) []byte {
		t.Helper()
		output, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", filepath.Base(binary), err, output)
		}
		return output
	}
	work := t.TempDir()
	srt := filepath.Join(work, "subs.srt")
	if err := os.WriteFile(srt, []byte("1\n00:00:01,000 --> 00:00:02,500\nEarly\n\n2\n00:00:21,000 --> 00:00:25,000\nAcross segments\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(work, "base.mkv")
	run(t, ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=64x64:rate=12",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-i", srt, "-t", "26",
		"-map", "0", "-map", "1", "-map", "2", "-c:v", "ffv1", "-c:a", "pcm_s16le", "-c:s", "srt", base)
	// source shifts the video and the audio and subtitles of base.mkv.
	source := func(t *testing.T, name string, videoStart, otherStart int) string {
		t.Helper()
		path := filepath.Join(work, name+".mkv")
		if _, err := os.Stat(path); err == nil {
			return path
		}
		run(t, ffmpeg, "-v", "error",
			"-itsoffset", strconv.Itoa(videoStart), "-i", base, "-itsoffset", strconv.Itoa(otherStart), "-i", base,
			"-map", "0:v", "-map", "1:a", "-map", "1:s", "-c", "copy", path)
		return path
	}
	encoders := string(run(t, ffmpeg, "-hide_banner", "-encoders"))

	for _, container := range []struct {
		name, video string
		offset90k   int64
	}{
		{"mpegts", transcodeCodecH264, HLSMPEGTSTimestampOffset90k},
		{"fmp4", transcodeCodecHEVC, 0},
	} {
		for _, src := range []struct {
			name                   string
			videoStart, otherStart int
		}{
			{"offset", 7, 7},
			{"video-after-audio", 7, 3},
		} {
			// fMP4 differs only in its timestamp map; the offset source covers it.
			if container.video == transcodeCodecHEVC && src.name != "offset" {
				continue
			}
			for _, generation := range []struct {
				name  string
				seek  float64
				first int
			}{
				{"start", 0, 0},
				{"seek", 20, 10},
			} {
				t.Run(container.name+"/"+src.name+"/"+generation.name, func(t *testing.T) {
					if container.video == transcodeCodecHEVC && !strings.Contains(encoders, "libx265") {
						t.Skip("libx265 is unavailable")
					}
					dir := t.TempDir()
					opts := TranscodeOpts{
						InputPath: source(t, src.name, src.videoStart, src.otherStart), OutputDir: dir, FFmpegPath: ffmpeg, HWAccel: HWAccelNone,
						SourceVideoCodec: "ffv1", TargetCodecVideo: container.video, TargetCodecAudio: "aac",
						SeekSeconds: generation.seek, StartSegmentNumber: generation.first, SegmentDuration: 2, TotalDuration: 26,
						SubtitleTrackIndex: 0, SubtitleCodec: "subrip", SubtitleRendition: true,
						subtitleCueFile: subtitleCueFileName(0),
					}
					if container.video == transcodeCodecHEVC {
						opts.VideoSampleEntry = VideoSampleEntryHVC1
					}
					// A cue file a dropped span left behind can carry the generation's
					// name; ffmpeg must replace the file, not stop to ask.
					if err := os.WriteFile(filepath.Join(dir, opts.subtitleCueFile), []byte("WEBVTT\n\n00:21.000 --> 00:22.000\nStale\n"), 0o644); err != nil {
						t.Fatal(err)
					}
					run(t, ffmpeg, buildFFmpegArgs(opts)...)

					// The source time each produced video segment starts at: its first
					// video frame, less the shift the timestamp map declares.
					var starts []float64
					for segment := generation.first; ; segment++ {
						name := filepath.Join(dir, fmt.Sprintf("seg_%05d.ts", segment))
						input := []string{"-i", name}
						if container.video == transcodeCodecHEVC {
							name = filepath.Join(dir, fmt.Sprintf("seg_%05d.m4s", segment))
							input = []string{"-i", "concat:" + filepath.Join(dir, "init.mp4") + "|" + name}
						}
						if _, err := os.Stat(name); err != nil {
							break
						}
						var report struct {
							Packets []struct {
								PTS string `json:"pts_time"`
							} `json:"packets"`
						}
						output := run(t, ffprobe, append([]string{"-v", "error", "-select_streams", "v:0", "-show_entries", "packet=pts_time", "-of", "json"}, input...)...)
						if err := json.Unmarshal(output, &report); err != nil {
							t.Fatal(err)
						}
						first := math.Inf(1)
						for _, packet := range report.Packets {
							if pts, err := strconv.ParseFloat(packet.PTS, 64); err == nil {
								first = math.Min(first, pts)
							}
						}
						starts = append(starts, first-float64(container.offset90k)/90_000)
					}
					if len(starts) < 3 {
						t.Fatalf("produced %d segments", len(starts))
					}

					session := &TranscodeSession{outputDir: dir, opts: opts}
					session.startSubtitleSpanLocked(opts)
					cues := []struct {
						text     string
						from, to float64
					}{
						{"Early", float64(src.otherStart) + 1, float64(src.otherStart) + 2.5},
						{"Across segments", float64(src.otherStart) + 21, float64(src.otherStart) + 25},
					}
					wantMap := fmt.Sprintf("X-TIMESTAMP-MAP=LOCAL:00:00:00.000,MPEGTS:%d\n", container.offset90k)
					for i, start := range starts {
						segment := generation.first + i
						end := start + 2
						if i+1 < len(starts) {
							end = starts[i+1]
						}
						document, err := session.SubtitleSegmentVTT(ctx, segment)
						if err != nil {
							t.Fatal(err)
						}
						text := string(document)
						if !strings.HasPrefix(text, "WEBVTT\n"+wantMap) || strings.Contains(text, "Stale") {
							t.Fatalf("segment %d:\n%s", segment, text)
						}
						for _, cue := range cues {
							want := cue.from < end && cue.to > start
							if got := strings.Contains(text, cue.text); got != want {
								t.Errorf("segment %d covers source [%.3f, %.3f): cue %q [%.1f, %.1f) present=%v, want %v\n%s",
									segment, start, end, cue.text, cue.from, cue.to, got, want, text)
							}
						}
					}
				})
			}
		}
	}
}

func subtitleRenditionPlannerInputV3(t *testing.T, codec string, fidelity SubtitleFidelityV3) PlannerInputV3 {
	t.Helper()
	file := detailedFixtureFileV3()
	file.Duration = 6480
	file.VideoTracks[0].VideoRange = "SDR"
	file.VideoTracks[0].VideoRangeType = "SDR"
	file.VideoTracks[0].ColorTransfer = "bt709"
	file.SubtitleTracks = []models.SubtitleTrack{{Index: 3, Codec: codec, Language: "eng"}}
	req := validStartRequestV3()
	req.QualityPreference = "1080p"
	req.SubtitleFidelityPreference = fidelity
	req.Capabilities.VideoDecode = []VideoDecodeCapabilityV3{{Codec: "hevc", Profiles: []string{"main 10"}, Levels: []int{153}, BitDepths: []int{10}, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitrateKbps: 80_000, Hardware: true}}
	hls := req.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3]
	hls.Subtitles = DeliverySubtitleCapabilitiesV3{SidecarText: true, EmbeddedText: true}
	req.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3] = hls
	index := 0
	req.SubtitleTrackIndex = &index
	req.SubtitleTrackID = TrackIDV3(file.ID, "subtitle", index)
	req.ClientFeatures = append(req.ClientFeatures, FeatureHLSSubtitleRenditionsV3)
	return PlannerInputV3{
		Request: req, RequestedFile: file, EffectiveFile: file, AudioTrackIndex: 0,
		Settings: PlannerSettingsV3{TranscodeEnabled: true, Allow4KTranscode: true}, Registry: testTransformationRegistryV3(),
		SubtitleRenditionExecutable: func() bool { return true },
	}
}

func TestPlanPlaybackV3CarriesEmbeddedTextAsHLSSubtitleRendition(t *testing.T) {
	input := subtitleRenditionPlannerInputV3(t, "subrip", SubtitleFidelityCompatibleV3)
	result := PlanPlaybackV3(input)
	if result.Plan == nil || result.Plan.Delivery != DeliveryTranscodeHLSV3 {
		t.Fatalf("want a transcode, got %s", ExplainPlannerResultV3(result))
	}
	want := &HLSSubtitleRenditionV3{GroupID: "subs", Name: "Subtitles", Format: "vtt", MIMEType: "text/vtt"}
	subtitle := result.Plan.Subtitle
	if !result.SubtitleRendition || subtitle.HLSRendition == nil || *subtitle.HLSRendition != *want ||
		subtitle.Artifact != nil || subtitle.Embedded != nil || subtitle.Mode != SubtitleRenderV3 || subtitle.TrackID != input.Request.SubtitleTrackID {
		t.Fatalf("subtitle decision = %#v (rendition %v)", subtitle, result.SubtitleRendition)
	}
	if result.SubtitleTransportTrackIndex != 0 || result.SubtitleBurnIn || result.Plan.Claims.Subtitles.Reason != "server_hls_subtitle_rendition" {
		t.Fatalf("recipe = %#v", result)
	}
	recipe := FreezeExecutableRecipeV3(result)
	if !recipe.SubtitleRendition || recipe.Version != executableRecipeVersionSubtitleRenditionV3 || !recipe.ValidFor(*result.Plan) ||
		!recipe.PlannerResult(result.Plan).SubtitleRendition {
		t.Fatalf("frozen recipe lost the rendition: %#v", recipe)
	}

	// A failed rendition falls back to the sidecar plan instead of exhausting
	// the route.
	retry := input
	retry.AttemptedKeys = []string{result.Plan.PlanAttemptKey}
	fallback := PlanPlaybackV3(retry)
	if fallback.Plan == nil || fallback.SubtitleRendition || fallback.Plan.Subtitle.HLSRendition != nil ||
		fallback.Plan.Delivery != DeliveryTranscodeHLSV3 || fallback.Plan.PlanAttemptKey == result.Plan.PlanAttemptKey {
		t.Fatalf("rendition retry = %s", ExplainPlannerResultV3(fallback))
	}
}

// subtitleRenditionRemuxInputV3 plans an HLS copy remux for a client that
// opted into remux renditions: it plays the source's video but not its
// Matroska container, and has no progressive route.
func subtitleRenditionRemuxInputV3(t *testing.T, codec string) PlannerInputV3 {
	t.Helper()
	input := subtitleRenditionPlannerInputV3(t, codec, SubtitleFidelityCompatibleV3)
	input.Request.ClientFeatures = append(input.Request.ClientFeatures, FeatureHLSRemuxSubtitleRenditionsV3)
	input.Request.QualityPreference = "original"
	input.Request.Capabilities.Containers = []string{"mp4"}
	input.Request.ClientPlaybackContext.Deliveries[DeliveryClassProgressiveV3] = DeliveryCapabilityV3{}
	input.SubtitleRenditionExecutable = nil
	input.RemuxSubtitleRenditionExecutable = func() bool { return true }
	return input
}

func TestPlanPlaybackV3CarriesEmbeddedTextAsRemuxHLSSubtitleRendition(t *testing.T) {
	input := subtitleRenditionRemuxInputV3(t, "subrip")
	result := PlanPlaybackV3(input)
	if result.Plan == nil || result.Plan.Delivery != DeliveryRemuxHLSV3 || result.PlayMethod != PlayRemux || result.TargetVideoCodec != codecCopyV3 {
		t.Fatalf("want an HLS copy remux, got %s", ExplainPlannerResultV3(result))
	}
	subtitle := result.Plan.Subtitle
	if !result.SubtitleRendition || subtitle.HLSRendition == nil || subtitle.Artifact != nil || subtitle.Mode != SubtitleRenderV3 ||
		result.SubtitleTransportTrackIndex != 0 || result.Plan.Claims.Subtitles.Reason != "server_hls_subtitle_rendition" {
		t.Fatalf("subtitle decision = %#v (rendition %v)", subtitle, result.SubtitleRendition)
	}
	// The frozen recipe keeps version 4: every binary that reads it refuses a
	// rendition outside a transcode, so an older one cannot drop it.
	recipe := FreezeExecutableRecipeV3(result)
	if !recipe.SubtitleRendition || recipe.Version != executableRecipeVersionSubtitleRenditionV3 || !recipe.ValidFor(*result.Plan) ||
		!recipe.PlannerResult(result.Plan).SubtitleRendition {
		t.Fatalf("frozen recipe lost the rendition: %#v", recipe)
	}
	progressive := recipe
	progressive.TargetVideoCodec = ""
	if progressive.Valid() {
		t.Fatal("a progressive remux recipe must not carry a rendition")
	}

	retry := input
	retry.AttemptedKeys = []string{result.Plan.PlanAttemptKey}
	fallback := PlanPlaybackV3(retry)
	if fallback.Plan == nil || fallback.SubtitleRendition || fallback.Plan.Subtitle.HLSRendition != nil ||
		fallback.Plan.Delivery != DeliveryRemuxHLSV3 || fallback.Plan.PlanAttemptKey == result.Plan.PlanAttemptKey {
		t.Fatalf("a failed remux rendition must fall back to the remux sidecar: %s", ExplainPlannerResultV3(fallback))
	}
}

// copyRemuxRenditionOpts is a copy remux of a source whose embedded SubRip
// track 3 is carried as the rendition.
func copyRemuxRenditionOpts(dir string, mpegts bool) TranscodeOpts {
	opts := subtitleRenditionTestOpts(dir)
	opts.TargetCodecVideo, opts.SourceVideoCodec, opts.CopyVideoMPEGTS = "copy", transcodeCodecHEVC, mpegts
	opts.TotalDuration = 12.5
	return opts
}

// writeCopyRemuxPlaylist writes the playlist FFmpeg's HLS muxer keeps for a
// copy generation that started at segment first, with the given keyframe-cut
// durations, and the segment files it lists.
func writeCopyRemuxPlaylist(t *testing.T, dir string, opts TranscodeOpts, first int, durations []float64, ended bool) {
	t.Helper()
	lines := []string{"#EXTM3U", "#EXT-X-VERSION:7", "#EXT-X-TARGETDURATION:5", "#EXT-X-MEDIA-SEQUENCE:" + strconv.Itoa(first)}
	if videoUsesFMP4(opts) {
		lines = append(lines, `#EXT-X-MAP:URI="init.mp4"`)
		if err := os.WriteFile(filepath.Join(dir, hlsInitSegmentName), []byte("init"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for i, d := range durations {
		name := segmentFilename(first+i, opts)
		lines = append(lines, fmt.Sprintf("#EXTINF:%.6f,", d), name)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("media"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if ended {
		lines = append(lines, "#EXT-X-ENDLIST")
	}
	if err := os.WriteFile(filepath.Join(dir, "stream.m3u8"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A copy remux's subtitle segment N covers the source time of its video
// segment N, which the keyframe cut makes variable: the window comes from the
// playlist's own durations after the generation's observed first frame. An
// fMP4 mux that delayed the video declares that shift in the timestamp map.
func TestCopyRemuxSubtitleSegmentsFollowTheVideoSegments(t *testing.T) {
	cues := "WEBVTT\n\n00:41.000 --> 00:42.000\nFirst\n\n00:45.500 --> 00:46.000\nSecond\n\n00:46.500 --> 00:48.000\nAcross\n\n01:00.000 --> 01:01.000\nPast the video\n"
	for _, tc := range []struct {
		name    string
		mpegts  bool
		planned bool
		pts     string
		// videoStartMS is the generation's first video packet on the cues'
		// clock, as it records it.
		videoStartMS int
		mapLine      string
	}{
		// FFmpeg's playlist after a seek to the keyframe at 40 s; MPEG-TS
		// shifts the video 10 s from the cue clock.
		{"EVENT MPEG-TS", true, false, "50.000000", 40_000, "X-TIMESTAMP-MAP=LOCAL:00:00:00.000,MPEGTS:900000\n"},
		// A keyframe plan restarts at 0; this source's video starts at
		// 39.95 s on the cues' clock, which the mux delayed to 40 s.
		{"planned fMP4", false, true, "40.000000", 39_950, "X-TIMESTAMP-MAP=LOCAL:00:00:00.000,MPEGTS:4500\n"},
		// After its last read misses the video start, the origin is
		// observed without a shift rather than retried for good.
		{"planned fMP4 without a recorded video start", false, true, "40.000000", -1, "X-TIMESTAMP-MAP=LOCAL:00:00:00.000,MPEGTS:0\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ffmpegPath, fake := fakeSubtitleFFprobe(t, tc.pts, false)
			opts := copyRemuxRenditionOpts(t.TempDir(), tc.mpegts)
			opts.FFmpegPath = ffmpegPath
			opts.subtitleCueFile = subtitleCueFileName(0)
			durations := []float64{5, 2, 4}
			session := &TranscodeSession{outputDir: opts.OutputDir}
			if tc.planned {
				session.copyPlan = &copySegmentPlan{durations: durations}
				writeCopyRemuxPlaylist(t, opts.OutputDir, opts, 0, durations, true)
			} else {
				opts.StartSegmentNumber, opts.SeekSeconds, opts.StreamOriginSeconds, opts.CopySeekAnchorResolved = 10, 40.3, 40, true
				writeCopyRemuxPlaylist(t, opts.OutputDir, opts, 10, durations, true)
			}
			if err := os.WriteFile(filepath.Join(opts.OutputDir, opts.subtitleCueFile), []byte(cues), 0o644); err != nil {
				t.Fatal(err)
			}
			if tc.videoStartMS >= 0 {
				videoStart := fmt.Sprintf("#tb 0: 1/1000\n0, %d, %d, 42, 12345, 0x12345678\n", tc.videoStartMS-50, tc.videoStartMS)
				if err := os.WriteFile(filepath.Join(opts.OutputDir, subtitleVideoStartFileName(opts.subtitleCueFile)), []byte(videoStart), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			session.opts = opts
			session.startSubtitleSpanLocked(opts)
			if tc.videoStartMS < 0 {
				session.subtitleSpans[0].videoStartMisses = subtitleVideoStartReads - 1
			}

			want := [][]string{{"First"}, {"Second", "Across"}, {"Across", "Past the video"}}
			for i, texts := range want {
				segment := opts.StartSegmentNumber + i
				got, err := session.SubtitleSegmentVTT(t.Context(), segment)
				if err != nil || !strings.HasPrefix(string(got), "WEBVTT\n"+tc.mapLine) {
					t.Fatalf("segment %d (%v):\n%s", segment, err, got)
				}
				if strings.Count(string(got), "-->") != len(texts) {
					t.Errorf("segment %d carries %d cues, want %v:\n%s", segment, strings.Count(string(got), "-->"), texts, got)
				}
				for _, text := range texts {
					if !strings.Contains(string(got), text) {
						t.Errorf("segment %d lacks %q:\n%s", segment, text, got)
					}
				}
			}
			if runs := fakeSubtitleFFprobeRuns(t, fake); runs != 1 {
				t.Fatalf("ffprobe ran %d times for one generation", runs)
			}
			// A rebuilt session keeps the observed shift.
			if spans, _ := loadSubtitleSpans(opts, session.copyPlan); len(spans) != 1 || spans[0].muxShift != session.subtitleSpans[0].muxShift {
				t.Fatalf("recorded spans = %#v, want the shift %v", spans, session.subtitleSpans[0].muxShift)
			}
		})
	}
}

const copyRemuxSourceDuration = 20

// copyRemuxRealCues are the SubRip cues of the real copy remux sources, on
// the copy's clock: -start_at_zero removes the shifted source's offset. They
// sit off whole seconds so a frame-sized offset shows.
var copyRemuxRealCues = []struct {
	text     string
	from, to float64
}{{"Early", 1.37, 2.37}, {"Middle", 8.37, 10.8}, {"Late", 16.37, 17.37}}

// TestCopyRemuxSubtitleRenditionRealFFmpeg runs a real copy remux of a
// Matroska source whose 2.5 s keyframe interval gives segments off the 2 s
// grid, fed through a FIFO so FFmpeg cannot finish: the cues of every segment
// it has produced must be served while it still runs, each in the segments
// whose video covers it. MPEG-TS uses a zero source clock and fMP4 uses a
// source whose video starts after its subtitles. Its B-frames and re-encoded
// AAC make the fMP4 mux delay the video, which the timestamp map must
// declare to within a frame.
func TestCopyRemuxSubtitleRenditionRealFFmpeg(t *testing.T) {
	if testing.Short() {
		t.Skip("real FFmpeg integration test")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	if _, err := exec.LookPath(ffprobePathFromFFmpeg(ffmpeg)); err != nil {
		t.Skip("ffprobe is not installed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	base, shifted := copyRemuxRealSources(ctx, t, ffmpeg)

	for _, tc := range []struct {
		name, path string
		mpegts     bool
		offset90k  int64
		videoStart float64 // on the copy's clock, which starts with the subtitles
	}{
		{name: "fmp4", path: shifted, videoStart: 4},
		{name: "mpegts", path: base, mpegts: true, offset90k: HLSMPEGTSTimestampOffset90k},
	} {
		t.Run(tc.name+"/live", func(t *testing.T) {
			fifo, release, written := feedSourceThroughFIFO(t, tc.path)
			session, err := StartTranscode(ctx, TranscodeOpts{
				SessionID: "copy-rendition", InputPath: fifo, OutputDir: t.TempDir(), FFmpegPath: ffmpeg, HWAccel: HWAccelNone,
				SourceVideoCodec: "h264", TargetCodecVideo: "copy", TargetCodecAudio: "aac", CopyVideoMPEGTS: tc.mpegts,
				CopySeekAnchorResolved: true, SegmentDuration: 2, TotalDuration: copyRemuxSourceDuration + tc.videoStart,
				SubtitleTrackIndex: 0, SubtitleCodec: "subrip", SubtitleRendition: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = session.Close() })

			// FFmpeg is blocked on the FIFO with a quarter of the source unread.
			head := waitForProducedHead(ctx, t, session, 4)
			served := checkCopyRemuxCues(ctx, t, session, tc.offset90k, tc.videoStart, head, false)
			if !session.IsRunning() {
				t.Fatal("the remux finished before the cues were checked")
			}
			if !served["Middle"] || served["Late"] {
				t.Fatalf("cues served while the remux runs = %v, want Middle and not yet Late", served)
			}
			subtitles, err := session.BuildPlaybackManifest("segment/", "silo_rendition=subtitles")
			if err != nil || !strings.Contains(string(subtitles), "#EXT-X-PLAYLIST-TYPE:EVENT\n") ||
				!strings.Contains(string(subtitles), fmt.Sprintf("segment/sub_%05d.vtt", head)) {
				t.Fatalf("subtitle playlist (%v):\n%s", err, subtitles)
			}

			release()
			if err := <-written; err != nil {
				t.Fatal(err)
			}
			<-session.Done()
			if err := session.WaitError(); err != nil {
				t.Fatalf("ffmpeg: %v", err)
			}
			head, _ = session.liveProducedHead()
			if served := checkCopyRemuxCues(ctx, t, session, tc.offset90k, tc.videoStart, head+1, true); !served["Late"] {
				t.Fatalf("the finished remux lost its last cues: %v", served)
			}
		})
		// A seek generation starts at the keyframe at 7.5 s, numbered as the
		// segment the seek resolved to.
		t.Run(tc.name+"/seek", func(t *testing.T) {
			opts := TranscodeOpts{
				InputPath: base, OutputDir: t.TempDir(), FFmpegPath: ffmpeg, HWAccel: HWAccelNone,
				SourceVideoCodec: "h264", TargetCodecVideo: "copy", TargetCodecAudio: "aac", CopyVideoMPEGTS: tc.mpegts,
				SeekSeconds: 8, StreamOriginSeconds: 7.5, CopySeekAnchorResolved: true, StartSegmentNumber: 3,
				SegmentDuration: 2, TotalDuration: copyRemuxSourceDuration, SubtitleTrackIndex: 0, SubtitleCodec: "subrip", SubtitleRendition: true,
				subtitleCueFile: subtitleCueFileName(1),
			}
			if output, err := exec.CommandContext(ctx, ffmpeg, buildFFmpegArgs(opts)...).CombinedOutput(); err != nil {
				t.Fatalf("ffmpeg: %v\n%s", err, output)
			}
			session := &TranscodeSession{outputDir: opts.OutputDir, opts: opts}
			session.startSubtitleSpanLocked(opts)
			head, ok := session.liveProducedHead()
			if !ok || head < 6 {
				t.Fatalf("seek generation produced through segment %d", head)
			}
			if served := checkCopyRemuxCues(ctx, t, session, tc.offset90k, opts.StreamOriginSeconds, head+1, true); !served["Middle"] || !served["Late"] || served["Early"] {
				t.Fatalf("seek generation served %v, want Middle and Late", served)
			}
		})
	}

	// A keyframe-planned copy writes no playlist of its own; its cues follow
	// the assembled segments, from the start and after a restart mid-stream,
	// whose seek lands on the keyframe before its segment.
	t.Run("planned", func(t *testing.T) {
		if _, err := keyframes.Load(t.Context(), base); err != nil {
			t.Fatal(err)
		}
		session, err := StartTranscode(ctx, TranscodeOpts{
			SessionID: "planned-rendition", InputPath: base, OutputDir: t.TempDir(), FFmpegPath: ffmpeg, HWAccel: HWAccelNone,
			SourceVideoCodec: "h264", TargetCodecVideo: "copy", TargetCodecAudio: "aac", KeyframePlaylist: true,
			CopySeekAnchorResolved: true, SegmentDuration: 2, TotalDuration: copyRemuxSourceDuration,
			SubtitleTrackIndex: 0, SubtitleCodec: "subrip", SubtitleRendition: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = session.Close() })
		if session.copyGroups == nil {
			t.Fatal("no plan for a Matroska source with complete Cues")
		}
		<-session.Done()
		segments := len(session.copyPlan.durations)
		if served := checkCopyRemuxCues(ctx, t, session, 0, 0, segments, true); !served["Early"] || !served["Middle"] || !served["Late"] {
			t.Fatalf("planned copy served %v, want every cue", served)
		}

		const restartAt = 3 // starts at the keyframe at 7.5 s
		for n := restartAt; n < segments; n++ {
			if err := os.Remove(filepath.Join(session.outputDir, plannedSegmentName(n))); err != nil {
				t.Fatal(err)
			}
		}
		if _, ok, err := session.RestartSegment(ctx, restartAt); err != nil || !ok {
			t.Fatalf("RestartSegment = %v, %v", ok, err)
		}
		<-session.Done()
		if served := checkCopyRemuxCues(ctx, t, session, 0, session.copyPlan.keyframes[session.copyPlan.firstKey[restartAt]]-session.copyPlan.inputStart, segments, true); !served["Middle"] || !served["Late"] {
			t.Fatalf("planned restart served %v, want Middle and Late", served)
		}
	})
}

// copyRemuxRealSources writes a 20 s H.264 Matroska source with B-frames and
// a SubRip track, and a copy of it whose video starts 4 s after its other
// tracks.
func copyRemuxRealSources(ctx context.Context, t *testing.T, ffmpeg string) (base, shifted string) {
	t.Helper()
	run := func(args ...string) []byte {
		t.Helper()
		output, err := exec.CommandContext(ctx, ffmpeg, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("ffmpeg: %v\n%s", err, output)
		}
		return output
	}
	if !strings.Contains(string(run("-hide_banner", "-encoders")), "libx264") {
		t.Skip("libx264 is unavailable")
	}
	work := t.TempDir()
	var srt strings.Builder
	for i, cue := range copyRemuxRealCues {
		at := func(seconds float64) string {
			ms := int(math.Round(seconds * 1000))
			return fmt.Sprintf("00:00:%02d,%03d", ms/1000, ms%1000)
		}
		fmt.Fprintf(&srt, "%d\n%s --> %s\n%s\n\n", i+1, at(cue.from), at(cue.to), cue.text)
	}
	subs := filepath.Join(work, "subs.srt")
	if err := os.WriteFile(subs, []byte(srt.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	base = filepath.Join(work, "base.mkv")
	run("-v", "error", "-f", "lavfi", "-i", "testsrc2=size=96x64:rate=24",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-i", subs, "-t", strconv.Itoa(copyRemuxSourceDuration),
		"-map", "0", "-map", "1", "-map", "2",
		"-c:v", "libx264", "-preset", "veryfast", "-bf", "3", "-g", "60", "-keyint_min", "60", "-sc_threshold", "0",
		"-c:a", "aac", "-c:s", "srt", base)
	shifted = filepath.Join(work, "video-after-subtitles.mkv")
	run("-v", "error", "-itsoffset", "7", "-i", base, "-itsoffset", "3", "-i", base,
		"-map", "0:v", "-map", "1:a", "-map", "1:s", "-c", "copy", shifted)
	return base, shifted
}

// feedSourceThroughFIFO serves the first three quarters of a file through a
// FIFO and the rest once release is called. written reports the writer's end.
func feedSourceThroughFIFO(t *testing.T, path string) (fifo string, release func(), written <-chan error) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pipe := filepath.Join(t.TempDir(), "source.mkv")
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	released := make(chan struct{})
	var once sync.Once
	release = func() { once.Do(func() { close(released) }) }
	t.Cleanup(release)
	done := make(chan error, 1)
	go func() {
		f, err := os.OpenFile(pipe, os.O_WRONLY, 0)
		if err != nil {
			done <- err
			return
		}
		_, err = f.Write(data[:len(data)*3/4])
		<-released
		if err == nil {
			_, err = f.Write(data[len(data)*3/4:])
		}
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		done <- err
	}()
	return pipe, release, done
}

// waitForProducedHead waits until the live generation has listed segment
// least and returns its head.
func waitForProducedHead(ctx context.Context, t *testing.T, session *TranscodeSession, least int) int {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for {
		if head, ok := session.liveProducedHead(); ok && head >= least {
			return head
		}
		if time.Now().After(deadline) {
			t.Fatal("FFmpeg produced too few segments")
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-timer.C:
		}
	}
}

// checkCopyRemuxCues serves every segment of the generation before last and
// checks its cues against the source time its video covers, read from the
// segment itself. videoStart is where the generation's video starts on the
// cues' clock: the muxed video's lead over it is the shift the timestamp map
// must declare, within a frame, so each cue shows with its frame. open says
// segment last-1 is the media's last, which runs on. It returns the cues
// served.
func checkCopyRemuxCues(ctx context.Context, t *testing.T, session *TranscodeSession, offset90k int64, videoStart float64, last int, open bool) map[string]bool {
	t.Helper()
	const frame90k = 90_000 / 24
	served := map[string]bool{}
	first := session.Opts().StartSegmentNumber
	muxShift := realSegmentStart(ctx, t, session, offset90k, first) - videoStart
	wantMap90k := offset90k + int64(math.Round(muxShift*90_000))
	next := videoStart
	for segment := first; segment < last; segment++ {
		start, end := next, math.Inf(1)
		if segment+1 < last || !open {
			next = realSegmentStart(ctx, t, session, offset90k, segment+1) - muxShift
			end = next
		}
		document, err := session.SubtitleSegmentVTT(ctx, segment)
		if err != nil {
			t.Fatal(err)
		}
		text := string(document)
		var map90k int64
		if _, err := fmt.Sscanf(text, "WEBVTT\nX-TIMESTAMP-MAP=LOCAL:00:00:00.000,MPEGTS:%d\n", &map90k); err != nil ||
			map90k < wantMap90k-frame90k || map90k > wantMap90k+frame90k {
			t.Fatalf("segment %d maps cues to MPEGTS %d, want %d (the mux shift %.3f s) within a frame:\n%s", segment, map90k, wantMap90k, muxShift, text)
		}
		for _, cue := range copyRemuxRealCues {
			want, got := cue.from < end && cue.to > start, strings.Contains(text, cue.text)
			if got != want {
				t.Errorf("segment %d covers [%.3f, %.3f): cue %q [%.1f, %.1f) present=%v, want %v\n%s",
					segment, start, end, cue.text, cue.from, cue.to, got, want, text)
			}
			served[cue.text] = served[cue.text] || got
		}
	}
	return served
}

// realSegmentStart is the source time a produced video segment starts at: its
// first video frame, less the shift the timestamp map declares.
func realSegmentStart(ctx context.Context, t *testing.T, session *TranscodeSession, offset90k int64, segment int) float64 {
	t.Helper()
	opts := session.Opts()
	name := filepath.Join(opts.OutputDir, segmentFilename(segment, opts))
	if videoUsesFMP4(opts) {
		name = "concat:" + filepath.Join(opts.OutputDir, hlsInitSegmentName) + "|" + name
	}
	output, err := exec.CommandContext(ctx, ffprobePathFromFFmpeg(opts.FFmpegPath), "-v", "error", "-select_streams", "v:0",
		"-show_entries", "packet=pts_time", "-of", "json", "-i", name).CombinedOutput()
	if err != nil {
		t.Fatalf("ffprobe: %v\n%s", err, output)
	}
	var report struct {
		Packets []struct {
			PTS string `json:"pts_time"`
		} `json:"packets"`
	}
	if err := json.Unmarshal(output, &report); err != nil {
		t.Fatal(err)
	}
	first := math.Inf(1)
	for _, packet := range report.Packets {
		if pts, err := strconv.ParseFloat(packet.PTS, 64); err == nil {
			first = math.Min(first, pts)
		}
	}
	return first - float64(offset90k)/90_000
}
