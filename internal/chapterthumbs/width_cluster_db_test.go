package chapterthumbs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

type unavailableChapterLockRepo struct {
	testFileRepo
	err error
}

type mutableChapterWidth struct {
	width atomic.Int32
	fail  atomic.Bool
}

func (s *mutableChapterWidth) Get(_ context.Context, key string) (string, error) {
	if key != config.PreviewImageWidthSettingKey {
		return "", nil
	}
	if s.fail.Load() {
		return "", errors.New("settings unavailable")
	}
	return fmt.Sprint(s.width.Load()), nil
}

func TestWidthChangeDuringFirstChapterExtractionDB(t *testing.T) {
	pool := chapterURLTestPool(t, nil)
	fileID, _ := chapterURLTestFile(t, pool)
	setChapterURLPath(t, pool, fileID, "")
	settings := &mutableChapterWidth{}
	settings.width.Store(300)
	entered, resume := make(chan struct{}), make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(resume) }) })
	var extracts atomic.Int32
	var folderID int
	if err := pool.QueryRow(t.Context(), `SELECT media_folder_id FROM media_files WHERE id=$1`, fileID).Scan(&folderID); err != nil {
		t.Fatal(err)
	}
	service := &Service{
		fileRepo:       scanner.NewFileRepository(pool),
		folderRepo:     &testFolderRepo{folder: &models.MediaFolder{ID: folderID, Enabled: true, ChapterThumbnailsEnabled: true}},
		settings:       settings,
		queuedNormal:   map[int]ChapterThumbnailRequest{},
		queuedPriority: map[int]ChapterThumbnailRequest{},
		inProgress:     map[int]struct{}{fileID: {}},
		extractFrameFunc: func(ctx context.Context, _ *models.MediaFile, _ float64, _ string) ([]byte, string, error) {
			if extracts.Add(1) == 1 {
				close(entered)
				select {
				case <-resume:
				case <-ctx.Done():
					return nil, "", ctx.Err()
				}
			}
			return []byte("frame"), "", nil
		},
		uploadChapterThumbnailFunc: func(_ context.Context, fileID, index int, _ []byte) (string, string, error) {
			width := 320
			if extracts.Load() == 1 {
				width = 300
			}
			return chapterThumbnailKey(fileID, index, width), "hash", nil
		},
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	type outcome struct {
		requeue bool
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		requeue, err := service.processRequest(ctx, ChapterThumbnailRequest{FileID: fileID}, false)
		done <- outcome{requeue, err}
	}()
	select {
	case <-entered:
	case result := <-done:
		t.Fatalf("extraction did not start: %+v", result)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	settings.width.Store(320)
	pending, err := service.queueWidthBackfill(ctx, 320, false)
	if err != nil || pending.IsZero() {
		t.Fatalf("coordinator forgot the in-flight first image: pending=%v err=%v", pending, err)
	}
	if _, ok := service.queuedNormal[fileID]; !ok {
		t.Fatal("coordinator did not retain a follow-up for the missing image")
	}
	release.Do(func() { close(resume) })
	select {
	case result := <-done:
		if result.err != nil || !result.requeue {
			t.Fatalf("completed old width without a follow-up: %+v", result)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// Losing a retry to an unreadable setting must leave durable work that
	// the coordinator still sees once the database/settings recover.
	settings.fail.Store(true)
	if _, err := service.processRequest(ctx, ChapterThumbnailRequest{FileID: fileID}, false); err == nil {
		t.Fatal("unreadable width did not stop the next attempt")
	}
	settings.fail.Store(false)
	pending, err = service.queueWidthBackfill(ctx, 320, false)
	if err != nil || pending.IsZero() {
		t.Fatalf("retry outage lost the width replacement: pending=%v err=%v", pending, err)
	}
	if requeue, err := service.processRequest(ctx, ChapterThumbnailRequest{FileID: fileID}, false); err != nil || requeue {
		t.Fatalf("replacement: requeue=%v err=%v", requeue, err)
	}
	pending, err = service.queueWidthBackfill(ctx, 320, false)
	if err != nil || !pending.IsZero() {
		t.Fatalf("finished replacement remained pending: pending=%v err=%v", pending, err)
	}
}

func (r *unavailableChapterLockRepo) TryLockChapterThumbnails(ctx context.Context, _ int) (context.Context, func(), bool, error) {
	return ctx, nil, false, r.err
}

func TestChapterExtractionStopsForUnavailableClusterLock(t *testing.T) {
	for _, lockErr := range []error{nil, errors.New("database unavailable")} {
		service, repo, _, uploads := widthTestService(t, func() ([]byte, string, error) {
			t.Fatal("extracted without the cluster lock")
			return nil, "", nil
		})
		service.fileRepo = &unavailableChapterLockRepo{testFileRepo: *repo, err: lockErr}
		_, err := service.processRequest(t.Context(), ChapterThumbnailRequest{FileID: 42}, false)
		if !errors.Is(err, lockErr) {
			t.Fatalf("lock error = %v, want %v", err, lockErr)
		}
		if len(*uploads) != 0 {
			t.Fatalf("uploaded chapters without the cluster lock: %v", *uploads)
		}
	}
}

func TestWidthBackfillSerializesReplicasPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var folderID, fileID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled, chapter_thumbnails_enabled)
		VALUES ('movies', $1, true, true) RETURNING id`, fmt.Sprintf("chapter replicas %d", time.Now().UnixNano())).Scan(&folderID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=$1`, folderID) })
	if err := pool.QueryRow(ctx, `INSERT INTO media_files (media_folder_id, file_path, file_size, chapters)
		VALUES ($1, $2, 1, '[{"index":0,"start_seconds":0,"end_seconds":30,"thumbnail_path":"chapter-images/1/0/w300.webp"}]') RETURNING id`,
		folderID, fmt.Sprintf("/chapter-replicas/%d.mkv", time.Now().UnixNano())).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	var extracts, uploads atomic.Int32
	entered, resume := make(chan struct{}, 1), make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(resume) }) })
	newReplica := func() *Service {
		return &Service{
			fileRepo:   scanner.NewFileRepository(pool),
			folderRepo: &testFolderRepo{folder: &models.MediaFolder{ID: folderID, Enabled: true, ChapterThumbnailsEnabled: true}},
			settings:   testSettingsReader{values: map[string]string{config.PreviewImageWidthSettingKey: "320"}},
			extractFrameFunc: func(ctx context.Context, _ *models.MediaFile, _ float64, _ string) ([]byte, string, error) {
				if extracts.Add(1) == 1 {
					entered <- struct{}{}
					select {
					case <-resume:
					case <-ctx.Done():
						return nil, "", ctx.Err()
					}
				}
				return []byte("frame"), "", nil
			},
			uploadChapterThumbnailFunc: func(_ context.Context, fileID, index int, _ []byte) (string, string, error) {
				uploads.Add(1)
				return chapterThumbnailKey(fileID, index, 320), "new", nil
			},
		}
	}
	first, second := newReplica(), newReplica()
	// Replicas have separate query pools and independent admission budgets.
	secondPool, err := pgxpool.NewWithConfig(ctx, cfg.Copy())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(secondPool.Close)
	second.fileRepo = scanner.NewFileRepository(secondPool)
	done := make(chan error, 1)
	go func() {
		_, err := first.processRequest(ctx, ChapterThumbnailRequest{FileID: fileID}, false)
		done <- err
	}()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("first replica returned before extraction: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := second.processRequest(ctx, ChapterThumbnailRequest{FileID: fileID}, false); err != nil {
		t.Fatal(err)
	}
	if got := extracts.Load(); got != 1 {
		t.Fatalf("two replicas extracted the same stale file %d times", got)
	}
	release.Do(func() { close(resume) })
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := second.processRequest(ctx, ChapterThumbnailRequest{FileID: fileID}, false); err != nil {
		t.Fatal(err)
	}
	file, err := scanner.NewFileRepository(pool).GetByID(ctx, fileID)
	if err != nil {
		t.Fatal(err)
	}
	if extracts.Load() != 1 || uploads.Load() != 1 || len(file.Chapters) != 1 ||
		file.Chapters[0].ThumbnailPath != chapterThumbnailKey(fileID, 0, 320) || file.Chapters[0].ThumbnailThumbhash != "new" {
		t.Fatalf("extracts=%d uploads=%d chapters=%+v", extracts.Load(), uploads.Load(), file.Chapters)
	}
}

func TestChapterLockSessionLossRejectsStaleSaveDB(t *testing.T) {
	for _, maxConns := range []int32{1, 2} {
		t.Run(fmt.Sprint(maxConns), func(t *testing.T) { testChapterLockSessionLossRejectsStaleSave(t, maxConns) })
	}
}

func testChapterLockSessionLossRejectsStaleSave(t *testing.T, maxConns int32) {
	t.Helper()
	pool := chapterReplicaTestPool(t, maxConns)
	fileID, _ := chapterURLTestFile(t, pool)
	var folderID int
	if err := pool.QueryRow(t.Context(), `SELECT media_folder_id FROM media_files WHERE id=$1`, fileID).Scan(&folderID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	entered, resume := make(chan struct{}), make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(resume) }) })
	newReplica := func(stale bool) *Service {
		return &Service{
			fileRepo:   scanner.NewFileRepository(pool),
			folderRepo: &testFolderRepo{folder: &models.MediaFolder{ID: folderID, Enabled: true, ChapterThumbnailsEnabled: true}},
			settings:   testSettingsReader{values: map[string]string{config.PreviewImageWidthSettingKey: "320"}},
			extractFrameFunc: func(ctx context.Context, _ *models.MediaFile, _ float64, _ string) ([]byte, string, error) {
				if stale {
					close(entered)
					select {
					case <-resume:
					case <-ctx.Done():
						return nil, "", ctx.Err()
					}
				}
				return []byte("frame"), "", nil
			},
			uploadChapterThumbnailFunc: func(_ context.Context, fileID, index int, _ []byte) (string, string, error) {
				hash := "replacement"
				if stale {
					hash = "stale"
				}
				return chapterThumbnailKey(fileID, index, 320), hash, nil
			},
		}
	}
	first, second := newReplica(true), newReplica(false)
	// Replicas have separate local admission budgets. Terminating the first
	// backend releases its database lock while its paused worker still holds
	// that replica's one-session budget.
	second.fileRepo = scanner.NewFileRepository(chapterReplicaTestPool(t, maxConns))
	done := make(chan error, 1)
	go func() {
		_, err := first.processRequest(ctx, ChapterThumbnailRequest{FileID: fileID}, false)
		done <- err
	}()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("first worker returned before extraction: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// Wait for backend exit, rather than only sending the termination signal,
	// so the replacement sees the released advisory lock.
	var terminated bool
	if err := pool.QueryRow(ctx, `SELECT pg_terminate_backend(pid, 5000) FROM pg_stat_activity
		WHERE application_name=$1`, fmt.Sprintf("silo-chapter-thumbnails:%d", fileID)).Scan(&terminated); err != nil || !terminated {
		t.Fatalf("terminate held chapter session: %v, %v", terminated, err)
	}
	if _, err := second.processRequest(ctx, ChapterThumbnailRequest{FileID: fileID}, false); err != nil {
		t.Fatal(err)
	}
	release.Do(func() { close(resume) })
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("worker saved after losing its advisory lock session")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	file, err := scanner.NewFileRepository(pool).GetByID(ctx, fileID)
	if err != nil || file.Chapters[0].ThumbnailThumbhash != "replacement" {
		t.Fatalf("stale worker replaced completed chapters: file=%+v err=%v", file, err)
	}
}

func chapterReplicaTestPool(t *testing.T, maxConns int32) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	// Both one- and two-connection pools admit one chapter session. Keep the
	// test independent of the CPU-derived pgxpool default used on CI runners.
	cfg.MaxConns = maxConns
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}
