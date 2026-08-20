package catalog

import "testing"

func TestNormalizeCollectionSort(t *testing.T) {
	cases := []struct {
		name              string
		field             string
		order             string
		allowPersonalized bool
		wantOK            bool
		wantField         string
		wantOrder         string
	}{
		{name: "applies the field's default order", field: "release_date", wantOK: true, wantField: "release_date", wantOrder: "desc"},
		{name: "title defaults ascending", field: "title", wantOK: true, wantField: "title", wantOrder: "asc"},
		{name: "explicit order wins", field: "title", order: "desc", wantOK: true, wantField: "title", wantOrder: "desc"},
		{name: "trims and lowercases", field: "  Title  ", order: "ASC", wantOK: true, wantField: "title", wantOrder: "asc"},
		{name: "garbage order is rejected", field: "year", order: "sideways"},
		{name: "empty field means source order", field: ""},
		{name: "unknown field is rejected", field: "not_a_sort"},
		{name: "personalized rejected for library collections", field: "progress"},
		{
			name:              "personalized allowed for personal collections",
			field:             "progress",
			allowPersonalized: true,
			wantOK:            true,
			wantField:         "progress",
			wantOrder:         "desc",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			qs, ok := NormalizeCollectionSort(tc.field, tc.order, tc.allowPersonalized)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if qs.Field != tc.wantField || qs.Order != tc.wantOrder {
				t.Fatalf("got %q/%q, want %q/%q", qs.Field, qs.Order, tc.wantField, tc.wantOrder)
			}
		})
	}
}

func TestParseCollectionDefaultSort(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		wantOK    bool
		wantField string
		wantOrder string
	}{
		{name: "empty column", raw: ""},
		{name: "legacy empty object", raw: `{}`},
		{name: "json null", raw: `null`},
		{name: "unparseable json", raw: `{`},
		// The unimplemented manual-pin mode carries no field, so it must not be
		// mistaken for a configured sort.
		{name: "manual pins mode", raw: `{"mode":"manual_pins"}`},
		{name: "configured sort", raw: `{"field":"title","order":"asc"}`, wantOK: true, wantField: "title", wantOrder: "asc"},
		{name: "order filled in", raw: `{"field":"added_at"}`, wantOK: true, wantField: "added_at", wantOrder: "desc"},
		{name: "invalid order is rejected", raw: `{"field":"title","order":"sideways"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			qs, ok := ParseCollectionDefaultSort([]byte(tc.raw), false)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if qs.Field != tc.wantField || qs.Order != tc.wantOrder {
				t.Fatalf("got %q/%q, want %q/%q", qs.Field, qs.Order, tc.wantField, tc.wantOrder)
			}
		})
	}
}

func TestEncodeCollectionDefaultSort(t *testing.T) {
	encoded, err := EncodeCollectionDefaultSort(QuerySort{Field: "title", Order: "asc"}, true)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if encoded != `{"field":"title","order":"asc"}` {
		t.Fatalf("encoded = %s", encoded)
	}

	// Source order round-trips through the empty object the column defaults to.
	encoded, err = EncodeCollectionDefaultSort(QuerySort{}, false)
	if err != nil {
		t.Fatalf("encoding source order: %v", err)
	}
	if encoded != "{}" {
		t.Fatalf("source order encoded = %s, want {}", encoded)
	}
	if _, ok := ParseCollectionDefaultSort([]byte(encoded), false); ok {
		t.Fatal("re-parsing the source-order encoding reported a configured sort")
	}
}
