package handlers

import (
	"context"
	"errors"
	"testing"
)

func TestAdminSettingsGuardChecksLockedSnapshot(t *testing.T) {
	for _, single := range []bool{false, true} {
		t.Run(map[bool]string{false: "batch", true: "single"}[single], func(t *testing.T) {
			store := newSerializedSettingsStore(map[string]string{"server.log_level": "info"})
			callbacks := 0
			h := &AdminHandler{SettingsRepo: store, OnServerSettingUpdated: func(context.Context, string, string) { callbacks++ }}
			stale := errors.New("stale captured settings")
			calls := 0
			guard := func(snapshot AdminSettingsSnapshot) error {
				calls++
				if calls == 1 {
					if snapshot.Stored["server.log_level"] != "info" {
						t.Fatal("wrong preflight state")
					}
					// Simulate a different writer committing between preflight and admission.
					return store.Set(t.Context(), "server.log_level", "error")
				}
				if snapshot.Stored["server.log_level"] != "error" {
					t.Fatal("guard did not receive locked current state")
				}
				return stale
			}
			var err error
			if single {
				_, err = h.UpdateAdminSetting(t.Context(), "server.log_level", "debug", guard)
			} else {
				_, err = h.UpdateAdminSettings(t.Context(), map[string]string{"server.log_level": "debug"}, guard)
			}
			if !errors.Is(err, stale) || calls != 2 || store.atomicCalls != 1 || store.values["server.log_level"] != "error" || callbacks != 0 {
				t.Fatalf("err=%v guards=%d atomic=%d callbacks=%d", err, calls, store.atomicCalls, callbacks)
			}
		})
	}
}
func TestAdminSettingsGuardRefusesBeforeValidationOrProbes(t *testing.T) {
	for _, single := range []bool{false, true} {
		t.Run(map[bool]string{false: "batch", true: "single"}[single], func(t *testing.T) {
			store := newSerializedSettingsStore(map[string]string{})
			h := &AdminHandler{SettingsRepo: store}
			stale := errors.New("stale displayed settings")
			guard := func(AdminSettingsSnapshot) error { return stale }
			var err error
			// Enabling uploads would otherwise inspect storage prerequisites.
			if single {
				_, err = h.UpdateAdminSetting(t.Context(), "diagnostics.uploads_enabled", "true", guard)
			} else {
				_, err = h.UpdateAdminSettings(t.Context(), map[string]string{"diagnostics.uploads_enabled": "true"}, guard)
			}
			if !errors.Is(err, stale) || store.atomicCalls != 0 || len(store.values) != 0 {
				t.Fatalf("err=%v atomic=%d", err, store.atomicCalls)
			}
		})
	}
}
