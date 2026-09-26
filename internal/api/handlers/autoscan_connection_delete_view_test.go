package handlers

import (
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/autoscan"
)

func TestDeleteAdminAutoscanConnection(t *testing.T) {
	var ids []string
	var failure error
	h := NewAutoscanHandler(&fakeAutoscanStore{deleteConnectionFn: func(id string) error { ids = append(ids, id); return failure }}, new(fakeAutoscanTriggerer))
	if err := h.DeleteAdminAutoscanConnection(t.Context(), " connection-a "); err != nil || len(ids) != 1 || ids[0] != "connection-a" {
		t.Fatal(err, ids)
	}
	for _, err := range []error{autoscan.ErrNotFound, errors.New("in use by source")} {
		failure = err
		if got := h.DeleteAdminAutoscanConnection(t.Context(), "connection-a"); !errors.Is(got, err) {
			t.Fatal(got)
		}
	}
	if err := (*AutoscanHandler)(nil).DeleteAdminAutoscanConnection(t.Context(), "connection-a"); !errors.Is(err, ErrAdminAutoscanConnectionDeleteUnavailable) {
		t.Fatal(err)
	}
}
