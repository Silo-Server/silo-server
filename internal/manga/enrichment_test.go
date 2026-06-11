package manga

import (
	"strings"
	"testing"
)

func TestClaimBatchQueryTargetsManga(t *testing.T) {
	if !strings.Contains(claimBatchQuery, "mi.type = 'manga'") {
		t.Fatalf("claimBatchQuery must filter type='manga'")
	}
	if strings.Contains(claimBatchQuery, "'ebook'") {
		t.Fatalf("claimBatchQuery must not reference ebook")
	}
	if !strings.Contains(claimBatchQuery, "manga_enrichment_state") {
		t.Fatalf("claimBatchQuery must join manga_enrichment_state")
	}
}

func TestContentTypeIsManga(t *testing.T) {
	if got := mangaContentType(); got != "manga" {
		t.Fatalf("mangaContentType() = %q, want %q", got, "manga")
	}
}
