package playback

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// A window cut from a cached full-track .sup must hold the cues of that window
// at their stored times. The cached file's start time is its first cue, so an
// extract that lets ffmpeg offset the seek by it (or rebase onto it) starts
// late or comes out early by that much.
func TestStreamExtractArgs_CachedSupWindowsRealFFmpeg(t *testing.T) {
	if testing.Short() {
		t.Skip("real FFmpeg integration test")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	// One cue every 10 s from 40 s to 400 s, as an unseeked full extract
	// writes them: on the playback timeline, first cue well after zero.
	var cues []float64
	for at := 40.0; at <= 400; at += 10 {
		cues = append(cues, at)
	}
	cached := filepath.Join(t.TempDir(), "track.sup")
	if err := os.WriteFile(cached, testSupStream(cues, 0), 0o600); err != nil {
		t.Fatal(err)
	}

	// Each window is [seek, seek+60): the end is exclusive.
	for _, tc := range []struct {
		name string
		seek float64
	}{
		{name: "from the start", seek: 0},
		{name: "before the first cue", seek: 35},
		{name: "onto the first cue", seek: 40},
		{name: "after a seek", seek: 95},
		{name: "a seek onto a cue", seek: 100},
		{name: "an extend window", seek: 355},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			firstCue, err := readSupFirstCue(cached)
			if err != nil {
				t.Fatal(err)
			}
			args := streamExtractArgs(windowFromCachedSup(StreamExtractOpts{
				InputPath:           cached,
				SourceCodec:         "hdmv_pgs_subtitle",
				SeekSeconds:         tc.seek,
				DurationSeconds:     60,
				AllowWindow:         true,
				InputIsExtractedSup: true,
			}, firstCue))
			var stdout, stderr bytes.Buffer
			cmd := exec.CommandContext(ctx, ffmpeg, args...)
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("ffmpeg %v: %v\n%s", args, err, stderr.String())
			}
			var want []float64
			for _, at := range cues {
				if at >= tc.seek && at < tc.seek+60 {
					want = append(want, at)
				}
			}
			got := testSupPresentationTimes(stdout.Bytes())
			if !slices.Equal(got, want) {
				t.Fatalf("window at %gs held cues %v, want %v; args %v", tc.seek, got, want, args)
			}
		})
	}
}

// testSupStream builds a PGS elementary stream with one small display set per
// cue time (presentation, palette, object, end). With a decodeLead, every
// segment is decoded that many seconds before the cue and the palette, object
// and end segments are also stamped then, as in a Blu-ray transport stream.
func testSupStream(cues []float64, decodeLead float64) []byte {
	var out bytes.Buffer
	var dts uint32
	segment := func(kind byte, pts uint32, body []byte) {
		header := make([]byte, 13)
		header[0], header[1] = 'P', 'G'
		binary.BigEndian.PutUint32(header[2:], pts)
		binary.BigEndian.PutUint32(header[6:], dts)
		header[10] = kind
		binary.BigEndian.PutUint16(header[11:], uint16(len(body)))
		out.Write(header)
		out.Write(body)
	}
	for i, at := range cues {
		pts := uint32(at * 90000)
		if decodeLead > 0 {
			dts = uint32((at - decodeLead) * 90000)
		}
		segment(0x16, pts, []byte{
			0x07, 0x80, 0x04, 0x38, 0x10, // 1920x1080, frame rate
			byte(i >> 8), byte(i), 0x80, 0x00, 0x00, 0x01, // number, epoch start, palette 0, one object
			0x00, 0x00, 0x00, 0x00, 0x00, 0x64, 0x03, 0x84, // object 0, window 0, at 100,900
		})
		if decodeLead > 0 {
			pts = dts
		}
		segment(0x14, pts, []byte{0x00, 0x00, 0x01, 0xeb, 0x80, 0x80, 0xff})
		// Two lines of 1024 pixels in color 1.
		rle := []byte{0x00, 0xc4, 0x00, 0x01, 0x00, 0x00, 0x00, 0xc4, 0x00, 0x01, 0x00, 0x00}
		object := []byte{0x00, 0x00, 0x00, 0xc0, 0x00, 0x00, byte(len(rle) + 4)} // id 0, first and last
		object = binary.BigEndian.AppendUint16(object, 1024)
		object = binary.BigEndian.AppendUint16(object, 2)
		segment(0x15, pts, append(object, rle...))
		segment(0x80, pts, nil)
	}
	return out.Bytes()
}

// testSupPresentationTimes lists the presentation segments' times, in seconds.
func testSupPresentationTimes(data []byte) []float64 {
	var times []float64
	for offset := 0; offset+13 <= len(data); {
		size := int(binary.BigEndian.Uint16(data[offset+11:]))
		if data[offset+10] == 0x16 {
			times = append(times, float64(binary.BigEndian.Uint32(data[offset+2:]))/90000)
		}
		offset += 13 + size
	}
	return times
}

// A cache made from a Blu-ray transport stream keeps each segment's own
// timestamps, and a display set is decoded ahead of the time it shows. A
// window starting at or just before a cue must still read that whole set.
func TestStreamExtractArgs_CachedSupDecodedAheadRealFFmpeg(t *testing.T) {
	if testing.Short() {
		t.Skip("real FFmpeg integration test")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	var cues []float64
	for at := 40.0; at <= 400; at += 10 {
		cues = append(cues, at)
	}
	cached := filepath.Join(t.TempDir(), "track.sup")
	if err := os.WriteFile(cached, testSupStream(cues, 0.6), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, seek := range []float64{100, 99.7} {
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		args := streamExtractArgs(StreamExtractOpts{
			InputPath:           cached,
			SourceCodec:         "hdmv_pgs_subtitle",
			SeekSeconds:         seek,
			DurationSeconds:     60,
			AllowWindow:         true,
			InputIsExtractedSup: true,
		})
		var stdout, stderr bytes.Buffer
		cmd := exec.CommandContext(ctx, ffmpeg, args...)
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		cancel()
		if err != nil {
			t.Fatalf("ffmpeg %v: %v\n%s", args, err, stderr.String())
		}
		got := testSupPresentationTimes(stdout.Bytes())
		if len(got) == 0 || got[0] > 100 {
			t.Fatalf("window at %gs held cues %v, want them to start with the 100 s cue; args %v", seek, got, args)
		}
	}
}
