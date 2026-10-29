package playback

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

func subtitleRenditionTestOpts(dir string) TranscodeOpts {
	return TranscodeOpts{
		InputPath: "/media/movie.mkv", OutputDir: dir, TargetCodecVideo: "h264", TargetCodecAudio: "aac",
		HWAccel: HWAccelNone, SegmentDuration: 2, TotalDuration: 60,
		SubtitleTrackIndex: 3, SubtitleCodec: "subrip", SubtitleRendition: true,
	}
}

func TestBuildFFmpegArgsAppendsSubtitleRenditionOutput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*TranscodeOpts)
		shared []string
		// videoStart is whether the generation records its first video
		// packet on the cues' clock, which a copy's mux may shift.
		videoStart bool
	}{
		{"transcode", func(*TranscodeOpts) {}, []string{"-copyts", "-ss"}, false},
		{"copy remux", func(o *TranscodeOpts) { o.TargetCodecVideo = "copy" }, []string{"-copyts", "-start_at_zero", "-ss"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			opts := subtitleRenditionTestOpts(dir)
			opts.SeekSeconds = 20
			opts.StartSegmentNumber = 10
			opts.subtitleCueFile = subtitleCueFileName(2)
			tc.mutate(&opts)
			args := buildFFmpegArgs(opts)

			manifest := slices.Index(args, filepath.Join(dir, "stream.m3u8"))
			if manifest < 0 {
				t.Fatalf("HLS output missing: %v", args)
			}
			// The HLS output itself stays subtitle-free; the cue output follows it so
			// its options cannot leak into the HLS mux.
			if !slices.Contains(args[:manifest], "-sn") {
				t.Fatalf("HLS output must keep -sn: %v", args)
			}
			want := []string{"-y", "-map", "0:s:3", "-c:s", "webvtt", "-flush_packets", "1", "-f", "webvtt", filepath.Join(dir, "subtitles_2.vtt")}
			if tc.videoStart {
				want = append(want, "-map", "0:v:0", "-c:v", "copy", "-frames:v", "1", "-avoid_negative_ts", "disabled",
					"-flush_packets", "1", "-f", "framecrc", filepath.Join(dir, "subtitles_2_video.crc"))
			}
			if got := args[manifest+1:]; !slices.Equal(got, want) {
				t.Fatalf("cue output = %v, want %v", got, want)
			}
			for _, shared := range tc.shared {
				if !slices.Contains(args[:manifest], shared) {
					t.Fatalf("cue output must share %s with the HLS output: %v", shared, args)
				}
			}
		})
	}
}

func TestSubtitleRenditionInactiveOutsideItsRecipe(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*TranscodeOpts)
	}{
		{"not requested", func(o *TranscodeOpts) { o.SubtitleRendition = false }},
		{"no track", func(o *TranscodeOpts) { o.SubtitleTrackIndex = -1 }},
		{"burn in", func(o *TranscodeOpts) { o.SubtitleBurnIn = true }},
		{"bitmap", func(o *TranscodeOpts) { o.SubtitleCodec = "hdmv_pgs_subtitle" }},
		{"unknown duration", func(o *TranscodeOpts) { o.TotalDuration = 0 }},
		{"oversized playlist", func(o *TranscodeOpts) { o.TotalDuration = float64(maxSyntheticManifestSegments*2 + 10) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := subtitleRenditionTestOpts(t.TempDir())
			tc.mutate(&opts)
			if subtitleRenditionActive(opts) {
				t.Fatal("rendition must be inactive")
			}
			if args := strings.Join(buildFFmpegArgs(opts), " "); strings.Contains(args, "webvtt") {
				t.Fatalf("inactive rendition still emitted a cue output: %s", args)
			}
			session := &TranscodeSession{outputDir: opts.OutputDir, opts: opts}
			if session.HasSubtitleRendition() {
				t.Fatal("inactive rendition published a multivariant playlist")
			}
		})
	}
}

func TestBuildPlaybackManifestPublishesSubtitleRendition(t *testing.T) {
	opts := subtitleRenditionTestOpts(t.TempDir())
	opts.TotalDuration = 9
	opts.TargetBitrateKbps = 4000
	opts.TargetAudioBitrateKbps = 128
	session := &TranscodeSession{outputDir: opts.OutputDir, opts: opts}

	master, err := session.BuildPlaybackManifest("segment/", "st=abc")
	if err != nil {
		t.Fatal(err)
	}
	wantMaster := strings.Join([]string{
		"#EXTM3U",
		"#EXT-X-VERSION:3",
		"#EXT-X-INDEPENDENT-SEGMENTS",
		`#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="Subtitles",DEFAULT=YES,AUTOSELECT=YES,FORCED=NO,URI="master.m3u8?silo_rendition=subtitles&st=abc"`,
		`#EXT-X-STREAM-INF:BANDWIDTH=4128000,SUBTITLES="subs"`,
		"master.m3u8?silo_rendition=video&st=abc",
		"",
	}, "\n")
	if string(master) != wantMaster {
		t.Fatalf("multivariant playlist:\n%s\nwant:\n%s", master, wantMaster)
	}

	video, err := session.BuildPlaybackManifest("segment/", "silo_rendition=video&st=abc")
	if err != nil {
		t.Fatal(err)
	}
	if want := session.GenerateFullManifest("segment/", "st=abc"); string(video) != string(want) {
		t.Fatalf("video variant must be the unchanged synthetic playlist:\n%s\nwant:\n%s", video, want)
	}

	subtitles, err := session.BuildPlaybackManifest("segment/", "st=abc&silo_rendition=subtitles")
	if err != nil {
		t.Fatal(err)
	}
	wantSubtitles := strings.Join([]string{
		"#EXTM3U",
		"#EXT-X-VERSION:3",
		"#EXT-X-TARGETDURATION:2",
		"#EXT-X-MEDIA-SEQUENCE:0",
		"#EXT-X-PLAYLIST-TYPE:VOD",
		"#EXTINF:2.000000,", "segment/sub_00000.vtt?st=abc",
		"#EXTINF:2.000000,", "segment/sub_00001.vtt?st=abc",
		"#EXTINF:2.000000,", "segment/sub_00002.vtt?st=abc",
		"#EXTINF:2.000000,", "segment/sub_00003.vtt?st=abc",
		"#EXTINF:1.000000,", "segment/sub_00004.vtt?st=abc",
		"#EXT-X-ENDLIST",
		"",
	}, "\n")
	if string(subtitles) != wantSubtitles {
		t.Fatalf("subtitle playlist:\n%s\nwant:\n%s", subtitles, wantSubtitles)
	}
	videoTimeline, err := parseManifestTimeline(video)
	if err != nil {
		t.Fatal(err)
	}
	if len(videoTimeline.entries) != 5 {
		t.Fatalf("video playlist lists %d segments, subtitle playlist 5", len(videoTimeline.entries))
	}
}

func TestAppendManifestQueryParamCoversSubtitleRendition(t *testing.T) {
	session := &TranscodeSession{opts: subtitleRenditionTestOpts(t.TempDir())}
	master, err := session.BuildPlaybackManifest("segment/", "")
	if err != nil {
		t.Fatal(err)
	}
	got := string(AppendManifestQueryParam(master, "st", "tok"))
	for _, want := range []string{`URI="master.m3u8?silo_rendition=subtitles&st=tok"`, "\nmaster.m3u8?silo_rendition=video&st=tok\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("relay token missing from %q:\n%s", want, got)
		}
	}
}

func TestSubtitleSegmentNamesStayOutOfTheVideoPipeline(t *testing.T) {
	if n, ok := ParseSubtitleSegmentNumber("sub_00012.vtt"); !ok || n != 12 {
		t.Fatalf("ParseSubtitleSegmentNumber = %d, %v", n, ok)
	}
	for _, name := range []string{"seg_00012.ts", "sub_.vtt", "sub_00012.ts", "sub_-1.vtt", "subtitles_0.vtt"} {
		if _, ok := ParseSubtitleSegmentNumber(name); ok {
			t.Fatalf("%q parsed as a subtitle segment", name)
		}
	}
	// Segment acknowledgement, pruning, and restart all key off
	// ParseSegmentNumber; a subtitle segment must never look like media.
	if _, err := ParseSegmentNumber("sub_00012.vtt"); err == nil {
		t.Fatal("subtitle segment parsed as a media segment")
	}
}

const subtitleRenditionCues = `WEBVTT

00:01.000 --> 00:03.500
First cue

00:21.000 --> 00:25.000 align:start position:10%
After the gap,
spans segments

01:00:40.500 --> 01:00:41.500
Late cue
`

func TestSubtitleSegmentDocumentCutsTheFixedGrid(t *testing.T) {
	opts := subtitleRenditionTestOpts("")
	opts.TotalDuration = 2 * 60 * 60
	cues := []byte(subtitleRenditionCues)
	header := "WEBVTT\nX-TIMESTAMP-MAP=LOCAL:00:00:00.000,MPEGTS:900000\n"
	spanning := "\n00:21.000 --> 00:25.000 align:start position:10%\nAfter the gap,\nspans segments\n"
	for _, tc := range []struct {
		segment int
		want    string
	}{
		{0, header + "\n00:01.000 --> 00:03.500\nFirst cue\n"},
		{1, header + "\n00:01.000 --> 00:03.500\nFirst cue\n"},
		{2, header},
		{10, header + spanning},
		{11, header + spanning},
		{12, header + spanning},
		{13, header},
		{1820, header + "\n01:00:40.500 --> 01:00:41.500\nLate cue\n"},
	} {
		if got := string(subtitleSegmentDocument(opts, tc.segment, subtitleCueWindow{offset: float64(tc.segment * 2)}, cues)); got != tc.want {
			t.Errorf("segment %d:\n%q\nwant\n%q", tc.segment, got, tc.want)
		}
	}
}

func TestSubtitleSegmentDocumentTimestampMapFollowsTheContainer(t *testing.T) {
	opts := subtitleRenditionTestOpts("")
	opts.TargetCodecVideo = transcodeCodecHEVC
	if got := string(subtitleSegmentDocument(opts, 0, subtitleCueWindow{}, nil)); got != "WEBVTT\nX-TIMESTAMP-MAP=LOCAL:00:00:00.000,MPEGTS:0\n" {
		t.Fatalf("fMP4 segments keep the source clock: %q", got)
	}
}

func TestSubtitleSegmentDocumentKeepsCuesPastTheLastBoundary(t *testing.T) {
	opts := subtitleRenditionTestOpts("")
	opts.TotalDuration = 21
	// The last listed segment (10) covers the remainder and anything a
	// slightly longer real stream carries past the probed duration.
	got := string(subtitleSegmentDocument(opts, 10, subtitleCueWindow{offset: 20}, []byte(subtitleRenditionCues)))
	if !strings.Contains(got, "Late cue") || !strings.Contains(got, "After the gap") {
		t.Fatalf("last segment dropped trailing cues: %q", got)
	}
}

func TestSubtitleSegmentDocumentSkipsACueStillBeingWritten(t *testing.T) {
	opts := subtitleRenditionTestOpts("")
	partial := []byte("WEBVTT\n\n00:01.000 --> 00:03.500\nFirst cue\n\n00:02.000 --> 00:0")
	got := string(subtitleSegmentDocument(opts, 1, subtitleCueWindow{offset: 2}, partial))
	if strings.Count(got, "-->") != 1 || !strings.Contains(got, "First cue") {
		t.Fatalf("partial cue leaked into the segment: %q", got)
	}
}

func TestSubtitleSegmentDocumentFollowsTheObservedOrigin(t *testing.T) {
	opts := subtitleRenditionTestOpts("")
	opts.TotalDuration = 2 * 60 * 60
	cues := []byte(subtitleRenditionCues)
	// A source whose clock starts at 7 s: video segment N starts at 7+2N, so
	// the cue at 21-25 s belongs in segments 7 and 8, not 10-12.
	for segment, want := range map[int]bool{6: false, 7: true, 8: true, 9: false, 10: false, 11: false, 12: false} {
		got := string(subtitleSegmentDocument(opts, segment, subtitleCueWindow{origin: 7, offset: float64(segment * 2), observed: true}, cues))
		if strings.Contains(got, "After the gap") != want {
			t.Errorf("segment %d carries the 21 s cue=%v, want %v: %q", segment, !want, want, got)
		}
	}
}

func TestSubtitleSegmentDocumentFollowsTheCueClockAcrossTheMPEGTSRollover(t *testing.T) {
	opts := subtitleRenditionTestOpts("")
	opts.TotalDuration = 2 * 60 * 60
	wrap := float64(subtitleCueClockWrap90k) / 90_000
	// A broadcast capture whose clock starts 2 s before the 33-bit rollover:
	// its cue file keeps counting past 95443.7 s while the MPEG-TS video
	// segments start over, so the observed origin sits an epoch below the cues.
	cues := []byte("WEBVTT\n\n26:30:41.500 --> 26:30:42.000\nBefore the rollover\n\n26:30:44.000 --> 26:30:45.000\nPast the rollover\n")
	origin := 95441.0 - wrap // segment 0 starts at source 95441 s
	for segment, want := range map[int]string{0: "Before the rollover", 1: "Past the rollover"} {
		got := string(subtitleSegmentDocument(opts, segment, subtitleCueWindow{origin: origin, offset: float64(segment * 2), observed: true}, cues))
		if !strings.Contains(got, want) || strings.Count(got, "-->") != 1 {
			t.Errorf("segment %d: %q, want only %q", segment, got, want)
		}
	}
	// ffprobe can report the same time as a signed 33-bit value.
	if got := string(subtitleSegmentDocument(opts, 1, subtitleCueWindow{origin: origin - wrap, offset: 2, observed: true}, cues)); !strings.Contains(got, "Past the rollover") {
		t.Errorf("signed observation: %q", got)
	}
	// fMP4 timestamps do not wrap, so the same arithmetic must not alias.
	opts.TargetCodecVideo = transcodeCodecHEVC
	if got := string(subtitleSegmentDocument(opts, 1, subtitleCueWindow{origin: origin, offset: 2, observed: true}, cues)); strings.Contains(got, "rollover") {
		t.Errorf("fMP4 window aliased across an epoch: %q", got)
	}
	// A long capture keeps its origin's epoch more than half a range later:
	// segment 24000 starts 48000 s after the origin, at source 143441 s.
	opts.TargetCodecVideo = transcodeCodecH264
	opts.TotalDuration = 60_000
	late := []byte(string(cues) + "\n39:50:41.500 --> 39:50:42.000\nThirteen hours later\n")
	window := subtitleCueWindow{origin: origin, offset: 48_000, observed: true}
	if got := string(subtitleSegmentDocument(opts, 24_000, window, late)); !strings.Contains(got, "Thirteen hours later") || strings.Count(got, "-->") != 1 {
		t.Errorf("segment 24000 lost the origin's epoch: %q", got)
	}
}

func TestSubtitleWindowEpochKeepsLongZeroClockStreams(t *testing.T) {
	// A 20 h MPEG-TS stream from a zero clock never reaches the rollover; its
	// late windows must not be pulled an epoch back toward the first cue.
	if got := subtitleWindowEpoch(72_000*90_000, 90_000); got != 72_000*90_000 {
		t.Fatalf("window moved to %d", got)
	}
	// A window slightly below zero (a lifted first frame) stays there.
	if got := subtitleWindowEpoch(-90_000, 0); got != -90_000 {
		t.Fatalf("window moved to %d", got)
	}
	// A zero-clock stream whose first cue comes 14 h in is still one epoch.
	if got := subtitleWindowEpoch(0, 14*60*60*90_000); got != 0 {
		t.Fatalf("origin moved to %d", got)
	}
	// An origin observed just below the rollover's wrap, with its cue just
	// before the rollover, moves up one epoch.
	if got := subtitleWindowEpoch(-3*90_000, 95_441*90_000); got != subtitleCueClockWrap90k-3*90_000 {
		t.Fatalf("origin moved to %d", got)
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

func TestSubtitleSpanOriginDiscardsAnObservationARestartOverlapped(t *testing.T) {
	ffmpegPath, fake := fakeSubtitleFFprobe(t, "27.000000", true)
	session := subtitleOriginTestSession(t, ffmpegPath, 10)
	opts := session.Opts()

	done := make(chan float64, 1)
	go func() {
		window := session.subtitleSegmentWindow(t.Context(), opts.subtitleCueFile, 12)
		done <- window.origin + window.offset
	}()
	// Opening the FIFO waits for the fake ffprobe to start.
	if _, err := os.ReadFile(filepath.Join(fake, "started")); err != nil {
		t.Fatal(err)
	}
	// A backward seek restart replaces the generation while its first segment
	// is read: the new generation rewrites output from segment 4 onward.
	writeSubtitleRenditionPlaylist(t, session.outputDir, 10, 14)
	restartSubtitleGeneration(session, opts, 4)
	if err := os.WriteFile(filepath.Join(fake, "release"), []byte("go\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := <-done; got != 24 {
		t.Fatalf("overlapped observation must fall back to the fixed grid: %v", got)
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if len(session.subtitleSpans) != 2 {
		t.Fatalf("spans = %#v", session.subtitleSpans)
	}
	for _, span := range session.subtitleSpans {
		if span.originKnown || !span.originFailedAt.IsZero() {
			t.Fatalf("span %q kept the result of a read across a restart: %#v", span.file, span)
		}
	}
}

// A forward seek leaves the earlier generation's output in place, so a backward
// seek into it later still gets that generation's own origin.
func TestSubtitleSpanOriginObservesAnEarlierGenerationItsSuccessorKept(t *testing.T) {
	ffmpegPath, fake := fakeSubtitleFFprobe(t, "27.000000", false)
	session := subtitleOriginTestSession(t, ffmpegPath, 10)
	opts := session.Opts()
	writeSubtitleRenditionPlaylist(t, session.outputDir, 10, 14)
	restartSubtitleGeneration(session, opts, 900)

	if got := session.subtitleSegmentWindow(t.Context(), opts.subtitleCueFile, 12); got.origin+got.offset != 21 {
		t.Fatalf("earlier generation's window = %+v, want a start at 21", got)
	}
	if runs := fakeSubtitleFFprobeRuns(t, fake); runs != 1 {
		t.Fatalf("ffprobe ran %d times", runs)
	}
}

// subtitleCueTestOpts returns rendition options for a fresh output dir whose
// generation-0 cue file already holds subtitleRenditionCues.
func subtitleCueTestOpts(t *testing.T) (string, TranscodeOpts) {
	t.Helper()
	dir := t.TempDir()
	opts := subtitleRenditionTestOpts(dir)
	opts.subtitleCueFile = subtitleCueFileName(0)
	if err := os.WriteFile(filepath.Join(dir, opts.subtitleCueFile), []byte(subtitleRenditionCues), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, opts
}

// restartSubtitleGeneration closes the session's current cue span and opens
// generation 1 at start, as a seek restart does.
func restartSubtitleGeneration(session *TranscodeSession, opts TranscodeOpts, start int) {
	session.closeSubtitleSpan()
	next := opts
	next.StartSegmentNumber = start
	next.subtitleCueFile = subtitleCueFileName(1)
	session.mu.Lock()
	session.startSubtitleSpanLocked(next)
	session.mu.Unlock()
}

// A rebuild keeps each span's observed origin: a later generation that started
// at or before an earlier span's start makes both of that span's probe
// candidates unreadable, so the origin cannot be observed again.
func TestRebuiltSessionKeepsAnObservedOrigin(t *testing.T) {
	ffmpegPath, _ := fakeSubtitleFFprobe(t, "127.000000", false)
	session := subtitleOriginTestSession(t, ffmpegPath, 50)
	opts := session.Opts()
	for _, name := range []string{subtitleCueFileName(0), subtitleCueFileName(1)} {
		if err := os.WriteFile(filepath.Join(session.outputDir, name), []byte(subtitleRenditionCues), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	observed := session.subtitleSegmentWindow(t.Context(), opts.subtitleCueFile, 52)
	if !observed.observed {
		t.Fatalf("origin not observed: %+v", observed)
	}
	writeSubtitleRenditionPlaylist(t, session.outputDir, 50, 100)
	session.closeSubtitleSpan()
	// A backward seek restarts at 40 and gets as far as 60 before the server dies.
	next := opts
	next.StartSegmentNumber = 40
	next.subtitleCueFile = subtitleCueFileName(1)
	session.mu.Lock()
	session.startSubtitleSpanLocked(next)
	session.mu.Unlock()
	session.saveSubtitleSpans()
	writeSubtitleRenditionPlaylist(t, session.outputDir, 40, 60)
	session.closeSubtitleSpan()

	spans, _ := loadSubtitleSpans(opts, nil)
	rebuilt := &TranscodeSession{outputDir: session.outputDir, opts: opts, running: true, subtitleSpans: spans}
	got := rebuilt.subtitleSegmentWindow(t.Context(), opts.subtitleCueFile, 80)
	if want := (subtitleCueWindow{origin: observed.origin, offset: 60, duration: 2, observed: true, open: true}); got != want {
		t.Fatalf("rebuilt window = %+v, want %+v", got, want)
	}
}

func writeSubtitleRenditionPlaylist(t *testing.T, dir string, first, last int) {
	t.Helper()
	if err := writeSubtitleRenditionPlaylistFile(dir, first, last); err != nil {
		t.Fatal(err)
	}
}

// writeSubtitleRenditionPlaylistFile is the goroutine-safe form: it reports
// the error instead of failing the test.
func writeSubtitleRenditionPlaylistFile(dir string, first, last int) error {
	lines := []string{"#EXTM3U", "#EXT-X-VERSION:3", "#EXT-X-TARGETDURATION:2", "#EXT-X-MEDIA-SEQUENCE:" + strconv.Itoa(first)}
	for segment := first; segment <= last; segment++ {
		lines = append(lines, "#EXTINF:2.000000,", fmt.Sprintf("seg_%05d.ts", segment))
	}
	return os.WriteFile(filepath.Join(dir, "stream.m3u8"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

func TestSubtitleSegmentVTTServesOnlyDemuxedSegments(t *testing.T) {
	dir, opts := subtitleCueTestOpts(t)
	writeSubtitleRenditionPlaylist(t, dir, 10, 12)
	opts.StartSegmentNumber = 10
	session := &TranscodeSession{outputDir: dir, opts: opts, running: true}
	session.startSubtitleSpanLocked(opts)

	got, err := session.SubtitleSegmentVTT(t.Context(), 11)
	if err != nil || !strings.Contains(string(got), "After the gap") {
		t.Fatalf("segment behind the produced head: %q, %v", got, err)
	}

	// Segment 12 is the newest the process finished: its next segment is not
	// written, so later cues may still be in flight.
	if _, ready, _, imminent := session.subtitleSegmentSource(12); ready || !imminent {
		t.Fatalf("live-edge segment ready=%v imminent=%v, want a long wait for production", ready, imminent)
	}
	// A segment far past the produced output waits briefly and ends in a
	// valid empty document rather than an error.
	session.opts.SegmentDuration = 1 // shortest bounded wait
	start := time.Now()
	got, err = session.SubtitleSegmentVTT(t.Context(), 40)
	if err != nil || strings.Contains(string(got), "-->") || !strings.HasPrefix(string(got), "WEBVTT\nX-TIMESTAMP-MAP=") {
		t.Fatalf("far segment: %q, %v", got, err)
	}
	if waited := time.Since(start); waited < minSegmentWait || waited > maxSegmentWait+time.Second {
		t.Fatalf("far-segment wait took %v", waited)
	}

	// Before the generation's start, nothing covers the segment.
	session.mu.Lock()
	session.running = false
	session.mu.Unlock()
	start = time.Now()
	got, err = session.SubtitleSegmentVTT(t.Context(), 9)
	if err != nil || strings.Contains(string(got), "-->") || time.Since(start) > time.Second {
		t.Fatalf("uncovered segment of a stopped session must answer at once: %q, %v", got, err)
	}

	// A process that exited cleanly finished every segment it listed.
	got, err = session.SubtitleSegmentVTT(t.Context(), 12)
	if err != nil || !strings.HasPrefix(string(got), "WEBVTT") {
		t.Fatalf("finished segment: %q, %v", got, err)
	}
}

func TestSubtitleSegmentVTTWakesWhenProductionCatchesUp(t *testing.T) {
	dir, opts := subtitleCueTestOpts(t)
	writeSubtitleRenditionPlaylist(t, dir, 0, 10)
	session := &TranscodeSession{outputDir: dir, opts: opts, running: true}
	session.startSubtitleSpanLocked(opts)

	produced := make(chan error, 1)
	go func() {
		//nolint:forbidigo // Simulates FFmpeg finishing a segment while the request is already waiting.
		time.Sleep(3 * subtitleCuePollInterval)
		produced <- writeSubtitleRenditionPlaylistFile(dir, 0, 12)
	}()
	got, err := session.SubtitleSegmentVTT(t.Context(), 11)
	if writeErr := <-produced; writeErr != nil {
		t.Fatal(writeErr)
	}
	if err != nil || !strings.Contains(string(got), "After the gap") {
		t.Fatalf("waited segment: %q, %v", got, err)
	}
}

// After a seek restart on slow storage the new generation can take longer than
// the short wait to reach the segment the player asks for first. An empty
// answer would be final for a VOD player, so a segment the live generation is
// about to produce waits as long as its video segment does.
func TestSubtitleSegmentVTTWaitsOutASlowRestart(t *testing.T) {
	dir, opts := subtitleCueTestOpts(t)
	opts.SegmentDuration = 1 // the short wait is its minimum; cue 00:21 is in segment 21
	opts.StartSegmentNumber = 20
	session := &TranscodeSession{outputDir: dir, opts: opts, running: true}
	session.startSubtitleSpanLocked(opts)

	produced := make(chan error, 1)
	go func() {
		//nolint:forbidigo // Simulates a restarted FFmpeg reaching the segment after the short wait.
		time.Sleep(minSegmentWait + time.Second)
		produced <- writeSubtitleRenditionPlaylistFile(dir, 20, 22)
	}()
	got, err := session.SubtitleSegmentVTT(t.Context(), 21)
	if writeErr := <-produced; writeErr != nil {
		t.Fatal(writeErr)
	}
	if err != nil || !strings.Contains(string(got), "After the gap") {
		t.Fatalf("segment after a slow restart: %q, %v", got, err)
	}
}

func TestSubtitleSegmentVTTIgnoresAnInheritedPlaylist(t *testing.T) {
	dir, opts := subtitleCueTestOpts(t)
	writeSubtitleRenditionPlaylist(t, dir, 0, 20)
	session := &TranscodeSession{outputDir: dir, opts: opts, inheritedManifest: statManifest(dir)}
	session.startSubtitleSpanLocked(opts)
	got, err := session.SubtitleSegmentVTT(t.Context(), 11)
	if err != nil || strings.Contains(string(got), "-->") {
		t.Fatalf("an earlier generation's playlist is not this process's progress: %q, %v", got, err)
	}
}

func TestSubtitleSpansKeepEarlierGenerationsReadable(t *testing.T) {
	dir, opts := subtitleCueTestOpts(t)
	writeSubtitleRenditionPlaylist(t, dir, 0, 14)
	session := &TranscodeSession{outputDir: dir, opts: opts}
	session.startSubtitleSpanLocked(opts)

	// A seek restart at segment 900 closes generation 0 at its head.
	session.closeSubtitleSpan()
	next := opts
	next.StartSegmentNumber = 900
	next.subtitleCueFile = subtitleCueFileName(1)
	if err := os.WriteFile(filepath.Join(dir, next.subtitleCueFile), []byte("WEBVTT\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeSubtitleRenditionPlaylist(t, dir, 900, 901)
	session.mu.Lock()
	session.running = true
	session.opts = next
	session.startSubtitleSpanLocked(next)
	session.mu.Unlock()

	if want := []subtitleCueSpan{{file: "subtitles_0.vtt", start: 0, end: 14}, {file: "subtitles_1.vtt", start: 900, end: -1, startTime: 1800}}; !reflect.DeepEqual(session.subtitleSpans, want) {
		t.Fatalf("spans = %#v, want %#v", session.subtitleSpans, want)
	}
	// A backward seek into generation 0's output still has its cues; the
	// boundary segment it finished last is included.
	got, err := session.SubtitleSegmentVTT(t.Context(), 12)
	if err != nil || !strings.Contains(string(got), "After the gap") {
		t.Fatalf("earlier generation's segment: %q, %v", got, err)
	}
	if file, ready, _, _ := session.subtitleSegmentSource(14); !ready || file != "subtitles_0.vtt" {
		t.Fatalf("closed span must cover its head: %q, %v", file, ready)
	}
	if _, ready, _, _ := session.subtitleSegmentSource(15); ready {
		t.Fatal("segment past the closed span's head must not be served from it")
	}
	if file, ready, _, _ := session.subtitleSegmentSource(900); !ready || file != "subtitles_1.vtt" {
		t.Fatalf("live generation must serve its own segments: %q, %v", file, ready)
	}
}

// A session rebuilt from its recipe on the same output directory (after the
// server that ran it crashed) serves the earlier process's video segments from
// disk, so it has to serve their cues too.
func TestRebuiltSessionKeepsEarlierGenerationCues(t *testing.T) {
	dir := t.TempDir()
	opts := subtitleRenditionTestOpts(dir)
	opts.FFmpegPath = fakeFFmpegScript(t)
	first, err := StartTranscode(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	firstCues := first.Opts().subtitleCueFile
	if err := os.WriteFile(filepath.Join(dir, firstCues), []byte(subtitleRenditionCues), 0o644); err != nil {
		t.Fatal(err)
	}
	writeSubtitleRenditionPlaylist(t, dir, 0, 14)
	// The server dies: its process goes, the directory stays.
	if err := first.CloseProcess(); err != nil {
		t.Fatal(err)
	}

	rebuiltOpts := opts
	rebuiltOpts.Reconstructed = true
	rebuiltOpts.StartSegmentNumber = 25
	rebuiltOpts.SeekSeconds = 50
	rebuilt, err := StartTranscode(t.Context(), rebuiltOpts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rebuilt.CloseProcess() })
	if got := rebuilt.Opts().subtitleCueFile; got == firstCues {
		t.Errorf("the rebuilt process writes %s again, replacing the earlier process's cues", got)
	}

	start := time.Now()
	got, err := rebuilt.SubtitleSegmentVTT(t.Context(), 11)
	if err != nil || !strings.Contains(string(got), "After the gap") {
		t.Fatalf("segment the earlier process produced: %q, %v", got, err)
	}
	if waited := time.Since(start); waited > time.Second {
		t.Fatalf("a finished segment waited %v", waited)
	}
	if file, ready, _, _ := rebuilt.subtitleSegmentSource(14); !ready || file != firstCues {
		t.Fatalf("the earlier process's last segment: %q, %v", file, ready)
	}
	if _, ready, _, _ := rebuilt.subtitleSegmentSource(15); ready {
		t.Fatal("a segment the earlier process never finished must not be served from its cues")
	}

	// A seek restart of the rebuilt session keeps numbering past both files.
	if err := rebuilt.Restart(t.Context(), 10, 5); err != nil {
		t.Fatal(err)
	}
	if got := rebuilt.Opts().subtitleCueFile; got == firstCues {
		t.Fatalf("restart reused %s", got)
	}
}

// Only spans that can still serve cues survive a rebuild.
func TestLoadSubtitleSpansDropsUnusableSpans(t *testing.T) {
	dir := t.TempDir()
	opts := subtitleRenditionTestOpts(dir)
	for _, name := range []string{subtitleCueFileName(0), subtitleCueFileName(2)} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(subtitleRenditionCues), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeSubtitleRenditionPlaylist(t, dir, 0, 14)
	inherited := statManifest(dir)
	session := &TranscodeSession{outputDir: dir, opts: opts, inheritedManifest: inherited, subtitleSpans: []subtitleCueSpan{
		{file: subtitleCueFileName(0), start: 0, end: 14},
		// Its cue file is gone.
		{file: subtitleCueFileName(1), start: 100, end: 120},
		// The live generation never wrote a playlist: the one on disk is
		// generation 0's.
		{file: subtitleCueFileName(2), start: 10, end: -1},
	}}
	session.saveSubtitleSpans()

	spans, next := loadSubtitleSpans(opts, nil)
	if want := []subtitleCueSpan{{file: subtitleCueFileName(0), start: 0, end: 14}}; !reflect.DeepEqual(spans, want) {
		t.Fatalf("spans = %#v, want %#v", spans, want)
	}
	if next != 3 {
		t.Fatalf("next generation = %d, want 3: a dropped span's file name stays taken", next)
	}
	// Nor is a playlist another generation wrote after the record was last
	// saved, such as one whose own record write failed.
	writeSubtitleRenditionPlaylist(t, dir, 0, 20)
	if spans, _ := loadSubtitleSpans(opts, nil); len(spans) != 1 {
		t.Fatalf("a playlist starting at 0 was credited to the span starting at 10: %#v", spans)
	}

	// Another track's cues are not this recipe's.
	other := opts
	other.SubtitleTrackIndex = 4
	if spans, _ := loadSubtitleSpans(other, nil); len(spans) != 0 {
		t.Fatalf("spans of another track: %#v", spans)
	}
}

func TestSplitSubtitleRenditionQueryKeepsOtherParametersVerbatim(t *testing.T) {
	rendition, rest := splitSubtitleRenditionQuery("a=1&silo_rendition=video&st=x%2By&b")
	if rendition != subtitleRenditionVideo || rest != "a=1&st=x%2By&b" {
		t.Fatalf("got %q, %q", rendition, rest)
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

func TestPlanPlaybackV3KeepsSidecarWithoutHLSSubtitleRendition(t *testing.T) {
	for _, tc := range []struct {
		name     string
		codec    string
		fidelity SubtitleFidelityV3
		mutate   func(*PlannerInputV3)
	}{
		{"client did not negotiate", "subrip", SubtitleFidelityCompatibleV3, func(in *PlannerInputV3) {
			in.Request.ClientFeatures = WithoutFeatureV3(in.Request.ClientFeatures, FeatureHLSSubtitleRenditionsV3)
		}},
		{"no capable executor", "subrip", SubtitleFidelityCompatibleV3, func(in *PlannerInputV3) {
			in.SubtitleRenditionExecutable = func() bool { return false }
		}},
		{"unknown duration", "subrip", SubtitleFidelityCompatibleV3, func(in *PlannerInputV3) { in.EffectiveFile.Duration = 0 }},
		{"ASS keeps its styling", "ass", SubtitleFidelityPreserveV3, func(in *PlannerInputV3) {
			hls := in.Request.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3]
			hls.Subtitles.ASSStyling, hls.Subtitles.FontAttachments = true, true
			in.Request.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3] = hls
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := subtitleRenditionPlannerInputV3(t, tc.codec, tc.fidelity)
			tc.mutate(&input)
			result := PlanPlaybackV3(input)
			if result.Plan == nil || result.Plan.Delivery != DeliveryTranscodeHLSV3 {
				t.Fatalf("want a transcode, got %s", ExplainPlannerResultV3(result))
			}
			if result.SubtitleRendition || result.Plan.Subtitle.HLSRendition != nil {
				t.Fatalf("unexpected rendition: %#v", result.Plan.Subtitle)
			}
		})
	}
}

func TestPlanPlaybackV3ConvertsASSIntoHLSSubtitleRendition(t *testing.T) {
	result := PlanPlaybackV3(subtitleRenditionPlannerInputV3(t, "ass", SubtitleFidelityCompatibleV3))
	if result.Plan == nil || !result.SubtitleRendition || result.Plan.Subtitle.HLSRendition == nil {
		t.Fatalf("ASS that is converted anyway can use the rendition: %s", ExplainPlannerResultV3(result))
	}
}

// A copy remux's rendition keeps its VIDEO-RANGE through a restart or a
// session rebuilt from the stream token; losing it declares HDR as SDR.
func TestRecipeCardCarriesSubtitleRendition(t *testing.T) {
	opts := subtitleRenditionTestOpts(t.TempDir())
	opts.TargetCodecVideo, opts.VideoRange = "copy", HLSVideoRangeHLG
	card := NewRecipeCard(1, "profile", 42, "", opts)
	claims := card.ToClaims()
	fromClaims := RecipeCardFromClaims(&claims)
	rebuilt := fromClaims.TranscodeOpts(opts.OutputDir, "", nil)
	if !card.SubtitleRendition || !claims.SubtitleRendition || !fromClaims.SubtitleRendition || !rebuilt.SubtitleRendition {
		t.Fatal("subtitle rendition lost between recipe card, claims, and options")
	}
	if rebuilt.VideoRange != HLSVideoRangeHLG {
		t.Fatalf("video range = %q after the stream token, want HLG", rebuilt.VideoRange)
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

// A client that asks for MPEG-TS copies (hls_copy_mpegts_v1) and remux
// renditions gets both on one plan: the rendition follows a TS remux's
// segments as it does an fMP4 remux's, and the frozen recipe keeps both at
// the MPEG-TS version.
func TestPlanPlaybackV3CarriesRemuxSubtitleRenditionOnMPEGTSCopy(t *testing.T) {
	input := subtitleRenditionRemuxInputV3(t, "subrip")
	hls := input.Request.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3]
	hls.Features = append(hls.Features, FeatureHLSCopyMPEGTSV3)
	input.Request.ClientPlaybackContext.Deliveries[DeliveryClassHLSV3] = hls
	result := PlanPlaybackV3(input)
	if result.Plan == nil || result.Plan.Delivery != DeliveryRemuxHLSV3 || !PlanCopiesVideoAsMPEGTSV3(*result.Plan) {
		t.Fatalf("want an MPEG-TS HLS copy remux, got %s", ExplainPlannerResultV3(result))
	}
	if !result.SubtitleRendition || result.Plan.Subtitle.HLSRendition == nil || result.Plan.Subtitle.Artifact != nil {
		t.Fatalf("subtitle decision = %#v (rendition %v)", result.Plan.Subtitle, result.SubtitleRendition)
	}
	recipe := FreezeExecutableRecipeV3(result)
	if !recipe.SubtitleRendition || !recipe.CopyVideoMPEGTS || recipe.Version != executableRecipeVersionCopyMPEGTSV3 || !recipe.ValidFor(*result.Plan) {
		t.Fatalf("frozen recipe = %#v", recipe)
	}
	if !recipe.PlannerResult(result.Plan).SubtitleRendition {
		t.Fatal("the restored planner result lost the rendition")
	}
	opts := TranscodeOpts{TargetCodecVideo: "copy", SourceVideoCodec: result.Plan.Source.VideoCodec, CopyVideoMPEGTS: PlanCopiesVideoAsMPEGTSV3(*result.Plan)}
	if HLSOutputContainer(opts) != OutputContainerMPEGTS || subtitleSegmentPTSOffset90k(opts) != HLSMPEGTSTimestampOffset90k {
		t.Fatalf("a TS copy rendition must map cues onto the MPEG-TS offset: container %s", HLSOutputContainer(opts))
	}

	fmp4 := subtitleRenditionRemuxInputV3(t, "subrip")
	if got := PlanPlaybackV3(fmp4); got.Plan == nil || got.Plan.PlanID == result.Plan.PlanID || got.Plan.PlanAttemptKey == result.Plan.PlanAttemptKey {
		t.Fatal("the TS rendition plan must not share an identity with the fMP4 one")
	}
}

// A client that negotiated only transcode renditions (the Apple and Android
// feature sets) gets exactly the remux plan it got before remux renditions:
// the same plan as a client that negotiated no rendition at all.
func TestPlanPlaybackV3KeepsTheRemuxPlanForTranscodeRenditionsOnly(t *testing.T) {
	transcodeOnly := subtitleRenditionRemuxInputV3(t, "subrip")
	transcodeOnly.Request.ClientFeatures = WithoutFeatureV3(transcodeOnly.Request.ClientFeatures, FeatureHLSRemuxSubtitleRenditionsV3)
	none := subtitleRenditionRemuxInputV3(t, "subrip")
	none.Request.ClientFeatures = WithoutFeatureV3(WithoutFeatureV3(none.Request.ClientFeatures, FeatureHLSRemuxSubtitleRenditionsV3), FeatureHLSSubtitleRenditionsV3)
	got, want := PlanPlaybackV3(transcodeOnly), PlanPlaybackV3(none)
	if got.Plan == nil || got.Plan.Delivery != DeliveryRemuxHLSV3 || got.SubtitleRendition {
		t.Fatalf("want the sidecar remux, got %s", ExplainPlannerResultV3(got))
	}
	gotJSON, err := json.Marshal(got.Plan)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal(want.Plan)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotJSON) != string(wantJSON) || FreezeExecutableRecipeV3(got) != FreezeExecutableRecipeV3(want) {
		t.Fatalf("plan changed:\n%s\nwant:\n%s", gotJSON, wantJSON)
	}
}

func TestPlanPlaybackV3KeepsRemuxSidecarWithoutHLSSubtitleRendition(t *testing.T) {
	for _, tc := range []struct {
		name   string
		codec  string
		mutate func(*PlannerInputV3)
	}{
		{"remux renditions alone", "subrip", func(in *PlannerInputV3) {
			in.Request.ClientFeatures = WithoutFeatureV3(in.Request.ClientFeatures, FeatureHLSSubtitleRenditionsV3)
		}},
		// A transcode executor's rendition says nothing about a remux's.
		{"no remux executor", "subrip", func(in *PlannerInputV3) {
			in.SubtitleRenditionExecutable = func() bool { return true }
			in.RemuxSubtitleRenditionExecutable = func() bool { return false }
		}},
		{"CEA-608 captions", "eia_608", func(*PlannerInputV3) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := subtitleRenditionRemuxInputV3(t, tc.codec)
			tc.mutate(&input)
			result := PlanPlaybackV3(input)
			if result.Plan == nil || result.Plan.Delivery != DeliveryRemuxHLSV3 {
				t.Fatalf("want an HLS copy remux, got %s", ExplainPlannerResultV3(result))
			}
			if result.SubtitleRendition || result.Plan.Subtitle.HLSRendition != nil {
				t.Fatalf("unexpected rendition: %#v", result.Plan.Subtitle)
			}
		})
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

// A copy remux's subtitle playlist lists the video playlist's own
// keyframe-cut segments, so the two cover one timeline whichever playlist
// the remux serves.
func TestCopyRemuxSubtitleRenditionPlaylistsFollowTheVideo(t *testing.T) {
	t.Run("keyframe plan", func(t *testing.T) {
		opts := copyRemuxRenditionOpts(t.TempDir(), false)
		opts.VideoRange = HLSVideoRangePQ
		writeCopyRemuxPlaylist(t, opts.OutputDir, opts, 0, []float64{4.004}, false)
		session := &TranscodeSession{outputDir: opts.OutputDir, opts: opts, copyPlan: &copySegmentPlan{durations: []float64{4.004, 2.002, 6.494}}}

		master, err := session.BuildPlaybackManifest("segment/", "st=abc")
		if err != nil {
			t.Fatal(err)
		}
		// HLS would read a variant without a range as SDR.
		if !strings.Contains(string(master), "\n#EXT-X-STREAM-INF:BANDWIDTH=8192000,VIDEO-RANGE=PQ,SUBTITLES=\"subs\"\nmaster.m3u8?silo_rendition=video&st=abc\n") {
			t.Fatalf("multivariant playlist:\n%s", master)
		}
		video, err := session.BuildPlaybackManifest("segment/", "silo_rendition=video&st=abc")
		if err != nil || string(video) != string(session.copyPlan.manifest(opts, "segment/", "st=abc")) {
			t.Fatalf("video variant must be the unchanged keyframe plan (%v):\n%s", err, video)
		}
		subtitles, err := session.BuildPlaybackManifest("segment/", "silo_rendition=subtitles&st=abc")
		if err != nil {
			t.Fatal(err)
		}
		want := strings.Join([]string{
			"#EXTM3U",
			"#EXT-X-VERSION:7",
			"#EXT-X-TARGETDURATION:6",
			"#EXT-X-MEDIA-SEQUENCE:0",
			"#EXT-X-PLAYLIST-TYPE:VOD",
			"#EXTINF:4.004000,", "segment/sub_00000.vtt?st=abc",
			"#EXTINF:2.002000,", "segment/sub_00001.vtt?st=abc",
			"#EXTINF:6.494000,", "segment/sub_00002.vtt?st=abc",
			"#EXT-X-ENDLIST",
			"",
		}, "\n")
		if string(subtitles) != want {
			t.Fatalf("subtitle playlist:\n%s\nwant:\n%s", subtitles, want)
		}
	})

	t.Run("FFmpeg's EVENT playlist", func(t *testing.T) {
		opts := copyRemuxRenditionOpts(t.TempDir(), true)
		writeCopyRemuxPlaylist(t, opts.OutputDir, opts, 10, []float64{5.005, 2.002}, false)
		session := &TranscodeSession{outputDir: opts.OutputDir, opts: opts}

		video, err := session.BuildPlaybackManifest("segment/", "silo_rendition=video&st=abc")
		if err != nil || !strings.Contains(string(video), "#EXT-X-PLAYLIST-TYPE:EVENT\n") {
			t.Fatalf("video variant (%v):\n%s", err, video)
		}
		subtitles, err := session.BuildPlaybackManifest("segment/", "silo_rendition=subtitles&st=abc")
		if err != nil {
			t.Fatal(err)
		}
		if want := strings.NewReplacer("segment/seg_", "segment/sub_", ".ts?", ".vtt?").Replace(string(video)); string(subtitles) != want {
			t.Fatalf("subtitle playlist:\n%s\nwant the video playlist's segments:\n%s", subtitles, want)
		}
	})
}

// The variant states the copied video's range, or HLS reads it as SDR.
func TestCopyHLSVideoRange(t *testing.T) {
	for _, tc := range []struct {
		name, transfer, filter, sampleEntry, want string
	}{
		{"HDR10", "smpte2084", "", VideoSampleEntryHVC1, HLSVideoRangePQ},
		{"HLG", "arib-std-b67", "", "", HLSVideoRangeHLG},
		{"SDR", "bt709", "", "", ""},
		// Dolby Vision 8.2 has an SDR base layer.
		{"Dolby Vision SDR base", "bt709", "", VideoSampleEntryDVH1, ""},
		// Profile 5 signals no transfer of its own.
		{"Dolby Vision profile 5", "unknown", "", VideoSampleEntryDVH1, HLSVideoRangePQ},
		{"Dolby Vision strip", "", DV7ToHDR10BitstreamFilter, VideoSampleEntryHVC1, HLSVideoRangePQ},
		{"unsignaled HEVC", "", "", VideoSampleEntryHVC1, ""},
	} {
		if got := CopyHLSVideoRange(tc.transfer, tc.filter, tc.sampleEntry); got != tc.want {
			t.Errorf("%s: range = %q, want %q", tc.name, got, tc.want)
		}
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

// A keyframe-planned copy's request for a subtitle segment FFmpeg has not
// reached waits as its video request does, counting toward the throttle, then
// fails as it does: an empty answer would be final, so after a long forward
// seek that window would never show its cues.
func TestPlannedCopySubtitleSegmentAheadOfFFmpegIsNotFound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		opts := copyRemuxRenditionOpts(t.TempDir(), false)
		opts.subtitleCueFile = subtitleCueFileName(0)
		durations := []float64{2, 2, 2, 2, 2, 2}
		writeCopyRemuxPlaylist(t, opts.OutputDir, opts, 0, durations[:2], false)
		if err := os.WriteFile(filepath.Join(opts.OutputDir, opts.subtitleCueFile), []byte("WEBVTT\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		session := &TranscodeSession{outputDir: opts.OutputDir, opts: opts, copyPlan: &copySegmentPlan{durations: durations}, running: true}
		session.startSubtitleSpanLocked(opts)

		started := time.Now()
		rr := httptest.NewRecorder()
		session.ServeSubtitleSegment(rr, httptest.NewRequest(http.MethodGet, "/segment/sub_00005.vtt", nil), 5)
		if rr.Code != http.StatusNotFound || time.Since(started) < subtitleSegmentProductionWait {
			t.Fatalf("segment ahead of FFmpeg = %d after %v, want 404 after the planned wait", rr.Code, time.Since(started))
		}
		// The throttler would otherwise hold FFmpeg back from it.
		if got := session.LastRequestedSegment(); got != 5 {
			t.Fatalf("throttle position = %d, want the requested segment 5", got)
		}
	})
}

// A subtitle request past the copy plan never moves the throttle position:
// the throttler would measure the buffer against a segment that will never
// exist and stop pausing FFmpeg.
func TestPlannedCopySubtitleSegmentPastThePlanLeavesTheThrottle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		opts := copyRemuxRenditionOpts(t.TempDir(), false)
		opts.subtitleCueFile = subtitleCueFileName(0)
		durations := []float64{2, 2, 2, 2, 2, 2}
		writeCopyRemuxPlaylist(t, opts.OutputDir, opts, 0, durations[:2], false)
		if err := os.WriteFile(filepath.Join(opts.OutputDir, opts.subtitleCueFile), []byte("WEBVTT\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		session := &TranscodeSession{outputDir: opts.OutputDir, opts: opts, copyPlan: &copySegmentPlan{durations: durations}, running: true, lastRequestedSegment: 1}
		session.startSubtitleSpanLocked(opts)

		rr := httptest.NewRecorder()
		session.ServeSubtitleSegment(rr, httptest.NewRequest(http.MethodGet, "/segment/sub_99999.vtt", nil), 99999)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("segment past the plan = %d, want 404", rr.Code)
		}
		if got := session.LastRequestedSegment(); got != 1 {
			t.Fatalf("throttle position = %d, want 1", got)
		}
	})
}

// A seek restart replaces FFmpeg's playlist, so a closed generation keeps the
// durations its own playlist listed: its segments' cues stay where they were,
// in the running session and in one rebuilt from the span record.
func TestCopyRemuxSubtitleSpansKeepTheirTimelineAcrossRestarts(t *testing.T) {
	opts := copyRemuxRenditionOpts(t.TempDir(), true)
	opts.TotalDuration = 600
	opts.StartSegmentNumber, opts.StreamOriginSeconds, opts.CopySeekAnchorResolved = 10, 40, true
	opts.subtitleCueFile = subtitleCueFileName(0)
	cues := "WEBVTT\n\n00:45.500 --> 00:46.000\nSecond\n"
	for _, name := range []string{subtitleCueFileName(0), subtitleCueFileName(1)} {
		if err := os.WriteFile(filepath.Join(opts.OutputDir, name), []byte(cues), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeCopyRemuxPlaylist(t, opts.OutputDir, opts, 10, []float64{5, 2, 4}, false)
	session := &TranscodeSession{outputDir: opts.OutputDir, opts: opts}
	session.startSubtitleSpanLocked(opts)
	session.saveSubtitleSpans()

	// A forward seek restarts at segment 30; FFmpeg replaces the playlist.
	restartSubtitleGeneration(session, opts, 30)
	writeCopyRemuxPlaylist(t, opts.OutputDir, opts, 30, []float64{3, 3}, false)
	session.saveSubtitleSpans()

	check := func(t *testing.T, session *TranscodeSession) {
		t.Helper()
		got, err := session.SubtitleSegmentVTT(t.Context(), 11)
		if err != nil || !strings.Contains(string(got), "Second") {
			t.Fatalf("the earlier generation's segment 11 [45 s, 47 s) lost its cue (%v):\n%s", err, got)
		}
		if window := session.subtitleSegmentWindow(t.Context(), subtitleCueFileName(0), 12); window.offset != 7 || window.duration != 4 {
			t.Fatalf("segment 12 window = %+v, want offset 7 and duration 4", window)
		}
	}
	check(t, session)

	spans, next := loadSubtitleSpans(opts, nil)
	if len(spans) != 2 || next != 2 || !slices.Equal(spans[0].durations, []float64{5, 2, 4}) || !slices.Equal(spans[1].durations, []float64{3, 3}) {
		t.Fatalf("recorded spans = %#v, next %d", spans, next)
	}
	check(t, &TranscodeSession{outputDir: opts.OutputDir, opts: opts, subtitleSpans: spans})
}

// copyRemuxRealCues are the SubRip cues of the real copy remux sources, on
// the copy's clock: -start_at_zero removes the shifted source's offset. They
// sit off whole seconds so a frame-sized offset shows.
var copyRemuxRealCues = []struct {
	text     string
	from, to float64
}{{"Early", 1.37, 2.37}, {"Middle", 21.37, 24.8}, {"Late", 41.37, 42.37}}

// TestCopyRemuxSubtitleRenditionRealFFmpeg runs a real copy remux of a
// Matroska source whose 2.5 s keyframe interval gives segments off the 2 s
// grid, fed through a FIFO so FFmpeg cannot finish: the cues of every segment
// it has produced must be served while it still runs, each in the segments
// whose video covers it, on fMP4 and MPEG-TS, from a zero clock and from a
// source whose video starts after its subtitles. Its B-frames and re-encoded
// AAC make the fMP4 mux delay the video, which the timestamp map must
// declare to within a frame. Each subtest takes seconds of real FFmpeg work.
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

	for _, container := range []struct {
		name      string
		mpegts    bool
		offset90k int64
	}{{"fmp4", false, 0}, {"mpegts", true, HLSMPEGTSTimestampOffset90k}} {
		for _, src := range []struct {
			name, path string
			videoStart float64 // on the copy's clock, which starts with the subtitles
		}{{"zero", base, 0}, {"video-after-subtitles", shifted, 4}} {
			t.Run(container.name+"/"+src.name, func(t *testing.T) {
				fifo, release, written := feedSourceThroughFIFO(t, src.path)
				session, err := StartTranscode(ctx, TranscodeOpts{
					SessionID: "copy-rendition", InputPath: fifo, OutputDir: t.TempDir(), FFmpegPath: ffmpeg, HWAccel: HWAccelNone,
					SourceVideoCodec: "h264", TargetCodecVideo: "copy", TargetCodecAudio: "aac", CopyVideoMPEGTS: container.mpegts,
					CopySeekAnchorResolved: true, SegmentDuration: 2, TotalDuration: 48 + src.videoStart,
					SubtitleTrackIndex: 0, SubtitleCodec: "subrip", SubtitleRendition: true,
				})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = session.Close() })

				// FFmpeg is blocked on the FIFO with a quarter of the source unread.
				head := waitForProducedHead(ctx, t, session, 11)
				served := checkCopyRemuxCues(ctx, t, session, container.offset90k, src.videoStart, head, false)
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
				if served := checkCopyRemuxCues(ctx, t, session, container.offset90k, src.videoStart, head+1, true); !served["Late"] {
					t.Fatalf("the finished remux lost its last cues: %v", served)
				}
			})
		}
		// A seek generation starts at the keyframe at 20 s, numbered as the
		// segment the seek resolved to.
		t.Run(container.name+"/seek", func(t *testing.T) {
			opts := TranscodeOpts{
				InputPath: base, OutputDir: t.TempDir(), FFmpegPath: ffmpeg, HWAccel: HWAccelNone,
				SourceVideoCodec: "h264", TargetCodecVideo: "copy", TargetCodecAudio: "aac", CopyVideoMPEGTS: container.mpegts,
				SeekSeconds: 20.3, StreamOriginSeconds: 20, CopySeekAnchorResolved: true, StartSegmentNumber: 8,
				SegmentDuration: 2, TotalDuration: 48, SubtitleTrackIndex: 0, SubtitleCodec: "subrip", SubtitleRendition: true,
				subtitleCueFile: subtitleCueFileName(1),
			}
			if output, err := exec.CommandContext(ctx, ffmpeg, buildFFmpegArgs(opts)...).CombinedOutput(); err != nil {
				t.Fatalf("ffmpeg: %v\n%s", err, output)
			}
			session := &TranscodeSession{outputDir: opts.OutputDir, opts: opts}
			session.startSubtitleSpanLocked(opts)
			head, ok := session.liveProducedHead()
			if !ok || head < 12 {
				t.Fatalf("seek generation produced through segment %d", head)
			}
			if served := checkCopyRemuxCues(ctx, t, session, container.offset90k, opts.StreamOriginSeconds, head+1, true); !served["Middle"] || !served["Late"] || served["Early"] {
				t.Fatalf("seek generation served %v, want Middle and Late", served)
			}
		})
	}
}

// copyRemuxRealSources writes a 48 s H.264 Matroska source with B-frames and
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
	run("-v", "error", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-i", subs, "-t", "48",
		"-map", "0", "-map", "1", "-map", "2",
		"-c:v", "libx264", "-g", "60", "-keyint_min", "60", "-sc_threshold", "0",
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
	next := realSegmentStart(ctx, t, session, offset90k, first) - muxShift
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
