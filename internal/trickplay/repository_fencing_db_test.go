package trickplay

import (
	"strings"
	"testing"
	"time"
)

func TestTrickplayQueuesBigintMediaFileIDsDB(t *testing.T) {
	f := newFixture(t)
	folder := f.library(t, "movies", true)
	initialID := f.file(t, folder, "bigint-file")
	fileID := initialID + 1<<33
	f.exec(t, `UPDATE public.media_files SET id = $1 WHERE id = $2`, fileID, initialID)
	f.files = append(f.files, fileID)
	f.reconcile(t)
	if _, exists := f.row(t, fileID); !exists {
		t.Fatal("a bigint media file was not queued")
	}
	revision := f.generate(t, fileID, "server")
	manifests, err := f.repo.Manifests(t.Context(), []int{fileID}, testStore)
	if err != nil || manifests[fileID].Revision != revision {
		t.Fatalf("bigint manifest: %+v %v", manifests, err)
	}
	prefix := revisionPrefix(fileID, revision)
	live, err := BlobNamespace().Live(t.Context(), f.pool, []string{prefix})
	if err != nil || !live[prefix] {
		t.Fatalf("bigint revision liveness: %+v %v", live, err)
	}
}

func TestExpiredLeaseRejectsCompletionBeforeReconcileDB(t *testing.T) {
	f := newFixture(t)
	folder := f.library(t, "movies", true)
	for _, completion := range []string{"publish", "finish"} {
		t.Run(completion, func(t *testing.T) {
			file := f.file(t, folder, completion)
			f.reconcile(t)
			job, err := f.repo.ClaimFile(t.Context(), file, "expired-server", time.Minute)
			if err != nil || job == nil {
				t.Fatalf("claim: %v %v", job, err)
			}
			revision, ok, err := f.repo.BeginUpload(t.Context(), file, job.LeaseToken)
			if err != nil || !ok {
				t.Fatalf("begin upload: %v %v", ok, err)
			}
			f.exec(t, `UPDATE public.media_file_trickplay SET lease_expires_at = now() - interval '1 second' WHERE media_file_id = $1`, file)
			if completion == "publish" {
				ok, err = f.repo.Publish(t.Context(), file, job.LeaseToken, revision, Published{Recipe: testRecipe, StoreIdentity: testStore, Height: 168, Count: 1, SheetBytes: []int{1}})
			} else {
				ok, err = f.repo.Finish(t.Context(), file, job.LeaseToken, Failed, "late failure", 0)
			}
			if err != nil || ok {
				t.Fatalf("expired %s accepted = %v, error = %v", completion, ok, err)
			}
		})
	}
}

func TestFinishAcceptsTruncatedUTF8ErrorDB(t *testing.T) {
	f := newFixture(t)
	folder := f.library(t, "movies", true)
	file := f.file(t, folder, "utf8-error")
	f.reconcile(t)
	job, err := f.repo.ClaimFile(t.Context(), file, "server", time.Minute)
	if err != nil || job == nil {
		t.Fatalf("claim: %v %v", job, err)
	}
	ok, err := f.repo.Finish(t.Context(), file, job.LeaseToken, Failed, strings.Repeat("a", 999)+"é", 0)
	if err != nil || !ok {
		t.Fatalf("finish: %v %v", ok, err)
	}
	row, _ := f.row(t, file)
	if row.state != statePending || row.failures != 1 {
		t.Fatalf("failure was not recorded: %+v", row)
	}
	if row.lastError != strings.Repeat("a", 999) {
		t.Fatalf("stored error = %q, want 999 ASCII bytes without a split UTF-8 character", row.lastError)
	}
}

func TestSameOwnerReclaimRejectsPriorAttemptDB(t *testing.T) {
	f := newFixture(t)
	folder := f.library(t, "movies", true)
	for _, operation := range []string{"heartbeat", "upload", "publish", "finish"} {
		t.Run(operation, func(t *testing.T) {
			file := f.file(t, folder, operation)
			f.reconcile(t)
			old, err := f.repo.ClaimFile(t.Context(), file, "same-process", time.Minute)
			if err != nil || old == nil {
				t.Fatalf("old claim: %+v %v", old, err)
			}
			f.exec(t, `UPDATE public.media_file_trickplay SET lease_expires_at = now() - interval '1 second' WHERE media_file_id = $1`, file)
			f.reconcile(t)
			f.exec(t, `UPDATE public.media_file_trickplay SET available_at = '-infinity' WHERE media_file_id = $1`, file)
			current, err := f.repo.ClaimFile(t.Context(), file, "same-process", time.Minute)
			if err != nil || current == nil {
				t.Fatalf("current claim: %+v %v", current, err)
			}
			if old.LeaseToken == "" || old.LeaseToken == current.LeaseToken {
				t.Fatalf("claim token was reused: old=%q current=%q", old.LeaseToken, current.LeaseToken)
			}
			var revision int64
			if operation != "upload" {
				var ok bool
				revision, ok, err = f.repo.BeginUpload(t.Context(), file, current.LeaseToken)
				if err != nil || !ok {
					t.Fatalf("current upload: %v %v", ok, err)
				}
			}
			var accepted bool
			switch operation {
			case "heartbeat":
				accepted, err = f.repo.Heartbeat(t.Context(), file, old.LeaseToken, time.Minute)
			case "upload":
				_, accepted, err = f.repo.BeginUpload(t.Context(), file, old.LeaseToken)
			case "publish":
				accepted, err = f.repo.Publish(t.Context(), file, old.LeaseToken, revision, Published{Recipe: testRecipe, StoreIdentity: testStore, Height: 168, Count: 1, SheetBytes: []int{1}})
			case "finish":
				accepted, err = f.repo.Finish(t.Context(), file, old.LeaseToken, Failed, "stale attempt", 0)
			}
			if err != nil || accepted {
				t.Fatalf("stale %s accepted=%v error=%v", operation, accepted, err)
			}
			if ok, err := f.repo.Heartbeat(t.Context(), file, current.LeaseToken, time.Minute); err != nil || !ok {
				t.Fatalf("current attempt lost its lease: %v %v", ok, err)
			}
			if queued := f.queued(t, file); len(queued) != 0 {
				t.Fatalf("stale attempt queued current work: %v", queued)
			}
		})
	}
}

// TestFinishMakesRepeatedDamageUnusableDB covers #5088: a failure that blames
// the file's data backs off, and only damagedAttempts of them in a row, with
// nothing else between, make the file unusable.
func TestFinishMakesRepeatedDamageUnusableDB(t *testing.T) {
	f := newFixture(t)
	folder := f.library(t, "movies", true)
	for _, tc := range []struct {
		name string
		// D: Damaged, F: Failed, E: lease expired, X: lease expired and taken
		// back by a regenerate while the file was missing, R: regenerated,
		// C: file size changed, L: duration changed
		steps string
		state string
	}{
		{"first verdict backs off", "D", statePending},
		{"fourth in a row is final", "DDDD", stateUnusable},
		{"another failure restarts the count", "DDDFDDD", statePending},
		{"an expired lease restarts the count", "DDDEDDD", statePending},
		{"an expired lease a regenerate takes back restarts the count", "DDDXDDD", statePending},
		{"a regenerate restarts the count", "DDDRDDD", statePending},
		{"a replaced file restarts the count", "DDDCDDD", statePending},
		{"a changed duration restarts the count", "DDDLDDD", statePending},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := f.file(t, folder, tc.name)
			f.reconcile(t)
			for _, step := range tc.steps {
				switch step {
				case 'R':
					if _, err := f.repo.Regenerate(t.Context(), []int{file}); err != nil {
						t.Fatal(err)
					}
					continue
				case 'C':
					f.exec(t, `UPDATE public.media_files SET file_size = file_size + 1 WHERE id = $1`, file)
					continue
				case 'L':
					f.exec(t, `UPDATE public.media_files SET duration = duration + 3 WHERE id = $1`, file)
					continue
				}
				f.exec(t, `UPDATE public.media_file_trickplay SET available_at = now() WHERE media_file_id = $1`, file)
				job, err := f.repo.ClaimFile(t.Context(), file, "server", time.Minute)
				if err != nil || job == nil {
					t.Fatalf("claim before %c: %v %v", step, job, err)
				}
				switch step {
				case 'E':
					f.exec(t, `UPDATE public.media_file_trickplay SET lease_expires_at = now() - interval '1 second' WHERE media_file_id = $1`, file)
					f.reconcile(t)
				case 'X':
					// A missing file is not requeued, so only the reclaim runs.
					f.exec(t, `UPDATE public.media_file_trickplay SET lease_expires_at = now() - interval '1 second' WHERE media_file_id = $1`, file)
					f.exec(t, `UPDATE public.media_files SET missing_since = now() WHERE id = $1`, file)
					if _, err := f.repo.Regenerate(t.Context(), []int{file}); err != nil {
						t.Fatal(err)
					}
					f.exec(t, `UPDATE public.media_files SET missing_since = NULL WHERE id = $1`, file)
				default:
					outcome := Damaged
					if step == 'F' {
						outcome = Failed
					}
					if ok, err := f.repo.Finish(t.Context(), file, job.LeaseToken, outcome, "ffmpeg sampling failed (exit)", 0); err != nil || !ok {
						t.Fatalf("finish %c: %v %v", step, ok, err)
					}
				}
			}
			row, _ := f.row(t, file)
			if row.state != tc.state || (row.state == statePending && row.due) {
				t.Fatalf("after %s: %+v, want %s and backing off", tc.steps, row, tc.state)
			}
		})
	}
}
