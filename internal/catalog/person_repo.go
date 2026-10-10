package catalog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

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

// FindOrCreate looks up a person by tmdb_id, imdb_id, or a provider-safe
// case-insensitive name match. If found, it enriches empty fields with new data
// and returns the existing ID. If not found, it creates a new person and
// returns the new ID.
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
		rows, err := r.pool.Query(ctx, `
			SELECT id, name, tmdb_id, imdb_id, tvdb_id, plex_guid
			FROM people
			WHERE LOWER(name) = LOWER($1)
			ORDER BY id`, p.Name)
		if err != nil {
			return 0, fmt.Errorf("lookup by name: %w", err)
		}
		for rows.Next() {
			candidate := models.Person{}
			if err := rows.Scan(&candidate.ID, &candidate.Name, &candidate.TmdbID, &candidate.ImdbID, &candidate.TvdbID, &candidate.PlexGUID); err != nil {
				rows.Close()
				return 0, fmt.Errorf("scan name lookup: %w", err)
			}
			if !canResolvePersonByName(p, candidate) {
				continue
			}
			rows.Close()
			return r.enrichExisting(ctx, candidate.ID, p)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return 0, fmt.Errorf("iterate name lookup: %w", err)
		}
		rows.Close()
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

// personPhotoReplacePredicate builds the SQL condition under which item-credit
// data may write a person's photo columns.
//
// A cached artwork key is immutable and is never replaced from a credit.
// Credits carry the provider URL, so overwriting the key would both re-arm the
// download loop (EnqueueExistingProviderArtwork treats a URL in photo_path as
// "not cached yet") and hand the displaced key to the artwork GC trigger.
// Anything that is not a cached key is still replaceable by a real image: an
// empty column, the "-" no-photo sentinel, and a provider URL that never made
// it through the cache. Keeping URLs replaceable is what stops a person with
// no external id — FindRefreshCandidates skips them, so no refresh will ever
// revisit the row — from being stuck with a dead URL forever. The
// LIKE '%://%' test for "not a cached key" is the same one the artwork GC
// trigger and the image cache sweep use.
func personPhotoReplacePredicate(existingPath, incomingPath string) string {
	return fmt.Sprintf(
		"((COALESCE(%[1]s, '') = '' AND %[2]s <> '') OR "+
			"((%[1]s = '-' OR %[1]s LIKE '%%://%%') AND %[2]s NOT IN ('', '-') "+
			"AND %[2]s IS DISTINCT FROM %[1]s))",
		existingPath, incomingPath,
	)
}

// batchPersonEnrichmentSQL is built once: the text is constant, and the batch
// enricher runs per scan batch.
var batchPersonEnrichmentSQL = sync.OnceValue(buildBatchPersonEnrichmentQuery)

func buildBatchPersonEnrichmentQuery() string {
	photoReplace := personPhotoReplacePredicate("people.photo_path", "t.photo_path")
	return fmt.Sprintf(`
		UPDATE people SET
			tmdb_id = CASE WHEN COALESCE(people.tmdb_id, '') = '' AND t.tmdb_id <> '' THEN t.tmdb_id ELSE people.tmdb_id END,
			imdb_id = CASE WHEN COALESCE(people.imdb_id, '') = '' AND t.imdb_id <> '' THEN t.imdb_id ELSE people.imdb_id END,
			tvdb_id = CASE WHEN COALESCE(people.tvdb_id, '') = '' AND t.tvdb_id <> '' THEN t.tvdb_id ELSE people.tvdb_id END,
			plex_guid = CASE WHEN COALESCE(people.plex_guid, '') = '' AND t.plex_guid <> '' THEN t.plex_guid ELSE people.plex_guid END,
			photo_path = CASE WHEN %[1]s THEN t.photo_path ELSE people.photo_path END,
			photo_source_path = CASE WHEN %[1]s THEN t.photo_source_path ELSE people.photo_source_path END,
			photo_thumbhash = CASE WHEN %[1]s THEN t.photo_thumbhash ELSE people.photo_thumbhash END,
			bio = CASE WHEN COALESCE(people.bio, '') = '' AND t.bio <> '' THEN t.bio ELSE people.bio END,
			birthplace = CASE WHEN COALESCE(people.birthplace, '') = '' AND t.birthplace <> '' THEN t.birthplace ELSE people.birthplace END,
			homepage = CASE WHEN COALESCE(people.homepage, '') = '' AND t.homepage <> '' THEN t.homepage ELSE people.homepage END,
			updated_at = NOW()
		FROM UNNEST($1::bigint[], $2::text[], $3::text[], $4::text[], $5::text[],
		            $6::text[], $7::text[], $8::text[], $9::text[], $10::text[], $11::text[])
			AS t(id, tmdb_id, imdb_id, tvdb_id, plex_guid,
			     photo_path, photo_source_path, photo_thumbhash, bio, birthplace, homepage)
		WHERE people.id = t.id
		  AND (
			(COALESCE(people.tmdb_id, '') = '' AND t.tmdb_id <> '') OR
			(COALESCE(people.imdb_id, '') = '' AND t.imdb_id <> '') OR
			(COALESCE(people.tvdb_id, '') = '' AND t.tvdb_id <> '') OR
			(COALESCE(people.plex_guid, '') = '' AND t.plex_guid <> '') OR
			%[1]s OR
			(COALESCE(people.bio, '') = '' AND t.bio <> '') OR
			(COALESCE(people.birthplace, '') = '' AND t.birthplace <> '') OR
			(COALESCE(people.homepage, '') = '' AND t.homepage <> '')
		  )`, photoReplace)
}

// enrichExisting fills gaps on an existing person from p. It never rewrites a
// field the catalog already holds, and it leaves the row — including
// updated_at — untouched when there is nothing to fill.
func (r *PersonRepository) enrichExisting(ctx context.Context, id int64, p models.Person) (int64, error) {
	var setClauses []string
	var changePredicates []string
	var args []interface{}
	argIdx := 1

	// fillEmpty only sets the column when the existing DB value is empty.
	fillEmpty := func(column, value string) {
		if value == "" {
			return
		}
		setClauses = append(setClauses, fmt.Sprintf("%s = CASE WHEN COALESCE(%s, '') = '' THEN $%d ELSE %s END", column, column, argIdx, column))
		changePredicates = append(changePredicates, fmt.Sprintf("COALESCE(%s, '') = ''", column))
		args = append(args, value)
		argIdx++
	}
	// fillPhoto writes the photo triple as a unit under one decision taken on
	// photo_path. The path, its source URL, and its thumbhash describe a single
	// image: moving the path alone binds the new photo to the previous source,
	// which is what UpdatePhotoIfSourceMatches keys the cache handshake on, so
	// the finished download would land the *old* image on the row — and leave
	// the old image's thumbhash behind it.
	fillPhoto := func(person models.Person) {
		if person.PhotoPath == "" {
			return
		}
		incoming := fmt.Sprintf("$%d", argIdx)
		predicate := personPhotoReplacePredicate("photo_path", incoming)
		setClauses = append(setClauses,
			fmt.Sprintf("photo_path = CASE WHEN %s THEN %s ELSE photo_path END", predicate, incoming),
			fmt.Sprintf("photo_source_path = CASE WHEN %s THEN $%d ELSE photo_source_path END", predicate, argIdx+1),
			fmt.Sprintf("photo_thumbhash = CASE WHEN %s THEN $%d ELSE photo_thumbhash END", predicate, argIdx+2),
		)
		changePredicates = append(changePredicates, predicate)
		args = append(args, person.PhotoPath, person.PhotoSourcePath, person.PhotoThumbhash)
		argIdx += 3
	}

	fillEmpty("tmdb_id", p.TmdbID)
	fillEmpty("imdb_id", p.ImdbID)
	fillEmpty("tvdb_id", p.TvdbID)
	fillEmpty("plex_guid", p.PlexGUID)
	fillPhoto(p)
	fillEmpty("bio", p.Bio)
	fillEmpty("birthplace", p.Birthplace)
	fillEmpty("homepage", p.Homepage)

	if len(setClauses) == 0 {
		return id, nil
	}

	setClauses = append(setClauses, "updated_at = now()")
	query := fmt.Sprintf(
		"UPDATE people SET %s WHERE id = $%d AND (%s)",
		strings.Join(setClauses, ", "), argIdx, strings.Join(changePredicates, " OR "),
	)
	args = append(args, id)

	if _, err := r.pool.Exec(ctx, query, args...); err != nil {
		return 0, fmt.Errorf("enrich person %d: %w", id, err)
	}
	return id, nil
}

// BatchFindOrCreate resolves a batch of people in 5 phases: lookup by tmdb_id,
// imdb_id, and provider-safe name matching; enrich found people; insert new
// people. Returns a slice of IDs positionally matching the input. Zero values
// indicate failures.
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

	// Phase 3: Batch lookup by name (remaining). A name is not identity: only a
	// shared provider id, or two records with no provider identity at all, may
	// resolve here. This prevents unrelated namesakes from being folded together.
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
			`SELECT id, name, tmdb_id, imdb_id, tvdb_id, plex_guid
			 FROM people
			 WHERE LOWER(name) = ANY($1::text[])
			 ORDER BY id`,
			nameValues)
		if err != nil {
			return nil, fmt.Errorf("batch lookup by name: %w", err)
		}
		for rows.Next() {
			candidate := models.Person{}
			if err := rows.Scan(&candidate.ID, &candidate.Name, &candidate.TmdbID, &candidate.ImdbID, &candidate.TvdbID, &candidate.PlexGUID); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scanning name result: %w", err)
			}
			for _, ui := range nameLookup[strings.ToLower(candidate.Name)] {
				if resolved[ui] || !canResolvePersonByName(uniquePeople[ui], candidate) {
					continue
				}
				uniqueIDs[ui] = candidate.ID
				resolved[ui] = true
				toEnrich = append(toEnrich, enrichEntry{candidate.ID, uniquePeople[ui]})
			}
		}
		rows.Close()
	}

	// Phase 4: Batch enrich found people. Item-credit data fills gaps and may
	// replace a photo that was never cached; a cached artwork key is immutable
	// here. See personPhotoReplacePredicate.
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
		_, err := r.pool.Exec(ctx, batchPersonEnrichmentSQL(),
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
	return r.getByID(ctx, id, "")
}

// GetVisible retrieves a person by ID only when the viewer can see at least
// one of the person's credits, using the predicate SearchScoped applies with
// no media scope. A person the viewer cannot see answers pgx.ErrNoRows, the
// same as an unknown ID, so the answer does not reveal that the person exists.
func (r *PersonRepository) GetVisible(ctx context.Context, id int64, filter AccessFilter) (*models.Person, error) {
	var args []any
	argIdx := getByIDFirstExtraArg
	visible := personCreditVisibleSQL("people.id", nil, personCreditScope{}, filter, &args, &argIdx)
	return r.getByID(ctx, id, " AND "+visible, args...)
}

// getByIDFirstExtraArg is the first placeholder number an extraWhere passed to
// getByID may use; $1 is the person ID.
const getByIDFirstExtraArg = 2

// getByID reads the person with the given ID, narrowed by extraWhere, whose
// placeholders start at getByIDFirstExtraArg and bind extraArgs in order.
func (r *PersonRepository) getByID(ctx context.Context, id int64, extraWhere string, extraArgs ...any) (*models.Person, error) {
	args := append([]any{id}, extraArgs...)
	var p models.Person
	err := r.pool.QueryRow(ctx, `
		SELECT id, name, sort_name, bio, birth_date, death_date, birthplace, homepage,
			photo_path, photo_source_path, photo_thumbhash, tmdb_id, imdb_id, tvdb_id, plex_guid, created_at, updated_at,
			metadata_refresh_attempted_at
		FROM people WHERE id = $1`+extraWhere, args...,
	).Scan(&p.ID, &p.Name, &p.SortName, &p.Bio, &p.BirthDate, &p.DeathDate, &p.Birthplace, &p.Homepage,
		&p.PhotoPath, &p.PhotoSourcePath, &p.PhotoThumbhash, &p.TmdbID, &p.ImdbID, &p.TvdbID, &p.PlexGUID, &p.CreatedAt, &p.UpdatedAt,
		&p.MetadataRefreshAttemptedAt,
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
			photo_path, photo_source_path, photo_thumbhash, tmdb_id, imdb_id, tvdb_id, plex_guid, created_at, updated_at,
			metadata_refresh_attempted_at
		FROM people WHERE LOWER(name) = LOWER($1)`, name,
	).Scan(&p.ID, &p.Name, &p.SortName, &p.Bio, &p.BirthDate, &p.DeathDate, &p.Birthplace, &p.Homepage,
		&p.PhotoPath, &p.PhotoSourcePath, &p.PhotoThumbhash, &p.TmdbID, &p.ImdbID, &p.TvdbID, &p.PlexGUID, &p.CreatedAt, &p.UpdatedAt,
		&p.MetadataRefreshAttemptedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("get person by name %q: %w", name, err)
	}
	return &p, nil
}

// SearchAlphabetical finds persons by name substring (case-insensitive), ordered by
// name, among the people the viewer can see through at least one credit (the
// predicate SearchScoped and GetVisible apply with no media scope). It keeps
// the v1 bridge search's untrimmed query and alphabetical order; only the
// visibility filter is added.
//
// The predicate is `name ILIKE '%term%'` rather than `LOWER(name) LIKE ...` so
// the pg_trgm GIN index idx_people_name_trgm can serve it: for a rare 3+ char
// term that index turns a ~300-400ms full name-index scan into a ~10ms bitmap
// scan. ILIKE is itself case-insensitive, so this stays equivalent to the prior
// LOWER(name) comparison (including its existing treatment of % and _ in the
// term as LIKE wildcards).
func (r *PersonRepository) SearchAlphabetical(ctx context.Context, query string, limit int, filter AccessFilter) ([]models.Person, error) {
	return r.search(ctx, query, limit, "", filter, false)
}

// SearchScoped ranks exact names first and restricts people to credits in the
// selected media scope and viewer access before applying the limit. Empty scope
// includes accessible credits across all media types. Every query word must
// start a word of the name, so "hacks" finds "Lark Hackshaw" but not
// "Chad Thackston", while a partial last word still serves typeahead.
func (r *PersonRepository) SearchScoped(ctx context.Context, query string, limit int, mediaScope string, filter AccessFilter) ([]models.Person, error) {
	return r.search(ctx, strings.TrimSpace(query), limit, mediaScope, filter, true)
}

// search runs a name search over the people the viewer can see. scoped
// selects the v2 rules: word-start matching with exact names first. Without
// it the query matches anywhere in the name, as the v1 bridge search does.
func (r *PersonRepository) search(ctx context.Context, query string, limit int, mediaScope string, filter AccessFilter, scoped bool) ([]models.Person, error) {
	if limit <= 0 {
		limit = 20
	}
	// The query binds only where the SQL reads it: Postgres cannot type an
	// unreferenced parameter, which a scoped empty query would leave.
	args := []any{limit}
	argIdx := 2
	var conditions []string
	if scoped {
		conditions = personNameWordStartConditions(query, &args, &argIdx)
	} else {
		conditions = []string{fmt.Sprintf("name ILIKE '%%' || $%d || '%%'", argIdx)}
		args = append(args, query)
		argIdx++
	}
	conditions = append(conditions, personCreditVisibleSQL("people.id", MediaScopeItemTypes(mediaScope), personCreditScope{}, filter, &args, &argIdx))
	where := strings.Join(conditions, " AND ")
	order := "name ASC, id ASC"
	if scoped && query != "" {
		// Words match however they are spaced, so a name equal to the query
		// as typed or with its whitespace collapsed counts as exact.
		order = fmt.Sprintf("(LOWER(name) IN (LOWER($%d), LOWER($%d))) DESC, ", argIdx, argIdx+1) + order
		args = append(args, query, strings.Join(strings.Fields(query), " "))
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, name, sort_name, bio, birth_date, death_date, birthplace, homepage,
			photo_path, photo_source_path, photo_thumbhash, tmdb_id, imdb_id, tvdb_id, plex_guid, created_at, updated_at,
			metadata_refresh_attempted_at
		FROM people WHERE `+where+` ORDER BY `+order+` LIMIT $1`, args...,
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
			&p.MetadataRefreshAttemptedAt,
		); err != nil {
			return nil, fmt.Errorf("scan person: %w", err)
		}
		people = append(people, p)
	}
	return people, rows.Err()
}

// maxPersonSearchWords caps the distinct query words people search matches
// on, so a long query cannot add predicates without bound.
const maxPersonSearchWords = 8

// personNameWordStartConditions matches people whose name has, for every
// distinct whitespace-separated word of query, a word starting with it. A word
// starts at the beginning of the name or after a character that is not a
// letter or digit, so "luc" finds "Jean-Luc" and "brien" finds "O'Brien"; a
// query word that opens with punctuation, like "'brien", carries its own
// boundary. Each word also carries a substring ILIKE, which lets
// idx_people_name_trgm narrow the rows the regex checks. Both predicates leave
// case folding to the database: ~* folds one character at a time and Go's
// lowercasing ignores the collation, so either would drop matches ILIKE keeps
// (a name spelled "GROẞ" for "groß", a Turkish dotted I). An empty query adds
// no conditions.
func personNameWordStartConditions(query string, args *[]any, argIdx *int) []string {
	var conditions []string
	seen := map[string]bool{}
	for _, word := range strings.Fields(query) {
		// Only identical words repeat: case pairs depend on the collation.
		if seen[word] {
			continue
		}
		if len(seen) == maxPersonSearchWords {
			break
		}
		seen[word] = true
		pattern := escapeRegexLiteral(word)
		if first, _ := utf8.DecodeRuneInString(word); unicode.IsLetter(first) || unicode.IsDigit(first) {
			pattern = "(^|[^[:alnum:]])" + pattern
		}
		conditions = append(conditions,
			fmt.Sprintf(`name ILIKE $%d ESCAPE '\'`, *argIdx),
			fmt.Sprintf("LOWER(name) ~ LOWER($%d)", *argIdx+1))
		*args = append(*args, "%"+escapeLikeLiteral(word)+"%", pattern)
		*argIdx += 2
	}
	return conditions
}

// personCreditVisibleSQL renders an EXISTS predicate that holds when the person
// at personIDExpr has at least one credit the viewer can see, limited to
// credits on items of the given types when types is non-empty and to credits
// inside scope. People search, person detail and the Jellyfin person reads
// share it so a person found by one opens in the others.
func personCreditVisibleSQL(personIDExpr string, types []string, scope personCreditScope, filter AccessFilter, args *[]any, argIdx *int) string {
	conditions := []string{"ip.person_id = " + personIDExpr}
	if len(types) > 0 {
		conditions = append(conditions, fmt.Sprintf("mi.type = ANY($%d::text[])", *argIdx))
		*args = append(*args, types)
		*argIdx = *argIdx + 1
	}
	// Episode access follows the parent series, while scope and excluded
	// media types describe the credited item itself.
	appendLibraryAccessConditions("access_item.content_id", filter, &conditions, args, argIdx)
	applyAccessFilter("access_item", AccessFilter{MaturityLimits: filter.MaturityLimits}, &conditions, args, argIdx)
	applyAccessFilter("mi", AccessFilter{ExcludedMediaTypes: filter.ExcludedMediaTypes}, &conditions, args, argIdx)
	if scope.LibraryID > 0 {
		conditions = append(conditions, fmt.Sprintf("EXISTS (SELECT 1 FROM media_item_libraries scope_mil WHERE scope_mil.content_id = access_item.content_id AND scope_mil.media_folder_id = $%d)", *argIdx))
		*args = append(*args, scope.LibraryID)
		*argIdx = *argIdx + 1
	}
	if scope.ContentID != "" {
		// The credits on the item and, for a series, on its episodes: the
		// same rows as matching the credited or the access item. Resolving
		// them up front lets the lookup start from item_people's content_id
		// index instead of joining every credit to its access item first.
		conditions = append(conditions, fmt.Sprintf("ip.content_id = ANY(ARRAY(SELECT $%d::text UNION ALL SELECT scope_e.content_id FROM episodes scope_e WHERE scope_e.series_id = $%d))", *argIdx, *argIdx))
		*args = append(*args, scope.ContentID)
		*argIdx = *argIdx + 1
	}
	return `EXISTS (
			SELECT 1 FROM item_people ip
			JOIN media_items mi ON mi.content_id = ip.content_id
			JOIN media_items access_item ON access_item.content_id = CASE
				WHEN mi.type = 'episode' THEN ` + episodeParentSeriesIDExpr("mi.content_id") + `
				ELSE mi.content_id END
			WHERE ` + strings.Join(conditions, " AND ") + ")"
}

// personCreditScope narrows the credits personCreditVisibleSQL counts to one
// library or one item; the zero value counts every credit. Both match the item
// that carries access, which for an episode credit is the parent series, so a
// guest star belongs to the series' libraries. ContentID also matches the
// credited item itself: a series scope covers its episodes' guest stars, and
// an episode scope covers that episode's own credits.
type personCreditScope struct {
	LibraryID int
	ContentID string
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
	_, err := r.update(ctx, p, nil)
	return err
}

// ErrPersonIdentityChanged reports that a person's provider ids are no longer
// the ones a metadata lookup asked about.
var ErrPersonIdentityChanged = errors.New("person's provider ids changed")

// UpdateRefreshed is Update for a metadata refresh's result: it writes p only
// while the person still has the identity the lookup asked about, checked
// under the write's row lock. An admin's correction made during the lookup
// wins, and ErrPersonIdentityChanged is returned. It returns the identity it
// stored, which resolving an id conflict can change from p's.
func (r *PersonRepository) UpdateRefreshed(ctx context.Context, p models.Person, lookedUp PersonIdentity) (PersonIdentity, error) {
	return r.update(ctx, p, &lookedUp)
}

// update writes p, retrying as a whole when Postgres picks it as a deadlock
// victim: two writes that each take the other's id wait on each other in
// the unique index, and the retry finds the survivor's committed state.
func (r *PersonRepository) update(ctx context.Context, p models.Person, guard *PersonIdentity) (PersonIdentity, error) {
	var stored PersonIdentity
	err := retryOnDeadlock(ctx, func() error {
		err := r.applyUpdate(ctx, p, guard)
		if err == nil {
			stored = PersonIdentityOf(p)
			return nil
		}
		if !isDuplicateKeyError(err) {
			return err
		}
		stored, err = r.updateResolvingConflicts(ctx, p, guard)
		return err
	})
	if err != nil {
		return PersonIdentity{}, err
	}
	return stored, nil
}

// missedPersonUpdate explains a person write that matched no row: the person
// is gone (pgx.ErrNoRows), or, under a guard, their provider ids changed.
// The guard itself is in the write's WHERE clause, so it is checked
// atomically with the write and takes no lock ahead of the conflict
// resolver's ordered ones.
func missedPersonUpdate(ctx context.Context, tx pgx.Tx, id int64, guard *PersonIdentity) error {
	if guard == nil {
		return pgx.ErrNoRows
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM people WHERE id = $1)`, id).Scan(&exists); err != nil {
		return fmt.Errorf("check person %d: %w", id, err)
	}
	if exists {
		return ErrPersonIdentityChanged
	}
	return pgx.ErrNoRows
}

// applyUpdate writes all non-key fields on a person and enqueues a search
// reindex for the items they appear in, all in one transaction.
func (r *PersonRepository) applyUpdate(ctx context.Context, p models.Person, guard *PersonIdentity) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin person update tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	affected, err := execPersonUpdate(ctx, tx, p, guard)
	if err != nil {
		return err
	}
	if affected == 0 {
		// The row was deleted out from under us (e.g. merged away
		// concurrently), or its ids no longer match the guard.
		return missedPersonUpdate(ctx, tx, p.ID, guard)
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
// lands. Bounded by maxPersonConflictResolutions so it cannot spin. It
// returns the identity it stored: resolving a conflict can restore an id or
// fold in a merged person's.
func (r *PersonRepository) updateResolvingConflicts(ctx context.Context, p models.Person, guard *PersonIdentity) (PersonIdentity, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return PersonIdentity{}, fmt.Errorf("begin person merge tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var affected int64
	for attempt := 0; ; attempt++ {
		affected, err = tryPersonUpdate(ctx, tx, p, guard)
		if err == nil {
			break
		}
		if !isDuplicateKeyError(err) || attempt >= maxPersonConflictResolutions {
			return PersonIdentity{}, err
		}
		field, value, ok := conflictingExternalID(extractConstraint(err), p)
		if !ok {
			// Unique violation on a constraint we do not know how to reconcile.
			return PersonIdentity{}, err
		}
		resolved, resolveErr := r.resolveExternalIDConflict(ctx, tx, &p, field, value)
		if resolveErr != nil {
			return PersonIdentity{}, resolveErr
		}
		if !resolved {
			return PersonIdentity{}, err
		}
	}

	if affected == 0 {
		// Survivor was deleted concurrently (e.g. merged into another row), so
		// there is nothing left to persist for this id, or its ids no longer
		// match the guard. A merge changes only p before the retried write,
		// never the survivor's stored ids, so the guard holds across retries.
		return PersonIdentity{}, missedPersonUpdate(ctx, tx, p.ID, guard)
	}
	if err := reindexPersonItems(ctx, tx, p.ID); err != nil {
		return PersonIdentity{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PersonIdentity{}, fmt.Errorf("commit person merge tx: %w", err)
	}
	return PersonIdentityOf(p), nil
}

// execPersonUpdate writes all non-key person fields and returns the rows affected.
// execPersonUpdate writes a person's fields. When their provider ids change,
// the last lookup's outcome was about another identity: it is cleared and the
// person is due now, so one the providers didn't know under the old id isn't
// left out of the sweep for good. The attempt time stays, so the person isn't
// taken for one never looked up. The SET list reads the row's old ids. With a
// guard the write matches only while the row still has the guarded ids.
func execPersonUpdate(ctx context.Context, tx pgx.Tx, p models.Person, guard *PersonIdentity) (int64, error) {
	var g PersonIdentity
	if guard != nil {
		g = *guard
	}
	tag, err := tx.Exec(ctx, `
		UPDATE people SET name=$2, sort_name=$3, bio=$4, birth_date=$5, death_date=$6,
			birthplace=$7, homepage=$8, photo_path=$9, photo_source_path=$10, photo_thumbhash=$11,
			tmdb_id=$12, imdb_id=$13, tvdb_id=$14, plex_guid=$15, updated_at=now(),
			metadata_refresh_outcome = CASE WHEN (tmdb_id, imdb_id, tvdb_id) IS DISTINCT FROM ($12, $13, $14)
				THEN NULL ELSE metadata_refresh_outcome END,
			metadata_refresh_failures = CASE WHEN (tmdb_id, imdb_id, tvdb_id) IS DISTINCT FROM ($12, $13, $14)
				THEN 0 ELSE metadata_refresh_failures END,
			metadata_refresh_due_at = CASE WHEN (tmdb_id, imdb_id, tvdb_id) IS DISTINCT FROM ($12, $13, $14)
				THEN NOW() ELSE metadata_refresh_due_at END
		WHERE id = $1
			AND (NOT $16::boolean OR (tmdb_id IS NOT DISTINCT FROM $17
				AND imdb_id IS NOT DISTINCT FROM $18
				AND tvdb_id IS NOT DISTINCT FROM $19))`,
		p.ID, p.Name, p.SortName, p.Bio, p.BirthDate, p.DeathDate,
		p.Birthplace, p.Homepage, p.PhotoPath, p.PhotoSourcePath, p.PhotoThumbhash,
		p.TmdbID, p.ImdbID, p.TvdbID, p.PlexGUID,
		guard != nil, g.TmdbID, g.ImdbID, g.TvdbID,
	)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// tryPersonUpdate runs execPersonUpdate inside a savepoint so a unique violation
// rolls back only the attempt, leaving the surrounding transaction usable.
func tryPersonUpdate(ctx context.Context, tx pgx.Tx, p models.Person, guard *PersonIdentity) (int64, error) {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin person update savepoint: %w", err)
	}
	affected, err := execPersonUpdate(ctx, sp, p, guard)
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
		slog.WarnContext(ctx, "person merge: declining to merge, preserving survivor's existing id", "component", "catalog",
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

	slog.InfoContext(ctx, "person merge: folded duplicate person", "component", "catalog",
		"survivor_id", p.ID, "merged_id", partnerID, "field", field, "value", value)
	return true, nil
}

// scanPeopleByIDsForUpdate loads the given people, locking them FOR UPDATE in
// ascending id order so concurrent merges acquire locks consistently.
func scanPeopleByIDsForUpdate(ctx context.Context, tx pgx.Tx, ids ...int64) (map[int64]models.Person, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, name, sort_name, bio, birth_date, death_date, birthplace, homepage,
			photo_path, photo_source_path, photo_thumbhash, tmdb_id, imdb_id, tvdb_id, plex_guid, created_at, updated_at,
			metadata_refresh_attempted_at
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
			&p.MetadataRefreshAttemptedAt,
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

// canResolvePersonByName permits the legacy name fallback only when it has an
// identity signal stronger than the name itself. A shared provider id proves
// the rows refer to the same provider entity; two entirely unidentified rows
// may also reuse one name-only placeholder. Disjoint provider identities never
// match merely because two people have the same name.
func canResolvePersonByName(a, b models.Person) bool {
	if !personNamesMatch(a.Name, b.Name) || !externalIDsCompatible(a, b) {
		return false
	}
	if !hasExternalPersonID(a) && !hasExternalPersonID(b) {
		return true
	}
	return sharedExternalPersonID(a, b)
}

func hasExternalPersonID(p models.Person) bool {
	return p.TmdbID != "" || p.ImdbID != "" || p.TvdbID != "" || p.PlexGUID != ""
}

func sharedExternalPersonID(a, b models.Person) bool {
	return sharedNonEmptyID(a.TmdbID, b.TmdbID) ||
		sharedNonEmptyID(a.ImdbID, b.ImdbID) ||
		sharedNonEmptyID(a.TvdbID, b.TvdbID) ||
		sharedNonEmptyID(a.PlexGUID, b.PlexGUID)
}

func sharedNonEmptyID(x, y string) bool {
	return x != "" && x == y
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

// MarkRefreshAttempt records that a provider lookup is starting, without
// changing the person's metadata timestamp. The lookup's outcome replaces the
// short due time set here; if the process dies before recording it, the sweep
// tries the person again once PersonRefreshAttemptLease has passed.
func (r *PersonRepository) MarkRefreshAttempt(ctx context.Context, id int64) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE people
		SET metadata_refresh_attempted_at = NOW(),
			metadata_refresh_due_at = NOW() + make_interval(secs => $2)
		WHERE id = $1`, id, PersonRefreshAttemptLease.Seconds())
	if err != nil {
		return fmt.Errorf("mark person %d refresh attempt: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// PersonRefreshOutcome is what a provider lookup for a person found.
type PersonRefreshOutcome string

const (
	// PersonRefreshAnswered means a provider returned the person, whichever
	// fields it filled. An empty bio is an answer, not a gap to retry.
	PersonRefreshAnswered PersonRefreshOutcome = "answered"
	// PersonRefreshNotFound means every provider consulted reported the person
	// missing or had nothing for them.
	PersonRefreshNotFound PersonRefreshOutcome = "not_found"
	// PersonRefreshFailed means a provider errored or timed out, or none was
	// available, so the lookup learned nothing about the person.
	PersonRefreshFailed PersonRefreshOutcome = "failed"
)

// PersonIdentity is the provider ids a lookup asked about. An outcome
// recorded for an identity the person no longer has is stale.
type PersonIdentity struct {
	TmdbID, ImdbID, TvdbID string
}

// PersonIdentityOf is the identity a lookup of p asks about.
func PersonIdentityOf(p models.Person) PersonIdentity {
	return PersonIdentity{TmdbID: p.TmdbID, ImdbID: p.ImdbID, TvdbID: p.TvdbID}
}

// RecordRefreshOutcome stores a lookup's outcome and when the sweep may look
// the person up again. An answer is trusted for PersonMetadataStaleAfter.
// metadata_refresh_failures counts consecutive lookups with the outcome just
// recorded; an answer resets it to 0, and a different unanswered outcome
// starts it again at 1. Failures back off from PersonRefreshFailureBackoff,
// doubling up to PersonRefreshRetryAfter. A person the providers do not know
// is retried every PersonRefreshRetryAfter until PersonRefreshNotFoundAttempts
// not-found lookups in a row, then only on demand, so failures and
// unavailable providers never use up that budget.
//
// The outcome is written only while the person still has the identity the
// lookup asked about: a lookup that started before an admin corrected the
// person's ids says nothing about the new ones, and it must not undo the
// reset the correction made (see execPersonUpdate). Such a write matches no
// row and returns pgx.ErrNoRows. Recording an outcome also stamps a missing
// attempt time, so a lookup whose attempt mark failed isn't taken for one
// never made.
func (r *PersonRepository) RecordRefreshOutcome(ctx context.Context, id int64, identity PersonIdentity, outcome PersonRefreshOutcome) error {
	switch outcome {
	case PersonRefreshAnswered, PersonRefreshNotFound, PersonRefreshFailed:
	default:
		return fmt.Errorf("record person %d refresh outcome: unknown outcome %q", id, outcome)
	}
	// Every SET expression reads the row being updated, which Postgres
	// re-reads after waiting on a concurrent writer's lock, so two outcomes
	// recorded at once both count. The SET list sees the old column values.
	tag, err := r.pool.Exec(ctx, `
		UPDATE people
		SET metadata_refresh_attempted_at = COALESCE(metadata_refresh_attempted_at, NOW()),
			metadata_refresh_outcome = $2,
			metadata_refresh_failures = `+personRefreshStreak+`,
			metadata_refresh_due_at = CASE
				WHEN $2 = 'answered' THEN NOW() + make_interval(secs => $3)
				WHEN $2 = 'not_found' AND `+personRefreshStreak+` >= $4 THEN NULL
				WHEN $2 = 'not_found' THEN NOW() + make_interval(secs => $5)
				ELSE NOW() + make_interval(secs => LEAST($6 * power(2, LEAST(`+personRefreshStreak+` - 1, 30)), $5))
			END
		WHERE id = $1
			AND tmdb_id IS NOT DISTINCT FROM $7
			AND imdb_id IS NOT DISTINCT FROM $8
			AND tvdb_id IS NOT DISTINCT FROM $9`,
		id,
		string(outcome),
		PersonMetadataStaleAfter.Seconds(),
		PersonRefreshNotFoundAttempts,
		PersonRefreshRetryAfter.Seconds(),
		PersonRefreshFailureBackoff.Seconds(),
		identity.TmdbID,
		identity.ImdbID,
		identity.TvdbID,
	)
	if err != nil {
		return fmt.Errorf("record person %d refresh outcome: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// personRefreshStreak is the SQL for a person's streak after recording outcome
// $2, computed from the row's current outcome and count.
const personRefreshStreak = `(CASE
	WHEN $2 = 'answered' THEN 0
	WHEN metadata_refresh_outcome IS DISTINCT FROM $2 THEN 1
	ELSE metadata_refresh_failures + 1
END)`

// Person metadata refresh policy. The sweep (FindRefreshCandidates) follows
// each person's recorded outcome; a detail-page view (PersonRefreshDue) looks
// a person up on demand at most once per PersonRefreshRetryAfter.
const (
	// PersonMetadataStaleAfter is how long an answered lookup, or never-looked-up
	// complete metadata, is trusted before the sweep looks the person up again.
	PersonMetadataStaleAfter = 90 * 24 * time.Hour

	// PersonRefreshRetryAfter bounds how often a detail-page view sends one
	// person to a provider, and is the longest wait between unanswered sweep
	// lookups.
	PersonRefreshRetryAfter = 7 * 24 * time.Hour

	// PersonRefreshFailureBackoff is the sweep's wait after a first failed
	// lookup. It doubles with each consecutive failure, up to
	// PersonRefreshRetryAfter.
	PersonRefreshFailureBackoff = time.Hour

	// PersonRefreshNotFoundAttempts is how many consecutive unanswered lookups
	// the sweep makes for a person before it stops looking them up.
	PersonRefreshNotFoundAttempts = 3

	// PersonRefreshAttemptLease is how long the sweep waits before retrying a
	// person whose lookup started but never recorded an outcome.
	PersonRefreshAttemptLease = time.Hour
)

// PersonMetadataIncomplete reports whether a provider could still fill in
// metadata Silo does not have. A photo_path of "-" is the "provider has no
// photo" sentinel — an answer, not a gap — so it counts as complete, matching
// the SQL predicate in FindRefreshCandidates.
func PersonMetadataIncomplete(person models.Person) bool {
	return person.Bio == "" || person.PhotoPath == "" || person.BirthDate == nil
}

// PersonRefreshDue reports whether a detail-page view should queue a provider
// lookup: the person carries an external id, no lookup has been attempted
// within PersonRefreshRetryAfter, and their metadata is either incomplete or
// older than PersonMetadataStaleAfter. The background sweep does not use this
// rule; it follows each person's recorded outcome (FindRefreshCandidates).
func PersonRefreshDue(person models.Person, now time.Time) bool {
	if person.TmdbID == "" && person.ImdbID == "" && person.TvdbID == "" {
		return false
	}
	if person.MetadataRefreshAttemptedAt != nil &&
		!person.MetadataRefreshAttemptedAt.Before(now.Add(-PersonRefreshRetryAfter)) {
		return false
	}
	return PersonMetadataIncomplete(person) ||
		person.UpdatedAt.Before(now.Add(-PersonMetadataStaleAfter))
}

// FindRefreshCandidates returns up to limit people due for a background
// provider lookup: first people never looked up whose metadata is incomplete
// or stale, newest first, so the cast of a new scan goes next; then people
// whose recorded outcome made their next lookup due, earliest due first.
func (r *PersonRepository) FindRefreshCandidates(ctx context.Context, limit int) ([]int64, error) {
	if limit <= 0 {
		return []int64{}, nil
	}

	ids := make([]int64, 0, limit)
	queries := []struct {
		name string
		sql  string
		args func(remaining int) []any
	}{
		{
			name: "never looked up",
			sql: `
				SELECT id
				FROM people
				WHERE metadata_refresh_attempted_at IS NULL
					AND (tmdb_id <> '' OR imdb_id <> '' OR tvdb_id <> '')
					AND (
						COALESCE(bio, '') = ''
						OR COALESCE(photo_path, '') = ''
						OR birth_date IS NULL
						OR updated_at < $2
					)
				ORDER BY id DESC
				LIMIT $1`,
			args: func(remaining int) []any {
				return []any{remaining, time.Now().Add(-PersonMetadataStaleAfter)}
			},
		},
		{
			name: "due again",
			sql: `
				SELECT id
				FROM people
				WHERE metadata_refresh_due_at IS NOT NULL
					AND metadata_refresh_due_at <= NOW()
					AND (tmdb_id <> '' OR imdb_id <> '' OR tvdb_id <> '')
				ORDER BY metadata_refresh_due_at, id
				LIMIT $1`,
			args: func(remaining int) []any { return []any{remaining} },
		},
		{
			// An older API server, during a rolling upgrade, records an
			// attempt with no outcome or due time. Retry those as it would
			// have, PersonRefreshRetryAfter after the attempt.
			name: "attempted without an outcome",
			sql: `
				SELECT id
				FROM people
				WHERE metadata_refresh_attempted_at IS NOT NULL
					AND metadata_refresh_outcome IS NULL
					AND metadata_refresh_due_at IS NULL
					AND metadata_refresh_attempted_at <= $2
					AND (tmdb_id <> '' OR imdb_id <> '' OR tvdb_id <> '')
				ORDER BY metadata_refresh_attempted_at, id
				LIMIT $1`,
			args: func(remaining int) []any {
				return []any{remaining, time.Now().Add(-PersonRefreshRetryAfter)}
			},
		},
	}
	for _, query := range queries {
		remaining := limit - len(ids)
		if remaining <= 0 {
			break
		}
		rows, err := r.pool.Query(ctx, query.sql, query.args(remaining)...)
		if err != nil {
			return nil, fmt.Errorf("query %s refresh candidates: %w", query.name, err)
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan %s refresh candidate: %w", query.name, err)
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate %s refresh candidates: %w", query.name, err)
		}
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

// SearchVisible restricts both the page and total to people the viewer can see
// through a credit on a video item. A hidden library's credits never enter the
// result set.
func (r *PersonRepository) SearchVisible(ctx context.Context, term string, exact bool, limit, offset int, filter AccessFilter, includeTotal bool) ([]models.Person, int, error) {
	return r.SearchVisibleWithOptions(ctx, PersonSearchOptions{Term: term, Exact: exact, Limit: limit, Offset: offset, Filter: filter, IncludeTotal: includeTotal})
}

// videoCreditTypes are the credited item types the Jellyfin person reads
// (SearchVisibleWithOptions, EnsureAccessible) count. The Jellyfin surface
// lists only these types, so a credit on any other item opens nothing there.
var videoCreditTypes = []string{playableTypeMovie, playableTypeSeries, browseTypeEpisode}

// PersonSearchOptions narrows SearchVisibleWithOptions. Name bounds compare
// lowercased names, as Jellyfin's people query does; LibraryID and
// ContentID keep only people credited on items in that library or on that
// item, where an episode credit belongs to its series (see personCreditScope).
type PersonSearchOptions struct {
	Term                    string
	Exact                   bool
	NameStartsWith          string
	NameLessThan            string
	NameStartsWithOrGreater string
	LibraryID               int
	ContentID               string
	Limit                   int
	Offset                  int
	Filter                  AccessFilter
	IncludeTotal            bool
}

// SearchVisibleWithOptions lists the people the viewer can see through a
// credit on a video item, ordered by name. Visibility is the person-detail
// rule (GetVisible), narrowed to videoCreditTypes, so an episode credit counts
// when the viewer can see the parent series.
func (r *PersonRepository) SearchVisibleWithOptions(ctx context.Context, opts PersonSearchOptions) ([]models.Person, int, error) {
	args := []any{}
	argIdx := 1
	scope := personCreditScope{LibraryID: opts.LibraryID, ContentID: opts.ContentID}
	visible := personCreditVisibleSQL("p.id", videoCreditTypes, scope, opts.Filter, &args, &argIdx)
	personConditions := []string{}
	addName := func(clause, value string) {
		personConditions = append(personConditions, fmt.Sprintf(clause, argIdx))
		args = append(args, value)
		argIdx++
	}
	switch {
	case opts.Exact:
		addName("LOWER(p.name) = LOWER($%d)", opts.Term)
	case opts.Term != "":
		addName("p.name ILIKE '%%' || $%d || '%%'", opts.Term)
	}
	if opts.NameStartsWith != "" {
		addName("LOWER(p.name) LIKE $%d ESCAPE '\\'", likePrefixPattern(opts.NameStartsWith))
	}
	if opts.NameLessThan != "" {
		addName("LOWER(p.name) < LOWER($%d)", opts.NameLessThan)
	}
	if opts.NameStartsWithOrGreater != "" {
		addName("LOWER(p.name) >= LOWER($%d)", opts.NameStartsWithOrGreater)
	}
	personConditions = append(personConditions, visible)
	where := strings.Join(personConditions, " AND ")
	limit, offset, includeTotal := opts.Limit, opts.Offset, opts.IncludeTotal
	var total int
	if includeTotal {
		if err := r.pool.QueryRow(ctx, "SELECT COUNT(*) FROM people p WHERE "+where, args...).Scan(&total); err != nil {
			return nil, 0, err
		}
	}
	args = append(args, max(limit, 0), max(offset, 0))
	query := `SELECT p.id, p.name, p.sort_name, p.bio, p.birth_date, p.death_date, p.birthplace, p.homepage,
	p.photo_path, p.photo_source_path, p.photo_thumbhash, p.tmdb_id, p.imdb_id, p.tvdb_id, p.plex_guid, p.created_at, p.updated_at, p.metadata_refresh_attempted_at
	FROM people p WHERE ` + where + fmt.Sprintf(" ORDER BY p.name, p.id LIMIT $%d OFFSET $%d", argIdx, argIdx+1)
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	people := []models.Person{}
	for rows.Next() {
		var p models.Person
		if err := rows.Scan(&p.ID, &p.Name, &p.SortName, &p.Bio, &p.BirthDate, &p.DeathDate, &p.Birthplace, &p.Homepage, &p.PhotoPath, &p.PhotoSourcePath, &p.PhotoThumbhash, &p.TmdbID, &p.ImdbID, &p.TvdbID, &p.PlexGUID, &p.CreatedAt, &p.UpdatedAt, &p.MetadataRefreshAttemptedAt); err != nil {
			return nil, 0, err
		}
		people = append(people, p)
	}
	return people, total, rows.Err()
}

// EnsureAccessible requires a credit on a visible video item before serving
// person metadata or image bytes, including already cached derivatives. It
// applies the SearchVisibleWithOptions rule, so a person the Jellyfin people
// list shows also serves their photo.
func (r *PersonRepository) EnsureAccessible(ctx context.Context, id int64, filter AccessFilter) error {
	args := []any{id}
	index := 2
	visible := personCreditVisibleSQL("$1", videoCreditTypes, personCreditScope{}, filter, &args, &index)
	var exists bool
	if err := r.pool.QueryRow(ctx, "SELECT "+visible, args...).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return pgx.ErrNoRows
	}
	return nil
}
