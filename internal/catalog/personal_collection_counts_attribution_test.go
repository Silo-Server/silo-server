package catalog

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/database/queryattr"
)

func TestPersonalCollectionLabelCoversBatchAndRetries(t *testing.T) {
	s := queryattr.NewStore()
	pool := collectionAttributionPool(t, queryattr.Tracer{Store: s})
	for n := 1; n <= queryattr.ShapeThreshold+1; n++ {
		batch := make([]collectionCountStatement, n)
		for i := range batch {
			batch[i] = collectionCountStatement{id: "fixture", sql: "SELECT 7"}
		}
		counts, failures := countPersonalCollectionBatch(t.Context(), pool, batch)
		if len(counts) != 1 || counts["fixture"] != 7 || len(failures) != 0 {
			t.Fatalf("batch %d counts=%v failures=%v, want fixture=7 without failure", n, counts, failures)
		}
	}
	want := uint64((queryattr.ShapeThreshold + 1) * (queryattr.ShapeThreshold + 4) / 2)
	var labeled uint64
	for _, row := range s.Snapshot() {
		if row.Identity.Caller.Function == "catalog.personal_collection_counts" {
			labeled += row.Counters.Completed
		}
	}
	if labeled != want {
		t.Fatalf("labeled statements=%d, want all %d initial/retry statements", labeled, want)
	}
	warnings := s.DrainWarnings()
	if len(warnings) != 1 || warnings[0].Caller != "catalog.personal_collection_counts" || warnings[0].Kind != "shapes" {
		t.Fatalf("warnings=%v, want one shape warning naming the label", warnings)
	}
	if next := s.DrainWarnings(); len(next) != 0 {
		t.Fatalf("repeat warnings=%v, want none", next)
	}
}

func collectionAttributionPool(t *testing.T, tr pgx.QueryTracer) *pgxpool.Pool {
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
			for key, value := range map[string]string{"server_version": "17.0", "client_encoding": "UTF8", "standard_conforming_strings": "on"} {
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
				switch m := m.(type) {
				case *pgproto3.Terminate:
					return
				case *pgproto3.Query:
					if strings.Contains(m.String, "::text") {
						b.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: "42601", Message: "PRIVATE_ERROR", Detail: "PRIVATE_DETAIL", Hint: "PRIVATE_HINT"})
					} else {
						b.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{{Name: []byte("count"), DataTypeOID: 23, DataTypeSize: 4, TypeModifier: -1}}})
						b.Send(&pgproto3.DataRow{Values: [][]byte{[]byte("7")}})
						b.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")})
					}
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
