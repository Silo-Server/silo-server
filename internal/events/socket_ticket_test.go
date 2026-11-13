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
	store := NewSocketTicketStore(nil, SocketAudienceEvents)
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
	// An API-key proof names the key at a revision and nothing else.
	key := SocketIdentity{UserID: 7, Role: "admin", APIKeyID: 3, APIKeyRevision: 9, AccessExpiresAt: time.Now().Add(time.Minute)}
	if _, err := store.Mint(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*SocketIdentity){
		"key with a session":   func(i *SocketIdentity) { i.SessionID = "session" },
		"key with a profile":   func(i *SocketIdentity) { i.ProfileID = "profile" },
		"key with no revision": func(i *SocketIdentity) { i.APIKeyRevision = 0 },
	} {
		invalid := key
		change(&invalid)
		if _, err := store.Mint(t.Context(), invalid); err == nil {
			t.Fatal(name, "minted")
		}
	}
}

// Deployments hold every audience's tickets under one Redis prefix; sharing
// the memory map stands in for it.
func TestSocketTicketRefusedOutsideItsAudience(t *testing.T) {
	events := NewSocketTicketStore(nil, SocketAudienceEvents)
	logs := NewSocketTicketStore(nil, SocketAudienceAdminLogs)
	logs.tickets = events.tickets
	ticket, err := events.Mint(t.Context(), socketIdentity())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := logs.Consume(t.Context(), ticket); err == nil {
		t.Fatal("events ticket opened the log stream")
	}
	ticket, err = logs.Mint(t.Context(), socketIdentity())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := logs.Consume(t.Context(), ticket); err != nil || got.Audience != SocketAudienceAdminLogs {
		t.Fatalf("own audience: %+v %v", got, err)
	}
}
