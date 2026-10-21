package downloads

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/downloadprepare"
	"github.com/Silo-Server/silo-server/internal/idgen"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

func newArtifactTestRepo(t *testing.T) (*ArtifactRepository, *pgxpool.Pool, int) {
	t.Helper()
	pool := newDownloadsTestPool(t)
	ctx := context.Background()
	var present *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.download_artifacts')::text`).Scan(&present); err != nil {
		t.Fatalf("check download_artifacts: %v", err)
	}
	if present == nil {
		t.Skip("download_artifacts migration has not been applied")
	}
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.download_artifact_orphans')::text`).Scan(&present); err != nil {
		t.Fatalf("check download_artifact_orphans: %v", err)
	}
	if present == nil {
		t.Skip("download_artifact_orphans migration has not been applied")
	}

	suffix := time.Now().UnixNano()
	var folderID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO media_folders (type, name) VALUES ('movies', $1) RETURNING id`,
		fmt.Sprintf("Artifacts Test %d", suffix),
	).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	var fileID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO media_files (media_folder_id, file_path) VALUES ($1, $2) RETURNING id`,
		folderID, fmt.Sprintf("/tmp/artifact-%d.mkv", suffix),
	).Scan(&fileID); err != nil {
		t.Fatalf("seed media file: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM download_artifacts WHERE media_file_id = $1`, fileID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE id = $1`, fileID)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	return NewArtifactRepository(pool), pool, fileID
}

func newArtifact(t *testing.T, fileID int, hash string) *Artifact {
	t.Helper()
	id, err := idgen.NextID()
	if err != nil {
		t.Fatalf("id: %v", err)
	}
	return &Artifact{
		ID: id, MediaFileID: fileID, Format: "transcode", ParamsHash: hash,
		Container: "mp4", CodecVideo: "h264", CodecAudio: "aac", Resolution: "1080p",
		AudioTrackIndex: -1, OutputPath: "/tmp/" + id + ".mp4", MaxAttempts: 3,
	}
}

func remoteOrphansForArtifact(orphans []RemoteArtifactOrphan, artifactID string) []RemoteArtifactOrphan {
	filtered := make([]RemoteArtifactOrphan, 0, 1)
	for _, orphan := range orphans {
		if orphan.DownloadArtifactID == artifactID {
			filtered = append(filtered, orphan)
		}
	}
	return filtered
}

func TestArtifactEnsureQueuedRoundTripsToneMapRecipe(t *testing.T) {
	for _, kind := range tonemap.AllSourceKinds() {
		t.Run(string(kind), func(t *testing.T) {
			assertArtifactToneMapRecipeRoundTrips(t, kind)
		})
	}
}

// assertArtifactToneMapRecipeRoundTrips proves the download_artifacts recipe
// constraint accepts a tone-map source kind and that every field survives.
func assertArtifactToneMapRecipeRoundTrips(t *testing.T, kind tonemap.SourceKind) {
	t.Helper()
	repo, _, fileID := newArtifactTestRepo(t)
	ctx := context.Background()
	artifact := newArtifact(t, fileID, "hash-tone-map-round-trip-"+string(kind))
	artifact.ToneMapPolicy = tonemap.PolicyHardwareThenSoftware
	artifact.ToneMapMode = tonemap.ModeHardware
	artifact.ToneMapSourceKind = kind
	wantRecipeVersion := playback.TransformationHDRToSDRToneMapRecipeVersionV3
	wantSourceRevision := tonemap.SourceRevision{MediaFileID: fileID, FileSize: 100, StreamSignature: "video"}.Encode()
	artifact.ToneMapRecipeVersion = wantRecipeVersion
	artifact.ToneMapSourceRevision = wantSourceRevision

	returned, created, err := repo.EnsureQueued(ctx, artifact)
	if err != nil || !created {
		t.Fatalf("EnsureQueued = (%+v, created=%v, %v), want new tone-mapped artifact", returned, created, err)
	}
	assertToneMapRecipe := func(name string, got *Artifact) {
		t.Helper()
		if got.ToneMapPolicy != tonemap.PolicyHardwareThenSoftware || got.ToneMapMode != tonemap.ModeHardware || got.ToneMapSourceKind != kind || got.ToneMapRecipeVersion != wantRecipeVersion || got.ToneMapSourceRevision != wantSourceRevision {
			t.Fatalf("%s tone-map recipe = policy %q mode %q source %q version %q revision %q; want version %q revision %q", name, got.ToneMapPolicy, got.ToneMapMode, got.ToneMapSourceKind, got.ToneMapRecipeVersion, got.ToneMapSourceRevision, wantRecipeVersion, wantSourceRevision)
		}
	}
	assertToneMapRecipe("returned", returned)
	persisted, err := repo.GetByID(ctx, returned.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertToneMapRecipe("persisted", persisted)
}

func TestArtifactRecoveryDeletesWrongSizedRemoteBeforeRequeue(t *testing.T) {
	repo, pool, fileID := newArtifactTestRepo(t)
	ctx := context.Background()
	row, _, err := repo.EnsureQueued(ctx, newArtifact(t, fileID, "hash-remote-size-mismatch"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ClaimNext(ctx, "worker", time.Minute); err != nil {
		t.Fatal(err)
	}
	if applied, err := repo.MarkReady(ctx, row.ID, "worker", row.OutputPath, 17, "http://transcode", "host-a", "artifact-truncated", 4242, nil); err != nil || !applied {
		t.Fatalf("MarkReady = (%v, %v)", applied, err)
	}

	preparer := &lifecycleTestPreparer{stat: downloadprepare.Result{ArtifactID: "artifact-truncated", FileSize: 42}}
	manager := &ArtifactManager{
		repo:      repo,
		downloads: NewRepository(pool),
		preparer:  preparer,
	}
	manager.recoverReadyArtifacts(ctx)

	got, err := repo.GetByID(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != ArtifactQueued || got.OriginArtifactID != "" {
		t.Fatalf("recovered artifact = %+v, want queued without remote locator", got)
	}
	orphans, err := repo.ListRemoteOrphansDue(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	orphans = remoteOrphansForArtifact(orphans, row.ID)
	if len(orphans) != 1 || orphans[0].OriginNodeID != 17 || orphans[0].OriginArtifactID != "artifact-truncated" {
		t.Fatalf("remote cleanup queue = %+v", orphans)
	}
	if preparer.deleted != "artifact-truncated" {
		t.Fatalf("deleted remote artifact = %q, want artifact-truncated", preparer.deleted)
	}
	t.Cleanup(func() { _ = repo.DeleteRemoteOrphan(ctx, orphans[0].ID) })
}
