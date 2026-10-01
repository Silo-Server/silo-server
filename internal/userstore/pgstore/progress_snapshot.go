package pgstore

import "github.com/jackc/pgx/v5/pgxpool"

func (s *PostgresUserStore) ProgressSnapshotDatabase() (*pgxpool.Pool, int) { return s.pool, s.userID }
