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

func registerLegacyMobile(t *testing.T, repo *PushDeviceRepository, platform string, user int, profile, device, mode string) (*PushDevice, error) {
	t.Helper()
	if platform == PushPlatformApple {
		return repo.UpsertApple(t.Context(), ApplePushDeviceRegistration{
			UserID: user, ProfileID: profile, DeviceID: device, PushMode: mode,
			APNsToken: strings.Repeat("a", 64), APNsEnvironment: APNsEnvironmentProd, APNsTopic: ApplePushTopicSilo,
		}, testPushCipher(t))
	}
	return repo.UpsertFCM(t.Context(), FCMPushDeviceRegistration{
		UserID: user, ProfileID: profile, DeviceID: device, PushMode: mode, FCMToken: strings.Repeat("a", 64),
	}, testPushCipher(t))
}

// These fixtures exercise repository transactions and cascading attempts without
// constructing any sender or provider. They do not replace migrated-schema tests.
func legacyMobileAttempts(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(t.Context(), `CREATE TEMP TABLE push_delivery_attempts (
		id text PRIMARY KEY, push_device_id text REFERENCES push_devices(id) ON DELETE CASCADE,
		outcome text NOT NULL, payload text NOT NULL);
		INSERT INTO push_delivery_attempts
		SELECT id || '-' || outcome, id, outcome, 'synthetic' FROM push_devices
		CROSS JOIN unnest(ARRAY['pending','retrying','delivered']) AS outcome`)
	if err != nil {
		t.Fatal(err)
	}
}

func legacyMobileSnapshot(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var result strings.Builder
	for _, table := range []string{"push_devices", "push_delivery_attempts", "apple_push_installations", "android_push_installations"} {
		var rows string
		err := pool.QueryRow(t.Context(), `SELECT coalesce(jsonb_agg(to_jsonb(r) ORDER BY to_jsonb(r)::text), '[]')::text FROM `+pgx.Identifier{table}.Sanitize()+` r`).Scan(&rows)
		if err != nil {
			t.Fatal(err)
		}
		result.WriteString(rows)
	}
	return result.String()
}

func TestLegacyMobilePushRefusesForeignAccountDB(t *testing.T) {
	for _, platform := range []string{PushPlatformApple, PushPlatformAndroid} {
		for _, profile := range []string{"default", "different-profile"} {
			for _, disabled := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/disabled=%v", platform, profile, disabled), func(t *testing.T) {
					repo, pool := newPushDeviceTestRepo(t)
					foreign, err := registerLegacyMobile(t, repo, platform, 7, "default", "fixture-phone", PushModePrivatePush)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = registerLegacyMobile(t, repo, platform, 8, profile, "unrelated-phone", PushModePrivatePush); err != nil {
						t.Fatal(err)
					}
					if _, err = pool.Exec(t.Context(), `UPDATE push_devices SET enabled=$2 WHERE id=$1`, foreign.ID, !disabled); err != nil {
						t.Fatal(err)
					}
					// Model a pre-ordering legacy row: a refused writer must also roll
					// back the newly inserted installation lock row.
					table := pgx.Identifier{platform + "_push_installations"}.Sanitize()
					if _, err = pool.Exec(t.Context(), `DELETE FROM `+table+` WHERE device_id='fixture-phone'`); err != nil {
						t.Fatal(err)
					}
					legacyMobileAttempts(t, pool)
					before := legacyMobileSnapshot(t, pool)
					// Even an identical provider token is not installation proof.
					if _, err = registerLegacyMobile(t, repo, platform, 8, profile, "fixture-phone", PushModePrivatePush); !errors.Is(err, ErrPushGenerationConflict) {
						t.Fatalf("foreign registration error = %v, want conflict", err)
					}
					if after := legacyMobileSnapshot(t, pool); after != before {
						t.Fatal("refusal changed a destination, attempt, or installation")
					}
				})
			}
		}
	}
}

func TestLegacyMobilePushProfileSwitchRollbackDB(t *testing.T) {
	for _, platform := range []string{PushPlatformApple, PushPlatformAndroid} {
		t.Run(platform, func(t *testing.T) {
			repo, pool := newPushDeviceTestRepo(t)
			first, err := registerLegacyMobile(t, repo, platform, 7, "default", "fixture-phone", PushModePrivatePush)
			if err != nil {
				t.Fatal(err)
			}
			otherPlatform := PushPlatformApple
			if platform == PushPlatformApple {
				otherPlatform = PushPlatformAndroid
			}
			other, err := registerLegacyMobile(t, repo, otherPlatform, 8, "default", "fixture-phone", PushModePrivatePush)
			if err != nil {
				t.Fatal(err)
			}
			legacyMobileAttempts(t, pool)
			if _, err = pool.Exec(t.Context(), `ALTER TABLE push_devices ADD CONSTRAINT reject_off_fixture CHECK(push_mode<>'off')`); err != nil {
				t.Fatal(err)
			}
			before := legacyMobileSnapshot(t, pool)
			if _, err = registerLegacyMobile(t, repo, platform, 7, "next-profile", "fixture-phone", PushModeOff); err == nil {
				t.Fatal("synthetic insertion failure did not fail")
			}
			if legacyMobileSnapshot(t, pool) != before {
				t.Fatal("failed profile switch changed rows or attempts")
			}
			next, err := registerLegacyMobile(t, repo, platform, 7, "next-profile", "fixture-phone", PushModePrivatePush)
			if err != nil || next.ID == first.ID {
				t.Fatalf("same-account switch failed: %v", err)
			}
			var oldAttempts, otherAttempts, destinations int
			err = pool.QueryRow(t.Context(), `SELECT
				(SELECT count(*) FROM push_delivery_attempts WHERE push_device_id=$1),
				(SELECT count(*) FROM push_delivery_attempts WHERE push_device_id=$2),
				(SELECT count(*) FROM push_devices)`, first.ID, other.ID).Scan(&oldAttempts, &otherAttempts, &destinations)
			if err != nil || oldAttempts != 0 || otherAttempts != 3 || destinations != 2 {
				t.Fatalf("switch counts = %d/%d/%d, err=%v", oldAttempts, otherAttempts, destinations, err)
			}
			rotated, err := registerLegacyMobile(t, repo, platform, 7, "next-profile", "fixture-phone", PushModeInAppOnly)
			if err != nil || rotated.ID != next.ID || rotated.ServerDeviceID != next.ServerDeviceID || rotated.PushMode != PushModeInAppOnly {
				t.Fatalf("same-profile update failed: %v", err)
			}
		})
	}
}

func applyOrderedMobile(t *testing.T, repo *PushDeviceRepository, platform string, user int, key string, generation int64) error {
	t.Helper()
	if platform == PushPlatformApple {
		input := validApplePushInput()
		input.DeviceID = "fixture-phone"
		_, err := repo.ApplyApplePush(t.Context(), ApplePushCommand{UserID: user, ProfileID: "default", InstallationKey: key, Generation: generation, ApplePushRegistrationInput: input}, testPushCipher(t))
		return err
	}
	_, err := repo.ApplyAndroidPush(t.Context(), AndroidPushCommand{UserID: user, ProfileID: "default", DeviceID: "fixture-phone", InstallationKey: key, Generation: generation, Token: strings.Repeat("a", 64), PushMode: PushModePrivatePush}, testPushCipher(t))
	return err
}

func TestLegacyMobilePushPreservesOrderedProofDB(t *testing.T) {
	for _, platform := range []string{PushPlatformApple, PushPlatformAndroid} {
		t.Run(platform, func(t *testing.T) {
			repo, pool := newPushDeviceTestRepo(t)
			if _, err := registerLegacyMobile(t, repo, platform, 7, "default", "fixture-phone", PushModePrivatePush); err != nil {
				t.Fatal(err)
			}
			legacyMobileAttempts(t, pool)
			key := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
			before := legacyMobileSnapshot(t, pool)
			if err := applyOrderedMobile(t, repo, platform, 8, key, 1); !errors.Is(err, ErrPushGenerationConflict) {
				t.Fatalf("foreign bootstrap = %v", err)
			}
			if legacyMobileSnapshot(t, pool) != before {
				t.Fatal("foreign bootstrap changed rows")
			}
			if err := applyOrderedMobile(t, repo, platform, 7, key, 2); err != nil {
				t.Fatal(err)
			}
			before = legacyMobileSnapshot(t, pool)
			if _, err := registerLegacyMobile(t, repo, platform, 7, "next-profile", "fixture-phone", PushModePrivatePush); !errors.Is(err, ErrPushLegacyWriter) {
				t.Fatalf("ordered legacy writer = %v", err)
			}
			wrong := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("x", 32)))
			if err := applyOrderedMobile(t, repo, platform, 8, wrong, 3); !errors.Is(err, ErrPushInstallationProof) {
				t.Fatalf("wrong proof = %v", err)
			}
			if err := applyOrderedMobile(t, repo, platform, 7, key, 1); !errors.Is(err, ErrPushGenerationConflict) {
				t.Fatalf("stale generation = %v", err)
			}
			if legacyMobileSnapshot(t, pool) != before {
				t.Fatal("refused ordered/legacy commands changed rows")
			}
			// Existing installation proof still permits an ordered account transfer.
			if err := applyOrderedMobile(t, repo, platform, 8, key, 3); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLegacyMobilePushWaitsForOwnershipDB(t *testing.T) {
	for _, platform := range []string{PushPlatformApple, PushPlatformAndroid} {
		for _, commit := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/commit=%v", platform, commit), func(t *testing.T) {
				_, fixture := newPushDeviceTestRepo(t)
				schema := "push_owner_" + strings.ToLower(ulid.Make().String())
				quoted := pgx.Identifier{schema}.Sanitize()
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				if _, err := fixture.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _, _ = fixture.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE") })
				for _, table := range []string{"push_devices", "apple_push_installations", "android_push_installations"} {
					if _, err := fixture.Exec(ctx, "CREATE TABLE "+quoted+"."+table+" (LIKE "+table+" INCLUDING ALL)"); err != nil {
						t.Fatal(err)
					}
				}
				cfg := fixture.Config()
				cfg.MaxConns = 3
				cfg.ConnConfig.RuntimeParams["search_path"] = schema
				cfg.ConnConfig.RuntimeParams["application_name"] = schema
				pool, err := pgxpool.NewWithConfig(ctx, cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer pool.Close()
				repo := NewPushDeviceRepository(pool)
				tx, err := pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = tx.Rollback(context.Background()) }()
				lock := lockApplePushInstallation
				if platform == PushPlatformAndroid {
					lock = lockAndroidPushInstallation
				}
				if _, err = lock(ctx, tx, "fixture-phone"); err != nil {
					t.Fatal(err)
				}
				if _, err = tx.Exec(ctx, `INSERT INTO push_devices(id,user_id,profile_id,device_id,platform,provider,server_device_id) VALUES('fixture-owner',7,'default','fixture-phone',$1,'silo_relay','fixture-server-device')`, platform); err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() {
					_, e := registerLegacyMobile(t, repo, platform, 8, "default", "fixture-phone", PushModePrivatePush)
					done <- e
				}()
				tick := time.NewTicker(5 * time.Millisecond)
				defer tick.Stop()
				for {
					var blocked bool
					if err = fixture.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND cardinality(pg_blocking_pids(pid))>0)`, schema).Scan(&blocked); err != nil {
						t.Fatal(err)
					}
					if blocked {
						break
					}
					select {
					case e := <-done:
						t.Fatalf("writer did not wait for owner transaction: %v", e)
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					case <-tick.C:
					}
				}
				if commit {
					err = tx.Commit(ctx)
				} else {
					err = tx.Rollback(ctx)
				}
				if err != nil {
					t.Fatal(err)
				}
				select {
				case err = <-done:
					if (commit && !errors.Is(err, ErrPushGenerationConflict)) || (!commit && err != nil) {
						t.Fatalf("writer after commit=%v: %v", commit, err)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			})
		}
	}
}
