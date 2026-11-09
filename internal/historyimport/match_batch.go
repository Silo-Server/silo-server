package historyimport

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// maxLookupBatch bounds the values one batched catalog read names.
const maxLookupBatch = 20_000

// maxMatchPasses bounds MatchAllOn's passes: an episode's match reads its
// own ids, then its series' ids, then the series' episode, so three passes
// read everything and a fourth finds nothing new.
const maxMatchPasses = 6

// MatchAllOn matches every record as Match does, with the catalog read in
// batches on q rather than a query per lookup: it runs the matcher over all
// records against what it has read, collects the lookups it has not read
// yet, reads them a batch at a time (one query per lookup and provider
// column, maxLookupBatch values each), and runs again until nothing is
// left to read. A lookup that finds nothing is remembered as such. The
// result holds each record's match, nil for none.
func MatchAllOn(ctx context.Context, q lookupQuerier, records []Record) ([]*Match, error) {
	matches, _, err := matchAllWithReasons(ctx, q, records)
	return matches, err
}

// matchAllWithReasons is MatchAllOn that also says why each unmatched record
// did not match, as Match does.
func matchAllWithReasons(ctx context.Context, q lookupQuerier, records []Record) ([]*Match, []string, error) {
	b := &batchLookup{q: q, media: map[mediaKey][]mediaLookupRow{}, episodes: map[columnValue][]mediaLookupRow{},
		seriesEpisodes: map[seriesIDEpisode][]mediaLookupRow{}, bySeries: map[seriesEpisode]*Match{}}
	m := NewMatcher(b)
	for range maxMatchPasses {
		b.pending = pendingLookups{}
		out := make([]*Match, len(records))
		reasons := make([]string, len(records))
		for i, r := range records {
			match, reason, err := m.Match(ctx, r)
			if err != nil {
				return nil, nil, err
			}
			out[i], reasons[i] = match, reason
		}
		if b.pending.empty() {
			return out, reasons, nil
		}
		if err := b.read(ctx); err != nil {
			return nil, nil, err
		}
	}
	return nil, nil, errors.New("matching items did not settle")
}

type (
	mediaKey        struct{ kind, column, value string }
	columnValue     struct{ column, value string }
	seriesIDEpisode struct {
		column, value   string
		season, episode int
	}
	seriesEpisode struct {
		series          string
		season, episode int
	}
)

// pendingLookups are the lookups a pass asked for that are not read yet.
type pendingLookups struct {
	media          map[mediaKey]bool
	episodes       map[columnValue]bool
	seriesEpisodes map[seriesIDEpisode]bool
	bySeries       map[seriesEpisode]bool
}

func (p pendingLookups) empty() bool {
	return len(p.media)+len(p.episodes)+len(p.seriesEpisodes)+len(p.bySeries) == 0
}

func mark[K comparable](m *map[K]bool, k K) {
	if *m == nil {
		*m = map[K]bool{}
	}
	(*m)[k] = true
}

// batchLookup answers the matcher's lookups from what it has read; one it
// has not read finds nothing for now and is recorded in pending.
type batchLookup struct {
	q              lookupQuerier
	media          map[mediaKey][]mediaLookupRow
	episodes       map[columnValue][]mediaLookupRow
	seriesEpisodes map[seriesIDEpisode][]mediaLookupRow
	bySeries       map[seriesEpisode]*Match
	pending        pendingLookups
}

func (b *batchLookup) MatchMediaByExternalID(_ context.Context, kind, column, value string) ([]mediaLookupRow, error) {
	if value == "" {
		return nil, nil
	}
	k := mediaKey{kind, column, value}
	rows, ok := b.media[k]
	if !ok {
		mark(&b.pending.media, k)
	}
	return rows, nil
}

func (b *batchLookup) MatchEpisodeByExternalID(_ context.Context, column, value string) ([]mediaLookupRow, error) {
	if value == "" {
		return nil, nil
	}
	k := columnValue{column, value}
	rows, ok := b.episodes[k]
	if !ok {
		mark(&b.pending.episodes, k)
	}
	return rows, nil
}

func (b *batchLookup) MatchEpisodeBySeriesExternalID(_ context.Context, column, value string, season, episode int) ([]mediaLookupRow, error) {
	if value == "" {
		return nil, nil
	}
	k := seriesIDEpisode{column, value, season, episode}
	rows, ok := b.seriesEpisodes[k]
	if !ok {
		mark(&b.pending.seriesEpisodes, k)
	}
	return rows, nil
}

func (b *batchLookup) MatchEpisodeBySeries(_ context.Context, series string, season, episode int) (*Match, error) {
	k := seriesEpisode{series, season, episode}
	match, ok := b.bySeries[k]
	if !ok {
		mark(&b.pending.bySeries, k)
	}
	return match, nil
}

func (b *batchLookup) MatchEpisodesBySeries(context.Context, string, *int, *time.Time, []int) ([]Match, error) {
	return nil, errors.New("batched matching does not expand series")
}

// The provider id columns the matcher looks up by; only these are named in
// SQL.
const (
	columnTMDB = "tmdb_id"
	columnIMDb = "imdb_id"
	columnTVDB = "tvdb_id"
)

var providerColumns = map[string]bool{columnTMDB: true, columnIMDb: true, columnTVDB: true}

// read reads every pending lookup, in batches, and records each, empty
// when it finds nothing.
func (b *batchLookup) read(ctx context.Context) error {
	media := map[[2]string][]string{}
	for k := range b.pending.media {
		media[[2]string{k.kind, k.column}] = append(media[[2]string{k.kind, k.column}], k.value)
	}
	for kc, values := range media {
		kind, column := kc[0], kc[1]
		for _, v := range values {
			b.media[mediaKey{kind, column, v}] = nil // read: nothing unless a row says otherwise
		}
		err := b.batches(ctx, column, values, func(chunk []string) (string, []any) {
			return `SELECT content_id, title, COALESCE(year, 0), ` + column + ` FROM media_items
				WHERE type = $1 AND status = 'matched' AND ` + column + ` = ANY($2) ORDER BY content_id ASC`, []any{kind, chunk}
		}, func(row mediaLookupRow, key string) {
			k := mediaKey{kind, column, key}
			b.media[k] = append(b.media[k], row)
		})
		if err != nil {
			return err
		}
	}
	episodes := map[string][]string{}
	for k := range b.pending.episodes {
		episodes[k.column] = append(episodes[k.column], k.value)
	}
	for column, values := range episodes {
		for _, v := range values {
			b.episodes[columnValue{column, v}] = nil
		}
		err := b.batches(ctx, column, values, func(chunk []string) (string, []any) {
			return `SELECT e.content_id, COALESCE(e.title, ''), COALESCE(series.year, 0), e.` + column + ` FROM episodes e
				LEFT JOIN media_items series ON series.content_id = e.series_id
				WHERE e.` + column + ` = ANY($1) ORDER BY e.content_id ASC`, []any{chunk}
		}, func(row mediaLookupRow, key string) {
			k := columnValue{column, key}
			b.episodes[k] = append(b.episodes[k], row)
		})
		if err != nil {
			return err
		}
	}
	if err := b.readSeriesEpisodes(ctx); err != nil {
		return err
	}
	return b.readBySeries(ctx)
}

// batches reads values maxLookupBatch at a time: query builds a chunk's
// SQL, whose rows are a lookup row and then the value it was found by.
func (b *batchLookup) batches(ctx context.Context, column string, values []string, query func([]string) (string, []any),
	found func(mediaLookupRow, string)) error {
	if !providerColumns[column] {
		return fmt.Errorf("matching by %q", column)
	}
	for start := 0; start < len(values); start += maxLookupBatch {
		sql, args := query(values[start:min(start+maxLookupBatch, len(values))])
		rows, err := b.q.Query(ctx, sql, args...)
		if err != nil {
			return fmt.Errorf("matching by %s: %w", column, err)
		}
		for rows.Next() {
			var row mediaLookupRow
			var key string
			if err := rows.Scan(&row.ContentID, &row.Title, &row.Year, &key); err != nil {
				rows.Close()
				return fmt.Errorf("matching by %s: %w", column, err)
			}
			found(row, key)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("matching by %s: %w", column, err)
		}
	}
	return nil
}

func (b *batchLookup) readSeriesEpisodes(ctx context.Context) error {
	byColumn := map[string][]seriesIDEpisode{}
	for k := range b.pending.seriesEpisodes {
		byColumn[k.column] = append(byColumn[k.column], k)
	}
	for column, keys := range byColumn {
		if !providerColumns[column] {
			return fmt.Errorf("matching by series %q", column)
		}
		for start := 0; start < len(keys); start += maxLookupBatch {
			chunk := keys[start:min(start+maxLookupBatch, len(keys))]
			values, seasons, numbers := make([]string, len(chunk)), make([]int, len(chunk)), make([]int, len(chunk))
			for i, k := range chunk {
				values[i], seasons[i], numbers[i] = k.value, k.season, k.episode
				b.seriesEpisodes[k] = nil
			}
			rows, err := b.q.Query(ctx, `
				SELECT e.content_id, COALESCE(e.title, ''), COALESCE(series.year, 0), u.value, u.season, u.episode
				FROM unnest($1::text[], $2::int[], $3::int[]) AS u(value, season, episode)
				JOIN media_items series ON series.type = 'series' AND series.status = 'matched' AND series.`+column+` = u.value
				JOIN episodes e ON e.series_id = series.content_id AND e.season_number = u.season AND e.episode_number = u.episode
				ORDER BY e.content_id ASC`, values, seasons, numbers)
			if err != nil {
				return fmt.Errorf("matching episodes by series %s: %w", column, err)
			}
			for rows.Next() {
				var row mediaLookupRow
				var k seriesIDEpisode
				if err := rows.Scan(&row.ContentID, &row.Title, &row.Year, &k.value, &k.season, &k.episode); err != nil {
					rows.Close()
					return fmt.Errorf("matching episodes by series %s: %w", column, err)
				}
				k.column = column
				b.seriesEpisodes[k] = append(b.seriesEpisodes[k], row)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return fmt.Errorf("matching episodes by series %s: %w", column, err)
			}
		}
	}
	return nil
}

func (b *batchLookup) readBySeries(ctx context.Context) error {
	var keys []seriesEpisode
	for k := range b.pending.bySeries {
		keys = append(keys, k)
	}
	for start := 0; start < len(keys); start += maxLookupBatch {
		chunk := keys[start:min(start+maxLookupBatch, len(keys))]
		series, seasons, numbers := make([]string, len(chunk)), make([]int, len(chunk)), make([]int, len(chunk))
		for i, k := range chunk {
			series[i], seasons[i], numbers[i] = k.series, k.season, k.episode
			b.bySeries[k] = nil
		}
		rows, err := b.q.Query(ctx, `
			SELECT e.content_id, COALESCE(e.title, ''), COALESCE(series.year, 0), u.series, u.season, u.episode
			FROM unnest($1::text[], $2::int[], $3::int[]) AS u(series, season, episode)
			JOIN episodes e ON e.series_id = u.series AND e.season_number = u.season AND e.episode_number = u.episode
			LEFT JOIN media_items series ON series.content_id = e.series_id
			ORDER BY e.content_id ASC`, series, seasons, numbers)
		if err != nil {
			return fmt.Errorf("matching episodes: %w", err)
		}
		for rows.Next() {
			match := &Match{Kind: KindEpisode}
			var k seriesEpisode
			if err := rows.Scan(&match.MediaItemID, &match.Title, &match.Year, &k.series, &k.season, &k.episode); err != nil {
				rows.Close()
				return fmt.Errorf("matching episodes: %w", err)
			}
			if b.bySeries[k] == nil {
				b.bySeries[k] = match
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("matching episodes: %w", err)
		}
	}
	return nil
}
