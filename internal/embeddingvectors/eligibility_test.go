package embeddingvectors

import "testing"

func TestItemEligibilityWhereClauseMatchesLegacy(t *testing.T) {
	for _, tc := range []struct{ alias, want string }{
		{"mi", "(mi.status = 'matched' OR mi.type = 'audiobook' OR mi.type = 'ebook')"},
		{"media_items", "(media_items.status = 'matched' OR media_items.type = 'audiobook' OR media_items.type = 'ebook')"},
		{"  e  ", "(e.status = 'matched' OR e.type = 'audiobook' OR e.type = 'ebook')"},
		{"", "(media_items.status = 'matched' OR media_items.type = 'audiobook' OR media_items.type = 'ebook')"},
		{"   ", "(media_items.status = 'matched' OR media_items.type = 'audiobook' OR media_items.type = 'ebook')"},
	} {
		if got := ItemEligibilityWhereClause(tc.alias); got != tc.want {
			t.Fatalf("ItemEligibilityWhereClause(%q) = %q, want %q", tc.alias, got, tc.want)
		}
	}
}
