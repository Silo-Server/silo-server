package catalog

import (
	"net/url"
	"testing"
)

func TestParseCatalogMediaScope_AllowsEbook(t *testing.T) {
	if got := parseCatalogMediaScope(" ebook "); got != "ebook" {
		t.Fatalf("expected ebook media scope, got %q", got)
	}
}

func TestParseCatalogMediaScope_AllowsManga(t *testing.T) {
	if got := parseCatalogMediaScope(" manga "); got != "manga" {
		t.Fatalf("expected manga media scope, got %q", got)
	}
}

func TestParseCatalogRequest_AllowsCollectionOverlayParams(t *testing.T) {
	values := url.Values{
		"source":                     {"user_collection"},
		"collection_id":              {"col-7"},
		"type":                       {"movie"},
		"sort":                       {"title"},
		"order":                      {"asc"},
		"groups[0][match]":           {"all"},
		"groups[0][rules][0][field]": {"watched"},
		"groups[0][rules][0][op]":    {"is"},
		"groups[0][rules][0][value]": {"false"},
		"groups[1][match]":           {"all"},
		"groups[1][rules][0][field]": {"resolution"},
		"groups[1][rules][0][op]":    {"is"},
		"groups[1][rules][0][value]": {"4K"},
	}

	req, err := ParseCatalogRequest(values)
	if err != nil {
		t.Fatalf("ParseCatalogRequest returned error: %v", err)
	}
	if req.Source != CatalogSourceUserCollection || req.CollectionID != "col-7" {
		t.Fatalf("unexpected collection source: source=%q collection=%q", req.Source, req.CollectionID)
	}
	if req.UseSourceOrder {
		t.Fatal("explicit sort should opt out of source order")
	}
	if req.Query.MediaScope != "movie" {
		t.Fatalf("media scope = %q, want movie", req.Query.MediaScope)
	}
	if req.Query.Sort != (QuerySort{Field: "title", Order: "asc"}) {
		t.Fatalf("sort = %+v, want title asc", req.Query.Sort)
	}
	if len(req.Query.Groups) != 2 {
		t.Fatalf("groups = %+v, want two overlay groups", req.Query.Groups)
	}
}
