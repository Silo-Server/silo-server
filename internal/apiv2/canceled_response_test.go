package apiv2

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A server error raised by the client's own cancellation is still written, but
// it is recorded as abandoned, not counted as a 5xx: the client has gone.
// Every other failure is recorded with its status.
func TestCanceledClientServerErrorIsAbandoned(t *testing.T) {
	canceled := func() context.Context {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}
	expired := func() context.Context {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		t.Cleanup(cancel)
		return ctx
	}
	for _, tc := range []struct {
		name       string
		ctx        func() context.Context
		problem    ProblemType
		wantSent   int
		wantLogged float64
		wantClass  string
	}{
		{"client gone", canceled, TypeInternalError, http.StatusInternalServerError, 0, "abandoned"},
		{"client gone, unavailable", canceled, TypeDependencyUnavailable, http.StatusServiceUnavailable, 0, "abandoned"},
		{"client gone, body read timeout", canceled, TypeRequestTimeout, http.StatusRequestTimeout, http.StatusRequestTimeout, "4xx"},
		{"client waiting", context.Background, TypeInternalError, http.StatusInternalServerError, http.StatusInternalServerError, "5xx"},
		{"deadline", expired, TypeInternalError, http.StatusInternalServerError, http.StatusInternalServerError, "5xx"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLogs(t)
			h := observe(bufferResponse(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeProblem(w, r, NewProblem(tc.problem, "An unexpected error occurred."))
			})))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, Prefix+"/libraries", nil).WithContext(tc.ctx()))
			if w.Code != tc.wantSent || w.Body.Len() == 0 {
				t.Fatalf("sent %d with %d body bytes, want the %d problem", w.Code, w.Body.Len(), tc.wantSent)
			}
			var line struct {
				Msg    string  `json:"msg"`
				Status float64 `json:"status"`
				Class  string  `json:"status_class"`
			}
			for _, raw := range bytes.Split(logs.Bytes(), []byte("\n")) {
				if bytes.Contains(raw, []byte(`"msg":"apiv2 request"`)) {
					if err := json.Unmarshal(raw, &line); err != nil {
						t.Fatal(err)
					}
				}
			}
			if line.Msg != "apiv2 request" {
				t.Fatalf("no request log line: %q", logs.String())
			}
			if line.Status != tc.wantLogged || line.Class != tc.wantClass {
				t.Fatalf("logged status %v class %q, want %v %q", line.Status, line.Class, tc.wantLogged, tc.wantClass)
			}
		})
	}
}

// net/http cancels the request context when an HTTP/1.1 client half-closes
// its side, though that client is still reading. Its server error has to
// arrive: a handler that writes nothing gets net/http's empty 200 instead.
func TestHalfClosedClientGetsServerError(t *testing.T) {
	handlerDone := make(chan error, 1)
	srv := httptest.NewServer(observe(bufferResponse(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done() // the half-close has reached the server
		handlerDone <- r.Context().Err()
		writeProblem(w, r, NewProblem(TypeInternalError, "An unexpected error occurred."))
	}))))
	t.Cleanup(srv.Close)

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := io.WriteString(conn, "POST "+Prefix+"/libraries HTTP/1.1\r\nHost: silo\r\nContent-Type: application/json\r\nContent-Length: 2\r\n\r\n{}"); err != nil {
		t.Fatal(err)
	}
	if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("reading response: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if ctxErr := <-handlerDone; ctxErr != context.Canceled { //nolint:errorlint // net/http cancels with the bare sentinel
		t.Fatalf("request context ended with %v, want context.Canceled", ctxErr)
	}
	if resp.StatusCode != http.StatusInternalServerError || len(body) == 0 {
		t.Fatalf("half-closed client got %s with body %q, want the 500 problem", resp.Status, body)
	}
}
