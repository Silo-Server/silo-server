package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/Silo-Server/silo-server/internal/workercontrolcore"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

type viewerQueryCounter struct{ count atomic.Int64 }

func (q *viewerQueryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	q.count.Add(1)
	return ctx
}
func (*viewerQueryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

type viewerAssignmentCounter struct {
	*workercontrolcore.MemoryStore
	gets, candidates, active, snapshots atomic.Int64
}

func (s *viewerAssignmentCounter) Get(ctx context.Context, node int, work string) (workerwire.Assignment, bool, error) {
	s.gets.Add(1)
	return s.MemoryStore.Get(ctx, node, work)
}
func (s *viewerAssignmentCounter) Candidate(ctx context.Context, node int, work string) (workerwire.Assignment, bool, error) {
	s.candidates.Add(1)
	return s.MemoryStore.Candidate(ctx, node, work)
}
func (s *viewerAssignmentCounter) IsActive(ctx context.Context, node int, work string, generation uint64) (bool, error) {
	s.active.Add(1)
	return s.MemoryStore.IsActive(ctx, node, work, generation)
}
func (s *viewerAssignmentCounter) Snapshot(ctx context.Context, node int) (workercontrolcore.AuthoritySnapshot, error) {
	s.snapshots.Add(1)
	return s.MemoryStore.Snapshot(ctx, node)
}

//nolint:funlen // Keep this ordered failure-injection scenario and its exact authority assertions in one scope.
func TestWorkerViewerSnapshotBatchesEquivalentContentAndSiblingCredentials(t *testing.T) {
	f := newContentFixture(t)
	queries := &viewerQueryCounter{}
	cfg := f.pool.Config()
	cfg.ConnConfig.Tracer = queries
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	provider := pgstore.NewPostgresProvider(pool)
	gate := &workerContentAuthority{}
	gate.configure(pool, access.NewResolver(auth.NewUserRepository(pool), provider, nil, access.NewGroupStore(pool)))
	corePublic, corePrivate, _ := ed25519.GenerateKey(rand.Reader)
	nodePublic, nodePrivate, _ := ed25519.GenerateKey(rand.Reader)
	node := workercontrolcore.Node{ID: 7, URL: "https://worker.example.test", PublicKey: nodePublic, Enabled: true}
	store := &viewerAssignmentCounter{MemoryStore: workercontrolcore.NewMemoryStore()}
	a, err := store.Begin(t.Context(), workerwire.Assignment{Version: 1, NodeID: node.ID, WorkID: f.card.SessionID, Path: "/transcode/start"})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Complete(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if err = store.Activate(t.Context(), node.ID, a.WorkID, a.Generation); err != nil {
		t.Fatal(err)
	}
	registry := workercontrolcore.NewMemoryOutputAuthorities()
	const secret = "viewer-batch-source-test-secret"
	logins := make([]string, 128)
	for i := range logins {
		logins[i] = uuid.NewString()
	}
	if _, err = f.pool.Exec(t.Context(), "INSERT INTO auth_sessions(id,user_id,expires_at) SELECT unnest($1::text[]),$2,NOW()+INTERVAL '1 hour'", logins, f.user); err != nil {
		t.Fatal(err)
	}
	for i, login := range logins {
		claims := f.card.ToClaims()
		claims.TranscodeNode = node.URL
		claims.OriginLoginSessionID = login
		token, err := streamtoken.SignAt(claims, secret, time.Now().Add(-time.Duration(i)*time.Second), time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		verified, err := streamtoken.Verify(token, secret)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = registry.Register(t.Context(), workercontrolcore.OutputAuthorityRecord{Token: token, NodeID: node.ID, WorkID: a.WorkID, Generation: a.Generation, Epoch: "epoch", ExpiresAt: verified.ExpiresAt.Unix()}); err != nil {
			t.Fatal(err)
		}
	}
	var executions atomic.Int64
	service, err := workercontrolcore.New(workercontrolcore.Options{Origin: "https://core.example.test", PrivateKey: corePrivate, Nodes: reportNodes{node}, Store: store, OutputAuthorities: registry, VerifyTokens: func(ctx context.Context, tokens []string) ([]workercontrolcore.TokenVerification, error) {
		return verifyWorkerOutputTokens(ctx, pool, gate, tokens, secret)
	}, VerifyToken: func(ctx context.Context, token string) (*streamtoken.Claims, error) {
		return verifyWorkerOutputToken(ctx, pool, gate, token, secret)
	}, ExecutionActive: func(context.Context, workercontrolcore.Node, workerwire.Assignment) bool {
		executions.Add(1)
		return true
	}})
	if err != nil {
		t.Fatal(err)
	}
	queries.count.Store(0)
	request := httptest.NewRequest(http.MethodPost, "https://core.example.test/api/internal/workers/7/snapshot", strings.NewReader(`{"boot_id":"batch","nonce":"snapshot-batch","sequence":0}`))
	if err = workeridentity.Sign(request, "https://core.example.test", workeridentity.DomainNodeProof, nodePrivate, node.ID, false); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("snapshot=%d %s", response.Code, response.Body.String())
	}
	var reply workercontrolcore.GrantReply
	if err = json.Unmarshal(response.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	grant, err := workeridentity.DecodeGrant(reply.Grant, workeridentity.DomainSnapshotGrant, node.URL, corePublic, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var snapshot workerwire.Snapshot
	if err = json.Unmarshal(grant.Body, &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Outputs) != 128 || snapshot.ValidForMillis != 2000 {
		t.Fatalf("viewer batch lost outputs or changed lease: outputs=%d lease=%d", len(snapshot.Outputs), snapshot.ValidForMillis)
	}
	t.Logf("128 sibling viewers: SQL=%d snapshots=%d Get=%d IsActive=%d Candidate=%d execution=%d", queries.count.Load(), store.snapshots.Load(), store.gets.Load(), store.active.Load(), store.candidates.Load(), executions.Load())
	if queries.count.Load() > 32 || store.snapshots.Load() != 1 || store.gets.Load() != 0 || store.active.Load() != 0 || store.candidates.Load() != 0 || executions.Load() != 1 {
		t.Fatal("snapshot repeats database or assignment authority reads per viewer")
	}
	pool.Close()
	requestSnapshot := func(want int) {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "https://core.example.test/api/internal/workers/7/snapshot", strings.NewReader(`{"boot_id":"batch","nonce":"snapshot-batch","sequence":0}`))
		if err := workeridentity.Sign(request, "https://core.example.test", workeridentity.DomainNodeProof, nodePrivate, node.ID, false); err != nil {
			t.Fatal(err)
		}
		out := httptest.NewRecorder()
		service.Handler().ServeHTTP(out, request)
		if out.Code != want {
			t.Fatalf("snapshot after dependency change=%d want=%d", out.Code, want)
		}
	}
	requestSnapshot(503)
	if records, err := registry.List(t.Context(), node.ID); err != nil || len(records) != 128 {
		t.Fatal("unavailable SQL deleted retained viewer credentials")
	}
	pool, gate = f.pool, f.gate
	requestSnapshot(200)
}

func viewerBatchToken(t *testing.T, claims streamtoken.Claims, secret string) string {
	t.Helper()
	token, err := streamtoken.Sign(claims, secret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestWorkerViewerBatchKeepsCredentialsIndependentAndRechecksPolicy(t *testing.T) {
	f := newContentFixture(t)
	ctx := t.Context()
	const secret = "viewer-batch-policy-secret"
	loginA, loginB, remote := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, id := range []string{loginA, loginB, remote} {
		if err := auth.NewSessionRepository(f.pool).Create(ctx, models.AuthSession{ID: id, UserID: f.user, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.pool.Exec(ctx, "UPDATE auth_sessions SET purpose='remote_playback',bound_profile_id='viewer',absolute_expires_at=NOW()+INTERVAL '1 hour' WHERE id=$1", remote); err != nil {
		t.Fatal(err)
	}
	base := f.card.ToClaims()
	base.TranscodeNode = "https://worker.example.test"
	base.OriginLoginSessionID = loginA
	a := viewerBatchToken(t, base, secret)
	base.OriginLoginSessionID = loginB
	b := viewerBatchToken(t, base, secret)
	check := func(tokens []string, want ...error) {
		t.Helper()
		results, err := verifyWorkerOutputTokens(ctx, f.pool, f.gate, tokens, secret)
		if err != nil {
			t.Fatal(err)
		}
		if len(results) != len(want) {
			t.Fatal("batch result count changed")
		}
		for i, result := range results {
			if want[i] == nil {
				if result.Err != nil || result.Claims == nil {
					t.Fatalf("valid sibling%d poisoned by another token: %v", i, result.Err)
				}
			} else if !errors.Is(result.Err, want[i]) {
				t.Fatalf("result%d err=%v want=%v", i, result.Err, want[i])
			}
		}
	}
	check([]string{a, b, "invalid-token"}, nil, nil, workercontrolcore.ErrOutputRevoked)
	if err := auth.NewSessionRepository(f.pool).Revoke(ctx, loginA); err != nil {
		t.Fatal(err)
	}
	check([]string{a, b}, workercontrolcore.ErrOutputRevoked, nil)
	key, err := auth.NewAPIKeyRepository(f.pool).Create(ctx, f.user, "viewer batch fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	api := base
	api.OriginCredential = streamtoken.OriginCredential{OriginAPIKeyID: key.ID}
	apiToken := viewerBatchToken(t, api, secret)
	check([]string{apiToken, b}, nil, nil)
	if _, err = f.pool.Exec(ctx, "UPDATE api_keys SET scopes=ARRAY['libraries:read'] WHERE id=$1", key.ID); err != nil {
		t.Fatal(err)
	}
	check([]string{apiToken, b}, workercontrolcore.ErrOutputRevoked, nil)
	if err = auth.NewAPIKeyRepository(f.pool).Delete(ctx, key.ID, f.user); err != nil {
		t.Fatal(err)
	}
	check([]string{apiToken, b}, workercontrolcore.ErrOutputRevoked, nil)
	r := base
	r.OriginLoginSessionID = remote
	remoteAllowed := viewerBatchToken(t, r, secret)
	r.ProfileID = "primary"
	remoteWrongProfile := viewerBatchToken(t, r, secret)
	r.ProfileID = "viewer"
	r.DownloadArtifactID = "prepared-artifact"
	remoteDownload := viewerBatchToken(t, r, secret)
	r.DownloadArtifactID = ""
	r.ThemeID = 1
	remoteTheme := viewerBatchToken(t, r, secret)
	check([]string{remoteAllowed, remoteWrongProfile, remoteDownload, remoteTheme, b}, nil, workercontrolcore.ErrOutputRevoked, workercontrolcore.ErrOutputRevoked, workercontrolcore.ErrOutputRevoked, nil)
	if _, err = f.pool.Exec(ctx, "UPDATE users SET transcode_allowed=FALSE WHERE id=$1", f.user); err != nil {
		t.Fatal(err)
	}
	cloned := base
	cloned.PlayMethod = "remux"
	copyToken := viewerBatchToken(t, cloned, secret)
	check([]string{copyToken, b}, nil, workercontrolcore.ErrDenied)
	check([]string{b, copyToken}, workercontrolcore.ErrDenied, nil)
	if _, err = f.pool.Exec(ctx, "UPDATE users SET transcode_allowed=TRUE WHERE id=$1", f.user); err != nil {
		t.Fatal(err)
	}
	check([]string{b}, nil)
	if _, err = f.pool.Exec(ctx, "UPDATE media_folders SET enabled=FALSE WHERE id=$1", f.folder); err != nil {
		t.Fatal(err)
	}
	check([]string{b}, workercontrolcore.ErrDenied)
	if _, err = f.pool.Exec(ctx, "UPDATE media_folders SET enabled=TRUE WHERE id=$1", f.folder); err != nil {
		t.Fatal(err)
	}
	check([]string{b}, nil)
	if err = f.store.DeleteProfile(ctx, "viewer"); err != nil {
		t.Fatal(err)
	}
	check([]string{b}, workercontrolcore.ErrDenied)
}

func TestWorkerViewerBatchKeepsPreparedAndThemeDecisionsExact(t *testing.T) {
	f := newContentFixture(t)
	ctx := t.Context()
	const secret = "viewer-batch-content-secret"
	login := uuid.NewString()
	if err := auth.NewSessionRepository(f.pool).Create(ctx, models.AuthSession{ID: login, UserID: f.user, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	prepared := *f.preparedClaims(t, "720p")
	prepared.OriginLoginSessionID = login
	ceiling := "1080p"
	if err := f.store.UpdateProfile(ctx, "viewer", userstore.UpdateProfileInput{MaxPlaybackQuality: &ceiling}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, "UPDATE media_files SET resolution='2160p' WHERE id=$1", f.file); err != nil {
		t.Fatal(err)
	}
	wrong := prepared
	wrong.DownloadExecutionFingerprint = "wrong-fingerprint"
	validToken, badToken := viewerBatchToken(t, prepared, secret), viewerBatchToken(t, wrong, secret)
	results, err := verifyWorkerOutputTokens(ctx, f.pool, f.gate, []string{validToken, badToken, validToken}, secret)
	if err != nil || results[0].Err != nil || !errors.Is(results[1].Err, workercontrolcore.ErrDenied) || results[2].Err != nil {
		t.Fatalf("prepared quality/fingerprint decisions merged or applied source quality: %v %+v", err, results)
	}
	if _, err = f.pool.Exec(ctx, "UPDATE download_artifacts SET resolution='2160p' WHERE id=$1", prepared.DownloadArtifactRowID); err != nil {
		t.Fatal(err)
	}
	results, err = verifyWorkerOutputTokens(ctx, f.pool, f.gate, []string{validToken}, secret)
	if err != nil || !errors.Is(results[0].Err, workercontrolcore.ErrDenied) {
		t.Fatal("next batch reused prior artifact quality permission")
	}
	ceiling = ""
	if err = f.store.UpdateProfile(ctx, "viewer", userstore.UpdateProfileInput{MaxPlaybackQuality: &ceiling}); err != nil {
		t.Fatal(err)
	}
	path := "/approved-fixture/" + f.content + "-theme.mp3"
	var theme int64
	if err = f.pool.QueryRow(ctx, `INSERT INTO item_theme_songs(media_folder_id,owner_path,file_path,title,duration_seconds,container,audio_codec,audio_channels,bitrate_kbps,sample_rate,file_size,file_modified_at) VALUES($1,'/approved-fixture',$2,'Fixture theme',1,'mp3','mp3',2,128,48000,10,NOW()) RETURNING id`, f.folder, path).Scan(&theme); err != nil {
		t.Fatal(err)
	}
	themeClaims := f.card.ToClaims()
	themeClaims.OriginLoginSessionID = login
	themeClaims.TranscodeNode = prepared.TranscodeNode
	themeClaims.MediaFileID = 0
	themeClaims.MediaPath = path
	themeClaims.ThemeID = theme
	themeClaims.PlayMethod = streamtoken.PlayMethodThemeDirect
	missing := themeClaims
	missing.MediaPath += "-missing"
	results, err = verifyWorkerOutputTokens(ctx, f.pool, f.gate, []string{viewerBatchToken(t, themeClaims, secret), viewerBatchToken(t, missing, secret)}, secret)
	if err != nil || results[0].Err != nil || !errors.Is(results[1].Err, workercontrolcore.ErrDenied) {
		t.Fatalf("theme access or exact path changed: %v %+v", err, results)
	}
}

type viewerDegradedResolver struct{ workerScopeResolver }

func (r viewerDegradedResolver) Resolve(ctx context.Context, input access.ResolveInput) (access.Scope, error) {
	scope, err := r.workerScopeResolver.Resolve(ctx, input)
	scope.PreferencesDegraded = true
	return scope, err
}

func TestWorkerViewerBatchUnknownContentAndCancellationDoNotBecomeRevocation(t *testing.T) {
	f := newContentFixture(t)
	const secret = "viewer-batch-unknown-secret"
	login := uuid.NewString()
	if err := auth.NewSessionRepository(f.pool).Create(t.Context(), models.AuthSession{ID: login, UserID: f.user, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	claims := f.card.ToClaims()
	claims.OriginLoginSessionID = login
	token := viewerBatchToken(t, claims, secret)
	state := f.gate.state.Load()
	degraded := *state
	degraded.resolver = viewerDegradedResolver{state.resolver}
	f.gate.state.Store(&degraded)
	if _, err := verifyWorkerOutputTokens(t.Context(), f.pool, f.gate, []string{token}, secret); err == nil || errors.Is(err, workercontrolcore.ErrOutputRevoked) {
		t.Fatal("unknown preferences produced a new permission or destructive source revoke")
	}
	f.gate.state.Store(state)
	if results, err := verifyWorkerOutputTokens(t.Context(), f.pool, f.gate, []string{token}, secret); err != nil || results[0].Err != nil {
		t.Fatal("fresh restored content did not recover")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := verifyWorkerOutputTokens(ctx, f.pool, f.gate, []string{token}, secret); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled batch did not preserve cancellation: %v", err)
	}
}
