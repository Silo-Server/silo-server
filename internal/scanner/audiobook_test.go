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
	"time"

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

func TestPopulateFromTags_SeriesDoesNotFallBackToAlbum(t *testing.T) {
	// In audiobook tagging the `album` tag holds the book title, not the
	// series name. populateFromTags must NOT use it as a series fallback,
	// otherwise every book without an explicit series tag ends up with
	// series_name = its own title (each becomes a singleton "series",
	// polluting the Series filter dropdown — see migration 145 history).
	b := &parsedAudiobook{}
	b.populateFromTags(map[string]string{
		"title":  "Project Hail Mary",
		"album":  "Project Hail Mary",
		"artist": "Andy Weir",
	})
	if b.Series != "" {
		t.Fatalf("expected Series to stay empty when only `album` is set; got %q", b.Series)
	}
}

func TestPopulateFromTags_SeriesPrefersExplicitTag(t *testing.T) {
	b := &parsedAudiobook{}
	b.populateFromTags(map[string]string{
		"title":  "The Way of Kings",
		"album":  "The Way of Kings",
		"series": "The Stormlight Archive",
	})
	if b.Series != "The Stormlight Archive" {
		t.Fatalf("expected Series=%q, got %q", "The Stormlight Archive", b.Series)
	}
}

func TestPopulateFromTags_SeriesFallsBackToMovementName(t *testing.T) {
	b := &parsedAudiobook{}
	b.populateFromTags(map[string]string{
		"title": "The Way of Kings",
		"mvnm":  "The Stormlight Archive",
	})
	if b.Series != "The Stormlight Archive" {
		t.Fatalf("expected Series=%q (from mvnm fallback), got %q", "The Stormlight Archive", b.Series)
	}
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
	// The fixture has `album: Test Series` but no real `series` or
	// `mvnm` tag. Series must stay empty — previously the parser
	// fell back to album, which polluted audiobook_series with one
	// fake singleton per book on real libraries.
	if got.Series != "" {
		t.Errorf("Series = %q, want %q (album must NOT be used as a fallback)", got.Series, "")
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
		if f.Chapters[0].StartSeconds != 0 || f.Chapters[0].EndSeconds <= f.Chapters[0].StartSeconds {
			t.Errorf("file %d: synthesized chapter range = %.3f..%.3f, want positive probed duration", i, f.Chapters[0].StartSeconds, f.Chapters[0].EndSeconds)
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

func TestScanAudiobookFolderReturnsCanceledContext(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	s := &Scanner{}
	err := s.ScanAudiobookFolder(ctx, &models.MediaFolder{ID: 42, Paths: []string{root}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ScanAudiobookFolder error = %v, want context.Canceled", err)
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

func TestStripNarratorSuffix(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Solitaire Read By Holly Gibbs", "Solitaire"},
		{"The Skyward Series 3 - Cytonic (UK Version: Read by Sophie Aldred)", "The Skyward Series 3 - Cytonic"},
		{"Mick Stranahan (unabridged) 1 - Skin Tight", "Mick Stranahan 1 - Skin Tight"},
		{"An Evening with Alan Titchmarsh (Unabridged)", "An Evening with Alan Titchmarsh"},
		// "Read by Celebrities" is a series name here, not a narrator credit
		{"Classic Stories and Tales Read by Celebrities 1 - Classics of Childhood, Volume One",
			"Classic Stories and Tales Read by Celebrities 1 - Classics of Childhood, Volume One"},
		{"Old Harry's Game", "Old Harry's Game"}, // unchanged
	}
	for _, c := range cases {
		got := stripNarratorSuffix(c.in)
		if got != c.want {
			t.Errorf("stripNarratorSuffix(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAudiobookFolderUnchangedAllMatch(t *testing.T) {
	now := time.Now().UTC()
	files := []*models.MediaFile{
		{FilePath: "/lib/Author/Book/a.m4b", FileSize: 100, FileModifiedAt: &now},
		{FilePath: "/lib/Author/Book/b.m4b", FileSize: 200, FileModifiedAt: &now},
	}
	onDisk := []audiobookDiskFile{
		{Path: "/lib/Author/Book/a.m4b", Size: 100, ModTime: now},
		{Path: "/lib/Author/Book/b.m4b", Size: 200, ModTime: now},
	}
	if !audiobookFolderUnchanged(files, onDisk) {
		t.Fatal("expected unchanged=true when sizes+mtimes match")
	}
}

func TestAudiobookFolderUnchangedSizeDiffers(t *testing.T) {
	now := time.Now().UTC()
	files := []*models.MediaFile{
		{FilePath: "/lib/Author/Book/a.m4b", FileSize: 100, FileModifiedAt: &now},
	}
	onDisk := []audiobookDiskFile{
		{Path: "/lib/Author/Book/a.m4b", Size: 101, ModTime: now},
	}
	if audiobookFolderUnchanged(files, onDisk) {
		t.Fatal("expected unchanged=false when size differs")
	}
}

func TestAudiobookFolderUnchangedNewFileAppeared(t *testing.T) {
	now := time.Now().UTC()
	files := []*models.MediaFile{
		{FilePath: "/lib/Author/Book/a.m4b", FileSize: 100, FileModifiedAt: &now},
	}
	onDisk := []audiobookDiskFile{
		{Path: "/lib/Author/Book/a.m4b", Size: 100, ModTime: now},
		{Path: "/lib/Author/Book/b.m4b", Size: 200, ModTime: now},
	}
	if audiobookFolderUnchanged(files, onDisk) {
		t.Fatal("expected unchanged=false when a new file appeared")
	}
}

func TestAudiobookFolderUnchangedFileDisappeared(t *testing.T) {
	now := time.Now().UTC()
	files := []*models.MediaFile{
		{FilePath: "/lib/Author/Book/a.m4b", FileSize: 100, FileModifiedAt: &now},
		{FilePath: "/lib/Author/Book/b.m4b", FileSize: 200, FileModifiedAt: &now},
	}
	onDisk := []audiobookDiskFile{
		{Path: "/lib/Author/Book/a.m4b", Size: 100, ModTime: now},
	}
	if audiobookFolderUnchanged(files, onDisk) {
		t.Fatal("expected unchanged=false when a file disappeared")
	}
}

func TestAudiobookFolderUnchangedMtimeDiffers(t *testing.T) {
	now := time.Now().UTC()
	earlier := now.Add(-time.Hour)
	files := []*models.MediaFile{
		{FilePath: "/lib/Author/Book/a.m4b", FileSize: 100, FileModifiedAt: &earlier},
	}
	onDisk := []audiobookDiskFile{
		{Path: "/lib/Author/Book/a.m4b", Size: 100, ModTime: now},
	}
	if audiobookFolderUnchanged(files, onDisk) {
		t.Fatal("expected unchanged=false when mtime differs")
	}
}

func TestAudiobookPeopleCreditsEqual(t *testing.T) {
	tests := []struct {
		name     string
		existing []models.ItemPerson
		desired  []audiobookCredit
		want     bool
	}{
		{
			name: "equal sets in same order",
			existing: []models.ItemPerson{
				{Person: models.Person{Name: "Author A"}, Kind: models.PersonKindAuthor, SortOrder: 0},
				{Person: models.Person{Name: "Narrator N"}, Kind: models.PersonKindNarrator, SortOrder: 1},
			},
			desired: []audiobookCredit{
				{Name: "Author A", Kind: models.PersonKindAuthor},
				{Name: "Narrator N", Kind: models.PersonKindNarrator},
			},
			want: true,
		},
		{
			name: "extra existing credit",
			existing: []models.ItemPerson{
				{Person: models.Person{Name: "Author A"}, Kind: models.PersonKindAuthor, SortOrder: 0},
				{Person: models.Person{Name: "Narrator N"}, Kind: models.PersonKindNarrator, SortOrder: 1},
			},
			desired: []audiobookCredit{
				{Name: "Author A", Kind: models.PersonKindAuthor},
			},
			want: false,
		},
		{
			name: "name case differs - still equal",
			existing: []models.ItemPerson{
				{Person: models.Person{Name: "AUTHOR A"}, Kind: models.PersonKindAuthor, SortOrder: 0},
			},
			desired: []audiobookCredit{
				{Name: "Author A", Kind: models.PersonKindAuthor},
			},
			want: true,
		},
		{
			name: "different kind",
			existing: []models.ItemPerson{
				{Person: models.Person{Name: "X"}, Kind: models.PersonKindAuthor, SortOrder: 0},
			},
			desired: []audiobookCredit{
				{Name: "X", Kind: models.PersonKindNarrator},
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := audiobookPeopleCreditsEqual(tt.existing, tt.desired); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFloatPtrEqual(t *testing.T) {
	a := 1.5
	b := 1.5
	c := 2.0
	cases := []struct {
		name string
		x, y *float64
		want bool
	}{
		{"both nil", nil, nil, true},
		{"left nil", nil, &a, false},
		{"right nil", &a, nil, false},
		{"equal", &a, &b, true},
		{"unequal", &a, &c, false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := floatPtrEqual(tt.x, tt.y); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}
