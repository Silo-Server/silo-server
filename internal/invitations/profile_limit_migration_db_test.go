package invitations

import (
	"context"
	"os"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

func invitationProfileMigration(t *testing.T, f atomicInvitationFixture) (string, string) {
	t.Helper()
	migration, err := os.ReadFile("../../migrations/sql/20261008030636_add_invitation_profile_limit.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, down, ok := strings.Cut(string(migration), "-- +goose Down")
	if !ok {
		t.Fatal("missing migration Down")
	}
	// Execute the production migration SQL against this nonce-owned schema.
	qualified := pgx.Identifier{f.schema, "invitations"}.Sanitize()
	return strings.ReplaceAll(up, "public.invitations", qualified), strings.ReplaceAll(down, "public.invitations", qualified)
}

func invitationRowsSnapshot(t *testing.T, f atomicInvitationFixture, withoutCap bool) string {
	t.Helper()
	projection := "to_jsonb(i)"
	if withoutCap {
		projection += "-'max_profiles'"
	}
	var rows string
	if err := f.pool.QueryRow(t.Context(), `SELECT COALESCE(jsonb_agg(`+projection+` ORDER BY id),'[]'::jsonb)::text FROM invitations i`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

func executeInvitationMigrationDown(ctx context.Context, f atomicInvitationFixture, down string) error {
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err = tx.Exec(ctx, down); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func TestInvitationProfileLimitMigrationDownDB(t *testing.T) {
	for _, state := range []string{"claimable cap", "claimable default", "expired", "revoked", "accepted"} {
		t.Run(state, func(t *testing.T) {
			f := atomicInvitationDB(t)
			input := guardedInvitationInput("migration@example.invalid")
			if state == "claimable default" {
				input.MaxProfiles = nil
			}
			inv, err := f.repo.Create(t.Context(), input, HashToken("migration-cap"))
			if err != nil {
				t.Fatal(err)
			}
			var acceptedUser *models.User
			switch state {
			case "expired":
				_, err = f.pool.Exec(t.Context(), `UPDATE invitations SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, inv.ID)
			case "revoked":
				err = f.repo.Revoke(t.Context(), inv.ID)
			case "accepted":
				accounts := auth.NewAccountProvisioner(auth.NewUserRepository(f.pool), pgstore.NewPostgresProvider(f.pool))
				acceptedUser, err = f.repo.Accept(t.Context(), inv.TokenHash, func(inv *models.Invitation, tx pgx.Tx) (*models.User, error) {
					return accounts.CreateAccountInTransaction(t.Context(), tx, auth.CreateAccountInput{User: models.CreateUserInput{Username: inv.Email, Email: inv.Email, Password: "synthetic-password", Role: inv.Role, MaxProfiles: inv.MaxProfiles}})
				})
			}
			if err != nil {
				t.Fatal(err)
			}
			up, down := invitationProfileMigration(t, f)
			before := invitationRowsSnapshot(t, f, state != "claimable cap")
			err = executeInvitationMigrationDown(t.Context(), f, down)
			if state == "claimable cap" {
				if err == nil || !strings.Contains(err.Error(), "profile-limited invitations") {
					t.Fatalf("rollback discarded a claimable cap: %v", err)
				}
				if after := invitationRowsSnapshot(t, f, false); after != before {
					t.Fatalf("refused Down changed exact rows: before=%s after=%s", before, after)
				}
				stored, err := f.repo.GetByID(t.Context(), inv.ID)
				if err != nil || !reflect.DeepEqual(inv, stored) {
					t.Fatal("refused Down changed pending invitation", err)
				}
				return
			}
			if err != nil {
				t.Fatal("safe migration rollback refused", err)
			}
			if after := invitationRowsSnapshot(t, f, true); after != before {
				t.Fatalf("Down changed retained rows: before=%s after=%s", before, after)
			}
			if acceptedUser != nil {
				stored, err := auth.NewUserRepository(f.pool).GetByID(t.Context(), acceptedUser.ID)
				if err != nil || stored.MaxProfiles != 2 {
					t.Fatal("Down changed the accepted account cap", err)
				}
			}
			if _, err := f.pool.Exec(t.Context(), up); err != nil {
				t.Fatal(err)
			}
			if _, err := f.repo.GetByID(t.Context(), inv.ID); err != nil {
				t.Fatal("Up failed to restore invitation reads", err)
			}
		})
	}
}

func TestInvitationProfileLimitMigrationDownConcurrentCreateDB(t *testing.T) {
	f := atomicInvitationDB(t)
	ctx := t.Context()
	if _, err := f.pool.Exec(ctx, `CREATE FUNCTION migration_insert_barrier() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(hashtext(current_schema())); RETURN NEW; END $$; CREATE TRIGGER migration_insert_barrier BEFORE INSERT ON invitations FOR EACH ROW EXECUTE FUNCTION migration_insert_barrier()`); err != nil {
		t.Fatal(err)
	}
	barrier, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	unblock := func() { once.Do(func() { _ = barrier.Commit(context.WithoutCancel(ctx)) }) }
	defer unblock()
	if _, err = barrier.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext(current_schema()))`); err != nil {
		t.Fatal(err)
	}
	type created struct {
		inv *models.Invitation
		err error
	}
	createDone := make(chan created, 1)
	go func() {
		inv, err := f.repo.Create(ctx, guardedInvitationInput("migration-race@example.invalid"), HashToken("migration-race"))
		createDone <- created{inv, err}
	}()
	f.waitBlocked(t)
	_, down := invitationProfileMigration(t, f)
	downDone := make(chan error, 1)
	go func() { downDone <- executeInvitationMigrationDown(ctx, f, down) }()
	waitCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for {
		var waiting bool
		if err := f.pool.QueryRow(waitCtx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE relation=$1::regclass AND mode='AccessExclusiveLock' AND NOT granted)`, pgx.Identifier{f.schema, "invitations"}.Sanitize()).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		runtime.Gosched()
	}
	unblock()
	result, downErr := <-createDone, <-downDone
	if result.err != nil || result.inv == nil {
		t.Fatal("concurrent creation failed", result.err)
	}
	if downErr == nil || !strings.Contains(downErr.Error(), "profile-limited invitations") {
		t.Fatalf("Down discarded concurrently committed cap: %v", downErr)
	}
	stored, err := f.repo.GetByID(ctx, result.inv.ID)
	if err != nil || !reflect.DeepEqual(result.inv, stored) || stored.MaxProfiles == nil || *stored.MaxProfiles != 2 {
		t.Fatal("Down altered concurrent invitation", err)
	}
	var total int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM invitations`).Scan(&total); err != nil || total != 1 {
		t.Fatalf("total=%d err=%v", total, err)
	}
}
