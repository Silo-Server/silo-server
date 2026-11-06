package backup

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/historyimport"
)

// itemResolver maps a document's content ids to this server's catalog. A
// content id this catalog holds is the same item (content ids are
// deterministic); any other is matched by the provider ids the document
// recorded, through the history import matcher. Items it cannot map are
// recorded by title for the report.
type itemResolver struct {
	refs      map[string]ItemRef
	present   map[string]bool
	matched   map[string]string
	unmatched map[string]string
	hidden    map[string]string
}

// newItemResolver checks in one query which ids the catalog holds, every
// ref and every extra id a document names, and matches every other ref by
// its provider ids at once, in batched reads (historyimport.MatchAllOn), so
// resolving reads nothing per item. Both read through q: the caller's
// transaction already holds a connection, and a read that waited on the
// pool for another could wait forever.
func newItemResolver(ctx context.Context, q queryer, refs []ItemRef, extra []string) (*itemResolver, error) {
	r := &itemResolver{refs: map[string]ItemRef{}, present: map[string]bool{}, matched: map[string]string{},
		unmatched: map[string]string{}, hidden: map[string]string{}}
	ids := map[string]bool{}
	for _, ref := range refs {
		r.refs[ref.ContentID] = ref
		ids[ref.ContentID] = true
	}
	for _, id := range extra {
		ids[id] = true
	}
	if len(ids) == 0 {
		return r, nil
	}
	found, err := collectStringsArgs(ctx, q, `
		SELECT id FROM unnest($1::text[]) AS id
		WHERE EXISTS (SELECT 1 FROM media_items WHERE content_id = id)
		   OR EXISTS (SELECT 1 FROM episodes WHERE content_id = id)
		   OR EXISTS (SELECT 1 FROM seasons WHERE content_id = id)`, slices.Collect(maps.Keys(ids)))
	if err != nil {
		return nil, fmt.Errorf("finding items: %w", err)
	}
	for _, id := range found {
		r.present[id] = true
	}
	if err := r.matchAll(ctx, q); err != nil {
		return nil, err
	}
	return r, nil
}

// matchAll matches every ref the catalog does not hold by its provider
// ids; one without any matches nothing.
func (r *itemResolver) matchAll(ctx context.Context, q queryer) error {
	var sources []string
	var records []historyimport.Record
	for _, source := range slices.Sorted(maps.Keys(r.refs)) {
		if r.present[source] {
			continue
		}
		record, matchable := matchRecord(r.refs[source])
		if !matchable {
			r.matched[source] = ""
			continue
		}
		sources, records = append(sources, source), append(records, record)
	}
	if len(records) == 0 {
		return nil
	}
	matches, err := historyimport.MatchAllOn(ctx, q, records)
	if err != nil {
		return fmt.Errorf("matching items: %w", err)
	}
	for i, source := range sources {
		r.matched[source] = ""
		if matches[i] != nil {
			r.matched[source] = matches[i].MediaItemID
		}
	}
	return nil
}

func (r *itemResolver) Resolve(ctx context.Context, source string) (string, bool, error) {
	local, ok, err := r.find(ctx, source)
	if err == nil && !ok {
		r.unmatched[source] = r.label(source)
	}
	return local, ok, err
}

// contentFilter is what a profile may see: the canonical catalog visibility
// (catalog.FilterAccessibleContentIDsInTransaction) with its libraries (nil
// when unrestricted) and maturity limits.
type contentFilter struct {
	libraries []int
	limits    access.MaturityLimits
}

// visibleTo resolves through r and leaves out items the profile may not see,
// so an import never gives a profile data about titles hidden from it.
func (r *itemResolver) visibleTo(ctx context.Context, tx pgx.Tx, f contentFilter) (*filteredItems, error) {
	v := &filteredItems{r: r, tx: tx, filter: f, visible: map[string]bool{}}
	// Every item the resolver can return, checked at once.
	ids := slices.Collect(maps.Keys(r.present))
	for _, local := range r.matched {
		if local != "" {
			ids = append(ids, local)
		}
	}
	if err := v.check(ctx, ids); err != nil {
		return nil, err
	}
	return v, nil
}

// filteredItems is an itemResolver seen through one profile's access.
type filteredItems struct {
	r       *itemResolver
	tx      pgx.Tx
	filter  contentFilter
	visible map[string]bool
}

func (v *filteredItems) check(ctx context.Context, ids []string) error {
	var todo []string
	for _, id := range ids {
		if _, done := v.visible[id]; !done {
			todo = append(todo, id)
		}
	}
	if len(todo) == 0 {
		return nil
	}
	ok, err := catalog.FilterAccessibleContentIDsInTransaction(ctx, v.tx, todo, v.filter.libraries, nil, v.filter.limits)
	if err != nil {
		return fmt.Errorf("checking access to items: %w", err)
	}
	for _, id := range todo {
		v.visible[id] = ok[id]
	}
	return nil
}

func (v *filteredItems) Resolve(ctx context.Context, source string) (string, bool, error) {
	local, ok, err := v.r.Resolve(ctx, source)
	if err != nil || !ok {
		return "", false, err
	}
	if err := v.check(ctx, []string{local}); err != nil {
		return "", false, err
	}
	if !v.visible[local] {
		v.r.hidden[source] = v.r.label(source)
		return "", false, nil
	}
	return local, true, nil
}

func (r *itemResolver) find(_ context.Context, source string) (string, bool, error) {
	if r.present[source] {
		return source, true, nil
	}
	local := r.matched[source]
	return local, local != "", nil
}

// matchRecord turns a ref into a matcher record; false when it carries
// nothing a match can use.
func matchRecord(ref ItemRef) (historyimport.Record, bool) {
	rec := historyimport.Record{Title: ref.Title, Year: ref.Year, IMDbID: ref.IMDbID, TMDBID: ref.TMDBID, TVDBID: ref.TVDBID}
	switch ref.Kind {
	case historyimport.KindMovie, historyimport.KindSeries:
		rec.Kind = ref.Kind
		return rec, ref.IMDbID != "" || ref.TMDBID != "" || ref.TVDBID != ""
	case historyimport.KindEpisode:
		rec.Kind = historyimport.KindEpisode
		rec.SeasonNumber, rec.EpisodeNumber = ref.Season, ref.Episode
		if s := ref.Series; s != nil {
			rec.SeriesTitle, rec.SeriesYear, rec.SeriesIMDbID, rec.SeriesTMDBID, rec.SeriesTVDBID = s.Title, s.Year, s.IMDbID, s.TMDBID, s.TVDBID
		}
		return rec, rec.IMDbID != "" || rec.TMDBID != "" || rec.TVDBID != "" || rec.SeriesIMDbID != "" || rec.SeriesTMDBID != "" || rec.SeriesTVDBID != ""
	}
	return rec, false
}

func (r *itemResolver) label(source string) string {
	if ref, ok := r.refs[source]; ok {
		return ref.label()
	}
	return source
}

// maxListedTitles bounds the titles a report lists.
const maxListedTitles = 200

// unmatchedTitles lists the items the resolver could not map, by title.
func (r *itemResolver) unmatchedTitles() []string { return sortedLabels(r.unmatched) }

// hiddenTitles lists items left out because the profile cannot see them.
func (r *itemResolver) hiddenTitles() []string { return sortedLabels(r.hidden) }

func sortedLabels(m map[string]string) []string {
	out := slices.Sorted(maps.Values(m))
	if len(out) > maxListedTitles {
		more := len(out) - maxListedTitles
		out = append(out[:maxListedTitles], fmt.Sprintf("and %d more", more))
	}
	return out
}
