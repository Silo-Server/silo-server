package middleware

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Silo-Server/silo-server/internal/database/queryattr"
)

func TestRequestAttributionFieldsAndSkips(t *testing.T) {
	queryattr.Initialize()
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	for _, path := range []string{"/api/v1/items", "/api/v1/health", "/api/v1/ready", "/api/v1/admin/logs", "/api/v2/items"} {
		logs.Reset()
		s := queryattr.NewStore()
		tr := queryattr.Tracer{Store: s}
		var scope *queryattr.RequestScope
		h := RequestLogger("fixture")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, scope = queryattr.StartRequest(r.Context())
			ctx := tr.TraceQueryStart(r.Context(), nil, pgx.TraceQueryStartData{SQL: "SELECT 'PRIVATE_REQUEST_SQL', $1 FROM PRIVATE_REQUEST_TABLE", Args: []any{"PRIVATE_REQUEST_ARG"}})
			tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: &pgconn.PgError{Code: "23505", Message: "PRIVATE_REQUEST_ERROR", Detail: "PRIVATE_REQUEST_DETAIL", Hint: "PRIVATE_REQUEST_HINT"}})
			w.WriteHeader(http.StatusUnauthorized)
		}))
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
		if scope == nil || scope.Seal().Queries != 1 {
			t.Fatalf("path=%s scope=%+v want one query even on skipped paths", path, scope.Seal())
		}
		if path != "/api/v1/items" {
			if logs.Len() != 0 {
				t.Fatalf("skipped path=%s logged %s", path, logs.String())
			}
			continue
		}
		var fields map[string]any
		if err := json.Unmarshal(logs.Bytes(), &fields); err != nil {
			t.Fatal(err)
		}
		if fields["db_queries"] != float64(1) || fields["db_in_flight"] != float64(0) || fields["db_incomplete"] != false || fields["db_scheme"] != float64(queryattr.Scheme) {
			t.Fatalf("request fields=%v want one complete query and scheme", fields)
		}
		if strings.Contains(logs.String(), "PRIVATE_REQUEST_") || strings.Contains(logs.String(), ".go") {
			t.Fatalf("request leaked database data: %s", logs.String())
		}
	}
}

func TestRequestAttributionNoQueryAndCancellation(t *testing.T) {
	queryattr.Initialize()
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprint(canceled), func(t *testing.T) {
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			tr := queryattr.Tracer{Store: queryattr.NewStore()}
			var pending context.Context
			h := RequestLogger("fixture")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if canceled {
					pending = tr.TraceQueryStart(r.Context(), nil, pgx.TraceQueryStartData{SQL: "SELECT 1"})
					cancel()
					w.WriteHeader(http.StatusInternalServerError)
				}
			}))
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/items", nil))
			var got map[string]any
			if err := json.Unmarshal(logs.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if canceled {
				if got["status"] != float64(499) || got["db_queries"] != float64(1) || got["db_in_flight"] != float64(1) || got["db_incomplete"] != true {
					t.Fatalf("client closed fields=%v want 499 and one unfinished query", got)
				}
				tr.TraceQueryEnd(pending, nil, pgx.TraceQueryEndData{Err: context.Canceled})
				if totals := tr.Store.Totals(); totals.Outcomes[queryattr.Canceled] != 1 {
					t.Fatalf("canceled node totals=%+v want canceled=1", totals)
				}
			} else if got["db_queries"] != float64(0) || got["db_slowest_site"] != "none" || got["db_slowest_fp"] != "none" || got["db_incomplete"] != false {
				t.Fatalf("no-query fields=%v want explicit none and zero", got)
			}
		})
	}
}

func TestRequestAttributionRouting(t *testing.T) {
	queryattr.Initialize()
	for _, scenario := range []string{"success", "rejected", "missing", "method"} {
		t.Run(scenario, func(t *testing.T) {
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			tr := queryattr.Tracer{Store: queryattr.NewStore()}
			router := chi.NewRouter()
			router.Use(RequestLogger("fixture"))
			router.Use(func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					q := tr.TraceQueryStart(r.Context(), nil, pgx.TraceQueryStartData{SQL: "SELECT 1"})
					tr.TraceQueryEnd(q, nil, pgx.TraceQueryEndData{})
					if scenario == "rejected" {
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					next.ServeHTTP(w, r)
				})
			})
			router.Get("/api/v1/probe", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
			path, method, wantStatus := "/api/v1/probe", http.MethodGet, http.StatusOK
			switch scenario {
			case "rejected":
				wantStatus = http.StatusUnauthorized
			case "missing":
				path, wantStatus = "/api/v1/missing", http.StatusNotFound
			case "method":
				method, wantStatus = http.MethodPost, http.StatusMethodNotAllowed
			}
			router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, path, nil))
			var fields map[string]any
			if err := json.Unmarshal(logs.Bytes(), &fields); err != nil {
				t.Fatal(err)
			}
			if fields["msg"] != "api request" || fields["status"] != float64(wantStatus) || fields["db_queries"] != float64(1) || fields["db_incomplete"] != false {
				t.Fatalf("request fields=%v want api request, status=%d, one complete auth query", fields, wantStatus)
			}
			for _, key := range []string{"db_queries", "db_batches", "db_query_ns", "db_acquire_wait_ns", "db_slowest_site", "db_slowest_fp", "db_slowest_ns", "db_repeated", "db_in_flight", "db_incomplete", "db_scheme"} {
				if _, ok := fields[key]; !ok {
					t.Errorf("missing %s in %v", key, fields)
				}
			}
		})
	}
}

type attributionHijacker struct {
	*httptest.ResponseRecorder
	err error
}

func (w attributionHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, w.err
}

func TestRequestAttributionHijackAndPanic(t *testing.T) {
	queryattr.Initialize()
	for _, success := range []bool{false, true} {
		t.Run(fmt.Sprint(success), func(t *testing.T) {
			tr := queryattr.Tracer{Store: queryattr.NewStore()}
			var scope *queryattr.RequestScope
			var hijackErr error
			h := RequestLogger("fixture")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, scope = queryattr.StartRequest(r.Context())
				_, _, hijackErr = http.NewResponseController(w).Hijack()
				for range 11 {
					ctx := tr.TraceQueryStart(r.Context(), nil, pgx.TraceQueryStartData{SQL: "SELECT 1"})
					tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})
				}
			}))
			writer := attributionHijacker{ResponseRecorder: httptest.NewRecorder()}
			wantRepeated := uint64(0)
			if !success {
				writer.err, wantRepeated = errors.New("hijack refused"), 1
			}
			h.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/api/v1/items", nil))
			if got := scope.Seal(); got.Queries != 11 || got.Repeated != wantRepeated || !errors.Is(hijackErr, writer.err) {
				t.Fatalf("hijack=%v seal=%+v want queries=11 repeated=%d error=%v", hijackErr, got, wantRepeated, writer.err)
			}
		})
	}
	tr := queryattr.Tracer{Store: queryattr.NewStore()}
	var pending context.Context
	var scope *queryattr.RequestScope
	h := RequestLogger("fixture")(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, scope = queryattr.StartRequest(r.Context())
		pending = tr.TraceQueryStart(r.Context(), nil, pgx.TraceQueryStartData{SQL: "SELECT 1"})
		panic("fixture panic")
	}))
	func() {
		defer func() {
			if got := recover(); got != "fixture panic" {
				t.Errorf("panic=%v want fixture panic", got)
			}
		}()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/items", nil))
	}()
	tr.TraceQueryEnd(pending, nil, pgx.TraceQueryEndData{})
	if got := scope.Seal(); !got.Incomplete || got.InFlight != 1 {
		t.Fatalf("panic seal=%+v want immutable unfinished snapshot", got)
	}
}

func TestRequestAttributionDisabled(t *testing.T) {
	if os.Getenv("SILO_REQUEST_OFF_CHILD") != "1" {
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(t.Context(), exe, "-test.run=^TestRequestAttributionDisabled$")
		for _, value := range os.Environ() {
			if !strings.HasPrefix(value, "SILO_QUERY_ATTRIBUTION_ENABLED=") {
				cmd.Env = append(cmd.Env, value)
			}
		}
		cmd.Env = append(cmd.Env, "SILO_REQUEST_OFF_CHILD=1", "SILO_QUERY_ATTRIBUTION_ENABLED=false")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("off child=%v output=%s", err, output)
		}
		return
	}
	if queryattr.Initialize() {
		t.Fatal("attribution enabled; want process kill switch off")
	}
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	terminal := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler { return RequestLogger("fixture")(next) })
	router.Get("/api/v1/items", terminal)
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/items", nil))
	if logs.Len() == 0 || strings.Contains(logs.String(), "db_") {
		t.Fatalf("off log=%s want request without database fields", logs.String())
	}
}
