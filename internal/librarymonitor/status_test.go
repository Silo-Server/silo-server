package librarymonitor

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// A library whose second path has no root state yet (its attempt was not
// spawned, or it was just added) must still name the path its first root's
// detail belongs to.
func TestStatusRowsNameThePathWhileAnotherHasNoState(t *testing.T) {
	m, err := New(Config{Folders: &fakeFolders{}, Queue: newFakeQueue(), Logger: quietLogger()})
	if err != nil {
		t.Fatal(err)
	}
	lib := library(1, "/a", "/b", "/a/")
	m.desiredOrder = []*models.MediaFolder{lib}
	m.roots["/a"] = &rootState{path: "/a", state: StateMonitoring, backendName: BackendInotify, notes: []string{"Note."}}
	rows := m.statusRowsLocked()
	if len(rows) != 1 || rows[0].Detail != "/a: Note." {
		t.Fatalf("rows = %+v, want one row whose detail names /a", rows)
	}
	m.roots["/b"] = &rootState{path: "/b", state: StateMonitoring, backendName: BackendInotify, notes: []string{"Note."}}
	if rows := m.statusRowsLocked(); rows[0].Detail != "Note." {
		t.Fatalf("detail = %q once both paths share it, want it without paths", rows[0].Detail)
	}
}
