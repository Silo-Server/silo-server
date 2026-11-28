package handlers

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/collectionutil"
	"github.com/Silo-Server/silo-server/internal/mdblist"
)

type checkedMDBListLists struct{ err error }

func (c checkedMDBListLists) Check(context.Context, int, string) error { return c.err }

func (checkedMDBListLists) AccountLists(context.Context, int, string) ([]mdblist.AccountList, error) {
	return nil, nil
}

// The collection editor offers a profile its own MDBList lists when it can
// read them, and tells it to connect MDBList only when a connection is all
// that is missing: an old plugin or a rate-limited key is not fixed that way.
func TestProfileMDBListAccountListsState(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		lists       ProfileMDBListLists
		profile     string
		available   bool
		connectable bool
	}{
		"connected":           {lists: checkedMDBListLists{}, profile: "p1", available: true},
		"not connected":       {lists: checkedMDBListLists{err: collectionutil.ErrMDBListNoConnection}, profile: "p1", connectable: true},
		"plugin too old":      {lists: checkedMDBListLists{err: collectionutil.ErrMDBListProviderTooOld}, profile: "p1"},
		"rate limited":        {lists: checkedMDBListLists{err: &collectionutil.MDBListReadError{Kind: collectionutil.MDBListRateLimited}}, profile: "p1"},
		"no profile":          {lists: checkedMDBListLists{}, profile: ""},
		"no reader installed": {profile: "p1"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := &UserCollectionImportHandler{ProfileMDBListLists: tc.lists}
			available, connectable := h.ProfileMDBListAccountListsState(context.Background(), 7, tc.profile)
			if available != tc.available || connectable != tc.connectable {
				t.Fatalf("state = (%t, %t), want (%t, %t)", available, connectable, tc.available, tc.connectable)
			}
		})
	}
}
