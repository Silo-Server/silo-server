package middleware

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// The panic log names the request path with any {token} or {secret}
// parameter redacted, so a crash on a credential-bearing route doesn't write
// the credential to the server log.
func TestRecovererRedactsSecretPathParams(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	const token = "s3cr3t-stream-token"
	router := chi.NewRouter()
	router.Use(Recoverer)
	router.Get("/stream/{token}", func(http.ResponseWriter, *http.Request) { panic("boom") })
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/stream/"+token, nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(logs.String(), token) || !strings.Contains(logs.String(), "/stream/[redacted]") {
		t.Fatalf("panic log = %q, want the path with its token redacted", logs.String())
	}
}

// The panic value is logged with its credential assignments masked, nested
// ones included: an error from a plugin or gRPC call can carry one in its
// text (desc = api_key=...), and the log handler redacts by key only.
func TestRecovererRedactsSecretsInThePanicValue(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	const secret = "s3cr3t-api-key"
	handler := Recoverer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(errors.New("rpc error: code = Unknown desc = api_key=" + secret))
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	if strings.Contains(logs.String(), secret) || !strings.Contains(logs.String(), "desc = api_key=[REDACTED]") {
		t.Fatalf("panic log = %q, want the panic value with its api_key masked", logs.String())
	}
}

// An aborted response is not a panic to answer: Recoverer passes
// http.ErrAbortHandler on, so net/http still breaks the connection.
func TestRecovererPassesAbortThrough(t *testing.T) {
	handler := Recoverer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) }))
	defer func() {
		if rec := recover(); rec != http.ErrAbortHandler { //nolint:errorlint // sentinel compared by identity, as net/http does
			t.Fatalf("recovered %v, want http.ErrAbortHandler", rec)
		}
	}()
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	t.Fatal("Recoverer swallowed http.ErrAbortHandler")
}
