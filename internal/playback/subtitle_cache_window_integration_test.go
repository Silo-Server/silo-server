package playback

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
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
	if err := os.WriteFile(cached, testSupStream(cues), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name      string
		seek      float64
		wantFirst float64
		wantLast  float64
	}{
		// Each window is [seek, seek+60): the end is exclusive.
		{name: "from the start", seek: 0, wantFirst: 40, wantLast: 50},
		{name: "after a seek", seek: 95, wantFirst: 100, wantLast: 150},
		{name: "an extend window", seek: 355, wantFirst: 360, wantLast: 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			args := streamExtractArgs(StreamExtractOpts{
				InputPath:           cached,
				SourceCodec:         "hdmv_pgs_subtitle",
				SeekSeconds:         tc.seek,
				DurationSeconds:     60,
				AllowWindow:         true,
				InputIsExtractedSup: true,
			})
			var stdout, stderr bytes.Buffer
			cmd := exec.CommandContext(ctx, ffmpeg, args...)
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("ffmpeg %v: %v\n%s", args, err, stderr.String())
			}
			got := testSupPresentationTimes(stdout.Bytes())
			if len(got) == 0 {
				t.Fatalf("window at %gs came back empty; args %v", tc.seek, args)
			}
			if got[0] != tc.wantFirst || got[len(got)-1] != tc.wantLast {
				t.Fatalf("window at %gs held cues %v..%v, want %v..%v; args %v",
					tc.seek, got[0], got[len(got)-1], tc.wantFirst, tc.wantLast, args)
			}
		})
	}
}

// testSupStream builds a PGS elementary stream with one small display set per
// cue time (presentation, palette, object, end).
func testSupStream(cues []float64) []byte {
	var out bytes.Buffer
	segment := func(kind byte, pts uint32, body []byte) {
		header := make([]byte, 13)
		header[0], header[1] = 'P', 'G'
		binary.BigEndian.PutUint32(header[2:], pts)
		header[10] = kind
		binary.BigEndian.PutUint16(header[11:], uint16(len(body)))
		out.Write(header)
		out.Write(body)
	}
	for i, at := range cues {
		pts := uint32(at * 90000)
		segment(0x16, pts, []byte{
			0x07, 0x80, 0x04, 0x38, 0x10, // 1920x1080, frame rate
			byte(i >> 8), byte(i), 0x80, 0x00, 0x00, 0x01, // number, epoch start, palette 0, one object
			0x00, 0x00, 0x00, 0x00, 0x00, 0x64, 0x03, 0x84, // object 0, window 0, at 100,900
		})
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
