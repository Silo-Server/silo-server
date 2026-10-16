package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/workercontrolcore"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

func TestWorkerOutputRelayMissingSecretFailsClosed(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("missing core secret panicked: %v", recovered)
		}
	}()
	request := httptest.NewRequest(http.MethodGet, "https://worker.example.test/transcode/work/master.m3u8", nil)
	if _, err := (workerOutputRelay{}).token(request, workercontrolcore.Node{}); err == nil {
		t.Fatal("missing core secret admitted output")
	}
}

//nolint:funlen,gocyclo // Keep this ordered failure-injection scenario and its exact authority assertions in one scope.
func TestWorkerPreparedRelayReusesExactCredentialArtifactAndGeneration(t *testing.T) {
	f := newContentFixture(t)
	prepared := f.preparedClaims(t, "720p")
	ctx := t.Context()
	if _, err := f.pool.Exec(ctx, "UPDATE download_artifacts SET file_size=10 WHERE id=$1", prepared.DownloadArtifactRowID); err != nil {
		t.Fatal(err)
	}
	node := workercontrolcore.Node{ID: prepared.RoutingExecutionNodeID, URL: prepared.TranscodeNode, Enabled: true, AllowHTTP: true}
	store := workercontrolcore.NewMemoryStore()
	start := func() workerwire.Assignment {
		t.Helper()
		assignment, err := store.Begin(ctx, workerwire.Assignment{Version: 1, NodeID: node.ID, WorkID: prepared.DownloadArtifactID, Path: "/downloads/prepare"})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Complete(ctx, assignment); err != nil {
			t.Fatal(err)
		}
		if err := store.Activate(ctx, node.ID, assignment.WorkID, assignment.Generation); err != nil {
			t.Fatal(err)
		}
		return assignment
	}
	assignment := start()
	loginA, loginB := uuid.NewString(), uuid.NewString()
	sessions := auth.NewSessionRepository(f.pool)
	for _, id := range []string{loginA, loginB} {
		if err := sessions.Create(ctx, models.AuthSession{ID: id, UserID: f.user, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	const secret = "prepared-relay-source-secret"
	now := time.Now()
	registry := workercontrolcore.NewMemoryOutputAuthorities()
	relay := workerOutputRelay{pool: f.pool, authorities: registry, assignments: store, secret: func() string { return secret }, now: func() time.Time { return now }}
	request := func(caller *auth.Claims) *http.Request {
		r := httptest.NewRequest(http.MethodGet, node.URL+"/downloads/artifacts/"+prepared.DownloadArtifactID, nil)
		r.Header.Set("Range", "bytes=0-4")
		return r.WithContext(apimw.SetProfileID(apimw.SetClaims(ctx, caller), "viewer"))
	}
	caller := &auth.Claims{UserID: f.user, SessionID: loginA, TokenType: auth.TokenTypeAccess}
	first, err := relay.token(request(caller), node)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := verifyWorkerOutputToken(ctx, f.pool, f.gate, first, secret)
	if err != nil {
		t.Fatal(err)
	}
	if claims.OriginLoginSessionID != loginA || claims.DownloadArtifactRowID != prepared.DownloadArtifactRowID || claims.DownloadArtifactID != prepared.DownloadArtifactID || claims.MediaFileID != f.file || claims.DownloadArtifactSize != 10 {
		t.Fatalf("prepared relay lost original credential or row identity: %+v", claims)
	}
	lookupKey := workercontrolcore.OutputAuthorityLookupKey(*claims, node.ID, assignment.WorkID, assignment.Generation, workeridentity.OutputEpoch(secret))
	if _, err := registry.Register(ctx, workercontrolcore.OutputAuthorityRecord{Token: first, LookupKey: lookupKey, NodeID: node.ID, WorkID: assignment.WorkID, Generation: assignment.Generation, Epoch: workeridentity.OutputEpoch(secret), ExpiresAt: claims.ExpiresAt.Unix()}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	second, err := relay.token(request(caller), node)
	if err != nil || second != first {
		t.Fatalf("Range request did not reuse exact retained token: equal=%v error=%v", second == first, err)
	}
	if _, err := f.pool.Exec(ctx, "UPDATE download_artifacts SET origin_node_url=$2 WHERE id=$1", prepared.DownloadArtifactRowID, strings.Replace(node.URL, f.content, strings.ToUpper(f.content), 1)+"/"); err != nil {
		t.Fatal(err)
	}
	if equivalent, err := relay.token(request(caller), node); err != nil || equivalent != first {
		t.Errorf("equivalent artifact origin lost relay authority: equal=%v error=%v", equivalent == first, err)
	}
	if _, err := f.pool.Exec(ctx, "UPDATE download_artifacts SET origin_node_url=$2 WHERE id=$1", prepared.DownloadArtifactRowID, node.URL); err != nil {
		t.Fatal(err)
	}
	duplicate := uuid.NewString()
	if _, err := f.pool.Exec(ctx, `INSERT INTO download_artifacts (id,media_file_id,format,params_hash,status,origin_node_id,origin_node_url,origin_artifact_id)
		SELECT $2,media_file_id,format,$2,status,origin_node_id,origin_node_url,origin_artifact_id FROM download_artifacts WHERE id=$1`, prepared.DownloadArtifactRowID, duplicate); err != nil {
		t.Fatal(err)
	}
	if _, err := relay.token(request(caller), node); err == nil {
		t.Error("ambiguous artifact rows received a relay token")
	}
	if _, err := f.pool.Exec(ctx, "DELETE FROM download_artifacts WHERE id=$1", duplicate); err != nil {
		t.Fatal(err)
	}
	callerB := &auth.Claims{UserID: f.user, SessionID: loginB, TokenType: auth.TokenTypeAccess}
	other, err := relay.token(request(callerB), node)
	if err != nil || other == first {
		t.Fatalf("another login reused source authority: equal=%v error=%v", other == first, err)
	}
	for _, fileID := range []int{0, f.file + 1} {
		link := &auth.Claims{UserID: f.user, SessionID: loginA, TokenType: auth.TokenTypeDirectDownloadLink, FileID: fileID}
		if _, err := relay.token(request(link), node); err == nil {
			t.Errorf("direct-download link for file %d authorized artifact for file %d", fileID, f.file)
		}
	}
	link := &auth.Claims{UserID: f.user, SessionID: loginA, TokenType: auth.TokenTypeDirectDownloadLink, FileID: f.file}
	if token, err := relay.token(request(link), node); err != nil || token != first {
		t.Fatalf("valid direct-download link lost its exact source authority: equal=%v error=%v", token == first, err)
	}
	if err := store.Stop(ctx, node.ID, assignment.WorkID); err != nil {
		t.Fatal(err)
	}
	start()
	reminted, err := relay.token(request(caller), node)
	if err != nil || reminted == first {
		t.Fatalf("replacement generation reused previous record: equal=%v error=%v", reminted == first, err)
	}
	if err := sessions.Revoke(ctx, loginA); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyWorkerOutputToken(ctx, f.pool, f.gate, first, secret); err == nil {
		t.Fatal("prepared relay survived original login revocation")
	}
	if _, err := verifyWorkerOutputToken(ctx, f.pool, f.gate, other, secret); err != nil {
		t.Fatalf("another same-account login was revoked: %v", err)
	}
	if _, err := f.pool.Exec(ctx, "UPDATE download_artifacts SET status='failed' WHERE id=$1", prepared.DownloadArtifactRowID); err != nil {
		t.Fatal(err)
	}
	if _, err := relay.token(request(callerB), node); err == nil {
		t.Fatal("unready artifact received private relay credential")
	}
}
