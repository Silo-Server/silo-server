package apiv2

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodeconfig"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestWorkerStartProtocol(t *testing.T) {
	data, err := GenerateOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	registry := describeWorkerProtocols()
	index := -1
	for i, op := range registry.Operations {
		if op.Listener == "transcode_node" && op.Path == "/transcode/start" {
			index = i
		}
	}
	if index < 0 {
		t.Fatal("missing retained start")
	}
	op := registry.Operations[index]
	if op.RetrySafety != "non_retryable" || op.Responses["202"].Content["application/json"] != nil {
		t.Fatal("fabricated start contract")
	}
	compiler := jsonschema.NewCompiler()
	const schemaURL = "https://schema.example.invalid/worker-start.json"
	if err := compiler.AddResource(schemaURL, document); err != nil {
		t.Fatal(err)
	}
	request, err := compiler.Compile(schemaURL + op.RequestBody.Content["application/json"].Schema.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := request.Validate(map[string]any{"session_id": "fixture", "input_path": "/synthetic/input.mkv", "future": true}); err != nil {
		t.Fatal(err)
	}
	encoded := op.Responses["202"].Content["text/plain"].Schema
	if encoded.Type != "string" || encoded.Extensions["contentMediaType"] != "application/json" {
		t.Fatal("missing JSON text annotation")
	}
	result, err := compiler.Compile(schemaURL + "#/x-silo-worker-protocols/operations/" + strconv.Itoa(index) + "/responses/202/content/text~1plain/schema/contentSchema")
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(transcodenode.TranscodeStartResponse{SessionID: "fixture", Status: "started"})
	if err != nil {
		t.Fatal(err)
	}
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatal(err)
	}
	if err := result.Validate(value); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "synthetic-start-protocol"
	cfg.Playback.TranscodeDir = t.TempDir()
	watcher := nodeconfig.NewWatcher(nil, nil, nil, nodeconfig.BootstrapOverrides{})
	watcher.SetConfigForTest(cfg)
	handler := transcodenode.NewServer(watcher, nil).Handler()
	for _, tc := range []struct {
		body   string
		auth   bool
		status int
	}{
		{"{}", false, 401}, {"{", true, 400}, {"{}", true, 400},
		{`{"session_id":"fixture","input_path":"/synthetic/input.mkv"}`, true, 503},
	} {
		req := httptest.NewRequest(http.MethodPost, op.Path, strings.NewReader(tc.body))
		if tc.auth {
			req.Header.Set("Authorization", "Bearer "+cfg.Auth.JWTSecret)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != tc.status || op.Responses[strconv.Itoa(rec.Code)].Content["text/plain"] == nil {
			t.Fatal(rec.Code, rec.Body)
		}
	}
}
