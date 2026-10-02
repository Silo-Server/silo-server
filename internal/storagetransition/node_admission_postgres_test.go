package storagetransition

import (
	"context"
	"encoding/json"
	"errors"
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

func TestStorageTransitionExcludesNodeJoinThroughCopyAndCommit(t *testing.T) {
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

	source := &fencedMemoryStore{memoryStore: &memoryStore{
		identity: "s3|old|public|", objects: map[string][]byte{"tmdb/a.webp": []byte("a")},
	}}
	target := &memoryStore{identity: "local|target", objects: map[string][]byte{}}
	settings := stagedLocal(t, t.TempDir())
	service := New(nil, settings, nil, source, nil)
	service.SetNodeAdmission(owner)
	service.openPublic = func(map[string]string) (blobstore.Store, error) { return target, nil }
	fenced := make(chan struct{})
	resume := make(chan struct{})
	source.onFence = func() {
		close(fenced)
		<-resume
	}
	done := make(chan error, 1)
	go func() {
		_, err := service.ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{Policy: PolicyMigrateAll}, func(adminjob.StorageTransitionProgress) {})
		done <- err
	}()
	select {
	case <-fenced:
	case <-time.After(5 * time.Second):
		t.Fatal("transition never reached the final copy fence")
	}
	assertJoinBlocked := func(phase string) {
		t.Helper()
		joinCtx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
		defer cancel()
		joined, err := pglock.AdmitNode(joinCtx, pool, key)
		if joined != nil || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("join %s = (%v, %v), want deadline", phase, joined, err)
		}
	}
	assertJoinBlocked("during final copy")
	close(resume)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("execute transition: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("transition did not finish after final copy resumed")
	}
	var stage stagedTarget
	if err := json.Unmarshal([]byte(settings.values[StagedTargetSettingKey]), &stage); err != nil {
		t.Fatal(err)
	}
	if stage.Phase != transitionPhaseRestartPending || !source.fenced {
		t.Fatalf("committed stage/fence = %q/%t", stage.Phase, source.fenced)
	}
	assertJoinBlocked("after commit, before old process exit")
	if err := owner.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	joined, err := pglock.AdmitNode(t.Context(), pool, key)
	if err != nil {
		t.Fatalf("join after old process exit: %v", err)
	}
	if err := joined.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// A queued transition can be claimed as soon as the database answers again,
// before the node's admission monitor has rejoined. Execute waits for the
// rejoin instead of failing the job.
func TestExclusiveAdmissionWaitsForRejoin(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	owner, err := pglock.AdmitNode(t.Context(), pool, time.Now().UnixNano())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close(context.Background()) })
	unreachable, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	if err := owner.Probe(unreachable); !errors.Is(err, pglock.ErrAdmissionRejoining) {
		t.Fatalf("probe without a reachable database = %v, want rejoining", err)
	}
	service := New(nil, &memorySettings{values: map[string]string{}}, nil, &memoryStore{identity: "local|/srv/silo", objects: map[string][]byte{}}, nil)
	service.SetNodeAdmission(owner)
	// The monitor rejoins while Execute waits between attempts.
	waited := 0
	service.admissionRejoinBackoff = func(ctx context.Context, _ int) error {
		waited++
		return owner.Probe(ctx)
	}
	acquired, err := service.tryExclusiveAdmission(t.Context())
	if err != nil || !acquired || waited != 1 {
		t.Fatalf("exclusive admission during rejoin = (%t, %v) after %d waits", acquired, err, waited)
	}
}

// terminatingCommitSettings ends the owner's admission session as the commit's
// settings transaction starts, the latest point a node could still rejoin.
type terminatingCommitSettings struct {
	*memorySettings
	armed     bool
	terminate func()
}

func (s *terminatingCommitSettings) UpdateAtomic(ctx context.Context, update func(map[string]string) (map[string]string, error)) error {
	if s.armed {
		s.armed = false
		s.terminate()
	}
	return s.memorySettings.UpdateAtomic(ctx, update)
}

func TestCommitRequiresLiveExclusiveAdmission(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	// A key below 2^32 keeps the advisory lock's classid at zero for the lookup.
	key := time.Now().UnixNano()%1_000_000_000 + 1
	owner, err := pglock.AdmitNode(t.Context(), pool, key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close(context.Background()) })
	sourceDir, targetDir := t.TempDir(), t.TempDir()
	source := &memoryStore{identity: "local|" + sourceDir, objects: map[string][]byte{"tmdb/a.webp": []byte("a")}}
	settings := &terminatingCommitSettings{memorySettings: &memorySettings{values: map[string]string{
		settingArtworkBackend:   blobstore.BackendLocal,
		settingArtworkLocalPath: sourceDir,
	}}}
	settings.terminate = func() {
		if _, err := pool.Exec(context.Background(), `SELECT pg_terminate_backend(pid) FROM pg_locks
			WHERE locktype = 'advisory' AND classid = 0 AND objid = $1::bigint::oid AND objsubid = 1 AND granted`, key); err != nil {
			t.Error(err)
		}
	}
	service := New(nil, settings, memoryJobs{}, source, nil)
	service.SetNodeAdmission(owner)
	if _, _, err := service.Start(t.Context(), 1, StartRequest{Policy: PolicyMigrateAll, Values: map[string]string{
		settingArtworkBackend:   blobstore.BackendLocal,
		settingArtworkLocalPath: targetDir,
	}}); err != nil {
		t.Fatal(err)
	}
	_, err = service.ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{Policy: PolicyMigrateAll}, func(progress adminjob.StorageTransitionProgress) {
		if progress.Phase == "committing" {
			settings.armed = true
		}
	})
	if err == nil || !strings.Contains(err.Error(), "admission lost before commit") {
		t.Fatalf("commit after losing admission = %v, want refusal", err)
	}
	if got := settings.values[blobstore.IdentitySettingKey]; got != "" {
		t.Fatalf("refused commit recorded identity %q", got)
	}
	select {
	case <-owner.Lost():
	default:
		t.Fatal("lost admission was not signaled")
	}
}
