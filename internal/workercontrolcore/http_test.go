package workercontrolcore

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

func TestSignedSnapshotReconstructionAndStop(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	corePub, coreKey, _ := ed25519.GenerateKey(rand.Reader)
	store := NewMemoryStore()
	node := Node{ID: 7, URL: "https://worker.example", PublicKey: pub, Enabled: true}
	service, err := New(Options{Origin: "https://core.example", PrivateKey: coreKey, Nodes: testNodes{[]Node{node}}, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	a, err := store.Begin(t.Context(), workerwire.Assignment{Version: 1, NodeID: 7, WorkID: "work", Body: []byte(`{"session_id":"work","input_path":"source:media-ref","target_bitrate_kbps":1234}`), Sources: map[string]workerwire.Source{"source:media-ref": {Ref: workerwire.SourceRef{ID: "media-ref", Kind: "media", Revision: "revision"}, NodeID: 7, WorkID: "work", Generation: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Complete(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if err = store.Activate(t.Context(), 7, "work", a.Generation); err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "https://core.example/api/internal/workers/7"+path, strings.NewReader(body))
		if err := workeridentity.Sign(r, "https://core.example", workeridentity.DomainNodeProof, key, 7, false); err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		service.Handler().ServeHTTP(w, r)
		return w
	}
	w := request("POST", "/snapshot", `{"boot_id":"boot","nonce":"challenge","sequence":10}`)
	if w.Code != 200 {
		t.Fatalf("snapshot=%d %s", w.Code, w.Body.String())
	}
	var envelope GrantReply
	if err = json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	grant, err := workeridentity.DecodeGrant(envelope.Grant, workeridentity.DomainSnapshotGrant, node.URL, corePub, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var snapshot workerwire.Snapshot
	if err = json.Unmarshal(grant.Body, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.BootID != "boot" || snapshot.Nonce != "challenge" || snapshot.Sequence != 11 || snapshot.Active["work"] != 1 || snapshot.ValidForMillis != 2000 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	w = request("GET", "/recipes/work", "")
	if w.Code != 200 {
		t.Fatalf("recipe=%d %s", w.Code, w.Body.String())
	}
	if err = json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	grant, err = workeridentity.DecodeGrant(envelope.Grant, "silo/worker-recipe/v1", node.URL, corePub, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var reply RecipeReply
	if err = json.Unmarshal(grant.Body, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Recipe.InputPath != "source:media-ref" || reply.Recipe.TargetBitrateKbps != 1234 || reply.Assignment.Generation != 1 || grant.WorkID != "work" {
		t.Fatalf("recipe=%+v", reply)
	}
	if err = store.Stop(t.Context(), 7, "work"); err != nil {
		t.Fatal(err)
	}
	if w = request("GET", "/recipes/work", ""); w.Code == 200 {
		t.Fatal("stopped work reconstructed")
	}
	if w = request("POST", "/snapshot", `{"boot_id":"boot","nonce":"next","sequence":11}`); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if err = json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	grant, err = workeridentity.DecodeGrant(envelope.Grant, workeridentity.DomainSnapshotGrant, node.URL, corePub, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	snapshot = workerwire.Snapshot{}
	_ = json.Unmarshal(grant.Body, &snapshot)
	if len(snapshot.Active) != 0 {
		t.Fatal("stopped work renewed")
	}
}

func TestWrongNodeAndRevokedExecutionCannotObtainOutputGrant(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	_, coreKey, _ := ed25519.GenerateKey(rand.Reader)
	store := NewMemoryStore()
	node := Node{ID: 7, URL: "https://worker.example", PublicKey: pub, Enabled: true}
	allow := true
	service, err := New(Options{Origin: "https://core.example", PrivateKey: coreKey, Nodes: testNodes{[]Node{node}}, Store: store, ExecutionActive: func(_ context.Context, _ Node, _ workerwire.Assignment) bool { return allow }, VerifyToken: func(context.Context, string) (*streamtoken.Claims, error) {
		return &streamtoken.Claims{SessionID: "work", TranscodeNode: node.URL, RoutingExecutionNodeID: 7, MediaPath: "/private/approved.mp4", RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	a, err := store.Begin(t.Context(), workerwire.Assignment{Version: 1, NodeID: 7, WorkID: "work", Body: []byte(`{"session_id":"work","input_path":"source:media-ref"}`), Sources: map[string]workerwire.Source{"source:media-ref": {Ref: workerwire.SourceRef{ID: "media-ref", Kind: "media", Revision: "revision"}, NodeID: 7, WorkID: "work", Generation: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Complete(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if err = store.Activate(t.Context(), 7, "work", a.Generation); err != nil {
		t.Fatal(err)
	}
	request := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "https://core.example/api/internal/workers/"+path+"/tokens", strings.NewReader(`{"token":"verified","work_id":"work"}`))
		if err := workeridentity.Sign(r, "https://core.example", workeridentity.DomainNodeProof, key, 7, false); err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		service.Handler().ServeHTTP(w, r)
		return w
	}
	if w := request("8"); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong node=%d", w.Code)
	}
	w := request("7")
	if w.Code != 200 {
		t.Fatalf("grant=%d %s", w.Code, w.Body.String())
	}
	var envelope GrantReply
	_ = json.Unmarshal(w.Body.Bytes(), &envelope)
	grant, err := workeridentity.DecodeGrant(envelope.Grant, workeridentity.DomainOutputGrant, node.URL, coreKey.Public().(ed25519.PublicKey), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var claims streamtoken.Claims
	_ = json.Unmarshal(grant.Body, &claims)
	if claims.MediaPath != "source:media-ref" || grant.Generation != 1 {
		t.Fatal("core media path or wrong generation reached worker")
	}
	allow = false
	if w = request("7"); w.Code == 200 {
		t.Fatal("revoked recipe still grants output")
	}
}

type testRecipes struct{ card *playback.RecipeCard }

func (s *testRecipes) Get(context.Context, string) (*playback.RecipeCard, bool) {
	if s.card == nil {
		return nil, false
	}
	copy := *s.card
	return &copy, true
}
func (s *testRecipes) Delete(context.Context, string) error     { s.card = nil; return nil }
func (s *testRecipes) RevokeNode(context.Context, string) error { s.card = nil; return nil }
func TestRecipeUsesCurrentCoreCardAndCannotRetargetSource(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	_, coreKey, _ := ed25519.GenerateKey(rand.Reader)
	store := NewMemoryStore()
	node := Node{ID: 7, URL: "https://worker.example", PublicKey: pub, Enabled: true}
	recipes := &testRecipes{card: &playback.RecipeCard{SessionID: "work", InputPath: "/private/approved.mp4", TranscodeNodeURL: node.URL, RoutingExecutionNodeID: 7, TargetBitrateKbps: 1234}}
	service, err := New(Options{Origin: "https://core.example", PrivateKey: coreKey, Nodes: testNodes{[]Node{node}}, Store: store, RecipeStore: recipes, Sources: &testSources{store: store}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", node.URL+"/transcode/start", strings.NewReader(`{"session_id":"work","input_path":"/private/approved.mp4","target_bitrate_kbps":1234}`))
	out, err := service.PrepareRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if err = service.FinishRequest(out, &http.Response{StatusCode: 202}, nil); err != nil {
		t.Fatal(err)
	}
	recipes.card.TargetBitrateKbps = 4321
	request := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "https://core.example/api/internal/workers/7/recipes/work", nil)
		if err := workeridentity.Sign(r, "https://core.example", workeridentity.DomainNodeProof, key, 7, false); err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		service.Handler().ServeHTTP(w, r)
		return w
	}
	w := request()
	if w.Code != 200 {
		t.Fatalf("current recipe=%d %s", w.Code, w.Body.String())
	}
	var envelope GrantReply
	_ = json.Unmarshal(w.Body.Bytes(), &envelope)
	grant, err := workeridentity.DecodeGrant(envelope.Grant, "silo/worker-recipe/v1", node.URL, coreKey.Public().(ed25519.PublicKey), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var reply RecipeReply
	_ = json.Unmarshal(grant.Body, &reply)
	if reply.Recipe.TargetBitrateKbps != 4321 || reply.Recipe.InputPath != "source:media-ref" || strings.Contains(string(grant.Body), "/private/") {
		t.Fatalf("stale/leaked recipe: %s", grant.Body)
	}
	recipes.card.InputPath = "/private/unapproved.mp4"
	if w = request(); w.Code == 200 {
		t.Fatal("mutable recipe silently changed granted input")
	}
}

func TestNodeProofReplayIsRejectedAcrossCoreInstances(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	_, coreKey, _ := ed25519.GenerateKey(rand.Reader)
	store := NewMemoryStore()
	options := Options{Origin: "https://core.example", PrivateKey: coreKey, Nodes: testNodes{[]Node{{ID: 7, URL: "https://worker.example", PublicKey: pub, Enabled: true}}}, Store: store}
	one, _ := New(options)
	two, _ := New(options)
	r := httptest.NewRequest("POST", "https://core.example/api/internal/workers/7/snapshot", strings.NewReader(`{"boot_id":"boot","nonce":"one","sequence":0}`))
	if err := workeridentity.Sign(r, "https://core.example", workeridentity.DomainNodeProof, key, 7, false); err != nil {
		t.Fatal(err)
	}
	if err := one.VerifyRequest(r, 7, workeridentity.DomainNodeProof); err != nil {
		t.Fatal(err)
	}
	if err := two.VerifyRequest(r, 7, workeridentity.DomainNodeProof); err == nil {
		t.Fatal("proof replayed on another core")
	}
}
