package discoverrows

import "testing"

// Every built-in row's default title is keyed, and only that title: a title
// an admin changed has no key, and a row that is not built in never has one.
func TestTitleKeyFollowsBuiltInDefaultTitles(t *testing.T) {
	for _, row := range DefaultRows() {
		if TitleKey(row.ID, row.Title) == "" {
			t.Errorf("default row %q has no title key", row.ID)
		}
		if key := TitleKey(row.ID, row.Title+" (mine)"); key != "" {
			t.Errorf("renamed row %q has key %q", row.ID, key)
		}
	}
	if key := TitleKey("custom_row", "Trending Movies"); key != "" {
		t.Errorf("custom row has key %q", key)
	}
}
