package intromarkers

import "testing"

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

// Stored season analysis state carries this key, and changing it re-runs every
// season comparison. A deliberate change, such as bumping
// AnalysisBehaviorVersion, updates this value in the same commit.
func TestAnalysisConfigHashKeepsStoredSeasonStateKey(t *testing.T) {
	if got := DefaultConfig("ffmpeg").AnalysisConfigHash(); got != "29b65deb3453cd3b" {
		t.Fatalf("AnalysisConfigHash() = %s, want the stored season state key 29b65deb3453cd3b", got)
	}
}
