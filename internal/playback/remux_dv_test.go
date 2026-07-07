package playback

import (
	"strings"
	"testing"
)

func argsContainPair(args []string, a, b string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == a && args[i+1] == b {
			return true
		}
	}
	return false
}

// Profile 7 remuxes drop the enhancement-layer track (-map 0:v:0 keeps only
// the base layer), which leaves dangling dual-layer RPU metadata on the BL.
// Stripping the RPUs yields a clean HDR10 stream — both a correctness fix and
// the Apple-parity fallback presentation for devices without a P7 decoder.
func TestBuildRemuxArgsStripsDolbyVisionRPUForProfile7(t *testing.T) {
	args := buildRemuxArgs("/x.mkv", "mp4", 0, false, -1, 7)
	if !argsContainPair(args, "-bsf:v", "dovi_rpu=strip=1") {
		t.Fatalf("profile 7 remux must strip DV RPUs, args=%v", strings.Join(args, " "))
	}
}

// Profile 8 base layers are self-contained: the RPU stays valid without an
// enhancement layer and DV-capable clients can render it. Never strip.
func TestBuildRemuxArgsKeepsRPUForProfile8AndPlainFiles(t *testing.T) {
	for _, profile := range []int{0, 5, 8} {
		args := buildRemuxArgs("/x.mkv", "mp4", 0, false, -1, profile)
		if argsContainPair(args, "-bsf:v", "dovi_rpu=strip=1") {
			t.Fatalf("profile %d remux must not strip DV RPUs, args=%v", profile, strings.Join(args, " "))
		}
	}
}
