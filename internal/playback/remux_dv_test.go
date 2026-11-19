package playback

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

// Dual-layer Profile 7 cannot be preserved by a base-layer-only remux; the
// explicit preserve recipe must fail fast instead of emitting a stream with
// dangling RPUs and no enhancement layer.
func TestStartRemuxRejectsPreservedProfile7(t *testing.T) {
	if _, err := StartRemuxWithDVMode(t.Context(), "/nonexistent.mkv", "mp4", 0, false, -1, 7, RemuxDVPreserveV3, filepath.Join(t.TempDir(), "missing-ffmpeg")); err == nil || !strings.Contains(err.Error(), "Dolby Vision profile 7 cannot be preserved in a progressive remux") {
		t.Fatalf("preserve profile 7 error = %v, want the unsupported-profile rejection", err)
	}
}

// An unknown mode must fail for every profile, not only Profile 7 sources.
func TestStartRemuxRejectsUnknownModeForAllProfiles(t *testing.T) {
	for _, profile := range []int{0, 5, 7, 8} {
		if _, err := StartRemuxWithDVMode(t.Context(), "/nonexistent.mkv", "mp4", 0, false, -1, profile, RemuxDVMode("bogus"), filepath.Join(t.TempDir(), "missing-ffmpeg")); err == nil || !strings.Contains(err.Error(), "unknown remux Dolby Vision mode") {
			t.Fatalf("unknown remux DV mode error for profile %d = %v, want the invalid-mode rejection", profile, err)
		}
	}
}

// writeRemuxFFmpeg stands in for an ffmpeg that carries the dovi_rpu and
// filter_units filters and records the arguments of every non-probe
// invocation. When probeFails is set it cannot parse this source's RPU: it
// fails the probe the way the real one does (rejecting packets while exiting
// 0); otherwise the probe succeeds.
func writeRemuxFFmpeg(t *testing.T, probeFails bool) (bin, argLog string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "ffmpeg")
	argLog = filepath.Join(dir, "args")
	probe := "  *'-f null'*) exit 0;;\n"
	if probeFails {
		probe = "  *'-f null'*) echo '[dovi_rpu @ 0x55] Failed to read unit 1 (type 39).' >&2; exit 0;;\n"
	}
	script := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"  *-bsfs*) printf 'dovi_rpu\\nfilter_units\\n'; exit 0;;\n" +
		probe +
		"esac\n" +
		"echo \"$*\" >> " + argLog + "\n" +
		"exit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	return bin, argLog
}

func recordedRemuxArgs(t *testing.T, session *RemuxSession, argLog string) string {
	t.Helper()
	_, _ = io.ReadAll(session)
	_ = session.Close()
	recorded, err := os.ReadFile(argLog)
	if err != nil {
		t.Fatalf("the remux never ran: %v", err)
	}
	return string(recorded)
}

// The preserve mode, not the caller, decides the sample-entry tagging:
// buildRemuxArgs cannot be handed the flag directly by production code, so the
// mapping from RemuxDVPreserveV3 must be exercised through the mode switch.
func TestPreserveModeTagsTheSampleEntry(t *testing.T) {
	for _, profile := range []int{5, 8} {
		bin, argLog := writeRemuxFFmpeg(t, false)
		session, err := StartRemuxWithDVMode(t.Context(), remuxSourceFile(t), "mp4", 0, false, -1, profile, RemuxDVPreserveV3, bin)
		if err != nil {
			t.Fatalf("preserve remux refused profile %d: %v", profile, err)
		}
		recorded := recordedRemuxArgs(t, session, argLog)
		if !strings.Contains(recorded, "-tag:v dvh1") || !strings.Contains(recorded, "-strict unofficial") {
			t.Fatalf("preserve mode must map to dvh1 + -strict unofficial for profile %d: %s", profile, recorded)
		}
	}
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
	bin, argLog := writeRemuxFFmpeg(t, true)
	path := remuxSourceFile(t)

	session, err := StartRemuxWithDVMode(context.Background(), path, "mp4", 0, false, -1, 7, RemuxDVLegacyAutoV3, bin)
	if err != nil {
		t.Fatalf("legacy remux refused to start: %v", err)
	}
	// Drain to EOF so the stand-in has finished recording before it is killed.
	if recorded := recordedRemuxArgs(t, session, argLog); strings.Contains(recorded, DV7ToHDR10BitstreamFilter) {
		t.Fatalf("the hanging filter survived into the remux: %s", recorded)
	}
}

// The explicit v3 recipe has already promised the client HDR10. Reaching it
// with an unstrippable source means a session or stream token minted before
// the verdict was known, and neither honouring nor silently dropping the strip
// is right: fail so the request gets a definite error instead of a stalled
// stream, and so the next start re-plans onto a route that works.
func TestExplicitStripRecipeRefusesAnUnstrippableSource(t *testing.T) {
	bin, _ := writeRemuxFFmpeg(t, true)
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
