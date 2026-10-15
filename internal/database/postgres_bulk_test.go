package database

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/Silo-Server/silo-server/internal/database/queryattr"
)

type bulkTx struct {
	pgx.Tx
	conn *pgx.Conn
	copy func(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error)
}

func (tx bulkTx) Conn() *pgx.Conn { return tx.conn }
func (tx bulkTx) CopyFrom(ctx context.Context, table pgx.Identifier, columns []string, source pgx.CopyFromSource) (int64, error) {
	return tx.copy(ctx, table, columns, source)
}

type unreadCopySource struct{ calls int }

func (s *unreadCopySource) Next() bool             { s.calls++; return false }
func (s *unreadCopySource) Values() ([]any, error) { s.calls++; return nil, nil }
func (s *unreadCopySource) Err() error             { s.calls++; return nil }

func copyExporter(t *testing.T) *tracetest.InMemoryExporter {
	t.Helper()
	e := tracetest.NewInMemoryExporter()
	p := sdktrace.NewTracerProvider(sdktrace.WithSyncer(e))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(p)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		if err := p.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return e
}

func TestCopyFromWrapperCompletesMissingCoarseEnd(t *testing.T) {
	for _, missing := range []bool{true, false} {
		t.Run(fmt.Sprintf("missing_end_%v", missing), func(t *testing.T) {
			e := copyExporter(t)
			s := queryattr.NewStore()
			tr := poolTracer{coarse: postgresTracer{role: "checks"}, attribution: queryattr.Tracer{Store: s}}
			conn := bulkMemoryConn(t, tr, pgx.QueryExecModeSimpleProtocol)
			e.Reset()
			want := &pgconn.PgError{Code: "23505", Message: "PRIVATE_ERROR", Detail: "PRIVATE_DETAIL", Hint: "PRIVATE_HINT"}
			source := &unreadCopySource{}
			table, columns := pgx.Identifier{"PRIVATE_TABLE"}, []string{"PRIVATE_COLUMN"}
			tx := bulkTx{conn: conn, copy: func(ctx context.Context, got pgx.Identifier, cols []string, src pgx.CopyFromSource) (int64, error) {
				if !reflect.DeepEqual(got, table) || !reflect.DeepEqual(cols, columns) || src != source {
					t.Fatal("copy inputs changed")
				}
				c := tr.TraceCopyFromStart(ctx, conn, pgx.TraceCopyFromStartData{TableName: got, ColumnNames: cols})
				if !missing {
					tr.TraceCopyFromEnd(c, conn, pgx.TraceCopyFromEndData{CommandTag: pgconn.NewCommandTag("COPY 7"), Err: want})
				}
				return 7, want
			}}
			for range 2 {
				n, err := CopyFrom(t.Context(), tx, table, columns, source)
				if n != 7 || !reflect.ValueOf(err).Equal(reflect.ValueOf(want)) || source.calls != 0 {
					t.Fatalf("result=%d/%v source calls=%d, want original 7/error and unread", n, err, source.calls)
				}
			}
			got := s.Totals()
			wantGaps := uint64(0)
			if missing {
				wantGaps = 2
			}
			if got.Started != 2 || got.Completed != 2 || got.CopySetupFailures != wantGaps || got.DuplicateEnds != 0 || got.Rows != 14 {
				t.Fatalf("totals=%+v, want two completed copies, 14 rows and %d setup gaps", got, wantGaps)
			}
			if spans := e.GetSpans(); len(spans) != 2 {
				t.Fatalf("coarse spans=%d, want 2", len(spans))
			}
			encoded, err := json.Marshal(struct {
				Rows  []queryattr.RowSnapshot
				Spans tracetest.SpanStubs
			}{s.Snapshot(), e.GetSpans()})
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"PRIVATE_TABLE", "PRIVATE_COLUMN", "PRIVATE_ERROR", "PRIVATE_DETAIL", "PRIVATE_HINT"} {
				if strings.Contains(string(encoded), secret) {
					t.Fatalf("output contains sentinel %q", secret)
				}
			}
			// A transaction rejected before pgx starts a copy must not use the previous ticket.
			tx.copy = func(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
				return 0, pgx.ErrTxClosed
			}
			if _, err := CopyFrom(t.Context(), tx, table, columns, source); !errors.Is(err, pgx.ErrTxClosed) {
				t.Fatalf("closed transaction error=%v", err)
			}
			if after := s.Totals(); after.Completed != 2 || after.DuplicateEnds != 0 {
				t.Fatalf("stale ticket changed totals: %+v", after)
			}
		})
	}
}

func TestCopyFromWrapperPreservesPanic(t *testing.T) {
	s := queryattr.NewStore()
	tr := poolTracer{attribution: queryattr.Tracer{Store: s}}
	conn := bulkMemoryConn(t, tr, pgx.QueryExecModeSimpleProtocol)
	tx := bulkTx{conn: conn, copy: func(ctx context.Context, table pgx.Identifier, cols []string, _ pgx.CopyFromSource) (int64, error) {
		tr.TraceCopyFromStart(ctx, conn, pgx.TraceCopyFromStartData{TableName: table, ColumnNames: cols})
		panic("copy panic")
	}}
	func() {
		defer func() {
			if got := recover(); got != "copy panic" {
				t.Fatalf("panic=%v, want original panic", got)
			}
		}()
		_, _ = CopyFrom(t.Context(), tx, pgx.Identifier{"table"}, []string{"column"}, &unreadCopySource{})
	}()
	if got := s.Totals(); got.Started != 1 || got.Completed != 0 || got.CopySetupFailures != 0 {
		t.Fatalf("panic totals=%+v, want unfinished copy", got)
	}
	tr.attribution.BeforeClose(conn)
	if got := s.Totals(); got.Completed != 1 || got.Unobserved != 1 {
		t.Fatalf("teardown totals=%+v, want one unobserved completion", got)
	}
}

func TestCopyFromRealSuccessAndSourceError(t *testing.T) {
	sourceFailure := errors.New("PRIVATE_SOURCE_ERROR")
	for _, sourceError := range []bool{false, true} {
		t.Run(fmt.Sprintf("source_error_%v", sourceError), func(t *testing.T) {
			e := copyExporter(t)
			var plainN int64
			var plainError string
			for _, on := range []bool{false, true} {
				s := queryattr.NewStore()
				var tr pgx.QueryTracer = postgresTracer{role: "checks"}
				if on {
					tr = poolTracer{coarse: postgresTracer{role: "checks"}, attribution: queryattr.Tracer{Store: s}}
				}
				conn := bulkMemoryConn(t, tr, pgx.QueryExecModeDescribeExec)
				tx, err := conn.Begin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				e.Reset()
				before := s.Totals()
				source := pgx.CopyFromRows([][]any{{int32(1)}, {int32(2)}})
				if sourceError {
					source = &failingCopySource{err: sourceFailure}
				}
				var n int64
				if on {
					n, err = CopyFrom(t.Context(), tx, pgx.Identifier{"private_table"}, []string{"private_column"}, source)
				} else {
					n, err = tx.CopyFrom(t.Context(), pgx.Identifier{"private_table"}, []string{"private_column"}, source)
				}
				if sourceError {
					if n != 0 || err == nil {
						t.Fatalf("source result=%d/%v, want pgx failure", n, err)
					}
				} else if n != 2 || err != nil {
					t.Fatalf("copy result=%d/%v, want 2/nil", n, err)
				}
				errorText := ""
				if err != nil {
					errorText = err.Error()
				}
				if !on {
					plainN, plainError = n, errorText
					continue
				}
				if n != plainN || errorText != plainError {
					t.Fatalf("result=%d/%q, want off-mode %d/%q", n, errorText, plainN, plainError)
				}
				got := s.Totals()
				if got.Started-before.Started != 1 || got.Completed-before.Completed != 1 || got.CopySetupFailures != 0 || got.DuplicateEnds != 0 {
					t.Fatalf("copy totals=%+v, want exactly one completion with no gaps", got)
				}
				if spans := e.GetSpans(); len(spans) != 1 {
					t.Fatalf("coarse spans=%d, want 1", len(spans))
				}
			}
		})
	}
}

type failingCopySource struct{ err error }

func (*failingCopySource) Next() bool             { return false }
func (*failingCopySource) Values() ([]any, error) { panic("source has no rows") }
func (s *failingCopySource) Err() error           { return s.err }

func TestCopyFromWrapperWarmedAllocations(t *testing.T) {
	tr := poolTracer{coarse: postgresTracer{role: "checks"}, attribution: queryattr.Tracer{Store: queryattr.NewStore()}}
	conn := bulkMemoryConn(t, tr, pgx.QueryExecModeSimpleProtocol)
	table, columns, source := pgx.Identifier{"table"}, []string{"column"}, &unreadCopySource{}
	tx := &bulkTx{conn: conn, copy: func(ctx context.Context, table pgx.Identifier, cols []string, _ pgx.CopyFromSource) (int64, error) {
		c := tr.TraceCopyFromStart(ctx, conn, pgx.TraceCopyFromStartData{TableName: table, ColumnNames: cols})
		tr.TraceCopyFromEnd(c, conn, pgx.TraceCopyFromEndData{CommandTag: pgconn.NewCommandTag("COPY 2")})
		return 2, nil
	}}
	ctx := t.Context()
	direct := testing.AllocsPerRun(100, func() { _, _ = tx.CopyFrom(ctx, table, columns, source) })
	wrapped := testing.AllocsPerRun(100, func() { _, _ = CopyFrom(ctx, tx, table, columns, source) })
	if wrapped != direct {
		t.Fatalf("wrapper allocations=%g direct=%g, want no added warmed allocations", wrapped, direct)
	}
}

func TestCopyFromRealSetupFailuresAndNestedTransaction(t *testing.T) {
	for _, mode := range []pgx.QueryExecMode{pgx.QueryExecMode(99), pgx.QueryExecModeDescribeExec} {
		for _, nested := range []bool{false, true} {
			t.Run(fmt.Sprintf("mode_%d_nested_%v", mode, nested), func(t *testing.T) {
				e := copyExporter(t)
				var plainError string
				for _, on := range []bool{false, true} {
					s := queryattr.NewStore()
					var tr pgx.QueryTracer = postgresTracer{role: "checks"}
					if on {
						tr = poolTracer{coarse: postgresTracer{role: "checks"}, attribution: queryattr.Tracer{Store: s}}
					}
					conn := bulkMemoryConn(t, tr, mode)
					tx, err := conn.Begin(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					if nested {
						tx, err = tx.Begin(t.Context())
						if err != nil {
							t.Fatal(err)
						}
					}
					before := s.Totals()
					e.Reset()
					source := &unreadCopySource{}
					var n int64
					if on {
						n, err = CopyFrom(t.Context(), tx, pgx.Identifier{"PRIVATE_MISSING_TABLE"}, []string{"PRIVATE_COLUMN"}, source)
					} else {
						n, err = tx.CopyFrom(t.Context(), pgx.Identifier{"PRIVATE_MISSING_TABLE"}, []string{"PRIVATE_COLUMN"}, source)
					}
					if n != 0 || err == nil || source.calls != 0 {
						t.Fatalf("n=%d err=%v source calls=%d, want setup failure before iteration", n, err, source.calls)
					}
					if !on {
						plainError = err.Error()
						continue
					}
					if err.Error() != plainError {
						t.Fatalf("error=%q, want off-mode %q", err.Error(), plainError)
					}
					got := s.Totals()
					if got.Started-before.Started != 1 || got.Completed-before.Completed != 1 || got.CopySetupFailures != 1 {
						t.Fatalf("delta totals before=%+v after=%+v, want one completed copy setup failure", before, got)
					}
					copies := 0
					for _, span := range e.GetSpans() {
						if strings.Contains(span.Name, "copy") {
							copies++
						}
					}
					if copies != 1 {
						t.Fatalf("ended copy spans=%d, want 1", copies)
					}
				}
			})
		}
	}
}

type batchResultSentinel struct{ pgx.BatchResults }

type batchForwarder struct {
	result     pgx.BatchResults
	ctx        context.Context
	batch      *pgx.Batch
	panicValue any
}

func (s *batchForwarder) SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults {
	s.ctx, s.batch = ctx, b
	if s.panicValue != nil {
		panic(s.panicValue)
	}
	return s.result
}

func TestSendBatchPreservesPanic(t *testing.T) {
	want := "batch panic"
	s := &batchForwarder{panicValue: want}
	b := &pgx.Batch{}
	b.Queue("SELECT 1")
	defer func() {
		if got := recover(); got != want {
			t.Fatalf("panic=%v, want original %v", got, want)
		}
	}()
	SendBatch(t.Context(), s, b)
}

func TestSendBatchCountsEmptyAndPreservesResults(t *testing.T) {
	queryattr.Initialize()
	before := queryattr.Default.Totals()
	s := &batchForwarder{result: &batchResultSentinel{}}
	b := &pgx.Batch{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got := SendBatch(ctx, s, b); got != s.result || s.batch != b || s.ctx != ctx {
		t.Fatal("batch results, input or canceled context changed")
	}
	if got := queryattr.Default.Totals(); got.EmptyBatches-before.EmptyBatches != 1 || got.Started != before.Started {
		t.Fatalf("empty batch delta before=%+v after=%+v, want one gap, no statement", before, got)
	}
	b.Queue("SELECT 1")
	SendBatch(t.Context(), s, b)
	if got := queryattr.Default.Totals().EmptyBatches - before.EmptyBatches; got != 1 {
		t.Fatalf("empty gaps=%d, want 1", got)
	}
}

func TestSendBatchEmptyOnPoolConnectionAndTransaction(t *testing.T) {
	queryattr.Initialize()
	s := queryattr.NewStore()
	pool := memoryPool(t, true, s)
	if err := pool.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
	acquired, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	acquired.Release()
	tr := poolTracer{attribution: queryattr.Tracer{Store: s}}
	conn := bulkMemoryConn(t, tr, pgx.QueryExecModeSimpleProtocol)
	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, sender := range []BatchSender{pool, conn, tx} {
		before, gaps := s.Totals(), queryattr.Default.Totals().EmptyBatches
		results := SendBatch(t.Context(), sender, &pgx.Batch{})
		if err := results.Close(); err != nil {
			t.Fatal(err)
		}
		if got := s.Totals(); got.Started != before.Started || got.Completed != before.Completed {
			t.Fatalf("empty batch changed statements: before=%+v after=%+v", before, got)
		}
		if got := queryattr.Default.Totals().EmptyBatches; got != gaps+1 {
			t.Fatalf("empty gaps=%d, want %d", got, gaps+1)
		}
	}
}

// bulkMemoryConn drives real pgx setup and transaction checks without a database.
func bulkMemoryConn(t *testing.T, tr pgx.QueryTracer, mode pgx.QueryExecMode) *pgx.Conn {
	t.Helper()
	client, server := net.Pipe()
	done := make(chan struct{})
	cfg, err := pgx.ParseConfig("host=127.0.0.1 user=qa sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Tracer, cfg.DefaultQueryExecMode = tr, mode
	cfg.DialFunc = func(context.Context, string, string) (net.Conn, error) { return client, nil }
	go func() {
		defer close(done)
		defer func() { _ = server.Close() }()
		b := pgproto3.NewBackend(server, server)
		if _, err := b.ReceiveStartupMessage(); err != nil {
			return
		}
		b.Send(&pgproto3.AuthenticationOk{})
		b.Send(&pgproto3.ParameterStatus{Name: "server_version", Value: "17.0"})
		b.Send(&pgproto3.ParameterStatus{Name: "standard_conforming_strings", Value: "on"})
		b.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
		if b.Flush() != nil {
			return
		}
		failedDescribe := false
		var copyData []byte
		for {
			m, err := b.Receive()
			if err != nil {
				return
			}
			switch m := m.(type) {
			case *pgproto3.Query:
				if strings.HasPrefix(m.String, "copy ") {
					copyData = nil
					b.Send(&pgproto3.CopyInResponse{OverallFormat: 1, ColumnFormatCodes: []uint16{1}})
					break
				}
				tag := "BEGIN"
				if strings.HasPrefix(m.String, "savepoint") {
					tag = "SAVEPOINT"
				}
				b.Send(&pgproto3.CommandComplete{CommandTag: []byte(tag)})
				b.Send(&pgproto3.ReadyForQuery{TxStatus: 'T'})
			case *pgproto3.Parse:
				failedDescribe = strings.Contains(m.Query, "MISSING")
				if failedDescribe {
					b.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: "42P01", Message: "PRIVATE_ERROR", Detail: "PRIVATE_DETAIL", Hint: "PRIVATE_HINT"})
				} else {
					b.Send(&pgproto3.ParseComplete{})
				}
			case *pgproto3.Describe:
				if !failedDescribe {
					b.Send(&pgproto3.ParameterDescription{})
					b.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{{Name: []byte("column"), DataTypeOID: 23, DataTypeSize: 4, TypeModifier: -1}}})
				}
			case *pgproto3.CopyData:
				copyData = append(copyData, m.Data...)
			case *pgproto3.CopyDone:
				rows := 0
				// Binary COPY header: signature, flags, extension length.
				for pos := 19; pos+2 <= len(copyData); {
					columns := int(int16(binary.BigEndian.Uint16(copyData[pos:])))
					pos += 2
					if columns < 0 {
						break
					}
					rows++
					for range columns {
						if pos+4 > len(copyData) {
							return
						}
						n := int(int32(binary.BigEndian.Uint32(copyData[pos:])))
						pos += 4
						if n > 0 {
							pos += n
						}
					}
				}
				b.Send(&pgproto3.CommandComplete{CommandTag: []byte(fmt.Sprintf("COPY %d", rows))})
				b.Send(&pgproto3.ReadyForQuery{TxStatus: 'T'})
			case *pgproto3.CopyFail:
				b.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: "57014", Message: "PRIVATE_ERROR"})
				b.Send(&pgproto3.ReadyForQuery{TxStatus: 'T'})
			case *pgproto3.Sync:
				b.Send(&pgproto3.ReadyForQuery{TxStatus: 'T'})
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
	conn, err := pgx.ConnectConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := conn.Close(context.Background()); err != nil {
			t.Error(err)
		}
		<-done
	})
	return conn
}

func forbiddenBulkCalls(name, source string) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, source, 0)
	if err != nil {
		return nil, err
	}
	imports := map[string]bool{}
	for _, i := range f.Imports {
		path, err := strconv.Unquote(i.Path.Value)
		if err != nil {
			return nil, err
		}
		if path == "github.com/Silo-Server/silo-server/internal/database" {
			alias := "database"
			if i.Name != nil {
				alias = i.Name.Name
			}
			imports[alias] = true
		}
	}
	var bad []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name == "SendBatch" || sel.Sel.Name == "CopyFrom" {
			if id, ok := sel.X.(*ast.Ident); !ok || !imports[id.Name] {
				bad = append(bad, fset.Position(call.Pos()).String())
			}
		}
		if sel.Sel.Name == "PgConn" {
			// PgConn inspection is permitted; returning or executing through it is not.
			allowed := false
			ast.Inspect(f, func(parent ast.Node) bool {
				if binary, ok := parent.(*ast.BinaryExpr); ok && binary.X == call {
					if id, ok := binary.Y.(*ast.Ident); ok && id.Name == "nil" {
						allowed = true
					}
				}
				if outer, ok := parent.(*ast.SelectorExpr); ok && outer.X == call {
					switch outer.Sel.Name {
					case "PID", "TxStatus", "ParameterStatus", "CustomData":
						allowed = true
					}
				}
				return true
			})
			if !allowed {
				bad = append(bad, fset.Position(call.Pos()).String())
			}
		}
		return true
	})
	return bad, nil
}

func TestBulkCallsUseQueryattrWrappers(t *testing.T) {
	for _, root := range []string{"..", "../../cmd"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || filepath.Clean(path) == "../database/postgres_bulk.go" {
				return nil
			}
			source, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			bad, err := forbiddenBulkCalls(path, string(source))
			if err != nil {
				return err
			}
			if len(bad) != 0 {
				t.Errorf("direct bulk/PgConn calls: %v", bad)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestBulkCallGuardRejectsDirectCalls(t *testing.T) {
	for _, expr := range []string{"tx.CopyFrom(ctx, table, columns, source)", "pool.SendBatch(ctx, batch)", "conn.PgConn().Exec(ctx, sql)", "raw := conn.PgConn(); raw.Exec(ctx, sql)"} {
		bad, err := forbiddenBulkCalls("control.go", "package control; func call() { "+expr+" }")
		if err != nil || len(bad) != 1 {
			t.Fatalf("guard(%q)=%v/%v, want one rejected call", expr, bad, err)
		}
	}
	good := `package control; import db "github.com/Silo-Server/silo-server/internal/database"; func call() { db.CopyFrom(ctx, tx, table, columns, source); db.SendBatch(ctx, tx, batch); conn.PgConn().TxStatus() }`
	if bad, err := forbiddenBulkCalls("control.go", good); err != nil || len(bad) != 0 {
		t.Fatalf("guard accepted-source=%v/%v, want no findings", bad, err)
	}
}
