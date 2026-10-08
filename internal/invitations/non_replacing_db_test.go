package invitations

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

func guardedInvitationInput(email string) models.CreateInvitationInput {
	return models.CreateInvitationInput{Email: email, ReplaceExisting: new(false), Role: models.RoleUser, InvitedBy: 1, ExpiresAt: time.Now().Add(time.Hour), MaxProfiles: new(2)}
}

func TestInvitationNonReplacingCreationDB(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending", true: "expired"}[expired], func(t *testing.T) {
			f := atomicInvitationDB(t)
			prior := f.invite(t, "guarded")
			if expired {
				if _, err := f.pool.Exec(t.Context(), `UPDATE invitations SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, prior.ID); err != nil {
					t.Fatal(err)
				}
				var err error
				prior, err = f.repo.GetByID(t.Context(), prior.ID)
				if err != nil {
					t.Fatal(err)
				}
			}
			// A guard must not attempt an UPDATE even if it would later roll back.
			if _, err := f.pool.Exec(t.Context(), `CREATE FUNCTION reject_guarded_update() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'guard must never update'; END $$; CREATE TRIGGER reject_guarded_update BEFORE UPDATE ON invitations FOR EACH ROW EXECUTE FUNCTION reject_guarded_update()`); err != nil {
				t.Fatal(err)
			}
			users := auth.NewUserRepository(f.pool)
			sender := &fakeMail{configured: true}
			svc := NewService(f.repo, users, auth.NewAccountProvisioner(users, pgstore.NewPostgresProvider(f.pool)), &fakeSessions{}, sender, nil, nil, "https://server.example.invalid")
			result, err := svc.Send(t.Context(), SendInput{Email: prior.Email, ReplaceExisting: new(false), InvitedBy: 1, MaxProfiles: new(2)})
			if !errors.Is(err, ErrInvitationExists) || result != nil || len(sender.sent) != 0 {
				t.Fatalf("result=%v err=%v sends=%d", result, err, len(sender.sent))
			}
			stored, err := f.repo.GetByID(t.Context(), prior.ID)
			if err != nil || !reflect.DeepEqual(prior, stored) {
				t.Fatalf("source changed: before=%+v after=%+v err=%v", prior, stored, err)
			}
			f.counts(t, prior, 0, 0, 0)
			var total int
			if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM invitations`).Scan(&total); err != nil || total != 1 {
				t.Fatalf("total=%d err=%v", total, err)
			}
		})
	}
}

func TestInvitationNonReplacingLegacyAndResendDB(t *testing.T) {
	for _, replace := range []*bool{nil, new(true)} {
		t.Run(map[bool]string{true: "omitted", false: "true"}[replace == nil], func(t *testing.T) {
			f := atomicInvitationDB(t)
			input := guardedInvitationInput("legacy@example.invalid")
			first, err := f.repo.Create(t.Context(), input, HashToken("guarded-first"))
			if err != nil {
				t.Fatal(err)
			}
			input.ReplaceExisting = replace
			second, err := f.repo.Create(t.Context(), input, HashToken("legacy-second"))
			if err != nil {
				t.Fatal(err)
			}
			old, err := f.repo.GetByID(t.Context(), first.ID)
			if err != nil || old.RevokedAt == nil {
				t.Fatal("legacy replacement did not revoke source", err)
			}
			input.ReplaceExisting, input.MaxProfiles = new(false), new(99)
			third, err := f.repo.Resend(t.Context(), second.ID, input, HashToken("resend-third"))
			if err != nil || third.MaxProfiles == nil || *third.MaxProfiles != 2 {
				t.Fatalf("resend=%+v err=%v", third, err)
			}
			if result, err := f.repo.Resend(t.Context(), second.ID, input, HashToken("stale-fourth")); !errors.Is(err, ErrNotClaimable) || result != nil {
				t.Fatalf("stale resend=%v err=%v", result, err)
			}
			stored, err := f.repo.GetByID(t.Context(), third.ID)
			if err != nil || !reflect.DeepEqual(third, stored) {
				t.Fatal("stale resend changed the current source", err)
			}
			var total, pending int
			if err := f.pool.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER(WHERE accepted_at IS NULL AND revoked_at IS NULL) FROM invitations`).Scan(&total, &pending); err != nil || total != 3 || pending != 1 {
				t.Fatalf("total/pending=%d/%d err=%v", total, pending, err)
			}
		})
	}
}

// Hold the guarded insert after its no-pending read, then let an actual legacy
// Create publish a competing row. The partial unique index must return a typed
// conflict, without allowing guarded creation to revoke that unseen source.
func TestInvitationNonReplacingConcurrentFreshLegacyCreateDB(t *testing.T) {
	f := atomicInvitationDB(t)
	ctx := t.Context()
	guardHash := HashToken("guarded-race")
	if _, err := f.pool.Exec(ctx, `CREATE FUNCTION guarded_insert_barrier() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.token_hash='`+guardHash+`' THEN PERFORM pg_advisory_xact_lock(hashtext(current_schema())); END IF; RETURN NEW; END $$; CREATE TRIGGER guarded_insert_barrier BEFORE INSERT ON invitations FOR EACH ROW EXECUTE FUNCTION guarded_insert_barrier()`); err != nil {
		t.Fatal(err)
	}
	barrier, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = barrier.Rollback(context.WithoutCancel(ctx)) }()
	if _, err = barrier.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext(current_schema()))`); err != nil {
		t.Fatal(err)
	}
	input := guardedInvitationInput("fresh-guard@example.invalid")
	done := make(chan error, 1)
	go func() { _, err := f.repo.Create(ctx, input, guardHash); done <- err }()
	f.waitBlocked(t)
	legacy := input
	legacy.ReplaceExisting = nil
	winner, err := f.repo.Create(ctx, legacy, HashToken("legacy-winner"))
	if err != nil {
		t.Fatal(err)
	}
	if err = barrier.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; !errors.Is(err, ErrInvitationExists) {
		t.Fatalf("guarded race error=%v", err)
	}
	stored, err := f.repo.GetByID(ctx, winner.ID)
	if err != nil || !reflect.DeepEqual(winner, stored) {
		t.Fatal("guarded losing insert changed legacy winner", err)
	}
	var total int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM invitations`).Scan(&total); err != nil || total != 1 {
		t.Fatalf("total=%d err=%v", total, err)
	}
}

func TestInvitationNonReplacingConcurrentAcceptanceDB(t *testing.T) {
	for _, acceptanceCommits := range []bool{false, true} {
		t.Run(map[bool]string{false: "acceptance rollback", true: "acceptance commit"}[acceptanceCommits], func(t *testing.T) {
			f := atomicInvitationDB(t)
			ctx := t.Context()
			prior := f.invite(t, "accept-guard")
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			accepted := make(chan error, 1)
			provision := f.provision(pgstore.NewPostgresProvider(f.pool))
			go func() {
				_, err := f.repo.Accept(ctx, prior.TokenHash, func(inv *models.Invitation, tx pgx.Tx) (*models.User, error) {
					user, err := provision(inv, tx)
					if err != nil {
						return nil, err
					}
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
					if !acceptanceCommits {
						return nil, errors.New("fixture acceptance rollback")
					}
					return user, nil
				})
				accepted <- err
			}()
			select {
			case <-entered:
			case err := <-accepted:
				t.Fatalf("accept failed before barrier: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			guarded := make(chan error, 1)
			go func() {
				_, err := f.repo.Create(ctx, guardedInvitationInput(prior.Email), HashToken("guarded-accept-race"))
				guarded <- err
			}()
			f.waitBlocked(t)
			unblock()
			acceptErr, guardErr := <-accepted, <-guarded
			if acceptanceCommits {
				if acceptErr != nil || !errors.Is(guardErr, ErrEmailTaken) {
					t.Fatalf("accept=%v guarded=%v", acceptErr, guardErr)
				}
				f.counts(t, prior, 1, 1, 1)
			} else {
				if acceptErr == nil || !errors.Is(guardErr, ErrInvitationExists) {
					t.Fatalf("accept=%v guarded=%v", acceptErr, guardErr)
				}
				f.counts(t, prior, 0, 0, 0)
			}
			stored, err := f.repo.GetByID(ctx, prior.ID)
			if err != nil || stored.RevokedAt != nil {
				t.Fatal("guarded create revoked acceptance source", err)
			}
			if !acceptanceCommits && !reflect.DeepEqual(prior, stored) {
				t.Fatal("accept rollback or guarded refusal changed source")
			}
			var total int
			if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM invitations`).Scan(&total); err != nil || total != 1 {
				t.Fatalf("total=%d err=%v", total, err)
			}
		})
	}
}

// Wait for an exact fixture lock count, never a fixed elapsed-time assumption.
func waitInvitationFixtureLocks(t *testing.T, f atomicInvitationFixture, want int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	for {
		var count int
		if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock'`, f.schema).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == want {
			return
		}
		runtime.Gosched()
	}
}

func TestInvitationNonReplacingConcurrentLegacyReplacementDB(t *testing.T) {
	f := atomicInvitationDB(t)
	ctx := t.Context()
	prior := f.invite(t, "legacy-guard")
	if _, err := f.pool.Exec(ctx, `CREATE FUNCTION legacy_replace_barrier() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(hashtext(current_schema())); RETURN NEW; END $$; CREATE TRIGGER legacy_replace_barrier BEFORE UPDATE ON invitations FOR EACH ROW EXECUTE FUNCTION legacy_replace_barrier()`); err != nil {
		t.Fatal(err)
	}
	barrier, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = barrier.Rollback(context.WithoutCancel(ctx)) }()
	if _, err = barrier.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext(current_schema()))`); err != nil {
		t.Fatal(err)
	}
	legacyDone := make(chan *models.Invitation, 1)
	legacyErr := make(chan error, 1)
	input := guardedInvitationInput(prior.Email)
	legacy := input
	legacy.ReplaceExisting = nil
	go func() {
		inv, err := f.repo.Create(ctx, legacy, HashToken("legacy-new-source"))
		legacyDone <- inv
		legacyErr <- err
	}()
	waitInvitationFixtureLocks(t, f, 1)
	guarded := make(chan error, 1)
	go func() { _, err := f.repo.Create(ctx, input, HashToken("guarded-stale-read")); guarded <- err }()
	waitInvitationFixtureLocks(t, f, 2)
	if err = barrier.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	winner, winnerErr := <-legacyDone, <-legacyErr
	if winnerErr != nil || winner == nil {
		t.Fatal(winnerErr)
	}
	if err = <-guarded; !errors.Is(err, ErrInvitationExists) {
		t.Fatalf("guarded replacement race err=%v", err)
	}
	stored, err := f.repo.GetByID(ctx, winner.ID)
	if err != nil || !reflect.DeepEqual(winner, stored) {
		t.Fatal("guarded attempt changed the replacement source", err)
	}
	old, err := f.repo.GetByID(ctx, prior.ID)
	if err != nil || old.RevokedAt == nil {
		t.Fatal("legacy source was not superseded", err)
	}
	var total, pending int
	if err := f.pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE accepted_at IS NULL AND revoked_at IS NULL) FROM invitations`).Scan(&total, &pending); err != nil || total != 2 || pending != 1 {
		t.Fatalf("total/pending=%d/%d err=%v", total, pending, err)
	}
}
