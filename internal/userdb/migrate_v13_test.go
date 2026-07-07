package userdb

import (
	"database/sql"
	"testing"
	"time"
)

// legacyV1StampTriggers is the pre-v13 UPDATE trigger body: it only defaulted
// event_at when NULL, leaving the LWW key stale on writes that advanced
// updated_at without hand-setting event_at.
const legacyV1StampUpdTrigger = `
CREATE TRIGGER IF NOT EXISTS watch_progress_stamp_upd AFTER UPDATE ON watch_progress
BEGIN
    UPDATE watch_progress
    SET synced_seq = (SELECT COALESCE(MAX(synced_seq), 0) + 1 FROM watch_progress),
        event_at = COALESCE(event_at, updated_at)
    WHERE rowid = NEW.rowid;
END;`

// TestMigrateToV13ReplacesStampTriggers verifies the v13 userdb migration
// upgrades an existing database's stamp triggers in place: after migrating, a
// batch mark-played advances event_at, so a queued offline event older than
// the mark can no longer win LWW (the v1 trigger left event_at frozen).
func TestMigrateToV13ReplacesStampTriggers(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}

	// Simulate an existing v12 database still carrying the v1 trigger body.
	if _, err := db.Exec(`DROP TRIGGER IF EXISTS watch_progress_stamp_upd`); err != nil {
		t.Fatalf("drop new trigger: %v", err)
	}
	if _, err := db.Exec(legacyV1StampUpdTrigger); err != nil {
		t.Fatalf("install legacy trigger: %v", err)
	}
	if _, err := db.Exec("PRAGMA user_version = 12"); err != nil {
		t.Fatalf("set user_version: %v", err)
	}

	if err := runMigrations(db); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}
	version, err := userVersion(db)
	if err != nil {
		t.Fatalf("userVersion: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}

	// Post-migration behavior: MarkProgressBatch must advance event_at.
	base := time.Now().UTC().Add(-time.Hour)
	if _, err := SetProgressIfNewer(db, "p", "m1", 100, 1000, false, base); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := MarkProgressBatch(db, "p", []string{"m1"}, time.Time{}); err != nil {
		t.Fatalf("MarkProgressBatch: %v", err)
	}
	wrote, err := SetProgressIfNewer(db, "p", "m1", 999, 1000, false, base.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("stale replay: %v", err)
	}
	if wrote {
		t.Fatal("stale offline event won after migration; v13 did not replace the stamp trigger")
	}
}
