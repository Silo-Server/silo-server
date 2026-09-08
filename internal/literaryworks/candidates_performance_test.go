package literaryworks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Run explicitly against a disposable database. Planner costs alone cannot
// establish that the rewrite avoids catalog-wide work with prepared statements.
func TestCandidateLookupLargeCatalog(t *testing.T) {
	if os.Getenv("SILO_TEST_LITERARY_PERF") != "1" {
		t.Skip("set SILO_TEST_LITERARY_PERF=1 for the large synthetic catalog comparison")
	}
	pool := candidateTestPool(t)
	ctx := context.Background()
	_, err := pool.Exec(ctx, `
 SET statement_timeout = '180s';
 INSERT INTO media_items
 SELECT 'book-'||g, CASE WHEN g%3=0 THEN 'movie' WHEN g%3=1 THEN 'ebook' ELSE 'audiobook' END,
 CASE WHEN g%1000=0 THEN 'Common title' ELSE 'Title '||g END
 FROM generate_series(1,620000) g;
 INSERT INTO media_item_provider_ids SELECT content_id,'isbn',content_id,type FROM media_items;
 INSERT INTO media_item_provider_ids SELECT content_id,'work',content_id,type FROM media_items LIMIT 247000;
 INSERT INTO ebook_series SELECT content_id,'Series '||content_id,1 FROM media_items WHERE type='ebook';
 INSERT INTO audiobook_series SELECT content_id,'Series '||content_id,1 FROM media_items WHERE type='audiobook';
 ANALYZE media_items; ANALYZE media_item_provider_ids; ANALYZE ebook_series; ANALYZE audiobook_series; ANALYZE literary_work_match_decisions;
 SET statement_timeout = '30s';
 `)
	if err != nil {
		t.Fatal(err)
	}
	index := 1.0
	for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
		if _, err := pool.Exec(ctx, "SET plan_cache_mode = "+mode); err != nil {
			t.Fatal(err)
		}
		for _, format := range []string{FormatEbook, FormatAudiobook} {
			target := "book-2"
			title := "Title 2"
			if format == FormatAudiobook {
				target = "book-1"
				title = "Title 1"
			}
			cases := []struct {
				name   string
				source MatchItem
			}{
				{"title", MatchItem{Title: title}},
				{"provider", MatchItem{ExternalIDs: map[string]string{"isbn": target}}},
				{"series", MatchItem{SeriesName: "Series " + target, SeriesIndex: &index}},
				{"combined", MatchItem{Title: title, ExternalIDs: map[string]string{"isbn": target, "work": target}, SeriesName: "Series " + target, SeriesIndex: &index}},
				{"common", MatchItem{Title: "Common title", ExternalIDs: map[string]string{"isbn": target}}},
				{"no_match", MatchItem{Title: "Missing", ExternalIDs: map[string]string{"isbn": "missing", "work": "missing"}, SeriesName: "Missing", SeriesIndex: &index}},
			}
			for _, tc := range cases {
				t.Run(mode+"/"+format+"/"+tc.name, func(t *testing.T) {
					source := tc.source
					source.Type = format
					source.ContentID = "source"
					oldQuery, oldArgs := legacyCandidateQuery(source, 100)
					newQuery, newArgs := matchCandidateIDsQuery(source, 100)
					old := explainCandidate(t, pool, oldQuery, oldArgs)
					next := explainCandidate(t, pool, newQuery, newArgs)
					t.Logf("legacy %.3f ms; indexed %.3f ms", old.ExecutionTime, next.ExecutionTime)
					checkCandidatePlan(t, next.Plan)
					if next.JIT != nil {
						t.Error("indexed candidate query unnecessarily triggers JIT")
					}
				})
			}
		}
	}
	source := MatchItem{ContentID: "source", Type: FormatEbook, Title: "Missing", ExternalIDs: map[string]string{"isbn": "missing"}}
	query, args := matchCandidateIDsQuery(source, 100)
	started := time.Now()
	for i := 0; i < 1000; i++ {
		candidateIDs(t, pool, query, args)
	}
	t.Logf("1000 repeated no-match lookups: %s", time.Since(started))
}

type candidatePlan struct {
	NodeType   string          `json:"Node Type"`
	Relation   string          `json:"Relation Name"`
	ActualRows float64         `json:"Actual Rows"`
	Loops      float64         `json:"Actual Loops"`
	Removed    float64         `json:"Rows Removed by Filter"`
	Plans      []candidatePlan `json:"Plans"`
}
type candidateExplain struct {
	Plan          candidatePlan   `json:"Plan"`
	ExecutionTime float64         `json:"Execution Time"`
	JIT           json.RawMessage `json:"JIT"`
}

func explainCandidate(t *testing.T, pool *pgxpool.Pool, query string, args []any) candidateExplain {
	t.Helper()
	ctx := context.Background()
	// EXPLAIN's own prepared statement does not exercise a cached SELECT plan.
	// Prepare the SELECT explicitly and EXPLAIN EXECUTE it with bound literals.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err = conn.Conn().Prepare(ctx, "candidate_perf", query); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := conn.Conn().Deallocate(ctx, "candidate_perf"); err != nil {
			t.Error(err)
		}
	}()
	literals := make([]string, len(args))
	for i, arg := range args {
		literals[i] = candidateSQLLiteral(arg)
	}
	var raw []byte
	if err := conn.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) EXECUTE candidate_perf("+strings.Join(literals, ",")+")").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var plans []candidateExplain
	if err := json.Unmarshal(raw, &plans); err != nil {
		t.Fatal(err)
	}
	return plans[0]
}

func checkCandidatePlan(t *testing.T, plan candidatePlan) {
	t.Helper()
	// Even an index scan can read the entire catalog and discard it via a filter.
	// Common-title fixtures have hundreds of hits; catalog scans have hundreds of thousands.
	if plan.Relation != "" && (plan.ActualRows+plan.Removed)*plan.Loops > 10000 {
		t.Errorf("lookup scanned too many rows: %+v", plan)
	}
	for _, child := range plan.Plans {
		checkCandidatePlan(t, child)
	}
}

func candidateSQLLiteral(value any) string {
	switch value := value.(type) {
	case string:
		return "'" + strings.ReplaceAll(value, "'", "''") + "'"
	default:
		return fmt.Sprint(value)
	}
}
