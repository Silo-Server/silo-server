package catalog

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

// libraryCollectionSlugConstraint is the unique index on a collection's slug
// within its legacy library_id.
const libraryCollectionSlugConstraint = "library_collections_library_slug_unique"

// LibraryCollectionTitleTakenError refuses a collection title that another
// admin collection already has in one of the collection's libraries.
type LibraryCollectionTitleTakenError struct {
	// LibraryName is the name of a library both collections show in.
	LibraryName string
}

func (e *LibraryCollectionTitleTakenError) Error() string {
	return fmt.Sprintf("another collection in %s already has this title", e.LibraryName)
}

// IsLibraryCollectionSlugConflict reports whether err is the unique index on
// a collection's slug within its library: another writer claiming a derived
// slug first, or an update naming a slug another collection already uses.
func IsLibraryCollectionSlugConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == libraryCollectionSlugConstraint
}
