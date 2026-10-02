package userstore

import "github.com/jackc/pgx/v5/pgxpool"

// ProgressSnapshotSource identifies the concrete selected account database.
// A wrapper must forward both values; nil means unsupported. This does not
// advertise that any other account uses PostgreSQL.
type ProgressSnapshotSource interface {
	ProgressSnapshotDatabase() (*pgxpool.Pool, int)
}

// CatalogProgressRelationStore is implemented by progress stores whose rows
// share the catalog database. The store owns progress visibility (hidden
// history, timestamp precision); catalog only joins the returned relation. A
// wrapper must forward the backing store's answer and report false when the
// backing store cannot supply one.
type CatalogProgressRelationStore interface {
	CatalogProgressRelation(pool *pgxpool.Pool, userID int, profileID string, firstArg int) (string, []any, bool)
}
