package literaryworks

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These temporary tables shadow catalog tables on a single test connection.
// They let the same fixture exercise both the old predicate and the new lookup
// without touching existing catalog rows in the configured test database.
func candidateTestPool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	cfg.MinConns = 0
	cfg.MaxConnLifetime = time.Hour
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "30000"
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	_, err = pool.Exec(context.Background(), `
 CREATE TEMP TABLE media_items (content_id text PRIMARY KEY, type text NOT NULL, title text NOT NULL);
 CREATE INDEX ON media_items (content_id, type);
 CREATE INDEX ON media_items (type);
 CREATE INDEX ON media_items (lower(title)) WHERE type IN ('ebook','audiobook');
 CREATE TEMP TABLE media_item_provider_ids (content_id text, provider text, provider_id text, item_type text, PRIMARY KEY(content_id,provider), UNIQUE(provider,provider_id,item_type));
 CREATE TEMP TABLE ebook_series (content_id text PRIMARY KEY, series_name text, series_index numeric);
 CREATE INDEX ON ebook_series (lower(series_name),series_index);
 CREATE TEMP TABLE audiobook_series (LIKE ebook_series INCLUDING ALL);
 CREATE TEMP TABLE literary_work_match_decisions (source_content_id text, target_content_id text, decision text, PRIMARY KEY(source_content_id,target_content_id));
 CREATE INDEX ON literary_work_match_decisions(target_content_id,decision);
 `)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}

// legacyCandidateQuery is the pre-rewrite predicate, retained as a result oracle
// and performance baseline. It deliberately uses correlated OR predicates.
func legacyCandidateQuery(source MatchItem, limit int) (string, []any) {
	args := []any{source.ContentID, source.Type}
	var criteria []string
	if strings.TrimSpace(source.Title) != "" {
		args = append(args, source.Title)
		criteria = append(criteria, fmt.Sprintf("lower(mi.title)=lower($%d)", len(args)))
	}
	for provider, id := range source.ExternalIDs {
		if provider == "" || provider == "asin" || id == "" {
			continue
		}
		args = append(args, provider, id)
		criteria = append(criteria, fmt.Sprintf("EXISTS (SELECT 1 FROM media_item_provider_ids p WHERE p.content_id=mi.content_id AND p.provider=$%d AND p.provider_id=$%d)", len(args)-1, len(args)))
	}
	if strings.TrimSpace(source.SeriesName) != "" && source.SeriesIndex != nil {
		table := "ebook_series"
		if source.Type == FormatEbook {
			table = "audiobook_series"
		}
		args = append(args, source.SeriesName, *source.SeriesIndex)
		criteria = append(criteria, fmt.Sprintf("EXISTS (SELECT 1 FROM %s s WHERE s.content_id=mi.content_id AND lower(s.series_name)=lower($%d) AND s.series_index=$%d)", table, len(args)-1, len(args)))
	}
	where := ""
	if len(criteria) > 0 {
		where = " AND (" + strings.Join(criteria, " OR ") + ")"
	}
	args = append(args, limit)
	return `SELECT mi.content_id FROM media_items mi WHERE mi.content_id<>$1 AND mi.type IN ('ebook','audiobook') AND mi.type<>$2 AND NOT EXISTS (SELECT 1 FROM literary_work_match_decisions d WHERE d.decision='ignored' AND ((d.source_content_id=$1 AND d.target_content_id=mi.content_id) OR (d.source_content_id=mi.content_id AND d.target_content_id=$1)))` + where + ` ORDER BY mi.title,mi.content_id LIMIT $` + fmt.Sprint(len(args)), args
}

func candidateIDs(t testing.TB, pool *pgxpool.Pool, query string, args []any) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), query, args...)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

func TestCandidateLookupMatchesLegacy(t *testing.T) {
	pool := candidateTestPool(t)
	ctx := context.Background()
	_, err := pool.Exec(ctx, `
 INSERT INTO media_items VALUES
 ('source','ebook','Common'),('same-format','ebook','Common'),
 ('title-a','audiobook','Common'),('title-b','audiobook','Common'),
 ('provider','audiobook','A provider'),('series','audiobook','B series'),
 ('ignored-forward','audiobook','Common'),('ignored-reverse','audiobook','Common'),
 ('nonbook','movie','Common'),('asin-only','audiobook','ASIN match'),
 ('ebook-provider','ebook','A ebook provider'),('ebook-series','ebook','B ebook series');
 INSERT INTO media_item_provider_ids VALUES
 ('provider','isbn','123','audiobook'),('ebook-provider','isbn','123','ebook'),
 ('title-a','work','456','audiobook'),('asin-only','asin','789','audiobook'),
 ('nonbook','isbn','123','movie');
 INSERT INTO audiobook_series VALUES ('series','Sequence',1),('title-a','Sequence',1),('nonbook','Sequence',1);
 INSERT INTO ebook_series VALUES ('ebook-series','Sequence',1);
 INSERT INTO literary_work_match_decisions VALUES ('source','ignored-forward','ignored'),('ignored-reverse','source','ignored'),('source','title-b','confirmed');
 `)
	if err != nil {
		t.Fatal(err)
	}
	index := 1.0
	cases := []struct {
		name   string
		source MatchItem
		limit  int
		want   []string
	}{
		{"title", MatchItem{Title: "cOmMoN"}, 100, []string{"title-a", "title-b"}},
		{"provider", MatchItem{ExternalIDs: map[string]string{"isbn": "123"}}, 100, []string{"provider"}},
		{"series", MatchItem{SeriesName: "sEqUeNcE", SeriesIndex: &index}, 100, []string{"series", "title-a"}},
		{"combined deduplicated", MatchItem{Title: "Common", ExternalIDs: map[string]string{"isbn": "123", "work": "456", "asin": "789"}, SeriesName: "Sequence", SeriesIndex: &index}, 100, []string{"provider", "series", "title-a", "title-b"}},
		{"global limit", MatchItem{Title: "Common", ExternalIDs: map[string]string{"isbn": "123"}}, 2, []string{"provider", "title-a"}},
		{"no match", MatchItem{Title: "Absent", ExternalIDs: map[string]string{"isbn": "missing"}}, 100, nil},
		{"asin excluded", MatchItem{Title: "Absent", ExternalIDs: map[string]string{"asin": "789"}}, 100, nil},
		{"empty criteria", MatchItem{}, 2, []string{"provider", "asin-only"}},
		{"unusable criteria fallback", MatchItem{Title: "  ", ExternalIDs: map[string]string{"": "123", "isbn": "", "asin": "789"}, SeriesName: "Sequence"}, 2, []string{"provider", "asin-only"}},
		{"reverse format", MatchItem{Type: FormatAudiobook, ExternalIDs: map[string]string{"isbn": "123"}, SeriesName: "Sequence", SeriesIndex: &index}, 100, []string{"ebook-provider", "ebook-series"}},
		{"source excluded", MatchItem{ContentID: "title-a", Title: "Common"}, 100, []string{"ignored-forward", "ignored-reverse", "title-b"}},
	}
	for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
		if _, err := pool.Exec(ctx, "SET plan_cache_mode = "+mode); err != nil {
			t.Fatal(err)
		}
		for _, tc := range cases {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				source := tc.source
				if source.Type == "" {
					source.Type = FormatEbook
				}
				if source.ContentID == "" {
					source.ContentID = "source"
				}
				query, args := matchCandidateIDsQuery(source, tc.limit)
				got := candidateIDs(t, pool, query, args)
				oldQuery, oldArgs := legacyCandidateQuery(source, tc.limit)
				old := candidateIDs(t, pool, oldQuery, oldArgs)
				if !reflect.DeepEqual(got, old) {
					t.Fatalf("new=%v legacy=%v", got, old)
				}
				if len(got) != len(tc.want) || (len(got) > 0 && !reflect.DeepEqual(got, tc.want)) {
					t.Fatalf("got=%v want=%v", got, tc.want)
				}
			})
		}
	}
}

func TestCandidateParametersStable(t *testing.T) {
	source := MatchItem{ContentID: "source", Type: FormatEbook, ExternalIDs: map[string]string{"z": "last", "a": "first", "asin": "skip"}}
	query, args := matchCandidateIDsQuery(source, 100)
	for i := 0; i < 50; i++ {
		next, nextArgs := matchCandidateIDsQuery(source, 100)
		if query != next || !reflect.DeepEqual(args, nextArgs) {
			t.Fatal("query parameters depend on map iteration order")
		}
	}
	if !reflect.DeepEqual(args, []any{"source", FormatEbook, "a", "first", "z", "last", 100}) {
		t.Fatalf("args=%v", args)
	}
}
