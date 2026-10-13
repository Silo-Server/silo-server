package workerwire

import (
	"sync"
	"testing"
	"time"
)

func testSnapshot() Snapshot {
	return Snapshot{Version: 1, NodeID: 17, BootID: "boot", Nonce: "nonce", Sequence: 1, Active: map[string]uint64{"work": 4}, ValidForMillis: 2000}
}

func TestAuthorityRejectsDelayedResponseWithoutRenewing(t *testing.T) {
	a, err := NewLocalAuthority(17, "boot", "nonce", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	s := testSnapshot()
	s.ValidForMillis = 60000
	if err = a.Apply(s, time.Now().Add(-3*time.Second)); err == nil {
		t.Fatal("delayed reply renewed authority beyond two seconds")
	}
	if a.Allowed("work", 4) {
		t.Fatal("expired work allowed")
	}
	s.ValidForMillis = 2000
	if err = a.Apply(s, time.Now()); err != nil {
		t.Fatal(err)
	}
	s.Sequence = 2
	if err = a.Apply(s, time.Now().Add(-3*time.Second)); err == nil {
		t.Fatal("delayed newer reply accepted")
	}
	if !a.Allowed("work", 4) {
		t.Fatal("rejected reply altered valid authority")
	}
}

func TestAuthorityBindsNodeBootNonceAndStrictSequence(t *testing.T) {
	a, _ := NewLocalAuthority(17, "boot", "nonce", nil)
	defer a.Close()
	s := testSnapshot()
	if err := a.Apply(s, time.Now()); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Snapshot){"version": func(s *Snapshot) { s.Version = 2 }, "node": func(s *Snapshot) { s.NodeID++ }, "boot": func(s *Snapshot) { s.BootID = "other" }, "nonce": func(s *Snapshot) { s.Nonce = "other" }, "zero_validity": func(s *Snapshot) { s.ValidForMillis = 0 }, "zero_generation": func(s *Snapshot) { s.Active = map[string]uint64{"work": 0} }} {
		t.Run(name, func(t *testing.T) {
			changed := s
			changed.Sequence = 2
			mutate(&changed)
			if err := a.Apply(changed, time.Now()); err == nil {
				t.Fatal("invalid authority accepted")
			}
		})
	}
	if err := a.Apply(s, time.Now()); err == nil {
		t.Fatal("replayed sequence accepted")
	}
	if err := a.Apply(Snapshot{Version: 1, NodeID: 17, BootID: "boot", Nonce: "nonce", Sequence: 2, Active: s.Active, ValidForMillis: 2000}, time.Now().Add(time.Second)); err == nil {
		t.Fatal("future request-send time accepted")
	}
	if err := a.SetNonce("next"); err != nil {
		t.Fatal(err)
	}
	s.Sequence = 2
	if err := a.Apply(s, time.Now()); err == nil {
		t.Fatal("superseded request nonce accepted")
	}
	s.Nonce = "next"
	if err := a.Apply(s, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestAuthorityCopiesActiveSetAndRevokesOldGeneration(t *testing.T) {
	var mu sync.Mutex
	revoked := map[uint64]int{}
	a, _ := NewLocalAuthority(17, "boot", "nonce", func(work string, generation uint64) {
		mu.Lock()
		defer mu.Unlock()
		if work != "work" {
			t.Errorf("unexpected work %q", work)
		}
		revoked[generation]++
	})
	defer a.Close()
	s := testSnapshot()
	if err := a.Apply(s, time.Now()); err != nil {
		t.Fatal(err)
	}
	s.Active["work"] = 5
	if !a.Allowed("work", 4) || a.Allowed("work", 5) {
		t.Fatal("caller mutated authority")
	}
	s.Sequence = 2
	if err := a.Apply(s, time.Now()); err != nil {
		t.Fatal(err)
	}
	if a.Allowed("work", 4) || !a.Allowed("work", 5) {
		t.Fatal("replacement generation wrong")
	}
	s.Sequence = 3
	s.Active = map[string]uint64{}
	if err := a.Apply(s, time.Now()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if revoked[4] != 1 || revoked[5] != 1 {
		t.Fatalf("revocations=%v", revoked)
	}
}

func TestAuthorityAutomaticallyExpiresAndCallbackCanReenter(t *testing.T) {
	ended := make(chan uint64, 2)
	var a *LocalAuthority
	a, _ = NewLocalAuthority(17, "boot", "nonce", func(work string, generation uint64) {
		if a.Allowed(work, generation) {
			t.Error("expired generation allowed in callback")
		}
		ended <- generation
	})
	defer a.Close()
	s := testSnapshot()
	s.ValidForMillis = 30
	if err := a.Apply(s, time.Now()); err != nil {
		t.Fatal(err)
	}
	select {
	case generation := <-ended:
		if generation != 4 {
			t.Fatalf("wrong generation: %d", generation)
		}
	case <-time.After(time.Second):
		t.Fatal("authority expiry did not revoke work")
	}
	if a.Allowed("work", 4) {
		t.Fatal("expired work remains allowed")
	}
	select {
	case <-ended:
		t.Fatal("duplicate expiry callback")
	default:
	}
}

func TestAuthorityCloseAndConcurrentReads(t *testing.T) {
	a, _ := NewLocalAuthority(17, "boot", "nonce", nil)
	s := testSnapshot()
	if err := a.Apply(s, time.Now()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				a.Allowed("work", 4)
			}
		}()
	}
	for sequence := uint64(2); sequence < 20; sequence++ {
		s.Sequence = sequence
		if err := a.Apply(s, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	a.Close()
	wg.Wait()
	if a.Allowed("work", 4) {
		t.Fatal("closed authority allowed work")
	}
	s.Sequence = 21
	if a.Apply(s, time.Now()) == nil {
		t.Fatal("closed authority renewed")
	}
}
