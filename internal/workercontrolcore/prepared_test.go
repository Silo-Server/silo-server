package workercontrolcore

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

func TestPreparedReceiptBridgesOnlyVerifiedOpaqueExecution(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	store := NewMemoryStore()
	node := Node{ID: 7, URL: "https://worker.example", PublicKey: pub, Enabled: true}
	service, err := New(Options{Origin: "https://core.example", PrivateKey: key, Nodes: testNodes{[]Node{node}}, Store: store, Sources: &testSources{store: store}})
	if err != nil {
		t.Fatal(err)
	}
	original := downloadprepare.Request{ArtifactID: "artifact", InputPath: "/private/approved.mp4", TargetCodecVideo: "h264", TargetCodecAudio: "aac", TargetAudioChannels: 2, TargetBitrateKbps: 4000}
	body, _ := json.Marshal(original)
	bad := false
	var workerFingerprint string
	transport := service.WrapTransport(testTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodHead {
			header := http.Header{}
			downloadprepare.SetResultHeaders(header, downloadprepare.Result{ArtifactID: "artifact", FileSize: 100, ExecutionFingerprint: workerFingerprint})
			return &http.Response{StatusCode: 200, Header: header, Body: http.NoBody, Request: r}, nil
		}
		var a workerwire.Assignment
		if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
			t.Fatal(err)
		}
		var prepared downloadprepare.Request
		_ = json.Unmarshal(a.Body, &prepared)
		if prepared.SourceIdentity == "" || prepared.InputPath == original.InputPath {
			t.Fatal("prepared request lacks admitted source identity")
		}
		workerFingerprint = prepared.ExecutionFingerprint()
		fingerprint := workerFingerprint
		if bad {
			fingerprint = "wrong"
		}
		result, _ := json.Marshal(downloadprepare.Result{ArtifactID: "artifact", FileSize: 100, ExecutionFingerprint: fingerprint})
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(result))), Request: r}, nil
	}))
	req := httptest.NewRequest("POST", node.URL+"/downloads/prepare", strings.NewReader(string(body)))
	response, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var result downloadprepare.Result
	if err = json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.ExecutionFingerprint != original.ExecutionFingerprint() {
		t.Fatal("legacy core fingerprint not restored")
	}
	statResponse, statErr := transport.RoundTrip(httptest.NewRequest("HEAD", node.URL+"/downloads/artifacts/artifact", nil))
	if statErr != nil {
		t.Fatal(statErr)
	}
	attestation, err := downloadprepare.ResultFromHeaders(statResponse.Header)
	_ = statResponse.Body.Close()
	if err != nil || attestation.ExecutionFingerprint != original.ExecutionFingerprint() {
		t.Fatal("artifact stat lost verified core receipt")
	}
	bad = true
	req = httptest.NewRequest("POST", node.URL+"/downloads/prepare", strings.NewReader(string(body)))
	if badResponse, badErr := transport.RoundTrip(req); badErr == nil {
		if badResponse != nil {
			_ = badResponse.Body.Close()
		}
		t.Fatal("wrong execution fingerprint published")
	}
	live, ok, err := store.Get(t.Context(), 7, "artifact")
	if err != nil || !ok || live.Generation != 1 {
		t.Fatal("bad receipt replaced healthy prepared artifact")
	}
}
