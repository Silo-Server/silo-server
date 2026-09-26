package apiv2

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/policy"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This test drives the real v2 transport, handler service, PostgreSQL CAS, and
// post-commit application outcome. The configured database must be a test DB.
func TestAdminPolicyPostgresTransport(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	// The shared synthetic router authenticates administrator account 2.
	inserted, err := pool.Exec(ctx, `INSERT INTO users(id,username,email,password_hash,role,enabled) VALUES(2,'policy-v2-transport','policy-v2-transport@example.invalid','x','admin',true) ON CONFLICT(id) DO NOTHING`)
	if err != nil {
		t.Fatal(err)
	}
	if inserted.RowsAffected() > 0 {
		defer func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=2`) }()
	}
	if _, err := pool.Exec(ctx, `TRUNCATE policy_documents, policy_document_versions RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	store := policy.NewPolicyStore(pool)
	deps, _ := libraryDeps(t)
	reloadPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	reloadPool.Close()
	system := policy.NewSystem(policy.NewPolicyStore(reloadPool), nil, nil, policy.WithSystemEvalTimeout(2*time.Second))
	deps.AdminPolicy = handlers.NewPolicyHandler(system, store, policy.NewDecisionRepository(pool), func() bool { return true })
	h := newTestHandler(t, deps)
	base := Prefix + "/admin/policy/documents"
	created := do(t, h, "POST", base, `{"domain":"scope","name":"Transport fixture"}`, bearer(adminToken))
	if created.Code != 201 {
		t.Fatalf("create %d %s", created.Code, created.Body)
	}
	var doc AdminPolicyDocument
	if err := json.Unmarshal(created.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	path := created.Header().Get("Location")
	if path != base+"/"+string(doc.ID) {
		t.Fatal("wrong location")
	}
	defer func() { _, _ = pool.Exec(ctx, `DELETE FROM policy_documents WHERE id=$1`, string(doc.ID)) }()
	original := do(t, h, "GET", path, "", bearer(adminToken)).Header().Get("ETag")
	invalid := do(t, h, "POST", path+"/versions", `{"source":"invalid"}`, bearer(adminToken))
	if invalid.Code != 201 || !strings.Contains(invalid.Body.String(), `"compiled_ok":false`) {
		t.Fatalf("invalid saved draft %d %s", invalid.Code, invalid.Body)
	}
	var bad AdminPolicyVersion
	if err := json.Unmarshal(invalid.Body.Bytes(), &bad); err != nil {
		t.Fatal(err)
	}
	saved := do(t, h, "GET", invalid.Header().Get("Location"), "", bearer(adminToken))
	if saved.Code != 200 || !strings.Contains(saved.Body.String(), `"source":"invalid"`) {
		t.Fatalf("saved draft missing %d %s", saved.Code, saved.Body)
	}
	requireProblem(t, do(t, h, "PATCH", path, `{"enabled":false}`, with(bearer(adminToken), "If-Match", original)), TypePreconditionFailed)
	before := do(t, h, "GET", path, "", bearer(adminToken)).Header().Get("ETag")
	activateBad := fmt.Sprintf(`{"version_id":%q}`, bad.ID)
	requireProblem(t, do(t, h, "PUT", path+"/active-version", activateBad, with(bearer(adminToken), "If-Match", before)), TypeValidationFailed)
	if current := do(t, h, "GET", path, "", bearer(adminToken)).Header().Get("ETag"); current != before {
		t.Fatal("rejected activation advanced revision")
	}
	source := "package silo_custom.scope\nimport rego.v1\noverride(base, _) := base"
	body, _ := json.Marshal(AdminPolicyVersionCreate{Source: source})
	valid := do(t, h, "POST", path+"/versions", string(body), bearer(adminToken))
	if valid.Code != 201 {
		t.Fatalf("valid version %d %s", valid.Code, valid.Body)
	}
	var version AdminPolicyVersion
	if err := json.Unmarshal(valid.Body.Bytes(), &version); err != nil {
		t.Fatal(err)
	}
	if !version.CompiledOK {
		t.Fatalf("source did not compile: %s", valid.Body)
	}
	// Disable before activation to avoid any other enabled scope fixture.
	disabled := do(t, h, "PATCH", path, `{"enabled":false}`, with(bearer(adminToken), "If-Match", "*"))
	if disabled.Code != 200 {
		t.Fatalf("disable %d %s", disabled.Code, disabled.Body)
	}
	tag := disabled.Header().Get("ETag")
	active := do(t, h, "PUT", path+"/active-version", fmt.Sprintf(`{"version_id":%q}`, version.ID), with(bearer(adminToken), "If-Match", tag))
	if active.Code != 200 {
		t.Fatalf("activate %d %s", active.Code, active.Body)
	}
	var result AdminPolicyApplyResult
	if err := json.Unmarshal(active.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Persisted || result.Application.LocalApplied || result.PersistedGeneration <= 0 {
		t.Fatalf("persisted apply failure lost: %s", active.Body)
	}
	canonical := do(t, h, "GET", path, "", bearer(adminToken))
	if canonical.Header().Get("ETag") != active.Header().Get("ETag") || !strings.Contains(canonical.Body.String(), `"active_version":`) {
		t.Fatal("committed snapshot or validator mismatch")
	}
	var generation int64
	if err := pool.QueryRow(ctx, `SELECT generation FROM policy_generation WHERE id=true`).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	requireProblem(t, do(t, h, "PUT", path+"/active-version", fmt.Sprintf(`{"version_id":%q}`, version.ID), with(bearer(adminToken), "If-Match", tag)), TypePreconditionFailed)
	var after int64
	if err := pool.QueryRow(ctx, `SELECT generation FROM policy_generation WHERE id=true`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if generation != after {
		t.Fatal("stale replay advanced generation")
	}
	testAdminPolicyPages(t, pool, h, doc.ID)
}
