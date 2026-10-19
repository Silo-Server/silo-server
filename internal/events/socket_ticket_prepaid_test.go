package events

import (
	"testing"
	"time"
)

func TestSocketTicketPreservesOriginalPrepaidDeadline(t *testing.T) {
	store := NewSocketTicketStore(nil)
	deadline := time.Now().Add(5 * time.Second)
	identity := SocketIdentity{UserID: 7, SessionID: "synthetic-login", AccessFingerprint: "scope", AccessExpiresAt: time.Now().Add(time.Minute), InitialWorkPrepaid: true, TicketExpiresAt: deadline}
	ticket, err := store.Mint(t.Context(), identity)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Consume(t.Context(), ticket)
	if err != nil || !got.InitialWorkPrepaid || !got.TicketExpiresAt.Equal(deadline) {
		t.Fatalf("prepaid deadline moved: %+v %v", got, err)
	}
	if _, err := store.Consume(t.Context(), ticket); err == nil {
		t.Fatal("prepaid proof replayed")
	}
	identity.TicketExpiresAt = time.Now().Add(-time.Second)
	if _, err := store.Mint(t.Context(), identity); err == nil {
		t.Fatal("expired prepaid deadline renewed")
	}
}
