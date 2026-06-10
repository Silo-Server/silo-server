package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// fakeEbookReadStateStore is an in-memory EbookReaderProgressReadWriter keyed
// by content ID for a single (user, profile) scope.
type fakeEbookReadStateStore struct {
	rows map[string]EbookReaderProgress
}

func newFakeEbookReadStateStore() *fakeEbookReadStateStore {
	return &fakeEbookReadStateStore{rows: map[string]EbookReaderProgress{}}
}

func (f *fakeEbookReadStateStore) Get(_ context.Context, _ int, _ string, contentID string) (*EbookReaderProgress, error) {
	row, ok := f.rows[contentID]
	if !ok {
		return nil, nil
	}
	return &row, nil
}

func (f *fakeEbookReadStateStore) Upsert(_ context.Context, progress EbookReaderProgress) error {
	f.rows[progress.ContentID] = progress
	return nil
}

func (f *fakeEbookReadStateStore) Delete(_ context.Context, _ int, _ string, contentID string) error {
	delete(f.rows, contentID)
	return nil
}

func (f *fakeEbookReadStateStore) ListByContentIDs(_ context.Context, _ int, _ string, contentIDs []string) (map[string]EbookReaderProgress, error) {
	result := make(map[string]EbookReaderProgress, len(contentIDs))
	for _, contentID := range contentIDs {
		if row, ok := f.rows[contentID]; ok {
			result[contentID] = row
		}
	}
	return result, nil
}

// fakeEbookFileProvider implements EpisodeFileProvider for ebook file lookups.
type fakeEbookFileProvider struct {
	files map[string][]*models.MediaFile
	err   error
}

func (f *fakeEbookFileProvider) GetByContentID(_ context.Context, contentID string) ([]*models.MediaFile, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.files[contentID], nil
}

func (f *fakeEbookFileProvider) GetByEpisodeID(context.Context, string) ([]*models.MediaFile, error) {
	return nil, nil
}

func (f *fakeEbookFileProvider) ListByContentIDs(context.Context, []string) (map[string][]*models.MediaFile, error) {
	return nil, nil
}

func TestMarkEbookReadPreservesExistingFileAndLocation(t *testing.T) {
	ctx := context.Background()
	store := newFakeEbookReadStateStore()
	store.rows["ebook-1"] = EbookReaderProgress{
		UserID:    42,
		ProfileID: "profile-1",
		ContentID: "ebook-1",
		FileID:    7,
		Location:  "epubcfi(/6/14!/4/2/14)",
		Progress:  0.42,
		UpdatedAt: time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC),
	}
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)

	err := markEbookRead(ctx, store, 42, "profile-1", "ebook-1", now, func(context.Context) (int, error) {
		t.Fatal("defaultFileID must not be called when a progress row exists")
		return 0, nil
	})
	if err != nil {
		t.Fatalf("markEbookRead: %v", err)
	}

	row := store.rows["ebook-1"]
	if row.Progress != 1.0 {
		t.Fatalf("progress = %v, want 1.0", row.Progress)
	}
	if row.FileID != 7 || row.Location != "epubcfi(/6/14!/4/2/14)" {
		t.Fatalf("file/location not preserved: %+v", row)
	}
	if !row.UpdatedAt.Equal(now) {
		t.Fatalf("updated_at = %v, want %v", row.UpdatedAt, now)
	}
}

func TestMarkEbookReadUsesDefaultFileWhenNoProgressExists(t *testing.T) {
	ctx := context.Background()
	store := newFakeEbookReadStateStore()
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)

	err := markEbookRead(ctx, store, 42, "profile-1", "ebook-1", now, func(context.Context) (int, error) {
		return 12, nil
	})
	if err != nil {
		t.Fatalf("markEbookRead: %v", err)
	}

	row, ok := store.rows["ebook-1"]
	if !ok {
		t.Fatal("expected a progress row to be created")
	}
	if row.Progress != 1.0 || row.FileID != 12 || row.Location != "" {
		t.Fatalf("row = %+v, want progress 1.0 with file 12 and empty location", row)
	}
	if row.UserID != 42 || row.ProfileID != "profile-1" {
		t.Fatalf("row scope = user %d profile %q, want user 42 profile-1", row.UserID, row.ProfileID)
	}
}

func TestMarkEbookReadPropagatesDefaultFileError(t *testing.T) {
	ctx := context.Background()
	store := newFakeEbookReadStateStore()

	err := markEbookRead(ctx, store, 42, "profile-1", "ebook-1", time.Now().UTC(), func(context.Context) (int, error) {
		return 0, catalog.ErrItemNotFound
	})
	if !errors.Is(err, catalog.ErrItemNotFound) {
		t.Fatalf("err = %v, want catalog.ErrItemNotFound", err)
	}
	if len(store.rows) != 0 {
		t.Fatalf("no row must be written on error, got %+v", store.rows)
	}
}

func TestMarkEbookUnreadDeletesProgressRow(t *testing.T) {
	ctx := context.Background()
	store := newFakeEbookReadStateStore()
	store.rows["ebook-1"] = EbookReaderProgress{ContentID: "ebook-1", FileID: 7, Progress: 1.0}

	if err := markEbookUnread(ctx, store, 42, "profile-1", "ebook-1"); err != nil {
		t.Fatalf("markEbookUnread: %v", err)
	}
	if _, ok := store.rows["ebook-1"]; ok {
		t.Fatal("progress row must be deleted on mark unread")
	}
}

func TestPreferredEbookReadFilePrefersEpub(t *testing.T) {
	pdf := &models.MediaFile{ID: 1, BaseType: "ebook", FilePath: "/books/a.pdf", Container: "pdf"}
	epub := &models.MediaFile{ID: 2, BaseType: "ebook", FilePath: "/books/a.epub", Container: "epub"}
	video := &models.MediaFile{ID: 3, BaseType: "movie", FilePath: "/movies/a.mkv", Container: "mkv"}

	if got := preferredEbookReadFile([]*models.MediaFile{video, pdf, epub}); got == nil || got.ID != 2 {
		t.Fatalf("preferredEbookReadFile = %+v, want epub file 2", got)
	}
	if got := preferredEbookReadFile([]*models.MediaFile{video, pdf}); got == nil || got.ID != 1 {
		t.Fatalf("preferredEbookReadFile = %+v, want first reader-supported file 1", got)
	}
	if got := preferredEbookReadFile([]*models.MediaFile{video}); got != nil {
		t.Fatalf("preferredEbookReadFile = %+v, want nil without ebook files", got)
	}
}

func TestSetEbookReadStateMarkReadShowsPlayedUserState(t *testing.T) {
	ctx := context.Background()
	store := newFakeEbookReadStateStore()
	handler := &ItemsHandler{
		ebookProgressStore:  store,
		ebookReadStateStore: store,
		fileRepo: &fakeEbookFileProvider{files: map[string][]*models.MediaFile{
			"ebook-1": {{ID: 5, BaseType: "ebook", FilePath: "/books/b.epub", Container: "epub"}},
		}},
	}

	if err := handler.setEbookReadState(ctx, 42, "profile-1", "ebook-1", true, catalog.AccessFilter{}); err != nil {
		t.Fatalf("setEbookReadState(read): %v", err)
	}

	userStore := newProfileTestStore(t)
	items := []*models.MediaItem{{ContentID: "ebook-1", Type: "ebook", Title: "Book"}}
	states, err := resolveItemUserStatesWithOptions(ctx, userStore, "profile-1", nil, items, itemUserStateOptions{
		UserID:             42,
		EbookProgressStore: store,
	})
	if err != nil {
		t.Fatalf("resolveItemUserStatesWithOptions: %v", err)
	}
	if states["ebook-1"] == nil || !states["ebook-1"].Played {
		t.Fatalf("user state = %+v, want played after mark read", states["ebook-1"])
	}

	// A marked-read book is finished, not in progress: the reader-progress
	// response derived from the row must exclude it from Continue Reading.
	row := store.rows["ebook-1"]
	if row.Progress < models.EbookFinishedProgressThreshold {
		t.Fatalf("progress %v must cross the finished threshold", row.Progress)
	}

	if err := handler.setEbookReadState(ctx, 42, "profile-1", "ebook-1", false, catalog.AccessFilter{}); err != nil {
		t.Fatalf("setEbookReadState(unread): %v", err)
	}
	if _, ok := store.rows["ebook-1"]; ok {
		t.Fatal("mark unread must delete the reader progress row")
	}
}

func TestSetEbookReadStateFailsWithoutAccessibleFile(t *testing.T) {
	ctx := context.Background()
	store := newFakeEbookReadStateStore()
	handler := &ItemsHandler{
		ebookProgressStore:  store,
		ebookReadStateStore: store,
		fileRepo:            &fakeEbookFileProvider{},
	}

	err := handler.setEbookReadState(ctx, 42, "profile-1", "ebook-1", true, catalog.AccessFilter{})
	if !errors.Is(err, catalog.ErrItemNotFound) {
		t.Fatalf("err = %v, want catalog.ErrItemNotFound", err)
	}
}
