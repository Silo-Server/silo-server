package playback

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodesource"
	"github.com/Silo-Server/silo-server/internal/nodesource/nodesourcetest"
)

// An extracted track must start as an ASS script, a byte order mark aside.
func TestCheckASSScriptRequiresTheScriptInfoSection(t *testing.T) {
	dir := t.TempDir()
	for name, tc := range map[string]struct {
		text string
		ok   bool
	}{
		"script":      {"[Script Info]\nScriptType: v4.00+\n\n[Events]\n", true},
		"bom":         {utf8BOM + "[Script Info]\n[Events]\n", true},
		"blank first": {"\r\n[Script Info]\n", true},
		"events only": {"Dialogue: x\n[Events]\n", false}, //nolint:misspell // ASS event keyword
		"empty":       {"", false},
		"other text":  {"WEBVTT\n\n00:00.000 --> 00:01.000\n[Script Info]\n", false},
	} {
		path := filepath.Join(dir, strings.ReplaceAll(name, " ", "-"))
		if err := os.WriteFile(path, []byte(tc.text), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := checkASSScript(path); (err == nil) != tc.ok {
			t.Errorf("%s: checkASSScript = %v, want ok %t", name, err, tc.ok)
		}
	}
}

// A staged track is the script's only track (si=0), with the source's fonts
// in its fontsdir; a local source keeps its alias and track index.
func TestSubtitleBurnInFilterForAStagedTrack(t *testing.T) {
	opts := TranscodeOpts{InputPath: "/media/movie.mkv", SubtitleTrackIndex: 2}
	opts.subtitleFilterInputPath = "/tmp/session/subtitle-source.media"
	if got := subtitleBurnInFilter(opts, true); got != "subtitles=filename='/tmp/session/subtitle-source.media':si=2" {
		t.Fatalf("aliased filter %q", got)
	}
	useStagedSubtitle(&opts, "/tmp/a:b/subtitle-0f")
	want := `subtitles='/tmp/a\:b/subtitle-0f/selected.ass':si=0:fontsdir='/tmp/a\:b/subtitle-0f/fonts'`
	if got := subtitleBurnInFilter(opts, false); got != want {
		t.Fatalf("staged filter\n got %q\nwant %q", got, want)
	}
}

// A relay URL never reaches the subtitles filter: without a staged track a
// relayed burn-in is refused, released handle or not.
func TestCheckSubtitleFilterInputRefusesARelayURL(t *testing.T) {
	h := nodesourcetest.Relayed(t, writeSource(t))
	opts := TranscodeOpts{InputPath: h.Input(), SubtitleBurnIn: true, SubtitleTrackIndex: 0, SubtitleCodec: "ass", TargetCodecVideo: "h264"}
	if err := checkSubtitleFilterInput(opts); !errors.Is(err, ErrSubtitleStaging) {
		t.Fatalf("unstaged relayed burn-in = %v", err)
	}
	_ = h.Close()
	if err := checkSubtitleFilterInput(opts); !errors.Is(err, ErrSubtitleStaging) {
		t.Fatalf("unstaged burn-in of a released relay URL = %v", err)
	}
	useStagedSubtitle(&opts, t.TempDir())
	if err := checkSubtitleFilterInput(opts); err != nil {
		t.Fatalf("staged burn-in = %v", err)
	}
	opts.TargetCodecVideo = "copy"
	opts.subtitleFilterInputPath = ""
	if err := checkSubtitleFilterInput(opts); err != nil {
		t.Fatalf("copied video = %v", err)
	}
}

func writeSource(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.mkv")
	if err := os.WriteFile(path, []byte("relayed source bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// fastSubtitlePolls makes a session ask the core for its bundle every few
// milliseconds, and give up on a failing core after giveUp.
func fastSubtitlePolls(t *testing.T, giveUp time.Duration) {
	t.Helper()
	poll, previousGiveUp := relayedSubtitlePoll, relayedSubtitleGiveUp
	relayedSubtitlePoll, relayedSubtitleGiveUp = 5*time.Millisecond, giveUp
	t.Cleanup(func() { relayedSubtitlePoll, relayedSubtitleGiveUp = poll, previousGiveUp })
}

// testBundle is a subtitle bundle of script and fonts, by name.
func testBundle(t *testing.T, script string, fonts map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := writeBundleEntry(tw, subtitleBundleScript, int64(len(script)), strings.NewReader(script)); err != nil {
		t.Fatal(err)
	}
	for name, data := range fonts {
		if err := writeBundleEntry(tw, subtitleBundleFontsDir+"/"+name, int64(len(data)), bytes.NewReader(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fakeBundleCore is a core whose subtitle bundle requests are scripted: each
// answers 202 until release closes, then status when it is set, else bundle.
type fakeBundleCore struct {
	requests atomic.Int32
	release  chan struct{}
	status   atomic.Int32
	bundle   []byte
	relay    *nodesource.Relay
}

func newFakeBundleCore(t *testing.T) *fakeBundleCore {
	t.Helper()
	fastSubtitlePolls(t, time.Second)
	core := &fakeBundleCore{release: make(chan struct{}), bundle: testBundle(t, fixtureScriptHeader, map[string][]byte{"attachment-3.ttf": []byte("\x00\x01\x00\x00font")})}
	core.relay = nodesourcetest.CoreRelayWithSubtitles(t, core.serve)
	return core
}

func (c *fakeBundleCore) serve(w http.ResponseWriter, r *http.Request, _ string) {
	c.requests.Add(1)
	select {
	case <-c.release:
	case <-time.After(20 * time.Millisecond):
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if status := c.status.Load(); status != 0 {
		http.Error(w, "refused", int(status))
		return
	}
	_, _ = w.Write(c.bundle)
}

// relayed opens path through the core and returns its handle, closed when the
// test ends.
func (c *fakeBundleCore) relayed(t *testing.T, path string) *nodesource.Handle {
	t.Helper()
	h, err := c.relay.Open(t.Context(), nodesourcetest.Grant(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition never held")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitStaged(t *testing.T, st *stagedSubtitle) {
	t.Helper()
	select {
	case <-st.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the staging never ended")
	}
}

func relayedOpts(t *testing.T, h *nodesource.Handle) TranscodeOpts {
	return TranscodeOpts{InputPath: h.Input(), SubtitleBurnIn: true, SubtitleTrackIndex: 0, SubtitleCodec: "ass",
		TargetCodecVideo: "h264", OutputDir: t.TempDir(), SessionID: "staging"}
}

// A session asks for its bundle until the core has built it, and unpacks it
// in a directory of its own under the session's output directory.
func TestStagedSubtitleWaitsForTheCore(t *testing.T) {
	core := newFakeBundleCore(t)
	opts := relayedOpts(t, core.relayed(t, writeSource(t)))
	st, err := stageRelayedSubtitle(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer st.stop()
	waitFor(t, func() bool { return core.requests.Load() >= 2 })
	if st.ended() {
		t.Fatal("the staging ended while the core was building the bundle")
	}
	close(core.release)
	waitStaged(t, st)
	if st.err != nil || filepath.Dir(st.dir) != opts.OutputDir || !strings.HasPrefix(filepath.Base(st.dir), relayedSubtitleDirPrefix) {
		t.Fatalf("staging = %q, %v", st.dir, st.err)
	}
	if err := checkASSScript(filepath.Join(st.dir, subtitleBundleScript)); err != nil {
		t.Fatal(err)
	}
	if font, err := os.ReadFile(filepath.Join(st.dir, subtitleBundleFontsDir, "attachment-3.ttf")); err != nil || !bytes.HasPrefix(font, []byte("\x00\x01")) {
		t.Fatalf("unpacked font %q, %v", font, err)
	}
}

// A bundle the core cannot build, or a source it refuses, fails the staging
// at once and not as retryable; a core that keeps failing otherwise fails it
// as retryable once the session gives up on it.
func TestStagedSubtitleFailures(t *testing.T) {
	for _, tc := range []struct {
		status    int
		retryable bool
	}{
		{http.StatusUnprocessableEntity, false},
		{http.StatusGone, false},
		{http.StatusServiceUnavailable, true},
		{http.StatusInternalServerError, true},
	} {
		core := newFakeBundleCore(t)
		fastSubtitlePolls(t, 100*time.Millisecond)
		core.status.Store(int32(tc.status))
		close(core.release)
		st, err := stageRelayedSubtitle(relayedOpts(t, core.relayed(t, writeSource(t))))
		if err != nil {
			t.Fatal(err)
		}
		waitStaged(t, st)
		if !errors.Is(st.err, ErrSubtitleStaging) || errors.Is(st.err, ErrSubtitleStagingUnavailable) != tc.retryable {
			t.Errorf("status %d: staging = %v, want retryable %t", tc.status, st.err, tc.retryable)
		}
		if requests := core.requests.Load(); tc.retryable == (requests == 1) {
			t.Errorf("status %d: %d requests", tc.status, requests)
		}
	}

	// A download cut short, or a bundle that does not unpack, is retried as
	// a passing failure.
	core := newFakeBundleCore(t)
	fastSubtitlePolls(t, 100*time.Millisecond)
	core.bundle = core.bundle[:len(core.bundle)/2]
	close(core.release)
	opts := relayedOpts(t, core.relayed(t, writeSource(t)))
	st, err := stageRelayedSubtitle(opts)
	if err != nil {
		t.Fatal(err)
	}
	waitStaged(t, st)
	if !errors.Is(st.err, ErrSubtitleStagingUnavailable) || core.requests.Load() < 2 {
		t.Fatalf("truncated bundle: staging = %v after %d requests", st.err, core.requests.Load())
	}
	if entries, _ := os.ReadDir(opts.OutputDir); len(entries) != 0 {
		t.Fatalf("failed unpacks left %d entries", len(entries))
	}
}

// Stopping a staging cancels its request and waits for it, so nothing
// writes into the session's directory afterwards.
func TestStoppingAStagingEndsIt(t *testing.T) {
	core := newFakeBundleCore(t)
	opts := relayedOpts(t, core.relayed(t, writeSource(t)))
	st, err := stageRelayedSubtitle(opts)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return core.requests.Load() >= 1 })
	st.stop()
	if !st.failed() {
		t.Fatal("a stopped staging did not end failed")
	}
	if entries, _ := os.ReadDir(opts.OutputDir); len(entries) != 0 {
		t.Fatalf("a stopped staging left %d entries", len(entries))
	}
	if launching := goroutinesNamed("fetchSubtitleBundle"); launching != 0 {
		t.Fatalf("%d fetches outlive their staging", launching)
	}
}

// Copied video renders no subtitle and stages nothing.
func TestCopiedVideoStagesNoSubtitle(t *testing.T) {
	h := nodesourcetest.Relayed(t, writeSource(t))
	copied := TranscodeOpts{InputPath: h.Input(), SubtitleBurnIn: true, SubtitleTrackIndex: 0, SubtitleCodec: "ass", TargetCodecVideo: "copy"}
	if stagesRelayedSubtitle(copied) {
		t.Fatal("copied video stages a subtitle")
	}
	copied.TargetCodecVideo = "h264"
	if !stagesRelayedSubtitle(copied) {
		t.Fatal("a relayed text burn-in does not stage its track")
	}
}

// subtitleFixture makes an MKV with an ASS track of script, an attached
// TrueType font whose attachment filename tries to escape the font directory,
// and, after it, an attachment that claims to be a font but is not, or skips
// the test without FFmpeg or a font to attach.
func subtitleFixture(t *testing.T, script string) (mkv, font string) {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe is not installed")
	}
	fonts, _ := filepath.Glob("/usr/share/fonts/truetype/*/*.ttf")
	if len(fonts) == 0 {
		t.Skip("no TrueType font under /usr/share/fonts to attach")
	}
	dir := t.TempDir()
	ass := filepath.Join(dir, "sub.ass")
	if err := os.WriteFile(ass, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	notFont := filepath.Join(dir, "readme.txt")
	if err := os.WriteFile(notFont, []byte("not a font at all\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mkv = filepath.Join(dir, "fixture.mkv")
	out, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=25:duration=4",
		"-i", ass, "-attach", fonts[0], "-metadata:s:t:0", "mimetype=application/x-truetype-font", "-metadata:s:t:0", "filename=../../x.ttf",
		"-attach", notFont, "-metadata:s:t:1", "mimetype=font/ttf", "-metadata:s:t:1", "filename=fake.ttf",
		"-map", "0", "-map", "1", "-c:v", "mpeg4", "-c:s", "ass", mkv).CombinedOutput()
	if err != nil {
		t.Fatalf("make fixture: %v\n%s", err, out)
	}
	return mkv, fonts[0]
}

const fixtureScriptHeader = "[Script Info]\nScriptType: v4.00+\nPlayResX: 320\nPlayResY: 240\n\n[V4+ Styles]\n" +
	"Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n" +
	"Style: Default,Attached,28,&H0000FFFF,&H000000FF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,2,0,2,10,10,10,1\n\n" +
	"[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n"

// fakeFFmpeg writes a stand-in ffmpeg that records each run's argv, one
// line per run, then waits to be killed.
func fakeFFmpeg(t *testing.T) (bin, runs string) {
	t.Helper()
	dir := t.TempDir()
	bin, runs = filepath.Join(dir, "ffmpeg"), filepath.Join(dir, "runs")
	script := "#!/bin/sh\necho \"$*\" >> \"" + runs + "\"\nexec sleep 60\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, runs
}

func ffmpegRuns(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func preparingOpts(t *testing.T, h *nodesource.Handle, ffmpeg string) TranscodeOpts {
	opts := relayedOpts(t, h)
	opts.SessionID = "preparing"
	opts.FFmpegPath = ffmpeg
	opts.HWAccel = HWAccelNone
	opts.TargetCodecAudio = "aac"
	opts.SegmentDuration = 2
	opts.TotalDuration = 60
	return opts
}

// A start whose subtitle is still staging returns a preparing session at
// once. A restart while it prepares replaces the pending launch rather than
// starting FFmpeg without the subtitle; once the staging completes, FFmpeg
// starts once, with the restart's position and the staged script.
func TestPreparingSessionLaunchesOnceStaged(t *testing.T) {
	core := newFakeBundleCore(t)
	ffmpeg, runs := fakeFFmpeg(t)
	h := core.relayed(t, writeSource(t))
	session, err := StartTranscode(t.Context(), preparingOpts(t, h, ffmpeg))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	if !session.IsRunning() || !session.SegmentProgress(time.Now()).Preparing || len(ffmpegRuns(t, runs)) != 0 {
		t.Fatal("a start whose subtitle stages is not preparing")
	}
	if _, err := session.GetManifest(); !errors.Is(err, ErrManifestNotReady) {
		t.Fatalf("manifest while preparing = %v", err)
	}
	if decision := session.SegmentRecoveryDecision(40, time.Now()); !decision.Wait || decision.RestartOnTimeout || decision.Reason != "transcode_preparing" {
		t.Fatalf("segment decision while preparing = %+v", decision)
	}
	if err := session.Restart(t.Context(), 30, 15); err != nil {
		t.Fatal(err)
	}
	if !session.SegmentProgress(time.Now()).Preparing || len(ffmpegRuns(t, runs)) != 0 {
		t.Fatal("a restart while preparing started FFmpeg without its subtitle")
	}
	close(core.release)
	waitFor(t, func() bool { return len(ffmpegRuns(t, runs)) == 1 && !session.SegmentProgress(time.Now()).Preparing })
	args := ffmpegRuns(t, runs)[0]
	if !strings.Contains(args, "-ss 30") || !strings.Contains(args, subtitleBundleScript+"':si=0:fontsdir=") {
		t.Fatalf("launched ffmpeg %s", args)
	}
	fetched := core.requests.Load()
	// A later restart reuses the staged bundle and starts FFmpeg at once.
	if err := session.Restart(t.Context(), 0, 0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(ffmpegRuns(t, runs)) == 2 })
	if core.requests.Load() != fetched {
		t.Fatal("a restart fetched the bundle again")
	}
}

// Closing a preparing session cancels its pending launch and its staging:
// FFmpeg never starts, and nothing is left running.
func TestClosingAPreparingSessionCancelsItsLaunch(t *testing.T) {
	core := newFakeBundleCore(t)
	ffmpeg, runs := fakeFFmpeg(t)
	h := core.relayed(t, writeSource(t))
	session, err := StartTranscode(t.Context(), preparingOpts(t, h, ffmpeg))
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-session.Done():
	default:
		t.Fatal("Close returned with the pending launch alive")
	}
	if session.IsRunning() || !errors.Is(session.WaitError(), context.Canceled) {
		t.Fatalf("closed session: running %t, %v", session.IsRunning(), session.WaitError())
	}
	if fetching := goroutinesNamed("fetchSubtitleBundle"); fetching != 0 {
		t.Fatalf("%d fetches outlive their session", fetching)
	}
	close(core.release)
	time.Sleep(50 * time.Millisecond)
	if got := ffmpegRuns(t, runs); len(got) != 0 {
		t.Fatalf("a closed session started ffmpeg: %q", got)
	}
}

// A staging that fails ends its preparing session with the failure, which
// manifest requests answer as a failed transcode. A restart stages again.
func TestPreparingSessionEndsWithItsStagingFailure(t *testing.T) {
	core := newFakeBundleCore(t)
	core.status.Store(http.StatusUnprocessableEntity)
	close(core.release)
	ffmpeg, runs := fakeFFmpeg(t)
	h := core.relayed(t, writeSource(t))
	session, err := StartTranscode(t.Context(), preparingOpts(t, h, ffmpeg))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	waitFor(t, func() bool { return !session.IsRunning() })
	if !errors.Is(session.WaitError(), ErrSubtitleStaging) || errors.Is(session.WaitError(), ErrSubtitleStagingUnavailable) {
		t.Fatalf("session ended with %v", session.WaitError())
	}
	if err := session.WaitSubtitleStaged(t.Context()); !errors.Is(err, ErrSubtitleStaging) {
		t.Fatalf("WaitSubtitleStaged = %v", err)
	}
	if _, err := session.GetManifest(); !errors.Is(err, ErrTranscodeFailed) {
		t.Fatalf("manifest = %v, want ErrTranscodeFailed", err)
	}
	if got := ffmpegRuns(t, runs); len(got) != 0 {
		t.Fatalf("ffmpeg ran without its subtitle: %q", got)
	}
	core.status.Store(0)
	if err := session.Restart(t.Context(), 0, 0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(ffmpegRuns(t, runs)) == 1 })
}

// Close and a concurrent restart, in either order: once Close returns, no
// generation runs, and a pending launch never starts FFmpeg. A restart that
// comes after Close has begun is refused.
func TestCloseDuringARestartLeavesNothingRunning(t *testing.T) {
	ffmpeg, runs := fakeFFmpeg(t)
	for _, preparing := range []bool{true, false} {
		core := newFakeBundleCore(t)
		if !preparing {
			close(core.release)
		}
		for i := range 40 {
			h := core.relayed(t, writeSource(t))
			session, err := StartTranscode(t.Context(), preparingOpts(t, h, ffmpeg))
			if err != nil {
				t.Fatal(err)
			}
			if !preparing {
				waitFor(t, func() bool { return !session.SegmentProgress(time.Now()).Preparing })
			}
			restarted := make(chan error, 1)
			go func() { restarted <- session.Restart(t.Context(), 10, 5) }()
			if err := session.Close(); err != nil {
				t.Fatal(err)
			}
			restartErr := <-restarted
			select {
			case <-session.Done():
			default:
				t.Fatalf("preparing %t, iteration %d: Close returned with a generation alive (restart: %v)", preparing, i, restartErr)
			}
			if session.IsRunning() {
				t.Fatalf("preparing %t, iteration %d: a closed session runs (restart: %v)", preparing, i, restartErr)
			}
			if preparing {
				before := len(ffmpegRuns(t, runs))
				if launching := goroutinesNamed("launchWhenStaged") + goroutinesNamed("fetchSubtitleBundle"); launching != 0 || len(ffmpegRuns(t, runs)) != before {
					t.Fatalf("iteration %d: a closed session's launch is pending (%d) or started FFmpeg", i, launching)
				}
			}
		}
	}
}

// goroutinesNamed counts the goroutines whose stack runs fn.
func goroutinesNamed(fn string) int {
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	count := 0
	for _, stack := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(stack, "."+fn+"(") {
			count++
		}
	}
	return count
}

// writeOldGeneration leaves a playable playlist and its segments in dir, as
// an earlier session reusing the output directory would.
func writeOldGeneration(t *testing.T, dir string) {
	t.Helper()
	manifest := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:0\n"
	for i := range 4 {
		name := fmt.Sprintf("seg_%05d.ts", i)
		manifest += "#EXTINF:2.000000,\n" + name + "\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte("old segment"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "stream.m3u8"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A preparing generation has started no FFmpeg: a playlist an earlier
// generation left in its reused directory is not its readiness, which under
// hw_accel=auto would count as a hardware success. That holds for a start
// and for a restart that has to wait for the staging.
func TestPreparingGenerationIgnoresAnEarlierPlaylist(t *testing.T) {
	core := newFakeBundleCore(t)
	ffmpeg, _ := fakeFFmpeg(t)
	h := core.relayed(t, writeSource(t))
	opts := preparingOpts(t, h, ffmpeg)
	writeOldGeneration(t, opts.OutputDir)
	session, err := StartTranscode(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	if _, err := session.WaitForGenerationManifestContext(t.Context(), 300*time.Millisecond); !errors.Is(err, ErrManifestNotReady) {
		t.Fatalf("readiness of a preparing start = %v, want a timeout", err)
	}

	// A restart while preparing writes into the same directory.
	if err := session.Restart(t.Context(), 0, 0); err != nil {
		t.Fatal(err)
	}
	writeOldGeneration(t, opts.OutputDir)
	if _, err := session.WaitForGenerationManifestContext(t.Context(), 300*time.Millisecond); !errors.Is(err, ErrManifestNotReady) {
		t.Fatalf("readiness of a preparing restart = %v, want a timeout", err)
	}
}

// A preparing session holds no GPU reservation: its device is chosen at the
// start but counted only once FFmpeg launches, and released with it.
func TestPreparingSessionReservesItsGPUAtLaunch(t *testing.T) {
	core := newFakeBundleCore(t)
	ffmpeg, runs := fakeFFmpeg(t)
	h := core.relayed(t, writeSource(t))
	opts := preparingOpts(t, h, ffmpeg)
	device := "GPU-preparing-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	opts.HWAccel, opts.HWDevice = transcodeHWNVENC, device
	session, err := StartTranscode(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	if !session.SegmentProgress(time.Now()).Preparing {
		t.Fatal("the session is not preparing")
	}
	if n := HWDeviceLoadSnapshot()[device]; n != 0 {
		t.Fatalf("a preparing session holds %d reservations", n)
	}
	if session.Opts().HWDevice != device {
		t.Fatalf("the preparing session's device is %q", session.Opts().HWDevice)
	}
	// A restart while preparing reserves nothing either.
	if err := session.Restart(t.Context(), 0, 0); err != nil {
		t.Fatal(err)
	}
	if n := HWDeviceLoadSnapshot()[device]; n != 0 {
		t.Fatalf("a preparing restart holds %d reservations", n)
	}
	close(core.release)
	waitFor(t, func() bool { return len(ffmpegRuns(t, runs)) == 1 && HWDeviceLoadSnapshot()[device] == 1 })
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if n := HWDeviceLoadSnapshot()[device]; n != 0 {
		t.Fatalf("a closed session holds %d reservations", n)
	}
}

// fakeReadyFFmpeg writes a stand-in ffmpeg that publishes a playable
// playlist at once, then waits to be killed.
func fakeReadyFFmpeg(t *testing.T) string {
	t.Helper()
	ffmpeg := filepath.Join(t.TempDir(), "ffmpeg")
	script := "#!/bin/sh\n" +
		"printf '#EXTM3U\\n#EXT-X-VERSION:3\\n#EXT-X-TARGETDURATION:2\\n#EXT-X-MEDIA-SEQUENCE:0\\n' > stream.tmp\n" +
		"for i in 0 1 2 3; do printf 'segment' > seg_0000$i.ts; printf '#EXTINF:2.000000,\\nseg_0000%s.ts\\n' $i >> stream.tmp; done\n" +
		"mv stream.tmp stream.m3u8\nexec sleep 60\n"
	if err := os.WriteFile(ffmpeg, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return ffmpeg
}

// A readiness-gated start counts its subtitle staging against its request,
// not against FFmpeg's readiness wait: a staging longer than that wait still
// starts, and a staging that outlasts the request fails the start as any
// start not ready in time does.
func TestReadinessWaitsForTheStagingWithinTheRequest(t *testing.T) {
	core := newFakeBundleCore(t)
	h := core.relayed(t, writeSource(t))
	opts := preparingOpts(t, h, fakeReadyFFmpeg(t))
	start := func(ctx context.Context) (*TranscodeSession, error) {
		return StartReadyTranscode(ctx, NewAutoTranscodePipeline(ctx, opts), TranscodeStartup{Timeout: 200 * time.Millisecond})
	}
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	_, err := start(ctx)
	cancel()
	var startupErr *TranscodeStartupError
	if !errors.As(err, &startupErr) {
		t.Fatalf("a start whose staging outlasts its request = %v, want a TranscodeStartupError", err)
	}
	time.AfterFunc(400*time.Millisecond, func() { close(core.release) })
	session, err := start(t.Context())
	if err != nil {
		t.Fatalf("a start whose staging outlasts FFmpeg's readiness wait = %v", err)
	}
	defer func() { _ = session.Close() }()
}
