package trickplay

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
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
	live, err := BlobNamespace(f.pool).Live(t.Context(), []string{prefix})
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
			revision, ok, err := f.repo.BeginUpload(t.Context(), file, "expired-server")
			if err != nil || !ok {
				t.Fatalf("begin upload: %v %v", ok, err)
			}
			f.exec(t, `UPDATE public.media_file_trickplay SET lease_expires_at = now() - interval '1 second' WHERE media_file_id = $1`, file)
			if completion == "publish" {
				ok, err = f.repo.Publish(t.Context(), file, "expired-server", revision, Published{Recipe: testRecipe, StoreIdentity: testStore, Height: 168, Count: 1, SheetBytes: []int{1}})
			} else {
				ok, err = f.repo.Finish(t.Context(), file, "expired-server", Failed, "late failure", 0)
			}
			if err != nil || ok {
				t.Fatalf("expired %s accepted = %v, error = %v", completion, ok, err)
			}
		})
	}
}

func TestCleanErrorRetainsValidUTF8(t *testing.T) {
	message := cleanError(strings.Repeat("a", 999) + "é")
	if !utf8.ValidString(message) || len(message) > 1000 {
		t.Fatalf("invalid bounded message: %q", message)
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
	ok, err := f.repo.Finish(t.Context(), file, "server", Failed, strings.Repeat("a", 999)+"é", 0)
	if err != nil || !ok {
		t.Fatalf("finish: %v %v", ok, err)
	}
	row, _ := f.row(t, file)
	if row.state != statePending || row.failures != 1 {
		t.Fatalf("failure was not recorded: %+v", row)
	}
}
