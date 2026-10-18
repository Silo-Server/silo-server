package metadata

import (
	"slices"
	"testing"
)

// TMDB reports origin countries as alpha-2 ("US") and TVDB as lowercase
// alpha-3 ("usa"). The merge keeps both spellings, so the stored item must
// collapse them after canonicalization instead of saving ["US", "US"].
func TestMetadataResultToItem_CollapsesProviderCountrySpellings(t *testing.T) {
	target := &MetadataResult{HasMetadata: true, Title: "Breaking Bad", Countries: []string{"US"}}
	MergeMetadata(&MetadataResult{Countries: []string{"usa"}}, target, nil, MergeFillEmpty)

	item := metadataResultToItem(target, "series")
	if want := []string{"US"}; !slices.Equal(item.Countries, want) {
		t.Fatalf("countries = %v, want %v", item.Countries, want)
	}
}
