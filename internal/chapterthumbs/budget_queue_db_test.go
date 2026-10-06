package chapterthumbs

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/jackc/pgx/v5/pgxpool"
)

type chapterWaitContext struct {
	context.Context
	waited func()
}

func (c chapterWaitContext) Done() <-chan struct{} {
	c.waited()
	return c.Context.Done()
}

type chapterAdmissionResult struct {
	acquired bool
	err      error
}

type observedChapterAdmissionRepo struct {
	*scanner.FileRepository
	waiting    chan struct{}
	notifyWait func()
	returned   chan chapterAdmissionResult
}

func (r *observedChapterAdmissionRepo) TryLockChapterThumbnails(ctx context.Context, fileID int) (context.Context, func(), bool, error) {
	observed := chapterWaitContext{Context: ctx, waited: r.notifyWait}
	lockCtx, release, acquired, err := r.FileRepository.TryLockChapterThumbnails(observed, fileID)
	r.returned <- chapterAdmissionResult{acquired, err}
	return lockCtx, release, acquired, err
}

type chapterProbeFunc func(context.Context, *models.MediaFile) (*models.MediaFile, error)

func (f chapterProbeFunc) EnsureProbeOnly(ctx context.Context, file *models.MediaFile) (*models.MediaFile, error) {
	return f(ctx, file)
}

type chapterSavedSignal chan struct{}

func (s chapterSavedSignal) ChapterThumbnailReady(context.Context, int, int, string, string) {
	s <- struct{}{}
}

func TestChapterBudgetRetainsQueuedUnprobedFileDB(t *testing.T) {
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
	cfg.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	fileID, _ := chapterURLTestFile(t, pool)
	var folderID int
	if err := pool.QueryRow(ctx, `UPDATE media_files SET chapters=NULL WHERE id=$1 RETURNING media_folder_id`, fileID).Scan(&folderID); err != nil {
		t.Fatal(err)
	}
	repo := scanner.NewFileRepository(pool)
	_, releaseHeld, acquired, err := repo.TryLockChapterThumbnails(ctx, fileID+1000000)
	if err != nil || !acquired {
		t.Fatalf("occupy session budget: acquired=%v err=%v", acquired, err)
	}
	t.Cleanup(releaseHeld)
	observed := &observedChapterAdmissionRepo{FileRepository: repo, waiting: make(chan struct{}), returned: make(chan chapterAdmissionResult, 1)}
	observed.notifyWait = sync.OnceFunc(func() { close(observed.waiting) })
	service := widthQueueService(observed)
	saved := make(chapterSavedSignal, 1)
	service.notifier = saved
	service.folderRepo = &testFolderRepo{folder: &models.MediaFolder{ID: folderID, Enabled: true, ChapterThumbnailsEnabled: true}}
	probed := make(chan struct{}, 1)
	service.probeEnsurer = chapterProbeFunc(func(_ context.Context, file *models.MediaFile) (*models.MediaFile, error) {
		updated := *file
		updated.Chapters = []models.MediaChapter{{Index: 0, StartSeconds: 0, EndSeconds: 30}}
		probed <- struct{}{}
		return &updated, nil
	})
	service.extractFrameFunc = func(context.Context, *models.MediaFile, float64, string) ([]byte, string, error) {
		return []byte("frame"), "", nil
	}
	service.uploadChapterThumbnailFunc = func(_ context.Context, fileID, index int, _ []byte) (string, string, error) {
		return chapterThumbnailKey(fileID, index, 320), "first-image", nil
	}
	service.queuedNormal[fileID] = ChapterThumbnailRequest{FileID: fileID}
	service.normalQueue = append(service.normalQueue, fileID)
	done := make(chan struct{})
	go func() { defer close(done); service.worker(ctx, false) }()
	// Done is observed only within the repository call. The saturated budget
	// must enter a cancellable wait instead of reporting the request finished.
	select {
	case <-observed.waiting:
	case result := <-observed.returned:
		t.Fatalf("queued unprobed file was removed without waiting: %+v", result)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	service.mu.Lock()
	_, processing := service.inProgress[fileID]
	service.mu.Unlock()
	if !processing {
		t.Fatal("worker did not retain the queued file during admission")
	}
	releaseHeld()
	select {
	case result := <-observed.returned:
		if result.err != nil || !result.acquired {
			t.Fatalf("released capacity did not admit queued work: %+v", result)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-probed:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// The ready notification follows the catalog save.
	select {
	case <-saved:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	file, err := repo.GetByID(ctx, fileID)
	if err != nil || len(file.Chapters) != 1 || file.Chapters[0].ThumbnailPath != chapterThumbnailKey(fileID, 0, 320) || file.Chapters[0].ThumbnailThumbhash != "first-image" {
		t.Fatalf("queued first image was lost: file=%+v err=%v", file, err)
	}
	cancel()
	select {
	case <-done:
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
}
