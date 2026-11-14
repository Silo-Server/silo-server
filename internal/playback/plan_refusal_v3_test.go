package playback

import "testing"

// A burn-in requirement that only an SDR alternate can satisfy must still reach
// the alternate-version fallback. The planner reports that refusal in terms of
// the subtitle rather than the HDR pipeline, so a gate listing only the HDR and
// version reasons silently retires the fallback and refuses playback outright.
func TestAnotherVersionMayPlayCoversSubtitleForcedRefusals(t *testing.T) {
	for _, reason := range []string{
		TerminalNoAlternateVersionV3,
		TerminalHDRTranscodeUnsupportedV3,
		TerminalSubtitleConversionUnsupportedV3,
		// A damaged file says nothing about the item's other versions.
		TerminalSourceUnreadableV3,
	} {
		if !terminalPlannerResultV3(reason, "", false).AnotherVersionMayPlay() {
			t.Fatalf("terminal %q must allow an alternate-version retry", reason)
		}
	}
	if terminalPlannerResultV3("client_hls_unsupported", "", false).AnotherVersionMayPlay() {
		t.Fatal("a client-route refusal must not trigger an alternate-version retry")
	}
	if (PlannerResultV3{}).AnotherVersionMayPlay() {
		t.Fatal("a result without a terminal must not trigger an alternate-version retry")
	}
}
