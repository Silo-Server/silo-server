package playback

import (
	"context"
	"io"
	"os"
	"path/filepath"
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
	args := buildRemuxArgs("/x.mkv", "mp4", 0, false, -1, 7, false, false)
	if !argsContainPair(args, "-bsf:v", "dovi_rpu=strip=1") {
		t.Fatalf("profile 7 remux must strip DV RPUs from the base layer, args=%v", strings.Join(args, " "))
	}
}

func TestBuildRemuxArgsExcludesAttachedPictures(t *testing.T) {
	args := buildRemuxArgs("/book.m4b", "mp4", 0, true, -1, 0, false, true)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-map 0:V:0?") {
		t.Fatalf("remux args must exclude attached-picture video streams: %s", joined)
	}
}

func TestBuildRemuxArgsHonorsPlannedAACOutput(t *testing.T) {
	args := buildRemuxArgsWithAudioV3("/book.m4b", "mp4", 0, true, -1, 0, false, true, 1, 96)
	if !argsContainPair(args, "-ac", "1") || !argsContainPair(args, "-b:a", "96k") {
		t.Fatalf("planned mono bitrate missing from remux args: %s", strings.Join(args, " "))
	}
}

func TestBuildRemuxArgsRequiresVideoForVideoPlans(t *testing.T) {
	args := buildRemuxArgs("/movie.mkv", "mp4", 0, false, -1, 0, false, false)
	if !argsContainPair(args, "-map", "0:V:0") || argsContainPair(args, "-map", "0:V:0?") {
		t.Fatalf("video remux must use a mandatory video map, args=%s", strings.Join(args, " "))
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
		args := buildRemuxArgs("/x.mkv", "mp4", 0, false, -1, profile, false, false)
		if argsContainPair(args, "-bsf:v", "dovi_rpu=strip=1") {
			t.Fatalf("profile %d remux must not strip DV RPUs, args=%v", profile, strings.Join(args, " "))
		}
	}
}

// The DV sample entry is an explicit opt-in for the v3 preserve recipe:
// Media3 keys decoder selection from it, but legacy web/jellycompat consumers
// rely on the pre-v3 hev1 labeling their demuxers accept.
func TestBuildRemuxArgsTagsPreservedDolbyVisionOnlyWhenRequested(t *testing.T) {
	args := buildRemuxArgs("/x.mkv", "mp4", 0, false, -1, 8, true, false)
	if !argsContainPair(args, "-tag:v", "dvhe") {
		t.Fatalf("preserved Dolby Vision must retain a DV sample entry, args=%v", strings.Join(args, " "))
	}
	legacy := buildRemuxArgs("/x.mkv", "mp4", 0, false, -1, 8, false, false)
	if argsContainPair(legacy, "-tag:v", "dvhe") {
		t.Fatalf("legacy remux consumers must keep hev1 labeling, args=%v", strings.Join(legacy, " "))
	}
	stripped := buildRemuxArgs("/x.mkv", "mp4", 0, false, -1, 7, false, false)
	if argsContainPair(stripped, "-tag:v", "dvhe") {
		t.Fatalf("HDR10 fallback must not retain a DV sample entry, args=%v", strings.Join(stripped, " "))
	}
}

// Dual-layer Profile 7 cannot be preserved by a base-layer-only remux; the
// explicit preserve recipe must fail fast instead of emitting a stream with
// dangling RPUs and no enhancement layer.
func TestStartRemuxRejectsPreservedProfile7(t *testing.T) {
	if _, err := StartRemuxWithDVMode(t.Context(), "/nonexistent.mkv", "mp4", 0, false, -1, 7, RemuxDVPreserveV3, ""); err == nil {
		t.Fatal("preserve mode accepted a profile 7 source")
	}
}

// An unknown mode must fail for every profile, not only Profile 7 sources.
func TestStartRemuxRejectsUnknownModeForAllProfiles(t *testing.T) {
	for _, profile := range []int{0, 5, 7, 8} {
		if _, err := StartRemuxWithDVMode(t.Context(), "/nonexistent.mkv", "mp4", 0, false, -1, profile, RemuxDVMode("bogus"), ""); err == nil {
			t.Fatalf("unknown remux DV mode accepted for profile %d", profile)
		}
	}
}

func TestBuildRemuxArgsDelaysMoovForCopiedAtmosConfiguration(t *testing.T) {
	args := buildRemuxArgs("/x.mkv", "mp4", 0, false, -1, 8, false, false)
	if !argsContainPair(args, "-movflags", "frag_keyframe+delay_moov+default_base_moof") {
		t.Fatalf("remux must delay moov until copied audio is parsed, args=%v", strings.Join(args, " "))
	}
}

// writeProbeAwareFFmpeg stands in for an ffmpeg that carries the dovi_rpu
// filter but cannot parse this source's RPU: it advertises the filter, fails
// the probe the way the real one does (rejecting packets while exiting 0), and
// records the arguments of every non-probe invocation.
func writeProbeAwareFFmpeg(t *testing.T) (bin, argLog string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "ffmpeg")
	argLog = filepath.Join(dir, "args")
	script := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"  *-bsfs*) echo dovi_rpu; exit 0;;\n" +
		"  *'-f null'*) echo '[dovi_rpu @ 0x55] Failed to read unit 1 (type 39).' >&2; exit 0;;\n" +
		"esac\n" +
		"echo \"$*\" >> " + argLog + "\n" +
		"exit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	return bin, argLog
}

func remuxSourceFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(path, []byte("not really a movie"), 0o600); err != nil {
		t.Fatalf("write remux fixture: %v", err)
	}
	return path
}

// The legacy/auto mode derives the strip from the profile alone, with no plan
// to consult — the web player, jellycompat and pre-v3 stream tokens all arrive
// here. An unparseable RPU has to neutralize the profile the same way a missing
// dovi_rpu filter does, or the filter rejects every packet and the response
// never produces a byte.
func TestLegacyRemuxDropsTheStripForAnUnstrippableSource(t *testing.T) {
	bin, argLog := writeProbeAwareFFmpeg(t)
	path := remuxSourceFile(t)

	session, err := StartRemuxWithDVMode(context.Background(), path, "mp4", 0, false, -1, 7, RemuxDVLegacyAutoV3, bin)
	if err != nil {
		t.Fatalf("legacy remux refused to start: %v", err)
	}
	// Drain to EOF so the stand-in has finished recording before it is killed.
	_, _ = io.ReadAll(session)
	session.Close()

	recorded, err := os.ReadFile(argLog)
	if err != nil {
		t.Fatalf("the remux never ran: %v", err)
	}
	if strings.Contains(string(recorded), DV7ToHDR10BitstreamFilter) {
		t.Fatalf("the hanging filter survived into the remux: %s", recorded)
	}
}

// The explicit v3 recipe has already promised the client HDR10. Reaching it
// with an unstrippable source means a session or stream token minted before
// the verdict was known, and neither honouring nor silently dropping the strip
// is right: fail so the request gets a definite error instead of a stalled
// stream, and so the next start re-plans onto a route that works.
func TestExplicitStripRecipeRefusesAnUnstrippableSource(t *testing.T) {
	bin, _ := writeProbeAwareFFmpeg(t)
	path := remuxSourceFile(t)

	session, err := StartRemuxWithDVMode(context.Background(), path, "mp4", 0, false, -1, 7, RemuxDVStripToHDR10V3, bin)
	if err == nil {
		session.Close()
		t.Fatal("the explicit HDR10 strip accepted a source it cannot strip")
	}
	if !strings.Contains(err.Error(), "cannot be stripped") {
		t.Fatalf("error = %v, want it to name the unstrippable source", err)
	}
}
