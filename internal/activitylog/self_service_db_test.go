package activitylog

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/auditmutation"
	"github.com/Silo-Server/silo-server/internal/clientip"
)

// TestSelfServiceDomainRowsDB pins the request-row contract for operations
// outside /api/v2/admin: an opted-in v2 operation that commits a domain row
// replaces its request row, while v1, an unmarked v2 operation and a rolled
// back mutation keep the request row and write no domain row. A secret path
// segment never reaches the domain row.
func TestSelfServiceDomainRowsDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	target := fmt.Sprint(time.Now().UnixNano())

	var actor int
	if err := pool.QueryRow(t.Context(), `INSERT INTO users(username, email, password_hash, role) VALUES ($1, $2, 'x', 'user') RETURNING id`,
		"audit-self-service-"+target, "audit-self-service-"+target+"@example.invalid").Scan(&actor); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, actor) })
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM activity_log WHERE target_id = $1`, target)
	})
	mutate := func(optIn, commit bool, status int) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			if optIn {
				ctx = auditmutation.WithDomainAudit(ctx)
			}
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Error(err)
				return
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			entry, err := auditmutation.RecordMutation(ctx, tx, auditmutation.ActionPasswordChanged, auditmutation.TargetUser, target, status,
				auditmutation.SecretChanged(auditmutation.FieldPassword))
			if err != nil {
				t.Error(err)
				return
			}
			if !commit {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			if err := tx.Commit(ctx); err != nil {
				t.Error(err)
				return
			}
			auditmutation.CommitMutation(ctx, entry)
			w.WriteHeader(status)
		}
	}
	w := &captureWriter{}
	r := chi.NewRouter()
	r.Use(NewMiddleware(w, "test"), clientip.Middleware(clientip.NewResolver(nil)), func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
			GetLogContext(req.Context()).UserID = &actor
			next.ServeHTTP(rw, req)
		})
	})
	r.Post("/api/v2/account/password", mutate(true, true, http.StatusNoContent))
	r.Post("/api/v2/password-resets/{token}/complete", mutate(true, true, http.StatusOK))
	r.Post("/api/v2/account/unmarked", mutate(false, true, http.StatusNoContent))
	r.Post("/api/v1/auth/account/password", mutate(true, true, http.StatusNoContent))
	r.Post("/api/v2/account/rolled-back", mutate(true, false, http.StatusNoContent))

	domainRows := func() []string {
		t.Helper()
		rows, err := pool.Query(t.Context(), `SELECT path FROM activity_log WHERE target_id = $1 ORDER BY id`, target)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var paths []string
		for rows.Next() {
			var path string
			if err := rows.Scan(&path); err != nil {
				t.Fatal(err)
			}
			paths = append(paths, path)
		}
		return paths
	}
	for _, tc := range []struct {
		path        string
		domainPath  string
		requestRows int
	}{
		{path: "/api/v2/account/password", domainPath: "/api/v2/account/password"},
		{path: "/api/v2/password-resets/secret-link-token/complete", domainPath: "/api/v2/password-resets/[redacted]/complete"},
		{path: "/api/v2/account/unmarked", requestRows: 1},
		{path: "/api/v1/auth/account/password", requestRows: 1},
		{path: "/api/v2/account/rolled-back", requestRows: 1},
	} {
		before, logged := len(domainRows()), len(w.entries)
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, tc.path, nil))
		rows := domainRows()
		if got := len(w.entries) - logged; got != tc.requestRows {
			t.Fatalf("%s: %d request rows, want %d", tc.path, got, tc.requestRows)
		}
		if tc.domainPath == "" {
			if len(rows) != before {
				t.Fatalf("%s: wrote a domain row: %v", tc.path, rows)
			}
			continue
		}
		if len(rows) != before+1 || rows[len(rows)-1] != tc.domainPath {
			t.Fatalf("%s: domain rows %v, want %s", tc.path, rows, tc.domainPath)
		}
	}
}
