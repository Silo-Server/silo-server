package trickplay

import (
	"strings"
	"testing"
	"time"
)

func TestClaimRecordsAlgorithmVersionDB(t *testing.T) {
	f := newFixture(t)
	folder := f.library(t, "movies", true)
	file := f.file(t, folder, "older-version")
	f.exec(t, `INSERT INTO public.media_file_trickplay (media_file_id, recipe_version) VALUES ($1, $2)`, file, AlgorithmVersion-1)
	job, err := f.repo.ClaimFile(t.Context(), file, "current-server", time.Minute)
	if err != nil || job == nil {
		t.Fatalf("claim: %v %v", job, err)
	}
	row, ok := f.row(t, file)
	if !ok || row.version != AlgorithmVersion {
		t.Fatalf("claimed version = %d, want %d", row.version, AlgorithmVersion)
	}
}

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
