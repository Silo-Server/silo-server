package jellycompat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scantrigger"
)

type fakeAutoscanFolders struct {
	folders []*models.MediaFolder
}

func (f *fakeAutoscanFolders) GetByID(_ context.Context, id int) (*models.MediaFolder, error) {
	for _, folder := range f.folders {
		if folder.ID == id {
			return folder, nil
		}
	}
	return nil, catalog.ErrFolderNotFound
}

func (f *fakeAutoscanFolders) List(context.Context) ([]*models.MediaFolder, error) {
	return f.folders, nil
}

type fakeAutoscanQueue struct {
	calls    []queuedScan
	batches  [][]scantrigger.Target
	batchErr error
}

type queuedScan struct {
	libraryID int
	mode      string
	path      string
	trigger   string
}

func (q *fakeAutoscanQueue) EnqueueScan(_ context.Context, folderID int, mode, path, trigger string) (bool, error) {
	q.calls = append(q.calls, queuedScan{libraryID: folderID, mode: mode, path: path, trigger: trigger})
	return true, nil
}

func (q *fakeAutoscanQueue) EnqueueScans(_ context.Context, targets []scantrigger.Target) error {
	copied := append([]scantrigger.Target(nil), targets...)
	q.batches = append(q.batches, copied)
	if q.batchErr != nil {
		return q.batchErr
	}
	for _, target := range targets {
		folderID := 0
		if target.Folder != nil {
			folderID = target.Folder.ID
		}
		q.calls = append(q.calls, queuedScan{
			libraryID: folderID,
			mode:      target.Mode,
			path:      target.Path,
			trigger:   target.Trigger,
		})
	}
	return nil
}

func TestAutoscanMediaUpdatedRejectsAmbiguousLibraryWithoutPartialEnqueue(t *testing.T) {
	root := t.TempDir()
	ambiguousRoot := t.TempDir()
	filePath := filepath.Join(root, "Movie.mkv")
	ambiguousPath := filepath.Join(ambiguousRoot, "Other.mkv")
	if err := os.WriteFile(filePath, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ambiguousPath, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	queue := &fakeAutoscanQueue{}
	handler := NewAutoscanHandler(&fakeAutoscanFolders{folders: []*models.MediaFolder{
		{
			ID:      4,
			Name:    "Movies",
			Type:    "movie",
			Enabled: true,
			Paths:   []string{root},
		},
		{
			ID:      5,
			Name:    "Movies A",
			Type:    "movie",
			Enabled: true,
			Paths:   []string{ambiguousRoot},
		},
		{
			ID:      6,
			Name:    "Movies B",
			Type:    "movie",
			Enabled: true,
			Paths:   []string{ambiguousRoot},
		},
	}}, queue, NewResourceIDCodec(), nil)

	payload := map[string]any{"Updates": []map[string]string{
		{"path": filePath, "updateType": "Modified"},
		{"path": ambiguousPath, "updateType": "Modified"},
	}}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/Library/Media/Updated", bytes.NewReader(data))
	rec := httptest.NewRecorder()

	handler.HandleMediaUpdated(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(queue.calls) != 0 {
		t.Fatalf("expected no partial enqueue, got %#v", queue.calls)
	}
}

func TestAutoscanMediaUpdatedIgnoresUnsupportedSidecars(t *testing.T) {
	root := t.TempDir()
	movieDir := filepath.Join(root, "Movie (2024)")
	if err := os.Mkdir(movieDir, 0o755); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(movieDir, "Movie.mkv")
	nfoPath := filepath.Join(movieDir, "Movie.nfo")
	posterPath := filepath.Join(movieDir, "poster.jpg")
	for _, path := range []string{filePath, nfoPath, posterPath} {
		if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	queue := &fakeAutoscanQueue{}
	handler := NewAutoscanHandler(&fakeAutoscanFolders{folders: []*models.MediaFolder{{
		ID:      5,
		Name:    "Movies",
		Type:    "movie",
		Enabled: true,
		Paths:   []string{root},
	}}}, queue, NewResourceIDCodec(), nil)

	payload := map[string]any{"Updates": []map[string]string{
		{"path": nfoPath, "updateType": "Modified"},
		{"path": filePath, "updateType": "Modified"},
		{"path": posterPath, "updateType": "Modified"},
	}}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/Library/Media/Updated", bytes.NewReader(data))
	rec := httptest.NewRecorder()

	handler.HandleMediaUpdated(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(queue.calls) != 1 {
		t.Fatalf("expected one parent scan, got %#v", queue.calls)
	}
	if queue.calls[0].libraryID != 5 || queue.calls[0].mode != "subtree" || queue.calls[0].path != movieDir || queue.calls[0].trigger != "jellyfin_autoscan" {
		t.Fatalf("unexpected parent scan: %#v", queue.calls[0])
	}
}

func TestAutoscanMediaUpdatedFallsBackToParentForMissingFiles(t *testing.T) {
	root := t.TempDir()
	movieDir := filepath.Join(root, "Movie (2024)")
	if err := os.Mkdir(movieDir, 0o755); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(movieDir, "Movie.mkv")
	missingPath := filepath.Join(movieDir, "pollermovie.mkv")
	if err := os.WriteFile(filePath, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	queue := &fakeAutoscanQueue{}
	handler := NewAutoscanHandler(&fakeAutoscanFolders{folders: []*models.MediaFolder{{
		ID:      8,
		Name:    "Movies",
		Type:    "movie",
		Enabled: true,
		Paths:   []string{root},
	}}}, queue, NewResourceIDCodec(), nil)

	payload := map[string]any{"Updates": []map[string]string{
		{"path": missingPath, "updateType": "Modified"},
	}}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/Library/Media/Updated", bytes.NewReader(data))
	rec := httptest.NewRecorder()

	handler.HandleMediaUpdated(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(queue.calls) != 1 {
		t.Fatalf("expected one queued scan, got %d", len(queue.calls))
	}
	if queue.calls[0].libraryID != 8 || queue.calls[0].mode != "subtree" || queue.calls[0].path != movieDir || queue.calls[0].trigger != "jellyfin_autoscan" {
		t.Fatalf("unexpected queued scan: %#v", queue.calls[0])
	}
}

func TestAutoscanMediaUpdatedHidesInternalQueueError(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "Movie.mkv")
	if err := os.WriteFile(filePath, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	queue := &fakeAutoscanQueue{batchErr: errors.New("database password leaked")}
	handler := NewAutoscanHandler(&fakeAutoscanFolders{folders: []*models.MediaFolder{{
		ID:      10,
		Name:    "Movies",
		Type:    "movie",
		Enabled: true,
		Paths:   []string{root},
	}}}, queue, NewResourceIDCodec(), nil)

	body := []byte(`{"Updates":[{"path":` + strconv.Quote(filePath) + `,"updateType":"Modified"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/Library/Media/Updated", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	handler.HandleMediaUpdated(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("database password leaked")) {
		t.Fatalf("response leaked internal error: %s", rec.Body.String())
	}
}
