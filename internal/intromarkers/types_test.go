package intromarkers

import "testing"

func TestConfigNormalizedDefaultsSilenceNoiseThreshold(t *testing.T) {
	cfg := (Config{}).normalized()
	if cfg.SilenceNoiseThresholdDB == nil || *cfg.SilenceNoiseThresholdDB != -50 {
		t.Fatalf("expected default silence threshold -50, got %v", cfg.SilenceNoiseThresholdDB)
	}
}

func TestConfigNormalizedPreservesExplicitZeroSilenceNoiseThreshold(t *testing.T) {
	cfg := DefaultConfig("ffmpeg")
	cfg.SilenceNoiseThresholdDB = intPtr(0)

	normalized := cfg.normalized()
	if normalized.SilenceNoiseThresholdDB == nil || *normalized.SilenceNoiseThresholdDB != 0 {
		t.Fatalf("expected explicit silence threshold 0, got %v", normalized.SilenceNoiseThresholdDB)
	}
}

func TestConfigNormalizedRejectsPositiveSilenceNoiseThreshold(t *testing.T) {
	cfg := DefaultConfig("ffmpeg")
	cfg.SilenceNoiseThresholdDB = intPtr(5)

	normalized := cfg.normalized()
	if normalized.SilenceNoiseThresholdDB == nil || *normalized.SilenceNoiseThresholdDB != -50 {
		t.Fatalf("expected positive silence threshold to fall back to -50, got %v", normalized.SilenceNoiseThresholdDB)
	}
}

// Every stored fingerprint carries this key. Changing it discards the cache,
// and re-reading a large library's audio takes days.
func TestConfigHashKeepsStoredFingerprintKey(t *testing.T) {
	cfg := DefaultConfig("ffmpeg")
	if got := cfg.ConfigHash(); got != "475b8bded0ab1398" {
		t.Fatalf("ConfigHash() = %s, want the stored fingerprint key 475b8bded0ab1398", got)
	}
	cfg.MinimumIntroDurationSeconds, cfg.MaximumIntroDurationSeconds = 8, 200
	if got := cfg.ConfigHash(); got != "475b8bded0ab1398" {
		t.Fatalf("ConfigHash() with other duration bounds = %s, want it unchanged", got)
	}
	cfg.AnalysisPercent = 30
	if cfg.ConfigHash() == "475b8bded0ab1398" {
		t.Fatal("ConfigHash() must change with the analysis window")
	}
}

func TestAnalysisConfigHashTracksDurationBounds(t *testing.T) {
	base := DefaultConfig("ffmpeg")
	changed := base
	changed.MinimumIntroDurationSeconds = 15
	if base.AnalysisConfigHash() == changed.AnalysisConfigHash() {
		t.Fatal("changing the minimum intro duration must re-run season analysis")
	}
}

func TestDialogueRefinementKeepsTheMinimumIntroDuration(t *testing.T) {
	cfg := DefaultConfig("ffmpeg")
	if cfg.DialogueRefinementMinimumRemainingSeconds != float64(cfg.MinimumIntroDurationSeconds) {
		t.Fatalf("subtitle refinement minimum = %.0fs, want the %ds intro minimum so every detectable intro can be refined",
			cfg.DialogueRefinementMinimumRemainingSeconds, cfg.MinimumIntroDurationSeconds)
	}
}
