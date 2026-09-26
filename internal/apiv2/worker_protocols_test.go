package apiv2

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodeconfig"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/proxy"
	"github.com/Silo-Server/silo-server/internal/routeinventory"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
	"github.com/Silo-Server/silo-server/internal/transcodeproxy"
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
	generated, err := GenerateOpenAPI()
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
		request.Header.Set("Authorization", "Bearer "+cfg.Auth.JWTSecret)
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

func TestWorkerControlsRetainProtocolAndBearerGate(t *testing.T) {
	watcher := nodeconfig.NewWatcher(nil, nil, nil, nodeconfig.BootstrapOverrides{})
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "synthetic-worker-secret"
	cfg.Playback.TranscodeDir = t.TempDir()
	watcher.SetConfigForTest(cfg)
	handlers := map[string]http.Handler{
		"proxy":          proxy.NewServer(watcher, nil).Handler(),
		"transcode_node": transcodenode.NewServer(watcher, nil).Handler(),
	}
	count := 0
	for _, op := range describeWorkerProtocols().Operations {
		if op.Method != http.MethodPost || !strings.HasPrefix(op.Path, "/admin/") {
			continue
		}
		count++
		if op.RetrySafety != "non_retryable" || op.Description == "" {
			t.Fatalf("command lacks uncertainty semantics: %+v", op)
		}
		if op.Responses["401"].Content["text/plain"] == nil {
			t.Fatal("worker failure must remain plain text", op.Path)
		}
		request := httptest.NewRequest(op.Method, op.Path, nil)
		response := httptest.NewRecorder()
		handlers[op.Listener].ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized || response.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
			t.Fatalf("%s %s bearer refusal: %d %s", op.Listener, op.Path, response.Code, response.Body)
		}
		if op.Path == "/admin/reprobe-capabilities" {
			if op.Responses["409"] == nil || op.Responses["503"] == nil || op.Responses["200"].Content["application/json"] == nil {
				t.Fatal("incomplete reprobe protocol", op.Listener)
			}
		} else if op.Responses["204"] == nil || len(op.Responses["204"].Content) != 0 || op.Responses["500"] == nil {
			t.Fatal("reload must preserve empty success and possible failure", op.Listener, op.Path)
		}
	}
	if count != 6 {
		t.Fatalf("got %d worker controls, want 6", count)
	}
}

func TestWorkerTranscodeControlsDescribeUnconfiguredRefusal(t *testing.T) {
	watcher := nodeconfig.NewWatcher(nil, nil, nil, nodeconfig.BootstrapOverrides{})
	handler := transcodenode.NewServer(watcher, nil).Handler()
	for _, op := range describeWorkerProtocols().Operations {
		if op.Listener != "transcode_node" || op.Method != http.MethodPost || !strings.HasPrefix(op.Path, "/admin/") {
			continue
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(op.Method, op.Path, nil))
		if response.Code != http.StatusServiceUnavailable || response.Header().Get("Content-Type") != "text/plain; charset=utf-8" || op.Responses["503"].Content["text/plain"] == nil {
			t.Fatalf("unconfigured %s: %d %s", op.Path, response.Code, response.Body)
		}
	}
}

func TestWorkerChapterExtractionProtocol(t *testing.T) {
	registry := describeWorkerProtocols()
	var index int
	for i, op := range registry.Operations {
		if op.Path == "/chapter-thumbnails/extract" {
			index = i
		}
	}
	op := registry.Operations[index]
	if op.Path != "/chapter-thumbnails/extract" || op.RetrySafety != "non_retryable" || op.Responses["200"].Content["image/jpeg"] == nil {
		t.Fatal("missing retained JPEG extraction protocol")
	}
	generated, err := GenerateOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(generated, &document); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{op.Path, Prefix + op.Path} {
		if _, exists := document["paths"].(map[string]any)[path]; exists {
			t.Fatal("worker extraction advertised as native", path)
		}
	}
	compiler := jsonschema.NewCompiler()
	const url = "https://schema.example.invalid/chapter-worker.json"
	if err := compiler.AddResource(url, document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile(url + op.RequestBody.Content["application/json"].Schema.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(map[string]any{"input_path": "/synthetic/frame.mkv", "future_option": true}); err != nil {
		t.Fatal("omitted options/unknown key must remain decodable", err)
	}
	watcher := nodeconfig.NewWatcher(nil, nil, nil, nodeconfig.BootstrapOverrides{})
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "synthetic-worker-secret"
	cfg.Playback.TranscodeDir = t.TempDir()
	watcher.SetConfigForTest(cfg)
	handler := transcodenode.NewServer(watcher, nil).Handler()
	for _, tc := range []struct {
		body   string
		auth   bool
		status int
		media  string
	}{
		{"{", false, 401, "text/plain"},
		{"{", true, 400, "application/json"},
		{"{}", true, 400, "application/json"},
		{`{"input_path":"/synthetic/frame.mkv"}`, true, 503, "text/plain"},
	} {
		request := httptest.NewRequest(op.Method, op.Path, strings.NewReader(tc.body))
		if tc.auth {
			request.Header.Set("Authorization", "Bearer "+cfg.Auth.JWTSecret)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != tc.status || !strings.HasPrefix(response.Header().Get("Content-Type"), tc.media) {
			t.Fatalf("got %d %s", response.Code, response.Body)
		}
		content := op.Responses[fmt.Sprint(tc.status)].Content[tc.media]
		if content == nil {
			t.Fatal("actual failure media omitted", tc)
		}
		if tc.media == "application/json" {
			failure, err := compiler.Compile(url + content.Schema.Ref)
			if err != nil {
				t.Fatal(err)
			}
			var body any
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if err := failure.Validate(body); err != nil {
				t.Fatal(err)
			}
		}
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
		req.Header.Set("Authorization", "Bearer "+cfg.Auth.JWTSecret)
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

func TestWorkerFontBundleUsesOwningWireSchema(t *testing.T) {
	generated, err := GenerateOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(generated, &document); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	const url = "https://schema.example.invalid/proxy-fonts.json"
	if err := compiler.AddResource(url, document); err != nil {
		t.Fatal(err)
	}
	found := false
	for index, op := range describeWorkerProtocols().Operations {
		if op.Path != "/stream/subtitles/{token}/{track}/fonts" {
			continue
		}
		found = true
		schema, err := compiler.Compile(fmt.Sprintf("%s#/x-silo-worker-protocols/operations/%d/responses/200/content/application~1json/schema", url, index))
		if err != nil {
			t.Fatal(err)
		}
		for _, fonts := range [][]playback.SubtitleFontAttachment{nil, {{Name: "synthetic.ttf", Data: []byte{1, 2}}}} {
			data, err := json.Marshal(playback.EncodeSubtitleFontBundle(fonts))
			if err != nil {
				t.Fatal(err)
			}
			var body any
			if err := json.Unmarshal(data, &body); err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(body); err != nil {
				t.Fatal(string(data), err)
			}
			if fonts != nil && !strings.Contains(string(data), `"data":"AQI="`) {
				t.Fatal("owning base64 wire changed", string(data))
			}
		}
	}
	if !found {
		t.Fatal("missing retained fonts")
	}
}

func TestWorkerSegmentAcknowledgementRefusals(t *testing.T) {
	watcher := nodeconfig.NewWatcher(nil, nil, nil, nodeconfig.BootstrapOverrides{})
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "synthetic-worker-secret"
	cfg.Playback.TranscodeDir = t.TempDir()
	watcher.SetConfigForTest(cfg)
	handler := transcodenode.NewServer(watcher, nil).Handler()
	op := transcodenode.ProtocolSegmentAcknowledgement()
	for _, tc := range []struct {
		name, generation string
		auth             bool
		status           int
	}{
		{"seg_00001.ts", "generation", false, 401},
		{"invalid", "generation", true, 400},
		{"seg_00001.ts", "", true, 400},
		{"seg_00001.ts", "generation", true, 404},
	} {
		req := httptest.NewRequest(http.MethodPost, "/transcode/missing/segment/"+tc.name+"/downloaded", nil)
		if tc.auth {
			req.Header.Set("Authorization", "Bearer "+cfg.Auth.JWTSecret)
		}
		req.Header.Set(transcodeproxy.GenerationHeader, tc.generation)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != tc.status {
			t.Fatal(tc, response.Code, response.Body)
		}
		if op.Responses[strconv.Itoa(tc.status)].Content["text/plain"] == nil {
			t.Fatal("failure omitted", tc.status)
		}
	}
	if op.RetrySafety != "natural_idempotent" || op.Responses["204"] == nil || len(op.Responses["204"].Content) != 0 {
		t.Fatal("acknowledgement semantics changed")
	}
}
