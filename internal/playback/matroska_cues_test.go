package playback

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestProbeMatroskaCueTimeline(t *testing.T) {
	info := testEBMLElement([]byte{0x15, 0x49, 0xa9, 0x66}, append(
		testEBMLElement([]byte{0x2a, 0xd7, 0xb1}, testEBMLUint(1_000_000)),
		testEBMLElement([]byte{0x44, 0x89}, testEBMLFloat64(10_000))...,
	))
	trackEntry := testEBMLElement([]byte{0xae}, append(
		testEBMLElement([]byte{0xd7}, testEBMLUint(1)),
		testEBMLElement([]byte{0x83}, testEBMLUint(1))...,
	))
	tracks := testEBMLElement([]byte{0x16, 0x54, 0xae, 0x6b}, trackEntry)
	var cuePayload []byte
	for _, timestamp := range []uint64{0, 2500, 4100, 6900} {
		positions := testEBMLElement([]byte{0xb7}, testEBMLElement([]byte{0xf7}, testEBMLUint(1)))
		point := append(testEBMLElement([]byte{0xb3}, testEBMLUint(timestamp)), positions...)
		cuePayload = append(cuePayload, testEBMLElement([]byte{0xbb}, point)...)
	}
	cues := testEBMLElement([]byte{0x1c, 0x53, 0xbb, 0x6b}, cuePayload)
	segmentPayload := append(append(info, tracks...), cues...)
	data := append(testEBMLElement([]byte{0x1a, 0x45, 0xdf, 0xa3}, nil),
		testEBMLElement([]byte{0x18, 0x53, 0x80, 0x67}, segmentPayload)...)
	path := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	keyframes, duration, err := probeMatroskaCueTimeline(context.Background(), path)
	if err != nil {
		t.Fatalf("probeMatroskaCueTimeline: %v", err)
	}
	want := []float64{0, 2.5, 4.1, 6.9}
	if len(keyframes) != len(want) {
		t.Fatalf("keyframes = %v, want %v", keyframes, want)
	}
	for i := range want {
		if math.Abs(keyframes[i]-want[i]) > 0.000001 {
			t.Fatalf("keyframes = %v, want %v", keyframes, want)
		}
	}
	if math.Abs(duration-10) > 0.000001 {
		t.Fatalf("duration = %v, want 10", duration)
	}
}

func TestProbeMatroskaCueTimeline_UsesSeekHeadPastClusters(t *testing.T) {
	info := testEBMLElement([]byte{0x15, 0x49, 0xa9, 0x66}, append(
		testEBMLElement([]byte{0x2a, 0xd7, 0xb1}, testEBMLUint(1_000_000)),
		testEBMLElement([]byte{0x44, 0x89}, testEBMLFloat64(10_000))...,
	))
	trackEntry := testEBMLElement([]byte{0xae}, append(
		testEBMLElement([]byte{0xd7}, testEBMLUint(1)),
		testEBMLElement([]byte{0x83}, testEBMLUint(1))...,
	))
	tracks := testEBMLElement([]byte{0x16, 0x54, 0xae, 0x6b}, trackEntry)
	positions := testEBMLElement([]byte{0xb7}, testEBMLElement([]byte{0xf7}, testEBMLUint(1)))
	cuePoint := append(testEBMLElement([]byte{0xb3}, testEBMLUint(0)), positions...)
	cues := testEBMLElement([]byte{0x1c, 0x53, 0xbb, 0x6b}, testEBMLElement([]byte{0xbb}, cuePoint))
	cluster := testEBMLElement([]byte{0x1f, 0x43, 0xb6, 0x75}, []byte{0})

	seekHead := testMatroskaSeekHead(0, 0, 0)
	infoPosition := uint64(len(seekHead))
	tracksPosition := infoPosition + uint64(len(info))
	cuesPosition := tracksPosition + uint64(len(tracks)) + uint64(len(cluster))
	seekHead = testMatroskaSeekHead(infoPosition, tracksPosition, cuesPosition)
	segmentPayload := append(append(append(append(seekHead, info...), tracks...), cluster...), cues...)
	data := append(testEBMLElement([]byte{0x1a, 0x45, 0xdf, 0xa3}, nil),
		testEBMLElement([]byte{0x18, 0x53, 0x80, 0x67}, segmentPayload)...)
	path := filepath.Join(t.TempDir(), "indexed.mkv")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	keyframes, duration, err := probeMatroskaCueTimeline(context.Background(), path)
	if err != nil {
		t.Fatalf("probeMatroskaCueTimeline: %v", err)
	}
	if len(keyframes) != 1 || keyframes[0] != 0 || math.Abs(duration-10) > 0.000001 {
		t.Fatalf("keyframes = %v, duration = %v", keyframes, duration)
	}
}

func TestProbeMatroskaCueTimeline_RejectsZeroTimestampScale(t *testing.T) {
	info := testEBMLElement([]byte{0x15, 0x49, 0xa9, 0x66}, append(
		testEBMLElement([]byte{0x2a, 0xd7, 0xb1}, testEBMLUint(0)),
		testEBMLElement([]byte{0x44, 0x89}, testEBMLFloat64(10_000))...,
	))
	trackEntry := testEBMLElement([]byte{0xae}, append(
		testEBMLElement([]byte{0xd7}, testEBMLUint(1)),
		testEBMLElement([]byte{0x83}, testEBMLUint(1))...,
	))
	tracks := testEBMLElement([]byte{0x16, 0x54, 0xae, 0x6b}, trackEntry)
	positions := testEBMLElement([]byte{0xb7}, testEBMLElement([]byte{0xf7}, testEBMLUint(1)))
	point := append(testEBMLElement([]byte{0xb3}, testEBMLUint(1)), positions...)
	cues := testEBMLElement([]byte{0x1c, 0x53, 0xbb, 0x6b}, testEBMLElement([]byte{0xbb}, point))
	segmentPayload := append(append(info, tracks...), cues...)
	data := append(testEBMLElement([]byte{0x1a, 0x45, 0xdf, 0xa3}, nil),
		testEBMLElement([]byte{0x18, 0x53, 0x80, 0x67}, segmentPayload)...)
	path := filepath.Join(t.TempDir(), "zero-scale.mkv")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := probeMatroskaCueTimeline(context.Background(), path); err == nil {
		t.Fatal("zero TimestampScale was accepted")
	}
}

func TestProbeMatroskaCueTimeline_FFmpegFixture(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	path := filepath.Join(t.TempDir(), "movie.mkv")
	cmd := exec.Command(ffmpeg,
		"-v", "error", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=24",
		"-t", "4", "-c:v", "mpeg4", "-g", "24", "-an", "-y", path,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg could not create Matroska fixture: %v (%s)", err, output)
	}

	keyframes, duration, err := probeMatroskaCueTimeline(context.Background(), path)
	if err != nil {
		t.Fatalf("probeMatroskaCueTimeline: %v", err)
	}
	if len(keyframes) < 3 {
		t.Fatalf("keyframes = %v, want several cue-index entries", keyframes)
	}
	if duration < 3.9 || duration > 4.1 {
		t.Fatalf("duration = %v, want about 4 seconds", duration)
	}
}

func testEBMLElement(id, payload []byte) []byte {
	if len(payload) >= 127 {
		panic("test EBML payload too large")
	}
	result := append([]byte(nil), id...)
	result = append(result, byte(0x80|len(payload)))
	return append(result, payload...)
}

func testEBMLUint(value uint64) []byte {
	var raw [8]byte
	binary.BigEndian.PutUint64(raw[:], value)
	first := 0
	for first < len(raw)-1 && raw[first] == 0 {
		first++
	}
	return raw[first:]
}

func testEBMLFloat64(value float64) []byte {
	var raw [8]byte
	binary.BigEndian.PutUint64(raw[:], math.Float64bits(value))
	return raw[:]
}

func testMatroskaSeekHead(infoPosition, tracksPosition, cuesPosition uint64) []byte {
	var payload []byte
	for _, entry := range []struct {
		id       []byte
		position uint64
	}{
		{[]byte{0x15, 0x49, 0xa9, 0x66}, infoPosition},
		{[]byte{0x16, 0x54, 0xae, 0x6b}, tracksPosition},
		{[]byte{0x1c, 0x53, 0xbb, 0x6b}, cuesPosition},
	} {
		seek := append(testEBMLElement([]byte{0x53, 0xab}, entry.id),
			testEBMLElement([]byte{0x53, 0xac}, testEBMLUintFixed8(entry.position))...)
		payload = append(payload, testEBMLElement([]byte{0x4d, 0xbb}, seek)...)
	}
	return testEBMLElement([]byte{0x11, 0x4d, 0x9b, 0x74}, payload)
}

func testEBMLUintFixed8(value uint64) []byte {
	var raw [8]byte
	binary.BigEndian.PutUint64(raw[:], value)
	return raw[:]
}
