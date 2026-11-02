package storagetransition

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/database/pglock"
)

func TestQueuedStorageTransitionRejectsNodeThatJoinedAfterStart(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	key := time.Now().UnixNano()
	owner, err := pglock.AdmitNode(t.Context(), pool, key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close(context.Background()) })
	sourceDir, targetDir := t.TempDir(), t.TempDir()
	source := &memoryStore{identity: "local|" + sourceDir, objects: map[string][]byte{"tmdb/a.webp": []byte("a")}}
	settings := &memorySettings{values: map[string]string{
		settingArtworkBackend:   blobstore.BackendLocal,
		settingArtworkLocalPath: sourceDir,
	}}
	service := New(nil, settings, memoryJobs{}, source, nil)
	service.SetNodeAdmission(owner)
	job, _, err := service.Start(t.Context(), 1, StartRequest{Policy: PolicyMigrateAll, Values: map[string]string{
		settingArtworkBackend:   blobstore.BackendLocal,
		settingArtworkLocalPath: targetDir,
	}})
	if err != nil || job == nil {
		t.Fatalf("Start = (%v, %v), want a queued job", job, err)
	}
	peer, err := pglock.AdmitNode(t.Context(), pool, key)
	if err != nil {
		t.Fatalf("join after Start: %v", err)
	}
	t.Cleanup(func() { _ = peer.Close(context.Background()) })
	_, err = service.ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{Policy: PolicyMigrateAll}, func(adminjob.StorageTransitionProgress) {})
	if err == nil || !strings.Contains(err.Error(), "another write-capable API node") {
		t.Fatalf("ExecuteStorageTransition after join = %v, want admission rejection", err)
	}
	if source.lists != 0 || source.gets != 0 || settings.values[blobstore.IdentitySettingKey] != "" {
		t.Fatalf("rejected job touched source or committed: lists=%d gets=%d identity=%q", source.lists, source.gets, settings.values[blobstore.IdentitySettingKey])
	}
}
