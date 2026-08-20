package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

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

	// countCache memoizes the per-(library, filter, access) COUNT(*) that
	// ListAudiobooks would otherwise recompute on every page. A full client
	// library sync pages through thousands of requests reading the same total;
	// the count only shifts when the library changes, so a short TTL is safe.
	countMu    sync.Mutex
	countCache map[string]absCountEntry
}

type absCountEntry struct {
	n   int
	exp time.Time
}

// absCountCacheTTL bounds how stale the paginated total may be. During an active
// scan the count can lag by up to this window; clients re-sync, so that is fine.
const absCountCacheTTL = 60 * time.Second

const (
	absMediaTypeAudiobook = abs.MediaTypeAudiobook
	absMediaTypeEbook     = abs.MediaTypeEbook
	absLibraryTypeEbooks  = "ebooks"
)

// cachedAudiobookCount returns a memoized COUNT(*) for the given (countSQL, args)
// pair, running the query on a miss/expiry. The key is derived from the fully
// rendered SQL plus its bound args, so it automatically covers every input the
// count WHERE depends on (library, pushed-down filter, and all access
// predicates) — it can't drift as access logic evolves. The DB query runs
// outside the lock so concurrent syncs don't serialize on it.
func (s *ABSMediaStore) cachedAudiobookCount(ctx context.Context, countSQL string, args []any) (int, error) {
	key := countSQL + "\x1f" + fmt.Sprintf("%v", args)
	now := time.Now()
	s.countMu.Lock()
	if e, ok := s.countCache[key]; ok && now.Before(e.exp) {
		s.countMu.Unlock()
		return e.n, nil
	}
	s.countMu.Unlock()

	var n int
	if err := s.Pool.QueryRow(ctx, countSQL, args...).Scan(&n); err != nil {
		return 0, err
	}

	s.countMu.Lock()
	if s.countCache == nil {
		s.countCache = make(map[string]absCountEntry)
	}
	// Sweep expired entries on write so per-filter keys (e.g. one per author
	// during a sync) don't accumulate for the process lifetime.
	for k, e := range s.countCache {
		if now.After(e.exp) {
			delete(s.countCache, k)
		}
	}
	s.countCache[key] = absCountEntry{n: n, exp: now.Add(absCountCacheTTL)}
	s.countMu.Unlock()
	return n, nil
}

var _ abs.MediaStore = (*ABSMediaStore)(nil)

// GetAudiobookByID returns an ABS-supported book: either an audiobook or an
// ebook.  The historical method name is retained to avoid widening the ABS
// adapter interface for every consumer.
func (s *ABSMediaStore) GetAudiobookByID(ctx context.Context, contentID string, access catalog.AccessFilter) (*models.MediaItem, error) {
	items, err := s.Items.GetByIDsWithAccess(ctx, []string{contentID}, access)
	if err != nil {
		return nil, fmt.Errorf("abs_media_store: get audiobook %q: %w", contentID, err)
	}
	if len(items) == 0 {
		return nil, nil
	}
	item := items[0]
	if item == nil || (item.Type != absMediaTypeAudiobook && item.Type != absMediaTypeEbook) {
		return nil, nil
	}
	// Hydrate authors + narrators so the ABS metadata mapper can fill
	// authorName / narratorName on the response.
	if err := s.hydratePeople(ctx, []*models.MediaItem{item}); err != nil {
		// Non-fatal: caller can still render the item without people data.
		_ = err
	}
	if err := s.hydrateBookSeries(ctx, []*models.MediaItem{item}); err != nil {
		_ = err
	}
	return item, nil
}

// GetAudiobooksByIDs batch-fetches audiobooks by content_id, hydrating people
// and series once for the whole set. List/shelf handlers (continue-listening,
// similar, my-progress) previously called GetAudiobookByID per row, issuing a
// handful of queries per item — up to ~500 single fetches on app open. Returns
// a map keyed by content_id; missing or non-audiobook ids are omitted.
func (s *ABSMediaStore) GetAudiobooksByIDs(ctx context.Context, contentIDs []string, access catalog.AccessFilter) (map[string]*models.MediaItem, error) {
	if len(contentIDs) == 0 {
		return map[string]*models.MediaItem{}, nil
	}
	items, err := s.Items.GetByIDsWithAccess(ctx, contentIDs, access)
	if err != nil {
		return nil, fmt.Errorf("abs_media_store: get audiobooks: %w", err)
	}
	books := make([]*models.MediaItem, 0, len(items))
	for _, item := range items {
		if item != nil && (item.Type == absMediaTypeAudiobook || item.Type == absMediaTypeEbook) {
			books = append(books, item)
		}
	}
	if len(books) == 0 {
		return map[string]*models.MediaItem{}, nil
	}
	// Hydrate the whole set in two queries rather than per item.
	if err := s.hydratePeople(ctx, books); err != nil {
		_ = err
	}
	if err := s.hydrateBookSeries(ctx, books); err != nil {
		_ = err
	}
	out := make(map[string]*models.MediaItem, len(books))
	for _, b := range books {
		out[b.ContentID] = b
	}
	return out, nil
}

// ListAudiobooks returns a page of media_items with type='audiobook'.
// When libraryID is non-zero, results are filtered to items in that
// media_folder (via the media_item_libraries junction); 0 means all libraries.
//
// We can't reuse ItemRepository.Search because Search is a text-search
// path that bails out when the query string is empty. We page content_ids
// here via SQL, then load full rows via GetByIDs so the scan logic stays
// in the catalog package.
func (s *ABSMediaStore) ListAudiobooks(ctx context.Context, libraryID int64, limit, offset int, access catalog.AccessFilter, filter abs.Filter) ([]*models.MediaItem, int, error) {
	if s.Pool == nil {
		return nil, 0, fmt.Errorf("abs_media_store: no pgx pool")
	}
	if offset < 0 {
		offset = 0
	}

	var total int
	itemType, err := s.libraryItemType(ctx, libraryID)
	if err != nil {
		return nil, 0, err
	}
	conditions := []string{"mi.type = '" + itemType + "'"}
	args := []any{}
	argIdx := 1
	if libraryID != 0 {
		conditions = append(conditions, fmt.Sprintf(`EXISTS (
			SELECT 1 FROM media_item_libraries mil
			WHERE mil.content_id = mi.content_id AND mil.media_folder_id = $%d
		)`, argIdx))
		args = append(args, int(libraryID))
		argIdx++
	}
	appendAudiobookAccessConditions("mi", access, &conditions, &args, &argIdx)
	// Push author/series/narrator filters into SQL so per-author album syncs
	// don't load and hydrate the entire library on every request. Uses the
	// idx_item_people_content_kind_person and audiobook_series indexes.
	appendAudiobookFilterConditions(itemType, filter, &conditions, &args, &argIdx)
	where := strings.Join(conditions, " AND ")

	total, err = s.cachedAudiobookCount(ctx, `SELECT COUNT(*) FROM media_items mi WHERE `+where, args)
	if err != nil {
		return nil, 0, fmt.Errorf("abs_media_store: count audiobooks: %w", err)
	}
	if total == 0 {
		return []*models.MediaItem{}, 0, nil
	}

	dataArgs := append([]any(nil), args...)
	// Order by the same expression idx_media_items_sort_key is built on so the
	// page can be served by an ordered index scan instead of sorting the whole
	// library on every request; content_id is a stable tiebreaker so sequential
	// pages don't skip or repeat rows when sort keys collide.
	dataSQL := `SELECT mi.content_id FROM media_items mi WHERE ` + where +
		` ORDER BY lower(coalesce(nullif(btrim(mi.sort_title), ''), mi.title)), mi.content_id`
	if limit > 0 {
		argIdx = len(dataArgs) + 1
		dataSQL += fmt.Sprintf(` LIMIT $%d OFFSET $%d`, argIdx, argIdx+1)
		dataArgs = append(dataArgs, limit, offset)
	}

	rows, err := s.Pool.Query(ctx, dataSQL, dataArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("abs_media_store: list audiobooks: %w", err)
	}
	defer rows.Close()
	ids := make([]string, 0, 32)
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
	if err := s.hydrateBookSeries(ctx, ordered); err != nil {
		_ = err
	}
	return ordered, total, nil
}

// libraryItemType maps a Silo media_folder type to its media_items type.
// Unknown/virtual folders preserve the long-standing audiobook default.
func (s *ABSMediaStore) libraryItemType(ctx context.Context, libraryID int64) (string, error) {
	if libraryID == 0 || s.Pool == nil {
		return absMediaTypeAudiobook, nil
	}
	var folderType string
	if err := s.Pool.QueryRow(ctx, `SELECT type FROM media_folders WHERE id = $1`, libraryID).Scan(&folderType); err != nil {
		return "", fmt.Errorf("abs_media_store: resolve library %d: %w", libraryID, err)
	}
	if folderType == absMediaTypeEbook || folderType == absLibraryTypeEbooks {
		return absMediaTypeEbook, nil
	}
	return absMediaTypeAudiobook, nil
}

func bookSeriesTable(itemType string) string {
	if itemType == absMediaTypeEbook {
		return "ebook_series"
	}
	return "audiobook_series"
}

// appendAudiobookFilterConditions pushes an ABS authors/series/narrators
// filter down into the SQL WHERE clause. Author values are the numeric
// person_id (as returned by the /authors endpoint), with a name fallback for
// non-numeric values; narrator and series values are names. This mirrors the
// case-sensitive exact-match identity semantics of abs.Filter.Matches.
// Progress/tag filters are not pushable here (they need per-user or detail
// data) and are handled by the caller in Go.
func appendAudiobookFilterConditions(itemType string, filter abs.Filter, conditions *[]string, args *[]any, argIdx *int) {
	switch filter.Kind {
	case abs.FilterAuthors:
		if id, err := strconv.ParseInt(filter.Value, 10, 64); err == nil {
			*conditions = append(*conditions, fmt.Sprintf(
				`EXISTS (SELECT 1 FROM item_people ip WHERE ip.content_id = mi.content_id AND ip.kind = %d AND ip.person_id = $%d)`,
				models.PersonKindAuthor, *argIdx))
			*args = append(*args, id)
		} else {
			*conditions = append(*conditions, fmt.Sprintf(
				`EXISTS (SELECT 1 FROM item_people ip JOIN people p ON p.id = ip.person_id WHERE ip.content_id = mi.content_id AND ip.kind = %d AND p.name = $%d)`,
				models.PersonKindAuthor, *argIdx))
			*args = append(*args, filter.Value)
		}
		*argIdx = *argIdx + 1
	case abs.FilterNarrators:
		*conditions = append(*conditions, fmt.Sprintf(
			`EXISTS (SELECT 1 FROM item_people ip JOIN people p ON p.id = ip.person_id WHERE ip.content_id = mi.content_id AND ip.kind = %d AND p.name = $%d)`,
			models.PersonKindNarrator, *argIdx))
		*args = append(*args, filter.Value)
		*argIdx = *argIdx + 1
	case abs.FilterSeries:
		seriesTable := bookSeriesTable(itemType)
		if filter.Value == abs.SentinelNoSeries {
			*conditions = append(*conditions, `NOT EXISTS (SELECT 1 FROM `+seriesTable+` book_series WHERE book_series.content_id = mi.content_id)`)
		} else {
			*conditions = append(*conditions, fmt.Sprintf(
				`EXISTS (SELECT 1 FROM `+seriesTable+` book_series WHERE book_series.content_id = mi.content_id AND book_series.series_name = $%d)`, *argIdx))
			*args = append(*args, filter.Value)
			*argIdx = *argIdx + 1
		}
	case abs.FilterGenres:
		*conditions = append(*conditions, fmt.Sprintf(
			`EXISTS (SELECT 1 FROM unnest(COALESCE(mi.genres, ARRAY[]::text[])) AS genre WHERE LOWER(genre) = LOWER($%d))`, *argIdx))
		*args = append(*args, filter.Value)
		*argIdx = *argIdx + 1
	case abs.FilterLanguages:
		*conditions = append(*conditions, fmt.Sprintf(
			`LOWER(COALESCE(NULLIF(BTRIM(mi.original_language), ''), NULLIF(BTRIM(mi.default_metadata_language), ''), 'en')) = LOWER($%d)`, *argIdx))
		*args = append(*args, filter.Value)
		*argIdx = *argIdx + 1
	case abs.FilterPublishers:
		*conditions = append(*conditions, fmt.Sprintf(
			`EXISTS (SELECT 1 FROM unnest(COALESCE(mi.studios, ARRAY[]::text[])) AS publisher WHERE LOWER(publisher) = LOWER($%d))`, *argIdx))
		*args = append(*args, filter.Value)
		*argIdx = *argIdx + 1
	}
}

func appendAudiobookAccessConditions(alias string, filter catalog.AccessFilter, conditions *[]string, args *[]any, argIdx *int) {
	if filter.AllowedLibraryIDs != nil {
		if len(filter.AllowedLibraryIDs) == 0 {
			*conditions = append(*conditions, "1 = 0")
		} else {
			*conditions = append(*conditions, fmt.Sprintf(`EXISTS (
				SELECT 1 FROM media_item_libraries mil_access
				WHERE mil_access.content_id = %s.content_id
				  AND mil_access.media_folder_id = ANY($%d)
			)`, alias, *argIdx))
			*args = append(*args, filter.AllowedLibraryIDs)
			*argIdx = *argIdx + 1
		}
	}
	if len(filter.DisabledLibraryIDs) > 0 {
		if filter.AllowedLibraryIDs == nil {
			*conditions = append(*conditions, fmt.Sprintf(`EXISTS (
				SELECT 1 FROM media_item_libraries mil_present
				WHERE mil_present.content_id = %s.content_id
			)`, alias))
		}
		*conditions = append(*conditions, fmt.Sprintf(`NOT EXISTS (
			SELECT 1 FROM media_item_libraries mil_disabled
			WHERE mil_disabled.content_id = %s.content_id
			  AND mil_disabled.media_folder_id = ANY($%d)
		)`, alias, *argIdx))
		*args = append(*args, filter.DisabledLibraryIDs)
		*argIdx = *argIdx + 1
	}
	catalog.ApplySectionAccessFilter(alias, filter, conditions, args, argIdx)
}

func appendLibraryAccessConditions(alias string, filter catalog.AccessFilter, conditions *[]string, args *[]any, argIdx *int) {
	if filter.AllowedLibraryIDs != nil {
		if len(filter.AllowedLibraryIDs) == 0 {
			*conditions = append(*conditions, "1 = 0")
		} else {
			*conditions = append(*conditions, fmt.Sprintf("%s.id = ANY($%d)", alias, *argIdx))
			*args = append(*args, filter.AllowedLibraryIDs)
			*argIdx = *argIdx + 1
		}
	}
	if len(filter.DisabledLibraryIDs) > 0 {
		*conditions = append(*conditions, fmt.Sprintf("NOT (%s.id = ANY($%d))", alias, *argIdx))
		*args = append(*args, filter.DisabledLibraryIDs)
		*argIdx = *argIdx + 1
	}
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

func (s *ABSMediaStore) hydrateBookSeries(ctx context.Context, items []*models.MediaItem) error {
	if len(items) == 0 || s.Pool == nil {
		return nil
	}
	itemsByType := make(map[string][]*models.MediaItem, 2)
	for _, it := range items {
		if it != nil && (it.Type == absMediaTypeAudiobook || it.Type == absMediaTypeEbook) {
			itemsByType[it.Type] = append(itemsByType[it.Type], it)
		}
	}
	for itemType, typedItems := range itemsByType {
		if err := s.hydrateBookSeriesTable(ctx, bookSeriesTable(itemType), typedItems); err != nil {
			return err
		}
	}
	return nil
}

func (s *ABSMediaStore) hydrateBookSeriesTable(ctx context.Context, seriesTable string, items []*models.MediaItem) error {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ContentID)
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT content_id, series_name, COALESCE(series_index::text, '')
		FROM `+seriesTable+`
		WHERE content_id = ANY($1)
		ORDER BY content_id, series_index NULLS LAST, series_name
	`, ids)
	if err != nil {
		return fmt.Errorf("abs_media_store: load %s: %w", seriesTable, err)
	}
	defer rows.Close()
	grouped := make(map[string][]models.AudiobookSeriesMembership, len(items))
	for rows.Next() {
		var contentID, name, indexRaw string
		if err := rows.Scan(&contentID, &name, &indexRaw); err != nil {
			return fmt.Errorf("abs_media_store: scan %s: %w", seriesTable, err)
		}
		membership := models.AudiobookSeriesMembership{Name: name}
		if indexRaw != "" {
			if f, err := strconv.ParseFloat(indexRaw, 64); err == nil {
				membership.Index = &f
			}
		}
		grouped[contentID] = append(grouped[contentID], membership)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("abs_media_store: iterate %s: %w", seriesTable, err)
	}
	for _, it := range items {
		it.AudiobookSeries = grouped[it.ContentID]
	}
	return nil
}

// GetMediaFiles returns all media_files for the given content_id, ordered by
// file_path so ABS clients receive a stable chapter ordering.
func (s *ABSMediaStore) GetMediaFiles(ctx context.Context, contentID string, access catalog.AccessFilter) ([]*models.MediaFile, error) {
	items, err := s.Items.GetByIDsWithAccess(ctx, []string{contentID}, access)
	if err != nil {
		return nil, fmt.Errorf("abs_media_store: check media file access for %q: %w", contentID, err)
	}
	if len(items) == 0 {
		return []*models.MediaFile{}, nil
	}
	files, err := s.Files.GetByContentID(ctx, contentID)
	if err != nil {
		return nil, fmt.Errorf("abs_media_store: get media files for %q: %w", contentID, err)
	}
	return catalog.FilterMediaFilesByAccess(files, access), nil
}

// GetMediaFilesByContentIDs returns access-filtered media files for a list
// page in two bounded queries: one access gate and one file fetch.
func (s *ABSMediaStore) GetMediaFilesByContentIDs(ctx context.Context, contentIDs []string, access catalog.AccessFilter) (map[string][]*models.MediaFile, error) {
	out := make(map[string][]*models.MediaFile, len(contentIDs))
	if len(contentIDs) == 0 {
		return out, nil
	}
	items, err := s.Items.GetByIDsWithAccess(ctx, contentIDs, access)
	if err != nil {
		return nil, fmt.Errorf("abs_media_store: check batch media file access: %w", err)
	}
	accessibleIDs := make([]string, 0, len(items))
	for _, item := range items {
		if item != nil {
			accessibleIDs = append(accessibleIDs, item.ContentID)
		}
	}
	if len(accessibleIDs) == 0 {
		return out, nil
	}
	grouped, err := s.Files.ListByContentIDs(ctx, accessibleIDs)
	if err != nil {
		return nil, fmt.Errorf("abs_media_store: get batch media files: %w", err)
	}
	for _, contentID := range accessibleIDs {
		out[contentID] = catalog.FilterMediaFilesByAccess(grouped[contentID], access)
	}
	return out, nil
}

// GetMediaFileByID fetches a single media_file by its integer PK.
func (s *ABSMediaStore) GetMediaFileByID(ctx context.Context, fileID int) (*models.MediaFile, error) {
	file, err := s.Files.GetByID(ctx, fileID)
	if err != nil {
		return nil, fmt.Errorf("abs_media_store: get media file %d: %w", fileID, err)
	}
	return file, nil
}

// GetPrimaryEbookFileID returns the explicit ABS primary ebook selection.
// A missing row means callers should apply ABS's EPUB-first default. A row
// with a NULL file_id is an explicit ABS "no primary" selection.
func (s *ABSMediaStore) GetPrimaryEbookFileID(ctx context.Context, contentID string) (int, bool, bool, error) {
	if s.Pool == nil {
		return 0, false, false, nil
	}
	var fileID *int
	err := s.Pool.QueryRow(ctx, `SELECT p.file_id
		FROM abs_ebook_primary_files p
		LEFT JOIN media_files mf ON mf.id = p.file_id AND mf.content_id = p.content_id
		WHERE p.content_id = $1 AND (p.file_id IS NULL OR mf.id IS NOT NULL)`, contentID).Scan(&fileID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, false, nil
	}
	if err != nil {
		return 0, false, false, fmt.Errorf("abs_media_store: get primary ebook: %w", err)
	}
	if fileID == nil {
		return 0, true, false, nil
	}
	return *fileID, true, true, nil
}

// GetPrimaryEbookFileIDs loads explicit ABS ebook selections in one query.
// Missing map keys mean the item has no preference row and should use the
// EPUB-first default.
func (s *ABSMediaStore) GetPrimaryEbookFileIDs(ctx context.Context, contentIDs []string) (map[string]abs.EbookPrimarySelection, error) {
	out := make(map[string]abs.EbookPrimarySelection, len(contentIDs))
	if len(contentIDs) == 0 || s.Pool == nil {
		return out, nil
	}
	rows, err := s.Pool.Query(ctx, `SELECT p.content_id, p.file_id
		FROM abs_ebook_primary_files p
		LEFT JOIN media_files mf ON mf.id = p.file_id AND mf.content_id = p.content_id
		WHERE p.content_id = ANY($1) AND (p.file_id IS NULL OR mf.id IS NOT NULL)`, contentIDs)
	if err != nil {
		return nil, fmt.Errorf("abs_media_store: get primary ebooks: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			contentID string
			fileID    *int
		)
		if err := rows.Scan(&contentID, &fileID); err != nil {
			return nil, fmt.Errorf("abs_media_store: scan primary ebook: %w", err)
		}
		selection := abs.EbookPrimarySelection{Configured: true}
		if fileID != nil {
			selection.FileID = *fileID
			selection.HasPrimary = true
		}
		out[contentID] = selection
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("abs_media_store: iterate primary ebooks: %w", err)
	}
	return out, nil
}

// ClearPrimaryEbookFileID records ABS's explicit "all files supplementary"
// state, reached by toggling the currently primary file's status.
func (s *ABSMediaStore) ClearPrimaryEbookFileID(ctx context.Context, contentID string) error {
	if s.Pool == nil {
		return fmt.Errorf("abs_media_store: no pgx pool")
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO abs_ebook_primary_files (content_id, file_id, updated_at)
		VALUES ($1, NULL, now())
		ON CONFLICT (content_id) DO UPDATE SET file_id = NULL, updated_at = now()`, contentID)
	if err != nil {
		return fmt.Errorf("abs_media_store: clear primary ebook: %w", err)
	}
	return nil
}

// SetPrimaryEbookFileID persists a primary ebook selection made through ABS.
func (s *ABSMediaStore) SetPrimaryEbookFileID(ctx context.Context, contentID string, fileID int) error {
	if s.Pool == nil {
		return fmt.Errorf("abs_media_store: no pgx pool")
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO abs_ebook_primary_files (content_id, file_id, updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (content_id) DO UPDATE SET file_id = EXCLUDED.file_id, updated_at = now()`, contentID, fileID)
	if err != nil {
		return fmt.Errorf("abs_media_store: set primary ebook: %w", err)
	}
	return nil
}

// ListAudiobookLibraries returns every Silo folder ABS can render as a book
// library: audiobooks and ebooks.  ABS uses the same `book` media type for
// both; Type remains the Silo source type for query isolation.
func (s *ABSMediaStore) ListAudiobookLibraries(ctx context.Context, access catalog.AccessFilter) ([]abs.AudiobookLibrary, error) {
	if s.Pool == nil {
		return nil, nil
	}
	conditions := []string{"type IN ('audiobooks', 'audiobook', 'ebooks', 'ebook')", "enabled = TRUE"}
	args := []any{}
	argIdx := 1
	appendLibraryAccessConditions("media_folders", access, &conditions, &args, &argIdx)
	rows, err := s.Pool.Query(ctx, `
		SELECT id, name, type
		FROM media_folders
		WHERE `+strings.Join(conditions, " AND ")+`
		ORDER BY sort_order, id`, args...)
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

// GetItemLibraryIDs returns one deterministic accessible source library per
// item. Items may belong to multiple folders; the same sort order as the ABS
// library list chooses the canonical one without per-item query fan-out.
func (s *ABSMediaStore) GetItemLibraryIDs(ctx context.Context, contentIDs []string, access catalog.AccessFilter) (map[string]int64, error) {
	out := make(map[string]int64, len(contentIDs))
	if len(contentIDs) == 0 || s.Pool == nil {
		return out, nil
	}
	conditions := []string{
		"mil.content_id = ANY($1)",
		"mf.type IN ('audiobooks', 'audiobook', 'ebooks', 'ebook')",
		"mf.enabled = TRUE",
	}
	args := []any{contentIDs}
	argIdx := 2
	appendLibraryAccessConditions("mf", access, &conditions, &args, &argIdx)
	rows, err := s.Pool.Query(ctx, `
		SELECT DISTINCT ON (mil.content_id) mil.content_id, mf.id
		FROM media_item_libraries mil
		JOIN media_folders mf ON mf.id = mil.media_folder_id
		WHERE `+strings.Join(conditions, " AND ")+`
		ORDER BY mil.content_id, mf.sort_order, mf.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("abs_media_store: get item libraries: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var contentID string
		var libraryID int64
		if err := rows.Scan(&contentID, &libraryID); err != nil {
			return nil, fmt.Errorf("abs_media_store: scan item library: %w", err)
		}
		out[contentID] = libraryID
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("abs_media_store: iterate item libraries: %w", err)
	}
	return out, nil
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
	if err := s.hydrateBookSeries(ctx, ordered); err != nil {
		_ = err // non-fatal
	}
	return ordered, nil
}

// SearchAudiobooks matches the query against title plus author/narrator name.
// Capped by limit; ordered by title-prefix match first, then title substring,
// then author/narrator matches, then alphabetical.
//
// Both arms are shaped to hit the existing pg_trgm GIN indexes rather than
// seq-scanning the whole library: the title arm matches media_items.
// title_normalized (idx_media_items_title_normalized_trgm) using the same
// normalize_search_text() the rest of catalog search uses, and the people arm
// matches people.name (idx_people_name_trgm). Running them as a UNION lets the
// planner drive each arm from its own index; GROUP BY content_id keeps the best
// rank when an item matches both. normalize_search_text($2) <> ” guards a
// punctuation-only query from degenerating into ILIKE '%%' over everything.
func (s *ABSMediaStore) SearchAudiobooks(ctx context.Context, libraryID int64, query string, limit int, access catalog.AccessFilter) ([]*models.MediaItem, error) {
	if limit <= 0 {
		limit = 12
	}
	itemType, err := s.libraryItemType(ctx, libraryID)
	if err != nil {
		return nil, err
	}
	// $1 = raw-query substring pattern for people.name; $2 = raw query, normalized
	// in-SQL for the title arm.
	args := []any{"%" + query + "%", query}
	argIdx := 3

	// Library + access predicates are identical in both UNION arms and reuse the
	// same positional placeholders, so append their args only once.
	scopeConds := []string{}
	if libraryID != 0 {
		scopeConds = append(scopeConds, fmt.Sprintf(`EXISTS (
			SELECT 1 FROM media_item_libraries mil
			WHERE mil.content_id = mi.content_id AND mil.media_folder_id = $%d
		)`, argIdx))
		args = append(args, int(libraryID))
		argIdx++
	}
	appendAudiobookAccessConditions("mi", access, &scopeConds, &args, &argIdx)
	scope := ""
	if len(scopeConds) > 0 {
		scope = " AND " + strings.Join(scopeConds, " AND ")
	}
	args = append(args, limit)

	sql := buildABSSearchSQL(itemType, scope, argIdx)
	return s.listAudiobookIDs(ctx, sql, args)
}

func buildABSSearchSQL(itemType, scope string, limitArg int) string {
	return `
		SELECT content_id FROM (
			SELECT mi.content_id AS content_id, mi.sort_title AS sort_title, mi.title AS title,
			       CASE WHEN mi.title_normalized LIKE normalize_search_text($2) || '%' THEN 0 ELSE 1 END AS rank
			FROM media_items mi
			WHERE mi.type = '` + itemType + `'
			  AND normalize_search_text($2) <> ''
			  AND mi.title_normalized ILIKE '%' || normalize_search_text($2) || '%'` + scope + `
			UNION ALL
			SELECT mi.content_id AS content_id, mi.sort_title AS sort_title, mi.title AS title, 2 AS rank
			FROM media_items mi
			WHERE mi.type = '` + itemType + `'
			  AND EXISTS (
			      SELECT 1 FROM item_people ip
			      JOIN people p ON p.id = ip.person_id
			      WHERE ip.content_id = mi.content_id
			        AND ip.kind IN (7, 8)
			        AND p.name ILIKE $1
			  )` + scope + `
		) m
		GROUP BY content_id, sort_title, title
		ORDER BY MIN(rank), LOWER(sort_title), LOWER(title)
		LIMIT $` + strconv.Itoa(limitArg) + `
	`
}

// ListContinueListening returns in-progress audiobooks or ebooks. Ebook
// progress is stored separately by Silo's native reader, so it needs its own
// query while keeping the same ABS shelf contract.
func (s *ABSMediaStore) ListContinueListening(ctx context.Context, userID, profileID string, libraryID int64, limit int, access catalog.AccessFilter) ([]*models.MediaItem, error) {
	if userID == "" {
		return []*models.MediaItem{}, nil
	}
	if limit <= 0 {
		limit = 10
	}
	itemType, err := s.libraryItemType(ctx, libraryID)
	if err != nil {
		return nil, err
	}
	args := []any{userID, profileID, limit}
	if itemType == absMediaTypeEbook {
		conditions := []string{
			`mi.type = 'ebook'`,
			`erp.user_id = $1::integer`,
			`($2 = '' OR erp.profile_id = $2)`,
			`erp.progress > 0`,
			fmt.Sprintf(`erp.progress < %v`, models.EbookFinishedProgressThreshold),
			`NOT EXISTS (
					SELECT 1 FROM user_history_hidden_items hhi
					WHERE hhi.user_id = erp.user_id
					  AND hhi.profile_id = erp.profile_id
					  AND hhi.media_item_id = erp.content_id
					  AND erp.updated_at <= hhi.hidden_before
				)`,
		}
		argIdx := 4
		if libraryID != 0 {
			conditions = append(conditions, fmt.Sprintf(`EXISTS (
				SELECT 1 FROM media_item_libraries mil
				WHERE mil.content_id = mi.content_id AND mil.media_folder_id = $%d
			)`, argIdx))
			args = append(args, int(libraryID))
			argIdx++
		}
		appendAudiobookAccessConditions("mi", access, &conditions, &args, &argIdx)
		return s.listAudiobookIDs(ctx, `
			SELECT mi.content_id FROM media_items mi
			JOIN ebook_reader_progress erp ON erp.content_id = mi.content_id
			WHERE `+strings.Join(conditions, " AND ")+`
			ORDER BY erp.updated_at DESC
			LIMIT $3
		`, args)
	}

	conditions := []string{
		`mi.type = 'audiobook'`,
		`wp.user_id::text = $1`,
		`($2 = '' OR wp.profile_id = $2)`,
		`wp.position_seconds > 0`,
		`COALESCE(wp.completed, FALSE) = FALSE`,
		`COALESCE(wp.hide_from_continue, FALSE) = FALSE`,
	}
	argIdx := 4
	if libraryID != 0 {
		conditions = append(conditions, fmt.Sprintf(`EXISTS (
			SELECT 1 FROM media_item_libraries mil
			WHERE mil.content_id = mi.content_id AND mil.media_folder_id = $%d
		)`, argIdx))
		args = append(args, int(libraryID))
		argIdx++
	}
	appendAudiobookAccessConditions("mi", access, &conditions, &args, &argIdx)
	sql := `
		SELECT mi.content_id FROM media_items mi
		JOIN user_watch_progress wp ON wp.media_item_id = mi.content_id
		WHERE ` + strings.Join(conditions, " AND ") + `
		ORDER BY wp.updated_at DESC
		LIMIT $3
	`
	return s.listAudiobookIDs(ctx, sql, args)
}

// ListFinished returns books completed by the active ABS profile. It feeds
// Audiobookshelf's Listen Again or Read Again shelf.
func (s *ABSMediaStore) ListFinished(ctx context.Context, userID, profileID string, libraryID int64, limit int, access catalog.AccessFilter) ([]*models.MediaItem, error) {
	if userID == "" {
		return []*models.MediaItem{}, nil
	}
	if limit <= 0 {
		limit = 10
	}
	itemType, err := s.libraryItemType(ctx, libraryID)
	if err != nil {
		return nil, err
	}
	args := []any{userID, profileID, limit}
	if itemType == "ebook" {
		conditions := []string{
			`mi.type = 'ebook'`,
			`erp.user_id = $1::integer`,
			`($2 = '' OR erp.profile_id = $2)`,
			fmt.Sprintf(`erp.progress >= %v`, models.EbookFinishedProgressThreshold),
			`NOT EXISTS (
				SELECT 1 FROM user_history_hidden_items hhi
				WHERE hhi.user_id = erp.user_id
				  AND hhi.profile_id = erp.profile_id
				  AND hhi.media_item_id = erp.content_id
				  AND erp.updated_at <= hhi.hidden_before
			)`,
		}
		argIdx := 4
		if libraryID != 0 {
			conditions = append(conditions, fmt.Sprintf(`EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = mi.content_id AND mil.media_folder_id = $%d)`, argIdx))
			args = append(args, int(libraryID))
			argIdx++
		}
		appendAudiobookAccessConditions("mi", access, &conditions, &args, &argIdx)
		return s.listAudiobookIDs(ctx, `SELECT mi.content_id FROM media_items mi JOIN ebook_reader_progress erp ON erp.content_id = mi.content_id WHERE `+strings.Join(conditions, " AND ")+` ORDER BY erp.updated_at DESC LIMIT $3`, args)
	}
	conditions := []string{`mi.type = 'audiobook'`, `wp.user_id::text = $1`, `($2 = '' OR wp.profile_id = $2)`, `COALESCE(wp.completed, FALSE) = TRUE`}
	argIdx := 4
	if libraryID != 0 {
		conditions = append(conditions, fmt.Sprintf(`EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = mi.content_id AND mil.media_folder_id = $%d)`, argIdx))
		args = append(args, int(libraryID))
		argIdx++
	}
	appendAudiobookAccessConditions("mi", access, &conditions, &args, &argIdx)
	return s.listAudiobookIDs(ctx, `SELECT mi.content_id FROM media_items mi JOIN user_watch_progress wp ON wp.media_item_id = mi.content_id WHERE `+strings.Join(conditions, " AND ")+` ORDER BY wp.updated_at DESC LIMIT $3`, args)
}

// ListRecentlyAdded returns the most recently added items in the requested
// audiobook or ebook library. Added-at comes from MIN(first_seen_at) in
// media_item_libraries.
func (s *ABSMediaStore) ListRecentlyAdded(ctx context.Context, libraryID int64, limit int, access catalog.AccessFilter) ([]*models.MediaItem, error) {
	if limit <= 0 {
		limit = 10
	}
	itemType, err := s.libraryItemType(ctx, libraryID)
	if err != nil {
		return nil, err
	}
	args := []any{limit}
	conditions := []string{"mi.type = '" + itemType + "'"}
	argIdx := 2
	libFilter := ""
	if libraryID != 0 {
		libFilter = fmt.Sprintf(` AND mil.media_folder_id = $%d`, argIdx)
		args = append(args, int(libraryID))
		argIdx++
	}
	appendAudiobookAccessConditions("mi", access, &conditions, &args, &argIdx)
	sql := `
		SELECT mi.content_id FROM media_items mi
		JOIN LATERAL (
		  SELECT MIN(first_seen_at) AS added_at
		  FROM media_item_libraries mil
		  WHERE mil.content_id = mi.content_id` + libFilter + `
		) added ON added.added_at IS NOT NULL
		WHERE ` + strings.Join(conditions, " AND ") + `
		ORDER BY added.added_at DESC
		LIMIT $1
	`
	return s.listAudiobookIDs(ctx, sql, args)
}

// ListDiscover returns a random sampling of library items for the home
// Discover shelf. Uses TABLESAMPLE for cheap random sampling on large
// libraries (38k+ books); falls back to ORDER BY random() for tiny libs.
func (s *ABSMediaStore) ListDiscover(ctx context.Context, libraryID int64, limit int, access catalog.AccessFilter) ([]*models.MediaItem, error) {
	if limit <= 0 {
		limit = 10
	}
	itemType, err := s.libraryItemType(ctx, libraryID)
	if err != nil {
		return nil, err
	}
	args := []any{limit}
	conditions := []string{"mi.type = '" + itemType + "'", `COALESCE(mi.poster_path, '') <> ''`}
	argIdx := 2
	if libraryID != 0 {
		conditions = append(conditions, fmt.Sprintf(`EXISTS (
			SELECT 1 FROM media_item_libraries mil
			WHERE mil.content_id = mi.content_id AND mil.media_folder_id = $%d
		)`, argIdx))
		args = append(args, int(libraryID))
		argIdx++
	}
	appendAudiobookAccessConditions("mi", access, &conditions, &args, &argIdx)
	// Random sample with poster preference so the shelf has cover art.
	sql := `
		SELECT mi.content_id FROM media_items mi
		WHERE ` + strings.Join(conditions, " AND ") + `
		ORDER BY random()
		LIMIT $1
	`
	return s.listAudiobookIDs(ctx, sql, args)
}

// ListLibraryGenres returns the distinct catalog genres used by a library.
func (s *ABSMediaStore) ListLibraryGenres(ctx context.Context, libraryID int64, access catalog.AccessFilter) ([]string, error) {
	if s.Pool == nil {
		return []string{}, nil
	}
	itemType, err := s.libraryItemType(ctx, libraryID)
	if err != nil {
		return nil, err
	}
	conditions := []string{"mi.type = '" + itemType + "'", `genre <> ''`}
	args := []any{}
	argIdx := 1
	if libraryID != 0 {
		conditions = append(conditions, fmt.Sprintf(`EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = mi.content_id AND mil.media_folder_id = $%d)`, argIdx))
		args = append(args, int(libraryID))
		argIdx++
	}
	appendAudiobookAccessConditions("mi", access, &conditions, &args, &argIdx)
	rows, err := s.Pool.Query(ctx, `SELECT genre FROM media_items mi CROSS JOIN LATERAL unnest(COALESCE(mi.genres, ARRAY[]::text[])) AS genre WHERE `+strings.Join(conditions, " AND ")+` GROUP BY genre ORDER BY LOWER(genre), genre`, args...)
	if err != nil {
		return nil, fmt.Errorf("abs_media_store: list genres: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var genre string
		if err := rows.Scan(&genre); err != nil {
			return nil, err
		}
		out = append(out, genre)
	}
	return out, rows.Err()
}

func (s *ABSMediaStore) listLibraryStringValues(ctx context.Context, libraryID int64, access catalog.AccessFilter, from, value string) ([]string, error) {
	if s.Pool == nil {
		return []string{}, nil
	}
	itemType, err := s.libraryItemType(ctx, libraryID)
	if err != nil {
		return nil, err
	}
	conditions := []string{"mi.type = '" + itemType + "'", value + " <> ''"}
	args := []any{}
	argIdx := 1
	if libraryID != 0 {
		conditions = append(conditions, fmt.Sprintf(`EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = mi.content_id AND mil.media_folder_id = $%d)`, argIdx))
		args = append(args, int(libraryID))
		argIdx++
	}
	appendAudiobookAccessConditions("mi", access, &conditions, &args, &argIdx)
	rows, err := s.Pool.Query(ctx, buildLibraryStringValuesSQL(value, from, conditions), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func buildLibraryStringValuesSQL(value, from string, conditions []string) string {
	return `SELECT ` + value + ` FROM media_items mi ` + from + ` WHERE ` + strings.Join(conditions, " AND ") +
		` GROUP BY ` + value + ` ORDER BY LOWER(` + value + `), ` + value
}

func (s *ABSMediaStore) ListLibraryNarrators(ctx context.Context, libraryID int64, access catalog.AccessFilter) ([]string, error) {
	return s.listLibraryStringValues(ctx, libraryID, access, `JOIN item_people ip ON ip.content_id = mi.content_id AND ip.kind = 8 JOIN people p ON p.id = ip.person_id`, `p.name`)
}
func (s *ABSMediaStore) ListLibraryPublishers(ctx context.Context, libraryID int64, access catalog.AccessFilter) ([]string, error) {
	return s.listLibraryStringValues(ctx, libraryID, access, `CROSS JOIN LATERAL unnest(COALESCE(mi.studios, ARRAY[]::text[])) AS publisher`, `publisher`)
}
func (s *ABSMediaStore) ListLibraryLanguages(ctx context.Context, libraryID int64, access catalog.AccessFilter) ([]string, error) {
	return s.listLibraryStringValues(ctx, libraryID, access, ``, `COALESCE(NULLIF(BTRIM(mi.original_language), ''), NULLIF(BTRIM(mi.default_metadata_language), ''), 'en')`)
}

// RefreshAuthorCounts rebuilds the abs_audiobook_author_counts materialized
// view that ListLibraryAuthors reads. CONCURRENTLY keeps it readable during the
// refresh (requires the unique index). Driven by a periodic ticker in the
// audiobooks service.
func (s *ABSMediaStore) RefreshAuthorCounts(ctx context.Context) error {
	if s.Pool == nil {
		return nil
	}
	_, err := s.Pool.Exec(ctx, `REFRESH MATERIALIZED VIEW CONCURRENTLY abs_audiobook_author_counts`)
	if err != nil {
		return fmt.Errorf("abs_media_store: refresh author counts: %w", err)
	}
	return nil
}

// ListLibraryAuthors returns one page of distinct audiobook authors plus the
// total author count for the library. It reads the precomputed
// abs_audiobook_author_counts materialized view keyed by media_folder_id.
//
// The MV is keyed by library_id only and carries no per-item access predicate,
// so it is safe only when the caller has no item-level restriction. When the
// access filter carries an item-level predicate (a content-rating cap or
// excluded media types), reading the MV would leak authors of books the caller
// can't see, so we take the access-aware live path instead. Library-level
// access is already enforced by scoping to a single resolved library.
//
// When the MV read returns zero rows (stale, empty, or not-yet-refreshed view)
// we also fall back to the live GROUP BY so /authors never blanks out.
func (s *ABSMediaStore) ListLibraryAuthors(ctx context.Context, libraryID int64, limit, offset int, sortBy string, sortDesc bool, access catalog.AccessFilter) ([]abs.AuthorSummary, int, error) {
	if s.Pool == nil {
		return nil, 0, nil
	}
	if offset < 0 {
		offset = 0
	}
	itemType, err := s.libraryItemType(ctx, libraryID)
	if err != nil {
		return nil, 0, err
	}
	// The materialized view deliberately contains audiobooks only. Ebooks use
	// the indexed live aggregation until they receive their own aggregate.
	if itemType == "ebook" {
		return s.listLibraryAuthorsLive(ctx, libraryID, limit, offset, sortBy, sortDesc, access)
	}
	if access.MaxContentRating != "" || len(access.ExcludedMediaTypes) > 0 {
		return s.listLibraryAuthorsLive(ctx, libraryID, limit, offset, sortBy, sortDesc, access)
	}
	var total int
	if err := s.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM abs_audiobook_author_counts WHERE library_id = $1`, int(libraryID),
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("abs_media_store: count authors: %w", err)
	}
	if total == 0 {
		// Stale/empty/unrefreshed MV: fall back to the live query so a fresh
		// deploy (before the first REFRESH) still serves authors.
		return s.listLibraryAuthorsLive(ctx, libraryID, limit, offset, sortBy, sortDesc, access)
	}

	dir := "ASC"
	if sortDesc {
		dir = "DESC"
	}
	var orderBy string
	switch sortBy {
	case "addedAt":
		orderBy = "c.added_at " + dir + ", c.person_id"
	case "numBooks":
		orderBy = "c.num_books " + dir + ", LOWER(c.name)"
	default: // name
		orderBy = "LOWER(c.name) " + dir
	}

	// The MV carries no photo column; join people at read time so photo
	// presence is always current instead of stale until the next REFRESH.
	dataSQL := `SELECT c.person_id, c.name, c.num_books, c.added_at,
			COALESCE(p.photo_path, '') <> ''
		FROM abs_audiobook_author_counts c
		LEFT JOIN people p ON p.id = c.person_id
		WHERE c.library_id = $1 ORDER BY ` + orderBy
	args := []any{int(libraryID)}
	if limit > 0 {
		dataSQL += ` LIMIT $2 OFFSET $3`
		args = append(args, limit, offset)
	}
	rows, err := s.Pool.Query(ctx, dataSQL, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("abs_media_store: list authors: %w", err)
	}
	defer rows.Close()
	out := make([]abs.AuthorSummary, 0, 64)
	for rows.Next() {
		var (
			id       int64
			name     string
			books    int
			addedAt  time.Time
			hasPhoto bool
		)
		if err := rows.Scan(&id, &name, &books, &addedAt, &hasPhoto); err != nil {
			return nil, 0, fmt.Errorf("abs_media_store: scan author: %w", err)
		}
		out = append(out, abs.AuthorSummary{ID: fmt.Sprintf("%d", id), Name: name, NumBooks: books, HasPhoto: hasPhoto})
	}
	return out, total, rows.Err()
}

// listLibraryAuthorsLive is the pre-materialized-view live aggregation, kept as
// a fallback for ListLibraryAuthors when the MV has no rows for the library.
func (s *ABSMediaStore) listLibraryAuthorsLive(ctx context.Context, libraryID int64, limit, offset int, sortBy string, sortDesc bool, access catalog.AccessFilter) ([]abs.AuthorSummary, int, error) {
	itemType, err := s.libraryItemType(ctx, libraryID)
	if err != nil {
		return nil, 0, err
	}
	conditions := []string{"mi.type = '" + itemType + "'"}
	args := []any{}
	argIdx := 1
	if libraryID != 0 {
		conditions = append(conditions, fmt.Sprintf(`EXISTS (
			SELECT 1 FROM media_item_libraries mil
			WHERE mil.content_id = mi.content_id AND mil.media_folder_id = $%d
		)`, argIdx))
		args = append(args, int(libraryID))
		argIdx++
	}
	appendAudiobookAccessConditions("mi", access, &conditions, &args, &argIdx)
	where := strings.Join(conditions, " AND ")

	var total int
	countSQL := `SELECT COUNT(*) FROM (
		SELECT p.id
		FROM media_items mi
		JOIN item_people ip ON ip.content_id = mi.content_id AND ip.kind = 7
		JOIN people p ON p.id = ip.person_id
		WHERE ` + where + `
		GROUP BY p.id, p.name
	) t`
	if err := s.Pool.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("abs_media_store: count authors (live): %w", err)
	}
	if total == 0 {
		return []abs.AuthorSummary{}, 0, nil
	}

	dir := "ASC"
	if sortDesc {
		dir = "DESC"
	}
	var orderBy string
	switch sortBy {
	case "addedAt":
		orderBy = "p.created_at " + dir + ", p.id"
	case "numBooks":
		orderBy = "num_books " + dir + ", LOWER(p.name)"
	default: // name
		orderBy = "LOWER(p.name) " + dir
	}
	dataSQL := `
		SELECT p.id, p.name, COUNT(DISTINCT mi.content_id) AS num_books,
			p.photo_path <> ''
		FROM media_items mi
		JOIN item_people ip ON ip.content_id = mi.content_id AND ip.kind = 7
		JOIN people p ON p.id = ip.person_id
		WHERE ` + where + `
		GROUP BY p.id, p.name
		ORDER BY ` + orderBy
	dataArgs := append([]any(nil), args...)
	if limit > 0 {
		dataSQL += fmt.Sprintf(` LIMIT $%d OFFSET $%d`, argIdx, argIdx+1)
		dataArgs = append(dataArgs, limit, offset)
	}
	rows, err := s.Pool.Query(ctx, dataSQL, dataArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("abs_media_store: list authors (live): %w", err)
	}
	defer rows.Close()
	out := make([]abs.AuthorSummary, 0, 64)
	for rows.Next() {
		var (
			id       int64
			name     string
			books    int
			hasPhoto bool
		)
		if err := rows.Scan(&id, &name, &books, &hasPhoto); err != nil {
			return nil, 0, fmt.Errorf("abs_media_store: scan author (live): %w", err)
		}
		out = append(out, abs.AuthorSummary{ID: fmt.Sprintf("%d", id), Name: name, NumBooks: books, HasPhoto: hasPhoto})
	}
	return out, total, rows.Err()
}

// ListLibrarySeries returns distinct series from audiobook_series for the
// audiobook library, with per-series book count and up to 4 book preview
// rows (content_id + title + updated_at) used by the ABS mobile client
// to render the LazySeriesCard cover stack.
func (s *ABSMediaStore) ListLibrarySeries(ctx context.Context, libraryID int64, limit, offset int, access catalog.AccessFilter) ([]abs.SeriesSummary, int, error) {
	if s.Pool == nil {
		return nil, 0, nil
	}
	if offset < 0 {
		offset = 0
	}
	itemType, err := s.libraryItemType(ctx, libraryID)
	if err != nil {
		return nil, 0, err
	}
	conditions := []string{"mi.type = '" + itemType + "'"}
	args := []any{}
	argIdx := 1
	if libraryID != 0 {
		conditions = append(conditions, fmt.Sprintf(`EXISTS (
			SELECT 1 FROM media_item_libraries mil
			WHERE mil.content_id = mi.content_id AND mil.media_folder_id = $%d
		)`, argIdx))
		args = append(args, int(libraryID))
		argIdx++
	}
	appendAudiobookAccessConditions("mi", access, &conditions, &args, &argIdx)
	where := strings.Join(conditions, " AND ")
	seriesTable := bookSeriesTable(itemType)

	// Count distinct multi-book series (HAVING COUNT > 1) so pagination totals
	// match what the data query returns.
	var total int
	countSQL := `SELECT COUNT(*) FROM (
		SELECT s.series_name
		FROM ` + seriesTable + ` s JOIN media_items mi ON mi.content_id = s.content_id
		WHERE ` + where + `
		GROUP BY s.series_name HAVING COUNT(*) > 1
	) t`
	if err := s.Pool.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("abs_media_store: count series: %w", err)
	}
	if total == 0 {
		return []abs.SeriesSummary{}, 0, nil
	}

	// Two-stage: window-rank books inside each series (lowest series_index
	// first), then aggregate the top 4 ids/titles/updated_at into parallel
	// arrays. `book_ids[]` is text because content_id is text in this
	// schema; parallel `titles[]` and `updated_ats[]` keep iteration
	// straightforward in Go without composite type plumbing.
	dataSQL := `
		WITH ranked AS (
			SELECT
				s.series_name,
				s.content_id,
				mi.title,
				mi.updated_at,
				ROW_NUMBER() OVER (
					PARTITION BY s.series_name
					ORDER BY COALESCE(s.series_index, 999999), s.content_id
				) AS rn,
				COUNT(*) OVER (PARTITION BY s.series_name) AS series_count
			FROM ` + seriesTable + ` s
			JOIN media_items mi
				ON mi.content_id = s.content_id
			WHERE ` + where + `
		)
		SELECT
			series_name,
			MAX(series_count)::int AS num_books,
			array_agg(content_id ORDER BY rn) FILTER (WHERE rn <= 4) AS book_ids,
			array_agg(title      ORDER BY rn) FILTER (WHERE rn <= 4) AS titles,
			array_agg(updated_at ORDER BY rn) FILTER (WHERE rn <= 4) AS updated_ats
		FROM ranked
		GROUP BY series_name
		HAVING MAX(series_count) > 1
		ORDER BY LOWER(series_name)`
	dataArgs := append([]any(nil), args...)
	if limit > 0 {
		dataSQL += fmt.Sprintf(` LIMIT $%d OFFSET $%d`, argIdx, argIdx+1)
		dataArgs = append(dataArgs, limit, offset)
	}
	rows, err := s.Pool.Query(ctx, dataSQL, dataArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("abs_media_store: list series: %w", err)
	}
	defer rows.Close()
	out := make([]abs.SeriesSummary, 0, 64)
	for rows.Next() {
		var (
			name       string
			books      int
			ids        []string
			titles     []string
			updatedAts []time.Time
		)
		if err := rows.Scan(&name, &books, &ids, &titles, &updatedAts); err != nil {
			return nil, 0, fmt.Errorf("abs_media_store: scan series: %w", err)
		}
		previews := make([]abs.SeriesBookPreview, 0, len(ids))
		for i := range ids {
			p := abs.SeriesBookPreview{ContentID: ids[i]}
			if i < len(titles) {
				p.Title = titles[i]
			}
			if i < len(updatedAts) {
				p.UpdatedAt = updatedAts[i]
			}
			previews = append(previews, p)
		}
		// Series ID is the canonical series name — there's no first-class
		// series row yet, so the slug is stable for a given name.
		out = append(out, abs.SeriesSummary{ID: name, Name: name, NumBooks: books, Books: previews})
	}
	return out, total, rows.Err()
}

// GetAuthorByID looks up the author by people.id and returns the row
// plus their audiobooks.
func (s *ABSMediaStore) GetAuthorByID(ctx context.Context, authorID string, access catalog.AccessFilter) (abs.Author, error) {
	if s.Pool == nil {
		return abs.Author{}, abs.ErrNotFound
	}
	id, err := strconv.Atoi(authorID)
	if err != nil {
		return abs.Author{}, abs.ErrNotFound
	}
	var name, photo string
	row := s.Pool.QueryRow(ctx, `SELECT name, photo_path FROM people WHERE id = $1`, id)
	if err := row.Scan(&name, &photo); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return abs.Author{}, abs.ErrNotFound
		}
		return abs.Author{}, fmt.Errorf("abs_media_store: get author: %w", err)
	}
	author := abs.Author{ID: authorID, Name: name, PosterPath: photo}
	conditions := []string{`ip.person_id = $1`, `ip.kind = 7`, `mi.type IN ('audiobook', 'ebook')`}
	args := []any{id}
	argIdx := 2
	appendAudiobookAccessConditions("mi", access, &conditions, &args, &argIdx)
	items, err := s.listAudiobookIDs(ctx, `
		SELECT mi.content_id
		FROM item_people ip
		JOIN media_items mi ON mi.content_id = ip.content_id
		WHERE `+strings.Join(conditions, " AND ")+`
		ORDER BY LOWER(mi.title)`, args)
	if err != nil {
		return abs.Author{}, fmt.Errorf("abs_media_store: get author books: %w", err)
	}
	author.Books = items
	return author, nil
}

// GetSeriesByName looks up a series case-insensitively, plus its books.
func (s *ABSMediaStore) GetSeriesByName(ctx context.Context, seriesName string, access catalog.AccessFilter) (abs.Series, error) {
	if s.Pool == nil {
		return abs.Series{}, abs.ErrNotFound
	}
	var canonicalName string
	row := s.Pool.QueryRow(ctx, `
		SELECT series_name FROM (
			SELECT series_name FROM audiobook_series
			UNION ALL
			SELECT series_name FROM ebook_series
		) book_series
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
	conditions := []string{`LOWER(book_series.series_name) = LOWER($1)`, `mi.type IN ('audiobook', 'ebook')`}
	args := []any{seriesName}
	argIdx := 2
	appendAudiobookAccessConditions("mi", access, &conditions, &args, &argIdx)
	items, err := s.listAudiobookIDs(ctx, `
		SELECT mi.content_id
		FROM (
			SELECT content_id, series_name, series_index FROM audiobook_series
			UNION ALL
			SELECT content_id, series_name, series_index FROM ebook_series
		) book_series
		JOIN media_items mi ON mi.content_id = book_series.content_id
		WHERE `+strings.Join(conditions, " AND ")+`
		ORDER BY book_series.series_index NULLS LAST, LOWER(mi.title)`, args)
	if err != nil {
		return abs.Series{}, fmt.Errorf("abs_media_store: get series books: %w", err)
	}
	series.Books = items
	return series, nil
}
