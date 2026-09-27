package handlers

import (
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/autoscan"
)

func TestReadAdminAutoscanEvents(t *testing.T) {
	filter := autoscan.EventListFilter{Status: "running", Search: "needle", Limit: 7, Offset: 14}
	listed := false
	counted := false
	var failure error
	store := &fakeAutoscanStore{listEventsFn: func(got autoscan.EventListFilter) ([]autoscan.EventWithRuns, error) {
		if got != filter {
			t.Fatal(got)
		}
		listed = true
		return []autoscan.EventWithRuns{{Event: autoscan.Event{ID: 7}}}, failure
	}, countEventsFn: func(got autoscan.EventListFilter) (int, error) {
		if !listed || got != filter {
			t.Fatal(got)
		}
		counted = true
		return 99, nil
	}}
	h := NewAutoscanHandler(store, new(fakeAutoscanTriggerer))
	rows, total, err := h.ReadAdminAutoscanEvents(t.Context(), filter)
	if err != nil || len(rows) != 1 || total != 99 || !counted {
		t.Fatal(rows, total, err)
	}
	counted = false
	failure = errors.New("list failed")
	_, _, err = h.ReadAdminAutoscanEvents(t.Context(), filter)
	if !errors.Is(err, failure) || counted {
		t.Fatal(err, counted)
	}
	_, _, err = (*AutoscanHandler)(nil).ReadAdminAutoscanEvents(t.Context(), filter)
	if !errors.Is(err, ErrAdminAutoscanEventsUnavailable) {
		t.Fatal(err)
	}
}
