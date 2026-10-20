package main

import (
	"errors"
	"testing"
	"time"

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
func TestRealtimeAdmissionKeepsServerAvailableWithoutWorkStore(t *testing.T) {
	for _, backend := range []string{"redis", "Redis", "unknown"} {
		admission, closeStore, err := newRealtimeWorkAdmission(config.RateLimitConfig{Backend: backend}, config.RedisConfig{})
		if err == nil || admission == nil || closeStore == nil {
			t.Fatalf("%s: missing work store must return a refusing controller and a diagnostic", backend)
		}
		t.Cleanup(closeStore)
		if admission.Status().Scope != "unavailable" {
			t.Fatalf("%s: status=%+v", backend, admission.Status())
		}
		for _, class := range []realtimeadmission.Class{realtimeadmission.Events, realtimeadmission.RoomRecovery, realtimeadmission.RoomAction} {
			workErr := admission.Admit(t.Context(), class, realtimeadmission.Identity{UserID: 7, ProfileID: "profile", RoomID: "room"})
			refusal, ok := errors.AsType[*realtimeadmission.Refusal](workErr)
			if !ok || refusal.Code != realtimeadmission.UnavailableCode || refusal.RetryAfter != 2*time.Second {
				t.Fatalf("%s %s: missing store granted work or wrong refusal: %v", backend, class, workErr)
			}
		}
	}
}
