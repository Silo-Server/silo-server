package trickplay

import (
	"log/slog"
	"testing"

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
