package notifications

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

func orderedAppleCommand() ApplePushCommand {
	return ApplePushCommand{UserID: 7, ProfileID: "profile", InstallationKey: base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("k", 32))), Generation: 1, ApplePushRegistrationInput: validApplePushInput()}
}
func appleLegacyCommand(cmd ApplePushCommand) ApplePushDeviceRegistration {
	r, _ := normalizeApplePushRegistration(cmd.ApplePushRegistrationInput)
	r.UserID, r.ProfileID = cmd.UserID, cmd.ProfileID
	return r
}
func TestOrderedApplePushReplayAndCurrentRow(t *testing.T) {
	repo, pool := newPushDeviceTestRepo(t)
	ctx, cipher, cmd := t.Context(), testPushCipher(t), orderedAppleCommand()
	first, err := repo.ApplyApplePush(ctx, cmd, cipher)
	if err != nil || !first.Enabled || first.Removed || first.RegistrationID == "" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	cmd.APNsToken = strings.ToUpper(cmd.APNsToken)
	if replay, e := repo.ApplyApplePush(ctx, cmd, cipher); e != nil || replay != first {
		t.Fatalf("canonical replay=%+v err=%v", replay, e)
	}
	if err = repo.RecordPushFailure(ctx, first.RegistrationID, "unregistered", true); err != nil {
		t.Fatal(err)
	}
	disabled, e := repo.ApplyApplePush(ctx, cmd, cipher)
	if e != nil || disabled.Enabled || disabled.RegistrationID != first.RegistrationID {
		t.Fatalf("disabled=%+v err=%v", disabled, e)
	}
	cmd.Generation = 3
	cmd.ProfileID = "next-profile"
	cmd.APNsToken = strings.Repeat("b", 64)
	cmd.APNsEnvironment = APNsEnvironmentSandbox
	cmd.PushMode = PushModeOff
	next, e := repo.ApplyApplePush(ctx, cmd, cipher)
	if e != nil || next.RegistrationID == first.RegistrationID || !next.Enabled || next.PushMode != PushModeOff {
		t.Fatalf("replacement=%+v err=%v", next, e)
	}
	if err = repo.RecordPushFailure(ctx, first.RegistrationID, "late", true); err != nil {
		t.Fatal(err)
	}
	current, e := repo.getPushDeviceByID(ctx, next.RegistrationID)
	if e != nil || !current.Enabled || current.APNsEnvironment != cmd.APNsEnvironment {
		t.Fatalf("current=%+v err=%v", current, e)
	}
	if err = repo.DeleteAllForProfile(ctx, cmd.ProfileID); err != nil {
		t.Fatal(err)
	}
	removed, e := repo.ApplyApplePush(ctx, cmd, cipher)
	if e != nil || !removed.Removed || removed.Enabled {
		t.Fatalf("removed=%+v err=%v", removed, e)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM push_devices`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("resurrected=%d err=%v", count, err)
	}
}
func TestOrderedApplePushConflictsAndBridge(t *testing.T) {
	repo, _ := newPushDeviceTestRepo(t)
	ctx, cipher, cmd := t.Context(), testPushCipher(t), orderedAppleCommand()
	foreign := cmd
	foreign.UserID = 8
	if _, err := repo.UpsertApple(ctx, appleLegacyCommand(foreign), cipher); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ApplyApplePush(ctx, cmd, cipher); !errors.Is(err, ErrPushGenerationConflict) {
		t.Fatalf("foreign=%v", err)
	}
	cmd.UserID = 8
	if _, err := repo.ApplyApplePush(ctx, cmd, cipher); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*ApplePushCommand){
		func(c *ApplePushCommand) { c.APNsToken = strings.Repeat("c", 64) },
		func(c *ApplePushCommand) { c.APNsEnvironment = APNsEnvironmentSandbox },
		func(c *ApplePushCommand) { c.PushMode = PushModeOff },
		func(c *ApplePushCommand) { c.ProfileID = "other" },
	} {
		changed := cmd
		mutate(&changed)
		if _, err := repo.ApplyApplePush(ctx, changed, cipher); !errors.Is(err, ErrPushGenerationConflict) {
			t.Fatalf("changed intent=%v", err)
		}
	}
	if _, err := repo.UpsertApple(ctx, appleLegacyCommand(cmd), cipher); !errors.Is(err, ErrPushLegacyWriter) {
		t.Fatalf("bridge=%v", err)
	}
	if err := repo.DeleteByProfileDevice(ctx, cmd.ProfileID, cmd.DeviceID); !errors.Is(err, ErrPushLegacyWriter) {
		t.Fatalf("bridge delete=%v", err)
	}
	wrong := cmd
	wrong.InstallationKey = base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("x", 32)))
	if _, err := repo.ApplyApplePush(ctx, wrong, cipher); !errors.Is(err, ErrPushInstallationProof) {
		t.Fatalf("proof=%v", err)
	}
	cmd.Generation = 3
	if _, err := repo.ApplyApplePush(ctx, cmd, cipher); err != nil {
		t.Fatal(err)
	}
	cmd.Generation = 2
	if _, err := repo.ApplyApplePush(ctx, cmd, cipher); !errors.Is(err, ErrPushGenerationConflict) {
		t.Fatalf("old=%v", err)
	}
	cmd.APNsTopic = "unsupported"
	if _, err := repo.ApplyApplePush(ctx, cmd, cipher); err == nil {
		t.Fatal("unsupported topic accepted")
	}
}

func TestOrderedApplePushWaitsForCommittedGeneration(t *testing.T) {
	for _, bridge := range []bool{false, true} {
		t.Run(fmt.Sprintf("bridge=%v", bridge), func(t *testing.T) {
			_, fixturePool := newPushDeviceTestRepo(t)
			schema := "push_order_" + strings.ToLower(ulid.Make().String())
			quoted := pgx.Identifier{schema}.Sanitize()
			ctx := t.Context()
			if _, err := fixturePool.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _, _ = fixturePool.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE") })
			for _, table := range []string{"push_devices", "android_push_installations", "apple_push_installations"} {
				if _, err := fixturePool.Exec(ctx, "CREATE TABLE "+quoted+"."+table+" (LIKE "+table+" INCLUDING ALL)"); err != nil {
					t.Fatal(err)
				}
			}
			cfg := fixturePool.Config()
			cfg.MaxConns = 3
			cfg.ConnConfig.RuntimeParams["search_path"] = schema
			cfg.ConnConfig.RuntimeParams["application_name"] = schema
			pool, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			repo := NewPushDeviceRepository(pool)
			cmd := orderedAppleCommand()
			cipher := testPushCipher(t)
			if _, err = repo.ApplyApplePush(ctx, cmd, cipher); err != nil {
				t.Fatal(err)
			}
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if _, err = tx.Exec(ctx, `UPDATE apple_push_installations SET generation=3 WHERE device_id=$1`, cmd.DeviceID); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				if bridge {
					_, e := repo.UpsertApple(ctx, appleLegacyCommand(cmd), cipher)
					done <- e
					return
				}
				cmd.Generation = 2
				_, e := repo.ApplyApplePush(ctx, cmd, cipher)
				done <- e
			}()
			deadline := time.NewTimer(5 * time.Second)
			defer deadline.Stop()
			tick := time.NewTicker(5 * time.Millisecond)
			defer tick.Stop()
			for {
				var blocked bool
				if err = fixturePool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND cardinality(pg_blocking_pids(pid))>0)`, schema).Scan(&blocked); err != nil {
					t.Fatal(err)
				}
				if blocked {
					break
				}
				select {
				case e := <-done:
					t.Fatalf("writer returned before lock release: %v", e)
				case <-deadline.C:
					t.Fatal("writer never waited on the database lock")
				case <-tick.C:
				}
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case e := <-done:
				want := ErrPushGenerationConflict
				if bridge {
					want = ErrPushLegacyWriter
				}
				if !errors.Is(e, want) {
					t.Fatalf("after commit=%v want=%v", e, want)
				}
			case <-deadline.C:
				t.Fatal("writer did not finish")
			}
		})
	}
}

func TestOrderedApplePushReplacementRollbackAndAttemptRetirement(t *testing.T) {
	repo, pool := newPushDeviceTestRepo(t)
	ctx := t.Context()
	cipher := testPushCipher(t)
	cmd := orderedAppleCommand()
	first, err := repo.ApplyApplePush(ctx, cmd, cipher)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `CREATE TEMP TABLE push_delivery_attempts(id text PRIMARY KEY,push_device_id text REFERENCES push_devices(id) ON DELETE CASCADE); ALTER TABLE push_devices ADD CONSTRAINT reject_off_fixture CHECK(push_mode<>'off')`); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO push_delivery_attempts VALUES('pending',$1)`, first.RegistrationID); err != nil {
		t.Fatal(err)
	}
	cmd.Generation = 2
	cmd.PushMode = PushModeOff
	if _, err = repo.ApplyApplePush(ctx, cmd, cipher); err == nil {
		t.Fatal("synthetic insertion failure did not fail")
	}
	var generation int64
	var attempts int
	if err = pool.QueryRow(ctx, `SELECT generation FROM apple_push_installations WHERE device_id=$1`, cmd.DeviceID).Scan(&generation); err != nil || generation != 1 {
		t.Fatalf("rollback generation=%d err=%v", generation, err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM push_delivery_attempts`).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("rollback attempts=%d err=%v", attempts, err)
	}
	if _, err = repo.getPushDeviceByID(ctx, first.RegistrationID); err != nil {
		t.Fatal("failed replacement lost previous device")
	}
	if _, err = pool.Exec(ctx, `ALTER TABLE push_devices DROP CONSTRAINT reject_off_fixture`); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.ApplyApplePush(ctx, cmd, cipher); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM push_delivery_attempts`).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("replaced attempts=%d err=%v", attempts, err)
	}
}
