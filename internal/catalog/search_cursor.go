package catalog

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	searchCursorFTS            = "fts"
	searchCursorCombined       = "combined"
	searchCursorFuzzy          = "fuzzy"
	searchSortRelevance        = "relevance"
	searchLowerTitleExpression = "LOWER(title)"
	searchTypeAudiobook        = "audiobook"
	searchTypeEbook            = "ebook"
)

// SearchCursor holds a complete relevance tuple, not an offset. The API binds
// it to query, item types and effective viewer access in its opaque envelope.
// Combined mode is the existing bounded FTS-plus-fuzzy family, not a snapshot.
type SearchCursor struct {
	Consumed int                `json:"consumed,omitempty"`
	Ordered  *QueryCursor       `json:"ordered,omitempty"`
	Mode     string             `json:"mode"`
	Phase    string             `json:"phase,omitempty"`
	Keys     []QueryCursorValue `json:"keys,omitempty"`
}
type SearchCursorPage struct {
	Items      []*models.MediaItem
	Total      int
	TotalExact bool
	HasMore    bool
	Next       *SearchCursor
	Scope      *SearchCursor
}
type SearchCursorOptions struct {
	Definition  QueryDefinition
	GroupByWork bool
}

type searchCursorSQL struct {
	relation     string
	relationArgs []any
	request      SearchCursorOptions
	after        *SearchCursor
	jump         bool
	countArgs    []any
	err          error
}

func searchFTSTerms() []queryCursorTerm {
	terms := []queryCursorTerm{}
	for _, expression := range []string{"exact_title_match", "contiguous_title_match", "year_match", "phrase_rank", "title_prefix_rank", "overview_rank"} {
		terms = append(terms, queryCursorTerm{expression: expression, kind: cursorKindNumber, descending: true, nullsLast: false})
	}
	return append(terms, queryCursorTerm{expression: searchLowerTitleExpression, kind: cursorKindText, nullsLast: true}, queryCursorTerm{expression: cursorContentIDColumn, kind: cursorKindText, nullsLast: true})
}

func (r *ItemRepository) searchCursorPage(ctx context.Context, query string, itemTypes []string, limit int, after *SearchCursor, filter AccessFilter, includeTotal bool, request ...SearchCursorOptions) (SearchCursorPage, error) {
	if limit < 1 || limit > 200 {
		return SearchCursorPage{}, fmt.Errorf("search page limit must be between 1 and 200")
	}
	if after != nil && after.Mode != searchCursorFTS && after.Mode != searchCursorCombined {
		return SearchCursorPage{}, fmt.Errorf("invalid search cursor mode")
	}
	ctx, cancel := context.WithTimeout(ctx, postgresSearchTimeout)
	defer cancel()
	parsed := parseSearchQuery(query)
	if sort := searchOptions(request).Definition.Sort.Field; sort != "" && sort != searchSortRelevance {
		return r.searchOrderedCursorPage(ctx, parsed, itemTypes, limit, after, filter, includeTotal, searchOptions(request))
	}
	if after != nil && after.Mode == searchCursorCombined {
		return r.searchCombinedCursorPage(ctx, parsed, itemTypes, limit, after, filter, includeTotal, request...)
	}
	probeLimit := limit + 1
	if after == nil && eligibleForFuzzy(parsed) {
		probeLimit = max(probeLimit, fuzzyFallbackThreshold)
	}
	if after == nil && searchOptions(request).GroupByWork && eligibleForFuzzy(parsed) {
		probeOptions := searchOptions(request)
		probeOptions.GroupByWork = false
		probe, _, _, err := r.searchFTSCursorRows(ctx, parsed, itemTypes, fuzzyFallbackThreshold, nil, filter, false, false, 0, probeOptions)
		if err != nil {
			return SearchCursorPage{}, err
		}
		if len(probe) < fuzzyFallbackThreshold {
			return r.searchCombinedCursorPage(ctx, parsed, itemTypes, limit, nil, filter, includeTotal, request...)
		}
		after = &SearchCursor{Mode: searchCursorFTS}
	}
	items, keys, total, err := r.searchFTSCursorRows(ctx, parsed, itemTypes, probeLimit, after, filter, includeTotal, false, 0, request...)
	if err != nil {
		return SearchCursorPage{}, err
	}
	if after == nil && eligibleForFuzzy(parsed) && len(items) < fuzzyFallbackThreshold {
		return r.searchCombinedCursorPage(ctx, parsed, itemTypes, limit, nil, filter, includeTotal, request...)
	}
	page := SearchCursorPage{Items: items, Total: total, TotalExact: includeTotal, HasMore: len(items) > limit, Scope: &SearchCursor{Mode: searchCursorFTS}}
	if page.HasMore {
		page.Items = items[:limit]
		page.Next = &SearchCursor{Mode: searchCursorFTS, Phase: searchCursorFTS, Keys: keys[limit-1].Keys}
	}
	return page, nil
}

func (r *ItemRepository) searchFTSCursorRows(ctx context.Context, parsed parsedSearchQuery, itemTypes []string, limit int, after *SearchCursor, filter AccessFilter, includeTotal, jump bool, index int, request ...SearchCursorOptions) ([]*models.MediaItem, []QueryCursor, int, error) {
	options := &searchCursorSQL{after: after, jump: jump, request: searchOptions(request)}
	sql, countSQL, args := r.buildMixedSearchCursorSQL(parsed, itemTypes, limit, index, filter, false, options)
	if options.err != nil {
		return nil, nil, 0, options.err
	}
	if sql == "" {
		return []*models.MediaItem{}, nil, 0, nil
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, nil, 0, err
	}
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), postgresSearchRollbackTimeout)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, nil, 0, err
	}
	wrapped := &cursorRows{Rows: rows, terms: searchFTSTerms()}
	items, err := scanItems(wrapped)
	rows.Close()
	if err != nil {
		return nil, nil, 0, err
	}
	total := 0
	if includeTotal {
		if err := tx.QueryRow(ctx, countSQL, options.countArgs...).Scan(&total); err != nil {
			return nil, nil, 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, 0, err
	}
	return items, wrapped.keys, total, nil
}

// SeekSearchCursor uses OFFSET only to find one explicit jump boundary. Its
// work can grow with index; the search deadline bounds execution time.
func (r *ItemRepository) SeekSearchCursor(ctx context.Context, query string, itemTypes []string, index int, scope *SearchCursor, filter AccessFilter, request ...SearchCursorOptions) (result *SearchCursor, resultErr error) {
	defer func() {
		if result != nil {
			result.Consumed = index
		}
	}()
	if cap := searchOptions(request).Definition.Limit; cap != nil && index > *cap {
		return nil, pgx.ErrNoRows
	}
	if index < 0 || index > 10_000_000 {
		return nil, fmt.Errorf("search jump must be between 0 and 10000000")
	}
	ctx, cancel := context.WithTimeout(ctx, postgresSearchTimeout)
	defer cancel()
	if scope == nil {
		page, err := r.SearchCursorPage(ctx, query, itemTypes, 1, nil, filter, false, request...)
		if err != nil {
			return nil, err
		}
		scope = page.Scope
	}
	if scope == nil {
		return nil, nil
	}
	if index == 0 {
		return &SearchCursor{Mode: scope.Mode}, nil
	}
	if sort := searchOptions(request).Definition.Sort.Field; sort != "" && sort != searchSortRelevance {
		executor, def, access, mode, _, err := r.orderedSearchExecutor(ctx, parseSearchQuery(query), itemTypes, scope, filter, searchOptions(request))
		if err != nil {
			return nil, err
		}
		boundary, err := executor.SeekCursor(ctx, def, access, index)
		if err != nil {
			return nil, err
		}
		return &SearchCursor{Mode: mode, Ordered: boundary}, nil
	}
	if scope.Mode == searchCursorCombined {

		return r.combinedBoundaryAt(ctx, parseSearchQuery(query), itemTypes, index, filter, request...)
	}
	if scope.Mode != searchCursorFTS {
		return nil, fmt.Errorf("invalid search cursor mode")
	}
	_, keys, _, err := r.searchFTSCursorRows(ctx, parseSearchQuery(query), itemTypes, 1, nil, filter, false, true, index-1, request...)
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, pgx.ErrNoRows
	}
	return &SearchCursor{Mode: searchCursorFTS, Phase: searchCursorFTS, Keys: keys[0].Keys}, nil
}

type searchCandidate struct {
	item   *models.MediaItem
	cursor *SearchCursor
}

func fuzzySQLTerms() []queryCursorTerm {
	return []queryCursorTerm{{expression: "fuzzy_rank", kind: cursorKindNumber, descending: true}, {expression: "fuzzy_full_rank", kind: cursorKindNumber, descending: true}, {expression: searchLowerTitleExpression, kind: cursorKindText, nullsLast: true}, {expression: cursorContentIDColumn, kind: cursorKindText, nullsLast: true}}
}
func fuzzyCompleteTerms() []queryCursorTerm {
	terms := []queryCursorTerm{}
	for i, desc := range []bool{true, true, true, false, false} {
		terms = append(terms, queryCursorTerm{expression: fmt.Sprintf("score%d", i), kind: cursorKindNumber, descending: desc})
	}
	return append(terms, fuzzySQLTerms()...)
}
func numberCursorValue(value int) QueryCursorValue {
	return QueryCursorValue{Kind: cursorKindNumber, Value: new(strconv.Itoa(value))}
}
func boolCursorValue(value bool) QueryCursorValue {
	if value {
		return numberCursorValue(1)
	}
	return numberCursorValue(0)
}

func (r *ItemRepository) combinedSearchCandidates(ctx context.Context, parsed parsedSearchQuery, itemTypes []string, filter AccessFilter, request ...SearchCursorOptions) ([]searchCandidate, bool, error) {
	rawOptions := searchOptions(request)
	rawOptions.GroupByWork = false
	fts, ftsKeys, _, err := r.searchFTSCursorRows(ctx, parsed, itemTypes, fuzzyFallbackThreshold, nil, filter, false, false, 0, rawOptions)
	if err != nil {
		return nil, false, err
	}
	if len(fts) >= fuzzyFallbackThreshold {
		return nil, false, fmt.Errorf("%w: sparse search family changed", ErrCatalogCursorChanged)
	}
	candidates := make([]searchCandidate, 0, fuzzyFallbackThreshold+fuzzyMaxResults)
	for i, item := range fts {
		candidates = append(candidates, searchCandidate{item: item, cursor: &SearchCursor{Mode: searchCursorCombined, Phase: searchCursorFTS, Keys: ftsKeys[i].Keys}})
	}
	if searchBlockHasExactTitle(fts, parsed.ExactTitleHint) {
		candidates, err = r.groupSearchCandidates(ctx, candidates, searchOptions(request).GroupByWork, searchOptions(request).Definition.Limit)
		return candidates, false, err
	}
	floor := 0.0
	if len(fts) > 0 {
		floor = fuzzyAugmentSimilarityFloor
	}
	options := &searchCursorSQL{request: searchOptions(request)}
	sql, _, args := r.buildFuzzySearchCursorSQL(parsed, itemTypes, fuzzyMaxResults+1, 0, filter, false, contentIDsFromMediaItems(fts), floor, options)
	if options.err != nil {
		return nil, false, options.err
	}
	if sql == "" {
		return candidates, false, nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), postgresSearchRollbackTimeout)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()
	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL pg_trgm.strict_word_similarity_threshold = %g", trgmWordSimilarityThreshold)); err != nil {
		return nil, false, err
	}
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, false, err
	}
	wrapped := &cursorRows{Rows: rows, terms: fuzzySQLTerms()}
	fuzzy, err := scanItems(wrapped)
	rows.Close()
	if err != nil {
		return nil, false, err
	}
	truncated := len(fuzzy) > fuzzyMaxResults
	if truncated {
		fuzzy = fuzzy[:fuzzyMaxResults]
	}
	aliases, err := loadFuzzyRerankAliases(ctx, tx, searchTextFromParsed(parsed), fuzzy)
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	query := normalizeTitleForComparison(searchTextFromParsed(parsed))
	tokens, validTokens := boundedFuzzyTokens(query, fuzzyRerankMaxQueryTokens)
	ranks := make([]fuzzyRankedItem, len(fuzzy))
	keysByID := map[string][]QueryCursorValue{}
	for i, item := range fuzzy {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		score := fuzzyTitleScore{}
		if validTokens && len(tokens) > 0 {
			score = fuzzyTitleScore{editDistance: int(^uint(0) >> 1), extraTokens: int(^uint(0) >> 1)}
			variants := append([]string(nil), aliases[item.ContentID]...)
			if len(item.Title) <= fuzzyRerankMaxTitleBytes {
				variants = append(variants, item.Title)
			}
			for _, variant := range variants {
				candidate := scoreFuzzyTitleVariant(query, tokens, variant)
				if fuzzyTitleScoreBetter(candidate, score) {
					score = candidate
				}
			}
		}
		ranks[i] = fuzzyRankedItem{item: item, score: score, order: i}
		keys := []QueryCursorValue{boolCursorValue(score.exact), numberCursorValue(score.matchedTokens), boolCursorValue(score.contiguous), numberCursorValue(score.editDistance), numberCursorValue(score.extraTokens)}
		keysByID[item.ContentID] = append(keys, wrapped.keys[i].Keys...)
	}
	slices.SortStableFunc(ranks, func(a, b fuzzyRankedItem) int {
		if fuzzyTitleScoreBetter(a.score, b.score) {
			return -1
		}
		if fuzzyTitleScoreBetter(b.score, a.score) {
			return 1
		}
		return 0
	})
	for _, rank := range ranks {
		candidates = append(candidates, searchCandidate{item: rank.item, cursor: &SearchCursor{Mode: searchCursorCombined, Phase: searchCursorFuzzy, Keys: keysByID[rank.item.ContentID]}})
	}
	candidates, err = r.groupSearchCandidates(ctx, candidates, searchOptions(request).GroupByWork, searchOptions(request).Definition.Limit)
	return candidates, truncated, err
}

func (r *ItemRepository) searchCombinedCursorPage(ctx context.Context, parsed parsedSearchQuery, itemTypes []string, limit int, after *SearchCursor, filter AccessFilter, includeTotal bool, request ...SearchCursorOptions) (SearchCursorPage, error) {
	candidates, truncated, err := r.combinedSearchCandidates(ctx, parsed, itemTypes, filter, request...)
	if err != nil {
		return SearchCursorPage{}, err
	}
	total := len(candidates)
	candidates, err = r.seekCombinedCandidates(ctx, candidates, after)
	if err != nil {
		return SearchCursorPage{}, err
	}
	page := SearchCursorPage{Items: []*models.MediaItem{}, HasMore: len(candidates) > limit, TotalExact: includeTotal && !truncated, Scope: &SearchCursor{Mode: searchCursorCombined}}
	if includeTotal {
		page.Total = total
	}
	for _, candidate := range candidates[:min(limit, len(candidates))] {
		page.Items = append(page.Items, candidate.item)
	}
	if page.HasMore {
		page.Next = candidates[limit-1].cursor
	}
	return page, nil
}

// This VALUES relation is capped at 54 candidates. PostgreSQL performs text
// comparisons so database collation stays identical to the SQL ranking ties;
// Go byte ordering would be incorrect for a non-C database collation.
func (r *ItemRepository) seekCombinedCandidates(ctx context.Context, candidates []searchCandidate, after *SearchCursor) ([]searchCandidate, error) {
	if after == nil || len(after.Keys) == 0 {
		return candidates, nil
	}
	terms := searchFTSTerms()
	if after.Phase == searchCursorFuzzy {
		terms = fuzzyCompleteTerms()
	} else if after.Phase != searchCursorFTS {
		return nil, fmt.Errorf("invalid combined search phase")
	}
	columns := []string{"ordinal"}
	for i := range terms {
		terms[i].expression = fmt.Sprintf("k%d", i)
		columns = append(columns, terms[i].expression)
	}
	args := []any{}
	values := []string{}
	accepted := map[int]bool{}
	for index, candidate := range candidates {
		if candidate.cursor.Phase != after.Phase {
			if after.Phase == searchCursorFTS {
				accepted[index] = true
			}
			continue
		}
		row := []string{strconv.Itoa(index)}
		for i, key := range candidate.cursor.Keys {
			args = append(args, key.Value)
			row = append(row, fmt.Sprintf("$%d::%s", len(args), terms[i].cast()))
		}
		values = append(values, "("+strings.Join(row, ",")+")")
	}
	seek, seekArgs, err := cursorSeekSQL(terms, &QueryCursor{Keys: after.Keys}, len(args)+1)
	if err != nil {
		return nil, err
	}
	if len(values) > 0 {
		args = append(args, seekArgs...)
		sql := "SELECT ordinal FROM (VALUES " + strings.Join(values, ",") + ") AS candidates(" + strings.Join(columns, ",") + ") WHERE " + seek
		rows, err := r.pool.Query(ctx, sql, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var index int
			if err := rows.Scan(&index); err != nil {
				rows.Close()
				return nil, err
			}
			accepted[index] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	result := []searchCandidate{}
	for index, candidate := range candidates {
		if accepted[index] {
			result = append(result, candidate)
		}
	}
	return result, nil
}
func (r *ItemRepository) combinedBoundaryAt(ctx context.Context, parsed parsedSearchQuery, itemTypes []string, index int, filter AccessFilter, request ...SearchCursorOptions) (*SearchCursor, error) {
	candidates, _, err := r.combinedSearchCandidates(ctx, parsed, itemTypes, filter, request...)
	if err != nil {
		return nil, err
	}
	if index < 1 || index > len(candidates) {
		return nil, pgx.ErrNoRows
	}
	return candidates[index-1].cursor, nil
}

func searchOptions(options []SearchCursorOptions) SearchCursorOptions {
	if len(options) > 0 {
		return options[0]
	}
	return SearchCursorOptions{}
}

// searchDefinitionNeedsPredicate reports whether a source-specific search must
// constrain candidates through the collection definition. Library, media
// scope and access are already applied by each search branch, so a definition
// without rules adds nothing but a second scan of the whole library. That scan
// is what made the cursor path time out on large series libraries: the
// predicate's preview plan materialized and sorted every episode in the
// library before the nine title hits were checked against it.
func searchDefinitionNeedsPredicate(def QueryDefinition) bool {
	return len(def.Groups) > 0
}

// Constrain candidates before title/overview fallback chooses a family.
func (r *ItemRepository) appendSearchCursorDefinition(cursor *searchCursorSQL, episode bool, filter AccessFilter, conditions *[]string, args *[]any, index *int) {
	if cursor == nil {
		return
	}
	def := cursor.request.Definition
	scope := def.MediaScope
	if !searchDefinitionNeedsPredicate(def) {
		// A scope mismatch between the branch and the definition still must
		// empty the branch, exactly as the predicate path does below.
		if episode && scope != "" && !isEpisodeCatalogScope(scope) || !episode && isEpisodeCatalogScope(scope) {
			*conditions = append(*conditions, "FALSE")
		}
		return
	}
	if episode {
		if scope != "" && !isEpisodeCatalogScope(scope) {
			*conditions = append(*conditions, "FALSE")
			return
		}
		def.MediaScope = recentTVTypeEpisode
	} else if isEpisodeCatalogScope(scope) {
		*conditions = append(*conditions, "FALSE")
		return
	}
	def.Sort = QuerySort{}
	def.Limit = nil
	executor := &QueryExecutor{Pool: r.pool, Scope: def.MediaScope}
	predicate, values, err := collectionDefinitionPredicate(executor, def, filter)
	if err != nil {
		cursor.err = err
		return
	}
	if episode {
		predicate = strings.Replace(predicate, "mi.content_id IN (", "ece.episode_id IN (", 1)
	}
	*conditions = append(*conditions, rebindSQLPlaceholders(predicate, *index-1))
	*args = append(*args, values...)
	*index += len(values)
}

func (r *ItemRepository) groupSearchCandidates(ctx context.Context, candidates []searchCandidate, grouped bool, cap *int) ([]searchCandidate, error) {
	if grouped && cap != nil {
		candidates = candidates[:min(len(candidates), max(0, *cap))]
	}
	if !grouped || len(candidates) == 0 {
		return candidates, nil
	}
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.item.Type == searchTypeEbook || candidate.item.Type == searchTypeAudiobook {
			ids = append(ids, candidate.item.ContentID)
		}
	}
	rows, err := r.pool.Query(ctx, "SELECT content_id,work_id FROM literary_work_items WHERE content_id=ANY($1)", ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	works := map[string]string{}
	for rows.Next() {
		var id, work string
		if err := rows.Scan(&id, &work); err != nil {
			return nil, err
		}
		works[id] = work
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	result := make([]searchCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		key := "item:" + candidate.item.ContentID
		if work := works[candidate.item.ContentID]; work != "" {
			key = "work:" + work
		}
		if !seen[key] {
			seen[key] = true
			result = append(result, candidate)
		}
	}
	return result, nil
}

// searchCandidatesExecutor accepts a trusted candidate-ID relation. Each media
// branch applies the definition and access rules before the shared sort runs.
func (r *ItemRepository) searchCandidatesExecutor(def QueryDefinition, access AccessFilter, sourceSQL string, sourceArgs []any) (*QueryExecutor, QueryDefinition, AccessFilter, error) {
	if def.MediaScope != "" {
		executor := &QueryExecutor{Pool: r.pool, Scope: def.MediaScope, SourceWhere: "mi.content_id IN (" + sourceSQL + ")", SourceArgs: sourceArgs}
		return executor, def, access, nil
	}
	args := append([]any(nil), sourceArgs...)
	branches := []string{}
	for _, episode := range []bool{false, true} {
		options := &searchCursorSQL{request: SearchCursorOptions{Definition: def}}
		conditions := []string{"mi.content_id IN (" + sourceSQL + ")"}
		index := len(args) + 1
		r.appendSearchCursorDefinition(options, episode, access, &conditions, &args, &index)
		if options.err != nil {
			return nil, QueryDefinition{}, access, options.err
		}
		if episode && len(conditions) > 1 {
			conditions[len(conditions)-1] = strings.Replace(conditions[len(conditions)-1], "ece.episode_id IN (", "mi.content_id IN (", 1)
		}
		condition := strings.Join(conditions, " AND ")
		relation := catalogBaseRelationForScope("")
		if episode {
			relation = episodeCatalogBaseRelation
		}
		branches = append(branches, "SELECT "+qualifiedItemColumns("mi")+", mi.last_air_date_at, mi.content_rating_age FROM "+relation+" WHERE "+condition)
	}
	executor := &QueryExecutor{Pool: r.pool, BaseRelationSQL: "(" + strings.Join(branches, " UNION ALL ") + ") mi"}
	// The full access/definition predicate has already been applied in the
	// mixed relation; repeating a media_items library join would drop episodes.
	executor.SourceWhere = cursorTruePredicate
	executor.SourceArgs = args
	if def.Sort.Field == defaultSortField {
		libraryIDs := append([]int(nil), def.LibraryIDs...)
		if access.AllowedLibraryIDs != nil {
			if len(libraryIDs) == 0 {
				libraryIDs = append([]int(nil), access.AllowedLibraryIDs...)
			} else {
				libraryIDs = intersectInts(libraryIDs, access.AllowedLibraryIDs)
			}
		}
		if len(libraryIDs) > 0 {
			executor.SourceArgs = append(executor.SourceArgs, libraryIDs)
			parameter := fmt.Sprintf("$%d", len(executor.SourceArgs))
			expression := "CASE WHEN mi.type='episode' THEN (SELECT MIN(el.first_seen_at) FROM episode_libraries el WHERE el.episode_id=mi.content_id AND el.media_folder_id=ANY(" + parameter + ")) ELSE (SELECT MIN(mil.first_seen_at) FROM media_item_libraries mil WHERE mil.content_id=mi.content_id AND mil.media_folder_id=ANY(" + parameter + ")) END"
			executor.SourceOrder = []queryCursorTerm{{expression: expression, kind: cursorKindTimestamp, descending: def.Sort.Order != querySortAsc, nullsLast: true}, {expression: "LOWER(COALESCE(NULLIF(BTRIM(mi.sort_title),''),mi.title))", kind: cursorKindText, nullsLast: true}, {expression: cursorContentIDExpression, kind: cursorKindText, nullsLast: true}}
		}
	}
	// Source arguments normally bind SourceWhere, while our trusted relation
	// references them as well. No additional filter args may precede them.
	outer := QueryDefinition{Sort: def.Sort, Limit: def.Limit}
	outerAccess := access
	outerAccess.AllowedLibraryIDs = nil
	outerAccess.DisabledLibraryIDs = nil
	outerAccess.AllowedContentIDs = nil
	outerAccess.NamePrefix = ""
	outerAccess.MaturityLimits = AccessFilter{}.MaturityLimits
	outerAccess.ExcludedMediaTypes = nil
	return executor, outer, outerAccess, nil
}

func (r *ItemRepository) orderedSearchExecutor(ctx context.Context, parsed parsedSearchQuery, itemTypes []string, after *SearchCursor, access AccessFilter, request SearchCursorOptions) (*QueryExecutor, QueryDefinition, AccessFilter, string, bool, error) {
	relevance := request
	relevance.Definition.Sort = QuerySort{}
	relevance.GroupByWork = false
	mode := ""
	if after != nil {
		mode = after.Mode
	}

	if mode == "" {
		probe, _, _, err := r.searchFTSCursorRows(ctx, parsed, itemTypes, fuzzyFallbackThreshold, nil, access, false, false, 0, relevance)
		if err != nil {
			return nil, QueryDefinition{}, access, "", false, err
		}
		mode = searchCursorFTS
		if eligibleForFuzzy(parsed) && len(probe) < fuzzyFallbackThreshold {
			mode = searchCursorCombined
		}
	}

	truncated := false
	var source string
	var args []any
	if mode == searchCursorCombined {
		candidates, capped, err := r.combinedSearchCandidates(ctx, parsed, itemTypes, access, relevance)
		if err != nil {
			return nil, QueryDefinition{}, access, "", false, err
		}
		truncated = capped
		ids := make([]string, len(candidates))
		for i, candidate := range candidates {
			ids[i] = candidate.item.ContentID
		}
		source = "SELECT unnest($1::text[])"
		args = []any{ids}
	} else {
		options := &searchCursorSQL{request: relevance}
		_, _, _ = r.buildMixedSearchCursorSQL(parsed, itemTypes, 1, 0, access, false, options)
		if options.err != nil {
			return nil, QueryDefinition{}, access, "", false, options.err
		}
		source, args = options.relation, options.relationArgs
		if source == "" {
			source = "SELECT NULL::text WHERE FALSE"
		}
	}
	executor, def, filter, err := r.searchCandidatesExecutor(request.Definition, access, source, args)
	if err != nil {
		return nil, def, filter, "", false, err
	}
	executor.GroupByWork = request.GroupByWork
	return executor, def, filter, mode, truncated, nil
}

func (r *ItemRepository) searchOrderedCursorPage(ctx context.Context, parsed parsedSearchQuery, itemTypes []string, limit int, after *SearchCursor, filter AccessFilter, includeTotal bool, request SearchCursorOptions) (SearchCursorPage, error) {
	executor, def, access, mode, truncated, err := r.orderedSearchExecutor(ctx, parsed, itemTypes, after, filter, request)
	if err != nil {
		return SearchCursorPage{}, err
	}
	var boundary *QueryCursor
	if after != nil {
		boundary = after.Ordered
	}
	result, err := executor.PreviewCursorPage(ctx, def, access, limit, boundary, includeTotal)
	if err != nil {
		return SearchCursorPage{}, err
	}
	page := SearchCursorPage{Items: result.Items, Total: result.Total, TotalExact: result.TotalExact && !truncated, HasMore: result.HasMore, Scope: &SearchCursor{Mode: mode}}
	if result.Next != nil {
		page.Next = &SearchCursor{Mode: mode, Ordered: result.Next}
	}
	return page, nil
}

func (r *ItemRepository) SearchCursorPage(ctx context.Context, query string, itemTypes []string, limit int, after *SearchCursor, filter AccessFilter, includeTotal bool, request ...SearchCursorOptions) (SearchCursorPage, error) {
	if limit < 1 || limit > 200 {
		return SearchCursorPage{}, fmt.Errorf("search page limit must be between 1 and 200")
	}
	consumed := 0
	if after != nil {
		consumed = after.Consumed
	}
	if consumed < 0 {
		return SearchCursorPage{}, fmt.Errorf("invalid search cursor consumed count")
	}
	cap := searchOptions(request).Definition.Limit
	if cap != nil && consumed >= *cap {
		page := SearchCursorPage{Items: []*models.MediaItem{}, Scope: after, TotalExact: includeTotal}
		if includeTotal {
			page.Total = *cap
		}
		return page, nil
	}
	if cap != nil {
		limit = min(limit, *cap-consumed)
	}
	page, err := r.searchCursorPage(ctx, query, itemTypes, limit, after, filter, includeTotal, request...)
	if err != nil {
		return page, err
	}
	if cap != nil {
		if includeTotal && page.Total >= *cap {
			page.TotalExact = true
		}
		page.Total = min(page.Total, *cap)
		if consumed+len(page.Items) >= *cap {
			page.HasMore = false
			page.Next = nil
		}
	}
	if page.Next != nil {
		page.Next.Consumed = consumed + len(page.Items)
	}
	return page, nil
}
