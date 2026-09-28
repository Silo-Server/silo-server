package pgstore

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/jackc/pgx/v5"
)

// ViewerSnapshotReader exposes only the owning preference reads on an existing
// transaction. It neither starts a second snapshot nor implements a writable store.
type ViewerSnapshotReader struct {
	tx     pgx.Tx
	userID int
}

func NewViewerSnapshotReader(tx pgx.Tx, userID int) *ViewerSnapshotReader {
	return &ViewerSnapshotReader{tx: tx, userID: userID}
}
func (r *ViewerSnapshotReader) ListSettingValuesForResolution(ctx context.Context, q userstore.SettingResolutionQuery) ([]userstore.SettingValue, error) {
	return listSettingValuesForResolution(ctx, r.tx, r.userID, q)
}
