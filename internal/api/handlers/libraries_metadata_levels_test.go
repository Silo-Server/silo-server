package handlers

import (
	"reflect"
	"testing"
)

func TestMetadataContentLevelsForLibraryTypeIncludesEbooks(t *testing.T) {
	cases := []struct {
		name        string
		libraryType string
		want        []string
	}{
		{name: "plural ebooks", libraryType: "ebooks", want: []string{"ebook"}},
		{name: "singular ebook", libraryType: "ebook", want: []string{"ebook"}},
		{name: "mixed includes ebook", libraryType: "mixed", want: []string{"movie", "series", "season", "episode", "audiobook", "ebook"}},
		{name: "manga", libraryType: "manga", want: []string{"manga"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := metadataContentLevelsForLibraryType(tc.libraryType); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("metadataContentLevelsForLibraryType(%q) = %#v, want %#v", tc.libraryType, got, tc.want)
			}
		})
	}
}
