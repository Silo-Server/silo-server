package downloads

import (
	"context"
	"errors"
	"github.com/Silo-Server/silo-server/internal/models"
	"runtime"
	"testing"
	"time"
)

func TestManagedCreateRevisionPostgres(t *testing.T) {
	repo := statusEventTestRepo(t)
	svc := &Service{repo: repo}
	now := time.Now().UTC()
	original := &Download{ID: "entry", UserID: 1, ProfileID: "profile", DeviceID: "device", ContentID: "movie", MediaFileID: 42, Kind: KindQueued, Status: StatusReady, Format: FormatOriginal, Quality: QualityOriginal, EffectiveQuality: QualityOriginal, FileSize: 100, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := repo.Create(t.Context(), original); err != nil {
		t.Fatal(err)
	}
	target := *original
	target.MediaFileID = 43
	target.FileSize = 200
	// A missing-row precondition cannot replace a registered choice.
	if _, err := svc.reuseOrReplaceManaged(t.Context(), original, &target, new(0), "entry"); !errors.Is(err, ErrStatusConflict) {
		t.Fatal(err)
	}
	stored, err := svc.reuseOrReplaceManaged(t.Context(), original, &target, new(1), "entry")
	if err != nil || stored.Revision != 2 || stored.MediaFileID != 43 {
		t.Fatalf("%+v %v", stored, err)
	}
	// A stale request cannot restore the old target after a later choice.
	if _, err := svc.reuseOrReplaceManaged(t.Context(), stored, original, new(1), "entry"); !errors.Is(err, ErrStatusConflict) {
		t.Fatal(err)
	}
	// Reproduce a losing CAS: the caller checked revision1 in its earlier
	// snapshot, but another target has already won revision2 in the database.
	losing := *original
	losing.MediaFileID = 44
	if _, err := svc.reuseOrReplaceManaged(t.Context(), original, &losing, new(1), "entry"); !errors.Is(err, ErrStatusConflict) {
		t.Fatalf("acknowledged different winner: %v", err)
	}
	current, err := repo.GetManagedEntry(t.Context(), 1, "profile", "device", "movie", "")
	if err != nil || current.MediaFileID != 43 || current.Revision != 2 {
		t.Fatalf("changed winner %+v %v", current, err)
	}
	same, err := svc.reuseOrReplaceManaged(t.Context(), current, &target, new(2), "entry")
	if err != nil || same.Revision != 2 {
		t.Fatalf("unnecessary replacement %+v %v", same, err)
	}
	recreated := *current
	recreated.ID = "recreated"
	recreated.Revision = 1
	if err := checkManagedCreateRevision(&recreated, new(1), "entry"); !errors.Is(err, ErrStatusConflict) {
		t.Fatal("old identity authorized a recreated entry")
	}
	// Frozen bridge calls have no revision requirement and keep existing behavior.
	bridge, err := svc.reuseOrReplaceManaged(t.Context(), current, original, nil, "")
	if err != nil || bridge.MediaFileID != 42 || bridge.Revision != 3 {
		t.Fatalf("bridge %+v %v", bridge, err)
	}
	// Stale native prepared-file requests are rejected before artifact admission.
	// No artifact manager is configured: reaching Ensure would panic this test.
	if _, err := svc.createArtifactDownload(t.Context(), 1, CreateRequest{ProfileID: "profile", DeviceID: "device", ExpectedRevision: new(1), ExpectedDownloadID: "entry"}, &models.MediaFile{ID: 42, ContentID: "movie"}, QualityDecision{}); !errors.Is(err, ErrStatusConflict) {
		t.Fatalf("artifact admission %v", err)
	}
	if err := checkManagedCreateRevision(nil, new(3), "entry"); !errors.Is(err, ErrStatusConflict) {
		t.Fatal("revived a deleted target")
	}
	if err := checkManagedCreateRevision(nil, new(0), ""); err != nil {
		t.Fatal(err)
	}
}

func TestManagedCreateAbsenceRacePostgres(t *testing.T) {
	repo := statusEventTestRepo(t)
	svc := &Service{repo: repo}
	_, err := repo.pool.Exec(t.Context(), `CREATE UNIQUE INDEX managed_identity ON downloads(user_id,profile_id,device_id,content_id,COALESCE(episode_id,'')) WHERE device_id IS NOT NULL;
 CREATE TABLE user_devices(user_id integer,profile_id text,device_id text,device_name text,device_platform text,last_seen_at timestamptz,PRIMARY KEY(user_id,profile_id,device_id))`)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := repo.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	if _, err := tx.Exec(t.Context(), `SELECT pg_advisory_xact_lock($1,$2)`, downloadQuotaLockClassID, 1); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := svc.ensureManaged(ctx, 1, CreateRequest{ProfileID: "profile", DeviceID: "device", ExpectedEntries: map[string]ManagedCreateExpectation{"episode": {Revision: 0}}}, []managedItem{{file: &models.MediaFile{ID: 42, FileSize: 10}, contentID: "series", episodeID: "episode"}}, originalDecision(), "intent")
		done <- err
	}()
	// Observe the contender's quota-lock wait, proving its initial absence read
	// has completed before the competing registry row is inserted.
	for {
		var waiting bool
		err := repo.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND classid=$1 AND objid=1 AND NOT granted)`, downloadQuotaLockClassID).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		runtime.Gosched()
	}
	now := time.Now()
	winner := &Download{ID: "winner", UserID: 1, ProfileID: "profile", DeviceID: "device", ContentID: "series", EpisodeID: "episode", MediaFileID: 99, Kind: KindQueued, Status: StatusReady, Format: FormatOriginal, Quality: QualityOriginal, EffectiveQuality: QualityOriginal, FileSize: 20, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := repo.Create(ctx, winner); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrStatusConflict) {
		t.Fatalf("acknowledged concurrent create: %v", err)
	}
	row, err := repo.GetManagedEntry(ctx, 1, "profile", "device", "series", "episode")
	if err != nil || row.ID != "winner" || row.MediaFileID != 99 {
		t.Fatalf("changed winner %+v %v", row, err)
	}
}
