package jellycompat

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestRequestLoggerRedactsQueryCredentials(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	const target = "/Items?%41piKey=query-secret&API_KEY=legacy-secret&Limit=20"
	r := httptest.NewRequest(http.MethodGet, target, nil)
	h := requestLoggerMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != strings.SplitN(target, "?", 2)[1] {
			t.Fatal("logging changed the live query")
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	h.ServeHTTP(httptest.NewRecorder(), r)
	assertLogSecretsAbsent(t, logs.String(), "query-secret", "legacy-secret")
	for _, want := range []string{"Limit=20", `"status":202`, `"path":"/Items"`, "duration_ms"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("missing diagnostic %q: %s", want, logs.String())
		}
	}
}

func TestSeasonsPanicLoggerRedactsQueryCredentials(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	codec := NewResourceIDCodec()
	h := &ItemsHandler{codec: codec} // nil content triggers the recovery path.
	r := httptest.NewRequest(http.MethodGet, "/Shows/series/Seasons?ApiKey=panic-secret&Limit=20", nil)
	ctx := chi.NewRouteContext()
	ctx.URLParams.Add("id", codec.EncodeStringID(EncodedIDItem, "series"))
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, ctx))
	r = r.WithContext(context.WithValue(r.Context(), compatSessionKey, &Session{}))
	w := httptest.NewRecorder()
	h.HandleSeasons(w, r)
	if w.Code != http.StatusInternalServerError || !strings.Contains(logs.String(), "HandleSeasons panic") {
		t.Fatalf("panic path not exercised: status=%d log=%s", w.Code, logs.String())
	}
	assertLogSecretsAbsent(t, logs.String(), "panic-secret")
	if !strings.Contains(logs.String(), "Limit=20") {
		t.Fatal("panic log lost harmless query")
	}
}

func TestDebugLoggerRedactsCredentialsAndPreservesTraffic(t *testing.T) {
	const requestBody = `{"Username":"example","Pw":"password-secret","nested":[{"PASSWORD":"nested-secret"}],"Limit":20}`
	const responseBody = `{"AccessToken":"session-secret","Items":[{"Name":"Example","DirectStreamUrl":"/stream?api_key=stream-secret&Static=true"}]}`
	var logs bytes.Buffer
	h := newDebugLogMiddleware(&logs, "")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != requestBody || r.URL.Query().Get("ApiKey") != "query-secret" {
			t.Fatal("debug logging changed the request")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, responseBody)
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName?ApiKey=query-secret&Limit=20", strings.NewReader(requestBody)))
	if w.Body.String() != responseBody {
		t.Fatal("debug logging changed the response")
	}
	assertLogSecretsAbsent(t, logs.String(), "password-secret", "nested-secret", "session-secret", "stream-secret", "query-secret")
	for _, want := range []string{"Example", `"Limit": 20`, "Limit=20", "Static=true", "Status: 200", "[REDACTED]"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("missing diagnostic %q: %s", want, logs.String())
		}
	}
}

func TestDebugLoggerOmitsUnsafeBodiesWithoutTruncatingRequests(t *testing.T) {
	for _, body := range []string{
		`{"Pw":"malformed-secret"`,
		`Pw=form-secret`,
		strings.Repeat(" ", debugMaxBodyCapture) + `{"Pw":"oversize-secret"}`,
	} {
		t.Run(body[:8], func(t *testing.T) {
			var logs bytes.Buffer
			h := newDebugLogMiddleware(&logs, "")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got, err := io.ReadAll(r.Body)
				if err != nil || string(got) != body {
					t.Errorf("handler received %d bytes, want %d; error=%v", len(got), len(body), err)
				}
				w.Header().Set("Content-Type", "text/plain")
				_, _ = io.WriteString(w, "response-secret")
			}))
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
			assertLogSecretsAbsent(t, logs.String(), "malformed-secret", "form-secret", "oversize-secret", "response-secret")
			if !strings.Contains(logs.String(), "omitted") {
				t.Fatal("missing body omission diagnostic")
			}
		})
	}
}

func assertLogSecretsAbsent(t *testing.T, log string, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if strings.Contains(log, secret) {
			t.Errorf("log contains synthetic credential %q", secret)
		}
	}
}

type debugErrorBody struct {
	err    error
	closed bool
}

func (b *debugErrorBody) Read(p []byte) (int, error) {
	return copy(p, "partial"), b.err
}

func (b *debugErrorBody) Close() error {
	b.closed = true
	return b.err
}

func TestDebugRequestBodyPreservesReadAndCloseErrors(t *testing.T) {
	wantErr := errors.New("synthetic read error")
	original := &debugErrorBody{err: wantErr}
	body := &debugRequestBody{ReadCloser: original}
	buf := make([]byte, 16)
	n, err := body.Read(buf)
	if !errors.Is(err, wantErr) || string(buf[:n]) != "partial" || body.body.String() != "partial" {
		t.Fatalf("read changed: n=%d err=%v capture=%q", n, err, body.body.String())
	}
	if err := body.Close(); !errors.Is(err, wantErr) || !original.closed {
		t.Fatalf("Close not forwarded: %v", err)
	}
}

func TestDebugLoggerFilterLeavesRequestUnread(t *testing.T) {
	var logs bytes.Buffer
	original := io.NopCloser(strings.NewReader("unchanged"))
	h := newDebugLogMiddleware(&logs, "selected-client")(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if r.Body != original {
			t.Fatal("filtered request body was wrapped")
		}
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", original))
	if logs.Len() != 0 {
		t.Fatal("filtered request was logged")
	}
}
