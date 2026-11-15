package apiv2

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// A plan whose effective release differs from the requested row must publish
// both substitution fields on the v2 wire. The domain plan carried them, but the
// apiv2 projection rebuilt the plan field by field and dropped them, so across
// 35 production plan/replan decisions with 14 id changes the client saw
// substituted_from_file_id and substitution_reason exactly zero times. This pins
// the projection and the serialized body, not just the DTO.
func TestPlaybackDecisionV2ProjectsSubstitutionFields(t *testing.T) {
	in := playback.DecisionResponseV3{PlaybackPlan: &playback.PlanV3{
		RequestedMediaFileID:  613586,
		EffectiveMediaFileID:  1436768,
		SubstitutedFromFileID: 613586,
		SubstitutionReason:    "transport_failed",
	}}
	out := playbackDecision(in)
	if out.PlaybackPlan == nil {
		t.Fatal("projection dropped the plan")
	}
	encoded, err := json.Marshal(out.PlaybackPlan)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(encoded)
	if !strings.Contains(body, `"substituted_from_file_id":"613586"`) {
		t.Fatalf("plan body = %s, want substituted_from_file_id naming the requested row", body)
	}
	if !strings.Contains(body, `"substitution_reason":"transport_failed"`) {
		t.Fatalf("plan body = %s, want substitution_reason", body)
	}

	// An unsubstituted plan omits both fields entirely rather than publishing
	// "0" or an empty reason.
	plain := playbackDecision(playback.DecisionResponseV3{PlaybackPlan: &playback.PlanV3{
		RequestedMediaFileID: 613586,
		EffectiveMediaFileID: 613586,
	}})
	plainBody, err := json.Marshal(plain.PlaybackPlan)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(plainBody), "substituted_from_file_id") || strings.Contains(string(plainBody), "substitution_reason") {
		t.Fatalf("unsubstituted plan body = %s, want both fields omitted", plainBody)
	}
}
