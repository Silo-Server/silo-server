package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodehttp"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/workercontrol"
	"github.com/Silo-Server/silo-server/internal/workercontrolcore"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerproxy"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

func TestReadyWorkerOutputRevokesCachedViewerPermissionWithoutStoppingArtifact(t *testing.T) {
	f := newContentFixture(t)
	corePublic, coreKey, _ := ed25519.GenerateKey(rand.Reader)
	nodePublic, nodeKey, _ := ed25519.GenerateKey(rand.Reader)
	worker := httptest.NewUnstartedServer(nil)
	defer worker.Close()
	nodeURL := "http://" + worker.Listener.Addr().String()
	artifact := uuid.NewString()
	work := artifact + "-attempt"
	var nodeID int
	if err := f.pool.QueryRow(t.Context(), "INSERT INTO stream_nodes(url,name,type,enabled) VALUES($1,'Cached output fixture','transcode',TRUE) RETURNING id", nodeURL).Scan(&nodeID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), "DELETE FROM download_artifacts WHERE id=$1", artifact)
		_, _ = f.pool.Exec(context.Background(), "DELETE FROM stream_nodes WHERE id=$1", nodeID)
	})
	if _, err := f.pool.Exec(t.Context(), "INSERT INTO download_artifacts(id,media_file_id,format,params_hash,status,attempts,lease_owner,lease_expires_at,worker_kind,worker_node_id) VALUES($1,$2,'mp4',$1,'running',1,'output-owner',now()+interval '1 minute','node',$3)", artifact, f.file, nodeID); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(downloadprepare.Request{ArtifactID: work, LogSessionID: "download-prepare-" + artifact})
	store := workercontrolcore.NewMemoryStore()
	a, err := store.Begin(t.Context(), workerwire.Assignment{Version: 1, NodeID: nodeID, WorkID: work, Path: workerPreparePath, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	node := workercontrolcore.Node{ID: nodeID, URL: nodeURL, PublicKey: nodePublic, Enabled: true, AllowHTTP: true}
	if err = admitWorkerDownload(t.Context(), f.pool, node, a); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(t.Context(), "UPDATE download_artifacts SET status='ready',origin_node_id=$2,origin_artifact_id=$3,origin_node_url=$4,lease_owner=NULL,lease_expires_at=NULL WHERE id=$1", artifact, nodeID, work, nodeURL); err != nil {
		t.Fatal(err)
	}
	if err = store.BindInputs(t.Context(), nodeID, work, a.Generation, map[string]string{"receipt:original": strings.Repeat("a", 64), "receipt:worker": strings.Repeat("b", 64)}); err != nil {
		t.Fatal(err)
	}
	if err = store.Complete(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if err = store.Activate(t.Context(), nodeID, work, a.Generation); err != nil {
		t.Fatal(err)
	}
	var service *workercontrolcore.Service
	core := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { service.Handler().ServeHTTP(w, r) }))
	defer core.Close()
	coreURL := "http://" + core.Listener.Addr().String()
	const secret = "ready-output-revocation-test-secret"
	outputAuthorities := workercontrolcore.NewMemoryOutputAuthorities()
	service, err = workercontrolcore.New(workercontrolcore.Options{OutputAuthorities: outputAuthorities, Origin: coreURL, AllowHTTP: true, PrivateKey: coreKey, Nodes: reportNodes{node}, Store: store, ExecutionActive: func(ctx context.Context, n workercontrolcore.Node, a workerwire.Assignment) bool {
		return workerDownloadActive(ctx, f.pool, n.ID, a)
	}, ExecutionConfig: func(context.Context, int) (workerwire.ExecutionConfig, error) {
		return workerwire.ExecutionConfig{Revision: "ready-output-test", SegmentRetentionSeconds: 600, SubtitleSyncCapacity: 1, OutputAuthorizationEpoch: workeridentity.OutputEpoch(secret)}, nil
	}, VerifyToken: func(ctx context.Context, encoded string) (*streamtoken.Claims, error) {
		return verifyWorkerOutputToken(ctx, f.pool, f.gate, encoded, secret)
	}})
	if err != nil {
		t.Fatal(err)
	}
	core.Start()
	scratch := t.TempDir()
	if err = os.MkdirAll(filepath.Join(scratch, downloadprepare.ArtifactDirectoryName), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(scratch, downloadprepare.ArtifactDirectoryName, work+".mp4"), []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	const artifactBytes = 64 << 20
	if err = os.Truncate(filepath.Join(scratch, downloadprepare.ArtifactDirectoryName, work+".mp4"), artifactBytes); err != nil {
		t.Fatal(err)
	}
	receipt, _ := json.Marshal(downloadprepare.Result{ArtifactID: work, FileSize: artifactBytes, ExecutionFingerprint: strings.Repeat("b", 64)})
	if err = os.WriteFile(filepath.Join(scratch, downloadprepare.ArtifactDirectoryName, work+".mp4.receipt.json"), receipt, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := workercontrol.New(t.Context(), workercontrol.Options{Binding: workeridentity.Binding{Version: 1, NodeID: nodeID, CoreURL: coreURL, CorePublicKey: corePublic, NodePrivateKey: nodeKey, NodeURL: nodeURL, AllowHTTP: true}, Scratch: scratch, HWAccel: "none"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtime.Close(context.Background()) }()
	worker.Config.Handler = runtime.Handler()
	worker.Start()
	runtime.Run()
	login := uuid.NewString()
	if err := auth.NewSessionRepository(f.pool).Create(t.Context(), models.AuthSession{ID: login, UserID: f.user, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	claims := streamtoken.Claims{OriginCredential: streamtoken.OriginCredential{OriginLoginSessionID: login}, SessionID: "download-viewer", DownloadArtifactRowID: artifact, DownloadArtifactID: work, TranscodeNode: nodeURL, RoutingExecutionNodeID: nodeID, PlayMethod: streamtoken.PlayMethodDownload, UserID: f.user, ProfileID: "viewer", MediaFileID: f.file}
	token, err := streamtoken.Sign(claims, secret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := service.IssueOutputGrant(t.Context(), token, nodeURL, secret)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := workeridentity.DecodeGrant(reply.Grant, workeridentity.DomainProxyOutputGrant, nodeURL, corePublic, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	private, err := workeridentity.OutputPrivateKey(secret, grant)
	if err != nil {
		t.Fatal(err)
	}
	client := workeridentity.NewHTTPClient(true, time.Second)
	defer client.CloseIdleConnections()
	getGrant := func(encoded string, signingKey ed25519.PrivateKey) int {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, nodeURL+"/downloads/artifacts/"+work, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Silo-Stream-Token", encoded)
		req.Header.Set("Range", "bytes=0-9")
		if err = workeridentity.Sign(req, nodeURL, workeridentity.DomainProxyOutputRequest, signingKey, nodeID, true); err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		_, _ = io.Copy(io.Discard, response.Body)
		return response.StatusCode
	}
	get := func() int { return getGrant(reply.Grant, private) }
	other := newContentFixture(t)
	otherClaims := claims
	otherClaims.UserID = other.user
	otherLogin := uuid.NewString()
	if err := auth.NewSessionRepository(f.pool).Create(t.Context(), models.AuthSession{ID: otherLogin, UserID: other.user, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	otherClaims.OriginLoginSessionID = otherLogin
	otherClaims.SessionID = "second-download-viewer"
	otherToken, err := streamtoken.Sign(otherClaims, secret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	otherReply, err := service.IssueOutputGrant(t.Context(), otherToken, nodeURL, secret)
	if err != nil {
		t.Fatal(err)
	}
	otherGrant, err := workeridentity.DecodeGrant(otherReply.Grant, workeridentity.DomainProxyOutputGrant, nodeURL, corePublic, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	otherPrivate, err := workeridentity.OutputPrivateKey(secret, otherGrant)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for get() != http.StatusPartialContent {
		if time.Now().After(deadline) {
			t.Fatal("valid ready output never became available")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Exercise the core's tokenless HTTPPreparer relay against an already-warm
	// real worker. A newly minted viewer must be accepted on its first read,
	// without manually refreshing its authority snapshot after registration.
	outputs, err := workerproxy.New(coreURL, secret, reportNodes{node})
	if err != nil {
		t.Fatal(err)
	}
	outputs.Grant = func(ctx context.Context, token, origin string) (workercontrolcore.GrantReply, error) {
		return service.IssueOutputGrant(ctx, token, origin, secret)
	}
	relay := workerOutputRelay{pool: f.pool, authorities: outputAuthorities, assignments: store, secret: func() string { return secret }}
	outputs.ResolveOutput = relay.token
	nodehttp.Configure(func(base http.RoundTripper) http.RoundTripper {
		return outputs.WrapOutputs(base, service.WrapTransport(base))
	})
	t.Cleanup(func() { nodehttp.Configure(nil) })
	relayContext := apimw.SetProfileID(apimw.SetClaims(t.Context(), &auth.Claims{UserID: f.user, SessionID: login, TokenType: auth.TokenTypeAccess}), "viewer")
	readRelay := func() (int, error) {
		response, err := (downloadprepare.HTTPPreparer{}).Open(relayContext, nodeURL, secret, work, http.MethodGet, http.Header{"Range": []string{"bytes=0-9"}})
		if err != nil {
			return 0, err
		}
		defer func() { _ = response.Body.Close() }()
		_, err = io.Copy(io.Discard, response.Body)
		return response.StatusCode, err
	}
	if status, err := readRelay(); err != nil || status != http.StatusPartialContent {
		t.Fatalf("first tokenless prepared relay denied: status=%d error=%v", status, err)
	}
	slowClient := workeridentity.NewHTTPClient(true, 10*time.Second)
	defer slowClient.CloseIdleConnections()
	slowRequest, err := http.NewRequestWithContext(t.Context(), http.MethodGet, nodeURL+"/downloads/artifacts/"+work, nil)
	if err != nil {
		t.Fatal(err)
	}
	slowRequest.Header.Set("X-Silo-Stream-Token", reply.Grant)
	if err = workeridentity.Sign(slowRequest, nodeURL, workeridentity.DomainProxyOutputRequest, private, nodeID, true); err != nil {
		t.Fatal(err)
	}
	slowResponse, err := slowClient.Do(slowRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = slowResponse.Body.Close() }()
	if slowResponse.StatusCode != http.StatusOK || slowResponse.ContentLength != artifactBytes {
		t.Fatal("valid long download did not open")
	}
	first := make([]byte, 1024)
	if _, err = io.ReadFull(slowResponse.Body, first); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(t.Context(), "UPDATE users SET download_allowed=FALSE WHERE id=$1", f.user); err != nil {
		t.Fatal(err)
	}
	// Let multiple signed two-second authority windows expire. The shared
	// artifact remains healthy for other viewers; only this cached grant loses rights.
	timer := time.NewTimer(3100 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	received, readErr := io.Copy(io.Discard, slowResponse.Body)
	if readErr == nil || errors.Is(readErr, context.DeadlineExceeded) || received+int64(len(first)) >= artifactBytes {
		t.Fatalf("revoked already-open download was not fenced: received=%d err=%v", received, readErr)
	}
	if get() == http.StatusPartialContent {
		t.Fatal("cached ready grant survived viewer download permission revocation")
	}
	if status, err := readRelay(); err == nil && status < 400 {
		t.Fatalf("tokenless prepared relay bypassed viewer revocation: status=%d", status)
	}
	if getGrant(otherReply.Grant, otherPrivate) != http.StatusPartialContent {
		t.Fatal("viewer revocation stopped a different authorized viewer")
	}
	if !workerDownloadActive(t.Context(), f.pool, nodeID, a) {
		t.Fatal("viewer revocation unnecessarily stopped shared artifact")
	}
}
