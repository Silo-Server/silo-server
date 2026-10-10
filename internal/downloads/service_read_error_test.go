package downloads

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	err := svc.serveLocalFile(ctx, w, httptest.NewRequest("GET", "/file", nil), path, 2)
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
	err := (&Service{}).serveLocalFile(t.Context(), w, httptest.NewRequest("GET", "/file", nil), path, 2)
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
	err := (&Service{}).serveLocalFile(t.Context(), w, httptest.NewRequest("GET", "/file", nil), path, 2)
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
			if err := (&Service{}).serveLocalFile(t.Context(), w, req, path, 2); err != nil {
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
			if err := repo.Create(t.Context(), row); err != nil {
				t.Fatal(err)
			}
			svc := NewService(repo, nil, nil, fakeFileResolver{&models.MediaFile{ID: 42, FilePath: path}}, nil, nil, fakeUserRepo{&models.User{ID: 2, DownloadAllowed: new(true)}}, nil, nil, &config.DownloadConfig{Enabled: true})
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
