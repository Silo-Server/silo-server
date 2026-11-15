package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/database/pglock"
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

	if n == 0 {
		// A library mid-scan reads zero until its first books land; caching
		// that would hide them for the whole TTL.
		return 0, nil
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

// GetAudiobookByID returns the media_item with the given content_id, provided
// it is of type 'audiobook'. Returns nil and a wrapped error for any other
// outcome; the caller interprets a nil result as not-found.
func (s *ABSMediaStore) GetAudiobookByID(ctx context.Context, contentID string, access catalog.AccessFilter) (*models.MediaItem, error) {
	items, err := s.Items.GetByIDsWithAccess(ctx, []string{contentID}, access)
	if err != nil {
		return nil, fmt.Errorf("abs_media_store: get audiobook %q: %w", contentID, err)
	}
	if len(items) == 0 {
		return nil, nil
	}
	item := items[0]
	if item == nil || item.Type != "audiobook" {
		return nil, nil
	}
	// Hydrate authors + narrators so the ABS metadata mapper can fill
	// authorName / narratorName on the response.
	s.hydrateListing(ctx, []*models.MediaItem{item}, access, 0)
	return item, nil
}

// GetAudiobooksByIDs batch-fetches audiobooks by content_id, hydrating people
// and series once for the whole set. List/shelf handlers (continue-listening,
// similar, my-progress) previously called GetAudiobookByID per row, issuing a
// handful of queries per item — up to ~500 single fetches on app open. Returns
// a map keyed by content_id; missing or non-audiobook ids are omitted.
func (s *ABSMediaStore) GetAudiobooksByIDs(ctx context.Context, contentIDs []string, access catalog.AccessFilter) (map[string]*models.MediaItem, error) {
	contentIDs, err := s.idsInALibrary(ctx, contentIDs)
	if err != nil {
		return nil, fmt.Errorf("abs_media_store: get audiobooks: %w", err)
	}
	if len(contentIDs) == 0 {
		return map[string]*models.MediaItem{}, nil
	}
	items, err := s.Items.GetByIDsWithAccess(ctx, contentIDs, access)
	if err != nil {
		return nil, fmt.Errorf("abs_media_store: get audiobooks: %w", err)
	}
	books := make([]*models.MediaItem, 0, len(items))
	for _, item := range items {
		if item != nil && item.Type == "audiobook" {
			books = append(books, item)
		}
	}
	if len(books) == 0 {
		return map[string]*models.MediaItem{}, nil
	}
	// Hydrate the whole set in a few queries rather than per item.
	s.hydrateListing(ctx, books, access, 0)
	out := make(map[string]*models.MediaItem, len(books))
	for _, b := range books {
		out[b.ContentID] = b
	}
	return out, nil
}

// idsInALibrary returns the contentIDs that belong to at least one library.
// GetAudiobooksByIDs hydrates listings, and progress keeps the IDs of books in
// no library (held through the file removal grace), which an unrestricted
// viewer's by-ID read would otherwise return. See catalog.InSomeLibraryWhere.
func (s *ABSMediaStore) idsInALibrary(ctx context.Context, contentIDs []string) ([]string, error) {
	if len(contentIDs) == 0 || s.Pool == nil {
		return contentIDs, nil
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT cid FROM unnest($1::text[]) AS cid WHERE `+catalog.InSomeLibraryWhere("cid"), contentIDs)
	if err != nil {
		return nil, fmt.Errorf("load library memberships: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// ListAudiobooks returns a page of media_items with type='audiobook'.
// When libraryID is non-zero, results are filtered to items in that
// media_folder (via the media_item_libraries junction); 0 means all libraries.
// q.Filter and q.Sort are applied in SQL, so a filtered or sorted page never
// loads the rest of the library. limit <= 0 returns every match.
//
// We can't reuse ItemRepository.Search because Search is a text-search
// path that bails out when the query string is empty. We page content_ids
// here via SQL, then load full rows via GetByIDs so the scan logic stays
// in the catalog package.
func (s *ABSMediaStore) ListAudiobooks(ctx context.Context, libraryID int64, limit, offset int, access catalog.AccessFilter, q abs.ItemsQuery) ([]*models.MediaItem, int, error) {
	items, _, total, err := s.listAudiobooks(ctx, libraryID, limit, offset, access, q, false, nil)
	return items, total, err
}

// absStreamBatch is how many books StreamAudiobooks loads and hands on at a
// time.
const absStreamBatch = 1000

// StreamAudiobooks is ListAudiobooks with no limit, handing the books to emit
// in order, absStreamBatch at a time, so a whole-library response never holds
// every hydrated book at once. It returns the total before the first emit
// through onTotal.
func (s *ABSMediaStore) StreamAudiobooks(ctx context.Context, libraryID int64, access catalog.AccessFilter, q abs.ItemsQuery, onTotal func(int) error, emit func([]*models.MediaItem) error) error {
	_, _, _, err := s.listAudiobooks(ctx, libraryID, 0, 0, access, q, false, &absStream{onTotal: onTotal, emit: emit})
	return err
}

// absStream receives a whole-library listing in batches.
type absStream struct {
	onTotal func(int) error
	emit    func([]*models.MediaItem) error
}

// ListAudiobooksCollapsed is ListAudiobooks with each series folded into one
// entry, as ABS collapseseries=1 asks: the series' first book in the
// requested order stands for it, carrying the IDs of every matching book in
// the series. Books without a series pass through. The folding and paging run
// in SQL, so a page loads only its own entries.
func (s *ABSMediaStore) ListAudiobooksCollapsed(ctx context.Context, libraryID int64, limit, offset int, access catalog.AccessFilter, q abs.ItemsQuery) ([]abs.CollapsedAudiobook, int, error) {
	items, members, total, err := s.listAudiobooks(ctx, libraryID, limit, offset, access, q, true, nil)
	if err != nil {
		return nil, 0, err
	}
	out := make([]abs.CollapsedAudiobook, len(items))
	for i, item := range items {
		out[i] = abs.CollapsedAudiobook{Item: item}
		if m, ok := members[item.ContentID]; ok {
			out[i].SeriesID, out[i].SeriesName, out[i].SeriesBookIDs = m.key, m.name, m.books
		}
	}
	return out, total, nil
}

// audiobookSeriesKey is the abs.SeriesID grouping of a book's series in SQL,
// NULL for a book without one.
const audiobookSeriesKey = `(SELECT LOWER(asx_key.series_name) FROM audiobook_series asx_key WHERE asx_key.content_id = mi.content_id)`

// collapsedSeries is one folded series on a collapsed page.
type collapsedSeries struct {
	key, name string
	books     []string
}

func (s *ABSMediaStore) listAudiobooks(ctx context.Context, libraryID int64, limit, offset int, access catalog.AccessFilter, q abs.ItemsQuery, collapse bool, stream *absStream) ([]*models.MediaItem, map[string]collapsedSeries, int, error) {
	if s.Pool == nil {
		return nil, nil, 0, errors.New("abs_media_store: no pgx pool")
	}
	if offset < 0 {
		offset = 0
	}

	conditions := []string{"mi.type = 'audiobook'"}
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
	viewer := newABSViewer(q.UserID, q.ProfileID)
	appendAudiobookFilterConditions(q.Filter, viewer, &conditions, &args, &argIdx)
	where := strings.Join(conditions, " AND ")

	countSQL := `SELECT COUNT(*) FROM media_items mi WHERE ` + where
	if collapse {
		countSQL = `SELECT COUNT(*) FILTER (WHERE skey IS NULL) + COUNT(DISTINCT skey)
			FROM (SELECT ` + audiobookSeriesKey + ` AS skey FROM media_items mi WHERE ` + where + `) f`
	}
	var total int
	var err error
	if q.Filter.Kind == abs.FilterProgress {
		// Progress changes with every listen; a cached total would disagree
		// with the page for up to the cache TTL.
		err = s.Pool.QueryRow(ctx, countSQL, args...).Scan(&total)
	} else {
		total, err = s.cachedAudiobookCount(ctx, countSQL, args)
	}
	if err != nil {
		return nil, nil, 0, fmt.Errorf("abs_media_store: count audiobooks: %w", err)
	}
	if total == 0 {
		return []*models.MediaItem{}, nil, 0, nil
	}

	dataArgs := append([]any(nil), args...)
	argIdx = len(dataArgs) + 1
	join, orderBy := audiobookItemsOrder(q, libraryID, viewer, access, &dataArgs, &argIdx)
	dataSQL := `SELECT mi.content_id FROM media_items mi` + join + ` WHERE ` + where + ` ORDER BY ` + orderBy
	if collapse {
		// Number the matches in the requested order, keep each series' first,
		// and page over what is left.
		dataSQL = `WITH ordered AS (
				SELECT mi.content_id, ` + audiobookSeriesKey + ` AS skey,
					ROW_NUMBER() OVER (ORDER BY ` + orderBy + `) AS ord
				FROM media_items mi` + join + ` WHERE ` + where + `
			), firsts AS (
				SELECT content_id, skey, ord, ROW_NUMBER() OVER (PARTITION BY skey ORDER BY ord) AS rn
				FROM ordered
			)
			SELECT content_id FROM firsts WHERE skey IS NULL OR rn = 1 ORDER BY ord`
	}
	if limit > 0 {
		dataSQL += fmt.Sprintf(` LIMIT $%d OFFSET $%d`, argIdx, argIdx+1)
		dataArgs = append(dataArgs, limit, offset)
	}

	rows, err := s.Pool.Query(ctx, dataSQL, dataArgs...)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("abs_media_store: list audiobooks: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, nil, 0, fmt.Errorf("abs_media_store: list audiobook ids: %w", err)
	}
	if len(ids) == 0 {
		return []*models.MediaItem{}, nil, total, nil
	}

	if stream != nil {
		return nil, nil, total, s.streamListing(ctx, stream, total, ids, access, libraryID)
	}

	ordered, err := s.loadListing(ctx, ids, access, libraryID)
	if err != nil {
		return nil, nil, 0, err
	}
	if !collapse {
		return ordered, nil, total, nil
	}
	members, err := s.collapsedSeriesMembers(ctx, ids, where, args)
	if err != nil {
		return nil, nil, 0, err
	}
	return ordered, members, total, nil
}

// streamListing reports total, then loads and emits ids a batch at a time.
func (s *ABSMediaStore) streamListing(ctx context.Context, stream *absStream, total int, ids []string, access catalog.AccessFilter, libraryID int64) error {
	if err := stream.onTotal(total); err != nil {
		return err
	}
	for start := 0; start < len(ids); start += absStreamBatch {
		batch, err := s.loadListing(ctx, ids[start:min(start+absStreamBatch, len(ids))], access, libraryID)
		if err != nil {
			return err
		}
		if err := stream.emit(batch); err != nil {
			return err
		}
	}
	return nil
}

// loadListing loads and hydrates the books ids names, in that order.
// Each query reads at most absStreamBatch IDs, so a whole-library listing
// never sends one unbounded ID array.
func (s *ABSMediaStore) loadListing(ctx context.Context, ids []string, access catalog.AccessFilter, libraryID int64) ([]*models.MediaItem, error) {
	ordered := make([]*models.MediaItem, 0, len(ids))
	for start := 0; start < len(ids); start += absStreamBatch {
		batch := ids[start:min(start+absStreamBatch, len(ids))]
		items, err := s.Items.GetByIDs(ctx, batch)
		if err != nil {
			return nil, fmt.Errorf("abs_media_store: load audiobooks: %w", err)
		}
		// GetByIDs sorts by content_id ASC; preserve the requested order.
		byID := make(map[string]*models.MediaItem, len(items))
		for _, it := range items {
			byID[it.ContentID] = it
		}
		loaded := make([]*models.MediaItem, 0, len(batch))
		for _, id := range batch {
			if it, ok := byID[id]; ok {
				loaded = append(loaded, it)
			}
		}
		s.hydrateListing(ctx, loaded, access, libraryID)
		ordered = append(ordered, loaded...)
	}
	return ordered, nil
}

// collapsedSeriesMembers returns, for each page entry that stands for a
// series, the series' key, its list name (the lowest spelling among the
// matching books), and the IDs of the books that match the same filter
// (where and args), in series order.
func (s *ABSMediaStore) collapsedSeriesMembers(ctx context.Context, ids []string, where string, args []any) (map[string]collapsedSeries, error) {
	memberArgs := append(append([]any(nil), args...), ids)
	rows, err := s.Pool.Query(ctx, `
		WITH page AS (
			SELECT LOWER(series_name) AS skey, content_id AS rep
			FROM audiobook_series WHERE content_id = ANY($`+strconv.Itoa(len(memberArgs))+`)
		)
		SELECT page.rep, page.skey, MIN(asx.series_name), array_agg(mi.content_id ORDER BY asx.series_index NULLS LAST, `+audiobookTitleSortKey+`, mi.content_id)
		FROM page
		JOIN audiobook_series asx ON LOWER(asx.series_name) = page.skey
		JOIN media_items mi ON mi.content_id = asx.content_id
		WHERE `+where+`
		GROUP BY page.rep, page.skey`, memberArgs...)
	if err != nil {
		return nil, fmt.Errorf("abs_media_store: load collapsed series: %w", err)
	}
	defer rows.Close()
	members := make(map[string]collapsedSeries, len(ids))
	for rows.Next() {
		var rep string
		var m collapsedSeries
		if err := rows.Scan(&rep, &m.key, &m.name, &m.books); err != nil {
			return nil, fmt.Errorf("abs_media_store: scan collapsed series: %w", err)
		}
		members[rep] = m
	}
	return members, rows.Err()
}

// absViewer is the profile whose listening progress a progress filter or
// sort reads. A user ID that is not an integer matches no progress row.
type absViewer struct {
	userID    int
	profileID string
}

func newABSViewer(userID, profileID string) absViewer {
	uid, err := strconv.Atoi(userID)
	if err != nil {
		uid = -1
	}
	return absViewer{userID: uid, profileID: profileID}
}

// ABS sort keys used more than once.
const (
	absSortAddedAt  = "addedAt"
	absSortProgress = "progress"
	absSortTitle    = "media.metadata.title"
)

func sqlDirection(desc bool) string {
	if desc {
		return "DESC"
	}
	return "ASC"
}

// audiobookTitleSortKey is the expression idx_media_items_sort_key is built
// on, so a title-ordered page can come from an ordered index scan.
const audiobookTitleSortKey = `lower(coalesce(nullif(btrim(mi.sort_title), ''), mi.title))`

// audiobookItemsOrder renders the ORDER BY (and any join it needs) for an ABS
// sort key. Keys follow real ABS getOrder (libraryItemsBookFilters.js); the
// ones Silo has no data for (size, progress.createdAt, progress.finishedAt)
// fall back to title order, as does an unknown key. birthtimeMs and mtimeMs
// sort by the added date because that is what Silo reports for both.
// Every order ends on the title key and content_id so pages never skip or
// repeat rows on ties.
func audiobookItemsOrder(q abs.ItemsQuery, libraryID int64, viewer absViewer, access catalog.AccessFilter, args *[]any, argIdx *int) (join, orderBy string) {
	dir := sqlDirection(q.Desc)
	nulls := "NULLS LAST"
	tail := audiobookTitleSortKey + " " + dir + ", mi.content_id " + dir
	byTitle := audiobookTitleSortKey + " ASC, mi.content_id ASC"
	nextArg := func(v any) string {
		*args = append(*args, v)
		ph := fmt.Sprintf("$%d", *argIdx)
		*argIdx = *argIdx + 1
		return ph
	}
	// Every book's joined author names, aggregated once and hash-joined:
	// about a third of the cost of a per-book subquery on a large library.
	authorNames := func(expr string) string {
		return ` LEFT JOIN (SELECT ip_sort.content_id, string_agg(` + expr + `, ', ' ORDER BY ip_sort.sort_order, p_sort.name) AS names
			FROM item_people ip_sort JOIN people p_sort ON p_sort.id = ip_sort.person_id
			WHERE ip_sort.kind = ` + strconv.Itoa(int(models.PersonKindAuthor)) + `
			GROUP BY ip_sort.content_id) an_sort ON an_sort.content_id = mi.content_id`
	}

	// Without a route library, the added date and duration come from the
	// libraries the viewer may see, as hydrateListing reports them.
	visibleFolder := func(col string) string {
		return ` AND (` + nextArg(access.AllowedLibraryIDs) + `::int[] IS NULL OR ` + col + ` = ANY(` + nextArg(access.AllowedLibraryIDs) + `))` +
			` AND NOT (` + col + ` = ANY(COALESCE(` + nextArg(access.DisabledLibraryIDs) + `::int[], '{}')))`
	}

	switch q.Sort {
	case absSortAddedAt, "birthtimeMs", "mtimeMs":
		if libraryID == 0 {
			return "", "(SELECT MIN(mil_sort.first_seen_at) FROM media_item_libraries mil_sort WHERE mil_sort.content_id = mi.content_id" +
				visibleFolder("mil_sort.media_folder_id") + ") " + dir + " " + nulls + ", " + tail
		}
		// The library's own first_seen_at, joined so
		// idx_item_libraries_folder_seen_content
		// can serve the order.
		join = ` JOIN media_item_libraries mil_sort ON mil_sort.content_id = mi.content_id AND mil_sort.media_folder_id = ` + nextArg(int(libraryID))
		return join, "mil_sort.first_seen_at " + dir + ", " + tail
	case "media.duration":
		// The per-folder file total. It can differ from the duration shown
		// (one presentation, see hydrateAudiobookRuntime) only for a book
		// with two versions in one folder; computing that rule for every row
		// would cost a media_files scan per page.
		dur := `(SELECT MAX(fs.duration_seconds) FROM audiobook_item_file_stats fs WHERE fs.content_id = mi.content_id`
		if libraryID != 0 {
			dur += ` AND fs.media_folder_id = ` + nextArg(int(libraryID))
		} else {
			dur += visibleFolder("fs.media_folder_id")
		}
		return "", dur + ") " + dir + " " + nulls + ", " + tail
	case "media.metadata.publishedYear":
		return "", "NULLIF(mi.year, 0) " + dir + " " + nulls + ", " + tail
	case "media.metadata.authorName":
		return authorNames("p_sort.name"), "lower(an_sort.names) " + dir + " " + nulls + ", " + byTitle
	case "media.metadata.authorNameLF":
		// Same rule as lastFirst: the last space-separated word moves first.
		return authorNames(`regexp_replace(btrim(p_sort.name), '^(.+) ([^ ]+)$', '\2, \1')`), "lower(an_sort.names) " + dir + " " + nulls + ", " + byTitle
	case "sequence":
		// Real ABS honors sequence only inside a series filter.
		if q.Filter.Kind == abs.FilterSeries && q.Filter.Value != abs.SentinelNoSeries {
			if q.Desc {
				nulls = "NULLS FIRST"
			}
			return "", "(SELECT asx_sort.series_index FROM audiobook_series asx_sort WHERE asx_sort.content_id = mi.content_id) " + dir + " " + nulls + ", " + byTitle
		}
		return "", byTitle
	case absSortProgress:
		join = ` LEFT JOIN user_watch_progress wp_sort ON wp_sort.media_item_id = mi.content_id AND wp_sort.user_id = ` +
			nextArg(viewer.userID) + ` AND wp_sort.profile_id = ` + nextArg(viewer.profileID)
		return join, "wp_sort.updated_at " + dir + " " + nulls + ", " + byTitle
	case "random":
		return "", "random()"
	case absSortTitle:
		return "", tail
	default:
		return "", byTitle
	}
}

// hydrateListing fills what the ABS mapper reads beyond the media_items row:
// people, series, duration, and the added date. Each is one batched query.
// Failures are non-fatal: the item still renders, without that field.
func (s *ABSMediaStore) hydrateListing(ctx context.Context, items []*models.MediaItem, access catalog.AccessFilter, libraryID int64) {
	for _, err := range []error{
		s.hydratePeople(ctx, items),
		s.hydrateAudiobookSeries(ctx, items),
		s.hydrateAudiobookRuntime(ctx, items, access, libraryID),
		s.hydrateAudiobookAddedAt(ctx, items, access, libraryID),
	} {
		if err != nil {
			slog.WarnContext(ctx, "abs listing hydration failed", "component", "audiobooks", "err", err)
		}
	}
}

// hydrateAudiobookAddedAt sets AddedAt to when the book first appeared in a
// library the viewer may see (the route library when libraryID is non-zero),
// the date the addedAt sort orders by.
func (s *ABSMediaStore) hydrateAudiobookAddedAt(ctx context.Context, items []*models.MediaItem, access catalog.AccessFilter, libraryID int64) error {
	if len(items) == 0 || s.Pool == nil {
		return nil
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if item != nil {
			ids = append(ids, item.ContentID)
		}
	}
	allowed := access.AllowedLibraryIDs
	if libraryID != 0 {
		allowed = []int{int(libraryID)}
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT content_id, MIN(first_seen_at)
		FROM media_item_libraries
		WHERE content_id = ANY($1)
		  AND ($2::int[] IS NULL OR media_folder_id = ANY($2))
		  AND (COALESCE(cardinality($3::int[]), 0) = 0 OR NOT (media_folder_id = ANY($3)))
		GROUP BY content_id`, ids, allowed, access.DisabledLibraryIDs)
	if err != nil {
		return fmt.Errorf("abs_media_store: load audiobook added dates: %w", err)
	}
	defer rows.Close()
	added := make(map[string]time.Time, len(ids))
	for rows.Next() {
		var id string
		var at time.Time
		if err := rows.Scan(&id, &at); err != nil {
			return fmt.Errorf("abs_media_store: scan audiobook added date: %w", err)
		}
		added[id] = at
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("abs_media_store: iterate audiobook added dates: %w", err)
	}
	for _, item := range items {
		if at, ok := added[item.ContentID]; ok {
			item.AddedAt = &at
		}
	}
	return nil
}

// hydrateAudiobookRuntime overlays the canonical duration from the active
// file-stat rollup. MediaItem.Runtime is a video-era integer measured in
// minutes; ABS exposes audiobook durations in seconds, so consumers convert
// the resulting minutes to seconds at the wire boundary. Keeping the catalog
// unit intact avoids changing the native API contract while ensuring library
// listings have a useful duration even when a scan did not populate Runtime.
//
// The rollup is scoped to the folders the caller may serve: one content ID can
// be linked to copies in several libraries, and an unrestricted MAX would let
// an inaccessible copy's duration decide what the viewer sees. A non-zero
// route library pins the rollup to that folder; otherwise the access
// allowlist applies, with disabled libraries excluded either way.
func (s *ABSMediaStore) hydrateAudiobookRuntime(ctx context.Context, items []*models.MediaItem, access catalog.AccessFilter, libraryID int64) error {
	if len(items) == 0 || s.Pool == nil {
		return nil
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if item != nil {
			ids = append(ids, item.ContentID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	allowed := access.AllowedLibraryIDs
	if libraryID != 0 {
		allowed = []int{int(libraryID)}
	}
	rows, err := s.Pool.Query(ctx, `
		WITH presentations AS (
			SELECT content_id, media_folder_id,
			       COALESCE(presentation_group_key, '') AS presentation_key,
			       SUM(COALESCE(duration, 0)) AS duration_seconds
			FROM media_files
			WHERE content_id = ANY($1) AND missing_since IS NULL
			  AND ($2::int[] IS NULL OR media_folder_id = ANY($2))
			  AND (COALESCE(cardinality($3::int[]), 0) = 0 OR NOT (media_folder_id = ANY($3)))
			GROUP BY content_id, media_folder_id, COALESCE(presentation_group_key, '')
		), chosen AS (
			SELECT content_id, duration_seconds,
			       ROW_NUMBER() OVER (PARTITION BY content_id ORDER BY media_folder_id, presentation_key) AS rn
			FROM presentations
		)
		SELECT content_id, duration_seconds FROM chosen WHERE rn = 1`, ids, allowed, access.DisabledLibraryIDs)
	if err != nil {
		return fmt.Errorf("abs_media_store: load audiobook durations: %w", err)
	}
	defer rows.Close()
	durations := make(map[string]float64, len(ids))
	for rows.Next() {
		var id string
		var seconds float64
		if err := rows.Scan(&id, &seconds); err != nil {
			return fmt.Errorf("abs_media_store: scan audiobook duration: %w", err)
		}
		durations[id] = seconds
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("abs_media_store: iterate audiobook durations: %w", err)
	}
	for _, item := range items {
		if seconds, ok := durations[item.ContentID]; ok && seconds > 0 {
			item.AudiobookDurationSeconds = int(math.Round(seconds))
			item.Runtime = int(math.Round(seconds / 60))
		}
	}
	return nil
}

// appendAudiobookFilterConditions renders an ABS filter as a SQL predicate.
// Author values are the numeric person_id (as returned by the /authors
// endpoint), with a name fallback for non-numeric values; narrator values are
// names. A series value is a series ID or name, compared case-insensitively
// (see abs.SeriesID). Progress filters read only the viewer's own profile.
// Tags match nothing because Silo audiobooks carry none, and a filter kind
// Silo has no data for matches nothing rather than everything.
func appendAudiobookFilterConditions(filter abs.Filter, viewer absViewer, conditions *[]string, args *[]any, argIdx *int) {
	if filter.Kind == "" {
		return
	}
	nextArg := func(v any) string {
		*args = append(*args, v)
		ph := fmt.Sprintf("$%d", *argIdx)
		*argIdx = *argIdx + 1
		return ph
	}
	add := func(cond string) { *conditions = append(*conditions, cond) }
	switch filter.Kind {
	case abs.FilterAuthors:
		if id, err := strconv.ParseInt(filter.Value, 10, 64); err == nil {
			add(fmt.Sprintf(
				`EXISTS (SELECT 1 FROM item_people ip WHERE ip.content_id = mi.content_id AND ip.kind = %d AND ip.person_id = %s)`,
				models.PersonKindAuthor, nextArg(id)))
		} else {
			add(fmt.Sprintf(
				`EXISTS (SELECT 1 FROM item_people ip JOIN people p ON p.id = ip.person_id WHERE ip.content_id = mi.content_id AND ip.kind = %d AND p.name = %s)`,
				models.PersonKindAuthor, nextArg(filter.Value)))
		}
	case abs.FilterNarrators:
		add(fmt.Sprintf(
			`EXISTS (SELECT 1 FROM item_people ip JOIN people p ON p.id = ip.person_id WHERE ip.content_id = mi.content_id AND ip.kind = %d AND p.name = %s)`,
			models.PersonKindNarrator, nextArg(filter.Value)))
	case abs.FilterSeries:
		if filter.Value == abs.SentinelNoSeries {
			add(`NOT EXISTS (SELECT 1 FROM audiobook_series abs WHERE abs.content_id = mi.content_id)`)
		} else {
			add(`EXISTS (SELECT 1 FROM audiobook_series abs WHERE abs.content_id = mi.content_id AND LOWER(abs.series_name) = LOWER(` + nextArg(filter.Value) + `))`)
		}
	case abs.FilterGenres:
		// @> lets idx_media_items_genres serve the match.
		add(`mi.genres @> ARRAY[` + nextArg(filter.Value) + `::text]`)
	case abs.FilterLanguages:
		// The same value audiobookLanguage reports on the wire.
		add(`COALESCE(NULLIF(btrim(mi.original_language), ''), btrim(mi.default_metadata_language)) = ` + nextArg(filter.Value))
	case abs.FilterProgress:
		progress := func(cond string) string {
			return `EXISTS (SELECT 1 FROM user_watch_progress wp WHERE wp.user_id = ` + nextArg(viewer.userID) +
				` AND wp.profile_id = ` + nextArg(viewer.profileID) + ` AND wp.media_item_id = mi.content_id AND ` + cond + `)`
		}
		switch filter.Value {
		case abs.ProgressInProgress, abs.ProgressAudioInProgress:
			add(progress(`wp.position_seconds > 0 AND NOT wp.completed`))
		case abs.ProgressFinished:
			add(progress(`wp.completed`))
		case abs.ProgressNotFinished:
			add(`NOT ` + progress(`wp.completed`))
		case abs.ProgressNotStarted:
			add(`NOT ` + progress(`(wp.position_seconds > 0 OR wp.completed)`))
		default:
			add(`FALSE`)
		}
	default:
		add(`FALSE`)
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
	} else {
		*conditions = append(*conditions, catalog.InSomeLibraryWhere(alias+".content_id"))
	}
	if len(filter.DisabledLibraryIDs) > 0 {
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

func (s *ABSMediaStore) hydrateAudiobookSeries(ctx context.Context, items []*models.MediaItem) error {
	if len(items) == 0 || s.Pool == nil {
		return nil
	}
	ids := make([]string, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ContentID)
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT content_id, series_name, LOWER(series_name), COALESCE(series_index::text, '')
		FROM audiobook_series
		WHERE content_id = ANY($1)
		ORDER BY content_id, series_index NULLS LAST, series_name
	`, ids)
	if err != nil {
		return fmt.Errorf("abs_media_store: load audiobook_series: %w", err)
	}
	defer rows.Close()
	grouped := make(map[string][]models.AudiobookSeriesMembership, len(items))
	for rows.Next() {
		var contentID, name, key, indexRaw string
		if err := rows.Scan(&contentID, &name, &key, &indexRaw); err != nil {
			return fmt.Errorf("abs_media_store: scan audiobook_series: %w", err)
		}
		membership := models.AudiobookSeriesMembership{Name: name, Key: key}
		if indexRaw != "" {
			if f, err := strconv.ParseFloat(indexRaw, 64); err == nil {
				membership.Index = &f
			}
		}
		grouped[contentID] = append(grouped[contentID], membership)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("abs_media_store: iterate audiobook_series: %w", err)
	}
	for _, it := range items {
		it.AudiobookSeries = grouped[it.ContentID]
	}
	return nil
}

// GetMediaFiles returns all media_files for the given content_id, ordered by
// file_path so ABS clients receive a stable chapter ordering. Like
// GetAudiobookByID it serves audiobooks only: any other item, such as a movie
// the profile can see, has no files here, so the file routes cannot stream it.
func (s *ABSMediaStore) GetMediaFiles(ctx context.Context, contentID string, access catalog.AccessFilter) ([]*models.MediaFile, error) {
	items, err := s.Items.GetByIDsWithAccess(ctx, []string{contentID}, access)
	if err != nil {
		return nil, fmt.Errorf("abs_media_store: check media file access for %q: %w", contentID, err)
	}
	if len(items) == 0 || items[0] == nil || items[0].Type != "audiobook" {
		return []*models.MediaFile{}, nil
	}
	files, err := s.Files.GetByContentIDPresentation(ctx, contentID)
	if err != nil {
		return nil, fmt.Errorf("abs_media_store: get media files for %q: %w", contentID, err)
	}
	return catalog.FilterMediaFilesByAccess(files, access), nil
}

// ListAudiobookLibraries returns media_folder rows where type='audiobooks'
// (the canonical silo type for the audiobooks sub-plan).
func (s *ABSMediaStore) ListAudiobookLibraries(ctx context.Context, access catalog.AccessFilter) ([]abs.AudiobookLibrary, error) {
	if s.Pool == nil {
		return nil, nil
	}
	conditions := []string{"type IN ('audiobooks', 'audiobook')", "enabled = TRUE"}
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

// listAudiobookIDs is the shared helper used by Search/ContinueListening/
// RecentlyAdded/Discover. It runs a parameterized SQL fragment that yields
// a list of audiobook content_ids; the caller composes the WHERE/ORDER
// portions. Returned items have People hydrated.
func (s *ABSMediaStore) listAudiobookIDs(ctx context.Context, sql string, args []any, access catalog.AccessFilter, libraryID int64) ([]*models.MediaItem, error) {
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
	return s.loadListing(ctx, ids, access, libraryID)
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

	sql := `
		SELECT content_id FROM (
			SELECT mi.content_id AS content_id, mi.sort_title AS sort_title, mi.title AS title,
			       CASE WHEN mi.title_normalized LIKE normalize_search_text($2) || '%' THEN 0 ELSE 1 END AS rank
			FROM media_items mi
			WHERE mi.type = 'audiobook'
			  AND normalize_search_text($2) <> ''
			  AND mi.title_normalized ILIKE '%' || normalize_search_text($2) || '%'` + scope + `
			UNION ALL
			SELECT mi.content_id AS content_id, mi.sort_title AS sort_title, mi.title AS title, 2 AS rank
			FROM media_items mi
			WHERE mi.type = 'audiobook'
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
		LIMIT $` + strconv.Itoa(argIdx) + `
	`
	return s.listAudiobookIDs(ctx, sql, args, access, libraryID)
}

// ListContinueListening returns audiobooks the profile has in progress (and
// hasn't finished). userID is the silo integer-id-as-string from the ABS
// JWT. Only that profile's progress counts: an empty profileID is an account
// without profiles, whose rows carry an empty profile_id, so it never reads
// another household member's progress.
func (s *ABSMediaStore) ListContinueListening(ctx context.Context, userID, profileID string, libraryID int64, limit int, access catalog.AccessFilter) ([]*models.MediaItem, error) {
	if userID == "" {
		return []*models.MediaItem{}, nil
	}
	if limit <= 0 {
		limit = 10
	}
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return nil, fmt.Errorf("abs_media_store: continue listening: invalid user_id %q: %w", userID, err)
	}
	args := []any{uid, profileID, limit}
	conditions := []string{
		`mi.type = 'audiobook'`,
		`wp.user_id = $1`,
		`wp.profile_id = $2`,
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
	return s.listAudiobookIDs(ctx, sql, args, access, libraryID)
}

// ListRecentlyAdded returns the most recently added audiobooks. Added-at
// for audiobooks comes from MIN(first_seen_at) in media_item_libraries.
func (s *ABSMediaStore) ListRecentlyAdded(ctx context.Context, libraryID int64, limit int, access catalog.AccessFilter) ([]*models.MediaItem, error) {
	if limit <= 0 {
		limit = 10
	}
	args := []any{limit}
	conditions := []string{`mi.type = 'audiobook'`}
	argIdx := 2
	if libraryID != 0 {
		args = append(args, int(libraryID))
		argIdx++
	}
	appendAudiobookAccessConditions("mi", access, &conditions, &args, &argIdx)
	if libraryID != 0 {
		// Walk idx_item_libraries_folder_seen_content newest first and stop at
		// limit, instead of computing every book's added date and sorting them
		// all. The content_id direction matches the index.
		sql := `
			SELECT mi.content_id FROM media_item_libraries mil
			JOIN media_items mi ON mi.content_id = mil.content_id
			WHERE mil.media_folder_id = $2 AND ` + strings.Join(conditions, " AND ") + `
			ORDER BY mil.first_seen_at DESC, mil.content_id ASC
			LIMIT $1
		`
		return s.listAudiobookIDs(ctx, sql, args, access, libraryID)
	}
	sql := `
		SELECT mi.content_id FROM media_items mi
		JOIN LATERAL (
		  SELECT MIN(first_seen_at) AS added_at
		  FROM media_item_libraries mil
		  WHERE mil.content_id = mi.content_id
		) added ON added.added_at IS NOT NULL
		WHERE ` + strings.Join(conditions, " AND ") + `
		ORDER BY added.added_at DESC
		LIMIT $1
	`
	return s.listAudiobookIDs(ctx, sql, args, access, libraryID)
}

// ListDiscover returns a random sampling of audiobooks for the home
// Discover shelf. Uses TABLESAMPLE for cheap random sampling on large
// libraries (38k+ books); falls back to ORDER BY random() for tiny libs.
func (s *ABSMediaStore) ListDiscover(ctx context.Context, libraryID int64, limit int, access catalog.AccessFilter) ([]*models.MediaItem, error) {
	if limit <= 0 {
		limit = 10
	}
	args := []any{limit}
	conditions := []string{`mi.type = 'audiobook'`, `COALESCE(mi.poster_path, '') <> ''`}
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
	return s.listAudiobookIDs(ctx, sql, args, access, libraryID)
}

// authorCountsRefreshLock serializes RefreshAuthorCounts across API nodes.
const authorCountsRefreshLock int64 = 0x4142_5341_5554_4831 // "ABSAUTH1"

// RefreshAuthorCounts rebuilds the abs_audiobook_author_counts materialized
// view that ListLibraryAuthors reads, unless some node already did within
// minInterval. Every API node calls it on a ticker; the advisory lock and the
// abs_author_counts_refresh row make the cluster refresh once per interval.
// CONCURRENTLY keeps the view readable during the refresh (requires the
// unique index). Every statement runs on the connection that holds the lock,
// so a pool of one connection does not wait on itself.
func (s *ABSMediaStore) RefreshAuthorCounts(ctx context.Context, minInterval time.Duration) error {
	if s.Pool == nil {
		return nil
	}
	lock, acquired, err := pglock.TryAcquire(ctx, s.Pool, authorCountsRefreshLock)
	if err != nil {
		return fmt.Errorf("abs_media_store: lock author counts refresh: %w", err)
	}
	if !acquired {
		return nil // another node is refreshing
	}
	defer func() { _ = lock.Release(ctx) }()
	conn := lock.Conn()
	// Record when this refresh started, not when it finished: a refresh that
	// takes minutes must not make the next tick look early and skip.
	var fresh bool
	var startedAt time.Time
	if err := conn.QueryRow(ctx, `
		SELECT now(), EXISTS (SELECT 1 FROM abs_author_counts_refresh WHERE refreshed_at > now() - $1::interval)`,
		minInterval).Scan(&startedAt, &fresh); err != nil {
		return fmt.Errorf("abs_media_store: read author counts refresh: %w", err)
	}
	if fresh {
		return nil
	}
	if _, err := conn.Exec(ctx, `REFRESH MATERIALIZED VIEW CONCURRENTLY abs_audiobook_author_counts`); err != nil {
		return fmt.Errorf("abs_media_store: refresh author counts: %w", err)
	}
	if _, err := conn.Exec(ctx, `
		INSERT INTO abs_author_counts_refresh (singleton, refreshed_at) VALUES (true, $1)
		ON CONFLICT (singleton) DO UPDATE SET refreshed_at = EXCLUDED.refreshed_at`, startedAt); err != nil {
		return fmt.Errorf("abs_media_store: record author counts refresh: %w", err)
	}
	return nil
}

// libraryOverlapsDisabled reports whether a book in the library is also in
// one of the profile's disabled libraries. The author view counts such a book
// but the profile must not see it. It is an access answer, so it is read on
// every request rather than cached. The disabled libraries' memberships drive
// it (idx_item_libraries_folder_seen_content), each probing this library
// through the primary key, and it stops at the first overlap.
func (s *ABSMediaStore) libraryOverlapsDisabled(ctx context.Context, libraryID int64, disabled []int) (bool, error) {
	if len(disabled) == 0 {
		return false, nil
	}
	var overlap bool
	err := s.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM media_item_libraries hidden
			WHERE hidden.media_folder_id = ANY($2)
			  AND EXISTS (
				SELECT 1 FROM media_item_libraries here
				WHERE here.content_id = hidden.content_id AND here.media_folder_id = $1
			  )
		)`, int(libraryID), disabled).Scan(&overlap)
	if err != nil {
		return false, fmt.Errorf("abs_media_store: check disabled library overlap: %w", err)
	}
	return overlap, nil
}

// ListLibraryAuthors returns one page of distinct audiobook authors plus the
// total author count for the library. It reads the precomputed
// abs_audiobook_author_counts materialized view keyed by media_folder_id.
//
// The MV is keyed by library_id only and carries no per-item access predicate,
// so it is safe only when the caller has no item-level restriction. When the
// access filter carries an item-level predicate (a content-rating cap or
// excluded media types), reading the MV would leak authors of books the caller
// can't see, so we take the access-aware live path instead. The same holds
// when a book in this library is also in a library the profile disabled.
// Library-level access is otherwise enforced by scoping to a single resolved
// library.
//
// The advisory-age limit is deliberately not such a predicate here. Advisory
// ages exist for movies and series only (the host drops one reported for any
// other type), so no audiobook carries one and the limit cannot hide a book;
// taking the live path for it would only change a beta surface for nothing.
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
	if access.MaxContentRating != "" || len(access.ExcludedMediaTypes) > 0 {
		return s.listLibraryAuthorsLive(ctx, libraryID, limit, offset, sortBy, sortDesc, access)
	}
	if overlap, err := s.libraryOverlapsDisabled(ctx, libraryID, access.DisabledLibraryIDs); err != nil {
		return nil, 0, err
	} else if overlap {
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

	dir := sqlDirection(sortDesc)
	var orderBy string
	switch sortBy {
	case absSortAddedAt:
		orderBy = "c.added_at " + dir + ", c.person_id"
	case "numBooks":
		orderBy = "c.num_books " + dir + ", LOWER(c.name), c.person_id"
	default: // name; person_id keeps duplicate names from skipping or repeating across pages
		orderBy = "LOWER(c.name) " + dir + ", c.person_id " + dir
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
	conditions := []string{`mi.type = 'audiobook'`}
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

	dir := sqlDirection(sortDesc)
	var orderBy string
	switch sortBy {
	case absSortAddedAt:
		orderBy = "p.created_at " + dir + ", p.id"
	case "numBooks":
		orderBy = "num_books " + dir + ", LOWER(p.name), p.id"
	default: // name
		orderBy = "LOWER(p.name) " + dir + ", p.id " + dir
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
	conditions := []string{`mi.type = 'audiobook'`}
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

	// Count distinct multi-book series (HAVING COUNT > 1) so pagination totals
	// match what the data query returns. The count only shifts when the
	// library changes, so it shares ListAudiobooks' short-lived cache.
	total, err := s.cachedAudiobookCount(ctx, `SELECT COUNT(*) FROM (
		SELECT LOWER(s.series_name)
		FROM audiobook_series s JOIN media_items mi ON mi.content_id = s.content_id
		WHERE `+where+`
		GROUP BY LOWER(s.series_name) HAVING COUNT(*) > 1
	) t`, args)
	if err != nil {
		return nil, 0, fmt.Errorf("abs_media_store: count series: %w", err)
	}
	if total == 0 {
		return []abs.SeriesSummary{}, 0, nil
	}

	// Two stages: pick the page of series first, then read up to four
	// preview books (lowest series_index first) for those series only, so a
	// page never ranks and aggregates every book in the library. book_ids is
	// text because content_id is text in this schema; parallel titles and
	// updated_ats keep iteration straightforward in Go.
	pageSQL := `
		SELECT LOWER(s.series_name) AS series_key, MIN(s.series_name) AS series_name, COUNT(*)::int AS num_books
		FROM audiobook_series s JOIN media_items mi ON mi.content_id = s.content_id
		WHERE ` + where + `
		GROUP BY LOWER(s.series_name) HAVING COUNT(*) > 1
		ORDER BY series_key`
	dataArgs := append([]any(nil), args...)
	if limit > 0 {
		pageSQL += fmt.Sprintf(` LIMIT $%d OFFSET $%d`, argIdx, argIdx+1)
		dataArgs = append(dataArgs, limit, offset)
	}
	dataSQL := `
		WITH page AS (` + pageSQL + `)
		SELECT page.series_key, page.series_name, page.num_books,
			COALESCE(array_agg(b.content_id ORDER BY b.rn) FILTER (WHERE b.content_id IS NOT NULL), '{}'),
			COALESCE(array_agg(b.title ORDER BY b.rn) FILTER (WHERE b.content_id IS NOT NULL), '{}'),
			COALESCE(array_agg(b.updated_at ORDER BY b.rn) FILTER (WHERE b.content_id IS NOT NULL), '{}')
		FROM page
		LEFT JOIN LATERAL (
			SELECT s.content_id, mi.title, mi.updated_at,
				ROW_NUMBER() OVER (ORDER BY COALESCE(s.series_index, 999999), s.content_id) AS rn
			FROM audiobook_series s JOIN media_items mi ON mi.content_id = s.content_id
			WHERE LOWER(s.series_name) = page.series_key AND ` + where + `
			ORDER BY COALESCE(s.series_index, 999999), s.content_id
			LIMIT 4
		) b ON TRUE
		GROUP BY page.series_key, page.series_name, page.num_books
		ORDER BY page.series_key`
	rows, err := s.Pool.Query(ctx, dataSQL, dataArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("abs_media_store: list series: %w", err)
	}
	defer rows.Close()
	out := make([]abs.SeriesSummary, 0, 64)
	for rows.Next() {
		var (
			key, name  string
			books      int
			ids        []string
			titles     []string
			updatedAts []time.Time
		)
		if err := rows.Scan(&key, &name, &books, &ids, &titles, &updatedAts); err != nil {
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
		out = append(out, abs.SeriesSummary{ID: key, Name: name, NumBooks: books, Books: previews})
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
	conditions := []string{`ip.person_id = $1`, `ip.kind = 7`, `mi.type = 'audiobook'`}
	args := []any{id}
	argIdx := 2
	appendAudiobookAccessConditions("mi", access, &conditions, &args, &argIdx)
	items, err := s.listAudiobookIDs(ctx, `
		SELECT mi.content_id
		FROM item_people ip
		JOIN media_items mi ON mi.content_id = ip.content_id
		WHERE `+strings.Join(conditions, " AND ")+`
		ORDER BY LOWER(mi.title)`, args, access, 0)
	if err != nil {
		return abs.Author{}, fmt.Errorf("abs_media_store: get author books: %w", err)
	}
	author.Books = items
	return author, nil
}

// GetSeriesByName looks up a series by ID or name, case-insensitively, plus
// the books in it the viewer may see. A series with no such book is not
// found, so its name does not leak. The display name is the one the series
// list shows: the lowest spelling among the visible books.
func (s *ABSMediaStore) GetSeriesByName(ctx context.Context, seriesName string, access catalog.AccessFilter) (abs.Series, error) {
	if s.Pool == nil {
		return abs.Series{}, abs.ErrNotFound
	}
	conditions := []string{`LOWER(asx.series_name) = LOWER($1)`, `mi.type = 'audiobook'`}
	args := []any{seriesName}
	argIdx := 2
	appendAudiobookAccessConditions("mi", access, &conditions, &args, &argIdx)
	where := strings.Join(conditions, " AND ")
	var canonicalName, key *string
	if err := s.Pool.QueryRow(ctx, `
		SELECT MIN(asx.series_name), LOWER(MIN(asx.series_name))
		FROM audiobook_series asx
		JOIN media_items mi ON mi.content_id = asx.content_id
		WHERE `+where, args...).Scan(&canonicalName, &key); err != nil {
		return abs.Series{}, fmt.Errorf("abs_media_store: get series: %w", err)
	}
	if canonicalName == nil {
		return abs.Series{}, abs.ErrNotFound
	}
	items, err := s.listAudiobookIDs(ctx, `
		SELECT mi.content_id
		FROM audiobook_series asx
		JOIN media_items mi ON mi.content_id = asx.content_id
		WHERE `+where+`
		ORDER BY asx.series_index NULLS LAST, LOWER(mi.title)`, args, access, 0)
	if err != nil {
		return abs.Series{}, fmt.Errorf("abs_media_store: get series books: %w", err)
	}
	return abs.Series{ID: *key, Name: *canonicalName, Books: items}, nil
}

// likeContains and likePrefix build ILIKE patterns that match term literally.
func likeContains(term string) string { return "%" + escapeLike(term) + "%" }
func likePrefix(term string) string   { return escapeLike(term) + "%" }

func escapeLike(term string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(term)
}

// audiobookScope renders the library and access predicates on media_items mi
// shared by the ABS aggregate lists, starting at $argIdx.
func audiobookScope(libraryID int64, access catalog.AccessFilter, args *[]any, argIdx *int) string {
	conditions := []string{`mi.type = 'audiobook'`}
	if libraryID != 0 {
		conditions = append(conditions, fmt.Sprintf(`EXISTS (
			SELECT 1 FROM media_item_libraries mil
			WHERE mil.content_id = mi.content_id AND mil.media_folder_id = $%d
		)`, *argIdx))
		*args = append(*args, int(libraryID))
		*argIdx = *argIdx + 1
	}
	appendAudiobookAccessConditions("mi", access, &conditions, args, argIdx)
	return strings.Join(conditions, " AND ")
}

// SearchLibraryAuthors returns up to limit authors of books the viewer may
// see in the library whose name contains term, name-prefix matches first.
// The name match runs on idx_people_name_trgm, so every author is reachable
// however large the library.
func (s *ABSMediaStore) SearchLibraryAuthors(ctx context.Context, libraryID int64, term string, limit int, access catalog.AccessFilter) ([]abs.AuthorSummary, error) {
	if s.Pool == nil || strings.TrimSpace(term) == "" {
		return []abs.AuthorSummary{}, nil
	}
	args := []any{likeContains(term), likePrefix(term)}
	argIdx := 3
	scope := audiobookScope(libraryID, access, &args, &argIdx)
	args = append(args, limit)
	rows, err := s.Pool.Query(ctx, `
		SELECT p.id, p.name, COUNT(DISTINCT mi.content_id)::int, COALESCE(p.photo_path, '') <> ''
		FROM people p
		JOIN item_people ip ON ip.person_id = p.id AND ip.kind = `+strconv.Itoa(int(models.PersonKindAuthor))+`
		JOIN media_items mi ON mi.content_id = ip.content_id
		WHERE p.name ILIKE $1 AND `+scope+`
		GROUP BY p.id, p.name
		ORDER BY (p.name ILIKE $2) DESC, LOWER(p.name), p.id
		LIMIT $`+strconv.Itoa(argIdx), args...)
	if err != nil {
		return nil, fmt.Errorf("abs_media_store: search authors: %w", err)
	}
	defer rows.Close()
	out := []abs.AuthorSummary{}
	for rows.Next() {
		var (
			id       int64
			name     string
			books    int
			hasPhoto bool
		)
		if err := rows.Scan(&id, &name, &books, &hasPhoto); err != nil {
			return nil, fmt.Errorf("abs_media_store: scan author search: %w", err)
		}
		out = append(out, abs.AuthorSummary{ID: strconv.FormatInt(id, 10), Name: name, NumBooks: books, HasPhoto: hasPhoto})
	}
	return out, rows.Err()
}

// SearchLibrarySeries returns up to limit series with a book the viewer may
// see in the library whose name contains term, name-prefix matches first,
// grouped by abs.SeriesID like the series list. Each carries up to four
// preview books. The name match runs on idx_audiobook_series_name_trgm.
func (s *ABSMediaStore) SearchLibrarySeries(ctx context.Context, libraryID int64, term string, limit int, access catalog.AccessFilter) ([]abs.SeriesSummary, error) {
	if s.Pool == nil || strings.TrimSpace(term) == "" {
		return []abs.SeriesSummary{}, nil
	}
	args := []any{likeContains(term), likePrefix(term)}
	argIdx := 3
	scope := audiobookScope(libraryID, access, &args, &argIdx)
	args = append(args, limit)
	rows, err := s.Pool.Query(ctx, `
		SELECT LOWER(s.series_name), MIN(s.series_name), COUNT(*)::int,
			(array_agg(s.content_id ORDER BY COALESCE(s.series_index, 999999), s.content_id))[1:4],
			(array_agg(mi.title ORDER BY COALESCE(s.series_index, 999999), s.content_id))[1:4],
			(array_agg(mi.updated_at ORDER BY COALESCE(s.series_index, 999999), s.content_id))[1:4]
		FROM audiobook_series s
		JOIN media_items mi ON mi.content_id = s.content_id
		WHERE s.series_name ILIKE $1 AND `+scope+`
		GROUP BY LOWER(s.series_name)
		HAVING COUNT(*) > 1
		ORDER BY (MIN(s.series_name) ILIKE $2) DESC, LOWER(s.series_name)
		LIMIT $`+strconv.Itoa(argIdx), args...)
	if err != nil {
		return nil, fmt.Errorf("abs_media_store: search series: %w", err)
	}
	defer rows.Close()
	out := []abs.SeriesSummary{}
	for rows.Next() {
		var (
			key, name  string
			books      int
			ids        []string
			titles     []string
			updatedAts []time.Time
		)
		if err := rows.Scan(&key, &name, &books, &ids, &titles, &updatedAts); err != nil {
			return nil, fmt.Errorf("abs_media_store: scan series search: %w", err)
		}
		previews := make([]abs.SeriesBookPreview, len(ids))
		for i := range ids {
			previews[i] = abs.SeriesBookPreview{ContentID: ids[i], Title: titles[i], UpdatedAt: updatedAts[i]}
		}
		out = append(out, abs.SeriesSummary{ID: key, Name: name, NumBooks: books, Books: previews})
	}
	return out, rows.Err()
}

// AudiobookPosterPath reads one audiobook's poster column. Like the full
// lookup it replaces on the cover route, it applies no access filter.
func (s *ABSMediaStore) AudiobookPosterPath(ctx context.Context, contentID string) (string, error) {
	if s.Pool == nil {
		return "", abs.ErrNotFound
	}
	var poster string
	err := s.Pool.QueryRow(ctx, `
		SELECT COALESCE(poster_path, '') FROM media_items
		WHERE content_id = $1 AND type = 'audiobook'`, contentID).Scan(&poster)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", abs.ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("abs_media_store: audiobook poster: %w", err)
	}
	return poster, nil
}

// AuthorPhotoPath reads the photo path of a person credited as the author of
// an audiobook. Anyone else, such as a film's cast, is not found: the author
// image route needs no sign-in.
func (s *ABSMediaStore) AuthorPhotoPath(ctx context.Context, authorID string) (string, error) {
	id, err := strconv.ParseInt(authorID, 10, 64)
	if err != nil || s.Pool == nil {
		return "", abs.ErrNotFound
	}
	var photo string
	err = s.Pool.QueryRow(ctx, `
		SELECT COALESCE(p.photo_path, '') FROM people p
		WHERE p.id = $1 AND EXISTS (
			SELECT 1 FROM item_people ip JOIN media_items mi ON mi.content_id = ip.content_id
			WHERE ip.person_id = p.id AND ip.kind = `+strconv.Itoa(int(models.PersonKindAuthor))+` AND mi.type = 'audiobook'
		)`, id).Scan(&photo)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", abs.ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("abs_media_store: author photo: %w", err)
	}
	return photo, nil
}
