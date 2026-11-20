package scanner

import (
	"context"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

const (
	snapshotDeclaredBytes = int64(2_040_000_000) // provider's rounded declaration
	snapshotObservedBytes = int64(1_780_000_000) // what ffprobe actually read (~13% off)
	snapshotRuntimeMin    = 102
	snapshotDurationSec   = 6_120
)

func snapshotSubtitleTracks(codec string) []models.SubtitleTrack {
	return []models.SubtitleTrack{{Codec: codec, Language: "eng"}}
}

// A rounded provider declaration is the production shape: the served bytes are
// ~13% short of the declared size, so the 5% gate can only excuse them when the
// measured duration proves the content. Evidence that leans on the duration must
// record the observed size for the pin, because the declared size will never
// clear the gate on its own — but recording is not admission. One serve cannot
// tell the release from a wrong file of the same runtime, so the recording serve
// stays unverified and a later serve of the same bytes is what admits the pin.
func TestVirtualServedSizeSnapshotRecordedOnDurationProvenAcceptance(t *testing.T) {
	ResetVirtualServedSizeSnapshots()
	t.Cleanup(ResetVirtualServedSizeSnapshots)

	pin := VirtualServedSizePin("virtual://movie/tt-snap?result=rounded", 7)
	if pin == "" {
		t.Fatal("a concrete candidate must have a pin")
	}
	token := VirtualServedSizeSubtitleToken(snapshotSubtitleTracks("subrip"))

	// Without a snapshot the evidence is playable but unverified: the size
	// contradicts the declaration and only the duration argues for identity.
	verdict := ClassifyVirtualServedEvidenceForPin(pin, snapshotDeclaredBytes, snapshotObservedBytes, snapshotDurationSec, snapshotRuntimeMin, token)
	if verdict != VirtualServedEvidenceUnverified {
		t.Fatalf("verdict before acceptance = %v, want unverified", verdict)
	}

	// The first duration-proven serve records the observation and still does
	// not establish identity.
	verdict = ClassifyVirtualServedEvidenceForPinWithSnapshot(pin, snapshotDeclaredBytes, snapshotObservedBytes, snapshotDurationSec, snapshotRuntimeMin, token)
	if verdict != VirtualServedEvidenceUnverified {
		t.Fatalf("verdict on the recording serve = %v, want unverified (one observation is not identity)", verdict)
	}
	snapshot, ok := VirtualServedSizeSnapshotFor(pin, snapshotDeclaredBytes, token)
	if !ok {
		t.Fatal("duration-proven acceptance recorded no snapshot")
	}
	if snapshot.ObservedBytes != snapshotObservedBytes {
		t.Fatalf("snapshot observed = %d, want %d", snapshot.ObservedBytes, snapshotObservedBytes)
	}
	if snapshot.DeclaredBytes != snapshotDeclaredBytes {
		t.Fatalf("snapshot declared = %d, want the provider declaration %d", snapshot.DeclaredBytes, snapshotDeclaredBytes)
	}

	// A second serve of the recorded bytes confirms the pin, and needs no new
	// duration proof: the observation already carries it.
	verdict = ClassifyVirtualServedEvidenceForPinWithSnapshot(pin, snapshotDeclaredBytes, snapshotObservedBytes, 0, 0, token)
	if verdict != VirtualServedEvidenceAdmitted {
		t.Fatalf("verdict on the confirming serve = %v, want admitted", verdict)
	}
}

// The confirmed observation is the baseline future comparisons for the same pin
// use, so a re-probe of the same bytes is admitted instead of being re-probed
// forever, while a genuinely different size under the pin still has to prove
// identity by duration.
func TestVirtualServedSizeSnapshotDrivesLaterEvidenceComparisons(t *testing.T) {
	ResetVirtualServedSizeSnapshots()
	t.Cleanup(ResetVirtualServedSizeSnapshots)

	pin := VirtualServedSizePin("virtual://movie/tt-snap-later?result=rounded", 7)
	token := VirtualServedSizeSubtitleToken(snapshotSubtitleTracks("subrip"))
	if verdict := ClassifyVirtualServedEvidenceForPinWithSnapshot(pin, snapshotDeclaredBytes, snapshotObservedBytes, snapshotDurationSec, snapshotRuntimeMin, token); verdict != VirtualServedEvidenceUnverified {
		t.Fatalf("recording serve verdict = %v, want unverified", verdict)
	}
	if verdict := ClassifyVirtualServedEvidenceForPinWithSnapshot(pin, snapshotDeclaredBytes, snapshotObservedBytes, snapshotDurationSec, snapshotRuntimeMin, token); verdict != VirtualServedEvidenceAdmitted {
		t.Fatalf("confirming serve verdict = %v, want admitted", verdict)
	}

	// A later probe reads the same release: the baseline is now the observed
	// size, so a few megabytes of drift stays inside the 5% window.
	later := snapshotObservedBytes + 20_000_000
	if verdict := ClassifyVirtualServedEvidenceForPin(pin, snapshotDeclaredBytes, later, 0, 0, token); verdict != VirtualServedEvidenceAdmitted {
		t.Fatalf("later probe verdict = %v, want admitted against the observed baseline", verdict)
	}
	// A size the declared baseline would have accepted is a different posting
	// against the observed baseline. Size alone is not an identity verdict, so
	// the bytes stay usable but unverified; only a duration contradiction
	// rejects them.
	declaredBaselineOnly := snapshotDeclaredBytes
	if verdict := ClassifyVirtualServedEvidenceForPin(pin, snapshotDeclaredBytes, declaredBaselineOnly, 0, 0, token); verdict != VirtualServedEvidenceUnverified {
		t.Fatalf("different posting verdict = %v, want unverified (size alone is not identity)", verdict)
	}
}

// A duration-plausible wrong serve must not become identity by repetition: a
// second serve with different bytes stays unverified, and the observation the
// pin was proven to serve is what still confirms it. This is the failure the
// two-observation rule exists for — one plausible-looking wrong serve (a
// different release of the same runtime) would otherwise be the pin's baseline
// forever.
func TestVirtualServedSizeSnapshotDivergentServeKeepsPinUnverified(t *testing.T) {
	ResetVirtualServedSizeSnapshots()
	t.Cleanup(ResetVirtualServedSizeSnapshots)

	pin := VirtualServedSizePin("virtual://movie/tt-snap-divergent?result=wrong", 7)
	token := VirtualServedSizeSubtitleToken(snapshotSubtitleTracks("subrip"))
	if verdict := ClassifyVirtualServedEvidenceForPinWithSnapshot(pin, snapshotDeclaredBytes, snapshotObservedBytes, snapshotDurationSec, snapshotRuntimeMin, token); verdict != VirtualServedEvidenceUnverified {
		t.Fatalf("recording serve verdict = %v, want unverified", verdict)
	}

	// Different bytes, plausible duration, outside the 5% window on both the
	// declaration and the recorded observation.
	divergent := snapshotObservedBytes + 120_000_000
	if verdict := ClassifyVirtualServedEvidenceForPinWithSnapshot(pin, snapshotDeclaredBytes, divergent, snapshotDurationSec, snapshotRuntimeMin, token); verdict != VirtualServedEvidenceUnverified {
		t.Fatalf("divergent serve verdict = %v, want unverified (never admitted, never cached)", verdict)
	}
	snapshot, ok := VirtualServedSizeSnapshotFor(pin, snapshotDeclaredBytes, token)
	if !ok {
		t.Fatal("a divergent serve discarded the pin's recorded observation")
	}
	if snapshot.ObservedBytes != snapshotObservedBytes {
		t.Fatalf("snapshot observed = %d, want the original observation %d", snapshot.ObservedBytes, snapshotObservedBytes)
	}

	// The bytes the pin was first proven to serve still confirm it after the
	// divergent serve.
	if verdict := ClassifyVirtualServedEvidenceForPinWithSnapshot(pin, snapshotDeclaredBytes, snapshotObservedBytes, 0, 0, token); verdict != VirtualServedEvidenceAdmitted {
		t.Fatalf("confirming serve after divergence verdict = %v, want admitted", verdict)
	}

	// A divergent serve without a duration proof is a different release, not
	// merely unverified evidence.
	if verdict := ClassifyVirtualServedEvidenceForPinWithSnapshot(pin, snapshotDeclaredBytes, divergent, 600, snapshotRuntimeMin, token); verdict != VirtualServedEvidenceMismatch {
		t.Fatalf("unproven divergent serve verdict = %v, want mismatch", verdict)
	}
}

// A subtitle-layout change means the bytes the pin's ordinals address changed,
// so the observed size recorded for the old layout must stop standing in for the
// declaration.
func TestVirtualServedSizeSnapshotInvalidatedOnLayoutChange(t *testing.T) {
	ResetVirtualServedSizeSnapshots()
	t.Cleanup(ResetVirtualServedSizeSnapshots)

	pin := VirtualServedSizePin("virtual://movie/tt-snap-layout?result=rounded", 7)
	before := VirtualServedSizeSubtitleToken(snapshotSubtitleTracks("subrip"))
	if verdict := ClassifyVirtualServedEvidenceForPinWithSnapshot(pin, snapshotDeclaredBytes, snapshotObservedBytes, snapshotDurationSec, snapshotRuntimeMin, before); verdict != VirtualServedEvidenceUnverified {
		t.Fatalf("recording serve verdict = %v, want unverified", verdict)
	}
	if _, ok := VirtualServedSizeSnapshotFor(pin, snapshotDeclaredBytes, before); !ok {
		t.Fatal("the recording serve left no observation")
	}

	after := VirtualServedSizeSubtitleToken([]models.SubtitleTrack{{Codec: "ass", Language: "eng"}})
	if _, ok := VirtualServedSizeSnapshotFor(pin, snapshotDeclaredBytes, after); ok {
		t.Fatal("snapshot survived a subtitle-layout change")
	}
	if verdict := ClassifyVirtualServedEvidenceForPin(pin, snapshotDeclaredBytes, snapshotObservedBytes, snapshotDurationSec, snapshotRuntimeMin, after); verdict != VirtualServedEvidenceUnverified {
		t.Fatalf("verdict after layout change = %v, want unverified (declared baseline again)", verdict)
	}
}

// The snapshot records what the pin's bytes measured; it must never replace the
// release's declared size, which backs the AltMount-completion and release
// identity comparisons.
func TestVirtualServedSizeSnapshotPreservesDeclaredSize(t *testing.T) {
	ResetVirtualServedSizeSnapshots()
	t.Cleanup(ResetVirtualServedSizeSnapshots)

	pin := VirtualServedSizePin("virtual://movie/tt-snap-declared?result=rounded", 7)
	token := VirtualServedSizeSubtitleToken(snapshotSubtitleTracks("subrip"))
	row := &models.MediaFile{FileSize: snapshotDeclaredBytes}

	if ok := RecordVirtualServedSizeSnapshotIfProven(pin, row.FileSize, snapshotObservedBytes, snapshotDurationSec, snapshotRuntimeMin, token); !ok {
		t.Fatal("duration-proven acceptance recorded no snapshot")
	}
	if row.FileSize != snapshotDeclaredBytes {
		t.Fatalf("row declared size = %d, want the untouched declaration %d", row.FileSize, snapshotDeclaredBytes)
	}
	if !VirtualServedSizeMismatch(row.FileSize, snapshotObservedBytes) {
		t.Fatal("declared-vs-served mismatch disappeared: the snapshot leaked into the declared size")
	}
	// A re-declaration invalidates the snapshot rather than silently comparing
	// the new listing against the old observation.
	if _, ok := VirtualServedSizeSnapshotFor(pin, snapshotDeclaredBytes+1, token); ok {
		t.Fatal("snapshot survived a declared-size change")
	}
}

// Unproven evidence must never teach the gate: a size gap with an implausible
// duration is a different release, and a size inside the tolerance needs no
// snapshot at all.
func TestVirtualServedSizeSnapshotNeverRecordedWithoutDurationProof(t *testing.T) {
	ResetVirtualServedSizeSnapshots()
	t.Cleanup(ResetVirtualServedSizeSnapshots)

	pin := VirtualServedSizePin("virtual://movie/tt-snap-unproven?result=wrong", 7)
	token := VirtualServedSizeSubtitleToken(snapshotSubtitleTracks("subrip"))

	tests := []struct {
		name          string
		declared      int64
		served        int64
		probedSeconds int
		expectedMins  int
	}{
		{name: "duration does not prove identity", declared: snapshotDeclaredBytes, served: snapshotObservedBytes, probedSeconds: 600, expectedMins: 102},
		{name: "no measured duration", declared: snapshotDeclaredBytes, served: snapshotObservedBytes, probedSeconds: 0, expectedMins: 102},
		{name: "no expected runtime", declared: snapshotDeclaredBytes, served: snapshotObservedBytes, probedSeconds: 6_120, expectedMins: 0},
		{name: "size inside tolerance", declared: snapshotDeclaredBytes, served: snapshotDeclaredBytes - 20_000_000, probedSeconds: 6_120, expectedMins: 102},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if ok := RecordVirtualServedSizeSnapshotIfProven(pin, tc.declared, tc.served, tc.probedSeconds, tc.expectedMins, token); ok {
				t.Fatal("unproven evidence recorded a snapshot")
			}
			if _, ok := VirtualServedSizeSnapshotFor(pin, tc.declared, token); ok {
				t.Fatal("unproven evidence left a snapshot behind")
			}
		})
	}

	// A rejected probe is returned as a mismatch and records nothing even
	// through the acceptance entry point.
	if verdict := ClassifyVirtualServedEvidenceForPinWithSnapshot(pin, snapshotDeclaredBytes, snapshotObservedBytes, 600, snapshotRuntimeMin, token); verdict != VirtualServedEvidenceMismatch {
		t.Fatalf("verdict = %v, want mismatch", verdict)
	}
	if _, ok := VirtualServedSizeSnapshotFor(pin, snapshotDeclaredBytes, token); ok {
		t.Fatal("a rejected probe recorded a snapshot")
	}
}

// A gap far beyond a rounded listing is not excused by a matching duration.
// Production admitted 17.7 GB served bytes for a 3.78 GB declaration (4.7x) on
// duration proof alone: two identical serves of such an implausible file then
// confirmed it, because the recorded observation became the comparison baseline.
// The plausibility ratio stops the observation from being recorded at all, so no
// repeat serve can establish identity, while the bytes stay playable (Unverified)
// for the request that measured them.
func TestVirtualServedSizeSnapshotRefusesImplausibleGap(t *testing.T) {
	ResetVirtualServedSizeSnapshots()
	t.Cleanup(ResetVirtualServedSizeSnapshots)

	const (
		declared = int64(3_780_000_000)
		served   = int64(17_700_000_000) // 4.7x the declaration
		runtime  = 102
		duration = 6_120
	)
	if !VirtualServedSizeImplausible(declared, served) {
		t.Fatalf("served %d vs declared %d is not classed implausible", served, declared)
	}
	pin := VirtualServedSizePin("virtual://movie/tt-snap-implausible?result=oversized", 7)
	token := VirtualServedSizeSubtitleToken(snapshotSubtitleTracks("subrip"))

	if ok := RecordVirtualServedSizeSnapshotIfProven(pin, declared, served, duration, runtime, token); ok {
		t.Fatal("a 4.7x gap recorded a served-size observation")
	}
	if _, ok := VirtualServedSizeSnapshotFor(pin, declared, token); ok {
		t.Fatal("a 4.7x gap left a snapshot behind")
	}

	// Every entry point stays unverified (playable) and never admitted, even on
	// a second serve of the identical bytes.
	for i := 0; i < 2; i++ {
		if verdict := ClassifyVirtualServedEvidenceForPinWithSnapshot(pin, declared, served, duration, runtime, token); verdict != VirtualServedEvidenceUnverified {
			t.Fatalf("serve %d verdict = %v, want unverified", i+1, verdict)
		}
		if verdict := ClassifyVirtualServedEvidenceForPin(pin, declared, served, duration, runtime, token); verdict != VirtualServedEvidenceUnverified {
			t.Fatalf("read-only serve %d verdict = %v, want unverified", i+1, verdict)
		}
	}
	if _, ok := VirtualServedSizeSnapshotFor(pin, declared, token); ok {
		t.Fatal("repeated implausible serves recorded a snapshot")
	}

	// Without a measured duration the same gap is still usable: size alone
	// never rejects, and only identity stays unproven.
	if verdict := ClassifyVirtualServedEvidenceForPinWithSnapshot(pin, declared, served, 0, runtime, token); verdict != VirtualServedEvidenceUnverified {
		t.Fatalf("implausible serve without a duration verdict = %v, want unverified", verdict)
	}
}

// A gap inside the plausibility ratio keeps the existing two-observation rule:
// the first duration-proven serve records the observation and stays unverified,
// and a second serve of the same size admits the pin. The boundary is far above
// the 12–19% rounding band, so ordinary rounded listings are unaffected.
func TestVirtualServedSizeSnapshotConfirmsWithinPlausibilityRatio(t *testing.T) {
	ResetVirtualServedSizeSnapshots()
	t.Cleanup(ResetVirtualServedSizeSnapshots)

	const (
		declared = int64(4_000_000_000)
		served   = int64(7_000_000_000) // 1.75x, inside the 2x cap
		runtime  = 102
		duration = 6_120
	)
	if VirtualServedSizeImplausible(declared, served) {
		t.Fatalf("served %d vs declared %d must stay inside the plausibility ratio", served, declared)
	}
	pin := VirtualServedSizePin("virtual://movie/tt-snap-within-ratio?result=large", 7)
	token := VirtualServedSizeSubtitleToken(snapshotSubtitleTracks("subrip"))

	if verdict := ClassifyVirtualServedEvidenceForPinWithSnapshot(pin, declared, served, duration, runtime, token); verdict != VirtualServedEvidenceUnverified {
		t.Fatalf("recording serve verdict = %v, want unverified", verdict)
	}
	if _, ok := VirtualServedSizeSnapshotFor(pin, declared, token); !ok {
		t.Fatal("an in-ratio gap recorded no observation")
	}
	if verdict := ClassifyVirtualServedEvidenceForPinWithSnapshot(pin, declared, served, duration, runtime, token); verdict != VirtualServedEvidenceAdmitted {
		t.Fatalf("confirming serve verdict = %v, want admitted", verdict)
	}
}

// A release-level path carries no concrete ?result= candidate, so no observed
// size can be attributed to one pin's bytes.
func TestVirtualServedSizePinRequiresConcreteCandidate(t *testing.T) {
	ResetVirtualServedSizeSnapshots()
	t.Cleanup(ResetVirtualServedSizeSnapshots)

	if pin := VirtualServedSizePin("virtual://movie/tt-neutral", 7); pin != "" {
		t.Fatalf("neutral path pin = %q, want empty", pin)
	}
	pin := VirtualServedSizePin("virtual://movie/tt-neutral?profile=4k&result=abc", 7)
	want := VirtualServedSizePin("virtual://movie/tt-neutral?profile=4k&result=abc", 7)
	if pin != want || pin == "" {
		t.Fatalf("concrete pin = %q, want stable %q", pin, want)
	}
	if other := VirtualServedSizePin("virtual://movie/tt-neutral?profile=4k&result=abc", 8); other == pin {
		t.Fatal("pins from different owners must not collide")
	}
}

// The probe cache is the recurring 5% comparison: a rounded declaration whose
// duration-proven observation a later probe confirms must cache (so the row is
// not re-probed on every start), and the cached entry must remain the evidence
// the row receives. The recording probe itself stays uncached — one observation
// is not identity — so a pin costs exactly one extra probe, once.
func TestVirtualProbeCacheCachesDurationProvenRoundedDeclaration(t *testing.T) {
	ResetVirtualServedSizeSnapshots()
	t.Cleanup(ResetVirtualServedSizeSnapshots)

	cache := NewVirtualProbeCache(time.Minute, 4)
	row := &models.MediaFile{
		ID: 7, FilePath: "virtual://movie/tt-cache-rounded?result=rounded",
		FileSize:                      snapshotDeclaredBytes,
		VirtualExpectedRuntimeMinutes: snapshotRuntimeMin,
	}
	var calls int
	probe := func(_ context.Context, _ string, input *models.MediaFile) (*models.MediaFile, error) {
		calls++
		result := *input
		result.ProbedFormatSize = snapshotObservedBytes
		result.Duration = snapshotDurationSec
		result.SubtitleTracks = snapshotSubtitleTracks("subrip")
		return &result, nil
	}
	first, err := cache.Probe(context.Background(), "https://provider.example/video.mkv?token=one", row, probe)
	if err != nil {
		t.Fatal(err)
	}
	if first == nil || first.ProbedFormatSize != snapshotObservedBytes {
		t.Fatalf("first probe = %+v, want the observed bytes", first)
	}
	if calls != 1 {
		t.Fatalf("probe calls = %d, want 1", calls)
	}
	// The recording probe must not have cached: its evidence is unverified.
	if cached := cache.Lookup("https://provider.example/video.mkv?token=two", row); cached != nil {
		t.Fatalf("an unconfirmed observation was cached: %+v", cached)
	}
	second, err := cache.Probe(context.Background(), "https://provider.example/video.mkv?token=two", row, probe)
	if err != nil {
		t.Fatal(err)
	}
	if second == nil || second.ProbedFormatSize != snapshotObservedBytes {
		t.Fatalf("second probe = %+v, want the observed bytes", second)
	}
	if calls != 2 {
		t.Fatalf("probe calls = %d, want 2: the confirming probe must reach the provider", calls)
	}
	third, err := cache.Probe(context.Background(), "https://provider.example/video.mkv?token=three", row, probe)
	if err != nil {
		t.Fatal(err)
	}
	if third == nil || third.ProbedFormatSize != snapshotObservedBytes {
		t.Fatalf("third probe = %+v, want the cached observed bytes", third)
	}
	if calls != 2 {
		t.Fatalf("probe calls = %d, want 2: a confirmed observation must cache", calls)
	}

	// The cached inventory keeps the subtitle layout the observation was keyed
	// on, so warming consumers see the same token.
	if got, want := playback.SubtitleInventoryToken(third.SubtitleTracks), playback.SubtitleInventoryToken(snapshotSubtitleTracks("subrip")); got != want {
		t.Fatalf("cached subtitle token = %q, want %q", got, want)
	}
}

// The cache must never make a wrong file authoritative: a second probe that
// serves different bytes against a recorded observation stays uncached, so the
// row keeps being probed until the provider serves the bytes the pin was proven
// on.
func TestVirtualProbeCacheRejectsDivergentSecondServe(t *testing.T) {
	ResetVirtualServedSizeSnapshots()
	t.Cleanup(ResetVirtualServedSizeSnapshots)

	cache := NewVirtualProbeCache(time.Minute, 4)
	row := &models.MediaFile{
		ID: 8, FilePath: "virtual://movie/tt-cache-divergent?result=wrong",
		FileSize:                      snapshotDeclaredBytes,
		VirtualExpectedRuntimeMinutes: snapshotRuntimeMin,
	}
	var calls int
	served := snapshotObservedBytes
	probe := func(_ context.Context, _ string, input *models.MediaFile) (*models.MediaFile, error) {
		calls++
		result := *input
		result.ProbedFormatSize = served
		result.Duration = snapshotDurationSec
		result.SubtitleTracks = snapshotSubtitleTracks("subrip")
		return &result, nil
	}
	if _, err := cache.Probe(context.Background(), "https://provider.example/video.mkv?token=one", row, probe); err != nil {
		t.Fatal(err)
	}
	served = snapshotObservedBytes + 120_000_000
	if _, err := cache.Probe(context.Background(), "https://provider.example/video.mkv?token=two", row, probe); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("probe calls = %d, want 2", calls)
	}
	if cached := cache.Lookup("https://provider.example/video.mkv?token=two", row); cached != nil {
		t.Fatalf("a divergent second serve was cached: %+v", cached)
	}
}
