package handlers

import (
	"encoding/json"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestNormalizePersonalSortPreferenceRequest(t *testing.T) {
	for _, kind := range []string{userstore.CollectionKindWatchlist, userstore.CollectionKindFavorites} {
		if gotKind, gotID, ok := normalizeCollectionRef(kind, "ignored"); !ok || gotKind != kind || gotID != userstore.PersonalSortPreferenceCollectionID {
			t.Fatalf("normalizeCollectionRef(%q) = %q/%q/%v", kind, gotKind, gotID, ok)
		}
		for _, valid := range []struct {
			field, wantOrder string
		}{
			{field: "added_at", wantOrder: "desc"},
			{field: "last_air_date", wantOrder: "desc"},
			{field: "runtime", wantOrder: "desc"},
		} {
			if field, order, ok := normalizeSortPreference(kind, valid.field, ""); !ok || field != valid.field || order != valid.wantOrder {
				t.Fatalf("normalizeSortPreference(%q, %s) = %q/%q/%v", kind, valid.field, field, order, ok)
			}
		}
		for _, invalid := range []struct{ field, order string }{
			{field: "progress"},
			{field: "relevance"},
			{field: "not_a_sort"},
			{field: "title", order: "sideways"},
		} {
			if _, _, ok := normalizeSortPreference(kind, invalid.field, invalid.order); ok {
				t.Fatalf("normalizeSortPreference(%q, %q, %q) accepted invalid sort", kind, invalid.field, invalid.order)
			}
		}
	}
}

// TestNormalizeCollectionSortConfigForCreators covers the validation the
// collection create/update handlers apply to a creator-configured default.
func TestNormalizeCollectionSortConfigForCreators(t *testing.T) {
	cases := []struct {
		name              string
		raw               string
		allowPersonalized bool
		want              string
		wantErr           bool
	}{
		{name: "absent", raw: "", want: "{}"},
		{name: "explicit null", raw: `null`, want: "{}"},
		{name: "legacy empty object", raw: `{}`, want: "{}"},
		{name: "empty field remains source order", raw: `{"field":""}`, want: `{"field":""}`},
		{name: "preserves manual pins mode", raw: `{"mode":"manual_pins"}`, want: `{"mode":"manual_pins"}`},
		{name: "canonicalizes", raw: `{"field":"  Title ","order":"ASC"}`, want: `{"field":"title","order":"asc"}`},
		{name: "fills the default order", raw: `{"field":"year"}`, want: `{"field":"year","order":"desc"}`},
		{name: "rejects unknown fields", raw: `{"field":"not_a_sort"}`, wantErr: true},
		{name: "rejects invalid orders", raw: `{"field":"year","order":"sideways"}`, wantErr: true},
		{name: "rejects personalized for library", raw: `{"field":"progress"}`, wantErr: true},
		{
			name:              "allows personalized for personal",
			raw:               `{"field":"progress"}`,
			allowPersonalized: true,
			want:              `{"field":"progress","order":"desc"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeCollectionSortConfig(json.RawMessage(tc.raw), tc.allowPersonalized)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}
