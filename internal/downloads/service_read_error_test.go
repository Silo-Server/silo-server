package downloads

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestLocalDownloadCanceledReadReturnsCommittedError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.mp4")
	payload := bytes.Repeat([]byte("synthetic media"), 8192)
	if err := os.WriteFile(path, payload, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	svc := &Service{bandwidth: NewBandwidthManager(131072, 0)}
	w := httptest.NewRecorder()
	err := svc.serveLocalFile(ctx, w, httptest.NewRequest("GET", "/file", nil), path, "", 2)
	if !errors.Is(err, ErrResponseCommitted) || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled body read must report a committed response error: %v", err)
	}
	if w.Body.Len() >= len(payload) {
		t.Fatalf("canceled transfer unexpectedly delivered the complete body: %d bytes", w.Body.Len())
	}
}

type interruptedResponseWriter struct {
	*httptest.ResponseRecorder
	cancel        context.CancelFunc
	beforeFailure func()
}

func (w *interruptedResponseWriter) Write([]byte) (int, error) {
	if w.beforeFailure != nil {
		w.beforeFailure()
	}
	if w.cancel != nil {
		w.cancel()
	}
	return 0, io.ErrClosedPipe
}
func TestLocalDownloadWriteFailureReturnsCommittedError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.mp4")
	if err := os.WriteFile(path, bytes.Repeat([]byte("media"), 8192), 0600); err != nil {
		t.Fatal(err)
	}
	w := &interruptedResponseWriter{ResponseRecorder: httptest.NewRecorder()}
	err := (&Service{}).serveLocalFile(t.Context(), w, httptest.NewRequest("GET", "/file", nil), path, "", 2)
	if !errors.Is(err, ErrResponseCommitted) || !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("write failure must be reported: %v", err)
	}
}

type truncatedResponseWriter struct {
	*httptest.ResponseRecorder
	path string
	t    *testing.T
}

func (w *truncatedResponseWriter) WriteHeader(status int) {
	if status == http.StatusOK {
		if err := os.Truncate(w.path, 16); err != nil {
			w.t.Fatal(err)
		}
	}
	w.ResponseRecorder.WriteHeader(status)
}
func TestLocalDownloadEarlyEOFReturnsCommittedError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.mp4")
	if err := os.WriteFile(path, bytes.Repeat([]byte("media"), 8192), 0600); err != nil {
		t.Fatal(err)
	}
	w := &truncatedResponseWriter{ResponseRecorder: httptest.NewRecorder(), path: path, t: t}
	err := (&Service{}).serveLocalFile(t.Context(), w, httptest.NewRequest("GET", "/file", nil), path, "", 2)
	if !errors.Is(err, ErrResponseCommitted) || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("short response must be reported: %v", err)
	}
}
func TestLocalDownloadCompleteResponses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.mp4")
	payload := bytes.Repeat([]byte("media"), 8192)
	if err := os.WriteFile(path, payload, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, ranged string
		length         int
	}{{"GET", "", len(payload)}, {"GET", "bytes=3-20", 18}, {"GET", "bytes=0-2,10-12", -1}, {"HEAD", "", 0}} {
		t.Run(tc.method+tc.ranged, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/file", nil)
			if tc.ranged != "" {
				req.Header.Set("Range", tc.ranged)
			}
			w := httptest.NewRecorder()
			if err := (&Service{}).serveLocalFile(t.Context(), w, req, path, "", 2); err != nil {
				t.Fatal(err)
			}
			if tc.length >= 0 && w.Body.Len() != tc.length {
				t.Fatalf("body length %d, want %d", w.Body.Len(), tc.length)
			}
		})
	}
}
func TestLocalDownloadCanceledWritePersistsFailureDB(t *testing.T) {
	for _, terminal := range []string{"", StatusCompleted, StatusCancelled} {
		t.Run("newer-status-"+terminal, func(t *testing.T) {
			repo := statusEventTestRepo(t)
			path := filepath.Join(t.TempDir(), "fixture.mp4")
			payload := bytes.Repeat([]byte("media"), 8192)
			if err := os.WriteFile(path, payload, 0600); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			row := &Download{ID: "canceled-write", UserID: 2, MediaFileID: 42, ContentID: "movie", Kind: KindQueued, Status: StatusQueued, Format: FormatOriginal, FileSize: int64(len(payload)), CreatedAt: now, UpdatedAt: now}
			seedSchemaFiles(t, repo, &models.MediaFile{ID: 42, ContentID: "movie"})
			if err := repo.Create(t.Context(), row); err != nil {
				t.Fatal(err)
			}
			svc := NewService(repo, nil, nil, fakeFileResolver{&models.MediaFile{ID: 42, FilePath: path}}, nil, nil, fakeUserRepo{&models.User{ID: 2, DownloadAllowed: new(true)}}, &syncAccess{}, nil, &config.DownloadConfig{Enabled: true})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			w := &interruptedResponseWriter{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
			if terminal != "" {
				w.beforeFailure = func() {
					var err error
					if terminal == StatusCancelled {
						err = repo.CancelByID(t.Context(), row.ID, 2)
					} else {
						err = repo.UpdateStatus(t.Context(), row.ID, terminal, row.FileSize, &now)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			err := svc.ServeFile(ctx, w, httptest.NewRequest("GET", "/file", nil).WithContext(ctx), 2, "", "", row.ID, catalog.AccessFilter{})
			if !errors.Is(err, ErrResponseCommitted) || !errors.Is(err, io.ErrClosedPipe) {
				t.Fatalf("write failure: %v", err)
			}
			stored, err := repo.GetByID(t.Context(), row.ID)
			if err != nil {
				t.Fatal(err)
			}
			expected := terminal
			if expected == "" {
				expected = StatusFailed
			}
			if stored.Status != expected {
				t.Fatalf("stored status %s, want %s", stored.Status, expected)
			}
		})
	}
}

// hiddenItemAccess hides one item, as a profile's rating limit or library
// restriction does, and admits every other.
type hiddenItemAccess struct{ hidden string }

func (a hiddenItemAccess) EnsureAccessible(_ context.Context, id string, _ catalog.AccessFilter) error {
	if id == a.hidden {
		return catalog.ErrItemNotFound
	}
	return nil
}

func (a hiddenItemAccess) EnsureAccessibleIDs(_ context.Context, ids []string, _ catalog.AccessFilter) (map[string]bool, error) {
	allowed := map[string]bool{}
	for _, id := range ids {
		if id != a.hidden {
			allowed[id] = true
		}
	}
	return allowed, nil
}

func TestEphemeralDownloadRechecksProfileAccessDB(t *testing.T) {
	repo := statusEventTestRepo(t)
	path := filepath.Join(t.TempDir(), "fixture.mp4")
	if err := os.WriteFile(path, []byte("media"), 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	// Another profile on the account queued these account-level rows. The
	// requesting profile's limits hide the newer one's item.
	row := &Download{ID: "shared-row", UserID: 2, MediaFileID: 42, ContentID: "movie", Kind: KindQueued, Status: StatusQueued, Format: FormatOriginal, FileSize: 5, CreatedAt: now, UpdatedAt: now}
	open := &Download{ID: "open-row", UserID: 2, MediaFileID: 43, ContentID: "open-movie", Kind: KindQueued, Status: StatusQueued, Format: FormatOriginal, FileSize: 5, CreatedAt: now.Add(-time.Minute), UpdatedAt: now}
	seedSchemaFiles(t, repo, &models.MediaFile{ID: 42, ContentID: "movie"}, &models.MediaFile{ID: 43, ContentID: "open-movie"})
	for _, d := range []*Download{row, open} {
		if err := repo.Create(t.Context(), d); err != nil {
			t.Fatal(err)
		}
	}
	newService := func(access ItemAccessChecker) *Service {
		return NewService(repo, nil, nil, fakeFileResolver{&models.MediaFile{ID: 42, FilePath: path}}, nil, nil, fakeUserRepo{&models.User{ID: 2, DownloadAllowed: new(true)}}, access, nil, &config.DownloadConfig{Enabled: true})
	}
	svc := newService(hiddenItemAccess{hidden: "movie"})
	ids := func(rows []*Download) []string {
		out := make([]string, 0, len(rows))
		for _, d := range rows {
			out = append(out, d.ID)
		}
		return out
	}

	w := httptest.NewRecorder()
	err := svc.ServeFile(t.Context(), w, httptest.NewRequest("GET", "/file", nil), 2, "restricted", "", row.ID, catalog.AccessFilter{})
	if !errors.Is(err, catalog.ErrItemNotFound) || w.Body.Len() != 0 {
		t.Fatalf("profile without access to the item: err %v, %d bytes served", err, w.Body.Len())
	}

	list, err := svc.List(t.Context(), 2, "restricted", "", catalog.AccessFilter{})
	if err != nil || !slices.Equal(ids(list), []string{open.ID}) {
		t.Fatalf("list for a profile without access to one item: %v, err %v", ids(list), err)
	}
	// A page position names a row's ID and creation time, so it never comes
	// from a hidden row, and a page has a next one only when a row the
	// profile may see follows. Here the hidden row is the newer one.
	page, err := svc.ListPage(t.Context(), 2, "restricted", "", nil, 1, catalog.AccessFilter{})
	if err != nil || !slices.Equal(ids(page.Items), []string{open.ID}) || page.Next != nil {
		t.Errorf("page behind a hidden row: %v, next %+v, err %v", ids(page.Items), page.Next, err)
	}
	// Visible rows with hidden ones between them, the second run longer than
	// one read holds: each page's position is its own last row.
	newest := &Download{ID: "newest-row", UserID: 2, MediaFileID: 43, ContentID: "open-movie", Kind: KindQueued, Status: StatusQueued, Format: FormatOriginal, FileSize: 5, CreatedAt: now.Add(time.Minute), UpdatedAt: now}
	oldest := &Download{ID: "oldest-row", UserID: 2, MediaFileID: 43, ContentID: "open-movie", Kind: KindQueued, Status: StatusQueued, Format: FormatOriginal, FileSize: 5, CreatedAt: now.Add(-time.Hour), UpdatedAt: now}
	added := []*Download{newest, oldest}
	for i := range registryPageMaxRows + 10 {
		added = append(added, &Download{ID: fmt.Sprintf("hidden-%03d", i), UserID: 2, MediaFileID: 42, ContentID: "movie", Kind: KindQueued, Status: StatusQueued, Format: FormatOriginal, FileSize: 5, CreatedAt: now.Add(-2*time.Minute - time.Duration(i)*time.Second), UpdatedAt: now})
	}
	if err := repo.CreateBatch(t.Context(), added); err != nil {
		t.Fatal(err)
	}
	var after *RegistryPosition
	for i, want := range []*Download{newest, open, oldest} {
		page, err := svc.ListPage(t.Context(), 2, "restricted", "", after, 1, catalog.AccessFilter{})
		more := want != oldest
		if err != nil || !slices.Equal(ids(page.Items), []string{want.ID}) || (page.Next != nil) != more || (more && page.Next.ID != want.ID) {
			t.Fatalf("page %d: %v, next %+v, err %v", i+1, ids(page.Items), page.Next, err)
		}
		after = page.Next
	}

	if err := svc.Delete(t.Context(), 2, "restricted", "", row.ID, catalog.AccessFilter{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete by a profile without access to the item: %v", err)
	}
	stored, err := repo.GetByID(t.Context(), row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusQueued {
		t.Fatalf("refused requests moved the row to %s", stored.Status)
	}

	// A failed access check must not fall back to the unfiltered rows, nor
	// let the delete through.
	failing := newService(&syncAccess{err: errors.New("catalog unavailable")})
	if list, err := failing.List(t.Context(), 2, "restricted", "", catalog.AccessFilter{}); err == nil || len(list) != 0 {
		t.Fatalf("list with a failing access check: %v, err %v", ids(list), err)
	}
	if page, err := failing.ListPage(t.Context(), 2, "restricted", "", nil, 1, catalog.AccessFilter{}); err == nil || len(page.Items) != 0 || page.Next != nil {
		t.Fatalf("page with a failing access check: %v, next %+v, err %v", ids(page.Items), page.Next, err)
	}
	if err := failing.Delete(t.Context(), 2, "restricted", "", open.ID, catalog.AccessFilter{}); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("delete with a failing access check: %v", err)
	}
	if _, err := repo.GetByID(t.Context(), open.ID); err != nil {
		t.Fatalf("delete with a failing access check removed the row: %v", err)
	}
}
