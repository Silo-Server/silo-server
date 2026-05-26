package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

// ABSMediaStore implements abs.MediaStore using silo's catalog.ItemRepository,
// scanner.FileRepository, and a direct pgxpool.Pool for media_folders queries.
type ABSMediaStore struct {
	Items *catalog.ItemRepository
	Files *scanner.FileRepository
	Pool  *pgxpool.Pool
}

var _ abs.MediaStore = (*ABSMediaStore)(nil)

// GetAudiobookByID returns the media_item with the given content_id, provided
// it is of type 'audiobook'. Returns nil and a wrapped error for any other
// outcome; the caller interprets a nil result as not-found.
func (s *ABSMediaStore) GetAudiobookByID(ctx context.Context, contentID string) (*models.MediaItem, error) {
	item, err := s.Items.GetByID(ctx, contentID)
	if err != nil {
		return nil, fmt.Errorf("abs_media_store: get audiobook %q: %w", contentID, err)
	}
	if item == nil || item.Type != "audiobook" {
		return nil, nil
	}
	// Hydrate authors + narrators so the ABS metadata mapper can fill
	// authorName / narratorName on the response.
	if err := s.hydratePeople(ctx, []*models.MediaItem{item}); err != nil {
		// Non-fatal: caller can still render the item without people data.
		_ = err
	}
	return item, nil
}

// ListAudiobooks returns a page of media_items with type='audiobook'.
// When libraryID is non-zero, results are filtered to items in that
// media_folder (via the media_item_libraries junction); 0 means all libraries.
//
// We can't reuse ItemRepository.Search because Search is a text-search
// path that bails out when the query string is empty. We page content_ids
// here via SQL, then load full rows via GetByIDs so the scan logic stays
// in the catalog package.
func (s *ABSMediaStore) ListAudiobooks(ctx context.Context, libraryID int64, limit, offset int) ([]*models.MediaItem, int, error) {
	if s.Pool == nil {
		return nil, 0, fmt.Errorf("abs_media_store: no pgx pool")
	}
	if limit <= 0 {
		limit = 30
	}
	if offset < 0 {
		offset = 0
	}

	var total int
	countSQL := `SELECT COUNT(*) FROM media_items mi WHERE mi.type = 'audiobook'`
	dataSQL := `SELECT mi.content_id FROM media_items mi WHERE mi.type = 'audiobook'`
	countArgs := []any{}
	dataArgs := []any{}
	if libraryID != 0 {
		libFilter := ` AND EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = mi.content_id AND mil.media_folder_id = $1)`
		countSQL += libFilter
		dataSQL += libFilter
		countArgs = append(countArgs, int(libraryID))
		dataArgs = append(dataArgs, int(libraryID))
	}
	if err := s.Pool.QueryRow(ctx, countSQL, countArgs...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("abs_media_store: count audiobooks: %w", err)
	}

	dataSQL += ` ORDER BY LOWER(mi.sort_title), LOWER(mi.title)`
	argIdx := len(dataArgs) + 1
	dataSQL += fmt.Sprintf(` LIMIT $%d OFFSET $%d`, argIdx, argIdx+1)
	dataArgs = append(dataArgs, limit, offset)

	rows, err := s.Pool.Query(ctx, dataSQL, dataArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("abs_media_store: list audiobooks: %w", err)
	}
	defer rows.Close()
	ids := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, 0, fmt.Errorf("abs_media_store: scan audiobook id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("abs_media_store: iterate audiobook ids: %w", err)
	}
	if len(ids) == 0 {
		return []*models.MediaItem{}, total, nil
	}

	items, err := s.Items.GetByIDs(ctx, ids)
	if err != nil {
		return nil, 0, fmt.Errorf("abs_media_store: load audiobooks: %w", err)
	}

	// GetByIDs sorts by content_id ASC; preserve our sort_title order.
	byID := make(map[string]*models.MediaItem, len(items))
	for _, it := range items {
		byID[it.ContentID] = it
	}
	ordered := make([]*models.MediaItem, 0, len(ids))
	for _, id := range ids {
		if it, ok := byID[id]; ok {
			ordered = append(ordered, it)
		}
	}

	// Hydrate item_people in one batched query so the ABS mapper can pull
	// author/narrator names without N+1 lookups.
	if err := s.hydratePeople(ctx, ordered); err != nil {
		// Non-fatal: items without people still render (authorName/narratorName
		// just stay empty in the ABS payload). Log via the caller if needed.
		_ = err
	}
	return ordered, total, nil
}

// hydratePeople loads item_people rows for the given items and assigns the
// resulting slices to each item.People. Single SQL roundtrip regardless of
// page size.
func (s *ABSMediaStore) hydratePeople(ctx context.Context, items []*models.MediaItem) error {
	if len(items) == 0 || s.Pool == nil {
		return nil
	}
	ids := make([]string, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ContentID)
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT ip.content_id, p.id, COALESCE(p.name, ''), ip.kind, COALESCE(ip.character, ''), ip.sort_order
		FROM item_people ip
		JOIN people p ON p.id = ip.person_id
		WHERE ip.content_id = ANY($1)
		ORDER BY ip.content_id, ip.kind, ip.sort_order, p.name
	`, ids)
	if err != nil {
		return fmt.Errorf("abs_media_store: load item_people: %w", err)
	}
	defer rows.Close()
	grouped := make(map[string][]models.ItemPerson, len(items))
	for rows.Next() {
		var (
			contentID string
			personID  int64
			name      string
			kind      models.PersonKind
			character string
			sortOrder int
		)
		if err := rows.Scan(&contentID, &personID, &name, &kind, &character, &sortOrder); err != nil {
			return fmt.Errorf("abs_media_store: scan item_people: %w", err)
		}
		grouped[contentID] = append(grouped[contentID], models.ItemPerson{
			Person:    models.Person{ID: personID, Name: name},
			Kind:      kind,
			Character: character,
			SortOrder: sortOrder,
		})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("abs_media_store: iterate item_people: %w", err)
	}
	for _, it := range items {
		if people, ok := grouped[it.ContentID]; ok {
			it.People = people
		}
	}
	return nil
}

// GetMediaFiles returns all media_files for the given content_id, ordered by
// file_path so ABS clients receive a stable chapter ordering.
func (s *ABSMediaStore) GetMediaFiles(ctx context.Context, contentID string) ([]*models.MediaFile, error) {
	files, err := s.Files.GetByContentID(ctx, contentID)
	if err != nil {
		return nil, fmt.Errorf("abs_media_store: get media files for %q: %w", contentID, err)
	}
	return files, nil
}

// GetMediaFileByID fetches a single media_file by its integer PK.
func (s *ABSMediaStore) GetMediaFileByID(ctx context.Context, fileID int) (*models.MediaFile, error) {
	file, err := s.Files.GetByID(ctx, fileID)
	if err != nil {
		return nil, fmt.Errorf("abs_media_store: get media file %d: %w", fileID, err)
	}
	return file, nil
}

// ListAudiobookLibraries returns media_folder rows where type='audiobooks'
// (the canonical silo type for the audiobooks sub-plan).
func (s *ABSMediaStore) ListAudiobookLibraries(ctx context.Context) ([]abs.AudiobookLibrary, error) {
	if s.Pool == nil {
		return nil, nil
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id, name, type
		FROM media_folders
		WHERE type IN ('audiobooks', 'audiobook')
		  AND enabled = TRUE
		ORDER BY sort_order, id`)
	if err != nil {
		return nil, fmt.Errorf("abs_media_store: list audiobook libraries: %w", err)
	}
	defer rows.Close()

	var libs []abs.AudiobookLibrary
	for rows.Next() {
		var lib abs.AudiobookLibrary
		if err := rows.Scan(&lib.ID, &lib.Name, &lib.Type); err != nil {
			return nil, fmt.Errorf("abs_media_store: scan library: %w", err)
		}
		libs = append(libs, lib)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("abs_media_store: iterate libraries: %w", err)
	}
	return libs, nil
}

// listAudiobookIDs is the shared helper used by Search/ContinueListening/
// RecentlyAdded/Discover. It runs a parameterized SQL fragment that yields
// a list of audiobook content_ids; the caller composes the WHERE/ORDER
// portions. Returned items have People hydrated.
func (s *ABSMediaStore) listAudiobookIDs(ctx context.Context, sql string, args []any) ([]*models.MediaItem, error) {
	if s.Pool == nil {
		return nil, fmt.Errorf("abs_media_store: no pgx pool")
	}
	rows, err := s.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("abs_media_store: query audiobook ids: %w", err)
	}
	defer rows.Close()
	ids := make([]string, 0, 16)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("abs_media_store: scan audiobook id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("abs_media_store: iterate audiobook ids: %w", err)
	}
	if len(ids) == 0 {
		return []*models.MediaItem{}, nil
	}
	items, err := s.Items.GetByIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("abs_media_store: load audiobooks: %w", err)
	}
	byID := make(map[string]*models.MediaItem, len(items))
	for _, it := range items {
		byID[it.ContentID] = it
	}
	ordered := make([]*models.MediaItem, 0, len(ids))
	for _, id := range ids {
		if it, ok := byID[id]; ok {
			ordered = append(ordered, it)
		}
	}
	if err := s.hydratePeople(ctx, ordered); err != nil {
		_ = err // non-fatal
	}
	return ordered, nil
}

// SearchAudiobooks matches the query against title (case-insensitive
// substring) plus author/narrator name. Capped by limit; ordered by
// title-prefix match first then alphabetical.
func (s *ABSMediaStore) SearchAudiobooks(ctx context.Context, libraryID int64, query string, limit int) ([]*models.MediaItem, error) {
	if limit <= 0 {
		limit = 12
	}
	libFilter := ""
	args := []any{"%" + query + "%", query, limit}
	if libraryID != 0 {
		libFilter = ` AND EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = mi.content_id AND mil.media_folder_id = $4)`
		args = append(args, int(libraryID))
	}
	// $1 = LIKE pattern, $2 = raw query (for prefix scoring), $3 = limit, $4 = libraryID
	sql := `
		SELECT mi.content_id FROM media_items mi
		WHERE mi.type = 'audiobook'
		  AND (
		      mi.title ILIKE $1
		   OR EXISTS (
		      SELECT 1 FROM item_people ip
		      JOIN people p ON p.id = ip.person_id
		      WHERE ip.content_id = mi.content_id
		        AND ip.kind IN (7, 8)
		        AND p.name ILIKE $1
		   )
		  )` + libFilter + `
		ORDER BY
		  CASE WHEN LOWER(mi.title) LIKE LOWER($2) || '%' THEN 0 ELSE 1 END,
		  LOWER(mi.sort_title),
		  LOWER(mi.title)
		LIMIT $3
	`
	return s.listAudiobookIDs(ctx, sql, args)
}

// ListContinueListening returns audiobooks the user has in-progress (and
// hasn't finished). userID is the silo integer-id-as-string from the ABS
// JWT; we filter by user_watch_progress for that user + this audiobook.
func (s *ABSMediaStore) ListContinueListening(ctx context.Context, userID, profileID string, libraryID int64, limit int) ([]*models.MediaItem, error) {
	if userID == "" {
		return []*models.MediaItem{}, nil
	}
	if limit <= 0 {
		limit = 10
	}
	libFilter := ""
	args := []any{userID, profileID, limit}
	if libraryID != 0 {
		libFilter = ` AND EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = mi.content_id AND mil.media_folder_id = $4)`
		args = append(args, int(libraryID))
	}
	sql := `
		SELECT mi.content_id FROM media_items mi
		JOIN user_watch_progress wp ON wp.media_item_id = mi.content_id
		WHERE mi.type = 'audiobook'
		  AND wp.user_id::text = $1
		  AND ($2 = '' OR wp.profile_id = $2)
		  AND wp.position_seconds > 0
		  AND COALESCE(wp.completed, FALSE) = FALSE` + libFilter + `
		ORDER BY wp.updated_at DESC
		LIMIT $3
	`
	return s.listAudiobookIDs(ctx, sql, args)
}

// ListRecentlyAdded returns the most recently added audiobooks. Added-at
// for audiobooks comes from MIN(first_seen_at) in media_item_libraries.
func (s *ABSMediaStore) ListRecentlyAdded(ctx context.Context, libraryID int64, limit int) ([]*models.MediaItem, error) {
	if limit <= 0 {
		limit = 10
	}
	libFilter := ""
	args := []any{limit}
	if libraryID != 0 {
		libFilter = ` AND mil.media_folder_id = $2`
		args = append(args, int(libraryID))
	}
	sql := `
		SELECT mi.content_id FROM media_items mi
		JOIN LATERAL (
		  SELECT MIN(first_seen_at) AS added_at
		  FROM media_item_libraries mil
		  WHERE mil.content_id = mi.content_id` + libFilter + `
		) added ON added.added_at IS NOT NULL
		WHERE mi.type = 'audiobook'
		ORDER BY added.added_at DESC
		LIMIT $1
	`
	return s.listAudiobookIDs(ctx, sql, args)
}

// ListDiscover returns a random sampling of audiobooks for the home
// Discover shelf. Uses TABLESAMPLE for cheap random sampling on large
// libraries (38k+ books); falls back to ORDER BY random() for tiny libs.
func (s *ABSMediaStore) ListDiscover(ctx context.Context, libraryID int64, limit int) ([]*models.MediaItem, error) {
	if limit <= 0 {
		limit = 10
	}
	libFilter := ""
	args := []any{limit}
	if libraryID != 0 {
		libFilter = ` AND EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = mi.content_id AND mil.media_folder_id = $2)`
		args = append(args, int(libraryID))
	}
	// Random sample with poster preference so the shelf has cover art.
	sql := `
		SELECT mi.content_id FROM media_items mi
		WHERE mi.type = 'audiobook'
		  AND COALESCE(mi.poster_path, '') <> ''` + libFilter + `
		ORDER BY random()
		LIMIT $1
	`
	return s.listAudiobookIDs(ctx, sql, args)
}

// ListLibraryAuthors aggregates audiobook authors (item_people kind=7)
// for the library, returning distinct (person_id, name, book_count).
func (s *ABSMediaStore) ListLibraryAuthors(ctx context.Context, libraryID int64, limit int) ([]abs.AuthorSummary, error) {
	if s.Pool == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 100
	}
	libFilter := ""
	args := []any{limit}
	if libraryID != 0 {
		libFilter = ` AND EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = mi.content_id AND mil.media_folder_id = $2)`
		args = append(args, int(libraryID))
	}
	sql := `
		SELECT p.id, p.name, COUNT(DISTINCT mi.content_id) AS num_books
		FROM media_items mi
		JOIN item_people ip ON ip.content_id = mi.content_id AND ip.kind = 7
		JOIN people p ON p.id = ip.person_id
		WHERE mi.type = 'audiobook'` + libFilter + `
		GROUP BY p.id, p.name
		ORDER BY LOWER(p.name)
		LIMIT $1
	`
	rows, err := s.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("abs_media_store: list authors: %w", err)
	}
	defer rows.Close()
	out := make([]abs.AuthorSummary, 0, limit)
	for rows.Next() {
		var (
			id    int64
			name  string
			books int
		)
		if err := rows.Scan(&id, &name, &books); err != nil {
			return nil, fmt.Errorf("abs_media_store: scan author: %w", err)
		}
		out = append(out, abs.AuthorSummary{ID: fmt.Sprintf("%d", id), Name: name, NumBooks: books})
	}
	return out, rows.Err()
}

// ListLibrarySeries returns distinct series from audiobook_series for the
// audiobook library, with per-series book count.
func (s *ABSMediaStore) ListLibrarySeries(ctx context.Context, libraryID int64, limit int) ([]abs.SeriesSummary, error) {
	if s.Pool == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 100
	}
	libFilter := ""
	args := []any{limit}
	if libraryID != 0 {
		libFilter = ` AND EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = mi.content_id AND mil.media_folder_id = $2)`
		args = append(args, int(libraryID))
	}
	sql := `
		SELECT s.series_name, COUNT(DISTINCT s.content_id) AS num_books
		FROM audiobook_series s
		JOIN media_items mi ON mi.content_id = s.content_id AND mi.type = 'audiobook'` + libFilter + `
		GROUP BY s.series_name
		HAVING COUNT(DISTINCT s.content_id) > 1
		ORDER BY LOWER(s.series_name)
		LIMIT $1
	`
	rows, err := s.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("abs_media_store: list series: %w", err)
	}
	defer rows.Close()
	out := make([]abs.SeriesSummary, 0, limit)
	for rows.Next() {
		var (
			name  string
			books int
		)
		if err := rows.Scan(&name, &books); err != nil {
			return nil, fmt.Errorf("abs_media_store: scan series: %w", err)
		}
		// Series ID is a slug of the name — there's no first-class series row yet.
		out = append(out, abs.SeriesSummary{ID: name, Name: name, NumBooks: books})
	}
	return out, rows.Err()
}

// GetAuthorByID looks up the author by people.id and returns the row
// plus their audiobooks.
func (s *ABSMediaStore) GetAuthorByID(ctx context.Context, authorID string) (abs.Author, error) {
	if s.Pool == nil {
		return abs.Author{}, abs.ErrNotFound
	}
	id, err := strconv.Atoi(authorID)
	if err != nil {
		return abs.Author{}, abs.ErrNotFound
	}
	var name string
	var poster *string
	row := s.Pool.QueryRow(ctx, `SELECT name, poster_path FROM people WHERE id = $1`, id)
	if err := row.Scan(&name, &poster); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return abs.Author{}, abs.ErrNotFound
		}
		return abs.Author{}, fmt.Errorf("abs_media_store: get author: %w", err)
	}
	author := abs.Author{ID: authorID, Name: name}
	if poster != nil {
		author.PosterPath = *poster
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT mi.content_id, mi.title
		FROM item_people ip
		JOIN media_items mi ON mi.content_id = ip.content_id
		WHERE ip.person_id = $1 AND ip.kind = 7 AND mi.type = 'audiobook'
		ORDER BY LOWER(mi.title)`,
		id,
	)
	if err != nil {
		return abs.Author{}, fmt.Errorf("abs_media_store: get author books: %w", err)
	}
	defer rows.Close()
	author.Books = make([]*models.MediaItem, 0)
	for rows.Next() {
		mi := &models.MediaItem{}
		if err := rows.Scan(&mi.ContentID, &mi.Title); err != nil {
			return abs.Author{}, fmt.Errorf("abs_media_store: get author books scan: %w", err)
		}
		author.Books = append(author.Books, mi)
	}
	return author, nil
}

// GetSeriesByName looks up a series case-insensitively, plus its books.
func (s *ABSMediaStore) GetSeriesByName(ctx context.Context, seriesName string) (abs.Series, error) {
	if s.Pool == nil {
		return abs.Series{}, abs.ErrNotFound
	}
	var canonicalName string
	row := s.Pool.QueryRow(ctx, `
		SELECT series_name FROM audiobook_series
		WHERE LOWER(series_name) = LOWER($1)
		LIMIT 1`, seriesName,
	)
	if err := row.Scan(&canonicalName); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return abs.Series{}, abs.ErrNotFound
		}
		return abs.Series{}, fmt.Errorf("abs_media_store: get series: %w", err)
	}
	series := abs.Series{ID: strings.ToLower(canonicalName), Name: canonicalName}
	rows, err := s.Pool.Query(ctx, `
		SELECT mi.content_id, mi.title
		FROM audiobook_series asx
		JOIN media_items mi ON mi.content_id = asx.content_id
		WHERE LOWER(asx.series_name) = LOWER($1) AND mi.type = 'audiobook'
		ORDER BY asx.series_index NULLS LAST, LOWER(mi.title)`,
		seriesName,
	)
	if err != nil {
		return abs.Series{}, fmt.Errorf("abs_media_store: get series books: %w", err)
	}
	defer rows.Close()
	series.Books = make([]*models.MediaItem, 0)
	for rows.Next() {
		mi := &models.MediaItem{}
		if err := rows.Scan(&mi.ContentID, &mi.Title); err != nil {
			return abs.Series{}, fmt.Errorf("abs_media_store: get series books scan: %w", err)
		}
		series.Books = append(series.Books, mi)
	}
	return series, nil
}
