package intromarkers

import "testing"

func TestConfigNormalizedSilenceNoiseThreshold(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  *int
		want int
	}{
		{"unset defaults to -50", nil, -50},
		{"explicit zero is kept", intPtr(0), 0},
		{"positive falls back to -50", intPtr(5), -50},
	} {
		cfg := DefaultConfig("ffmpeg")
		cfg.SilenceNoiseThresholdDB = tc.set
		if got := cfg.normalized().SilenceNoiseThresholdDB; got == nil || *got != tc.want {
			t.Errorf("%s: threshold %v, want %d", tc.name, got, tc.want)
		}
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

// Stored season analysis state carries this key, and changing it re-runs every
// season comparison. A deliberate change, such as bumping
// AnalysisBehaviorVersion, updates this value in the same commit.
func TestAnalysisConfigHashKeepsStoredSeasonStateKey(t *testing.T) {
	if got := DefaultConfig("ffmpeg").AnalysisConfigHash(); got != "1925c82931845811" {
		t.Fatalf("AnalysisConfigHash() = %s, want the stored season state key 1925c82931845811", got)
	}
}
