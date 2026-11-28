package intromarkers

import (
	"math"
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/mediasample"
)

// The payload outlives the binary that wrote it, so a layout change must
// show up here rather than as fingerprints that silently fail to load.
func TestFingerprintSilencesRoundTrip(t *testing.T) {
	points := []uint32{0, 1, 0xdeadbeef, 42}
	// The second silence ends between two milliseconds, and the last was
	// still open when the window ended at 351.5.
	silences := storedSilences([]mediasample.Interval{{Start: 0, End: 1.25}, {Start: 98.5, End: 100.7499}, {Start: 340.5}}, 0, 351.5)
	payload := encodeFingerprintSilences(points, silences, 0)
	if want := 8 + 4*len(points) + 8*len(silences); len(payload) != want {
		t.Fatalf("payload is %d bytes, want %d", len(payload), want)
	}
	gotPoints, gotSilences, err := decodeFingerprintSilences(payload, 0)
	if err != nil {
		t.Fatal(err)
	}
	// What matching sees for a fresh fingerprint is what its row reads back.
	if want := []mediasample.Interval{{Start: 0, End: 1.25}, {Start: 98.5, End: 100.75}, {Start: 340.5, End: 351.5}}; !reflect.DeepEqual(silences, want) {
		t.Fatalf("stored form is %v, want %v", silences, want)
	}
	if !reflect.DeepEqual(gotPoints, points) || !reflect.DeepEqual(gotSilences, silences) {
		t.Fatalf("decoded %v %v", gotPoints, gotSilences)
	}
	if _, _, err := decodeFingerprintSilences(payload[:len(payload)-1], 0); err == nil {
		t.Fatal("truncated payload decoded")
	}
}

// Rows written before silences were stored must keep loading: dropping the
// older format would make every install read every opening again.
func TestDecodeFingerprintPayloadReadsBothFormats(t *testing.T) {
	points := []uint32{7, 0xdeadbeef, 42}
	silences := []mediasample.Interval{{Start: 1.5, End: 2.25}}
	withSilences := encodeFingerprintSilences(points, silences, 0)

	got, gotSilences := decodeFingerprintPayload(ChromaprintFormat, mediasample.EncodeRawFingerprint(points), 0)
	if !reflect.DeepEqual(got, points) || len(gotSilences) != 0 {
		t.Fatalf("older format decoded %v %v, want the points and no silences", got, gotSilences)
	}
	got, gotSilences = decodeFingerprintPayload(chromaprintSilenceFormat, withSilences, 0)
	if !reflect.DeepEqual(got, points) || !reflect.DeepEqual(gotSilences, silences) {
		t.Fatalf("current format decoded %v %v", got, gotSilences)
	}
	if got, _ := decodeFingerprintPayload(chromaprintSilenceFormat, withSilences[:len(withSilences)-1], 0); got != nil {
		t.Fatalf("truncated payload decoded %v, want a cache miss", got)
	}
	if got, _ := decodeFingerprintPayload("unknown", withSilences, 0); got != nil {
		t.Fatalf("unknown format decoded %v, want a cache miss", got)
	}
}

func TestSplitSegmentBySilences(t *testing.T) {
	// Pure silence run is dropped.
	if got := splitSegmentBySilences(Segment{Start: 0, End: 14}, []mediasample.Interval{{Start: 0, End: 14}}, 7.5); len(got) != 0 {
		t.Fatalf("pure silence returned %+v, want empty", got)
	}

	// Near-zero logo + silence + theme splits off the theme cleanly.
	got := splitSegmentBySilences(
		Segment{Start: 0, End: 45},
		[]mediasample.Interval{{Start: 0, End: 1.1}, {Start: 4.8, End: 6.5}, {Start: 44.5, End: 46}},
		7.5,
	)
	if len(got) != 1 || math.Abs(got[0].Start-6.5) > 0.01 || math.Abs(got[0].End-44.5) > 0.01 {
		t.Fatalf("near-zero split = %+v, want [6.5, 44.5]", got)
	}

	// Cold-open intro keeps its opening across an early internal pause.
	got = splitSegmentBySilences(
		Segment{Start: 73.5, End: 162},
		[]mediasample.Interval{{Start: 87.9, End: 88.45}},
		7.5,
	)
	if len(got) != 1 || math.Abs(got[0].Start-73.5) > 0.01 || math.Abs(got[0].End-162) > 0.01 {
		t.Fatalf("cold-open internal pause = %+v, want [73.5, 162]", got)
	}

	// Short intro with an internal silence falls back rather than dropping both halves.
	got = splitSegmentBySilences(
		Segment{Start: 0, End: 16},
		[]mediasample.Interval{{Start: 0, End: 2.0}, {Start: 6.0, End: 8.1}},
		7.5,
	)
	if len(got) != 1 || got[0].Start != 0 || got[0].End != 16 {
		t.Fatalf("short intro fallback = %+v, want [0, 16]", got)
	}
}

func TestRefineEdgesBySilence(t *testing.T) {
	// Cold-open start advances past a crisp 1 s transition silence, and end trims
	// back to a silence within 15 s.
	got := refineEdgesBySilence(
		Segment{Start: 86, End: 188},
		[]mediasample.Interval{{Start: 91, End: 92}, {Start: 182, End: 183.5}},
	)
	if got.Start != 92 || got.End != 182 {
		t.Fatalf("cold-open refine = %+v, want 92-182", got)
	}

	// Brief (<0.8 s) pause and long (>3 s) quiet fade-in do not move cold-open start.
	got = refineEdgesBySilence(
		Segment{Start: 82, End: 170},
		[]mediasample.Interval{{Start: 86.5, End: 87.05}},
	)
	if got.Start != 82 {
		t.Fatalf("brief pause moved start to %.2f, want 82", got.Start)
	}
	got = refineEdgesBySilence(
		Segment{Start: 144, End: 217},
		[]mediasample.Interval{{Start: 143, End: 149.5}},
	)
	if got.Start != 144 {
		t.Fatalf("long fade-in moved start to %.2f, want 144", got.Start)
	}
}
