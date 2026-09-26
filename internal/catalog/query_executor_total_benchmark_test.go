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

type previewTotalBenchmarkTracer struct{ counts int }

func (tracer *previewTotalBenchmarkTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "SELECT COUNT(*)") {
		tracer.counts++
	}
	return ctx
}

func (*previewTotalBenchmarkTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {
}

// This benchmark requires a disposable migrated database. Only the 100 movies
// visible to these preview requests are needed; playback fixtures are separate.
func BenchmarkPreviewPageExactTotalQueries(b *testing.B) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		b.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		b.Fatal(err)
	}
	tracer := &previewTotalBenchmarkTracer{}
	config.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(b.Context(), config)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(pool.Close)
	prefix := fmt.Sprintf("preview-total-perf-%d", time.Now().UnixNano())
	movieIDs := make([]string, 100)
	for i := range movieIDs {
		movieIDs[i] = fmt.Sprintf("%s-movie-%03d", prefix, i)
	}
	if _, err := pool.Exec(b.Context(), `INSERT INTO media_items (content_id, type, title, status, genres)
		SELECT id, 'movie', id, 'matched', '{}'::text[] FROM unnest($1::text[]) id`, movieIDs); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = ANY($1)`, movieIDs)
	})
	if _, err := pool.Exec(b.Context(), `ANALYZE media_items`); err != nil {
		b.Fatal(err)
	}
	for _, tc := range []struct {
		name               string
		limit, offset, cap int
		empty              bool
	}{
		{name: "short-first-page", limit: 120},
		{name: "short-final-page", limit: 20, offset: 90},
		{name: "empty-first-page", limit: 20, empty: true},
		{name: "capped-first-page", limit: 20, cap: 5},
		{name: "full-first-page", limit: 20},
	} {
		b.Run(tc.name, func(b *testing.B) {
			executor := &QueryExecutor{Pool: pool}
			definition := QueryDefinition{Sort: QuerySort{Field: "title", Order: "asc"}}
			if tc.cap > 0 {
				definition.Limit = new(tc.cap)
			}
			access := AccessFilter{AllowedContentIDs: movieIDs}
			if tc.empty {
				access.AllowedContentIDs = []string{}
			}
			wantTotal := len(access.AllowedContentIDs)
			if tc.cap > 0 {
				wantTotal = min(wantTotal, tc.cap)
			}
			items, total, _, err := executor.PreviewPage(b.Context(), definition, access, tc.limit, tc.offset, true)
			if err != nil || total != wantTotal || len(items) != min(tc.limit, max(0, wantTotal-tc.offset)) {
				b.Fatalf("fixture preview: items=%d total=%d error=%v, want total=%d", len(items), total, err, wantTotal)
			}
			tracer.counts = 0
			for b.Loop() {
				if _, _, _, err := executor.PreviewPage(b.Context(), definition, access, tc.limit, tc.offset, true); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(tracer.counts)/float64(b.N), "count-queries/op")
		})
	}
}
