package discoverrows

import "testing"

// An MDBList row stores the list page link: /json comes off the path, and a
// query such as sort=rank stays.
func TestMDBListRowStoresListPageLink(t *testing.T) {
	const list = "https://mdblist.com/lists/example-user/watchlist"
	for raw, want := range map[string]string{
		list + "/json":           list,
		list + "?sort=rank":      list + "?sort=rank",
		list + "/json?sort=rank": list + "?sort=rank",
	} {
		row, errs := NormalizeRow(Row{Type: TypeMDBList, Title: "Watchlist", List: &ListSpec{URL: raw}})
		if len(errs) > 0 {
			t.Fatalf("NormalizeRow(%q) errors: %v", raw, errs)
		}
		if row.List.URL != want {
			t.Errorf("NormalizeRow(%q) stored %q, want %q", raw, row.List.URL, want)
		}
	}
}
