// Package audittest drives domain audit emitters in database tests: it builds
// the request context the activity middleware and router give a handler, and
// reads back the rows an emitter committed.
package audittest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/auditmutation"
	"github.com/Silo-Server/silo-server/internal/clientip"
)

// Request describes the request an emitter runs under.
type Request struct {
	Method string
	// Pattern is the chi route pattern; Path the concrete path it matched.
	// Path segments under {name} placeholders become route parameters.
	Pattern, Path string
	UserID        *int
	Impersonator  *int
	SessionID     string
	// OptIn marks the context the way the v2 registry marks an Audited
	// operation.
	OptIn bool
}

// Context returns the request context and its LogContext. Committed lists
// the entries CommitMutation published, as the middleware reads them. Rows
// the request writes are deleted when the test ends.
func Context(t *testing.T, pool *pgxpool.Pool, req Request) (context.Context, *auditmutation.LogContext) {
	t.Helper()
	nodeID := fmt.Sprintf("audittest-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM activity_log WHERE node_id = $1`, nodeID)
	})
	r := httptest.NewRequest(req.Method, req.Path, nil)
	route := chi.NewRouteContext()
	route.RoutePatterns = []string{req.Pattern}
	patternParts, pathParts := strings.Split(req.Pattern, "/"), strings.Split(req.Path, "/")
	for i, part := range patternParts {
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") && i < len(pathParts) {
			route.URLParams.Add(strings.Trim(part, "{}"), pathParts[i])
		}
	}
	lc := &auditmutation.LogContext{Request: r, Started: time.Now(), UserID: req.UserID,
		ImpersonatorUserID: req.Impersonator, SessionID: req.SessionID, NodeID: nodeID}
	ctx := context.WithValue(r.Context(), chi.RouteCtxKey, route)
	ctx = auditmutation.SetLogContext(ctx, lc)
	if req.OptIn {
		ctx = auditmutation.WithDomainAudit(ctx)
	}
	var scoped context.Context
	clientip.Middleware(clientip.NewResolver(nil))(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		scoped = r.Context()
	})).ServeHTTP(httptest.NewRecorder(), r.WithContext(ctx))
	lc.Request = r.WithContext(scoped)
	return scoped, lc
}

// Row is one committed domain row.
type Row struct {
	ID                 int64
	UserID             *int
	ImpersonatorUserID *int
	StatusCode         int
	Path, PathPattern  string
	Changes            []auditmutation.Change
	// Raw is the whole row as JSON, for asserting a secret appears nowhere.
	Raw string
}

// Rows reads the domain rows with this action and target, oldest first.
func Rows(t *testing.T, pool *pgxpool.Pool, action, targetType, targetID string) []Row {
	t.Helper()
	rows, err := pool.Query(context.WithoutCancel(t.Context()), `SELECT id, user_id, impersonator_user_id, status_code, path, path_pattern,
		COALESCE(changes, '[]'::jsonb)::text, row_to_json(a)::text FROM activity_log a
		WHERE action = $1 AND target_type = $2 AND target_id = $3 ORDER BY id`, action, targetType, targetID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var row Row
		var changes string
		if err := rows.Scan(&row.ID, &row.UserID, &row.ImpersonatorUserID, &row.StatusCode, &row.Path, &row.PathPattern, &changes, &row.Raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(changes), &row.Changes); err != nil {
			t.Fatal(err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// One asserts exactly one row and returns it.
func One(t *testing.T, pool *pgxpool.Pool, action, targetType, targetID string) Row {
	t.Helper()
	rows := Rows(t, pool, action, targetType, targetID)
	if len(rows) != 1 {
		t.Fatalf("%s %s #%s: %d domain rows, want 1: %+v", action, targetType, targetID, len(rows), rows)
	}
	return rows[0]
}

// Change returns the named change of a row, failing when it is missing.
func (r Row) Change(t *testing.T, field string) auditmutation.Change {
	t.Helper()
	for _, change := range r.Changes {
		if change.Field == field {
			return change
		}
	}
	t.Fatalf("row %d has no %q change: %s", r.ID, field, r.Raw)
	return auditmutation.Change{}
}

// Value is the JSON text of one side of a change, or "absent".
func Value(side *string) string {
	if side == nil {
		return "absent"
	}
	return *side
}

// AssertNoSecrets fails when any secret appears in the row.
func (r Row) AssertNoSecrets(t *testing.T, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if secret != "" && strings.Contains(r.Raw, secret) {
			t.Fatalf("row %d contains secret %q: %s", r.ID, secret, r.Raw)
		}
	}
}
