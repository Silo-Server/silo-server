package realtimeadmission

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Silo-Server/silo-server/internal/ratelimit"
)

func TestAdmissionStatusDefaultsAndEpoch(t *testing.T) {
	for _, tc := range []struct{ backend, scope string }{
		{"memory", "per_node"}, {"redis", "shared"}, {"unknown", "unavailable"},
	} {
		c := New(nil, tc.backend)
		status := c.Status()
		if status.Version != 1 || status.Backend != tc.backend || status.Scope != tc.scope || status.Epoch == "" || status.DeadlineMS != 250 {
			t.Fatalf("status = %+v", status)
		}
		if status != c.Status() || status.Epoch == New(nil, tc.backend).Status().Epoch {
			t.Fatal("epoch does not identify one controller lifetime")
		}
		raw, err := json.Marshal(status)
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]any
		if err := json.Unmarshal(raw, &wire); err != nil || wire["version"] != float64(1) || wire["scope"] != tc.scope || wire["epoch"] != status.Epoch {
			t.Fatalf("wire status = %s error=%v", raw, err)
		}
		status.EventsAccount.PerSecond = 0
		if c.Status().EventsAccount.PerSecond != 20 {
			t.Fatal("status mutation changed enforcement")
		}
	}
}

func TestAdmissionUsesApprovedLimits(t *testing.T) {
	var calls []workCall
	c := New(storeFunc(func(_ context.Context, key string, limit ratelimit.WorkLimit) ratelimit.WorkResult {
		calls = append(calls, workCall{key, limit})
		return ratelimit.WorkResult{Allowed: true}
	}), "memory")
	id := Identity{UserID: 1, ProfileID: "profile", RoomID: "room"}
	for _, class := range []Class{Events, RoomRecovery, RoomAction} {
		requireAdmitted(t, c, class, id)
	}
	want := []ratelimit.WorkLimit{
		{PerSecond: 20, PerMinute: 120},
		{PerSecond: 8, PerMinute: 360}, {PerSecond: 32, PerMinute: 1440},
		{PerSecond: 4, PerMinute: 30}, {PerSecond: 8, PerMinute: 120},
	}
	if len(calls) != len(want) {
		t.Fatalf("store calls = %+v", calls)
	}
	for i, limit := range want {
		if calls[i].limit != limit {
			t.Fatalf("call %d = %+v, want %+v", i, calls[i], limit)
		}
	}
	if c.config.socketRate != 16 || c.config.socketBurst != 32 || c.config.accountRate != 64 || c.config.accountBurst != 128 {
		t.Fatal("input shield defaults differ from approved policy")
	}
}
