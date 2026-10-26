package playback

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSeparateAudioHLSPrunesOwnTimelineAndPreservesOpenBody(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-time.Minute)
	writeRenditionTimeline(t, dir, "audio.m3u8", "audio_%05d.ts", 20, 1)
	writeRenditionTimeline(t, dir, "stream.m3u8", "seg_%05d.m4s", 20, 10)
	for number := 0; number < 20; number++ {
		writePrunerTestFile(t, filepath.Join(dir, fmt.Sprintf("audio_%05d.ts", number)), []byte("audio body"), old)
	}
	session := &TranscodeSession{outputDir: dir, opts: TranscodeOpts{SeparateAudioHLS: true, TargetCodecVideo: "copy", SegmentDuration: 2, SegmentRetentionSeconds: 4, SeekSeconds: 1000}}
	lease, err := session.OpenMediaSegment("audio_00014.ts")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Close() }()
	session.ReportMediaSegmentDownloadedForGeneration("audio_00019.ts", 0)
	waitForPrunerFileMissing(t, filepath.Join(dir, "audio_00014.ts"))
	for _, number := range []int{0, 1, 15, 16, 17, 18, 19} {
		assertPrunerFileExists(t, filepath.Join(dir, fmt.Sprintf("audio_%05d.ts", number)))
	}
	held, err := io.ReadAll(lease.File)
	if err != nil || string(held) != "audio body" {
		t.Fatalf("held body = %q, %v", held, err)
	}
	if session.LastRequestedSegment() != 0 {
		t.Fatal("audio download advanced video ordinal")
	}
}

func TestDV7HLSPrunesPrivateSourceWithConvertedVideo(t *testing.T) {
	dir := t.TempDir()
	raw := filepath.Join(dir, dv7SourceDirectory)
	if err := os.Mkdir(raw, 0700); err != nil {
		t.Fatal(err)
	}
	opts := TranscodeOpts{TargetCodecVideo: "copy", SeparateAudioHLS: true, DV7ToDV81: true, SegmentDuration: 2, SegmentRetentionSeconds: 10}
	var manifest strings.Builder
	manifest.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:2\n")
	old := time.Now().Add(-time.Minute)
	for number := 0; number <= 15; number++ {
		name := fmt.Sprintf("seg_%05d.m4s", number)
		fmt.Fprintf(&manifest, "#EXTINF:2.000,\n%s\n", name)
		writePrunerTestFile(t, filepath.Join(dir, name), []byte("converted"), old)
		writePrunerTestFile(t, filepath.Join(raw, name), []byte("source"), old)
	}
	writePrunerTestFile(t, filepath.Join(dir, "stream.m3u8"), []byte(manifest.String()), old)
	session := &TranscodeSession{outputDir: dir, opts: opts, lastPruneFloor: -1}
	session.ReportSegmentDownloaded(15)
	waitForPrunerFileMissing(t, filepath.Join(raw, "seg_00009.m4s"))
	assertPrunerFileMissing(t, filepath.Join(dir, "seg_00009.m4s"))
	assertPrunerFileExists(t, filepath.Join(raw, "seg_00015.m4s"))
}

func TestAudioFirstRecoveryResumesProducerWithoutVideoOrdinal(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	writeManifestRange(t, dir, 0, 100, ".ts")
	writeSegmentFile(t, dir, "seg_00100.ts", []byte("video"), now)
	s := &TranscodeSession{outputDir: dir, running: true, opts: TranscodeOpts{SeparateAudioHLS: true, TargetCodecVideo: "h264", SegmentDuration: 2}}
	writer := &recordingWriteCloser{}
	throttler := NewTranscodeThrottler(s, writer, 60, 2)
	throttler.CheckOnce()
	if !throttler.paused {
		t.Fatal("producer did not pause ahead of video demand")
	}
	done := make(chan error, 1)
	go func() {
		lease, err := s.OpenMediaSegment("audio_00150.ts")
		if lease != nil {
			_ = lease.Close()
		}
		done <- err
	}()
	waitForAudioDemands(t, s, 1)
	throttler.CheckOnce()
	if throttler.paused || s.LastRequestedSegment() != 0 {
		t.Fatal("audio-first recovery did not preserve independent video demand")
	}
	writeSegmentFile(t, dir, "audio_00150.ts", []byte("audio"), now)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("audio request did not complete")
	}
	throttler.CheckOnce()
	if !throttler.paused {
		t.Fatal("producer did not restore normal buffering after audio became available")
	}
}

func TestSeparateAudioThrottleUsesRenditionMediaTime(t *testing.T) {
	dir := t.TempDir()
	writeRenditionTimeline(t, dir, "stream.m3u8", "seg_%05d.m4s", 10, 10)
	writeRenditionTimeline(t, dir, "audio.m3u8", "audio_%05d.ts", 25, 4)
	writeSegmentFile(t, dir, "seg_00009.m4s", []byte("video"), time.Now())
	s := &TranscodeSession{outputDir: dir, running: true, opts: TranscodeOpts{SeparateAudioHLS: true, TargetCodecVideo: "copy", SegmentDuration: 2, SeekSeconds: 1000}}
	writer := &recordingWriteCloser{}
	throttler := NewTranscodeThrottler(s, writer, 60, 2)
	throttler.CheckOnce()
	if writer.writes != "p" {
		t.Fatalf("100-second video buffer commands = %q, want pause", writer.writes)
	}
	s.ReportMediaSegmentDownloadedForGeneration("audio_00015.ts", 0)
	throttler.CheckOnce()
	if writer.writes != "pu" {
		t.Fatalf("audio progress at 64 seconds commands = %q, want resume", writer.writes)
	}
	if s.LastRequestedSegment() != 0 {
		t.Fatal("audio completion changed video recovery position")
	}
}

func writeRenditionTimeline(t *testing.T, dir, manifestName, pattern string, count int, duration float64) {
	t.Helper()
	var manifest strings.Builder
	manifest.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:10\n#EXT-X-MEDIA-SEQUENCE:0\n")
	for number := range count {
		fmt.Fprintf(&manifest, "#EXTINF:%.6f,\n%s\n", duration, fmt.Sprintf(pattern, number))
	}
	if err := os.WriteFile(filepath.Join(dir, manifestName), []byte(manifest.String()), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSeparateAudioKnownDemandKeepsForwardWindow(t *testing.T) {
	dir := t.TempDir()
	writeRenditionTimeline(t, dir, "stream.m3u8", "seg_%05d.m4s", 10, 10)
	writeRenditionTimeline(t, dir, "audio.m3u8", "audio_%05d.ts", 25, 4)
	writeSegmentFile(t, dir, "seg_00009.m4s", []byte("video"), time.Now())
	s := &TranscodeSession{outputDir: dir, running: true, opts: TranscodeOpts{SeparateAudioHLS: true, TargetCodecVideo: "copy", SegmentDuration: 2}}
	writer := &recordingWriteCloser{}
	throttler := NewTranscodeThrottler(s, writer, 60, 2)
	throttler.CheckOnce()
	// Exercise scheduling directly; a missing published segment triggers recovery.
	demand := &audioSegmentDemand{generation: s.segmentGeneration, name: "audio_00002.ts"}
	s.audioSegmentDemands = map[*audioSegmentDemand]struct{}{demand: {}}
	throttler.CheckOnce()
	if writer.writes != "p" {
		t.Errorf("known audio target commands = %q, want producer to remain bounded", writer.writes)
	}
}

func TestSeparateAudioFulfilledTargetDoesNotExemptOtherWaiters(t *testing.T) {
	dir := t.TempDir()
	writeRenditionTimeline(t, dir, "stream.m3u8", "seg_%05d.m4s", 10, 10)
	writeRenditionTimeline(t, dir, "audio.m3u8", "audio_%05d.ts", 25, 4)
	writeSegmentFile(t, dir, "seg_00009.m4s", []byte("video"), time.Now())
	s := &TranscodeSession{outputDir: dir, running: true, opts: TranscodeOpts{SeparateAudioHLS: true, TargetCodecVideo: "copy", SegmentDuration: 2}}
	writer := &recordingWriteCloser{}
	throttler := NewTranscodeThrottler(s, writer, 60, 2)
	throttler.CheckOnce()
	// Keep duplicate waiters distinct while exercising scheduler state directly.
	early := &audioSegmentDemand{generation: s.segmentGeneration, name: "audio_00002.ts"}
	late := &audioSegmentDemand{generation: s.segmentGeneration, name: "audio_00018.ts"}
	duplicate := &audioSegmentDemand{generation: s.segmentGeneration, name: "audio_00018.ts"}
	s.audioSegmentDemands = map[*audioSegmentDemand]struct{}{early: {}, late: {}, duplicate: {}}
	throttler.CheckOnce()
	if writer.writes != "pu" {
		t.Errorf("pending target at 76 seconds commands = %q, want resume", writer.writes)
	}
	writeSegmentFile(t, dir, "audio_00018.ts", []byte("audio"), time.Now())
	throttler.CheckOnce()
	if writer.writes != "pup" {
		t.Errorf("fulfilled target with early waiter commands = %q, want pause", writer.writes)
	}
}

func TestSeparateAudioOldGenerationCannotAdvanceProducer(t *testing.T) {
	dir := t.TempDir()
	writeRenditionTimeline(t, dir, "stream.m3u8", "seg_%05d.m4s", 10, 10)
	writeRenditionTimeline(t, dir, "audio.m3u8", "audio_%05d.ts", 25, 4)
	writeSegmentFile(t, dir, "seg_00009.m4s", []byte("video"), time.Now())
	s := &TranscodeSession{outputDir: dir, running: true, opts: TranscodeOpts{SeparateAudioHLS: true, TargetCodecVideo: "copy", SegmentDuration: 2}}
	writer := &recordingWriteCloser{}
	throttler := NewTranscodeThrottler(s, writer, 60, 2)
	done := make(chan error, 1)
	go func() {
		lease, err := s.OpenMediaSegment("audio_00150.ts")
		if lease != nil {
			_ = lease.Close()
		}
		done <- err
	}()
	waitForAudioDemands(t, s, 1)
	throttler.CheckOnce()
	if writer.writes != "" {
		t.Errorf("cold distant audio request paused producer: %q", writer.writes)
	}
	writeSegmentFile(t, dir, "audio_00018.ts", []byte("audio"), time.Now())
	oldLease, err := s.OpenMediaSegment("audio_00018.ts")
	if err != nil {
		t.Fatal(err)
	}
	oldToken := oldLease.GenerationToken
	_ = oldLease.Close()
	s.mu.Lock()
	s.segmentGeneration++
	s.mu.Unlock()
	throttler.CheckOnce()
	if writer.writes != "p" {
		t.Errorf("old audio request changed replacement producer: %q", writer.writes)
	}
	s.ReportMediaSegmentDownloadedForGeneration("audio_00018.ts", 0)
	s.ReportMediaSegmentDownloadedForGenerationToken("audio_00018.ts", oldToken)
	throttler.CheckOnce()
	if writer.writes != "p" {
		t.Errorf("old audio acknowledgement advanced replacement: %q", writer.writes)
	}
	writeSegmentFile(t, dir, "audio_00150.ts", []byte("audio"), time.Now())
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("old audio request did not complete")
	}
	s.ReportMediaSegmentDownloadedForGeneration("audio_00018.ts", 1)
	throttler.CheckOnce()
	if writer.writes != "pu" {
		t.Errorf("replacement audio acknowledgement commands = %q, want resume", writer.writes)
	}
}

func TestSeparateAudioFutureVideoDemandKeepsProducerRunning(t *testing.T) {
	dir := t.TempDir()
	writeRenditionTimeline(t, dir, "stream.m3u8", "seg_%05d.m4s", 10, 10)
	writeSegmentFile(t, dir, "seg_00009.m4s", []byte("video"), time.Now())
	s := &TranscodeSession{outputDir: dir, running: true, lastRequestedSegment: 100, opts: TranscodeOpts{SeparateAudioHLS: true, TargetCodecVideo: "copy", SegmentDuration: 2}}
	writer := &recordingWriteCloser{}
	NewTranscodeThrottler(s, writer, 60, 2).CheckOnce()
	if writer.writes != "" {
		t.Fatalf("future video demand paused producer: %q", writer.writes)
	}
}

func TestSeparateAudioAheadOfVideoKeepsForwardWindow(t *testing.T) {
	dir := t.TempDir()
	writeRenditionTimeline(t, dir, "stream.m3u8", "seg_%05d.m4s", 10, 2)
	writeRenditionTimeline(t, dir, "audio.m3u8", "audio_%05d.ts", 25, 4)
	writeSegmentFile(t, dir, "seg_00009.m4s", []byte("video"), time.Now())
	writeSegmentFile(t, dir, "audio_00024.ts", []byte("audio"), time.Now())
	s := &TranscodeSession{outputDir: dir, running: true, opts: TranscodeOpts{SeparateAudioHLS: true, TargetCodecVideo: "copy", SegmentDuration: 2}}
	writer := &recordingWriteCloser{}
	NewTranscodeThrottler(s, writer, 60, 2).CheckOnce()
	if writer.writes != "p" {
		t.Fatalf("100-second audio buffer commands = %q, want pause", writer.writes)
	}
}

func TestSeparateAudioInheritedAudioDoesNotPauseReplacement(t *testing.T) {
	dir := t.TempDir()
	writeRenditionTimeline(t, dir, "stream.m3u8", "seg_%05d.m4s", 10, 2)
	writeRenditionTimeline(t, dir, "audio.m3u8", "audio_%05d.ts", 25, 4)
	writeSegmentFile(t, dir, "seg_00009.m4s", []byte("video"), time.Now())
	writeSegmentFile(t, dir, "audio_00024.ts", []byte("audio"), time.Now().Add(-time.Hour))
	if err := os.Chtimes(filepath.Join(dir, "audio.m3u8"), time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	s := &TranscodeSession{outputDir: dir, running: true, generationStartedAt: time.Now().Add(-time.Minute), opts: TranscodeOpts{SeparateAudioHLS: true, TargetCodecVideo: "copy", SegmentDuration: 2}}
	writer := &recordingWriteCloser{}
	NewTranscodeThrottler(s, writer, 60, 2).CheckOnce()
	if writer.writes != "" {
		t.Fatalf("inherited audio paused replacement: %q", writer.writes)
	}
}

func TestSeparateAudioRetentionRequiresCurrentTimelineAndOwnAcknowledgement(t *testing.T) {
	for _, scenario := range []string{"no-audio-ack", "unknown-duration", "inherited-manifest", "wrong-origin", "stale-generation", "stale-token", "stale-owner-token", "disabled"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			old := time.Now().Add(-time.Minute)
			writeRenditionTimeline(t, dir, "audio.m3u8", "audio_%05d.ts", 20, 1)
			path := filepath.Join(dir, "audio_00010.ts")
			writePrunerTestFile(t, path, []byte("keep"), old)
			s := &TranscodeSession{outputDir: dir, segmentGeneration: 3, generationStartedAt: old.Add(-time.Second), opts: TranscodeOpts{SeparateAudioHLS: true, TargetCodecVideo: "copy", SegmentDuration: 2, SegmentRetentionSeconds: 4}}
			t.Cleanup(func() { s.mu.Lock(); s.segmentGeneration++; s.mu.Unlock() })
			switch scenario {
			case "no-audio-ack":
				s.ReportMediaSegmentDownloadedForGeneration("seg_00019.ts", 3)
			case "unknown-duration":
				writePrunerTestFile(t, filepath.Join(dir, "audio.m3u8"), []byte("#EXTM3U\n#EXTINF:0,\naudio_00019.ts\n"), time.Now())
				s.ReportMediaSegmentDownloadedForGeneration("audio_00019.ts", 3)
			case "inherited-manifest":
				writePrunerTestFile(t, filepath.Join(dir, "audio.m3u8"), []byte("#EXTM3U\n"), old.Add(-time.Second*2))
				s.ReportMediaSegmentDownloadedForGeneration("audio_00019.ts", 3)
			case "wrong-origin":
				s.opts.StartSegmentNumber = 5
				s.ReportMediaSegmentDownloadedForGeneration("audio_00019.ts", 3)
			case "stale-generation":
				s.ReportMediaSegmentDownloadedForGeneration("audio_00019.ts", 2)
			case "stale-token":
				token := s.segmentGenerationTokenLocked()
				s.segmentGeneration++
				s.ReportMediaSegmentDownloadedForGenerationToken("audio_00019.ts", token)
			case "stale-owner-token":
				s.segmentIncarnation = "replacement"
				s.ReportMediaSegmentDownloadedForGenerationToken("audio_00019.ts", "previous:3")
			case "disabled":
				s.opts.SegmentRetentionSeconds = 0
				s.ReportMediaSegmentDownloadedForGeneration("audio_00019.ts", 3)
			}
			// Allow scheduled work to finish; checking the file alone can race a pass.
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			deadline := time.Now().Add(time.Second)
			for {
				s.mu.Lock()
				active := s.audioPrune.running
				s.mu.Unlock()
				if !active {
					break
				}
				if time.Now().After(deadline) {
					break
				}
				<-ticker.C
			}
			assertPrunerFileExists(t, path)
		})
	}
}

func TestSeparateAudioRetentionBatchesNonzeroOriginWithoutPruningInheritedOrFutureFiles(t *testing.T) {
	dir := t.TempDir()
	started := time.Now().Add(-2 * time.Minute)
	var manifest strings.Builder
	manifest.WriteString("#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:100\n")
	for number := 100; number <= 702; number++ {
		fmt.Fprintf(&manifest, "#EXTINF:1,\naudio_%05d.ts\n", number)
		writePrunerTestFile(t, filepath.Join(dir, fmt.Sprintf("audio_%05d.ts", number)), []byte("audio"), started.Add(time.Second))
	}
	writePrunerTestFile(t, filepath.Join(dir, "audio.m3u8"), []byte(manifest.String()), time.Now())
	writePrunerTestFile(t, filepath.Join(dir, "audio_00110.ts"), []byte("inherited"), started.Add(-time.Second))
	s := &TranscodeSession{outputDir: dir, generationStartedAt: started, opts: TranscodeOpts{SeparateAudioHLS: true, TargetCodecVideo: "copy", SegmentDuration: 2, StartSegmentNumber: 100, SeekSeconds: 1234, SegmentRetentionSeconds: 4}}
	t.Cleanup(func() { s.mu.Lock(); s.segmentGeneration++; s.mu.Unlock() })
	lease, err := s.OpenMediaSegment("audio_00699.ts")
	if err != nil {
		t.Fatal(err)
	}
	token := lease.GenerationToken
	_ = lease.Close()
	s.ReportMediaSegmentDownloadedForGenerationToken("audio_00699.ts", token)
	waitForPrunerFileMissing(t, filepath.Join(dir, "audio_00694.ts"))
	for _, number := range []int{100, 101, 110, 695, 699, 700, 701, 702} {
		assertPrunerFileExists(t, filepath.Join(dir, fmt.Sprintf("audio_%05d.ts", number)))
	}
	s.mu.Lock()
	floor, active := s.audioPrune.floor, s.audioPrune.running
	s.mu.Unlock()
	if floor != 695 || active {
		t.Fatalf("prune cursor = %d, active = %t", floor, active)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.audioCompleted || s.audioPrune.running {
		t.Fatal("closed session retained audio ACK/prune state")
	}
}

func BenchmarkSeparateAudioRetentionFloor(b *testing.B) {
	dir := b.TempDir()
	var manifest strings.Builder
	manifest.WriteString("#EXTM3U\n")
	for number := 0; number < 10000; number++ {
		fmt.Fprintf(&manifest, "#EXTINF:2,\naudio_%05d.ts\n", number)
	}
	if err := os.WriteFile(filepath.Join(dir, "audio.m3u8"), []byte(manifest.String()), 0600); err != nil {
		b.Fatal(err)
	}
	opts := TranscodeOpts{SeparateAudioHLS: true, SegmentRetentionSeconds: 600}
	for _, cold := range []bool{true, false} {
		name := "warm"
		if cold {
			name = "cold"
		}
		b.Run(name, func(b *testing.B) {
			s := &TranscodeSession{opts: opts, outputDir: dir}
			if _, _, err := s.mediaSegmentRetentionFloor(0, opts, 9999, true); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if cold {
					s.audioDurationIndex.reset(0)
					s.audioManifestInfo = nil
					s.audioTimeline = manifestTimeline{}
				}
				floor, complete, err := s.mediaSegmentRetentionFloor(0, opts, 9999, true)
				if err != nil || !complete || floor != 9699 {
					b.Fatalf("floor = %d, %t, %v", floor, complete, err)
				}
			}
		})
	}
}

func TestSeparateAudioRetentionExtendsCachedTimelineFromTail(t *testing.T) {
	dir := t.TempDir()
	writeRenditionTimeline(t, dir, "audio.m3u8", "audio_%05d.ts", 3001, 1)
	old := time.Now().Add(-time.Minute)
	first := filepath.Join(dir, "audio_02995.ts")
	next := filepath.Join(dir, "audio_03000.ts")
	writePrunerTestFile(t, first, []byte("first"), old)
	writePrunerTestFile(t, next, []byte("next"), old)
	s := &TranscodeSession{outputDir: dir, opts: TranscodeOpts{SeparateAudioHLS: true, SegmentDuration: 2, SegmentRetentionSeconds: 4}}
	s.ReportMediaSegmentDownloadedForGeneration("audio_03000.ts", 0)
	waitForPrunerFileMissing(t, first)
	writeRenditionTimeline(t, dir, "audio.m3u8", "audio_%05d.ts", 3006, 1)
	path := filepath.Join(dir, "audio.m3u8")
	manifest, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// A full reparse would fail on the already-indexed prefix.
	manifest = []byte(strings.Replace(string(manifest), "#EXTINF:1.000000,", "#EXTINF:invalid,", 1))
	if err := os.WriteFile(path, manifest, 0600); err != nil {
		t.Fatal(err)
	}
	s.ReportMediaSegmentDownloadedForGeneration("audio_03005.ts", 0)
	waitForPrunerFileMissing(t, next)
}

func waitForAudioDemands(t *testing.T, s *TranscodeSession, want int) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		s.mu.Lock()
		count := len(s.audioSegmentDemands)
		s.mu.Unlock()
		if count >= want {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("audio request did not register its demand")
		}
	}
}

func TestSeparateAudioRecoveryUsesPublishedTimelineAfterPruning(t *testing.T) {
	for _, tc := range []struct {
		name    string
		number  int
		running bool
		failed  bool
		stale   bool
		start   int
		want    bool
	}{
		{name: "published head", number: 2, running: true, want: true},
		{name: "pruned head and earlier cut", number: 1, running: true, want: true},
		{name: "not yet published", number: 3, running: true},
		{name: "clean EOF published head", number: 2, want: true},
		{name: "failed EOF unpublished cut", number: 3, failed: true},
		{name: "stale timeline", number: 1, running: true, stale: true},
		{name: "different start boundary", number: 1, running: true, start: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeRenditionTimeline(t, dir, "audio.m3u8", "audio_%05d.ts", 3, 2)
			session := &TranscodeSession{outputDir: dir, running: tc.running, segmentGeneration: 1, opts: TranscodeOpts{SeparateAudioHLS: true, StartSegmentNumber: tc.start}}
			if tc.failed {
				session.waitErr = errors.New("producer failed")
			}
			if tc.stale {
				session.generationStartedAt = time.Now().Add(time.Minute)
			}
			if got := session.audioSegmentNeedsRecovery(fmt.Sprintf("audio_%05d.ts", tc.number)); got != tc.want {
				t.Fatalf("missing published audio recovery = %v, want %v", got, tc.want)
			}
		})
	}
}
