# Plugin-Platform SDK Helpers Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extract the duplicated request_router plugin platform code (HTTP client + manifest self-checksum + serve bootstrap) into shared `silo-plugin-sdk` helpers, then migrate the arr and seerr plugins onto them.

**Architecture:** Three new SDK pieces — `pkg/pluginsdk/httpclient` (one credential-carrying JSON client with a single `*StatusError` error model that swallows empty 2xx bodies), `manifest.LoadWithChecksum` (embed → version → sha256-of-running-binary → Checksum), and `runtime.ServeManifest` (collapses `main.go`, wires the host broker inline). Then both plugins delete their copied client + boilerplate and adopt the helpers.

**Tech Stack:** Go 1.26 (pure Go, no CGO), `github.com/Silo-Server/silo-plugin-sdk` (local `replace`), `net/http` + `net/http/httptest`.

**Spec:** `docs/superpowers/specs/2026-06-08-plugin-platform-sdk-helpers-design.md`

**Conventions for every task:**
- Go toolchain: `/opt/deployarr/.local/go-sdk/go/bin/go` (referred to as `go` below — always use the full path).
- LOCAL-ONLY: `git commit` only — never push/tag/PR. The `replace => /opt/silo-plugin-sdk` in each plugin's `go.mod` stays.
- The Edit/Write tools are path-guarded outside `/opt/silo`. All three repos here (`/opt/silo-plugin-sdk`, `/opt/silo-plugin-requests-arr`, `/opt/silo-plugin-requests-seerr`) are outside `/opt/silo`, so create/modify their files via **Bash heredocs** (`cat > file <<'EOF' … EOF`), and for edits to existing files, rewrite the whole file (or the changed funcs) via heredoc after reading it.
- `gofmt` is invoked as `$(/opt/deployarr/.local/go-sdk/go/bin/go env GOROOT)/bin/gofmt`.
- SDK branch: `feat/request-router-capability` (leave as-is). Plugin branches: `master` each (leave as-is).

---

## File Structure

```
/opt/silo-plugin-sdk/pkg/pluginsdk/
  httpclient/
    httpclient.go        # Client, New, DoJSON/GetJSON/PostJSON, StatusError, parseMessage (Task 1)
    httpclient_test.go
  manifest/
    checksum.go          # LoadWithChecksum (Task 2)
    checksum_test.go
  runtime/
    serve_manifest.go    # manifestRuntime + ServeManifest (Task 3)
    serve_manifest_test.go

/opt/silo-plugin-requests-seerr/        # Task 4 (migrate)
  internal/seerr/client.go     -> DELETED
  internal/seerr/errors.go     -> NEW (ErrNotFound moves here)
  internal/seerr/api.go        -> free funcs taking *httpclient.Client
  internal/seerr/client_test.go-> DELETED (HTTP tested in SDK)
  internal/seerr/api_test.go   -> retargeted
  internal/router/server.go    -> httpclient.New + StatusError checks
  main.go                      -> runtime.ServeManifest

/opt/silo-plugin-requests-arr/          # Task 5 (migrate)
  internal/arr/client.go       -> DELETED
  internal/arr/resources.go    -> *Client -> *httpclient.Client
  internal/arr/radarr.go       -> httpclient.New + id==0 empty-body recovery
  internal/arr/sonarr.go       -> httpclient.New + id==0 empty-body recovery
  main.go                      -> runtime.ServeManifest
```

---

## Task 1: SDK `httpclient` package

The one shared credential-carrying JSON client.

**Files:**
- Create: `/opt/silo-plugin-sdk/pkg/pluginsdk/httpclient/httpclient.go`
- Test: `/opt/silo-plugin-sdk/pkg/pluginsdk/httpclient/httpclient_test.go`

- [ ] **Step 1: Write the failing test**

```bash
mkdir -p /opt/silo-plugin-sdk/pkg/pluginsdk/httpclient
cat > /opt/silo-plugin-sdk/pkg/pluginsdk/httpclient/httpclient_test.go <<'EOF'
package httpclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPostJSONSetsApiKeyAndDecodes(t *testing.T) {
	var key, method, ct string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, method, ct = r.Header.Get("X-Api-Key"), r.Method, r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":7}`))
	}))
	defer srv.Close()
	var out struct {
		ID int `json:"id"`
	}
	if err := New(srv.URL, "secret", nil).PostJSON(context.Background(), "/x", map[string]any{"a": 1}, &out); err != nil {
		t.Fatalf("PostJSON: %v", err)
	}
	if key != "secret" || method != http.MethodPost || ct != "application/json" || out.ID != 7 {
		t.Fatalf("got key=%q method=%q ct=%q id=%d", key, method, ct, out.ID)
	}
}

func TestStatusErrorParsesMessageWithRawFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/json":
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte(`{"message":"dup"}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`boom`))
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "k", nil)

	err := c.GetJSON(context.Background(), "/json", nil)
	var se *StatusError
	if !errors.As(err, &se) || se.StatusCode != 409 || se.Message != "dup" {
		t.Fatalf("want StatusError{409,dup}, got %v", err)
	}
	err = c.GetJSON(context.Background(), "/raw", nil)
	if !errors.As(err, &se) || se.StatusCode != 500 || se.Message != "boom" || se.Body != "boom" {
		t.Fatalf("want StatusError{500,boom,boom}, got %v", err)
	}
}

func TestEmpty2xxBodyIsSuccessButTruncatedIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/empty":
			w.WriteHeader(http.StatusCreated) // no body
		case "/truncated":
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":1`)) // cut off mid-JSON
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "k", nil)

	var out struct {
		ID int `json:"id"`
	}
	if err := c.PostJSON(context.Background(), "/empty", nil, &out); err != nil {
		t.Fatalf("empty 2xx body should be success, got %v", err)
	}
	if out.ID != 0 {
		t.Fatalf("empty body should leave zero value, got %d", out.ID)
	}
	out.ID = 5
	if err := c.PostJSON(context.Background(), "/truncated", nil, &out); err == nil {
		t.Fatal("truncated body should be a decode error, got nil")
	}
}

func TestRequiresBaseURLAndKeyAndTrimsBaseURL(t *testing.T) {
	if err := New("", "k", nil).GetJSON(context.Background(), "/x", nil); err == nil {
		t.Fatal("want error for empty base url")
	}
	if err := New("http://x", "", nil).GetJSON(context.Background(), "/x", nil); err == nil {
		t.Fatal("want error for empty api key")
	}
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	// trailing slash on base url must not double up with the leading slash on path
	var out map[string]any
	if err := New(srv.URL+"/", "k", nil).GetJSON(context.Background(), "/api/v1/x", &out); err != nil {
		t.Fatalf("GetJSON: %v", err)
	}
	if gotPath != "/api/v1/x" || strings.Contains(gotPath, "//") {
		t.Fatalf("bad joined path %q", gotPath)
	}
}
EOF
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd /opt/silo-plugin-sdk && /opt/deployarr/.local/go-sdk/go/bin/go test ./pkg/pluginsdk/httpclient/ -count=1`
Expected: FAIL — `undefined: New / StatusError`.

- [ ] **Step 3: Write `httpclient.go`**

```bash
cat > /opt/silo-plugin-sdk/pkg/pluginsdk/httpclient/httpclient.go <<'EOF'
// Package httpclient is the shared outbound JSON-over-HTTP client for Silo
// plugins that talk to a credentialed third-party API (Sonarr/Radarr, Seerr,
// …). It carries an X-Api-Key header, caps the response body, and surfaces one
// typed error (*StatusError) for any non-2xx. A fully-empty 2xx body is treated
// as success (zero-value dest); callers detect "created but no body returned"
// by inspecting the decoded value (e.g. id == 0). The client is stateless;
// every call carries its own base URL + key.
package httpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const maxResponseBody = 1 << 20 // 1 MiB

// Client talks to one API instance.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// StatusError is any non-2xx response. Message is the parsed {"message":...}
// field when present, else the trimmed raw body; Body is always the trimmed raw
// body. Pointer receiver so errors.As(err, &se) works.
type StatusError struct {
	StatusCode int
	Body       string
	Message    string
}

func (e *StatusError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = e.Body
	}
	if msg == "" {
		return fmt.Sprintf("httpclient: HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("httpclient: HTTP %d: %s", e.StatusCode, msg)
}

// New builds a client. A nil hc gets a default 30s-timeout client. baseURL is
// right-trimmed of "/"; apiKey is space-trimmed.
func New(baseURL, apiKey string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{
		baseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		apiKey:     strings.TrimSpace(apiKey),
		httpClient: hc,
	}
}

// GetJSON issues a GET and decodes into dest (dest may be nil).
func (c *Client) GetJSON(ctx context.Context, path string, dest any) error {
	return c.DoJSON(ctx, http.MethodGet, path, nil, dest)
}

// PostJSON issues a POST with a JSON body and decodes into dest.
func (c *Client) PostJSON(ctx context.Context, path string, body, dest any) error {
	return c.DoJSON(ctx, http.MethodPost, path, body, dest)
}

// DoJSON performs the request with the X-Api-Key header, mapping any non-2xx to
// *StatusError. A fully-empty 2xx body decodes to the zero value with nil error.
func (c *Client) DoJSON(ctx context.Context, method, path string, body, dest any) error {
	if c.baseURL == "" {
		return fmt.Errorf("httpclient: base url is required")
	}
	if c.apiKey == "" {
		return fmt.Errorf("httpclient: api key is required")
	}

	var reader io.Reader
	if body != nil {
		var buf bytes.Buffer
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return fmt.Errorf("httpclient: encode request: %w", err)
		}
		reader = &buf
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("httpclient: create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Api-Key", c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("httpclient: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
		trimmed := strings.TrimSpace(string(raw))
		return &StatusError{StatusCode: resp.StatusCode, Body: trimmed, Message: parseMessage(raw)}
	}
	if dest == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBody))
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBody)).Decode(dest); err != nil {
		if err == io.EOF {
			return nil // empty 2xx body: success, no content to decode
		}
		return fmt.Errorf("httpclient: decode response: %w", err)
	}
	return nil
}

// parseMessage extracts the "message" field from an error body, falling back to
// the trimmed raw body.
func parseMessage(raw []byte) string {
	var envelope struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &envelope) == nil && envelope.Message != "" {
		return envelope.Message
	}
	return strings.TrimSpace(string(raw))
}
EOF
```

Note: the decode check uses `err == io.EOF` (not `errors.Is`) deliberately — `json.Decoder.Decode` returns `io.EOF` directly on an empty stream, while a mid-JSON cut returns `io.ErrUnexpectedEOF` (NOT equal to `io.EOF`), so a truncated body correctly falls through to the decode-error branch.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd /opt/silo-plugin-sdk && /opt/deployarr/.local/go-sdk/go/bin/go test ./pkg/pluginsdk/httpclient/ -count=1`
Expected: PASS (all four tests).

- [ ] **Step 5: Commit**

```bash
cd /opt/silo-plugin-sdk
git add pkg/pluginsdk/httpclient/
git commit -m "feat(httpclient): shared outbound JSON client with X-Api-Key and StatusError"
```

---

## Task 2: `manifest.LoadWithChecksum`

**Files:**
- Create: `/opt/silo-plugin-sdk/pkg/pluginsdk/manifest/checksum.go`
- Test: `/opt/silo-plugin-sdk/pkg/pluginsdk/manifest/checksum_test.go`

- [ ] **Step 1: Write the failing test**

```bash
cat > /opt/silo-plugin-sdk/pkg/pluginsdk/manifest/checksum_test.go <<'EOF'
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
)

const testManifestJSON = `{
  "plugin_id": "silo.test.plugin",
  "version": "0.0.1",
  "silo_api_version": "v1",
  "capabilities": []
}`

func TestLoadWithChecksumOverridesVersionAndStampsChecksum(t *testing.T) {
	m, err := LoadWithChecksum([]byte(testManifestJSON), "9.9.9")
	if err != nil {
		t.Fatalf("LoadWithChecksum: %v", err)
	}
	if m.GetPluginId() != "silo.test.plugin" {
		t.Fatalf("plugin_id: got %q", m.GetPluginId())
	}
	if m.GetVersion() != "9.9.9" {
		t.Fatalf("version override: got %q want 9.9.9", m.GetVersion())
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatalf("read exe: %v", err)
	}
	sum := sha256.Sum256(data)
	if want := hex.EncodeToString(sum[:]); m.GetChecksum() != want {
		t.Fatalf("checksum: got %q want %q", m.GetChecksum(), want)
	}
}

func TestLoadWithChecksumEmptyVersionKeepsManifestVersion(t *testing.T) {
	m, err := LoadWithChecksum([]byte(testManifestJSON), "")
	if err != nil {
		t.Fatalf("LoadWithChecksum: %v", err)
	}
	if m.GetVersion() != "0.0.1" {
		t.Fatalf("version: got %q want 0.0.1", m.GetVersion())
	}
}
EOF
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd /opt/silo-plugin-sdk && /opt/deployarr/.local/go-sdk/go/bin/go test ./pkg/pluginsdk/manifest/ -run TestLoadWithChecksum -count=1`
Expected: FAIL — `undefined: LoadWithChecksum`. (If it instead fails because `Load`/`Validate` rejects the empty-`capabilities` test manifest, add the missing required field the error names to `testManifestJSON` and re-run — a manifest with `plugin_id`+`version`+`silo_api_version` and no capabilities is expected to validate.)

- [ ] **Step 3: Write `checksum.go`**

```bash
cat > /opt/silo-plugin-sdk/pkg/pluginsdk/manifest/checksum.go <<'EOF'
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

// LoadWithChecksum loads an embedded manifest, optionally overrides its version,
// and stamps Checksum with the hex sha256 of the running binary. It reads
// os.Executable() itself, so it cannot be precomputed at SDK build time. This is
// the canonical plugin manifest-bootstrap previously copied into each plugin's
// main.go.
func LoadWithChecksum(embedded []byte, version string) (*pluginv1.PluginManifest, error) {
	m, err := Load(embedded)
	if err != nil {
		return nil, fmt.Errorf("load embedded manifest: %w", err)
	}
	if version != "" {
		m.Version = version
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve executable path: %w", err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		return nil, fmt.Errorf("read executable %q: %w", exe, err)
	}
	sum := sha256.Sum256(data)
	m.Checksum = hex.EncodeToString(sum[:])
	return m, nil
}
EOF
```

(The import path for the generated proto is the same one `manifest.go` already uses — verify by grepping `pkg/pluginsdk/manifest/manifest.go` for `pluginproto`; if the alias/path differs, match it.)

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd /opt/silo-plugin-sdk && /opt/deployarr/.local/go-sdk/go/bin/go test ./pkg/pluginsdk/manifest/ -count=1`
Expected: PASS (the new checksum tests + the existing manifest tests).

- [ ] **Step 5: Commit**

```bash
cd /opt/silo-plugin-sdk
git add pkg/pluginsdk/manifest/checksum.go pkg/pluginsdk/manifest/checksum_test.go
git commit -m "feat(manifest): LoadWithChecksum (embed + self-checksum bootstrap)"
```

---

## Task 3: `runtime.ServeManifest`

**Files:**
- Create: `/opt/silo-plugin-sdk/pkg/pluginsdk/runtime/serve_manifest.go`
- Test: `/opt/silo-plugin-sdk/pkg/pluginsdk/runtime/serve_manifest_test.go`

- [ ] **Step 1: Write the failing test**

```bash
cat > /opt/silo-plugin-sdk/pkg/pluginsdk/runtime/serve_manifest_test.go <<'EOF'
package runtime

import (
	"context"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

// Compile-time: manifestRuntime satisfies the Runtime server contract.
var _ pluginv1.RuntimeServer = (*manifestRuntime)(nil)

func TestManifestRuntimeServesManifestAndConfigure(t *testing.T) {
	m := &pluginv1.PluginManifest{PluginId: "silo.test", Version: "1.0.0"}
	rt := &manifestRuntime{manifest: m}

	resp, err := rt.GetManifest(context.Background(), &pluginv1.GetManifestRequest{})
	if err != nil || resp.GetManifest().GetPluginId() != "silo.test" {
		t.Fatalf("GetManifest: resp=%v err=%v", resp, err)
	}
	if _, err := rt.Configure(context.Background(), &pluginv1.ConfigureRequest{}); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	bresp, err := rt.BindHostBroker(context.Background(), &pluginv1.BindHostBrokerRequest{BrokerId: 7})
	if err != nil || bresp == nil {
		t.Fatalf("BindHostBroker: resp=%v err=%v", bresp, err)
	}
}
EOF
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd /opt/silo-plugin-sdk && /opt/deployarr/.local/go-sdk/go/bin/go test ./pkg/pluginsdk/runtime/ -run TestManifestRuntime -count=1`
Expected: FAIL — `undefined: manifestRuntime`.

- [ ] **Step 3: Write `serve_manifest.go`**

```bash
cat > /opt/silo-plugin-sdk/pkg/pluginsdk/runtime/serve_manifest.go <<'EOF'
package runtime

import (
	"context"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/manifest"
)

// manifestRuntime is the default Runtime capability server installed by
// ServeManifest: it answers GetManifest with the embedded manifest, treats
// Configure as a no-op, and wires the host broker so runtime.Host() works.
//
// BindHostBroker is implemented inline (calling the same-package SetHostBrokerID)
// rather than by embedding runtimedefault.Server, which would create a
// runtime -> runtimedefault -> runtime import cycle.
type manifestRuntime struct {
	pluginv1.UnimplementedRuntimeServer
	manifest *pluginv1.PluginManifest
}

func (s *manifestRuntime) GetManifest(context.Context, *pluginv1.GetManifestRequest) (*pluginv1.GetManifestResponse, error) {
	return &pluginv1.GetManifestResponse{Manifest: s.manifest}, nil
}

func (s *manifestRuntime) Configure(context.Context, *pluginv1.ConfigureRequest) (*pluginv1.ConfigureResponse, error) {
	return &pluginv1.ConfigureResponse{}, nil
}

func (s *manifestRuntime) BindHostBroker(_ context.Context, req *pluginv1.BindHostBrokerRequest) (*pluginv1.BindHostBrokerResponse, error) {
	SetHostBrokerID(req.GetBrokerId())
	return &pluginv1.BindHostBrokerResponse{}, nil
}

// ServeManifest loads + checksums the embedded manifest, installs the default
// manifestRuntime as the Runtime server, and serves the given capability
// servers (the caller supplies only the non-Runtime servers). It never returns;
// a fatal manifest error panics, matching a misbuilt plugin's old main().
func ServeManifest(manifestBytes []byte, version string, servers CapabilityServers) {
	m, err := manifest.LoadWithChecksum(manifestBytes, version)
	if err != nil {
		panic(err)
	}
	servers.Runtime = &manifestRuntime{manifest: m}
	Serve(ServeConfig{Servers: servers})
}
EOF
```

(Verify the proto getter/setter names against the generated code: `BindHostBrokerRequest.GetBrokerId()` and `BindHostBrokerResponse{}` — grep `pkg/pluginproto/silo/plugin/v1/*.go` for `BindHostBroker` if the field is named differently. `SetHostBrokerID` is the existing exported func in `runtime.go`.)

- [ ] **Step 4: Run build + tests (this also proves there is no import cycle)**

Run: `cd /opt/silo-plugin-sdk && /opt/deployarr/.local/go-sdk/go/bin/go build ./... && /opt/deployarr/.local/go-sdk/go/bin/go test ./pkg/pluginsdk/runtime/ -count=1`
Expected: build succeeds (no `import cycle not allowed` — confirms `runtime → manifest` is acyclic), `TestManifestRuntimeServesManifestAndConfigure` PASSES. If a cycle IS reported, STOP and report it (the design assumed `manifest` does not import `runtime`).

- [ ] **Step 5: Whole-SDK gate + commit**

```bash
cd /opt/silo-plugin-sdk
/opt/deployarr/.local/go-sdk/go/bin/go build ./... && /opt/deployarr/.local/go-sdk/go/bin/go vet ./... && /opt/deployarr/.local/go-sdk/go/bin/go test ./... -count=1
git add pkg/pluginsdk/runtime/serve_manifest.go pkg/pluginsdk/runtime/serve_manifest_test.go
git commit -m "feat(runtime): ServeManifest bootstrap (manifest + checksum + broker wiring)"
```
Expected: whole SDK builds/vets/tests green.

---

## Task 4: Migrate `silo-plugin-requests-seerr`

seerr already matches the empty-body model, so this is delete-the-client + retarget. Default branch `master`.

**Files:** delete `internal/seerr/client.go` + `internal/seerr/client_test.go`; create `internal/seerr/errors.go`; rewrite `internal/seerr/api.go`, `internal/seerr/api_test.go`, `internal/router/server.go`, `main.go`.

- [ ] **Step 1: Delete the local HTTP client + its test, move ErrNotFound**

```bash
cd /opt/silo-plugin-requests-seerr
rm internal/seerr/client.go internal/seerr/client_test.go
cat > internal/seerr/errors.go <<'EOF'
package seerr

import "errors"

// ErrNotFound is returned by FindExistingRequest when no matching request is in
// the scanned page. It is a plugin-local sentinel (not an HTTP-layer error).
var ErrNotFound = errors.New("seerr: not found")
EOF
```

- [ ] **Step 2: Rewrite `api.go` as free functions over `*httpclient.Client`**

```bash
cat > internal/seerr/api.go <<'EOF'
package seerr

import (
	"context"
	"fmt"

	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/httpclient"
)

// CreateRequest submits a new media request. A 409 surfaces as a
// *httpclient.StatusError{StatusCode: 409}; an empty 2xx body yields a
// zero-value MediaRequest (ID 0) with nil error.
func CreateRequest(ctx context.Context, c *httpclient.Client, body CreateRequestBody) (*MediaRequest, error) {
	var out MediaRequest
	if err := c.PostJSON(ctx, "/api/v1/request", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetRequest fetches a single request by its Seerr id.
func GetRequest(ctx context.Context, c *httpclient.Client, id int) (*MediaRequest, error) {
	var out MediaRequest
	if err := c.GetJSON(ctx, fmt.Sprintf("/api/v1/request/%d", id), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// FindExistingRequest scans the most recent requests for (tmdbID, is4k). Used on
// the 409 / empty-body recovery path. Returns ErrNotFound if no match is in the
// scanned page.
func FindExistingRequest(ctx context.Context, c *httpclient.Client, tmdbID int, is4k bool) (*MediaRequest, error) {
	var page requestPage
	if err := c.GetJSON(ctx, "/api/v1/request?take=100&sort=added", &page); err != nil {
		return nil, err
	}
	for i := range page.Results {
		r := page.Results[i]
		if r.Media.TMDBID == tmdbID && r.Is4K == is4k {
			return &r, nil
		}
	}
	return nil, ErrNotFound
}

// Me validates the base URL + API key via the authenticated /auth/me.
func Me(ctx context.Context, c *httpclient.Client) error {
	return c.GetJSON(ctx, "/api/v1/auth/me", nil)
}
EOF
```

- [ ] **Step 3: Retarget `api_test.go` to the free functions + httpclient**

Read the current `internal/seerr/api_test.go`, then rewrite the calls: `New(srv.URL, "k", nil).CreateRequest(ctx, body)` → `CreateRequest(ctx, httpclient.New(srv.URL, "k", nil), body)`; same for `FindExistingRequest` (now `FindExistingRequest(ctx, httpclient.New(...), tmdbID, is4k)`); `MapStatus` tests are unchanged (it's a pure func in types.go). Add the import `"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/httpclient"`. Keep every assertion identical. (The `ErrNotFound` assertion still refers to `seerr.ErrNotFound`/bare `ErrNotFound` in-package — unchanged.)

- [ ] **Step 4: Rewrite `internal/router/server.go`'s client + error usage**

Read the current `internal/router/server.go`. Apply these precise changes (the surrounding logic — quality loop, recovery, status mapping — stays identical):
- Add import `"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/httpclient"`.
- Fulfill: `client := seerr.New(conn.BaseURL, conn.APIKey, nil)` → `client := httpclient.New(conn.BaseURL, conn.APIKey, nil)`.
- `fulfillOne`/`recoverExisting`: change the `client` parameter type from `*seerr.Client` to `*httpclient.Client`; `client.CreateRequest(ctx, body)` → `seerr.CreateRequest(ctx, client, body)`; `client.FindExistingRequest(ctx, tmdbID, is4k)` → `seerr.FindExistingRequest(ctx, client, tmdbID, is4k)`.
- The duplicate branch `case errors.Is(err, seerr.ErrDuplicate):` → branch on the status code:
  ```go
  var se *httpclient.StatusError
  // ... in the switch:
  case errors.As(err, &se) && se.StatusCode == http.StatusConflict:
  ```
  (declare `var se *httpclient.StatusError` before the `switch`; `net/http` is already imported.)
- CheckStatus: `byID := make(map[string]*seerr.Client, …)` → `map[string]*httpclient.Client`; `byID[conn.ID] = seerr.New(...)` → `httpclient.New(...)`; `client.GetRequest(ctx, id)` → `seerr.GetRequest(ctx, client, id)`; `var apiErr *seerr.APIError; errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound` → `var se *httpclient.StatusError; errors.As(err, &se) && se.StatusCode == http.StatusNotFound`.
- TestConnection: `seerr.New(conn.BaseURL, conn.APIKey, nil).Me(ctx)` → `seerr.Me(ctx, httpclient.New(conn.BaseURL, conn.APIKey, nil))`.

- [ ] **Step 5: Rewrite `main.go` to use ServeManifest**

```bash
cat > main.go <<'EOF'
// Command silo-plugin-requests-seerr implements the Silo request_router.v1
// capability backed by a Seerr (Overseerr/Jellyseerr) instance.
package main

import (
	_ "embed"

	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/runtime"

	"github.com/Silo-Server/silo-plugin-requests-seerr/internal/router"
)

var version string

//go:embed manifest.json
var manifestJSON []byte

func main() {
	runtime.ServeManifest(manifestJSON, version, runtime.CapabilityServers{
		RequestRouter: router.New(),
	})
}
EOF
```

- [ ] **Step 6: Build, vet, gofmt, test**

Run:
```bash
cd /opt/silo-plugin-requests-seerr && /opt/deployarr/.local/go-sdk/go/bin/go mod tidy && /opt/deployarr/.local/go-sdk/go/bin/go build ./... && /opt/deployarr/.local/go-sdk/go/bin/go vet ./... && $(/opt/deployarr/.local/go-sdk/go/bin/go env GOROOT)/bin/gofmt -l . && /opt/deployarr/.local/go-sdk/go/bin/go test ./... -count=1
```
Expected: build/vet clean; `gofmt -l .` empty (run `gofmt -w .` if not); all tests pass (router, seerr, main). Fix any remaining reference to the deleted `seerr.New`/`seerr.Client`/`seerr.APIError`/`seerr.ErrDuplicate` the compiler flags.

- [ ] **Step 7: Commit**

```bash
cd /opt/silo-plugin-requests-seerr
git add -A
git commit -m "refactor: adopt shared SDK httpclient + ServeManifest; drop local HTTP client"
```

---

## Task 5: Migrate `silo-plugin-requests-arr`

The behavior-change one: empty-body recovery moves from `IsEmptyOrTruncatedDecodeError` to an `id == 0` check. Default branch `master`.

**Files:** delete `internal/arr/client.go`; rewrite `internal/arr/resources.go`, `internal/arr/radarr.go`, `internal/arr/sonarr.go`, `main.go`; update `internal/arr/radarr_test.go` + `sonarr_test.go` if they assert the old empty-body path.

- [ ] **Step 1: Delete the local HTTP client**

```bash
cd /opt/silo-plugin-requests-arr
rm internal/arr/client.go
```

- [ ] **Step 2: Retarget every `*arr.Client` parameter + `New(...)` call to httpclient**

Read `internal/arr/resources.go`, `radarr.go`, `sonarr.go`. The arr-local `Client` type is gone, so:
- In **`resources.go`**: add import `"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/httpclient"`; change the three function signatures `ListRootFolders(ctx context.Context, client *Client)` / `ListQualityProfiles(... *Client)` / `ListTags(... *Client)` → `*httpclient.Client`. Their bodies (`client.GetJSON(...)`) are unchanged.
- In **`radarr.go`** and **`sonarr.go`**: add the `httpclient` import; replace every `New(integration.BaseURL, integration.APIKeyRef, c.httpClient)` with `httpclient.New(integration.BaseURL, integration.APIKeyRef, c.httpClient)`; change the helper signatures that take `client *Client` (`lookupMovie`/`findMovieByTMDBID`/`queueDetails` in radarr.go; `lookupSeries`/`findSeriesByTVDBID`/`queueDetails` in sonarr.go — whatever the actual names are) to `client *httpclient.Client`. (The `RadarrClient`/`SonarrClient` structs keep their own `httpClient *http.Client` field unchanged — that's the stdlib client passed into `httpclient.New`.)

- [ ] **Step 3: Change the empty-body recovery in `SubmitMovie` (radarr.go)**

Replace the POST-and-recover block. The OLD shape is:
```go
	if err := client.PostJSON(ctx, "/api/v3/movie", movie, &created); err != nil {
		if !IsEmptyOrTruncatedDecodeError(err) {
			return FulfillmentResult{}, err
		}
		if found, lookErr := c.findMovieByTMDBID(ctx, client, req.TMDBID); lookErr == nil && found.ID > 0 {
			return resultFromMovie(found), nil
		}
		return AcceptedWithoutResponse("radarr"), nil
	}
	return resultFromMovie(created), nil
```
The NEW shape (empty body now returns nil with a zero-value `created`, so a non-nil error is a real failure; an empty body is detected by `created.ID == 0`):
```go
	if err := client.PostJSON(ctx, "/api/v3/movie", movie, &created); err != nil {
		return FulfillmentResult{}, err
	}
	if created.ID == 0 {
		// POST accepted but Radarr returned an empty body. Recover the new
		// movie's Radarr ID by listing movies filtered by TMDB ID; without the
		// ID the reconcile loop cannot advance the request.
		if found, lookErr := c.findMovieByTMDBID(ctx, client, req.TMDBID); lookErr == nil && found.ID > 0 {
			return resultFromMovie(found), nil
		}
		return AcceptedWithoutResponse("radarr"), nil
	}
	return resultFromMovie(created), nil
```

- [ ] **Step 4: Change the empty-body recovery in `SubmitSeries` (sonarr.go)**

Same transformation. OLD:
```go
	if err := client.PostJSON(ctx, "/api/v3/series", series, &created); err != nil {
		if !IsEmptyOrTruncatedDecodeError(err) {
			return FulfillmentResult{}, err
		}
		if found, lookErr := c.findSeriesByTVDBID(ctx, client, *req.TVDBID); lookErr == nil && found.ID > 0 {
			return resultFromSeries(found), nil
		}
		return AcceptedWithoutResponse("sonarr"), nil
	}
	return resultFromSeries(created), nil
```
NEW:
```go
	if err := client.PostJSON(ctx, "/api/v3/series", series, &created); err != nil {
		return FulfillmentResult{}, err
	}
	if created.ID == 0 {
		// POST accepted but Sonarr returned an empty body. Recover the new
		// series' Sonarr ID by listing series filtered by TVDB ID; without the
		// ID the reconcile loop cannot advance the request.
		if found, lookErr := c.findSeriesByTVDBID(ctx, client, *req.TVDBID); lookErr == nil && found.ID > 0 {
			return resultFromSeries(found), nil
		}
		return AcceptedWithoutResponse("sonarr"), nil
	}
	return resultFromSeries(created), nil
```

- [ ] **Step 5: Rewrite `main.go` to use ServeManifest**

```bash
cat > main.go <<'EOF'
// Command silo-plugin-requests-arr implements the Silo request_router.v1
// capability for multi-instance Sonarr/Radarr.
package main

import (
	_ "embed"

	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/runtime"

	"github.com/Silo-Server/silo-plugin-requests-arr/internal/router"
)

var version string

//go:embed manifest.json
var manifestJSON []byte

func main() {
	runtime.ServeManifest(manifestJSON, version, runtime.CapabilityServers{
		RequestRouter: router.New(),
	})
}
EOF
```

- [ ] **Step 6: Update the submit tests if they assert the old empty-body path**

Read `internal/arr/radarr_test.go` + `sonarr_test.go`. If any test simulates an empty/truncated 201 to exercise the recovery (it would have relied on `IsEmptyOrTruncatedDecodeError`), update it so: a stub returning **201 with an empty body** + a lookup stub returning the record → asserts the recovered id (the `created.ID == 0` path). If a test specifically asserted *truncated*-body recovery, change it to assert the new behavior (a truncated 201 now returns an error from `SubmitMovie`/`SubmitSeries`). Tests that POST a normal non-empty body are unaffected. (If neither test touches the empty-body path, no test change is needed — note that.)

- [ ] **Step 7: Build, vet, gofmt, test**

Run:
```bash
cd /opt/silo-plugin-requests-arr && /opt/deployarr/.local/go-sdk/go/bin/go mod tidy && /opt/deployarr/.local/go-sdk/go/bin/go build ./... && /opt/deployarr/.local/go-sdk/go/bin/go vet ./... && $(/opt/deployarr/.local/go-sdk/go/bin/go env GOROOT)/bin/gofmt -l . && /opt/deployarr/.local/go-sdk/go/bin/go test ./... -count=1
```
Expected: build/vet clean; `gofmt -l .` empty; all tests pass. Fix any leftover reference to the deleted `arr.Client`/`New`/`HTTPError`/`DecodeError`/`IsEmptyOrTruncatedDecodeError` the compiler flags.

- [ ] **Step 8: Commit**

```bash
cd /opt/silo-plugin-requests-arr
git add -A
git commit -m "refactor: adopt shared SDK httpclient + ServeManifest; drop local HTTP client"
```

---

## Task 6: Final cross-repo verification

- [ ] **Step 1: All three repos build/vet/test green**

Run:
```bash
for r in /opt/silo-plugin-sdk /opt/silo-plugin-requests-seerr /opt/silo-plugin-requests-arr; do
  echo "=== $r ==="
  (cd "$r" && /opt/deployarr/.local/go-sdk/go/bin/go build ./... && /opt/deployarr/.local/go-sdk/go/bin/go vet ./... && $(/opt/deployarr/.local/go-sdk/go/bin/go env GOROOT)/bin/gofmt -l . && /opt/deployarr/.local/go-sdk/go/bin/go test ./... -count=1 2>&1 | tail -8)
done
```
Expected: each repo builds, vets, `gofmt -l .` prints nothing, all tests pass.

- [ ] **Step 2: Confirm the duplication is gone**

Run:
```bash
test ! -f /opt/silo-plugin-requests-arr/internal/arr/client.go && echo "arr client.go deleted"
test ! -f /opt/silo-plugin-requests-seerr/internal/seerr/client.go && echo "seerr client.go deleted"
grep -rl "os.Executable\|sha256" /opt/silo-plugin-requests-arr/main.go /opt/silo-plugin-requests-seerr/main.go 2>/dev/null && echo "BOOTSTRAP STILL DUPLICATED (unexpected)" || echo "main.go bootstrap removed from both plugins"
```
Expected: both `client.go` deleted; neither `main.go` still does the self-checksum (that lives in the SDK now).

- [ ] **Step 3: Confirm zero host changes + clean trees**

Run:
```bash
git -C /opt/silo status --porcelain && echo "(silo-server clean — zero host changes)"
for r in /opt/silo-plugin-sdk /opt/silo-plugin-requests-seerr /opt/silo-plugin-requests-arr; do git -C "$r" status --porcelain && echo "($r clean)"; done
```
Expected: `/opt/silo` clean (no host changes); all three working repos clean (everything committed).

---

## Notes for the implementer

- **Zero `/opt/silo` (host) changes.** This is SDK + the two plugin repos only. The host already consumes plugins over the wire; nothing in the host depends on a plugin's internal HTTP client or `main.go`.
- **The one deliberate behavior change** is arr's truncated-2xx body: it now returns an error from `SubmitMovie`/`SubmitSeries` instead of auto-recovering. Empty-body recovery (the common case) is preserved via the `created.ID == 0` check. Do not try to re-add truncated tolerance — it's intentionally dropped.
- **Method → free function (seerr):** seerr's `api.go` funcs stop being methods on a deleted `seerr.Client` and become free functions taking `*httpclient.Client`; call sites change from `client.CreateRequest(ctx, body)` to `seerr.CreateRequest(ctx, client, body)`.
- **arr keeps `RadarrClient`/`SonarrClient`** (they hold a stdlib `*http.Client` and build a per-call `httpclient.Client`); only the arr-local `*Client` *type* (the deleted HTTP wrapper) is replaced by `*httpclient.Client`.
- **Import-cycle guard:** Task 3's `go build ./...` is the canary — if `runtime` importing `manifest` reports a cycle, the design's assumption (manifest doesn't import runtime) is wrong; STOP and report rather than working around it.
- If `go mod tidy` needs to resolve the SDK's transitive deps in a plugin, they're already in the module cache (the plugins built before); the local `replace` keeps the SDK itself local. Report any specific module fetch failure rather than hand-editing go.sum.
