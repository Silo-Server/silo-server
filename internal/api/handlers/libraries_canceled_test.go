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

// A client that leaves while its library list loads is not a failure to log:
// the only error is its own cancellation. A list that fails while the client
// waits still logs ERROR and answers 500.
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
	const failure = `level=ERROR msg="listing libraries"`

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.HandleListLibraries(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/libraries", nil).WithContext(ctx))
	if strings.Contains(logs.String(), failure) {
		t.Fatalf("canceled request logged as a failure: %q", logs.String())
	}

	waiting := httptest.NewRecorder()
	h.HandleListLibraries(waiting, httptest.NewRequest(http.MethodGet, "/api/v1/libraries", nil))
	if waiting.Code != http.StatusInternalServerError || !strings.Contains(logs.String(), failure) {
		t.Fatalf("failed list while the client waits: status=%d logs=%q", waiting.Code, logs.String())
	}
}
