package apiv2

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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

// A server error raised by the client's own cancellation is neither sent nor
// counted as a 5xx: the client has gone. Every other failure still is.
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
		name        string
		ctx         func() context.Context
		problem     ProblemType
		wantWritten bool
		wantStatus  float64
	}{
		{"client gone", canceled, TypeInternalError, false, 0},
		{"client gone, unavailable", canceled, TypeDependencyUnavailable, false, 0},
		{"client gone, body read timeout", canceled, TypeRequestTimeout, true, http.StatusRequestTimeout},
		{"client waiting", context.Background, TypeInternalError, true, http.StatusInternalServerError},
		{"deadline", expired, TypeInternalError, true, http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLogs(t)
			h := observe(bufferResponse(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeProblem(w, r, NewProblem(tc.problem, "An unexpected error occurred."))
			})))
			w := &headerWatcher{ResponseRecorder: httptest.NewRecorder()}
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, Prefix+"/libraries", nil).WithContext(tc.ctx()))
			if w.wrote != tc.wantWritten {
				t.Fatalf("response written = %v, want %v (status %d)", w.wrote, tc.wantWritten, w.Code)
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
			if line.Status != tc.wantStatus || (!tc.wantWritten && line.Class != "abandoned") {
				t.Fatalf("logged status %v class %q, want %v", line.Status, line.Class, tc.wantStatus)
			}
		})
	}
}
