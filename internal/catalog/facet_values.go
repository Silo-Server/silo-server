package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
)

// FacetValue is one facet value and the number of titles in the scope that
// carry it.
type FacetValue struct {
	Value string
	Count int
}

// facetSearchMode picks how a facet typeahead matches and orders values.
type facetSearchMode int

const (
	// facetSearchRanked matches q at the start of the value or of any word in
	// it. Whole-value prefix matches come first, then word-start matches;
	// within each group more titles first, then A-Z. An empty q returns the
	// most common values. It answers v2 values.
	facetSearchRanked facetSearchMode = iota
	// facetSearchPrefix matches q at the start of the value, A-Z by
	// lowercased code points. It answers v2 matches.
	facetSearchPrefix
)

// facetSearch is one typeahead request against a facet.
type facetSearch struct {
	Q     string
	Limit int
	Mode  facetSearchMode
}

const (
	// facetValueCacheTTL bounds how stale a cached value list can be. A
	// title added, removed or re-tagged shows up in typeahead within this
	// window; each node keeps its own cache.
	facetValueCacheTTL = 2 * time.Minute
	// facetValueCacheBudget caps the values held across all cached lists
	// (roughly 100 bytes each with the lowercased copy, so ~50 MB at the
	// cap). Least recently used lists go first. A single scope with more
	// distinct values than this is never cached; its searches push an
	// escaped match into SQL instead.
	facetValueCacheBudget = 500_000
	// facetValueLoadTimeout bounds one list build, which runs on after the
	// request that started it is canceled.
	facetValueLoadTimeout = 30 * time.Second
)

// facetEntry is one cached value with its lowercased form for matching.
type facetEntry struct {
	value string
	lower string
	count int
}

// facetValueList is a scope's whole value list, sorted by count descending
// then case-insensitive A-Z. oversize marks a scope whose list exceeded the
// per-list cap; entries is then empty and searches go to SQL.
type facetValueList struct {
	entries  []facetEntry
	oversize bool
}

func newFacetValueList(values []FacetValue) *facetValueList {
	entries := make([]facetEntry, 0, len(values))
	for _, v := range values {
		entries = append(entries, facetEntry{value: v.Value, lower: strings.ToLower(v.Value), count: v.Count})
	}
	slices.SortFunc(entries, func(a, b facetEntry) int {
		if a.count != b.count {
			return b.count - a.count
		}
		return compareFacetNames(a, b)
	})
	return &facetValueList{entries: entries}
}

// compareFacetNames orders entries case-insensitively A-Z, then by exact
// value so the order is total.
func compareFacetNames(a, b facetEntry) int {
	if c := strings.Compare(a.lower, b.lower); c != 0 {
		return c
	}
	return strings.Compare(a.value, b.value)
}

// search answers one typeahead from the list. hasMore reports whether more
// values matched than limit.
func (l *facetValueList) search(s facetSearch) ([]FacetValue, bool) {
	q := strings.ToLower(strings.TrimSpace(s.Q))
	limit := s.Limit
	if s.Mode == facetSearchPrefix {
		var matched []facetEntry
		for _, e := range l.entries {
			if strings.HasPrefix(e.lower, q) {
				matched = append(matched, e)
			}
		}
		slices.SortFunc(matched, compareFacetNames)
		return facetValuesOf(matched, limit)
	}
	if q == "" {
		return facetValuesOf(l.entries, limit)
	}
	// The list is already in count-then-name order, so each group keeps
	// that order as it fills. Stop once the prefix group alone overflows.
	var prefix, words []facetEntry
	for _, e := range l.entries {
		if strings.HasPrefix(e.lower, q) {
			prefix = append(prefix, e)
			if len(prefix) > limit {
				break
			}
			continue
		}
		if len(words) <= limit && hasWordStartMatch(e.lower, q) {
			words = append(words, e)
		}
	}
	return facetValuesOf(append(prefix, words...), limit)
}

func facetValuesOf(entries []facetEntry, limit int) ([]FacetValue, bool) {
	hasMore := len(entries) > limit
	if hasMore {
		entries = entries[:limit]
	}
	out := make([]FacetValue, len(entries))
	for i, e := range entries {
		out[i] = FacetValue{Value: e.value, Count: e.count}
	}
	return out, hasMore
}

// hasWordStartMatch reports whether q starts a word of lower after its
// first one. A word starts after any rune that is not a letter or digit,
// so "Warner Bros./Pictures" has words "bros" and "pictures".
func hasWordStartMatch(lower, q string) bool {
	if !strings.Contains(lower, q) {
		return false
	}
	prevSep := false
	for i, r := range lower {
		sep := isFacetWordSeparator(r)
		if i > 0 && prevSep && !sep && strings.HasPrefix(lower[i:], q) {
			return true
		}
		prevSep = sep
	}
	return false
}

func isFacetWordSeparator(r rune) bool {
	return !unicode.IsLetter(r) && !unicode.IsDigit(r)
}

// facetValueCache holds scope value lists in memory, keyed by a hash of
// the exact SQL and arguments that built them, so every access predicate
// (libraries, disabled libraries, maturity limits, personal sources) is
// part of the key and two viewers share a list only when their scope SQL
// is identical. Entries expire after ttl; the cache evicts least recently
// used lists once the values it holds exceed budget. Concurrent misses
// for one key run one load.
type facetValueCache struct {
	ttl         time.Duration
	budget      int
	loadTimeout time.Duration
	now         func() time.Time

	mu      sync.Mutex
	entries map[string]*facetCacheItem
	// lru is the sentinel of a ring of the entries: lru.next is the most
	// recently used, lru.prev the least.
	lru     facetCacheItem
	used    int
	flights map[string]*facetFlight
}

type facetCacheItem struct {
	key        string
	list       *facetValueList
	expires    time.Time
	weight     int
	prev, next *facetCacheItem
}

type facetFlight struct {
	done chan struct{}
	list *facetValueList
	err  error
	// waiters counts the callers that joined the flight, under the cache
	// lock.
	waiters int
}

func newFacetValueCache(ttl time.Duration, budget int) *facetValueCache {
	c := &facetValueCache{
		ttl:         ttl,
		budget:      budget,
		loadTimeout: facetValueLoadTimeout,
		now:         time.Now,
		entries:     make(map[string]*facetCacheItem),
		flights:     make(map[string]*facetFlight),
	}
	c.lru.prev, c.lru.next = &c.lru, &c.lru
	return c
}

// maxListValues is the most values one list may hold and still be cached.
func (c *facetValueCache) maxListValues() int {
	if c == nil {
		return facetValueCacheBudget
	}
	return c.budget
}

// get returns the cached list for key, or waits for the one load that
// builds it for every concurrent caller of the key. The load runs apart
// from the callers' contexts, bounded by loadTimeout: typeahead clients
// cancel a request on the next keystroke, and the next keystroke needs the
// same list. A caller whose context ends stops waiting; a failed load
// caches nothing and every waiter gets its error.
func (c *facetValueCache) get(ctx context.Context, key string, load func(context.Context) (*facetValueList, error)) (*facetValueList, error) {
	if c == nil {
		return load(ctx)
	}
	c.mu.Lock()
	if item, ok := c.entries[key]; ok {
		if c.now().Before(item.expires) {
			c.unlinkLocked(item)
			c.pushFrontLocked(item)
			c.mu.Unlock()
			return item.list, nil
		}
		c.removeLocked(item)
	}
	f, ok := c.flights[key]
	if ok {
		f.waiters++
	} else {
		f = &facetFlight{done: make(chan struct{})}
		c.flights[key] = f
		go c.fly(context.WithoutCancel(ctx), key, f, load)
	}
	c.mu.Unlock()
	select {
	case <-f.done:
		return f.list, f.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// fly runs the load for a flight and publishes its result, caching it
// when it succeeded. A panicking load fails the flight instead of the
// process.
func (c *facetValueCache) fly(ctx context.Context, key string, f *facetFlight, load func(context.Context) (*facetValueList, error)) {
	ctx, cancel := context.WithTimeout(ctx, c.loadTimeout)
	defer func() {
		cancel()
		if r := recover(); r != nil {
			f.list, f.err = nil, fmt.Errorf("facet value load panicked: %v", r)
		}
		c.mu.Lock()
		delete(c.flights, key)
		if f.err == nil {
			c.storeLocked(key, f.list)
		}
		c.mu.Unlock()
		close(f.done)
	}()
	f.list, f.err = load(ctx)
}

func (c *facetValueCache) storeLocked(key string, l *facetValueList) {
	weight := max(len(l.entries), 1)
	if weight > c.budget {
		return
	}
	item := &facetCacheItem{key: key, list: l, expires: c.now().Add(c.ttl), weight: weight}
	c.pushFrontLocked(item)
	c.entries[key] = item
	c.used += weight
	for c.used > c.budget {
		c.removeLocked(c.lru.prev)
	}
}

func (c *facetValueCache) removeLocked(item *facetCacheItem) {
	c.unlinkLocked(item)
	delete(c.entries, item.key)
	c.used -= item.weight
}

func (c *facetValueCache) unlinkLocked(item *facetCacheItem) {
	item.prev.next, item.next.prev = item.next, item.prev
}

func (c *facetValueCache) pushFrontLocked(item *facetCacheItem) {
	item.prev, item.next = &c.lru, c.lru.next
	c.lru.next.prev = item
	c.lru.next = item
}

// facetColumn names a media_items column a facet reads, and whether it is a
// text[] (genres, studios, networks, countries) or a scalar text column.
type facetColumn struct {
	name  string
	array bool
}

// facetCacheKey hashes the list query and its arguments. Arguments that do
// not encode to JSON make the scope uncacheable rather than risk two scopes
// sharing a key.
func facetCacheKey(query string, args []any) (string, bool) {
	encoded, err := json.Marshal(args)
	if err != nil {
		return "", false
	}
	sum := sha256.New()
	sum.Write([]byte(query))
	sum.Write([]byte{0})
	sum.Write(encoded)
	return string(sum.Sum(nil)), true
}

// searchColumnFacet answers a typeahead over an array or scalar column
// facet from the scope's cached value list, building it on a miss. A scope
// too large to cache is searched in SQL.
func searchColumnFacet(
	ctx context.Context,
	pool *pgxpool.Pool,
	cache *facetValueCache,
	column facetColumn,
	filters BrowseFilters,
	baseRelation string,
	mediaScope string,
	s facetSearch,
) ([]FacetValue, bool, error) {
	if s.Limit <= 0 {
		return []FacetValue{}, false, nil
	}
	fromClause, whereClause, args, empty := filterWhereClauseForSource(filters, baseRelation, mediaScope)
	if empty {
		return []FacetValue{}, false, nil
	}
	values := facetValuesSource(column, fromClause, whereClause, filters.PersonID > 0)
	maxValues := cache.maxListValues()
	listQuery := fmt.Sprintf(`SELECT name, n FROM %s LIMIT %d`, values, maxValues+1)
	load := func(ctx context.Context) (*facetValueList, error) {
		got, err := scanFacetValues(ctx, pool, listQuery, args)
		if err != nil {
			return nil, fmt.Errorf("listing %s values: %w", column.name, err)
		}
		if len(got) > maxValues {
			return &facetValueList{oversize: true}, nil
		}
		return newFacetValueList(got), nil
	}
	var l *facetValueList
	var err error
	if key, ok := facetCacheKey(listQuery, args); ok {
		l, err = cache.get(ctx, key, load)
	} else {
		l, err = load(ctx)
	}
	if err != nil {
		return nil, false, err
	}
	if !l.oversize {
		matches, hasMore := l.search(s)
		return matches, hasMore, nil
	}
	return searchFacetValuesSQL(ctx, pool, values, args, s)
}

// facetValuesSource is a subquery of every non-blank (trimmed) value of the
// column in the scope with its title count, as (name, n). Credits joined
// for a person scope can repeat a title, so that scope counts distinct
// titles.
func facetValuesSource(column facetColumn, fromClause, whereClause string, distinctTitles bool) string {
	count := "count(*)"
	if distinctTitles {
		count = "count(DISTINCT cid)"
	}
	value := "mi." + column.name
	if column.array {
		value = "UNNEST(" + value + ")"
	}
	return fmt.Sprintf(`(
		SELECT name, %s AS n FROM (
			SELECT mi.content_id AS cid, BTRIM(%s) AS name
			FROM %s
			%s
		) vals
		WHERE name <> ''
		GROUP BY name
	) facet_values`, count, value, fromClause, whereClause)
}

// searchFacetValuesSQL is the uncached search for a scope whose value list
// is over the cache cap. It matches and orders like facetValueList.search:
// q is escaped so % and _ are literal, names compare in code point order
// (COLLATE "C", as Go compares strings), and letters and digits decide
// word starts on both sides, though Postgres and Go may disagree on
// exotic characters.
func searchFacetValuesSQL(ctx context.Context, pool *pgxpool.Pool, values string, args []any, s facetSearch) ([]FacetValue, bool, error) {
	q := strings.TrimSpace(s.Q)
	args = slices.Clone(args)
	bind := func(v any) int {
		args = append(args, v)
		return len(args)
	}
	var where, order string
	switch {
	case s.Mode == facetSearchPrefix:
		where = fmt.Sprintf("WHERE LOWER(name) LIKE $%d ESCAPE '\\'", bind(likePrefixPattern(q)))
		order = `LOWER(name) COLLATE "C", name COLLATE "C"`
	case q == "":
		order = `n DESC, LOWER(name) COLLATE "C", name COLLATE "C"`
	default:
		where = fmt.Sprintf("WHERE LOWER(name) ~ $%d", bind(`(^|[^[:alnum:]])`+escapeRegexLiteral(strings.ToLower(q))))
		order = fmt.Sprintf(`(LOWER(name) LIKE $%d ESCAPE '\') DESC, n DESC, LOWER(name) COLLATE "C", name COLLATE "C"`, bind(likePrefixPattern(q)))
	}
	// OFFSET 0 keeps Postgres from pushing the match below the GROUP BY,
	// where it would run once per title instead of once per value.
	query := fmt.Sprintf(`SELECT name, n FROM (SELECT name, n FROM %s OFFSET 0) v %s ORDER BY %s LIMIT %d`, values, where, order, s.Limit+1)
	return searchFacetValues(ctx, pool, query, args, s.Limit)
}

// escapeRegexLiteral escapes s for a Postgres regular expression. A
// backslash before a non-alphanumeric character always means that literal
// character, so escaping every ASCII punctuation or space is safe.
func escapeRegexLiteral(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < utf8.RuneSelf && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// scanFacetValues reads (name, count) rows, trimming names and skipping
// blank ones.
func scanFacetValues(ctx context.Context, pool *pgxpool.Pool, query string, args []any) ([]FacetValue, error) {
	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []FacetValue{}
	for rows.Next() {
		var v FacetValue
		if err := rows.Scan(&v.Value, &v.Count); err != nil {
			return nil, err
		}
		v.Value = strings.TrimSpace(v.Value)
		if v.Value == "" {
			continue
		}
		values = append(values, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

// searchFacetValues runs a typeahead query built with LIMIT limit+1 and
// splits off hasMore.
func searchFacetValues(ctx context.Context, pool *pgxpool.Pool, query string, args []any, limit int) ([]FacetValue, bool, error) {
	got, err := scanFacetValues(ctx, pool, query, args)
	if err != nil {
		return nil, false, err
	}
	if len(got) > limit {
		return got[:limit], true, nil
	}
	return got, false, nil
}
