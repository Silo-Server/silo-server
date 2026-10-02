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

func orderedPushCommand() AndroidPushCommand {
	return AndroidPushCommand{UserID: 7, ProfileID: "profile", DeviceID: "install", InstallationKey: base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("k", 32))), Generation: 1, Token: strings.Repeat("a", 64), PushMode: PushModePrivatePush}
}
func TestOrderedAndroidPushReplayReplacementAndTombstone(t *testing.T) {
	repo, pool := newPushDeviceTestRepo(t)
	cipher := testPushCipher(t)
	ctx := t.Context()
	cmd := orderedPushCommand()
	receipt, err := repo.ApplyAndroidPush(ctx, cmd, cipher)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Generation != 1 || receipt.RegistrationID == "" || receipt.Removed {
		t.Fatalf("receipt=%+v", receipt)
	}
	if err = repo.RecordPushFailure(ctx, receipt.RegistrationID, "unregistered", true); err != nil {
		t.Fatal(err)
	}
	replay, err := repo.ApplyAndroidPush(ctx, cmd, cipher)
	if err != nil || replay != receipt {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	device, err := repo.getPushDeviceByID(ctx, receipt.RegistrationID)
	if err != nil || device.Enabled {
		t.Fatalf("replay reenabled: device=%+v err=%v", device, err)
	}
	cmd.Token = strings.Repeat("b", 64)
	if _, err = repo.ApplyAndroidPush(ctx, cmd, cipher); !errors.Is(err, ErrPushGenerationConflict) {
		t.Fatalf("changed same generation=%v", err)
	}
	cmd.Generation = 3
	cmd.ProfileID = "next-profile"
	replacement, err := repo.ApplyAndroidPush(ctx, cmd, cipher)
	if err != nil || replacement.RegistrationID == receipt.RegistrationID {
		t.Fatalf("replacement=%+v err=%v", replacement, err)
	}
	if err = repo.RecordPushFailure(ctx, receipt.RegistrationID, "late-unregistered", true); err != nil {
		t.Fatal(err)
	}
	current, err := repo.getPushDeviceByID(ctx, replacement.RegistrationID)
	if err != nil || !current.Enabled {
		t.Fatalf("late failure changed new registration: %v", err)
	}
	plaintext, err := cipher.Decrypt(current.FCMTokenCiphertext, pushDeviceFCMTokenAAD(current.ID))
	if err != nil || plaintext != cmd.Token {
		t.Fatalf("cipher roundtrip=%v", err)
	}
	old := orderedPushCommand()
	old.Generation = 2
	if _, err = repo.ApplyAndroidPush(ctx, old, cipher); !errors.Is(err, ErrPushGenerationConflict) {
		t.Fatalf("late registration=%v", err)
	}
	old.Remove = true
	old.Token = ""
	old.PushMode = ""
	old.Generation = 4
	if _, err = repo.ApplyAndroidPush(ctx, old, cipher); !errors.Is(err, ErrPushGenerationConflict) {
		t.Fatalf("old-profile removal=%v", err)
	}
	cmd.Remove = true
	cmd.Token = ""
	cmd.PushMode = ""
	cmd.Generation = 4
	removed, err := repo.ApplyAndroidPush(ctx, cmd, cipher)
	if err != nil || !removed.Removed {
		t.Fatalf("remove=%+v err=%v", removed, err)
	}
	if again, err := repo.ApplyAndroidPush(ctx, cmd, cipher); err != nil || again != removed {
		t.Fatalf("remove replay=%+v err=%v", again, err)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM push_devices`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("device rows=%d err=%v", count, err)
	}
	old.Generation = 1
	old.Remove = false
	old.Token = strings.Repeat("a", 64)
	old.PushMode = PushModePrivatePush
	if _, err = repo.ApplyAndroidPush(ctx, old, cipher); !errors.Is(err, ErrPushGenerationConflict) {
		t.Fatalf("post-tombstone replay=%v", err)
	}
}
func TestOrderedAndroidPushFencesBridgeAndBootstrap(t *testing.T) {
	repo, _ := newPushDeviceTestRepo(t)
	ctx := t.Context()
	cipher := testPushCipher(t)
	cmd := orderedPushCommand()
	legacy := FCMPushDeviceRegistration{UserID: 8, ProfileID: "other", DeviceID: cmd.DeviceID, FCMToken: cmd.Token, PushMode: cmd.PushMode}
	if _, err := repo.UpsertFCM(ctx, legacy, cipher); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ApplyAndroidPush(ctx, cmd, cipher); !errors.Is(err, ErrPushGenerationConflict) {
		t.Fatalf("foreign bootstrap=%v", err)
	}
	cmd.UserID = 8
	cmd.ProfileID = "other"
	if _, err := repo.ApplyAndroidPush(ctx, cmd, cipher); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.UpsertFCM(ctx, legacy, cipher); !errors.Is(err, ErrPushLegacyWriter) {
		t.Fatalf("legacy register=%v", err)
	}
	if err := repo.DeleteByProfileDevice(ctx, cmd.ProfileID, cmd.DeviceID); !errors.Is(err, ErrPushLegacyWriter) {
		t.Fatalf("legacy remove=%v", err)
	}
	cmd.Generation = 2
	cmd.InstallationKey = base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("x", 32)))
	if _, err := repo.ApplyAndroidPush(ctx, cmd, cipher); !errors.Is(err, ErrPushInstallationProof) {
		t.Fatalf("wrong installation proof=%v", err)
	}
}

func TestOrderedAndroidPushWaitsForCommittedGeneration(t *testing.T) {
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
			for _, table := range []string{"push_devices", "android_push_installations"} {
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
			cmd := orderedPushCommand()
			cipher := testPushCipher(t)
			if _, err = repo.ApplyAndroidPush(ctx, cmd, cipher); err != nil {
				t.Fatal(err)
			}
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if _, err = tx.Exec(ctx, `UPDATE android_push_installations SET generation=3 WHERE device_id=$1`, cmd.DeviceID); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				if bridge {
					_, e := repo.UpsertFCM(ctx, FCMPushDeviceRegistration{UserID: cmd.UserID, ProfileID: cmd.ProfileID, DeviceID: cmd.DeviceID, FCMToken: cmd.Token, PushMode: cmd.PushMode}, cipher)
					done <- e
					return
				}
				cmd.Generation = 2
				_, e := repo.ApplyAndroidPush(ctx, cmd, cipher)
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

func TestOrderedAndroidPushReplacementRollbackAndAttemptRetirement(t *testing.T) {
	repo, pool := newPushDeviceTestRepo(t)
	ctx := t.Context()
	cipher := testPushCipher(t)
	cmd := orderedPushCommand()
	first, err := repo.ApplyAndroidPush(ctx, cmd, cipher)
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
	if _, err = repo.ApplyAndroidPush(ctx, cmd, cipher); err == nil {
		t.Fatal("synthetic insertion failure did not fail")
	}
	var generation int64
	var attempts int
	if err = pool.QueryRow(ctx, `SELECT generation FROM android_push_installations WHERE device_id=$1`, cmd.DeviceID).Scan(&generation); err != nil || generation != 1 {
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
	if _, err = repo.ApplyAndroidPush(ctx, cmd, cipher); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM push_delivery_attempts`).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("replaced attempts=%d err=%v", attempts, err)
	}
}
