package chapterthumbs

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/blobgc"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

func TestTerminatedChapterWorkerCannotReplaceCurrentBlobDB(t *testing.T) {
	for _, maxConns := range []int32{1, 2} {
		t.Run(fmt.Sprint(maxConns), func(t *testing.T) { testTerminatedChapterWorkerCannotReplaceCurrentBlob(t, maxConns) })
	}
}

func testTerminatedChapterWorkerCannotReplaceCurrentBlob(t *testing.T, maxConns int32) {
	t.Helper()
	pool := chapterReplicaTestPool(t, maxConns)
	fileID, _ := chapterURLTestFile(t, pool)
	var folderID int
	if err := pool.QueryRow(t.Context(), `SELECT media_folder_id FROM media_files WHERE id=$1`, fileID).Scan(&folderID); err != nil {
		t.Fatal(err)
	}
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	frame := func(fill color.RGBA) []byte {
		img := image.NewRGBA(image.Rect(0, 0, 320, 180))
		for y := range 180 {
			for x := range 320 {
				img.SetRGBA(x, y, fill)
			}
		}
		var out bytes.Buffer
		if err := jpeg.Encode(&out, img, nil); err != nil {
			t.Fatal(err)
		}
		return out.Bytes()
	}
	staleFrame, currentFrame := frame(color.RGBA{R: 255, A: 255}), frame(color.RGBA{B: 255, A: 255})
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
			store:      store,
			blobQueue:  blobgc.NewQueue(pool),
			extractFrameFunc: func(ctx context.Context, _ *models.MediaFile, seek float64, _ string) ([]byte, string, error) {
				if stale {
					close(entered)
					select {
					case <-resume:
					case <-ctx.Done():
						return nil, "", ctx.Err()
					}
				}
				if seek < 10 {
					return staleFrame, "", nil
				}
				return currentFrame, "", nil
			},
		}
	}
	first, second := newReplica(true), newReplica(false)
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
	if err := pool.QueryRow(ctx, `SELECT pg_terminate_backend(pid, 5000) FROM pg_stat_activity WHERE application_name=$1`, fmt.Sprintf("silo-chapter-thumbnails:%d", fileID)).Scan(&terminated); err != nil || !terminated {
		t.Fatalf("terminate chapter session: %v %v", terminated, err)
	}
	// A rescan can change capture timing before the replacement worker reads.
	if _, err := pool.Exec(ctx, `UPDATE media_files SET chapters=jsonb_set(chapters, '{0,start_seconds}', '10') WHERE id=$1`, fileID); err != nil {
		t.Fatal(err)
	}
	if _, err := second.processRequest(ctx, ChapterThumbnailRequest{FileID: fileID}, false); err != nil {
		t.Fatal(err)
	}
	published, err := scanner.NewFileRepository(pool).GetByID(ctx, fileID)
	if err != nil || published == nil || len(published.Chapters) != 1 {
		t.Fatalf("replacement chapter unavailable: file=%+v err=%v", published, err)
	}
	key := published.Chapters[0].ThumbnailPath
	readBytes := func() []byte {
		t.Helper()
		r, _, err := store.Get(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = r.Close() }()
		data, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	currentBytes := readBytes()
	release.Do(func() { close(resume) })
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stale worker saved after losing its lock")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	file, err := scanner.NewFileRepository(pool).GetByID(ctx, fileID)
	if err != nil || file == nil || len(file.Chapters) != 1 || file.Chapters[0].StartSeconds != 10 || file.Chapters[0].ThumbnailPath != key || file.Chapters[0].ThumbnailThumbhash != published.Chapters[0].ThumbnailThumbhash {
		t.Fatalf("replacement chapter state lost: file=%+v err=%v", file, err)
	}
	if !bytes.Equal(currentBytes, readBytes()) {
		t.Fatalf("stale worker overwrote replacement object despite failed DB save; replacement chapter remains at %v seconds", file.Chapters[0].StartSeconds)
	}
}
