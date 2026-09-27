package historyimport

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/jackc/pgx/v5/pgxpool"
)

func queueRunnerRepository(t *testing.T) *Repository {
	t.Helper()
	repo := queueRunnerRepositoryFromPool(t, queueTestPool(t, false))
	// Claim validation also references personal-import targets. Keep the admin
	// fixture independent of application tables in the public schema.
	if _, err := repo.pool.Exec(t.Context(), `CREATE TABLE users(id integer PRIMARY KEY);
 CREATE TABLE user_profiles(user_id integer REFERENCES users(id),id text,PRIMARY KEY(user_id,id));
 INSERT INTO users VALUES(1); INSERT INTO user_profiles VALUES(1,'p')`); err != nil {
		t.Fatal(err)
	}
	return repo
}

func queueRunnerRepositoryFromPool(t *testing.T, pool *pgxpool.Pool) *Repository {
	t.Helper()
	_, err := pool.Exec(t.Context(), `CREATE TABLE history_import_sources(id integer PRIMARY KEY,name text NOT NULL DEFAULT 'Test',source_type text NOT NULL DEFAULT 'emby',base_url text NOT NULL DEFAULT 'http://example.test',system_id text,enabled boolean NOT NULL DEFAULT true,sort_order integer NOT NULL DEFAULT 0,admin_token text,revision bigint NOT NULL DEFAULT 1,created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now());
 ALTER TABLE history_import_user_mappings ADD COLUMN source_id integer NOT NULL DEFAULT 1,ADD COLUMN silo_user_id integer NOT NULL DEFAULT 1,ADD COLUMN silo_profile_id text NOT NULL DEFAULT 'p',ADD COLUMN external_user_id text NOT NULL DEFAULT 'external',ADD COLUMN external_user_name text NOT NULL DEFAULT 'External',ADD COLUMN revision bigint NOT NULL DEFAULT 1,ADD COLUMN last_imported_at timestamptz,ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now()`)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := secret.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(pool, cipher)
	token, err := repo.encryptSourceAdminToken(1, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(t.Context(), `INSERT INTO history_import_sources(id,admin_token) VALUES(1,$1)`, token); err != nil {
		t.Fatal(err)
	}
	return repo
}

func TestQueueClaimGenerationCancellationAndLegacyWriters(t *testing.T) {
	repo := queueRunnerRepository(t)
	ctx := t.Context()
	run, err := repo.EnqueueAdminRun(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	claimed, claim, err := repo.claimAdminRun(ctx)
	if err != nil || claimed == nil || claimed.ID != run.ID || claim.Generation != 1 {
		t.Fatalf("claim=%+v %+v %v", claimed, claim, err)
	}
	if err = repo.validateRunClaim(ctx, claim); err != nil {
		t.Fatal(err)
	}
	wrong := claim
	wrong.Generation++
	for name, write := range map[string]func() error{
		"legacy start":     func() error { return repo.MarkRunStarted(ctx, run.ID) },
		"legacy heartbeat": func() error { return repo.TouchRunHeartbeat(ctx, run.ID) },
		"legacy progress":  func() error { return repo.UpdateRunProgress(ctx, run.ID, ExecutionSummary{Fetched: 99}) },
		"legacy complete":  func() error { return repo.CompleteRun(ctx, run.ID, ExecutionSummary{}) },
		"legacy fail":      func() error { return repo.FailRun(ctx, run.ID, ExecutionSummary{}, "old writer") },
		"wrong generation": func() error { return repo.completeRun(ctx, wrong, ExecutionSummary{}) },
	} {
		if err = write(); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if err = repo.CancelRunIfActive(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetRunByID(ctx, run.ID)
	if err != nil || got.Status != RunStatusRunning || !got.CancelRequested {
		t.Fatalf("cancel must await worker: %+v %v", got, err)
	}
	if err = repo.validateRunClaim(ctx, claim); !errors.Is(err, ErrRunCancellationRequested) {
		t.Fatalf("cancel=%v", err)
	}
	if err = repo.completeRun(ctx, claim, ExecutionSummary{}); err == nil {
		t.Fatal("completion erased cancellation")
	}
	if err = repo.acknowledgeRunCancellation(ctx, claim); err != nil {
		t.Fatal(err)
	}
	if err = repo.failRun(ctx, claim, ExecutionSummary{}, "late failure"); err == nil {
		t.Fatal("terminal rewritten")
	}
	next, err := repo.EnqueueAdminRun(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.CancelRunIfActive(ctx, next.ID); err != nil {
		t.Fatal(err)
	}
	if got, _, err := repo.claimAdminRun(ctx); err != nil || got != nil {
		t.Fatalf("claimed queued cancellation=%+v %v", got, err)
	}
}

func TestQueueTwoNodesAdmissionAndClaim(t *testing.T) {
	repo := queueRunnerRepository(t)
	ctx := t.Context()
	var wg sync.WaitGroup
	var accepted atomic.Int32
	for range 12 {
		wg.Go(func() {
			_, err := repo.EnqueueAdminRun(ctx, 1)
			if err == nil {
				accepted.Add(1)
			} else if !errors.Is(err, ErrActiveRunExists) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("accepted=%d", accepted.Load())
	}
	var claimed atomic.Int32
	for range 2 {
		wg.Go(func() {
			run, _, err := repo.claimAdminRun(ctx)
			if err != nil {
				t.Error(err)
			}
			if run != nil {
				claimed.Add(1)
			}
		})
	}
	wg.Wait()
	if claimed.Load() != 1 {
		t.Fatalf("claims=%d", claimed.Load())
	}
}

func TestQueueRecoversExpiredCancellation(t *testing.T) {
	repo := queueRunnerRepository(t)
	ctx := t.Context()
	run, err := repo.EnqueueAdminRun(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	_, claim, err := repo.claimAdminRun(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.updateRunProgress(ctx, claim, ExecutionSummary{Fetched: 7}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CancelRunIfActive(ctx, run.ID); err != nil {
		t.Fatal(err)
	}

	// A restarted service must allow a worker with a live lease to acknowledge.
	service := &Service{repo: repo}
	if err := service.reconcileStaleRuns(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetRunByID(ctx, run.ID)
	if err != nil || got.Status != RunStatusRunning || !got.CancelRequested {
		t.Fatalf("live cancellation=%+v err=%v", got, err)
	}
	if _, err := repo.pool.Exec(ctx, `UPDATE history_import_runs SET last_heartbeat_at=now()-interval '2 minutes' WHERE id=$1`, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.reconcileStaleRuns(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err = repo.GetRunByID(ctx, run.ID)
	if err != nil || got.Status != RunStatusCancelled || got.CompletedAt == nil || got.Fetched != 7 {
		t.Fatalf("expired cancellation=%+v err=%v", got, err)
	}
	if err := repo.completeRun(ctx, claim, ExecutionSummary{}); err == nil {
		t.Fatal("late worker completed a canceled run")
	}
	if _, err := repo.EnqueueAdminRun(ctx, 1); err != nil {
		t.Fatalf("canceled run still blocks its mapping: %v", err)
	}
}

func TestQueueConfigurationChangesAndStaleWorkAreNotReplayed(t *testing.T) {
	repo := queueRunnerRepository(t)
	ctx := t.Context()
	for _, mutation := range []string{
		`UPDATE history_import_sources SET revision=revision+1 WHERE id=1`,
		`UPDATE history_import_user_mappings SET revision=revision+1,silo_profile_id='other' WHERE id=1`,
	} {
		run, err := repo.EnqueueAdminRun(ctx, 1)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = repo.pool.Exec(ctx, mutation); err != nil {
			t.Fatal(err)
		}
		_, claim, err := repo.claimAdminRun(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = repo.validateRunClaim(ctx, claim); !errors.Is(err, ErrRunConfigurationChanged) {
			t.Fatalf("config=%v", err)
		}
		if err = repo.failRun(ctx, claim, ExecutionSummary{}, ErrRunConfigurationChanged.Error()); err != nil {
			t.Fatal(err)
		}
		got, err := repo.GetRunByID(ctx, run.ID)
		if err != nil || got.Status != RunStatusFailed {
			t.Fatalf("failed=%+v %v", got, err)
		}
	}
	run, err := repo.EnqueueAdminRun(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	_, claim, err := repo.claimAdminRun(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.pool.Exec(ctx, `UPDATE history_import_runs SET last_heartbeat_at=now()-interval '2 minutes' WHERE id=$1`, run.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := repo.FailStaleRuns(ctx, time.Now().Add(-time.Minute), staleRunInterruptedMessage); err != nil || n != 1 {
		t.Fatalf("stale=%d %v", n, err)
	}
	if err = repo.completeRun(ctx, claim, ExecutionSummary{}); err == nil {
		t.Fatal("late completion rewrote stale failure")
	}
	if got, _, err := repo.claimAdminRun(ctx); err != nil || got != nil {
		t.Fatalf("replayed stale=%+v %v", got, err)
	}
}

func TestQueuePersistedIntentRestartAndCapacity(t *testing.T) {
	repo := queueRunnerRepository(t)
	ctx := t.Context()
	var fetched atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetched.Add(1)
		_, _ = w.Write([]byte(`{"Items":[],"TotalRecordCount":0}`))
	}))
	defer upstream.Close()
	if _, err := repo.pool.Exec(ctx, `UPDATE history_import_sources SET base_url=$1 WHERE id=1`, upstream.URL); err != nil {
		t.Fatal(err)
	}
	// No Service exists at acceptance: simulates process death after persistence,
	// before an in-memory wake or provider construction.
	run, err := repo.EnqueueAdminRun(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{repo: repo, bgContext: ctx, emby: NewEmbyClient(), runSemaphore: make(chan struct{}, 1), queueWake: make(chan struct{}, 1), runCancels: make(map[string]context.CancelFunc)}
	service.runSemaphore <- struct{}{}
	service.dispatchQueuedRuns()
	got, err := repo.GetRunByID(ctx, run.ID)
	if err != nil || got.Status != RunStatusQueued || fetched.Load() != 0 {
		t.Fatalf("capacity claimed=%+v %v fetches=%d", got, err, fetched.Load())
	}
	<-service.runSemaphore
	service.dispatchQueuedRuns()
	deadline, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		got, err = repo.GetRunByID(deadline, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == RunStatusCompleted {
			break
		}
		if got.Status == RunStatusFailed {
			t.Fatalf("restart failed: %+v", got)
		}
	}
	if fetched.Load() != 3 {
		t.Fatalf("upstream calls=%d", fetched.Load())
	}
}

func TestQueueBulkLimitAndOrderedOutcomes(t *testing.T) {
	repo := queueRunnerRepository(t)
	ctx := t.Context()
	service := &Service{repo: repo}
	if _, err := repo.EnqueueAdminRun(ctx, 1); err != nil {
		t.Fatal(err)
	}
	result, err := service.BulkCreateAdminRuns(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Outcomes) != 2 || result.Outcomes[0].MappingID != 1 || result.Outcomes[0].Status != "active" || result.Outcomes[1].Status != "accepted" {
		t.Fatalf("outcomes=%+v", result)
	}
	for id := 3; id <= 201; id++ {
		if _, err = repo.pool.Exec(ctx, `INSERT INTO history_import_user_mappings(id,external_user_name) VALUES($1,$2)`, id, fmt.Sprint(id)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = service.BulkCreateAdminRuns(ctx, 1); !errors.Is(err, ErrBulkRunTooLarge) {
		t.Fatalf("oversize=%v", err)
	}
	var count int
	if err = repo.pool.QueryRow(ctx, `SELECT count(*) FROM history_import_runs`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("oversize effects=%d %v", count, err)
	}
}

type heldQueueProvider struct {
	started chan struct{}
	release chan struct{}
}

func (p heldQueueProvider) Fetch(ctx context.Context) ([]Record, []string, error) {
	close(p.started)
	select {
	case <-p.release:
		return []Record{}, nil, nil
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
}

func TestQueueWorkerAcknowledgesRemoteCancelAndConfigurationChange(t *testing.T) {
	for _, change := range []string{"cancel", "source", "mapping-delete"} {
		t.Run(change, func(t *testing.T) {
			repo := queueRunnerRepository(t)
			ctx := t.Context()
			run, err := repo.EnqueueAdminRun(ctx, 1)
			if err != nil {
				t.Fatal(err)
			}
			claimed, claim, err := repo.claimAdminRun(ctx)
			if err != nil {
				t.Fatal(err)
			}
			provider := heldQueueProvider{make(chan struct{}), make(chan struct{})}
			service := &Service{repo: repo, bgContext: ctx, runCancels: make(map[string]context.CancelFunc)}
			done := make(chan struct{})
			go func() { service.executeRunWithClaim(claimed, provider, claim, true); close(done) }()
			select {
			case <-provider.started:
			case <-time.After(3 * time.Second):
				t.Fatal("provider did not start")
			}
			switch change {
			case "cancel":
				err = repo.CancelRunIfActive(ctx, run.ID)
			case "source":
				_, err = repo.pool.Exec(ctx, `UPDATE history_import_sources SET revision=revision+1,admin_token=NULL WHERE id=1`)
			default:
				_, err = repo.pool.Exec(ctx, `DELETE FROM history_import_user_mappings WHERE id=1`)
			}
			if err != nil {
				t.Fatal(err)
			}
			// A different node changed durable state; no local cancellation signal.
			close(provider.release)
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("worker did not acknowledge durable change")
			}
			got, err := repo.GetRunByID(ctx, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := RunStatusFailed
			if change == "cancel" {
				want = RunStatusCancelled
			}
			if got.Status != want {
				t.Fatalf("status=%s want=%s error=%s", got.Status, want, got.ErrorMessage)
			}
			if got.UserID != 1 || got.ProfileID != "p" {
				t.Fatalf("execution retargeted: %+v", got)
			}
			if change != "cancel" && got.ErrorMessage != ErrRunConfigurationChanged.Error() {
				t.Fatalf("missing review reason: %s", got.ErrorMessage)
			}
		})
	}
}

func TestQueueLegacyOrphansAndPersonalLifecycle(t *testing.T) {
	repo := queueRunnerRepository(t)
	ctx := t.Context()
	if _, err := repo.pool.Exec(ctx, `INSERT INTO history_import_runs(id,status,connection_mode) VALUES('old-admin','queued','admin_token'),('personal','queued','custom')`); err != nil {
		t.Fatal(err)
	}
	if err := repo.reconcileUndispatchedRuns(ctx); err != nil {
		t.Fatal(err)
	}
	old, err := repo.GetRunByID(ctx, "old-admin")
	if err != nil || old.Status != RunStatusFailed || old.ErrorMessage == "" {
		t.Fatalf("orphan=%+v %v", old, err)
	}
	personal, err := repo.GetRunByID(ctx, "personal")
	if err != nil || personal.Status != RunStatusQueued {
		t.Fatalf("personal changed=%+v %v", personal, err)
	}
	if err = repo.MarkRunStarted(ctx, "personal"); err != nil {
		t.Fatal(err)
	}
	if err = repo.UpdateRunProgress(ctx, "personal", ExecutionSummary{Fetched: 2}); err != nil {
		t.Fatal(err)
	}
	if err = repo.TouchRunHeartbeat(ctx, "personal"); err != nil {
		t.Fatal(err)
	}
	if err = repo.CompleteRun(ctx, "personal", ExecutionSummary{Fetched: 2}); err != nil {
		t.Fatal(err)
	}
	if err = repo.FailRun(ctx, "personal", ExecutionSummary{}, "late writer"); err == nil {
		t.Fatal("personal terminal was overwritten")
	}
}

type queueObserverFunc func(Run)

func (f queueObserverFunc) RunUpdated(run Run) { f(run) }

func TestQueueBulkPartialFailureAndSourceAuditPaging(t *testing.T) {
	repo := queueRunnerRepository(t)
	ctx := t.Context()
	service := &Service{repo: repo}
	service.AddObserver(queueObserverFunc(func(Run) {
		if _, err := repo.pool.Exec(ctx, `UPDATE history_import_sources SET admin_token=NULL,revision=revision+1 WHERE id=1`); err != nil {
			t.Error(err)
		}
	}))
	result, err := service.BulkCreateAdminRuns(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Outcomes) != 2 || result.Outcomes[0].Status != "accepted" || result.Outcomes[1].Status != "failed" || result.Errors != 1 || result.Outcomes[1].Error != "Source has no admin token configured." {
		t.Fatalf("partial=%+v", result)
	}
	first := result.Runs[0]
	if err = repo.CancelRunIfActive(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	// Detaching a mapping must not erase source-filtered run history.
	if _, err = repo.pool.Exec(ctx, `DELETE FROM history_import_user_mappings WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.pool.Exec(ctx, `INSERT INTO history_import_runs(id,status,dispatch_version,dispatch_source_id,created_at) VALUES('older','completed',1,1,now()-interval '1 day'),('other-source','completed',1,2,now())`); err != nil {
		t.Fatal(err)
	}
	page, more, err := repo.ListAdminRunsPage(ctx, new(1), nil, 1)
	if err != nil || !more || len(page) != 1 || page[0].ID != first.ID {
		t.Fatalf("page=%+v more=%v err=%v", page, more, err)
	}
	page, more, err = repo.ListAdminRunsPage(ctx, new(1), &RunKey{CreatedAt: page[0].CreatedAt, ID: page[0].ID}, 1)
	if err != nil || more || len(page) != 1 || page[0].ID != "older" {
		t.Fatalf("next=%+v more=%v err=%v", page, more, err)
	}
}

func TestQueueCancelCompletionRaceHasOneTerminalOutcome(t *testing.T) {
	repo := queueRunnerRepository(t)
	ctx := t.Context()
	for range 12 {
		run, err := repo.EnqueueAdminRun(ctx, 1)
		if err != nil {
			t.Fatal(err)
		}
		_, claim, err := repo.claimAdminRun(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Go(func() {
			<-start
			err := repo.CancelRunIfActive(ctx, run.ID)
			if err != nil && !errors.Is(err, ErrRunNotFound) && !errors.Is(err, ErrRunNotCancelable) {
				t.Error(err)
			}
		})
		wg.Go(func() {
			<-start
			err := repo.completeRun(ctx, claim, ExecutionSummary{})
			if err != nil && !errors.Is(err, ErrRunNotFound) && !errors.Is(err, ErrRunNotCancelable) {
				t.Error(err)
			}
		})
		close(start)
		wg.Wait()
		_ = repo.acknowledgeRunCancellation(ctx, claim)
		got, err := repo.GetRunByID(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != RunStatusCompleted && got.Status != RunStatusCancelled {
			t.Fatalf("race outcome=%s", got.Status)
		}
		if err = repo.failRun(ctx, claim, ExecutionSummary{}, "late provider error"); err == nil {
			t.Fatal("terminal overwritten")
		}
	}
}

func TestQueueCancellationTerminalSemantics(t *testing.T) {
	repo := queueRunnerRepository(t)
	ctx := t.Context()
	service := &Service{repo: repo}
	for _, status := range []string{RunStatusCompleted, RunStatusFailed, RunStatusCancelled} {
		if _, err := repo.pool.Exec(ctx, `INSERT INTO history_import_runs(id,status) VALUES($1,$2)`, status, status); err != nil {
			t.Fatal(err)
		}
		err := service.CancelAdminRun(ctx, status)
		if status == RunStatusCancelled {
			if err != nil {
				t.Fatalf("repeat cancel=%v", err)
			}
		} else if !errors.Is(err, ErrRunNotCancelable) {
			t.Fatalf("terminal %s=%v", status, err)
		}
	}
	if err := service.CancelAdminRun(ctx, "missing"); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("missing=%v", err)
	}
}

func TestQueueDisabledSourceRejectsBeforeAdmission(t *testing.T) {
	repo := queueRunnerRepository(t)
	ctx := t.Context()
	if _, err := repo.pool.Exec(ctx, `UPDATE history_import_sources SET enabled=false WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.EnqueueAdminRun(ctx, 1); !errors.Is(err, ErrSourceDisabled) {
		t.Fatalf("disabled=%v", err)
	}
	var count int
	if err := repo.pool.QueryRow(ctx, `SELECT count(*) FROM history_import_runs`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("effects=%d %v", count, err)
	}
}

func TestHistorySourceRejectsCredentialQueries(t *testing.T) {
	for _, address := range []string{"https://example.test?token=private", "https://example.test?", "https://user:private@example.test", "https://example.test#private"} {
		if err := validateSource(Source{Name: "Source", SourceType: SourceTypeEmby, BaseURL: address}); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("unsafe address accepted: %v", err)
		}
	}
	if err := validateSource(Source{Name: "Source", SourceType: SourceTypeEmby, BaseURL: "https://example.test/base"}); err != nil {
		t.Fatal(err)
	}
}
