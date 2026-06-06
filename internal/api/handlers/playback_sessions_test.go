package handlers

import "testing"

func TestSessionComponentDecisionLabelsCopiedAudioDuringHLSAsRemux(t *testing.T) {
	videoDecision, audioDecision := sessionComponentDecision("transcode", false, "copy")

	if videoDecision != "remux" {
		t.Fatalf("videoDecision = %q, want remux", videoDecision)
	}
	if audioDecision != "remux" {
		t.Fatalf("audioDecision = %q, want remux", audioDecision)
	}
}
