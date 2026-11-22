package playback

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/tonemap"
)

// trueBinaryPath resolves `true`, which starts and exits cleanly and stands
// in for ffmpeg. It lives in /bin on Linux but /usr/bin on macOS.
func trueBinaryPath(t *testing.T) string {
	t.Helper()
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skipf("`true` not found in PATH: %v", err)
	}
	return truePath
}

// restartRetentionSession writes the given segments into a fresh output dir
// (stamped with modTime unless it is zero) and returns an h264 session with a
// 20s retention window whose player has requested segment 70.
func restartRetentionSession(t *testing.T, segments []int, modTime time.Time, lastCompleted, pruneFloor int) (*TranscodeSession, string) {
	t.Helper()
	truePath := trueBinaryPath(t)
	dir := t.TempDir()
	for _, segment := range segments {
		name := filepath.Join(dir, segmentFilename(segment, TranscodeOpts{}))
		if err := os.WriteFile(name, []byte("segment"), 0o600); err != nil {
			t.Fatal(err)
		}
		if !modTime.IsZero() {
			if err := os.Chtimes(name, modTime, modTime); err != nil {
				t.Fatal(err)
			}
		}
	}
	return &TranscodeSession{
		outputDir:            dir,
		lastRequestedSegment: 70,
		lastCompletedSegment: lastCompleted,
		lastPruneFloor:       pruneFloor,
		opts: TranscodeOpts{
			OutputDir:               dir,
			TargetCodecVideo:        "h264",
			SegmentDuration:         4,
			SegmentRetentionSeconds: 20,
			StartSegmentNumber:      0,
			FFmpegPath:              truePath,
		},
		runningRecipe: emittedRecipeOf(TranscodeOpts{
			OutputDir: dir, TargetCodecVideo: "h264", SegmentDuration: 4,
			SegmentRetentionSeconds: 20, StartSegmentNumber: 0, FFmpegPath: truePath,
		}),
	}, dir
}

func TestForwardRestartPreservesConfiguredBackBuffer(t *testing.T) {
	session, dir := restartRetentionSession(t, []int{3, 40, 64, 65, 69, 70, 100}, time.Now().Add(-time.Minute), 70, -1)

	if err := session.Restart(context.Background(), 400, 100); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	// Restart itself has no downstream-completion proof for the replacement
	// generation, so it must not unlink even old files synchronously.
	for _, segment := range []int{3, 40, 64, 65, 69, 70, 100} {
		path := filepath.Join(dir, segmentFilename(segment, TranscodeOpts{}))
		if _, err := os.Stat(path); err != nil {
			t.Errorf("segment %d was removed during restart: %v", segment, err)
		}
	}

	// Once the replacement generation has produced a complete back buffer,
	// the preserved range must become eligible again rather than leaking one
	// full window after every forward seek.
	session.ReportSegmentDownloaded(105)
	waitForPrunerFileMissing(t, filepath.Join(dir, segmentFilename(70, TranscodeOpts{})))
	for _, segment := range []int{3, 40, 64, 65, 69, 70} {
		path := filepath.Join(dir, segmentFilename(segment, TranscodeOpts{}))
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("expired pre-restart segment %d survived completed-download pruning: %v", segment, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, segmentFilename(100, TranscodeOpts{}))); err != nil {
		t.Fatalf("replacement startup segment was removed: %v", err)
	}
}

func TestNonForwardRestartKeepsPreservedRangePrunable(t *testing.T) {
	session, dir := restartRetentionSession(t, []int{3, 40, 64, 65, 70}, time.Now().Add(-time.Minute), 70, 40)

	// A restart from the reported playback position can trail the player's
	// completed-download high-water mark. The recipe is unchanged, so the
	// preserved range survives the restart and must still become prunable once
	// the replacement has produced a complete back buffer.
	if err := session.Restart(context.Background(), 260, 65); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	session.mu.Lock()
	pruneFloor := session.lastPruneFloor
	pruneBeforeStart := session.pruneBeforeStart
	session.mu.Unlock()
	if pruneFloor != 40 || !pruneBeforeStart {
		t.Fatalf("restart prune state = floor %d, pre-start %t; want floor 40, pre-start true", pruneFloor, pruneBeforeStart)
	}

	session.ReportSegmentDownloaded(70)
	waitForPrunerFileMissing(t, filepath.Join(dir, segmentFilename(64, TranscodeOpts{})))
	for _, segment := range []int{3, 40, 64} {
		path := filepath.Join(dir, segmentFilename(segment, TranscodeOpts{}))
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("expired pre-restart segment %d survived completed-download pruning: %v", segment, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, segmentFilename(65, TranscodeOpts{}))); err != nil {
		t.Fatalf("replacement startup segment was removed: %v", err)
	}
}

// A pre-restart audio switch mutates the session options, but the running
// process emitted its segments with the earlier track and downmix filter. The
// restart must use the running process's frozen recipe, so the segment at the
// restart point is discarded instead of served as wrong-generation audio.
func TestRestartAfterAudioSwitchDiscardsSegmentsFromPreviousTrack(t *testing.T) {
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skipf("`true` not found in PATH: %v", err)
	}

	dir := t.TempDir()
	opts := TranscodeOpts{
		OutputDir:               dir,
		TargetCodecVideo:        "copy",
		TargetCodecAudio:        "aac",
		AudioTrackIndex:         1,
		SourceAudioChannels:     6,
		SegmentDuration:         2,
		SegmentRetentionSeconds: 600,
		FFmpegPath:              truePath,
	}
	writeSegmentFile(t, dir, segmentFilename(9, opts), []byte("old track"), time.Now())
	writeManifestRange(t, dir, 5, 9, hlsSegmentExtension(opts))

	session := &TranscodeSession{outputDir: dir, opts: opts, runningRecipe: emittedRecipeOf(opts)}

	// The audio switch edits s.opts, exactly as the Jellyfin track switch does
	// before calling Restart.
	session.SetAudioTrackIndex(2)
	session.SetSourceAudioChannels(2)

	if err := session.Restart(context.Background(), 18, 9); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	if _, err := os.Stat(filepath.Join(dir, segmentFilename(9, opts))); !os.IsNotExist(err) {
		t.Fatalf("segment emitted with the previous audio track survived the restart: %v", err)
	}
}

func TestForwardRestartWithoutCompletedDownloadPreservesFreshFiles(t *testing.T) {
	session, dir := restartRetentionSession(t, []int{3, 40, 70}, time.Time{}, -1, -1)

	if err := session.Restart(context.Background(), 400, 100); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	for _, segment := range []int{3, 40, 70} {
		if _, err := os.Stat(filepath.Join(dir, segmentFilename(segment, TranscodeOpts{}))); err != nil {
			t.Errorf("fresh segment %d was removed without a completed download: %v", segment, err)
		}
	}
}

// TestRestartWaiterReceivesInFlightOutcome covers the single-flight outcome:
// a caller arriving while a restart is in progress must not perform its own
// restart, and it must receive the in-flight restart's result instead of
// assuming success. The first caller here fails validation; the waiter must
// surface that failure rather than returning nil.
func TestRestartWaiterReceivesInFlightOutcome(t *testing.T) {
	session := &TranscodeSession{
		outputDir:  t.TempDir(),
		restarting: &restartFlight{done: make(chan struct{})},
		opts: TranscodeOpts{
			TargetCodecVideo:   "h264",
			SegmentDuration:    2,
			StartSegmentNumber: 0,
			// Nonexistent binary: if the waiter were to start its own restart
			// instead of joining the flight, exec fails and the call returns an
			// error, failing the assertions below.
			FFmpegPath: "/nonexistent/ffmpeg-single-flight-test",
		},
	}
	flight := session.restarting

	// The in-flight leader completes its restart with a validation failure.
	go func() {
		session.mu.Lock()
		flight.err = tonemap.ErrSourceRevisionChanged
		session.mu.Unlock()
		close(flight.done)
	}()

	err := session.Restart(context.Background(), 20, 10)
	if !errors.Is(err, tonemap.ErrSourceRevisionChanged) {
		t.Fatalf("Restart during in-flight restart = %v, want the in-flight validation outcome", err)
	}

	session.mu.Lock()
	restartCount := session.restartCount
	session.mu.Unlock()
	if restartCount != 0 {
		t.Errorf("restartCount = %d, want 0 (waiter must not perform a restart)", restartCount)
	}
}

// TestRestartSeekTarget_CopyForwardJumpRequiresBoundedEnvelope pins the guard
// around the copy-mode forward-jump fallback: it only fabricates a seek target
// when the media duration is known and the requested segment lands inside that
// envelope at or after the generation's first segment. Unknown-duration,
// past-the-end, and before-start targets must stay unresolved so the caller
// keeps its retryable-miss behavior.
func TestRestartSeekTarget_CopyForwardJumpRequiresBoundedEnvelope(t *testing.T) {
	base := func() *TranscodeSession {
		return &TranscodeSession{
			outputDir: t.TempDir(),
			opts: TranscodeOpts{
				SeekSeconds:            18.261,
				StreamOriginSeconds:    18,
				CopySeekAnchorResolved: true,
				TargetCodecVideo:       "copy",
				SegmentDuration:        2,
				StartSegmentNumber:     9,
				TotalDuration:          120,
			},
		}
	}

	unknown := base()
	unknown.opts.TotalDuration = 0
	if got, ok, err := unknown.RestartSeekTarget(40); err != nil || ok || got != 0 {
		t.Fatalf("unknown-duration target = (%v, %v, %v), want (0, false, nil)", got, ok, err)
	}

	beyond := base()
	beyond.opts.TotalDuration = 60
	if got, ok, err := beyond.RestartSeekTarget(40); err != nil || ok || got != 0 {
		t.Fatalf("past-envelope target = (%v, %v, %v), want (0, false, nil)", got, ok, err)
	}

	before := base()
	if got, ok, err := before.RestartSeekTarget(5); err != nil || ok || got != 0 {
		t.Fatalf("before-start target = (%v, %v, %v), want (0, false, nil)", got, ok, err)
	}

	inEnvelope := base()
	got, ok, err := inEnvelope.RestartSeekTarget(40)
	if err != nil || !ok {
		t.Fatalf("bounded target = (%v, %v, %v), want (80, true, nil)", got, ok, err)
	}
	// base 18 + (40-9)*2 = 80.
	if math.Abs(got-80) > 0.0001 {
		t.Fatalf("bounded target = %.6f, want 80", got)
	}
}

// TestRestartSeekTarget_CopyForwardJumpScalesByManifestAverage pins the
// forward-jump estimate against the manifest's real segment timing. hls_time is
// only a lower bound: keyframe-aligned copy fragments run longer, so a long
// jump computed from the nominal duration lands early and serves content ahead
// of the timeline. With five produced fragments averaging 2.5s the estimate
// must use that average; with a single fragment it falls back to nominal 2s.
func TestRestartSeekTarget_CopyForwardJumpScalesByManifestAverage(t *testing.T) {
	baseOpts := TranscodeOpts{
		SeekSeconds:            18.261,
		StreamOriginSeconds:    18,
		CopySeekAnchorResolved: true,
		TargetCodecVideo:       "copy",
		SegmentDuration:        2,
		StartSegmentNumber:     9,
		TotalDuration:          1000,
	}
	writeManifest := func(t *testing.T, dir, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "stream.m3u8"), []byte(body), 0o644); err != nil {
			t.Fatalf("write manifest: %v", err)
		}
	}
	writeProducedSegments := func(t *testing.T, dir string, numbers ...int) {
		t.Helper()
		for _, number := range numbers {
			name := filepath.Join(dir, segmentFilename(number, baseOpts))
			if err := os.WriteFile(name, []byte("segment"), 0o644); err != nil {
				t.Fatalf("write segment %d: %v", number, err)
			}
		}
	}

	fiveDir := t.TempDir()
	fiveSegments := "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:3\n#EXT-X-MEDIA-SEQUENCE:9\n#EXT-X-MAP:URI=\"init.mp4\"\n"
	for i := 0; i < 5; i++ {
		fiveSegments += fmt.Sprintf("#EXTINF:2.500000,\nseg_%05d.m4s\n", 9+i)
	}
	writeManifest(t, fiveDir, fiveSegments)
	writeProducedSegments(t, fiveDir, 9, 10, 11, 12, 13)

	averaged := &TranscodeSession{outputDir: fiveDir, opts: baseOpts}
	got, ok, err := averaged.RestartSeekTarget(109)
	if err != nil || !ok {
		t.Fatalf("averaged RestartSeekTarget = (%v, %v, %v), want (268, true, nil)", got, ok, err)
	}
	// base 18 + (109-9)*2.5 = 268; the nominal 2s would give 218.
	if math.Abs(got-268) > 0.0001 {
		t.Fatalf("averaged forward jump = %.6f, want 268 (manifest average 2.5s)", got)
	}

	// A manifest entry with no produced file is not a real timing: with only one
	// produced segment the estimate must fall back to the nominal duration.
	singleDir := t.TempDir()
	writeManifest(t, singleDir, "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:3\n#EXT-X-MEDIA-SEQUENCE:9\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:2.500000,\nseg_00009.m4s\n")
	writeProducedSegments(t, singleDir, 9)

	nominal := &TranscodeSession{outputDir: singleDir, opts: baseOpts}
	got, ok, err = nominal.RestartSeekTarget(109)
	if err != nil || !ok {
		t.Fatalf("nominal RestartSeekTarget = (%v, %v, %v), want (218, true, nil)", got, ok, err)
	}
	// base 18 + (109-9)*2 = 218 when fewer than two produced timings exist.
	if math.Abs(got-218) > 0.0001 {
		t.Fatalf("nominal forward jump = %.6f, want 218 (nominal 2s)", got)
	}
}
