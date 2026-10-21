package trickplay

import (
	"testing"
	"time"
)

func TestLibraryTypeChangeFencesWorkerProgressDB(t *testing.T) {
	for _, stage := range []string{"claim", "heartbeat", "upload", "publish"} {
		t.Run(stage, func(t *testing.T) {
			f := newFixture(t)
			folder := f.library(t, "movies", true)
			file := f.file(t, folder, stage)
			f.reconcile(t)
			var job *Job
			var revision int64
			if stage != "claim" {
				var err error
				job, err = f.repo.ClaimFile(t.Context(), file, "server", time.Hour)
				if err != nil || job == nil {
					t.Fatalf("claim eligible file: %v %v", job, err)
				}
				if stage == "publish" {
					var ok bool
					revision, ok, err = f.repo.BeginUpload(t.Context(), file, job.LeaseToken)
					if err != nil || !ok {
						t.Fatalf("begin eligible upload: %v %v", ok, err)
					}
				}
			}
			f.exec(t, `UPDATE media_folders SET type='audiobooks' WHERE id=$1`, folder)
			switch stage {
			case "claim":
				if claimed, err := f.repo.ClaimFile(t.Context(), file, "server", time.Hour); err != nil || claimed != nil {
					t.Errorf("unsupported claim: %+v %v", claimed, err)
				}
			case "heartbeat":
				if ok, err := f.repo.Heartbeat(t.Context(), file, job.LeaseToken, time.Hour); err != nil || ok {
					t.Errorf("unsupported lease renewal: %v %v", ok, err)
				}
			case "upload":
				if _, ok, err := f.repo.BeginUpload(t.Context(), file, job.LeaseToken); err != nil || ok {
					t.Errorf("unsupported upload: %v %v", ok, err)
				}
			case "publish":
				if ok, err := f.repo.Publish(t.Context(), file, job.LeaseToken, revision, Published{Recipe: testRecipe, StoreIdentity: testStore, Height: 168, Count: 360, SheetBytes: []int{1}}); err != nil || ok {
					t.Errorf("unsupported publication: %v %v", ok, err)
				}
				if ok, err := f.repo.Finish(t.Context(), file, job.LeaseToken, Released, "library type changed", 0); err != nil || !ok {
					t.Errorf("abandoned upload cleanup: %v %v", ok, err)
				}
				if queued := f.queued(t, file); len(queued) != 1 || queued[0] != revisionPrefix(file, revision) {
					t.Errorf("abandoned revision cleanup: %v", queued)
				}
			}
			if job != nil && stage != "publish" {
				if ok, err := f.repo.Finish(t.Context(), file, job.LeaseToken, Released, "library type changed", 0); err != nil || !ok {
					t.Fatalf("finish unsupported work: %v %v", ok, err)
				}
			}
			f.reconcile(t)
			if row, ok := f.row(t, file); ok {
				t.Errorf("finished unsupported work retained: %+v", row)
			}
		})
	}
}

func TestLibraryTypeChangeRetiresFinishedAndExpiredWorkDB(t *testing.T) {
	for _, state := range []string{statePending, stateReady, stateRunning} {
		t.Run(state, func(t *testing.T) {
			f := newFixture(t)
			folder := f.library(t, "movies", true)
			file := f.file(t, folder, state)
			f.reconcile(t)
			var revision int64
			switch state {
			case stateReady:
				revision = f.generate(t, file, "server")
			case stateRunning:
				job, err := f.repo.ClaimFile(t.Context(), file, "server", time.Hour)
				if err != nil || job == nil {
					t.Fatalf("claim: %v %v", job, err)
				}
				var ok bool
				revision, ok, err = f.repo.BeginUpload(t.Context(), file, job.LeaseToken)
				if err != nil || !ok {
					t.Fatalf("upload: %v %v", ok, err)
				}
			}
			f.exec(t, `UPDATE media_folders SET type='audiobooks' WHERE id=$1`, folder)
			f.reconcile(t)
			if state == stateRunning {
				if row, ok := f.row(t, file); !ok || row.state != stateRunning {
					t.Fatalf("active lease removed before expiry: %+v %v", row, ok)
				}
				f.exec(t, `UPDATE media_file_trickplay SET lease_expires_at=now()-interval '1 second' WHERE media_file_id=$1`, file)
				f.reconcile(t)
			}
			if row, ok := f.row(t, file); ok {
				t.Errorf("unsupported work retained: %+v", row)
			}
			if revision != 0 {
				if queued := f.queued(t, file); len(queued) != 1 || queued[0] != revisionPrefix(file, revision) {
					t.Errorf("unsupported revision not retired: %v", queued)
				}
			}
		})
	}
}

func TestIneligibleFilesCannotProgressAndCanFinishDB(t *testing.T) {
	for _, change := range []struct{ name, sql string }{
		{"opt-out", `UPDATE media_folders SET trickplay_enabled=false WHERE id=(SELECT media_folder_id FROM media_files WHERE id=$1)`},
		{"disabled", `UPDATE media_folders SET enabled=false WHERE id=(SELECT media_folder_id FROM media_files WHERE id=$1)`},
		{"missing", `UPDATE media_files SET missing_since=now() WHERE id=$1`},
		{"unprobed", `UPDATE media_files SET probe_updated_at=NULL WHERE id=$1`},
		{"audio-only", `UPDATE media_files SET video_tracks='[]' WHERE id=$1`},
		{"unknown-duration", `UPDATE media_files SET duration=NULL WHERE id=$1`},
	} {
		t.Run(change.name, func(t *testing.T) {
			f := newFixture(t)
			file := f.file(t, f.library(t, "movies", true), change.name)
			f.reconcile(t)
			job, err := f.repo.ClaimFile(t.Context(), file, "server", time.Hour)
			if err != nil || job == nil {
				t.Fatalf("claim eligible file: %v %v", job, err)
			}
			revision, ok, err := f.repo.BeginUpload(t.Context(), file, job.LeaseToken)
			if err != nil || !ok {
				t.Fatalf("begin upload: %v %v", ok, err)
			}
			f.exec(t, change.sql, file)
			if ok, err := f.repo.Heartbeat(t.Context(), file, job.LeaseToken, time.Hour); err != nil || ok {
				t.Errorf("ineligible lease renewed: %v %v", ok, err)
			}
			if ok, err := f.repo.Publish(t.Context(), file, job.LeaseToken, revision, Published{Recipe: testRecipe, StoreIdentity: testStore, Height: 168, Count: 360, SheetBytes: []int{1}}); err != nil || ok {
				t.Errorf("ineligible publication accepted: %v %v", ok, err)
			}
			if ok, err := f.repo.Finish(t.Context(), file, job.LeaseToken, Released, "file became ineligible", 0); err != nil || !ok {
				t.Fatalf("ineligible work cannot finish: %v %v", ok, err)
			}
			if queued := f.queued(t, file); len(queued) != 1 || queued[0] != revisionPrefix(file, revision) {
				t.Errorf("abandoned revision not retired: %v", queued)
			}
			if job, err := f.repo.ClaimFile(t.Context(), file, "replacement", time.Hour); err != nil || job != nil {
				t.Errorf("ineligible file reclaimed: %+v %v", job, err)
			}
		})
	}
}
