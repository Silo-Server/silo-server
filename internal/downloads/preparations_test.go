package downloads

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestArtifactLiveStateIsFencedAndResetOnClaim(t *testing.T) {
	repo, pool, fileID := newArtifactTestRepo(t)
	ctx := context.Background()
	row, _, err := repo.EnsureQueued(ctx, newArtifact(t, fileID, "hash-live-state"))
	if err != nil {
		t.Fatal(err)
	}
	claim, err := repo.ClaimNext(ctx, "worker-1", time.Minute)
	if err != nil || claim.ID != row.ID {
		t.Fatalf("claim = (%+v, %v)", claim, err)
	}
	if claim.StartedAt == nil || claim.WorkerKind != "" || claim.Progress != nil {
		t.Fatalf("fresh claim live state = started %v worker %q progress %+v", claim.StartedAt, claim.WorkerKind, claim.Progress)
	}

	node := 7
	if applied, err := repo.RecordWorker(ctx, row.ID, "worker-1", WorkerNode, &node, "gpu-01"); err != nil || !applied {
		t.Fatalf("RecordWorker = (%v, %v)", applied, err)
	}
	if applied, err := repo.RecordProgress(ctx, row.ID, "worker-1", ArtifactProgress{EncodedSeconds: 30, DurationSeconds: 120, Speed: 2.5}); err != nil || !applied {
		t.Fatalf("RecordProgress = (%v, %v)", applied, err)
	}
	// Another worker's writes are fenced out.
	if applied, err := repo.RecordProgress(ctx, row.ID, "worker-2", ArtifactProgress{EncodedSeconds: 99}); err != nil || applied {
		t.Fatalf("foreign RecordProgress = (%v, %v), want fenced", applied, err)
	}
	if applied, err := repo.RecordProgressUnavailable(ctx, row.ID, "worker-2"); err != nil || applied {
		t.Fatalf("foreign RecordProgressUnavailable = (%v, %v), want fenced", applied, err)
	}
	got, err := repo.GetByID(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.WorkerKind != WorkerNode || got.WorkerNodeID == nil || *got.WorkerNodeID != 7 || got.WorkerName != "gpu-01" {
		t.Fatalf("worker = %q %v %q", got.WorkerKind, got.WorkerNodeID, got.WorkerName)
	}
	if got.Progress == nil || got.Progress.EncodedSeconds != 30 || got.Progress.DurationSeconds != 120 || got.Progress.Speed != 2.5 {
		t.Fatalf("progress = %+v", got.Progress)
	}

	// Falling back to the API server replaces the worker and clears progress.
	if applied, err := repo.RecordWorker(ctx, row.ID, "worker-1", WorkerServer, nil, "api-1"); err != nil || !applied {
		t.Fatalf("RecordWorker server = (%v, %v)", applied, err)
	}
	if applied, err := repo.RecordProgressUnavailable(ctx, row.ID, "worker-1"); err != nil || !applied {
		t.Fatalf("RecordProgressUnavailable = (%v, %v)", applied, err)
	}
	got, _ = repo.GetByID(ctx, row.ID)
	if got.WorkerKind != WorkerServer || got.WorkerNodeID != nil || got.Progress != nil || !got.ProgressUnavailable {
		t.Fatalf("after fallback = %q %v %+v unavailable=%v", got.WorkerKind, got.WorkerNodeID, got.Progress, got.ProgressUnavailable)
	}

	// A retry claim starts the next attempt from a clean slate.
	if _, applied, err := repo.MarkFailedOrRetry(ctx, row.ID, "worker-1", "boom", time.Second); err != nil || !applied {
		t.Fatalf("MarkFailedOrRetry = (%v, %v)", applied, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE download_artifacts SET next_retry_at = now() - interval '1 second' WHERE id = $1`, row.ID); err != nil {
		t.Fatal(err)
	}
	again, err := repo.ClaimNext(ctx, "worker-3", time.Minute)
	if err != nil || again.ID != row.ID {
		t.Fatalf("reclaim = (%+v, %v)", again, err)
	}
	if again.WorkerKind != "" || again.WorkerNodeID != nil || again.WorkerName != "" || again.Progress != nil || again.ProgressUnavailable {
		t.Fatalf("second claim kept live state: %+v", again)
	}
}

func TestPreparationReaderListsStatesInOrderWithRequesters(t *testing.T) {
	repo, pool, fileID := newArtifactTestRepo(t)
	ctx := context.Background()
	ensure := func(hash string) *Artifact {
		t.Helper()
		row, _, err := repo.EnsureQueued(ctx, newArtifact(t, fileID, hash))
		if err != nil {
			t.Fatal(err)
		}
		return row
	}

	running := ensure("hash-prep-running")
	claim, err := repo.ClaimNext(ctx, "worker-1", time.Minute)
	if err != nil || claim.ID != running.ID {
		t.Fatalf("claim = (%+v, %v)", claim, err)
	}
	if _, err := repo.RecordWorker(ctx, running.ID, "worker-1", WorkerServer, nil, "api-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RecordProgress(ctx, running.ID, "worker-1", ArtifactProgress{EncodedSeconds: 60, DurationSeconds: 240, Speed: 4}); err != nil {
		t.Fatal(err)
	}

	queuedFirst := ensure("hash-prep-queued-1")
	queuedSecond := ensure("hash-prep-queued-2")
	retrying := ensure("hash-prep-retrying")
	failedRecent := ensure("hash-prep-failed-recent")
	failedOld := ensure("hash-prep-failed-old")
	ready := ensure("hash-prep-ready")
	if _, err := pool.Exec(ctx, `UPDATE download_artifacts SET created_at = now() - interval '1 hour' WHERE id = $1`, queuedFirst.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE download_artifacts
		SET attempts = 1, error_message = 'node went away', next_retry_at = now() + interval '1 minute'
		WHERE id = $1`, retrying.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE download_artifacts
		SET status = 'failed', attempts = 3, error_message = 'no space', completed_at = now() - interval '1 hour'
		WHERE id = $1`, failedRecent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE download_artifacts
		SET status = 'failed', attempts = 3, completed_at = now() - interval '2 days'
		WHERE id = $1`, failedOld.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE download_artifacts SET status = 'ready', completed_at = now() WHERE id = $1`, ready.ID); err != nil {
		t.Fatal(err)
	}

	userID, deviceID := seedPreparationRequester(t, pool, fileID, running.ID, "preparing")
	// A one-off web download stores no profile or device.
	linkRecoveryDownload(t, pool, fileID, queuedFirst.ID, "preparing")
	names := func(_ context.Context, id int) (map[string]string, error) {
		if id != userID {
			return nil, errors.New("unexpected user")
		}
		return map[string]string{"profile-prep": "Alex"}, nil
	}
	list, err := NewPreparationReader(pool, names).List(ctx, 500)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if list.Counts.Running < 1 || list.Counts.Queued < 2 || list.Counts.Retrying < 1 || list.Counts.FailedRecent < 1 {
		t.Fatalf("counts = %+v", list.Counts)
	}
	byID := map[string]Preparation{}
	var order []string
	for _, item := range list.Items {
		byID[item.ArtifactID] = item
		order = append(order, item.ArtifactID)
	}
	if _, ok := byID[failedOld.ID]; ok {
		t.Fatal("a failure older than the window is listed")
	}
	if _, ok := byID[ready.ID]; ok {
		t.Fatal("a ready artifact is listed")
	}
	position := func(id string) int {
		for i, candidate := range order {
			if candidate == id {
				return i
			}
		}
		t.Fatalf("%s not listed", id)
		return -1
	}
	want := []string{running.ID, retrying.ID, queuedFirst.ID, queuedSecond.ID, failedRecent.ID}
	for i := 1; i < len(want); i++ {
		if position(want[i-1]) >= position(want[i]) {
			t.Fatalf("order = %v, want %v as a subsequence", order, want)
		}
	}

	r := byID[running.ID]
	if r.State != PreparationRunning || r.WorkerKind != WorkerServer || r.WorkerName != "api-1" || r.Progress == nil || r.Progress.EncodedSeconds != 60 {
		t.Fatalf("running = %+v", r)
	}
	if len(r.Requesters) != 1 || r.Requesters[0].UserID != userID || r.Requesters[0].DeviceID != deviceID ||
		r.Requesters[0].DeviceName != "Test iPhone" || r.Requesters[0].ProfileName != "Alex" || r.Requesters[0].Status != "preparing" {
		t.Fatalf("requesters = %+v", r.Requesters)
	}
	if web := byID[queuedFirst.ID].Requesters; len(web) != 1 || web[0].ProfileID != "" || web[0].DeviceID != "" || web[0].Username == "" {
		t.Fatalf("web requesters = %+v, want one account-level row", web)
	}
	if q := byID[queuedFirst.ID]; q.State != PreparationQueued || q.QueuePosition < 1 || byID[queuedSecond.ID].QueuePosition != q.QueuePosition+1 {
		t.Fatalf("queue positions = %d, %d", q.QueuePosition, byID[queuedSecond.ID].QueuePosition)
	}
	if rt := byID[retrying.ID]; rt.State != PreparationRetrying || rt.NextRetryAt == nil || rt.QueuePosition != 0 || rt.ErrorMessage != "node went away" {
		t.Fatalf("retrying = %+v", rt)
	}
	if f := byID[failedRecent.ID]; f.State != PreparationFailed || f.CompletedAt == nil || f.NextRetryAt != nil || f.Progress != nil {
		t.Fatalf("failed = %+v", f)
	}

	capped, err := NewPreparationReader(pool, nil).List(ctx, 1)
	if err != nil || len(capped.Items) != 1 || capped.Counts != list.Counts {
		t.Fatalf("capped list = %+v (%v), want one item with full counts %+v", capped, err, list.Counts)
	}
}

// seedPreparationRequester links a managed device download to artifactID.
func seedPreparationRequester(t *testing.T, pool *pgxpool.Pool, fileID int, artifactID, status string) (int, string) {
	t.Helper()
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	var userID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (username, role, download_allowed) VALUES ($1, 'user', true) RETURNING id`,
		fmt.Sprintf("prep-user-%d", suffix),
	).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	deviceID := fmt.Sprintf("prep-device-%d", suffix)
	downloadID := fmt.Sprintf("prep-download-%d", suffix)
	repo := NewRepository(pool)
	if err := repo.EnsureDevice(ctx, userID, "profile-prep", deviceID, "Test iPhone", "ios"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Create(ctx, &Download{
		ID: downloadID, UserID: userID, ProfileID: "profile-prep", DeviceID: deviceID, MediaFileID: fileID,
		ContentID: "prep-content", Kind: KindQueued, Status: status,
		Format: FormatTranscode, ArtifactID: artifactID,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM downloads WHERE id = $1`, downloadID)
		_, _ = pool.Exec(ctx, `DELETE FROM user_devices WHERE user_id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
	})
	return userID, deviceID
}

func TestDownloadRequesterChangesNotifyPreparationListeners(t *testing.T) {
	repo, pool, fileID := newArtifactTestRepo(t)
	ctx := context.Background()
	row, _, err := repo.EnsureQueued(ctx, newArtifact(t, fileID, "hash-requester-events"))
	if err != nil {
		t.Fatal(err)
	}
	var events []PreparationEvent
	m := NewArtifactManager(repo, NewRepository(pool), nil, &recordingEncodePreparer{}, "api-1", nil, nil)
	m.SetPreparationNotifier(func(_ context.Context, event PreparationEvent) { events = append(events, event) })
	svc := &Service{repo: NewRepository(pool), artifacts: m}

	linkRecoveryDownload(t, pool, fileID, row.ID, StatusPreparing)
	var downloadID string
	var userID int
	if err := pool.QueryRow(ctx, `SELECT id, user_id FROM downloads WHERE artifact_id = $1`, row.ID).Scan(&downloadID, &userID); err != nil {
		t.Fatal(err)
	}
	d, err := svc.repo.GetByID(ctx, downloadID)
	if err != nil {
		t.Fatal(err)
	}
	svc.confirmArtifactLink(ctx, d)
	if len(events) != 1 || events[0].Name != PreparationChangedEvent || events[0].ArtifactID != row.ID {
		t.Fatalf("events after linking = %+v", events)
	}

	if err := svc.Delete(ctx, userID, "", "", downloadID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(events) != 2 || events[1].ArtifactID != row.ID {
		t.Fatalf("events after delete = %+v", events)
	}

	// A download of a ready artifact is not on the preparation list.
	svc.notifyPreparationRequesters(ctx, &Download{ArtifactID: row.ID, Status: StatusReady})
	if len(events) != 2 {
		t.Fatalf("a ready download notified listeners: %+v", events)
	}
}
