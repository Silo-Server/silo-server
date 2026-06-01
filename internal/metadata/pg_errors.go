package metadata

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// isPgConstraintViolation reports whether err (or any error it wraps) is a
// Postgres error with the given SQLSTATE code raised by the named constraint.
// Centralizes the errors.As(&pgconn.PgError) + Code/ConstraintName boilerplate
// that several constraint-specific predicates in this package share.
func isPgConstraintViolation(err error, code, constraint string) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == code && pgErr.ConstraintName == constraint
}
