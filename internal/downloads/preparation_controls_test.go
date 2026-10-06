package downloads

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestPreparationControlsPauseResumeAndCancel(t *testing.T) {
	repo, pool, fileID := newArtifactTestRepo(t)
	ctx := context.Background()
	var mu sync.Mutex
	var changed []string
	var published []*Download
	m := &ArtifactManager{
		repo:      repo,
		downloads: NewRepository(pool),
		owner:     "api-test",
		notify: func(_ context.Context, d *Download) {
			mu.Lock()
			published = append(published, d)
			mu.Unlock()
		},
		prepNotify: func(_ context.Context, e PreparationEvent) {
			mu.Lock()
			changed = append(changed, e.ArtifactID)
			mu.Unlock()
		},
	}
	ensure := func(hash string) string {
		t.Helper()
		row, _, err := repo.EnsureQueued(ctx, newArtifact(t, fileID, hash))
		if err != nil {
			t.Fatal(err)
		}
		return row.ID
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	queued := ensure("hash-ctl-queued")
	running := ensure("hash-ctl-running")
	retrying := ensure("hash-ctl-retrying")
	failed := ensure("hash-ctl-failed")
	ready := ensure("hash-ctl-ready")
	exec(`UPDATE download_artifacts
		SET status = 'running', attempts = 2, lease_owner = 'worker-elsewhere', lease_expires_at = now() + interval '1 minute',
		    started_at = now(), worker_kind = 'node', worker_node_id = 3, worker_name = 'gpu-01',
		    progress_encoded_seconds = 30, progress_duration_seconds = 120, progress_speed = 2, progress_updated_at = now()
		WHERE id = $1`, running)
	exec(`UPDATE download_artifacts SET attempts = 1, next_retry_at = now() + interval '10 minutes' WHERE id = $1`, retrying)
	exec(`UPDATE download_artifacts SET status = 'failed', attempts = 3, completed_at = now() WHERE id = $1`, failed)
	exec(`UPDATE download_artifacts SET status = 'ready', completed_at = now() WHERE id = $1`, ready)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM download_artifacts WHERE id = ANY($1)`, []string{queued, running, retrying, failed, ready})
	})
	seedPreparationRequester(t, pool, fileID, running, StatusPreparing)
	linkRecoveryDownload(t, pool, fileID, queued, StatusPreparing)
	linkRecoveryDownload(t, pool, fileID, ready, StatusReady)

	outcomes := func(results []PreparationResult) map[string]PreparationOutcome {
		out := make(map[string]PreparationOutcome, len(results))
		for _, r := range results {
			out[r.ArtifactID] = r.Outcome
		}
		return out
	}

	// Pause: unfinished jobs pause, a failed one cannot, a finished or
	// unknown one is not found. Repeated ids report once, in request order.
	results, err := m.PausePreparations(ctx, []string{queued, running, retrying, failed, ready, "missing-job", queued})
	if err != nil {
		t.Fatalf("PausePreparations: %v", err)
	}
	wantOrder := []string{queued, running, retrying, failed, ready, "missing-job"}
	var gotOrder []string
	for _, r := range results {
		gotOrder = append(gotOrder, r.ArtifactID)
	}
	if !reflect.DeepEqual(gotOrder, wantOrder) {
		t.Fatalf("result order = %v, want %v", gotOrder, wantOrder)
	}
	want := map[string]PreparationOutcome{
		queued: PreparationApplied, running: PreparationApplied, retrying: PreparationApplied,
		failed: PreparationNotApplicable, ready: PreparationNotFound, "missing-job": PreparationNotFound,
	}
	if got := outcomes(results); !reflect.DeepEqual(got, want) {
		t.Fatalf("pause outcomes = %v, want %v", got, want)
	}
	if len(changed) != 3 {
		t.Fatalf("pause announced %v, want the three paused jobs", changed)
	}

	// The running attempt lost its lease and live state, its attempt was
	// refunded, and it cannot be claimed during the stop grace.
	var status string
	var attempts int
	var leaseOwner *string
	var workerKind string
	var startedAt, progressAt *time.Time
	var graceLeft float64
	if err := pool.QueryRow(ctx, `SELECT status, attempts, lease_owner, worker_kind, started_at, progress_updated_at,
		EXTRACT(EPOCH FROM lease_expires_at - now()) FROM download_artifacts WHERE id = $1`, running).
		Scan(&status, &attempts, &leaseOwner, &workerKind, &startedAt, &progressAt, &graceLeft); err != nil {
		t.Fatal(err)
	}
	if status != ArtifactQueued || attempts != 1 || leaseOwner != nil || workerKind != "" || startedAt != nil || progressAt != nil {
		t.Fatalf("paused running job = status %q attempts %d lease %v worker %q started %v progress %v", status, attempts, leaseOwner, workerKind, startedAt, progressAt)
	}
	if graceLeft < pauseClaimGrace.Seconds()-5 {
		t.Fatalf("paused running job claimable in %.0fs, want about %v", graceLeft, pauseClaimGrace)
	}
	// The worker that lost the job is fenced out of every write.
	if ok, err := repo.Heartbeat(ctx, running, "worker-elsewhere", time.Minute); err != nil || ok {
		t.Fatalf("stale Heartbeat = (%v, %v), want fenced", ok, err)
	}
	if applied, err := repo.MarkReady(ctx, running, "worker-elsewhere", "/tmp/x", 0, "", "", "", 10, nil); err != nil || applied {
		t.Fatalf("stale MarkReady = (%v, %v), want fenced", applied, err)
	}

	results, err = m.PausePreparations(ctx, []string{queued})
	if err != nil || len(results) != 1 || results[0].Outcome != PreparationUnchanged {
		t.Fatalf("second pause = (%+v, %v), want unchanged", results, err)
	}

	list, err := NewPreparationReader(pool, nil).List(ctx, 500)
	if err != nil {
		t.Fatal(err)
	}
	if list.Counts.Paused < 3 {
		t.Fatalf("paused count = %d, want at least 3", list.Counts.Paused)
	}
	for _, item := range list.Items {
		switch item.ArtifactID {
		case queued, running, retrying:
			if item.State != PreparationPaused || item.PausedAt == nil || item.QueuePosition != 0 || item.NextRetryAt != nil {
				t.Fatalf("paused item = %+v", item)
			}
		}
	}

	// No worker claims a paused job, even once its backoff and stop grace
	// have elapsed.
	exec(`UPDATE download_artifacts SET next_retry_at = now() - interval '1 second' WHERE id = $1`, retrying)
	exec(`UPDATE download_artifacts SET lease_expires_at = now() - interval '1 second' WHERE id = $1`, running)
	for {
		claim, err := repo.ClaimNext(ctx, "worker-drain", time.Minute)
		if errors.Is(err, ErrNoArtifactJob) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if claim.ID == queued || claim.ID == running || claim.ID == retrying {
			t.Fatalf("claimed paused job %s", claim.ID)
		}
		_, _ = pool.Exec(ctx, `UPDATE download_artifacts SET status = 'failed', lease_owner = NULL, completed_at = now() WHERE id = $1`, claim.ID)
	}
	exec(`UPDATE download_artifacts SET next_retry_at = now() + interval '10 minutes' WHERE id = $1`, retrying)
	exec(`UPDATE download_artifacts SET lease_expires_at = now() + interval '1 minute' WHERE id = $1`, running)

	// Resume returns each job to the state it was paused in.
	results, err = m.ResumePreparations(ctx, []string{queued, retrying, running, failed, ready})
	if err != nil {
		t.Fatalf("ResumePreparations: %v", err)
	}
	want = map[string]PreparationOutcome{
		queued: PreparationApplied, retrying: PreparationApplied, running: PreparationApplied,
		failed: PreparationNotApplicable, ready: PreparationNotFound,
	}
	if got := outcomes(results); !reflect.DeepEqual(got, want) {
		t.Fatalf("resume outcomes = %v, want %v", got, want)
	}
	results, err = m.ResumePreparations(ctx, []string{queued})
	if err != nil || len(results) != 1 || results[0].Outcome != PreparationUnchanged {
		t.Fatalf("second resume = (%+v, %v), want unchanged", results, err)
	}
	list, err = NewPreparationReader(pool, nil).List(ctx, 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range list.Items {
		switch item.ArtifactID {
		case queued:
			if item.State != PreparationQueued || item.QueuePosition < 1 {
				t.Fatalf("resumed queued item = %+v", item)
			}
		case retrying:
			if item.State != PreparationRetrying {
				t.Fatalf("resumed retrying item = %+v", item)
			}
		case running:
			// Resumed inside its stop grace: listed as queued, not as a retry.
			if item.State != PreparationQueued || item.NextRetryAt != nil {
				t.Fatalf("resumed running item = %+v", item)
			}
		}
	}
	// The stop grace still keeps workers off the resumed job.
	exec(`UPDATE download_artifacts SET status = 'failed', completed_at = now() WHERE id = $1`, queued)
	for {
		claim, err := repo.ClaimNext(ctx, "worker-drain", time.Minute)
		if errors.Is(err, ErrNoArtifactJob) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if claim.ID == running {
			t.Fatal("claimed a job inside its stop grace")
		}
		_, _ = pool.Exec(ctx, `UPDATE download_artifacts SET status = 'failed', lease_owner = NULL, completed_at = now() WHERE id = $1`, claim.ID)
	}

	// Cancel deletes unfinished and failed jobs and fails their waiting
	// downloads; a finished job and its downloads are left alone.
	changed, published = nil, nil
	results, err = m.CancelPreparations(ctx, []string{queued, running, failed, ready, "missing-job"})
	if err != nil {
		t.Fatalf("CancelPreparations: %v", err)
	}
	want = map[string]PreparationOutcome{
		queued: PreparationApplied, running: PreparationApplied, failed: PreparationApplied,
		ready: PreparationNotFound, "missing-job": PreparationNotFound,
	}
	if got := outcomes(results); !reflect.DeepEqual(got, want) {
		t.Fatalf("cancel outcomes = %v, want %v", got, want)
	}
	var remaining int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM download_artifacts WHERE id = ANY($1)`, []string{queued, running, failed}).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("canceled jobs remaining = (%d, %v)", remaining, err)
	}
	if len(published) != 2 {
		t.Fatalf("published %d download changes, want the two waiting downloads", len(published))
	}
	for _, d := range published {
		if d.Status != StatusFailed || d.ErrorMessage != PreparationCanceledMessage {
			t.Fatalf("canceled download = %+v", d)
		}
	}
	var readyStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM downloads WHERE artifact_id = $1`, ready).Scan(&readyStatus); err != nil || readyStatus != StatusReady {
		t.Fatalf("ready job's download = (%q, %v)", readyStatus, err)
	}
	if len(changed) != 3 {
		t.Fatalf("cancel announced %v, want the three canceled jobs", changed)
	}
	if _, err := repo.GetByID(ctx, ready); err != nil {
		t.Fatalf("ready job after cancel: %v", err)
	}
}

func TestDownloadLinkedToCanceledPreparationFails(t *testing.T) {
	repo, pool, fileID := newArtifactTestRepo(t)
	ctx := context.Background()
	m := &ArtifactManager{repo: repo, downloads: NewRepository(pool), owner: "api-test"}
	row, _, err := repo.EnsureQueued(ctx, newArtifact(t, fileID, "hash-ctl-canceled-link"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.CancelPreparations(ctx, []string{row.ID}); err != nil {
		t.Fatal(err)
	}

	// A create that read the job before the cancel committed links its new
	// download to the deleted id.
	create := func(id string) *Download {
		t.Helper()
		d := &Download{
			ID: id, UserID: 0, MediaFileID: fileID, ContentID: "ctl-content", Kind: KindQueued,
			Status: StatusPreparing, Format: FormatTranscode, ArtifactID: row.ID,
			CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}
		var userID int
		if err := pool.QueryRow(ctx, `INSERT INTO users (username, role, download_allowed) VALUES ($1, 'user', true) RETURNING id`, id).Scan(&userID); err != nil {
			t.Fatal(err)
		}
		d.UserID = userID
		if err := m.downloads.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = pool.Exec(ctx, `DELETE FROM downloads WHERE id = $1`, id)
			_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
		})
		return d
	}
	suffix := time.Now().UnixNano()

	confirmed, err := m.downloads.ConfirmArtifactLink(ctx, create(fmt.Sprintf("ctl-confirm-%d", suffix)))
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.Status != StatusFailed || confirmed.ErrorMessage != PreparationCanceledMessage {
		t.Fatalf("confirmed download = %q %q, want failed with the cancel message", confirmed.Status, confirmed.ErrorMessage)
	}

	// Reconciliation catches one that was never confirmed.
	stranded := create(fmt.Sprintf("ctl-stranded-%d", suffix))
	_, failed, err := m.downloads.ReconcileLinkedDownloads(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range failed {
		if d.ID == stranded.ID {
			found = d.Status == StatusFailed && d.ErrorMessage == PreparationCanceledMessage
		}
	}
	if !found {
		t.Fatalf("reconcile did not fail the stranded download: %+v", failed)
	}
}

func TestStopLocalAttemptsCancelsOnlyRunningAttemptsOnThisReplica(t *testing.T) {
	m := &ArtifactManager{}
	runningCtx, cancelRunning := context.WithCancel(context.Background())
	defer cancelRunning()
	queuedCtx, cancelQueued := context.WithCancel(context.Background())
	defer cancelQueued()
	untrack := m.trackLocalAttempt("job-running", cancelRunning)
	m.trackLocalAttempt("job-queued", cancelQueued)

	m.stopLocalAttempts([]stoppedArtifact{
		{ID: "job-running", Running: true},
		{ID: "job-queued", Running: false},
		{ID: "job-elsewhere", Running: true},
	})
	if runningCtx.Err() == nil {
		t.Fatal("the running attempt on this replica was not stopped")
	}
	if queuedCtx.Err() != nil {
		t.Fatal("an attempt reported as not running was stopped")
	}

	untrack()
	m.mu.Lock()
	_, tracked := m.localAttempts["job-running"]
	m.mu.Unlock()
	if tracked {
		t.Fatal("a finished attempt is still tracked")
	}
}
