package catalog

import (
	"context"
	"errors"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

var ErrCatalogStorageUnsupported = errors.New("the selected user store does not support personalized catalog queries")

// requireQueryStore prevents PostgreSQL query predicates from silently reading
// unrelated or empty viewer state when the selected provider is SQLite.
func (r *CatalogResolver) requireQueryStore(ctx context.Context, def QueryDefinition, access AccessFilter) error {
	if !def.IsPersonalized() {
		return nil
	}
	store, err := r.catalogStoreForAccess(ctx, access)
	if err != nil {
		return err
	}
	if !userstore.HasCatalogSQLState(store) {
		return ErrCatalogStorageUnsupported
	}
	return nil
}
