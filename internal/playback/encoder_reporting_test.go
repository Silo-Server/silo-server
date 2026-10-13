package playback

import (
	"context"
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

func TestHEVCReconstructionConfirmsActualEncoder(t *testing.T) {
	sessions := NewSessionManager(0, 0)
	m := NewTranscodeManager()
	m.Sessions = sessions
	card := NewRecipeCard(7, "profile", 42, "", TranscodeOpts{
		SessionID: "hevc-report", HWAccel: transcodeHWVAAPI, TargetCodecVideo: transcodeCodecHEVC, ToneMapMode: tonemap.ModeHardware,
	})
	session, inserted, ok := m.reconstructSession(t.Context(), card.SessionID, 7, card, true)
	if !ok || inserted == nil {
		t.Fatal("session reconstruction failed")
	}
	runtime := &TranscodeSession{opts: TranscodeOpts{
		HWAccel: transcodeHWVAAPI, ToneMapMode: tonemap.ModeHardware, softwareEncode: true,
	}}
	result := m.completeTranscodeLoad(sessions, sessions.GetSession, session, inserted, 0, runtime)
	if result.status != SessionLoaded || result.session.TranscodeHWAccel != transcodeHWNone || result.session.ToneMapMode != tonemap.ModeHardware {
		t.Fatalf("runtime confirmation retained old GPU encoder: %#v", result.session)
	}
}

func TestEncoderReportingFallsBackForLegacyCards(t *testing.T) {
	card := RecipeCard{HWAccel: transcodeHWQSV}
	if card.EffectiveEncoderHWAccel() != transcodeHWQSV || card.TranscodeOpts("", "", nil).EffectiveEncoderHWAccel() != transcodeHWQSV {
		t.Fatal("legacy card lost its reported hardware encoder")
	}
}

func TestHEVCExistingSessionReconstructPublishesActualEncoder(t *testing.T) {
	sessions := NewSessionManager(0, 0)
	sessions.RegisterReconstructed(&Session{ID: "live", UserID: 5, ProfileID: "p", MediaFileID: 77, PlayMethod: PlayTranscode, TranscodeHWAccel: transcodeHWQSV})
	m := NewTranscodeManager()
	m.Sessions = sessions
	m.Config = func() TranscodeRuntimeConfig {
		return TranscodeRuntimeConfig{TranscodeDir: t.TempDir(), HWAccel: transcodeHWQSV}
	}
	m.resolveToneMapExecutor = func(_ context.Context, opts TranscodeOpts) (TranscodeOpts, error) { return opts, nil }
	m.startTranscode = func(_ context.Context, opts TranscodeOpts) (*TranscodeSession, error) {
		opts.HWAccel, opts.EncoderHWAccel = transcodeHWNone, ""
		return &TranscodeSession{opts: opts}, nil
	}
	card := NewRecipeCard(5, "p", 77, "", TranscodeOpts{SessionID: "live", TargetCodecVideo: transcodeCodecHEVC, HWAccel: transcodeHWQSV})
	session, runtime, status := m.LoadOrReconstructTranscode(t.Context(), sessions.GetSession, "live", 5, -1, &card)
	if status != SessionLoaded || session == nil || runtime == nil {
		t.Fatalf("reconstruction status=%v session=%v runtime=%v", status, session, runtime)
	}
	if session.TranscodeHWAccel != runtime.Opts().EffectiveEncoderHWAccel() {
		t.Fatalf("session reports %q while reconstructed runtime encodes on %q", session.TranscodeHWAccel, runtime.Opts().EffectiveEncoderHWAccel())
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
