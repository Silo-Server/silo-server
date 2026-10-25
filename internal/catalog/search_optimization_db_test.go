package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
)

func searchOptimizationPool(t *testing.T, tracer pgx.QueryTracer) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestSearchUsesCustomPlansPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	id := fmt.Sprintf("custom-search-plan-%d", time.Now().UnixNano())
	if _, err := pool.Exec(t.Context(), `INSERT INTO media_items(content_id,type,title) VALUES($1,'movie','Planner Fixture')`, id); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id=$1`, id) }()
	if _, err := pool.Exec(t.Context(), "SET plan_cache_mode=force_generic_plan"); err != nil {
		t.Fatal(err)
	}
	repo := NewItemRepository(pool)
	for range 7 {
		page, err := repo.SearchCursorPage(t.Context(), "Planner Fixture", []string{"movie"}, 20, nil, AccessFilter{}, true)
		if err != nil || !slices.Equal(contentIDsFromMediaItems(page.Items), []string{id}) || page.Total != 1 {
			t.Fatalf("cursor search under forced generic mode: page=%+v err=%v", page, err)
		}
		items, total, _, _, err := repo.SearchPage(t.Context(), "Planner Fixture", []string{"movie"}, 20, 0, AccessFilter{}, true)
		if err != nil || !slices.Equal(contentIDsFromMediaItems(items), []string{id}) || total != 1 {
			t.Fatalf("legacy search under forced generic mode: IDs=%v total=%d err=%v", contentIDsFromMediaItems(items), total, err)
		}
	}
	var namedSearchPlans int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_prepared_statements
		WHERE (statement LIKE '%title_scored AS MATERIALIZED%' OR statement LIKE '%title_candidates AS MATERIALIZED%')
		AND statement NOT LIKE '%FROM pg_prepared_statements%'`).Scan(&namedSearchPlans); err != nil {
		t.Fatal(err)
	}
	if namedSearchPlans != 0 {
		t.Fatalf("ranked search retained %d named statements eligible for generic plans", namedSearchPlans)
	}
}

func TestMediaSearchDocumentsPostgres(t *testing.T) {
	pool := searchOptimizationPool(t, nil)
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck
	id := fmt.Sprintf("search-document-%d", time.Now().UnixNano())
	if _, err := tx.Exec(t.Context(), `INSERT INTO media_items(content_id,type,title,original_title,sort_title,overview)
		VALUES($1,'movie','Café & Dune: Part Two','Dune Part 2','Dune II','A buried signal returns.')`, id); err != nil {
		t.Fatal(err)
	}
	check := func() {
		t.Helper()
		var equal bool
		if err := tx.QueryRow(t.Context(), `SELECT
			original_title_normalized = public.normalize_search_text(original_title)
			AND sort_title_normalized = public.normalize_search_text(sort_title)
			AND search_title_vector = (
				setweight(to_tsvector('simple', public.normalize_search_text(title)), 'A') ||
				setweight(to_tsvector('simple', public.normalize_search_text(original_title)), 'A') ||
				setweight(to_tsvector('simple', public.normalize_search_text(sort_title)), 'B'))
			AND search_overview_vector = to_tsvector('english', COALESCE(overview,''))
			FROM media_items WHERE content_id=$1`, id).Scan(&equal); err != nil || !equal {
			t.Fatalf("stored documents differ from original expressions: equal=%v error=%v", equal, err)
		}
	}
	check()
	for _, update := range []string{
		`title='Law and Order: Third'`,
		`original_title=''`,
		`sort_title='Étoile & lumière'`,
		`overview=''`,
		`title='東京 2',original_title='Tokyo Two',sort_title='',overview='A quartz beacon glows.'`,
	} {
		if _, err := tx.Exec(t.Context(), "UPDATE media_items SET "+update+" WHERE content_id=$1", id); err != nil {
			t.Fatal(err)
		}
		check()
	}
	// Deliberately change the derived document inside this rolled-back fixture
	// to detect recomputation on unchanged inputs or an unrelated metadata write.
	if _, err := tx.Exec(t.Context(), `UPDATE media_items SET search_title_vector='sentinel'::tsvector WHERE content_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `UPDATE media_items SET title=title,original_title=original_title,
		sort_title=sort_title,overview=overview,year=2026 WHERE content_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	var document string
	if err := tx.QueryRow(t.Context(), `SELECT search_title_vector::text FROM media_items WHERE content_id=$1`, id).Scan(&document); err != nil || document != "'sentinel'" {
		t.Fatalf("unchanged inputs rebuilt document: %q error=%v", document, err)
	}
	if _, err := tx.Exec(t.Context(), "SET LOCAL enable_seqscan=off"); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"title", "overview"} {
		var plan json.RawMessage
		if err := tx.QueryRow(t.Context(), "EXPLAIN (FORMAT JSON) SELECT content_id FROM media_items WHERE search_"+field+"_vector @@ plainto_tsquery('simple','quartz')").Scan(&plan); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(plan), "idx_media_items_stored_search_"+field) {
			t.Fatalf("%s search does not use the stored-vector GIN index: %s", field, plan)
		}
	}
}

const originalMediaSearchTitleVector = `(
	setweight(to_tsvector('simple', public.normalize_search_text(COALESCE(mi.title, ''))), 'A') ||
	setweight(to_tsvector('simple', public.normalize_search_text(COALESCE(mi.original_title, ''))), 'A') ||
	setweight(to_tsvector('simple', public.normalize_search_text(COALESCE(mi.sort_title, ''))), 'B')
)`

func TestSearchOptimizedAccuracyPostgres(t *testing.T) {
	pool := searchOptimizationPool(t, nil)
	ctx := t.Context()
	prefix := fmt.Sprintf("search-accuracy-%d", time.Now().UnixNano())
	libraries := make([]int, 2)
	for i := range libraries {
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders(type,name,enabled) VALUES('series',$1,true) RETURNING id`, fmt.Sprintf("%s-%d", prefix, i)).Scan(&libraries[i]); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=ANY($1)`, libraries)
	})
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	for i, title := range []string{"Quasar Adventure", "Quasar Adventure", "Quasar", "Quasar Adventurer", "Hidden Movie", "Other Movie", "Café Two", "Dune: Part Two", "IT", "Pride and Prejudice", "Unrelated Movie", "Low Overview"} {
		id := fmt.Sprintf("%s-movie-%02d", prefix, i)
		original, sortTitle, overview := "", "", ""
		switch i {
		case 4:
			original = "Quasar Adventure"
		case 5:
			sortTitle = "Quasar Adventure"
		case 10:
			overview = strings.Repeat("A buried signal returns. ", 4)
		case 11:
			overview = "Buried " + strings.Repeat("ordinary ", 200) + "signal"
		}
		exec(`INSERT INTO media_items(content_id,type,title,original_title,sort_title,overview,year,content_rating,advisory_age)
			VALUES($1,'movie',$2,$3,$4,$5,$6,$7,$8)`, id, title, original, sortTitle, overview, 2024+i%2, map[bool]string{true: "R", false: "PG"}[i == 1], map[bool]int{true: 18, false: 10}[i == 1])
		exec(`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, id, libraries[0])
		if i == 2 {
			exec(`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, id, libraries[1])
		}
		if i == 10 {
			exec(`INSERT INTO media_item_aliases(content_id,title,kind,provider) VALUES($1,'Quasar Adventure','alternate','fixture')`, id)
		}
	}
	seriesID := prefix + "-series"
	exec(`INSERT INTO media_items(content_id,type,title,content_rating) VALUES($1,'series','Fixture Parent','PG')`, seriesID)
	for _, library := range libraries {
		exec(`INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, seriesID, library)
	}
	for i, title := range []string{"Quasar Adventure", "Quasar", "Other Episode", "Low Overview"} {
		id := fmt.Sprintf("%s-episode-%d", prefix, i)
		overview := strings.Repeat("A buried signal returns. ", 4)
		if i == 3 {
			overview = "Buried " + strings.Repeat("ordinary ", 200) + "signal"
		}
		exec(`INSERT INTO episodes(content_id,series_id,season_number,episode_number,title,overview,air_date)
			VALUES($1,$2,1,$3,$4,$5,'2024-01-01')`, id, seriesID, i+1, title, overview)
		for _, library := range libraries {
			exec(`INSERT INTO episode_libraries(episode_id,media_folder_id) VALUES($1,$2)`, id, library)
		}
	}
	repo := NewItemRepository(pool)
	originalExpressions := strings.NewReplacer(
		"title_mi.search_title_vector", strings.ReplaceAll(originalMediaSearchTitleVector, "mi.", "title_mi."),
		mediaSearchTitleVector, originalMediaSearchTitleVector,
		mediaSearchOverviewVector, `to_tsvector('english', COALESCE(mi.overview, ''))`,
		`mi.original_title_normalized`, `public.normalize_search_text(mi.original_title)`,
		`mi.sort_title_normalized`, `public.normalize_search_text(mi.sort_title)`,
	)
	filters := []struct {
		name   string
		filter AccessFilter
	}{
		{"one library", AccessFilter{AllowedLibraryIDs: libraries[:1]}},
		{"two libraries", AccessFilter{AllowedLibraryIDs: libraries}},
		{"disabled membership", AccessFilter{AllowedLibraryIDs: libraries[:1], DisabledLibraryIDs: libraries[1:]}},
		{"maturity", AccessFilter{AllowedLibraryIDs: libraries, MaturityLimits: access.MaturityLimits{MaxContentRating: "PG-13", AllowUnratedContent: true, MaxAdvisoryAge: 13}}},
		{"excluded episodes", AccessFilter{AllowedLibraryIDs: libraries, ExcludedMediaTypes: []string{"episode"}}},
	}
	for _, query := range []string{"Quasar", `"Quasar Adventure"`, "Quasar Adventure 2024", "Quasar and Adventure", "Café two", "dune part two", "IT", "Pride and P", "buried signal", "absent unique signal"} {
		for _, scope := range filters {
			t.Run(query+"/"+scope.name, func(t *testing.T) {
				parsed := parseSearchQuery(query)
				options := &searchCursorSQL{}
				dataSQL, countSQL, args := repo.buildMixedSearchCursorSQL(parsed, nil, 200, 0, scope.filter, false, options)
				exactIdx := len(options.countArgs) - 2
				titleLookupIdx := exactIdx
				if useLeadingShortTitleSearch(parsed) {
					exactIdx--
					titleLookupIdx = exactIdx + 1
				}
				aliasArm := `mi.content_id = ANY(COALESCE((SELECT array_agg(alias_scores.content_id) FROM alias_scores), '{}'::text[]))`
				oldMatch := searchTitleMatchCondition(mediaSearchTitleVector, aliasArm)
				if useExactShortTitleSearch(parsed) {
					oldMatch = fmt.Sprintf("(mi.title_normalized = $%d OR %s)", exactIdx, aliasArm)
				} else if useLeadingShortTitleSearch(parsed) {
					oldMatch = fmt.Sprintf("(mi.title_normalized LIKE $%d || '%%%%' OR %s)", titleLookupIdx, aliasArm)
				}
				candidateUnion := regexp.MustCompile(`mi.content_id IN \(SELECT title_mi.content_id FROM media_items title_mi WHERE (?s:.*?) UNION SELECT alias_scores.content_id FROM alias_scores\)`)
				baselineSQL := originalExpressions.Replace(candidateUnion.ReplaceAllStringFunc(dataSQL, func(string) string { return oldMatch }))
				rows, err := pool.Query(ctx, baselineSQL, args...) //nolint:rowserrcheck // scanItems checks Err through the cursorRows wrapper
				if err != nil {
					t.Fatal(err)
				}
				wrapped := &cursorRows{Rows: rows, terms: searchFTSTerms()}
				baseline, err := scanItems(wrapped)
				rows.Close()
				if err != nil {
					t.Fatal(err)
				}
				baselineKeys := wrapped.keys
				var total, scoredTotal int
				if err := pool.QueryRow(ctx, countSQL, options.countArgs...).Scan(&total); err != nil {
					t.Fatal(err)
				}
				baselineScored, _, ok := strings.Cut(baselineSQL, ", page AS (")
				if !ok {
					t.Fatalf("baseline search SQL has no page CTE:\n%s", baselineSQL)
				}
				scoredCount := baselineScored + " SELECT COUNT(*) FROM scored WHERE $1::text IS NOT NULL"
				if err := pool.QueryRow(ctx, scoredCount, options.countArgs...).Scan(&scoredTotal); err != nil {
					t.Fatal(err)
				}
				if total != scoredTotal || total != len(baseline) {
					t.Fatalf("unranked total=%d scored total=%d baseline rows=%d", total, scoredTotal, len(baseline))
				}
				var after *SearchCursor
				var gotIDs []string
				var gotKeys []QueryCursor
				for range 20 {
					items, keys, gotTotal, err := repo.searchFTSCursorRows(ctx, parsed, nil, 2, after, scope.filter, true, false, 0)
					if err != nil {
						t.Fatal(err)
					}
					if gotTotal != total {
						t.Fatalf("continuation changed total: got %d want %d", gotTotal, total)
					}
					for _, item := range items {
						gotIDs = append(gotIDs, item.ContentID)
					}
					gotKeys = append(gotKeys, keys...)
					if len(items) < 2 {
						break
					}
					after = &SearchCursor{Mode: searchCursorFTS, Keys: keys[len(keys)-1].Keys}
				}
				if !slices.Equal(gotIDs, contentIDsFromMediaItems(baseline)) || len(gotKeys) != len(baselineKeys) || len(gotKeys) > 0 && !reflect.DeepEqual(gotKeys, baselineKeys) {
					t.Fatalf("stored search/cursor differs from expression baseline: IDs=%v baseline=%v keys=%v baseline keys=%v", gotIDs, contentIDsFromMediaItems(baseline), gotKeys, baselineKeys)
				}
				if query == "buried signal" && len(baseline) != 4 && scope.name == "one library" {
					t.Fatalf("overview floor admits weak separated words or suppresses strong matches: %v", gotIDs)
				}
			})
		}
	}
}

type sparseFTSTracer struct{ queries atomic.Int64 }

func (tracer *sparseFTSTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, ", page AS (") && strings.Contains(data.SQL, "title_prefix_rank") && strings.Contains(data.SQL, "mi.search_title_vector") {
		tracer.queries.Add(1)
	}
	return ctx
}

func (*sparseFTSTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestSearchSparseProbeReusePostgres(t *testing.T) {
	tracer := &sparseFTSTracer{}
	pool := searchOptimizationPool(t, tracer)
	prefix := fmt.Sprintf("sparse-probe-%d", time.Now().UnixNano())
	var library int
	if err := pool.QueryRow(t.Context(), `INSERT INTO media_folders(type,name,enabled) VALUES('movies',$1,true) RETURNING id`, prefix).Scan(&library); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=$1`, library)
	})
	for i := range 3 {
		id := fmt.Sprintf("%s-%d", prefix, i)
		if _, err := pool.Exec(t.Context(), `INSERT INTO media_items(content_id,type,title) VALUES($1,'movie','Quasar Adventure')`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(t.Context(), `INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)`, id, library); err != nil {
			t.Fatal(err)
		}
	}
	repo := NewItemRepository(pool)
	filter := AccessFilter{AllowedLibraryIDs: []int{library}}
	for _, query := range []string{"Quasar Adventure", "Quasar Adventur", "Quasar Advneture"} {
		for _, grouped := range []bool{false, true} {
			for _, totals := range []bool{false, true} {
				tracer.queries.Store(0)
				page, err := repo.SearchCursorPage(t.Context(), query, []string{"movie"}, 2, nil, filter, totals, SearchCursorOptions{GroupByWork: grouped})
				if err != nil {
					t.Fatal(err)
				}
				if tracer.queries.Load() != 1 {
					t.Fatalf("query=%q grouped=%v totals=%v issued %d FTS queries for one sparse family", query, grouped, totals, tracer.queries.Load())
				}
				if !page.HasMore || len(page.Items) != 2 || page.Next == nil || page.Scope.Mode != searchCursorCombined || totals && page.Total != 3 {
					t.Fatalf("sparse reuse changed page: %+v", page)
				}
				next, err := repo.SearchCursorPage(t.Context(), query, []string{"movie"}, 2, page.Next, filter, totals, SearchCursorOptions{GroupByWork: grouped})
				if err != nil || len(next.Items) != 1 || next.HasMore || next.Items[0].ContentID == page.Items[0].ContentID || totals && next.Total != 3 {
					t.Fatalf("sparse continuation lost original FTS keys: %+v error=%v", next, err)
				}
			}
		}
	}
}

// TestFoldSearchDiacriticsMigrationPostgres runs the diacritic-folding
// migration over rows written by the previous normalization, in a scratch
// schema, and checks that every stored search field is refreshed and that
// aliases colliding on the unique index collapse to one row.
func TestFoldSearchDiacriticsMigrationPostgres(t *testing.T) {
	pool := searchOptimizationPool(t, nil)
	ctx := t.Context()
	q := pgx.Identifier{fmt.Sprintf("fold_migration_%d", time.Now().UnixNano())}.Sanitize()
	local := func(sql string) string { return strings.ReplaceAll(sql, "public.", q+".") }
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%v\n%s", err, sql)
		}
	}
	migration, err := os.ReadFile("../../migrations/sql/20261023173138_fold_search_diacritics.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, down, _ := strings.Cut(string(migration), "-- +goose Down")
	exec("CREATE SCHEMA " + q)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DROP SCHEMA "+q+" CASCADE") })
	for _, function := range []string{"normalize_search_number_token", "set_media_item_search_fields", "set_episode_catalog_entry_search_fields"} {
		var definition string
		if err := pool.QueryRow(ctx, "SELECT pg_get_functiondef(('public.'||$1)::regproc)", function).Scan(&definition); err != nil {
			t.Fatal(err)
		}
		exec(local(definition))
	}
	// The Down section's first statement restores the pre-fold normalization.
	exec(local(migrationStatements(down)[0]))
	exec(local(`CREATE TABLE public.episodes (content_id text PRIMARY KEY, overview text);
		CREATE TABLE public.media_items (
			content_id text PRIMARY KEY, title text NOT NULL, original_title text, sort_title text, overview text,
			title_normalized text GENERATED ALWAYS AS (public.normalize_search_text(title)) STORED,
			original_title_normalized text, sort_title_normalized text,
			search_title_vector tsvector, search_overview_vector tsvector);
		CREATE TRIGGER search_fields BEFORE INSERT OR UPDATE OF content_id, title, original_title, sort_title, overview
			ON public.media_items FOR EACH ROW EXECUTE FUNCTION public.set_media_item_search_fields();
		CREATE TABLE public.media_item_aliases (
			id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, content_id text NOT NULL, title text NOT NULL,
			normalized_title text GENERATED ALWAYS AS (public.normalize_search_text(title)) STORED,
			language text NOT NULL DEFAULT '', kind text NOT NULL, provider text NOT NULL, snapshot_language text);
		CREATE UNIQUE INDEX ON public.media_item_aliases (content_id, normalized_title, language, kind, provider, snapshot_language);
		CREATE TABLE public.episode_catalog_entries (
			media_folder_id integer, episode_id text, title text NOT NULL,
			search_title_normalized text, search_title_vector tsvector, search_overview_vector tsvector,
			PRIMARY KEY (media_folder_id, episode_id));
		CREATE TRIGGER search_fields BEFORE INSERT OR UPDATE OF episode_id, title
			ON public.episode_catalog_entries FOR EACH ROW EXECUTE FUNCTION public.set_episode_catalog_entry_search_fields();
		INSERT INTO public.media_items (content_id, title, original_title, sort_title) VALUES
			('shogun', 'Shōgun', 'Shōgun', 'Shogun'), ('heat', 'Heat', NULL, NULL);
		INSERT INTO public.episode_catalog_entries (media_folder_id, episode_id, title) VALUES (1, 'episode', 'Pokémon');`))
	// Each content ID is one collision group, in insertion (id) order.
	for _, alias := range [][2]string{
		{"ascii-kept", "Café"}, {"ascii-kept", "Cafe"},
		// The trailing combining mark already normalized to "cafe".
		{"current-kept", "Café"}, {"current-kept", "Café"},
		{"oldest-kept", "Ōtsu"}, {"oldest-kept", "Ŏtsu"},
	} {
		exec(local(`INSERT INTO public.media_item_aliases (content_id, title, kind, provider, snapshot_language)
			VALUES ($1, $2, 'alternate', 'tmdb', 'en')`), alias[0], alias[1])
	}

	for _, statement := range migrationStatements(up) {
		exec(local(statement))
	}

	var item, episode string
	if err := pool.QueryRow(ctx, local(`SELECT concat_ws('|', title_normalized, original_title_normalized, sort_title_normalized, search_title_vector::text)
		FROM public.media_items WHERE content_id = 'shogun'`)).Scan(&item); err != nil {
		t.Fatal(err)
	}
	if want := "shogun|shogun|shogun|'shogun':1A,2A,3B"; item != want {
		t.Errorf("refreshed item = %q, want %q", item, want)
	}
	if err := pool.QueryRow(ctx, local(`SELECT concat_ws('|', search_title_normalized, search_title_vector::text)
		FROM public.episode_catalog_entries`)).Scan(&episode); err != nil {
		t.Fatal(err)
	}
	if want := "pokemon|'pokemon':1A"; episode != want {
		t.Errorf("refreshed episode = %q, want %q", episode, want)
	}
	rows, err := pool.Query(ctx, local(`SELECT content_id || ':' || title || ':' || normalized_title FROM public.media_item_aliases ORDER BY id`))
	if err != nil {
		t.Fatal(err)
	}
	aliases, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"ascii-kept:Cafe:cafe", "current-kept:Café:cafe", "oldest-kept:Ōtsu:otsu"}; !slices.Equal(aliases, want) {
		t.Errorf("aliases = %q, want %q", aliases, want)
	}

	checkFoldMigrationRollback(t, pool, local, exec, down)
}

// checkFoldMigrationRollback runs the Down section over a table the Up section
// refreshed. Rolling back collides too: the restored normalization turns a
// decomposed accent into a word break, so "Poke\u0301mon" and "Poke Mon" both
// become "poke mon" again, and the alias already current under it is kept.
func checkFoldMigrationRollback(t *testing.T, pool *pgxpool.Pool, local func(string) string, exec func(string, ...any), down string) {
	t.Helper()
	for _, title := range []string{"Poke\u0301mon", "Poke Mon"} {
		exec(local(`INSERT INTO public.media_item_aliases (content_id, title, kind, provider, snapshot_language)
			VALUES ('down-collides', $1, 'alternate', 'tmdb', 'en')`), title)
	}
	for _, statement := range migrationStatements(down) {
		exec(local(statement))
	}
	rows, err := pool.Query(t.Context(), local(`SELECT title || ':' || normalized_title FROM public.media_item_aliases
		WHERE content_id = 'down-collides' ORDER BY id`))
	if err != nil {
		t.Fatal(err)
	}
	rolledBack, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Poke Mon:poke mon"}; !slices.Equal(rolledBack, want) {
		t.Errorf("rolled-back aliases = %q, want %q", rolledBack, want)
	}
}

// migrationStatements splits a Goose SQL section into its statements: each
// StatementBegin/StatementEnd block, and each other statement ending a line
// with a semicolon.
func migrationStatements(section string) []string {
	var statements []string
	var current strings.Builder
	inBlock := false
	for line := range strings.Lines(section) {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "-- +goose StatementBegin":
			inBlock = true
		case trimmed == "-- +goose StatementEnd":
			inBlock = false
			statements = append(statements, current.String())
			current.Reset()
		case inBlock:
			current.WriteString(line)
		case trimmed == "" || strings.HasPrefix(trimmed, "--"):
		default:
			current.WriteString(line)
			if strings.HasSuffix(trimmed, ";") {
				statements = append(statements, current.String())
				current.Reset()
			}
		}
	}
	return statements
}
