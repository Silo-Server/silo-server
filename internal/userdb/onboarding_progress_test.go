package userdb

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestOnboardingRevisionMigration(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	_, err = db.Exec(`CREATE TABLE profile_onboarding(profile_id TEXT NOT NULL,tour_id TEXT NOT NULL,last_step TEXT NOT NULL DEFAULT '',completed_at TEXT,skipped_at TEXT,updated_at TEXT NOT NULL,PRIMARY KEY(profile_id,tour_id));
 INSERT INTO profile_onboarding VALUES('p','tour','welcome','2026-01-01T00:00:00Z',NULL,'2026-01-01T00:00:00Z'); PRAGMA user_version=24;`)
	if err != nil {
		t.Fatal(err)
	}
	// Mirrors normal opening order; trigger declarations must permit old columns.
	if err := InitSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := runMigrations(db); err != nil {
		t.Fatal(err)
	}
	var revision int64
	var completed string
	if err := db.QueryRow("SELECT revision,completed_at FROM profile_onboarding").Scan(&revision, &completed); err != nil || revision != 1 || completed == "" {
		t.Fatal(revision, completed, err)
	}
	if _, err := db.Exec("UPDATE profile_onboarding SET last_step='playback'"); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT revision FROM profile_onboarding").Scan(&revision); err != nil || revision != 2 {
		t.Fatal(revision, err)
	}
}

// Exercise the public opening order, including InitSchema before migrations.
func TestOnboardingRevisionPreV14CloseReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := NewUserDB(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`DROP TABLE profile_onboarding; PRAGMA user_version=13;`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewUserDB(path, 1)
	if err != nil {
		t.Fatalf("reopen pre-v14 store: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	if version, err := userVersion(reopened.DB); err != nil || version != schemaVersion {
		t.Fatal(version, err)
	}
	if _, err := reopened.DB.Exec(`INSERT INTO profile_onboarding(profile_id,tour_id,last_step,updated_at) VALUES('p','tour','welcome','2026-01-01T00:00:00Z'); UPDATE profile_onboarding SET last_step='playback' WHERE profile_id='p'`); err != nil {
		t.Fatal(err)
	}
	var revision int
	if err := reopened.DB.QueryRow(`SELECT revision FROM profile_onboarding WHERE profile_id='p'`).Scan(&revision); err != nil || revision != 2 {
		t.Fatal(revision, err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := NewUserDB(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = again.Close() }()
	if err := again.DB.QueryRow(`SELECT revision FROM profile_onboarding WHERE profile_id='p'`).Scan(&revision); err != nil || revision != 2 {
		t.Fatal(revision, err)
	}
}
