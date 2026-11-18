package metadata

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestResolveStaleUpsertError(t *testing.T) {
	// The benign missing-parent race is swallowed (nil); every other error is
	// wrapped and propagated so a real failure still surfaces.
	if err := resolveStaleUpsertError(nil, "c", "tmdb"); err != nil {
		t.Fatalf("resolveStaleUpsertError(nil) = %v, want nil", err)
	}

	fkErr := &pgconn.PgError{Code: "23503", ConstraintName: "stale_media_ids_content_id_fkey"}
	if err := resolveStaleUpsertError(fkErr, "c", "tmdb"); err != nil {
		t.Fatalf("resolveStaleUpsertError(missing-content FK) = %v, want nil", err)
	}

	other := &pgconn.PgError{Code: "23505", ConstraintName: "stale_media_ids_pkey"}
	if err := resolveStaleUpsertError(other, "c", "tmdb"); err == nil {
		t.Fatal("resolveStaleUpsertError(other pg error) = nil, want wrapped error")
	}

	generic := errors.New("connection reset")
	if err := resolveStaleUpsertError(generic, "c", "tmdb"); err == nil ||
		!errors.Is(err, generic) {
		t.Fatalf("resolveStaleUpsertError(generic) = %v, want wrapped generic", err)
	}
}
