package scanner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestMarkerSegmentsLifecyclePostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var folderID, fileID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name) VALUES ('movies', 'Marker lifecycle test') RETURNING id`).Scan(&folderID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		if _, err := pool.Exec(cleanup, `DELETE FROM media_files WHERE media_folder_id = $1`, folderID); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(cleanup, `DELETE FROM media_folders WHERE id = $1`, folderID); err != nil {
			t.Error(err)
		}
	})
	if err := pool.QueryRow(ctx, `INSERT INTO media_files (media_folder_id, file_path, duration, file_hash, file_size) VALUES ($1, $2, 1000, 'first-cut', 1000) RETURNING id`, folderID, fmt.Sprintf("/marker-lifecycle-%d.mkv", time.Now().UnixNano())).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	repo := NewFileRepository(pool)
	read := func() *models.MediaFile {
		t.Helper()
		file, err := repo.GetByID(ctx, fileID)
		if err != nil {
			t.Fatal(err)
		}
		return file
	}
	result := markers.Result{ProviderID: "provider", SourceClass: models.MarkerSourceOnline, RefreshedProviders: []string{"provider"},
		Markers: []markers.Marker{
			{Kind: markers.MarkerKindCredits, Start: 900 * time.Second, End: 950 * time.Second, Confidence: 0.9},
			{Kind: markers.MarkerKindIntro, Start: 10 * time.Second, End: 50 * time.Second, Confidence: 0.9},
			{Kind: markers.MarkerKindCredits, Start: 800 * time.Second, End: 850 * time.Second, Confidence: 0.9},
		}}
	write := func(result markers.Result) {
		t.Helper()
		update := MarkerUpdateFromPayload(markers.BuildUpdatePayload(result))
		update.ExpectedFile = read()
		if wrote, err := repo.UpsertMarkers(ctx, fileID, update); err != nil || !wrote {
			t.Fatalf("write result: wrote=%v err=%v", wrote, err)
		}
	}
	write(result)
	file := read()
	if len(file.MarkerSegments) != 3 || *file.CreditsStart != 800 || *file.CreditsEnd != 850 {
		t.Fatalf("lost occurrences: %+v", file.MarkerSegments)
	}
	result.Markers[0].End = 980 * time.Second
	write(result)
	if got := read().MarkerSegments[2].EndSeconds; got != 980 {
		t.Fatalf("same-confidence correction ignored: %v", got)
	}
	if wrote, err := repo.UpsertMarkers(ctx, fileID, MarkerUpdate{MarkersSource: models.MarkerSourceManual, IntroStart: new(20.0), IntroEnd: new(60.0)}); err != nil || !wrote {
		t.Fatalf("manual edit: %v %v", wrote, err)
	}
	write(markers.Result{RefreshedProviders: []string{"provider"}})
	file = read()
	if len(file.MarkerSegments) != 1 || *file.IntroStart != 20 || file.CreditsStart != nil {
		t.Fatal("provider miss removed manual marker or retained withdrawn ranges")
	}
	result.Markers = result.Markers[:1]
	write(result)
	expected := read()
	if _, err := pool.Exec(ctx, `INSERT INTO marker_fetch_state (media_file_id, provider, identity_key) VALUES ($1, 'provider', $2)`, fileID, models.MarkerFileIdentity(expected)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE media_files SET file_hash = 'replacement-cut', file_size = 2000 WHERE id = $1`, fileID); err != nil {
		t.Fatal(err)
	}
	file = read()
	if len(file.MarkerSegments) != 1 || file.CreditsStart != nil || *file.IntroStart != 20 {
		t.Fatal("replacement retained derived ranges or removed manual marker")
	}
	var fetches int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM marker_fetch_state WHERE media_file_id=$1`, fileID).Scan(&fetches); err != nil || fetches != 0 {
		t.Fatalf("replacement retained fetch state: %d %v", fetches, err)
	}
	update := MarkerUpdateFromPayload(markers.BuildUpdatePayload(result))
	update.ExpectedFile = expected
	if wrote, err := repo.UpsertMarkers(ctx, fileID, update); wrote || !errors.Is(err, ErrStaleMarkerUpdate) {
		t.Fatalf("stale result accepted: wrote=%v err=%v", wrote, err)
	}
}

func TestMarkerMixedMutationAtomicAuditPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var folderID, fileID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name) VALUES ('movies', 'Marker atomic test') RETURNING id`).Scan(&folderID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		for _, stmt := range []string{
			`DELETE FROM marker_edit_audit WHERE media_file_id IN (SELECT id FROM media_files WHERE media_folder_id = $1)`,
			`DELETE FROM media_files WHERE media_folder_id = $1`,
			`DELETE FROM media_folders WHERE id = $1`,
		} {
			if _, err := pool.Exec(cleanupCtx, stmt, folderID); err != nil {
				t.Errorf("clean marker fixture: %v", err)
			}
		}
	})
	if err := pool.QueryRow(ctx, `INSERT INTO media_files (media_folder_id, file_path, duration) VALUES ($1, $2, 100) RETURNING id`, folderID, fmt.Sprintf("/marker-atomic-%d.mkv", time.Now().UnixNano())).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	repo := NewFileRepository(pool)
	if wrote, err := repo.UpsertMarkers(ctx, fileID, MarkerUpdate{
		MarkersSource: models.MarkerSourceManual,
		IntroStart:    new(1.0), IntroEnd: new(10.0),
		CreditsStart: new(90.0), CreditsEnd: new(100.0),
	}); err != nil || !wrote {
		t.Fatalf("seed markers: wrote=%v err=%v", wrote, err)
	}
	snapshot := func() (string, int) {
		t.Helper()
		var row string
		var count int
		if err := pool.QueryRow(ctx, `SELECT to_jsonb(f)::text FROM media_files f WHERE id = $1`, fileID).Scan(&row); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM marker_edit_audit WHERE media_file_id = $1`, fileID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return row, count
	}
	before, beforeCount := snapshot()
	patch := MarkerUpdate{MarkersSource: models.MarkerSourceManual, IntroStart: new(2.0), IntroEnd: new(12.0)}
	auditCtx := WithMarkerAuditContext(ctx, MarkerAuditContext{RequestID: "marker-atomic"})
	// Intro would change before the later credits segment fails duration validation.
	invalid := patch
	invalid.CreditsStart, invalid.CreditsEnd = new(95.0), new(102.0)
	if wrote, err := repo.UpsertAndClearMarkers(auditCtx, fileID, invalid, []string{"credits"}); err == nil || wrote {
		t.Fatalf("invalid duration accepted: wrote=%v err=%v", wrote, err)
	}
	if after, count := snapshot(); after != before || count != beforeCount {
		t.Fatal("duration validation failure changed the file or audit")
	}
	// Invalid inet forces the audit INSERT to fail after the media row UPDATE.
	badAuditCtx := WithMarkerAuditContext(ctx, MarkerAuditContext{ClientIP: "invalid-address"})
	if wrote, err := repo.UpsertAndClearMarkers(badAuditCtx, fileID, patch, []string{"credits"}); err == nil || wrote {
		t.Fatalf("audit failure accepted: wrote=%v err=%v", wrote, err)
	}
	if after, count := snapshot(); after != before || count != beforeCount {
		t.Fatal("audit insertion failure did not roll back the entire mixed mutation")
	}
	if wrote, err := repo.UpsertAndClearMarkers(auditCtx, fileID, patch, []string{"credits"}); err != nil || !wrote {
		t.Fatalf("mixed mutation: wrote=%v err=%v", wrote, err)
	}
	var valid bool
	if err := pool.QueryRow(ctx, `SELECT intro_start = 2 AND intro_end = 12 AND credits_start IS NULL AND credits_end IS NULL FROM media_files WHERE id = $1`, fileID).Scan(&valid); err != nil || !valid {
		t.Fatalf("mixed marker state: valid=%v err=%v", valid, err)
	}
	var setCount, clearCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE segment_kind = 'intro' AND action = 'set' AND before_marker IS NOT NULL AND after_marker IS NOT NULL), count(*) FILTER (WHERE segment_kind = 'credits' AND action = 'clear' AND before_marker IS NOT NULL AND after_marker IS NULL) FROM marker_edit_audit WHERE media_file_id = $1`, fileID).Scan(&setCount, &clearCount); err != nil || setCount != 1 || clearCount != 1 {
		t.Fatalf("mixed audit: set=%d clear=%d err=%v", setCount, clearCount, err)
	}
	committed, committedCount := snapshot()
	if wrote, err := repo.UpsertAndClearMarkers(auditCtx, fileID, patch, []string{"credits"}); err != nil || wrote {
		t.Fatalf("identical replay: wrote=%v err=%v", wrote, err)
	}
	if after, count := snapshot(); after != committed || count != committedCount || count != 2 {
		t.Fatal("identical replay changed file timestamps or appended audit rows")
	}
}
