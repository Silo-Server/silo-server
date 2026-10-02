package historyimport

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestPersonalClaimRequiresExactRunningAuthority(t *testing.T) {
	repo := personalQueueRepository(t)
	ctx := t.Context()
	run, err := repo.enqueuePersonalRun(ctx, directPersonalAdmission())
	if err != nil {
		t.Fatal(err)
	}
	unclaimed := RunClaim{RunID: run.ID, DispatchKind: "personal", Generation: 1}
	if _, err = repo.readPersonalRunCredentials(ctx, unclaimed); !errors.Is(err, ErrRunClaimLost) {
		t.Fatalf("queued credential read: %v", err)
	}
	var won atomic.Int32
	var wg sync.WaitGroup
	claims := make(chan RunClaim, 8)
	for range 8 {
		wg.Go(func() {
			claimed, claim, claimErr := repo.claimQueuedRun(ctx)
			if claimErr != nil {
				t.Error(claimErr)
				return
			}
			if claimed != nil {
				won.Add(1)
				claims <- claim
				if claimed.ConnectionMode != ConnectionModeCustom || claimed.MappingID != nil || claimed.ProfileID != "p" {
					t.Error("personal claim lost target or mode")
				}
			}
		})
	}
	wg.Wait()
	close(claims)
	if won.Load() != 1 {
		t.Fatalf("claims=%d", won.Load())
	}
	claim := <-claims
	if claim.DispatchKind != "personal" || claim.Generation != 1 || claim.MappingID != 0 || claim.SourceID != 0 {
		t.Fatalf("personal claim metadata=%+v", claim)
	}
	credential, err := repo.readPersonalRunCredentials(ctx, claim)
	if err != nil || credential != directPersonalAdmission().Credentials {
		t.Fatalf("claimed credential mismatch: %v", err)
	}
	for _, bad := range []RunClaim{{RunID: run.ID, DispatchKind: "personal"}, {RunID: run.ID, DispatchKind: "personal", Generation: 2}, {RunID: run.ID, DispatchKind: "admin", Generation: 1}} {
		if _, err = repo.readPersonalRunCredentials(ctx, bad); !errors.Is(err, ErrRunClaimLost) {
			t.Fatalf("invalid authority read: %v", err)
		}
	}
	if err = repo.CancelRunIfActive(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.readPersonalRunCredentials(ctx, claim); !errors.Is(err, ErrRunCancellationRequested) {
		t.Fatalf("canceled read: %v", err)
	}
	if err = repo.acknowledgeRunCancellation(ctx, claim); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.readPersonalRunCredentials(ctx, claim); !errors.Is(err, ErrRunClaimLost) {
		t.Fatalf("terminal read: %v", err)
	}
}

func TestPersonalClaimRevalidatesSourceAndTarget(t *testing.T) {
	for _, change := range []string{"revision", "disabled", "type", "deleted source", "deleted profile"} {
		t.Run(change, func(t *testing.T) {
			repo := personalQueueRepository(t)
			ctx := t.Context()
			in := directPersonalAdmission()
			in.SourceType = SourceTypeEmby
			in.ConnectionMode = ConnectionModePredefined
			in.SourceID = 1
			in.SourceRevision = 1
			in.Credentials.BaseURL = "http://example.test"
			run, err := repo.enqueuePersonalRun(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			_, claim, err := repo.claimQueuedRun(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = repo.readPersonalRunCredentials(ctx, claim); err != nil {
				t.Fatal(err)
			}
			statements := map[string]string{"revision": `UPDATE history_import_sources SET revision=revision+1 WHERE id=1`, "disabled": `UPDATE history_import_sources SET enabled=false WHERE id=1`, "type": `UPDATE history_import_sources SET source_type='plex' WHERE id=1`, "deleted source": `DELETE FROM history_import_sources WHERE id=1`, "deleted profile": `DELETE FROM user_profiles WHERE user_id=1 AND id='p'`}
			if _, err = repo.pool.Exec(ctx, statements[change]); err != nil {
				t.Fatal(err)
			}
			want := ErrRunConfigurationChanged
			if change == "deleted profile" {
				want = ErrRunClaimLost
			}
			if _, err = repo.readPersonalRunCredentials(ctx, claim); !errors.Is(err, want) {
				t.Fatalf("%s read: %v", change, err)
			}
			if err = repo.touchClaimHeartbeat(ctx, claim); !errors.Is(err, want) {
				t.Fatalf("%s heartbeat: %v", change, err)
			}
			if change != "deleted profile" {
				if err = repo.failRun(ctx, claim, ExecutionSummary{}, ErrRunConfigurationChanged.Error()); err != nil {
					t.Fatal(err)
				}
			}
			var count int
			if err = repo.pool.QueryRow(ctx, `SELECT count(*) FROM history_import_run_credentials WHERE run_id=$1`, run.ID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("secret retained: %v", err)
			}
		})
	}
}

func TestPersonalClaimFailsClosedForDamagedCredentials(t *testing.T) {
	for _, damage := range []string{"missing", "plaintext", "envelope", "ciphertext"} {
		t.Run(damage, func(t *testing.T) {
			repo := personalQueueRepository(t)
			ctx := t.Context()
			run, err := repo.enqueuePersonalRun(ctx, directPersonalAdmission())
			if err != nil {
				t.Fatal(err)
			}
			_, claim, err := repo.claimQueuedRun(ctx)
			if err != nil {
				t.Fatal(err)
			}
			// Simulate storage corruption explicitly. Normal SQL writers cannot remove
			// or replace these active credentials under the production constraints.
			if _, err = repo.pool.Exec(ctx, `ALTER TABLE history_import_run_credentials DISABLE TRIGGER USER; ALTER TABLE history_import_run_credentials DROP CONSTRAINT history_import_run_credentials_payload_check; ALTER TABLE history_import_run_credentials DROP CONSTRAINT history_import_run_credentials_envelope_version_check`); err != nil {
				t.Fatal(err)
			}
			switch damage {
			case "missing":
				_, err = repo.pool.Exec(ctx, `DELETE FROM history_import_run_credentials WHERE run_id=$1`, run.ID)
			case "plaintext":
				_, err = repo.pool.Exec(ctx, `UPDATE history_import_run_credentials SET payload='private-server-token' WHERE run_id=$1`, run.ID)
			case "envelope":
				_, err = repo.pool.Exec(ctx, `UPDATE history_import_run_credentials SET envelope_version=999 WHERE run_id=$1`, run.ID)
			default:
				_, err = repo.pool.Exec(ctx, `UPDATE history_import_run_credentials SET payload='enc:v1:invalid' WHERE run_id=$1`, run.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = repo.pool.Exec(ctx, `ALTER TABLE history_import_run_credentials ENABLE TRIGGER USER`); err != nil {
				t.Fatal(err)
			}
			if _, err = repo.readPersonalRunCredentials(ctx, claim); !errors.Is(err, ErrPersonalCredentialsUnavailable) {
				t.Fatalf("damaged credential read: %v", err)
			}
			if err = repo.failRun(ctx, claim, ExecutionSummary{}, ErrPersonalCredentialsUnavailable.Error()); err != nil {
				t.Fatal(err)
			}
			var count int
			if err = repo.pool.QueryRow(ctx, `SELECT count(*) FROM history_import_run_credentials WHERE run_id=$1`, run.ID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("damaged secret retained: %v", err)
			}
		})
	}
}

func TestPersonalClaimQuarantinesMissingEnvelopeWithoutStarvingQueue(t *testing.T) {
	for _, missing := range []bool{true, false} {
		t.Run(map[bool]string{true: "missing", false: "unsupported"}[missing], func(t *testing.T) {
			repo := personalQueueRepository(t)
			ctx := t.Context()
			broken, err := repo.enqueuePersonalRun(ctx, directPersonalAdmission())
			if err != nil {
				t.Fatal(err)
			}
			good, err := repo.enqueuePersonalRun(ctx, directPersonalAdmission())
			if err != nil {
				t.Fatal(err)
			}
			if _, err = repo.pool.Exec(ctx, `ALTER TABLE history_import_run_credentials DISABLE TRIGGER USER; ALTER TABLE history_import_run_credentials DROP CONSTRAINT history_import_run_credentials_envelope_version_check`); err != nil {
				t.Fatal(err)
			}
			if missing {
				_, err = repo.pool.Exec(ctx, `DELETE FROM history_import_run_credentials WHERE run_id=$1`, broken.ID)
			} else {
				_, err = repo.pool.Exec(ctx, `UPDATE history_import_run_credentials SET envelope_version=999 WHERE run_id=$1`, broken.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = repo.pool.Exec(ctx, `ALTER TABLE history_import_run_credentials ENABLE TRIGGER USER`); err != nil {
				t.Fatal(err)
			}
			terminal, claim, err := repo.claimQueuedRun(ctx)
			if err != nil || terminal == nil || terminal.ID != broken.ID || terminal.Status != RunStatusFailed || claim.Generation != 0 {
				t.Fatalf("quarantine=%+v claim=%+v err=%v", terminal, claim, err)
			}
			if terminal.ErrorMessage != ErrPersonalCredentialsUnavailable.Error() {
				t.Fatal("quarantine leaked storage detail")
			}
			next, claim, err := repo.claimQueuedRun(ctx)
			if err != nil || next == nil || next.ID != good.ID || claim.Generation != 1 {
				t.Fatalf("next queue entry blocked: %v", err)
			}
			if _, err = repo.readPersonalRunCredentials(ctx, claim); err != nil {
				t.Fatal(err)
			}
			var count int
			if err = repo.pool.QueryRow(ctx, `SELECT count(*) FROM history_import_run_credentials WHERE run_id=$1`, broken.ID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("quarantined secret retained: %v", err)
			}
		})
	}
}
