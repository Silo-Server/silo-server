package trickplay

import (
	"log/slog"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/mediasample"
)

func TestServiceRecordsFailureWithClaimTokenDB(t *testing.T) {
	f := newFixture(t)
	file := f.file(t, f.library(t, "movies", true), "attempt-failure")
	f.reconcile(t)
	extractor := &fakeExtractor{err: &mediasample.Error{Reason: mediasample.ReasonTimeout}}
	s := newService(f.repo, &fakeStore{}, fakeSettings{}, extractor, "server")
	s.logger = slog.New(slog.DiscardHandler)
	job, err := f.repo.ClaimFile(t.Context(), file, s.owner, leaseDuration)
	if err != nil || job == nil {
		t.Fatalf("claim: %+v %v", job, err)
	}
	s.process(t.Context(), job)
	row, ok := f.row(t, file)
	if !ok || row.state != statePending || row.failures != 1 || row.lastError == "" {
		t.Fatalf("failure was not recorded for the claimed attempt: %+v", row)
	}
}

func TestClaimTakesRegeneratedThenNewestDB(t *testing.T) {
	f := newFixture(t)
	folder := f.library(t, "movies", true)
	oldest, older, backingOff, newest := f.file(t, folder, "oldest"), f.file(t, folder, "older"), f.file(t, folder, "backing-off"), f.file(t, folder, "newest")
	f.reconcile(t)
	// The newest file of all is backing off, and an administrator asked for
	// the oldest to be made again.
	last := f.file(t, folder, "last")
	f.reconcile(t)
	f.exec(t, `UPDATE public.media_file_trickplay SET available_at = now() + interval '1 hour' WHERE media_file_id = ANY($1)`, []int{backingOff, last})
	if _, err := f.repo.Regenerate(t.Context(), []int{oldest}); err != nil {
		t.Fatal(err)
	}
	// Claim names no file. On a database other tests share it may take one of
	// theirs, so it runs in a transaction that is rolled back.
	tx, err := f.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	job, err := f.repo.claim(t.Context(), tx, "server", time.Minute, nil)
	_ = tx.Rollback(t.Context())
	if err != nil || job == nil {
		t.Fatalf("claim of any file took %+v, error %v; want a due file", job, err)
	}
	for _, want := range []int{oldest, newest, older} {
		job, err := f.repo.claim(t.Context(), f.pool, "server", time.Minute, f.files)
		if err != nil || job == nil || job.FileID != want {
			t.Fatalf("claimed %+v, error %v; want file %d", job, err, want)
		}
	}
	if job, err := f.repo.claim(t.Context(), f.pool, "server", time.Minute, f.files); err != nil || job != nil {
		t.Fatalf("claimed %+v, error %v; want nothing due", job, err)
	}
}
