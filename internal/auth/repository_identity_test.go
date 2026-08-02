package auth

import (
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsUserIdentityDuplicateError(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		want       bool
		constraint string
	}{
		{
			name:       "username constraint",
			err:        &pgconn.PgError{Code: "23505", ConstraintName: "users_username_key"},
			want:       true,
			constraint: "users_username_key",
		},
		{
			name:       "email constraint",
			err:        &pgconn.PgError{Code: "23505", ConstraintName: "users_email_key"},
			want:       true,
			constraint: "users_email_key",
		},
		{
			name:       "wrapped username constraint",
			err:        fmt.Errorf("scan insert result: %w", &pgconn.PgError{Code: "23505", ConstraintName: "users_username_key"}),
			want:       true,
			constraint: "users_username_key",
		},
		{
			name:       "wrapped email constraint",
			err:        fmt.Errorf("scan update result: %w", &pgconn.PgError{Code: "23505", ConstraintName: "users_email_key"}),
			want:       true,
			constraint: "users_email_key",
		},
		{
			name: "primary key constraint",
			err:  &pgconn.PgError{Code: "23505", ConstraintName: "users_pkey"},
		},
		{
			name: "wrapped primary key constraint",
			err:  fmt.Errorf("scan insert result: %w", &pgconn.PgError{Code: "23505", ConstraintName: "users_pkey"}),
		},
		{
			name: "identity constraint with non-unique SQL state",
			err:  &pgconn.PgError{Code: "23503", ConstraintName: "users_username_key"},
		},
		{
			name: "non postgres error",
			err:  fmt.Errorf("write failed"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, constraint := isUserIdentityDuplicateError(test.err)
			if got != test.want || constraint != test.constraint {
				t.Fatalf("isUserIdentityDuplicateError() = (%v, %q), want (%v, %q)", got, constraint, test.want, test.constraint)
			}
		})
	}
}
