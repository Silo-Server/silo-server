package workercontrolcore

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

func TestProxyOutputGrantBindsPreparedReceiptNodeAndLiveGeneration(t *testing.T) {
	nodePub, _, _ := ed25519.GenerateKey(rand.Reader)
	corePub, coreKey, _ := ed25519.GenerateKey(rand.Reader)
	node := Node{ID: 7, URL: "https://worker.example", PublicKey: nodePub, Enabled: true}
	store := NewMemoryStore()
	a, err := store.Begin(t.Context(), workerwire.Assignment{Version: 1, NodeID: 7, WorkID: "artifact", Path: preparePath})
	if err != nil {
		t.Fatal(err)
	}
	const secret = "output-grant-test-secret"
	original, worker := strings.Repeat("a", 64), strings.Repeat("b", 64)
	if err := store.BindInputs(t.Context(), 7, "artifact", a.Generation, map[string]string{receiptOriginal: original, receiptWorker: worker}); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if err := store.Activate(t.Context(), 7, "artifact", a.Generation); err != nil {
		t.Fatal(err)
	}
	service, err := New(Options{OutputAuthorities: NewMemoryOutputAuthorities(), Origin: "https://core.example", PrivateKey: coreKey, Nodes: testNodes{[]Node{node}}, Store: store, VerifyToken: func(_ context.Context, encoded string) (*streamtoken.Claims, error) {
		return streamtoken.Verify(encoded, secret)
	}})
	if err != nil {
		t.Fatal(err)
	}
	claims := streamtoken.Claims{SessionID: "download", DownloadArtifactID: "artifact", TranscodeNode: node.URL, RoutingExecutionNodeID: 7, PlayMethod: streamtoken.PlayMethodToneMapDownload, DownloadExecutionFingerprint: original, DownloadArtifactSize: 10, UserID: 5, ProfileID: "profile", MediaFileID: 9}
	token, err := streamtoken.Sign(claims, secret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := service.IssueOutputGrant(t.Context(), token, node.URL, secret)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := workeridentity.DecodeGrant(reply.Grant, workeridentity.DomainProxyOutputGrant, node.URL, corePub, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if grant.WorkID != "artifact" || grant.Generation != a.Generation || grant.NodeID != 7 {
		t.Fatalf("grant scope=%+v", grant)
	}
	var authority workeridentity.OutputAuthority
	if err := json.Unmarshal(grant.Body, &authority); err != nil {
		t.Fatal(err)
	}
	expected, err := workeridentity.OutputPrivateKey(secret, grant)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(authority.ProxyPublicKey, expected.Public().(ed25519.PublicKey)) || authority.ArtifactFingerprint != worker || authority.ClientFingerprint != original || authority.Epoch != workeridentity.OutputEpoch(secret) {
		t.Fatal("delegated authority changed receipt/key/epoch")
	}
	if bytes.Contains(grant.Body, []byte(secret)) || bytes.Contains(grant.Body, coreKey) || bytes.Contains(grant.Body, expected) {
		t.Fatal("private authority escaped output grant")
	}
	for name, modify := range map[string]func(*streamtoken.Claims){"node": func(c *streamtoken.Claims) { c.RoutingExecutionNodeID = 8 }, "url": func(c *streamtoken.Claims) { c.TranscodeNode = "https://other.example" }, "work": func(c *streamtoken.Claims) { c.DownloadArtifactID = "other" }, "receipt": func(c *streamtoken.Claims) { c.DownloadExecutionFingerprint = strings.Repeat("c", 64) }} {
		t.Run(name, func(t *testing.T) {
			bad := claims
			modify(&bad)
			encoded, err := streamtoken.Sign(bad, secret, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.IssueOutputGrant(t.Context(), encoded, node.URL, secret); !errors.Is(err, ErrDenied) {
				t.Fatalf("invalid authority=%v", err)
			}
		})
	}
	if _, err := service.IssueOutputGrant(t.Context(), "forged-client-token", node.URL, secret); !errors.Is(err, ErrDenied) {
		t.Fatal("forged client token accepted")
	}
	expired, _ := streamtoken.Sign(claims, secret, -time.Minute)
	if _, err := service.IssueOutputGrant(t.Context(), expired, node.URL, secret); !errors.Is(err, ErrDenied) {
		t.Fatal("expired client token accepted")
	}
	if err := service.stop(t.Context(), node, "artifact"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.IssueOutputGrant(t.Context(), token, node.URL, secret); !errors.Is(err, ErrDenied) {
		t.Fatalf("stop did not fence output delegation: %v", err)
	}
}
