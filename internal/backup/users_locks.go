package backup

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/backup/userdata"
)

// prepareImport takes every data type's locks for the documents an import is
// about to write, after the caller took the account locks
// (pgstore.LockAccountForImport) and before any data row. Both import
// entrypoints, a selective restore's account and a profile import, use it,
// so they take locks in the one order userdata.Preparer documents.
func prepareImport(ctx context.Context, tx pgx.Tx, docs map[string][]userdata.Prepared) error {
	return userdata.PrepareAll(ctx, tx, userDataTypes, docs)
}
