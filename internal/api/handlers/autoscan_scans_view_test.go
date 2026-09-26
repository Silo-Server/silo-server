package handlers

import (
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/autoscan"
)

func TestReadAdminAutoscanScans(t *testing.T) {
	filter := autoscan.ScanListFilter{Status: "running", Search: "needle", Limit: 7, Offset: 14}
	listed := false
	counted := false
	var failure error
	store := &fakeAutoscanStore{listScansFn: func(got autoscan.ScanListFilter) ([]autoscan.ScanWithEvent, error) {
		if got != filter {
			t.Fatal(got)
		}
		listed = true
		return []autoscan.ScanWithEvent{{ScanRunSummary: autoscan.ScanRunSummary{ID: "scan"}}}, failure
	}, countScansFn: func(got autoscan.ScanListFilter) (int, error) {
		if !listed || got != filter {
			t.Fatal(got)
		}
		counted = true
		return 99, nil
	}}
	h := NewAutoscanHandler(store, new(fakeAutoscanTriggerer))
	rows, total, err := h.ReadAdminAutoscanScans(t.Context(), filter)
	if err != nil || len(rows) != 1 || total != 99 || !counted {
		t.Fatal(rows, total, err)
	}
	counted = false
	failure = errors.New("list failed")
	_, _, err = h.ReadAdminAutoscanScans(t.Context(), filter)
	if !errors.Is(err, failure) || counted {
		t.Fatal(err, counted)
	}
	_, _, err = (*AutoscanHandler)(nil).ReadAdminAutoscanScans(t.Context(), filter)
	if !errors.Is(err, ErrAdminAutoscanScansUnavailable) {
		t.Fatal(err)
	}
}
