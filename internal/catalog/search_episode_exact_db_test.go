package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/access"
)

type episodeExactTierTracer struct{ exact, general atomic.Int64 }

func (tracer *episodeExactTierTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "exact episode tier") {
		tracer.exact.Add(1)
	} else if strings.Contains(data.SQL, ", page AS (") && strings.Contains(data.SQL, "ece.search_title_vector") {
		tracer.general.Add(1)
	}
	return ctx
}
func (*episodeExactTierTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestEpisodeExactTierCursorAndPlanPostgres(t *testing.T) {
	tracer := &episodeExactTierTracer{}
	pool := searchOptimizationPool(t, tracer)
	ctx := t.Context()
	prefix := fmt.Sprintf("exact-tier-%d", time.Now().UnixNano())
	var library, other int
	for i, id := range []*int{&library, &other} {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('series',$1,true) RETURNING id`, fmt.Sprintf("%s-%d", prefix, i)).Scan(id); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=ANY($1)`, []int{library, other})
	})
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	series := prefix + "-series"
	exec(`INSERT INTO media_items(content_id,type,title,year,content_rating,advisory_age) VALUES($1,'series','Fixture',2024,'PG',10)`, series)
	exec(`INSERT INTO media_items(content_id,type,title) SELECT $1||'-other-parent-'||i::text,'series','Unrelated Parent' FROM generate_series(1,3000)i`, prefix)
	exec(`INSERT INTO media_item_libraries(content_id,media_folder_id) SELECT content_id,$2 FROM media_items WHERE content_id LIKE $1`, prefix+"-other-parent-%", library)
	exec(`INSERT INTO episodes(content_id,series_id,season_number,episode_number,title)
		SELECT $1||'-spill-episode-'||i::text,$1||'-other-parent-'||i::text,1,1,'Parentspill Entry' FROM generate_series(1,3000)i`, prefix)
	exec(`INSERT INTO episode_libraries(episode_id,media_folder_id) SELECT content_id,$2 FROM episodes WHERE content_id LIKE $1`, prefix+"-spill-episode-%", library)
	exec(`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, series, library)
	exec(`INSERT INTO episodes(content_id,series_id,season_number,episode_number,title,overview,air_date)
		SELECT $1||'-episode-'||lpad(i::text,4,'0'),$2,1,i,
		CASE WHEN i<=200 THEN (ARRAY['Star','STAR!','Star &'])[1+i%3]
		WHEN i<=600 THEN 'Star Adventure' WHEN i<=900 THEN 'Pilot' ELSE 'Quartzrare Adventure' END,
		'A buried signal returns. A buried signal returns.','2024-01-01' FROM generate_series(1,1000)i`, prefix, series)
	exec(`INSERT INTO episode_libraries(episode_id,media_folder_id) SELECT content_id,$2 FROM episodes WHERE series_id=$1`, series, library)
	exec(`INSERT INTO episode_libraries(episode_id,media_folder_id) VALUES($1,$2)`, prefix+"-episode-0001", other)
	const collisionOther = "collision45871bb8652a039deda7d8990281e7cc"
	const collisionTarget = "collisionc1e7f5d04fa87fb7f6aeb52f7b0f1484"
	var collision bool
	if err := pool.QueryRow(ctx, `SELECT hashtext($1)=hashtext($2)`, collisionOther, collisionTarget).Scan(&collision); err != nil || !collision {
		t.Fatalf("hash collision fixture: collision=%v err=%v", collision, err)
	}
	exec(`INSERT INTO episodes(content_id,series_id,season_number,episode_number,title,overview,air_date)
		SELECT $1||'-extra-'||lpad(i::text,4,'0'),$2,2,i,
		CASE WHEN i<=30 THEN $3 WHEN i<=60 THEN $4 WHEN i=61 THEN 'Limited' ELSE 'Limited Adventure' END,
		'','2024-01-01' FROM generate_series(1,90)i`, prefix, series, collisionOther, collisionTarget)
	exec(`INSERT INTO episode_libraries(episode_id,media_folder_id) SELECT content_id,$2 FROM episodes WHERE series_id=$1 AND season_number=2`, series, library)
	exec(`ANALYZE episode_catalog_entries; ANALYZE episodes; ANALYZE media_items; ANALYZE media_item_libraries`)
	repo := NewItemRepository(pool)
	filter := AccessFilter{AllowedLibraryIDs: []int{library}}
	parsed := parseSearchQuery("Star")
	options := &searchCursorSQL{}
	fullSQL, _, args := repo.buildMixedSearchCursorSQL(parsed, []string{"episode"}, 2000, 0, filter, false, options)
	rows, err := pool.Query(ctx, fullSQL, args...)
	if err != nil {
		t.Fatal(err)
	}
	wrapped := &cursorRows{Rows: rows, terms: searchFTSTerms()}
	baseline, err := scanItems(wrapped)
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(baseline) != 600 {
		t.Fatalf("baseline=%d", len(baseline))
	}
	var got []string
	var after *SearchCursor
	fastPages := 0
	for range 50 {
		tracer.exact.Store(0)
		tracer.general.Store(0)
		page, err := repo.SearchCursorPage(ctx, "Star", []string{"episode"}, 17, after, filter, false)
		if err != nil {
			t.Fatal(err)
		}
		if tracer.exact.Load() == 1 && tracer.general.Load() == 0 {
			fastPages++
		}
		for _, item := range page.Items {
			got = append(got, item.ContentID)
		}
		if page.HasMore && !reflect.DeepEqual(page.Next.Keys, wrapped.keys[len(got)-1].Keys) {
			t.Fatalf("exact tier changed full cursor tuple at position%d: %v vs %v", len(got), page.Next.Keys, wrapped.keys[len(got)-1].Keys)
		}
		if !page.HasMore {
			break
		}
		after = page.Next
	}
	if !slices.Equal(got, contentIDsFromMediaItems(baseline)) || fastPages == 0 {
		t.Fatalf("tier traversal=%d baseline=%d fast pages=%d", len(got), len(baseline), fastPages)
	}
	for _, tc := range []struct {
		name, query    string
		types          []string
		filter         AccessFilter
		total          bool
		options        SearchCursorOptions
		exact, general int64
	}{
		{name: "full exact", query: "Star", types: []string{"episode"}, filter: filter, exact: 1},
		{name: "hash collision", query: collisionTarget, types: []string{"episode"}, filter: filter, exact: 1},
		{name: "insufficient exact", query: "Limited", types: []string{"episode"}, filter: filter, exact: 1, general: 1},
		{name: "missing exact", query: "Quartzrare", types: []string{"episode"}, filter: filter, exact: 1, general: 1},
		{name: "missing family", query: "Absentunique", types: []string{"episode"}, filter: filter, exact: 1, general: 1},
		{name: "exact short title", query: "St", types: []string{"episode"}, filter: filter, exact: 1, general: 1},
		{name: "leading short title", query: "Star A", types: []string{"episode"}, filter: filter, exact: 1, general: 1},
		{name: "phrase", query: `"Star"`, types: []string{"episode"}, filter: filter, general: 1},
		{name: "year", query: "Star 2024", types: []string{"episode"}, filter: filter, general: 1},
		{name: "mixed", query: "Star", filter: filter, general: 1},
		{name: "multi library", query: "Star", types: []string{"episode"}, filter: AccessFilter{AllowedLibraryIDs: []int{library, other}}, general: 1},
		{name: "total", query: "Star", types: []string{"episode"}, filter: filter, total: true, general: 1},
		{name: "group", query: "Star", types: []string{"episode"}, filter: filter, options: SearchCursorOptions{GroupByWork: true}, exact: 1, general: 1},
		{name: "rules", query: "Star", types: []string{"episode"}, filter: filter, options: SearchCursorOptions{Definition: QueryDefinition{Groups: []QueryGroup{{Rules: []QueryRule{{Field: "year", Op: "gte", Value: 2020}}}}}}, general: 1},
		{name: "disabled membership", query: "Star", types: []string{"episode"}, filter: AccessFilter{AllowedLibraryIDs: []int{library}, DisabledLibraryIDs: []int{other}}, exact: 1},
		{name: "maturity", query: "Star", types: []string{"episode"}, filter: AccessFilter{AllowedLibraryIDs: []int{library}, MaturityLimits: access.MaturityLimits{MaxContentRating: "PG-13", MaxAdvisoryAge: 13}}, exact: 1, general: 1},
		{name: "maturity allow unrated", query: "Star", types: []string{"episode"}, filter: AccessFilter{AllowedLibraryIDs: []int{library}, MaturityLimits: access.MaturityLimits{MaxContentRating: "PG-13", AllowUnratedContent: true, MaxAdvisoryAge: 13}}, exact: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tracer.exact.Store(0)
			tracer.general.Store(0)
			page, err := repo.SearchCursorPage(ctx, tc.query, tc.types, 20, nil, tc.filter, tc.total, tc.options)
			if err != nil {
				t.Fatal(err)
			}
			if tracer.exact.Load() != tc.exact || tracer.general.Load() != tc.general {
				t.Fatalf("queries exact=%d general=%d want%d/%d", tracer.exact.Load(), tracer.general.Load(), tc.exact, tc.general)
			}
			reference, _, _, err := repo.searchFTSCursorRows(ctx, parseSearchQuery(tc.query), tc.types, 21, nil, tc.filter, true, false, 0, tc.options)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(contentIDsFromMediaItems(page.Items), contentIDsFromMediaItems(reference[:min(20, len(reference))])) {
				t.Fatalf("policy/admission changed IDs=%v baseline=%v", contentIDsFromMediaItems(page.Items), contentIDsFromMediaItems(reference))
			}
		})
	}
	planOptions := &searchCursorSQL{}
	repo.buildMixedSearchCursorSQL(parsed, []string{"episode"}, 21, 0, filter, false, planOptions)
	var plan json.RawMessage
	if err := pool.QueryRow(ctx, "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON,TIMING OFF) "+planOptions.exactSQL, planOptions.exactArgs...).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	var nodes []map[string]any
	if err := json.Unmarshal(plan, &nodes); err != nil {
		t.Fatal(err)
	}
	indexed := false
	var walk func(map[string]any)
	walk = func(node map[string]any) {
		if node["Index Name"] == "idx_episode_catalog_entries_exact_search_page" {
			indexed = true
		}
		if node["Node Type"] == "Sort" {
			if children, ok := node["Plans"].([]any); ok {
				for _, child := range children {
					if rows, ok := child.(map[string]any)["Actual Rows"].(float64); ok && rows > 21 {
						t.Errorf("exact tier sorted%.0f input rows rather than bounded21", rows)
					}
				}
			}
		}
		if children, ok := node["Plans"].([]any); ok {
			for _, child := range children {
				walk(child.(map[string]any))
			}
		}
	}
	walk(nodes[0]["Plan"].(map[string]any))
	if !indexed {
		t.Fatalf("exact tier failed ordered index budget: %s", plan)
	}
	// A broad fallback must join the parent identity set once, including when
	// work_mem is too small to keep it in memory. Never probe a parent PK for
	// every matching episode before the bounded hydration step.
	broadTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer broadTx.Rollback(context.Background()) //nolint:errcheck
	if _, err := broadTx.Exec(ctx, "SET LOCAL work_mem='64kB'"); err != nil {
		t.Fatal(err)
	}
	// Prefer the hash alternative in this small fixture to exercise its real
	// disk spill; normal million-row plans are measured separately.
	if _, err := broadTx.Exec(ctx, "SET LOCAL enable_nestloop=off"); err != nil {
		t.Fatal(err)
	}
	broadSQL, _, broadArgs := repo.buildMixedSearchCursorSQL(parseSearchQuery("Parentspill"), []string{"episode"}, 21, 0, filter, false, &searchCursorSQL{})
	// Inspect the unchanged scored relation and page limit independently of
	// hydration; disabling nested loops is only for the parent join spill.
	broadSQL = broadSQL[:strings.Index(broadSQL, ", page AS (")] +
		fmt.Sprintf(" SELECT content_id FROM scored WHERE $1::text IS NOT NULL ORDER BY %s LIMIT $%d", mixedSearchOrder(""), len(broadArgs))
	if err := broadTx.QueryRow(ctx, "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON,TIMING OFF) "+broadSQL, broadArgs...).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(plan, &nodes); err != nil {
		t.Fatal(err)
	}
	parentSet := false
	hashJoin := false
	spilled := false
	var broadWalk func(map[string]any)
	broadWalk = func(node map[string]any) {
		loops, _ := node["Actual Loops"].(float64)
		alias, _ := node["Alias"].(string)
		if node["Relation Name"] == "media_items" && strings.HasPrefix(alias, "si") && loops > 0 && node["Actual Rows"].(float64) > 21 {
			parentSet = true
			if loops != 1 {
				t.Errorf("broad search reread parent set%.0f times", loops)
			}
		}
		if node["Node Type"] == "Hash Join" && loops > 0 {
			hashJoin = true
		}
		if batches, ok := node["Hash Batches"].(float64); ok && batches > 1 {
			spilled = true
		}
		if node["Relation Name"] == "media_items" && node["Node Type"] != "Seq Scan" && loops > 21 {
			t.Errorf("broad search probed parent PK%.0f times before hydration", loops)
		}
		if children, ok := node["Plans"].([]any); ok {
			for _, child := range children {
				broadWalk(child.(map[string]any))
			}
		}
	}
	broadWalk(nodes[0]["Plan"].(map[string]any))
	if !parentSet || !hashJoin || !spilled {
		t.Fatalf("broad search lost spillable parent join: parent set=%v hash join=%v spill=%v", parentSet, hashJoin, spilled)
	}
	if err := broadTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	// Existing membership columns are integer even though folder IDs are
	// bigint. The new scalar probe must not introduce its own narrower cast.
	// Exercise that scalar independently of the existing integer[] policy
	// parameters: a valid bigint with no representable membership is empty.
	highIDOptions := &searchCursorSQL{countArgs: []any{"Star", "star:*", "star", nil, ""}}
	highIDSQL, highIDArgs := buildEpisodeExactTierSQL(parsed, AccessFilter{AllowedLibraryIDs: []int{3_000_000_000}}, highIDOptions,
		&episodeSearchSource{}, searchTitleLookup{exactIdx: 3, titleLookupIdx: 3}, 4, 5, 21)
	highIDRows, err := pool.Query(ctx, highIDSQL, highIDArgs...)
	if err != nil {
		t.Fatal(err)
	}
	defer highIDRows.Close()
	if highIDRows.Next() || highIDRows.Err() != nil {
		t.Fatalf("bigint probe should be empty: %v", highIDRows.Err())
	}
}
