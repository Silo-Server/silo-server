package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
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

// fakeEbookPrimaryFileResolver stands in for the abs_ebook_primary_files
// lookup: configured reports whether a selection row exists at all, hasPrimary
// distinguishes a pinned file from the explicit "all files supplementary"
// state.
type fakeEbookPrimaryFileResolver struct {
	fileID     int
	configured bool
	hasPrimary bool
	err        error
	calls      int
}

func (f *fakeEbookPrimaryFileResolver) GetPrimaryEbookFileID(context.Context, string) (abs.EbookPrimarySelection, error) {
	f.calls++
	return abs.EbookPrimarySelection{FileID: f.fileID, Configured: f.configured, HasPrimary: f.hasPrimary}, f.err
}

// TestDefaultEbookFileIDHonorsCuratedPrimary pins the cross-surface agreement:
// a file pinned as primary through Audiobookshelf is the file a native
// mark-read points at, instead of the reader's EPUB-first guess.
func TestDefaultEbookFileIDHonorsCuratedPrimary(t *testing.T) {
	ctx := context.Background()
	files := map[string][]*models.MediaFile{
		"ebook-1": {
			{ID: 5, BaseType: "ebook", FilePath: "/books/b.epub", Container: "epub"},
			{ID: 6, BaseType: "ebook", FilePath: "/books/b.pdf", Container: "pdf"},
		},
	}

	tests := []struct {
		name     string
		resolver *fakeEbookPrimaryFileResolver
		want     int
	}{
		{
			name:     "no resolver falls back to the reader default",
			resolver: nil,
			want:     5,
		},
		{
			name:     "pinned primary wins over the EPUB default",
			resolver: &fakeEbookPrimaryFileResolver{fileID: 6, configured: true, hasPrimary: true},
			want:     6,
		},
		{
			name:     "all-supplementary selection falls back to the reader default",
			resolver: &fakeEbookPrimaryFileResolver{configured: true},
			want:     5,
		},
		{
			name:     "no selection row falls back to the reader default",
			resolver: &fakeEbookPrimaryFileResolver{},
			want:     5,
		},
		{
			name:     "primary outside the accessible files falls back",
			resolver: &fakeEbookPrimaryFileResolver{fileID: 99, configured: true, hasPrimary: true},
			want:     5,
		},
		{
			name:     "resolver failure must not block marking the book read",
			resolver: &fakeEbookPrimaryFileResolver{err: errors.New("boom"), configured: true, hasPrimary: true},
			want:     5,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := &ItemsHandler{fileRepo: &fakeEbookFileProvider{files: files}}
			if tt.resolver != nil {
				handler.SetEbookPrimaryFileResolver(tt.resolver)
			}

			fileID, err := handler.defaultEbookFileID(ctx, "ebook-1", catalog.AccessFilter{})
			if err != nil {
				t.Fatalf("defaultEbookFileID: %v", err)
			}
			if fileID != tt.want {
				t.Fatalf("file id = %d, want %d", fileID, tt.want)
			}
		})
	}
}

// TestDefaultEbookFileIDIgnoresPrimaryOutsideAccess keeps the curated pin
// subject to the caller's access filter: a primary in a library this viewer
// cannot see must not leak into their progress row.
func TestDefaultEbookFileIDIgnoresPrimaryOutsideAccess(t *testing.T) {
	ctx := context.Background()
	handler := &ItemsHandler{
		fileRepo: &fakeEbookFileProvider{files: map[string][]*models.MediaFile{
			"ebook-1": {
				{ID: 5, BaseType: "ebook", FilePath: "/books/b.epub", Container: "epub", MediaFolderID: 1},
				{ID: 6, BaseType: "ebook", FilePath: "/books/b.pdf", Container: "pdf", MediaFolderID: 2},
			},
		}},
	}
	handler.SetEbookPrimaryFileResolver(&fakeEbookPrimaryFileResolver{fileID: 6, configured: true, hasPrimary: true})

	fileID, err := handler.defaultEbookFileID(ctx, "ebook-1", catalog.AccessFilter{AllowedLibraryIDs: []int{1}})
	if err != nil {
		t.Fatalf("defaultEbookFileID: %v", err)
	}
	if fileID != 5 {
		t.Fatalf("file id = %d, want the accessible EPUB 5", fileID)
	}
}
