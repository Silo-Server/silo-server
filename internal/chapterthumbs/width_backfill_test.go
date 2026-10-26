package chapterthumbs

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

type widthPageRepo struct {
	testFileRepo
	files int
	pages chan int
}

func (r *widthPageRepo) ListChapterThumbnailsAtOtherWidths(_ context.Context, limit int, _ string, afterID int, _ bool) ([]*models.MediaFile, time.Time, error) {
	r.pages <- afterID
	var files []*models.MediaFile
	for id := afterID + 1; id <= min(r.files, afterID+limit); id++ {
		files = append(files, &models.MediaFile{ID: id})
	}
	return files, time.Now(), nil
}

func widthQueueService(repo FileRepository) *Service {
	return &Service{
		fileRepo:        repo,
		settings:        testSettingsReader{values: map[string]string{config.PreviewImageWidthSettingKey: "320"}},
		notifyNormal:    make(chan struct{}, 1),
		notifyPriority:  make(chan struct{}, 1),
		widthQueueSpace: make(chan struct{}, 1),
		queuedNormal:    map[int]ChapterThumbnailRequest{},
		queuedPriority:  map[int]ChapterThumbnailRequest{},
		inProgress:      map[int]struct{}{},
	}
}

func TestWidthBackfillPagesAndWaitsForWorkers(t *testing.T) {
	repo := &widthPageRepo{files: defaultBatchLimit + 2, pages: make(chan int, 2)}
	s := widthQueueService(repo)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := s.queueWidthBackfill(ctx, 320, false); done <- err }()
	for _, want := range []int{0, defaultBatchLimit} {
		select {
		case got := <-repo.pages:
			if got != want {
				t.Fatalf("cursor %d, want %d", got, want)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	s.mu.Lock()
	depth := len(s.queuedNormal)
	s.mu.Unlock()
	if depth != defaultBatchLimit {
		t.Fatalf("queued %d files before workers consumed any, want %d", depth, defaultBatchLimit)
	}
	select {
	case err := <-done:
		t.Fatalf("backfill finished while its queue was full: %v", err)
	default:
	}
	// Consuming two requests makes room for the rest of the second page.
	for range 2 {
		s.mu.Lock()
		req, ok := s.popQueuedLocked(false)
		s.mu.Unlock()
		if !ok {
			t.Fatal("no queued file")
		}
		s.finishProcessing(req.FileID)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	s.mu.Lock()
	queued := slices.Clone(s.normalQueue)
	s.mu.Unlock()
	if len(queued) != defaultBatchLimit || queued[len(queued)-1] != repo.files {
		t.Fatalf("remaining queue %v", queued)
	}
}

func TestWidthBackfillKeepsFollowupForRunningFile(t *testing.T) {
	s := widthQueueService(&testFileRepo{})
	s.inProgress[42] = struct{}{}
	if err := s.queueWidthFile(t.Context(), 42, 320); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.popQueuedLocked(false); ok {
		t.Fatal("width follow-up started while its previous request was running")
	}
	s.finishProcessing(42)
	if req, ok := s.popQueuedLocked(false); !ok || req.FileID != 42 {
		t.Fatalf("follow-up = %+v, %v", req, ok)
	}
}

func TestWidthBackfillCancelsWhileQueueIsFull(t *testing.T) {
	s := widthQueueService(&testFileRepo{})
	for id := range defaultBatchLimit {
		s.queuedNormal[id+1] = ChapterThumbnailRequest{FileID: id + 1}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.queueWidthFile(ctx, 42, 320); !errors.Is(err, context.Canceled) {
		t.Fatalf("queue = %v, want canceled", err)
	}
}

func TestWidthBackfillStopsForChangedOrUnreadableWidth(t *testing.T) {
	s := widthQueueService(&testFileRepo{})
	if _, err := s.queueWidthBackfill(t.Context(), 300, false); !errors.Is(err, errPreviewWidthChanged) {
		t.Fatalf("changed width = %v", err)
	}
	s.settings = failingSettingsReader{}
	if _, err := s.queueWidthBackfill(t.Context(), 320, false); err == nil {
		t.Fatal("queued files with an unreadable width")
	}
}
