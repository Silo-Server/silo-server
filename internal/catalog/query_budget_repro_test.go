package catalog

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The original opt-in reproduction asserted unbounded construction and retry
// amplification. These regressions use only a task-owned schema with three
// synthetic rows, and can also run against CI's disposable test database.
func collectionBudgetTestPool(t *testing.T, tracer pgx.QueryTracer, customize func(*pgxpool.Config)) (*pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("SILO_CATALOG_BUDGET_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("SILO_TEST_DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("requires a disposable PostgreSQL test database")
	}
	admin, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("collection_count_budget_%d", time.Now().UnixNano())
	if _, err := admin.Exec(t.Context(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop synthetic schema: %v", err)
		}
	})
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 8
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.ConnConfig.RuntimeParams["application_name"] = schema
	config.ConnConfig.Tracer = tracer
	if customize != nil {
		customize(config)
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(t.Context(), `CREATE TABLE media_items(content_id text,type text,year int); CREATE TABLE manga_chapters(chapter_content_id text); INSERT INTO media_items VALUES ('synthetic-a','movie',2000),('synthetic-b','movie',2001),('synthetic-c','movie',2002)`); err != nil {
		t.Fatal(err)
	}
	return pool, admin
}

type collectionBudgetTrace struct {
	mu                  sync.Mutex
	calls, active, peak int
	codes               map[string]int
	onStart             func(context.Context)
	onEnd               func(*pgx.Conn)
}

func (q *collectionBudgetTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if !strings.HasPrefix(data.SQL, "SELECT $1::text") && !strings.HasPrefix(data.SQL, "/* collection-count-test */") {
		return ctx
	}
	q.mu.Lock()
	q.calls++
	q.active++
	q.peak = max(q.peak, q.active)
	q.mu.Unlock()
	if q.onStart != nil {
		q.onStart(ctx)
	}
	return context.WithValue(ctx, collectionBudgetTraceKey{}, true)
}

type collectionBudgetTraceKey struct{}

func (q *collectionBudgetTrace) TraceQueryEnd(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryEndData) {
	if ctx.Value(collectionBudgetTraceKey{}) != true {
		return
	}
	q.mu.Lock()
	q.active--
	if pgErr, ok := errors.AsType[*pgconn.PgError](data.Err); ok {
		if q.codes == nil {
			q.codes = make(map[string]int)
		}
		q.codes[pgErr.Code]++
	}
	q.mu.Unlock()
	if q.onEnd != nil {
		q.onEnd(conn)
	}
}
func (q *collectionBudgetTrace) result() (int, int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.calls, q.peak
}

func syntheticBudgetCollections(n int) []PersonalCollectionDefinition {
	collections := make([]PersonalCollectionDefinition, n)
	for i := range collections {
		collections[i] = PersonalCollectionDefinition{ID: fmt.Sprintf("synthetic-%d", i), CollectionType: "smart", QueryDefinition: fmt.Sprintf(`{"media_scope":"movie","groups":[{"rules":[{"field":"year","op":"gte","value":%d}]}]}`, 2000+i)}
	}
	return collections
}

func TestCollectionCountConstructionAndWorkersDB(t *testing.T) {
	trace := &collectionBudgetTrace{}
	var arrived atomic.Int32
	ready := make(chan struct{})
	trace.onStart = func(ctx context.Context) {
		if arrived.Add(1) == 2 {
			close(ready)
		}
		select {
		case <-ready:
		case <-ctx.Done():
		}
	}
	pool, _ := collectionBudgetTestPool(t, trace, nil)
	ctx, cancel := WithCollectionCountBudget(t.Context())
	defer cancel()
	collections := syntheticBudgetCollections(132)
	counts, err := CountPersonalCollections(ctx, pool, 1, collections, AccessFilter{})
	calls, peak := trace.result()
	if !errors.Is(err, ErrCollectionCountBudget) || len(counts) != 128 || counts["synthetic-0"] != 3 || counts["synthetic-1"] != 2 || counts["synthetic-2"] != 1 || calls != 4 || peak != 2 {
		t.Fatalf("bounded counts=%d calls=%d peak=%d error=%v", len(counts), calls, peak, err)
	}
	// Later owner calls reuse effective definitions and preserve exhaustion.
	alias := collections[0]
	alias.ID = "alias"
	counts, err = CountPersonalCollections(ctx, pool, 1, []PersonalCollectionDefinition{alias}, AccessFilter{})
	if err != nil || counts["alias"] != 3 {
		t.Fatalf("cross-call dedup: %v %v", counts, err)
	}
	before, _ := trace.result()
	_, err = CountPersonalCollections(ctx, pool, 2, []PersonalCollectionDefinition{alias}, AccessFilter{})
	after, _ := trace.result()
	if !errors.Is(err, ErrCollectionCountBudget) || after != before {
		t.Fatalf("next owner reset construction budget: calls=%d error=%v", after-before, err)
	}
	t.Logf("132 definitions: built=128 submitted=%d worker_peak=%d; next owner submissions=0", calls, peak)
}

func TestCollectionCountDuplicatesAndSingleConnectionDB(t *testing.T) {
	trace := &collectionBudgetTrace{}
	pool, _ := collectionBudgetTestPool(t, trace, func(config *pgxpool.Config) { config.MaxConns = 1 })
	collections := syntheticBudgetCollections(128)
	counts, err := CountPersonalCollections(t.Context(), pool, 1, collections, AccessFilter{})
	if err != nil || len(counts) != 128 {
		t.Fatalf("one-connection scheduling rejected itself: %d %v", len(counts), err)
	}
	calls, peak := trace.result()
	if calls != 4 || peak != 1 {
		t.Fatalf("one-connection scheduling: calls=%d peak=%d", calls, peak)
	}
	ctx, cancel := WithCollectionCountBudget(t.Context())
	defer cancel()
	duplicates := make([]PersonalCollectionDefinition, 200)
	for i := range duplicates {
		duplicates[i] = collections[0]
		duplicates[i].ID = fmt.Sprintf("copy-%d", i)
	}
	counts, err = CountPersonalCollections(ctx, pool, 1, duplicates, AccessFilter{})
	if err != nil || len(counts) != 200 {
		t.Fatalf("duplicates consumed definition quota: %d %v", len(counts), err)
	}
	bad := duplicates[0]
	bad.ID = "raw-excess"
	bad.QueryDefinition = `{"library_ids":[` + strings.Repeat("1,", 256) + `1]}`
	counts, err = CountPersonalCollections(ctx, pool, 1, []PersonalCollectionDefinition{bad, duplicates[0]}, AccessFilter{})
	if !errors.Is(err, ErrQueryComplexity) || len(counts) != 1 {
		t.Fatalf("raw excess dedup: %v %v", counts, err)
	}
}

func TestCollectionCountMaximumStructureDB(t *testing.T) {
	trace := &collectionBudgetTrace{}
	pool, _ := collectionBudgetTestPool(t, trace, nil)
	if _, err := pool.Exec(t.Context(), `ALTER TABLE media_items ADD COLUMN title text; UPDATE media_items SET title=repeat('x',510); CREATE TABLE media_item_libraries(content_id text,media_folder_id int); INSERT INTO media_item_libraries VALUES ('synthetic-a',1),('synthetic-b',2),('synthetic-c',256)`); err != nil {
		t.Fatal(err)
	}
	def := QueryDefinition{MediaScope: "movie", Groups: make([]QueryGroup, queryMaxGroups), LibraryIDs: make([]int, queryMaxLibraries)}
	for i := range def.LibraryIDs {
		def.LibraryIDs[i] = i + 1
	}
	for i := range def.Groups {
		def.Groups[i] = QueryGroup{Rules: make([]QueryRule, 8)}
		for j := range def.Groups[i].Rules {
			def.Groups[i].Rules[j] = QueryRule{Field: "title", Op: "contains", Value: strings.Repeat("x", 510)}
		}
	}
	c := PersonalCollectionDefinition{ID: "maximum", CollectionType: "smart", QueryDefinition: budgetDefinitionJSON(t, def)}
	counts, err := CountPersonalCollections(t.Context(), pool, 1, []PersonalCollectionDefinition{c}, AccessFilter{})
	if err != nil || counts[c.ID] != 3 {
		t.Fatalf("maximum accepted structure: %v %v", counts, err)
	}
	calls, _ := trace.result()
	def.Groups[0].Rules = append(def.Groups[0].Rules, QueryRule{Field: "year", Op: "gte", Value: 2000})
	c.QueryDefinition = budgetDefinitionJSON(t, def)
	counts, err = CountPersonalCollections(t.Context(), pool, 1, []PersonalCollectionDefinition{c}, AccessFilter{})
	after, _ := trace.result()
	if !errors.Is(err, ErrQueryComplexity) || len(counts) != 0 || after != calls {
		t.Fatalf("over-limit construction reached SQL: %v %v calls=%d", counts, err, after-calls)
	}
	t.Log("accepted together: 16 groups, 128 rules, 256 libraries, 65536 aggregate encoded value bytes; over-limit submissions=0")
}

func TestCollectionCountManualAndSmartShareLedgerDB(t *testing.T) {
	pool, _ := collectionBudgetTestPool(t, nil, nil)
	if _, err := pool.Exec(t.Context(), `CREATE TABLE user_personal_collection_items(user_id int,collection_id text,sub_item_id text,media_item_id text); INSERT INTO user_personal_collection_items VALUES (1,'manual','','synthetic-a'),(2,'manual','','synthetic-b')`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := WithCollectionCountBudget(t.Context())
	defer cancel()
	manual := PersonalCollectionDefinition{ID: "manual", CollectionType: "manual", DisplayQueryDefinition: `{"groups":[{"rules":[{"field":"type","op":"is","value":"movie"}]}]}`}
	collections := append(syntheticBudgetCollections(1), manual)
	viewer := AccessFilter{AllowedContentIDs: []string{"synthetic-a", "synthetic-c"}}
	counts, err := CountPersonalCollections(ctx, pool, 1, collections, viewer)
	if err != nil || len(counts) != 2 || counts["manual"] != 1 || counts["synthetic-0"] != 2 {
		t.Fatalf("manual/smart counts: %v %v", counts, err)
	}
	counts, err = CountPersonalCollections(ctx, pool, 2, []PersonalCollectionDefinition{manual}, viewer)
	if count, ok := counts["manual"]; err != nil || !ok || count != 0 {
		t.Fatalf("account membership reused incorrectly: %v %v", counts, err)
	}
	budget := ctx.Value(collectionCountBudgetKey{}).(*collectionCountBudget)
	if len(budget.entries) != 3 {
		t.Fatalf("all owners/types must share one ledger: %d", len(budget.entries))
	}
}

func TestCollectionCountBatchFailureNoRetryDB(t *testing.T) {
	trace := &collectionBudgetTrace{}
	pool, _ := collectionBudgetTestPool(t, trace, nil)
	if _, err := pool.Exec(t.Context(), `CREATE FUNCTION count_resource_error() RETURNS int LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic resource error' USING ERRCODE='54000'; END $$`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := WithCollectionCountBudget(t.Context())
	defer cancel()
	queries := []collectionCountStatement{{id: "good-prefix", sql: "SELECT 1"}, {id: "failure", sql: "SELECT count_resource_error()"}, {id: "good-suffix", sql: "SELECT 2"}}
	for i := range queries {
		queries[i].result = &collectionCountResult{done: make(chan struct{})}
	}
	jobs := buildCollectionCountJobs(ctx, pool, 1, queries)
	counts, err := executeCollectionCountBatch(ctx, pool, 1, jobs[0].sql, jobs[0].args)
	calls, _ := trace.result()
	if len(counts) != 0 || !errors.Is(err, ErrCollectionCountBudget) || calls != 1 {
		t.Fatalf("failed batch retained partial counts or retried: %v calls=%d err=%v", counts, calls, err)
	}
	// Ordinary SQL errors retain their classification, with no individual retry.
	counts, err = executeCollectionCountBatch(ctx, pool, 1, `SELECT $1::text, (SELECT missing_column)`, []any{"ordinary"})
	if len(counts) != 0 || err == nil || errors.Is(err, ErrCollectionCountBudget) {
		t.Fatalf("ordinary failure: %v %v", counts, err)
	}
	before, _ := trace.result()
	canceled, cancelNow := context.WithCancel(ctx)
	cancelNow()
	_, err = executeCollectionCountBatch(canceled, pool, 1, jobs[0].sql, jobs[0].args)
	after, _ := trace.result()
	if !errors.Is(err, context.Canceled) || after != before {
		t.Fatalf("pre-canceled submission: calls=%d err=%v", after-before, err)
	}
	t.Logf("resource failure: submissions=1 returned_counts=0; pre-canceled submissions=0")
}

type collectionDeadlineOnlyContext struct {
	context.Context
	deadline time.Time
}

func (c collectionDeadlineOnlyContext) Deadline() (time.Time, bool) { return c.deadline, true }

func TestCollectionCountServerDeadlineAndSessionIsolationDB(t *testing.T) {
	pool, admin := collectionBudgetTestPool(t, nil, func(config *pgxpool.Config) {
		config.MaxConns = 1
		config.ConnConfig.RuntimeParams["statement_timeout"] = "15000"
	})
	ctx, cancel := WithCollectionCountBudget(t.Context())
	defer cancel()
	counts, err := executeCollectionCountBatch(ctx, pool, 1, `/* collection-count-test */ SELECT 'readonly', (current_setting('transaction_read_only')='on')::int`, nil)
	if err != nil || counts["readonly"] != 1 {
		t.Fatalf("transaction is not read-only: %v %v", counts, err)
	}
	// Deadline advertised to the executor, but no client cancellation: PostgreSQL
	// itself must end the statement, independently of pgx's CancelRequest path.
	serverCtx := collectionDeadlineOnlyContext{Context: t.Context(), deadline: time.Now().Add(180 * time.Millisecond)}
	started := time.Now()
	_, err = executeCollectionCountBatch(serverCtx, pool, 1, `/* collection-count-test */ SELECT 'timeout', (SELECT 1 FROM pg_sleep(10))`, nil)
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	if !errors.Is(err, ErrCollectionCountBudget) || !ok || pgErr.Code != "57014" || time.Since(started) > 2*time.Second {
		t.Fatalf("server deadline did not end query: %v duration=%s", err, time.Since(started))
	}
	var setting string
	if err := pool.QueryRow(t.Context(), "SHOW statement_timeout").Scan(&setting); err != nil || setting != "15s" {
		t.Fatalf("session setting leaked: %q %v", setting, err)
	}
	if active := collectionBudgetActivity(t, admin, pool); active != 0 {
		t.Fatalf("server deadline left %d active backends", active)
	}
	t.Logf("server-owned statement timeout: sqlstate=%s elapsed=%s remaining_active=0; session_timeout=%s", pgErr.Code, time.Since(started), setting)
}

func collectionBudgetActivity(t *testing.T, admin, pool *pgxpool.Pool) int {
	t.Helper()
	var active int
	if err := admin.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity WHERE application_name=$1 AND state='active' AND query LIKE '/* collection-count-test */%'`, pool.Config().ConnConfig.RuntimeParams["application_name"]).Scan(&active); err != nil {
		t.Fatal(err)
	}
	return active
}

func waitCollectionBudgetActivity(t *testing.T, admin, pool *pgxpool.Pool, want int) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if got := collectionBudgetActivity(t, admin, pool); got == want {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("backend activity did not reach %d", want)
		case <-t.Context().Done():
			t.Fatal("test canceled")
		}
	}
}

func TestCollectionCountNodeAndAccountLifetimeDB(t *testing.T) {
	pool, admin := collectionBudgetTestPool(t, nil, nil)
	ctx, cancel := WithCollectionCountBudget(t.Context())
	defer cancel()
	results := make(chan error, 4)
	run := func(account int) {
		go func() {
			_, err := executeCollectionCountBatch(ctx, pool, account, `/* collection-count-test */ SELECT 'sleep', (SELECT 1 FROM pg_sleep(10))`, nil)
			results <- err
		}()
	}
	run(1)
	run(1)
	waitCollectionBudgetActivity(t, admin, pool, 2)
	started := time.Now()
	if _, err := executeCollectionCountBatch(ctx, pool, 1, `/* collection-count-test */ SELECT 'extra',1`, nil); !errors.Is(err, ErrCollectionCountBudget) {
		t.Fatalf("account admission: %v", err)
	}
	run(2)
	run(2)
	waitCollectionBudgetActivity(t, admin, pool, 4)
	if _, err := executeCollectionCountBatch(ctx, pool, 3, `/* collection-count-test */ SELECT 'extra',1`, nil); !errors.Is(err, ErrCollectionCountBudget) {
		t.Fatalf("node admission: %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("admission queued instead of failing fast")
	}
	cancel()
	for range 4 {
		if err := <-results; !errors.Is(err, context.Canceled) {
			t.Fatalf("mid-flight cancellation: %v", err)
		}
	}
	waitCollectionBudgetActivity(t, admin, pool, 0)
	t.Logf("pg_stat_activity peak=4 across two accounts; per-account peak=2; canceled active backends reached 0 in %s", time.Since(started))
}

func TestCollectionCountCanceledConnectionKeepsLeaseDB(t *testing.T) {
	var blockCancelDial atomic.Bool
	gate, entered := make(chan struct{}), make(chan struct{})
	openGate := sync.OnceFunc(func() { close(gate) })
	defer openGate()
	var enteredOnce sync.Once
	var cleanup <-chan struct{}
	trace := &collectionBudgetTrace{onEnd: func(conn *pgx.Conn) {
		cleanup = conn.PgConn().CleanupDone()
		t.Logf("count trace ended: connection_closed=%t", conn.PgConn().IsClosed())
	}}
	pool, admin := collectionBudgetTestPool(t, trace, func(config *pgxpool.Config) {
		config.MaxConns = 1
		dial := config.ConnConfig.DialFunc
		config.ConnConfig.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
			if blockCancelDial.Load() {
				enteredOnce.Do(func() { close(entered) })
				select {
				case <-gate:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return dial(ctx, network, address)
		}
	})
	ctx, cancel := WithCollectionCountBudget(t.Context())
	defer cancel()
	result := make(chan error, 1)
	blockCancelDial.Store(true)
	go func() {
		_, err := executeCollectionCountBatch(ctx, pool, 87, `/* collection-count-test */ SELECT $1::text, (SELECT 1 FROM pg_sleep(10))`, []any{"cancel"})
		result <- err
	}()
	waitCollectionBudgetActivity(t, admin, pool, 1)
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-flight cancel: %v", err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("pgx cancel cleanup did not start")
	}
	select {
	case <-cleanup:
		t.Fatal("cleanup completed before cancel connection gate")
	default:
	}
	// Even with a new request, the one-connection pool's statement admission
	// remains occupied while the old backend is still being canceled.
	newCtx, newCancel := WithCollectionCountBudget(t.Context())
	defer newCancel()
	if _, err := executeCollectionCountBatch(newCtx, pool, 88, `/* collection-count-test */ SELECT 'replacement',1`, nil); !errors.Is(err, ErrCollectionCountBudget) {
		t.Fatalf("replacement admitted before cleanup: %v", err)
	}
	openGate()
	select {
	case <-cleanup:
	case <-time.After(2 * time.Second):
		t.Fatal("pgx cleanup did not finish")
	}
	waitCollectionBudgetActivity(t, admin, pool, 0)
	t.Log("canceled connection retained pool/node/account admission until pgx CleanupDone; backend active count reached 0")
}
