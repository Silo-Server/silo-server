package handlers

import (
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/autoscan"
)

func TestDeleteAdminAutoscanSource(t *testing.T) {
	var ids []string
	var failure error
	h := NewAutoscanHandler(&fakeAutoscanStore{deleteSourceFn: func(id string) error { ids = append(ids, id); return failure }}, new(fakeAutoscanTriggerer))
	if err := h.DeleteAdminAutoscanSource(t.Context(), " source-a "); err != nil || len(ids) != 1 || ids[0] != "source-a" {
		t.Fatal(err, ids)
	}
	for _, err := range []error{autoscan.ErrNotFound, errors.New("in use by source")} {
		failure = err
		if got := h.DeleteAdminAutoscanSource(t.Context(), "source-a"); !errors.Is(got, err) {
			t.Fatal(got)
		}
	}
	if err := (*AutoscanHandler)(nil).DeleteAdminAutoscanSource(t.Context(), "source-a"); !errors.Is(err, ErrAdminAutoscanSourceDeleteUnavailable) {
		t.Fatal(err)
	}
}
