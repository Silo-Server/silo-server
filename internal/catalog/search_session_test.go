package catalog

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestInMemorySearchSessionStoreExpiresIsolatesAndEvicts(t *testing.T) {
	store := &searchSessionStore{}
	expiresAt := time.Now().Add(time.Hour)
	ids := make([]string, 0, searchSessionsPerAccount+1)
	for i := range searchSessionsPerAccount + 1 {
		id, err := store.put(t.Context(), 7, searchRankingSession{
			IDs:       []string{fmt.Sprintf("item-%d", i)},
			ExpiresAt: expiresAt.Add(time.Duration(i) * time.Second),
		})
		if err != nil {
			t.Fatalf("put session %d: %v", i, err)
		}
		ids = append(ids, id)
	}
	if _, err := store.get(t.Context(), 7, ids[0]); !errors.Is(err, ErrCatalogCursorChanged) {
		t.Fatalf("oldest session error = %v, want ErrCatalogCursorChanged", err)
	}
	got, err := store.get(t.Context(), 7, ids[len(ids)-1])
	if err != nil {
		t.Fatalf("get newest session: %v", err)
	}
	if got.IDs[0] != fmt.Sprintf("item-%d", searchSessionsPerAccount) {
		t.Fatalf("newest session IDs = %v", got.IDs)
	}
	if _, err := store.get(t.Context(), 8, ids[len(ids)-1]); !errors.Is(err, ErrCatalogCursorChanged) {
		t.Fatalf("other account error = %v, want ErrCatalogCursorChanged", err)
	}

	expiredID, err := store.put(t.Context(), 8, searchRankingSession{ExpiresAt: time.Now().Add(-time.Second)})
	if err != nil {
		t.Fatalf("put expired session: %v", err)
	}
	if _, err := store.put(t.Context(), 9, searchRankingSession{ExpiresAt: expiresAt}); err != nil {
		t.Fatalf("put session after expired account: %v", err)
	}
	if _, ok := store.memory[8]; ok {
		t.Fatal("expired account remains after the next write")
	}
	if _, err := store.get(t.Context(), 8, expiredID); !errors.Is(err, ErrCatalogCursorChanged) {
		t.Fatalf("expired session error = %v, want ErrCatalogCursorChanged", err)
	}
}
