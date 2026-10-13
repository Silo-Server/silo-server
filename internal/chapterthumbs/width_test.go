package chapterthumbs

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeBlobQueue struct {
	scheduled []string
	delay     time.Duration
}

func (q *fakeBlobQueue) Schedule(_ context.Context, prefixes []string, delay time.Duration) error {
	q.scheduled = append(q.scheduled, prefixes...)
	q.delay = delay
	return nil
}

func TestChapterEligibilityFollowsTheWidth(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	later := now.Add(time.Hour)
	w300 := "chapter-images/1/0/w300.webp"
	for _, tt := range []struct {
		name    string
		chapter models.MediaChapter
		width   int
		want    bool
	}{
		{"no thumbnail", models.MediaChapter{}, 300, true},
		{"made at this width", models.MediaChapter{ThumbnailPath: w300}, 300, false},
		{"made at another width", models.MediaChapter{ThumbnailPath: w300}, 320, true},
		{"another width, waiting out a failure", models.MediaChapter{ThumbnailPath: w300, ThumbnailRetryAfter: &later}, 320, false},
	} {
		if got := isChapterEligible(tt.chapter, now, tt.width); got != tt.want {
			t.Errorf("%s: eligible = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// widthTestService serves file 42, whose chapter 0 was made at 300 px,
// chapter 1 at 320 px, and chapter 2 has no thumbnail, with the width
// setting at 320 px.
func widthTestService(t *testing.T, extract func() ([]byte, string, error)) (*Service, *testFileRepo, *fakeBlobQueue, *[]int) {
	t.Helper()
	repo := &testFileRepo{file: &models.MediaFile{
		ID: 42, MediaFolderID: 9, FilePath: "/media/movie.mkv",
		Chapters: []models.MediaChapter{
			{Index: 0, StartSeconds: 0, EndSeconds: 10, ThumbnailPath: "chapter-images/42/0/w300.webp", ThumbnailThumbhash: "old"},
			{Index: 1, StartSeconds: 10, EndSeconds: 20, ThumbnailPath: "chapter-images/42/1/w320.webp", ThumbnailThumbhash: "current"},
			{Index: 2, StartSeconds: 20, EndSeconds: 30},
		},
	}}
	queue := &fakeBlobQueue{}
	var uploaded []int
	service := &Service{
		fileRepo:   repo,
		folderRepo: &testFolderRepo{folder: &models.MediaFolder{ID: 9, Enabled: true, ChapterThumbnailsEnabled: true}},
		settings:   testSettingsReader{values: map[string]string{config.PreviewImageWidthSettingKey: "320"}},
		blobQueue:  queue,
		clock:      func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
		extractFrameFunc: func(context.Context, *models.MediaFile, float64, string) ([]byte, string, error) {
			return extract()
		},
		uploadChapterThumbnailFunc: func(_ context.Context, fileID, chapterIndex int, _ []byte) (string, string, error) {
			uploaded = append(uploaded, chapterIndex)
			return fmt.Sprintf("chapter-images/%d/%d/w320.webp", fileID, chapterIndex), "new", nil
		},
	}
	return service, repo, queue, &uploaded
}

func TestProcessRequestRemakesOtherWidthsAndRetiresThem(t *testing.T) {
	service, repo, queue, uploaded := widthTestService(t, func() ([]byte, string, error) { return []byte("frame"), "", nil })

	requeue, err := service.processRequest(t.Context(), ChapterThumbnailRequest{FileID: 42}, false)
	if err != nil || requeue {
		t.Fatalf("processRequest() = %v, %v; want no error and nothing left", requeue, err)
	}
	if !slices.Equal(*uploaded, []int{0, 2}) {
		t.Fatalf("uploaded chapters %v, want 0 (made at 300 px) and 2 (none)", *uploaded)
	}
	if got := repo.file.Chapters[0].ThumbnailPath; got != "chapter-images/42/0/w320.webp" {
		t.Fatalf("chapter 0 path %q", got)
	}
	// Only the replaced image is retired, and only after the save.
	if !slices.Equal(queue.scheduled, []string{"chapter-images/42/0/w300.webp"}) || queue.delay != displacedImageGrace {
		t.Fatalf("scheduled %v after %v", queue.scheduled, queue.delay)
	}
}

func TestProcessRequestKeepsTheOldImageWhenItsReplacementFails(t *testing.T) {
	service, repo, queue, _ := widthTestService(t, func() ([]byte, string, error) {
		return nil, reasonChapterExtractFailed, errors.New("seek failed")
	})

	if _, err := service.processRequest(t.Context(), ChapterThumbnailRequest{FileID: 42}, false); err != nil {
		t.Fatal(err)
	}
	chapter := repo.file.Chapters[0]
	if chapter.ThumbnailPath != "chapter-images/42/0/w300.webp" || chapter.ThumbnailThumbhash != "old" {
		t.Fatalf("chapter 0 lost its image: %+v", chapter)
	}
	if chapter.ThumbnailRetryAfter == nil || chapter.ThumbnailLastError == "" {
		t.Fatalf("chapter 0 failure not recorded: %+v", chapter)
	}
	if len(queue.scheduled) != 0 {
		t.Fatalf("retired %v with no replacement", queue.scheduled)
	}
}

func TestProcessRequestStopsWhenTheWidthIsUnreadable(t *testing.T) {
	service, repo, _, uploaded := widthTestService(t, func() ([]byte, string, error) { return []byte("frame"), "", nil })
	service.settings = failingSettingsReader{}

	if _, err := service.processRequest(t.Context(), ChapterThumbnailRequest{FileID: 42}, false); err == nil {
		t.Fatal("processRequest() made thumbnails without knowing their width")
	}
	if len(*uploaded) != 0 || repo.updateCalls != 0 {
		t.Fatalf("uploaded %v, saved %d times", *uploaded, repo.updateCalls)
	}
	if _, err := service.BackfillMissing(t.Context(), 10); err == nil {
		t.Fatal("BackfillMissing() listed files without knowing the width")
	}
}

func TestBackfillListsFilesMissingTheCurrentWidth(t *testing.T) {
	service, repo, _, _ := widthTestService(t, func() ([]byte, string, error) { return []byte("frame"), "", nil })
	if _, err := service.BackfillMissing(t.Context(), 10); err != nil {
		t.Fatal(err)
	}
	if repo.missingSuffix != "/w320.webp" {
		t.Fatalf("suffix %q", repo.missingSuffix)
	}
}

func TestImageKeyGroup(t *testing.T) {
	for key, want := range map[string]bool{
		"chapter-images/42/0/w300.webp": true,
		"chapter-images/42/0-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef/w300.webp":  true,
		"chapter-images/42/0-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcde/w300.webp":   false,
		"chapter-images/42/0-0123456789ABCDEF0123456789abcdef0123456789abcdef0123456789abcdef/w300.webp":  false,
		"chapter-images/42/00-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef/w300.webp": false,
		"chapter-images/42/0-/w300.webp":    false,
		"chapter-images/7/15/w640.webp":     true,
		"chapter-images/42/0/original.webp": false,
		"chapter-images/42/0/w0300.webp":    false,
		"chapter-images/42/0/w.webp":        false,
		"chapter-images/42/00/w300.webp":    false,
		"chapter-images/042/0/w300.webp":    false,
		"chapter-images/42/":                false,
		"chapter-images/42/0/w300.jpg":      false,
		"chapter-images/42/0/1/w300.webp":   false,
		"trickplay/42/7/0.7.jpg":            false,
	} {
		group, ok := imageKeyGroup(key)
		if ok != want || (ok && group != key) {
			t.Errorf("imageKeyGroup(%q) = %q, %v; want %v", key, group, ok, want)
		}
	}
}
