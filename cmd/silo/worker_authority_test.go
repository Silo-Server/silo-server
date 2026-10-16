package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/Silo-Server/silo-server/internal/workercontrolcore"
	"github.com/Silo-Server/silo-server/internal/workerwire"
)

type contentFixture struct {
	pool               *pgxpool.Pool
	user, file, folder int
	content            string
	store              userstore.UserStore
	gate               *workerContentAuthority
	card               *playback.RecipeCard
}

func (f *contentFixture) preparedClaims(t *testing.T, resolution string) *streamtoken.Claims {
	t.Helper()
	artifact := uuid.NewString()
	work := "prepared-" + artifact
	origin := "http://" + f.content + ".example.test"
	var node int
	if err := f.pool.QueryRow(t.Context(), "INSERT INTO stream_nodes(name,url,type,enabled) VALUES($1,$2,'transcode',TRUE) RETURNING id", artifact, origin).Scan(&node); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), "DELETE FROM download_artifacts WHERE id=$1", artifact)
		_, _ = f.pool.Exec(context.Background(), "DELETE FROM stream_nodes WHERE id=$1", node)
	})
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO download_artifacts
		(id,media_file_id,format,params_hash,status,resolution,origin_node_id,origin_node_url,origin_artifact_id)
		VALUES($1,$2,'transcode',$1,'ready',$3,$4,$5,$6)`, artifact, f.file, resolution, node, origin, work); err != nil {
		t.Fatal(err)
	}
	return &streamtoken.Claims{UserID: f.user, ProfileID: "viewer", MediaFileID: f.file,
		DownloadArtifactID: work, DownloadArtifactRowID: artifact, DownloadExecutionFingerprint: artifact,
		TranscodeNode: origin, RoutingExecutionNodeID: node, PlayMethod: streamtoken.PlayMethodDownload}
}

func newContentFixture(t *testing.T) *contentFixture {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	f := &contentFixture{pool: pool, content: "worker-authority-" + uuid.NewString()}
	if err := pool.QueryRow(t.Context(), "INSERT INTO users(username,email,password_hash,role,enabled) VALUES($1,$2,'fixture-only','user',TRUE) RETURNING id", f.content, f.content+"@example.test").Scan(&f.user); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), "INSERT INTO media_folders(type,name,enabled) VALUES('movies',$1,TRUE) RETURNING id", f.content).Scan(&f.folder); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), "INSERT INTO media_items(content_id,type,title,content_rating) VALUES($1,'movie','Worker authorization fixture','PG-13')", f.content); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), "INSERT INTO media_item_libraries(content_id,media_folder_id) VALUES($1,$2)", f.content, f.folder); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), "INSERT INTO media_files(content_id,media_folder_id,file_path,file_size,resolution) VALUES($1,$2,$3,10,'1080p') RETURNING id", f.content, f.folder, "/approved-fixture/"+f.content+".mkv").Scan(&f.file); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, "DELETE FROM download_artifacts WHERE media_file_id=$1", f.file)
		_, _ = pool.Exec(ctx, "DELETE FROM media_files WHERE id=$1", f.file)
		_, _ = pool.Exec(ctx, "DELETE FROM media_items WHERE content_id=$1", f.content)
		_, _ = pool.Exec(ctx, "DELETE FROM media_folders WHERE id=$1", f.folder)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id=$1", f.user)
	})
	provider := pgstore.NewPostgresProvider(pool)
	f.store, err = provider.ForUser(t.Context(), f.user)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"primary", "viewer"} {
		if err := f.store.CreateProfile(t.Context(), userstore.Profile{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	f.gate = &workerContentAuthority{}
	f.gate.configure(pool, access.NewResolver(auth.NewUserRepository(pool), provider, nil, access.NewGroupStore(pool)))
	f.card = &playback.RecipeCard{SessionID: "viewer-session", UserID: f.user, ProfileID: "viewer", MediaFileID: f.file, InputPath: "/approved-fixture/" + f.content + ".mkv"}
	if !f.gate.allowed(t.Context(), f.card) {
		t.Fatal("current account/profile/library fixture denied initial recipe")
	}
	return f
}

func TestWorkerContentAuthorityRevokesAccountProfileLibraryAndMediaFolder(t *testing.T) {
	f := newContentFixture(t)
	ctx := t.Context()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	denied := func(reason string) {
		t.Helper()
		if f.gate.allowed(ctx, f.card) {
			t.Fatalf("durable recipe survived %s revocation", reason)
		}
	}
	allowed := func() {
		t.Helper()
		if !f.gate.allowed(ctx, f.card) {
			t.Fatal("restored live access was refused")
		}
	}
	exec("UPDATE users SET enabled=FALSE WHERE id=$1", f.user)
	denied("account")
	exec("UPDATE users SET enabled=TRUE WHERE id=$1", f.user)
	allowed()
	if err := f.store.DeleteProfile(ctx, "viewer"); err != nil {
		t.Fatal(err)
	}
	denied("deleted profile")
	if err := f.store.CreateProfile(ctx, userstore.Profile{ID: "viewer", Name: "viewer"}); err != nil {
		t.Fatal(err)
	}
	allowed()
	restricted := true
	empty := []int{}
	if err := f.store.UpdateProfile(ctx, "viewer", userstore.UpdateProfileInput{LibraryRestrictionsEnabled: &restricted, AllowedLibraryIDs: &empty}); err != nil {
		t.Fatal(err)
	}
	denied("profile library")
	restricted = false
	if err := f.store.UpdateProfile(ctx, "viewer", userstore.UpdateProfileInput{LibraryRestrictionsEnabled: &restricted}); err != nil {
		t.Fatal(err)
	}
	allowed()
	exec("UPDATE media_folders SET enabled=FALSE WHERE id=$1", f.folder)
	denied("media folder")
	exec("UPDATE media_folders SET enabled=TRUE WHERE id=$1", f.folder)
	allowed()
	rating := "G"
	if err := f.store.UpdateProfile(ctx, "viewer", userstore.UpdateProfileInput{MaxContentRating: &rating}); err != nil {
		t.Fatal(err)
	}
	denied("profile rating")
	rating = ""
	if err := f.store.UpdateProfile(ctx, "viewer", userstore.UpdateProfileInput{MaxContentRating: &rating}); err != nil {
		t.Fatal(err)
	}
	allowed()
	exec("UPDATE media_files SET missing_since=now() WHERE id=$1", f.file)
	denied("source disappearance")
	claims := f.preparedClaims(t, "1080p")
	if !f.gate.allowedClaims(ctx, claims) {
		t.Fatal("ready artifact needlessly required original source bytes")
	}
	exec("UPDATE users SET download_allowed=FALSE WHERE id=$1", f.user)
	if f.gate.allowedClaims(ctx, claims) {
		t.Fatal("prepared authority survived download permission revoke")
	}
}

func TestWorkerPreparedOutputUsesAuthoritativeArtifactQuality(t *testing.T) {
	f := newContentFixture(t)
	ctx := t.Context()
	if _, err := f.pool.Exec(ctx, "UPDATE media_files SET resolution='2160p' WHERE id=$1", f.file); err != nil {
		t.Fatal(err)
	}
	ceiling := "1080p"
	if err := f.store.UpdateProfile(ctx, "viewer", userstore.UpdateProfileInput{MaxPlaybackQuality: &ceiling}); err != nil {
		t.Fatal(err)
	}
	claims := f.preparedClaims(t, "720p")
	if !f.gate.allowedClaims(ctx, claims) {
		t.Fatal("permitted 720p artifact was checked against its 2160p source")
	}
	if _, err := f.pool.Exec(ctx, "UPDATE download_artifacts SET resolution='2160p' WHERE id=$1", claims.DownloadArtifactRowID); err != nil {
		t.Fatal(err)
	}
	if f.gate.allowedClaims(ctx, claims) {
		t.Fatal("authoritative artifact quality change did not revoke access")
	}
	if _, err := f.pool.Exec(ctx, "UPDATE download_artifacts SET resolution='' WHERE id=$1", claims.DownloadArtifactRowID); err != nil {
		t.Fatal(err)
	}
	if f.gate.allowedClaims(ctx, claims) {
		t.Fatal("unknown artifact quality did not fall back to the source quality")
	}
	if _, err := f.pool.Exec(ctx, "UPDATE download_artifacts SET resolution='720p' WHERE id=$1", claims.DownloadArtifactRowID); err != nil {
		t.Fatal(err)
	}
	restricted := true
	empty := []int{}
	if err := f.store.UpdateProfile(ctx, "viewer", userstore.UpdateProfileInput{LibraryRestrictionsEnabled: &restricted, AllowedLibraryIDs: &empty}); err != nil {
		t.Fatal(err)
	}
	if f.gate.allowedClaims(ctx, claims) {
		t.Fatal("prepared quality handling bypassed the profile's library restrictions")
	}
}

func TestWorkerPreparedOutputRequiresMatchingReadyArtifact(t *testing.T) {
	f := newContentFixture(t)
	claims := f.preparedClaims(t, "720p")
	alias := *claims
	alias.TranscodeNode = "http://" + strings.ToUpper(strings.TrimPrefix(claims.TranscodeNode, "http://")) + ":80/"
	if !f.gate.allowedClaims(t.Context(), &alias) {
		t.Fatal("equivalent canonical artifact origin was rejected")
	}
	for _, status := range []string{"ready", "tone_map_ready", "audio_v2_ready", "tracks_v1_ready"} {
		if _, err := f.pool.Exec(t.Context(), "UPDATE download_artifacts SET status=$2 WHERE id=$1", claims.DownloadArtifactRowID, status); err != nil {
			t.Fatal(err)
		}
		if !f.gate.allowedClaims(t.Context(), claims) {
			t.Fatalf("ready artifact status %s was rejected", status)
		}
	}
	for _, change := range []struct {
		name string
		edit func(*streamtoken.Claims)
	}{
		{"missing row", func(c *streamtoken.Claims) { c.DownloadArtifactRowID = "" }},
		{"unknown row", func(c *streamtoken.Claims) { c.DownloadArtifactRowID = uuid.NewString() }},
		{"wrong work", func(c *streamtoken.Claims) { c.DownloadArtifactID = "another-artifact" }},
		{"wrong origin", func(c *streamtoken.Claims) { c.TranscodeNode = "https://another.example.test" }},
		{"wrong node", func(c *streamtoken.Claims) { c.RoutingExecutionNodeID++ }},
		{"wrong fingerprint", func(c *streamtoken.Claims) { c.DownloadExecutionFingerprint = "another-fingerprint" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			candidate := *claims
			change.edit(&candidate)
			if f.gate.allowedClaims(t.Context(), &candidate) {
				t.Fatal("prepared output accepted a mismatched artifact")
			}
		})
	}
	if _, err := f.pool.Exec(t.Context(), "UPDATE download_artifacts SET status='queued' WHERE id=$1", claims.DownloadArtifactRowID); err != nil {
		t.Fatal(err)
	}
	if f.gate.allowedClaims(t.Context(), claims) {
		t.Fatal("prepared output accepted an artifact that is no longer ready")
	}
}

func TestWorkerDownloadAttemptRetryFencesSameLeaseOwnerAndReadyArtifact(t *testing.T) {
	f := newContentFixture(t)
	ctx := t.Context()
	artifact := uuid.NewString()
	url := "https://" + artifact + ".example.test"
	var node int
	if err := f.pool.QueryRow(ctx, "INSERT INTO stream_nodes(url,name,type,enabled) VALUES($1,'Worker authority fixture','transcode',TRUE) RETURNING id", url).Scan(&node); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), "DELETE FROM download_artifacts WHERE id=$1", artifact)
		_, _ = f.pool.Exec(context.Background(), "DELETE FROM worker_http_download_attempts WHERE node_id=$1", node)
		_, _ = f.pool.Exec(context.Background(), "DELETE FROM stream_nodes WHERE id=$1", node)
	})
	if _, err := f.pool.Exec(ctx, "INSERT INTO download_artifacts(id,media_file_id,format,params_hash,status,attempts,lease_owner,lease_expires_at,worker_kind,worker_node_id) VALUES($1,$2,'mp4',$1,'running',1,'same-owner',now()+interval '1 minute','node',$3)", artifact, f.file, node); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(downloadprepare.Request{ArtifactID: artifact + "-attempt", LogSessionID: "download-prepare-" + artifact})
	a := workerwire.Assignment{Version: 1, NodeID: node, WorkID: artifact + "-attempt", Generation: 1, Path: "/downloads/prepare", Body: body}
	if err := admitWorkerDownload(ctx, f.pool, workercontrolcore.Node{ID: node}, a); err != nil {
		t.Fatal(err)
	}
	if !workerDownloadActive(ctx, f.pool, node, a) {
		t.Fatal("current attempt was refused")
	}
	if _, err := f.pool.Exec(ctx, "UPDATE download_artifacts SET attempts=2 WHERE id=$1", artifact); err != nil {
		t.Fatal(err)
	}
	if workerDownloadActive(ctx, f.pool, node, a) {
		t.Fatal("same lease owner resurrected previous attempt")
	}
	next := a
	next.Generation = 2
	if err := admitWorkerDownload(ctx, f.pool, workercontrolcore.Node{ID: node}, next); err != nil {
		t.Fatal(err)
	}
	if !workerDownloadActive(ctx, f.pool, node, next) {
		t.Fatal("current retry was refused")
	}
	if _, err := f.pool.Exec(ctx, "UPDATE download_artifacts SET status='ready',origin_node_id=$2,origin_artifact_id=$3,origin_node_url=$4,lease_owner=NULL,lease_expires_at=NULL WHERE id=$1", artifact, node, a.WorkID, url); err != nil {
		t.Fatal(err)
	}
	if !workerDownloadActive(ctx, f.pool, node, next) {
		t.Fatal("ready artifact required an encode lease")
	}
	if workerDownloadActive(ctx, f.pool, node, a) {
		t.Fatal("ready artifact revived stale generation from previous attempt")
	}
	if _, err := f.pool.Exec(ctx, "UPDATE download_artifacts SET status='queued',origin_node_id=0,origin_artifact_id='',origin_node_url='' WHERE id=$1", artifact); err != nil {
		t.Fatal(err)
	}
	if workerDownloadActive(ctx, f.pool, node, next) {
		t.Fatal("requeued artifact retained output authority")
	}
}

func TestWorkerContentAuthorityRevokesDirectAndInheritedEncodePermissions(t *testing.T) {
	f := newContentFixture(t)
	ctx := t.Context()
	video := *f.card
	video.TargetCodecVideo = "h264"
	audio := *f.card
	audio.PlayMethod = playback.PlayRemux
	audio.TranscodeAudio = true
	audio.TargetCodecAudio = "aac"
	cloned := *f.card
	cloned.PlayMethod = playback.PlayRemux
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := f.pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	check := func(card *playback.RecipeCard, want bool, reason string) {
		t.Helper()
		if got := f.gate.allowed(ctx, card); got != want {
			t.Fatalf("%s allowed=%v want=%v", reason, got, want)
		}
	}
	check(&video, true, "initial video")
	check(&audio, true, "initial audio")
	exec("UPDATE users SET transcode_allowed=FALSE WHERE id=$1", f.user)
	check(&video, false, "direct video revoke")
	check(&cloned, true, "copy remux preserves video permission independence")
	exec("UPDATE users SET transcode_allowed=TRUE,audio_transcode_allowed=FALSE WHERE id=$1", f.user)
	check(&audio, false, "direct audio revoke")
	check(&video, true, "video restored")
	var group int64
	if err := f.pool.QueryRow(ctx, "INSERT INTO access_groups(name,transcode_allowed,audio_transcode_allowed,download_allowed) VALUES($1,TRUE,TRUE,TRUE) RETURNING id", f.content).Scan(&group); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), "UPDATE users SET access_group_id=NULL WHERE id=$1", f.user)
		_, _ = f.pool.Exec(context.Background(), "DELETE FROM access_groups WHERE id=$1", group)
	})
	exec("UPDATE users SET access_group_id=$2,transcode_allowed=NULL,audio_transcode_allowed=NULL,download_allowed=NULL WHERE id=$1", f.user, group)
	check(&video, true, "inherited video")
	check(&audio, true, "inherited audio")
	exec("UPDATE access_groups SET transcode_allowed=FALSE WHERE id=$1", group)
	check(&video, false, "group video revoke")
	check(&cloned, true, "group video copy")
	exec("UPDATE access_groups SET transcode_allowed=TRUE,audio_transcode_allowed=FALSE WHERE id=$1", group)
	check(&audio, false, "group audio revoke")
	check(&video, true, "group video restored")
	exec("UPDATE access_groups SET audio_transcode_allowed=TRUE,download_allowed=FALSE WHERE id=$1", group)
	claims := streamtoken.Claims{UserID: f.user, ProfileID: "viewer", MediaFileID: f.file, DownloadArtifactID: "ready-artifact", PlayMethod: streamtoken.PlayMethodDownload}
	if f.gate.allowedClaims(ctx, &claims) {
		t.Fatal("ready artifact survived inherited group download revoke")
	}
}
