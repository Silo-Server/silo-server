package userstore

import "github.com/jackc/pgx/v5/pgxpool"

// ProgressSnapshotSource identifies the concrete selected account database.
// A wrapper must forward both values; nil means unsupported. This does not
// advertise that any other account uses PostgreSQL.
type ProgressSnapshotSource interface {
	ProgressSnapshotDatabase() (*pgxpool.Pool, int)
}
