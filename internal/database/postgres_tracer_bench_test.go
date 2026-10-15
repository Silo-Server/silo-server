package database

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"

	"github.com/Silo-Server/silo-server/internal/database/queryattr"
)

// memoryPool uses pgxpool, pgx and the real coarse tracer over memory pipes.
// It contacts no PostgreSQL server and has no operating-system socket.
func memoryPool(tb testing.TB, on bool, s *queryattr.Store, override ...pgx.QueryTracer) *pgxpool.Pool {
	tb.Helper()
	cfg, err := pgxpool.ParseConfig("host=127.0.0.1 user=qa sslmode=disable")
	if err != nil {
		tb.Fatal(err)
	}
	cfg.MaxConns = 1
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	configurePostgresTracing(cfg, "checks", on)
	if on {
		tr := queryattr.Tracer{Store: s}
		cfg.ConnConfig.Tracer = poolTracer{coarse: postgresTracer{role: "checks"}, attribution: tr}
		cfg.BeforeClose = tr.BeforeClose
	}
	if len(override) > 0 {
		cfg.ConnConfig.Tracer = override[0]
	}
	var wg sync.WaitGroup
	cfg.ConnConfig.DialFunc = func(context.Context, string, string) (net.Conn, error) {
		client, server := net.Pipe()
		wg.Go(func() {
			defer func() { _ = server.Close() }()
			b := pgproto3.NewBackend(server, server)
			if _, err := b.ReceiveStartupMessage(); err != nil {
				return
			}
			b.Send(&pgproto3.AuthenticationOk{})
			for key, value := range map[string]string{"server_version": "17.0", "standard_conforming_strings": "on", "client_encoding": "UTF8"} {
				b.Send(&pgproto3.ParameterStatus{Name: key, Value: value})
			}
			b.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
			if b.Flush() != nil {
				return
			}
			for {
				m, err := b.Receive()
				if err != nil {
					return
				}
				switch m.(type) {
				case *pgproto3.Terminate:
					return
				case *pgproto3.Query:
					b.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{{Name: []byte("value"), DataTypeOID: 23, DataTypeSize: 4, TypeModifier: -1}}})
					b.Send(&pgproto3.DataRow{Values: [][]byte{[]byte("1")}})
					b.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")})
					b.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
				default:
					return
				}
				if b.Flush() != nil {
					return
				}
			}
		})
		return client, nil
	}
	pool, err := pgxpool.NewWithConfig(tb.Context(), cfg)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { pool.Close(); wg.Wait() })
	return pool
}

func BenchmarkQueryAttributionFullStack(b *testing.B) {
	queryattr.Initialize()
	nonRecordingPostgresSDK(b)
	for _, on := range []bool{false, true} {
		b.Run(fmt.Sprintf("enabled_%t", on), func(b *testing.B) {
			pool := memoryPool(b, on, queryattr.NewStore())
			// This database-package fixture is skipped by caller resolution. A
			// reviewed label exercises admitted identities and real scope entries.
			ctx := queryattr.WithLabel(b.Context(), queryattr.LabelPersonalCollectionCounts)
			var n int
			if err := pool.QueryRow(ctx, "SELECT 1").Scan(&n); err != nil {
				b.Fatal(err)
			}
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx, span := otel.Tracer("silo/checks").Start(r.Context(), "fixture")
				defer span.End()
				if on {
					var scope *queryattr.RequestScope
					ctx, scope = queryattr.StartRequest(ctx)
					defer scope.Seal()
				}
				var n int
				if err := pool.QueryRow(ctx, "SELECT 1").Scan(&n); err != nil {
					b.Error(err)
					return
				}
				if _, err := fmt.Fprint(w, n); err != nil {
					b.Error(err)
				}
			})
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/fixture", nil)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != http.StatusOK {
					b.Fatal(w.Code)
				}
			}
		})
	}
}

func BenchmarkQueryAttributionPaths(b *testing.B) {
	queryattr.Initialize()
	nonRecordingPostgresSDK(b)
	ctx := queryattr.WithLabel(b.Context(), queryattr.LabelPersonalCollectionCounts)
	ctx, scope := queryattr.StartRequest(ctx)
	defer scope.Seal()
	coarse := postgresTracer{role: "checks"}
	tr := poolTracer{coarse: coarse, attribution: queryattr.Tracer{Store: queryattr.NewStore()}}
	d := pgx.TraceQueryStartData{SQL: "SELECT $1", Args: []any{1}}
	b.Run("disabled", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			c := coarse.TraceQueryStart(ctx, nil, d)
			coarse.TraceQueryEnd(c, nil, pgx.TraceQueryEndData{})
		}
	})
	b.Run("cached", func(b *testing.B) {
		c := tr.TraceQueryStart(ctx, nil, d)
		tr.TraceQueryEnd(c, nil, pgx.TraceQueryEndData{})
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			c := tr.TraceQueryStart(ctx, nil, d)
			tr.TraceQueryEnd(c, nil, pgx.TraceQueryEndData{})
		}
	})
	b.Run("first_seen", func(b *testing.B) {
		statements := make([]string, queryattr.IdentityCap)
		for i := range statements {
			statements[i] = fmt.Sprintf("SELECT column%d", i)
		}
		local := tr
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if i%queryattr.IdentityCap == 0 {
				local.attribution.Store = queryattr.NewStore()
			}
			c := local.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: statements[i%len(statements)]})
			local.TraceQueryEnd(c, nil, pgx.TraceQueryEndData{})
		}
	})
	b.Run("parallel_many", func(b *testing.B) {
		ds := make([]pgx.TraceQueryStartData, 128)
		for i := range ds {
			ds[i].SQL = fmt.Sprintf("SELECT column%d", i)
			c := tr.TraceQueryStart(ctx, nil, ds[i])
			tr.TraceQueryEnd(c, nil, pgx.TraceQueryEndData{})
		}
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				c := tr.TraceQueryStart(ctx, nil, ds[i%len(ds)])
				tr.TraceQueryEnd(c, nil, pgx.TraceQueryEndData{})
				i++
			}
		})
	})
	b.Run("parallel_hot", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				c := tr.TraceQueryStart(ctx, nil, d)
				tr.TraceQueryEnd(c, nil, pgx.TraceQueryEndData{})
			}
		})
	})
	b.Run("largest_static", func(b *testing.B) {
		// The source inventory identifies this file's repair-query template as
		// the largest static SQL literal. Input-dependent builders have no maximum.
		f, err := parser.ParseFile(token.NewFileSet(), "../metadata/image_cache_job_repo.go", nil, 0)
		if err != nil {
			b.Fatal(err)
		}
		var largest string
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err == nil && len(s) > len(largest) && strings.HasPrefix(strings.TrimSpace(s), "WITH all_candidates AS") {
				largest = s
			}
			return true
		})
		if largest == "" {
			b.Fatal("largest SQL template not found")
		}
		data := pgx.TraceQueryStartData{SQL: largest}
		c := tr.TraceQueryStart(ctx, nil, data)
		tr.TraceQueryEnd(c, nil, pgx.TraceQueryEndData{})
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			c := tr.TraceQueryStart(ctx, nil, data)
			tr.TraceQueryEnd(c, nil, pgx.TraceQueryEndData{})
		}
		b.ReportMetric(float64(len(largest)), "query_bytes")
	})
}
