package storagetransition

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/database/pglock"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
)

type gatedAdmissionJobs struct {
	JobRepository
	entered chan struct{}
	proceed chan struct{}
}

func (j *gatedAdmissionJobs) Create(ctx context.Context, input adminjob.CreateJobInput) (*models.AdminJob, error) {
	close(j.entered)
	select {
	case <-j.proceed:
		return j.JobRepository.Create(ctx, input)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestStartSerializesStageAndAdmissionAcrossServicesPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var userID int
	if err := pool.QueryRow(ctx, `SELECT id FROM users ORDER BY id LIMIT 1`).Scan(&userID); err != nil {
		t.Fatal("database requires a fixture user: ", err)
	}
	repo := adminjob.NewRepository(pool)
	jobs := &gatedAdmissionJobs{JobRepository: repo, entered: make(chan struct{}), proceed: make(chan struct{})}
	var resumeOnce sync.Once
	resume := func() { resumeOnce.Do(func() { close(jobs.proceed) }) }
	t.Cleanup(resume)
	sourceDir := t.TempDir()
	source := &memoryStore{identity: "local|" + sourceDir, objects: map[string][]byte{}}
	settings := &memorySettings{values: map[string]string{settingArtworkBackend: blobstore.BackendLocal, settingArtworkLocalPath: sourceDir}}
	owner := New(pool, settings, jobs, source, nil)
	contender := New(pool, settings, repo, source, nil)
	req := StartRequest{Policy: PolicyFresh, Values: map[string]string{settingArtworkLocalPath: t.TempDir()}}
	type result struct {
		job *models.AdminJob
		err error
	}
	done := make(chan result, 1)
	go func() {
		job, _, err := owner.Start(ctx, userID, req)
		done <- result{job: job, err: err}
	}()
	select {
	case <-jobs.entered:
	case <-ctx.Done():
		t.Fatal("owner did not reach job admission")
	}
	before, err := settings.Get(ctx, StagedTargetSettingKey)
	if err != nil {
		t.Fatal(err)
	}
	_, _, contenderErr := contender.Start(ctx, userID, req)
	after, err := settings.Get(ctx, StagedTargetSettingKey)
	if err != nil {
		t.Fatal(err)
	}
	resume()
	select {
	case got := <-done:
		if got.job != nil {
			t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM admin_jobs WHERE id=$1`, got.job.ID) })
		}
		if got.err != nil || got.job == nil {
			t.Fatalf("owner admission failed: %v", got.err)
		}
	case <-ctx.Done():
		t.Fatal("owner did not finish admission")
	}
	if !errors.Is(contenderErr, adminjob.ErrActiveJobConflict) {
		t.Fatalf("contending Start = %v, want admission conflict", contenderErr)
	}
	if before != after {
		t.Fatal("contending request changed the owner's stage")
	}
}

func TestFinalizeCommittedCompletesInterruptedReceiptPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var userID int
	if err := pool.QueryRow(t.Context(), `SELECT id FROM users ORDER BY id LIMIT 1`).Scan(&userID); err != nil {
		t.Skipf("database has no fixture user: %v", err)
	}
	transitionID := uuid.NewString()
	jobID := "storage-transition-finalize-" + uuid.NewString()
	failedJobID := "storage-transition-failed-" + uuid.NewString()
	runningJobID := "storage-transition-running-" + uuid.NewString()
	request, _ := json.Marshal(adminjob.StorageTransitionRequest{TransitionID: transitionID, Policy: PolicyFresh})
	if _, err := pool.Exec(t.Context(), `INSERT INTO admin_jobs (id, job_type, status, created_by_user_id, request_payload, result_payload, message, completed_at) VALUES ($1,$2,'completed',$3,$4::jsonb,'{"manual_restart_required":true}'::jsonb,'restart Silo manually',now())`, jobID, adminjob.JobTypeStorageTransition, userID, string(request)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO admin_jobs (id, job_type, status, created_by_user_id, request_payload, result_payload, message, error_message, completed_at) VALUES ($1,$2,'failed',$3,$4::jsonb,'{"manual_restart_required":true}'::jsonb,'earlier attempt failed','old failure',now())`, failedJobID, adminjob.JobTypeStorageTransition, userID, string(request)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO admin_jobs (id, job_type, status, created_by_user_id, request_payload, result_payload, message) VALUES ($1,$2,'running',$3,$4::jsonb,'"interrupted"'::jsonb,'copy interrupted')`, runningJobID, adminjob.JobTypeStorageTransition, userID, string(request)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM admin_jobs WHERE id=ANY($1)`, []string{jobID, failedJobID, runningJobID})
	})
	var completedUpdatedAt time.Time
	if err := pool.QueryRow(t.Context(), `SELECT updated_at FROM admin_jobs WHERE id=$1`, jobID).Scan(&completedUpdatedAt); err != nil {
		t.Fatal(err)
	}

	identity := "s3|https://target.example|public|"
	stage := stagedTarget{ID: transitionID, Policy: PolicyFresh, SourceIdentity: "s3|https://old.example|public|", TargetIdentity: identity, Phase: transitionPhaseRestartPending, Values: map[string]string{"artwork.storage_backend": blobstore.BackendS3}}
	raw, _ := json.Marshal(stage)
	settings := &memorySettings{values: map[string]string{StagedTargetSettingKey: string(raw)}}
	service := New(pool, settings, nil, &memoryStore{identity: identity, objects: map[string][]byte{}}, nil)
	if err := service.FinalizeCommitted(t.Context()); err != nil {
		t.Fatal(err)
	}
	var status, message string
	var result []byte
	if err := pool.QueryRow(t.Context(), `SELECT status, message, result_payload FROM admin_jobs WHERE id=$1`, jobID).Scan(&status, &message, &result); err != nil {
		t.Fatal(err)
	}
	if status != adminjob.StatusCompleted || message != "restart Silo manually" {
		t.Fatalf("finalized job status=%q message=%q", status, message)
	}
	var structured Result
	if err := json.Unmarshal(result, &structured); err != nil || structured.ManualRestartRequired || structured.Phase != "completed" {
		t.Fatalf("finalized result=%s err=%v", result, err)
	}
	var failedStatus, failedMessage, failedError string
	if err := pool.QueryRow(t.Context(), `SELECT status, message, error_message FROM admin_jobs WHERE id=$1`, failedJobID).Scan(&failedStatus, &failedMessage, &failedError); err != nil {
		t.Fatal(err)
	}
	if failedStatus != adminjob.StatusFailed || failedMessage != "earlier attempt failed" || failedError != "old failure" {
		t.Fatalf("failed attempt was rewritten: status=%q message=%q error=%q", failedStatus, failedMessage, failedError)
	}
	var runningStatus, runningMessage string
	var runningResult []byte
	var runningExpiresAt time.Time
	if err := pool.QueryRow(t.Context(), `SELECT status, message, result_payload, expires_at FROM admin_jobs WHERE id=$1`, runningJobID).Scan(&runningStatus, &runningMessage, &runningResult, &runningExpiresAt); err != nil {
		t.Fatal(err)
	}
	if runningStatus != adminjob.StatusCompleted || !strings.Contains(runningMessage, "after restart") {
		t.Fatalf("running receipt status=%q message=%q", runningStatus, runningMessage)
	}
	if runningExpiresAt.Before(time.Now().Add(6 * 24 * time.Hour)) {
		t.Fatalf("running receipt expires too soon: %v", runningExpiresAt)
	}
	var runningFields struct {
		Phase                 string `json:"phase"`
		ManualRestartRequired bool   `json:"manual_restart_required"`
	}
	if err := json.Unmarshal(runningResult, &runningFields); err != nil || runningFields.Phase != "completed" || runningFields.ManualRestartRequired {
		t.Fatalf("running receipt result=%s err=%v", runningResult, err)
	}
	if err := service.completeFinalizedJob(t.Context(), stage); err != nil {
		t.Fatal(err)
	}
	var completedUpdatedAgain time.Time
	if err := pool.QueryRow(t.Context(), `SELECT updated_at FROM admin_jobs WHERE id=$1`, jobID).Scan(&completedUpdatedAgain); err != nil {
		t.Fatal(err)
	}
	if !completedUpdatedAgain.Equal(completedUpdatedAt) {
		t.Fatalf("second finalization touched completed job: before=%v after=%v", completedUpdatedAt, completedUpdatedAgain)
	}
}

func TestPostRestartRepairsArtifactsAfterBootStageReadFailurePostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var userID int
	if err := pool.QueryRow(t.Context(), `SELECT id FROM users ORDER BY id LIMIT 1`).Scan(&userID); err != nil {
		t.Skipf("database has no fixture user: %v", err)
	}

	transitionID := uuid.NewString()
	artifactJobID := "storage-transition-artifact-" + uuid.NewString()
	oldBucket := "private-old-" + uuid.NewString()
	newBucket := "private-new-" + uuid.NewString()
	request, err := json.Marshal(adminjob.StorageTransitionRequest{TransitionID: transitionID, Policy: PolicyMigrateAll})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO admin_jobs (id, job_type, status, created_by_user_id, request_payload, result_payload)
		VALUES ($1, $2, 'running', $3, $4::jsonb, '{}')`, transitionID, adminjob.JobTypeStorageTransition, userID, string(request)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM admin_jobs WHERE id=ANY($1)`, []string{transitionID, artifactJobID})
	})
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO admin_jobs (id, job_type, status, created_by_user_id, artifact_bucket, artifact_key)
		VALUES ($1, 'catalog_export', 'completed', $2, $3, 'catalog-seeds/test')`, artifactJobID, userID, oldBucket); err != nil {
		t.Fatal(err)
	}

	target := &memoryStore{identity: "s3|https://s3|public-new|", objects: map[string][]byte{}}
	stage := stagedTarget{
		ID: transitionID, Policy: PolicyMigrateAll, Phase: transitionPhaseRestartPending,
		SourceIdentity: "s3|https://s3|public-old|", TargetIdentity: target.Identity(),
		SourcePrivateBucket: oldBucket, TargetPrivateBucket: newBucket, PublicReconcile: true,
	}
	raw, err := json.Marshal(stage)
	if err != nil {
		t.Fatal(err)
	}
	settings := &transientGetSettings{memorySettings: &memorySettings{values: map[string]string{StagedTargetSettingKey: string(raw)}}, remainingFailures: 1}
	service := New(pool, settings, nil, target, nil)
	service.reconcile = func(context.Context, blobstore.Store, func(float64, string)) (metadata.ArtworkReconcileStats, error) {
		return metadata.ArtworkReconcileStats{Verified: 1}, nil
	}
	if err := service.FinalizeCommitted(t.Context()); err != nil {
		t.Fatal(err)
	}
	var artifactBucket, receiptStatus string
	if err := pool.QueryRow(t.Context(), `SELECT artifact_bucket FROM admin_jobs WHERE id=$1`, artifactJobID).Scan(&artifactBucket); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT status FROM admin_jobs WHERE id=$1`, transitionID).Scan(&receiptStatus); err != nil {
		t.Fatal(err)
	}
	if artifactBucket != oldBucket || receiptStatus != adminjob.StatusRunning {
		t.Fatalf("boot read failure changed artifact bucket %q or receipt status %q", artifactBucket, receiptStatus)
	}

	if err := service.RunPostRestartWork(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT artifact_bucket FROM admin_jobs WHERE id=$1`, artifactJobID).Scan(&artifactBucket); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT status FROM admin_jobs WHERE id=$1`, transitionID).Scan(&receiptStatus); err != nil {
		t.Fatal(err)
	}
	if artifactBucket != newBucket || receiptStatus != adminjob.StatusCompleted || settings.values[StagedTargetSettingKey] != "" {
		t.Fatalf("recovery left artifact bucket %q, receipt status %q, or staged transition %q", artifactBucket, receiptStatus, settings.values[StagedTargetSettingKey])
	}
	if err := service.RunPostRestartWork(t.Context()); err != nil {
		t.Fatalf("repeated recovery failed: %v", err)
	}
}

func TestPostRestartReconcileHasSingleDatabaseOwner(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	target := &memoryStore{identity: "local|target", objects: map[string][]byte{}}
	stage := stagedTarget{ID: uuid.NewString(), Policy: PolicyFresh, SourceIdentity: "local|old", TargetIdentity: target.Identity(), PublicReconcile: true, Phase: transitionPhaseRestartPending}
	raw, _ := json.Marshal(stage)
	settings := &memorySettings{values: map[string]string{StagedTargetSettingKey: string(raw)}}
	owner := New(pool, settings, nil, target, nil)
	wake := New(pool, settings, nil, target, nil)
	ownerStarted := make(chan struct{})
	owner.reconcile = func(ctx context.Context, _ blobstore.Store, _ func(float64, string)) (metadata.ArtworkReconcileStats, error) {
		close(ownerStarted)
		<-ctx.Done()
		return metadata.ArtworkReconcileStats{}, ctx.Err()
	}
	wakeRuns := make(chan struct{}, 1)
	wake.reconcile = func(context.Context, blobstore.Store, func(float64, string)) (metadata.ArtworkReconcileStats, error) {
		wakeRuns <- struct{}{}
		return metadata.ArtworkReconcileStats{}, nil
	}
	waiterObserved := make(chan struct{})
	allowRetry := make(chan struct{})
	wake.postRestartBackoff = func(ctx context.Context, _ int) error {
		select {
		case <-waiterObserved:
		default:
			close(waiterObserved)
		}
		select {
		case <-allowRetry:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	ownerCtx, cancelOwner := context.WithCancel(t.Context())
	ownerDone := make(chan error, 1)
	go func() { ownerDone <- owner.RunPostRestartWork(ownerCtx) }()
	select {
	case <-ownerStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("lock owner did not start reconciliation")
	}
	wakeDone := make(chan error, 1)
	go func() { wakeDone <- wake.RunPostRestartWork(t.Context()) }()
	select {
	case <-waiterObserved:
	case <-time.After(5 * time.Second):
		t.Fatal("lock waiter did not enter retry backoff")
	}
	currentRaw, err := settings.Get(t.Context(), StagedTargetSettingKey)
	if err != nil {
		t.Fatal(err)
	}
	var current stagedTarget
	if err := json.Unmarshal([]byte(currentRaw), &current); err != nil {
		t.Fatal(err)
	}
	if current.RecoveryState != recoveryStateRunning {
		t.Fatalf("lock contender overwrote owner recovery state: %q", current.RecoveryState)
	}
	cancelOwner()
	select {
	case err := <-ownerDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("owner shutdown = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("lock owner did not stop after cancellation")
	}
	close(allowRetry)
	select {
	case err := <-wakeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("lock waiter did not take over after owner release")
	}
	select {
	case <-wakeRuns:
	default:
		t.Fatal("waiting API node did not take over after owner released the advisory lock")
	}
}

func TestPostRestartWithoutStageReturnsWhileReconcileLockIsHeld(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	lock, acquired, err := pglock.TryAcquire(t.Context(), pool, pglock.ArtworkReconcileLockKey)
	if err != nil || !acquired {
		t.Fatalf("hold artwork reconcile lock: acquired=%t err=%v", acquired, err)
	}
	t.Cleanup(func() { _ = lock.Release(context.Background()) })

	settings := &memorySettings{values: map[string]string{}}
	service := New(pool, settings, nil, &memoryStore{identity: "local|target", objects: map[string][]byte{}}, nil)
	backoffs := 0
	service.postRestartBackoff = func(context.Context, int) error {
		backoffs++
		return nil
	}
	if err := service.RunPostRestartWork(t.Context()); err != nil {
		t.Fatal(err)
	}
	if backoffs != 0 {
		t.Fatalf("idle recovery loop backed off %d times", backoffs)
	}
}

func TestPostRestartRetriesStageReadFailureAfterTakingLock(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	target := &memoryStore{identity: "local|target", objects: map[string][]byte{}}
	stage := stagedTarget{ID: uuid.NewString(), TargetIdentity: target.Identity(), PublicReconcile: true, Phase: transitionPhaseRestartPending}
	raw, _ := json.Marshal(stage)
	settings := &nthGetErrorSettings{memorySettings: &memorySettings{values: map[string]string{StagedTargetSettingKey: string(raw)}}, failAt: 2}
	service := New(pool, settings, nil, target, nil)
	backoffs := 0
	service.postRestartBackoff = func(context.Context, int) error {
		backoffs++
		health, err := service.SourceHealth(t.Context(), false)
		if err != nil {
			t.Fatal(err)
		}
		if health.RecoveryState != recoveryStateWaitingRetry || !strings.Contains(health.RecoveryError, "staged-state read outage") {
			t.Fatalf("retry health=%#v", health)
		}
		return nil
	}
	service.reconcile = func(context.Context, blobstore.Store, func(float64, string)) (metadata.ArtworkReconcileStats, error) {
		return metadata.ArtworkReconcileStats{Verified: 1}, nil
	}
	if err := service.RunPostRestartWork(t.Context()); err != nil {
		t.Fatal(err)
	}
	if backoffs != 1 || settings.values[StagedTargetSettingKey] != "" {
		t.Fatalf("backoffs=%d staged=%q", backoffs, settings.values[StagedTargetSettingKey])
	}
}

func TestPostRestartLockAcquisitionErrorDoesNotOverwriteOwnerStatus(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	target := &memoryStore{identity: "local|target", objects: map[string][]byte{}}
	stage := stagedTarget{ID: uuid.NewString(), TargetIdentity: target.Identity(), PublicReconcile: true, Phase: transitionPhaseRestartPending, RecoveryState: recoveryStateRunning, RecoveryMessage: "Owner is reconciling"}
	raw, _ := json.Marshal(stage)
	settings := &memorySettings{values: map[string]string{StagedTargetSettingKey: string(raw)}}
	service := New(pool, settings, nil, target, nil)
	pool.Close()
	done, owned, err := service.runPostRestartAttempt(t.Context())
	if err == nil || done || owned {
		t.Fatalf("attempt done=%t owned=%t error=%v", done, owned, err)
	}
	currentRaw, getErr := settings.Get(t.Context(), StagedTargetSettingKey)
	if getErr != nil {
		t.Fatal(getErr)
	}
	var current stagedTarget
	if err := json.Unmarshal([]byte(currentRaw), &current); err != nil {
		t.Fatal(err)
	}
	if current.RecoveryState != recoveryStateRunning || current.RecoveryMessage != "Owner is reconciling" {
		t.Fatalf("non-owner overwrote recovery state: %#v", current)
	}
}

func TestPostgresListingStateAndOrphanCleanupAcrossPages(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	transitionID := uuid.NewString()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM storage_transition_checkpoints WHERE transition_id=$1`, transitionID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM storage_transition_cursors WHERE transition_id=$1`, transitionID)
	})
	source := &memoryStore{identity: "s3|source|public|", objects: map[string][]byte{}}
	for i := range 301 {
		source.objects[fmt.Sprintf("tmdb/%03d.webp", i)] = []byte(fmt.Sprintf("image-%03d", i))
	}
	target := &memoryStore{identity: "s3|target|public|", objects: map[string][]byte{"unrelated/keep.webp": []byte("keep")}}
	service := New(pool, &memorySettings{values: map[string]string{}}, nil, source, nil)
	runID := uuid.NewString()
	if _, _, _, err := service.copyPrefixPass(t.Context(), transitionID, "public:", source, target, "", func(int, int, string) {}, 0, runID, nil, false); err != nil {
		t.Fatal(err)
	}
	bulkGets := source.gets
	delete(source.objects, "tmdb/000.webp")
	if _, _, _, err := service.copyPrefixPass(t.Context(), transitionID, "public:", source, target, "", func(int, int, string) {}, 0, runID, nil, true); err != nil {
		t.Fatal(err)
	}
	if source.gets != bulkGets {
		t.Fatalf("database listing shortcut read source objects: before=%d after=%d", bulkGets, source.gets)
	}
	if _, ok := target.objects["tmdb/000.webp"]; ok {
		t.Fatal("target retained an object removed between passes")
	}
	if string(target.objects["unrelated/keep.webp"]) != "keep" {
		t.Fatal("orphan cleanup removed an unrelated target object")
	}
	if len(source.objects) != 300 {
		t.Fatalf("source object count=%d, want 300", len(source.objects))
	}
}

func TestPostgresPartialOrphanDeletionRetainsCheckpoints(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	transitionID := uuid.NewString()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM storage_transition_checkpoints WHERE transition_id=$1`, transitionID)
	})
	for _, key := range []string{"tmdb/failed.webp", "tmdb/succeeded.webp"} {
		if _, err := pool.Exec(t.Context(), `INSERT INTO storage_transition_checkpoints
			(transition_id, scope, object_key, source_size, sha256)
			VALUES ($1, 'public:', $2, 1, $3)`, transitionID, key, strings.Repeat("0", 64)); err != nil {
			t.Fatal(err)
		}
	}
	target := &partialDeleteStore{
		memoryStore: &memoryStore{identity: "s3|target|public|", objects: map[string][]byte{
			"tmdb/failed.webp":    []byte("failed"),
			"tmdb/succeeded.webp": []byte("succeeded"),
		}},
		failDeleteKey: "tmdb/failed.webp",
	}
	service := New(pool, &memorySettings{values: map[string]string{}}, nil, nil, nil)
	if err := service.deleteCheckpointOrphans(t.Context(), transitionID, "public:", "final-run", target, nil); err == nil || !strings.Contains(err.Error(), "deleted 1 of 2") {
		t.Fatalf("partial orphan deletion error=%v", err)
	}
	var remaining int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM storage_transition_checkpoints WHERE transition_id=$1`, transitionID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 2 {
		t.Fatalf("remaining checkpoints=%d, want 2 after partial deletion", remaining)
	}

	target.failDeleteKey = ""
	if err := service.deleteCheckpointOrphans(t.Context(), transitionID, "public:", "final-run", target, nil); err != nil {
		t.Fatalf("retry orphan deletion: %v", err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM storage_transition_checkpoints WHERE transition_id=$1`, transitionID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("remaining checkpoints=%d, want none after retry", remaining)
	}
}

func TestPostgresCancellationFlushesPageReceiptsForSameRunFencedPass(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	transitionID := uuid.NewString()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM storage_transition_checkpoints WHERE transition_id=$1`, transitionID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM storage_transition_cursors WHERE transition_id=$1`, transitionID)
	})
	source := &memoryStore{identity: "s3|source|public|", objects: map[string][]byte{}}
	for i := range 8 {
		source.objects[fmt.Sprintf("tmdb/%02d.webp", i)] = []byte(fmt.Sprintf("image-%d", i))
	}
	target := &memoryStore{identity: "s3|target|public|", objects: map[string][]byte{}}
	service := sequential(New(pool, &memorySettings{values: map[string]string{}}, nil, source, nil))
	runID := uuid.NewString()
	ctx, cancel := context.WithCancel(t.Context())
	_, _, _, err = service.copyPrefixPass(ctx, transitionID, "public:", source, target, "", func(current, _ int, _ string) {
		if current == 4 {
			cancel()
		}
	}, 0, runID, nil, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("first pass error=%v", err)
	}
	getsAfterCancel := source.gets
	if _, _, _, err := service.copyPrefixPass(t.Context(), transitionID, "public:", source, target, "", func(int, int, string) {}, 0, runID, nil, true); err != nil {
		t.Fatal(err)
	}
	if source.gets-getsAfterCancel != 4 {
		t.Fatalf("resume source gets=%d, want only 4 uncopied objects", source.gets-getsAfterCancel)
	}
}

type recordingJobRepository struct {
	created bool
}

func (r *recordingJobRepository) GetActiveByType(context.Context, string) (*models.AdminJob, error) {
	return nil, adminjob.ErrJobNotFound
}

func TestRepointPrivateArtifactsPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	var userID int
	if err := pool.QueryRow(t.Context(), `SELECT id FROM users ORDER BY id LIMIT 1`).Scan(&userID); err != nil {
		t.Skipf("database has no fixture user: %v", err)
	}
	reportID := uuid.New()
	jobID := "storage-transition-repoint-" + uuid.NewString()
	shortID := "SILO-" + strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", ""))[:12]
	oldBucket := "old-private-" + uuid.NewString()
	newBucket := "new-private-" + uuid.NewString()
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO client_diagnostic_reports
			(id, short_id, user_id, state, captured_at, report_type, platform, app_version, manifest, blob_bucket, blob_key)
		VALUES ($1, $2, $3, 'ready', now(), 'manual', 'android', 'test', '{}', $4, 'diagnostics/test')`, reportID, shortID, userID, oldBucket); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO admin_jobs (id, job_type, status, created_by_user_id, artifact_bucket, artifact_key)
		VALUES ($1, 'catalog_export', 'completed', $2, $3, 'catalog/test')`, jobID, userID, oldBucket); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM client_diagnostic_reports WHERE id=$1`, reportID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM admin_jobs WHERE id=$1`, jobID)
	})

	service := &Service{pool: pool}
	if err := service.repointPrivateArtifacts(t.Context(), oldBucket, newBucket); err != nil {
		t.Fatal(err)
	}
	var reportBucket, jobBucket string
	if err := pool.QueryRow(t.Context(), `SELECT blob_bucket FROM client_diagnostic_reports WHERE id=$1`, reportID).Scan(&reportBucket); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT artifact_bucket FROM admin_jobs WHERE id=$1`, jobID).Scan(&jobBucket); err != nil {
		t.Fatal(err)
	}
	if reportBucket != newBucket || jobBucket != newBucket {
		t.Fatalf("buckets after relocation: diagnostic=%q catalog=%q", reportBucket, jobBucket)
	}
}

// A local root records the "local" bucket. Moving it into private S3 must
// repoint those rows under every policy, since an S3 reader would take "local"
// as a real bucket, and leave rows that name any other bucket alone.
func TestFinalizeCommittedRepointsLocalArtifactsPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	var userID int
	if err := pool.QueryRow(t.Context(), `SELECT id FROM users ORDER BY id LIMIT 1`).Scan(&userID); err != nil {
		t.Skipf("database has no fixture user: %v", err)
	}
	// The repoint is global by bucket name, and "local" is the one name this
	// test cannot randomize. Refuse to rewrite rows it did not create.
	var foreign int
	if err := pool.QueryRow(t.Context(), `SELECT
		(SELECT count(*) FROM admin_jobs WHERE artifact_bucket=$1) +
		(SELECT count(*) FROM client_diagnostic_reports WHERE blob_bucket=$1)`, blobstore.LocalBucket).Scan(&foreign); err != nil {
		t.Fatal(err)
	}
	if foreign > 0 {
		t.Skipf("database already holds %d rows in the %q bucket", foreign, blobstore.LocalBucket)
	}
	for _, policy := range []string{PolicyPreserveUploads, PolicyMigrateAll} {
		t.Run(policy, func(t *testing.T) {
			newBucket := "new-private-" + uuid.NewString()
			otherBucket := "other-private-" + uuid.NewString()
			localJob := "storage-transition-local-" + uuid.NewString()
			otherJob := "storage-transition-other-" + uuid.NewString()
			for _, row := range []struct{ id, bucket string }{{localJob, blobstore.LocalBucket}, {otherJob, otherBucket}} {
				if _, err := pool.Exec(t.Context(), `
					INSERT INTO admin_jobs (id, job_type, status, created_by_user_id, artifact_bucket, artifact_key)
					VALUES ($1, 'catalog_export', 'completed', $2, $3, 'catalog-seeds/test')`, row.id, userID, row.bucket); err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() {
				_, _ = pool.Exec(context.Background(), `DELETE FROM admin_jobs WHERE id = ANY($1)`, []string{localJob, otherJob})
			})

			source := &memoryStore{identity: "local|/srv/silo", objects: map[string][]byte{}}
			stage := stagedTarget{
				ID: "local-repoint-" + uuid.NewString(), Policy: policy, Phase: transitionPhaseRestartPending,
				SourceIdentity: source.Identity(), TargetIdentity: source.Identity(), TargetPrivateBucket: newBucket,
			}
			raw, err := json.Marshal(stage)
			if err != nil {
				t.Fatal(err)
			}
			service := New(pool, &memorySettings{values: map[string]string{StagedTargetSettingKey: string(raw)}}, nil, source, nil)
			if err := service.FinalizeCommitted(t.Context()); err != nil {
				t.Fatal(err)
			}
			for _, want := range []struct{ id, bucket string }{{localJob, newBucket}, {otherJob, otherBucket}} {
				var bucket string
				if err := pool.QueryRow(t.Context(), `SELECT artifact_bucket FROM admin_jobs WHERE id=$1`, want.id).Scan(&bucket); err != nil {
					t.Fatal(err)
				}
				if bucket != want.bucket {
					t.Fatalf("job %s bucket = %q, want %q", want.id, bucket, want.bucket)
				}
			}
		})
	}
}

func (r *recordingJobRepository) Create(_ context.Context, _ adminjob.CreateJobInput) (*models.AdminJob, error) {
	r.created = true
	return &models.AdminJob{ID: "storage-transition-qa"}, nil
}

func TestStartRejectsMultipleActiveNodesPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	nodeIDs := []string{"storage-transition-qa-node-a", "storage-transition-qa-node-b"}
	for _, nodeID := range nodeIDs {
		if _, err := pool.Exec(t.Context(), `
			INSERT INTO node_heartbeats (node_id, node_type, node_url, updated_at)
			VALUES ($1, 'integrated', 'http://127.0.0.1', now())
			ON CONFLICT (node_id) DO UPDATE SET updated_at = EXCLUDED.updated_at
		`, nodeID); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM node_heartbeats WHERE node_id = ANY($1)`, nodeIDs)
	})

	sourceDir := t.TempDir()
	targetDir := t.TempDir()
	settings := &memorySettings{values: map[string]string{
		"artwork.storage_backend": blobstore.BackendLocal,
		"artwork.local_path":      sourceDir,
	}}
	jobs := &recordingJobRepository{}
	service := New(pool, settings, jobs, &memoryStore{identity: "local|" + sourceDir, objects: map[string][]byte{}}, nil)

	_, _, err = service.Start(t.Context(), 1, StartRequest{
		Policy: PolicyFresh,
		Values: map[string]string{
			"artwork.storage_backend": blobstore.BackendLocal,
			"artwork.local_path":      targetDir,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "only one active Silo node") {
		t.Fatalf("Start error = %v, want active-node rejection", err)
	}
	if jobs.created {
		t.Fatal("transition job was created while two nodes were active")
	}

	if _, err := pool.Exec(t.Context(), `UPDATE node_heartbeats SET updated_at = $2 WHERE node_id = $1`, nodeIDs[1], time.Now().Add(-3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Start(t.Context(), 1, StartRequest{
		Policy: PolicyFresh,
		Values: map[string]string{
			"artwork.storage_backend": blobstore.BackendLocal,
			"artwork.local_path":      targetDir,
		},
	}); err != nil {
		t.Fatalf("Start with one active node: %v", err)
	}
	if !jobs.created {
		t.Fatal("transition job was not created after the second heartbeat became stale")
	}
}
