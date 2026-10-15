package jellycompat

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

func TestRequestAttributionSurface(t *testing.T) {
	queryattr.Initialize()
	for _, scenario := range []string{"success", "no_query", "rejected", "missing", "method", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			tr := queryattr.Tracer{Store: queryattr.NewStore()}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var pending context.Context
			var authScope, handlerScope *queryattr.RequestScope
			router := chi.NewRouter()
			router.Use(observeCompatRequest)
			router.Use(func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, authScope = queryattr.StartRequest(r.Context())
					if scenario != "no_query" {
						q := tr.TraceQueryStart(r.Context(), nil, pgx.TraceQueryStartData{SQL: "SELECT $1 FROM PRIVATE_REQUEST_AUTH", Args: []any{"PRIVATE_REQUEST_ARGUMENT"}})
						tr.TraceQueryEnd(q, nil, pgx.TraceQueryEndData{})
					}
					next.ServeHTTP(w, r)
				})
			})
			router.Use(requestLoggerMiddleware)
			router.Use(func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if scenario == "rejected" {
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					next.ServeHTTP(w, r)
				})
			})
			router.Get("/probe", func(w http.ResponseWriter, r *http.Request) {
				_, handlerScope = queryattr.StartRequest(r.Context())
				if scenario == "no_query" {
					w.WriteHeader(http.StatusOK)
					return
				}
				q := tr.TraceQueryStart(r.Context(), nil, pgx.TraceQueryStartData{SQL: "SELECT 'PRIVATE_REQUEST_LITERAL', $$PRIVATE_REQUEST_DOLLAR$$, $1 FROM PRIVATE_REQUEST_TABLE /* PRIVATE_REQUEST_COMMENT */", Args: []any{[]string{"PRIVATE_REQUEST_ARRAY"}}})
				if scenario == "canceled" {
					pending = q
					cancel()
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				tr.TraceQueryEnd(q, nil, pgx.TraceQueryEndData{Err: &pgconn.PgError{Code: "23505", Message: "PRIVATE_REQUEST_ERROR", Detail: "PRIVATE_REQUEST_DETAIL", Hint: "PRIVATE_REQUEST_HINT"}})
				w.WriteHeader(http.StatusOK)
			})
			var h http.Handler = router

			path, method := "/probe", http.MethodGet
			wantQueries, wantStatus := float64(2), float64(200)
			switch scenario {
			case "no_query":
				wantQueries = 0
			case "rejected":
				wantQueries, wantStatus = 1, 401
			case "missing":
				path, wantQueries, wantStatus = "/missing", 1, 404
			case "method":
				method, wantQueries, wantStatus = http.MethodPost, 1, 405
			case "canceled":
				wantStatus = 500
			}
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(ctx, method, path, nil))
			var fields map[string]any
			if err := json.Unmarshal(logs.Bytes(), &fields); err != nil {
				t.Fatalf("log=%s: %v", logs.String(), err)
			}
			if fields["msg"] != "jellycompat request" || fields["db_queries"] != wantQueries || fields["status"] != wantStatus || fields["db_scheme"] != float64(queryattr.Scheme) {
				t.Fatalf("request fields=%v want queries=%v status=%v scheme=%d", fields, wantQueries, wantStatus, queryattr.Scheme)
			}
			for _, key := range []string{"db_batches", "db_query_ns", "db_acquire_wait_ns", "db_slowest_site", "db_slowest_fp", "db_slowest_ns", "db_repeated", "db_in_flight", "db_incomplete"} {
				if _, ok := fields[key]; !ok {
					t.Errorf("missing %s in %v", key, fields)
				}
			}
			if scenario == "no_query" && (fields["db_slowest_site"] != "none" || fields["db_slowest_fp"] != "none" || fields["db_incomplete"] != false) {
				t.Fatalf("no-query fields=%v want explicit none and complete", fields)
			}
			if handlerScope != nil && handlerScope != authScope {
				t.Fatalf("auth scope=%p handler scope=%p want same owner", authScope, handlerScope)
			}
			if strings.Contains(logs.String(), "PRIVATE_REQUEST_") || strings.Contains(logs.String(), ".go") {
				t.Fatalf("private database data in log: %s", logs.String())
			}
			if scenario == "canceled" {
				if fields["db_in_flight"] != float64(1) || fields["db_incomplete"] != true {
					t.Fatalf("canceled fields=%v want unfinished and incomplete", fields)
				}
				tr.TraceQueryEnd(pending, nil, pgx.TraceQueryEndData{Err: context.Canceled})
				if seal := authScope.Seal(); seal.InFlight != 1 || !seal.Incomplete {
					t.Fatalf("late end changed snapshot=%+v", seal)
				}
				if totals := tr.Store.Totals(); totals.Outcomes[queryattr.Canceled] != 1 {
					t.Fatalf("node totals=%+v want canceled=1", totals)
				}

			}
		})
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
	router.Use(func(next http.Handler) http.Handler { return requestLoggerMiddleware(next) })
	router.Get("/probe", terminal)
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/probe", nil))
	if logs.Len() == 0 || strings.Contains(logs.String(), "db_") {
		t.Fatalf("off log=%s want request without database fields", logs.String())
	}
}

type requestAttributionHijacker struct {
	*httptest.ResponseRecorder
	err error
}

func (w requestAttributionHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, w.err
}

func TestRequestAttributionLifecycle(t *testing.T) {
	queryattr.Initialize()
	for _, success := range []bool{false, true} {
		t.Run(fmt.Sprint(success), func(t *testing.T) {
			tr := queryattr.Tracer{Store: queryattr.NewStore()}
			var scope *queryattr.RequestScope
			var gotErr error
			terminal := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, scope = queryattr.StartRequest(r.Context())
				_, _, gotErr = http.NewResponseController(w).Hijack()
				for range 11 {
					q := tr.TraceQueryStart(r.Context(), nil, pgx.TraceQueryStartData{SQL: "SELECT 1"})
					tr.TraceQueryEnd(q, nil, pgx.TraceQueryEndData{})
				}
			})
			writer := requestAttributionHijacker{ResponseRecorder: httptest.NewRecorder()}
			want := uint64(0)
			if !success {
				writer.err, want = errors.New("hijack refused"), 1
			}
			router := chi.NewRouter()
			router.Use(func(next http.Handler) http.Handler { return requestLoggerMiddleware(next) })
			router.Get("/probe", terminal)
			router.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/probe", nil))
			if got := scope.Seal(); got.Queries != 11 || got.Repeated != want || !errors.Is(gotErr, writer.err) {
				t.Fatalf("hijack=%v scope=%+v want eleven queries, repeats=%d, error=%v", gotErr, got, want, writer.err)
			}
		})
	}
	tr := queryattr.Tracer{Store: queryattr.NewStore()}
	var scope *queryattr.RequestScope
	var pending context.Context
	terminal := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, scope = queryattr.StartRequest(r.Context())
		pending = tr.TraceQueryStart(r.Context(), nil, pgx.TraceQueryStartData{SQL: "SELECT 1"})
		panic("fixture panic")
	})
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler { return requestLoggerMiddleware(next) })
	router.Get("/probe", terminal)
	func() {
		defer func() {
			if got := recover(); got != "fixture panic" {
				t.Errorf("panic=%v want fixture panic", got)
			}
		}()
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/probe", nil))
	}()
	tr.TraceQueryEnd(pending, nil, pgx.TraceQueryEndData{})
	if got := scope.Seal(); got.InFlight != 1 || !got.Incomplete {
		t.Fatalf("panic seal=%+v want frozen unfinished query", got)
	}
}
