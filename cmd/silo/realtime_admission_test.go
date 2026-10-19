package main

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/realtimeadmission"
)

func TestRealtimeAdmissionIndependentOfHTTPEnable(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		admission, closeStore, err := newRealtimeWorkAdmission(config.RateLimitConfig{Enabled: enabled, Backend: "memory"}, config.RedisConfig{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(closeStore)
		status := admission.Status()
		if status.Version != 1 || status.Scope != "per_node" || status.Backend != "memory" {
			t.Fatalf("status=%+v", status)
		}
		if err := admission.Admit(t.Context(), realtimeadmission.Events, realtimeadmission.Identity{UserID: 7}); err != nil {
			t.Fatal(err)
		}
	}
}
func TestRealtimeAdmissionRefusesMissingConfiguredBackend(t *testing.T) {
	for _, backend := range []string{"redis", "unknown"} {
		if _, _, err := newRealtimeWorkAdmission(config.RateLimitConfig{Backend: backend}, config.RedisConfig{}); err == nil {
			t.Fatalf("%s silently selected local capacity", backend)
		}
	}
}
