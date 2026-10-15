package nodecontrol

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/secret"
)

func newControlTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	var columns int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM information_schema.columns
		 WHERE table_name = 'stream_nodes' AND column_name = 'node_key_ciphertext'`).Scan(&columns); err != nil || columns != 1 {
		t.Skip("test database has not applied the node key migration")
	}
	return pool
}

// recordNode is the handler behind RequireNode: it echoes the verified node id
// and the body, so a test sees both what passed and what the handler got.
var recordNode = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	id, ok := NodeID(r.Context())
	if !ok {
		http.Error(w, "no node id", http.StatusInternalServerError)
		return
	}
	body, _ := io.ReadAll(r.Body)
	_, _ = fmt.Fprintf(w, "%d:%s", id, body)
})

func TestRequireNodeAuthenticatesTranscodeNodesByTheirOwnKey(t *testing.T) {
	pool := newControlTestPool(t)
	ctx := context.Background()
	repo := nodepool.NewRepository(pool)
	cipher, err := secret.New([]byte("node-control-test-master-key-0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	create := func(nodeType string) *nodepool.Node {
		t.Helper()
		suffix := fmt.Sprintf("%s-%d", nodeType, time.Now().UnixNano())
		node, err := repo.Create(ctx, nodepool.CreateNodeInput{Name: "control-" + suffix, Type: nodeType, URL: "http://control-" + suffix})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = repo.Delete(context.Background(), node.ID) })
		return node
	}
	issue := func(node *nodepool.Node) string {
		t.Helper()
		key, err := repo.IssueNodeKey(ctx, cipher, node.ID)
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	enabled, disabled, keyless, deleted, proxy := create(nodepool.NodeTypeTranscode), create(nodepool.NodeTypeTranscode),
		create(nodepool.NodeTypeTranscode), create(nodepool.NodeTypeTranscode), create(nodepool.NodeTypeProxy)
	enabledKey, disabledKey, deletedKey := issue(enabled), issue(disabled), issue(deleted)
	off := false
	if _, err := repo.Update(ctx, disabled.ID, nodepool.UpdateNodeInput{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	registry := nodepool.NewKeyRegistry(repo, cipher)
	if err := registry.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	handler := RequireNode(registry)(recordNode)
	serve := func(r *http.Request) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, r)
		return recorder
	}
	signed := func(nodeID int, key, body string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/api/internal/nodes/self/report", strings.NewReader(body))
		nodeauthtest.SignFromNode(t, r, nodeID, key)
		return r
	}

	for _, tc := range []struct {
		name string
		id   int
		key  string
	}{{"enabled", enabled.ID, enabledKey}, {"disabled", disabled.ID, disabledKey}} {
		got := serve(signed(tc.id, tc.key, `{"seq":1}`))
		if want := fmt.Sprintf(`%d:{"seq":1}`, tc.id); got.Code != http.StatusOK || got.Body.String() != want {
			t.Errorf("%s node: %d %q, want 200 %q", tc.name, got.Code, got.Body.String(), want)
		}
	}

	if err := repo.Delete(ctx, deleted.ID); err != nil {
		t.Fatal(err)
	}
	if err := registry.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	unsigned := httptest.NewRequest(http.MethodPost, "/api/internal/nodes/self/report", strings.NewReader(`{}`))
	coreSigned := httptest.NewRequest(http.MethodPost, "/api/internal/nodes/self/report", strings.NewReader(`{}`))
	nodeauthtest.Sign(t, coreSigned, enabledKey)
	for _, tc := range []struct {
		name string
		r    *http.Request
	}{
		{"deleted node", signed(deleted.ID, deletedKey, `{}`)},
		{"keyless node", signed(keyless.ID, "snk1_0_unused", `{}`)},
		{"proxy", signed(proxy.ID, "shared-secret", `{}`)},
		{"unknown node", signed(2_000_000_000, enabledKey, `{}`)},
		{"another node's key", signed(disabled.ID, enabledKey, `{}`)},
		{"unsigned", unsigned},
		{"core-to-node signature", coreSigned},
	} {
		got := serve(tc.r)
		if got.Code != http.StatusUnauthorized || got.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
			t.Errorf("%s: %d %q (%s), want a plain-text 401", tc.name, got.Code, got.Body.String(), got.Header().Get("Content-Type"))
		}
	}

	// A rotated key replaces the old one within one refresh.
	rotated := issue(enabled)
	if err := registry.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if got := serve(signed(enabled.ID, enabledKey, `{}`)); got.Code != http.StatusUnauthorized {
		t.Fatalf("old key after rotation: %d, want 401", got.Code)
	}
	if got := serve(signed(enabled.ID, rotated, `{}`)); got.Code != http.StatusOK {
		t.Fatalf("rotated key: %d %s, want 200", got.Code, got.Body.String())
	}
}

func TestNodeIDIsAbsentOutsideRequireNode(t *testing.T) {
	if id, ok := NodeID(context.Background()); ok || id != 0 {
		t.Fatalf("NodeID = %d, %v; want none", id, ok)
	}
}

type staticKeys map[int]string

func (k staticKeys) NodeKey(id int) (string, error) {
	if key, ok := k[id]; ok {
		return key, nil
	}
	return "", nodeauth.ErrNoNodeKey
}

func (k staticKeys) ReloadNodeKey(id int) (string, error) { return k.NodeKey(id) }

// The node id a body names is never the identity: only the signed header is.
func TestRequireNodeIgnoresANodeIDInTheBody(t *testing.T) {
	handler := RequireNode(staticKeys{3: "snk1_3_node-three-key"})(recordNode)
	r := httptest.NewRequest(http.MethodPost, "/api/internal/nodes/self/logs", strings.NewReader(`{"node_id":9}`))
	nodeauthtest.SignFromNode(t, r, 3, "snk1_3_node-three-key")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, r)
	if recorder.Code != http.StatusOK || !strings.HasPrefix(recorder.Body.String(), "3:") {
		t.Fatalf("got %d %q, want node 3", recorder.Code, recorder.Body.String())
	}
}

// A node the server has no key for gets a bare 401, which tells its operator
// nothing; the server logs the node and why, once a minute per node.
func TestRequireNodeLogsANodeWithoutAKey(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	const keyless = 9017
	handler := RequireNode(staticKeys{3: "snk1_3_node-three-key"})(recordNode)
	for range 3 {
		r := httptest.NewRequest(http.MethodPost, PathReport, strings.NewReader(`{}`))
		nodeauthtest.SignFromNode(t, r, keyless, "snk1_9017_old-key")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, r)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("keyless node: %d, want 401", recorder.Code)
		}
	}
	out := logs.String()
	if strings.Count(out, "has no key for the node it names") != 1 {
		t.Fatalf("want one warning for three refusals, got:\n%s", out)
	}
	if !strings.Contains(out, "node_id=9017") || !strings.Contains(out, `reason="no key issued`) {
		t.Fatalf("warning does not name the node and the reason:\n%s", out)
	}

	for err, want := range map[error]string{
		nodepool.ErrNodeNotFound:       "unknown or deleted node",
		nodepool.ErrNodeKeyNotIssuable: "not a transcode node",
		nodeauth.ErrNoNodeKey:          "no key issued, or its stored key cannot be decrypted",
	} {
		if got := noKeyReason(err); got != want {
			t.Fatalf("noKeyReason(%v) = %q, want %q", err, got, want)
		}
	}
}
