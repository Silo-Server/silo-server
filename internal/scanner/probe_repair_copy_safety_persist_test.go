package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// conflictingPPSAnnexB is a two-NAL Annex-B stream that redefines
// pic_parameter_set_id 0 with two different payloads — what
// DetectMultiplePPSH264 reports as multi-PPS. Written as printf octal escapes
// so a /bin/sh stub can emit it: 00 00 01 68 80 | 00 00 01 68 C0.
const conflictingPPSAnnexB = `\000\000\001\150\200\000\000\001\150\300`

// fakeFFmpeg writes a stub ffmpeg that appends one line to a log file per
// invocation and emits the given printf-escaped payload on stdout. It returns
// the stub's path and a func reporting how many times it ran.
func fakeFFmpeg(t *testing.T, stdoutPayload string, delay time.Duration) (string, func() int) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "invocations.log")
	ffmpegPath := filepath.Join(dir, "ffmpeg")
	sleep := ""
	if delay > 0 {
		sleep = fmt.Sprintf("sleep %.2f\n", delay.Seconds())
	}
	script := fmt.Sprintf("#!/bin/sh\necho run >> %q\n%sprintf '%s'\n", logPath, sleep, stdoutPayload)
	writeFakeTool(t, ffmpegPath, script)
	return ffmpegPath, func() int { return countFFmpegRuns(t, logPath) }
}

// fakeFFmpegGated is like fakeFFmpeg but blocks after recording its invocation
// until the returned release func is called, so a test can observe that a scan
// has actually started — rather than assume it via a fixed sleep — before
// letting it complete.
func fakeFFmpegGated(t *testing.T, stdoutPayload string) (ffmpegPath string, runs func() int, release func()) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "invocations.log")
	releasePath := filepath.Join(dir, "release")
	ffmpegPath = filepath.Join(dir, "ffmpeg")
	// The invocation is logged before the gate so the log is the signal that
	// this process has started, not that it has finished.
	script := fmt.Sprintf("#!/bin/sh\necho run >> %q\nwhile [ ! -f %q ]; do sleep 0.01; done\nprintf '%s'\n", logPath, releasePath, stdoutPayload)
	writeFakeTool(t, ffmpegPath, script)
	runs = func() int { return countFFmpegRuns(t, logPath) }
	release = func() {
		if err := os.WriteFile(releasePath, nil, 0o644); err != nil {
			t.Fatalf("write gated fake ffmpeg release file: %v", err)
		}
	}
	return ffmpegPath, runs, release
}

// countFFmpegRuns reports how many invocations a fake ffmpeg has logged.
func countFFmpegRuns(t *testing.T, logPath string) int {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("read fake ffmpeg log: %v", err)
	}
	runs := 0
	for _, b := range data {
		if b == '\n' {
			runs++
		}
	}
	return runs
}

type recordedPPSWrite struct {
	fileID      int
	multiplePPS bool
	scanSize    int64
	// scanMtime is flattened to a comparable pair so a write for a row with no
	// mtime is distinguishable from one that carries the zero time.
	scanMtime    time.Time
	scanMtimeSet bool
}

type fakeCopySafetyWriter struct {
	mu     sync.Mutex
	writes []recordedPPSWrite
	err    error
}

func (w *fakeCopySafetyWriter) UpdateMultiplePPS(_ context.Context, fileID int, multiplePPS bool, scanSize int64, scanMtime *time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	write := recordedPPSWrite{
		fileID:      fileID,
		multiplePPS: multiplePPS,
		scanSize:    scanSize,
	}
	if scanMtime != nil {
		write.scanMtime = *scanMtime
		write.scanMtimeSet = true
	}
	w.writes = append(w.writes, write)
	return w.err
}

// setErr changes what the next write returns, so a test can bring a failed
// backing store back up.
func (w *fakeCopySafetyWriter) setErr(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.err = err
}

func (w *fakeCopySafetyWriter) recorded() []recordedPPSWrite {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]recordedPPSWrite(nil), w.writes...)
}

func copySafetyTestFile(mtime time.Time) *models.MediaFile {
	modified := mtime
	return &models.MediaFile{
		ID:             42,
		FilePath:       "/library/movie.mkv",
		FileSize:       1234,
		FileModifiedAt: &modified,
		CodecVideo:     "h264",
		VideoTracks:    []models.VideoTrack{{Codec: "h264"}},
	}
}

func TestEnsureCopySafetyUsesPersistedVerdictWithoutScanning(t *testing.T) {
	ffmpegPath, runs := fakeFFmpeg(t, "", 0)
	ensurer := &PlaybackProbeEnsurer{ffmpegPath: ffmpegPath}

	mtime := time.Date(2026, time.March, 4, 5, 6, 7, 0, time.UTC)
	file := copySafetyTestFile(mtime)
	verdict := true
	scanSize := file.FileSize
	scanMtime := mtime
	file.MultiplePPS = &verdict
	file.MultiplePPSScanSize = &scanSize
	file.MultiplePPSScanMtime = &scanMtime

	got, err := ensurer.ensureCopySafety(context.Background(), file)
	if err != nil {
		t.Fatalf("ensureCopySafety() error = %v", err)
	}
	if runs() != 0 {
		t.Fatalf("ffmpeg ran %d times, want 0 for a valid persisted verdict", runs())
	}
	track := got.VideoTracks[0]
	if track.MultiplePPS == nil || !*track.MultiplePPS {
		t.Fatalf("MultiplePPS = %v, want true from the persisted verdict", track.MultiplePPS)
	}
	if !track.VideoCopyUnsafe {
		t.Fatal("VideoCopyUnsafe = false, want true for a multi-PPS file")
	}
	if _, ok := ensurer.copySafety.Load(file.ID); !ok {
		t.Fatal("persisted verdict was not promoted into the in-memory cache")
	}
}

func TestEnsureCopySafetyRescansStaleVerdict(t *testing.T) {
	mtime := time.Date(2026, time.March, 4, 5, 6, 7, 0, time.UTC)

	tests := []struct {
		name      string
		scanSize  int64
		scanMtime time.Time
	}{
		{name: "size mismatch", scanSize: 999, scanMtime: mtime},
		{name: "mtime mismatch", scanSize: 1234, scanMtime: mtime.Add(time.Hour)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ffmpegPath, runs := fakeFFmpeg(t, conflictingPPSAnnexB, 0)
			ensurer := &PlaybackProbeEnsurer{ffmpegPath: ffmpegPath}

			file := copySafetyTestFile(mtime)
			verdict := false
			scanSize := tc.scanSize
			scanMtime := tc.scanMtime
			file.MultiplePPS = &verdict
			file.MultiplePPSScanSize = &scanSize
			file.MultiplePPSScanMtime = &scanMtime

			got, err := ensurer.ensureCopySafety(context.Background(), file)
			if err != nil {
				t.Fatalf("ensureCopySafety() error = %v", err)
			}
			if runs() != 1 {
				t.Fatalf("ffmpeg ran %d times, want 1 for a stale persisted verdict", runs())
			}
			track := got.VideoTracks[0]
			if track.MultiplePPS == nil || !*track.MultiplePPS {
				t.Fatalf("MultiplePPS = %v, want the rescanned true, not the stale false", track.MultiplePPS)
			}
		})
	}
}

func TestEnsureCopySafetyPersistsScanResult(t *testing.T) {
	ffmpegPath, runs := fakeFFmpeg(t, conflictingPPSAnnexB, 0)
	writer := &fakeCopySafetyWriter{}
	ensurer := &PlaybackProbeEnsurer{ffmpegPath: ffmpegPath, copySafetyRepo: writer}

	mtime := time.Date(2026, time.March, 4, 5, 6, 7, 0, time.UTC)
	file := copySafetyTestFile(mtime)

	got, err := ensurer.ensureCopySafety(context.Background(), file)
	if err != nil {
		t.Fatalf("ensureCopySafety() error = %v", err)
	}
	if runs() != 1 {
		t.Fatalf("ffmpeg ran %d times, want 1", runs())
	}
	writes := writer.recorded()
	if len(writes) != 1 {
		t.Fatalf("UpdateMultiplePPS called %d times, want 1", len(writes))
	}
	want := recordedPPSWrite{fileID: 42, multiplePPS: true, scanSize: 1234, scanMtime: mtime, scanMtimeSet: true}
	if writes[0] != want {
		t.Fatalf("UpdateMultiplePPS(%+v), want %+v", writes[0], want)
	}
	if track := got.VideoTracks[0]; track.MultiplePPS == nil || !*track.MultiplePPS {
		t.Fatalf("MultiplePPS = %v, want true", track.MultiplePPS)
	}
}

func TestEnsureCopySafetyScanSurvivesPersistFailure(t *testing.T) {
	ffmpegPath, _ := fakeFFmpeg(t, conflictingPPSAnnexB, 0)
	writer := &fakeCopySafetyWriter{err: fmt.Errorf("database unavailable")}
	ensurer := &PlaybackProbeEnsurer{ffmpegPath: ffmpegPath, copySafetyRepo: writer}

	file := copySafetyTestFile(time.Date(2026, time.March, 4, 5, 6, 7, 0, time.UTC))

	got, err := ensurer.ensureCopySafety(context.Background(), file)
	if err != nil {
		t.Fatalf("ensureCopySafety() error = %v, want the scan result to be used anyway", err)
	}
	if track := got.VideoTracks[0]; track.MultiplePPS == nil || !*track.MultiplePPS {
		t.Fatalf("MultiplePPS = %v, want the scan result despite the failed write", track.MultiplePPS)
	}
}

// A verdict whose write failed is correct in this process but invisible to
// every other replica, which keeps rescanning the file and keeps planning fresh
// sessions onto the copy route it condemns. The next lookup has to retry the
// write — and only the write: the answer is already memoized, so re-running
// ffmpeg would pay the whole bitstream read again for nothing.
func TestEnsureCopySafetyRetriesAFailedPersistWithoutRescanning(t *testing.T) {
	ffmpegPath, runs := fakeFFmpeg(t, conflictingPPSAnnexB, 0)
	writer := &fakeCopySafetyWriter{err: fmt.Errorf("database unavailable")}
	ensurer := &PlaybackProbeEnsurer{ffmpegPath: ffmpegPath, copySafetyRepo: writer}

	mtime := time.Date(2026, time.March, 4, 5, 6, 7, 0, time.UTC)
	want := recordedPPSWrite{fileID: 42, multiplePPS: true, scanSize: 1234, scanMtime: mtime, scanMtimeSet: true}

	if _, err := ensurer.ensureCopySafety(context.Background(), copySafetyTestFile(mtime)); err != nil {
		t.Fatalf("ensureCopySafety() error = %v", err)
	}
	if runs() != 1 {
		t.Fatalf("ffmpeg ran %d times for the first call, want 1", runs())
	}
	if writes := writer.recorded(); len(writes) != 1 || writes[0] != want {
		t.Fatalf("first-call writes = %+v, want exactly [%+v]", writes, want)
	}

	// The database comes back. The next lookup answers from the memo and
	// retries the write behind it.
	writer.setErr(nil)
	got, err := ensurer.ensureCopySafety(context.Background(), copySafetyTestFile(mtime))
	if err != nil {
		t.Fatalf("ensureCopySafety() error = %v", err)
	}
	if runs() != 1 {
		t.Fatalf("ffmpeg ran %d times, want the retry to write only", runs())
	}
	if track := got.VideoTracks[0]; track.MultiplePPS == nil || !*track.MultiplePPS {
		t.Fatalf("MultiplePPS = %v, want the memoized verdict", track.MultiplePPS)
	}
	if writes := writer.recorded(); len(writes) != 2 || writes[1] != want {
		t.Fatalf("writes after the retry = %+v, want the verdict written a second time as %+v", writes, want)
	}

	// The verdict has landed, so a third lookup must not touch the row again.
	if _, err := ensurer.ensureCopySafety(context.Background(), copySafetyTestFile(mtime)); err != nil {
		t.Fatalf("ensureCopySafety() error = %v", err)
	}
	if writes := writer.recorded(); len(writes) != 2 {
		t.Fatalf("UpdateMultiplePPS called %d times, want the successful write to stop the retries", len(writes))
	}
	if runs() != 1 {
		t.Fatalf("ffmpeg ran %d times overall, want 1", runs())
	}
}

// A scan reads the opening seconds over storage that can be slow, so the file
// can be rewritten in place while it runs. The row then refuses the write, and
// the verdict is about bytes nobody is serving: it must not be memoized as
// though it described the file, and the caller has to be told, because a
// verdict that never reached the row must not be pushed at live sessions either.
func TestScanCopySafetyReportsASupersededWriteAsStale(t *testing.T) {
	ffmpegPath, runs := fakeFFmpeg(t, conflictingPPSAnnexB, 0)
	writer := &fakeCopySafetyWriter{err: ErrStaleCopySafetyScan}
	ensurer := &PlaybackProbeEnsurer{ffmpegPath: ffmpegPath, copySafetyRepo: writer}

	mtime := time.Date(2026, time.March, 4, 5, 6, 7, 0, time.UTC)
	file := copySafetyTestFile(mtime)

	multi, stale, err := ensurer.ScanCopySafety(context.Background(), file)
	if err != nil {
		t.Fatalf("ScanCopySafety() error = %v, want a superseded write reported as stale, not failed", err)
	}
	if !stale {
		t.Fatal("ScanCopySafety() stale = false, want true for a write the row refused")
	}
	if multi {
		t.Fatal("ScanCopySafety() = true alongside stale, want no verdict claimed for a superseded generation")
	}
	if runs() != 1 {
		t.Fatalf("ffmpeg ran %d times, want 1", runs())
	}
	// Nothing was memoized, so the file is still unresolved: the next request
	// for whatever generation the row now holds scans it properly.
	if !ensurer.NeedsCopySafetyScan(file) {
		t.Fatal("NeedsCopySafetyScan() = false after a superseded write, want the file still unresolved")
	}
	if _, known := ensurer.KnownCopySafetyVerdict(context.Background(), file); known {
		t.Fatal("KnownCopySafetyVerdict() reported a verdict for a superseded generation")
	}
}

// KnownCopySafetyVerdict is the question the serve gate and the race both ask.
// It has to see the memo — a verdict whose write failed is authoritative here
// and invisible on the row — and it must retry that write, without ffmpeg.
func TestKnownCopySafetyVerdictSeesTheMemoAndRetriesTheWrite(t *testing.T) {
	ffmpegPath, runs := fakeFFmpeg(t, conflictingPPSAnnexB, 0)
	writer := &fakeCopySafetyWriter{err: fmt.Errorf("database unavailable")}
	ensurer := &PlaybackProbeEnsurer{ffmpegPath: ffmpegPath, copySafetyRepo: writer}

	mtime := time.Date(2026, time.March, 4, 5, 6, 7, 0, time.UTC)
	file := copySafetyTestFile(mtime)

	if _, _, err := ensurer.ScanCopySafety(context.Background(), file); err != nil {
		t.Fatalf("ScanCopySafety() error = %v", err)
	}
	if writes := writer.recorded(); len(writes) != 1 {
		t.Fatalf("UpdateMultiplePPS called %d times for the first scan, want 1", len(writes))
	}

	multi, known := ensurer.KnownCopySafetyVerdict(context.Background(), file)
	if !known || !multi {
		t.Fatalf("KnownCopySafetyVerdict() = (%t, %t), want the unpersisted unsafe verdict", multi, known)
	}
	if runs() != 1 {
		t.Fatalf("ffmpeg ran %d times, want the known-verdict lookup to never scan", runs())
	}
	if writes := writer.recorded(); len(writes) != 2 {
		t.Fatalf("UpdateMultiplePPS called %d times, want the unpersisted write retried", len(writes))
	}

	// An untouched file is simply unknown; the lookup never invents a verdict.
	other := copySafetyTestFile(mtime)
	other.ID = 43
	if _, known := ensurer.KnownCopySafetyVerdict(context.Background(), other); known {
		t.Fatal("KnownCopySafetyVerdict() reported a verdict for a file nothing has scanned")
	}
}

// Rows predating the file_modified_at column carry no mtime. Their verdict is
// still persisted and still honored on read — refusing to write it would leave
// them permanently unverdicted, so every replica would rescan the same file and
// tear down the same playback again.
func TestEnsureCopySafetyPersistsVerdictForRowWithoutMtime(t *testing.T) {
	ffmpegPath, runs := fakeFFmpeg(t, conflictingPPSAnnexB, 0)
	writer := &fakeCopySafetyWriter{}
	ensurer := &PlaybackProbeEnsurer{ffmpegPath: ffmpegPath, copySafetyRepo: writer}

	file := copySafetyTestFile(time.Now())
	file.FileModifiedAt = nil

	if _, err := ensurer.ensureCopySafety(context.Background(), file); err != nil {
		t.Fatalf("ensureCopySafety() error = %v", err)
	}
	if runs() != 1 {
		t.Fatalf("ffmpeg ran %d times, want 1", runs())
	}
	writes := writer.recorded()
	want := recordedPPSWrite{fileID: 42, multiplePPS: true, scanSize: 1234}
	if len(writes) != 1 || writes[0] != want {
		t.Fatalf("UpdateMultiplePPS writes = %+v, want exactly [%+v]", writes, want)
	}

	// A fresh process reading that row back must trust the verdict on size
	// alone rather than rescanning.
	reread := copySafetyTestFile(time.Now())
	reread.FileModifiedAt = nil
	verdict := true
	scanSize := reread.FileSize
	reread.MultiplePPS = &verdict
	reread.MultiplePPSScanSize = &scanSize
	cold := &PlaybackProbeEnsurer{ffmpegPath: ffmpegPath}
	if cold.NeedsCopySafetyScan(reread) {
		t.Fatal("NeedsCopySafetyScan() = true, want the persisted mtime-less verdict honored")
	}
	got, err := cold.ensureCopySafety(context.Background(), reread)
	if err != nil {
		t.Fatalf("ensureCopySafety() error = %v", err)
	}
	if track := got.VideoTracks[0]; track.MultiplePPS == nil || !*track.MultiplePPS || !track.VideoCopyUnsafe {
		t.Fatalf("track = %+v, want the persisted multi-PPS verdict stamped", track)
	}
}

func TestEnsureCopySafetyWithoutRepoDoesNotPanic(t *testing.T) {
	ffmpegPath, runs := fakeFFmpeg(t, conflictingPPSAnnexB, 0)
	ensurer := &PlaybackProbeEnsurer{ffmpegPath: ffmpegPath}

	file := copySafetyTestFile(time.Date(2026, time.March, 4, 5, 6, 7, 0, time.UTC))

	if _, err := ensurer.ensureCopySafety(context.Background(), file); err != nil {
		t.Fatalf("ensureCopySafety() error = %v", err)
	}
	if runs() != 1 {
		t.Fatalf("ffmpeg ran %d times, want 1", runs())
	}
}

// A failed scan must stay fail-closed and stateless: nothing is written, so a
// transient error never becomes sticky state on the row and the next request
// retries cleanly.
func TestEnsureCopySafetyFailureRecordsNothing(t *testing.T) {
	writer := &fakeCopySafetyWriter{}
	ensurer := &PlaybackProbeEnsurer{
		ffmpegPath:     filepath.Join(t.TempDir(), "missing-ffmpeg"),
		copySafetyRepo: writer,
	}

	file := copySafetyTestFile(time.Date(2026, time.March, 4, 5, 6, 7, 0, time.UTC))

	got, err := ensurer.ensureCopySafety(context.Background(), file)
	if err != nil {
		t.Fatalf("ensureCopySafety() error = %v", err)
	}
	if !got.VideoTracks[0].VideoCopyUnsafe {
		t.Fatal("VideoCopyUnsafe = false, want true after an inconclusive scan")
	}
	if writes := writer.recorded(); len(writes) != 0 {
		t.Fatalf("failed scan recorded %d verdicts, want 0", len(writes))
	}
}

// Browse surfaces call EnsureProbeOnly. The copy-safety verdict never reaches
// a client from those responses, so scanning there was pure warm-up — and the
// read it costs is what made first-time browsing slow on remote storage.
func TestEnsureProbeOnlySkipsCopySafetyScan(t *testing.T) {
	ffmpegPath, runs := fakeFFmpeg(t, conflictingPPSAnnexB, 0)
	writer := &fakeCopySafetyWriter{}
	ensurer := &PlaybackProbeEnsurer{ffmpegPath: ffmpegPath, copySafetyRepo: writer}

	file := copySafetyTestFile(time.Date(2026, time.March, 4, 5, 6, 7, 0, time.UTC))

	got, err := ensurer.EnsureProbeOnly(context.Background(), file)
	if err != nil {
		t.Fatalf("EnsureProbeOnly() error = %v", err)
	}
	if runs() != 0 {
		t.Fatalf("ffmpeg ran %d times for a browse-detail load, want 0", runs())
	}
	if got.VideoTracks[0].MultiplePPS != nil {
		t.Fatal("EnsureProbeOnly() resolved the copy-safety verdict")
	}
	if got.VideoTracks[0].VideoCopyUnsafe {
		t.Fatal("EnsureProbeOnly() marked the file copy-unsafe")
	}
	if writes := writer.recorded(); len(writes) != 0 {
		t.Fatalf("EnsureProbeOnly() persisted %d verdicts, want 0", len(writes))
	}

	// The same ensurer still scans when a play is being prepared.
	if _, err := ensurer.Ensure(context.Background(), file); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if runs() != 1 {
		t.Fatalf("ffmpeg ran %d times for a playback load, want 1", runs())
	}
}

func TestEnsureCopySafetyConcurrentCallsScanOnce(t *testing.T) {
	ffmpegPath, runs, release := fakeFFmpegGated(t, conflictingPPSAnnexB)
	writer := &fakeCopySafetyWriter{}
	ensurer := &PlaybackProbeEnsurer{ffmpegPath: ffmpegPath, copySafetyRepo: writer}

	mtime := time.Date(2026, time.March, 4, 5, 6, 7, 0, time.UTC)

	const callers = 8
	var wg sync.WaitGroup
	var entered atomic.Int64
	results := make([]*models.MediaFile, callers)
	errs := make([]error, callers)
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			entered.Add(1)
			results[i], errs[i] = ensurer.ensureCopySafety(context.Background(), copySafetyTestFile(mtime))
		}(i)
	}
	close(start)

	// Hold the winning ffmpeg inside its scan until observable state says the
	// dedup path is genuinely under test: every caller has reached the
	// ensureCopySafety call site, and exactly one ffmpeg process has started.
	//
	// ensureCopySafety dedupes through a singleflight.Group, which exposes no
	// waiter count, so "all callers entered" is the strongest signal available
	// from outside the package. It is sufficient here: between the call site
	// and singleflight.Do, ensureCopySafety only does non-blocking work (a
	// codec check and a sync.Map lookup), so a caller that has entered reaches
	// the dedup point without waiting on anything — and the scan it would
	// otherwise start for itself is still blocked when it gets there.
	timeout := ""
	deadline := time.Now().Add(5 * time.Second)
	for int(entered.Load()) != callers || runs() != 1 {
		if time.Now().After(deadline) {
			timeout = fmt.Sprintf("timed out waiting for the deduped scan to start: %d/%d callers entered, %d ffmpeg runs", entered.Load(), callers, runs())
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Always release, even on timeout, so the callers are not left blocked.
	release()
	wg.Wait()
	if timeout != "" {
		t.Fatal(timeout)
	}

	for i, err := range errs {
		if err != nil {
			t.Fatalf("ensureCopySafety() caller %d error = %v", i, err)
		}
		if track := results[i].VideoTracks[0]; track.MultiplePPS == nil || !*track.MultiplePPS {
			t.Fatalf("caller %d MultiplePPS = %v, want true", i, track.MultiplePPS)
		}
	}
	if got := runs(); got != 1 {
		t.Fatalf("ffmpeg ran %d times for %d concurrent callers, want 1", got, callers)
	}
	if writes := writer.recorded(); len(writes) != 1 {
		t.Fatalf("UpdateMultiplePPS called %d times, want 1", len(writes))
	}
}

// A file rewritten in place keeps its row ID and changes its bytes. Two callers
// that disagree about the size and mtime are therefore looking at two different
// bitstreams, and must not share a scan: the joiner would take the leader's
// verdict for its own file — clearing the condemnation of a copy-unsafe
// replacement, or condemning a copy-safe one.
func TestCopySafetyScanDoesNotShareAFlightAcrossFileGenerations(t *testing.T) {
	mtime := time.Date(2026, time.March, 4, 5, 6, 7, 0, time.UTC)

	for _, tc := range []struct {
		name   string
		mutate func(*models.MediaFile)
	}{
		{
			name:   "size changed",
			mutate: func(f *models.MediaFile) { f.FileSize = 9999 },
		},
		{
			name: "mtime changed",
			mutate: func(f *models.MediaFile) {
				later := mtime.Add(time.Hour)
				f.FileModifiedAt = &later
			},
		},
		{
			name:   "mtime dropped",
			mutate: func(f *models.MediaFile) { f.FileModifiedAt = nil },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ffmpegPath, runs, release := fakeFFmpegGated(t, conflictingPPSAnnexB)
			writer := &fakeCopySafetyWriter{}
			ensurer := &PlaybackProbeEnsurer{ffmpegPath: ffmpegPath, copySafetyRepo: writer}

			replaced := copySafetyTestFile(mtime)
			tc.mutate(replaced)

			var wg sync.WaitGroup
			errs := make([]error, 2)
			for i, file := range []*models.MediaFile{copySafetyTestFile(mtime), replaced} {
				wg.Add(1)
				go func(i int, file *models.MediaFile) {
					defer wg.Done()
					_, errs[i] = ensurer.ensureCopySafety(context.Background(), file)
				}(i, file)
			}

			// Both scans are held inside the stub, so "two ffmpeg processes have
			// started" is only reachable if the two generations were given
			// separate flights. A shared flight leaves the second caller parked
			// on the first and the count stuck at one.
			timeout := ""
			deadline := time.Now().Add(5 * time.Second)
			for runs() != 2 {
				if time.Now().After(deadline) {
					timeout = fmt.Sprintf("timed out with %d ffmpeg runs, want one scan per file generation", runs())
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			release()
			wg.Wait()
			if timeout != "" {
				t.Fatal(timeout)
			}
			for i, err := range errs {
				if err != nil {
					t.Fatalf("ensureCopySafety() caller %d error = %v", i, err)
				}
			}
			if writes := writer.recorded(); len(writes) != 2 {
				t.Fatalf("UpdateMultiplePPS called %d times, want one verdict per generation: %+v", len(writes), writes)
			}
		})
	}
}

// The same generation read twice — with the mtime differing only below the
// precision the row can store — is one bitstream and must still share one scan.
func TestCopySafetyScanSharesAFlightAcrossSubMicrosecondMtimeDrift(t *testing.T) {
	ffmpegPath, runs, release := fakeFFmpegGated(t, conflictingPPSAnnexB)
	writer := &fakeCopySafetyWriter{}
	ensurer := &PlaybackProbeEnsurer{ffmpegPath: ffmpegPath, copySafetyRepo: writer}

	mtime := time.Date(2026, time.March, 4, 5, 6, 7, 0, time.UTC)
	jittered := copySafetyTestFile(mtime)
	drifted := mtime.Add(17 * time.Nanosecond).Local()
	jittered.FileModifiedAt = &drifted

	var wg sync.WaitGroup
	var entered atomic.Int64
	errs := make([]error, 2)
	for i, file := range []*models.MediaFile{copySafetyTestFile(mtime), jittered} {
		wg.Add(1)
		go func(i int, file *models.MediaFile) {
			defer wg.Done()
			entered.Add(1)
			_, errs[i] = ensurer.ensureCopySafety(context.Background(), file)
		}(i, file)
	}

	timeout := ""
	deadline := time.Now().Add(5 * time.Second)
	for entered.Load() != 2 || runs() != 1 {
		if time.Now().After(deadline) {
			timeout = fmt.Sprintf("timed out: %d callers entered, %d ffmpeg runs", entered.Load(), runs())
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	release()
	wg.Wait()
	if timeout != "" {
		t.Fatal(timeout)
	}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("ensureCopySafety() caller %d error = %v", i, err)
		}
	}
	if got := runs(); got != 1 {
		t.Fatalf("ffmpeg ran %d times, want sub-microsecond mtime drift to share one scan", got)
	}
}

func TestPersistedCopySafetyVerdict(t *testing.T) {
	mtime := time.Date(2026, time.March, 4, 5, 6, 7, 0, time.UTC)
	base := func() *models.MediaFile {
		file := copySafetyTestFile(mtime)
		verdict := true
		scanSize := file.FileSize
		scanMtime := mtime
		file.MultiplePPS = &verdict
		file.MultiplePPSScanSize = &scanSize
		file.MultiplePPSScanMtime = &scanMtime
		return file
	}

	tests := []struct {
		name      string
		mutate    func(*models.MediaFile)
		wantMulti bool
		wantOK    bool
	}{
		{name: "valid", mutate: func(*models.MediaFile) {}, wantMulti: true, wantOK: true},
		{name: "never scanned", mutate: func(f *models.MediaFile) { f.MultiplePPS = nil }},
		{name: "missing scan size", mutate: func(f *models.MediaFile) { f.MultiplePPSScanSize = nil }},
		{name: "missing scan mtime", mutate: func(f *models.MediaFile) { f.MultiplePPSScanMtime = nil }},
		{name: "size drifted", mutate: func(f *models.MediaFile) { f.FileSize = 4321 }},
		{
			name: "mtime drifted",
			mutate: func(f *models.MediaFile) {
				later := mtime.Add(time.Second)
				f.FileModifiedAt = &later
			},
		},
		{name: "file mtime unknown", mutate: func(f *models.MediaFile) { f.FileModifiedAt = nil }},
		{
			name: "sub-microsecond mtime drift is absorbed",
			mutate: func(f *models.MediaFile) {
				jittered := mtime.Add(17 * time.Nanosecond).Local()
				f.FileModifiedAt = &jittered
			},
			wantMulti: true,
			wantOK:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			file := base()
			tc.mutate(file)
			multi, ok := persistedCopySafetyVerdict(file)
			if ok != tc.wantOK || multi != tc.wantMulti {
				t.Fatalf("persistedCopySafetyVerdict() = (%v, %v), want (%v, %v)", multi, ok, tc.wantMulti, tc.wantOK)
			}
		})
	}
}
