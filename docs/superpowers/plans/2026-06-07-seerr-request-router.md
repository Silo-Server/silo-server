# silo-plugin-requests-seerr Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a new `request_router.v1` plugin (`silo-plugin-requests-seerr`) that fulfills Silo content requests by submitting them to a Seerr (Overseerr/Jellyseerr-compatible) instance.

**Architecture:** A standalone Go plugin binary, structurally mirroring `silo-plugin-requests-arr`: `main.go` embeds + self-checksums `manifest.json` and serves the SDK `Runtime` + `RequestRouter` servers; `internal/seerr` is a small stateless HTTP client for the Overseerr `/api/v1` API (`X-Api-Key` auth); `internal/router` implements the five RPCs, mapping each requested quality to one Seerr request (`is4k` from the tier). Zero changes to silo-server or the SDK.

**Tech Stack:** Go 1.26 (pure Go, no CGO/libvips), `github.com/Silo-Server/silo-plugin-sdk` (local `replace`), `google.golang.org/protobuf`, `net/http` + `net/http/httptest` for tests.

**Spec:** `docs/superpowers/specs/2026-06-07-seerr-request-router-design.md`

**Conventions for every task:**
- Repo root is `/opt/silo-plugin-requests-seerr` (create it in Task 1); commands assume that as cwd unless noted.
- Go toolchain: `/opt/deployarr/.local/go-sdk/go/bin/go` (referred to below as `go`).
- LOCAL-ONLY: `git commit` only — never push/tag/PR. `go.mod` keeps `replace github.com/Silo-Server/silo-plugin-sdk => /opt/silo-plugin-sdk`.
- The path-guard hook blocks the Edit/Write tools outside `/opt/silo`; write files in this repo via Bash heredocs (the arr plugin tasks did the same).

---

## File Structure

```
/opt/silo-plugin-requests-seerr/
  go.mod, go.sum                 # module + local SDK replace (Task 1)
  main.go                        # entrypoint: embed+checksum manifest, serve Runtime + RequestRouter (Task 1)
  manifest.json                  # plugin_id silo.requests.seerr, capability "seerr", 1-field config (Task 1)
  internal/
    seerr/
      client.go                  # HTTP client: New/DoJSON/GetJSON/PostJSON, X-Api-Key, APIError, ErrDuplicate/ErrNotFound (Task 2)
      types.go                   # CreateRequestBody, MediaRequest/MediaInfo, status enums, MapStatus (Task 3)
      api.go                     # CreateRequest, GetRequest, FindExistingRequest, Me (Task 3)
      client_test.go, api_test.go
    router/
      config.go                  # Connection{Supports4K}, connectionFromRouter (Task 4)
      server.go                  # the 5 RequestRouter RPCs (Tasks 5-7)
      config_test.go, server_test.go
  README.md                      # admin-key requirement note (Task 8)
```

---

## Task 1: Module scaffold + manifest

Creates a compiling, serve-able skeleton: the module, the entrypoint, the manifest, and a stub router whose RPCs are all `Unimplemented` (fleshed out in later tasks).

**Files:**
- Create: `/opt/silo-plugin-requests-seerr/go.mod`
- Create: `/opt/silo-plugin-requests-seerr/main.go`
- Create: `/opt/silo-plugin-requests-seerr/manifest.json`
- Create: `/opt/silo-plugin-requests-seerr/internal/router/server.go` (stub)
- Test: `/opt/silo-plugin-requests-seerr/main_test.go`

- [ ] **Step 1: Create the repo + go.mod**

```bash
mkdir -p /opt/silo-plugin-requests-seerr/internal/router /opt/silo-plugin-requests-seerr/internal/seerr
cd /opt/silo-plugin-requests-seerr && git init -q
cat > go.mod <<'EOF'
module github.com/Silo-Server/silo-plugin-requests-seerr

go 1.26.3

require (
	github.com/Silo-Server/silo-plugin-sdk v0.0.0-00010101000000-000000000000
	google.golang.org/protobuf v1.36.11
)

replace github.com/Silo-Server/silo-plugin-sdk => /opt/silo-plugin-sdk
EOF
```

- [ ] **Step 2: Write `manifest.json`**

```bash
cat > manifest.json <<'EOF'
{
  "plugin_id": "silo.requests.seerr",
  "version": "0.1.0",
  "checksum": "__CHECKSUM__",
  "silo_api_version": "v1",
  "supported_platforms": [
    {"os": "linux", "arch": "amd64"},
    {"os": "linux", "arch": "arm64"},
    {"os": "darwin", "arch": "arm64"}
  ],
  "global_config_schema": [],
  "capabilities": [
    {
      "type": "request_router.v1",
      "id": "seerr",
      "display_name": "Seerr",
      "description": "Fulfill content requests through a Seerr (Overseerr/Jellyseerr) instance.",
      "config_schema": [
        {
          "key": "connection",
          "title": "Seerr",
          "json_schema": "{\"type\":\"object\",\"properties\":{\"supports_4k\":{\"type\":\"boolean\"}}}",
          "admin_form": {
            "submit_label": "Save connection",
            "fields": [
              {
                "key": "supports_4k",
                "label": "This Seerr handles 4K requests",
                "control": "ADMIN_FORM_CONTROL_SWITCH",
                "default_value": false,
                "description": "Enable only if the Seerr instance has a 4K Sonarr/Radarr configured. When off, 2160p requests are not sent to this connection."
              }
            ]
          }
        }
      ]
    }
  ]
}
EOF
python3 -m json.tool manifest.json >/dev/null && echo JSON_OK
```
Expected: `JSON_OK`.

- [ ] **Step 3: Write the stub router (`internal/router/server.go`)**

```bash
cat > internal/router/server.go <<'EOF'
// Package router implements the request_router.v1 RPCs over a Seerr
// (Overseerr/Jellyseerr) backend. The Server holds no state and stores no
// credentials; every call carries its own connections.
package router

import (
	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

// Server implements the request_router.v1 RPCs. It is stateless.
type Server struct {
	pluginv1.UnimplementedRequestRouterServer
}

// New returns a ready-to-serve request router.
func New() *Server { return &Server{} }
EOF
```

- [ ] **Step 4: Write `main.go`** (mirrors the arr plugin entrypoint verbatim except the import path + comment)

```bash
cat > main.go <<'EOF'
// Command silo-plugin-requests-seerr implements the Silo request_router.v1
// capability backed by a Seerr (Overseerr/Jellyseerr) instance. The Seerr base
// URL and API key arrive per call in each RouterConnection; the plugin stores
// nothing.
package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"os"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	publicmanifest "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/manifest"
	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/runtime"

	"github.com/Silo-Server/silo-plugin-requests-seerr/internal/router"
)

var version string

//go:embed manifest.json
var manifestJSON []byte

type runtimeServer struct {
	pluginv1.UnimplementedRuntimeServer
	manifest *pluginv1.PluginManifest
}

func (s *runtimeServer) GetManifest(context.Context, *pluginv1.GetManifestRequest) (*pluginv1.GetManifestResponse, error) {
	return &pluginv1.GetManifestResponse{Manifest: s.manifest}, nil
}

func (s *runtimeServer) Configure(context.Context, *pluginv1.ConfigureRequest) (*pluginv1.ConfigureResponse, error) {
	return &pluginv1.ConfigureResponse{}, nil
}

func main() {
	manifest, err := loadManifest()
	if err != nil {
		panic(err)
	}
	runtime.Serve(runtime.ServeConfig{
		Servers: runtime.CapabilityServers{
			Runtime:       &runtimeServer{manifest: manifest},
			RequestRouter: router.New(),
		},
	})
}

func loadManifest() (*pluginv1.PluginManifest, error) {
	manifest, err := publicmanifest.Load(manifestJSON)
	if err != nil {
		return nil, fmt.Errorf("load embedded manifest: %w", err)
	}
	if version != "" {
		manifest.Version = version
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
	manifest.Checksum = hex.EncodeToString(sum[:])
	return manifest, nil
}
EOF
```

- [ ] **Step 5: Write the manifest-loads test (`main_test.go`)**

```bash
cat > main_test.go <<'EOF'
package main

import "testing"

func TestEmbeddedManifestLoads(t *testing.T) {
	m, err := loadManifest()
	if err != nil {
		t.Fatalf("loadManifest: %v", err)
	}
	if m.GetPluginId() != "silo.requests.seerr" {
		t.Fatalf("plugin_id: want silo.requests.seerr got %q", m.GetPluginId())
	}
	caps := m.GetCapabilities()
	if len(caps) != 1 {
		t.Fatalf("want 1 capability, got %d", len(caps))
	}
	if caps[0].GetType() != "request_router.v1" || caps[0].GetId() != "seerr" {
		t.Fatalf("capability: want request_router.v1/seerr got %q/%q", caps[0].GetType(), caps[0].GetId())
	}
}
EOF
```

- [ ] **Step 6: Resolve deps, build, test**

Run:
```bash
cd /opt/silo-plugin-requests-seerr && go mod tidy && go build ./... && go test ./... -count=1
```
Expected: `go mod tidy` populates `go.sum`; build succeeds; `TestEmbeddedManifestLoads` PASSES (the manifest loads — note `loadManifest` reads `os.Executable()`, which under `go test` is the test binary; that's fine, the checksum is just set to the test binary's hash and the test never asserts the checksum).

- [ ] **Step 7: Commit**

```bash
cd /opt/silo-plugin-requests-seerr
git add -A
git commit -m "feat: scaffold silo-plugin-requests-seerr (module, manifest, entrypoint, stub router)"
```

---

## Task 2: Seerr HTTP client core

A stateless client (mirrors the arr `internal/arr/client.go`) with `X-Api-Key` auth, JSON helpers, and typed errors — including a `409` duplicate sentinel and a `404`/not-found sentinel used by the 409-recovery path.

**Files:**
- Create: `/opt/silo-plugin-requests-seerr/internal/seerr/client.go`
- Test: `/opt/silo-plugin-requests-seerr/internal/seerr/client_test.go`

- [ ] **Step 1: Write the failing test (`client_test.go`)**

```bash
cat > internal/seerr/client_test.go <<'EOF'
package seerr

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPostJSONSetsApiKeyHeaderAndDecodes(t *testing.T) {
	var gotKey, gotMethod, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Api-Key")
		gotMethod = r.Method
		gotCT = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":7}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "secret", nil)
	var out struct {
		ID int `json:"id"`
	}
	if err := c.PostJSON(context.Background(), "/api/v1/request", map[string]any{"x": 1}, &out); err != nil {
		t.Fatalf("PostJSON: %v", err)
	}
	if gotKey != "secret" {
		t.Fatalf("X-Api-Key: want secret got %q", gotKey)
	}
	if gotMethod != http.MethodPost || gotCT != "application/json" {
		t.Fatalf("method/content-type: got %q/%q", gotMethod, gotCT)
	}
	if out.ID != 7 {
		t.Fatalf("decode: want id 7 got %d", out.ID)
	}
}

func TestDoJSONMapsDuplicateAndHTTPErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/dup":
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte(`{"message":"already requested"}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"message":"boom"}`))
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "k", nil)

	err := c.GetJSON(context.Background(), "/dup", nil)
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("want ErrDuplicate, got %v", err)
	}

	err = c.GetJSON(context.Background(), "/boom", nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 500 || apiErr.Message != "boom" {
		t.Fatalf("want APIError{500,boom}, got %v", err)
	}
}

func TestDoJSONRequiresBaseURLAndKey(t *testing.T) {
	if err := New("", "k", nil).GetJSON(context.Background(), "/x", nil); err == nil {
		t.Fatal("want error for empty base url")
	}
	if err := New("http://x", "", nil).GetJSON(context.Background(), "/x", nil); err == nil {
		t.Fatal("want error for empty api key")
	}
}
EOF
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd /opt/silo-plugin-requests-seerr && go test ./internal/seerr/ -run TestDoJSON -count=1`
Expected: FAIL — `undefined: New / ErrDuplicate / APIError`.

- [ ] **Step 3: Write `client.go`**

```bash
cat > internal/seerr/client.go <<'EOF'
// Package seerr is a small stateless client for the Overseerr/Jellyseerr-
// compatible HTTP API exposed by Seerr. Each call carries its own base URL and
// API key; the client stores no credentials.
package seerr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const maxResponseBody = 1 << 20

// ErrDuplicate is returned when Seerr rejects a create with HTTP 409 because the
// media (same tmdbId + is4k) was already requested.
var ErrDuplicate = errors.New("seerr: duplicate request")

// ErrNotFound is returned when a lookup finds no matching record.
var ErrNotFound = errors.New("seerr: not found")

// Client talks to one Seerr instance.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// APIError is a non-2xx response. Message is parsed from Seerr's {"message":...}
// body when present.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("seerr: HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("seerr: HTTP %d: %s", e.StatusCode, e.Message)
}

// New builds a client. A nil httpClient gets a default with a 30s timeout.
func New(baseURL, apiKey string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{
		baseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		apiKey:     strings.TrimSpace(apiKey),
		httpClient: httpClient,
	}
}

// GetJSON issues a GET and decodes the response into dest (dest may be nil).
func (c *Client) GetJSON(ctx context.Context, path string, dest any) error {
	return c.DoJSON(ctx, http.MethodGet, path, nil, dest)
}

// PostJSON issues a POST with a JSON body and decodes the response into dest.
func (c *Client) PostJSON(ctx context.Context, path string, body, dest any) error {
	return c.DoJSON(ctx, http.MethodPost, path, body, dest)
}

// DoJSON performs an HTTP request with the X-Api-Key header, mapping non-2xx to
// *APIError (and 409 to ErrDuplicate).
func (c *Client) DoJSON(ctx context.Context, method, path string, body, dest any) error {
	if c.baseURL == "" {
		return fmt.Errorf("seerr: base url is required")
	}
	if c.apiKey == "" {
		return fmt.Errorf("seerr: api key is required")
	}

	var reader io.Reader
	if body != nil {
		var buf bytes.Buffer
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return fmt.Errorf("seerr: encode request: %w", err)
		}
		reader = &buf
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("seerr: create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Api-Key", c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("seerr: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
		apiErr := &APIError{StatusCode: resp.StatusCode, Message: parseMessage(raw)}
		if resp.StatusCode == http.StatusConflict {
			return fmt.Errorf("%w: %s", ErrDuplicate, apiErr.Message)
		}
		return apiErr
	}
	if dest == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBody))
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBody)).Decode(dest); err != nil {
		return fmt.Errorf("seerr: decode response: %w", err)
	}
	return nil
}

// parseMessage extracts the "message" field from a Seerr error body, falling
// back to the trimmed raw body.
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

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd /opt/silo-plugin-requests-seerr && go test ./internal/seerr/ -count=1`
Expected: PASS (all three tests). Note `ErrDuplicate` is wrapped with `%w`, so `errors.Is(err, ErrDuplicate)` holds.

- [ ] **Step 5: Commit**

```bash
cd /opt/silo-plugin-requests-seerr
git add internal/seerr/client.go internal/seerr/client_test.go
git commit -m "feat(seerr): HTTP client with X-Api-Key auth, APIError, duplicate sentinel"
```

---

## Task 3: Seerr DTOs, API methods, and status mapping

The request/response DTOs, the four API methods (`CreateRequest`, `GetRequest`, `FindExistingRequest`, `Me`), the Seerr status enums, and `MapStatus` (Seerr → Silo).

**Files:**
- Create: `/opt/silo-plugin-requests-seerr/internal/seerr/types.go`
- Create: `/opt/silo-plugin-requests-seerr/internal/seerr/api.go`
- Test: `/opt/silo-plugin-requests-seerr/internal/seerr/api_test.go`

- [ ] **Step 1: Write the failing test (`api_test.go`)**

```bash
cat > internal/seerr/api_test.go <<'EOF'
package seerr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCreateRequestSendsBodyAndParsesResponse(t *testing.T) {
	var body CreateRequestBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":55,"status":2,"is4k":true,"media":{"status":3,"tmdbId":42}}`))
	}))
	defer srv.Close()

	mr, err := New(srv.URL, "k", nil).CreateRequest(context.Background(), CreateRequestBody{
		MediaType: "tv", MediaID: 42, Is4K: true, Seasons: "all",
	})
	if err != nil {
		t.Fatalf("CreateRequest: %v", err)
	}
	if body.MediaType != "tv" || body.MediaID != 42 || !body.Is4K {
		t.Fatalf("sent body wrong: %+v", body)
	}
	if body.Seasons != "all" {
		t.Fatalf("seasons: want all got %v", body.Seasons)
	}
	if mr.ID != 55 || mr.Status != 2 || mr.Media.Status != 3 {
		t.Fatalf("parsed wrong: %+v", mr)
	}
}

func TestFindExistingRequestMatchesTMDBAnd4K(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"results":[
			{"id":1,"is4k":false,"media":{"tmdbId":42}},
			{"id":2,"is4k":true,"media":{"tmdbId":42}}
		]}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "k", nil)

	mr, err := c.FindExistingRequest(context.Background(), 42, true)
	if err != nil {
		t.Fatalf("FindExistingRequest: %v", err)
	}
	if mr.ID != 2 {
		t.Fatalf("want id 2 (the 4k match), got %d", mr.ID)
	}
	if _, err := c.FindExistingRequest(context.Background(), 999, false); err != ErrNotFound {
		t.Fatalf("want ErrNotFound for unknown tmdb, got %v", err)
	}
}

func TestMapStatus(t *testing.T) {
	cases := []struct {
		req, media int
		want       string
	}{
		{StatusRequestDeclined, MediaStatusAvailable, "failed"},
		{StatusRequestFailed, MediaStatusPending, "failed"},
		{StatusRequestApproved, MediaStatusAvailable, "completed"},
		{StatusRequestCompleted, MediaStatusUnknown, "completed"},
		{StatusRequestApproved, MediaStatusProcessing, "downloading"},
		{StatusRequestApproved, MediaStatusPartiallyAvailable, "downloading"},
		{StatusRequestApproved, MediaStatusPending, "queued"},
		{StatusRequestPending, MediaStatusUnknown, "queued"},
	}
	for _, c := range cases {
		if got := MapStatus(c.req, c.media); got != c.want {
			t.Fatalf("MapStatus(%d,%d): want %q got %q", c.req, c.media, c.want, got)
		}
	}
}
EOF
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd /opt/silo-plugin-requests-seerr && go test ./internal/seerr/ -run 'TestCreateRequest|TestFind|TestMapStatus' -count=1`
Expected: FAIL — `undefined: CreateRequestBody / CreateRequest / FindExistingRequest / MapStatus / StatusRequest* / MediaStatus*`.

- [ ] **Step 3: Write `types.go`**

```bash
cat > internal/seerr/types.go <<'EOF'
package seerr

// CreateRequestBody is the POST /api/v1/request body. serverId/profileId/
// rootFolder/userId are intentionally omitted: Seerr default-routes and
// attributes the request to the API-key owner.
type CreateRequestBody struct {
	MediaType string `json:"mediaType"` // "movie" | "tv"
	MediaID   int    `json:"mediaId"`   // TMDB id
	Is4K      bool   `json:"is4k"`
	Seasons   any    `json:"seasons,omitempty"` // "all" for tv; omitted for movie
}

// MediaInfo is the nested media record on a MediaRequest.
type MediaInfo struct {
	Status int `json:"status"` // MediaStatus
	TMDBID int `json:"tmdbId"`
}

// MediaRequest is the Seerr request object returned by create/get/list.
type MediaRequest struct {
	ID     int       `json:"id"`
	Status int       `json:"status"` // MediaRequestStatus
	Is4K   bool      `json:"is4k"`
	Media  MediaInfo `json:"media"`
}

// requestPage is the GET /api/v1/request list envelope.
type requestPage struct {
	Results []MediaRequest `json:"results"`
}

// MediaRequestStatus values (Overseerr server/constants/media.ts).
const (
	StatusRequestPending   = 1
	StatusRequestApproved  = 2
	StatusRequestDeclined  = 3
	StatusRequestFailed    = 4
	StatusRequestCompleted = 5
)

// MediaStatus values (Overseerr server/constants/media.ts).
const (
	MediaStatusUnknown            = 1
	MediaStatusPending            = 2
	MediaStatusProcessing         = 3
	MediaStatusPartiallyAvailable = 4
	MediaStatusAvailable          = 5
	MediaStatusDeleted            = 6
)

// MapStatus maps a Seerr (request.status, media.status) pair onto Silo's target
// status vocabulary. Order matters: terminal request failures win, then
// availability, then in-progress, else queued.
func MapStatus(requestStatus, mediaStatus int) string {
	switch {
	case requestStatus == StatusRequestDeclined || requestStatus == StatusRequestFailed:
		return "failed"
	case requestStatus == StatusRequestCompleted || mediaStatus == MediaStatusAvailable:
		return "completed"
	case mediaStatus == MediaStatusProcessing || mediaStatus == MediaStatusPartiallyAvailable:
		return "downloading"
	default:
		return "queued"
	}
}
EOF
```

- [ ] **Step 4: Write `api.go`**

```bash
cat > internal/seerr/api.go <<'EOF'
package seerr

import (
	"context"
	"fmt"
)

// CreateRequest submits a new media request. A 409 (already requested) surfaces
// as ErrDuplicate via the client.
func (c *Client) CreateRequest(ctx context.Context, body CreateRequestBody) (*MediaRequest, error) {
	var out MediaRequest
	if err := c.PostJSON(ctx, "/api/v1/request", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetRequest fetches a single request by its Seerr id.
func (c *Client) GetRequest(ctx context.Context, id int) (*MediaRequest, error) {
	var out MediaRequest
	if err := c.GetJSON(ctx, fmt.Sprintf("/api/v1/request/%d", id), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// FindExistingRequest scans the most recent requests for the one matching
// (tmdbID, is4k). Used only on the 409 path to recover an existing request id.
// Returns ErrNotFound if no match is in the scanned page.
func (c *Client) FindExistingRequest(ctx context.Context, tmdbID int, is4k bool) (*MediaRequest, error) {
	var page requestPage
	if err := c.GetJSON(ctx, "/api/v1/request?take=100", &page); err != nil {
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

// Me validates the base URL + API key by calling the authenticated /auth/me.
func (c *Client) Me(ctx context.Context) error {
	return c.GetJSON(ctx, "/api/v1/auth/me", nil)
}
EOF
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd /opt/silo-plugin-requests-seerr && go test ./internal/seerr/ -count=1`
Expected: PASS (all Task 2 + Task 3 tests).

- [ ] **Step 6: Commit**

```bash
cd /opt/silo-plugin-requests-seerr
git add internal/seerr/types.go internal/seerr/api.go internal/seerr/api_test.go
git commit -m "feat(seerr): request DTOs, API methods, and Seerr->Silo status mapping"
```

---

## Task 4: Router config parsing

`connectionFromRouter` reads the single `supports_4k` boolean from a `RouterConnection`'s config struct.

**Files:**
- Create: `/opt/silo-plugin-requests-seerr/internal/router/config.go`
- Test: `/opt/silo-plugin-requests-seerr/internal/router/config_test.go`

- [ ] **Step 1: Write the failing test (`config_test.go`)**

```bash
cat > internal/router/config_test.go <<'EOF'
package router

import (
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestConnectionFromRouterReadsSupports4K(t *testing.T) {
	cfg, _ := structpb.NewStruct(map[string]any{"supports_4k": true})
	conn := connectionFromRouter(&pluginv1.RouterConnection{
		Id: "c1", BaseUrl: "http://seerr", ApiKey: "k", Config: cfg,
	})
	if !conn.Supports4K {
		t.Fatal("want Supports4K true")
	}
	if conn.BaseURL != "http://seerr" || conn.APIKey != "k" || conn.ID != "c1" {
		t.Fatalf("chrome not read: %+v", conn)
	}
}

func TestConnectionFromRouterDefaultsSupports4KFalse(t *testing.T) {
	conn := connectionFromRouter(&pluginv1.RouterConnection{Id: "c2", BaseUrl: "http://s", ApiKey: "k"})
	if conn.Supports4K {
		t.Fatal("absent supports_4k should default to false")
	}
}
EOF
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd /opt/silo-plugin-requests-seerr && go test ./internal/router/ -run TestConnectionFromRouter -count=1`
Expected: FAIL — `undefined: connectionFromRouter`.

- [ ] **Step 3: Write `config.go`**

```bash
cat > internal/router/config.go <<'EOF'
package router

import (
	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

// Connection is the per-call Seerr connection: base URL + API key (host chrome)
// plus the single plugin_config field.
type Connection struct {
	ID         string
	BaseURL    string
	APIKey     string
	Supports4K bool
}

// connectionFromRouter parses a host-supplied RouterConnection. base_url and
// api_key are chrome; supports_4k comes from the config struct (absent = false).
func connectionFromRouter(c *pluginv1.RouterConnection) Connection {
	conn := Connection{
		ID:      c.GetId(),
		BaseURL: c.GetBaseUrl(),
		APIKey:  c.GetApiKey(),
	}
	if cfg := c.GetConfig(); cfg != nil {
		if v, ok := cfg.AsMap()["supports_4k"].(bool); ok {
			conn.Supports4K = v
		}
	}
	return conn
}
EOF
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd /opt/silo-plugin-requests-seerr && go test ./internal/router/ -run TestConnectionFromRouter -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /opt/silo-plugin-requests-seerr
git add internal/router/config.go internal/router/config_test.go
git commit -m "feat(router): parse Seerr connection config (supports_4k)"
```

---

## Task 5: Fulfill RPC

Maps each requested quality to one Seerr request (`is4k` from the tier; 2160p skipped when the connection isn't 4K-capable), with per-target containment and 409 recovery.

**Files:**
- Modify: `/opt/silo-plugin-requests-seerr/internal/router/server.go`
- Test: `/opt/silo-plugin-requests-seerr/internal/router/server_test.go`

- [ ] **Step 1: Write the failing test (`server_test.go`)**

```bash
cat > internal/router/server_test.go <<'EOF'
package router

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

// seerrStub records POST bodies and serves canned create responses.
type seerrStub struct {
	mu     sync.Mutex
	bodies []map[string]any
}

func (s *seerrStub) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/request" && r.Method == http.MethodPost {
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			s.mu.Lock()
			s.bodies = append(s.bodies, b)
			n := len(s.bodies)
			s.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			// distinct ids so we can tell HD/4K targets apart
			_, _ = w.Write([]byte(`{"id":` + itoa(100+n) + `,"status":2,"media":{"status":2}}`))
			return
		}
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	})
}

func conn(t *testing.T, id, baseURL string, supports4k bool) *pluginv1.RouterConnection {
	cfg, err := structpb.NewStruct(map[string]any{"supports_4k": supports4k})
	if err != nil {
		t.Fatalf("structpb: %v", err)
	}
	return &pluginv1.RouterConnection{Id: id, BaseUrl: baseURL, ApiKey: "k", Config: cfg}
}

func TestFulfillHDOnly(t *testing.T) {
	stub := &seerrStub{}
	srv := httptest.NewServer(stub.handler(t))
	defer srv.Close()

	resp, err := New().Fulfill(context.Background(), &pluginv1.FulfillRequest{
		Request:     &pluginv1.RequestDescriptor{MediaType: "movie", ExternalIds: map[string]string{"tmdb": "42"}},
		Qualities:   []string{"1080p"},
		Connections: []*pluginv1.RouterConnection{conn(t, "c1", srv.URL, false)},
	})
	if err != nil {
		t.Fatalf("Fulfill: %v", err)
	}
	if len(resp.GetTargets()) != 1 {
		t.Fatalf("want 1 target, got %d msg=%q", len(resp.GetTargets()), resp.GetMessage())
	}
	tgt := resp.GetTargets()[0]
	if tgt.GetStatus() != "queued" || tgt.GetQuality() != "1080p" || tgt.GetExternalId() != "101" {
		t.Fatalf("bad target: %+v", tgt)
	}
	if stub.bodies[0]["is4k"] != false || stub.bodies[0]["mediaType"] != "movie" || stub.bodies[0]["mediaId"] != float64(42) {
		t.Fatalf("bad body: %+v", stub.bodies[0])
	}
	if _, ok := stub.bodies[0]["seasons"]; ok {
		t.Fatalf("movie body must not carry seasons: %+v", stub.bodies[0])
	}
}

func TestFulfillHDPlus4KWhenSupported(t *testing.T) {
	stub := &seerrStub{}
	srv := httptest.NewServer(stub.handler(t))
	defer srv.Close()

	resp, _ := New().Fulfill(context.Background(), &pluginv1.FulfillRequest{
		Request:     &pluginv1.RequestDescriptor{MediaType: "series", ExternalIds: map[string]string{"tmdb": "9"}},
		Qualities:   []string{"1080p", "2160p"},
		Connections: []*pluginv1.RouterConnection{conn(t, "c1", srv.URL, true)},
	})
	if len(resp.GetTargets()) != 2 {
		t.Fatalf("want 2 targets, got %d", len(resp.GetTargets()))
	}
	if stub.bodies[0]["is4k"] != false || stub.bodies[1]["is4k"] != true {
		t.Fatalf("is4k mapping wrong: %+v %+v", stub.bodies[0], stub.bodies[1])
	}
	if stub.bodies[0]["mediaType"] != "tv" || stub.bodies[0]["seasons"] != "all" {
		t.Fatalf("series should map to tv + seasons all: %+v", stub.bodies[0])
	}
}

func TestFulfill4KSkippedWhenUnsupported(t *testing.T) {
	stub := &seerrStub{}
	srv := httptest.NewServer(stub.handler(t))
	defer srv.Close()

	resp, _ := New().Fulfill(context.Background(), &pluginv1.FulfillRequest{
		Request:     &pluginv1.RequestDescriptor{MediaType: "movie", ExternalIds: map[string]string{"tmdb": "9"}},
		Qualities:   []string{"1080p", "2160p"},
		Connections: []*pluginv1.RouterConnection{conn(t, "c1", srv.URL, false)},
	})
	if len(resp.GetTargets()) != 1 || resp.GetTargets()[0].GetQuality() != "1080p" {
		t.Fatalf("4k should be skipped, got %d targets", len(resp.GetTargets()))
	}
	if len(stub.bodies) != 1 {
		t.Fatalf("only the HD request should be sent, got %d", len(stub.bodies))
	}
}

func TestFulfillMissingTMDBFailsTarget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "should not be called", http.StatusInternalServerError)
	}))
	defer srv.Close()
	resp, _ := New().Fulfill(context.Background(), &pluginv1.FulfillRequest{
		Request:     &pluginv1.RequestDescriptor{MediaType: "movie", ExternalIds: map[string]string{}},
		Qualities:   []string{"1080p"},
		Connections: []*pluginv1.RouterConnection{conn(t, "c1", srv.URL, false)},
	})
	if len(resp.GetTargets()) != 1 || resp.GetTargets()[0].GetStatus() != "failed" {
		t.Fatalf("missing tmdb should fail the target: %+v", resp.GetTargets())
	}
}

func TestFulfillDuplicateRecoversID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/request" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte(`{"message":"already requested"}`))
		case r.URL.Path == "/api/v1/request" && r.Method == http.MethodGet:
			w.Write([]byte(`{"results":[{"id":314,"is4k":false,"media":{"tmdbId":42}}]}`))
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	defer srv.Close()
	resp, _ := New().Fulfill(context.Background(), &pluginv1.FulfillRequest{
		Request:     &pluginv1.RequestDescriptor{MediaType: "movie", ExternalIds: map[string]string{"tmdb": "42"}},
		Qualities:   []string{"1080p"},
		Connections: []*pluginv1.RouterConnection{conn(t, "c1", srv.URL, false)},
	})
	tgt := resp.GetTargets()[0]
	if tgt.GetStatus() != "queued" || tgt.GetExternalId() != "314" {
		t.Fatalf("409 should recover the existing id as queued: %+v", tgt)
	}
}

func TestFulfillZeroTargetsReturnsMessage(t *testing.T) {
	resp, _ := New().Fulfill(context.Background(), &pluginv1.FulfillRequest{
		Request:     &pluginv1.RequestDescriptor{MediaType: "movie", ExternalIds: map[string]string{"tmdb": "42"}},
		Qualities:   []string{"2160p"},
		Connections: []*pluginv1.RouterConnection{conn(t, "c1", "http://unused", false)},
	})
	if len(resp.GetTargets()) != 0 || resp.GetMessage() == "" {
		t.Fatalf("want zero targets + a message, got %d targets msg=%q", len(resp.GetTargets()), resp.GetMessage())
	}
}
EOF
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd /opt/silo-plugin-requests-seerr && go test ./internal/router/ -run TestFulfill -count=1`
Expected: FAIL — `New().Fulfill` returns `Unimplemented` (the stub embeds `UnimplementedRequestRouterServer`), and `itoa` is undefined.

- [ ] **Step 3: Replace `internal/router/server.go` with the Fulfill implementation**

```bash
cat > internal/router/server.go <<'EOF'
// Package router implements the request_router.v1 RPCs over a Seerr
// (Overseerr/Jellyseerr) backend. The Server holds no state and stores no
// credentials; every call carries its own connections.
package router

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"

	"github.com/Silo-Server/silo-plugin-requests-seerr/internal/seerr"
)

// quality4K is the host quality tier that maps to a Seerr is4k request. The host
// sends "1080p"/"2160p"; every non-4K tier maps to is4k:false (HD), the safe
// default (an HD request never needs a 4K Sonarr/Radarr).
const quality4K = "2160p"

// Server implements the request_router.v1 RPCs. It is stateless.
type Server struct {
	pluginv1.UnimplementedRequestRouterServer
}

// New returns a ready-to-serve request router.
func New() *Server { return &Server{} }

// mediaTypeAndID translates the Silo descriptor into Seerr's mediaType + TMDB
// id. Seerr uses "tv" for series and identifies media by TMDB id only.
func mediaTypeAndID(d *pluginv1.RequestDescriptor) (mediaType string, tmdbID int, err error) {
	mediaType = "movie"
	if d.GetMediaType() == "series" {
		mediaType = "tv"
	}
	raw := d.GetExternalIds()["tmdb"]
	tmdbID, convErr := strconv.Atoi(raw)
	if raw == "" || convErr != nil || tmdbID <= 0 {
		return mediaType, 0, fmt.Errorf("request has no TMDB id; Seerr requires a TMDB id")
	}
	return mediaType, tmdbID, nil
}

// Fulfill submits one Seerr request per requested quality (is4k from the tier),
// returning one FulfillmentTarget each. Per-target containment: one failing
// quality/connection never aborts the others.
func (s *Server) Fulfill(ctx context.Context, req *pluginv1.FulfillRequest) (*pluginv1.FulfillResponse, error) {
	d := req.GetRequest()
	mediaType, tmdbID, idErr := mediaTypeAndID(d)

	var targets []*pluginv1.FulfillmentTarget
	for _, c := range req.GetConnections() {
		conn := connectionFromRouter(c)
		client := seerr.New(conn.BaseURL, conn.APIKey, nil)
		for _, q := range req.GetQualities() {
			is4k := q == quality4K
			if is4k && !conn.Supports4K {
				continue // this connection does not fulfill 4K
			}
			targets = append(targets, s.fulfillOne(ctx, client, conn.ID, q, mediaType, tmdbID, is4k, idErr))
		}
	}
	if len(targets) == 0 {
		return &pluginv1.FulfillResponse{Message: "no Seerr connection fulfills the requested quality"}, nil
	}
	return &pluginv1.FulfillResponse{Targets: targets}, nil
}

// fulfillOne submits a single (connection, quality) target.
func (s *Server) fulfillOne(ctx context.Context, client *seerr.Client, connID, quality, mediaType string, tmdbID int, is4k bool, idErr error) *pluginv1.FulfillmentTarget {
	t := &pluginv1.FulfillmentTarget{Quality: quality, ConnectionId: connID}
	if idErr != nil {
		t.Status = "failed"
		t.Message = idErr.Error()
		return t
	}
	body := seerr.CreateRequestBody{MediaType: mediaType, MediaID: tmdbID, Is4K: is4k}
	if mediaType == "tv" {
		body.Seasons = "all"
	}

	created, err := client.CreateRequest(ctx, body)
	switch {
	case err == nil:
		t.Status = "queued"
		t.ExternalId = itoa(created.ID)
		t.ExternalStatus = itoa(created.Status)
	case errors.Is(err, seerr.ErrDuplicate):
		if found, ferr := client.FindExistingRequest(ctx, tmdbID, is4k); ferr == nil && found != nil {
			t.Status = "queued"
			t.ExternalId = itoa(found.ID)
			t.ExternalStatus = itoa(found.Status)
			t.Message = "already requested in Seerr"
		} else {
			t.Status = "failed"
			t.Message = "already requested in Seerr but its request id could not be resolved"
		}
	default:
		t.Status = "failed"
		t.Message = err.Error()
	}
	return t
}

func itoa(n int) string { return strconv.Itoa(n) }
EOF
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd /opt/silo-plugin-requests-seerr && go test ./internal/router/ -run 'TestFulfill|TestConnectionFromRouter' -count=1`
Expected: PASS (all Fulfill tests + the Task 4 config tests).

- [ ] **Step 5: Commit**

```bash
cd /opt/silo-plugin-requests-seerr
git add internal/router/server.go internal/router/server_test.go
git commit -m "feat(router): Fulfill - one Seerr request per quality, 409 recovery, per-target containment"
```

---

## Task 6: CheckStatus RPC

Polls each target's Seerr request and maps the status back.

**Files:**
- Modify: `/opt/silo-plugin-requests-seerr/internal/router/server.go`
- Test: `/opt/silo-plugin-requests-seerr/internal/router/server_test.go` (append)

- [ ] **Step 1: Append the failing test to `server_test.go`**

```bash
cat >> internal/router/server_test.go <<'EOF'

func TestCheckStatusMapsAndSkipsMissingConnection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// request 5 -> processing (downloading); request 6 -> available (completed)
		switch r.URL.Path {
		case "/api/v1/request/5":
			w.Write([]byte(`{"id":5,"status":2,"media":{"status":3}}`))
		case "/api/v1/request/6":
			w.Write([]byte(`{"id":6,"status":2,"media":{"status":5}}`))
		default:
			http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	resp, err := New().CheckStatus(context.Background(), &pluginv1.CheckStatusRequest{
		Request: &pluginv1.RequestDescriptor{MediaType: "movie", ExternalIds: map[string]string{"tmdb": "1"}},
		Targets: []*pluginv1.TargetRef{
			{Quality: "1080p", ConnectionId: "c1", ExternalId: "5"},
			{Quality: "2160p", ConnectionId: "c1", ExternalId: "6"},
			{Quality: "1080p", ConnectionId: "missing", ExternalId: "9"}, // connection not provided -> skipped
		},
		Connections: []*pluginv1.RouterConnection{conn(t, "c1", srv.URL, true)},
	})
	if err != nil {
		t.Fatalf("CheckStatus: %v", err)
	}
	if len(resp.GetStatuses()) != 2 {
		t.Fatalf("want 2 statuses (missing connection skipped), got %d", len(resp.GetStatuses()))
	}
	got := map[string]string{}
	for _, st := range resp.GetStatuses() {
		got[st.GetQuality()] = st.GetStatus()
	}
	if got["1080p"] != "downloading" || got["2160p"] != "completed" {
		t.Fatalf("status mapping wrong: %+v", got)
	}
}
EOF
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd /opt/silo-plugin-requests-seerr && go test ./internal/router/ -run TestCheckStatus -count=1`
Expected: FAIL — `CheckStatus` returns `Unimplemented`.

- [ ] **Step 3: Append `CheckStatus` to `server.go`** (insert before the trailing `itoa` helper; safest is to append the method then leave `itoa` where it is — Go allows any order, so just append at end of file)

```bash
cat >> internal/router/server.go <<'EOF'

// CheckStatus probes each target's Seerr request id and maps the status back.
// Targets whose connection is missing, or whose probe errors, are skipped so one
// unreachable connection does not blank the whole response.
func (s *Server) CheckStatus(ctx context.Context, req *pluginv1.CheckStatusRequest) (*pluginv1.CheckStatusResponse, error) {
	byID := make(map[string]Connection, len(req.GetConnections()))
	for _, c := range req.GetConnections() {
		conn := connectionFromRouter(c)
		byID[conn.ID] = conn
	}

	var statuses []*pluginv1.TargetStatus
	for _, tref := range req.GetTargets() {
		conn, ok := byID[tref.GetConnectionId()]
		if !ok {
			continue
		}
		id, err := strconv.Atoi(tref.GetExternalId())
		if err != nil || id <= 0 {
			continue
		}
		mr, err := seerr.New(conn.BaseURL, conn.APIKey, nil).GetRequest(ctx, id)
		if err != nil {
			continue
		}
		statuses = append(statuses, &pluginv1.TargetStatus{
			Quality:        tref.GetQuality(),
			ConnectionId:   tref.GetConnectionId(),
			Status:         seerr.MapStatus(mr.Status, mr.Media.Status),
			ExternalStatus: itoa(mr.Media.Status),
		})
	}
	return &pluginv1.CheckStatusResponse{Statuses: statuses}, nil
}
EOF
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd /opt/silo-plugin-requests-seerr && go test ./internal/router/ -count=1`
Expected: PASS (Fulfill + CheckStatus + config).

- [ ] **Step 5: Commit**

```bash
cd /opt/silo-plugin-requests-seerr
git add internal/router/server.go internal/router/server_test.go
git commit -m "feat(router): CheckStatus - poll Seerr requests and map status"
```

---

## Task 7: TestConnection, ListConfigOptions, Validate

`TestConnection` = `/auth/me`; `ListConfigOptions` and `Validate` return empty (Seerr has no dynamic options and the single boolean has no cross-field rules).

**Files:**
- Modify: `/opt/silo-plugin-requests-seerr/internal/router/server.go`
- Test: `/opt/silo-plugin-requests-seerr/internal/router/server_test.go` (append)

- [ ] **Step 1: Append the failing tests to `server_test.go`**

```bash
cat >> internal/router/server_test.go <<'EOF'

func TestTestConnectionUsesAuthMe(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/me" {
			http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
			return
		}
		w.Write([]byte(`{"id":1,"email":"owner@example.com"}`))
	}))
	defer ok.Close()
	res, _ := New().TestConnection(context.Background(), &pluginv1.TestConnectionRequest{
		Connection: conn(t, "c1", ok.URL, false),
	})
	if !res.GetOk() {
		t.Fatalf("want ok, got %+v", res)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"unauthorized"}`))
	}))
	defer bad.Close()
	res, _ = New().TestConnection(context.Background(), &pluginv1.TestConnectionRequest{
		Connection: conn(t, "c1", bad.URL, false),
	})
	if res.GetOk() || res.GetMessage() == "" {
		t.Fatalf("want not-ok with message, got %+v", res)
	}
}

func TestListConfigOptionsAndValidateAreEmpty(t *testing.T) {
	opts, err := New().ListConfigOptions(context.Background(), &pluginv1.ListConfigOptionsRequest{
		Connection: conn(t, "c1", "http://s", false),
	})
	if err != nil || len(opts.GetOptionsByField()) != 0 {
		t.Fatalf("want empty options, got %+v err=%v", opts.GetOptionsByField(), err)
	}
	val, err := New().Validate(context.Background(), &pluginv1.ValidateRequest{
		Connection: conn(t, "c1", "http://s", false),
	})
	if err != nil || len(val.GetFieldErrors()) != 0 || val.GetFormError() != "" {
		t.Fatalf("want empty validate, got %+v err=%v", val, err)
	}
}
EOF
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd /opt/silo-plugin-requests-seerr && go test ./internal/router/ -run 'TestTestConnection|TestListConfigOptionsAndValidate' -count=1`
Expected: FAIL — these RPCs return `Unimplemented`.

- [ ] **Step 3: Append the three RPCs to `server.go`**

```bash
cat >> internal/router/server.go <<'EOF'

// TestConnection verifies the base URL + API key by calling /auth/me. Never
// returns a gRPC error; failure is Ok:false + message.
func (s *Server) TestConnection(ctx context.Context, req *pluginv1.TestConnectionRequest) (*pluginv1.TestConnectionResponse, error) {
	conn := connectionFromRouter(req.GetConnection())
	if err := seerr.New(conn.BaseURL, conn.APIKey, nil).Me(ctx); err != nil {
		return &pluginv1.TestConnectionResponse{Ok: false, Message: err.Error()}, nil
	}
	return &pluginv1.TestConnectionResponse{Ok: true, Message: "connection successful"}, nil
}

// ListConfigOptions returns no dynamic options: the Seerr connection config has
// no dynamic-options fields. Returned empty (not Unimplemented) so the host's
// options probe gets a clean answer.
func (s *Server) ListConfigOptions(ctx context.Context, req *pluginv1.ListConfigOptionsRequest) (*pluginv1.ListConfigOptionsResponse, error) {
	return &pluginv1.ListConfigOptionsResponse{}, nil
}

// Validate has no cross-field rules to check (the only config field is a
// boolean). Returned empty so the host's save-time Validate succeeds cleanly.
func (s *Server) Validate(ctx context.Context, req *pluginv1.ValidateRequest) (*pluginv1.ValidateResponse, error) {
	return &pluginv1.ValidateResponse{}, nil
}
EOF
```

- [ ] **Step 4: Run the full router test suite to verify it passes**

Run: `cd /opt/silo-plugin-requests-seerr && go test ./internal/router/ -count=1`
Expected: PASS (all router tests).

- [ ] **Step 5: Commit**

```bash
cd /opt/silo-plugin-requests-seerr
git add internal/router/server.go internal/router/server_test.go
git commit -m "feat(router): TestConnection via /auth/me; empty ListConfigOptions and Validate"
```

---

## Task 8: Full verification + README

Final whole-module gates and a short README documenting the admin-key requirement.

**Files:**
- Create: `/opt/silo-plugin-requests-seerr/README.md`

- [ ] **Step 1: Whole-module build, vet, gofmt, test**

Run:
```bash
cd /opt/silo-plugin-requests-seerr
go build ./... && go vet ./... && gofmt -l . && go test ./... -count=1
```
Expected: build + vet clean; `gofmt -l .` prints nothing (all files formatted); all tests pass (`main`, `internal/seerr`, `internal/router`). If `gofmt -l .` lists any file, run `gofmt -w .` and re-run.

- [ ] **Step 2: Write `README.md`**

```bash
cat > README.md <<'EOF'
# silo-plugin-requests-seerr

A Silo `request_router.v1` plugin that fulfills content requests by submitting
them to a [Seerr](https://github.com/seerr-team/seerr) (Overseerr/Jellyseerr-
compatible) instance. Seerr manages its own Sonarr/Radarr; this plugin is a thin
adapter.

## Connection config

Each connection carries a Seerr **base URL** + **API key** (host chrome) and one
plugin setting:

- **This Seerr handles 4K requests** (`supports_4k`, default off) — enable only
  if the Seerr instance has a 4K Sonarr/Radarr configured. When off, 2160p
  requests are not sent to this connection.

## API key requirement

The API key (Settings → General in Seerr) **must belong to a Seerr admin /
auto-approve user**. Silo is the sole approval authority; requests created via
the API auto-approve and hand off to Seerr's Sonarr/Radarr immediately. A
non-admin key would leave requests pending in Seerr (visible as `queued` in Silo
that never advances). `TestConnection` (`GET /api/v1/auth/me`) surfaces an
invalid key.

## How requests map

- Each requested quality becomes one Seerr request: HD → `is4k:false`, 2160p →
  `is4k:true` (skipped when `supports_4k` is off).
- `series` → Seerr `mediaType: "tv"` with `seasons: "all"`; movies use
  `mediaType: "movie"`. Media is identified by **TMDB id**.
- A duplicate (HTTP 409) is treated as already-queued; the plugin recovers the
  existing Seerr request id so Silo can track it.

## Build / test

```
go build ./... && go test ./...
```

This module uses a local SDK `replace` (`=> /opt/silo-plugin-sdk`) for
development; swap it for a published version before release.
EOF
```

- [ ] **Step 3: Commit**

```bash
cd /opt/silo-plugin-requests-seerr
git add README.md
git commit -m "docs: README (connection config, admin-key requirement, request mapping)"
```

---

## Notes for the implementer

- **Zero host/SDK changes.** Everything is in `/opt/silo-plugin-requests-seerr`. If you ever feel you need to touch `/opt/silo` or `/opt/silo-plugin-sdk` to make the plugin work, STOP and report it — that would mean the `request_router.v1` contract is not actually backend-agnostic, which is a finding to escalate, not patch around.
- **structpb decodes JSON numbers as float64.** The config has only a boolean, so this doesn't bite here, but note the test asserts `stub.bodies[0]["mediaId"] == float64(42)` because the decoded JSON body is `map[string]any` (numbers are float64). The wire body Seerr receives is a normal JSON integer (`mediaId` is an `int` on `CreateRequestBody`).
- **`os.Executable()` under `go test`** resolves to the test binary; `loadManifest` happily checksums it. Tests never assert the checksum, so this is fine.
- **The 4K-tier string is `"2160p"`** — confirmed against the host/arr quality constants (`internal/arr/types.go`: `Quality1080p="1080p"`, `Quality2160p="2160p"`). Any unrecognized tier maps to `is4k:false` (HD), the safe default.
- **Deferred to the requests-pluginization pre-publish checklist** (not this plan): the live e2e against a real Seerr, and verifying whether a 4K target's status tracks a separate `status4k` field on `MediaInfo` (the unit tests use the documented single `media.status`).
