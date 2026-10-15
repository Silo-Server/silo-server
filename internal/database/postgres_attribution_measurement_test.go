package database

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/database/queryattr"
)

func TestAttributionConnectionHeapBound(t *testing.T) {
	const connections = 100
	tr := poolTracer{attribution: queryattr.Tracer{Store: queryattr.NewStore()}}
	conns := make([]*pgx.Conn, connections)
	for i := range conns {
		conns[i] = bulkMemoryConn(t, tr, pgx.QueryExecModeSimpleProtocol)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for _, conn := range conns {
		queryattr.ClearCopyTicket(conn)
		if _, on := copyTracer(conn); !on {
			t.Fatal("connection did not retain the composite copy tracer")
		}
		if len(conn.PgConn().CustomData()) != 2 {
			t.Fatalf("custom data entries=%d want=2", len(conn.PgConn().CustomData()))
		}
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	heap := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	t.Logf("connections=%d retained_heap=%d ceiling=%d", connections, heap, connections*512)
	if heap > connections*512 {
		t.Errorf("connection state, CustomData entries and boxed copy config retain %d bytes want<=%d", heap, connections*512)
	}
	runtime.KeepAlive(conns)
}

type identityCallerCounts struct {
	Caller     string `json:"caller"`
	Identities int    `json:"identities"`
	Started    uint64 `json:"started"`
	Completed  uint64 `json:"completed"`
}

// The projection is test-only and contains counts and binary-backed callers.
func writeIdentityCounts(out io.Writer, store *queryattr.Store) error {
	reserved := map[string]bool{}
	for _, row := range queryattr.NewStore().Snapshot() {
		reserved[row.Identity.Caller.Function] = true
	}
	counts := struct {
		Identities int                    `json:"identities"`
		Functions  int                    `json:"functions"`
		Callers    []identityCallerCounts `json:"callers"`
	}{Callers: []identityCallerCounts{}}
	byCaller := map[string]*identityCallerCounts{}
	for _, row := range store.Snapshot() {
		name := row.Identity.Caller.Function
		isReserved := reserved[name]
		if isReserved {
			if name != "caller_unavailable" && name != "connection_setup" && name != "telemetry_persistence" || row.Counters.Started == 0 && row.Counters.Completed == 0 {
				continue
			}
		}
		caller := byCaller[name]
		if caller == nil {
			caller = &identityCallerCounts{Caller: name}
			byCaller[name] = caller
		}
		if row.Identity.Form == queryattr.FunctionOnly {
			counts.Functions++
		} else if !isReserved {
			counts.Identities++
			caller.Identities++
		}
		caller.Started += row.Counters.Started
		caller.Completed += row.Counters.Completed
	}
	names := make([]string, 0, len(byCaller))
	for name := range byCaller {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		counts.Callers = append(counts.Callers, *byCaller[name])
	}
	return json.NewEncoder(out).Encode(counts)
}

func TestIdentityCountDumpPrivacy(t *testing.T) {
	s := queryattr.NewStore()
	tr := queryattr.Tracer{Store: s}
	const sentinel = "PRIVATE_IDENTITY_DUMP_SENTINEL"
	ctx := queryattr.WithLabel(t.Context(), queryattr.LabelPersonalCollectionCounts)
	r := tr.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "SELECT '" + sentinel + "', $1", Args: []any{sentinel}})
	tr.TraceQueryEnd(r, nil, pgx.TraceQueryEndData{Err: &pgconn.PgError{Code: "23505", Message: sentinel, Detail: sentinel, Hint: sentinel}})
	var out bytes.Buffer
	if err := writeIdentityCounts(&out, s); err != nil {
		t.Fatal("identity count dump failed")
	}
	want := `{"identities":1,"functions":1,"callers":[{"caller":"catalog.personal_collection_counts","identities":1,"started":1,"completed":1}]}` + "\n"
	if out.String() != want {
		t.Fatalf("identity count projection=%q want=%q", out.String(), want)
	}
	if strings.Contains(out.String(), sentinel) || !strings.Contains(`{"sql":"`+sentinel+`"}`, sentinel) {
		t.Fatal("privacy predicate failed to reject private SQL, arguments or error fields")
	}
	var empty bytes.Buffer
	if err := writeIdentityCounts(&empty, queryattr.NewStore()); err != nil || empty.String() != "{\"identities\":0,\"functions\":0,\"callers\":[]}\n" {
		t.Fatalf("empty dump=%q error=%v want zero counts", empty.String(), err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil || len(decoded) != 3 {
		t.Fatalf("dump fields=%d error=%v want three count/caller fields", len(decoded), err)
	}
}

func TestQueryAttributionIdentityCountsDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := NewPoolForRole(t.Context(), config.DatabaseConfig{URL: dsn, MaxConnections: 1}, "checks")
	if err != nil {
		t.Fatal("measurement pool could not connect")
	}
	t.Cleanup(pool.Close)
	if !queryattr.Enabled() {
		t.Fatal("identity measurement requires attribution enabled")
	}
	var value int
	if err := pool.QueryRow(t.Context(), "SELECT 1").Scan(&value); err != nil || value != 1 {
		t.Fatal("measurement query did not return the expected value")
	}
	var out bytes.Buffer
	if err := writeIdentityCounts(&out, queryattr.Default); err != nil {
		t.Fatal("identity count dump failed")
	}
	t.Log(out.String())
}
