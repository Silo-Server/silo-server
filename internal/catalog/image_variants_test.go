package catalog

import "testing"

func TestImageTypeFromCachedPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{"episode still", "tvdb/series/73141/seasons/22/episodes/9/still/original.webp", "still"},
		{"movie backdrop", "tmdb/movies/550/backdrop/original.webp", "backdrop"},
		{"leading slash poster", "/tmdb/movies/550/poster/original.jpg", "poster"},
		{"logo", "tmdb/movies/550/logo/original.png", "logo"},
		{"http url", "https://images.example.com/backdrop/original.jpg", ""},
		{"plugin path", "plugin://tmdb/backdrop/original.jpg", ""},
		{"no slash", "original.webp", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := imageTypeFromCachedPath(tt.path); got != tt.want {
				t.Fatalf("imageTypeFromCachedPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestBackdropVariantPath(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		desired string
		want    string
	}{
		{
			name:    "real backdrop keeps requested w1280",
			path:    "tmdb/movies/550/backdrop/original.webp",
			desired: "w1280",
			want:    "tmdb/movies/550/backdrop/w1280.webp",
		},
		{
			name:    "real backdrop keeps requested w1920",
			path:    "/tmdb/shows/1399/backdrop/original.jpg",
			desired: "w1920",
			want:    "/tmdb/shows/1399/backdrop/w1920.jpg",
		},
		{
			name:    "episode still clamps to w500 (no large variant exists)",
			path:    "tvdb/series/73141/seasons/22/episodes/9/still/original.webp",
			desired: "w1280",
			want:    "tvdb/series/73141/seasons/22/episodes/9/still/w500.webp",
		},
		{
			name:    "episode still clamps w1920 to w500",
			path:    "tvdb/series/73141/seasons/22/episodes/9/still/original.webp",
			desired: "w1920",
			want:    "tvdb/series/73141/seasons/22/episodes/9/still/w500.webp",
		},
		{
			name:    "http url passes through",
			path:    "https://images.example.com/backdrop/original.jpg",
			desired: "w1280",
			want:    "https://images.example.com/backdrop/original.jpg",
		},
		{
			name:    "plugin path passes through",
			path:    "plugin://tmdb/backdrop/original.jpg",
			desired: "w1280",
			want:    "plugin://tmdb/backdrop/original.jpg",
		},
		{
			name:    "path without original segment passes through",
			path:    "tmdb/movies/550/backdrop/w300.webp",
			desired: "w1280",
			want:    "tmdb/movies/550/backdrop/w300.webp",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BackdropVariantPath(tt.path, tt.desired); got != tt.want {
				t.Fatalf("BackdropVariantPath(%q, %q) = %q, want %q", tt.path, tt.desired, got, tt.want)
			}
		})
	}
}
