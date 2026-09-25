package config

import "testing"

func TestMarkersDetectionWorkersSetting(t *testing.T) {
	for raw, want := range map[string]string{"1": "1", "4": "4", " 64 ": "64"} {
		got, err := NormalizeAdminSetting(MarkersDetectionWorkersSettingKey, raw)
		if err != nil || got != want {
			t.Fatalf("NormalizeAdminSetting(%q) = (%q, %v), want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{"0", "-1", "65", "many"} {
		if _, err := NormalizeAdminSetting(MarkersDetectionWorkersSettingKey, raw); err == nil {
			t.Fatalf("NormalizeAdminSetting(%q) accepted an out-of-range value", raw)
		}
	}

	cfg, err := LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatalf("LoadFromDB() returned error: %v", err)
	}
	if cfg.Markers.DetectionWorkers != 1 {
		t.Fatalf("default detection workers = %d, want 1", cfg.Markers.DetectionWorkers)
	}
	cfg, err = LoadFromDB(map[string]string{MarkersDetectionWorkersSettingKey: "3"})
	if err != nil {
		t.Fatalf("LoadFromDB() with detection workers returned error: %v", err)
	}
	if cfg.Markers.DetectionWorkers != 3 {
		t.Fatalf("configured detection workers = %d, want 3", cfg.Markers.DetectionWorkers)
	}
}
