package handlers

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// singleFileResolver answers one file per call and counts the calls.
type singleFileResolver struct{ calls []int }

func (r *singleFileResolver) GetByID(_ context.Context, id int) (*models.MediaFile, error) {
	r.calls = append(r.calls, id)
	return &models.MediaFile{ID: id}, nil
}

// batchFileResolver also answers many files in one call, as the catalog's
// file repository does.
type batchFileResolver struct {
	singleFileResolver
	batches [][]int
}

func (r *batchFileResolver) GetByIDs(_ context.Context, ids []int) ([]*models.MediaFile, error) {
	r.batches = append(r.batches, slices.Clone(ids))
	files := make([]*models.MediaFile, 0, len(ids))
	for _, id := range ids {
		files = append(files, &models.MediaFile{ID: id})
	}
	return files, nil
}

// failingBatchResolver fails every batch, as one unreadable file does.
type failingBatchResolver struct{ singleFileResolver }

func (r *failingBatchResolver) GetByIDs(context.Context, []int) ([]*models.MediaFile, error) {
	return nil, errors.New("scanning a media file: bad chapters")
}

// TestPausedCandidateFilesReadsEachFileOnce pins that a page of paused-timeout
// candidates reads its files in one lookup when the resolver can batch, and
// once per distinct file when it can't or the batch fails, however many
// sessions share a file.
func TestPausedCandidateFilesReadsEachFileOnce(t *testing.T) {
	candidates := []pausedTimeoutCandidate{{sessionID: "a", mediaFileID: 7}, {sessionID: "b", mediaFileID: 9}, {sessionID: "c", mediaFileID: 7}}

	batch := &batchFileResolver{}
	files := (&PlaybackHandler{fileResolver: batch}).pausedCandidateFiles(t.Context(), candidates)
	if len(batch.batches) != 1 || !slices.Equal(batch.batches[0], []int{7, 9}) || len(batch.calls) != 0 {
		t.Fatalf("batch resolver: batches %v, single calls %v; want one batch of [7 9]", batch.batches, batch.calls)
	}
	if files[7] == nil || files[9] == nil {
		t.Fatalf("batch resolver files = %v, want 7 and 9", files)
	}

	single := &singleFileResolver{}
	files = (&PlaybackHandler{fileResolver: single}).pausedCandidateFiles(t.Context(), candidates)
	if !slices.Equal(single.calls, []int{7, 9}) {
		t.Fatalf("single resolver calls = %v, want [7 9]", single.calls)
	}
	if files[7] == nil || files[9] == nil {
		t.Fatalf("single resolver files = %v, want 7 and 9", files)
	}

	failing := &failingBatchResolver{}
	files = (&PlaybackHandler{fileResolver: failing}).pausedCandidateFiles(t.Context(), candidates)
	if !slices.Equal(failing.calls, []int{7, 9}) || files[7] == nil || files[9] == nil {
		t.Fatalf("after a failed batch: calls %v, files %v; want each file read once", failing.calls, files)
	}
}
