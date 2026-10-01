package catalog

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

type playableTargetBenchmarkProgressStore struct {
	delegate PlayableTargetProgressStore
	queries  int
	ids      int
}

func (s *playableTargetBenchmarkProgressStore) ListProgressByMediaItems(ctx context.Context, profile string, ids []string) (map[string]userstore.WatchProgress, error) {
	s.queries++
	s.ids += len(ids)
	return s.delegate.ListProgressByMediaItems(ctx, profile, ids)
}

// This benchmark requires a disposable migrated database. It reports progress
// query counts alongside timings so removed work is visible even when
// connection latency varies between runs.
func BenchmarkPlayableTargetReadQueries(b *testing.B) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		b.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(b.Context(), dsn)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(pool.Close)
	prefix := fmt.Sprintf("catalog-perf-%d", time.Now().UnixNano())
	movieIDs := make([]string, 100)
	for i := range movieIDs {
		movieIDs[i] = fmt.Sprintf("%s-movie-%03d", prefix, i)
	}
	seriesID := prefix + "-series"
	profileID := prefix + "-profile"
	var userID, folderID int
	if err := pool.QueryRow(b.Context(), `INSERT INTO users (email, username, role, enabled)
		VALUES ($1, $1, 'user', TRUE) RETURNING id`, prefix+"@example.invalid").Scan(&userID); err != nil {
		b.Fatal(err)
	}
	if err := pool.QueryRow(b.Context(), `INSERT INTO media_folders (type, name, enabled)
		VALUES ('mixed', $1, TRUE) RETURNING id`, prefix).Scan(&folderID); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, append(movieIDs, seriesID))
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
	})
	statements := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO user_profiles (id, user_id, name) VALUES ($1, $2, 'Benchmark')`, []any{profileID, userID}},
		{`INSERT INTO media_items (content_id, type, title, status, genres)
			SELECT id, 'movie', id, 'matched', '{}'::text[] FROM unnest($1::text[]) id`, []any{movieIDs}},
		{`INSERT INTO media_items (content_id, type, title, status, genres)
			VALUES ($1, 'series', $1, 'matched', '{}')`, []any{seriesID}},
		{`INSERT INTO media_files (content_id, media_folder_id, file_path)
			SELECT id, $2, id || '.mkv' FROM unnest($1::text[]) id`, []any{movieIDs, folderID}},
		{`INSERT INTO episodes (content_id, series_id, season_number, episode_number, title)
			SELECT $1 || '-episode-' || n, $1, 1, n, 'Episode ' || n FROM generate_series(1, 200) n`, []any{seriesID}},
		{`INSERT INTO media_files (episode_id, media_folder_id, file_path)
			SELECT content_id, $2, content_id || '.mkv' FROM episodes WHERE series_id = $1`, []any{seriesID, folderID}},
	}
	for _, statement := range statements {
		if _, err := pool.Exec(b.Context(), statement.sql, statement.args...); err != nil {
			b.Fatal(err)
		}
	}
	// Give both benchmark binaries plans based on the fixture cardinalities,
	// rather than empty-table estimates left over from initial migrations.
	if _, err := pool.Exec(b.Context(), `ANALYZE media_items; ANALYZE episodes; ANALYZE media_files; ANALYZE media_folders`); err != nil {
		b.Fatal(err)
	}
	delegate, err := pgstore.NewPostgresProvider(pool).ForUser(b.Context(), userID)
	if err != nil {
		b.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		items []PlayableTargetInput
	}{
		{name: "movie-targets-100", items: func() []PlayableTargetInput {
			items := make([]PlayableTargetInput, len(movieIDs))
			for i, id := range movieIDs {
				items[i] = PlayableTargetInput{ContentID: id, Type: "movie"}
			}
			return items
		}()},
		{name: "hinted-series-200-episodes", items: []PlayableTargetInput{{ContentID: seriesID, Type: "series", PreferredContentID: seriesID + "-episode-100"}}},
		{name: "unhinted-series-200-episodes", items: []PlayableTargetInput{{ContentID: seriesID, Type: "series"}}},
	} {
		b.Run(tc.name, func(b *testing.B) {
			store := &playableTargetBenchmarkProgressStore{delegate: delegate}
			resolver := NewPlayableTargetResolver(pool)
			query := PlayableTargetQuery{UserID: userID, ProfileID: profileID, Items: tc.items, ProgressStore: store,
				Access: AccessFilter{AllowedLibraryIDs: []int{folderID}}}
			for b.Loop() {
				if _, err := resolver.Resolve(b.Context(), query); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(store.queries)/float64(b.N), "progress-queries/op")
			b.ReportMetric(float64(store.ids)/float64(b.N), "progress-IDs/op")
		})
	}
}
