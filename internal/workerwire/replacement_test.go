package workerwire

import (
	"sync"
	"testing"
	"time"
)

func TestPendingReplacementKeepsHealthyGeneration(t *testing.T) {
	var mu sync.Mutex
	var revoked []uint64
	a, err := NewLocalAuthority(7, "boot", "nonce", func(_ string, generation uint64) { mu.Lock(); revoked = append(revoked, generation); mu.Unlock() })
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	snapshot := Snapshot{Version: 1, NodeID: 7, BootID: "boot", Nonce: "nonce", Sequence: 1, ValidForMillis: 2000, Active: map[string]uint64{"work": 1}, Pending: map[string]uint64{"work": 2}}
	if err := a.Apply(snapshot, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !a.Allowed("work", 1) || !a.Allowed("work", 2) {
		t.Fatal("candidate revoked healthy producer or cannot read its input")
	}
	snapshot.Sequence++
	snapshot.Pending = nil
	if err := a.Apply(snapshot, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !a.Allowed("work", 1) || a.Allowed("work", 2) {
		t.Fatal("failed replacement changed healthy generation")
	}
	mu.Lock()
	if len(revoked) != 1 || revoked[0] != 2 {
		t.Errorf("revoked=%v", revoked)
	}
	mu.Unlock()
}
