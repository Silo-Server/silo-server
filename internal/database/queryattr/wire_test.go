package queryattr

import (
	"context"
	"errors"
	"fmt"
	"net"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
)

// A memory-only wire peer exercises pgx itself. It opens no socket or database.
func wireConnection(t *testing.T, tr pgx.QueryTracer, mode pgx.QueryExecMode) (*pgx.Conn, func()) {
	t.Helper()
	client, server := net.Pipe()
	done := make(chan struct{})
	cfg, err := pgx.ParseConfig("host=127.0.0.1 user=qa sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Tracer = tr
	cfg.DefaultQueryExecMode = mode
	cfg.StatementCacheCapacity = 1
	cfg.DialFunc = func(context.Context, string, string) (net.Conn, error) { return client, nil }
	go func() {
		defer close(done)
		defer func() { _ = server.Close() }()
		b := pgproto3.NewBackend(server, server)
		if _, err := b.ReceiveStartupMessage(); err != nil {
			return
		}
		b.Send(&pgproto3.AuthenticationOk{})
		b.Send(&pgproto3.ParameterStatus{Name: "standard_conforming_strings", Value: "on"})
		b.Send(&pgproto3.ParameterStatus{Name: "server_version", Value: "17.0"})
		b.Send(&pgproto3.ParameterStatus{Name: "client_encoding", Value: "UTF8"})
		b.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
		if b.Flush() != nil {
			return
		}
		status := byte('I')
		prepared := map[string]string{}
		bound := ""
		fields := []pgproto3.FieldDescription{{Name: []byte("value"), DataTypeOID: 23, DataTypeSize: 4, TypeModifier: -1}}
		sendRows := func(sql string, describe bool) {
			lower := strings.ToLower(sql)
			switch {
			case strings.HasPrefix(lower, "begin"):
				status = 'T'
				b.Send(&pgproto3.CommandComplete{CommandTag: []byte("BEGIN")})
			case strings.HasPrefix(lower, "commit"), strings.HasPrefix(lower, "rollback") && !strings.Contains(lower, " to "):
				status = 'I'
				tag := "ROLLBACK"
				if strings.HasPrefix(lower, "commit") {
					tag = "COMMIT"
				}
				b.Send(&pgproto3.CommandComplete{CommandTag: []byte(tag)})
			case strings.HasPrefix(lower, "savepoint"), strings.HasPrefix(lower, "release"), strings.HasPrefix(lower, "rollback to"):
				b.Send(&pgproto3.CommandComplete{CommandTag: []byte("SAVEPOINT")})
			case strings.Contains(lower, "fail"):
				if status == 'T' {
					status = 'E'
				}
				b.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: "23505", Message: "PRIVATE_ERROR", Detail: "PRIVATE_DETAIL", Hint: "PRIVATE_HINT"})
			default:
				if describe {
					b.Send(&pgproto3.RowDescription{Fields: fields})
				}
				n := 3
				if strings.Contains(lower, "empty") {
					n = 0
				}
				for i := 0; i < n; i++ {
					b.Send(&pgproto3.DataRow{Values: [][]byte{[]byte(fmt.Sprint(i + 1))}})
				}
				b.Send(&pgproto3.CommandComplete{CommandTag: []byte(fmt.Sprintf("SELECT %d", n))})
			}
		}
		for {
			m, err := b.Receive()
			if err != nil {
				return
			}
			switch m := m.(type) {
			case *pgproto3.Query:
				sendRows(m.String, true)
				b.Send(&pgproto3.ReadyForQuery{TxStatus: status})
			case *pgproto3.Parse:
				prepared[m.Name] = m.Query
				b.Send(&pgproto3.ParseComplete{})
			case *pgproto3.Describe:
				b.Send(&pgproto3.ParameterDescription{ParameterOIDs: []uint32{23}})
				b.Send(&pgproto3.RowDescription{Fields: fields})
			case *pgproto3.Bind:
				bound = prepared[m.PreparedStatement]
				b.Send(&pgproto3.BindComplete{})
			case *pgproto3.Execute:
				sendRows(bound, false)
			case *pgproto3.Close: // Force the pgx invalidated-cache-deallocation gap.
				b.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: "26000", Message: "PRIVATE_DEALLOCATE"})
			case *pgproto3.Sync:
				b.Send(&pgproto3.ReadyForQuery{TxStatus: status})
			case *pgproto3.Flush:
			case *pgproto3.Terminate:
				return
			default:
				return
			}
			if b.Flush() != nil {
				return
			}
		}
	}()
	c, err := pgx.ConnectConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()); _ = client.Close(); _ = server.Close(); <-done })
	return c, func() { _ = server.Close() }
}

func TestPGXRowsLifecycleAndUnobservedClose(t *testing.T) {
	for _, kind := range []string{"exhaust", "early_close", "repeated_close", "scan_error", "abandoned", "no_rows", "exec", "queryrow", "multi_exec"} {
		t.Run(kind, func(t *testing.T) {
			s := NewStore()
			tr := Tracer{Store: s}
			c, _ := wireConnection(t, tr, pgx.QueryExecModeSimpleProtocol)
			switch kind {
			case "exec", "multi_exec":
				sql := "SELECT 1"
				if kind == "multi_exec" {
					sql += "; SELECT 2"
				}
				if _, err := c.Exec(t.Context(), sql); err != nil {
					t.Fatal(err)
				}
			case "queryrow":
				var n int
				if err := c.QueryRow(t.Context(), "SELECT 1").Scan(&n); err != nil || n != 1 {
					t.Fatalf("row=%d err=%v", n, err)
				}
			case "no_rows":
				var n int
				if err := c.QueryRow(t.Context(), "SELECT empty").Scan(&n); !errors.Is(err, pgx.ErrNoRows) {
					t.Fatalf("err=%v want ErrNoRows", err)
				}
			default:
				rows, err := c.Query(t.Context(), "SELECT 1")
				if err != nil {
					t.Fatal(err)
				}
				if got := s.Totals(); got.Started != 1 || got.Completed != 0 {
					t.Fatalf("before close=%+v", got)
				}
				switch kind {
				case "exhaust":
					for rows.Next() {
						var n int
						if err := rows.Scan(&n); err != nil {
							t.Fatal(err)
						}
					}
					if rows.Err() != nil {
						t.Fatal(rows.Err())
					}
				case "early_close", "repeated_close":
					rows.Next()
					rows.Close()
					if kind == "repeated_close" {
						rows.Close()
					}
				case "scan_error":
					rows.Next()
					var bad struct{}
					if rows.Scan(&bad) == nil {
						t.Fatal("bad scan succeeded")
					}
				case "abandoned":
					tr.BeforeClose(c)
				}
			}
			got := s.Totals()
			if got.Started != 1 || got.Completed != 1 {
				t.Fatalf("finished totals=%+v", got)
			}
			if kind == "abandoned" {
				if got.Unobserved != 1 || got.Observed != 0 || got.ConnectionClosed != 1 {
					t.Fatalf("abandonment=%+v", got)
				}
			} else {
				want := Success
				if kind == "scan_error" {
					want = Client
				}
				if got.Observed != 1 || got.Outcomes[want] != 1 {
					t.Fatalf("outcomes=%+v want class=%d", got, want)
				}
			}
		})
	}
}

func TestPGXFailedDeallocationResolvesAtClose(t *testing.T) {
	s := NewStore()
	tr := Tracer{Store: s}
	c, _ := wireConnection(t, tr, pgx.QueryExecModeCacheStatement)
	// Two distinct statements evict the first prepared statement from the size-one cache.
	for _, sql := range []string{"SELECT $1+1", "SELECT $1+2"} {
		if _, err := c.Exec(t.Context(), sql, 1); err != nil {
			t.Fatal(err)
		}
	}
	_, err := c.Exec(t.Context(), "SELECT $1+3", 1)
	if err == nil || !strings.Contains(err.Error(), "failed to deallocate") {
		t.Fatalf("err=%v want unchanged pgx deallocation failure", err)
	}
	before := s.Totals()
	if before.Started != 3 || before.Completed != 2 || before.Observed != 2 {
		t.Fatalf("before close=%+v", before)
	}
	tr.BeforeClose(c)
	got := s.Totals()
	if got.Completed != 3 || got.Observed != 2 || got.Unobserved != 1 || got.ConnectionClosed != 1 {
		t.Fatalf("resolved totals=%+v", got)
	}
	if got.Outcomes[classIndex("26")] != 0 {
		t.Fatal("close inferred SQLSTATE outcome")
	}
}

func TestPGXTransactionOwnerThroughDeferredRollback(t *testing.T) {
	s := NewStore()
	tr := Tracer{Store: s}
	c, _ := wireConnection(t, tr, pgx.QueryExecModeSimpleProtocol)
	ctx := WithLabel(t.Context(), labelTest)
	tx, err := c.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	later := WithLabel(t.Context(), LabelPersonalCollectionCounts)
	nested, err := tx.Begin(later)
	if err != nil {
		t.Fatal(err)
	}
	if err := nested.Commit(later); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{"SAVEPOINT custom", "ROLLBACK TO SAVEPOINT custom", "RELEASE SAVEPOINT custom"} {
		if _, err := tx.Exec(later, sql); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(later, "SELECT fail"); err == nil {
		t.Fatal("failed statement succeeded")
	}
	func() {
		defer func() {
			if err := tx.Rollback(later); err != nil {
				t.Error(err)
			}
		}()
	}()
	if got := stateFor(c).owner; got.Function != "" {
		t.Fatalf("owner after rollback=%+v want empty", got)
	}
	var controls uint64
	for _, r := range s.Snapshot() {
		if r.Identity.Form == Transaction && r.Counters.Completed != 0 {
			if r.Identity.Caller.Function != "queryattr.test" {
				t.Fatalf("control owner=%+v", r.Identity)
			}
			controls += r.Counters.Completed
		}
	}
	if controls != 7 {
		t.Fatalf("transaction controls=%d want=7", controls)
	}
	// A failed BEGIN cannot set the owner, even with a fresh caller.
	start := tr.TraceQueryStart(later, c, pgx.TraceQueryStartData{SQL: "BEGIN"})
	tr.TraceQueryEnd(start, c, pgx.TraceQueryEndData{Err: errors.New("begin failed")})
	if got := stateFor(c).owner; got.Function != "" {
		t.Fatalf("failed begin owner=%+v", got)
	}
	next, err := c.Begin(later)
	if err != nil {
		t.Fatal(err)
	}
	if got := stateFor(c).owner.Function; got != personalCollectionCountsLabel {
		t.Fatalf("next owner=%q", got)
	}
	if err := next.Commit(later); err != nil {
		t.Fatal(err)
	}
}

func TestPGXOverlappingStartsRetainUnfinishedRecords(t *testing.T) {
	t.Run("busy_query_keeps_abandoned_rows", func(t *testing.T) {
		s := NewStore()
		tr := Tracer{Store: s}
		c, _ := wireConnection(t, tr, pgx.QueryExecModeSimpleProtocol)
		rows, err := c.Query(t.Context(), "SELECT 1")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		if _, err := c.Exec(t.Context(), "SELECT 2"); err == nil {
			t.Fatal("busy connection accepted Exec")
		}
		tr.BeforeClose(c)
		got := s.Totals()
		if got.Started != 2 || got.Completed != 2 || got.Observed != 1 || got.Unobserved != 1 {
			t.Fatalf("busy close=%+v", got)
		}
	})
	t.Run("repeated_deallocation_gaps", func(t *testing.T) {
		s := NewStore()
		tr := Tracer{Store: s}
		c, _ := wireConnection(t, tr, pgx.QueryExecModeCacheStatement)
		for _, sql := range []string{"SELECT $1+1", "SELECT $1+2"} {
			if _, err := c.Exec(t.Context(), sql, 1); err != nil {
				t.Fatal(err)
			}
		}
		for range 2 {
			if _, err := c.Exec(t.Context(), "SELECT $1+3", 1); err == nil {
				t.Fatal("deallocation failure missing")
			}
		}
		tr.BeforeClose(c)
		got := s.Totals()
		if got.Started != 4 || got.Completed != 4 || got.Observed != 2 || got.Unobserved != 2 || got.ConnectionClosed != 2 {
			t.Fatalf("repeated gap close=%+v", got)
		}
	})
}

func TestPGXFailedSendIsOneNetworkOutcome(t *testing.T) {
	s := NewStore()
	tr := Tracer{Store: s}
	c, disconnect := wireConnection(t, tr, pgx.QueryExecModeSimpleProtocol)
	disconnect()
	if _, err := c.Exec(t.Context(), "SELECT 1"); err == nil {
		t.Fatal("disconnected peer accepted query")
	}
	got := s.Totals()
	if got.Started != 1 || got.Completed != 1 || got.Outcomes[Network] != 1 {
		t.Fatalf("failed send=%+v", got)
	}
}

func TestPGXReturnsMatchAttributionOff(t *testing.T) {
	type result struct {
		values                       []int
		noRows, canceled, scanFailed bool
		code                         string
		status                       byte
	}
	run := func(on bool) result {
		var tracer pgx.QueryTracer
		if on {
			tracer = Tracer{Store: NewStore()}
		}
		c, _ := wireConnection(t, tracer, pgx.QueryExecModeSimpleProtocol)
		r := result{}
		rows, err := c.Query(t.Context(), "SELECT 1")
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var n int
			if err := rows.Scan(&n); err != nil {
				t.Fatal(err)
			}
			r.values = append(r.values, n)
		}
		rows.Close()
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		var n int
		r.noRows = errors.Is(c.QueryRow(t.Context(), "SELECT empty").Scan(&n), pgx.ErrNoRows)
		rows, err = c.Query(t.Context(), "SELECT 1")
		if err != nil {
			t.Fatal(err)
		}
		rows.Next()
		var bad struct{}
		r.scanFailed = rows.Scan(&bad) != nil
		tx, err := c.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(t.Context(), "SELECT fail")
		var p *pgconn.PgError
		if errors.As(err, &p) {
			r.code = p.Code
		}
		if err := tx.Rollback(t.Context()); err != nil {
			t.Fatal(err)
		}
		r.status = c.PgConn().TxStatus()
		canceled, cancel := context.WithCancel(t.Context())
		cancel()
		_, err = c.Exec(canceled, "SELECT 1")
		r.canceled = errors.Is(err, context.Canceled)
		return r
	}
	off, on := run(false), run(true)
	if !reflect.DeepEqual(off, on) {
		t.Fatalf("off=%+v on=%+v", off, on)
	}
	if !on.noRows || !on.canceled || !on.scanFailed || on.code != "23505" || on.status != 'I' {
		t.Fatalf("fixture outcomes=%+v", on)
	}
}
