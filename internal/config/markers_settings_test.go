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
}
