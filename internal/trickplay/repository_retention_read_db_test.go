package trickplay

import (
	"context"
	"testing"
	"time"
)

func TestRevisionProtectionReusesCoveredExpiryDB(t *testing.T) {
	f := newFixture(t)
	file := f.file(t, f.library(t, "movies", true), "covered-retention")
	f.reconcile(t)
	revision := f.generate(t, file, "server")
	expiry := time.Now().Add(96 * time.Hour).Truncate(time.Microsecond)
	if ok, err := f.repo.ProtectRevision(t.Context(), file, revision, expiry, testStore); err != nil || !ok {
		t.Fatalf("initial protection=%v error=%v", ok, err)
	}
	t.Run("does not rewrite the row", func(t *testing.T) {
		var before, after string
		if err := f.pool.QueryRow(t.Context(), `SELECT ctid::text FROM public.media_file_trickplay WHERE media_file_id=$1`, file).Scan(&before); err != nil {
			t.Fatal(err)
		}
		for i := range 32 {
			if ok, err := f.repo.ProtectRevision(t.Context(), file, revision, expiry.Add(-time.Duration(i)*time.Minute), testStore); err != nil || !ok {
				t.Fatalf("covered protection %d=%v error=%v", i, ok, err)
			}
		}
		if err := f.pool.QueryRow(t.Context(), `SELECT ctid::text FROM public.media_file_trickplay WHERE media_file_id=$1`, file).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if before != after {
			t.Fatalf("covered URL reads physically rewrote the row: ctid %s -> %s", before, after)
		}
	})
	t.Run("does not wait for a row lock", func(t *testing.T) {
		tx, err := f.pool.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(context.WithoutCancel(t.Context())) }()
		if _, err := tx.Exec(t.Context(), `SELECT 1 FROM public.media_file_trickplay WHERE media_file_id=$1 FOR UPDATE`, file); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if ok, err := f.repo.ProtectRevision(ctx, file, revision, expiry, testStore); err != nil || !ok {
			t.Fatalf("covered protection waited for an unrelated writer: protected=%v error=%v", ok, err)
		}
	})
}
