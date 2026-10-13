package mediasample

import (
	"math"
	"strings"
	"testing"
)

func TestInputHeaderDisplayAspect(t *testing.T) {
	for _, rotation := range []string{"90.00", "-90.00", "270.00", "180.00", "0.00"} {
		p := &inputHeaderParser{}
		log := "Input #0, mov,mp4, from '/a.mp4':\n" +
			"  Duration: 00:00:06.00, start: 0.000000, bitrate: 8 kb/s\n" +
			"  Stream #0:0: Video: mjpeg, yuvj420p, 1000x1000 (attached pic)\n" +
			"  Stream #0:1[0x2](und): Video: h264, yuv420p, 720x480 [SAR 32:27 DAR 16:9]\n" +
			"    Side data:\n      displaymatrix: rotation of " + rotation + " degrees\n" +
			"  Stream #0:2: Audio: aac\n" +
			"    displaymatrix: rotation of 1 degrees\n" +
			"Stream mapping:\nOutput #0, null, to 'pipe:':\n" +
			"  Stream #0:0: Video: h264, yuv420p, 100x100 [SAR 1:1 DAR 1:1]\n"
		for line := range strings.SplitSeq(log, "\n") {
			p.line(line)
		}
		want := 9.0 / 16
		if rotation == "180.00" || rotation == "0.00" {
			want = 16.0 / 9
		}
		if got := p.info.displayAspect(); math.Abs(got-want) > 1e-6 {
			t.Fatalf("rotation %s: display aspect %v, want %v", rotation, got, want)
		}
	}
}
