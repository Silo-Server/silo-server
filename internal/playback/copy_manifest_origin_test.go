package playback

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCopyManifestEvictionRetainsObservedSourceTimeline(t *testing.T) {
	// Simulate a wrap-spanning TS generation with unequal fragment lengths.
	// The first observation includes its anchor; later windows evict that entry.
	for _, initial := range []float64{0, 95440} {
		t.Run(fmt.Sprint(initial), func(t *testing.T) {
			dir := t.TempDir()
			opts := TranscodeOpts{TargetCodecVideo: "copy", CopyVideoMPEGTS: true,
				CopySeekAnchorResolved: true, StreamOriginSeconds: initial, SeekSeconds: initial,
				SegmentDuration: 2, StartSegmentNumber: 10}
			s := &TranscodeSession{opts: opts, outputDir: dir}
			for i, want := range []float64{initial, initial + 3.5, initial + 8.25} {
				manifest := fmt.Sprintf("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:5\n#EXT-X-MEDIA-SEQUENCE:%d\n", 10+i)
				for j, duration := range []float64{3.5, 4.75, 2.25}[i:] {
					manifest += fmt.Sprintf("#EXTINF:%g,\nseg_%05d.ts\n", duration, 10+i+j)
				}
				if err := os.WriteFile(filepath.Join(dir, "stream.m3u8"), []byte(manifest), 0600); err != nil {
					t.Fatal(err)
				}
				aligned, err := s.BuildSourceAlignedPlaybackManifest("", "")
				if err != nil {
					t.Fatal(err)
				}
				if want > 0 && !strings.Contains(string(aligned), "#EXT-X-GAP") {
					t.Fatalf("missing source gap: %s", aligned)
				}
				origin, ok, err := s.SegmentStartTime(10 + i)
				if err != nil || !ok || math.Abs(origin-want) > 0.000001 {
					t.Fatalf("origin=%g ok=%v err=%v want=%g", origin, ok, err, want)
				}
				number, ok, err := s.segmentNumberAtSourceTime(want)
				if err != nil || !ok || number != 10+i {
					t.Fatalf("recovery number=%d ok=%v err=%v", number, ok, err)
				}
			}
			s.mu.Lock()
			s.segmentGeneration++
			s.mu.Unlock()
			if _, _, err := s.SegmentStartTime(12); err == nil {
				t.Fatal("reused previous generation's TS epoch")
			}
		})
	}
}

func TestCopyManifestOriginRejectsStaleGenerationAndKeepsLongerWindow(t *testing.T) {
	opts := TranscodeOpts{TargetCodecVideo: "copy", CopyVideoMPEGTS: true, CopySeekAnchorResolved: true, StartSegmentNumber: 10}
	s := &TranscodeSession{opts: opts, segmentGeneration: 2}
	full := manifestTimeline{entries: []manifestSegmentEntry{{10, 3.5}, {11, 4.75}, {12, 2.25}}}
	if _, err := s.copyManifestOrigin(opts, 1, full); err == nil {
		t.Fatal("accepted stale generation")
	}
	if len(s.copyTimeline.timeline.entries) != 0 {
		t.Fatal("stale read populated cache")
	}
	s.restarting = &restartFlight{}
	if _, err := s.copyManifestOrigin(opts, 2, full); err == nil {
		t.Fatal("accepted transitional generation")
	}
	s.restarting = nil
	if _, err := s.copyManifestOrigin(opts, 2, full); err != nil {
		t.Fatal(err)
	}
	short := full
	short.entries = full.entries[:1]
	if _, err := s.copyManifestOrigin(opts, 2, short); err != nil {
		t.Fatal(err)
	}
	// The older shorter read must retain durations through segment12, and an
	// immediately adjacent window requires no timestamp probe or wrap guess.
	next := manifestTimeline{entries: []manifestSegmentEntry{{13, 3}}}
	origin, err := s.copyManifestOrigin(opts, 2, next)
	if err != nil || origin != 10.5 {
		t.Fatalf("origin=%g err=%v", origin, err)
	}
}
