package scanner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeRootContentFinder struct {
	contentID string
	err       error
	calls     int
}

func (f *fakeRootContentFinder) FindContentIDByRootPath(context.Context, int, string, string) (string, error) {
	f.calls++
	return f.contentID, f.err
}

type fakeFilesystemItemWriter struct {
	upserts []*models.MediaItem
}

func (f *fakeFilesystemItemWriter) Upsert(_ context.Context, item *models.MediaItem) error {
	cp := *item
	f.upserts = append(f.upserts, &cp)
	return nil
}

func TestParseAudiobookFolderSingleM4B(t *testing.T) {
	ffprobePath := FFprobePathFromFFmpeg("ffmpeg")
	if _, err := exec.LookPath(ffprobePath); err != nil {
		ffprobePath = "ffprobe"
		if _, err := exec.LookPath(ffprobePath); err != nil {
			t.Skip("ffprobe not available")
		}
	}

	ctx := context.Background()
	got, err := parseAudiobookFolder(ctx, ffprobePath, "testdata/audiobook_fixtures/single_book")
	if err != nil {
		t.Fatalf("parseAudiobookFolder: %v", err)
	}
	if got.Title != "Test Audiobook" {
		t.Errorf("Title = %q, want %q", got.Title, "Test Audiobook")
	}
	if got.Author != "Test Author" {
		t.Errorf("Author = %q, want %q", got.Author, "Test Author")
	}
	if got.Series != "Test Series" {
		t.Errorf("Series = %q, want %q", got.Series, "Test Series")
	}
	if got.Year != 2024 {
		t.Errorf("Year = %d, want 2024", got.Year)
	}
	if len(got.Files) != 1 {
		t.Fatalf("got %d files, want 1", len(got.Files))
	}
	if len(got.Files[0].Chapters) != 2 {
		t.Errorf("file 0 chapters = %d, want 2", len(got.Files[0].Chapters))
	}
}

func TestParseAudiobookFolderMultiFile(t *testing.T) {
	ffprobePath := FFprobePathFromFFmpeg("ffmpeg")
	if _, err := exec.LookPath(ffprobePath); err != nil {
		ffprobePath = "ffprobe"
		if _, err := exec.LookPath(ffprobePath); err != nil {
			t.Skip("ffprobe not available")
		}
	}

	ctx := context.Background()
	got, err := parseAudiobookFolder(ctx, ffprobePath, "testdata/audiobook_fixtures/multi_file")
	if err != nil {
		t.Fatalf("parseAudiobookFolder: %v", err)
	}
	if got.Title != "Multi File Test" {
		t.Errorf("Title = %q, want %q", got.Title, "Multi File Test")
	}
	if got.Author != "Multi Author" {
		t.Errorf("Author = %q, want %q", got.Author, "Multi Author")
	}
	if got.Year != 2023 {
		t.Errorf("Year = %d, want 2023", got.Year)
	}
	if len(got.Files) != 3 {
		t.Fatalf("got %d files, want 3", len(got.Files))
	}
	for i, f := range got.Files {
		if len(f.Chapters) != 1 {
			t.Errorf("file %d: %d synthesized chapters, want 1", i, len(f.Chapters))
			continue
		}
		wantStem := fmt.Sprintf("part%d", i+1)
		if f.Chapters[0].Title != wantStem {
			t.Errorf("file %d: chapter title = %q, want %q", i, f.Chapters[0].Title, wantStem)
		}
		if f.Chapters[0].StartSeconds != 0 || f.Chapters[0].EndSeconds != 0 {
			t.Errorf("file %d: synthesized chapter range = %.3f..%.3f, want 0..0 unknown duration", i, f.Chapters[0].StartSeconds, f.Chapters[0].EndSeconds)
		}
	}
}

func TestAudiobookIdentityConfidenceReflectsMetadataCompleteness(t *testing.T) {
	book := &parsedAudiobook{Title: "Tagged Book", Author: "Author", Narrator: "Narrator", Year: 2024}
	file := parsedAudiobookFile{Chapters: []ChapterInfo{{Title: "One", StartSeconds: 0, EndSeconds: 10}}}
	if got := audiobookIdentityConfidence(book, file); got != "high" {
		t.Fatalf("complete metadata confidence = %q, want high", got)
	}

	book = &parsedAudiobook{Title: "Tagged Book"}
	file = parsedAudiobookFile{Chapters: []ChapterInfo{{Title: "Unknown duration"}}}
	if got := audiobookIdentityConfidence(book, file); got != "medium" {
		t.Fatalf("partial metadata confidence = %q, want medium", got)
	}

	book = &parsedAudiobook{}
	file = parsedAudiobookFile{}
	if got := audiobookIdentityConfidence(book, file); got != "low" {
		t.Fatalf("empty metadata confidence = %q, want low", got)
	}
}

func TestScanAudiobookFolderReturnsErrorWhenEveryReconcileFails(t *testing.T) {
	root := t.TempDir()
	bookDir := filepath.Join(root, "bad-book")
	if err := os.Mkdir(bookDir, 0o755); err != nil {
		t.Fatalf("mkdir book dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bookDir, "chapter.mp3"), []byte("not real audio"), 0o644); err != nil {
		t.Fatalf("write fake audio: %v", err)
	}

	s := &Scanner{ffprobePath: "definitely-missing-ffprobe"}
	err := s.ScanAudiobookFolder(context.Background(), &models.MediaFolder{ID: 42, Paths: []string{root}})
	if err == nil {
		t.Fatal("ScanAudiobookFolder returned nil, want aggregate failure")
	}
	if !strings.Contains(err.Error(), "folder_id=42") {
		t.Fatalf("error = %q, want folder id", err)
	}
}

func TestResolveAudiobookMediaItemReusesRootScopedContentID(t *testing.T) {
	finder := &fakeRootContentFinder{contentID: "book-root-id"}
	writer := &fakeFilesystemItemWriter{}

	got, err := resolveAudiobookMediaItem(
		context.Background(),
		finder,
		writer,
		7,
		"/library/Author/Same Title",
		&parsedAudiobook{Title: "Same Title", Year: 0, Author: "Author A"},
	)
	if err != nil {
		t.Fatalf("resolveAudiobookMediaItem: %v", err)
	}
	if got != "book-root-id" {
		t.Fatalf("contentID = %q, want root-scoped id", got)
	}
	if finder.calls != 1 {
		t.Fatalf("root finder calls = %d, want 1", finder.calls)
	}
	if len(writer.upserts) != 0 {
		t.Fatalf("unexpected item upsert for existing root: %d", len(writer.upserts))
	}
}

func TestResolveAudiobookMediaItemCreatesNewWhenRootHasNoClaim(t *testing.T) {
	finder := &fakeRootContentFinder{}
	writer := &fakeFilesystemItemWriter{}

	got, err := resolveAudiobookMediaItem(
		context.Background(),
		finder,
		writer,
		7,
		"/library/Other Author/Same Title",
		&parsedAudiobook{Title: "Same Title", Year: 0, Author: "Author B"},
	)
	if err != nil {
		t.Fatalf("resolveAudiobookMediaItem: %v", err)
	}
	if got == "" {
		t.Fatal("contentID is empty")
	}
	if len(writer.upserts) != 1 {
		t.Fatalf("upserts = %d, want 1", len(writer.upserts))
	}
	if writer.upserts[0].ContentID != got || writer.upserts[0].Type != "audiobook" {
		t.Fatalf("upserted item = %+v, contentID %q", writer.upserts[0], got)
	}
}

func TestResolveAudiobookMediaItemPropagatesRootLookupError(t *testing.T) {
	wantErr := errors.New("root lookup failed")
	finder := &fakeRootContentFinder{err: wantErr}
	writer := &fakeFilesystemItemWriter{}

	_, err := resolveAudiobookMediaItem(
		context.Background(),
		finder,
		writer,
		7,
		"/library/Author/Book",
		&parsedAudiobook{Title: "Book"},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if len(writer.upserts) != 0 {
		t.Fatalf("upserts = %d, want 0", len(writer.upserts))
	}
}
