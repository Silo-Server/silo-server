package main

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

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/workercontrolcore"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

//nolint:funlen,gocyclo // Keep this ordered failure-injection scenario and its exact authority assertions in one scope.
func TestWorkerOutputAuthorityRevokesExactOriginatingCredential(t *testing.T) {
	f := newContentFixture(t)
	ctx := t.Context()
	sessions := auth.NewSessionRepository(f.pool)
	loginA, loginB, remote := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, id := range []string{loginA, loginB, remote} {
		session := models.AuthSession{ID: id, UserID: f.user, ExpiresAt: time.Now().Add(time.Hour)}
		if err := sessions.Create(ctx, session); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.pool.Exec(ctx, "UPDATE auth_sessions SET purpose='remote_playback',bound_profile_id='viewer',absolute_expires_at=NOW()+INTERVAL '1 hour' WHERE id=$1", remote); err != nil {
		t.Fatal(err)
	}
	const secret = "origin-credential-test-secret"
	check := func(origin streamtoken.OriginCredential, profile string, want bool) {
		t.Helper()
		claims := f.card.ToClaims()
		claims.OriginCredential, claims.ProfileID = origin, profile
		// Reissuing a seek token must preserve the source login, not make its
		// revocation disappear behind a fresh signature or playback ID.
		claims.SeekSeconds = 30
		token, err := streamtoken.Sign(claims, secret, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		_, err = verifyWorkerOutputToken(ctx, f.pool, f.gate, token, secret)
		if (err == nil) != want {
			t.Fatalf("origin login=%q key=%d profile=%q allowed=%v want=%v: %v", origin.OriginLoginSessionID, origin.OriginAPIKeyID, profile, err == nil, want, err)
		}
	}
	// Exercise actual login revocation through the signed core snapshot used
	// by workers, with two independent logins for the same account and work.
	corePublic, coreKey, _ := ed25519.GenerateKey(rand.Reader)
	nodePublic, nodeKey, _ := ed25519.GenerateKey(rand.Reader)
	node := workercontrolcore.Node{ID: 7, URL: "https://worker.example.test", PublicKey: nodePublic, Enabled: true}
	store := workercontrolcore.NewMemoryStore()
	assignment, err := store.Begin(ctx, workerwire.Assignment{Version: 1, NodeID: node.ID, WorkID: f.card.SessionID, Path: "/transcode/start"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(ctx, assignment); err != nil {
		t.Fatal(err)
	}
	if err := store.Activate(ctx, node.ID, assignment.WorkID, assignment.Generation); err != nil {
		t.Fatal(err)
	}
	registry := workercontrolcore.NewMemoryOutputAuthorities()
	service, err := workercontrolcore.New(workercontrolcore.Options{Origin: "https://core.example.test", PrivateKey: coreKey, Nodes: reportNodes{node}, Store: store, OutputAuthorities: registry, VerifyToken: func(ctx context.Context, token string) (*streamtoken.Claims, error) {
		return verifyWorkerOutputToken(ctx, f.pool, f.gate, token, secret)
	}})
	if err != nil {
		t.Fatal(err)
	}
	register := func(login string) string {
		t.Helper()
		claims := f.card.ToClaims()
		claims.TranscodeNode = node.URL
		claims.OriginLoginSessionID = login
		token, err := streamtoken.Sign(claims, secret, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		verified, err := streamtoken.Verify(token, secret)
		if err != nil {
			t.Fatal(err)
		}
		id, err := registry.Register(ctx, workercontrolcore.OutputAuthorityRecord{Token: token, NodeID: node.ID, WorkID: assignment.WorkID, Generation: assignment.Generation, Epoch: "epoch", ExpiresAt: verified.ExpiresAt.Unix()})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	viewerA, viewerB := register(loginA), register(loginB)
	snapshot := func(sequence uint64) workerwire.Snapshot {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"boot_id": "origin-test", "nonce": "snapshot-test", "sequence": sequence})
		request := httptest.NewRequest(http.MethodPost, "https://core.example.test/api/internal/workers/7/snapshot", strings.NewReader(string(body)))
		if err := workeridentity.Sign(request, "https://core.example.test", workeridentity.DomainNodeProof, nodeKey, node.ID, false); err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("snapshot=%d %s", response.Code, response.Body.String())
		}
		var reply workercontrolcore.GrantReply
		if err := json.Unmarshal(response.Body.Bytes(), &reply); err != nil {
			t.Fatal(err)
		}
		grant, err := workeridentity.DecodeGrant(reply.Grant, workeridentity.DomainSnapshotGrant, node.URL, corePublic, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(grant.Body), loginA) || strings.Contains(string(grant.Body), loginB) {
			t.Fatal("snapshot exposed source login identity")
		}
		var result workerwire.Snapshot
		if err := json.Unmarshal(grant.Body, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	initial := snapshot(0)
	if !initial.Outputs[viewerA] || !initial.Outputs[viewerB] {
		t.Fatal("live same-account logins missing from ordinary output snapshot")
	}
	a := streamtoken.OriginCredential{OriginLoginSessionID: loginA}
	b := streamtoken.OriginCredential{OriginLoginSessionID: loginB}
	check(a, "viewer", true)
	check(b, "viewer", true)
	if err := sessions.Revoke(ctx, loginA); err != nil {
		t.Fatal(err)
	}
	check(a, "viewer", false)
	check(b, "viewer", true)
	updated := snapshot(1)
	if updated.Outputs[viewerA] || !updated.Outputs[viewerB] || updated.Active[assignment.WorkID] != assignment.Generation {
		t.Fatal("login revocation retained stale viewer or stopped another login/producer")
	}
	check(streamtoken.OriginCredential{OriginLoginSessionID: remote}, "viewer", true)
	check(streamtoken.OriginCredential{OriginLoginSessionID: remote}, "primary", false)
	if _, err := f.pool.Exec(ctx, "UPDATE auth_sessions SET expires_at=NOW()-INTERVAL '1 second' WHERE id=$1", loginB); err != nil {
		t.Fatal(err)
	}
	check(b, "viewer", false)
	check(streamtoken.OriginCredential{}, "viewer", false)
	check(streamtoken.OriginCredential{OriginLoginSessionID: uuid.NewString()}, "viewer", false)
	other := newContentFixture(t)
	foreignLogin := uuid.NewString()
	if err := sessions.Create(ctx, models.AuthSession{ID: foreignLogin, UserID: other.user, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	check(streamtoken.OriginCredential{OriginLoginSessionID: foreignLogin}, "viewer", false)
	if _, err := f.pool.Exec(ctx, "UPDATE auth_sessions SET absolute_expires_at=NOW()-INTERVAL '1 second' WHERE id=$1", remote); err != nil {
		t.Fatal(err)
	}
	check(streamtoken.OriginCredential{OriginLoginSessionID: remote}, "viewer", false)
	keys := auth.NewAPIKeyRepository(f.pool)
	key, err := keys.Create(ctx, f.user, "worker origin fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	foreignKey, err := keys.Create(ctx, other.user, "foreign worker fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	check(streamtoken.OriginCredential{OriginAPIKeyID: foreignKey.ID}, "viewer", false)
	originKey := streamtoken.OriginCredential{OriginAPIKeyID: key.ID}
	check(originKey, "viewer", true)
	check(streamtoken.OriginCredential{OriginLoginSessionID: remote, OriginAPIKeyID: key.ID}, "viewer", false)
	if _, err := f.pool.Exec(ctx, "UPDATE api_keys SET scopes=ARRAY['libraries:read'] WHERE id=$1", key.ID); err != nil {
		t.Fatal(err)
	}
	check(originKey, "viewer", false)
	if _, err := f.pool.Exec(ctx, "UPDATE api_keys SET scopes=ARRAY[]::TEXT[] WHERE id=$1", key.ID); err != nil {
		t.Fatal(err)
	}
	check(originKey, "viewer", true)
	if err := keys.Delete(ctx, key.ID, f.user); err != nil {
		t.Fatal(err)
	}
	check(originKey, "viewer", false)
}
