package requests

// BundledStudio is a curated movie studio surfaced in the request discover
// section. The TMDB ID identifies the company in /discover/movie?with_companies=.
type BundledStudio struct {
	TMDBID      int
	Slug        string
	DisplayName string
	BrandColor  string
}

// BundledNetwork is a curated TV network surfaced in the request discover
// section. The TMDB ID identifies the network in /discover/tv?with_networks=.
type BundledNetwork struct {
	TMDBID      int
	Slug        string
	DisplayName string
	BrandColor  string
}

// BundledGenre is a curated genre. MovieID is the TMDB movie genre ID;
// SeriesID is the TMDB tv genre ID, or 0 when no TV equivalent exists.
type BundledGenre struct {
	Slug         string
	DisplayName  string
	GradientFrom string
	GradientTo   string
	MovieID      int
	SeriesID     int
}

// BundledStudios is the compile-time list of studios shown in the Studios
// carousel. Order is preservation order; render in this order.
var BundledStudios = []BundledStudio{
	{TMDBID: 2, Slug: "walt-disney-pictures", DisplayName: "Walt Disney Pictures", BrandColor: "#003087"},
	{TMDBID: 3, Slug: "pixar", DisplayName: "Pixar", BrandColor: "#0a85ca"},
	{TMDBID: 420, Slug: "marvel-studios", DisplayName: "Marvel Studios", BrandColor: "#ed1d24"},
	{TMDBID: 1, Slug: "lucasfilm", DisplayName: "Lucasfilm", BrandColor: "#000000"},
	{TMDBID: 174, Slug: "warner-bros-pictures", DisplayName: "Warner Bros. Pictures", BrandColor: "#004c97"},
	{TMDBID: 33, Slug: "universal-pictures", DisplayName: "Universal Pictures", BrandColor: "#1f2a44"},
	{TMDBID: 4, Slug: "paramount-pictures", DisplayName: "Paramount Pictures", BrandColor: "#0066b3"},
	{TMDBID: 5, Slug: "sony-pictures", DisplayName: "Sony Pictures", BrandColor: "#bf2f38"},
	{TMDBID: 25, Slug: "20th-century-studios", DisplayName: "20th Century Studios", BrandColor: "#000000"},
	{TMDBID: 10342, Slug: "studio-ghibli", DisplayName: "Studio Ghibli", BrandColor: "#1a4d2e"},
}

// BundledNetworks is the compile-time list of networks shown in the Networks
// carousel. Order is preservation order.
var BundledNetworks = []BundledNetwork{
	{TMDBID: 213, Slug: "netflix", DisplayName: "Netflix", BrandColor: "#e50914"},
	{TMDBID: 2739, Slug: "disney-plus", DisplayName: "Disney+", BrandColor: "#0e3c7d"},
	{TMDBID: 2552, Slug: "apple-tv-plus", DisplayName: "Apple TV+", BrandColor: "#000000"},
	{TMDBID: 49, Slug: "hbo", DisplayName: "HBO", BrandColor: "#000000"},
	{TMDBID: 453, Slug: "hulu", DisplayName: "Hulu", BrandColor: "#1ce783"},
	{TMDBID: 1024, Slug: "amazon-prime-video", DisplayName: "Amazon Prime Video", BrandColor: "#00a8e1"},
	{TMDBID: 3186, Slug: "max", DisplayName: "Max", BrandColor: "#002be7"},
	{TMDBID: 4330, Slug: "paramount-plus", DisplayName: "Paramount+", BrandColor: "#0064ff"},
	{TMDBID: 4, Slug: "bbc", DisplayName: "BBC", BrandColor: "#000000"},
	{TMDBID: 88, Slug: "fx", DisplayName: "FX", BrandColor: "#000000"},
}

// BundledGenres is the compile-time list of genres shown in the Genres
// carousel. SeriesID = 0 means the genre has no direct TV equivalent and
// the browse page hides the Series tab.
var BundledGenres = []BundledGenre{
	{Slug: "action", DisplayName: "Action", GradientFrom: "#dc2626", GradientTo: "#7f1d1d", MovieID: 28, SeriesID: 10759},
	{Slug: "comedy", DisplayName: "Comedy", GradientFrom: "#fbbf24", GradientTo: "#b45309", MovieID: 35, SeriesID: 35},
	{Slug: "drama", DisplayName: "Drama", GradientFrom: "#64748b", GradientTo: "#1e293b", MovieID: 18, SeriesID: 18},
	{Slug: "sci-fi", DisplayName: "Sci-Fi", GradientFrom: "#7c3aed", GradientTo: "#312e81", MovieID: 878, SeriesID: 10765},
	{Slug: "horror", DisplayName: "Horror", GradientFrom: "#7f1d1d", GradientTo: "#1f2937", MovieID: 27, SeriesID: 0},
	{Slug: "romance", DisplayName: "Romance", GradientFrom: "#ec4899", GradientTo: "#831843", MovieID: 10749, SeriesID: 0},
	{Slug: "animation", DisplayName: "Animation", GradientFrom: "#06b6d4", GradientTo: "#155e75", MovieID: 16, SeriesID: 16},
	{Slug: "documentary", DisplayName: "Documentary", GradientFrom: "#475569", GradientTo: "#0f172a", MovieID: 99, SeriesID: 99},
}

// FindStudioBySlug looks up a bundled studio by slug. Returns (zero, false)
// if not found.
func FindStudioBySlug(slug string) (BundledStudio, bool) {
	for _, s := range BundledStudios {
		if s.Slug == slug {
			return s, true
		}
	}
	return BundledStudio{}, false
}

// FindNetworkBySlug looks up a bundled network by slug.
func FindNetworkBySlug(slug string) (BundledNetwork, bool) {
	for _, n := range BundledNetworks {
		if n.Slug == slug {
			return n, true
		}
	}
	return BundledNetwork{}, false
}

// FindGenreBySlug looks up a bundled genre by slug.
func FindGenreBySlug(slug string) (BundledGenre, bool) {
	for _, g := range BundledGenres {
		if g.Slug == slug {
			return g, true
		}
	}
	return BundledGenre{}, false
}
