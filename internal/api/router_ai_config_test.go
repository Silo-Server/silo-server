package api

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
)

func TestEffectiveSubtitleAIConfigAllowsOpenRouter(t *testing.T) {
	for _, dedicated := range []bool{false, true} {
		t.Run(map[bool]string{false: "shared", true: "dedicated"}[dedicated], func(t *testing.T) {
			cfg := &config.Config{}
			cfg.AI.BaseURL = "https://openrouter.ai/api/v1"
			cfg.AI.ASRModel = "openai/whisper-large-v3"
			if dedicated {
				cfg.AI.BaseURL = "https://chat.example.test"
				cfg.AI.ASRBaseURL = "https://openrouter.ai/api/v1"
			}
			cfg.SubtitleAI.TranscribeEnabled = true
			if got := effectiveSubtitleAIConfig(cfg); !got.TranscribeEnabled || got.ASRModel != cfg.AI.ASRModel {
				t.Fatalf("OpenRouter transcription disabled or model changed: %+v", got)
			}
			cfg.SubtitleAI.TranscribeEnabled = false
			if effectiveSubtitleAIConfig(cfg).TranscribeEnabled {
				t.Fatal("explicitly disabled transcription was enabled")
			}
		})
	}
}
