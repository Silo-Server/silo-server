package apiv2

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodeconfig"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/proxy"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

// The registered native routes, durable production stores, config watcher and
// assembled private proxy listener are all exercised together. Only the reply
// loss/concurrent mutation wrapper is synthetic.
func TestAdminNodeDrainPostgresProductionPath(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires a migrated private PostgreSQL fixture")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = pool.Exec(t.Context(), `INSERT INTO server_settings(key,value) VALUES ('server.identity_id',$1) ON CONFLICT(key) DO NOTHING`, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	store := nodepool.NewAdminConfigurationStore(pool)
	secret := "synthetic-worker-drain-secret"
	listener := httptest.NewUnstartedServer(nil)
	url := "http://" + listener.Listener.Addr().String()
	name := "drain-" + uuid.NewString()
	watchCtx, cancelWatch := context.WithCancel(t.Context())
	defer cancelWatch()
	watcher := nodeconfig.NewWatcher(pool, nil, &cache.NoopEventBus{}, nodeconfig.BootstrapOverrides{Mode: "proxy", NodeURL: url, NodeName: name})
	scratchDir := t.TempDir()
	watcher.OnLoad(func(cfg *config.Config) { cfg.Auth.JWTSecret = secret; cfg.Playback.TranscodeDir = scratchDir })
	if err = watcher.Start(watchCtx); err != nil {
		listener.Close()
		t.Fatal(err)
	}
	worker := proxy.NewServer(watcher, nil).Handler()
	unregistered := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/admin/drain", nil)
	request.Header.Set("Authorization", "Bearer "+secret)
	worker.ServeHTTP(unregistered, request)
	if unregistered.Code != http.StatusServiceUnavailable {
		listener.Close()
		t.Fatalf("worker before registration had authority: %d %s", unregistered.Code, unregistered.Body.String())
	}
	wrongControl := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/admin/drain", nil)
	request.Header.Set("Authorization", "Bearer synthetic-at-rest-master-key")
	worker.ServeHTTP(wrongControl, request)
	if wrongControl.Code != http.StatusUnauthorized {
		listener.Close()
		t.Fatalf("master key substituted for runtime control bearer: %d", wrongControl.Code)
	}
	node, err := store.Create(t.Context(), nodepool.CreateNodeInput{Name: name, Type: nodepool.NodeTypeProxy, URL: url})
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.WithoutCancel(t.Context())
		if _, err := pool.Exec(ctx, `DELETE FROM stream_nodes WHERE id=$1`, node.ID); err != nil {
			t.Errorf("cleanup node: %v", err)
		}
		var remaining int
		if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM stream_node_drain_fences WHERE node_id=$1`, node.ID).Scan(&remaining); err != nil || remaining != 0 {
			t.Errorf("cleanup fence: %d %v", remaining, err)
		}
	})
	request = httptest.NewRequest(http.MethodPost, "/admin/reload-config", nil)
	request.Header.Set("Authorization", "Bearer "+secret)
	reloaded := httptest.NewRecorder()
	worker.ServeHTTP(reloaded, request)
	if reloaded.Code != http.StatusOK && reloaded.Code != http.StatusNoContent {
		listener.Close()
		t.Fatalf("private registration reload failed: %d %s", reloaded.Code, reloaded.Body.String())
	}
	if got, ok := watcher.NodeRowID(); !ok || got != node.ID {
		listener.Close()
		t.Fatalf("worker identity: %d %t", got, ok)
	}
	var fault atomic.Int64
	listener.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/admin/drain" {
			worker.ServeHTTP(w, r)
			return
		}
		recorder := httptest.NewRecorder()
		worker.ServeHTTP(recorder, r)
		switch fault.Load() {
		case 1:
			connection, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = connection.Close()
			}
			return
		case 2:
			if _, _, err := store.Update(r.Context(), node.ID, nodepool.UpdateNodeInput{Group: new("concurrent-" + uuid.NewString())}, func(int64) error { return nil }); err != nil {
				t.Errorf("inject concurrent update: %v", err)
			}
		case 3, 4, 5, 7, 8, 9:
			var encoded map[string]any
			if err := json.Unmarshal(recorder.Body.Bytes(), &encoded); err != nil {
				t.Errorf("decode fault response: %v", err)
			}
			switch fault.Load() {
			case 3:
				delete(encoded, "active_jobs")
			case 4:
				encoded["active_requests"] = nil
			case 5:
				delete(encoded, "active_reservations")
			case 7:
				encoded["native_server_id"] = "foreign-deployment"
			case 8:
				delete(encoded, "native_server_id")
			case 9:
				encoded["observed_at"] = "2000-01-01T00:00:00Z"
			}
			recorder.Body.Reset()
			_ = json.NewEncoder(recorder.Body).Encode(encoded)
		case 6:
			_, _ = recorder.Body.WriteString(`{}`)
		}
		for name, values := range recorder.Header() {
			w.Header()[name] = values
		}
		w.WriteHeader(recorder.Code)
		_, _ = w.Write(recorder.Body.Bytes())
	})
	listener.Start()
	defer listener.Close()
	owner := handlers.NewNodeHandler(nodepool.NewRepository(pool), nil, nil, nil, nil, nil, secret)
	owner.SetConfigurationStore(store)
	owner.SetDrainStore(store)
	deps := pilotDeps(nil, nil)
	deps.AdminNodeConfiguration = owner
	deps.AdminNodeDrain = owner
	deps.AdminNodesRead = owner
	native := NewHandler(deps)
	path := fmt.Sprintf("%s/admin/nodes/%d/drain", Prefix, node.ID)
	readTag := func() string {
		t.Helper()
		response := do(t, native, http.MethodGet, Prefix+"/admin/nodes", "", bearer(adminToken))
		if response.Code != http.StatusOK {
			t.Fatal(response.Code, response.Body.String())
		}
		var page Collection[AdminNode]
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			if item.ID == IDFromInt(int64(node.ID)) {
				return item.ConfigETag
			}
		}
		t.Fatal("node absent from native list")
		return ""
	}
	tag := readTag()
	for _, tc := range []struct {
		headers map[string]string
		code    int
	}{{nil, 401}, {bearer(memberToken), 403}, {bearer(adminToken), 428}} {
		response := do(t, native, http.MethodPut, path, "", tc.headers)
		if response.Code != tc.code {
			t.Fatalf("authority: %d %s", response.Code, response.Body.String())
		}
	}
	stale := bearer(adminToken)
	stale["If-Match"] = `"stale"`
	if response := do(t, native, http.MethodPut, path, "", stale); response.Code != 412 {
		t.Fatal(response.Code, response.Body.String())
	}
	before, err := store.ReadDrain(t.Context(), node.ID)
	if err != nil || before.FenceID != "" || !before.Node.Enabled {
		t.Fatalf("rejected writes mutated: %+v %v", before, err)
	}
	valid := bearer(adminToken)
	valid["If-Match"] = tag
	fault.Store(1)
	if response := do(t, native, http.MethodPut, path, "", valid); response.Code != 503 {
		t.Fatal(response.Code, response.Body.String())
	}
	durable, err := store.ReadDrain(t.Context(), node.ID)
	if err != nil || durable.FenceID == "" || durable.Node.Enabled {
		t.Fatalf("lost reply lost fence: %+v %v", durable, err)
	}
	fault.Store(0)
	response := do(t, native, http.MethodGet, path, "", bearer(adminToken))
	var proof AdminNodeDrain
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &proof) != nil || !proof.Drained || !proof.Fenced || proof.FenceID != durable.FenceID || proof.ConfigETag == tag || proof.WorkerInstanceID == "" || proof.NativeServerID != durable.NativeServerID || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("fresh sealed proof: %d %s", response.Code, response.Body.String())
	}
	valid["If-Match"] = proof.ConfigETag
	for _, mode := range []int64{3, 4, 5, 6, 7, 8, 9} {
		fault.Store(mode)
		if response := do(t, native, http.MethodGet, path, "", bearer(adminToken)); response.Code != 503 {
			t.Fatalf("incomplete worker proof %d accepted: %d %s", mode, response.Code, response.Body.String())
		}
	}
	fault.Store(0)
	unchanged, err := store.ReadDrain(t.Context(), node.ID)
	if err != nil || unchanged.FenceID != durable.FenceID || unchanged.Node.AdminRevision != durable.Node.AdminRevision {
		t.Fatalf("malformed proof changed authority: %+v %v", unchanged, err)
	}
	configPath := fmt.Sprintf("%s/admin/nodes/%d", Prefix, node.ID)
	if response := do(t, native, http.MethodPut, configPath, `{"enabled":true}`, valid); response.Code != 409 {
		t.Fatalf("reenabled fenced worker: %d %s", response.Code, response.Body.String())
	}
	for _, body := range []string{`{"url":"http://replacement.invalid"}`, `{"name":"replacement"}`} {
		if response := do(t, native, http.MethodPut, configPath, body, valid); response.Code != 409 {
			t.Fatalf("repointed fenced worker: %d %s", response.Code, response.Body.String())
		}
	}
	fault.Store(2)
	if response := do(t, native, http.MethodGet, path, "", bearer(adminToken)); response.Code != 409 {
		t.Fatalf("changed revision yielded proof: %d %s", response.Code, response.Body.String())
	}
	fault.Store(0)
	if response := do(t, native, http.MethodDelete, path, "", valid); response.Code != 412 {
		t.Fatalf("stale cancellation: %d %s", response.Code, response.Body.String())
	}
	valid["If-Match"] = readTag()
	response = do(t, native, http.MethodDelete, path, "", valid)
	if response.Code != 204 {
		t.Fatalf("cancellation did not invalidate proof: %d %s", response.Code, response.Body.String())
	}
	canceled, err := store.ReadDrain(t.Context(), node.ID)
	if err != nil || canceled.FenceID != "" || canceled.Node.Enabled {
		t.Fatalf("cancellation state: %+v %v", canceled, err)
	}
	currentTag := readTag()
	if currentTag == valid["If-Match"] {
		t.Fatal("cancellation did not invalidate previous configuration validator")
	}
	valid["If-Match"] = currentTag
	if response := do(t, native, http.MethodPut, configPath, `{"enabled":true}`, valid); response.Code != 200 {
		t.Fatalf("explicit reenable: %d %s", response.Code, response.Body.String())
	}
	// An acknowledged guarded registration delete closes the gap between the
	// final zero receipt and exact process retirement. Cancellation before the
	// delete would invalidate this original validator; afterward the old node
	// identity cannot authorize another admission, even when its URL is reused.
	valid["If-Match"] = readTag()
	response = do(t, native, http.MethodPut, path, "", valid)
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &proof) != nil || !proof.Drained {
		t.Fatalf("retirement custody proof: %d %s", response.Code, response.Body.String())
	}
	valid["If-Match"] = proof.ConfigETag
	response = do(t, native, http.MethodDelete, configPath, "", valid)
	if response.Code != http.StatusNoContent {
		t.Fatalf("guarded retirement registration removal: %d %s", response.Code, response.Body.String())
	}
	privateRead := func(method, target string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, target, nil)
		request.Header.Set("Authorization", "Bearer "+secret)
		response := httptest.NewRecorder()
		worker.ServeHTTP(response, request)
		return response
	}
	if response := privateRead(http.MethodGet, "/admin/drain"); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("deleted registration remained authoritative: %d %s", response.Code, response.Body.String())
	}
	oldID := node.ID
	replacement, err := store.Create(t.Context(), nodepool.CreateNodeInput{Name: name, Type: nodepool.NodeTypeProxy, URL: url, Enabled: new(false)})
	if err != nil || replacement.ID == oldID || replacement.Enabled {
		t.Fatalf("disabled replacement registration: %+v %v", replacement, err)
	}
	node = replacement // Cleanup owns the exact replacement as well.
	if response := privateRead(http.MethodPost, "/admin/reload-config"); response.Code != http.StatusNoContent {
		t.Fatalf("replacement watcher reload: %d %s", response.Code, response.Body.String())
	}
	if response := privateRead(http.MethodGet, "/admin/drain"); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("old process adopted replacement authority: %d %s", response.Code, response.Body.String())
	}
	token, err := streamtoken.Sign(streamtoken.Claims{SessionID: "after-registration-delete", MediaPath: "/unused", PlayMethod: "direct"}, secret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if response := privateRead(http.MethodGet, "/stream/direct/"+token); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("old process reopened admission after URL reuse: %d %s", response.Code, response.Body.String())
	}
}
