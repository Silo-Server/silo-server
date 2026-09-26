package historyimport

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func personalQueueRepository(t *testing.T) *Repository {
	t.Helper()
	repo := queueRunnerRepositoryFromPool(t, queueTestPoolBeforePersonalMigration(t, false))
	_, err := repo.pool.Exec(t.Context(), `CREATE TABLE users(id integer PRIMARY KEY); INSERT INTO users VALUES(1),(2);
 CREATE TABLE user_profiles(user_id integer REFERENCES users(id),id text,PRIMARY KEY(user_id,id)); INSERT INTO user_profiles VALUES(1,'p'),(2,'other');
 ALTER TABLE history_import_runs ADD FOREIGN KEY(user_id,profile_id) REFERENCES user_profiles(user_id,id) ON DELETE CASCADE;
 CREATE TABLE history_import_connect_sessions(LIKE public.history_import_connect_sessions INCLUDING ALL);
 CREATE TABLE history_import_plex_sessions(LIKE public.history_import_plex_sessions INCLUDING ALL);
 INSERT INTO history_import_runs(id,status,connection_mode) VALUES('old-queued','queued','connect'),('old-running','running','connect'),('old-terminal','completed','connect')`)
	if err != nil {
		t.Fatal(err)
	}
	applyPersonalQueueMigration(t, repo.pool)
	return repo
}
func directPersonalAdmission() personalRunAdmission {
	return personalRunAdmission{UserID: 1, ProfileID: "p", SourceType: SourceTypeJellyfin, ConnectionMode: ConnectionModeCustom, Credentials: personalRunCredentials{BaseURL: "https://example.invalid", ExternalUserID: "external", ServerToken: "private-server-token"}}
}
func personalSessionAdmission(t *testing.T, repo *Repository, plex bool) personalRunAdmission {
	t.Helper()
	in := directPersonalAdmission()
	if plex {
		session, err := repo.CreatePlexSession(t.Context(), PlexSession{ID: uuid.NewString(), UserID: 1, PinID: "pin", PinCode: "code", AuthToken: "private-account-token", ExpiresAt: time.Now().Add(time.Hour), Servers: []PlexServer{{ClientIdentifier: "server", AccessToken: "private-server-token", RemoteURL: in.Credentials.BaseURL}}})
		if err != nil {
			t.Fatal(err)
		}
		in.SourceType = SourceTypePlex
		in.ConnectionMode = ConnectionModePlexOAuth
		in.PlexSession = session
		in.Credentials.AccountToken = session.AuthToken
		in.Credentials.ExternalUserID = ""
	} else {
		session, err := repo.CreateConnectSession(t.Context(), ConnectSession{ID: uuid.NewString(), UserID: 1, ConnectUserID: "connect-user", ConnectAccessToken: "private-connect-token", ExpiresAt: time.Now().Add(time.Hour), Servers: []ConnectServer{{ID: "server", URL: in.Credentials.BaseURL, AccessKey: "private-access-key"}}})
		if err != nil {
			t.Fatal(err)
		}
		in.SourceType = SourceTypeEmby
		in.ConnectionMode = ConnectionModeConnect
		in.ConnectSession = session
	}
	in.SelectedServerID = "server"
	return in
}
func assertPersonalUnconsumed(t *testing.T, repo *Repository, in personalRunAdmission) {
	t.Helper()
	var err error
	if in.ConnectSession != nil {
		_, err = repo.GetConnectSession(t.Context(), in.UserID, in.ConnectSession.ID)
	} else {
		_, err = repo.GetPlexSession(t.Context(), in.UserID, in.PlexSession.ID)
	}
	if err != nil {
		t.Fatalf("session was consumed: %v", err)
	}
	var count int
	if err = repo.pool.QueryRow(t.Context(), `SELECT count(*) FROM history_import_runs WHERE dispatch_kind='personal'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("orphan runs=%d err=%v", count, err)
	}
	if err = repo.pool.QueryRow(t.Context(), `SELECT count(*) FROM history_import_run_credentials`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("orphan credentials=%d err=%v", count, err)
	}
}
func TestPersonalAdmissionSessionRace(t *testing.T) {
	for _, plex := range []bool{false, true} {
		t.Run(map[bool]string{false: "connect", true: "plex"}[plex], func(t *testing.T) {
			repo := personalQueueRepository(t)
			in := personalSessionAdmission(t, repo, plex)
			var won atomic.Int32
			var wg sync.WaitGroup
			start := make(chan struct{})
			for range 8 {
				wg.Go(func() {
					<-start
					run, err := repo.enqueuePersonalRun(t.Context(), in)
					if err == nil {
						won.Add(1)
						credential, readErr := repo.readPersonalRunCredentials(t.Context(), claimPersonalForTest(t, repo, run.ID))
						if readErr != nil || credential != in.Credentials {
							t.Errorf("reconstruction mismatch: %v", readErr)
						}
					} else if !errors.Is(err, ErrConnectSessionUsed) && !errors.Is(err, ErrPlexSessionUsed) {
						t.Errorf("race: %v", err)
					}
				})
			}
			close(start)
			wg.Wait()
			if won.Load() != 1 {
				t.Fatalf("accepted=%d", won.Load())
			}
			var count int
			if err := repo.pool.QueryRow(t.Context(), `SELECT count(*) FROM history_import_run_credentials`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("credentials=%d err=%v", count, err)
			}
		})
	}
}
func TestPersonalAdmissionRollbackAndUncertainCommit(t *testing.T) {
	for _, mode := range []string{"cipher", "insert", "commit"} {
		t.Run(mode, func(t *testing.T) {
			repo := personalQueueRepository(t)
			in := personalSessionAdmission(t, repo, true)
			if mode == "cipher" {
				repo.cipher = nil
			} else {
				trigger := `CREATE TRIGGER reject_personal BEFORE INSERT ON history_import_runs FOR EACH ROW EXECUTE FUNCTION reject_personal()`
				if mode == "commit" {
					trigger = `CREATE CONSTRAINT TRIGGER reject_personal AFTER INSERT ON history_import_runs DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_personal()`
				}
				if _, err := repo.pool.Exec(t.Context(), `CREATE FUNCTION reject_personal() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected storage failure'; END $$;`+trigger); err != nil {
					t.Fatal(err)
				}
			}
			_, err := repo.enqueuePersonalRun(t.Context(), in)
			if err == nil {
				t.Fatal("accepted failed transaction")
			}
			if mode == "commit" && !errors.Is(err, ErrPersonalAdmissionUncertain) {
				t.Fatalf("commit was not reported uncertain: %v", err)
			}
			// The injected server-side exception proves rollback in this test. A lost
			// COMMIT response in production does not provide that evidence.
			if mode == "cipher" {
				repo = NewRepository(repo.pool, nil)
				var consumed bool
				if err = repo.pool.QueryRow(t.Context(), `SELECT consumed_at IS NOT NULL FROM history_import_plex_sessions WHERE id=$1`, in.PlexSession.ID).Scan(&consumed); err != nil || consumed {
					t.Fatalf("nil cipher consumed session: %v", err)
				}
				return
			}
			assertPersonalUnconsumed(t, repo, in)
		})
	}
}
func TestPersonalAdmissionRevalidatesSnapshots(t *testing.T) {
	for _, kind := range []string{"connect changed", "plex changed", "selected server", "profile", "expired", "source revision", "source disabled", "source missing"} {
		t.Run(kind, func(t *testing.T) {
			repo := personalQueueRepository(t)
			in := personalSessionAdmission(t, repo, strings.HasPrefix(kind, "plex"))
			switch kind {
			case "connect changed":
				in.ConnectSession.ConnectAccessToken = "obsolete"
			case "plex changed":
				in.PlexSession.AuthToken = "obsolete"
			case "selected server":
				in.SelectedServerID = "different"
			case "profile":
				in.ProfileID = "other"
			case "expired":
				if _, err := repo.pool.Exec(t.Context(), `UPDATE history_import_connect_sessions SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, in.ConnectSession.ID); err != nil {
					t.Fatal(err)
				}
			default:
				in = directPersonalAdmission()
				in.SourceType = SourceTypeEmby
				in.ConnectionMode = ConnectionModePredefined
				in.SourceID = 1
				in.SourceRevision = 1
				in.Credentials.BaseURL = "http://example.test"
				switch kind {
				case "source revision":
					in.SourceRevision = 2
				case "source disabled":
					_, _ = repo.pool.Exec(t.Context(), `UPDATE history_import_sources SET enabled=false WHERE id=1`)
				case "source missing":
					in.SourceID = 999
				}
			}
			if _, err := repo.enqueuePersonalRun(t.Context(), in); err == nil {
				t.Fatal("accepted invalid snapshot")
			}
			var count int
			if err := repo.pool.QueryRow(t.Context(), `SELECT count(*) FROM history_import_run_credentials`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("credentials=%d err=%v", count, err)
			}
		})
	}
	repo := personalQueueRepository(t)
	in := personalSessionAdmission(t, repo, false)
	in.Credentials.BaseURL += "/emby"
	if _, err := repo.enqueuePersonalRun(t.Context(), in); err != nil {
		t.Fatalf("authenticated /emby candidate rejected: %v", err)
	}
}

func TestPersonalCredentialDatabaseInvariants(t *testing.T) {
	repo := personalQueueRepository(t)
	ctx := t.Context()
	in := directPersonalAdmission()
	run, err := repo.enqueuePersonalRun(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	var ciphertext string
	if err = repo.pool.QueryRow(ctx, `SELECT payload FROM history_import_run_credentials WHERE run_id=$1`, run.ID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ciphertext, "enc:v1:") || strings.Contains(ciphertext, in.Credentials.ServerToken) || strings.Contains(ciphertext, in.Credentials.ExternalUserID) {
		t.Fatal("credential is not encrypted")
	}
	if _, _, err = repo.claimAdminRun(ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	if err = repo.pool.QueryRow(ctx, `SELECT status FROM history_import_runs WHERE id=$1`, run.ID).Scan(&status); err != nil || status != RunStatusQueued {
		t.Fatal("old admin worker claimed personal work")
	}
	for _, statement := range []string{
		`UPDATE history_import_runs SET dispatch_kind='admin' WHERE id=$1`,
		`UPDATE history_import_runs SET dispatch_version=1 WHERE id=$1`,
		`UPDATE history_import_runs SET mapping_id=1 WHERE id=$1`,
		`UPDATE history_import_runs SET profile_id='other' WHERE id=$1`,
		`UPDATE history_import_run_credentials SET payload=payload WHERE run_id=$1`,
		`DELETE FROM history_import_run_credentials WHERE run_id=$1`,
	} {
		if _, err = repo.pool.Exec(ctx, statement, run.ID); err == nil {
			t.Fatalf("accepted mutation: %s", statement)
		}
	}
	tx, err := repo.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, `INSERT INTO history_import_runs(id,status,source_type,connection_mode,dispatch_kind,dispatch_version) VALUES('missing-secret','queued','jellyfin','custom','personal',2)`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err == nil {
		t.Fatal("active personal run committed without credential")
	}
	tx, err = repo.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO history_import_runs(id,status,source_type,connection_mode,dispatch_kind,dispatch_version) VALUES('transplanted','queued','jellyfin','custom','personal',2);`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO history_import_run_credentials VALUES('transplanted',1,$1)`, ciphertext); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.readPersonalRunCredentials(ctx, claimPersonalForTest(t, repo, "transplanted")); !errors.Is(err, ErrPersonalCredentialsUnavailable) {
		t.Fatal("transplanted ciphertext was accepted")
	}
	for _, terminal := range []string{RunStatusCompleted, RunStatusFailed, RunStatusCancelled} {
		another, err := repo.enqueuePersonalRun(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = repo.pool.Exec(ctx, `UPDATE history_import_runs SET status=$2,completed_at=now() WHERE id=$1`, another.ID, terminal); err != nil {
			t.Fatal(err)
		}
		var count int
		if err = repo.pool.QueryRow(ctx, `SELECT count(*) FROM history_import_run_credentials WHERE run_id=$1`, another.ID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("terminal credential retained: %v", err)
		}
		if _, err = repo.pool.Exec(ctx, `UPDATE history_import_runs SET status='running' WHERE id=$1`, another.ID); err == nil {
			t.Fatal("terminal result changed")
		}
	}
	var legacy int
	if err = repo.pool.QueryRow(ctx, `SELECT count(*) FROM history_import_runs WHERE id LIKE 'old-%' AND dispatch_version IS NULL AND dispatch_kind='admin' AND ((id='old-queued' AND status='queued') OR (id='old-running' AND status='running') OR (id='old-terminal' AND status='completed'))`).Scan(&legacy); err != nil || legacy != 3 {
		t.Fatalf("legacy rows changed: %d %v", legacy, err)
	}
	if _, err = repo.pool.Exec(ctx, `DELETE FROM user_profiles WHERE user_id=1 AND id='p'`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = repo.pool.QueryRow(ctx, `SELECT count(*) FROM history_import_run_credentials`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("deleted target retains credentials: %v", err)
	}
}

func TestPersonalOldMaintenanceRechecksHeartbeatAndErasesCredentials(t *testing.T) {
	repo := personalQueueRepository(t)
	ctx := t.Context()
	run, err := repo.enqueuePersonalRun(ctx, directPersonalAdmission())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.pool.Exec(ctx, `UPDATE history_import_runs SET status='running',claim_generation=1,started_at=now()-interval '2 minutes',last_heartbeat_at=now()-interval '2 minutes' WHERE id=$1`, run.ID); err != nil {
		t.Fatal(err)
	}
	for name, write := range map[string]func() error{
		"start":     func() error { return repo.MarkRunStarted(ctx, run.ID) },
		"heartbeat": func() error { return repo.TouchRunHeartbeat(ctx, run.ID) },
		"progress":  func() error { return repo.UpdateRunProgress(ctx, run.ID, ExecutionSummary{}) },
		"complete":  func() error { return repo.CompleteRun(ctx, run.ID, ExecutionSummary{}) },
		"fail":      func() error { return repo.FailRun(ctx, run.ID, ExecutionSummary{}, "obsolete execution") },
	} {
		if err = write(); err == nil {
			t.Fatalf("generation-zero %s modified personal run", name)
		}
	}
	tx, err := repo.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// The maintenance statement starts from a stale row but blocks behind this
	// heartbeat transaction. PostgreSQL must recheck the predicate after commit.
	if _, err = tx.Exec(ctx, `UPDATE history_import_runs SET last_heartbeat_at=clock_timestamp() WHERE id=$1`, run.ID); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		_, maintenanceErr := repo.FailStaleRuns(ctx, time.Now().Add(-time.Minute), "Worker lease expired")
		done <- maintenanceErr
	}()
	<-started
	// Observe the actual lock wait; do not use elapsed sleeps to infer ordering.
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		var waiting bool
		err = repo.pool.QueryRow(waitCtx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%COALESCE(last_heartbeat_at, started_at, created_at)%')`).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if _, err = repo.readPersonalRunCredentials(ctx, RunClaim{RunID: run.ID, DispatchKind: "personal", Generation: 1}); err != nil {
		t.Fatalf("fresh heartbeat lost credentials: %v", err)
	}
	if _, err = repo.pool.Exec(ctx, `UPDATE history_import_runs SET last_heartbeat_at=now()-interval '2 minutes' WHERE id=$1`, run.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := repo.FailStaleRuns(ctx, time.Now().Add(-time.Minute), "Worker lease expired"); err != nil || n != 1 {
		t.Fatalf("stale sweep n=%d err=%v", n, err)
	}
	if _, err = repo.readPersonalRunCredentials(ctx, RunClaim{RunID: run.ID, DispatchKind: "personal", Generation: 1}); !errors.Is(err, ErrRunClaimLost) {
		t.Fatal("stale terminal retained credentials")
	}
}

func claimPersonalForTest(t *testing.T, repo *Repository, runID string) RunClaim {
	t.Helper()
	claim := RunClaim{RunID: runID, DispatchKind: "personal"}
	err := repo.pool.QueryRow(t.Context(), `UPDATE history_import_runs SET status='running',claim_generation=claim_generation+1,started_at=now(),last_heartbeat_at=now() WHERE id=$1 AND status='queued' RETURNING claim_generation`, runID).Scan(&claim.Generation)
	if err != nil {
		t.Fatal(err)
	}
	return claim
}
