package userdb

import (
	"errors"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestCollectionCASConcurrentSQLite(t *testing.T) {
	for _, operation := range []string{"update", "delete"} {
		t.Run(operation, func(t *testing.T) {
			s := newConformanceStore(t).(*SQLiteUserStore)
			ctx := t.Context()
			c, err := s.CreateCollection(ctx, userstore.CreateCollectionInput{CreatorProfileID: "owner", Name: "first"})
			if err != nil {
				t.Fatal(err)
			}
			revision, err := s.CollectionRevision(ctx, c.ID)
			if err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			var wg sync.WaitGroup
			for range 2 {
				wg.Go(func() {
					<-start
					other := NewSQLiteUserStore(s.db)
					if operation == "update" {
						results <- other.UpdateCollection(ctx, userstore.UpdateCollectionInput{ID: c.ID, RequestProfileID: "owner", Name: new("winner"), ExpectedRevision: &revision})
					} else {
						results <- other.DeleteCollectionIfRevision(ctx, c.ID, revision)
					}
				})
			}
			close(start)
			wg.Wait()
			close(results)
			winners, stale := 0, 0
			for err := range results {
				if err == nil {
					winners++
				} else if errors.Is(err, userstore.ErrCollectionRevisionMismatch) || (operation == "delete" && errors.Is(err, userstore.ErrCollectionNotFound)) {
					stale++
				} else {
					t.Fatalf("unexpected concurrent failure: %v", err)
				}
			}
			if winners != 1 || stale != 1 {
				t.Fatalf("winners=%d stale=%d", winners, stale)
			}
			if operation == "update" {
				if err := s.UpdateCollection(ctx, userstore.UpdateCollectionInput{ID: c.ID, RequestProfileID: "owner", Name: new("wildcard"), ExpectedRevision: new(int64(-1))}); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
