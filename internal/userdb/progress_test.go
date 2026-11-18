package userdb

import (
	"testing"
	"time"
)

// ClearResumePosition must only move a row's timestamps forward. The Jellyfin
// path stores fractional seconds, which a whole-second clear in the same second
// sorts after as text although it is earlier (issue #2134 review).
func TestClearResumePositionNeverMovesTimestampsBack(t *testing.T) {
	const updatedAt, eventAt = "2026-10-14T23:48:50.220Z", "2026-10-14T23:48:50.220123456Z"
	for _, tc := range []struct {
		name                       string
		at                         time.Time
		wantUpdatedAt, wantEventAt string
	}{
		{"same second as a fractional write", time.Date(2026, 10, 14, 23, 48, 50, 900_000_000, time.UTC), updatedAt, eventAt},
		{"a later second", time.Date(2026, 10, 14, 23, 48, 51, 0, time.UTC), "2026-10-14T23:48:51Z", "2026-10-14T23:48:51Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newSchemaDB(t)
			if _, err := db.Exec(`
				INSERT INTO watch_progress (profile_id, media_item_id, position_seconds, duration_seconds, completed, updated_at, event_at)
				VALUES ('p1', 'm1', 600, 5400, 0, ?, ?)`, updatedAt, eventAt); err != nil {
				t.Fatalf("seed progress: %v", err)
			}
			var seqBefore int64
			if err := db.QueryRow(`SELECT synced_seq FROM watch_progress WHERE profile_id = 'p1' AND media_item_id = 'm1'`).Scan(&seqBefore); err != nil {
				t.Fatalf("read seq: %v", err)
			}

			cleared, err := ClearResumePosition(db, "p1", "m1", tc.at)
			if err != nil || !cleared {
				t.Fatalf("ClearResumePosition = %v, %v; want cleared", cleared, err)
			}

			var position float64
			var gotUpdatedAt, gotEventAt string
			var seqAfter int64
			if err := db.QueryRow(`
				SELECT position_seconds, updated_at, event_at, synced_seq
				FROM watch_progress WHERE profile_id = 'p1' AND media_item_id = 'm1'`).Scan(&position, &gotUpdatedAt, &gotEventAt, &seqAfter); err != nil {
				t.Fatalf("read progress: %v", err)
			}
			if position != 0 || gotUpdatedAt != tc.wantUpdatedAt || gotEventAt != tc.wantEventAt {
				t.Fatalf("after clear: position %v, updated_at %q, event_at %q; want 0, %q, %q", position, gotUpdatedAt, gotEventAt, tc.wantUpdatedAt, tc.wantEventAt)
			}
			if seqAfter <= seqBefore {
				t.Fatalf("synced_seq = %d, want past %d so delta sync carries the clear", seqAfter, seqBefore)
			}
		})
	}
}
