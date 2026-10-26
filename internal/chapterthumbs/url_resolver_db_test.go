package chapterthumbs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/blobgc"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/s3client"
)

type chapterURLTracer struct {
	active atomic.Bool
	count  atomic.Int32
	insert chan struct{}
}

func (tr *chapterURLTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if tr.active.Load() {
		tr.count.Add(1)
		if tr.insert != nil && strings.Contains(data.SQL, "INSERT INTO public.blob_gc_queue") {
			select {
			case tr.insert <- struct{}{}:
			default:
			}
		}
	}
	return ctx
}

func (*chapterURLTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func chapterURLTestPool(t *testing.T, tracer *chapterURLTracer) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if tracer != nil {
		cfg.ConnConfig.Tracer = tracer
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func chapterURLTestFile(t *testing.T, pool *pgxpool.Pool) (int, string) {
	t.Helper()
	var folderID, fileID int
	if err := pool.QueryRow(t.Context(), `INSERT INTO media_folders (type, name, enabled, chapter_thumbnails_enabled)
		VALUES ('movies', $1, true, true) RETURNING id`, fmt.Sprintf("chapter URL %d", time.Now().UnixNano())).Scan(&folderID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `INSERT INTO media_files (media_folder_id, file_path, file_size)
		VALUES ($1, $2, 1) RETURNING id`, folderID, fmt.Sprintf("/chapter-urls/%d.mkv", time.Now().UnixNano())).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=$1`, folderID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM blob_gc_queue WHERE prefix LIKE $1`, fmt.Sprintf("chapter-images/%d/%%", fileID))
	})
	key := chapterThumbnailKey(fileID, 0, 300)
	setChapterURLPath(t, pool, fileID, key)
	return fileID, key
}

func setChapterURLPath(t *testing.T, pool *pgxpool.Pool, fileID int, key string) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `UPDATE media_files SET chapters=$2::jsonb WHERE id=$1`, fileID,
		fmt.Sprintf(`[{"index":0,"start_seconds":0,"end_seconds":30,"thumbnail_path":%q}]`, key)); err != nil {
		t.Fatal(err)
	}
}

type chapterTestURLs struct {
	expiry time.Time
	before func()
}

func (r chapterTestURLs) ResolveURLs(_ context.Context, keys []string) map[string]catalog.ResolvedImageURL {
	if r.before != nil {
		r.before()
	}
	out := make(map[string]catalog.ResolvedImageURL, len(keys))
	for _, key := range keys {
		out[key] = catalog.ResolvedImageURL{URL: "https://cdn.example.test/" + key, ExpiresAt: new(r.expiry)}
	}
	return out
}

func TestRetiredChapterImagesOutliveIssuedURLsDB(t *testing.T) {
	const lifetime = 72 * time.Hour
	cfg := s3client.BucketConfig{Endpoint: "https://s3.example.test", Region: "us-east-1", Bucket: "silo", AccessKey: "k", SecretKey: "s", PathStyle: true}
	tokenCfg := cfg
	tokenCfg.PublicEndpoint, tokenCfg.URLAuth = "https://cdn.example.test", s3client.URLAuthCloudflareToken
	tokenCfg.TokenSecret, tokenCfg.TokenTTL = "secret", int((96 * time.Hour).Seconds())
	for _, delivery := range []struct {
		name     string
		resolver artworkurl.Resolver
	}{
		{"local", artworkurl.NewServerResolver(artworkurl.NewSigner("secret", lifetime))},
		{"s3", artworkurl.NewDirectResolver(blobstore.NewS3(s3client.NewClient(cfg)), lifetime)},
		{"cloudflare token", artworkurl.NewDirectResolver(blobstore.NewS3(s3client.NewClient(tokenCfg)), lifetime)},
	} {
		for _, retirement := range []string{"replace", "delete"} {
			t.Run(delivery.name+"/"+retirement, func(t *testing.T) {
				pool := chapterURLTestPool(t, nil)
				fileID, key := chapterURLTestFile(t, pool)
				resolver := NewURLResolver(pool, delivery.resolver)
				url := resolver.ResolveURLs(t.Context(), []string{key})[key]
				if url.URL == "" || url.ExpiresAt == nil {
					t.Fatalf("chapter URL unavailable: %+v", url)
				}
				// A shorter lifetime on a different replica or after restart must
				// preserve a URL that the earlier resolver already issued.
				short := NewURLResolver(pool, artworkurl.NewServerResolver(artworkurl.NewSigner("secret", time.Hour)))
				if _, ok := artworkurl.ResolveURLFor(t.Context(), short, key, 15*time.Minute); !ok {
					t.Fatal("notifier TTL resolver lost its chapter URL")
				}
				prefix := key
				if retirement == "replace" {
					setChapterURLPath(t, pool, fileID, chapterThumbnailKey(fileID, 0, 320))
					if err := blobgc.NewQueue(pool).Schedule(t.Context(), []string{key}, displacedImageGrace); err != nil {
						t.Fatal(err)
					}
				} else {
					// Keep the old key protected even if chapters have since been
					// cleared. The parent trigger must inherit all child deadlines.
					if _, err := pool.Exec(t.Context(), `UPDATE media_files SET chapters='[]' WHERE id=$1`, fileID); err != nil {
						t.Fatal(err)
					}
					if _, err := pool.Exec(t.Context(), `DELETE FROM media_files WHERE id=$1`, fileID); err != nil {
						t.Fatal(err)
					}
					prefix = fmt.Sprintf("chapter-images/%d/", fileID)
				}
				var notBefore time.Time
				if err := pool.QueryRow(t.Context(), `SELECT not_before FROM blob_gc_queue WHERE prefix=$1`, prefix).Scan(&notBefore); err != nil {
					t.Fatal(err)
				}
				if notBefore.Before(*url.ExpiresAt) {
					t.Fatalf("image retires at %v before issued URL expires at %v", notBefore, *url.ExpiresAt)
				}
				if _, ok := resolver.ResolveURLs(t.Context(), []string{key})[key]; ok {
					t.Fatal("stale chapter image acquired a new URL")
				}
			})
		}
	}
}

func TestChapterURLWithholdsImageRetiredWhileSigningDB(t *testing.T) {
	for _, retirement := range []string{"replace", "delete"} {
		t.Run(retirement, func(t *testing.T) {
			pool := chapterURLTestPool(t, nil)
			fileID, key := chapterURLTestFile(t, pool)
			resolver := NewURLResolver(pool, chapterTestURLs{expiry: time.Now().Add(72 * time.Hour), before: func() {
				if retirement == "replace" {
					setChapterURLPath(t, pool, fileID, chapterThumbnailKey(fileID, 0, 320))
				} else if _, err := pool.Exec(t.Context(), `DELETE FROM media_files WHERE id=$1`, fileID); err != nil {
					t.Fatal(err)
				}
			}})
			if got := resolver.ResolveURLs(t.Context(), []string{key}); len(got) != 0 {
				t.Fatalf("exposed a stale image after signing: %+v", got)
			}
		})
	}
}

func TestChapterURLProtectionSerializesFileDeletionDB(t *testing.T) {
	tracer := &chapterURLTracer{insert: make(chan struct{}, 1)}
	pool := chapterURLTestPool(t, tracer)
	fileID, key := chapterURLTestFile(t, pool)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := blobgc.NewQueue(pool).Schedule(ctx, []string{key}, time.Hour); err != nil {
		t.Fatal(err)
	}
	queueTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = queueTx.Rollback(context.Background()) }()
	if _, err := queueTx.Exec(ctx, `SELECT prefix FROM blob_gc_queue WHERE prefix=$1 FOR UPDATE`, key); err != nil {
		t.Fatal(err)
	}
	tracer.active.Store(true)
	done := make(chan map[string]catalog.ResolvedImageURL, 1)
	resolver := NewURLResolver(pool, chapterTestURLs{expiry: time.Now().Add(72 * time.Hour)})
	go func() { done <- resolver.ResolveURLs(ctx, []string{key}) }()
	select {
	case <-tracer.insert:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// The URL transaction already holds the file SHARE lock while its queue
	// write waits. Deletion and chapter update cannot overtake protection.
	_, err = pool.Exec(ctx, `SELECT id FROM media_files WHERE id=$1 FOR UPDATE NOWAIT`, fileID)
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	if !ok || pgErr.Code != "55P03" {
		t.Fatalf("file was not locked against deletion: %v", err)
	}
	select {
	case got := <-done:
		t.Fatalf("exposed URL before protection committed: %+v", got)
	default:
	}
	if err := queueTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var url catalog.ResolvedImageURL
	select {
	case got := <-done:
		url = got[key]
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if url.URL == "" || url.ExpiresAt == nil {
		t.Fatalf("URL unavailable after protection: %+v", url)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM media_files WHERE id=$1`, fileID); err != nil {
		t.Fatal(err)
	}
	var deadline time.Time
	if err := pool.QueryRow(ctx, `SELECT not_before FROM blob_gc_queue WHERE prefix=$1`, fmt.Sprintf("chapter-images/%d/", fileID)).Scan(&deadline); err != nil {
		t.Fatal(err)
	}
	if deadline.Before(*url.ExpiresAt) {
		t.Fatalf("file prefix expires at %v before issued URL %v", deadline, *url.ExpiresAt)
	}
}

func TestChapterURLProtectionBatchesFilesDB(t *testing.T) {
	tracer := &chapterURLTracer{}
	pool := chapterURLTestPool(t, tracer)
	for _, count := range []int{1, 100} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			var keys []string
			for range count {
				_, key := chapterURLTestFile(t, pool)
				keys = append(keys, key)
			}
			// Reverse order and duplicates exercise canonical lock ordering;
			// unrelated artwork must keep its URL without another transaction.
			for i, j := 0, len(keys)-1; i < j; i, j = i+1, j-1 {
				keys[i], keys[j] = keys[j], keys[i]
			}
			keys = append(keys, keys[0], "tmdb/movies/poster.webp")
			resolver := NewURLResolver(pool, chapterTestURLs{expiry: time.Now().Add(72 * time.Hour).Truncate(time.Microsecond)})
			for range 2 {
				tracer.count.Store(0)
				tracer.active.Store(true)
				got := resolver.ResolveURLs(t.Context(), keys)
				tracer.active.Store(false)
				if len(got) != count+1 || tracer.count.Load() != 4 {
					t.Fatalf("%d files: resolved %d URLs in %d statements; want %d URLs in 4 statements", count, len(got), tracer.count.Load(), count+1)
				}
			}
		})
	}
}

func TestChapterURLsRequireExpiryAndDatabase(t *testing.T) {
	key := "chapter-images/42/0/w300.webp"
	resolver := NewURLResolver(nil, chapterTestURLs{expiry: time.Now().Add(time.Hour)})
	got := resolver.ResolveURLs(t.Context(), []string{key, "tmdb/movies/poster.webp"})
	if len(got) != 1 || got["tmdb/movies/poster.webp"].URL == "" {
		t.Fatalf("chapter URL exposed without durable protection: %+v", got)
	}
}

type chapterReuseStore struct {
	blobstore.Store
	put chan struct{}
}

func (s chapterReuseStore) Put(ctx context.Context, key string, data []byte) error {
	close(s.put)
	return s.Store.Put(ctx, key, data)
}

func TestChapterImageReusePreservesIssuedURLProtectionDB(t *testing.T) {
	pool := chapterURLTestPool(t, nil)
	fileID, _ := chapterURLTestFile(t, pool)
	queue := blobgc.NewQueue(pool)
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{store: store, blobQueue: queue}
	frame := testFrameJPEG(t, 320, 180)
	key, _, err := service.uploadChapterThumbnail(t.Context(), fileID, 0, frame, 300)
	if err != nil {
		t.Fatal(err)
	}
	setChapterURLPath(t, pool, fileID, key)
	resolver := NewURLResolver(pool, chapterTestURLs{expiry: time.Now().Add(72 * time.Hour)})
	url := resolver.ResolveURLs(t.Context(), []string{key})[key]
	if url.ExpiresAt == nil {
		t.Fatal("chapter URL unavailable")
	}
	setChapterURLPath(t, pool, fileID, chapterThumbnailKey(fileID, 0, 320))
	if err := queue.Schedule(t.Context(), []string{key}, displacedImageGrace); err != nil {
		t.Fatal(err)
	}
	if reused, _, err := service.uploadChapterThumbnail(t.Context(), fileID, 0, frame, 300); err != nil || reused != key {
		t.Fatalf("reuse chapter image: %q, %v", reused, err)
	}
	setChapterURLPath(t, pool, fileID, key)
	if _, err := pool.Exec(t.Context(), `DELETE FROM media_files WHERE id=$1`, fileID); err != nil {
		t.Fatal(err)
	}
	var deadline time.Time
	if err := pool.QueryRow(t.Context(), `SELECT not_before FROM blob_gc_queue WHERE prefix=$1`, fmt.Sprintf("chapter-images/%d/", fileID)).Scan(&deadline); err != nil {
		t.Fatal(err)
	}
	if deadline.Before(*url.ExpiresAt) {
		t.Fatalf("reused width lost issued URL protection: parent expires %v before %v", deadline, *url.ExpiresAt)
	}
}

func TestChapterImageReuseWaitsForCollectorDB(t *testing.T) {
	tracer := &chapterURLTracer{insert: make(chan struct{}, 1)}
	pool := chapterURLTestPool(t, tracer)
	fileID, _ := chapterURLTestFile(t, pool)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	queue := blobgc.NewQueue(pool)
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	frame := testFrameJPEG(t, 320, 180)
	initial := &Service{store: store, blobQueue: queue}
	key, _, err := initial.uploadChapterThumbnail(ctx, fileID, 0, frame, 300)
	if err != nil {
		t.Fatal(err)
	}
	setChapterURLPath(t, pool, fileID, key)
	if _, err := pool.Exec(ctx, `UPDATE blob_gc_queue SET not_before=now()-interval '1 hour' WHERE prefix=$1`, key); err != nil {
		t.Fatal(err)
	}
	collectorTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = collectorTx.Rollback(context.Background()) }()
	if _, err := collectorTx.Exec(ctx, `SELECT prefix FROM blob_gc_queue WHERE prefix=$1 FOR UPDATE`, key); err != nil {
		t.Fatal(err)
	}
	put := make(chan struct{})
	service := &Service{store: chapterReuseStore{Store: store, put: put}, blobQueue: queue}
	tracer.active.Store(true)
	done := make(chan error, 1)
	go func() {
		_, _, err := service.uploadChapterThumbnail(ctx, fileID, 0, frame, 300)
		done <- err
	}()
	select {
	case <-tracer.insert:
	case err := <-done:
		t.Fatalf("upload returned before protecting reuse: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-put:
		t.Fatal("new image written before the collector's deletion finished")
	default:
	}
	if _, err := store.DeletePrefix(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, err := collectorTx.Exec(ctx, `DELETE FROM blob_gc_queue WHERE prefix=$1`, key); err != nil {
		t.Fatal(err)
	}
	if err := collectorTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := store.Stat(ctx, key); err != nil {
		t.Fatalf("new image disappeared after the collector completed: %v", err)
	}
}
