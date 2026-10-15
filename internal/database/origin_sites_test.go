package database_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/activitylog"
	"github.com/Silo-Server/silo-server/internal/database/queryattr"
	"github.com/Silo-Server/silo-server/internal/opslog"
)

type cleanupScope struct{ started int }

func (s *cleanupScope) Start(_ queryattr.Identity, _ queryattr.Kind, n uint64, _ queryattr.Sizes, _ int64) {
	s.started += int(n)
}
func (*cleanupScope) Finish(queryattr.Observation) {}

type cleanupSettings struct{ pool *pgxpool.Pool }

func (s cleanupSettings) Get(ctx context.Context, key string) (string, error) {
	_, err := s.pool.Exec(ctx, "SELECT 1")
	if key == "opslog.bucket_policies" {
		return "[]", err
	}
	return "", err
}
func (s cleanupSettings) Set(context.Context, string, string) error { return nil }

type cleanupPartitions struct {
	pool  *pgxpool.Pool
	calls int
}

func (p *cleanupPartitions) EnsureFuturePartitions(ctx context.Context) error {
	p.calls++
	_, err := p.pool.Exec(ctx, "SELECT 1")
	return err
}
func (p *cleanupPartitions) DropExpiredPartitions(ctx context.Context, _ time.Time) ([]string, error) {
	p.calls++
	if _, err := p.pool.Exec(ctx, "SELECT 1"); err != nil {
		return nil, err
	}
	return nil, errors.New("fixture partition cleanup unavailable")
}
func (p *cleanupPartitions) DeleteExpiredRowsFromDefault(ctx context.Context, _ time.Time) (int64, error) {
	p.calls++
	_, err := p.pool.Exec(ctx, "DELETE FROM telemetry_fixture")
	return 0, err
}

func TestCleanupMarkerSites(t *testing.T) {
	for _, name := range []string{"opslog.CleanupOnce", "activitylog.CleanupOnce"} {
		t.Run(name, func(t *testing.T) {
			s := queryattr.NewStore()
			pool := cleanupMemoryPool(t, queryattr.Tracer{Store: s})
			p := &cleanupScope{}
			parent := queryattr.WithOwnership(t.Context(), queryattr.RequestOrigin, p)
			pm := &cleanupPartitions{pool: pool}
			var n int64
			if name == "opslog.CleanupOnce" {
				n = opslog.CleanupOnce(parent, pool, cleanupSettings{pool}, pm)
			} else {
				n = activitylog.CleanupOnce(parent, pool, cleanupSettings{pool}, pm)
			}
			if n != 0 || pm.calls != 3 {
				t.Fatalf("deleted=%d partition calls=%d, want 0/3", n, pm.calls)
			}
			got := s.Totals()
			if got.Started < 5 || got.Completed != got.Started || p.started != 0 {
				t.Fatalf("totals=%+v request starts=%d, want completed internal settings/partitions/fallback", got, p.started)
			}
			for _, row := range s.Snapshot() {
				if row.Identity.Caller.Function == "telemetry_persistence" && row.Counters.Completed != got.Completed {
					t.Fatalf("persistence=%d, want %d", row.Counters.Completed, got.Completed)
				}
				if (row.Identity.Caller.Function == "request" || row.Identity.Caller.Function == "background") && row.Counters.Started != 0 {
					t.Fatalf("unexpected origin=%+v", row)
				}
			}
			// The caller's later read/progress work keeps its original ownership.
			if _, err := pool.Exec(parent, "SELECT 1"); err != nil {
				t.Fatal(err)
			}
			if p.started != 1 {
				t.Fatalf("caller starts=%d, want 1 after cleanup returns", p.started)
			}
		})
	}
}

func cleanupMemoryPool(t *testing.T, tr pgx.QueryTracer) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig("host=127.0.0.1 user=qa sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	cfg.ConnConfig.Tracer = tr
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
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
			b.Send(&pgproto3.ParameterStatus{Name: "server_version", Value: "17.0"})
			b.Send(&pgproto3.ParameterStatus{Name: "client_encoding", Value: "UTF8"})
			b.Send(&pgproto3.ParameterStatus{Name: "standard_conforming_strings", Value: "on"})
			b.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
			if b.Flush() != nil {
				return
			}
			for {
				m, err := b.Receive()
				if err != nil {
					return
				}
				switch m := m.(type) {
				case *pgproto3.Terminate:
					return
				case *pgproto3.Query:
					tag := "DELETE 0"
					if strings.HasPrefix(strings.TrimSpace(m.String), "SELECT") {
						tag = "SELECT 0"
						b.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{{Name: []byte("value"), DataTypeOID: 20, DataTypeSize: 8, TypeModifier: -1}}})
					}
					b.Send(&pgproto3.CommandComplete{CommandTag: []byte(tag)})
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
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close(); wg.Wait() })
	return pool
}
