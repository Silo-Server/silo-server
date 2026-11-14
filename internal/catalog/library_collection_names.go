package catalog

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

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
// taken lists candidates a unique conflict already showed are in use: a
// SERIALIZABLE transaction's snapshot can't see a slug another writer
// committed after it began, so a retry has to skip it by name.
func freeLibraryCollectionSlug(ctx context.Context, tx pgx.Tx, libraryID int, slug string, taken []string) (string, error) {
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
			AND s.candidate <> ALL(coalesce($3::text[], '{}'))
		ORDER BY n LIMIT 1`, slug, libraryID, taken).Scan(&free)
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

// libraryCollectionTitleCheck is what the name rule checks for one update.
type libraryCollectionTitleCheck struct {
	// renamed reports a title change beyond case and spaces.
	renamed bool
	// legacyLibraryID is the collection's library_id before the update.
	legacyLibraryID int
	// within limits the check to these libraries; nil checks all of the
	// collection's libraries.
	within []int
}

// needed reports whether the update has to pass the check at all.
func (c libraryCollectionTitleCheck) needed() bool {
	return c.renamed || len(c.within) > 0
}

// planLibraryCollectionTitleCheck decides, from the collection's row as the
// update's transaction holds it, what the name rule must check: every library
// of the collection after a rename, only the added ones when an update just
// grows its libraries, and nothing otherwise, so a collection that already
// shares its title (an import's, or one from before the rule) still saves the
// rest of a full editor draft. Call it after the collection's parent lock.
func planLibraryCollectionTitleCheck(ctx context.Context, tx pgx.Tx, collectionID string, title *string, libraryIDs *[]int) (libraryCollectionTitleCheck, error) {
	var check libraryCollectionTitleCheck
	var currentTitle string
	var scope []int32
	err := tx.QueryRow(ctx, `
		SELECT c.title, c.library_id,
			coalesce(array_agg(l.library_id) FILTER (WHERE l.library_id IS NOT NULL), '{}')::int[]
		FROM library_collections c
		LEFT JOIN library_collection_libraries l ON l.collection_id = c.id
		WHERE c.id = $1
		GROUP BY c.id`, collectionID).Scan(&currentTitle, &check.legacyLibraryID, &scope)
	if errors.Is(err, pgx.ErrNoRows) {
		return check, ErrLibraryCollectionNotFound
	}
	if err != nil {
		return check, fmt.Errorf("loading collection title: %w", err)
	}
	if title != nil && comparableCollectionTitle(*title) != comparableCollectionTitle(currentTitle) {
		check.renamed = true
		return check, nil
	}
	if libraryIDs == nil {
		return check, nil
	}
	current := int32SliceToInts(scope)
	if len(current) == 0 {
		current = []int{check.legacyLibraryID}
	}
	for _, libraryID := range *libraryIDs {
		if libraryID > 0 && !slices.Contains(current, libraryID) && !slices.Contains(check.within, libraryID) {
			check.within = append(check.within, libraryID)
		}
	}
	return check, nil
}

// comparableCollectionTitle is a title as the name rule compares it: without
// case, with runs of spaces read as one and surrounding spaces dropped.
func comparableCollectionTitle(title string) string {
	return strings.ToLower(strings.Join(strings.Fields(title), " "))
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
