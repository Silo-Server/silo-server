package handlers

import (
	"context"
	"errors"
	"testing"
)

type scanControlQueueFixture struct {
	canceled int
	id       int
	failure  error
}

func (*scanControlQueueFixture) EnqueueLibraryScan(context.Context, int, string) (bool, error) {
	return false, nil
}
func (*scanControlQueueFixture) EnqueueScan(context.Context, int, string, string, string) (bool, error) {
	return false, nil
}
func (*scanControlQueueFixture) CancelAcceptedByLibrary(context.Context, int) (int, error) {
	panic("wrong cancellation boundary")
}
func (q *scanControlQueueFixture) CancelByLibrary(_ context.Context, id int) (int, error) {
	q.id = id
	return q.canceled, q.failure
}
func TestScanControlsSharedCancellation(t *testing.T) {
	q := &scanControlQueueFixture{canceled: 3}
	h := &LibraryHandler{ScanQueue: q}
	result, err := h.CancelLibraryScans(t.Context(), 42)
	if err != nil || result.Canceled != 3 || result.LibraryID != 42 || q.id != 42 {
		t.Fatal(result, err, q)
	}
	q.failure = errors.New("synthetic queue failure")
	_, err = h.CancelLibraryScans(t.Context(), 42)
	if failure, ok := errors.AsType[*APIError](err); !ok || failure.Status != 500 {
		t.Fatal(err)
	}
	_, err = h.CancelLibraryScans(t.Context(), 0)
	if failure, ok := errors.AsType[*APIError](err); !ok || failure.Status != 400 {
		t.Fatal(err)
	}
	h.ScanQueue = nil
	if h.ScanControlAvailable() {
		t.Fatal("missing scanner advertised")
	}
	_, err = h.CancelLibraryScans(t.Context(), 42)
	if failure, ok := errors.AsType[*APIError](err); !ok || failure.Status != 503 {
		t.Fatal(err)
	}
}
