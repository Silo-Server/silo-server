package abs

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fileStreamTestStore struct {
	*stubMediaStore
	files []*models.MediaFile
}

func (s *fileStreamTestStore) GetMediaFiles(context.Context, string, catalog.AccessFilter) ([]*models.MediaFile, error) {
	return s.files, nil
}

func TestFileStreamNumericIdentifierIsResolvedByMediaType(t *testing.T) {
	dir := t.TempDir()
	firstPath := filepath.Join(dir, "first.bin")
	secondPath := filepath.Join(dir, "second.bin")
	if err := os.WriteFile(firstPath, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	files := []*models.MediaFile{{ID: 1, FilePath: firstPath}, {ID: 99, FilePath: secondPath}}

	tests := []struct {
		name     string
		itemType string
		wantBody string
	}{
		{name: "audiobook numeric value is raw track index", itemType: mediaTypeAudiobook, wantBody: "second"},
		{name: "ebook numeric value is media file id", itemType: mediaTypeEbook, wantBody: "first"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fileStreamTestStore{
				stubMediaStore: &stubMediaStore{known: map[string]*models.MediaItem{
					testBookID: {ContentID: testBookID, Type: tt.itemType},
				}},
				files: files,
			}
			h := New(Dependencies{MediaStore: store})
			rec := dispatchABSWithParams(http.MethodGet, "/api/items/book-1/file/1",
				map[string]string{libraryItemIDKey: testBookID, "ino": "1"}, nil, "1", "", h.handleFileStream)
			if rec.Code != http.StatusOK || rec.Body.String() != tt.wantBody {
				t.Fatalf("status/body = %d/%q, want 200/%q", rec.Code, rec.Body.String(), tt.wantBody)
			}
		})
	}
}
