package trickplay

import (
	"fmt"
	"slices"
	"testing"
	"time"
)

// TestRegenerateReclaimsExpiredLeasesDB requeues a file whose worker died
// holding its lease, queuing the revision it had started uploading, and
// leaves a live lease alone.
func TestRegenerateReclaimsExpiredLeasesDB(t *testing.T) {
	f := newFixture(t)
	folder := f.library(t, "movies", true)
	dead, live := f.file(t, folder, "dead-worker"), f.file(t, folder, "live-worker")
	f.reconcile(t)

	job, err := f.repo.ClaimFile(t.Context(), dead, "server-a", time.Hour)
	if err != nil || job == nil {
		t.Fatalf("claim: %v %v", job, err)
	}
	revision, ok, err := f.repo.BeginUpload(t.Context(), dead, job.LeaseToken)
	if err != nil || !ok {
		t.Fatalf("begin upload: %v %v", ok, err)
	}
	f.exec(t, `UPDATE public.media_file_trickplay SET lease_expires_at = now() - interval '1 second' WHERE media_file_id = $1`, dead)
	if job, err := f.repo.ClaimFile(t.Context(), live, "server-b", time.Hour); err != nil || job == nil {
		t.Fatalf("claim live: %v %v", job, err)
	}

	requeued, err := f.repo.Regenerate(t.Context(), []int{dead, live})
	if err != nil || requeued != 1 {
		t.Fatalf("regenerate: %d %v, want only the expired lease requeued", requeued, err)
	}
	row, _ := f.row(t, dead)
	if row.state != statePending || !row.due || row.failures != 0 || row.working != nil {
		t.Fatalf("expired row %+v, want pending, due now, no failure, no work revision", row)
	}
	if want := fmt.Sprintf("trickplay/%d/%d/", dead, revision); !slices.Contains(f.queued(t, dead), want) {
		t.Fatalf("queued %v, want the abandoned revision %s", f.queued(t, dead), want)
	}
	if row, _ := f.row(t, live); row.state != stateRunning {
		t.Fatalf("live row %+v, want it left running", row)
	}
}
