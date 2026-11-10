package catalog

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// libraryCollectionSlugConstraint is the unique index on a collection's slug
// within its legacy library_id.
const libraryCollectionSlugConstraint = "library_collections_library_slug_unique"

// emptyLibraryCollectionSlug stands in for a slug a title gives none of, such
// as a title written only in a non-Latin script.
const emptyLibraryCollectionSlug = "collection"

// titleSpacePattern matches a run of the characters Go's unicode.IsSpace
// treats as spaces, which the admin service's title comparison drops. Postgres
// [[:space:]] alone leaves out the no-break and ideographic spaces in a C
// locale database.
const titleSpacePattern = `[[:space:]\u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]+`

// LibraryCollectionTitleTakenError refuses a collection title that another
// admin collection already has in one of the collection's libraries.
type LibraryCollectionTitleTakenError struct {
	// LibraryName is the name of a library both collections show in.
	LibraryName string
}

func (e *LibraryCollectionTitleTakenError) Error() string {
	return fmt.Sprintf("another collection in %s already has this title", e.LibraryName)
}

// freeLibraryCollectionSlug returns slug when no collection of the library
// has it, and otherwise the first free "<slug>-<n>". The slug is a URL name
// the server derives from the title, so two titles that read differently can
// share one ("Sci-Fi" and "Sci Fi"); neither should fail to save for it.
func freeLibraryCollectionSlug(ctx context.Context, tx pgx.Tx, libraryID int, slug string) (string, error) {
	if slug == "" {
		slug = emptyLibraryCollectionSlug
	}
	var free string
	err := tx.QueryRow(ctx, `
		SELECT candidate FROM (
			SELECT 1 AS n, $1::text AS candidate
			UNION ALL
			SELECT n, $1 || '-' || n FROM generate_series(2, 10000) n
		) s
		WHERE NOT EXISTS (SELECT 1 FROM library_collections WHERE library_id = $2 AND slug = s.candidate)
		ORDER BY n LIMIT 1`, slug, libraryID).Scan(&free)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("no free collection slug for %q", slug)
	}
	if err != nil {
		return "", fmt.Errorf("choosing collection slug: %w", err)
	}
	return free, nil
}

// IsLibraryCollectionSlugConflict reports whether err is the unique index on
// a collection's slug within its library: another writer claiming a derived
// slug first, or an update naming a slug another collection already uses.
func IsLibraryCollectionSlugConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == libraryCollectionSlugConstraint
}

// checkLibraryCollectionTitleFree refuses, with
// *LibraryCollectionTitleTakenError, a collection whose title another admin
// collection has in one of its libraries. Titles compare without case, and
// with runs of spaces read as one and surrounding spaces dropped, as a page
// shows them. A collection's libraries are its scope rows, or its
// legacy library_id when it has none; a non-empty within limits the check to
// those of them. It runs in the writer's transaction after the collection's
// title and scope are written.
func checkLibraryCollectionTitleFree(ctx context.Context, tx pgx.Tx, collectionID string, within []int) error {
	var libraryName string
	err := tx.QueryRow(ctx, `
		WITH self AS (
			SELECT id, library_id, lower(btrim(regexp_replace(title, $3, ' ', 'g'))) AS title
			FROM library_collections
			WHERE id = $1
		), self_libraries AS (
			SELECT lcl.library_id FROM library_collection_libraries lcl, self WHERE lcl.collection_id = self.id
			UNION
			SELECT self.library_id FROM self
			WHERE NOT EXISTS (SELECT 1 FROM library_collection_libraries lcl WHERE lcl.collection_id = self.id)
		)
		SELECT mf.name
		FROM self
		JOIN library_collections other
			ON other.id <> self.id AND lower(btrim(regexp_replace(other.title, $3, ' ', 'g'))) = self.title
		JOIN self_libraries sl ON (
			EXISTS (SELECT 1 FROM library_collection_libraries lcl WHERE lcl.collection_id = other.id AND lcl.library_id = sl.library_id)
			OR (other.library_id = sl.library_id
				AND NOT EXISTS (SELECT 1 FROM library_collection_libraries lcl WHERE lcl.collection_id = other.id))
		)
		JOIN media_folders mf ON mf.id = sl.library_id
		WHERE coalesce(cardinality($2::int[]), 0) = 0 OR sl.library_id = ANY($2::int[])
		ORDER BY mf.name, mf.id
		LIMIT 1`, collectionID, within, titleSpacePattern).Scan(&libraryName)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("checking collection title: %w", err)
	}
	return &LibraryCollectionTitleTakenError{LibraryName: libraryName}
}
