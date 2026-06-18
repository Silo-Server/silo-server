package metadata

import "testing"

func TestProviderChainContentLevel(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		want        string
	}{
		{name: "movie", contentType: "movie", want: "movie"},
		{name: "movies alias", contentType: "movies", want: "movie"},
		{name: "series", contentType: "series", want: "series"},
		{name: "season uses series chain", contentType: "season", want: "series"},
		{name: "episode uses series chain", contentType: "episode", want: "series"},
		{name: "ebook uses ebook chain", contentType: "ebook", want: "ebook"},
		{name: "manga uses manga chain", contentType: " Manga ", want: "manga"},
		{name: "audiobook uses audiobook chain", contentType: "audiobook", want: "audiobook"},
		{name: "empty defaults to series", contentType: "", want: "series"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := providerChainContentLevel(tt.contentType); got != tt.want {
				t.Fatalf("providerChainContentLevel(%q) = %q, want %q", tt.contentType, got, tt.want)
			}
		})
	}
}
