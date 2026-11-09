package playback

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReportSegmentDownloadedPrunesOnlyExpiredBackBuffer(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-time.Minute)
	for segment := 0; segment <= 15; segment++ {
		writePrunerTestFile(t, filepath.Join(dir, segmentFilename(segment, TranscodeOpts{})), []byte("segment"), old)
	}
	writePrunerTestFile(t, filepath.Join(dir, "init.mp4"), []byte("init"), old)
	writePrunerTestFile(t, filepath.Join(dir, "stream.m3u8"), []byte("manifest"), old)
	writePrunerTestFile(t, filepath.Join(dir, "seg_00004.ts.tmp"), []byte("temporary"), old)
	writePrunerTestFile(t, filepath.Join(dir, "notes.txt"), []byte("other"), old)
	// A newly-created file below the floor may belong to a replacement ffmpeg
	// process and must survive this pass.
	writePrunerTestFile(t, filepath.Join(dir, segmentFilename(4, TranscodeOpts{})), []byte("fresh"), time.Now())

	session := &TranscodeSession{
		opts: TranscodeOpts{
			SessionID:               "prune-test",
			SegmentDuration:         2,
			SegmentRetentionSeconds: 10,
		},
		outputDir:            dir,
		lastPruneFloor:       -1,
		lastRequestedSegment: 0,
	}
	session.ReportSegmentDownloaded(15)
	waitForPrunerFileMissing(t, filepath.Join(dir, segmentFilename(9, TranscodeOpts{})))
	t.Cleanup(func() {
		session.mu.Lock()
		session.segmentGeneration++
		session.segmentPruneRunning = false
		session.mu.Unlock()
	})

	for _, segment := range []int{0, 1, 2, 4, 10, 11, 12, 13, 14, 15} {
		assertPrunerFileExists(t, filepath.Join(dir, segmentFilename(segment, TranscodeOpts{})))
	}
	for _, segment := range []int{3, 5, 6, 7, 8, 9} {
		assertPrunerFileMissing(t, filepath.Join(dir, segmentFilename(segment, TranscodeOpts{})))
	}
	for _, name := range []string{"init.mp4", "stream.m3u8", "seg_00004.ts.tmp", "notes.txt"} {
		assertPrunerFileExists(t, filepath.Join(dir, name))
	}
}

func TestFreshSegmentIsRetriedAfterGuardExpires(t *testing.T) {
	dir := t.TempDir()
	opts := TranscodeOpts{
		SessionID:               "retry-test",
		SegmentDuration:         2,
		SegmentRetentionSeconds: 10,
	}
	guard := 2*time.Duration(opts.SegmentDuration)*time.Second + 30*time.Second
	path := filepath.Join(dir, segmentFilename(3, opts))
	writePrunerTestFile(t, path, []byte("segment"), time.Now().Add(-guard+time.Second))
	session := &TranscodeSession{
		opts:                 opts,
		outputDir:            dir,
		lastPruneFloor:       -1,
		lastRequestedSegment: 0,
	}

	session.ReportSegmentDownloaded(9)
	waitForPrunerFileMissing(t, path)
	waitForPrunePass(t, session, 4)
}

func TestPruningContinuesAcrossBoundedBatches(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-time.Minute)
	opts := TranscodeOpts{
		SessionID:               "batch-test",
		SegmentDuration:         2,
		SegmentRetentionSeconds: 10,
	}
	for segment := 0; segment <= 600; segment++ {
		writePrunerTestFile(t, filepath.Join(dir, segmentFilename(segment, opts)), []byte("segment"), old)
	}
	session := &TranscodeSession{
		opts:                 opts,
		outputDir:            dir,
		lastPruneFloor:       -1,
		lastRequestedSegment: 0,
	}

	session.ReportSegmentDownloaded(600)
	waitForPrunerFileMissing(t, filepath.Join(dir, segmentFilename(594, opts)))
	waitForPrunePass(t, session, 595)

	for _, segment := range []int{0, 1, 2, 595, 600} {
		assertPrunerFileExists(t, filepath.Join(dir, segmentFilename(segment, opts)))
	}
}

func TestForwardRestartPrunesOnlyFilesPresentAcrossSparseGap(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-time.Minute)
	opts := TranscodeOpts{
		SessionID:               "sparse-restart-test",
		SegmentDuration:         2,
		SegmentRetentionSeconds: 10,
		StartSegmentNumber:      100_000,
	}
	for _, segment := range []int{3, 70, 99_999, 100_000} {
		writePrunerTestFile(t, filepath.Join(dir, segmentFilename(segment, opts)), []byte("segment"), old)
	}
	writePrunerTestFile(t, filepath.Join(dir, "seg_00004.ts.tmp"), []byte("temporary"), old)
	session := &TranscodeSession{
		opts:                 opts,
		outputDir:            dir,
		lastPruneFloor:       -1,
		lastRequestedSegment: opts.StartSegmentNumber,
		lastCompletedSegment: opts.StartSegmentNumber - 1,
		pruneBeforeStart:     true,
	}

	session.ReportSegmentDownloaded(opts.StartSegmentNumber + 5)
	waitForPrunerFileMissing(t, filepath.Join(dir, segmentFilename(99_999, opts)))
	waitForPrunePass(t, session, opts.StartSegmentNumber)

	for _, segment := range []int{3, 70, 99_999} {
		assertPrunerFileMissing(t, filepath.Join(dir, segmentFilename(segment, opts)))
	}
	assertPrunerFileExists(t, filepath.Join(dir, segmentFilename(100_000, opts)))
	assertPrunerFileExists(t, filepath.Join(dir, "seg_00004.ts.tmp"))
}

func TestForwardRestartPrunesPreservedRangeBeforeFreshWindowFills(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-time.Minute)
	opts := TranscodeOpts{
		SessionID:               "partial-restart-window-test",
		SegmentDuration:         2,
		SegmentRetentionSeconds: 10,
		StartSegmentNumber:      100,
	}
	for _, segment := range []int{90, 96, 97, 99, 100, 101, 102} {
		writePrunerTestFile(t, filepath.Join(dir, segmentFilename(segment, opts)), []byte("segment"), old)
	}
	session := &TranscodeSession{
		opts:                 opts,
		outputDir:            dir,
		lastPruneFloor:       90,
		lastPruneHighWater:   opts.StartSegmentNumber - 1,
		lastRequestedSegment: opts.StartSegmentNumber,
		lastCompletedSegment: opts.StartSegmentNumber - 1,
		pruneBeforeStart:     true,
	}

	// Only three replacement segments have been consumed, so the configured
	// five-segment back buffer still reaches into the preserved generation.
	// Files older than that partial window should be reclaimed immediately.
	session.ReportSegmentDownloaded(opts.StartSegmentNumber + 2)
	waitForPrunerFileMissing(t, filepath.Join(dir, segmentFilename(96, opts)))
	waitForPrunePass(t, session, 97)

	for _, segment := range []int{90, 96} {
		assertPrunerFileMissing(t, filepath.Join(dir, segmentFilename(segment, opts)))
	}
	for _, segment := range []int{97, 99, 100, 101, 102} {
		assertPrunerFileExists(t, filepath.Join(dir, segmentFilename(segment, opts)))
	}
	session.mu.Lock()
	pruneBeforeStart := session.pruneBeforeStart
	session.mu.Unlock()
	if !pruneBeforeStart {
		t.Fatal("partial replacement window discarded the remaining preserved back buffer")
	}
}

func TestCopyRetentionUsesManifestDurations(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-time.Minute)
	opts := TranscodeOpts{
		SessionID:               "copy-duration-test",
		SourceVideoCodec:        "h264",
		TargetCodecVideo:        "copy",
		SegmentDuration:         2,
		SegmentRetentionSeconds: 10,
	}
	manifest := strings.Join([]string{
		"#EXTM3U",
		"#EXT-X-VERSION:7",
		"#EXT-X-MEDIA-SEQUENCE:8",
		"#EXTINF:1.0,",
		"seg_00008.m4s",
		"#EXTINF:2.0,",
		"seg_00009.m4s",
		"#EXTINF:3.0,",
		"seg_00010.m4s",
		"#EXTINF:4.0,",
		"seg_00011.m4s",
		"#EXTINF:5.0,",
		"seg_00012.m4s",
		"#EXTINF:6.0,",
		"seg_00013.m4s",
		"#EXTINF:7.0,",
		"seg_00014.m4s",
		"#EXTINF:8.0,",
		"seg_00015.m4s",
		"",
	}, "\n")
	writePrunerTestFile(t, filepath.Join(dir, "stream.m3u8"), []byte(manifest), old)
	for segment := 8; segment <= 15; segment++ {
		writePrunerTestFile(t, filepath.Join(dir, segmentFilename(segment, opts)), []byte("segment"), old)
	}

	session := &TranscodeSession{
		opts:                 opts,
		outputDir:            dir,
		lastPruneFloor:       -1,
		lastRequestedSegment: 0,
	}
	session.ReportSegmentDownloaded(15)
	waitForPrunerFileMissing(t, filepath.Join(dir, segmentFilename(12, opts)))
	t.Cleanup(func() {
		session.mu.Lock()
		session.segmentGeneration++
		session.segmentPruneRunning = false
		session.mu.Unlock()
	})

	assertPrunerFileExists(t, filepath.Join(dir, segmentFilename(13, opts)))
	assertPrunerFileExists(t, filepath.Join(dir, segmentFilename(14, opts)))
	assertPrunerFileExists(t, filepath.Join(dir, segmentFilename(15, opts)))
}

func TestCopyRetentionParsesOnlyNewManifestEntries(t *testing.T) {
	dir := t.TempDir()
	opts := TranscodeOpts{
		SessionID:               "copy-duration-cache-test",
		SourceVideoCodec:        "h264",
		TargetCodecVideo:        "copy",
		SegmentRetentionSeconds: 10,
	}
	manifestPath := filepath.Join(dir, "stream.m3u8")
	initialManifest := copyPrunerManifest(opts, 0, 3_000)
	if err := os.WriteFile(manifestPath, []byte(initialManifest), 0o600); err != nil {
		t.Fatalf("write initial manifest: %v", err)
	}
	session := &TranscodeSession{opts: opts, outputDir: dir}

	floor, complete, err := session.segmentRetentionFloor(0, opts, 3_000)
	if err != nil {
		t.Fatalf("initial retention floor: %v", err)
	}
	if !complete || floor != 2_995 {
		t.Fatalf("initial retention floor = (%d, %t), want (2995, true)", floor, complete)
	}

	// Make the already-indexed prefix invalid before appending five entries.
	// An incremental update reads only the bounded manifest tail; reparsing the
	// complete growing playlist would encounter this sentinel and fail.
	advancedManifest := copyPrunerManifest(opts, 0, 3_005)
	advancedManifest = strings.Replace(advancedManifest, "#EXTINF:2.0,", "#EXTINF:not-a-duration,", 1)
	if err := os.WriteFile(manifestPath, []byte(advancedManifest), 0o600); err != nil {
		t.Fatalf("write advanced manifest: %v", err)
	}

	floor, complete, err = session.segmentRetentionFloor(0, opts, 3_005)
	if err != nil {
		t.Fatalf("incremental retention floor: %v", err)
	}
	if !complete || floor != 3_000 {
		t.Fatalf("incremental retention floor = (%d, %t), want (3000, true)", floor, complete)
	}
}

func TestReportSegmentDownloadedDisabledKeepsSegments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, segmentFilename(3, TranscodeOpts{}))
	writePrunerTestFile(t, path, []byte("segment"), time.Now().Add(-time.Minute))
	session := &TranscodeSession{
		opts:                 TranscodeOpts{SegmentDuration: 2},
		outputDir:            dir,
		lastPruneFloor:       -1,
		lastRequestedSegment: 0,
	}

	session.ReportSegmentDownloaded(500)
	time.Sleep(20 * time.Millisecond)
	assertPrunerFileExists(t, path)
}

func TestOldGenerationDownloadDoesNotAdvanceRestartedSession(t *testing.T) {
	session := &TranscodeSession{
		opts:                 TranscodeOpts{SegmentRetentionSeconds: 600},
		lastRequestedSegment: 100,
	}
	oldGeneration := session.SegmentGeneration()

	session.mu.Lock()
	session.segmentGeneration++
	session.lastRequestedSegment = 20
	session.mu.Unlock()

	session.ReportSegmentDownloadedForGeneration(101, oldGeneration)
	if got := session.LastRequestedSegment(); got != 20 {
		t.Fatalf("last requested segment = %d, want restarted position 20", got)
	}
}

func TestCloseInvalidatesInFlightSegmentGeneration(t *testing.T) {
	session := &TranscodeSession{
		opts:                 TranscodeOpts{SegmentRetentionSeconds: 600},
		outputDir:            t.TempDir(),
		lastRequestedSegment: 20,
	}
	downloadGeneration := session.SegmentGeneration()

	if err := session.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	session.ReportSegmentDownloadedForGeneration(100, downloadGeneration)
	if got := session.LastRequestedSegment(); got != 20 {
		t.Fatalf("last requested segment = %d, want closed-session position 20", got)
	}
}

func TestOpenSegmentDescriptorSurvivesUnlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "seg_00001.ts")
	want := []byte("complete segment")
	writePrunerTestFile(t, path, want, time.Now())
	session := &TranscodeSession{outputDir: dir, segmentGeneration: 7}

	segment, err := session.OpenSegment("seg_00001.ts")
	if err != nil {
		t.Fatalf("OpenSegment: %v", err)
	}
	defer func() { _ = segment.Close() }()
	if segment.Generation != 7 {
		t.Fatalf("segment generation = %d, want 7", segment.Generation)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove opened segment: %v", err)
	}
	got, err := io.ReadAll(segment.File)
	if err != nil {
		t.Fatalf("read opened segment: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("opened segment = %q, want %q", got, want)
	}
}

func TestGenerationTokenRejectsPriorSessionIncarnation(t *testing.T) {
	oldDir := t.TempDir()
	newDir := t.TempDir()
	const name = "seg_00007.ts"
	writePrunerTestFile(t, filepath.Join(oldDir, name), []byte("old segment"), time.Now())
	writePrunerTestFile(t, filepath.Join(newDir, name), []byte("new segment"), time.Now())

	oldSession := &TranscodeSession{outputDir: oldDir}
	oldLease, err := oldSession.OpenSegment(name)
	if err != nil {
		t.Fatalf("open old segment: %v", err)
	}
	defer func() { _ = oldLease.Close() }()

	newSession := &TranscodeSession{outputDir: newDir}
	newLease, err := newSession.OpenSegment(name)
	if err != nil {
		t.Fatalf("open new segment: %v", err)
	}
	defer func() { _ = newLease.Close() }()
	if oldLease.Generation != newLease.Generation {
		t.Fatalf("numeric generations differ: old=%d new=%d; test requires restart collision", oldLease.Generation, newLease.Generation)
	}
	if oldLease.GenerationToken == newLease.GenerationToken {
		t.Fatal("separate session objects reused an opaque generation token")
	}

	newSession.ReportSegmentDownloadedForGenerationToken(7, oldLease.GenerationToken)
	if got := newSession.LastRequestedSegment(); got != 0 {
		t.Fatalf("prior-session token advanced replacement to %d", got)
	}
	newSession.ReportSegmentDownloadedForGenerationToken(7, newLease.GenerationToken)
	if got := newSession.LastRequestedSegment(); got != 7 {
		t.Fatalf("current token advanced replacement to %d, want 7", got)
	}
}

func TestPrunePassAbortedByRestartReleasesPruneFlag(t *testing.T) {
	dir := t.TempDir()
	session := &TranscodeSession{
		outputDir:           dir,
		opts:                TranscodeOpts{SegmentRetentionSeconds: 120, SegmentDuration: 2, SessionID: "abort-release"},
		segmentPruneRunning: true,
		restarting:          &restartFlight{done: make(chan struct{})},
	}

	// A pass observing an in-flight restart of its own generation must release
	// the running flag: if that restart later fails validation and aborts, no
	// bookkeeping block ever runs, and a leaked flag would disable pruning for
	// the rest of the generation.
	session.pruneDownloadedSegments(session.segmentGeneration, 500, false)

	session.mu.Lock()
	running := session.segmentPruneRunning
	session.mu.Unlock()
	if running {
		t.Fatal("aborted prune pass left segmentPruneRunning set")
	}

	// A stale-generation pass must leave the flag alone: the generation owner
	// already reset it and may have scheduled its own pass.
	session.mu.Lock()
	session.segmentPruneRunning = true
	session.mu.Unlock()
	session.pruneDownloadedSegments(session.segmentGeneration+1, 500, false)
	session.mu.Lock()
	running = session.segmentPruneRunning
	session.mu.Unlock()
	if !running {
		t.Fatal("stale-generation prune pass cleared the current generation's flag")
	}
}

func TestOpenSegmentWaitsThroughRestartInsteadOfLeasingStaleFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "seg_00001.ts")
	writePrunerTestFile(t, path, []byte("stale segment"), time.Now())
	session := &TranscodeSession{outputDir: dir, restarting: &restartFlight{done: make(chan struct{})}}

	if segment, err := session.OpenSegment("seg_00001.ts"); !errors.Is(err, ErrSegmentNotFound) {
		if segment != nil {
			_ = segment.Close()
		}
		t.Fatalf("OpenSegment error = %v, want ErrSegmentNotFound", err)
	}
}

func writePrunerTestFile(t *testing.T, path string, contents []byte, modTime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatalf("write %s: %v", filepath.Base(path), err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("set times for %s: %v", filepath.Base(path), err)
	}
}

func copyPrunerManifest(opts TranscodeOpts, firstSegment, lastSegment int) string {
	var manifest strings.Builder
	manifest.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n")
	for segment := firstSegment; segment <= lastSegment; segment++ {
		manifest.WriteString("#EXTINF:2.0,\n")
		manifest.WriteString(segmentFilename(segment, opts))
		manifest.WriteByte('\n')
	}
	return manifest.String()
}

func waitForPrunePass(t *testing.T, session *TranscodeSession, wantFloor int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		session.mu.Lock()
		running := session.segmentPruneRunning
		floor := session.lastPruneFloor
		session.mu.Unlock()
		if !running && floor >= wantFloor {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("segment prune pass did not finish")
}

func waitForPrunerFileMissing(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("expected %s to be removed", filepath.Base(path))
}

func assertPrunerFileExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected %s to exist: %v", filepath.Base(path), err)
	}
}

func assertPrunerFileMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be removed, stat error = %v", filepath.Base(path), err)
	}
}

// The fetched span is the run of segments the client most recently finished,
// on the timeline the session serves.
func TestDeliveryProgressFetchedRun(t *testing.T) {
	encoded := TranscodeOpts{TargetCodecVideo: "h264", SegmentDuration: 4, TotalDuration: 98}
	for _, tc := range []struct {
		name      string
		opts      TranscodeOpts
		plan      []float64
		completed []int
		want      MediaSpan
		wantOK    bool
	}{
		{name: "nothing downloaded", opts: encoded},
		{name: "contiguous run", opts: encoded, completed: []int{5, 6, 7}, want: MediaSpan{Start: 20, End: 32}, wantOK: true},
		{name: "out-of-order pair keeps the run", opts: encoded, completed: []int{5, 7, 6}, want: MediaSpan{Start: 20, End: 32}, wantOK: true},
		{name: "forward seek starts a new run", opts: encoded, completed: []int{5, 6, 20, 21}, want: MediaSpan{Start: 80, End: 88}, wantOK: true},
		{name: "backward seek starts a new run", opts: encoded, completed: []int{10, 11, 3}, want: MediaSpan{Start: 12, End: 16}, wantOK: true},
		{name: "last segment ends with the media", opts: encoded, completed: []int{24}, want: MediaSpan{Start: 96, End: 98}, wantOK: true},
		{name: "keyframe plan durations", opts: TranscodeOpts{TargetCodecVideo: "copy"}, plan: []float64{2.5, 3, 1.5}, completed: []int{1, 2}, want: MediaSpan{Start: 2.5, End: 7}, wantOK: true},
	} {
		session := &TranscodeSession{outputDir: t.TempDir(), opts: tc.opts}
		if tc.plan != nil {
			session.copyPlan = &copySegmentPlan{durations: tc.plan}
		}
		for _, segment := range tc.completed {
			session.ReportSegmentDownloaded(segment)
		}
		got := session.DeliveryProgress(time.Now())
		if got.FetchedOK != tc.wantOK || got.Fetched != tc.want {
			t.Errorf("%s: fetched = (%+v, %t), want (%+v, %t)", tc.name, got.Fetched, got.FetchedOK, tc.want, tc.wantOK)
		}
	}
}

// A copy stream without a plan maps segments through FFmpeg's real playlist,
// and its produced head counts only when the current process wrote it.
func TestDeliveryProgressReadsRealPlaylist(t *testing.T) {
	dir := t.TempDir()
	opts := TranscodeOpts{TargetCodecVideo: "copy", SeekSeconds: 18, StartSegmentNumber: 9, SegmentDuration: 2}
	lines := []string{"#EXTM3U", "#EXT-X-VERSION:7", "#EXT-X-TARGETDURATION:3", "#EXT-X-MEDIA-SEQUENCE:9"}
	for i, duration := range []string{"2.5", "1.5", "2.0"} {
		name := segmentFilename(9+i, opts)
		lines = append(lines, "#EXTINF:"+duration+",", name)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("segment"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manifestPath := filepath.Join(dir, "stream.m3u8")
	if err := os.WriteFile(manifestPath, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	session := &TranscodeSession{outputDir: dir, opts: opts}
	session.ReportSegmentDownloaded(9)
	session.ReportSegmentDownloaded(10)

	got := session.DeliveryProgress(time.Now())
	if !got.FetchedOK || got.Fetched != (MediaSpan{Start: 18, End: 22}) || !got.ProducedOK || got.ProducedThrough != 24 {
		t.Fatalf("progress = %+v, want fetched 18-22 and produced through 24", got)
	}

	stale := time.Now().Add(-time.Minute)
	if err := os.Chtimes(manifestPath, stale, stale); err != nil {
		t.Fatal(err)
	}
	session.mu.Lock()
	session.generationStartedAt = time.Now()
	session.mu.Unlock()
	if got := session.DeliveryProgress(time.Now()); got.ProducedOK {
		t.Fatalf("produced through %v read from an earlier generation's playlist", got.ProducedThrough)
	}
}
