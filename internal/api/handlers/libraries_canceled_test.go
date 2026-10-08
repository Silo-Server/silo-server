package handlers

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// headerWatcher records whether a status line was written at all;
// httptest.ResponseRecorder reports 200 when nothing was.
type headerWatcher struct {
	*httptest.ResponseRecorder
	wrote bool
}

func (w *headerWatcher) WriteHeader(status int) {
	w.wrote = true
	w.ResponseRecorder.WriteHeader(status)
}

func (w *headerWatcher) Write(p []byte) (int, error) {
	w.wrote = true
	return w.ResponseRecorder.Write(p)
}

// A client that leaves while its library list loads gets no 500 written and
// no ERROR logged: the only failure is its own cancellation. A list that
// fails while the client waits is still an internal error.
func TestListLibrariesCanceledRequestIsNotAServerError(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://silo:silo@127.0.0.1:1/silo?connect_timeout=1")
	if err != nil {
		t.Fatalf("create unreachable pool: %v", err)
	}
	t.Cleanup(pool.Close)
	h := &LibraryHandler{folderRepo: catalog.NewFolderRepository(pool)}
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	gone := &headerWatcher{ResponseRecorder: httptest.NewRecorder()}
	h.HandleListLibraries(gone, httptest.NewRequest(http.MethodGet, "/api/v1/libraries", nil).WithContext(ctx))
	if gone.wrote || strings.Contains(logs.String(), "level=ERROR") {
		t.Fatalf("canceled request: written=%v status=%d logs=%q", gone.wrote, gone.Code, logs.String())
	}

	waiting := httptest.NewRecorder()
	h.HandleListLibraries(waiting, httptest.NewRequest(http.MethodGet, "/api/v1/libraries", nil))
	if waiting.Code != http.StatusInternalServerError || !strings.Contains(logs.String(), "level=ERROR") {
		t.Fatalf("failed list while the client waits: status=%d logs=%q", waiting.Code, logs.String())
	}
}
