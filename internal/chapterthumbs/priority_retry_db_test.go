package chapterthumbs

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

type observedContendedChapterRepo struct {
	*scanner.FileRepository
	contended chan struct{}
}

func (r *observedContendedChapterRepo) TryLockChapterThumbnails(ctx context.Context, fileID int) (context.Context, func(), bool, error) {
	lockCtx, release, acquired, err := r.FileRepository.TryLockChapterThumbnails(ctx, fileID)
	if !acquired && err == nil {
		select {
		case r.contended <- struct{}{}:
		default:
		}
	}
	return lockCtx, release, acquired, err
}

func TestPriorityChapterRequestSurvivesReplicaContentionDB(t *testing.T) {
	pool := chapterURLTestPool(t, nil)
	secondPool := chapterURLTestPool(t, nil)
	fileID, _ := chapterURLTestFile(t, pool)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var folderID int
	if err := pool.QueryRow(ctx, `SELECT media_folder_id FROM media_files WHERE id=$1`, fileID).Scan(&folderID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE media_files SET chapters=(
		SELECT jsonb_agg(jsonb_build_object('index', n, 'start_seconds', n*100, 'end_seconds', (n+1)*100))
		FROM generate_series(0,11) n) WHERE id=$1`, fileID); err != nil {
		t.Fatal(err)
	}
	newReplica := func(repo FileRepository) *Service {
		service := widthQueueService(repo)
		service.folderRepo = &testFolderRepo{folder: &models.MediaFolder{ID: folderID, Enabled: true, ChapterThumbnailsEnabled: true}}
		service.normalBatchSize, service.priorityBatchSize = 1, 1
		service.uploadChapterThumbnailFunc = func(_ context.Context, fileID, index int, _ []byte) (string, string, error) {
			return chapterThumbnailKey(fileID, index, 320), fmt.Sprint(index), nil
		}
		return service
	}
	first := newReplica(scanner.NewFileRepository(pool))
	entered, resume := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(resume) })
	t.Cleanup(release)
	first.extractFrameFunc = func(ctx context.Context, _ *models.MediaFile, _ float64, _ string) ([]byte, string, error) {
		close(entered)
		select {
		case <-resume:
			return []byte("normal frame"), "", nil
		case <-ctx.Done():
			return nil, "", ctx.Err()
		}
	}
	firstDone := make(chan error, 1)
	go func() {
		_, err := first.processRequest(ctx, ChapterThumbnailRequest{FileID: fileID}, false)
		firstDone <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	repo := &observedContendedChapterRepo{FileRepository: scanner.NewFileRepository(secondPool), contended: make(chan struct{}, 1)}
	second := newReplica(repo)
	extracted := make(chan float64, 16)
	second.extractFrameFunc = func(_ context.Context, _ *models.MediaFile, seek float64, _ string) ([]byte, string, error) {
		extracted <- seek
		return []byte("priority frame"), "", nil
	}
	second.notifier = make(chapterSavedSignal, 16)
	workerDone := make(chan struct{})
	second.QueuePriorityFileAtPosition(ctx, fileID, 850)
	// An ordinary worker can dequeue the playback request, and must preserve
	// its priority as well as its position when the other replica owns the lock.
	go func() { defer close(workerDone); second.worker(ctx, false) }()
	select {
	case <-repo.contended:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	release()
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case seek := <-extracted:
		if seek != 805 {
			t.Fatalf("retried chapter seek = %v, want retained target at 805", seek)
		}
	case <-ctx.Done():
		t.Fatal("contended playback request was lost")
	}
	select {
	case <-second.notifier.(chapterSavedSignal):
	case <-ctx.Done():
		t.Fatal("retried chapter was not saved")
	}
	file, err := repo.GetByID(ctx, fileID)
	if err != nil || file == nil || len(file.Chapters) < 9 || file.Chapters[8].ThumbnailPath != chapterThumbnailKey(fileID, 8, 320) {
		t.Fatalf("retained target was not published: file=%+v err=%v", file, err)
	}
	cancel()
	select {
	case <-workerDone:
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
}
