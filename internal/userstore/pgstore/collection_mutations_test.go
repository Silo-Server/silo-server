package pgstore

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestCollectionMutationRetryClassification(t *testing.T) {
	s := &PostgresUserStore{}
	deadlock := &pgconn.PgError{Code: "40P01", Message: "deadlock"}
	calls := 0
	err := s.runCollectionMutation(t.Context(), "", -1, func() error { calls++; return deadlock })
	if !errors.Is(err, deadlock) || calls != 1 || errors.Is(err, userstore.ErrCollectionRevisionMismatch) {
		t.Fatalf("deadlock reclassified/retried: %v calls=%d", err, calls)
	}
	serial := &pgconn.PgError{Code: "40001", Message: "serialization failure"}
	calls = 0
	err = s.runCollectionMutation(t.Context(), "", -1, func() error { calls++; return serial })
	if !errors.Is(err, serial) || calls != 3 || errors.Is(err, userstore.ErrCollectionRevisionMismatch) {
		t.Fatalf("wildcard serialization reclassified/unbounded: %v calls=%d", err, calls)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	calls = 0
	err = s.runCollectionMutation(canceled, "", -1, func() error { calls++; return nil })
	if !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("cancellation: %v calls=%d", err, calls)
	}
}
