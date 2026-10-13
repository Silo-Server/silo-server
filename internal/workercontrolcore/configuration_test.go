package workercontrolcore

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

func TestSnapshotSignsOnlyAllowlistedExecutionConfigAndFailsClosed(t *testing.T) {
	nodePublic, nodeKey, _ := ed25519.GenerateKey(rand.Reader)
	corePublic, coreKey, _ := ed25519.GenerateKey(rand.Reader)
	node := Node{ID: 7, URL: "https://worker.example", PublicKey: nodePublic, Enabled: true}
	settings := workerwire.ExecutionConfig{Revision: "r1", SegmentRetentionSeconds: 120, SubtitleSyncCapacity: 2, Drain: true, OutputAuthorizationEpoch: "opaque"}
	var configErr error
	service, err := New(Options{Origin: "https://core.example", PrivateKey: coreKey, Nodes: testNodes{[]Node{node}}, Store: NewMemoryStore(), ExecutionConfig: func(_ context.Context, id int) (workerwire.ExecutionConfig, error) {
		if id != node.ID {
			t.Error("wrong settings scope")
		}
		return settings, configErr
	}})
	if err != nil {
		t.Fatal(err)
	}
	request := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "https://core.example/api/internal/workers/7/snapshot", strings.NewReader(`{"boot_id":"boot","nonce":"challenge","sequence":0}`))
		if err := workeridentity.Sign(r, "https://core.example", workeridentity.DomainNodeProof, nodeKey, node.ID, false); err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		service.Handler().ServeHTTP(w, r)
		return w
	}
	w := request()
	if w.Code != 200 {
		t.Fatalf("snapshot=%d %s", w.Code, w.Body.String())
	}
	var reply GrantReply
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	grant, err := workeridentity.DecodeGrant(reply.Grant, workeridentity.DomainSnapshotGrant, node.URL, corePublic, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var snapshot workerwire.Snapshot
	if err := json.Unmarshal(grant.Body, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.ExecutionConfig == nil || *snapshot.ExecutionConfig != settings {
		t.Fatalf("config=%+v", snapshot.ExecutionConfig)
	}
	configErr = errors.New("settings unavailable")
	if w = request(); w.Code != 503 {
		t.Fatal("failed config produced new signed authority")
	}
	configErr = nil
	settings.OutputAuthorizationEpoch = ""
	if w = request(); w.Code != 503 {
		t.Fatal("blank epoch produced signed settings")
	}
}
