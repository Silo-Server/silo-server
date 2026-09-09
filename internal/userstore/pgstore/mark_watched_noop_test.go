package pgstore

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMarkWatchedBatchNoTargetsDoesNotAcquireConnection(t *testing.T) {
	pool, err := pgxpool.New(t.Context(), "postgres://localhost/unused?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	store := newStore(pool, 1)
	for _, targets := range [][]userstore.MarkWatchedTarget{nil, {{MediaItemID: ""}, {MediaItemID: " \t\n"}}} {
		written, err := store.MarkWatchedBatch(ctx, "profile", targets, nil)
		if err != nil || len(written) != 0 {
			t.Fatalf("no-op batch: written=%v error=%v", written, err)
		}
	}
}
