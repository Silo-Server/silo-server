package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodehttp"
	"github.com/Silo-Server/silo-server/internal/noderecipe"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/themesongs"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/Silo-Server/silo-server/internal/workercontrolcore"
	"github.com/Silo-Server/silo-server/internal/workeridentity"
	"github.com/Silo-Server/silo-server/internal/workerregistration"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

// This fixture uses the actual composition root: no execution or viewer
// authorization callback is replaced by a counting stub.
type productionSnapshotFixture struct {
	*contentFixture
	pool       *pgxpool.Pool
	queries    *productionSnapshotQueryCounter
	rdb        *redis.Client
	store      *workercontrolcore.RedisStore
	recipes    *noderecipe.Store
	outputs    *workercontrolcore.RedisOutputAuthorities
	handler    http.Handler
	node       workercontrolcore.Node
	nodeKey    ed25519.PrivateKey
	corePublic ed25519.PublicKey
	secret     string
}

type productionSnapshotCountKey struct{}
type productionSnapshotQueryCounter struct{ count atomic.Int64 }

func (q *productionSnapshotQueryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	// Include every SQL read caused by the real HTTP request, while the
	// registry's independent background refresh cannot distort the budget.
	if ctx.Value(productionSnapshotCountKey{}) != nil {
		q.count.Add(1)
	}
	return ctx
}

func (*productionSnapshotQueryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {
}

func newProductionSnapshotFixture(t *testing.T) *productionSnapshotFixture {
	t.Helper()
	f := &productionSnapshotFixture{contentFixture: newContentFixture(t), secret: "production-snapshot-fixture-secret"}
	raw := os.Getenv("SILO_TEST_REDIS_URL")
	if raw == "" {
		t.Skip("SILO_TEST_REDIS_URL is not set")
	}
	opts, err := redis.ParseURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	f.rdb = redis.NewClient(opts)
	t.Cleanup(func() { _ = f.rdb.Close() })
	// Isolate the protected signing key without replacing any production table
	// used by the account, profile, catalog, registry or download checks.
	schema := "snapshot_keys_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = f.contentFixture.pool.Exec(t.Context(), "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.contentFixture.pool.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE") })
	if _, err = f.contentFixture.pool.Exec(t.Context(), "CREATE TABLE "+quoted+".worker_http_keys (LIKE public.worker_http_keys INCLUDING ALL)"); err != nil {
		t.Fatal(err)
	}
	f.queries = &productionSnapshotQueryCounter{}
	poolConfig := f.contentFixture.pool.Config()
	poolConfig.ConnConfig.Tracer = f.queries
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	f.pool, err = pgxpool.NewWithConfig(t.Context(), poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.pool.Close)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.nodeKey = private
	f.node = workercontrolcore.Node{URL: "https://" + uuid.NewString() + ".example.test", PublicKey: public, Enabled: true}
	if err = f.pool.QueryRow(t.Context(), "INSERT INTO stream_nodes(url,name,type,enabled) VALUES($1,'Production snapshot fixture','transcode',TRUE) RETURNING id", f.node.URL).Scan(&f.node.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.contentFixture.pool.Exec(context.Background(), "DELETE FROM stream_nodes WHERE id=$1", f.node.ID)
	})
	if _, err = f.pool.Exec(t.Context(), "INSERT INTO worker_http_nodes(node_id,public_key,allow_http) VALUES($1,$2,FALSE)", f.node.ID, []byte(public)); err != nil {
		t.Fatal(err)
	}
	cipher, err := secret.New(bytes.Repeat([]byte{0x53}, secret.MinMasterKeyLen))
	if err != nil {
		t.Fatal(err)
	}
	coreKey, err := workerregistration.New(f.pool, cipher).SigningKey(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	f.corePublic = coreKey.Public().(ed25519.PublicKey)
	gate := &workerContentAuthority{}
	gate.configure(f.pool, access.NewResolver(auth.NewUserRepository(f.pool), pgstore.NewPostgresProvider(f.pool), nil, access.NewGroupStore(f.pool)))
	f.gate = gate
	cfg := &config.Config{}
	cfg.Server.PublicURL = "https://core.example.test"
	cfg.Auth.JWTSecret = f.secret
	cfg.Playback.SegmentRetentionSeconds = 600
	cfg.Playback.SubtitleSyncNodeCapacity = 1
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	f.handler, err = initializeHTTPWorkers(ctx, f.pool, cipher, f.rdb, cfg, gate)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { nodehttp.Configure(nil) })
	f.store = workercontrolcore.NewRedisStore(f.rdb, "")
	f.recipes = noderecipe.NewStore(f.rdb, time.Hour)
	f.outputs = workercontrolcore.NewRedisOutputAuthorities(f.rdb, "")
	t.Cleanup(func() {
		records, _ := f.outputs.List(context.Background(), f.node.ID)
		for _, record := range records {
			_ = f.outputs.Delete(context.Background(), record)
		}
	})
	return f
}

func (f *productionSnapshotFixture) viewer(t *testing.T, card playback.RecipeCard, login string) string {
	t.Helper()
	work := uuid.NewString()
	card.SessionID, card.TranscodeTransportID = work, work
	card.TranscodeNodeURL, card.OriginLoginSessionID = f.node.URL, login
	a, err := f.store.Begin(t.Context(), workerwire.Assignment{Version: 1, NodeID: f.node.ID, WorkID: work, Path: "/transcode/start"})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.Complete(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if err = f.store.Activate(t.Context(), f.node.ID, work, a.Generation); err != nil {
		t.Fatal(err)
	}
	if err = f.recipes.Put(t.Context(), work, card); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = f.recipes.Delete(context.Background(), work)
		_ = f.store.Stop(context.Background(), f.node.ID, work)
	})
	token := viewerBatchToken(t, card.ToClaims(), f.secret)
	verified, err := streamtoken.Verify(token, f.secret)
	if err != nil {
		t.Fatal(err)
	}
	id, err := f.outputs.Register(t.Context(), workercontrolcore.OutputAuthorityRecord{Token: token, NodeID: f.node.ID, WorkID: work, Generation: a.Generation, Epoch: "fixture", ExpiresAt: verified.ExpiresAt.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestWorkerProductionSnapshotScopesAndUnknownRecovery(t *testing.T) {
	f := newProductionSnapshotFixture(t)
	login := uuid.NewString()
	if _, err := f.pool.Exec(t.Context(), "INSERT INTO auth_sessions(id,user_id,expires_at) VALUES($1,$2,NOW()+INTERVAL '1 hour')", login, f.user); err != nil {
		t.Fatal(err)
	}
	viewer := f.viewer(t, *f.card, login)
	primaryCard := *f.card
	primaryCard.ProfileID = "primary"
	primary := f.viewer(t, primaryCard, login)
	if snapshot := f.snapshot(t, http.StatusOK); len(snapshot.Active) != 2 || len(snapshot.Outputs) != 2 {
		t.Fatal("initial exact scopes were not authorized")
	}
	restricted, empty := true, []int{}
	if err := f.contentFixture.store.UpdateProfile(t.Context(), "viewer", userstore.UpdateProfileInput{LibraryRestrictionsEnabled: &restricted, AllowedLibraryIDs: &empty}); err != nil {
		t.Fatal(err)
	}
	if snapshot := f.snapshot(t, http.StatusOK); len(snapshot.Active) != 1 || snapshot.Outputs[viewer] || !snapshot.Outputs[primary] {
		t.Fatal("snapshot facts crossed profile scope or reused the preceding request")
	}
	before, err := f.outputs.List(t.Context(), f.node.ID)
	if err != nil || len(before) != 2 {
		t.Fatal("content denial destructively retired recoverable viewer records")
	}
	state := f.gate.state.Load()
	degraded := *state
	degraded.resolver = viewerDegradedResolver{state.resolver}
	f.gate.state.Store(&degraded)
	f.snapshot(t, http.StatusServiceUnavailable)
	after, err := f.outputs.List(t.Context(), f.node.ID)
	beforeJSON, _ := json.Marshal(before)
	afterJSON, _ := json.Marshal(after)
	if err != nil || !bytes.Equal(beforeJSON, afterJSON) {
		t.Fatal("unknown content read changed retained viewer credentials")
	}
	f.gate.state.Store(state)
	closed, err := pgxpool.NewWithConfig(t.Context(), f.pool.Config())
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	unavailable := *state
	unavailable.files = scanner.NewFileRepository(closed)
	f.gate.state.Store(&unavailable)
	f.snapshot(t, http.StatusServiceUnavailable)
	after, err = f.outputs.List(t.Context(), f.node.ID)
	afterJSON, _ = json.Marshal(after)
	if err != nil || !bytes.Equal(beforeJSON, afterJSON) {
		t.Fatal("unavailable SQL reader changed retained viewer credentials")
	}
	f.gate.state.Store(state)
	restricted = false
	if err = f.contentFixture.store.UpdateProfile(t.Context(), "viewer", userstore.UpdateProfileInput{LibraryRestrictionsEnabled: &restricted}); err != nil {
		t.Fatal(err)
	}
	if snapshot := f.snapshot(t, http.StatusOK); len(snapshot.Active) != 2 || !snapshot.Outputs[viewer] || !snapshot.Outputs[primary] {
		t.Fatal("fresh production facts did not recover after restored scope/dependency")
	}
}

func TestWorkerProductionSnapshotRetargetDoesNotFollowAnotherFilesPath(t *testing.T) {
	f := newProductionSnapshotFixture(t)
	login := uuid.NewString()
	if _, err := f.pool.Exec(t.Context(), "INSERT INTO auth_sessions(id,user_id,expires_at) VALUES($1,$2,NOW()+INTERVAL '1 hour')", login, f.user); err != nil {
		t.Fatal(err)
	}
	id := f.viewer(t, *f.card, login)
	if snapshot := f.snapshot(t, http.StatusOK); len(snapshot.Active) != 1 || !snapshot.Outputs[id] {
		t.Fatal("initial source path was denied")
	}
	if _, err := f.pool.Exec(t.Context(), "UPDATE media_files SET file_path=file_path||'-retarget' WHERE id=$1", f.file); err != nil {
		t.Fatal(err)
	}
	var shadow int
	if err := f.pool.QueryRow(t.Context(), "INSERT INTO media_files(content_id,media_folder_id,file_path,file_size,resolution) VALUES($1,$2,$3,10,'1080p') RETURNING id", f.content, f.folder, f.card.InputPath).Scan(&shadow); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.contentFixture.pool.Exec(context.Background(), "DELETE FROM media_files WHERE id=$1", shadow)
	})
	if snapshot := f.snapshot(t, http.StatusOK); len(snapshot.Active) != 0 || snapshot.Outputs[id] {
		t.Fatal("retargeted file borrowed another catalog row's old path authorization")
	}
	if records, err := f.outputs.List(t.Context(), f.node.ID); err != nil || len(records) != 1 {
		t.Fatal("retarget denial destroyed a recoverable viewer record")
	}
	if _, err := f.pool.Exec(t.Context(), "DELETE FROM media_files WHERE id=$1", shadow); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), "UPDATE media_files SET file_path=$2 WHERE id=$1", f.file, f.card.InputPath); err != nil {
		t.Fatal(err)
	}
	if snapshot := f.snapshot(t, http.StatusOK); len(snapshot.Active) != 1 || !snapshot.Outputs[id] {
		t.Fatal("restored exact source identity did not recover")
	}
}

func TestWorkerProductionThemeSnapshotKeepsDenialAndUnknownDistinct(t *testing.T) {
	f := newProductionSnapshotFixture(t)
	login := uuid.NewString()
	if _, err := f.pool.Exec(t.Context(), "INSERT INTO auth_sessions(id,user_id,expires_at) VALUES($1,$2,NOW()+INTERVAL '1 hour')", login, f.user); err != nil {
		t.Fatal(err)
	}
	path := "/approved-fixture/" + f.content + "-theme.mp3"
	var theme int64
	if err := f.pool.QueryRow(t.Context(), `INSERT INTO item_theme_songs(media_folder_id,owner_path,file_path,title,duration_seconds,container,audio_codec,audio_channels,bitrate_kbps,sample_rate,file_size,file_modified_at) VALUES($1,'/approved-fixture',$2,'Snapshot theme',1,'mp3','mp3',2,128,48000,10,NOW()) RETURNING id`, f.folder, path).Scan(&theme); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.contentFixture.pool.Exec(context.Background(), "DELETE FROM item_theme_songs WHERE id=$1", theme)
	})
	card := *f.card
	card.MediaFileID, card.InputPath = 0, path
	card.PlayMethod = playback.PlayMethod(streamtoken.PlayMethodThemeDirect)
	id := f.viewer(t, card, login)
	if snapshot := f.snapshot(t, http.StatusOK); len(snapshot.Active) != 1 || !snapshot.Outputs[id] {
		t.Fatal("valid theme ownership was denied")
	}
	closed, err := pgxpool.NewWithConfig(t.Context(), f.pool.Config())
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	state := f.gate.state.Load()
	unavailable := *state
	unavailable.themes = themesongs.NewRepository(closed)
	f.gate.state.Store(&unavailable)
	f.snapshot(t, http.StatusServiceUnavailable)
	if records, err := f.outputs.List(t.Context(), f.node.ID); err != nil || len(records) != 1 {
		t.Fatal("unknown theme ownership read deleted a retained viewer")
	}
	f.gate.state.Store(state)
	if _, err = f.pool.Exec(t.Context(), "UPDATE item_theme_songs SET file_path=file_path||'-retarget' WHERE id=$1", theme); err != nil {
		t.Fatal(err)
	}
	if snapshot := f.snapshot(t, http.StatusOK); len(snapshot.Active) != 0 || snapshot.Outputs[id] {
		t.Fatal("missing exact theme path retained permission")
	}
	if _, err = f.pool.Exec(t.Context(), "UPDATE item_theme_songs SET file_path=$2 WHERE id=$1", theme, path); err != nil {
		t.Fatal(err)
	}
	if snapshot := f.snapshot(t, http.StatusOK); len(snapshot.Active) != 1 || !snapshot.Outputs[id] {
		t.Fatal("restored theme ownership did not recover")
	}
}

func (f *productionSnapshotFixture) snapshot(t *testing.T, wantStatus int) workerwire.Snapshot {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("https://core.example.test/api/internal/workers/%d/snapshot", f.node.ID), strings.NewReader(`{"boot_id":"production","nonce":"`+uuid.NewString()+`","sequence":0}`))
	request = request.WithContext(context.WithValue(request.Context(), productionSnapshotCountKey{}, true))
	if err := workeridentity.Sign(request, "https://core.example.test", workeridentity.DomainNodeProof, f.nodeKey, f.node.ID, false); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("production snapshot=%d want=%d: %s", response.Code, wantStatus, response.Body.String())
	}
	if wantStatus != http.StatusOK {
		if strings.Contains(response.Body.String(), `"grant"`) {
			t.Fatal("failed snapshot returned a partial signed authorization")
		}
		return workerwire.Snapshot{}
	}
	var reply workercontrolcore.GrantReply
	if err := json.Unmarshal(response.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	grant, err := workeridentity.DecodeGrant(reply.Grant, workeridentity.DomainSnapshotGrant, f.node.URL, f.corePublic, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var snapshot workerwire.Snapshot
	if err = json.Unmarshal(grant.Body, &snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestWorkerProductionSnapshotQueryBudget(t *testing.T) {
	f := newProductionSnapshotFixture(t)
	login := uuid.NewString()
	if _, err := f.pool.Exec(t.Context(), "INSERT INTO auth_sessions(id,user_id,expires_at) VALUES($1,$2,NOW()+INTERVAL '1 hour')", login, f.user); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 128; i++ {
		content := f.content + fmt.Sprintf("-%d", i)
		var file int
		if _, err := f.pool.Exec(t.Context(), "INSERT INTO media_items(content_id,type,title,content_rating) VALUES($1,'movie','Distinct snapshot media','PG-13')", content); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(t.Context(), "INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)", content, f.folder); err != nil {
			t.Fatal(err)
		}
		path := "/approved-fixture/" + content + ".mkv"
		if err := f.pool.QueryRow(t.Context(), "INSERT INTO media_files(content_id,media_folder_id,file_path,file_size,resolution) VALUES($1,$2,$3,10,'1080p') RETURNING id", content, f.folder, path).Scan(&file); err != nil {
			t.Fatal(err)
		}
		work := uuid.NewString()
		t.Cleanup(func() {
			_ = f.recipes.Delete(context.Background(), work)
			_ = f.store.Stop(context.Background(), f.node.ID, work)
			_, _ = f.contentFixture.pool.Exec(context.Background(), "DELETE FROM media_files WHERE id=$1", file)
			_, _ = f.contentFixture.pool.Exec(context.Background(), "DELETE FROM media_items WHERE content_id=$1", content)
		})
		a, err := f.store.Begin(t.Context(), workerwire.Assignment{Version: 1, NodeID: f.node.ID, WorkID: work, Path: "/transcode/start"})
		if err != nil {
			t.Fatal(err)
		}
		if err = f.store.Complete(t.Context(), a); err != nil {
			t.Fatal(err)
		}
		if err = f.store.Activate(t.Context(), f.node.ID, work, a.Generation); err != nil {
			t.Fatal(err)
		}
		card := *f.card
		card.SessionID, card.TranscodeTransportID = work, work
		card.MediaFileID, card.InputPath, card.TranscodeNodeURL = file, path, f.node.URL
		card.OriginLoginSessionID = login
		if err = f.recipes.Put(t.Context(), work, card); err != nil {
			t.Fatal(err)
		}
		claims := card.ToClaims()
		token := viewerBatchToken(t, claims, f.secret)
		verified, err := streamtoken.Verify(token, f.secret)
		if err != nil {
			t.Fatal(err)
		}
		id, err := f.outputs.Register(t.Context(), workercontrolcore.OutputAuthorityRecord{Token: token, NodeID: f.node.ID, WorkID: work, Generation: a.Generation, Epoch: "fixture", ExpiresAt: verified.ExpiresAt.Unix()})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			records, _ := f.outputs.List(context.Background(), f.node.ID)
			for _, record := range records {
				if record.ID == id {
					_ = f.outputs.Delete(context.Background(), record)
				}
			}
		})
		if i != 1 && i != 32 && i != 128 {
			continue
		}
		f.queries.count.Store(0)
		started := time.Now()
		snapshot := f.snapshot(t, http.StatusOK)
		count := f.queries.count.Load()
		t.Logf("production snapshot distinct works/media=%d viewers=%d SQL=%d elapsed=%s", i, len(snapshot.Outputs), count, time.Since(started))
		if len(snapshot.Active) != i || len(snapshot.Outputs) != i || snapshot.ValidForMillis != 2000 {
			t.Fatalf("production authorization changed: active=%d outputs=%d lease=%d", len(snapshot.Active), len(snapshot.Outputs), snapshot.ValidForMillis)
		}
		if count > 32 {
			t.Fatalf("production snapshot SQL grows with active media: %d queries for %d distinct works", count, i)
		}
	}
}

//nolint:funlen // Keep the production budget and artifact failure/recovery sequence together.
func TestWorkerProductionPreparedSnapshotQueryBudget(t *testing.T) {
	f := newProductionSnapshotFixture(t)
	login := uuid.NewString()
	if _, err := f.pool.Exec(t.Context(), "INSERT INTO auth_sessions(id,user_id,expires_at) VALUES($1,$2,NOW()+INTERVAL '1 hour')", login, f.user); err != nil {
		t.Fatal(err)
	}
	ceiling := "1080p"
	if err := f.contentFixture.store.UpdateProfile(t.Context(), "viewer", userstore.UpdateProfileInput{MaxPlaybackQuality: &ceiling}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(t.Context(), "UPDATE media_files SET resolution='2160p',missing_since=NOW() WHERE id=$1", f.file); err != nil {
		t.Fatal(err)
	}
	var artifactToCorrupt string
	for i := 0; i < 128; i++ {
		artifact, work := uuid.NewString(), "prepared-"+uuid.NewString()
		artifactToCorrupt = artifact
		a, err := f.store.Begin(t.Context(), workerwire.Assignment{Version: 1, NodeID: f.node.ID, WorkID: work, Path: workerPreparePath})
		if err != nil {
			t.Fatal(err)
		}
		if err = f.store.Complete(t.Context(), a); err != nil {
			t.Fatal(err)
		}
		if err = f.store.Activate(t.Context(), f.node.ID, work, a.Generation); err != nil {
			t.Fatal(err)
		}
		if _, err = f.pool.Exec(t.Context(), `INSERT INTO download_artifacts(id,media_file_id,format,params_hash,status,resolution,attempts,origin_node_id,origin_node_url,origin_artifact_id) VALUES($1,$2,'transcode',$1,'ready','720p',1,$3,$4,$5)`, artifact, f.file, f.node.ID, f.node.URL, work); err != nil {
			t.Fatal(err)
		}
		if _, err = f.pool.Exec(t.Context(), "INSERT INTO worker_http_download_attempts(node_id,work_id,generation,artifact_id,lease_owner,attempt) VALUES($1,$2,$3,$4,'fixture',1)", f.node.ID, work, a.Generation, artifact); err != nil {
			t.Fatal(err)
		}
		claims := streamtoken.Claims{UserID: f.user, ProfileID: "viewer", MediaFileID: f.file, DownloadArtifactID: work, DownloadArtifactRowID: artifact, DownloadExecutionFingerprint: artifact, TranscodeNode: f.node.URL, RoutingExecutionNodeID: f.node.ID, PlayMethod: streamtoken.PlayMethodDownload}
		claims.OriginLoginSessionID = login
		token := viewerBatchToken(t, claims, f.secret)
		verified, err := streamtoken.Verify(token, f.secret)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.outputs.Register(t.Context(), workercontrolcore.OutputAuthorityRecord{Token: token, NodeID: f.node.ID, WorkID: work, Generation: a.Generation, Epoch: "fixture", ExpiresAt: verified.ExpiresAt.Unix()}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = f.store.Stop(context.Background(), f.node.ID, work)
			_, _ = f.contentFixture.pool.Exec(context.Background(), "DELETE FROM worker_http_download_attempts WHERE node_id=$1 AND work_id=$2", f.node.ID, work)
			_, _ = f.contentFixture.pool.Exec(context.Background(), "DELETE FROM download_artifacts WHERE id=$1", artifact)
		})
	}
	f.queries.count.Store(0)
	started := time.Now()
	snapshot := f.snapshot(t, http.StatusOK)
	count := f.queries.count.Load()
	t.Logf("production prepared snapshot distinct artifacts=128 SQL=%d elapsed=%s", count, time.Since(started))
	if len(snapshot.Active) != 128 || len(snapshot.Outputs) != 128 || snapshot.ValidForMillis != 2000 {
		t.Fatalf("prepared snapshot lost artifact authority: active=%d outputs=%d lease=%d", len(snapshot.Active), len(snapshot.Outputs), snapshot.ValidForMillis)
	}
	if count > 32 {
		t.Fatalf("production prepared SQL grows with artifacts: %d queries", count)
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE download_artifacts SET prepared_audio_tracks='{}'::jsonb WHERE id=$1`, artifactToCorrupt); err != nil {
		t.Fatal(err)
	}
	f.snapshot(t, http.StatusServiceUnavailable)
	if records, err := f.outputs.List(t.Context(), f.node.ID); err != nil || len(records) != 128 {
		t.Fatal("unknown artifact row decode retired retained viewers")
	}
	if _, err := f.pool.Exec(t.Context(), `UPDATE download_artifacts SET prepared_audio_tracks='[]'::jsonb WHERE id=$1`, artifactToCorrupt); err != nil {
		t.Fatal(err)
	}
	if restored := f.snapshot(t, http.StatusOK); len(restored.Outputs) != 128 {
		t.Fatal("artifact row decode recovery did not restore the whole snapshot")
	}
	assignments, err := f.store.Snapshot(t.Context(), f.node.ID)
	if err != nil {
		t.Fatal(err)
	}
	var batch []workerwire.Assignment
	for _, assignment := range assignments.Active {
		batch = append(batch, assignment)
	}
	// Prepared execution has its own durable attempt authority. It must not
	// require account/content state merely to keep an accepted artifact active.
	execution := workerExecutionAuthority{pool: f.pool}
	allowed, err := execution.statuses(t.Context(), f.node, batch)
	if err != nil {
		t.Fatalf("prepared-only execution required a content gate: %v", err)
	}
	for _, active := range allowed {
		if !active {
			t.Fatal("prepared-only execution lost its durable attempt authority")
		}
	}
}
