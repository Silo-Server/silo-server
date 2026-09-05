package catalog

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type previewCountTracer struct{ counts int }

func (tracer *previewCountTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "SELECT COUNT(*) FROM (") {
		tracer.counts++
	}
	return ctx
}

func (*previewCountTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestQueryExecutorTerminalPageTotals(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	tracer := &previewCountTracer{}
	config.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	ids := make([]string, 23)
	for i := range ids {
		ids[i] = fmt.Sprintf("preview-total-%d-%02d", time.Now().UnixNano(), i)
	}
	_, err = pool.Exec(t.Context(), `INSERT INTO media_items (content_id, type, title, status, genres)
		SELECT id, 'movie', id, 'matched', '{}'::text[] FROM unnest($1::text[]) id`, ids)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = ANY($1)`, ids)
	})
	executor := &QueryExecutor{Pool: pool}
	for _, tc := range []struct {
		name                            string
		limit, offset, cap, total, size int
		counts                          int
		empty, hasMore, skipTotal       bool
	}{
		{name: "full first page counts", limit: 20, total: 23, size: 20, counts: 1, hasMore: true},
		{name: "short first page", limit: 30, total: 23, size: 23},
		{name: "short final page", limit: 20, offset: 20, total: 23, size: 3},
		{name: "exactly full final page has no extra row", limit: 23, total: 23, size: 23},
		{name: "empty first page", limit: 20, empty: true},
		{name: "empty deep page counts", limit: 20, offset: 40, total: 23, counts: 1},
		{name: "empty exact end counts", limit: 20, offset: 23, total: 23, counts: 1},
		{name: "cap on first page", limit: 20, cap: 5, total: 5, size: 5},
		{name: "cap on later page", limit: 20, offset: 20, cap: 22, total: 22, size: 2},
		{name: "terminal before cap", limit: 20, offset: 20, cap: 25, total: 23, size: 3},
		{name: "offset at cap counts", limit: 20, offset: 5, cap: 5, total: 5, counts: 1},
		{name: "offset beyond cap counts", limit: 20, offset: 30, cap: 5, total: 5, counts: 1},
		{name: "short catalog before cap counts beyond end", limit: 20, offset: 25, cap: 30, total: 23, counts: 1},
		{name: "skip total with extra row", limit: 20, size: 20, hasMore: true, skipTotal: true},
		{name: "skip total final page", limit: 20, offset: 20, size: 3, skipTotal: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			definition := QueryDefinition{Sort: QuerySort{Field: "title", Order: "asc"}}
			if tc.cap > 0 {
				definition.Limit = new(tc.cap)
			}
			access := AccessFilter{AllowedContentIDs: ids}
			if tc.empty {
				access.AllowedContentIDs = []string{}
			}
			tracer.counts = 0
			items, total, hasMore, err := executor.PreviewPage(t.Context(), definition, access, tc.limit, tc.offset, !tc.skipTotal)
			if err != nil {
				t.Fatal(err)
			}
			if len(items) != tc.size || total != tc.total || hasMore != tc.hasMore {
				t.Fatalf("page = (size %d, total %d, hasMore %v), want (%d, %d, %v)", len(items), total, hasMore, tc.size, tc.total, tc.hasMore)
			}
			if tracer.counts != tc.counts {
				t.Fatalf("COUNT queries = %d, want %d", tracer.counts, tc.counts)
			}
		})
	}
}
