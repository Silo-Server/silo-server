package extrastills

import (
	"context"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type pendingStillsQueryTrace struct {
	ran atomic.Bool
}

func (tr *pendingStillsQueryTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "FROM media_extras") {
		tr.ran.Store(true)
	}
	return ctx
}

func (*pendingStillsQueryTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestGenerateLeavesOneConnectionPoolAvailableDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 1
	trace := &pendingStillsQueryTrace{}
	config.ConnConfig.Tracer = trace
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	// Before the fix, Generate holds the pool's only connection for its lock
	// and its first catalog query waits on that same connection until this deadline.
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	generator := NewGenerator(pool, nil, nil, nil)
	extras, posters, err := generator.Generate(ctx, 0)
	if err != nil || extras != 0 || posters != 0 {
		t.Fatalf("Generate() = %d, %d, %v; want no work and no error", extras, posters, err)
	}
	if !trace.ran.Load() {
		t.Fatal("Generate() returned without querying pending frame stills")
	}
}
