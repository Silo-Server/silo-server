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

// An ffmpeg without the dovi_rpu bitstream filter (pre-7.1) would abort on
// the unknown filter before producing any output. The profile must be
// neutralized so the remux still starts, keeping the pre-strip behavior.
func TestRemuxDVProfileFallsBackWithoutFilterSupport(t *testing.T) {
	if got := remuxDVProfile(7, false); got != 0 {
		t.Errorf("remuxDVProfile(7, false) = %d, want 0 (no strip without filter support)", got)
	}
	if got := remuxDVProfile(7, true); got != 7 {
		t.Errorf("remuxDVProfile(7, true) = %d, want 7", got)
	}
	for _, profile := range []int{0, 5, 8} {
		for _, canStrip := range []bool{false, true} {
			if got := remuxDVProfile(profile, canStrip); got != profile {
				t.Errorf("remuxDVProfile(%d, %t) = %d, want %d (pass through)", profile, canStrip, got, profile)
			}
		}
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
