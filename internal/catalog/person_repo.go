package catalog

import (
	"context"
	"fmt"
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
func (r *PersonRepository) Search(ctx context.Context, query string, limit int) ([]models.Person, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, name, sort_name, bio, birth_date, death_date, birthplace, homepage,
			photo_path, photo_source_path, photo_thumbhash, tmdb_id, imdb_id, tvdb_id, plex_guid, created_at, updated_at
		FROM people WHERE LOWER(name) LIKE '%' || LOWER($1) || '%'
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

// Update updates all non-key fields on a person.
func (r *PersonRepository) Update(ctx context.Context, p models.Person) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE people SET name=$2, sort_name=$3, bio=$4, birth_date=$5, death_date=$6,
			birthplace=$7, homepage=$8, photo_path=$9, photo_source_path=$10, photo_thumbhash=$11,
			tmdb_id=$12, imdb_id=$13, tvdb_id=$14, plex_guid=$15, updated_at=now()
		WHERE id = $1`,
		p.ID, p.Name, p.SortName, p.Bio, p.BirthDate, p.DeathDate,
		p.Birthplace, p.Homepage, p.PhotoPath, p.PhotoSourcePath, p.PhotoThumbhash,
		p.TmdbID, p.ImdbID, p.TvdbID, p.PlexGUID,
	)
	return err
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
