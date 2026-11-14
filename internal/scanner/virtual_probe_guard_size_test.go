package scanner

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// A served-vs-declared size gap is an identity question, not a usability one.
// The provider and the storage layer routinely disagree on size in a normal
// deployment, so the bytes must stay usable (inventory, plan, playback) while
// the verdict withholds release identity.
func TestClassifyVirtualServedEvidenceSizeGapStaysUsable(t *testing.T) {
	const (
		declared = int64(4_269_999_999)
		served   = int64(3_743_905_729)
		runtime  = 102
	)
	tests := []struct {
		name          string
		probedSeconds int
		expectedMins  int
	}{
		{name: "duration proves the content", probedSeconds: 6_120, expectedMins: runtime},
		{name: "no measured duration", probedSeconds: 0, expectedMins: runtime},
		{name: "no catalog runtime", probedSeconds: 6_120, expectedMins: 0},
		{name: "neither duration known", probedSeconds: 0, expectedMins: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			verdict := ClassifyVirtualServedEvidence(declared, served, tc.probedSeconds, tc.expectedMins)
			if verdict != VirtualServedEvidenceUnverified {
				t.Fatalf("verdict = %v, want unverified for a size gap", verdict)
			}
			if !verdict.Usable() {
				t.Fatal("a size gap must stay usable for inventory, planning and playback")
			}
			if verdict.EstablishesIdentity() {
				t.Fatal("a size gap must not establish release identity")
			}
		})
	}

	// A size inside the evidence tolerance is admitted with no caveat.
	if verdict := ClassifyVirtualServedEvidence(declared, declared, 0, 0); verdict != VirtualServedEvidenceAdmitted {
		t.Fatalf("matching size verdict = %v, want admitted", verdict)
	}
}

// The real safeguard: a measured duration that contradicts the item runtime is
// positive evidence of a different file, regardless of how the sizes compare.
func TestClassifyVirtualServedEvidenceDurationContradictionRejects(t *testing.T) {
	const (
		declared = int64(8_449_999_999)
		served   = int64(7_427_636_945)
		runtime  = 39
		// 3072s against a 2340s expectation is +31%, the production
		// different-title shape.
		probedSeconds = 3_072
	)
	if verdict := ClassifyVirtualServedEvidence(declared, served, probedSeconds, runtime); verdict != VirtualServedEvidenceMismatch {
		t.Fatalf("implausible duration verdict = %v, want mismatch", verdict)
	}
	if verdict := ClassifyVirtualServedEvidence(declared, declared, probedSeconds, runtime); verdict != VirtualServedEvidenceMismatch {
		t.Fatalf("implausible duration with matching size verdict = %v, want mismatch", verdict)
	}
	if VirtualServedEvidenceRejected(declared, served, probedSeconds, runtime) != true {
		t.Fatal("a duration contradiction must still be reported as rejected")
	}
}

// Track layout is the second positive identity signal: a declaration that names
// video served with no video stream at all is a different file. Audio-only
// declarations and codec/container variants are not contradictions.
func TestClassifyVirtualServedEvidenceTrackLayoutContradictionRejects(t *testing.T) {
	declaredVideo := &models.MediaFile{Resolution: "2160p", CodecVideo: "hevc"}
	audioOnlyProbe := &models.MediaFile{
		AudioTracks:      []models.AudioTrack{{Codec: "aac", Channels: 2}},
		Duration:         6_120,
		ProbedFormatSize: 1_700_000_000,
	}
	if !VirtualTrackLayoutContradictsDeclaration(declaredVideo, audioOnlyProbe) {
		t.Fatal("a declared video release served audio-only is not flagged")
	}
	if verdict := ClassifyVirtualServedEvidenceForProbe("pin", declaredVideo.FileSize, declaredVideo, audioOnlyProbe, 102); verdict != VirtualServedEvidenceMismatch {
		t.Fatalf("verdict = %v, want mismatch for a track-layout contradiction", verdict)
	}

	// A normal variant of the same release is not a contradiction.
	normalProbe := &models.MediaFile{
		Resolution:       "2160p",
		VideoTracks:      []models.VideoTrack{{Codec: "av1", Width: 3840, Height: 2160}},
		AudioTracks:      []models.AudioTrack{{Codec: "opus", Channels: 6}},
		Duration:         6_120,
		ProbedFormatSize: 4_269_999_999,
	}
	if VirtualTrackLayoutContradictsDeclaration(declaredVideo, normalProbe) {
		t.Fatal("a re-encode of the declared release was flagged as a track-layout contradiction")
	}

	// An audio-only declaration cannot be contradicted by an audio-only probe.
	declaredAudio := &models.MediaFile{BaseType: "audiobook", CodecAudio: "aac"}
	if VirtualTrackLayoutContradictsDeclaration(declaredAudio, audioOnlyProbe) {
		t.Fatal("an audio-only declaration was judged as expecting video")
	}
}

// The unified gate must behave the same whether the bytes arrived from a fresh
// probe or a cache hit. A size gap is Unverified on both; a second serve that
// lands on the recorded observation is Admitted on both.
func TestClassifyVirtualServedEvidenceForProbeIsPathIndependent(t *testing.T) {
	ResetVirtualServedSizeSnapshots()
	t.Cleanup(ResetVirtualServedSizeSnapshots)

	const (
		declared = int64(4_269_999_999)
		served   = int64(3_743_905_729)
		runtime  = 102
	)
	pin := VirtualServedSizePin("virtual://movie/tt-path-independent?result=cand", 5)
	probed := &models.MediaFile{
		Resolution:       "2160p",
		VideoTracks:      []models.VideoTrack{{Codec: "hevc", Width: 3840, Height: 2160}},
		Duration:         6_120,
		ProbedFormatSize: served,
	}
	declaredFile := &models.MediaFile{Resolution: "2160p", CodecVideo: "hevc", FileSize: declared}

	// With no recorded observation, the read-only and recording paths agree on
	// the same bytes.
	readOnly := ClassifyVirtualServedEvidenceForPin(pin, declared, served, 6_120, runtime, VirtualServedSizeSubtitleToken(probed.SubtitleTracks))
	if readOnly != VirtualServedEvidenceUnverified {
		t.Fatalf("read-only verdict = %v, want unverified", readOnly)
	}
	first := ClassifyVirtualServedEvidenceForProbe(pin, declared, declaredFile, probed, runtime)
	if first != VirtualServedEvidenceUnverified {
		t.Fatalf("first-serve verdict = %v, want unverified", first)
	}
	// A later serve of the same bytes is the confirming observation on both
	// entry points.
	if readOnly := ClassifyVirtualServedEvidenceForPin(pin, declared, served, 6_120, runtime, VirtualServedSizeSubtitleToken(probed.SubtitleTracks)); readOnly != VirtualServedEvidenceAdmitted {
		t.Fatalf("read-only confirming verdict = %v, want admitted", readOnly)
	}
	second := ClassifyVirtualServedEvidenceForProbe(pin, declared, declaredFile, probed, runtime)
	if second != VirtualServedEvidenceAdmitted {
		t.Fatalf("confirming-serve verdict = %v, want admitted", second)
	}
}
