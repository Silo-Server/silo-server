package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodeconfig"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
	"github.com/Silo-Server/silo-server/internal/workerprotocol"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type prepareProtocolPathAuthority struct{}

func (prepareProtocolPathAuthority) Allowed(context.Context, string) (bool, error) { return true, nil }

func TestWorkerDownloadPreparationProtocol(t *testing.T) {
	document, err := GenerateOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(document, &raw); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	const schemaURL = "https://schema.example.invalid/worker-prepare.json"
	if err := compiler.AddResource(schemaURL, raw); err != nil {
		t.Fatal(err)
	}
	registry := describeWorkerProtocols()
	index := slices.IndexFunc(registry.Operations, func(op workerprotocol.Operation) bool {
		return op.Listener == "transcode_node" && op.Path == "/downloads/prepare"
	})
	if index < 0 {
		t.Fatal("missing preparation description")
	}
	op := registry.Operations[index]
	if op.Path != "/downloads/prepare" || op.RetrySafety != "non_retryable" {
		t.Fatal(op)
	}
	requestSchema, err := compiler.Compile(schemaURL + op.RequestBody.Content["application/json"].Schema.Ref)
	if err != nil {
		t.Fatal(err)
	}
	// Existing decoder permits omitted codec/index fields and ignores unknown keys.
	if err := requestSchema.Validate(map[string]any{"artifact_id": "fixture", "input_path": "/synthetic/input.mkv", "future": true}); err != nil {
		t.Fatal(err)
	}
	if err := requestSchema.Validate(map[string]any{"artifact_id": "../bad", "input_path": "/synthetic/input.mkv"}); err == nil {
		t.Fatal("invalid opaque handle accepted")
	}
	resultSchema, err := compiler.Compile(schemaURL + op.Responses["200"].Content["application/json"].Schema.Ref)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "synthetic-worker-secret"
	cfg.Playback.TranscodeDir = t.TempDir()
	cfg.Download.ArtifactDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(cfg.Download.ArtifactDir, "fixture.mp4"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	watcher := nodeconfig.NewWatcher(nil, nil, nil, nodeconfig.BootstrapOverrides{})
	watcher.SetConfigForTest(cfg)
	server := transcodenode.NewServer(watcher, nil)
	server.SetInputPathAuthorizer(prepareProtocolPathAuthority{})
	handler := server.Handler()
	for _, tc := range []struct {
		body   string
		auth   bool
		status int
	}{
		{"{", false, 401}, {"{", true, 400}, {"{}", true, 400},
		{`{"artifact_id":"fixture","input_path":"/synthetic/input.mkv","future":true}`, true, 200},
		{`{"artifact_id":"fixture","input_path":"/synthetic/input.mkv","audio_recipe_version":"partial"}`, true, 400},
		{strings.Repeat(" ", 64<<10) + "{}", true, 400},
	} {
		req := httptest.NewRequest(http.MethodPost, op.Path, strings.NewReader(tc.body))
		if tc.auth {
			req.Header.Set("Authorization", "Bearer "+cfg.Auth.JWTSecret)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != tc.status {
			t.Fatal(tc.status, rec.Code, rec.Body)
		}
		if tc.status == 200 {
			var body any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if err := resultSchema.Validate(body); err != nil {
				t.Fatal(err)
			}
			if body.(map[string]any)["file_size"] != float64(7) {
				t.Fatal(body)
			}
		} else if op.Responses[strconv.Itoa(tc.status)].Content["text/plain"] == nil {
			t.Fatal("missing refusal", tc.status)
		}
	}
	watcher.SetConfigForTest(nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, op.Path, strings.NewReader(`{"artifact_id":"fixture","input_path":"/synthetic/input.mkv"}`)))
	if rec.Code != 503 {
		t.Fatal(rec.Code, rec.Body)
	}
}
