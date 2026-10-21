package events

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func socketIdentity() SocketIdentity {
	return SocketIdentity{UserID: 7, SessionID: "session", Role: "user", ProfileID: "profile", ProfileToken: "pin-proof", AccessFingerprint: "scope", AccessExpiresAt: time.Now().Add(time.Minute)}
}

func testSocketTicketSingleUse(t *testing.T, mint, consume *SocketTicketStore) {
	t.Helper()
	identity := socketIdentity()
	ticket, err := mint.Mint(t.Context(), identity)
	if err != nil {
		t.Fatal(err)
	}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			got, err := consume.Consume(t.Context(), ticket)
			if err == nil {
				successes.Add(1)
				if got.SessionID != identity.SessionID || got.ProfileToken != identity.ProfileToken || got.AccessFingerprint != identity.AccessFingerprint {
					t.Error("authority changed")
				}
			}
		})
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("successful consumptions: %d", successes.Load())
	}
}
func TestSocketTicketMemorySingleUseAndExpiry(t *testing.T) {
	store := NewSocketTicketStore(nil)
	testSocketTicketSingleUse(t, store, store)
	ticket, err := store.Mint(t.Context(), socketIdentity())
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	v := store.tickets[ticket]
	v.TicketExpiresAt = time.Now().Add(-time.Second)
	store.tickets[ticket] = v
	store.mu.Unlock()
	if _, err := store.Consume(t.Context(), ticket); err == nil {
		t.Fatal("expired proof accepted")
	}
	invalid := socketIdentity()
	invalid.SessionID = ""
	if _, err := store.Mint(t.Context(), invalid); err == nil {
		t.Fatal("sessionless proof minted")
	}
}
