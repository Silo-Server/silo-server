package catalog

import "testing"

func TestParseCatalogMediaScope_AllowsEbook(t *testing.T) {
	if got := parseCatalogMediaScope(" ebook "); got != "ebook" {
		t.Fatalf("expected ebook media scope, got %q", got)
	}
}
