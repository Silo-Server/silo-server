package metadata

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsMissingContentForeignKeyViolation(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"generic", errors.New("boom"), false},
		{
			"unique violation",
			&pgconn.PgError{Code: "23505", ConstraintName: "stale_media_ids_pkey"},
			false,
		},
		{
			"fk violation on a different constraint",
			&pgconn.PgError{Code: "23503", ConstraintName: "some_other_fkey"},
			false,
		},
		{
			"fk violation on the content_id fkey",
			&pgconn.PgError{Code: "23503", ConstraintName: "stale_media_ids_content_id_fkey"},
			true,
		},
		{
			"wrapped fk violation on the content_id fkey",
			fmt.Errorf("upserting stale media ID: %w",
				&pgconn.PgError{Code: "23503", ConstraintName: "stale_media_ids_content_id_fkey"}),
			true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isMissingContentForeignKeyViolation(tc.err); got != tc.want {
				t.Fatalf("isMissingContentForeignKeyViolation(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestIsPgConstraintViolation(t *testing.T) {
	const code = "23503"
	const constraint = "some_fkey"
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"generic", errors.New("boom"), false},
		{"wrong code", &pgconn.PgError{Code: "23505", ConstraintName: constraint}, false},
		{"wrong constraint", &pgconn.PgError{Code: code, ConstraintName: "other"}, false},
		{"match", &pgconn.PgError{Code: code, ConstraintName: constraint}, true},
		{"wrapped match", fmt.Errorf("x: %w", &pgconn.PgError{Code: code, ConstraintName: constraint}), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPgConstraintViolation(tc.err, code, constraint); got != tc.want {
				t.Fatalf("isPgConstraintViolation(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

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
