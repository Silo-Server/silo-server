package downloads

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
)

func TestServiceRelaysRemoteArtifactOnEstablishedRoute(t *testing.T) {
	const secret = "artifact-secret"
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/downloads/artifacts/artifact-1" || r.Header.Get("Authorization") != "Bearer "+secret {
			t.Errorf("origin request = %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
			http.Error(w, "unexpected origin request", http.StatusBadRequest)
			return
		}
		if r.Header.Get("Range") != "bytes=1-3" {
			t.Errorf("Range = %q", r.Header.Get("Range"))
			http.Error(w, "unexpected range", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Length", "3")
		w.Header().Set("Content-Range", "bytes 1-3/5")
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Disposition", `attachment; filename="artifact-1.mp4"`)
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("123"))
	}))
	defer origin.Close()
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = secret
	svc := &Service{artifacts: &ArtifactManager{liveCfg: func() *config.Config { return cfg }}}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/downloads/dl/file", nil)
	req.Header.Set("Range", "bytes=1-3")
	rr := httptest.NewRecorder()
	err := svc.serveFileTarget(req.Context(), rr, req, &FileTarget{
		Path:             `/artifacts/Movie "Final".mp4`,
		OriginNodeURL:    origin.URL,
		OriginArtifactID: "artifact-1",
	}, 7)
	if err != nil {
		t.Fatal(err)
	}
	if rr.Code != http.StatusPartialContent || rr.Body.String() != "123" || rr.Header().Get("Content-Range") != "bytes 1-3/5" ||
		rr.Header().Get("Content-Disposition") != `attachment; filename="Movie _Final_.mp4"` {
		t.Fatalf("response status=%d body=%q range=%q disposition=%q", rr.Code, rr.Body.String(), rr.Header().Get("Content-Range"), rr.Header().Get("Content-Disposition"))
	}
}

func TestServiceRequeuesReadyArtifactWhenRemoteFileIsMissing(t *testing.T) {
	repo, pool, fileID := newArtifactTestRepo(t)
	ctx := context.Background()
	artifact, _, err := repo.EnsureQueued(ctx, newArtifact(t, fileID, fmt.Sprintf("hash-missing-remote-%d", time.Now().UnixNano())))
	if err != nil {
		t.Fatal(err)
	}
	origin := httptest.NewServer(http.NotFoundHandler())
	defer origin.Close()
	const (
		secret           = "artifact-secret"
		originNodeID     = 424242
		originArtifactID = "artifact-missing"
	)
	if _, err := pool.Exec(ctx,
		`UPDATE download_artifacts
		 SET status = 'ready', file_size = 23, completed_at = now(),
		     origin_node_id = $2, origin_node_url = $3, origin_node_group = 'host-a', origin_artifact_id = $4
		 WHERE id = $1`,
		artifact.ID, originNodeID, origin.URL, originArtifactID,
	); err != nil {
		t.Fatal(err)
	}
	var userID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (username, role, download_allowed) VALUES ($1, 'user', true) RETURNING id`,
		fmt.Sprintf("missing-remote-user-%d", time.Now().UnixNano()),
	).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	downloadID := fmt.Sprintf("missing-remote-download-%d", time.Now().UnixNano())
	downloadRepo := NewRepository(pool)
	if err := downloadRepo.Create(ctx, &Download{
		ID: downloadID, UserID: userID, MediaFileID: fileID,
		ContentID: "missing-remote-content", Kind: KindQueued, Status: StatusCompleted,
		Format: FormatTranscode, ArtifactID: artifact.ID, FileSize: 23,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx,
			`DELETE FROM download_artifact_orphans
			 WHERE download_artifact_id = $1 AND origin_node_id = $2 AND origin_artifact_id = $3`,
			artifact.ID, originNodeID, originArtifactID,
		)
		_, _ = pool.Exec(ctx, `DELETE FROM downloads WHERE id = $1`, downloadID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
	})

	cfg := &config.Config{}
	cfg.Auth.JWTSecret = secret
	svc := &Service{artifacts: &ArtifactManager{
		repo:    repo,
		liveCfg: func() *config.Config { return cfg },
	}}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/downloads/dl/file", nil)
	err = svc.serveFileTarget(req.Context(), httptest.NewRecorder(), req, &FileTarget{
		ArtifactID:       artifact.ID,
		OriginNodeID:     originNodeID,
		OriginNodeURL:    origin.URL,
		OriginNodeGroup:  "host-a",
		OriginArtifactID: originArtifactID,
	}, 7)
	if !errors.Is(err, ErrDownloadNotActive) {
		t.Fatalf("serveFileTarget error = %v, want ErrDownloadNotActive", err)
	}

	queued, err := repo.GetByID(ctx, artifact.ID)
	if err != nil {
		t.Fatal(err)
	}
	if queued.Status != ArtifactQueued || queued.OriginNodeID != 0 || queued.OriginArtifactID != "" {
		t.Fatalf("artifact after missing remote response = %+v", queued)
	}
	linked, err := downloadRepo.GetByID(ctx, downloadID)
	if err != nil {
		t.Fatal(err)
	}
	if linked.Status != StatusPreparing || linked.BytesSent != 0 {
		t.Fatalf("linked download after missing remote response = %+v", linked)
	}
	var cleanupURL string
	if err := pool.QueryRow(ctx,
		`SELECT origin_node_url FROM download_artifact_orphans
		 WHERE download_artifact_id = $1 AND origin_node_id = $2 AND origin_artifact_id = $3`,
		artifact.ID, originNodeID, originArtifactID,
	).Scan(&cleanupURL); err != nil {
		t.Fatalf("remote cleanup row: %v", err)
	}
	if cleanupURL != origin.URL {
		t.Fatalf("remote cleanup URL = %q, want %q", cleanupURL, origin.URL)
	}
}

func TestServiceDoesNotAppendAPIErrorAfterRemoteBodyIsCommitted(t *testing.T) {
	const secret = "artifact-secret"
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "10")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "123")
	}))
	defer origin.Close()
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = secret
	svc := &Service{artifacts: &ArtifactManager{liveCfg: func() *config.Config { return cfg }}}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/downloads/dl/file", nil)
	rr := httptest.NewRecorder()
	err := svc.serveFileTarget(req.Context(), rr, req, &FileTarget{
		OriginNodeURL:    origin.URL,
		OriginArtifactID: "artifact-1",
	}, 7)
	if !errors.Is(err, ErrResponseCommitted) {
		t.Fatalf("serveFileTarget error = %v, want ErrResponseCommitted", err)
	}
	if rr.Code != http.StatusOK || rr.Body.String() != "123" {
		t.Fatalf("response status=%d body=%q", rr.Code, rr.Body.String())
	}
}

func TestServiceRelaysRemotePreconditionFailure(t *testing.T) {
	const secret = "artifact-secret"
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("If-Match"); got != `"old-etag"` {
			t.Errorf("origin If-Match = %q", got)
			http.Error(w, "unexpected condition", http.StatusBadRequest)
			return
		}
		w.Header().Set("ETag", `"new-etag"`)
		w.WriteHeader(http.StatusPreconditionFailed)
	}))
	defer origin.Close()
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = secret
	svc := &Service{artifacts: &ArtifactManager{liveCfg: func() *config.Config { return cfg }}}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/downloads/dl/file", nil)
	req.Header.Set("If-Match", `"old-etag"`)
	rr := httptest.NewRecorder()
	if err := svc.serveFileTarget(req.Context(), rr, req, &FileTarget{
		OriginNodeURL:    origin.URL,
		OriginArtifactID: "artifact-1",
	}, 7); err != nil {
		t.Fatal(err)
	}
	if rr.Code != http.StatusPreconditionFailed || rr.Header().Get("ETag") != `"new-etag"` {
		t.Fatalf("response status=%d etag=%q body=%q", rr.Code, rr.Header().Get("ETag"), rr.Body.String())
	}
}
