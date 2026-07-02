package catalog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/idgen"
	"github.com/Silo-Server/silo-server/internal/models"
)

// PersonRepository manages the people table.
type PersonRepository struct {
	pool *pgxpool.Pool
}

// NewPersonRepository creates a new PersonRepository.
func NewPersonRepository(pool *pgxpool.Pool) *PersonRepository {
	return &PersonRepository{pool: pool}
}

// FindOrCreate looks up a person by tmdb_id, imdb_id, or case-insensitive name.
// If found, it enriches empty fields with new data and returns the existing ID.
// If not found, it creates a new person and returns the new ID.
func (r *PersonRepository) FindOrCreate(ctx context.Context, p models.Person) (int64, error) {
	var existingID int64

	if p.TmdbID != "" {
		err := r.pool.QueryRow(ctx, "SELECT id FROM people WHERE tmdb_id = $1", p.TmdbID).Scan(&existingID)
		if err == nil {
			return r.enrichExisting(ctx, existingID, p)
		}
		if err != pgx.ErrNoRows {
			return 0, fmt.Errorf("lookup by tmdb_id: %w", err)
		}
	}

	if p.ImdbID != "" {
		err := r.pool.QueryRow(ctx, "SELECT id FROM people WHERE imdb_id = $1", p.ImdbID).Scan(&existingID)
		if err == nil {
			return r.enrichExisting(ctx, existingID, p)
		}
		if err != pgx.ErrNoRows {
			return 0, fmt.Errorf("lookup by imdb_id: %w", err)
		}
	}

	if p.Name != "" {
		err := r.pool.QueryRow(ctx, "SELECT id FROM people WHERE LOWER(name) = LOWER($1)", p.Name).Scan(&existingID)
		if err == nil {
			return r.enrichExisting(ctx, existingID, p)
		}
		if err != pgx.ErrNoRows {
			return 0, fmt.Errorf("lookup by name: %w", err)
		}
	}

	// Not found — create new person
	idStr, err := idgen.NextID()
	if err != nil {
		return 0, fmt.Errorf("generate person id: %w", err)
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse generated id: %w", err)
	}

	_, err = r.pool.Exec(ctx, `
		INSERT INTO people (id, name, sort_name, bio, birth_date, death_date, birthplace, homepage,
			photo_path, photo_source_path, photo_thumbhash, tmdb_id, imdb_id, tvdb_id, plex_guid)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		id, p.Name, p.SortName, p.Bio, p.BirthDate, p.DeathDate, p.Birthplace, p.Homepage,
		p.PhotoPath, p.PhotoSourcePath, p.PhotoThumbhash, p.TmdbID, p.ImdbID, p.TvdbID, p.PlexGUID,
	)
	if err != nil {
		return 0, fmt.Errorf("insert person: %w", err)
	}
	return id, nil
}

// enrichExisting updates empty fields on an existing person with non-empty values from p.
func (r *PersonRepository) enrichExisting(ctx context.Context, id int64, p models.Person) (int64, error) {
	var setClauses []string
	var args []interface{}
	argIdx := 1

	// fillEmpty only sets the column when the existing DB value is empty.
	fillEmpty := func(column, value string) {
		if value == "" {
			return
		}
		setClauses = append(setClauses, fmt.Sprintf("%s = CASE WHEN %s = '' THEN $%d ELSE %s END", column, column, argIdx, column))
		args = append(args, value)
		argIdx++
	}
	// overwriteIfReal sets the column when the new value is real. The "-"
	// sentinel ("no photo, but we tried") is only written when the existing
	// column is empty, so it cannot clobber a real provider path.
	overwriteIfReal := func(column, value string) {
		if value == "" {
			return
		}
		if value == "-" {
			setClauses = append(setClauses, fmt.Sprintf("%s = CASE WHEN %s = '' THEN $%d ELSE %s END", column, column, argIdx, column))
		} else {
			setClauses = append(setClauses, fmt.Sprintf("%s = $%d", column, argIdx))
		}
		args = append(args, value)
		argIdx++
	}

	fillEmpty("tmdb_id", p.TmdbID)
	fillEmpty("imdb_id", p.ImdbID)
	fillEmpty("tvdb_id", p.TvdbID)
	fillEmpty("plex_guid", p.PlexGUID)
	overwriteIfReal("photo_path", p.PhotoPath)
	overwriteIfReal("photo_source_path", p.PhotoSourcePath)
	overwriteIfReal("photo_thumbhash", p.PhotoThumbhash)
	fillEmpty("bio", p.Bio)
	fillEmpty("birthplace", p.Birthplace)
	fillEmpty("homepage", p.Homepage)

	if len(setClauses) == 0 {
		return id, nil
	}

	setClauses = append(setClauses, "updated_at = now()")
	query := fmt.Sprintf("UPDATE people SET %s WHERE id = $%d", strings.Join(setClauses, ", "), argIdx)
	args = append(args, id)

	if _, err := r.pool.Exec(ctx, query, args...); err != nil {
		return 0, fmt.Errorf("enrich person %d: %w", id, err)
	}
	return id, nil
}

// BatchFindOrCreate resolves a batch of people in 5 phases: lookup by tmdb_id,
// imdb_id, and name; enrich found people; insert new people. Returns a slice
// of IDs positionally matching the input. Zero values indicate failures.
func (r *PersonRepository) BatchFindOrCreate(ctx context.Context, people []models.Person) ([]int64, error) {
	if len(people) == 0 {
		return nil, nil
	}

	ids := make([]int64, len(people))

	// Deduplicate input by (TmdbID, ImdbID, Name) so each unique person is
	// looked up once. Track which input indices map to each unique person.
	type dedupKey struct{ tmdb, imdb, name string }
	uniqueMap := make(map[dedupKey]int)  // key → index in uniquePeople
	indexMap := make([]int, len(people)) // input index → unique index
	var uniquePeople []models.Person

	for i, p := range people {
		key := dedupKey{p.TmdbID, p.ImdbID, strings.ToLower(p.Name)}
		if ui, ok := uniqueMap[key]; ok {
			indexMap[i] = ui
		} else {
			ui := len(uniquePeople)
			uniqueMap[key] = ui
			indexMap[i] = ui
			uniquePeople = append(uniquePeople, p)
		}
	}

	uniqueIDs := make([]int64, len(uniquePeople))
	resolved := make([]bool, len(uniquePeople))

	// Track which unique people need enrichment.
	type enrichEntry struct {
		id     int64
		person models.Person
	}
	var toEnrich []enrichEntry

	// Phase 1: Batch lookup by tmdb_id.
	tmdbLookup := make(map[string][]int) // tmdb_id → unique indices
	var tmdbIDs []string
	for i, p := range uniquePeople {
		if p.TmdbID != "" {
			tmdbLookup[p.TmdbID] = append(tmdbLookup[p.TmdbID], i)
			tmdbIDs = append(tmdbIDs, p.TmdbID)
		}
	}
	if len(tmdbIDs) > 0 {
		rows, err := r.pool.Query(ctx,
			"SELECT id, tmdb_id FROM people WHERE tmdb_id = ANY($1::text[]) AND tmdb_id <> ''",
			tmdbIDs)
		if err != nil {
			return nil, fmt.Errorf("batch lookup by tmdb_id: %w", err)
		}
		for rows.Next() {
			var id int64
			var tid string
			if err := rows.Scan(&id, &tid); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scanning tmdb_id result: %w", err)
			}
			for _, ui := range tmdbLookup[tid] {
				if !resolved[ui] {
					uniqueIDs[ui] = id
					resolved[ui] = true
					toEnrich = append(toEnrich, enrichEntry{id, uniquePeople[ui]})
				}
			}
		}
		rows.Close()
	}

	// Phase 2: Batch lookup by imdb_id (remaining).
	var imdbIDs []string
	imdbLookup := make(map[string][]int)
	for i, p := range uniquePeople {
		if !resolved[i] && p.ImdbID != "" {
			imdbLookup[p.ImdbID] = append(imdbLookup[p.ImdbID], i)
			imdbIDs = append(imdbIDs, p.ImdbID)
		}
	}
	if len(imdbIDs) > 0 {
		rows, err := r.pool.Query(ctx,
			"SELECT id, imdb_id FROM people WHERE imdb_id = ANY($1::text[]) AND imdb_id <> ''",
			imdbIDs)
		if err != nil {
			return nil, fmt.Errorf("batch lookup by imdb_id: %w", err)
		}
		for rows.Next() {
			var id int64
			var iid string
			if err := rows.Scan(&id, &iid); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scanning imdb_id result: %w", err)
			}
			for _, ui := range imdbLookup[iid] {
				if !resolved[ui] {
					uniqueIDs[ui] = id
					resolved[ui] = true
					toEnrich = append(toEnrich, enrichEntry{id, uniquePeople[ui]})
				}
			}
		}
		rows.Close()
	}

	// Phase 3: Batch lookup by name (remaining).
	var nameValues []string
	nameLookup := make(map[string][]int) // LOWER(name) → unique indices
	for i, p := range uniquePeople {
		if !resolved[i] && p.Name != "" {
			lower := strings.ToLower(p.Name)
			nameLookup[lower] = append(nameLookup[lower], i)
			nameValues = append(nameValues, lower)
		}
	}
	if len(nameValues) > 0 {
		rows, err := r.pool.Query(ctx,
			"SELECT id, LOWER(name) FROM people WHERE LOWER(name) = ANY($1::text[])",
			nameValues)
		if err != nil {
			return nil, fmt.Errorf("batch lookup by name: %w", err)
		}
		for rows.Next() {
			var id int64
			var name string
			if err := rows.Scan(&id, &name); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scanning name result: %w", err)
			}
			for _, ui := range nameLookup[name] {
				if !resolved[ui] {
					uniqueIDs[ui] = id
					resolved[ui] = true
					toEnrich = append(toEnrich, enrichEntry{id, uniquePeople[ui]})
				}
			}
		}
		rows.Close()
	}

	// Phase 4: Batch enrich found people (same fillEmpty/overwrite semantics).
	if len(toEnrich) > 0 {
		enrichIDs := make([]int64, len(toEnrich))
		eTmdbIDs := make([]string, len(toEnrich))
		eImdbIDs := make([]string, len(toEnrich))
		eTvdbIDs := make([]string, len(toEnrich))
		ePlexGUIDs := make([]string, len(toEnrich))
		ePhotoPaths := make([]string, len(toEnrich))
		ePhotoSourcePaths := make([]string, len(toEnrich))
		ePhotoThumbs := make([]string, len(toEnrich))
		eBios := make([]string, len(toEnrich))
		eBirthplaces := make([]string, len(toEnrich))
		eHomepages := make([]string, len(toEnrich))
		for i, e := range toEnrich {
			enrichIDs[i] = e.id
			eTmdbIDs[i] = e.person.TmdbID
			eImdbIDs[i] = e.person.ImdbID
			eTvdbIDs[i] = e.person.TvdbID
			ePlexGUIDs[i] = e.person.PlexGUID
			ePhotoPaths[i] = e.person.PhotoPath
			ePhotoSourcePaths[i] = e.person.PhotoSourcePath
			ePhotoThumbs[i] = e.person.PhotoThumbhash
			eBios[i] = e.person.Bio
			eBirthplaces[i] = e.person.Birthplace
			eHomepages[i] = e.person.Homepage
		}
		_, err := r.pool.Exec(ctx, `
			UPDATE people SET
				tmdb_id = CASE WHEN people.tmdb_id = '' AND t.tmdb_id <> '' THEN t.tmdb_id ELSE people.tmdb_id END,
				imdb_id = CASE WHEN people.imdb_id = '' AND t.imdb_id <> '' THEN t.imdb_id ELSE people.imdb_id END,
				tvdb_id = CASE WHEN people.tvdb_id = '' AND t.tvdb_id <> '' THEN t.tvdb_id ELSE people.tvdb_id END,
				plex_guid = CASE WHEN people.plex_guid = '' AND t.plex_guid <> '' THEN t.plex_guid ELSE people.plex_guid END,
				photo_path = CASE
					WHEN t.photo_path NOT IN ('', '-') THEN t.photo_path
					WHEN people.photo_path = ''        THEN t.photo_path
					ELSE people.photo_path
				END,
				photo_source_path = CASE
					WHEN t.photo_source_path NOT IN ('', '-') THEN t.photo_source_path
					WHEN people.photo_source_path = ''        THEN t.photo_source_path
					ELSE people.photo_source_path
				END,
				photo_thumbhash = CASE
					WHEN t.photo_thumbhash NOT IN ('', '-') THEN t.photo_thumbhash
					WHEN people.photo_thumbhash = ''        THEN t.photo_thumbhash
					ELSE people.photo_thumbhash
				END,
				bio = CASE WHEN people.bio = '' AND t.bio <> '' THEN t.bio ELSE people.bio END,
				birthplace = CASE WHEN people.birthplace = '' AND t.birthplace <> '' THEN t.birthplace ELSE people.birthplace END,
				homepage = CASE WHEN people.homepage = '' AND t.homepage <> '' THEN t.homepage ELSE people.homepage END,
				updated_at = NOW()
			FROM UNNEST($1::bigint[], $2::text[], $3::text[], $4::text[], $5::text[],
			            $6::text[], $7::text[], $8::text[], $9::text[], $10::text[], $11::text[])
				AS t(id, tmdb_id, imdb_id, tvdb_id, plex_guid,
				     photo_path, photo_source_path, photo_thumbhash, bio, birthplace, homepage)
			WHERE people.id = t.id`,
			enrichIDs, eTmdbIDs, eImdbIDs, eTvdbIDs, ePlexGUIDs,
			ePhotoPaths, ePhotoSourcePaths, ePhotoThumbs, eBios, eBirthplaces, eHomepages,
		)
		if err != nil {
			return nil, fmt.Errorf("batch enrich people: %w", err)
		}
	}

	// Phase 5: Batch insert new people.
	var newIndices []int
	for i := range uniquePeople {
		if !resolved[i] {
			newIndices = append(newIndices, i)
		}
	}
	if len(newIndices) > 0 {
		newIDs := make([]int64, len(newIndices))
		names := make([]string, len(newIndices))
		sortNames := make([]string, len(newIndices))
		bios := make([]string, len(newIndices))
		birthDates := make([]*time.Time, len(newIndices))
		deathDates := make([]*time.Time, len(newIndices))
		birthplaces := make([]string, len(newIndices))
		homepages := make([]string, len(newIndices))
		photoPaths := make([]string, len(newIndices))
		photoSourcePaths := make([]string, len(newIndices))
		photoThumbs := make([]string, len(newIndices))
		nTmdbIDs := make([]string, len(newIndices))
		nImdbIDs := make([]string, len(newIndices))
		nTvdbIDs := make([]string, len(newIndices))
		nPlexGUIDs := make([]string, len(newIndices))

		for j, ui := range newIndices {
			idStr, err := idgen.NextID()
			if err != nil {
				continue
			}
			id, err := strconv.ParseInt(idStr, 10, 64)
			if err != nil {
				continue
			}
			p := uniquePeople[ui]
			newIDs[j] = id
			names[j] = p.Name
			sortNames[j] = p.SortName
			bios[j] = p.Bio
			birthDates[j] = p.BirthDate
			deathDates[j] = p.DeathDate
			birthplaces[j] = p.Birthplace
			homepages[j] = p.Homepage
			photoPaths[j] = p.PhotoPath
			photoSourcePaths[j] = p.PhotoSourcePath
			photoThumbs[j] = p.PhotoThumbhash
			nTmdbIDs[j] = p.TmdbID
			nImdbIDs[j] = p.ImdbID
			nTvdbIDs[j] = p.TvdbID
			nPlexGUIDs[j] = p.PlexGUID
		}

		rows, err := r.pool.Query(ctx, `
			INSERT INTO people (id, name, sort_name, bio, birth_date, death_date, birthplace, homepage,
				photo_path, photo_source_path, photo_thumbhash, tmdb_id, imdb_id, tvdb_id, plex_guid)
			SELECT * FROM UNNEST(
				$1::bigint[], $2::text[], $3::text[], $4::text[], $5::date[], $6::date[],
				$7::text[], $8::text[], $9::text[], $10::text[], $11::text[], $12::text[],
				$13::text[], $14::text[], $15::text[]
			)
			ON CONFLICT DO NOTHING
			RETURNING id`,
			newIDs, names, sortNames, bios, birthDates, deathDates,
			birthplaces, homepages, photoPaths, photoSourcePaths, photoThumbs, nTmdbIDs, nImdbIDs,
			nTvdbIDs, nPlexGUIDs,
		)
		if err != nil {
			return nil, fmt.Errorf("batch insert people: %w", err)
		}

		// Collect inserted IDs.
		insertedSet := make(map[int64]bool)
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scanning inserted person id: %w", err)
			}
			insertedSet[id] = true
		}
		rows.Close()

		// Assign IDs: either from successful insert or via re-query for race losers.
		for j, ui := range newIndices {
			if newIDs[j] == 0 {
				continue
			}
			if insertedSet[newIDs[j]] {
				uniqueIDs[ui] = newIDs[j]
				resolved[ui] = true
			}
		}

		// Re-query for any people that lost the insert race (ON CONFLICT DO NOTHING
		// skipped them). Fall back to single-row FindOrCreate for these rare cases.
		for j, ui := range newIndices {
			if resolved[ui] || newIDs[j] == 0 {
				continue
			}
			p := uniquePeople[ui]
			id, err := r.FindOrCreate(ctx, p)
			if err == nil {
				uniqueIDs[ui] = id
				resolved[ui] = true
			}
		}
	}

	// Map unique IDs back to the original input positions.
	for i := range people {
		ids[i] = uniqueIDs[indexMap[i]]
	}

	return ids, nil
}

// Get retrieves a person by ID.
func (r *PersonRepository) Get(ctx context.Context, id int64) (*models.Person, error) {
	var p models.Person
	err := r.pool.QueryRow(ctx, `
		SELECT id, name, sort_name, bio, birth_date, death_date, birthplace, homepage,
			photo_path, photo_source_path, photo_thumbhash, tmdb_id, imdb_id, tvdb_id, plex_guid, created_at, updated_at
		FROM people WHERE id = $1`, id,
	).Scan(&p.ID, &p.Name, &p.SortName, &p.Bio, &p.BirthDate, &p.DeathDate, &p.Birthplace, &p.Homepage,
		&p.PhotoPath, &p.PhotoSourcePath, &p.PhotoThumbhash, &p.TmdbID, &p.ImdbID, &p.TvdbID, &p.PlexGUID, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("get person %d: %w", id, err)
	}
	return &p, nil
}

// GetByName retrieves a person by exact name (case-insensitive).
func (r *PersonRepository) GetByName(ctx context.Context, name string) (*models.Person, error) {
	var p models.Person
	err := r.pool.QueryRow(ctx, `
		SELECT id, name, sort_name, bio, birth_date, death_date, birthplace, homepage,
			photo_path, photo_source_path, photo_thumbhash, tmdb_id, imdb_id, tvdb_id, plex_guid, created_at, updated_at
		FROM people WHERE LOWER(name) = LOWER($1)`, name,
	).Scan(&p.ID, &p.Name, &p.SortName, &p.Bio, &p.BirthDate, &p.DeathDate, &p.Birthplace, &p.Homepage,
		&p.PhotoPath, &p.PhotoSourcePath, &p.PhotoThumbhash, &p.TmdbID, &p.ImdbID, &p.TvdbID, &p.PlexGUID, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("get person by name %q: %w", name, err)
	}
	return &p, nil
}

// Search finds persons by name substring (case-insensitive), ordered by name.
//
// The predicate is `name ILIKE '%term%'` rather than `LOWER(name) LIKE ...` so
// the pg_trgm GIN index idx_people_name_trgm can serve it: for a rare 3+ char
// term that index turns a ~300-400ms full name-index scan into a ~10ms bitmap
// scan. ILIKE is itself case-insensitive, so this stays equivalent to the prior
// LOWER(name) comparison (including its existing treatment of % and _ in the
// term as LIKE wildcards).
func (r *PersonRepository) Search(ctx context.Context, query string, limit int) ([]models.Person, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, name, sort_name, bio, birth_date, death_date, birthplace, homepage,
			photo_path, photo_source_path, photo_thumbhash, tmdb_id, imdb_id, tvdb_id, plex_guid, created_at, updated_at
		FROM people WHERE name ILIKE '%' || $1 || '%'
		ORDER BY name LIMIT $2`, query, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("search people: %w", err)
	}
	defer rows.Close()

	var people []models.Person
	for rows.Next() {
		var p models.Person
		if err := rows.Scan(&p.ID, &p.Name, &p.SortName, &p.Bio, &p.BirthDate, &p.DeathDate, &p.Birthplace, &p.Homepage,
			&p.PhotoPath, &p.PhotoSourcePath, &p.PhotoThumbhash, &p.TmdbID, &p.ImdbID, &p.TvdbID, &p.PlexGUID, &p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan person: %w", err)
		}
		people = append(people, p)
	}
	return people, rows.Err()
}

// maxPersonConflictResolutions bounds how many external-id collisions a single
// Update is allowed to resolve before giving up. One per unique external-id
// field is the natural ceiling (today only tmdb_id and imdb_id are unique).
const maxPersonConflictResolutions = 4

// Update updates all non-key fields on a person.
//
// A person's resolved external ids (tmdb_id / imdb_id) can collide with another
// row that turns out to be the same human discovered through a different id —
// e.g. one credit ingested with only a tmdb_id and another with only an imdb_id,
// later reconciled when a refresh fills in the missing cross-id. Rather than
// failing that write (SQLSTATE 23505) and letting the refresh worker retry the
// same row forever, Update detects the collision and resolves it: it merges the
// two people when they are the same human, or drops just the conflicting id when
// they are not, then retries the write so updated_at advances and the row leaves
// the stale set.
//
// The common, no-collision path runs as a single plain transaction. Only when a
// 23505 surfaces does Update fall back to updateResolvingConflicts, which does
// the whole reconciliation — merge or drop, plus the field write and reindex —
// inside one transaction so it commits atomically (or not at all). Returns
// pgx.ErrNoRows if the row no longer exists (e.g. merged away concurrently).
func (r *PersonRepository) Update(ctx context.Context, p models.Person) error {
	err := r.applyUpdate(ctx, p)
	if err == nil || !isDuplicateKeyError(err) {
		return err
	}
	return r.updateResolvingConflicts(ctx, p)
}

// applyUpdate writes all non-key fields on a person and enqueues a search
// reindex for the items they appear in, all in one transaction.
func (r *PersonRepository) applyUpdate(ctx context.Context, p models.Person) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin person update tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	affected, err := execPersonUpdate(ctx, tx, p)
	if err != nil {
		return err
	}
	if affected == 0 {
		// The row was deleted out from under us (e.g. merged away concurrently).
		return pgx.ErrNoRows
	}
	if err := reindexPersonItems(ctx, tx, p.ID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit person update tx: %w", err)
	}
	return nil
}

// updateResolvingConflicts performs the person write inside a single transaction,
// reconciling external-id collisions (merge or drop) as they arise. The write is
// retried after each resolution via a savepoint so a failed attempt does not
// poison the transaction; the whole thing commits atomically once the write
// lands. Bounded by maxPersonConflictResolutions so it cannot spin.
func (r *PersonRepository) updateResolvingConflicts(ctx context.Context, p models.Person) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin person merge tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var affected int64
	for attempt := 0; ; attempt++ {
		affected, err = tryPersonUpdate(ctx, tx, p)
		if err == nil {
			break
		}
		if !isDuplicateKeyError(err) || attempt >= maxPersonConflictResolutions {
			return err
		}
		field, value, ok := conflictingExternalID(extractConstraint(err), p)
		if !ok {
			// Unique violation on a constraint we do not know how to reconcile.
			return err
		}
		resolved, resolveErr := r.resolveExternalIDConflict(ctx, tx, &p, field, value)
		if resolveErr != nil {
			return resolveErr
		}
		if !resolved {
			return err
		}
	}

	if affected == 0 {
		// Survivor was deleted concurrently (e.g. merged into another row); there
		// is nothing left to persist for this id.
		return pgx.ErrNoRows
	}
	if err := reindexPersonItems(ctx, tx, p.ID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit person merge tx: %w", err)
	}
	return nil
}

// execPersonUpdate writes all non-key person fields and returns the rows affected.
func execPersonUpdate(ctx context.Context, tx pgx.Tx, p models.Person) (int64, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE people SET name=$2, sort_name=$3, bio=$4, birth_date=$5, death_date=$6,
			birthplace=$7, homepage=$8, photo_path=$9, photo_source_path=$10, photo_thumbhash=$11,
			tmdb_id=$12, imdb_id=$13, tvdb_id=$14, plex_guid=$15, updated_at=now()
		WHERE id = $1`,
		p.ID, p.Name, p.SortName, p.Bio, p.BirthDate, p.DeathDate,
		p.Birthplace, p.Homepage, p.PhotoPath, p.PhotoSourcePath, p.PhotoThumbhash,
		p.TmdbID, p.ImdbID, p.TvdbID, p.PlexGUID,
	)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// tryPersonUpdate runs execPersonUpdate inside a savepoint so a unique violation
// rolls back only the attempt, leaving the surrounding transaction usable.
func tryPersonUpdate(ctx context.Context, tx pgx.Tx, p models.Person) (int64, error) {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin person update savepoint: %w", err)
	}
	affected, err := execPersonUpdate(ctx, sp, p)
	if err != nil {
		_ = sp.Rollback(ctx)
		return 0, err
	}
	if err := sp.Commit(ctx); err != nil {
		return 0, fmt.Errorf("release person update savepoint: %w", err)
	}
	return affected, nil
}

// reindexPersonItems enqueues a catalog-search reindex for every item the person
// appears in. After a merge the survivor's link set already includes the merged
// person's items, so this covers all items whose denormalized people text changed.
func reindexPersonItems(ctx context.Context, tx pgx.Tx, personID int64) error {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT content_id
		FROM item_people
		WHERE person_id = $1
	`, personID)
	if err != nil {
		return fmt.Errorf("listing items linked to person %d: %w", personID, err)
	}
	contentIDs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("collecting items linked to person %d: %w", personID, err)
	}
	if err := EnqueueSearchIndexUpserts(ctx, tx, contentIDs); err != nil {
		return fmt.Errorf("enqueueing catalog search person update for person %d: %w", personID, err)
	}
	return nil
}

// resolveExternalIDConflict reconciles a unique-violation on (field=value) raised
// while updating *p, operating within the caller's transaction tx. It mutates *p
// in place (folding in the partner's data, or dropping the conflicting id) and
// reports whether the caller should retry the write. resolved=false means the
// conflict could not be reconciled and the original error should be surfaced.
func (r *PersonRepository) resolveExternalIDConflict(
	ctx context.Context,
	tx pgx.Tx,
	p *models.Person,
	field string,
	value string,
) (resolved bool, err error) {
	if value == "" {
		return false, nil
	}

	// Identify the row that currently owns the colliding id.
	var partnerID int64
	lookup := fmt.Sprintf("SELECT id FROM people WHERE %s = $1", field)
	switch scanErr := tx.QueryRow(ctx, lookup, value).Scan(&partnerID); {
	case errors.Is(scanErr, pgx.ErrNoRows):
		// The conflicting owner vanished (concurrent merge); retry the plain write.
		return true, nil
	case scanErr != nil:
		return false, fmt.Errorf("locate person owning %s=%q: %w", field, value, scanErr)
	}
	if partnerID == p.ID {
		// Self-conflict is impossible under the unique index; bail rather than loop.
		return false, nil
	}

	// Lock both rows in a deterministic order so concurrent merges cannot deadlock.
	people, err := scanPeopleByIDsForUpdate(ctx, tx, p.ID, partnerID)
	if err != nil {
		return false, err
	}
	if _, ok := people[p.ID]; !ok {
		// The survivor was deleted under us (concurrent merge); retry the plain
		// write, which will affect zero rows and surface as pgx.ErrNoRows.
		return true, nil
	}
	partner, ok := people[partnerID]
	if !ok {
		// Partner already gone; retry the plain write.
		return true, nil
	}
	// Re-confirm the partner still holds the colliding value after locking.
	if externalIDValue(partner, field) != value {
		return true, nil
	}

	if !canMergePeople(*p, partner) {
		// Not confidently the same human (contradictory ids, or names disagree):
		// restore the survivor's currently-persisted value for this field so the
		// retried write becomes a no-op on that column. This lets the write commit
		// instead of looping forever, without destructively deleting a
		// possibly-distinct person and without blanking an id the survivor already
		// held (blanking would silently drop a previously-valid provider id, e.g.
		// on the admin PATCH path that mutates an existing id into a colliding one).
		// Writing the row's own current value back can never violate the unique
		// index, so this field will not re-trigger the conflict.
		setExternalIDField(p, field, externalIDValue(people[p.ID], field))
		slog.Warn("person merge: declining to merge, preserving survivor's existing id",
			"person_id", p.ID, "partner_id", partnerID, "field", field, "value", value,
			"person_name", p.Name, "partner_name", partner.Name)
		return true, nil
	}

	// Fold the partner's ids and any metadata this write lacks onto the survivor,
	// so the retried write persists data only the partner had.
	mergePersonFields(p, partner)

	// Repoint the partner's credits onto the survivor, skipping links that would
	// duplicate an existing (content_id, kind, character) credit on the survivor.
	if _, err := tx.Exec(ctx, `
		UPDATE item_people ip SET person_id = $1
		WHERE ip.person_id = $2
		  AND NOT EXISTS (
			SELECT 1 FROM item_people e
			WHERE e.person_id = $1
			  AND e.content_id = ip.content_id
			  AND e.kind = ip.kind
			  AND e.character = ip.character
		  )`, p.ID, partnerID); err != nil {
		return false, fmt.Errorf("repoint item_people from %d to %d: %w", partnerID, p.ID, err)
	}

	// Delete the partner; any leftover duplicate credits cascade away.
	if _, err := tx.Exec(ctx, `DELETE FROM people WHERE id = $1`, partnerID); err != nil {
		return false, fmt.Errorf("delete merged person %d: %w", partnerID, err)
	}

	slog.Info("person merge: folded duplicate person",
		"survivor_id", p.ID, "merged_id", partnerID, "field", field, "value", value)
	return true, nil
}

// scanPeopleByIDsForUpdate loads the given people, locking them FOR UPDATE in
// ascending id order so concurrent merges acquire locks consistently.
func scanPeopleByIDsForUpdate(ctx context.Context, tx pgx.Tx, ids ...int64) (map[int64]models.Person, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, name, sort_name, bio, birth_date, death_date, birthplace, homepage,
			photo_path, photo_source_path, photo_thumbhash, tmdb_id, imdb_id, tvdb_id, plex_guid, created_at, updated_at
		FROM people
		WHERE id = ANY($1)
		ORDER BY id
		FOR UPDATE`, ids)
	if err != nil {
		return nil, fmt.Errorf("lock people for merge: %w", err)
	}
	defer rows.Close()

	out := make(map[int64]models.Person, len(ids))
	for rows.Next() {
		var p models.Person
		if err := rows.Scan(&p.ID, &p.Name, &p.SortName, &p.Bio, &p.BirthDate, &p.DeathDate, &p.Birthplace, &p.Homepage,
			&p.PhotoPath, &p.PhotoSourcePath, &p.PhotoThumbhash, &p.TmdbID, &p.ImdbID, &p.TvdbID, &p.PlexGUID, &p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan locked person: %w", err)
		}
		out[p.ID] = p
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate locked people: %w", err)
	}
	return out, nil
}

// conflictingExternalID maps a violated unique constraint to the person field
// and value that caused it. ok is false for constraints Update cannot reconcile.
func conflictingExternalID(constraint string, p models.Person) (field, value string, ok bool) {
	switch constraint {
	case "idx_people_tmdb_id":
		return "tmdb_id", p.TmdbID, p.TmdbID != ""
	case "idx_people_imdb_id":
		return "imdb_id", p.ImdbID, p.ImdbID != ""
	default:
		return "", "", false
	}
}

// externalIDValue returns the value of the named external-id column on p.
func externalIDValue(p models.Person, field string) string {
	switch field {
	case "tmdb_id":
		return p.TmdbID
	case "imdb_id":
		return p.ImdbID
	case "tvdb_id":
		return p.TvdbID
	case "plex_guid":
		return p.PlexGUID
	default:
		return ""
	}
}

// setExternalIDField sets the named external-id column on p to value.
func setExternalIDField(p *models.Person, field, value string) {
	switch field {
	case "tmdb_id":
		p.TmdbID = value
	case "imdb_id":
		p.ImdbID = value
	case "tvdb_id":
		p.TvdbID = value
	case "plex_guid":
		p.PlexGUID = value
	}
}

// externalIDsCompatible reports whether a and b can be the same person: no
// external-id field where both hold a different non-empty value. A field where
// one side is empty (or both match) is compatible.
func externalIDsCompatible(a, b models.Person) bool {
	return idsCompatible(a.TmdbID, b.TmdbID) &&
		idsCompatible(a.ImdbID, b.ImdbID) &&
		idsCompatible(a.TvdbID, b.TvdbID) &&
		idsCompatible(a.PlexGUID, b.PlexGUID)
}

func idsCompatible(x, y string) bool {
	return x == "" || y == "" || x == y
}

// canMergePeople reports whether two rows that collided on a shared external id
// are confidently the same human and may be folded together. A shared id is a
// strong signal, but providers occasionally hand the same id to two distinct
// people; requiring the names to agree as well guards against destructively
// deleting a genuinely different person. When this returns false the caller
// drops the conflicting id instead (non-destructive).
func canMergePeople(a, b models.Person) bool {
	return externalIDsCompatible(a, b) && personNamesMatch(a.Name, b.Name)
}

func personNamesMatch(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	return strings.EqualFold(a, b)
}

// mergePersonFields fills empty external ids and metadata on dst from src,
// preserving dst's existing values. dst is the refreshed survivor, so its
// populated fields win; src (the row being merged away) only backfills gaps.
func mergePersonFields(dst *models.Person, src models.Person) {
	if dst.Name == "" {
		dst.Name = src.Name
	}
	if dst.TmdbID == "" {
		dst.TmdbID = src.TmdbID
	}
	if dst.ImdbID == "" {
		dst.ImdbID = src.ImdbID
	}
	if dst.TvdbID == "" {
		dst.TvdbID = src.TvdbID
	}
	if dst.PlexGUID == "" {
		dst.PlexGUID = src.PlexGUID
	}
	if dst.Bio == "" {
		dst.Bio = src.Bio
	}
	if dst.Birthplace == "" {
		dst.Birthplace = src.Birthplace
	}
	if dst.Homepage == "" {
		dst.Homepage = src.Homepage
	}
	if dst.SortName == "" {
		dst.SortName = src.SortName
	}
	if dst.BirthDate == nil {
		dst.BirthDate = src.BirthDate
	}
	if dst.DeathDate == nil {
		dst.DeathDate = src.DeathDate
	}
	if dst.PhotoPath == "" {
		dst.PhotoPath = src.PhotoPath
		dst.PhotoSourcePath = src.PhotoSourcePath
		dst.PhotoThumbhash = src.PhotoThumbhash
	}
}

func (r *PersonRepository) UpdatePhotoIfSourceMatches(ctx context.Context, personID int64, sourcePath, cachedPath, thumbhash string) (bool, error) {
	if r == nil || r.pool == nil {
		return false, pgx.ErrNoRows
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE people
		SET photo_path = $3,
			photo_source_path = $2,
			photo_thumbhash = NULLIF($4, ''),
			updated_at = NOW()
		WHERE id = $1
		  AND photo_source_path = $2
	`, personID, sourcePath, cachedPath, thumbhash)
	if err != nil {
		return false, fmt.Errorf("updating cached person photo: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// FindRefreshCandidates returns people with external IDs that are incomplete or stale.
func (r *PersonRepository) FindRefreshCandidates(
	ctx context.Context,
	staleAfter time.Duration,
	limit int,
) ([]int64, error) {
	if limit <= 0 {
		return []int64{}, nil
	}

	args := []any{limit}
	stalePredicate := ""
	if staleAfter > 0 {
		args = append(args, time.Now().Add(-staleAfter))
		stalePredicate = " OR updated_at < $2"
	}

	rows, err := r.pool.Query(ctx, `
		SELECT id
		FROM people
		WHERE
			(tmdb_id <> '' OR imdb_id <> '' OR tvdb_id <> '')
			AND (
				COALESCE(bio, '') = ''
				OR COALESCE(photo_path, '') = ''
				OR birth_date IS NULL`+stalePredicate+`
			)
		ORDER BY updated_at ASC
		LIMIT $1`,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("query refresh candidates: %w", err)
	}
	defer rows.Close()

	ids := make([]int64, 0, limit)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan refresh candidate: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate refresh candidates: %w", err)
	}

	return ids, nil
}

// ListForItem returns all people credited on a media item, ordered by sort_order.
func (r *PersonRepository) ListForItem(ctx context.Context, contentID string) ([]models.ItemPerson, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT p.id, p.name, p.sort_name, p.bio, p.birth_date, p.death_date, p.birthplace, p.homepage,
			p.photo_path, p.photo_source_path, p.photo_thumbhash, p.tmdb_id, p.imdb_id, p.tvdb_id, p.plex_guid,
			p.created_at, p.updated_at,
			ip.kind, ip.character, ip.sort_order
		FROM item_people ip
		JOIN people p ON p.id = ip.person_id
		WHERE ip.content_id = $1
		ORDER BY ip.kind, ip.sort_order`, contentID,
	)
	if err != nil {
		return nil, fmt.Errorf("list people for item %s: %w", contentID, err)
	}
	defer rows.Close()

	return scanItemPeople(rows)
}

// ListForItems returns people credited on any of the given media items, grouped
// by content ID. This is the batch equivalent of ListForItem.
func (r *PersonRepository) ListForItems(ctx context.Context, contentIDs []string) (map[string][]models.ItemPerson, error) {
	if len(contentIDs) == 0 {
		return map[string][]models.ItemPerson{}, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT ip.content_id,
			p.id, p.name, p.sort_name, p.bio, p.birth_date, p.death_date, p.birthplace, p.homepage,
			p.photo_path, p.photo_source_path, p.photo_thumbhash, p.tmdb_id, p.imdb_id, p.tvdb_id, p.plex_guid,
			p.created_at, p.updated_at,
			ip.kind, ip.character, ip.sort_order
		FROM item_people ip
		JOIN people p ON p.id = ip.person_id
		WHERE ip.content_id = ANY($1)
		ORDER BY ip.content_id, ip.kind, ip.sort_order`, contentIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("list people for items: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]models.ItemPerson)
	for rows.Next() {
		var contentID string
		var p models.ItemPerson
		if err := rows.Scan(
			&contentID,
			&p.ID, &p.Name, &p.SortName, &p.Bio, &p.BirthDate, &p.DeathDate,
			&p.Birthplace, &p.Homepage, &p.PhotoPath, &p.PhotoSourcePath, &p.PhotoThumbhash,
			&p.TmdbID, &p.ImdbID, &p.TvdbID, &p.PlexGUID,
			&p.CreatedAt, &p.UpdatedAt,
			&p.Kind, &p.Character, &p.SortOrder,
		); err != nil {
			return nil, fmt.Errorf("scan item person: %w", err)
		}
		result[contentID] = append(result[contentID], p)
	}
	return result, rows.Err()
}

// ListByKind returns people of a specific kind credited on a media item.
func (r *PersonRepository) ListByKind(ctx context.Context, contentID string, kind models.PersonKind) ([]models.ItemPerson, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT p.id, p.name, p.sort_name, p.bio, p.birth_date, p.death_date, p.birthplace, p.homepage,
			p.photo_path, p.photo_source_path, p.photo_thumbhash, p.tmdb_id, p.imdb_id, p.tvdb_id, p.plex_guid,
			p.created_at, p.updated_at,
			ip.kind, ip.character, ip.sort_order
		FROM item_people ip
		JOIN people p ON p.id = ip.person_id
		WHERE ip.content_id = $1 AND ip.kind = $2
		ORDER BY ip.sort_order`, contentID, kind,
	)
	if err != nil {
		return nil, fmt.Errorf("list people for item %s kind %d: %w", contentID, kind, err)
	}
	defer rows.Close()

	return scanItemPeople(rows)
}

// CountItemsByType returns the number of media items a person appears in,
// grouped by item type (e.g. "movie", "series", "episode").
func (r *PersonRepository) CountItemsByType(ctx context.Context, personID int64) (map[string]int, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT mi.type, COUNT(DISTINCT mi.content_id)
		FROM item_people ip
		JOIN media_items mi ON mi.content_id = ip.content_id
		WHERE ip.person_id = $1
		GROUP BY mi.type`, personID,
	)
	if err != nil {
		return nil, fmt.Errorf("count items by type for person %d: %w", personID, err)
	}
	defer rows.Close()

	counts := make(map[string]int)
	for rows.Next() {
		var itemType string
		var count int
		if err := rows.Scan(&itemType, &count); err != nil {
			return nil, fmt.Errorf("scan item type count: %w", err)
		}
		counts[itemType] = count
	}
	return counts, rows.Err()
}

func scanItemPeople(rows pgx.Rows) ([]models.ItemPerson, error) {
	var people []models.ItemPerson
	for rows.Next() {
		var ip models.ItemPerson
		if err := rows.Scan(
			&ip.ID, &ip.Name, &ip.SortName, &ip.Bio, &ip.BirthDate, &ip.DeathDate, &ip.Birthplace, &ip.Homepage,
			&ip.PhotoPath, &ip.PhotoSourcePath, &ip.PhotoThumbhash, &ip.TmdbID, &ip.ImdbID, &ip.TvdbID, &ip.PlexGUID,
			&ip.CreatedAt, &ip.UpdatedAt,
			&ip.Kind, &ip.Character, &ip.SortOrder,
		); err != nil {
			return nil, fmt.Errorf("scan item_person: %w", err)
		}
		people = append(people, ip)
	}
	return people, rows.Err()
}
