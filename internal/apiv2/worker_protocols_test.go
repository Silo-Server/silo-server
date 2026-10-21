package apiv2

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/nodeconfig"
	"github.com/Silo-Server/silo-server/internal/proxy"
	"github.com/Silo-Server/silo-server/internal/routeinventory"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestWorkerRegistryMatchesOwningInventory(t *testing.T) {
	registry := describeWorkerProtocols()
	inventory, err := routeinventory.LoadArtifact(".")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, read := range registry.Operations {
		key := read.Listener + " " + read.Method + " " + read.Path
		if seen[key] {
			t.Fatal("duplicate worker operation", key)
		}
		seen[key] = true
		matches := 0
		for _, route := range inventory.Routes {
			if route.Listener == read.Listener && route.Method == read.Method && route.Path == read.Path {
				matches++
				if route.Handler != read.Handler || route.AuthClass != read.AuthClass {
					t.Fatalf("%s handler/auth drift: %+v", key, read)
				}
			}
		}
		if matches != 1 {
			t.Fatalf("%s: %d matching routes", key, matches)
		}
	}
	// This is a partial registry; it does not assert that all worker routes
	// have completed migration. Keep both listener variants explicitly covered.
	for _, listener := range []string{"proxy", "transcode_node"} {
		for _, path := range []string{"/status", "/hw-capabilities"} {
			if !seen[listener+" GET "+path] {
				t.Fatal("missing retained read", listener, path)
			}
		}
	}
}

func TestWorkerStatusSchemasMatchRealListeners(t *testing.T) {
	generated, err := generatedOpenAPIBytes()
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(generated, &document); err != nil {
		t.Fatal(err)
	}
	paths := document["paths"].(map[string]any)
	for _, path := range []string{"/status", "/hw-capabilities", Prefix + "/status", Prefix + "/hw-capabilities"} {
		if _, exists := paths[path]; exists {
			t.Fatal("worker read advertised as native API route", path)
		}
	}
	compiler := jsonschema.NewCompiler()
	const url = "https://schema.example.invalid/worker-contract.json"
	if err := compiler.AddResource(url, document); err != nil {
		t.Fatal(err)
	}
	for _, operation := range describeWorkerProtocols().Operations {
		if operation.Responses["200"] == nil || operation.Responses["200"].Content["application/json"] == nil {
			continue
		}
		ref := operation.Responses["200"].Content["application/json"].Schema.Ref
		if _, err := compiler.Compile(url + ref); err != nil {
			t.Fatal(operation.Listener, operation.Path, err)
		}
	}
	watcher := nodeconfig.NewWatcher(nil, nil, nil, nodeconfig.BootstrapOverrides{})
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "synthetic-worker-secret"
	cfg.Playback.TranscodeDir = t.TempDir()
	watcher.SetConfigForTest(cfg)
	handlers := map[string]http.Handler{
		"proxy":          proxy.NewServer(watcher, nil).Handler(),
		"transcode_node": transcodenode.NewServer(watcher, nil).Handler(),
	}
	bodies := map[string]any{}
	schemas := map[string]*jsonschema.Schema{}
	for _, read := range describeWorkerProtocols().Operations {
		if read.Path != "/status" {
			continue
		}
		schema, err := compiler.Compile(url + read.Responses["200"].Content["application/json"].Schema.Ref)
		if err != nil {
			t.Fatal(err)
		}
		schemas[read.Listener] = schema
		request := httptest.NewRequest(http.MethodGet, read.Path, nil)
		nodeauthtest.Sign(t, request, cfg.Auth.JWTSecret)
		response := httptest.NewRecorder()
		handlers[read.Listener].ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("%s: %d %s", read.Listener, response.Code, response.Body)
		}
		var body any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(body); err != nil {
			t.Fatal(read.Listener, err)
		}
		bodies[read.Listener] = body
		denied := httptest.NewRecorder()
		handlers[read.Listener].ServeHTTP(denied, httptest.NewRequest(http.MethodGet, read.Path, nil))
		if denied.Code != http.StatusUnauthorized || denied.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
			t.Fatalf("%s worker bearer failure changed: %d %s", read.Listener, denied.Code, denied.Body)
		}
	}
	if schemas["proxy"].Validate(bodies["transcode_node"]) == nil || schemas["transcode_node"].Validate(bodies["proxy"]) == nil {
		t.Fatal("listener-specific status schemas were conflated")
	}
}

func TestWorkerArtifactConditionalProtocol(t *testing.T) {
	watcher := nodeconfig.NewWatcher(nil, nil, nil, nodeconfig.BootstrapOverrides{})
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "synthetic-worker-secret"
	cfg.Playback.TranscodeDir = t.TempDir()
	cfg.Download.ArtifactDir = t.TempDir()
	watcher.SetConfigForTest(cfg)
	if err := os.WriteFile(filepath.Join(cfg.Download.ArtifactDir, "fixture.mp4"), []byte("0123456789"), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(transcodenode.NewServer(watcher, nil).Handler())
	defer server.Close()
	registry := describeWorkerProtocols()
	for _, tc := range []struct {
		method, header, value string
		status                int
	}{
		{"GET", "Range", "bytes=1-2", 206},
		{"GET", "Range", "bytes=0-1,8-9", 206},
		{"GET", "Range", "bytes=99-100", 416},
		{"GET", "If-None-Match", `"fixture-10"`, 304},
		{"GET", "If-Match", `"different"`, 412},
		{"HEAD", "", "", 200},
		{"DELETE", "", "", 204},
		{"DELETE", "", "", 204},
		{"GET", "", "", 404},
	} {
		req, err := http.NewRequestWithContext(t.Context(), tc.method, server.URL+"/downloads/artifacts/fixture", nil)
		if err != nil {
			t.Fatal(err)
		}
		nodeauthtest.Sign(t, req, cfg.Auth.JWTSecret)
		if tc.header != "" {
			req.Header.Set(tc.header, tc.value)
		}
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		if err != nil || closeErr != nil {
			t.Fatal(err, closeErr)
		}
		if response.StatusCode != tc.status {
			t.Fatalf("%+v got %d %s", tc, response.StatusCode, body)
		}
		if tc.method == "HEAD" && len(body) != 0 {
			t.Fatal("HEAD emitted bytes")
		}
		found := false
		for _, op := range registry.Operations {
			if op.Path != "/downloads/artifacts/{artifact_id}" || op.Method != tc.method {
				continue
			}
			declared := op.Responses[strconv.Itoa(tc.status)]
			if declared == nil {
				t.Fatal("undeclared artifact status", tc)
			}
			if len(body) > 0 {
				media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
				if err != nil || declared.Content[media] == nil {
					t.Fatal("undeclared media", media, tc)
				}
			}
			found = true
		}
		if !found {
			t.Fatal("missing artifact operation", tc.method)
		}
	}
}
