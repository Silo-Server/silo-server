package notifications

import (
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

func (s *interestTrackingStore) ProgressSnapshotDatabase() (*pgxpool.Pool, int) {
	if source, ok := s.UserStore.(userstore.ProgressSnapshotSource); ok {
		return source.ProgressSnapshotDatabase()
	}
	return nil, 0
}
