package apiv2

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/nodepool"
)

// Disabled registration and separate guarded activation use the registered
// native routes and production stores, including durable pool invalidation.
func nativeNodeCreationFixture(t *testing.T) (*pgxpool.Pool, *nodepool.Repository, http.Handler, string) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires a migrated private PostgreSQL fixture")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := nodepool.NewAdminConfigurationStore(pool)
	repo := nodepool.NewRepository(pool)
	owner := handlers.NewNodeHandler(repo, nil, nil, nil, nil, nil, "synthetic-node-secret")
	owner.SetConfigurationStore(store)
	owner.SetDrainStore(store)
	deps := pilotDeps(nil, nil)
	deps.AdminNodeConfiguration = owner
	deps.AdminNodeDrain = owner
	deps.AdminNodesRead = owner
	native := NewHandler(deps)
	url := "http://disabled-" + uuid.NewString() + ".invalid"
	t.Cleanup(func() {
		if _, err := pool.Exec(context.WithoutCancel(t.Context()), `DELETE FROM stream_nodes WHERE url=$1`, url); err != nil {
			t.Errorf("cleanup disabled node: %v", err)
		}
	})
	return pool, repo, native, url
}

func TestAdminNodeDisabledCreationPostgresProductionPath(t *testing.T) {
	pool, repo, native, url := nativeNodeCreationFixture(t)
	body := fmt.Sprintf(`{"name":"commissioned worker","type":"transcode","url":%q,"enabled":false}`, url)
	created := do(t, native, http.MethodPost, Prefix+"/admin/nodes", body, bearer(adminToken))
	var node AdminNode
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &node) != nil || node.Enabled {
		t.Fatalf("disabled registration: %d %s", created.Code, created.Body.String())
	}
	if node.ConfigETag == "" || created.Header().Get("ETag") != node.ConfigETag {
		t.Fatal("creation lacks committed configuration validator")
	}
	capabilities := do(t, native, http.MethodGet, Prefix+"/admin/nodes/capabilities", "", bearer(adminToken))
	var caps AdminNodeDrainCapabilities
	if capabilities.Code != http.StatusOK || json.Unmarshal(capabilities.Body.Bytes(), &caps) != nil || !caps.DisabledCreation || caps.State != StateAvailable || caps.Allowed == nil || !*caps.Allowed {
		t.Fatalf("disabled creation capability: %d %s", capabilities.Code, capabilities.Body.String())
	}
	assertExcluded := func() {
		t.Helper()
		enabled, err := repo.ListEnabled(t.Context(), nodepool.NodeTypeTranscode)
		if err != nil {
			t.Fatal(err)
		}
		for _, current := range enabled {
			if current.URL == url {
				t.Fatal("commissioning node was admitted to enabled placement")
			}
		}
	}
	assertExcluded()
	repeated := do(t, native, http.MethodPost, Prefix+"/admin/nodes", body, bearer(adminToken))
	var retry AdminNode
	if repeated.Code != http.StatusCreated || json.Unmarshal(repeated.Body.Bytes(), &retry) != nil || retry.ID != node.ID || retry.ConfigETag != node.ConfigETag || retry.Enabled {
		t.Fatalf("exact disabled retry: %d %s", repeated.Code, repeated.Body.String())
	}
	for _, opposite := range []string{
		fmt.Sprintf(`{"name":"commissioned worker","type":"transcode","url":%q}`, url),
		fmt.Sprintf(`{"name":"commissioned worker","type":"transcode","url":%q,"enabled":true}`, url),
	} {
		response := do(t, native, http.MethodPost, Prefix+"/admin/nodes", opposite, bearer(adminToken))
		if response.Code != http.StatusConflict {
			t.Fatalf("opposite registration changed disabled configuration: %d %s", response.Code, response.Body.String())
		}
	}
	path := Prefix + "/admin/nodes/" + string(node.ID)
	for _, tc := range []struct {
		headers map[string]string
		code    int
	}{{bearer(adminToken), 428}, {with(bearer(adminToken), "If-Match", `"stale"`), 412}} {
		response := do(t, native, http.MethodPut, path, `{"enabled":true}`, tc.headers)
		if response.Code != tc.code {
			t.Fatalf("unguarded activation: %d %s", response.Code, response.Body.String())
		}
		assertExcluded()
	}
	headers := bearer(adminToken)
	headers["If-Match"] = node.ConfigETag
	activated := do(t, native, http.MethodPut, path, `{"enabled":true}`, headers)
	var active AdminNode
	if activated.Code != http.StatusOK || json.Unmarshal(activated.Body.Bytes(), &active) != nil || !active.Enabled || active.ConfigETag == node.ConfigETag {
		t.Fatalf("guarded activation: %d %s", activated.Code, activated.Body.String())
	}
	late := do(t, native, http.MethodPost, Prefix+"/admin/nodes", body, bearer(adminToken))
	if late.Code != http.StatusConflict {
		t.Fatalf("late registration superseded activation: %d %s", late.Code, late.Body.String())
	}
	headers["If-Match"] = active.ConfigETag
	removed := do(t, native, http.MethodDelete, path, "", headers)
	if removed.Code != http.StatusNoContent {
		t.Fatalf("guarded commissioning cleanup: %d %s", removed.Code, removed.Body.String())
	}
	var remaining int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM stream_nodes WHERE url=$1`, url).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("cleanup left registration: %d %v", remaining, err)
	}
}

func TestAdminNodeDisabledPublicationFaultsPostgresProductionPath(t *testing.T) {
	pool, _, native, url := nativeNodeCreationFixture(t)
	body := fmt.Sprintf(`{"name":"publication worker","type":"proxy","url":%q,"enabled":false}`, url)
	name := "creation_fault_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	install := func(event, condition string) {
		t.Helper()
		_, err := pool.Exec(t.Context(), fmt.Sprintf(`CREATE FUNCTION %[1]s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
		IF NEW.url='%[2]s' AND (%[3]s) THEN RAISE EXCEPTION 'synthetic post-publication fault'; END IF;
		RETURN NEW; END $$;
		CREATE TRIGGER %[1]s AFTER %[4]s ON stream_nodes FOR EACH ROW EXECUTE FUNCTION %[1]s()`, name, url, condition, event))
		if err != nil {
			t.Fatal(err)
		}
	}
	remove := func() {
		t.Helper()
		if _, err := pool.Exec(context.WithoutCancel(t.Context()), "DROP TRIGGER IF EXISTS "+name+" ON stream_nodes; DROP FUNCTION IF EXISTS "+name+"()"); err != nil {
			t.Errorf("cleanup publication fault: %v", err)
		}
	}
	t.Cleanup(remove)
	generation := func() int64 {
		t.Helper()
		var value int64
		if err := pool.QueryRow(t.Context(), `SELECT generation FROM stream_node_pool_generation WHERE singleton`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	install("INSERT", "true")
	before := generation()
	failed := do(t, native, http.MethodPost, Prefix+"/admin/nodes", body, bearer(adminToken))
	if failed.Code != http.StatusInternalServerError || generation() != before {
		t.Fatalf("failed creation leaked durable publication: %d %s", failed.Code, failed.Body.String())
	}
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM stream_nodes WHERE url=$1`, url).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed creation leaked row: %d %v", count, err)
	}
	remove()
	created := do(t, native, http.MethodPost, Prefix+"/admin/nodes", body, bearer(adminToken))
	var node AdminNode
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &node) != nil || node.Enabled {
		t.Fatalf("creation after fault removal: %d %s", created.Code, created.Body.String())
	}
	install("UPDATE", "NEW.enabled IS DISTINCT FROM OLD.enabled")
	before = generation()
	headers := bearer(adminToken)
	headers["If-Match"] = node.ConfigETag
	path := Prefix + "/admin/nodes/" + string(node.ID)
	failed = do(t, native, http.MethodPut, path, `{"enabled":true}`, headers)
	if failed.Code != http.StatusInternalServerError || generation() != before {
		t.Fatalf("failed activation leaked durable publication: %d %s", failed.Code, failed.Body.String())
	}
	var enabled bool
	if err := pool.QueryRow(t.Context(), `SELECT enabled FROM stream_nodes WHERE url=$1`, url).Scan(&enabled); err != nil || enabled {
		t.Fatalf("failed activation leaked enabled row: %t %v", enabled, err)
	}
	remove()
	activated := do(t, native, http.MethodPut, path, `{"enabled":true}`, headers)
	var active AdminNode
	if activated.Code != http.StatusOK || json.Unmarshal(activated.Body.Bytes(), &active) != nil || !active.Enabled || active.ConfigETag == node.ConfigETag {
		t.Fatalf("same validator cannot recover rolled-back activation: %d %s", activated.Code, activated.Body.String())
	}
	headers["If-Match"] = active.ConfigETag
	removed := do(t, native, http.MethodDelete, path, "", headers)
	if removed.Code != http.StatusNoContent {
		t.Fatalf("publication cleanup: %d %s", removed.Code, removed.Body.String())
	}
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM stream_nodes WHERE url=$1`, url).Scan(&count); err != nil || count != 0 {
		t.Fatalf("publication cleanup left row: %d %v", count, err)
	}
}

func TestAdminNodeLateDisabledCreationPostgresProductionPath(t *testing.T) {
	pool, repo, native, url := nativeNodeCreationFixture(t)
	body := fmt.Sprintf(`{"name":"late worker","type":"transcode","url":%q,"enabled":false}`, url)
	name := "creation_wait_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	// The DB trigger stops the real native write after its insert but before
	// commit, modeling a caller that cannot yet know its registration outcome.
	var key int64
	if err := pool.QueryRow(t.Context(), `SELECT nextval('stream_node_admin_revision_seq')`).Scan(&key); err != nil {
		t.Fatal(err)
	}
	_, err := pool.Exec(t.Context(), fmt.Sprintf(`CREATE FUNCTION %[1]s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
	IF NEW.url='%[2]s' THEN PERFORM pg_advisory_xact_lock(%[3]d); END IF;
	RETURN NEW; END $$;
	CREATE TRIGGER %[1]s AFTER INSERT ON stream_nodes FOR EACH ROW EXECUTE FUNCTION %[1]s()`, name, url, key))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.WithoutCancel(t.Context()), "DROP TRIGGER IF EXISTS "+name+" ON stream_nodes; DROP FUNCTION IF EXISTS "+name+"()"); err != nil {
			t.Errorf("cleanup delayed creation: %v", err)
		}
	})
	lock, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Rollback(context.WithoutCancel(t.Context())) }()
	if _, err = lock.Exec(t.Context(), `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
		t.Fatal(err)
	}
	type creationResponse struct {
		status int
		body   []byte
	}
	responses := make(chan creationResponse, 1)
	errors := make(chan error, 1)
	listener := httptest.NewServer(native)
	t.Cleanup(listener.Close)
	go func() {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, listener.URL+Prefix+"/admin/nodes", strings.NewReader(body))
		if err != nil {
			errors <- err
			return
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", bearer(adminToken)["Authorization"])
		response, err := listener.Client().Do(request)
		if err != nil {
			errors <- err
			return
		}
		defer func() { _ = response.Body.Close() }()
		contents, err := io.ReadAll(response.Body)
		if err != nil {
			errors <- err
			return
		}
		responses <- creationResponse{status: response.StatusCode, body: contents}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err := pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND NOT granted AND objid::bigint=$1)`, key).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native registration did not reach blocked DB publication")
		}
		time.Sleep(time.Millisecond)
	}
	var remaining int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM stream_nodes WHERE url=$1`, url).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("uncommitted registration appeared to cleanup reader: %d %v", remaining, err)
	}
	if err := lock.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	var response creationResponse
	select {
	case response = <-responses:
	case err := <-errors:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("native delayed registration failed to complete")
	}
	var node AdminNode
	if response.status != http.StatusCreated || json.Unmarshal(response.body, &node) != nil || node.Enabled {
		t.Fatalf("late committed registration was enabled: %d %+v", response.status, node)
	}
	assertExcluded := func() {
		t.Helper()
		enabled, err := repo.ListEnabled(t.Context(), nodepool.NodeTypeTranscode)
		if err != nil {
			t.Fatal(err)
		}
		for _, current := range enabled {
			if current.URL == url {
				t.Fatal("late registration entered placement without activation")
			}
		}
	}
	assertExcluded()
	headers := bearer(adminToken)
	headers["If-Match"] = node.ConfigETag
	path := Prefix + "/admin/nodes/" + string(node.ID)
	if deleted := do(t, native, http.MethodDelete, path, "", headers); deleted.Code != http.StatusNoContent {
		t.Fatalf("exact late registration cleanup: %d %s", deleted.Code, deleted.Body.String())
	}
	// A second very late request may recreate a deleted natural URL. It remains
	// disabled; controllers must reconcile it and never infer finality from 404.
	recreated := do(t, native, http.MethodPost, Prefix+"/admin/nodes", body, bearer(adminToken))
	var late AdminNode
	if recreated.Code != http.StatusCreated || json.Unmarshal(recreated.Body.Bytes(), &late) != nil || late.ID == node.ID || late.Enabled {
		t.Fatalf("post-cleanup late request admitted work: %d %s", recreated.Code, recreated.Body.String())
	}
	assertExcluded()
	headers["If-Match"] = late.ConfigETag
	if deleted := do(t, native, http.MethodDelete, Prefix+"/admin/nodes/"+string(late.ID), "", headers); deleted.Code != http.StatusNoContent {
		t.Fatalf("recreated disabled registration cleanup: %d %s", deleted.Code, deleted.Body.String())
	}
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM stream_nodes WHERE url=$1`, url).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("late-write cleanup left row: %d %v", remaining, err)
	}
}

func TestAdminNodeCreationPreservesEnabledDefaultsPostgresProductionPath(t *testing.T) {
	pool, _, native, url := nativeNodeCreationFixture(t)
	for _, tc := range []struct {
		name, extra string
		bridge      bool
	}{{"native omitted", "", false}, {"native true", `,"enabled":true`, false}, {"frozen bridge ignores false", `,"enabled":false`, true}} {
		t.Run(tc.name, func(t *testing.T) {
			target := url + "/" + uuid.NewString()
			t.Cleanup(func() {
				if _, err := pool.Exec(context.WithoutCancel(t.Context()), `DELETE FROM stream_nodes WHERE url=$1`, target); err != nil {
					t.Errorf("cleanup default node: %v", err)
				}
			})
			body := fmt.Sprintf(`{"name":"default worker","type":"proxy","url":%q%s}`, target, tc.extra)
			route := native
			if tc.bridge {
				owner := handlers.NewNodeHandler(nodepool.NewRepository(pool), nil, nil, nil, nil, nil, "synthetic-node-secret")
				route = http.HandlerFunc(owner.HandleCreateNode)
			}
			created := do(t, route, http.MethodPost, Prefix+"/admin/nodes", body, bearer(adminToken))
			var node struct {
				Enabled bool `json:"enabled"`
			}
			if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &node) != nil || !node.Enabled {
				t.Fatalf("creation default changed: %d %s", created.Code, created.Body.String())
			}
		})
	}
}
