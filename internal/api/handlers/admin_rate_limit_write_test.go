package handlers

import (
	"context"
	"errors"
	"testing"
)

func TestAdminRateLimitWriteGuardAndNoChange(t *testing.T) {
	store := newFakeRateLimitStore()
	h := NewRateLimitHandler(store, nil, nil, NewServerRestartStatusTracker())
	rejected := errors.New("captured version changed")
	_, err := h.UpdateAdminRateLimitConfig(t.Context(), AdminRateLimitUpdate{Enabled: new(false)}, func(v AdminRateLimitConfigView) error {
		if !v.Enabled {
			t.Fatal("guard did not receive current defaults")
		}
		return rejected
	})
	if !errors.Is(err, rejected) || store.setManyCalls != 0 {
		t.Fatal(err, store.setManyCalls)
	}
	before, _ := h.ReadAdminRateLimitConfig(context.Background())
	result, err := h.UpdateAdminRateLimitConfig(t.Context(), AdminRateLimitUpdate{Enabled: new(before.Enabled)}, func(AdminRateLimitConfigView) error { return nil })
	if err != nil || result.RestartRequired || store.setManyCalls != 0 {
		t.Fatal(result, err, store.setManyCalls)
	}
	result, err = h.UpdateAdminRateLimitConfig(t.Context(), AdminRateLimitUpdate{Enabled: new(false)}, func(AdminRateLimitConfigView) error { return nil })
	if err != nil || result.Status != "ok" || store.setManyCalls != 1 {
		t.Fatal(result, err, store.setManyCalls)
	}
}
