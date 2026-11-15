package keyframes

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// HEVCKeyframeIsIDR must tell an open-GOP encode's CRAs from its first IDR,
// and a closed-GOP encode's IDRs, from the blocks FFmpeg muxed, including
// access units that lead with a delimiter and repeated parameter sets.
func TestHEVCKeyframeIsIDRReadsTheFirstSlice(t *testing.T) {
	ffmpeg, _ := requireFFmpeg(t)
	encoders, err := exec.Command(ffmpeg, "-hide_banner", "-encoders").Output()
	if err != nil || !strings.Contains(string(encoders), "libx265") {
		t.Skip("libx265 is unavailable")
	}
	encode := func(name, x265Params string) string {
		path := filepath.Join(t.TempDir(), name)
		out, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
			"-f", "lavfi", "-i", "testsrc2=size=128x72:rate=24",
			"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "8",
			"-c:v", "libx265", "-preset", "ultrafast",
			"-x265-params", "log-level=error:keyint=48:min-keyint=48:scenecut=0:bframes=4:"+x265Params,
			"-c:a", "aac", path).CombinedOutput()
		if err != nil {
			t.Fatalf("ffmpeg: %v\n%s", err, out)
		}
		return path
	}
	for _, test := range []struct {
		name, x265Params string
		laterIDR         bool
	}{
		{name: "open GOP", x265Params: "open-gop=1", laterIDR: false},
		{name: "open GOP with delimiters and repeated headers", x265Params: "open-gop=1:aud=1:repeat-headers=1", laterIDR: false},
		{name: "closed GOP", x265Params: "open-gop=0", laterIDR: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			idx, err := Load(encode(strings.ReplaceAll(test.name, " ", "-")+".mkv", test.x265Params))
			if err != nil {
				t.Fatal(err)
			}
			if len(idx.Keyframes) < 3 {
				t.Fatalf("keyframes = %v, want at least 3", idx.Keyframes)
			}
			for k := range idx.Keyframes {
				idr, err := idx.HEVCKeyframeIsIDR(k)
				if err != nil {
					t.Fatalf("keyframe %d at %gs: %v", k, idx.Keyframes[k], err)
				}
				if want := k == 0 || test.laterIDR; idr != want {
					t.Fatalf("keyframe %d at %gs: IDR = %v, want %v", k, idx.Keyframes[k], idr, want)
				}
			}
		})
	}

	t.Run("H.264", func(t *testing.T) {
		idx, err := Load(makeTestMKV(t, ffmpeg))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := idx.HEVCKeyframeIsIDR(0); !errors.Is(err, ErrNoHEVCBlock) {
			t.Fatalf("err = %v, want ErrNoHEVCBlock", err)
		}
	})
}
