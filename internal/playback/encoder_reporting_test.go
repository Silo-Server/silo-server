package playback

import (
	"encoding/json"
	"testing"

	"github.com/Silo-Server/silo-server/internal/tonemap"
)

func TestHEVCEncoderReportingPreservesGPURecipeAcrossReconstruction(t *testing.T) {
	opts := TranscodeOpts{
		SessionID: "hevc-report", HWAccel: transcodeHWVAAPI, TargetCodecVideo: transcodeCodecHEVC,
		ToneMapMode: tonemap.ModeHardware, ToneMapFilter: tonemap.HardwareFilterVAAPI, softwareEncode: true,
	}
	if opts.EffectiveEncoderHWAccel() != transcodeHWNone {
		t.Fatal("CPU HEVC encoder reported GPU acceleration")
	}
	card := NewRecipeCard(7, "profile", 42, "", opts)
	encoded, err := json.Marshal(card)
	if err != nil {
		t.Fatal(err)
	}
	var restored RecipeCard
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.HWAccel != transcodeHWVAAPI || restored.EffectiveEncoderHWAccel() != transcodeHWNone {
		t.Fatalf("stored card lost GPU conversion or CPU encode: %#v", restored)
	}
	runtimeOpts := restored.TranscodeOpts("", "", nil)
	if runtimeOpts.HWAccel != transcodeHWVAAPI || runtimeOpts.ToneMapMode != tonemap.ModeHardware || runtimeOpts.EffectiveEncoderHWAccel() != transcodeHWNone {
		t.Fatalf("restored opts lost execution/reporting distinction: %#v", runtimeOpts)
	}
	m := NewTranscodeManager()
	m.Sessions = NewSessionManager(0, 0)
	session := m.ReconstructSession(t.Context(), opts.SessionID, 7, restored)
	if session == nil || session.TranscodeHWAccel != transcodeHWNone || session.ToneMapMode != tonemap.ModeHardware {
		t.Fatalf("reconstructed activity misreported encoder: %#v", session)
	}
}

func TestReconstructionEncoderConfirmationPreservesSuccessor(t *testing.T) {
	for _, replace := range []bool{false, true} {
		name := "replanned"
		if replace {
			name = "replaced"
		}
		t.Run(name, func(t *testing.T) {
			sessions := NewSessionManager(0, 0)
			sessions.RegisterReconstructed(&Session{ID: "live", TranscodeHWAccel: transcodeHWQSV})
			expected, revision := sessions.CaptureReconstructedExecution("live")
			sessions.mu.Lock()
			if replace {
				sessions.sessions["live"] = &Session{ID: "live", TranscodeHWAccel: transcodeHWNVENC}
			} else {
				sessions.sessions["live"].streamRevision++
				sessions.sessions["live"].TranscodeHWAccel = transcodeHWNVENC
			}
			sessions.mu.Unlock()
			current := sessions.ConfirmReconstructedExecution(expected, revision, tonemap.ModeHardware, transcodeHWNone)
			if current == nil || current.TranscodeHWAccel != transcodeHWNVENC || current.ToneMapMode != "" {
				t.Fatalf("stale confirmation replaced successor's execution facts: %#v", current)
			}
		})
	}
}
